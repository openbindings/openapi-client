package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Request bodies: the JSON codec class, pre-encoded []byte and io.Reader
// bodies, media type selection, replay and Content-Length, and caller
// codecs.

const bodyDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{
	"/json":{"post":{"operationId":"json","requestBody":{"content":{"application/json":{}}}}},
	"/required":{"post":{"operationId":"required","requestBody":{"required":true,"content":{"application/json":{}}}}},
	"/none":{"post":{"operationId":"none"}},
	"/vnd":{"post":{"operationId":"vnd","requestBody":{"content":{"application/vnd.api+json":{}}}}},
	"/two":{"post":{"operationId":"two","requestBody":{"content":{"application/json":{},"application/xml":{}}}}},
	"/range":{"post":{"operationId":"range","requestBody":{"content":{"application/*":{}}}}},
	"/anyrange":{"post":{"operationId":"anyrange","requestBody":{"content":{"*/*":{}}}}},
	"/params":{"post":{"operationId":"params","requestBody":{"content":{"application/json; charset=utf-8":{}}}}},
	"/empty":{"post":{"operationId":"empty","requestBody":{"content":{}}}},
	"/specific":{"post":{"operationId":"specific","requestBody":{"content":{"application/*":{},"application/json":{}}}}},
	"/text":{"post":{"operationId":"text","requestBody":{"content":{"text/plain":{}}}}},
	"/octets":{"put":{"operationId":"octets","requestBody":{"content":{"application/octet-stream":{}}}}},
	"/cbor":{"post":{"operationId":"cbor","requestBody":{"content":{"application/cbor":{}}}}},
	"/get":{"get":{"operationId":"getWithBody","requestBody":{"content":{"application/json":{}}}}},
	"/trace":{"trace":{"operationId":"traceWithBody","requestBody":{"content":{"application/json":{}}}}}
}}`

// namedBytes is a named byte-slice type, which is a value for the codec.
type namedBytes []byte

// doc.go, Values: a JSON type "is written as json.Marshal writes the value,
// with no trailing newline"; "a typed nil, such as a nil pointer or map, is a
// value, which encoding/json writes as null". client.go, Input.Body: "Under a
// JSON type, json.RawMessage("null") sends null, and bytes go as a base64
// string: give the string, or the bytes as a named byte-slice type". doc.go,
// Fixed rules, Header fields: the client generates Content-Type, and
// Content-Length for a body that can be sent again, which every encoded value
// can.
func TestJSONBody(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, bodyDoc, nil)
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"struct", Pet{Name: "Rex", Tag: "dog"}, `{"name":"Rex","tag":"dog"}`},
		{"exact number", map[string]any{"weight": json.Number("12.50")}, `{"weight":12.50}`},
		{"typed nil pointer", (*Pet)(nil), `null`},
		{"nil map", map[string]any(nil), `null`},
		{"RawMessage null", json.RawMessage("null"), `null`},
		{"RawMessage object", json.RawMessage(`{"a":1}`), `{"a":1}`},
		{"string", "hi", `"hi"`},
		{"named bytes as base64", namedBytes("hi"), `"aGk="`},
		// json.Marshal escapes <, > and & ("as encoding/json writes"
		// means json.Marshal).
		{"HTML escaping", map[string]string{"a": "<&>"}, `{"a":"\u003c\u0026\u003e"}`},
		{"array with null", []any{1, "x", nil}, `[1,"x",null]`},
		{"zero", 0, `0`},
		{"false", false, `false`},
		{"empty object", map[string]any{}, `{}`},
		{"empty array", []int{}, `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, c, "json", &openapi.Input{Body: tt.value}, nil)
			got := w.last(t)
			if string(trimNL(got.Body)) != tt.want {
				t.Errorf("body %q, want %q", got.Body, tt.want)
			}
			if ct := got.Header.Values("Content-Type"); !slices.Equal(ct, []string{"application/json"}) {
				t.Errorf("Content-Type = %q", ct)
			}
			if got.ContentLength != int64(len(got.Body)) {
				t.Errorf("Content-Length %d for %d bytes", got.ContentLength, len(got.Body))
			}
		})
	}
}

// client.go, Input.Body: "A value whose type is exactly []byte, or an
// io.Reader, is sent as its bytes, under whatever media type is chosen,
// JSON types included. This is a complete pre-encoded body: the client does
// not inspect its schema".
func TestPreEncodedBody(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, bodyDoc, nil)
	tests := []struct {
		name, key string
		body      any
		want, ct  string
	}{
		{"bytes under JSON", "json", []byte(`{not json`), `{not json`, "application/json"},
		{"reader under JSON", "json", strings.NewReader(`[1,`), `[1,`, "application/json"},
		{"bytes under text", "text", []byte("hello"), "hello", "text/plain"},
		{"reader under octets", "octets", bytes.NewReader([]byte{0, 1, 2, 255}), "\x00\x01\x02\xff", "application/octet-stream"},
		{"empty bytes", "json", []byte{}, "", "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, c, tt.key, &openapi.Input{Body: tt.body}, nil)
			got := w.last(t)
			if string(got.Body) != tt.want || got.Header.Get("Content-Type") != tt.ct {
				t.Errorf("sent %q as %q, want %q as %q", got.Body, got.Header.Get("Content-Type"), tt.want, tt.ct)
			}
			if got.ContentLength != int64(len(tt.want)) {
				t.Errorf("Content-Length %d, want %d", got.ContentLength, len(tt.want))
			}
		})
	}
}

// errors.go, RequestError.Inputs: "a body the operation does not take";
// client.go, Input.Body: "A nil Body where the request body is required is
// refused at Inputs["Input.Body"]", and a typed nil is a value. doc.go,
// Fixed rules, Bodies by method: "a request body declared on TRACE or
// CONNECT ... is ignored, so the operation takes none; otherwise a declared
// body is sent with any method."
func TestBodyPresence(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, bodyDoc, nil)

	for _, tt := range []struct {
		key string
		in  *openapi.Input
	}{
		{"none", &openapi.Input{Body: map[string]any{"a": 1}}},
		{"none", &openapi.Input{Body: []byte("x"), MediaType: "application/json"}},
		{"required", &openapi.Input{}},
		{"required", nil},
		{"traceWithBody", &openapi.Input{Body: map[string]any{"a": 1}}},
	} {
		resp, err := c.Call(t.Context(), tt.key, tt.in, nil)
		re := refusedBeforeSending(t, w, resp, err)
		wantKeys(t, tt.key+" Inputs", re.Inputs, true, "Input.Body")
	}
	if op := mustOp(t, c, "traceWithBody"); op.Body != nil {
		t.Errorf("TRACE operation Body = %+v, want nil", op.Body)
	}

	// A typed nil satisfies a required body.
	mustCall(t, c, "required", &openapi.Input{Body: (*Pet)(nil)}, nil)
	if got := w.last(t); string(trimNL(got.Body)) != "null" {
		t.Errorf("typed nil sent %q, want null", got.Body)
	}
	// An optional body left out sends none.
	mustCall(t, c, "json", nil, nil)
	if got := w.last(t); len(got.Body) != 0 || got.Header.Get("Content-Type") != "" || got.ContentLength != 0 {
		t.Errorf("no body: sent %q as %q, Content-Length %d", got.Body, got.Header.Get("Content-Type"), got.ContentLength)
	}
	// A declared body on GET is sent (OpenAPI 3.1).
	mustCall(t, c, "getWithBody", &openapi.Input{Body: map[string]int{"a": 1}}, nil)
	if got := w.last(t); got.Method != "GET" || string(trimNL(got.Body)) != `{"a":1}` {
		t.Errorf("GET sent %s with %q", got.Method, got.Body)
	}
}

// doc.go, Configuration: "A body uses the declared request media type when
// exactly one is declared and it is concrete; otherwise, a range counting as
// an alternative, it requires Options.MediaType or Input.MediaType."
// client.go, Input.MediaType: "a concrete type matching one the operation
// declares, by the rules on Response.Media ... A range is refused.
// MediaType may carry parameters, which are sent as given"; an empty content
// map takes any concrete type with a pre-encoded body. client.go,
// Request.Media: the Media the Content-Type matches, most specific first,
// "nil when there is no body or none is declared", and "the same immutable
// descriptor Operation.Body.Media exposes".
func TestMediaTypeSelection(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, bodyDoc, nil)
	pet := Pet{Name: "Rex"}
	tests := []struct {
		name, key string
		mediaType string
		body      any
		ct        string   // the Content-Type sent, or "" when refused
		media     int      // the index of Request.Media in Operation.Body.Media, or -1 for nil
		settings  []string // when refused, Settings keys of which one must be present
	}{
		{"sole concrete", "json", "", pet, "application/json", 0, nil},
		{"+json is JSON", "vnd", "", pet, "application/vnd.api+json", 0, nil},
		{"declared with parameters", "params", "", pet, "application/json; charset=utf-8", 0, nil},
		{"two need a choice", "two", "", pet, "", 0, []string{"Input.MediaType", "Options.MediaType"}},
		{"two chosen", "two", "application/json", pet, "application/json", 0, nil},
		{"two chosen raw", "two", "application/xml", []byte("<a/>"), "application/xml", 1, nil},
		{"a range is an alternative", "range", "", pet, "", 0, []string{"Input.MediaType", "Options.MediaType"}},
		{"concrete under a range", "range", "application/merge-patch+json", pet, "application/merge-patch+json", 0, nil},
		{"*/* is an alternative", "anyrange", "", []byte("hi"), "", 0, []string{"Input.MediaType", "Options.MediaType"}},
		{"concrete under */*", "anyrange", "text/plain", []byte("hi"), "text/plain", 0, nil},
		{"undeclared type", "json", "application/xml", []byte("<a/>"), "", 0, []string{"Input.MediaType"}},
		// Response.Media: "every parameter it names is present with an equal
		// value": the declared charset is missing here.
		{"declared parameter missing", "params", "application/json", pet, "", 0, []string{"Input.MediaType"}},
		// A charset is compared without regard to case.
		{"charset case", "params", "application/json; charset=UTF-8", pet, "application/json; charset=UTF-8", 0, nil},
		{"parameters sent as given", "json", "application/json; charset=utf-8", pet, "application/json; charset=utf-8", 0, nil},
		{"type compared without case", "json", "Application/JSON", pet, "Application/JSON", 0, nil},
		{"most specific wins", "specific", "application/json", pet, "application/json", 1, nil},
		{"range match", "specific", "application/problem+json", pet, "application/problem+json", 0, nil},
		{"empty content with a type", "empty", "application/x-custom", []byte("raw"), "application/x-custom", -1, nil},
		{"empty content without a type", "empty", "", []byte("raw"), "", 0, []string{"Input.MediaType", "Options.MediaType"}},
		{"raw under the sole type", "text", "", []byte("hello"), "text/plain", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &openapi.Input{Body: tt.body, MediaType: tt.mediaType}
			before := w.count()
			req, err := c.Prepare(tt.key, in)
			if tt.ct == "" {
				re := asRequestError(t, err)
				if req != nil {
					t.Errorf("Prepare returned a Request")
				}
				wantAnyKey(t, "Settings", re.Settings, tt.settings...)
				resp, err := c.Call(t.Context(), tt.key, in, nil)
				refusedBeforeSending(t, nil, resp, err)
				if w.count() != before {
					t.Errorf("a refused call was sent")
				}
				return
			}
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			op := mustOp(t, c, tt.key)
			if tt.media < 0 {
				if req.Media != nil {
					t.Errorf("Request.Media = %+v, want nil", req.Media)
				}
			} else if req.Media != reqMedia(t, op, tt.media) {
				t.Errorf("Request.Media = %+v, want Operation.Body.Media[%d]", req.Media, tt.media)
			}
			sendAndClose(t, req)
			got := w.last(t)
			if ct := got.Header.Values("Content-Type"); !slices.Equal(ct, []string{tt.ct}) {
				t.Errorf("Content-Type = %q, want %q", ct, tt.ct)
			}
		})
	}

	// A range given as the type is refused.
	for _, mt := range []string{"application/*", "*/*"} {
		resp, err := c.Call(t.Context(), "json", &openapi.Input{Body: pet, MediaType: mt}, nil)
		refusedBeforeSending(t, nil, resp, err)
	}
	// Without a body, Request.Media is nil.
	if req := mustPrepare(t, c, "json", nil); req.Media != nil {
		t.Errorf("Request.Media = %+v without a body", req.Media)
	}
}

// doc.go, Configuration: "Options.Security, Options.SecurityKey and
// Options.MediaType are preferences: each applies to the operations that
// offer its selection, and any other operation is called as if it were
// unset. The Input fields select exactly ... and refuse an operation that
// does not offer their selection." client.go, Input.MediaType: it
// overrides Options.MediaType for one call.
func TestMediaTypePreference(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, bodyDoc, &openapi.Options{MediaType: "application/json"})

	mustCall(t, c, "two", &openapi.Input{Body: Pet{Name: "Rex"}}, nil)
	if got := w.last(t); got.Header.Get("Content-Type") != "application/json" || string(trimNL(got.Body)) != `{"name":"Rex"}` {
		t.Errorf("two sent %q as %q", got.Body, got.Header.Get("Content-Type"))
	}
	// text does not declare application/json: called as if unset.
	mustCall(t, c, "text", &openapi.Input{Body: []byte("hi")}, nil)
	if got := w.last(t); got.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("text sent as %q, want text/plain", got.Header.Get("Content-Type"))
	}
	// Input.MediaType overrides the preference.
	mustCall(t, c, "two", &openapi.Input{Body: []byte("<a/>"), MediaType: "application/xml"}, nil)
	if got := w.last(t); got.Header.Get("Content-Type") != "application/xml" {
		t.Errorf("two sent as %q, want application/xml", got.Header.Get("Content-Type"))
	}
}

// client.go, Input.Body: "A Part or io.Reader inside a value the client
// encodes with encoding/json is refused with an Inputs entry at its place in
// Body"; errors.go, RequestError.Inputs: "Input.Body" followed by a JSON
// Pointer (RFC 6901: "/" in a key is "~1").
func TestReaderInsideJSONRefused(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, bodyDoc, nil)
	type withReader struct {
		F io.Reader `json:"f"`
	}
	tests := []struct {
		name string
		body any
		key  string
	}{
		{"map member", map[string]any{"file": strings.NewReader("x")}, "Input.Body/file"},
		{"array item", map[string]any{"a": []any{1, openapi.Part{Content: "x"}}}, "Input.Body/a/1"},
		{"escaped key", map[string]any{"x/y": strings.NewReader("")}, "Input.Body/x~1y"},
		{"struct field", withReader{F: strings.NewReader("x")}, "Input.Body/f"},
		// A Part as the body itself, under a JSON type.
		{"Part as the body", openapi.Part{Content: []byte("x")}, "Input.Body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := c.Call(t.Context(), "json", &openapi.Input{Body: tt.body}, nil)
			re := refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, tt.key)
		})
	}
}

// closeRecorder is a read-once reader that records whether it was closed.
type closeRecorder struct {
	io.Reader
	mu     sync.Mutex
	closed bool
}

func (c *closeRecorder) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *closeRecorder) wasClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// client.go, Input.Body: "A body can be sent again ... when every source in
// it can: a []byte, a *bytes.Buffer, a *bytes.Reader and a *strings.Reader,
// from the bytes they hold when the call is prepared, without being drained
// ...; an *os.File that Stat reports to be a regular file, from its offset
// when the call is prepared; and every value the client encodes. Any other
// reader ... is read once". client.go, Request.HTTP: "GetBody is set when
// the body can be sent again". doc.go, Fixed rules, Header fields:
// Content-Length "for a body that can be sent again". "The client never
// closes a reader it is given."
func TestBodyReplay(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, bodyDoc, nil)

	buf := bytes.NewBufferString("0123456789")
	buf.Next(3)
	br := bytes.NewReader([]byte("abcdef"))
	br.Read(make([]byte, 2))
	sr := strings.NewReader("uvwxyz")
	sr.Read(make([]byte, 1))
	path := filepath.Join(t.TempDir(), "body.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Seek(4, io.SeekStart)

	tests := []struct {
		name  string
		body  any
		want  string
		after func() bool // reports that the source was left undrained
	}{
		{"bytes", []byte("raw"), "raw", nil},
		{"bytes.Buffer", buf, "3456789", func() bool { return buf.Len() == 7 }},
		{"bytes.Reader", br, "cdef", func() bool { return br.Len() == 4 }},
		{"strings.Reader", sr, "vwxyz", func() bool { return sr.Len() == 5 }},
		{"regular file", f, "456789", func() bool { _, err := f.Stat(); return err == nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustPrepare(t, c, "octets", &openapi.Input{Body: tt.body})
			if req.HTTP.GetBody == nil {
				t.Fatalf("GetBody = nil for a body that can be sent again")
			}
			for range 2 {
				rc, err := req.HTTP.GetBody()
				if err != nil {
					t.Fatalf("GetBody: %v", err)
				}
				b, _ := io.ReadAll(rc)
				rc.Close()
				if string(b) != tt.want {
					t.Errorf("GetBody gave %q, want %q", b, tt.want)
				}
			}
			if req.HTTP.ContentLength != int64(len(tt.want)) {
				t.Errorf("Request.HTTP.ContentLength = %d, want %d", req.HTTP.ContentLength, len(tt.want))
			}
			// client.go, Request.Call: a body that can be sent again "may be
			// sent any number of times".
			before := w.count()
			for range 2 {
				if _, err := req.Call(t.Context(), nil); err != nil {
					t.Fatalf("Request.Call: %v", err)
				}
			}
			reqs := w.requests()[before:]
			if len(reqs) != 2 {
				t.Fatalf("server received %d requests, want 2", len(reqs))
			}
			for _, r := range reqs {
				if string(r.Body) != tt.want || r.ContentLength != int64(len(tt.want)) {
					t.Errorf("sent %q with Content-Length %d, want %q", r.Body, r.ContentLength, tt.want)
				}
			}
			if tt.after != nil && !tt.after() {
				t.Errorf("the source was drained or closed")
			}
		})
	}

	// A read-once reader: no GetBody, no Content-Length (so chunked on
	// HTTP/1.1), never closed, and a second send refused with nothing sent
	// (client.go, Request.Call).
	once := &closeRecorder{Reader: strings.NewReader("stream")}
	req := mustPrepare(t, c, "octets", &openapi.Input{Body: once})
	if req.HTTP.GetBody != nil {
		t.Errorf("GetBody set for a reader that is read once")
	}
	before := w.count()
	if _, err := req.Call(t.Context(), nil); err != nil {
		t.Fatalf("Request.Call: %v", err)
	}
	got := w.last(t)
	if string(got.Body) != "stream" || !slices.Contains(got.TransferEncoding, "chunked") {
		t.Errorf("sent %q with Transfer-Encoding %q, want chunked", got.Body, got.TransferEncoding)
	}
	resp, err := req.Call(t.Context(), nil)
	refusedBeforeSending(t, nil, resp, err)
	if _, err := req.Send(t.Context()); err == nil {
		t.Errorf("a third send of a read-once body was not refused")
	} else {
		asRequestError(t, err)
	}
	if w.count() != before+1 {
		t.Errorf("server received %d requests, want 1", w.count()-before)
	}
	if once.wasClosed() {
		t.Errorf("the client closed a reader it was given")
	}
}

// client.go, Request.Call: a Request whose body can be sent again "may be
// sent any number of times, concurrently too".
func TestRequestConcurrentSends(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, bodyDoc, nil)
	req := mustPrepare(t, c, "json", &openapi.Input{Body: map[string]int{"n": 1}})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := req.Call(t.Context(), nil)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Request.Call: %v", err)
		}
	}
	reqs := w.requests()
	if len(reqs) != 8 {
		t.Fatalf("server received %d requests, want 8", len(reqs))
	}
	for _, r := range reqs {
		if string(trimNL(r.Body)) != `{"n":1}` {
			t.Errorf("sent %q", r.Body)
		}
	}
}

// tagCodec is a caller codec that writes and reads a recognizable form.
type tagCodec struct {
	tag       string
	encodeErr error
}

func (c tagCodec) Encode(w io.Writer, v any) error {
	if c.encodeErr != nil {
		return c.encodeErr
	}
	_, err := fmt.Fprintf(w, "%s(%v)", c.tag, v)
	return err
}

func (c tagCodec) Decode(r io.Reader, v any) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	switch p := v.(type) {
	case *any:
		*p = c.tag + ":" + string(b)
	case *string:
		*p = c.tag + ":" + string(b)
	default:
		return fmt.Errorf("%s cannot decode into %T", c.tag, v)
	}
	return nil
}

// client.go, Options.Codecs: a key is a media type without parameters or a
// structured syntax suffix, "compared without regard to case; a type key
// wins over a suffix key, which wins over a built-in codec ... A *[]byte or
// io.Writer target, and a []byte or io.Reader body, bypass codecs ... An
// Encode error refuses the call at the body's ... Inputs key". doc.go,
// Values: "a caller's codec ... receives the value as given"; "a type with
// a caller's codec takes a value of any Go type".
func TestCodecsForRequestBodies(t *testing.T) {
	w := newWire(t, nil)
	errEncode := errors.New("cannot encode")
	tests := []struct {
		name   string
		codecs map[string]openapi.Codec
		key    string
		body   any
		want   string
	}{
		{"new type", map[string]openapi.Codec{"application/cbor": tagCodec{tag: "CBOR"}}, "cbor", 5, "CBOR(5)"},
		{"key case", map[string]openapi.Codec{"APPLICATION/CBOR": tagCodec{tag: "CBOR"}}, "cbor", 5, "CBOR(5)"},
		{"text type", map[string]openapi.Codec{"text/plain": tagCodec{tag: "TXT"}}, "text", 42, "TXT(42)"},
		{"replaces JSON", map[string]openapi.Codec{"application/json": tagCodec{tag: "FAKE"}}, "json", 7, "FAKE(7)"},
		// The value as given, not converted to JSON data first.
		{"value as given", map[string]openapi.Codec{"application/json": tagCodec{tag: "FAKE"}}, "json", tagged{A: "1", B: "2"}, "FAKE({1 2 })"},
		{"suffix key", map[string]openapi.Codec{"+json": tagCodec{tag: "SUFFIX"}}, "vnd", 1, "SUFFIX(1)"},
		{"type key wins", map[string]openapi.Codec{"+json": tagCodec{tag: "SUFFIX"}, "application/vnd.api+json": tagCodec{tag: "TYPE"}}, "vnd", 1, "TYPE(1)"},
		// application/json has no +json suffix: the built-in codec stays.
		{"suffix does not cover application/json", map[string]openapi.Codec{"+json": tagCodec{tag: "SUFFIX"}}, "json", 1, "1"},
		// A type key covers only its type.
		{"type key does not cover another", map[string]openapi.Codec{"application/json": tagCodec{tag: "FAKE"}}, "vnd", 1, "1"},
		{"bytes bypass", map[string]openapi.Codec{"application/json": tagCodec{tag: "FAKE"}}, "json", []byte(`{"raw":true}`), `{"raw":true}`},
		{"reader bypasses", map[string]openapi.Codec{"application/cbor": tagCodec{tag: "CBOR"}}, "cbor", strings.NewReader("\xa0"), "\xa0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := parseFor(t, w, bodyDoc, &openapi.Options{Codecs: tt.codecs})
			mustCall(t, c, tt.key, &openapi.Input{Body: tt.body}, nil)
			if got := w.last(t); string(trimNL(got.Body)) != tt.want {
				t.Errorf("body %q, want %q", got.Body, tt.want)
			}
		})
	}

	c := parseFor(t, w, bodyDoc, &openapi.Options{Codecs: map[string]openapi.Codec{"application/cbor": tagCodec{tag: "CBOR", encodeErr: errEncode}}})
	before := w.count()
	resp, err := c.Call(t.Context(), "cbor", &openapi.Input{Body: 5}, nil)
	re := asRequestError(t, err)
	if resp != nil || w.count() != before {
		t.Errorf("a body that failed to encode was sent")
	}
	wantKeys(t, "Inputs", re.Inputs, true, "Input.Body")
	if !errors.Is(re.Inputs["Input.Body"], errEncode) && !errors.Is(err, errEncode) {
		t.Errorf("the refusal does not wrap the codec's error")
	}
}

// client.go, Options.Codecs: "Load refuses any other key, and one that names
// a sequential, multipart or application/x-www-form-urlencoded type, whose
// framing and field encoding stay the client's, as OpenAPI's Encoding Object
// governs them". The refusal is keyed Options.Codecs (errors.go,
// RequestError.Settings).
func TestLoadRefusesCodecKeys(t *testing.T) {
	codec := tagCodec{tag: "X"}
	for _, key := range []string{
		"application/json; charset=utf-8", // parameters
		"application/*",                   // a range
		"*/*",
		"json",                // neither a type nor a suffix
		"",                    // empty
		"multipart/form-data", // multipart framing
		"multipart/mixed",
		"application/jsonl", // sequential framing
		"application/x-ndjson",
		"application/json-seq",
		"text/event-stream",
		"+json-seq",
		"application/x-www-form-urlencoded", // form field encoding
		"Application/X-WWW-Form-Urlencoded",
	} {
		_, err := openapi.Parse(t.Context(), []byte(expand(bodyDoc, "https://api.example.test")), testDocURI,
			&openapi.Options{Codecs: map[string]openapi.Codec{key: codec}})
		if err == nil {
			t.Errorf("Codecs key %q accepted", key)
			continue
		}
		wantKeys(t, fmt.Sprintf("Settings for %q", key), asRequestError(t, err).Settings, false, "Options.Codecs")
	}
	for _, key := range []string{"application/cbor", "+cbor", "APPLICATION/CBOR", "application/json", "+json", "text/csv", "application/xml"} {
		if _, err := openapi.Parse(t.Context(), []byte(expand(bodyDoc, "https://api.example.test")), testDocURI,
			&openapi.Options{Codecs: map[string]openapi.Codec{key: codec}}); err != nil {
			t.Errorf("Codecs key %q refused: %v", key, err)
		}
	}
}
