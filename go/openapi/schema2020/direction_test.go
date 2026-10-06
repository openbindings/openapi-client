package schema2020_test

import (
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// Directions. Project: "In OpenAPI 3.1 and 3.2, readOnly and writeOnly
// are annotations, so every direction gives the same output, which equals
// the authored schema apart from the rewritten references and the removed
// identifiers. In OpenAPI 3.0, a Request projection removes each readOnly
// property from required, and a Response projection each writeOnly one. In
// Swagger 2.0, whose readOnly properties must not be sent, a Request
// projection removes each readOnly property from required and replaces its
// schema with false. A required list left empty is removed. A property is
// readOnly or writeOnly when any declaration of it says so at the root of
// its schema, after following $ref; its declarations are those in the
// object's properties and in the properties of every schema the object
// reaches through allOf."
//
// OAS 3.0.4 section 4.7.24.2 (readOnly: "SHOULD NOT be sent as part of the
// request ... the required will take effect on the response only"; and
// writeOnly the reverse); OAS 2.0 section 6.4.18.1 (readOnly: "MUST NOT be
// sent as part of the request"); OAS 3.1.2 section 4.8.24.3.2 and 3.2.1
// section 4.24.5.2 (both are annotations, which "differs from that
// specified by version 3.0").

const directionSchemas = `
	"Account":{"type":"object","required":["id","name","secret","kind"],"properties":{
		"id":{"type":"string","readOnly":true},
		"name":{"type":"string"},
		"secret":{"type":"string","writeOnly":true},
		"kind":{"$ref":"#C/ReadOnlyKind"}}},
	"ReadOnlyKind":{"type":"string","readOnly":true}`

// In OpenAPI 3.0 a Request projection drops readOnly names from required
// and a Response projection drops writeOnly ones; the properties and their
// annotations stay, as "SHOULD NOT be sent" does not forbid them. A
// property whose schema is a $ref to a readOnly schema is readOnly.
func TestProjectDirection30(t *testing.T) {
	props := `"properties":{"id":{"type":"string","readOnly":true},"name":{"type":"string"},"secret":{"type":"string","writeOnly":true},"kind":{"$ref":"=>#C/ReadOnlyKind"}}`
	cases := map[schema2020.Direction]string{
		schema2020.Neutral:  `{"type":"object","required":["id","name","secret","kind"],` + props + `}`,
		schema2020.Request:  `{"type":"object","required":["name","secret"],` + props + `}`,
		schema2020.Response: `{"type":"object","required":["id","name","kind"],` + props + `}`,
	}
	for _, f := range formats {
		c := load(t, f, docText(v30, "", directionSchemas), nil)
		for d, root := range cases {
			t.Run(f+"/"+dirName(d), func(t *testing.T) {
				p := project(t, c, comp(t, c, v30, "Account"), d)
				edAll(v30, want{root: root, defs: map[string]string{"#C/ReadOnlyKind": `{"type":"string","readOnly":true}`}}).check(t, c, p)
			})
		}
	}
}

// In Swagger 2.0 a Request projection also replaces each readOnly
// property's schema with false, through $ref too, so nothing it referred
// to remains in Defs. Swagger 2.0 has no writeOnly, so a Response
// projection is the Neutral one, whatever an unknown writeOnly field says.
func TestProjectDirection20(t *testing.T) {
	neutral := `{"type":"object","required":["id","name","secret","kind"],"properties":{
		"id":{"type":"string","readOnly":true},"name":{"type":"string"},"secret":{"type":"string","writeOnly":true},"kind":{"$ref":"=>#C/ReadOnlyKind"}}}`
	for _, f := range formats {
		c := load(t, f, docText(v20, "", directionSchemas), nil)
		kind := map[string]string{"#C/ReadOnlyKind": `{"type":"string","readOnly":true}`}
		for d, w := range map[schema2020.Direction]want{
			schema2020.Neutral:  {root: neutral, defs: kind},
			schema2020.Response: {root: neutral, defs: kind},
			schema2020.Request: {root: `{"type":"object","required":["name","secret"],"properties":{
				"id":false,"name":{"type":"string"},"secret":{"type":"string","writeOnly":true},"kind":false}}`},
		} {
			t.Run(f+"/"+dirName(d), func(t *testing.T) {
				p := project(t, c, comp(t, c, v20, "Account"), d)
				edAll(v20, w).check(t, c, p)
			})
		}
	}
}

// In OpenAPI 3.1 and 3.2 every direction gives the authored schema.
func TestProjectDirectionModern(t *testing.T) {
	root := `{"type":"object","required":["id","name","secret","kind"],"properties":{
		"id":{"type":"string","readOnly":true},"name":{"type":"string"},"secret":{"type":"string","writeOnly":true},"kind":{"$ref":"=>#C/ReadOnlyKind"}}}`
	for _, v := range modernEditions {
		for _, f := range formats {
			c := load(t, f, docText(v, "", directionSchemas), nil)
			var first string
			for _, d := range directions {
				t.Run(v+"/"+f+"/"+dirName(d), func(t *testing.T) {
					p := project(t, c, comp(t, c, v, "Account"), d)
					edAll(v, want{root: root, defs: map[string]string{"#C/ReadOnlyKind": `{"type":"string","readOnly":true}`}}).check(t, c, p)
					got := canon(mustDecode(t, "Root", p.Root))
					if first == "" {
						first = got
					} else if got != first {
						t.Errorf("%s Root differs from Neutral:\n%s\n%s", dirName(d), got, first)
					}
				})
			}
		}
	}
}

// The declarations of a property are those of the object itself and of
// every schema it reaches through allOf, through $ref and transitively;
// any one marking the property is enough, even where another declaration
// of the same name does not.
const allOfSchemas = `
	"Base":{"type":"object","properties":{"id":{"type":"string","readOnly":true},"pw":{"type":"string","writeOnly":true}}},
	"Mid":{"allOf":[{"$ref":"#C/Base"}]},
	"Pet":{"allOf":[{"$ref":"#C/Mid"},{"properties":{"tag":{"type":"string","readOnly":true}}}],
		"required":["id","pw","name","tag"],
		"properties":{"name":{"type":"string"},"id":{"type":"string"}}}`

func TestProjectDirectionAllOf30(t *testing.T) {
	base := `{"type":"object","properties":{"id":{"type":"string","readOnly":true},"pw":{"type":"string","writeOnly":true}}}`
	mid := `{"allOf":[{"$ref":"=>#C/Base"}]}`
	pet := func(required string) string {
		return `{"allOf":[{"$ref":"=>#C/Mid"},{"properties":{"tag":{"type":"string","readOnly":true}}}],"required":` + required +
			`,"properties":{"name":{"type":"string"},"id":{"type":"string"}}}`
	}
	for _, f := range formats {
		c := load(t, f, docText(v30, "", allOfSchemas), nil)
		for d, required := range map[schema2020.Direction]string{
			schema2020.Neutral:  `["id","pw","name","tag"]`,
			schema2020.Request:  `["pw","name"]`,
			schema2020.Response: `["id","name","tag"]`,
		} {
			t.Run(f+"/"+dirName(d), func(t *testing.T) {
				p := project(t, c, comp(t, c, v30, "Pet"), d)
				edAll(v30, want{root: pet(required), defs: map[string]string{"#C/Mid": mid, "#C/Base": base}}).check(t, c, p)
			})
		}
	}
}

func TestProjectDirectionAllOf20(t *testing.T) {
	for _, f := range formats {
		c := load(t, f, docText(v20, "", allOfSchemas), nil)
		t.Run(f, func(t *testing.T) {
			p := project(t, c, comp(t, c, v20, "Pet"), schema2020.Request)
			edAll(v20, want{
				root: `{"allOf":[{"$ref":"=>#C/Mid"},{"properties":{"tag":false}}],"required":["pw","name"],"properties":{"name":{"type":"string"},"id":false}}`,
				defs: map[string]string{
					"#C/Mid":  `{"allOf":[{"$ref":"=>#C/Base"}]}`,
					"#C/Base": `{"type":"object","properties":{"id":false,"pw":{"type":"string","writeOnly":true}}}`,
				},
			}).check(t, c, p)
		})
	}
}

// Only allOf adds declarations: in OpenAPI 3.0, oneOf and anyOf members are
// alternatives (Swagger 2.0 has neither keyword, OAS 2.0 section 6.4.18).
// A readOnly below the root of a property's schema does not mark the
// property, nor does readOnly on a schema that is not a property's. In
// Swagger 2.0 and OpenAPI 3.0 a readOnly beside $ref is ignored with the
// other members (OAS 3.0.4 section 4.7.23: they "SHALL be ignored").
func TestProjectDirectionNotDeclarations(t *testing.T) {
	schemas := `
		"Base":{"type":"object","properties":{"id":{"type":"string","readOnly":true}}},
		"Alt":{"oneOf":[{"$ref":"#C/Base"}],"anyOf":[{"$ref":"#C/Base"}],"required":["id","x"],"properties":{"x":{"type":"string"}}},
		"Deep":{"type":"object","required":["a","x"],"properties":{"a":{"allOf":[{"readOnly":true}]},"x":{"type":"string"}}},
		"Beside":{"type":"object","required":["a","x"],"properties":{"a":{"$ref":"#C/Plain","readOnly":true},"x":{"type":"string"}}},
		"Plain":{"type":"string"},
		"Top":{"type":"string","readOnly":true}`
	for _, v := range legacyEditions {
		for _, f := range formats {
			c := load(t, f, docText(v, "", schemas), nil)
			t.Run(v+"/"+f, func(t *testing.T) {
				for _, d := range []schema2020.Direction{schema2020.Request, schema2020.Response} {
					if v == v30 {
						p := project(t, c, comp(t, c, v, "Alt"), d)
						edAll(v, want{
							root: `{"oneOf":[{"$ref":"=>#C/Base"}],"anyOf":[{"$ref":"=>#C/Base"}],"required":["id","x"],"properties":{"x":{"type":"string"}}}`,
							defs: map[string]string{"#C/Base": `{"type":"object","properties":{"id":{"type":"string","readOnly":true}}}`},
						}).check(t, c, p)
					}

					p := project(t, c, comp(t, c, v, "Deep"), d)
					want{root: `{"type":"object","required":["a","x"],"properties":{"a":{"allOf":[{"readOnly":true}]},"x":{"type":"string"}}}`}.check(t, c, p)

					p = project(t, c, comp(t, c, v, "Beside"), d)
					edAll(v, want{
						root: `{"type":"object","required":["a","x"],"properties":{"a":{"$ref":"=>#C/Plain"},"x":{"type":"string"}}}`,
						defs: map[string]string{"#C/Plain": `{"type":"string"}`},
					}).check(t, c, p)

					p = project(t, c, comp(t, c, v, "Top"), d)
					want{root: `{"type":"string","readOnly":true}`}.check(t, c, p)
				}
			})
		}
	}
}

// A Projection of one direction may differ from another's under the same
// keys (Projection: "A key may have a different value in another
// direction"): the OpenAPI 3.0 Account above is the same key in every
// direction, with the values the rules give.
func TestProjectDirectionKeysAcrossDirections(t *testing.T) {
	c := load(t, "json", docText(v30, `"/a":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"array","items":{"$ref":"#C/Account"}}}}}}}}`, directionSchemas), nil)
	op, err := c.Operation("GET /a")
	if err != nil {
		t.Fatal(err)
	}
	s := op.Responses[0].Media[0].Schema
	values := map[string]bool{}
	for _, d := range directions {
		p := project(t, c, s, d)
		def, ok := p.Defs["Account"]
		if !ok {
			t.Fatalf("%s: no Defs[Account]: %v", dirName(d), p.Sources)
		}
		values[canon(mustDecode(t, "Account", def))] = true
	}
	if len(values) != 3 {
		t.Errorf("Defs[Account] takes %d distinct values over three directions, want 3", len(values))
	}
}

// A required list the rules leave empty is removed, in OpenAPI 3.0 for
// either direction and in a Swagger 2.0 Request projection.
func TestProjectDirectionEmptiedRequired(t *testing.T) {
	schemas := `"Token":{"type":"object","required":["id"],"properties":{"id":{"type":"string","readOnly":true},"note":{"type":"string"}}},
		"Secret":{"type":"object","required":["pw"],"properties":{"pw":{"type":"string","writeOnly":true}}}`
	for _, f := range formats {
		t.Run(f, func(t *testing.T) {
			c := load(t, f, docText(v30, "", schemas), nil)
			for _, tc := range []struct {
				name string
				d    schema2020.Direction
				root string
			}{
				{"Token", schema2020.Request, `{"type":"object","properties":{"id":{"type":"string","readOnly":true},"note":{"type":"string"}}}`},
				{"Token", schema2020.Response, `{"type":"object","required":["id"],"properties":{"id":{"type":"string","readOnly":true},"note":{"type":"string"}}}`},
				{"Secret", schema2020.Response, `{"type":"object","properties":{"pw":{"type":"string","writeOnly":true}}}`},
				{"Secret", schema2020.Request, `{"type":"object","required":["pw"],"properties":{"pw":{"type":"string","writeOnly":true}}}`},
			} {
				p := project(t, c, comp(t, c, v30, tc.name), tc.d)
				want{root: tc.root}.check(t, c, p)
				if _, has := mustDecode(t, "Root", p.Root).(map[string]any)["required"]; has != strings.Contains(tc.root, "required") {
					t.Errorf("%s %s: Root %s", tc.name, dirName(tc.d), p.Root)
				}
			}
			c = load(t, f, docText(v20, "", schemas), nil)
			p := project(t, c, comp(t, c, v20, "Token"), schema2020.Request)
			want{root: `{"type":"object","properties":{"id":false,"note":{"type":"string"}}}`}.check(t, c, p)
			if _, has := mustDecode(t, "Root", p.Root).(map[string]any)["required"]; has {
				t.Errorf("2.0 Request keeps an empty required: %s", p.Root)
			}
		})
	}
}

// The allOf chain of the scaling test, at four links: each link's
// required list loses the names any declaration along the chain marks
// readOnly (Request) or writeOnly (Response), including those declared
// only further down the chain.
func TestProjectDirectionAllOfChain(t *testing.T) {
	c := load(t, "json", allOfDocument(4, 1), nil)
	requiredOf := func(t *testing.T, b []byte) string {
		v, _ := mustDecode(t, "schema", b).(map[string]any)
		return canon(v["required"])
	}
	for d, w := range map[schema2020.Direction]map[string]string{
		schema2020.Request: {
			"O0": `["q0","p3"]`, "L0": `["w0","p1"]`, "L1": `["p1","w1"]`, "L2": `["w2","p3"]`, "L3": `["p3","w3","p4"]`,
		},
		schema2020.Response: {
			"O0": `["q0","p0","p3"]`, "L0": `["p0","p1"]`, "L1": `["p1","w1","p2"]`, "L2": `["p2","w2","p3"]`, "L3": `["p3","p4"]`,
		},
	} {
		t.Run(dirName(d), func(t *testing.T) {
			p := project(t, c, comp(t, c, v30, "O0"), d)
			if got := requiredOf(t, p.Root); got != canon(mustDecode(t, "want", []byte(w["O0"]))) {
				t.Errorf("O0 required = %s, want %s", got, w["O0"])
			}
			for _, link := range []string{"L0", "L1", "L2", "L3"} {
				def, ok := p.Defs[link]
				if !ok {
					t.Errorf("Defs has no %s: %v", link, p.Sources)
					continue
				}
				if got := requiredOf(t, def); got != canon(mustDecode(t, "want", []byte(w[link]))) {
					t.Errorf("%s required = %s, want %s", link, got, w[link])
				}
			}
		})
	}
}

// Each declaration is read under its own edition (Project: "Schemas are
// read under their openapi.Schema.Version"). An OpenAPI 3.0 object reaching
// an OpenAPI 3.1 schema through allOf finds readOnly and writeOnly beside a
// $ref there, and properties beside a $ref in an allOf member there, since
// 3.1 does not ignore members beside $ref; the same schemas in an OpenAPI
// 3.0 document are references only (OAS 3.0.4 section 4.7.23), and declare
// nothing.
func TestProjectDirectionMixedEditionSiblings(t *testing.T) {
	models := func(version string) string {
		return `{"openapi":"` + version + `","info":{"title":"M","version":"1"},"paths":{},"components":{"schemas":{
			"Base":{"type":"object","properties":{"id":{"$ref":"#/components/schemas/Id","readOnly":true},"n":{"type":"string"}}},
			"Base2":{"allOf":[{"$ref":"#/components/schemas/Other","properties":{"id2":{"type":"string","readOnly":true},"w":{"type":"string","writeOnly":true}}}]},
			"Other":{"type":"object"},
			"Id":{"type":"string"}}}}`
	}
	entry := docText(v30, "", `"Create":{"allOf":[{"$ref":"models.json#/components/schemas/Base"},{"$ref":"models.json#/components/schemas/Base2"}],"required":["id","n","id2","w"]}`)
	for _, f := range formats {
		for _, tc := range []struct {
			models   string
			required map[schema2020.Direction]string
		}{
			{v31, map[schema2020.Direction]string{
				schema2020.Neutral:  `["id","n","id2","w"]`,
				schema2020.Request:  `["n","w"]`,
				schema2020.Response: `["id","n","id2"]`,
			}},
			{"3.0.3", map[schema2020.Direction]string{
				schema2020.Neutral:  `["id","n","id2","w"]`,
				schema2020.Request:  `["id","n","id2","w"]`,
				schema2020.Response: `["id","n","id2","w"]`,
			}},
		} {
			t.Run(f+"/models "+tc.models, func(t *testing.T) {
				c := load(t, f, entry, map[string]string{modelsURI: models(tc.models)})
				for d, required := range tc.required {
					p := project(t, c, comp(t, c, v30, "Create"), d)
					root := mustDecode(t, "Root", p.Root).(map[string]any)
					if got := canon(root["required"]); got != canon(mustDecode(t, "want", []byte(required))) {
						t.Errorf("%s: required = %s, want %s", dirName(d), got, required)
					}
					if tc.models == v31 {
						want{
							root: `{"allOf":[{"$ref":"=>models.json#/components/schemas/Base"},{"$ref":"=>models.json#/components/schemas/Base2"}],"required":` + required + `}`,
							defs: map[string]string{
								"models.json#/components/schemas/Base":  `{"type":"object","properties":{"id":{"$ref":"=>models.json#/components/schemas/Id","readOnly":true},"n":{"type":"string"}}}`,
								"models.json#/components/schemas/Base2": `{"allOf":[{"$ref":"=>models.json#/components/schemas/Other","properties":{"id2":{"type":"string","readOnly":true},"w":{"type":"string","writeOnly":true}}}]}`,
								"models.json#/components/schemas/Other": `{"type":"object"}`,
								"models.json#/components/schemas/Id":    `{"type":"string"}`,
							},
						}.check(t, c, p)
					}
				}
			})
		}
	}
}
