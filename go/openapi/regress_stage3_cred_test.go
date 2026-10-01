package openapi_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the stage 3 review round, credentials and security
// (stage 3 ledger, "Review round (8101e19)"; review/panel.json, F1 to F22;
// review/gpt-answer.md, A1 to A8): class rulings C3-3 (the client applies
// the cookie jar), C3-6 (credentials placed after the last edit, checked
// against the URL sent), C3-7 (a value its destination cannot carry) and
// C3-8 (static credential problems at Load), and findings F3, F4, F8, F9,
// F10, F12, F13, F20 and A2 (redaction) and A8.

// cookiePairs returns the pairs of the one Cookie field h holds, or nil
// when it holds none, failing when it holds several.
func cookiePairs(t *testing.T, h http.Header) []string {
	t.Helper()
	fields := h.Values("Cookie")
	switch len(fields) {
	case 0:
		return nil
	case 1:
		return strings.Split(fields[0], "; ")
	}
	t.Errorf("%d Cookie fields %q, want one (doc.go, Cookies: \"one Cookie field\")", len(fields), fields)
	return strings.Split(strings.Join(fields, "; "), "; ")
}

// newJar returns a cookie jar holding cookies for the server at base.
func newJar(t *testing.T, base string, cookies ...*http.Cookie) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) > 0 {
		u, _ := url.Parse(base)
		jar.SetCookies(u, cookies)
	}
	return jar
}

// C3-3 (F11, A6): "The client applies the cookie jar itself, as net/http's
// send does (Jar.Cookies before sending, Jar.SetCookies after each
// response), on the copy with Jar nil: jar cookies first, then credentials
// replacing a pair of the same name and going last". doc.go, Credentials:
// "Query and cookie credentials go last ... and one that replaces a pair of
// the same name ... removes it and goes last"; Cookies: "one Cookie field".
func TestJarCookiesThenCredentials(t *testing.T) {
	for _, via := range []string{"Call", "Send"} {
		t.Run(via, func(t *testing.T) {
			w := newWire(t, nil)
			jar := newJar(t, w.URL, &http.Cookie{Name: "sid", Value: "from-jar"}, &http.Cookie{Name: "z", Value: "1"})
			c := credClient(t, w, func(o *openapi.Options) { o.HTTPClient = &http.Client{Jar: jar} })
			in := &openapi.Input{Params: map[string]any{"c1": "v"}}
			if via == "Call" {
				mustCall(t, c, "keyCookie", in, nil)
			} else {
				sendAndClose(t, mustPrepare(t, c, "keyCookie", in))
			}
			pairs := cookiePairs(t, w.last(t).Header)
			if len(pairs) == 0 || pairs[len(pairs)-1] != "sid="+cSecret {
				t.Errorf("Cookie pairs %q, want the credential sid=%s last", pairs, cSecret)
			}
			n := 0
			for _, p := range pairs {
				if strings.HasPrefix(p, "sid=") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("Cookie pairs %q hold %d pairs named sid, want only the credential", pairs, n)
			}
			for _, want := range []string{"z=1", "c1=v"} {
				if !contains(pairs, want) {
					t.Errorf("Cookie pairs %q lack %s", pairs, want)
				}
			}
		})
	}
}

// C3-3 and F12 (test gap: "a jar with FollowAll and same-origin hops, with
// and without a placed credential"; quality-economy's
// TestQEJarRedirectNoCredential: without the jar step a third hop sent
// "j=1; j=1"): each hop carries the jar's cookies for it once, the cookie a
// response set included (Jar.SetCookies after each response), then the
// credential, last.
func TestJarOnEveryHop(t *testing.T) {
	doc := doc31(`"/r":{"get":{"operationId":"plain"}},"/s":{"get":{"operationId":"withCredential","security":[{"key_c":[]}]}}`, credSchemes)
	setAndGo := func(loc string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: "j", Value: "1", Path: "/"})
			redirect(302, loc)(w, r)
		}
	}
	for _, tt := range []struct {
		key  string
		want [3][]string
	}{
		{"plain", [3][]string{nil, {"j=1"}, {"j=1"}}},
		{"withCredential", [3][]string{{"sid=" + cSecret}, {"j=1", "sid=" + cSecret}, {"j=1", "sid=" + cSecret}}},
	} {
		t.Run(tt.key, func(t *testing.T) {
			w := newWire(t, routes(map[string]http.HandlerFunc{
				"/r": setAndGo("/r2"), "/r2": redirect(302, "/r3"),
				"/s": setAndGo("/s2"), "/s2": redirect(302, "/s3"),
			}))
			jar := newJar(t, w.URL)
			c := parseFor(t, w, doc, &openapi.Options{HTTPClient: &http.Client{Jar: jar}, Redirects: openapi.FollowAll,
				Credentials: map[string]openapi.Credential{"key_c": openapi.Secret(cSecret)}})
			mustCall(t, c, tt.key, nil, nil)
			reqs := w.requests()
			if len(reqs) != 3 {
				t.Fatalf("server received %d requests, want 3", len(reqs))
			}
			for i, r := range reqs {
				if got := cookiePairs(t, r.Header); strings.Join(got, "; ") != strings.Join(tt.want[i], "; ") {
					t.Errorf("request %d (%s) Cookie pairs %q, want %q", i, r.Path, got, tt.want[i])
				}
			}
			u, _ := url.Parse(w.URL)
			if got := jar.Cookies(u); len(got) != 1 || got[0].Name != "j" {
				t.Errorf("the caller's jar holds %v, want the cookie the server set", got)
			}
		})
	}
}

// C3-3: "Jar cookies go on the request sent, never into Request.HTTP."
// client.go, Request.HTTP: a prepared request is the caller's to send again;
// Request.Call: one whose body can be sent again "may be sent any number of
// times". client.go, Options.HTTPClient: "It is used as given and never
// modified."
func TestJarNeverEditsRequestHTTP(t *testing.T) {
	w := newWire(t, nil)
	jar := newJar(t, w.URL, &http.Cookie{Name: "z", Value: "1"})
	hc := &http.Client{Jar: jar}
	c := credClient(t, w, func(o *openapi.Options) { o.HTTPClient = hc })
	req := mustPrepare(t, c, "keyCookie", &openapi.Input{Params: map[string]any{"c1": "v"}})
	for i := range 2 {
		sendAndClose(t, req)
		wantField(t, req.HTTP.Header, "Cookie", "c1=v")
		if got := cookiePairs(t, w.last(t).Header); !contains(got, "z=1") || got[len(got)-1] != "sid="+cSecret {
			t.Errorf("send %d: Cookie pairs %q, want the jar's z=1 and the credential last", i, got)
		}
	}
	if hc.Jar != jar {
		t.Errorf("the http.Client's Jar was replaced")
	}
}

// C3-3 with Redirects: a hop to another origin carries no credential, the
// jar's cookies being the jar's to choose (client.go, Redirects: "On a hop
// to another origin ... the client removes the credentials it added").
func TestJarKeepsCredentialOnItsOrigin(t *testing.T) {
	b := newWire(t, nil)
	a := newWire(t, routes(map[string]http.HandlerFunc{"/c": func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "j", Value: "1", Path: "/"})
		redirect(307, b.URL+"/b")(w, r)
	}}))
	jar := newJar(t, a.URL)
	c := credClient(t, a, func(o *openapi.Options) {
		o.HTTPClient = &http.Client{Jar: jar}
		o.Redirects = openapi.FollowAll
	})
	mustCall(t, c, "keyCookie", nil, nil)
	hop := b.only(t)
	for _, v := range hop.Header.Values("Cookie") {
		if strings.Contains(v, cSecret) {
			t.Errorf("the hop to another origin carried the cookie credential: %q", v)
		}
	}
}

// securedWriterDoc has operations with a query parameter p under a bearer token,
// an API key in a header, a cookie API key, and no security.
var securedWriterDoc = doc31(`
	"/w":{"get":{"operationId":"w","security":[{"bearer":[]}],"parameters":[{"name":"p","in":"query"}]}},
	"/wk":{"get":{"operationId":"wk","security":[{"key_h":[]}],"parameters":[{"name":"p","in":"query"}]}},
	"/wc":{"get":{"operationId":"wc","security":[{"key_c":[]}],"parameters":[{"name":"p","in":"query"}]}},
	"/wo":{"get":{"operationId":"wo","parameters":[{"name":"p","in":"query"}]}}`, credSchemes)

// writerCreds are the credentials for securedWriterDoc's schemes.
func writerCreds() map[string]openapi.Credential {
	return map[string]openapi.Credential{
		"bearer": openapi.Secret(bToken), "key_h": openapi.Secret(hSecret), "key_c": openapi.Secret(cSecret),
	}
}

// writer returns Input.ParamWriters giving p the writer f.
func writer(f func(*http.Request)) *openapi.Input {
	return &openapi.Input{ParamWriters: map[string]func(*http.Request) error{"p": func(r *http.Request) error { f(r); return nil }}}
}

// C3-6 (F9): "Credentials are placed after the last edit, and the origin
// and plain-http checks run against the URL actually sent, on every hop the
// client signs (F9 writers changing scheme or host ...)". doc.go,
// Credentials: the client "adds credentials only to requests with the
// origin of the server the call resolved to"; client.go, Request.HTTP: "If
// the URL is changed to another origin and the call needs credentials,
// sending it is refused". The adversarial reviewer's TestZA3WriterMovesOrigin
// sent a bearer token over plain http to another host.
func TestParamWriterCannotMoveCredentials(t *testing.T) {
	moves := map[string]func(*http.Request){
		"to another host over plain http": func(r *http.Request) { r.URL.Scheme, r.URL.Host = "http", "elsewhere.example.test" },
		"to another host":                 func(r *http.Request) { r.URL.Host = "elsewhere.example.test" },
		"to another scheme":               func(r *http.Request) { r.URL.Scheme = "http" },
		"to another port":                 func(r *http.Request) { r.URL.Host = "api.example.test:8443" },
	}
	for name, move := range moves {
		for _, key := range []string{"w", "wk", "wc"} {
			for _, via := range []string{"Call", "Prepare"} {
				t.Run(name+", "+key+", "+via, func(t *testing.T) {
					hc, rt := memClient()
					c := parseAt(t, securedWriterDoc, "https://api.example.test", testDocURI, &openapi.Options{HTTPClient: hc, Credentials: writerCreds()})
					var err error
					if via == "Call" {
						_, err = c.Call(t.Context(), key, writer(move), nil)
					} else {
						// Prepare may refuse, or the send of what it prepared.
						var req *openapi.Request
						if req, err = c.Prepare(key, writer(move)); err == nil {
							_, err = req.Call(t.Context(), nil)
						}
					}
					if n := rt.count(); n != 0 {
						t.Errorf("transport carried %d requests moved off the call's origin with its credentials", n)
					}
					asRequestError(t, err)
					noSecrets(t, err, credSecrets...)
				})
			}
		}
	}
	// A call that places no credential may be moved.
	hc, rt := memClient()
	c := parseAt(t, securedWriterDoc, "https://api.example.test", testDocURI, &openapi.Options{HTTPClient: hc, Credentials: writerCreds()})
	mustCall(t, c, "wo", writer(moves["to another host"]), nil)
	if reqs := rt.requests(); len(reqs) != 1 || reqs[0].URL.Host != "elsewhere.example.test" {
		t.Errorf("a call with no credentials was not sent where its writer moved it")
	}
}

// F3 and C3-6: doc.go, Credentials: bearer and Basic credentials go over
// plain http only to "a loopback IP address, an IPv4-mapped one included,
// or the name localhost written exactly so, the one name net/http's proxy
// settings never apply to"; C3-6: "the origin and plain-http checks run
// against the URL actually sent, on every hop the client signs" (panel F3:
// a hop from http://localhost:P to http://LOCALHOST:P is the same origin,
// signed again without the rule, and net/http proxies it). The transport
// dials nothing.
func TestLocalhostSpellingCheckedOnTheURLSent(t *testing.T) {
	const base = "http://localhost:9"
	t.Run("Request.HTTP", func(t *testing.T) {
		hc, rt := memClient()
		c := parseAt(t, credDoc, "https://unused.example.test", testDocURI, &openapi.Options{HTTPClient: hc, BaseURL: base, Credentials: credSet()})
		req := mustPrepare(t, c, "bearer", nil)
		req.HTTP.URL.Host = "LOCALHOST:9"
		resp, err := req.Send(t.Context())
		refusedBeforeSending(t, nil, resp, err)
		if n := rt.count(); n != 0 {
			t.Errorf("transport carried %d requests with a bearer token to LOCALHOST over plain http", n)
		}
	})
	t.Run("a ParamWriter", func(t *testing.T) {
		hc, rt := memClient()
		c := parseAt(t, securedWriterDoc, "https://unused.example.test", testDocURI, &openapi.Options{HTTPClient: hc, BaseURL: base, Credentials: writerCreds()})
		resp, err := c.Call(t.Context(), "w", writer(func(r *http.Request) { r.URL.Host = "LOCALHOST:9" }), nil)
		refusedBeforeSending(t, nil, resp, err)
		if n := rt.count(); n != 0 {
			t.Errorf("transport carried %d requests with a bearer token to LOCALHOST over plain http", n)
		}
	})
	for _, tt := range []struct {
		loc  string
		sent bool
	}{{"http://LOCALHOST:9/next", false}, {"http://Localhost:9/next", false}, {"http://localhost:9/next", true}} {
		t.Run("a hop to "+tt.loc, func(t *testing.T) {
			rt := &memRT{}
			rt.answer = func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/bearer" {
					return memResponse(r, 302, http.Header{"Location": {tt.loc}}, ""), nil
				}
				return memResponse(r, 200, nil, "{}"), nil
			}
			c := parseAt(t, credDoc, "https://unused.example.test", testDocURI, &openapi.Options{
				HTTPClient: &http.Client{Transport: rt}, BaseURL: base, Redirects: openapi.FollowAll, Credentials: credSet(),
			})
			resp, err := c.Call(t.Context(), "bearer", nil, nil)
			reqs := rt.requests()
			if tt.sent {
				if err != nil || len(reqs) != 2 || reqs[1].Header.Get("Authorization") != "Bearer "+bToken {
					t.Errorf("Call = %v after %d requests; want the hop sent with its token", err, len(reqs))
				}
				return
			}
			var ue *url.Error
			if !errors.As(err, &ue) {
				t.Errorf("Call error %v (%T), want the *url.Error that ends the chain", err, err)
			}
			for _, r := range reqs[1:] {
				if r.Header.Get("Authorization") != "" {
					t.Errorf("the hop to %s carried Authorization over plain http", r.URL)
				}
			}
			if len(reqs) != 1 {
				t.Errorf("transport carried %d requests, want the first only: the hop is refused", len(reqs))
			}
			if resp == nil || resp.StatusCode != 302 {
				t.Errorf("Response %v, want the 302 that arrived (C3-5)", resp)
			}
			noSecrets(t, err, credSecrets...)
		})
	}
}

// valueCase is a credential value for a scheme of credDoc, called through
// the operation key, and whether its destination can carry it.
type valueCase struct {
	scheme, key, value string
	ok                 bool
}

// valueCases returns, for each scheme of credSchemes but mutualTLS, values
// its destination can carry and values it cannot. Each holds the marker
// "Zq9", so an error that quotes one is found.
func valueCases() []valueCase {
	var cases []valueCase
	add := func(scheme, key string, ok bool, values ...string) {
		for _, v := range values {
			cases = append(cases, valueCase{scheme, key, v, ok})
		}
	}
	// A header field value (RFC 9110 section 5.5): no control character
	// but a tab, and no whitespace around it.
	add("key_h", "keyHeader", true, "Zq9 a\tb", "Zq9-\xc3\xa9")
	add("key_h", "keyHeader", false, "Zq9\x01", "Zq9\x7f", " Zq9", "Zq9\t", "Zq9\rX", "Zq9\nX", "Zq9\x00")
	// A bearer token (RFC 6750 section 2.1): b64token = 1*( ALPHA / DIGIT /
	// "-" / "." / "_" / "~" / "+" / "/" ) *"=".
	add("bearer", "bearer", true, "Zq9", "Zq9-A.z_0~9+/", "Zq9=", "Zq9==", "mF_9.B5f-4.1JqM-Zq9")
	add("bearer", "bearer", false, "Zq9 a", "Zq9,a", "Zq9\"a", "Zq9;a", "=Zq9", "Zq9=a", "Zq9\ta", "Zq9\xc3\xa9", "Zq9:a", "Zq9\\a", "Zq9@a", "Zq9\x01")
	add("bearer_uc", "bearerUpper", false, "Zq9 a", "Zq9=a")
	add("oauth", "oauth", false, "Zq9 a", "Zq9,a")
	add("oidc", "oidc", false, "Zq9 a", "Zq9;a")
	add("oauth", "oauth", true, "Zq9-A.z_0~9+/==")
	// An http basic user-pass (RFC 7617 section 2): user-id ":" password,
	// neither holding a control character (RFC 5234's CTL, %x00-1F / %x7F).
	add("basic", "basic", true, "Zq9:pw", ":Zq9", "Zq9:", "Zq9:p:w", "\xc3\xbcZq9:p\xc3\xa4ss", "Zq9 a:p w")
	add("basic", "basic", false, "Zq9-no-colon", "Zq9\x01:pw", "Zq9:pw\x7f", "Zq9:p\tw", "Zq9\n:pw", "Zq9:pw\x00")
	// Any other http scheme: a header field value only.
	add("dpop", "dpop", true, "Zq9 a,b=\"c\"", "Zq9\tx")
	add("dpop", "dpop", false, "Zq9\x01", " Zq9", "Zq9 ", "Zq9\nX")
	// A cookie value, as RFC 6265 section 5 lets a user agent send it: no
	// ";" and no control character (stage 3 ledger, C3-7 and R6: a
	// backslash, a quote, a comma or a space within is carried).
	add("key_c", "keyCookie", true, "Zq9\\a", "Zq9\"a", "Zq9,a", "Zq9 a")
	add("key_c", "keyCookie", false, "Zq9;a", "Zq9\x00", "Zq9\ta", "Zq9\x7f", "Zq9\r\nX")
	// A query value is percent-encoded, so any value can be carried.
	add("key_q", "keyQuery", true, "Zq9 a;b&c=d", "Zq9\x01")
	return cases
}

// C3-7 (F21, A7): "A credential value is refused at
// Options.Credentials["name"] (Load for a static one, the call for a
// source's) wherever its wire syntax cannot carry it". C3-8: "every static
// credential problem is refused by Load (by each call for a Client.With's
// Options), a source's value by the call". doc.go, Credentials: "A
// credential value its destination cannot carry is refused at
// Options.Credentials["name"]: by Load for a static credential (by each
// call, for a Client from Client.With), by the call for a source's. Such a
// value is a header field value with a control character other than a tab,
// or with leading or trailing whitespace; a cookie value with ";" or a
// control character; a bearer token with a character outside RFC 6750's
// b64token; and a Basic value without the colon, or with a control
// character in the user-id or password, which RFC 7617 forbids."
// credential.go, Secret: for http basic, "the user-id and password joined by
// a colon, as RFC 7617 writes them"; for a bearer token, "RFC 6750 limits it
// to b64token characters". A refusal never quotes the value (doc.go,
// Outcomes).
func TestCredentialValueSyntax(t *testing.T) {
	w := newWire(t, nil)
	base := credClient(t, w, nil)
	for _, tc := range valueCases() {
		verdict := "refused"
		if tc.ok {
			verdict = "carried"
		}
		t.Run(fmt.Sprintf("%s %s %q", verdict, tc.scheme, tc.value), func(t *testing.T) {
			creds := func(cred openapi.Credential) map[string]openapi.Credential {
				return map[string]openapi.Credential{tc.scheme: cred}
			}
			// A static credential, at Load.
			c, err := openapi.Parse(t.Context(), []byte(expand(credDoc, w.URL)), w.URL+"/openapi.json",
				&openapi.Options{Credentials: creds(openapi.Secret(tc.value))})
			switch {
			case tc.ok && err != nil:
				t.Errorf("Load: %v", err)
			case tc.ok:
				before := w.count()
				if _, err := c.Call(t.Context(), tc.key, nil, nil); err != nil || w.count() != before+1 {
					t.Errorf("Call = %v; want it sent", err)
				}
			case err == nil:
				t.Errorf("Load accepted a static credential its destination cannot carry")
			default:
				wantKeys(t, "Settings", asRequestError(t, err).Settings, true, credKey(tc.scheme))
				noSecrets(t, err, tc.value)
			}

			// A static credential through With, at each call.
			d := base.With(func(o *openapi.Options) { o.Credentials[tc.scheme] = openapi.Secret(tc.value) })
			before := w.count()
			resp, err := d.Call(t.Context(), tc.key, nil, nil)
			if tc.ok {
				if err != nil {
					t.Errorf("With, Call: %v", err)
				}
			} else {
				re := refusedSince(t, w, before, resp, err)
				wantKeys(t, "Settings", re.Settings, true, credKey(tc.scheme))
				noSecrets(t, err, tc.value)
			}

			// A credential source's value, at the call.
			src := fixedSource(tc.value)
			e := base.With(func(o *openapi.Options) { o.Credentials[tc.scheme] = src.credential() })
			before = w.count()
			resp, err = e.Call(t.Context(), tc.key, nil, nil)
			if tc.ok {
				if err != nil {
					t.Errorf("SecretFunc, Call: %v", err)
				}
			} else {
				re := refusedSince(t, w, before, resp, err)
				wantKeys(t, "Settings", re.Settings, true, credKey(tc.scheme))
				noSecrets(t, err, tc.value)
			}
		})
	}
}

// F12 (test gap: "Basic given to a non-basic scheme through With"):
// doc.go, Credentials: Load refuses "a [Basic] credential for a name none of
// whose schemes is http basic"; client.go, With: a derived Client skips only
// Load's name checks, "and any other Options the document cannot use refuse
// each call they affect"; C3-8: "by each call for a Client.With's Options".
func TestBasicForAnotherSchemeThroughWith(t *testing.T) {
	w := newWire(t, nil)
	c := credClient(t, w, nil)
	for scheme, key := range map[string]string{"bearer": "bearer", "key_h": "keyHeader", "oauth": "oauth", "dpop": "dpop", "key_c": "keyCookie"} {
		d := c.With(func(o *openapi.Options) { o.Credentials[scheme] = openapi.Basic("u", "basic-pw-5Tg") })
		resp, err := d.Call(t.Context(), key, nil, nil)
		re := refusedBeforeSending(t, w, resp, err)
		wantKeys(t, "Settings", re.Settings, true, credKey(scheme))
		noSecrets(t, err, "basic-pw-5Tg")
	}
}

// F13: "a hop's refused credential names its scheme; no unchecked type
// assertion." C3-7 applies on a hop as on the first request (the value is a
// source's, refused by the call), and C3-5 returns the response that
// arrived. errors.go, RequestError.Err: "a credential source's error (naming
// the scheme)"; credential.go, SecretFunc: on a hop, "an error or an empty
// secret ends the call with a *url.Error ... along with the last response".
func TestHopCredentialRefusalNamesItsScheme(t *testing.T) {
	doc := doc31(`"/r":{"get":{"operationId":"getR"}}`, `"security":[{"corp_bearer":[]}]`,
		`"components":{"securitySchemes":{"corp_bearer":{"type":"http","scheme":"bearer"}}}`)
	for name, bad := range map[string]string{
		"a value no header field carries": "bad\nhop-5Zx",
		"a value outside b64token":        "bad hop-5Zx",
	} {
		t.Run(name, func(t *testing.T) {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next")}))
			src := &source{fn: func(_ context.Context, n int64) (string, error) {
				if n == 1 {
					return "tok-1Ab", nil
				}
				return bad, nil
			}}
			c := parseFor(t, a, doc, &openapi.Options{Redirects: openapi.FollowAll,
				Credentials: map[string]openapi.Credential{"corp_bearer": src.credential()}})
			resp, err := c.Call(t.Context(), "getR", nil, nil)
			var ue *url.Error
			if !errors.As(err, &ue) {
				t.Fatalf("Call error %v (%T), want a *url.Error ending the chain", err, err)
			}
			if !strings.Contains(err.Error(), "corp_bearer") {
				t.Errorf("error %q does not name the scheme corp_bearer", err)
			}
			noSecrets(t, err, bad, "tok-1Ab")
			if n := a.count(); n != 1 {
				t.Errorf("server received %d requests, want the first only", n)
			}
			if resp == nil || resp.StatusCode != 302 {
				t.Errorf("Response %v, want the 302 that arrived (C3-5)", resp)
			}
		})
	}
}

// F4: "a refusal before dispatch leaves a prepared Request sendable; T1 on
// Request.Call: a *RequestError does not use up a body that can be read
// once." client.go, Request.Call: a read-once Request "may be sent once, and
// sending it again is refused with a *RequestError, nothing sent; a send
// refused with a *RequestError does not count." credential.go, SecretFunc:
// "An error from f, or an empty secret, on the first request refuses the
// call with a *RequestError: nothing is sent."
func TestRefusedSendKeepsReadOnceRequest(t *testing.T) {
	doc := doc31(`"/up":{"post":{"operationId":"up","security":[{"bearer":[]}],"requestBody":{"content":{"application/octet-stream":{}}}}}`, credSchemes)
	for name, first := range map[string]func() (string, error){
		"a source error":    func() (string, error) { return "", errors.New("token endpoint down") },
		"an empty secret":   func() (string, error) { return "", nil },
		"an unsendable one": func() (string, error) { return "bad\ntok-9Ui", nil },
	} {
		for _, via := range []string{"Call", "Send"} {
			t.Run(name+", "+via, func(t *testing.T) {
				w := newWire(t, nil)
				src := &source{fn: func(_ context.Context, n int64) (string, error) {
					if n == 1 {
						return first()
					}
					return bToken, nil
				}}
				c := parseFor(t, w, doc, &openapi.Options{Credentials: map[string]openapi.Credential{"bearer": src.credential()}})
				req := mustPrepare(t, c, "up", &openapi.Input{Body: newOnce("payload")})
				send := func() (*openapi.Response, error) {
					if via == "Call" {
						return req.Call(t.Context(), nil)
					}
					resp, err := req.Send(t.Context())
					if err == nil {
						io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
					}
					return resp, err
				}
				resp, err := send()
				refusedBeforeSending(t, w, resp, err)
				if _, err := send(); err != nil {
					t.Fatalf("second send: %v; the refused first send used up the body", err)
				}
				if r := w.only(t); string(r.Body) != "payload" {
					t.Errorf("server received %q, want the whole body", r.Body)
				}
			})
		}
	}
}

// F10: "replacing a header credential deletes every spelling of the field
// on the sent copy, on every path". doc.go, Credentials: "A header
// credential replaces a field of the same name"; field names compare
// without regard to case (RFC 9110 section 5.1). The adversarial reviewer's
// TestZA3WriterLowercaseCredentialField sent two fields from a ParamWriter
// on the Call path, and TestZA3CheckRedirectLowercaseCredentialField two on
// a hop.
func TestCredentialReplacesEverySpelling(t *testing.T) {
	cases := []struct {
		key, spelled, value, field string
		want                       string
	}{
		{"w", "authorization", "Bearer writer", "Authorization", "Bearer " + bToken},
		{"wk", "x-api-key", "writer", "X-Api-Key", hSecret},
		{"wc", "cookie", "a=1; sid=writer", "Cookie", "a=1; sid=" + cSecret},
	}
	for _, tc := range cases {
		for _, via := range []string{"ParamWriter, Call", "ParamWriter, Prepare", "Request.HTTP"} {
			t.Run(tc.key+", "+via, func(t *testing.T) {
				w := newWire(t, nil)
				c := parseFor(t, w, securedWriterDoc, &openapi.Options{Credentials: writerCreds()})
				in := writer(func(r *http.Request) { r.Header[tc.spelled] = []string{tc.value} })
				switch via {
				case "ParamWriter, Call":
					mustCall(t, c, tc.key, in, nil)
				case "ParamWriter, Prepare":
					sendAndClose(t, mustPrepare(t, c, tc.key, in))
				default:
					req := mustPrepare(t, c, tc.key, nil)
					req.HTTP.Header[tc.spelled] = []string{tc.value}
					sendAndClose(t, req)
				}
				wantField(t, w.last(t).Header, tc.field, tc.want)
			})
		}
	}
	t.Run("CheckRedirect, on a hop", func(t *testing.T) {
		w := newWire(t, routes(map[string]http.HandlerFunc{"/bearer": redirect(302, "/next")}))
		c := credClient(t, w, func(o *openapi.Options) {
			o.Redirects = openapi.FollowAll
			o.HTTPClient = &http.Client{CheckRedirect: func(r *http.Request, _ []*http.Request) error {
				r.Header["authorization"] = []string{"Bearer from-check-redirect"}
				return nil
			}}
		})
		mustCall(t, c, "bearer", nil, nil)
		reqs := w.requests()
		if len(reqs) != 2 {
			t.Fatalf("server received %d requests, want 2", len(reqs))
		}
		wantField(t, reqs[1].Header, "Authorization", "Bearer "+bToken)
	})
}

// F10: "caller spellings are not canonicalized otherwise." net/http writes
// a header field name as the map key holds it (net/http, Header: "To use
// non-canonical keys, assign to the map directly"); quality-economy's
// TestQEPreparedHeaderSpellingRewritten found a prepared send rewriting it.
// The listener keeps the bytes of each request.
func TestCallerFieldSpellingKept(t *testing.T) {
	const line = "\r\nx-legacy-token: v\r\n"
	for _, key := range []string{"open", "keyHeader", "keyQuery"} {
		for _, via := range []string{"Request.HTTP", "ParamWriter"} {
			if via == "ParamWriter" && key != "keyQuery" {
				continue // only keyQuery has a parameter to write
			}
			t.Run(key+", "+via, func(t *testing.T) {
				b := newRawServer(t, rawOK)
				c := parseAt(t, credDoc, b.URL, b.URL+"/openapi.json", &openapi.Options{Credentials: credSet()})
				if via == "Request.HTTP" {
					req := mustPrepare(t, c, key, nil)
					req.HTTP.Header["x-legacy-token"] = []string{"v"}
					sendAndClose(t, req)
				} else {
					mustCall(t, c, key, &openapi.Input{ParamWriters: map[string]func(*http.Request) error{
						"p1": func(r *http.Request) error { r.Header["x-legacy-token"] = []string{"v"}; return nil },
					}}, nil)
				}
				raws := b.requests()
				if len(raws) != 1 {
					t.Fatalf("listener received %d requests, want 1", len(raws))
				}
				if !bytes.Contains(raws[0], []byte(line)) {
					t.Errorf("the request does not carry the field as the caller spelled it:\n%s", raws[0])
				}
			})
		}
	}
}

// F20: "placing a query credential preserves every other pair byte for
// byte, empty ones included." doc.go, Credentials: a query credential "that
// replaces a pair of the same name, including one edited into Request.HTTP,
// removes it and goes last"; RFC 3986 section 3.4 leaves the query's syntax
// to the application. conformance's TestE_PairsRewritten found empty pairs
// dropped.
func TestQueryCredentialKeepsOtherPairs(t *testing.T) {
	w := newWire(t, nil)
	c := credClient(t, w, nil)
	for _, tt := range []struct{ query, want string }{
		{"&p1=a&&p2=", "/q?&p1=a&&p2=&api_key=" + qSecret},
		{"p1=a&&p2=b", "/q?p1=a&&p2=b&api_key=" + qSecret},
		{"p1=a&&api_key=old&p2=b", "/q?p1=a&&p2=b&api_key=" + qSecret},
		{"p1=a&p1=b&x", "/q?p1=a&p1=b&x&api_key=" + qSecret},
	} {
		req := mustPrepare(t, c, "keyQuery", nil)
		req.HTTP.URL.RawQuery = tt.query
		sendAndClose(t, req)
		wantURI(t, w.last(t), tt.want)
	}
}

// F12 (test gap: "an edited query pair spelled with %XX or + that a query
// credential replaces"): a pair whose name decodes to the credential's
// (application/x-www-form-urlencoded, as url.QueryUnescape reads it) is
// replaced; another name, a different case included, is kept (doc.go,
// Credentials: "one that replaces a pair of the same name ... removes it").
// A name the client writes is percent-encoded (doc.go, Percent-encoding).
func TestQueryCredentialReplacesEncodedName(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/q":{"get":{"operationId":"keyQuery","security":[{"key_q":[]}]}},"/s":{"get":{"operationId":"spaced","security":[{"spaced":[]}]}}`,
		`"components":{"securitySchemes":{"key_q":{"type":"apiKey","in":"query","name":"api_key"},"spaced":{"type":"apiKey","in":"query","name":"my key"}}}`)
	c := parseFor(t, w, doc, &openapi.Options{Credentials: map[string]openapi.Credential{
		"key_q": openapi.Secret(qSecret), "spaced": openapi.Secret("s-key-5Tg"),
	}})
	for _, tt := range []struct{ key, query, want string }{
		{"keyQuery", "api%5Fkey=old&x=1", "/q?x=1&api_key=" + qSecret},
		{"keyQuery", "api%5fkey=old&x=1", "/q?x=1&api_key=" + qSecret},
		{"keyQuery", "API_KEY=keep&x=1", "/q?API_KEY=keep&x=1&api_key=" + qSecret},
		{"spaced", "my+key=old&x=1", "/s?x=1&my%20key=s-key-5Tg"},
		{"spaced", "my%20key=old&x=1", "/s?x=1&my%20key=s-key-5Tg"},
		{"spaced", "my%2Bkey=keep&x=1", "/s?my%2Bkey=keep&x=1&my%20key=s-key-5Tg"},
	} {
		req := mustPrepare(t, c, tt.key, nil)
		req.HTTP.URL.RawQuery = tt.query
		sendAndClose(t, req)
		wantURI(t, w.last(t), tt.want)
	}
}

// F8: "a present security value that is not an array, an entry that is not
// an object, or scopes that are not an array of strings sets Operation.Err
// (root security: each inheriting operation)." OAS 3.1.2 sections 4.8.1.1
// and 4.8.10.1: security is "[Security Requirement Object]"; section
// 4.8.30.1: a Security Requirement Object's fields are "[string]". describe.go, Operation.Err:
// "Calling an operation with Err set returns a *RequestError wrapping Err";
// Operation.Security: "An empty Security means the operation declares no
// requirement; the client adds no credentials", which a malformed value
// must not be read as (conformance's TestC_MalformedSecurity: sent without
// credentials).
func TestMalformedSecurityValue(t *testing.T) {
	malformed := []string{
		`null`, `{}`, `{"key_h":[]}`, `["key_h"]`, `"key_h"`, `5`, `true`,
		`[5]`, `[{"key_h":[]},"key_h"]`, `[{"key_h":[]},null]`,
		`[{"key_h":"read"}]`, `[{"key_h":["read",1]}]`, `[{"key_h":null}]`, `[{"key_h":{}}]`,
	}
	valid := []string{`[]`, `[{}]`, `[{"key_h":[]}]`, `[{"key_h":["read"]}]`, `[{"key_h":[]},{}]`}
	creds := map[string]openapi.Credential{"key_h": openapi.Secret(hSecret)}

	for _, sec := range append(malformed, valid...) {
		bad := !contains(valid, sec)
		t.Run("an operation's "+sec, func(t *testing.T) {
			w := newWire(t, nil)
			doc := doc31(`"/r":{"get":{"operationId":"getR","security":`+sec+`}},"/ok":{"get":{"operationId":"ok"}}`,
				`"security":[{"key_h":[]}]`, credSchemes)
			c := parseFor(t, w, doc, &openapi.Options{Credentials: creds})
			op := mustOp(t, c, "getR")
			if (op.Err != nil) != bad {
				t.Errorf("security %s: Operation.Err = %v, want set %t", sec, op.Err, bad)
			}
			if bad {
				resp, err := c.Call(t.Context(), "getR", nil, nil)
				refusedBeforeSending(t, w, resp, err)
				if op.Err != nil && !errors.Is(err, op.Err) {
					t.Errorf("security %s: error %v does not wrap Operation.Err", sec, err)
				}
			}
			mustCall(t, c, "ok", nil, nil)
			wantField(t, w.last(t).Header, "X-API-Key", hSecret)
		})
		t.Run("the root's "+sec, func(t *testing.T) {
			w := newWire(t, nil)
			doc := doc31(`"/a":{"get":{"operationId":"a"}},"/b":{"get":{"operationId":"b","security":[{"key_h":[]}]}},"/c":{"get":{"operationId":"c","security":[]}}`,
				`"security":`+sec, credSchemes)
			c := parseFor(t, w, doc, &openapi.Options{Credentials: creds})
			if a := mustOp(t, c, "a"); (a.Err != nil) != bad {
				t.Errorf("root security %s: inheriting operation's Err = %v, want set %t", sec, a.Err, bad)
			}
			for _, key := range []string{"b", "c"} {
				if op := mustOp(t, c, key); op.Err != nil {
					t.Errorf("root security %s: %s, with security of its own, has Err %v", sec, key, op.Err)
				}
			}
			if bad {
				resp, err := c.Call(t.Context(), "a", nil, nil)
				refusedBeforeSending(t, w, resp, err)
			}
			mustCall(t, c, "b", nil, nil)
			wantField(t, w.last(t).Header, "X-API-Key", hSecret)
		})
	}
}

// schemeCase is a Security Scheme Object and whether it is defective.
type schemeCase struct {
	name, scheme string
	defective    bool
}

// checkSchemeCases declares each case's scheme as "s" for an operation
// "s", and checks SecurityScheme.Err; a defective scheme refuses a call
// with a Secret, at the credential's key, and is satisfied by FromTransport
// (describe.go, SecurityScheme.Err: "a defective or missing declaration.
// Alternatives that use it can be applied only when FromTransport satisfies
// it"; doc.go, Credentials: "FromTransport also satisfies a scheme a
// requirement names but the document never declares, or declares
// defectively"). The Secret is given through With, which refuses at each
// call whatever Load would.
func checkSchemeCases(t *testing.T, cases []schemeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWire(t, nil)
			doc := doc31(`"/s":{"get":{"operationId":"s","security":[{"s":[]}]}}`,
				`"components":{"securitySchemes":{"s":`+tc.scheme+`}}`)
			c := parseFor(t, w, doc, nil)
			op := mustOp(t, c, "s")
			if len(op.Security) != 1 || len(op.Security[0].Schemes) != 1 {
				t.Fatalf("Security = %+v, want one alternative of one scheme", op.Security)
			}
			if err := op.Security[0].Schemes[0].Err; (err != nil) != tc.defective {
				t.Errorf("SecurityScheme.Err = %v, want set %t", err, tc.defective)
			}
			if op.Err != nil {
				t.Errorf("a scheme's defect set Operation.Err: %v", op.Err)
			}
			if !tc.defective {
				return
			}
			d := c.With(func(o *openapi.Options) { o.Credentials["s"] = openapi.Secret("s-secret-4Wd") })
			resp, err := d.Call(t.Context(), "s", nil, nil)
			re := refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Settings", re.Settings, true, credKey("s"))
			noSecrets(t, err, "s-secret-4Wd")
			e := c.With(func(o *openapi.Options) { o.Credentials["s"] = openapi.FromTransport() })
			mustCall(t, e, "s", nil, nil)
		})
	}
}

// A8: "an OAuth Flow Object missing a URL its flow requires or its scopes
// map, or that is not an object, sets SecurityScheme.Err, as openIdConnect
// without openIdConnectUrl already does (class: a Security Scheme or OAuth
// Flow Object missing a field OpenAPI requires is defective)." OAS 3.1.2
// section 4.8.29.1: authorizationUrl is REQUIRED for implicit and
// authorizationCode, tokenUrl for password, clientCredentials and
// authorizationCode, scopes for all four, "the map MAY be empty"; refreshUrl
// is optional.
func TestOAuthFlowDefects(t *testing.T) {
	const az, tk = `"authorizationUrl":"https://auth.example.test/a"`, `"tokenUrl":"https://auth.example.test/t"`
	oauth := func(flows string) string { return `{"type":"oauth2","flows":{` + flows + `}}` }
	checkSchemeCases(t, []schemeCase{
		{"implicit without authorizationUrl", oauth(`"implicit":{"scopes":{}}`), true},
		{"implicit without scopes", oauth(`"implicit":{` + az + `}`), true},
		{"password without tokenUrl", oauth(`"password":{"scopes":{}}`), true},
		{"password without scopes", oauth(`"password":{` + tk + `}`), true},
		{"clientCredentials without tokenUrl", oauth(`"clientCredentials":{"scopes":{}}`), true},
		{"clientCredentials without scopes", oauth(`"clientCredentials":{` + tk + `}`), true},
		{"clientCredentials empty", oauth(`"clientCredentials":{}`), true},
		{"authorizationCode without authorizationUrl", oauth(`"authorizationCode":{` + tk + `,"scopes":{}}`), true},
		{"authorizationCode without tokenUrl", oauth(`"authorizationCode":{` + az + `,"scopes":{}}`), true},
		{"authorizationCode without scopes", oauth(`"authorizationCode":{` + az + `,` + tk + `}`), true},
		{"a flow that is not an object", oauth(`"implicit":5`), true},
		{"a flow that is null", oauth(`"password":null`), true},
		{"a tokenUrl that is not a string", oauth(`"clientCredentials":{"tokenUrl":5,"scopes":{}}`), true},
		{"scopes that are not an object", oauth(`"clientCredentials":{` + tk + `,"scopes":["read"]}`), true},
		{"one good flow, one defective", oauth(`"clientCredentials":{` + tk + `,"scopes":{}},"password":{"scopes":{}}`), true},

		{"implicit", oauth(`"implicit":{` + az + `,"scopes":{"read":"r"}}`), false},
		{"password", oauth(`"password":{` + tk + `,"scopes":{}}`), false},
		{"clientCredentials with refreshUrl", oauth(`"clientCredentials":{` + tk + `,"refreshUrl":"/r","scopes":{}}`), false},
		{"authorizationCode", oauth(`"authorizationCode":{` + az + `,` + tk + `,"scopes":{}}`), false},
		{"all four", oauth(`"implicit":{` + az + `,"scopes":{}},"password":{` + tk + `,"scopes":{}},"clientCredentials":{` + tk + `,"scopes":{}},"authorizationCode":{` + az + `,` + tk + `,"scopes":{}}`), false},
	})
}

// F12 (test gap: "each newScheme defect") and R5: "SecurityScheme.Err for a
// header apiKey naming Content-Type, Cookie or a derived field follows the
// existing rule for Options.Header and header parameters (checkHeader,
// derivedFields); a cookie apiKey name that is not a token (RFC 6265
// section 4.1.1) is Err." OAS 3.1.2 section 4.8.27.1: flows is REQUIRED for
// oauth2, openIdConnectUrl for openIdConnect, scheme for http; RFC 9110
// section 11.1: an auth-scheme is a token. doc.go, Header fields: Host,
// Content-Length, Transfer-Encoding, Trailer, Connection, Keep-Alive,
// Proxy-Connection and Upgrade are derived or forbidden; Content-Type and
// Cookie are the client's.
func TestSecuritySchemeDefectsEveryBranch(t *testing.T) {
	header := func(name string) string { return `{"type":"apiKey","in":"header","name":"` + name + `"}` }
	cookie := func(name string) string { return `{"type":"apiKey","in":"cookie","name":"` + name + `"}` }
	var cases []schemeCase
	for _, f := range []string{"Content-Type", "content-type", "Cookie", "COOKIE", "Host", "Content-Length", "Transfer-Encoding",
		"Trailer", "Connection", "Keep-Alive", "Proxy-Connection", "upgrade", "X Key", "X-Key:"} {
		cases = append(cases, schemeCase{"header " + f, header(f), true})
	}
	for _, f := range []string{"a=b", "a b", "a;b", "a,b", "(a)"} {
		cases = append(cases, schemeCase{"cookie " + f, cookie(f), true})
	}
	cases = append(cases,
		schemeCase{"oauth2 without flows", `{"type":"oauth2"}`, true},
		schemeCase{"oauth2 with flows not an object", `{"type":"oauth2","flows":5}`, true},
		schemeCase{"openIdConnect without its URL", `{"type":"openIdConnect"}`, true},
		schemeCase{"openIdConnect with a URL that is not a string", `{"type":"openIdConnect","openIdConnectUrl":5}`, true},
		schemeCase{"http scheme not a token", `{"type":"http","scheme":"bear er"}`, true},
		schemeCase{"http scheme empty", `{"type":"http","scheme":""}`, true},
		schemeCase{"header Authorization", header("Authorization"), false},
		schemeCase{"header X-Key", header("X-Key"), false},
		schemeCase{"cookie sid", cookie("sid"), false},
		schemeCase{"cookie with token punctuation", cookie("a.b-c_d!e"), false},
	)
	checkSchemeCases(t, cases)
}

// A2 (not a defect; T1 clarification): "a *url.Error the caller's own
// transport creates is the caller's error, passed on as it is (Q13);
// rewriting caller error trees cannot be done in general". doc.go,
// Outcomes: "No credential appears in the text of an error the client
// creates, nor in the URL of the *url.Error the http.Client returns, which
// names the request without the credentials the client added. Errors made by
// the caller's own code, such as its transport or a credential source, are
// passed on as they are, even when their text quotes a URL." Astra's
// reproduction: a transport returning &url.Error{URL: r.URL.String(), ...}.
func TestCallerURLErrorPassedOn(t *testing.T) {
	var made atomic.Pointer[url.Error]
	rt := &memRT{answer: func(r *http.Request) (*http.Response, error) {
		e := &url.Error{Op: "Get", URL: r.URL.String(), Err: io.EOF}
		made.Store(e)
		return nil, e
	}}
	c := parseAt(t, credDoc, "https://api.example.test", "https://api.example.test/openapi.json",
		&openapi.Options{HTTPClient: &http.Client{Transport: rt}, Credentials: credSet()})
	for _, via := range []string{"Call", "Send"} {
		var err error
		if via == "Call" {
			_, err = c.Call(t.Context(), "keyQuery", nil, nil)
		} else {
			_, err = mustPrepare(t, c, "keyQuery", nil).Send(t.Context())
		}
		mine := made.Load()
		if mine == nil {
			t.Fatalf("%s: the transport did not run", via)
		}
		var outer *url.Error
		if !errors.As(err, &outer) || outer == mine {
			t.Fatalf("%s: error %v, want the http.Client's *url.Error around the transport's", via, err)
		}
		wantNoSecretText(t, via+": the http.Client's *url.Error URL", outer.URL, qSecret)
		found := false
		walkErrors(err, func(e error) {
			if e == error(mine) {
				found = true
			}
		})
		if !found {
			t.Errorf("%s: the transport's own *url.Error is not in the chain", via)
		}
		if want := "https://api.example.test/q?api_key=" + qSecret; mine.URL != want {
			t.Errorf("%s: the transport's *url.Error names %q, want it unchanged, %q", via, mine.URL, want)
		}
		if !errors.Is(err, io.EOF) {
			t.Errorf("%s: error %v does not wrap the transport's cause", via, err)
		}
	}
}
