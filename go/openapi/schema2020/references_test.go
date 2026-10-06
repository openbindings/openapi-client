package schema2020_test

import (
	"testing"

	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// Reference closure. Project: "Root is the schema itself; Defs holds every
// schema it reaches by reference, transitively, and nothing else. Every
// reference that
// openapi.Schema.References reports, discriminator mapping and
// defaultMapping values included, is rewritten to #/$defs/KEY for its
// target." Projection: "Defs contains the complete transitive closure."

// Defs holds what Root reaches, directly and through other Defs entries,
// and nothing it does not reach.
func TestProjectClosure(t *testing.T) {
	schemas := `
		"R":{"type":"object","properties":{"a":{"$ref":"#C/A"}}},
		"A":{"type":"object","properties":{"b":{"$ref":"#C/B"},"c":{"type":"array","items":{"$ref":"#C/C"}}},"additionalProperties":{"$ref":"#C/B"}},
		"B":{"type":"string","maxLength":3},
		"C":{"allOf":[{"$ref":"#C/B"},{"minLength":1}]},
		"Unrelated":{"$ref":"#C/B"}`
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				want{
					root: ed(v, `{"type":"object","properties":{"a":{"$ref":"=>#C/A"}}}`),
					defs: map[string]string{
						ed(v, "#C/A"): ed(v, `{"type":"object","properties":{"b":{"$ref":"=>#C/B"},"c":{"type":"array","items":{"$ref":"=>#C/C"}}},"additionalProperties":{"$ref":"=>#C/B"}}`),
						ed(v, "#C/B"): `{"type":"string","maxLength":3}`,
						ed(v, "#C/C"): ed(v, `{"allOf":[{"$ref":"=>#C/B"},{"minLength":1}]}`),
					},
				}.check(t, c, p)
			})
		}
	}
}

// A schema that refers to itself is both Root and the Defs entry its
// references name; a cycle through other schemas closes in Defs.
func TestProjectSelfReferenceAndCycles(t *testing.T) {
	schemas := `
		"Node":{"type":"object","properties":{"next":{"$ref":"#C/Node"},"kids":{"type":"array","items":{"$ref":"#C/Node"}}}},
		"A":{"type":"object","properties":{"b":{"$ref":"#C/B"}}},
		"B":{"type":"object","properties":{"a":{"$ref":"#C/A"}}}`
	node := `{"type":"object","properties":{"next":{"$ref":"=>#C/Node"},"kids":{"type":"array","items":{"$ref":"=>#C/Node"}}}}`
	a := `{"type":"object","properties":{"b":{"$ref":"=>#C/B"}}}`
	b := `{"type":"object","properties":{"a":{"$ref":"=>#C/A"}}}`
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				for _, d := range directions {
					p := project(t, c, comp(t, c, v, "Node"), d)
					want{root: ed(v, node), defs: map[string]string{ed(v, "#C/Node"): ed(v, node)}}.check(t, c, p)
					if p.Sources["Node"] != comp(t, c, v, "Node").Source() {
						t.Errorf("Sources[Node] = %q", p.Sources["Node"])
					}
					p = project(t, c, comp(t, c, v, "A"), d)
					want{root: ed(v, a), defs: map[string]string{ed(v, "#C/A"): ed(v, a), ed(v, "#C/B"): ed(v, b)}}.check(t, c, p)
				}
			})
		}
	}
}

// Schema: "In OpenAPI 3.1 and 3.2 it stays at that site, whether or not a
// $ref there has siblings. In Swagger 2.0 and OpenAPI 3.0 ... it follows
// $ref to a schema that is not only a $ref". So a descriptor whose schema
// is a $ref projects to a Root that refers to the component in 3.1 and
// 3.2, and to the component itself in 2.0 and 3.0; a component that is
// only a $ref is its own Defs entry in 3.1 and 3.2 and is passed through in
// 2.0 and 3.0.
func TestProjectReferenceAtRoot(t *testing.T) {
	schemas := `"Pet":{"type":"object","properties":{"tag":{"$ref":"#C/Alias"}}},"Alias":{"$ref":"#C/Tag"},"Tag":{"type":"string"}`
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				var paths string
				if v == v20 {
					paths = `"/pets":{"get":{"produces":["application/json"],"responses":{"200":{"description":"ok","schema":{"$ref":"#C/Pet"}}}}}`
				} else {
					paths = `"/pets":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#C/Pet"}}}}}}}`
				}
				c := load(t, f, docText(v, paths, schemas), nil)
				op, err := c.Operation("GET /pets")
				if err != nil {
					t.Fatal(err)
				}
				s := op.Responses[0].Media[0].Schema
				p := project(t, c, s, schema2020.Neutral)
				if isModern(v) {
					want{
						root: ed(v, `{"$ref":"=>#C/Pet"}`),
						defs: map[string]string{
							ed(v, "#C/Pet"):   ed(v, `{"type":"object","properties":{"tag":{"$ref":"=>#C/Alias"}}}`),
							ed(v, "#C/Alias"): ed(v, `{"$ref":"=>#C/Tag"}`),
							ed(v, "#C/Tag"):   `{"type":"string"}`,
						},
					}.check(t, c, p)
					return
				}
				if s.Source() != comp(t, c, v, "Pet").Source() {
					t.Fatalf("descriptor schema Source %s, want the component it refers to", s.Source())
				}
				want{
					root: ed(v, `{"type":"object","properties":{"tag":{"$ref":"=>#C/Tag"}}}`),
					defs: map[string]string{ed(v, "#C/Tag"): `{"type":"string"}`},
				}.check(t, c, p)
			})
		}
	}
}

// References into other documents resolve against each document's own
// base, and the targets join Defs wherever they are written, a component
// of the entry document reached from another document included.
func TestProjectReferencesAcrossDocuments(t *testing.T) {
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				others := map[string]string{
					modelsURI: `{"Pet":{"type":"object","properties":{"tag":{"$ref":"#/Tag"},"err":{"$ref":"../shared/common.json#/Err"}}},"Tag":{"type":"string"}}`,
					commonURI: ed(v, `{"Err":{"type":"object","properties":{"code":{"$ref":"../v1/openapi.json#C/Code"}}}}`),
				}
				c := load(t, f, docText(v, "", `"R":{"properties":{"pet":{"$ref":"models.json#/Pet"}}},"Code":{"type":"integer"}`), others)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				want{
					root: `{"properties":{"pet":{"$ref":"=>models.json#/Pet"}}}`,
					defs: map[string]string{
						"models.json#/Pet":           `{"type":"object","properties":{"tag":{"$ref":"=>models.json#/Tag"},"err":{"$ref":"=>../shared/common.json#/Err"}}}`,
						"models.json#/Tag":           `{"type":"string"}`,
						"../shared/common.json#/Err": ed(v, `{"type":"object","properties":{"code":{"$ref":"=>#C/Code"}}}`),
						ed(v, "#C/Code"):             `{"type":"integer"}`,
					},
				}.check(t, c, p)
				if _, ok := p.Defs["Code"]; !ok {
					t.Errorf("the entry's component Code is not keyed \"Code\": %v", p.Sources)
				}
			})
		}
	}
}

// Schemas are read under their own document's edition (Project: "Schemas
// are read under their openapi.Schema.Version"): an OpenAPI 3.1 schema
// referring to an OpenAPI 3.0 one, and the reverse, each translated by its
// own edition's rules within one Projection.
func TestProjectMixedEditions(t *testing.T) {
	v30doc := `{"openapi":"3.0.3","info":{"title":"o","version":"1"},"paths":{},"components":{"schemas":{
		"N":{"type":"string","nullable":true,"maxLength":4}}}}`
	v31doc := `{"openapi":"3.1.0","info":{"title":"o","version":"1"},"paths":{},"components":{"schemas":{
		"M":{"type":["string","null"],"format":"binary","nullable":true}}}}`
	others := map[string]string{
		"https://api.example.test/v1/v30.json": v30doc,
		"https://api.example.test/v1/v31.json": v31doc,
	}
	for _, f := range formats {
		t.Run(f, func(t *testing.T) {
			c := load(t, f, docText(v31, "", `"R":{"properties":{"n":{"$ref":"v30.json#/components/schemas/N"},"x":{"type":"string","nullable":true}}}`), others)
			p := project(t, c, comp(t, c, v31, "R"), schema2020.Neutral)
			want{
				root: `{"properties":{"n":{"$ref":"=>v30.json#/components/schemas/N"},"x":{"type":"string","nullable":true}}}`,
				defs: map[string]string{"v30.json#/components/schemas/N": `{"type":["string","null"],"maxLength":4}`},
			}.check(t, c, p)

			c = load(t, f, docText(v30, "", `"R":{"properties":{"m":{"$ref":"v31.json#/components/schemas/M"},"x":{"type":"string","nullable":true}}}`), others)
			p = project(t, c, comp(t, c, v30, "R"), schema2020.Neutral)
			want{
				root: `{"properties":{"m":{"$ref":"=>v31.json#/components/schemas/M"},"x":{"type":["string","null"]}}}`,
				defs: map[string]string{"v31.json#/components/schemas/M": `{"type":["string","null"],"format":"binary","nullable":true}`},
			}.check(t, c, p)
		})
	}
}

// Nested schema resources (JSON Schema 2020-12 Core section 8.2): $id
// changes the base of the references beneath it, $anchor names a schema,
// and a referenced nested $defs entry becomes a Defs entry while an
// unreferenced one is dropped (Project). Every target is keyed by its
// Source, the physical location, whatever identifier reached it.
func TestProjectNestedResources(t *testing.T) {
	const rID = "https://api.example.test/schemas/r.json"
	schemas := `
		"R":{"$id":"` + rID + `","$anchor":"top","type":"object",
			"$defs":{"inner":{"$anchor":"in","type":"string"},"unused":{"type":"null"}},
			"properties":{
				"b":{"$ref":"#in"},
				"c":{"$ref":"#/$defs/inner"},
				"d":{"$ref":"#top"},
				"e":{"$id":"e.json","$defs":{"x":{"type":"integer"}},"type":"array","items":{"$ref":"#/$defs/x"}}}},
		"S":{"properties":{"byID":{"$ref":"` + rID + `"},"byAnchor":{"$ref":"` + rID + `#top"},"nested":{"$ref":"https://api.example.test/schemas/e.json#/$defs/x"}}}`
	inner := "#/components/schemas/R/$defs/inner"
	x := "#/components/schemas/R/properties/e/$defs/x"
	root := `{"type":"object","properties":{
		"b":{"$ref":"=>` + inner + `"},
		"c":{"$ref":"=>` + inner + `"},
		"d":{"$ref":"=>#/components/schemas/R"},
		"e":{"type":"array","items":{"$ref":"=>` + x + `"}}}}`
	for _, v := range modernEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				want{root: root, defs: map[string]string{
					inner:                    `{"type":"string"}`,
					x:                        `{"type":"integer"}`,
					"#/components/schemas/R": root,
				}}.check(t, c, p)
				for _, k := range []string{inner, x, "R"} {
					if _, ok := p.Defs[k]; !ok {
						t.Errorf("Defs has no key %q: %v", k, p.Sources)
					}
				}
				p = project(t, c, comp(t, c, v, "S"), schema2020.Neutral)
				want{
					root: `{"properties":{"byID":{"$ref":"=>#/components/schemas/R"},"byAnchor":{"$ref":"=>#/components/schemas/R"},"nested":{"$ref":"=>` + x + `"}}}`,
					defs: map[string]string{inner: `{"type":"string"}`, x: `{"type":"integer"}`, "#/components/schemas/R": root},
				}.check(t, c, p)
			})
		}
	}
}

// A plain-name fragment reaches a schema by its $anchor or $dynamicAnchor
// (Core section 8.2.2); the target is keyed by where it is written and
// carries neither keyword.
func TestProjectAnchors(t *testing.T) {
	schemas := `
		"R":{"properties":{"p":{"$ref":"#pet"},"q":{"$ref":"#C/Tree"}}},
		"Pet":{"$anchor":"pet","type":"object","title":"pet"},
		"Tree":{"$dynamicAnchor":"tree","type":"object","properties":{"kid":{"$ref":"#C/Tree"}}}`
	for _, v := range modernEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				edAll(v, want{
					root: `{"properties":{"p":{"$ref":"=>#C/Pet"},"q":{"$ref":"=>#C/Tree"}}}`,
					defs: map[string]string{
						"#/components/schemas/Pet":  `{"type":"object","title":"pet"}`,
						"#/components/schemas/Tree": `{"type":"object","properties":{"kid":{"$ref":"=>#/components/schemas/Tree"}}}`,
					},
				}).check(t, c, p)
				if _, ok := p.Defs["Pet"]; !ok {
					t.Errorf("a component reached by $anchor is not keyed by its name: %v", p.Sources)
				}
			})
		}
	}
}

// Discriminator mapping values, by component name, by fragment and by
// relative or absolute URI, and the OpenAPI 3.2 defaultMapping, are
// references (SchemaReference): each is rewritten and its target joins
// Defs even when nothing else refers to it. The discriminator's other
// fields are kept, an implicit mapping adds nothing, and a Swagger 2.0
// discriminator, a property name, is unchanged.
func TestProjectDiscriminator(t *testing.T) {
	schemas := `
		"Pet":{"oneOf":[{"$ref":"#C/Cat"},{"$ref":"#C/Dog"}],
			"discriminator":{"propertyName":"kind","x-note":"kept","mapping":{
				"cat":"Cat","dog":"#C/Dog","bird":"models.json#/Bird","lizard":"` + entryURI + `#C/Lizard"}}},
		"Implicit":{"oneOf":[{"$ref":"#C/Cat"}],"discriminator":{"propertyName":"kind"}},
		"Cat":{"type":"object","title":"cat"},
		"Dog":{"type":"object","title":"dog"},
		"Lizard":{"type":"object","title":"lizard"}`
	others := map[string]string{modelsURI: `{"Bird":{"type":"object","title":"bird"}}`}
	for _, v := range []string{v30, v31, v32} {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), others)
				p := project(t, c, comp(t, c, v, "Pet"), schema2020.Neutral)
				edAll(v, want{
					root: `{"oneOf":[{"$ref":"=>#C/Cat"},{"$ref":"=>#C/Dog"}],
						"discriminator":{"propertyName":"kind","x-note":"kept","mapping":{
							"cat":"=>#C/Cat","dog":"=>#C/Dog","bird":"=>models.json#/Bird","lizard":"=>#C/Lizard"}}}`,
					defs: map[string]string{
						"#C/Cat":            `{"type":"object","title":"cat"}`,
						"#C/Dog":            `{"type":"object","title":"dog"}`,
						"#C/Lizard":         `{"type":"object","title":"lizard"}`,
						"models.json#/Bird": `{"type":"object","title":"bird"}`,
					},
				}).check(t, c, p)
				p = project(t, c, comp(t, c, v, "Implicit"), schema2020.Neutral)
				edAll(v, want{
					root: `{"oneOf":[{"$ref":"=>#C/Cat"}],"discriminator":{"propertyName":"kind"}}`,
					defs: map[string]string{"#C/Cat": `{"type":"object","title":"cat"}`},
				}).check(t, c, p)
			})
		}
	}
	t.Run("3.2 defaultMapping", func(t *testing.T) {
		for _, f := range formats {
			c := load(t, f, docText(v32, "", `
				"Pet":{"oneOf":[{"$ref":"#C/Cat"}],"discriminator":{"propertyName":"kind","mapping":{"cat":"Cat"},"defaultMapping":"Other"}},
				"Byuri":{"discriminator":{"propertyName":"kind","defaultMapping":"models.json#/Bird"}},
				"Cat":{"type":"object"},"Other":{"type":"object","title":"other"}`), others)
			p := project(t, c, comp(t, c, v32, "Pet"), schema2020.Neutral)
			edAll(v32, want{
				root: `{"oneOf":[{"$ref":"=>#C/Cat"}],"discriminator":{"propertyName":"kind","mapping":{"cat":"=>#C/Cat"},"defaultMapping":"=>#C/Other"}}`,
				defs: map[string]string{"#C/Cat": `{"type":"object"}`, "#C/Other": `{"type":"object","title":"other"}`},
			}).check(t, c, p)
			p = project(t, c, comp(t, c, v32, "Byuri"), schema2020.Neutral)
			want{
				root: `{"discriminator":{"propertyName":"kind","defaultMapping":"=>models.json#/Bird"}}`,
				defs: map[string]string{"models.json#/Bird": `{"type":"object","title":"bird"}`},
			}.check(t, c, p)
		}
	})
	t.Run("2.0", func(t *testing.T) {
		for _, f := range formats {
			c := load(t, f, docText(v20, "", `"Pet":{"type":"object","discriminator":"kind","required":["kind"],"properties":{"kind":{"type":"string"}}},
				"Cat":{"allOf":[{"$ref":"#C/Pet"},{"properties":{"purrs":{"type":"boolean"}}}]}`), nil)
			p := project(t, c, comp(t, c, v20, "Cat"), schema2020.Neutral)
			want{
				root: `{"allOf":[{"$ref":"=>#/definitions/Pet"},{"properties":{"purrs":{"type":"boolean"}}}]}`,
				defs: map[string]string{"#/definitions/Pet": `{"type":"object","discriminator":"kind","required":["kind"],"properties":{"kind":{"type":"string"}}}`},
			}.check(t, c, p)
		}
	})
}

// edAll applies ed to every "#C/" in a want, for fixtures shared across
// editions.
func edAll(v string, w want) want {
	out := want{root: ed(v, w.root), defs: map[string]string{}}
	for k, d := range w.defs {
		out.defs[ed(v, k)] = ed(v, d)
	}
	return out
}

// A discriminator mapping written as an array is not a mapping (OAS 3.0.4
// section 4.7.25.1 and 3.1.2 section 4.8.25.1: "Map[string, string]"), so
// Schema.References reports nothing from it and Project keeps it as
// authored, without an Issue, while the other references are rewritten.
func TestProjectDiscriminatorMappingArray(t *testing.T) {
	schemas := `"A":{"oneOf":[{"$ref":"#C/B"}],"discriminator":{"propertyName":"k","mapping":["#C/B","B"]}},"B":{"type":"object"}`
	for _, v := range []string{v30, v31, v32} {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				p := project(t, c, comp(t, c, v, "A"), schema2020.Neutral)
				edAll(v, want{
					root: `{"oneOf":[{"$ref":"=>#C/B"}],"discriminator":{"propertyName":"k","mapping":["#C/B","B"]}}`,
					defs: map[string]string{"#C/B": `{"type":"object"}`},
				}).check(t, c, p)
			})
		}
	}
}
