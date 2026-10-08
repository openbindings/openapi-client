package openapi_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Parameters: path (simple), query (form, explode true and false) and
// header (simple), for primitives, arrays and objects.

// rgb is a struct value, whose members encoding/json writes in declaration
// order.
type rgb struct {
	R int `json:"R"`
	G int `json:"G"`
	B int `json:"B"`
}

// tagged exercises encoding/json's struct tags.
type tagged struct {
	A string `json:"alpha"`
	B string `json:"-"`
	C string `json:"c,omitempty"`
}

// shout has a MarshalJSON method.
type shout string

func (s shout) MarshalJSON() ([]byte, error) { return json.Marshal(strings.ToUpper(string(s))) }

// coord is a non-string map key type with a MarshalText method, which
// encoding/json uses for its keys. (For a key of string kind,
// encoding/json uses the string itself and never calls MarshalText.)
type coord struct{ X, Y int }

func (k coord) MarshalText() ([]byte, error) { return []byte(fmt.Sprintf("%dx%d", k.X, k.Y)), nil }

var (
	colors    = []string{"blue", "black", "brown"}
	colorMap  = map[string]int{"R": 100, "G": 200, "B": 150} // encoding/json sorts: B, G, R
	colorRGB  = rgb{100, 200, 150}
	fixedTime = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	strPtr    = func(s string) *string { return &s }
)

type paramCase struct {
	name  string
	value any
	want  string
}

// runParamCases calls, for each case, the operation "op" of a document with
// one path and the parameter decl, and compares the request target (or,
// with header set, that header field's only value) with want.
func runParamCases(t *testing.T, path, decl, key, header string, cases []paramCase) {
	t.Helper()
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"`+path+`":{"get":{"operationId":"op","parameters":[`+decl+`]}}`), nil)
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			before := w.count()
			if _, err := c.Call(t.Context(), "op", &openapi.Input{Params: map[string]any{key: tt.value}}, nil); err != nil {
				t.Fatalf("Call: %v", err)
			}
			if w.count() != before+1 {
				t.Fatalf("server received %d requests, want 1", w.count()-before)
			}
			got := w.last(t)
			if header == "" {
				if got.RequestURI != tt.want {
					t.Errorf("request target %q, want %q", got.RequestURI, tt.want)
				}
				return
			}
			if v := got.Header.Values(header); len(v) != 1 || v[0] != tt.want {
				t.Errorf("%s = %q, want [%q]", header, v, tt.want)
			}
		})
	}
}

// OAS 3.1.2 section 4.8.12.6, style simple (the path default, section
// 4.8.12.2.2), explode false: "blue", "blue,black,brown",
// "R,100,G,200,B,150". doc.go, Values: a value is first converted to JSON
// data as encoding/json would; numbers and booleans in their JSON spelling.
// doc.go, Fixed rules, Percent-encoding: path values and member names
// "encode every byte outside RFC 3986's unreserved set as %XX in uppercase
// hex"; doc.go, Fixed rules, Order: members "follow the order encoding/json
// writes members in (a struct's fields in declaration order, a map's keys
// sorted)".
func TestPathParamSimple(t *testing.T) {
	runParamCases(t, "/items/{id}", `{"name":"id","in":"path","required":true,"schema":{}}`, "id", "", []paramCase{
		{"string", "blue", "/items/blue"},
		{"array", colors, "/items/blue,black,brown"},
		{"map sorted", colorMap, "/items/B,150,G,200,R,100"},
		{"struct in field order", colorRGB, "/items/R,100,G,200,B,150"},
		{"integer", 3, "/items/3"},
		{"float", 2.5, "/items/2.5"},
		{"boolean", true, "/items/true"},
		// doc.go, Values: "a number, boolean or json.Number is written in
		// its JSON spelling".
		{"json.Number", json.Number("9007199254740993"), "/items/9007199254740993"},
		// encoding/json writes 1e21 as 1e+21, and + is not unreserved.
		{"exponent", 1e21, "/items/1e%2B21"},
		{"percent-encoding", "a/b c,d~é%", "/items/a%2Fb%20c%2Cd~%C3%A9%25"},
		// Every RFC 3986 reserved character (section 2.2) is encoded.
		{"reserved", "!*'();:@&=+$,/?#[]", "/items/%21%2A%27%28%29%3B%3A%40%26%3D%2B%24%2C%2F%3F%23%5B%5D"},
		// RFC 3986 section 2.3: the unreserved set stays literal.
		{"unreserved", "AZaz09-._~", "/items/AZaz09-._~"},
		// The style's delimiters stay literal; a delimiter inside an item is
		// encoded (OAS 3.1.2 section 4.8.12.4).
		{"array items encoded", []string{"a,b", "c d"}, "/items/a%2Cb,c%20d"},
		{"member names encoded", map[string]string{"a b": "c/d"}, "/items/a%20b,c%2Fd"},
		// doc.go, Values: "" is a value.
		{"empty string", "", "/items/"},
		// doc.go, Values: "An undefined member or array item is skipped".
		{"null member skipped", map[string]any{"a": nil, "b": "x"}, "/items/b,x"},
		// encoding/json: MarshalJSON, struct tags, TextMarshaler keys, []byte
		// as base64, pointers followed.
		{"time", fixedTime, "/items/2024-01-02T03%3A04%3A05Z"},
		{"MarshalJSON", shout("hi"), "/items/HI"},
		{"struct tags", tagged{A: "1", B: "2"}, "/items/alpha,1"},
		{"TextMarshaler key", map[coord]string{{1, 2}: "x"}, "/items/1x2,x"},
		{"bytes as base64", []byte("hi"), "/items/aGk%3D"},
		{"pointer", strPtr("p-7"), "/items/p-7"},
	})
}

// OAS 3.1.2 section 4.8.12.6, style simple, explode true: arrays as with
// explode false, objects "R=100,G=200,B=150".
func TestPathParamSimpleExplode(t *testing.T) {
	runParamCases(t, "/items/{id}", `{"name":"id","in":"path","required":true,"explode":true,"schema":{}}`, "id", "", []paramCase{
		{"string", "blue", "/items/blue"},
		{"array", colors, "/items/blue,black,brown"},
		{"map sorted", colorMap, "/items/B=150,G=200,R=100"},
		{"struct", colorRGB, "/items/R=100,G=200,B=150"},
		{"names and values encoded", map[string]string{"a b": "c/d"}, "/items/a%20b=c%2Fd"},
	})
}

// OAS 3.1.2 section 4.8.12.6, style form (the query default), explode true
// (form's default): "color=blue", "color=blue&color=black&color=brown",
// "R=100&G=200&B=150". doc.go, Values: null, an empty array and an object
// whose members are all undefined are undefined, "an undefined optional
// parameter is omitted", and "" is a value.
func TestQueryParamFormExplode(t *testing.T) {
	runParamCases(t, "/q", `{"name":"color","in":"query","schema":{}}`, "color", "", []paramCase{
		{"string", "blue", "/q?color=blue"},
		{"array", colors, "/q?color=blue&color=black&color=brown"},
		{"map sorted", colorMap, "/q?B=150&G=200&R=100"},
		{"struct", colorRGB, "/q?R=100&G=200&B=150"},
		{"empty string", "", "/q?color="},
		{"empty items", []string{"", "a"}, "/q?color=&color=a"},
		{"empty array omitted", []string{}, "/q"},
		{"nil interface omitted", nil, "/q"},
		{"typed nil omitted", (*string)(nil), "/q"},
		{"empty object omitted", map[string]any{}, "/q"},
		{"undefined members omitted", map[string]any{"a": nil}, "/q"},
		{"null member skipped", map[string]any{"a": nil, "b": "x"}, "/q?b=x"},
		{"value encoded", "a&b=c+d e", "/q?color=a%26b%3Dc%2Bd%20e"},
		{"member encoded", map[string]string{"a b": "c&d"}, "/q?a%20b=c%26d"},
		{"integer", 10, "/q?color=10"},
		{"boolean", false, "/q?color=false"},
		{"json.Number", json.Number("12.50"), "/q?color=12.50"},
	})
}

// OAS 3.1.2 section 4.8.12.6, style form, explode false:
// "color=blue,black,brown", "color=R,100,G,200,B,150"; a comma inside an
// item is encoded, the delimiter is not.
func TestQueryParamFormNoExplode(t *testing.T) {
	runParamCases(t, "/q", `{"name":"color","in":"query","explode":false,"schema":{}}`, "color", "", []paramCase{
		{"string", "blue", "/q?color=blue"},
		{"array", colors, "/q?color=blue,black,brown"},
		{"map sorted", colorMap, "/q?color=B,150,G,200,R,100"},
		{"struct", colorRGB, "/q?color=R,100,G,200,B,150"},
		{"items encoded", []string{"a,b", "c"}, "/q?color=a%2Cb,c"},
		{"empty string", "", "/q?color="},
		{"two empty items", []string{"", ""}, "/q?color=,"},
		{"null member skipped", map[string]any{"a": nil, "b": "x"}, "/q?color=b,x"},
		{"empty array omitted", []int{}, "/q"},
	})
}

// doc.go, Fixed rules, Percent-encoding: "parameter and member names encode
// every byte outside RFC 3986's unreserved set".
func TestQueryParamNameEncoding(t *testing.T) {
	runParamCases(t, "/q", `{"name":"fil ter","in":"query","schema":{}}`, "fil ter", "", []paramCase{
		{"space", "x", "/q?fil%20ter=x"},
	})
	runParamCases(t, "/q", `{"name":"a[b]","in":"query","schema":{}}`, "a[b]", "", []paramCase{
		{"brackets", "x", "/q?a%5Bb%5D=x"},
	})
}

// OAS 3.1.2 section 4.8.12.6, style simple for headers (section 4.8.12.2.2:
// "URI percent-encoding MUST NOT be applied"); doc.go, Fixed rules,
// Percent-encoding: "Header values are written as given in every edition,
// never percent-encoded".
func TestHeaderParamSimple(t *testing.T) {
	runParamCases(t, "/h", `{"name":"X-Color","in":"header","schema":{}}`, "X-Color", "X-Color", []paramCase{
		{"string", "blue", "blue"},
		{"array", colors, "blue,black,brown"},
		{"map sorted", colorMap, "B,150,G,200,R,100"},
		{"struct", colorRGB, "R,100,G,200,B,150"},
		{"not encoded", "a b/c,d%é", "a b/c,d%é"},
		{"items not encoded", []string{"a b", "c,d"}, "a b,c,d"},
		{"member names not encoded", map[string]string{"a b": "c"}, "a b,c"},
		{"empty string", "", ""},
		{"integer", 3, "3"},
		{"boolean", true, "true"},
	})
	runParamCases(t, "/h", `{"name":"X-Color","in":"header","explode":true,"schema":{}}`, "X-Color", "X-Color", []paramCase{
		{"map sorted", colorMap, "B=150,G=200,R=100"},
		{"array", colors, "blue,black,brown"},
	})
}

// doc.go, Fixed rules, Order: "the path item's parameters, then the
// operation's, in declared order, an overriding parameter (one with the same
// location and name, ...) taking the place of the one it overrides". The
// operation's a (explode false) replaces the
// path item's a (explode true) in the first place.
func TestParamOrderAndOverride(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/o":{
		"parameters":[{"name":"a","in":"query","schema":{}},{"name":"b","in":"query","schema":{}}],
		"get":{"operationId":"o","parameters":[{"name":"c","in":"query","schema":{}},{"name":"a","in":"query","explode":false,"schema":{}}]}
	}`)
	c := parseFor(t, w, doc, nil)
	mustCall(t, c, "o", &openapi.Input{Params: map[string]any{"c": "y", "b": "x", "a": []int{1, 2}}}, nil)
	if got := w.only(t).RequestURI; got != "/o?a=1,2&b=x&c=y" {
		t.Errorf("request target %q, want /o?a=1,2&b=x&c=y", got)
	}
}

// Several path parameters, and a template segment with literal text around
// its variable: each {name} is replaced by its serialized value and the
// literal text is kept.
func TestPathTemplateSubstitution(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/users/{user}/files/{name}.{ext}":{"get":{"operationId":"f","parameters":[
		{"name":"user","in":"path","required":true,"schema":{}},
		{"name":"name","in":"path","required":true,"schema":{}},
		{"name":"ext","in":"path","required":true,"schema":{}}
	]}}`)
	c := parseFor(t, w, doc, nil)
	mustCall(t, c, "f", &openapi.Input{Params: map[string]any{"user": "a b", "name": "r/1", "ext": "tar.gz"}}, nil)
	if got := w.only(t).RequestURI; got != "/users/a%20b/files/r%2F1.tar.gz" {
		t.Errorf("request target %q", got)
	}
}

// errors.go, RequestError.Inputs: "a missing required one, a value its style
// cannot serialize or a header cannot carry", keyed by Param.Key. doc.go,
// Values: "an undefined required one is missing" (a null array item is
// not refused: "An undefined member or array item is skipped"; see
// TestUndefinedSettledFirst); doc.go, Fixed rules, Styles: "Nesting in any
// style but deepObject is refused". client.go, Input.Params: "A key the
// operation does not declare, or a missing required parameter, refuses the
// call".
func TestParamRefusals(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/r/{id}":{"get":{"operationId":"r","parameters":[
		{"name":"id","in":"path","required":true,"schema":{}},
		{"name":"color","in":"query","schema":{}},
		{"name":"X-Color","in":"header","schema":{}},
		{"name":"need","in":"query","required":true,"schema":{}}
	]}}`)
	c := parseFor(t, w, doc, nil)
	ok := map[string]any{"id": "1", "need": "n"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range ok {
			m[k] = v
		}
		for k, v := range extra {
			if v == "<delete>" {
				delete(m, k)
				continue
			}
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name   string
		params map[string]any
		keys   []string
	}{
		{"nested array", with(map[string]any{"color": [][]string{{"a"}}}), []string{"color"}},
		{"nested object", with(map[string]any{"color": map[string]any{"a": map[string]string{"b": "c"}}}), []string{"color"}},
		{"array member", with(map[string]any{"color": map[string]any{"a": []string{"x"}}}), []string{"color"}},
		{"nested path array", with(map[string]any{"id": []any{[]int{1}}}), []string{"id"}},
		{"nested header", with(map[string]any{"X-Color": []any{[]int{1}}}), []string{"X-Color"}},
		// RFC 9110 section 5.5: a field value cannot hold CR, LF, NUL or DEL.
		{"header CRLF", with(map[string]any{"X-Color": "a\r\nX-Evil: 1"}), []string{"X-Color"}},
		{"header LF", with(map[string]any{"X-Color": "a\nb"}), []string{"X-Color"}},
		{"header NUL", with(map[string]any{"X-Color": "a\x00b"}), []string{"X-Color"}},
		{"header DEL", with(map[string]any{"X-Color": "a\x7fb"}), []string{"X-Color"}},
		{"missing path", with(map[string]any{"id": "<delete>"}), []string{"id"}},
		{"missing query", with(map[string]any{"need": "<delete>"}), []string{"need"}},
		{"required nil", with(map[string]any{"id": nil}), []string{"id"}},
		{"required typed nil", with(map[string]any{"need": (*string)(nil)}), []string{"need"}},
		{"required empty array", with(map[string]any{"need": []string{}}), []string{"need"}},
		{"required empty object", with(map[string]any{"id": map[string]any{}}), []string{"id"}},
		{"undeclared", with(map[string]any{"colour": "x"}), []string{"colour"}},
		{"nil Input", nil, []string{"id", "need"}},
		// Prepare "returns a *RequestError listing every problem at once".
		{"several", map[string]any{"colour": 1, "color": []any{[]int{1}}}, []string{"colour", "color", "id", "need"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in *openapi.Input
			if tt.params != nil {
				in = &openapi.Input{Params: tt.params}
			}
			resp, err := c.Call(t.Context(), "r", in, nil)
			re := refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, tt.keys...)
			if len(re.Settings) != 0 {
				t.Errorf("Settings = %q, want none", sortedKeys(re.Settings))
			}
			// Prepare refuses the same way.
			req, err := c.Prepare("r", in)
			if req != nil {
				t.Errorf("Prepare returned a Request")
			}
			wantKeys(t, "Prepare Inputs", asRequestError(t, err).Inputs, true, tt.keys...)
		})
	}
}

// errors.go, RequestError.Error: "The text the client writes never holds a
// credential, an input's value, or a value given for a server variable or a
// header field".
func TestRequestErrorOmitsValues(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/r":{"get":{"operationId":"r","parameters":[
		{"name":"X-Token","in":"header","schema":{}},
		{"name":"color","in":"query","schema":{}}
	]}}`)
	c := parseFor(t, w, doc, nil)
	_, err := c.Call(t.Context(), "r", &openapi.Input{Params: map[string]any{
		"X-Token": "SECRET-TOKEN-1\r\nX-Evil: 1",
		"color":   []any{"SECRET-ITEM-2", []string{"nested"}},
		"colour":  "SECRET-VALUE-3",
	}}, nil)
	re := asRequestError(t, err)
	msg := re.Error()
	for _, secret := range []string{"SECRET-TOKEN-1", "SECRET-ITEM-2", "SECRET-VALUE-3"} {
		if strings.Contains(msg, secret) {
			t.Errorf("error text %q contains the input value %q", msg, secret)
		}
	}
	// "Error describes every problem and the field that fixes each": each
	// Inputs key is named.
	for k := range re.Inputs {
		if !strings.Contains(msg, k) {
			t.Errorf("error text %q does not name %q", msg, k)
		}
	}
	w.nothingSent(t)
}
