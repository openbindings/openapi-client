package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Where the rule for values written as references reaches, and what it costs.
// describe.go, Operation: "a reference whose target lies inside it cannot be
// followed before bundling either: it makes its own nearest part unusable
// the same way. Extension values and examples are not such values." The
// harness, and the checks of each call in both orders and of nothing being
// fetched, are those of bundlevalues_test.go.

// largeMapDoc is an OpenAPI 3.1.2 document with n members of
// components.parameters, named prefix followed by a number, and m operations
// each referring to one of the first eight; ext adds a Paths entry whose $ref
// names another document, so that loading follows references across
// documents.
func largeMapDoc(n, m int, prefix string, ext bool) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.2","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{`)
	if ext {
		b.WriteString(`"/ext":{"$ref":"ext.json"}`)
	}
	for i := range m {
		if i > 0 || ext {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"/p%d":{"get":{"operationId":"o%d","parameters":[{"$ref":"#/components/parameters/%s%d"}],"responses":{"200":{"description":"ok"}}}}`, i, i, prefix, i%8)
	}
	b.WriteString(`},"components":{"parameters":{`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"%s%d":{"name":"p","in":"query"}`, prefix, i)
	}
	b.WriteString(`}}}`)
	return []byte(b.String())
}

// largeMapLoader serves doc as testDocURI and an empty Path Item as ext.json.
func largeMapLoader(doc []byte) *openapi.Loader {
	return &openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
		if u == testDocURI {
			return io.NopCloser(strings.NewReader(string(doc))), "", nil
		}
		return io.NopCloser(strings.NewReader(`{}`)), "", nil
	}}
}

// Regression check, not contract: following a reference into a map is a lookup
// of its member: its cost must not grow with the members the map holds, whether
// their names sort above or below "$ref", in describing (Client.Operations) or
// in loading (load.go, Loader: references "are followed"). With the operations
// fixed, sixteen times the members must cost about the same (wantFlat, the
// convention of scaling_test.go; only parsing the larger map grows, and it is
// small beside the references).
func TestBundleCostLargeMap(t *testing.T) {
	const operations = 4000
	for _, prefix := range []string{"P", "!P"} {
		wantFlat(t, "describing, names "+prefix, 1000, func(n int) func() {
			return timedOperations(t, largeMapDoc(n, operations, prefix, false))
		})
		wantFlat(t, "loading across documents, names "+prefix, 1000, func(n int) func() {
			doc := largeMapDoc(n, operations, prefix, true)
			l := largeMapLoader(doc)
			return func() {
				if _, err := l.Load(context.Background(), testDocURI, nil); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// load.go, Load: "ctx bounds the whole load, reading and parsing included".
// A context canceled while the load is under way, here when the other
// document is retrieved, ends it with the context's error and no Client.
func TestBundleCostLoadCanceled(t *testing.T) {
	doc := largeMapDoc(16000, 4000, "P", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
		if u == testDocURI {
			return io.NopCloser(strings.NewReader(string(doc))), "", nil
		}
		cancel()
		return io.NopCloser(strings.NewReader(`{}`)), "", nil
	}}
	c, err := l.Load(ctx, testDocURI, nil)
	if c != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Load returned %v, %v; want no Client and an error matching context.Canceled", c != nil, err)
	}
}

// A schemas map (OAS 3.0.4 and 3.1.2 Components Object: schemas is
// "Map[string, Schema Object | Reference Object]"), or Swagger 2.0
// definitions, is never itself a reference. Written as one, its members are
// not read, so a Schema reference into it cannot be followed, and the schema
// a member beside the reference refers to is never retrieved. A schema
// defect is reported by Schema.References, not Media.Err (describe.go,
// Media.Err: "Any other schema or Encoding defect that affects structured
// value encoding does not set Media.Err: it is reported by
// Schema.References"); the reference's Err says to bundle and its Target is
// nil (SchemaReference: "It is nil when Err is set"). Client.Schema for the
// member's URI returns an error, the member being no Schema Object of the
// loaded graph. A JSON body needs no schema to be written ("values are never
// validated against schemas", doc.go, Values), so it is sent.
func TestBundleScopeSchemaMap(t *testing.T) {
	pet := `"Pet":{"type":"object","properties":{"owner":{"$ref":"owner.json"}}}`
	docs := map[string]string{
		"2.0": editionDoc("2.0", `"/pets":{"post":{"operationId":"create","consumes":["application/json"],
			"parameters":[{"name":"body","in":"body","schema":{"$ref":"#/definitions/Pet"}}],`+swaggerResponse+`}}`,
			`"definitions":{"$ref":"defs.json",`+pet+`}`),
	}
	uris := map[string]string{"2.0": testDocURI + "#/definitions/Pet"}
	for _, v := range []string{"3.0.4", "3.1.2"} {
		docs[v] = shapeDoc(v, `"/pets":{"post":{"operationId":"create","requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Pet"}}}},`+partErrResponse+`}}`,
			`"components":{"schemas":{"$ref":"schemas.json",`+pet+`}}`)
		uris[v] = testDocURI + "#/components/schemas/Pet"
	}
	var cases []shapeCase
	for _, v := range editionVersions[:3] {
		uri := uris[v]
		cases = append(cases, shapeCase{orderCase: orderCase{name: v, key: "create", ok: &openapi.Input{Body: map[string]string{"name": "x"}}, body: firstMedia},
			doc: docs[v],
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				m := mediaAt(0)(op)
				if m == nil || m.Schema == nil {
					t.Fatalf("Media %+v; want one with a Schema", m)
				}
				wantUsable(t, "Media", m.Err)
				refs, err := m.Schema.References()
				if err != nil {
					t.Fatalf("References: %v", err)
				}
				found := false
				for _, r := range refs {
					if !strings.HasSuffix(r.Value, "/Pet") {
						continue
					}
					found = true
					wantBundled(t, "reference "+r.Value, r.Err)
					if r.Target != nil {
						t.Errorf("reference %s has a Target %s", r.Value, r.Target.Source())
					}
				}
				if !found {
					t.Errorf("References %+v; want the reference to Pet", refs)
				}
			},
			after: func(t *testing.T, c *openapi.Client, _ *openapi.Operation) {
				if s, err := c.Schema(uri); s != nil || err == nil {
					t.Errorf("Client.Schema(%q) = %v, %v; want an error", uri, s != nil, err)
				}
			}})
	}
	runShapeCases(t, cases)
}

// An OpenAPI 3.2 Security Requirement Object is never a reference, so one
// whose $ref member is a string is a value written as a reference and none of
// its members is read: neither it nor the names beside it are retrieved. The
// security value is held by the operation, at the root or its own, so the
// nearest part is the Operation (Operation.Err: "a security value, the
// operation's or the root's it inherits", written as a reference), and every
// call is refused wrapping its Err. An operation whose own security is empty
// declares no requirement and inherits nothing (describe.go,
// Operation.Security).
func TestBundleScopeSecurityRequirement(t *testing.T) {
	doc := shapeDoc("3.2.1", `
		"/r":{"get":{"operationId":"r",`+partErrResponse+`}},
		"/o":{"get":{"operationId":"o","security":[{"$ref":"opreq.json","opsib.json#/s":[]}],`+partErrResponse+`}},
		"/none":{"get":{"operationId":"none","security":[],`+partErrResponse+`}}`,
		`"security":[{"$ref":"req.json","sibling.json":[]}]`)
	bundled := func(t *testing.T, op *openapi.Operation) { wantBundled(t, "Operation", op.Err) }
	runShapeCases(t, []shapeCase{
		{orderCase: orderCase{name: "root", key: "r", bad: []refusal{{"a call", nil, "Err", "", opErr}}}, doc: doc, check: bundled},
		{orderCase: orderCase{name: "operation", key: "o", bad: []refusal{{"a call", nil, "Err", "", opErr}}}, doc: doc, check: bundled},
		{orderCase: orderCase{name: "empty, for contrast", key: "none", ok: &openapi.Input{}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) { wantUsable(t, "Operation", op.Err) }},
	})
}

// "Extension values and examples are not such values": a Callback Object's,
// or the Paths Object's, extension member is data, so an object inside it
// whose $ref member is a string marks nothing, and a parameter reference into
// its other members is followed as before. Nothing is retrieved.
func TestBundleScopeExtensionTargets(t *testing.T) {
	ext := `{"get":{"$ref":"data","parameters":[{"name":"q","in":"query","schema":{"type":"string"}}]}}`
	doc := shapeDoc("3.1.2", `
		"/a":{"get":{"operationId":"a","parameters":[{"$ref":"#/components/callbacks/C/x-ext/get/parameters/0"}],`+partErrResponse+`}},
		"/b":{"get":{"operationId":"b","parameters":[{"$ref":"#/paths/x-ext/get/parameters/0"}],`+partErrResponse+`}},
		"x-ext":`+ext,
		`"components":{"callbacks":{"C":{"x-ext":`+ext+`}}}`)
	var cases []shapeCase
	for name, key := range map[string]string{"Callback extension": "a", "Paths extension": "b"} {
		cases = append(cases, shapeCase{orderCase: orderCase{name: name, key: key, ok: &openapi.Input{Params: map[string]any{"q": "x"}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				p := paramByName(op.Params, "q")
				if p == nil {
					t.Fatalf("Params %q; want q", paramNames(op.Params))
				}
				wantUsable(t, "parameter q", p.Err)
			},
			after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
				req := mustPrepare(t, c, op.Key, &openapi.Input{Params: map[string]any{"q": "x"}})
				if got := req.HTTP.URL.RawQuery; got != "q=x" {
					t.Errorf("query %q; want q=x", got)
				}
			}})
	}
	runShapeCases(t, cases)
}

// A nested encoding written as a reference counts even where OpenAPI says it
// is ignored (describe.go, Operation: "A value so written makes its part
// unusable even where OpenAPI says the value is ignored, such as an encoding
// under a JSON media type ..., since the document still needs bundling"), as
// in OAS 3.2.1, Encoding Usage and Restrictions, "for all other media types
// all three fields SHALL be ignored". A nested encoding map, or a
// prefixEncoding list, written as a reference under a part whose type is
// application/json therefore makes the field's Encoding Param unusable, its
// nearest part, and a call using the field is refused at its pointer wrapping
// that Err; one without it is sent.
func TestBundleScopeNestedEncodingNotApplying(t *testing.T) {
	flat := `"schema":{"type":"object","properties":{"meta":{"type":"object"},"ok":{"type":"string"}}}`
	nested := `"schema":{"type":"object","properties":{"meta":{"type":"object","properties":{"g":{"type":"object"}}},"ok":{"type":"string"}}}`
	doc := shapeDoc("3.2.1", `
		"/a":{"post":{"operationId":"a","requestBody":{"content":{"multipart/form-data":{`+flat+`,
			"encoding":{"meta":{"contentType":"application/json","encoding":{"$ref":"e.yaml"}}}}}},`+partErrResponse+`}},
		"/b":{"post":{"operationId":"b","requestBody":{"content":{"multipart/form-data":{`+nested+`,
			"encoding":{"meta":{"contentType":"multipart/mixed","encoding":{"g":{"contentType":"application/json","encoding":{"h":{"$ref":"e.yaml"}}}}}}}}},`+partErrResponse+`}},
		"/c":{"post":{"operationId":"c","requestBody":{"content":{"application/x-www-form-urlencoded":{`+flat+`,
			"encoding":{"meta":{"contentType":"application/json","prefixEncoding":{"$ref":"e.yaml"}}}}}},`+partErrResponse+`}}`)
	meta := &openapi.Input{Body: map[string]any{"meta": map[string]any{"g": map[string]any{"x": 1}}}}
	var cases []shapeCase
	for _, tc := range []struct{ name, key string }{
		{"multipart field of type application/json, encoding map", "a"},
		{"nested application/json part, encoding entry", "b"},
		{"form field of type application/json, prefixEncoding list", "c"},
	} {
		cases = append(cases, shapeCase{orderCase: orderCase{name: tc.name, key: tc.key,
			ok: &openapi.Input{Body: map[string]any{"ok": "x"}}, body: firstMedia,
			bad: []refusal{{"a value for meta", meta, "Inputs", "Input.Body/meta", fieldNamed("meta")}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantUsable(t, "Media", mediaErrAt(0)(op))
				wantBundled(t, "field meta", fieldNamed("meta")(op))
			}})
	}
	runShapeCases(t, cases)
}

// An encoding map written as a reference sets Media.Err "under any media
// type" (describe.go, Media.Err), and counts "even where OpenAPI says the
// value is ignored" (describe.go, Operation), as OAS 3.0.4 and 3.1.2 say of a
// response's ("The encoding field SHALL only apply to Request Body
// Objects"). So a response Media of a form or multipart type whose encoding
// is written as a reference has Media.Err saying to bundle in every 3.x
// edition. The response is not part of the request, which is sent.
func TestBundleScopeResponseEncoding(t *testing.T) {
	var cases []shapeCase
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		doc := shapeDoc(v, `"/r":{"get":{"operationId":"r","responses":{"200":{"description":"ok","content":{
			"multipart/form-data":{"schema":{"type":"object","properties":{"a":{"type":"string"}}},"encoding":{"$ref":"e.yaml"}},
			"application/x-www-form-urlencoded":{"schema":{"type":"object"},"encoding":{"$ref":"e.yaml"}}}}}}}`)
		cases = append(cases, shapeCase{orderCase: orderCase{name: v, key: "r"}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				m := respByKey(op, "200")
				if m == nil || len(m.Media) != 2 {
					t.Fatalf("response 200 %+v; want two Media", m)
				}
				wantUsable(t, "response 200", m.Err)
				for _, md := range m.Media {
					wantBundled(t, "Media "+md.Type, md.Err)
				}
			},
			after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) { sendDeclared(t, c, op, nil, "200") }})
	}
	runShapeCases(t, cases)
}
