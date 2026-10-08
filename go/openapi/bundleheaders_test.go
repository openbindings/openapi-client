package openapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// References inside the Header Objects of an Encoding Object's headers map.
// describe.go, Operation: a value written as a reference "makes the nearest
// part holding that value unusable", and "A value so written makes its part
// unusable even where OpenAPI says the value is ignored, such as an encoding
// under a JSON media type or an Encoding Object's headers for a field written
// by its style". describe.go, Media.Err: it is set by "an entry of one or an
// itemEncoding that no Encoding Param describes written as or holding one ...,
// under any media type, which leaves no Encoding Param to report it". The
// harness, and the checks of each call in both orders and of nothing being
// fetched, are those of bundlevalues_test.go.

// headerValues are headers maps whose header X-A holds a value written as a
// reference, with the editions each applies to and the root members they
// need: a content map so written in every 3.x edition; before OpenAPI 3.2,
// where a content entry is a Media Type Object and never a reference, a
// content entry so written, and a content entry whose encoding map is so
// written; and, in every 3.x edition, a Header reference, which OpenAPI
// defines, to a component whose content map is so written. "The descriptions
// follow references" (describe.go, Operation), and so does holding: the
// Header reference's entry holds what its target holds.
var headerValues = []struct {
	name, headers string
	editions      []string
	extra         []string
}{
	{"content map", `{"X-A":{"content":{"$ref":"c.yaml"}}}`, []string{"3.0.4", "3.1.2", "3.2.1"}, nil},
	{"content entry", `{"X-A":{"content":{"text/plain":{"$ref":"m.yaml"}}}}`, []string{"3.0.4", "3.1.2"}, nil},
	{"content entry's encoding", `{"X-A":{"content":{"multipart/mixed":{"encoding":{"$ref":"e.yaml"}}}}}`, []string{"3.0.4", "3.1.2"}, nil},
	{"Header reference to a holding component", `{"X-A":{"$ref":"#/components/headers/H"}}`, []string{"3.0.4", "3.1.2", "3.2.1"},
		[]string{`"components":{"headers":{"H":{"content":{"$ref":"c.yaml"}}}}`}},
}

// headerSchema is an object schema with the properties a and ok.
const headerSchema = `"schema":{"type":"object","properties":{"a":{"type":"string"},"ok":{"type":"string"}}}`

// The nearest described part carries the bundling Err, wherever the headers
// sit:
//
//   - under application/json, which has no Encoding Params (Media.Encoding
//     describes "the fields of form or multipart content"), and in a response,
//     the Media (Media.Err, as above); a structured body is refused at the
//     body, and a pre-encoded one, which "is checked against neither", is
//     sent;
//   - in a form-urlencoded field, whose Encoding headers OpenAPI ignores
//     (OAS 3.x Encoding Object: headers "SHALL be ignored if the media type is
//     not a multipart"), and in a field written by its style in
//     multipart/form-data, whose headers "are never read" (describe.go,
//     Param.Headers), the field's Encoding Param;
//   - in a query parameter's form content, which has no field descriptors,
//     the parameter;
//   - in OpenAPI 3.2, two levels down, in the Encoding of a multipart/mixed
//     part's own field, the outer field's Encoding Param.
//
// A call using that part is refused at its key wrapping the Err; one without
// it is sent.
func TestBundleHeadersNearestPart(t *testing.T) {
	var cases []shapeCase
	for _, hv := range headerValues {
		for _, v := range hv.editions {
			entry := `{"a":{"headers":` + hv.headers + `}}`
			paths := []string{
				`"/json":{"post":{"operationId":"json","requestBody":{"content":{"application/json":{` + headerSchema + `,"encoding":` + entry + `}}},` + partErrResponse + `}}`,
				`"/form":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{` + headerSchema + `,"encoding":` + entry + `}}},` + partErrResponse + `}}`,
				`"/resp":{"get":{"operationId":"resp","responses":{"200":{"description":"ok","content":{"multipart/mixed":{` + headerSchema + `,"encoding":` + entry + `}}}}}}`,
				`"/param":{"get":{"operationId":"param","parameters":[{"name":"q","in":"query","content":{"application/x-www-form-urlencoded":{` + headerSchema + `,"encoding":` + entry + `}}}],` + partErrResponse + `}}`,
			}
			if v != "3.0.4" { // styles apply under multipart/form-data from OpenAPI 3.1
				paths = append(paths, `"/styled":{"post":{"operationId":"styled","requestBody":{"content":{"multipart/form-data":{`+headerSchema+`,"encoding":{"a":{"style":"form","headers":`+hv.headers+`}}}}},`+partErrResponse+`}}`)
			}
			if v == "3.2.1" { // nested encodings are OpenAPI 3.2
				paths = append(paths, `"/nested":{"post":{"operationId":"nested","requestBody":{"content":{"multipart/form-data":{
					"schema":{"type":"object","properties":{"f":{"type":"object","properties":{"x":{"type":"string"}}},"ok":{"type":"string"}}},
					"encoding":{"f":{"contentType":"multipart/mixed","encoding":{"x":{"contentType":"text/plain","headers":`+hv.headers+`}}}}}}},`+partErrResponse+`}}`)
			}
			doc := shapeDoc(v, strings.Join(paths, ","), hv.extra...)
			prefix := v + " " + hv.name + ", "
			mediaBundled := func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantBundled(t, "Media", mediaErrAt(0)(op))
			}
			fieldBundled := func(name string) func(*testing.T, *openapi.Operation) {
				return func(t *testing.T, op *openapi.Operation) {
					wantUsable(t, "Operation", op.Err)
					wantUsable(t, "Media", mediaErrAt(0)(op))
					wantBundled(t, "field "+name, fieldNamed(name)(op))
					wantUsable(t, "field ok", fieldNamed("ok")(op))
				}
			}
			field := func(name string, value any) []refusal {
				return []refusal{{"a value for " + name, &openapi.Input{Body: map[string]any{name: value}}, "Inputs", "Input.Body/" + name, fieldNamed(name)}}
			}
			okBody := &openapi.Input{Body: map[string]any{"ok": "x"}}
			cases = append(cases,
				shapeCase{orderCase: orderCase{name: prefix + "application/json", key: "json", ok: &openapi.Input{Body: []byte(`{"a":"x"}`)}, body: firstMedia,
					bad: []refusal{{"a structured body", &openapi.Input{Body: map[string]any{"a": "x"}}, "Inputs", "Input.Body", mediaErrAt(0)}}}, doc: doc, check: mediaBundled},
				shapeCase{orderCase: orderCase{name: prefix + "form field", key: "form", ok: okBody, body: firstMedia, bad: field("a", "x")}, doc: doc, check: fieldBundled("a")},
				shapeCase{orderCase: orderCase{name: prefix + "response", key: "resp"}, doc: doc,
					check: func(t *testing.T, op *openapi.Operation) {
						wantUsable(t, "Operation", op.Err)
						if m := respByKey(op, "200"); m == nil || len(m.Media) != 1 {
							t.Fatalf("response 200 %+v; want one Media", m)
						} else {
							wantUsable(t, "response 200", m.Err)
							wantBundled(t, "response Media", m.Media[0].Err)
						}
					},
					after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) { sendDeclared(t, c, op, nil, "200") }},
				shapeCase{orderCase: orderCase{name: prefix + "query parameter", key: "param", ok: &openapi.Input{},
					bad: []refusal{{"a value", &openapi.Input{Params: map[string]any{"q": map[string]string{"a": "x"}}}, "Inputs", "q", paramNamed("q")}}}, doc: doc,
					check: func(t *testing.T, op *openapi.Operation) {
						wantUsable(t, "Operation", op.Err)
						wantBundled(t, "parameter q", paramNamed("q")(op))
					}},
			)
			if v != "3.0.4" {
				cases = append(cases, shapeCase{orderCase: orderCase{name: prefix + "styled multipart/form-data field", key: "styled", ok: okBody, body: firstMedia, bad: field("a", "x")}, doc: doc, check: fieldBundled("a")})
			}
			if v == "3.2.1" {
				cases = append(cases, shapeCase{orderCase: orderCase{name: prefix + "nested multipart field", key: "nested", ok: okBody, body: firstMedia,
					bad: field("f", map[string]string{"x": "v"})}, doc: doc, check: fieldBundled("f")})
			}
		}
	}
	runShapeCases(t, cases)
}

// Controls. In a multipart field that is not written by its style, the
// headers its Encoding declares are described as the field's Headers
// (describe.go, Param.Headers), so a header holding a value written as a
// reference is the nearest part: that header's Param.Err says to bundle, and
// the field's does not. A header written as a Reference Object to
// components/headers is a reference OpenAPI defines (OAS 3.x Encoding
// Object: headers is "Map[string, Header Object | Reference Object]"), so it
// marks nothing, and a call using the field is sent. When that reference's
// target holds a value written as a reference, the reference is followed and
// the header's Param describes the target (describe.go, Operation: "The
// descriptions follow references"), so that Param is the nearest part holding
// the value and reports it.
func TestBundleHeadersControls(t *testing.T) {
	var cases []shapeCase
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		mixed := func(key, headers string) string {
			return `"/` + key + `":{"post":{"operationId":"` + key + `","requestBody":{"content":{"multipart/mixed":{` + headerSchema + `,"encoding":{"a":{"headers":` + headers + `}}}}},` + partErrResponse + `}}`
		}
		doc := shapeDoc(v, strings.Join([]string{
			mixed("own", `{"X-A":{"content":{"$ref":"c.yaml"}}}`),
			mixed("legit", `{"X-A":{"$ref":"#/components/headers/H"}}`),
			mixed("target", `{"X-A":{"$ref":"#/components/headers/H2"}}`),
		}, ","), `"components":{"headers":{"H":{"schema":{"type":"string"}},"H2":{"content":{"$ref":"c.yaml"}}}}`)
		header := func(op *openapi.Operation) *openapi.Param {
			if f := mediaAt(0)(op); f != nil {
				if a := paramByName(f.Encoding, "a"); a != nil {
					return paramByName(a.Headers, "X-A")
				}
			}
			return nil
		}
		headerCheck := func(bundled bool) func(*testing.T, *openapi.Operation) {
			return func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantUsable(t, "Media", mediaErrAt(0)(op))
				wantUsable(t, "field a", fieldNamed("a")(op))
				h := header(op)
				if h == nil {
					t.Fatal("field a describes no header X-A")
				}
				if bundled {
					wantBundled(t, "header X-A", h.Err)
				} else {
					wantUsable(t, "header X-A", h.Err)
				}
			}
		}
		okBody := &openapi.Input{Body: map[string]any{"ok": "x"}}
		cases = append(cases,
			shapeCase{orderCase: orderCase{name: v + " header with its own Param", key: "own", ok: okBody, body: firstMedia}, doc: doc, check: headerCheck(true)},
			shapeCase{orderCase: orderCase{name: v + " Header reference", key: "legit", ok: &openapi.Input{Body: map[string]any{"a": "x"}}, body: firstMedia}, doc: doc, check: headerCheck(false)},
			shapeCase{orderCase: orderCase{name: v + " Header reference to a target holding one", key: "target", ok: okBody, body: firstMedia}, doc: doc, check: headerCheck(true)},
		)
	}
	runShapeCases(t, cases)
}

// describe.go, Param.ContentType: under multipart/form-data it is empty for a
// field whose Encoding sets style, "except for an OpenAPI 3.2 positional part,
// which may still be given as a Part of a type its Encoding lists (see
// Input.Body)"; otherwise it is "its Encoding's, which may be a range or a
// comma-separated list (see Response.Media), or else the default the client
// uses". So a styled positional part describes the contentType its Encoding
// lists, or, listing none, the default its prefixItems schema gives (doc.go,
// Configuration), application/json for an object (OAS 3.2.1, Encoding Object).
// A Part's type must match a listed type or range, any concrete type where none
// is listed (client.go, Part.MediaType), and a mismatch is keyed in
// RequestError.Settings at the part's pointer (errors.go).
func TestBundleHeadersPositionalStyledContentType(t *testing.T) {
	schema := `"schema":{"type":"array","prefixItems":[{"type":"object","properties":{"a":{"type":"string"}}}]}`
	encodings := []struct{ name, key, encoding, contentType, accepted, refused string }{
		{"text/plain", "plain", `{"style":"form","contentType":"text/plain"}`, "text/plain", "text/plain", "image/png"},
		{"image/*", "range", `{"style":"form","contentType":"image/*"}`, "image/*", "image/png", "text/plain"},
		{"no contentType", "none", `{"style":"form"}`, "application/json", "image/png", ""},
	}
	var paths []string
	for _, e := range encodings {
		paths = append(paths, `"/`+e.key+`":{"post":{"operationId":"`+e.key+`","requestBody":{"content":{"multipart/form-data":{`+schema+`,"prefixEncoding":[`+e.encoding+`]}}},`+partErrResponse+`}}`)
	}
	doc := shapeDoc("3.2.1", strings.Join(paths, ","))
	part := func(mediaType string) *openapi.Input {
		return &openapi.Input{Body: []any{openapi.Part{Header: http.Header{"Content-Disposition": {`form-data; name="n"`}}, MediaType: mediaType, Content: []byte("v")}}}
	}
	var cases []shapeCase
	for _, e := range encodings {
		sc := shapeCase{orderCase: orderCase{name: e.name, key: e.key, ok: part(e.accepted), body: firstMedia}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				p := paramByName(mediaAt(0)(op).Encoding, "0")
				if p == nil {
					t.Fatal("no part 0")
				}
				wantUsable(t, "part 0", p.Err)
				if p.Style != "form" || p.ContentType != e.contentType {
					t.Errorf("part 0 Style %q ContentType %q; want form and %q", p.Style, p.ContentType, e.contentType)
				}
			}}
		if e.refused != "" {
			sc.bad = []refusal{{"a Part of type " + e.refused, part(e.refused), "Settings", "Input.Body/0", nil}}
		}
		cases = append(cases, sc)
	}
	runShapeCases(t, cases)
}
