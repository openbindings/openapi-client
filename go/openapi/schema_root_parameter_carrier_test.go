package openapi_test

import (
	"fmt"
	"slices"
	"testing"
)

// Loader gives versionless targets their reference context (load.go, Loader:
// "its object types come from the contexts of the references that reach
// it"); unknown fields on
// known non-schema OpenAPI objects do not create schema scope. OAS Parameter
// Object makes example instance data. A document-root $schema cue is not an
// explicit Schema reference. Standalone-root positives remain in
// TestSchemaRootDialectWithoutIdentifier; explicit mixed kinds in
// TestSchemaSharedParameterSchemaContainment are deliberately separate.
func TestSchemaRootParameterContextOverridesSchemaCue(t *testing.T) {
	const bundle = "https://schemas.example.test/api/root-parameter.json"
	const relay = "https://schemas.example.test/api/root-parameter-relay.json"
	const carrier = "https://schemas.example.test/api/root-parameter-carrier.json"
	const external = "https://schemas.example.test/api/root-parameter-target.json"
	const source = bundle + "#/example/payload"
	const raw = `{"$ref":"root-parameter-target.json#/T"}`
	const declaredRaw = `{"type":"object"}`
	const targetRaw = `{"type":"string"}`
	for _, version := range []string{"3.1.2", "3.2.1"} {
		for _, foreign := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/foreign=%t", version, foreign), func(t *testing.T) {
				marker := dialect2020
				if foreign {
					marker = "https://dialects.example.test/data"
				}
				bundleRaw := `{"$schema":"` + marker + `","$id":"data-only/","name":"q","in":"query","schema":` + declaredRaw + `,"example":{"payload":` + raw + `}}`
				relayRaw := `{"$ref":"root-parameter-carrier.json#/components/schemas/Carrier"}`
				carrierRaw := `{"openapi":"` + version + `","info":{"title":"Root Parameter carrier","version":"1"},"paths":{"/carrier":{"get":{"parameters":[{"$ref":"root-parameter.json"}],"responses":{"200":{"description":"ok"}}}}},"components":{"schemas":{"Carrier":{}}}}`
				fetch := newMemFetch(map[string]string{bundle: bundleRaw, relay: relayRaw, carrier: carrierRaw, external: `{"T":` + targetRaw + `}`})
				c := schemaGraphParse(t, schemaGraphDoc(version, `"Payload":{"$ref":"root-parameter.json#/example/payload"},"Relay":{"$ref":"root-parameter-relay.json"}`), fetch)
				before := fetch.callList()
				gotFetches := slices.Clone(before)
				wantFetches := []string{bundle, relay, carrier, external}
				slices.Sort(gotFetches)
				slices.Sort(wantFetches)
				if !slices.Equal(gotFetches, wantFetches) {
					t.Fatalf("Parse fetches=%q want %q", gotFetches, wantFetches)
				}
				dialect := dialectOAS31
				if version == "3.2.1" {
					dialect = dialectOAS32
				}
				for range 2 {
					s := schemaGraphGet(t, c, source)
					if s.Source() != source || s.Base() != bundle || s.Dialect() != dialect || string(s.Raw()) != raw {
						t.Errorf("payload Source=%q Base=%q Dialect=%q Raw=%s", s.Source(), s.Base(), s.Dialect(), s.Raw())
					}
					refs := schemaGraphRefs(t, s)
					schemaGraphWantEdges(t, refs, []schemaGraphEdge{{"/$ref", "$ref", "root-parameter-target.json#/T", external + "#/T", external + "#/T"}})
					if refs[0].Target.Base() != external || refs[0].Target.Dialect() != dialect || string(refs[0].Target.Raw()) != targetRaw {
						t.Error("genuine target metadata differs")
					}
					declared := schemaGraphGet(t, c, bundle+"#/schema")
					if declared.Source() != bundle+"#/schema" || declared.Base() != bundle || declared.Dialect() != dialect || string(declared.Raw()) != declaredRaw || len(schemaGraphRefs(t, declared)) != 0 {
						t.Error("declared root Parameter schema inherited the schema-looking root fields")
					}
				}
				relayHandle := schemaGraphGet(t, c, relay)
				schemaGraphWantEdges(t, schemaGraphRefs(t, relayHandle), []schemaGraphEdge{{"/$ref", "$ref", "root-parameter-carrier.json#/components/schemas/Carrier", carrier + "#/components/schemas/Carrier", carrier + "#/components/schemas/Carrier"}})
				if _, err := c.Schema("https://schemas.example.test/api/data-only/"); err == nil {
					t.Error("root Parameter's ordinary $id field claimed a Schema alias")
				}
				if !slices.Equal(fetch.callList(), before) {
					t.Errorf("accessors fetched: before=%q after=%q", before, fetch.callList())
				}
			})
		}
	}
}
