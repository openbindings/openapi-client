package openapi_test

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 4, multipart bodies from an object, parsed back with
// mime/multipart and checked part by part: names, filenames, media types,
// header fields and content. client.go, Input.Body: "For form and multipart
// media, Body is an object (a map or a struct) whose properties are the
// fields. A property may be a []byte, an io.Reader or a [Part] (or a
// non-nil *Part); a property whose value is an array sends one field or
// part per item under the property's name, unless its collectionFormat or
// Encoding style says otherwise, each item taking the property's content
// type (an array schema's items type by default); any other value is one
// field or part. ... A multipart object with no fields sends the close
// delimiter alone ("--" boundary "--" CRLF), as browsers do."
// Part: "A part's name and filename are written in its Content-Disposition
// as given, each as a quoted-string with \ and " escaped, never as
// filename*; a control character other than a tab in either is refused, as
// a quoted-string cannot carry it"; Filename "Empty means the default: the
// part's name for a []byte or io.Reader Content whose media type is not
// multipart, and none otherwise"; NoFilename "sends no filename, whatever
// the Content"; Header "holds other header fields of the part, such as those
// its Encoding declares, under the rules for Options.Header's field names
// and values. A Content-Disposition field replaces the one the client
// writes; Content-Type, in any spelling, is refused (set MediaType)". doc.go,
// Fixed rules, Form bodies: "Multipart/form-data fields are never URI
// percent-encoded." OAS 3.1.2 section 4.8.15.1.1 gives each part's default
// Content-Type from its schema, and section 4.8.15.3: "Array properties are
// handled by applying the same name to multiple parts, as is recommended by
// [RFC7578] Section 4.3". RFC 7578 section 4.2: "Each part MUST contain a
// Content-Disposition header field where the disposition type is
// "form-data"". Stage 4 ledger, test round (6e13978), Q4 to Q7, Q9, Q11,
// Q13 to Q15.

const mpPaths = `
	"/oas1":{"post":{"operationId":"oas1","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{
		"id":{"type":"string","format":"uuid"},
		"profileImage":{},
		"addresses":{"type":"array","items":{"$ref":"#/components/schemas/Address"}}}}}}}}},
	"/oas2":{"post":{"operationId":"oas2","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{
			"id":{"type":"string","format":"uuid"},
			"addresses":{"description":"addresses in XML format","type":"array","items":{"$ref":"#/components/schemas/Address"}},
			"profileImage":{}}},
		"encoding":{
			"addresses":{"contentType":"application/xml; charset=utf-8"},
			"profileImage":{"contentType":"image/png, image/jpeg","headers":{"X-Rate-Limit-Limit":{"description":"The number of allowed requests in the current period","schema":{"type":"integer"}}}}}}}}}},
	"/oas3":{"post":{"operationId":"oas3","requestBody":{"content":{"multipart/form-data":{"schema":{"properties":{"file":{"type":"array","items":{}}}}}}}}},
	"/mp":{"post":{"operationId":"mp","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{
			"title":{"type":"string"},
			"n":{"type":"integer"},
			"ok":{"type":"boolean"},
			"meta":{"type":"object"},
			"blob":{},
			"tags":{"type":"array","items":{"type":"string"}},
			"doc":{},
			"img":{},
			"any":{},
			"bundle":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"object"}}}}},
		"encoding":{
			"doc":{"contentType":"application/pdf"},
			"img":{"contentType":"image/*"},
			"bundle":{"contentType":"multipart/mixed"}}}}}}},
	"/raw":{"post":{"operationId":"raw","requestBody":{"content":{"multipart/form-data":{}}}}},
	"/mixed":{"post":{"operationId":"mixed","requestBody":{"content":{"multipart/mixed":{"schema":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}}}}}},
	"/styled":{"post":{"operationId":"styled","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"d":{},"ex":{},"f1":{},"f2":{},"pp":{},"sp":{}}},
		"encoding":{
			"d":{"style":"deepObject"},
			"ex":{"explode":true},
			"f1":{"style":"form","explode":true},
			"f2":{"style":"form","explode":false,"allowReserved":true},
			"pp":{"style":"pipeDelimited"},
			"sp":{"style":"spaceDelimited"}}}}}}}`

const mpComponents = `"components":{"schemas":{"Address":{"type":"object","properties":{"street":{"type":"string"},"city":{"type":"string"}}}}}`

func mpDoc() string { return doc31(mpPaths, mpComponents) }

// mpAddress is OAS 3.1.2's Address, a struct so its members keep their
// order.
type mpAddress struct {
	Street string `json:"street"`
	City   string `json:"city"`
}

// sendMultipart calls key with in and returns the request the server
// received and its parts, checking that its Content-Type is the media type
// with one parameter, its boundary (client.go, Input.MediaType: "a boundary
// given for a multipart body the client encodes is used", else one is
// generated), and that a body of values is sent with Content-Length (doc.go,
// Fixed rules, Header fields: "Content-Length for a body that can be sent
// again").
func sendMultipart(t *testing.T, w *wire, c *openapi.Client, key, mediaType string, body any) (rec, []mpart) {
	t.Helper()
	mustCall(t, c, key, &openapi.Input{Body: body}, nil)
	got := w.last(t)
	ct := got.Header.Values("Content-Type")
	if len(ct) != 1 {
		t.Fatalf("Content-Type = %q", ct)
	}
	mt, params, err := mime.ParseMediaType(ct[0])
	if err != nil || mt != mediaType || len(params) != 1 || params["boundary"] == "" {
		t.Errorf("Content-Type %q, want %s with a boundary alone", ct[0], mediaType)
	}
	if got.ContentLength != int64(len(got.Body)) || len(got.TransferEncoding) != 0 {
		t.Errorf("Content-Length %d, Transfer-Encoding %q, for %d bytes", got.ContentLength, got.TransferEncoding, len(got.Body))
	}
	_, _, parts := readMultipart(t, ct[0], got.Body)
	return got, parts
}

// The examples of OAS 3.1.2 section 4.8.15.3: 4.8.15.3.1, "Basic Multipart
// Form" (id "default content type for a string without contentEncoding is
// text/plain", profileImage "default content type for a schema without
// type is application/octet-stream", addresses "the default content type
// for each item is application/json"); 4.8.15.3.2, "Multipart Form with
// Encoding Objects" (each addresses item "application/xml; charset=utf-8",
// profileImage "accepts only PNG or JPEG, and also describes a custom
// header for just this part", here given by Part.Header since the client
// never invents a value: doc.go, Values, "Schema defaults are never sent");
// 4.8.15.3.3, "Multipart Form with Multiple Files" ("the empty schema for
// items indicates a media type of application/octet-stream"). A []byte or
// reader part is named after its property for its filename (client.go,
// Part.Filename).
func TestMultipartOASExamples(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	const id = "f81d4fae-7dec-11d0-a765-00a0c91e6bf6"
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00")
	type form1 struct {
		ID           string      `json:"id"`
		ProfileImage []byte      `json:"profileImage"`
		Addresses    []mpAddress `json:"addresses"`
	}
	_, parts := sendMultipart(t, w, c, "oas1", "multipart/form-data", form1{id, png, []mpAddress{{"1 Main St", "Somewhere"}, {"2 Side St", "Elsewhere"}}})
	checkParts(t, parts, []wantPart{
		{disposition: formData("id"), ctype: "text/plain", content: id},
		{disposition: formData("profileImage", "profileImage"), ctype: "application/octet-stream", content: string(png)},
		{disposition: formData("addresses"), ctype: "application/json", content: `{"street":"1 Main St","city":"Somewhere"}`},
		{disposition: formData("addresses"), ctype: "application/json", content: `{"street":"2 Side St","city":"Elsewhere"}`},
	})

	type form2 struct {
		ID           string   `json:"id"`
		Addresses    []string `json:"addresses"`
		ProfileImage any      `json:"profileImage"`
	}
	xml1, xml2 := "<Address><street>1 Main St</street></Address>", "<Address><street>2 Side St</street></Address>"
	_, parts = sendMultipart(t, w, c, "oas2", "multipart/form-data", form2{id, []string{xml1, xml2},
		openapi.Part{Content: png, MediaType: "image/png", Header: http.Header{"X-Rate-Limit-Limit": {"10"}}}})
	checkParts(t, parts, []wantPart{
		{disposition: formData("id"), ctype: "text/plain", content: id},
		{disposition: formData("addresses"), ctype: "application/xml; charset=utf-8", content: xml1},
		{disposition: formData("addresses"), ctype: "application/xml; charset=utf-8", content: xml2},
		{disposition: formData("profileImage", "profileImage"), ctype: "image/png", content: string(png), extra: http.Header{"X-Rate-Limit-Limit": {"10"}}},
	})
	// Without a codec, XML takes a string; a list requires Part.MediaType.
	before := w.count()
	resp, err := c.Call(t.Context(), "oas2", &openapi.Input{Body: map[string]any{
		"addresses": []mpAddress{{"1 Main St", "Somewhere"}}, "profileImage": png,
	}}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/addresses/0")
	wantKeys(t, "Settings", re.Settings, true, "Input.Body/profileImage")

	_, parts = sendMultipart(t, w, c, "oas3", "multipart/form-data", map[string]any{"file": []any{[]byte("first"), strings.NewReader("second")}})
	checkParts(t, parts, []wantPart{
		{disposition: formData("file", "file"), ctype: "application/octet-stream", content: "first"},
		{disposition: formData("file", "file"), ctype: "application/octet-stream", content: "second"},
	})
}

// Default part types by the property's schema (OAS 3.1.2 section
// 4.8.15.1.1), never by the Go value (doc.go, Configuration: "The default
// is read from the field's schema ..., never from the Go value"); text as
// doc.go, Values, says; a JSON part "exactly what its codec writes, null
// members, [] and {} included", as json.Marshal writes it (stage 1 ledger,
// Q6); null properties and items omitted (doc.go, Values); fields in the
// order encoding/json writes them (doc.go, Fixed rules, Order); content
// never percent-encoded (doc.go, Fixed rules, Form bodies).
func TestMultipartFieldDefaults(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{
		"title": "Q3 report: a+b c&d=é\r\n",
		"n":     5,
		"ok":    true,
		"meta":  map[string]any{"k": "<", "z": nil, "e": []int{}},
		"blob":  []byte{0, 1, '\r', '\n', '-', '-'},
		"tags":  []string{"finance", "q3"},
		"any":   "a plain string",
	})
	checkParts(t, parts, []wantPart{
		{disposition: formData("any"), ctype: "application/octet-stream", content: "a plain string"},
		{disposition: formData("blob", "blob"), ctype: "application/octet-stream", content: "\x00\x01\r\n--"},
		{disposition: formData("meta"), ctype: "application/json", content: `{"e":[],"k":"` + htmlEsc("003c") + `","z":null}`},
		{disposition: formData("n"), ctype: "text/plain", content: "5"},
		{disposition: formData("ok"), ctype: "text/plain", content: "true"},
		{disposition: formData("tags"), ctype: "text/plain", content: "finance"},
		{disposition: formData("tags"), ctype: "text/plain", content: "q3"},
		{disposition: formData("title"), ctype: "text/plain", content: "Q3 report: a+b c&d=é\r\n"},
	})

	type ordered struct {
		Title string   `json:"title"`
		Tags  []any    `json:"tags"`
		N     any      `json:"n"`
		Meta  *string  `json:"meta"`
		Skip  string   `json:"-"`
		Blob  []string `json:"blob,omitempty"`
	}
	_, parts = sendMultipart(t, w, c, "mp", "multipart/form-data", ordered{Title: "t", Tags: []any{"a", nil, "b"}, N: 2.5, Skip: "x"})
	checkParts(t, parts, []wantPart{
		{disposition: formData("title"), ctype: "text/plain", content: "t"},
		{disposition: formData("tags"), ctype: "text/plain", content: "a"},
		{disposition: formData("tags"), ctype: "text/plain", content: "b"},
		{disposition: formData("n"), ctype: "text/plain", content: "2.5"},
	})

	_, parts = sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{"blob": strings.NewReader("from a reader"), "n": nil})
	checkParts(t, parts, []wantPart{
		{disposition: formData("blob", "blob"), ctype: "application/octet-stream", content: "from a reader"},
	})
}

// Part overrides one at a time: Filename, NoFilename, MediaType (a
// concrete type matching one the Encoding lists, by the rules on
// Response.Media, or any concrete type where it lists none; sent as the
// part's Content-Type) and Header; and the Content-Disposition escaping and
// filename defaults of client.go, Part.
func TestMultipartPartOverrides(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	tests := []struct {
		name, key string
		prop      string
		v         any
		want      wantPart
	}{
		{"Filename", "mp", "doc", openapi.Part{Content: []byte("%PDF"), Filename: "q3.pdf"}, wantPart{disposition: formData("doc", "q3.pdf"), ctype: "application/pdf", content: "%PDF"}},
		{"NoFilename", "mp", "doc", openapi.Part{Content: []byte("%PDF"), NoFilename: true}, wantPart{disposition: formData("doc"), ctype: "application/pdf", content: "%PDF"}},
		{"bytes default", "mp", "doc", []byte("%PDF"), wantPart{disposition: formData("doc", "doc"), ctype: "application/pdf", content: "%PDF"}},
		{"reader default", "mp", "doc", openapi.Part{Content: strings.NewReader("%PDF")}, wantPart{disposition: formData("doc", "doc"), ctype: "application/pdf", content: "%PDF"}},
		{"Filename for text", "mp", "title", openapi.Part{Content: "s", Filename: "s.txt"}, wantPart{disposition: formData("title", "s.txt"), ctype: "text/plain", content: "s"}},
		{"value has no filename", "mp", "any", openapi.Part{Content: map[string]int{"a": 1}, MediaType: "application/json"}, wantPart{disposition: formData("any"), ctype: "application/json", content: `{"a":1}`}},
		{"MediaType with parameters", "mp", "title", openapi.Part{Content: "s", MediaType: "text/plain; charset=utf-8"}, wantPart{disposition: formData("title"), ctype: "text/plain; charset=utf-8", content: "s"}},
		{"MediaType under a range, as given", "mp", "img", openapi.Part{Content: []byte("x"), MediaType: "IMAGE/PNG"}, wantPart{disposition: formData("img", "img"), ctype: "IMAGE/PNG", content: "x"}},
		{"Header field", "mp", "title", openapi.Part{Content: "x", Header: http.Header{"X-Checksum": {"abc"}}}, wantPart{disposition: formData("title"), ctype: "text/plain", content: "x", extra: http.Header{"X-Checksum": {"abc"}}}},
		{"Header Content-Disposition replaces", "mp", "title", openapi.Part{Content: "x", Header: http.Header{"Content-Disposition": {`form-data; name="other"; filename="o.txt"`}}}, wantPart{disposition: `form-data; name="other"; filename="o.txt"`, ctype: "text/plain", content: "x"}},
		{"filename escaped", "mp", "doc", openapi.Part{Content: []byte("x"), Filename: `a"b\c é.pdf`}, wantPart{disposition: `form-data; name="doc"; filename="a\"b\\c é.pdf"`, ctype: "application/pdf", content: "x"}},
		{"filename as given", "mp", "doc", openapi.Part{Content: []byte("x"), Filename: "../etc/pass wd%41"}, wantPart{disposition: formData("doc", "../etc/pass wd%41"), ctype: "application/pdf", content: "x"}},
		{"name escaped", "raw", `n"a\me`, "v", wantPart{disposition: `form-data; name="n\"a\\me"`, ctype: "application/octet-stream", content: "v"}},
		{"name as given", "raw", "é a/b", "v", wantPart{disposition: formData("é a/b"), ctype: "application/octet-stream", content: "v"}},
		{"a tab in a name", "raw", "a\tb", "v", wantPart{disposition: formData("a\tb"), ctype: "application/octet-stream", content: "v"}},
		{"a tab in a filename", "mp", "doc", openapi.Part{Content: []byte("x"), Filename: "a\tb.pdf"}, wantPart{disposition: formData("doc", "a\tb.pdf"), ctype: "application/pdf", content: "x"}},
		{"a tab inside a Header value", "mp", "title", openapi.Part{Content: "x", Header: http.Header{"X-Note": {"a\tb"}}}, wantPart{disposition: formData("title"), ctype: "text/plain", content: "x", extra: http.Header{"X-Note": {"a\tb"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, parts := sendMultipart(t, w, c, tt.key, "multipart/form-data", map[string]any{tt.prop: tt.v})
			checkParts(t, parts, []wantPart{tt.want})
		})
	}
}

// Refusals, at the keys errors.go gives: Inputs for a value that cannot be
// sent ("a Part that sets both Filename and NoFilename"; client.go, Part: "a
// control character other than a tab in either is refused", Header "under
// the rules for Options.Header's field names and values" and
// "Content-Type, in any spelling, is refused"; stage 4 ledger, Q11: "Part.Header
// names must be tokens and values pass the header-field rule (C3-7)", the
// doc.go Credentials rule of "a control character other than a tab, or ...
// leading or trailing whitespace"; stage brief, Refusals: "a property or part
// value its media type cannot encode"; client.go, Input.Body: a Part or
// reader "inside a JSON value is refused with an Inputs entry at its place
// in Body", the pointer passing through a Part with no Content segment
// (ledger, Q14), and Body is an object, a slice being an OpenAPI 3.2
// shape), and Settings for a part's media type the call must give or gave
// wrongly (RequestError.Settings: "for a part's media type, "Input.Body"
// followed by the part's JSON Pointer"; Part.MediaType: "A range is
// refused").
func TestMultipartRefusals(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	tests := []struct {
		name     string
		key      string
		body     any
		inputs   []string
		settings []string
	}{
		{"Filename with NoFilename", "mp", map[string]any{"doc": openapi.Part{Content: []byte("x"), Filename: "x", NoFilename: true}}, []string{"Input.Body/doc"}, nil},
		{"Content-Type in Header", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"Content-Type": {"text/csv"}}}}, []string{"Input.Body/title"}, nil},
		{"content-type in Header", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"content-type": {"text/csv"}}}}, []string{"Input.Body/title"}, nil},
		{"CR in a name", "raw", map[string]any{"a\rb": "x"}, []string{"Input.Body/a\rb"}, nil},
		{"LF in a name", "raw", map[string]any{"a\nb": "x"}, []string{"Input.Body/a\nb"}, nil},
		{"LF in a filename", "mp", map[string]any{"doc": openapi.Part{Content: []byte("x"), Filename: "a\nb"}}, []string{"Input.Body/doc"}, nil},
		{"CR in a filename", "mp", map[string]any{"doc": openapi.Part{Content: []byte("x"), Filename: "a\rb"}}, []string{"Input.Body/doc"}, nil},
		{"NUL in a name", "raw", map[string]any{"a\x00b": "x"}, []string{"Input.Body/a\x00b"}, nil},
		{"ESC in a name", "raw", map[string]any{"a\x1bb": "x"}, []string{"Input.Body/a\x1bb"}, nil},
		{"DEL in a name", "raw", map[string]any{"a\x7fb": "x"}, []string{"Input.Body/a\x7fb"}, nil},
		{"NUL in a filename", "mp", map[string]any{"doc": openapi.Part{Content: []byte("x"), Filename: "a\x00"}}, []string{"Input.Body/doc"}, nil},
		{"DEL in a filename", "mp", map[string]any{"doc": openapi.Part{Content: []byte("x"), Filename: "a\x7f"}}, []string{"Input.Body/doc"}, nil},
		{"Header name not a token", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"X Bad": {"v"}}}}, []string{"Input.Body/title"}, nil},
		{"Header name with a colon", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"X:Bad": {"v"}}}}, []string{"Input.Body/title"}, nil},
		{"Header value with CR LF", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"X-A": {"a\r\nX-Injected: 1"}}}}, []string{"Input.Body/title"}, nil},
		{"Header value with LF", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"X-A": {"a\nb"}}}}, []string{"Input.Body/title"}, nil},
		{"Header value with NUL", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"X-A": {"a\x00b"}}}}, []string{"Input.Body/title"}, nil},
		{"Header value with another control", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"X-A": {"a\x01b"}}}}, []string{"Input.Body/title"}, nil},
		{"Header value with leading space", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"X-A": {" a"}}}}, []string{"Input.Body/title"}, nil},
		{"Header value with trailing tab", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"X-A": {"a\t"}}}}, []string{"Input.Body/title"}, nil},
		{"CONTENT-TYPE in Header", "mp", map[string]any{"title": openapi.Part{Content: "x", Header: http.Header{"CONTENT-TYPE": {"text/csv"}}}}, []string{"Input.Body/title"}, nil},
		{"reader inside a Part's JSON", "mp", map[string]any{"any": openapi.Part{Content: map[string]any{"r": strings.NewReader("x")}, MediaType: "application/json"}}, []string{"Input.Body/any/r"}, nil},
		{"array items inside Parts", "mp", map[string]any{"meta": []any{map[string]int{"a": 1}, openapi.Part{Content: []any{0, strings.NewReader("x")}, MediaType: "application/json"}}}, []string{"Input.Body/meta/1/1"}, nil},
		{"object under text/plain", "mp", map[string]any{"title": map[string]string{"a": "b"}}, []string{"Input.Body/title"}, nil},
		{"number under octet-stream", "mp", map[string]any{"blob": 5}, []string{"Input.Body/blob"}, nil},
		{"object under a Part's text/plain", "mp", map[string]any{"any": openapi.Part{Content: map[string]int{"a": 1}, MediaType: "text/plain"}}, []string{"Input.Body/any"}, nil},
		{"array item under text/plain", "mp", map[string]any{"tags": []any{"a", map[string]int{"b": 1}}}, []string{"Input.Body/tags/1"}, nil},
		{"reader inside a JSON part", "mp", map[string]any{"meta": map[string]any{"r": strings.NewReader("x")}}, []string{"Input.Body/meta/r"}, nil},
		{"Part inside a JSON part", "mp", map[string]any{"meta": map[string]any{"p": openapi.Part{Content: "x"}}}, []string{"Input.Body/meta/p"}, nil},
		{"a string body", "mp", "title=x", []string{"Input.Body"}, nil},
		{"a slice body in OpenAPI 3.1", "mp", []any{map[string]string{"title": "x"}}, []string{"Input.Body"}, nil},
		{"a range needs Part.MediaType", "mp", map[string]any{"img": []byte("x")}, nil, []string{"Input.Body/img"}},
		{"a range given", "mp", map[string]any{"img": openapi.Part{Content: []byte("x"), MediaType: "image/*"}}, nil, []string{"Input.Body/img"}},
		{"a type not offered", "mp", map[string]any{"doc": openapi.Part{Content: []byte("x"), MediaType: "image/png"}}, nil, []string{"Input.Body/doc"}},
		{"a range without an Encoding", "mp", map[string]any{"title": openapi.Part{Content: "x", MediaType: "text/*"}}, nil, []string{"Input.Body/title"}},
		{"not a media type", "mp", map[string]any{"title": openapi.Part{Content: "x", MediaType: "text"}}, nil, []string{"Input.Body/title"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), tt.key, &openapi.Input{Body: tt.body}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, tt.inputs...)
			wantKeys(t, "Settings", re.Settings, true, tt.settings...)
		})
	}
}

// The boundary: generated, as RFC 2046 section 5.1.1 allows it
// (readMultipart checks it), or given (client.go, Input.MediaType:
// "MediaType may carry parameters, which are sent as given: a pre-encoded
// multipart body requires its boundary here, and a boundary given for a
// multipart body the client encodes is used, a part whose content holds its
// delimiter, or "--" and the boundary after a CR or LF, being an input that
// cannot be encoded (RFC 2046 section 5.1.1)"; Options.MediaType selects "as
// Input.MediaType does"), a quoted
// one included (RFC 2046: "bchars :=
// bcharsnospace / " "", so a boundary may hold a space, and RFC 9110
// section 8.3.1 quotes such a parameter value).
func TestMultipartBoundary(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	body := map[string]any{"title": "x", "blob": []byte("y")}
	for _, tt := range []struct {
		name      string
		client    *openapi.Client
		mediaType string // Input.MediaType
		ct        string // the Content-Type sent
		boundary  string
	}{
		{"Input.MediaType", c, "multipart/form-data; boundary=b0und4ry", "multipart/form-data; boundary=b0und4ry", "b0und4ry"},
		{"quoted", c, `multipart/form-data; boundary="a b'c:d"`, `multipart/form-data; boundary="a b'c:d"`, "a b'c:d"},
		{"Options.MediaType", c.With(func(o *openapi.Options) { o.MediaType = "multipart/form-data; boundary=from-options" }), "", "multipart/form-data; boundary=from-options", "from-options"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, tt.client, "mp", &openapi.Input{Body: body, MediaType: tt.mediaType}, nil)
			got := w.last(t)
			if ct := got.Header.Values("Content-Type"); len(ct) != 1 || ct[0] != tt.ct {
				t.Fatalf("Content-Type = %q, want %q", ct, tt.ct)
			}
			_, b, parts := readMultipart(t, tt.ct, got.Body)
			if b != tt.boundary || !strings.Contains(string(got.Body), "--"+tt.boundary+"\r\n") {
				t.Errorf("boundary %q not used in %q", tt.boundary, got.Body)
			}
			checkParts(t, parts, []wantPart{
				{disposition: formData("blob", "blob"), ctype: "application/octet-stream", content: "y"},
				{disposition: formData("title"), ctype: "text/plain", content: "x"},
			})
		})
	}

	// A pre-encoded body is sent as given, with its boundary in
	// Input.MediaType, which it requires.
	const pre = "--pre\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\nv\r\n--pre--\r\n"
	for _, b := range []any{[]byte(pre), strings.NewReader(pre)} {
		mustCall(t, c, "raw", &openapi.Input{Body: b, MediaType: "multipart/form-data; boundary=pre"}, nil)
		if got := w.last(t); string(got.Body) != pre || got.Header.Get("Content-Type") != "multipart/form-data; boundary=pre" {
			t.Errorf("%T: sent %q as %q", b, got.Body, got.Header.Get("Content-Type"))
		}
	}
	for _, tt := range []struct {
		body      any
		mediaType string
	}{
		{[]byte(pre), ""},
		{strings.NewReader(pre), ""},
		{[]byte(pre), "multipart/form-data"},
		{[]byte(pre), "multipart/form-data; charset=utf-8"},
	} {
		before := w.count()
		resp, err := c.Call(t.Context(), "raw", &openapi.Input{Body: tt.body, MediaType: tt.mediaType}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Settings", re.Settings, false, "Input.MediaType")
	}
}

// A part whose media type is multipart, encoded one level deep (client.go,
// Input.Body: "A part whose media type is multipart is encoded, one level
// deep, from an object or slice by its Encoding's own encoding,
// prefixEncoding or itemEncoding; a []byte or io.Reader supplies it
// pre-encoded, with its boundary in Part.MediaType"). In OpenAPI 3.1 an
// Encoding Object has no encoding of its own, so the nested parts take the
// defaults of the nested schema's properties, each named by its
// Content-Disposition form-data (stage 4 ledger, Q5), with exactly that
// field and its Content-Type (ledger, readings confirmed). A part whose
// media type is multipart takes no default filename (client.go,
// Part.Filename; ledger, Q15). Nesting past one level is refused at its
// Inputs key (stage brief, Refusals), the pointer passing through a Part
// (ledger, Q14).
func TestMultipartNested(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{
		"bundle": map[string]any{"a": "x y", "b": map[string]int{"k": 1}},
	})
	if len(parts) != 1 {
		t.Fatalf("%d parts, want 1", len(parts))
	}
	p := parts[0]
	if d := p.header.Get("Content-Disposition"); d != formData("bundle") || len(p.header) != 2 {
		t.Errorf("Content-Disposition %q (%d fields), want %q", d, len(p.header), formData("bundle"))
	}
	mt, params, err := mime.ParseMediaType(p.header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" || len(params) != 1 {
		t.Fatalf("nested Content-Type %q, want multipart/mixed with a boundary", p.header.Get("Content-Type"))
	}
	_, _, nested := readMultipart(t, p.header.Get("Content-Type"), p.body)
	checkParts(t, nested, []wantPart{
		{disposition: formData("a"), ctype: "text/plain", content: "x y"},
		{disposition: formData("b"), ctype: "application/json", content: `{"k":1}`},
	})

	// Pre-encoded, with its boundary in Part.MediaType, from bytes and from
	// a reader: no filename.
	const inner = "--in\r\nContent-Type: text/plain\r\n\r\nz\r\n--in--\r\n"
	for _, content := range []any{[]byte(inner), strings.NewReader(inner)} {
		_, parts = sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{
			"bundle": openapi.Part{Content: content, MediaType: "multipart/mixed; boundary=in"},
		})
		checkParts(t, parts, []wantPart{{disposition: formData("bundle"), ctype: "multipart/mixed; boundary=in", content: inner}})
	}
	// A Part naming a multipart type for a field with no Encoding.
	_, parts = sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{
		"any": openapi.Part{Content: []byte(inner), MediaType: "multipart/related; boundary=in"},
	})
	checkParts(t, parts, []wantPart{{disposition: formData("any"), ctype: "multipart/related; boundary=in", content: inner}})

	for _, tt := range []struct {
		name     string
		body     any
		inputs   []string
		settings []string
	}{
		{"pre-encoded without a boundary", map[string]any{"bundle": []byte(inner)}, nil, []string{"Input.Body/bundle"}},
		{"two levels", map[string]any{"bundle": map[string]any{"a": "x", "b": openapi.Part{MediaType: "multipart/mixed", Content: map[string]string{"c": "d"}}}}, []string{"Input.Body/bundle/b"}, nil},
		{"two levels through a Part", map[string]any{"any": openapi.Part{MediaType: "multipart/mixed", Content: map[string]any{
			"x": openapi.Part{MediaType: "multipart/mixed", Content: map[string]string{"y": "z"}}}}}, []string{"Input.Body/any/x"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "mp", &openapi.Input{Body: tt.body}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, tt.inputs...)
			wantKeys(t, "Settings", re.Settings, true, tt.settings...)
		})
	}
}

// Another multipart type with an object Body: one part per property (stage
// brief, Scope: "multipart/form-data and other multipart types (object
// Body): one part per property"), each with Content-Disposition form-data
// and its name (stage 4 ledger, Q5; OAS 3.1.2 section 4.8.15.3: such types
// may be supported "when Content-Disposition: form-data is used with a name
// parameter"), typed by its schema.
func TestMultipartMixedObject(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	_, parts := sendMultipart(t, w, c, "mixed", "multipart/mixed", map[string]any{"a": "x", "b": 7})
	checkParts(t, parts, []wantPart{
		{disposition: formData("a"), ctype: "text/plain", content: "x"},
		{disposition: formData("b"), ctype: "text/plain", content: "7"},
	})
}

// RFC 6570 fields in multipart/form-data: OAS 3.1.2 Appendix C, "When
// using style and similar keywords to produce a multipart/form-data body,
// the query string names are placed in the name parameter of the
// Content-Disposition part header, and the values are placed in the
// corresponding part body; the ?, =, and & characters are not used, and URI
// percent encoding is not applied, regardless of the value of
// allowReserved"; section 4.8.15.1.2: "When using RFC6570-style
// serialization for multipart/form-data, URI percent-encoding MUST NOT be
// applied". doc.go, Values: an undefined value (an empty array) is omitted.
// Each such part is text/plain (stage 4 ledger, Q4: RFC 7578 section 4.4's
// default, as OpenAPI says contentType is ignored), its value unencoded.
func TestMultipartStyledFields(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	_, parts := sendMultipart(t, w, c, "styled", "multipart/form-data", map[string]any{
		"d":  map[string]any{"x": "1", "y": map[string]string{"z": "2 3"}},
		"ex": map[string]string{"m": "1", "n": "2/3"},
		"f1": []string{"a b", "c/d"},
		"f2": []string{"a b", "c&d"},
		"pp": []string{"a", "b"},
		"sp": []string{"a", "b c"},
	})
	text := func(name, content string) wantPart {
		return wantPart{disposition: formData(name), ctype: "text/plain", content: content}
	}
	checkParts(t, parts, []wantPart{
		text("d[x]", "1"),
		text("d[y][z]", "2 3"),
		text("m", "1"),
		text("n", "2/3"),
		text("f1", "a b"),
		text("f1", "c/d"),
		text("f2", "a b,c&d"),
		text("pp", "a|b"),
		text("sp", "a b c"),
	})
	_, parts = sendMultipart(t, w, c, "styled", "multipart/form-data", map[string]any{"f1": []string{}, "f2": "s t"})
	checkParts(t, parts, []wantPart{text("f2", "s t")})
}

// A property's value decides how many parts it sends (stage 4 ledger, Q7;
// client.go, Input.Body): an array value, []byte excluded, one part per
// item, each typed by the items schema when the property's schema is an
// array, else by the property's own type, an Encoding contentType applying
// to each; any other value one part, typed by the items type for an array
// schema.
func TestMultipartArrayValues(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	tests := []struct {
		name string
		prop string
		v    any
		want []wantPart
	}{
		{"strings for a string", "title", []string{"a", "b"}, []wantPart{
			{disposition: formData("title"), ctype: "text/plain", content: "a"},
			{disposition: formData("title"), ctype: "text/plain", content: "b"},
		}},
		{"objects for an object", "meta", []any{map[string]int{"a": 1}, map[string]int{"b": 2}}, []wantPart{
			{disposition: formData("meta"), ctype: "application/json", content: `{"a":1}`},
			{disposition: formData("meta"), ctype: "application/json", content: `{"b":2}`},
		}},
		{"a scalar for an array", "tags", "x", []wantPart{
			{disposition: formData("tags"), ctype: "text/plain", content: "x"},
		}},
		{"bytes for an array are one part", "tags", []byte("raw"), []wantPart{
			{disposition: formData("tags", "tags"), ctype: "text/plain", content: "raw"},
		}},
		{"bytes items for an untyped property", "blob", [][]byte{[]byte("a"), []byte("b")}, []wantPart{
			{disposition: formData("blob", "blob"), ctype: "application/octet-stream", content: "a"},
			{disposition: formData("blob", "blob"), ctype: "application/octet-stream", content: "b"},
		}},
		{"an Encoding contentType for each item", "doc", []any{[]byte("%PDF-1"), strings.NewReader("%PDF-2")}, []wantPart{
			{disposition: formData("doc", "doc"), ctype: "application/pdf", content: "%PDF-1"},
			{disposition: formData("doc", "doc"), ctype: "application/pdf", content: "%PDF-2"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{tt.prop: tt.v})
			checkParts(t, parts, tt.want)
		})
	}
}

// An untyped property is application/octet-stream (OAS 3.1.2 section
// 4.8.15.1.1: type absent, application/octet-stream), whether the schema
// gives it no type or does not list it, so it takes a string, []byte or
// reader; a number is refused at its key, the error naming the media type
// (stage 4 ledger, Q6: "{"count": 5} is refused at its key, naming the
// media type. No inference from Go values").
func TestUntypedPropertyTakesOctets(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	_, parts := sendMultipart(t, w, c, "raw", "multipart/form-data", map[string]any{"count": "5"})
	checkParts(t, parts, []wantPart{{disposition: formData("count"), ctype: "application/octet-stream", content: "5"}})
	fc := parseFor(t, w, doc31(formDoc), nil)
	for _, tt := range []struct {
		c        *openapi.Client
		key, at  string
		property string
	}{
		{c, "raw", "Input.Body/count", "count"},
		{c, "mp", "Input.Body/blob", "blob"},
		{fc, "bare", "Input.Body/count", "count"},
		{fc, "form", "Input.Body/raw", "raw"},
	} {
		before := w.count()
		resp, err := tt.c.Call(t.Context(), tt.key, &openapi.Input{Body: map[string]any{tt.property: 5}}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, tt.key+" Inputs", re.Inputs, true, tt.at)
		if e := re.Inputs[tt.at]; e == nil || !strings.Contains(e.Error(), "application/octet-stream") {
			t.Errorf("%s: %v does not name application/octet-stream", tt.key, e)
		}
	}
}

// A field string that is not valid UTF-8 is sent as given (stage 4 ledger,
// Q9), as stage 2 sends a content parameter's.
func TestMultipartInvalidUTF8(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{"title": "a\xffb", "any": "\xfe"})
	checkParts(t, parts, []wantPart{
		{disposition: formData("any"), ctype: "application/octet-stream", content: "\xfe"},
		{disposition: formData("title"), ctype: "text/plain", content: "a\xffb"},
	})
}

// With a boundary the caller gives, a part whose content holds its
// delimiter, CRLF "--" boundary (RFC 2046 section 5.1.1: "The boundary
// delimiter MUST NOT appear inside any of the encapsulated parts"), or
// begins with "--" boundary, which follows the header block's CRLF (stage 4
// ledger, QQ2), cannot be encoded (client.go, Input.MediaType; ledger, Q13).
// Content held as a value, a string or a []byte is refused at its key
// before sending; content from any io.Reader, replayable or not, is checked
// as it streams, and holding it ends the body as an upload error (ledger,
// QQ3: "only content held as values, strings or []byte is checked before
// sending"). The boundary's text elsewhere is sent.
func TestMultipartGivenBoundaryInContent(t *testing.T) {
	const mt = "multipart/form-data; boundary=b0und4ry"
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	for _, tt := range []struct {
		name string
		body map[string]any
		at   string
	}{
		{"bytes", map[string]any{"blob": []byte("x\r\n--b0und4ry\r\ny")}, "Input.Body/blob"},
		{"bytes beginning with it", map[string]any{"blob": []byte("--b0und4ry\r\ny")}, "Input.Body/blob"},
		{"text", map[string]any{"title": "a\r\n--b0und4ry--"}, "Input.Body/title"},
		{"text beginning with it", map[string]any{"title": "--b0und4ry"}, "Input.Body/title"},
		{"an item", map[string]any{"tags": []string{"ok", "z\r\n--b0und4ry"}}, "Input.Body/tags/1"},
		{"a Part", map[string]any{"doc": openapi.Part{Content: []byte("\r\n--b0und4ry"), Filename: "f.pdf"}}, "Input.Body/doc"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "mp", &openapi.Input{Body: tt.body, MediaType: mt}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, tt.at)
		})
	}
	mustCall(t, c, "mp", &openapi.Input{Body: map[string]any{"title": "the b0und4ry word, --b0und4ry", "blob": []byte("x--b0und4ry\n")}, MediaType: mt}, nil)
	_, _, parts := readMultipart(t, mt, w.last(t).Body)
	checkParts(t, parts, []wantPart{
		{disposition: formData("blob", "blob"), ctype: "application/octet-stream", content: "x--b0und4ry\n"},
		{disposition: formData("title"), ctype: "text/plain", content: "the b0und4ry word, --b0und4ry"},
	})

	for _, content := range []string{"x\r\n--b0und4ry\r\ny", "--b0und4ry\r\ny"} {
		for _, r := range readerKinds(t, content) {
			t.Run(fmt.Sprintf("%s %q", r.name, content), func(t *testing.T) {
				srv := newBodyServer(t, false, "")
				sc := parseAt(t, mpDoc(), srv.URL, srv.URL+"/openapi.json", nil)
				ctx, _ := gateCtx(t)
				res := awaitCall(t, callAsync(ctx, sc, "mp", &openapi.Input{Body: map[string]any{"blob": r.reader}, MediaType: mt}), "Call")
				if res.err == nil || isRequestError(res.err) {
					t.Fatalf("Call = %v, want an upload error, not a *RequestError", res.err)
				}
				if _, err := srv.finished(t); err == nil {
					t.Errorf("the server read a complete body")
				}
			})
		}
	}
}

// readerKind is a reader of some content, of one of the kinds Input.Body
// names.
type readerKind struct {
	name   string
	reader io.Reader
}

// readerKinds returns content as every kind of reader: replayable (a
// *strings.Reader, a *bytes.Reader, a regular file) and read once.
func readerKinds(t *testing.T, content string) []readerKind {
	path := filepath.Join(t.TempDir(), "content")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return []readerKind{
		{"strings.Reader", strings.NewReader(content)},
		{"bytes.Reader", bytes.NewReader([]byte(content))},
		{"regular file", f},
		{"read once", newOnce(content)},
	}
}
