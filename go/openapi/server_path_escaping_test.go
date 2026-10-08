package openapi_test

import (
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// doc.go, Fixed rules, URL: "In the request target, the Paths key and the
// path of the server URL or Options.BaseURL keep each %XX triple as written
// and write as %XX, in uppercase hex, any other byte RFC 3986 does not allow
// in a path". A server path holding both a triple and a byte to encode keeps
// the triple, so an encoded "/" never becomes a separator, whether the server
// URL is absolute, relative to the document's URI (RFC 3986 section 5.2), or
// Options.BaseURL. The Paths key, the rule's other half, is the control.
func TestServerPathKeepsTriples(t *testing.T) {
	w := newWire(t, nil)
	const ops = `"/p/{id}":{"get":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}`
	for _, tc := range []struct {
		name, path, want string
	}{
		{"encoded slash and space", "/a%2Fb c", "/a%2Fb%20c"},
		{"triple kept as written", "/caf\u00e9/a%2fb", "/caf%C3%A9/a%2fb"},
		{"sub-delims kept", "/a%3Bb%7C!|", "/a%3Bb%7C!%7C"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, server := range []struct{ how, url string }{
				{"absolute", w.URL + tc.path},
				{"relative", tc.path},
			} {
				doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":` + jsonString(server.url) + `}],"paths":{` + ops + `}}`
				c := parseFor(t, w, doc, nil)
				if got := callWithID(t, w, c).RequestURI; got != tc.want+"/p/x" {
					t.Errorf("%s server URL %q: request target %q, want %q", server.how, server.url, got, tc.want+"/p/x")
				}
			}
			doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{` + ops + `}}`
			c := parseFor(t, w, doc, &openapi.Options{BaseURL: w.URL + tc.path})
			if got := callWithID(t, w, c).RequestURI; got != tc.want+"/p/x" {
				t.Errorf("Options.BaseURL %q: request target %q, want %q", w.URL+tc.path, got, tc.want+"/p/x")
			}
			key := jsonString("/k" + tc.path + "/{id}")
			doc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{` + key + `:{"get":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}}}`
			c = parseFor(t, w, doc, nil)
			if got := callWithID(t, w, c).RequestURI; got != "/k"+tc.want+"/x" {
				t.Errorf("Paths key: request target %q, want %q", got, "/k"+tc.want+"/x")
			}
		})
	}
}

// callWithID calls "op" with the path parameter id "x" and returns the
// request w received.
func callWithID(t *testing.T, w *wire, c *openapi.Client) rec {
	t.Helper()
	mustCall(t, c, "op", &openapi.Input{Params: map[string]any{"id": "x"}}, nil)
	return w.last(t)
}

// doc.go, Fixed rules, URL: "a "%" that begins no triple is such a byte in
// the Paths key, and makes a server URL unusable and Options.BaseURL
// refused".
func TestLonePercentInPaths(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{
		"/100%/{id}":{"get":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}}}`, nil)
	if got := callWithID(t, w, c).RequestURI; got != "/100%25/x" {
		t.Errorf("Paths key: request target %q, want /100%%25/x", got)
	}
	c = parseFor(t, w, `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@/100%"}],"paths":{
		"/p":{"get":{"operationId":"op"}}}}`, nil)
	if s := server(t, mustOp(t, c, "op"), 0); s.Err == nil {
		t.Errorf("server URL %q: no Server.Err", s.URL)
	}
	err := parseErr(t, `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/p":{"get":{"operationId":"op"}}}}`, w.URL, &openapi.Options{BaseURL: w.URL + "/100%"})
	wantKeys(t, "Settings", asRequestError(t, err).Settings, true, "Options.BaseURL")
}
