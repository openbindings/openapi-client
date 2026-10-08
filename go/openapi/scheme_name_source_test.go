package openapi_test

import (
	"errors"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// describe.go, Operation: "a scheme whose name in an OpenAPI 3.2 requirement
// is neither a component name nor a URI that can be resolved has that Security
// Requirement Object's location as its Source"; SecurityScheme.Source: "It is
// empty for a scheme the document never declares, but in OpenAPI 3.2 it is
// then the location of the Security Requirement Object that writes the name".
// A plain name no component has, a fragment reaching nothing in the document,
// and a URI of a document that cannot be fetched, in an operation's
// requirement and in a root requirement the operation inherits, each have the
// location of the requirement that writes them, the same name in two
// requirements included; each Err still wraps ErrUnresolved. A scheme that
// resolves keeps its component's Source.
func TestUnresolvableSchemeNameSource(t *testing.T) {
	doc := editionDoc("3.2.0", `"/a":{"get":{"operationId":"a","security":[{"k":[]},{"nope":[]},{"#/components/securitySchemes/Missing":[],"k":[]}]}},
		"/b":{"get":{"operationId":"b"}}`,
		`"security":[{"nope":[]},{"other.json#/x":[]}]`,
		`"components":{"securitySchemes":{"k":{"type":"apiKey","in":"header","name":"K"}}}`)
	c, err := (&openapi.Loader{Fetch: mapFetch(nil)}).Parse(t.Context(), []byte(doc), testDocURI, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	const (
		k  = testDocURI + "#/components/securitySchemes/k"
		op = testDocURI + "#/paths/~1a/get/security/"
	)
	for key, want := range map[string][][]string{
		"a": {{k}, {op + "1"}, {op + "2", k}},
		"b": {{testDocURI + "#/security/0"}, {testDocURI + "#/security/1"}},
	} {
		alternatives := mustOp(t, c, key).Security
		if len(alternatives) != len(want) {
			t.Fatalf("%s: %d alternatives, want %d", key, len(alternatives), len(want))
		}
		for i, r := range alternatives {
			if len(r.Schemes) != len(want[i]) {
				t.Fatalf("%s: alternative %d has %d schemes, want %d", key, i, len(r.Schemes), len(want[i]))
			}
			for j, s := range r.Schemes {
				if s.Source != want[i][j] {
					t.Errorf("%s: alternative %d, scheme %q: Source %q, want %q", key, i, s.Name, s.Source, want[i][j])
				}
				if resolved := want[i][j] == k; resolved != (s.Err == nil) || !resolved && !errors.Is(s.Err, openapi.ErrUnresolved) {
					t.Errorf("%s: alternative %d, scheme %q: Err %v", key, i, s.Name, s.Err)
				}
			}
		}
	}
}
