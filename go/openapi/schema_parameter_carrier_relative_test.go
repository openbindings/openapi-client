package openapi_test

import (
	"slices"
	"strings"
	"testing"
)

// Loader resolves relative references against the document base (RFC 3986
// section 5.2). These entry spellings therefore name the same schemas as the
// absolute-payload carrier control. OAS 3.1.2 Parameter Object (4.8.12) still
// makes example instance data, independent of the URI spelling used to reach it.
func TestSchemaParameterContextThroughRelativeCarrier(t *testing.T) {
	const bundle = "https://schemas.example.test/api/carrier-bundle.json"
	const relay = "https://schemas.example.test/api/relay.json"
	const carrier = "https://schemas.example.test/api/carrier-openapi.json"
	const external = "https://schemas.example.test/api/carrier-target.json"
	const source = bundle + "#/P/example/payload"
	const carrierSchema = carrier + "#/components/schemas/Carrier"
	const declaredRaw = `{"type":"object"}`
	const payloadRaw = `{"$ref":"carrier-target.json#/T"}`
	const targetRaw = `{"type":"string"}`
	const relayRaw = `{"$ref":"carrier-openapi.json#/components/schemas/Carrier"}`
	bundleRaw := `{"P":{"name":"q","in":"query","schema":` + declaredRaw + `,"example":{"$schema":"https://dialects.example.test/data","$id":"data-only/","payload":` + payloadRaw + `}}}`
	carrierRaw := `{"openapi":"3.1.2","info":{"title":"Carrier","version":"1"},"paths":{"/carrier":{"get":{"parameters":[{"$ref":"carrier-bundle.json#/P"}],"responses":{"200":{"description":"ok"}}}}},"components":{"schemas":{"Carrier":{}}}}`
	fetch := newMemFetch(map[string]string{bundle: bundleRaw, relay: relayRaw, carrier: carrierRaw, external: `{"T":` + targetRaw + `}`})
	c := schemaGraphParse(t, schemaGraphDoc("3.1.2", `"Payload":{"$ref":"carrier-bundle.json#/P/example/payload"},"Relay":{"$ref":"relay.json"}`), fetch)
	before := fetch.callList()
	gotFetches := slices.Clone(before)
	wantFetches := []string{bundle, relay, carrier, external}
	slices.Sort(gotFetches)
	slices.Sort(wantFetches)
	if !slices.Equal(gotFetches, wantFetches) {
		t.Fatalf("Parse fetches=%q want %q", gotFetches, wantFetches)
	}
	for range 2 {
		s := schemaGraphGet(t, c, source)
		if s.Source() != source || s.Base() != bundle || s.Dialect() != dialectOAS31 || string(s.Raw()) != payloadRaw {
			t.Errorf("payload Source=%q Base=%q Dialect=%q Raw=%s", s.Source(), s.Base(), s.Dialect(), s.Raw())
		}
		refs := schemaGraphRefs(t, s)
		schemaGraphWantEdges(t, refs, []schemaGraphEdge{{"/$ref", "$ref", "carrier-target.json#/T", external + "#/T", external + "#/T"}})
		if string(refs[0].Target.Raw()) != targetRaw || refs[0].Target.Dialect() != dialectOAS31 {
			t.Error("payload target control differs")
		}
	}
	declared := schemaGraphGet(t, c, bundle+"#/P/schema")
	if declared.Source() != bundle+"#/P/schema" || declared.Base() != bundle || declared.Dialect() != dialectOAS31 || string(declared.Raw()) != declaredRaw || len(schemaGraphRefs(t, declared)) != 0 {
		t.Error("declared Parameter schema control differs")
	}
	relayHandle := schemaGraphGet(t, c, relay)
	if strings.TrimSuffix(relayHandle.Source(), "#") != relay || relayHandle.Base() != relay || relayHandle.Dialect() != dialectOAS31 || string(relayHandle.Raw()) != relayRaw {
		t.Error("relay schema control differs")
	}
	relayRefs := schemaGraphRefs(t, relayHandle)
	schemaGraphWantEdges(t, relayRefs, []schemaGraphEdge{{"/$ref", "$ref", "carrier-openapi.json#/components/schemas/Carrier", carrierSchema, carrierSchema}})
	if string(relayRefs[0].Target.Raw()) != `{}` || relayRefs[0].Target.Base() != carrier || relayRefs[0].Target.Dialect() != dialectOAS31 || len(schemaGraphRefs(t, relayRefs[0].Target)) != 0 {
		t.Error("Carrier schema fragment control differs")
	}
	if !slices.Equal(fetch.callList(), before) {
		t.Errorf("accessors fetched: before=%q after=%q", before, fetch.callList())
	}
}
