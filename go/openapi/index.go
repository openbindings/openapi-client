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

	serversOnce sync.Once
	servers     []*server // the root servers, or the default one
	walks       sync.Map  // reflect.Type to *walk, for finding readers in bodies
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
	m      int     // the method, an index into methods; -1 for a Paths entry that cannot be read
	node   value   // the Operation Object
	levels []level // the Path Item chain
	at     int     // the level that defines the operation
	err    error   // why the entry cannot be read, or its method is defined twice
	once   sync.Once
	op     *operation
}

// A level is one Path Item of a chain: the Paths entry, then each $ref
// target in turn.
type level struct {
	v   value
	ptr string
}

// compile completes the entry's descriptor and plan once.
func (e *entry) compile() *operation {
	e.once.Do(func() { e.op = e.build() })
	return e.op
}

// ptr returns the Operation Object's JSON Pointer.
func (e *entry) ptr() string { return e.levels[e.at].ptr + "/" + methods[e.m].name }

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
	if err != nil || len(u.Scheme) <= 1 { // not a URL: a file path, perhaps with a drive letter
		u, err = &url.URL{}, nil
	} else {
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
					err = errors.New(resp.Status)
				}
			}
		}
	case u.Scheme == "file" && u.Host != "" && u.Host != "localhost":
		err = errors.New("a file URL cannot name a host other than localhost (RFC 8089)")
	case u.Scheme == "file":
		r, size, err = open(filepath.FromSlash(u.Path))
	case u.Scheme == "":
		var abs string
		if abs, err = filepath.Abs(uri); err == nil {
			final = (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
			r, size, err = open(abs)
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
		if n, err = io.Copy(&b, io.LimitReader(r, bound+1)); n > bound {
			err = &http.MaxBytesError{Limit: bound}
		}
		if err == nil {
			return b.String(), final, nil
		}
	}
	return "", "", fmt.Errorf("openapi: load %s: %w", shown, err)
}

// open opens the file at path, returning its size.
func open(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
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
	if err == nil && !base.IsAbs() {
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
// those RFC 3986 allows in a fragment.
func (d *document) source(ptr string) string {
	var b strings.Builder
	b.Grow(len(d.uri) + len(ptr) + 1)
	b.WriteString(d.uri)
	b.WriteByte('#')
	for i := 0; i < len(ptr); i++ {
		if c := ptr[i]; unreserved(c) || strings.IndexByte("!$&'()*+,;=:@/?", c) >= 0 {
			b.WriteByte(c)
		} else {
			writeEscaped(&b, c)
		}
	}
	return b.String()
}

func (d *document) index(ctx context.Context) error {
	d.byID, d.byRoute, d.broken = map[string]*entry{}, map[route]*entry{}, map[string]*entry{}
	for path, item := range d.root().get("paths").members() {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("openapi: %w", err)
		}
		if strings.HasPrefix(path, "x-") {
			continue
		}
		ptr := "/paths/" + escapeToken(path)
		levels, err := d.chain(item, ptr)
		if err == nil && !strings.HasPrefix(path, "/") {
			err = fmt.Errorf("Paths key %q does not begin with /", path)
		}
		if err != nil {
			e := &entry{doc: d, path: path, m: -1, levels: []level{{item, ptr}}, err: err}
			d.entries = append(d.entries, e)
			d.broken[path] = e
			continue
		}
		for m := range methods {
			d.addOperation(path, m, levels)
		}
	}
	return nil
}

// addOperation indexes the operation for method m of a Path Item chain, if
// one of its levels defines it.
func (d *document) addOperation(path string, m int, levels []level) {
	var e *entry
	for i, l := range levels {
		n := l.v.get(methods[m].name)
		switch {
		case n.kind() != '{':
		case e != nil:
			e.err = fmt.Errorf("the Path Item and its $ref target both define %s", methods[m].name)
		default:
			e = &entry{doc: d, path: path, id: n.str("operationId"), m: m, node: n, levels: levels, at: i}
		}
	}
	if e == nil {
		return
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
			keys = append(keys, methods[e.m].upper+" "+e.path)
		}
	}
	return nil, fmt.Errorf("%w: %q is the operationId of %s", ErrNoOperation, key, strings.Join(keys, ", "))
}

// chain follows the Path Item $refs from v, at ptr, returning every level,
// nearest first.
func (d *document) chain(v value, ptr string) ([]level, error) {
	levels := []level{{v, ptr}}
	var loop cycle
	for {
		next, at, err := d.deref(v)
		if err != nil || !next.ok() {
			return levels, err
		}
		if loop.seen(next) {
			return nil, fmt.Errorf("%w %q: a reference cycle", ErrUnresolved, v.str("$ref"))
		}
		levels = append(levels, level{next, at})
		v = next
	}
}

// follow resolves the Reference Objects from v, at ptr, returning the
// target, its pointer, and the description of the nearest level that gives
// one: in OpenAPI 3.1 a Reference Object's description replaces its
// target's.
func (d *document) follow(v value, ptr string) (value, string, string, error) {
	desc, described := "", false
	var loop cycle
	for {
		var ref value
		for name, m := range v.members() {
			switch {
			case name == "$ref":
				ref = m
			case name == "description" && !described && m.kind() == '"':
				desc, described = m.text(), true
			}
		}
		if ref.kind() != '"' {
			return v, ptr, desc, nil
		}
		next, at, err := d.target(ref.text())
		switch {
		case err != nil:
			return value{}, "", "", err
		case loop.seen(next):
			return value{}, "", "", fmt.Errorf("%w %q: a reference cycle", ErrUnresolved, ref.text())
		}
		v, ptr = next, at
	}
}

// deref returns the target of v's $ref, and its pointer, or an absent value
// when v is not a reference.
func (d *document) deref(v value) (value, string, error) {
	if ref := v.get("$ref"); ref.kind() == '"' {
		return d.target(ref.text())
	}
	return value{}, "", nil
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

// A cycle detects a reference cycle by Brent's method, in constant space.
type cycle struct {
	mark      int32
	steps, at int
}

// seen reports whether v closes a cycle of the values it has been given.
func (c *cycle) seen(v value) bool {
	if c.steps > 0 && v.i == c.mark {
		return true
	}
	if c.steps++; c.steps > c.at {
		c.mark, c.at = v.i, 2*c.at+1
	}
	return false
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
// request body of the document uses, without compiling operations.
func (d *document) checkNames(cfg *config, re *RequestError) {
	server, serverID := cfg.Server == "", cfg.ServerID == ""
	media := cfg.MediaType == "" || cfg.mediaTypeErr != nil
	unused := maps.Clone(cfg.Variables)
	check := func(list value, ptr func() string) {
		i := 0
		for _, s := range list.members() {
			u := s.str("url")
			server = server || u == cfg.Server
			serverID = serverID || d.source(ptr()+"/servers/"+strconv.Itoa(i)) == cfg.ServerID
			if len(unused) > 0 {
				for _, name := range templateNames(u) {
					delete(unused, name)
				}
			}
			i++
		}
	}
	if s := d.root().get("servers"); s.hasMembers() {
		check(s, func() string { return "" })
	} else {
		server, serverID = server || cfg.Server == "/", serverID || cfg.ServerID == "default"
	}
	var prev *level
	for _, e := range d.entries {
		if server && serverID && media && len(unused) == 0 {
			return
		}
		if e.m < 0 {
			continue
		}
		if &e.levels[0] != prev { // the first operation of its path
			prev = &e.levels[0]
			for _, l := range e.levels {
				check(l.v.get("servers"), func() string { return l.ptr })
			}
		}
		check(e.node.get("servers"), e.ptr)
		if rb := e.node.get("requestBody"); !media && rb.ok() && methods[e.m].upper != "TRACE" {
			body, _, _, err := d.follow(rb, "")
			for typ := range body.get("content").members() {
				m, ok := parseMedia(typ)
				_, covers := m.covers(cfg.mediaType)
				media = media || err == nil && ok && covers
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
}
