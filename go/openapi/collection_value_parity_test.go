package openapi_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

type namedStrings []string
type namedString string
type namedObject map[string]any
type marshalerStrings []string

func (marshalerStrings) MarshalJSON() ([]byte, error) {
	return []byte(`["custom value",""]`), nil
}

// Values makes encoding/json authoritative for collection data: where the
// client applies encoding/json, its output and behavior govern. Keep the
// caller's invalid UTF-8 intact until Prepare: the edition fuzz oracle
// currently normalizes its arguments before invoking the client. Named types
// and custom marshalers must retain that same JSON-data rule.
func TestSwaggerCollectionValueParity(t *testing.T) {
	values := []struct {
		name string
		v    any
	}{
		{"native invalid UTF8", []string{"a\xff\xfeb", "\xc3", ""}},
		{"named slice", namedStrings{"a\xffb", "x y"}},
		{"named elements", []namedString{"a\xffb", "x y"}},
		{"custom slice", marshalerStrings{"ignored"}},
		{"custom elements", []shout{"first", "second"}},
		{"pointer marshaler elements", []ptrJSON{"first", "second"}},
	}
	for _, cf := range []string{"csv", "ssv", "tsv", "pipes", "multi"} {
		c := editionClient(t, editionDoc("2.0", fmt.Sprintf(`"/x":{"get":{"parameters":[{"name":"p","in":"query","type":"array","items":{"type":"string"},"collectionFormat":%q}]}}`, cf)), nil)
		for _, tc := range values {
			t.Run(cf+"/"+tc.name, func(t *testing.T) {
				encoded, err := json.Marshal(tc.v)
				if err != nil {
					t.Fatal(err)
				}
				var data []string
				if err := json.Unmarshal(encoded, &data); err != nil {
					t.Fatal(err)
				}
				for i := range data {
					data[i] = pctName(data[i])
				}
				separator := map[string]string{"csv": ",", "ssv": "%20", "tsv": "%09", "pipes": "%7C", "multi": "&p="}[cf]
				want := "p=" + strings.Join(data, separator)
				got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"p": tc.v}}).HTTP.URL.RawQuery
				if got != want {
					t.Errorf("query %q want %q (JSON %s)", got, want, encoded)
				}
			})
		}
	}
}

// Querystring uses form-body rules, including byte-preserving non-JSON
// strings, undefined omission, JSON marshaling, and Encoding. Mixed fields
// must produce each pair once, in sorted map-key order, even when only some
// fields can be handled by a specialized encoder.
func TestQuerystringFormValueParity(t *testing.T) {
	base := `"schema":{"type":"object","properties":{"a":{"type":"string"},"z":{"type":"integer"},"p":{"type":"array","items":{"type":"string"}}}}`
	for _, tc := range []struct {
		name, media, want string
		value             any
		options           *openapi.Options
	}{
		{"native raw strings", base, "a=%FF&p=%C3&p=", map[string]any{"a": "\xff", "p": []string{"\xc3", ""}}, nil},
		{"native omission", base, "a=", map[string]any{"a": "", "p": []string{}, "z": nil}, nil},
		{"named map", base, "a=first&p=x+y&p=last", namedObject{"a": "first", "p": []string{"x y", "last"}}, nil},
		{"named scalar", base, "a=first&p=last", map[string]any{"a": namedString("first"), "p": namedStrings{"last"}}, nil},
		{"mixed number", base, "a=first&z=42", map[string]any{"a": "first", "z": 42}, nil},
		{"mixed bytes", base, "a=first&z=%FF", map[string]any{"a": "first", "z": []byte{0xff}}, nil},
		{"mixed marshaler", base, "a=first&p=custom+value&p=", map[string]any{"a": "first", "p": marshalerStrings{"ignored"}}, nil},
		{"mixed custom scalar", base, "a=first&z=LAST", map[string]any{"a": "first", "z": shout("last")}, nil},
		{"mixed typed nil", base, "a=first", map[string]any{"a": "first", "z": (*string)(nil)}, nil},
		{"styled field", base + `,"encoding":{"p":{"style":"form","explode":false}}`, "a=first&p=x%20y,last", map[string]any{"a": "first", "p": []string{"x y", "last"}}, nil},
		{"JSON field", base + `,"encoding":{"z":{"contentType":"application/json"}}`, "a=first&z=%22last%22", map[string]any{"a": "first", "z": "last"}, nil},
		{"field codec", base, "a=TXT%28first%29&z=TXT%28last%29", map[string]any{"a": "first", "z": "last"}, &openapi.Options{Codecs: map[string]openapi.Codec{"text/plain": tagCodec{tag: "TXT"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"whole","in":"querystring","content":{"application/x-www-form-urlencoded":{`+tc.media+`}}}]}}`), tc.options)
			got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"whole": tc.value}}).HTTP.URL.RawQuery
			if got != tc.want {
				t.Errorf("query %q want %q", got, tc.want)
			}
		})
	}
}

// A Swagger 2.0 operation reads only its own edition's fields, at admission
// too: Loader refuses an Options.MediaType that no operation declares. An
// inactive Swagger requestBody cannot supply a match absent from the
// operation's actual consumes/body declarations.
func TestSwaggerRequestBodyFieldNoMediaPreference(t *testing.T) {
	for _, hasBody := range []bool{false, true} {
		t.Run(fmt.Sprint(hasBody), func(t *testing.T) {
			body := ""
			if hasBody {
				body = `"consumes":["application/json"],"parameters":[{"name":"payload","in":"body","schema":{"type":"object"}}],`
			}
			doc := editionDoc("2.0", `"/x":{"post":{`+body+`"requestBody":{"content":{"text/plain":{"schema":{"type":"string"}}}}}}`)
			c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{MediaType: "text/plain"})
			if c != nil {
				t.Error("inactive requestBody admitted a media preference")
			}
			wantKeys(t, "Settings", asRequestError(t, err).Settings, true, "Options.MediaType")
			if hasBody {
				c = editionClient(t, doc, &openapi.Options{MediaType: "application/json"})
				mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]int{"n": 1}})
			}
		})
	}
}

// Values requires serialization to stop at the 1 MiB request-target limit.
// Reuse TestRequestSizeLimit's established 16 MiB refusal allocation ceiling,
// with 32 MiB of would-be output from a prebuilt, physically small value.
// This measures allocation only, not time: it distinguishes bounded rejection
// from emitting the entire repeated field before noticing the limit.
func TestQuerystringStopsAtTargetLimit(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"whole","in":"querystring","content":{"application/x-www-form-urlencoded":{}}}]}}`), nil)
	mustOp(t, c, "GET /x")
	values := make([]string, 4096)
	value := strings.Repeat("x", 8192)
	for i := range values {
		values[i] = value
	}
	in := &openapi.Input{Params: map[string]any{"whole": map[string]any{"p": values}}}
	var req *openapi.Request
	var err error
	allocated := allocatedBy(func() { req, err = c.Prepare("GET /x", in) })
	if req != nil || err == nil {
		t.Fatal("prepared a request beyond the target limit")
	}
	wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "whole")
	if allocated > 16<<20 {
		t.Errorf("refusal allocated %d bytes; existing request-limit ceiling is 16 MiB", allocated)
	}
}

// The same Values stop rule applies within a collection element, not only
// between elements. Build the 32 MiB scalar before measuring Prepare and
// reuse TestRequestSizeLimit's 16 MiB refusal allocation ceiling.
func TestCollectionStopsWithinOversizedElement(t *testing.T) {
	c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"p","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"csv"}]}}`), nil)
	mustOp(t, c, "GET /x")
	in := &openapi.Input{Params: map[string]any{"p": []string{strings.Repeat("x", 32<<20)}}}
	var req *openapi.Request
	var err error
	allocated := allocatedBy(func() { req, err = c.Prepare("GET /x", in) })
	if req != nil || err == nil {
		t.Fatal("prepared a request beyond the target limit")
	}
	wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "p")
	if allocated > 16<<20 {
		t.Errorf("refusal allocated %d bytes; existing request-limit ceiling is 16 MiB", allocated)
	}
}
