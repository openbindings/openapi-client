package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Tests for the package documentation's sentences on loading: rejection
// positions, the alias byte bound, URI claims, file admission,
// discriminator mapping values, redirected references, unreadable
// referenced documents, %YAML directives, references with userinfo, and
// the JSON of a YAML document. Each test cites the sentence it pins. That a
// node's position is where it starts, its tag included, is tested in
// TestYAMLRejections.

// load.go, Loader: "A rejection names the document's URI and where the
// problem is: ... otherwise the line and column, both counted from 1, the
// column in the document's own bytes (two per UTF-16 code unit, four per
// UTF-32 character) after any byte order mark", with or without the mark, a
// character outside the Basic Multilingual Plane taking two UTF-16 code
// units. A duplicate key is no syntax error, so its line and column are
// named.
func TestColumnsInUTF16AndUTF32(t *testing.T) {
	encodings := []struct {
		name  string
		text  func(string) []byte
		unit  int // bytes per code unit
		utf16 bool
	}{
		{"UTF-16LE with a BOM", func(s string) []byte { return utf16Text(s, false, true) }, 2, true},
		{"UTF-16BE with a BOM", func(s string) []byte { return utf16Text(s, true, true) }, 2, true},
		{"UTF-16LE deduced", func(s string) []byte { return utf16Text(s, false, false) }, 2, true},
		{"UTF-16BE deduced", func(s string) []byte { return utf16Text(s, true, false) }, 2, true},
		{"UTF-32LE with a BOM", func(s string) []byte { return utf32Text(s, false, true) }, 4, false},
		{"UTF-32BE deduced", func(s string) []byte { return utf32Text(s, true, false) }, 4, false},
	}
	// Each case: the document, the line of the problem, and the text before
	// it on that line.
	cases := []struct {
		name, doc, before string
		line              int
		bomOnly           bool // a JSON-looking document, read with a BOM only
	}{
		{"ASCII", yamlHead + "x-a: {k: 1, k: 2}\n", "x-a: {k: 1, ", 6, false},
		{"after a surrogate pair", yamlHead + "x-😀: {k: 1, k: 2}\n", "x-😀: {k: 1, ", 6, false},
		{"on the first line", `{"openapi":"3.1.0","paths":{},"paths":{}}`, `{"openapi":"3.1.0","paths":{},`, 1, true},
	}
	for _, e := range encodings {
		for _, c := range cases {
			if c.bomOnly && strings.Contains(e.name, "deduced") {
				continue
			}
			t.Run(e.name+"/"+c.name, func(t *testing.T) {
				units := utf8.RuneCountInString(c.before)
				if e.utf16 {
					units = len(utf16.Encode([]rune(c.before)))
				}
				err := rejected(t, string(e.text(c.doc)))
				wantPosition(t, err, testDocURI, c.line, 1+e.unit*units)
			})
		}
	}
}

// Regression check, not contract: the 64 MiB figure for what rejecting the
// document allocates. The rest is contract: load.go, Loader: "A document whose
// aliases would add ... more than 100 times its own size in bytes ... is
// rejected too", by what its aliases would add, so before they are added, and
// "A rejection names the document's URI": one 30 KB string aliased 8,000 times
// adds 8,000 nodes, within the node bounds, and 240 MB, beyond the byte bound,
// so it is rejected; aliased 50 times, it adds 1.5 MB, under the bound of about
// 3 MB, and loads. The same holds for the document written in UTF-16, whose own
// size counts its own bytes as retrieved, with the same margins. A loader that
// expanded the first would use hundreds of megabytes, so the test runs in a
// child process.
func TestYAMLAliasByteBound(t *testing.T) {
	if !inChild(t) {
		return
	}
	for _, enc := range []struct {
		name string
		text func(string) []byte
	}{
		{"UTF-8", func(s string) []byte { return []byte(s) }},
		{"UTF-16LE with a BOM", func(s string) []byte { return utf16Text(s, false, true) }},
	} {
		doc := func(aliases int) []byte {
			return enc.text(yamlHead + "x-s: &s \"" + strings.Repeat("a", 30000) + "\"\nx-a: [" + strings.TrimSuffix(strings.Repeat("*s, ", aliases), ", ") + "]\n")
		}
		c := parsed(t, doc(50))
		sameJSON(t, enc.name+": the last alias", c.Document(testDocURI+"#/x-a/49"), []byte(`"`+strings.Repeat("a", 30000)+`"`))

		big := doc(8000)
		var err error
		cost := measureRun(func() { _, err = openapi.Parse(context.Background(), big, testDocURI, nil) })
		if err == nil {
			t.Fatalf("%s: a %d-byte document whose aliases add 240 MB loaded", enc.name, len(big))
		}
		if !strings.Contains(err.Error(), testDocURI) {
			t.Errorf("%s: rejection %q does not name the document", enc.name, err)
		}
		t.Logf("%s: rejected in %v, %d bytes allocated: %v", enc.name, cost.d, cost.bytes, err)
		if cost.bytes > 64<<20 {
			t.Errorf("%s: rejecting it allocated %d bytes; the bound stops it at about %d added", enc.name, cost.bytes, 100*len(big))
		}
	}
}

// load.go, Loader: "A URI claimed by two different documents or schemas is
// unresolvable, and the error names both; a document's URI and the $id of the
// schema at its root claim one schema". A schema document whose root $id is
// its own URL resolves, by that URL and by a relative reference; one whose
// nested schema claims the same URL as its root conflicts. (Each document is
// reached only as a schema, so its root is read in one context;
// TestURIClaimedTwice checks that errors name both claimants.)
func TestRootIDEqualToRetrievalURI(t *testing.T) {
	s := newSite(t)
	body := func(op, ref string) string {
		return fmt.Sprintf(`"/%s":{"post":{"operationId":"%s","requestBody":{"content":{"multipart/form-data":{"schema":{"$ref":%q}}}}}}`, op, op, ref)
	}
	s.put("/openapi.json", entry31(strings.Join([]string{
		body("relative", "schemas/pet.json"),
		body("absolute", "@SELF@/schemas/pet.json"),
		body("dup", "schemas/dup.json"),
	}, ",")))
	s.put("/schemas/pet.json", `{"$id":"@SELF@/schemas/pet.json","type":"object","properties":{"n":{"type":"integer"}}}`)
	s.put("/schemas/dup.json", `{"$id":"@SELF@/schemas/dup.json","properties":{"r":{"type":"integer"}},
		"$defs":{"x":{"$id":"@SELF@/schemas/dup.json","properties":{"m":{"type":"integer"}}}}}`)
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	want := []string{"n=text/plain@" + s.uri("/schemas/pet.json#/properties/n")}
	wantStrings(t, "relative", encodingOf(t, c, "relative"), want)
	wantStrings(t, "absolute", encodingOf(t, c, "absolute"), want)
	if got := encodingOf(t, c, "dup"); len(got) != 0 {
		t.Errorf("a URI two nodes claim reached %q; want it unresolvable", got)
	}
	for _, path := range []string{"/schemas/pet.json", "/schemas/dup.json"} {
		if n := s.count(path); n != 1 {
			t.Errorf("%s fetched %d times, want once", path, n)
		}
	}
}

// load.go, Loader.AllowReference: "With nil, http and https references may
// reach the entry document's original origin and Origins. For a file entry,
// file references may reach only files under the entry file's directory ...;
// Origins does not enlarge that file boundary": a file entry reaches an origin
// Origins lists, not another, and still no file outside its directory.
func TestFileEntryReachesOrigins(t *testing.T) {
	listed, other := newSite(t), newSite(t)
	listed.put("/p.json", `{"P":{"name":"p","in":"query"}}`)
	other.put("/p.json", `{"P":{"name":"o","in":"query"}}`)
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "sibling"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "sibling", "b.json"), []byte(`{"P":{"name":"b","in":"query"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(tmp, "root", "openapi.json")
	if err := os.MkdirAll(filepath.Dir(entry), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte(bare31(paramOps(map[string]string{
		"listed": listed.URL + "/p.json#/P", "other": other.URL + "/p.json#/P", "sibling": "../sibling/b.json#/P",
	}))), 0o600); err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, &openapi.Loader{Origins: []string{listed.URL}}, entry, nil)
	admittedRef(t, c, "listed")
	refusedRef(t, c, "other", other.URL+"/p.json")
	refusedRef(t, c, "sibling", "sibling/b.json")
	if n := listed.count("/p.json"); n != 1 {
		t.Errorf("the listed origin's document fetched %d times, want once", n)
	}
	if n := other.total(); n != 0 {
		t.Errorf("an origin Origins does not list received %d requests", n)
	}
}

// load.go, Loader: Discriminator mapping values that are not component names
// are followed, and "a value that could be a component name is read as one, as
// OpenAPI recommends, and never fetched" (OpenAPI 3.1.2 section 4.8.25.3, and
// section 4.8.7: component names match ^[a-zA-Z0-9\.\-_]+$), whether or not
// such a component exists, in the entry and in a referenced document; a value
// that cannot be a name, such as "./far.json", is a URI and is fetched.
func TestNameShapedMappingNeverFetched(t *testing.T) {
	s := newSite(t)
	mapped := func(mapping string) string {
		return `{"oneOf":[{"type":"object"}],"discriminator":{"propertyName":"kind","mapping":{` + mapping + `}}}`
	}
	s.put("/api/openapi.json", entry31(`"/x":{"post":{"operationId":"x","requestBody":{"content":{"application/json":{"schema":`+
		mapped(`"dog":"Dog","cat":"dog.json","bird":"Cat-1.v2","far":"./far.json"`)+`}}},
		"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"other/defs.json#/S"}}}}}}}`))
	s.put("/api/far.json", `{"type":"object"}`)
	s.put("/api/other/defs.json", `{"S":`+mapped(`"a":"Ant","b":"ant.json","c":"../near.json"`)+`}`)
	s.put("/api/near.json", `{"type":"object"}`)
	r := &recorder{admit: func(from, to string) bool { return true }}
	c := mustLoad(t, &openapi.Loader{AllowReference: r.allow}, s.uri("/api/openapi.json"), nil)
	admittedRef(t, c, "x")
	for path, n := range map[string]int{"/api/far.json": 1, "/api/other/defs.json": 1, "/api/near.json": 1,
		"/api/Dog": 0, "/api/dog.json": 0, "/api/Cat-1.v2": 0, "/api/other/Ant": 0, "/api/other/ant.json": 0} {
		if got := s.count(path); got != n {
			t.Errorf("%s requested %d times, want %d", path, got, n)
		}
	}
	for _, cl := range r.list() {
		for _, name := range []string{"/Dog", "/dog.json", "/Cat-1.v2", "/Ant", "/ant.json"} {
			if strings.HasSuffix(cl.to, name) {
				t.Errorf("AllowReference was asked about %q, a component name", cl.to)
			}
		}
	}
}

// load.go, Loader: "A reference resolves first to what loaded documents
// identify: a document by its retrieval URI (and the URI requested, when a
// redirect led there)": a later reference to a URI that redirected is not
// requested again, over http or through a Fetch that reports a final URI.
func TestRedirectedURIIdentified(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		s := newSite(t)
		s.put("/openapi.json", entry31(paramOps(map[string]string{"first": "a.json#/P", "later": "b.json#/R"})))
		s.handle("/a.json", redirect(http.StatusFound, s.uri("/new/a.json")))
		s.put("/new/a.json", `{"P":{"name":"p","in":"query"},"Q":{"name":"q","in":"query"}}`)
		s.put("/b.json", `{"R":{"$ref":"a.json#/Q"}}`)
		c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
		admittedRef(t, c, "first")
		admittedRef(t, c, "later")
		if op := mustOp(t, c, "later"); op.Err == nil && op.Params[0].Source != s.uri("/new/a.json#/Q") {
			t.Errorf("later's parameter Source = %q, want it in the redirected document", op.Params[0].Source)
		}
		if n, m := s.count("/a.json"), s.count("/new/a.json"); n != 1 || m != 1 {
			t.Errorf("a.json requested %d times and new/a.json %d; want each once", n, m)
		}
	})
	t.Run("Fetch", func(t *testing.T) {
		const base = "https://docs.example.test/"
		m := newMemFetch(map[string]string{
			base + "openapi.json": bare31(paramOps(map[string]string{"first": "a.json#/P", "later": "b.json#/R"})),
			base + "a.json":       `{"P":{"name":"p","in":"query"},"Q":{"name":"q","in":"query"}}`,
			base + "b.json":       `{"R":{"$ref":"a.json#/Q"}}`,
		})
		m.finals[base+"a.json"] = base + "new/a.json"
		c := mustLoad(t, &openapi.Loader{Fetch: m.fetch}, base+"openapi.json", nil)
		admittedRef(t, c, "first")
		admittedRef(t, c, "later")
		if n := m.callsTo(base + "a.json"); n != 1 {
			t.Errorf("Fetch(a.json) called %d times, want once", n)
		}
	})
}

// errors.go, ErrUnresolved: the Err "wraps the retrieval error too when
// fetching or reading its document failed (one that cannot be read is named
// with where the problem is, as Loader describes for an entry document)": a
// referenced YAML document with a duplicate key, a JSON one with a duplicate
// key, and one with invalid UTF-8, each disabling only what reaches it.
func TestUnreadableReferencedDocument(t *testing.T) {
	const base = "https://docs.example.test/"
	docs := map[string]string{
		base + "openapi.json": bare31(paramOps(map[string]string{"yaml": "bad.yaml#/P", "json": "bad.json#/P", "utf8": "bad8.yaml#/P", "good": "good.json#/P"})),
		base + "bad.yaml":     "# a parameter\nP:\n  in: query\n  q: {name: p, name: q}\n",
		base + "bad.json":     "{\n  \"P\": {\n    \"in\": \"query\",\n    \"name\": \"p\", \"name\": \"q\"\n  }\n}\n",
		base + "bad8.yaml":    "P:\n  in: query\n  name: \"a\xffb\"\n",
		base + "good.json":    `{"P":{"name":"g","in":"query"}}`,
	}
	c := mustLoad(t, &openapi.Loader{Fetch: newMemFetch(docs).fetch}, base+"openapi.json", nil)
	admittedRef(t, c, "good")
	for _, tt := range []struct {
		key, doc  string
		line, col int
	}{
		{"yaml", "bad.yaml", 4, 16},
		{"json", "bad.json", 4, 18},
		{"utf8", "bad8.yaml", 3, 11},
	} {
		op := mustOp(t, c, tt.key)
		if !errors.Is(op.Err, openapi.ErrUnresolved) {
			t.Errorf("%s Err = %v, want unresolved", tt.key, op.Err)
			continue
		}
		wantPosition(t, op.Err, base+tt.doc, tt.line, tt.col)
		if c.Document(base+tt.doc) != nil {
			t.Errorf("Document(%s) is not nil", tt.doc)
		}
	}
}

// load.go, Loader: "A %YAML directive for any version 1.x changes none of
// this, and any other major version rejects the document (YAML 1.2.2 section
// 6.8.1)", the rejection at the directive, which begins its line.
func TestYAMLDirectiveVersions(t *testing.T) {
	for _, v := range []string{"1.1", "1.2", "1.3"} {
		c := parsed(t, []byte("%YAML "+v+"\n---\n"+yamlHead+"x-v: yes\n"))
		sameJSON(t, "%YAML "+v, c.Document(testDocURI+"#/x-v"), []byte(`"yes"`))
	}
	for _, v := range []string{"2.0", "4.0"} {
		wantPosition(t, rejected(t, "# a\n# b\n%YAML "+v+"\n---\n"+yamlHead), testDocURI, 3, 1)
	}
}

// load.go, Loader: "A reference is unresolvable and never fetched when the
// URI requested for it has userinfo ..., or is a file URL naming a host
// other than localhost, as Load refuses such a uri" (RFC 9110 section 4.2.4,
// RFC 8089). The reference is not shown with its userinfo (errors.go,
// ErrUnresolved: "Where the client names a URI, a reference or a redirect's
// Location included, it omits userinfo and query").
// Admission is never asked, nothing is requested, and no error text shows
// the user or the password.
func TestUserinfoAndFileHostReferences(t *testing.T) {
	s := newSite(t)
	host := strings.TrimPrefix(s.URL, "http://")
	s.put("/x.json", `{"P":{"name":"x","in":"query"}}`)
	s.put("/openapi.json", entry31(paramOps(map[string]string{
		"userinfo": "http://u53r:s3cr3t-pw@" + host + "/x.json#/P",
		"filehost": "file://otherhost/x.json#/P",
	})))
	r := &recorder{admit: func(from, to string) bool { return true }}
	c := mustLoad(t, &openapi.Loader{AllowReference: r.allow}, s.uri("/openapi.json"), nil)
	for _, key := range []string{"userinfo", "filehost"} {
		op := mustOp(t, c, key)
		if !errors.Is(op.Err, openapi.ErrUnresolved) {
			t.Errorf("%s Err = %v, want unresolved", key, op.Err)
		}
		_, callErr := c.Call(t.Context(), key, nil, nil)
		_, prepErr := c.Prepare(key, nil)
		for _, err := range []error{op.Err, callErr, prepErr} {
			if msg := fmt.Sprint(err); strings.Contains(msg, "u53r") || strings.Contains(msg, "s3cr3t-pw") {
				t.Errorf("%s: error text %q shows the userinfo", key, msg)
			}
		}
	}
	if n := s.count("/x.json"); n != 0 {
		t.Errorf("x.json fetched %d times through a URI with userinfo", n)
	}
	if calls := r.list(); len(calls) != 0 {
		t.Errorf("AllowReference was asked %q", calls)
	}

	m := newMemFetch(map[string]string{"https://docs.example.test/openapi.json": bare31(paramOps(map[string]string{
		"userinfo": "https://u53r:s3cr3t-pw@docs.example.test/x.json#/P",
		"filehost": "file://otherhost/x.json#/P",
	}))})
	c = mustLoad(t, &openapi.Loader{Fetch: m.fetch, AllowReference: r.allow}, "https://docs.example.test/openapi.json", nil)
	for _, uri := range m.callList() {
		if uri != "https://docs.example.test/openapi.json" {
			t.Errorf("Fetch was called for %q", uri)
		}
	}
	for _, key := range []string{"userinfo", "filehost"} {
		if op := mustOp(t, c, key); !errors.Is(op.Err, openapi.ErrUnresolved) || strings.Contains(fmt.Sprint(op.Err), "s3cr3t-pw") {
			t.Errorf("%s Err = %v", key, op.Err)
		}
	}
	if calls := r.list(); len(calls) != 0 {
		t.Errorf("AllowReference was asked %q", calls)
	}
}

// jsonNoHTML writes s as encoding/json writes a string without HTML
// escaping.
func jsonNoHTML(t testing.TB, s string) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// load.go, Client.Document: "A YAML document's JSON has no insignificant
// whitespace, its members in the order written and its strings as
// encoding/json writes them without HTML escaping. A number keeps its spelling
// where JSON's grammar allows it; otherwise only what the grammar requires
// changes: a leading + is dropped, as are zeros leading a whole part of more
// than one digit, a 0 is written before a leading point, a point with no digit
// after it is dropped, and a hexadecimal or octal integer is written in
// decimal". Exact bytes, for the whole document, a node, and a Schema's Raw
// ("as the Loader converts it").
func TestYAMLDocumentBytes(t *testing.T) {
	str := "tab\there \"q\" back\\slash \u2028 \x01 \x7f é 😀 </script> & <b>"
	yamlDoc := `openapi: 3.1.0
info: {title: "<a & b>", version: '1'}
paths:
  /p:
    get:
      operationId: p
      parameters:
        - name: q
          in: query
          schema: {type: integer, maximum: +12, minimum: .5, multipleOf: 5., x-hex: 0x1F}
x-s: "tab\there \"q\" back\\slash \u2028 \x01 \x7f é 😀 </script> & <b>"
x-n: [+12, .5, -.5, 5., +1.5e3, 1E-2, 0x1F, 0o17, 0xFF, 1e400, -0, 1.50, 0.000, 5.e3, +.5, -5., 1E+2, 123456789012345678901234567890, 007, 0777, -007, 00.5, -00.5]
x-b: [true, True, FALSE, null, ~, Null, '']
x-o: {z: 1, a: 2}
x-keys: {200: a, 1.0: b, "q\"k": c, "<k>": d, ü: e}
x-block: |
  line one
  line two
`
	schema := `{"type":"integer","maximum":12,"minimum":0.5,"multipleOf":5,"x-hex":31}`
	numbers := `[12,0.5,-0.5,5,1.5e3,1E-2,31,15,255,1e400,-0,1.50,0.000,5e3,0.5,-5,1E+2,123456789012345678901234567890,7,777,-7,0.5,-0.5]`
	want := `{"openapi":"3.1.0","info":{"title":"<a & b>","version":"1"},"paths":{"/p":{"get":{"operationId":"p","parameters":[{"name":"q","in":"query","schema":` + schema + `}]}}},` +
		`"x-s":` + jsonNoHTML(t, str) + `,"x-n":` + numbers + `,"x-b":[true,true,false,null,null,null,""],"x-o":{"z":1,"a":2},` +
		`"x-keys":{"200":"a","1.0":"b",` + jsonNoHTML(t, `q"k`) + `:"c","<k>":"d","ü":"e"},"x-block":"line one\nline two\n"}`
	c := parsed(t, []byte(yamlDoc))
	if got := string(c.Document("")); got != want {
		t.Errorf("Document:\n got %s\nwant %s", got, want)
	}
	if got := string(c.Document(testDocURI + "#/x-n")); got != numbers {
		t.Errorf("Document(#/x-n) = %s, want %s", got, numbers)
	}
	if got := string(param(t, mustOp(t, c, "p"), 0).Schema.Raw()); got != schema {
		t.Errorf("Raw = %s, want %s", got, schema)
	}
}
