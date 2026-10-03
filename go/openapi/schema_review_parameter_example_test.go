package openapi_test

import (
	"fmt"
	"slices"
	"testing"
)

// Loader's contextual reference typing, Param.Schema, Dialect and References;
// OAS 3.1.2 Parameter Object: example is instance data, not a Schema Object.
// An explicitly referenced schema inside that data remains independently
// usable, but a schema-looking intermediate data object creates no dialect
// scope. P is reached only as Parameter; no conflicting type is assigned to P.
func TestReview9ParameterExampleDoesNotCreateDialectScope(t *testing.T) {
	const bundle = "https://schemas.example.test/api/parameter-example.json"
	const external = "https://schemas.example.test/api/target.json"
	const intermediate = "https://schemas.example.test/api/parameter-hop.json"
	const source = bundle + "#/P/example/payload"
	const leaf = `{"type":"string"}`
	type scenario struct {
		version                  string
		eager, reverse, indirect bool
	}
	var scenarios []scenario
	for _, version := range []string{"3.1.2", "3.2.1"} {
		for _, eager := range []bool{false, true} {
			for _, reverse := range []bool{false, true} {
				scenarios = append(scenarios, scenario{version: version, eager: eager, reverse: reverse})
			}
		}
	}
	// This one extra loading wave supplies P's Parameter context through a
	// Reference Object; no scheduling assumption determines when it arrives.
	scenarios = append(scenarios, scenario{version: "3.1.2", eager: true, indirect: true})
	for _, scenario := range scenarios {
		version, eager, reverse := scenario.version, scenario.eager, scenario.reverse
		t.Run(fmt.Sprintf("%s/eager=%t/reverse=%t/indirect=%t", version, eager, reverse, scenario.indirect), func(t *testing.T) {
			value, target := "#/T", bundle+"#/T"
			wantFetches := []string{bundle}
			if eager {
				value, target = "target.json#/T", external+"#/T"
				wantFetches = append(wantFetches, external)
			}
			raw := `{"$ref":"` + value + `"}`
			p := `"P":{"name":"q","in":"query","schema":` + leaf + `,"example":{"$schema":"https://dialects.example.test/data","payload":` + raw + `}}`
			targetField := `"T":` + leaf
			members := p + "," + targetField
			parameterReference := bundle + "#/P"
			if scenario.indirect {
				parameterReference = intermediate
				wantFetches = append(wantFetches, intermediate)
			}
			paths := `"paths":{"/x":{"get":{"operationId":"x","parameters":[{"$ref":"` + parameterReference + `"}],"responses":{"200":{"description":"ok"}}}}}`
			components := `"components":{"schemas":{"Payload":{"$ref":"` + source + `"}}}`
			entryMembers := paths + "," + components
			if reverse {
				members = targetField + "," + p
				entryMembers = components + "," + paths
			}
			fetch := newMemFetch(map[string]string{bundle: "{" + members + "}", external: `{"T":` + leaf + `}`, intermediate: `{"$ref":"` + bundle + `#/P"}`})
			c := schema9Parse(t, `{"openapi":"`+version+`","info":{"title":"Parameter example","version":"1"},`+entryMembers+`}`, fetch)
			before := fetch.callList()
			gotFetches := slices.Clone(before)
			slices.Sort(gotFetches)
			slices.Sort(wantFetches)
			if !slices.Equal(gotFetches, wantFetches) {
				t.Fatalf("Parse fetches=%q want %q", gotFetches, wantFetches)
			}
			dialect := schema9OAS31
			if version == "3.2.1" {
				dialect = schema9OAS32
			}
			for range 2 {
				s := schema9Get(t, c, source)
				if s.Dialect() != dialect || s.Base() != bundle || s.Source() != source || string(s.Raw()) != raw {
					t.Errorf("payload Source=%q Base=%q Dialect=%q Raw=%s", s.Source(), s.Base(), s.Dialect(), s.Raw())
				}
				r := schema9Refs(t, s)
				schema9WantEdges(t, r, []schema9Edge{{"/$ref", "$ref", value, target, target}})
				if string(r[0].Target.Raw()) != leaf || r[0].Target.Dialect() != dialect {
					t.Error("payload target control differs")
				}
			}
			op := mustOp(t, c, "x")
			if op.Err != nil || len(op.Params) != 1 || op.Params[0].Schema == nil {
				t.Fatalf("Parameter descriptor=%+v", op)
			}
			declared := op.Params[0].Schema
			if declared.Source() != bundle+"#/P/schema" || declared.Dialect() != dialect || string(declared.Raw()) != leaf || len(schema9Refs(t, declared)) != 0 {
				t.Error("declared Parameter schema control differs")
			}
			if !slices.Equal(fetch.callList(), before) {
				t.Errorf("accessors fetched: before=%q after=%q", before, fetch.callList())
			}
		})
	}
}
