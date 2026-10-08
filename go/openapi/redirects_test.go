package openapi_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Redirects, tested against httptest servers on two origins. Two httptest
// servers on 127.0.0.1 with different ports are two origins (scheme, host and
// port); a raw listener shows every byte a hop sent.

// redirDoc has one operation per method on /r, each carrying a header, a
// query and a cookie parameter, under root security that places a header,
// a query and a cookie credential and a bearer token.
var redirDoc = doc31(`
	"/r":{
		"parameters":[{"name":"X-Trace","in":"header"},{"name":"q","in":"query"},{"name":"c","in":"cookie"}],
		"get":{"operationId":"getR"},
		"head":{"operationId":"headR"},
		"post":{"operationId":"postR","requestBody":{"content":{"application/json":{}}}},
		"put":{"operationId":"putR","requestBody":{"content":{"application/json":{}}}},
		"patch":{"operationId":"patchR","requestBody":{"content":{"application/json":{}}}},
		"delete":{"operationId":"deleteR","requestBody":{"content":{"application/json":{}}}}
	}`,
	`"security":[{"key_h":[],"key_q":[],"key_c":[],"bearer":[]}]`,
	`"components":{"securitySchemes":{
		"key_h":{"type":"apiKey","in":"header","name":"X-API-Key"},
		"key_q":{"type":"apiKey","in":"query","name":"api_key"},
		"key_c":{"type":"apiKey","in":"cookie","name":"sid"},
		"bearer":{"type":"http","scheme":"bearer"}
	}}`)

// redirSecrets are the credentials redirOptions places.
var redirSecrets = []string{hSecret, qSecret, cSecret, bToken}

// redirOptions follows every redirect, with a header, query and cookie
// credential, a bearer token from src, and a caller field in Options.Header.
func redirOptions(src *source) *openapi.Options {
	return &openapi.Options{
		Redirects: openapi.FollowAll,
		Header:    http.Header{"X-Opt": {"o"}},
		Credentials: map[string]openapi.Credential{
			"key_h": openapi.Secret(hSecret), "key_q": openapi.Secret(qSecret),
			"key_c": openapi.Secret(cSecret), "bearer": src.credential(),
		},
	}
}

// redirKeys maps each operation of redirDoc to its method.
var redirKeys = map[string]string{
	"getR": "GET", "headR": "HEAD", "postR": "POST", "putR": "PUT", "patchR": "PATCH", "deleteR": "DELETE",
}

// redirInput gives every parameter, a caller field in Input.Header, and for
// a method with a body a JSON body and a content field.
func redirInput(method string) *openapi.Input {
	in := &openapi.Input{
		Params: map[string]any{"X-Trace": "tr", "q": "qv", "c": "cv"},
		Header: http.Header{"X-In": {"i"}},
	}
	if method != "GET" && method != "HEAD" {
		in.Body = map[string]any{"n": 1}
		in.Header.Set("Content-Language", "en")
	}
	return in
}

// routes answers a request for a path in m with its handler, and any other
// with 200 and an empty JSON object.
func routes(m map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h, ok := m[r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}
}

// redirect answers with status and a Location field, when loc is not empty.
func redirect(status int, loc string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if loc != "" {
			w.Header().Set("Location", loc)
		}
		w.WriteHeader(status)
	}
}

// wantPlaced fails unless r carries every credential redirOptions places,
// the header parameter and the caller fields, with the request target
// target+"api_key="+qSecret (a query credential last).
func wantPlaced(t *testing.T, r rec, target string) {
	t.Helper()
	wantURI(t, r, target+"api_key="+qSecret)
	wantField(t, r.Header, "X-API-Key", hSecret)
	wantField(t, r.Header, "Authorization", "Bearer "+bToken)
	wantField(t, r.Header, "X-Trace", "tr")
	wantField(t, r.Header, "X-Opt", "o")
	wantField(t, r.Header, "X-In", "i")
}

// wantStripped fails if r carries any credential, header parameter, cookie
// or caller field, or any secret anywhere in its target or header fields.
func wantStripped(t *testing.T, r rec) {
	t.Helper()
	wantNoFields(t, r.Header, "X-API-Key", "Authorization", "Cookie", "X-Trace", "X-Opt", "X-In", "X-Edit",
		"Accept", "Range", "Content-Language")
	for name, vs := range r.Header {
		for _, v := range vs {
			for _, s := range redirSecrets {
				if strings.Contains(v, s) {
					t.Errorf("%s: %q holds a credential", name, v)
				}
			}
		}
	}
	for _, s := range redirSecrets {
		if strings.Contains(r.RequestURI, s) {
			t.Errorf("request target %q holds a credential", r.RequestURI)
		}
	}
}

// hopMethod is the method a hop of status uses after method, and whether it
// keeps the body (client.go, Redirects: "A 303 is followed with GET (HEAD
// stays HEAD) and no body. A 301 or 302 changes POST to GET with no body,
// and keeps any other method and its body, as 307 and 308 do.").
func hopMethod(status int, method string) (string, bool) {
	switch {
	case status == 303 && method == "HEAD":
		return "HEAD", false
	case status == 303:
		return "GET", false
	case (status == 301 || status == 302) && method == "POST":
		return "GET", false
	}
	return method, method != "GET" && method != "HEAD"
}

// client.go, Redirects: which method and body each followed status uses; "A
// hop that drops the body drops Content-Type and the other content fields"
// (RFC 9110 section 15.4, item 5: "If the request method has been changed
// to GET or HEAD, remove content-specific header fields, including ...
// Content-Language ... Content-Type, Content-Length"); "On a hop within the
// origin, header and cookie credentials are placed again; a query
// credential goes only on the request the client builds, never onto a
// Location." credential.go, SecretFunc: f is called for "each redirect hop
// on which Redirects says credentials are placed again".
func TestRedirectMethodsAndBodies(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for key, method := range redirKeys {
			t.Run(fmt.Sprintf("%d %s", status, method), func(t *testing.T) {
				a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(status, "/next?x=1")}))
				src := fixedSource(bToken)
				c := parseFor(t, a, redirDoc, redirOptions(src))
				resp := mustCall(t, c, key, redirInput(method), nil)
				if resp.StatusCode != 200 {
					t.Errorf("status %d, want the 200 the hop answered", resp.StatusCode)
				}
				reqs := a.requests()
				if len(reqs) != 2 {
					t.Fatalf("server received %d requests, want 2", len(reqs))
				}
				first, hop := reqs[0], reqs[1]
				wantPlaced(t, first, "/r?q=qv&")
				wantField(t, first.Header, "Cookie", "c=cv; sid="+cSecret)

				wantMethod, keep := hopMethod(status, method)
				if hop.Method != wantMethod {
					t.Errorf("hop method %s, want %s", hop.Method, wantMethod)
				}
				wantPlaced(t, hop, "/next?x=1&")
				wantField(t, hop.Header, "Cookie", "c=cv; sid="+cSecret)
				switch {
				case keep:
					if !bytes.Equal(hop.Body, first.Body) || len(first.Body) == 0 {
						t.Errorf("hop body %q, want %q", hop.Body, first.Body)
					}
					if hop.ContentLength != int64(len(first.Body)) {
						t.Errorf("hop Content-Length %d, want %d", hop.ContentLength, len(first.Body))
					}
					wantField(t, hop.Header, "Content-Type", "application/json")
					wantField(t, hop.Header, "Content-Language", "en")
				case method != "GET" && method != "HEAD":
					if len(hop.Body) != 0 || hop.ContentLength != 0 {
						t.Errorf("hop sent a body of %d bytes (Content-Length %d), want none", len(hop.Body), hop.ContentLength)
					}
					wantNoFields(t, hop.Header, "Content-Type", "Content-Length", "Content-Language", "Transfer-Encoding")
				}
				if n := src.calls(); n != 2 {
					t.Errorf("the bearer source was called %d times, want 2 (the request and the hop)", n)
				}
			})
		}
	}
}

// client.go, Redirects: "Only 301, 302, 303, 307 and 308 with a Location
// that url.Parse accepts can be followed, as net/http follows them ... A 3xx
// not followed is the outcome, a *StatusError";
// Options.Redirects: "zero means none"; "a hop that must resend a body that
// cannot be sent again (see Input.Body) is not followed". client.go,
// Input.Body: "Any other reader, such as a pipe or an os.Stdin that is not a
// regular file, is read once".
func TestRedirectStatusesNotFollowed(t *testing.T) {
	notFollowed := func(t *testing.T, opts *openapi.Options, key string, in *openapi.Input, status int, loc func(b *wire) string) {
		t.Helper()
		b := newWire(t, nil)
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(status, loc(b))}))
		c := parseFor(t, a, redirDoc, opts)
		resp, err := c.Call(t.Context(), key, in, nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) {
			t.Fatalf("Call error %v (%T), want a *StatusError", err, err)
		}
		if se.StatusCode != status || resp == nil || resp.StatusCode != status {
			t.Errorf("status %d, want the %d not followed", se.StatusCode, status)
		}
		if l := loc(b); l != "" && se.Header.Get("Location") != l {
			t.Errorf("Location %q, want %q", se.Header.Get("Location"), l)
		}
		b.nothingSent(t)
		if n := len(a.requests()); n != 1 {
			t.Errorf("first origin received %d requests, want 1", n)
		}
		noSecrets(t, err, redirSecrets...)
	}
	toB := func(b *wire) string { return b.URL + "/b" }
	toSelf := func(*wire) string { return "/next" }

	t.Run("statuses FollowAll does not follow", func(t *testing.T) {
		for _, status := range []int{300, 304, 305, 306, 399} {
			t.Run(strconv.Itoa(status), func(t *testing.T) {
				notFollowed(t, redirOptions(fixedSource(bToken)), "getR", redirInput("GET"), status, toB)
			})
		}
		t.Run("301 without Location", func(t *testing.T) {
			notFollowed(t, redirOptions(fixedSource(bToken)), "getR", redirInput("GET"), 301, func(*wire) string { return "" })
		})
	})
	t.Run("zero Redirects", func(t *testing.T) {
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(strconv.Itoa(status), func(t *testing.T) {
				o := redirOptions(fixedSource(bToken))
				o.Redirects = openapi.FollowNone
				notFollowed(t, o, "postR", redirInput("POST"), status, toSelf)
			})
		}
	})
	t.Run("a body that cannot be sent again", func(t *testing.T) {
		for _, tt := range []struct {
			key    string
			status int
		}{{"postR", 307}, {"postR", 308}, {"putR", 301}, {"putR", 302}, {"patchR", 307}, {"deleteR", 308}} {
			t.Run(fmt.Sprintf("%d %s", tt.status, tt.key), func(t *testing.T) {
				in := redirInput(redirKeys[tt.key])
				in.Body = newOnce(`{"n":1}`)
				notFollowed(t, redirOptions(fixedSource(bToken)), tt.key, in, tt.status, toSelf)
			})
		}
	})
	// A hop that sends no body needs none again.
	t.Run("a body that cannot be sent again, dropped by the hop", func(t *testing.T) {
		for _, status := range []int{301, 302, 303} {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(status, "/next")}))
			c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
			in := redirInput("POST")
			in.Body = newOnce(`{"n":1}`)
			mustCall(t, c, "postR", in, nil)
			if reqs := a.requests(); len(reqs) != 2 || reqs[1].Method != "GET" || len(reqs[1].Body) != 0 {
				t.Errorf("%d: requests %d, want the POST and a GET without a body", status, len(reqs))
			}
		}
	})
}

// client.go, Redirects: "On a hop to another origin (scheme, host and port,
// ...), the client removes the credentials it added and any header a security
// scheme placed, the Authorization and Cookie fields (cookie parameters
// included), all header parameters, and every field supplied through
// Options.Header, Input.Header, or an edit to Request.HTTP.Header. ...
// Generated fields needed to describe a replayed body, such as Content-Type
// and Content-Length, are rebuilt"; "The client adds no Referer",
// and a caller's Referer is a caller field like any other. The second origin
// is a raw listener, so every byte of the hop is checked.
func TestRedirectCrossOriginStrips(t *testing.T) {
	for _, via := range []string{"Call", "Send"} {
		t.Run(via, func(t *testing.T) {
			b := newRawServer(t, rawOK)
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, b.URL+"/b?x=1")}))
			src := fixedSource(bToken)
			c := parseFor(t, a, redirDoc, redirOptions(src))
			in := redirInput("POST")
			in.Header.Set("Accept", "application/json")
			in.Header.Set("Range", "bytes=0-1023")
			in.Header.Set("Referer", "https://caller.example.test/page")
			var err error
			if via == "Call" {
				_, err = c.Call(t.Context(), "postR", in, nil)
			} else {
				req := mustPrepare(t, c, "postR", in)
				req.HTTP.Header.Set("X-Edit", "e")
				var resp *openapi.Response
				if resp, err = req.Send(t.Context()); err == nil {
					resp.Body.Close()
				}
			}
			if err != nil {
				t.Fatalf("%s: %v", via, err)
			}
			first := a.only(t)
			wantPlaced(t, first, "/r?q=qv&")
			raws := b.requests()
			if len(raws) != 1 {
				t.Fatalf("second origin received %d requests, want 1", len(raws))
			}
			raw := raws[0]
			for _, s := range redirSecrets {
				for _, form := range secretForms(s) {
					if bytes.Contains(raw, []byte(form)) {
						t.Errorf("the hop to another origin carried the credential %q:\n%s", s, raw)
					}
				}
			}
			req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
			if err != nil {
				t.Fatalf("reading the hop: %v", err)
			}
			body, _ := io.ReadAll(req.Body)
			hop := rec{Method: req.Method, RequestURI: req.RequestURI, Header: req.Header, Body: body, ContentLength: req.ContentLength}
			if hop.Method != "POST" || hop.RequestURI != "/b?x=1" {
				t.Errorf("hop %s %s, want POST /b?x=1, the Location as given", hop.Method, hop.RequestURI)
			}
			wantStripped(t, hop)
			if !bytes.Equal(hop.Body, first.Body) {
				t.Errorf("hop body %q, want %q", hop.Body, first.Body)
			}
			wantField(t, hop.Header, "Content-Type", "application/json")
			if hop.ContentLength != int64(len(first.Body)) {
				t.Errorf("hop Content-Length %d, want %d", hop.ContentLength, len(first.Body))
			}
			if ref := hop.Header.Get("Referer"); strings.Contains(ref, "caller.example.test") {
				t.Errorf("hop Referer %q is the caller's field", ref)
			}
			if n := src.calls(); n != 1 {
				t.Errorf("the bearer source was called %d times, want 1: no credential goes to another origin", n)
			}
		})
	}
}

// client.go, Redirects: "Once a hop has left the call's origin, no later
// hop has credentials, header parameters or caller fields placed again,
// even one back on that origin."
func TestRedirectStrippingIsSticky(t *testing.T) {
	t.Run("A to B to A", func(t *testing.T) {
		var a *wire
		b := newWire(t, nil)
		a = newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, b.URL+"/b")}))
		b.setAnswer(routes(map[string]http.HandlerFunc{"/b": redirect(307, a.URL+"/back")}))
		src := fixedSource(bToken)
		c := parseFor(t, a, redirDoc, redirOptions(src))
		mustCall(t, c, "postR", redirInput("POST"), nil)
		reqs := a.requests()
		if len(reqs) != 2 {
			t.Fatalf("first origin received %d requests, want 2", len(reqs))
		}
		wantStripped(t, b.only(t))
		back := reqs[1]
		wantURI(t, back, "/back")
		wantStripped(t, back)
		wantField(t, back.Header, "Content-Type", "application/json")
		if !bytes.Equal(back.Body, reqs[0].Body) {
			t.Errorf("body back on the first origin %q, want %q", back.Body, reqs[0].Body)
		}
		if n := src.calls(); n != 1 {
			t.Errorf("the bearer source was called %d times, want 1", n)
		}
	})
	t.Run("A to A to B to A", func(t *testing.T) {
		var a *wire
		b := newWire(t, nil)
		a = newWire(t, routes(map[string]http.HandlerFunc{
			"/r":     redirect(302, "/again"),
			"/again": redirect(302, b.URL+"/b"),
		}))
		b.setAnswer(routes(map[string]http.HandlerFunc{"/b": redirect(302, a.URL+"/back")}))
		src := fixedSource(bToken)
		c := parseFor(t, a, redirDoc, redirOptions(src))
		mustCall(t, c, "getR", redirInput("GET"), nil)
		reqs := a.requests()
		if len(reqs) != 3 {
			t.Fatalf("first origin received %d requests, want 3", len(reqs))
		}
		wantPlaced(t, reqs[1], "/again?")
		wantStripped(t, b.only(t))
		wantURI(t, reqs[2], "/back")
		wantStripped(t, reqs[2])
		if n := src.calls(); n != 2 {
			t.Errorf("the bearer source was called %d times, want 2", n)
		}
	})
}

// client.go, Redirects: "The HTTPClient's CheckRedirect is still consulted on
// every hop the client follows, after the client applies the other rules here
// and before it places credentials on the hop, and can restore a field the
// caller deliberately wants to forward ... CheckRedirect may restore a field
// intentionally, such as Range or Accept". CheckRedirect sees each hop after
// stripping and before credentials are placed (the unsigned view Prepare
// gives), and ErrUseLastResponse and errors follow net/http's meanings: an
// error ends the call with a *url.Error wrapping it, and ErrUseLastResponse
// returns the 3xx, which the client reports as a *StatusError ("A 3xx not
// followed is the outcome").
func TestRedirectCheckRedirect(t *testing.T) {
	t.Run("sees a same-origin hop before credentials are placed", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/next?x=1")}))
		var (
			mu     sync.Mutex
			header http.Header
			target string
		)
		src := fixedSource(bToken)
		o := redirOptions(src)
		o.HTTPClient = &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
			mu.Lock()
			header, target = r.Header.Clone(), r.URL.String()
			mu.Unlock()
			// A field of a credential's name, set here, is replaced when the
			// credential is placed (doc.go, Credentials: "A header credential
			// replaces a field of the same name").
			r.Header.Set("X-API-Key", "from-check-redirect")
			return nil
		}}
		c := parseFor(t, a, redirDoc, o)
		mustCall(t, c, "getR", redirInput("GET"), nil)
		mu.Lock()
		defer mu.Unlock()
		if header == nil {
			t.Fatalf("CheckRedirect was not consulted")
		}
		// The unsigned view: no credential, but the header parameter, the
		// caller fields and the cookie parameter, as Prepare gives them.
		wantNoFields(t, header, "X-API-Key", "Authorization")
		wantField(t, header, "Cookie", "c=cv")
		wantField(t, header, "X-Trace", "tr")
		wantField(t, header, "X-Opt", "o")
		wantField(t, header, "X-In", "i")
		for _, s := range redirSecrets {
			if strings.Contains(target, s) {
				t.Errorf("CheckRedirect saw the URL %q, holding a credential", target)
			}
		}
		reqs := a.requests()
		if len(reqs) != 2 {
			t.Fatalf("server received %d requests, want 2", len(reqs))
		}
		wantPlaced(t, reqs[1], "/next?x=1&")
		wantField(t, reqs[1].Header, "Cookie", "c=cv; sid="+cSecret)
		if n := src.calls(); n != 2 {
			t.Errorf("the bearer source was called %d times, want 2", n)
		}
	})
	t.Run("an error on a same-origin hop places nothing", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/next")}))
		errStop := errors.New("caller stopped the redirect")
		src := fixedSource(bToken)
		o := redirOptions(src)
		o.HTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errStop }}
		c := parseFor(t, a, redirDoc, o)
		_, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		var ue *url.Error
		if !errors.Is(err, errStop) || !errors.As(err, &ue) {
			t.Errorf("Call error %v, want a *url.Error wrapping CheckRedirect's error", err)
		}
		if n := a.count(); n != 1 {
			t.Errorf("server received %d requests, want 1", n)
		}
		if n := src.calls(); n != 1 {
			t.Errorf("the bearer source was called %d times, want 1: credentials are placed after CheckRedirect returns", n)
		}
		noSecrets(t, err, redirSecrets...)
	})
	t.Run("restores a field on another origin", func(t *testing.T) {
		b := newWire(t, nil)
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, b.URL+"/b")}))
		var (
			mu   sync.Mutex
			seen []http.Header
			vias []int
		)
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
			mu.Lock()
			seen = append(seen, r.Header.Clone())
			vias = append(vias, len(via))
			mu.Unlock()
			r.Header.Set("Range", "bytes=0-1023")
			r.Header.Set("Accept", "image/png")
			return nil
		}}
		c := parseFor(t, a, redirDoc, o)
		in := redirInput("GET")
		in.Header.Set("Range", "bytes=0-1023")
		mustCall(t, c, "getR", in, nil)
		hop := b.only(t)
		wantField(t, hop.Header, "Range", "bytes=0-1023")
		wantField(t, hop.Header, "Accept", "image/png")
		wantNoFields(t, hop.Header, "X-API-Key", "Authorization", "Cookie", "X-In", "X-Opt", "X-Trace")
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 1 || vias[0] != 1 {
			t.Fatalf("CheckRedirect called %d times (via %v), want once with the first request in via", len(seen), vias)
		}
		// Consulted after the client applied the rules: it sees the hop
		// already stripped.
		wantNoFields(t, seen[0], "X-API-Key", "Authorization", "Cookie", "X-In", "X-Opt", "X-Trace", "Range")
	})
	t.Run("consulted on every hop", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/2"), "/2": redirect(308, "/3")}))
		var mu sync.Mutex
		var vias []int
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
			mu.Lock()
			vias = append(vias, len(via))
			mu.Unlock()
			return nil
		}}
		c := parseFor(t, a, redirDoc, o)
		mustCall(t, c, "getR", redirInput("GET"), nil)
		mu.Lock()
		defer mu.Unlock()
		if fmt.Sprint(vias) != "[1 2]" {
			t.Errorf("CheckRedirect saw via of lengths %v, want [1 2]", vias)
		}
		if n := a.count(); n != 3 {
			t.Errorf("server received %d requests, want 3", n)
		}
	})
	t.Run("an error", func(t *testing.T) {
		b := newWire(t, nil)
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, b.URL+"/b")}))
		errStop := errors.New("caller stopped the redirect")
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errStop }}
		c := parseFor(t, a, redirDoc, o)
		_, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		var ue *url.Error
		if !errors.Is(err, errStop) || !errors.As(err, &ue) {
			t.Errorf("Call error %v, want a *url.Error wrapping CheckRedirect's error", err)
		}
		b.nothingSent(t)
		noSecrets(t, err, redirSecrets...)
	})
	t.Run("ErrUseLastResponse", func(t *testing.T) {
		b := newWire(t, nil)
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, b.URL+"/b")}))
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		c := parseFor(t, a, redirDoc, o)
		_, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) || se.StatusCode != 302 {
			t.Errorf("Call error %v, want a *StatusError for the 302", err)
		}
		b.nothingSent(t)
	})
}

// chain answers /r and /h<i> with a 302 to /h<i+1> until hops redirects
// have been made, then 200.
func chain(hops int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		i := 0
		if s, ok := strings.CutPrefix(r.URL.Path, "/h"); ok {
			i, _ = strconv.Atoi(s)
		}
		if i < hops {
			w.Header().Set("Location", "/h"+strconv.Itoa(i+1))
			w.WriteHeader(302)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}
}

// client.go, Redirects: "with a nil CheckRedirect, the chain stops after 10
// requests, as net/http's default does", and a caller's CheckRedirect
// decides otherwise. net/http is the authority for its own limit: the same
// chain is sent through a plain http.Client, and the client must make as
// many requests and fail or succeed as it does, a failure being the
// *url.Error net/http returns.
func TestRedirectHopLimit(t *testing.T) {
	for _, hops := range []int{9, 10, 11} {
		t.Run(strconv.Itoa(hops), func(t *testing.T) {
			ref := newWire(t, chain(hops))
			resp, refErr := (&http.Client{}).Get(ref.URL + "/r")
			if refErr == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}

			a := newWire(t, chain(hops))
			c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
			_, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
			if a.count() != ref.count() {
				t.Errorf("the client made %d requests, net/http %d", a.count(), ref.count())
			}
			var ue *url.Error
			switch {
			case refErr == nil && err != nil:
				t.Errorf("Call: %v; net/http followed the chain", err)
			case refErr != nil && !errors.As(err, &ue):
				t.Errorf("Call error %v (%T), want the *url.Error net/http returns (%v)", err, err, refErr)
			}
			noSecrets(t, err, redirSecrets...)
		})
	}
	t.Run("a caller's CheckRedirect", func(t *testing.T) {
		a := newWire(t, chain(15))
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 20 {
				return errors.New("too many")
			}
			return nil
		}}
		c := parseFor(t, a, redirDoc, o)
		mustCall(t, c, "getR", redirInput("GET"), nil)
		if n := a.count(); n != 16 {
			t.Errorf("server received %d requests, want 16", n)
		}
	})
}

// requestChain returns r and every earlier request reachable from it
// through Request.Response.Request.
func requestChain(r *http.Request) []*http.Request {
	var out []*http.Request
	for r != nil && len(out) < 100 {
		out = append(out, r)
		if r.Response == nil {
			break
		}
		r = r.Response.Request
	}
	return out
}

// wantNoCredentialsIn fails if any request of the chain from r holds a
// credential the client added: in its URL or its header fields.
func wantNoCredentialsIn(t *testing.T, r *http.Request) {
	t.Helper()
	if r == nil {
		t.Fatalf("Response.Request is nil")
	}
	for i, req := range requestChain(r) {
		for _, s := range redirSecrets {
			for _, form := range secretForms(s) {
				if strings.Contains(req.URL.String(), form) {
					t.Errorf("request %d back: URL %q holds a credential", i, req.URL)
				}
				for name, vs := range req.Header {
					for _, v := range vs {
						if strings.Contains(v, form) {
							t.Errorf("request %d back: %s %q holds a credential", i, name, v)
						}
					}
				}
			}
		}
	}
}

// client.go, Response: "Its Request is the last request sent, after any
// redirects, without the credentials the client added to its URL and header
// fields or the cookies the HTTPClient's Jar supplied, and so is every
// earlier request reachable from it."
func TestResponseRequestHasNoCredentials(t *testing.T) {
	t.Run("after same-origin hops", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/next"), "/next": redirect(308, "/last")}))
		c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
		resp := mustCall(t, c, "getR", redirInput("GET"), nil)
		if a.count() != 3 {
			t.Fatalf("server received %d requests, want 3", a.count())
		}
		wantNoCredentialsIn(t, resp.Request)
		if resp.Request.URL.Path != "/last" {
			t.Errorf("Response.Request.URL = %q, want the last request's", resp.Request.URL)
		}
	})
	t.Run("without redirects", func(t *testing.T) {
		a := newWire(t, nil)
		c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
		resp := mustCall(t, c, "getR", redirInput("GET"), nil)
		wantNoCredentialsIn(t, resp.Request)
		req := mustPrepare(t, c, "getR", redirInput("GET"))
		resp = sendAndClose(t, req)
		wantNoCredentialsIn(t, resp.Request)
	})
	t.Run("a StatusError's", func(t *testing.T) {
		a := newWire(t, jsonAnswer(403, `{}`))
		c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
		_, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) {
			t.Fatalf("Call error %v, want a *StatusError", err)
		}
		wantNoCredentialsIn(t, se.Request)
		noSecrets(t, err, redirSecrets...)
	})
	t.Run("a 3xx not followed", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next")}))
		o := redirOptions(fixedSource(bToken))
		o.Redirects = openapi.FollowNone
		c := parseFor(t, a, redirDoc, o)
		_, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) {
			t.Fatalf("Call error %v, want a *StatusError", err)
		}
		wantNoCredentialsIn(t, se.Request)
	})
}

// client.go, Options.HTTPClient: "It is used as given and never modified",
// FollowAll included.
func TestRedirectsLeaveHTTPClientUnmodified(t *testing.T) {
	a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next")}))
	check := func(*http.Request, []*http.Request) error { return nil }
	tr := &countingTransport{}
	for name, hc := range map[string]*http.Client{
		"nil CheckRedirect": {Transport: tr},
		"a CheckRedirect":   {Transport: tr, CheckRedirect: check},
	} {
		t.Run(name, func(t *testing.T) {
			before := *hc
			o := redirOptions(fixedSource(bToken))
			o.HTTPClient = hc
			c := parseFor(t, a, redirDoc, o)
			mustCall(t, c, "getR", redirInput("GET"), nil)
			c.With(func(o *openapi.Options) { o.Redirects = openapi.FollowNone }).Call(t.Context(), "getR", redirInput("GET"), nil)
			if hc.Transport != before.Transport || hc.Jar != before.Jar || hc.Timeout != before.Timeout {
				t.Errorf("the http.Client was modified")
			}
			if reflect.ValueOf(hc.CheckRedirect).Pointer() != reflect.ValueOf(before.CheckRedirect).Pointer() {
				t.Errorf("the http.Client's CheckRedirect was replaced")
			}
		})
	}
}

// credential.go, FromTransport: "Such a transport sees every hop of a
// redirect, other origins included, and plain http too"; client.go,
// Redirects: "A transport that satisfies a FromTransport scheme sees every
// hop, other origins included".
func TestFromTransportSeesEveryHop(t *testing.T) {
	var a *wire
	b := newWire(t, nil)
	a = newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, b.URL+"/b")}))
	b.setAnswer(routes(map[string]http.HandlerFunc{"/b": redirect(307, a.URL+"/back")}))
	tr := &countingTransport{}
	o := &openapi.Options{Redirects: openapi.FollowAll, HTTPClient: &http.Client{Transport: tr}, Credentials: map[string]openapi.Credential{}}
	for _, name := range []string{"key_h", "key_q", "key_c", "bearer"} {
		o.Credentials[name] = openapi.FromTransport()
	}
	c := parseFor(t, a, redirDoc, o)
	mustCall(t, c, "getR", nil, nil)
	if n := tr.count(); n != 3 {
		t.Errorf("the transport saw %d requests, want every hop (3)", n)
	}
}

// credential.go, SecretFunc: "On a redirect hop the first request has already
// been sent, so an error or an empty secret ends the call with a *url.Error
// naming the scheme, and wrapping f's error if it returned one, along with the
// last response, its body closed". An empty secret on a hop ends the call as an
// error does, with a *url.Error and no hop sent.
func TestSecretFuncFailsOnHop(t *testing.T) {
	errHop := errors.New("token refresh failed")
	for name, answer := range map[string]func(int64) (string, error){
		"error": func(n int64) (string, error) {
			if n == 1 {
				return bToken, nil
			}
			return "", errHop
		},
		"empty secret": func(n int64) (string, error) {
			if n == 1 {
				return bToken, nil
			}
			return "", nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/next")}))
			src := &source{fn: func(_ context.Context, n int64) (string, error) { return answer(n) }}
			c := parseFor(t, a, redirDoc, redirOptions(src))
			resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
			var ue *url.Error
			if !errors.As(err, &ue) {
				t.Errorf("Call error %v (%T), want a *url.Error", err, err)
			}
			if resp == nil || resp.StatusCode != 307 {
				t.Errorf("Call returned Response %v, want the 307 that arrived", resp)
			} else {
				wantBodyClosed(t, resp)
			}
			if name == "error" && !errors.Is(err, errHop) {
				t.Errorf("Call error %v does not wrap the source's error", err)
			}
			if n := a.count(); n != 1 {
				t.Errorf("server received %d requests, want only the first", n)
			}
			if n := src.calls(); n != 2 {
				t.Errorf("the source was called %d times, want 2", n)
			}
			noSecrets(t, err, redirSecrets...)
		})
	}
}

// client.go, Request.HTTP: "A send takes HTTP.Body the first time and
// GetBody for every replay, as net/http does, so a caller who replaces the
// body sets GetBody and ContentLength with it, or clears GetBody";
// Redirects: "a hop that must resend a body that cannot be sent again ...
// is not followed". Send returns the 3xx itself ("Send ... returns at the
// response headers for every final HTTP status").
func TestRedirectSendReplacedBody(t *testing.T) {
	for _, tt := range []struct {
		status int
		follow bool
	}{{307, false}, {308, false}, {303, true}} {
		t.Run(strconv.Itoa(tt.status), func(t *testing.T) {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(tt.status, "/next")}))
			c := parseFor(t, a, redirDoc, redirOptions(fixedSource(bToken)))
			req := mustPrepare(t, c, "postR", redirInput("POST"))
			req.HTTP.Body = io.NopCloser(newOnce(`{"n":2}`))
			req.HTTP.GetBody = nil
			req.HTTP.ContentLength = -1
			resp, err := req.Send(t.Context())
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			switch reqs := a.requests(); {
			case tt.follow && (resp.StatusCode != 200 || len(reqs) != 2):
				t.Errorf("status %d after %d requests, want the hop followed", resp.StatusCode, len(reqs))
			case !tt.follow && (resp.StatusCode != tt.status || len(reqs) != 1):
				t.Errorf("status %d after %d requests, want the %d returned", resp.StatusCode, len(reqs), tt.status)
			}
		})
	}
}
