package openapi_test

import (
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Loader reads YAML under the Core schema. YAML 1.2.2 sections 6.9.1 and
// 10.3.2 resolve the explicit non-specific ! tag on a mapping or sequence
// by its kind, so the collection keeps its kind and is not a value with a
// tag outside the Core schema. How the parser presents a non-specific tag
// on a scalar is the parser's own syntax, so no scalar is checked here.
func TestYAMLAdapterNonSpecificTags(t *testing.T) {
	for _, tt := range []struct{ yaml, json string }{
		{"! [1]", `[1]`}, {"! {a: 1}", `{"a":1}`},
	} {
		t.Run(tt.yaml, func(t *testing.T) {
			sameJSON(t, tt.yaml, yamlValue(t, "x-v: "+tt.yaml+"\n", "#/x-v"), []byte(tt.json))
		})
	}
}

// Loader reads a %YAML 1.x directive as 1.2, which changes the
// interpretation of a valid directive, not its grammar: YAML 1.2.2 section
// 6.8.1 requires a numeric major.minor and at most one YAML directive;
// section 9.1.5 requires --- after directives. Loader rejects an unparseable
// document with its URI and source position.
func TestYAMLAdapterDirectiveGrammar(t *testing.T) {
	for _, tt := range []struct {
		name, prefix string
		line         int
	}{
		{"repeated", "%YAML 1.2\n%YAML 1.2\n---\n", 2},
		{"missing minor", "%YAML 1\n---\n", 1},
		{"nonnumeric minor", "%YAML 1.x\n---\n", 1},
		{"trailing argument", "%YAML 1.2 extra\n---\n", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The directive contains the defect; its scanner-specific column
			// may identify either the directive or the offending character.
			err := rejected(t, tt.prefix+yamlHead)
			if !strings.Contains(err.Error(), testDocURI) || !numberRE(tt.line).MatchString(err.Error()) {
				t.Errorf("rejection %q does not name URI and line %d", err, tt.line)
			}
		})
	}
	t.Run("missing document marker", func(t *testing.T) {
		rejected(t, "%YAML 1.2\n"+yamlHead)
	})
	// The grammar checks must still admit a future 1.x version.
	c := parsed(t, []byte("%YAML 1.3 # a comment\n---\n"+yamlHead+"x-v: yes\n"))
	sameJSON(t, "valid directive", c.Document(testDocURI+"#/x-v"), []byte(`"yes"`))
}

// Loader: "for a YAML syntax error, the parser's own message, as it gives
// it". YAML 1.2.2 section 5.3 reserves @, so no token starts with it, and
// section 5.4 recognizes LF, CRLF and CR. Whatever the document's encoding
// or line endings, the rejection names the document's URI and carries the
// parser's message.
func TestYAMLAdapterSyntaxError(t *testing.T) {
	for _, enc := range []struct {
		name string
		text func(string) []byte
	}{
		{"UTF-8", func(s string) []byte { return []byte(s) }},
		{"UTF-16LE", func(s string) []byte { return utf16Text(s, false, true) }},
		{"UTF-32BE", func(s string) []byte { return utf32Text(s, true, true) }},
	} {
		for _, ending := range []struct{ name, text string }{{"LF", "\n"}, {"CRLF", "\r\n"}, {"CR", "\r"}} {
			for _, prefix := range []string{"x-v:  ", "x-😀:  "} {
				t.Run(enc.name+"/"+ending.name+"/"+prefix, func(t *testing.T) {
					doc := strings.ReplaceAll(yamlHead+prefix+"@bad\n", "\n", ending.text)
					c, err := openapi.Parse(t.Context(), enc.text(doc), testDocURI, nil)
					if c != nil {
						t.Fatalf("Parse accepted a document with @ starting a token on line 6")
					}
					wantSyntaxError(t, err, testDocURI)
				})
			}
		}
	}
}

// Loader's node rejection positions use the same line model as the YAML
// parser (YAML 1.2.2 section 5.4). Line endings do not change a value or a
// duplicate key's position within its line.
func TestYAMLAdapterLineEndings(t *testing.T) {
	for _, ending := range []struct{ name, text string }{{"LF", "\n"}, {"CRLF", "\r\n"}, {"CR", "\r"}} {
		t.Run(ending.name, func(t *testing.T) {
			c := parsed(t, []byte(strings.ReplaceAll(yamlHead+"x-v: 12\n", "\n", ending.text)))
			sameJSON(t, ending.name, c.Document(testDocURI+"#/x-v"), []byte(`12`))
			doc := strings.ReplaceAll(yamlHead+"x-a: {k: 1, k: 2}\n", "\n", ending.text)
			wantPosition(t, rejected(t, doc), testDocURI, 6, 13)
		})
	}
}

// Loader reads scalar keys as the strings they spell, and YAML 1.2.2 section
// 7.1 aliases an earlier anchored node, including a scalar key. An
// explicitly tagged scalar key is read as its spelling too; tags on values
// still undergo Core-schema validation.
func TestYAMLAdapterScalarKeyAliases(t *testing.T) {
	for _, tt := range []struct{ name, tail, json string }{
		{"key anchor used as value", "x-v: {&k name: first, other: *k}\n", `{"name":"first","other":"name"}`},
		{"value anchor used as key", "x-k: &k name\nx-v: {*k : first}\n", `{"name":"first"}`},
		{"tagged scalar key", "x-v: {!!timestamp 2001-12-14: first}\n", `{"2001-12-14":"first"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sameJSON(t, tt.name, yamlValue(t, tt.tail, "#/x-v"), []byte(tt.json))
		})
	}
	t.Run("duplicate spelled key", func(t *testing.T) {
		wantPosition(t, rejected(t, yamlHead+"x-v:\n  &k name: first\n  *k : second\n"), testDocURI, 8, 3)
	})
	t.Run("outside Core tag on value", func(t *testing.T) {
		wantPosition(t, rejected(t, yamlHead+"x-v:  !!timestamp 2001-12-14\n"), testDocURI, 6, 7)
	})
}
