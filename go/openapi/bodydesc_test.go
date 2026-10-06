package openapi_test

import (
	"errors"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Request-body descriptors. describe.go, Media.Encoding: "Encoding describes
// the fields of form or multipart content, as Params whose Name is the
// field, each with its effective ContentType: the properties the schema
// lists at its top level (after following $ref), in document order, then
// those that only declare an Encoding Object, in the encoding map's order".
// Param: "a field of a form or multipart body ... (In and Key are empty)";
// Style "as declared or as OpenAPI defaults it ..., or empty when the value
// is serialized by ContentType instead"; Explode "the effective explode
// (true for deepObject, which ignores the field), and ExplodeSet whether the
// document writes it"; AllowReserved "the effective allowReserved: false
// where the edition or the media type ignores it, as for a
// multipart/form-data field"; ContentType "for a form or multipart field,
// its effective contentType: its Encoding's, which may be a comma-separated
// list or a range, or else the default the client uses ... Under
// application/x-www-form-urlencoded and multipart/form-data it is empty for
// a field whose Encoding sets style, explode or allowReserved, which OpenAPI
// says makes contentType ignored there"; Headers "the header fields a multipart field's Encoding
// declares for its part, except Content-Type, which OpenAPI ignores there";
// Source "a JSON Pointer to its ... Encoding Object"; Err "why built-in
// serialization cannot use the value". Media.Err: "A schema or Encoding
// defect that affects structured value encoding does not set Media.Err: it
// is reported by ... the relevant Encoding Param.Err." Media.ItemSchema: "in
// OpenAPI 3.2, the schema of each item of sequential content, or nil". OAS
// 3.1.2 section 4.8.14.1, encoding: "The encoding field SHALL only apply to
// Request Body Objects, and only when the media type is multipart or
// application/x-www-form-urlencoded"; section 4.8.15.1.1, headers:
// "Content-Type is described separately and SHALL be ignored in this
// section. This field SHALL be ignored if the request body media type is
// not a multipart"; section 4.8.15.1.2, style "follows the same values as
// query parameters, including the default value of "form" which applies
// only when contentType is not being used due to one or both of explode or
// allowReserved being explicitly specified".

const descPaths = `
	"/a":{"post":{"operationId":"schemaOnly","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{
		"s":{"type":"string"},
		"i":{"type":"integer"},
		"x":{"type":"number"},
		"b":{"type":"boolean"},
		"o":{"type":"object"},
		"none":{},
		"enc":{"type":"string","contentEncoding":"base64"},
		"as":{"type":"array","items":{"type":"string"}},
		"ao":{"type":"array","items":{"$ref":"#/components/schemas/Obj"}},
		"ae":{"type":"array","items":{}},
		"ref":{"$ref":"#/components/schemas/Obj"}}}}}}}},
	"/b":{"post":{"operationId":"encodingOnly","requestBody":{"content":{"multipart/form-data":{"encoding":{
		"z":{"contentType":"image/png, image/jpeg","headers":{"X-Rate-Limit-Limit":{"description":"limit","schema":{"type":"integer"}},"Content-Type":{"schema":{}},"X-Other":{"schema":{}}}},
		"y":{"contentType":"application/xml; charset=utf-8"},
		"x":{"contentType":"image/*"}}}}}}},
	"/c":{"post":{"operationId":"both","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"id":{"type":"string"},"file":{}}},
		"encoding":{"file":{"contentType":"application/pdf"}}}}}}},
	"/d":{"post":{"operationId":"styles","requestBody":{"content":{
		"application/x-www-form-urlencoded":{"encoding":{
			"s1":{"style":"form","explode":false},
			"s2":{"explode":true},
			"s3":{"allowReserved":true},
			"s4":{"style":"deepObject"},
			"s5":{"style":"spaceDelimited"},
			"s6":{"style":"pipeDelimited","explode":false},
			"bad1":{"style":"matrix"},
			"bad2":{"style":"spaceDelimited","explode":true},
			"ct":{"contentType":"application/json"},
			"hdr":{"contentType":"text/plain","headers":{"X-A":{"schema":{}}}},
			"badct":{"contentType":"not a media type"}}},
		"multipart/form-data":{"encoding":{"m1":{"style":"form","explode":false}}}}}}},
	"/m":{"post":{"operationId":"mixedOrder","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"b":{"type":"string"},"a":{}}},
		"encoding":{"z":{"contentType":"text/csv"},"a":{"contentType":"image/png"},"y":{"headers":{"content-type":{"schema":{}}}}}}}}}},
	"/e":{"post":{"operationId":"json","requestBody":{"content":{"application/json":{
		"schema":{"type":"object","properties":{"a":{}}},"encoding":{"a":{"contentType":"text/plain"}}}}}}},
	"/f":{"post":{"operationId":"refSchema","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"$ref":"#/components/schemas/Upload"}}}}}},
	"/g":{"post":{"operationId":"itemSchema","requestBody":{"content":{"application/jsonl":{"schema":{"type":"array"},"itemSchema":{"type":"object"}}}}}},
	"/h":{"post":{"operationId":"useBad","requestBody":{"content":{"application/x-www-form-urlencoded":{"encoding":{
		"bad1":{"style":"matrix"},"ok":{"contentType":"text/plain"}}}}}}}`

const descComponents = `"components":{"schemas":{"Obj":{"type":"object"},"Upload":{"type":"object","properties":{"p":{"type":"string"},"q":{"type":"integer"}}}}}`

// encodingByName returns m's Encoding Params by name, failing on a
// duplicate.
func encodingByName(t *testing.T, m *openapi.Media) map[string]*openapi.Param {
	t.Helper()
	byName := map[string]*openapi.Param{}
	for _, e := range m.Encoding {
		if _, dup := byName[e.Name]; dup {
			t.Errorf("Encoding lists %q twice", e.Name)
		}
		byName[e.Name] = e
	}
	return byName
}

// encodingNames returns the names of m's Encoding Params in order.
func encodingNames(m *openapi.Media) []string {
	var names []string
	for _, e := range m.Encoding {
		names = append(names, e.Name)
	}
	return names
}

// Fields a schema lists, with no encoding map: in property order, each
// with the default ContentType OAS 3.1.2 section 4.8.15.1.1 gives, a $ref
// followed (its 4.8.15.3.1 example: items that $ref an object schema are
// application/json), serialized by ContentType, not a style.
func TestEncodingDescriptorsFromSchema(t *testing.T) {
	c := parseAt(t, doc31(descPaths, descComponents), "https://api.example.test", testDocURI, nil)
	m := reqMedia(t, mustOp(t, c, "schemaOnly"), 0)
	want := []struct{ name, ct string }{
		{"s", "text/plain"},
		{"i", "text/plain"},
		{"x", "text/plain"},
		{"b", "text/plain"},
		{"o", "application/json"},
		{"none", "application/octet-stream"},
		{"enc", "application/octet-stream"},
		{"as", "text/plain"},
		{"ao", "application/json"},
		{"ae", "application/octet-stream"},
		{"ref", "application/json"},
	}
	if len(m.Encoding) != len(want) {
		t.Fatalf("Encoding %q, want %d fields", encodingNames(m), len(want))
	}
	for i, w := range want {
		e := m.Encoding[i]
		if e.Name != w.name || e.ContentType != w.ct {
			t.Errorf("Encoding[%d] = %q %q, want %q %q", i, e.Name, e.ContentType, w.name, w.ct)
		}
		if e.In != "" || e.Key != "" || e.Style != "" || e.Err != nil || len(e.Headers) != 0 {
			t.Errorf("%s: In %q, Key %q, Style %q, Err %v, %d Headers", e.Name, e.In, e.Key, e.Style, e.Err, len(e.Headers))
		}
	}
	if s := m.Encoding[0].Schema; s == nil || compact(t, s.Raw()) != `{"type":"string"}` {
		t.Errorf("Encoding[0].Schema = %v, want the property's schema", s)
	}
	if m.Err != nil {
		t.Errorf("Media.Err = %v", m.Err)
	}
}

// Fields from an encoding map alone: in its order, ContentType as written
// (a list or a range included), Headers in document order with their
// descriptions, and Sources pointing at the Encoding and Header Objects.
func TestEncodingDescriptorsFromEncoding(t *testing.T) {
	c := parseAt(t, doc31(descPaths, descComponents), "https://api.example.test", testDocURI, nil)
	m := reqMedia(t, mustOp(t, c, "encodingOnly"), 0)
	want := []struct{ name, ct string }{
		{"z", "image/png, image/jpeg"},
		{"y", "application/xml; charset=utf-8"},
		{"x", "image/*"},
	}
	if len(m.Encoding) != len(want) {
		t.Fatalf("Encoding %q, want %d fields", encodingNames(m), len(want))
	}
	for i, w := range want {
		e := m.Encoding[i]
		if e.Name != w.name || e.ContentType != w.ct || e.Err != nil {
			t.Errorf("Encoding[%d] = %q %q (Err %v), want %q %q", i, e.Name, e.ContentType, e.Err, w.name, w.ct)
		}
		if src := m.Source + "/encoding/" + w.name; e.Source != src {
			t.Errorf("Encoding[%d].Source = %q, want %q", i, e.Source, src)
		}
	}
	z := m.Encoding[0]
	if len(z.Headers) != 2 || z.Headers[0].Name != "X-Rate-Limit-Limit" || z.Headers[1].Name != "X-Other" {
		t.Fatalf("z Headers = %v, want X-Rate-Limit-Limit and X-Other, without Content-Type", z.Headers)
	}
	if h := z.Headers[0]; h.Description != "limit" || h.Source != z.Source+"/headers/X-Rate-Limit-Limit" {
		t.Errorf("header: Description %q, Source %q", h.Description, h.Source)
	}
	if s := z.Headers[0].Schema; s == nil || compact(t, s.Raw()) != `{"type":"integer"}` {
		t.Errorf("header Schema = %v", s)
	}

	both := reqMedia(t, mustOp(t, c, "both"), 0)
	byName := encodingByName(t, both)
	if names := encodingNames(both); len(names) != 2 || names[0] != "id" || names[1] != "file" {
		t.Fatalf("Encoding %q, want id and file, the schema's order", names)
	}
	if byName["id"].ContentType != "text/plain" || byName["file"].ContentType != "application/pdf" {
		t.Errorf("ContentType id %q, file %q", byName["id"].ContentType, byName["file"].ContentType)
	}
	if src := both.Source + "/encoding/file"; byName["file"].Source != src {
		t.Errorf("file Source = %q, want %q", byName["file"].Source, src)
	}

	// The schema's properties after following its $ref.
	ref := reqMedia(t, mustOp(t, c, "refSchema"), 0)
	if names := encodingNames(ref); len(names) != 2 || names[0] != "p" || names[1] != "q" ||
		ref.Encoding[0].ContentType != "text/plain" || ref.Encoding[1].ContentType != "text/plain" {
		t.Errorf("Encoding through $ref: %q", names)
	}

	// The schema's properties in document order, then the names only the
	// encoding map has, in its order; Content-Type among an Encoding's
	// headers, in any spelling, is left out.
	mixed := reqMedia(t, mustOp(t, c, "mixedOrder"), 0)
	if names := encodingNames(mixed); len(names) != 4 || names[0] != "b" || names[1] != "a" || names[2] != "z" || names[3] != "y" {
		t.Errorf("Encoding %q, want b, a, z, y", names)
	} else {
		if mixed.Encoding[0].ContentType != "text/plain" || mixed.Encoding[1].ContentType != "image/png" || mixed.Encoding[2].ContentType != "text/csv" {
			t.Errorf("ContentTypes %q, %q, %q", mixed.Encoding[0].ContentType, mixed.Encoding[1].ContentType, mixed.Encoding[2].ContentType)
		}
		if h := mixed.Encoding[3].Headers; len(h) != 0 {
			t.Errorf("y Headers = %v, want none (content-type left out)", h)
		}
	}

	// Encoding is for form and multipart content only.
	if j := reqMedia(t, mustOp(t, c, "json"), 0); len(j.Encoding) != 0 {
		t.Errorf("application/json Encoding = %q, want none", encodingNames(j))
	}
}

// RFC 6570 fields: effective style, explode and allowReserved, as for
// query parameters (OAS 3.1.2 section 4.8.15.1.2; describe.go, Param);
// defects on the field's Err, never Media.Err, and never a "not
// implemented" (an undefined combination is a defect of the document);
// headers ignored outside multipart.
func TestEncodingDescriptorStyles(t *testing.T) {
	c := parseAt(t, doc31(descPaths, descComponents), "https://api.example.test", testDocURI, nil)
	op := mustOp(t, c, "styles")
	form, multi := reqMedia(t, op, 0), reqMedia(t, op, 1)
	byName := encodingByName(t, form)
	type desc struct {
		style                             string
		explode, explodeSet, allowReserve bool
	}
	for name, d := range map[string]desc{
		"s1": {"form", false, true, false},
		"s2": {"form", true, true, false},
		"s3": {"form", true, false, true},
		"s4": {"deepObject", true, false, false},
		"s5": {"spaceDelimited", false, false, false},
		"s6": {"pipeDelimited", false, true, false},
	} {
		e := byName[name]
		if e == nil {
			t.Errorf("no Encoding %q", name)
			continue
		}
		if e.Style != d.style || e.Explode != d.explode || e.ExplodeSet != d.explodeSet || e.AllowReserved != d.allowReserve || e.Err != nil {
			t.Errorf("%s: Style %q Explode %t ExplodeSet %t AllowReserved %t Err %v; want %+v", name, e.Style, e.Explode, e.ExplodeSet, e.AllowReserved, e.Err, d)
		}
		if e.ContentType != "" {
			t.Errorf("%s: ContentType = %q, want empty for a styled field", name, e.ContentType)
		}
	}
	for _, name := range []string{"bad1", "bad2", "badct"} {
		if e := byName[name]; e == nil || e.Err == nil || errors.Is(e.Err, errors.ErrUnsupported) {
			t.Errorf("%s: Err = %v, want the document's defect", name, e)
		}
	}
	if e := byName["ct"]; e == nil || e.Style != "" || e.ContentType != "application/json" || e.Err != nil {
		t.Errorf("ct = %+v", e)
	}
	if e := byName["hdr"]; e == nil || e.ContentType != "text/plain" || len(e.Headers) != 0 {
		t.Errorf("hdr = %+v, want text/plain and no Headers outside multipart", e)
	}
	if form.Err != nil {
		t.Errorf("Media.Err = %v for Encoding defects", form.Err)
	}
	if m1 := encodingByName(t, multi)["m1"]; m1 == nil || m1.Style != "form" || m1.Explode || !m1.ExplodeSet || m1.ContentType != "" {
		t.Errorf("multipart m1 = %+v", m1)
	}
}

// A field whose Encoding has Err refuses only a call that uses it, at its
// Inputs key (describe.go, Operation.Err: "A defect in an optional part is
// reported on that part instead, and fails a call only when the call uses
// it"; errors.go, RequestError.Inputs).
func TestEncodingDefectRefusesItsField(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(descPaths, descComponents), nil)
	resp, err := c.Call(t.Context(), "useBad", &openapi.Input{Body: map[string]string{"bad1": "x", "ok": "y"}}, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/bad1")
	mustCall(t, c, "useBad", &openapi.Input{Body: map[string]string{"ok": "y z"}}, nil)
	if got := w.last(t); string(got.Body) != "ok=y+z" {
		t.Errorf("body %q, want ok=y+z", got.Body)
	}
}

// ItemSchema is an OpenAPI 3.2 field: in a 3.1 document it is nil, whatever
// the Media Type Object writes; the type is sequential all the same.
func TestItemSchemaIn31(t *testing.T) {
	c := parseAt(t, doc31(descPaths, descComponents), "https://api.example.test", testDocURI, nil)
	m := reqMedia(t, mustOp(t, c, "itemSchema"), 0)
	if m.ItemSchema != nil || !m.Sequential || len(m.Encoding) != 0 {
		t.Errorf("ItemSchema %v, Sequential %t, Encoding %d; want nil, true, none", m.ItemSchema, m.Sequential, len(m.Encoding))
	}
}
