package openapi_test

import (
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/openbindings/openapi-client/go/openapi"
)

// load.go, Client.Document: "A YAML document's JSON has ... its strings as
// encoding/json writes them without HTML escaping". YAML 1.2.2 section 5.7
// writes U+0085, U+2028 and U+2029 in a double-quoted scalar as the escapes
// \N, \L and \P, or as \u escapes; whether the characters written as
// themselves end a line is the parser's syntax (section 5.4), so they are
// escaped here. Document writes them as encoding/json does, U+2028 and
// U+2029 as \u escapes.
func TestYAMLEscapedNonASCIIBreakCharacters(t *testing.T) {
	const content = "a\u0085b\u2028c\u2029d"
	each := "[" + jsonNoHTML(t, "\u0085") + "," + jsonNoHTML(t, "\u2028") + "," + jsonNoHTML(t, "\u2029") + "]"
	for _, tt := range []struct{ name, tail, want string }{
		{"named escapes", `x-v: "a\Nb\Lc\Pd"` + "\n", jsonNoHTML(t, content)},
		{"unicode escapes", `x-v: "a\u0085b\u2028c\u2029d"` + "\n", jsonNoHTML(t, content)},
		{"each alone", `x-v: ["\N", "\L", "\P"]` + "\n", each},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(yamlValue(t, tt.tail, "#/x-v")); got != tt.want {
				t.Errorf("Document = %s, want %s", got, tt.want)
			}
		})
	}
}

// load.go, Loader: a rejection that is not a YAML syntax error names "the
// line and column, both counted from 1, the column in the document's own
// bytes (two per UTF-16 code unit, four per UTF-32 character) ..., a node's
// position being where it starts". YAML 1.2.2 section 5.4 makes U+0085,
// U+2028 and U+2029 content, while YAML 1.1 made them line breaks; which
// the parser does is its syntax ("YAML is parsed by go.yaml.in/yaml/v3,
// whose syntax rules apply"). A quoted scalar holding one is read either
// way, and a duplicate key after it is rejected where the key starts: on
// line 6 if the character does not end a line, or on line 7 if it does,
// the column counting the document's own bytes from that line's start.
func TestYAMLPositionAfterNonASCIIBreakCharacters(t *testing.T) {
	const after = `b", {k: 1, ` // the text before the second key, after the character
	for _, tt := range []struct {
		name, char, open string
		utf16            bool
	}{
		{"NEL, double quoted", "\u0085", `"`, false},
		{"LS, single quoted", "\u2028", `'`, false},
		{"PS, double quoted", "\u2029", `"`, false},
		{"NEL, double quoted, UTF-16", "\u0085", `"`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := "x-v: [" + tt.open + "a" + tt.char // line 6 before the character
			rest := strings.ReplaceAll(after, `"`, tt.open)
			doc := yamlHead + before + rest + "k: 2}]\n"
			sameLine, nextLine := len(before+rest)+1, len(rest)+1
			content := []byte(doc)
			if tt.utf16 {
				content = utf16Text(doc, false, true)
				sameLine = 2*len(utf16.Encode([]rune(before+rest))) + 1
				nextLine = 2*len(rest) + 1
			}
			_, err := openapi.Parse(t.Context(), content, testDocURI, nil)
			wantOnePosition(t, err, testDocURI, [2]int{6, sameLine}, [2]int{7, nextLine})
		})
	}
}

// wantOnePosition checks that err names uri and, as whole numbers, the line
// and column of one of the positions.
func wantOnePosition(t testing.TB, err error, uri string, positions ...[2]int) {
	t.Helper()
	if err == nil {
		t.Errorf("no error, want a rejection at one of %v", positions)
		return
	}
	msg := err.Error()
	if !strings.Contains(msg, uri) {
		t.Errorf("rejection %q does not name the document %q", msg, uri)
	}
	for _, p := range positions {
		if numberRE(p[0]).MatchString(msg) && numberRE(p[1]).MatchString(msg) {
			return
		}
	}
	t.Errorf("rejection %q names none of the lines and columns %v", msg, positions)
}

// Loader reads valid 1.x directives as YAML 1.2. YAML 1.2.2 section 6.8.1
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
