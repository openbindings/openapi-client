package openapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Shared apparatus for the credential, security and redirect tests: a document
// with one operation per security scheme type, the credentials for it, an
// in-memory transport for hosts that must never be dialled, a raw listener
// that keeps the bytes of every request, counting credential sources, and a
// check that no secret appears in an error's text or in a *url.Error's URL.

// Secrets the credential tests place. Each is distinctive, so finding one in
// an error or on the wire is unambiguous.
const (
	hSecret   = "h-secret-7Qw" // apiKey in a header
	qSecret   = "q-secret-8Er" // apiKey in the query
	cSecret   = "c-secret-9Ty" // apiKey in a cookie
	basicUser = "alice"
	basicPass = "wonder-land-3Op" // http basic password
	bToken    = "b-token-1Ui"     // http bearer
	uToken    = "u-token-2Pa"     // http BEARER, spelled in capitals
	dProof    = "d-proof-4Sd"     // http DPoP, an http scheme other than basic and bearer
	oToken    = "o-token-5Fg"     // oauth2
	iToken    = "i-token-6Hj"     // openIdConnect
)

// basicField is the credentials part of the Authorization field for
// Basic(basicUser, basicPass): RFC 7617 section 2, user-id ":" password,
// base64-encoded.
var basicField = base64.StdEncoding.EncodeToString([]byte(basicUser + ":" + basicPass))

// credSecrets lists every secret credSet holds, for redaction checks.
var credSecrets = []string{hSecret, qSecret, cSecret, basicPass, basicField, bToken, uToken, dProof, oToken, iToken}

// credSchemes declares one Security Scheme Object of every OpenAPI 3.1 type
// (OAS 3.1.2 section 4.8.27), and the http schemes spelled in more than one
// case (the scheme "is case-insensitive"; doc.go, Credentials: "An http
// scheme, such as bearer or basic, is compared without regard to case").
const credSchemes = `"components":{"securitySchemes":{
	"key_h":{"type":"apiKey","in":"header","name":"X-API-Key"},
	"key_q":{"type":"apiKey","in":"query","name":"api_key"},
	"key_c":{"type":"apiKey","in":"cookie","name":"sid"},
	"basic":{"type":"http","scheme":"basic"},
	"basic_mc":{"type":"http","scheme":"Basic"},
	"bearer":{"type":"http","scheme":"bearer","bearerFormat":"JWT"},
	"bearer_uc":{"type":"http","scheme":"BEARER"},
	"dpop":{"type":"http","scheme":"DPoP"},
	"oauth":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{"read":"","write":""}}}},
	"oidc":{"type":"openIdConnect","openIdConnectUrl":"https://auth.example.test/.well-known/openid-configuration"},
	"mtls":{"type":"mutualTLS"}
}}`

// credPaths has one operation for each scheme of credSchemes, and a few
// with other alternatives.
const credPaths = `
	"/h":{"get":{"operationId":"keyHeader","security":[{"key_h":[]}]}},
	"/q":{"get":{"operationId":"keyQuery","security":[{"key_q":[]}],"parameters":[{"name":"p1","in":"query"},{"name":"p2","in":"query"}]}},
	"/c":{"get":{"operationId":"keyCookie","security":[{"key_c":[]}],"parameters":[{"name":"c1","in":"cookie"}]}},
	"/basic":{"get":{"operationId":"basic","security":[{"basic":[]}]}},
	"/basic-mc":{"get":{"operationId":"basicMixedCase","security":[{"basic_mc":[]}]}},
	"/bearer":{"get":{"operationId":"bearer","security":[{"bearer":[]}]}},
	"/bearer-uc":{"get":{"operationId":"bearerUpper","security":[{"bearer_uc":[]}]}},
	"/dpop":{"get":{"operationId":"dpop","security":[{"dpop":[]}]}},
	"/oauth":{"get":{"operationId":"oauth","security":[{"oauth":["read"]}]}},
	"/oidc":{"get":{"operationId":"oidc","security":[{"oidc":["openid"]}]}},
	"/mtls":{"get":{"operationId":"mtls","security":[{"mtls":[]}]}},
	"/two":{"get":{"operationId":"two","security":[{"key_h":[],"key_q":[]}]}},
	"/either":{"get":{"operationId":"either","security":[{"key_h":[]},{"bearer":[]},{}]}},
	"/open":{"get":{"operationId":"open"}}`

// credDoc is credPaths and credSchemes in a document whose sole server is
// @BASE@.
var credDoc = doc31(credPaths, credSchemes)

// credSet returns a static credential for every scheme of credSchemes but
// mtls, which needs none.
func credSet() map[string]openapi.Credential {
	return map[string]openapi.Credential{
		"key_h":     openapi.Secret(hSecret),
		"key_q":     openapi.Secret(qSecret),
		"key_c":     openapi.Secret(cSecret),
		"basic":     openapi.Basic(basicUser, basicPass),
		"basic_mc":  openapi.Basic("bob", "pw-2Kd"),
		"bearer":    openapi.Secret(bToken),
		"bearer_uc": openapi.Secret(uToken),
		"dpop":      openapi.Secret(dProof),
		"oauth":     openapi.Secret(oToken),
		"oidc":      openapi.Secret(iToken),
	}
}

// credClient parses credDoc against w with every credential of credSet,
// after edit, if not nil, changes the Options.
func credClient(t testing.TB, w *wire, edit func(*openapi.Options)) *openapi.Client {
	t.Helper()
	o := &openapi.Options{Credentials: credSet()}
	if edit != nil {
		edit(o)
	}
	return parseFor(t, w, credDoc, o)
}

// credKey is the RequestError.Settings key for the credential of scheme
// name (errors.go, RequestError.Settings: "Options.<Field>[<name>], for
// Credentials and Variables, the name quoted as strconv.Quote does").
func credKey(name string) string {
	return `Options.Credentials["` + name + `"]`
}

// wantField fails unless h holds the field name with exactly the values
// want, in one field line each; with no values, unless h lacks the field.
func wantField(t testing.TB, h http.Header, name string, want ...string) {
	t.Helper()
	got := h.Values(name)
	if len(want) == 0 {
		if got != nil {
			t.Errorf("%s = %q, want no such field", name, got)
		}
		return
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") || len(got) != len(want) {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

// wantNoFields fails if h holds any of names.
func wantNoFields(t testing.TB, h http.Header, names ...string) {
	t.Helper()
	for _, n := range names {
		if v := h.Values(n); v != nil {
			t.Errorf("%s = %q, want no such field", n, v)
		}
	}
}

// wantAuthorization fails unless h holds one Authorization field whose
// auth-scheme equals scheme without regard to case (RFC 9110 section 11.1:
// "It uses a case-insensitive token to identify the authentication
// scheme") followed by one space and exactly rest.
func wantAuthorization(t testing.TB, h http.Header, scheme, rest string) {
	t.Helper()
	got := h.Values("Authorization")
	if len(got) != 1 {
		t.Errorf("Authorization = %q, want one field %q", got, scheme+" "+rest)
		return
	}
	s, r, ok := strings.Cut(got[0], " ")
	if !ok || !strings.EqualFold(s, scheme) || r != rest {
		t.Errorf("Authorization = %q, want %q (scheme compared without regard to case)", got[0], scheme+" "+rest)
	}
}

// wantURI fails unless r's request target is exactly uri.
func wantURI(t testing.TB, r rec, uri string) {
	t.Helper()
	if r.RequestURI != uri {
		t.Errorf("request target %q, want %q", r.RequestURI, uri)
	}
}

// memReq is one request as memRT carried it.
type memReq struct {
	Method string
	URL    *url.URL
	Header http.Header
	Body   []byte
}

// memRT is an http.RoundTripper that sends nothing: it records each
// request, reading its body, and answers it with answer, or with 200 and
// an empty JSON object. It carries requests to hosts that must never be
// dialled, and to any URL scheme.
type memRT struct {
	mu     sync.Mutex
	reqs   []memReq
	answer func(*http.Request) (*http.Response, error)
}

func (m *memRT) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		r.Body.Close()
	}
	u := *r.URL
	m.mu.Lock()
	m.reqs = append(m.reqs, memReq{r.Method, &u, r.Header.Clone(), body})
	answer := m.answer
	m.mu.Unlock()
	if answer != nil {
		return answer(r)
	}
	return memResponse(r, 200, nil, "{}"), nil
}

func (m *memRT) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reqs)
}

func (m *memRT) requests() []memReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]memReq(nil), m.reqs...)
}

// memResponse is a well-formed response to r with status, header fields h
// and a JSON body.
func memResponse(r *http.Request, status int, h http.Header, body string) *http.Response {
	if h == nil {
		h = http.Header{}
	}
	if body != "" {
		h.Set("Content-Type", "application/json")
	}
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: r,
	}
}

// memClient returns an http.Client over a fresh memRT.
func memClient() (*http.Client, *memRT) {
	rt := &memRT{}
	return &http.Client{Transport: rt}, rt
}

// source is a credential source that counts its calls and answers each
// with fn, called with the context it received and the call's number,
// from 1.
type source struct {
	n  atomic.Int64
	fn func(ctx context.Context, call int64) (string, error)
}

func (s *source) credential() openapi.Credential {
	return openapi.SecretFunc(func(ctx context.Context) (string, error) {
		return s.fn(ctx, s.n.Add(1))
	})
}

func (s *source) calls() int64 { return s.n.Load() }

// fixedSource returns a source that always returns secret.
func fixedSource(secret string) *source {
	return &source{fn: func(context.Context, int64) (string, error) { return secret, nil }}
}

// walkErrors calls f for err and every error in its chain, following
// Unwrap() error and Unwrap() []error as errors.Is does.
func walkErrors(err error, f func(error)) {
	if err == nil {
		return
	}
	f(err)
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		walkErrors(u.Unwrap(), f)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			walkErrors(e, f)
		}
	}
}

// secretForms returns s as it may appear in text: as given, percent-encoded
// as the client encodes query values (every byte outside RFC 3986's
// unreserved set as %XX; doc.go, Percent-encoding), as url.QueryEscape and
// url.PathEscape write it, and in hex.
func secretForms(s string) []string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	forms := []string{s, b.String(), url.QueryEscape(s), url.PathEscape(s), hex.EncodeToString([]byte(s))}
	var out []string
	for _, f := range forms {
		if f != "" && !contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// noSecrets fails if any of secrets appears in err's text, or in the URL or
// text of any *url.Error in its chain (doc.go, Outcomes: "No credential
// appears in the text of an error the client creates, nor in the URL of the
// *url.Error the http.Client returns, which names the request without the
// credentials the client added"). It walks the whole chain, which is
// stricter than that: use it only where the caller's own code makes no error
// that quotes a credential (doc.go, Outcomes: "Errors made by the caller's
// own code, such as its transport or a credential source, are passed on as
// they are").
func noSecrets(t testing.TB, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		return
	}
	check := func(where, text string) {
		for _, s := range secrets {
			for _, form := range secretForms(s) {
				if strings.Contains(text, form) {
					t.Errorf("%s holds the credential %q: %q", where, s, text)
				}
			}
		}
	}
	check("the error text", err.Error())
	walkErrors(err, func(e error) {
		if ue, ok := e.(*url.Error); ok {
			check("a *url.Error's URL", ue.URL)
			check("a *url.Error's text", ue.Error())
		}
	})
}

// rawServer is a TCP listener that reads one HTTP/1.1 request per
// connection, keeps the exact bytes received, and writes reply, or closes
// the connection without a response when reply is empty.
type rawServer struct {
	ln    net.Listener
	URL   string
	reply string
	mu    sync.Mutex
	raw   [][]byte
}

// rawOK is a complete 200 response with an empty JSON object.
const rawOK = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"

func newRawServer(t testing.TB, reply string) *rawServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &rawServer{ln: ln, URL: "http://" + ln.Addr().String(), reply: reply}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *rawServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *rawServer) handle(conn net.Conn) {
	defer conn.Close()
	var buf bytes.Buffer
	req, err := http.ReadRequest(bufio.NewReader(io.TeeReader(conn, &buf)))
	if err != nil {
		return
	}
	io.Copy(io.Discard, req.Body)
	s.mu.Lock()
	s.raw = append(s.raw, bytes.Clone(buf.Bytes()))
	s.mu.Unlock()
	if s.reply != "" {
		io.WriteString(conn, s.reply)
	}
}

// requests returns the bytes of every request received so far.
func (s *rawServer) requests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.raw...)
}

// hostport is the listener's host and port.
func (s *rawServer) hostport() string { return s.ln.Addr().String() }

// errorContains reports whether err's text contains every one of subs.
func errorContains(err error, subs ...string) bool {
	if err == nil {
		return false
	}
	for _, s := range subs {
		if !strings.Contains(err.Error(), s) {
			return false
		}
	}
	return true
}

// errTransport is an error a caller's transport returns.
var errTransport = errors.New("caller transport refused the request")
