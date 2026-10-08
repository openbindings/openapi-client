package openapi

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// A formEncoding is the Encoding of a form or multipart Media, or the fields
// a schema gives a nested part, compiled once: its fields by name, and, for
// each struct type a body of it has been, that type's fields in the order
// encoding/json writes them.
type formEncoding struct {
	positional []*field
	rest       *field
	ordered    bool
	required   bool
	swagger    bool
	fallback   *field
	byName     map[string]*field
	lists      sync.Map // reflect.Type to []*field
}

// noFields is the encoding of a nested part no schema describes.
var noFields = &formEncoding{}

// A field is a form or multipart field compiled: its descriptor; the RFC
// 6570 style its Encoding sets, which applies under form-urlencoded and
// multipart/form-data; and the media types it otherwise takes.
type field struct {
	encoding value
	param
	whole  bool
	styled bool                            // its Encoding sets style, explode or allowReserved, where they apply
	types  []string                        // the media types it takes, as written
	parsed []parsedMedia                   // types, parsed
	class  class                           // the class of a sole type
	listed bool                            // its Encoding lists them, so a Part's MediaType must match one
	err    error                           // why its contentType cannot type a value
	plain  bool                            // a value takes its sole concrete type, not the form type, with no style or Err
	roots  []value                         // its declarations, whose properties a nested part's fields are
	nested [2]atomic.Pointer[formEncoding] // the fields of its nested part, and of an item's
}

// untyped is a field no schema or Encoding describes, whose type is absent
// (OpenAPI 3.1.2 section 4.8.15.1.1).
var untyped = &field{param: param{Param: &Param{ContentType: octetStream.full}},
	types: []string{octetStream.full}, parsed: []parsedMedia{octetStream}, class: otherClass, plain: true}

func (e *formEncoding) field(name string) *field {
	if f := e.byName[name]; f != nil {
		return f
	}
	if e.fallback != nil {
		return e.fallback
	}
	return untyped
}

// fieldsOf returns the fields of e that the struct type t's fields fs, as
// encoding/json writes them, are, found once per type.
func (e *formEncoding) fieldsOf(t reflect.Type, fs []jsonField) []*field {
	if l, ok := e.lists.Load(t); ok {
		return l.([]*field)
	}
	l := make([]*field, len(fs))
	for i, jf := range fs {
		l[i] = e.field(jf.name)
	}
	got, _ := e.lists.LoadOrStore(t, l)
	return got.([]*field)
}

// stylesApply reports whether an Encoding's style, explode and allowReserved
// apply under m (OpenAPI 3.1.2 section 4.8.15.1.2): form-urlencoded and
// multipart/form-data.
func stylesApply(m parsedMedia) bool {
	return isForm(m) || strings.EqualFold(m.full, "multipart/form-data")
}

// byStyle reports whether a field whose Encoding Object is e, in a body of
// media type m, is written by e's style, explode and allowReserved, by RFC
// 6570, in place of its content type: e sets one of them, and they apply
// under m (see stylesApply), as in OpenAPI 3.0 they do under form-urlencoded
// alone. A field so written takes no Part. newField records it as the
// field's styled, which the writers follow.
func byStyle(e value, m parsedMedia) bool {
	if !e.ok() || !stylesApply(m) || e.t.edition == 30 && isMultipart(m) {
		return false
	}
	return e.get("style").ok() || e.get("explode").ok() || e.get("allowReserved").ok()
}

// formData is multipart/form-data, parsed.
var formData = parsedMedia{"multipart/form-data", "multipart", "form-data", ""}

// encodingOf compiles the fields of a form or multipart body whose schema is
// s, at src, with the Encoding Objects of encodings, at esrc, under the media
// type or range m, and describes them: the properties s and the schemas its
// $ref and allOf reach declare, then the names only encodings has.
func (d *document) encodingOf(s []value, src string, encodings value, esrc string, m parsedMedia, edition int) (*formEncoding, []*Param) {
	if bundled(encodings) { // the part holding it reports it
		encodings = value{}
	}
	fields := map[string]*field{}
	var names []string
	var first []*Schema
	d.closure(s, src, func(s value, at string) {
		for name, ps := range s.get("properties").members() {
			if f := fields[name]; f != nil {
				f.roots = append(f.roots, ps)
				continue
			}
			fields[name] = &field{roots: []value{ps}}
			names, first = append(names, name), append(first, d.schema(ps, at, "/properties/"+token(name)))
		}
	})
	list := make([]*Param, 0, len(names))
	for i, name := range names {
		f, p := d.newField(name, first[i], fields[name].roots, encodings.get(name), esrc+"/"+token(name), m, false)
		fields[name], list = f, append(list, p)
	}
	for name, e := range encodings.members() {
		if fields[name] == nil {
			f, p := d.newField(name, nil, nil, e, esrc+"/"+token(name), m, false)
			fields[name], list = f, append(list, p)
		}
	}
	enc := &formEncoding{byName: fields}
	if edition <= 30 {
		enc.fallback = textField
	}
	return enc, list
}

// nested returns the fields of a nested part f writes from an object: in
// OpenAPI 3.1 those its schemas declare, each of its default type, or, for an
// item or a field whose schemas allow an array but no object, those its items
// declare; compiled once per schema, and found once per field.
func (d *document) nested(f *field, item bool, m parsedMedia) *formEncoding {
	if f.encoding.ok() && f.encoding.t.edition == 32 {
		mode := 0
		if isForm(m) {
			mode = 1
		} else if stylesApply(m) {
			mode = 2
		}
		k := struct {
			field *field
			item  bool
			mode  int
		}{f, item, mode}
		if got, ok := d.forms.Load(k); ok {
			return got.(*formEncoding)
		}
		roots := f.roots
		if l := d.typing(roots, false); !f.whole && l.array && (item || !l.object) {
			roots = l.items
			if l.item.ok() {
				roots = []value{l.item}
			}
		}
		enc, _ := d.encodingOf(roots, "", f.encoding.get("encoding"), f.Source+"/encoding", m, f.encoding.t.edition)
		d.positionalEncoding(enc, f.encoding, roots, f.Source, m)
		got, _ := d.forms.LoadOrStore(k, enc)
		return got.(*formEncoding)
	}
	p := &f.nested[0]
	if item {
		p = &f.nested[1]
	}
	if enc := p.Load(); enc != nil {
		return enc
	}
	roots, enc := f.roots, noFields
	if l := d.typing(roots, false); !f.whole && l.array && (item || !l.object) {
		if roots = l.items; l.item.ok() {
			roots = []value{l.item}
		}
	}
	if len(roots) > 0 {
		k := stateOf(roots)
		e, ok := d.forms.Load(k)
		if !ok {
			e, _ = d.encodingOf(roots, "", value{}, "", parsedMedia{}, roots[0].t.edition)
			e, _ = d.forms.LoadOrStore(k, e)
		}
		enc = e.(*formEncoding)
	}
	p.CompareAndSwap(nil, enc)
	return p.Load()
}

// A shape is what a schema and the schemas its $ref and allOf reach say of
// a value, computed once per schema: the JSON types all of them allow,
// whether one has a type or a contentEncoding, the items schema when just
// one of them declares items, and, so that their properties and items are
// found walking only the schemas that declare some, which of the schemas it
// names reach a declaration. A $ref is a keyword of its schema (JSON Schema
// 2020-12 section 8.2.3.1), its target another schema reached, whose own
// keywords and $ref apply too.
type shape struct {
	types          uint8 // jsonTypes' bits
	typed, encoded bool
	declares       bool  // it or a schema it reaches declares properties or items
	items          value // the schemas declaring items declare it, if one does
	several        bool  // and several do
	kids           []kid // the schemas it names that declare, in document order
}

// A kid is a schema another names: its $ref target, at ptr, or its allOf
// member n.
type kid struct {
	v   value
	ptr string
	n   int // -1 for the $ref target
}

// JSON Schema's types, as bits.
var jsonTypes = map[string]uint8{"string": 1, "number": 2, "integer": 4, "boolean": 8, "object": 16, "array": 32, "null": 64, "file": 1}

// meet makes s what s and t say together.
func (s *shape) meet(t *shape) {
	s.types &= t.types
	s.typed, s.encoded, s.declares = s.typed || t.typed, s.encoded || t.encoded, s.declares || t.declares
	if !s.items.ok() {
		s.items, s.several = t.items, t.several
	} else if t.items.ok() && (t.items != s.items || t.several) {
		s.several = true
	}
}

// own returns what the schema v says itself, and the schemas it names, its
// keywords read in one pass while it has few, and looked up otherwise.
func (d *document) own(v value) (shape, []kid) {
	var t, enc, props, ref, all value
	s, n := shape{types: 127}, 0
	for name, m := range v.members() {
		if n++; n > many {
			t, enc, props, s.items, ref, all = v.get("type"), v.get("contentEncoding"), v.get("properties"), v.get("items"), v.get("$ref"), v.get("allOf")
			break
		}
		switch name {
		case "type":
			t = m
		case "contentEncoding":
			enc = m
		case "properties":
			props = m
		case "items":
			s.items = m
		case "$ref":
			ref = m
		case "allOf":
			all = m
		}
	}
	if v.t.edition <= 30 && ref.ok() {
		t, enc, props, all, s.items = value{}, value{}, value{}, value{}, value{}
	}
	if v.t.edition <= 30 {
		enc = value{}
	}
	if v.t.edition <= 30 && !ref.ok() && (v.str("format") == "binary" || v.str("format") == "byte" || t.string() == "file") {
		enc = v
		if t.string() == "file" {
			s.encoded = true
		}
	}
	if t.ok() {
		s.typed, s.types = true, jsonTypes[t.string()]
		for _, n := range t.members() {
			s.types |= jsonTypes[n.string()]
		}
		if s.types&2 != 0 {
			s.types |= 4 // a number may be an integer (JSON Schema 2020-12 Validation section 6.1.1)
		}
	}
	s.encoded, s.declares = enc.ok(), props.ok() || s.items.ok() || v.t.edition == 32 && v.get("prefixItems").ok()
	var kids []kid
	if ref.kind() == '"' {
		if t, ptr, err := d.target(ref); err == nil {
			kids = append(kids, kid{t, ptr, -1})
		}
	}
	n = 0
	for _, e := range all.members() {
		kids, n = append(kids, kid{e, "", n}), n+1
	}
	if len(kids) > 1 && kids[0].n < 0 && all.i < ref.i { // allOf first in document order
		kids = append(kids[1:], kids[0])
	}
	return s, kids
}

// The facts of a node are what it compiles to, each once: a schema's shape,
// and the default media types of a field it alone types, and whether an
// Encoding or Media Type Object holds a value written as a reference.
type facts struct {
	shape   atomic.Pointer[shape]
	set     atomic.Uint32 // the mediaSet, with bit 8 set once known
	bundled atomic.Uint32 // what holdsBundled found, once known
}

// factsPage is how many nodes' facts are made at a time.
const factsPage = 128

// facts returns node i's facts, its page of them made on first use.
func (d *document) facts(i int32) *facts {
	p := &d.pages[i/factsPage]
	if p.Load() == nil {
		p.CompareAndSwap(nil, new([factsPage]facts))
	}
	return &p.Load()[i%factsPage]
}

// shapeOf returns the shape of the schema v, computing it, and that of each
// schema it reaches by $ref and allOf, once: at once when each of those it
// names is known or names none, as most do, and otherwise in one walk without
// recursion that finds the schemas of each cycle, which reach each other and
// so share what they say (Tarjan's strongly connected components).
func (d *document) shapeOf(v value) *shape {
	f := d.facts(v.id())
	if s := f.shape.Load(); s != nil {
		return s
	}
	s, kids := d.own(v)
	kept := kids[:0] // filtered in place
	for _, k := range kids {
		ks := d.facts(k.v.id()).shape.Load()
		if ks == nil {
			if s, kids := d.own(k.v); len(kids) == 0 {
				ks = d.keep(k.v, s)
			}
		}
		if ks == nil {
			return d.walk(v)
		}
		if s.meet(ks); ks.declares {
			kept = append(kept, k)
		}
	}
	s.kids = kept
	return d.keep(v, s)
}

// keep publishes s as the shape of v, unless one is, returning the one
// published.
func (d *document) keep(v value, s shape) *shape {
	f := d.facts(v.id())
	f.shape.CompareAndSwap(nil, &s)
	return f.shape.Load()
}

// walk computes the shape of v and of each schema it reaches without one.
func (d *document) walk(v value) *shape {
	s, kids := d.own(v)
	type node struct {
		v          value
		s          shape
		kids, kept []kid // all it names, and those that declare or are of its cycle
		next, low  int
	}
	var nodes []node     // in the order entered, which is each one's index
	var path, open []int // the walk's path, and the nodes whose cycle is not complete
	byNode := map[int32]int{}
	for next := v; ; {
		if next.ok() { // enter it
			byNode[next.id()] = len(nodes)
			nodes, path, open = append(nodes, node{v: next, s: s, kids: kids, kept: kids[:0], low: len(nodes)}), append(path, len(nodes)), append(open, len(nodes))
			next = value{}
		}
		top := path[len(path)-1]
		n := &nodes[top]
		if n.next < len(n.kids) {
			k := n.kids[n.next]
			n.next++
			m, ok := byNode[k.v.id()]
			switch ks := d.facts(k.v.id()).shape.Load(); {
			case ks != nil:
				n.s.meet(ks)
				if ks.declares {
					n.kept = append(n.kept, k)
				}
			case ok: // open, as one done is kept: of n's cycle
				n.low, n.kept = min(n.low, m), append(n.kept, k)
			default:
				if s, kids = d.own(k.v); len(kids) > 0 {
					next = k.v
				} else {
					ks := d.keep(k.v, s)
					if n.s.meet(ks); ks.declares {
						n.kept = append(n.kept, k)
					}
				}
			}
			continue
		}
		path = path[:len(path)-1]
		var p *node
		if len(path) > 0 {
			p = &nodes[path[len(path)-1]]
		}
		if n.low < top { // of a cycle with p
			p.low, p.kept = min(p.low, n.low), append(p.kept, p.kids[p.next-1])
			continue
		}
		i := slices.Index(open, top) // n and the nodes opened after it are a cycle, or n alone
		cs := n.s
		for _, m := range open[i:] {
			cs.meet(&nodes[m].s)
		}
		var done *shape
		for _, m := range open[i:] {
			ms := cs
			if ms.kids = nodes[m].kept; !cs.declares {
				ms.kids = nil
			}
			if ks := d.keep(nodes[m].v, ms); m == top {
				done = ks
			}
		}
		if open = open[:i]; p == nil {
			return done
		}
		if p.s.meet(done); done.declares {
			p.kept = append(p.kept, p.kids[p.next-1])
		}
	}
}

// closure calls f with each schema of s, at src, and each schema they reach
// by $ref and allOf that declares properties or items, depth first in
// document order, each once.
func (d *document) closure(s []value, src string, f func(s value, at string)) {
	type schema struct {
		v  value
		at string
	}
	var stack []schema
	for i := len(s) - 1; i >= 0; i-- {
		stack = append(stack, schema{s[i], src})
	}
	seen := map[int32]bool{}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !top.v.ok() || seen[top.v.id()] {
			continue
		}
		seen[top.v.id()] = true
		if top.v.t.edition >= 31 || top.v.get("$ref").kind() != '"' {
			f(top.v, top.at)
		}
		kids := d.shapeOf(top.v).kids
		for i := len(kids) - 1; i >= 0; i-- { // the first in document order on top
			k, at := kids[i], ""
			switch {
			case top.at == "":
			case k.n < 0:
				at = k.v.t.source(k.ptr)
			default:
				at = top.at + "/allOf/" + strconv.Itoa(k.n)
			}
			stack = append(stack, schema{k.v, at})
		}
	}
}

// A mediaSet is a set of the default media types a field may take.
type mediaSet uint8

const (
	textDefault mediaSet = 1 << iota
	jsonDefault
	octetDefault
)

var defaultMedia = [...]struct {
	set    mediaSet
	parsed parsedMedia
}{
	{jsonDefault, parsedMedia{"application/json", "application", "json", ""}},
	{octetDefault, octetStream},
	{textDefault, parsedMedia{"text/plain", "text", "plain", ""}},
}

// A typing is what the schemas of a value and those their $ref and allOf
// reach say of its type together: the defaults of the types they allow other
// than array (OpenAPI 3.1.2 section 4.8.15.1.1), whether they allow an
// object or an array, and the schema of an array's items, or the schemas
// when several declare items. A string with a contentEncoding is
// application/octet-stream; a value whose type no schema constrains, or
// nothing can be, and an array's items without a schema, take the untyped
// default.
type typing struct {
	own           mediaSet
	object, array bool
	item          value
	items         []value
}

// typing returns the typing of a value whose schemas are s.
func (d *document) typing(s []value, whole bool) typing {
	sh := shape{types: 127}
	for _, r := range s {
		if r.ok() {
			sh.meet(d.shapeOf(r))
		}
	}
	var l typing
	switch t := sh.types; {
	case !sh.typed || t&^64 == 0:
		return typing{own: untypedDefault(s)}
	case t&1 != 0 && sh.encoded:
		l.own = octetDefault
	case t&1 != 0:
		l.own = textDefault
	}
	if sh.types&(2|4|8) != 0 {
		l.own |= textDefault
	}
	if l.object = sh.types&16 != 0; l.object {
		l.own |= jsonDefault
	}
	switch l.array = sh.types&32 != 0; {
	case !l.array:
	case whole:
		l.own |= jsonDefault
	case !sh.items.ok():
		l.own |= untypedDefault(s) // items allowing anything have no type
	case !sh.several:
		l.item = sh.items
	default:
		d.closure(s, "", func(s value, _ string) {
			if it := s.get("items"); it.ok() {
				l.items = append(l.items, it)
			}
		})
	}
	return l
}

// untypedDefault is the default of a value of the schemas s whose type none
// constrains: text/plain in OpenAPI 3.0, and Swagger 2.0, whose tables give
// it none, and application/octet-stream in OpenAPI 3.1 and 3.2.
func untypedDefault(s []value) mediaSet {
	if len(s) > 0 && s[0].ok() && s[0].t.edition <= 30 {
		return textDefault
	}
	return octetDefault
}

// A state identifies the schemas of a value for the document's caches: a
// node, or, for several, a list of them.
type state struct {
	i    int32
	list string
}

func stateOf(s []value) state {
	if len(s) == 1 {
		return state{i: s[0].id()}
	}
	var b strings.Builder
	for _, v := range s {
		b.WriteString(strconv.Itoa(int(v.id())))
		b.WriteByte(',')
	}
	return state{i: -1, list: b.String()}
}

// defaults returns the default media types of a field whose schemas are s:
// those of the types they allow, an array's following its items, whose own
// array type is application/json where an item's schemas are OpenAPI 3.2's,
// whose table says so of an array inside a top-level array, and otherwise
// follows its items in turn. An items chain that leads back into schemas
// being resolved contributes the untyped default, so that every schema of a
// cycle has the same set, whichever is resolved first. The chain is followed
// without recursion, and each set is computed once: a set kept for schemas is
// what they give a field, which is what they give an item too, but for
// OpenAPI 3.2's, whose set as an item is not kept.
func (d *document) defaults(s []value) mediaSet {
	type step struct {
		key     state
		own     mediaSet
		untyped mediaSet
	}
	var path []step
	var acc mediaSet
	var one [1]value
	cycle, mark, lap := -1, -1, 1 // Brent's cycle detection: the step at the last power of two
	for len(s) > 0 {
		if len(path) > 0 && s[0].ok() && s[0].t.edition == 32 { // an item, whose array is application/json, whatever its items
			acc = d.typing(s, true).own
			break
		}
		k := stateOf(s)
		if set, ok := d.setOf(k); ok {
			acc = set
			break
		}
		if mark >= 0 && path[mark].key == k { // a cycle of len(path)-mark steps, entered at the first that recurs as far on
			n := len(path) - mark
			for cycle = 0; cycle+n < len(path) && path[cycle].key != path[cycle+n].key; cycle++ {
			}
			break
		}
		l := d.typing(s, false)
		if path = append(path, step{k, l.own, untypedDefault(s)}); !l.array {
			break
		}
		if len(path)-1-mark == lap || mark < 0 {
			mark, lap = len(path)-1, 2*lap
		}
		if s = l.items; l.item.ok() {
			one[0] = l.item
			s = one[:]
		}
	}
	if cycle >= 0 {
		acc = path[cycle].untyped
		for _, st := range path[cycle:] {
			acc |= st.own
		}
		for _, st := range path[cycle:] {
			d.keepSet(st.key, acc)
		}
		path = path[:cycle]
	}
	for i := len(path) - 1; i >= 0; i-- {
		acc |= path[i].own
		d.keepSet(path[i].key, acc)
	}
	return acc
}

// setOf returns the default media types of a field whose schemas are k,
// if known.
func (d *document) setOf(k state) (mediaSet, bool) {
	if k.i >= 0 {
		set := d.facts(k.i).set.Load()
		return mediaSet(set), set != 0
	}
	set, ok := d.types.Load(k.list)
	if !ok {
		return 0, false
	}
	return set.(mediaSet), true
}

// keepSet publishes set as the default media types of a field whose schemas
// are k, unless some are published.
func (d *document) keepSet(k state, set mediaSet) {
	if k.i >= 0 {
		d.facts(k.i).set.CompareAndSwap(0, uint32(set)|1<<8)
	} else {
		d.types.LoadOrStore(k.list, set)
	}
}

// newField compiles the field name, whose first declaration is schema and
// all of them roots, and whose Encoding Object is e, at src, under m.
func (d *document) newField(name string, schema *Schema, roots []value, e value, src string, m parsedMedia, whole bool) (*field, *Param) {
	f := &field{whole: whole, encoding: e, param: param{Param: &Param{Name: name, Schema: schema}}, roots: roots}
	p := f.Param
	if schema != nil {
		p.Source = schema.Source()
	}
	if e.ok() {
		p.Source = src
	}
	multipart := isMultipart(m)
	if bundled(e) { // none of it is read
		p.Err, f.err, f.style = errBundle, errBundle, &noStyle
		return f, p
	}
	// Under multipart its headers are described as Params of their own,
	// unless its style writes it as a named field (see Param.Headers); each
	// such Param reports what its Header Object holds, so its check here
	// leaves them out.
	describesHeaders := multipart && (whole || !byStyle(e, m))
	if d.holdsBundled(e, encodingKind, !describesHeaders, !describesHeaders) {
		p.Err, f.err, f.style = errBundle, errBundle, &noStyle
		if h := e.get("headers"); describesHeaders && !bundled(h) { // what it holds lies elsewhere
			p.Headers = d.headers(h, src+"/headers")
		}
		return f, p
	}
	// RFC 6570 fields apply under form-urlencoded and multipart/form-data, and
	// then contentType is ignored; a range of multipart types may be either
	// (OpenAPI 3.1.2 section 4.8.15.1.2), its form-data calls written by them.
	f.styled = byStyle(e, m) || multipart && m.sub == "*" && byStyle(e, formData)
	var style, explode, reserved, ctype value
	for k, v := range e.members() {
		switch k {
		case "style":
			style = v
		case "explode":
			explode = v
		case "allowReserved":
			reserved = v
		case "contentType":
			ctype = v
		case "headers":
			if describesHeaders {
				p.Headers = d.headers(v, src+"/headers")
			}
		}
	}
	f.style = &noStyle
	if f.styled {
		p.Style, p.AllowReserved = style.string(), reserved.kind() == 't' && !multipart
		if p.Style == "" {
			p.Style = "form"
		}
		f.param = compileStyle(p, "query", explode)
		if p.AllowReserved {
			f.set = reservedSet // in a body, which no "#" ends; a querystring encodes it as it is written
		}
		p.Err = d.defect(e, p.Err)
	}
	// Under form-urlencoded and multipart/form-data the style writes every
	// value of a named field, which so takes no type (see Param.ContentType),
	// but a positional part given as a Part takes one its Encoding lists.
	if f.styled && stylesApply(m) && !whole {
		return f, p
	}
	if f.listed = ctype.ok(); !f.listed {
		var set mediaSet
		if whole {
			set = d.typing(roots, true).own
		} else {
			set = d.defaults(roots)
		}
		for _, dm := range defaultMedia {
			if set&dm.set != 0 || set == 0 && dm.set == octetDefault {
				f.types, f.parsed = append(f.types, dm.parsed.full), append(f.parsed, dm.parsed)
			}
		}
		p.ContentType, f.class = strings.Join(f.types, ", "), f.parsed[0].class()
		f.plain = len(f.parsed) == 1 && !f.styled && !isForm(f.parsed[0])
	} else {
		p.ContentType = ctype.string()
		for _, t := range mediaList(p.ContentType) {
			m, ok := parseMedia(t)
			if !ok {
				f.err = d.defect(e, fmt.Errorf("contentType %q is not a list of media types or ranges", p.ContentType))
				break
			}
			f.types, f.parsed, f.class = append(f.types, t), append(f.parsed, m), m.class()
		}
		f.plain = len(f.parsed) == 1 && f.parsed[0].concrete() && !f.styled && f.err == nil && !isForm(f.parsed[0])
	}
	if !f.listed {
		return f, p
	}
	if p.Err == nil {
		if p.Err = f.err; f.styled && f.err != nil { // values its style writes, which ignore contentType: a range's form-data calls, or a positional part's
			sp := *p
			sp.Err = nil
			f.Param = &sp
		}
	}
	return f, p
}

// bundledEncoding reports whether the Encoding Object e is written as a
// reference or holds a value so written, at any depth (see holdsBundled). The
// fields of a nested part ask it again of the Encoding Objects their own
// field's walk passed, so its answer is kept.
func (d *document) bundledEncoding(e value) bool {
	return bundled(e) || d.holdsBundled(e, encodingKind, true, true)
}

// holdsBundled reports whether v, an Encoding Object (k encodingKind), a
// Media Type Object (mediaKind) or a Header Object (parameterKind), holds a
// value written as a reference among the objects it declares for encoding
// values: an encoding map, prefixEncoding list or itemEncoding, a headers
// map, a content map, and the Encoding, Header and Media Type Objects in
// them, at any depth. Such a value counts whatever media type, style or
// edition makes of it (see Operation), so it asks nothing of them: it reads
// the members the edition defines, as discovery does (see modelSlot), which
// no extension is. A Reference Object among them, as a Header Object, or in
// OpenAPI 3.2 a Media Type Object, may be, is followed as descriptions
// follow it, within the documents loaded: it counts when its target lies
// inside a value written as a reference or holds one. Without headers, the
// Header Objects of v's own headers map that a Param describes (see
// headerDescribed) are left out, as each reports what it holds.
//
// An object holding Encoding Objects, in its own maps or in its Header
// Objects' content, is read again whenever an object holding it is, and a
// reference target whenever a Reference Object leads to it, so their
// answers are kept: that of v, with keep, for a v that may be asked again,
// and those of the Encoding Objects so holding others and the targets the
// walk reads. Every other object is read only as part of one of those, once
// for each, so the walks of a document are together linear in what they
// read.
//
// It reads the Encoding Objects and reference targets v reaches one at a
// time, without recursion, as references may form long chains and cycles:
// the strongly connected components of what reaches what are found as it
// goes (Tarjan's algorithm), and none of them holds such a value unless
// the walk finds one, which ends it.
func (d *document) holdsBundled(v value, k kind, keep, headers bool) bool {
	if !v.ok() {
		return false
	}
	if keep = keep && headers; keep {
		if got, known := d.knownBundled(v.id(), bundleShift(k)); known {
			return got
		}
	}
	w := bundleWalk{d: d}
	found, holder, kids := w.scan(v, k, headers, nil)
	return w.run(v, k, keep && holder, found, kids)
}

// headersBundled reports whether the headers map h holds a value written as
// a reference (see holdsBundled) in a Header Object that no Param describes
// (see headerDescribed), as headers leaves Content-Type out.
func (d *document) headersBundled(h value) bool {
	w := bundleWalk{d: d}
	found, _, kids := w.slot(h, slot{parameterKind, '{'}, false, nil)
	return w.run(value{}, 0, false, found, kids)
}

// run ends the walk w that began at v, of kind k, whose answer is kept with
// keep, and found, or else read kids: the Encoding Objects and reference
// targets v holds.
func (w *bundleWalk) run(v value, k kind, keep, found bool, kids []bundleKid) bool {
	d := w.d
	switch {
	case found || len(kids) == 0:
		if keep {
			d.keepBundled(v.id(), bundleShift(k), found)
		}
		return found
	}
	w.push(v, k, keep, kids)
	for len(w.path) > 0 {
		i := w.path[len(w.path)-1]
		if n := &w.nodes[i]; n.next < len(n.kids) {
			kid := n.kids[n.next]
			n.next++
			id, shift := kid.v.id(), bundleShift(kid.k)
			key := uint64(id)<<3 | uint64(shift)
			if got, known := d.knownBundled(id, shift); known {
				if got {
					return w.found()
				}
				continue
			}
			if m, ok := w.seen[key]; ok { // a target being read, to which a cycle leads back
				n.low = min(n.low, w.nodes[m].index)
				continue
			}
			found, holder, kids := w.scan(kid.v, kid.k, true, nil)
			keep := kid.target || holder
			if found || len(kids) == 0 {
				if keep {
					d.keepBundled(id, shift, found)
				}
				if found {
					return w.found()
				}
				continue
			}
			if kid.target {
				if w.seen == nil {
					w.seen = map[uint64]int{}
				}
				w.seen[key] = len(w.nodes)
			}
			w.push(kid.v, kid.k, keep, kids)
			continue
		}
		n := w.nodes[i]
		if w.path = w.path[:len(w.path)-1]; len(w.path) > 0 {
			p := &w.nodes[w.path[len(w.path)-1]]
			p.low = min(p.low, n.low)
		}
		if n.low == n.index { // the root of a component, all of which the walk has read: none holds such a value
			for {
				m := w.scc[len(w.scc)-1]
				w.scc = w.scc[:len(w.scc)-1]
				if mn := &w.nodes[m]; mn.keep {
					d.keepBundled(mn.v.id(), bundleShift(mn.k), false)
				}
				if m == i {
					break
				}
			}
		}
	}
	return false
}

// A bundleWalk is one walk of holdsBundled: the objects it reads that hold
// others, in the order read, the path to the one being read, those whose
// component is not yet known, and the reference targets among them.
type bundleWalk struct {
	d     *document
	nodes []bundleNode
	path  []int
	scc   []int
	seen  map[uint64]int // node by id and kind, for each reference target, which a cycle may reach again
}

// A bundleNode is an object holdsBundled reads that holds others: what it
// holds, the next of them to read, and its place in the walk.
type bundleNode struct {
	v          value
	k          kind
	keep       bool // its answer is kept
	kids       []bundleKid
	next       int
	index, low int
}

// A bundleKid is an object a bundleNode holds: an Encoding Object, or the
// target of a Reference Object.
type bundleKid struct {
	v      value
	k      kind
	target bool
}

func (w *bundleWalk) push(v value, k kind, keep bool, kids []bundleKid) {
	i := len(w.nodes)
	w.nodes = append(w.nodes, bundleNode{v: v, k: k, keep: keep, kids: kids, index: i, low: i})
	w.path, w.scc = append(w.path, i), append(w.scc, i)
}

// found ends the walk with a value written as a reference, which every
// object on the path to it holds.
func (w *bundleWalk) found() bool {
	for _, i := range w.path {
		if n := &w.nodes[i]; n.keep {
			w.d.keepBundled(n.v.id(), bundleShift(n.k), true)
		}
	}
	return true
}

// scan reads v, an object of kind k, up to the Encoding Objects and
// reference targets it holds, which it adds to kids: it reports a value
// written as a reference it holds itself, and whether it holds Encoding
// Objects, directly or in its Header and Media Type Objects, which it reads
// itself, as they nest no deeper than a Header Object's content. Without
// headers, the Header Objects of v's own headers map that a Param describes
// are left out.
func (w *bundleWalk) scan(v value, k kind, headers bool, kids []bundleKid) (found, holder bool, _ []bundleKid) {
	for name, x := range v.members() {
		s, ok := modelSlot(k, name, v.t.edition)
		if !ok || s.k != encodingKind && s.k != mediaKind && s.k != parameterKind { // its schemas and examples
			continue
		}
		var h bool
		found, h, kids = w.slot(x, s, headers, kids)
		if holder = holder || h; found {
			break
		}
	}
	return found, holder, kids
}

// slot reads x, which a slot s of the model holds (see modelSlot), as scan
// reads an object.
func (w *bundleWalk) slot(x value, s slot, headers bool, kids []bundleKid) (found, holder bool, _ []bundleKid) {
	if s.how != '1' && bundled(x) { // a map or list, never a reference
		return true, false, kids
	}
	holder = s.k == encodingKind && x.hasMembers()
	for name, e := range x.members() {
		if s.how == '1' {
			e = x
		}
		switch ref, _, _ := reference(e); {
		case s.k == parameterKind && !headers && headerDescribed(name, x.t.edition):
		case !ref.ok() && s.k == encodingKind:
			kids = append(kids, bundleKid{v: e, k: encodingKind})
		case !ref.ok():
			var h bool
			found, h, kids = w.scan(e, s.k, true, kids)
			holder = holder || h
		case !referenceObject(s.k, e.t.edition):
			found = true
		default:
			t, _, _, err := w.d.follow(e, "")
			found = errors.Is(err, errBundle) // a target inside a value written as a reference
			if err == nil {
				kids = append(kids, bundleKid{t, s.k, true})
			}
		}
		if found || s.how == '1' {
			break
		}
	}
	return found, holder, kids
}

// headerDescribed reports whether the Header Object named name, in a headers
// map of an object of the given edition, is described by a Param of its own
// where headers describes the map: all but Content-Type, which OpenAPI 3
// ignores there.
func headerDescribed(name string, edition int) bool {
	return edition == 20 || !strings.EqualFold(name, "Content-Type")
}

// knownBundled returns what holdsBundled kept of node id as an object of the
// kind at shift, if anything, without making its facts.
func (d *document) knownBundled(id int32, shift int) (found, known bool) {
	if p := d.pages[id/factsPage].Load(); p != nil {
		got := p[id%factsPage].bundled.Load() >> shift
		return got&2 != 0, got&1 != 0
	}
	return false, false
}

// keepBundled keeps what holdsBundled found of node id as an object of the
// kind at shift.
func (d *document) keepBundled(id int32, shift int, found bool) {
	d.facts(id).bundled.Or(bundleBits(found) << shift)
}

// bundleShift is where a node's facts keep what holdsBundled found of it as
// an object of kind k: two bits, known and the answer.
func bundleShift(k kind) int {
	switch k {
	case mediaKind:
		return 2
	case parameterKind:
		return 4
	}
	return 0
}

// bundleBits is what holdsBundled keeps for an answer.
func bundleBits(found bool) uint32 {
	if found {
		return 3
	}
	return 1
}

// bundledMedia reports whether the Media Type Object v, at src, holds a value
// written as a reference among its Encoding Objects (see holdsBundled) that
// no Param of described, those describing its parts, reports: an encoding
// map or prefixEncoding list so written, or an Encoding Object that is or
// holds one and whose part none describes. None is described under a type
// whose parts the client does not describe, or in a response, and the
// encoding map's entries are not in a positional multipart body. Its
// answer is kept when v is shared.
func (d *document) bundledMedia(v value, src string, described []*Param, shared bool) bool {
	if found := d.holdsBundled(v, mediaKind, shared, true); !found || len(described) == 0 {
		return found
	}
	sources := make(map[string]bool, len(described))
	for _, p := range described {
		sources[p.Source] = true
	}
	for name, x := range v.members() {
		s, ok := modelSlot(mediaKind, name, v.t.edition)
		at := src + "/" + name
		switch {
		case !ok || s.k != encodingKind:
		case s.how == '1':
			if !sources[at] && d.bundledEncoding(x) {
				return true
			}
		case bundled(x):
			return true
		default:
			i := 0
			for key, e := range x.members() {
				if s.how == '[' {
					key = fmt.Sprint(i)
				}
				if i++; !sources[at+"/"+token(key)] && d.bundledEncoding(e) {
					return true
				}
			}
		}
	}
	return false
}

// mediaList splits a comma-separated list of media types, a comma inside a
// quoted parameter value excepted (RFC 9110 section 5.6.4).
func mediaList(s string) []string {
	var list []string
	quoted, start := false, 0
	for i := 0; i <= len(s); i++ {
		switch {
		case i == len(s) || s[i] == ',' && !quoted:
			list, start = append(list, trimOWS(s[start:i])), i+1
		case s[i] == '\\' && quoted && i+1 < len(s): // one at the end is left for the parser to refuse
			i++
		case s[i] == '"':
			quoted = !quoted
		}
	}
	return list
}

// headers describes the Header Objects of the headers map h of an Encoding
// or Response Object, at src, but Content-Type, which OpenAPI ignores there.
func (d *document) headers(h value, src string) []*Param {
	var list []*Param
	for name, v := range h.members() {
		if !headerDescribed(name, h.t.edition) {
			continue
		}
		at := src + "/" + token(name)
		if h.t.edition == 20 && bundled(v) { // a Swagger 2.0 Header Object is never a reference
			list = append(list, &Param{Name: name, In: "header", Source: at, Err: errBundle})
			continue
		}
		t, tat, desc, err := d.follow(v, at)
		if err == nil { // a reference that cannot be followed keeps its own location
			at = tat
		}
		p := &Param{Name: name, In: "header", Description: desc, Source: at, Err: err}
		if err == nil {
			p.Required, p.Deprecated, p.Schema = t.flag("required"), t.flag("deprecated"), d.schema(t.get("schema"), at, "/schema")
			if content := t.get("content"); !content.ok() && t.t.edition != 20 { // a header value, by the one style a Header Object has
				p.Style, p.Explode, p.ExplodeSet = "simple", t.flag("explode"), t.get("explode").ok()
				if style := t.get("style"); style.ok() && style.string() != "simple" {
					p.Err = fmt.Errorf("style %q is not allowed for a header value", style.string())
				}
			} else if bundled(content) {
				p.Err = errBundle
			} else if content.ok() {
				n := 0
				for typ, m := range content.members() {
					n++
					p.ContentType = typ
					mat := at + "/content/" + token(typ)
					if m.t.edition == 32 {
						m, mat, _, p.Err = d.follow(m, mat)
					} else if bundled(m) { // a Reference Object only from OpenAPI 3.2: none of it is read
						m, p.Err = value{}, errBundle
					}
					if d.holdsBundled(m, mediaKind, true, true) { // the header is the nearest part holding it, and many may share it
						p.Err = errBundle
					}
					p.Schema = d.schema(m.get("schema"), mat, "/schema")
				}
				if n != 1 || t.get("schema").ok() {
					p.Err = errors.New("a Header content map requires exactly one entry and no schema")
				}
			}
			if t.t.edition == 20 {
				pp := d.swaggerParam(t, at, p)
				p = pp.Param
			}
		}
		list = append(list, p)
	}
	return list
}

// media returns the media type of a value of f, as written and parsed, and
// its class: mt, a Part's MediaType, when set, which must be concrete and
// match one of the types f's Encoding lists, if it lists any; else f's own,
// when one concrete type selects itself.
func (f *field) media(mt string) (string, parsedMedia, class, error) {
	if mt == "" {
		if len(f.parsed) == 1 && f.parsed[0].concrete() {
			return f.types[0], f.parsed[0], f.class, nil
		}
		offers := make([]string, len(f.types))
		for i, t := range f.types {
			offers[i] = label(t)
		}
		return "", parsedMedia{}, 0, fmt.Errorf("the field offers %s; select one with Part.MediaType", strings.Join(offers, ", "))
	}
	m, ok := parseMedia(mt)
	switch {
	case !ok || !m.concrete():
		return "", parsedMedia{}, 0, errors.New("Part.MediaType is not a concrete media type")
	case f.listed && !slices.ContainsFunc(f.parsed, func(d parsedMedia) bool { _, ok := d.covers(m); return ok }):
		return "", parsedMedia{}, 0, fmt.Errorf("the field does not offer %s", label(m.full))
	}
	return mt, m, m.class(), nil
}

// A fieldValue is one value of a field, resolved: its content, its Part, if
// it is one, and its media type, as written and parsed, and its class.
type fieldValue struct {
	v  any
	pt *Part
	mt string
	m  parsedMedia
	k  class
}

// value resolves v, a value of f named name at at, in a form body or else a
// part, recording why it cannot be sent: f's Err first, as the field's own
// defect, then its Part's problems, a name a part cannot carry, and its media
// type's problems. ok is false when nothing is to be sent. read reports that
// v was read as a datum already, and is not null.
func (f *field) value(d *document, v any, name string, at key, form, read bool, re *RequestError) (fieldValue, bool) {
	v, pt, err := part(v, form)
	if err == nil && (pt != nil || !read) && !scalar(v) {
		x := d.datum(v)
		if x.null {
			return fieldValue{}, false // a value, or a Part's content, whose JSON data is null is omitted, whatever the media type
		}
		v = x.v
	}
	switch {
	case f.err != nil:
		re.input(at.String(), cmp.Or(f.Err, f.err)) // the Err it describes, which may be its style's
	case err != nil:
		re.input(at.String(), err)
	case !form && !quotable(name):
		re.input(at.String(), errors.New("a part name cannot hold a control character other than a tab"))
	default:
		mt, m, k, err := f.media(pt.mediaType())
		if err == nil {
			return fieldValue{v, pt, mt, m, k}, true
		}
		re.setting(at.String(), err)
	}
	return fieldValue{}, false
}

var errNotObject = errors.New("the body is an object, a map or a struct, whose properties are its fields or parts")

// members calls f with each member of v, an object, as encoding/json writes
// them, and the field of enc it is, returning nil, or why v is no object, or
// encoding/json's error for it. It walks a map, its keys as json takes them,
// sorted; a struct's fields as json chooses and omits them; and pointers and
// interfaces. The members of a value json writes by its own method, a map
// whose keys json refuses and a cycle of pointers are those of the JSON json
// writes for it, or json's refusal.
func (d *document) members(v any, enc *formEncoding, f func(name string, fd *field, v any)) error {
	if m, ok := v.(map[string]any); ok && m != nil {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			f(k, enc.field(k), m[k])
		}
		return nil
	}
	if j, ok := v.(jsonData); ok {
		return jsonMembers(j, enc, f)
	}
	rv := reflect.ValueOf(v)
	if h, ok := v.(held); ok {
		rv = h.p.Elem()
	}
	rv, ok := d.deref(rv)
	if !ok {
		return jsonMembers(v, enc, f)
	}
	if (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && rv.IsNil() || !rv.IsValid() {
		return errNotObject
	}
	w := d.walkOf(rv.Type(), nil)
	switch {
	case w.json || w.text || rv.CanAddr() && (w.ptrJSON || w.ptrText) || rv.Kind() == reflect.Map && !jsonKeys(rv.Type()):
		return jsonMembers(v, enc, f) // v itself, as json dispatches on the types it meets
	case w.reader || rv.Kind() == reflect.Map && rv.IsNil():
		return errNotObject
	case rv.Kind() == reflect.Map:
		type member struct {
			name string
			v    reflect.Value
		}
		ms := make([]member, 0, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			name, err := mapKey(it.Key())
			if err != nil {
				return &encodingError{err}
			}
			ms = append(ms, member{name, it.Value()})
		}
		slices.SortFunc(ms, func(a, b member) int { return strings.Compare(a.name, b.name) })
		for _, m := range ms {
			f(m.name, enc.field(m.name), d.elem(m.v))
		}
		return nil
	case rv.Kind() == reflect.Struct:
		fds := enc.fieldsOf(rv.Type(), w.fields)
		for i, jf := range w.fields {
			fv, err := rv.FieldByIndexErr(jf.index)
			if err != nil || jf.omitZero && omitsZero(fv) || jf.omitEmpty && omitsEmpty(fv) {
				continue // json writes no such field
			}
			x := d.elem(fv)
			if jf.quoted && !null(x) {
				if w := d.walkOf(reflect.TypeOf(x), nil); !w.json && !w.text {
					if s, err := marshal(x); err == nil {
						x = s // the string option: a JSON string holding the scalar's JSON, unless a method writes it
					}
				}
			}
			f(jf.name, fds[i], x)
		}
		return nil
	}
	return errNotObject
}

// jsonMembers calls f with the members of the object encoding/json writes for
// v, or a jsonData holds, each as the JSON data of a json.RawMessage of its
// text, and the fields of enc they are.
func jsonMembers(v any, enc *formEncoding, f func(name string, fd *field, v any)) error {
	j, ok := v.(jsonData)
	if !ok {
		j = marshaled(v)
	}
	switch {
	case j.err != nil:
		return j.err
	case j.s[0] != '{':
		return errNotObject
	}
	r := &jsonReader{s: j.s}
	r.each(func(name string) error {
		start := r.i
		r.skip()
		f(name, enc.field(name), rawData(r.s[start:r.i]))
		return nil
	})
	return nil
}

// rawData is the JSON data of a json.RawMessage of s, JSON text as marshal
// writes it, which bare makes only for a caller's codec.
func rawData(s string) jsonData { return jsonData{s: s} }

// deref returns v past the pointers and interfaces encoding/json follows to
// write it, to a value it writes by its own method or by reflection, or to
// a nil one, which it writes as null. It reports false for a cycle, which
// json refuses: a pointer met again past maxDepth dereferences.
func (d *document) deref(v reflect.Value) (reflect.Value, bool) {
	var seen map[ptrKey]bool
	for n := 0; (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && !v.IsNil(); n++ {
		if w := d.walkOf(v.Type(), nil); w.json || w.text || w.reader {
			break
		}
		if n > maxDepth && v.Kind() == reflect.Pointer {
			k := ptrKey{v.Type(), v.Pointer()}
			if seen[k] {
				return v, false
			}
			if seen == nil {
				seen = map[ptrKey]bool{}
			}
			seen[k] = true
		}
		v = v.Elem()
	}
	return v, true
}

// elem returns the value v holds as encoding/json reaches it: a pointer to
// it where v can be addressed and json writes it otherwise than a copy, by
// a pointer's method at it or inside it, held when that pointer is a reader;
// and held when v's interface type selects a method other than the value's
// own (see item).
func (d *document) elem(v reflect.Value) any {
	switch {
	case v.Kind() == reflect.Interface:
		return d.item(v)
	case v.CanAddr():
		if w := d.walkOf(v.Type(), nil); w.addr && w.ptrRead {
			return held{v.Addr()}
		} else if w.addr {
			return v.Addr().Interface()
		}
	}
	return v.Interface()
}

// item returns the value the interface v holds inside a value, as a struct
// field, map value or array element, as encoding/json writes the value around
// it: held when v's interface type selects a method other than the value's
// own, as MarshalText does over a MarshalJSON, or one the value does not run,
// as either does on a nil pointer, whose own encoding is null. A whole list
// body's element is read on its own instead (see datum.item).
func (d *document) item(v reflect.Value) any {
	if v.Kind() == reflect.Interface && v.NumMethod() > 0 && !v.IsNil() { // an empty interface selects no method
		w := d.walkOf(v.Type(), nil)
		e := v.Elem()
		if (w.json || w.text) && e.Kind() == reflect.Pointer && e.IsNil() || w.text && !w.json && d.walkOf(e.Type(), nil).json {
			p := reflect.New(v.Type())
			p.Elem().Set(v)
			return held{p}
		}
	}
	return v.Interface()
}

// A key is where a field's value is in a body, for a RequestError: the
// body's key, then, as a JSON Pointer, the property's name and an item's
// index, if any; built only for a problem.
type key struct {
	body, name string
	item       int // -1 for the property's value
}

func (k key) String() string {
	s := k.body + "/" + escapeToken(k.name)
	if k.item >= 0 {
		s += "/" + strconv.Itoa(k.item)
	}
	return s
}

// values calls f with v, a field's value, at at, or, when encoding/json
// writes v as an array and v is no []byte, with each item, at its index. A
// value or item whose JSON data is null is left out. It reports whether v
// is a nonempty array, which counts as given even if no items are written.
func (d *document) values(v any, at key, f func(v any, at key)) bool {
	item := func(i int) key { at.item = i; return at }
	switch x := v.(type) {
	case nil, string, int, bool, float64, json.Number, []byte, Part, *Part, io.Reader:
	case jsonData:
		return dataValues(x, at, f)
	case []any:
		for i, v := range x {
			if !null(v) {
				f(v, item(i))
			}
		}
		return len(x) > 0
	default:
		rv := reflect.ValueOf(v)
		if h, ok := v.(held); ok {
			rv = h.p.Elem()
		}
		rv, ok := d.deref(rv)
		if !ok || !rv.IsValid() {
			break // a cycle, which json refuses as it encodes the value
		}
		w := d.walkOf(rv.Type(), nil)
		switch k := rv.Kind(); {
		case w.json || rv.CanAddr() && w.ptrJSON:
			return dataValues(marshaled(v), at, f)
		case w.text || rv.CanAddr() && w.ptrText || k != reflect.Slice && k != reflect.Array || k == reflect.Slice && bytesKind(rv.Type()):
		default:
			for i := range rv.Len() {
				if v := d.elem(rv.Index(i)); !null(v) {
					f(v, item(i))
				}
			}
			return rv.Len() > 0
		}
	}
	if !null(v) {
		f(v, at)
	}
	return false
}

// dataValues calls f as values does for the JSON data j: with each item of an
// array, null items left out, at its index, or else with j, unless it is
// null.
func dataValues(j jsonData, at key, f func(v any, at key)) bool {
	if j.err != nil || j.s[0] != '[' {
		if j.err != nil || j.s != "null" {
			f(j, at)
		}
		return false
	}
	r := &jsonReader{s: j.s}
	at.item = 0
	r.each(func(string) error {
		start := r.i
		r.skip()
		if s := r.s[start:r.i]; s != "null" {
			f(rawData(s), at)
		}
		at.item++
		return nil
	})
	return j.s[1] != ']'
}

// bytesKind reports whether encoding/json writes the slice type t as base64.
func bytesKind(t reflect.Type) bool {
	e := reflect.PointerTo(t.Elem())
	return t.Elem().Kind() == reflect.Uint8 && !e.Implements(marshalerType) && !e.Implements(textMarshalerType)
}

// null reports whether encoding/json writes v as null by its Go value: a nil
// interface or pointer, or a nil map or slice that it writes by reflection.
func null(v any) bool {
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Invalid:
		return true
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	case reflect.Map, reflect.Slice:
		t := rv.Type()
		return rv.IsNil() && !t.Implements(marshalerType) && !t.Implements(textMarshalerType) && (rv.Kind() == reflect.Slice || jsonKeys(t))
	}
	return false
}

// A datum is a value as json.Marshal reads it: its JSON data is null, a list,
// whose items are items, or other data, bytes when it is a byte slice's
// base64 string. v is the value to carry on: as given, or a jsonData when json
// writes it by its own MarshalJSON, or cannot write it.
type datum struct {
	v                 any
	null, list, bytes bool
	items             reflect.Value
}

// datum reads v as json.Marshal does: through the pointers and interfaces it
// follows, a nil one, a nil map whose keys it can write and a nil slice being
// null, up to a value it
// writes by its own MarshalJSON, which it runs once on v itself (json
// dispatches on the types it meets), or MarshalText, which writes a string,
// or by reflection, a slice or an array, not a []byte, being a list. Its
// elements are read each on its own. A reader is raw content, given as it is,
// though null when its own MarshalJSON writes null.
func (d *document) datum(v any) datum {
	switch x := v.(type) {
	case nil:
		return datum{null: true}
	case string, int, bool, float64, json.Number:
		return datum{v: v}
	case map[string]any:
		return datum{v: v, null: x == nil}
	case []any:
		return datum{v: v, null: x == nil, list: x != nil, items: reflect.ValueOf(x)}
	case jsonData:
		return x.datum()
	case held:
		if d.walkOf(x.p.Elem().Type(), nil).json {
			return marshaled(v).datum()
		}
		return datum{v: v}
	}
	if _, reader := v.(io.Reader); reader {
		if null(v) {
			return datum{v: v, null: true}
		}
		if !d.walkOf(reflect.TypeOf(v), nil).json {
			return datum{v: v}
		}
		s, err := marshal(v)
		return datum{v: v, null: err == nil && s == "null"}
	}
	rv, ok := d.deref(reflect.ValueOf(v))
	switch {
	case !ok: // a cycle, which json refuses as it encodes v
		return datum{v: v}
	case !rv.IsValid(), (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && rv.IsNil():
		return datum{v: v, null: true}
	}
	switch w := d.walkOf(rv.Type(), nil); {
	case w.json:
		return marshaled(v).datum() // a caller's codec still receives v
	case w.text || w.reader:
		return datum{v: v}
	}
	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() && !jsonKeys(rv.Type()) {
			return marshaled(v).datum() // json's refusal of its keys, nil or not
		}
		return datum{v: v, null: rv.IsNil()}
	case reflect.Slice:
		if rv.IsNil() {
			return datum{v: v, null: true}
		}
		if bytesKind(rv.Type()) {
			return datum{v: v, bytes: true} // a base64 string
		}
		fallthrough
	case reflect.Array:
		return datum{v: v, list: true, items: rv}
	}
	return datum{v: v}
}

// len returns the number of items of a list, and 0 for null.
func (x datum) len() int {
	if !x.list {
		return 0
	}
	return x.items.Len()
}

// item returns the list's item i on its own, as an iterator yields it: the
// value the element holds, whatever the list's element type, a copy where
// the element could be addressed.
func (x datum) item(i int) any { return x.items.Index(i).Interface() }

// scalar reports whether v is a string, bool or number of a type a datum
// reads as it is: never null, and written by no method.
func scalar(v any) bool {
	switch v.(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, uintptr, float32, float64:
		return true
	}
	return false
}

// nullData reports whether v's JSON data is null.
func (d *document) nullData(v any) bool { return d.datum(v).null }

// datum reads the JSON data j: null, a list of its items, each the JSON data
// of a json.RawMessage, or other data, as which j is carried on.
func (j jsonData) datum() datum {
	switch {
	case j.err != nil:
		return datum{v: j}
	case j.s == "null":
		return datum{v: j, null: true}
	case j.s[0] == '[':
		return datum{v: j, list: true, items: reflect.ValueOf(jsonItems(j.s))}
	}
	return datum{v: j}
}

// jsonItems returns the items of the JSON array s, as marshal writes it, each
// as the JSON data of a json.RawMessage.
func jsonItems(s string) []any {
	r := &jsonReader{s: s}
	var items []any
	r.each(func(string) error {
		start := r.i
		r.skip()
		items = append(items, rawData(r.s[start:r.i]))
		return nil
	})
	return items
}

// part returns the content of v, a field's value, and its Part, if it is one
// or a non-nil *Part, or why the Part cannot be used; form refuses what
// applies only to parts.
func part(v any, form bool) (any, *Part, error) {
	var pt *Part
	switch p := v.(type) {
	case Part:
		pt = &p
	case *Part:
		pt = p
	default:
		return v, nil, nil
	}
	err := checkHeader(pt.Header)
	switch {
	case form && (pt.Filename != "" || pt.NoFilename || len(pt.Header) > 0):
		err = errors.New("a form field takes no Filename, NoFilename or Header")
	case pt.Filename != "" && pt.NoFilename:
		err = errors.New("the Part sets both Filename and NoFilename")
	case !quotable(pt.Filename):
		err = errors.New("a filename cannot hold a control character other than a tab")
	}
	return pt.Content, pt, err
}

// quotable reports whether a quoted-string can carry s: no control
// character but a tab (RFC 9110 section 5.6.4).
func quotable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < ' ' && r != '\t' || r == 0x7f })
}

// mediaType returns the Part's MediaType, or "" for none.
func (pt *Part) mediaType() string {
	if pt == nil {
		return ""
	}
	return pt.MediaType
}
