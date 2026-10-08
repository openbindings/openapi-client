package openapi_test

import (
	"encoding/json"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// doc.go, Fixed rules, Querystring: "Under application/x-www-form-urlencoded
// its value is an object written by the form-body rules ..., and a value
// whose JSON data is null is undefined ... An undefined value or an empty
// result sends no query, and leaves a required parameter missing"; doc.go,
// Values: "an undefined required one is missing". For a required OpenAPI 3.2
// querystring parameter under application/x-www-form-urlencoded, a typed-nil
// map, a nil *struct, a nil []any, json.RawMessage("null") and an empty
// object are each refused at the parameter's key alone, with the error an
// absent value gives; so is "" under text/plain, an empty result too.
func TestRequiredQuerystringWritingNothingIsMissing(t *testing.T) {
	type object struct{ A string }
	c := editionClient(t, editionDoc("3.2.0", `"/x":{"get":{"parameters":[{"name":"qs","in":"querystring","required":true,"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object"}}}}]}},
		"/t":{"get":{"parameters":[{"name":"qs","in":"querystring","required":true,"content":{"text/plain":{}}}]}}`,
		`"servers":[{"url":"https://api.example.test"}]`), nil)
	refused := func(t *testing.T, key string, v any) error {
		t.Helper()
		req, err := c.Prepare(key, &openapi.Input{Params: map[string]any{"qs": v}})
		if err == nil {
			t.Fatalf("prepared %s", req.HTTP.URL)
		}
		if req != nil {
			t.Errorf("a refused Prepare returned a Request")
		}
		re := asRequestError(t, err)
		wantKeys(t, "Inputs", re.Inputs, true, "qs")
		wantKeys(t, "Settings", re.Settings, true)
		return re.Inputs["qs"]
	}
	for _, tc := range []struct {
		key, name string
		v         any
	}{
		{"GET /x", "typed-nil map", map[string]any(nil)},
		{"GET /x", "nil *struct", (*object)(nil)},
		{"GET /x", "nil []any", []any(nil)},
		{"GET /x", "RawMessage null", json.RawMessage("null")},
		{"GET /x", "empty object", map[string]any{}},
		{"GET /t", "empty string", ""},
	} {
		t.Run(tc.key+"/"+tc.name, func(t *testing.T) {
			missing := refused(t, tc.key, nil)
			if err := refused(t, tc.key, tc.v); err != nil && missing != nil && err.Error() != missing.Error() {
				t.Errorf("Inputs[qs] %q, want %q, as for an absent value", err, missing)
			}
		})
	}
}
