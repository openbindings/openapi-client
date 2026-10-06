package openapi_test

import "testing"

// load.go, Loader: "YAML is parsed by go.yaml.in/yaml/v3, whose syntax rules
// apply"; "A rejection names the document's URI and where the problem is:
// for a YAML syntax error, the parser's own message, as it gives it". Each
// document breaks a rule of YAML 1.2.2's syntax, and its rejection names the
// document and carries the parser's message, whatever its wording.
func TestYAMLSyntaxErrorNamesURI(t *testing.T) {
	for _, tt := range []struct {
		name, tail string
	}{
		// Section 5.3: @ and ` are reserved indicators, so no token starts
		// with either.
		{"a reserved indicator @", "x-v:  @bad\n"},
		{"a reserved indicator `", "x-v: `bad\n"},
		// Section 5.7: an escape not listed there is an error.
		{"an unknown escape", "x-v: \"a\\qb\"\n"},
		// Section 6.1: tabs are not indentation.
		{"a tab indenting a sequence", "x-v:\n\t- a\n"},
		// Section 6.9.2: an anchor's name has at least one character.
		{"an empty anchor name", "x-v: & value\n"},
		// Section 7.1: an alias names an anchor, so it has a name too.
		{"an empty alias name", "x-a: &a value\nx-v: * \n"},
		// Section 7.3.1: a double-quoted scalar ends with a quote.
		{"a double-quoted scalar that does not end", "x-v: \"abc\n"},
		// Section 8.2.3: a block mapping that is a value begins on a line
		// after its key's.
		{"a block mapping on its key's line", "x-m: a: b\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantSyntaxError(t, rejected(t, yamlHead+tt.tail), testDocURI)
		})
	}
}
