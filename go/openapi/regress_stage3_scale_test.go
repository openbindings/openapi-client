package openapi_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the stage 3 review round, cost (stage 3 ledger,
// "Review round (8101e19)"): F5 (retained memory of a secured operation)
// and F17 (per-call selection cost). The harness is in
// regress2_scale_test.go.

// fourParamOps returns a document of n operations, each with a path, two
// query and a header parameter, under root security with one apiKey header
// scheme when secured; no parameter is at the scheme's destination.
func fourParamOps(n int, secured bool) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],`)
	if secured {
		b.WriteString(`"security":[{"k":[]}],"components":{"securitySchemes":{"k":{"type":"apiKey","in":"header","name":"X-Key"}}},`)
	}
	b.WriteString(`"paths":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"/p%d/{id}":{"get":{"operationId":"op%d","parameters":[{"name":"id","in":"path","required":true},{"name":"a","in":"query"},{"name":"b","in":"query"},{"name":"X-Trace","in":"header"}]}}`, i, i)
	}
	b.WriteString(`}}`)
	return []byte(b.String())
}

// F5: "derive destinations from shape()'s map; retain nothing extra per
// secured operation" (panel F5: o.dests kept about 400 bytes per secured
// operation for a parameter-credential collision real documents almost never
// have). Dev loop, "Hostile-input cost is a standing gate": every compiled
// form is computed at most once per document node. The same operations
// retain the same memory, give or take noise, with root security as
// without; the root's alternatives are shared.
func TestSecuredOperationsRetainNothingExtra(t *testing.T) {
	const n = 4000
	keep := func(secured bool) int64 {
		c, err := openapi.Parse(context.Background(), fourParamOps(n, secured), testDocURI, nil)
		if err != nil {
			t.Fatal(err)
		}
		return retainedBy(func() any { return c.Operations() })
	}
	plain, secured := keep(false), keep(true)
	per := (secured - plain) / n
	t.Logf("retained after Operations(): %d bytes without security, %d with (%d bytes per operation)", plain, secured, per)
	if per > 64 {
		t.Errorf("a secured operation retains %d bytes more than the same operation without security; want nothing extra", per)
	}
}

// identicalAlternatives returns a document whose operation "a" lists n
// copies of one alternative of two schemes with long names.
func identicalAlternatives(n int) []byte {
	const alt = `{"scheme_with_a_rather_long_name_number_one":[],"scheme_with_a_rather_long_name_number_two":[]}`
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/a":{"get":{"operationId":"a","security":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(alt)
	}
	b.WriteString(`]}}},"components":{"securitySchemes":{` +
		`"scheme_with_a_rather_long_name_number_one":{"type":"apiKey","in":"header","name":"X-One"},` +
		`"scheme_with_a_rather_long_name_number_two":{"type":"apiKey","in":"header","name":"X-Two"}}}}`)
	return []byte(b.String())
}

// F17: "whether one alternative selects itself is decided at plan time
// (P1)" (panel F17: each call compared every alternative's key, about 20 ns
// per alternative on every call). Dev loop, P1: "every compiled or decoded
// form is computed at most once per document node". doc.go, Configuration:
// "One security alternative selects itself", and identical alternatives are
// one. Preparing the call after the operation is compiled costs the same
// whatever the number of copies: sixteen times the copies may cost at most
// three times as much, where a per-call scan costs about sixteen.
func TestIdenticalAlternativesSelectInConstantTime(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	prepare := func(n int) func() {
		c := warmClient(t, identicalAlternatives(n), &openapi.Options{Credentials: map[string]openapi.Credential{
			"scheme_with_a_rather_long_name_number_one": openapi.Secret("one-1Qa"),
			"scheme_with_a_rather_long_name_number_two": openapi.Secret("two-2Ws"),
		}})
		if _, err := c.Prepare("a", nil); err != nil {
			t.Fatalf("Prepare with %d identical alternatives: %v", n, err)
		}
		return func() {
			for range 200 {
				c.Prepare("a", nil)
			}
		}
	}
	ca, cb := bestCosts(prepare(250), prepare(4000))
	ratio := float64(cb.d) / float64(ca.d)
	t.Logf("200 Prepares: %v with 250 identical alternatives, %v with 4,000 (%.1fx)", ca.d, cb.d, ratio)
	if ratio > 3 {
		t.Errorf("sixteen times the identical alternatives made each call %.1f times as costly; want the choice made once", ratio)
	}
}
