package openapi_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 4, application/x-www-form-urlencoded bodies, checked byte for byte.
// client.go, Input.Body: "For form and multipart media, Body is an object
// (a map or a struct) whose properties are the fields. A property may be a
// []byte, an io.Reader or a [Part]; an array property sends one field or
// part per item under the property's name, unless its collectionFormat or
// Encoding style says otherwise." doc.go, Fixed rules, Form bodies: "Form
// bodies use the WHATWG application/x-www-form-urlencoded encoder in every
// edition (a space as +, letters, digits and *-._ literal, every other byte
// as %XX), except that a property whose Encoding sets style, explode or
// allowReserved is written by RFC 6570, as OpenAPI says"; Order: "a form or
// multipart body's fields, follow the order encoding/json writes members in
// (a struct's fields in declaration order, a map's keys sorted)"; Values:
// "A form or multipart property or array item whose JSON data is null is
// omitted, whatever its serialization", and a field "uses its Encoding
// contentType ..., or its default type when the Encoding gives none; a list
// or a range requires Part.MediaType. The default is read from the field's
// schema" (doc.go, Configuration). OAS 3.1.2 section 4.8.15.1.1 gives the
// defaults: no type application/octet-stream, a string with contentEncoding
// application/octet-stream, a string text/plain, a number, integer or
// boolean text/plain, an object application/json, an array "according to
// the type of the items schema". Section 4.8.15.2: the body "MUST be
// encoded per [RFC1866] when passed to the server, after any complex
// objects have been serialized to a string representation"; Appendix E.4:
// each content-encoded field "is encoded based on the media type (e.g.
// text/plain or application/json), and must then be percent-encoded".

const formDoc = `
	"/form":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{
			"s":{"type":"string"},
			"i":{"type":"integer"},
			"x":{"type":"number"},
			"b":{"type":"boolean"},
			"o":{"type":"object"},
			"a":{"type":"array","items":{"type":"string"}},
			"ao":{"type":"array","items":{"type":"object"}},
			"raw":{},
			"b64":{"type":"string","contentEncoding":"base64"},
			"j":{"type":"string"},
			"cb":{"type":"string"},
			"pick":{"type":"string"},
			"img":{"type":"string"}
		}},
		"encoding":{
			"j":{"contentType":"application/json"},
			"cb":{"contentType":"application/cbor"},
			"pick":{"contentType":"application/json, text/plain"},
			"img":{"contentType":"image/*"}
		}}}}}},
	"/bare":{"post":{"operationId":"bare","requestBody":{"content":{"application/x-www-form-urlencoded":{}}}}},
	"/oas1":{"post":{"operationId":"oas1","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"id":{"type":"string","format":"uuid"},"address":{"type":"object","properties":{}}}}}}}}},
	"/oas1j":{"post":{"operationId":"oas1j","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"id":{"type":"string","format":"uuid"}}},
		"encoding":{"id":{"contentType":"application/json"}}}}}}},
	"/oas2":{"post":{"operationId":"oas2","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"name":{"type":"string"},"icon":{"type":"string","contentEncoding":"base64url"}}},
		"encoding":{"icon":{"contentType":"image/png, image/jpeg"}}}}}}}`

// usAddress is the address of OAS 3.1.2 section 4.8.15.2.1, a struct so
// its members keep the example's order.
type usAddress struct {
	StreetAddress string `json:"streetAddress"`
	City          string `json:"city"`
	State         string `json:"state"`
	Zip           string `json:"zip"`
}

// The two examples of OAS 3.1.2 section 4.8.15.2, reproduced exactly:
// 4.8.15.2.1, "URL Encoded Form with JSON Values" (id as text/plain, the
// object as compact JSON, then encoded per RFC 1866: "space characters have
// been replaced with + and +, ", :, ,, {, and } have been percent-encoded";
// and id serialized as application/json, "id=%22f81d4fae-...%22"), and
// 4.8.15.2.2, "URL Encoded Form with Binary Values", whose icon's Encoding
// lists image/png and image/jpeg, so the call names one with Part.MediaType
// (doc.go, Configuration: "a list or a range requires Part.MediaType"); the
// base64url string is sent as its bytes, "=" padding percent-encoded.
func TestFormBodyOASExamples(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(formDoc), nil)
	const id = "f81d4fae-7dec-11d0-a765-00a0c91e6bf6"
	addr := usAddress{"123 Example Dr.", "Somewhere", "CA", "99999+1234"}
	type form1 struct {
		ID      string    `json:"id"`
		Address usAddress `json:"address"`
	}
	mustCall(t, c, "oas1", &openapi.Input{Body: form1{id, addr}}, nil)
	want := "id=f81d4fae-7dec-11d0-a765-00a0c91e6bf6&address=%7B%22streetAddress%22%3A%22123+Example+Dr.%22%2C%22city%22%3A%22Somewhere%22%2C%22state%22%3A%22CA%22%2C%22zip%22%3A%2299999%2B1234%22%7D"
	got := w.last(t)
	if string(got.Body) != want {
		t.Errorf("4.8.15.2.1: body\n%s\nwant\n%s", got.Body, want)
	}
	if ct := got.Header.Values("Content-Type"); !slices.Equal(ct, []string{"application/x-www-form-urlencoded"}) || got.ContentLength != int64(len(want)) {
		t.Errorf("4.8.15.2.1: Content-Type %q, Content-Length %d", ct, got.ContentLength)
	}

	mustCall(t, c, "oas1j", &openapi.Input{Body: map[string]string{"id": id}}, nil)
	if got := w.last(t); string(got.Body) != "id=%22f81d4fae-7dec-11d0-a765-00a0c91e6bf6%22" {
		t.Errorf("4.8.15.2.1 as application/json: body %q", got.Body)
	}

	const icon = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAABGdBTUEAALGPC_xhBQAAADhlWElmTU0AKgAAAAgAAYdpAAQAAAABAAAAGgAAAAAAAqACAAQAAAABAAAAAqADAAQAAAABAAAAAgAAAADO0J6QAAAAEElEQVQIHWP8zwACTGCSAQANHQEDqtPptQAAAABJRU5ErkJggg=="
	type form2 struct {
		Name string `json:"name"`
		Icon any    `json:"icon"`
	}
	mustCall(t, c, "oas2", &openapi.Input{Body: form2{"example", openapi.Part{Content: icon, MediaType: "image/png"}}}, nil)
	want = "name=example&icon=iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAABGdBTUEAALGPC_xhBQAAADhlWElmTU0AKgAAAAgAAYdpAAQAAAABAAAAGgAAAAAAAqACAAQAAAABAAAAAqADAAQAAAABAAAAAgAAAADO0J6QAAAAEElEQVQIHWP8zwACTGCSAQANHQEDqtPptQAAAABJRU5ErkJggg%3D%3D"
	if got := w.last(t); string(got.Body) != want {
		t.Errorf("4.8.15.2.2: body\n%s\nwant\n%s", got.Body, want)
	}
	before := w.count()
	resp, err := c.Call(t.Context(), "oas2", &openapi.Input{Body: form2{"example", icon}}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "4.8.15.2.2 without Part.MediaType: Settings", re.Settings, true, "Input.Body/icon")
}

// Content-encoded fields, each the field's default type or its Encoding
// contentType, then WHATWG-encoded, names included.
func TestFormBodyContentFields(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(formDoc), &openapi.Options{Codecs: map[string]openapi.Codec{"application/cbor": tagCodec{tag: "CBOR"}}})
	esc := htmlEsc
	tests := []struct {
		name, key string
		body      any
		want      string
	}{
		// text/plain: a string as it is, a number or boolean in its JSON
		// spelling (doc.go, Values: "Where a parameter or a form or multipart
		// field needs text, a number or boolean is written in its JSON
		// spelling (10, 2.5, true) and a string or json.Number as it is").
		{"WHATWG bytes", "form", map[string]any{"s": "a b+c&d=e~f*g-h.i_j/k%l"}, "s=a+b%2Bc%26d%3De%7Ef*g-h.i_j%2Fk%25l"},
		{"non-ASCII", "form", map[string]any{"s": "é☃"}, "s=%C3%A9%E2%98%83"},
		{"line breaks", "form", map[string]any{"s": "a\r\nb\nc"}, "s=a%0D%0Ab%0Ac"},
		{"empty string", "form", map[string]any{"s": ""}, "s="},
		{"integer", "form", map[string]any{"i": 10}, "i=10"},
		{"float", "form", map[string]any{"x": 2.5}, "x=2.5"},
		{"exponent", "form", map[string]any{"x": 1e21}, "x=1e%2B21"},
		{"json.Number", "form", map[string]any{"x": json.Number("1.50")}, "x=1.50"},
		{"boolean", "form", map[string]any{"b": true}, "b=true"},
		// application/json for an object, as json.Marshal writes it (stage 1
		// ledger, Q6), HTML escaping included.
		{"object as JSON", "form", map[string]any{"o": map[string]any{"k": "v w", "n": nil}}, "o=%7B%22k%22%3A%22v+w%22%2C%22n%22%3Anull%7D"},
		{"JSON HTML escaping", "form", map[string]any{"o": map[string]string{"k": "<&>"}}, "o=" + formEnc(`{"k":"`+esc("003c")+esc("0026")+esc("003e")+`"}`)},
		{"empty object as JSON", "form", map[string]any{"o": map[string]any{}}, "o=%7B%7D"},
		// Arrays: one field per item, typed by the items schema.
		{"array of strings", "form", map[string]any{"a": []string{"x y", "z"}}, "a=x+y&a=z"},
		{"array of objects", "form", map[string]any{"ao": []any{map[string]int{"a": 1}, map[string]int{"b": 2}}}, "ao=%7B%22a%22%3A1%7D&ao=%7B%22b%22%3A2%7D"},
		{"empty array", "form", map[string]any{"a": []string{}, "s": "x"}, "s=x"},
		// application/octet-stream (a schema without type, or a string with
		// contentEncoding): a string, []byte or reader as its bytes.
		{"bytes", "form", map[string]any{"raw": []byte{0, 0xff, 'a', ' '}}, "raw=%00%FFa+"},
		{"string as octets", "form", map[string]any{"raw": "a b"}, "raw=a+b"},
		{"reader", "form", map[string]any{"raw": strings.NewReader("r d&")}, "raw=r+d%26"},
		{"contentEncoding", "form", map[string]any{"b64": "aGk="}, "b64=aGk%3D"},
		// An Encoding contentType.
		{"application/json string", "form", map[string]any{"j": "x"}, "j=%22x%22"},
		{"caller codec", "form", map[string]any{"cb": 5}, "cb=CBOR%285%29"},
		{"Part selects JSON from a list", "form", map[string]any{"pick": openapi.Part{Content: map[string]int{"a": 1}, MediaType: "application/json"}}, "pick=%7B%22a%22%3A1%7D"},
		{"Part selects text from a list", "form", map[string]any{"pick": openapi.Part{Content: "t t", MediaType: "text/plain"}}, "pick=t+t"},
		{"Part under a range", "form", map[string]any{"img": openapi.Part{Content: []byte("\x89P"), MediaType: "image/png"}}, "img=%89P"},
		// Names are encoded as values are; a property without a schema is
		// application/octet-stream and takes a string.
		{"names encoded", "bare", map[string]string{"a b": "1", "~*": "2", "é": "3"}, "a+b=1&%7E*=2&%C3%A9=3"},
		{"empty name", "bare", map[string]string{"": "v"}, "=v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, c, tt.key, &openapi.Input{Body: tt.body}, nil)
			got := w.last(t)
			if string(got.Body) != tt.want {
				t.Errorf("body %q, want %q", got.Body, tt.want)
			}
			if ct := got.Header.Values("Content-Type"); !slices.Equal(ct, []string{"application/x-www-form-urlencoded"}) {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

// formOrder has fields out of alphabetical order, one renamed, one
// skipped, one omitted when empty, and an embedded struct whose field is
// promoted, as encoding/json treats them.
type formOrder struct {
	S     string `json:"s"`
	B     bool   `json:"b"`
	Skip  string `json:"-"`
	Empty string `json:"x,omitempty"`
	formEmbedded
	I int `json:"i"`
}

type formEmbedded struct {
	A []string `json:"a"`
}

// formKey is a map key encoded through MarshalText, as encoding/json
// encodes map keys.
type formKey int

func (k formKey) MarshalText() ([]byte, error) { return []byte(fmt.Sprint("k", 10-int(k))), nil }

// Field order: "the order encoding/json writes members in (a struct's
// fields in declaration order, a map's keys sorted)" (doc.go, Fixed rules,
// Order); items keep their order. Null properties and items are omitted
// (doc.go, Values). A pointer to a struct is an object, as encoding/json
// writes it.
func TestFormBodyOrderAndNulls(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(formDoc), nil)
	var nilStr *string
	tests := []struct {
		name string
		key  string
		body any
		want string
	}{
		{"map keys sorted", "form", map[string]any{"s": "1", "i": 2, "b": true}, "b=true&i=2&s=1"},
		{"struct order", "form", formOrder{S: "x", B: true, Skip: "no", formEmbedded: formEmbedded{A: []string{"p", "q"}}, I: 3}, "s=x&b=true&a=p&a=q&i=3"},
		{"pointer to struct", "form", &formOrder{S: "y"}, "s=y&b=false&i=0"},
		{"TextMarshaler keys sorted by text", "bare", map[formKey]string{1: "one", 2: "two"}, "k8=two&k9=one"},
		{"null property omitted", "form", map[string]any{"s": nil, "i": 1}, "i=1"},
		{"typed nil property omitted", "form", struct {
			S *string `json:"s"`
			I int     `json:"i"`
		}{nilStr, 1}, "i=1"},
		{"null items omitted", "form", map[string]any{"a": []any{"x", nil, "y"}}, "a=x&a=y"},
		{"null JSON field omitted", "form", map[string]any{"o": nil, "s": "v"}, "s=v"},
		{"empty body", "form", map[string]any{}, ""},
		{"only nulls", "form", map[string]any{"s": nil}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, c, tt.key, &openapi.Input{Body: tt.body}, nil)
			got := w.last(t)
			if string(got.Body) != tt.want {
				t.Errorf("body %q, want %q", got.Body, tt.want)
			}
			if got.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || got.ContentLength != int64(len(tt.want)) {
				t.Errorf("Content-Type %q, Content-Length %d", got.Header.Get("Content-Type"), got.ContentLength)
			}
		})
	}
}

// formStyleCfgs are the Encoding Objects with RFC 6570 fields for a form
// property p: OAS 3.1.2 section 4.8.15.1.2 ("The behavior follows the same
// values as query parameters, including the default value of "form" which
// applies only when contentType is not being used due to one or both of
// explode or allowReserved being explicitly specified"; explode "When style
// is "form", the default value is true. For all other styles, the default
// value is false").
var formStyleCfgs = []struct {
	id, encoding string
	style        string
	explode      bool
	reserved     bool
}{
	{"formExplode", `{"style":"form","explode":true}`, "form", true, false},
	{"formFlat", `{"style":"form","explode":false}`, "form", false, false},
	{"explodeOnly", `{"explode":false}`, "form", false, false},
	{"reservedOnly", `{"allowReserved":true}`, "form", true, true},
	{"reservedFlat", `{"explode":false,"allowReserved":true}`, "form", false, true},
	{"space", `{"style":"spaceDelimited","explode":false}`, "spaceDelimited", false, false},
	{"pipe", `{"style":"pipeDelimited","explode":false,"allowReserved":true}`, "pipeDelimited", false, true},
	{"deep", `{"style":"deepObject"}`, "deepObject", true, false},
	{"deepReserved", `{"style":"deepObject","explode":true,"allowReserved":true}`, "deepObject", true, true},
}

// formStyleDoc has one operation per formStyleCfgs entry, each a form body
// whose property p has that Encoding and whose property c is text/plain.
func formStyleDoc() string {
	var ops []string
	for _, cfg := range formStyleCfgs {
		ops = append(ops, fmt.Sprintf(`"/%s":{"post":{"operationId":%q,"requestBody":{"content":{"application/x-www-form-urlencoded":{`+
			`"schema":{"type":"object","properties":{"c":{"type":"string"},"p":{}}},"encoding":{"p":%s}}}}}}`, cfg.id, cfg.id, cfg.encoding))
	}
	return doc31(strings.Join(ops, ","))
}

// A property whose Encoding sets style, explode or allowReserved is
// written by RFC 6570, against the oracle (styledField), its "?" prefix
// removed, beside a content field c: "c=1+2" then the expansion, joined by
// "&". A space is %20 there, not +, as RFC 6570 percent-encodes it;
// allowReserved is RFC 6570 reserved expansion exactly (doc.go, Fixed
// rules, Percent-encoding: "the caller supplies any percent-encoding
// OpenAPI leaves to the application"; OAS 3.1.2: "Applications are still
// responsible for percent-encoding reserved characters that ... have a
// special meaning in application/x-www-form-urlencoded"). Undefined values
// are omitted, and a value a style refuses is refused at
// Inputs["Input.Body/p"] (stage brief, Refusals; doc.go, Fixed rules,
// Styles: each "refused at the parameter's key").
func TestFormBodyStyledFields(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, formStyleDoc(), nil)
	for _, cfg := range formStyleCfgs {
		t.Run(cfg.id, func(t *testing.T) {
			for _, tt := range styleCorpus {
				t.Run(tt.name, func(t *testing.T) {
					field, o := styledField(t, "p", cfg.style, cfg.explode, cfg.reserved, tt.v)
					before := w.count()
					resp, err := c.Call(t.Context(), cfg.id, &openapi.Input{Body: map[string]any{"c": "1 2", "p": tt.v}}, nil)
					if o == refused {
						re := refusedSince(t, w, before, resp, err)
						wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/p")
						return
					}
					if err != nil {
						t.Fatalf("Call: %v; want %q", err, field)
					}
					want := formJoin("c=1+2", field)
					if got := w.last(t); string(got.Body) != want {
						t.Errorf("body %q, want %q", got.Body, want)
					}
				})
			}
		})
	}
}

// OAS 3.1.2 section 4.8.12.6's style examples, as form fields: color with
// ["blue","black","brown"] and {"R":100,"G":200,"B":150}.
func TestFormBodyStyleExamples(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, formStyleDoc(), nil)
	arr := []string{"blue", "black", "brown"}
	for _, tt := range []struct {
		key  string
		v    any
		want string
	}{
		{"formExplode", arr, "p=blue&p=black&p=brown"},
		{"formFlat", arr, "p=blue,black,brown"},
		{"formExplode", colorRGB, "R=100&G=200&B=150"},
		{"formFlat", colorRGB, "p=R,100,G,200,B,150"},
		{"space", arr, "p=blue%20black%20brown"},
		{"pipe", arr, "p=blue%7Cblack%7Cbrown"},
		{"deep", colorRGB, "p%5BR%5D=100&p%5BG%5D=200&p%5BB%5D=150"},
		{"reservedOnly", "a/b?c", "p=a/b?c"},
		{"formExplode", "a b", "p=a%20b"},
	} {
		mustCall(t, c, tt.key, &openapi.Input{Body: map[string]any{"p": tt.v}}, nil)
		if got := w.last(t); string(got.Body) != tt.want {
			t.Errorf("%s %v: body %q, want %q", tt.key, tt.v, got.Body, tt.want)
		}
	}
}

// Refusals, each at its Inputs key: "Input.Body" followed by a JSON
// Pointer to the part of Body concerned (errors.go, RequestError.Inputs;
// RFC 6901, "~" as "~0" and "/" as "~1"): a body that is not an object
// (client.go, Input.Body: "Body is an object (a map or a struct)"); a
// property or item value its media type cannot encode (doc.go, Values; stage
// brief, Refusals); a reader or Part inside a JSON value (client.go,
// Input.Body: "A Part or io.Reader inside a JSON value is refused with an
// Inputs entry at its place in Body"). A field's media type that the call
// must choose, or chose outside the Encoding's list, is a setting:
// Settings["Input.Body/<field>"] (errors.go, RequestError.Settings: "for a
// part's media type, "Input.Body" followed by the part's JSON Pointer";
// client.go, Part.MediaType: "A range is refused").
func TestFormBodyRefusals(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(formDoc), nil)
	tests := []struct {
		name     string
		key      string
		body     any
		inputs   []string
		settings []string
	}{
		{"a string body", "form", "s=x", []string{"Input.Body"}, nil},
		{"an array body", "form", []string{"a"}, []string{"Input.Body"}, nil},
		{"a number body", "form", 5, []string{"Input.Body"}, nil},
		{"object under text/plain", "form", map[string]any{"s": map[string]string{"a": "b"}}, []string{"Input.Body/s"}, nil},
		{"array item under text/plain", "form", map[string]any{"a": []any{"x", []string{"y"}}}, []string{"Input.Body/a/1"}, nil},
		{"number under octet-stream", "form", map[string]any{"raw": 5}, []string{"Input.Body/raw"}, nil},
		{"number without a codec", "form", map[string]any{"cb": 5}, []string{"Input.Body/cb"}, nil},
		{"object under octet-stream, escaped name", "bare", map[string]any{"a/b~": map[string]int{"c": 1}}, []string{"Input.Body/a~1b~0"}, nil},
		{"reader inside JSON", "form", map[string]any{"o": map[string]any{"f": strings.NewReader("x")}}, []string{"Input.Body/o/f"}, nil},
		{"Part inside JSON", "form", map[string]any{"o": map[string]any{"p": openapi.Part{Content: "x"}}}, []string{"Input.Body/o/p"}, nil},
		{"two problems", "form", map[string]any{"s": map[string]int{"x": 1}, "raw": true}, []string{"Input.Body/raw", "Input.Body/s"}, nil},
		{"a list needs Part.MediaType", "form", map[string]any{"pick": "x"}, nil, []string{"Input.Body/pick"}},
		{"a range needs Part.MediaType", "form", map[string]any{"img": []byte("x")}, nil, []string{"Input.Body/img"}},
		{"a range given", "form", map[string]any{"pick": openapi.Part{Content: "x", MediaType: "text/*"}}, nil, []string{"Input.Body/pick"}},
		{"a type not offered", "form", map[string]any{"pick": openapi.Part{Content: "x", MediaType: "application/xml"}}, nil, []string{"Input.Body/pick"}},
		{"an item's type not offered", "form", map[string]any{"pick": []any{openapi.Part{Content: "x", MediaType: "text/plain"}, openapi.Part{Content: "y", MediaType: "text/csv"}}}, nil, []string{"Input.Body/pick/1"}},
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

// A form body is a value the client encodes: Prepare encodes it once, so
// HTTP.Body and every GetBody give the same bytes (doc.go, Fixed rules,
// Form bodies: "A body is encoded once, so HTTP.Body and every GetBody
// give the same bytes"), with Content-Length (Header fields: "for a body
// that can be sent again").
func TestFormBodyPrepared(t *testing.T) {
	c := parseAt(t, doc31(formDoc), "https://api.example.test", testDocURI, nil)
	req := mustPrepare(t, c, "form", &openapi.Input{Body: map[string]any{"s": "a b", "a": []string{"1", "2"}, "o": map[string]int{"k": 1}}})
	const want = "a=1&a=2&o=%7B%22k%22%3A1%7D&s=a+b"
	if got := string(preparedBody(t, req)); got != want {
		t.Errorf("GetBody gave %q, want %q", got, want)
	}
	if got := string(preparedBody(t, req)); got != want {
		t.Errorf("a second GetBody gave %q", got)
	}
	if req.HTTP.ContentLength != int64(len(want)) || req.HTTP.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("ContentLength %d, Content-Type %q", req.HTTP.ContentLength, req.HTTP.Header.Get("Content-Type"))
	}
	op := mustOp(t, c, "form")
	if req.Media != reqMedia(t, op, 0) {
		t.Errorf("Request.Media is not Operation.Body.Media[0]")
	}
}
