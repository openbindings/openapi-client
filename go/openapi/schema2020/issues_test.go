package schema2020_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// Losses. Project: "Project returns an *Error, and with it the Projection,
// when part of the schema cannot be carried: a reference that cannot be
// resolved, ...; a $dynamicRef, whose target depends on dynamic scope; a
// keyword removed as above; or a schema resource in a dialect other than
// those openapi.Schema reads. Each Issue's At names the keyword concerned,
// which then contributes nothing: a reference or removed keyword is dropped,
// with its mapping entry for a mapping value; a resource in another dialect,
// named by its $schema or, when it has none, by its root, becomes true, the
// schema that accepts anything; and a schema left with no keywords becomes
// true." Issue: "Source is the openapi.Schema.Source of the Root or Defs
// entry holding the problem", and At is "JSON Pointer from that schema's Raw
// to the problem". The checker also verifies that the Issues are exactly those
// the authored schemas call for.

// An unresolved $ref, to a missing component or an unavailable document,
// is dropped; a schema it leaves with no keywords is true.
func TestProjectUnresolvedReference(t *testing.T) {
	schemas := `"R":{"type":"object","properties":{"a":{"$ref":"#C/Missing"},"b":{"$ref":"#C/B"},"c":{"type":"array","items":{"$ref":"missing.json#/X"}}}},"B":{"type":"string"}`
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				r := comp(t, c, v, "R")
				for _, d := range directions {
					p, issues := projectLossy(t, c, r, d)
					edAll(v, want{
						root: `{"type":"object","properties":{"a":true,"b":{"$ref":"=>#C/B"},"c":{"type":"array","items":true}}}`,
						defs: map[string]string{"#C/B": `{"type":"string"}`},
					}).check(t, c, p)
					wantIssues(t, issues, []issueWant{
						{r.Source(), "/properties/a/$ref"},
						{r.Source(), "/properties/c/items/$ref"},
					})
				}
			})
		}
	}
}

// In OpenAPI 3.1 and 3.2 the members beside an unresolved $ref stay, as
// they would beside a resolved one; in Swagger 2.0 and OpenAPI 3.0 they
// are ignored, so the schema is left with no keywords and is true.
func TestProjectUnresolvedReferenceSiblings(t *testing.T) {
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", `"R":{"properties":{"a":{"$ref":"#C/Missing","description":"kept","maxLength":3}}}`), nil)
				r := comp(t, c, v, "R")
				p, issues := projectLossy(t, c, r, schema2020.Neutral)
				root := `{"properties":{"a":true}}`
				if isModern(v) {
					root = `{"properties":{"a":{"description":"kept","maxLength":3}}}`
				}
				want{root: root}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{r.Source(), "/properties/a/$ref"}})
			})
		}
	}
}

// A descriptor schema that is an unresolved $ref is a lost Root: in every
// edition the handle stays at the site (Schema: "when it cannot ... it stays
// at the site, and References reports the Err"), and the schema left with
// no keywords is true.
func TestProjectUnresolvedRoot(t *testing.T) {
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				var paths string
				if v == v20 {
					paths = `"/x":{"get":{"produces":["application/json"],"responses":{"200":{"description":"ok","schema":{"$ref":"#C/Missing"}}}}}`
				} else {
					paths = `"/x":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#C/Missing"}}}}}}}`
				}
				c := load(t, f, docText(v, paths, `"Other":{}`), nil)
				op, err := c.Operation("GET /x")
				if err != nil {
					t.Fatal(err)
				}
				s := op.Responses[0].Media[0].Schema
				p, issues := projectLossy(t, c, s, schema2020.Neutral)
				if string(p.Root) != "true" {
					t.Errorf("Root = %s, want true", p.Root)
				}
				want{root: `true`}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{s.Source(), "/$ref"}})
			})
		}
	}
}

// An unresolved reference inside a Defs entry is an Issue of that entry's
// Source. In Swagger 2.0 and OpenAPI 3.0 a component that is only an
// unresolved $ref cannot be followed, so the reference to it is the
// unresolved one; in OpenAPI 3.1 and 3.2 the component is its own Defs
// entry, true once its $ref is dropped.
func TestProjectUnresolvedInDefs(t *testing.T) {
	schemas := `"R":{"properties":{"a":{"$ref":"#C/A"},"b":{"$ref":"#C/Broken"}}},
		"A":{"type":"object","properties":{"x":{"$ref":"#C/Missing"},"y":{"type":"integer"}}},
		"Broken":{"$ref":"#C/Missing"}`
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				r, a := comp(t, c, v, "R"), comp(t, c, v, "A")
				p, issues := projectLossy(t, c, r, schema2020.Neutral)
				defA := `{"type":"object","properties":{"x":true,"y":{"type":"integer"}}}`
				if isModern(v) {
					broken := comp(t, c, v, "Broken")
					edAll(v, want{
						root: `{"properties":{"a":{"$ref":"=>#C/A"},"b":{"$ref":"=>#C/Broken"}}}`,
						defs: map[string]string{"#C/A": defA, "#C/Broken": `true`},
					}).check(t, c, p)
					wantIssues(t, issues, []issueWant{{a.Source(), "/properties/x/$ref"}, {broken.Source(), "/$ref"}})
					return
				}
				edAll(v, want{
					root: `{"properties":{"a":{"$ref":"=>#C/A"},"b":true}}`,
					defs: map[string]string{"#C/A": defA},
				}).check(t, c, p)
				wantIssues(t, issues, []issueWant{{a.Source(), "/properties/x/$ref"}, {r.Source(), "/properties/b/$ref"}})
			})
		}
	}
}

// An unresolved discriminator mapping value loses its mapping entry, and an
// unresolved OpenAPI 3.2 defaultMapping is removed; the resolved values
// are rewritten.
func TestProjectUnresolvedMapping(t *testing.T) {
	for _, v := range []string{v30, v31, v32} {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				def := ""
				if v == v32 {
					def = `,"defaultMapping":"Nobody"`
				}
				c := load(t, f, docText(v, "", `"R":{"oneOf":[{"$ref":"#C/Cat"}],"discriminator":{"propertyName":"kind","mapping":{"cat":"Cat","ghost":"Ghost"}`+def+`}},"Cat":{"type":"object"}`), nil)
				r := comp(t, c, v, "R")
				p, issues := projectLossy(t, c, r, schema2020.Neutral)
				edAll(v, want{
					root: `{"oneOf":[{"$ref":"=>#C/Cat"}],"discriminator":{"propertyName":"kind","mapping":{"cat":"=>#C/Cat"}}}`,
					defs: map[string]string{"#C/Cat": `{"type":"object"}`},
				}).check(t, c, p)
				w := []issueWant{{r.Source(), "/discriminator/mapping/ghost"}}
				if v == v32 {
					w = append(w, issueWant{r.Source(), "/discriminator/defaultMapping"})
				}
				wantIssues(t, issues, w)
			})
		}
	}
}

// Every $dynamicRef is an Issue, whatever its fragment (JSON Schema
// 2020-12 Core section 8.2.3.2): to a $dynamicAnchor, to no anchor, or to
// a JSON Pointer. It is dropped, once per authored site even when the
// schema is both Root and a Defs entry, its lexical target is not reached
// by it, and the members beside it stay.
func TestProjectDynamicRef(t *testing.T) {
	schemas := `
		"Tree":{"$dynamicAnchor":"node","type":"object","properties":{"kids":{"type":"array","items":{"$dynamicRef":"#node"}},"self":{"$ref":"#C/Tree"}}},
		"Loose":{"properties":{
			"a":{"$dynamicRef":"#nowhere"},
			"b":{"$dynamicRef":"#node"},
			"c":{"$dynamicRef":"#C/Leaf"},
			"d":{"$dynamicRef":"#node","description":"kept"}}},
		"Leaf":{"type":"string"}`
	for _, v := range modernEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				tree := comp(t, c, v, "Tree")
				for _, d := range directions {
					p, issues := projectLossy(t, c, tree, d)
					root := `{"type":"object","properties":{"kids":{"type":"array","items":true},"self":{"$ref":"=>#C/Tree"}}}`
					edAll(v, want{root: root, defs: map[string]string{"#C/Tree": root}}).check(t, c, p)
					wantIssues(t, issues, []issueWant{{tree.Source(), "/properties/kids/items/$dynamicRef"}})
				}
				loose := comp(t, c, v, "Loose")
				p, issues := projectLossy(t, c, loose, schema2020.Neutral)
				want{root: `{"properties":{"a":true,"b":true,"c":true,"d":{"description":"kept"}}}`}.check(t, c, p)
				wantIssues(t, issues, []issueWant{
					{loose.Source(), "/properties/a/$dynamicRef"},
					{loose.Source(), "/properties/b/$dynamicRef"},
					{loose.Source(), "/properties/c/$dynamicRef"},
					{loose.Source(), "/properties/d/$dynamicRef"},
				})
			})
		}
	}
}

// A schema resource in another dialect becomes true at its resource root,
// whether that is Root, a Defs entry, or a nested resource, with an Issue
// at its $schema.
func TestProjectOtherDialect(t *testing.T) {
	schemas := `
		"Custom":{"$schema":"` + dialectDraft7 + `","type":"string","maxLength":2},
		"UsesCustom":{"properties":{"c":{"$ref":"#C/Custom"},"d":{"type":"integer"}}},
		"Nested":{"properties":{"c":{"$id":"nested.json","$schema":"` + dialectDraft7 + `","type":"string"},"d":{"type":"integer"}}}`
	for _, v := range modernEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				custom := comp(t, c, v, "Custom")
				p, issues := projectLossy(t, c, custom, schema2020.Neutral)
				want{root: `true`}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{custom.Source(), "/$schema"}})

				p, issues = projectLossy(t, c, comp(t, c, v, "UsesCustom"), schema2020.Request)
				edAll(v, want{
					root: `{"properties":{"c":{"$ref":"=>#C/Custom"},"d":{"type":"integer"}}}`,
					defs: map[string]string{"#C/Custom": `true`},
				}).check(t, c, p)
				wantIssues(t, issues, []issueWant{{custom.Source(), "/$schema"}})

				nested := comp(t, c, v, "Nested")
				p, issues = projectLossy(t, c, nested, schema2020.Response)
				want{root: `{"properties":{"c":true,"d":{"type":"integer"}}}`}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{nested.Source(), "/properties/c/$schema"}})
			})
		}
	}
}

// The supported dialects are those the client itself reads: a schema
// resource declaring one gives no Issue, and Schema.References reads it,
// at the root of a component and as a nested resource; a dialect outside
// them is reported by both.
func TestProjectDialectConsistency(t *testing.T) {
	unknown := []string{"https://example.test/dialect/custom", dialectDraft7, "https://spec.openapis.org/oas/3.0/dialect/base"}
	for _, v := range modernEditions {
		for _, dialect := range append(slices.Clone(supportedDialects), unknown...) {
			t.Run(v+"/"+dialect, func(t *testing.T) {
				c := load(t, "json", docText(v, "", `
					"Top":{"$schema":"`+dialect+`","type":"object","properties":{"a":{"$ref":"#C/Leaf"}}},
					"Holder":{"properties":{"n":{"$id":"n.json","$schema":"`+dialect+`","type":"integer"},"m":{"type":"string"}}},
					"Leaf":{"type":"string"}`), nil)
				top, holder := comp(t, c, v, "Top"), comp(t, c, v, "Holder")
				_, topErr := top.References()
				_, holderErr := holder.References()
				if supportedDialect(dialect) {
					if topErr != nil || holderErr != nil {
						t.Errorf("References: %v, %v; the client should read %s", topErr, holderErr, dialect)
					}
					p := project(t, c, top, schema2020.Neutral)
					edAll(v, want{root: `{"type":"object","properties":{"a":{"$ref":"=>#C/Leaf"}}}`, defs: map[string]string{"#C/Leaf": `{"type":"string"}`}}).check(t, c, p)
					p = project(t, c, holder, schema2020.Neutral)
					want{root: `{"properties":{"n":{"type":"integer"},"m":{"type":"string"}}}`}.check(t, c, p)
					return
				}
				if topErr == nil || holderErr == nil {
					t.Errorf("References: %v, %v; want errors for %s", topErr, holderErr, dialect)
				}
				p, issues := projectLossy(t, c, top, schema2020.Neutral)
				want{root: `true`}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{top.Source(), "/$schema"}})
				p, issues = projectLossy(t, c, holder, schema2020.Neutral)
				want{root: `{"properties":{"n":true,"m":{"type":"string"}}}`}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{holder.Source(), "/properties/n/$schema"}})
			})
		}
	}
}

// A document's jsonSchemaDialect sets the dialect of every schema it holds
// (Schema.Dialect): another dialect loses each schema at its root, with an
// Issue at the root since no $schema is written there; a supported one
// loses nothing.
func TestProjectDocumentDialect(t *testing.T) {
	for _, v := range modernEditions {
		for _, dialect := range []string{dialectDraft7, dialect2020, "https://spec.openapis.org/oas/3.1/dialect/2024-11-10"} {
			t.Run(v+"/"+dialect, func(t *testing.T) {
				doc := `{"openapi":"` + v + `","jsonSchemaDialect":"` + dialect + `","info":{"title":"T","version":"1"},"paths":{},
					"components":{"schemas":{"R":{"properties":{"a":{"$ref":"#/components/schemas/A"}}},"A":{"type":"string"}}}}`
				c := load(t, "json", doc, nil)
				r := comp(t, c, v, "R")
				if supportedDialect(dialect) {
					p := project(t, c, r, schema2020.Neutral)
					want{root: `{"properties":{"a":{"$ref":"=>#/components/schemas/A"}}}`, defs: map[string]string{"#/components/schemas/A": `{"type":"string"}`}}.check(t, c, p)
					return
				}
				p, issues := projectLossy(t, c, r, schema2020.Neutral)
				want{root: `true`}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{r.Source(), ""}})
			})
		}
	}
}

// A nested resource in another dialect becomes true with an Issue at its
// $schema (Project: "a resource in another dialect, named by its $schema or,
// when it has none, by its root, becomes true"); the
// other references in its tree are read through c and rewritten as any
// other, mapping values included, while one that cannot be resolved is
// lost as usual. References inside the foreign resource are part of it.
func TestProjectNestedDialectBesideReference(t *testing.T) {
	schemas := `"R":{"properties":{
		"b":{"$ref":"#C/B"},
		"e":{"$ref":"#C/B","description":"kept"},
		"m":{"$ref":"#C/Missing"},
		"d":{"discriminator":{"propertyName":"k","mapping":{"x":"B"}}},
		"c":{"$id":"c.json","$schema":"` + dialectDraft7 + `","properties":{"z":{"$ref":"#C/B"}}}}},
		"B":{"type":"string"}`
	for _, v := range modernEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				r := comp(t, c, v, "R")
				if _, err := r.References(); err == nil {
					t.Fatal("References succeeds for a tree holding a resource in another dialect")
				}
				p, issues := projectLossy(t, c, r, schema2020.Neutral)
				edAll(v, want{
					root: `{"properties":{"b":{"$ref":"=>#C/B"},"e":{"$ref":"=>#C/B","description":"kept"},"m":true,
						"d":{"discriminator":{"propertyName":"k","mapping":{"x":"=>#C/B"}}},"c":true}}`,
					defs: map[string]string{"#C/B": `{"type":"string"}`},
				}).check(t, c, p)
				wantIssues(t, issues, []issueWant{
					{r.Source(), "/properties/m/$ref"},
					{r.Source(), "/properties/c/$schema"},
				})
			})
		}
	}
}

// A resource in another dialect inside an unreferenced nested $defs entry
// is dropped with the entry (Project: "an unreferenced one is dropped") and
// changes nothing else; one inside a referenced entry is that Defs entry,
// true with its Issue.
func TestProjectForeignResourceInDefs(t *testing.T) {
	schemas := `
		"R":{"type":"object","$defs":{"old":{"$schema":"` + dialectDraft7 + `","type":"string"}},"properties":{"a":{"$ref":"#C/B"}}},
		"S":{"$id":"https://api.example.test/schemas/s.json","$defs":{"old":{"$schema":"` + dialectDraft7 + `","type":"string"},"used":{"type":"integer"}},
			"properties":{"a":{"$ref":"#/$defs/used"},"b":{"$ref":"#/$defs/old"}}},
		"B":{"type":"string"}`
	for _, v := range modernEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				edAll(v, want{root: `{"type":"object","properties":{"a":{"$ref":"=>#C/B"}}}`, defs: map[string]string{"#C/B": `{"type":"string"}`}}).check(t, c, p)

				used, old := "#/components/schemas/S/$defs/used", "#/components/schemas/S/$defs/old"
				p, issues := projectLossy(t, c, comp(t, c, v, "S"), schema2020.Neutral)
				want{
					root: `{"properties":{"a":{"$ref":"=>` + used + `"},"b":{"$ref":"=>` + old + `"}}}`,
					defs: map[string]string{used: `{"type":"integer"}`, old: `true`},
				}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{schemaAt(t, c, old).Source(), "/$schema"}})
			})
		}
	}
}

// Project: a reference "that openapi.Schema.References does not report for the
// schema holding it (such as a $ref in a Swagger 2.0 items object, or one that
// is not a string), which is removed alone", is lost: removed with an Issue at
// that $ref. A Swagger 2.0 Items Object (OAS 2.0 section 6.4.10) is not a
// Schema Object, so a $ref there, in a parameter, a header or a formData field,
// is not reported.
func TestProjectUnreportedReference(t *testing.T) {
	paths := `
		"/x":{"get":{"produces":["application/json"],"parameters":[
			{"name":"q","in":"query","type":"array","items":{"$ref":"#/definitions/D"}},
			{"name":"X-Ids","in":"header","type":"array","items":{"$ref":"#/definitions/D"}}],
			"responses":{"200":{"description":"ok","headers":{"H":{"type":"array","items":{"$ref":"#/definitions/D"}}}}}}},
		"/f":{"post":{"consumes":["multipart/form-data"],"parameters":[
			{"name":"f","in":"formData","type":"array","items":{"$ref":"#/definitions/D"}}],
			"responses":{"204":{"description":"none"}}}}`
	for _, f := range formats {
		t.Run("2.0 items/"+f, func(t *testing.T) {
			c := load(t, f, docText(v20, paths, `"D":{"type":"string"}`), nil)
			get, err := c.Operation("GET /x")
			if err != nil {
				t.Fatal(err)
			}
			schemas := []*openapi.Schema{get.Params[0].Schema, get.Params[1].Schema, get.Responses[0].Headers[0].Schema}
			post, err := c.Operation("POST /f")
			if err != nil {
				t.Fatal(err)
			}
			form := post.Body.Media[0]
			schemas = append(schemas, form.Encoding[0].Schema)
			for _, s := range schemas {
				if s == nil {
					t.Fatal("a parameter has no schema")
				}
				for _, d := range directions {
					p, issues := projectLossy(t, c, s, d)
					want{root: `{"type":"array","items":true}`}.check(t, c, p)
					wantIssues(t, issues, []issueWant{{s.Source(), "/items/$ref"}})
				}
			}
			raw := mustDecode(t, "formData Raw", form.Schema.Raw()).(map[string]any)
			raw["properties"] = map[string]any{"f": map[string]any{"type": "array", "items": true}}
			p, issues := projectLossy(t, c, form.Schema, schema2020.Request)
			sameSchema(t, "formData Root", p.Root, raw)
			wantIssues(t, issues, []issueWant{{form.Schema.Source(), "/properties/f/items/$ref"}})
		})
	}
	schemas := `"S":{"type":"object","properties":{"x":{"$ref":5,"type":"string"},"y":{"$ref":{"a":1}},"z":{"$ref":"#C/B"}}},"B":{"type":"string"}`
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+" not a string/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				s := comp(t, c, v, "S")
				p, issues := projectLossy(t, c, s, schema2020.Neutral)
				wantIssues(t, issues, []issueWant{{s.Source(), "/properties/x/$ref"}, {s.Source(), "/properties/y/$ref"}})
				if isModern(v) {
					edAll(v, want{
						root: `{"type":"object","properties":{"x":{"type":"string"},"y":true,"z":{"$ref":"=>#C/B"}}}`,
						defs: map[string]string{"#C/B": `{"type":"string"}`},
					}).check(t, c, p)
					return
				}
				// Whether members beside a $ref that is not a string are
				// ignored is not decided; the $ref itself is gone.
				root := mustDecode(t, "Root", p.Root).(map[string]any)
				props := root["properties"].(map[string]any)
				if x := canon(emptyAsTrue(props["x"])); x != "true" && x != canon(map[string]any{"type": "string"}) {
					t.Errorf("properties/x = %s", x)
				}
				if props["y"] != true {
					t.Errorf("properties/y = %s, want true", canon(props["y"]))
				}
				if len(p.Defs) != 1 {
					t.Errorf("Defs %v, want B only", p.Sources)
				}
			})
		}
	}
}

// An unresolved reference in a referenced nested $defs entry is an Issue
// of that entry, the schema it is emitted in, and of no other.
func TestProjectIssueInNestedDefsEntry(t *testing.T) {
	schemas := `"R":{"$id":"https://api.example.test/schemas/n.json","$defs":{"inner":{"properties":{"x":{"$ref":"#/$defs/nope"},"y":{"type":"string"}}}},
		"properties":{"a":{"$ref":"#/$defs/inner"}}}`
	for _, v := range modernEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				inner := "#/components/schemas/R/$defs/inner"
				p, issues := projectLossy(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				want{
					root: `{"properties":{"a":{"$ref":"=>` + inner + `"}}}`,
					defs: map[string]string{inner: `{"properties":{"x":true,"y":{"type":"string"}}}`},
				}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{schemaAt(t, c, inner).Source(), "/properties/x/$ref"}})
			})
		}
	}
}

// A dropped, unreferenced $defs entry takes its losses with it: an
// unresolved reference or a $dynamicRef there is no Issue.
func TestProjectDroppedDefsNoIssue(t *testing.T) {
	schemas := `"R":{"type":"object","$defs":{"gone":{"$ref":"#/components/schemas/Gone"},"dyn":{"$dynamicRef":"#x"}},"properties":{"a":{"type":"string"}}}`
	for _, v := range modernEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				want{root: `{"type":"object","properties":{"a":{"type":"string"}}}`}.check(t, c, p)
			})
		}
	}
}

// Error: "Error names each Source and At with its reason", and Unwrap
// "returns each issue's Err in order".
func TestProjectErrorReportsEveryIssue(t *testing.T) {
	for _, v := range modernEditions {
		t.Run(v, func(t *testing.T) {
			c := load(t, "json", docText(v, "", `
				"R":{"properties":{"a":{"$ref":"#C/Missing"},"b":{"$dynamicRef":"#node"},"c":{"$ref":"#C/Custom"}}},
				"B":{"$dynamicAnchor":"node","type":"string"},
				"Custom":{"$schema":"`+dialectDraft7+`"}`), nil)
			r := comp(t, c, v, "R")
			p, err := schema2020.Project(c, r, schema2020.Neutral)
			var pe *schema2020.Error
			if !errors.As(err, &pe) {
				t.Fatalf("error %v, want *schema2020.Error", err)
			}
			checkProjection(t, c, r, schema2020.Neutral, p, err)
			if len(pe.Issues) != 3 {
				t.Fatalf("Issues %+v, want 3", pe.Issues)
			}
			msg := err.Error()
			for _, is := range pe.Issues {
				if !strings.Contains(msg, is.Source) || !strings.Contains(msg, is.At) {
					t.Errorf("Error() %q does not name %s %q", msg, is.Source, is.At)
				}
				if !errors.Is(err, is.Err) {
					t.Errorf("errors.Is(err, %v) is false", is.Err)
				}
			}
			if got := pe.Unwrap(); len(got) != 3 {
				t.Errorf("Unwrap() = %v, want 3 errors", got)
			}
		})
	}
}
