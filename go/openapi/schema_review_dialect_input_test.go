package openapi_test

import (
	"slices"
	"testing"
)

// Loader reads JSON and YAML into the same public document model. Dialect and
// References therefore retain the root-resource and annotation boundaries in
// both encodings (Core 4.3.5/9.1.1). JSON member names are decoded strings: an
// escaped spelling of $schema has the same meaning (RFC 8259 sections 7 and
// 8.3). These four cases supplement the JSON matrix without repeating its
// edition, reference-order or child-resource axes.
func TestReview9DialectInputParity(t *testing.T) {
	const external = "https://schemas.example.test/api/dialect-input"
	const target = external + "#/$defs/Leaf"
	const raw = `{"$ref":"#/$defs/Leaf"}`
	const rootYAML = `$schema: ` + schema9JSON + `
properties:
  child:
    $ref: '#/$defs/Leaf'
$defs:
  Leaf:
    type: string
`
	const annotationYAML = `$schema: ` + schema9OAS32 + `
default:
  $schema: https://dialects.example.test/data
  $id: ignored.json
  properties:
    decoy:
      $ref: never.json
  payload:
    $ref: '#/$defs/Leaf'
$defs:
  Leaf:
    type: string
`
	const rootEscaped = `{"\u0024\u0073chema":"` + schema9JSON + `","properties":{"child":` + raw + `},"$defs":{"Leaf":{"type":"string"}}}`
	const annotationEscaped = `{"\u0024\u0073chema":"` + schema9OAS32 + `","default":{"\u0024\u0073chema":"https://dialects.example.test/data","$id":"ignored.json","properties":{"decoy":{"$ref":"never.json"}},"payload":` + raw + `},"$defs":{"Leaf":{"type":"string"}}}`
	for _, tt := range []struct {
		name, version, doc, pointer, dialect string
		annotation                           bool
	}{
		{"yaml-root", "3.1.2", rootYAML, "/properties/child", schema9JSON, false},
		{"yaml-annotation", "3.2.1", annotationYAML, "/default/payload", schema9OAS32, true},
		{"escaped-json-root", "3.1.2", rootEscaped, "/properties/child", schema9JSON, false},
		{"escaped-json-annotation", "3.2.1", annotationEscaped, "/default/payload", schema9OAS32, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := external + "#" + tt.pointer
			uses := `"Child":{"$ref":"` + source + `"}`
			if tt.annotation {
				uses += `,"Root":{"$ref":"` + external + `"}`
			}
			fetch := newMemFetch(map[string]string{external: tt.doc})
			c := schema9Parse(t, schema9Doc(tt.version, uses), fetch)
			before := fetch.callList()
			if !slices.Equal(before, []string{external}) {
				t.Fatalf("parse fetches=%q", before)
			}
			for range 2 {
				s := schema9Get(t, c, source)
				if s.Dialect() != tt.dialect || s.Source() != source || s.Base() != external {
					t.Errorf("Dialect=%q Source=%q Base=%q; want %q %q %q", s.Dialect(), s.Source(), s.Base(), tt.dialect, source, external)
				}
				sameJSON(t, "child Raw", s.Raw(), []byte(raw))
				schema9WantEdges(t, schema9Refs(t, s), []schema9Edge{{"/$ref", "$ref", "#/$defs/Leaf", target, target}})
				if tt.annotation {
					root := schema9Get(t, c, external)
					if root.Dialect() != tt.dialect {
						t.Errorf("root Dialect=%q want %q", root.Dialect(), tt.dialect)
					}
					if refs := schema9Refs(t, root); len(refs) != 0 {
						t.Errorf("root traversed schema-looking annotation: %+v", refs)
					}
				}
			}
			if !slices.Equal(fetch.callList(), before) {
				t.Errorf("accessors fetched: before=%q after=%q", before, fetch.callList())
			}
		})
	}
}
