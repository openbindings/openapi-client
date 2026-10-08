package openapi_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// serverDoc builds an OpenAPI 3.1.0 document whose operation "op", at
// /x, has servers as its root servers array.
func serverDoc(servers string) string {
	return `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[` + servers +
		`],"paths":{"/x":{"get":{"operationId":"op"}}}}`
}

// A server URL that no Options.Variables value can make usable has
// Server.Err, set by the document alone: a literal port that is not a
// port, digits out of range included (a TCP port is 16 bits, RFC 9293
// section 3.1); an empty host, with a port or without; a relative URL in a
// document not retrieved over http or https, whether or not it begins with
// "/". The call keys none of its variables, so a sole such server is keyed
// Options.BaseURL, the setting that fixes it, whatever values are given.
// describe.go, Server.Err: "Err is why the document alone makes the server
// unusable, or nil (see URL in the package documentation)"; doc.go, Fixed
// rules, URL: "only an http or https URI is such a base, and otherwise the
// server cannot be used" and "After substitution, a server URL that url.Parse
// refuses, such as one whose port holds anything but digits, cannot be used
// either, nor can one with no host, userinfo, a query or a fragment
// (Server.Err, where the document alone decides it)"; Configuration: "When a
// call finds no usable server, RequestError.Settings keys each variable that
// has no value, or whose Options.Variables value is refused, of each server
// whose Err is nil, never of one whose Err is set ... If neither is set,
// Settings also keys Options.BaseURL, unless the operation has one server and
// one of its variables is keyed."
func TestServerUnusableByTheDocumentAlone(t *testing.T) {
	w := newWire(t, nil)
	_, port, _ := strings.Cut(w.hostport(), ":")
	for _, tt := range []struct {
		name, docURI, server string
		values               map[string]string // values a caller might try
	}{
		{"literal port that is not a port", testDocURI,
			`{"url":"https://a.example:abc/{v}","variables":{"v":{"default":"x"}}}`, map[string]string{"v": "y"}},
		{"literal port that is not a port, host variable", testDocURI,
			`{"url":"http://{h}:abc/","variables":{"h":{"default":"a.example"}}}`, map[string]string{"h": "b.example"}},
		// The loopback host keeps a dial of the port from leaving the
		// machine.
		{"literal port out of range", testDocURI,
			`{"url":"http://127.0.0.1:99999/{v}","variables":{"v":{"default":"x"}}}`, map[string]string{"v": "y"}},
		{"literal port out of range, no variables", testDocURI, `{"url":"http://127.0.0.1:99999/"}`, nil},
		{"empty host", testDocURI,
			`{"url":"https:///{v}","variables":{"v":{"default":"x"}}}`, map[string]string{"v": "y"}},
		// net/http would dial the empty host as the local machine, where
		// the test server listens on this port.
		{"empty host with a port", testDocURI,
			`{"url":"http://:` + port + `/{v}","variables":{"v":{"default":"x"}}}`, map[string]string{"v": "y"}},
		{"empty host with a port, no variables", testDocURI, `{"url":"http://:` + port + `/"}`, nil},
		{"relative path, file document", "file:///srv/api/openapi.json",
			`{"url":"api/{v}","variables":{"v":{"default":"x"}}}`, map[string]string{"v": "y"}},
		{"relative path led by a variable, file document", "file:///srv/api/openapi.json",
			`{"url":"{v}/x","variables":{"v":{"default":"api"}}}`, map[string]string{"v": "v2"}},
		{"absolute path, file document", "file:///srv/api/openapi.json",
			`{"url":"/{v}","variables":{"v":{"default":"x"}}}`, map[string]string{"v": "y"}},
		{"relative path, content parsed without a uri", "",
			`{"url":"api/{v}","variables":{"v":{"default":"x"}}}`, map[string]string{"v": "y"}},
		{"relative path, no default, file document", "file:///srv/api/openapi.json",
			`{"url":"api/{v}","variables":{"v":{"enum":["x"]}}}`, map[string]string{"v": "x"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := serverDoc(tt.server)
			for _, values := range []map[string]string{nil, tt.values} {
				c, err := openapi.Parse(context.Background(), []byte(doc), tt.docURI, &openapi.Options{Variables: values})
				if err != nil {
					t.Fatalf("Parse with Variables %v: %v", values, err)
				}
				s := mustOp(t, c, "op").Servers[0]
				if s.Err == nil {
					t.Errorf("with Variables %v, Server.Err = nil for %s, which no value can make usable", values, s.URL)
				}
				before := w.count()
				resp, err := c.Call(t.Context(), "op", nil, nil)
				re := refusedSince(t, w, before, resp, err)
				wantKeys(t, "Settings", re.Settings, true, "Options.BaseURL")
			}

			// Options.BaseURL is the fix.
			c, err := openapi.Parse(context.Background(), []byte(doc), tt.docURI, &openapi.Options{BaseURL: w.URL})
			if err != nil {
				t.Fatalf("Parse with BaseURL: %v", err)
			}
			before := w.count()
			mustCall(t, c, "op", nil, nil)
			if w.count() != before+1 {
				t.Errorf("Options.BaseURL did not send the call")
			}
		})
	}
}

// Beside a server the document alone makes unusable, another server's
// variable without a value is keyed, Options.BaseURL is keyed for the
// several servers, and the unusable server's variable is not keyed, though
// a value is given for it. doc.go, Configuration: Settings keys such
// variables "of each server whose Err is nil, never of one whose Err is set".
func TestUnusableServerVariableNotKeyedBesideAnother(t *testing.T) {
	doc := serverDoc(`{"url":"https://a.example:abc/{v}","variables":{"v":{"default":"x"}}},` +
		`{"url":"https://b.example/{w}","variables":{"w":{"enum":["a"]}}}`)
	c, err := openapi.Parse(context.Background(), []byte(doc), testDocURI, &openapi.Options{Variables: map[string]string{"v": "y"}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	op := mustOp(t, c, "op")
	if op.Servers[0].Err == nil || op.Servers[1].Err != nil {
		t.Errorf("Server.Err = %v, %v; want the first set and the second nil", op.Servers[0].Err, op.Servers[1].Err)
	}
	resp, err := c.Call(t.Context(), "op", nil, nil)
	re := refusedBeforeSending(t, nil, resp, err)
	wantKeys(t, "Settings", re.Settings, true, "Options.BaseURL", `Options.Variables["w"]`)
}

// A server a variable's value can repair has no Server.Err, and the call
// keys that variable when its value cannot be used: the port and the host
// are variables here, so the document alone does not decide. errors.go,
// RequestError.Settings: "A server made unusable by a variable that has no
// value, or by Options.Variables values, is keyed by variable, in the form
// above, when its Err is nil: by the variable without a value, by one whose
// value is off its enum or unfit for its place in the URL, or, when the URL
// the given values form cannot be used, by each variable given a value."
func TestRepairableServerKeysItsVariable(t *testing.T) {
	for _, tt := range []struct {
		name, variable string
		server         func(host, port string) string
		bad            string
		good           func(host, port string) string
	}{
		{"port variable", "p",
			func(host, _ string) string {
				return `{"url":"http://` + host + `:{p}/","variables":{"p":{"default":"x1"}}}`
			},
			"abc", func(_, port string) string { return port }},
		{"host variable", "h",
			func(string, string) string {
				return `{"url":"http://{h}/","variables":{"h":{"description":"no default"}}}`
			},
			"", func(host, port string) string { return host + ":" + port }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newWire(t, nil)
			host, port, _ := strings.Cut(w.hostport(), ":")
			c := parseAt(t, serverDoc(tt.server(host, port)), w.URL, testDocURI, nil)
			if s := mustOp(t, c, "op").Servers[0]; s.Err != nil {
				t.Fatalf("Server.Err = %v for %s, which a value can repair", s.Err, s.URL)
			}
			key := fmt.Sprintf("Options.Variables[%q]", tt.variable)
			bad := c.With(func(o *openapi.Options) { o.Variables = map[string]string{tt.variable: tt.bad} })
			resp, err := bad.Call(t.Context(), "op", nil, nil)
			re := refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Settings", re.Settings, true, key)

			good := c.With(func(o *openapi.Options) { o.Variables = map[string]string{tt.variable: tt.good(host, port)} })
			mustCall(t, good, "op", nil, nil)
			if got := w.only(t).RequestURI; got != "/x" {
				t.Errorf("request target %q, want /x", got)
			}
		})
	}
}

// A relative server URL in a document retrieved over http or https is
// usable, whether or not it begins with "/". doc.go, Fixed rules, URL: "A
// relative result then resolves, by RFC 3986 section 5.2, against the URI of
// the document that contains the Server Object".
func TestRelativeServerUsableInHTTPDocument(t *testing.T) {
	w := newWire(t, nil)
	for _, tt := range []struct{ url, want string }{
		{"api/{v}", "/specs/api/x/x"},
		{"/{v}", "/x/x"},
	} {
		t.Run(tt.url, func(t *testing.T) {
			c := parseAt(t, serverDoc(`{"url":"`+tt.url+`","variables":{"v":{"default":"x"}}}`), w.URL, w.URL+"/specs/openapi.json", nil)
			if s := mustOp(t, c, "op").Servers[0]; s.Err != nil {
				t.Fatalf("Server.Err = %v", s.Err)
			}
			before := w.count()
			mustCall(t, c, "op", nil, nil)
			if w.count() != before+1 {
				t.Fatalf("no request reached the server")
			}
			if got := w.last(t).RequestURI; got != tt.want {
				t.Errorf("request target %q, want %q", got, tt.want)
			}
		})
	}
}
