package openapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the stage 4 review round, class ruling C4-2, schema
// inspection (stage 4 ledger, "Review round (6917b84)"): "property lists
// and types come from the schema and every schema reachable from it by $ref
// and allOf (OpenAPI 3.2.1 section 4.24.4 MUST; adopted for 3.1, whose text
// is silent). Types from several reachable schemas intersect (a schema
// without type allows all); an array's default follows its items the same
// way; an items chain that leads back into a schema being resolved
// contributes the absent type (application/octet-stream). Results do not
// depend on which operation compiles first. No compile path recurses once
// per $ref or items link (iterative, or bounded at 1,000 links, past which
// the absent type applies); each node's result is computed once (P1); the
// default set is held as a set of the three possible defaults and
// formatted only for a descriptor." The verification pass's C4-8 withdraws
// the bound ("Items chains are followed iteratively with each node's set
// memoized and no length bound: a long chain gets its true default"; see
// regress_stage4_inspect_test.go). Findings F2, F9, F17, A3; and F4/A7
// (nested multipart from array items), F6/A6 (RFC 6570 fields ignored
// outside form-urlencoded and multipart/form-data, OAS 3.1.2 section
// 4.8.15.1.2: "This field SHALL be ignored if the request body media type
// is not application/x-www-form-urlencoded or multipart/form-data"), F29
// and A8.

const schemaPaths = `
	"/ao":{"post":{"operationId":"allOf","requestBody":{"content":{
		"application/x-www-form-urlencoded":{"schema":{"allOf":[{"type":"object","properties":{"age":{"type":"integer"},"meta":{"type":"object"}}}]}},
		"multipart/form-data":{"schema":{"allOf":[{"type":"object","properties":{"age":{"type":"integer"},"meta":{"type":"object"}}}]}}}}}},
	"/rs":{"post":{"operationId":"refSiblings","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"$ref":"#/components/schemas/B","properties":{"extra":{"type":"string"}}}}}}}},
	"/pt":{"post":{"operationId":"propTypes","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{
		"p":{"allOf":[{"type":"object"}]},
		"q":{"$ref":"#/components/schemas/Obj","description":"an object by reference, with a sibling"},
		"r":{"type":["string","object"],"allOf":[{"type":"string"}]},
		"u":{"type":"integer","allOf":[{"type":["integer","string"]}]},
		"w":{"type":["string","object"],"$ref":"#/components/schemas/Obj"},
		"v":{"type":["string","object"]},
		"arr":{"type":"array","items":{"allOf":[{"type":"object"}]}},
		"arr2":{"type":"array","items":{"$ref":"#/components/schemas/Obj","description":"x"}},
		"any":{"allOf":[{"description":"no type"}]}}}}}}}},
	"/one":{"post":{"operationId":"one","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"x":{"$ref":"#/components/schemas/X"}}}}}}}},
	"/two":{"post":{"operationId":"two","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"y":{"$ref":"#/components/schemas/Y"}}}}}}}},
	"/n":{"post":{"operationId":"nested","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"docs":{"type":"array","items":{"type":"object","properties":{"n":{"type":"integer"},"s":{"type":"string"},"o":{"type":"object"}}}}}},
		"encoding":{"docs":{"contentType":"multipart/mixed"}}}}}}},
	"/mx":{"post":{"operationId":"mixedStyles","requestBody":{"content":{"multipart/mixed":{
		"schema":{"type":"object","properties":{"meta":{"type":"object"},"tags":{"type":"array","items":{"type":"string"}},"p":{}}},
		"encoding":{"meta":{"style":"form","explode":true,"contentType":"application/json"},"tags":{"explode":false},"p":{"contentType":"application/json","style":"form","explode":false}}}}}}},
	"/ar":{"post":{"operationId":"allowReserved","requestBody":{"content":{
		"multipart/form-data":{"encoding":{"r":{"allowReserved":true}}},
		"application/x-www-form-urlencoded":{"encoding":{"r":{"allowReserved":true}}}}}}}`

const schemaComponents = `"components":{"schemas":{
	"B":{"type":"object","properties":{"x":{"type":"integer"}}},
	"Obj":{"type":"object"},
	"X":{"type":"array","items":{"$ref":"#/components/schemas/Y"}},
	"Y":{"type":["array","object"],"items":{"$ref":"#/components/schemas/X"}}}}`

func schemaDoc() string { return doc31(schemaPaths, schemaComponents) }

// contentTypes splits a descriptor's ContentType list into a sorted set.
func contentTypes(ct string) []string {
	var set []string
	for _, s := range strings.Split(ct, ",") {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(set, s) {
			set = append(set, s)
		}
	}
	slices.Sort(set)
	return set
}

// C4-2 (F17): properties declared through allOf, or through a $ref with
// sibling keywords, are fields with their declared types: in
// Media.Encoding, and on the wire under form and multipart.
func TestC42AllOfAndRefSiblingProperties(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, schemaDoc(), nil)
	op := mustOp(t, c, "allOf")
	for i := range 2 {
		byName := encodingByName(t, reqMedia(t, op, i))
		if e := byName["age"]; e == nil || e.ContentType != "text/plain" {
			t.Errorf("%s age = %+v, want text/plain", reqMedia(t, op, i).Type, e)
		}
		if e := byName["meta"]; e == nil || e.ContentType != "application/json" {
			t.Errorf("%s meta = %+v, want application/json", reqMedia(t, op, i).Type, e)
		}
	}
	body := map[string]any{"age": 5, "meta": map[string]string{"k": "v"}}
	mustCall(t, c, "allOf", &openapi.Input{Body: body, MediaType: "application/x-www-form-urlencoded"}, nil)
	if got := w.last(t); string(got.Body) != "age=5&meta=%7B%22k%22%3A%22v%22%7D" {
		t.Errorf("form body %q", got.Body)
	}
	mustCall(t, c, "allOf", &openapi.Input{Body: body, MediaType: "multipart/form-data"}, nil)
	got := w.last(t)
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	checkParts(t, parts, []wantPart{
		{disposition: formData("age"), ctype: "text/plain", content: "5"},
		{disposition: formData("meta"), ctype: "application/json", content: `{"k":"v"}`},
	})

	rs := encodingByName(t, reqMedia(t, mustOp(t, c, "refSiblings"), 0))
	if rs["x"] == nil || rs["x"].ContentType != "text/plain" || rs["extra"] == nil || rs["extra"].ContentType != "text/plain" {
		t.Errorf("$ref with siblings: Encoding %q", encodingNames(reqMedia(t, mustOp(t, c, "refSiblings"), 0)))
	}
	mustCall(t, c, "refSiblings", &openapi.Input{Body: map[string]any{"x": 1, "extra": "e"}}, nil)
	if got := w.last(t); string(got.Body) != "extra=e&x=1" {
		t.Errorf("form body %q, want extra=e&x=1", got.Body)
	}
}

// C4-2 (F17): a property's type is read through allOf and a $ref with
// siblings, types from several reachable schemas intersect (a schema
// without type allows all), an array's default follows its items the same
// way, and a type left with two defaults requires Part.MediaType (stage 4
// ledger, IP4-2).
func TestC42PropertyTypes(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, schemaDoc(), nil)
	byName := encodingByName(t, reqMedia(t, mustOp(t, c, "propTypes"), 0))
	for name, want := range map[string]string{
		"p": "application/json", "q": "application/json", "w": "application/json",
		"arr": "application/json", "arr2": "application/json",
		"r": "text/plain", "u": "text/plain", "any": "application/octet-stream",
	} {
		if e := byName[name]; e == nil || e.ContentType != want {
			t.Errorf("%s: %+v, want ContentType %s", name, e, want)
		}
	}
	if e := byName["v"]; e == nil || !slices.Equal(contentTypes(e.ContentType), []string{"application/json", "text/plain"}) {
		t.Errorf("v: %+v, want application/json and text/plain", e)
	}
	_, parts := sendMultipart(t, w, c, "propTypes", "multipart/form-data", map[string]any{
		"p": map[string]int{"k": 1}, "q": map[string]int{"k": 2}, "r": "s", "u": 5, "w": map[string]int{"k": 3},
		"arr": []any{map[string]int{"a": 1}}, "arr2": []any{map[string]int{"b": 2}}, "any": "raw",
		"v": openapi.Part{Content: map[string]int{"v": 1}, MediaType: "application/json"},
	})
	checkParts(t, parts, []wantPart{
		{disposition: formData("any"), ctype: "application/octet-stream", content: "raw"},
		{disposition: formData("arr"), ctype: "application/json", content: `{"a":1}`},
		{disposition: formData("arr2"), ctype: "application/json", content: `{"b":2}`},
		{disposition: formData("p"), ctype: "application/json", content: `{"k":1}`},
		{disposition: formData("q"), ctype: "application/json", content: `{"k":2}`},
		{disposition: formData("r"), ctype: "text/plain", content: "s"},
		{disposition: formData("u"), ctype: "text/plain", content: "5"},
		{disposition: formData("v"), ctype: "application/json", content: `{"v":1}`},
		{disposition: formData("w"), ctype: "application/json", content: `{"k":3}`},
	})
	before := w.count()
	resp, err := c.Call(t.Context(), "propTypes", &openapi.Input{Body: map[string]any{"v": "x"}}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "Settings", re.Settings, true, "Input.Body/v")
}

// C4-2 (F9, IP4-2 cycle coverage): X is an array of Y and Y an array or
// object of X. An items chain that leads back into a schema being resolved
// contributes the absent type, and "Results do not depend on which
// operation compiles first": whichever field is described first, each
// field's default set is that of its schema entered first, application/json
// (Y's object) and application/octet-stream (the back edge), and the same
// call has the same outcome.
func TestC42ItemsCycleOrderIndependent(t *testing.T) {
	type outcome struct {
		x, y           string
		oneErr, twoErr string
	}
	describe := func(first, second string) outcome {
		c, err := openapi.Parse(context.Background(), []byte(expand(schemaDoc(), "https://api.example.test")), testDocURI, nil)
		if err != nil {
			t.Fatal(err)
		}
		mustOp(t, c, first)
		mustOp(t, c, second)
		var o outcome
		o.x = strings.Join(contentTypes(encodingByName(t, reqMedia(t, mustOp(t, c, "one"), 0))["x"].ContentType), ", ")
		o.y = strings.Join(contentTypes(encodingByName(t, reqMedia(t, mustOp(t, c, "two"), 0))["y"].ContentType), ", ")
		_, err = c.Prepare("one", &openapi.Input{Body: map[string]any{"x": []any{"a"}}})
		o.oneErr = fmt.Sprint(err)
		_, err = c.Prepare("two", &openapi.Input{Body: map[string]any{"y": []any{"a"}}})
		o.twoErr = fmt.Sprint(err)
		return o
	}
	a, b := describe("one", "two"), describe("two", "one")
	if a != b {
		t.Errorf("the result depends on the order of first use:\none, two: %+v\ntwo, one: %+v", a, b)
	}
	const want = "application/json, application/octet-stream"
	if a.x != want || a.y != want || b.x != want || b.y != want {
		t.Errorf("defaults x %q, y %q (and %q, %q), want %q for both", a.x, a.y, b.x, b.y, want)
	}
}

// chainDoc is a document whose one form field f, under multipart/form-data,
// is the head of an items chain of n links through $ref, each link a
// shallow schema in an extension array (F2: "a valid but hostile document
// with a long chain under a form or multipart body"), the last a string.
func chainDoc(n int) []byte {
	var b strings.Builder
	b.Grow(n * 48)
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/up":{"post":{"operationId":"up","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"f":{"$ref":"#/x-chain/0"}}}}}}}}},"x-chain":[`)
	for i := range n {
		fmt.Fprintf(&b, `{"type":"array","items":{"$ref":"#/x-chain/%d"}},`, i+1)
	}
	b.WriteString(`{"type":"string"}]}`)
	return []byte(b.String())
}

// C4-2 (F2): "No compile path recurses once per $ref or items link". A
// 900,000-link chain (40 MB, under the default document bound) is
// described without a crash, at a cost linear in the chain from 225,000
// links: parse and Operations(), which describes the field, text/plain as
// the string at the chain's end gives it (C4-8, which withdraws the
// 1,000-link cut: "a long chain gets its true default"; at b4872f9 the cut
// gave application/octet-stream). At 6917b84 it ended the process with a
// fatal stack overflow, so it runs in a child process.
func TestC42LongItemsChain(t *testing.T) {
	if !inChild(t) {
		return
	}
	wantLinear(t, "parse and Operations()", 225000, func(n int) func() {
		doc := chainDoc(n)
		return func() {
			c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			ops := c.Operations()
			if len(ops) != 1 || ops[0].Err != nil || ops[0].Body == nil || len(ops[0].Body.Media[0].Encoding) != 1 {
				t.Errorf("the chained field is not described")
			} else if e := ops[0].Body.Media[0].Encoding[0]; e.ContentType != "text/plain" {
				t.Errorf("the chained field's ContentType is %q, want text/plain", e.ContentType)
			}
		}
	})
}

// typeListChain is Astra's A3 document: schemas S0 to S(n-1) of type
// [array, string] whose items is the next, Sn an object, and one form
// field whose schema is S0.
func typeListChain(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/f":{"post":{"operationId":"f","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"f":{"$ref":"#/components/schemas/S0"}}}}}}}}},"components":{"schemas":{`)
	for i := range n {
		fmt.Fprintf(&b, `"S%d":{"type":["array","string"],"items":{"$ref":"#/components/schemas/S%d"}},`, i, i+1)
	}
	fmt.Fprintf(&b, `"S%d":{"type":"object"}}}}`, n)
	return []byte(b.String())
}

// C4-2 (A3): "the default set is held as a set of the three possible
// defaults and formatted only for a descriptor": a chain of
// [array, string] schemas gives one field the defaults text/plain and
// application/json, a short ContentType, and costs allocated bytes and
// retained memory linear in the chain (a joined list grown per link would
// be quadratic: about 600 MB at 10,000 links).
func TestC42DefaultSetLinear(t *testing.T) {
	c, err := openapi.Parse(context.Background(), typeListChain(1000), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := encodingByName(t, reqMedia(t, mustOp(t, c, "f"), 0))["f"]
	if e == nil || len(e.ContentType) > 80 || !slices.Equal(contentTypes(e.ContentType), []string{"application/json", "text/plain"}) {
		t.Errorf("f = %.200v, want the two defaults", e)
	}
	wantLinearBytes(t, "Operations() bytes", 1000, func(n int) func() { return timedOperations(t, typeListChain(n)) })
	t.Run("retained after Operations()", func(t *testing.T) {
		keep := func(n int) int64 {
			c, err := openapi.Parse(context.Background(), typeListChain(n), testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			return retainedBy(func() any { return c.Operations() })
		}
		small, large := keep(1000), keep(4000)
		ratio := float64(large) / float64(max(small, 64<<10))
		t.Logf("1,000 links: %d bytes, 4,000 links: %d bytes (%.1fx)", small, large, ratio)
		if ratio > 8 {
			t.Errorf("four times the chain retained %.1f times the memory; want linear", ratio)
		}
	})
}

// F4, A7: a nested multipart part made from an array value takes its
// fields from the items schema (client.go, Input.Body: "each item taking
// the property's content type (an array schema's items type by default)";
// stage 4 ledger, Q7), so each item's members have their declared types.
func TestF4NestedMultipartFromArrayItems(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, schemaDoc(), nil)
	_, parts := sendMultipart(t, w, c, "nested", "multipart/form-data", map[string]any{
		"docs": []any{map[string]any{"n": 5, "s": "x", "o": map[string]int{"k": 1}}, map[string]any{"n": 6}},
	})
	if len(parts) != 2 {
		t.Fatalf("%d parts, want 2", len(parts))
	}
	want := [][]wantPart{
		{
			{disposition: formData("n"), ctype: "text/plain", content: "5"},
			{disposition: formData("o"), ctype: "application/json", content: `{"k":1}`},
			{disposition: formData("s"), ctype: "text/plain", content: "x"},
		},
		{{disposition: formData("n"), ctype: "text/plain", content: "6"}},
	}
	for i, p := range parts {
		if d := p.header.Get("Content-Disposition"); d != formData("docs") {
			t.Errorf("part %d: Content-Disposition %q", i, d)
		}
		_, _, nested := readMultipart(t, p.header.Get("Content-Type"), p.body)
		checkParts(t, nested, want[i])
	}
}

// F6, A6: under a multipart type other than multipart/form-data, an
// Encoding's style, explode and allowReserved are ignored and its
// contentType governs (OAS 3.1.2 section 4.8.15.1.2; describe.go,
// Param.ContentType: "Under application/x-www-form-urlencoded and
// multipart/form-data it is empty for a field whose Encoding sets style,
// explode or allowReserved"): the parts are typed by contentType or the
// default, an array value one part per item, and the descriptors carry no
// style.
func TestF6StylesIgnoredOutsideFormData(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, schemaDoc(), nil)
	_, parts := sendMultipart(t, w, c, "mixedStyles", "multipart/mixed", map[string]any{
		"meta": map[string]string{"a": "1", "b": "2"}, "tags": []string{"x", "y"}, "p": map[string]string{"a": "b"},
	})
	checkParts(t, parts, []wantPart{
		{disposition: formData("meta"), ctype: "application/json", content: `{"a":"1","b":"2"}`},
		{disposition: formData("p"), ctype: "application/json", content: `{"a":"b"}`},
		{disposition: formData("tags"), ctype: "text/plain", content: "x"},
		{disposition: formData("tags"), ctype: "text/plain", content: "y"},
	})
	byName := encodingByName(t, reqMedia(t, mustOp(t, c, "mixedStyles"), 0))
	for name, ct := range map[string]string{"meta": "application/json", "tags": "text/plain", "p": "application/json"} {
		if e := byName[name]; e == nil || e.Style != "" || e.ContentType != ct || e.Err != nil {
			t.Errorf("%s = %+v, want no Style and ContentType %s", name, e, ct)
		}
	}
}

// F29: Param.AllowReserved is "false where the edition or the media type
// ignores it, as for a multipart/form-data field" (OAS 3.1.2 section
// 4.8.15.1.2: "When using RFC6570-style serialization for
// multipart/form-data, URI percent-encoding MUST NOT be applied, and the
// value of allowReserved has no effect"); under
// application/x-www-form-urlencoded it is the declared value.
func TestF29AllowReservedIgnoredInFormData(t *testing.T) {
	c := parseAt(t, schemaDoc(), "https://api.example.test", testDocURI, nil)
	op := mustOp(t, c, "allowReserved")
	if e := encodingByName(t, reqMedia(t, op, 0))["r"]; e == nil || e.AllowReserved || e.Style != "form" {
		t.Errorf("multipart/form-data r = %+v, want Style form and AllowReserved false", e)
	}
	if e := encodingByName(t, reqMedia(t, op, 1))["r"]; e == nil || !e.AllowReserved {
		t.Errorf("form-urlencoded r = %+v, want AllowReserved true", e)
	}
}

// A8: an Encoding's contentType list is split where a comma separates
// media types, not inside a quoted parameter value (RFC 9110 section 5.6.4,
// quoted-string): `text/plain; profile="a,b"` is one type, which selects
// itself, and a list of it and application/json requires Part.MediaType.
func TestA8ContentTypeListQuoteAware(t *testing.T) {
	one, _ := json.Marshal(`text/plain; profile="a,b"`)
	two, _ := json.Marshal(`text/plain; profile="a,b", application/json`)
	doc := doc31(fmt.Sprintf(`"/q":{"post":{"operationId":"quoted","requestBody":{"content":{
		"multipart/form-data":{"encoding":{"x":{"contentType":%s},"y":{"contentType":%s}}},
		"application/x-www-form-urlencoded":{"encoding":{"x":{"contentType":%s}}}}}}}`, one, two, one))
	w := newWire(t, nil)
	c := parseFor(t, w, doc, nil)
	if e := encodingByName(t, reqMedia(t, mustOp(t, c, "quoted"), 0))["x"]; e == nil || e.Err != nil || e.ContentType != `text/plain; profile="a,b"` {
		t.Errorf("x = %+v", e)
	}
	mustCall(t, c, "quoted", &openapi.Input{MediaType: "multipart/form-data", Body: map[string]any{
		"x": "ok", "y": openapi.Part{Content: "ok", MediaType: `text/plain; profile="a,b"`},
	}}, nil)
	got := w.last(t)
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	checkParts(t, parts, []wantPart{
		{disposition: formData("x"), ctype: `text/plain; profile="a,b"`, content: "ok"},
		{disposition: formData("y"), ctype: `text/plain; profile="a,b"`, content: "ok"},
	})
	before := w.count()
	resp, err := c.Call(t.Context(), "quoted", &openapi.Input{Body: map[string]any{"y": "ok"}, MediaType: "multipart/form-data"}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "Settings", re.Settings, true, "Input.Body/y")
	mustCall(t, c, "quoted", &openapi.Input{Body: map[string]any{"x": "ok"}, MediaType: "application/x-www-form-urlencoded"}, nil)
	if got := w.last(t); string(got.Body) != "x=ok" {
		t.Errorf("form body %q", got.Body)
	}
}

// Stage 4 ledger, RQ5: Media.Encoding lists fields only for a form type, a
// multipart type, or a range of multipart types such as multipart/*; for
// */* and application/* it lists none, their fields depending on the type
// a call selects (describe.go, Media.Encoding: "the fields of form or
// multipart content").
func TestRQ5RangeEncodingDescriptors(t *testing.T) {
	doc := doc31(`"/r":{"post":{"operationId":"ranges","requestBody":{"content":{
		"*/*":{"schema":{"type":"object","properties":{"x":{"type":"string"}}}},
		"application/*":{"schema":{"type":"object","properties":{"x":{"type":"string"}}}},
		"multipart/*":{"schema":{"type":"object","properties":{"x":{"type":"string"}}}}}}}}`)
	c := parseAt(t, doc, "https://api.example.test", testDocURI, nil)
	op := mustOp(t, c, "ranges")
	for i, want := range []int{0, 0, 1} {
		m := reqMedia(t, op, i)
		if len(m.Encoding) != want {
			t.Errorf("%s Encoding %q, want %d fields", m.Type, encodingNames(m), want)
		}
	}
	if e := encodingByName(t, reqMedia(t, op, 2))["x"]; e == nil || e.ContentType != "text/plain" {
		t.Errorf("multipart/* x = %+v, want text/plain", e)
	}
}

// Stage 4 ledger, RQ6: Media.Encoding's order is the schema's own
// properties in document order, then those reached through $ref and allOf,
// depth first in document order, a property's first declaration fixing its
// place, then the names only the encoding map has (describe.go,
// Media.Encoding: "the properties the schema lists at its top level (after
// following $ref), in document order, then those that only declare an
// Encoding Object, in the encoding map's order"). Here the schema writes a
// $ref, its own properties, then two allOf branches, the second a $ref; R,
// reached first, has an allOf of its own and repeats own2; allOf's first
// branch repeats r1.
func TestRQ6EncodingOrder(t *testing.T) {
	doc := doc31(`"/o":{"post":{"operationId":"ordered","requestBody":{"content":{"multipart/form-data":{
		"schema":{"$ref":"#/components/schemas/R","properties":{"own1":{"type":"string"},"own2":{"type":"string"}},
			"allOf":[{"properties":{"a1":{},"r1":{}}},{"$ref":"#/components/schemas/A2"}]},
		"encoding":{"enc":{"contentType":"text/csv"},"a1":{"contentType":"image/png"},"enc2":{"contentType":"text/csv"}}}}}}}`,
		`"components":{"schemas":{
			"R":{"type":"object","properties":{"r1":{"type":"string"},"own2":{}},"allOf":[{"properties":{"rr":{"type":"integer"}}}]},
			"A2":{"properties":{"a2":{"type":"boolean"}}}}}`)
	c := parseAt(t, doc, "https://api.example.test", testDocURI, nil)
	want := []string{"own1", "own2", "r1", "rr", "a1", "a2", "enc", "enc2"}
	if got := encodingNames(reqMedia(t, mustOp(t, c, "ordered"), 0)); !slices.Equal(got, want) {
		t.Errorf("Encoding %q, want %q", got, want)
	}
}
