package openapi_test

import (
	"slices"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// arrayDefaultsDoc is a document of the given edition whose multipart and
// form bodies have properties that are arrays: of arrays (nested two and
// three deep), without items, of items without type, of items that may be an
// array or a string, through an items cycle of two schemas and of one, and,
// as controls, of objects and a property without type.
func arrayDefaultsDoc(version string) string {
	const props = `{"type":"object","properties":{
		"aa":{"type":"array","items":{"type":"array","items":{"type":"string"}}},
		"aaa":{"type":"array","items":{"type":"array","items":{"type":"array","items":{"type":"integer"}}}},
		"noitems":{"type":"array"},
		"untypeditems":{"type":"array","items":{}},
		"either":{"type":"array","items":{"type":["array","string"],"items":{"type":"integer"}}},
		"cycle":{"$ref":"#/components/schemas/X"},
		"self":{"$ref":"#/components/schemas/S"},
		"objects":{"type":"array","items":{"type":"object"}},
		"untyped":{}}}`
	return editionDoc(version, `"/x":{"post":{"requestBody":{"content":{
		"multipart/form-data":{"schema":`+props+`},
		"application/x-www-form-urlencoded":{"schema":`+props+`}}}}}`,
		`"components":{"schemas":{
			"X":{"type":"array","items":{"$ref":"#/components/schemas/Y"}},
			"Y":{"type":"array","items":{"$ref":"#/components/schemas/X"}},
			"S":{"type":"array","items":{"$ref":"#/components/schemas/S"}}}}`)
}

// doc.go, Configuration: a field's default type is read "by the Encoding
// Object's table of defaults in the schema's edition ... Where they allow
// every type, or none but null, it is application/octet-stream in OpenAPI 3.1
// and 3.2 ... and text/plain in 3.0, whose table gives no default for it ...
// Of a property, whose array is sent one field or part per item, the array
// type takes the defaults of its items, those of a schema without type when
// it declares none. Of an item, the array type is application/json in OpenAPI
// 3.2, as its table says of an array inside a top-level array; in 3.0 and
// 3.1, whose tables read an array by its items, it takes the defaults of its
// own items in turn, as a property's does, and an items chain that leads back
// into itself adds the default of a schema without type." OAS 3.2.1 section
// 4.15.1.1: "the array row in this table applies only to array values inside
// of a top-level array when encoding by name"; OAS 3.1.2 section 4.8.15.1.1
// and OAS 3.0.4 section 4.7.15.1.1: an array's default is "according to the
// type of the items schema". Param.ContentType reports the default under
// both form types, a list as a comma-separated one.
func TestArrayFieldDefaultsByEdition(t *testing.T) {
	want := map[string]map[string][]string{
		"3.0.4": {
			"aa": {"text/plain"}, "aaa": {"text/plain"}, "noitems": {"text/plain"}, "untypeditems": {"text/plain"},
			"cycle": {"text/plain"}, "self": {"text/plain"}, "objects": {"application/json"}, "untyped": {"text/plain"},
		},
		"3.1.2": {
			"aa": {"text/plain"}, "aaa": {"text/plain"}, "noitems": {"application/octet-stream"}, "untypeditems": {"application/octet-stream"},
			"either": {"text/plain"}, "cycle": {"application/octet-stream"}, "self": {"application/octet-stream"},
			"objects": {"application/json"}, "untyped": {"application/octet-stream"},
		},
		"3.2.1": {
			"aa": {"application/json"}, "aaa": {"application/json"}, "noitems": {"application/octet-stream"}, "untypeditems": {"application/octet-stream"},
			"either": {"application/json", "text/plain"}, "cycle": {"application/json"}, "self": {"application/json"},
			"objects": {"application/json"}, "untyped": {"application/octet-stream"},
		},
	}
	for version, fields := range want {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, arrayDefaultsDoc(version), nil)
			op := mustOp(t, c, "POST /x")
			for i := range 2 {
				m := reqMedia(t, op, i)
				byName := encodingByName(t, m)
				for name, types := range fields {
					e := byName[name]
					if e == nil {
						t.Errorf("%s: no Encoding Param %s", m.Type, name)
						continue
					}
					if got := contentTypes(e.ContentType); !slices.Equal(got, types) {
						t.Errorf("%s %s: ContentType %q, want %q", m.Type, name, e.ContentType, types)
					}
				}
			}
		})
	}
}

// doc.go, Configuration, as for TestArrayFieldDefaultsByEdition: in OpenAPI
// 3.2 an array inside an array property is application/json, so each item of
// a property that is an array of arrays is sent as one JSON part or field,
// rather than refused as a text/plain value that cannot hold an array.
func TestNestedArrayItemsSentAsJSON(t *testing.T) {
	w := newWire(t, nil)
	const schema = `{"schema":{"type":"object","properties":{"aa":{"type":"array","items":{"type":"array","items":{"type":"string"}}}}}}`
	c := parseFor(t, w, `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{
		"/m":{"post":{"operationId":"m","requestBody":{"content":{"multipart/form-data":`+schema+`}}}},
		"/f":{"post":{"operationId":"f","requestBody":{"content":{"application/x-www-form-urlencoded":`+schema+`}}}}}}`, nil)
	body := map[string]any{"aa": [][]string{{"a", "b"}, {"c"}}}
	_, parts := sendMultipart(t, w, c, "m", "multipart/form-data", body)
	checkParts(t, parts, []wantPart{
		{disposition: formData("aa"), ctype: "application/json", content: `["a","b"]`},
		{disposition: formData("aa"), ctype: "application/json", content: `["c"]`},
	})
	mustCall(t, c, "f", &openapi.Input{Body: body}, nil)
	if got := string(w.last(t).Body); got != "aa=%5B%22a%22%2C%22b%22%5D&aa=%5B%22c%22%5D" {
		t.Errorf("form body %q, want aa=%%5B%%22a%%22%%2C%%22b%%22%%5D&aa=%%5B%%22c%%22%%5D", got)
	}
}

// doc.go, Configuration: "Of an item, the array type is application/json in
// OpenAPI 3.2, as its table says of an array inside a top-level array; in 3.0
// and 3.1, whose tables read an array by its items, it takes the defaults of
// its own items in turn"; doc.go, Values: an "array item ... whose JSON data
// is null is omitted", so null takes no default (OAS 3.2.1 section 4.15.1.1:
// "If null values are entirely omitted, then the contentType is irrelevant").
// So in OpenAPI 3.2 the items of an array property that are arrays declaring
// no items, arrays declaring only prefixItems, or arrays or null, default to
// application/json alone, and under multipart/form-data and
// application/x-www-form-urlencoded the Body {"p": [["1","2"]]} sends its one
// item as JSON.
func TestArrayItemWithoutItemsIsJSON(t *testing.T) {
	for _, inner := range []string{
		`{"type":"array"}`,
		`{"type":"array","prefixItems":[{"type":"string"}]}`,
		`{"type":["array","null"]}`,
	} {
		t.Run(inner, func(t *testing.T) {
			props := `{"type":"object","properties":{"p":{"type":"array","items":` + inner + `}}}`
			c := editionClient(t, editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{
				"multipart/form-data":{"schema":`+props+`},
				"application/x-www-form-urlencoded":{"schema":`+props+`}}}}}`), nil)
			op := mustOp(t, c, "POST /x")
			for i, tc := range []struct{ media, want string }{
				{"multipart/form-data; boundary=B", "--B\r\nContent-Disposition: form-data; name=\"p\"\r\nContent-Type: application/json\r\n\r\n[\"1\",\"2\"]\r\n--B--\r\n"},
				{"application/x-www-form-urlencoded", "p=%5B%221%22%2C%222%22%5D"},
			} {
				m := reqMedia(t, op, i)
				if e := encodingByName(t, m)["p"]; e == nil {
					t.Errorf("%s: no Encoding Param p", m.Type)
				} else if e.ContentType != "application/json" {
					t.Errorf("%s: p's ContentType %q, want application/json", m.Type, e.ContentType)
				}
				req, err := c.Prepare("POST /x", &openapi.Input{MediaType: tc.media, Body: map[string]any{"p": [][]string{{"1", "2"}}}})
				if err != nil {
					t.Errorf("%s: refused: %v", tc.media, err)
					continue
				}
				if got := string(editionBody(t, req)); got != tc.want {
					t.Errorf("%s: body %q, want %q", tc.media, got, tc.want)
				}
			}
		})
	}
}
