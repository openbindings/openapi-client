package openapi_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Inference-dependent context: a route whose inferred schema scope is
// contradicted by context learned only through that route becomes
// unresolved. ErrUnresolved and SchemaReference.Err preserve a per-part
// failure. Loader retains reached documents/context; ordinary independently
// routed contexts remain governed by the stronger existing carrier
// regressions. OAS 3.1.2 Parameter Object (4.8.12) still makes the newly
// identified example data.
func TestReview9InferenceDependentContextIsUnresolved(t *testing.T) {
	const bundle = "https://schemas.example.test/api/bundle.json"
	const inferredCarrier = "https://schemas.example.test/api/data-only/carrier-openapi.json"
	const otherCarrier = "https://schemas.example.test/api/carrier-openapi.json"
	const source = bundle + "#/P/example/payload"
	const bundleRaw = `{"P":{"name":"q","in":"query","schema":{"type":"object"},"example":{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"data-only/","payload":{"$ref":"carrier-openapi.json#/components/schemas/C"}}}}`
	const inferredRaw = `{"openapi":"3.1.2","info":{"title":"Inference-dependent carrier","version":"1"},"paths":{"/carrier":{"get":{"parameters":[{"$ref":"../bundle.json#/P"}],"responses":{"200":{"description":"ok"}}}}},"components":{"schemas":{"C":{}}}}`
	const otherRaw = `{"openapi":"3.1.2","info":{"title":"Different target","version":"1"},"paths":{},"components":{"schemas":{"C":{"type":"string"}}}}`
	documents := map[string]string{bundle: bundleRaw, inferredCarrier: inferredRaw, otherCarrier: otherRaw}
	fetch := newMemFetch(documents)
	c := schema9Parse(t, schema9Doc("3.1.2", `"Payload":{"$ref":"bundle.json#/P/example/payload"},"Stable":{"type":"integer"}`), fetch)
	before := fetch.callList()
	inventory := c.DocumentURIs()
	// No minimal fetch set is prescribed: an inference-dependent fetch cannot
	// be undone. Every successfully reached fixture document must stay visible.
	for _, uri := range before {
		if _, supplied := documents[uri]; supplied {
			if !slices.Contains(inventory, uri) || len(c.Document(uri)) == 0 {
				t.Errorf("reached document was discarded: %s", uri)
			}
		}
	}
	// The affected error's public location is left open: direct lookup,
	// whole References, or its edge. A successful affected edge is forbidden
	// even if another error is also returned.
	unresolved := func(uri, value string) {
		t.Helper()
		s, lookupErr := c.Schema(uri)
		if lookupErr != nil {
			if !errors.Is(lookupErr, openapi.ErrUnresolved) {
				t.Errorf("Schema(%s) error=%v; want ErrUnresolved", uri, lookupErr)
			}
			return
		}
		if s == nil {
			t.Fatalf("Schema(%s) returned no handle or error", uri)
		}
		refs, refsErr := s.References()
		failed := errors.Is(refsErr, openapi.ErrUnresolved)
		if refsErr != nil && !failed {
			t.Errorf("References(%s) error=%v; want ErrUnresolved", uri, refsErr)
		}
		if refsErr == nil && len(refs) != 1 {
			t.Errorf("References(%s) returned %d edges without a top-level error", uri, len(refs))
		}
		for _, ref := range refs {
			if ref.At != "/$ref" || ref.Keyword != "$ref" || ref.Value != value {
				t.Errorf("unexpected affected reference at %s: %+v", uri, ref)
			}
			if ref.Target != nil {
				t.Errorf("affected reference still exposes a successful target at %s: %+v", uri, ref)
			}
			if ref.Err != nil && !errors.Is(ref.Err, openapi.ErrUnresolved) {
				t.Errorf("affected edge error=%v; want ErrUnresolved", ref.Err)
			}
			failed = failed || errors.Is(ref.Err, openapi.ErrUnresolved)
		}
		if !failed {
			t.Errorf("affected schema %s reported no ErrUnresolved", uri)
		}
	}
	for range 2 {
		unresolved(source, "carrier-openapi.json#/components/schemas/C")
		unresolved(schema9Entry+"#/components/schemas/Payload", "bundle.json#/P/example/payload")
		for _, alias := range []string{"https://schemas.example.test/api/data-only/", "https://schemas.example.test/api/data-only/#/payload"} {
			if _, err := c.Schema(alias); err == nil {
				t.Errorf("invalidated generic identifier alias succeeded: %s", alias)
			}
		}
		declared := schema9Get(t, c, bundle+"#/P/schema")
		if declared.Source() != bundle+"#/P/schema" || declared.Base() != bundle || declared.Dialect() != schema9OAS31 || string(declared.Raw()) != `{"type":"object"}` || len(schema9Refs(t, declared)) != 0 {
			t.Error("unaffected Parameter schema lost its context")
		}
		stable := schema9Get(t, c, schema9Entry+"#/components/schemas/Stable")
		if stable.Source() != schema9Entry+"#/components/schemas/Stable" || stable.Base() != schema9Entry || stable.Dialect() != schema9OAS31 || string(stable.Raw()) != `{"type":"integer"}` || len(schema9Refs(t, stable)) != 0 {
			t.Error("independent stable schema was affected")
		}
	}
	if !slices.Equal(c.DocumentURIs(), inventory) {
		t.Error("accessors changed the loaded inventory")
	}
	if !slices.Equal(fetch.callList(), before) {
		t.Errorf("accessors fetched: before=%q after=%q", before, fetch.callList())
	}
}
