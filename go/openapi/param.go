package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	"deepObject":     {composite: true, deep: true},
}

var (
	errItem       = errors.New("an array item must be a string, number or boolean")
	errNested     = errors.New("the style cannot serialize a nested array or object")
	errComposite  = errors.New("the style takes an array or object")
	errDeepObject = errors.New("the deepObject style takes an object without arrays")
	errDepth      = errors.New("the value nests deeper than 1,000 levels")
)

// writeParam writes the value v given for p into b, after lead unless v
// is undefined, and reports whether it wrote a value, recording in re why
// it cannot. In a path, lead is the style's first.
func (cfg *config) writeParam(b *strings.Builder, lead string, p *param, v any, re *RequestError) bool {
	if p.ContentType == "" {
		e := emitter{param: p, b: b, lead: lead}
		defined, err := e.write(v)
		if err != nil {
			re.input(p.Key, err)
		}
		return defined && err == nil
	}
	if cfg.codecsErr != nil {
		re.setting("Options.Codecs", cfg.codecsErr)
	}
	var s string
	err := p.Err
	if err == nil {
		s, err = cfg.encode(p.media, v)
	}
	if err != nil {
		re.input(p.Key, err)
		return false
	}
	b.WriteString(lead)
	switch p.In {
	case "header":
		b.WriteString(s)
		return true
	case "query", "cookie":
		b.WriteString(p.name)
		b.WriteByte('=')
	}
	escapeTo(b, s, unreservedSet)
	return true
}

// encode returns v encoded as a body of media type m is.
func (cfg *config) encode(m parsedMedia, v any) (string, error) {
	if codec, _ := cfg.codec(m); codec != nil {
		var b strings.Builder
		err := codec.Encode(&b, v)
		return b.String(), err
	}
	k := m.class()
	if s, ok := v.(string); ok && k != jsonClass {
		return jsonText(s), nil
	}
	b, err := marshal(v)
	switch {
	case err != nil:
		return "", err
	case k == jsonClass:
		return string(b), nil
	case b[0] == '"':
		return jsonString(string(b)), nil
	case k == textClass && b[0] != '{' && b[0] != '[' && b[0] != 'n':
		return string(b), nil
	case k == textClass:
		return "", fmt.Errorf("%s takes a string, number or boolean", m.full)
	}
	return "", fmt.Errorf("%s takes a string", m.full)
}

// marshal returns v as encoding/json writes it, refusing a value that nests
// deeper than 1,000 levels.
func marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	switch {
	case err != nil:
		return nil, &encodingError{err}
	case tooDeep(b):
		return nil, errDepth
	}
	return b, nil
}

// tooDeep reports whether the JSON text b, as encoding/json writes it, nests
// deeper than 1,000 levels, the outermost value being level 1: whether an
// array or object at level 1,000 has a member.
func tooDeep(b []byte) bool {
	if bytes.Count(b, []byte("["))+bytes.Count(b, []byte("{")) < maxDepth {
		return false
	}
	depth := 0 // the arrays and objects open
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			for i++; b[i] != '"'; i++ {
				if b[i] == '\\' {
					i++
				}
			}
		case '[', '{':
			if depth++; depth == maxDepth && b[i+1] != ']' && b[i+1] != '}' {
				return true
			}
		case ']', '}':
			depth--
		}
	}
	return false
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

// write writes v, reporting whether it is defined.
func (e *emitter) write(v any) (bool, error) {
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
		return e.end(), nil
	}
	b, err := marshal(v)
	if err != nil {
		return false, err
	}
	r := &jsonReader{s: string(b)}
	switch r.s[0] {
	case 'n':
		return false, nil
	case '[':
		if r.s[1] != ']' && e.deep {
			return false, errDeepObject
		}
		err = r.each(func(string) error {
			if k := r.s[r.i]; k == '[' || k == '{' || k == 'n' {
				return errItem
			}
			return e.item(r.scalar())
		})
	case '{':
		if e.deep {
			err = e.object(r, nil)
			break
		}
		err = r.each(func(name string) error {
			if k := r.s[r.i]; k == '[' || k == '{' || k == 'n' {
				if r.skip() {
					return errNested
				}
				return nil // an undefined member is skipped
			}
			return e.member(name, r.scalar())
		})
	default:
		return e.primitive(r.scalar())
	}
	return e.end(), err
}

// start writes lead, before the value's first item, unless the parameter
// cannot be serialized.
func (e *emitter) start() error {
	if e.Err != nil {
		return e.Err
	}
	e.b.WriteString(e.lead)
	return nil
}

func (e *emitter) primitive(s string) (bool, error) {
	switch {
	case e.deep:
		return false, errDeepObject
	case e.composite:
		return false, errComposite
	}
	if err := e.start(); err != nil {
		return false, err
	}
	if e.named {
		e.b.WriteString(e.name)
		if s == "" {
			e.b.WriteString(e.ifemp)
			return true, nil
		}
		e.b.WriteByte('=')
	}
	escapeTo(e.b, s, e.set)
	return true, nil
}

// next writes what precedes an item or member, whose first text is empty
// if empty: the value's start before the first, else a separator.
func (e *emitter) next(empty bool) error {
	switch {
	case e.n == 0:
		if err := e.start(); err != nil {
			return err
		}
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

// end completes the value, reporting whether it was defined.
func (e *emitter) end() bool {
	if e.held {
		e.b.WriteString(e.ifemp)
	}
	return e.n > 0
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
		if e.n == 0 {
			if err := e.start(); err != nil {
				return err
			}
		} else {
			e.b.WriteByte('&')
		}
		e.n++
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
	case '"':
		r.i = closingQuote(r.s, r.i) + 1
		defined = true
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
	encodes := pathOf(u.RawPath) == u.Path // RawPath is an encoding of Path
	for _, i := range o.pathParams {
		p := &o.params[i]
		if writers[p.Key] == nil {
			continue
		}
		if token := "{" + p.Name + "}"; strings.Contains(u.RawPath, token) || !encodes && strings.Contains(u.Path, token) {
			re.input(p.Key, errors.New("the writer left the parameter's {name} token in URL.Path or URL.RawPath"))
		}
	}
}
