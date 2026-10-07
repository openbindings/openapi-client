package openapi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// schemaGraph is built only by schema inspection. It records schema positions
// and reference events, not complete pointer strings or copies of subtrees.
// Events are grouped by schema-vocabulary ancestry, then by document order.
type schemaGraph struct {
	nodes map[int32]*schemaNode
}

type schemaNode struct {
	v       value
	path    *documentPath
	depth   int
	base    *url.URL
	dialect string
	parent  *schemaNode   // only a schema-bearing keyword establishes this link
	group   *schemaNode   // root of that semantic component, settled after indexing
	edges   []*schemaEdge // stored only on the component root
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
	if t.kind() == rootKind {
		if d := t.root().str("jsonSchemaDialect"); d != "" {
			return d
		}
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
		base = v.resourceBase(base, id)
	}
	return base, dialect
}

func (d *document) schemaGraph(t *tree) *schemaGraph {
	t.schemasOnce.Do(func() {
		// Loading a pure-local tree only marks the contexts that reach it.
		// Complete those with discovery's own object model before any generic
		// graph scan can interpret the same nodes. No retrieval is possible:
		// this tree has neither nonlocal references nor identifier declarations.
		if !t.reaches && !t.declares && t.kind() == anyKind {
			t.scopeReady = true
			// This runs after Load, for methods that take no context, and
			// reads a tree already in memory once, in time linear in its
			// size, retrieving nothing: no context can cancel it.
			r := reader{d: d, ctx: context.Background(), t: t, complete: true}
			if t.dialects {
				for frag, kinds := range t.schemaIntents {
					for k := kind(1); k <= itemsKind; k++ {
						if kinds&(1<<k) != 0 {
							r.admit(t.root(), t.uri, want{frag, k, t, 0})
						}
					}
				}
			}
			open := func(frag string, kinds uint16) {
				for k := kind(1); k <= itemsKind; k++ {
					if kinds&(1<<k) != 0 {
						r.open(t.root(), anyKind, t.refbase, nil, t.uri, "", false, want{frag, k, t, 0})
					}
				}
			}
			open("", t.schemaRoots)
			for frag, kinds := range t.schemaIntents {
				open(frag, kinds)
			}
			r.drain()
		}
		g := &schemaGraph{nodes: map[int32]*schemaNode{}}
		b := schemaBuilder{g: g, seen: make([]uint32, len(t.nodes)), seeds: map[int32][]item{}}
		seeds := t.schemaSeeds
		if !t.declares && (t.schemaRoots != 0 || t.schemaContexts != nil) {
			seeds = unscopedSeeds(t)
		}
		for _, it := range seeds {
			if it.v.scopeError() != nil {
				continue
			}
			b.seeds[it.v.i] = append(b.seeds[it.v.i], it)
		}
		b.visit(t.root(), t.kind(), t.refbase, schemaDialect(t), nil, 0, nil)
		// A referenced fragment can lie outside the root's known vocabulary.
		// Its authored reference context takes precedence over generic scanning.
		for _, it := range seeds {
			if b.seen[it.v.i]&(1<<it.k) != 0 {
				continue
			}
			p, depth := schemaPath(it.ptr)
			dialect := it.dialect
			if dialect == "" {
				dialect = schemaDialect(t)
			}
			b.visit(it.v, it.k, it.base, dialect, p, depth, nil)
		}
		slices.SortFunc(b.edges, func(a, b *schemaEdge) int {
			if a.v.i < b.v.i {
				return -1
			}
			if a.v.i > b.v.i {
				return 1
			}
			return 0
		})
		for _, n := range g.nodes {
			n.component()
		}
		for _, e := range b.edges {
			root := e.owner.group
			root.edges = append(root.edges, e)
		}
		t.schemas = g
		t.schemaIntents = nil
	})
	return t.schemas
}

// unscopedSeeds derives physical paths only when the graph is requested.
// Without identifiers, discovery retains queued context bits. Walk toward
// the next seeded node in document order, sharing ancestors and resource
// dialect scope while skipping unrelated subtrees.
func unscopedSeeds(t *tree) []item {
	var seeds []item
	add := func(v value, kinds uint16, path *documentPath, scope documentScope) {
		for k := kind(1); k <= itemsKind; k++ {
			if kinds&(1<<k) != 0 {
				seeds = append(seeds, item{v, k, scope.base, path, scope.dialect})
			}
		}
	}
	scope := documentScope{kinds: 1 << t.kind(), base: t.refbase}
	add(t.root(), t.schemaRoots, nil, scope)
	next := int32(0)
	advance := func() {
		for next++; int(next) < len(t.schemaContexts); next++ {
			if t.schemaContexts[next]&0x55555554 != 0 { // queued bits, excluding anyKind
				return
			}
		}
	}
	advance()
	var walk func(value, *documentPath, documentScope)
	walk = func(v value, path *documentPath, scope documentScope) {
		scope = scope.enter(v)
		i := 0
		for name, m := range v.members() {
			if int(next) >= len(t.schemaContexts) || uint32(next) >= t.nodes[v.i].next {
				return
			}
			if v.kind() == '[' {
				name = strconv.Itoa(i)
				i++
			}
			if uint32(next) >= t.nodes[m.i].next {
				continue
			}
			p := &documentPath{parent: path, part: name, token: true}
			child := scope.child(name, t.edition)
			if next == m.i {
				var kinds uint16
				for k := kind(1); k <= itemsKind; k++ {
					if t.schemaContexts[next]&(1<<(2*k)) != 0 {
						kinds |= 1 << k
					}
				}
				add(m, kinds, p, child)
				advance()
			}
			if int(next) < len(t.schemaContexts) && uint32(next) < t.nodes[m.i].next {
				walk(m, p, child)
			}
		}
	}
	walk(t.root(), nil, scope)
	return seeds
}

// schemaResource recognizes a schema location in an otherwise unclassified
// document. Once recognized, only the schema vocabulary supplies descendants.
func schemaResource(v value) bool {
	return v.i == 0 && v.t.dialects && v.get("$schema").ok() ||
		v.t.declares && (v.get("$id").ok() || v.get("$anchor").ok() || v.get("$dynamicAnchor").ok())
}

// component is settled after every context has been visited, so independently
// seeded children attach to their vocabulary parent regardless of visit order.
func (n *schemaNode) component() *schemaNode {
	if n.group == nil {
		n.group = n
		if n.parent != nil {
			n.group = n.parent.component()
		}
	}
	return n.group
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
	edges []*schemaEdge
}

func (b *schemaBuilder) visit(v value, k kind, base *url.URL, dialect string, path *documentPath, depth int, parent *schemaNode) {
	if v.scopeError() != nil {
		if parent != nil {
			b.edges = append(b.edges, &schemaEdge{v: v, owner: parent, path: path, depth: depth})
		}
		return
	}
	if k == anyKind {
		if seeds := b.seeds[v.i]; len(seeds) > 0 {
			for _, it := range seeds {
				b.visit(v, it.k, base, dialect, path, depth, nil)
			}
			return
		}
		if schemaResource(v) {
			k = schemaKind
		}
	}
	if k == schemaKind && parent != nil {
		if n := b.g.nodes[v.i]; n != nil {
			n.parent = parent
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
		n = &schemaNode{v: v, path: path, depth: depth, base: base, dialect: dialect, parent: parent}
		b.g.nodes[v.i] = n
		if dialect != "" && !ownDialect(dialect) {
			b.edges = append(b.edges, &schemaEdge{v: v, owner: n, path: path, depth: depth})
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
		if referenceObject(k, v.t.edition) {
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
			b.into(name, m, s, base, dialect, path, depth, n)
		}
	}
}

func (b *schemaBuilder) into(name string, v value, s slot, base *url.URL, dialect string, path *documentPath, depth int, parent *schemaNode) {
	path = &documentPath{parent: path, part: name, token: true}
	depth++
	if s.how == '1' {
		b.visit(v, s.k, base, dialect, path, depth, parent)
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
		b.into(key, c, slot{s.k, '1'}, base, dialect, path, depth, parent)
	}
}

func (b *schemaBuilder) edge(v value, n *schemaNode, path *documentPath, depth int, keyword string) {
	if v.kind() == '"' {
		b.edges = append(b.edges, &schemaEdge{v: v, owner: n,
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
			if m.kind() != '{' {
				continue // an array is not a mapping
			}
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
	location, err := d.targetNode(d.tree, uri, resource, frag)
	if err != nil {
		return nil, err
	}
	n := d.schemaGraph(location.v.t).nodes[location.v.i]
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
	if err := s.v.scopeError(); err != nil {
		return nil, err
	}
	if s.legacy || s.synthetic != nil {
		// Swagger Parameter/Items Objects have no reference-bearing fields.
		return nil, nil
	}
	g := s.doc.schemaGraph(s.v.t)
	n := g.nodes[s.v.i]
	if n == nil {
		return nil, fmt.Errorf("%w: not a Schema Object", ErrUnresolved)
	}
	edges := n.group.edges
	first, _ := slices.BinarySearchFunc(edges, s.v.i, func(e *schemaEdge, i int32) int {
		if e.v.i < i {
			return -1
		}
		if e.v.i > i {
			return 1
		}
		return 0
	})
	var refs []SchemaReference
	for _, e := range edges[first:] {
		if uint32(e.v.i) >= s.v.t.nodes[s.v.i].next {
			break
		}
		if e.keyword == "" {
			if err := e.v.scopeError(); err != nil {
				return nil, err
			}
			return nil, errors.New("openapi: unsupported schema dialect")
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
	if r.Err = e.v.scopeError(); r.Err != nil {
		return
	}
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
