package openapi_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// This file holds the shared test apparatus: a recording httptest server,
// document builders, and assertions on the public error types. Every test in
// the package uses only the exported API.

// rec is one request as the test server received it.
type rec struct {
	Method           string
	RequestURI       string // the request target exactly as sent
	Path             string // r.URL.EscapedPath()
	RawQuery         string
	Header           http.Header
	Body             []byte
	BodyErr          error
	ContentLength    int64
	TransferEncoding []string
	Host             string
}

// wire is an httptest server that records every request it receives, reads
// its body to the end, and then answers with the answer handler, whose
// request body replays the bytes read. A nil answer is 200 with an empty
// JSON object.
type wire struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []rec
	answer http.HandlerFunc
}

func newWire(t testing.TB, answer http.HandlerFunc) *wire {
	t.Helper()
	w := &wire{answer: answer}
	w.Server = httptest.NewServer(http.HandlerFunc(w.serve))
	t.Cleanup(w.Close)
	return w
}

func (w *wire) serve(rw http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	w.mu.Lock()
	w.reqs = append(w.reqs, rec{
		Method:           r.Method,
		RequestURI:       r.RequestURI,
		Path:             r.URL.EscapedPath(),
		RawQuery:         r.URL.RawQuery,
		Header:           r.Header.Clone(),
		Body:             body,
		BodyErr:          err,
		ContentLength:    r.ContentLength,
		TransferEncoding: slices.Clone(r.TransferEncoding),
		Host:             r.Host,
	})
	answer := w.answer
	w.mu.Unlock()
	if answer == nil {
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, "{}")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	answer(rw, r)
}

func (w *wire) setAnswer(a http.HandlerFunc) {
	w.mu.Lock()
	w.answer = a
	w.mu.Unlock()
}

func (w *wire) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.reqs)
}

func (w *wire) requests() []rec {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.reqs)
}

// only returns the single request the server received, failing otherwise.
func (w *wire) only(t testing.TB) rec {
	t.Helper()
	reqs := w.requests()
	if len(reqs) != 1 {
		t.Fatalf("server received %d requests, want 1", len(reqs))
	}
	return reqs[0]
}

// last returns the most recent request, failing when there is none.
func (w *wire) last(t testing.TB) rec {
	t.Helper()
	reqs := w.requests()
	if len(reqs) == 0 {
		t.Fatalf("server received no request")
	}
	return reqs[len(reqs)-1]
}

// nothingSent fails when the server received any request.
func (w *wire) nothingSent(t testing.TB) {
	t.Helper()
	if n := w.count(); n != 0 {
		t.Fatalf("server received %d requests, want none", n)
	}
}

// hostport is the server's host and port, without a scheme.
func (w *wire) hostport() string {
	u, _ := url.Parse(w.URL)
	return u.Host
}

// jsonAnswer answers with status and a JSON body.
func jsonAnswer(status int, body string) http.HandlerFunc {
	return typedAnswer(status, "application/json", body)
}

// typedAnswer answers with status, a Content-Type (none when ct is empty,
// suppressing net/http's sniffing) and body.
func typedAnswer(status int, ct, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ct == "" {
			w.Header()["Content-Type"] = nil
		} else {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// testDocURI names documents parsed by tests that send nothing.
const testDocURI = "https://api.example.test/openapi.json"

// doc31 builds an OpenAPI 3.1.0 document whose sole root server is @BASE@,
// with paths as the members of its Paths Object and extra as further root
// members, each a complete "name": value pair.
func doc31(paths string, extra ...string) string {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{`)
	b.WriteString(paths)
	b.WriteString("}")
	for _, e := range extra {
		b.WriteString(",")
		b.WriteString(e)
	}
	b.WriteString("}")
	return b.String()
}

// bare31 builds an OpenAPI 3.1.0 document with no servers.
func bare31(paths string, extra ...string) string {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{`)
	b.WriteString(paths)
	b.WriteString("}")
	for _, e := range extra {
		b.WriteString(",")
		b.WriteString(e)
	}
	b.WriteString("}")
	return b.String()
}

// expand replaces @BASE@ with base and @HOSTPORT@ with base's host and port.
func expand(doc, base string) string {
	doc = strings.ReplaceAll(doc, "@BASE@", base)
	if u, err := url.Parse(base); err == nil {
		doc = strings.ReplaceAll(doc, "@HOSTPORT@", u.Host)
	}
	return doc
}

// parseAt parses doc, expanded against base, as if retrieved from docURI.
func parseAt(t testing.TB, doc, base, docURI string, opts *openapi.Options) *openapi.Client {
	t.Helper()
	c, err := openapi.Parse(context.Background(), []byte(expand(doc, base)), docURI, opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c == nil {
		t.Fatalf("Parse returned a nil Client and no error")
	}
	return c
}

// parseFor parses doc against the wire server w, as if retrieved from
// w.URL + "/openapi.json".
func parseFor(t testing.TB, w *wire, doc string, opts *openapi.Options) *openapi.Client {
	t.Helper()
	return parseAt(t, doc, w.URL, w.URL+"/openapi.json", opts)
}

// parseErr parses doc against base and returns the error, failing when
// parsing succeeds.
func parseErr(t testing.TB, doc, base string, opts *openapi.Options) error {
	t.Helper()
	c, err := openapi.Parse(context.Background(), []byte(expand(doc, base)), base+"/openapi.json", opts)
	if err == nil {
		t.Fatalf("Parse succeeded, want an error")
	}
	if c != nil {
		t.Fatalf("Parse returned a Client with its error %v", err)
	}
	return err
}

// asRequestError returns err as a *RequestError, failing when it is not one.
func asRequestError(t testing.TB, err error) *openapi.RequestError {
	t.Helper()
	var re *openapi.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("error %v (%T) is not a *openapi.RequestError", err, err)
	}
	return re
}

// wantKeys fails unless every key is present in m. With exact, m must hold
// exactly those keys.
func wantKeys(t testing.TB, field string, m map[string]error, exact bool, keys ...string) {
	t.Helper()
	for _, k := range keys {
		e, ok := m[k]
		if !ok {
			t.Errorf("%s has no key %q; keys %q", field, k, sortedKeys(m))
			continue
		}
		if e == nil {
			t.Errorf("%s[%q] is a nil error", field, k)
		}
	}
	if exact && len(m) != len(keys) {
		t.Errorf("%s keys %q, want exactly %q", field, sortedKeys(m), keys)
	}
}

// wantAnyKey fails unless m holds at least one of keys.
func wantAnyKey(t testing.TB, field string, m map[string]error, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return
		}
	}
	t.Errorf("%s keys %q, want one of %q", field, sortedKeys(m), keys)
}

func sortedKeys(m map[string]error) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// refusedBeforeSending asserts that err is a *RequestError, that resp is nil,
// and that the server received nothing.
func refusedBeforeSending(t testing.TB, w *wire, resp *openapi.Response, err error) *openapi.RequestError {
	t.Helper()
	re := asRequestError(t, err)
	if resp != nil {
		t.Errorf("a refused call returned a Response")
	}
	if w != nil {
		w.nothingSent(t)
	}
	return re
}

// mustOp returns the operation named key.
func mustOp(t testing.TB, c *openapi.Client, key string) *openapi.Operation {
	t.Helper()
	op, err := c.Operation(key)
	if err != nil {
		t.Fatalf("Operation(%q): %v", key, err)
	}
	if op == nil {
		t.Fatalf("Operation(%q) returned nil and no error", key)
	}
	return op
}

// mustCall calls key and fails on any error.
func mustCall(t testing.TB, c *openapi.Client, key string, in *openapi.Input, out any) *openapi.Response {
	t.Helper()
	resp, err := c.Call(t.Context(), key, in, out)
	if err != nil {
		t.Fatalf("Call(%q): %v", key, err)
	}
	if resp == nil {
		t.Fatalf("Call(%q) returned a nil Response and no error", key)
	}
	return resp
}

// mustPrepare prepares key and fails on any error.
func mustPrepare(t testing.TB, c *openapi.Client, key string, in *openapi.Input) *openapi.Request {
	t.Helper()
	req, err := c.Prepare(key, in)
	if err != nil {
		t.Fatalf("Prepare(%q): %v", key, err)
	}
	if req == nil || req.HTTP == nil {
		t.Fatalf("Prepare(%q) returned no request", key)
	}
	return req
}

// sendAndClose sends req with Send and closes the body, returning the
// response.
func sendAndClose(t testing.TB, req *openapi.Request) *openapi.Response {
	t.Helper()
	resp, err := req.Send(t.Context())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// trimNL removes one trailing newline, which encoding/json's Encoder writes
// after a value and json.Marshal does not; the package doc does not say
// which of the two the built-in codec is.
func trimNL(b []byte) []byte {
	return bytes.TrimSuffix(b, []byte("\n"))
}

// onceReader is an io.Reader of no type the client can replay (see
// Input.Body): not a []byte, *bytes.Buffer, *bytes.Reader, *strings.Reader
// or *os.File.
type onceReader struct{ r io.Reader }

func (o *onceReader) Read(p []byte) (int, error) { return o.r.Read(p) }

func newOnce(s string) *onceReader { return &onceReader{strings.NewReader(s)} }

// countingTransport counts the requests it carries, and records the
// operation OperationFromContext reports for each.
type countingTransport struct {
	mu   sync.Mutex
	n    int
	ops  []*openapi.Operation
	next http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.n++
	c.ops = append(c.ops, openapi.OperationFromContext(r.Context()))
	c.mu.Unlock()
	next := c.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(r)
}

func (c *countingTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// snapshot returns the operations recorded so far.
func (c *countingTransport) snapshot() []*openapi.Operation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.ops)
}
