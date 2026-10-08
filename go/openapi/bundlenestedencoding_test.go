package openapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// bigFragment is a document that is not OpenAPI, with no $id and only local
// references: defs.F is an object schema with n properties, the first a
// reference to defs.G, written as JSON or, with yaml, as block YAML.
func bigFragment(n int, yaml bool) string {
	var b strings.Builder
	if yaml {
		b.WriteString("defs:\n  G:\n    type: string\n  F:\n    type: object\n    properties:\n      p0:\n        $ref: '#/defs/G'\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "      p%d: {type: string}\n", i)
		}
		return b.String()
	}
	b.WriteString(`{"defs":{"G":{"type":"string"},"F":{"type":"object","properties":{"p0":{"$ref":"#/defs/G"}`)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, `,"p%d":{"type":"string"}`, i)
	}
	b.WriteString(`}}}}`)
	return b.String()
}

// A schema reference into a large document that is not OpenAPI, one whose
// schema graph has more than 65,536 nodes, is followed as any other: the
// operation is described and prepared, Schema.References "lists the
// references in Raw" (describe.go), the target's own included, and
// Client.Schema "returns the Schema Object identified by an absolute URI in
// the loaded graph" (describe.go), each returning normally, for the document
// written as JSON or as YAML (load.go, Loader: "anything else is read as
// YAML").
func TestLargeNonOpenAPIFragmentReference(t *testing.T) {
	const properties = 70000
	for _, yaml := range []bool{false, true} {
		name, file := "JSON", "frag.json"
		if yaml {
			name, file = "YAML", "frag.yaml"
		}
		t.Run(name, func(t *testing.T) {
			frag := bigFragment(properties, yaml)
			fragURI := "https://api.example.test/" + file
			doc := shapeDoc("3.1.2", `"/a":{"post":{"operationId":"a","requestBody":{"content":{"application/json":{"schema":{"$ref":"`+file+`#/defs/F"}}}},`+partErrResponse+`}}`)
			l := &openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
				if u == fragURI {
					return io.NopCloser(strings.NewReader(frag)), "", nil
				}
				return nil, "", fmt.Errorf("no document %s", u)
			}}
			var c *openapi.Client
			noPanic(t, "Parse", func() {
				var err error
				if c, err = l.Parse(t.Context(), []byte(doc), testDocURI, nil); err != nil {
					t.Fatalf("Parse: %v", err)
				}
			})
			if c == nil {
				return
			}
			var op *openapi.Operation
			noPanic(t, "Operation", func() { op = mustOp(t, c, "a") })
			if op == nil {
				return
			}
			wantUsable(t, "Operation", op.Err)
			noPanic(t, "Prepare", func() { mustPrepare(t, c, "a", &openapi.Input{Body: map[string]string{"p1": "x"}}) })
			m := mediaAt(0)(op)
			if m == nil || m.Schema == nil {
				t.Fatalf("Media %+v; want one with a Schema", m)
			}
			noPanic(t, "References of the body schema", func() {
				refs, err := m.Schema.References()
				if err != nil || len(refs) != 1 || refs[0].Err != nil || refs[0].Target == nil {
					t.Fatalf("References = %+v, %v; want the reference to F, resolved", refs, err)
				}
				if got := refs[0].Target.Source(); got != fragURI+"#/defs/F" {
					t.Errorf("Target.Source() = %q; want %q", got, fragURI+"#/defs/F")
				}
				inner, err := refs[0].Target.References()
				if err != nil || len(inner) != 1 || inner[0].Value != "#/defs/G" || inner[0].Err != nil || inner[0].Target == nil {
					t.Errorf("References of F = %+v, %v; want the reference to G, resolved", inner, err)
				}
			})
			noPanic(t, "Client.Schema", func() {
				s, err := c.Schema(fragURI + "#/defs/F")
				if err != nil || s == nil || s.Source() != fragURI+"#/defs/F" {
					t.Errorf("Client.Schema = %v, %v; want F", s != nil, err)
				}
			})
		})
	}
}

// OAS 3.2.1, Encoding Object: its encoding "Applies nested Encoding Objects
// in the same manner as the Media Type Object's encoding field", which
// "SHALL only apply when the media type is multipart or
// application/x-www-form-urlencoded". A part whose contentType is
// application/x-www-form-urlencoded with a charset parameter is still of that
// media type (a media type is its type and subtype; RFC 9110 section 8.3.1),
// so a nested encoding map written as a reference applies to it, and, having
// no descriptor nearer than the field, makes the field's Encoding Param
// unusable (describe.go, Operation); a call using the field is refused at
// its pointer wrapping that Err, and one without it is sent.
func TestBundleNestedFormEncodingWithCharset(t *testing.T) {
	schema := `"schema":{"type":"object","properties":{"f":{"type":"object","properties":{"a":{"type":"string"}}},"ok":{"type":"string"}}}`
	field := func(ctype string) string {
		return `{"post":{"operationId":"op","requestBody":{"content":{"multipart/form-data":{` + schema + `,
			"encoding":{"f":{"contentType":"` + ctype + `","encoding":{"$ref":"e.yaml"}}}}}},` + partErrResponse + `}}`
	}
	var cases []shapeCase
	for _, ctype := range []string{"application/x-www-form-urlencoded", "application/x-www-form-urlencoded; charset=utf-8"} {
		cases = append(cases, shapeCase{orderCase: orderCase{name: ctype, key: "op",
			ok: &openapi.Input{Body: map[string]any{"ok": "x"}}, body: firstMedia,
			bad: []refusal{{"a value for f", &openapi.Input{Body: map[string]any{"f": map[string]string{"a": "x"}}}, "Inputs", "Input.Body/f", fieldNamed("f")}}},
			doc: shapeDoc("3.2.1", `"/op":`+field(ctype)),
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantUsable(t, "Media", mediaErrAt(0)(op))
				wantBundled(t, "field f", fieldNamed("f")(op))
				wantUsable(t, "field ok", fieldNamed("ok")(op))
			}})
	}
	runShapeCases(t, cases)
}

// A multipart field whose Encoding lists no contentType may be sent as any
// concrete type its caller names in Part.MediaType (client.go, Part.MediaType:
// "where the Encoding lists no type ..., any concrete type"), and a part whose
// media type is multipart "is encoded, one level deep, from an object or
// slice by its Encoding's own encoding, prefixEncoding or itemEncoding"
// (client.go, Input.Body). So its prefixEncoding is one the client can read
// for a type the field can be sent as, and written as a reference it "makes
// the nearest part holding that value unusable" (describe.go, Operation): the
// field's Encoding Param.Err says to bundle. Every call using the field is
// refused at its pointer wrapping that Err, as multipart/mixed or as its
// default type, and a call without it is sent.
func TestBundleUntypedFieldPrefixEncoding(t *testing.T) {
	doc := shapeDoc("3.2.1", `"/any":{"post":{"operationId":"any","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"meta":{},"ok":{"type":"string"}}},
		"encoding":{"meta":{"prefixEncoding":{"$ref":"e.yaml"}}}}}},`+partErrResponse+`}}`)
	runShapeCases(t, []shapeCase{{
		orderCase: orderCase{name: "3.2.1", key: "any", ok: &openapi.Input{Body: map[string]any{"ok": "x"}}, body: firstMedia,
			bad: []refusal{
				{"the field as multipart/mixed", &openapi.Input{Body: map[string]any{"meta": openapi.Part{MediaType: "multipart/mixed", Content: []any{"p", "q"}}}},
					"Inputs", "Input.Body/meta", fieldNamed("meta")},
				{"the field as its default type", &openapi.Input{Body: map[string]any{"meta": "x"}}, "Inputs", "Input.Body/meta", fieldNamed("meta")},
			}},
		doc: doc,
		check: func(t *testing.T, op *openapi.Operation) {
			wantUsable(t, "Operation", op.Err)
			wantUsable(t, "Media", mediaErrAt(0)(op))
			wantBundled(t, "field meta", fieldNamed("meta")(op))
			wantUsable(t, "field ok", fieldNamed("ok")(op))
		},
	}})
}

// yamlAliasDoc is a YAML OpenAPI document with k flow mappings of 17 members,
// one a string $ref, then a list of a aliases of one scalar.
func yamlAliasDoc(k, a int) []byte {
	var b strings.Builder
	b.WriteString("openapi: 3.1.2\ninfo: {title: t, version: \"1\"}\npaths: {}\nx-s: &s v\nx-big:\n")
	for range k {
		b.WriteString("  - {$ref: r, a: 1, b: 1, c: 1, d: 1, e: 1, f: 1, g: 1, h: 1, i: 1, j: 1, k: 1, l: 1, m: 1, n: 1, o: 1, p: 1}\n")
	}
	b.WriteString("x-al: [")
	for i := range a {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("*s")
	}
	b.WriteString("]\n")
	return []byte(b.String())
}

// Regression check, not contract: what each YAML alias costs does not grow
// with the rest of the document. load.go, Loader bounds aliases (a document
// "whose aliases would add more than 1,000,000 nodes, more than 100 times its
// own node count, or more than 100 times its own size in bytes ... is rejected
// too"); within those bounds, with the aliases fixed, sixteen times the
// mappings must cost about the same (wantFlat, the convention of
// scaling_test.go; the mappings themselves are small beside the aliases, whose
// time is not checked under -short).
func TestYAMLAliasCostIndependentOfMappings(t *testing.T) {
	const aliases = 200000
	wantFlat(t, "aliases beside more mappings with a $ref", 250, func(k int) func() {
		doc := yamlAliasDoc(k, aliases)
		return func() {
			if _, err := openapi.Parse(context.Background(), doc, testDocURI, nil); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// A field whose Encoding sets style, explode or allowReserved is written by
// its style under application/x-www-form-urlencoded and multipart/form-data,
// where OpenAPI ignores its contentType, and so its nested encodings and
// headers (describe.go, Param.ContentType and Param.Headers: "the headers its
// Encoding declares are never read"). Written as a reference, they still
// count: describe.go, Operation, "A value so written makes its part unusable
// even where OpenAPI says the value is ignored, such as ... an Encoding
// Object's headers for a field written by its style, since the document still
// needs bundling". So under every type the field's Encoding Param.Err says to
// bundle, a call using it is refused at its pointer wrapping that Err, and a
// call without it is sent.
func TestBundleStyledFieldReferences(t *testing.T) {
	schema := `"schema":{"type":"object","properties":{"s":{"type":"object","properties":{"a":{"type":"string"}}},"ok":{"type":"string"}}}`
	types := []string{"application/x-www-form-urlencoded", "multipart/form-data", "multipart/mixed"}
	variants := []struct{ name, key, encoding string }{
		{"headers", "headers", `{"style":"form","explode":true,"headers":{"$ref":"h.yaml"}}`},
		{"nested encoding", "nested", `{"style":"form","contentType":"multipart/mixed","encoding":{"$ref":"e.yaml"}}`},
		{"deepObject, nested encoding", "deep", `{"style":"deepObject","contentType":"multipart/mixed","encoding":{"$ref":"e.yaml"}}`},
	}
	var paths []string
	for _, v := range variants {
		var content []string
		for _, typ := range types {
			content = append(content, `"`+typ+`":{`+schema+`,"encoding":{"s":`+v.encoding+`}}`)
		}
		paths = append(paths, `"/`+v.key+`":{"post":{"operationId":"`+v.key+`","requestBody":{"content":{`+strings.Join(content, ",")+`}},`+partErrResponse+`}}`)
	}
	doc := shapeDoc("3.2.1", strings.Join(paths, ","))
	var cases []shapeCase
	for _, v := range variants {
		for i, typ := range types {
			cases = append(cases, shapeCase{orderCase: orderCase{name: v.name + ", " + typ, key: v.key,
				ok: &openapi.Input{Body: map[string]any{"ok": "x"}, MediaType: typ}, body: mediaAt(i),
				bad: []refusal{{"the field", &openapi.Input{Body: map[string]any{"s": map[string]string{"a": "x"}}, MediaType: typ}, "Inputs", "Input.Body/s", encodingErr(i, "s")}}},
				doc: doc,
				check: func(t *testing.T, op *openapi.Operation) {
					wantUsable(t, "Operation", op.Err)
					if m := mediaAt(i)(op); m == nil || m.Type != typ {
						t.Fatalf("Media %d %+v; want %s", i, m, typ)
					}
					wantUsable(t, "Media "+typ, mediaErrAt(i)(op))
					wantBundled(t, typ+" field s", encodingErr(i, "s")(op))
				}})
		}
	}
	runShapeCases(t, cases)
}

// "A value so written makes its part unusable even where OpenAPI says the
// value is ignored" (describe.go, Operation), at any depth of a field's
// Encoding Object: a prefixEncoding, an itemEncoding, or a nested Encoding's
// headers, under a form or multipart body, whatever contentType the field
// lists, and references two levels into a field's nested encodings. Each makes
// the field's Encoding Param unusable, its nearest part; a call using the
// field is refused at its pointer wrapping that Err, and a call without it is
// sent.
func TestBundleReferencesAnyEncodingDepth(t *testing.T) {
	schema := `"schema":{"type":"object","properties":{"f":{"type":"object","properties":{"x":{"type":"string"},"a":{"type":"string"}}},"ok":{"type":"string"}}}`
	type variant struct{ name, media, encoding string }
	var variants []variant
	for _, media := range []string{"application/x-www-form-urlencoded", "multipart/form-data"} {
		for _, e := range []struct{ name, encoding string }{
			{"prefixEncoding", `{"prefixEncoding":{"$ref":"e.yaml"}}`},
			{"itemEncoding", `{"itemEncoding":{"$ref":"e.yaml"}}`},
			{"nested headers", `{"encoding":{"a":{"headers":{"$ref":"h.yaml"}}}}`},
			{"listed types, itemEncoding", `{"contentType":"text/plain, multipart/mixed","itemEncoding":{"$ref":"e.yaml"}}`},
			{"listed types, nested headers", `{"contentType":"text/plain, multipart/mixed","encoding":{"a":{"headers":{"$ref":"h.yaml"}}}}`},
			{"text/plain, itemEncoding", `{"contentType":"text/plain","itemEncoding":{"$ref":"e.yaml"}}`},
		} {
			variants = append(variants, variant{media + ", " + e.name, media, e.encoding})
		}
	}
	variants = append(variants,
		variant{"multipart part's part, nested headers", "multipart/form-data", `{"contentType":"multipart/mixed","encoding":{"x":{"encoding":{"y":{"headers":{"$ref":"h.yaml"}}}}}}`},
		variant{"multipart part's part, prefixEncoding", "multipart/form-data", `{"contentType":"multipart/mixed","encoding":{"x":{"prefixEncoding":{"$ref":"e.yaml"}}}}`},
		variant{"form part's field, prefixEncoding", "multipart/form-data", `{"contentType":"application/x-www-form-urlencoded","encoding":{"x":{"prefixEncoding":{"$ref":"e.yaml"}}}}`},
		variant{"form body field's field, itemEncoding", "application/x-www-form-urlencoded", `{"contentType":"application/x-www-form-urlencoded","encoding":{"x":{"itemEncoding":{"$ref":"e.yaml"}}}}`},
	)
	var paths []string
	for i, v := range variants {
		paths = append(paths, fmt.Sprintf(`"/v%d":{"post":{"operationId":"v%d","requestBody":{"content":{%q:{%s,"encoding":{"f":%s}}}},%s}}`, i, i, v.media, schema, v.encoding, partErrResponse))
	}
	doc := shapeDoc("3.2.1", strings.Join(paths, ","))
	var cases []shapeCase
	for i, v := range variants {
		cases = append(cases, shapeCase{orderCase: orderCase{name: v.name, key: fmt.Sprintf("v%d", i),
			ok: &openapi.Input{Body: map[string]any{"ok": "x"}}, body: firstMedia,
			bad: []refusal{{"a value for f", &openapi.Input{Body: map[string]any{"f": map[string]string{"x": "v"}}}, "Inputs", "Input.Body/f", fieldNamed("f")}}},
			doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantUsable(t, "Media", mediaErrAt(0)(op))
				wantBundled(t, "field f", fieldNamed("f")(op))
			}})
	}
	runShapeCases(t, cases)
}

// nestedCostDoc is a 3.2.1 document with a multipart/form-data field whose
// contentType lists l types and whose Encoding holds m nested entries, in an
// encoding map or, with positional, a prefixEncoding list.
func nestedCostDoc(l, m int, positional bool) []byte {
	types := strings.TrimSuffix(strings.Repeat("multipart/mixed,", l), ",")
	var entries strings.Builder
	for i := range m {
		if i > 0 {
			entries.WriteString(",")
		}
		if positional {
			entries.WriteString(`{}`)
		} else {
			fmt.Fprintf(&entries, `"a%d":{}`, i)
		}
	}
	nested := `"encoding":{` + entries.String() + `}`
	if positional {
		nested = `"prefixEncoding":[` + entries.String() + `]`
	}
	return []byte(shapeDoc("3.2.1", `"/p":{"post":{"operationId":"op","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"f":{"type":"object"}}},
		"encoding":{"f":{"contentType":"`+types+`",`+nested+`}}}}},`+partErrResponse+`}}`))
}

// Regression check, not contract: describing a field reads its contentType list
// and its nested entries once each, so what the entries cost must not grow with
// the list: a cost proportional to the list times the entries is quadratic in
// the document. The measure isolates the entries: for a list of l types, the
// cost of describing the document with its m entries less the cost of the same
// document with none, each the best of interleaved runs (bestCosts, from
// scaling_test.go). The list's own cost, whatever it is, is in both and
// cancels. With the list sixteen times longer, what the entries add must stay
// within 3 times (flatBound, as wantFlat bounds a cost independent of what
// grows), in time, which is not checked under -short, and in bytes allocated. A
// difference below a floor, 1 ms or 16 bytes an entry, counts as the floor, so
// that a cost too small to measure cannot fail the test.
func TestBundleCostNestedEncodingList(t *testing.T) {
	const entries, small = 16000, 100
	// added returns what the entries add to describing, for l listed types.
	added := func(l int, positional bool) (time.Duration, int64) {
		with, without := bestCosts(timedOperations(t, nestedCostDoc(l, entries, positional)), timedOperations(t, nestedCostDoc(l, 0, positional)))
		return with.d - without.d, int64(with.bytes) - int64(without.bytes)
	}
	for _, positional := range []bool{false, true} {
		name := "encoding map"
		if positional {
			name = "prefixEncoding list"
		}
		t.Run("listed types beside a large "+name, func(t *testing.T) {
			ds, bs := added(small, positional)
			dl, bl := added(16*small, positional)
			timeRatio := float64(dl) / float64(max(ds, time.Millisecond))
			byteRatio := float64(bl) / float64(max(bs, 16*entries))
			t.Logf("entries add %v, %d bytes beside %d types; %v, %d bytes beside %d (time %.1fx, bytes %.1fx)", ds, bs, small, dl, bl, 16*small, timeRatio, byteRatio)
			if byteRatio > flatBound {
				t.Errorf("beside sixteen times the types, the entries allocated %.1f times the bytes; want a cost independent of the list", byteRatio)
			}
			if !testing.Short() && timeRatio > flatBound {
				t.Errorf("beside sixteen times the types, the entries took %.1f times as long; want a cost independent of the list", timeRatio)
			}
		})
	}
}

// A positional part of an OpenAPI 3.2 multipart/form-data body may be given as
// a Part whose Header gives Content-Disposition (client.go, Input.Body), and
// Part.MediaType must be "a concrete type matching, by the rules on
// Response.Media, one its Encoding lists"; a part's media type is keyed in
// RequestError.Settings at "Input.Body" followed by the part's pointer
// (errors.go). An Encoding listing text/plain therefore refuses image/png
// and multipart/mixed whether or not it also sets style, as a Part is not
// written by the style. With no Part.MediaType, "Empty uses the part's type
// where one selects itself" (Part.MediaType): the listed text/plain, or, where
// the Encoding lists none, the default the part's prefixItems schema gives
// (doc.go, Configuration), application/json for an object (OAS 3.2.1,
// Encoding Object), so the Part is sent with that type.
func TestPositionalStyledPartMediaType(t *testing.T) {
	schema := `"schema":{"type":"array","prefixItems":[{"type":"object","properties":{"a":{"type":"string"}}}]}`
	encodings := []struct{ name, key, encoding, defaultType string }{
		{"styled, text/plain", "styled", `{"style":"form","contentType":"text/plain"}`, "text/plain"},
		{"text/plain, for contrast", "plain", `{"contentType":"text/plain"}`, "text/plain"},
		{"styled, no contentType", "untyped", `{"style":"form"}`, "application/json"},
	}
	var paths []string
	for _, e := range encodings {
		paths = append(paths, `"/`+e.key+`":{"post":{"operationId":"`+e.key+`","requestBody":{"content":{"multipart/form-data":{`+schema+`,"prefixEncoding":[`+e.encoding+`]}}},`+partErrResponse+`}}`)
	}
	doc := shapeDoc("3.2.1", strings.Join(paths, ","))
	part := func(mediaType string, content any) *openapi.Input {
		return &openapi.Input{Body: []any{openapi.Part{Header: http.Header{"Content-Disposition": {`form-data; name="n"`}}, MediaType: mediaType, Content: content}}}
	}
	var cases []shapeCase
	for _, e := range encodings {
		content := any("v")
		if e.defaultType == "application/json" {
			content = map[string]string{"a": "v"}
		}
		check := func(t *testing.T, op *openapi.Operation) {
			wantUsable(t, "Operation", op.Err)
			wantUsable(t, "part 0", fieldNamed("0")(op))
		}
		cases = append(cases, shapeCase{orderCase: orderCase{name: e.name + ", a Part without MediaType", key: e.key, ok: part("", content), body: firstMedia}, doc: doc, check: check,
			after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
				req := mustPrepare(t, c, op.Key, part("", content))
				_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
				if len(parts) != 1 || parts[0].header.Get("Content-Type") != e.defaultType {
					t.Errorf("parts %+v; want one part of type %s", parts, e.defaultType)
				}
			}})
		if e.defaultType == "text/plain" {
			cases = append(cases, shapeCase{orderCase: orderCase{name: e.name + ", a Part of an unlisted type", key: e.key, bad: []refusal{
				{"image/png", part("image/png", []byte("png")), "Settings", "Input.Body/0", nil},
				{"multipart/mixed", part("multipart/mixed", map[string]string{"a": "v"}), "Settings", "Input.Body/0", nil},
			}}, doc: doc, check: check})
		}
	}
	runShapeCases(t, cases)
}
