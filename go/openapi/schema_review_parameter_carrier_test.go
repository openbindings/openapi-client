package openapi_test

import (
	"slices"
	"strings"
	"testing"
)

// Loader follows references anywhere in each loaded document and recognizes
// its declared OpenAPI edition. OAS 3.1.2 Parameter Object (4.8.12) makes example
// instance data. That context still applies when an ordinary OpenAPI document
// carrying the Parameter reference is reached through a Schema reference chain.
func TestReview9ParameterDataContextThroughSchemaCarrier(t *testing.T) {
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
	c := schema9Parse(t, schema9Doc("3.1.2", `"Payload":{"$ref":"`+source+`"},"Relay":{"$ref":"relay.json"}`), fetch)
	before := fetch.callList()
	gotFetches := slices.Clone(before)
	wantFetches := []string{bundle, relay, carrier, external}
	slices.Sort(gotFetches)
	slices.Sort(wantFetches)
	if !slices.Equal(gotFetches, wantFetches) {
		t.Fatalf("Parse fetches=%q want %q", gotFetches, wantFetches)
	}
	for range 2 {
		s := schema9Get(t, c, source)
		if s.Source() != source || s.Base() != bundle || s.Dialect() != schema9OAS31 || string(s.Raw()) != payloadRaw {
			t.Errorf("payload Source=%q Base=%q Dialect=%q Raw=%s", s.Source(), s.Base(), s.Dialect(), s.Raw())
		}
		refs := schema9Refs(t, s)
		schema9WantEdges(t, refs, []schema9Edge{{"/$ref", "$ref", "carrier-target.json#/T", external + "#/T", external + "#/T"}})
		if string(refs[0].Target.Raw()) != targetRaw || refs[0].Target.Dialect() != schema9OAS31 {
			t.Error("payload target control differs")
		}
	}
	declared := schema9Get(t, c, bundle+"#/P/schema")
	if declared.Source() != bundle+"#/P/schema" || declared.Base() != bundle || declared.Dialect() != schema9OAS31 || string(declared.Raw()) != declaredRaw || len(schema9Refs(t, declared)) != 0 {
		t.Error("declared Parameter schema control differs")
	}
	relayHandle := schema9Get(t, c, relay)
	if strings.TrimSuffix(relayHandle.Source(), "#") != relay || relayHandle.Base() != relay || relayHandle.Dialect() != schema9OAS31 || string(relayHandle.Raw()) != relayRaw {
		t.Error("relay schema control differs")
	}
	relayRefs := schema9Refs(t, relayHandle)
	schema9WantEdges(t, relayRefs, []schema9Edge{{"/$ref", "$ref", "carrier-openapi.json#/components/schemas/Carrier", carrierSchema, carrierSchema}})
	if string(relayRefs[0].Target.Raw()) != `{}` || relayRefs[0].Target.Base() != carrier || relayRefs[0].Target.Dialect() != schema9OAS31 || len(schema9Refs(t, relayRefs[0].Target)) != 0 {
		t.Error("Carrier schema fragment control differs")
	}
	if !slices.Equal(fetch.callList(), before) {
		t.Errorf("accessors fetched: before=%q after=%q", before, fetch.callList())
	}
}
