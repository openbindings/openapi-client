package openapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// O2: OAS 3.2.1 4.15.1.1 defaults a whole positional array to JSON.
// Named array properties still expand into individual parts whose default
// comes from the items schema. Defaults never depend on the caller's value.
func TestStage6WholePositionalArrayDefaults(t *testing.T) {
	array := `{"type":"array","items":{"type":"integer"}}`
	for _, tc := range []struct{ name, decl, extra string }{
		{"itemSchema", `"itemSchema":` + array, ""},
		{"items", `"schema":{"type":"array","items":` + array + `}`, ""},
		{"prefixItems", `"schema":{"type":"array","prefixItems":[` + array + `]}`, ""},
		{"referenced item", `"itemSchema":{"$ref":"#/components/schemas/A"}`, `"components":{"schemas":{"A":` + array + `}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extras := []string{}
			if tc.extra != "" {
				extras = append(extras, tc.extra)
			}
			c := editionClient(t, editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{"multipart/mixed":{`+tc.decl+`}}}}}`, extras...), nil)
			for _, value := range []any{[]int{1, 2}, 123} {
				want, _ := json.Marshal(value)
				req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: []any{value}})
				_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
				if len(parts) != 1 {
					t.Fatalf("parts %d", len(parts))
				}
				if parts[0].header.Get("Content-Type") != "application/json" || string(trimNL(parts[0].body)) != string(want) {
					t.Errorf("part %#v want JSON %s", parts[0], want)
				}
			}
		})
	}
	t.Run("explicit type", func(t *testing.T) {
		c := editionClient(t, positionalDoc("multipart/mixed", `"itemSchema":`+array+`,"itemEncoding":{"contentType":"text/plain"}`), nil)
		req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: []any{"manual"}})
		_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
		if len(parts) != 1 || parts[0].header.Get("Content-Type") != "text/plain" || string(parts[0].body) != "manual" {
			t.Errorf("parts %#v", parts)
		}
	})
	t.Run("named array control", func(t *testing.T) {
		c := editionClient(t, editionPost("3.2.1", "multipart/form-data", `{"type":"object","properties":{"p":`+array+`}}`), nil)
		req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]any{"p": []int{1, 2}}})
		_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
		if len(parts) != 2 {
			t.Fatalf("parts %d", len(parts))
		}
		for i, p := range parts {
			if p.header.Get("Content-Type") != "text/plain" || string(p.body) != fmt.Sprint(i+1) {
				t.Errorf("part %#v", p)
			}
		}
	})
}

// O3: Encoding styles apply under multipart/form-data, override contentType,
// and use the established unescaped multipart style serializer. Other
// multipart types retain content encoding. Headers remain public descriptors.
func TestStage6PositionalEncodingContext(t *testing.T) {
	enc := `{"style":"form","explode":false,"contentType":"application/json","headers":{"X-Part":{"description":"part header","schema":{"type":"string"}},"Content-Type":{"schema":{}}}}`
	for _, media := range []string{"multipart/form-data", "multipart/mixed"} {
		for _, field := range []string{"prefixEncoding", "itemEncoding"} {
			t.Run(media+"/"+field, func(t *testing.T) {
				decl := `"itemEncoding":` + enc
				if field == "prefixEncoding" {
					decl = `"prefixEncoding":[` + enc + `]`
				}
				c := editionClient(t, positionalDoc(media, decl), nil)
				m := reqMedia(t, mustOp(t, c, "POST /x"), 0)
				p := m.Encoding[0]
				if len(p.Headers) != 1 || p.Headers[0].Name != "X-Part" || p.Headers[0].Source != p.Source+"/headers/X-Part" || p.Headers[0].Description != "part header" {
					t.Errorf("declared headers %+v", p.Headers)
				}
				if media == "multipart/form-data" && (p.Style != "form" || p.ContentType != "" || p.Explode) {
					t.Errorf("styled descriptor %+v", p)
				}
				for _, value := range []any{"hello", []string{"a b", "c"}, []byte("hi")} {
					part := value
					want, _ := json.Marshal(value)
					if media == "multipart/form-data" {
						part = map[string]any{"p": value}
						want = []byte("hello")
						if _, ok := value.([]string); ok {
							want = []byte("a b,c")
						}
						if _, ok := value.([]byte); ok {
							want = []byte("aGk=")
						}
					}
					req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: []any{part}})
					_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
					if len(parts) != 1 {
						t.Fatalf("parts %d", len(parts))
					}
					if string(trimNL(parts[0].body)) != string(want) {
						t.Errorf("body %q want %q", parts[0].body, want)
					}
				}
			})
		}
	}
}

// One nested Encoding declaration can be selected as different concrete
// multipart types on successive calls. Selection order must not change the
// applicable style semantics. The outer body is always the same declaration.
func TestStage6NestedEncodingMediaSelectionOrder(t *testing.T) {
	for _, positional := range []bool{false, true} {
		for _, first := range []string{"multipart/form-data", "multipart/mixed"} {
			t.Run(fmt.Sprintf("positional=%v/%s", positional, first), func(t *testing.T) {
				child := `"encoding":{"p":{"style":"form","explode":false,"contentType":"application/json"}}`
				if positional {
					child = `"itemEncoding":{"style":"form","explode":false,"contentType":"application/json"}`
				}
				c := editionClient(t, positionalDoc("multipart/mixed", `"itemEncoding":{"contentType":"multipart/form-data, multipart/mixed",`+child+`}`), nil)
				order := []string{first, "multipart/mixed", "multipart/form-data", first}
				for _, media := range order {
					var inner any = map[string]string{"p": "hello"}
					want := `"hello"`
					if positional {
						inner = []any{"hello"}
						if media == "multipart/form-data" {
							inner = []any{map[string]string{"p": "hello"}}
						}
					}
					if media == "multipart/form-data" {
						want = "hello"
					}
					req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: []any{openapi.Part{MediaType: media, Content: inner}}})
					_, _, outer := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
					if len(outer) != 1 {
						t.Fatalf("outer parts %d", len(outer))
					}
					_, _, parts := readMultipart(t, outer[0].header.Get("Content-Type"), outer[0].body)
					if len(parts) != 1 || string(trimNL(parts[0].body)) != want {
						t.Errorf("selection %s parts %#v want %q", media, parts, want)
					}
					if media == "multipart/form-data" && len(parts) == 1 && parts[0].header.Get("Content-Disposition") != formData("p") {
						t.Errorf("nested disposition %q", parts[0].header.Get("Content-Disposition"))
					}
				}
			})
		}
	}
}

// Part.Header retains its established wire and refusal behavior with
// positional Encoding headers; content-type belongs in Part.MediaType.
func TestStage6PositionalPartHeaderControl(t *testing.T) {
	for _, media := range []string{"multipart/form-data", "multipart/mixed"} {
		t.Run(media, func(t *testing.T) {
			c := editionClient(t, positionalDoc(media, `"itemEncoding":{"contentType":"text/plain","headers":{"X-Part":{"schema":{"type":"string"}}}}`), nil)
			for _, bad := range []bool{false, true} {
				p := openapi.Part{Content: "hello", Header: http.Header{"X-Part": {"value"}}}
				if bad {
					p.Header.Set("Content-Type", "text/plain")
				}
				var value any = p
				if media == "multipart/form-data" {
					value = map[string]any{"p": p}
				}
				req, err := c.Prepare("POST /x", &openapi.Input{Body: []any{value}})
				if bad {
					asRequestError(t, err)
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
				if len(parts) != 1 || parts[0].header.Get("X-Part") != "value" || string(parts[0].body) != "hello" {
					t.Errorf("parts %#v", parts)
				}
			}
		})
	}
}

// O4: Message.Media retains only form consumes entries for Swagger formData,
// preserving their order. Adjacent rejected entries cannot leave a stray
// alternative or prevent the sole usable alternative from selecting itself.
func TestStage6SwaggerFormConsumesFiltering(t *testing.T) {
	for _, types := range [][]string{
		{"application/json", "text/plain", "multipart/form-data"},
		{"multipart/form-data", "application/json", "text/plain"},
		{"application/json", "multipart/form-data", "text/plain"},
		{"application/json", "text/plain", "application/x-www-form-urlencoded"},
		{"text/plain", "application/json", "multipart/form-data", "application/x-www-form-urlencoded"},
		{"text/plain", "application/x-www-form-urlencoded", "application/json", "multipart/form-data"},
		{"application/json", "text/plain"},
	} {
		t.Run(strings.Join(types, ","), func(t *testing.T) {
			list, _ := json.Marshal(types)
			c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":`+string(list)+`,"parameters":[{"name":"p","in":"formData","type":"string"}]}}`), nil)
			var want []string
			for _, typ := range types {
				if typ == "multipart/form-data" || typ == "application/x-www-form-urlencoded" {
					want = append(want, typ)
				}
			}
			if len(want) == 0 {
				want = []string{""}
			}
			var got []string
			for _, m := range mustOp(t, c, "POST /x").Body.Media {
				got = append(got, m.Type)
			}
			if !slices.Equal(got, want) {
				t.Errorf("media %q want %q", got, want)
			}
			in := &openapi.Input{Body: map[string]string{"p": "value"}}
			if len(want) != 1 || want[0] == "" {
				if _, err := c.Prepare("POST /x", in); err == nil {
					t.Error("unspecified media selection accepted")
				}
				in.MediaType = "multipart/form-data"
			}
			mustPrepare(t, c, "POST /x", in)
		})
	}
}

// O5: collectionFormat governs structured fields; it does not replace the
// Input.Body raw-byte, reader or Part paths. Both form encoders obey this.
func TestStage6SwaggerArrayRawFieldParity(t *testing.T) {
	for _, media := range []string{"application/x-www-form-urlencoded", "multipart/form-data"} {
		for _, tc := range []struct {
			name      string
			makeValue func() any
		}{
			{"bytes", func() any { return []byte("a,b") }},
			{"reader", func() any { return strings.NewReader("a,b") }},
			{"one-shot reader", func() any { return struct{ io.Reader }{strings.NewReader("a,b")} }},
			{"Part", func() any { return openapi.Part{Content: []byte("a,b")} }},
			{"Part pointer", func() any { return &openapi.Part{Content: strings.NewReader("a,b")} }},
			{"named structured slice", func() any { return stage6Strings{"a", "b"} }},
		} {
			t.Run(media+"/"+tc.name, func(t *testing.T) {
				c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":["`+media+`"],"parameters":[{"name":"p","in":"formData","type":"array","items":{"type":"string"},"collectionFormat":"csv"}]}}`), nil)
				req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]any{"p": tc.makeValue()}})
				body := editionBody(t, req)
				if media == "application/x-www-form-urlencoded" {
					if string(body) != "p=a%2Cb" {
						t.Errorf("body %q", body)
					}
					return
				}
				_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), body)
				if len(parts) != 1 || string(parts[0].body) != "a,b" {
					t.Errorf("parts %#v", parts)
				}
			})
		}
	}
}

// O7: explicit external versions govern their body declaration; versionless
// fragments inherit the entry edition. An undeclared multipart field has
// the owning 3.0 text default or the later octet default, without guessing
// from the Go value. Exercise both absent and object-only schema context.
func TestStage6ExternalBodyOwnsFallbackDefault(t *testing.T) {
	for _, entry := range []string{"3.0.4", "3.1.2"} {
		for _, owner := range []string{"", "3.0.4", "3.1.2"} {
			for _, schema := range []string{"", `"schema":{"type":"object"}`} {
				t.Run(entry+"/owner="+owner+"/"+schema, func(t *testing.T) {
					version := ""
					if owner != "" {
						version = `"openapi":"` + owner + `",`
					}
					doc := `{` + version + `"components":{"requestBodies":{"B":{"content":{"multipart/form-data":{` + schema + `}}}}}}`
					l := openapi.Loader{Fetch: mapFetch(map[string]string{"https://api.example.test/body.json": doc})}
					c, err := l.Parse(t.Context(), []byte(editionDoc(entry, `"/x":{"post":{"requestBody":{"$ref":"body.json#/components/requestBodies/B"}}}`)), testDocURI, nil)
					if err != nil {
						t.Fatal(err)
					}
					actual := owner
					if actual == "" {
						actual = entry
					}
					if _, err := c.Prepare("POST /x", &openapi.Input{Body: map[string]any{"p": 123}}); actual == "3.0.4" {
						if err != nil {
							t.Fatal(err)
						}
					} else {
						wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, "Input.Body/p")
					}
					req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]string{"p": "123"}})
					_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
					want := "application/octet-stream"
					if actual == "3.0.4" {
						want = "text/plain"
					}
					if len(parts) != 1 || parts[0].header.Get("Content-Type") != want || string(parts[0].body) != "123" {
						t.Errorf("parts %#v want %s", parts, want)
					}
				})
			}
		}
	}
}

// O5 undefined raw/structured sources remain omitted before serialization.
func TestStage6SwaggerArrayUndefinedField(t *testing.T) {
	for _, media := range []string{"application/x-www-form-urlencoded", "multipart/form-data"} {
		c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":["`+media+`"],"parameters":[{"name":"p","in":"formData","type":"array","items":{"type":"string"},"collectionFormat":"csv"}]}}`), nil)
		for _, value := range []any{nil, (*strings.Reader)(nil), (*openapi.Part)(nil), []byte(nil), stage6Strings(nil)} {
			req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]any{"p": value}})
			body := editionBody(t, req)
			if media == "application/x-www-form-urlencoded" {
				if len(body) != 0 {
					t.Errorf("undefined form %q", body)
				}
				continue
			}
			_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), body)
			if len(parts) != 0 {
				t.Errorf("undefined parts %#v", parts)
			}
		}
	}
}

// O6: the same 1 MiB stop rule covers named/general/nested collection values
// and headers. Inputs are prebuilt and plans compiled outside measurement;
// reuse TestRequestSizeLimit's established 16 MiB allocation ceiling. Small
// counterparts retain JSON replacement and undefined-item semantics.
func TestStage6GeneralCollectionStopsAtLimit(t *testing.T) {
	const n = 4096
	chunk := strings.Repeat("x", 8192)
	named := make(stage6Strings, n)
	general := make([]any, n)
	nested := make([]stage6Strings, n)
	for i := range n {
		named[i] = chunk
		general[i] = chunk
		nested[i] = stage6Strings{chunk}
	}
	for _, tc := range []struct {
		name, location, items, want string
		large, small                any
	}{
		{"named query", "query", `{"type":"string"}`, "p=a%EF%BF%BDb,c", named, stage6Strings{"a\xffb", "c"}},
		{"general query", "query", `{"type":"string"}`, "p=a%EF%BF%BDb,c", general, []any{nil, "a\xffb", "c"}},
		{"named header", "header", `{"type":"string"}`, "a\ufffdb,c", named, stage6Strings{"a\xffb", "c"}},
		{"nested query", "query", `{"type":"array","items":{"type":"string"},"collectionFormat":"pipes"}`, "p=a%EF%BF%BDb%7Cc,d", nested, []stage6Strings{{"a\xffb", "c"}, {"d"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"p","in":"`+tc.location+`","type":"array","items":`+tc.items+`,"collectionFormat":"csv"}]}}`), nil)
			r := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"p": tc.small}})
			got := r.HTTP.URL.RawQuery
			if tc.location == "header" {
				got = r.HTTP.Header.Get("p")
			}
			if got != tc.want {
				t.Fatalf("small value %q want %q", got, tc.want)
			}
			in := &openapi.Input{Params: map[string]any{"p": tc.large}}
			var req *openapi.Request
			var err error
			cost := allocatedBy(func() { req, err = c.Prepare("GET /x", in) })
			if req != nil || err == nil {
				t.Fatal("oversized collection prepared")
			}
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "p")
			if cost > 16<<20 {
				t.Errorf("refusal allocated %d bytes; existing request-limit ceiling is 16 MiB", cost)
			}
		})
	}
}
