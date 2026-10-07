package openapi_test

import (
	"slices"
	"testing"
)

// Loader contextual typing and Schema Base/Dialect/References; OAS 3.1.2
// Parameter Object (4.8.12) makes example instance data. Core 8.2.1 gives $id
// resource semantics in schemas, not in arbitrary instance objects. Neither
// $id nor $schema inside Parameter.example scopes a separately reached schema.
func TestSchemaParameterExampleIdentifiersRemainData(t *testing.T) {
	const bundle = "https://schemas.example.test/api/parameter-example-id.json"
	const hop = "https://schemas.example.test/api/parameter-id-hop.json"
	const external = "https://schemas.example.test/api/identifier-target.json"
	const source = bundle + "#/P/example/payload"
	const declaredRaw = `{"type":"object"}`
	const targetRaw = `{"type":"string"}`
	for _, indirect := range []bool{false, true} {
		name := "direct-local"
		if indirect {
			name = "indirect-eager"
		}
		t.Run(name, func(t *testing.T) {
			parameterRef, value, target := bundle+"#/P", "#/T", bundle+"#/T"
			wantFetches := []string{bundle}
			if indirect {
				parameterRef, value, target = hop, "identifier-target.json#/T", external+"#/T"
				wantFetches = append(wantFetches, hop, external)
			}
			raw := `{"$ref":"` + value + `"}`
			bundleRaw := `{"P":{"name":"q","in":"query","schema":` + declaredRaw + `,"example":{"$schema":"https://dialects.example.test/data","$id":"data-only/","payload":` + raw + `}},"T":` + targetRaw + `}`
			paths := `"paths":{"/x":{"get":{"operationId":"x","parameters":[{"$ref":"` + parameterRef + `"}],"responses":{"200":{"description":"ok"}}}}}`
			components := `"components":{"schemas":{"Payload":{"$ref":"` + source + `"}}}`
			members := paths + "," + components
			if indirect {
				members = components + "," + paths
			}
			fetch := newMemFetch(map[string]string{bundle: bundleRaw, hop: `{"$ref":"` + bundle + `#/P"}`, external: `{"T":` + targetRaw + `}`})
			c := schemaGraphParse(t, `{"openapi":"3.1.2","info":{"title":"Parameter example identifiers","version":"1"},`+members+`}`, fetch)
			before := fetch.callList()
			gotFetches := slices.Clone(before)
			slices.Sort(gotFetches)
			slices.Sort(wantFetches)
			if !slices.Equal(gotFetches, wantFetches) {
				t.Fatalf("Parse fetches=%q want %q", gotFetches, wantFetches)
			}
			for range 2 {
				s := schemaGraphGet(t, c, source)
				if s.Source() != source || s.Base() != bundle || s.Dialect() != dialectOAS31 || string(s.Raw()) != raw {
					t.Errorf("payload Source=%q Base=%q Dialect=%q Raw=%s", s.Source(), s.Base(), s.Dialect(), s.Raw())
				}
				refs := schemaGraphRefs(t, s)
				schemaGraphWantEdges(t, refs, []schemaGraphEdge{{"/$ref", "$ref", value, target, target}})
				if string(refs[0].Target.Raw()) != targetRaw || refs[0].Target.Dialect() != dialectOAS31 {
					t.Error("resolved target control differs")
				}
			}
			op := mustOp(t, c, "x")
			if op.Err != nil || len(op.Params) != 1 || op.Params[0].Schema == nil {
				t.Fatalf("Parameter descriptor=%+v", op)
			}
			declared := op.Params[0].Schema
			if declared.Source() != bundle+"#/P/schema" || declared.Base() != bundle || declared.Dialect() != dialectOAS31 || string(declared.Raw()) != declaredRaw || len(schemaGraphRefs(t, declared)) != 0 {
				t.Error("declared Parameter schema control differs")
			}
			if !slices.Equal(fetch.callList(), before) {
				t.Errorf("accessors fetched: before=%q after=%q", before, fetch.callList())
			}
		})
	}
}
