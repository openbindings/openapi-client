package schema2020

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math"
	"strconv"
	"strings"

	"github.com/openbindings/openapi-client/go/openapi"
)

var (
	errDynamic    = errors.New("$dynamicRef target depends on dynamic scope")
	errUndefined  = errors.New("keyword not defined by the edition's Schema Object")
	errUnreported = errors.New("$ref not reported by openapi.Schema.References")
)

func dialectError(dialect string) error {
	return fmt.Errorf("unsupported schema dialect %q", dialect)
}

// slots are the JSON Schema 2020-12 keywords that hold subschemas: one
// ('1'), an array of them ('[') or an object of them ('{'). The client
// follows the same ones (openapi.Schema.References), so every reference it
// reports is met where it can be rewritten. $defs is not here: its entries
// are emitted only as the Defs entries something refers to.
var slots = map[string]byte{
	"items": '1', "not": '1', "if": '1', "then": '1', "else": '1', "contains": '1',
	"additionalProperties": '1', "propertyNames": '1', "unevaluatedItems": '1',
	"unevaluatedProperties": '1', "contentSchema": '1',
	"allOf": '[', "anyOf": '[', "oneOf": '[', "prefixItems": '[',
	"properties": '{', "patternProperties": '{', "dependentSchemas": '{',
}

// undefined maps each JSON Schema 2020-12 applicator and assertion that
// the OpenAPI 3.0 or the Swagger 2.0 Schema Object lacks to the last
// edition lacking it. contentSchema is one too: kept as an annotation, it
// would hold a schema the edition never reads. What remains of slots in
// those editions is what they define.
var undefined = map[string]int{
	"anyOf": 20, "oneOf": 20, "not": 20,
	"$dynamicRef": 30, "if": 30, "then": 30, "else": 30, "dependentSchemas": 30, "prefixItems": 30,
	"contains": 30, "patternProperties": 30, "propertyNames": 30, "unevaluatedItems": 30,
	"unevaluatedProperties": 30, "const": 30, "maxContains": 30, "minContains": 30,
	"dependentRequired": 30, "contentSchema": 30,
}

// partner pairs each bound with its Swagger 2.0 and OpenAPI 3.0 boolean
// modifier.
var partner = map[string]string{
	"minimum": "exclusiveMinimum", "exclusiveMinimum": "minimum",
	"maximum": "exclusiveMaximum", "exclusiveMaximum": "maximum",
}

// emit writes the schema s to s.out.
func (p *projector) emit(s *schema) {
	p.cur, p.ptr = s, p.ptr[:0]
	p.out.Reset()
	if s.foreign != "" {
		at := ""
		if s.root.get("$schema").ok() {
			at = "/$schema"
		}
		p.issues = append(p.issues, Issue{Source: s.src, At: at, Err: dialectError(s.foreign)})
		p.out.WriteString("true")
	} else {
		p.subschema(s.root)
	}
	s.out = bytes.Clone(p.out.Bytes())
}

// subschema writes the schema n, at p.ptr in p.cur.
func (p *projector) subschema(n node) {
	switch {
	case !n.is('{'):
		p.data(n)
	case p.cur.edition < 31:
		p.legacy(n)
	default:
		p.modern(n)
	}
}

// modern writes an OpenAPI 3.1 or 3.2 object schema as authored, less its
// identifiers, with its references rewritten or lost.
func (p *projector) modern(n node) {
	covered := false
	if p.cur.refsErr != nil && !p.covered {
		if d := n.get("$schema"); d.is('"') && !supported(d.text()) {
			// A nested resource in another dialect, which made References
			// fail for the whole tree.
			p.issue("$schema", dialectError(d.text()))
			p.out.WriteString("true")
			return
		}
		if len(p.ptr) > 0 && p.cover() {
			p.covered, covered = true, true
		}
	}
	o := p.open()
	for _, m := range n.members() {
		switch m.name() {
		case "$id", "$schema", "$anchor", "$dynamicAnchor", "$defs":
		case "$dynamicRef":
			p.issue("$dynamicRef", errDynamic)
		case "$ref":
			p.reference(&o, m)
		case "discriminator":
			p.discriminator(&o, m)
		default:
			p.member(&o, m, nil)
		}
	}
	p.close(o)
	if covered {
		p.covered = false
	}
}

// cover reads the references of the subtree at p.ptr through its own
// handle when References fails for p.cur's whole tree, which holds a
// resource in another dialect elsewhere. It reports whether the subtree's
// References succeeded, its references then joining p.cur's.
func (p *projector) cover() bool {
	s := p.cur
	h, err := p.c.Schema(s.src + fragment(p.ptr))
	if err != nil {
		return false
	}
	refs, err := h.References()
	if err != nil {
		return false
	}
	if s.refs == nil {
		s.refs = map[string]*openapi.SchemaReference{}
	}
	for i := range refs {
		s.refs[string(p.ptr)+refs[i].At] = &refs[i]
	}
	return true
}

// legacy writes a Swagger 2.0 or OpenAPI 3.0 object schema translated to
// JSON Schema 2020-12.
func (p *projector) legacy(n node) {
	o := p.open()
	s := p.cur
	if ref := n.get("$ref"); ref.is('"') && (s.refsErr != nil || s.refs[p.at("$ref")] != nil) {
		// These editions ignore every member beside a reported $ref.
		p.reference(&o, ref)
		p.close(o)
		return
	}
	typ, format := n.get("type").text(), n.get("format").text()
	binary := typ == "string" && format == "binary" || s.edition == 20 && typ == "file"
	nullable := s.edition == 30 && n.get("nullable").isTrue()
	var flags map[string]bool
	if kw := p.flagKeyword(); kw != "" && (n.get("required").ok() || s.edition == 20 && n.get("properties").ok()) {
		flags = p.flagged(s, n, p.ptr, kw)
	}
	for _, m := range n.members() {
		switch m.name() {
		case "$id", "$schema", "$anchor", "$dynamicAnchor", "$defs":
		case "$ref":
			p.reference(&o, m)
		case "type":
			if !binary {
				p.typ(&o, m, nullable)
			}
		case "format":
			switch {
			case format == "byte":
				p.key(&o, `"contentEncoding"`)
				p.out.WriteString(`"base64"`)
			case !binary:
				p.write(&o, m)
			}
		case "contentEncoding":
			if format != "byte" {
				p.write(&o, m)
			}
		case "nullable":
			if s.edition != 30 {
				p.write(&o, m)
			}
		case "exclusiveMinimum", "exclusiveMaximum":
			// A boolean modifies minimum or maximum; JSON Schema 2020-12 has
			// the bound itself.
			bound := n.get(partner[m.name()])
			switch {
			case !m.is('t') && !m.is('f'):
				p.write(&o, m)
			case m.isTrue() && bound.ok():
				p.key(&o, m.key())
				p.data(bound)
			}
		case "minimum", "maximum":
			if !n.get(partner[m.name()]).isTrue() {
				p.write(&o, m)
			}
		case "required":
			p.required(&o, m, flags)
		case "discriminator":
			p.discriminator(&o, m)
		case "properties":
			if s.edition == 20 {
				p.member(&o, m, flags) // readOnly properties must not be sent
			} else {
				p.member(&o, m, nil)
			}
		default:
			// Neither edition defines items written as an array.
			if undefined[m.name()] >= s.edition || m.name() == "items" && m.is('[') {
				p.issue(m.name(), errUndefined)
			} else {
				p.member(&o, m, nil)
			}
		}
	}
	p.close(o)
}

// typ writes the type m, with "null" added when nullable says so and the
// type does not already allow null.
func (p *projector) typ(o *obj, m node, nullable bool) {
	if !nullable || m.text() == "null" || !m.is('"') && !m.is('[') {
		p.write(o, m)
		return
	}
	for _, e := range m.members() {
		if e.text() == "null" {
			p.write(o, m)
			return
		}
	}
	p.key(o, m.key())
	p.out.WriteByte('[')
	if m.is('"') {
		p.data(m)
		p.out.WriteByte(',')
	}
	for _, e := range m.members() {
		p.data(e)
		p.out.WriteByte(',')
	}
	p.out.WriteString(`"null"]`)
}

// member writes m, descending into the subschemas of a slot keyword, each
// property named in falseNames written as false instead.
func (p *projector) member(o *obj, m node, falseNames map[string]bool) {
	p.key(o, m.key())
	slot := slots[m.name()]
	if slot == 0 || slot != '1' && !m.is(slot) {
		p.data(m)
		return
	}
	mark := p.push(m.name())
	switch slot {
	case '1':
		p.subschema(m)
	case '[':
		p.out.WriteByte('[')
		for i, e := range m.members() {
			if i > 0 {
				p.out.WriteByte(',')
			}
			at := len(p.ptr)
			p.ptr = strconv.AppendInt(append(p.ptr, '/'), int64(i), 10)
			p.subschema(e)
			p.ptr = p.ptr[:at]
		}
		p.out.WriteByte(']')
	case '{':
		e := p.open()
		for _, f := range m.members() {
			p.key(&e, f.key())
			if falseNames[f.name()] {
				p.out.WriteString("false")
				continue
			}
			at := p.push(f.name())
			p.subschema(f)
			p.ptr = p.ptr[:at]
		}
		p.out.WriteByte('}')
	}
	p.ptr = p.ptr[:mark]
}

// reference writes m, whose value may be a reference: rewritten to its
// target's Defs entry, or dropped with an Issue when it is not resolved or
// is a $ref that References does not report. A mapping value that is no
// reference is written as authored.
func (p *projector) reference(o *obj, m node) {
	s := p.cur
	var r *openapi.SchemaReference
	if s.refs != nil {
		r = s.refs[p.at(m.name())]
	}
	switch {
	case r != nil && r.Err == nil:
		p.key(o, m.key())
		p.writeRef(p.target(r.Target))
	case r != nil:
		p.issue(m.name(), r.Err)
	case s.refsErr != nil && !p.covered && m.is('"'):
		p.issue(m.name(), s.refsErr)
	case m.name() == "$ref":
		p.issue("$ref", errUnreported)
	default:
		p.write(o, m)
	}
}

// discriminator writes an OpenAPI 3.x discriminator with its mapping
// values and defaultMapping rewritten as references; Swagger 2.0's is a
// property name.
func (p *projector) discriminator(o *obj, m node) {
	if p.cur.edition == 20 || !m.is('{') {
		p.write(o, m)
		return
	}
	p.key(o, m.key())
	mark := p.push("discriminator")
	d := p.open()
	for _, f := range m.members() {
		switch {
		case f.name() == "mapping" && f.is('{'):
			p.key(&d, f.key())
			at := p.push("mapping")
			e := p.open()
			for _, g := range f.members() {
				p.reference(&e, g)
			}
			p.out.WriteByte('}')
			p.ptr = p.ptr[:at]
		case f.name() == "defaultMapping" && p.cur.edition == 32:
			p.reference(&d, f)
		default:
			p.write(&d, f)
		}
	}
	p.out.WriteByte('}')
	p.ptr = p.ptr[:mark]
}

// required writes a required list without the names flags marks, and
// nothing when that leaves it empty.
func (p *projector) required(o *obj, m node, flags map[string]bool) {
	if len(flags) == 0 || !m.is('[') {
		p.write(o, m)
		return
	}
	start, n, kept := p.out.Len(), o.n, 0
	p.key(o, m.key())
	p.out.WriteByte('[')
	for _, e := range m.members() {
		if e.is('"') && flags[e.text()] {
			continue
		}
		if kept > 0 {
			p.out.WriteByte(',')
		}
		kept++
		p.data(e)
	}
	if kept == 0 {
		p.out.Truncate(start)
		o.n = n
		return
	}
	p.out.WriteByte(']')
}

// flagKeyword is the annotation that marks the properties a required list
// does not bind in p.dir, under p.cur's edition, or "" when none does.
func (p *projector) flagKeyword() string {
	switch {
	case p.dir == Request && p.cur.edition < 31:
		return "readOnly"
	case p.dir == Response && p.cur.edition == 30:
		return "writeOnly"
	}
	return ""
}

// flagged returns the names of the properties of the object n, at ptr in
// s, that a declaration marks with kw: true at the root of its schema,
// after following $ref. The declarations are in the properties of n and of
// every schema n reaches through allOf, transitively. kw is the same for
// every call within one call to Project, so each object's names are found
// once: a depth-first walk over allOf (Tarjan's algorithm) unions each
// member's names into its own, and gives the objects of an allOf cycle,
// which form one strongly connected component, the names of the whole
// component.
func (p *projector) flagged(s *schema, n node, ptr []byte, kw string) map[string]bool {
	if p.flags == nil {
		p.flags, p.walking = map[node]map[string]bool{}, map[node]int{}
	}
	names, _ := p.walk(s, n, ptr, kw)
	return names
}

// walk returns n's names as flagged does, and the earliest place in the
// walk of an object that an allOf cycle from n reaches. The names of an
// object in an unfinished cycle are those found so far.
func (p *projector) walk(s *schema, n node, ptr []byte, kw string) (names map[string]bool, low int) {
	if names, ok := p.flags[n]; ok {
		return names, math.MaxInt
	}
	if low, ok := p.walking[n]; ok {
		return nil, low
	}
	place := len(p.walking)
	p.walking[n], p.component = place, append(p.component, n)
	d := decl{names: p.own(s, n, ptr, kw)}
	low = place
	visit := func(s *schema, n node, ptr []byte) {
		names, l := p.walk(s, n, ptr, kw)
		d.union(names)
		low = min(low, l)
	}
	for i, e := range n.get("allOf").members() {
		at := "/allOf/" + strconv.Itoa(i)
		if t, reported := p.referent(s, ptr, at, e); reported {
			if t != nil {
				visit(t, t.root, nil)
			}
			if s.edition < 31 {
				continue // these editions ignore the members beside it
			}
		}
		visit(s, e, append(ptr[:len(ptr):len(ptr)], at...))
	}
	if low == place {
		// n roots its component: every object walked since has its names.
		for {
			m := p.component[len(p.component)-1]
			p.component = p.component[:len(p.component)-1]
			p.flags[m] = d.names
			delete(p.walking, m)
			if m == n {
				break
			}
		}
	}
	return d.names, low
}

// A decl is the names of an object being walked.
type decl struct {
	names  map[string]bool
	shared bool // names belongs to another object too
}

// union adds names to d's. It keeps the larger set, copied if it is shared,
// and adds the smaller to it.
func (d *decl) union(names map[string]bool) {
	if len(names) > len(d.names) {
		d.names, names, d.shared = names, d.names, true
	}
	if len(names) == 0 {
		return
	}
	if d.shared {
		d.names, d.shared = maps.Clone(d.names), false
	}
	maps.Copy(d.names, names)
}

// own returns the names of the properties of n, at ptr in s, whose schema
// kw marks at its root, after following $ref, under s's edition: in
// OpenAPI 3.1 and 3.2 the members beside a $ref apply too.
func (p *projector) own(s *schema, n node, ptr []byte, kw string) map[string]bool {
	props := n.get("properties")
	if !props.is('{') {
		return nil
	}
	var names map[string]bool
	for _, m := range props.members() {
		t, reported := p.referent(s, ptr, "/properties/"+escape(m.name()), m)
		if t != nil && t.root.get(kw).isTrue() || (!reported || s.edition >= 31) && m.get(kw).isTrue() {
			if names == nil {
				names = map[string]bool{}
			}
			names[m.name()] = true
		}
	}
	return names
}

// referent reports whether References reports a $ref of the object n, at
// ptr+at in s, and returns the schema it leads to, or nil when it is not
// resolved.
func (p *projector) referent(s *schema, ptr []byte, at string, n node) (t *schema, reported bool) {
	if !n.get("$ref").ok() {
		return nil, false
	}
	r := s.refs[string(ptr)+at+"/$ref"]
	if r == nil {
		return nil, false
	}
	if r.Err == nil {
		t = p.load(r.Target)
	}
	return t, true
}

// ---- output ----

// An obj is a JSON object being written: where it starts, the Issues
// before it, and how many members it has.
type obj struct{ start, issues, n int }

func (p *projector) open() obj {
	o := obj{start: p.out.Len(), issues: len(p.issues)}
	p.out.WriteByte('{')
	return o
}

func (p *projector) key(o *obj, key string) {
	if o.n > 0 {
		p.out.WriteByte(',')
	}
	o.n++
	p.out.WriteString(key)
	p.out.WriteByte(':')
}

// close ends a schema object. One that Issues leave with no members is
// written true, the schema that accepts anything.
func (p *projector) close(o obj) {
	if o.n == 0 && len(p.issues) > o.issues {
		p.out.Truncate(o.start)
		p.out.WriteString("true")
		return
	}
	p.out.WriteByte('}')
}

// write writes m as authored.
func (p *projector) write(o *obj, m node) {
	p.key(o, m.key())
	p.data(m)
}

// data writes n as authored, without insignificant whitespace.
func (p *projector) data(n node) {
	raw := n.raw()
	if !n.is('{') && !n.is('[') {
		p.out.WriteString(raw)
		return
	}
	p.out.WriteByte(raw[0])
	for i, m := range n.members() {
		if i > 0 {
			p.out.WriteByte(',')
		}
		if k := m.key(); k != "" {
			p.out.WriteString(k)
			p.out.WriteByte(':')
		}
		p.data(m)
	}
	p.out.WriteByte(raw[len(raw)-1])
}

// writeRef writes the reference to key as a JSON string: key escaped as a
// JSON Pointer token, then percent-encoded where RFC 3986 section 3.5 does
// not allow a character in a fragment.
func (p *projector) writeRef(key string) {
	p.out.WriteByte('"')
	p.out.Write(appendFragment([]byte("#/$defs/"), escape(key)))
	p.out.WriteByte('"')
}

// fragment is the JSON Pointer ptr as the tail of a URI fragment (RFC
// 6901 section 6).
func fragment(ptr []byte) string { return string(appendFragment(nil, string(ptr))) }

// appendFragment appends s, percent-encoding each byte RFC 3986 section 3.5
// does not allow in a fragment.
func appendFragment(b []byte, s string) []byte {
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', strings.IndexByte("-._~!$&'()*+,;=:@/?", c) >= 0:
			b = append(b, c)
		default:
			b = append(b, '%', hex[c>>4], hex[c&15])
		}
	}
	return b
}

// ---- JSON Pointers ----

func (p *projector) push(token string) int {
	n := len(p.ptr)
	p.ptr = append(append(p.ptr, '/'), escape(token)...)
	return n
}

// at is the JSON Pointer to the member token of the node at p.ptr.
func (p *projector) at(token string) string {
	return string(p.ptr) + "/" + escape(token)
}

func (p *projector) issue(token string, err error) {
	p.issues = append(p.issues, Issue{Source: p.cur.src, At: p.at(token), Err: err})
}

// escape escapes a JSON Pointer token (RFC 6901 section 3).
func escape(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// ---- JSON values ----

// A tree is the Raw of one schema, parsed: its text, and one val per JSON
// value in document order, each container followed by its members, so that
// member order, number spellings and string escapes stay as authored.
type tree struct {
	s    string
	vals []val
}

// A val holds offsets into the text: the quoted name of an object member
// (empty for an array element), the value, and the index of the val after
// its last descendant.
type val struct {
	key, keyEnd, start, end, next int32
	escaped                       bool // the name is written with an escape
}

// A node is one value of a tree. The zero node is absent.
type node struct {
	t *tree
	i int32
}

func (n node) ok() bool    { return n.t != nil }
func (n node) v() *val     { return &n.t.vals[n.i] }
func (n node) raw() string { return n.t.s[n.v().start:n.v().end] }

// key is n's name as written, quoted, or "" for an array element.
func (n node) key() string { return n.t.s[n.v().key:n.v().keyEnd] }

// name is n's name, unquoted.
func (n node) name() string {
	k := n.key()
	if n.v().escaped {
		return unquote(k)
	}
	return k[1 : len(k)-1]
}

func (n node) is(c byte) bool { return n.ok() && n.t.s[n.v().start] == c }

func (n node) isTrue() bool { return n.ok() && n.raw() == "true" }

// members yields an object's members or an array's elements, in order.
func (n node) members() iter.Seq2[int, node] {
	return func(yield func(int, node) bool) {
		if !n.ok() {
			return
		}
		for i, j := 0, n.i+1; j < n.v().next; i, j = i+1, n.t.vals[j].next {
			if !yield(i, node{n.t, j}) {
				return
			}
		}
	}
}

// get returns the member of the object n with the given name, or the zero node.
func (n node) get(name string) node {
	if n.is('{') {
		for _, m := range n.members() {
			if m.name() == name {
				return m
			}
		}
	}
	return node{}
}

// text is the string n holds, or "".
func (n node) text() string {
	if !n.is('"') {
		return ""
	}
	return unquote(n.raw())
}

func unquote(q string) string {
	if strings.IndexByte(q, '\\') < 0 {
		return q[1 : len(q)-1]
	}
	var s string
	_ = json.Unmarshal([]byte(q), &s)
	return s
}

// parse reads raw, which openapi.Schema.Raw guarantees to be JSON.
func parse(raw []byte) node {
	// Every value but the root and the first of each container follows a
	// comma, so this many fit.
	r := &reader{tree: tree{s: string(raw)}}
	r.vals = make([]val, 0, 1+bytes.Count(raw, []byte(","))+bytes.Count(raw, []byte("{"))+bytes.Count(raw, []byte("[")))
	r.value(val{})
	return node{&r.tree, 0}
}

type reader struct {
	tree
	i int
}

// value reads the value at r.i, whose name, if any, m holds.
func (r *reader) value(m val) {
	for r.i < len(r.s) && isSpace(r.s[r.i]) {
		r.i++
	}
	v := len(r.vals)
	m.start = int32(r.i)
	r.vals = append(r.vals, m)
	switch c := r.s[r.i]; c {
	case '{', '[':
		end := c + 2 // '}' or ']'
		r.i++
		for {
			for isSpace(r.s[r.i]) || r.s[r.i] == ',' {
				r.i++
			}
			if r.s[r.i] == end {
				break
			}
			var m val
			if c == '{' {
				m.key = int32(r.i)
				m.escaped = r.str()
				m.keyEnd = int32(r.i)
				r.i += strings.IndexByte(r.s[r.i:], ':') + 1
			}
			r.value(m)
		}
		r.i++
	case '"':
		r.str()
	default:
		for r.i < len(r.s) && !isSpace(r.s[r.i]) && strings.IndexByte(",]}", r.s[r.i]) < 0 {
			r.i++
		}
	}
	r.vals[v].end, r.vals[v].next = int32(r.i), int32(len(r.vals))
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// str skips a string and reports whether it has an escape.
func (r *reader) str() (escaped bool) {
	for r.i++; r.s[r.i] != '"'; r.i++ {
		if r.s[r.i] == '\\' {
			r.i++
			escaped = true
		}
	}
	r.i++
	return escaped
}
