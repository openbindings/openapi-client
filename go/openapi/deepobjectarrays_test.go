package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Options.DeepObjectArrays. client.go, Options.DeepObjectArrays: "says how
// the deepObject style writes an array inside its value, which OpenAPI
// leaves undefined, wherever the client writes a value in that style. An
// array it does not write, every array when it is zero, is refused at the
// value's key in RequestError.Inputs, and RequestError.Settings names
// "Options.DeepObjectArrays"." DeepObjectArrays: "Each item takes the
// array's name with a suffix; a member of an object item adds [member]
// after it. Indexes number the items written, from 0, so an item skipped as
// undefined takes none. Brackets and member names are percent-encoded as
// the rest of the name is." BracketArrays: "a[]=1&a[]=2; an item that is an
// object or array is refused"; IndexArrays: "a[0]=1&a[1]=2, and a[0][b]=x
// for an object item". doc.go, Fixed rules, Styles: "Nesting in any style
// but deepObject is refused, and so are an array in a deepObject value
// unless Options.DeepObjectArrays says how to write it"; "Whether a value is
// undefined (see Values) is settled first; the refusals here apply to
// defined values"; Percent-encoding: path and query values and "parameter
// and member names encode every byte outside RFC 3986's unreserved set as
// %XX in uppercase hex ... So deepObject nests objects as
// a%5Bb%5D%5Bc%5D=v". OAS 3.1.2 section 4.8.12.3, Style Values (3.0.4
// section 4.7.12.3, 3.2.1 section 4.12.3): deepObject "Allows objects with
// scalar properties to be represented using form parameters. The
// representation of array or object properties is not defined."

// deepObjectArraysSetting is the Settings key that names the setting when
// an array is refused.
const deepObjectArraysSetting = "Options.DeepObjectArrays"

// refusedArray is the outcome, in place of a query or body, of an array the
// setting does not write (every array under RefuseArrays; an object or
// array item under BracketArrays): refused at the value's key in Inputs,
// with Settings naming Options.DeepObjectArrays.
const refusedArray = "\x00refused array"

var deepSettings = []struct {
	name string
	v    openapi.DeepObjectArrays
}{
	{"RefuseArrays", openapi.RefuseArrays},
	{"BracketArrays", openapi.BracketArrays},
	{"IndexArrays", openapi.IndexArrays},
}

// deepArraysDoc has a deepObject query parameter p, the same with
// allowReserved, a form body whose field p has a deepObject Encoding beside
// a text field c, and a form-style array parameter the setting must not
// touch.
const deepArraysDoc = `
	"/q":{"get":{"operationId":"q","parameters":[{"name":"p","in":"query","style":"deepObject","explode":true,"schema":{"type":"object"}}]}},
	"/r":{"get":{"operationId":"r","parameters":[{"name":"p","in":"query","style":"deepObject","allowReserved":true,"schema":{"type":"object"}}]}},
	"/f":{"post":{"operationId":"f","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"c":{"type":"string"},"p":{}}},
		"encoding":{"p":{"style":"deepObject","explode":true}}}}}}},
	"/plain":{"get":{"operationId":"plain","parameters":[{"name":"p","in":"query","schema":{"type":"array","items":{"type":"string"}}}]}}`

// lineItem is an object item whose members keep their declared order
// (doc.go, Fixed rules, Order: "a struct's fields in declaration order").
type lineItem struct {
	Price string `json:"price"`
	Qty   int    `json:"qty"`
}

// ordered keeps an array between two scalar members, in declaration order.
type ordered struct {
	Z string   `json:"z"`
	A []string `json:"a"`
	M string   `json:"m"`
}

// deepCases are deepObject values for p and the query each setting writes,
// without the leading "p" name repeated in the comments: the array's name
// is the member path, written with %5B and %5D, and its items take the
// suffix %5B%5D or %5Bindex%5D.
func deepCases() []struct {
	name                   string
	v                      any
	refuse, bracket, index string
} {
	twelve := make([]string, 12)
	var bracket12, index12 []string
	for i := range twelve {
		twelve[i] = fmt.Sprintf("v%d", i)
		bracket12 = append(bracket12, fmt.Sprintf("p%%5Bn%%5D%%5B%%5D=v%d", i))
		index12 = append(index12, fmt.Sprintf("p%%5Bn%%5D%%5B%d%%5D=v%d", i, i))
	}
	nested := "p%5Ba%5D%5Bb%5D%5Bc%5D=v"
	return []struct {
		name                   string
		v                      any
		refuse, bracket, index string
	}{
		// Values without arrays keep their existing form under every
		// setting.
		{"nested objects", map[string]any{"a": map[string]any{"b": map[string]any{"c": "v"}}}, nested, nested, nested},
		// An empty array is undefined, and undefinedness is settled
		// before any refusal, so the member is skipped.
		{"empty array member skipped", map[string]any{"a": []string{}, "c": "x"}, "p%5Bc%5D=x", "p%5Bc%5D=x", "p%5Bc%5D=x"},
		// Scalar items, in order, each value percent-encoded as a query
		// value is: a space %20, "/" %2F, a number and a boolean in their
		// JSON spelling, UTF-8 bytes as %XX.
		{"scalar items", map[string]any{"tags": []any{"a b", "c/d", 1, true, "é"}}, refusedArray,
			"p%5Btags%5D%5B%5D=a%20b&p%5Btags%5D%5B%5D=c%2Fd&p%5Btags%5D%5B%5D=1&p%5Btags%5D%5B%5D=true&p%5Btags%5D%5B%5D=%C3%A9",
			"p%5Btags%5D%5B0%5D=a%20b&p%5Btags%5D%5B1%5D=c%2Fd&p%5Btags%5D%5B2%5D=1&p%5Btags%5D%5B3%5D=true&p%5Btags%5D%5B4%5D=%C3%A9"},
		// A member name is percent-encoded as the rest of the name is.
		{"member name encoded", map[string]any{"a b": []string{"x"}}, refusedArray,
			"p%5Ba%20b%5D%5B%5D=x", "p%5Ba%20b%5D%5B0%5D=x"},
		// The array's items stay in place among the object's members.
		{"order among members", ordered{"1", []string{"x", "y"}, "2"}, refusedArray,
			"p%5Bz%5D=1&p%5Ba%5D%5B%5D=x&p%5Ba%5D%5B%5D=y&p%5Bm%5D=2",
			"p%5Bz%5D=1&p%5Ba%5D%5B0%5D=x&p%5Ba%5D%5B1%5D=y&p%5Bm%5D=2"},
		// An array in a deeper member takes its whole member path.
		{"array in a deep member", map[string]any{"a": map[string]any{"b": []int{1, 2}}}, refusedArray,
			"p%5Ba%5D%5Bb%5D%5B%5D=1&p%5Ba%5D%5Bb%5D%5B%5D=2",
			"p%5Ba%5D%5Bb%5D%5B0%5D=1&p%5Ba%5D%5Bb%5D%5B1%5D=2"},
		// Indexes are decimal, past nine too.
		{"twelve items", map[string]any{"n": twelve}, refusedArray,
			strings.Join(bracket12, "&"), strings.Join(index12, "&")},
		// An object item: BracketArrays refuses it; IndexArrays adds
		// [member] after the index.
		{"object items", map[string]any{"items": []lineItem{{"p1", 2}, {"p2", 1}}}, refusedArray, refusedArray,
			"p%5Bitems%5D%5B0%5D%5Bprice%5D=p1&p%5Bitems%5D%5B0%5D%5Bqty%5D=2&p%5Bitems%5D%5B1%5D%5Bprice%5D=p2&p%5Bitems%5D%5B1%5D%5Bqty%5D=1"},
		// An array item: refused by BracketArrays; with IndexArrays,
		// nested arrays follow the same rule.
		{"nested arrays", map[string]any{"m": [][]string{{"a", "b"}, {"c"}}}, refusedArray, refusedArray,
			"p%5Bm%5D%5B0%5D%5B0%5D=a&p%5Bm%5D%5B0%5D%5B1%5D=b&p%5Bm%5D%5B1%5D%5B0%5D=c"},
		{"array inside an object item", map[string]any{"l": []any{map[string]any{"b": []string{"x", "y"}}}}, refusedArray, refusedArray,
			"p%5Bl%5D%5B0%5D%5Bb%5D%5B0%5D=x&p%5Bl%5D%5B0%5D%5Bb%5D%5B1%5D=y"},
		// The value itself an array: its name is the parameter's.
		{"top-level array", []string{"customer", "invoice.lines"}, refusedArray,
			"p%5B%5D=customer&p%5B%5D=invoice.lines", "p%5B0%5D=customer&p%5B1%5D=invoice.lines"},
		{"top-level array of objects", []any{map[string]string{"a": "1"}}, refusedArray, refusedArray, "p%5B0%5D%5Ba%5D=1"},
		// Undefined items, a null, an empty array and an object whose
		// members are all undefined, are skipped (doc.go, Values: "An
		// undefined member or array item is skipped"), before any refusal
		// (Fixed rules, Styles: "the refusals here apply to defined
		// values"), and take no index.
		{"skipped items take no index", map[string]any{"a": []any{nil, "x", []int{}, map[string]any{"b": nil}, "y"}}, refusedArray,
			"p%5Ba%5D%5B%5D=x&p%5Ba%5D%5B%5D=y", "p%5Ba%5D%5B0%5D=x&p%5Ba%5D%5B1%5D=y"},
		{"skipped object item takes no index", map[string]any{"l": []any{map[string]any{"a": nil}, map[string]any{"a": "1"}}}, refusedArray, refusedArray,
			"p%5Bl%5D%5B0%5D%5Ba%5D=1"},
		{"skipped top-level item takes no index", []any{nil, "x"}, refusedArray, "p%5B%5D=x", "p%5B0%5D=x"},
	}
}

// checkDeep compares a call's outcome with want, for the parameter or body
// field at key.
func checkDeep(t *testing.T, w *wire, before int, resp *openapi.Response, err error, key, want string, got func(rec) string) {
	t.Helper()
	if want == refusedArray {
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Inputs", re.Inputs, true, key)
		wantKeys(t, "Settings", re.Settings, true, deepObjectArraysSetting)
		return
	}
	if err != nil {
		t.Fatalf("Call: %v; want %q", err, want)
	}
	if w.count() != before+1 {
		t.Fatalf("server received %d requests, want 1", w.count()-before)
	}
	if g := got(w.last(t)); g != want {
		t.Errorf("sent\n %q\nwant\n %q", g, want)
	}
}

func pickDeep(setting openapi.DeepObjectArrays, refuse, bracket, index string) string {
	switch setting {
	case openapi.BracketArrays:
		return bracket
	case openapi.IndexArrays:
		return index
	}
	return refuse
}

// Every case under every setting, as a deepObject query parameter.
func TestDeepObjectArraysQuery(t *testing.T) {
	w := newWire(t, nil)
	for _, s := range deepSettings {
		c := parseFor(t, w, doc31(deepArraysDoc), &openapi.Options{DeepObjectArrays: s.v})
		t.Run(s.name, func(t *testing.T) {
			for _, tt := range deepCases() {
				t.Run(tt.name, func(t *testing.T) {
					want := pickDeep(s.v, tt.refuse, tt.bracket, tt.index)
					if !strings.HasPrefix(want, "\x00") {
						want = "/q?" + want
					}
					before := w.count()
					resp, err := c.Call(t.Context(), "q", &openapi.Input{Params: map[string]any{"p": tt.v}}, nil)
					checkDeep(t, w, before, resp, err, "p", want, func(r rec) string { return r.RequestURI })
				})
			}
		})
	}
}

// The same cases as a form field whose Encoding Object uses deepObject,
// beside a field c written by the form-body encoder: "c=1+2" then the
// field's RFC 6570 expansion, in which a space is %20 (doc.go, Fixed rules,
// Form bodies: "a property whose Encoding sets style, explode or
// allowReserved is written by RFC 6570, as OpenAPI says"). A refused array
// is keyed at "Input.Body/p", the field's value (errors.go,
// RequestError.Inputs: "Input.Body" followed by a JSON Pointer).
func TestDeepObjectArraysFormField(t *testing.T) {
	w := newWire(t, nil)
	for _, s := range deepSettings {
		c := parseFor(t, w, doc31(deepArraysDoc), &openapi.Options{DeepObjectArrays: s.v})
		t.Run(s.name, func(t *testing.T) {
			for _, tt := range deepCases() {
				t.Run(tt.name, func(t *testing.T) {
					want := pickDeep(s.v, tt.refuse, tt.bracket, tt.index)
					if !strings.HasPrefix(want, "\x00") {
						want = "c=1+2&" + want
					}
					before := w.count()
					resp, err := c.Call(t.Context(), "f", &openapi.Input{Body: map[string]any{"c": "1 2", "p": tt.v}}, nil)
					checkDeep(t, w, before, resp, err, "Input.Body/p", want, func(r rec) string { return string(r.Body) })
				})
			}
		})
	}
}

// The client writes a multipart/form-data field in an Encoding style too
// (doc.go, Values: "a form or multipart field serialized by a style";
// describe.go, Param.ContentType: "Under application/x-www-form-urlencoded
// and multipart/form-data it is empty for a field whose Encoding sets
// style, explode or allowReserved ... In OpenAPI 3.0, these Encoding fields
// apply only under application/x-www-form-urlencoded"), so the setting
// applies there in OpenAPI 3.1 and 3.2. OAS 3.1.2 Appendix C: "the query
// string names are placed in the name parameter of the Content-Disposition
// part header, and the values are placed in the corresponding part body;
// the ?, =, and & characters are not used, and URI percent encoding is not
// applied"; doc.go, Fixed rules, Form bodies: "Multipart/form-data fields
// are never URI percent-encoded". So each pair is a text/plain part whose
// name keeps its brackets literal, as the rest of the name is written.
func TestDeepObjectArraysMultipartField(t *testing.T) {
	w := newWire(t, nil)
	text := func(name, content string) wantPart {
		return wantPart{disposition: formData(name), ctype: "text/plain", content: content}
	}
	c12 := text("c", "1 2")
	cases := []struct {
		name            string
		v               any
		bracket, index  []wantPart // nil for a refused array
		refusedByRefuse bool
	}{
		{"nested objects", map[string]any{"a": map[string]any{"b": map[string]any{"c": "v"}}},
			[]wantPart{c12, text("p[a][b][c]", "v")}, []wantPart{c12, text("p[a][b][c]", "v")}, false},
		{"scalar items", map[string]any{"tags": []any{"a b", "c/d", 1, true, "é"}},
			[]wantPart{c12, text("p[tags][]", "a b"), text("p[tags][]", "c/d"), text("p[tags][]", "1"), text("p[tags][]", "true"), text("p[tags][]", "é")},
			[]wantPart{c12, text("p[tags][0]", "a b"), text("p[tags][1]", "c/d"), text("p[tags][2]", "1"), text("p[tags][3]", "true"), text("p[tags][4]", "é")}, true},
		{"object items", map[string]any{"items": []lineItem{{"p1", 2}}},
			nil, []wantPart{c12, text("p[items][0][price]", "p1"), text("p[items][0][qty]", "2")}, true},
		{"top-level array", []string{"x", "y"},
			[]wantPart{c12, text("p[]", "x"), text("p[]", "y")}, []wantPart{c12, text("p[0]", "x"), text("p[1]", "y")}, true},
		{"skipped items take no index", map[string]any{"a": []any{nil, "x", []int{}, "y"}},
			[]wantPart{c12, text("p[a][]", "x"), text("p[a][]", "y")}, []wantPart{c12, text("p[a][0]", "x"), text("p[a][1]", "y")}, true},
	}
	for _, version := range []string{"3.1.2", "3.2.1"} {
		doc := editionDoc(version, `"/m":{"post":{"operationId":"m","requestBody":{"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"c":{"type":"string"},"p":{}}},
			"encoding":{"p":{"style":"deepObject","explode":true}}}}},"responses":{"200":{"description":"ok"}}}}`,
			`"servers":[{"url":"@BASE@"}]`)
		for _, s := range deepSettings {
			c := parseFor(t, w, doc, &openapi.Options{DeepObjectArrays: s.v})
			for _, tt := range cases {
				t.Run(version+"/"+s.name+"/"+tt.name, func(t *testing.T) {
					var want []wantPart
					switch s.v {
					case openapi.RefuseArrays:
						if !tt.refusedByRefuse {
							want = tt.bracket
						}
					case openapi.BracketArrays:
						want = tt.bracket
					default:
						want = tt.index
					}
					before := w.count()
					resp, err := c.Call(t.Context(), "m", &openapi.Input{Body: map[string]any{"c": "1 2", "p": tt.v}}, nil)
					if want == nil {
						re := refusedSince(t, w, before, resp, err)
						wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/p")
						wantKeys(t, "Settings", re.Settings, true, deepObjectArraysSetting)
						return
					}
					if err != nil {
						t.Fatalf("Call: %v", err)
					}
					got := w.last(t)
					_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
					checkParts(t, parts, want)
				})
			}
		}
	}
}

// With allowReserved, member names and values take RFC 6570 reserved
// expansion and the parameter name the unreserved rule (doc.go, Fixed
// rules, Percent-encoding: "RFC 6570 reserved expansion is used exactly,
// member names included (parameter names always follow the rule above)").
// The suffix brackets are percent-encoded as the rest of the name is, which
// for deepObject keeps every bracket %5B and %5D, as a%5Bb%5D%5Bc%5D=v does
// under allowReserved too.
func TestDeepObjectArraysAllowReserved(t *testing.T) {
	w := newWire(t, nil)
	v := map[string]any{"k/1": []string{"a/b?c", "d e"}}
	for _, s := range deepSettings {
		c := parseFor(t, w, doc31(deepArraysDoc), &openapi.Options{DeepObjectArrays: s.v})
		t.Run(s.name, func(t *testing.T) {
			want := pickDeep(s.v, refusedArray,
				"/r?p%5Bk/1%5D%5B%5D=a/b?c&p%5Bk/1%5D%5B%5D=d%20e",
				"/r?p%5Bk/1%5D%5B0%5D=a/b?c&p%5Bk/1%5D%5B1%5D=d%20e")
			before := w.count()
			resp, err := c.Call(t.Context(), "r", &openapi.Input{Params: map[string]any{"p": v}}, nil)
			checkDeep(t, w, before, resp, err, "p", want, func(r rec) string { return r.RequestURI })
		})
	}
}

// The setting governs deepObject alone: a form-style array is written as
// before under every setting (OAS 3.1.2 section 4.8.12.6: form, explode
// true, "color=blue&color=black&color=brown").
func TestDeepObjectArraysOtherStylesUnchanged(t *testing.T) {
	w := newWire(t, nil)
	for _, s := range deepSettings {
		c := parseFor(t, w, doc31(deepArraysDoc), &openapi.Options{DeepObjectArrays: s.v})
		mustCall(t, c, "plain", &openapi.Input{Params: map[string]any{"p": []string{"blue", "black", "brown"}}}, nil)
		if got := w.last(t).RequestURI; got != "/plain?p=blue&p=black&p=brown" {
			t.Errorf("%s: request target %q", s.name, got)
		}
	}
}

// stripeDoc is a document shaped like a widely used form API: a query
// parameter expand, an array with style deepObject, and a form body whose
// expand and metadata fields have deepObject Encoding Objects.
func stripeDoc(version string) string {
	return editionDoc(version, `
		"/v1/charges":{"post":{"operationId":"createCharge",
			"parameters":[{"name":"expand","in":"query","style":"deepObject","explode":true,"schema":{"type":"array","items":{"type":"string"}}}],
			"requestBody":{"content":{"application/x-www-form-urlencoded":{
				"schema":{"type":"object","properties":{"amount":{"type":"integer"},"expand":{"type":"array","items":{"type":"string"}},"metadata":{"type":"object","additionalProperties":{"type":"string"}}}},
				"encoding":{"expand":{"style":"deepObject","explode":true},"metadata":{"style":"deepObject","explode":true}}}}},
			"responses":{"200":{"description":"ok"}}}}`, `"servers":[{"url":"@BASE@"}]`)
}

type charge struct {
	Amount   int               `json:"amount"`
	Expand   []string          `json:"expand,omitempty"`
	Metadata map[string]string `json:"metadata"`
}

// The form API case, in each edition that has deepObject (OAS 3.0.4,
// 3.1.2 and 3.2.1; Swagger 2.0 has no style), under each setting: the query
// parameter and the body field are written by the same rule, and a call
// without arrays is sent under every setting. The body's fields follow the
// struct's order; amount is written by the form-body encoder.
func TestDeepObjectArraysFormAPI(t *testing.T) {
	w := newWire(t, nil)
	in := &openapi.Input{
		Params: map[string]any{"expand": []string{"customer", "invoice.lines"}},
		Body:   charge{2000, []string{"balance_transaction"}, map[string]string{"order_id": "6735"}},
	}
	plain := &openapi.Input{Body: charge{Amount: 2000, Metadata: map[string]string{"order_id": "6735"}}}
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		for _, s := range deepSettings {
			t.Run(version+"/"+s.name, func(t *testing.T) {
				c := parseFor(t, w, stripeDoc(version), &openapi.Options{DeepObjectArrays: s.v})
				before := w.count()
				resp, err := c.Call(t.Context(), "createCharge", in, nil)
				switch s.v {
				case openapi.RefuseArrays:
					// Both values are refused, each at its own key
					// (errors.go, RequestError: "It reports every
					// independently detectable problem").
					re := refusedSince(t, w, before, resp, err)
					wantKeys(t, "Inputs", re.Inputs, true, "expand", "Input.Body/expand")
					wantKeys(t, "Settings", re.Settings, true, deepObjectArraysSetting)
				default:
					if err != nil {
						t.Fatalf("Call: %v", err)
					}
					wantQuery := pickDeep(s.v, "", "/v1/charges?expand%5B%5D=customer&expand%5B%5D=invoice.lines", "/v1/charges?expand%5B0%5D=customer&expand%5B1%5D=invoice.lines")
					wantBody := pickDeep(s.v, "", "amount=2000&expand%5B%5D=balance_transaction&metadata%5Border_id%5D=6735", "amount=2000&expand%5B0%5D=balance_transaction&metadata%5Border_id%5D=6735")
					got := w.last(t)
					if got.RequestURI != wantQuery || string(got.Body) != wantBody {
						t.Errorf("sent %q with body %q;\nwant %q with body %q", got.RequestURI, got.Body, wantQuery, wantBody)
					}
				}
				mustCall(t, c, "createCharge", plain, nil)
				if got := w.last(t); got.RequestURI != "/v1/charges" || string(got.Body) != "amount=2000&metadata%5Border_id%5D=6735" {
					t.Errorf("without arrays: sent %q with body %q", got.RequestURI, got.Body)
				}
			})
		}
	}
}

// An OpenAPI 3.2 querystring parameter under
// application/x-www-form-urlencoded is "an object written by the form-body
// rules, Encoding included, and is not encoded again" (doc.go, Fixed
// rules, Querystring), so a field whose Encoding uses deepObject is a form
// field the setting governs.
func TestDeepObjectArraysQuerystring(t *testing.T) {
	w := newWire(t, nil)
	doc := editionDoc("3.2.1", `"/qs":{"get":{"operationId":"qs","parameters":[{"name":"q","in":"querystring","content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"expand":{"type":"array","items":{"type":"string"}},"limit":{"type":"integer"}}},
		"encoding":{"expand":{"style":"deepObject","explode":true}}}}}],"responses":{"200":{"description":"ok"}}}}`,
		`"servers":[{"url":"@BASE@"}]`)
	type query struct {
		Expand []string `json:"expand"`
		Limit  int      `json:"limit"`
	}
	for _, s := range deepSettings {
		t.Run(s.name, func(t *testing.T) {
			c := parseFor(t, w, doc, &openapi.Options{DeepObjectArrays: s.v})
			before := w.count()
			resp, err := c.Call(t.Context(), "qs", &openapi.Input{Params: map[string]any{"q": query{[]string{"a", "b"}, 3}}}, nil)
			want := pickDeep(s.v, refusedArray, "/qs?expand%5B%5D=a&expand%5B%5D=b&limit=3", "/qs?expand%5B0%5D=a&expand%5B1%5D=b&limit=3")
			checkDeep(t, w, before, resp, err, "q", want, func(r rec) string { return r.RequestURI })
			mustCall(t, c, "qs", &openapi.Input{Params: map[string]any{"q": map[string]any{"limit": 3}}}, nil)
			if got := w.last(t).RequestURI; got != "/qs?limit=3" {
				t.Errorf("without arrays: request target %q", got)
			}
		})
	}
}

// Load fails on "a Redirects or DeepObjectArrays value that is none of its
// constants" (load.go, Load), with a *RequestError keyed
// "Options.DeepObjectArrays" (errors.go, RequestError.Settings: "A setting
// the document cannot use ... is keyed by its field, at Load or at a
// call"). Every constant is accepted, even by a document without
// deepObject, as no other condition is documented.
func TestDeepObjectArraysLoadRefusesOtherValues(t *testing.T) {
	doc := doc31(`"/x":{"get":{"operationId":"x"}}`)
	for _, v := range []openapi.DeepObjectArrays{3, -1, 100} {
		err := parseErr(t, doc, "https://api.example.test", &openapi.Options{DeepObjectArrays: v})
		wantKeys(t, fmt.Sprintf("Parse with %d: Settings", v), asRequestError(t, err).Settings, false, deepObjectArraysSetting)

		l := &openapi.Loader{Fetch: mapFetch(map[string]string{testDocURI: expand(doc, "https://api.example.test")})}
		c, err := l.Load(context.Background(), testDocURI, &openapi.Options{DeepObjectArrays: v})
		if err == nil || c != nil {
			t.Errorf("Load with %d = %v, %v; want a *RequestError", v, c, err)
			continue
		}
		wantKeys(t, fmt.Sprintf("Load with %d: Settings", v), asRequestError(t, err).Settings, false, deepObjectArraysSetting)
	}
	for _, s := range deepSettings {
		if _, err := openapi.Parse(context.Background(), []byte(expand(doc, "https://api.example.test")), testDocURI, &openapi.Options{DeepObjectArrays: s.v}); err != nil {
			t.Errorf("Parse with %s: %v", s.name, err)
		}
	}
}

// client.go, Client.With: "f receives a copy" of the Options, "may reset
// any field to its zero value to restore the default", "Nothing f does
// affects c", and "the new Client keeps a copy of the Options f leaves";
// "any other Options the document cannot use refuse each call they
// affect". So the setting carries across With unless f changes it.
func TestDeepObjectArraysWith(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(deepArraysDoc), &openapi.Options{DeepObjectArrays: openapi.BracketArrays})
	v := map[string]any{"a": []string{"x", "y"}}
	send := func(t *testing.T, c *openapi.Client) (int, *openapi.Response, error) {
		before := w.count()
		resp, err := c.Call(t.Context(), "q", &openapi.Input{Params: map[string]any{"p": v}}, nil)
		return before, resp, err
	}
	const bracket, index = "/q?p%5Ba%5D%5B%5D=x&p%5Ba%5D%5B%5D=y", "/q?p%5Ba%5D%5B0%5D=x&p%5Ba%5D%5B1%5D=y"
	target := func(r rec) string { return r.RequestURI }

	kept := c.With(func(o *openapi.Options) {
		if o.DeepObjectArrays != openapi.BracketArrays {
			t.Errorf("With's f received DeepObjectArrays %d, want BracketArrays", o.DeepObjectArrays)
		}
		o.Header = http.Header{"X-Tenant": {"t1"}}
	})
	t.Run("kept", func(t *testing.T) {
		before, resp, err := send(t, kept)
		checkDeep(t, w, before, resp, err, "p", bracket, target)
	})
	t.Run("changed", func(t *testing.T) {
		before, resp, err := send(t, c.With(func(o *openapi.Options) { o.DeepObjectArrays = openapi.IndexArrays }))
		checkDeep(t, w, before, resp, err, "p", index, target)
	})
	t.Run("reset to the default", func(t *testing.T) {
		before, resp, err := send(t, c.With(func(o *openapi.Options) { o.DeepObjectArrays = 0 }))
		checkDeep(t, w, before, resp, err, "p", refusedArray, target)
	})
	t.Run("original unchanged", func(t *testing.T) {
		before, resp, err := send(t, c)
		checkDeep(t, w, before, resp, err, "p", bracket, target)
	})
	t.Run("a value that is none of the constants", func(t *testing.T) {
		before, resp, err := send(t, c.With(func(o *openapi.Options) { o.DeepObjectArrays = 9 }))
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Settings", re.Settings, false, deepObjectArraysSetting)
	})
	t.Run("from the zero setting", func(t *testing.T) {
		z := parseFor(t, w, doc31(deepArraysDoc), nil)
		before, resp, err := send(t, z)
		checkDeep(t, w, before, resp, err, "p", refusedArray, target)
		before, resp, err = send(t, z.With(func(o *openapi.Options) { o.DeepObjectArrays = openapi.BracketArrays }))
		checkDeep(t, w, before, resp, err, "p", bracket, target)
	})
}

// A refusal under RefuseArrays describes the setting that would send the
// value: its text names Options.DeepObjectArrays (errors.go, RequestError:
// "Error describes every problem and the field that fixes each").
func TestDeepObjectArraysRefusalNamesTheSetting(t *testing.T) {
	c := parseAt(t, doc31(deepArraysDoc), "https://api.example.test", testDocURI, nil)
	_, err := c.Prepare("q", &openapi.Input{Params: map[string]any{"p": map[string]any{"a": []int{1}}}})
	var re *openapi.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("Prepare: %v, want a *RequestError", err)
	}
	if !strings.Contains(err.Error(), deepObjectArraysSetting) {
		t.Errorf("error %q does not name %s", err, deepObjectArraysSetting)
	}
}
