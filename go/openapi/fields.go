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
)

// A formEncoding is the Encoding of a form or multipart Media, compiled once
// per node: its fields by name.
type formEncoding struct {
	fields map[string]*field
}

// A field is a form or multipart field compiled: its descriptor and, for
// one written by RFC 6570, its style, or else the media types it takes.
type field struct {
	param
	types  []string      // the media types ContentType lists, as written
	parsed []parsedMedia // types, parsed
	class  class         // the class of a sole type
	listed bool          // its Encoding lists them, so a Part's MediaType must match one
	schema value         // its schema, whose properties a nested multipart part's fields are
}

// untyped is a field no schema or Encoding describes, whose type is absent
// (OpenAPI 3.1.2 section 4.8.15.1.1).
var untyped = &field{param: param{Param: &Param{ContentType: "application/octet-stream"}},
	types: []string{octetStream.full}, parsed: []parsedMedia{octetStream}, class: otherClass}

// noFields is the encoding of an object no schema describes.
var noFields = &formEncoding{}

func (e *formEncoding) field(name string) *field {
	if f := e.fields[name]; f != nil {
		return f
	}
	return untyped
}

// encodingOf compiles the fields of the object schema s, whose Source is
// src, with the Encoding Objects of encodings, at esrc, and describes them:
// the properties s lists, then the names only encodings has.
func (d *document) encodingOf(s value, src string, encodings value, esrc string, multipart bool) (*formEncoding, []*Param) {
	enc := &formEncoding{fields: map[string]*field{}}
	var list []*Param
	s, src = d.object(s, src)
	for name, ps := range s.get("properties").members() {
		f := d.newField(name, d.schema(ps, src, "/properties/"+token(name)), encodings.get(name), esrc+"/"+token(name), multipart)
		enc.fields[name], list = f, append(list, f.Param)
	}
	for name, e := range encodings.members() {
		if enc.fields[name] == nil {
			f := d.newField(name, nil, e, esrc+"/"+token(name), multipart)
			enc.fields[name], list = f, append(list, f.Param)
		}
	}
	return enc, list
}

// nested returns the fields of a multipart part f writes from an object: in
// OpenAPI 3.1 its schema's properties, each of its default type, compiled
// once per schema.
func (d *document) nested(f *field) *formEncoding {
	if !f.schema.ok() {
		return noFields
	}
	return d.encodings.get(f.schema.i, func() *formEncoding {
		enc, _ := d.encodingOf(f.schema, "", value{}, "", true)
		return enc
	})
}

// object returns the schema s, whose Source is src, or the schema its $ref
// leads to when it lists no properties of its own.
func (d *document) object(s value, src string) (value, string) {
	if ref, _, _ := reference(s); ref.ok() && !s.get("properties").ok() {
		if t, at, _, err := d.follow(s, src); err == nil {
			return t, at
		}
	}
	return s, src
}

// newField compiles the field name, whose schema is s and Encoding Object e,
// at src.
func (d *document) newField(name string, s *Schema, e value, src string, multipart bool) *field {
	f := &field{param: param{Param: &Param{Name: name, Schema: s}}}
	p := f.Param
	if s != nil {
		f.schema, p.Source = s.v, s.Source()
	}
	if e.ok() {
		p.Source = src
	}
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
	if style.ok() || explode.ok() || reserved.ok() { // RFC 6570, contentType ignored (OpenAPI 3.1.2 section 4.8.15.1.2)
		p.Style, p.ExplodeSet, p.AllowReserved = cmp.Or(style.string(), "form"), explode.ok(), reserved.kind() == 't'
		p.Explode = explode.kind() == 't' || !explode.ok() && p.Style == "form" || p.Style == "deepObject"
		switch {
		case !styleAllowed("query", p.Style):
			p.Err = fmt.Errorf("style %q is not allowed for a field", p.Style)
		case p.Explode && (p.Style == "spaceDelimited" || p.Style == "pipeDelimited"):
			p.Err = fmt.Errorf("OpenAPI does not define the %s style with explode true", p.Style)
		}
		f.style, f.set, f.name = cmp.Or(styles[p.Style], &noStyle), unreservedSet, escape(name, unreservedSet)
		if p.AllowReserved {
			f.set = reservedSet
		}
		return f
	}
	f.style, f.listed = &noStyle, ctype.ok()
	if p.ContentType = ctype.string(); !f.listed {
		p.ContentType = d.fieldType(f.schema)
	}
	for t := range strings.SplitSeq(p.ContentType, ",") {
		t = strings.Trim(t, " \t")
		m, ok := parseMedia(t)
		if !ok {
			p.Err = fmt.Errorf("contentType %q is not a list of media types or ranges", p.ContentType)
			break
		}
		f.types, f.parsed, f.class = append(f.types, t), append(f.parsed, m), m.class()
	}
	return f
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

// fieldType returns the default media type of a field whose schema is s
// (OpenAPI 3.1.2 section 4.8.15.1.1): application/octet-stream with no type
// or for a string with a contentEncoding, text/plain for any other string, a
// number, integer or boolean, application/json for an object, and for an
// array its items' type, following references. Several types, null aside,
// list each one's. It is computed once per schema; a cycle of references or
// items has no type.
func (d *document) fieldType(s value) string {
	d.typesMu.Lock()
	defer d.typesMu.Unlock()
	return d.defaultType(s)
}

func (d *document) defaultType(s value) string {
	if !s.ok() {
		return octetStream.full
	}
	if t, ok := d.types[s.i]; ok {
		return cmp.Or(t, octetStream.full) // "" while computed: a cycle
	}
	if d.types == nil {
		d.types = map[int32]string{}
	}
	d.types[s.i] = ""
	t := s.get("type")
	names := t.strs()
	if t.kind() == '"' {
		names = []string{t.text()}
	}
	var types []string
	if ref, _, _ := reference(s); !t.ok() && ref.ok() {
		if target, _, _, err := d.follow(s, ""); err == nil {
			types = append(types, d.defaultType(target))
		}
	}
	for _, n := range names {
		ct := "text/plain"
		switch {
		case n == "string" && s.get("contentEncoding").ok():
			ct = octetStream.full
		case n == "object":
			ct = "application/json"
		case n == "array":
			ct = d.defaultType(s.get("items"))
		case n != "string" && n != "number" && n != "integer" && n != "boolean":
			continue
		}
		if !slices.Contains(types, ct) {
			types = append(types, ct)
		}
	}
	ct := strings.Join(types, ", ")
	if ct == "" {
		ct = octetStream.full
	}
	d.types[s.i] = ct
	return ct
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

// members calls f with each member of v, an object, as encoding/json writes
// them: a map's in the order of their keys, a struct's fields, and those
// of the object a MarshalJSON writes. It reports false when v is no object.
func (d *document) members(v any, f func(name string, v any)) bool {
	if m, ok := v.(map[string]any); ok {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			f(k, m[k])
		}
		return m != nil
	}
	rv := reflect.ValueOf(v)
	for rv.IsValid() {
		if w := d.walkOf(rv.Type(), nil); w.json || w.text || rv.CanAddr() && (w.ptrJSON || w.ptrText) {
			return jsonMembers(rv.Interface(), f)
		}
		switch rv.Kind() {
		case reflect.Pointer, reflect.Interface:
			if rv.IsNil() {
				return false
			}
			rv = rv.Elem()
			continue
		case reflect.Map:
			if rv.IsNil() {
				return false
			}
			type member struct {
				name string
				v    reflect.Value
			}
			ms := make([]member, 0, rv.Len())
			for it := rv.MapRange(); it.Next(); {
				ms = append(ms, member{mapKey(it.Key()), it.Value()})
			}
			slices.SortFunc(ms, func(a, b member) int { return strings.Compare(a.name, b.name) })
			for _, m := range ms {
				f(m.name, m.v.Interface())
			}
			return true
		case reflect.Struct:
			for _, jf := range d.walkOf(rv.Type(), nil).fields {
				fv, err := rv.FieldByIndexErr(jf.index)
				if err != nil || !fv.CanInterface() || jf.omitZero && omitsZero(fv) || jf.omitEmpty && omitsEmpty(fv) {
					continue // json writes no such field
				}
				x := fv.Interface()
				if jf.quoted && !(fv.Kind() == reflect.Pointer && fv.IsNil()) {
					s, _ := marshal(x)
					x = s // a JSON string holding the scalar's JSON
				}
				f(jf.name, x)
			}
			return true
		}
		break
	}
	return false
}

// jsonMembers calls f with the members of the object v's JSON is, each as
// its JSON text.
func jsonMembers(v any, f func(name string, v any)) bool {
	s, err := marshal(v)
	if err != nil || s[0] != '{' {
		return false
	}
	r := &jsonReader{s: s}
	r.each(func(name string) error {
		start := r.i
		r.skip()
		f(name, json.RawMessage(r.s[start:r.i]))
		return nil
	})
	return true
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
	case nil, string, int, bool, float64, json.Number, []byte, Part, io.Reader:
	case []any:
		for i, v := range x {
			if !null(v) {
				f(v, item(i))
			}
		}
		return
	case []string:
		for i, v := range x {
			f(v, item(i))
		}
		return
	default:
		rv := reflect.ValueOf(v)
		for rv.Kind() == reflect.Pointer && !rv.IsNil() && !d.walkOf(rv.Type(), nil).json {
			rv = rv.Elem()
		}
		switch w := d.walkOf(rv.Type(), nil); {
		case w.json:
			if s, err := marshal(v); err == nil && s[0] == '[' {
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
		case w.text || rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array || rv.Kind() == reflect.Slice && bytesKind(rv.Type()):
		default:
			for i := range rv.Len() {
				if v := rv.Index(i); !nullValue(v) {
					f(v.Interface(), item(i))
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

// null reports whether encoding/json writes v as null by its Go value.
func null(v any) bool { return v == nil || nullValue(reflect.ValueOf(v)) }

func nullValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// part returns the content of v, a field's value, its Part, if it is one,
// and whether to send it, recording at at why the Part cannot be used; form
// refuses what applies only to parts.
func part(v any, at key, form bool, re *RequestError) (any, *Part, bool) {
	if _, ok := v.(Part); !ok {
		return v, nil, true
	}
	pt := v.(Part)
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
	return pt.Content, &pt, !null(pt.Content)
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
