package schema2020_test

import (
	"testing"

	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// A property or allOf member written as an array is no schema, but the
// Loader accepts it; Project, which takes any schema of c and documents no
// panic, must not panic looking up keywords in it.
func TestProjectArrayWhereSchemaExpected(t *testing.T) {
	for _, tc := range []struct{ doc, at string }{
		{`{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{"A":{"type":"object","required":["a"],"properties":{"a":[1,2]}}}}}`, "#/components/schemas/A"},
		{`{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{"A":{"type":"object","required":["a"],"allOf":[[1]],"properties":{"a":{}}}}}}`, "#/components/schemas/A"},
		{`{"swagger":"2.0","info":{"title":"t","version":"1"},"paths":{},"definitions":{"A":{"type":"object","properties":{"a":[1,2]}}}}`, "#/definitions/A"},
	} {
		c := parseSynthetic(t, tc.doc)
		s, err := c.Schema(entryURI + tc.at)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range directions {
			schema2020.Project(c, s, d)
		}
	}
}

// An allOf cycle gives every object in it the names of the whole cycle. The
// projection is compared as JSON values, not bytes, since Project promises no
// member order.
func TestProjectAllOfCycleFlags(t *testing.T) {
	c := parseSynthetic(t, `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{
"A":{"allOf":[{"$ref":"#/components/schemas/B"}],"required":["a","b","x"],"properties":{"a":{"readOnly":true}}},
"B":{"allOf":[{"$ref":"#/components/schemas/A"}],"required":["a","b","y"],"properties":{"b":{"readOnly":true}}}}}}`)
	s, err := c.Schema(entryURI + "#/components/schemas/A")
	if err != nil {
		t.Fatal(err)
	}
	p, err := schema2020.Project(c, s, schema2020.Request)
	if err != nil {
		t.Fatal(err)
	}
	root := `{"allOf":[{"$ref":"#/$defs/B"}],"required":["x"],
		"properties":{"a":{"readOnly":true}}}`
	b := `{"allOf":[{"$ref":"#/$defs/A"}],"required":["y"],
		"properties":{"b":{"readOnly":true}}}`
	sameSchema(t, "Root", p.Root, mustDecode(t, "want", []byte(root)))
	sameSchema(t, "Defs[B]", p.Defs["B"], mustDecode(t, "want", []byte(b)))
}
