package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The tests here cover production code that no other test reached (go test
// -coverprofile, child processes included) where a contract line needs its
// behavior, each citing that line. Each pins behavior the client already had
// unless it says otherwise.

const contractLinePaths = `
	"/f":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"s":{"type":"string"},"j":{"type":"object"},
			"t":{"type":"object","properties":{"a":{"type":"string"},"n":{"type":"integer"}}}}},
		"encoding":{"l":{"contentType":"application/jsonl"},"m":{"contentType":"multipart/mixed"},
			"t":{"contentType":"application/x-www-form-urlencoded"}}}}}}},
	"/m":{"post":{"operationId":"mp","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"s":{"type":"string"},"j":{"type":"object"},"arr":{"type":"array"}}},
		"encoding":{"l":{"contentType":"application/x-ndjson"}}}}}}},
	"/q":{"get":{"operationId":"params","parameters":[
		{"name":"fq","in":"query","content":{"application/x-www-form-urlencoded":{}}},
		{"name":"jq","in":"query","content":{"application/json":{}}},
		{"name":"sq","in":"query","schema":{}},
		{"name":"dq","in":"query","style":"spaceDelimited","explode":true,"schema":{}}]}},
	"/p/{id}":{"get":{"operationId":"path","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}`

func contractLineClient(t *testing.T) (*wire, *openapi.Client) {
	t.Helper()
	w := newWire(t, nil)
	return w, parseFor(t, w, doc31(contractLinePaths), nil)
}

// refusedAt calls key with in and checks that it is refused before sending,
// at exactly the Inputs (or, with settings, Settings) key at.
func refusedAt(t *testing.T, w *wire, c *openapi.Client, key string, in *openapi.Input, settings bool, at string) *openapi.RequestError {
	t.Helper()
	before := w.count()
	resp, err := c.Call(t.Context(), key, in, nil)
	re := refusedSince(t, w, before, resp, err)
	if settings {
		wantKeys(t, "Settings", re.Settings, true, at)
	} else {
		wantKeys(t, "Inputs", re.Inputs, true, at)
	}
	return re
}

// A property or part value its media type cannot encode is refused at its
// Inputs key (errors.go, RequestError.Inputs: "Input.Body" followed by "a
// JSON Pointer to the part of Body concerned"); doc.go, Values: "A body
// under a form, multipart or sequential type takes the shapes Input.Body
// lists". A form field typed JSON Lines or multipart, and a multipart part
// typed NDJSON, cannot encode an object.
func TestFieldMediaTypesThatCannotEncodeObject(t *testing.T) {
	w, c := contractLineClient(t)
	obj := map[string]any{"a": 1}
	refusedAt(t, w, c, "form", &openapi.Input{Body: map[string]any{"l": obj}}, false, "Input.Body/l")
	refusedAt(t, w, c, "form", &openapi.Input{Body: map[string]any{"m": obj}}, false, "Input.Body/m")
	refusedAt(t, w, c, "mp", &openapi.Input{Body: map[string]any{"l": obj}}, false, "Input.Body/l")
}

// In an application/x-www-form-urlencoded body, a field's reader that can be
// read only once is streamed as the body is read, and a replayable one is read
// when the call is prepared (doc.go, Fixed rules, Form bodies: "In an
// application/x-www-form-urlencoded body, a field's reader that can be sent
// again is read into memory when the call is prepared, and one that can be
// read only once is encoded as the transport reads it"). The read-once
// reader's error fails the call (client.go, Client.Call: "If request-body
// consumption fails, the error wraps its cause"); the replayable reader's, an
// *os.File opened only for writing, refuses it, kept for errors.As (errors.go,
// RequestError.Inputs: "An error with a cause, such as encoding/json's, a
// codec's, a reader's or a ParamWriters function's error, or that Message.Err
// or Media.Err, wraps it, for errors.Is and errors.As").
func TestFormFieldReaderErrors(t *testing.T) {
	w, c := contractLineClient(t)
	errRead := errors.New("the disk went away")
	_, err := c.Call(t.Context(), "form", &openapi.Input{Body: map[string]any{
		"s": &onceReader{io.MultiReader(strings.NewReader("ab"), iotest.ErrReader(errRead))},
	}}, nil)
	if !errors.Is(err, errRead) {
		t.Errorf("Call = %v, want the reader's error", err)
	}
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "w"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("content"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	re := refusedAt(t, w, c, "form", &openapi.Input{Body: map[string]any{"s": f}}, false, "Input.Body/s")
	var pe *fs.PathError
	if !errors.As(re, &pe) {
		t.Errorf("refusal %v does not keep the file's *fs.PathError", re)
	}
}

// Input.MediaType: "a boundary given for a multipart body the client encodes
// is used, a part whose content holds its delimiter, or "--" and the boundary
// after a CR or LF, being an input that cannot be encoded (RFC 2046 section
// 5.1.1)". Content streamed from a reader is checked as it streams, and a
// delimiter found there ends the body as an upload error. A reader that yields
// one byte at a time splits the delimiter across reads, which the check still
// finds; the same reader without it is sent whole.
func TestMultipartDelimiterAcrossShortReads(t *testing.T) {
	w, c := contractLineClient(t)
	in := func(content string) *openapi.Input {
		return &openapi.Input{MediaType: "multipart/form-data; boundary=B", Body: map[string]any{
			"s": openapi.Part{Content: &onceReader{iotest.OneByteReader(strings.NewReader(content))}, MediaType: "text/plain"},
		}}
	}
	mustCall(t, c, "mp", in("a-B\r-x\n-B"), nil)
	got := w.last(t)
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	checkParts(t, parts, []wantPart{{disposition: formData("s", "s"), ctype: "text/plain", content: "a-B\r-x\n-B"}})
	_, err := c.Call(t.Context(), "mp", in("ab\r\n--B--\r\n"), nil)
	if err == nil || isRequestError(err) {
		t.Errorf("Call = %v, want an upload error for the delimiter in the content", err)
	}
}

// doc.go, Values: "a reader, and a multipart or sequential media type,
// cannot serialize a parameter and are refused at its key": a form content
// parameter holding a reader.
func TestFormContentParameterRefusesReaders(t *testing.T) {
	w, c := contractLineClient(t)
	refusedAt(t, w, c, "params", &openapi.Input{Params: map[string]any{"fq": map[string]any{"a": strings.NewReader("x")}}}, false, "fq")
}

// A form field typed application/x-www-form-urlencoded is encoded by the
// client's form encoder from its schema's fields, as is every form-typed part
// or content parameter (Options.Codecs: a form key is refused, as "OpenAPI's
// Encoding Object governs" form encoding), and then as the value of its field
// (doc.go, Fixed rules, Form bodies).
func TestFormTypedFieldInFormBody(t *testing.T) {
	w, c := contractLineClient(t)
	mustCall(t, c, "form", &openapi.Input{Body: map[string]any{"t": map[string]any{"a": "b c", "n": 5}}}, nil)
	if got, want := string(w.last(t).Body), formPairs("t", "a=b+c&n=5"); got != want {
		t.Errorf("form body %q, want %q", got, want)
	}
}

// nullJSON is written by its MarshalJSON as null.
type nullJSON struct{}

func (nullJSON) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// doc.go, Values: "A form or multipart property or array item, or a
// positional part, whose JSON data is null is omitted, whatever its
// serialization or media type": JSON data null from
// a json.RawMessage under a JSON field, and from a MarshalJSON under a text
// field.
func TestNullJSONFieldsOmitted(t *testing.T) {
	w, c := contractLineClient(t)
	body := map[string]any{"j": json.RawMessage("null"), "s": nullJSON{}, "t": map[string]any{"a": "x"}}
	mustCall(t, c, "form", &openapi.Input{Body: body}, nil)
	if got := string(w.last(t).Body); got != "t=a%3Dx" {
		t.Errorf("form body %q, want t=a%%3Dx", got)
	}
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{"j": json.RawMessage("null"), "s": nullJSON{}, "arr": []any{"x", nullJSON{}}})
	checkParts(t, parts, []wantPart{{disposition: formData("arr"), ctype: "application/octet-stream", content: "x"}})
}

// Input.MediaType: a given boundary's delimiter cannot appear in a part,
// RFC 2046 section 5.1.1: "the boundary delimiter MUST NOT appear inside any
// of the encapsulated parts", so a nested boundary may not begin with an
// enclosing one: a generated one never does. A nested multipart part whose
// given boundary begins with the outer one is refused at its key, whether it
// has parts (each refused too) or only its close delimiter.
func TestNestedBoundaryHoldingOuterRefused(t *testing.T) {
	w, c := contractLineClient(t)
	for _, content := range []map[string]any{{}, {"a": "x"}} {
		before := w.count()
		resp, err := c.Call(t.Context(), "mp", &openapi.Input{MediaType: "multipart/form-data; boundary=B", Body: map[string]any{
			"n": openapi.Part{Content: content, MediaType: "multipart/mixed; boundary=Bx"},
		}}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Inputs", re.Inputs, false, "Input.Body/n") // and each nested part's key
	}
}

// Input.MediaType: a boundary is "checked", and "Two boundary parameters in a
// multipart type are refused"; a part's media type problem is at its key in
// Settings (errors.go, RequestError.Settings: "for a part's media type,
// "Input.Body" followed by the part's JSON Pointer"). A nested multipart
// Part.MediaType with an invalid boundary, or two, is refused.
func TestPartBoundaryRefused(t *testing.T) {
	w, c := contractLineClient(t)
	for _, mt := range []string{`multipart/mixed; boundary="a "`, "multipart/mixed; boundary=a; boundary=b", `multipart/mixed; boundary="a@b"`} {
		refusedAt(t, w, c, "mp", &openapi.Input{Body: map[string]any{
			"n": openapi.Part{Content: map[string]any{"a": "x"}, MediaType: mt},
		}}, true, "Input.Body/n")
	}
}

// Input.MediaType's boundary is checked against RFC 2046 section 5.1.1:
// "bcharsnospace := DIGIT / ALPHA / "'" / "(" / ")" / "+" / "_" / "," / "-" /
// "." / "/" / ":" / "=" / "?"" and the space, so "@", "~", "*" and a byte
// past ASCII are refused at Input.MediaType, and every bchar
// is taken.
func TestBoundaryCharacters(t *testing.T) {
	w, c := contractLineClient(t)
	for _, b := range []string{`"a@b"`, `"a~b"`, `"a*b"`, "\"a\xc3\xa9b\""} {
		refusedAt(t, w, c, "mp", &openapi.Input{MediaType: "multipart/form-data; boundary=" + b, Body: map[string]any{"s": "x"}}, true, "Input.MediaType")
	}
	b := `"0aZ'()+_,-./:=? x"`
	mustCall(t, c, "mp", &openapi.Input{MediaType: "multipart/form-data; boundary=" + b, Body: map[string]any{"s": "x"}}, nil)
	got := w.last(t)
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	checkParts(t, parts, []wantPart{{disposition: formData("s"), ctype: "text/plain", content: "x"}})
}

// An array's default content type follows its items (client.go, Input.Body:
// "an array schema's items type by default"), and a schema without type allows
// all, so an array whose items are absent has the absent type,
// application/octet-stream (OAS 3.1.2 section 4.8.15.1.1), and
// each item is sent so.
func TestArrayWithoutItemsDefaultsToOctetStream(t *testing.T) {
	w, c := contractLineClient(t)
	if e := encodingByName(t, reqMedia(t, mustOp(t, c, "mp"), 0))["arr"]; e == nil || e.ContentType != "application/octet-stream" {
		t.Errorf("arr = %+v, want ContentType application/octet-stream", e)
	}
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{"arr": []string{"x", "y"}})
	checkParts(t, parts, []wantPart{
		{disposition: formData("arr"), ctype: "application/octet-stream", content: "x"},
		{disposition: formData("arr"), ctype: "application/octet-stream", content: "y"},
	})
}

// client.go, Input.Body: "For form and multipart media, Body is an object
// whose properties are the fields: a value whose JSON data is an object, such
// as a struct, a non-nil map or a non-nil pointer to either, but not a Part, a
// *Part, or a pointer to either or to a reader, which is refused at
// Inputs["Input.Body"]"; a typed nil body is a value (doc.go, Values: "a typed
// nil, such as a nil pointer or map, is a value, which encoding/json writes as
// null"), and null is no object. A typed nil pointer to a struct, a pointer to
// a *Part and a pointer to a reader are refused at Input.Body.
func TestFormBodyThatIsNoObjectRefused(t *testing.T) {
	w, c := contractLineClient(t)
	part := &openapi.Part{Content: "x"}
	reader := strings.NewReader("x")
	for _, body := range []any{(*struct{ S string })(nil), &part, &reader} {
		for _, key := range []string{"form", "mp"} {
			refusedAt(t, w, c, key, &openapi.Input{Body: body}, false, "Input.Body")
		}
	}
}

// client.go, Client: "The zero Client has no operations: it describes
// nothing and refuses every call with a *RequestError wrapping
// ErrNoOperation", and so does a Client With derives from it.
func TestZeroClientRefusesCalls(t *testing.T) {
	var c openapi.Client
	if v, uris, d := c.Version(), c.DocumentURIs(), c.Document(""); v != "" || len(uris) != 0 || d != nil {
		t.Errorf("the zero Client describes Version %q, DocumentURIs %q, Document %q", v, uris, d)
	}
	derived := c.With(func(o *openapi.Options) { o.BaseURL = "https://api.example.test" })
	for _, c := range []*openapi.Client{&c, derived} {
		resp, err := c.Call(t.Context(), "anything", nil, nil)
		if !errors.Is(err, openapi.ErrNoOperation) || !isRequestError(err) || resp != nil {
			t.Errorf("Call = %v, %v; want a *RequestError wrapping ErrNoOperation", resp, err)
		}
	}
}

// client.go, Request.Call "returns as [Client.Call] does", and Call: "out
// must be nil, a non-nil *[]byte, an io.Writer, or a non-nil pointer;
// anything else ... is refused before sending".
func TestRequestCallChecksOut(t *testing.T) {
	w, c := contractLineClient(t)
	req := mustPrepare(t, c, "params", nil)
	for _, out := range []any{5, (*int)(nil)} {
		resp, err := req.Call(t.Context(), out)
		refusedBeforeSending(t, w, resp, err)
	}
}

// client.go, Response.WaitRequest: "It returns nil when no request carried a
// body", as for a Response the client did not make.
func TestWaitRequestWithoutRequest(t *testing.T) {
	for _, r := range []*openapi.Response{{}, {Response: &http.Response{StatusCode: 200}}} {
		if err := r.WaitRequest(t.Context()); err != nil {
			t.Errorf("WaitRequest = %v, want nil", err)
		}
	}
}

// The error types' Error methods describe a value with no Response, as a
// caller may build one, without failing: no panic, and some text.
func TestErrorTypesWithoutResponse(t *testing.T) {
	for _, err := range []error{&openapi.StatusError{}, &openapi.DecodeError{}} {
		var msg string
		noPanic(t, fmt.Sprintf("%T.Error", err), func() { msg = err.Error() })
		if msg == "" {
			t.Errorf("%T.Error() is empty", err)
		}
	}
}

// errors.go, RequestError.Settings: "A setting the document cannot use, or one
// that conflicts with another, is keyed by its field, at Load or at a call":
// Options.Redirects outside its two values (client.go, Redirects), and an
// Options.MediaType that is no concrete media type (client.go,
// Options.MediaType: "selects a concrete request media type"), refuse Load.
func TestLoadRefusesUnusableRedirectsAndMediaType(t *testing.T) {
	doc := doc31(`"/x":{"post":{"operationId":"x","requestBody":{"content":{"application/json":{}}}}}`)
	for _, tt := range []struct {
		opts *openapi.Options
		key  string
	}{
		{&openapi.Options{Redirects: 7}, "Options.Redirects"},
		{&openapi.Options{MediaType: "application/*"}, "Options.MediaType"},
		{&openapi.Options{MediaType: "not a media type"}, "Options.MediaType"},
	} {
		err := parseErr(t, doc, "https://api.example.test", tt.opts)
		wantKeys(t, "Settings", asRequestError(t, err).Settings, false, tt.key)
	}
}

// load.go, Loader.Fetch: "Fetch, if set, retrieves each document the loader
// needs in place of the default ... It returns the content ... and the URI
// it was finally retrieved from ...; an empty final means uri";
// a URI scheme other than http, https and file, with no
// Fetch, is refused.
func TestLoaderFetchEmptyFinalURI(t *testing.T) {
	const uri = "https://docs.example.test/openapi.json"
	var asked []string
	l := openapi.Loader{Fetch: func(ctx context.Context, u string) (io.ReadCloser, string, error) {
		asked = append(asked, u)
		return io.NopCloser(strings.NewReader(doc31(`"/x":{"get":{"operationId":"x"}}`))), "", nil
	}}
	c, err := l.Load(t.Context(), uri, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if uris := c.DocumentURIs(); len(uris) != 1 || uris[0] != uri || len(asked) != 1 || asked[0] != uri {
		t.Errorf("DocumentURIs %q after fetching %q, want %s", uris, asked, uri)
	}
	if c, err := openapi.Load(t.Context(), "ftp://docs.example.test/openapi.json", nil); err == nil || c != nil {
		t.Errorf("Load of an ftp URI = %v, %v; want an error", c, err)
	}
}

// load.go, Parse: "The uri, if not empty, is the absolute URI ... the
// document is meant to live at": a relative or unparsable one is refused.
func TestParseURIMustBeAbsolute(t *testing.T) {
	doc := []byte(bare31(`"/x":{"get":{}}`))
	for _, uri := range []string{"openapi.json", "/specs/openapi.json", "https://api example.test/openapi.json"} {
		if c, err := openapi.Parse(t.Context(), doc, uri, nil); err == nil || c != nil {
			t.Errorf("Parse with uri %q = %v, %v; want an error", uri, c, err)
		}
	}
}

// laterCanceled is a context whose Err is nil for its first n calls and
// context.Canceled after, so a load meets the end of its context at each
// check in turn.
type laterCanceled struct {
	context.Context
	n int
}

func (c *laterCanceled) Err() error {
	if c.n > 0 {
		c.n--
		return nil
	}
	return context.Canceled
}

// load.go, Load: "ctx bounds the whole load": a context that ends at any point
// of a load ends it with an error matching context.Canceled, while the
// document is read (every 65,535 nodes), while its operations are
// indexed and while Load checks the Options' names;
// the load either completes or fails so, at every
// point.
func TestLoadEndsWithContextAtEveryCheck(t *testing.T) {
	var b strings.Builder
	b.WriteString(`"/x":{"post":{"operationId":"x","requestBody":{"content":{"application/json":{}}}}},"/y":{"get":{}},"x-big":[`)
	b.WriteString(strings.TrimSuffix(strings.Repeat("0,", 70000), ","))
	b.WriteString("]")
	doc := []byte(expand(doc31(b.String()), "https://api.example.test"))
	failed := 0
	for n := range 12 {
		c, err := openapi.Parse(&laterCanceled{context.Background(), n}, doc, testDocURI, &openapi.Options{MediaType: "application/json"})
		switch {
		case err == nil && c != nil:
		case errors.Is(err, context.Canceled) && c == nil:
			failed++
		default:
			t.Errorf("Parse after %d checks = %v, %v; want a Client or an error matching context.Canceled", n, c, err)
		}
	}
	if failed < 3 {
		t.Errorf("only %d of 12 loads ended with their context", failed)
	}
}

// load.go, Loader: references resolve as RFC 3986 and RFC 6901 say, and
// errors.go, ErrUnresolved "is wrapped by the Err of a part, or of a
// SchemaReference, whose defect is a reference that cannot be resolved": a $ref
// that is no URI reference, a fragment that is no JSON Pointer or whose
// percent-encoding is invalid cannot be resolved; one that names the document
// by its own URI resolves (load.go: "A reference resolves first to what loaded
// documents identify: a document by its retrieval URI").
func TestReferenceFormsUnresolved(t *testing.T) {
	c := parseAt(t, doc31(`"/x":{"get":{"operationId":"x","parameters":[
		{"$ref":"%zz"},{"$ref":"#plain-name"},{"$ref":"#/components/parameters/a%zz"},
		{"$ref":"openapi.json#/components/parameters/p"}]}}`,
		`"components":{"parameters":{"p":{"name":"p","in":"query","schema":{}}}}`), "https://api.example.test", testDocURI, nil)
	op := mustOp(t, c, "x")
	if len(op.Params) != 4 {
		t.Fatalf("%d parameters, want 4", len(op.Params))
	}
	for i, p := range op.Params[:3] {
		if !errors.Is(p.Err, openapi.ErrUnresolved) {
			t.Errorf("parameter %d: Err %v, want ErrUnresolved", i, p.Err)
		}
	}
	if p := op.Params[3]; p.Err != nil || p.Name != "p" {
		t.Errorf("a reference by the document's URI: %+v", p)
	}
}

// describe.go, Operation: a Path Item's field "on both sides, which OpenAPI
// leaves undefined, sets Err on each operation whose request it affects ...
// every operation that uses the parameters or servers"; a path template with
// an unclosed "{" is a defect of its operation; and a Paths key that does not
// begin with "/" is listed only to report it (describe.go, Client.Operations:
// "a Paths entry that cannot be read, because its key does not begin with "/"
// or its $ref cannot be followed, listed once with its Path and Err and no
// Method"), which does not upset Load's check of Options.MediaType.
func TestPathItemDefectsReported(t *testing.T) {
	c := parseAt(t, doc31(`
		"nope":{"get":{"operationId":"nope"}},
		"/both":{"$ref":"#/components/pathItems/P","parameters":[{"name":"a","in":"query","schema":{}}]},
		"/open/{id":{"post":{"operationId":"open","requestBody":{"content":{"application/json":{}}}}}`,
		`"components":{"pathItems":{"P":{"get":{"operationId":"both"},"parameters":[{"name":"b","in":"query","schema":{}}]}}}`),
		"https://api.example.test", testDocURI, &openapi.Options{MediaType: "application/json"})
	for _, key := range []string{"both", "open"} {
		if op, _ := c.Operation(key); op == nil || op.Err == nil {
			t.Errorf("%s: %+v, want an Err", key, op)
		}
	}
	var defect bool
	for _, op := range c.Operations() {
		defect = defect || op.Key == "" && op.Err != nil && op.Path == "nope"
	}
	if !defect {
		t.Errorf("Operations() lists no defect entry for the Paths key nope")
	}
}

// describe.go, Param: AllowEmptyValue is the parameter's declared
// allowEmptyValue; a location other than path, query,
// header and cookie, and a content key that is no media type, are the
// parameter's Err.
func TestParameterDeclarationDefects(t *testing.T) {
	c := parseAt(t, doc31(`"/x":{"get":{"operationId":"x","parameters":[
		{"name":"e","in":"query","allowEmptyValue":true,"schema":{}},
		{"name":"b","in":"body","schema":{}},
		{"name":"c","in":"query","content":{"not a type":{}}}]}}`), "https://api.example.test", testDocURI, nil)
	op := mustOp(t, c, "x")
	if p := param(t, op, 0); !p.AllowEmptyValue || p.Err != nil {
		t.Errorf("e = %+v, want AllowEmptyValue", p)
	}
	for i := 1; i < 3; i++ {
		if p := param(t, op, i); p.Err == nil {
			t.Errorf("%s = %+v, want an Err", p.Name, p)
		}
	}
}

// doc.go, Values: "A parameter that would take the request target or a header
// field past 1 MiB ... is refused at its key": a styled value whose
// percent-encoding passes the bound, a content parameter whose
// encoded value passes it, and one whose percent-encoding does.
func TestParameterLengthBound(t *testing.T) {
	w, c := contractLineClient(t)
	wide := strings.Repeat("é", 200000) // 400,000 bytes, 1,200,000 percent-encoded
	for _, tt := range []struct {
		name string
		v    any
	}{{"sq", wide}, {"jq", strings.Repeat("a", 1<<20)}, {"jq", wide}} {
		refusedAt(t, w, c, "params", &openapi.Input{Params: map[string]any{tt.name: tt.v}}, false, tt.name)
	}
}

// doc.go, Fixed rules, Styles: a style OpenAPI does not define for its
// location, such as spaceDelimited exploded, is refused: "Each is refused at
// the parameter's key, with Param.Err set where the document alone decides
// it", and "the refusals here apply to defined values". doc.go, Values: "An
// array whose items are all undefined is itself defined, as RFC 6570 section
// 2.3 says, and is refused where [""] would be". So a call that gives the
// parameter [nil], as one that gives it ["a"], is refused at its key.
func TestUndefinedStyleRefusesUndefinedItems(t *testing.T) {
	w, c := contractLineClient(t)
	if p := param(t, mustOp(t, c, "params"), 3); p.Err == nil {
		t.Fatalf("dq = %+v, want an Err", p)
	}
	for _, v := range []any{[]any{nil}, []string{"a"}} {
		refusedAt(t, w, c, "params", &openapi.Input{Params: map[string]any{"dq": v}}, false, "dq")
	}
}

// client.go, Input.ParamWriters: a writer "must keep the URL valid and
// return an error on failure; that error is reported at
// RequestError.Inputs[key]"; a nil writer cannot write and is refused at
// its key. A writer that leaves an unclosed "{" in the path
// does not stall preparation.
func TestParamWriterNilAndUnclosedBrace(t *testing.T) {
	w, c := contractLineClient(t)
	refusedAt(t, w, c, "path", &openapi.Input{ParamWriters: map[string]func(*http.Request) error{"id": nil}}, false, "id")
	promptly(t, 10*time.Second, "Prepare", func() {
		c.Prepare("path", &openapi.Input{ParamWriters: map[string]func(*http.Request) error{"id": func(r *http.Request) error {
			r.URL.Path = strings.Replace(r.URL.Path, "{id}", "{x", 1)
			r.URL.RawPath = strings.Replace(r.URL.RawPath, "{id}", "{x", 1)
			return nil
		}}})
	})
}

// errors.go, RequestError.Err: "the error of HTTP.GetBody when a later send
// of a prepared request takes its body from it"; and a
// body the transport takes again for a 307 hop from a GetBody that fails
// ends the call with that error. The caller replaces the
// prepared body with its own, and its own GetBody.
func TestCallerGetBodyFailure(t *testing.T) {
	errGet := errors.New("the body is gone")
	answer := routes(map[string]http.HandlerFunc{"/again": redirect(307, "/done")})
	w := newWire(t, answer)
	c := parseFor(t, w, doc31(`"/again":{"put":{"operationId":"again","requestBody":{"content":{"application/json":{}}}}}`),
		&openapi.Options{Redirects: openapi.FollowAll})
	in := &openapi.Input{Body: map[string]int{"a": 1}}
	req := mustPrepare(t, c, "again", in)
	req.HTTP.Body = io.NopCloser(strings.NewReader(`{"a":2}`))
	req.HTTP.GetBody = func() (io.ReadCloser, error) { return nil, errGet }
	_, err := req.Send(t.Context())
	if !errors.Is(err, errGet) {
		t.Errorf("Send with a 307 = %v, want the GetBody error", err)
	}
	_, err = req.Send(t.Context())
	if !errors.Is(err, errGet) || !isRequestError(err) {
		t.Errorf("a second Send = %v, want a *RequestError wrapping the GetBody error", err)
	}
	// A transport that takes the body again, as net/http's does to retry
	// on a new connection, gets the GetBody error.
	retry := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if _, err := r.GetBody(); err != nil {
			return nil, err
		}
		return memResponse(r, 200, nil, ""), nil
	})
	c = parseFor(t, w, doc31(`"/again":{"put":{"operationId":"again","requestBody":{"content":{"application/json":{}}}}}`),
		&openapi.Options{HTTPClient: &http.Client{Transport: retry}})
	req = mustPrepare(t, c, "again", in)
	req.HTTP.Body = io.NopCloser(strings.NewReader(`{"a":2}`))
	req.HTTP.GetBody = func() (io.ReadCloser, error) { return nil, errGet }
	if _, err := req.Send(t.Context()); !errors.Is(err, errGet) {
		t.Errorf("Send through a transport that takes GetBody = %v, want the GetBody error", err)
	}
}

// client.go, Response.Decode: it "reads r's open Body into out, using Call's
// target, codec, empty-body and MaxBodyBytes rules for any HTTP status", for
// a Response the client did not make too; and "A missing,
// repeated or unparsable Content-Type is treated as
// application/octet-stream, which a *any receives as a []byte".
func TestDecodeForeignResponse(t *testing.T) {
	foreign := func(ct, body string) *openapi.Response {
		return &openapi.Response{Response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}},
			Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}}
	}
	var m map[string]any
	if err := foreign("application/json", `{"a":1}`).Decode(&m); err != nil || fmt.Sprint(m) != "map[a:1]" {
		t.Errorf("Decode = %v, %v", m, err)
	}
	var v any
	if err := foreign("not a type", "raw").Decode(&v); err != nil || !bytes.Equal(v.([]byte), []byte("raw")) {
		t.Errorf("Decode with an unparsable Content-Type = %#v, %v; want the bytes", v, err)
	}
}

// client.go, Call, for an empty body decoded into a pointer: "Otherwise it
// leaves out as it was under a JSON or XML type, or a type with a caller's
// codec, which is not called; under any other type, it decodes as "" into a
// *string or *any for a text/* type, as an empty array for a sequential type
// (a *DecodeError for an out that cannot hold one), and as an empty []byte
// into a *any for any other type, leaving any other out as it was": an empty
// body under a type with a caller's codec, an empty JSON Lines body, and an
// empty image into a typed pointer. Call decodes a nonempty sequential body as
// a JSON array of its items (client.go, Call: "a sequential type, as a JSON
// array of its items").
func TestEmptyAndSequentialResponseBodies(t *testing.T) {
	var ct, body string
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) { typedAnswer(200, ct, body)(rw, r) })
	c := parseFor(t, w, doc31(`"/u":{"get":{"operationId":"undeclared"}}`),
		&openapi.Options{Codecs: map[string]openapi.Codec{"application/x-tag": tagCodec{tag: "T"}}})
	ct, body = "application/x-tag", ""
	var v any = "untouched"
	if _, err := c.Call(t.Context(), "undeclared", nil, &v); err != nil || v != "untouched" {
		t.Errorf("an empty body with a codec: %v, %v; want success, out untouched", v, err)
	}
	ct = "application/jsonl"
	var items []any
	if _, err := c.Call(t.Context(), "undeclared", nil, &items); err != nil || items == nil || len(items) != 0 {
		t.Errorf("an empty JSON Lines body: %#v, %v; want an empty array", items, err)
	}
	ct = "image/png"
	n := 7
	if _, err := c.Call(t.Context(), "undeclared", nil, &n); err != nil || n != 7 {
		t.Errorf("an empty image into an *int: %v, %v; want success, out untouched", n, err)
	}
	ct, body = "application/jsonl", "{}\n{}\n"
	_, err := c.Call(t.Context(), "undeclared", nil, &items)
	if err != nil || len(items) != 2 {
		t.Fatalf("a JSON Lines body: %#v, %v; want two empty objects", items, err)
	}
	for i, item := range items {
		if object, ok := item.(map[string]any); !ok || len(object) != 0 {
			t.Errorf("item %d = %#v, want an empty object", i, item)
		}
	}
}

// client.go, Call: JSON types are decoded "with encoding/json, anything but
// whitespace after the value being a failure, as for json.Unmarshal", and a
// body that fails to decode is a *DecodeError, into a *any as into any
// other pointer.
func TestDecodeIntoAnyFailures(t *testing.T) {
	var body string
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) { jsonAnswer(200, body)(rw, r) })
	c := parseFor(t, w, doc31(`"/u":{"get":{"operationId":"undeclared"}}`), nil)
	for _, body = range []string{`{"a":`, `{} x`, `{}{}`} {
		var v any
		_, err := c.Call(t.Context(), "undeclared", nil, &v)
		var de *openapi.DecodeError
		if !errors.As(err, &de) {
			t.Errorf("%q into a *any: %v, want a *DecodeError", body, err)
		}
	}
}

// client.go, Call: XML is read "with encoding/xml, which ... reads UTF-8,
// US-ASCII and ISO-8859-1 documents, taking the encoding from a byte order
// mark, else the Content-Type's charset, else the document's own
// declaration": a declared encoding outside the three is a *DecodeError,
// and a declared UTF-8 or US-ASCII decodes.
func TestXMLEncodingDeclaration(t *testing.T) {
	var body string
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) { typedAnswer(200, "application/xml", body)(rw, r) })
	c := parseFor(t, w, doc31(`"/u":{"get":{"operationId":"undeclared"}}`), nil)
	type doc struct {
		A string `xml:"a"`
	}
	var out doc
	body = `<?xml version="1.0" encoding="UTF-8"?><doc><a>x</a></doc>`
	if _, err := c.Call(t.Context(), "undeclared", nil, &out); err != nil || out.A != "x" {
		t.Errorf("declared UTF-8: %+v, %v", out, err)
	}
	body = `<?xml version="1.0" encoding="US-ASCII"?><doc><a>y</a></doc>`
	if _, err := c.Call(t.Context(), "undeclared", nil, &out); err != nil || out.A != "y" {
		t.Errorf("declared US-ASCII: %+v, %v", out, err)
	}
	body = `<?xml version="1.0" encoding="windows-1252"?><doc><a>x</a></doc>`
	_, err := c.Call(t.Context(), "undeclared", nil, &out)
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Errorf("declared windows-1252: %v, want a *DecodeError", err)
	}
}

// doc.go, Outcomes: "An upload error may be joined with a response error;
// errors.As can find both", as when Response.Decode fails and the upload
// failed too.
func TestDecodeErrorJoinsUploadError(t *testing.T) {
	srv := newBodyServer(t, true, "")
	ctx, _ := gateCtx(t)
	gate := make(chan struct{})
	it := func(yield func(any, error) bool) {
		if !yield(itemJSON(1), nil) {
			return
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return
		}
		yield(nil, errCursor)
	}
	req := mustPrepare(t, seqClient(t, srv), "jsonl", &openapi.Input{Body: iter.Seq2[any, error](it)})
	resp, err := req.Send(ctx)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	close(gate)
	var n int
	err = resp.Decode(&n)
	var de *openapi.DecodeError
	if !errors.As(err, &de) || !errors.Is(err, errCursor) {
		t.Errorf("Decode = %v, want a *DecodeError joined with the iterator's error", err)
	}
}

// Regression check, not contract: the error text names the context's error.
// doc.go, Outcomes promises only the match: "When the call's context is done
// before the call completes, the error matches ctx.Err() with errors.Is".
func TestContextErrorTextNamesCause(t *testing.T) {
	_, c := contractLineClient(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := c.Call(ctx, "params", nil, nil)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("Call = %v, want an error matching and naming context.Canceled", err)
	}
}

// client.go, Input.Body: a *strings.Reader is sent "from the bytes they hold
// when the call is prepared", so an empty one, or one already read to its
// end, is an empty part.
func TestEmptyReplayablePart(t *testing.T) {
	w, c := contractLineClient(t)
	read := strings.NewReader("x")
	read.ReadByte()
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{"j": strings.NewReader(""), "s": read})
	checkParts(t, parts, []wantPart{
		{disposition: formData("j", "j"), ctype: "application/json", content: ""},
		{disposition: formData("s", "s"), ctype: "text/plain", content: ""},
	})
}

// errors.go, ErrUnresolved: wrapped by "the Err of a part, or of a
// SchemaReference, whose defect is a reference that cannot be resolved", a
// Security Scheme Object's included.
func TestUnresolvableSecurityScheme(t *testing.T) {
	c := parseAt(t, doc31(`"/x":{"get":{"operationId":"x","security":[{"s":[]}]}}`,
		`"components":{"securitySchemes":{"s":{"$ref":"#/components/securitySchemes/nope"}}}`), "https://api.example.test", testDocURI, nil)
	if s := schemeOf(t, mustOp(t, c, "x"), 0, "s"); !errors.Is(s.Err, openapi.ErrUnresolved) {
		t.Errorf("scheme s: Err %v, want ErrUnresolved", s.Err)
	}
}

// client.go, Input.Body: a sequential body is "a list, an iter.Seq, or an
// iter.Seq2 whose second value is an error"; "A nil iterator is refused at
// Inputs["Input.Body"]", the
// fast path's iter.Seq[any] included, and a function of
// another shape is no iterator.
func TestSequentialBodyIteratorShapes(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	for _, body := range []any{iter.Seq[any](nil), func(func(int) int) {}, func(func(any) bool) bool { return true }, func(func(any, int) bool) {}} {
		refusedAt(t, w, c, "jsonl", &openapi.Input{Body: body}, false, "Input.Body")
	}
}

// readAfterClose is a transport that reads one item of the request body,
// closes it, reads again, and answers 200.
type readAfterClose struct {
	again chan error
}

func (rt readAfterClose) RoundTrip(r *http.Request) (*http.Response, error) {
	buf := make([]byte, 64)
	r.Body.Read(buf)
	r.Body.Close()
	_, err := r.Body.Read(buf)
	rt.again <- err
	return memResponse(r, 200, nil, ""), nil
}

// An iterator body ends at the transport's Close, its yield then returning
// false (client.go, Input.Body: "its yield returns false once the body is no
// longer wanted"); a Read after Close is refused with an error.
func TestIteratorBodyReadAfterClose(t *testing.T) {
	rt := readAfterClose{again: make(chan error, 1)}
	c := parseAt(t, seqDoc(), "https://api.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
	stopped := make(chan bool, 1)
	_, err := c.Call(t.Context(), "jsonl", &openapi.Input{Body: endless(t.Context(), stopped)}, nil)
	if err == nil {
		t.Errorf("Call = nil error for a body the transport closed early")
	}
	if again := <-rt.again; again == nil {
		t.Errorf("a Read after Close returned no error")
	}
	awaitStopped(t, stopped, "the iterator")
}

// client.go, Client.Document: "With a JSON Pointer fragment ... it returns a
// copy of only that node, or nil when there is none": a node that ends in
// false, and pointers that name no node: an array index out of
// range, with a leading zero or negative, into a scalar, a fragment that
// does not begin with "/", and an invalid percent-encoding.
func TestDocumentPointerResolution(t *testing.T) {
	many := strings.TrimSuffix(strings.Repeat("7,", 100), ",")
	c := parseAt(t, doc31(`"/x":{"get":{}}`, `"x-e":{"a":[1,2],"f":false}`, `"x-many":[`+many+`]`), "https://api.example.test", testDocURI, nil)
	if got := string(c.Document(testDocURI + "#/x-e")); got != `{"a":[1,2],"f":false}` {
		t.Errorf("Document(#/x-e) = %q", got)
	}
	if got := string(c.Document(testDocURI + "#/x-many/99")); got != "7" {
		t.Errorf("Document(#/x-many/99) = %q", got)
	}
	for _, frag := range []string{"/x-e/a/2", "/x-e/a/01", "/x-e/a/-1", "/x-e/a/x", "/openapi/x", "x-e", "/x-e/%zz", "/x-many/100"} {
		if got := c.Document(testDocURI + "#" + frag); got != nil {
			t.Errorf("Document(#%s) = %q, want nil", frag, got)
		}
	}
}

// load.go, Loader: "invalid UTF-8 rejects the document", and "A rejection
// names the document's URI and where the problem is: for a YAML syntax
// error, the parser's own message, as it gives it; otherwise the line
// and column". Each case is also no YAML (load.go: a document that is not
// JSON "is read as YAML"): invalid UTF-8, a control character, an invalid
// escape in a value and in a member name, a string that does not end, a
// member without its colon, and a minus sign with no digits. Invalid UTF-8
// is rejected before any YAML is read, at its line; every other case is a
// YAML syntax error, whose line is the parser's to give.
func TestLoadRejectsMalformedJSON(t *testing.T) {
	head := "{\n  \"openapi\": \"3.1.0\",\n  \"info\": {\"title\": \"t\", \"version\": \"1\"},\n  \"paths\": {},\n"
	for name, tail := range map[string]string{
		"invalid UTF-8":              "  \"x-a\": \"a\xffb\"\n}",
		"control character":          "  \"x-a\": \"a\x01b\"\n}",
		"invalid escape":             "  \"x-a\": \"a\\qb\"\n}",
		"invalid escape in a name":   "  \"x-\\q\": 1\n}",
		"string that does not end":   "  \"x-a\": \"ab",
		"member without its colon":   "  \"x-a\" 1\n}",
		"escape that does not end":   "  \"x-a\": \"\\u12",
		"name that does not end":     "  \"x-a",
		"name followed by the end":   "  \"x-a\"",
		"value followed by the end":  "  \"x-a\": ",
		"invalid escape after valid": "  \"x-a\": \"\\n\\x\"\n}",
		"a minus sign alone":         "  \"x-a\": -\n}",
	} {
		t.Run(name, func(t *testing.T) {
			c, err := openapi.Parse(t.Context(), []byte(head+tail), testDocURI, nil)
			if err == nil || c != nil {
				t.Fatalf("Parse = %v, %v; want a rejection", c, err)
			}
			if name == "invalid UTF-8" {
				namesURIAndLine(t, err, testDocURI, 5)
			} else {
				wantSyntaxError(t, err, testDocURI)
			}
		})
	}
}

// doc.go, Fixed rules, URL: server variables are substituted, and a result
// that is no usable URL means "the server cannot be used"; with no
// variable given, the defaults alone decide, and the call is refused before
// sending.
func TestServerDefaultsUnusable(t *testing.T) {
	c := parseAt(t, `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://{h}/v1","variables":{"h":{"default":"a b"}}}],"paths":{"/x":{"get":{"operationId":"x"}}}}`,
		"", testDocURI, nil)
	_, err := c.Prepare("x", nil)
	if re := asRequestError(t, err); len(re.Settings) == 0 {
		t.Errorf("Prepare = %v, want a refusal in Settings", err)
	}
}

// cancelThenRead is a transport that ends the call's context, then reads
// the request body, and fails.
type cancelThenRead struct {
	cancel func()
	read   chan error
}

var errGaveUp = errors.New("the transport gave up")

func (rt cancelThenRead) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.cancel()
	_, err := r.Body.Read(make([]byte, 8))
	rt.read <- err
	return nil, errGaveUp
}

// doc.go, Outcomes: "When the call's context is done before the call
// completes, the error matches ctx.Err()", the transport's own error kept;
// and a body read after the context ended reports the
// context's error.
func TestContextEndsDuringUpload(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	rt := cancelThenRead{cancel: cancel, read: make(chan error, 1)}
	c := parseAt(t, doc31(`"/x":{"put":{"operationId":"x","requestBody":{"content":{"application/json":{}}}}}`),
		"https://api.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
	_, err := c.Call(ctx, "x", &openapi.Input{Body: map[string]int{"a": 1}}, nil)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errGaveUp) || !strings.Contains(err.Error(), errGaveUp.Error()) {
		t.Errorf("Call = %v, want an error matching context.Canceled and the transport's", err)
	}
	if read := <-rt.read; !errors.Is(read, context.Canceled) {
		t.Errorf("a body read after the context ended = %v, want context.Canceled", read)
	}
}

// The client applies the cookie jar itself, as net/http's send does
// (Jar.Cookies before sending, Jar.SetCookies after each response): a request
// whose Header the caller set to nil still carries the jar's cookies;
// and, as net/http's redirect does (Go issue 17494), a hop
// drops the request's own cookie pairs that the redirect response set again,
// the jar supplying their new values.
func TestJarCookiesOnSendAndRedirect(t *testing.T) {
	t.Run("nil Header", func(t *testing.T) {
		w := newWire(t, nil)
		jar := newJar(t, w.URL, &http.Cookie{Name: "j", Value: "1"})
		c := parseFor(t, w, doc31(`"/r":{"get":{"operationId":"plain"}}`), &openapi.Options{HTTPClient: &http.Client{Jar: jar}})
		req := mustPrepare(t, c, "plain", nil)
		req.HTTP.Header = nil
		sendAndClose(t, req)
		if got := cookiePairs(t, w.last(t).Header); strings.Join(got, "; ") != "j=1" {
			t.Errorf("Cookie pairs %q, want j=1", got)
		}
	})
	t.Run("a cookie the redirect sets again", func(t *testing.T) {
		w := newWire(t, routes(map[string]http.HandlerFunc{"/r": func(rw http.ResponseWriter, r *http.Request) {
			http.SetCookie(rw, &http.Cookie{Name: "c1", Value: "new", Path: "/"})
			redirect(302, "/r2")(rw, r)
		}}))
		jar := newJar(t, w.URL)
		c := parseFor(t, w, doc31(`"/r":{"get":{"operationId":"cookie","parameters":[{"name":"c1","in":"cookie"}]}}`),
			&openapi.Options{HTTPClient: &http.Client{Jar: jar}, Redirects: openapi.FollowAll})
		mustCall(t, c, "cookie", &openapi.Input{Params: map[string]any{"c1": "old"}}, nil)
		reqs := w.requests()
		if len(reqs) != 2 {
			t.Fatalf("server received %d requests, want 2", len(reqs))
		}
		if got := cookiePairs(t, reqs[1].Header); strings.Join(got, "; ") != "c1=new" {
			t.Errorf("the hop's Cookie pairs %q, want c1=new", got)
		}
	})
}

var errStopHop = errors.New("no further")

// client.go, Redirects: "The HTTPClient's CheckRedirect is still consulted
// on every hop the client follows", and its error ends the call as
// net/http's does, a *url.Error whose Op is the method's ("Get" for a
// request whose Method the caller left empty, which net/http sends as GET).
func TestCheckRedirectErrorOp(t *testing.T) {
	w := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/r2")}))
	c := parseFor(t, w, doc31(`"/r":{"get":{"operationId":"plain"}}`), &openapi.Options{Redirects: openapi.FollowAll,
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errStopHop }}})
	req := mustPrepare(t, c, "plain", nil)
	req.HTTP.Method = ""
	_, err := req.Send(t.Context())
	var ue *url.Error
	if !errors.As(err, &ue) || ue.Op != "Get" || !errors.Is(err, errStopHop) {
		t.Errorf("Send = %v, want a *url.Error with Op Get wrapping CheckRedirect's error", err)
	}
}

// credential.go, SecretFunc: "A nil f, like an empty secret, is no
// credential": Load refuses it as it refuses the zero Credential, at
// Options.Credentials[name], and a Client With derives, which skips that
// check, refuses a call that needs it as it refuses a call with no credential
// at all, nothing sent.
func TestSecretFuncOfNilIsNoCredential(t *testing.T) {
	w := newWire(t, nil)
	err := parseErr(t, credDoc, w.URL, &openapi.Options{Credentials: map[string]openapi.Credential{"bearer": openapi.SecretFunc(nil)}})
	wantKeys(t, "Settings", asRequestError(t, err).Settings, true, credKey("bearer"))
	c := parseFor(t, w, credDoc, nil)
	_, missing := c.Call(t.Context(), "bearer", nil, nil)
	want := asRequestError(t, missing)
	nilFunc := c.With(func(o *openapi.Options) {
		o.Credentials = map[string]openapi.Credential{"bearer": openapi.SecretFunc(nil)}
	})
	resp, err := nilFunc.Call(t.Context(), "bearer", nil, nil)
	re := refusedBeforeSending(t, w, resp, err)
	if got, wantKeys := sortedKeys(re.Settings), sortedKeys(want.Settings); fmt.Sprint(got) != fmt.Sprint(wantKeys) || len(got) == 0 {
		t.Errorf("Settings %q, want %q, as with no credential", got, wantKeys)
	}
}

// client.go, Input.Body: an iterator "runs on a goroutine of the transport,
// from the transport's first Read of the body, so a body closed unread never
// runs it". A prepared
// iterator body that is closed and then read never starts the iterator: the
// read fails. Its GetBody is nil, an iterator being read once.
func TestClosedIteratorBodyNeverStarts(t *testing.T) {
	c := parseAt(t, seqDoc(), "https://api.example.test", testDocURI, nil)
	started := make(chan struct{}, 1)
	it := func(yield func(any) bool) {
		started <- struct{}{}
		yield(1)
	}
	req := mustPrepare(t, c, "jsonl", &openapi.Input{Body: iter.Seq[any](it)})
	if req.HTTP.GetBody != nil {
		t.Errorf("an iterator body has a GetBody")
	}
	if err := req.HTTP.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	n, err := req.HTTP.Body.Read(make([]byte, 64))
	if n != 0 || err == nil {
		t.Errorf("Read after Close = %d, %v; want an error", n, err)
	}
	select {
	case <-started:
		t.Errorf("the iterator started after its body was closed")
	default:
	}
}

// load.go, Load: "Any other defect ... is reported on the part it reaches, in
// its Err, or ignored where nothing depends on it". A tags value that is an
// object instead of an array lists nothing, and so does a server variable's
// enum, which also makes its server unusable (describe.go, Variable.Enum: "An
// enum written as anything but an array of strings, a variable's values being
// strings, makes the server unusable (Server.Err)" and "Enum is nil for
// either"; see TestServerVariableEnumShapes); a security requirement whose
// scopes are an object is a defect of each operation it reaches (Operation.Err,
// with no alternative listed), and Load does not take an Options.SecurityKey
// naming the scopes that object holds as an alternative (client.go,
// Options.SecurityKey: "A key that names no alternative ... is refused by
// Load").
func TestObjectsWhereArraysBelong(t *testing.T) {
	doc := func(scopes string) string {
		return `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"https://{h}.example.test","variables":{"h":{"default":"a","enum":{"x":"a","y":"b"}}}}],
		"paths":{"/x":{"get":{"operationId":"x","tags":{"t":"pets"},"security":[{"oauth":` + scopes + `}]}}},
		"components":{"securitySchemes":{"oauth":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{"read":""}}}}}}}`
	}
	c := parseAt(t, doc(`{"s":"read"}`), "", testDocURI, nil)
	op := mustOp(t, c, "x")
	if len(op.Tags) != 0 {
		t.Errorf("Tags %q, want none", op.Tags)
	}
	if enum := server(t, op, 0).Variables[0].Enum; enum != nil {
		t.Errorf("Enum %q, want nil", enum)
	}
	if op.Err == nil || len(op.Security) != 0 {
		t.Errorf("Err %v, Security %+v; want an Err and no alternative", op.Err, op.Security)
	}
	key := mustOp(t, parseAt(t, doc(`["read"]`), "", testDocURI, nil), "x").Security[0].Key
	err := parseErr(t, doc(`{"s":"read"}`), "https://api.example.test", &openapi.Options{SecurityKey: key})
	wantKeys(t, "Settings", asRequestError(t, err).Settings, false, "Options.SecurityKey")
}
