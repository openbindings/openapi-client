package openapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	cfg *config
	op  *operation
	selection
	origin  *url.URL // the scheme and host of the server, when the call places credentials
	payload payload
	body    *sentBody   // the body Prepare set in the request
	taken   atomic.Bool // a send has handed over a body that can be read once
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
	cfg *config
	op  *operation
	selection
	claim *atomic.Bool // a body that can be read once, which the send that hands it over takes
	resp  Response
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

// An upload is the progress of a request body through the transport, read
// in generations: the first request's, a hop's, or a replay the transport
// takes with GetBody, each a new reading of the body. Its result is the
// last generation's that the call handed the transport, published once the
// chain has returned and the transport has closed every generation it was
// handed, so that no Read or Close of the body is then to come.
type upload struct {
	payload  payload                       // the body the client made, or the size of one the caller set
	getBody  func() (io.ReadCloser, error) // the source of a body the caller set
	first    sentBody                      // the first generation
	checking atomic.Bool                   // CheckRedirect runs: a copy it takes is not handed over

	mu       sync.Mutex
	gens     []*sentBody // the generations handed to the transport
	open     int         // those that have not ended
	returned bool        // the chain has returned, so that no generation follows
	done     bool        // the result is published
	err      error
	wait     chan struct{}
}

var errClosedEarly = errors.New("openapi: the request body was closed before it was sent completely")

// hand records that b goes to the transport, as the last generation.
func (u *upload) hand(b *sentBody) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if b.handed || u.checking.Load() {
		return
	}
	b.handed, u.gens = true, append(u.gens, b)
	if !b.ended {
		u.open++
	}
}

// end ends b, the first time, with how its reading finished, or as closed
// early; u.mu is held.
func (u *upload) end(b *sentBody) {
	if b.ended {
		return
	}
	if b.ended = true; !b.finished {
		b.result = withContext(b.x, errClosedEarly)
	}
	if b.handed {
		u.open--
		u.publish()
	}
}

// publish publishes the result once the chain has returned and every
// generation has ended; u.mu is held.
func (u *upload) publish() {
	if u.done || !u.returned || u.open > 0 {
		return
	}
	u.done = true
	if len(u.gens) > 0 {
		u.err = u.gens[len(u.gens)-1].result
	}
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

// settle waits for the upload before Call returns: for its result, or, once
// the call's context is done, until every generation the transport was
// handed has ended, the client closing those the transport has not, so that
// Call never returns while its body is read or its iterator runs. A
// generation closed during a Read ends as the Read returns.
func (x *exchange) settle() error {
	err := x.waitUpload(x)
	x.mu.Lock()
	var open []*sentBody
	for _, b := range x.gens {
		if !x.done && !b.closed {
			open = append(open, b)
		}
	}
	x.mu.Unlock()
	if open == nil {
		return err
	}
	for _, b := range open {
		b.Close()
	}
	return x.waitUpload(context.Background())
}

// attach sets req's body to one that reports its reading of p.
func (x *exchange) attach(req *http.Request, p payload) {
	x.payload = p
	if p.size == 0 {
		return
	}
	x.first = sentBody{x: x, p: &x.payload, src: x.payload.source()}
	req.Body = &x.first
	if p.size >= 0 {
		req.GetBody = x.replay
	}
}

// replay is the GetBody of a request the client sends: a new generation,
// handed to the transport that calls it.
func (x *exchange) replay() (io.ReadCloser, error) {
	b, err := x.newBody()
	if err != nil {
		return nil, err
	}
	x.hand(b)
	return b, nil
}

// newBody returns a new generation of the body, not yet handed over.
func (x *exchange) newBody() (*sentBody, error) {
	b := newSent(x, &x.payload)
	if x.getBody != nil {
		rc, err := x.getBody()
		if err != nil {
			return nil, err
		}
		b.rc = rc
	}
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
	x := &exchange{Context: ctx, cfg: pr.cfg, op: pr.op, selection: pr.selection}
	req := r.HTTP.WithContext(x)
	if x.places && (req.URL == nil || !sameOrigin(req.URL, pr.origin)) {
		return nil, nil, &RequestError{Err: errOtherOrigin}
	}
	body := req.Body
	if body != nil && body != http.NoBody && (body != pr.body || req.GetBody == nil) {
		x.claim = &pr.taken // HTTP.Body goes once; a body the caller set and GetBody give later sends theirs
	}
	x.payload = pr.payload
	switch {
	case body == nil || body == http.NoBody:
	case body == pr.body:
		x.attach(req, pr.payload)
	default: // a body the caller set, of the length it declares, if any
		x.getBody, x.payload = req.GetBody, payload{size: -1, ctype: pr.payload.ctype}
		if req.ContentLength > 0 {
			x.payload.size = req.ContentLength
		}
		x.first = sentBody{x: x, p: &x.payload, rc: body}
		req.Body = &x.first
		if req.GetBody != nil {
			req.GetBody = x.replay
		}
	}
	return x, req, nil
}

// send sends req as sign makes it, following redirects as
// Options.Redirects says, and describes the last response. A response that
// arrived comes with any error that ended the chain.
func (x *exchange) send(req *http.Request) (*Response, error) {
	var re RequestError
	signed := x.sign(req, x.Context, true, &re)
	if err := re.refused(); err != nil {
		return nil, err
	}
	if x.claim != nil && x.claim.Swap(true) { // HTTP.Body went with an earlier send
		if x.getBody == nil {
			return nil, &RequestError{Err: errors.New("the request body can be read only once, and was sent")}
		}
		rc, err := x.getBody()
		if err != nil {
			return nil, &RequestError{Err: fmt.Errorf("the request body's GetBody: %w", err)}
		}
		x.first.rc = rc
	}
	if x.first.p != nil {
		x.hand(&x.first)
	}
	resp, err := x.follow(req, signed)
	x.mu.Lock()
	x.returned = true
	x.publish()
	x.mu.Unlock()
	if resp == nil {
		return nil, withContext(x, err)
	}
	r := &x.resp
	r.Response, r.Security = resp, x.key()
	if d := x.op.declaration(resp.StatusCode); d != nil {
		r.Declaration = d.Message
		if ct, ok := contentType(resp.Header["Content-Type"]); ok {
			r.Media = match(d.media, d.Message.Media, ct)
		}
	}
	return r, withContext(x, err)
}

// call sends req as send does and applies Call's policy to the response.
// Once the request has gone to the transport, an error waits for the
// upload too, so that the body is not read after Call returns.
func (x *exchange) call(req *http.Request, out any) (*Response, error) {
	resp, err := x.send(req)
	if err != nil {
		if x.returned {
			x.settle()
		}
		return resp, err
	}
	return resp, x.finish(resp, out)
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
	return join(se, x.settle())
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
	cfg, ctx := &config{}, context.Context(context.Background())
	if x != nil {
		cfg, ctx = x.cfg, x
	}
	head, eof, err := cfg.read(r.Response, r.Declaration, out)
	var upload error
	if eof && x != nil {
		upload = x.settle()
	}
	r.Body.Close()
	if !eof && x != nil {
		upload = x.settle()
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
		return decoded(codec.Decode(bytes.NewReader(data), out))
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
		return decoded(json.Unmarshal(data, out))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return decoded(err)
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
	return decoded(dec.Decode(out))
}

// A decoderError is a decoder's own error, whose message can quote the
// body, and so is not in the text.
type decoderError struct{ err error }

func (e *decoderError) Error() string { return "the body does not decode into the value" }
func (e *decoderError) Unwrap() error { return e.err }

// decoded returns err, a decoder's, as a *decoderError.
func decoded(err error) error {
	if err == nil {
		return nil
	}
	return &decoderError{err}
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
// read again from where it stood, a reader read once, or, for a body of
// several sources, its parts in order or an iterator's items, encoded as
// they are read; and the Content-Type the client wrote for it.
type payload struct {
	data  []byte
	ra    io.ReaderAt
	off   int64
	size  int64 // the length, or -1 when the body can be read only once
	once  io.Reader
	ctype string

	parts []payload // the sources of a body of several, in order
	items *items    // an iterator's items
	form  bool      // as a part, encoded as a form field is, as it is read
	check []string  // as a part, what its content cannot hold, found as it is read
}

// source returns what a generation of p reads, when p has several sources.
func (p *payload) source() io.Reader {
	switch {
	case p.items != nil:
		return p.items
	case p.parts != nil:
		return &cursor{parts: p.parts, tail: []byte(lead(p.parts[0].check))}
	}
	return nil
}

// readAt reads p's one source from pos: its bytes, its reader at p.off+pos,
// or its reader read once.
func (p *payload) readAt(buf []byte, pos int64) (int, error) {
	switch {
	case p.once != nil:
		return p.once.Read(buf)
	case pos >= p.size:
		return 0, io.EOF
	case p.ra != nil:
		return p.ra.ReadAt(buf[:min(int64(len(buf)), p.size-pos)], p.off+pos)
	}
	return copy(buf, p.data[pos:p.size]), nil
}

// A sentBody is one generation of a request body: a payload, or a body the
// caller set, which the client closes as net/http would. One the client
// tracks reports to the upload: the first Read that returns an error, io.EOF
// included, says how its reading finished, and no later Read reaches the
// reader; it ends at the transport's Close, or, when a Read is then in
// flight, as that Read returns. A Read once the call's context is done
// fails without reaching it.
type sentBody struct {
	x   *exchange // tracks the upload, or nil
	p   *payload
	pos int64
	rc  io.ReadCloser // read instead of p when set, p.size being its declared length or -1
	src io.Reader     // read instead of p when p has several sources

	// Guarded by x.mu.
	handed, reading, finished, closed, ended bool
	result                                   error // how its reading finished: nil at io.EOF
}

// newSent returns a generation of p, tracked by x if not nil.
func newSent(x *exchange, p *payload) *sentBody { return &sentBody{x: x, p: p, src: p.source()} }

func (b *sentBody) Read(buf []byte) (int, error) {
	if b.x == nil {
		return b.read(buf)
	}
	u := &b.x.upload
	u.mu.Lock()
	if err := b.x.Err(); err != nil && !b.closed && !b.finished {
		b.finished, b.result = true, withContext(b.x, err)
	}
	if b.closed || b.finished {
		err := b.result
		if b.closed {
			err = errClosedEarly
		}
		u.mu.Unlock()
		return 0, cmp.Or(err, io.EOF)
	}
	b.reading = true
	u.mu.Unlock()
	n, err := b.read(buf)
	u.mu.Lock()
	if b.reading = false; err != nil {
		b.finished = true
		if err != io.EOF {
			b.result = withContext(b.x, err)
		}
	}
	if b.closed {
		u.end(b)
	}
	u.mu.Unlock()
	return n, err
}

// read reads the payload, or the caller's body, a body of known length
// ending at its length and failing short of it.
func (b *sentBody) read(buf []byte) (n int, err error) {
	p := b.p
	switch {
	case b.rc != nil:
		n, err = b.rc.Read(buf)
	case b.src != nil:
		n, err = b.src.Read(buf)
	default:
		n, err = p.readAt(buf, b.pos)
	}
	b.pos += int64(n)
	if p.size >= 0 {
		switch {
		case err == nil && b.pos >= p.size:
			err = io.EOF
		case err == io.EOF && b.pos < p.size:
			err = io.ErrUnexpectedEOF
		}
	}
	return n, err
}

// Close closes the caller's body, stops an iterator once no Read of it is
// in flight, and ends the generation.
func (b *sentBody) Close() error {
	var err error
	if b.rc != nil {
		err = b.rc.Close()
	}
	if c, ok := b.src.(io.Closer); ok {
		c.Close()
	}
	if b.x != nil {
		u := &b.x.upload
		u.mu.Lock()
		if b.closed = true; !b.reading {
			u.end(b)
		}
		u.mu.Unlock()
	}
	return err
}

// A cursor reads the parts of a body in order.
type cursor struct {
	parts []payload
	pos   int64  // read of parts[0]
	tail  []byte // the end of what parts[0] has given, for its check
	pend  []byte // what a form part has encoded, not yet read
	raw   []byte // what a form part has given, to be encoded
}

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
			return 0, errHolds(p.check)
		}
		next := err == io.EOF || p.size >= 0 && c.pos >= p.size
		if next {
			c.parts, c.pos, err = c.parts[1:], 0, nil
			if len(c.parts) > 0 {
				c.tail = append(c.tail[:0], lead(c.parts[0].check)...)
			}
		}
		switch {
		case p.form:
			c.pend = appendForm(c.pend[:0], dst[:n])
		case n > 0 || err != nil || !next:
			return n, err
		}
		if err != nil {
			return 0, err
		}
	}
}

// holds reports whether data, read after the tail, holds one of patterns,
// and keeps the end of what has been read as the tail, as long as the
// longest of them but one byte.
func (c *cursor) holds(patterns []string, data []byte) bool {
	c.tail = append(c.tail, data...)
	found, keep := false, 0
	for _, s := range patterns {
		found = found || bytes.Contains(c.tail, []byte(s))
		keep = max(keep, len(s)-1)
	}
	c.tail = append(c.tail[:0], c.tail[max(0, len(c.tail)-keep):]...)
	return found
}

// lead returns what precedes a part's content, for its check: the CRLF
// before a multipart part's content, kept as a tail is, or nothing.
func lead(patterns []string) string {
	keep := 0
	for _, s := range patterns {
		keep = max(keep, len(s)-1)
	}
	return "\r\n"[2-min(2, keep):]
}

// errHolds is why a part's content, read, ends the body.
func errHolds(patterns []string) error {
	if len(patterns[0]) == 1 {
		return errSeparator
	}
	return errDelimiter
}
