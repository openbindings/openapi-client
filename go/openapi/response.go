package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

type (
	operationKey struct{}
	exchangeKey  struct{}
	preparedKey  struct{}
)

// A prepared is the context of a request Prepare built.
type prepared struct {
	context.Context
	cfg      *config
	op       *operation
	security string
	taken    atomic.Bool // a send has taken Request.HTTP.Body
}

func (p *prepared) Value(key any) any {
	switch key.(type) {
	case operationKey:
		return p.op
	case preparedKey:
		return p
	}
	return p.Context.Value(key)
}

// An exchange is one send of a request: the context the request carries,
// and the progress of its body, which WaitRequest reports.
type exchange struct {
	context.Context
	cfg      *config
	op       *operation
	security string
	payload  payload                       // the body, for a request Call built
	getBody  func() (io.ReadCloser, error) // or the source of a prepared request's body
	first    sentBody                      // the body as the transport first reads it
	resp     Response                      // the response, once it arrives

	mu       sync.Mutex
	gen      int  // which reading of the body counts, one more for each replay
	closed   bool // that reading was closed before its end
	returned bool // the round trip returned, so no replay follows
	done     bool
	err      error
	wait     chan struct{}
}

func (x *exchange) Value(key any) any {
	switch key.(type) {
	case operationKey:
		return x.op
	case exchangeKey:
		return x
	}
	return x.Context.Value(key)
}

// exchangeOf returns the exchange a response answers, or nil.
func exchangeOf(r *http.Response) *exchange {
	if r == nil || r.Request == nil {
		return nil
	}
	x, _ := r.Request.Context().Value(exchangeKey{}).(*exchange)
	return x
}

// attach sets the body of a request Call built.
func (x *exchange) attach(req *http.Request, p payload) {
	x.payload = p
	if p.size == 0 {
		x.done = true
		return
	}
	x.first = sentBody{x: x, p: p}
	req.Body = &x.first
	if p.once == nil {
		req.GetBody = x.replay
	}
}

// newExchange starts a send of the prepared request r.
func (r *Request) newExchange(ctx context.Context) (*exchange, *http.Request, error) {
	var pr *prepared
	if r.HTTP != nil {
		pr, _ = r.HTTP.Context().Value(preparedKey{}).(*prepared)
	}
	if pr == nil {
		return nil, nil, &RequestError{Err: fmt.Errorf("%w: the Request was not made by Prepare", ErrNoOperation)}
	}
	x := &exchange{Context: ctx, cfg: pr.cfg, op: pr.op, security: pr.security}
	req := r.HTTP.WithContext(x)
	req.Header = req.Header.Clone() // the http.Client adds a Jar's cookies to it
	body := req.Body
	if body == nil || body == http.NoBody {
		x.done = true
		return x, req, nil
	}
	if pr.taken.Swap(true) {
		if req.GetBody == nil {
			return nil, nil, &RequestError{Err: errors.New("the request body can be read only once, and was sent")}
		}
		var err error
		if body, err = req.GetBody(); err != nil {
			return nil, nil, &RequestError{Err: err}
		}
	}
	x.getBody = req.GetBody
	x.first = sentBody{x: x, rc: body}
	req.Body = &x.first
	if req.GetBody != nil {
		req.GetBody = x.replay
	}
	return x, req, nil
}

// replay returns the body afresh, for a transport that sends it again.
func (x *exchange) replay() (io.ReadCloser, error) {
	b := &sentBody{x: x, p: x.payload}
	if x.getBody != nil {
		rc, err := x.getBody()
		if err != nil {
			return nil, err
		}
		b = &sentBody{x: x, rc: rc}
	}
	x.mu.Lock()
	x.gen++
	x.closed = false
	b.gen = x.gen
	x.mu.Unlock()
	return b, nil
}

// send sends req and describes the response.
func (x *exchange) send(req *http.Request) (*Response, error) {
	resp, err := x.cfg.client.Do(req)
	x.mu.Lock()
	x.returned = true
	if x.closed {
		x.end(x.closedEarly())
	}
	x.mu.Unlock()
	if err != nil {
		return nil, withContext(x, err)
	}
	r := &x.resp
	r.Response, r.Security = resp, x.security
	if d := x.op.declaration(resp.StatusCode); d != nil {
		r.Declaration = d.Message
		if ct, ok := contentType(resp.Header["Content-Type"]); ok {
			r.Media = match(d.media, d.Message.Media, ct)
		}
	}
	return r, nil
}

// finish applies Call's policy to r: a StatusError for a final status other
// than 2xx, else the body decoded into out.
func (x *exchange) finish(r *Response, out any) error {
	if r.StatusCode/100 == 2 {
		return x.decode(r, out)
	}
	se := &StatusError{Response: r}
	if !bodiless(r.Response) {
		se.Content, se.Err = readAll(nil, r.Body, r.ContentLength, limit(x.cfg.MaxErrorBytes, 1<<20))
		se.Err = withContext(x, se.Err)
	}
	r.Body.Close()
	se.Response = r.keep(se.Content, se.Err != nil)
	return join(se, x.waitUpload(x))
}

// keep makes r's Body read content, in place of the body it has read, and
// returns a copy of r whose Body reads content too. When cut, content is
// incomplete and becomes the ContentLength.
func (r *Response) keep(content []byte, cut bool) *Response {
	if cut {
		r.ContentLength = int64(len(content))
	}
	r.Body = io.NopCloser(bytes.NewReader(content))
	c, hr := *r, *r.Response
	hr.Body = io.NopCloser(bytes.NewReader(content))
	c.Response = &hr
	return &c
}

// decode reads r's body into out by Call's rules, waits for the upload to
// end, and closes the body.
func (x *exchange) decode(r *Response, out any) error {
	cfg, ctx := &config{}, context.Background()
	if x != nil {
		cfg, ctx = x.cfg, x
	}
	head, err := cfg.read(r.Response, r.Declaration, out)
	var upload error
	if err == nil {
		upload = x.waitUpload(ctx)
	}
	r.Body.Close()
	if err == nil {
		return upload
	}
	return join(decodeError(r, head, withContext(ctx, err)), x.waitUpload(ctx))
}

// decodeError describes a body r could not decode, head being its start.
func decodeError(r *Response, head []byte, err error) *DecodeError {
	head = bytes.Clone(head[:min(len(head), 4096)])
	return &DecodeError{Response: r.keep(head, true), Content: head, Err: err}
}

func join(err, also error) error {
	if also == nil {
		return err
	}
	return errors.Join(err, also)
}

// limit returns the bound n sets: def for zero, none for a negative n.
func limit(n, def int64) int64 {
	if n == 0 {
		return def
	}
	return n
}

// bodiless reports whether r has no body to read.
func bodiless(r *http.Response) bool {
	s, m := r.StatusCode, ""
	if r.Request != nil {
		m = r.Request.Method
	}
	return s/100 == 1 || s == 204 || s == 205 || s == 304 || m == "HEAD" || m == "CONNECT" && s/100 == 2
}

// read reads the body of r, governed by decl, into out by Call's rules,
// returning the bytes read for a DecodeError with the reason it failed.
func (cfg *config) read(r *http.Response, decl *Message, out any) ([]byte, error) {
	if bodiless(r) {
		return nil, nil
	}
	n := limit(cfg.MaxBodyBytes, 64<<20)
	switch p := out.(type) {
	case nil:
		if n < 0 {
			_, err := io.Copy(io.Discard, r.Body)
			return nil, err
		}
		if _, err := io.CopyN(io.Discard, r.Body, n); err != io.EOF {
			return nil, err
		}
		return nil, nil
	case *[]byte:
		var err error
		*p, err = readAll((*p)[:0], r.Body, r.ContentLength, n)
		return *p, err
	case io.Writer:
		_, err := io.Copy(p, r.Body)
		return nil, err
	}
	if coding := r.Header.Get("Content-Encoding"); coding != "" && !strings.EqualFold(coding, "identity") {
		return nil, fmt.Errorf("cannot decode a body with Content-Encoding %s", coding)
	}
	data, err := readAll(nil, r.Body, r.ContentLength, n)
	if err != nil {
		return data, err
	}
	return data, cfg.decodeData(r.Header, decl, data, out)
}

// decodeData decodes a response body, read whole, into out: a typed
// pointer or a *any.
func (cfg *config) decodeData(h http.Header, decl *Message, data []byte, out any) error {
	ct, ok := contentType(h["Content-Type"])
	if !ok {
		ct = octetStream
	}
	cls := ct.class()
	isText := cls == textClass || cls == sequentialClass && strings.EqualFold(ct.typ, "text")
	if len(data) == 0 && (cls == jsonClass || cls == xmlClass) {
		if decl != nil && len(decl.Media) > 0 {
			return io.EOF
		}
		return nil
	}
	if codec, _ := cfg.codec(ct); codec != nil {
		if len(data) == 0 {
			return nil
		}
		return codec.Decode(bytes.NewReader(data), out)
	}
	switch p := out.(type) {
	case *string:
		if isText {
			*p = string(data)
			return nil
		}
	case *any:
		switch {
		case isText:
			*p = string(data)
			return nil
		case cls == otherClass || cls == xmlClass:
			*p = append([]byte{}, data...)
			return nil
		}
	}
	switch {
	case cls == jsonClass:
		return decodeJSON(data, out)
	case cls == xmlClass:
		return decodeXML(data, ct, out)
	case cls == sequentialClass && len(data) == 0:
		return json.Unmarshal([]byte("[]"), out)
	case cls == sequentialClass:
		return notYet("decoding " + ct.full)
	case len(data) == 0:
		return nil
	}
	return fmt.Errorf("cannot decode %s into %T", ct.full, out)
}

// decodeJSON decodes data, a JSON value followed only by whitespace, into
// out, keeping numbers exact where the codec creates the values.
func decodeJSON(data []byte, out any) error {
	switch out.(type) {
	case *any, *map[string]any, *[]any:
	default:
		return json.Unmarshal(data, out)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if len(bytes.TrimLeft(data[dec.InputOffset():], " \t\r\n")) > 0 {
		return errors.New("invalid data after the JSON value")
	}
	return nil
}

// decodeXML decodes data into out, reading UTF-8, US-ASCII and ISO-8859-1
// by its byte order mark, else the Content-Type's charset, else its own
// declaration.
func decodeXML(data []byte, ct media, out any) error {
	charset, given := ct.param("charset")
	if b, ok := bytes.CutPrefix(data, []byte("\xEF\xBB\xBF")); ok {
		data, charset, given = b, "utf-8", true
	}
	latin1 := func(label string) bool {
		return strings.EqualFold(label, "iso-8859-1") || strings.EqualFold(label, "latin1")
	}
	supported := func(label string) bool {
		return latin1(label) || strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii")
	}
	if given && !supported(charset) {
		return fmt.Errorf("unsupported charset %q", charset)
	}
	if given && latin1(charset) {
		data = fromLatin1(data)
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = func(label string, r io.Reader) (io.Reader, error) {
		switch {
		case given: // the byte order mark or the Content-Type decided
			return r, nil
		case !supported(label):
			return nil, fmt.Errorf("unsupported charset %q", label)
		case latin1(label):
			b, err := io.ReadAll(r)
			return bytes.NewReader(fromLatin1(b)), err
		}
		return r, nil
	}
	return dec.Decode(out)
}

func fromLatin1(b []byte) []byte {
	s := make([]byte, 0, len(b))
	for _, c := range b {
		s = utf8.AppendRune(s, rune(c))
	}
	return s
}

// readAll appends r's content to dst, size bytes when size is not
// negative, reading at most max bytes (any number when max is negative)
// and failing past it with an *http.MaxBytesError. It keeps a nil dst nil
// for an empty body.
func readAll(dst []byte, r io.Reader, size, bound int64) ([]byte, error) {
	if size == 0 {
		return dst, nil
	}
	if bound < 0 {
		bound = 1<<63 - 2
	}
	orig, start := dst, len(dst)
	if size > 0 && size <= bound {
		dst = slices.Grow(dst, int(size)+1)
	}
	for {
		if len(dst) == cap(dst) {
			dst = slices.Grow(dst, max(512, len(dst)-start))
		}
		n, err := r.Read(dst[len(dst):cap(dst)])
		dst = dst[:len(dst)+n]
		if int64(len(dst)-start) > bound {
			return dst[:start+int(bound)], &http.MaxBytesError{Limit: bound}
		}
		switch {
		case err == io.EOF && orig == nil && len(dst) == 0:
			return nil, nil
		case err == io.EOF:
			return dst, nil
		case err != nil:
			return dst, err
		}
	}
}

// withContext makes err, from a call whose context is done, match the
// context's error and its cause.
func withContext(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}
	var also []error
	for _, e := range []error{ctx.Err(), context.Cause(ctx)} {
		if !errors.Is(err, e) && !slices.Contains(also, e) {
			also = append(also, e)
		}
	}
	if also == nil {
		return err
	}
	return &contextError{err, also}
}

type contextError struct {
	err  error
	also []error
}

func (e *contextError) Error() string   { return e.err.Error() }
func (e *contextError) Unwrap() []error { return append([]error{e.err}, e.also...) }

// A payload is the content of a request body: bytes, a reader that can be
// read again from where it stood, or a reader that can be read once.
type payload struct {
	data []byte
	ra   io.ReaderAt
	off  int64
	size int64 // the length, or -1 for a reader read once
	once io.Reader
}

// A sentBody is one reading of a request body by the transport: of a
// payload, or of the body a prepared request holds, which the client does
// not close when it is a reader the caller gave.
type sentBody struct {
	x   *exchange // tracks the upload, or nil
	gen int
	p   payload
	pos int64
	rc  io.ReadCloser // read instead of p when set
}

func (b *sentBody) Read(buf []byte) (n int, err error) {
	switch {
	case b.rc != nil:
		n, err = b.rc.Read(buf)
	case b.p.once != nil:
		n, err = b.p.once.Read(buf)
	case b.pos >= b.p.size:
		err = io.EOF
	case b.p.ra != nil:
		buf = buf[:min(int64(len(buf)), b.p.size-b.pos)]
		n, err = b.p.ra.ReadAt(buf, b.p.off+b.pos)
		if err == io.EOF && n == len(buf) {
			err = nil
		}
	default:
		n = copy(buf, b.p.data[b.pos:])
	}
	b.pos += int64(n)
	if err != nil && b.x != nil {
		b.x.mu.Lock()
		if b.gen == b.x.gen {
			if err == io.EOF {
				b.x.end(nil)
			} else {
				b.x.end(withContext(b.x, err))
			}
		}
		b.x.mu.Unlock()
	}
	return n, err
}

func (b *sentBody) Close() error {
	var err error
	if b.rc != nil {
		err = b.rc.Close()
	}
	if x := b.x; x != nil {
		x.mu.Lock()
		if b.gen == x.gen && !x.done {
			x.closed = true
			if x.returned {
				x.end(x.closedEarly())
			}
		}
		x.mu.Unlock()
	}
	return err
}

// end records how the upload ended; x.mu is held.
func (x *exchange) end(err error) {
	if x.done {
		return
	}
	x.done, x.err = true, err
	if x.wait != nil {
		close(x.wait)
	}
}

func (x *exchange) closedEarly() error {
	return withContext(x, errors.New("openapi: the request body was closed before it was sent completely"))
}

// waitUpload waits until the upload ends, or ctx does.
func (x *exchange) waitUpload(ctx context.Context) error {
	if x == nil {
		return nil
	}
	x.mu.Lock()
	if x.done {
		defer x.mu.Unlock()
		return x.err
	}
	if x.wait == nil {
		x.wait = make(chan struct{})
	}
	wait := x.wait
	x.mu.Unlock()
	select {
	case <-wait:
		return x.err
	case <-ctx.Done():
		return withContext(ctx, ctx.Err())
	}
}
