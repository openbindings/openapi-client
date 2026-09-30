package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the stage 1 focused second review (ledger, "Focused
// second review"; review2/panel.json "#N"). Gn are its accepted fixes; Tn-n
// its doc refreshes.

// promptly runs f and fails when it has not returned within d, so a hang
// fails the test instead of the run. The goroutine is left behind then.
func promptly(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
}

// echoLenRT reads each request body to the end, closes it, and answers 200
// with {"got":<bytes read>}; it never involves net/http's connection code.
type echoLenRT struct{}

func (echoLenRT) RoundTrip(r *http.Request) (*http.Response, error) {
	n := 0
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		n = len(b)
	}
	body := fmt.Sprintf(`{"got":%d}`, n)
	return &http.Response{StatusCode: 200, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)), Request: r}, nil
}

const g2Doc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://h.example.test"}],"paths":{
	"/p":{"put":{"operationId":"put","requestBody":{"content":{"application/json":{}}},"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}
}}`

// g2Bodies are the bodies G2 found hanging: a []byte, a structured value, a
// small *strings.Reader.
var g2Bodies = map[string]struct {
	body func() any
	size int
}{
	"bytes":          {func() any { return []byte(`{"a":1}`) }, 7},
	"encoded":        {func() any { return map[string]int{"a": 1} }, 7},
	"strings.Reader": {func() any { return strings.NewReader(`{"a":"bc"}`) }, 10},
}

// checkG2 calls put with each G2 body under context.Background and wants the
// transport's answer promptly, and Send's WaitRequest nil.
func checkG2(t *testing.T, c *openapi.Client) {
	t.Helper()
	for name, b := range g2Bodies {
		t.Run(name, func(t *testing.T) {
			var out struct{ Got int }
			var err error
			promptly(t, 2*time.Second, "Call", func() {
				_, err = c.Call(context.Background(), "put", &openapi.Input{Body: b.body()}, &out)
			})
			if err != nil || out.Got != b.size {
				t.Errorf("Call = %+v, %v; want {Got:%d}", out, err, b.size)
			}
			promptly(t, 2*time.Second, "Send and WaitRequest", func() {
				resp, err := mustPrepare(t, c, "put", &openapi.Input{Body: b.body()}).Send(context.Background())
				if err != nil {
					t.Errorf("Send: %v", err)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if err := resp.WaitRequest(context.Background()); err != nil {
					t.Errorf("WaitRequest = %v", err)
				}
			})
		})
	}
}

// G2 (#2): whether net/http's own transport carries a request is decided
// per send (ledger: "resolve a nil Transport to http.DefaultTransport at send
// time"). A replaced http.DefaultTransport, with Options.HTTPClient nil, is a
// transport like any other: a call completes when it has read and closed
// the body (client.go, Response.WaitRequest; Call).
func TestG2ReplacedDefaultTransport(t *testing.T) {
	orig := http.DefaultTransport
	http.DefaultTransport = echoLenRT{}
	t.Cleanup(func() { http.DefaultTransport = orig })
	c := parseAt(t, g2Doc, "", "https://h.example.test/openapi.json", nil)
	checkG2(t, c)
}

// G2 (#2): a *http.Transport that routes the scheme to another
// RoundTripper through RegisterProtocol (ledger: "detect routes the static
// type cannot see").
func TestG2RegisteredProtocol(t *testing.T) {
	tr := &http.Transport{}
	tr.RegisterProtocol("https", echoLenRT{})
	c := parseAt(t, g2Doc, "", "https://h.example.test/openapi.json", &openapi.Options{HTTPClient: &http.Client{Transport: tr}})
	checkG2(t, c)
}

// G5 (#5): a JSON Pointer token is compared with member names as decoded
// (RFC 6901 section 4; RFC 8259 section 7), so a token holding a backslash
// names the member whose decoded name holds it, not one whose raw spelling
// happens to match. Member "a\u0062" decodes to "ab"; member "a\\u0062"
// decodes to the token.
func TestG5PointerTokenWithBackslash(t *testing.T) {
	const bs = `\`
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{` +
		`"/p":{"$ref":"#/components/pathItems/a` + bs + bs + `u0062"}},` +
		`"components":{"pathItems":{` +
		`"a` + bs + `u0062":{"delete":{"operationId":"deleteEverything"}},` +
		`"a` + bs + bs + `u0062":{"get":{"operationId":"harmlessRead"}}}}}`
	var generic map[string]any
	if err := json.Unmarshal([]byte(doc), &generic); err != nil {
		t.Fatal(err)
	}
	c := parseAt(t, doc, "", testDocURI, nil)
	ops := c.Operations()
	if len(ops) != 1 || ops[0].Key != "harmlessRead" || ops[0].Method != "GET" {
		t.Fatalf("Operations = %q, want only harmlessRead (GET /p)", opKeys(ops))
	}
	if _, err := c.Operation("DELETE /p"); !errors.Is(err, openapi.ErrNoOperation) {
		t.Errorf("DELETE /p = %v, want ErrNoOperation", err)
	}
	// Client.Document reads the same node.
	if got := c.Document(testDocURI + "#/components/pathItems/a%5Cu0062"); got == nil || !strings.Contains(string(got), "harmlessRead") {
		t.Errorf("Document(#/components/pathItems/a%%5Cu0062) = %s, want the member decoded as a\\u0062", got)
	}
}

type nilTextKey struct{ N int }

func (k nilTextKey) MarshalText() ([]byte, error) { return []byte(fmt.Sprint("k", k.N)), nil }

type optsOnlyA struct {
	R io.Reader `json:",omitempty"`
}

type optsOnlyB struct{ R io.Reader }

// optsOnly holds two fields named R at the same depth, neither named by a
// tag (",omitempty" is options only), so encoding/json drops both.
type optsOnly struct {
	optsOnlyA
	optsOnlyB
	N int
}

// G9 (#11): the reader walk follows encoding/json's rules for a nil-pointer
// TextMarshaler map key (encoding/json names it "") and for a tag with only
// options, which does not name the field in dominance (client.go,
// Input.Body; encoding/json's documentation of map keys and embedded
// fields).
func TestG9ReaderWalkEdgeCases(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(jsonBodyOp), nil)
	for _, tt := range []struct {
		name string
		body any
	}{
		{"nil pointer key", map[*nilTextKey]any{nil: "a"}},
		{"options-only tags", optsOnly{optsOnlyA{strings.NewReader("x")}, optsOnlyB{strings.NewReader("y")}, 1}},
	} {
		want, err := json.Marshal(tt.body)
		if err != nil {
			t.Fatalf("%s: json.Marshal: %v", tt.name, err)
		}
		noPanic(t, tt.name, func() {
			before := w.count()
			if _, err := c.Call(t.Context(), "create", &openapi.Input{Body: tt.body}, nil); err != nil {
				t.Errorf("%s: %v", tt.name, err)
				return
			}
			if w.count() != before+1 || string(w.last(t).Body) != string(want) {
				t.Errorf("%s: sent %q, want %s as encoding/json writes it", tt.name, w.last(t).Body, want)
			}
		})
	}
	// A reader under the nil key is refused at its place, the key "".
	noPanic(t, "reader under the nil key", func() {
		before := w.count()
		resp, err := c.Call(t.Context(), "create", &openapi.Input{Body: map[*nilTextKey]any{nil: strings.NewReader("x")}}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/")
	})
}

// G10 (#12) and T1-20: the authority restriction on Variables values
// applies to "a value whose first character falls in the authority of the
// substituted URL (after "//", before the path)" (client.go,
// Options.Variables), a network-path reference included (RFC 3986 section
// 4.2); a value in the path, as {basePath} in "{scheme}://{host}{basePath}",
// is not restricted.
func TestG10NetworkPathAuthority(t *testing.T) {
	w := newWire(t, nil)
	host := w.hostport()
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"//{tenant}.api.example.test/v1","variables":{"tenant":{"default":"acme"}}}],
		"paths":{"/x":{"get":{"operationId":"op"}}}}`
	for _, v := range []string{host + "/", "a@" + host + "/", host + "?", host + "#", host + "\\"} {
		t.Run(v, func(t *testing.T) {
			c, err := openapi.Parse(t.Context(), []byte(doc), w.URL+"/openapi.json", &openapi.Options{Variables: map[string]string{"tenant": v}})
			if err != nil {
				wantKeys(t, "Settings", asRequestError(t, err).Settings, false, `Options.Variables["tenant"]`)
				return
			}
			before := w.count()
			resp, err := c.Call(t.Context(), "op", nil, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, `Options.Variables["tenant"]`)
		})
	}
	w.nothingSent(t)

	basePath := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"{scheme}://{host}{basePath}","variables":{"scheme":{"default":"http"},"host":{"default":"` + host + `"},"basePath":{"default":"/v1"}}}],
		"paths":{"/x":{"get":{"operationId":"op"}}}}`
	c := parseAt(t, basePath, "", testDocURI, &openapi.Options{Variables: map[string]string{"basePath": "/v2"}})
	mustCall(t, c, "op", nil, nil)
	if got := w.last(t).RequestURI; got != "/v2/x" {
		t.Errorf("{basePath} /v2: request target %q, want /v2/x", got)
	}
	// {host} is in the authority: restricted.
	hc, err := openapi.Parse(t.Context(), []byte(basePath), testDocURI, &openapi.Options{Variables: map[string]string{"host": host + "/"}})
	if err != nil {
		wantKeys(t, "Settings", asRequestError(t, err).Settings, false, `Options.Variables["host"]`)
	} else {
		before := w.count()
		resp, err := hc.Call(t.Context(), "op", nil, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Settings", re.Settings, false, `Options.Variables["host"]`)
	}
}

// G11 (#13): sub-delimiters in a Paths key are sent as written whatever the
// parameter value (doc.go, Fixed rules, URL: "The path is then appended as
// written"; RFC 3986 section 6.2.2.2: "(" and %28 are not equivalent).
func TestG11PathSubDelimsAsWritten(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/People('{id}')":{"get":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}},
		"/f!*$&+,;=:@/{id}":{"get":{"operationId":"subs","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}`), nil)
	for _, tt := range []struct{ key, id, want string }{
		{"op", "russell", "/People('russell')"},
		{"op", "a b", "/People('a%20b')"},
		{"subs", "x", "/f!*$&+,;=:@/x"},
		{"subs", "x y", "/f!*$&+,;=:@/x%20y"},
	} {
		mustCall(t, c, tt.key, &openapi.Input{Params: map[string]any{"id": tt.id}}, nil)
		if got := w.last(t).RequestURI; got != tt.want {
			t.Errorf("%s with id %q: request target %q, want %q", tt.key, tt.id, got, tt.want)
		}
		req := mustPrepare(t, c, tt.key, &openapi.Input{Params: map[string]any{"id": tt.id}})
		if got := req.HTTP.URL.EscapedPath(); got != tt.want {
			t.Errorf("%s with id %q: EscapedPath %q, want %q", tt.key, tt.id, got, tt.want)
		}
	}
}

// G13 (#15): a Load URI net/url rejects is refused as a URI, not read as a
// file path; the password never appears in the error (doc.go, Outcomes:
// "No credential appears in the text of an error the client creates"), and
// Loader.Fetch never sees it (T2-3: userinfo and fragments are refused).
func TestG13UnparsableLoadURI(t *testing.T) {
	for _, uri := range []string{
		"http://user:s3cret@127.0.0.1:1/%zz",
		"https://user:s3cret@api.example.test/openapi.json?x=%",
		"http://user:s3cret@[::1/openapi.json",
		"https://user:s3cret@h.example.test/a%zz#frag",
	} {
		t.Run(uri, func(t *testing.T) {
			c, err := openapi.Load(t.Context(), uri, nil)
			if err == nil || c != nil {
				t.Fatalf("Load = %v, %v; want a refusal", c, err)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("error text holds the password: %v", err)
			}
			var pe *fs.PathError
			if errors.As(err, &pe) {
				t.Errorf("the URI was read as a file path: %v", err)
			}
			fetched := false
			l := openapi.Loader{Fetch: func(ctx context.Context, u string) (io.ReadCloser, string, error) {
				fetched = true
				return io.NopCloser(strings.NewReader(`{"openapi":"3.1.0","paths":{}}`)), "", nil
			}}
			c, err = l.Load(t.Context(), uri, nil)
			if err == nil || c != nil {
				t.Errorf("Loader.Load with Fetch = %v, %v; want a refusal", c, err)
			} else if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("Loader error text holds the password: %v", err)
			}
			if fetched {
				t.Errorf("Fetch was called with a URI Load refuses")
			}
		})
	}
}

// G14 (#16): "The client never presents an incomplete upload as complete"
// (stream.go, Stream; client.go, Response.WaitRequest): a regular file body
// that shrinks after Prepare ends short of its declared length and is
// reported incomplete; and a small in-memory reader is never sent padded
// with bytes it does not hold.
func TestG14ShortBodies(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "body")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		f.Write(bytes.Repeat([]byte("a"), 10000))
		f.Seek(0, io.SeekStart)
		var got int
		rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			got = len(b)
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
		})
		c := uploadClient(t, rt, nil)
		req := mustPrepare(t, c, "octets", &openapi.Input{Body: f})
		if err := f.Truncate(4000); err != nil {
			t.Fatal(err)
		}
		resp, err := req.Send(t.Context())
		if err != nil {
			return // refused or failed in the transport: not presented as complete
		}
		resp.Body.Close()
		if err := waitResult(t, resp); err == nil {
			t.Errorf("WaitRequest = nil after the transport read %d of %d bytes", got, req.HTTP.ContentLength)
		}
	})
	t.Run("in-memory reader reset after Prepare", func(t *testing.T) {
		w := newWire(t, nil)
		c := parseFor(t, w, doc31(`"/p":{"put":{"operationId":"put","requestBody":{"content":{"application/json":{}}}}}`), nil)
		const original = `{"name":"a long enough value"}`
		r := strings.NewReader(original)
		req := mustPrepare(t, c, "put", &openapi.Input{Body: r})
		r.Reset(`{"n":1}`)
		_, err := req.Call(t.Context(), nil)
		for _, got := range w.requests() {
			if bytes.IndexByte(got.Body, 0) >= 0 {
				t.Errorf("the server received padding: %q", got.Body)
			}
		}
		// client.go, Input.Body: a *strings.Reader is sent "from the bytes
		// [it holds] when the call is prepared"; if the call succeeds, those
		// are the bytes.
		if err == nil && string(w.last(t).Body) != original {
			t.Errorf("the call succeeded sending %q, want the %q held at Prepare", w.last(t).Body, original)
		}
	})
}

// G15 (#17): untrusted text is quoted in error strings: an operation key
// and a parameter key from the document, and a server's reason phrase, so
// they cannot forge a log line or send terminal escapes.
func TestG15ErrorTextQuoting(t *testing.T) {
	w := newWire(t, jsonAnswer(500, "{}"))
	c := parseFor(t, w, doc31(`"/a":{"get":{"operationId":"getA\nlevel=ERROR msg=forged"}},
		"/b":{"get":{"operationId":"getB","parameters":[{"name":"id\nlevel=ERROR msg=forged2","in":"query","required":true,"schema":{}}]}}`), nil)
	_, err := c.Call(t.Context(), "getA\nlevel=ERROR msg=forged", nil, nil)
	var se *openapi.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v, want a *StatusError", err)
	}
	if strings.Contains(err.Error(), "\nlevel=") {
		t.Errorf("StatusError text holds the raw operation key: %q", err)
	}
	_, err = c.Call(t.Context(), "getB", nil, nil)
	re := asRequestError(t, err)
	if strings.Contains(re.Error(), "\nlevel=") {
		t.Errorf("RequestError text holds the raw parameter key: %q", re)
	}

	base := rawHTTP(t, "HTTP/1.1 404 Not Found \x1b[2J\x1b[31mFAKE\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", 0)
	_, err = openapi.Load(t.Context(), base+"/openapi.json", nil)
	if err == nil {
		t.Fatal("Load of a 404 succeeded")
	}
	if strings.ContainsRune(err.Error(), 0x1b) {
		t.Errorf("Load error text holds the server's escape bytes: %q", err)
	}
}

// G8 (#9) and T1-18: "consumed completely (read to EOF, or, for a body of
// known length, read to that length)" (client.go, Response.WaitRequest): a
// transport that reads exactly the declared length and closes has consumed
// the body completely, on every upload path.
func TestG8ExactLengthConsumer(t *testing.T) {
	framed := func(copyN bool) roundTripFunc {
		return func(r *http.Request) (*http.Response, error) {
			if copyN {
				io.CopyN(io.Discard, r.Body, r.ContentLength)
			} else {
				io.ReadFull(r.Body, make([]byte, r.ContentLength))
			}
			r.Body.Close()
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
		}
	}
	bodies := map[string]func() any{
		"bytes 8 KiB":        func() any { return bytes.Repeat([]byte("a"), 8192) },
		"bytes.Reader 8 KiB": func() any { return bytes.NewReader(bytes.Repeat([]byte("a"), 8192)) },
		"strings.Reader":     func() any { return strings.NewReader(`{"a":"bc"}`) },
		"bytes":              func() any { return []byte("hello") },
	}
	for _, copyN := range []bool{false, true} {
		c := uploadClient(t, framed(copyN), nil)
		for name, body := range bodies {
			t.Run(fmt.Sprintf("%s, CopyN %t", name, copyN), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				if _, err := c.Call(ctx, "octets", &openapi.Input{Body: body()}, nil); err != nil {
					t.Errorf("Call = %v, want nil", err)
				}
				resp, err := mustPrepare(t, c, "octets", &openapi.Input{Body: body()}).Send(ctx)
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				resp.Body.Close()
				if err := waitResult(t, resp); err != nil {
					t.Errorf("WaitRequest = %v, want nil", err)
				}
			})
		}
	}
}

// T1-19: "It relies on the transport closing the request body, as
// http.RoundTripper requires; with a transport that neither reads nor
// closes it, the wait, and Call's, ends only with the context" (client.go,
// Response.WaitRequest). The call ends at its deadline, not before and not
// never, with the Response and the context's error.
func TestT1_19TransportIgnoringBody(t *testing.T) {
	canned := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader("{}")), ContentLength: 2, Request: r}, nil
	})
	c := uploadClient(t, canned, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	var resp *openapi.Response
	var err error
	promptly(t, 5*time.Second, "Call", func() {
		resp, err = c.Call(ctx, "put", &openapi.Input{Body: []byte(`{"a":1}`)}, nil)
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Call = %v, want the context's error", err)
	}
	if resp == nil || resp.StatusCode != 200 {
		t.Errorf("Response = %v, want the 200", resp)
	}
}

// T1-21: a DecodeError's text may quote "a short token" of the body through
// a decoder's message, and never the body itself (errors.go,
// DecodeError.Error).
func TestT1_21DecodeErrorQuotesAtMostAToken(t *testing.T) {
	body := "SECRETBODY-" + strings.Repeat("x", 200)
	_, c := respClient(t, jsonAnswer(200, body), nil)
	_, err := c.Call(t.Context(), "getPet", nil, new(Pet))
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error %v, want a *DecodeError", err)
	}
	if strings.Contains(err.Error(), "SECRETBODY-xxxx") {
		t.Errorf("DecodeError text holds the body: %q", err)
	}
}

// G18 (T2-5): "a value that forms a whole "." or ".." segment of the path is
// refused, as for path parameters" (client.go, Options.Variables), at
// Options.Variables["name"]; a value forming any other segment is used.
func TestG18ServerVariableDotSegments(t *testing.T) {
	w := newWire(t, nil)
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"@BASE@/{v}/api","variables":{"v":{"default":"v1"}}},{"url":"@BASE@/x{w}/api","variables":{"w":{"default":"1"}}}],
		"paths":{"/p":{"get":{"operationId":"op"}}}}`
	for _, tt := range []struct {
		name, value string
	}{{"v", "."}, {"v", ".."}} {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			opts := &openapi.Options{Server: w.URL + "/{v}/api", Variables: map[string]string{tt.name: tt.value}}
			c, err := openapi.Parse(t.Context(), []byte(expand(doc, w.URL)), w.URL+"/openapi.json", opts)
			key := fmt.Sprintf("Options.Variables[%q]", tt.name)
			if err != nil {
				wantKeys(t, "Settings", asRequestError(t, err).Settings, false, key)
				return
			}
			before := w.count()
			resp, err := c.Call(t.Context(), "op", nil, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, key)
		})
	}
	for _, tt := range []struct{ server, name, value, want string }{
		{"/{v}/api", "v", "...", "/.../api/p"},
		{"/{v}/api", "v", ".v", "/.v/api/p"},
		{"/x{w}/api", "w", ".", "/x./api/p"},
	} {
		t.Run(tt.name+"="+tt.value+" sent", func(t *testing.T) {
			c := parseFor(t, w, doc, &openapi.Options{Server: w.URL + tt.server, Variables: map[string]string{tt.name: tt.value}})
			mustCall(t, c, "op", nil, nil)
			if got := w.last(t).RequestURI; got != tt.want {
				t.Errorf("request target %q, want %q", got, tt.want)
			}
		})
	}
}

// T1-22 (ledger: "a value followed by a literal "://" must be a URI scheme
// (RFC 3986 3.1); only a variable that starts the URL and is not followed
// by "://" is an unrestricted whole URL"; client.go, Options.Variables): in
// "{scheme}://{host}/v1", the scheme must match ALPHA *( ALPHA / DIGIT /
// "+" / "-" / "." ), and anything else is refused at
// Options.Variables["scheme"] with nothing sent, so a scheme value cannot
// carry a host. "{endpoint}/v1" stays an unrestricted whole URL.
func TestT1_22SchemeVariableIsAScheme(t *testing.T) {
	w := newWire(t, nil)
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"{scheme}://{host}/v1","variables":{"scheme":{"default":"https"},"host":{"default":"` + w.hostport() + `"}}}],
		"paths":{"/x":{"get":{"operationId":"op"}}}}`
	t.Run("http", func(t *testing.T) {
		c := parseAt(t, doc, "", testDocURI, &openapi.Options{Variables: map[string]string{"scheme": "http"}})
		before := w.count()
		mustCall(t, c, "op", nil, nil)
		if got := w.last(t).RequestURI; w.count() != before+1 || got != "/v1/x" {
			t.Errorf("request target %q, want /v1/x", got)
		}
	})
	t.Run("https", func(t *testing.T) {
		// Accepted; the test server speaks plain http, so only the refusal
		// matters here: preparing the call is not refused.
		c := parseAt(t, doc, "", testDocURI, &openapi.Options{Variables: map[string]string{"scheme": "https"}})
		req := mustPrepare(t, c, "op", nil)
		if req.HTTP.URL.Scheme != "https" || req.HTTP.URL.Host != w.hostport() {
			t.Errorf("prepared URL %s, want https://%s/v1/x", req.HTTP.URL, w.hostport())
		}
	})
	for _, v := range []string{"http://evil.example#", "1http", "ht tp", ""} {
		t.Run(fmt.Sprintf("refused %q", v), func(t *testing.T) {
			before := w.count()
			c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{Variables: map[string]string{"scheme": v}})
			if err != nil {
				wantKeys(t, "Settings", asRequestError(t, err).Settings, false, `Options.Variables["scheme"]`)
				return
			}
			resp, err := c.Call(t.Context(), "op", nil, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, `Options.Variables["scheme"]`)
		})
	}

	t.Run("whole URL", func(t *testing.T) {
		endpoint := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
			"servers":[{"url":"{endpoint}/v1","variables":{"endpoint":{"default":"https://api.example/x"}}}],
			"paths":{"/x":{"get":{"operationId":"op"}}}}`
		// The default, a URL with a path, is used as given.
		c := parseAt(t, endpoint, "", testDocURI, nil)
		req := mustPrepare(t, c, "op", nil)
		if got := req.HTTP.URL.String(); got != "https://api.example/x/v1/x" {
			t.Errorf("prepared URL %q, want https://api.example/x/v1/x", got)
		}
		// The test server's URL, given in Options.Variables, is sent.
		c = parseAt(t, endpoint, "", testDocURI, &openapi.Options{Variables: map[string]string{"endpoint": w.URL + "/x"}})
		before := w.count()
		mustCall(t, c, "op", nil, nil)
		if got := w.last(t).RequestURI; w.count() != before+1 || got != "/x/v1/x" {
			t.Errorf("request target %q, want /x/v1/x", got)
		}
	})
}
