package openapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for parameter and body serialization. Where the client
// applies encoding/json, the encoder's output and behavior are
// authoritative: nesting depth is counted in the JSON it writes, and the
// search for readers follows the values it encodes. Also covered:
// content-serialized cookies written as given, framing header fields,
// []byte and reader content parameters, Header removal conflicts,
// Param.ExplodeSet, nil ParamWriters entries, path writers in RawPath, lists
// with no defined item, invalid UTF-8 in content parameters, and the 1 MiB
// bound on the request target and header fields.

// depthDoc has a JSON content query parameter p and a JSON body.
const depthDoc = `
	"/q":{"get":{"operationId":"content","parameters":[{"name":"p","in":"query","content":{"application/json":{}}}]}},
	"/b":{"post":{"operationId":"body","requestBody":{"content":{"application/json":{}}}}}`

// nestAny returns n []any around leaf.
func nestAny(n int, leaf any) any {
	v := leaf
	for range n {
		v = []any{v}
	}
	return v
}

// nestMap returns n map[string]any around leaf.
func nestMap(n int, leaf any) any {
	v := leaf
	for range n {
		v = map[string]any{"a": v}
	}
	return v
}

// brackets returns n arrays around inner, as JSON text.
func brackets(n int, inner string) string {
	return strings.Repeat("[", n) + inner + strings.Repeat("]", n)
}

// deepByte998 and deepByte999 are byte-kinded types whose MarshalJSON writes
// 998 or 999 arrays around 0: encoding/json writes a slice of them as an
// array of those values, not as base64, since the element's pointer
// implements json.Marshaler.
type (
	deepByte998 uint8
	deepByte999 uint8
)

func (deepByte998) MarshalJSON() ([]byte, error) { return []byte(brackets(998, "0")), nil }
func (deepByte999) MarshalJSON() ([]byte, error) { return []byte(brackets(999, "0")), nil }

// ptrDeepByte is deepByte999 with a pointer receiver, which encoding/json
// calls on an addressable slice element.
type ptrDeepByte uint8

func (*ptrDeepByte) MarshalJSON() ([]byte, error) { return []byte(brackets(999, "0")), nil }

// textByte is a byte-kinded TextMarshaler: a []textByte is an array of
// strings.
type textByte uint8

func (textByte) MarshalText() ([]byte, error) { return []byte("t"), nil }

// prepareIn prepares key with v as the parameter p (key "content") or the
// body (key "body").
func prepareIn(c *openapi.Client, key string, v any) (*openapi.Request, error) {
	if key == "body" {
		return c.Prepare("body", &openapi.Input{Body: v})
	}
	return c.Prepare("content", &openapi.Input{Params: map[string]any{"p": v}})
}

// wantEncoded checks that req carries v as encoding/json writes it: as the
// body, or as the percent-encoded value of p.
func wantEncoded(t *testing.T, key string, req *openapi.Request, v any) {
	t.Helper()
	want, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if key == "body" {
		got, _ := io.ReadAll(req.HTTP.Body)
		if !bytes.Equal(trimNL(got), want) {
			t.Errorf("body %.80q..., want %.80q...", got, want)
		}
		return
	}
	if got, want := req.HTTP.URL.RawQuery, "p="+pctName(string(want)); got != want {
		t.Errorf("query %.80q..., want %.80q...", got, want)
	}
}

// Depth is counted in the JSON encoding/json writes, a MarshalJSON's output
// included (doc.go, Values: a value "whose JSON, a MarshalJSON's output
// included, nests deeper than 1,000 levels, the outermost value ... being
// level 1", is refused "at the key of the body, field, part, sequential item or
// parameter that is or holds it"; a scalar leaf is a level, as the Loader
// counts documents).
// A static per-type depth may not refuse a value whose JSON is within the
// bound, nor accept one whose JSON is not.
func TestValueDepthCountedInEncodedJSON(t *testing.T) {
	c := parseAt(t, doc31(depthDoc), "https://api.example.test", testDocURI, nil)
	deepType := reflect.TypeFor[int]()
	for range 1001 {
		deepType = reflect.SliceOf(deepType)
	}
	within := []struct {
		name string
		v    any
	}{
		// 1,000 levels ending in an empty typed map.
		{"999 objects around an empty typed map", nestMap(999, map[string]int{})},
		{"999 arrays around an empty typed slice", nestAny(999, []int{})},
		{"999 arrays around a nil typed slice", nestAny(999, []int(nil))},
		{"999 arrays around a nil struct pointer", nestAny(999, (*readerHolder)(nil))},
		{"999 arrays around empty raw JSON", nestAny(999, json.RawMessage("[]"))},
		// A shallow value of a type 1,001 slices deep is null or [].
		{"nil value of a deep type", reflect.Zero(deepType).Interface()},
		{"empty value of a deep type", reflect.MakeSlice(deepType, 0, 0).Interface()},
		// []deepByte998{0}: 1 + 998 arrays + 0 = 1,000 levels.
		{"byte-kinded Marshaler, 1,000 levels", []deepByte998{0}},
		{"byte-kinded TextMarshaler, 1,000 levels", nestAny(998, []textByte{1})},
	}
	beyond := []struct {
		name string
		v    any
	}{
		// []B{0}, B's MarshalJSON writing 999 arrays around 0: 1,001.
		{"byte-kinded Marshaler, 1,001 levels", []deepByte999{0}},
		{"byte-kinded Marshaler in a map", map[string]any{"k": []deepByte998{0}}},
		{"pointer-receiver byte Marshaler in a struct field", struct{ F []ptrDeepByte }{[]ptrDeepByte{0}}},
		{"byte-kinded TextMarshaler, 1,001 levels", nestAny(999, []textByte{1})},
		{"999 objects around a non-empty typed map", nestMap(999, map[string]int{"a": 1})},
		{"raw JSON, 1,001 levels", json.RawMessage(brackets(1000, "0"))},
	}
	for _, key := range []string{"content", "body"} {
		for _, tt := range within {
			t.Run(key+"/"+tt.name, func(t *testing.T) {
				req, err := prepareIn(c, key, tt.v)
				if err != nil {
					t.Fatalf("refused a value whose JSON nests at most 1,000 levels: %.200v", err)
				}
				wantEncoded(t, key, req, tt.v)
			})
		}
		for _, tt := range beyond {
			t.Run(key+"/"+tt.name, func(t *testing.T) {
				req, err := prepareIn(c, key, tt.v)
				if req != nil || err == nil {
					t.Fatalf("prepared a value whose JSON nests 1,001 levels or more")
				}
				want := "p"
				if key == "body" {
					want = "Input.Body"
				}
				wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, want)
			})
		}
	}
}

// hiddenPtr hides a reader behind a pointer-receiver MarshalJSON, which
// encoding/json calls only on an addressable value.
type hiddenPtr struct{ R io.Reader }

func (*hiddenPtr) MarshalJSON() ([]byte, error) { return []byte(`"ok"`), nil }

// hiddenVal hides a reader behind a value-receiver MarshalJSON.
type hiddenVal struct{ R io.Reader }

func (hiddenVal) MarshalJSON() ([]byte, error) { return []byte(`"ok"`), nil }

// hiddenText hides a reader behind a value-receiver MarshalText.
type hiddenText struct{ R io.Reader }

func (hiddenText) MarshalText() ([]byte, error) { return []byte("text"), nil }

// hiddenPtrText hides a reader behind a pointer-receiver MarshalText.
type hiddenPtrText struct{ R io.Reader }

func (*hiddenPtrText) MarshalText() ([]byte, error) { return []byte("text"), nil }

// unexported holds a reader where encoding/json does not look.
type unexported struct {
	r io.Reader
	A int
	S io.Reader `json:"-"`
}

// The reader walk mirrors encoding/json (doc.go, Values: "A reader or Part
// anywhere inside a parameter value the client encodes with encoding/json is
// refused at the parameter's key, as for a body"): the fields json encodes,
// value and pointer-receiver Marshaler and TextMarshaler on addressable
// values, and it stops where json would stop. A reader json never reaches is
// no refusal, and the value is sent as json writes it; a reader json encodes
// is refused.
func TestReaderSearchFollowsEncodingJSON(t *testing.T) {
	c := parseAt(t, doc31(depthDoc), "https://api.example.test", testDocURI, nil)
	r := func() io.Reader { return strings.NewReader("x") }
	sent := []struct {
		name string
		v    any
	}{
		// json calls the pointer method on an addressable slice element.
		{"pointer-receiver MarshalJSON, slice element", []hiddenPtr{{R: r()}}},
		{"pointer-receiver MarshalJSON, pointer", &hiddenPtr{R: r()}},
		{"pointer-receiver MarshalJSON, struct field", struct{ H []hiddenPtr }{[]hiddenPtr{{R: r()}}}},
		{"value-receiver MarshalJSON", hiddenVal{R: r()}},
		{"value-receiver MarshalJSON in a map", map[string]hiddenVal{"k": {R: r()}}},
		{"value-receiver MarshalText", hiddenText{R: r()}},
		{"value-receiver MarshalText in a slice", []hiddenText{{R: r()}}},
		{"pointer-receiver MarshalText, slice element", []hiddenPtrText{{R: r()}}},
		{"unexported and ignored fields", unexported{r: r(), A: 1, S: r()}},
		{"TextMarshaler map keys", map[coord]string{{1, 2}: "v"}},
		{"byte slice field", struct{ B []byte }{[]byte("hi")}},
	}
	refused := []struct {
		name string
		v    any
	}{
		// Not addressable: json encodes the fields, reader included.
		{"pointer-receiver MarshalJSON, not addressable", hiddenPtr{R: r()}},
		{"pointer-receiver MarshalJSON, map value", map[string]hiddenPtr{"k": {R: r()}}},
		{"pointer-receiver MarshalText, not addressable", hiddenPtrText{R: r()}},
		{"TextMarshaler map key, reader value", map[coord]io.Reader{{1, 2}: r()}},
		{"reader in an exported field", readerHolder{Name: "n", R: r()}},
		{"Part in a slice", []any{openapi.Part{Content: "x"}}},
	}
	for _, key := range []string{"content", "body"} {
		for _, tt := range sent {
			t.Run(key+"/"+tt.name, func(t *testing.T) {
				req, err := prepareIn(c, key, tt.v)
				if err != nil {
					t.Fatalf("refused a value whose reader encoding/json never reaches: %v", err)
				}
				wantEncoded(t, key, req, tt.v)
			})
		}
		for _, tt := range refused {
			t.Run(key+"/"+tt.name, func(t *testing.T) {
				req, err := prepareIn(c, key, tt.v)
				if req != nil || err == nil {
					t.Fatalf("prepared a value holding a reader or Part encoding/json encodes")
				}
				want := "p"
				if key == "body" {
					want = "Input.Body/"
				}
				re := asRequestError(t, err)
				if key == "body" {
					// A body refusal names the reader's place in Body
					// (client.go, Input.Body), under "Input.Body".
					if len(re.Inputs) != 1 {
						t.Errorf("Inputs keys %q, want one", sortedKeys(re.Inputs))
					}
					for k := range re.Inputs {
						if k != "Input.Body" && !strings.HasPrefix(k, want) {
							t.Errorf("Inputs key %q, want Input.Body or below it", k)
						}
					}
					return
				}
				wantKeys(t, "Inputs", re.Inputs, true, want)
			})
		}
	}
}

// A reader or Part inside a style parameter's value is refused at the key,
// with nothing sent (doc.go, Values: "A reader or Part anywhere inside a
// parameter value the client encodes with encoding/json is refused at the
// parameter's key"), never written as {} or as its Go fields. A reader json
// never reaches is no refusal.
func TestReadersInStyleParamValuesRefused(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`
		"/s/{s}":{"get":{"operationId":"op","parameters":[
			{"name":"s","in":"path","required":true,"schema":{}},
			{"name":"f","in":"query","schema":{}},
			{"name":"d","in":"query","style":"deepObject","schema":{}},
			{"name":"X-H","in":"header","schema":{}},
			{"name":"c","in":"cookie","schema":{}}]}}`), nil)
	r := func() io.Reader { return strings.NewReader("secret-bytes") }
	values := []struct {
		name string
		v    func() any
	}{
		{"reader", func() any { return r() }},
		{"reader in a map", func() any { return map[string]any{"a": "1", "b": r()} }},
		{"reader in a nested map", func() any { return map[string]any{"a": map[string]any{"b": r()}} }},
		{"reader item", func() any { return []any{"a", r()} }},
		{"reader field", func() any { return readerHolder{Name: "n", R: r()} }},
		{"Part", func() any { return openapi.Part{Content: "x", Filename: "a.txt"} }},
		{"Part pointer in a map", func() any { return map[string]any{"p": &openapi.Part{Content: "x"}} }},
	}
	for _, tt := range values {
		for _, key := range []string{"s", "f", "d", "X-H", "c"} {
			t.Run(tt.name+"/"+key, func(t *testing.T) {
				params := map[string]any{"s": "x", key: tt.v()}
				before := w.count()
				resp, err := c.Call(t.Context(), "op", &openapi.Input{Params: params}, nil)
				re := refusedSince(t, w, before, resp, err)
				wantKeys(t, "Inputs", re.Inputs, true, key)
			})
		}
	}
	mustCall(t, c, "op", &openapi.Input{Params: map[string]any{"s": hiddenVal{R: r()}, "f": hiddenVal{R: r()}}}, nil)
	if got := w.last(t).RequestURI; got != "/s/ok?f=ok" {
		t.Errorf("request target %q, want /s/ok?f=ok: a value-receiver MarshalJSON hides its reader", got)
	}
}

// doc.go, Fixed rules, Percent-encoding: "a content-serialized cookie value
// (OpenAPI 3.1.2 recommends text/plain content so the application assembles
// the cookie) ... [is] written as given too; a cookie value written as given
// that holds a ";" or a control character is refused" (OAS 3.1.2 section
// 4.8.12.2.3 and Appendix D).
func TestContentCookieWrittenAsGiven(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/c":{"get":{"operationId":"op","parameters":[
		{"name":"session","in":"cookie","content":{"text/plain":{}}},
		{"name":"prefs","in":"cookie","content":{"application/json":{}}},
		{"name":"form","in":"cookie","schema":{}}]}}`), nil)
	for _, tt := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"session": "abc/def+g=="}, "session=abc/def+g=="},
		{map[string]any{"prefs": map[string]int{"a": 1}}, `prefs={"a":1}`},
		{map[string]any{"session": "a b,c\"d\\e%41"}, `session=a b,c"d\e%41`},
		{map[string]any{"session": "s", "prefs": []string{"x/y"}, "form": "x/y"}, `session=s; prefs=["x/y"]; form=x%2Fy`},
	} {
		mustCall(t, c, "op", &openapi.Input{Params: tt.params}, nil)
		if got := w.last(t).Header.Values("Cookie"); len(got) != 1 || got[0] != tt.want {
			t.Errorf("Cookie = %q, want [%q]", got, tt.want)
		}
	}
	for _, tt := range []struct {
		key string
		v   any
	}{
		{"session", "a;b"}, {"session", "a\x01b"}, {"session", "a\tb"}, {"session", "a\r\nb"}, {"session", "a\x7fb"},
		{"session", []byte("a;b")}, {"prefs", map[string]string{"a": ";"}},
	} {
		before := w.count()
		resp, err := c.Call(t.Context(), "op", &openapi.Input{Params: map[string]any{tt.key: tt.v}}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, fmt.Sprintf("%q Inputs", tt.v), re.Inputs, true, tt.key)
	}
}

// framingFields are the fields net/http derives or HTTP forbids a client to
// set (doc.go, Fixed rules, Header fields; RFC 9110 sections 6.6.2 and 8.6,
// RFC 9113 section 8.2.2).
var framingFields = []string{"Content-Length", "Transfer-Encoding", "Trailer", "Connection", "Keep-Alive", "Proxy-Connection", "Upgrade"}

// doc.go, Fixed rules, Header fields: "a field or a header parameter named
// Host, Content-Length, Transfer-Encoding, Trailer, Connection, Keep-Alive,
// Proxy-Connection or Upgrade" is refused. As a header parameter, Param.Err
// is set, a value is refused at its key, a required one refuses the call,
// and a writer still bypasses it (client.go, Input.ParamWriters: "A writer
// also bypasses that parameter's serialization Err"); as an Options.Header
// field, Load refuses it (keyed Options.Header), and so does each call when
// it comes from With; as an Input.Header field, the call is refused at
// Input.Header. Field names are compared without regard to case.
func TestFramingHeaderFieldsRefused(t *testing.T) {
	w := newWire(t, nil)
	for _, name := range framingFields {
		t.Run(name, func(t *testing.T) {
			doc := doc31(fmt.Sprintf(`"/o":{"get":{"operationId":"opt","parameters":[{"name":%q,"in":"header","schema":{}}]}},
				"/r":{"get":{"operationId":"req","parameters":[{"name":%q,"in":"header","required":true,"schema":{}}]}},
				"/p":{"get":{"operationId":"plain"}}`, name, name))
			c := parseFor(t, w, doc, nil)
			for _, key := range []string{"opt", "req"} {
				if p := param(t, mustOp(t, c, key), 0); p.Err == nil {
					t.Errorf("%s: Param.Err = nil", key)
				}
			}
			before := w.count()
			resp, err := c.Call(t.Context(), "opt", &openapi.Input{Params: map[string]any{name: "7"}}, nil)
			wantKeys(t, "value Inputs", refusedSince(t, w, before, resp, err).Inputs, true, name)
			resp, err = c.Call(t.Context(), "req", nil, nil)
			wantKeys(t, "required Inputs", refusedSince(t, w, before, resp, err).Inputs, true, name)
			if _, err := c.Prepare("req", &openapi.Input{ParamWriters: map[string]func(*http.Request) error{
				name: func(*http.Request) error { return nil },
			}}); err != nil {
				t.Errorf("a writer does not bypass the parameter's Err: %v", err)
			}

			for _, spelling := range []string{name, strings.ToLower(name)} {
				h := http.Header{spelling: {"x"}}
				_, err := openapi.Parse(t.Context(), []byte(expand(doc, w.URL)), w.URL+"/openapi.json", &openapi.Options{Header: h})
				wantKeys(t, spelling+" Load Settings", asRequestError(t, err).Settings, false, "Options.Header")
				resp, err := c.Call(t.Context(), "plain", &openapi.Input{Header: h}, nil)
				wantKeys(t, spelling+" Settings", refusedSince(t, w, before, resp, err).Settings, false, "Input.Header")
				d := c.With(func(o *openapi.Options) { o.Header[spelling] = []string{"x"} })
				resp, err = d.Call(t.Context(), "plain", nil, nil)
				wantKeys(t, spelling+" With Settings", refusedSince(t, w, before, resp, err).Settings, false, "Options.Header")
			}
		})
	}
}

// doc.go, Values: "a []byte is the encoded content; a reader, and a
// multipart or sequential media type, cannot serialize a parameter and are
// refused at its key". The bytes are then percent-encoded by
// location, or written as given in a header or cookie.
func TestContentParamBytesReadersAndMediaTypes(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(contentDoc+`,
		"/mp":{"get":{"operationId":"multipart","parameters":[{"name":"p","in":"query","content":{"multipart/form-data":{}}}]}},
		"/mm":{"get":{"operationId":"mixed","parameters":[{"name":"p","in":"query","content":{"multipart/mixed":{}}}]}},
		"/jl":{"get":{"operationId":"jsonl","parameters":[{"name":"p","in":"query","content":{"application/jsonl":{}}}]}},
		"/js":{"get":{"operationId":"jsonseq","parameters":[{"name":"p","in":"query","content":{"application/json-seq":{}}}]}},
		"/es":{"get":{"operationId":"events","parameters":[{"name":"X-E","in":"header","content":{"text/event-stream":{}}}]}},
		"/fu":{"get":{"operationId":"form","parameters":[{"name":"p","in":"query","content":{"application/x-www-form-urlencoded":{}}}]}}`), nil)
	for _, tt := range []struct {
		key, param string
		v          []byte
		want       string // the request target, or the field's value
	}{
		{"q", "p", []byte(`{"a":1}`), "/q?p=%7B%22a%22%3A1%7D"},
		{"q", "p", []byte("not JSON at all"), "/q?p=not%20JSON%20at%20all"},
		{"path", "p", []byte(`"x/y"`), "/p/%22x%2Fy%22"},
		{"text", "p", []byte("hi there"), "/t?p=hi%20there"},
		{"textPath", "p", []byte("a/b"), "/tp/a%2Fb"},
		{"octets", "p", []byte("raw\x00"), "/o?p=raw%00"},
		{"custom", "p", []byte("pre-encoded"), "/x?p=pre-encoded"},
		{"h", "X-P", []byte(`{"a":1}`), `{"a":1}`},
		{"octetsHeader", "X-O", []byte("a b"), "a b"},
		{"cookie", "p", []byte(`{"a":1}`), `p={"a":1}`},
		{"textCookie", "p", []byte("abc/def+g=="), "p=abc/def+g=="},
	} {
		got, re := callOne(t, w, c, tt.key, tt.param, tt.v)
		if re != nil {
			t.Errorf("%s %q: refused: %v", tt.key, tt.v, re)
			continue
		}
		var v string
		switch {
		case tt.param[0] == 'X':
			v = strings.Join(got.Header.Values(tt.param), "|")
		case strings.HasSuffix(tt.key, "ookie"):
			v = strings.Join(got.Header.Values("Cookie"), "|")
		default:
			v = got.RequestURI
		}
		if v != tt.want {
			t.Errorf("%s %q: sent %q, want %q", tt.key, tt.v, v, tt.want)
		}
	}
	for _, tt := range []struct {
		key, param string
		v          any
	}{
		{"text", "p", strings.NewReader("x")},
		{"octets", "p", bytes.NewReader([]byte("x"))},
		{"octetsHeader", "X-O", newOnce("x")},
		{"q", "p", strings.NewReader("{}")},
		{"multipart", "p", map[string]string{"a": "b"}},
		{"multipart", "p", []byte("--b\r\n\r\n--b--")},
		{"mixed", "p", "x"},
		{"jsonl", "p", []any{1, 2}},
		{"jsonseq", "p", "x"},
		{"events", "X-E", "data: x"},
	} {
		_, re := callOne(t, w, c, tt.key, tt.param, tt.v)
		if re == nil {
			t.Errorf("%s %T: sent, want a refusal", tt.key, tt.v)
			continue
		}
		wantKeys(t, fmt.Sprintf("%s %T Inputs", tt.key, tt.v), re.Inputs, true, tt.param)
	}
	// Form-urlencoded content is encoded as a form body is (WHATWG, a space
	// as +: doc.go, Fixed rules, Form bodies), then percent-encoded as a
	// query value (doc.go, Fixed rules, Percent-encoding: "path and query
	// values (content-serialized ones included,
	// application/x-www-form-urlencoded too)"), so it stays one value of the
	// named query parameter rather than separate pairs.
	got, re := callOne(t, w, c, "form", "p", map[string]string{"a": "1 2", "b": "x"})
	if re != nil {
		t.Fatalf("form-urlencoded content refused: %v", re)
	}
	if want := "/fu?p=" + pctName("a=1+2&b=x"); got.RequestURI != want {
		t.Errorf("request target %q, want %q", got.RequestURI, want)
	}
}

// doc.go, Fixed rules, Header fields: "A Header entry with no values is a
// conflict like any other when a header parameter the call supplies, the
// call's credential, or the Cookie field, sets that field", keyed by the
// Header that set it. A removal entry for a field the call does not set is
// no conflict.
func TestHeaderRemovalEntriesConflict(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/o":{"get":{"operationId":"op","parameters":[
		{"name":"X-A","in":"header","schema":{}},
		{"name":"c","in":"cookie","schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	for _, tt := range []struct {
		name    string
		client  *openapi.Client
		header  http.Header
		params  map[string]any
		setting string
	}{
		{"Input.Header nil for a header parameter", c, http.Header{"X-A": nil}, map[string]any{"X-A": "v"}, "Input.Header"},
		{"Input.Header empty for a header parameter", c, http.Header{"X-A": {}}, map[string]any{"X-A": "v"}, "Input.Header"},
		{"Input.Header nil Cookie for a cookie parameter", c, http.Header{"Cookie": nil}, map[string]any{"c": "v"}, "Input.Header"},
		{"Options.Header empty for a header parameter", c.With(func(o *openapi.Options) { o.Header["X-A"] = []string{} }), nil, map[string]any{"X-A": "v"}, "Options.Header"},
		{"Options.Header nil Cookie for a cookie parameter", c.With(func(o *openapi.Options) { o.Header["Cookie"] = nil }), nil, map[string]any{"c": "v"}, "Options.Header"},
		{"Load Options.Header empty", parseFor(t, w, doc, &openapi.Options{Header: http.Header{"X-A": {}}}), nil, map[string]any{"X-A": "v"}, "Options.Header"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := w.count()
			resp, err := tt.client.Call(t.Context(), "op", &openapi.Input{Params: tt.params, Header: tt.header}, nil)
			wantKeys(t, "Settings", refusedSince(t, w, before, resp, err).Settings, false, tt.setting)
		})
	}
	mustCall(t, c, "op", &openapi.Input{Header: http.Header{"X-A": nil, "Cookie": nil}}, nil)
	got := w.last(t)
	if v, ok := got.Header["X-A"]; ok {
		t.Errorf("X-A = %q, want none", v)
	}
	if v, ok := got.Header["Cookie"]; ok {
		t.Errorf("Cookie = %q, want none", v)
	}
}

// describe.go, Param.ExplodeSet: "whether the document writes it", true for
// a content parameter whose document writes explode.
func TestContentParamExplodeSet(t *testing.T) {
	doc := bare31(`"/q":{"get":{"operationId":"q","parameters":[
		{"name":"t","in":"query","explode":true,"content":{"application/json":{}}},
		{"name":"f","in":"query","explode":false,"content":{"application/json":{}}},
		{"name":"u","in":"query","content":{"application/json":{}}}]}}`)
	op := mustOp(t, parseAt(t, doc, "", testDocURI, nil), "q")
	for _, p := range op.Params {
		if want := p.Name != "u"; p.ExplodeSet != want {
			t.Errorf("%s: ExplodeSet = %t, want %t", p.Name, p.ExplodeSet, want)
		}
		if p.ContentType != "application/json" {
			t.Errorf("%s: ContentType %q", p.Name, p.ContentType)
		}
	}
}

// client.go, Input.ParamWriters: "an unknown key, a nil writer, and the same
// key in Params and ParamWriters, are refused at that key": a nil entry is
// refused at Inputs[key].
func TestNilParamWriterEntriesRefused(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/q":{"get":{"operationId":"op","parameters":[{"name":"q","in":"query","schema":{}}]}}`), nil)
	for _, tt := range []struct {
		name string
		in   *openapi.Input
		keys []string
	}{
		{"unknown key", &openapi.Input{ParamWriters: map[string]func(*http.Request) error{"nope": nil}}, []string{"nope"}},
		{"key in Params too", &openapi.Input{Params: map[string]any{"q": "v"}, ParamWriters: map[string]func(*http.Request) error{"q": nil}}, []string{"q"}},
		{"both", &openapi.Input{Params: map[string]any{"q": "v"}, ParamWriters: map[string]func(*http.Request) error{"q": nil, "typo": nil}}, []string{"q", "typo"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := c.Prepare("op", tt.in)
			if req != nil {
				t.Errorf("Prepare returned a Request")
			}
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, tt.keys...)
			resp, err := c.Call(t.Context(), "op", tt.in, nil)
			refusedBeforeSending(t, w, resp, err)
		})
	}
}

// The depth scan on encoded JSON, used where no static per-type bound
// decides, as for raw JSON and a time.Time field: brackets inside strings,
// escaped quotes, and many sibling arrays are not nesting; 1,000 levels
// ending in an empty array is within the bound; a member at level 1,001 is
// not.
func TestDepthScanOfEncodedJSON(t *testing.T) {
	c := parseAt(t, doc31(depthDoc), "https://api.example.test", testDocURI, nil)
	type stamped struct {
		T time.Time       `json:"t"`
		R json.RawMessage `json:"r"`
	}
	quoteRun := `"\"` + strings.Repeat("[", 1200) + `\"\\` + strings.Repeat("{", 300) + `"`
	siblings := "[" + strings.TrimSuffix(strings.Repeat("[[]],", 800), ",") + "]"
	within := []struct {
		name string
		v    any
	}{
		{"brackets in a string", json.RawMessage("[" + quoteRun + "]")},
		{"brackets in a key", json.RawMessage(`{` + quoteRun + `:` + brackets(990, "") + `}`)},
		{"sibling arrays", json.RawMessage(siblings)},
		{"1,000 levels ending in []", json.RawMessage(brackets(1000, ""))},
		{"1,000 levels ending in {}", json.RawMessage(brackets(999, "{}"))},
		{"time.Time beside 999 levels", stamped{fixedTime, json.RawMessage(brackets(999, ""))}},
		{"raw JSON in a map, 1,000 levels", map[string]json.RawMessage{"k": json.RawMessage(brackets(999, ""))}},
		{"raw JSON in a slice after deep siblings", []json.RawMessage{json.RawMessage(brackets(998, "")), json.RawMessage(brackets(999, ""))}},
	}
	beyond := []struct {
		name string
		v    any
	}{
		{"1,000 arrays around 0", json.RawMessage(brackets(1000, "0"))},
		{"999 arrays around {\"a\":1}", json.RawMessage(brackets(999, `{"a":1}`))},
		{"time.Time beside 1,000 levels", stamped{fixedTime, json.RawMessage(brackets(1000, ""))}},
		{"raw JSON in a map, 1,001 levels", map[string]json.RawMessage{"k": json.RawMessage(brackets(1000, ""))}},
		{"deep after a string of brackets", json.RawMessage("[" + quoteRun + "," + brackets(1000, "") + "]")},
	}
	for _, key := range []string{"content", "body"} {
		for _, tt := range within {
			t.Run(key+"/"+tt.name, func(t *testing.T) {
				req, err := prepareIn(c, key, tt.v)
				if err != nil {
					t.Fatalf("refused JSON nested at most 1,000 levels: %.200v", err)
				}
				wantEncoded(t, key, req, tt.v)
			})
		}
		for _, tt := range beyond {
			t.Run(key+"/"+tt.name, func(t *testing.T) {
				req, err := prepareIn(c, key, tt.v)
				if req != nil || err == nil {
					t.Fatal("prepared JSON nested more than 1,000 levels")
				}
				want := "p"
				if key == "body" {
					want = "Input.Body"
				}
				wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, want)
			})
		}
	}
}

// label is a named string type.
type label string

// encode's branches for a text or other media type (doc.go, Values: a
// value is converted "to JSON data as encoding/json would", then "any other
// type takes only a string, as its bytes, and a text type also a number,
// boolean or json.Number"): a named string type and a TextMarshaler are
// strings, their JSON escapes undone; a value encoding/json cannot write is
// refused at the key.
func TestContentParamTextEncodeBranches(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(contentDoc), nil)
	for _, tt := range []struct {
		key  string
		v    any
		want string
	}{
		{"text", label(`a "b" <c>`), "/t?p=a%20%22b%22%20%3Cc%3E"},
		{"text", fixedTime, "/t?p=2024-01-02T03%3A04%3A05Z"},
		{"text", coord{1, 2}, "/t?p=1x2"},
		{"octets", label("x y"), "/o?p=x%20y"},
	} {
		got, re := callOne(t, w, c, tt.key, "p", tt.v)
		if re != nil || got.RequestURI != tt.want {
			t.Errorf("%s %#v: %q, %v; want %q", tt.key, tt.v, got.RequestURI, re, tt.want)
		}
	}
	for _, tt := range []struct {
		key string
		v   any
	}{
		{"text", math.Inf(1)}, {"text", map[string]any{"f": func() {}}}, {"octets", math.NaN()}, {"octets", true},
	} {
		_, re := callOne(t, w, c, tt.key, "p", tt.v)
		if re == nil {
			t.Errorf("%s %#v: sent, want a refusal", tt.key, tt.v)
			continue
		}
		wantKeys(t, "Inputs", re.Inputs, true, "p")
	}
}

// client.go, Input.ParamWriters: "Locate the token in RawPath: there other
// values are percent-encoded, so their text cannot match it, as it can in
// Path"; "an unresolved path token after all writers refuses preparation",
// at the writer's key. A writer that finds its token in RawPath, and keeps
// RawPath an encoding of Path, is sent whichever side of it another value
// holding the token's text lies; a value holding the token's text is no
// unresolved token; and an unresolved token whose name holds a percent sign
// refuses only its own parameter.
func TestParamWritersTokenInRawPath(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`
		"/a/{id}/{v}":{"get":{"operationId":"tokenFirst","parameters":[
			{"name":"id","in":"path","required":true,"schema":{}},
			{"name":"v","in":"path","required":true,"schema":{}}]}},
		"/b/{v}/{id}":{"get":{"operationId":"valueFirst","parameters":[
			{"name":"v","in":"path","required":true,"schema":{}},
			{"name":"id","in":"path","required":true,"schema":{}}]}},
		"/c/{id}/{v}/{q%41}":{"get":{"operationId":"pctName","parameters":[
			{"name":"id","in":"path","required":true,"schema":{}},
			{"name":"v","in":"path","required":true,"schema":{}},
			{"name":"q%41","in":"path","required":true,"schema":{}}]}}`), nil)
	// fromRaw replaces the token in RawPath and derives Path by decoding
	// each segment, leaving a segment that is another writer's token as it
	// is.
	fromRaw := func(token, raw string) func(*http.Request) error {
		return func(r *http.Request) error {
			r.URL.RawPath = strings.Replace(r.URL.RawPath, token, raw, 1)
			segs := strings.Split(r.URL.RawPath, "/")
			for i, seg := range segs {
				if strings.HasPrefix(seg, "{") {
					continue
				}
				d, err := url.PathUnescape(seg)
				if err != nil {
					return err
				}
				segs[i] = d
			}
			r.URL.Path = strings.Join(segs, "/")
			return nil
		}
	}
	for _, tt := range []struct{ key, want string }{
		{"tokenFirst", "/a/7%2F8/%7Bid%7D"},
		{"valueFirst", "/b/%7Bid%7D/7%2F8"},
	} {
		req := mustPrepare(t, c, tt.key, &openapi.Input{
			Params:       map[string]any{"v": "{id}"},
			ParamWriters: map[string]func(*http.Request) error{"id": fromRaw("{id}", "7%2F8")},
		})
		if got := req.HTTP.URL.EscapedPath(); got != tt.want {
			t.Errorf("%s: prepared path %q, want %q", tt.key, got, tt.want)
		}
		sendAndClose(t, req)
		if got := w.last(t).RequestURI; got != tt.want {
			t.Errorf("%s: request target %q, want %q", tt.key, got, tt.want)
		}
	}
	// q%41's writer leaves its token: that parameter alone is refused; id,
	// resolved beside a value spelling "{id}", is not.
	_, err := c.Prepare("pctName", &openapi.Input{
		Params: map[string]any{"v": "{id}"},
		ParamWriters: map[string]func(*http.Request) error{
			"id":   fromRaw("{id}", "7"),
			"q%41": func(*http.Request) error { return nil },
		},
	})
	wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "q%41")
}

// [null] is a defined list (RFC 6570 section 2.3: a list is undefined only
// "if the list contains zero members"). Unexploded it expands like "" in
// every style ("p=" form and the delimited styles, ";p" matrix, "." label,
// "" simple); exploded, a case RFC 6570 does not settle, nothing is written,
// prefix included (no "." or ";", no form pair, no header field); and the
// parameter counts as given, never missing. The same holds for any list
// whose items are all undefined.
func TestAllUndefinedList(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`
		"/f":{"get":{"operationId":"formNo","parameters":[{"name":"p","in":"query","explode":false,"schema":{}}]}},
		"/fe":{"get":{"operationId":"form","parameters":[{"name":"p","in":"query","required":true,"schema":{}}]}},
		"/m{p}":{"get":{"operationId":"matrix","parameters":[{"name":"p","in":"path","required":true,"style":"matrix","schema":{}}]}},
		"/l{p}":{"get":{"operationId":"label","parameters":[{"name":"p","in":"path","required":true,"style":"label","schema":{}}]}},
		"/s/{p}":{"get":{"operationId":"simple","parameters":[{"name":"p","in":"path","required":true,"schema":{}}]}},
		"/se/{p}":{"get":{"operationId":"simpleExplode","parameters":[{"name":"p","in":"path","required":true,"explode":true,"schema":{}}]}},
		"/me{p}":{"get":{"operationId":"matrixExplode","parameters":[{"name":"p","in":"path","required":true,"style":"matrix","explode":true,"schema":{}}]}},
		"/le{p}":{"get":{"operationId":"labelExplode","parameters":[{"name":"p","in":"path","required":true,"style":"label","explode":true,"schema":{}}]}},
		"/sd":{"get":{"operationId":"space","parameters":[{"name":"p","in":"query","required":true,"style":"spaceDelimited","explode":false,"schema":{}}]}},
		"/pd":{"get":{"operationId":"pipe","parameters":[{"name":"p","in":"query","required":true,"style":"pipeDelimited","explode":false,"schema":{}}]}},
		"/h":{"get":{"operationId":"header","parameters":[{"name":"X-P","in":"header","required":true,"schema":{}}]}},
		"/he":{"get":{"operationId":"headerExplode","parameters":[{"name":"X-P","in":"header","required":true,"explode":true,"schema":{}}]}},
		"/c":{"get":{"operationId":"cookie","parameters":[{"name":"p","in":"cookie","explode":false,"required":true,"schema":{}}]}},
		"/ce":{"get":{"operationId":"cookieExplode","parameters":[{"name":"p","in":"cookie","schema":{}}]}}`), nil)
	for _, v := range []any{[]any{nil}, []any{nil, []int{}, map[string]any{}}, []*string{nil}} {
		for _, tt := range []struct {
			key, param string
			want       string // the request target, or the field's value; "-" for no field
		}{
			{"formNo", "p", "/f?p="},
			{"form", "p", "/fe"},
			{"matrix", "p", "/m;p"},
			{"label", "p", "/l."},
			{"simple", "p", "/s/"},
			{"simpleExplode", "p", "/se/"},
			{"matrixExplode", "p", "/me"},
			{"labelExplode", "p", "/le"},
			{"space", "p", "/sd?p="},
			{"pipe", "p", "/pd?p="},
			{"header", "X-P", ""},
			{"headerExplode", "X-P", "-"},
			{"cookie", "p", "p="},
			{"cookieExplode", "p", "-"},
		} {
			t.Run(fmt.Sprintf("%s %#v", tt.key, v), func(t *testing.T) {
				got, re := callOne(t, w, c, tt.key, tt.param, v)
				if re != nil {
					t.Fatalf("refused: %v; want %q", re, tt.want)
				}
				var sent []string
				switch {
				case strings.HasPrefix(tt.key, "header"):
					sent = got.Header.Values("X-P")
				case strings.HasPrefix(tt.key, "cookie"):
					sent = got.Header.Values("Cookie")
				default:
					sent = []string{got.RequestURI}
				}
				switch {
				case tt.want == "-" && sent != nil:
					t.Errorf("sent %q, want no field", sent)
				case tt.want != "-" && (len(sent) != 1 || sent[0] != tt.want):
					t.Errorf("sent %q, want [%q]", sent, tt.want)
				}
			})
		}
	}
}

// An invalid-UTF-8 string under a non-JSON content type is sent as its bytes
// as given; under JSON, encoding/json's U+FFFD replacement stands, since a
// JSON type "is written as json.Marshal writes the value" (doc.go, Values).
// The bytes are then percent-encoded by location, or written as given in a
// header or cookie.
func TestContentParamInvalidUTF8(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(contentDoc), nil)
	for _, tt := range []struct {
		key, param, v string
		want          string // the request target, or the field's value
	}{
		{"text", "p", "a\xffb", "/t?p=a%FFb"},
		{"textPath", "p", "\xfe\xff", "/tp/%FE%FF"},
		{"octets", "p", "\xc3", "/o?p=%C3"},
		{"custom", "p", "x\x80", "/x?p=x%80"},
		{"textHeader", "X-T", "a\xffb", "a\xffb"},
		{"octetsHeader", "X-O", "\xff", "\xff"},
		{"textCookie", "p", "a\xffb", "p=a\xffb"},
	} {
		got, re := callOne(t, w, c, tt.key, tt.param, tt.v)
		if re != nil {
			t.Errorf("%s %q: refused: %v", tt.key, tt.v, re)
			continue
		}
		var sent string
		switch {
		case tt.param[0] == 'X':
			sent = strings.Join(got.Header.Values(tt.param), "|")
		case strings.HasSuffix(tt.key, "ookie"):
			sent = strings.Join(got.Header.Values("Cookie"), "|")
		default:
			sent = got.RequestURI
		}
		if sent != tt.want {
			t.Errorf("%s %q: sent %q, want %q", tt.key, tt.v, sent, tt.want)
		}
	}
	// JSON: as encoding/json writes the string, U+FFFD replacing each
	// invalid byte.
	for _, key := range []string{"q", "vnd", "h"} {
		param := map[string]string{"h": "X-P"}[key]
		if param == "" {
			param = "p"
		}
		want, _ := json.Marshal("a\xffb")
		got, re := callOne(t, w, c, key, param, "a\xffb")
		if re != nil {
			t.Errorf("%s: refused: %v", key, re)
			continue
		}
		if key == "h" {
			if v := got.Header.Values("X-P"); len(v) != 1 || v[0] != string(want) {
				t.Errorf("h: X-P = %q, want [%s]", v, want)
			}
			continue
		}
		if !strings.HasSuffix(got.RequestURI, "?p="+pctName(string(want))) {
			t.Errorf("%s: request target %q, want p=%s", key, got.RequestURI, pctName(string(want)))
		}
	}
}

// valReader is an io.Reader whose value-receiver MarshalJSON encodes it.
type valReader struct{ S string }

func (valReader) Read([]byte) (int, error)     { return 0, io.EOF }
func (valReader) MarshalJSON() ([]byte, error) { return []byte(`"r"`), nil }

// ptrReader is an io.Reader whose MarshalJSON has a pointer receiver, which
// encoding/json calls only on an addressable value (or a pointer).
type ptrReader struct{ S string }

func (ptrReader) Read([]byte) (int, error)      { return 0, io.EOF }
func (*ptrReader) MarshalJSON() ([]byte, error) { return []byte(`"r"`), nil }

// textReader is an io.Reader whose value-receiver MarshalText encodes it.
type textReader struct{ S string }

func (textReader) Read([]byte) (int, error)     { return 0, io.EOF }
func (textReader) MarshalText() ([]byte, error) { return []byte("t"), nil }

// ptrTextReader is an io.Reader whose MarshalText has a pointer receiver.
type ptrTextReader struct{ S string }

func (ptrTextReader) Read([]byte) (int, error)      { return 0, io.EOF }
func (*ptrTextReader) MarshalText() ([]byte, error) { return []byte("t"), nil }

// client.go, Input.Body: "A Part or io.Reader inside a JSON value is refused
// ... unless its own MarshalJSON or MarshalText encodes it"; doc.go, Values:
// a reader inside a parameter value the client encodes with encoding/json is
// refused "unless its own MarshalJSON or MarshalText encodes it". A reader
// encoding/json encodes by its MarshalJSON is sent as json.Marshal writes
// it, in a JSON body, a JSON content parameter and a style parameter; one
// json would encode by reflection (a pointer-receiver MarshalJSON on a value
// that is not addressable, or a plain reader) is refused. The body and
// content parameter hold the reader inside the value, since a reader that is
// the whole body or content value is sent or refused as a reader (client.go,
// Input.Body; doc.go, Values).
func TestSelfMarshalingReaders(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(depthDoc+`,
		"/s":{"get":{"operationId":"style","parameters":[
			{"name":"f","in":"query","schema":{}},
			{"name":"d","in":"query","style":"deepObject","schema":{}},
			{"name":"X-H","in":"header","schema":{}}]}}`), nil)
	sent := []struct {
		name string
		v    func() any
	}{
		{"value receiver in a map", func() any { return map[string]any{"r": valReader{}} }},
		{"value receiver in a slice", func() any { return []any{"a", valReader{S: "x"}} }},
		{"value receiver in a struct field", func() any { return struct{ R io.Reader }{valReader{}} }},
		{"pointer receiver, addressable slice element", func() any { return map[string]any{"r": []ptrReader{{}}} }},
		{"pointer receiver, pointer", func() any { return map[string]any{"r": &ptrReader{}} }},
		{"MarshalText, value receiver in a map", func() any { return map[string]any{"r": textReader{}} }},
		{"MarshalText, value receiver in a slice", func() any { return []any{"a", textReader{S: "x"}} }},
		{"MarshalText, value receiver in a struct field", func() any { return struct{ R io.Reader }{textReader{}} }},
		{"MarshalText, pointer receiver, addressable slice element", func() any { return map[string]any{"r": []ptrTextReader{{}}} }},
		{"MarshalText, pointer receiver, pointer", func() any { return map[string]any{"r": &ptrTextReader{}} }},
	}
	refused := []struct {
		name string
		v    func() any
	}{
		{"pointer receiver, not addressable", func() any { return map[string]any{"r": ptrReader{}} }},
		{"pointer receiver in an interface field, not addressable", func() any { return struct{ R io.Reader }{ptrReader{}} }},
		{"MarshalText, pointer receiver, not addressable", func() any { return map[string]any{"r": ptrTextReader{}} }},
		{"MarshalText, pointer receiver in an interface field", func() any { return struct{ R io.Reader }{ptrTextReader{}} }},
		{"plain reader", func() any { return map[string]any{"r": strings.NewReader("x")} }},
	}
	for _, key := range []string{"content", "body"} {
		for _, tt := range sent {
			t.Run(key+"/sent/"+tt.name, func(t *testing.T) {
				v := tt.v()
				req, err := prepareIn(c, key, v)
				if err != nil {
					t.Fatalf("refused a reader its own MarshalJSON encodes: %v", err)
				}
				wantEncoded(t, key, req, v)
			})
		}
		for _, tt := range refused {
			t.Run(key+"/refused/"+tt.name, func(t *testing.T) {
				req, err := prepareIn(c, key, tt.v())
				if req != nil || err == nil {
					t.Fatal("prepared a reader encoding/json encodes by reflection")
				}
				re := asRequestError(t, err)
				if key == "content" {
					wantKeys(t, "Inputs", re.Inputs, true, "p")
					return
				}
				if len(re.Inputs) != 1 {
					t.Errorf("Inputs keys %q, want one", sortedKeys(re.Inputs))
				}
				for k := range re.Inputs {
					if k != "Input.Body" && !strings.HasPrefix(k, "Input.Body/") {
						t.Errorf("Inputs key %q, want Input.Body or below it", k)
					}
				}
			})
		}
	}

	// Style parameters: the JSON the MarshalJSON writes is then serialized
	// by the style.
	for _, tt := range []struct {
		name, param string
		v           any
		want        string // the request target, or the header's value
	}{
		{"value receiver", "f", valReader{}, "/s?f=r"},
		{"value receiver items", "f", []valReader{{}, {S: "x"}}, "/s?f=r&f=r"},
		{"pointer receiver, pointer", "f", &ptrReader{}, "/s?f=r"},
		{"pointer receiver, addressable items", "f", []ptrReader{{}}, "/s?f=r"},
		{"deepObject member", "d", map[string]any{"k": valReader{}}, "/s?d%5Bk%5D=r"},
		{"header", "X-H", valReader{}, "r"},
		{"MarshalText, value receiver", "f", textReader{}, "/s?f=t"},
		{"MarshalText, value receiver items", "f", []textReader{{}, {S: "x"}}, "/s?f=t&f=t"},
		{"MarshalText, pointer receiver, pointer", "f", &ptrTextReader{}, "/s?f=t"},
		{"MarshalText, pointer receiver, addressable items", "f", []ptrTextReader{{}}, "/s?f=t"},
		{"MarshalText, deepObject member", "d", map[string]any{"k": textReader{}}, "/s?d%5Bk%5D=t"},
		{"MarshalText, header", "X-H", textReader{}, "t"},
	} {
		t.Run("style/sent/"+tt.name, func(t *testing.T) {
			got, re := callOne(t, w, c, "style", tt.param, tt.v)
			if re != nil {
				t.Fatalf("refused: %v", re)
			}
			if tt.param == "X-H" {
				if v := got.Header.Values("X-H"); len(v) != 1 || v[0] != tt.want {
					t.Errorf("X-H = %q, want [%q]", v, tt.want)
				}
				return
			}
			if got.RequestURI != tt.want {
				t.Errorf("request target %q, want %q", got.RequestURI, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		name, param string
		v           any
	}{
		{"pointer receiver, not addressable", "f", ptrReader{}},
		{"pointer receiver member, not addressable", "d", map[string]any{"k": ptrReader{}}},
		{"MarshalText, pointer receiver, not addressable", "f", ptrTextReader{}},
		{"MarshalText, pointer receiver member, not addressable", "d", map[string]any{"k": ptrTextReader{}}},
		{"plain reader", "f", strings.NewReader("x")},
		{"plain reader member", "d", map[string]any{"k": strings.NewReader("x")}},
	} {
		t.Run("style/refused/"+tt.name, func(t *testing.T) {
			_, re := callOne(t, w, c, "style", tt.param, tt.v)
			if re == nil {
				t.Fatal("sent a reader encoding/json encodes by reflection")
			}
			wantKeys(t, "Inputs", re.Inputs, true, tt.param)
		})
	}
}

// marshalingReader is an io.Reader, read once, whose value-receiver
// MarshalJSON writes an object.
type marshalingReader struct{ r *strings.Reader }

func (m marshalingReader) Read(p []byte) (int, error) { return m.r.Read(p) }
func (marshalingReader) MarshalJSON() ([]byte, error) { return []byte(`{"a":"json"}`), nil }

// A value that is both an io.Reader and a json.Marshaler is a reader wherever
// a reader is raw content, as TestSelfMarshalingReaders has a whole body or
// content value "sent or refused as a reader". client.go, Input.Body: "A
// property may be a []byte, an io.Reader or a [Part]", and a positional part
// "a []byte, an io.Reader, a Part, or a value encoded by that part's media
// type"; Part.Content: "a []byte or an io.Reader for raw content";
// Part.Filename: "the part's name for a []byte or io.Reader Content whose
// media type is not multipart"; Options.Codecs: "a []byte or io.Reader body,
// bypass codecs"; Request.HTTP: "GetBody is set when the body can be sent
// again", and Input.Body says such a reader "is read once"; doc.go, Values:
// "a reader ... cannot serialize a parameter and [is] refused at its key". So
// its bytes, never its MarshalJSON, are sent as a form field, a text/plain
// form field, a multipart/form-data field with a filename, a text/plain one,
// a Part's Content, a field of a caller's codec type, and an OpenAPI 3.2
// positional multipart/mixed part, each in a body sent once; and it is
// refused at the key of a querystring parameter under
// application/x-www-form-urlencoded.
func TestMarshalingReaderIsAReader(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `
		"/f":{"post":{"requestBody":{"content":{"application/x-www-form-urlencoded":{
			"schema":{"type":"object","properties":{"f":{},"t":{"type":"string"}}}}}}}},
		"/m":{"post":{"requestBody":{"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"f":{},"t":{"type":"string"},"c":{}}},
			"encoding":{"c":{"contentType":"application/x-tag"}}}}}}},
		"/p":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/json"}}}}}},
		"/q":{"get":{"parameters":[{"name":"qs","in":"querystring","content":{"application/x-www-form-urlencoded":{}}}]}}`),
		&openapi.Options{Codecs: map[string]openapi.Codec{"application/x-tag": tagCodec{tag: "TAG"}}})
	const mfd, mixed = "multipart/form-data; boundary=B", "multipart/mixed; boundary=B"
	part := func(name, ctype string) string {
		return "--B\r\nContent-Disposition: form-data; name=\"" + name + "\"; filename=\"" + name + "\"\r\nContent-Type: " + ctype + "\r\n\r\nraw\r\n--B--\r\n"
	}
	for _, tc := range []struct {
		name, key, media string
		body             func(r any) any
		want             string
	}{
		{"form field", "POST /f", "", func(r any) any { return map[string]any{"f": r} }, "f=raw"},
		{"text/plain form field", "POST /f", "", func(r any) any { return map[string]any{"t": r} }, "t=raw"},
		{"multipart field", "POST /m", mfd, func(r any) any { return map[string]any{"f": r} }, part("f", "application/octet-stream")},
		{"text/plain multipart field", "POST /m", mfd, func(r any) any { return map[string]any{"t": r} }, part("t", "text/plain")},
		{"Part Content", "POST /m", mfd, func(r any) any { return map[string]any{"f": openapi.Part{Content: r}} }, part("f", "application/octet-stream")},
		{"caller's codec field", "POST /m", mfd, func(r any) any { return map[string]any{"c": r} }, part("c", "application/x-tag")},
		{"positional part", "POST /p", mixed, func(r any) any { return []any{r} }, "--B\r\nContent-Type: application/json\r\n\r\nraw\r\n--B--\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := c.Prepare(tc.key, &openapi.Input{MediaType: tc.media, Body: tc.body(marshalingReader{strings.NewReader("raw")})})
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if req.HTTP.GetBody != nil {
				t.Error("GetBody is set for a body holding a reader read once")
			}
			if got := string(editionBody(t, req)); got != tc.want {
				t.Errorf("body %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("querystring", func(t *testing.T) {
		_, err := c.Prepare("GET /q", &openapi.Input{Params: map[string]any{"qs": marshalingReader{strings.NewReader("raw")}}})
		if err == nil {
			t.Fatal("prepared a reader as a querystring value")
		}
		wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "qs")
	})
}

// limit is the bound on the request target and on each header field the
// client builds.
const limit = 1 << 20

// allocatedBy returns the bytes allocated while f runs.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// Regression check, not contract: the 16 MiB figure for what Prepare
// allocates before refusing, where the full output would be 20 MiB or more.
// The rest is contract: doc.go, Values: "A parameter that would take the
// request target or a header field past 1 MiB ... is refused at its key, and
// its serialization stops there". A value or an amplified serialization, such
// as deepObject repeating each member's bracket path per leaf or a template
// repeating a variable, past 1 MiB is refused at the parameter's key, with
// nothing sent, and without building the oversized output; a request under
// the limit, with a margin of 1 KiB, is sent.
func TestRequestSizeLimit(t *testing.T) {
	w := newWire(t, nil)
	var rep strings.Builder
	rep.WriteString("/r")
	for range 20000 {
		rep.WriteString("/{id}")
	}
	c := parseFor(t, w, doc31(`
		"/q":{"get":{"operationId":"query","parameters":[
			{"name":"p","in":"query","schema":{}},
			{"name":"p2","in":"query","schema":{}}]}},
		"/d":{"get":{"operationId":"deep","parameters":[{"name":"d","in":"query","style":"deepObject","schema":{}}]}},
		"`+rep.String()+`":{"get":{"operationId":"repeat","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}},
		"/h":{"get":{"operationId":"header","parameters":[{"name":"X-Big","in":"header","schema":{}}]}},
		"/c":{"get":{"operationId":"cookie","parameters":[
			{"name":"a","in":"cookie","schema":{}},
			{"name":"b","in":"cookie","schema":{}}]}}`), nil)
	refused := func(t *testing.T, key string, params map[string]any, want string) {
		t.Helper()
		var req *openapi.Request
		var err error
		n := allocatedBy(func() { req, err = c.Prepare(key, &openapi.Input{Params: params}) })
		if req != nil || err == nil {
			t.Fatalf("prepared a request past the 1 MiB bound")
		}
		wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, want)
		if n > 16<<20 {
			t.Errorf("Prepare allocated %d MiB before refusing; want under 16 MiB", n>>20)
		}
		before := w.count()
		resp, err := c.Call(t.Context(), key, &openapi.Input{Params: params}, nil)
		wantKeys(t, "Call Inputs", refusedSince(t, w, before, resp, err).Inputs, true, want)
	}

	t.Run("query value over the bound", func(t *testing.T) {
		refused(t, "query", map[string]any{"p": strings.Repeat("a", limit+1)}, "p")
	})
	t.Run("the query parameter that passes the bound", func(t *testing.T) {
		half := strings.Repeat("a", limit/2+1024)
		refused(t, "query", map[string]any{"p": half, "p2": half}, "p2")
	})
	t.Run("deepObject amplification", func(t *testing.T) {
		// A 512 KiB member name over 1,000 leaves: a value of about 520 KiB
		// whose deepObject output would be about 500 MiB.
		leaves := map[string]any{}
		for i := range 1000 {
			leaves["k"+strconv.Itoa(i)] = "v"
		}
		v := map[string]any{strings.Repeat("n", 512<<10): leaves}
		if b, _ := json.Marshal(v); len(b) >= limit {
			t.Fatalf("test value is %d bytes, want under 1 MiB", len(b))
		}
		refused(t, "deep", map[string]any{"d": v}, "d")
	})
	t.Run("repeated template variable", func(t *testing.T) {
		// 20,000 occurrences of a 1 KiB value: about 20 MiB of path.
		refused(t, "repeat", map[string]any{"id": strings.Repeat("x", 1024)}, "id")
	})
	t.Run("header value over the bound", func(t *testing.T) {
		refused(t, "header", map[string]any{"X-Big": strings.Repeat("a", limit+1)}, "X-Big")
	})
	t.Run("the cookie parameter that passes the bound", func(t *testing.T) {
		half := strings.Repeat("a", limit/2+1024)
		refused(t, "cookie", map[string]any{"a": half, "b": half}, "b")
	})

	t.Run("under the bound", func(t *testing.T) {
		value := strings.Repeat("a", limit-1024)
		mustCall(t, c, "query", &openapi.Input{Params: map[string]any{"p": value}}, nil)
		if got := w.last(t).RequestURI; got != "/q?p="+value {
			t.Errorf("request target of %d bytes, want %d", len(got), len("/q?p=")+len(value))
		}
		mustCall(t, c, "header", &openapi.Input{Params: map[string]any{"X-Big": value}}, nil)
		if got := w.last(t).Header.Values("X-Big"); len(got) != 1 || got[0] != value {
			t.Errorf("X-Big not sent whole")
		}
		mustCall(t, c, "repeat", &openapi.Input{Params: map[string]any{"id": "x"}}, nil)
		if got := w.last(t).RequestURI; got != "/r"+strings.Repeat("/x", 20000) {
			t.Errorf("repeated template: request target of %d bytes", len(got))
		}
	})
}
