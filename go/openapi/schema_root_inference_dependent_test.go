package openapi_test

import (
	"slices"
	"testing"
)

// load.go, Loader: "If retrieval based on an inferred schema resource later
// reveals that the resource is instance data, references whose scope depends
// on that inference are unresolvable." A schema reference that enabled
// inference-dependent discovery must not become a stale success merely
// because its node is also a valid schema field in the later Parameter
// context. The causal route is payload -> /schema -> carrier -> root
// Parameter, not an execution-order assumption. Both entry references name
// fragments, never the root as Schema.
func TestSchemaDisprovedRootInferenceRefusesDependentReferences(t *testing.T) {
	const bundle = "https://schemas.example.test/api/executed-root.json"
	const inferredCarrier = "https://schemas.example.test/api/data-only/executed-carrier.json"
	const otherCarrier = "https://schemas.example.test/api/executed-carrier.json"
	const bundleRaw = `{"name":"q","in":"query","$schema":"` + dialect2020 + `","$id":"data-only/","schema":{"$ref":"executed-carrier.json#/components/schemas/C"},"payload":{"$ref":"#/schema"}}`
	const inferredRaw = `{"openapi":"3.1.2","info":{"title":"Executed schema carrier","version":"1"},"paths":{"/carrier":{"get":{"parameters":[{"$ref":"../executed-root.json"}],"responses":{"200":{"description":"ok"}}}}},"components":{"schemas":{"C":{}}}}`
	const otherRaw = `{"openapi":"3.1.2","info":{"title":"Other target","version":"1"},"paths":{},"components":{"schemas":{"C":{"type":"string"}}}}`
	documents := map[string]string{bundle: bundleRaw, inferredCarrier: inferredRaw, otherCarrier: otherRaw}
	fetch := newMemFetch(documents)
	c := schemaGraphParse(t, schemaGraphDoc("3.1.2", `"Declared":{"$ref":"executed-root.json#/schema"},"Payload":{"$ref":"executed-root.json#/payload"},"Stable":{"type":"integer"}`), fetch)
	before := fetch.callList()
	inventory := c.DocumentURIs()
	// Require the context-revealing carrier, without prescribing the complete
	// fetch set or any order. Only /schema's relative reference can reach it.
	if !slices.Contains(before, inferredCarrier) {
		t.Fatal("the inference-dependent Parameter carrier was not reached")
	}
	for _, uri := range before {
		if _, supplied := documents[uri]; supplied && (!slices.Contains(inventory, uri) || len(c.Document(uri)) == 0) {
			t.Errorf("reached document was discarded: %s", uri)
		}
	}
	for range 2 {
		for _, node := range []disprovedInferenceNode{
			{uri: bundle + "#/schema", value: "executed-carrier.json#/components/schemas/C"},
			{uri: bundle + "#/payload", value: "#/schema"},
			{uri: schemaGraphEntry + "#/components/schemas/Declared", value: "executed-root.json#/schema"},
			{uri: schemaGraphEntry + "#/components/schemas/Payload", value: "executed-root.json#/payload"},
		} {
			got := disprovedInferenceRead{}
			got.schema, got.lookupErr = c.Schema(node.uri)
			if got.lookupErr == nil && got.schema != nil {
				got.refsCalled = true
				got.refs, got.refsErr = got.schema.References()
			}
			disprovedInferenceWantUnresolved(t, node, got)
		}
		if string(c.Document(bundle)) != bundleRaw {
			t.Error("root Parameter document content was not retained")
		}
		stable := schemaGraphGet(t, c, schemaGraphEntry+"#/components/schemas/Stable")
		if stable.Source() != schemaGraphEntry+"#/components/schemas/Stable" || stable.Base() != schemaGraphEntry || stable.Dialect() != dialectOAS31 || string(stable.Raw()) != `{"type":"integer"}` || len(schemaGraphRefs(t, stable)) != 0 {
			t.Error("independent stable schema was affected")
		}
	}
	if !slices.Equal(c.DocumentURIs(), inventory) || !slices.Equal(fetch.callList(), before) {
		t.Error("accessors changed inventory or fetched documents")
	}
}
