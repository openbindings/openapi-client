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
	styled bool          // its Encoding sets style, explode or allowReserved, where they apply
	types  []string      // the media types it takes, as written
	parsed []parsedMedia // types, parsed
	class  class         // the class of a sole type
	listed bool          // its Encoding lists them, so a Part's MediaType must match one
	roots  []value       // its declarations, whose properties a nested part's fields are
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
// declare; compiled once per schema.
func (d *document) nested(f *field, item bool) *formEncoding {
	roots := f.roots
	if sh := d.shapeOf(roots); sh.array && (item || !sh.object) {
		roots = sh.items
	}
	if len(roots) == 0 {
		return noFields
	}
	k := stateKey(roots)
	if enc, ok := d.forms.Load(k); ok {
		return enc.(*formEncoding)
	}
	enc, _ := d.encodingOf(roots, "", value{}, "", parsedMedia{})
	got, _ := d.forms.LoadOrStore(k, enc)
	return got.(*formEncoding)
}

// closure calls f with each schema of s, at src, and every schema they reach
// by $ref and allOf, depth first in document order, each once and at most
// maxDepth of them. It reports false when there were more. A $ref is a
// keyword of its schema (JSON Schema 2020-12 section 8.2.3.1), its target
// another schema reached, whose own keywords and $ref apply too.
func (d *document) closure(s []value, src string, f func(s value, at string)) bool {
	type schema struct {
		v  value
		at string
	}
	var stack []schema
	for i := len(s) - 1; i >= 0; i-- {
		stack = append(stack, schema{s[i], src})
	}
	var seen map[int32]bool // made once a schema can be reached twice
	if len(s) > 1 {
		seen = map[int32]bool{}
	}
	for n := 0; len(stack) > 0; {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !top.v.ok() || seen[top.v.i] {
			continue
		}
		if n++; n > maxDepth {
			return false
		}
		f(top.v, top.at)
		mark := len(stack)
		for name, m := range top.v.members() {
			switch name {
			case "$ref":
				if m.kind() != '"' {
					break
				}
				if t, ptr, err := d.target(m.text()); err == nil {
					at := ""
					if top.at != "" {
						at = d.source(ptr)
					}
					stack = append(stack, schema{t, at})
				}
			case "allOf":
				i := 0
				for _, e := range m.members() {
					at := ""
					if top.at != "" {
						at = top.at + "/allOf/" + strconv.Itoa(i)
					}
					stack, i = append(stack, schema{e, at}), i+1
				}
			}
		}
		if len(stack) > mark || seen != nil {
			if seen == nil {
				seen = map[int32]bool{}
			}
			seen[top.v.i] = true
			slices.Reverse(stack[mark:]) // the first in document order on top
		}
	}
	return true
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

// A shape is what a field's schemas say of its values' types: the defaults
// of the types they allow other than array (OpenAPI 3.1.2 section
// 4.8.15.1.1), whether they allow an object or an array, and the schemas of
// an array's items.
type shape struct {
	own           mediaSet
	object, array bool
	items         []value
}

// JSON Schema's types, as bits.
var jsonTypes = map[string]uint8{"string": 1, "number": 2, "integer": 4, "boolean": 8, "object": 16, "array": 32, "null": 64}

// shapeOf returns the shape of the values the schemas s and those their $ref
// and allOf reach allow: the types they allow, intersected, a schema without
// type allowing all. A string with a contentEncoding is
// application/octet-stream, as is a value whose type no schema constrains,
// or nothing can be.
func (d *document) shapeOf(s []value) shape {
	allowed, constrained, encoded := uint8(127), false, false
	var items []value
	all := d.closure(s, "", func(s value, _ string) {
		if t := s.get("type"); t.ok() {
			set := uint8(0)
			if t.kind() == '"' {
				set = jsonTypes[t.text()]
			}
			for _, n := range t.members() {
				set |= jsonTypes[n.string()]
			}
			allowed, constrained = allowed&set, true
		}
		encoded = encoded || s.get("contentEncoding").ok()
		if it := s.get("items"); it.ok() {
			items = append(items, it)
		}
	})
	var sh shape
	switch {
	case !all || !constrained || allowed&^64 == 0:
		return shape{own: octetDefault}
	case allowed&1 != 0 && encoded:
		sh.own = octetDefault
	case allowed&1 != 0:
		sh.own = textDefault
	}
	if allowed&(2|4|8) != 0 {
		sh.own |= textDefault
	}
	if sh.object = allowed&16 != 0; sh.object {
		sh.own |= jsonDefault
	}
	if sh.array = allowed&32 != 0; sh.array && len(items) == 0 {
		sh.own |= octetDefault // items allowing anything have no type
	}
	sh.items = items
	return sh
}

// stateKey identifies the schemas s for the document's caches.
func stateKey(s []value) any {
	if len(s) == 1 {
		return s[0].i
	}
	var b strings.Builder
	for _, v := range s {
		b.WriteString(strconv.Itoa(int(v.i)))
		b.WriteByte(',')
	}
	return b.String()
}

// defaults returns the default media types of a field whose schemas are s:
// those of the types they allow, an array's following its items. An items
// chain that leads back into schemas being resolved contributes the absent
// type, so that every schema of a cycle has the same set, whichever is
// resolved first. The chain is followed without recursion, and each set is
// computed once.
func (d *document) defaults(s []value) mediaSet {
	type step struct {
		key any
		own mediaSet
	}
	var path []step
	var on map[any]int // where each key is on the path, once it goes on
	var acc mediaSet
	cycle := -1
	for len(s) > 0 {
		if len(path) > maxDepth { // the absent type, past the bound on links
			acc = octetDefault
			break
		}
		k := stateKey(s)
		if set, ok := d.types.Load(k); ok {
			acc = set.(mediaSet)
			break
		}
		if i, ok := on[k]; ok {
			cycle = i
			break
		}
		sh := d.shapeOf(s)
		path = append(path, step{k, sh.own})
		if !sh.array {
			break
		}
		if on == nil {
			on = map[any]int{}
		}
		on[k], s = len(path)-1, sh.items
	}
	if cycle >= 0 {
		acc = octetDefault
		for _, st := range path[cycle:] {
			acc |= st.own
		}
		for _, st := range path[cycle:] {
			d.types.Store(st.key, acc)
		}
		path = path[:cycle]
	}
	for i := len(path) - 1; i >= 0; i-- {
		acc |= path[i].own
		d.types.Store(path[i].key, acc)
	}
	return acc
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
		case s[i] == '\\' && quoted:
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
