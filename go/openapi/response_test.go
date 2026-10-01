package openapi_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Calls and responses: Call's decode targets, codec classes and empty-body
// rules, StatusError, DecodeError, bounds, Response.Declaration and
// Response.Media, Send, Decode and WaitRequest.

const respDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{
	"/pet":{"get":{"operationId":"getPet","responses":{
		"200":{"description":"ok","content":{"application/json":{"schema":{}}}},
		"404":{"description":"not found","content":{"application/problem+json":{}}},
		"default":{"description":"other"}}},
		"head":{"operationId":"headPet","responses":{"200":{"description":"ok","content":{"application/json":{}}}}},
		"put":{"operationId":"putPet","requestBody":{"content":{"application/json":{}}},"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}},
	"/nomedia":{"get":{"operationId":"noMedia","responses":{"200":{"description":"no content declared"}}}},
	"/undeclared":{"get":{"operationId":"undeclared"}},
	"/text":{"get":{"operationId":"text","responses":{"200":{"description":"ok","content":{"text/plain":{}}}}}},
	"/octets":{"get":{"operationId":"octets","responses":{"200":{"description":"ok","content":{"application/octet-stream":{}}}}}},
	"/xml":{"get":{"operationId":"xml","responses":{"200":{"description":"ok","content":{"application/xml":{"schema":{}}}}}}},
	"/multi":{"get":{"operationId":"multi","responses":{"200":{"description":"ok","content":{"application/json":{},"application/xml":{}}}}}},
	"/multistatus":{"get":{"operationId":"multiStatus","responses":{"200":{"description":"ok","content":{"application/json":{}}},"201":{"description":"ok","content":{"text/plain":{}}}}}},
	"/sameclass":{"get":{"operationId":"sameClass","responses":{"200":{"description":"ok","content":{"application/json":{},"application/problem+json":{}}}}}},
	"/ranges":{"get":{"operationId":"ranges","responses":{"200":{"description":"ok","content":{"application/json":{},"*/*":{},"text/*":{}}}}}},
	"/cbor":{"get":{"operationId":"cborAndJSON","responses":{"200":{"description":"ok","content":{"application/json":{},"application/cbor":{}}}}}},
	"/nonsuccess":{"get":{"operationId":"nonSuccessClasses","responses":{"200":{"description":"ok","content":{"application/json":{}}},"400":{"description":"bad","content":{"application/xml":{}}}}}},
	"/decl":{"get":{"operationId":"decl","responses":{"200":{"description":"a"},"2XX":{"description":"b"},"4XX":{"description":"c"},"default":{"description":"d"}}}},
	"/only200":{"get":{"operationId":"only200","responses":{"200":{"description":"a"}}}},
	"/lower":{"get":{"operationId":"lower","responses":{"2xx":{"description":"a"}}}}
}}`

// respClient returns a wire answering with answer and a Client for respDoc.
func respClient(t *testing.T, answer http.HandlerFunc, opts *openapi.Options) (*wire, *openapi.Client) {
	t.Helper()
	w := newWire(t, answer)
	return w, parseFor(t, w, respDoc, opts)
}

// client.go, Call: "Any other pointer receives the body decoded straight
// from the connection by ... its codec class ... JSON types ... with
// encoding/json". client.go, Response.Declaration and Response.Media: "the
// same immutable descriptor Operation.Responses exposes".
func TestCallDecodesJSON(t *testing.T) {
	_, c := respClient(t, jsonAnswer(200, `{"id":"p-7","name":"Rex","tag":"dog"}`), nil)
	var pet Pet
	resp := mustCall(t, c, "getPet", nil, &pet)
	if pet != (Pet{ID: "p-7", Name: "Rex", Tag: "dog"}) {
		t.Errorf("pet = %+v", pet)
	}
	op := mustOp(t, c, "getPet")
	if resp.StatusCode != 200 || resp.Declaration != response(t, op, 0) || resp.Media != responseMedia(t, op, 0, 0) {
		t.Errorf("StatusCode %d, Declaration %p, Media %p", resp.StatusCode, resp.Declaration, resp.Media)
	}
	if resp.Security != "" {
		t.Errorf("Security = %q for an operation with no requirement", resp.Security)
	}
}

// doc.go, Values: "Where the client's own JSON codec creates the values, as
// when out is a *any, a *map[string]any or a *[]any ... JSON numbers are
// kept exact as json.Number. A caller's own type decodes exactly as
// json.Unmarshal would, its any-typed fields included."
func TestDecodeDynamicJSON(t *testing.T) {
	body := `{"n":9007199254740993,"f":1.50,"a":[1],"s":"x","b":true,"z":null}`
	want := map[string]any{
		"n": json.Number("9007199254740993"), "f": json.Number("1.50"), "a": []any{json.Number("1")},
		"s": "x", "b": true, "z": nil,
	}
	_, c := respClient(t, jsonAnswer(200, body), nil)

	var a any
	mustCall(t, c, "getPet", nil, &a)
	if !reflect.DeepEqual(a, want) {
		t.Errorf("*any = %#v", a)
	}
	var m map[string]any
	mustCall(t, c, "getPet", nil, &m)
	if !reflect.DeepEqual(m, want) {
		t.Errorf("*map[string]any = %#v", m)
	}

	_, c = respClient(t, jsonAnswer(200, `[1, 2.5, -0]`), nil)
	var s []any
	mustCall(t, c, "getPet", nil, &s)
	if !reflect.DeepEqual(s, []any{json.Number("1"), json.Number("2.5"), json.Number("-0")}) {
		t.Errorf("*[]any = %#v", s)
	}

	_, c = respClient(t, jsonAnswer(200, `{"n":3}`), nil)
	var st struct {
		N any `json:"n"`
	}
	mustCall(t, c, "getPet", nil, &st)
	if st.N != float64(3) {
		t.Errorf("an any field = %#v, want float64(3) as json.Unmarshal gives", st.N)
	}
}

// client.go, Call: "A *[]byte receives the raw bytes, appended to (*p)[:0]
// so its capacity is reused, bounded by MaxBodyBytes. It stays nil when the
// body is empty and the slice was nil"; "An io.Writer receives the raw bytes
// as they arrive, unbounded"; "nil discards it, reading at most
// MaxBodyBytes before closing the connection."
func TestDecodeRawTargets(t *testing.T) {
	body := `{"not":"decoded"}`
	_, c := respClient(t, jsonAnswer(200, body), &openapi.Options{MaxBodyBytes: 5})

	buf := make([]byte, 3, 100)
	copy(buf, "xyz")
	first := &buf[:1][0]
	big := c.With(func(o *openapi.Options) { o.MaxBodyBytes = 0 })
	mustCall(t, big, "getPet", nil, &buf)
	if string(buf) != body || cap(buf) != 100 || &buf[0] != first {
		t.Errorf("*[]byte = %q (cap %d), want the body in the same array", buf, cap(buf))
	}

	// An io.Writer is not bounded by MaxBodyBytes.
	var w bytes.Buffer
	mustCall(t, c, "getPet", nil, &w)
	if w.String() != body {
		t.Errorf("io.Writer received %q", w.String())
	}
	// nil discards; a longer body is simply cut off (client.go,
	// Options.MaxBodyBytes).
	mustCall(t, c, "getPet", nil, nil)
}

// client.go, Call: "out must be nil, a *[]byte, an io.Writer, or a non-nil
// pointer; anything else is refused before sending". errors.go,
// RequestError.Err: "an out that cannot receive a result".
func TestOutRefusedBeforeSending(t *testing.T) {
	w, c := respClient(t, nil, nil)
	for name, out := range map[string]any{
		"struct value": Pet{},
		"integer":      5,
		"string":       "s",
		"nil pointer":  (*Pet)(nil),
		"byte slice":   []byte{},
		"map value":    map[string]any{},
	} {
		resp, err := c.Call(t.Context(), "getPet", nil, out)
		re := refusedBeforeSending(t, w, resp, err)
		if re.Err == nil {
			t.Errorf("%s: RequestError.Err = nil", name)
		}
	}
}

// client.go, Call: JSON is decoded "anything but whitespace after the value
// being a failure, as for json.Unmarshal".
func TestTrailingDataAfterJSON(t *testing.T) {
	for body, ok := range map[string]bool{
		`{"name":"Rex"}` + "\n\t \r\n": true,
		`{"name":"Rex"} garbage`:       false,
		`{"name":"Rex"}{"name":"R"}`:   false,
		`{"name":"Rex"},`:              false,
	} {
		_, c := respClient(t, jsonAnswer(200, body), nil)
		var pet Pet
		_, err := c.Call(t.Context(), "getPet", nil, &pet)
		var de *openapi.DecodeError
		if ok && err != nil {
			t.Errorf("%q: %v", body, err)
		}
		if !ok && !errors.As(err, &de) {
			t.Errorf("%q: error %v, want a *DecodeError", body, err)
		}
	}
}

// client.go, Call: "An empty body is a success for every out, except that a
// JSON or XML type decoded into a pointer is a *DecodeError wrapping io.EOF
// when the response can have a body and its governing Message has Media ...
// An empty text body decodes as "" ... and any other empty body into a *any
// as an empty []byte." errors.go, DecodeError: "Err is io.EOF".
func TestEmptyBody(t *testing.T) {
	// JSON with Media declared: typed and *any targets fail with io.EOF.
	_, c := respClient(t, jsonAnswer(200, ""), nil)
	for name, out := range map[string]any{"typed": new(Pet), "*any": new(any)} {
		resp, err := c.Call(t.Context(), "getPet", nil, out)
		var de *openapi.DecodeError
		if !errors.As(err, &de) || !errors.Is(err, io.EOF) || !errors.Is(de.Err, io.EOF) {
			t.Errorf("%s: error %v, want a *DecodeError wrapping io.EOF", name, err)
		}
		if resp == nil {
			t.Errorf("%s: no Response with the DecodeError", name)
		}
	}
	// Raw targets and nil succeed.
	var raw []byte
	mustCall(t, c, "getPet", nil, &raw)
	if raw != nil {
		t.Errorf("*[]byte = %q, want nil for an empty body and a nil slice", raw)
	}
	notNil := make([]byte, 0, 8)
	mustCall(t, c, "getPet", nil, &notNil)
	if notNil == nil || len(notNil) != 0 {
		t.Errorf("*[]byte = %#v, want empty and non-nil", notNil)
	}
	var w bytes.Buffer
	mustCall(t, c, "getPet", nil, &w)
	mustCall(t, c, "getPet", nil, nil)

	// No Media on the governing Message, or no governing Message: success.
	for _, key := range []string{"noMedia", "undeclared"} {
		pet := Pet{Name: "kept"}
		mustCall(t, c, key, nil, &pet)
	}

	// An empty text body decodes as "".
	_, c = respClient(t, typedAnswer(200, "text/plain", ""), nil)
	s := "prev"
	mustCall(t, c, "text", nil, &s)
	if s != "" {
		t.Errorf("*string = %q, want \"\"", s)
	}
	// Any other empty body into a *any is an empty []byte.
	_, c = respClient(t, typedAnswer(200, "application/octet-stream", ""), nil)
	var a any = "prev"
	mustCall(t, c, "octets", nil, &a)
	if b, ok := a.([]byte); !ok || len(b) != 0 {
		t.Errorf("*any = %#v, want an empty []byte", a)
	}
}

// client.go, Call: "A 1xx, 204, 205 or 304 response, a response to HEAD, and
// a 2xx response to CONNECT have no body: Call, Stream and StatusError do
// not read one".
func TestBodilessResponses(t *testing.T) {
	for _, status := range []int{204, 205} {
		_, c := respClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, "not json") // net/http sends it for 205
		}, nil)
		pet := Pet{Name: "kept"}
		resp := mustCall(t, c, "getPet", nil, &pet)
		if resp.StatusCode != status || pet.Name != "kept" {
			t.Errorf("%d: status %d, out %+v", status, resp.StatusCode, pet)
		}
	}

	_, c := respClient(t, jsonAnswer(200, `{"name":"Rex"}`), nil)
	pet := Pet{Name: "kept"}
	resp := mustCall(t, c, "headPet", nil, &pet)
	if resp.StatusCode != 200 || pet.Name != "kept" {
		t.Errorf("HEAD: status %d, out %+v", resp.StatusCode, pet)
	}

	_, c = respClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(304) }, nil)
	_, err := c.Call(t.Context(), "getPet", nil, &pet)
	var se *openapi.StatusError
	if !errors.As(err, &se) || se.StatusCode != 304 || len(se.Content) != 0 || se.Err != nil {
		t.Errorf("304: %v, want a *StatusError with no content", err)
	}
}

// client.go, Call: "When out is a pointer to decode into (not a *[]byte, an
// io.Writer or a *any) and the operation's 2xx responses declare concrete
// media types of more than one codec class (a type with a caller's codec is
// a class of its own), a call whose request carries no Accept field is
// refused before sending, at Settings key "Options.Header", naming the
// offered types."
func TestTypedDecodeNeedsAccept(t *testing.T) {
	w, c := respClient(t, jsonAnswer(200, `{"name":"Rex"}`), nil)
	refuse := func(c *openapi.Client, key string, in *openapi.Input, types ...string) {
		t.Helper()
		before := w.count()
		resp, err := c.Call(t.Context(), key, in, new(Pet))
		re := asRequestError(t, err)
		if resp != nil || w.count() != before {
			t.Errorf("%s: a refused call was sent", key)
		}
		wantKeys(t, key+" Settings", re.Settings, false, "Options.Header")
		for _, typ := range types {
			if !strings.Contains(re.Error(), typ) {
				t.Errorf("%s: error %q does not name %s", key, re.Error(), typ)
			}
		}
	}
	refuse(c, "multi", nil, "application/json", "application/xml")
	refuse(c, "multiStatus", nil, "application/json", "text/plain")
	withCBOR := c.With(func(o *openapi.Options) { o.Codecs["application/cbor"] = tagCodec{tag: "CBOR"} })
	refuse(withCBOR, "cborAndJSON", nil, "application/json", "application/cbor")

	// An Accept field at either level lets it proceed.
	mustCall(t, c, "multi", &openapi.Input{Header: http.Header{"Accept": {"application/json"}}}, new(Pet))
	accepting := c.With(func(o *openapi.Options) { o.Header.Set("Accept", "application/json") })
	mustCall(t, accepting, "multi", nil, new(Pet))
	// Raw, dynamic and nil targets proceed.
	for name, out := range map[string]any{"*[]byte": new([]byte), "io.Writer": new(bytes.Buffer), "*any": new(any), "nil": nil} {
		if _, err := c.Call(t.Context(), "multi", nil, out); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// One class, one concrete type among ranges, and non-2xx responses do
	// not count.
	for _, key := range []string{"sameClass", "ranges", "nonSuccessClasses"} {
		mustCall(t, c, key, nil, new(Pet))
	}
}

// client.go, Call: "A missing, repeated or unparsable Content-Type is
// treated as application/octet-stream, which a *any receives as a []byte and
// a typed target cannot"; "any text/* type ... into a *string, its bytes as
// sent, the charset left in the Content-Type; and into a *any, text as a
// string and any other non-JSON type ... as a []byte"; "a *[]byte takes any
// body as it is"; "A type these rules cannot decode into out is a
// *DecodeError".
func TestDecodeByContentType(t *testing.T) {
	multi := func(values ...string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Content-Type"] = values
			io.WriteString(w, `{"name":"Rex"}`)
		}
	}
	tests := []struct {
		name   string
		answer http.HandlerFunc
		key    string
		out    func() any
		want   any // the value out points to after, or nil for a *DecodeError
	}{
		{"missing, typed", typedAnswer(200, "", `{"name":"Rex"}`), "getPet", func() any { return new(Pet) }, nil},
		{"missing, *any", typedAnswer(200, "", `{"name":"Rex"}`), "getPet", func() any { return new(any) }, []byte(`{"name":"Rex"}`)},
		{"repeated, typed", multi("application/json", "application/json"), "getPet", func() any { return new(Pet) }, nil},
		{"repeated, *any", multi("application/json", "application/json"), "getPet", func() any { return new(any) }, []byte(`{"name":"Rex"}`)},
		{"unparsable, typed", typedAnswer(200, "json", `{"name":"Rex"}`), "getPet", func() any { return new(Pet) }, nil},
		{"unparsable, *any", typedAnswer(200, "not a type", `{"name":"Rex"}`), "getPet", func() any { return new(any) }, []byte(`{"name":"Rex"}`)},
		{"missing, *[]byte", typedAnswer(200, "", `{"name":"Rex"}`), "getPet", func() any { return new([]byte) }, []byte(`{"name":"Rex"}`)},
		{"charset parameter", typedAnswer(200, "application/json; charset=utf-8", `{"name":"Rex"}`), "getPet", func() any { return new(Pet) }, Pet{Name: "Rex"}},
		{"+json", typedAnswer(200, "application/problem+json", `{"title":"t","detail":"d"}`), "getPet", func() any { return new(Problem) }, Problem{Title: "t", Detail: "d"}},
		{"text into *string", typedAnswer(200, "text/plain", "hello"), "text", func() any { return new(string) }, "hello"},
		{"text bytes as sent", typedAnswer(200, "text/plain; charset=iso-8859-1", "\xe9t\xe9"), "text", func() any { return new(string) }, "\xe9t\xe9"},
		{"text into *any", typedAnswer(200, "text/csv", "a,b"), "text", func() any { return new(any) }, "a,b"},
		{"octets into *any", typedAnswer(200, "application/octet-stream", "\x00\x01"), "octets", func() any { return new(any) }, []byte("\x00\x01")},
		{"octets into typed", typedAnswer(200, "application/octet-stream", "\x00\x01"), "octets", func() any { return new(Pet) }, nil},
		{"image into typed", typedAnswer(200, "image/png", "\x89PNG"), "octets", func() any { return new(Pet) }, nil},
		{"image into *[]byte", typedAnswer(200, "image/png", "\x89PNG"), "octets", func() any { return new([]byte) }, []byte("\x89PNG")},
		// XML types are decoded with encoding/xml, which ignores json tags.
		{"xml into typed", typedAnswer(200, "application/xml", "<pet><name>Rex</name></pet>"), "xml", func() any { return new(xmlPet) }, xmlPet{XMLName: xml.Name{Local: "pet"}, Name: "Rex"}},
		{"xml into *any", typedAnswer(200, "application/xml", "<pet/>"), "xml", func() any { return new(any) }, []byte("<pet/>")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, c := respClient(t, tt.answer, nil)
			out := tt.out()
			resp, err := c.Call(t.Context(), tt.key, nil, out)
			if tt.want == nil {
				var de *openapi.DecodeError
				if !errors.As(err, &de) {
					t.Fatalf("error %v, want a *DecodeError", err)
				}
				if resp == nil {
					t.Errorf("no Response with the DecodeError")
				}
				return
			}
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if got := reflect.ValueOf(out).Elem().Interface(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("out = %#v, want %#v", got, tt.want)
			}
		})
	}
}

type xmlPet struct {
	XMLName xml.Name `xml:"pet"`
	Name    string   `xml:"name" json:"ignored"`
}

// doc.go, Fixed rules, Content codings: "the transport may ask for gzip and
// remove it ... A header field that sets Accept-Encoding turns that off. A
// body whose Content-Encoding, other than identity, remains passes through
// unchanged to a *[]byte or io.Writer; any other target ... report[s] an
// error naming the coding."
func TestContentCodings(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	io.WriteString(zw, `{"name":"Rex"}`)
	zw.Close()
	gzipped := gz.Bytes()

	_, c := respClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(gzipped)
			return
		}
		io.WriteString(w, `{"name":"Rex"}`)
	}, nil)

	var pet Pet
	mustCall(t, c, "getPet", nil, &pet)
	if pet.Name != "Rex" {
		t.Errorf("transparent gzip: pet = %+v", pet)
	}

	asked := &openapi.Input{Header: http.Header{"Accept-Encoding": {"gzip"}}}
	_, err := c.Call(t.Context(), "getPet", asked, &pet)
	var de *openapi.DecodeError
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "gzip") {
		t.Errorf("coded body into a struct: %v, want a *DecodeError naming gzip", err)
	}
	var raw []byte
	mustCall(t, c, "getPet", asked, &raw)
	if !bytes.Equal(raw, gzipped) {
		t.Errorf("*[]byte = %q, want the coded bytes", raw)
	}
	var w bytes.Buffer
	mustCall(t, c, "getPet", asked, &w)
	if !bytes.Equal(w.Bytes(), gzipped) {
		t.Errorf("io.Writer received %q, want the coded bytes", w.Bytes())
	}
}

// errors.go, StatusError: "A StatusError is a response whose final status is
// not 2xx ... The promoted Body reads Content again"; "Error returns the
// operation and the status ... never the body or the URL"; "Decode decodes
// Content into v by the response's media type, as Response.Decode does ...
// any number of times." client.go, Call: "Any other final status is a
// *StatusError, and out is untouched"; doc.go, Outcomes: "Whenever a
// response arrived, the *Response is returned, even with an error".
// client.go, Response: for a Response from Call with a StatusError, Body
// "reads that error's Content".
func TestStatusError(t *testing.T) {
	body := `{"title":"Not found","detail":"no pet p-404"}`
	w, c := respClient(t, typedAnswer(404, "application/problem+json", body), nil)
	pet := Pet{Name: "kept"}
	resp, err := c.Call(t.Context(), "getPet", nil, &pet)
	var se *openapi.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v (%T), want a *StatusError", err, err)
	}
	if resp == nil || resp.StatusCode != 404 {
		t.Fatalf("Response = %v, want the 404", resp)
	}
	if pet.Name != "kept" {
		t.Errorf("out was changed: %+v", pet)
	}
	op := mustOp(t, c, "getPet")
	if se.StatusCode != 404 || string(se.Content) != body || se.Err != nil {
		t.Errorf("StatusError = %d %q Err %v", se.StatusCode, se.Content, se.Err)
	}
	if se.Declaration != response(t, op, 1) || se.Media != responseMedia(t, op, 1, 0) {
		t.Errorf("Declaration %p, Media %p; want the 404 Message and its Media", se.Declaration, se.Media)
	}
	for range 2 {
		var p Problem
		if err := se.Decode(&p); err != nil || p.Title != "Not found" {
			t.Errorf("Decode = %+v, %v", p, err)
		}
	}
	if b, _ := io.ReadAll(se.Body); string(b) != body {
		t.Errorf("StatusError.Body reads %q", b)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != body {
		t.Errorf("Response.Body reads %q", b)
	}
	msg := se.Error()
	if !strings.Contains(msg, "getPet") || !strings.Contains(msg, "404") {
		t.Errorf("Error() = %q, want the operation and the status", msg)
	}
	if strings.Contains(msg, "no pet") || strings.Contains(msg, w.hostport()) || strings.Contains(msg, "/pet") {
		t.Errorf("Error() = %q holds the body or the URL", msg)
	}
	// Not a success: errors.As finds no DecodeError or RequestError.
	var de *openapi.DecodeError
	var re *openapi.RequestError
	if errors.As(err, &de) || errors.As(err, &re) {
		t.Errorf("a StatusError also matched another of the package's types")
	}

	// An undeclared status is governed by "default", or by nothing.
	w.setAnswer(typedAnswer(500, "text/plain", "boom"))
	_, err = c.Call(t.Context(), "getPet", nil, nil)
	if !errors.As(err, &se) || se.Declaration != response(t, op, 2) {
		t.Errorf("500: %v, want a *StatusError governed by default", err)
	}
	_, err = c.Call(t.Context(), "undeclared", nil, nil)
	if !errors.As(err, &se) || se.Declaration != nil || string(se.Content) != "boom" {
		t.Errorf("500 undeclared: %v, want a *StatusError with a nil Declaration", err)
	}
}

// client.go, Options.MaxErrorBytes: "bounds the body a StatusError keeps.
// Zero means 1 MiB, and a negative value means no limit. A longer body is
// cut, and the StatusError says so; it is never replaced by a size error."
// errors.go, StatusError: "when Content is incomplete the promoted
// ContentLength is len(Content)"; Err is "an *http.MaxBytesError when the
// body was longer than MaxErrorBytes"; Decode "returns Err instead of
// decoding an incomplete body, except into a *[]byte, which receives the
// bytes read along with Err".
func TestMaxErrorBytes(t *testing.T) {
	long := strings.Repeat("x", 100)
	_, c := respClient(t, typedAnswer(404, "application/problem+json", long), &openapi.Options{MaxErrorBytes: 10})
	_, err := c.Call(t.Context(), "getPet", nil, nil)
	var se *openapi.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v, want a *StatusError", err)
	}
	var tooBig *http.MaxBytesError
	if string(se.Content) != long[:10] || !errors.As(se.Err, &tooBig) || tooBig.Limit != 10 {
		t.Errorf("Content %q, Err %v; want 10 bytes and an *http.MaxBytesError with Limit 10", se.Content, se.Err)
	}
	if se.ContentLength != 10 {
		t.Errorf("ContentLength = %d, want len(Content)", se.ContentLength)
	}
	if b, _ := io.ReadAll(se.Body); string(b) != long[:10] {
		t.Errorf("Body reads %q", b)
	}
	var p Problem
	if err := se.Decode(&p); err == nil || !errors.Is(err, se.Err) {
		t.Errorf("Decode of an incomplete body = %v, want Err", err)
	}
	var raw []byte
	if err := se.Decode(&raw); !errors.Is(err, se.Err) || string(raw) != long[:10] {
		t.Errorf("Decode into *[]byte = %q, %v; want the bytes read and Err", raw, err)
	}

	// The default of 1 MiB, and no limit.
	huge := strings.Repeat("y", 1<<20+10)
	_, c = respClient(t, typedAnswer(500, "text/plain", huge), nil)
	_, err = c.Call(t.Context(), "getPet", nil, nil)
	if !errors.As(err, &se) || len(se.Content) != 1<<20 || !errors.As(se.Err, &tooBig) {
		t.Errorf("default bound: %d bytes kept, Err %v; want 1 MiB and an *http.MaxBytesError", len(se.Content), se.Err)
	}
	_, err = c.With(func(o *openapi.Options) { o.MaxErrorBytes = -1 }).Call(t.Context(), "getPet", nil, nil)
	if !errors.As(err, &se) || len(se.Content) != len(huge) || se.Err != nil {
		t.Errorf("no bound: %d bytes kept, Err %v", len(se.Content), se.Err)
	}
}

// errors.go, DecodeError: "a response whose body could not be used by Call
// ... it did not decode into the value given"; "Content is the start of the
// body, at most 4 KiB"; "The promoted Body reads Content again, and
// ContentLength is len(Content)"; "Error returns the operation, the status,
// the media type and the reason, never the body." doc.go, Outcomes: "A 2xx
// whose body could not be read or decoded: a *DecodeError."
func TestDecodeError(t *testing.T) {
	bad := `{"name": 5, "detail":"SECRET-BODY"}`
	_, c := respClient(t, jsonAnswer(200, bad), nil)
	var pet Pet
	resp, err := c.Call(t.Context(), "getPet", nil, &pet)
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error %v, want a *DecodeError", err)
	}
	if resp == nil || resp.StatusCode != 200 || de.StatusCode != 200 || de.Err == nil {
		t.Errorf("Response %v, DecodeError status %d Err %v", resp, de.StatusCode, de.Err)
	}
	if len(de.Content) == 0 || !strings.HasPrefix(bad, string(de.Content)) {
		t.Errorf("Content %q is not a start of the body", de.Content)
	}
	if b, _ := io.ReadAll(de.Body); string(b) != string(de.Content) || de.ContentLength != int64(len(de.Content)) {
		t.Errorf("Body reads %q, ContentLength %d", b, de.ContentLength)
	}
	msg := de.Error()
	for _, want := range []string{"getPet", "200", "application/json"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want %s in it", msg, want)
		}
	}
	if strings.Contains(msg, "SECRET-BODY") {
		t.Errorf("Error() = %q holds the body", msg)
	}
	var se *openapi.StatusError
	if errors.As(err, &se) {
		t.Errorf("a DecodeError also matched *StatusError")
	}

	// At most 4 KiB is kept.
	long := `{"name":` + strings.Repeat(" ", 10000) + `x}`
	_, c = respClient(t, jsonAnswer(200, long), nil)
	_, err = c.Call(t.Context(), "getPet", nil, &pet)
	if !errors.As(err, &de) || len(de.Content) > 4096 || !strings.HasPrefix(long, string(de.Content)) {
		t.Errorf("Content has %d bytes, want a start of the body of at most 4096", len(de.Content))
	}
}

// client.go, Options.MaxBodyBytes: "bounds a body decoded by Call ... into a
// value, read into a *[]byte, or discarded for a nil out ... A longer body
// is a *DecodeError wrapping *http.MaxBytesError, except that a discarded
// one is simply cut off. A body copied to an io.Writer is bounded only by
// the writer." A body of exactly the bound is not longer.
func TestMaxBodyBytes(t *testing.T) {
	body := `"0123456789"` // 12 bytes
	_, c := respClient(t, jsonAnswer(200, body), &openapi.Options{MaxBodyBytes: 11})
	for name, out := range map[string]any{"typed": new(string), "*any": new(any), "*[]byte": new([]byte)} {
		_, err := c.Call(t.Context(), "getPet", nil, out)
		var de *openapi.DecodeError
		var tooBig *http.MaxBytesError
		if !errors.As(err, &de) || !errors.As(err, &tooBig) || tooBig.Limit != 11 {
			t.Errorf("%s: %v, want a *DecodeError wrapping *http.MaxBytesError with Limit 11", name, err)
		}
	}
	mustCall(t, c, "getPet", nil, nil)
	var w bytes.Buffer
	mustCall(t, c, "getPet", nil, &w)
	if w.String() != body {
		t.Errorf("io.Writer received %q", w.String())
	}

	exact := c.With(func(o *openapi.Options) { o.MaxBodyBytes = 12 })
	var s string
	mustCall(t, exact, "getPet", nil, &s)
	if s != "0123456789" {
		t.Errorf("s = %q", s)
	}
	unbounded := c.With(func(o *openapi.Options) { o.MaxBodyBytes = -1 })
	mustCall(t, unbounded, "getPet", nil, &s)
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

// client.go, Call: "A body that fails to read, to decode, or to be written
// to out is a *DecodeError".
func TestWriterFailureIsDecodeError(t *testing.T) {
	errWrite := errors.New("disk full")
	_, c := respClient(t, jsonAnswer(200, `{"name":"Rex"}`), nil)
	_, err := c.Call(t.Context(), "getPet", nil, failingWriter{errWrite})
	var de *openapi.DecodeError
	if !errors.As(err, &de) || !errors.Is(err, errWrite) {
		t.Errorf("error %v, want a *DecodeError wrapping the writer's error", err)
	}
}

// doc.go, Outcomes: "Transport failure: the *url.Error from the
// http.Client."
func TestTransportFailure(t *testing.T) {
	w, c := respClient(t, nil, nil)
	w.Close()
	resp, err := c.Call(t.Context(), "getPet", nil, nil)
	var ue *url.Error
	var re *openapi.RequestError
	var se *openapi.StatusError
	var de *openapi.DecodeError
	if err == nil || resp != nil {
		t.Fatalf("Call to a closed server = %v, %v", resp, err)
	}
	if errors.As(err, &re) || errors.As(err, &se) || errors.As(err, &de) {
		t.Errorf("a transport failure matched one of the package's types: %v", err)
	}
	if !errors.As(err, &ue) {
		t.Errorf("error %v (%T) is not the http.Client's *url.Error", err, err)
	}
}

// client.go, Response.Declaration: "the one whose Key is the exact code
// ("201"), else its range ("2XX"), else "default". It is nil when the
// operation declares nothing for the status ... A lowercase range such as
// "2xx" ... never governs." Request.Send returns every status (client.go,
// Request.Send).
func TestResponseDeclaration(t *testing.T) {
	w, c := respClient(t, nil, nil)
	tests := []struct {
		key    string
		status int
		want   int // index in Operation.Responses, or -1 for nil
	}{
		{"decl", 200, 0},
		{"decl", 201, 1},
		{"decl", 404, 2},
		{"decl", 500, 3},
		{"decl", 302, 3},
		{"only200", 404, -1},
		{"only200", 200, 0},
		{"lower", 200, -1},
	}
	for _, tt := range tests {
		w.setAnswer(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(tt.status) })
		resp := sendAndClose(t, mustPrepare(t, c, tt.key, nil))
		op := mustOp(t, c, tt.key)
		var want *openapi.Message
		if tt.want >= 0 {
			want = response(t, op, tt.want)
		}
		if resp.StatusCode != tt.status || resp.Declaration != want {
			t.Errorf("%s %d: Declaration %+v, want %+v", tt.key, tt.status, resp.Declaration, want)
		}
	}
}

// client.go, Response.Media: "A Media matches when its Type's type and
// subtype equal the Content-Type's, compared without regard to case, or
// cover them as a range does, and every parameter it names is present with
// an equal value: parameter names are compared without regard to case, and
// values after removing quoted-string quoting, a charset without regard to
// case and others exactly. The most specific match wins: a concrete type
// over type/*, type/* over */*, then more parameters over fewer; a tie
// matches none. An absent Content-Type is treated as
// application/octet-stream for matching; a repeated Content-Type matches
// none."
func TestResponseMediaMatching(t *testing.T) {
	tests := []struct {
		name     string
		declared []string
		ct       []string // Content-Type values sent; nil for none
		want     int      // index in Declaration.Media, or -1 for nil
	}{
		{"parameter in the response", []string{"application/json"}, []string{"application/json; charset=utf-8"}, 0},
		{"type case", []string{"application/json"}, []string{"Application/JSON"}, 0},
		{"charset case", []string{"application/json; charset=utf-8"}, []string{"application/json; charset=UTF-8"}, 0},
		{"quoted value", []string{"application/json; charset=utf-8"}, []string{`application/json; charset="utf-8"`}, 0},
		{"parameter name case", []string{"application/json; charset=utf-8"}, []string{"application/json; CHARSET=utf-8"}, 0},
		{"declared parameter missing", []string{"application/json; charset=utf-8"}, []string{"application/json"}, -1},
		{"other values exact", []string{"text/plain; format=flowed"}, []string{"text/plain; format=Flowed"}, -1},
		{"other value quoted", []string{"text/plain; format=flowed"}, []string{`text/plain; format="flowed"`}, 0},
		{"concrete wins", []string{"*/*", "application/*", "application/json"}, []string{"application/json"}, 2},
		{"subtype range wins", []string{"*/*", "application/*", "application/json"}, []string{"application/xml"}, 1},
		{"full range", []string{"*/*", "application/*", "application/json"}, []string{"text/plain"}, 0},
		{"more parameters win", []string{"application/json", "application/json; charset=utf-8"}, []string{"application/json; charset=utf-8"}, 1},
		{"fewer parameters", []string{"application/json", "application/json; charset=utf-8"}, []string{"application/json"}, 0},
		{"tie", []string{"application/json; a=1", "application/json; b=2"}, []string{"application/json; a=1; b=2"}, -1},
		{"absent is octet-stream", []string{"application/json", "application/octet-stream"}, nil, 1},
		{"absent under */*", []string{"*/*"}, nil, 0},
		{"repeated", []string{"application/json"}, []string{"application/json", "application/json"}, -1},
		{"no match", []string{"application/json"}, []string{"text/plain"}, -1},
		{"text range", []string{"text/*"}, []string{"text/html; charset=utf-8"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var content []string
			for _, d := range tt.declared {
				b, _ := json.Marshal(d)
				content = append(content, string(b)+":{}")
			}
			doc := doc31(`"/m":{"get":{"operationId":"m","responses":{"200":{"description":"ok","content":{` + strings.Join(content, ",") + `}}}}}`)
			w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
				rw.Header()["Content-Type"] = tt.ct
				io.WriteString(rw, "x")
			})
			c := parseFor(t, w, doc, nil)
			resp := sendAndClose(t, mustPrepare(t, c, "m", nil))
			decl := response(t, mustOp(t, c, "m"), 0)
			if resp.Declaration != decl {
				t.Fatalf("Declaration = %p, want the 200 Message", resp.Declaration)
			}
			var want *openapi.Media
			if tt.want >= 0 {
				want = responseMedia(t, mustOp(t, c, "m"), 0, tt.want)
			}
			if resp.Media != want {
				got := "nil"
				if resp.Media != nil {
					got = resp.Media.Type
				}
				t.Errorf("Media = %s, want index %d", got, tt.want)
			}
		})
	}
}

// client.go, Request.Send: "returns at the response headers for every final
// HTTP status. It neither classifies status as success or failure nor
// decodes the body. The caller owns and must close Response.Body."
// client.go, Response.Decode: "using Call's target, codec, empty-body and
// MaxBodyBytes rules for any HTTP status ... never returns a StatusError. A
// failure to read or decode is a *DecodeError holding r, even for a non-2xx
// status. An invalid out is a *DecodeError without consuming or closing
// Body".
func TestSendAndDecode(t *testing.T) {
	body := `{"title":"Not found","detail":"d"}`
	w, c := respClient(t, typedAnswer(404, "application/problem+json", body), nil)
	req := mustPrepare(t, c, "getPet", nil)
	resp, err := req.Send(t.Context())
	if err != nil {
		t.Fatalf("Send of a 404: %v", err)
	}
	if resp.StatusCode != 404 || resp.Declaration != response(t, mustOp(t, c, "getPet"), 1) {
		t.Errorf("status %d, Declaration %+v", resp.StatusCode, resp.Declaration)
	}
	var p Problem
	if err := resp.Decode(&p); err != nil || p.Title != "Not found" {
		t.Errorf("Decode = %+v, %v", p, err)
	}

	// An invalid out: a DecodeError, Body left to read.
	resp, err = req.Send(t.Context())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	err = resp.Decode(Problem{})
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Errorf("Decode(non-pointer) = %v, want a *DecodeError", err)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != body {
		t.Errorf("Body after a refused Decode reads %q, want the whole body", b)
	}
	resp.Body.Close()

	// A decode failure for a non-2xx status is a DecodeError, not a
	// StatusError.
	w.setAnswer(typedAnswer(500, "application/json", `{"title":`))
	resp, err = req.Send(t.Context())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	err = resp.Decode(&p)
	var se *openapi.StatusError
	if !errors.As(err, &de) || errors.As(err, &se) || de.StatusCode != 500 {
		t.Errorf("Decode of a broken 500 = %v, want a *DecodeError holding it", err)
	}

	// The empty-body rule applies to any status.
	w.setAnswer(typedAnswer(404, "application/problem+json", ""))
	resp, err = req.Send(t.Context())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := resp.Decode(&p); !errors.As(err, &de) || !errors.Is(err, io.EOF) {
		t.Errorf("Decode of an empty declared body = %v, want a *DecodeError wrapping io.EOF", err)
	}

	// MaxBodyBytes applies to Decode.
	w.setAnswer(jsonAnswer(200, `"0123456789"`))
	small := c.With(func(o *openapi.Options) { o.MaxBodyBytes = 4 })
	resp, err = mustPrepare(t, small, "getPet", nil).Send(t.Context())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	var s string
	var tooBig *http.MaxBytesError
	if err := resp.Decode(&s); !errors.As(err, &de) || !errors.As(err, &tooBig) {
		t.Errorf("Decode over MaxBodyBytes = %v", err)
	}

	// Send leaves a redirect as it is.
	w.setAnswer(func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "/elsewhere", http.StatusFound)
	})
	resp = sendAndClose(t, req)
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/elsewhere" {
		t.Errorf("Send of a 302 = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, r := range w.requests() {
		if r.Path == "/elsewhere" {
			t.Errorf("Send followed a redirect")
		}
	}
}

var errBoom = errors.New("boom")

// failingReader yields some bytes and then an error.
type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n == 0 {
		f.n++
		return copy(p, `{"partial":`), nil
	}
	return 0, errBoom
}

// client.go, Response.WaitRequest: "It returns nil when no request had a
// body or each body was consumed completely (read to EOF ...) ... The wait
// is safe to repeat and to call concurrently." client.go, Call: "If request-body consumption fails, the
// error wraps its cause".
func TestWaitRequest(t *testing.T) {
	_, c := respClient(t, jsonAnswer(200, `{}`), nil)
	for name, body := range map[string]func() any{
		"no body":        func() any { return nil },
		"encoded":        func() any { return map[string]int{"a": 1} },
		"bytes":          func() any { return []byte(`{"a":1}`) },
		"strings.Reader": func() any { return strings.NewReader(`{"a":1}`) },
		"read once":      func() any { return newOnce(`{"a":1}`) },
	} {
		resp, err := mustPrepare(t, c, "putPet", &openapi.Input{Body: body()}).Send(t.Context())
		if err != nil {
			t.Fatalf("%s: Send: %v", name, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		errs := make(chan error, 3)
		for range 3 {
			go func() { errs <- resp.WaitRequest(t.Context()) }()
		}
		for range 3 {
			if err := <-errs; err != nil {
				t.Errorf("%s: WaitRequest = %v", name, err)
			}
		}
		if err := resp.WaitRequest(t.Context()); err != nil {
			t.Errorf("%s: repeated WaitRequest = %v", name, err)
		}

		resp = mustCall(t, c, "putPet", &openapi.Input{Body: body()}, nil)
		if err := resp.WaitRequest(t.Context()); err != nil {
			t.Errorf("%s: WaitRequest after Call = %v", name, err)
		}
	}

	_, err := c.Call(t.Context(), "putPet", &openapi.Input{Body: &failingReader{}}, nil)
	if !errors.Is(err, errBoom) {
		t.Errorf("Call with a failing body = %v, want it to wrap the reader's error", err)
	}
}

// Redirects: the zero value follows none (client.go, Redirects: "A 3xx not
// followed is the outcome, a *StatusError"), and client.go,
// Options.HTTPClient: "It is used as given and never modified." The
// HTTPClient's CheckRedirect is consulted only on hops the client follows.
func TestRedirectsNotFollowed(t *testing.T) {
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			io.WriteString(rw, "{}")
			return
		}
		http.Redirect(rw, r, "/target", http.StatusSeeOther)
	})
	var checked atomic.Int32
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	withCheck := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { checked.Add(1); return nil }}
	plain := &http.Client{Transport: tr}
	for name, hc := range map[string]*http.Client{"CheckRedirect": withCheck, "plain": plain} {
		c := parseFor(t, w, respDoc, &openapi.Options{HTTPClient: hc})
		resp, err := c.Call(t.Context(), "getPet", nil, nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) || se.StatusCode != 303 || resp == nil || se.Header.Get("Location") != "/target" {
			t.Errorf("%s: %v, want a *StatusError for the 303", name, err)
		}
		if hc.Transport != tr || hc.Timeout != 0 || hc.Jar != nil {
			t.Errorf("%s: the caller's http.Client was modified", name)
		}
	}
	if plain.CheckRedirect != nil {
		t.Errorf("a CheckRedirect was set on the caller's http.Client")
	}
	if n := checked.Load(); n != 0 {
		t.Errorf("CheckRedirect consulted %d times with no hop followed", n)
	}
	for _, r := range w.requests() {
		if r.Path == "/target" {
			t.Errorf("a redirect was followed")
		}
	}
}

// errors.go, RequestError.Unwrap: "returns Err and the errors of Settings
// and Inputs, in key order." (Whether Settings and Inputs are ordered
// together or one after the other is not stated; each keeps its key order
// either way.) errors.go, StatusError.Unwrap and DecodeError.Unwrap: "returns
// e.Err".
func TestErrorUnwrap(t *testing.T) {
	errA, errB, errC, errD, errE := errors.New("a"), errors.New("b"), errors.New("c"), errors.New("d"), errors.New("e")
	re := &openapi.RequestError{
		Settings: map[string]error{"Options.Server": errB, "Input.MediaType": errA},
		Inputs:   map[string]error{"petId": errD, "Input.Body": errC},
		Err:      errE,
	}
	var got []error
	for _, e := range re.Unwrap() {
		if e != nil {
			got = append(got, e)
		}
	}
	if len(got) != 5 || got[0] != errE {
		t.Fatalf("Unwrap = %v, want Err first and all four others", got)
	}
	pos := func(e error) int { return slices.Index(got, e) }
	if pos(errA) > pos(errB) || pos(errC) > pos(errD) || pos(errA) < 0 || pos(errC) < 0 {
		t.Errorf("Unwrap = %v, want each map's errors in key order", got)
	}
	for _, e := range []error{errA, errB, errC, errD, errE} {
		if !errors.Is(re, e) {
			t.Errorf("errors.Is(re, %v) = false", e)
		}
	}

	se := &openapi.StatusError{Err: errA}
	de := &openapi.DecodeError{Err: errB}
	if se.Unwrap() != errA || de.Unwrap() != errB || !errors.Is(se, errA) || !errors.Is(de, errB) {
		t.Errorf("StatusError or DecodeError does not unwrap to Err")
	}
}
