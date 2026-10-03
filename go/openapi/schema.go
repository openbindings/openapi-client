package openapi

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// schemaGraph is built only by schema inspection. It records schema positions
// and reference events, not complete pointer strings or copies of subtrees.
// Events in a Raw subtree occupy one interval in the document's node order.
type schemaGraph struct {
	nodes map[int32]*schemaNode
	edges []*schemaEdge
}

type schemaNode struct {
	v       value
	path    *documentPath
	depth   int
	base    *url.URL
	dialect string
	once    sync.Once
	handle  *Schema
	err     error // a legacy root reference failed to reach a schema
}

type schemaEdge struct {
	v       value
	owner   *schemaNode
	path    *documentPath
	depth   int
	keyword string // empty for a foreign resource, which interrupts enumeration
	once    sync.Once
	ref     SchemaReference // At is relative to the caller's Raw, filled on return
}

func schemaDialect(t *tree) string {
	if t.edition <= 30 {
		return ""
	}
	if d := t.root().str("jsonSchemaDialect"); d != "" {
		return d
	}
	if t.edition == 32 {
		return "https://spec.openapis.org/oas/3.2/dialect/2025-09-17"
	}
	return "https://spec.openapis.org/oas/3.1/dialect/base"
}

// schemaScope applies the resource's own declaration to its inherited scope.
// A foreign resource leaves its base outside the resource untouched.
func schemaScope(v value, base *url.URL, dialect string) (*url.URL, string) {
	if v.t.edition <= 30 {
		return base, ""
	}
	if local := v.get("$schema"); local.kind() == '"' {
		dialect = local.text()
	}
	if id := v.get("$id"); id.kind() == '"' && (dialect == "" || ownDialect(dialect)) {
		if u, err := base.Parse(id.text()); err == nil {
			u.Fragment, u.RawFragment = "", ""
			base = u
		}
	}
	return base, dialect
}

func (d *document) schemaGraph(t *tree) *schemaGraph {
	t.schemasOnce.Do(func() {
		g := &schemaGraph{nodes: map[int32]*schemaNode{}}
		b := schemaBuilder{g: g, seen: make([]uint32, len(t.nodes)), seeds: map[int32][]item{}}
		for _, it := range t.schemaSeeds {
			b.seeds[it.v.i] = append(b.seeds[it.v.i], it)
		}
		b.visit(t.root(), t.kind(), t.refbase, schemaDialect(t), nil, 0)
		// A referenced fragment can lie outside the root's known vocabulary.
		// Its context was established while loading, never by an accessor.
		for _, it := range t.schemaSeeds {
			if b.seen[it.v.i]&(1<<it.k) != 0 {
				continue
			}
			p, depth := schemaPath(it.ptr)
			dialect := it.dialect
			if dialect == "" {
				dialect = schemaDialect(t)
			}
			b.visit(it.v, it.k, it.base, dialect, p, depth)
		}
		slices.SortFunc(g.edges, func(a, b *schemaEdge) int {
			if a.v.i < b.v.i {
				return -1
			}
			if a.v.i > b.v.i {
				return 1
			}
			return 0
		})
		t.schemas = g
	})
	return t.schemas
}

// schemaPath turns a discovery path's occasional pointer suffix into linked
// tokens, so relative pointers cost only the length of the returned pointer.
func schemaPath(p *documentPath) (*documentPath, int) {
	var path *documentPath
	depth := 0
	for ptr := p.pointer(); ptr != ""; {
		tok, rest, _ := nextToken(ptr)
		name, _ := unescapeToken(tok)
		path = &documentPath{parent: path, part: name, token: true}
		depth++
		ptr = rest
	}
	return path, depth
}

// schemaBuilder uses the same edition-specific object model as discovery.
// Unclassified versionless documents are examined for resource declarations;
// a fragment's retained context takes precedence over that generic scan.
type schemaBuilder struct {
	g     *schemaGraph
	seen  []uint32
	seeds map[int32][]item
}

func (b *schemaBuilder) visit(v value, k kind, base *url.URL, dialect string, path *documentPath, depth int) {
	if k == anyKind {
		if seeds := b.seeds[v.i]; len(seeds) > 0 {
			for _, it := range seeds {
				b.visit(v, it.k, base, dialect, path, depth)
			}
			return
		}
		if v.get("$schema").ok() || v.get("$id").ok() || v.get("$anchor").ok() || v.get("$dynamicAnchor").ok() {
			k = schemaKind
		}
	}
	if b.seen[v.i]&(1<<k) != 0 {
		return
	}
	b.seen[v.i] |= 1 << k
	var n *schemaNode
	if k == schemaKind {
		if v.kind() != '{' && !(v.t.edition >= 31 && (v.kind() == 't' || v.kind() == 'f')) {
			return
		}
		if b.g.nodes[v.i] != nil {
			return
		}
		base, dialect = schemaScope(v, base, dialect)
		n = &schemaNode{v: v, path: path, depth: depth, base: base, dialect: dialect}
		b.g.nodes[v.i] = n
		if dialect != "" && !ownDialect(dialect) {
			b.g.edges = append(b.g.edges, &schemaEdge{v: v, owner: n, path: path, depth: depth})
			return
		}
	}
	if v.kind() != '{' && !(k == anyKind && v.kind() == '[') {
		return
	}
	ref := v.get("$ref")
	if ref.kind() == '"' {
		if k == schemaKind && v.t.edition <= 30 {
			b.edge(ref, n, path, depth, "$ref")
			return
		}
		if k >= parameterKind && k <= refKind || k == mediaKind && v.t.edition == 32 {
			return
		}
	}
	i := 0
	for name, m := range v.members() {
		if v.kind() == '[' {
			name = strconv.Itoa(i)
			i++
		}
		if k == schemaKind {
			switch name {
			case "$ref", "$dynamicRef":
				if name == "$ref" || v.t.edition >= 31 {
					b.edge(m, n, path, depth, name)
				}
			case "discriminator":
				if v.t.edition >= 30 {
					b.discriminator(m, n, path, depth)
				}
			}
		}
		s, ok := modelSlot(k, name, v.t.edition)
		switch k {
		case anyKind:
			s, ok = slot{anyKind, '1'}, true
		case callbackKind:
			s, ok = slot{pathItemKind, '1'}, !strings.HasPrefix(name, "x-")
		}
		if ok {
			b.into(name, m, s, base, dialect, path, depth)
		}
	}
}

func (b *schemaBuilder) into(name string, v value, s slot, base *url.URL, dialect string, path *documentPath, depth int) {
	path = &documentPath{parent: path, part: name, token: true}
	depth++
	if s.how == '1' {
		b.visit(v, s.k, base, dialect, path, depth)
		return
	}
	if v.kind() != s.how && !(s.how == 'x' && v.kind() == '{') {
		return
	}
	i := 0
	for key, c := range v.members() {
		if s.how == '[' {
			key = strconv.Itoa(i)
			i++
		} else if s.how == 'x' && strings.HasPrefix(key, "x-") {
			continue
		}
		b.into(key, c, slot{s.k, '1'}, base, dialect, path, depth)
	}
}

func (b *schemaBuilder) edge(v value, n *schemaNode, path *documentPath, depth int, keyword string) {
	if v.kind() == '"' {
		b.g.edges = append(b.g.edges, &schemaEdge{v: v, owner: n,
			path: &documentPath{parent: path, part: v.t.name(v.i), token: true}, depth: depth + 1, keyword: keyword})
	}
}

func (b *schemaBuilder) discriminator(v value, n *schemaNode, path *documentPath, depth int) {
	path = &documentPath{parent: path, part: "discriminator", token: true}
	for name, m := range v.members() {
		switch name {
		case "defaultMapping":
			if v.t.edition == 32 {
				b.edge(m, n, path, depth+1, name)
			}
		case "mapping":
			p := &documentPath{parent: path, part: name, token: true}
			for _, item := range m.members() {
				b.edge(item, n, p, depth+2, name)
			}
		}
	}
}

func (s *Schema) schemaNode() *schemaNode {
	if s.legacy || s.synthetic != nil {
		return nil
	}
	return s.doc.schemaGraph(s.v.t).nodes[s.v.i]
}

// schemaURI uses the loader's identifier/alias/error machinery, then requires
// evidence that the physical node belongs to the schema object model.
func (d *document) schemaURI(uri string) (*Schema, error) {
	u, err := url.Parse(uri)
	if err != nil || !u.IsAbs() || strings.TrimSpace(uri) != uri {
		return nil, fmt.Errorf("%w: a schema URI must be absolute", ErrUnresolved)
	}
	resource, frag, _ := strings.Cut(uri, "#")
	u.Fragment, u.RawFragment = "", ""
	if err = checkURI(u, resource); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnresolved, err)
	}
	if d == nil {
		return nil, fmt.Errorf("%w: no document loaded", ErrUnresolved)
	}
	v, _, err := d.targetLocation(d.tree, uri, resource, frag)
	if err != nil {
		return nil, err
	}
	n := d.schemaGraph(v.t).nodes[v.i]
	if n == nil {
		return nil, fmt.Errorf("%w %q: not a Schema Object", ErrUnresolved, safeURI(uri))
	}
	s, _ := n.schema(d)
	return s, nil
}

// schema retains a failed legacy chain at its authored site. Its error is
// returned separately so an edge can report that it has no static target.
func (n *schemaNode) schema(d *document) (*Schema, error) {
	n.once.Do(func() {
		src := n.v.t.source(n.path.pointer())
		n.handle = &Schema{doc: d, v: n.v, src: src}
		if n.v.t.edition <= 30 {
			v, at, _, err := d.follow(n.v, src)
			if err == nil && d.schemaGraph(v.t).nodes[v.i] == nil {
				err = fmt.Errorf("%w: reference target is not a Schema Object", ErrUnresolved)
			}
			if n.err = err; err == nil {
				n.handle = &Schema{doc: d, v: v, src: at}
			}
		}
	})
	return n.handle, n.err
}

func (s *Schema) references() ([]SchemaReference, error) {
	if s.legacy || s.synthetic != nil {
		// Swagger Parameter/Items Objects have no reference-bearing fields.
		return nil, nil
	}
	g := s.doc.schemaGraph(s.v.t)
	n := g.nodes[s.v.i]
	if n == nil {
		return nil, fmt.Errorf("%w: not a Schema Object", ErrUnresolved)
	}
	first, _ := slices.BinarySearchFunc(g.edges, s.v.i, func(e *schemaEdge, i int32) int {
		if e.v.i < i {
			return -1
		}
		if e.v.i > i {
			return 1
		}
		return 0
	})
	var refs []SchemaReference
	for _, e := range g.edges[first:] {
		if uint32(e.v.i) >= s.v.t.nodes[s.v.i].next {
			break
		}
		if e.keyword == "" {
			return nil, fmt.Errorf("openapi: unsupported schema dialect %q", e.owner.dialect)
		}
		e.once.Do(func() { e.resolve(s.doc) })
		r := e.ref
		// Cut by token depth, including for separately reached fragments whose
		// shared physical ancestry may have different path allocations.
		p, count := e.path, e.depth-n.depth
		parts := make([]*documentPath, count)
		for i := count - 1; i >= 0; i-- {
			parts[i], p = p, p.parent
		}
		var at strings.Builder
		for _, part := range parts {
			at.WriteByte('/')
			at.WriteString(escapeToken(part.part))
		}
		r.At = at.String()
		refs = append(refs, r)
	}
	return refs, nil
}

func (e *schemaEdge) resolve(d *document) {
	r := &e.ref
	r.Keyword, r.Value = e.keyword, e.v.text()
	if (e.keyword == "mapping" || e.keyword == "defaultMapping") && componentName.MatchString(r.Value) {
		r.URI = d.tree.source("/components/schemas/" + escapeToken(r.Value))
	} else {
		resource, frag, hash := strings.Cut(r.Value, "#")
		res := d.resolve(e.v.t, e.owner.base, resource)
		if res.err != nil {
			r.Err = fmt.Errorf("%w %q: %w", ErrUnresolved, safeURI(r.Value), res.err)
			return
		}
		r.URI = res.uri
		if hash {
			r.URI += "#" + frag
		}
	}
	r.Target, r.Err = d.schemaURI(r.URI)
	if r.Err == nil && r.Target.v.t.edition <= 30 {
		// A public lookup preserves an unresolved legacy use site; an edge
		// needs the terminal target and must expose the chain's retained error.
		n := d.schemaGraph(r.Target.v.t).nodes[r.Target.v.i]
		_, r.Err = n.schema(d)
		if r.Err != nil {
			r.Target = nil
		}
	}
}
