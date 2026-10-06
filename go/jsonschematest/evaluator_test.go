package jsonschematest

import (
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// compile compiles a JSON Schema 2020-12 document given as text. The
// evaluator embeds the 2020-12 meta-schemas, so nothing is fetched.
func compile(t *testing.T, doc string) *jsonschema.Schema {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("urn:test:schema", v); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("urn:test:schema")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEvaluatorWorksOffline(t *testing.T) {
	s := compile(t, `{"type":["string","null"],"$defs":{"a":{"type":"integer"}}}`)
	for _, ok := range []string{`"x"`, `null`} {
		v, _ := jsonschema.UnmarshalJSON(strings.NewReader(ok))
		if err := s.Validate(v); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	v, _ := jsonschema.UnmarshalJSON(strings.NewReader(`1`))
	if s.Validate(v) == nil {
		t.Error("1 accepted by a string-or-null schema")
	}
}
