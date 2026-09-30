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
	"net/http/httptrace"
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
	payload  payload
	body     io.ReadCloser // the body Prepare set in the request
	taken    atomic.Bool   // a send has read a body that can be read once
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
// the response, and the upload of the request body.
type exchange struct {
	context.Context
	cfg      *config
	op       *operation
	security string
	resp     Response
	upload
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

// An upload is the progress of a request body through the transport. Each
// replay of the body starts a new reading of it, a generation; the result
// is the final generation's, published once the round trip has returned,
// when no replay can follow.
type upload struct {
	payload payload                       // the body, when the client made it
	getBody func() (io.ReadCloser, error) // or the source of a body the caller set
	first   sentBody                      // the first generation, when the body reports its reading
	mem     bytes.Reader                  // the first generation, when net/http reads it in memory

	mu       sync.Mutex
	gen      int           // the generation that counts
	memory   *bytes.Reader // its reader, for an in-memory body
	ended    bool          // it has ended
	result   error         // how it ended
	returned bool          // the round trip has returned
	done     bool          // the result is published
	err      error
	wait     chan struct{}
}

var errClosedEarly = errors.New("openapi: the request body was closed before it was sent completely")

// end records how generation gen ended, the first time.
func (u *upload) end(gen int, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if gen == u.gen && !u.ended {
		u.ended, u.result = true, err
		u.publish()
	}
}

// newGeneration starts the reading of a replay, with its in-memory reader
// if it has one.
func (u *upload) newGeneration(memory *bytes.Reader) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.gen++
	u.ended, u.result, u.memory = false, nil, memory
	return u.gen
}

// publish publishes the result once no replay can follow; u.mu is held.
func (u *upload) publish() {
	if u.done || !u.returned || !u.ended {
		return
	}
	u.done, u.err = true, u.result
	if u.wait != nil {
		close(u.wait)
	}
}

// waitUpload waits until the upload's result is published, or ctx ends.
func (u *upload) waitUpload(ctx context.Context) error {
	u.mu.Lock()
	if u.done {
		defer u.mu.Unlock()
		return u.err
	}
	if u.wait == nil {
		u.wait = make(chan struct{})
	}
	wait := u.wait
	u.mu.Unlock()
	select {
	case <-wait:
		return u.err
	case <-ctx.Done():
		return withContext(ctx, ctx.Err())
	}
}

// attach sets req's body from p. net/http's own transport gets an
// in-memory body it writes with its header, its end taken from a trace and
// its remaining length; any other body reports its own reading.
func (x *exchange) attach(req *http.Request, p payload) {
	x.payload = p
	switch {
	case p.size == 0:
		x.ended = true
	case x.cfg.native && (p.data != nil || p.inMemory && p.size <= 4096):
		if p.data == nil {
			x.payload.data = make([]byte, p.size)
			p.ra.ReadAt(x.payload.data, p.off)
		}
		x.mem.Reset(x.payload.data)
		x.memory = &x.mem
		req.Body, req.GetBody = io.NopCloser(&x.mem), x.replayMemory
		x.Context = httptrace.WithClientTrace(x.Context, &httptrace.ClientTrace{WroteRequest: x.wroteRequest})
	default:
		x.first = sentBody{x: x, p: p}
		req.Body = &x.first
		if p.once == nil {
			req.GetBody = x.replay
		}
	}
}

// replayMemory returns an in-memory body afresh.
func (x *exchange) replayMemory() (io.ReadCloser, error) {
	r := bytes.NewReader(x.payload.data)
	x.newGeneration(r)
	return io.NopCloser(r), nil
}

// wroteRequest ends the in-memory generation the transport has written or
// stopped writing, complete when nothing of it remains.
func (x *exchange) wroteRequest(info httptrace.WroteRequestInfo) {
	x.mu.Lock()
	gen, left := x.gen, x.memory.Len()
	x.mu.Unlock()
	switch {
	case left == 0:
		x.end(gen, nil)
	case info.Err != nil:
		x.end(gen, withContext(x, info.Err))
	default:
		x.end(gen, withContext(x, errClosedEarly))
	}
}

// replay returns a body that reports its own reading afresh.
func (x *exchange) replay() (io.ReadCloser, error) {
	b := &sentBody{x: x, p: x.payload}
	if x.getBody != nil {
		rc, err := x.getBody()
		if err != nil {
			return nil, err
		}
		b = &sentBody{x: x, rc: rc}
	}
	b.gen = x.newGeneration(nil)
	return b, nil
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
	req.Header = req.Header.Clone() // the http.Client adds a jar's cookies to it
	body := req.Body
	readOnce := body != nil && body != http.NoBody && req.GetBody == nil
	if readOnce && pr.taken.Swap(true) {
		return nil, nil, &RequestError{Err: errors.New("the request body can be read only once, and was sent")}
	}
	switch {
	case body == nil || body == http.NoBody:
		x.ended = true
	case body == pr.body:
		x.attach(req, pr.payload)
	default: // a body the caller set
		x.getBody = req.GetBody
		x.first = sentBody{x: x, rc: body}
		req.Body = &x.first
		if req.GetBody != nil {
			req.GetBody = x.replay
		}
	}
	return x, req, nil
}

// send sends req and describes the response.
func (x *exchange) send(req *http.Request) (*Response, error) {
	resp, err := x.cfg.client.Do(req)
	x.mu.Lock()
	x.returned = true
	x.publish()
	x.mu.Unlock()
	if err != nil {
		return nil, withContext(x, err)
	}
	if resp.Request == nil {
		resp.Request = req
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

// decode reads r's body into out by Call's rules and closes it, waiting
// for the upload to end: before closing, after a read to the end, since a
// finite duplex peer may read the rest of the request only then, and after
// closing when the read stopped short, since closing ends the upload.
func (x *exchange) decode(r *Response, out any) error {
	var (
		cfg *config
		ctx context.Context
		u   *upload
	)
	if x != nil {
		cfg, ctx, u = x.cfg, x, &x.upload
	} else {
		cfg, ctx, u = &config{}, context.Background(), &upload{done: true}
	}
	head, eof, err := cfg.read(r.Response, r.Declaration, out)
	var upload error
	if eof {
		upload = u.waitUpload(ctx)
	}
	r.Body.Close()
	if !eof {
		upload = u.waitUpload(ctx)
	}
	if err != nil {
		return join(decodeError(r, head, withContext(ctx, err)), upload)
	}
	return upload
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

// limit returns the bound n sets: def for zero, and for a negative n one
// no body reaches.
func limit(n, def int64) int64 {
	switch {
	case n == 0:
		return def
	case n < 0:
		return 1<<63 - 2
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

// read reads the body of r, governed by decl, into out by Call's rules. It
// returns the bytes read, for a DecodeError, whether the read reached the
// end of the body, and why it failed.
func (cfg *config) read(r *http.Response, decl *Message, out any) ([]byte, bool, error) {
	if bodiless(r) {
		return nil, true, nil
	}
	n := limit(cfg.MaxBodyBytes, 64<<20)
	switch p := out.(type) {
	case nil:
		_, err := io.CopyN(io.Discard, r.Body, n)
		if err == io.EOF {
			return nil, true, nil
		}
		return nil, false, err
	case *[]byte:
		var err error
		*p, err = readAll((*p)[:0], r.Body, r.ContentLength, n)
		return *p, err == nil, err
	case io.Writer:
		_, err := io.Copy(p, r.Body)
		return nil, err == nil, err
	}
	if coding := r.Header.Get("Content-Encoding"); coding != "" && !strings.EqualFold(coding, "identity") {
		return nil, false, fmt.Errorf("cannot decode a body with Content-Encoding %q", coding)
	}
	data, err := readAll(nil, r.Body, r.ContentLength, n)
	if err != nil {
		return data, false, err
	}
	return data, true, cfg.decodeData(r.Header, decl, data, out)
}

// decodeData decodes a response body, read whole, into out: a typed
// pointer or a *any.
func (cfg *config) decodeData(h http.Header, decl *Message, data []byte, out any) error {
	ct, ok := contentType(h["Content-Type"])
	if !ok {
		ct = octetStream
	}
	cls := ct.class()
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
	text := strings.EqualFold(ct.typ, "text")
	switch p := out.(type) {
	case *string:
		if text {
			*p = string(data)
			return nil
		}
	case *any:
		switch {
		case text:
			*p = string(data)
			return nil
		case cls == otherClass || cls == xmlClass:
			if data == nil {
				data = []byte{}
			}
			*p = data
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
func decodeXML(data []byte, ct parsedMedia, out any) error {
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
// negative, reading at most bound bytes and failing past it with an
// *http.MaxBytesError. It reserves at most 1 MiB up front, whatever size
// says, and reads at most one byte past the bound. It keeps a nil dst nil
// for an empty body.
func readAll(dst []byte, r io.Reader, size, bound int64) ([]byte, error) {
	if size == 0 {
		return dst, nil
	}
	orig, start := dst, len(dst)
	if size > 0 {
		dst = slices.Grow(dst, int(min(size, bound, 1<<20))+1)
	}
	for {
		left := bound + 1 - int64(len(dst)-start) // what may still be read
		if len(dst) == cap(dst) {
			dst = slices.Grow(dst, int(min(max(512, int64(len(dst)-start)), left)))
		}
		room := dst[len(dst):cap(dst)]
		if int64(len(room)) > left {
			room = room[:left]
		}
		n, err := r.Read(room)
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
	data     []byte
	ra       io.ReaderAt
	off      int64
	size     int64 // the length, or -1 for a reader read once
	inMemory bool  // ra holds its content in memory
	once     io.Reader
}

// A sentBody is one generation of a request body that reports its reading:
// a payload, or a body the caller set, which the client closes as net/http
// would.
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
		if n, err = b.p.ra.ReadAt(buf, b.p.off+b.pos); err == io.EOF && n == len(buf) {
			err = nil
		}
	default:
		n = copy(buf, b.p.data[b.pos:])
	}
	b.pos += int64(n)
	if err != nil && b.x != nil {
		if err == io.EOF {
			b.x.end(b.gen, nil)
		} else {
			b.x.end(b.gen, withContext(b.x, err))
		}
	}
	return n, err
}

func (b *sentBody) Close() error {
	var err error
	if b.rc != nil {
		err = b.rc.Close()
	}
	if b.x != nil {
		b.x.end(b.gen, withContext(b.x, errClosedEarly))
	}
	return err
}
