package openapi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// isForm and isMultipart report the types whose bodies are fields.
func isForm(m parsedMedia) bool {
	return strings.EqualFold(m.full, "application/x-www-form-urlencoded")
}
func isMultipart(m parsedMedia) bool { return strings.EqualFold(m.typ, "multipart") }

// appendContent appends v, encoded as content of media type m, of codec
// class k, to dst: by the caller's codec for m, as encoding/json writes it
// for a JSON type, and otherwise as text, a string as its bytes and, for a
// text type, a number or boolean in its JSON spelling. Every value it
// encodes itself is walked first, for a reader or Part and for its depth.
// It returns the JSON Pointer, from v, of what it refuses, and reports a
// value whose JSON data is null, which a field leaves out and only a JSON
// type encodes.
func (c *Client) appendContent(dst []byte, m parsedMedia, k class, v any) (b []byte, at string, null bool, err error) {
	if codec, _ := c.cfg.codec(m); codec != nil {
		var buf bytes.Buffer
		err = codec.Encode(&buf, bare(v))
		return append(dst, buf.Bytes()...), "", false, err
	}
	switch x := v.(type) {
	case string:
		if k != jsonClass {
			return append(dst, x...), "", false, nil // its bytes, as given
		}
	case int:
		if k == textClass {
			return strconv.AppendInt(dst, int64(x), 10), "", false, nil
		}
	case bool:
		if k == textClass {
			return strconv.AppendBool(dst, x), "", false, nil
		}
	}
	switch {
	case k == jsonClass:
		if b, at, err = encodeJSON(c.doc, v, marshalJSON); dst != nil {
			b = append(dst, b...)
		}
		return b, at, string(b[len(dst):]) == "null", err
	case k == sequentialClass || isMultipart(m) || isForm(m):
		return dst, "", false, fmt.Errorf("%s cannot encode this value", m.full)
	}
	s, at, err := encodeJSON(c.doc, v, marshal)
	switch {
	case err != nil:
		return dst, at, false, err
	case s[0] == '"':
		return append(dst, jsonString(s)...), "", false, nil
	case k == textClass && s[0] != '{' && s[0] != '[' && s[0] != 'n':
		return append(dst, s...), "", false, nil
	case k == textClass:
		return dst, "", s == "null", fmt.Errorf("%s takes a string, number or boolean", m.full)
	}
	return dst, "", s == "null", fmt.Errorf("%s takes a string", m.full)
}

// A builder assembles a body: bytes, and between them the sources that read
// a caller's reader or bytes.
type builder struct {
	buf   []byte
	parts []source
}

// A source is one source of a body of several, and how it is read: as it
// is, encoded as a form field is, or checked for the delimiters of a
// multipart body or the separators of a sequence, which end the body when
// found.
type source struct {
	payload
	form      bool
	check     [][]byte // the delimiters or separators its content cannot hold
	delimiter bool     // check holds delimiters, which content after a line break may not begin with either
}

// flush makes the bytes so far a source.
func (b *builder) flush() {
	if len(b.buf) > 0 {
		b.parts = append(b.parts, source{payload: payload{data: b.buf, size: int64(len(b.buf))}})
		b.buf = b.buf[len(b.buf):]
	}
}

// add adds the source p after the bytes so far.
func (b *builder) add(p source) {
	b.flush()
	b.parts = append(b.parts, p)
}

// Write appends p to the body, as an Encoder writes an item.
func (b *builder) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// payload returns the body: its bytes alone, or its sources, which can be
// sent again, with a length, unless one is read once.
func (b *builder) payload() payload {
	if b.parts == nil {
		return payload{data: b.buf, size: int64(len(b.buf))}
	}
	b.flush()
	p := payload{parts: b.parts}
	for _, part := range p.parts {
		if part.size < 0 {
			p.size = -1
			break
		}
		p.size += part.size
	}
	return p
}

// A cursor reads the sources of a body in order.
type cursor struct {
	parts []source
	pos   int64  // read of parts[0]
	tail  []byte // the end of what parts[0] has given, for its check
	pend  []byte // what a form source has encoded, not yet read
	enc   []byte // pend's array, kept
	raw   []byte // what a form source has given, to be encoded
}

func newCursor(parts []source) *cursor {
	c := &cursor{parts: parts}
	c.begin()
	return c
}

// begin starts parts[0]: content after a delimiter line follows a line
// break.
func (c *cursor) begin() {
	if c.tail = c.tail[:0]; len(c.parts) > 0 && c.parts[0].delimiter {
		c.tail = append(c.tail, '\n')
	}
}

func (c *cursor) Close() error { return nil }

func (c *cursor) Read(buf []byte) (int, error) {
	for {
		if len(c.pend) > 0 {
			n := copy(buf, c.pend)
			c.pend = c.pend[n:]
			return n, nil
		}
		if len(c.parts) == 0 {
			return 0, io.EOF
		}
		p, dst := &c.parts[0], buf
		if p.form {
			if c.raw == nil {
				c.raw = make([]byte, 4<<10)
			}
			dst = c.raw
		}
		n, err := p.readAt(dst, c.pos)
		if c.pos += int64(n); err == io.EOF && p.size >= 0 && c.pos < p.size {
			err = io.ErrUnexpectedEOF
		}
		if n > 0 && p.check != nil && c.holds(p.check, dst[:n]) {
			if p.delimiter {
				return 0, errDelimiter
			}
			return 0, errSeparator
		}
		next := err == io.EOF || p.size >= 0 && c.pos >= p.size
		if next {
			c.parts, c.pos, err = c.parts[1:], 0, nil
			c.begin()
		}
		switch {
		case p.form:
			c.enc = appendForm(c.enc[:0], dst[:n])
			c.pend = c.enc
		case n > 0 || err != nil || !next:
			return n, err
		}
		if err != nil {
			return 0, err
		}
	}
}

// holds reports whether data, read after the tail, holds one of patterns,
// searching the seam where the tail meets data and data itself, and keeps
// as the tail the end of what has been read, the longest pattern but one
// byte.
func (c *cursor) holds(patterns [][]byte, data []byte) bool {
	keep := 0
	for _, s := range patterns {
		keep = max(keep, len(s)-1)
	}
	seam := len(c.tail)
	c.tail = append(c.tail, data[:min(len(data), keep)]...)
	found := false
	for _, s := range patterns {
		found = found || bytes.Contains(c.tail, s) || bytes.Contains(data, s)
	}
	if len(data) >= keep {
		c.tail = append(c.tail[:0], data[len(data)-keep:]...)
	} else {
		c.tail = append(c.tail[:0], c.tail[max(0, seam+len(data)-keep):]...)
	}
	return found
}

// formBody encodes the object v as an application/x-www-form-urlencoded body
// by the fields of enc, into b, recording problems in re at keys after body.
// A reader that can be read again is read now, so the body is encoded once;
// one read once is encoded as the body is read, unless readers are refused,
// as in content.
func (c *Client) formBody(b *builder, enc *formEncoding, v any, body string, readers bool, re *RequestError) {
	var scratch []byte // a field's content, before it is encoded
	if b.buf == nil {
		b.buf = make([]byte, 0, 128)
	}
	plain := len(c.cfg.codecs) == 0
	err := c.membersChecked(v, enc, body, re, func(name string, f *field, v any) {
		if f.legacy.ok() {
			at := key{body, name, -1}
			if f.legacy.str("type") == "file" {
				re.input(at.String(), errors.New("a Swagger file requires multipart/form-data"))
				return
			}
			var out strings.Builder
			lead := ""
			if len(b.buf) > 0 || b.parts != nil {
				lead = "&"
			}
			if _, err := c.writeLegacy(&out, lead, &f.param, v, true); err != nil {
				re.input(at.String(), err)
			}
			b.buf = append(b.buf, out.String()...)
			return
		}
		if plain && f.plain && b.scalar(f, name, v) {
			return
		}
		at := key{body, name, -1}
		if f.styled {
			lead := ""
			if len(b.buf) > 0 || b.parts != nil {
				lead = "&"
			}
			s, err := c.styled(&f.param, v, lead)
			if err != nil {
				re.input(at.String(), err)
			}
			b.buf = append(b.buf, s...)
			return
		}
		c.doc.values(v, at, func(v any, at key) {
			fv, ok := f.value(v, name, at, true, re)
			if !ok {
				return
			}
			lead, inner := len(b.buf), ""
			if lead > 0 || b.parts != nil {
				b.buf = append(b.buf, '&')
			}
			b.buf = append(appendForm(b.buf, name), '=')
			var err error
			switch x := fv.v.(type) {
			case []byte:
				b.buf = appendForm(b.buf, x)
			case io.Reader:
				switch p := readerPayload(x); {
				case !readers:
					err = errFormReader
				case p.size < 0:
					b.add(source{payload: p, form: true})
				default:
					b.buf, err = p.appendForm(b.buf)
				}
			default:
				if isForm(fv.m) {
					var s string
					s, err = c.formContent(c.doc.nested(f, at.item >= 0), x)
					b.buf = appendForm(b.buf, s)
					break
				}
				if s, ok := x.(string); ok && fv.k != jsonClass {
					if codec, _ := c.cfg.codec(fv.m); codec == nil {
						b.buf = appendForm(b.buf, s) // its bytes, as appendContent writes them, without a copy
						break
					}
				}
				var null bool
				if scratch, inner, null, err = c.appendContent(scratch[:0], fv.m, fv.k, x); null {
					b.buf = b.buf[:lead] // left out
					return
				}
				b.buf = appendForm(b.buf, scratch)
			}
			if err != nil {
				re.input(at.String()+inner, err)
			}
		})
	})
	if err != nil {
		re.input(body, err)
	}
}

// scalar writes v, a value of exactly string, int or bool of the plain field
// f named name, as formBody does, reporting false, having written nothing,
// for any other value, and one its type does not write as text.
func (b *builder) scalar(f *field, name string, v any) bool {
	switch v.(type) {
	case string:
		if f.class == jsonClass {
			return false
		}
	case int, bool:
		if f.class != textClass {
			return false
		}
	default:
		return false
	}
	if len(b.buf) > 0 || b.parts != nil {
		b.buf = append(b.buf, '&')
	}
	b.buf = append(appendForm(b.buf, name), '=')
	switch x := v.(type) {
	case string:
		b.buf = appendForm(b.buf, x)
	case int:
		b.buf = strconv.AppendInt(b.buf, int64(x), 10)
	case bool:
		b.buf = strconv.AppendBool(b.buf, x)
	}
	return true
}

var errFormReader = errors.New("form content inside a field or parameter cannot hold a reader")

// appendForm appends the content of p, a reader that can be read again, as
// appendForm writes it, read now with ReadAt in chunks, so that it is
// neither drained nor held whole.
func (p *payload) appendForm(b []byte) ([]byte, error) {
	if p.ra == nil {
		return appendForm(b, p.data[:p.size]), nil
	}
	b = slices.Grow(b, int(p.size)) // its encoded length at least
	chunk := make([]byte, min(p.size, 32<<10))
	for pos := int64(0); pos < p.size; {
		n, err := p.ra.ReadAt(chunk[:min(int64(len(chunk)), p.size-pos)], p.off+pos)
		if b, pos = appendForm(b, chunk[:n]), pos+int64(n); err == io.EOF && pos < p.size {
			err = io.ErrUnexpectedEOF // shorter than its size, as when it is a file cut short
		}
		if err != nil && err != io.EOF {
			return b, fmt.Errorf("reading the field's reader: %w", err)
		}
	}
	return b, nil
}

// styled returns v written in the RFC 6570 style of p, as a query
// parameter's value is, after lead, with no bound on its length.
func (c *Client) styled(p *param, v any, lead string) (string, error) {
	var s strings.Builder
	e := emitter{param: p, b: &s, lead: lead}
	_, err := e.write(c.doc, v)
	return s.String(), err
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
	b = slices.Grow(b, len(s))
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
// field or parameter, whose fields enc describes.
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
	boundary, given, err := m.boundary()
	switch {
	case err != nil:
		re.setting("Input.MediaType", err)
		return payload{}, typ
	case !given:
		boundary = newBoundary(nil)
		typ += "; boundary=" + boundary
	}
	var b builder
	w := partWriter{c: c, b: &b, re: new(RequestError), styles: stylesApply(m)} // its own: the writers' recursion takes it to the heap
	if enc != nil && enc.ordered {
		if seq := iterator(v); seq != nil && !strings.EqualFold(m.full, "multipart/form-data") {
			w.boundary, w.own, w.positional = boundary, nil, true
			if given {
				w.own = delimiters(boundary)
			}
			iw := &itemWriter{c: c, multipart: &w, fields: enc}
			w.b = &iw.b
			return payload{once: &items{w: iw, seq: seq}, size: -1}, typ
		}
	}
	w.parts(enc, v, "Input.Body", boundary, given)
	for k, err := range w.re.Settings {
		re.setting(k, err)
	}
	for k, err := range w.re.Inputs {
		re.input(k, err)
	}
	return b.payload(), typ
}

// A partWriter writes the parts of one multipart body, or of one nested in
// a part of another.
type partWriter struct {
	c          *Client
	b          *builder
	re         *RequestError
	boundary   string
	styles     bool     // RFC 6570 fields apply, as under multipart/form-data
	written    bool     // a part, after which a delimiter begins with CRLF
	outer      [][]byte // the delimiters of the body around, which its parts and delimiters cannot hold
	own        [][]byte // and those the parts cannot hold: outer, and a given boundary's
	positional bool
	nested     bool // the body is a part's, whose parts cannot be multipart
}

// parts writes the object v as parts with boundary, given or generated, by
// the fields of enc, their keys after body, then the close delimiter.
func (w *partWriter) parts(enc *formEncoding, v any, body, boundary string, given bool) {
	w.boundary, w.own = boundary, w.outer
	if given {
		w.own = append(slices.Clip(w.outer), delimiters(boundary)...)
	}
	if enc.ordered {
		if rv := reflect.ValueOf(v); rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
			w.positional = !w.styles
			for i := range rv.Len() {
				w.position(enc, rv.Index(i).Interface(), body, i)
			}
			if !w.delimiter("--") {
				w.re.input(body, errDelimiter)
			}
			return
		}
	}
	err := w.c.membersChecked(v, enc, body, w.re, func(name string, f *field, v any) {
		at := key{body, name, -1}
		if f.legacy.ok() && f.CollectionFormat != "" && f.CollectionFormat != "multi" {
			s, _, err := encodeJSON(w.c.doc, v, marshal)
			if err == nil {
				var xs []string
				xs, err = legacyValues(&jsonReader{s: s}, f.legacy, nil)
				for _, x := range xs {
					w.write(textField, name, x, at)
				}
			}
			if err != nil {
				w.re.input(at.String(), err)
			}
			return
		}
		if !f.styled || !w.styles {
			w.c.doc.values(v, at, func(v any, at key) { w.write(f, name, v, at) })
			return
		}
		// RFC 6570 names and values, without URI percent-encoding (OpenAPI
		// 3.1.2 Appendix C), each a text/plain part (RFC 7578 section 4.4).
		p := f.param
		p.set = unreservedSet
		s, err := w.c.styled(&p, v, "")
		if err != nil {
			w.re.input(at.String(), err)
			return
		}
		for pair := range strings.SplitSeq(s, "&") {
			if pair != "" {
				n, v, _ := strings.Cut(pair, "=")
				n, _ = url.PathUnescape(n)
				v, _ = url.PathUnescape(v)
				w.write(textField, n, v, at)
			}
		}
	})
	if err != nil {
		w.re.input(body, err)
	}
	if !w.delimiter("--") {
		w.re.input(body, errDelimiter)
	}
}

// delimiter writes a delimiter line, or with end "--" the close delimiter,
// reporting whether it holds none of the delimiters around.
func (w *partWriter) delimiter(end string) bool {
	start := len(w.b.buf)
	if w.written {
		w.b.buf = append(w.b.buf, "\r\n"...)
	}
	w.b.buf = append(append(append(append(w.b.buf, "--"...), w.boundary...), end...), "\r\n"...)
	w.written = true
	return !holds(w.b.buf[start:], w.outer)
}

// textField is the field of a part RFC 6570 writes.
var textField = &field{param: param{Param: &Param{ContentType: "text/plain"}, style: &noStyle}, types: []string{"text/plain"},
	parsed: []parsedMedia{defaultMedia[2].parsed}, class: textClass}

// write writes v, a value of f named name, as a part, at at: its header,
// then its content, as given, encoded by its media type, or, one level
// deep, as a multipart body of its own.
func (w *partWriter) write(f *field, name string, v any, at key) {
	c, b, re := w.c, w.b, w.re
	fv, ok := f.value(v, name, at, false, re)
	if !ok {
		return
	}
	var content []byte // written as it is, unless v is a reader or a nested body
	r, raw := fv.v.(io.Reader)
	mt, multipart := fv.mt, isMultipart(fv.m)
	boundary, given, berr := fv.m.boundary()
	switch x := fv.v.(type) {
	case []byte:
		content, raw = x, true
	case io.Reader:
	default:
		var err error
		inner := ""
		switch {
		case multipart:
		case isForm(fv.m):
			var s string
			if s, err = c.formContent(c.doc.nested(f, at.item >= 0), x); err == nil {
				content = []byte(s)
			}
		default:
			var null bool
			if content, inner, null, err = c.appendContent(nil, fv.m, fv.k, x); null {
				return
			}
		}
		if err != nil {
			re.input(at.String()+inner, err)
			return
		}
	}
	switch {
	case !multipart:
	case berr != nil:
		re.setting(at.String(), berr)
		return
	case raw && !given:
		re.setting(at.String(), errors.New("pre-encoded multipart content needs its boundary in Part.MediaType"))
		return
	case raw:
	case w.nested:
		re.input(at.String(), errors.New("a multipart part nested one level cannot hold another"))
		return
	case !given:
		boundary = newBoundary(w.own)
		mt += "; boundary=" + boundary
	}

	if len(b.buf) >= 64<<10 { // a body of many parts is kept in pieces, never copied whole as it grows
		b.flush()
	}
	ok = w.delimiter("")
	start := len(b.buf)
	var fields []headerField // the Part's, sorted by their canonical names
	disposition := false     // one replaces the client's Content-Disposition
	if fv.pt != nil {
		for k, vs := range fv.pt.Header {
			ck := textproto.CanonicalMIMEHeaderKey(k)
			fields, disposition = append(fields, headerField{ck, vs}), disposition || ck == "Content-Disposition"
		}
		slices.SortFunc(fields, func(a, b headerField) int { return strings.Compare(a.name, b.name) })
	}
	if !disposition && !w.positional {
		b.buf = appendQuoted(append(b.buf, "Content-Disposition: form-data; name="...), name)
		switch {
		case fv.pt != nil && fv.pt.Filename != "":
			b.buf = appendQuoted(append(b.buf, "; filename="...), fv.pt.Filename)
		case raw && !multipart && (fv.pt == nil || !fv.pt.NoFilename):
			b.buf = appendQuoted(append(b.buf, "; filename="...), name)
		}
		b.buf = append(b.buf, "\r\n"...)
	}
	b.buf = append(append(append(b.buf, "Content-Type: "...), mt...), "\r\n"...)
	for _, h := range fields {
		for _, v := range h.values {
			b.buf = append(append(append(append(b.buf, h.name...), ": "...), v...), "\r\n"...)
		}
	}
	b.buf = append(b.buf, "\r\n"...)
	switch {
	case !ok || holds(b.buf[start:], w.own) || holds(content, w.own):
		re.input(at.String(), errDelimiter)
	case r != nil:
		b.add(source{payload: readerPayload(r), check: w.own, delimiter: true})
	case multipart && !raw:
		nw := partWriter{c: c, b: b, re: re, outer: w.own, nested: true}
		nw.parts(c.doc.nested(f, at.item >= 0), fv.v, at.String(), boundary, given)
	case raw && len(content) > 0:
		b.add(source{payload: payload{data: content, size: int64(len(content))}}) // not copied
	default:
		b.buf = append(b.buf, content...)
	}
}

// A headerField is a header field of a part.
type headerField struct {
	name   string
	values []string
}

var (
	errDelimiter = errors.New("the content holds the multipart body's boundary delimiter (RFC 2046 section 5.1.1)")
	errBoundary  = errors.New("the boundary is not one RFC 2046 section 5.1.1 allows")
	errBoundary2 = errors.New("the media type gives two boundary parameters")
)

// delimiters returns the delimiters of boundary as content cannot hold them:
// "--" and the boundary after a CR or an LF, as receivers in wide use split
// parts at either, and so at the start of a part's content too.
func delimiters(boundary string) [][]byte {
	return [][]byte{[]byte("\r--" + boundary), []byte("\n--" + boundary)}
}

// holds reports whether data, at the start of a line, holds one of the
// delimiters.
func holds(data []byte, delimiters [][]byte) bool {
	for _, d := range delimiters {
		if bytes.HasPrefix(data, d[1:]) || bytes.Contains(data, d) {
			return true
		}
	}
	return false
}

// newBoundary returns a random boundary whose delimiter line holds none of
// the delimiters given, which are those of valid boundaries.
func newBoundary(delimiters [][]byte) string {
	for {
		var r [16]byte
		rand.Read(r[:])
		b := hex.EncodeToString(r[:])
		if len(delimiters) == 0 || !holds([]byte("\r\n--"+b), delimiters) {
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
