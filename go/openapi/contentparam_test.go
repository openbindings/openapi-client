package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Content parameters (OAS 3.1.2 section 4.8.12.2.3: "the content field can
// define the media type and schema of the parameter"): doc.go, Values: "A
// parameter serialized by content is encoded as a body of its media type is,
// so under application/json null, [] and {} are present values"; "a type with
// a caller's codec takes a value of any Go type, which that codec encodes; a
// JSON type is written as json.Marshal writes the value, with no trailing
// newline; and any other type takes only a string, as its bytes, and a text
// type also a number, boolean or json.Number, in its JSON spelling"; "Only a
// nil interface is absent". doc.go, Fixed rules, Percent-encoding: "path and
// query values (content-serialized ones included ...) ... encode every byte
// outside RFC 3986's unreserved set as %XX in uppercase hex"; "Header values
// are written as given", and so is "a content-serialized cookie value". OAS
// 3.1.2 section 4.8.12.4: a value serialized "with a Media Type Object for a
// media type that does not already incorporate URI percent-encoding" is
// percent-encoded by the Parameter Object. "as json.Marshal writes" includes
// its HTML escaping.

const contentDoc = `
	"/q":{"get":{"operationId":"q","parameters":[{"name":"p","in":"query","content":{"application/json":{"schema":{}}}}]}},
	"/qr":{"get":{"operationId":"qr","parameters":[{"name":"p","in":"query","required":true,"content":{"application/json":{}}}]}},
	"/h":{"get":{"operationId":"h","parameters":[{"name":"X-P","in":"header","content":{"application/json":{}}}]}},
	"/p/{p}":{"get":{"operationId":"path","parameters":[{"name":"p","in":"path","required":true,"content":{"application/json":{}}}]}},
	"/c":{"get":{"operationId":"cookie","parameters":[{"name":"p","in":"cookie","content":{"application/json":{}}}]}},
	"/v":{"get":{"operationId":"vnd","parameters":[{"name":"p","in":"query","content":{"application/vnd.x+json":{}}}]}},
	"/t":{"get":{"operationId":"text","parameters":[{"name":"p","in":"query","content":{"text/plain":{}}}]}},
	"/th":{"get":{"operationId":"textHeader","parameters":[{"name":"X-T","in":"header","content":{"text/plain":{}}}]}},
	"/tp/{p}":{"get":{"operationId":"textPath","parameters":[{"name":"p","in":"path","required":true,"content":{"text/plain":{}}}]}},
	"/tc":{"get":{"operationId":"textCookie","parameters":[{"name":"p","in":"cookie","content":{"text/plain":{}}}]}},
	"/o":{"get":{"operationId":"octets","parameters":[{"name":"p","in":"query","content":{"application/octet-stream":{}}}]}},
	"/oh":{"get":{"operationId":"octetsHeader","parameters":[{"name":"X-O","in":"header","content":{"application/octet-stream":{}}}]}},
	"/x":{"get":{"operationId":"custom","parameters":[{"name":"p","in":"query","content":{"application/x-custom":{}}}]}},
	"/xh":{"get":{"operationId":"customHeader","parameters":[{"name":"X-C","in":"header","content":{"application/x-custom":{}}}]}}`

// htmlEsc is the JSON escape \u followed by hex, spelled with the backslash
// byte so the literal survives any tool that decodes escapes.
func htmlEsc(hex string) string { return "\x5c" + "u" + hex }

// uri percent-encodes JSON text as a content-serialized query, path or
// cookie value is.
func uri(s string) string { return pctName(s) }

func TestContentParamJSON(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(contentDoc), nil)
	tests := []struct {
		name string
		v    any
		json string // what json.Marshal writes
	}{
		{"object", map[string]any{"a": 1, "b": "x y"}, `{"a":1,"b":"x y"}`},
		{"struct", colorRGB, `{"R":100,"G":200,"B":150}`},
		{"string", "x", `"x"`},
		{"integer", 5, `5`},
		{"boolean", true, `true`},
		{"exact number", json.Number("12.50"), `12.50`},
		// doc.go, Values: "under application/json null, [] and {} are present
		// values"; "a typed nil ... is a value, which encoding/json writes as
		// null".
		{"typed nil", (*int)(nil), `null`},
		{"null", json.RawMessage("null"), `null`},
		{"empty array", []int{}, `[]`},
		{"empty object", map[string]any{}, `{}`},
		{"null member kept", map[string]any{"a": nil}, `{"a":null}`},
		// Nesting is no refusal here: the value is one JSON document.
		{"nested", map[string]any{"a": []any{1, map[string]any{"b": []int{}}}}, `{"a":[1,{"b":[]}]}`},
		// json.Marshal escapes <, & and > as \u003c, \u0026 and \u003e; the
		// expected text is built from its pieces so no escape is decoded.
		{"HTML escaping", "<&>", `"` + htmlEsc("003c") + htmlEsc("0026") + htmlEsc("003e") + `"`},
		{"reserved characters", map[string]string{"k": "a/b?c#d"}, `{"k":"a/b?c#d"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if b, _ := json.Marshal(tt.v); string(b) != tt.json {
				t.Fatalf("test table: json.Marshal writes %s", b)
			}
			got, re := callOne(t, w, c, "q", "p", tt.v)
			if re != nil {
				t.Fatalf("query refused: %v", re)
			}
			if want := "/q?p=" + uri(tt.json); got.RequestURI != want {
				t.Errorf("query: request target %q, want %q", got.RequestURI, want)
			}
			got, re = callOne(t, w, c, "vnd", "p", tt.v)
			if want := "/v?p=" + uri(tt.json); re != nil || got.RequestURI != want {
				t.Errorf("+json query: %q, %v; want %q", got.RequestURI, re, want)
			}
			got, re = callOne(t, w, c, "path", "p", tt.v)
			if want := "/p/" + uri(tt.json); re != nil || got.RequestURI != want {
				t.Errorf("path: %q, %v; want %q", got.RequestURI, re, want)
			}
			got, re = callOne(t, w, c, "h", "X-P", tt.v)
			if v := got.Header.Values("X-P"); re != nil || len(v) != 1 || v[0] != tt.json {
				t.Errorf("header: %q, %v; want [%s]", v, re, tt.json)
			}
			got, re = callOne(t, w, c, "cookie", "p", tt.v)
			if v := got.Header.Values("Cookie"); re != nil || len(v) != 1 || v[0] != "p="+tt.json {
				t.Errorf("cookie: %q, %v; want [p=%s], as given", v, re, tt.json)
			}
		})
	}
	// Exact bytes for one case, spelled out.
	got, _ := callOne(t, w, c, "q", "p", map[string]any{"a": 1})
	if got.RequestURI != "/q?p=%7B%22a%22%3A1%7D" {
		t.Errorf("request target %q, want /q?p=%%7B%%22a%%22%%3A1%%7D", got.RequestURI)
	}

	// Only a nil interface is absent: an optional parameter is omitted, a
	// required one missing.
	mustCall(t, c, "q", &openapi.Input{Params: map[string]any{"p": nil}}, nil)
	if got := w.last(t).RequestURI; got != "/q" {
		t.Errorf("nil: request target %q, want /q", got)
	}
	before := w.count()
	resp, err := c.Call(t.Context(), "qr", &openapi.Input{Params: map[string]any{"p": nil}}, nil)
	wantKeys(t, "Inputs", refusedSince(t, w, before, resp, err).Inputs, true, "p")
	mustCall(t, c, "qr", &openapi.Input{Params: map[string]any{"p": []int{}}}, nil)
	if got := w.last(t).RequestURI; got != "/qr?p=%5B%5D" {
		t.Errorf("required []: request target %q, want /qr?p=%%5B%%5D", got)
	}

	// A value encoding/json cannot write is refused at the key (errors.go,
	// RequestError.Inputs: "a value its style cannot serialize").
	for _, v := range []any{math.NaN(), make(chan int), map[string]any{"f": func() {}}} {
		_, re := callOne(t, w, c, "q", "p", v)
		if re == nil {
			t.Errorf("%T sent, want a refusal", v)
			continue
		}
		wantKeys(t, fmt.Sprintf("%T Inputs", v), re.Inputs, true, "p")
	}
}

// A text type takes a string as its UTF-8 bytes, and a number or boolean in
// its JSON spelling; any other value is refused at the key. Any other media
// type without a codec takes only a string (doc.go, Values).
func TestContentParamTextAndOther(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(contentDoc), nil)
	tests := []struct {
		name, key, param string
		v                any
		want             string // request target, or the header's value
	}{
		{"text string", "text", "p", "a b/c,d", "/t?p=a%20b%2Fc%2Cd"},
		{"text empty string", "text", "p", "", "/t?p="},
		{"text integer", "text", "p", 5, "/t?p=5"},
		{"text float", "text", "p", 2.5, "/t?p=2.5"},
		{"text boolean", "text", "p", false, "/t?p=false"},
		{"text json.Number", "text", "p", json.Number("1.50"), "/t?p=1.50"},
		{"text non-ASCII", "text", "p", "é", "/t?p=%C3%A9"},
		{"text header as given", "textHeader", "X-T", "a b/c%2F", "a b/c%2F"},
		{"text path", "textPath", "p", "a/b", "/tp/a%2Fb"},
		{"octets string", "octets", "p", "a b", "/o?p=a%20b"},
		{"octets header as given", "octetsHeader", "X-O", "a b/c", "a b/c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, re := callOne(t, w, c, tt.key, tt.param, tt.v)
			if re != nil {
				t.Fatalf("refused: %v", re)
			}
			if tt.param[0] == 'X' {
				if v := got.Header.Values(tt.param); len(v) != 1 || v[0] != tt.want {
					t.Errorf("%s = %q, want [%q]", tt.param, v, tt.want)
				}
				return
			}
			if got.RequestURI != tt.want {
				t.Errorf("request target %q, want %q", got.RequestURI, tt.want)
			}
		})
	}
	got, re := callOne(t, w, c, "textCookie", "p", "a b/c")
	if v := got.Header.Values("Cookie"); re != nil || len(v) != 1 || v[0] != "p=a b/c" {
		t.Errorf("text cookie: %q, %v; want [p=a b/c], as given", v, re)
	}
	for _, tt := range []struct {
		key, param string
		v          any
	}{
		{"text", "p", map[string]string{"a": "b"}},
		{"text", "p", []string{"a"}},
		{"textHeader", "X-T", map[string]int{"a": 1}},
		{"octets", "p", 5},
		{"octets", "p", true},
		{"octets", "p", map[string]string{"a": "b"}},
		{"octetsHeader", "X-O", []string{"a"}},
	} {
		_, re := callOne(t, w, c, tt.key, tt.param, tt.v)
		if re == nil {
			t.Errorf("%s with %#v sent, want a refusal", tt.key, tt.v)
			continue
		}
		wantKeys(t, fmt.Sprintf("%s %#v Inputs", tt.key, tt.v), re.Inputs, true, tt.param)
	}
	// A text value forming a whole "." or ".." path segment is refused
	// (doc.go, Fixed rules, Percent-encoding).
	for _, v := range []string{".", ".."} {
		_, re := callOne(t, w, c, "textPath", "p", v)
		if re == nil {
			t.Errorf("text path %q sent, want a refusal", v)
			continue
		}
		wantKeys(t, "Inputs", re.Inputs, true, "p")
	}
}

// recordingCodec records the value its Encode receives and writes a
// recognizable form of it.
type recordingCodec struct {
	tag string
	got *[]any
	err error
}

func (c recordingCodec) Encode(w io.Writer, v any) error {
	if c.got != nil {
		*c.got = append(*c.got, v)
	}
	if c.err != nil {
		return c.err
	}
	_, err := fmt.Fprintf(w, "%s(%v)", c.tag, v)
	return err
}

func (c recordingCodec) Decode(r io.Reader, v any) error { return errors.New("not used") }

// client.go, Options.Codecs: "A codec applies wherever the client encodes
// or decodes a value of its type: request bodies, parts and
// content-serialized parameters ... So entries for "application/json" and
// "+json" replace encoding/json wherever a value is encoded or decoded as
// content of a JSON type ... An Encode error refuses the
// call at the body's or parameter's Inputs key". doc.go, Values: "a caller's
// codec ... receives the value as given". The encoded bytes are then
// percent-encoded, or written as given in a header.
func TestContentParamCodecs(t *testing.T) {
	w := newWire(t, nil)
	var got []any
	c := parseFor(t, w, doc31(contentDoc), &openapi.Options{Codecs: map[string]openapi.Codec{
		"application/x-custom": recordingCodec{tag: "X", got: &got},
		"application/json":     recordingCodec{tag: "J"},
		"+json":                recordingCodec{tag: "S"},
	}})
	value := &tagged{A: "1", B: "2"}
	r, re := callOne(t, w, c, "custom", "p", value)
	if re != nil {
		t.Fatalf("refused: %v", re)
	}
	if want := "/x?p=" + uri(fmt.Sprintf("X(%v)", value)); r.RequestURI != want {
		t.Errorf("request target %q, want %q", r.RequestURI, want)
	}
	if len(got) == 0 || got[0] != any(value) {
		t.Errorf("the codec received %#v, want the value as given", got)
	}
	r, re = callOne(t, w, c, "customHeader", "X-C", 5)
	if v := r.Header.Values("X-C"); re != nil || len(v) != 1 || v[0] != "X(5)" {
		t.Errorf("header: %q, %v; want [X(5)]", v, re)
	}
	r, re = callOne(t, w, c, "q", "p", 5)
	if re != nil || r.RequestURI != "/q?p=J%285%29" {
		t.Errorf("application/json codec: %q, %v; want /q?p=J%%285%%29", r.RequestURI, re)
	}
	r, re = callOne(t, w, c, "vnd", "p", 5)
	if re != nil || r.RequestURI != "/v?p=S%285%29" {
		t.Errorf("+json codec: %q, %v; want /v?p=S%%285%%29", r.RequestURI, re)
	}
	// A string still goes through the codec for its type.
	r, re = callOne(t, w, c, "custom", "p", "s")
	if re != nil || r.RequestURI != "/x?p=X%28s%29" {
		t.Errorf("string: %q, %v; want /x?p=X%%28s%%29", r.RequestURI, re)
	}

	errEncode := errors.New("cannot encode")
	failing := c.With(func(o *openapi.Options) {
		o.Codecs["application/x-custom"] = recordingCodec{tag: "X", err: errEncode}
	})
	_, re = callOne(t, w, failing, "custom", "p", 1)
	if re == nil {
		t.Fatal("a value the codec could not encode was sent")
	}
	wantKeys(t, "Inputs", re.Inputs, true, "p")
	if !errors.Is(re.Inputs["p"], errEncode) && !errors.Is(re, errEncode) {
		t.Errorf("the refusal does not wrap the codec's error")
	}
}

// describe.go, Param: Style is "empty when the value is serialized by
// ContentType instead"; ContentType is "the media type the value is
// serialized with when it is described by content rather than by schema and
// style"; AllowReserved and ExplodeSet do not apply; Schema is "the schema
// of the single content entry". OAS 3.1.2 section 4.8.12.2.3: the content
// map "MUST only contain one entry"; section 4.8.12.2: "Parameter Objects
// MUST include either a content field or a schema field, but not both". A
// parameter violating either cannot be serialized by the document's
// description: Param.Err is set (describe.go, Param.Err: "why built-in
// serialization cannot use the value"; doc.go: the client "does not elect
// among multiple authored alternatives"), and a value for it is refused at
// its key.
func TestContentParamDescriptors(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(contentDoc + `,
		"/two":{"get":{"operationId":"two","parameters":[{"name":"p","in":"query","content":{"application/json":{},"text/plain":{}}}]}},
		"/both":{"get":{"operationId":"both","parameters":[{"name":"p","in":"query","schema":{},"content":{"application/json":{}}}]}}`)
	c := parseFor(t, w, doc, nil)
	for key, want := range map[string]string{
		"q": "application/json", "h": "application/json", "path": "application/json", "cookie": "application/json",
		"vnd": "application/vnd.x+json", "text": "text/plain", "octets": "application/octet-stream", "custom": "application/x-custom",
	} {
		p := param(t, mustOp(t, c, key), 0)
		if p.Style != "" || p.ContentType != want || p.ExplodeSet || p.AllowReserved || p.Err != nil {
			t.Errorf("%s: Style %q, ContentType %q, ExplodeSet %t, AllowReserved %t, Err %v; want \"\", %q, false, false, nil",
				key, p.Style, p.ContentType, p.ExplodeSet, p.AllowReserved, p.Err, want)
		}
	}
	if p := param(t, mustOp(t, c, "q"), 0); p.Schema == nil || compact(t, p.Schema.Raw()) != `{}` {
		t.Errorf("q: Schema is not the content entry's")
	}
	for _, key := range []string{"two", "both"} {
		if p := param(t, mustOp(t, c, key), 0); p.Err == nil {
			t.Errorf("%s: Param.Err = nil", key)
		}
		_, re := callOne(t, w, c, key, "p", "x")
		if re == nil {
			t.Errorf("%s: a value was sent", key)
			continue
		}
		wantKeys(t, key+" Inputs", re.Inputs, true, "p")
		mustCall(t, c, key, nil, nil) // unused, the optional defect does not fail the call
	}
}

// A With-derived malformed Codecs key refuses only calls that would use a
// codec (client.go, With: "any other Options the document cannot use refuse
// each call they affect"): a JSON content parameter uses a codec, so a call
// that gives one is refused at Settings["Options.Codecs"]; a call that leaves
// it out is sent.
func TestContentParamMalformedCodecKey(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(contentDoc), nil)
	e := c.With(func(o *openapi.Options) { o.Codecs["application/json; x=1"] = recordingCodec{tag: "X"} })
	for _, key := range []string{"q", "h", "path", "cookie", "vnd"} {
		param := map[string]string{"h": "X-P"}[key]
		if param == "" {
			param = "p"
		}
		before := w.count()
		resp, err := e.Call(t.Context(), key, &openapi.Input{Params: map[string]any{param: map[string]int{"a": 1}}}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, key+" Settings", re.Settings, false, "Options.Codecs")
	}
	mustCall(t, e, "q", nil, nil)
	if got := w.last(t).RequestURI; got != "/q" {
		t.Errorf("request target %q, want /q", got)
	}
}

// readerHolder is a struct with an io.Reader field.
type readerHolder struct {
	Name string    `json:"name"`
	R    io.Reader `json:"r"`
}

// doc.go, Values: "A reader or Part anywhere inside a parameter value the
// client encodes with encoding/json is refused at the parameter's key, as for
// a body" (client.go, Input.Body: "A Part or io.Reader inside a JSON value is
// refused"): at Inputs[Param.Key], with nothing sent, in a query, header or
// +json content parameter, whether the reader is the value, a map member or a
// struct field, and for a Part.
func TestContentParamRefusesReaders(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(contentDoc), nil)
	values := []struct {
		name string
		v    func() any
	}{
		{"reader", func() any { return strings.NewReader("x") }},
		{"reader in a map", func() any { return map[string]any{"a": 1, "r": strings.NewReader("x")} }},
		{"reader in a struct field", func() any { return readerHolder{Name: "n", R: strings.NewReader("x")} }},
		{"reader in an array", func() any { return []any{"a", bytes.NewReader([]byte("x"))} }},
		{"Part", func() any { return openapi.Part{Content: "x"} }},
		{"Part pointer in a map", func() any { return map[string]any{"p": &openapi.Part{Content: []byte("x")}} }},
	}
	for _, tt := range values {
		for _, target := range []struct{ key, param string }{{"q", "p"}, {"h", "X-P"}, {"vnd", "p"}, {"cookie", "p"}} {
			t.Run(tt.name+"/"+target.key, func(t *testing.T) {
				_, re := callOne(t, w, c, target.key, target.param, tt.v())
				if re == nil {
					t.Fatalf("sent, want a refusal at Inputs[%q]", target.param)
				}
				wantKeys(t, "Inputs", re.Inputs, true, target.param)
			})
		}
	}
}
