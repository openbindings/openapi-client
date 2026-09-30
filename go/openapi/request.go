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

var (
	errMissing    = errors.New("a required parameter is missing")
	errDotSegment = errors.New(`the value forms a "." or ".." path segment, which URI resolution removes`)
)

// record records err at key in *m, keeping the first error for a key.
func record(m *map[string]error, key string, err error) {
	if *m == nil {
		*m = map[string]error{}
	}
	if _, dup := (*m)[key]; !dup {
		(*m)[key] = err
	}
}

func (e *RequestError) setting(key string, err error) { record(&e.Settings, key, err) }
func (e *RequestError) input(key string, err error)   { record(&e.Inputs, key, err) }
func (e *RequestError) fail(err error)                { e.Err = errors.Join(e.Err, err) }

// refused returns e as an error when it holds a problem.
func (e *RequestError) refused() error {
	if e.Err == nil && len(e.Settings) == 0 && len(e.Inputs) == 0 {
		return nil
	}
	re := *e
	return &re
}

// An encodingError is a value encoding/json cannot encode, reported without
// the value.
type encodingError struct{ err error }

func (e *encodingError) Error() string { return "the value cannot be encoded as JSON" }
func (e *encodingError) Unwrap() error { return e.err }

// operation returns the operation key names, compiled, or the refusal of a
// call to it: none has the key, or its Paths entry cannot be read.
func (c *Client) operation(key string) (*operation, error) {
	e, err := c.doc.lookup(key)
	if err != nil {
		return nil, &RequestError{Err: err}
	}
	if o := e.compile(); e.m >= 0 {
		return o, nil
	}
	return nil, &RequestError{Err: e.err}
}

// checkOut returns why out cannot receive a result, or nil.
func checkOut(out any) error {
	if out == nil {
		return nil
	}
	v := reflect.ValueOf(out)
	if _, writer := out.(io.Writer); v.Kind() == reflect.Pointer && v.IsNil() || v.Kind() != reflect.Pointer && !writer {
		return fmt.Errorf("out must be nil, a non-nil *[]byte, an io.Writer or a non-nil pointer, not %T", out)
	}
	return nil
}

// checkOut records a refusal of an out that cannot receive a result, or
// whose codec the Codecs cannot supply.
func (cfg *config) checkOut(out any, re *RequestError) {
	if err := checkOut(out); err != nil {
		re.fail(err)
	} else if decodes(out) && cfg.codecsErr != nil {
		re.setting("Options.Codecs", cfg.codecsErr)
	}
}

// decodes reports whether out is a pointer a response body is decoded
// into.
func decodes(out any) bool {
	switch out.(type) {
	case nil, *[]byte, io.Writer:
		return false
	}
	return true
}

// checkAccept refuses a typed out when the operation's 2xx responses offer
// media types of several codec classes and the request asks for none.
func (cfg *config) checkAccept(o *operation, h http.Header, out any, re *RequestError) {
	few := len(o.success) == 0 || len(o.success) == 1 && len(o.success[0]) < 2
	if _, dynamic := out.(*any); dynamic || !decodes(out) || few || len(h["Accept"]) > 0 {
		return
	}
	classOf := func(m parsedMedia) string {
		if _, key := cfg.codec(m); key != "" {
			return key
		}
		return strconv.Itoa(int(m.class()))
	}
	first, mixed := classOf(o.success[0][0]), false
	for _, list := range o.success {
		mixed = mixed || slices.ContainsFunc(list, func(m parsedMedia) bool { return classOf(m) != first })
	}
	if mixed {
		var types []string
		for _, list := range o.success {
			for _, m := range list {
				types = append(types, m.full)
			}
		}
		re.setting("Options.Header", fmt.Errorf("2xx responses offer %s; set an Accept field to decode into %T",
			strings.Join(types, ", "), out))
	}
}

// newRequest builds the request for o with in, carried by ctx, recording
// every problem in re. It returns the request, whose header is complete
// even when refused, its body's content, the Media governing the body and
// the key of the security alternative.
func (c *Client) newRequest(ctx context.Context, o *operation, in *Input, re *RequestError) (*http.Request, payload, *Media, string) {
	if in == nil {
		in = &noInput
	}
	cfg := c.cfg
	for k, err := range cfg.refused {
		re.setting(k, err)
	}
	if len(in.ParamWriters) > 0 {
		re.fail(notYet("Input.ParamWriters"))
	}
	if err := checkHeader(in.Header); err != nil {
		re.setting("Input.Header", err)
	}
	ep := c.selectServer(o, re)
	security := selectSecurity(o, in, re)
	req, _ := http.NewRequestWithContext(ctx, o.Method, "", nil)
	h := req.Header
	applyFields(h, cfg.Header)
	applyFields(h, in.Header)

	// The path, in template order, then each parameter in declared order.
	var b strings.Builder
	b.Grow(len(ep.path) + len(o.Path) + 16*len(o.params))
	if strings.HasSuffix(ep.path, "/") && strings.HasPrefix(o.Path, "/") {
		b.WriteString(ep.path[:len(ep.path)-1])
	} else {
		b.WriteString(ep.path)
	}
	o.writePath(&b, in.Params, re)
	query, given := b.Len(), 0
	for i := range o.params {
		p := &o.params[i]
		v, ok := in.Params[p.Key]
		if ok && p.In != "" {
			given++
		}
		if p.In == "" || p.In == "path" && p.Err == nil {
			continue // unknown, or serialized in the path
		}
		d, ok := p.data(v, ok, re)
		switch {
		case !ok:
		case d.kind == undefined:
			if p.required && (p.In != "header" || len(h[p.field]) == 0) {
				re.input(p.Key, errMissing)
			}
		case p.In == "query":
			if b.Len() == query {
				b.WriteByte('?')
			} else {
				b.WriteByte('&')
			}
			d.write(&b, p.name, true, p.Explode, true)
		case len(h[p.field]) > 0:
			setter := "Options.Header"
			for k, vs := range in.Header {
				if len(vs) > 0 && textproto.CanonicalMIMEHeaderKey(k) == p.field {
					setter = "Input.Header"
				}
			}
			re.setting(setter, fmt.Errorf("sets %s, which the parameter %q supplies", p.field, p.Key))
		default:
			var sb strings.Builder
			d.write(&sb, "", false, p.Explode, false)
			if s := sb.String(); validFieldValue(s) {
				h[p.field] = []string{s}
			} else {
				re.input(p.Key, errors.New("a header field cannot carry the value"))
			}
		}
	}
	if given < len(in.Params) {
		for k := range in.Params {
			if !slices.ContainsFunc(o.params, func(p param) bool { return p.Key == k && p.In != "" }) {
				re.input(k, errors.New("the operation declares no such parameter"))
			}
		}
	}
	p, media := c.body(o, in, h, re)
	if re.Err != nil || len(re.Settings) > 0 || len(re.Inputs) > 0 {
		return req, payload{}, nil, ""
	}

	s := b.String()
	u := req.URL
	u.Scheme, u.Host, req.Host = ep.scheme, ep.host, ep.host
	u.Path = s[:query]
	if query < len(s) {
		u.RawQuery = s[query+1:]
	}
	u.RawPath = u.Path // as written: net/http would otherwise escape sub-delimiters
	if strings.IndexByte(u.Path, '%') >= 0 {
		u.Path, _ = url.PathUnescape(u.Path)
	}
	req.ContentLength = p.size
	return req, p, media, security
}

// writePath writes the path template's parts with the values of params,
// refusing a value that forms a "." or ".." segment.
func (o *operation) writePath(b *strings.Builder, params map[string]any, re *RequestError) {
	segment, valued := b.Len(), -1 // where the segment begins, and a parameter in it
	endSegment := func() {
		if s := b.String()[segment:]; valued >= 0 && (s == "." || s == "..") {
			re.input(o.params[valued].Key, errDotSegment)
		}
	}
	for _, part := range o.path {
		if part.param < 0 {
			for text := part.text; ; {
				i := strings.IndexByte(text, '/')
				if i < 0 {
					b.WriteString(text)
					break
				}
				b.WriteString(text[:i])
				endSegment()
				b.WriteByte('/')
				segment, valued, text = b.Len(), -1, text[i+1:]
			}
			continue
		}
		p := &o.params[part.param]
		v, given := params[p.Key]
		if d, ok := p.data(v, given, re); ok && d.kind == undefined {
			re.input(p.Key, errMissing)
		} else if ok {
			d.write(b, "", false, p.Explode, true)
			valued = part.param
		}
	}
	endSegment()
}

// data returns the JSON data of the value v given for p, undefined when
// none is given, or false after recording why it cannot be serialized.
func (p *param) data(v any, given bool, re *RequestError) (data, bool) {
	if !given {
		return data{}, true
	}
	d, err := jsonData(v)
	switch {
	case err != nil:
		re.input(p.Key, err)
	case d.kind == undefined:
		return d, true
	case p.Err != nil:
		re.input(p.Key, p.Err)
	case p.unsupported != nil:
		re.fail(fmt.Errorf("parameter %q: %w", p.Key, p.unsupported))
	default:
		return d, true
	}
	return data{}, false
}

// applyFields applies header fields over h, as the Header settings are: a
// field replaces the one set before, and one with no values removes it, a
// User-Agent included.
func applyFields(h, fields http.Header) {
	for k, vs := range fields {
		k = textproto.CanonicalMIMEHeaderKey(k)
		switch {
		case len(vs) > 0:
			h[k] = slices.Clone(vs)
		case k == "User-Agent":
			h[k] = nil // present and empty, so net/http writes none
		default:
			delete(h, k)
		}
	}
}

// A dataKind is the shape of a parameter value's JSON data for RFC 6570.
type dataKind int

const (
	undefined dataKind = iota
	primitive
	array
	object
)

// data is a parameter value as JSON data, ready for a style: a primitive's
// text, an array's items, or an object's defined members' names and
// values, alternating.
type data struct {
	kind  dataKind
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
		if utf8.ValidString(v) {
			return data{kind: primitive, text: v}, nil
		}
	case int:
		return data{kind: primitive, text: strconv.Itoa(v)}, nil
	case bool:
		return data{kind: primitive, text: strconv.FormatBool(v)}, nil
	case []string:
		if len(v) == 0 {
			return data{}, nil
		}
		if !slices.ContainsFunc(v, func(s string) bool { return !utf8.ValidString(s) }) {
			return data{kind: array, items: v}, nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return data{}, &encodingError{err}
	}
	t, err := parseTree(context.Background(), string(b), "")
	if err != nil {
		return data{}, &encodingError{err}
	}
	root := value{t, 0}
	var d data
	switch root.kind() {
	case 'n': // undefined
	case '[':
		d.kind = array
		for _, item := range root.members() {
			switch item.kind() {
			case 'n':
				return data{}, errors.New("an array item is null")
			case '[', '{':
				return data{}, errors.New("the style cannot serialize nested values")
			}
			d.items = append(d.items, item.text())
		}
	case '{':
		d.kind = object
		for name, m := range root.members() {
			switch {
			case isUndefined(m):
			case m.kind() == '[' || m.kind() == '{':
				return data{}, errors.New("the style cannot serialize nested values")
			default:
				d.items = append(d.items, name, m.text())
			}
		}
	default:
		d = data{kind: primitive, text: root.text()}
	}
	if d.kind != primitive && len(d.items) == 0 {
		d.kind = undefined
	}
	return d, nil
}

func isUndefined(v value) bool {
	switch v.kind() {
	case 'n':
		return true
	case '[':
		return !v.hasMembers()
	case '{':
		for _, m := range v.members() {
			if !isUndefined(m) {
				return false
			}
		}
		return true
	}
	return false
}

// write writes d in RFC 6570 simple style, or in form style as query pairs
// named name, already percent-encoded; escaped percent-encodes d's names
// and values.
func (d data) write(b *strings.Builder, name string, form, explode, escaped bool) {
	put := func(s string) {
		if escaped {
			escapeTo(b, s)
		} else {
			b.WriteString(s)
		}
	}
	pairs := form && explode && d.kind != primitive // each item or member a pair of its own
	if form && !pairs {
		b.WriteString(name)
		b.WriteByte('=')
	}
	if d.kind == primitive {
		put(d.text)
		return
	}
	for i, s := range d.items {
		switch {
		case i == 0:
		case d.kind == object && i%2 == 1 && explode:
			b.WriteByte('=')
		case pairs:
			b.WriteByte('&')
		default:
			b.WriteByte(',')
		}
		if pairs && d.kind == array {
			b.WriteString(name)
			b.WriteByte('=')
		}
		put(s)
	}
}

// selectServer returns the server the call goes to, recording why none can
// be chosen.
func (c *Client) selectServer(o *operation, re *RequestError) endpoint {
	cfg := c.cfg
	var s *server
	setting := "Options.BaseURL"
	switch {
	case cfg.base != nil:
		return *cfg.base
	case cfg.Server != "":
		setting = "Options.Server"
		for _, sv := range o.servers {
			if sv.URL == cfg.Server && s != nil {
				re.setting(setting, fmt.Errorf("%q names several servers; use Options.ServerID", cfg.Server))
				return endpoint{}
			} else if sv.URL == cfg.Server {
				s = sv
			}
		}
		if s == nil {
			re.setting(setting, fmt.Errorf("the operation has no server with the URL %q", cfg.Server))
			return endpoint{}
		}
	case cfg.ServerID != "":
		setting = "Options.ServerID"
		if i := slices.IndexFunc(o.servers, func(s *server) bool { return s.ID == cfg.ServerID }); i >= 0 {
			s = o.servers[i]
		} else {
			re.setting(setting, errors.New("the operation has no server with this ID"))
			return endpoint{}
		}
	case len(o.servers) == 1:
		s = o.servers[0]
	default:
		usable := 0
		for _, sv := range o.servers {
			if _, ok := c.resolve(sv, "", nil); ok {
				s, usable = sv, usable+1
			}
		}
		switch {
		case usable == 0:
			re.setting(setting, errors.New("the operation has no usable server"))
			for _, sv := range o.servers {
				c.resolve(sv, setting, re)
			}
			return endpoint{}
		case usable > 1:
			re.setting("Options.Server", fmt.Errorf("the operation has %d usable servers; select one with Options.Server, Options.ServerID or Options.BaseURL", usable))
			return endpoint{}
		}
	}
	ep, _ := c.resolve(s, setting, re)
	return ep
}

// resolve returns the endpoint server s resolves to with the Client's
// variable values, recording in re, if not nil, why s cannot be used.
func (c *Client) resolve(s *server, setting string, re *RequestError) (endpoint, bool) {
	cfg := c.cfg
	if s.Err != nil {
		if re != nil {
			re.setting(setting, s.Err)
		}
		return endpoint{}, false
	}
	given := slices.ContainsFunc(s.Variables, func(v Variable) bool { _, ok := cfg.Variables[v.Name]; return ok })
	if s.fixed != nil && !given {
		return *s.fixed, true
	}
	if ep, ok := cfg.endpoints.Load(s); ok {
		return ep.(endpoint), true
	}
	ok := true
	refuse := func(name string, err error) {
		if ok = false; re != nil {
			re.setting("Options.Variables["+strconv.Quote(name)+"]", err)
		}
	}
	type span struct {
		i, at int // the variable's place in the template, and where its value begins
		value string
	}
	var values []span // the values given
	u := s.substitute(func(i, at int) string {
		v := s.Variables[s.vars[i].index]
		value, given := cfg.Variables[v.Name]
		switch {
		case !given && v.DefaultSet:
			return v.Default
		case !given:
			refuse(v.Name, errors.New("the server variable has no default; give it a value"))
		case value != v.Default && v.Enum != nil && !slices.Contains(v.Enum, value):
			refuse(v.Name, fmt.Errorf("the value is not one of the variable's enum %q", v.Enum))
		default:
			values = append(values, span{i, at, value})
		}
		return value
	})
	colon, _, path := urlParts(u)
	for _, v := range values {
		if err := s.vars[v.i].part.check(u, v.value, v.at, colon, path); err != nil {
			refuse(s.Variables[s.vars[v.i].index].Name, err)
		}
	}
	if !ok {
		return endpoint{}, false
	}
	ep, err := c.doc.resolveServerURL(u)
	if err != nil {
		err = fmt.Errorf("server URL %q cannot be used with the variables' values: %w", s.URL, err)
		if len(values) == 0 && re != nil {
			re.setting(setting, err)
		}
		for _, v := range values { // the values given make it unusable
			refuse(s.Variables[s.vars[v.i].index].Name, err)
		}
		return endpoint{}, false
	}
	cfg.endpoints.Store(s, ep)
	return ep, true
}

// dotUnescaper decodes percent-encoded dots, equivalent to "." (RFC 3986
// section 6.2.2.2).
var dotUnescaper = strings.NewReplacer("%2e", ".", "%2E", ".")

// dotSegment reports whether one of the path segments of u from start,
// where one begins, to end is "." or "..", percent-encoded or not.
func dotSegment(u string, start, end int) bool {
	for start < end {
		n := strings.IndexAny(u[start:], "/?#")
		if n < 0 {
			n = len(u) - start
		}
		if s := dotUnescaper.Replace(u[start : start+n]); s == "." || s == ".." {
			return true
		}
		if start += n; start == len(u) || u[start] != '/' {
			return false
		}
		start++
	}
	return false
}

// resolveServerURL resolves the server URL s by the URL rule.
func (d *document) resolveServerURL(s string) (endpoint, error) {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return endpoint{}, errors.New("it is not a URL")
	case !u.IsAbs() && !d.httpBase():
		return endpoint{}, errors.New("a relative URL needs a document retrieved over http or https")
	case !u.IsAbs():
		u = d.base.ResolveReference(u)
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(s, "#") {
		return endpoint{}, errors.New("it has no host, or has userinfo, a query or a fragment")
	}
	return endpoint{u.Scheme, u.Host, escapePath(u.EscapedPath())}, nil
}

// httpBase reports whether relative server URLs resolve against the
// document's URI.
func (d *document) httpBase() bool { return d.base.Scheme == "http" || d.base.Scheme == "https" }

// selectSecurity selects the security alternative, returning its key.
func selectSecurity(o *operation, in *Input, re *RequestError) string {
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
	case !slices.ContainsFunc(alts, func(r SecurityRequirement) bool { return r.Key != alts[0].Key }):
		i = 0
	default:
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
	var p payload
	raw := true // pre-encoded
	switch v := in.Body.(type) {
	case []byte:
		p = payload{data: v, size: int64(len(v))}
	case io.Reader:
		p = readerPayload(v)
	default:
		if raw = false; c.cfg.codecsErr != nil {
			re.setting("Options.Codecs", c.cfg.codecsErr)
		}
	}
	if typ == "" {
		return payload{}, nil
	}
	if raw {
		h["Content-Type"] = []string{typ}
		return p, md
	}
	if md == nil {
		re.input("Input.Body", errors.New("an empty content map takes only a []byte or io.Reader body"))
		return payload{}, nil
	}
	h["Content-Type"] = []string{typ}
	var b []byte
	var err error
	if codec, _ := c.cfg.codec(m); codec != nil {
		var buf bytes.Buffer
		err = codec.Encode(&buf, in.Body)
		b = buf.Bytes()
	} else if m.class() == jsonClass {
		if b, err = json.Marshal(in.Body); err != nil {
			err = &encodingError{err}
		} else if at, ok := c.doc.findReader(in.Body); ok {
			re.input("Input.Body"+at, errors.New("a JSON value cannot hold an io.Reader or a Part"))
			return payload{}, nil
		}
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
func (c *Client) mediaType(o *operation, in *Input, re *RequestError) (string, parsedMedia, *Media) {
	cfg, declared := c.cfg, o.Body.Media
	if cfg.mediaTypeErr != nil {
		re.setting("Options.MediaType", cfg.mediaTypeErr)
	}
	typ := in.MediaType
	if typ == "" && cfg.MediaType != "" && cfg.mediaTypeErr == nil && match(o.body, declared, cfg.mediaType) != nil {
		typ = cfg.MediaType
	}
	if typ == "" {
		if len(declared) == 1 && declared[0].Err == nil && o.body[0].concrete() {
			return declared[0].Type, o.body[0], declared[0]
		}
		re.setting("Input.MediaType", errors.New("the operation offers no single concrete media type; select one with Input.MediaType or Options.MediaType"))
		return "", parsedMedia{}, nil
	}
	m, ok := parseMedia(typ)
	var md *Media
	switch {
	case !ok || !m.concrete():
		re.setting("Input.MediaType", errors.New("not a concrete media type"))
		return "", parsedMedia{}, nil
	case len(declared) > 0:
		if md = match(o.body, declared, m); md == nil {
			re.setting("Input.MediaType", fmt.Errorf("the operation does not declare %s", m.full))
			return "", parsedMedia{}, nil
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

// A walk says where a type's values can hold an io.Reader or a Part that
// encoding/json would reach.
type walk struct {
	reader bool        // the type is one
	holds  bool        // a value of the type can hold one
	fields []jsonField // for a struct, the fields that can
}

// A jsonField is a struct field encoding/json writes.
type jsonField struct {
	name  string
	index []int
}

var (
	readerType        = reflect.TypeFor[io.Reader]()
	partType          = reflect.TypeFor[Part]()
	marshalerType     = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// walkOf returns the walk for t, from the document's cache. open holds the
// types being examined; a value of one of them, reached again through a
// recursive type, is assumed to hold a reader.
func (d *document) walkOf(t reflect.Type, open map[reflect.Type]bool) *walk {
	if w, ok := d.walks.Load(t); ok {
		return w.(*walk)
	}
	if open[t] {
		return &walk{holds: true}
	}
	if open == nil {
		open = map[reflect.Type]bool{}
	}
	open[t] = true
	defer delete(open, t)
	w := &walk{}
	switch k := t.Kind(); {
	case t == partType || t.Implements(readerType):
		w.reader, w.holds = true, true
	case t.Implements(marshalerType) || t.Implements(textMarshalerType):
	case k == reflect.Interface:
		w.holds = true
	case k == reflect.Pointer || k == reflect.Map || k == reflect.Array || k == reflect.Slice && t.Elem().Kind() != reflect.Uint8:
		w.holds = d.walkOf(t.Elem(), open).holds
	case k == reflect.Struct:
		for _, f := range jsonFields(t) {
			if d.walkOf(t.FieldByIndex(f.index).Type, open).holds {
				w.fields = append(w.fields, f)
			}
		}
		w.holds = len(w.fields) > 0
	}
	d.walks.Store(t, w)
	return w
}

// findReader returns the JSON Pointer, from x, of an io.Reader or Part that
// encoding/json would reach in x, walking the values encoding/json itself
// creates without reflection.
func (d *document) findReader(x any) (string, bool) {
	switch x := x.(type) {
	case nil, string, bool, float64, json.Number:
		return "", false
	case map[string]any:
		for k, v := range x {
			if at, ok := d.findReader(v); ok {
				return "/" + escapeToken(k) + at, true
			}
		}
		return "", false
	case []any:
		for i, v := range x {
			if at, ok := d.findReader(v); ok {
				return "/" + strconv.Itoa(i) + at, true
			}
		}
		return "", false
	}
	return d.findValue(reflect.ValueOf(x))
}

// findValue is findReader for a value of any type.
func (d *document) findValue(v reflect.Value) (string, bool) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return "", false
		}
	case reflect.Struct, reflect.Array:
	default:
		return "", false
	}
	w := d.walkOf(v.Type(), nil)
	switch {
	case w.reader:
		return "", true
	case !w.holds:
		return "", false
	}
	child := func(name string, v reflect.Value) (string, bool) {
		at, ok := d.findValue(v)
		return "/" + escapeToken(name) + at, ok
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.CanInterface() {
			return d.findReader(v.Interface())
		}
		return d.findValue(v.Elem())
	case reflect.Pointer:
		return d.findValue(v.Elem())
	case reflect.Struct:
		for _, f := range w.fields {
			if fv, err := v.FieldByIndexErr(f.index); err == nil {
				if at, ok := child(f.name, fv); ok {
					return at, true
				}
			}
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			if at, ok := child(mapKey(it.Key()), it.Value()); ok {
				return at, true
			}
		}
	default:
		for i := range v.Len() {
			if at, ok := child(strconv.Itoa(i), v.Index(i)); ok {
				return at, true
			}
		}
	}
	return "", false
}

// mapKey returns the name encoding/json writes for a map key.
func mapKey(k reflect.Value) string {
	switch {
	case k.Kind() == reflect.String:
		return k.String()
	case k.Kind() == reflect.Pointer && k.IsNil():
		return "" // as encoding/json names a nil TextMarshaler, the only nil key it writes
	}
	if tm, ok := k.Interface().(encoding.TextMarshaler); ok {
		b, _ := tm.MarshalText()
		return string(b)
	}
	return fmt.Sprint(k.Interface())
}

// jsonFields returns the fields of struct type t that encoding/json writes:
// exported fields and those promoted from embedded structs, by their JSON
// names, a shallower field or else the one tagged dominating others of its
// name.
func jsonFields(t reflect.Type) []jsonField {
	type candidate struct {
		jsonField
		tagged bool
	}
	var all []candidate
	var visit func(t reflect.Type, index []int, seen []reflect.Type)
	visit = func(t reflect.Type, index []int, seen []reflect.Type) {
		if slices.Contains(seen, t) {
			return
		}
		seen = append(seen, t)
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			name, _, _ := strings.Cut(tag, ",")
			ft := f.Type
			if ft.Name() == "" && ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch {
			case tag == "-":
			case f.Anonymous && name == "" && ft.Kind() == reflect.Struct:
				visit(ft, append(slices.Clip(index), i), seen)
			case !f.IsExported():
			default:
				tagged := name != "" // a tag with only options does not name the field
				if !tagged {
					name = f.Name
				}
				all = append(all, candidate{jsonField{name, append(slices.Clip(index), i)}, tagged})
			}
		}
	}
	visit(t, nil, nil)
	var fields []jsonField
	for _, c := range all {
		dominant, rivals := c, 0
		for _, o := range all {
			switch {
			case o.name != c.name:
			case len(o.index) < len(dominant.index) || len(o.index) == len(dominant.index) && o.tagged && !dominant.tagged:
				dominant, rivals = o, 0
			case len(o.index) == len(dominant.index) && o.tagged == dominant.tagged:
				rivals++
			}
		}
		if rivals == 1 && slices.Equal(dominant.index, c.index) {
			fields = append(fields, c.jsonField)
		}
	}
	return fields
}
