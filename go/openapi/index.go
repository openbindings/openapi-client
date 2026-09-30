package openapi

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// A document is a loaded OpenAPI document: its source, its tree, and the
// index of its operations. Derived Clients share it.
type document struct {
	uri     string   // the URI the document was retrieved from
	base    *url.URL // uri, parsed
	src     string   // the content, without a byte order mark
	root    node
	version string
	dialect string // jsonSchemaDialect

	ops    []*operation            // in document order
	byID   map[string][]*operation // by operationId
	byKey  map[string]*operation   // by method and path
	broken map[string]*operation   // Paths entries that cannot be read, by path

	serversOnce sync.Once
	servers     []*server // the root servers, or the default one
}

// The fixed methods of an OpenAPI 3.1 Path Item, in the order Operations
// lists them.
var methods = [...]string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// fetch retrieves the document at uri, returning its content and the URI it
// was finally retrieved from.
func (l *Loader) fetch(ctx context.Context, uri string, hc *http.Client) (string, string, error) {
	var (
		r     io.ReadCloser
		final = uri
		size  = int64(-1)
	)
	u, err := url.Parse(uri)
	if err != nil {
		u, err = &url.URL{}, nil // not a URL: a file path
	}
	switch {
	case l.Fetch != nil:
		r, final, err = l.Fetch(ctx, uri)
		if final == "" {
			final = uri
		}
	case u.Scheme == "http" || u.Scheme == "https":
		var req *http.Request
		if req, err = http.NewRequestWithContext(ctx, "GET", uri, nil); err == nil {
			var resp *http.Response
			if resp, err = hc.Do(req); err == nil {
				r, final, size = resp.Body, resp.Request.URL.String(), resp.ContentLength
				if resp.StatusCode/100 != 2 {
					r.Close()
					return "", "", fmt.Errorf("openapi: load %s: %s", uri, resp.Status)
				}
			}
		}
	case u.Scheme == "file":
		r, size, err = open(filepath.FromSlash(u.Path))
	case len(u.Scheme) <= 1: // a path, perhaps with a drive letter
		var abs string
		if abs, err = filepath.Abs(uri); err == nil {
			final = (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
			r, size, err = open(abs)
		}
	default:
		err = fmt.Errorf("unsupported URI scheme %q", u.Scheme)
	}
	if err == nil {
		defer r.Close()
		var b strings.Builder
		bound := limit(l.MaxBytes, 64<<20)
		if bound < 0 {
			bound = 1<<63 - 2
		}
		if size > 0 && size <= bound {
			b.Grow(int(size))
		}
		var n int64
		if n, err = io.Copy(&b, io.LimitReader(r, bound+1)); n > bound {
			err = &http.MaxBytesError{Limit: bound}
		}
		if err == nil {
			return b.String(), final, nil
		}
	}
	return "", "", fmt.Errorf("openapi: load %s: %w", uri, err)
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
func newDocument(content, uri string) (*document, error) {
	if uri == "" {
		uri = contentURN(content)
	}
	base, err := url.Parse(uri)
	if err != nil || !base.IsAbs() {
		return nil, fmt.Errorf("openapi: document URI %q is not absolute", uri)
	}
	src := strings.TrimPrefix(content, "\xEF\xBB\xBF")
	if s := strings.TrimLeft(src, " \t\r\n"); s != "" && s[0] != '{' {
		return nil, fmt.Errorf("openapi: %s: YAML documents are not implemented yet: %w", uri, errors.ErrUnsupported)
	}
	root, err := parseTree(src, uri)
	if err != nil {
		return nil, err
	}
	d := &document{uri: uri, base: base, src: src, root: root}
	d.version = root.str("openapi")
	switch {
	case root.get("openapi") == nil && root.get("swagger") == nil:
		return nil, fmt.Errorf("openapi: %s: no openapi or swagger field", uri)
	case isPatchOf(d.version, "3.1"):
	case root.str("swagger") == "2.0" || isPatchOf(d.version, "3.0") || isPatchOf(d.version, "3.2"):
		return nil, fmt.Errorf("openapi: %s: this edition is not implemented yet: %w", uri, errors.ErrUnsupported)
	default:
		return nil, fmt.Errorf("openapi: %s: unsupported version", uri)
	}
	if !slices.ContainsFunc([]string{"paths", "components", "webhooks"}, func(k string) bool { return root.get(k) != nil }) {
		return nil, fmt.Errorf("openapi: %s: no paths, components or webhooks", uri)
	}
	d.dialect = root.str("jsonSchemaDialect")
	d.index()
	return d, nil
}

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

// source returns the URI naming the node at ptr.
func (d *document) source(ptr string) string { return d.uri + fragment(ptr) }

// A level is one Path Item of a chain: the Paths entry, then each $ref
// target in turn.
type level struct {
	n   *node
	ptr string
}

func (d *document) index() {
	d.byID, d.byKey, d.broken = map[string][]*operation{}, map[string]*operation{}, map[string]*operation{}
	paths := d.root.get("paths")
	if paths == nil {
		return
	}
	for i := range paths.kids {
		path := paths.kids[i].key
		ptr := "/paths/" + escapeToken(path)
		levels, err := d.chain(&paths.kids[i], ptr)
		if err != nil {
			o := &operation{Operation: Operation{Path: path, Source: d.source(ptr), Err: err}}
			d.ops = append(d.ops, o)
			d.broken[path] = o
			continue
		}
		for _, m := range methods {
			d.addOperation(path, m, levels)
		}
	}
	for _, o := range d.ops {
		switch {
		case o.Method == "":
		case len(d.byID[o.ID]) == 1 && !isMethodAndPath(o.ID):
			o.Key = o.ID
		default:
			o.Key = o.Method + " " + o.Path
		}
	}
}

// addOperation indexes the operation for method m of a Path Item chain, if
// one of its levels defines it.
func (d *document) addOperation(path, m string, levels []level) {
	var o *operation
	for _, l := range levels {
		n := l.n.get(m)
		if n == nil || n.kind != '{' {
			continue
		}
		if o != nil {
			o.conflict = fmt.Errorf("the Path Item and its $ref target both define %s", m)
			continue
		}
		o = &operation{doc: d, node: n, ptr: l.ptr + "/" + m, levels: levels, Operation: Operation{
			ID:     n.str("operationId"),
			Method: strings.ToUpper(m),
			Path:   path,
			Source: d.source(l.ptr + "/" + m),
		}}
	}
	if o == nil {
		return
	}
	d.ops = append(d.ops, o)
	d.byKey[o.Method+" "+path] = o
	if o.ID != "" {
		d.byID[o.ID] = append(d.byID[o.ID], o)
	}
}

func isMethodAndPath(key string) bool {
	_, path, ok := strings.Cut(key, " ")
	return ok && strings.HasPrefix(path, "/")
}

// lookup returns the operation key names, by the rules Call uses.
func (d *document) lookup(key string) (*operation, error) {
	if d == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoOperation, key)
	}
	if isMethodAndPath(key) {
		_, path, _ := strings.Cut(key, " ")
		if o := d.byKey[key]; o != nil {
			return o, nil
		}
		if o := d.broken[path]; o != nil {
			return o, nil
		}
		return nil, fmt.Errorf("%w: %q", ErrNoOperation, key)
	}
	ops := d.byID[key]
	switch len(ops) {
	case 0:
		return nil, fmt.Errorf("%w: %q", ErrNoOperation, key)
	case 1:
		return ops[0], nil
	}
	keys := make([]string, len(ops))
	for i, o := range ops {
		keys[i] = o.Key
	}
	return nil, fmt.Errorf("%w: %q is the operationId of %s", ErrNoOperation, key, strings.Join(keys, ", "))
}

// chain follows the Reference Objects (or Path Item $refs) from n, at ptr,
// to their target, returning every level, nearest first. A level that is
// not a reference ends the chain.
func (d *document) chain(n *node, ptr string) ([]level, error) {
	levels := []level{{n, ptr}}
	for {
		ref := n.get("$ref")
		if ref == nil || ref.kind != '"' {
			return levels, nil
		}
		target, err := d.resolve(ref.text)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(levels, func(l level) bool { return l.ptr == target }) {
			return nil, fmt.Errorf("%w %q: a reference cycle", ErrUnresolved, ref.text)
		}
		if n = pointerAt(&d.root, target); n == nil {
			return nil, fmt.Errorf("%w %q: no such node", ErrUnresolved, ref.text)
		}
		ptr = target
		levels = append(levels, level{n, ptr})
	}
}

// resolve returns the JSON Pointer a local reference names.
func (d *document) resolve(ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("%w %q: %v", ErrUnresolved, ref, err)
	}
	if !strings.HasPrefix(ref, "#") {
		doc := d.base.ResolveReference(u)
		doc.Fragment, doc.RawFragment = "", ""
		if doc.String() != d.base.String() {
			return "", fmt.Errorf("%w %q: references to other documents are not followed yet", ErrUnresolved, ref)
		}
	}
	ptr, err := url.PathUnescape(u.EscapedFragment())
	if err != nil || ptr != "" && ptr[0] != '/' {
		return "", fmt.Errorf("%w %q: not a JSON Pointer", ErrUnresolved, ref)
	}
	return ptr, nil
}
