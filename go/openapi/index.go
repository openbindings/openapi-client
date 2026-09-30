package openapi

import (
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
	"strconv"
	"strings"
	"sync"
)

// A document is a loaded OpenAPI document: its tree, and the index of its
// operations. Derived Clients share it.
type document struct {
	*tree
	uri     string   // the URI the document was retrieved from
	base    *url.URL // uri, parsed
	version string
	dialect string // jsonSchemaDialect

	entries []*entry          // in document order
	byID    map[string]*entry // by operationId; nil for one several operations share
	byRoute map[route]*entry
	broken  map[string]*entry // Paths entries that cannot be read, by path

	rootOnce sync.Once
	servers  *serverList           // the root servers, or the default one
	security []SecurityRequirement // the root security requirements
	walks    sync.Map              // reflect.Type to *walk, for finding readers in bodies

	refsMu sync.Mutex
	refs   map[int32]resolution // Reference Objects followed, by node

	// What nodes shared by several places compile to, by node.
	paramForms  memo[param]
	contents    memo[*content]
	serverLists memo[*serverList]
}

// A memo keeps what each node of a document compiles to.
type memo[T any] struct {
	mu    sync.Mutex
	cells map[int32]*cell[T]
}

type cell[T any] struct {
	once sync.Once
	v    T
}

// get returns what compile makes of node i, compiling it once.
func (m *memo[T]) get(i int32, compile func() T) T {
	m.mu.Lock()
	c := m.cells[i]
	if c == nil {
		if m.cells == nil {
			m.cells = map[int32]*cell[T]{}
		}
		c = new(cell[T])
		m.cells[i] = c
	}
	m.mu.Unlock()
	c.once.Do(func() { c.v = compile() })
	return c.v
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
	once   sync.Once
	op     *operation
}

// A group is the entries of several Paths entries that reach one Operation
// Object through one Path Item chain, and what it compiles to for all of
// them.
type group struct {
	once sync.Once
	op   *operation
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

// compile completes the entry's descriptor and plan once.
func (e *entry) compile() *operation {
	e.once.Do(func() { e.op = e.build() })
	return e.op
}

// ptr returns the Operation Object's JSON Pointer.
func (e *entry) ptr() string { return e.sum.at[e.m].ptr + "/" + methods[e.m].name }

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

// fetch retrieves the document at uri, returning its content and the URI it
// was finally retrieved from.
func (l *Loader) fetch(ctx context.Context, uri string, hc *http.Client) (string, string, error) {
	var (
		r     io.ReadCloser
		final = uri
		size  = int64(-1)
	)
	shown := uri
	u, err := url.Parse(uri)
	switch {
	case strings.TrimSpace(uri) != uri: // not shown: it may be a URI with userinfo
		return "", "", errors.New("openapi: load: the URI has leading or trailing whitespace")
	case err != nil && hasScheme(uri): // shown neither, as its userinfo cannot be found
		return "", "", errors.New("openapi: load: the URI cannot be parsed (RFC 3986)")
	case err != nil || len(u.Scheme) <= 1: // not a URL: a file path, perhaps with a drive letter
		u, err = &url.URL{}, nil
	default:
		shown = u.Redacted()
		err = checkURI(u, uri)
	}
	switch {
	case err != nil:
	case l.Fetch != nil:
		if r, final, err = l.Fetch(ctx, uri); final == "" {
			final = uri
		}
	case u.Scheme == "http" || u.Scheme == "https":
		var req *http.Request
		var resp *http.Response
		if req, err = http.NewRequestWithContext(ctx, "GET", uri, nil); err == nil {
			if resp, err = hc.Do(req); err == nil {
				r, size = resp.Body, resp.ContentLength
				if resp.Request != nil {
					final = resp.Request.URL.String()
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
			final = (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
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
		bound := limit(l.MaxBytes, 64<<20)
		b.Grow(int(min(max(size, 0), bound, 1<<20)))
		var n int64
		if n, err = io.Copy(&b, io.LimitReader(ctxReader{ctx, r}, bound+1)); n > bound {
			err = &http.MaxBytesError{Limit: bound}
		}
		if err == nil {
			return b.String(), final, nil
		}
	}
	return "", "", fmt.Errorf("openapi: load %s: %w", shown, withContext(ctx, err))
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

// newDocument parses content, retrieved from uri, and indexes its
// operations.
func newDocument(ctx context.Context, content, uri string) (*document, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	if uri == "" {
		uri = contentURN(content)
	}
	base, err := url.Parse(uri)
	switch {
	case err != nil: // not shown, as its userinfo cannot be found
		err = errors.New("the document URI cannot be parsed (RFC 3986)")
	case !base.IsAbs():
		err = errors.New("the document URI is not absolute")
	}
	if err == nil {
		err = checkURI(base, uri)
	}
	if err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	src := strings.TrimPrefix(content, "\xEF\xBB\xBF")
	if s := strings.TrimLeft(src, " \t\r\n"); s != "" && s[0] != '{' {
		return nil, fmt.Errorf("openapi: %s: YAML documents are not implemented yet: %w", uri, errors.ErrUnsupported)
	}
	t, err := parseTree(ctx, src, uri)
	if err != nil {
		return nil, err
	}
	d := &document{tree: t, uri: uri, base: base}
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
	if err := d.index(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *document) root() value { return value{d.tree, 0} }

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
func (d *document) source(ptr string) string {
	n := len(d.uri) + 1 + len(ptr)
	for i := range len(ptr) {
		if fragmentSet[ptr[i]] == 0 {
			n += 2
		}
	}
	var b strings.Builder
	b.Grow(n)
	b.WriteString(d.uri)
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
		next, at, e := d.target(ref.text())
		if err = e; err != nil {
			break
		}
		if k, ok := (*targets)[next.i]; ok {
			if l.next, rest, err = k.l, k.sum, k.err; !k.done {
				err = fmt.Errorf("%w %q: a reference cycle", ErrUnresolved, ref.text())
			}
			break
		}
		if *targets == nil {
			*targets = map[int32]link{}
		}
		(*targets)[next.i] = link{}
		l.next = &level{v: next, ptr: at}
		l = l.next
		walked = append(walked, l)
	}
	for i := len(walked) - 1; i >= 0; i-- {
		if err == nil {
			rest = rest.add(walked[i])
		}
		(*targets)[walked[i].v.i] = link{walked[i], rest, err, true}
	}
	if err != nil {
		return nil, nil, err
	}
	return head, rest.add(head), nil
}

// A resolution is where a Reference Object leads: the target, its Source
// and the description of the nearest level that gives one, or why it
// cannot be followed. It is not done while the chain through it is being
// followed.
type resolution struct {
	v         value
	src, desc string
	err       error
	done      bool
}

// follow resolves the Reference Objects from v, whose Source is src,
// returning the target, its Source, and the description of the nearest
// level that gives one: in OpenAPI 3.1 a Reference Object's description
// replaces its target's. Each Reference Object is followed once per
// document.
func (d *document) follow(v value, src string) (value, string, string, error) {
	ref, desc, described := reference(v)
	if !ref.ok() {
		return v, src, desc, nil
	}
	d.refsMu.Lock()
	defer d.refsMu.Unlock()
	type step struct {
		i         int32
		desc      string
		described bool
	}
	var walked []step // the Reference Objects followed, not yet kept
	var r resolution
	for at, last := "", ""; ; {
		if k, ok := d.refs[v.i]; ok {
			if r = k; !k.done {
				r = resolution{err: fmt.Errorf("%w %q: a reference cycle", ErrUnresolved, last)}
			}
			break
		}
		if !ref.ok() {
			r = resolution{v: v, src: d.source(at), desc: desc}
			break
		}
		if d.refs == nil {
			d.refs = map[int32]resolution{}
		}
		d.refs[v.i] = resolution{}
		walked = append(walked, step{v.i, desc, described})
		last = ref.text()
		next, nextAt, err := d.target(last)
		if err != nil {
			r = resolution{err: err}
			break
		}
		v, at = next, nextAt
		ref, desc, described = reference(v)
	}
	r.done = true
	for k := len(walked) - 1; k >= 0; k-- {
		if s := walked[k]; s.described && r.err == nil {
			r.desc = s.desc
		}
		d.refs[walked[k].i] = r
	}
	return r.v, r.src, r.desc, r.err
}

// reference returns v's $ref, if it is a string, and its description, if
// it gives one.
func reference(v value) (ref value, desc string, described bool) {
	for name, m := range v.members() {
		switch {
		case name == "$ref" && m.kind() == '"':
			ref = m
		case name == "description" && m.kind() == '"':
			desc, described = m.text(), true
		}
	}
	return ref, desc, described
}

// target returns the node a local reference names, and its pointer.
func (d *document) target(ref string) (value, string, error) {
	ptr, err := d.resolve(ref)
	if err != nil {
		return value{}, "", err
	}
	target := d.root().at(ptr)
	if !target.ok() {
		return value{}, "", fmt.Errorf("%w %q: no such node", ErrUnresolved, ref)
	}
	return target, ptr, nil
}

// resolve returns the JSON Pointer a local reference names.
func (d *document) resolve(ref string) (string, error) {
	frag, local := strings.CutPrefix(ref, "#")
	if !local {
		u, err := url.Parse(ref)
		if err != nil {
			return "", fmt.Errorf("%w %q: not a URI reference", ErrUnresolved, ref)
		}
		doc := d.base.ResolveReference(u)
		doc.Fragment, doc.RawFragment = "", ""
		if doc.String() != d.base.String() {
			return "", fmt.Errorf("%w %q: references to other documents are not followed yet", ErrUnresolved, ref)
		}
		frag = u.EscapedFragment()
	}
	ptr, err := frag, error(nil)
	if strings.IndexByte(frag, '%') >= 0 {
		ptr, err = url.PathUnescape(frag)
	}
	if err != nil || ptr != "" && ptr[0] != '/' {
		return "", fmt.Errorf("%w %q: not a JSON Pointer", ErrUnresolved, ref)
	}
	return ptr, nil
}

// checkNames refuses, as Load does, the names in cfg that no server or
// request body of the document uses, without compiling operations, stopping
// when ctx is done.
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
	seen := map[int32]bool{} // the nodes checked: Path Items, operations and request bodies
	first := func(v value) bool {
		if seen[v.i] {
			return false
		}
		seen[v.i] = true
		return true
	}
	for _, e := range d.entries {
		if server && serverID && media && len(unused) == 0 {
			return nil
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
	return nil
}
