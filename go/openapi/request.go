package openapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
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

// sent returns the problems e holds, found on a redirect hop after the call
// sent its first request, as an error that is not a *RequestError: each
// setting's error with its key, which names the scheme.
func (e *RequestError) sent() error {
	errs := []error{e.Err}
	for _, k := range slices.Sorted(maps.Keys(e.Settings)) {
		errs = append(errs, fmt.Errorf("%s: %w", label(k), e.Settings[k]))
	}
	return errors.Join(errs...)
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
// the security alternative the call applies.
func (c *Client) newRequest(ctx context.Context, o *operation, in *Input, re *RequestError) (*http.Request, payload, *Media, selection) {
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
	var sec selection
	var byCredential []bool // the parameters the credentials supply
	if sec.alt = c.selectSecurity(o, in, re); sec.alt != nil {
		sec.places, byCredential = c.checkCredentials(o, sec.alt, in, ep, re)
	}
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
		if byCredential != nil && byCredential[i] {
			if v != nil || hasWriter {
				re.input(p.Key, errors.New("the call's credential supplies the parameter"))
			}
			continue
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
		return req, payload{}, nil, selection{}
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
		wp := p // the body the writers see
		setBody(req, &wp)
		o.runWriters(req, in.ParamWriters, re)
		if sec.places && !sameOrigin(req.URL, &url.URL{Scheme: ep.scheme, Host: ep.host}) {
			re.fail(errOtherOrigin)
		}
		if re.Err != nil || len(re.Inputs) > 0 {
			return req, payload{}, nil, selection{}
		}
	}
	return req, p, media, sec
}

// errOtherOrigin refuses a request a caller moved to another origin than
// its server's when the call places credentials, which go only there.
var errOtherOrigin = errors.New("the request's URL was changed to another origin, where its credentials cannot go; set Options.BaseURL instead")

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
		if _, refused := re.Inputs[p.Key]; refused {
			continue // its serialization stopped at an earlier occurrence
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
		raw = false
		if c.cfg.codecsErr != nil {
			re.setting("Options.Codecs", c.cfg.codecsErr)
		}
		if typ == "" {
			return payload{}, nil
		}
		if md == nil {
			re.input("Input.Body", errors.New("an empty content map takes only a []byte or io.Reader body"))
			return payload{}, nil
		}
		enc := o.encodings[slices.Index(o.Body.Media, md)]
		switch k := m.class(); {
		case isForm(m):
			var b builder
			c.formBody(&b, enc, v, "Input.Body", true, re)
			p = b.payload()
		case isMultipart(m):
			p, typ = c.multipartBody(enc, typ, m, v, re)
		case k == sequentialClass:
			p = c.sequentialBody(m, v, re)
		default:
			b, at, _, err := c.appendContent(nil, m, k, v)
			if err != nil {
				re.input("Input.Body"+at, err)
				return payload{}, nil
			}
			p = payload{data: b, size: int64(len(b))}
		}
	}
	if typ == "" {
		return payload{}, nil
	}
	if raw && isMultipart(m) {
		if _, given := m.param("boundary"); !given {
			re.setting("Input.MediaType", errors.New("a pre-encoded multipart body needs its boundary in Input.MediaType"))
		}
	}
	h["Content-Type"], p.ctype = []string{typ}, typ
	return p, md
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
func setBody(req *http.Request, p *payload) {
	if p.size != 0 {
		req.Body = newSent(nil, p)
		if p.size > 0 {
			req.GetBody = func() (io.ReadCloser, error) { return newSent(nil, p), nil }
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
