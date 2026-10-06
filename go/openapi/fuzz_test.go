package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Fuzz targets: the document tree parser, and path and query serialization.

const fuzzURI = "https://fuzz.example.test/openapi.json"

// fuzzPrefix makes a document whose x-fuzz member is the fuzzed value.
const fuzzPrefix = `{"openapi":"3.1.0","info":{"title":"f","version":"1"},"paths":{},"x-fuzz":`

// jsonTokens returns the token stream encoding/json reads from b, with
// UseNumber: equal streams mean equal values with the same key order and
// the same number spellings.
func jsonTokens(b []byte) ([]json.Token, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var toks []json.Token
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return toks, nil
		}
		if err != nil {
			return toks, err
		}
		toks = append(toks, tok)
	}
}

// jsonShape reports whether valid JSON b has an object with a repeated key,
// and how deeply it nests.
func jsonShape(b []byte) (dup bool, depth int) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct {
		obj     bool
		wantKey bool
		keys    map[string]bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err != nil {
			return dup, depth
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				stack = append(stack, &frame{obj: d == '{', wantKey: true, keys: map[string]bool{}})
				depth = max(depth, len(stack))
			default:
				stack = stack[:len(stack)-1]
				if n := len(stack); n > 0 && stack[n-1].obj {
					stack[n-1].wantKey = true
				}
			}
			continue
		}
		if n := len(stack); n > 0 && stack[n-1].obj {
			top := stack[n-1]
			if top.wantKey {
				key := tok.(string)
				if top.keys[key] {
					dup = true
				}
				top.keys[key] = true
				top.wantKey = false
			} else {
				top.wantKey = true
			}
		}
	}
}

// FuzzDocumentTree: the document tree parser never panics, on any input;
// for a value encoding/json accepts (valid UTF-8, no repeated key, nesting
// well within the 1,000-level bound: load.go, Loader), the node Document
// returns for it equals encoding/json's reading with UseNumber, key order
// and number spellings included (load.go, Loader: "Numbers keep the exact
// value written"; Client.Document: "a copy of only that node").
func FuzzDocumentTree(f *testing.F) {
	for _, seed := range []string{
		`{}`, `[]`, `null`, `true`, `0`, `-0`, `1.50`, `1e400`, `-1.5E-7`, `9007199254740993`,
		`""`, `"\u00e9\ud83d\ude00\n\"\\\/"`, `"é"`, `{"b":1,"a":2,"c":{"z":[1,{"y":null}]}}`,
		`[1,"two",true,null,{"three":3.0}]`, `{"a":1,"a":2}`, `{"a":1,"\u0061":2}`,
		strings.Repeat("[", 50) + strings.Repeat("]", 50), `{"x-":{"$ref":"#/nowhere"}}`,
		"\xEF\xBB\xBF{}", `{"a":}`, "openapi: 3.1.0", fuzzPrefix + `1}`, `{"openapi":"3.1.0"`,
		" \t\n{ \"k\" : [ ] }\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		ctx := context.Background()
		// Any input as a whole document: no panic, whatever the outcome.
		if c, err := openapi.Parse(ctx, b, fuzzURI, nil); err == nil {
			c.Operations()
			c.Document("")
		}

		if !utf8.Valid(b) || !json.Valid(b) {
			return
		}
		if dup, depth := jsonShape(b); dup || depth > 900 {
			return
		}
		doc := append(append([]byte(fuzzPrefix), b...), '}')
		c, err := openapi.Parse(ctx, doc, fuzzURI, nil)
		if err != nil {
			t.Fatalf("valid JSON %q rejected: %v", b, err)
		}
		node := c.Document(fuzzURI + "#/x-fuzz")
		if node == nil {
			t.Fatalf("Document(#/x-fuzz) = nil for %q", b)
		}
		want, err := jsonTokens(b)
		if err != nil {
			t.Fatalf("reading the input: %v", err)
		}
		got, err := jsonTokens(node)
		if err != nil {
			t.Fatalf("node %q is not JSON: %v", node, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("node %q reads as %v, want %v", node, got, want)
		}
	})
}

// uriSafe matches text that holds only RFC 3986 unreserved characters and
// %XX triples in uppercase hex (doc.go, Fixed rules, Percent-encoding).
var uriSafe = regexp.MustCompile(`^(?:[A-Za-z0-9._~-]|%[0-9A-F]{2})*$`)

// asJSON returns s as encoding/json writes and reads it back, which is the
// JSON data the client serializes (doc.go, Values: "The client first
// converts a value to JSON data as encoding/json would"); invalid UTF-8
// becomes U+FFFD.
func asJSON(s string) string {
	b, _ := json.Marshal(s)
	var out string
	json.Unmarshal(b, &out)
	return out
}

const fuzzParamDoc = `{"openapi":"3.1.0","info":{"title":"f","version":"1"},"servers":[{"url":"https://api.example.test"}],
	"paths":{"/café/{id}/ü":{"get":{"operationId":"intl","parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}]}},
	"/items/{id}":{"get":{"operationId":"items","parameters":[
		{"name":"id","in":"path","required":true,"schema":{"type":"string"}},
		{"name":"q","in":"query","schema":{"type":"string"}},
		{"name":"arr","in":"query","schema":{"type":"array"}},
		{"name":"arr2","in":"query","explode":false,"schema":{"type":"array"}},
		{"name":"obj","in":"query","schema":{"type":"object"}}
	]}}}}`

// FuzzPathQuerySerialization: the path (simple) and query (form, explode
// true and false) serializations of any strings use only unreserved
// characters and %XX triples outside the style's own delimiters, and parse
// back with net/url to the values given. A template with non-ASCII literal
// text keeps the value's encoding. A value forming a whole "." or ".."
// segment is refused (doc.go, Fixed rules, Percent-encoding).
func FuzzPathQuerySerialization(f *testing.F) {
	c, err := openapi.Parse(context.Background(), []byte(fuzzParamDoc), fuzzURI, nil)
	if err != nil {
		f.Fatalf("Parse: %v", err)
	}
	for _, s := range [][6]string{
		{"p-7", "blue", "a", "b", "k", "v"},
		{"", "", "", "", "", ""},
		{"a/b c", "x&y=z+w", ",", "%", "a b", "é"},
		{"..", "~", "\x00", "\xff", "[]", "#?"},
		{"{id}", "q=1", "a,b", "", "q", "arr"},
		{"%2F", "%20", "+", " ", "=", "&"},
		{".", "é", "ü", "日本", "ß", "\u2028"},
		{"...", ".", "..", "a.b", ".x", "x."},
	} {
		f.Add(s[0], s[1], s[2], s[3], s[4], s[5])
	}
	f.Fuzz(func(t *testing.T, id, q, a, b, name, value string) {
		in := &openapi.Input{Params: map[string]any{
			"id": id, "q": q, "arr": []string{a, b}, "arr2": []string{a, b}, "obj": map[string]string{name: value},
		}}
		if v := asJSON(id); v == "." || v == ".." {
			for _, key := range []string{"items", "intl"} {
				in := in
				if key == "intl" {
					in = &openapi.Input{Params: map[string]any{"id": id}}
				}
				_, err := c.Prepare(key, in)
				var re *openapi.RequestError
				if !errors.As(err, &re) || re.Inputs["id"] == nil || len(re.Inputs) != 1 {
					t.Fatalf("%s: Prepare with the dot segment %q = %v, want a refusal at Inputs[\"id\"]", key, v, err)
				}
			}
			return
		}

		// The non-ASCII template: its literal text encoded once, the value
		// encoded as in any other path.
		ireq, err := c.Prepare("intl", &openapi.Input{Params: map[string]any{"id": id}})
		if err != nil {
			t.Fatalf("Prepare(intl): %v", err)
		}
		if ireq == nil || ireq.HTTP == nil || ireq.HTTP.URL == nil {
			t.Fatal("Prepare(intl) returned no request")
		}
		iep := ireq.HTTP.URL.EscapedPath()
		mid, ok := strings.CutPrefix(iep, "/caf%C3%A9/")
		if ok {
			mid, ok = strings.CutSuffix(mid, "/%C3%BC")
		}
		if !ok {
			t.Fatalf("escaped path %q, want /caf%%C3%%A9/<value>/%%C3%%BC", iep)
		}
		if !uriSafe.MatchString(mid) {
			t.Fatalf("path value %q holds a character outside the unreserved set or a lowercase triple", mid)
		}
		if got, err := url.PathUnescape(mid); err != nil || got != asJSON(id) {
			t.Errorf("path value %q unescapes to %q, %v; want %q", mid, got, err, asJSON(id))
		}
		if want := "/café/" + asJSON(id) + "/ü"; ireq.HTTP.URL.Path != want {
			t.Errorf("URL.Path %q, want %q", ireq.HTTP.URL.Path, want)
		}

		req, err := c.Prepare("items", in)
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if req == nil || req.HTTP == nil || req.HTTP.URL == nil {
			t.Fatal("Prepare returned no request")
		}
		u := req.HTTP.URL
		unescape := func(part string) string {
			t.Helper()
			if !uriSafe.MatchString(part) {
				t.Fatalf("%q holds a character outside the unreserved set or a lowercase triple", part)
			}
			s, err := url.PathUnescape(part)
			if err != nil {
				t.Fatalf("%q does not unescape: %v", part, err)
			}
			return s
		}

		// Path: /items/ then the encoded value.
		ep := u.EscapedPath()
		seg, ok := strings.CutPrefix(ep, "/items/")
		if !ok {
			t.Fatalf("escaped path %q", ep)
		}
		if got := unescape(seg); got != asJSON(id) {
			t.Errorf("path value %q, want %q", got, asJSON(id))
		}
		if u.Path != "/items/"+asJSON(id) {
			t.Errorf("URL.Path %q, want /items/%s", u.Path, asJSON(id))
		}

		// Query, in declared order: q, arr twice, arr2 once, obj's member.
		parts := strings.Split(u.RawQuery, "&")
		if len(parts) != 5 {
			t.Fatalf("query %q has %d parts, want 5", u.RawQuery, len(parts))
		}
		pair := func(i int) (string, string) {
			n, v, ok := strings.Cut(parts[i], "=")
			if !ok {
				t.Fatalf("query part %q has no =", parts[i])
			}
			return n, v
		}
		for i, want := range []struct{ name, value string }{{"q", q}, {"arr", a}, {"arr", b}} {
			n, v := pair(i)
			if n != want.name || unescape(v) != asJSON(want.value) {
				t.Errorf("query part %d = %q, want %s=%q", i, parts[i], want.name, asJSON(want.value))
			}
		}
		n, v := pair(3)
		items := strings.Split(v, ",")
		if n != "arr2" || len(items) != 2 || unescape(items[0]) != asJSON(a) || unescape(items[1]) != asJSON(b) {
			t.Errorf("query part 3 = %q, want arr2=%q,%q", parts[3], asJSON(a), asJSON(b))
		}
		n, v = pair(4)
		if unescape(n) != asJSON(name) || unescape(v) != asJSON(value) {
			t.Errorf("query part 4 = %q, want %q=%q", parts[4], asJSON(name), asJSON(value))
		}

		// net/url reads the same values back.
		values, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			t.Fatalf("url.ParseQuery(%q): %v", u.RawQuery, err)
		}
		if k := asJSON(name); k != "q" && k != "arr" && k != "arr2" {
			if !slices.Equal(values["q"], []string{asJSON(q)}) || !slices.Equal(values["arr"], []string{asJSON(a), asJSON(b)}) ||
				!slices.Equal(values[k], []string{asJSON(value)}) {
				t.Errorf("url.ParseQuery(%q) = %v", u.RawQuery, values)
			}
		}
	})
}
