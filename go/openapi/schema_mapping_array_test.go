package openapi_test

import (
	"fmt"
	"testing"
)

// SchemaReference: a reference is "a value of a discriminator's mapping".
// A mapping written as an array is not a mapping (OAS 3.0.4 section
// 4.7.25.1 and 3.1.2 section 4.8.25.1: "Map[string, string]"), so its
// elements are not references and Schema.References reports none of them.
func TestSchemaReferencesMappingArray(t *testing.T) {
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		for _, yaml := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/yaml=%v", v, yaml), func(t *testing.T) {
				doc := `{"openapi":"` + v + `","info":{"title":"M","version":"1"},"paths":{},"components":{"schemas":{
					"A":{"oneOf":[{"$ref":"#/components/schemas/B"}],"discriminator":{"propertyName":"k","mapping":["#/components/schemas/B","B"]}},
					"B":{"type":"object"}}}}`
				c := verParse(t, doc, nil, yaml)
				s, err := c.Schema(verEntry + "#/components/schemas/A")
				if err != nil {
					t.Fatal(err)
				}
				refs, err := s.References()
				if err != nil {
					t.Fatal(err)
				}
				if len(refs) != 1 || refs[0].At != "/oneOf/0/$ref" {
					var ats []string
					for _, r := range refs {
						ats = append(ats, r.At)
					}
					t.Errorf("References at %q, want only /oneOf/0/$ref", ats)
				}
			})
		}
	}
}
