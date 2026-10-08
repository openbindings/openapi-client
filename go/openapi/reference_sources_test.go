package openapi_test

import (
	"errors"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// describe.go, Operation: "A part written as a Reference Object that cannot
// be resolved has that Reference Object's location as its Source, a header's
// included, and a security scheme's whether its requirement names it by
// component name or by an OpenAPI 3.2 URI"; Param.Source: "where the value is
// declared"; SecurityScheme.Source: "where the scheme is declared". A response
// header and an Encoding header written as such a reference, directly or
// through a chain, have the location they are written at, and a scheme
// component written as one has its component's location whichever way the
// requirement names it. Each Err still wraps ErrUnresolved. A parameter, the
// rule's established case, is the control.
func TestUnresolvedReferenceSources(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.0", `"/a":{"post":{"operationId":"a",
		"parameters":[{"$ref":"#/components/parameters/Missing"}],
		"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"f":{}}},
			"encoding":{"f":{"headers":{"X-E":{"$ref":"#/components/headers/Missing"},"X-C":{"$ref":"#/components/headers/Chain"}}}}}}},
		"responses":{"200":{"description":"d","headers":{"X-H":{"$ref":"#/components/headers/Missing"},"X-C":{"$ref":"#/components/headers/Chain"}}}},
		"security":[{"#/components/securitySchemes/s1":[]},{"s1":[]}]}}`,
		`"components":{"headers":{"Chain":{"$ref":"#/components/headers/Missing"}},"securitySchemes":{"s1":{"$ref":"#/components/securitySchemes/Missing"}}}`), nil)
	op := mustOp(t, c, "a")
	const at = testDocURI + "#/paths/~1a/post"
	want := map[string]struct {
		p   *openapi.Param
		src string
	}{
		"parameter":       {param(t, op, 0), at + "/parameters/0"},
		"response header": {headerNamed(t, response(t, op, 0).Headers, "X-H"), at + "/responses/200/headers/X-H"},
		"response chain":  {headerNamed(t, response(t, op, 0).Headers, "X-C"), at + "/responses/200/headers/X-C"},
		"Encoding header": {headerNamed(t, reqMedia(t, op, 0).Encoding[0].Headers, "X-E"), at + "/requestBody/content/multipart~1form-data/encoding/f/headers/X-E"},
		"Encoding chain":  {headerNamed(t, reqMedia(t, op, 0).Encoding[0].Headers, "X-C"), at + "/requestBody/content/multipart~1form-data/encoding/f/headers/X-C"},
	}
	for what, w := range want {
		if w.p.Source != w.src || !errors.Is(w.p.Err, openapi.ErrUnresolved) {
			t.Errorf("%s: Source %q, Err %v; want Source %q and an Err wrapping ErrUnresolved", what, w.p.Source, w.p.Err, w.src)
		}
	}
	if len(op.Security) != 2 {
		t.Fatalf("Security %+v, want two alternatives", op.Security)
	}
	for _, r := range op.Security {
		s := r.Schemes[0]
		if s.Source != testDocURI+"#/components/securitySchemes/s1" || !errors.Is(s.Err, openapi.ErrUnresolved) {
			t.Errorf("scheme %q: Source %q, Err %v; want Source %s and an Err wrapping ErrUnresolved", s.Name, s.Source, s.Err, testDocURI+"#/components/securitySchemes/s1")
		}
	}
}

// headerNamed returns the header named name in headers.
func headerNamed(t *testing.T, headers []*openapi.Param, name string) *openapi.Param {
	t.Helper()
	for _, h := range headers {
		if h.Name == name {
			return h
		}
	}
	t.Fatalf("no header %s among %d", name, len(headers))
	return nil
}
