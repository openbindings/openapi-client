package openapi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/textproto"
	"net/url"
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
// text type, a number or boolean in its JSON spelling. It returns the JSON
// Pointer, from v, of what it refuses, and reports a value whose JSON data
// is null, which a field leaves out and only a JSON type encodes.
func (c *Client) appendContent(dst []byte, m parsedMedia, k class, v any) (b []byte, at string, null bool, err error) {
	if codec, _ := c.cfg.codec(m); codec != nil {
		var buf bytes.Buffer
		err = codec.Encode(&buf, v)
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
	s, err := marshal(v)
	switch {
	case err != nil:
		return dst, "", false, err
	case s[0] == '"':
		return append(dst, jsonString(s)...), "", false, nil
	case k == textClass && s[0] != '{' && s[0] != '[' && s[0] != 'n':
		return append(dst, s...), "", false, nil
	case k == textClass:
		return dst, "", s == "null", fmt.Errorf("%s takes a string, number or boolean", m.full)
	}
	return dst, "", s == "null", fmt.Errorf("%s takes a string", m.full)
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

// formBody encodes the object v as an application/x-www-form-urlencoded body
// by the fields of enc, into b, recording problems in re at keys after body.
// A reader that can be read again is read now, so the body is encoded once;
// one read once is encoded as the body is read, unless readers are refused,
// as in a parameter.
func (c *Client) formBody(b *builder, enc *formEncoding, v any, body string, readers bool, re *RequestError) {
	var scratch []byte // a field's content, before it is encoded
	if b.buf == nil {
		b.buf = make([]byte, 0, 128)
	}
	ok := c.doc.members(v, func(name string, v any) {
		at, f := key{body, name, -1}, enc.field(name)
		if f.Style != "" {
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
			v, pt, ok := part(v, at, true, re)
			if !ok {
				return
			}
			_, m, k, err := f.media(pt.mediaType())
			switch {
			case f.Err != nil:
				re.input(at.String(), f.Err)
				return
			case err != nil:
				re.setting(at.String(), err)
				return
			}
			lead, inner := len(b.buf), ""
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
				var null bool
				if scratch, inner, null, err = c.appendContent(scratch[:0], m, k, v); null {
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
	if !ok {
		re.input(body, errors.New("the body is an object, a map or a struct, whose properties are its fields"))
	}
}

// bytes returns the content of p, a reader that can be read again, read
// now with ReadAt, so that it is not drained.
func (p *payload) bytes() ([]byte, error) {
	if p.ra == nil {
		return p.data[:p.size], nil
	}
	return io.ReadAll(io.NewSectionReader(p.ra, p.off, p.size))
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
	w := partWriter{c: c, b: &b, re: new(RequestError)} // its own: the writers' recursion takes it to the heap
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
// the fields of enc, their keys after body, then the close delimiter.
func (w *partWriter) parts(enc *formEncoding, v any, body, boundary string, given bool) {
	w.boundary, w.own = boundary, w.outer
	if given {
		w.own = append(slices.Clip(w.outer), "\r\n--"+boundary)
	}
	ok := w.c.doc.members(v, func(name string, v any) {
		at, f := key{body, name, -1}, enc.field(name)
		if f.Style == "" {
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
	if !ok {
		w.re.input(body, errors.New("the body is an object, a map or a struct, whose properties are its parts"))
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
var textField = &field{param: param{Param: &Param{ContentType: "text/plain"}}, types: []string{"text/plain"},
	parsed: []parsedMedia{{"text/plain", "text", "plain", ""}}, class: textClass}

// write writes v, a value of f named name, as a part, at at: its header,
// then its content, as given, encoded by its media type, or, one level
// deep, as a multipart body of its own.
func (w *partWriter) write(f *field, name string, v any, at key) {
	c, b, re := w.c, w.b, w.re
	v, pt, ok := part(v, at, false, re)
	if !ok {
		return
	}
	mt, m, k, err := f.media(pt.mediaType())
	switch {
	case !quotable(name):
		re.input(at.String(), errors.New("a part name cannot hold a control character other than a tab"))
		return
	case f.Err != nil:
		re.input(at.String(), f.Err)
		return
	case err != nil:
		re.setting(at.String(), err)
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
		if content, inner, null, err = c.appendContent(nil, m, k, v); null || err != nil {
			if !null {
				re.input(at.String()+inner, err)
			}
			return
		}
	}
	switch {
	case raw && multipart && !given:
		re.setting(at.String(), errors.New("pre-encoded multipart content needs its boundary in Part.MediaType"))
		return
	case !multipart || raw:
	case w.nested:
		re.input(at.String(), errors.New("a multipart part nested one level cannot hold another"))
		return
	case !given:
		boundary = newBoundary(w.own)
		mt += "; boundary=" + boundary
	case !validBoundary(boundary):
		re.setting(at.String(), errBoundary)
		return
	}

	ok = w.delimiter("")
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
	case !ok || holds(b.buf[start:], w.own) || holds(content, w.own):
		re.input(at.String(), errDelimiter)
	case r != nil:
		p := readerPayload(r)
		p.check = w.own
		b.add(p)
	case multipart && !raw:
		nw := partWriter{c: c, b: b, re: re, outer: w.own, nested: true}
		nw.parts(c.doc.nested(f), v, at.String(), boundary, given)
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
