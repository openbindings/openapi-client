package openapi_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Shared apparatus for the request-body tests: independent references for
// form bytes, a multipart reader built on mime/multipart, a
// server-sent events reader written from the HTML standard, and a server
// that reports what it has received as it arrives, for the deterministic
// streaming tests.

// formEnc is the WHATWG application/x-www-form-urlencoded byte serializer
// (URL Standard, section 1.3: "The application/x-www-form-urlencoded
// percent-encode set contains all code points, except the ASCII
// alphanumeric, U+002A (*), U+002D (-), U+002E (.), and U+005F (_)"; and
// "percent-encode after encoding": "If spaceAsPlus is true and byte is 0x20
// (SP), then append U+002B (+)"), which doc.go, Fixed rules, Form bodies,
// names: "a space as +, letters, digits and *-._ literal, every other byte
// as %XX". It is built from net/url's QueryEscape, which differs from that
// set in two bytes only: QueryEscape keeps "~", which the WHATWG set
// encodes, and encodes "*", which the WHATWG set keeps.
// TestFormEncReference checks the result against the set, byte by byte.
func formEnc(s string) string {
	q := url.QueryEscape(s)
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		switch {
		case q[i] == '~':
			b.WriteString("%7E")
		case q[i] == '%' && q[i+1:i+3] == "2A": // every "%" QueryEscape writes begins a triple
			b.WriteByte('*')
			i += 2
		default:
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

// formPairs is the WHATWG serializer's output for name, value pairs: each
// encoded by formEnc, "name=value", joined by "&" (URL Standard, section
// 5.2: "If output is not the empty string, append U+0026 (&) to output.
// Append name, followed by U+003D (=), followed by value, to output.").
func formPairs(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, formEnc(kv[i])+"="+formEnc(kv[i+1]))
	}
	return strings.Join(parts, "&")
}

// formJoin joins the non-empty serialized fields of a form body with "&".
func formJoin(fields ...string) string {
	var parts []string
	for _, f := range fields {
		if f != "" {
			parts = append(parts, f)
		}
	}
	return strings.Join(parts, "&")
}

// The reference itself, against the URL Standard's set: every byte, alone
// and between letters.
func TestFormEncReference(t *testing.T) {
	for c := range 256 {
		b := byte(c)
		var want string
		switch {
		case b == ' ':
			want = "+"
		case 'a' <= b && b <= 'z', 'A' <= b && b <= 'Z', '0' <= b && b <= '9', b == '*', b == '-', b == '.', b == '_':
			want = string(b)
		default:
			want = "%" + strings.ToUpper(hexByte(b))
		}
		if got := formEnc(string([]byte{b})); got != want {
			t.Errorf("formEnc(%q) = %q, want %q", b, got, want)
		}
		if got := formEnc("a" + string([]byte{b}) + "z"); got != "a"+want+"z" {
			t.Errorf("formEnc(a%qz) = %q", b, got)
		}
	}
	if got := formPairs("a b", "c&d", "", ""); got != "a+b=c%26d&=" {
		t.Errorf("formPairs = %q", got)
	}
}

func hexByte(b byte) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[b>>4], hex[b&15]})
}

// styledField is what RFC 6570 writes for a form field named name whose
// Encoding sets style, explode or allowReserved (doc.go, Fixed rules, Form
// bodies: "a property whose Encoding sets style, explode or allowReserved
// is written by RFC 6570, as OpenAPI says"; OAS 3.1.2 section 4.8.15.1.2,
// style: "The behavior follows the same values as query parameters ...
// the initial ? used in query strings is not used in
// application/x-www-form-urlencoded message bodies, and MUST be removed"),
// derived as styleCfg.expect derives a query parameter (styles_test.go):
// the RFC 6570 oracle for form, the OAS 3.1.2 table and text for
// spaceDelimited, pipeDelimited and deepObject, with undefined values
// settled as doc.go, Values says. A field written as nothing is omitted
// (doc.go, Values: "A form or multipart property or array item whose JSON
// data is null is omitted, whatever its serialization"; an undefined value
// is omitted as an undefined optional parameter is).
func styledField(t testing.TB, name, style string, explode, reserved bool, v any) (string, fate) {
	t.Helper()
	if v == nil {
		return "", omitted
	}
	n := jsonData(t, v)
	allow := allowU
	if reserved {
		allow = allowUR
	}
	if jUndefined(n) {
		return "", omitted
	}
	switch style {
	case "deepObject":
		return deepExpect(name, n, allow)
	case "spaceDelimited", "pipeDelimited":
		if n.kind == 's' || n.kind == 'p' {
			return "", refused
		}
		uv, err := uvalOf(n)
		switch {
		case err != nil:
			return "", refused
		case !uv.defined():
			return "", omitted
		}
		delim := "%20"
		if style == "pipeDelimited" {
			delim = "%7C"
		}
		return delimited(name, uv, delim, allow), sent
	}
	uv, err := uvalOf(n)
	switch {
	case err != nil:
		return "", refused
	case !uv.defined():
		return "", omitted
	}
	o := uops["?"]
	if allow == allowUR {
		o = uformReserved
	}
	s := strings.TrimPrefix(uexpandOp(o, uspec{name: pctName(name), explode: explode, value: uv}), "?")
	if s == "" {
		return "", omitted
	}
	return s, sent
}

// jsonPtr escapes a JSON Pointer reference token (RFC 6901 section 3: "~"
// as "~0", "/" as "~1"), as the Inputs and Settings keys for body parts
// spell a property name (errors.go, RequestError.Inputs: "Input.Body"
// followed by a JSON Pointer to the part of Body concerned).
func jsonPtr(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// quoted is s as a quoted-string with \ and " escaped, as Part's doc
// writes names and filenames: "each as a quoted-string with \ and "
// escaped".
func quoted(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// formData is the Content-Disposition of a part named name, with a
// filename when one is given (RFC 7578 section 4.2: "Each part MUST contain
// a Content-Disposition header field where the disposition type is
// "form-data". The Content-Disposition header field MUST also contain an
// additional parameter of "name""; client.go, Part: "A part's name and
// filename are written in its Content-Disposition as given, each as a
// quoted-string with \ and " escaped, never as filename*"). Every named part
// of every multipart type takes it, nested multipart included (OAS 3.1.2
// section 4.8.15.3: other multipart types may be supported "when
// Content-Disposition: form-data is used with a name parameter").
func formData(name string, filename ...string) string {
	s := "form-data; name=" + quoted(name)
	for _, f := range filename {
		s += "; filename=" + quoted(f)
	}
	return s
}

// mpart is one part of a multipart body as mime/multipart reads it back,
// with no Content-Transfer-Encoding removed (NextRawPart).
type mpart struct {
	header textproto.MIMEHeader
	body   []byte
}

// readMultipart parses a multipart body sent with Content-Type ctype and
// returns its media type, its boundary and its parts. The boundary must be
// one RFC 2046 section 5.1.1 allows: "boundary := 0*69<bchars>
// bcharsnospace", 1 to 70 characters, the last not a space.
func readMultipart(t testing.TB, ctype string, body []byte) (string, string, []mpart) {
	t.Helper()
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		t.Fatalf("Content-Type %q: %v", ctype, err)
	}
	boundary := params["boundary"]
	if !validBoundary(boundary) {
		t.Fatalf("Content-Type %q: boundary %q is not one RFC 2046 section 5.1.1 allows", ctype, boundary)
	}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	var parts []mpart
	for {
		p, err := r.NextRawPart()
		if err == io.EOF {
			return mt, boundary, parts
		}
		if err != nil {
			t.Fatalf("reading part %d of %q: %v", len(parts), body, err)
		}
		b, err := io.ReadAll(p)
		if err != nil {
			t.Fatalf("reading part %d: %v", len(parts), err)
		}
		parts = append(parts, mpart{p.Header, b})
	}
}

// validBoundary reports whether b is an RFC 2046 boundary: bchars are
// bcharsnospace / " ", and bcharsnospace := DIGIT / ALPHA / "'" / "(" /
// ")" / "+" / "_" / "," / "-" / "." / "/" / ":" / "=" / "?".
func validBoundary(b string) bool {
	if b == "" || len(b) > 70 || b[len(b)-1] == ' ' {
		return false
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || strings.IndexByte("'()+_,-./:=? ", c) >= 0) {
			return false
		}
	}
	return true
}

// wantPart is what one part must hold: its Content-Disposition and
// Content-Type exactly ("" for none), its content, and any other field
// exactly.
type wantPart struct {
	disposition string
	ctype       string
	content     string
	extra       http.Header
}

// checkParts compares parts with want, part by part: each part's header
// fields are exactly those want names.
func checkParts(t testing.TB, parts []mpart, want []wantPart) {
	t.Helper()
	if len(parts) != len(want) {
		var got []string
		for _, p := range parts {
			got = append(got, p.header.Get("Content-Disposition")+" "+p.header.Get("Content-Type")+" "+string(p.body))
		}
		t.Fatalf("%d parts %q, want %d", len(parts), got, len(want))
	}
	for i, w := range want {
		p := parts[i]
		fields := map[string][]string{}
		if w.disposition != "" {
			fields["Content-Disposition"] = []string{w.disposition}
		}
		if w.ctype != "" {
			fields["Content-Type"] = []string{w.ctype}
		}
		for k, vs := range w.extra {
			fields[textproto.CanonicalMIMEHeaderKey(k)] = vs
		}
		for k, vs := range p.header {
			if want, ok := fields[k]; !ok || !slices.Equal(vs, want) {
				t.Errorf("part %d: %s = %q, want %q", i, k, vs, want)
			}
		}
		for k, vs := range fields {
			if _, ok := p.header[k]; !ok {
				t.Errorf("part %d: no %s, want %q", i, k, vs)
			}
		}
		if string(p.body) != w.content {
			t.Errorf("part %d (%s): content %q, want %q", i, p.header.Get("Content-Disposition"), p.body, w.content)
		}
	}
}

// wantLines checks a JSON Lines body: the items, each followed by "\n",
// the last included (JSON Lines: "Line Separator is '\n'"; client.go,
// Input.Body: "a JSON Lines item is followed by LF").
func wantLines(t testing.TB, body []byte, items ...string) {
	t.Helper()
	var want strings.Builder
	for _, it := range items {
		want.WriteString(it + "\n")
	}
	if string(body) != want.String() {
		t.Errorf("body %q, want %q", body, want.String())
	}
}

// jsonSeq is a JSON text sequence of the JSON texts items (RFC 7464 section
// 2.2: "JSON-sequence = *(RS JSON-text LF)").
func jsonSeq(items ...string) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString("\x1e" + it + "\n")
	}
	return b.String()
}

// bodyServer is an httptest server that reads each request body as it
// arrives, records the bytes, and closes the channels seen returns once its
// bytes hold a marker. In answerFirst mode it answers 200 with a complete
// {} body before reading the request body (as a full-duplex handler may,
// net/http's ResponseController.EnableFullDuplex), so a client can have the
// whole response while its upload is still running. With stopAfter set, it
// answers once the bytes hold that marker and stops reading.
type bodyServer struct {
	*httptest.Server
	answerFirst bool
	stopAfter   string
	answered    chan struct{} // closed once the answer is written
	done        chan struct{} // closed once a body read ends

	mu      sync.Mutex
	got     []byte
	readErr error
	header  http.Header
	waits   []bodyWait
	once    sync.Once
	ansOnce sync.Once
}

// A bodyWait is a channel to close once the bytes received hold marker,
// or, with marker empty, number at least n bytes.
type bodyWait struct {
	marker string
	n      int
	ch     chan struct{}
}

// reached reports whether got, which held before bytes when this wait last
// looked, now satisfies it; only the new bytes and the marker's length
// before them are searched.
func (w bodyWait) reached(got []byte, before int) bool {
	if w.marker == "" {
		return len(got) >= w.n
	}
	return bytes.Contains(got[max(0, before-len(w.marker)):], []byte(w.marker))
}

func newBodyServer(t testing.TB, answerFirst bool, stopAfter string) *bodyServer {
	t.Helper()
	s := &bodyServer{answerFirst: answerFirst, stopAfter: stopAfter, answered: make(chan struct{}), done: make(chan struct{})}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *bodyServer) answer(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", "2")
	w.WriteHeader(200)
	io.WriteString(w, "{}")
	w.(http.Flusher).Flush()
	s.ansOnce.Do(func() { close(s.answered) })
}

func (s *bodyServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.header = r.Header.Clone()
	s.mu.Unlock()
	if s.answerFirst {
		http.NewResponseController(w).EnableFullDuplex()
		s.answer(w)
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Body.Read(buf)
		s.mu.Lock()
		before := len(s.got)
		s.got = append(s.got, buf[:n]...)
		for i := 0; i < len(s.waits); i++ {
			if w := s.waits[i]; w.reached(s.got, before) {
				close(s.waits[i].ch)
				s.waits = slices.Delete(s.waits, i, i+1)
				i--
			}
		}
		stop := s.stopAfter != "" && bodyWait{marker: s.stopAfter}.reached(s.got, before)
		if err != nil && err != io.EOF {
			s.readErr = err
		}
		s.mu.Unlock()
		if err != nil || stop {
			s.once.Do(func() { close(s.done) })
			break
		}
	}
	if !s.answerFirst {
		s.answer(w)
	}
}

// seen returns a channel closed once the bytes received hold marker.
func (s *bodyServer) seen(marker string) <-chan struct{} {
	ch := make(chan struct{})
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytes.Contains(s.got, []byte(marker)) {
		close(ch)
		return ch
	}
	s.waits = append(s.waits, bodyWait{marker: marker, ch: ch})
	return ch
}

// seenBytes returns a channel closed once n bytes have been received.
func (s *bodyServer) seenBytes(n int) <-chan struct{} {
	ch := make(chan struct{})
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.got) >= n {
		close(ch)
		return ch
	}
	s.waits = append(s.waits, bodyWait{n: n, ch: ch})
	return ch
}

// received returns the bytes received and the error that ended the read.
func (s *bodyServer) received() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got), s.readErr
}

// finished waits for the request body read to end and returns the bytes
// and its error, failing the test after 10 seconds.
func (s *bodyServer) finished(t testing.TB) ([]byte, error) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the server's read of the request body did not end within 10s")
	}
	return s.received()
}

// gateCtx returns a context for a streaming call, ended when the test ends
// so that nothing it starts outlives the test, and after 20 seconds.
func gateCtx(t *testing.T) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx, cancel
}

// callAsync runs Call on another goroutine; the result arrives on the
// returned channel. It lets a test wait for an event or for the call,
// whichever comes first, so a call refused early fails fast.
type callResult struct {
	resp *openapi.Response
	err  error
}

func callAsync(ctx context.Context, c *openapi.Client, key string, in *openapi.Input) <-chan callResult {
	ch := make(chan callResult, 1)
	go func() {
		resp, err := c.Call(ctx, key, in, nil)
		ch <- callResult{resp, err}
	}()
	return ch
}

// awaitOr waits for ch, failing the test if the call ends first (with the
// call's result) or after 10 seconds.
func awaitOr(t *testing.T, ch <-chan struct{}, call <-chan callResult, what string) {
	t.Helper()
	select {
	case <-ch:
	case r := <-call:
		t.Fatalf("the call returned (%v) before %s", r.err, what)
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not happen within 10s", what)
	}
}

// awaitCall waits for the call's result, failing after 10 seconds.
func awaitCall(t *testing.T, call <-chan callResult, what string) callResult {
	t.Helper()
	select {
	case r := <-call:
		return r
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return within 10s", what)
		return callResult{}
	}
}

// isRequestError reports whether err is a *RequestError.
func isRequestError(err error) bool {
	var re *openapi.RequestError
	return errors.As(err, &re)
}

// preparedBody returns the bytes a prepared request's GetBody gives,
// failing when it has none.
func preparedBody(t testing.TB, req *openapi.Request) []byte {
	t.Helper()
	if req.HTTP.GetBody == nil {
		t.Fatalf("GetBody = nil")
	}
	rc, err := req.HTTP.GetBody()
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading GetBody: %v", err)
	}
	return b
}

// lineDiff returns the lines where got and want, texts of several lines,
// differ.
func lineDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	var b strings.Builder
	for i := range max(len(g), len(w)) {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			fmt.Fprintf(&b, "got  %s\nwant %s\n", gl, wl)
		}
	}
	return b.String()
}
