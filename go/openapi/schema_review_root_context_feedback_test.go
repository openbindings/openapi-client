package openapi_test

import (
	"slices"
	"testing"
)

// The ratified Tier 2 feedback boundary also applies when the disproved
// inferred resource is the versionless document root. Loader's known Parameter
// context must survive independently; root $schema/$id cues are not an explicit
// Schema reference. ErrUnresolved location latitude is shared with the frozen
// feedback oracle. No competing explicit root-Schema interpretation is added.
func TestReview9RootContextFeedbackPreservesParameterSchema(t *testing.T) {
	const bundle = "https://schemas.example.test/api/root-feedback.json"
	const inferredCarrier = "https://schemas.example.test/api/data-only/root-feedback-carrier.json"
	const otherCarrier = "https://schemas.example.test/api/root-feedback-carrier.json"
	const source = bundle + "#/payload"
	const bundleRaw = `{"name":"q","in":"query","schema":{"type":"object"},"$schema":"` + schema9JSON + `","$id":"data-only/","payload":{"$ref":"root-feedback-carrier.json#/components/schemas/C"}}`
	const inferredRaw = `{"openapi":"3.1.2","info":{"title":"Root feedback carrier","version":"1"},"paths":{"/carrier":{"get":{"parameters":[{"$ref":"../root-feedback.json"}],"responses":{"200":{"description":"ok"}}}}},"components":{"schemas":{"C":{}}}}`
	const otherRaw = `{"openapi":"3.1.2","info":{"title":"Other target","version":"1"},"paths":{},"components":{"schemas":{"C":{"type":"string"}}}}`
	documents := map[string]string{bundle: bundleRaw, inferredCarrier: inferredRaw, otherCarrier: otherRaw}
	fetch := newMemFetch(documents)
	c := schema9Parse(t, schema9Doc("3.1.2", `"Payload":{"$ref":"root-feedback.json#/payload"},"Stable":{"type":"integer"}`), fetch)
	before := fetch.callList()
	inventory := c.DocumentURIs()
	// Retain reached documents without prescribing a minimal Parse fetch set.
	for _, uri := range before {
		if _, supplied := documents[uri]; supplied && (!slices.Contains(inventory, uri) || len(c.Document(uri)) == 0) {
			t.Errorf("reached document was discarded: %s", uri)
		}
	}
	for range 2 {
		for _, node := range []review9FeedbackNode{
			{uri: source, value: "root-feedback-carrier.json#/components/schemas/C"},
			{uri: schema9Entry + "#/components/schemas/Payload", value: "root-feedback.json#/payload"},
		} {
			got := review9FeedbackRead{}
			got.schema, got.lookupErr = c.Schema(node.uri)
			if got.lookupErr == nil && got.schema != nil {
				got.refsCalled = true
				got.refs, got.refsErr = got.schema.References()
			}
			review9FeedbackWantUnresolved(t, node, got)
		}
		for _, alias := range []string{"https://schemas.example.test/api/data-only/", "https://schemas.example.test/api/data-only/#/payload"} {
			if _, err := c.Schema(alias); err == nil {
				t.Errorf("invalidated root identifier alias succeeded: %s", alias)
			}
		}
		declared := schema9Get(t, c, bundle+"#/schema")
		if declared.Source() != bundle+"#/schema" || declared.Base() != bundle || declared.Dialect() != schema9OAS31 || string(declared.Raw()) != `{"type":"object"}` || len(schema9Refs(t, declared)) != 0 {
			t.Error("unaffected root Parameter schema lost its context")
		}
		// The public API has no standalone Parameter-by-URI accessor. Document
		// proves authored content retention; /schema above proves schema usability.
		if string(c.Document(bundle)) != bundleRaw {
			t.Error("root Parameter document content was not retained")
		}
		stable := schema9Get(t, c, schema9Entry+"#/components/schemas/Stable")
		if stable.Source() != schema9Entry+"#/components/schemas/Stable" || stable.Base() != schema9Entry || stable.Dialect() != schema9OAS31 || string(stable.Raw()) != `{"type":"integer"}` || len(schema9Refs(t, stable)) != 0 {
			t.Error("independent stable schema was affected")
		}
	}
	if !slices.Equal(c.DocumentURIs(), inventory) || !slices.Equal(fetch.callList(), before) {
		t.Error("accessors changed inventory or fetched documents")
	}
}
