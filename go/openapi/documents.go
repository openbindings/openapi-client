package openapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
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
	origins []*url.URL   // Origins, then the entry's original origin if it is http or https
	dir     string       // a file entry's directory, cleaned and its symlinks resolved, or ""
}

// start begins a load with l's settings and the HTTPClient of opts.
func (l *Loader) start(ctx context.Context, opts *Options) (*loading, error) {
	if len(l.Origins) > 0 && l.AllowReference != nil {
		return nil, errors.New("openapi: a Loader cannot set both Origins and AllowReference")
	}
	ld := &loading{Loader: l, ctx: ctx, hc: http.DefaultClient, limit: limit(l.MaxBytes, 64<<20)}
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

// bound sets the default boundary of an entry requested as uri and
// retrieved from final: an http or https entry's origin as requested, or a
// file entry's directory.
func (ld *loading) bound(uri, final string) {
	if u, err := url.Parse(uri); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		ld.origins = append(ld.origins, u)
	} else if u, err := url.Parse(final); err == nil && u.Scheme == "file" {
		ld.dir, _ = filepath.EvalSymlinks(filepath.Dir(filepath.FromSlash(u.Path)))
	}
}

// admit reports whether the document retrieved from from may retrieve the
// one at to.
func (ld *loading) admit(from, to string) bool {
	if ld.AllowReference != nil {
		return ld.AllowReference(from, to)
	}
	u, err := url.Parse(to)
	switch {
	case err != nil:
	case u.Scheme == "http" || u.Scheme == "https":
		return slices.ContainsFunc(ld.origins, func(o *url.URL) bool { return sameOrigin(o, u) })
	case u.Scheme == "file" && ld.dir != "":
		path, err := filepath.EvalSymlinks(filepath.FromSlash(u.Path))
		rel, err2 := filepath.Rel(ld.dir, path)
		return err == nil && err2 == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return false
}

// notAdmitted is why uri, which admission refused, is not retrieved.
func notAdmitted(uri string) error {
	u, err := url.Parse(uri)
	if err != nil { // not shown, as its userinfo cannot be found
		return errors.New("a URI that cannot be parsed is outside the documents the Loader admits")
	}
	return fmt.Errorf("%s is outside the documents the Loader admits", u.Redacted())
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

// get retrieves and reads the document at uri, which the document retrieved
// from from references.
func (ld *loading) get(uri, from string) (*tree, error) {
	content, final, err := ld.retrieve(uri, from)
	if err != nil {
		return nil, err
	}
	return readTree(ld.ctx, content, final)
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
)

// A slot is what a member of an object holds: a k node ('1'), or an array
// ('[') or map ('{') of them.
type slot struct {
	k   kind
	how byte
}

// model maps the member names of each kind of OpenAPI 3.1 object to what
// they hold (OpenAPI 3.1.2 section 4.8; JSON Schema 2020-12 core sections
// 8.2.4, 10 and 11, and validation section 8.5, for a schema's subschemas).
var model = [...]map[string]slot{
	rootKind: {"paths": {pathItemKind, '{'}, "webhooks": {pathItemKind, '{'}, "components": {componentsKind, '1'}},
	componentsKind: {"schemas": {schemaKind, '{'}, "responses": {responseKind, '{'}, "parameters": {parameterKind, '{'},
		"examples": {refKind, '{'}, "requestBodies": {requestBodyKind, '{'}, "headers": {parameterKind, '{'},
		"securitySchemes": {refKind, '{'}, "links": {refKind, '{'}, "callbacks": {callbackKind, '{'}, "pathItems": {pathItemKind, '{'}},
	pathItemKind: {"parameters": {parameterKind, '['}, "get": {operationKind, '1'}, "put": {operationKind, '1'},
		"post": {operationKind, '1'}, "delete": {operationKind, '1'}, "options": {operationKind, '1'},
		"head": {operationKind, '1'}, "patch": {operationKind, '1'}, "trace": {operationKind, '1'}},
	operationKind: {"parameters": {parameterKind, '['}, "requestBody": {requestBodyKind, '1'},
		"responses": {responseKind, '{'}, "callbacks": {callbackKind, '{'}},
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

// A claim is the node an identifier names, its JSON Pointer in its
// document, and the base outside it, with another node that the same
// identifier names, if one does.
type claim struct {
	v     value
	ptr   string
	base  *url.URL
	other *claim
}

func (c *claim) source() string { return c.v.t.source(c.ptr) }

// A discovery reads the documents a load reaches: what it has yet to read,
// and the references to URIs that nothing read identifies yet.
type discovery struct {
	*document
	ld    *loading
	queue []item
	seen  map[visit]bool
	wants map[string][]want // by the URI they need, without its fragment, or a plain name's
	ready []string          // the URIs of wants that something now identifies
	at    string            // the JSON Pointer of the item being read
	path  []string          // and the reference tokens from it to the node being read
}

// An item is a k node to read, whose base is base, at ptr in its document.
type item struct {
	v    value
	k    kind
	base *url.URL
	ptr  string
}

type visit struct {
	v value
	k kind
}

// A want is a reference that needs the node a URI and the fragment frag
// name, read as a k node, from the document t, and how likely an $id is to
// identify the URI: 0 for a reference to an object other than a schema, 1
// for a schema reference written as a relative reference, 2 for one written
// as an absolute URI.
type want struct {
	frag string
	k    kind
	t    *tree
	rank int
}

// discover reads the entry document for its references and identifiers,
// retrieves the documents its references reach, in parallel within the
// load's bounds, and reads those in turn. A reference resolves first to
// what a document read identifies; when nothing more can be read, the URIs
// still unidentified are retrieved, those least likely to be an $id first
// (see want), each rank once no URI of a lower one is left. Every node of
// every document is then numbered: the entry's first, then the others' by
// URI.
func (d *document) discover(ld *loading) error {
	dc := &discovery{document: d, ld: ld, seen: map[visit]bool{}, wants: map[string][]want{}}
	d.named = map[string]*tree{}
	dc.add(d.tree, "")
	for {
		dc.drain()
		var waves [3][]string
		for uri, ws := range dc.wants {
			if !strings.Contains(uri, "#") { // not a plain name no schema declares
				r := slices.MinFunc(ws, func(a, b want) int { return a.rank - b.rank }).rank
				waves[r] = append(waves[r], uri)
			}
		}
		i := 0
		for i < len(waves)-1 && len(waves[i]) == 0 {
			i++
		}
		wave, froms := waves[i], []string(nil)
		if len(wave) == 0 {
			break
		}
		slices.Sort(wave)
		for _, uri := range wave {
			froms = append(froms, dc.wants[uri][0].t.uri)
		}
		got, errs := ld.getAll(wave, froms)
		if err := ld.ctx.Err(); err != nil {
			return fmt.Errorf("openapi: %w", err)
		}
		for i, uri := range wave {
			if errs[i] == nil {
				dc.add(got[i], uri)
				continue
			}
			if d.failed == nil {
				d.failed = map[string]error{}
			}
			d.failed[uri] = errs[i]
			delete(dc.wants, uri)
		}
	}
	slices.SortFunc(d.trees[1:], func(a, b *tree) int { return strings.Compare(a.uri, b.uri) })
	n := 0
	for _, t := range d.trees {
		if t.off = int32(n); n+len(t.nodes) > math.MaxInt32 {
			return fmt.Errorf("openapi: %s: the documents together hold too many values", d.uri)
		}
		n += len(t.nodes)
	}
	d.pages = make([]atomic.Pointer[[factsPage]facts], n/factsPage+1)
	return nil
}

// getAll retrieves and reads the document at each of uris, each of which
// the document retrieved from the same of froms references, in parallel,
// returning when they are read or ctx is done.
func (ld *loading) getAll(uris, froms []string) ([]*tree, []error) {
	got, errs := make([]*tree, len(uris)), make([]error, len(uris))
	var wg sync.WaitGroup
	sem := make(chan struct{}, fetches)
	for i := range uris {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				got[i], errs[i] = ld.get(uris[i], froms[i])
				<-sem
			case <-ld.ctx.Done():
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ld.ctx.Done():
	}
	return got, errs
}

// add records t, retrieved when uri was requested ("" for the entry), unless
// a document retrieved from the same URI is recorded, and reads it if it
// holds what discovery needs.
func (dc *discovery) add(t *tree, uri string) {
	if prev := dc.named[t.uri]; prev != nil {
		t = prev
	} else {
		dc.named[t.uri], dc.trees = t, append(dc.trees, t)
		dc.identify(t.uri)
		if k := t.kind(); t.declares || t.reaches && k == rootKind {
			dc.seen[visit{t.root(), k}] = true
			dc.queue = append(dc.queue, item{t.root(), k, t.base, ""})
		}
	}
	if uri != "" && dc.named[uri] == nil {
		dc.named[uri] = t
		dc.identify(uri)
	}
}

// identify notes that something now identifies uri.
func (dc *discovery) identify(uri string) {
	if _, ok := dc.wants[uri]; ok {
		dc.ready = append(dc.ready, uri)
	}
}

// drain reads every item, and every want something identifies, until none
// is left.
func (dc *discovery) drain() {
	for {
		if n := len(dc.queue); n > 0 {
			it := dc.queue[n-1]
			dc.queue = dc.queue[:n-1]
			dc.at, dc.path = it.ptr, dc.path[:0]
			dc.visit(it.v, it.k, it.base)
		} else if len(dc.ready) > 0 {
			uri := dc.ready[0]
			dc.ready = dc.ready[1:]
			for _, w := range dc.wants[uri] {
				dc.follow(uri, w)
			}
			delete(dc.wants, uri)
		} else {
			return
		}
	}
}

// visit reads v as a k node whose base outside it is base: its identifiers,
// its references, and the nodes it holds.
func (dc *discovery) visit(v value, k kind, base *url.URL) {
	if k == anyKind && v.kind() == '[' {
		i := 0
		for _, m := range v.members() {
			dc.into(strconv.Itoa(i), m, anyKind, '1', base)
			i++
		}
	}
	if v.kind() != '{' || k == dataKind {
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
	switch {
	case (k == schemaKind || k == anyKind) && dialect.ok() && !ownDialect(dialect.string()):
		return // another dialect: its identifiers and references are not read
	case k == schemaKind || k == anyKind:
		outer := base
		if id.kind() == '"' {
			if u, err := base.Parse(id.text()); err == nil {
				u.Fragment, u.RawFragment = "", ""
				base = u
				dc.claim(u.String(), v, outer)
			}
		}
		for _, a := range [...]value{anchor, dynamicAnchor} {
			if a.kind() == '"' {
				dc.claim(base.String()+"#"+a.text(), v, outer)
			}
		}
		if k == anyKind {
			for name, m := range v.members() {
				dc.into(name, m, anyKind, '1', base)
			}
			return
		}
		dc.reference(ref, k, base)
		dc.reference(dynamicRef, k, base)
		for _, m := range disc.get("mapping").members() {
			if m.kind() == '"' && !componentName.MatchString(m.text()) {
				dc.reference(m, k, base)
			}
		}
	case k == pathItemKind:
		dc.reference(ref, k, base)
	case k >= parameterKind && ref.kind() == '"': // a Reference Object
		dc.reference(ref, k, base)
		return
	case k == callbackKind:
		for name, m := range v.members() {
			if !strings.HasPrefix(name, "x-") {
				dc.into(name, m, pathItemKind, '1', base)
			}
		}
		return
	}
	for name, m := range v.members() {
		if s, ok := model[k][name]; ok {
			dc.into(name, m, s.k, s.how, base)
		}
	}
}

// into visits m, the member name of the node being read, as a k node, or as
// an array or map of them, as how says.
func (dc *discovery) into(name string, m value, k kind, how byte, base *url.URL) {
	dc.path = append(dc.path, name)
	switch {
	case how == '1':
		dc.visit(m, k, base)
	case m.kind() == how:
		i := 0
		for key, c := range m.members() {
			if how == '[' {
				key = strconv.Itoa(i)
				i++
			} else if (k == pathItemKind || k == responseKind) && strings.HasPrefix(key, "x-") {
				continue // an extension of a Paths, Responses or Callback Object
			}
			dc.into(key, c, k, '1', base)
		}
	}
	dc.path = dc.path[:len(dc.path)-1]
}

// claim records that key, an absolute URI, identifies v, whose base outside
// it is base.
func (dc *discovery) claim(key string, v value, base *url.URL) {
	switch c := dc.ids[key]; {
	case c == nil:
		if dc.ids == nil {
			dc.ids = map[string]*claim{}
		}
		dc.ids[key] = &claim{v: v, ptr: dc.pointer(), base: base}
		dc.identify(key)
	case c.v != v && c.other == nil:
		c.other = &claim{v: v, ptr: dc.pointer()}
	}
}

// pointer returns the JSON Pointer of the node being read.
func (dc *discovery) pointer() string {
	var b strings.Builder
	b.WriteString(dc.at)
	for _, tok := range dc.path {
		b.WriteByte('/')
		b.WriteString(escapeToken(tok))
	}
	return b.String()
}

// reference records ref, if it is a string, a reference of a k node whose
// base is base: the node it names is read as a k node, and its document
// retrieved unless something read identifies its URI. A reference that
// never retrieves (a relative one with no base, one with userinfo, one to a
// file URL that names a host) is left for its use to report.
func (dc *discovery) reference(ref value, k kind, base *url.URL) {
	if ref.kind() != '"' {
		return
	}
	t, text := ref.t, ref.text()
	uri, frag, rank := t.uri, "", 0
	if f, local := strings.CutPrefix(text, "#"); local && base == t.base {
		frag = f
	} else {
		u, err := url.Parse(text)
		if err != nil || !u.IsAbs() && base.Opaque != "" {
			return
		}
		if k == schemaKind {
			rank = 1
			if u.IsAbs() {
				rank = 2
			}
		}
		if u = base.ResolveReference(u); base != t.base {
			if dc.under == nil {
				dc.under = map[value]string{}
			}
			dc.under[ref] = u.String()
		}
		if u.User != nil || u.Scheme == "file" && u.Host != "" && u.Host != "localhost" {
			return
		}
		frag, u.Fragment, u.RawFragment = u.EscapedFragment(), "", ""
		uri = u.String()
	}
	w := want{frag, k, t, rank}
	switch {
	case dc.named[uri] != nil || dc.ids[uri] != nil:
		dc.follow(uri, w)
	case dc.failed[uri] == nil:
		dc.wants[uri] = append(dc.wants[uri], w)
	}
}

// follow queues the node w needs of what uri identifies to be read, unless
// it is read already or its document needs no reading, or waits for the
// plain name it needs to be declared.
func (dc *discovery) follow(uri string, w want) {
	var n value
	k, base, ptr := schemaKind, (*url.URL)(nil), ""
	if c := dc.ids[uri]; c != nil {
		n, base, ptr = c.v, c.base, c.ptr
	}
	if t := dc.named[uri]; t != nil {
		n, k, base, ptr = t.root(), t.kind(), t.base, ""
	}
	frag, err := url.PathUnescape(w.frag)
	switch {
	case err != nil:
		return
	case frag != "" && frag[0] == '/':
		n, base = descend(n, k, frag, base)
		ptr += frag
	case frag != "":
		name := uri + "#" + frag
		c := dc.ids[name]
		if c == nil {
			dc.wants[name] = append(dc.wants[name], want{"", w.k, w.t, w.rank})
			return
		}
		n, base, ptr = c.v, c.base, c.ptr
	}
	if !n.ok() || !n.t.reaches && !n.t.declares || dc.seen[visit{n, w.k}] {
		return
	}
	dc.seen[visit{n, w.k}] = true
	dc.queue = append(dc.queue, item{n, w.k, base, ptr})
}

// descend returns the node the JSON Pointer ptr names under v, a k node
// whose base outside it is base, and the base outside that node: the $id of
// each schema on the way sets it.
func descend(v value, k kind, ptr string, base *url.URL) (value, *url.URL) {
	how := byte('1')
	for ptr != "" && v.ok() {
		if how == '1' && (k == schemaKind || k == anyKind) {
			if id := v.get("$id"); id.kind() == '"' {
				if u, err := base.Parse(id.text()); err == nil {
					u.Fragment, u.RawFragment = "", ""
					base = u
				}
			}
		}
		tok, rest, ok := nextToken(ptr)
		if !ok {
			return value{}, base
		}
		v, ptr = v.step(tok), rest
		switch name, _ := unescapeToken(tok); {
		case how != '1':
			how = '1'
		case k == callbackKind:
			k = pathItemKind
		case k != anyKind:
			s, ok := model[k][name]
			if !ok {
				s = slot{dataKind, '1'}
			}
			k, how = s.k, s.how
		}
	}
	return v, base
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
	abs, under := d.under[ref]
	if f, local := strings.CutPrefix(text, "#"); local && !under {
		frag = f
	} else {
		if !under {
			abs = text
		}
		u, err := url.Parse(abs)
		switch {
		case err != nil:
			return value{}, "", fmt.Errorf("%w %q: not a URI reference", ErrUnresolved, text)
		case !u.IsAbs() && t.base.Opaque != "":
			return value{}, "", fmt.Errorf("%w %q: a relative reference in a document with no base URI", ErrUnresolved, text)
		case !under:
			u = t.base.ResolveReference(u)
		}
		switch {
		case u.User != nil: // not shown
			u.User = nil
			return value{}, "", fmt.Errorf("%w %q: a URI with userinfo is never retrieved (RFC 9110 section 4.2.4)", ErrUnresolved, u)
		case u.Scheme == "file" && u.Host != "" && u.Host != "localhost":
			return value{}, "", fmt.Errorf("%w %q: a file URL cannot name a host other than localhost (RFC 8089)", ErrUnresolved, text)
		}
		frag, u.Fragment, u.RawFragment = u.EscapedFragment(), "", ""
		uri = u.String()
	}
	n, ptr := t.root(), ""
	if uri != t.uri || d.ids[uri] != nil {
		var err error
		if n, ptr, err = d.node(uri); err != nil {
			return value{}, "", fmt.Errorf("%w %q: %w", ErrUnresolved, text, err)
		}
	}
	var err error
	if strings.IndexByte(frag, '%') >= 0 {
		if frag, err = url.PathUnescape(frag); err != nil {
			return value{}, "", fmt.Errorf("%w %q: the fragment cannot be percent-decoded", ErrUnresolved, text)
		}
	}
	switch {
	case frag == "":
	case frag[0] == '/':
		if n, ptr = n.at(frag), ptr+frag; !n.ok() {
			return value{}, "", fmt.Errorf("%w %q: no such node", ErrUnresolved, text)
		}
	default:
		if n, ptr, err = d.node(uri + "#" + frag); err != nil {
			return value{}, "", fmt.Errorf("%w %q: %w", ErrUnresolved, text, err)
		}
	}
	return n, ptr, nil
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
		return c.v, c.ptr, nil
	case t != nil:
		return t.root(), "", nil
	case d.failed[uri] != nil:
		return value{}, "", d.failed[uri]
	}
	return value{}, "", fmt.Errorf("no document loaded or schema declared has the URI %s", uri)
}
