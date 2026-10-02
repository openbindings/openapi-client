package openapi

import (
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
	byName map[string]*field
	lists  sync.Map // reflect.Type to []*field
}

// noFields is the encoding of a nested part no schema describes.
var noFields = &formEncoding{}

// A field is a form or multipart field compiled: its descriptor; the RFC
// 6570 style its Encoding sets, which applies under form-urlencoded and
// multipart/form-data; and the media types it otherwise takes.
type field struct {
	param
	styled bool                            // its Encoding sets style, explode or allowReserved, where they apply
	types  []string                        // the media types it takes, as written
	parsed []parsedMedia                   // types, parsed
	class  class                           // the class of a sole type
	listed bool                            // its Encoding lists them, so a Part's MediaType must match one
	roots  []value                         // its declarations, whose properties a nested part's fields are
	nested [2]atomic.Pointer[formEncoding] // the fields of its nested part, and of an item's
}

// untyped is a field no schema or Encoding describes, whose type is absent
// (OpenAPI 3.1.2 section 4.8.15.1.1).
var untyped = &field{param: param{Param: &Param{ContentType: octetStream.full}},
	types: []string{octetStream.full}, parsed: []parsedMedia{octetStream}, class: otherClass}

func (e *formEncoding) field(name string) *field {
	if f := e.byName[name]; f != nil {
		return f
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

// encodingOf compiles the fields of a form or multipart body whose schema is
// s, at src, with the Encoding Objects of encodings, at esrc, under the media
// type or range m, and describes them: the properties s and the schemas its
// $ref and allOf reach declare, then the names only encodings has.
func (d *document) encodingOf(s []value, src string, encodings value, esrc string, m parsedMedia) (*formEncoding, []*Param) {
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
		f := d.newField(name, first[i], fields[name].roots, encodings.get(name), esrc+"/"+token(name), m)
		fields[name], list = f, append(list, f.Param)
	}
	for name, e := range encodings.members() {
		if fields[name] == nil {
			f := d.newField(name, nil, nil, e, esrc+"/"+token(name), m)
			fields[name], list = f, append(list, f.Param)
		}
	}
	return &formEncoding{byName: fields}, list
}

// nested returns the fields of a nested part f writes from an object: in
// OpenAPI 3.1 those its schemas declare, each of its default type, or, for an
// item or a field whose schemas allow an array but no object, those its items
// declare; compiled once per schema, and found once per field.
func (d *document) nested(f *field, item bool) *formEncoding {
	p := &f.nested[0]
	if item {
		p = &f.nested[1]
	}
	if enc := p.Load(); enc != nil {
		return enc
	}
	roots, enc := f.roots, noFields
	if l := d.typing(roots); l.array && (item || !l.object) {
		if roots = l.items; l.item.ok() {
			roots = []value{l.item}
		}
	}
	if len(roots) > 0 {
		k := stateOf(roots)
		e, ok := d.forms.Load(k)
		if !ok {
			e, _ = d.encodingOf(roots, "", value{}, "", parsedMedia{})
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
var jsonTypes = map[string]uint8{"string": 1, "number": 2, "integer": 4, "boolean": 8, "object": 16, "array": 32, "null": 64}

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
	if t.ok() {
		s.typed, s.types = true, jsonTypes[t.string()]
		for _, n := range t.members() {
			s.types |= jsonTypes[n.string()]
		}
		if s.types&2 != 0 {
			s.types |= 4 // a number may be an integer (JSON Schema 2020-12 Validation section 6.1.1)
		}
	}
	s.encoded, s.declares = enc.ok(), props.ok() || s.items.ok()
	var kids []kid
	if ref.kind() == '"' {
		if t, ptr, err := d.target(ref.text()); err == nil {
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

// The facts of a schema node are what it compiles to, each once: its shape,
// and the default media types of a field it alone types.
type facts struct {
	shape atomic.Pointer[shape]
	set   atomic.Uint32 // the mediaSet, with bit 8 set once known
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
	f := d.facts(v.i)
	if s := f.shape.Load(); s != nil {
		return s
	}
	s, kids := d.own(v)
	kept := kids[:0] // filtered in place
	for _, k := range kids {
		ks := d.facts(k.v.i).shape.Load()
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
	f := d.facts(v.i)
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
	var buf [4]node
	var pbuf, obuf [4]int
	nodes, path, open := buf[:0], pbuf[:0], obuf[:0] // nodes in the order entered, which is each one's index
	var byNode map[int32]int                         // once there are many
	for next := v; ; {
		if next.ok() { // enter it
			nodes, path, open = append(nodes, node{v: next, s: s, kids: kids, kept: kids[:0], low: len(nodes)}), append(path, len(nodes)), append(open, len(nodes))
			if byNode == nil && len(nodes) > many {
				byNode = map[int32]int{}
				for n := range nodes {
					byNode[nodes[n].v.i] = n
				}
			} else if byNode != nil {
				byNode[next.i] = len(nodes) - 1
			}
			next = value{}
		}
		top := path[len(path)-1]
		n := &nodes[top]
		if n.next < len(n.kids) {
			k := n.kids[n.next]
			n.next++
			m, ok := byNode[k.v.i]
			for j := 0; byNode == nil && j < len(nodes) && !ok; j++ {
				m, ok = j, nodes[j].v.i == k.v.i
			}
			switch ks := d.facts(k.v.i).shape.Load(); {
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
		s := n.s
		for _, m := range open[i:] {
			s.meet(&nodes[m].s)
		}
		var done *shape
		for _, m := range open[i:] {
			ms := s
			if ms.kids = nodes[m].kept; !s.declares {
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
	var sbuf [8]schema
	var vbuf [many]int32
	stack, visited := sbuf[:0], vbuf[:0] // the schemas visited, scanned while few
	var seen map[int32]bool              // and then looked up
	for i := len(s) - 1; i >= 0; i-- {
		stack = append(stack, schema{s[i], src})
	}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !top.v.ok() || seen[top.v.i] || seen == nil && slices.Contains(visited, top.v.i) {
			continue
		}
		if seen == nil && len(visited) == many {
			seen = map[int32]bool{}
			for _, i := range visited {
				seen[i] = true
			}
		}
		if seen != nil {
			seen[top.v.i] = true
		} else {
			visited = append(visited, top.v.i)
		}
		f(top.v, top.at)
		kids := d.shapeOf(top.v).kids
		for i := len(kids) - 1; i >= 0; i-- { // the first in document order on top
			k, at := kids[i], ""
			switch {
			case top.at == "":
			case k.n < 0:
				at = d.source(k.ptr)
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
// application/octet-stream, as is a value whose type no schema constrains,
// or nothing can be.
type typing struct {
	own           mediaSet
	object, array bool
	item          value
	items         []value
}

// typing returns the typing of a value whose schemas are s.
func (d *document) typing(s []value) typing {
	sh := shape{types: 127}
	for _, r := range s {
		if r.ok() {
			sh.meet(d.shapeOf(r))
		}
	}
	var l typing
	switch t := sh.types; {
	case !sh.typed || t&^64 == 0:
		return typing{own: octetDefault}
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
	case !sh.items.ok():
		l.own |= octetDefault // items allowing anything have no type
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

// A state identifies the schemas of a value for the document's caches: a
// node, or, for several, a list of them.
type state struct {
	i    int32
	list string
}

func stateOf(s []value) state {
	if len(s) == 1 {
		return state{i: s[0].i}
	}
	var b strings.Builder
	for _, v := range s {
		b.WriteString(strconv.Itoa(int(v.i)))
		b.WriteByte(',')
	}
	return state{i: -1, list: b.String()}
}

// defaults returns the default media types of a field whose schemas are s:
// those of the types they allow, an array's following its items. An items
// chain that leads back into schemas being resolved contributes the absent
// type, so that every schema of a cycle has the same set, whichever is
// resolved first. The chain is followed without recursion, and each set is
// computed once.
func (d *document) defaults(s []value) mediaSet {
	type step struct {
		key state
		own mediaSet
	}
	var path []step
	var acc mediaSet
	var one [1]value
	cycle, mark, lap := -1, -1, 1 // Brent's cycle detection: the step at the last power of two
	for len(s) > 0 {
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
		l := d.typing(s)
		if path = append(path, step{k, l.own}); !l.array {
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
		acc = octetDefault
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
func (d *document) newField(name string, schema *Schema, roots []value, e value, src string, m parsedMedia) *field {
	f := &field{param: param{Param: &Param{Name: name, Schema: schema}}, roots: roots}
	p := f.Param
	if schema != nil {
		p.Source = schema.Source()
	}
	if e.ok() {
		p.Source = src
	}
	multipart := isMultipart(m)
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
			if multipart {
				p.Headers = d.headers(v, src+"/headers")
			}
		}
	}
	// RFC 6570 fields apply under form-urlencoded and multipart/form-data, and
	// then contentType is ignored; a range of multipart types may be either
	// (OpenAPI 3.1.2 section 4.8.15.1.2).
	f.style = &noStyle
	if f.styled = (style.ok() || explode.ok() || reserved.ok()) && (stylesApply(m) || multipart && m.sub == "*"); f.styled {
		p.Style, p.AllowReserved = style.string(), reserved.kind() == 't' && !multipart
		if p.Style == "" {
			p.Style = "form"
		}
		f.param = compileStyle(p, "query", explode)
		if stylesApply(m) {
			return f
		}
	}
	if f.listed = ctype.ok(); !f.listed {
		set := d.defaults(roots)
		for _, dm := range defaultMedia {
			if set&dm.set != 0 || set == 0 && dm.set == octetDefault {
				f.types, f.parsed = append(f.types, dm.parsed.full), append(f.parsed, dm.parsed)
			}
		}
		p.ContentType, f.class = strings.Join(f.types, ", "), f.parsed[0].class()
		return f
	}
	p.ContentType = ctype.string()
	for _, t := range mediaList(p.ContentType) {
		m, ok := parseMedia(t)
		if !ok {
			p.Err = fmt.Errorf("contentType %q is not a list of media types or ranges", p.ContentType)
			break
		}
		f.types, f.parsed, f.class = append(f.types, t), append(f.parsed, m), m.class()
	}
	return f
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

// headers describes the Header Objects of an Encoding's headers map h, at
// src, but Content-Type, which OpenAPI ignores there.
func (d *document) headers(h value, src string) []*Param {
	var list []*Param
	for name, v := range h.members() {
		if strings.EqualFold(name, "Content-Type") {
			continue
		}
		t, at, desc, err := d.follow(v, src+"/"+token(name))
		p := &Param{Name: name, In: "header", Description: desc, Source: at, Err: err}
		if err == nil {
			p.Required, p.Deprecated, p.Schema = t.flag("required"), t.flag("deprecated"), d.schema(t.get("schema"), at, "/schema")
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
		return "", parsedMedia{}, 0, fmt.Errorf("the field offers %s; select one with Part.MediaType", f.ContentType)
	}
	m, ok := parseMedia(mt)
	switch {
	case !ok || !m.concrete():
		return "", parsedMedia{}, 0, errors.New("Part.MediaType is not a concrete media type")
	case f.listed && !slices.ContainsFunc(f.parsed, func(d parsedMedia) bool { _, ok := d.covers(m); return ok }):
		return "", parsedMedia{}, 0, fmt.Errorf("the field does not offer %s", m.full)
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
// part, recording why it cannot be sent: its Part's problems, a name a part
// cannot carry, f's Err, and its media type's problems. ok is false when
// nothing is to be sent.
func (f *field) value(v any, name string, at key, form bool, re *RequestError) (fieldValue, bool) {
	v, pt, ok := part(v, at, form, re)
	if !ok {
		return fieldValue{}, false
	}
	mt, m, k, err := f.media(pt.mediaType())
	switch {
	case !form && !quotable(name):
		re.input(at.String(), errors.New("a part name cannot hold a control character other than a tab"))
	case f.Err != nil:
		re.input(at.String(), f.Err)
	case err != nil:
		re.setting(at.String(), err)
	default:
		return fieldValue{v, pt, mt, m, k}, true
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
	rv, ok := d.deref(reflect.ValueOf(v))
	if !ok {
		return jsonMembers(v, enc, f)
	}
	if (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && rv.IsNil() || !rv.IsValid() {
		return errNotObject
	}
	w := d.walkOf(rv.Type(), nil)
	switch {
	case w.json || w.text || rv.CanAddr() && (w.ptrJSON || w.ptrText) || rv.Kind() == reflect.Map && !jsonKeys(rv.Type()):
		return jsonMembers(d.elem(rv), enc, f)
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
// v, each as its JSON text, and the fields of enc they are.
func jsonMembers(v any, enc *formEncoding, f func(name string, fd *field, v any)) error {
	s, err := marshal(v)
	switch {
	case err != nil:
		return err
	case s[0] != '{':
		return errNotObject
	}
	r := &jsonReader{s: s}
	r.each(func(name string) error {
		start := r.i
		r.skip()
		f(name, enc.field(name), json.RawMessage(r.s[start:r.i]))
		return nil
	})
	return nil
}

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
// a pointer's method at it or inside it, unless that pointer would be read
// as a reader.
func (d *document) elem(v reflect.Value) any {
	if v.CanAddr() {
		if w := d.walkOf(v.Type(), nil); w.addr && !w.ptrRead {
			return v.Addr().Interface()
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
// value or item whose JSON data is null is left out.
func (d *document) values(v any, at key, f func(v any, at key)) {
	item := func(i int) key { at.item = i; return at }
	switch x := v.(type) {
	case nil, string, int, bool, float64, json.Number, []byte, Part, *Part, io.Reader:
	case []any:
		for i, v := range x {
			if !null(v) {
				f(v, item(i))
			}
		}
		return
	default:
		rv, ok := d.deref(reflect.ValueOf(v))
		if !ok || !rv.IsValid() {
			break // a cycle, which json refuses as it encodes the value
		}
		w := d.walkOf(rv.Type(), nil)
		switch k := rv.Kind(); {
		case w.json || rv.CanAddr() && w.ptrJSON:
			if s, err := marshal(d.elem(rv)); err == nil && s[0] == '[' {
				r, i := &jsonReader{s: s}, 0
				r.each(func(string) error {
					start := r.i
					r.skip()
					if v := r.s[start:r.i]; v != "null" {
						f(json.RawMessage(v), item(i))
					}
					i++
					return nil
				})
				return
			}
		case w.text || rv.CanAddr() && w.ptrText || k != reflect.Slice && k != reflect.Array || k == reflect.Slice && bytesKind(rv.Type()):
		default:
			for i := range rv.Len() {
				if v := d.elem(rv.Index(i)); !null(v) {
					f(v, item(i))
				}
			}
			return
		}
	}
	if !null(v) {
		f(v, at)
	}
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

// part returns the content of v, a field's value, its Part, if it is one or
// a non-nil *Part, and whether to send it, recording at at why the Part
// cannot be used; form refuses what applies only to parts.
func part(v any, at key, form bool, re *RequestError) (any, *Part, bool) {
	var pt *Part
	switch p := v.(type) {
	case Part:
		pt = &p
	case *Part:
		pt = p
	default:
		return v, nil, true
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
	if err != nil {
		re.input(at.String(), err)
		return nil, nil, false
	}
	return pt.Content, pt, !null(pt.Content)
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
