package openapi_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// keyDoc builds a document whose operation "x" has a request body with the
// content map content.
func keyDoc(content string) string {
	return doc31(`"/x":{"post":{"operationId":"x","requestBody":{"content":` + content + `},"responses":{"200":{"description":"ok"}}}}`)
}

// A content key that is not a valid media type, such as */json, matches no
// type, not even as a range: a body's type that a valid range beside it
// covers is governed by that range and sent, whether Input.MediaType or
// Options.MediaType gives it. client.go, Options.MediaType: it "selects a
// concrete request media type, as Input.MediaType does, for every operation
// whose request body has a Media that it matches by the rules on
// Input.MediaType, a Media whose Err its key causes not counting";
// Input.MediaType: "Its key causes that Err when the content key ...
// declaring the Media is not a valid media type"; Response.Media: "The most
// specific match wins: a concrete type over type/*, type/* over */*, then
// more parameters over fewer; a tie matches none", and "The client parses
// every media type it reads, such as a Content-Type, a content key, ..., by
// RFC 9110's media-type grammar, except that "*" is a type only in */*".
func TestInvalidContentKeyNeverMatchesAsARange(t *testing.T) {
	const content = `{"*/json":{},"*/*":{}}`
	for _, tt := range []struct {
		name, typ string
		in        *openapi.Input
		opts      *openapi.Options
	}{
		{"Input.MediaType text/plain", "text/plain", &openapi.Input{Body: []byte("x"), MediaType: "text/plain"}, nil},
		{"Input.MediaType application/json", "application/json", &openapi.Input{Body: []byte("x"), MediaType: "application/json"}, nil},
		{"Options.MediaType application/json", "application/json", &openapi.Input{Body: []byte("x")}, &openapi.Options{MediaType: "application/json"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := newWire(t, nil)
			c := parseFor(t, w, keyDoc(content), tt.opts)
			req := mustPrepare(t, c, "x", tt.in)
			if req.Media == nil || req.Media.Type != "*/*" {
				t.Errorf("Request.Media = %+v, want the */* Media", req.Media)
			}
			mustCall(t, c, "x", tt.in, nil)
			got := w.only(t)
			if ct := got.Header.Get("Content-Type"); ct != tt.typ || string(got.Body) != "x" {
				t.Errorf("sent Content-Type %q, body %q; want %s and x", ct, got.Body, tt.typ)
			}
		})
	}
}

// Load does not count a Media whose Err its key causes when it checks
// Options.MediaType: a content key that is not a valid media type, or a form
// or multipart key with an invalid or repeated boundary parameter, governs no
// body, so an Options.MediaType that only such a Media would match is
// refused. client.go, Options.MediaType: "Load refuses a type that matches no
// such Media in any request body whose Err is nil", such a Media being one the
// type matches, "a Media whose Err its key causes not counting";
// Input.MediaType: "Its key causes that Err when the content key or Swagger
// 2.0 consumes entry declaring the Media is not a valid media type, or an
// OpenAPI 3.x request body's form or multipart content key has an invalid or
// repeated boundary parameter".
func TestLoadOptionsMediaTypeIgnoresDefectiveKeys(t *testing.T) {
	for _, tt := range []struct{ name, doc, typ string }{
		{"not a media type", keyDoc(`{"*/json":{}}`), "application/json"},
		{"multipart, invalid boundary", keyDoc(`{"multipart/form-data; boundary=\"a~\"":{}}`), `multipart/form-data; boundary="a~"`},
		{"multipart, repeated boundary", keyDoc(`{"multipart/mixed; boundary=a; boundary=a":{}}`), "multipart/mixed; boundary=a"},
		{"form, invalid boundary", keyDoc(`{"application/x-www-form-urlencoded; boundary=\"a~\"":{}}`), `application/x-www-form-urlencoded; boundary="a~"`},
		{"form, repeated boundary", keyDoc(`{"application/x-www-form-urlencoded; boundary=a; boundary=a":{}}`), "application/x-www-form-urlencoded; boundary=a"},
		{"Swagger 2.0 consumes entry", `{"swagger":"2.0","info":{"title":"t","version":"1"},"host":"a.example","paths":{"/x":{"post":{"operationId":"x","consumes":["application/json; x"],"parameters":[{"name":"b","in":"body","schema":{}}],"responses":{"200":{"description":"ok"}}}}}}`, "application/json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := openapi.Parse(context.Background(), []byte(expand(tt.doc, "https://a.example")), testDocURI, &openapi.Options{MediaType: tt.typ})
			if err == nil || c != nil {
				t.Fatalf("Parse = %v, %v; want Options.MediaType %q refused", c, err, tt.typ)
			}
			wantKeys(t, "Settings", asRequestError(t, err).Settings, true, "Options.MediaType")
		})
	}
	// A valid boundary in a key declares its type.
	w := newWire(t, nil)
	const typ = "multipart/mixed; boundary=a"
	c := parseFor(t, w, keyDoc(`{"multipart/mixed; boundary=a":{}}`), &openapi.Options{MediaType: typ})
	mustCall(t, c, "x", &openapi.Input{Body: []byte("--a--\r\n")}, nil)
	if ct := w.only(t).Header.Get("Content-Type"); ct != typ {
		t.Errorf("sent Content-Type %q, want %q", ct, typ)
	}
}

// A request body with an empty content map has no Media, so
// Options.MediaType never applies to it, and a body sent without
// Input.MediaType is refused at Settings "Input.MediaType", the setting
// that helps, pre-encoded or not; the refusal does not name
// Options.MediaType. A pre-encoded body sent with a concrete
// Input.MediaType is sent. doc.go: a call "whose ... request media type is
// ambiguous refuses before dispatch and names the setting needed";
// describe.go, Message.Media: it "is empty for ... a 3.x requestBody with an
// empty content map. In the latter case a raw body with explicit
// Input.MediaType can still be sent, but no structured encoder governs it";
// client.go, Options.MediaType: it applies "for every operation whose
// request body has a Media that it matches by the rules on Input.MediaType, a
// Media whose Err its key causes not counting".
func TestEmptyContentMapRefusalNamesInputMediaType(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, keyDoc(`{}`), nil)
	for _, body := range []any{[]byte("raw"), map[string]any{"a": 1}} {
		resp, err := c.Call(t.Context(), "x", &openapi.Input{Body: body}, nil)
		re := refusedBeforeSending(t, w, resp, err)
		wantKeys(t, "Settings", re.Settings, true, "Input.MediaType")
		for _, text := range []string{re.Error(), fmt.Sprint(re.Settings["Input.MediaType"])} {
			if strings.Contains(text, "Options.MediaType") {
				t.Errorf("body %T: refusal %q names Options.MediaType, which does not apply to an empty content map", body, text)
			}
		}
	}
	mustCall(t, c, "x", &openapi.Input{Body: []byte("raw"), MediaType: "application/x-custom"}, nil)
	if got := w.only(t); got.Header.Get("Content-Type") != "application/x-custom" || string(got.Body) != "raw" {
		t.Errorf("sent Content-Type %q, body %q", got.Header.Get("Content-Type"), got.Body)
	}
}
