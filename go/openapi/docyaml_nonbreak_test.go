package openapi_test

import (
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Loader reads YAML 1.2, including when a 1.x directive is present (SQ8).
// YAML 1.2.2 section 5.4 makes U+0085, U+2028 and U+2029 non-break
// characters, so they remain scalar content rather than folding or
// ending a line: https://yaml.org/spec/1.2.2/#54-line-break-characters.
func TestYAMLNonASCIICharactersAreNotLineBreaks(t *testing.T) {
	const content = "a\u0085b\u2028c\u2029d"
	for _, tt := range []struct{ name, tail, value string }{
		{"double quoted NEL", "x-v: \"a\u0085b\"\n", "a\u0085b"},
		{"double quoted line separator", "x-v: \"a\u2028b\"\n", "a\u2028b"},
		{"double quoted paragraph separator", "x-v: \"a\u2029b\"\n", "a\u2029b"},
		{"single quoted", "x-v: '" + content + "'\n", content},
		{"plain", "x-v: " + content + "\n", content},
		{"literal block", "x-v: |-\n  " + content + "\n", content},
		{"folded block", "x-v: >-\n  " + content + "\n  next\n", content + " next"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// SQ10 pins exact encoding/json string bytes without HTML
			// escaping, including the escapes for U+2028 and U+2029.
			if got, want := string(yamlValue(t, tt.tail, "#/x-v")), jsonNoHTML(t, tt.value); got != want {
				t.Errorf("Document scalar = %q, want %q", got, want)
			}
		})
	}
	t.Run("version 1.1 directive", func(t *testing.T) {
		c := parsed(t, []byte("%YAML 1.1\n---\n"+yamlHead+"x-v: \""+content+"\"\n"))
		if got, want := string(c.Document(testDocURI+"#/x-v")), jsonNoHTML(t, content); got != want {
			t.Errorf("Document scalar = %q, want %q", got, want)
		}
	})

	// Loader's positions use the document's own bytes. These characters
	// neither advance the source line nor reset the byte column, for a
	// scanner error or a semantic rejection after a successfully read node.
	const scannerPrefix = "x-v: [\"" + content + "\", "
	for _, tt := range []struct {
		name, doc string
		line, col int
	}{
		{"scanner after quoted content", yamlHead + scannerPrefix + "@bad]\n", 6, len(scannerPrefix) + 1},
		{"duplicate after plain content", yamlHead + "x-v: {s: " + content + ", k: 1, k: 2}\n", 6, len("x-v: {s: "+content+", k: 1, ") + 1},
		{"duplicate after block content", yamlHead + "x-v: |-\n  " + content + "\nx-dupe: {k: 1, k: 2}\n", 8, len("x-dupe: {k: 1, ") + 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantPosition(t, rejected(t, tt.doc), testDocURI, tt.line, tt.col)
		})
	}
	t.Run("UTF-16 scanner column", func(t *testing.T) {
		doc := utf16Text(yamlHead+scannerPrefix+"@bad]\n", false, true)
		_, err := openapi.Parse(t.Context(), doc, testDocURI, nil)
		wantPosition(t, err, testDocURI, 6, 2*len([]rune(scannerPrefix))+1)
	})
}

// SQ8 accepts valid 1.x directives as YAML 1.2. YAML 1.2.2 section 6.8.1
// permits one or more decimal digits in each version component, with no
// two-digit limit; section 6.6 requires whitespace before a comment.
func TestYAMLDirectiveLongMinor(t *testing.T) {
	t.Run("valid minor", func(t *testing.T) {
		c := parsed(t, []byte("%YAML 1.123\n---\n"+yamlHead+"x-v: yes\n"))
		sameJSON(t, "valid long minor", c.Document(testDocURI+"#/x-v"), []byte(`"yes"`))
	})
	t.Run("comment needs separation", func(t *testing.T) {
		rejected(t, "%YAML 1.123#bad\n---\n"+yamlHead)
	})
}
