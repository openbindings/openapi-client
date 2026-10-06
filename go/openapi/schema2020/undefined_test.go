package schema2020_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// Keywords an older edition does not define. Project: "A keyword that the
// edition's Schema Object does not define and that JSON Schema 2020-12
// treats as an applicator or an assertion, such as oneOf in Swagger 2.0 or
// const in OpenAPI 3.0, is removed with an Issue, since it has no meaning
// in the authored edition. Every other keyword is kept as an annotation."
//
// The sets follow from the Schema Object of OAS 2.0 (section 6.4.18) and
// OAS 3.0.4 (sections 4.7.24.1 and 4.7.24.2, with $ref from section
// 4.7.23) against JSON Schema 2020-12: $ref and $dynamicRef (Core section
// 8.2.3), the applicators of Core sections 10 and 11, the assertions of
// Validation section 6, and contentSchema, whose value is a schema
// (Validation section 8.5). Format, other content and meta-data keywords
// (Validation sections 7 to 9) are annotations. Project also removes
// items written as an array, "a form neither edition defines".

var removed20 = []string{
	"$dynamicRef", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas", "prefixItems", "contains",
	"patternProperties", "propertyNames", "unevaluatedItems", "unevaluatedProperties",
	"const", "maxContains", "minContains", "dependentRequired", "contentSchema",
}

var removed30 = []string{
	"$dynamicRef", "if", "then", "else", "dependentSchemas", "prefixItems", "contains",
	"patternProperties", "propertyNames", "unevaluatedItems", "unevaluatedProperties",
	"const", "maxContains", "minContains", "dependentRequired", "contentSchema",
}

// keywordValues are well-formed values of those keywords.
var keywordValues = map[string]string{
	"$dynamicRef":           `"#meta"`,
	"anyOf":                 `[{"type":"string"}]`,
	"oneOf":                 `[{"type":"string"}]`,
	"not":                   `{"type":"null"}`,
	"if":                    `{"required":["a"]}`,
	"then":                  `{"required":["b"]}`,
	"else":                  `{"required":["c"]}`,
	"dependentSchemas":      `{"a":{"required":["b"]}}`,
	"prefixItems":           `[{"type":"string"}]`,
	"contains":              `{"type":"string"}`,
	"patternProperties":     `{"^x-":{"type":"string"}}`,
	"propertyNames":         `{"maxLength":5}`,
	"unevaluatedItems":      `false`,
	"unevaluatedProperties": `false`,
	"const":                 `"x"`,
	"maxContains":           `2`,
	"minContains":           `1`,
	"dependentRequired":     `{"a":["b"]}`,
	"contentSchema":         `{"type":"object"}`,
}

// The derived sets are the ones the shared checker applies.
func TestUndefinedKeywordSets(t *testing.T) {
	for v, want := range map[string][]string{v20: removed20, v30: removed30, "3.0.0": removed30} {
		got := removedKeywords(v)
		if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
			t.Errorf("%s: %q, want %q", v, got, want)
		}
	}
	for _, v := range modernEditions {
		if got := removedKeywords(v); got != nil {
			t.Errorf("%s removes %q", v, got)
		}
	}
}

// Each such keyword is removed with an Issue at it, beside the keywords
// the edition defines, which stay.
func TestProjectUndefinedKeywordRemoved(t *testing.T) {
	for _, v := range legacyEditions {
		set := removed20
		if v == v30 {
			set = removed30
		}
		for _, kw := range set {
			for _, f := range formats {
				t.Run(v+"/"+kw+"/"+f, func(t *testing.T) {
					c := load(t, f, docText(v, "", fmt.Sprintf(`"S":{"type":"object","title":"t",%s:%s}`, jstr(kw), keywordValues[kw])), nil)
					s := comp(t, c, v, "S")
					for _, d := range directions {
						p, issues := projectLossy(t, c, s, d)
						want{root: `{"type":"object","title":"t"}`}.check(t, c, p)
						wantIssues(t, issues, []issueWant{{s.Source(), pointer([]string{kw})}})
					}
				})
			}
		}
	}
}

// Removal happens wherever the edition reads a schema, and a schema left
// with no keywords is true. In Swagger 2.0 a $ref inside oneOf goes with
// the oneOf, so nothing refers to its target, which is not in Defs.
// Members beside $ref are ignored, not removed with an Issue.
func TestProjectUndefinedKeywordPlaces(t *testing.T) {
	schemas := `
		"Nested":{"type":"object","properties":{"p":{"type":"string","const":"x"}},"allOf":[{"patternProperties":{"^a":{}}}],
			"additionalProperties":{"type":"integer","dependentRequired":{"a":["b"]}}},
		"Empty":{"const":"x"},
		"Beside":{"properties":{"p":{"$ref":"#C/A","const":"x","propertyNames":{}}}},
		"A":{"type":"string"}`
	for _, v := range legacyEditions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				nested := comp(t, c, v, "Nested")
				p, issues := projectLossy(t, c, nested, schema2020.Neutral)
				want{root: `{"type":"object","properties":{"p":{"type":"string"}},"allOf":[true],"additionalProperties":{"type":"integer"}}`}.check(t, c, p)
				wantIssues(t, issues, []issueWant{
					{nested.Source(), "/properties/p/const"},
					{nested.Source(), "/allOf/0/patternProperties"},
					{nested.Source(), "/additionalProperties/dependentRequired"},
				})
				root := mustDecode(t, "Root", p.Root).(map[string]any)
				if root["allOf"].([]any)[0] != true {
					t.Errorf("an allOf member left with no keywords is %s, want true", canon(root["allOf"]))
				}

				empty := comp(t, c, v, "Empty")
				p, issues = projectLossy(t, c, empty, schema2020.Neutral)
				if string(p.Root) != "true" {
					t.Errorf("a schema left with no keywords: Root = %s, want true", p.Root)
				}
				wantIssues(t, issues, []issueWant{{empty.Source(), "/const"}})

				p = project(t, c, comp(t, c, v, "Beside"), schema2020.Neutral)
				edAll(v, want{root: `{"properties":{"p":{"$ref":"=>#C/A"}}}`, defs: map[string]string{"#C/A": `{"type":"string"}`}}).check(t, c, p)
			})
		}
	}
	t.Run("2.0 oneOf with $ref", func(t *testing.T) {
		for _, f := range formats {
			c := load(t, f, docText(v20, "", `"S":{"type":"object","oneOf":[{"$ref":"#/definitions/A"},{"$ref":"#/definitions/B"}]},"A":{"type":"string"},"B":{"type":"integer"}`), nil)
			s := comp(t, c, v20, "S")
			p, issues := projectLossy(t, c, s, schema2020.Request)
			want{root: `{"type":"object"}`}.check(t, c, p)
			wantIssues(t, issues, []issueWant{{s.Source(), "/oneOf"}})
		}
	})
	t.Run("3.0 oneOf with $ref", func(t *testing.T) {
		c := load(t, "json", docText(v30, "", `"S":{"type":"object","oneOf":[{"$ref":"#/components/schemas/A"}],"anyOf":[{"type":"object","const":{}}]},"A":{"type":"object"}`), nil)
		s := comp(t, c, v30, "S")
		p, issues := projectLossy(t, c, s, schema2020.Request)
		want{root: `{"type":"object","oneOf":[{"$ref":"=>#/components/schemas/A"}],"anyOf":[{"type":"object"}]}`, defs: map[string]string{"#/components/schemas/A": `{"type":"object"}`}}.check(t, c, p)
		wantIssues(t, issues, []issueWant{{s.Source(), "/anyOf/0/const"}})
	})
}

// Annotations an edition lacks stay: examples, deprecated in Swagger 2.0,
// unknown names, extensions, $comment, content keywords, and in Swagger 2.0
// nullable and x-nullable, which only OpenAPI 3.0 defines or reads.
func TestProjectUndefinedAnnotationsKept(t *testing.T) {
	cases := map[string]string{
		v20: `{"type":"string","examples":["a"],"deprecated":true,"writeOnly":true,"nullable":true,"x-nullable":true,
			"unknownName":{"oneOf":[1]},"x-ext":{"const":2},"$comment":"c","contentMediaType":"text/plain","readOnly":true}`,
		v30: `{"type":"string","examples":["a"],"deprecated":true,"x-nullable":true,"unknownName":{"oneOf":[1]},"x-ext":{"const":2},
			"$comment":"c","contentMediaType":"text/plain","contentEncoding":"base64"}`,
	}
	for v, schema := range cases {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", `"S":`+schema), nil)
				for _, d := range directions {
					p := project(t, c, comp(t, c, v, "S"), d)
					want{root: schema}.check(t, c, p)
				}
			})
		}
	}
}

// In OpenAPI 3.1 and 3.2 the same keywords are JSON Schema 2020-12's own:
// nothing is removed.
func TestProjectUndefinedKeywordsModern(t *testing.T) {
	members := ""
	for _, kw := range removed20 {
		if kw == "$dynamicRef" {
			continue
		}
		members += "," + jstr(kw) + ":" + keywordValues[kw]
	}
	schema := `{"type":"object"` + members + `}`
	for _, v := range modernEditions {
		t.Run(v, func(t *testing.T) {
			c := load(t, "json", docText(v, "", `"S":`+schema), nil)
			p := project(t, c, comp(t, c, v, "S"), schema2020.Request)
			want{root: schema}.check(t, c, p)
		})
	}
}

// contentSchema holds a schema whose references Swagger 2.0 and OpenAPI 3.0
// never report, so it goes with an Issue, and what it refers to is not in
// Defs; in OpenAPI 3.1 and 3.2 its references are rewritten.
func TestProjectContentSchema(t *testing.T) {
	schemas := `"S":{"type":"string","contentMediaType":"application/json","contentSchema":{"$ref":"#C/A"}},"A":{"type":"object"}`
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				s := comp(t, c, v, "S")
				if isModern(v) {
					p := project(t, c, s, schema2020.Neutral)
					edAll(v, want{
						root: `{"type":"string","contentMediaType":"application/json","contentSchema":{"$ref":"=>#C/A"}}`,
						defs: map[string]string{"#C/A": `{"type":"object"}`},
					}).check(t, c, p)
					return
				}
				p, issues := projectLossy(t, c, s, schema2020.Neutral)
				want{root: `{"type":"string","contentMediaType":"application/json"}`}.check(t, c, p)
				wantIssues(t, issues, []issueWant{{s.Source(), "/contentSchema"}})
			})
		}
	}
}

// items written as an array (the tuple form of JSON Schema draft 4) is
// defined by neither Swagger 2.0 (section 6.4.18) nor OpenAPI 3.0 (section
// 4.7.24.1: "items MUST be present if the type is array" and is a Schema
// Object), so Project removes it with an Issue at items, and nothing
// inside it is rewritten or reported. OpenAPI 3.1 and 3.2 keep it as
// authored: Schema.References reports nothing inside it, since JSON Schema
// 2020-12 has no array form of items (Core section 10.3.1.2).
func TestProjectArrayItems(t *testing.T) {
	schemas := `
		"Tuple":{"type":"array","maxItems":2,"items":[{"$ref":"#C/A"},{"type":"string","const":"x"},{"items":[{"$ref":"#C/Missing"}]}]},
		"Holder":{"type":"object","properties":{"p":{"type":"array","items":[{"type":"integer"}]},"q":{"type":"array","items":{"$ref":"#C/A"}}}},
		"Bare":{"items":[{"type":"string"}]},
		"A":{"type":"string"}`
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", schemas), nil)
				tuple, holder, bare := comp(t, c, v, "Tuple"), comp(t, c, v, "Holder"), comp(t, c, v, "Bare")
				if isModern(v) {
					for _, name := range []string{"Tuple", "Bare"} {
						s := comp(t, c, v, name)
						refs, err := s.References()
						if err != nil || len(refs) != 0 {
							t.Fatalf("%s: References = %+v, %v; want none inside array items", name, refs, err)
						}
						raw := string(s.Raw())
						p := project(t, c, s, schema2020.Request)
						want{root: raw}.check(t, c, p)
					}
					p := project(t, c, holder, schema2020.Request)
					edAll(v, want{
						root: `{"type":"object","properties":{"p":{"type":"array","items":[{"type":"integer"}]},"q":{"type":"array","items":{"$ref":"=>#C/A"}}}}`,
						defs: map[string]string{"#C/A": `{"type":"string"}`},
					}).check(t, c, p)
					return
				}
				for _, d := range directions {
					p, issues := projectLossy(t, c, tuple, d)
					want{root: `{"type":"array","maxItems":2}`}.check(t, c, p)
					wantIssues(t, issues, []issueWant{{tuple.Source(), "/items"}})

					p, issues = projectLossy(t, c, holder, d)
					edAll(v, want{
						root: `{"type":"object","properties":{"p":{"type":"array"},"q":{"type":"array","items":{"$ref":"=>#C/A"}}}}`,
						defs: map[string]string{"#C/A": `{"type":"string"}`},
					}).check(t, c, p)
					wantIssues(t, issues, []issueWant{{holder.Source(), "/properties/p/items"}})

					p, issues = projectLossy(t, c, bare, d)
					if string(p.Root) != "true" {
						t.Errorf("a schema left with no keywords: Root = %s, want true", p.Root)
					}
					wantIssues(t, issues, []issueWant{{bare.Source(), "/items"}})
				}
			})
		}
	}
}
