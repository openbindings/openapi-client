package schema2020_test

import (
	"testing"

	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// Identifiers. Project: "No emitted schema carries $id, $schema, $anchor,
// $dynamicAnchor or $defs: a referenced nested $defs entry becomes a Defs
// entry, and an unreferenced one is dropped." Projection: "Root has no
// $schema or $defs".

// Every identifier keyword is removed from every schema, at Root and
// nested, in a supported dialect, while other keywords such as $comment
// stay.
func TestProjectRemovesIdentifiers(t *testing.T) {
	for _, v := range modernEditions {
		for _, dialect := range []string{dialect2020, dialectOAS31, dialectOAS32} {
			for _, f := range formats {
				t.Run(v+"/"+dialect+"/"+f, func(t *testing.T) {
					schemas := `"R":{"$schema":"` + dialect + `","$id":"https://api.example.test/schemas/r2.json","$anchor":"r","$dynamicAnchor":"meta",
						"$comment":"kept","$defs":{"unused":{"type":"null"}},"type":"object",
						"properties":{"n":{"$id":"n.json","$schema":"` + dialect + `","$anchor":"n","$defs":{"z":{}},"type":"integer"}}}`
					c := load(t, f, docText(v, "", schemas), nil)
					for _, d := range directions {
						p := project(t, c, comp(t, c, v, "R"), d)
						want{root: `{"$comment":"kept","type":"object","properties":{"n":{"type":"integer"}}}`}.check(t, c, p)
					}
				})
			}
		}
	}
}

// Identifier names are keywords only where a schema is: a property named
// $id, an example or default value holding one, and an extension's value
// are kept as written (JSON Schema 2020-12 Core section 9.4.2: instance
// data is not a schema).
func TestProjectIdentifierNamesAsData(t *testing.T) {
	schema := `{"type":"object","required":["$id"],
		"properties":{"$id":{"type":"string"},"$schema":{"type":"string"},"$defs":{"type":"object"},"$anchor":{"type":"string"},"$dynamicAnchor":{"type":"string"}},
		"default":{"$schema":"y","$defs":{"a":{}}},
		"enum":[{"$anchor":"z"},{"$id":"x"}],
		"example":{"$id":"e"},
		"x-ext":{"$id":"w","$defs":{"b":{}}}}`
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", `"R":`+schema), nil)
				for _, d := range []schema2020.Direction{schema2020.Neutral, schema2020.Response} {
					p := project(t, c, comp(t, c, v, "R"), d)
					want{root: schema}.check(t, c, p)
				}
			})
		}
	}
}

// A nested $defs entry that is referenced, by a JSON Pointer through an
// enclosing resource's $id or through the document, becomes a Defs entry
// keyed by its Source; the $defs keyword itself is gone, with the entries
// nothing refers to.
func TestProjectNestedDefs(t *testing.T) {
	schemas := `
		"R":{"$id":"https://api.example.test/schemas/defs.json",
			"$defs":{"a":{"type":"string"},"b":{"$ref":"#/$defs/a"},"c":{"type":"boolean"}},
			"properties":{"x":{"$ref":"#/$defs/b"}}},
		"S":{"$defs":{"z":{"type":"integer"},"unused":{"type":"null"}},"properties":{"y":{"$ref":"#/components/schemas/S/$defs/z"}}}`
	for _, v := range modernEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				a, b := "#/components/schemas/R/$defs/a", "#/components/schemas/R/$defs/b"
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				want{
					root: `{"properties":{"x":{"$ref":"=>` + b + `"}}}`,
					defs: map[string]string{b: `{"$ref":"=>` + a + `"}`, a: `{"type":"string"}`},
				}.check(t, c, p)
				for _, k := range []string{a, b} {
					if _, ok := p.Defs[k]; !ok {
						t.Errorf("Defs has no key %q: %v", k, p.Sources)
					}
				}
				z := "#/components/schemas/S/$defs/z"
				p = project(t, c, comp(t, c, v, "S"), schema2020.Neutral)
				want{root: `{"properties":{"y":{"$ref":"=>` + z + `"}}}`, defs: map[string]string{z: `{"type":"integer"}`}}.check(t, c, p)
			})
		}
	}
}

// In Swagger 2.0 and OpenAPI 3.0 these names are not keywords of the
// edition, but Project's rule covers every emitted schema, so they are
// removed there too, and nothing they would identify is followed.
func TestProjectIdentifierKeywordsInLegacyEditions(t *testing.T) {
	for _, v := range legacyEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				schemas := `"R":{"type":"object","$id":"r.json","$schema":"` + dialectDraft7 + `","$anchor":"r","$dynamicAnchor":"d","title":"t",
					"$defs":{"a":{"type":"string"}},
					"properties":{"p":{"type":"string","$id":"p.json"}}}`
				c := load(t, f, docText(v, "", schemas), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				want{root: `{"type":"object","title":"t","properties":{"p":{"type":"string"}}}`}.check(t, c, p)
			})
		}
	}
}
