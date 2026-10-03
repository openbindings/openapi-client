package openapi_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Dialect and Core 4.3.5/9.1.1: a standalone root is a schema resource even
// without $id or $anchor. Its ordinary subschemas inherit its dialect; Core
// 9.3.2 extends that rule to a child resource without its own $schema. OAS
// 3.1.2 section 4.3.1 requires considering the containing document when a
// reference directly reaches a fragment. Entry reference order cannot change
// the authored dialect. Only known vocabulary supplies subschema locations.
func TestReview9RootDialectWithoutIdentifier(t *testing.T) {
	const external = "https://schemas.example.test/api/root-dialect.json"
	const source = external + "#/properties/child"
	const target = external + "#/$defs/Leaf"
	for _, version := range []string{"3.1.2", "3.2.1"} {
		for _, explicit := range []bool{false, true} {
			for _, resource := range []bool{false, true} {
				for _, reach := range []string{"child-only", "root-first", "child-first"} {
					t.Run(fmt.Sprintf("%s/explicit=%t/resource=%t/%s", version, explicit, resource, reach), func(t *testing.T) {
						wantDialect := schema9OAS31
						if version == "3.2.1" {
							wantDialect = schema9OAS32
						}
						header := ""
						if explicit {
							wantDialect = schema9JSON
							header = `"$schema":"` + wantDialect + `",`
						}
						value, base := "#/$defs/Leaf", external
						childHeader := ""
						if resource {
							value = target
							base = "https://schemas.example.test/api/child.json"
							childHeader = `"$id":"child.json",`
						}
						raw := `{` + childHeader + `"$ref":"` + value + `"}`
						doc := `{` + header + `"properties":{"child":` + raw + `},"$defs":{"Leaf":{"type":"string"}}}`
						childUse := `"Child":{"$ref":"` + source + `"}`
						rootUse := `"Root":{"$ref":"` + external + `"}`
						uses := childUse
						if reach == "root-first" {
							uses = rootUse + "," + childUse
						} else if reach == "child-first" {
							uses = childUse + "," + rootUse
						}
						fetch := newMemFetch(map[string]string{external: doc})
						c := schema9Parse(t, schema9Doc(version, uses), fetch)
						// Inspect the directly reached child before requesting a root
						// handle, including when no entry reference names that root.
						s := schema9Get(t, c, source)
						if s.Source() != source || s.Base() != base || s.Dialect() != wantDialect || string(s.Raw()) != raw {
							t.Errorf("child Source=%q Base=%q Dialect=%q Raw=%s; want %q %q %q %s", s.Source(), s.Base(), s.Dialect(), s.Raw(), source, base, wantDialect, raw)
						}
						r := schema9Refs(t, s)
						schema9WantEdges(t, r, []schema9Edge{{"/$ref", "$ref", value, target, target}})
						if r[0].Target.Dialect() != wantDialect {
							t.Errorf("target Dialect=%q want %q", r[0].Target.Dialect(), wantDialect)
						}
						if reach != "child-only" {
							root := schema9Get(t, c, external)
							if root.Dialect() != wantDialect || root.Base() != external || string(root.Raw()) != doc {
								t.Error("root control metadata differs")
							}
							schema9WantEdges(t, schema9Refs(t, root), []schema9Edge{{"/properties/child/$ref", "$ref", value, target, target}})
						}
						if !slices.Equal(fetch.callList(), []string{external}) {
							t.Errorf("fetches=%q", fetch.callList())
						}
					})
				}
			}
		}
	}
}

// A foreign root can be inspected without interpreting its vocabulary. This
// control requires no assertion about whether a foreign keyword holds a
// subschema: Schema's opaque-dialect contract requires References to error.
func TestReview9ForeignRootDialectWithoutIdentifier(t *testing.T) {
	const external = "https://schemas.example.test/api/foreign-root.json"
	const dialect = "https://dialects.example.test/opaque-root"
	const doc = `{"$schema":"` + dialect + `","$ref":"never.json","custom":{"type":"string"}}`
	for _, version := range []string{"3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			fetch := newMemFetch(map[string]string{external: doc})
			c := schema9Parse(t, schema9Doc(version, `"Root":{"$ref":"`+external+`"}`), fetch)
			s := schema9Get(t, c, external)
			if s.Dialect() != dialect || s.Base() != external || strings.TrimSuffix(s.Source(), "#") != external || string(s.Raw()) != doc {
				t.Error("foreign root metadata differs")
			}
			if _, err := s.References(); err == nil {
				t.Error("foreign root vocabulary was silently interpreted")
			}
			if !slices.Equal(fetch.callList(), []string{external}) {
				t.Errorf("foreign root caused retrieval: %q", fetch.callList())
			}
		})
	}
}

// The ledger's schema-looking data ruling and References' semantic containment
// contract: a $schema in ordinary default data cannot create dialect scope.
// The true outer root explicitly uses the edition's default dialect, so this
// control does not invent a rule about an independently reached annotation's
// relationship to a different true outer dialect.
func TestReview9AnnotationDoesNotCreateDialectScope(t *testing.T) {
	const external = "https://schemas.example.test/api/annotation-dialect.json"
	const source = external + "#/default/payload"
	const target = external + "#/$defs/Leaf"
	const raw = `{"$ref":"#/$defs/Leaf"}`
	for _, version := range []string{"3.1.2", "3.2.1"} {
		for _, rootFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/root-first=%t", version, rootFirst), func(t *testing.T) {
				dialect := schema9OAS31
				if version == "3.2.1" {
					dialect = schema9OAS32
				}
				data := `{"$schema":"https://dialects.example.test/data","$id":"ignored.json","properties":{"decoy":{"$ref":"never.json"}},"payload":` + raw + `}`
				doc := `{"$schema":"` + dialect + `","default":` + data + `,"$defs":{"Leaf":{"type":"string"}}}`
				childUse := `"Child":{"$ref":"` + source + `"}`
				rootUse := `"Root":{"$ref":"` + external + `"}`
				uses := childUse + "," + rootUse
				if rootFirst {
					uses = rootUse + "," + childUse
				}
				fetch := newMemFetch(map[string]string{external: doc})
				c := schema9Parse(t, schema9Doc(version, uses), fetch)
				s := schema9Get(t, c, source)
				if s.Dialect() != dialect || s.Base() != external || s.Source() != source || string(s.Raw()) != raw {
					t.Errorf("annotation imposed scope: Source=%q Base=%q Dialect=%q Raw=%s", s.Source(), s.Base(), s.Dialect(), s.Raw())
				}
				schema9WantEdges(t, schema9Refs(t, s), []schema9Edge{{"/$ref", "$ref", "#/$defs/Leaf", target, target}})
				if refs := schema9Refs(t, schema9Get(t, c, external)); len(refs) != 0 {
					t.Errorf("root traversed annotation data: %+v", refs)
				}
				if !slices.Equal(fetch.callList(), []string{external}) {
					t.Errorf("data caused retrieval: %q", fetch.callList())
				}
			})
		}
	}
}
