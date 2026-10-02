package openapi_test

import "testing"

// YAML 1.2.2 sections 6.9.2 and 7.1 define a nonempty anchor name from
// non-space characters other than the five flow indicators []{},. Names
// are not restricted to ASCII letters and digits. Space separates each
// name from the following node or line ending, including colon names.
func TestYAMLAnchorNameGrammar(t *testing.T) {
	for _, name := range []string{"café", "猫", "😀", "a.b", "a/b", "a:b", "a?b", "a#b", "a!b", "a@b", "a%b"} {
		t.Run(name, func(t *testing.T) {
			doc := yamlHead + "x-a: &" + name + " value\nx-v: *" + name + " \n"
			c := parsed(t, []byte(doc))
			sameJSON(t, "anchor alias", c.Document(testDocURI+"#/x-v"), []byte(`"value"`))
		})
	}
	for _, tt := range []struct{ name, tail string }{
		{"empty anchor", "x-v: & value\n"},
		{"empty alias", "x-a: &a value\nx-v: * \n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rejected(t, yamlHead+tt.tail)
		})
	}
}

// YAML 1.2.2 section 6.9.1, example 6.25: the verbatim tag !<!> is
// invalid because verbatim tags are not resolved. It is not the explicit
// non-specific tag !, which section 10.3.2 resolves to a scalar string.
func TestYAMLVerbatimBangIsNotNonSpecific(t *testing.T) {
	t.Run("invalid verbatim tag", func(t *testing.T) {
		rejected(t, yamlHead+"x-v: !<!> foo\n")
	})
	t.Run("valid non-specific tag", func(t *testing.T) {
		sameJSON(t, "non-specific tag", yamlValue(t, "x-v: ! foo\n", "#/x-v"), []byte(`"foo"`))
	})
}
