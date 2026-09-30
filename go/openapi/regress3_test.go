package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the stage 1 verification pass (ledger, "Verification
// pass"; verify/panel.json "#N"): class rulings P1 to P3, fixes H1 to H10,
// and the server variable rule T1-23.

// sharedOperation returns a document with n Paths entries, each a $ref to
// one Path Item whose single operation holds m servers, m query parameters
// or m response media types (kind "servers", "params" or "media").
func sharedOperation(n, m int, kind string) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{`)
	for i := range n {
		fmt.Fprintf(&b, `"/p%d":{"$ref":"#/components/pathItems/a"},`, i)
	}
	b.WriteString(`"/z":{"get":{}}},"components":{"pathItems":{"a":{"post":{"requestBody":{"content":{`)
	for j := range m {
		if j > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"text/x%d":{}`, j)
	}
	b.WriteString(`}}`)
	switch kind {
	case "servers":
		b.WriteString(`,"servers":[`)
		for j := range m {
			if j > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"url":"https://s%d.example.test"}`, j)
		}
		b.WriteString(`]`)
	case "params":
		b.WriteString(`,"parameters":[`)
		for j := range m {
			if j > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"name":"q%d","in":"query","schema":{}}`, j)
		}
		b.WriteString(`]`)
	case "media":
		b.WriteString(`,"responses":{"200":{"description":"ok","content":{`)
		for j := range m {
			if j > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `"text/y%d":{}`, j)
		}
		b.WriteString(`}}}`)
	}
	b.WriteString(`}}}}}`)
	return []byte(b.String())
}

// H1 and P1 (ledger: "every compiled or decoded form is computed at most
// once per document node"; H1: "compile each Operation Object node once and
// share its path-independent parts"): N Paths entries sharing one Operation
// Object with M servers, parameters or response media types cost
// Operations() time and retained memory linear in the document, N and M
// growing together; the first Call too.
func TestH1SharedOperationObject(t *testing.T) {
	for _, kind := range []string{"servers", "params", "media"} {
		// Compiling the shared Operation Object once per Paths entry made
		// N times M compiled parts: checked on allocations (stage 2 ledger,
		// test maintenance), which quadrupled 16 times before the fix.
		wantLinearAllocs(t, kind+": Operations()", 125, func(n int) func() {
			return timedOperations(t, sharedOperation(n, n, kind))
		})
		t.Run(kind+": retained after Operations()", func(t *testing.T) {
			keep := func(n int) int64 {
				c, err := openapi.Parse(context.Background(), sharedOperation(n, n, kind), testDocURI, nil)
				if err != nil {
					t.Fatal(err)
				}
				return retainedBy(func() any { return c.Operations() })
			}
			small, large := keep(125), keep(500)
			ratio := float64(large) / float64(max(small, 64<<10))
			t.Logf("125: %d bytes, 500: %d bytes (%.1fx)", small, large, ratio)
			if ratio > 8 {
				t.Errorf("four times the input retained %.1f times the memory; want linear", ratio)
			}
		})
		wantLinear(t, kind+": first Call", 125, func(n int) func() {
			clients := freshClients(t, sharedOperation(n, n, kind), func() *openapi.Options {
				return &openapi.Options{HTTPClient: &http.Client{Transport: cannedRT{}}, BaseURL: "https://api.example.test"}
			}, scaleRuns)
			i := 0
			return func() {
				c := clients[i%len(clients)]
				i++
				if _, err := c.Call(context.Background(), "POST /p0", &openapi.Input{Body: []byte("x"), MediaType: "text/x0"}, nil); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// H5 (ledger: "checkNames once per node"): Load with Options.Server or
// Options.MediaType that no server or media type matches, over the H1
// fan-in document, checks each shared Operation Object once.
func TestH5LoadChecksScale(t *testing.T) {
	refused := func(doc []byte, opts func() *openapi.Options) func() {
		return func() {
			_, err := openapi.Parse(context.Background(), doc, testDocURI, opts())
			var re *openapi.RequestError
			if !errors.As(err, &re) {
				t.Fatalf("Parse = %v, want the Options refused", err)
			}
		}
	}
	wantLinear(t, "Options.Server", 500, func(n int) func() {
		return refused(sharedOperation(n, n, "servers"), func() *openapi.Options { return &openapi.Options{Server: "https://nowhere.example.test"} })
	})
	wantLinear(t, "Options.MediaType", 500, func(n int) func() {
		return refused(sharedOperation(n, n, "servers"), func() *openapi.Options { return &openapi.Options{MediaType: "application/xml"} })
	})
}

// escapedQuotes returns n bytes of escaped quotes: \" repeated.
func escapedQuotes(n int) string { return strings.Repeat(`\"`, n/2) }

// H2 (ledger: "str consults the decoded cache before scanning; name
// comparison reads forward only as far as the key"): strings made of escaped
// quotes, as long member names on the path to $ref targets and as a shared
// description, cost Parse and Operations() linear in the document when the
// strings and the references grow together.
func TestH2EscapedQuoteStrings(t *testing.T) {
	names := func(k int, refs bool) []byte {
		var b strings.Builder
		b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},`)
		for i := range 12 {
			fmt.Fprintf(&b, `"x-%d%s":0,`, i, escapedQuotes(k*8<<10))
		}
		b.WriteString(`"paths":{`)
		for i := range k * 200 {
			if i > 0 {
				b.WriteByte(',')
			}
			if refs {
				fmt.Fprintf(&b, `"/p%d":{"$ref":"#/components/pathItems/a"}`, i)
			} else {
				fmt.Fprintf(&b, `"/p%d":{"get":{}}`, i)
			}
		}
		b.WriteString(`},"components":{"pathItems":{"a":{"get":{}}}}}`)
		return []byte(b.String())
	}
	wantLinear(t, "long escaped names, Parse", 1, func(k int) func() { return parseDoc(t, names(k, true), nil) })
	wantLinear(t, "long escaped names, Operations()", 1, func(k int) func() { return timedOperations(t, names(k, false)) })
	wantLinear(t, "shared escaped description, Operations()", 1, func(k int) func() {
		var b strings.Builder
		b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{`)
		for i := range k * 125 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `"/p%d":{"get":{"responses":{"200":{"$ref":"#/components/responses/r"}}}}`, i)
		}
		b.WriteString(`},"components":{"responses":{"r":{"description":"` + escapedQuotes(k*48<<10) + `"}}}}`)
		return timedOperations(t, []byte(b.String()))
	})
}

// varDoc returns a document whose single server is url, with variables
// declared by name and default, and one operation "op" at GET /x.
func varDoc(url string, defaults map[string]string) string {
	var vars []string
	for name, d := range defaults {
		vars = append(vars, fmt.Sprintf(`%q:{"default":%q}`, name, d))
	}
	return `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"` + url + `","variables":{` +
		strings.Join(vars, ",") + `}}],"paths":{"/x":{"get":{"operationId":"op"}}}}`
}

// H3, P2 and T1-23 (client.go, Options.Variables: "Each variable is placed
// by where its default falls in the URL with every default substituted. A
// variable whose default spans "://" supplies a whole URL and is not
// restricted. Otherwise a value may change only its own part"). Each
// refusal is keyed Options.Variables["<name>"] (at Load or at the call),
// and nothing is sent, to the document's host or to the host a value names.
func TestH3VariablesByPart(t *testing.T) {
	home, evil := newWire(t, nil), newWire(t, nil)
	evilHost := evil.hostport()
	refused := []struct {
		name, url, variable, deflt, value string
	}{
		// A path value supplying the second "/" of "//" would create an
		// authority (RFC 3986 section 4.2).
		{"relative path makes //", "/{tenant}/api", "tenant", "acme", "/" + evilHost},
		{"relative path makes // with a path", "/{tenant}/api", "tenant", "acme", "/" + evilHost + "/x"},
		{"after the scheme's colon", "http:{rest}", "rest", "acme", "//" + evilHost},
		{"second / of //", "http:/{x}", "x", "acme", "/" + evilHost},
		// p's default "http" lies in the scheme "https": the value must leave a
		// valid scheme (RFC 3986 section 3.1).
		{"scheme part", "{p}s://api.example.test", "p", "http", "https://127.0.0.1:1/v1/"},
		{"inside the scheme", "ht{x}tp://api.example.test", "x", "", "tp://" + evilHost + "/"},
		// A path value may not form a dot segment, percent-encoded or not (RFC
		// 3986 sections 5.2.4 and 6.2.2.2).
		{"encoded dot-dot", "@BASE@/tenants/{tenant}/api", "tenant", "acme", "%2e%2e"},
		{"encoded dot, dot", "@BASE@/tenants/{tenant}/api", "tenant", "acme", "%2E."},
		{"dot, encoded dot", "@BASE@/tenants/{tenant}/api", "tenant", "acme", ".%2e"},
		{"encoded dot", "@BASE@/tenants/{tenant}/api", "tenant", "acme", "%2E"},
		{"encoded dot-dot inside a longer value", "@BASE@/tenants/{tenant}/api", "tenant", "acme", "acme/%2e%2e/%2e%2e"},
		// A path value may not add a query or fragment.
		{"query in the path", "@BASE@/tenants/{tenant}/api", "tenant", "acme", "a?b=1"},
		{"fragment in the path", "@BASE@/tenants/{tenant}/api", "tenant", "acme", "a#b"},
	}
	for _, tt := range refused {
		t.Run("refused: "+tt.name, func(t *testing.T) {
			doc := expand(varDoc(tt.url, map[string]string{tt.variable: tt.deflt}), home.URL)
			key := fmt.Sprintf("Options.Variables[%q]", tt.variable)
			before, evilBefore := home.count(), evil.count()
			c, err := openapi.Parse(t.Context(), []byte(doc), home.URL+"/openapi.json", &openapi.Options{Variables: map[string]string{tt.variable: tt.value}})
			if err != nil {
				wantKeys(t, "Settings", asRequestError(t, err).Settings, false, key)
				return
			}
			resp, err := c.Call(t.Context(), "op", nil, nil)
			re := refusedSince(t, home, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, key)
			if evil.count() != evilBefore {
				t.Errorf("the request reached the host the value names")
			}
		})
	}

	allowed := []struct {
		name, url string
		defaults  map[string]string
		variable  string
		value     string
		want      string // the prepared URL
		send      bool   // the URL is the test server's, so the call is sent there
	}{
		// The resulting scheme "http" is valid.
		{"empty scheme suffix", "http{s}://@HOSTPORT@/v1", map[string]string{"s": "s"}, "s", "", "@BASE@/v1/x", true},
		// A default spanning "://" is a whole URL.
		{"whole URL", "{endpoint}/v1", map[string]string{"endpoint": "https://api.example/x"}, "endpoint", "@BASE@/e", "@BASE@/e/v1/x", true},
		// A path value.
		{"basePath", "{scheme}://{host}{basePath}", map[string]string{"scheme": "http", "host": "@HOSTPORT@", "basePath": "/v1"}, "basePath", "/v2", "@BASE@/v2/x", true},
		// An authority value may change the host: by rule; an enum is the
		// document's restriction (ledger P2).
		{"host moved by an authority value", "http://{host}/v1", map[string]string{"host": "api.example.test"}, "host", "@HOSTPORT@", "@BASE@/v1/x", true},
		{"domain labels appended", "https://api.example.test{port}", map[string]string{"port": ":443"}, "port", ".attacker.test", "https://api.example.test.attacker.test/x", false},
	}
	for _, tt := range allowed {
		t.Run("allowed: "+tt.name, func(t *testing.T) {
			doc := expand(varDoc(tt.url, tt.defaults), home.URL)
			value, want := expand(tt.value, home.URL), expand(tt.want, home.URL)
			c := parseAt(t, doc, "", testDocURI, &openapi.Options{Variables: map[string]string{tt.variable: value}})
			req := mustPrepare(t, c, "op", nil)
			if got := req.HTTP.URL.String(); got != want {
				t.Errorf("prepared URL %q, want %q", got, want)
			}
			if tt.send {
				before := home.count()
				mustCall(t, c, "op", nil, nil)
				if home.count() != before+1 {
					t.Errorf("the call did not reach %s", want)
				}
			}
		})
	}
}

// H6 and P3 (ledger: "Every body goes through the reporting reader with the
// T1-18 known-length rule"): a registered alternative-protocol transport
// that reads and closes the body in a goroutine after RoundTrip returns, as
// the http.RoundTripper contract allows, causes no data race (run with
// -race), and WaitRequest and Call report what it actually consumed.
func TestH6AsyncRegisteredProtocol(t *testing.T) {
	type async struct {
		wg   sync.WaitGroup
		read int // bytes to read before closing; -1 for all
	}
	newTransport := func(a *async) *http.Transport {
		rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			a.wg.Add(1)
			go func() {
				defer a.wg.Done()
				if a.read < 0 {
					io.Copy(io.Discard, r.Body)
				} else {
					io.ReadFull(r.Body, make([]byte, a.read))
				}
				r.Body.Close()
			}()
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader("{}")), ContentLength: 2, Request: r}, nil
		})
		tr := &http.Transport{}
		tr.RegisterProtocol("https", rt)
		return tr
	}
	bodies := map[string]func() any{
		"bytes":          func() any { return []byte(strings.Repeat("a", 3000)) },
		"strings.Reader": func() any { return strings.NewReader(strings.Repeat("a", 3000)) },
		"encoded":        func() any { return map[string]string{"a": strings.Repeat("a", 3000)} },
	}
	for name, body := range bodies {
		for _, complete := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, read completely %t", name, complete), func(t *testing.T) {
				a := &async{read: -1}
				if !complete {
					a.read = 10
				}
				c := uploadClient(t, newTransport(a), nil)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				_, err := c.Call(ctx, "put", &openapi.Input{Body: body()}, nil)
				if complete && err != nil {
					t.Errorf("Call = %v, want nil", err)
				}
				if !complete && err == nil {
					t.Errorf("Call = nil after the transport read 10 bytes and closed")
				}
				resp, err := mustPrepare(t, c, "put", &openapi.Input{Body: body()}).Send(ctx)
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				resp.Body.Close()
				werr := waitResult(t, resp)
				if complete && werr != nil {
					t.Errorf("WaitRequest = %v, want nil", werr)
				}
				if !complete && werr == nil {
					t.Errorf("WaitRequest = nil after the transport read 10 bytes and closed")
				}
				a.wg.Wait()
			})
		}
	}
}

// H7 (ledger: "ErrNoOperation's key list quoted like other keys"): a Paths
// key holding a newline appears quoted in the error listing the operations
// that share an operationId.
func TestH7NoOperationListQuoted(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/a\nlevel=ERROR msg=forged":{"get":{"operationId":"dup"}},"/b":{"get":{"operationId":"dup"}}`), nil)
	_, err := c.Operation("dup")
	if !errors.Is(err, openapi.ErrNoOperation) {
		t.Fatalf("Operation(dup) = %v, want ErrNoOperation", err)
	}
	if strings.Contains(err.Error(), "\nlevel=") {
		t.Errorf("Operation error text holds the raw Paths key: %q", err)
	}
	_, err = c.Call(t.Context(), "dup", nil, nil)
	if err == nil || strings.Contains(err.Error(), "\nlevel=") {
		t.Errorf("Call error text holds the raw Paths key: %q", err)
	}
	w.nothingSent(t)
}

// H8 (ledger: "the known-length rule applies to a caller-set body when the
// request declares ContentLength"; client.go, Request.HTTP: a caller who
// replaces the body sets GetBody and ContentLength with it, or clears
// GetBody; T1-18): a transport that reads exactly the declared length and
// closes has consumed the body completely; a body shorter than its declared
// length has not.
func TestH8CallerSetBodyKnownLength(t *testing.T) {
	var got int
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		n := int64(0)
		if r.ContentLength > 0 {
			n, _ = io.CopyN(io.Discard, r.Body, r.ContentLength)
		} else {
			n, _ = io.Copy(io.Discard, r.Body)
		}
		got = int(n)
		r.Body.Close()
		return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	c := uploadClient(t, rt, nil)
	for _, tt := range []struct {
		name     string
		body     string
		declared int64
		complete bool
	}{
		{"exact length", "1234567", 7, true},
		{"shorter than declared", "1234567", 100, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := mustPrepare(t, c, "octets", &openapi.Input{Body: []byte("x")})
			req.HTTP.Body = io.NopCloser(strings.NewReader(tt.body))
			req.HTTP.ContentLength = tt.declared
			req.HTTP.GetBody = nil
			resp, err := req.Send(t.Context())
			if err != nil {
				if !tt.complete {
					return // failed in the transport: not presented as complete
				}
				t.Fatalf("Send: %v", err)
			}
			resp.Body.Close()
			werr := waitResult(t, resp)
			if tt.complete && werr != nil {
				t.Errorf("WaitRequest = %v after the transport read the %d declared bytes", werr, got)
			}
			if !tt.complete && werr == nil {
				t.Errorf("WaitRequest = nil after the transport read %d of %d declared bytes", got, tt.declared)
			}
		})
	}
}

// H9 (ledger: "a Load URI with leading or trailing whitespace is refused
// (not a path)"): no password in the text, and Loader.Fetch is not called
// (T2-3; doc.go, Outcomes).
func TestH9PaddedLoadURI(t *testing.T) {
	for _, uri := range []string{
		" https://u:s3cret@127.0.0.1:1/x",
		"\thttps://u:s3cret@127.0.0.1:1/x",
		"https://127.0.0.1:1/x ",
		"https://u:s3cret@127.0.0.1:1/x\n",
	} {
		t.Run(fmt.Sprintf("%q", uri), func(t *testing.T) {
			c, err := openapi.Load(t.Context(), uri, nil)
			if err == nil || c != nil {
				t.Fatalf("Load = %v, %v; want a refusal", c, err)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("error text holds the password: %v", err)
			}
			fetched := false
			l := openapi.Loader{Fetch: func(ctx context.Context, u string) (io.ReadCloser, string, error) {
				fetched = true
				return io.NopCloser(strings.NewReader(`{"openapi":"3.1.0","paths":{}}`)), "", nil
			}}
			if c, err := l.Load(t.Context(), uri, nil); err == nil || c != nil {
				t.Errorf("Loader.Load = %v, %v; want a refusal", c, err)
			} else if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("Loader error text holds the password: %v", err)
			}
			if fetched {
				t.Errorf("Fetch was called")
			}
		})
	}
}

// H10 (ledger: "an unusable server from Variables values is keyed
// Options.Variables["<name>"], not Options.BaseURL"): values that make the
// server URL unusable are refused at their key, with nothing sent.
func TestH10UnusableServerFromValues(t *testing.T) {
	w := newWire(t, nil)
	host, port, _ := strings.Cut(w.hostport(), ":")
	for _, tt := range []struct {
		name, url, variable, deflt, value string
	}{
		{"port that is not a number", "http://" + host + ":{port}/v1", "port", port, "notaport"},
		{"empty host", "http://{host}/v1", "host", w.hostport(), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := varDoc(tt.url, map[string]string{tt.variable: tt.deflt})
			key := fmt.Sprintf("Options.Variables[%q]", tt.variable)
			before := w.count()
			c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{Variables: map[string]string{tt.variable: tt.value}})
			if err != nil {
				wantKeys(t, "Settings", asRequestError(t, err).Settings, false, key)
				return
			}
			resp, err := c.Call(t.Context(), "op", nil, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, key)
			if _, ok := re.Settings["Options.BaseURL"]; ok {
				t.Errorf("the refusal is keyed Options.BaseURL")
			}
		})
	}
}

// T1-24 (refines T1-23; client.go, Options.Variables: "A variable whose
// default spans "://", or that is the whole URL template, supplies a whole
// URL and is not restricted. An empty default at the boundary between two
// parts may take a value belonging to either"). Where a value is refused it
// is keyed Options.Variables["<name>"] (at Load or at the call), and
// nothing is sent, to the test server or to a host the value names.
func TestT1_24WholeTemplateAndBoundaryVariables(t *testing.T) {
	w, evil := newWire(t, nil), newWire(t, nil)
	host, port, _ := strings.Cut(w.hostport(), ":")
	sent := []struct {
		name, url, variable, value, want string
	}{
		// The whole template: a whole URL.
		{"whole template", "{endpoint}", "endpoint", w.URL + "/api", "/api/x"},
		// A boundary default between the authority and the path takes an
		// authority value...
		{"port at the boundary", "http://" + host + "{port}", "port", ":" + port, "/x"},
		{"no-op at the boundary", "http://" + host + ":" + port + "{port}", "port", "", "/x"},
		// ...or a path value.
		{"basePath at the boundary", "http://" + host + ":" + port + "{basePath}", "basePath", "/v2", "/v2/x"},
	}
	for _, tt := range sent {
		t.Run("sent: "+tt.name, func(t *testing.T) {
			doc := varDoc(tt.url, map[string]string{tt.variable: ""})
			c := parseAt(t, doc, "", testDocURI, &openapi.Options{Variables: map[string]string{tt.variable: tt.value}})
			before := w.count()
			mustCall(t, c, "op", nil, nil)
			if got := w.last(t).RequestURI; w.count() != before+1 || got != tt.want {
				t.Errorf("request target %q, want %q", got, tt.want)
			}
		})
	}
	refused := []struct {
		name, url, variable, value string
	}{
		// A path value may not add a query or form a dot segment, encoded or
		// not.
		{"basePath with a query", "http://" + host + ":" + port + "{basePath}", "basePath", "/v2?x"},
		{"basePath with an encoded dot segment", "http://" + host + ":" + port + "{basePath}", "basePath", "/%2e%2e"},
		// Neither an authority value nor a path value may hold "@" or "?".
		{"userinfo at the boundary", "http://" + host + "{port}", "port", "@" + evil.hostport()},
		{"query at the boundary", "http://" + host + "{port}", "port", "?x"},
	}
	for _, tt := range refused {
		t.Run("refused: "+tt.name, func(t *testing.T) {
			doc := varDoc(tt.url, map[string]string{tt.variable: ""})
			key := fmt.Sprintf("Options.Variables[%q]", tt.variable)
			before, evilBefore := w.count(), evil.count()
			c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{Variables: map[string]string{tt.variable: tt.value}})
			if err != nil {
				wantKeys(t, "Settings", asRequestError(t, err).Settings, false, key)
				return
			}
			resp, err := c.Call(t.Context(), "op", nil, nil)
			if evil.count() != evilBefore {
				t.Errorf("the request reached the host the value names")
			}
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, key)
		})
	}
}
