package openapi_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Values written as references inside parts the client does not describe:
// Content-Type headers, ignored header parameters, and Encodings reached only
// through headers' content. describe.go, Operation: a value written as a
// reference "makes the nearest part holding that value unusable", "even where
// OpenAPI says the value is ignored", and "The descriptions follow
// references". The harness, and the checks of each call in both orders and
// of nothing being fetched, are those of bundlevalues_test.go.

// nestedHeaderDoc is a 3.1.2 document whose component header C nests l levels
// of inline Header, content multipart/mixed, encoding x, headers h, the
// innermost Media holding w encoding entries; one operation's multipart/mixed
// field f declares headers referring to C and to the inline Header at each of
// the first k levels.
func nestedHeaderDoc(l, k, w int) []byte {
	var b strings.Builder
	const open = `{"content":{"multipart/mixed":{"schema":{"type":"object"},"encoding":{"x":{"headers":{"h":`
	const close = `}}}}}}`
	for range l {
		b.WriteString(open)
	}
	b.WriteString(`{"content":{"multipart/mixed":{"schema":{"type":"object"},"encoding":{`)
	for i := range w {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"e%d":{"contentType":"text/plain"}`, i)
	}
	b.WriteString(`}}}}`)
	for range l {
		b.WriteString(close)
	}
	step := "/content/multipart~1mixed/encoding/x/headers/h"
	headers := []string{`"c":{"$ref":"#/components/headers/C"}`}
	for i := 1; i <= k; i++ {
		headers = append(headers, fmt.Sprintf(`"t%d":{"$ref":"#/components/headers/C%s"}`, i, strings.Repeat(step, i)))
	}
	return []byte(shapeDoc("3.1.2", `"/p":{"post":{"operationId":"op","requestBody":{"content":{"multipart/mixed":{
		"schema":{"type":"object","properties":{"f":{"type":"string"}}},
		"encoding":{"f":{"contentType":"text/plain","headers":{`+strings.Join(headers, ",")+`}}}}}},`+partErrResponse+`}}`,
		`"components":{"headers":{"C":`+b.String()+`}}`))
}

// Describing reads each object of the document a bounded number of times, so
// describing is linear in the document even where references reach the same
// nested Encodings from many places. The measure isolates the innermost w
// entries: the cost of describing with them less the cost of the same
// document without them, each the best of interleaved runs (bestCosts, from
// scaling_test.go). Read once, they add the same whatever refers to
// them; read once for each reference, they add in proportion to the
// references. With sixteen times the references, what the entries add must
// stay within 3 times (flatBound), in time, not checked under -short, and in
// bytes; a difference below a floor, 1 ms or 16 bytes an entry, counts as the
// floor.
func TestBundleCostNestedHeaderReferences(t *testing.T) {
	const levels, entries, small = 150, 20000, 9
	added := func(k int) (time.Duration, int64) {
		with, without := bestCosts(timedOperations(t, nestedHeaderDoc(levels, k, entries)), timedOperations(t, nestedHeaderDoc(levels, k, 0)))
		return with.d - without.d, int64(with.bytes) - int64(without.bytes)
	}
	ds, bs := added(small)
	dl, bl := added(16 * small)
	timeRatio := float64(dl) / float64(max(ds, time.Millisecond))
	byteRatio := float64(bl) / float64(max(bs, 16*entries))
	t.Logf("entries add %v, %d bytes beside %d references; %v, %d bytes beside %d (time %.1fx, bytes %.1fx)", ds, bs, small, dl, bl, 16*small, timeRatio, byteRatio)
	if byteRatio > flatBound {
		t.Errorf("beside sixteen times the references, the entries allocated %.1f times the bytes; want them read once", byteRatio)
	}
	if !testing.Short() && timeRatio > flatBound {
		t.Errorf("beside sixteen times the references, the entries took %.1f times as long; want them read once", timeRatio)
	}
}

// contentTypeHeaders are Encoding headers maps whose Content-Type header holds
// a value written as a reference: inline, as a Header reference to a
// component holding one, and that reference under a lowercase name. Header
// names compare without regard to case (RFC 9110 section 5.1).
var contentTypeHeaders = []struct{ name, headers string }{
	{"inline", `{"Content-Type":{"content":{"$ref":"c.yaml"}},"X-B":{"schema":{"type":"string"}}}`},
	{"Header reference", `{"Content-Type":{"$ref":"#/components/headers/H2"},"X-B":{"schema":{"type":"string"}}}`},
	{"lowercase, Header reference", `{"content-type":{"$ref":"#/components/headers/H2"},"X-B":{"schema":{"type":"string"}}}`},
}

// holdingComponent is a component header whose content map is written as a
// reference.
const holdingComponent = `"components":{"headers":{"H2":{"content":{"$ref":"c.yaml"}}}}`

// A multipart field's Headers list what its Encoding declares "except
// Content-Type, which OpenAPI ignores there" (describe.go, Param.Headers), so
// a Content-Type header holding a value written as a reference has no Param
// of its own, and the nearest part is the field's Encoding Param: its Err
// says to bundle, other declared headers are still described, and a call
// using the field is refused at its pointer wrapping the Err, while one
// without it is sent. This holds for a field of a multipart/mixed body, an
// unstyled field of a multipart/form-data body, and an OpenAPI 3.2 positional
// part.
func TestBundleContentTypeHeaderInPart(t *testing.T) {
	schema := `"schema":{"type":"object","properties":{"f":{"type":"string"},"g":{"type":"string"}}}`
	var cases []shapeCase
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		for _, hv := range contentTypeHeaders {
			enc := `{"contentType":"text/plain","headers":` + hv.headers + `}`
			paths := []string{
				`"/mixed":{"post":{"operationId":"mixed","requestBody":{"content":{"multipart/mixed":{` + schema + `,"encoding":{"f":` + enc + `}}}},` + partErrResponse + `}}`,
				`"/form":{"post":{"operationId":"form","requestBody":{"content":{"multipart/form-data":{` + schema + `,"encoding":{"f":` + enc + `}}}},` + partErrResponse + `}}`,
			}
			if v == "3.2.1" {
				paths = append(paths, `"/positional":{"post":{"operationId":"positional","requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"prefixEncoding":[`+enc+`]}}},`+partErrResponse+`}}`)
			}
			doc := shapeDoc(v, strings.Join(paths, ","), holdingComponent)
			check := func(name string) func(*testing.T, *openapi.Operation) {
				return func(t *testing.T, op *openapi.Operation) {
					wantUsable(t, "Operation", op.Err)
					wantUsable(t, "Media", mediaErrAt(0)(op))
					p := paramByName(mediaAt(0)(op).Encoding, name)
					if p == nil {
						t.Fatalf("no field %s", name)
					}
					wantBundled(t, "field "+name, p.Err)
					var names []string
					for _, h := range p.Headers {
						names = append(names, h.Name)
					}
					if len(names) != 1 || names[0] != "X-B" {
						t.Errorf("field %s Headers %q; want X-B alone", name, names)
					}
				}
			}
			prefix := v + " " + hv.name + ", "
			for _, key := range []string{"mixed", "form"} {
				cases = append(cases, shapeCase{orderCase: orderCase{name: prefix + key, key: key,
					ok: &openapi.Input{Body: map[string]any{"g": "x"}}, body: firstMedia,
					bad: []refusal{{"a value for f", &openapi.Input{Body: map[string]any{"f": "x"}}, "Inputs", "Input.Body/f", fieldNamed("f")}}},
					doc: doc, check: check("f")})
			}
			if v == "3.2.1" {
				cases = append(cases, shapeCase{orderCase: orderCase{name: prefix + "positional part", key: "positional",
					ok: &openapi.Input{Body: []byte(rawMixed), MediaType: "multipart/mixed; boundary=b"}, body: firstMedia,
					bad: []refusal{{"a value for part 0", &openapi.Input{Body: []any{"x"}}, "Inputs", "Input.Body/0", fieldNamed("0")}}},
					doc: doc, check: check("0")})
			}
		}
	}
	runShapeCases(t, cases)
}

// A response's Headers list its declared header fields "without the
// Content-Type that OpenAPI 3.x ignores" (describe.go, Message.Headers), so a
// Content-Type response header holding a value written as a reference, inline
// or through a Header reference, has no Param, and the nearest part is the
// response: its Message.Err says to bundle, its other headers are described,
// and the request, of which the response is no part, is sent.
func TestBundleContentTypeHeaderInResponse(t *testing.T) {
	var cases []shapeCase
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		for _, hv := range contentTypeHeaders {
			doc := shapeDoc(v, `"/r":{"get":{"operationId":"r","responses":{"200":{"description":"ok","headers":`+hv.headers+`,
				"content":{"application/json":{"schema":{"type":"object"}}}}}}}`, holdingComponent)
			cases = append(cases, shapeCase{orderCase: orderCase{name: v + " " + hv.name, key: "r"}, doc: doc,
				check: func(t *testing.T, op *openapi.Operation) {
					wantUsable(t, "Operation", op.Err)
					m := respByKey(op, "200")
					if m == nil {
						t.Fatal("no response 200")
					}
					wantBundled(t, "response 200", m.Err)
					if len(m.Headers) != 1 || m.Headers[0].Name != "X-B" {
						t.Errorf("response Headers %q; want X-B alone", paramNames(m.Headers))
					}
				},
				after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) { sendDeclared(t, c, op, nil, "200") }})
		}
	}
	runShapeCases(t, cases)
}

// Operation.Params holds "no header parameter that OpenAPI 3.x tells clients
// to ignore (Accept, Content-Type, Authorization, matched without regard to
// case)" (describe.go), so such a parameter has no Param, and a value written
// as a reference in its content, here its encoding map or a Header reference
// in an encoding entry to a component holding one, is held by nothing nearer
// than the operation: Operation.Err says to bundle ("another value whose
// nearest part is the operation"), and every call is refused wrapping it.
func TestBundleIgnoredHeaderParameter(t *testing.T) {
	var cases []shapeCase
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		var paths, keys, names []string
		for i, name := range []string{"Accept", "Content-Type", "Authorization", "accept"} {
			for j, enc := range []struct{ name, encoding string }{
				{"encoding map", `{"$ref":"e.yaml"}`},
				{"Header reference in an entry", `{"f":{"headers":{"h":{"$ref":"#/components/headers/H2"}}}}`},
			} {
				key := fmt.Sprintf("p%d%d", i, j)
				keys, names = append(keys, key), append(names, name+", "+enc.name)
				paths = append(paths, fmt.Sprintf(`"/%s":{"get":{"operationId":%q,"parameters":[{"name":%q,"in":"header","content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"f":{"type":"string"}}},"encoding":%s}}}],%s}}`,
					key, key, name, enc.encoding, partErrResponse))
			}
		}
		doc := shapeDoc(v, strings.Join(paths, ","), holdingComponent)
		for i, key := range keys {
			cases = append(cases, shapeCase{orderCase: orderCase{name: v + " " + names[i], key: key, bad: []refusal{{"a call", nil, "Err", "", opErr}}}, doc: doc,
				check: func(t *testing.T, op *openapi.Operation) {
					wantBundled(t, "Operation", op.Err)
					if len(op.Params) != 0 {
						t.Errorf("Params %q; want the ignored header parameter not described", paramNames(op.Params))
					}
				}})
		}
	}
	runShapeCases(t, cases)
}
