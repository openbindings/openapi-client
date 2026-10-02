package openapi

import (
	"cmp"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// A document is a loaded OpenAPI description: its entry document's tree,
// every document its references reach, what they identify, and the index of
// its operations. Derived Clients share it.
type document struct {
	*tree          // the entry document's
	version string // the entry's
	dialect string // the entry's jsonSchemaDialect
	schemes SchemeLookup

	trees  []*tree           // every document loaded: the entry, then the others by URI
	named  map[string]*tree  // by the URI each was retrieved from, and the URI requested when a redirect led there
	ids    map[string]*claim // by each $id, and each base and plain name, the schemas declare
	failed map[string]error  // why each document that could not be loaded was not, by the URI requested

	resolutions sync.Map // resolving to *resolved (see resolve)

	entries []*entry          // in document order
	byID    map[string]*entry // by operationId; nil for one several operations share
	byRoute map[route]*entry
	broken  map[string]*entry // Paths entries that cannot be read, by path

	// What the document compiles to, each read without a lock and computed
	// unlocked, the first result published being the one every reader sees:
	// concurrent first uses may compute one twice, but none waits on another.
	inherits atomic.Pointer[inheritance]        // what operations inherit from the root
	walks    sync.Map                           // reflect.Type to *walk, for finding readers in bodies
	refs     sync.Map                           // node to the *resolution of the Reference Object there
	pages    []atomic.Pointer[[factsPage]facts] // what schema nodes compile to, a page made as first used
	types    sync.Map                           // a list of schemas to the mediaSet of a field they type
	forms    sync.Map                           // schemas' state to the formEncoding of a nested part they type

	// What nodes shared by several places compile to, by node.
	paramForms  memo[param]
	bodies      memo[*content] // a Request Body Object's
	contents    memo[*content] // a Response Object's
	serverLists memo[*serverList]
	schemeNames memo[*scheme] // by the securitySchemes member a name selects
	schemeForms memo[*scheme] // by the Security Scheme Object a reference reaches
}

// A memo keeps what each node of a document compiles to.
type memo[T any] struct{ m sync.Map }

// get returns what compile makes of node i (see value.id): the first result
// published.
func (m *memo[T]) get(i int32, compile func() T) T {
	if v, ok := m.m.Load(i); ok {
		return v.(T)
	}
	v, _ := m.m.LoadOrStore(i, compile())
	return v.(T)
}

type route struct{ method, path string }

// The fixed methods of an OpenAPI 3.1 Path Item, in the order Operations
// lists them.
var methods = [...]struct{ name, upper string }{
	{"get", "GET"}, {"put", "PUT"}, {"post", "POST"}, {"delete", "DELETE"},
	{"options", "OPTIONS"}, {"head", "HEAD"}, {"patch", "PATCH"}, {"trace", "TRACE"},
}

// An entry is an indexed operation, or a Paths entry that cannot be read.
// Its descriptor and plan are compiled on first use.
type entry struct {
	doc    *document
	path   string
	id     string
	m      int      // the method, an index into methods; -1 for a Paths entry that cannot be read
	node   value    // the Operation Object
	levels *level   // the Path Item chain
	sum    *summary // what its levels define, shared with every chain that defines the same
	err    error    // why the entry cannot be read, or its method is defined twice
	group  *group   // the entries sharing its Operation Object and inherited fields, or nil
	op     atomic.Pointer[operation]
}

// A group is the entries of several Paths entries that reach one Operation
// Object through one Path Item chain, and what it compiles to for all of
// them.
type group struct {
	op atomic.Pointer[operation]
}

// A level is one Path Item of a chain: the Paths entry, then each $ref
// target in turn. A target's level, and the rest of the chain from it, are
// shared by every chain through it.
type level struct {
	v    value
	ptr  string
	next *level
}

// A summary says which level of a Path Item chain defines each field the
// client reads, each method's and then parameters and servers: the nearest
// that does, a bit in dup saying a farther one does too. Nil summarizes a
// chain that defines none.
type summary struct {
	at  [serversField + 1]*level
	dup uint16
}

// The Path Item fields a summary covers after the methods.
const (
	parametersField = len(methods) + iota
	serversField
)

// add returns the summary of the chain from l, whose rest s summarizes: s
// itself when l defines none of the fields.
func (s *summary) add(l *level) *summary {
	sum := s
	for name, v := range l.v.members() {
		f, present := -1, v.kind() == '{'
		for m := range methods {
			if methods[m].name == name {
				f = m
			}
		}
		switch name {
		case "parameters":
			f, present = parametersField, true
		case "servers":
			f, present = serversField, v.hasMembers()
		}
		if f < 0 || !present {
			continue
		}
		if sum == s {
			sum = new(summary)
			if s != nil {
				*sum = *s
			}
		}
		if sum.at[f] != nil {
			sum.dup |= 1 << f
		}
		sum.at[f] = l
	}
	return sum
}

// compile completes the entry's descriptor and plan: the first published.
func (e *entry) compile() *operation {
	return loadOrMake(&e.op, e.build)
}

// loadOrMake returns p's value, made by build and stored if p has none yet,
// the first value stored being kept.
func loadOrMake[T any](p *atomic.Pointer[T], build func() *T) *T {
	if v := p.Load(); v != nil {
		return v
	}
	p.CompareAndSwap(nil, build())
	return p.Load()
}

// source returns the Operation Object's Source.
func (e *entry) source() string {
	l := e.sum.at[e.m]
	return l.v.t.source(l.ptr + "/" + methods[e.m].name)
}

// checkURI refuses a document URI with userinfo or a fragment.
func checkURI(u *url.URL, raw string) error {
	switch {
	case u.User != nil:
		return errors.New("a document URI cannot hold userinfo (RFC 9110 section 4.2.4)")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return errors.New("a document URI cannot hold a fragment")
	}
	return nil
}

// retrieve returns the content of the document at uri, the URI it was
// finally retrieved from, and that URI parsed, or nil, copying the content
// through buf, if not nil. from is "" for the entry document; otherwise it
// names the document whose references reach uri, which must admit it, and
// every redirect hop and the final URI.
func (ld *loading) retrieve(uri, from string, buf []byte) (string, string, *url.URL, error) {
	var (
		r     io.ReadCloser
		final = uri
		size  = int64(-1)
		ctx   = ld.ctx
	)
	u, err := url.Parse(uri)
	switch {
	case strings.TrimSpace(uri) != uri: // not shown: it may be a URI with userinfo
		return "", "", nil, errors.New("openapi: load: the URI has leading or trailing whitespace")
	case err != nil && hasScheme(uri): // shown neither, as its userinfo cannot be found
		return "", "", nil, errors.New("openapi: load: the URI cannot be parsed (RFC 3986)")
	case err != nil || len(u.Scheme) <= 1: // not a URL: a file path, perhaps with a drive letter
		u, err = &url.URL{}, nil
	default:
		err = checkURI(u, uri)
	}
	base := u // final, parsed
	switch {
	case err != nil:
	case from != "" && !ld.admit(from, uri, u):
		err = notAdmitted(uri)
	case ld.Fetch != nil:
		if r, final, err = ld.Fetch(ctx, uri); final == "" || final == uri {
			final = uri
		} else if base = nil; err == nil && from != "" && !ld.admit(from, final, nil) {
			err = notAdmitted(final)
		}
	case u.Scheme == "http" || u.Scheme == "https":
		hc := ld.hc
		if from != "" { // a copy that checks each hop
			c, next := *hc, hc.CheckRedirect
			if next == nil {
				next = tenRedirects
			}
			c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				to := *req.URL
				if to.Fragment, to.RawFragment = "", ""; !ld.admit(from, to.String(), &to) {
					return notAdmitted(to.String())
				}
				return next(req, via)
			}
			hc = &c
		}
		var req *http.Request
		var resp *http.Response
		if req, err = http.NewRequestWithContext(ctx, "GET", uri, nil); err == nil {
			if resp, err = hc.Do(req); err == nil {
				r, size = resp.Body, resp.ContentLength
				if resp.Request != nil {
					to := *resp.Request.URL
					to.Fragment, to.RawFragment = "", ""
					final, base = to.String(), &to
				}
				if resp.StatusCode/100 != 2 {
					err = errors.New(status(resp.StatusCode))
				}
			}
		}
	case u.Scheme == "file" && u.Host != "" && u.Host != "localhost":
		err = errors.New("a file URL cannot name a host other than localhost (RFC 8089)")
	case u.Scheme == "file":
		r, size, err = open(ctx, filepath.FromSlash(u.Path))
	case u.Scheme == "":
		var abs string
		if abs, err = filepath.Abs(uri); err == nil {
			base = &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
			final = base.String()
			r, size, err = open(ctx, abs)
		}
	default:
		err = fmt.Errorf("unsupported URI scheme %q", u.Scheme)
	}
	if r != nil {
		defer r.Close()
	}
	if err == nil {
		var b strings.Builder
		b.Grow(int(min(max(size, 0), max(ld.left.Load(), 0), 1<<20)))
		if _, err = io.CopyBuffer(&b, counted{ld, ctxReader{ctx, r}}, buf); err == nil {
			return b.String(), final, base, nil
		}
	}
	shown := uri
	if u.Scheme != "" {
		shown = u.Redacted()
	}
	return "", "", nil, fmt.Errorf("openapi: load %s: %w", shown, withContext(ctx, err))
}

// hasScheme reports whether s begins with a URI scheme longer than a drive
// letter.
func hasScheme(s string) bool {
	i := strings.IndexByte(s, ':')
	return i > 1 && isScheme(s[:i])
}

// isScheme reports whether s is a URI scheme (RFC 3986 section 3.1).
func isScheme(s string) bool {
	for j := range len(s) {
		if c := s[j] | 0x20; !('a' <= c && c <= 'z' || j > 0 && strings.IndexByte("0123456789+-.", s[j]) >= 0) {
			return false
		}
	}
	return s != ""
}

// A ctxReader reads r until ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// open opens the file at path, returning its size, or -1 when it is not a
// regular file. Opening or reading such a file, a FIFO or a device, may
// block: a goroutine does both, writing to a pipe that the end of ctx
// closes with the context's error, and closing the file.
func open(ctx context.Context, path string) (io.ReadCloser, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	if fi.Mode().IsRegular() {
		f, err := os.Open(path)
		if err != nil {
			return nil, 0, err
		}
		return f, fi.Size(), nil
	}
	pr, pw := io.Pipe()
	stop := context.AfterFunc(ctx, func() { pw.CloseWithError(ctx.Err()) })
	go func() {
		defer stop()
		f, err := os.Open(path)
		if err == nil {
			defer f.Close()
			defer context.AfterFunc(ctx, func() { f.Close() })()
			_, err = io.Copy(pw, f)
		}
		pw.CloseWithError(err)
	}()
	return pr, -1, nil
}

// newDocument reads content, retrieved from uri, and every document its
// references reach, and indexes its operations.
func newDocument(ld *loading, content, uri string) (*document, error) {
	if err := ld.ctx.Err(); err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	if uri == "" {
		uri = contentURN(content)
	}
	t, err := readTree(ld.ctx, content, uri, nil)
	if err != nil {
		return nil, err
	}
	d := &document{tree: t, schemes: ld.SchemeLookup}
	root := d.root()
	d.version = root.str("openapi")
	switch {
	case !root.get("openapi").ok() && !root.get("swagger").ok():
		return nil, fmt.Errorf("openapi: %s: no openapi or swagger field", uri)
	case isPatchOf(d.version, "3.1"):
	case root.str("swagger") == "2.0" || isPatchOf(d.version, "3.0") || isPatchOf(d.version, "3.2"):
		return nil, fmt.Errorf("openapi: %s: this edition is not implemented yet: %w", uri, errors.ErrUnsupported)
	default:
		return nil, fmt.Errorf("openapi: %s: unsupported version", uri)
	}
	if !root.get("paths").ok() && !root.get("components").ok() && !root.get("webhooks").ok() {
		return nil, fmt.Errorf("openapi: %s: no paths, components or webhooks", uri)
	}
	d.dialect = root.str("jsonSchemaDialect")
	if err := d.discover(ld); err != nil {
		return nil, err
	}
	if err := d.index(ld.ctx); err != nil {
		return nil, err
	}
	return d, nil
}

func (t *tree) root() value { return value{t, 0} }

// isPatchOf reports whether version is a patch of minor, as "3.1.2" is of
// "3.1".
func isPatchOf(version, minor string) bool {
	patch, ok := strings.CutPrefix(version, minor+".")
	_, err := strconv.ParseUint(patch, 10, 32)
	return ok && err == nil
}

// contentURN names content by a version 5 UUID (RFC 9562) derived from it,
// under the nil namespace.
func contentURN(content string) string {
	h := sha1.New()
	h.Write(make([]byte, 16))
	io.WriteString(h, content)
	u := h.Sum(nil)[:16]
	u[6] = u[6]&0x0f | 0x50
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// source returns the URI naming the node at ptr: the document's, with ptr
// percent-encoded as a fragment as RFC 6901 section 6 says, every byte but
// those RFC 3986 allows in a fragment. So a child's is its parent's followed
// by its token (see token).
func (t *tree) source(ptr string) string {
	n := len(t.uri) + 1 + len(ptr)
	for i := range len(ptr) {
		if fragmentSet[ptr[i]] == 0 {
			n += 2
		}
	}
	var b strings.Builder
	b.Grow(n)
	b.WriteString(t.uri)
	b.WriteByte('#')
	escapeTo(&b, ptr, fragmentSet)
	return b.String()
}

// token returns name as the reference token that follows its parent's
// Source and a "/".
func token(name string) string { return escape(escapeToken(name), fragmentSet) }

func (d *document) index(ctx context.Context) error {
	d.byID, d.byRoute, d.broken = map[string]*entry{}, map[route]*entry{}, map[string]*entry{}
	var targets map[int32]link
	type groupKey struct {
		sum *summary
		m   int
	}
	var groups map[groupKey]*group
	for path, item := range d.root().get("paths").members() {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("openapi: %w", err)
		}
		if strings.HasPrefix(path, "x-") {
			continue
		}
		ptr := "/paths/" + escapeToken(path)
		levels, sum, err := d.chain(item, ptr, &targets)
		if err == nil && !strings.HasPrefix(path, "/") {
			err = fmt.Errorf("Paths key %q does not begin with /", path)
		}
		if err != nil {
			e := &entry{doc: d, path: path, m: -1, levels: &level{v: item, ptr: ptr}, err: err}
			d.entries = append(d.entries, e)
			d.broken[path] = e
			continue
		}
		for m := range methods {
			if sum == nil || sum.at[m] == nil {
				continue
			}
			e := d.addOperation(path, m, levels, sum)
			if levels.next != nil { // a $ref: other Paths entries may reach the same
				k := groupKey{sum, m}
				if groups[k] == nil {
					if groups == nil {
						groups = map[groupKey]*group{}
					}
					groups[k] = new(group)
				}
				e.group = groups[k]
			}
		}
	}
	return nil
}

// addOperation indexes the operation for method m of a Path Item chain,
// which sum summarizes.
func (d *document) addOperation(path string, m int, levels *level, sum *summary) *entry {
	n := sum.at[m].v.get(methods[m].name)
	e := &entry{doc: d, path: path, id: n.str("operationId"), m: m, node: n, levels: levels, sum: sum}
	if sum.dup&(1<<m) != 0 {
		e.err = fmt.Errorf("the Path Item and its $ref target both define %s", methods[m].name)
	}
	d.entries = append(d.entries, e)
	d.byRoute[route{methods[m].upper, path}] = e
	if e.id != "" && !isMethodAndPath(e.id) {
		if _, dup := d.byID[e.id]; dup {
			d.byID[e.id] = nil
		} else {
			d.byID[e.id] = e
		}
	}
	return e
}

func isMethodAndPath(key string) bool {
	_, path, ok := strings.Cut(key, " ")
	return ok && strings.HasPrefix(path, "/")
}

// lookup returns the entry key names, by the rules Call uses.
func (d *document) lookup(key string) (*entry, error) {
	if d == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoOperation, key)
	}
	if isMethodAndPath(key) {
		method, path, _ := strings.Cut(key, " ")
		if e := d.byRoute[route{method, path}]; e != nil {
			return e, nil
		}
		if e := d.broken[path]; e != nil {
			return e, nil
		}
		return nil, fmt.Errorf("%w: %q", ErrNoOperation, key)
	}
	e, found := d.byID[key]
	switch {
	case !found:
		return nil, fmt.Errorf("%w: %q", ErrNoOperation, key)
	case e != nil:
		return e, nil
	}
	var keys []string
	for _, e := range d.entries {
		if e.id == key {
			keys = append(keys, label(methods[e.m].upper+" "+e.path))
		}
	}
	return nil, fmt.Errorf("%w: %q is the operationId of %s", ErrNoOperation, key, strings.Join(keys, ", "))
}

// A link is the Path Item chain from a $ref target: the target's level,
// sharing the rest, and its summary, or why it cannot be followed. It is
// not done while the chain through it is being followed.
type link struct {
	l    *level
	sum  *summary
	err  error
	done bool
}

// chain follows the Path Item $refs from v, at ptr, returning its level,
// followed by each target's, and its summary. The chain from each target is
// followed once, and kept in *targets.
func (d *document) chain(v value, ptr string, targets *map[int32]link) (*level, *summary, error) {
	head := &level{v: v, ptr: ptr}
	var walked []*level // the targets reached, not yet kept
	var rest *summary   // the chain after the last level walked
	var err error
	for l := head; ; {
		ref := l.v.get("$ref")
		if ref.kind() != '"' {
			break
		}
		next, at, e := d.target(ref)
		if err = e; err != nil {
			break
		}
		if k, ok := (*targets)[next.id()]; ok {
			if l.next, rest, err = k.l, k.sum, k.err; !k.done {
				err = fmt.Errorf("%w %q: a reference cycle", ErrUnresolved, ref.text())
			}
			break
		}
		if *targets == nil {
			*targets = map[int32]link{}
		}
		(*targets)[next.id()] = link{}
		l.next = &level{v: next, ptr: at}
		l = l.next
		walked = append(walked, l)
	}
	for i := len(walked) - 1; i >= 0; i-- {
		if err == nil {
			rest = rest.add(walked[i])
		}
		(*targets)[walked[i].v.id()] = link{walked[i], rest, err, true}
	}
	if err != nil {
		return nil, nil, err
	}
	return head, rest.add(head), nil
}

// A resolution is where a Reference Object leads: the target, its Source
// and the description of the nearest level that gives one, or why it
// cannot be followed.
type resolution struct {
	v         value
	src, desc string
	err       error
}

// follow resolves the Reference Objects from v, whose Source is src,
// returning the target, its Source, and the description of the nearest
// level that gives one: in OpenAPI 3.1 a Reference Object's description
// replaces its target's. Each Reference Object's resolution is published
// once per document, and a chain stops at one already published. A cycle is
// named by the reference of its first Reference Object in node order, so
// that every chain into it names it alike.
func (d *document) follow(v value, src string) (value, string, string, error) {
	ref, desc, described := reference(v)
	if !ref.ok() {
		return v, src, desc, nil
	}
	type step struct {
		i         int32 // the Reference Object (see value.id)
		ref       value
		desc      string
		described bool
	}
	var walked []step     // the Reference Objects followed, not yet published
	var on map[int32]bool // walked, once a scan of it would be long
	var r resolution
	for at := ""; ; {
		i := v.id()
		if k, ok := d.refs.Load(i); ok {
			r = *k.(*resolution)
			break
		}
		if !ref.ok() {
			r = resolution{v: v, src: v.t.source(at), desc: desc}
			break
		}
		if on[i] || on == nil && slices.ContainsFunc(walked, func(s step) bool { return s.i == i }) {
			cycle := walked[slices.IndexFunc(walked, func(s step) bool { return s.i == i }):]
			first := slices.MinFunc(cycle, func(a, b step) int { return cmp.Compare(a.i, b.i) })
			r = resolution{err: fmt.Errorf("%w %q: a reference cycle", ErrUnresolved, first.ref.text())}
			break
		}
		if walked = append(walked, step{i, ref, desc, described}); len(walked) == 16 {
			on = map[int32]bool{}
			for _, s := range walked {
				on[s.i] = true
			}
		} else if on != nil {
			on[i] = true
		}
		next, nextAt, err := d.target(ref)
		if err != nil {
			r = resolution{err: err}
			break
		}
		v, at = next, nextAt
		ref, desc, described = reference(v)
	}
	for k := len(walked) - 1; k >= 0; k-- {
		if s := walked[k]; s.described && r.err == nil {
			r.desc = s.desc
		}
		kept, _ := d.refs.LoadOrStore(walked[k].i, &resolution{r.v, r.src, r.desc, r.err})
		r = *kept.(*resolution)
	}
	return r.v, r.src, r.desc, r.err
}

// reference returns v's $ref, if it is a string, and its description, if
// it gives one, read in one pass while v has few members and looked up
// otherwise.
func reference(v value) (ref value, desc string, described bool) {
	var d value
	n := 0
	for name, m := range v.members() {
		if n++; n > many {
			ref, d = v.get("$ref"), v.get("description")
			break
		}
		switch name {
		case "$ref":
			ref = m
		case "description":
			d = m
		}
	}
	if ref.kind() != '"' {
		ref = value{}
	}
	if d.kind() == '"' {
		desc, described = d.text(), true
	}
	return ref, desc, described
}

// checkNames refuses, as Load does, the names in cfg that no server,
// request body or security requirement of the document uses, and
// credentials their schemes cannot use, without compiling operations,
// stopping when ctx is done.
func (d *document) checkNames(ctx context.Context, cfg *config, re *RequestError) error {
	server, serverID := cfg.Server == "", cfg.ServerID == ""
	media := cfg.MediaType == "" || cfg.mediaTypeErr != nil
	unused := maps.Clone(cfg.Variables)
	check := func(list value) {
		for _, s := range list.members() {
			u := s.str("url")
			server = server || u == cfg.Server
			serverID = serverID || idOf(s) == cfg.ServerID
			if len(unused) > 0 {
				_, names, _ := splitTemplate(u)
				for _, name := range names {
					delete(unused, name)
				}
			}
		}
	}
	if s := d.root().get("servers"); s.hasMembers() {
		check(s)
	} else {
		server, serverID = server || cfg.Server == "/", serverID || cfg.ServerID == "default"
	}
	sec := securityCheck{d: d, re: re, creds: maps.Clone(cfg.Credentials), names: cfg.securityNames, key: cfg.SecurityKey}
	free := sec.list(d.root().get("security")) // an operation that inherits it takes no credentials
	seen := map[int32]bool{}                   // the nodes checked: Path Items, operations and request bodies
	first := func(v value) bool {
		if seen[v.id()] {
			return false
		}
		seen[v.id()] = true
		return true
	}
	for _, e := range d.entries {
		if server && serverID && media && len(unused) == 0 && sec.done() {
			break
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("openapi: %w", err)
		}
		if e.m < 0 {
			continue
		}
		for l := e.levels; l != nil && first(l.v); l = l.next { // a level checked has its rest checked
			check(l.v.get("servers"))
		}
		if !first(e.node) {
			continue
		}
		check(e.node.get("servers"))
		none := free
		if s := e.node.get("security"); s.ok() {
			none = sec.list(s)
		}
		if none && sec.key == "{}" {
			sec.key = "" // a credential-free operation offers it
		}
		if rb := e.node.get("requestBody"); !media && rb.ok() && methods[e.m].upper != "TRACE" {
			body, _, _, err := d.follow(rb, "")
			if err != nil || !first(body) {
				continue
			}
			for typ := range body.get("content").members() {
				m, ok := parseMedia(typ)
				_, covers := m.covers(cfg.mediaType)
				media = media || ok && covers
			}
		}
	}
	if !server {
		re.setting("Options.Server", fmt.Errorf("no server has the URL %q", cfg.Server))
	}
	if !serverID {
		re.setting("Options.ServerID", errors.New("no server has this ID"))
	}
	if !media {
		re.setting("Options.MediaType", fmt.Errorf("no operation declares %s", cfg.MediaType))
	}
	for name := range unused {
		re.setting("Options.Variables["+strconv.Quote(name)+"]", errors.New("no server URL uses this variable"))
	}
	sec.refuse()
	return nil
}
