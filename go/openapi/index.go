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
	"runtime"
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
	mediaForms  sync.Map      // mediaUse to *mediaPlan, since the same target can govern different types
	schemeNames memo[*scheme] // by the securitySchemes member a name selects
	schemeForms memo[*scheme] // by the Security Scheme Object a reference reaches
	schemeURIs  sync.Map      // schemeUse to the *scheme of a name no component declares
	defects     sync.Map      // defectKey to the error the document keeps for that defect
}

// A defectKey is a defect of a node: its number (see value.id) and the text
// of its error.
type defectKey struct {
	node int32
	text string
}

// defect returns err, a defect of the node v, as the document's value for it:
// the first error with that text found for v. Each build that describes or
// compiles v, and each part v describes, such as every position an OpenAPI
// 3.2 itemEncoding governs, therefore holds the very same Err, which a call
// refused for that defect wraps, without pairing a plan's parts with the
// descriptors published before it.
func (d *document) defect(v value, err error) error {
	if err == nil || !v.ok() {
		return err
	}
	k := defectKey{v.id(), err.Error()}
	if e, ok := d.defects.Load(k); ok {
		return e.(error)
	}
	e, _ := d.defects.LoadOrStore(k, err)
	return e.(error)
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
	{"options", "OPTIONS"}, {"head", "HEAD"}, {"patch", "PATCH"}, {"trace", "TRACE"}, {"query", "QUERY"},
}

// An entry is an indexed operation, or a Paths entry that cannot be read.
type entry struct {
	cell // its descriptor and plan

	additionalLevel *level
	forbidden       bool
	m               int8 // the method, an index into methods; -1 for a Paths entry that cannot be read
	doc             *document
	path            string
	id              string
	node            value    // the Operation Object
	levels          *level   // the Path Item chain
	sum             *summary // what its levels define, shared with every chain that defines the same
	err             error    // why the entry cannot be read, or its method is defined twice
	group           *cell    // what its Operation Object compiles to for every entry reaching it through one Path Item chain, or nil
}

// A cell holds what an entry, or the shape its group shares, compiles to:
// its descriptor, published once by whichever of describing and compiling
// comes first, and its plan, built on first use and bound to that
// descriptor. Describing and calling therefore report the same values in
// either order, and describing alone keeps no plan.
type cell struct {
	desc atomic.Pointer[Operation]
	op   atomic.Pointer[operation]
}

// describe returns the cell's descriptor, building one without a plan when
// none is published.
func (c *cell) describe(build func(plan bool) *operation) *Operation {
	if d := c.desc.Load(); d != nil {
		return d
	}
	c.desc.CompareAndSwap(nil, build(false).Operation)
	return c.desc.Load()
}

// compile returns the cell's plan, building it on first use. A plan built
// once its descriptor is published is still private, and is bound to that
// descriptor before it is published in turn.
func (c *cell) compile(build func(plan bool) *operation) *operation {
	return loadOrMake(&c.op, func() *operation {
		o := build(true)
		if !c.desc.CompareAndSwap(nil, o.Operation) {
			o.bind(c.desc.Load())
		}
		return o
	})
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
	at  [additionalField + 1]*level
	dup uint16
}

// The Path Item fields a summary covers after the methods.
const (
	parametersField = len(methods) + iota
	serversField
	additionalField
)

// add returns the summary of the chain from l, whose rest s summarizes: s
// itself when l defines none of the fields.
func (s *summary) add(l *level) *summary {
	sum := s
	for name, v := range l.v.members() {
		f, present := -1, v.kind() == '{'
		for m := range methods {
			if methods[m].name == name && (name != "query" || l.v.t.edition == 32) && (name != "trace" || l.v.t.edition != 20) {
				f = m
			}
		}
		switch name {
		case "parameters":
			f, present = parametersField, true
		case "servers":
			f, present = serversField, v.hasMembers()
		case "additionalOperations":
			f, present = additionalField, l.v.t.edition == 32 && v.kind() == '{'
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

// compile returns the entry's plan, which reports the descriptor describe
// returns.
func (e *entry) compile() *operation { return e.cell.compile(e.build) }

// describe returns the entry's descriptor, building no plan.
func (e *entry) describe() *Operation { return e.cell.describe(e.build) }

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
func (e *entry) method() string {
	if e.additionalLevel != nil {
		return e.node.t.name(e.node.i)
	}
	return methods[e.m].upper
}

func (e *entry) source() string {
	if e.additionalLevel != nil {
		l := e.additionalLevel
		return l.v.t.source(l.ptr + "/additionalOperations/" + escapeToken(e.method()))
	}
	l := e.sum.at[e.m]
	return l.v.t.source(l.ptr + "/" + methods[e.m].name)
}

// checkURI refuses a document URI with userinfo or a fragment.
func checkURI(u *url.URL, raw string) error {
	switch {
	case strings.TrimSpace(raw) != raw:
		return errors.New("a document URI cannot hold leading or trailing whitespace")
	case u.Scheme == "file" && u.Host != "" && u.Host != "localhost":
		return errors.New("a file URL cannot name a host other than localhost (RFC 8089)")
	case u.User != nil:
		return errors.New("a document URI cannot hold userinfo (RFC 9110 section 4.2.4)")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return errors.New("a document URI cannot hold a fragment")
	}
	return nil
}

// retrieve returns the content of the document at uri, the URI it was
// finally retrieved from, and that URI parsed, or nil, copying the content
// through buf, if not nil. froms is nil for the entry document; otherwise it
// lists, in order, the documents whose references reach uri: the first that
// may retrieve it must also admit every redirect hop and the final URI, and
// errRefused says none may.
func (ld *loading) retrieve(uri string, froms []string, buf []byte) (string, string, *url.URL, error) {
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
	case err != nil || u.Scheme == "" || froms == nil && drivePath(uri):
		u, err = &url.URL{}, nil
	default:
		err = checkURI(u, uri)
	}
	base, from := u, "" // final, parsed, and the referrer retrieving it
	if i := slices.IndexFunc(froms, func(f string) bool { return err == nil && ld.admit(f, uri, u) }); i >= 0 {
		from = froms[i]
	} else if err == nil && froms != nil {
		err = errRefused
	}
	switch {
	case err != nil:
	case ld.Fetch != nil:
		if u.Scheme == "" { // a file path, which reaches Fetch as the file URL the default reads it by
			if base, _, err = fileURL(uri); err != nil {
				break
			}
			uri = base.String()
		}
		if r, final, err = ld.Fetch(ctx, uri); final == "" {
			final = uri
		}
		if err == nil {
			if final != uri || !base.IsAbs() {
				base, err = url.Parse(final)
			}
			if err != nil || !base.IsAbs() {
				err = errors.New("the final document URI must be an absolute URI")
			} else if err = checkURI(base, final); err == nil && from != "" && !ld.admit(from, final, base) {
				err = notAdmitted(final)
			}
		}
	case u.Scheme == "http" || u.Scheme == "https":
		c, next := *ld.hc, ld.hc.CheckRedirect
		if next == nil {
			next = tenRedirects
		}
		c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			to := *req.URL
			to.Fragment, to.RawFragment = "", ""
			if err := checkURI(&to, to.String()); err != nil {
				return err
			}
			if from != "" && !ld.admit(from, to.String(), &to) {
				return notAdmitted(to.String())
			}
			return next(req, via)
		}
		hc := &c
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
		path := filePath(u)
		if from != "" && ld.AllowReference == nil && ld.root != nil {
			if path, err = ld.filePath(path); err == nil {
				r, size, err = open(ctx, path, ld.root)
			}
		} else {
			r, size, err = open(ctx, path, nil)
		}
	case u.Scheme == "":
		var abs string
		if base, abs, err = fileURL(uri); err == nil {
			final = base.String()
			r, size, err = open(ctx, abs, nil)
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
	return "", "", nil, fmt.Errorf("openapi: load %s: %w", safeURI(uri), safeRetrievalError(withContext(ctx, err)))
}

// fileURL returns the file URL of the file path's absolute path, and that
// path.
func fileURL(path string) (*url.URL, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	slashed := filepath.ToSlash(abs)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed // a Windows drive is a path segment, not a host
	}
	return &url.URL{Scheme: "file", Path: slashed}, abs, nil
}

// hasScheme distinguishes URI schemes from native Windows drive paths.
func hasScheme(s string) bool {
	i := strings.IndexByte(s, ':')
	return i > 0 && isScheme(s[:i]) && !drivePath(s)
}

func drivePath(s string) bool {
	return runtime.GOOS == "windows" && len(s) >= 2 && s[1] == ':' && isScheme(s[:1]) && !strings.HasPrefix(s[2:], "//")
}

func filePath(u *url.URL) string {
	p := u.Path
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
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
	if err := contextErr(c.ctx); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// open opens the file at path, returning its size, or -1 when it is not a
// regular file. Opening or reading such a file, a FIFO or a device, may
// block: a goroutine does both, writing to a pipe that the end of ctx
// closes with the context's error, and closing the file.
func open(ctx context.Context, path string, root *os.Root) (io.ReadCloser, int64, error) {
	type result struct {
		f   *os.File
		err error
	}
	ready := make(chan result)
	go func() {
		var f *os.File
		var err error
		if root != nil {
			f, err = root.Open(path)
		} else {
			f, err = os.Open(path)
		}
		select {
		case ready <- result{f, err}:
		case <-ctx.Done():
			if f != nil {
				f.Close()
			}
		}
	}()
	var f *os.File
	select {
	case r := <-ready:
		if r.err != nil {
			return nil, 0, r.err
		}
		f = r.f
	case <-ctx.Done():
		return nil, 0, contextErr(ctx)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if fi.Mode().IsRegular() {
		return f, fi.Size(), nil
	}
	pr, pw := io.Pipe()
	stop := context.AfterFunc(ctx, func() { pw.CloseWithError(contextErr(ctx)); f.Close() })
	go func() {
		defer stop()
		defer f.Close()
		_, err := io.Copy(pw, f)
		pw.CloseWithError(err)
	}()
	return pr, -1, nil
}

// safeURI omits userinfo and queries from generated diagnostics. Explicit
// document and schema metadata retains the original URI unchanged.
func safeURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URI)"
	}
	u.User = nil
	u.RawQuery, u.ForceQuery = "", false
	return u.String()
}

// retrievalError changes presentation only: the original typed error and
// every cause remain reachable through errors.Is and errors.As.
type retrievalError struct {
	cause error
	shown string
}

func (e *retrievalError) Error() string { return e.shown }
func (e *retrievalError) Unwrap() error { return e.cause }
func safeRetrievalError(err error) error {
	var u *url.Error
	if errors.As(err, &u) {
		return &retrievalError{err, fmt.Sprintf("%s %q: %v", u.Op, safeURI(u.URL), safeRetrievalError(u.Err))}
	}
	return err
}

// newDocument reads content, retrieved from uri, and every document its
// references reach, and indexes its operations.
func newDocument(ld *loading, content, uri string) (*document, error) {
	if err := contextErr(ld.ctx); err != nil {
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
		return nil, fmt.Errorf("openapi: %s: no openapi or swagger field", safeURI(uri))
	case root.str("swagger") == "2.0":
		d.version = "2.0"
	case isPatchOf(d.version, "3.0"), isPatchOf(d.version, "3.1"), isPatchOf(d.version, "3.2"):
	default:
		return nil, fmt.Errorf("openapi: %s: unsupported version", safeURI(uri))
	}
	t.setEdition(31)
	if t.edition <= 30 && !root.get("paths").ok() {
		return nil, fmt.Errorf("openapi: %s: no paths", safeURI(uri))
	}
	if !root.get("paths").ok() && !root.get("components").ok() && !root.get("webhooks").ok() {
		return nil, fmt.Errorf("openapi: %s: no paths, components or webhooks", safeURI(uri))
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
	if !ok {
		return false
	}
	_, err := strconv.ParseUint(patch, 10, 32)
	return err == nil
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
	var groups map[*summary]int // first contiguous entries sharing a summary
	paths := d.root().get("paths")
	if bundled(paths) { // listed once, as Operations says
		d.entries = append(d.entries, &entry{doc: d, m: -1, levels: &level{v: paths, ptr: "/paths"}, err: errBundle})
		return nil
	}
	for path, item := range paths.members() {
		if err := contextErr(ctx); err != nil {
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
		first, shared := 0, false
		if sum != nil && levels.next != nil {
			first, shared = groups[sum]
			if !shared {
				if groups == nil {
					groups = map[*summary]int{}
				}
				groups[sum] = len(d.entries)
			}
		}
		for m := range methods {
			if sum == nil || sum.at[m] == nil {
				continue
			}
			e := d.addOperation(path, m, levels, sum)
			if shared {
				original := d.entries[first]
				if original.group == nil {
					original.group = new(cell)
				}
				e.group = original.group
				first++
			}
		}
		if sum == nil || sum.at[additionalField] == nil {
			continue
		}
		l := sum.at[additionalField]
		if ops := l.v.get("additionalOperations"); bundled(ops) { // listed once, as Operations says
			d.entries = append(d.entries, &entry{doc: d, path: path, m: -1, levels: &level{v: ops, ptr: l.ptr + "/additionalOperations"}, err: errBundle})
			if shared {
				first++ // the group's first path lists the same
			}
		} else {
			for name, n := range ops.members() {
				if n.kind() != '{' {
					continue
				}
				e := &entry{doc: d, path: path, id: operationID(n), node: n, levels: levels, sum: sum, additionalLevel: l}
				e.forbidden = !isToken(name) || slices.ContainsFunc(methods[:], func(m struct{ name, upper string }) bool { return m.upper == name })
				if e.forbidden {
					e.err = fmt.Errorf("forbidden additional method %q", name)
				} else if sum.dup&(1<<additionalField) != 0 {
					e.err = errors.New("the Path Item and its $ref target both define additionalOperations")
				}
				d.recordOperation(e)
				if shared {
					original := d.entries[first]
					if original.group == nil {
						original.group = new(cell)
					}
					e.group = original.group
					first++
				}
			}
		}
	}
	return nil
}

// addOperation indexes the operation for method m of a Path Item chain,
// which sum summarizes.
func (d *document) addOperation(path string, m int, levels *level, sum *summary) *entry {
	n := sum.at[m].v.get(methods[m].name)
	e := &entry{doc: d, path: path, id: operationID(n), m: int8(m), node: n, levels: levels, sum: sum}
	if sum.dup&(1<<m) != 0 {
		e.err = fmt.Errorf("the Path Item and its $ref target both define %s", methods[m].name)
	}
	d.recordOperation(e)
	return e
}

// operationID returns the operationId of the Operation Object n, which has
// none that can be read when it is written as a reference.
func operationID(n value) string {
	if bundled(n) {
		return ""
	}
	return n.str("operationId")
}

func (d *document) recordOperation(e *entry) {
	d.entries = append(d.entries, e)
	if e.forbidden {
		return
	}
	d.byRoute[route{e.method(), e.path}] = e
	if e.id != "" && !isMethodAndPath(e.id) {
		if _, dup := d.byID[e.id]; dup {
			d.byID[e.id] = nil
		} else {
			d.byID[e.id] = e
		}
	}
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
		if e.id == key && !e.forbidden { // a forbidden additional operation is no operation, and has no Key
			keys = append(keys, label(e.method()+" "+e.path))
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
				err = fmt.Errorf("%w %q: a reference cycle", ErrUnresolved, safeURI(ref.text()))
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
// that every chain into it names it alike. An entry of a Swagger 2.0 root
// parameters or responses map, which holds no Reference Object, that is
// written as a reference is for a bundler to replace, and is not followed.
func (d *document) follow(v value, src string) (value, string, string, error) {
	ref, desc, _ := reference(v)
	if !ref.ok() {
		return v, src, desc, nil
	}
	var walked []value    // the Reference Objects followed, not yet published
	var on map[int32]bool // their ids (see value.id), once a scan of walked would be long
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
		if on[i] || on == nil && slices.ContainsFunc(walked, func(w value) bool { return w.id() == i }) {
			cycle := walked[slices.IndexFunc(walked, func(w value) bool { return w.id() == i }):]
			first, _, _ := reference(slices.MinFunc(cycle, func(a, b value) int { return cmp.Compare(a.id(), b.id()) }))
			r = resolution{err: fmt.Errorf("%w %q: a reference cycle", ErrUnresolved, safeURI(first.text()))}
			break
		}
		if walked = append(walked, v); len(walked) == 16 {
			on = map[int32]bool{}
			for _, w := range walked {
				on[w.id()] = true
			}
		} else if on != nil {
			on[i] = true
		}
		next, nextAt, err := d.target(ref)
		if err != nil {
			r = resolution{err: err}
			break
		}
		if next.t.edition == 20 && next.t.openAPI && swaggerEntry(nextAt) && bundled(next) {
			r = resolution{err: errBundle}
			break
		}
		v, at = next, nextAt
		ref, desc, _ = reference(v)
	}
	for k := len(walked) - 1; k >= 0; k-- {
		if _, desc, described := reference(walked[k]); described && walked[k].t.edition >= 31 && r.err == nil {
			r.desc = desc
		}
		kept, _ := d.refs.LoadOrStore(walked[k].id(), &resolution{r.v, r.src, r.desc, r.err})
		r = *kept.(*resolution)
	}
	return r.v, r.src, r.desc, r.err
}

// swaggerEntry reports whether the JSON Pointer ptr names an entry of a
// Swagger 2.0 document's root parameters or responses map.
func swaggerEntry(ptr string) bool {
	name, ok := strings.CutPrefix(ptr, "/parameters/")
	if !ok {
		name, ok = strings.CutPrefix(ptr, "/responses/")
	}
	return ok && !strings.Contains(name, "/")
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
		if bundled(list) { // described as one server (see parseServers)
			serverID = serverID || idOf(list) == cfg.ServerID
			return
		}
		for _, s := range list.members() {
			serverID = serverID || idOf(s) == cfg.ServerID
			if bundled(s) {
				continue // described with no URL or name, and no variables
			}
			u := s.str("url")
			server = server || u == cfg.Server || s.t.edition == 32 && s.str("name") == cfg.Server
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
		if err := contextErr(ctx); err != nil {
			return fmt.Errorf("openapi: %w", err)
		}
		if e.m < 0 {
			continue
		}
		for l := e.levels; l != nil && first(l.v); l = l.next { // a level checked has its rest checked
			check(l.v.get("servers"))
		}
		if !first(e.node) || bundled(e.node) {
			continue
		}
		check(e.node.get("servers"))
		if e.node.t.edition == 20 && (!server || !serverID || !media) {
			o := e.compile()
			for _, s := range o.Servers {
				server = server || s.URL == cfg.Server
				serverID = serverID || s.ID == cfg.ServerID
			}
			if o.Body != nil && o.Body.Err == nil { // consumes written as a reference offers no type
				media = media || match(o.body, o.Body.Media, cfg.mediaType, true) != nil
			}
		}
		none := free
		if s := e.node.get("security"); s.ok() {
			none = sec.list(s)
		}
		if none && sec.key == "{}" {
			sec.key = "" // a credential-free operation offers it
		}
		if rb := e.node.get("requestBody"); e.node.t.edition != 20 && !media && rb.ok() && e.method() != "TRACE" && e.method() != "CONNECT" && !(e.node.t.edition == 30 && slices.Contains([]string{"GET", "HEAD", "DELETE", "OPTIONS"}, e.method())) {
			body, _, _, err := d.follow(rb, "")
			if err != nil || !first(body) {
				continue
			}
			if content := body.get("content"); bundled(content) {
				continue // its media types are in a document to bundle first
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
		re.setting("Options.MediaType", fmt.Errorf("no operation declares %s", label(cfg.MediaType)))
	}
	for name := range unused {
		re.setting("Options.Variables["+strconv.Quote(name)+"]", errors.New("no server URL uses this variable"))
	}
	sec.refuse()
	return nil
}
