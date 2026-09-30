package openapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A style is how a parameter value is written: an RFC 6570 operator, by
// the columns of its Appendix A, or one of OpenAPI's other query styles.
type style struct {
	first, sep, ifemp string
	delim             string // between the items of a value not exploded
	named             bool
	composite         bool     // it takes only an array or object
	deep              bool     // deepObject: it takes only an object, which may nest
	set               *charset // nil where values are written as given
}

var styles = map[string]style{
	"simple":         {sep: ",", delim: ","},
	"label":          {first: ".", sep: ".", delim: ","},
	"matrix":         {first: ";", sep: ";", delim: ",", named: true},
	"form":           {sep: "&", delim: ",", ifemp: "=", named: true},
	"spaceDelimited": {delim: "%20", ifemp: "=", named: true, composite: true},
	"pipeDelimited":  {delim: "%7C", ifemp: "=", named: true, composite: true},
	"deepObject":     {sep: "&", composite: true, deep: true},
}

var (
	errNested     = errors.New("the style cannot serialize a nested array or object")
	errComposite  = errors.New("the style takes an array or object")
	errDeepObject = errors.New("the deepObject style takes an object without arrays")
)

// writeParam writes the value v given for p into b, after lead unless it
// writes nothing, recording in re why it cannot. It reports whether v was
// given, a defined value, and whether it wrote anything. In a path, lead is
// the style's first.
func (c *Client) writeParam(b *strings.Builder, lead string, p *param, v any, re *RequestError) (given, written bool) {
	if p.ContentType == "" {
		e := emitter{param: p, b: b, lead: lead}
		given, err := e.write(c.doc, v)
		if err != nil {
			re.input(p.Key, err)
			return false, false
		}
		return given, e.n > 0
	}
	if _, raw := v.([]byte); !raw && c.cfg.codecsErr != nil {
		re.setting("Options.Codecs", c.cfg.codecsErr)
	}
	var s string
	err := p.Err
	if err == nil {
		s, err = c.encode(p, v)
	}
	if err == nil && p.In == "cookie" && strings.ContainsFunc(s, func(r rune) bool { return r == ';' || r < ' ' || r == 0x7f }) {
		err = errors.New(`a cookie value written as given cannot hold a ";" or a control character`)
	}
	if err != nil {
		re.input(p.Key, err)
		return false, false
	}
	b.WriteString(lead)
	if p.In == "query" || p.In == "cookie" {
		b.WriteString(p.name)
		b.WriteByte('=')
	}
	if p.In == "header" || p.In == "cookie" {
		b.WriteString(s) // as given
	} else {
		escapeTo(b, s, unreservedSet)
	}
	return true, true
}

// encode returns v encoded as a body of p's media type is, a []byte being
// the encoded content, or why it cannot be.
func (c *Client) encode(p *param, v any) (string, error) {
	m := p.media
	if p.class == sequentialClass || strings.EqualFold(m.typ, "multipart") {
		return "", fmt.Errorf("%s cannot serialize a parameter", m.full)
	}
	switch v := v.(type) {
	case []byte:
		return string(v), nil
	case io.Reader:
		return "", errors.New("a reader cannot serialize a parameter")
	}
	if b, _, ok, err := c.encodeValue(m, p.class, v); ok {
		return string(b), err
	}
	if strings.EqualFold(m.full, "application/x-www-form-urlencoded") {
		return "", notYet("encoding application/x-www-form-urlencoded content")
	}
	if s, ok := v.(string); ok {
		return s, nil // its bytes, as given
	}
	s, err := marshal(v)
	switch {
	case err != nil:
		return "", err
	case s[0] == '"':
		return jsonString(s), nil
	case p.class == textClass && s[0] != '{' && s[0] != '[' && s[0] != 'n':
		return s, nil
	case p.class == textClass:
		return "", fmt.Errorf("%s takes a string, number or boolean", m.full)
	}
	return "", fmt.Errorf("%s takes a string", m.full)
}

// jsonText returns s as JSON data holds it: each byte of invalid UTF-8
// replaced by U+FFFD, as encoding/json writes it.
func jsonText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for _, r := range s { // each invalid byte is a utf8.RuneError
		b.WriteRune(r)
	}
	return b.String()
}

// An emitter writes one value in its parameter's style, into b after lead
// once the value proves defined.
type emitter struct {
	*param
	b    *strings.Builder
	lead string
	n    int  // the items or members written
	held bool // the first item of a named value not exploded was "", and whether "=" follows it depends on a second
}

// write writes v, reporting whether it is defined. A value it gives
// encoding/json is checked as d checks JSON it encodes.
func (e *emitter) write(d *document, v any) (bool, error) {
	switch v := v.(type) {
	case string:
		return e.primitive(jsonText(v))
	case int:
		return e.primitive(strconv.Itoa(v))
	case bool:
		return e.primitive(strconv.FormatBool(v))
	case []string:
		if len(v) > 0 && e.deep {
			return false, errDeepObject
		}
		for _, s := range v {
			if err := e.item(jsonText(s)); err != nil {
				return false, err
			}
		}
		return e.end(len(v))
	}
	s, err := marshal(v)
	if err == nil {
		_, err = checkJSON(d, v, s)
	}
	if err != nil {
		return false, err
	}
	r := &jsonReader{s: s}
	switch k := s[0]; {
	case k == 'n':
		return false, nil
	case k == '{' && e.deep:
		if err := e.object(r, nil); err != nil {
			return false, err
		}
		return e.end(0)
	case k == '[' && e.deep && s[1] != ']':
		return false, errDeepObject
	case k == '[' || k == '{':
		items := 0 // a list's, which is defined if it has any (RFC 6570 section 2.3)
		err = r.each(func(name string) error {
			if k == '[' {
				items++
			}
			if c := r.s[r.i]; c == '[' || c == '{' || c == 'n' {
				if r.skip() {
					return errNested
				}
				return nil // an undefined item or member is skipped (RFC 6570 section 3.2.1)
			}
			if k == '[' {
				return e.item(r.scalar())
			}
			return e.member(name, r.scalar())
		})
		if err != nil {
			return false, err
		}
		return e.end(items)
	}
	return e.primitive(r.scalar())
}

func (e *emitter) primitive(s string) (bool, error) {
	switch {
	case e.deep:
		return false, errDeepObject
	case e.composite:
		return false, errComposite
	}
	if err := e.item(s); err != nil {
		return false, err
	}
	return e.end(0)
}

// next writes what precedes an item or member: the value's start before
// the first, unless the parameter cannot be serialized, else a separator. A
// named value not exploded defers the "=" after its name while its first
// item, empty says, is "".
func (e *emitter) next(empty bool) error {
	switch {
	case e.n == 0:
		if e.Err != nil {
			return e.Err
		}
		e.b.WriteString(e.lead)
		if e.named && !e.Explode {
			e.b.WriteString(e.name)
			if e.held = empty; !empty {
				e.b.WriteByte('=')
			}
		}
	case e.Explode:
		e.b.WriteString(e.sep)
	default:
		if e.held {
			e.b.WriteByte('=')
			e.held = false
		}
		e.b.WriteString(e.delim)
	}
	e.n++
	return nil
}

// item writes an array's item s.
func (e *emitter) item(s string) error {
	if err := e.next(s == ""); err != nil {
		return err
	}
	if e.named && e.Explode {
		e.b.WriteString(e.name)
		if s == "" {
			e.b.WriteString(e.ifemp)
			return nil
		}
		e.b.WriteByte('=')
	}
	escapeTo(e.b, s, e.set)
	return nil
}

// member writes an object's member named k, whose value is s.
func (e *emitter) member(k, s string) error {
	if err := e.next(false); err != nil { // not exploded, it holds a delimiter
		return err
	}
	escapeTo(e.b, k, e.set)
	switch {
	case !e.Explode:
		e.b.WriteString(e.delim)
	case s == "":
		e.b.WriteString(e.ifemp) // RFC 6570 section 3.2.1: the name alone, but in form style
		return nil
	default:
		e.b.WriteByte('=')
	}
	escapeTo(e.b, s, e.set)
	return nil
}

// end completes the value, reporting whether it is defined. A list of items
// members is, though none of them be (RFC 6570 section 2.3); then, not
// exploded, it is written as "" is, and exploded, nothing is written.
func (e *emitter) end(items int) (bool, error) {
	var err error
	switch {
	case e.n > 0 || items == 0:
	case !e.Explode:
		err = e.item("")
	case e.Err != nil:
		err = e.Err
	}
	if e.held {
		e.b.WriteString(e.ifemp)
	}
	return e.n > 0 || items > 0, err
}

// object writes the members of the object at the read position as
// deepObject pairs, path holding the names of the objects around them.
func (e *emitter) object(r *jsonReader, path []string) error {
	return r.each(func(name string) error {
		switch r.s[r.i] {
		case 'n':
			r.i += len("null")
			return nil
		case '[':
			if r.s[r.i+1] != ']' {
				return errDeepObject
			}
			r.i += len("[]")
			return nil
		case '{':
			return e.object(r, append(path, name))
		}
		if err := e.next(false); err != nil {
			return err
		}
		e.b.WriteString(e.name)
		for _, k := range path {
			e.b.WriteString("%5B")
			escapeTo(e.b, k, e.set)
			e.b.WriteString("%5D")
		}
		e.b.WriteString("%5B")
		escapeTo(e.b, name, e.set)
		e.b.WriteString("%5D=")
		escapeTo(e.b, r.scalar(), e.set)
		return nil
	})
}

// A jsonReader reads JSON text as encoding/json writes it: valid, with no
// space between tokens.
type jsonReader struct {
	s string
	i int // the read position
}

// scalar reads the string, number or boolean at the read position, and
// returns its value.
func (r *jsonReader) scalar() string {
	s, i := r.s, r.i
	if s[i] == '"' {
		r.i = closingQuote(s, i) + 1
		return jsonString(s[i:r.i])
	}
	for r.i < len(s) && s[r.i] != ',' && s[r.i] != ']' && s[r.i] != '}' {
		r.i++
	}
	return s[i:r.i]
}

// each calls f for each member of the object, or item of the array, at the
// read position, which is then at the member's value, whose name f is
// given, or at the item; f reads past it. each then moves past the object
// or array.
func (r *jsonReader) each(f func(name string) error) error {
	end := r.s[r.i] + 2 // '}' or ']'
	for r.i++; r.s[r.i] != end; {
		var name string
		if end == '}' {
			name = r.scalar()
			r.i++ // the colon
		}
		if err := f(name); err != nil {
			return err
		}
		if r.s[r.i] == ',' {
			r.i++
		}
	}
	r.i++
	return nil
}

// skip moves past the value at the read position and reports whether it
// is defined: RFC 6570 reads null, an empty array and an object whose
// members are all undefined as undefined.
func (r *jsonReader) skip() (defined bool) {
	switch r.s[r.i] {
	case 'n':
		r.i += len("null")
	case '[', '{':
		defined = r.s[r.i] == '[' && r.s[r.i+1] != ']'
		r.each(func(string) error {
			defined = r.skip() || defined
			return nil
		})
	default:
		r.scalar()
		defined = true
	}
	return defined
}

// pathOf returns the path that the escaped path raw encodes, each writer's
// {name} token kept as written.
func pathOf(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case c == '%' && i+2 < len(raw) && hexDigit(raw[i+1]) && hexDigit(raw[i+2]):
			b.WriteByte(unhex(raw[i+1])<<4 | unhex(raw[i+2]))
			i += 2
		case c == '{': // a token, to its end
			j := strings.IndexByte(raw[i:], '}')
			if j < 0 {
				j = len(raw) - i - 1
			}
			b.WriteString(raw[i : i+j+1])
			i += j
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func unhex(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return (c | 0x20) - 'a' + 10
}

// runWriters calls the call's parameter writers with req in parameter
// order, then refuses each path parameter whose {name} token a writer left
// unresolved.
func (o *operation) runWriters(req *http.Request, writers map[string]func(*http.Request) error, re *RequestError) {
	for i := range o.params {
		if p := &o.params[i]; p.In != "" && writers[p.Key] != nil {
			if err := writers[p.Key](req); err != nil {
				re.input(p.Key, err)
			}
		}
	}
	u := req.URL
	encodes := u.RawPath == "" || pathOf(u.RawPath) == u.Path
	for _, i := range o.pathParams {
		p := &o.params[i]
		if writers[p.Key] == nil {
			continue
		}
		// The token is found in RawPath, where other values are
		// percent-encoded, or else in Path.
		token := "{" + p.Name + "}"
		if !encodes || strings.Contains(u.RawPath, token) || u.RawPath == "" && strings.Contains(u.Path, token) {
			re.input(p.Key, errors.New("the writer left its {name} token, or a RawPath that is not an encoding of Path"))
		}
	}
}
