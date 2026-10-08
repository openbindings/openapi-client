package openapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// partTree renders a multipart body sent with Content-Type ctype as one line
// per part: its Content-Disposition, or "-" for none, its media type, and its
// content, or, for a multipart part, its parts indented below it. A part
// with any other header field, or a multipart part whose Content-Type has a
// parameter other than its boundary, fails.
func partTree(t testing.TB, ctype string, body []byte, indent string) string {
	t.Helper()
	_, _, parts := readMultipart(t, ctype, body)
	var b strings.Builder
	for i, p := range parts {
		for k := range p.header {
			if k != "Content-Disposition" && k != "Content-Type" {
				t.Errorf("%spart %d: unexpected header field %s", indent, i, k)
			}
		}
		disposition := p.header.Get("Content-Disposition")
		if _, ok := p.header["Content-Disposition"]; !ok {
			disposition = "-"
		}
		mt, params, err := mime.ParseMediaType(p.header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("%spart %d: Content-Type %q: %v", indent, i, p.header.Get("Content-Type"), err)
		}
		if !strings.HasPrefix(mt, "multipart/") {
			fmt.Fprintf(&b, "%s%s | %s | %s\n", indent, disposition, p.header.Get("Content-Type"), p.body)
			continue
		}
		if _, ok := params["boundary"]; !ok || len(params) != 1 {
			t.Errorf("%spart %d: Content-Type %q, want %s with its boundary alone", indent, i, p.header.Get("Content-Type"), mt)
		}
		fmt.Fprintf(&b, "%s%s | %s\n", indent, disposition, mt)
		b.WriteString(partTree(t, p.header.Get("Content-Type"), p.body, indent+"  "))
	}
	return b.String()
}

// doc.go, Values: "The client first converts a value to JSON data as
// encoding/json would (struct tags, MarshalJSON, TextMarshaler map keys), then
// serializes that data as the document says"; client.go, Input.Body: "a
// property whose value is an array sends one field or part per item under the
// property's name ..., each item taking the property's content type"; "A part
// whose media type is multipart is encoded, one level deep, from an object or
// list by its Encoding's own encoding, prefixEncoding or itemEncoding";
// doc.go, Values: a property "whose JSON data is null is omitted". In an
// OpenAPI 3.2 multipart/form-data body, a json.RawMessage and a value whose
// MarshalJSON returns the same JSON, given to a property whose Encoding
// contentType is multipart/mixed, are sent as that JSON data is: null leaves
// no part, an object's members are the nested parts, and an array's items are
// parts of the property, each a multipart/mixed part of its own. The decoded
// JSON data, a map or a []any, is the control.
func TestMarshalerMultipartFieldReadAsJSONData(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.0", `"/x":{"post":{"requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{
			"o":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}},
			"l":{"type":"array","items":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}},
			"z":{"type":"string"}}},
		"encoding":{"o":{"contentType":"multipart/mixed"},"l":{"contentType":"multipart/mixed"}}}}}}}`), nil)
	const (
		kept   = `form-data; name="z" | text/plain | kept` + "\n"
		object = `form-data; name="o" | multipart/mixed
  form-data; name="a" | text/plain | x
  form-data; name="b" | text/plain | 7
` + kept
		array = `form-data; name="l" | multipart/mixed
  form-data; name="a" | text/plain | x
form-data; name="l" | multipart/mixed
  form-data; name="b" | text/plain | 7
` + kept
		objectJSON = `{"a":"x","b":7}`
		arrayJSON  = `[{"a":"x"},{"b":7}]`
	)
	for _, tc := range []struct {
		name, field string
		v           any
		want        string
	}{
		{"null RawMessage", "o", json.RawMessage("null"), kept},
		{"null MarshalJSON", "o", callerReturnedJSON{data: []byte("null")}, kept},
		{"null RawMessage array", "l", json.RawMessage("null"), kept},
		{"object RawMessage", "o", json.RawMessage(objectJSON), object},
		{"object MarshalJSON", "o", callerReturnedJSON{data: []byte(objectJSON)}, object},
		{"object control", "o", map[string]any{"a": "x", "b": 7}, object},
		{"array RawMessage", "l", json.RawMessage(arrayJSON), array},
		{"array MarshalJSON", "l", callerReturnedJSON{data: []byte(arrayJSON)}, array},
		{"array control", "l", []any{map[string]any{"a": "x"}, map[string]any{"b": 7}}, array},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := c.Prepare("POST /x", &openapi.Input{Body: map[string]any{tc.field: tc.v, "z": "kept"}})
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got := partTree(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req), ""); got != tc.want {
				t.Errorf("parts\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// doc.go, Values: "The client first converts a value to JSON data as
// encoding/json would (struct tags, MarshalJSON, TextMarshaler map keys), then
// serializes that data as the document says", and "a positional part ...,
// whose JSON data is null is omitted"; client.go, Input.Body: "A part whose
// media type is multipart is encoded, one level deep, from an object or list
// by its Encoding's own encoding, prefixEncoding or itemEncoding". In an
// OpenAPI 3.2 positional multipart/mixed body whose first two positions are
// multipart/mixed, a json.RawMessage and a value whose MarshalJSON returns the
// same JSON are sent as that JSON data is: null leaves no part, an object's
// members are named nested parts, and an array's items are positional nested
// parts, each typed by its position's schema. The decoded JSON data is the
// control.
func TestMarshalerPositionalPartReadAsJSONData(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.0", `"/x":{"post":{"requestBody":{"content":{"multipart/mixed":{
		"schema":{"type":"array","prefixItems":[
			{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}},
			{"type":"array","items":{"type":"string"}}],"items":{"type":"string"}},
		"prefixEncoding":[{"contentType":"multipart/mixed"},{"contentType":"multipart/mixed"}]}}}}}`), nil)
	const (
		after = `- | text/plain | after` + "\n"
		both  = `- | multipart/mixed
  form-data; name="a" | text/plain | x
  form-data; name="b" | text/plain | 7
- | multipart/mixed
  - | text/plain | x
  - | text/plain | 7
` + after
		objectJSON = `{"a":"x","b":7}`
		arrayJSON  = `["x",7]`
	)
	for _, tc := range []struct {
		name          string
		object, array any
		want          string
	}{
		{"null RawMessage", json.RawMessage("null"), json.RawMessage("null"), after},
		{"null MarshalJSON", callerReturnedJSON{data: []byte("null")}, callerReturnedJSON{data: []byte("null")}, after},
		{"RawMessage", json.RawMessage(objectJSON), json.RawMessage(arrayJSON), both},
		{"MarshalJSON", callerReturnedJSON{data: []byte(objectJSON)}, callerReturnedJSON{data: []byte(arrayJSON)}, both},
		{"control", map[string]any{"a": "x", "b": 7}, []any{"x", 7}, both},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := c.Prepare("POST /x", &openapi.Input{Body: []any{tc.object, tc.array, "after"}})
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got := partTree(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req), ""); got != tc.want {
				t.Errorf("parts\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// doc.go, Values: "Params values, Body, and Part contents take any Go value
// ... The client first converts a value to JSON data as encoding/json would
// (struct tags, MarshalJSON, TextMarshaler map keys), then serializes that
// data as the document says"; client.go, Input.Body: "Any other type, a named
// byte-slice type included, is a value for the codec", so a json.RawMessage
// Body is its JSON data, never bytes. A json.RawMessage Body and one whose
// MarshalJSON returns the same JSON send exactly what their decoded JSON data
// sends, the control: an object as an OpenAPI 3.2 multipart/form-data body's
// fields, an array as a positional multipart/mixed body's parts, and an array
// as a JSON Lines body's items.
func TestMarshalerBodyReadAsJSONData(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.0", `"/x":{"post":{"requestBody":{"content":{
		"multipart/form-data":{"schema":{"type":"object","properties":{"z":{"type":"string"}}}},
		"multipart/mixed":{"schema":{"type":"array","items":{"type":"string"}}},
		"application/jsonl":{}}}}}`), nil)
	for _, tc := range []struct {
		media, json string
		control     any
		want        string
	}{
		{"multipart/form-data; boundary=B", `{"z":"kept"}`, map[string]any{"z": "kept"},
			"--B\r\nContent-Disposition: form-data; name=\"z\"\r\nContent-Type: text/plain\r\n\r\nkept\r\n--B--\r\n"},
		{"multipart/mixed; boundary=B", `["x",7]`, []any{"x", 7},
			"--B\r\nContent-Type: text/plain\r\n\r\nx\r\n--B\r\nContent-Type: text/plain\r\n\r\n7\r\n--B--\r\n"},
		{"application/jsonl", `[1,{"a":2}]`, []any{1, map[string]any{"a": 2}}, "1\n{\"a\":2}\n"},
	} {
		for _, v := range []any{tc.control, json.RawMessage(tc.json), callerReturnedJSON{data: []byte(tc.json)}} {
			t.Run(fmt.Sprintf("%s/%T", tc.media, v), func(t *testing.T) {
				req, err := c.Prepare("POST /x", &openapi.Input{MediaType: tc.media, Body: v})
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if got := string(editionBody(t, req)); got != tc.want {
					t.Errorf("body %q, want %q", got, tc.want)
				}
			})
		}
	}
}

// client.go, Input.Body: for a sequential media type, "a Body whose JSON data
// is null, as a nil slice's is, has no items"; doc.go, Values: "a typed nil,
// such as a nil pointer or map, is a value, which encoding/json writes as
// null". As a JSON Lines or JSON text sequence body, a json.RawMessage
// "null", a value whose MarshalJSON returns null, and a nil pointer send what
// a nil slice, the control, sends: an empty body under the media type.
func TestSequentialNullBodyHasNoItems(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{
		"application/jsonl":{},"application/json-seq":{}}}}}`), nil)
	for _, media := range []string{"application/jsonl", "application/json-seq"} {
		control, err := c.Prepare("POST /x", &openapi.Input{MediaType: media, Body: []any(nil)})
		if err != nil {
			t.Fatalf("%s: a nil slice refused: %v", media, err)
		}
		ct, n, body := control.HTTP.Header.Get("Content-Type"), control.HTTP.ContentLength, editionBody(t, control)
		if ct != media || n != 0 || len(body) != 0 {
			t.Fatalf("%s: a nil slice sends Content-Type %q, Content-Length %d, body %q; want an empty body", media, ct, n, body)
		}
		for _, v := range []any{json.RawMessage("null"), callerReturnedJSON{data: []byte("null")}, (*[]any)(nil)} {
			t.Run(fmt.Sprintf("%s/%T", media, v), func(t *testing.T) {
				req, err := c.Prepare("POST /x", &openapi.Input{MediaType: media, Body: v})
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if gct, gn, got := req.HTTP.Header.Get("Content-Type"), req.HTTP.ContentLength, editionBody(t, req); gct != ct || gn != n || string(got) != string(body) {
					t.Errorf("Content-Type %q, Content-Length %d, body %q; want %q, %d, %q, as a nil slice sends", gct, gn, got, ct, n, body)
				}
			})
		}
	}
}

// client.go, Input.Body, of "any other OpenAPI 3.2 multipart media type":
// "Under these types and OpenAPI 3.2 multipart/form-data, ... a Body whose JSON
// data is null, as a nil slice's is, has no parts: it is the close delimiter
// alone"; doc.go, Values: "a typed nil, such as a nil pointer or map, is a
// value, which encoding/json writes as null". As a multipart/mixed or
// multipart/form-data body, a json.RawMessage "null", a value whose MarshalJSON
// returns null, and a nil pointer send what a nil slice, the control, sends:
// the close delimiter alone, "--B--" CRLF for boundary B.
func TestPositionalMultipartNullBodyHasNoParts(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{
		"multipart/mixed":{"schema":{"type":"array","items":{"type":"string"}}},
		"multipart/form-data":{"schema":{"type":"array"}}}}}}`), nil)
	for _, media := range []string{"multipart/mixed; boundary=B", "multipart/form-data; boundary=B"} {
		control, err := c.Prepare("POST /x", &openapi.Input{MediaType: media, Body: []any(nil)})
		if err != nil {
			t.Fatalf("%s: a nil slice refused: %v", media, err)
		}
		ct, n, body := control.HTTP.Header.Get("Content-Type"), control.HTTP.ContentLength, editionBody(t, control)
		if ct != media || n != int64(len("--B--\r\n")) || string(body) != "--B--\r\n" {
			t.Fatalf("%s: a nil slice sends Content-Type %q, Content-Length %d, body %q; want the close delimiter alone", media, ct, n, body)
		}
		for _, v := range []any{json.RawMessage("null"), callerReturnedJSON{data: []byte("null")}, (*[]any)(nil)} {
			t.Run(fmt.Sprintf("%s/%T", media, v), func(t *testing.T) {
				req, err := c.Prepare("POST /x", &openapi.Input{MediaType: media, Body: v})
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if gct, gn, got := req.HTTP.Header.Get("Content-Type"), req.HTTP.ContentLength, editionBody(t, req); gct != ct || gn != n || string(got) != string(body) {
					t.Errorf("Content-Type %q, Content-Length %d, body %q; want %q, %d, %q, as a nil slice sends", gct, gn, got, ct, n, body)
				}
			})
		}
	}
}

// client.go, Input.Body: for a sequential media type, "a non-nil pointer to a
// list sends what the list it points to sends", and so under "these
// types and OpenAPI 3.2 multipart/form-data", these being "any other OpenAPI
// 3.2 multipart media type"; each element of a sequential body is "encoded as
// that value on its own would be". As a multipart/mixed, multipart/form-data,
// JSON Lines or JSON text sequence body, a pointer to a slice or an array sends
// exactly what the slice or array it points to, the control, sends: for a nil
// slice no items, and for a slice whose elements' pointers have a MarshalJSON
// the elements on their own, as the slice does.
func TestListBodyPointerSendsItsList(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{
		"multipart/mixed":{"schema":{"type":"array","items":{"type":"string"}}},
		"multipart/form-data":{"schema":{"type":"array"}},
		"application/jsonl":{},"application/json-seq":{}}}}}`), nil)
	var noStrings []string
	var noObjects []map[string]string
	items := []any{&[]any{1, "x"}, &[]string{"a", "b"}, &[2]int{1, 2}, &[]ptrJSON{"a", "b"}, &noStrings}
	objects := []any{&[]any{map[string]any{"a": "x"}}, &[]map[string]string{{"a": "x"}, {"b": "y"}},
		&[2]map[string]string{{"a": "x"}, {"b": "y"}}, &noObjects} // a positional form-data part is a one-property object
	for _, tc := range []struct {
		media    string
		pointers []any
	}{
		{"multipart/mixed; boundary=B", items},
		{"multipart/form-data; boundary=B", objects},
		{"application/jsonl", items},
		{"application/json-seq", items},
	} {
		for _, p := range tc.pointers {
			list := reflect.ValueOf(p).Elem().Interface()
			t.Run(fmt.Sprintf("%s/%T%v", tc.media, p, list), func(t *testing.T) {
				control, err := c.Prepare("POST /x", &openapi.Input{MediaType: tc.media, Body: list})
				if err != nil {
					t.Fatalf("the %T it points to refused: %v", list, err)
				}
				ct, n, body := control.HTTP.Header.Get("Content-Type"), control.HTTP.ContentLength, editionBody(t, control)
				req, err := c.Prepare("POST /x", &openapi.Input{MediaType: tc.media, Body: p})
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if gct, gn, got := req.HTTP.Header.Get("Content-Type"), req.HTTP.ContentLength, editionBody(t, req); gct != ct || gn != n || string(got) != string(body) {
					t.Errorf("Content-Type %q, Content-Length %d, body %q; want %q, %d, %q, as the %T it points to sends", gct, gn, got, ct, n, body, list)
				}
			})
		}
	}
}

// doc.go, Values: "A form or multipart property or array item, or a
// positional part ..., whose JSON data is null is omitted, whatever its
// serialization or media type"; client.go, Part.Content: "A nil Content, or
// one whose JSON data is null, omits the part ... whatever the part's media
// type". encoding/json follows pointers and interfaces, so a pointer to a
// pointer to a value whose MarshalJSON writes null, and a pointer to a nil map
// or slice, directly, through a second pointer or through an interface, has
// null as its JSON data. Each is omitted as a property, as an array item of a
// form or multipart/form-data property, as a Part's Content and as an OpenAPI
// 3.2 positional multipart/mixed part, under application/json and under a
// type with a caller's codec, which never receives it: each body is the one
// sent without it.
func TestNullPointerChainOmitted(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `
		"/f":{"post":{"requestBody":{"content":{"application/x-www-form-urlencoded":{
			"schema":{"type":"object","properties":{"j":{"type":"array"},"c":{"type":"array"},"t":{"type":"string"}}},
			"encoding":{"j":{"contentType":"application/json"},"c":{"contentType":"application/x-tag"}}}}}}},
		"/m":{"post":{"requestBody":{"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"j":{"type":"array"},"c":{"type":"array"},"t":{"type":"string"}}},
			"encoding":{"j":{"contentType":"application/json"},"c":{"contentType":"application/x-tag"}}}}}}},
		"/pj":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/json"}}}}}},
		"/pc":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/x-tag"}}}}}}`),
		&openapi.Options{Codecs: map[string]openapi.Codec{"application/x-tag": tagCodec{tag: "TAG"}}})
	null := callerReturnedJSON{data: []byte("null")}
	p := &null
	var noMap map[string]any
	var noSlice []any
	ps := &noSlice
	var holdsNoSlice any = []any(nil)
	type input struct {
		key, media string
		body       any
	}
	var cases []struct {
		name      string
		with, out input // with the value, and without it
	}
	add := func(name string, with, out input) {
		cases = append(cases, struct {
			name      string
			with, out input
		}{name, with, out})
	}
	for _, v := range []struct {
		name  string
		value any
	}{
		{"pointer to a pointer to a null MarshalJSON", &p},
		{"pointer to a nil map", &noMap},
		{"pointer to a nil slice", &noSlice},
		{"pointer to a pointer to a nil slice", &ps},
		{"pointer to an interface holding a nil slice", &holdsNoSlice},
	} {
		for _, f := range []struct{ key, media string }{{"POST /f", ""}, {"POST /m", "multipart/form-data; boundary=B"}} {
			for _, field := range []string{"j", "c"} {
				at := v.name + "/" + f.key + " " + field
				add(at+" property", input{f.key, f.media, map[string]any{field: v.value, "t": "x"}}, input{f.key, f.media, map[string]any{"t": "x"}})
				add(at+" array item", input{f.key, f.media, map[string]any{field: []any{v.value, "x"}}}, input{f.key, f.media, map[string]any{field: []any{"x"}}})
				add(at+" Part Content", input{f.key, f.media, map[string]any{field: openapi.Part{Content: v.value}, "t": "x"}}, input{f.key, f.media, map[string]any{"t": "x"}})
			}
		}
		for _, key := range []string{"POST /pj", "POST /pc"} {
			add(v.name+"/"+key+" positional part", input{key, "multipart/mixed; boundary=B", []any{v.value, "x"}}, input{key, "multipart/mixed; boundary=B", []any{"x"}})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := c.Prepare(tc.out.key, &openapi.Input{MediaType: tc.out.media, Body: tc.out.body})
			if err != nil {
				t.Fatalf("the body without it refused: %v", err)
			}
			req, err := c.Prepare(tc.with.key, &openapi.Input{MediaType: tc.with.media, Body: tc.with.body})
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got, body := string(editionBody(t, req)), string(editionBody(t, want)); got != body {
				t.Errorf("body %q, want %q, as without it", got, body)
			}
		})
	}
}

// doc.go, Values: "The client first converts a value to JSON data as
// encoding/json would (struct tags, MarshalJSON, TextMarshaler map keys), then
// serializes that data as the document says"; client.go, Input.Body: for a
// sequential media type, "a Body whose JSON data is null, as a nil slice's
// is, has no items", and under "these types and OpenAPI 3.2
// multipart/form-data", these being "any other OpenAPI 3.2 multipart media
// type", it "has no parts: it is the close delimiter alone". encoding/json
// follows pointers and interfaces, so a whole multipart/mixed or JSON Lines
// body is null or a list by the JSON data it reaches through them: a pointer
// to a pointer to a value whose MarshalJSON writes null, a pointer to a nil
// pointer, a pointer to an interface holding nil and a pointer to a pointer
// to a nil slice send what a nil slice sends; a value whose MarshalJSON writes
// an array, behind one or two pointers or in an interface, a pointer to a
// pointer whose pointer-receiver MarshalJSON writes an array, and a slice
// behind two pointers or in an interface, send what the slice of that
// array's items sends. Each sends the same under both types.
func TestWholeListBodyByJSONData(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{
		"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/json"}},
		"application/jsonl":{}}}}}`), nil)
	array := func() callerReturnedJSON { return callerReturnedJSON{data: []byte(`["a",1]`)} }
	null := &callerReturnedJSON{data: []byte("null")}
	var nilPointer *callerReturnedJSON
	var holdsNil any
	var noSlice []any
	noSlicePointer := &noSlice
	arrayPointer := &callerReturnedJSON{data: []byte(`["a",1]`)}
	var holdsArray any = array()
	receiver := &pointerReceiverJSON{"ignored"}
	slice := []any{"a", 1}
	slicePointer := &slice
	var holdsSlice any = []any{"a", 1}
	for _, tc := range []struct {
		name      string
		value, as any
	}{
		{"pointer to a pointer to a null MarshalJSON", &null, []any(nil)},
		{"pointer to a nil pointer", &nilPointer, []any(nil)},
		{"pointer to an interface holding nil", &holdsNil, []any(nil)},
		{"pointer to a pointer to a nil slice", &noSlicePointer, []any(nil)},
		{"pointer to an array MarshalJSON", &callerReturnedJSON{data: []byte(`["a",1]`)}, []any{"a", 1}},
		{"pointer to a pointer to an array MarshalJSON", &arrayPointer, []any{"a", 1}},
		{"pointer to an interface holding an array MarshalJSON", &holdsArray, []any{"a", 1}},
		{"pointer to a pointer with a pointer-receiver array MarshalJSON", &receiver, []any{"pointer result", ""}},
		{"pointer to a pointer to a slice", &slicePointer, []any{"a", 1}},
		{"pointer to an interface holding a slice", &holdsSlice, []any{"a", 1}},
	} {
		for _, media := range []string{"multipart/mixed; boundary=B", "application/jsonl"} {
			t.Run(tc.name+"/"+media, func(t *testing.T) {
				control, err := c.Prepare("POST /x", &openapi.Input{MediaType: media, Body: tc.as})
				if err != nil {
					t.Fatalf("the control %#v refused: %v", tc.as, err)
				}
				ct, n, body := control.HTTP.Header.Get("Content-Type"), control.HTTP.ContentLength, editionBody(t, control)
				req, err := c.Prepare("POST /x", &openapi.Input{MediaType: media, Body: tc.value})
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if gct, gn, got := req.HTTP.Header.Get("Content-Type"), req.HTTP.ContentLength, editionBody(t, req); gct != ct || gn != n || string(got) != string(body) {
					t.Errorf("Content-Type %q, Content-Length %d, body %q; want %q, %d, %q, as %#v sends", gct, gn, got, ct, n, body, tc.as)
				}
			})
		}
	}
}

// nilReceiver's MarshalJSON handles a nil receiver.
type nilReceiver struct{}

func (p *nilReceiver) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte(`"nil-receiver"`), nil
	}
	return []byte(`"value"`), nil
}

// doc.go, Values: "The client first converts a value to JSON data as
// encoding/json would", and only a value "whose JSON data is null is
// omitted". encoding/json calls the MarshalJSON of a json.Marshaler interface
// value even when it holds a nil pointer, so a pointer to a json.Marshaler
// holding a nil *nilReceiver has "nil-receiver" as its JSON data, as a JSON
// body sends it, the control. Under application/json it is sent so, never
// omitted, as a property and an array item of a form and a
// multipart/form-data property, as a Part's Content, and as an OpenAPI 3.2
// positional multipart/mixed part.
func TestNilReceiverMarshalerSent(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `
		"/json":{"post":{"requestBody":{"content":{"application/json":{}}}}},
		"/f":{"post":{"requestBody":{"content":{"application/x-www-form-urlencoded":{
			"schema":{"type":"object","properties":{"j":{"type":"array"}}},"encoding":{"j":{"contentType":"application/json"}}}}}}},
		"/m":{"post":{"requestBody":{"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"j":{"type":"array"}}},"encoding":{"j":{"contentType":"application/json"}}}}}}},
		"/p":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/json"}}}}}}`), nil)
	var m json.Marshaler = (*nilReceiver)(nil)
	v := &m
	control, err := c.Prepare("POST /json", &openapi.Input{Body: v})
	if err != nil {
		t.Fatalf("a JSON body refused: %v", err)
	}
	data := string(editionBody(t, control))
	if data != `"nil-receiver"` {
		t.Fatalf("a JSON body sends %q, want \"nil-receiver\"", data)
	}
	formData := url.QueryEscape(data)
	part := func(name string) string {
		return "--B\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\nContent-Type: application/json\r\n\r\n" + data + "\r\n"
	}
	const mfd, mixed = "multipart/form-data; boundary=B", "multipart/mixed; boundary=B"
	for _, tc := range []struct {
		name, key, media string
		body             any
		want             string
	}{
		{"form property", "POST /f", "", map[string]any{"j": v}, "j=" + formData},
		{"form array item", "POST /f", "", map[string]any{"j": []any{v, "x"}}, "j=" + formData + "&j=%22x%22"},
		{"form Part Content", "POST /f", "", map[string]any{"j": openapi.Part{Content: v}}, "j=" + formData},
		{"multipart property", "POST /m", mfd, map[string]any{"j": v}, part("j") + "--B--\r\n"},
		{"multipart array item", "POST /m", mfd, map[string]any{"j": []any{v, "x"}},
			part("j") + "--B\r\nContent-Disposition: form-data; name=\"j\"\r\nContent-Type: application/json\r\n\r\n\"x\"\r\n--B--\r\n"},
		{"multipart Part Content", "POST /m", mfd, map[string]any{"j": openapi.Part{Content: v}}, part("j") + "--B--\r\n"},
		{"positional part", "POST /p", mixed, []any{v, "x"},
			"--B\r\nContent-Type: application/json\r\n\r\n" + data + "\r\n--B\r\nContent-Type: application/json\r\n\r\n\"x\"\r\n--B--\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := c.Prepare(tc.key, &openapi.Input{MediaType: tc.media, Body: tc.body})
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got := string(editionBody(t, req)); got != tc.want {
				t.Errorf("body %q, want %q", got, tc.want)
			}
		})
	}
}

// doc.go, Values: "The client first converts a value to JSON data as
// encoding/json would"; RequestError.Inputs holds "each input the operation
// cannot accept, and why". encoding/json refuses a map whose key type it
// cannot write (an array, float, bool or struct key) even when the map is
// nil, so such a nil map, behind a pointer or in an interface too, has no
// JSON data: it is never null. It is refused, with json.Marshal's own error,
// at its key: as a form property, as an array item of one, as a
// multipart/form-data property, as an OpenAPI 3.2 positional multipart/mixed
// part, as a whole multipart/mixed or JSON Lines body, and as a querystring
// parameter's application/x-www-form-urlencoded value.
func TestUnencodableNilMapRefused(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `
		"/f":{"post":{"requestBody":{"content":{"application/x-www-form-urlencoded":{
			"schema":{"type":"object","properties":{"j":{},"t":{"type":"string"}}},"encoding":{"j":{"contentType":"application/json"}}}}}}},
		"/m":{"post":{"requestBody":{"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"j":{},"t":{"type":"string"}}},"encoding":{"j":{"contentType":"application/json"}}}}}}},
		"/p":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/json"}}}}}},
		"/s":{"post":{"requestBody":{"content":{"application/jsonl":{}}}}},
		"/q":{"get":{"parameters":[{"name":"qs","in":"querystring","content":{"application/x-www-form-urlencoded":{}}}]}}`), nil)
	var arrayKeys map[[2]int]string
	var holdsArrayKeys any = arrayKeys
	const mfd, mixed = "multipart/form-data; boundary=B", "multipart/mixed; boundary=B"
	for _, v := range []struct {
		name  string
		value any
	}{
		{"array keys", arrayKeys},
		{"float keys", map[float64]string(nil)},
		{"bool keys", map[bool]string(nil)},
		{"struct keys", map[struct{ A int }]string(nil)},
		{"pointer to array keys", &arrayKeys},
		{"pointer to an interface holding array keys", &holdsArrayKeys},
	} {
		if _, err := json.Marshal(v.value); err == nil {
			t.Fatalf("%s: json.Marshal accepts it", v.name)
		}
		for _, tc := range []struct {
			name, key, media string
			in               *openapi.Input
			at               string
		}{
			{"form property", "POST /f", "", &openapi.Input{Body: map[string]any{"j": v.value, "t": "x"}}, "Input.Body/j"},
			{"form array item", "POST /f", "", &openapi.Input{Body: map[string]any{"j": []any{v.value, "x"}}}, "Input.Body/j/0"},
			{"multipart property", "POST /m", mfd, &openapi.Input{Body: map[string]any{"j": v.value, "t": "x"}}, "Input.Body/j"},
			{"positional part", "POST /p", mixed, &openapi.Input{Body: []any{v.value, "x"}}, "Input.Body/0"},
			{"multipart/mixed body", "POST /p", mixed, &openapi.Input{Body: v.value}, "Input.Body"},
			{"JSON Lines body", "POST /s", "", &openapi.Input{Body: v.value}, "Input.Body"},
			{"querystring value", "GET /q", "", &openapi.Input{Params: map[string]any{"qs": v.value}}, "qs"},
		} {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				tc.in.MediaType = tc.media
				_, err := c.Prepare(tc.key, tc.in)
				if err == nil {
					t.Fatal("prepared a value encoding/json cannot encode")
				}
				re := asRequestError(t, err)
				wantKeys(t, "Inputs", re.Inputs, true, tc.at)
				var unsupported *json.UnsupportedTypeError
				if !errors.As(re.Inputs[tc.at], &unsupported) {
					t.Errorf("Inputs[%q] = %v; want json.Marshal's *json.UnsupportedTypeError", tc.at, re.Inputs[tc.at])
				}
			})
		}
	}
}

// client.go, Input.Body: "A value whose type is exactly []byte, or an
// io.Reader, is sent as its bytes ... Any other type, a named byte-slice type
// included, is a value for the codec"; a list body is "a slice or array other
// than a byte slice (whose JSON data is a string)", and "a non-nil pointer to
// a list sends what the list it points to sends"; "A part whose media type is
// multipart is encoded, one level deep, from an object or list". encoding/json
// writes a named byte slice, a *[]byte and a pointer to a named byte slice as
// a base64 string, so none is a list: each is refused at Input.Body as a whole
// JSON Lines body and as a whole OpenAPI 3.2 multipart/mixed body, by a
// refusal that names the byte slice it excludes, and as a multipart/mixed
// part's content it is what the string of that base64 text is. Exactly
// []byte is still a pre-encoded body, the control.
func TestByteSliceIsNotAList(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `
		"/s":{"post":{"requestBody":{"content":{"application/jsonl":{}}}}},
		"/p":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/json"}}}}}},
		"/m":{"post":{"requestBody":{"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"nm":{}}},"encoding":{"nm":{"contentType":"multipart/mixed"}}}}}}}`), nil)
	req, err := c.Prepare("POST /s", &openapi.Input{Body: []byte("\"raw\"\n")})
	if err != nil {
		t.Fatalf("a []byte body refused: %v", err)
	}
	if got := string(editionBody(t, req)); got != "\"raw\"\n" {
		t.Fatalf("a []byte body sends %q, want its bytes", got)
	}
	plain := []byte("hi")
	named := namedBytes("hi")
	for _, v := range []struct {
		name  string
		value any
	}{
		{"named byte slice", named},
		{"pointer to a byte slice", &plain},
		{"pointer to a named byte slice", &named},
	} {
		data, err := json.Marshal(v.value)
		if err != nil || string(data) != `"aGk="` {
			t.Fatalf("%s: json.Marshal gives %s, %v; want \"aGk=\"", v.name, data, err)
		}
		for _, tc := range []struct{ name, key, media string }{
			{"JSON Lines body", "POST /s", ""},
			{"multipart/mixed body", "POST /p", "multipart/mixed; boundary=B"},
		} {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				_, err := c.Prepare(tc.key, &openapi.Input{MediaType: tc.media, Body: v.value})
				if err == nil {
					t.Fatal("prepared a byte slice as a list")
				}
				re := asRequestError(t, err)
				wantKeys(t, "Inputs", re.Inputs, true, "Input.Body")
				if msg := fmt.Sprint(re.Inputs["Input.Body"]); !strings.Contains(msg, "byte slice") {
					t.Errorf("refusal %q does not name the byte slice a list excludes", msg)
				}
			})
		}
		t.Run(v.name+"/multipart/mixed part content", func(t *testing.T) {
			prepare := func(content any) string {
				req, err := c.Prepare("POST /m", &openapi.Input{MediaType: "multipart/form-data; boundary=B", Body: map[string]any{"nm": content}})
				if err != nil {
					return "refused: " + err.Error()
				}
				return string(editionBody(t, req))
			}
			if got, want := prepare(v.value), prepare("aGk="); got != want {
				t.Errorf("gives %q; want %q, as the string \"aGk=\" gives", got, want)
			}
		})
	}
}

// withMarshaler holds a json.Marshaler in a struct field.
type withMarshaler struct {
	J json.Marshaler `json:"j"`
	T string         `json:"t,omitempty"`
}

// doc.go, Values: "The client first converts a value to JSON data as
// encoding/json would". encoding/json calls the MarshalJSON of a
// json.Marshaler interface value holding a nil pointer, so inside a value a
// struct field, map value or slice element of type json.Marshaler holding a
// nil *nilReceiver is written as "nil-receiver", as json.Marshal writes
// {"j":"nil-receiver"}, the control. It is so written, never omitted nor
// written as null, as a member of an application/json form property, as a
// struct field and as a map value of a form body, and as an element of a form
// property's array. A whole list body's element is another matter: client.go,
// Input.Body: "each element is one item, encoded as that value on its own
// would be, so a list and an iterator yielding the same values send the same
// bytes", and a positional part "a value encoded by that part's media type as
// that value on its own would be". On its own the element is the bare nil
// *nilReceiver, which json.Marshal writes as null, so as a JSON Lines item it
// is null, and as an OpenAPI 3.2 positional multipart/mixed part, whose JSON
// data is null, it is omitted (doc.go, Values: "a positional part ..., whose
// JSON data is null is omitted").
func TestNilReceiverInsideValueSent(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `
		"/f":{"post":{"requestBody":{"content":{"application/x-www-form-urlencoded":{
			"schema":{"type":"object","properties":{"j":{},"o":{},"t":{"type":"string"}}},
			"encoding":{"j":{"contentType":"application/json"},"o":{"contentType":"application/json"}}}}}}},
		"/s":{"post":{"requestBody":{"content":{"application/jsonl":{}}}}},
		"/p":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/json"}}}}}}`), nil)
	var m json.Marshaler = (*nilReceiver)(nil)
	if data, err := json.Marshal(withMarshaler{J: m}); err != nil || string(data) != `{"j":"nil-receiver"}` {
		t.Fatalf("json.Marshal gives %s, %v; want {\"j\":\"nil-receiver\"}", data, err)
	}
	const item = `"nil-receiver"`
	for _, tc := range []struct {
		name, key, media string
		body             any
		want             string
	}{
		{"form property member", "POST /f", "", map[string]any{"o": withMarshaler{J: m}}, "o=" + url.QueryEscape(`{"j":"nil-receiver"}`)},
		{"form property map member", "POST /f", "", map[string]any{"o": map[string]json.Marshaler{"j": m}}, "o=" + url.QueryEscape(`{"j":"nil-receiver"}`)},
		{"form body struct field", "POST /f", "", withMarshaler{J: m, T: "x"}, "j=" + url.QueryEscape(item) + "&t=x"},
		{"form body map value", "POST /f", "", map[string]json.Marshaler{"j": m}, "j=" + url.QueryEscape(item)},
		{"form array element", "POST /f", "", map[string]any{"j": []json.Marshaler{m}}, "j=" + url.QueryEscape(item)},
		{"JSON Lines item", "POST /s", "", []json.Marshaler{m}, "null\n"},
		{"positional part", "POST /p", "multipart/mixed; boundary=B", []json.Marshaler{m}, "--B--\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := c.Prepare(tc.key, &openapi.Input{MediaType: tc.media, Body: tc.body})
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got := string(editionBody(t, req)); got != tc.want {
				t.Errorf("body %q, want %q", got, tc.want)
			}
		})
	}
}
