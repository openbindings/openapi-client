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

// Stage 4 ledger, "Fix round (23cfd73)", IP4F-8: "the test author covers the
// listed F35 lines or the implementer removes what no contract needs". The
// tests here cover the production blocks that no test reached at b4872f9
// (go test -coverprofile, child processes included) where a contract line
// or ruling needs their behavior, each citing it; the blocks no contract
// needs are listed for the implementer in the test author's report. Each
// pins a behavior that holds at b4872f9 unless it says otherwise.

const ip4f8Paths = `
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

func ip4f8Client(t *testing.T) (*wire, *openapi.Client) {
	t.Helper()
	w := newWire(t, nil)
	return w, parseFor(t, w, doc31(ip4f8Paths), nil)
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

// Stage brief, Refusals: "a property or part value its media type cannot
// encode ... each at its Inputs key"; doc.go, Values: "A body under a form,
// multipart or sequential type takes the shapes Input.Body lists". A form
// field typed JSON Lines or multipart, and a multipart part typed NDJSON,
// cannot encode an object (body.go:57 at b4872f9).
func TestIP4F8FieldTypesThatCannotEncodeAValue(t *testing.T) {
	w, c := ip4f8Client(t)
	obj := map[string]any{"a": 1}
	refusedAt(t, w, c, "form", &openapi.Input{Body: map[string]any{"l": obj}}, false, "Input.Body/l")
	refusedAt(t, w, c, "form", &openapi.Input{Body: map[string]any{"m": obj}}, false, "Input.Body/m")
	refusedAt(t, w, c, "mp", &openapi.Input{Body: map[string]any{"l": obj}}, false, "Input.Body/l")
}

// A form field's reader read once is streamed as the body is read (stage 4
// ledger, Q10), and its read error aborts the body and is reported by Call
// (client.go, Input.Body: an error "aborts the body and is reported by Call
// or Response.WaitRequest"; body.go:195). A replayable reader is read when
// the call is prepared, and its read error refuses the call, kept for
// errors.As (body.go:317): an *os.File opened only for writing.
func TestIP4F8FormFieldReadErrors(t *testing.T) {
	w, c := ip4f8Client(t)
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
// 5.1.1)"; stage 4 ledger, Q13 and QQ3: content "streamed from a reader ...
// ends the body as an upload error". A reader that yields one byte at a time
// splits the delimiter across reads, which the check still finds
// (body.go:218); the same reader without it is sent whole.
func TestIP4F8DelimiterAcrossShortReads(t *testing.T) {
	w, c := ip4f8Client(t)
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
// parameter holding a reader (body.go:264, 364).
func TestIP4F8FormContentParameterRefusesReaders(t *testing.T) {
	w, c := ip4f8Client(t)
	refusedAt(t, w, c, "params", &openapi.Input{Params: map[string]any{"fq": map[string]any{"a": strings.NewReader("x")}}}, false, "fq")
}

// A form field typed application/x-www-form-urlencoded is encoded by the
// client's form encoder from its schema's fields (stage 4 ledger, F7 and
// F20: "A form-typed part or content parameter is encoded by the client's
// form encoder everywhere"), and then as the value of its field (doc.go,
// Fixed rules, Form bodies; body.go:272).
func TestIP4F8FormTypedFieldInAFormBody(t *testing.T) {
	w, c := ip4f8Client(t)
	mustCall(t, c, "form", &openapi.Input{Body: map[string]any{"t": map[string]any{"a": "b c", "n": 5}}}, nil)
	if got, want := string(w.last(t).Body), formPairs("t", "a=b+c&n=5"); got != want {
		t.Errorf("form body %q, want %q", got, want)
	}
}

// nullJSON is written by its MarshalJSON as null.
type nullJSON struct{}

func (nullJSON) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// doc.go, Values: "A form or multipart property or array item whose JSON
// data is null is omitted, whatever its serialization": JSON data null from
// a json.RawMessage under a JSON field, and from a MarshalJSON under a text
// field (body.go:285, 492).
func TestIP4F8NullJSONDataOmitted(t *testing.T) {
	w, c := ip4f8Client(t)
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
// of the encapsulated parts"; stage 4 ledger, IP4-4 (a nested boundary never
// starts with an enclosing one) and C4-3. A nested multipart part whose
// given boundary begins with the outer one is refused at its key, whether
// it has parts (each refused too) or only its close delimiter
// (body.go:442).
func TestIP4F8NestedBoundaryHoldingTheOuter(t *testing.T) {
	w, c := ip4f8Client(t)
	for _, content := range []map[string]any{{}, {"a": "x"}} {
		before := w.count()
		resp, err := c.Call(t.Context(), "mp", &openapi.Input{MediaType: "multipart/form-data; boundary=B", Body: map[string]any{
			"n": openapi.Part{Content: content, MediaType: "multipart/mixed; boundary=Bx"},
		}}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Inputs", re.Inputs, false, "Input.Body/n") // and each nested part's key
	}
}

// Input.MediaType: a boundary is "checked", and "Two boundary parameters are
// refused"; a part's media type problem is at its key in Settings (stage 4
// ledger, test round: "part media-type problems in Settings"). A nested
// multipart Part.MediaType with an invalid boundary, or two, is refused
// (body.go:503).
func TestIP4F8PartBoundaryRefused(t *testing.T) {
	w, c := ip4f8Client(t)
	for _, mt := range []string{`multipart/mixed; boundary="a "`, "multipart/mixed; boundary=a; boundary=b", `multipart/mixed; boundary="a@b"`} {
		refusedAt(t, w, c, "mp", &openapi.Input{Body: map[string]any{
			"n": openapi.Part{Content: map[string]any{"a": "x"}, MediaType: mt},
		}}, true, "Input.Body/n")
	}
}

// Input.MediaType's boundary is checked against RFC 2046 section 5.1.1:
// "bcharsnospace := DIGIT / ALPHA / "'" / "(" / ")" / "+" / "_" / "," / "-" /
// "." / "/" / ":" / "=" / "?"" and the space, so "@", "~", "*" and a byte
// past ASCII are refused at Input.MediaType (body.go:608), and every bchar
// is taken.
func TestIP4F8BoundaryCharacters(t *testing.T) {
	w, c := ip4f8Client(t)
	for _, b := range []string{`"a@b"`, `"a~b"`, `"a*b"`, "\"a\xc3\xa9b\""} {
		refusedAt(t, w, c, "mp", &openapi.Input{MediaType: "multipart/form-data; boundary=" + b, Body: map[string]any{"s": "x"}}, true, "Input.MediaType")
	}
	b := `"0aZ'()+_,-./:=? x"`
	mustCall(t, c, "mp", &openapi.Input{MediaType: "multipart/form-data; boundary=" + b, Body: map[string]any{"s": "x"}}, nil)
	got := w.last(t)
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	checkParts(t, parts, []wantPart{{disposition: formData("s"), ctype: "text/plain", content: "x"}})
}

// C4-2: "an array's default follows its items the same way", and "a schema
// without type allows all", so an array whose items are absent has the
// absent type, application/octet-stream (OAS 3.1.2 section 4.8.15.1.1;
// fields.go:261), and each item is sent so.
func TestIP4F8ArrayWithoutItems(t *testing.T) {
	w, c := ip4f8Client(t)
	if e := encodingByName(t, reqMedia(t, mustOp(t, c, "mp"), 0))["arr"]; e == nil || e.ContentType != "application/octet-stream" {
		t.Errorf("arr = %+v, want ContentType application/octet-stream", e)
	}
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{"arr": []string{"x", "y"}})
	checkParts(t, parts, []wantPart{
		{disposition: formData("arr"), ctype: "application/octet-stream", content: "x"},
		{disposition: formData("arr"), ctype: "application/octet-stream", content: "y"},
	})
}

// client.go, Input.Body: "For form and multipart media, Body is an object (a
// map or a struct)"; C4-1: a typed nil body "is encoded as null or refused";
// null is no object. A typed nil pointer to a struct, a pointer to a *Part
// and a pointer to a reader are refused at Input.Body (fields.go:513, 518).
func TestIP4F8FormBodyThatIsNoObject(t *testing.T) {
	w, c := ip4f8Client(t)
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
// ErrNoOperation", and so does a Client With derives from it (load.go:203,
// 217, 245; client.go:233).
func TestIP4F8ZeroClient(t *testing.T) {
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
// anything else ... is refused before sending" (client.go:657).
func TestIP4F8RequestCallChecksOut(t *testing.T) {
	w, c := ip4f8Client(t)
	req := mustPrepare(t, c, "params", nil)
	for _, out := range []any{5, (*int)(nil)} {
		resp, err := req.Call(t.Context(), out)
		refusedBeforeSending(t, w, resp, err)
	}
}

// client.go, Response.WaitRequest: "It returns nil when no request carried a
// body", as for a Response the client did not make (client.go:757,
// response.go:73).
func TestIP4F8WaitRequestWithoutARequest(t *testing.T) {
	for _, r := range []*openapi.Response{{}, {Response: &http.Response{StatusCode: 200}}} {
		if err := r.WaitRequest(t.Context()); err != nil {
			t.Errorf("WaitRequest = %v, want nil", err)
		}
	}
}

// The error types' Error methods describe a value with no Response, as a
// caller may build one, without failing (errors.go:225).
func TestIP4F8ErrorsWithoutAResponse(t *testing.T) {
	for _, err := range []error{&openapi.StatusError{}, &openapi.DecodeError{}} {
		var msg string
		noPanic(t, fmt.Sprintf("%T.Error", err), func() { msg = err.Error() })
		if !strings.HasPrefix(msg, "openapi: ") {
			t.Errorf("%T.Error() = %q", err, msg)
		}
	}
}

// errors.go, RequestError.Settings: "Each Options field ... the document
// cannot use, or that conflicts with another, is keyed by its field, at Load
// or at a call": Options.Redirects outside its two values (client.go,
// Redirects; config.go:66), and an Options.MediaType that is no concrete
// media type (client.go, Options.MediaType: "selects a concrete request media
// type"; load.go:277), refuse Load.
func TestIP4F8OptionsLoadRefuses(t *testing.T) {
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
// it was finally retrieved from ...; an empty final means uri"
// (index.go:205); a URI scheme other than http, https and file, with no
// Fetch, is refused (index.go:233).
func TestIP4F8LoaderFetch(t *testing.T) {
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
// document is meant to live at": a relative or unparsable one is refused
// (index.go:326, 328).
func TestIP4F8ParseURIMustBeAbsolute(t *testing.T) {
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

// T1-12 (stage 1 ledger: "ctx bounds the whole load"): a context that ends
// at any point of a load ends it with an error matching context.Canceled,
// while the document is read (every 65,535 nodes, tree.go:395), while its
// operations are indexed (index.go:361, 421) and while Load checks the
// Options' names (load.go:283, index.go:726); the load either completes or
// fails so, at every point.
func TestIP4F8LoadEndsWithItsContext(t *testing.T) {
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
// errors.go, ErrUnresolved "is wrapped by the Err of a part whose defect is
// a reference that cannot be resolved": a $ref that is no URI reference
// (index.go:666), a fragment that is no JSON Pointer or whose
// percent-encoding is invalid (index.go:680) cannot be resolved; one that
// names the document by its own URI resolves (load.go: "A reference resolves
// first to what loaded documents identify: a document by its retrieval URI";
// index.go:674).
func TestIP4F8ReferenceForms(t *testing.T) {
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
// every operation that uses the parameters or servers" (compile.go:193); a
// path template with an unclosed "{" is a defect of its operation
// (compile.go:516, 776); a Paths key that does not begin with "/" is an
// entry listed only to report it (describe.go, Operation.Key), which does
// not upset Load's check of Options.MediaType (index.go:729).
func TestIP4F8PathItemDefects(t *testing.T) {
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
// allowEmptyValue (compile.go:297); a location other than path, query,
// header and cookie, and a content key that is no media type, are the
// parameter's Err (compile.go:324, 328).
func TestIP4F8ParameterDeclarations(t *testing.T) {
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

// doc.go, Values: "A parameter that would take the request target or header
// field past 1 MiB ... is refused at its key": a styled value whose
// percent-encoding passes the bound (param.go:58), a content parameter
// whose encoded value passes it (param.go:78), and one whose
// percent-encoding does (param.go:95).
func TestIP4F8ParameterLengthBound(t *testing.T) {
	w, c := ip4f8Client(t)
	wide := strings.Repeat("é", 200000) // 400,000 bytes, 1,200,000 percent-encoded
	for _, tt := range []struct {
		name string
		v    any
	}{{"sq", wide}, {"jq", strings.Repeat("a", 1<<20)}, {"jq", wide}} {
		refusedAt(t, w, c, "params", &openapi.Input{Params: map[string]any{tt.name: tt.v}}, false, tt.name)
	}
}

// Stage 2: a style OpenAPI does not define for its location, such as
// spaceDelimited exploded, is the parameter's Err, and a call that gives the
// parameter is refused at its key, its items defined or not (param.go:301).
func TestIP4F8UndefinedStyleRefusedForUndefinedItems(t *testing.T) {
	w, c := ip4f8Client(t)
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
// its key (request.go:198). A writer that leaves an unclosed "{" in the path
// does not stall preparation (param.go:422).
func TestIP4F8ParamWriterEdges(t *testing.T) {
	w, c := ip4f8Client(t)
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
// of a prepared request takes its body from it" (response.go:289); and a
// body the transport takes again for a 307 hop from a GetBody that fails
// ends the call with that error (response.go:216). The caller replaces the
// prepared body with its own, and its own GetBody.
func TestIP4F8CallerGetBodyFails(t *testing.T) {
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
	// on a new connection, gets the GetBody error (response.go:216).
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
// a Response the client did not make too (response.go:375); and "A missing,
// repeated or unparsable Content-Type is treated as
// application/octet-stream, which a *any receives as a []byte"
// (response.go:431).
func TestIP4F8DecodeAForeignResponse(t *testing.T) {
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

// client.go, Call: "An empty body is a success for every out, except that a
// JSON or XML type decoded into a pointer is a *DecodeError ... An empty
// text body decodes as "", an empty sequential body as an empty array, and
// any other empty body into a *any as an empty []byte": an empty body under
// a type with a caller's codec (response.go:481), an empty JSON Lines body
// (response.go:511), and an empty image into a typed pointer
// (response.go:515). A JSON Lines body that is not empty is decoded by
// Items, which stage 7 adds; until then decoding it into out is a
// *DecodeError wrapping errors.ErrUnsupported (request.go:24,
// response.go:513).
func TestIP4F8EmptyAndSequentialResponseBodies(t *testing.T) {
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
	var de *openapi.DecodeError
	if !errors.As(err, &de) || !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("a JSON Lines body: %v; want a *DecodeError wrapping errors.ErrUnsupported until stage 7", err)
	}
}

// client.go, Call: JSON types are decoded "with encoding/json, anything but
// whitespace after the value being a failure, as for json.Unmarshal", and a
// body that fails to decode is a *DecodeError, into a *any as into any
// other pointer (response.go:531, 534).
func TestIP4F8DecodeIntoAnyFailures(t *testing.T) {
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
// declaration": a declared encoding outside the three is a *DecodeError
// (response.go:565), and a declared UTF-8 or US-ASCII decodes
// (response.go:571).
func TestIP4F8XMLEncodingDeclaration(t *testing.T) {
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
// failed too (response.go:403).
func TestIP4F8DecodeErrorJoinsUploadError(t *testing.T) {
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

// doc.go, Outcomes: "When the call's context is done before the call
// completes, the error matches ctx.Err()", and its text says so
// (response.go:660).
func TestIP4F8ContextErrorText(t *testing.T) {
	_, c := ip4f8Client(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := c.Call(ctx, "params", nil, nil)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("Call = %v, want an error matching and naming context.Canceled", err)
	}
}

// client.go, Input.Body: a *strings.Reader is sent "from the bytes they hold
// when the call is prepared", so an empty one, or one already read to its
// end, is an empty part (response.go:695).
func TestIP4F8EmptyReplayablePart(t *testing.T) {
	w, c := ip4f8Client(t)
	read := strings.NewReader("x")
	read.ReadByte()
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{"j": strings.NewReader(""), "s": read})
	checkParts(t, parts, []wantPart{
		{disposition: formData("j", "j"), ctype: "application/json", content: ""},
		{disposition: formData("s", "s"), ctype: "text/plain", content: ""},
	})
}

// errors.go, ErrUnresolved: wrapped by "the Err of a part whose defect is a
// reference that cannot be resolved", a Security Scheme Object's included
// (security.go:55).
func TestIP4F8UnresolvableSecurityScheme(t *testing.T) {
	c := parseAt(t, doc31(`"/x":{"get":{"operationId":"x","security":[{"s":[]}]}}`,
		`"components":{"securitySchemes":{"s":{"$ref":"#/components/securitySchemes/nope"}}}`), "https://api.example.test", testDocURI, nil)
	if s := schemeOf(t, mustOp(t, c, "x"), 0, "s"); !errors.Is(s.Err, openapi.ErrUnresolved) {
		t.Errorf("scheme s: Err %v, want ErrUnresolved", s.Err)
	}
}

// client.go, Input.Body: a sequential body is "a slice, an iter.Seq, or an
// iter.Seq2 whose second value is an error"; a nil iterator is refused
// (stage 4 ledger, IP4-6), the fast path's iter.Seq[any] included
// (sequential.go:247), and a function of another shape is no iterator
// (sequential.go:257).
func TestIP4F8IteratorShapes(t *testing.T) {
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

// C3-2 and C4-5 (stage 3 and 4 ledgers): an iterator body ends at the
// transport's Close, its yield then returning false (client.go, Input.Body:
// "its yield returns false once the body is no longer wanted"); a Read after
// Close is refused with an error (sequential.go:291).
func TestIP4F8IteratorReadAfterClose(t *testing.T) {
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
// false (tree.go:100), and pointers that name no node: an array index out of
// range, with a leading zero or negative, into a scalar, a fragment that
// does not begin with "/", and an invalid percent-encoding (tree.go:265,
// 302, 319, 323; load.go:255).
func TestIP4F8DocumentPointers(t *testing.T) {
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
// names the document's URI and the line and column of the problem"; the
// stage 1 brief: "a document that is not valid JSON is a refusal". Each case
// is also no YAML (load.go: a document that is not JSON "is read as YAML"):
// invalid UTF-8 (tree.go:354), a control character, an invalid escape in a
// value and in a member name, a string that does not end, a member without
// its colon, and a minus sign with no digits (tree.go:439, 457, 495, 503,
// 508, 527).
func TestIP4F8LoadRejectsMalformedJSON(t *testing.T) {
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
			namesURIAndLine(t, err, testDocURI, 5)
		})
	}
}

// doc.go, Fixed rules, URL: server variables are substituted, and a result
// that is no usable URL means "the server cannot be used"; with no
// variable given, the defaults alone decide, and the call is refused before
// sending (request.go:499).
func TestIP4F8ServerDefaultsUnusable(t *testing.T) {
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
// completes, the error matches ctx.Err()", the transport's own error kept
// (response.go:660); and a body read after the context ended reports the
// context's error (stage 3 ledger, C3-2; response.go:731).
func TestIP4F8ContextEndsDuringTheUpload(t *testing.T) {
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

// Stage 3 ledger, C3-3: "The client applies the cookie jar itself, as
// net/http's send does": a request whose Header the caller set to nil still
// carries the jar's cookies (security.go:561); and, as net/http's redirect
// does (Go issue 17494), a hop drops the request's own cookie pairs that
// the redirect response set again, the jar supplying their new values
// (redirect.go:167).
func TestIP4F8JarCookies(t *testing.T) {
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
// request whose Method the caller left empty, which net/http sends as GET;
// redirect.go:261).
func TestIP4F8CheckRedirectErrorOp(t *testing.T) {
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
