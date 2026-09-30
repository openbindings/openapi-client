package openapi

import (
	"bytes"
	"context"
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
	var b, cookies strings.Builder
	b.Grow(len(ep.path) + len(o.Path) + 32*len(in.Params))
	if strings.HasSuffix(ep.path, "/") && strings.HasPrefix(o.Path, "/") {
		b.WriteString(ep.path[:len(ep.path)-1])
	} else {
		b.WriteString(ep.path)
	}
	c.writePath(&b, o, in, re)
	query, given, written, sendsCookies := b.Len(), 0, 0, false
	for i := range o.params {
		p := &o.params[i]
		if p.In == "" {
			continue // unknown
		}
		v, ok := in.Params[p.Key]
		w, hasWriter := in.ParamWriters[p.Key]
		if ok {
			given++
		}
		if hasWriter {
			written++
			switch {
			case ok:
				re.input(p.Key, errors.New("the parameter is given in both Params and ParamWriters"))
			case w == nil:
				re.input(p.Key, errors.New("the parameter's writer is nil"))
			}
		}
		if p.In == "path" && p.Err == nil {
			continue // serialized in the path
		}
		var hb strings.Builder                  // a header parameter's value
		supplied, wrote := hasWriter, hasWriter // a writer supplies it
		switch {
		case hasWriter || v == nil:
		case p.In == "query":
			lead := "&"
			if b.Len() == query {
				lead = "?"
			}
			supplied, wrote = c.writeParam(&b, lead, p, v, re)
		case p.In == "cookie":
			lead := "; "
			if cookies.Len() == 0 {
				lead = ""
			}
			supplied, wrote = c.writeParam(&cookies, lead, p, v, re)
		case p.In == "header":
			supplied, wrote = c.writeParam(&hb, "", p, v, re)
		default: // a path parameter with Err (see writePath): refused
			c.writeParam(&b, "", p, v, re)
		}
		switch {
		case !supplied && p.required && (p.In != "header" || len(h[p.field]) == 0):
			re.input(p.Key, errMissing)
		case !wrote:
		case p.In == "cookie":
			sendsCookies = true
		case p.In == "header":
			switch s := setter(in.Header, cfg.Header, p.field); {
			case s != "":
				re.setting(s, fmt.Errorf("sets %s, which the parameter %q supplies", p.field, p.Key))
			case hasWriter:
			case validFieldValue(hb.String()):
				h[p.field] = []string{hb.String()}
			default:
				re.input(p.Key, errors.New("a header field cannot carry the value"))
			}
		}
	}
	if sendsCookies {
		if s := setter(in.Header, cfg.Header, "Cookie"); s != "" {
			re.setting(s, errors.New("sets Cookie, which the call's cookie parameters set"))
		} else if cookies.Len() > 0 {
			h["Cookie"] = []string{cookies.String()}
		}
	}
	if given < len(in.Params) || written < len(in.ParamWriters) { // an unknown key
		keys := make(map[string]bool, len(o.params))
		for _, p := range o.params {
			keys[p.Key] = p.In != ""
		}
		unknown := errors.New("the operation declares no such parameter")
		for k := range in.Params {
			if !keys[k] {
				re.input(k, unknown)
			}
		}
		for k := range in.ParamWriters {
			if !keys[k] {
				re.input(k, unknown)
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
	u.RawPath, u.Path = s[:query], s[:query] // as written: net/http would otherwise escape sub-delimiters
	if query < len(s) {
		u.RawQuery = s[query+1:]
	}
	if strings.IndexByte(u.Path, '%') >= 0 {
		u.Path = pathOf(u.Path)
	}
	req.ContentLength = p.size
	if written > 0 {
		setBody(req, p)
		o.runWriters(req, in.ParamWriters, re)
		if len(re.Inputs) > 0 {
			return req, payload{}, nil, ""
		}
	}
	return req, p, media, security
}

// setter names the Header setting with an entry for field, whether or not
// it has values: Input.Header before Options.Header, or "" for neither.
func setter(in, opts http.Header, field string) string {
	for k := range in {
		if textproto.CanonicalMIMEHeaderKey(k) == field {
			return "Input.Header"
		}
	}
	if _, ok := opts[field]; ok {
		return "Options.Header"
	}
	return ""
}

// writePath writes the path template's parts, with the values of its
// parameters or their writers' {name} tokens, refusing a value that forms a
// "." or ".." segment.
func (c *Client) writePath(b *strings.Builder, o *operation, in *Input, re *RequestError) {
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
		if _, ok := in.ParamWriters[p.Key]; ok {
			b.WriteByte('{')
			b.WriteString(p.Name)
			b.WriteByte('}')
			continue
		}
		given, wrote := false, false
		if v := in.Params[p.Key]; v != nil {
			given, wrote = c.writeParam(b, p.first, p, v, re)
		}
		if wrote {
			valued = part.param
		}
		if !given {
			re.input(p.Key, errMissing) // unless refused already
		}
	}
	endSegment()
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
	return endpoint{u.Scheme, u.Host, escape(u.EscapedPath(), pathSet)}, nil
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
	b, at, ok, err := c.encodeValue(m, m.class(), in.Body)
	switch {
	case !ok:
		re.fail(notYet("encoding a " + m.full + " body"))
		return payload{}, nil
	case err != nil:
		re.input("Input.Body"+at, err)
		return payload{}, nil
	}
	return payload{data: b, size: int64(len(b))}, md
}

// encodeValue encodes v by the caller's codec for m, or, for a JSON type, as
// encoding/json writes it, refusing an io.Reader or Part that json reaches,
// at the JSON Pointer it returns, and nesting deeper than 1,000 levels. ok is
// false for any other type.
func (c *Client) encodeValue(m parsedMedia, k class, v any) (b []byte, at string, ok bool, err error) {
	if codec, _ := c.cfg.codec(m); codec != nil {
		var buf bytes.Buffer
		err = codec.Encode(&buf, v)
		return buf.Bytes(), "", true, err
	}
	if k != jsonClass {
		return nil, "", false, nil
	}
	if b, err = json.Marshal(v); err != nil { // first, as it refuses a cycle, which the walk would follow
		return nil, "", true, &encodingError{err}
	}
	at, err = checkJSON(c.doc, v, b)
	return b, at, true, err
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

// setBody gives req the body p, read again from the start by GetBody when p
// can be.
func setBody(req *http.Request, p payload) {
	if p.size != 0 {
		req.Body = &sentBody{p: p}
		if p.once == nil {
			req.GetBody = func() (io.ReadCloser, error) { return &sentBody{p: p}, nil }
		}
	}
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
