package openapi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Loading every document an entry document reaches: retrieval within the
// Loader's bounds, discovery by the OpenAPI object model, the identifiers the
// documents declare, and the resolution of references against them.

// fetches is how many documents one load retrieves at once.
const fetches = 16

// A loading is one load in progress: its Loader and context, the
// http.Client that retrieves documents, the bytes MaxBytes leaves, and the
// default boundary.
type loading struct {
	*Loader
	ctx     context.Context
	hc      *http.Client
	limit   int64
	left    atomic.Int64 // the bytes MaxBytes leaves, below zero once passed
	entry   string       // the URI the entry was requested as
	origins []*url.URL   // Origins, then the entry's original origin if it is http or https
	dir     string       // a file entry's directory, cleaned and its symlinks resolved, or ""
	root    *os.Root
	rootErr error
	pending <-chan struct{} // completion of the last retrieval wave
}

// start begins a load of the entry requested as uri, with l's settings and
// the HTTPClient of opts.
func (l *Loader) start(ctx context.Context, uri string, opts *Options) (*loading, error) {
	if len(l.Origins) > 0 && l.AllowReference != nil {
		return nil, errors.New("openapi: a Loader cannot set both Origins and AllowReference")
	}
	ld := &loading{Loader: l, ctx: ctx, hc: http.DefaultClient, limit: limit(l.MaxBytes, 64<<20), entry: uri}
	if opts != nil && opts.HTTPClient != nil {
		ld.hc = opts.HTTPClient
	}
	ld.left.Store(ld.limit)
	for _, o := range l.Origins {
		if u, err := url.Parse(o); err == nil {
			ld.origins = append(ld.origins, u)
		}
	}
	return ld, nil
}

// bound sets the default boundary of the entry, retrieved from final: an
// http or https entry's origin as requested, or a file entry's directory.
func (ld *loading) bound(final string) {
	if u, err := url.Parse(ld.entry); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		ld.origins = append(ld.origins, u)
	} else if u, err := url.Parse(final); err == nil && u.Scheme == "file" {
		ld.dir, ld.rootErr = filepath.EvalSymlinks(filepath.Dir(filepath.FromSlash(u.Path)))
		if ld.rootErr == nil && ld.AllowReference == nil && ld.Fetch == nil {
			ld.root, ld.rootErr = os.OpenRoot(ld.dir)
		}
	}
}

// close keeps the anchored directory alive until a canceled retrieval wave
// has actually left its workers; an uncooperative Fetch cannot delay Load.
func (ld *loading) close() {
	if ld.root == nil {
		return
	}
	if ld.pending != nil {
		select {
		case <-ld.pending:
		default:
			go func() { <-ld.pending; ld.root.Close() }()
			return
		}
	}
	ld.root.Close()
}

// filePath resolves absolute symlinks before the rooted open, which then
// prevents replacement of any remaining component from escaping the root.
func (ld *loading) filePath(path string) (string, error) {
	if ld.rootErr != nil {
		return "", ld.rootErr
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(ld.dir, resolved)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("file is outside the entry directory")
	}
	return rel, nil
}

// admit reports whether the document retrieved from from may retrieve the
// one at to, which u is parsed, if not nil.
func (ld *loading) admit(from, to string, u *url.URL) bool {
	var err error
	if u == nil {
		u, err = url.Parse(to)
	}
	if err != nil || !u.IsAbs() || checkURI(u, to) != nil {
		return false
	}
	if ld.AllowReference != nil {
		return ld.AllowReference(from, to)
	}
	switch {
	case u.Scheme == "http" || u.Scheme == "https":
		return slices.ContainsFunc(ld.origins, func(o *url.URL) bool { return sameOrigin(o, u) })
	case u.Scheme == "file" && ld.dir != "":
		_, err := ld.filePath(filepath.FromSlash(u.Path))
		return err == nil
	}
	return false
}

// errRefused is why a URI is not retrieved when no referrer asked may
// retrieve it: another may yet (see discovery.refuse).
var errRefused = errors.New("no referrer may retrieve it")

// notAdmitted is why uri, which admission refused, is not retrieved.
func notAdmitted(uri string) error {
	u, err := url.Parse(uri)
	if err != nil { // not shown, as its userinfo cannot be found
		return errors.New("a URI that cannot be parsed is outside the documents the Loader admits")
	}
	return fmt.Errorf("%s is outside the documents the Loader admits", safeURI(u.String()))
}

// A counted reads a document within the bytes MaxBytes leaves its load.
type counted struct {
	ld *loading
	r  io.Reader
}

func (c counted) Read(p []byte) (int, error) {
	if left := c.ld.left.Load(); int64(len(p)) > left {
		p = p[:max(left, 0)+1]
	}
	n, err := c.r.Read(p)
	if n > 0 && c.ld.left.Add(-int64(n)) < 0 {
		return n, &http.MaxBytesError{Limit: c.ld.limit}
	}
	return n, err
}

// get retrieves and parses the document at uri, which the documents
// retrieved from froms reference, copying its content through buf.
func (ld *loading) get(uri string, froms []string, buf []byte) (*tree, error) {
	content, final, base, err := ld.retrieve(uri, froms, buf)
	if err != nil {
		return nil, err
	}
	return readTree(ld.ctx, content, final, base)
}

// A kind is what discovery reads a node as: an OpenAPI object, which says
// where its references are and what the nodes it holds are, a Schema Object,
// which also declares identifiers, a node of a document that is not an
// OpenAPI document, read for identifiers only, or data.
type kind uint8

const (
	anyKind kind = iota
	dataKind
	rootKind
	componentsKind
	pathItemKind
	operationKind
	mediaKind
	encodingKind
	parameterKind // or Header Object; this and the kinds up to refKind may be Reference Objects
	requestBodyKind
	responseKind
	callbackKind
	refKind // an Example, Link or Security Scheme Object: only a Reference Object holds a reference
	schemaKind
	schemaIDsKind // schema locations inspected for identifiers, without following references
	itemsKind     // Swagger Items Object, not a schema or Reference Object
)

// A slot is what a member of an object holds: a k node ('1'), or an array
// ('[') or map ('{') of them; 'x' is a map with specification extensions.
type slot struct {
	k   kind
	how byte
}

// model maps the member names of each kind of OpenAPI 3.1 object to what
// they hold (OpenAPI 3.1.2 section 4.8; JSON Schema 2020-12 core sections
// 8.2.4, 10 and 11, and validation section 8.5, for a schema's subschemas).
var model = [...]map[string]slot{
	rootKind: {"paths": {pathItemKind, 'x'}, "webhooks": {pathItemKind, '{'}, "components": {componentsKind, '1'}},
	componentsKind: {"schemas": {schemaKind, '{'}, "responses": {responseKind, '{'}, "parameters": {parameterKind, '{'},
		"examples": {refKind, '{'}, "requestBodies": {requestBodyKind, '{'}, "headers": {parameterKind, '{'},
		"securitySchemes": {refKind, '{'}, "links": {refKind, '{'}, "callbacks": {callbackKind, '{'}, "pathItems": {pathItemKind, '{'}},
	pathItemKind: {"parameters": {parameterKind, '['}, "get": {operationKind, '1'}, "put": {operationKind, '1'},
		"post": {operationKind, '1'}, "delete": {operationKind, '1'}, "options": {operationKind, '1'},
		"head": {operationKind, '1'}, "patch": {operationKind, '1'}, "trace": {operationKind, '1'}},
	operationKind: {"parameters": {parameterKind, '['}, "requestBody": {requestBodyKind, '1'},
		"responses": {responseKind, 'x'}, "callbacks": {callbackKind, '{'}},
	parameterKind:   {"schema": {schemaKind, '1'}, "content": {mediaKind, '{'}, "examples": {refKind, '{'}},
	requestBodyKind: {"content": {mediaKind, '{'}},
	mediaKind:       {"schema": {schemaKind, '1'}, "examples": {refKind, '{'}, "encoding": {encodingKind, '{'}},
	encodingKind:    {"headers": {parameterKind, '{'}},
	responseKind:    {"headers": {parameterKind, '{'}, "content": {mediaKind, '{'}, "links": {refKind, '{'}},
	schemaKind: {"items": {schemaKind, '1'}, "not": {schemaKind, '1'}, "if": {schemaKind, '1'}, "then": {schemaKind, '1'},
		"else": {schemaKind, '1'}, "contains": {schemaKind, '1'}, "additionalProperties": {schemaKind, '1'},
		"propertyNames": {schemaKind, '1'}, "unevaluatedItems": {schemaKind, '1'}, "unevaluatedProperties": {schemaKind, '1'},
		"contentSchema": {schemaKind, '1'}, "allOf": {schemaKind, '['}, "anyOf": {schemaKind, '['}, "oneOf": {schemaKind, '['},
		"prefixItems": {schemaKind, '['}, "properties": {schemaKind, '{'}, "patternProperties": {schemaKind, '{'},
		"dependentSchemas": {schemaKind, '{'}, "$defs": {schemaKind, '{'}},
}

// componentName matches what a Discriminator mapping value that could be a
// component name looks like (OpenAPI 3.1.2 section 4.8.7).
var componentName = regexp.MustCompile(`^[a-zA-Z0-9.\-_]+$`)

// kind returns what discovery reads the root of t as: the OpenAPI Object of
// an OpenAPI document, or a node of another document.
func (t *tree) kind() kind {
	if r := t.root(); r.get("openapi").ok() || r.get("swagger").ok() {
		return rootKind
	}
	return anyKind
}

// setEdition settles a document's model before discovery or compilation.
func (t *tree) setEdition(inherit int) {
	t.edition = inherit
	r := t.root()
	switch {
	case r.str("swagger") == "2.0":
		t.edition = 20
	case isPatchOf(r.str("openapi"), "3.0"):
		t.edition = 30
	case isPatchOf(r.str("openapi"), "3.1"):
		t.edition = 31
	case isPatchOf(r.str("openapi"), "3.2"):
		t.edition = 32
	}
	t.reaches = t.reaches || t.edition == 32 && t.editionRefs
	t.refbase = t.base
	if t.edition == 32 && r.get("$self").kind() == '"' {
		if u, err := t.base.Parse(r.str("$self")); err == nil && u.IsAbs() && checkURI(u, r.str("$self")) == nil {
			t.refbase = u
		}
	}
}

// modelSlot applies the object's edition, excluding later feature-shaped data.
func modelSlot(k kind, name string, edition int) (slot, bool) {
	if edition == 20 {
		switch k {
		case rootKind:
			switch name {
			case "paths":
				return slot{pathItemKind, 'x'}, true
			case "definitions":
				return slot{schemaKind, '{'}, true
			case "parameters":
				return slot{parameterKind, '{'}, true
			case "responses":
				return slot{responseKind, '{'}, true
			case "securityDefinitions":
				return slot{refKind, '{'}, true
			}
			return slot{}, false
		case operationKind:
			if name != "parameters" && name != "responses" {
				return slot{}, false
			}
		case pathItemKind:
			if name == "trace" {
				return slot{}, false
			}
		case parameterKind:
			if name == "items" {
				return slot{itemsKind, '1'}, true
			}
			return slot{schemaKind, '1'}, name == "schema"
		case itemsKind:
			return slot{itemsKind, '1'}, name == "items"
		case schemaKind:
			if !slices.Contains([]string{"items", "allOf", "properties", "additionalProperties"}, name) {
				return slot{}, false
			}
		case responseKind:
			if name == "schema" {
				return slot{schemaKind, '1'}, true
			}
			if name != "headers" {
				return slot{}, false
			}
		}
	}
	if edition <= 30 {
		if k == rootKind && name == "webhooks" || k == componentsKind && name == "pathItems" {
			return slot{}, false
		}
		if k == schemaKind && !slices.Contains([]string{"items", "not", "additionalProperties", "allOf", "anyOf", "oneOf", "properties"}, name) {
			return slot{}, false
		}
	}
	if edition == 32 {
		switch k {
		case pathItemKind:
			if name == "query" {
				return slot{operationKind, '1'}, true
			}
			if name == "additionalOperations" {
				return slot{operationKind, '{'}, true
			}
		case componentsKind:
			if name == "mediaTypes" {
				return slot{mediaKind, '{'}, true
			}
		case encodingKind:
			switch name {
			case "encoding":
				return slot{encodingKind, '{'}, true
			case "prefixEncoding":
				return slot{encodingKind, '['}, true
			case "itemEncoding":
				return slot{encodingKind, '1'}, true
			}
		case mediaKind:
			if name == "itemSchema" {
				return slot{schemaKind, '1'}, true
			}
			if name == "prefixEncoding" {
				return slot{encodingKind, '['}, true
			}
			if name == "itemEncoding" {
				return slot{encodingKind, '1'}, true
			}
		}
	}
	if int(k) >= len(model) {
		return slot{}, false
	}
	s, ok := model[k][name]
	return s, ok
}

// A claim is the node an identifier names, its JSON Pointer in its
// document, and the base outside it, with another node that the same
// identifier names, if one does.
type claim struct {
	v       value
	ptr     *documentPath
	base    *url.URL
	dialect string
	other   *claim
}

func (c *claim) source() string { return c.v.t.source(c.ptr.pointer()) }

// A discovery reads the documents a load reaches: a reader for each, the
// references to URIs nothing read identifies yet, those that something now
// identifies, and the URIs to retrieve, by how likely an $id is to identify
// them (see want).
type discovery struct {
	*document
	ld      *loading
	readers map[*tree]*reader
	busy    []*reader         // the readers with items to read
	wants   map[string][]want // by the URI they need, without its fragment, or a plain name's
	ready   []string
	fresh   [3][]string        // the URIs of wants, by rank, that may need retrieving
	asked   map[string]bool    // the URIs retrieved or being retrieved
	refused map[[2]string]bool // the URIs, each with a referrer that may not retrieve it
}

// A want is a reference that needs the node a URI and the fragment frag
// name, read as a k node, from the document t, and how likely an $id is to
// identify the URI: 0 for a reference to an object other than a schema,
// which an $id cannot identify for it, 1 for a schema reference written as a
// relative reference, 2 for one written as an absolute URI.
type want struct {
	frag string
	k    kind
	t    *tree
	rank int
}

// A reader reads one document for discovery: the nodes it is to read, and
// what it has read: the identifiers the document declares, where its
// references lead, and those to other documents, which, with its new
// identifiers, the discovery takes. A worker reads a document it retrieved;
// the discovery reads the rest one reader at a time.
type reader struct {
	d       *document
	t       *tree
	ids     map[string]*claim // by identifier
	claimed []string          // the identifiers the discovery has not taken
	refs    []need            // the references to other documents the discovery has not taken
	located map[int32]location
	// Two bits per kind and node: queued, entered recursively. The sixteen
	// kinds through itemsKind occupy all 32 bits; new kinds require widening.
	seen    []uint32
	dialect string // the document default for schema resources
	queue   []item
	at      *documentPath // shared physical path to the node being read
	busy    bool
}

// A need is a want and the URI it needs.
type need struct {
	uri string
	w   want
}

// An item is a k node to read, whose base outside it is base, at ptr in its
// document.
type item struct {
	v       value
	k       kind
	base    *url.URL
	ptr     *documentPath
	dialect string
}

// A fetch is a URI to retrieve, the wants that need it, the referrers to ask
// (see retrieve), and its document, read for them, or why it could not be.
type fetch struct {
	uri   string
	wants []want
	froms []string
	r     *reader
	err   error
}

// discover reads the entry document for its references and identifiers,
// retrieves the documents its references reach, in parallel within the
// load's bounds, each worker reading the document it retrieved, and reads
// those in turn. A reference resolves first to what a document read
// identifies; when nothing more can be read, the URIs still unidentified are
// retrieved, those least likely to be an $id first (see want), each rank once
// no URI of a lower one is left, and with them, as they are found, the URIs
// that only references to objects other than schemas need and nothing
// identifies. A URI no referrer asked may retrieve waits for one not yet
// asked, and is refused once none is left. Every node of every document is
// then numbered: the entry's first, then the others' by URI.
func (d *document) discover(ld *loading) error {
	if !d.reaches && !d.declares { // the entry alone
		d.trees, d.named = []*tree{d.tree}, map[string]*tree{d.uri: d.tree}
		d.pages = make([]atomic.Pointer[[factsPage]facts], len(d.nodes)/factsPage+1)
		return nil
	}
	ld.bound(d.uri)
	defer ld.close()
	dc := &discovery{document: d, ld: ld, readers: map[*tree]*reader{}, wants: map[string][]want{}, asked: map[string]bool{}}
	d.named = map[string]*tree{}
	dc.add(d.tree, ld.entry, d.newReader(d.tree, rootKind))
	for {
		dc.drain()
		var wave []fetch
		var froms []string // the referrers of each fetch of the wave, end to end
		for rank := range dc.fresh {
			for _, uri := range dc.fresh[rank] {
				if ws := dc.wants[uri]; ws != nil && !dc.asked[uri] && !strings.Contains(uri, "#") {
					n := len(froms)
					if froms = dc.referrers(froms, uri, ws); len(froms) > n {
						dc.asked[uri] = true
						wave = append(wave, fetch{uri: uri, wants: ws, froms: froms[n:len(froms):len(froms)]})
						delete(dc.wants, uri)
					}
				}
			}
			if dc.fresh[rank] = dc.fresh[rank][:0]; len(wave) > 0 {
				break
			}
		}
		if len(wave) == 0 {
			break
		}
		wave = dc.getAll(wave)
		if err := ld.ctx.Err(); err != nil {
			return fmt.Errorf("openapi: %w", err)
		}
		slices.SortFunc(wave, func(a, b fetch) int { return strings.Compare(a.uri, b.uri) })
		for _, f := range wave {
			switch {
			case errors.Is(f.err, errRefused):
				dc.refuse(f)
			case f.err != nil:
				dc.fail(f.uri, f.err)
			case !dc.add(f.r.t, f.uri, f.r): // its document was read already
				for _, w := range f.wants {
					dc.follow(f.uri, w)
				}
			}
		}
	}
	for k := range dc.refused {
		if dc.wants[k[0]] != nil { // no referrer may retrieve it
			dc.fail(k[0], notAdmitted(k[0]))
		}
	}
	slices.SortFunc(d.trees[1:], func(a, b *tree) int { return strings.Compare(a.uri, b.uri) })
	n := 0
	for _, t := range d.trees {
		if t.off = int32(n); n+len(t.nodes) > math.MaxInt32 {
			return fmt.Errorf("openapi: %s: the documents together hold too many values", d.uri)
		}
		n += len(t.nodes)
		t.located = dc.readers[t].located
	}
	d.pages = make([]atomic.Pointer[[factsPage]facts], n/factsPage+1)
	return nil
}

// getAll retrieves in parallel the document of each of wave, as the first of
// its referrers that may retrieve it, and reads it for what they need,
// adding to the wave each URI that only references to objects other than
// schemas in the documents read need, and that nothing read before
// identifies, the wave holding it already. It returns the wave when no more
// is to be retrieved, or when ctx is done.
func (dc *discovery) getAll(wave []fetch) []fetch {
	var mu sync.Mutex
	more := sync.NewCond(&mu)
	next, busy := 0, 0
	var froms []string // the referrers of the fetches added, end to end
	var wg sync.WaitGroup
	for range fetches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf *[32 << 10]byte
			var scratch reader // its queue, reused
			mu.Lock()
			defer func() {
				if mu.Unlock(); buf != nil {
					buffers.Put(buf)
				}
			}()
			for {
				for next == len(wave) && busy > 0 && dc.ld.ctx.Err() == nil {
					more.Wait()
				}
				if next == len(wave) || dc.ld.ctx.Err() != nil {
					more.Broadcast()
					return
				}
				i := next
				next, busy = next+1, busy+1
				f := wave[i]
				mu.Unlock()
				if buf == nil {
					buf = buffers.Get().(*[32 << 10]byte)
				}
				r, err := dc.read(f, buf[:], &scratch)
				mu.Lock()
				wave[i].r, wave[i].err, busy = r, err, busy-1
				for j := 0; r != nil && j < len(r.refs); { // a run of references to one URI at a time
					uri, k, need := r.refs[j].uri, j+1, r.refs[j].w.rank == 0
					for ; k < len(r.refs) && r.refs[k].uri == uri; k++ {
						need = need || r.refs[k].w.rank == 0
					}
					if need && !dc.asked[uri] && dc.named[uri] == nil && dc.ids[uri] == nil && dc.failed[uri] == nil && !strings.Contains(uri, "#") &&
						!dc.refused[[2]string{uri, r.t.uri}] {
						dc.asked[uri] = true
						ws := make([]want, 0, k-j)
						for _, n := range r.refs[j:k] {
							ws = append(ws, n.w)
						}
						froms = append(froms, r.t.uri)
						wave = append(wave, fetch{uri: uri, wants: ws, froms: froms[len(froms)-1 : len(froms) : len(froms)]})
					}
					j = k
				}
				more.Broadcast()
			}
		}()
	}
	done := make(chan struct{})
	dc.ld.pending = done
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return wave
	case <-dc.ld.ctx.Done():
		return nil
	}
}

// buffers holds the buffers documents are copied through.
var buffers = sync.Pool{New: func() any { return new([32 << 10]byte) }}

// read retrieves the document f names, as the first of its referrers that
// may retrieve it, and reads it for what f's wants need, copying its content
// through buf and reading with scratch's queue.
func (dc *discovery) read(f fetch, buf []byte, scratch *reader) (*reader, error) {
	t, err := dc.ld.get(f.uri, f.froms, buf)
	if err != nil {
		return nil, err
	}
	t.setEdition(dc.tree.edition)
	k := t.kind()
	r := dc.newReader(t, k)
	r.queue = append(scratch.queue[:0], r.queue...)
	for _, w := range f.wants {
		r.open(t.root(), k, t.refbase, nil, t.uri, r.dialect, w)
	}
	r.drain()
	scratch.queue, r.queue = r.queue, nil
	return r, nil
}

// newReader returns a reader of t, whose root is a k node, with the root to
// read when t holds what discovery needs throughout: an identifier, or, in
// an OpenAPI document, a reference that may reach another document.
func (d *document) newReader(t *tree, k kind) *reader {
	if t.edition == 0 {
		t.setEdition(d.tree.edition)
	}
	r := &reader{d: d, t: t}
	if k == rootKind && t.edition >= 31 {
		r.dialect = t.root().get("jsonSchemaDialect").string()
	}
	if t.refbase != t.base {
		r.claim(t.refbase.String(), t.root(), t.base, r.dialect)
	}
	if t.declares || t.reaches && k == rootKind {
		r.open(t.root(), k, t.refbase, nil, t.uri, r.dialect, want{k: k})
	}
	return r
}

// add records t, retrieved when uri was requested ("" for the entry), and r,
// its reader, and takes what r read, unless a document retrieved from the
// same URI is recorded, reporting whether it was not.
func (dc *discovery) add(t *tree, uri string, r *reader) bool {
	prev := dc.named[t.uri]
	if prev == nil {
		dc.named[t.uri], dc.trees, dc.readers[t] = t, append(dc.trees, t), r
		dc.identify(t.uri)
		dc.take(r)
	}
	if uri != "" && dc.named[uri] == nil {
		dc.named[uri] = cmp.Or(prev, t)
		dc.identify(uri)
	}
	return prev == nil
}

// referrers appends to froms, in order, the URIs of the documents that
// wants, the references to uri, are in, but those that may not retrieve it.
func (dc *discovery) referrers(froms []string, uri string, wants []want) []string {
	n := len(froms)
	for _, w := range wants {
		if from := w.t.uri; !dc.refused[[2]string{uri, from}] && (len(froms) == n || froms[len(froms)-1] != from) {
			froms = append(froms, from)
		}
	}
	slices.Sort(froms[n:])
	return froms[:n+len(slices.Compact(froms[n:]))]
}

// refuse records that none of f's referrers may retrieve f's URI, which
// waits, with f's wants, for a referrer not yet asked; the default boundary
// refuses every referrer alike.
func (dc *discovery) refuse(f fetch) {
	if dc.ld.AllowReference == nil {
		dc.fail(f.uri, notAdmitted(f.uri))
		return
	}
	if dc.refused == nil {
		dc.refused = map[[2]string]bool{}
	}
	for _, from := range f.froms {
		dc.refused[[2]string{f.uri, from}] = true
	}
	delete(dc.asked, f.uri)
	dc.wants[f.uri] = append(dc.wants[f.uri], f.wants...)
}

// fail records why uri cannot be retrieved: nothing reaches its document.
func (dc *discovery) fail(uri string, err error) {
	if dc.failed == nil {
		dc.failed = map[string]error{}
	}
	dc.failed[uri] = err
	delete(dc.wants, uri)
}

// identify notes that something now identifies uri.
func (dc *discovery) identify(uri string) {
	if _, ok := dc.wants[uri]; ok {
		dc.ready = append(dc.ready, uri)
	}
}

// take takes the identifiers and references r has read: each identifier
// claims its node, and each reference is followed, or waits for its URI.
func (dc *discovery) take(r *reader) {
	claimed, refs := r.claimed, r.refs
	r.claimed, r.refs = nil, nil
	for _, key := range claimed {
		dc.claim(key, r.ids[key])
	}
	for i := 0; i < len(refs); { // a run of references to one URI at a time
		uri, j := refs[i].uri, i+1
		for j < len(refs) && refs[j].uri == uri {
			j++
		}
		switch {
		case dc.named[uri] != nil || dc.ids[uri] != nil:
			for _, n := range refs[i:j] {
				dc.follow(uri, n.w)
			}
		case dc.failed[uri] == nil:
			ws, rank := dc.wants[uri], len(dc.fresh)-1
			for _, n := range refs[i:j] {
				ws, rank = append(ws, n.w), min(rank, n.w.rank)
			}
			dc.wants[uri], dc.fresh[rank] = ws, append(dc.fresh[rank], uri)
		}
		i = j
	}
	dc.wake(r)
}

// claim records that key identifies the node of rc and that of rc.other,
// if any: the identifier claims the first of the nodes recorded for it, in
// the order of their documents' URIs and their place in them, and, when
// there are several, the second, whichever document is read first.
func (dc *discovery) claim(key string, rc *claim) {
	c := dc.ids[key]
	if c == nil {
		if dc.ids == nil {
			dc.ids = map[string]*claim{}
		}
		if dc.identify(key); rc.other == nil {
			dc.ids[key] = rc
			return
		}
	}
	var all []claim
	for _, x := range [...]*claim{c, rc, c.otherOf(), rc.other} {
		if x != nil && !slices.ContainsFunc(all, func(y claim) bool { return y.v == x.v }) {
			all = append(all, *x)
		}
	}
	slices.SortFunc(all, func(a, b claim) int { return cmp.Or(strings.Compare(a.v.t.uri, b.v.t.uri), cmp.Compare(a.v.i, b.v.i)) })
	first := all[0]
	if first.other = nil; len(all) > 1 {
		all[1].other = nil
		first.other = &all[1]
	}
	dc.ids[key] = &first
}

func (c *claim) otherOf() *claim {
	if c == nil {
		return nil
	}
	return c.other
}

// wake marks r busy when it has something to read or for the discovery to
// take.
func (dc *discovery) wake(r *reader) {
	if !r.busy && len(r.queue)+len(r.claimed)+len(r.refs) > 0 {
		r.busy, dc.busy = true, append(dc.busy, r)
	}
}

// follow queues the node w needs of what uri identifies, a document or a
// schema, to be read by the reader of its document.
func (dc *discovery) follow(uri string, w want) {
	if t := dc.named[uri]; t != nil {
		dc.readers[t].open(t.root(), t.kind(), t.refbase, nil, t.uri, dc.readers[t].dialect, w)
		dc.wake(dc.readers[t])
	} else if c := dc.ids[uri]; c != nil {
		k, base := schemaKind, c.base
		if c.v.i == 0 && c.v.t.refbase.String() == uri {
			k, base = c.v.t.kind(), c.v.t.refbase
		}
		dc.readers[c.v.t].open(c.v, k, base, c.ptr, uri, c.dialect, w)
		dc.wake(dc.readers[c.v.t])
	}
}

// drain reads every item, and follows every want something identifies,
// until none is left.
func (dc *discovery) drain() {
	for {
		if n := len(dc.busy); n > 0 {
			r := dc.busy[n-1]
			dc.busy, r.busy = dc.busy[:n-1], false
			r.drain()
			dc.take(r)
		} else if len(dc.ready) > 0 {
			uri := dc.ready[0]
			dc.ready = dc.ready[1:]
			if t := dc.named[uri]; t == nil || t.reaches || t.declares { // else nothing in it is read
				for _, w := range dc.wants[uri] {
					dc.follow(uri, w)
				}
			}
			delete(dc.wants, uri)
		} else {
			return
		}
	}
}

// open queues the node w needs under n, a k node whose base outside it is
// base, at ptr, which uri identifies, to be read, unless it is read already
// or its document holds nothing discovery needs; one a plain name names that
// r has not read is needed of the discovery.
func (r *reader) open(n value, k kind, base *url.URL, ptr *documentPath, uri, dialect string, w want) {
	if !r.t.reaches && !r.t.declares {
		return
	}
	frag, err := url.PathUnescape(w.frag)
	switch {
	case err != nil:
		return
	case frag != "" && frag[0] == '/':
		n, base, dialect = descend(n, k, frag, base, dialect)
		if r.t.declares {
			ptr = &documentPath{parent: ptr, part: frag}
		}
	case frag != "":
		c := r.ids[uri+"#"+frag]
		if c == nil {
			r.refs = append(r.refs, need{uri + "#" + frag, want{"", w.k, w.t, w.rank}})
			return
		}
		n, base, ptr, dialect = c.v, c.base, c.ptr, c.dialect
	}
	if !n.ok() {
		return
	}
	if r.seen == nil {
		r.seen = make([]uint32, len(r.t.nodes))
	}
	if r.seen[n.i]&(3<<(2*w.k)) == 0 {
		r.seen[n.i] |= 1 << (2 * w.k)
		r.queue = append(r.queue, item{n, w.k, base, ptr, dialect})
	}
}

// drain reads every item queued.
func (r *reader) drain() {
	for n := len(r.queue); n > 0; n = len(r.queue) {
		it := r.queue[n-1]
		r.queue = r.queue[:n-1]
		r.at = it.ptr
		r.visit(it.v, it.k, it.base, it.dialect)
	}
}

// visit reads v as a k node whose base outside it is base: its identifiers,
// its references, and the nodes it holds.
func (r *reader) visit(v value, k kind, base *url.URL, effective string) {
	if r.seen[v.i]&(2<<(2*k)) != 0 || k == anyKind && r.seen[v.i]&(3<<(2*schemaKind)) != 0 {
		return
	}
	r.seen[v.i] |= 2 << (2 * k)
	switch {
	case k == anyKind && v.kind() == '[':
		i := 0
		for _, m := range v.members() {
			r.into(strconv.Itoa(i), m, anyKind, '1', base, effective)
			i++
		}
		return
	case v.kind() != '{' || k == dataKind:
		return
	}
	var ref, dynamicRef, id, anchor, dynamicAnchor, disc, dialect value
	for name, m := range v.members() {
		switch name {
		case "$ref":
			ref = m
		case "$dynamicRef":
			dynamicRef = m
		case "$id":
			id = m
		case "$anchor":
			anchor = m
		case "$dynamicAnchor":
			dynamicAnchor = m
		case "discriminator":
			disc = m
		case "$schema":
			dialect = m
		}
	}
	if k == anyKind && (dialect.ok() || id.ok() || anchor.ok() || dynamicAnchor.ok()) {
		k = schemaIDsKind
	}
	if r.t.edition <= 30 {
		id, anchor, dynamicAnchor, dynamicRef, dialect = value{}, value{}, value{}, value{}, value{}
	}
	if dialect.kind() == '"' {
		effective = dialect.string()
	}
	switch {
	case (k == schemaKind || k == schemaIDsKind || k == anyKind) && effective != "" && !ownDialect(effective):
		return // another dialect: its identifiers and references are not read
	case k == schemaKind || k == schemaIDsKind || k == anyKind:
		outer := base
		if id.kind() == '"' {
			if u, err := base.Parse(id.text()); err == nil {
				u.Fragment, u.RawFragment = "", ""
				base = u
				r.claim(u.String(), v, outer, effective)
			}
		}
		for _, a := range [...]value{anchor, dynamicAnchor} {
			if a.kind() == '"' {
				r.claim(base.String()+"#"+a.text(), v, outer, effective)
			}
		}
		if k == schemaKind {
			r.reference(ref, k, base)
			if r.t.edition <= 30 && ref.kind() == '"' {
				return
			}
			r.reference(dynamicRef, k, base)
			if r.t.edition == 32 {
				r.reference(disc.get("defaultMapping"), k, base)
			}
			for _, m := range disc.get("mapping").members() {
				if r.t.edition == 20 {
					break
				}
				if m.kind() == '"' && !componentName.MatchString(m.text()) {
					r.reference(m, k, base)
				}
			}
		}
	case k == pathItemKind:
		r.reference(ref, k, base)
	case (k >= parameterKind && k <= refKind || k == mediaKind && r.t.edition == 32) && ref.kind() == '"': // a Reference Object
		r.reference(ref, k, base)
		return
	}
	if r.t.edition == 32 && (k == rootKind || k == operationKind) {
		for _, req := range v.get("security").members() {
			for name := range req.members() {
				if !r.d.schemeComponent(name, r.t).ok() {
					r.referenceText(name, refKind, base)
				}
			}
		}
	}
	for name, m := range v.members() {
		lookup := k
		if lookup == schemaIDsKind {
			lookup = schemaKind
		}
		s, ok := modelSlot(lookup, name, r.t.edition)
		switch k {
		case anyKind:
			s, ok = slot{anyKind, '1'}, true
		case schemaIDsKind:
			s.k = schemaIDsKind

		case callbackKind:
			s, ok = slot{pathItemKind, '1'}, !strings.HasPrefix(name, "x-")
		}
		if ok {
			r.into(name, m, s.k, s.how, base, effective)
		}
	}
}

// into visits m, the member name of the node being read, as a k node, or as
// an array or map of them, as how says.
func (r *reader) into(name string, m value, k kind, how byte, base *url.URL, dialect string) {
	parent := r.at
	// Only identifier claims retain physical paths during discovery.
	if r.t.declares {
		r.at = &documentPath{parent: parent, part: name, token: true}
	}
	switch {
	case how == '1':
		r.visit(m, k, base, dialect)
	case m.kind() == how || how == 'x' && m.kind() == '{':
		i := 0
		for key, c := range m.members() {
			if how == '[' {
				key = strconv.Itoa(i)
				i++
			} else if how == 'x' && strings.HasPrefix(key, "x-") {
				continue // an extension of a Paths, Responses or Callback Object
			}
			r.into(key, c, k, '1', base, dialect)
		}
	}
	r.at = parent
}

// claim records that key, an absolute URI, identifies v, whose base outside
// it is base.
func (r *reader) claim(key string, v value, base *url.URL, dialect string) {
	switch c := r.ids[key]; {
	case c == nil:
		if r.ids == nil {
			r.ids = map[string]*claim{}
		}
		r.ids[key], r.claimed = &claim{v: v, ptr: r.at, base: base, dialect: dialect}, append(r.claimed, key)
	case c.v != v && c.other == nil:
		c.other, r.claimed = &claim{v: v, ptr: r.at}, append(r.claimed, key)
	}
}

// A documentPath shares its ancestors with sibling and child identifiers.
// Discovery never copies a full pointer for each nested schema; only a
// requested Source, resolution result or diagnostic materializes the text.
type documentPath struct {
	parent *documentPath
	part   string
	token  bool // one raw token, otherwise a JSON Pointer suffix already escaped
}

func (p *documentPath) pointer() string {
	var parts []*documentPath
	for at := p; at != nil; at = at.parent {
		parts = append(parts, at)
	}
	var b strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		at := parts[i]
		if at.token {
			b.WriteByte('/')
			b.WriteString(escapeToken(at.part))
		} else {
			b.WriteString(at.part)
		}
	}
	return b.String()
}

// reference records ref, if it is a string, a reference of a k node whose
// base is base, and where it leads: the node it names is read as a k node,
// by r when r identifies its URI, and otherwise once something does or its
// document is retrieved. A reference that never retrieves (a relative one
// with no base, one with userinfo, one to a file URL that names a host) is
// left for its use to report.
func (r *reader) reference(ref value, k kind, base *url.URL) {
	if ref.kind() != '"' {
		return
	}
	r.referenceValue(ref, ref.text(), k, base)
}

func (r *reader) referenceText(text string, k kind, base *url.URL) {
	r.referenceValue(value{}, text, k, base)
}

func (r *reader) referenceValue(ref value, text string, k kind, base *url.URL) {
	t := r.t
	uri, frag, rank := t.uri, "", 0
	if f, local := strings.CutPrefix(text, "#"); local && base == t.base {
		frag = f
	} else {
		doc, f, _ := strings.Cut(text, "#")
		res := r.d.resolve(t, base, doc)
		if res.err != nil {
			return
		}
		if uri, frag = res.uri, f; ref.ok() && base != t.base { // at compile, only t's base is known
			if r.located == nil {
				r.located = map[int32]location{}
			}
			r.located[ref.i] = location{uri, frag}
		}
		if k == schemaKind {
			rank = 1 + res.abs
		}
	}
	w := want{frag, k, t, rank}
	switch c := r.ids[uri]; {
	case c != nil:
		kind, base := schemaKind, c.base
		if c.v.i == 0 && c.v.t.refbase.String() == uri {
			kind, base = c.v.t.kind(), c.v.t.refbase
		}
		r.open(c.v, kind, base, c.ptr, uri, c.dialect, w)
	case uri == t.uri:
		r.open(t.root(), t.kind(), t.refbase, nil, uri, r.dialect, w)
	default:
		r.refs = append(r.refs, need{uri, w})
	}
}

// descend returns the node the JSON Pointer ptr names under v, a k node
// whose base outside it is base, and the base outside that node: the $id of
// each schema on the way sets it.
func descend(v value, k kind, ptr string, base *url.URL, dialect string) (value, *url.URL, string) {
	how := byte('1')
	for ptr != "" && v.ok() {
		if how == '1' && (k == schemaKind || k == anyKind) && v.t.declares && v.t.edition >= 31 {
			if local := v.get("$schema"); local.kind() == '"' {
				dialect = local.string()
			}
			if id := v.get("$id"); id.kind() == '"' && (dialect == "" || ownDialect(dialect)) {
				if u, err := base.Parse(id.text()); err == nil {
					u.Fragment, u.RawFragment = "", ""
					base = u
				}
			}
		}
		tok, rest, ok := nextToken(ptr)
		if !ok {
			return value{}, base, dialect
		}
		edition := v.t.edition
		v, ptr = v.step(tok), rest
		switch name, _ := unescapeToken(tok); {
		case how != '1':
			how = '1'
		case k == callbackKind:
			k = pathItemKind
		case k != anyKind:
			s, ok := modelSlot(k, name, edition)
			if !ok {
				s = slot{dataKind, '1'}
			}
			k, how = s.k, s.how
		}
	}
	return v, base, dialect
}

// ownDialect reports whether the schema dialect d reads identifiers as JSON
// Schema 2020-12 does: 2020-12's own, or one of OpenAPI's.
func ownDialect(d string) bool {
	return d == "https://json-schema.org/draft/2020-12/schema" ||
		strings.HasPrefix(d, "https://spec.openapis.org/oas/3.1/dialect/") || strings.HasPrefix(d, "https://spec.openapis.org/oas/3.2/dialect/")
}

// target returns the node the reference ref, a string, names, and its JSON
// Pointer in its document, or why it names none: against the base of its
// document or, inside a schema, of the nearest $id, it names what a
// document read identifies, by its retrieval URI, a schema's $id, or a plain
// name a schema declares.
func (d *document) target(ref value) (value, string, error) {
	t, text := ref.t, ref.text()
	uri, frag := t.uri, ""
	if l, ok := t.located[ref.i]; ok {
		uri, frag = l.uri, l.frag
	} else if f, local := strings.CutPrefix(text, "#"); local {
		frag = f
	} else {
		doc, f, _ := strings.Cut(text, "#")
		r := d.resolve(t, t.refbase, doc)
		if r.err != nil {
			return value{}, "", fmt.Errorf("%w %q: %w", ErrUnresolved, cmp.Or(r.shown, safeURI(text)), r.err)
		}
		uri, frag = r.uri, f
	}
	return d.targetLocation(t, text, uri, frag)
}

func (d *document) targetName(t *tree, text string) (value, string, error) {
	doc, frag, _ := strings.Cut(text, "#")
	r := d.resolve(t, t.refbase, doc)
	if r.err != nil {
		return value{}, "", fmt.Errorf("%w: %w", ErrUnresolved, r.err)
	}
	return d.targetLocation(t, text, r.uri, frag)
}

func (d *document) targetLocation(t *tree, text, uri, frag string) (value, string, error) {
	n, ptr := t.root(), ""
	if uri != t.uri || d.ids[uri] != nil {
		var err error
		if n, ptr, err = d.node(uri); err != nil {
			return value{}, "", fmt.Errorf("%w %q: %w", ErrUnresolved, safeURI(text), err)
		}
	}
	var err error
	if strings.IndexByte(frag, '%') >= 0 {
		if frag, err = url.PathUnescape(frag); err != nil {
			return value{}, "", fmt.Errorf("%w %q: the fragment cannot be percent-decoded", ErrUnresolved, safeURI(text))
		}
	}
	switch {
	case frag == "":
	case frag[0] == '/':
		if n, ptr = n.at(frag), ptr+frag; !n.ok() {
			return value{}, "", fmt.Errorf("%w %q: no such node", ErrUnresolved, safeURI(text))
		}
	default:
		if n, ptr, err = d.node(uri + "#" + frag); err != nil {
			return value{}, "", fmt.Errorf("%w %q: %w", ErrUnresolved, safeURI(text), err)
		}
	}
	return n, ptr, nil
}

// A location is where a reference leads: the absolute URI it names, without
// its fragment, and the fragment as written.
type location struct{ uri, frag string }

// A resolved is what the part of a reference before its fragment resolves
// to against a base: an absolute URI, and whether it was written as one
// (1), or why it names nothing to retrieve, with the reference as it may be
// shown.
type resolved struct {
	uri   string
	abs   int
	err   error
	shown string
}

// A resolving is a reference's part before its fragment and a base,
// resolved once: a document's directory URI, against which a relative path
// resolves as against any URI in it, or else the base itself.
type resolving struct {
	base     *url.URL
	dir, doc string
}

// resolve returns what doc, the part of a reference before its fragment in
// the document t, resolves to against base. Against t's own base, a relative
// path of unreserved characters and slashes with no dot segment resolves, as
// RFC 3986 section 5.2 resolves it, to t's directory URI followed by it, and
// any other relative path once for each directory; the rest resolve once for
// each base.
func (d *document) resolve(t *tree, base *url.URL, doc string) resolved {
	k := resolving{base: base, doc: doc}
	if i := strings.IndexAny(doc, ":/?"); base == t.base && t.dir != "" && doc != "" && (i < 0 || i > 0 && doc[i] == '/') {
		if plainPath(doc) {
			return resolved{uri: t.canonicalDir() + doc}
		}
		k = resolving{dir: t.dir, doc: doc}
	}
	if r, ok := d.resolutions.Load(k); ok {
		return *r.(*resolved)
	}
	r, _ := d.resolutions.LoadOrStore(k, resolve(base, doc))
	return *r.(*resolved)
}

// canonicalDir returns the URI of t's directory as net/url writes it,
// resolving it once.
func (t *tree) canonicalDir() string {
	if dir := t.canonical.Load(); dir != nil {
		return *dir
	}
	dir := t.base.ResolveReference(&url.URL{Path: "."}).String()
	t.canonical.CompareAndSwap(nil, &dir)
	return *t.canonical.Load()
}

// plainPath reports whether doc, a relative path, has only unreserved
// characters and slashes (RFC 3986 section 2.3) and no segment that is "."
// or "..".
func plainPath(doc string) bool {
	for i, seg := 0, 0; i <= len(doc); i++ {
		if i == len(doc) || doc[i] == '/' {
			if s := doc[seg:i]; s == "." || s == ".." {
				return false
			}
			seg = i + 1
		} else if c := doc[i]; unreservedSet[c] != 1 {
			return false
		}
	}
	return true
}

// resolve returns what doc, the part of a reference before its fragment,
// resolves to against base.
func resolve(base *url.URL, doc string) *resolved {
	r := &resolved{shown: safeURI(doc)}
	u, err := url.Parse(doc)
	switch {
	case err != nil:
		r.err = errors.New("not a URI reference")
	case !u.IsAbs() && base.Opaque != "":
		r.err = errors.New("a relative reference in a document with no base URI")
	default:
		if u.IsAbs() {
			r.abs = 1
		}
		switch u = base.ResolveReference(u); {
		case u.User != nil: // not shown
			u.User = nil
			r.err, r.shown = errors.New("a URI with userinfo is never retrieved (RFC 9110 section 4.2.4)"), u.String()
		case u.Scheme == "file" && u.Host != "" && u.Host != "localhost":
			r.err = errors.New("a file URL cannot name a host other than localhost (RFC 8089)")
		default:
			u.Fragment, u.RawFragment = "", ""
			r.uri = u.String()
		}
	}
	return r
}

// node returns the node an absolute URI identifies, a document's root or a
// schema, and its JSON Pointer in its document, or why it identifies none.
func (d *document) node(uri string) (value, string, error) {
	t, c := d.named[uri], d.ids[uri]
	switch {
	case c != nil && c.other != nil:
		return value{}, "", fmt.Errorf("%s names both %s and %s", uri, c.source(), c.other.source())
	case c != nil && t != nil && c.v != t.root():
		return value{}, "", fmt.Errorf("%s names both the document %s and %s", uri, t.uri, c.source())
	case c != nil:
		return c.v, c.ptr.pointer(), nil
	case t != nil:
		return t.root(), "", nil
	case d.failed[uri] != nil:
		return value{}, "", d.failed[uri]
	}
	return value{}, "", fmt.Errorf("no document loaded or schema declared has the URI %s", uri)
}
