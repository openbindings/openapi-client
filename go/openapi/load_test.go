package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Loading: Load, Parse, Loader, Version, Document and DocumentURIs.

func readPets(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/pets.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameFile reports whether the file URI names path, resolving symlinks on
// both sides (the temporary directory is often reached through one).
func sameFile(t testing.TB, uri, path string) bool {
	t.Helper()
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		t.Errorf("document URI %q is not a file URI", uri)
		return false
	}
	got, err1 := filepath.EvalSymlinks(filepath.FromSlash(u.Path))
	want, err2 := filepath.EvalSymlinks(path)
	if err1 != nil || err2 != nil {
		t.Errorf("resolving %q and %q: %v, %v", u.Path, path, err1, err2)
		return false
	}
	return got == want
}

// compact returns b without insignificant whitespace, failing on invalid
// JSON. Compact keeps key order, string escapes and number spellings.
func compact(t testing.TB, b []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatalf("invalid JSON %q: %v", b, err)
	}
	return buf.String()
}

// load.go, Load: "The uri is an http or https URL, a file URL, or a file
// path." A file path loads, and its document is named by an absolute file
// URI, which is what Sources name (describe.go, Operation.Source).
func TestLoadFilePath(t *testing.T) {
	c, err := openapi.Load(t.Context(), "testdata/pets.json", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	abs, _ := filepath.Abs("testdata/pets.json")
	uris := c.DocumentURIs()
	if len(uris) != 1 {
		t.Fatalf("DocumentURIs = %q, want one", uris)
	}
	if !sameFile(t, uris[0], abs) {
		t.Errorf("DocumentURIs()[0] = %q, want a file URI for %s", uris[0], abs)
	}
	op := mustOp(t, c, "getPet")
	if want := uris[0] + "#/paths/~1pets~1%7BpetId%7D/get"; op.Source != want {
		t.Errorf("Source = %q, want %q", op.Source, want)
	}
	if got, want := compact(t, c.Document("")), compact(t, readPets(t)); got != want {
		t.Errorf("Document(\"\") differs from the file")
	}
}

// load.go, Load: a file URL loads the same document.
func TestLoadFileURL(t *testing.T) {
	abs, _ := filepath.Abs("testdata/pets.json")
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	c, err := openapi.Load(t.Context(), u.String(), nil)
	if err != nil {
		t.Fatalf("Load(%q): %v", u.String(), err)
	}
	if uris := c.DocumentURIs(); len(uris) != 1 || !sameFile(t, uris[0], abs) {
		t.Errorf("DocumentURIs = %q, want one file URI for %s", uris, abs)
	}
	if len(c.Operations()) != 5 {
		t.Errorf("len(Operations()) = %d, want 5", len(c.Operations()))
	}
}

// load.go, Load: an http URL loads; the document is named by the URI it was
// retrieved from (Client.DocumentURIs).
func TestLoadHTTP(t *testing.T) {
	pets := readPets(t)
	w := newWire(t, typedAnswer(200, "application/json", string(pets)))
	uri := w.URL + "/openapi.json"
	c, err := openapi.Load(t.Context(), uri, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.DocumentURIs(); len(got) != 1 || got[0] != uri {
		t.Errorf("DocumentURIs = %q, want [%q]", got, uri)
	}
	if got := w.only(t); got.Method != "GET" || got.Path != "/openapi.json" {
		t.Errorf("fetched %s %s, want GET /openapi.json", got.Method, got.Path)
	}
	if op := mustOp(t, c, "listPets"); op.Source != uri+"#/paths/~1pets/get" {
		t.Errorf("Source = %q", op.Source)
	}
}

// client.go, Options.HTTPClient: "HTTPClient sends every request, and
// fetches documents while loading." client.go, OperationFromContext: nil
// for a request that is not an operation's.
func TestLoadFetchesWithOptionsHTTPClient(t *testing.T) {
	w := newWire(t, typedAnswer(200, "application/json", string(readPets(t))))
	ct := &countingTransport{}
	_, err := openapi.Load(t.Context(), w.URL+"/openapi.json", &openapi.Options{HTTPClient: &http.Client{Transport: ct}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ct.count() != 1 {
		t.Fatalf("Options.HTTPClient carried %d requests, want 1", ct.count())
	}
	if ct.snapshot()[0] != nil {
		t.Errorf("OperationFromContext reported an operation for the document fetch")
	}
}

// load.go, Load: "Load fails when the document is unusable as a whole: it
// cannot be retrieved".
func TestLoadRetrievalFailure(t *testing.T) {
	w := newWire(t, typedAnswer(404, "text/plain", "not found"))
	c, err := openapi.Load(t.Context(), w.URL+"/openapi.json", nil)
	if err == nil || c != nil {
		t.Fatalf("Load of a 404 = %v, %v; want an error and no Client", c, err)
	}
	c, err = openapi.Load(t.Context(), filepath.Join(t.TempDir(), "missing.json"), nil)
	if err == nil || c != nil {
		t.Fatalf("Load of a missing file = %v, %v; want an error and no Client", c, err)
	}
}

// load.go, Parse: "The uri, if not empty, is the absolute URI the document
// is meant to live at, which stands for the URI it was retrieved from and is
// never fetched itself."
func TestParseWithURI(t *testing.T) {
	ct := &countingTransport{}
	uri := "https://pets.example.test/specs/openapi.json"
	c, err := openapi.Parse(t.Context(), readPets(t), uri, &openapi.Options{HTTPClient: &http.Client{Transport: ct}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ct.count() != 0 {
		t.Errorf("Parse fetched %d documents, want none", ct.count())
	}
	if got := c.DocumentURIs(); len(got) != 1 || got[0] != uri {
		t.Errorf("DocumentURIs = %q, want [%q]", got, uri)
	}
	if op := mustOp(t, c, "createPet"); op.Source != uri+"#/paths/~1pets/post" {
		t.Errorf("Source = %q", op.Source)
	}
	if !bytes.Equal(c.Document(uri), c.Document("")) {
		t.Errorf("Document(uri) differs from Document(\"\")")
	}
}

var uuidURN = regexp.MustCompile(`^urn:uuid:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-5[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// load.go, Parse: "With an empty uri ... Sources name the document by a
// "urn:uuid:" URI derived from the content (a name-based UUID, RFC 9562
// version 5), so Sources and $defs keys are the same on every run."
func TestParseEmptyURI(t *testing.T) {
	pets := readPets(t)
	c1, err := openapi.Parse(t.Context(), pets, "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c2, err := openapi.Parse(t.Context(), pets, "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u1, u2 := c1.DocumentURIs(), c2.DocumentURIs()
	if len(u1) != 1 || !uuidURN.MatchString(u1[0]) {
		t.Fatalf("DocumentURIs = %q, want one urn:uuid: URI of a version 5 UUID", u1)
	}
	if len(u2) != 1 || u1[0] != u2[0] {
		t.Errorf("the same content gave %q and %q", u1, u2)
	}
	if op := mustOp(t, c1, "getPet"); op.Source != u1[0]+"#/paths/~1pets~1%7BpetId%7D/get" {
		t.Errorf("Source = %q, want it under %q", op.Source, u1[0])
	}
	if c1.Document(u1[0]) == nil {
		t.Errorf("Document(%q) = nil", u1[0])
	}

	other := bytes.Replace(pets, []byte(`"title": "Pets"`), []byte(`"title": "Other"`), 1)
	c3, err := openapi.Parse(t.Context(), other, "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if u3 := c3.DocumentURIs(); len(u3) != 1 || u3[0] == u1[0] {
		t.Errorf("different content gave the same URI %q", u3)
	}
}

// load.go, Parse: "With an empty uri ... a call whose server URL is relative
// needs Options.BaseURL." doc.go, Configuration: "none requires BaseURL".
func TestParseEmptyURIRelativeServerNeedsBaseURL(t *testing.T) {
	w := newWire(t, jsonAnswer(200, `{"pets":[]}`))
	c, err := openapi.Parse(t.Context(), readPets(t), "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resp, err := c.Call(t.Context(), "listPets", nil, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Settings", re.Settings, false, "Options.BaseURL")

	based := c.With(func(o *openapi.Options) { o.BaseURL = w.URL + "/v9" })
	mustCall(t, based, "listPets", nil, nil)
	if got := w.only(t).RequestURI; got != "/v9/pets" {
		t.Errorf("request target %q, want /v9/pets", got)
	}
}

// doc.go, Fixed rules, URL: a relative server URL resolves only against an
// http or https document URI; "otherwise the server cannot be used". A
// document loaded from a file therefore needs Options.BaseURL.
func TestLoadFileRelativeServerNeedsBaseURL(t *testing.T) {
	w := newWire(t, nil)
	c, err := openapi.Load(t.Context(), "testdata/pets.json", nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resp, err := c.Call(t.Context(), "listPets", nil, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Settings", re.Settings, false, "Options.BaseURL")

	based := c.With(func(o *openapi.Options) { o.BaseURL = w.URL })
	mustCall(t, based, "listPets", nil, nil)
	if got := w.only(t).RequestURI; got != "/pets" {
		t.Errorf("request target %q, want /pets", got)
	}
}

// load.go, Loader: "a UTF-8 byte order mark is ignored".
func TestLoadIgnoresUTF8BOM(t *testing.T) {
	bom := []byte("\xEF\xBB\xBF")
	content := append(append([]byte{}, bom...), readPets(t)...)

	c, err := openapi.Parse(t.Context(), content, testDocURI, nil)
	if err != nil {
		t.Fatalf("Parse with a BOM: %v", err)
	}
	if len(c.Operations()) != 5 {
		t.Errorf("len(Operations()) = %d, want 5", len(c.Operations()))
	}
	// Whether Document keeps the mark is not stated; without it, the copy
	// is the document as written.
	if got, want := compact(t, bytes.TrimPrefix(c.Document(""), bom)), compact(t, readPets(t)); got != want {
		t.Errorf("Document(\"\") differs from the document")
	}

	path := filepath.Join(t.TempDir(), "bom.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openapi.Load(t.Context(), path, nil); err != nil {
		t.Errorf("Load of a file with a BOM: %v", err)
	}
}

// namesURIAndLine checks that err's text names the document URI and the
// line as a whole number. (The column's counting, bytes or characters, is
// not stated; the tests do not check it.)
func namesURIAndLine(t testing.TB, err error, uri string, line int) {
	t.Helper()
	msg := err.Error()
	if !strings.Contains(msg, uri) {
		t.Errorf("error %q does not name the document %q", msg, uri)
	}
	if !regexp.MustCompile(fmt.Sprintf(`(^|[^0-9])%d([^0-9]|$)`, line)).MatchString(msg) {
		t.Errorf("error %q does not name line %d", msg, line)
	}
}

// load.go, Loader: "A duplicate key ... rejects the document ... A rejection
// names the document's URI and the line and column of the problem."
func TestLoadRejectsDuplicateKeys(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		line int
	}{
		// A duplicate root member.
		{"root", `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "paths": {},
  "paths": {}
}`, 5},
		// A duplicate member of an extension, which nothing interprets.
		{"extension", `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "paths": {},
  "x-ext": {
    "a": 1,
    "b": 2,
    "a": 3
  }
}`, 8},
		// A duplicate member of a schema, which the client never interprets.
		{"schema", `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "paths": {},
  "components": {
    "schemas": {
      "Pet": {
        "type": "object",
        "type": "string"
      }
    }
  }
}`, 9},
		// Keys are compared as the strings they spell (RFC 8259 section 4,
		// section 7): "a" is "a".
		{"escaped", `{
  "openapi": "3.1.0",
  "info": {"title": "t", "version": "1"},
  "paths": {},
  "x-ext": {
    "a": 1,
    "a": 2
  }
}`, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := openapi.Parse(t.Context(), []byte(tt.doc), testDocURI, nil)
			if err == nil || c != nil {
				t.Fatalf("Parse = %v, %v; want a rejection", c, err)
			}
			namesURIAndLine(t, err, testDocURI, tt.line)
		})
	}
}

// deepDoc returns a document whose x-deep member nests n arrays, so that the
// document nests n+1 levels, on line 5.
func deepDoc(n int) string {
	return "{\n  \"openapi\": \"3.1.0\",\n  \"info\": {\"title\": \"t\", \"version\": \"1\"},\n  \"paths\": {},\n  \"x-deep\": " +
		strings.Repeat("[", n) + strings.Repeat("]", n) + "\n}"
}

// load.go, Loader: a document "that nests deeper than 1,000 levels, is
// rejected too." Where the count starts (the root object as level 1 or 0)
// is not stated, so the cases keep a margin on each side.
func TestLoadNestingDepth(t *testing.T) {
	if _, err := openapi.Parse(t.Context(), []byte(deepDoc(900)), testDocURI, nil); err != nil {
		t.Errorf("901 levels: %v, want accepted", err)
	}
	c, err := openapi.Parse(t.Context(), []byte(deepDoc(1100)), testDocURI, nil)
	if err == nil || c != nil {
		t.Fatalf("1,101 levels: %v, %v; want a rejection", c, err)
	}
	namesURIAndLine(t, err, testDocURI, 5)
}

// Stage brief, Loading: "a document that is not valid JSON is a refusal".
// load.go, Loader: a document whose first significant byte is '{' and that
// is not JSON is read as YAML, which also rejects these (a second document
// in the stream, or a truncated flow mapping).
func TestLoadRejectsInvalidJSON(t *testing.T) {
	valid := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{}}`
	for name, doc := range map[string]string{
		"truncated":      valid[:len(valid)-1],
		"second value":   valid + valid,
		"trailing token": valid + " ]",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, nil)
			if err == nil || c != nil {
				t.Fatalf("Parse = %v, %v; want an error", c, err)
			}
		})
	}
}

// load.go, Load: Load fails when "its swagger or openapi field is missing or
// not 2.0, 3.0.x, 3.1.x or 3.2.x". Every 3.1 patch loads.
func TestLoadVersionField(t *testing.T) {
	rest := `"info":{"title":"t","version":"1"},"paths":{}}`
	tests := []struct {
		field string // the openapi member, or "" for none
		ok    bool
	}{
		{`"openapi":"3.1.0",`, true},
		{`"openapi":"3.1.1",`, true},
		{`"openapi":"3.1.2",`, true},
		{``, false},
		{`"openapi":3.1,`, false},
		{`"openapi":"",`, false},
		{`"openapi":"4.0.0",`, false},
		// Not a 3.1.x version, despite beginning with "3.1".
		{`"openapi":"3.10.0",`, false},
	}
	for _, tt := range tests {
		c, err := openapi.Parse(t.Context(), []byte("{"+tt.field+rest), testDocURI, nil)
		if tt.ok && err != nil {
			t.Errorf("%s: %v, want loaded", tt.field, err)
		}
		if !tt.ok && (err == nil || c != nil) {
			t.Errorf("%s: loaded, want an error", tt.field)
		}
	}
}

// load.go, Version: "Version reports the version the entry document
// declares".
func TestVersion(t *testing.T) {
	c := parseAt(t, `{"openapi":"3.1.2","info":{"title":"t","version":"9"},"paths":{}}`, "", testDocURI, nil)
	if got := c.Version(); got != "3.1.2" {
		t.Errorf("Version() = %q, want 3.1.2", got)
	}
}

// load.go, Load: a 3.1 document needs "one of paths, components and
// webhooks"; any other missing field, such as info, is ignored where nothing
// depends on it.
func TestLoadRequiredRoots(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		ok   bool
	}{
		{"paths", `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{}}`, true},
		{"components", `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"components":{"schemas":{"A":{}}}}`, true},
		{"webhooks", `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"webhooks":{"ping":{"post":{"responses":{"200":{"description":"ok"}}}}}}`, true},
		{"no info", `{"openapi":"3.1.0","paths":{}}`, true},
		{"none", `{"openapi":"3.1.0","info":{"title":"t","version":"1"}}`, false},
	}
	for _, tt := range tests {
		c, err := openapi.Parse(t.Context(), []byte(tt.doc), testDocURI, nil)
		switch {
		case tt.ok && err != nil:
			t.Errorf("%s: %v, want loaded", tt.name, err)
		case !tt.ok && (err == nil || c != nil):
			t.Errorf("%s: loaded, want an error", tt.name)
		case tt.ok && len(c.Operations()) != 0:
			// load.go/doc.go: webhooks describe requests toward the consumer
			// and are not operations of the Client.
			t.Errorf("%s: %d operations, want none", tt.name, len(c.Operations()))
		}
	}
}

// load.go, Loader.MaxBytes: "MaxBytes bounds the bytes one load retrieves
// ... Past it, an entry document fails the load with an error wrapping
// *http.MaxBytesError ... Content passed to Parse is not counted."
func TestLoaderMaxBytes(t *testing.T) {
	pets := readPets(t)
	n := int64(len(pets))
	w := newWire(t, typedAnswer(200, "application/json", string(pets)))
	uri := w.URL + "/openapi.json"
	path, _ := filepath.Abs("testdata/pets.json")

	for _, src := range []string{uri, path} {
		// Exactly the document's size is not past the bound.
		if _, err := (&openapi.Loader{MaxBytes: n}).Load(t.Context(), src, nil); err != nil {
			t.Errorf("%s with MaxBytes = its size: %v", src, err)
		}
		// One byte less is.
		c, err := (&openapi.Loader{MaxBytes: n - 1}).Load(t.Context(), src, nil)
		var tooBig *http.MaxBytesError
		if err == nil || c != nil || !errors.As(err, &tooBig) {
			t.Errorf("%s with MaxBytes = size-1: %v, want an error wrapping *http.MaxBytesError", src, err)
		}
		// A negative value means no limit.
		if _, err := (&openapi.Loader{MaxBytes: -1}).Load(t.Context(), src, nil); err != nil {
			t.Errorf("%s with MaxBytes = -1: %v", src, err)
		}
	}
	if _, err := (&openapi.Loader{MaxBytes: 1}).Parse(t.Context(), pets, testDocURI, nil); err != nil {
		t.Errorf("Parse counted its content against MaxBytes: %v", err)
	}
}

// load.go, Load: "The Client keeps a copy of opts, its maps included, so
// changing them afterwards has no effect."
func TestLoadKeepsCopyOfOptions(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/pets":{"get":{"operationId":"listPets","responses":{"200":{"description":"ok"}}}}`)
	opts := &openapi.Options{Header: http.Header{"X-Env": {"one"}}}
	c := parseFor(t, w, doc, opts)
	opts.Header.Set("X-Env", "two")
	opts.Header.Set("X-Added", "yes")
	opts.BaseURL = "http://127.0.0.1:1"
	mustCall(t, c, "listPets", nil, nil)
	got := w.only(t)
	if got.Header.Get("X-Env") != "one" || got.Header.Get("X-Added") != "" {
		t.Errorf("sent X-Env %q, X-Added %q; changes after Load took effect", got.Header.Get("X-Env"), got.Header.Get("X-Added"))
	}
}

// load.go, Client.Document: the whole document, one node by a JSON Pointer
// fragment percent-encoded as RFC 6901 section 6 says, nil for no node or
// no document, and a copy each time.
func TestDocument(t *testing.T) {
	uri := "https://pets.example.test/openapi.json"
	c := parseAt(t, string(readPets(t)), "", uri, nil)

	if got, want := compact(t, c.Document("")), compact(t, readPets(t)); got != want {
		t.Errorf("Document(\"\") is not the document")
	}
	tests := []struct {
		fragment string
		want     string // compact JSON, or "" for nil
	}{
		// RFC 6901 section 6: "{" and "}" are percent-encoded in a fragment,
		// "/" in a key is "~1".
		{"#/paths/~1pets~1%7BpetId%7D/parameters/0", `{"$ref":"#/components/parameters/petId"}`},
		// A Reference Object is returned as written, not resolved.
		{"#/paths/~1pets~1%7BpetId%7D/get/responses/404", `{"$ref":"#/components/responses/Problem"}`},
		// An array index.
		{"#/paths/~1pets/get/parameters/1", `{"name":"cursor","in":"query","schema":{"type":"string"}}`},
		// A scalar node.
		{"#/openapi", `"3.1.0"`},
		// The empty pointer is the whole document (RFC 6901 section 5).
		{"#", compact(t, readPets(t))},
		// No such node.
		{"#/paths/~1nowhere", ""},
		{"#/paths/~1pets/get/parameters/9", ""},
	}
	for _, tt := range tests {
		got := c.Document(uri + tt.fragment)
		switch {
		case tt.want == "" && got != nil:
			t.Errorf("Document(%q) = %s, want nil", tt.fragment, got)
		case tt.want != "" && got == nil:
			t.Errorf("Document(%q) = nil, want %s", tt.fragment, tt.want)
		case tt.want != "" && compact(t, got) != tt.want:
			t.Errorf("Document(%q) = %s, want %s", tt.fragment, got, tt.want)
		}
	}
	if got := c.Document("https://other.example.test/openapi.json"); got != nil {
		t.Errorf("Document of a URI never loaded = %q, want nil", got)
	}

	// Each call returns a copy.
	if d := c.Document(""); len(d) > 0 {
		d[0] = 'X'
		if d := c.Document(""); len(d) == 0 || d[0] != '{' {
			t.Errorf("changing a returned document changed the Client's")
		}
	}
	if n := c.Document(uri + "#/openapi"); len(n) > 1 {
		n[1] = 'X'
	}
	if string(c.Document(uri+"#/openapi")) != `"3.1.0"` {
		t.Errorf("changing a returned node changed the Client's")
	}
}

// load.go, Client.Document: "~0" is "~" in a pointer (RFC 6901 section 4),
// and a percent-encoded key is decoded before it is read.
func TestDocumentPointerEscapes(t *testing.T) {
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{},
	"components":{"schemas":{"a~b":{"type":"string"},"c d":{"type":"integer"},"e%f":{"type":"boolean"}}}}`
	c := parseAt(t, doc, "", testDocURI, nil)
	for fragment, want := range map[string]string{
		"#/components/schemas/a~0b":   `{"type":"string"}`,
		"#/components/schemas/c%20d":  `{"type":"integer"}`,
		"#/components/schemas/e%25f":  `{"type":"boolean"}`,
		"#/components/schemas/a~1b":   "",
		"#/components/schemas/nobody": "",
	} {
		got := c.Document(testDocURI + fragment)
		if want == "" {
			if got != nil {
				t.Errorf("Document(%q) = %s, want nil", fragment, got)
			}
			continue
		}
		if got == nil || compact(t, got) != want {
			t.Errorf("Document(%q) = %s, want %s", fragment, got, want)
		}
	}
}

// load.go, Client.DocumentURIs: "The returned slice is new and may be
// changed by the caller."
func TestDocumentURIsNewSlice(t *testing.T) {
	c := parseAt(t, string(readPets(t)), "", testDocURI, nil)
	u := c.DocumentURIs()
	if len(u) == 0 {
		t.Fatal("DocumentURIs() is empty")
	}
	u[0] = "changed"
	if got := c.DocumentURIs(); len(got) == 0 || got[0] != testDocURI {
		t.Errorf("DocumentURIs() = %q after changing an earlier slice", got)
	}
}

// load.go, Load: "Load uses the zero Loader"; Loader.Load and Loader.Parse
// with the zero Loader behave as Load and Parse.
func TestZeroLoader(t *testing.T) {
	var l openapi.Loader
	c, err := l.Load(t.Context(), "testdata/pets.json", nil)
	if err != nil {
		t.Fatalf("Loader.Load: %v", err)
	}
	if len(c.Operations()) != 5 {
		t.Errorf("len(Operations()) = %d, want 5", len(c.Operations()))
	}
	c, err = l.Parse(context.Background(), readPets(t), testDocURI, nil)
	if err != nil {
		t.Fatalf("Loader.Parse: %v", err)
	}
	if c.Version() != "3.1.0" {
		t.Errorf("Version() = %q", c.Version())
	}
}
