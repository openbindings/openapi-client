package openapi_test

import (
	"net/http"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 1 refusals of features later stages implement. The stage brief
// says each is refused, before anything is sent, until its stage lands;
// these tests assert only that (not the wording), and each later stage
// replaces the cases it implements with tests of the feature itself.

// Stage brief, Loading: "Any other edition, YAML (first significant byte not
// '{') ... is a refusal with a clear error (YAML is stage 5, other editions
// stage 6)."
func TestStage1RefusesOtherDocuments(t *testing.T) {
	docs := map[string]string{
		"YAML":        "openapi: 3.1.0\ninfo: {title: t, version: \"1\"}\npaths: {}\n",
		"Swagger 2.0": `{"swagger":"2.0","info":{"title":"t","version":"1"},"paths":{}}`,
		"OpenAPI 3.0": `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{}}`,
		"OpenAPI 3.2": `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"paths":{}}`,
	}
	for name, doc := range docs {
		c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, nil)
		if err == nil || c != nil {
			t.Errorf("%s: Parse = %v, %v; want a refusal in stage 1", name, c, err)
		}
	}
}

// Stage brief, Calls and responses: "FollowAll is stage 3: refuse at Load
// with a not-implemented error."
func TestStage1RefusesFollowAll(t *testing.T) {
	c, err := openapi.Parse(t.Context(), []byte(expand(doc31(`"/a":{"get":{}}`), "https://api.example.test")), testDocURI,
		&openapi.Options{Redirects: openapi.FollowAll})
	if err == nil || c != nil {
		t.Errorf("Parse with FollowAll = %v, %v; want a refusal in stage 1", c, err)
	}
}

// Stage brief, Requests: "Other styles, cookies, content parameters,
// allowReserved, querystring and ParamWriters are stage 2: refuse them with
// a RequestError". Each case supplies a value for the parameter, so the
// call uses the feature.
func TestStage1RefusesParameterFeatures(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/label/{p}":{"get":{"operationId":"label","parameters":[{"name":"p","in":"path","required":true,"style":"label","schema":{}}]}},
		"/matrix/{p}":{"get":{"operationId":"matrix","parameters":[{"name":"p","in":"path","required":true,"style":"matrix","schema":{}}]}},
		"/deep":{"get":{"operationId":"deep","parameters":[{"name":"p","in":"query","style":"deepObject","explode":true,"schema":{}}]}},
		"/space":{"get":{"operationId":"space","parameters":[{"name":"p","in":"query","style":"spaceDelimited","explode":false,"schema":{}}]}},
		"/pipe":{"get":{"operationId":"pipe","parameters":[{"name":"p","in":"query","style":"pipeDelimited","explode":false,"schema":{}}]}},
		"/cookie":{"get":{"operationId":"cookie","parameters":[{"name":"p","in":"cookie","schema":{}}]}},
		"/content":{"get":{"operationId":"content","parameters":[{"name":"p","in":"query","content":{"application/json":{}}}]}},
		"/reserved":{"get":{"operationId":"reserved","parameters":[{"name":"p","in":"query","allowReserved":true,"schema":{}}]}},
		"/writer":{"get":{"operationId":"writer","parameters":[{"name":"p","in":"query","schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	tests := []struct {
		key string
		in  *openapi.Input
	}{
		{"label", &openapi.Input{Params: map[string]any{"p": "x"}}},
		{"matrix", &openapi.Input{Params: map[string]any{"p": "x"}}},
		{"deep", &openapi.Input{Params: map[string]any{"p": map[string]string{"a": "b"}}}},
		{"space", &openapi.Input{Params: map[string]any{"p": []string{"a", "b"}}}},
		{"pipe", &openapi.Input{Params: map[string]any{"p": []string{"a", "b"}}}},
		{"cookie", &openapi.Input{Params: map[string]any{"p": "x"}}},
		{"content", &openapi.Input{Params: map[string]any{"p": map[string]int{"a": 1}}}},
		{"reserved", &openapi.Input{Params: map[string]any{"p": "a/b"}}},
		{"writer", &openapi.Input{ParamWriters: map[string]func(*http.Request) error{
			"p": func(r *http.Request) error { r.URL.RawQuery = "p=x"; return nil },
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			resp, err := c.Call(t.Context(), tt.key, tt.in, nil)
			refusedBeforeSending(t, w, resp, err)
		})
	}
}

// Stage brief, Requests: "Form, multipart, text, XML and sequential bodies
// are stage 4: refuse with a not-implemented Err." A structured value under
// those types is refused; a pre-encoded body is not (see
// TestPreEncodedBody).
func TestStage1RefusesStructuredBodies(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/form":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{}}}}},
		"/multipart":{"post":{"operationId":"multipart","requestBody":{"content":{"multipart/form-data":{}}}}},
		"/text":{"post":{"operationId":"text","requestBody":{"content":{"text/plain":{}}}}},
		"/xml":{"post":{"operationId":"xml","requestBody":{"content":{"application/xml":{}}}}},
		"/jsonl":{"post":{"operationId":"jsonl","requestBody":{"content":{"application/jsonl":{}}}}}`)
	c := parseFor(t, w, doc, nil)
	tests := []struct {
		key  string
		body any
	}{
		{"form", map[string]string{"a": "b"}},
		{"multipart", map[string]any{"title": "Q3"}},
		{"text", "hello"},
		{"xml", xmlPet{Name: "Rex"}},
		{"jsonl", []Pet{{Name: "Rex"}}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			resp, err := c.Call(t.Context(), tt.key, &openapi.Input{Body: tt.body}, nil)
			refusedBeforeSending(t, w, resp, err)
		})
	}
}
