package openapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Values and Input.Body: a form/multipart property whose JSON data is null
// is omitted; a typed nil is a value, never a reader. This also applies to
// the single named property in a positional form-data element. Stage 4 IP4-3
// makes a Part with nil Content omitted too. The wrapper still supplies one
// property; it is not an invalid zero-property positional element.
func TestStage6PositionalFormUndefinedProperty(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"nil", nil},
		{"nil Part pointer", (*openapi.Part)(nil)},
		{"nil string pointer", (*string)(nil)},
		{"nil reader pointer", (*strings.Reader)(nil)},
		{"nil map", map[string]any(nil)},
		{"nil slice", []string(nil)},
		{"nil bytes", []byte(nil)},
		{"JSON null", json.RawMessage("null")},
		{"custom JSON null", nullJSON{}},
		{"Part nil content", openapi.Part{}},
		{"Part pointer nil content", &openapi.Part{}},
		{"Part typed nil content", openapi.Part{Content: (*string)(nil)}},
	} {
		for _, structure := range []string{"map", "struct"} {
			t.Run(tc.name+"/"+structure, func(t *testing.T) {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("undefined positional property panicked: %v", p)
					}
				}()
				var wrapper any = map[string]any{"p": tc.v}
				if structure == "struct" {
					wrapper = struct {
						P any `json:"p"`
					}{tc.v}
				}
				c := editionClient(t, positionalDoc("multipart/form-data", `"prefixEncoding":[{"contentType":"application/xml"},{"contentType":"text/plain"}],"itemEncoding":{"contentType":"text/plain"}`), nil)
				for _, tail := range []bool{false, true} {
					body := []any{wrapper}
					if tail {
						body = append(body, map[string]string{"kept": "after"})
					}
					req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: body})
					_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
					want := 0
					if tail {
						want = 1
					}
					if len(parts) != want {
						t.Fatalf("parts %d want %d", len(parts), want)
					}
					if tail && (parts[0].header.Get("Content-Type") != "text/plain" || string(parts[0].body) != "after" || parts[0].header.Get("Content-Disposition") != formData("kept")) {
						t.Errorf("remaining part %#v", parts[0])
					}
				}
			})
		}
	}
	// Empty strings are defined; non-nil Parts still supply their content.
	for _, tc := range []struct {
		name, want string
		v          any
	}{
		{"empty string", "", ""},
		{"Part", "value", openapi.Part{Content: "value"}},
		{"Part pointer", "pointer", &openapi.Part{Content: "pointer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := editionClient(t, positionalDoc("multipart/form-data", `"itemEncoding":{"contentType":"text/plain"}`), nil)
			req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: []any{map[string]any{"p": tc.v}}})
			_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
			if len(parts) != 1 {
				t.Fatalf("parts %d want 1", len(parts))
			}
			if string(parts[0].body) != tc.want || parts[0].header.Get("Content-Disposition") != formData("p") {
				t.Errorf("part %#v", parts[0])
			}
		})
	}
}
