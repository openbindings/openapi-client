package openapi_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// doc.go, Values: "A form or multipart property or array item, or a positional
// part ..., whose JSON data is null is omitted, whatever its serialization or
// media type, a form, multipart or sequential one included"; client.go,
// Part.Content: "A nil Content, or one whose JSON data is null, omits the
// part, as a null property is omitted, whatever the part's media type". JSON
// null from a json.RawMessage, from a MarshalJSON, and as a Part's Content,
// is omitted under a field or part whose own type is
// application/x-www-form-urlencoded, multipart/mixed or application/jsonl, as
// a typed nil is, in a multipart/form-data and in an
// application/x-www-form-urlencoded body, and as an array property's item;
// the other field is sent alone.
func TestJSONNullOmittedWhateverTheFieldType(t *testing.T) {
	doc := editionDoc("3.2.0", `"/x":{"post":{"requestBody":{"content":{
		"multipart/form-data":{"schema":{"type":"object","properties":{"f":{},"m":{},"l":{},"a":{"type":"array","items":{}},"z":{"type":"string"}}},
			"encoding":{"f":{"contentType":"application/x-www-form-urlencoded"},"m":{"contentType":"multipart/mixed"},"l":{"contentType":"application/jsonl"},"a":{"contentType":"application/jsonl"}}},
		"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"f":{},"m":{},"l":{},"a":{"type":"array","items":{}},"z":{"type":"string"}}},
			"encoding":{"f":{"contentType":"application/x-www-form-urlencoded"},"m":{"contentType":"multipart/mixed"},"l":{"contentType":"application/jsonl"},"a":{"contentType":"application/jsonl"}}}}}}}`)
	c := editionClient(t, doc, nil)
	nulls := []struct {
		name string
		v    any
	}{
		{"RawMessage", json.RawMessage("null")},
		{"MarshalJSON", nullJSON{}},
		{"Part", openapi.Part{Content: json.RawMessage("null")}},
		{"typed nil", map[string]any(nil)},
	}
	for _, media := range []string{"multipart/form-data", "application/x-www-form-urlencoded"} {
		for _, field := range []string{"f", "m", "l", "a"} {
			for _, n := range nulls {
				v := n.v
				if field == "a" {
					v = []any{n.v}
				}
				t.Run(fmt.Sprintf("%s/%s/%s", media, field, n.name), func(t *testing.T) {
					req, err := c.Prepare("POST /x", &openapi.Input{MediaType: media, Body: map[string]any{field: v, "z": "kept"}})
					if err != nil {
						t.Fatalf("refused: %v", err)
					}
					body := editionBody(t, req)
					if media == "application/x-www-form-urlencoded" {
						if string(body) != "z=kept" {
							t.Errorf("body %q, want z=kept", body)
						}
						return
					}
					_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), body)
					checkParts(t, parts, []wantPart{{disposition: formData("z"), ctype: "text/plain", content: "kept"}})
				})
			}
		}
	}
}

// doc.go, Values and client.go, Part.Content, as for
// TestJSONNullOmittedWhateverTheFieldType, for an OpenAPI 3.2 positional
// multipart body: an element whose JSON data is null, or a Part whose Content
// is, leaves no part even when its position's type is application/jsonl or
// multipart/mixed, and the next element keeps its own position's Encoding.
func TestJSONNullPositionalPartOmitted(t *testing.T) {
	c := editionClient(t, positionalDoc("multipart/mixed", `"prefixEncoding":[{"contentType":"application/jsonl"},{"contentType":"multipart/mixed"},{"contentType":"text/plain"}]`), nil)
	for _, n := range []any{json.RawMessage("null"), nullJSON{}, openapi.Part{Content: json.RawMessage("null")}} {
		t.Run(fmt.Sprintf("%T", n), func(t *testing.T) {
			req, err := c.Prepare("POST /x", &openapi.Input{Body: []any{n, n, "after"}})
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
			checkParts(t, parts, []wantPart{{ctype: "text/plain", content: "after"}})
		})
	}
}

// doc.go, Fixed rules, Querystring: "Under
// application/x-www-form-urlencoded its value is an object written by the
// form-body rules ..., and a value whose JSON data is null is undefined ...
// An undefined value or an empty result sends no query ... A query credential
// follows, joined by "&" to any query." A typed-nil map, a nil *struct, a nil
// []any and json.RawMessage("null") send no query and are not refused; a
// query credential then forms the query alone.
func TestQuerystringNullSendsNoQuery(t *testing.T) {
	type object struct{ A string }
	w := newWire(t, nil)
	doc := editionDoc("3.2.0", `"/x":{"get":{"parameters":[{"name":"qs","in":"querystring","content":{"application/x-www-form-urlencoded":{"schema":{"type":"object"}}}}]}},
		"/k":{"get":{"parameters":[{"name":"qs","in":"querystring","content":{"application/x-www-form-urlencoded":{"schema":{"type":"object"}}}}],"security":[{"key":[]}]}}`,
		`"servers":[{"url":"@BASE@"}]`,
		`"components":{"securitySchemes":{"key":{"type":"apiKey","in":"query","name":"api_key"}}}`)
	c := parseFor(t, w, doc, &openapi.Options{Credentials: map[string]openapi.Credential{"key": openapi.Secret("K")}})
	for _, v := range []any{map[string]any(nil), (*object)(nil), []any(nil), json.RawMessage("null")} {
		t.Run(fmt.Sprintf("%T", v), func(t *testing.T) {
			for key, want := range map[string]string{"GET /x": "/x", "GET /k": "/k?api_key=K"} {
				before := w.count()
				_, err := c.Call(t.Context(), key, &openapi.Input{Params: map[string]any{"qs": v}}, nil)
				if err != nil {
					t.Fatalf("%s: %v", key, err)
				}
				if w.count() != before+1 {
					t.Fatalf("%s: nothing sent", key)
				}
				if got := w.last(t).RequestURI; got != want {
					t.Errorf("%s: request target %q, want %q", key, got, want)
				}
			}
		})
	}
}
