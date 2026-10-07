package openapi_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for redirects: the client alone follows them; the unsigned
// view never holds a credential; a 3xx the client cannot follow is the
// outcome, and an error that ends a chain after a response arrived returns
// that response; errors on a hop never quote the Location; HTTPClient.Timeout
// bounds the whole chain; CheckRedirect sees the hop's body; a caller-set
// Request.Host is kept across a relative Location; and the hop rules for a
// caller's method, content fields, an empty User-Agent, a generated
// Content-Type, the default port and header spellings. Upload generations
// across hops are in upload_inflight_read_test.go; credentials are in
// credential_placement_test.go.

// plainRedirDoc has, on /r, a query parameter q and an operation per
// method, with no security, for the redirect rules that do not involve
// credentials.
var plainRedirDoc = doc31(`
	"/r":{
		"parameters":[{"name":"q","in":"query"}],
		"get":{"operationId":"getR"},
		"post":{"operationId":"postR","requestBody":{"content":{"application/json":{}}}},
		"put":{"operationId":"putR","requestBody":{"content":{"application/json":{}}}}
	}`)

// echoQuery answers with status and a Location of prefix followed by the
// request's query, as a server that canonicalizes a URL and keeps its query
// does (net/http's ServeMux redirects "/pets" to "/pets/" so).
func echoQuery(status int, prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", prefix+"?"+r.URL.RawQuery)
		w.WriteHeader(status)
	}
}

// redirectWithBody answers with status, a Location and a body.
func redirectWithBody(status int, loc, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", loc)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// echoed is the query the first request of redirInput("GET") carries, as a
// server that repeats it writes it into a Location: the parameter q and the
// query credential the client placed.
var echoed = "q=qv&api_key=" + qSecret

// wantNoSecretText fails if text, found at where, holds any of secrets in
// any of the forms secretForms lists.
func wantNoSecretText(t *testing.T, where, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		for _, form := range secretForms(s) {
			if strings.Contains(text, form) {
				t.Errorf("%s %q holds the credential %q", where, text, s)
			}
		}
	}
}

// checkViews records, for CheckRedirect, the URL and header fields of each
// hop and of every request in via.
type checkViews struct {
	mu   sync.Mutex
	reqs []*http.Request // copies of what CheckRedirect saw
	vias []int
	err  error // returned to the client
}

func (v *checkViews) check(r *http.Request, via []*http.Request) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, x := range append([]*http.Request{r}, via...) {
		c := &http.Request{Method: x.Method, Header: x.Header.Clone()}
		if x.URL != nil {
			u := *x.URL
			c.URL = &u
		}
		v.reqs = append(v.reqs, c)
	}
	v.vias = append(v.vias, len(via))
	return v.err
}

// wantClean fails if any request CheckRedirect saw holds a credential the
// client places, in its URL or a header field.
func (v *checkViews) wantClean(t *testing.T) {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.reqs) == 0 {
		t.Fatalf("CheckRedirect was not consulted")
	}
	for i, r := range v.reqs {
		if r.URL == nil {
			t.Errorf("CheckRedirect view %d has no URL", i)
			continue
		}
		wantNoSecretText(t, fmt.Sprintf("CheckRedirect view %d: URL", i), r.URL.String(), redirSecrets...)
		for name, vs := range r.Header {
			for _, val := range vs {
				wantNoSecretText(t, fmt.Sprintf("CheckRedirect view %d: %s", i, name), val, redirSecrets...)
			}
		}
	}
}

// callOrSend makes the call key with in through Call, or through Prepare and
// Send, closing a Send's body.
func callOrSend(t *testing.T, c *openapi.Client, via, key string, in *openapi.Input) (*openapi.Response, error) {
	t.Helper()
	if via == "Call" {
		return c.Call(t.Context(), key, in, nil)
	}
	resp, err := mustPrepare(t, c, key, in).Send(t.Context())
	if resp != nil && err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return resp, err
}

// The unsigned view never holds a credential value: every hop's unsigned
// URL drops each query pair named as a query credential of the applied
// alternative, on every hop, cross-origin included (a credential the server
// reflects is not carried onward), and the credential is appended only
// where the origin rule allows. Response.Request, via, CheckRedirect's view
// and every URL in an error the client creates come from the unsigned view
// (client.go, Redirects: "a query credential goes only on the request the
// client builds, never onto a Location"). client.go, Response: "Its
// Request is the last request sent, after any redirects, without the
// credentials the client added to its URL and header fields or the cookies
// the HTTPClient's Jar supplied, and so is every earlier request reachable
// from it. The responses in that chain hold what the server sent." The
// servers repeat the query, as a trailing-slash redirect does.
func TestEchoedQueryCredentialNotInUnsignedView(t *testing.T) {
	for _, origin := range []string{"same origin", "another origin"} {
		for _, via := range []string{"Call", "Send"} {
			t.Run(origin+", "+via, func(t *testing.T) {
				b := newWire(t, nil)
				prefix, want := "/next", ""
				if origin == "another origin" {
					prefix = b.URL + "/b"
				}
				a := newWire(t, routes(map[string]http.HandlerFunc{"/r": echoQuery(302, prefix)}))
				if origin == "same origin" {
					want = a.URL + "/next?q=qv"
				} else {
					want = b.URL + "/b?q=qv"
				}
				views := &checkViews{}
				o := redirOptions(fixedSource(bToken))
				o.HTTPClient = &http.Client{CheckRedirect: views.check}
				c := parseFor(t, a, redirDoc, o)
				resp, err := callOrSend(t, c, via, "getR", redirInput("GET"))
				if err != nil {
					t.Fatalf("%s: %v", via, err)
				}
				wantNoCredentialsIn(t, resp.Request)
				if got := resp.Request.URL.String(); got != want {
					t.Errorf("Response.Request.URL = %q, want the unsigned hop %q", got, want)
				}
				views.wantClean(t)
				// The responses hold what the server sent.
				if prev := resp.Request.Response; prev == nil {
					t.Errorf("Response.Request.Response is nil")
				} else if loc := prev.Header.Get("Location"); loc != prefix+"?"+echoed {
					t.Errorf("the 302's Location = %q, want what the server sent, %q", loc, prefix+"?"+echoed)
				}
				// On the wire: within the origin the credential is placed
				// once, last; another origin receives no credential.
				if origin == "same origin" {
					reqs := a.requests()
					if len(reqs) != 2 {
						t.Fatalf("server received %d requests, want 2", len(reqs))
					}
					wantPlaced(t, reqs[1], "/next?q=qv&")
				} else {
					hop := b.only(t)
					wantURI(t, hop, "/b?q=qv")
					wantStripped(t, hop)
				}
			})
		}
	}

	// A pair of the credential's name the Location gives another value is
	// dropped too, and replaced within the origin, as a query credential
	// replaces a pair of the same name (doc.go, Credentials).
	t.Run("another value under the credential's name", func(t *testing.T) {
		for _, origin := range []string{"same origin", "another origin"} {
			b := newWire(t, nil)
			loc := "/next?api_key=other-1Zx&x=1"
			if origin == "another origin" {
				loc = b.URL + "/b?api_key=other-1Zx&x=1"
			}
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, loc)}))
			views := &checkViews{}
			o := redirOptions(fixedSource(bToken))
			o.HTTPClient = &http.Client{CheckRedirect: views.check}
			c := parseFor(t, a, redirDoc, o)
			resp := mustCall(t, c, "getR", redirInput("GET"), nil)
			want := a.URL + "/next?x=1"
			if origin == "another origin" {
				want = b.URL + "/b?x=1"
				wantURI(t, b.only(t), "/b?x=1")
			} else if reqs := a.requests(); len(reqs) != 2 {
				t.Fatalf("server received %d requests, want 2", len(reqs))
			} else {
				wantURI(t, reqs[1], "/next?x=1&api_key="+qSecret)
			}
			if got := resp.Request.URL.String(); got != want {
				t.Errorf("%s: Response.Request.URL = %q, want %q", origin, got, want)
			}
			views.mu.Lock()
			if len(views.reqs) == 0 {
				t.Errorf("%s: CheckRedirect was not consulted", origin)
			} else if got := views.reqs[0].URL.String(); got != want {
				t.Errorf("%s: CheckRedirect saw the hop %q, want %q", origin, got, want)
			}
			views.mu.Unlock()
		}
	})

	// Another origin that repeats the query back to the first: what the
	// server reflected is not carried onward, and stripping stays sticky.
	t.Run("carried onward", func(t *testing.T) {
		var a *wire
		b := newWire(t, nil)
		a = newWire(t, routes(map[string]http.HandlerFunc{"/r": echoQuery(302, b.URL+"/b")}))
		b.setAnswer(routes(map[string]http.HandlerFunc{"/b": echoQuery(302, a.URL+"/back")}))
		c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
		resp := mustCall(t, c, "getR", redirInput("GET"), nil)
		wantURI(t, b.only(t), "/b?q=qv")
		reqs := a.requests()
		if len(reqs) != 2 {
			t.Fatalf("first origin received %d requests, want 2", len(reqs))
		}
		wantURI(t, reqs[1], "/back?q=qv")
		wantStripped(t, reqs[1])
		wantNoCredentialsIn(t, resp.Request)
	})
}

// The unsigned view with net/http's own ServeMux: a pattern "/pets/" answers
// "/pets?api_key=..." with a 301 to "/pets/?api_key=...", the query kept
// (net/http, ServeMux: a request "naming the subtree root without its
// trailing slash" is redirected "to the subtree root (adding the trailing
// slash)"). The credential is placed on the hop once, and the Response's
// Request holds none.
func TestServeMuxTrailingSlashKeepsNoCredential(t *testing.T) {
	var (
		mu      sync.Mutex
		targets []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/pets/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "[]")
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targets = append(targets, r.RequestURI)
		mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	doc := doc31(`"/pets":{"get":{"operationId":"listPets"}}`, `"security":[{"key_q":[]}]`, credSchemes)
	c := parseAt(t, doc, srv.URL, srv.URL+"/openapi.json", &openapi.Options{Redirects: openapi.FollowAll,
		Credentials: map[string]openapi.Credential{"key_q": openapi.Secret(qSecret)}})
	var pets []any
	resp := mustCall(t, c, "listPets", nil, &pets)
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"/pets?api_key=" + qSecret, "/pets/?api_key=" + qSecret}; fmt.Sprint(targets) != fmt.Sprint(want) {
		t.Errorf("request targets %q, want %q", targets, want)
	}
	wantNoSecretText(t, "Response.Request.URL", resp.Request.URL.String(), qSecret)
	if resp.Request.URL.Path != "/pets/" {
		t.Errorf("Response.Request.URL = %q, want the hop to /pets/", resp.Request.URL)
	}
	if prev := resp.Request.Response; prev == nil || prev.Header.Get("Location") != "/pets/?api_key="+qSecret {
		t.Errorf("the 301 does not hold the Location the server sent")
	}
}

// Errors the client creates on a hop name the unsigned hop's URL
// (Redacted), never the Location text, since every URL in an error the
// client creates comes from the unsigned view. doc.go, Outcomes: "No
// credential appears in the text of an error the client creates, nor in the
// URL of the *url.Error the http.Client returns, which names the request
// without the credentials the client added." An error that ends a chain
// after a response arrived (CheckRedirect's error or the hop limit, a
// SecretFunc or GetBody failure on a hop, a hop's transport failure)
// returns that last Response, its body closed, with the *url.Error (doc.go,
// Outcomes: "Whenever a response arrived, the [*Response] is returned, even
// with an error"). Each server repeats the query into its Location.
func TestHopErrorsNameTheUnsignedHop(t *testing.T) {
	errStop := errors.New("caller stopped the redirect")
	errHop := errors.New("token refresh failed")
	errReopen := errors.New("cannot reopen the body")

	// wantHopError checks err against the unsigned hop URL want, and that
	// the last response, of status, is returned with its body closed.
	wantHopError := func(t *testing.T, resp *openapi.Response, err error, want string, status int, cause error) {
		t.Helper()
		var ue *url.Error
		if !errors.As(err, &ue) {
			t.Fatalf("error %v (%T), want a *url.Error", err, err)
		}
		if ue.URL != want {
			t.Errorf("the *url.Error names %q, want the unsigned hop %q", ue.URL, want)
		}
		if cause != nil && !errors.Is(err, cause) {
			t.Errorf("error %v does not wrap %v", err, cause)
		}
		noSecrets(t, err, redirSecrets...)
		if resp == nil || resp.StatusCode != status {
			t.Errorf("Response = %v, want the %d that arrived", resp, status)
		} else {
			wantBodyClosed(t, resp)
		}
	}

	for _, origin := range []string{"same origin", "another origin"} {
		t.Run("CheckRedirect error, "+origin, func(t *testing.T) {
			b := newWire(t, nil)
			prefix := "/next"
			if origin == "another origin" {
				prefix = b.URL + "/b"
			}
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": echoQuery(302, prefix)}))
			o := redirOptions(fixedSource(bToken))
			o.HTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errStop }}
			c := parseFor(t, a, redirDoc, o)
			for _, via := range []string{"Call", "Send"} {
				resp, err := callOrSend(t, c, via, "getR", redirInput("GET"))
				want := a.URL + "/next?q=qv"
				if origin == "another origin" {
					want = b.URL + "/b?q=qv"
				}
				wantHopError(t, resp, err, want, 302, errStop)
			}
			b.nothingSent(t)
		})

		t.Run("transport failure, "+origin, func(t *testing.T) {
			b := newWire(t, routes(map[string]http.HandlerFunc{"/dead": hijackClose}))
			prefix := "/dead"
			if origin == "another origin" {
				prefix = b.URL + "/dead"
			}
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": echoQuery(302, prefix), "/dead": hijackClose}))
			c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
			for _, via := range []string{"Call", "Send"} {
				resp, err := callOrSend(t, c, via, "getR", redirInput("GET"))
				want := a.URL + "/dead?q=qv"
				if origin == "another origin" {
					want = b.URL + "/dead?q=qv"
				}
				wantHopError(t, resp, err, want, 302, nil)
			}
		})

		t.Run("GetBody failure, "+origin, func(t *testing.T) {
			b := newWire(t, nil)
			prefix := "/next"
			if origin == "another origin" {
				prefix = b.URL + "/b"
			}
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": echoQuery(307, prefix)}))
			c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
			req := mustPrepare(t, c, "postR", redirInput("POST"))
			req.HTTP.Body = io.NopCloser(strings.NewReader(`{"n":2}`))
			req.HTTP.ContentLength = 7
			req.HTTP.GetBody = func() (io.ReadCloser, error) { return nil, errReopen }
			resp, err := req.Send(t.Context())
			want := a.URL + "/next?q=qv"
			if origin == "another origin" {
				want = b.URL + "/b?q=qv"
			}
			wantHopError(t, resp, err, want, 307, errReopen)
			b.nothingSent(t)
		})
	}

	// A credential source is called only on a hop within the origin.
	t.Run("SecretFunc failure, same origin", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": echoQuery(302, "/next")}))
		src := &source{fn: func(_ context.Context, n int64) (string, error) {
			if n == 1 {
				return bToken, nil
			}
			return "", errHop
		}}
		c := parseFor(t, a, redirDoc, redirOptions(src))
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantHopError(t, resp, err, a.URL+"/next?q=qv", 302, errHop)
		if n := a.count(); n != 1 {
			t.Errorf("server received %d requests, want only the first", n)
		}
	})
}

// A 3xx the client cannot follow is the outcome: a Location that url.Parse
// does not accept is no Location, so its text is never quoted. client.go,
// Redirects: "Only 301, 302, 303, 307 and 308 with a Location that url.Parse
// accepts can be followed, as net/http follows them ... A 3xx not followed is
// the outcome, a *StatusError." The client alone follows redirects, so
// net/http's Client.do does not parse the Location either, and FollowNone
// gives the same outcome. An unparsable Location echoing the query is never
// quoted. client.go, Response: "The responses in that chain hold what the
// server sent."
func TestUnparsableLocationIsNotFollowed(t *testing.T) {
	for _, follow := range []openapi.Redirects{openapi.FollowNone, openapi.FollowAll} {
		t.Run(fmt.Sprintf("Redirects %d", follow), func(t *testing.T) {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": func(w http.ResponseWriter, r *http.Request) {
				redirectWithBody(302, "/r%zz?"+r.URL.RawQuery, "moved")(w, r)
			}}))
			o := redirOptions(fixedSource(bToken))
			o.Redirects = follow
			c := parseFor(t, a, redirDoc, o)

			resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
			var se *openapi.StatusError
			if !errors.As(err, &se) || se.StatusCode != 302 {
				t.Fatalf("Call error %v (%T), want a *StatusError for the 302", err, err)
			}
			if resp == nil || resp.StatusCode != 302 {
				t.Errorf("Call returned Response %v, want the 302", resp)
			}
			if got := se.Header.Get("Location"); got != "/r%zz?"+echoed {
				t.Errorf("Location = %q, want what the server sent", got)
			}
			if strings.Contains(err.Error(), "%zz") {
				t.Errorf("the error quotes the Location: %q", err)
			}
			noSecrets(t, err, redirSecrets...)

			resp, err = mustPrepare(t, c, "getR", redirInput("GET")).Send(t.Context())
			if err != nil {
				t.Fatalf("Send: %v, want the 302 returned", err)
			}
			if resp.StatusCode != 302 || resp.Header.Get("Location") != "/r%zz?"+echoed {
				t.Errorf("Send returned %d with Location %q, want the 302 as sent", resp.StatusCode, resp.Header.Get("Location"))
			}
			resp.Body.Close()
			if n := a.count(); n != 2 {
				t.Errorf("server received %d requests, want the two first requests only", n)
			}
		})
	}
}

// wantBodyClosed fails unless nothing more can be read from resp's Body: an
// error that ends a chain returns the last Response with its body closed.
func wantBodyClosed(t *testing.T, resp *openapi.Response) {
	t.Helper()
	if resp == nil || resp.Body == nil {
		return
	}
	if n, _ := resp.Body.Read(make([]byte, 16)); n != 0 {
		t.Errorf("the returned Response's body is still readable (%d bytes)", n)
	}
}

// An error that ends a chain after a response arrived (CheckRedirect's
// error or the hop limit, a SecretFunc or GetBody failure on a hop, a hop's
// transport failure, a Timeout spent before a hop) returns that last
// Response, its body closed, with the *url.Error, as net/http does for
// CheckRedirect. ErrUseLastResponse keeps returning it open, as the
// outcome. doc.go,
// Outcomes: "Whenever a response arrived, the [*Response] is returned, even
// with an error"; a transport failure is "the *url.Error from the
// http.Client", not a *StatusError. credential.go, SecretFunc: "On a
// redirect hop the first request has already been sent, so an error or an
// empty secret ends the call with a *url.Error wrapping f's error, along
// with the last response, its body closed." net/http's own Client.do returns
// the 3xx with a CheckRedirect error (go1.25 client.go, "Special case for Go
// 1 compatibility").
func TestErrorEndingAChainReturnsTheLastResponse(t *testing.T) {
	errStop := errors.New("caller stopped the redirect")
	wantChainError := func(t *testing.T, resp *openapi.Response, err error, status int, cause error) {
		t.Helper()
		var ue *url.Error
		if !errors.As(err, &ue) {
			t.Fatalf("error %v (%T), want a *url.Error", err, err)
		}
		if cause != nil && !errors.Is(err, cause) {
			t.Errorf("error %v does not wrap %v", err, cause)
		}
		var se *openapi.StatusError
		if errors.As(err, &se) {
			t.Errorf("error %v is a *StatusError; an error ending a chain is the *url.Error", err)
		}
		if resp == nil {
			t.Fatalf("Response is nil, want the %d that arrived", status)
		}
		if resp.StatusCode != status {
			t.Errorf("Response status %d, want %d", resp.StatusCode, status)
		}
		wantBodyClosed(t, resp)
		wantNoCredentialsIn(t, resp.Request)
		noSecrets(t, err, redirSecrets...)
	}

	t.Run("CheckRedirect error", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirectWithBody(302, "/next", "moved")}))
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errStop }}
		c := parseFor(t, a, redirDoc, o)
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainError(t, resp, err, 302, errStop)
		resp, err = mustPrepare(t, c, "getR", redirInput("GET")).Send(t.Context())
		wantChainError(t, resp, err, 302, errStop)
	})
	t.Run("the hop limit", func(t *testing.T) {
		a := newWire(t, chain(11))
		c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainError(t, resp, err, 302, nil)
		if n := a.count(); n != 10 {
			t.Errorf("server received %d requests, want 10 (client.go, Redirects: the chain stops after 10 requests)", n)
		}
	})
	t.Run("a SecretFunc failure on a hop", func(t *testing.T) {
		errHop := errors.New("token refresh failed")
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirectWithBody(307, "/next", "moved")}))
		src := &source{fn: func(_ context.Context, n int64) (string, error) {
			if n == 1 {
				return bToken, nil
			}
			return "", errHop
		}}
		c := parseFor(t, a, redirDoc, redirOptions(src))
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainError(t, resp, err, 307, errHop)
	})
	t.Run("an empty secret on a hop", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirectWithBody(307, "/next", "moved")}))
		src := &source{fn: func(_ context.Context, n int64) (string, error) {
			if n == 1 {
				return bToken, nil
			}
			return "", nil
		}}
		c := parseFor(t, a, redirDoc, redirOptions(src))
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainError(t, resp, err, 307, nil)
	})
	t.Run("a GetBody failure on a hop", func(t *testing.T) {
		errReopen := errors.New("cannot reopen the body")
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirectWithBody(307, "/next", "moved")}))
		c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
		req := mustPrepare(t, c, "postR", redirInput("POST"))
		req.HTTP.Body = io.NopCloser(strings.NewReader(`{"n":2}`))
		req.HTTP.ContentLength = 7
		req.HTTP.GetBody = func() (io.ReadCloser, error) { return nil, errReopen }
		resp, err := req.Send(t.Context())
		wantChainError(t, resp, err, 307, errReopen)
	})
	t.Run("a transport failure on a hop", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirectWithBody(302, "/dead", "moved"), "/dead": hijackClose}))
		c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainError(t, resp, err, 302, nil)
		resp, err = mustPrepare(t, c, "getR", redirInput("GET")).Send(t.Context())
		wantChainError(t, resp, err, 302, nil)
	})
	t.Run("a Timeout spent before a hop", func(t *testing.T) {
		const timeout = 300 * time.Millisecond
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirectWithBody(302, "/next", "moved")}))
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			time.Sleep(timeout + 100*time.Millisecond)
			return nil
		}}
		c := parseFor(t, a, redirDoc, o)
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainError(t, resp, err, 302, context.DeadlineExceeded)
		if n := a.count(); n != 1 {
			t.Errorf("server received %d requests, want the first only", n)
		}
	})
	t.Run("ErrUseLastResponse returns it open", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirectWithBody(302, "/next", "moved")}))
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		c := parseFor(t, a, redirDoc, o)
		resp, err := mustPrepare(t, c, "getR", redirInput("GET")).Send(t.Context())
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 302 || string(body) != "moved" {
			t.Errorf("Send returned %d with body %q, want the 302 open, reading %q", resp.StatusCode, body, "moved")
		}
		_, err = c.Call(t.Context(), "getR", redirInput("GET"), nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) || se.StatusCode != 302 || string(se.Content) != "moved" {
			t.Errorf("Call error %v, want a *StatusError for the 302 holding its body", err)
		}
	})
}

// slowly waits d, or until the request is abandoned, before h answers.
func slowly(d time.Duration, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(d):
			h(w, r)
		case <-r.Context().Done():
		}
	}
}

// wantTimeout fails unless err is a timeout as net/http reports Client.Timeout:
// a *url.Error whose Timeout reports true and that matches
// context.DeadlineExceeded (go1.25 net/http, timeoutError: "Timeout() bool {
// return true }", "Is(err error) bool { return err == context.DeadlineExceeded
// }").
func wantTimeout(t *testing.T, err error) {
	t.Helper()
	var ue *url.Error
	if !errors.As(err, &ue) || !ue.Timeout() {
		t.Errorf("error %v (%T), want a *url.Error reporting a timeout", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not match context.DeadlineExceeded", err)
	}
}

// HTTPClient.Timeout bounds the whole chain, as net/http's own loop does
// (its doc: "includes connection time, any redirects, and reading the
// response body"). Each later hop is sent with the time that remains; none
// left ends the call as net/http's timeout does. The error ends a chain
// after a response arrived, so that Response is returned with it, its body
// closed.
func TestTimeoutBoundsTheWholeChain(t *testing.T) {
	t.Run("hops share the time", func(t *testing.T) {
		const timeout = 600 * time.Millisecond
		ok := func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, "{}")
		}
		// Each request alone is well within the Timeout; the chain is not.
		a := newWire(t, routes(map[string]http.HandlerFunc{
			"/r":    slowly(300*time.Millisecond, redirect(302, "/next")),
			"/next": slowly(450*time.Millisecond, ok),
		}))
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{Timeout: timeout}
		c := parseFor(t, a, redirDoc, o)
		start := time.Now()
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantTimeout(t, err)
		if resp == nil || resp.StatusCode != 302 {
			t.Errorf("Response %v, want the 302 that arrived (TQ2)", resp)
		} else {
			wantBodyClosed(t, resp)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("the call took %v with a Timeout of %v", d, timeout)
		}
		noSecrets(t, err, redirSecrets...)
	})
	t.Run("none left", func(t *testing.T) {
		const timeout = 300 * time.Millisecond
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next")}))
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			time.Sleep(timeout + 100*time.Millisecond) // the chain's time runs out before the hop
			return nil
		}}
		c := parseFor(t, a, redirDoc, o)
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantTimeout(t, err)
		if resp == nil || resp.StatusCode != 302 {
			t.Errorf("Response %v, want the 302 that arrived (TQ2)", resp)
		} else {
			wantBodyClosed(t, resp)
		}
		if n := a.count(); n != 1 {
			t.Errorf("server received %d requests, want the first only: no time remained for the hop", n)
		}
		noSecrets(t, err, redirSecrets...)
	})
}

// CheckRedirect sees the hop as net/http passes it, with Body set (from
// GetBody) whenever the hop resends the body. When CheckRedirect refuses
// (ErrUseLastResponse or an error), the client drops that upload
// generation and closes the body, as it does for a 3xx it does not
// follow. client.go, Redirects: "A 303 is followed with GET (HEAD
// stays HEAD) and no body. A 301 or 302 changes POST to GET with no body, and
// keeps any other method and its body, as 307 and 308 do."
func TestCheckRedirectSeesTheHopBody(t *testing.T) {
	for _, tt := range []struct {
		status int
		key    string
		resend bool
	}{
		{307, "postR", true}, {308, "postR", true}, {301, "putR", true}, {302, "putR", true},
		{303, "putR", false}, {301, "postR", false}, {302, "postR", false},
	} {
		t.Run(fmt.Sprintf("%d %s", tt.status, tt.key), func(t *testing.T) {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(tt.status, "/next")}))
			var (
				mu      sync.Mutex
				hasBody bool
				getBody bool
				length  int64
				calls   int
			)
			o := redirOptions(fixedSource(bToken))
			o.HTTPClient = &http.Client{CheckRedirect: func(r *http.Request, _ []*http.Request) error {
				mu.Lock()
				defer mu.Unlock()
				calls++
				hasBody, getBody, length = r.Body != nil && r.Body != http.NoBody, r.GetBody != nil, r.ContentLength
				return nil
			}}
			c := parseFor(t, a, redirDoc, o)
			mustCall(t, c, tt.key, redirInput(redirKeys[tt.key]), nil)
			reqs := a.requests()
			if len(reqs) != 2 {
				t.Fatalf("server received %d requests, want 2", len(reqs))
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != 1 {
				t.Fatalf("CheckRedirect called %d times, want 1", calls)
			}
			if tt.resend {
				if !hasBody || !getBody || length != int64(len(reqs[0].Body)) {
					t.Errorf("CheckRedirect saw Body set %t, GetBody set %t, ContentLength %d; want the body to be resent, of %d bytes",
						hasBody, getBody, length, len(reqs[0].Body))
				}
				if !bytes.Equal(reqs[1].Body, reqs[0].Body) {
					t.Errorf("the hop sent %q, want %q", reqs[1].Body, reqs[0].Body)
				}
			} else if hasBody || length != 0 {
				t.Errorf("CheckRedirect saw Body set %t, ContentLength %d; the hop sends no body", hasBody, length)
			}
		})
	}

	for name, refusal := range map[string]error{
		"ErrUseLastResponse": http.ErrUseLastResponse,
		"an error":           errors.New("caller stopped the redirect"),
	} {
		t.Run("a refusal closes the body, "+name, func(t *testing.T) {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/next")}))
			o := redirOptions(fixedSource(bToken))
			o.HTTPClient = &http.Client{CheckRedirect: func(r *http.Request, _ []*http.Request) error { return refusal }}
			c := parseFor(t, a, redirDoc, o)
			req := mustPrepare(t, c, "postR", redirInput("POST"))
			const payload = `{"n":2}`
			var (
				mu    sync.Mutex
				given []*closeRecorder
			)
			req.HTTP.Body = io.NopCloser(strings.NewReader(payload))
			req.HTTP.ContentLength = int64(len(payload))
			req.HTTP.GetBody = func() (io.ReadCloser, error) {
				mu.Lock()
				defer mu.Unlock()
				b := &closeRecorder{Reader: strings.NewReader(payload)}
				given = append(given, b)
				return b, nil
			}
			resp, _ := req.Send(t.Context())
			if resp == nil {
				t.Fatalf("Send returned no Response for the 307 that arrived")
			}
			resp.Body.Close()
			mu.Lock()
			if len(given) != 1 || !given[0].wasClosed() {
				t.Errorf("GetBody gave %d bodies; want one, for the hop CheckRedirect saw, closed once it refused", len(given))
			}
			mu.Unlock()
			// The dropped generation is not waited for; the first request's
			// body was read completely.
			if err := waitResult(t, resp); err != nil {
				t.Errorf("WaitRequest = %v, want nil", err)
			}
			if n := a.count(); n != 1 {
				t.Errorf("server received %d requests, want 1", n)
			}
		})
	}
}

// legacyRT is a RoundTripper written before contexts: it ignores the
// request's context and Cancel channel and stops only when CancelRequest is
// called, which net/http calls on a Client.Timeout (go1.25 client.go,
// setRequestCancel: "The first way, used only for RoundTripper
// implementations written before Go 1.5 or Go 1.6").
type legacyRT struct {
	once     sync.Once
	canceled chan struct{}
}

func (l *legacyRT) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case <-l.canceled:
		return nil, errors.New("legacy transport: canceled")
	case <-time.After(5 * time.Second):
		return nil, errors.New("legacy transport: CancelRequest never came")
	}
}

func (l *legacyRT) CancelRequest(*http.Request) { l.once.Do(func() { close(l.canceled) }) }

// The client alone follows redirects. net/http's Client.do acts on a
// followable 3xx before CheckRedirect, so a CheckRedirect alone cannot make
// the send single-hop. The client's http.Client copy wraps the caller's
// Transport in a RoundTripper that moves Location off a 301/302/303/307/308
// response, and the client restores it after Do; Client.do then returns at its
// own `loc == ""` check, never parsing Location, calling GetBody or consulting
// CheckRedirect. The wrapper forwards CancelRequest when the inner transport
// has it. client.go, Request.HTTP: "A send takes HTTP.Body the first time and
// GetBody for every replay"; Redirects: "A 3xx not followed is the outcome, a
// *StatusError."
func TestNetHTTPFollowsNothing(t *testing.T) {
	const payload = `{"n":2}`
	// prepareCountingBody prepares postR with a body the caller set, whose
	// GetBody counts its calls and fails when fail is set.
	prepareCountingBody := func(t *testing.T, c *openapi.Client, fail bool) (*openapi.Request, *atomic.Int32) {
		req := mustPrepare(t, c, "postR", nil)
		var n atomic.Int32
		req.HTTP.Body = io.NopCloser(strings.NewReader(payload))
		req.HTTP.ContentLength = int64(len(payload))
		req.HTTP.GetBody = func() (io.ReadCloser, error) {
			n.Add(1)
			if fail {
				return nil, errors.New("cannot reopen the body")
			}
			return io.NopCloser(strings.NewReader(payload)), nil
		}
		return req, &n
	}

	t.Run("GetBody is called once per hop followed", func(t *testing.T) {
		for _, tt := range []struct {
			follow openapi.Redirects
			hops   int
			want   int32
		}{{openapi.FollowNone, 1, 0}, {openapi.FollowAll, 1, 1}, {openapi.FollowAll, 2, 2}} {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/h1"), "/h1": redirect(307, "/h2")}))
			if tt.hops == 1 {
				a.setAnswer(routes(map[string]http.HandlerFunc{"/r": redirect(307, "/h1")}))
			}
			c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: tt.follow})
			req, n := prepareCountingBody(t, c, false)
			_, err := req.Call(t.Context(), nil)
			var se *openapi.StatusError
			switch {
			case tt.follow == openapi.FollowNone && (!errors.As(err, &se) || se.StatusCode != 307):
				t.Errorf("FollowNone: error %v, want a *StatusError for the 307", err)
			case tt.follow == openapi.FollowAll && err != nil:
				t.Errorf("FollowAll, %d hops: %v", tt.hops, err)
			}
			if got := n.Load(); got != tt.want {
				t.Errorf("Redirects %d, %d hops: GetBody called %d times, want %d", tt.follow, tt.hops, got, tt.want)
			}
			for i, r := range a.requests() {
				if string(r.Body) != payload {
					t.Errorf("request %d carried %q, want %q", i, r.Body, payload)
				}
			}
		}
	})

	t.Run("a failing GetBody under FollowNone", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/next")}))
		c := parseFor(t, a, plainRedirDoc, nil)
		req, n := prepareCountingBody(t, c, true)
		_, err := req.Call(t.Context(), nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) || se.StatusCode != 307 {
			t.Errorf("Call error %v, want a *StatusError for the 307 not followed", err)
		}
		req, _ = prepareCountingBody(t, c, true)
		resp, err := req.Send(t.Context())
		if err != nil || resp == nil || resp.StatusCode != 307 {
			t.Errorf("Send = %v, %v; want the 307", resp, err)
		} else {
			resp.Body.Close()
		}
		if got := n.Load(); got != 0 {
			t.Errorf("GetBody called %d times for a 307 not followed", got)
		}
	})

	t.Run("a returned 3xx keeps its Location", func(t *testing.T) {
		wantLocation := func(t *testing.T, h http.Header, want string) {
			t.Helper()
			if got := h["Location"]; len(got) != 1 || got[0] != want {
				t.Errorf("Location = %q, want [%q]", got, want)
			}
			for k := range h {
				if k != "Location" && strings.EqualFold(k, "Location") {
					t.Errorf("the response holds the field %q beside Location", k)
				}
			}
		}
		for _, status := range []int{301, 302, 303, 307, 308} {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(status, "/next?x=1")}))
			c := parseFor(t, a, plainRedirDoc, nil)
			_, err := c.Call(t.Context(), "getR", nil, nil)
			var se *openapi.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("%d: error %v, want a *StatusError", status, err)
			}
			wantLocation(t, se.Header, "/next?x=1")
			resp, err := mustPrepare(t, c, "getR", nil).Send(t.Context())
			if err != nil {
				t.Fatalf("%d: Send: %v", status, err)
			}
			resp.Body.Close()
			wantLocation(t, resp.Header, "/next?x=1")

			d := c.With(func(o *openapi.Options) {
				o.Redirects = openapi.FollowAll
				o.HTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			})
			resp, err = mustPrepare(t, d, "getR", nil).Send(t.Context())
			if err != nil {
				t.Fatalf("%d: Send with ErrUseLastResponse: %v", status, err)
			}
			resp.Body.Close()
			wantLocation(t, resp.Header, "/next?x=1")
		}
	})

	t.Run("CheckRedirect is not consulted under FollowNone", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next")}))
		var n atomic.Int32
		c := parseFor(t, a, plainRedirDoc, &openapi.Options{HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			n.Add(1)
			return nil
		}}})
		c.Call(t.Context(), "getR", nil, nil)
		if got := n.Load(); got != 0 {
			t.Errorf("CheckRedirect consulted %d times for a call that follows no redirect", got)
		}
	})

	t.Run("CancelRequest reaches a legacy transport", func(t *testing.T) {
		rt := &legacyRT{canceled: make(chan struct{})}
		c := parseAt(t, plainRedirDoc, "https://api.example.test", testDocURI,
			&openapi.Options{HTTPClient: &http.Client{Transport: rt, Timeout: 100 * time.Millisecond}})
		start := time.Now()
		_, err := c.Call(t.Context(), "getR", nil, nil)
		if err == nil {
			t.Fatalf("Call succeeded through a transport that never answers")
		}
		select {
		case <-rt.canceled:
		default:
			t.Errorf("CancelRequest was never called on the caller's transport: %v", err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("the call took %v; the Timeout did not reach the transport", d)
		}
	})
}

// The client builds a hop without NewRequestWithContext, which refuses a
// method it deems invalid, so a caller's method is followed and the call never
// panics. client.go, Request.HTTP: "HTTP may be changed before sending";
// Redirects: a 307 keeps the method. net/http builds its own hop as a struct
// literal (go1.25 client.go, Client.do), so a method its transport accepts is
// followed.
func TestHopKeepsAMethodNetHTTPWouldRefuse(t *testing.T) {
	const method = "M-SEARCH\x7f"
	rt := &memRT{}
	rt.answer = func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/r" {
			return memResponse(r, 307, http.Header{"Location": {"/next"}}, ""), nil
		}
		return memResponse(r, 200, nil, "{}"), nil
	}
	c := parseAt(t, plainRedirDoc, "https://api.example.test", testDocURI,
		&openapi.Options{HTTPClient: &http.Client{Transport: rt}, Redirects: openapi.FollowAll})
	req := mustPrepare(t, c, "getR", nil)
	req.HTTP.Method = method
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("Request.Call panicked: %v", p)
			}
		}()
		if _, err := req.Call(t.Context(), nil); err != nil {
			t.Errorf("Request.Call: %v", err)
		}
	}()
	reqs := rt.requests()
	if len(reqs) != 2 {
		t.Fatalf("transport carried %d requests, want the request and the hop", len(reqs))
	}
	if reqs[1].Method != method || reqs[1].URL.Path != "/next" {
		t.Errorf("hop %q %s, want %q /next", reqs[1].Method, reqs[1].URL, method)
	}
}

// Content fields are dropped only when the hop drops the body (303, POST
// under 301/302), not when a kept body is empty. client.go,
// Redirects: "A hop that drops the body drops Content-Type and the other
// content fields"; RFC 9110 section 15.4, item 5: content fields go when
// "the request method has been changed to GET or HEAD".
func TestKeptEmptyBodyKeepsContentFields(t *testing.T) {
	for _, tt := range []struct {
		status     int
		key        string
		hopMethod  string
		keepFields bool
	}{
		{307, "postR", "POST", true}, {308, "postR", "POST", true}, {301, "putR", "PUT", true}, {302, "putR", "PUT", true},
		{303, "postR", "GET", false}, {302, "postR", "GET", false},
	} {
		t.Run(fmt.Sprintf("%d %s", tt.status, tt.key), func(t *testing.T) {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(tt.status, "/next")}))
			c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
			mustCall(t, c, tt.key, &openapi.Input{Body: []byte{}, Header: http.Header{"Content-Language": {"en"}}}, nil)
			reqs := a.requests()
			if len(reqs) != 2 {
				t.Fatalf("server received %d requests, want 2", len(reqs))
			}
			wantField(t, reqs[0].Header, "Content-Type", "application/json")
			hop := reqs[1]
			if hop.Method != tt.hopMethod || len(hop.Body) != 0 {
				t.Errorf("hop %s with %d bytes, want %s with none", hop.Method, len(hop.Body), tt.hopMethod)
			}
			if tt.keepFields {
				wantField(t, hop.Header, "Content-Type", "application/json")
				wantField(t, hop.Header, "Content-Language", "en")
			} else {
				wantNoFields(t, hop.Header, "Content-Type", "Content-Language")
			}
		})
	}
}

// A caller's empty User-Agent (suppressing net/http's default) is kept on
// every hop, even to another origin, since it carries no information.
// doc.go, Header fields: "one with no values removes it (a User-Agent
// included, so net/http adds none)".
func TestUserAgentRemovalKeptOnEveryHop(t *testing.T) {
	for name, edit := range map[string]func(*openapi.Options, *openapi.Input){
		"Options.Header": func(o *openapi.Options, _ *openapi.Input) { o.Header = http.Header{"User-Agent": {}} },
		"Input.Header":   func(_ *openapi.Options, in *openapi.Input) { in.Header = http.Header{"User-Agent": {}} },
	} {
		t.Run(name, func(t *testing.T) {
			b := newWire(t, nil)
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next"), "/next": redirect(302, b.URL+"/b")}))
			o, in := &openapi.Options{Redirects: openapi.FollowAll}, &openapi.Input{}
			edit(o, in)
			c := parseFor(t, a, plainRedirDoc, o)
			mustCall(t, c, "getR", in, nil)
			for i, r := range append(a.requests(), b.only(t)) {
				if ua := r.Header.Values("User-Agent"); ua != nil {
					t.Errorf("request %d (%s) carried User-Agent %q, want none", i, r.Path, ua)
				}
			}
		})
	}
}

// On a cross-origin hop Content-Type is kept only when it is the value the
// client generated; a caller-edited one is a caller field and goes
// (CheckRedirect can restore it). client.go, Redirects: on a hop to another
// origin the client removes "every field supplied through Options.Header,
// Input.Header, or an edit to Request.HTTP.Header. Generated fields needed
// to describe a replayed body, such as Content-Type and Content-Length, are
// rebuilt." A field a ParamWriter sets is the caller's too.
func TestCrossOriginKeepsOnlyGeneratedContentType(t *testing.T) {
	const edited = "application/json; token=ct-secret-3Gh"
	newPair := func(t *testing.T) (a, b *wire) {
		b = newWire(t, nil)
		a = newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, b.URL+"/b")}))
		return a, b
	}
	t.Run("edited in Request.HTTP", func(t *testing.T) {
		a, b := newPair(t)
		c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
		req := mustPrepare(t, c, "postR", &openapi.Input{Body: map[string]any{"n": 1}})
		req.HTTP.Header.Set("Content-Type", edited)
		sendAndClose(t, req)
		wantField(t, a.only(t).Header, "Content-Type", edited)
		hop := b.only(t)
		wantField(t, hop.Header, "Content-Type", "application/json")
		if !bytes.Equal(hop.Body, a.only(t).Body) {
			t.Errorf("hop body %q, want %q", hop.Body, a.only(t).Body)
		}
	})
	t.Run("set by a ParamWriter", func(t *testing.T) {
		a, b := newPair(t)
		c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
		mustCall(t, c, "postR", &openapi.Input{Body: map[string]any{"n": 1}, ParamWriters: map[string]func(*http.Request) error{
			"q": func(r *http.Request) error { r.Header.Set("Content-Type", edited); return nil },
		}}, nil)
		wantField(t, a.only(t).Header, "Content-Type", edited)
		wantField(t, b.only(t).Header, "Content-Type", "application/json")
	})
	t.Run("generated, with parameters", func(t *testing.T) {
		a, b := newPair(t)
		c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
		mustCall(t, c, "postR", &openapi.Input{Body: map[string]any{"n": 1}, MediaType: "application/json; charset=utf-8"}, nil)
		wantField(t, b.only(t).Header, "Content-Type", "application/json; charset=utf-8")
	})
	t.Run("an edited one is kept within the origin", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/next")}))
		c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
		req := mustPrepare(t, c, "postR", &openapi.Input{Body: map[string]any{"n": 1}})
		req.HTTP.Header.Set("Content-Type", edited)
		sendAndClose(t, req)
		reqs := a.requests()
		if len(reqs) != 2 {
			t.Fatalf("server received %d requests, want 2", len(reqs))
		}
		wantField(t, reqs[1].Header, "Content-Type", edited)
	})
}

// The client adds no Referer on hops; a caller's Referer is a caller field
// like any other. client.go, Redirects: "The client adds no Referer"; a
// caller field is kept within the origin and removed on a hop to another.
func TestRedirectAddsNoReferer(t *testing.T) {
	b := newWire(t, nil)
	a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next"), "/next": redirect(302, b.URL+"/b")}))
	c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
	mustCall(t, c, "getR", nil, nil)
	for _, r := range append(a.requests(), b.only(t)) {
		wantNoFields(t, r.Header, "Referer")
	}

	const ref = "https://caller.example.test/page"
	a2 := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next"), "/next": redirect(302, b.URL+"/b2")}))
	c = parseFor(t, a2, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
	mustCall(t, c, "getR", &openapi.Input{Header: http.Header{"Referer": {ref}}}, nil)
	reqs := a2.requests()
	if len(reqs) != 2 {
		t.Fatalf("server received %d requests, want 2", len(reqs))
	}
	wantField(t, reqs[1].Header, "Referer", ref)
	wantNoFields(t, b.last(t).Header, "Referer")
}

// A caller-set Request.Host is kept across a relative Location, as net/http
// does (Go issue 22233). net/http keeps it only for a relative Location
// (go1.25 client.go, Client.do: "If the caller specified a custom Host
// header and the redirect location is relative, preserve the Host header
// through the redirect").
func TestRedirectKeepsCallerHostOnRelativeLocation(t *testing.T) {
	const host = "virtual.example.test"
	a := newWire(t, nil)
	a.setAnswer(routes(map[string]http.HandlerFunc{
		"/r":    redirect(302, "/next"),
		"/next": redirect(302, a.URL+"/last"),
	}))
	c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
	req := mustPrepare(t, c, "getR", nil)
	req.HTTP.Host = host
	sendAndClose(t, req)
	reqs := a.requests()
	if len(reqs) != 3 {
		t.Fatalf("server received %d requests, want 3", len(reqs))
	}
	if reqs[0].Host != host || reqs[1].Host != host {
		t.Errorf("Host %q then %q across a relative Location, want %q kept", reqs[0].Host, reqs[1].Host, host)
	}
	if reqs[2].Host != a.hostport() {
		t.Errorf("Host %q after an absolute Location, want the URL's %q", reqs[2].Host, a.hostport())
	}
}

// http://h versus http://h:80 on a hop: client.go, Redirects: "On a hop to
// another origin (scheme, host and port)"; RFC 6454
// section 4: a URI's port is its scheme's default port when it names none,
// so http://h and http://h:80 are one origin, and a header credential is
// placed again; another port or scheme is another origin.
func TestRedirectDefaultPortIsSameOrigin(t *testing.T) {
	for _, tt := range []struct {
		base, loc string
		placed    bool
	}{
		{"http://api.example.test", "http://api.example.test:80/next", true},
		{"http://api.example.test:80", "http://api.example.test/next", true},
		{"https://api.example.test", "https://api.example.test:443/next", true},
		{"https://api.example.test:443", "https://API.example.test/next", true},
		{"http://api.example.test", "http://api.example.test:8080/next", false},
		{"http://api.example.test", "https://api.example.test/next", false},
		{"https://api.example.test", "https://api.example.test:8443/next", false},
	} {
		t.Run(tt.base+" to "+tt.loc, func(t *testing.T) {
			rt := &memRT{}
			rt.answer = func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/h" {
					return memResponse(r, 302, http.Header{"Location": {tt.loc}}, ""), nil
				}
				return memResponse(r, 200, nil, "{}"), nil
			}
			c, err := openapi.Parse(t.Context(), []byte(bare31(credPaths, credSchemes)), testDocURI, &openapi.Options{
				HTTPClient: &http.Client{Transport: rt}, BaseURL: tt.base, Redirects: openapi.FollowAll, Credentials: credSet(),
			})
			if err != nil {
				t.Fatal(err)
			}
			mustCall(t, c, "keyHeader", nil, nil)
			reqs := rt.requests()
			if len(reqs) != 2 {
				t.Fatalf("transport carried %d requests, want 2", len(reqs))
			}
			if got := reqs[1].Header.Get("X-API-Key") == hSecret; got != tt.placed {
				t.Errorf("credential placed on the hop: %t, want %t", got, tt.placed)
			}
		})
	}
}

// On hops, replacing a header credential deletes every spelling of the
// field on the sent copy, on every path, and caller spellings are not
// canonicalized otherwise; the Content-* drop and the cross-origin rules
// also match non-canonical keys. client.go, Redirects: "A hop that drops
// the body drops Content-Type and the other content fields"; on a hop to
// another origin every caller field goes. Field names compare without
// regard to case (RFC 9110 section 5.1).
func TestHopRulesMatchEverySpelling(t *testing.T) {
	t.Run("a content field a ParamWriter spelled in lowercase, on a 303", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(303, "/next")}))
		c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
		mustCall(t, c, "postR", &openapi.Input{Body: map[string]any{"n": 1}, ParamWriters: map[string]func(*http.Request) error{
			"q": func(r *http.Request) error { r.Header["content-language"] = []string{"en"}; return nil },
		}}, nil)
		reqs := a.requests()
		if len(reqs) != 2 {
			t.Fatalf("server received %d requests, want 2", len(reqs))
		}
		wantField(t, reqs[0].Header, "Content-Language", "en")
		wantNoFields(t, reqs[1].Header, "Content-Language", "Content-Type")
	})
	t.Run("a caller field in lowercase, on a hop to another origin", func(t *testing.T) {
		b := newWire(t, nil)
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, b.URL+"/b")}))
		c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
		req := mustPrepare(t, c, "postR", &openapi.Input{Body: map[string]any{"n": 1}})
		req.HTTP.Header["x-edit"] = []string{"e"}
		sendAndClose(t, req)
		mustCall(t, c, "postR", &openapi.Input{Body: map[string]any{"n": 1}, ParamWriters: map[string]func(*http.Request) error{
			"q": func(r *http.Request) error { r.Header["x-edit"] = []string{"e"}; return nil },
		}}, nil)
		for _, r := range b.requests() {
			wantNoFields(t, r.Header, "X-Edit")
		}
		if n := b.count(); n != 2 {
			t.Errorf("second origin received %d requests, want 2", n)
		}
	})
}
