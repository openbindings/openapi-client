package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The Swagger 2.0 Operation Object (section 6.4.7) takes its body from
// parameters and consumes. A requestBody-shaped unknown field cannot replace
// that body or impose requiredness, even when it appears before the real
// parameters.
func TestSwaggerRequestBodyFieldIgnored(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(fmt.Sprint(first), func(t *testing.T) {
			body := `"consumes":["application/json"],"parameters":[{"name":"payload","in":"body","schema":{"type":"object"}}]`
			inactive := `"requestBody":{"required":true,"content":{"text/plain":{"schema":{"type":"string"}}}}`
			op := body + "," + inactive
			if first {
				op = inactive + "," + body
			}
			c := editionClient(t, editionDoc("2.0", `"/x":{"post":{`+op+`}}`), nil)
			desc := mustOp(t, c, "POST /x")
			if desc.Body == nil || desc.Body.Required || len(desc.Body.Media) != 1 || desc.Body.Media[0].Type != "application/json" || desc.Body.Source != testDocURI+"#/paths/~1x/post/parameters/0" {
				t.Fatalf("body %+v", desc.Body)
			}
			mustPrepare(t, c, "POST /x", nil)
			if got := editionBody(t, mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]int{"n": 1}})); !bytes.Equal(trimNL(got), []byte(`{"n":1}`)) {
				t.Errorf("body %s", got)
			}
		})
	}
	t.Run("no Swagger body declaration", func(t *testing.T) {
		c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"string"}}}}}}`), nil)
		if body := mustOp(t, c, "POST /x").Body; body != nil {
			t.Errorf("inactive requestBody produced Body: %+v", body)
		}
		mustPrepare(t, c, "POST /x", nil)
	})
}

// Loader explicitly chooses component names for mapping/defaultMapping
// strings that can name a component. A dot is permitted in a component name;
// ./other.json is a URI, whereas other.json remains a name whether declared
// or missing. Neither mapping field may fetch a name-shaped string.
func TestDiscriminatorMappingNamesNotFetched(t *testing.T) {
	for _, field := range []string{"mapping", "defaultMapping"} {
		for _, name := range []string{"Pet", "other.json", "missing"} {
			t.Run(field+"/"+name, func(t *testing.T) {
				var mu sync.Mutex
				var hits []string
				l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
					mu.Lock()
					hits = append(hits, u)
					mu.Unlock()
					return io.NopCloser(strings.NewReader(`{"type":"object"}`)), "", nil
				}}
				value := fmt.Sprintf("%q", name)
				if field == "mapping" {
					value = `{"pet":` + value + `}`
				}
				doc := editionDoc("3.2.1", `"/x":{"get":{}}`, `"components":{"schemas":{"Pet":{"type":"object"},"other.json":{"type":"object"},"S":{"discriminator":{"propertyName":"kind","`+field+`":`+value+`}}}}`)
				c, err := l.Parse(t.Context(), []byte(doc), testDocURI, nil)
				if err != nil {
					t.Fatal(err)
				}
				mustPrepare(t, c, "GET /x", nil)
				if len(hits) != 0 {
					t.Errorf("component name fetched: %q", hits)
				}
			})
		}
	}
}

// doc.go Configuration derives positional part types from itemSchema or
// prefixItems/items after references. The raw schema's reference must not
// erase the type that controls supported structured multipart values.
func TestPositionalSchemaDerivedTypes(t *testing.T) {
	for _, tc := range []struct {
		name, media, extra string
		body               []any
		types, bodies      []string
	}{
		{"itemSchema", `{"itemSchema":{"type":"object"},"itemEncoding":{}}`, "", []any{map[string]int{"n": 1}, map[string]int{"n": 2}}, []string{"application/json", "application/json"}, []string{`{"n":1}`, `{"n":2}`}},
		{"itemSchema reference", `{"itemSchema":{"$ref":"#/components/schemas/Item"},"itemEncoding":{}}`, `"components":{"schemas":{"Item":{"type":"object"}}}`, []any{map[string]int{"n": 1}}, []string{"application/json"}, []string{`{"n":1}`}},
		{"prefixItems only reference", `{"schema":{"$ref":"#/components/schemas/Parts"}}`, `"components":{"schemas":{"Parts":{"type":"array","prefixItems":[{"type":"string"}]}}}`, []any{123}, []string{"text/plain"}, []string{"123"}},
		{"prefixItems reference", `{"schema":{"$ref":"#/components/schemas/Parts"}}`, `"components":{"schemas":{"Parts":{"type":"array","prefixItems":[{"type":"string"}],"items":{"type":"object"}}}}`, []any{123, map[string]int{"n": 1}}, []string{"text/plain", "application/json"}, []string{"123", `{"n":1}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := []string{}
			if tc.extra != "" {
				extra = append(extra, tc.extra)
			}
			c := editionClient(t, editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{"multipart/mixed":`+tc.media+`}}}}`, extra...), nil)
			req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: tc.body})
			_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
			if len(parts) != len(tc.types) {
				t.Fatalf("parts %d", len(parts))
			}
			for i, p := range parts {
				if p.header.Get("Content-Type") != tc.types[i] || string(trimNL(p.body)) != tc.bodies[i] {
					t.Errorf("part %d %+v", i, p)
				}
			}
		})
	}
}

// The same itemSchema default, with no explicit Encoding at all.
// Input.Body admits positional form-data slices with one-property wrappers,
// and ordinary multipart slices/iterators. The wrapper supplies the part name,
// while itemSchema controls the wrapped value's default media type.
func TestPositionalItemSchemaWithoutEncoding(t *testing.T) {
	for _, media := range []string{"multipart/mixed", "multipart/form-data"} {
		for _, iterator := range []bool{false, true} {
			if iterator && media == "multipart/form-data" {
				continue
			}
			t.Run(fmt.Sprintf("%s/iterator=%v", media, iterator), func(t *testing.T) {
				c := editionClient(t, editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{"`+media+`":{"itemSchema":{"type":"integer"}}}}}}`), nil)
				values := []any{42}
				if media == "multipart/form-data" {
					values = []any{map[string]any{"answer": 42}}
				}
				body := any(values)
				if iterator {
					body = slices.Values(values)
				}
				req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: body})
				_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
				if len(parts) != 1 {
					t.Fatalf("parts %d", len(parts))
				}
				if parts[0].header.Get("Content-Type") != "text/plain" || string(parts[0].body) != "42" {
					t.Errorf("part %#v", parts[0])
				}
				if media == "multipart/form-data" && parts[0].header.Get("Content-Disposition") != `form-data; name="answer"` {
					t.Errorf("disposition %q", parts[0].header.Get("Content-Disposition"))
				}
			})
		}
	}
}

// Edition gating applies to semantic schema reads as well as discovery.
// 3.0 Schema has no contentEncoding; legacy Reference Objects ignore sibling
// properties when describing/encoding a multipart body's fields.
func TestLegacyEditionMultipartSchemaSemantics(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version+"/contentEncoding", func(t *testing.T) {
			c := editionClient(t, editionPost(version, "multipart/form-data", `{"type":"object","properties":{"p":{"type":"string","contentEncoding":"base64"}}}`), nil)
			want := "application/octet-stream"
			if version == "3.0.4" {
				want = "text/plain"
			}
			m := reqMedia(t, mustOp(t, c, "POST /x"), 0)
			if got := m.Encoding[0].ContentType; got != want {
				t.Errorf("ContentType %q want %q", got, want)
			}
			req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]string{"p": "as given"}})
			_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
			if len(parts) != 1 || parts[0].header.Get("Content-Type") != want || string(parts[0].body) != "as given" {
				t.Errorf("parts %#v", parts)
			}
		})
	}
	for _, version := range []string{"2.0", "3.0.4"} {
		t.Run(version+"/ref siblings", func(t *testing.T) {
			ref := "#/components/schemas/S"
			extra := `"components":{"schemas":{"S":{"type":"object","properties":{"p":{"type":"string"}}}}}`
			if version == "2.0" {
				ref = "#/definitions/S"
				extra = `"definitions":{"S":{"type":"object","properties":{"p":{"type":"string"}}}}`
			}
			doc := strings.TrimSuffix(editionPost(version, "multipart/form-data", `{"$ref":"`+ref+`","properties":{"ignored":{"type":"object"}}}`), "}") + "," + extra + "}"
			c := editionClient(t, doc, nil)
			m := reqMedia(t, mustOp(t, c, "POST /x"), 0)
			if len(m.Encoding) != 1 || m.Encoding[0].Name != "p" {
				t.Errorf("Encoding names %q", encodingNames(m))
			}
			req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]string{"p": "target"}})
			_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
			if len(parts) != 1 || string(parts[0].body) != "target" {
				t.Errorf("parts %#v", parts)
			}
		})
	}
}

// Body and formData entries leave Params during Swagger normalization. The
// credential destinations must still identify the final Params, both when
// stale pre-compaction indices would be out of bounds and when they happen to
// remain in range and point at unrelated parameters. The required-destination
// and collision rules of doc.go Credentials apply after normalization.
func TestSwaggerBodyParamsCredentialDestinations(t *testing.T) {
	for _, bodyKind := range []string{"body", "formData"} {
		for _, position := range []int{0, 1, 2, 4} {
			t.Run(fmt.Sprintf("%s/%d", bodyKind, position), func(t *testing.T) {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("valid Swagger normalization panicked: %v", p)
					}
				}()
				params := []string{`{"name":"token","in":"query","required":true,"type":"string"}`, `{"name":"X-Key","in":"header","required":true,"type":"string"}`, `{"name":"other","in":"query","required":true,"type":"string"}`, `{"name":"X-Other","in":"header","required":true,"type":"string"}`}
				bodyDecl := `{"name":"payload","in":"body","schema":{"type":"object"}}`
				media := "application/json"
				if bodyKind == "formData" {
					bodyDecl = `{"name":"payload","in":"formData","type":"string","required":true}`
					media = "application/x-www-form-urlencoded"
				}
				params = slices.Insert(params, position, bodyDecl)
				doc := editionDoc("2.0", `"/x":{"post":{"consumes":["`+media+`"],"parameters":[`+strings.Join(params, ",")+`],"security":[{"q":[],"h":[]}]}}`, `"securityDefinitions":{"q":{"type":"apiKey","in":"query","name":"token"},"h":{"type":"apiKey","in":"header","name":"X-Key"}}`)
				rt := &memRT{}
				c := editionClient(t, doc, &openapi.Options{HTTPClient: &http.Client{Transport: rt}, Credentials: map[string]openapi.Credential{"q": openapi.Secret("query-secret"), "h": openapi.Secret("header-secret")}})
				op := mustOp(t, c, "POST /x")
				var names []string
				for _, p := range op.Params {
					names = append(names, p.Name)
				}
				if !slices.Equal(names, []string{"token", "X-Key", "other", "X-Other"}) {
					t.Fatalf("Params %q", names)
				}
				in := &openapi.Input{Params: map[string]any{"other": "ordinary", "X-Other": "plain"}, Body: map[string]string{"payload": "value"}}
				mustCall(t, c, op.Key, in, nil)
				r := rt.requests()[0]
				if r.URL.RawQuery != "other=ordinary&token=query-secret" || r.Header.Get("X-Key") != "header-secret" || r.Header.Get("X-Other") != "plain" {
					t.Errorf("request %+v", r)
				}
				for _, key := range []string{"token", "X-Key"} {
					bad := &openapi.Input{Params: map[string]any{"other": "ordinary", "X-Other": "plain", key: "collision"}, Body: in.Body}
					_, err := c.Prepare(op.Key, bad)
					wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, key)
				}
				_, err := c.Prepare(op.Key, &openapi.Input{Body: in.Body})
				re := asRequestError(t, err)
				wantKeys(t, "Inputs", re.Inputs, true, "other", "X-Other")
			})
		}
	}
	// Minimal out-of-bounds reproduction, separately from still-in-range cases.
	t.Run("minimal", func(t *testing.T) {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("normalization panicked: %v", p)
			}
		}()
		c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":["application/json"],"parameters":[{"name":"payload","in":"body","schema":{}},{"name":"token","in":"query","required":true,"type":"string"}],"security":[{"q":[]}]}}`, `"securityDefinitions":{"q":{"type":"apiKey","in":"query","name":"token"}}`), &openapi.Options{Credentials: map[string]openapi.Credential{"q": openapi.Secret("secret")}})
		op := mustOp(t, c, "POST /x")
		if len(op.Params) != 1 {
			t.Fatalf("Params %+v", op.Params)
		}
		mustPrepare(t, c, op.Key, nil)
	})
}

// A lazy-representation guard: normalized formData Raw contains each
// operation's own fields and shared schema data, is safe on concurrent first
// access, and returns independent mutable copies without altering Document.
func TestSwaggerFormDataSchemaRawIsolation(t *testing.T) {
	doc := editionDoc("2.0", `"/a":{"post":{"parameters":[{"$ref":"#/parameters/P"},{"name":"alpha","in":"formData","type":"integer"}]}},"/b":{"post":{"parameters":[{"$ref":"#/parameters/P"},{"name":"beta","in":"formData","type":"boolean"}]}}`, `"consumes":["application/x-www-form-urlencoded"],"parameters":{"P":{"name":"p","in":"formData","required":true,"type":"string","enum":["red","blue"],"minLength":1}}`)
	c := editionClient(t, doc, nil)
	schemas := []*openapi.Schema{reqMedia(t, mustOp(t, c, "POST /a"), 0).Schema, reqMedia(t, mustOp(t, c, "POST /b"), 0).Schema}
	var wants []any
	for _, field := range []string{`"alpha":{"type":"integer"}`, `"beta":{"type":"boolean"}`} {
		var v any
		if err := json.Unmarshal([]byte(`{"type":"object","properties":{"p":{"type":"string","enum":["red","blue"],"minLength":1},`+field+`},"required":["p"]}`), &v); err != nil {
			t.Fatal(err)
		}
		wants = append(wants, v)
	}
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 20 {
				for i, s := range schemas {
					raw := s.Raw()
					var got any
					if err := json.Unmarshal(raw, &got); err != nil {
						errs <- err
						return
					}
					if !reflect.DeepEqual(got, wants[i]) {
						errs <- fmt.Errorf("schema %d Raw %s", i, raw)
						return
					}
					raw[0] = '!'
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for i, s := range schemas {
		var got any
		if err := json.Unmarshal(s.Raw(), &got); err != nil || !reflect.DeepEqual(got, wants[i]) {
			t.Errorf("schema %d mutated Raw %s error %v", i, s.Raw(), err)
		}
	}
	if got := c.Document(testDocURI + "#/parameters/P"); !bytes.Contains(got, []byte(`"in":"formData"`)) || bytes.Contains(got, []byte("alpha")) || bytes.Contains(got, []byte("beta")) {
		t.Errorf("authored Parameter changed: %s", got)
	}
}
