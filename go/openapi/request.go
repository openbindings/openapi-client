package openapi

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// noInput is the empty Input a nil *Input stands for.
var noInput Input

// notYet reports a feature a later stage of this package implements.
func notYet(feature string) error {
	return fmt.Errorf("%s is not implemented yet: %w", feature, errors.ErrUnsupported)
}

// setting records a problem with the setting key.
func (e *RequestError) setting(key string, err error) {
	if e.Settings == nil {
		e.Settings = map[string]error{}
	}
	if _, dup := e.Settings[key]; !dup {
		e.Settings[key] = err
	}
}

// input records a problem with the input key.
func (e *RequestError) input(key string, err error) {
	if e.Inputs == nil {
		e.Inputs = map[string]error{}
	}
	if _, dup := e.Inputs[key]; !dup {
		e.Inputs[key] = err
	}
}

func (e *RequestError) fail(err error) { e.Err = errors.Join(e.Err, err) }

// refused returns e as an error when it holds a problem.
func (e *RequestError) refused() error {
	if e.Err == nil && len(e.Settings) == 0 && len(e.Inputs) == 0 {
		return nil
	}
	re := *e
	return &re
}

// operation returns the operation key names, compiled, or the refusal of a
// call to it.
func (c *Client) operation(key string) (*operation, error) {
	o, err := c.doc.lookup(key)
	if err != nil {
		return nil, &RequestError{Err: err}
	}
	if o.compile().Err != nil {
		return nil, &RequestError{Err: o.Err}
	}
	return o, nil
}

// checkOut records a refusal of an out that cannot receive a result.
func checkOut(out any, re *RequestError) {
	switch out.(type) {
	case nil, *[]byte, io.Writer:
		return
	}
	if v := reflect.ValueOf(out); v.Kind() != reflect.Pointer || v.IsNil() {
		re.fail(fmt.Errorf("out must be nil, a *[]byte, an io.Writer or a non-nil pointer, not %T", out))
	}
}

// checkAccept refuses a typed out when the operation's 2xx responses offer
// media types of several codec classes and the request asks for none.
func (cfg *config) checkAccept(o *operation, h http.Header, out any, re *RequestError) {
	switch out.(type) {
	case nil, *[]byte, io.Writer, *any:
		return
	}
	if len(o.success) < 2 || len(h["Accept"]) > 0 {
		return
	}
	classOf := func(m media) string {
		if _, key := cfg.codec(m); key != "" {
			return key
		}
		return strconv.Itoa(int(m.class()))
	}
	first := classOf(o.success[0])
	if slices.ContainsFunc(o.success[1:], func(m media) bool { return classOf(m) != first }) {
		types := make([]string, len(o.success))
		for i, m := range o.success {
			types[i] = m.full
		}
		re.setting("Options.Header", fmt.Errorf("2xx responses offer %s; set an Accept field to decode into %T",
			strings.Join(types, ", "), out))
	}
}

// newRequest builds the request for o with in, carried by ctx. It returns
// the request, its body's content, the Media governing the body and the key
// of the security alternative, or records every problem in re.
func (c *Client) newRequest(ctx context.Context, o *operation, in *Input, re *RequestError) (*http.Request, payload, *Media, string) {
	if in == nil {
		in = &noInput
	}
	for k, err := range c.cfg.refused {
		re.setting(k, err)
	}
	if len(in.ParamWriters) > 0 {
		re.fail(notYet("Input.ParamWriters"))
	}
	ep := c.endpoint(o, re)
	security := c.security(o, in, re)
	req, _ := http.NewRequestWithContext(ctx, o.Method, "", nil)
	h := req.Header
	for k, v := range c.cfg.Header {
		h[k] = slices.Clone(v)
	}
	applyFields(h, in.Header, "Input.Header", re)

	// The path, then the query.
	var b strings.Builder
	b.Grow(len(ep.path) + len(o.Path) + 16*len(o.params))
	used := 0
	for i, part := range o.path {
		if part.param < 0 {
			text := part.text
			if i == 0 && strings.HasSuffix(ep.path, "/") && strings.HasPrefix(text, "/") {
				text = text[1:]
			}
			if i == 0 {
				b.WriteString(ep.path)
			}
			b.WriteString(text)
			continue
		}
		p := &o.params[part.param]
		v, given := in.Params[p.Key]
		if given {
			used++
		}
		if d, ok := p.data(v, given, re); ok {
			d.writeSimple(&b, p.Explode, true)
		}
	}
	query := b.Len()
	for i := range o.params {
		p := &o.params[i]
		if p.In == "path" || p.In == "" {
			continue
		}
		v, given := in.Params[p.Key]
		if given {
			used++
		}
		d, ok := p.data(v, given, re)
		switch {
		case !ok:
		case p.In == "query":
			if b.Len() == query {
				b.WriteByte('?')
			} else {
				b.WriteByte('&')
			}
			d.writeForm(&b, p.name, p.Explode)
		case len(h[p.field]) > 0:
			setter := "Options.Header"
			for k, vs := range in.Header {
				if len(vs) > 0 && textproto.CanonicalMIMEHeaderKey(k) == p.field {
					setter = "Input.Header"
				}
			}
			re.setting(setter, fmt.Errorf("sets %s, which the parameter %s supplies", p.field, p.Key))
		default:
			var sb strings.Builder
			d.writeSimple(&sb, p.Explode, false)
			if s := sb.String(); validFieldValue(s) {
				h[p.field] = []string{s}
			} else {
				re.input(p.Key, errors.New("a header field cannot carry the value"))
			}
		}
	}
	for i := range o.params {
		if p := &o.params[i]; p.In == "header" && p.required && len(h[p.field]) == 0 && re.Inputs[p.Key] == nil {
			re.input(p.Key, errMissing)
		}
	}
	if used < len(in.Params) {
		for k := range in.Params {
			if !slices.ContainsFunc(o.params, func(p param) bool { return p.Key == k && p.In != "" }) {
				re.input(k, errors.New("the operation declares no such parameter"))
			}
		}
	}
	p, media := c.body(o, in, h, re)
	if re.Err != nil || len(re.Settings) > 0 || len(re.Inputs) > 0 {
		return nil, payload{}, nil, ""
	}

	s := b.String()
	u := req.URL
	u.Scheme, u.Host, req.Host = ep.scheme, ep.host, ep.host
	path := s[:query]
	if query < len(s) {
		u.RawQuery = s[query+1:]
	}
	u.Path = path
	if strings.IndexByte(path, '%') >= 0 {
		if decoded, err := url.PathUnescape(path); err == nil {
			u.Path, u.RawPath = decoded, path
		}
	}
	req.ContentLength = p.size
	return req, p, media, security
}

var errMissing = errors.New("a required parameter is missing")

// data returns the JSON data of the value v given for p, and whether it is
// to be serialized, recording why it cannot be.
func (p *param) data(v any, given bool, re *RequestError) (data, bool) {
	missing := p.required && p.In != "header" // a header field may supply a header parameter
	if !given {
		if missing {
			re.input(p.Key, errMissing)
		}
		return data{}, false
	}
	d, err := jsonData(v)
	switch {
	case err != nil:
		re.input(p.Key, err)
	case d.kind == undefined:
		if missing {
			re.input(p.Key, errMissing)
		}
	case p.Err != nil:
		re.input(p.Key, p.Err)
	case p.todo != nil:
		re.fail(fmt.Errorf("parameter %s: %w", p.Key, p.todo))
	default:
		return d, true
	}
	return data{}, false
}

func validFieldValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

// applyFields applies header fields over h, as the Header settings are: a
// field replaces the one set before, and one with no values removes it.
func applyFields(h, fields http.Header, setting string, re *RequestError) {
	for k, vs := range fields {
		k = textproto.CanonicalMIMEHeaderKey(k)
		switch {
		case reservedField(k):
			re.setting(setting, fmt.Errorf("sets %s, which the client generates", k))
		case len(vs) == 0:
			delete(h, k)
		default:
			h[k] = slices.Clone(vs)
		}
	}
}

// The kinds of data.
const (
	undefined = iota
	primitive
	array
	object
)

// data is a parameter value as JSON data, ready for a style: a primitive's
// text, an array's items, or an object's defined members' names and
// values, alternating.
type data struct {
	kind  int
	text  string
	items []string
}

// jsonData converts v to JSON data as encoding/json would. RFC 6570 reads
// null, an empty array and an object with no defined member as undefined.
func jsonData(v any) (data, error) {
	switch v := v.(type) {
	case nil:
		return data{}, nil
	case string:
		if !utf8.ValidString(v) { // as encoding/json writes it
			var b strings.Builder
			for _, r := range v {
				b.WriteRune(r)
			}
			v = b.String()
		}
		return data{kind: primitive, text: v}, nil
	case int:
		return data{kind: primitive, text: strconv.Itoa(v)}, nil
	case int64:
		return data{kind: primitive, text: strconv.FormatInt(v, 10)}, nil
	case bool:
		return data{kind: primitive, text: strconv.FormatBool(v)}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return data{}, err
	}
	n, err := parseTree(string(b), "")
	if err != nil {
		return data{}, err
	}
	d := data{kind: primitive, text: n.text}
	switch n.kind {
	case 'n':
		d.kind = undefined
	case 't', 'f':
		d.text = string(b)
	case '[':
		d.kind = array
		for _, k := range n.kids {
			switch k.kind {
			case 'n':
				return data{}, errors.New("an array item is null")
			case '[', '{':
				return data{}, errors.New("the style cannot serialize nested values")
			}
			d.items = append(d.items, text(k))
		}
	case '{':
		d.kind = object
		for _, k := range n.kids {
			switch {
			case isUndefined(&k):
			case k.kind == '[' || k.kind == '{':
				return data{}, errors.New("the style cannot serialize nested values")
			default:
				d.items = append(d.items, k.key, text(k))
			}
		}
	}
	if (d.kind == array || d.kind == object) && len(d.items) == 0 {
		d.kind = undefined
	}
	return d, nil
}

// text returns a primitive node's text.
func text(n node) string {
	switch n.kind {
	case 't':
		return "true"
	case 'f':
		return "false"
	}
	return n.text
}

func isUndefined(n *node) bool {
	switch n.kind {
	case 'n':
		return true
	case '[':
		return len(n.kids) == 0
	case '{':
		for i := range n.kids {
			if !isUndefined(&n.kids[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// writeSimple writes d in RFC 6570 simple style, its names and values
// percent-encoded when escaped.
func (d data) writeSimple(b *strings.Builder, explode, escaped bool) {
	write := func(s string) {
		if escaped {
			escapeTo(b, s)
		} else {
			b.WriteString(s)
		}
	}
	if d.kind == primitive {
		write(d.text)
		return
	}
	for i, s := range d.items {
		switch {
		case i == 0:
		case d.kind == object && i%2 == 1 && explode:
			b.WriteByte('=')
		default:
			b.WriteByte(',')
		}
		write(s)
	}
}

// writeForm writes d in RFC 6570 form style as query pairs, name already
// percent-encoded.
func (d data) writeForm(b *strings.Builder, name string, explode bool) {
	switch {
	case d.kind == object && explode:
		for i := 0; i < len(d.items); i += 2 {
			if i > 0 {
				b.WriteByte('&')
			}
			escapeTo(b, d.items[i])
			b.WriteByte('=')
			escapeTo(b, d.items[i+1])
		}
	case d.kind == array && explode:
		for i, s := range d.items {
			if i > 0 {
				b.WriteByte('&')
			}
			b.WriteString(name)
			b.WriteByte('=')
			escapeTo(b, s)
		}
	default:
		b.WriteString(name)
		b.WriteByte('=')
		d.writeSimple(b, false, true)
	}
}

// endpoint returns the server the call goes to, recording why none can be
// chosen.
func (c *Client) endpoint(o *operation, re *RequestError) endpoint {
	cfg := c.cfg
	var s *server
	switch {
	case cfg.base != nil:
		return *cfg.base
	case cfg.Server != "":
		var found []*server
		for _, sv := range o.servers {
			if sv.URL == cfg.Server || sv.Name == cfg.Server {
				found = append(found, sv)
			}
		}
		switch len(found) {
		case 0:
			re.setting("Options.Server", fmt.Errorf("the operation has no server with the URL or name %q", cfg.Server))
			return endpoint{}
		case 1:
			s = found[0]
		default:
			re.setting("Options.Server", fmt.Errorf("%q names several servers; use Options.ServerID", cfg.Server))
			return endpoint{}
		}
	case cfg.ServerID != "":
		i := slices.IndexFunc(o.servers, func(s *server) bool { return s.ID == cfg.ServerID })
		if i < 0 {
			re.setting("Options.ServerID", errors.New("the operation has no server with this ID"))
			return endpoint{}
		}
		s = o.servers[i]
	case len(o.usable) == 1:
		s = o.usable[0]
	case len(o.usable) == 0:
		re.setting("Options.BaseURL", errors.New("the operation has no usable server"))
		return endpoint{}
	default:
		re.setting("Options.Server", fmt.Errorf("the operation has %d usable servers; select one with Options.Server, Options.ServerID or Options.BaseURL", len(o.usable)))
		return endpoint{}
	}
	if s.Err != nil {
		re.setting("Options.BaseURL", s.Err)
		return endpoint{}
	}
	if s.fixed != nil && !slices.ContainsFunc(s.Variables, func(v Variable) bool { _, ok := cfg.Variables[v.Name]; return ok }) {
		return *s.fixed
	}
	ok := true
	u := s.substitute(func(v Variable) string {
		value, given := cfg.Variables[v.Name]
		key := "Options.Variables[" + strconv.Quote(v.Name) + "]"
		switch {
		case given && value != v.Default && v.Enum != nil && !slices.Contains(v.Enum, value):
			re.setting(key, fmt.Errorf("the value is not one of the variable's enum %q", v.Enum))
		case given:
			return value
		case v.DefaultSet:
			return v.Default
		default:
			re.setting(key, errors.New("the server variable has no default; give it a value"))
		}
		ok = false
		return ""
	})
	if !ok {
		return endpoint{}
	}
	ep, err := c.doc.endpoint(u)
	if err != nil {
		re.setting("Options.BaseURL", fmt.Errorf("server URL %q cannot be used: %w", s.URL, err))
	}
	return ep
}

// endpoint resolves the server URL s by the URL rule.
func (d *document) endpoint(s string) (endpoint, error) {
	u, err := url.Parse(s)
	if err != nil {
		return endpoint{}, err
	}
	if !u.IsAbs() {
		if d.base.Scheme != "http" && d.base.Scheme != "https" {
			return endpoint{}, errors.New("a relative URL needs a document retrieved over http or https")
		}
		u = d.base.ResolveReference(u)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return endpoint{}, errors.New("it has userinfo, a query or a fragment")
	}
	return endpoint{u.Scheme, u.Host, u.EscapedPath()}, nil
}

// security selects the security alternative, returning its key.
func (c *Client) security(o *operation, in *Input, re *RequestError) string {
	alts := o.Security
	i := -1
	switch {
	case in.Security != "":
		if len(alts) == 0 && in.Security == "{}" {
			return ""
		}
		if i = slices.IndexFunc(alts, func(r SecurityRequirement) bool { return r.Key == in.Security }); i < 0 {
			re.setting("Input.Security", errors.New("the operation does not offer this security alternative"))
			return ""
		}
	case len(alts) == 0:
		return ""
	case c.cfg.SecurityKey != "" || c.cfg.Security != nil:
		var keys []string
		for j, r := range alts {
			if offered(r, c.cfg) && !slices.Contains(keys, r.Key) {
				i, keys = j, append(keys, r.Key)
			}
		}
		if len(keys) > 1 {
			re.setting("Input.Security", errors.New("several alternatives have the schemes Options.Security names; select one"))
			return ""
		}
	}
	if i < 0 && !slices.ContainsFunc(alts, func(r SecurityRequirement) bool { return r.Key != alts[0].Key }) {
		i = 0
	}
	if i < 0 {
		re.setting("Options.Security", fmt.Errorf("the operation offers %d security alternatives; select one with Options.Security, Options.SecurityKey or Input.Security", len(alts)))
		return ""
	}
	if len(alts[i].Schemes) > 0 {
		re.fail(notYet("sending credentials"))
	}
	return alts[i].Key
}

// body encodes the request body, setting its Content-Type in h, and
// returns its content and the Media governing it.
func (c *Client) body(o *operation, in *Input, h http.Header, re *RequestError) (payload, *Media) {
	switch {
	case o.Body == nil:
		if in.Body != nil {
			re.input("Input.Body", errors.New("the operation takes no request body"))
		}
		return payload{}, nil
	case in.Body == nil:
		if o.Body.Required {
			re.input("Input.Body", errors.New("the operation requires a request body"))
		}
		return payload{}, nil
	}
	typ, m, md := c.mediaType(o, in, re)
	if typ == "" {
		return payload{}, nil
	}
	h["Content-Type"] = []string{typ}
	switch v := in.Body.(type) {
	case []byte:
		return payload{data: v, size: int64(len(v))}, md
	case io.Reader:
		return readerPayload(v), md
	}
	if md == nil {
		re.input("Input.Body", errors.New("an empty content map takes only a []byte or io.Reader body"))
		return payload{}, nil
	}
	var b []byte
	var err error
	if codec, _ := c.cfg.codec(m); codec != nil {
		var buf bytes.Buffer
		err = codec.Encode(&buf, in.Body)
		b = buf.Bytes()
	} else if m.class() == jsonClass {
		if at, ok := findReader(reflect.ValueOf(in.Body)); ok {
			re.input("Input.Body"+at, errors.New("a JSON value cannot hold an io.Reader or a Part"))
			return payload{}, nil
		}
		b, err = json.Marshal(in.Body)
	} else {
		re.fail(notYet("encoding a " + m.full + " body"))
		return payload{}, nil
	}
	if err != nil {
		re.input("Input.Body", err)
	}
	return payload{data: b, size: int64(len(b))}, md
}

// mediaType selects the request body's media type: as sent, parsed, and
// the Media governing it.
func (c *Client) mediaType(o *operation, in *Input, re *RequestError) (string, media, *Media) {
	declared := o.Body.Media
	typ := in.MediaType
	if typ == "" && c.cfg.MediaType != "" && match(o.body, declared, c.cfg.mediaType) != nil {
		typ = c.cfg.MediaType
	}
	if typ == "" {
		if len(declared) == 1 && declared[0].Err == nil && o.body[0].concrete() {
			return declared[0].Type, o.body[0], declared[0]
		}
		re.setting("Input.MediaType", errors.New("the operation offers no single concrete media type; select one with Input.MediaType or Options.MediaType"))
		return "", media{}, nil
	}
	m, ok := parseMedia(typ)
	var md *Media
	switch {
	case !ok || !m.concrete():
		re.setting("Input.MediaType", fmt.Errorf("%q is not a concrete media type", typ))
		return "", media{}, nil
	case len(declared) > 0:
		if md = match(o.body, declared, m); md == nil {
			re.setting("Input.MediaType", fmt.Errorf("the operation does not declare %s", typ))
			return "", media{}, nil
		}
	}
	return typ, m, md
}

// readerPayload returns the content of a reader given as the body,
// replayable from where it stands when it is one of the types Input.Body
// lists.
func readerPayload(r io.Reader) payload {
	switch r := r.(type) {
	case *bytes.Buffer:
		return payload{data: r.Bytes(), size: int64(r.Len())}
	case *bytes.Reader:
		return payload{ra: r, off: r.Size() - int64(r.Len()), size: int64(r.Len())}
	case *strings.Reader:
		return payload{ra: r, off: r.Size() - int64(r.Len()), size: int64(r.Len())}
	case *os.File:
		if fi, err := r.Stat(); err == nil && fi.Mode().IsRegular() {
			if off, err := r.Seek(0, io.SeekCurrent); err == nil {
				return payload{ra: r, off: off, size: fi.Size() - off}
			}
		}
	}
	return payload{once: r, size: -1}
}

var (
	readerType        = reflect.TypeFor[io.Reader]()
	partType          = reflect.TypeFor[Part]()
	marshalerType     = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// findReader returns the JSON Pointer, from v, of an io.Reader or Part that
// encoding/json would reach in v.
func findReader(v reflect.Value) (string, bool) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return "", false
		}
	case reflect.Struct, reflect.Array:
	default:
		return "", false
	}
	t := v.Type()
	switch {
	case t == partType || t.Implements(readerType):
		return "", true
	case t.Implements(marshalerType) || t.Implements(textMarshalerType):
		return "", false
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		return findReader(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			fv := v.Field(i)
			if k := fv.Kind(); k < reflect.Array || k == reflect.String {
				continue // a scalar
			}
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() && !f.Anonymous || name == "-" && f.Tag.Get("json") == "-" {
				continue
			}
			if at, ok := findReader(fv); ok {
				if name == "" && !f.Anonymous {
					name = f.Name
				}
				if name == "" {
					return at, true
				}
				return "/" + escapeToken(name) + at, true
			}
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			if at, ok := findReader(it.Value()); ok {
				return "/" + escapeToken(fmt.Sprint(it.Key().Interface())) + at, true
			}
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "", false
		}
		for i := range v.Len() {
			if at, ok := findReader(v.Index(i)); ok {
				return "/" + strconv.Itoa(i) + at, true
			}
		}
	}
	return "", false
}
