package openapi_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The once-per-node invariant covers physical schema descendants reached
// again through overlapping anchored roots. This 20/80-node case measures
// allocation count through Parse rather than adding production visit
// counters or timing assertions.
func TestDocumentOverlappingDiscoveryAllocationGrowth(t *testing.T) {
	var allocations [2]float64
	for j, n := range []int{20, 80} {
		schema := `{}`
		for i := n - 1; i >= 0; i-- {
			schema = fmt.Sprintf(`{"$anchor":"a%d","$ref":"#a%d","not":%s}`, i, i, schema)
		}
		doc := []byte(`{"openapi":"3.1.0","paths":{},"components":{"schemas":{"S":` + schema + `}}}`)
		allocations[j] = testing.AllocsPerRun(1, func() {
			if _, err := openapi.Parse(t.Context(), doc, testDocURI, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Logf("20/80 schemas: %.0f/%.0f allocations", allocations[0], allocations[1])
	if allocations[1] > 10*allocations[0] {
		t.Errorf("4x nested schemas took %.2fx allocations, want at most 10x", allocations[1]/allocations[0])
	}
}

// Known string values must not pay arbitrary-precision numeric conversion
// costs. The test compares equal-length 0x and xx strings through the public
// Parse and Document paths, for each contracted string form. A 32-allocation
// margin accommodates small parser bookkeeping differences but excludes
// discarded big-integer conversion.
func TestYAMLKnownStringsAvoidNumericAllocation(t *testing.T) {
	for _, style := range []struct{ name, prefix, suffix string }{
		{"double quoted", `x-v: "`, "\"\n"},
		{"single quoted", "x-v: '", "'\n"},
		{"block", "x-v: |-\n  ", "\n"},
		{"explicit string", "x-v: !!str ", "\n"},
		{"non-specific tag", "x-v: ! ", "\n"},
	} {
		t.Run(style.name, func(t *testing.T) {
			var allocations [2]float64
			for i, prefix := range []string{"xx", "0x"} {
				value := prefix + strings.Repeat("f", 64<<10)
				doc := []byte(yamlHead + style.prefix + value + style.suffix)
				want := `"` + value + `"`
				allocations[i] = testing.AllocsPerRun(1, func() {
					c := parsed(t, doc)
					if got := string(c.Document(testDocURI + "#/x-v")); got != want {
						t.Fatal("known string changed")
					}
				})
			}
			t.Logf("xx/0x: %.0f/%.0f allocations", allocations[0], allocations[1])
			if allocations[1] > allocations[0]+32 {
				t.Errorf("known hex-looking string added %.0f allocations, want at most 32", allocations[1]-allocations[0])
			}
		})
	}
}
