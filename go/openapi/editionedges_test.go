package openapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// OAS 3.0.4 4.7.15.1.2 explicitly ignores style/explode/allowReserved on
// multipart. Later lines apply them to multipart/form-data (OAS 3.1.2
// 4.8.15.1.2), without URI escaping. 3.0 keeps its distinct rule.
func TestEditionsMultipartStyleApplicability(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			doc := editionDoc(version, `"/x":{"post":{"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"p":{"type":"array","items":{"type":"string"}}}},"encoding":{"p":{"style":"form","explode":false,"contentType":"application/json"}}}}}}}`)
			c := editionClient(t, doc, nil)
			req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]any{"p": []string{"a b", "c/d"}}})
			_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
			if version == "3.0.4" {
				if len(parts) != 2 {
					t.Fatalf("parts %d want 2", len(parts))
				}
				for i, want := range []string{`"a b"`, `"c/d"`} {
					if string(trimNL(parts[i].body)) != want || parts[i].header.Get("Content-Type") != "application/json" {
						t.Errorf("part %d %#v", i, parts[i])
					}
				}
			} else {
				if len(parts) != 1 || string(parts[0].body) != "a b,c/d" || parts[0].header.Get("Content-Type") != "text/plain" {
					t.Errorf("parts %#v", parts)
				}
			}
		})
	}
}

// doc.go Values uses the latest patch of each minor line: patch spelling
// does not select historical Encoding defaults or disable newer fixes.
func TestEditionsPatchVersions(t *testing.T) {
	for _, version := range []string{"3.0.0", "3.0.123", "3.1.0", "3.1.123", "3.2.0", "3.2.123"} {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, editionPost(version, "multipart/form-data", `{"type":"object","properties":{"p":{"type":"string","format":"byte"}}}`), nil)
			want := "text/plain"
			if strings.HasPrefix(version, "3.0.") {
				want = "application/octet-stream"
			}
			if got := reqMedia(t, mustOp(t, c, "POST /x"), 0).Encoding[0].ContentType; got != want {
				t.Errorf("type %q want %q", got, want)
			}
		})
	}
}

// Reference Object descriptions apply at response/request-body/header/scheme
// sites as describe.go Operation specifies. The target Source is retained.
func TestEditionsReferencedDescriptors(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			doc := editionDoc(version, `"/x":{"post":{"requestBody":{"$ref":"#/components/requestBodies/B","description":"use"},"responses":{"200":{"$ref":"#/components/responses/R","description":"use"}},"security":[{"key":[]}]}}`, `"components":{"requestBodies":{"B":{"description":"target","content":{"text/plain":{}}}},"responses":{"R":{"description":"target","headers":{"X-H":{"$ref":"#/components/headers/H","description":"use"}}}},"headers":{"H":{"description":"target","schema":{"type":"string"}}},"securitySchemes":{"key":{"$ref":"#/components/securitySchemes/Target","description":"use"},"Target":{"type":"apiKey","in":"header","name":"X-Key","description":"target"}}}`)
			c := editionClient(t, doc, nil)
			op := mustOp(t, c, "POST /x")
			want := "use"
			if version == "3.0.4" {
				want = "target"
			}
			for _, tc := range []struct{ desc, source, wantSource string }{{op.Body.Description, op.Body.Source, "#/components/requestBodies/B"}, {op.Responses[0].Description, op.Responses[0].Source, "#/components/responses/R"}, {op.Responses[0].Headers[0].Description, op.Responses[0].Headers[0].Source, "#/components/headers/H"}, {op.Security[0].Schemes[0].Description, op.Security[0].Schemes[0].Source, "#/components/securitySchemes/Target"}} {
				if tc.desc != want || tc.source != testDocURI+tc.wantSource {
					t.Errorf("description %q Source %s, want %q %s", tc.desc, tc.source, want, testDocURI+tc.wantSource)
				}
			}
		})
	}
}

// External fragments need no version. A Swagger 2.0 parameter or response
// Reference Object naming another document's fragment is followed, and the
// part it reaches keeps the target as its Source. A root parameters or
// responses entry written as a reference is not such a Reference Object (see
// TestSwaggerDefinitionEntryWrittenAsReference).
func TestEditionsSwaggerExternalFragments(t *testing.T) {
	l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
		switch u {
		case "https://api.example.test/p.json":
			return io.NopCloser(strings.NewReader(`{"Body":{"name":"body","in":"body","schema":{"type":"object"}}}`)), "", nil
		case "https://api.example.test/r.json":
			return io.NopCloser(strings.NewReader(`{"OK":{"description":"target","schema":{"type":"object"}}}`)), "", nil
		}
		return nil, "", fmt.Errorf("unexpected %s", u)
	}}
	doc := editionDoc("2.0", `"/x":{"post":{"consumes":["application/json"],"produces":["application/json"],"parameters":[{"$ref":"p.json#/Body"}],"responses":{"200":{"$ref":"r.json#/OK","description":"ignored"}}}}`)
	c, err := l.Parse(t.Context(), []byte(doc), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	op := mustOp(t, c, "POST /x")
	if op.Body.Source != "https://api.example.test/p.json#/Body" || op.Responses[0].Source != "https://api.example.test/r.json#/OK" || op.Responses[0].Description != "target" {
		t.Errorf("body %+v response %+v", op.Body, op.Responses[0])
	}
	mustPrepare(t, c, op.Key, &openapi.Input{Body: map[string]int{"n": 1}})
}

// Schema.Dialect and Schema's handle semantics name the schema's document.
// An explicitly versioned referenced document therefore keeps its own schema
// edition, while a versionless fragment uses the entry edition.
func TestEditionsMixedDocumentSchema(t *testing.T) {
	l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
		if u != "https://api.example.test/legacy.json" {
			return nil, "", fmt.Errorf("unexpected %s", u)
		}
		return io.NopCloser(strings.NewReader(`{"openapi":"3.0.4","paths":{},"components":{"requestBodies":{"B":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/S","description":"ignored"}}}}},"schemas":{"S":{"type":"object"}}}}`)), "", nil
	}}
	c, err := l.Parse(t.Context(), []byte(editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"$ref":"legacy.json#/components/requestBodies/B"}}}`)), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := reqMedia(t, mustOp(t, c, "POST /x"), 0).Schema
	if s.Dialect() != "" || s.Source() != "https://api.example.test/legacy.json#/components/schemas/S" || string(s.Raw()) != `{"type":"object"}` {
		t.Errorf("schema %s source %s dialect %s", s.Raw(), s.Source(), s.Dialect())
	}
	mustPrepare(t, c, "POST /x", &openapi.Input{Body: map[string]int{"n": 1}})
}

// Swagger body/formData coexistence is prohibited by Parameter Object 6.4.9;
// two body parameters are prohibited too. These defects affect the body and
// cannot be silently resolved by selecting the first declaration.
func TestEditionsSwaggerBodyDefects(t *testing.T) {
	for _, decl := range []string{
		`{"name":"a","in":"body","schema":{}},{"name":"b","in":"body","schema":{}}`,
		`{"name":"a","in":"body","schema":{}},{"name":"b","in":"formData","type":"string"}`,
	} {
		t.Run(decl, func(t *testing.T) {
			c := editionClient(t, editionDoc("2.0", `"/x":{"post":{"consumes":["application/json","application/x-www-form-urlencoded"],"parameters":[`+decl+`]}}`), nil)
			if _, err := c.Prepare("POST /x", &openapi.Input{MediaType: "application/json", Body: map[string]int{"n": 1}}); err == nil {
				t.Error("ambiguous body declaration prepared")
			}
		})
	}
}

// A 307 replay and an ordinary failure response exercise inherited transport
// and response handling through each edition's normalized body descriptor.
func TestEditionsRedirectAndStatus(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/x" {
					rw.Header().Set("Location", "/done")
					rw.WriteHeader(307)
					return
				}
				rw.Header().Set("Content-Type", "application/json")
				io.WriteString(rw, `{"ok":true}`)
			})
			c := parseFor(t, w, editionPost(version, "application/json", `{"type":"object"}`), &openapi.Options{Redirects: openapi.FollowAll})
			mustCall(t, c, "POST /x", &openapi.Input{Body: map[string]int{"n": 1}}, nil)
			rs := w.requests()
			if len(rs) != 2 || string(rs[0].Body) != string(rs[1].Body) || rs[1].Method != "POST" {
				t.Errorf("requests %+v", rs)
			}
			w.setAnswer(jsonAnswer(400, `{"message":"bad"}`))
			resp, err := c.Call(t.Context(), "POST /x", &openapi.Input{Body: []byte("{}")}, nil)
			if resp == nil || err == nil {
				t.Error("400 not reported")
			}
		})
	}
}
