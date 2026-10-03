package openapi_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The owner feedback boundary preserves parts independent of the disproved
// inference. This explicitly reached schema supplies BOTH its own absolute
// $id and supported $schema (Core 8.2.1/8.1.1; Schema Base/Dialect), so neither
// depends on the surrounding Parameter example. Partial resets are not tested.
func TestReview9FeedbackPreservesIndependentNestedResource(t *testing.T) {
	const bundle = "https://schemas.example.test/api/bundle.json"
	const inferredCarrier = "https://schemas.example.test/api/data-only/carrier-openapi.json"
	const otherCarrier = "https://schemas.example.test/api/carrier-openapi.json"
	const independent = "https://schemas.example.test/independent-resource.json"
	const source = bundle + "#/P/example/Independent"
	const raw = `{"$id":"` + independent + `","$schema":"` + schema9JSON + `","$defs":{"Leaf":{"type":"string"}},"properties":{"v":{"$ref":"#/$defs/Leaf"}}}`
	const bundleRaw = `{"P":{"name":"q","in":"query","schema":{"type":"object"},"example":{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"data-only/","payload":{"$ref":"carrier-openapi.json#/components/schemas/C"},"Independent":` + raw + `}}}`
	const inferredRaw = `{"openapi":"3.1.2","info":{"title":"Inference-dependent carrier","version":"1"},"paths":{"/carrier":{"get":{"parameters":[{"$ref":"../bundle.json#/P"}],"responses":{"200":{"description":"ok"}}}}},"components":{"schemas":{"C":{}}}}`
	const otherRaw = `{"openapi":"3.1.2","info":{"title":"Different target","version":"1"},"paths":{},"components":{"schemas":{"C":{"type":"string"}}}}`
	fetch := newMemFetch(map[string]string{bundle: bundleRaw, inferredCarrier: inferredRaw, otherCarrier: otherRaw})
	c := schema9Parse(t, schema9Doc("3.1.2", `"Payload":{"$ref":"bundle.json#/P/example/payload"},"Independent":{"$ref":"bundle.json#/P/example/Independent"},"Stable":{"type":"integer"}`), fetch)
	before := fetch.callList()
	// Ensure the fixture still exercises the feedback boundary. Its failure
	// location remains free: lookup, References itself, or the affected edge.
	affected, err := c.Schema(bundle + "#/P/example/payload")
	failed := errors.Is(err, openapi.ErrUnresolved)
	if err == nil && affected != nil {
		refs, refsErr := affected.References()
		failed = errors.Is(refsErr, openapi.ErrUnresolved)
		if refsErr != nil && !failed {
			t.Errorf("affected References error=%v; want ErrUnresolved", refsErr)
		}
		for _, ref := range refs {
			if ref.Target != nil {
				t.Error("affected feedback reference still has a successful target")
			}
			failed = failed || errors.Is(ref.Err, openapi.ErrUnresolved)
		}
	}
	if !failed {
		t.Error("affected feedback schema reported no ErrUnresolved")
	}
	if _, err := c.Schema("https://schemas.example.test/api/data-only/#/Independent"); err == nil {
		t.Error("disproved parent identifier reached the valid independent resource")
	}
	for range 2 {
		for _, uri := range []string{source, independent} {
			s := schema9Get(t, c, uri)
			if s.Source() != source || s.Base() != independent || s.Dialect() != schema9JSON || string(s.Raw()) != raw {
				t.Errorf("independent resource via %s: Source=%q Base=%q Dialect=%q Raw=%s", uri, s.Source(), s.Base(), s.Dialect(), s.Raw())
			}
			refs := schema9Refs(t, s)
			schema9WantEdges(t, refs, []schema9Edge{{"/properties/v/$ref", "$ref", "#/$defs/Leaf", independent + "#/$defs/Leaf", source + "/$defs/Leaf"}})
			if refs[0].Target.Base() != independent || refs[0].Target.Dialect() != schema9JSON || string(refs[0].Target.Raw()) != `{"type":"string"}` {
				t.Error("independent leaf inherited the rejected surrounding scope")
			}
		}
		use := schema9Get(t, c, schema9Entry+"#/components/schemas/Independent")
		schema9WantEdges(t, schema9Refs(t, use), []schema9Edge{{"/$ref", "$ref", "bundle.json#/P/example/Independent", source, source}})
		declared := schema9Get(t, c, bundle+"#/P/schema")
		if declared.Source() != bundle+"#/P/schema" || declared.Base() != bundle || declared.Dialect() != schema9OAS31 || string(declared.Raw()) != `{"type":"object"}` || len(schema9Refs(t, declared)) != 0 {
			t.Error("unaffected Parameter schema lost its context")
		}
		stable := schema9Get(t, c, schema9Entry+"#/components/schemas/Stable")
		if stable.Source() != schema9Entry+"#/components/schemas/Stable" || stable.Base() != schema9Entry || stable.Dialect() != schema9OAS31 || string(stable.Raw()) != `{"type":"integer"}` || len(schema9Refs(t, stable)) != 0 {
			t.Error("independent stable schema was affected")
		}
	}
	if !slices.Equal(fetch.callList(), before) {
		t.Errorf("accessors fetched: before=%q after=%q", before, fetch.callList())
	}
}
