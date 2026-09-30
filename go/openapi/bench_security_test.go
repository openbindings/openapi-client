package openapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 3 benchmarks (brief, Tests: "a call with an apiKey header, with a
// bearer SecretFunc, and a same-origin redirect, each against the same
// request built by hand"; Budgets: "credential placement near hand-written
// parity"). The in-memory ones resolve the client's own cost (stage 1
// ledger, #29); the loopback redirect is the end-to-end check. Run with
// -benchmem.

// securedBenchDoc is benchDoc under root security that uses scheme, one of
// "api_key" (an apiKey header X-API-Key) and "bearer" (http bearer).
func securedBenchDoc(scheme string) string {
	return strings.Replace(benchDoc, `"components":{"schemas":`,
		`"security":[{"`+scheme+`":[]}],"components":{"securitySchemes":{`+
			`"api_key":{"type":"apiKey","in":"header","name":"X-API-Key"},"bearer":{"type":"http","scheme":"bearer"}},"schemas":`, 1)
}

const benchKey = "bench-api-key-0123456789"

func securedClient(b *testing.B, scheme, base string, hc *http.Client, cred openapi.Credential, redirects openapi.Redirects) *openapi.Client {
	b.Helper()
	c, err := openapi.Parse(context.Background(), []byte(expand(securedBenchDoc(scheme), base)), base+"/openapi.json",
		&openapi.Options{HTTPClient: hc, Redirects: redirects, Credentials: map[string]openapi.Credential{scheme: cred}})
	if err != nil {
		b.Fatal(err)
	}
	return c
}

// handGetWith is handGet with one header field set.
func handGetWith(b *testing.B, hc *http.Client, base, field, value string) {
	u := base + "/pets/" + url.PathEscape("p-7") + "?revision=" + strconv.Itoa(3)
	req, err := http.NewRequestWithContext(context.Background(), "GET", u, nil)
	if err != nil {
		b.Fatal(err)
	}
	req.Header.Set(field, value)
	resp, err := hc.Do(req)
	if err != nil {
		b.Fatal(err)
	}
	var pet Pet
	if resp.StatusCode/100 != 2 {
		b.Fatal(resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&pet); err != nil {
		b.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

var benchGetIn = &openapi.Input{Params: map[string]any{"petId": "p-7", "revision": 3}}

func BenchmarkMemCallAPIKeyHeader(b *testing.B) {
	c := securedClient(b, "api_key", cannedBase, &http.Client{Transport: cannedRT{}}, openapi.Secret(benchKey), openapi.FollowNone)
	b.ReportAllocs()
	for b.Loop() {
		callGet(b, c, benchGetIn)
	}
}

// BenchmarkMemNetHTTPAPIKeyHeader is BenchmarkMemCallAPIKeyHeader written
// by hand.
func BenchmarkMemNetHTTPAPIKeyHeader(b *testing.B) {
	hc := &http.Client{Transport: cannedRT{}}
	b.ReportAllocs()
	for b.Loop() {
		handGetWith(b, hc, cannedBase, "X-API-Key", benchKey)
	}
}

func BenchmarkMemCallBearerSecretFunc(b *testing.B) {
	token := func(context.Context) (string, error) { return benchKey, nil }
	c := securedClient(b, "bearer", cannedBase, &http.Client{Transport: cannedRT{}}, openapi.SecretFunc(token), openapi.FollowNone)
	b.ReportAllocs()
	for b.Loop() {
		callGet(b, c, benchGetIn)
	}
}

// BenchmarkMemNetHTTPBearerSecretFunc is BenchmarkMemCallBearerSecretFunc
// written by hand: the token from the same function for each request.
func BenchmarkMemNetHTTPBearerSecretFunc(b *testing.B) {
	token := func(context.Context) (string, error) { return benchKey, nil }
	hc := &http.Client{Transport: cannedRT{}}
	b.ReportAllocs()
	for b.Loop() {
		tok, err := token(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		handGetWith(b, hc, cannedBase, "Authorization", "Bearer "+tok)
	}
}

// redirectRT answers a path not ending in /v2 with a 307 to the path with
// /v2 appended, and any other as cannedRT does.
type redirectRT struct{}

func (redirectRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/v2") {
		return cannedRT{}.RoundTrip(r)
	}
	if r.Body != nil {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	return &http.Response{StatusCode: 307, Status: "307 Temporary Redirect", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Location": {r.URL.Path + "/v2"}}, Body: http.NoBody, Request: r}, nil
}

func BenchmarkMemCallSameOriginRedirect(b *testing.B) {
	c := securedClient(b, "api_key", cannedBase, &http.Client{Transport: redirectRT{}}, openapi.Secret(benchKey), openapi.FollowAll)
	b.ReportAllocs()
	for b.Loop() {
		callGet(b, c, benchGetIn)
	}
}

// BenchmarkMemNetHTTPSameOriginRedirect is BenchmarkMemCallSameOriginRedirect
// written by hand: net/http follows the 307 and copies the key to the same
// host.
func BenchmarkMemNetHTTPSameOriginRedirect(b *testing.B) {
	hc := &http.Client{Transport: redirectRT{}}
	b.ReportAllocs()
	for b.Loop() {
		handGetWith(b, hc, cannedBase, "X-API-Key", benchKey)
	}
}

// redirectServer serves benchServer's pets behind a 307 to the path with
// /v2 appended.
func redirectServer(b *testing.B) *httptest.Server {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v2") {
			w.Header().Set("Location", r.URL.Path+"/v2")
			w.WriteHeader(307)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(petJSON)
	}))
	b.Cleanup(srv.Close)
	return srv
}

func BenchmarkCallSameOriginRedirect(b *testing.B) {
	srv := redirectServer(b)
	c := securedClient(b, "api_key", srv.URL, srv.Client(), openapi.Secret(benchKey), openapi.FollowAll)
	b.ReportAllocs()
	for b.Loop() {
		callGet(b, c, benchGetIn)
	}
}

// BenchmarkNetHTTPSameOriginRedirect is BenchmarkCallSameOriginRedirect
// written by hand.
func BenchmarkNetHTTPSameOriginRedirect(b *testing.B) {
	srv := redirectServer(b)
	hc := srv.Client()
	b.ReportAllocs()
	for b.Loop() {
		handGetWith(b, hc, srv.URL, "X-API-Key", benchKey)
	}
}
