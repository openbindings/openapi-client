package openapi

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/textproto"
	"net/url"
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
	listed bool          // its Encoding lists them, so a Part's MediaType must match one
	schema value         // its schema, whose properties a nested multipart part's fields are
}

// untyped is a field no schema or Encoding describes, whose type is absent
// (OpenAPI 3.1.2 section 4.8.15.1.1).
var untyped = &field{param: param{Param: &Param{ContentType: "application/octet-stream"}},
	types: []string{octetStream.full}, parsed: []parsedMedia{octetStream}}

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
		p.ContentType = d.fieldType(f.schema, 0)
	}
	for t := range strings.SplitSeq(p.ContentType, ",") {
		t = strings.Trim(t, " \t")
		m, ok := parseMedia(t)
		if !ok {
			p.Err = fmt.Errorf("contentType %q is not a list of media types or ranges", p.ContentType)
			break
		}
		f.types, f.parsed = append(f.types, t), append(f.parsed, m)
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
// array its items' type. Several types, null aside, list each one's.
func (d *document) fieldType(s value, depth int) string {
	for ; depth < maxDepth && !s.get("type").ok(); depth++ { // a reference followed
		ref, _, _ := reference(s)
		t, _, _, err := d.follow(s, "")
		if !ref.ok() || err != nil {
			break
		}
		s = t
	}
	t := s.get("type")
	names := t.strs()
	if t.kind() == '"' {
		names = []string{t.text()}
	}
	var types []string
	for _, n := range names {
		ct := "text/plain"
		switch {
		case n == "string" && s.get("contentEncoding").ok():
			ct = octetStream.full
		case n == "object":
			ct = "application/json"
		case n == "array" && depth < maxDepth:
			ct = d.fieldType(s.get("items"), depth+1)
		case n != "string" && n != "number" && n != "integer" && n != "boolean":
			continue
		}
		if !slices.Contains(types, ct) {
			types = append(types, ct)
		}
	}
	if len(types) == 0 {
		return octetStream.full
	}
	return strings.Join(types, ", ")
}

// media returns the media type of a value of f, as written and parsed: mt,
// a Part's MediaType, when set, which must be concrete and match one of the
// types f's Encoding lists, if it lists any; else f's own, when one concrete
// type selects itself.
func (f *field) media(mt string) (string, parsedMedia, error) {
	if mt == "" {
		if len(f.parsed) == 1 && f.parsed[0].concrete() {
			return f.types[0], f.parsed[0], nil
		}
		return "", parsedMedia{}, fmt.Errorf("the field offers %s; select one with Part.MediaType", f.ContentType)
	}
	m, ok := parseMedia(mt)
	switch {
	case !ok || !m.concrete():
		return "", parsedMedia{}, errors.New("Part.MediaType is not a concrete media type")
	case f.listed && !slices.ContainsFunc(f.parsed, func(d parsedMedia) bool { _, ok := d.covers(m); return ok }):
		return "", parsedMedia{}, fmt.Errorf("the field does not offer %s", m.full)
	}
	return mt, m, nil
}

// isForm and isMultipart report the types whose bodies are fields.
func isForm(m parsedMedia) bool {
	return strings.EqualFold(m.full, "application/x-www-form-urlencoded")
}
func isMultipart(m parsedMedia) bool { return strings.EqualFold(m.typ, "multipart") }

// encodeContent encodes v as content of media type m, of codec class k: by
// the caller's codec for m, as encoding/json writes it for a JSON type, and
// otherwise as text, a string as its bytes and, for a text type, a number or
// boolean in its JSON spelling. It returns the JSON Pointer, from v, of what
// it refuses, and reports a value whose JSON data is null, which a field
// leaves out and only a JSON type encodes.
func (c *Client) encodeContent(m parsedMedia, k class, v any) (b []byte, at string, null bool, err error) {
	if codec, _ := c.cfg.codec(m); codec != nil {
		var buf bytes.Buffer
		err = codec.Encode(&buf, v)
		return buf.Bytes(), "", false, err
	}
	switch {
	case k == jsonClass:
		b, at, err = encodeJSON(c.doc, v, marshalJSON)
		return b, at, string(b) == "null", err
	case k == sequentialClass || isMultipart(m) || isForm(m):
		return nil, "", false, fmt.Errorf("%s cannot encode this value", m.full)
	}
	if s, ok := v.(string); ok {
		return []byte(s), "", false, nil // its bytes, as given
	}
	s, err := marshal(v)
	switch {
	case err != nil:
		return nil, "", false, err
	case s[0] == '"':
		return []byte(jsonString(s)), "", false, nil
	case k == textClass && s[0] != '{' && s[0] != '[' && s[0] != 'n':
		return []byte(s), "", false, nil
	case k == textClass:
		return nil, "", s == "null", fmt.Errorf("%s takes a string, number or boolean", m.full)
	}
	return nil, "", s == "null", fmt.Errorf("%s takes a string", m.full)
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

// values calls f with v, a field's value, at at, or, when encoding/json
// writes v as an array and v is no []byte, with each item, at its index
// after at. A value or item whose JSON data is null is left out.
func (d *document) values(v any, at string, f func(v any, at string)) {
	switch x := v.(type) {
	case nil, []byte, Part, io.Reader:
	case []any:
		for i, item := range x {
			if !null(item) {
				f(item, at+"/"+strconv.Itoa(i))
			}
		}
		return
	case []string:
		for i, item := range x {
			f(item, at+"/"+strconv.Itoa(i))
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
				r := &jsonReader{s: s}
				i := 0
				r.each(func(string) error {
					start := r.i
					r.skip()
					if item := r.s[start:r.i]; item != "null" {
						f(json.RawMessage(item), at+"/"+strconv.Itoa(i))
					}
					i++
					return nil
				})
				return
			}
		case w.text || rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array || rv.Kind() == reflect.Slice && bytesKind(rv.Type()):
		default:
			for i := range rv.Len() {
				if item := rv.Index(i); !nullValue(item) {
					f(item.Interface(), at+"/"+strconv.Itoa(i))
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

// A builder assembles a body: bytes, and between them the parts that read
// a caller's reader or bytes.
type builder struct {
	buf   []byte
	parts []payload
}

// add adds the part p after the bytes so far.
func (b *builder) add(p payload) {
	if len(b.buf) > 0 {
		b.parts = append(b.parts, payload{data: b.buf, size: int64(len(b.buf))})
		b.buf = b.buf[len(b.buf):]
	}
	b.parts = append(b.parts, p)
}

// payload returns the body: its bytes alone, or its parts, which can be
// sent again, with a length, unless one is read once.
func (b *builder) payload() payload {
	if b.parts == nil {
		return payload{data: b.buf, size: int64(len(b.buf))}
	}
	b.add(payload{})
	p := payload{parts: b.parts[:len(b.parts)-1]}
	for _, part := range p.parts {
		if part.size < 0 {
			p.size = -1
			break
		}
		p.size += part.size
	}
	return p
}

// part returns the content and Part of v, a field's value, recording why a
// Part cannot be used at at; form refuses what applies only to parts.
func part(v any, at string, form bool, re *RequestError) (any, *Part, bool) {
	pt, ok := v.(Part)
	if !ok {
		return v, nil, true
	}
	err := checkHeader(pt.Header)
	switch {
	case form && (pt.Filename != "" || pt.NoFilename || pt.Header != nil):
		err = errors.New("a form field takes no Filename, NoFilename or Header")
	case pt.Filename != "" && pt.NoFilename:
		err = errors.New("the Part sets both Filename and NoFilename")
	case !quotable(pt.Filename):
		err = errors.New("a filename cannot hold a control character other than a tab")
	}
	if err != nil {
		re.input(at, err)
		return nil, nil, false
	}
	return pt.Content, &pt, !null(pt.Content)
}

// quotable reports whether a quoted-string can carry s: no control
// character but a tab (RFC 9110 section 5.6.4).
func quotable(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < ' ' && r != '\t' || r == 0x7f })
}

// formBody encodes the object v as an application/x-www-form-urlencoded body
// by the fields of enc, into b, its keys after key, recording problems in
// re. A reader that can be read again is read now, so the body is encoded
// once; one read once is encoded as the body is read, unless readers are
// refused, as in a parameter.
func (c *Client) formBody(b *builder, enc *formEncoding, v any, key string, readers bool, re *RequestError) {
	ok := c.doc.members(v, func(name string, v any) {
		at, f := key+"/"+escapeToken(name), enc.field(name)
		if f.Style != "" {
			var s strings.Builder
			e := emitter{param: &f.param, b: &s}
			if len(b.buf) > 0 || b.parts != nil {
				e.lead = "&"
			}
			if _, err := e.write(c.doc, v); err != nil {
				re.input(at, err)
			}
			b.buf = append(b.buf, s.String()...)
			return
		}
		c.doc.values(v, at, func(v any, at string) {
			v, pt, ok := part(v, at, true, re)
			if !ok {
				return
			}
			_, m, err := f.media(pt.mediaType())
			switch {
			case f.Err != nil:
				re.input(at, f.Err)
				return
			case err != nil:
				re.setting(at, err)
				return
			}
			lead := len(b.buf)
			if lead > 0 || b.parts != nil {
				b.buf = append(b.buf, '&')
			}
			b.buf = append(appendForm(b.buf, name), '=')
			switch v := v.(type) {
			case []byte:
				b.buf = appendForm(b.buf, v)
			case io.Reader:
				if !readers {
					err = errors.New("a reader cannot serialize a parameter")
				} else if p := readerPayload(v); p.size < 0 {
					b.add(payload{once: v, size: -1, form: true})
				} else if data, rerr := p.bytes(); rerr != nil {
					err = fmt.Errorf("reading the field's reader: %w", rerr)
				} else {
					b.buf = appendForm(b.buf, data)
				}
			default:
				content, inner, null, cerr := c.encodeContent(m, m.class(), v)
				if null {
					b.buf = b.buf[:lead] // left out
					return
				}
				at, err = at+inner, cerr
				b.buf = appendForm(b.buf, content)
			}
			if err != nil {
				re.input(at, err)
			}
		})
	})
	if !ok {
		re.input(key, errors.New("the body is an object, a map or a struct, whose properties are its fields"))
	}
}

func (pt *Part) mediaType() string {
	if pt == nil {
		return ""
	}
	return pt.MediaType
}

// bytes returns the content of p, a reader that can be read again, read
// now with ReadAt, so that it is not drained.
func (p *payload) bytes() ([]byte, error) {
	if p.ra == nil {
		return p.data[:p.size], nil
	}
	return io.ReadAll(io.NewSectionReader(p.ra, p.off, p.size))
}

// formSet is what the WHATWG application/x-www-form-urlencoded serializer
// writes as it is: letters, digits and *-._.
var formSet = func() *charset {
	s := newCharset("*", false)
	s['~'] = 0
	return s
}()

// appendForm appends s to b as the WHATWG serializer writes it: a space as
// +, formSet as it is, every other byte as %XX.
func appendForm[T string | []byte](b []byte, s T) []byte {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case formSet[c] == 1:
			b = append(b, c)
		case c == ' ':
			b = append(b, '+')
		default:
			b = append(b, '%', upperHex[c>>4], upperHex[c&15])
		}
	}
	return b
}

// formContent encodes v as application/x-www-form-urlencoded content of a
// parameter, whose fields enc describes.
func (c *Client) formContent(enc *formEncoding, v any) (string, error) {
	var b builder
	var re RequestError
	c.formBody(&b, enc, v, "", false, &re)
	if err := re.refused(); err != nil {
		return "", errors.Join(re.Unwrap()...)
	}
	return string(b.buf), nil
}

// multipartBody encodes the object v as a multipart body of type typ, m
// parsed, by the fields of enc, returning it and its Content-Type.
func (c *Client) multipartBody(enc *formEncoding, typ string, m parsedMedia, v any, re *RequestError) (payload, string) {
	boundary, given := m.param("boundary")
	switch {
	case !given:
		boundary = newBoundary(nil)
		typ += "; boundary=" + boundary
	case !validBoundary(boundary):
		re.setting("Input.MediaType", errBoundary)
	}
	var b builder
	w := partWriter{c: c, b: &b, re: re}
	w.parts(enc, v, "Input.Body", boundary, given)
	return b.payload(), typ
}

// A partWriter writes the parts of one multipart body, or of one nested in
// a part of another.
type partWriter struct {
	c        *Client
	b        *builder
	re       *RequestError
	boundary string
	written  bool     // a part, after which a delimiter begins with CRLF
	outer    []string // the delimiters of the body around, which its parts and delimiters cannot hold
	own      []string // and those the parts cannot hold: outer, and a given boundary's
	nested   bool     // the body is a part's, whose parts cannot be multipart
}

// parts writes the object v as parts with boundary, given or generated, by
// the fields of enc, their keys after key, then the close delimiter.
func (w *partWriter) parts(enc *formEncoding, v any, key, boundary string, given bool) {
	w.boundary, w.own = boundary, w.outer
	if given {
		w.own = append(slices.Clip(w.outer), "\r\n--"+boundary)
	}
	ok := w.c.doc.members(v, func(name string, v any) {
		at, f := key+"/"+escapeToken(name), enc.field(name)
		if f.Style == "" {
			w.c.doc.values(v, at, func(v any, at string) { w.write(f, name, v, at) })
			return
		}
		// RFC 6570 names and values, without URI percent-encoding (OpenAPI
		// 3.1.2 Appendix C), each a text/plain part (RFC 7578 section 4.4).
		p := f.param
		p.set = unreservedSet
		var s strings.Builder
		e := emitter{param: &p, b: &s}
		if _, err := e.write(w.c.doc, v); err != nil {
			w.re.input(at, err)
			return
		}
		for pair := range strings.SplitSeq(s.String(), "&") {
			if pair != "" {
				n, v, _ := strings.Cut(pair, "=")
				n, _ = url.PathUnescape(n)
				v, _ = url.PathUnescape(v)
				w.write(textField, n, v, at)
			}
		}
	})
	if !ok {
		w.re.input(key, errors.New("the body is an object, a map or a struct, whose properties are its parts"))
	}
	w.delimiter(key, "--")
}

// delimiter writes a delimiter line, or with end "--" the close delimiter.
func (w *partWriter) delimiter(at, end string) {
	s := "\r\n--" + w.boundary + end + "\r\n"
	if !w.written {
		s = s[2:]
	}
	if holds([]byte(s), w.outer) {
		w.re.input(at, errDelimiter)
	}
	w.b.buf = append(w.b.buf, s...)
	w.written = true
}

// textField is the field of a part RFC 6570 writes.
var textField = &field{param: param{Param: &Param{ContentType: "text/plain"}}, types: []string{"text/plain"},
	parsed: []parsedMedia{{"text/plain", "text", "plain", ""}}}

// write writes v, a value of f named name, as a part, at at: its header,
// then its content, as given, encoded by its media type, or, one level
// deep, as a multipart body of its own.
func (w *partWriter) write(f *field, name string, v any, at string) {
	c, b, re := w.c, w.b, w.re
	v, pt, ok := part(v, at, false, re)
	if !ok {
		return
	}
	mt, m, err := f.media(pt.mediaType())
	switch {
	case !quotable(name):
		re.input(at, errors.New("a part name cannot hold a control character other than a tab"))
		return
	case f.Err != nil:
		re.input(at, f.Err)
		return
	case err != nil:
		re.setting(at, err)
		return
	}
	var content []byte // written as it is, unless v is a reader or a nested body
	r, raw := v.(io.Reader)
	multipart := isMultipart(m)
	boundary, given := m.param("boundary")
	switch x := v.(type) {
	case []byte:
		content, raw = x, true
	case io.Reader:
	default:
		if multipart {
			break
		}
		var inner string
		var null bool
		if content, inner, null, err = c.encodeContent(m, m.class(), v); null || err != nil {
			if !null {
				re.input(at+inner, err)
			}
			return
		}
	}
	switch {
	case raw && multipart && !given:
		re.setting(at, errors.New("pre-encoded multipart content needs its boundary in Part.MediaType"))
		return
	case !multipart || raw:
	case w.nested:
		re.input(at, errors.New("a multipart part nested one level cannot hold another"))
		return
	case !given:
		boundary = newBoundary(w.own)
		mt += "; boundary=" + boundary
	case !validBoundary(boundary):
		re.setting(at, errBoundary)
		return
	}

	w.delimiter(at, "")
	start := len(b.buf)
	disposition := false // a Part.Header field replaces it
	if pt != nil {
		for k := range pt.Header {
			disposition = disposition || textproto.CanonicalMIMEHeaderKey(k) == "Content-Disposition"
		}
	}
	if !disposition {
		b.buf = appendQuoted(append(b.buf, "Content-Disposition: form-data; name="...), name)
		switch {
		case pt != nil && pt.Filename != "":
			b.buf = appendQuoted(append(b.buf, "; filename="...), pt.Filename)
		case raw && !multipart && (pt == nil || !pt.NoFilename):
			b.buf = appendQuoted(append(b.buf, "; filename="...), name)
		}
		b.buf = append(b.buf, "\r\n"...)
	}
	b.buf = append(append(append(b.buf, "Content-Type: "...), mt...), "\r\n"...)
	if pt != nil {
		for _, k := range slices.Sorted(maps.Keys(pt.Header)) {
			for _, v := range pt.Header[k] {
				b.buf = append(append(append(append(b.buf, textproto.CanonicalMIMEHeaderKey(k)...), ": "...), v...), "\r\n"...)
			}
		}
	}
	b.buf = append(b.buf, "\r\n"...)
	switch {
	case holds(b.buf[start:], w.own) || content != nil && holds(content, w.own):
		re.input(at, errDelimiter)
	case r != nil:
		p := readerPayload(r)
		p.check = w.own
		b.add(p)
	case multipart && !raw:
		nw := partWriter{c: c, b: b, re: re, outer: w.own, nested: true}
		nw.parts(c.doc.nested(f), v, at, boundary, given)
	case raw && len(content) > 0:
		b.add(payload{data: content, size: int64(len(content))}) // not copied
	default:
		b.buf = append(b.buf, content...)
	}
}

var (
	errDelimiter = errors.New("the content holds the multipart body's boundary delimiter (RFC 2046 section 5.1.1)")
	errBoundary  = errors.New("the boundary is not one RFC 2046 section 5.1.1 allows")
)

// holds reports whether data, which begins a line, holds one of the
// delimiters, each CRLF "--" and a boundary: at its start, after the CRLF
// before it, or within.
func holds(data []byte, delimiters []string) bool {
	for _, d := range delimiters {
		if bytes.HasPrefix(data, []byte(d[2:])) || bytes.Contains(data, []byte(d)) {
			return true
		}
	}
	return false
}

// newBoundary returns a random boundary whose delimiter holds none of the
// delimiters given.
func newBoundary(delimiters []string) string {
	for {
		var r [16]byte
		rand.Read(r[:])
		b := hex.EncodeToString(r[:])
		if !holds([]byte("\r\n--"+b), delimiters) {
			return b
		}
	}
}

// validBoundary reports whether RFC 2046 section 5.1.1 allows b: 1 to 70
// bchars, the last not a space.
func validBoundary(b string) bool {
	for i := 0; i < len(b); i++ {
		if c := b[i]; !unreserved(c) && strings.IndexByte("'()+_,-./:=? ", c) < 0 || c == '~' {
			return false
		}
	}
	return b != "" && len(b) <= 70 && b[len(b)-1] != ' '
}

// appendQuoted appends s to b as a quoted-string, \ and " escaped.
func appendQuoted(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' || s[i] == '"' {
			b = append(b, '\\')
		}
		b = append(b, s[i])
	}
	return append(b, '"')
}
