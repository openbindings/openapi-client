package openapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The ordinary HTTP flows in every edition, including real transport
// header/body handling, credential placement and decoding.
func TestEditionsHTTPFlows(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			w := newWire(t, jsonAnswer(200, `{"ok":true}`))
			doc := editionPost(version, "application/json", `{"type":"object"}`)
			security := `"components":{"securitySchemes":{"key":{"type":"apiKey","in":"header","name":"X-Key"}}}`
			if version == "2.0" {
				security = `"securityDefinitions":{"key":{"type":"apiKey","in":"header","name":"X-Key"}}`
			}
			doc = strings.TrimSuffix(doc, "}") + `,"security":[{"key":[]}],` + security + `}`
			c := parseFor(t, w, doc, &openapi.Options{Credentials: map[string]openapi.Credential{"key": openapi.Secret("value")}})
			var out struct {
				OK bool `json:"ok"`
			}
			mustCall(t, c, "POST /x", &openapi.Input{Body: map[string]int{"n": 1}}, &out)
			r := w.only(t)
			if !out.OK || r.Method != "POST" || r.RequestURI != "/x" || r.Header.Get("X-Key") != "value" || r.Header.Get("Content-Type") != "application/json" || !bytes.Equal(trimNL(r.Body), []byte(`{"n":1}`)) {
				t.Errorf("response %+v wire %+v", out, r)
			}
		})
	}
}

// Runnable counterpart of Example_environmentsListed: named 3.2 servers
// select distinct environments, with With preserving descriptor identities.
func TestEditionsEnvironmentExample(t *testing.T) {
	a, b := newWire(t, nil), newWire(t, nil)
	doc := editionDoc("3.2.1", `"/x":{"get":{}}`, fmt.Sprintf(`"servers":[{"url":%q,"name":"production"},{"url":%q,"name":"sandbox"}]`, a.URL, b.URL))
	c := editionClient(t, doc, &openapi.Options{Server: "production"})
	d := c.With(func(o *openapi.Options) { o.Server = "sandbox" })
	mustCall(t, c, "GET /x", nil, nil)
	mustCall(t, d, "GET /x", nil, nil)
	a.only(t)
	b.only(t)
}

// Schema's handle rules (describe.go, Schema: "A handle is the schema where
// it is used"): 2.0/3.0 follow root $ref while 3.1/3.2 retain the use site.
// Swagger nonbody parameters retain all schema fields, without Parameter
// Object fields leaking into their Raw representation.
func TestEditionsSchemaHandles(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			ref := "#/components/schemas/S"
			extra := `"components":{"schemas":{"S":{"type":"string","minLength":2}}}`
			if version == "2.0" {
				ref = "#/definitions/S"
				extra = `"definitions":{"S":{"type":"string","minLength":2}}`
			}
			doc := editionPost(version, "application/json", `{"$ref":"`+ref+`","description":"use"}`)
			doc = strings.TrimSuffix(doc, "}") + "," + extra + "}"
			c := editionClient(t, doc, nil)
			s := reqMedia(t, mustOp(t, c, "POST /x"), 0).Schema
			var raw map[string]any
			if err := json.Unmarshal(s.Raw(), &raw); err != nil {
				t.Fatal(err)
			}
			if version == "2.0" || version == "3.0.4" {
				if s.Source() != testDocURI+ref || raw["type"] != "string" || raw["$ref"] != nil || s.Dialect() != "" {
					t.Errorf("legacy schema %s source %s dialect %s", s.Raw(), s.Source(), s.Dialect())
				}
			} else {
				want := "https://spec.openapis.org/oas/3.1/dialect/base"
				if version == "3.2.1" {
					want = "https://spec.openapis.org/oas/3.2/dialect/2025-09-17"
				}
				if raw["$ref"] != ref || s.Source() == testDocURI+ref || s.Dialect() != want {
					t.Errorf("schema %s source %s dialect %s", s.Raw(), s.Source(), s.Dialect())
				}
			}
		})
	}
	c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"p","in":"query","description":"parameter","required":true,"type":"array","items":{"type":"integer","minimum":1},"minItems":1,"maxItems":9,"uniqueItems":true,"default":[2],"enum":[[2],[3]]}]}}`), nil)
	p := param(t, mustOp(t, c, "GET /x"), 0)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(p.Schema.Raw(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "items", "minItems", "maxItems", "uniqueItems", "default", "enum"} {
		if raw[key] == nil {
			t.Errorf("missing schema field %s", key)
		}
	}
	for _, key := range []string{"name", "in", "required", "collectionFormat"} {
		if raw[key] != nil {
			t.Errorf("non-schema field %s in %s", key, p.Schema.Raw())
		}
	}
	if p.Schema.Source() != p.Source || p.Schema.Dialect() != "" {
		t.Errorf("synthetic provenance %s / %s", p.Schema.Source(), p.Source)
	}
}

func TestEditionsResponseHeaderDescriptions(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, editionDoc(version, `"/x":{"get":{"responses":{"200":{"description":"ok","headers":{"Content-Type":{"type":"string","schema":{"type":"string"}},"X-Count":{"type":"integer","schema":{"type":"integer"}}}}}}}`), nil)
			h := response(t, mustOp(t, c, "GET /x"), 0).Headers
			want := 1
			if version == "2.0" {
				want = 2
			}
			if len(h) != want {
				t.Errorf("headers %+v", h)
			}
		})
	}
}

// Undefined/malformed combinations must report their own parameter rather
// than an unknown input: Param.Err is visible before a value is supplied;
// a writer can bypass a supported-key serializer defect (client.go).
func TestEditionsParameterDefects(t *testing.T) {
	for _, tc := range []struct{ version, decl, path string }{
		{"2.0", `{"name":"p","in":"header","type":"array","collectionFormat":"multi","items":{"type":"string"}}`, "/x"},
		{"2.0", `{"name":"p","in":"path","required":true,"type":"array","collectionFormat":"multi","items":{"type":"string"}}`, "/x/{p}"},
		{"2.0", `{"name":"p","in":"query","type":"array","collectionFormat":"unknown","items":{"type":"string"}}`, "/x"},
		{"3.1.2", `{"name":"p","in":"cookie","style":"cookie","schema":{"type":"string"}}`, "/x"},
	} {
		t.Run(tc.version+tc.decl, func(t *testing.T) {
			c := editionClient(t, editionDoc(tc.version, fmt.Sprintf(`%q:{"get":{"parameters":[%s]}}`, tc.path, tc.decl)), nil)
			p := param(t, mustOp(t, c, "GET "+tc.path), 0)
			if p.Err == nil {
				t.Error("missing Param.Err")
			}
			_, err := c.Prepare("GET "+tc.path, &openapi.Input{Params: map[string]any{"p": []string{"a", "b"}}})
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, "p")
		})
	}
}

// Querystring has the full form-body Encoding rule and codec seam, and
// uniqueness applies after path/operation parameter merging (OAS 3.2.1 4.12).
func TestEditionsQuerystringEncodingAndInheritance(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"whole","in":"querystring","content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"p":{"type":"array","items":{"type":"string"}}}},"encoding":{"p":{"style":"form","explode":false}}}}}]}}`), nil)
	if got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"whole": map[string]any{"p": []string{"a b", "c"}}}}).HTTP.URL.RawQuery; got != "p=a%20b,c" {
		t.Errorf("query %q", got)
	}
	c = editionClient(t, editionDoc("3.2.1", `"/x":{"parameters":[{"name":"p","in":"query"}],"get":{"parameters":[{"name":"whole","in":"querystring","content":{"text/plain":{}}}]}}`), nil)
	if _, err := c.Prepare("GET /x", &openapi.Input{Params: map[string]any{"p": "x", "whole": "y"}}); err == nil {
		t.Error("path query coexists with querystring")
	}
}

// Positional default encoding comes from schema prefixItems/items even with
// no Encoding fields. One nested multipart level is required by Input.Body.
func TestEditionsPositionalDefaultsAndNested(t *testing.T) {
	doc := editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array","prefixItems":[{"type":"string"},{"type":"object"}],"items":{"type":"integer"}}}}}}}`)
	c := editionClient(t, doc, nil)
	req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: []any{"hello", map[string]int{"n": 1}, 2}})
	_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
	if len(parts) != 3 {
		t.Fatalf("parts %d", len(parts))
	}
	for i, want := range []string{"text/plain", "application/json", "text/plain"} {
		if got := parts[i].header.Get("Content-Type"); got != want {
			t.Errorf("part %d type %q want %q", i, got, want)
		}
	}
	c = editionClient(t, positionalDoc("multipart/mixed", `"prefixEncoding":[{"contentType":"multipart/mixed","prefixEncoding":[{"contentType":"text/plain"}],"itemEncoding":{"contentType":"application/json"}}]`), nil)
	req = mustPrepare(t, c, "POST /x", &openapi.Input{Body: []any{[]any{"nested", map[string]int{"n": 2}}}})
	_, _, parts = readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
	if len(parts) != 1 {
		t.Fatalf("outer parts %d", len(parts))
	}
	_, _, inner := readMultipart(t, parts[0].header.Get("Content-Type"), parts[0].body)
	if len(inner) != 2 || string(inner[0].body) != "nested" || string(trimNL(inner[1].body)) != `{"n":2}` {
		t.Errorf("inner %#v", inner)
	}
}

// Positional Example_filesPositionalSend: a Part can pick a PDF media type,
// its caller-owned reader stays open, and a raw byte slice is an octet part.
func TestEditionsFilesPositionalExample(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, positionalDoc("multipart/mixed", `"prefixEncoding":[{"contentType":"application/json"},{"contentType":"application/pdf"}],"itemEncoding":{"contentType":"application/octet-stream"}`), nil)
	f := &editionReadCloser{Reader: strings.NewReader("PDF")}
	mustCall(t, c, "POST /x", &openapi.Input{Body: []any{map[string]string{"title": "Q3"}, openapi.Part{Content: f, MediaType: "application/pdf"}, []byte("PNG")}}, nil)
	if f.closed {
		t.Error("caller reader closed")
	}
	r := w.only(t)
	_, _, parts := readMultipart(t, r.Header.Get("Content-Type"), r.Body)
	if len(parts) != 3 || string(parts[1].body) != "PDF" || string(parts[2].body) != "PNG" {
		t.Errorf("parts %#v", parts)
	}
}

type editionReadCloser struct {
	io.Reader
	closed bool
}

func (r *editionReadCloser) Close() error { r.closed = true; return nil }

// Custom codecs still govern body bytes under every edition's normalized
// media descriptor (Options.Codecs). Encoding details stay caller-owned.
func TestEditionsCustomCodec(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, editionPost(version, "application/x-edition", `{}`), &openapi.Options{Codecs: map[string]openapi.Codec{"application/x-edition": editionCodec{}}})
			if got := string(editionBody(t, mustPrepare(t, c, "POST /x", &openapi.Input{Body: "value"}))); got != "encoded:value" {
				t.Errorf("codec %q", got)
			}
		})
	}
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"whole","in":"querystring","content":{"application/x-edition":{}}}]}}`), &openapi.Options{Codecs: map[string]openapi.Codec{"application/x-edition": editionCodec{}}})
	if got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"whole": "a/b"}}).HTTP.URL.RawQuery; got != "encoded%3Aa%2Fb" {
		t.Errorf("codec query %q", got)
	}
}

type editionCodec struct{}

func (editionCodec) Encode(w io.Writer, v any) error {
	_, err := fmt.Fprint(w, "encoded:", v)
	return err
}
func (editionCodec) Decode(r io.Reader, v any) error {
	b, err := io.ReadAll(r)
	if err == nil {
		*(v.(*string)) = strings.TrimPrefix(string(b), "encoded:")
	}
	return err
}
