package openapi_test

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// YAML documents (stage 5): the Loader's rules for reading a document,
// stated in load.go, Loader: "A document whose first significant byte is
// '{' is read as JSON and, if it is not JSON, as YAML; anything else is
// read as YAML 1.2 under its Core schema, so yes and no stay strings, << is
// an ordinary key, and a scalar key such as an unquoted 200 is read as the
// string it spells. Numbers keep the exact value written. A duplicate key,
// a key that is not a scalar, or a second document in the stream rejects
// the document, and so, in every edition, does a value JSON cannot hold
// (.inf, .nan, or a tag outside the Core schema, such as !!timestamp),
// since Document and Raw are JSON." Scalars resolve by the YAML 1.2.2 Core
// schema, section 10.3.2 (tag resolution).

// yamlHead is a YAML 3.1 document's first five lines; a test's own lines
// follow from line 6.
const yamlHead = "openapi: 3.1.0\ninfo:\n  title: t\n  version: \"1\"\npaths: {}\n"

// yamlValue parses yamlHead followed by tail and returns the JSON of the
// node at the fragment, failing when the document is rejected or has no
// such node.
func yamlValue(t testing.TB, tail, fragment string) []byte {
	t.Helper()
	c := parsed(t, []byte(yamlHead+tail))
	got := c.Document(testDocURI + fragment)
	if got == nil {
		t.Fatalf("Document(%q) = nil for\n%s", fragment, tail)
	}
	return got
}

// Core schema scalars, YAML 1.2.2 section 10.3.2: null, booleans, integers
// in base 10, 8 (0o) and 16 (0x), floats, and everything else a string.
// load.go, Loader: "yes and no stay strings", "Numbers keep the exact value
// written"; client.go, Document: "a YAML document converted as the client
// read it". Each number is compared by its exact value, however the client
// spells it in JSON.
func TestYAMLCoreScalars(t *testing.T) {
	bigHex, _ := new(big.Int).SetString("123456789ABCDEF0123", 16)
	tests := []struct{ yaml, json string }{
		// Strings the YAML 1.1 type library would have made booleans.
		{"yes", `"yes"`}, {"no", `"no"`}, {"Yes", `"Yes"`}, {"NO", `"NO"`},
		{"on", `"on"`}, {"off", `"off"`}, {"y", `"y"`}, {"n", `"n"`},
		// Booleans: true|True|TRUE|false|False|FALSE.
		{"true", `true`}, {"True", `true`}, {"TRUE", `true`},
		{"false", `false`}, {"False", `false`}, {"FALSE", `false`},
		{"tRUE", `"tRUE"`},
		// Null: null|Null|NULL|~ and the empty node.
		{"null", `null`}, {"Null", `null`}, {"NULL", `null`}, {"~", `null`}, {"", `null`},
		{"nULL", `"nULL"`},
		// Integers: [-+]?[0-9]+ (base 10, so 0777 is 777, not octal),
		// 0o[0-7]+, 0x[0-9a-fA-F]+.
		{"0", `0`}, {"-0", `0`}, {"+12", `12`}, {"0777", `777`}, {"09", `9`},
		{"0o17", `15`}, {"0x1F", `31`}, {"0xff", `255`},
		{"123456789012345678901234567890", `123456789012345678901234567890`},
		{"0x123456789ABCDEF0123", bigHex.String()},
		{"18446744073709551615", `18446744073709551615`},
		// Not integers in the Core schema: strings.
		{"0X1F", `"0X1F"`}, {"0O17", `"0O17"`}, {"-0x1F", `"-0x1F"`}, {"0b101", `"0b101"`},
		{"1_000", `"1_000"`}, {"1:30", `"1:30"`}, {"0o8", `"0o8"`},
		// Floats: [-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?.
		{"1.5", `1.5`}, {".5", `0.5`}, {"-.5", `-0.5`}, {"+.5", `0.5`}, {"5.", `5`},
		{"+1.5e3", `1500`}, {"1E-2", `0.01`}, {"1e400", `1e400`},
		{"1.000000000000000000001", `1.000000000000000000001`}, {"0.1", `0.1`},
		// Not floats: strings.
		{"1e", `"1e"`}, {"e3", `"e3"`}, {"1.2.3", `"1.2.3"`}, {"Infinity", `"Infinity"`},
		{"NaN", `"NaN"`}, {"inf", `"inf"`}, {"nan", `"nan"`},
		// No timestamps in the Core schema.
		{"2001-12-14", `"2001-12-14"`}, {"12:30:45", `"12:30:45"`},
		// Quoted scalars are strings.
		{"'123'", `"123"`}, {`"true"`, `"true"`}, {"'yes'", `"yes"`}, {`"~"`, `"~"`},
		{`"\x41\u00e9\U0001F600"`, `"Aé😀"`}, {`"a\tb\\c\"d"`, `"a\tb\\c\"d"`},
		{`"\0\a\e"`, `"\u0000\u0007\u001b"`}, {"'it''s'", `"it's"`},
		// Core tags written explicitly (section 10.3.2's tags).
		{"!!str 123", `"123"`}, {`!!int "42"`, `42`}, {`!!float "1.5"`, `1.5`},
		{`!!bool "true"`, `true`}, {`!!null ""`, `null`}, {"!<tag:yaml.org,2002:str> 5", `"5"`},
		{"!!map {a: 1}", `{"a":1}`}, {"!!seq [1]", `[1]`},
		// Flow collections, and a comment after a value.
		{"[1, a, \"b\", {c: d}, [], {}]", `[1,"a","b",{"c":"d"},[],{}]`},
		{"7 # a comment", `7`},
	}
	for _, tt := range tests {
		t.Run(tt.yaml, func(t *testing.T) {
			sameJSON(t, tt.yaml, yamlValue(t, "x-v: "+tt.yaml+"\n", "#/x-v"), []byte(tt.json))
		})
	}
}

// Multi-line scalars, anchors and aliases, read as YAML 1.2.2 says
// (sections 8.1 block scalars, 7.3.3 plain scalars, 7.1 alias nodes): an
// alias is a copy of the anchored node in the JSON the client reads.
func TestYAMLStructures(t *testing.T) {
	tests := []struct {
		name, tail, fragment, json string
	}{
		{"literal block scalar", "x-v: |\n  a\n  b\nx-end: 0\n", "#/x-v", `"a\nb\n"`},
		{"folded block scalar, strip", "x-v: >-\n  a\n  b\nx-end: 0\n", "#/x-v", `"a b"`},
		{"literal, keep", "x-v: |+\n  a\n\nx-end: 0\n", "#/x-v", `"a\n\n"`},
		{"multi-line plain scalar", "x-v: a\n  b\n", "#/x-v", `"a b"`},
		{"multi-line double-quoted", "x-v: \"a\n  b\"\n", "#/x-v", `"a b"`},
		{"explicit scalar key", "x-v:\n  ? a\n  : b\n", "#/x-v", `{"a":"b"}`},
		{"block sequence of mappings", "x-v:\n  - a: 1\n    b: 2\n  - c\n", "#/x-v", `[{"a":1,"b":2},"c"]`},
		{"scalar alias", "x-a: &s hello\nx-v: *s\n", "#/x-v", `"hello"`},
		{"collection alias", "x-a: &c {k: [1, 2]}\nx-v: [*c, *c]\n", "#/x-v", `[{"k":[1,2]},{"k":[1,2]}]`},
		{"anchor kept where written", "x-v: &c {k: 1}\nx-w: *c\n", "#/x-v", `{"k":1}`},
		// Member order is kept: the JSON's order is the document's.
		{"member order", "x-v: {z: 1, a: 2, m: 3}\n", "#/x-v", `{"z":1,"a":2,"m":3}`},
		// load.go: "<< is an ordinary key": a merge key is not applied, so
		// a key it would merge is no duplicate either.
		{"merge key", "x-b: &b {a: 1}\nx-v: {<<: *b, c: 2}\n", "#/x-v", `{"<<":{"a":1},"c":2}`},
		{"merge key, no duplicate", "x-b: &b {a: 1}\nx-v: {<<: *b, a: 2}\n", "#/x-v", `{"<<":{"a":1},"a":2}`},
		{"merge key, block", "x-v:\n  <<: {a: 1}\n  b: 2\n", "#/x-v", `{"<<":{"a":1},"b":2}`},
		// load.go: "a scalar key such as an unquoted 200 is read as the
		// string it spells": every scalar key is the string it spells, not
		// the value it would resolve to.
		{"keys as spelled", "x-v: {200: a, 0x1F: b, 1.50: c, true: d, ~: e, .inf: f, 007: g, null: h, -1: i, 1e3: j, yes: k}\n", "#/x-v",
			`{"200":"a","0x1F":"b","1.50":"c","true":"d","~":"e",".inf":"f","007":"g","null":"h","-1":"i","1e3":"j","yes":"k"}`},
		{"keys as spelled, block", "x-v:\n  200: a\n  1.0: b\n  1.00: c\n", "#/x-v", `{"200":"a","1.0":"b","1.00":"c"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sameJSON(t, tt.name, yamlValue(t, tt.tail, tt.fragment), []byte(tt.json))
		})
	}
}

// The stream around the document (YAML 1.2.2 sections 6.8.1, the %YAML
// directive, and 9.1, document markers): a %YAML 1.2 directive, "---" and
// "..." are accepted, and comments are not content.
func TestYAMLStream(t *testing.T) {
	for name, doc := range map[string]string{
		"directive and markers": "%YAML 1.2\n---\n" + yamlHead + "x-v: yes\n...\n",
		"start marker only":     "---\n" + yamlHead + "x-v: yes\n",
		"comments":              "# a description\n" + yamlHead + "# between\nx-v: yes # after\n",
	} {
		t.Run(name, func(t *testing.T) {
			c := parsed(t, []byte(doc))
			if v := c.Version(); v != "3.1.0" {
				t.Errorf("Version() = %q", v)
			}
			sameJSON(t, name, c.Document(testDocURI+"#/x-v"), []byte(`"yes"`))
		})
	}
}

// load.go, Loader: "A document whose first significant byte is '{' is read
// as JSON and, if it is not JSON, as YAML" (RFC 9512 section 3.4: YAML
// "could look like JSON"); "anything else is read as YAML". A flow mapping
// root, and JSON carrying a YAML comment, load.
func TestYAMLDetection(t *testing.T) {
	for name, doc := range map[string]string{
		"flow mapping root":    "{openapi: 3.1.0, info: {title: t, version: '1'}, paths: {}, x-v: yes}",
		"JSON with a comment":  "{\"openapi\": \"3.1.0\", # a YAML comment\n \"info\": {\"title\": \"t\", \"version\": \"1\"}, \"paths\": {}, \"x-v\": \"yes\"}",
		"comment before JSON":  "# a YAML comment\n{\"openapi\": \"3.1.0\", \"info\": {\"title\": \"t\", \"version\": \"1\"}, \"paths\": {}, \"x-v\": \"yes\"}",
		"flow with plain keys": "  \n{openapi: \"3.1.0\", info: {title: t, version: \"1\"},\n paths: {}, x-v: 'yes'}\n",
	} {
		t.Run(name, func(t *testing.T) {
			c := parsed(t, []byte(doc))
			sameJSON(t, name, c.Document(testDocURI+"#/x-v"), []byte(`"yes"`))
			sameJSON(t, name, c.Document(testDocURI+"#/info"), []byte(`{"title":"t","version":"1"}`))
		})
	}
}

// load.go, Loader: "A duplicate key, a key that is not a scalar, or a second
// document in the stream rejects the document, and so, in every edition,
// does a value JSON cannot hold (.inf, .nan, or a tag outside the Core
// schema, such as !!timestamp)"; "invalid UTF-8 rejects the document"; and
// "A rejection names the document's URI and the line and column of the
// problem, both counted from 1, the column in bytes". Keys are compared as
// the strings they spell, so 200 and "200" are one key. A tagged node's
// position may be its tag's or its content's (YAML 1.2.2 section 6.9: the
// properties are part of the node), so both are accepted.
func TestYAMLRejections(t *testing.T) {
	tests := []struct {
		name string
		tail string
		line int
		cols []int
	}{
		{"duplicate key, block", "x-a:\n  k: 1\n  k: 2\n", 8, []int{3}},
		{"duplicate key, flow", "x-a: {k: 1, k: 2}\n", 6, []int{13}},
		{"duplicate key, quoted", "x-a: {k: 1, 'k': 2}\n", 6, []int{13}},
		{"duplicate key, 200 and \"200\"", "x-a: {200: a, \"200\": b}\n", 6, []int{15}},
		{"duplicate key, after a multibyte name", "x-ü: {k: 1, k: 2}\n", 6, []int{14}}, // byte 14, character 13
		{"sequence key", "x-a:\n  ? [k]\n  : 1\n", 7, []int{5}},
		{"sequence key, flow", "x-a: {[k]: 1}\n", 6, []int{7}},
		{"mapping key, flow", "x-a: {{k: v}: 1}\n", 6, []int{7}},
		{"alias of a mapping as a key", "x-m: &m {a: 1}\nx-a:\n  *m : 1\n", 8, []int{3}},
		{"a second document after ...", "...\nx-b: 1\n", 7, []int{1}},
		{".inf", "x-v: .inf\n", 6, []int{6}},
		{"-.inf", "x-v: [0, -.inf]\n", 6, []int{10}},
		{".Inf", "x-v: .Inf\n", 6, []int{6}},
		{"+.INF", "x-v: +.INF\n", 6, []int{6}},
		{".nan", "x-v: .nan\n", 6, []int{6}},
		{".NaN", "x-v: .NaN\n", 6, []int{6}},
		{".NAN in a mapping", "x-v: {k: .NAN}\n", 6, []int{10}},
		{"!!float .inf", "x-v: !!float .inf\n", 6, []int{6, 14}},
		{"!!timestamp", "x-v: !!timestamp 2001-12-14\n", 6, []int{6, 18}},
		{"!!binary", "x-v: !!binary aGk=\n", 6, []int{6, 15}},
		{"!!set", "x-v: !!set {a: null}\n", 6, []int{6, 12}},
		{"a local tag", "x-v: !local x\n", 6, []int{6, 13}},
		{"invalid UTF-8", "x-v: \"a\xffb\"\n", 6, []int{8}},
		{"an undefined alias", "x-v: *nope\n", 6, []int{6}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantPosition(t, rejected(t, yamlHead+tt.tail), testDocURI, tt.line, tt.cols...)
		})
	}
	// A second document after "---": the position is the second
	// document's, on its marker's line or the next.
	err := rejected(t, yamlHead+"---\nx-b: 1\n")
	if msg := err.Error(); !strings.Contains(msg, testDocURI) || !numberRE(6).MatchString(msg) && !numberRE(7).MatchString(msg) {
		t.Errorf("rejection %q does not name the document and line 6 or 7", msg)
	}
}

// load.go, Loader: "the column in bytes after any byte order mark": a UTF-8
// byte order mark moves neither the line nor the column of a YAML
// rejection.
func TestYAMLPositionAfterBOM(t *testing.T) {
	tail := "x-a: {k: 1, k: 2}\n"
	plain := rejected(t, yamlHead+tail)
	bom := rejected(t, "\xEF\xBB\xBF"+yamlHead+tail)
	wantPosition(t, plain, testDocURI, 6, 13)
	wantPosition(t, bom, testDocURI, 6, 13)
}

// load.go, Loader: a document "that nests deeper than 1,000 levels (the
// outermost value being level 1), is rejected too", the rejection naming
// the line and column of the problem: the value at level 1,001. Line 6
// holds x-deep, whose value is level 2, so n brackets nest n+1 levels.
func TestYAMLNestingDepth(t *testing.T) {
	deep := func(n int) string {
		return yamlHead + "x-deep: " + strings.Repeat("[", n) + strings.Repeat("]", n) + "\n"
	}
	if _, err := openapi.Parse(t.Context(), []byte(deep(999)), testDocURI, nil); err != nil {
		t.Errorf("1,000 levels: %v, want accepted", err)
	}
	// The 1,000th bracket opens level 1,001, at byte 8+1,000 of line 6.
	wantPosition(t, rejected(t, deep(1000)), testDocURI, 6, 1008)

	// Block style nests the same way: a mapping per level.
	var b strings.Builder
	b.WriteString(yamlHead + "x-deep:\n")
	for i := range 1000 {
		fmt.Fprintf(&b, "%sk:\n", strings.Repeat(" ", 2*(i+1)))
	}
	rejected(t, b.String())

	// An alias counts where it is used: a node nesting 600 levels, used
	// 500 levels deep, makes a document that nests past 1,000.
	tail := "x-a: &d " + strings.Repeat("[", 600) + strings.Repeat("]", 600) + "\n" +
		"x-b: " + strings.Repeat("[", 500) + "*d" + strings.Repeat("]", 500) + "\n"
	rejected(t, yamlHead+tail)
}

// aliasDoc is a YAML document with an anchored sequence of k scalars, a
// sequence of filler scalars, and a sequence of m aliases of the first, so
// its own nodes number about k+filler+m and its aliases add about m*(k+1).
func aliasDoc(k, filler, m int) []byte {
	var b strings.Builder
	b.WriteString(yamlHead)
	b.WriteString("x-anchor: &a [" + strings.Repeat("1, ", k-1) + "1]\n")
	if filler > 0 {
		b.WriteString("x-filler: [" + strings.Repeat("0, ", filler-1) + "0]\n")
	}
	b.WriteString("x-aliases: [" + strings.Repeat("*a, ", m-1) + "*a]\n")
	return []byte(b.String())
}

// load.go, Loader: "A document whose aliases would add more than 1,000,000
// nodes, or more than 100 times its own node count ... is rejected too"
// (RFC 9512 section 4.2 asks for such a bound). The cases keep a wide
// margin on either side of each bound, so they hold however nodes are
// counted (keys included or not, the alias node itself or not).
func TestYAMLAliasBounds(t *testing.T) {
	tests := []struct {
		name          string
		k, filler, m  int
		accept        bool
		addedAbout    int
		ownAbout      int
		boundCrossing string
	}{
		{"about 38 times its own nodes", 1000, 0, 40, true, 40040, 1050, "none"},
		{"about 165 times its own nodes", 1000, 0, 200, false, 200200, 1215, "100 times"},
		{"about 900,000 nodes, 30 times", 10000, 20000, 90, true, 900090, 30100, "none"},
		{"about 1,100,000 nodes, 36 times", 10000, 20000, 110, false, 1100110, 30120, "1,000,000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := aliasDoc(tt.k, tt.filler, tt.m)
			c, err := openapi.Parse(t.Context(), doc, testDocURI, nil)
			switch {
			case tt.accept && err != nil:
				t.Fatalf("aliases adding about %d nodes to about %d: %v, want accepted", tt.addedAbout, tt.ownAbout, err)
			case !tt.accept && (err == nil || c != nil):
				t.Fatalf("aliases adding about %d nodes to about %d (past %s): loaded, want a rejection", tt.addedAbout, tt.ownAbout, tt.boundCrossing)
			case !tt.accept && !strings.Contains(err.Error(), testDocURI):
				t.Errorf("rejection %q does not name the document", err)
			case tt.accept:
				// Each alias is a copy of the anchored sequence.
				last := c.Document(fmt.Sprintf("%s#/x-aliases/%d/%d", testDocURI, tt.m-1, tt.k-1))
				sameJSON(t, "the last alias's last item", last, []byte("1"))
			}
		})
	}
}

// An exponential alias document (the "billion laughs" shape RFC 9512
// section 4.2 describes) is rejected, at a cost the bound sets: its own
// nodes number about 120, so the aliases it may add before rejection number
// at most about 12,000, and rejecting it allocates little.
func TestYAMLAliasBomb(t *testing.T) {
	if !inChild(t) { // a loader that expands it would exhaust memory
		return
	}
	var b strings.Builder
	b.WriteString(yamlHead + "x-l0: &l0 [" + strings.TrimSuffix(strings.Repeat("lol, ", 10), ", ") + "]\n")
	for i := 1; i < 10; i++ {
		fmt.Fprintf(&b, "x-l%d: &l%d [%s]\n", i, i, strings.TrimSuffix(strings.Repeat(fmt.Sprintf("*l%d, ", i-1), 10), ", "))
	}
	doc := []byte(b.String())
	// Two levels of the same shape add 110 nodes to about 50: within both
	// bounds.
	small := "x-l0: &l0 [" + strings.TrimSuffix(strings.Repeat("lol, ", 10), ", ") + "]\n" +
		"x-l1: &l1 [" + strings.TrimSuffix(strings.Repeat("*l0, ", 10), ", ") + "]\n"
	sameJSON(t, "two levels", yamlValue(t, small, "#/x-l1/9/9"), []byte(`"lol"`))
	var err error
	cost := measureRun(func() { _, err = openapi.Parse(context.Background(), doc, testDocURI, nil) })
	if err == nil {
		t.Fatalf("a document whose aliases add ten billion nodes loaded")
	}
	t.Logf("rejected in %v, %d bytes allocated: %v", cost.d, cost.bytes, err)
	if cost.bytes > 32<<20 {
		t.Errorf("rejecting a %d-byte alias bomb allocated %d bytes; want the alias bound to stop it early", len(doc), cost.bytes)
	}
}

// An alias inside the node it names makes a cycle, which no JSON can hold;
// it is rejected and never followed forever (load.go, Loader: the alias and
// depth bounds). A crash would end the test binary, so the cases run in a
// child process.
func TestYAMLRecursiveAlias(t *testing.T) {
	if !inChild(t) {
		return
	}
	for _, tail := range []string{
		"x-a: &a [*a]\n",
		"x-a: &a {k: *a}\n",
		"x-a: &a\n  k: [1, *a]\n",
	} {
		rejected(t, yamlHead+tail)
	}
	// The same alias outside the node it names is a copy.
	sameJSON(t, "alias", yamlValue(t, "x-a: &a [1]\nx-b: {k: *a}\n", "#/x-b"), []byte(`{"k":[1]}`))
}

// load.go, Loader: "A document is UTF-8, or UTF-16 or UTF-32 with a byte
// order mark or, for YAML, as YAML 1.2.2 section 5.2 deduces it" (from the
// pattern of null bytes the first character makes); "a UTF-8 byte order
// mark is ignored". Each encoding reads the same document, characters
// outside the Basic Multilingual Plane included; Document returns UTF-8
// JSON.
func TestDocumentEncodings(t *testing.T) {
	yamlDoc := yamlHead + "x-v: \"é😀\"\n"
	jsonDoc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{},"x-v":"é😀"}`
	cases := map[string][]byte{
		"YAML, UTF-8 with a BOM":               append([]byte("\xEF\xBB\xBF"), yamlDoc...),
		"YAML, UTF-16LE with a BOM":            utf16Text(yamlDoc, false, true),
		"YAML, UTF-16BE with a BOM":            utf16Text(yamlDoc, true, true),
		"YAML, UTF-32LE with a BOM":            utf32Text(yamlDoc, false, true),
		"YAML, UTF-32BE with a BOM":            utf32Text(yamlDoc, true, true),
		"YAML, UTF-16LE deduced":               utf16Text(yamlDoc, false, false),
		"YAML, UTF-16BE deduced":               utf16Text(yamlDoc, true, false),
		"YAML, UTF-32LE deduced":               utf32Text(yamlDoc, false, false),
		"YAML, UTF-32BE deduced":               utf32Text(yamlDoc, true, false),
		"JSON, UTF-16LE with a BOM":            utf16Text(jsonDoc, false, true),
		"JSON, UTF-16BE with a BOM":            utf16Text(jsonDoc, true, true),
		"JSON, UTF-32LE with a BOM":            utf32Text(jsonDoc, false, true),
		"JSON, UTF-32BE with a BOM":            utf32Text(jsonDoc, true, true),
		"YAML flow root, UTF-16BE with a BOM":  utf16Text("{openapi: 3.1.0, info: {title: t, version: '1'}, paths: {}, x-v: \"é😀\"}", true, true),
		"YAML flow root, UTF-32LE with a BOM":  utf32Text("{openapi: 3.1.0, info: {title: t, version: '1'}, paths: {}, x-v: \"é😀\"}", false, true),
		"YAML after a comment, UTF-16LE, BOM":  utf16Text("# c\n"+yamlDoc, false, true),
		"YAML after a comment, UTF-16BE, none": utf16Text("# c\n"+yamlDoc, true, false),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			c := parsed(t, content)
			if v := c.Version(); v != "3.1.0" {
				t.Errorf("Version() = %q", v)
			}
			sameJSON(t, "x-v", c.Document(testDocURI+"#/x-v"), []byte(`"é😀"`))
			sameJSON(t, "Document", c.Document(""), []byte(jsonDoc))
		})
	}
}

// Numbers keep "the exact value written" (load.go, Loader) in Raw and in
// Document, beyond float64 in range and precision, in YAML and in JSON
// alike (describe.go, Schema.Raw: "a copy of the Schema Object exactly as
// written, as JSON (from YAML if need be, as the Loader converts it)"). The
// JSON case is a pin: stage 1 keeps numbers as written.
func TestDocumentNumbersExact(t *testing.T) {
	schema := `{"type":"integer","maximum":18446744073709551615,"minimum":-123456789012345678901234567890,"multipleOf":0.1,"x-tiny":1e-400,"x-fine":1.000000000000000000001}`
	jsonDoc := bare31(`"/n":{"get":{"operationId":"n","parameters":[{"name":"q","in":"query","schema":` + schema + `}]}}`)
	yamlDoc := "openapi: 3.1.0\ninfo: {title: t, version: '1'}\npaths:\n  /n:\n    get:\n      operationId: n\n      parameters:\n" +
		"        - name: q\n          in: query\n          schema:\n            type: integer\n            maximum: 18446744073709551615\n" +
		"            minimum: -123456789012345678901234567890\n            multipleOf: 0.1\n            x-tiny: 1e-400\n            x-fine: 1.000000000000000000001\n"
	for name, doc := range map[string]string{"JSON": jsonDoc, "YAML": yamlDoc} {
		t.Run(name, func(t *testing.T) {
			c := parsed(t, []byte(doc))
			p := param(t, mustOp(t, c, "n"), 0)
			if p.Schema == nil {
				t.Fatal("Schema = nil")
			}
			sameJSON(t, "Raw", p.Schema.Raw(), []byte(schema))
			sameJSON(t, "Document", c.Document(p.Source+"/schema"), []byte(schema))
		})
	}
}

// load.go, Parse: "The content is JSON or YAML text"; Load reads YAML from
// a file and over http too. client.go, Document: "a YAML document converted
// as the client read it". A YAML document describes what the same JSON
// document describes: the same operations, parameters, Sources and calls.
// describe.go, Message.Key: a response's key "a code such as "404"": an
// unquoted 200 is the key "200".
func TestYAMLDocumentLoads(t *testing.T) {
	yamlDoc := `openapi: 3.1.0
info:
  title: Pets
  version: 1.0.0
servers:
  - url: '@BASE@'
paths:
  /pets/{petId}:
    get:
      operationId: getPet
      parameters:
        - name: petId
          in: path
          required: true
          schema: {type: string}
        - name: verbose
          in: query
          schema: {type: boolean}
      responses:
        200:
          description: the pet
          content:
            application/json:
              schema: {type: object}
        default:
          description: a problem
`
	w := newWire(t, jsonAnswer(200, `{"name":"Rex"}`))
	check := func(t *testing.T, c *openapi.Client, uri string) {
		op := mustOp(t, c, "getPet")
		if op.Err != nil || len(op.Params) != 2 || len(op.Responses) != 2 {
			t.Fatalf("getPet = %+v", op)
		}
		if op.Responses[0].Key != "200" || op.Responses[1].Key != "default" {
			t.Errorf("response keys %q, %q; want 200 and default", op.Responses[0].Key, op.Responses[1].Key)
		}
		if want := uri + "#/paths/~1pets~1%7BpetId%7D/get"; op.Source != want {
			t.Errorf("Source = %q, want %q", op.Source, want)
		}
		if got := c.Document(op.Source + "/parameters/0/schema"); got == nil {
			t.Errorf("Document(%s/parameters/0/schema) = nil", op.Source)
		} else {
			sameJSON(t, "parameter schema", got, []byte(`{"type":"string"}`))
		}
		if got := c.Document(uri + "#/info/version"); string(got) != `"1.0.0"` {
			t.Errorf("info.version = %s, want the string \"1.0.0\"", got)
		}
		var out map[string]any
		mustCall(t, c, "getPet", &openapi.Input{Params: map[string]any{"petId": "p1", "verbose": true}}, &out)
		if got := w.last(t).RequestURI; got != "/pets/p1?verbose=true" {
			t.Errorf("sent %q", got)
		}
		if out["name"] != "Rex" {
			t.Errorf("decoded %v", out)
		}
	}
	content := strings.ReplaceAll(yamlDoc, "@BASE@", w.URL)

	t.Run("Parse", func(t *testing.T) {
		check(t, parsed(t, []byte(content)), testDocURI)
	})
	t.Run("Load, file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "openapi.yaml")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		c := mustLoad(t, nil, path, nil)
		check(t, c, c.DocumentURIs()[0])
	})
	t.Run("Load, http", func(t *testing.T) {
		s := newSite(t)
		s.put("/openapi.yaml", content)
		c := mustLoad(t, nil, s.uri("/openapi.yaml"), nil)
		check(t, c, s.uri("/openapi.yaml"))
	})
}

// A YAML document and the JSON document it was written from describe the
// same Client: every descriptor equal, Sources included, and Document the
// same JSON (load.go, Loader; client.go, Document). The YAML is the tests'
// own block and flow writing of testdata/pets.json and of a synthetic
// document, in several scalar styles.
func TestYAMLSameAsJSON(t *testing.T) {
	large, _ := largeDoc(12, 6)
	for name, doc := range map[string][]byte{"pets": readPets(t), "synthetic": large} {
		want := parsed(t, doc)
		wantDesc := describeAll(want)
		for _, flow := range []bool{false, true} {
			for i, st := range []yamlStyle{{}, {pick: rotating()}, {pick: func(n int) int { return n - 1 }}} {
				t.Run(fmt.Sprintf("%s/flow=%t/style%d", name, flow, i), func(t *testing.T) {
					y := toYAML(t, doc, flow, st)
					got := parsed(t, y)
					if d := describeAll(got); d != wantDesc {
						t.Errorf("descriptors differ from the JSON document's:\n%s", firstDiff(d, wantDesc))
					}
					sameJSON(t, "Document", got.Document(""), want.Document(""))
					if got.Version() != want.Version() {
						t.Errorf("Version() = %q, want %q", got.Version(), want.Version())
					}
				})
			}
		}
	}
}

// rotating returns a choice function that cycles through the choices.
func rotating() func(n int) int {
	i := 0
	return func(n int) int {
		i++
		return i % n
	}
}

// firstDiff shows the first line where got and want differ.
func firstDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(g), len(w)) {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			return fmt.Sprintf("line %d:\n got %s\nwant %s", i+1, a, b)
		}
	}
	return "(equal)"
}

// The emitter the equivalence tests rely on writes YAML the tests can read
// back by hand: a check of its own output on a few values, against YAML
// 1.2.2's productions as written out here.
func TestYAMLEmitterReference(t *testing.T) {
	n, err := readJSON([]byte(`{"a":[1,{"b":"x y","c":[]}],"200":"yes","q\"k":"line\nbreak","e":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	wantBlock := "\"a\":\n  - 1\n  -\n    \"b\": \"x y\"\n    \"c\": []\n\"200\": \"yes\"\n\"q\\\"k\": \"line\\nbreak\"\n\"e\": {}\n"
	if got := yamlBlock(n, yamlStyle{}); got != wantBlock {
		t.Errorf("block:\n%s\nwant\n%s", got, wantBlock)
	}
	wantFlow := `{"a": [1, {"b": "x y", "c": []}], "200": "yes", "q\"k": "line\nbreak", "e": {}}`
	if got := yamlFlow(n, yamlStyle{}); got != wantFlow {
		t.Errorf("flow: %s\nwant %s", got, wantFlow)
	}
	plain := yamlStyle{pick: func(n int) int { return n - 1 }}
	if got, want := yamlFlow(n, plain), `{a: [1, {b: "x y", c: []}], 200: yes, "q\"k": "line\nbreak", e: {}}`; got != want {
		t.Errorf("plain: %s\nwant %s", got, want)
	}
	if got := yamlDouble("\u0085\u2028\ufeff\x7f\x01\U0001F600"); got != `"\u0085\u2028\uFEFF\u007F\u0001😀"` {
		t.Errorf("escapes: %s", got)
	}
	if !bytes.Contains(toYAML(t, []byte(`{"k":1}`), true, yamlStyle{}), []byte("\n{")) {
		t.Errorf("flow YAML does not begin after a comment line")
	}
}
