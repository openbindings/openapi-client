package openapi_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The scaling harness's 8x allocated-byte bound (scaleAllocBound) applies
// to fourfold document growth. Distinct identifiers at nested schema
// locations must not retain or repeatedly construct every full ancestor
// pointer. This public Parse fixture contains no operations or descriptor
// Sources.
func TestDocumentNestedIdentifierByteGrowth(t *testing.T) {
	ctx := t.Context()
	run := func(depth int) func() {
		var b strings.Builder
		b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{"S":`)
		for i := range depth {
			fmt.Fprintf(&b, `{"$anchor":"a%d","$ref":"#a%d","not":`, i, i)
		}
		b.WriteString(`{}`)
		b.WriteString(strings.Repeat("}", depth))
		b.WriteString(`}}}`)
		doc := []byte(b.String())
		f := func() {
			c, err := openapi.Parse(ctx, doc, testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			runtime.KeepAlive(c)
		}
		f() // Warm parser and package state outside measurement.
		return f
	}
	// Reuse the interleaved five-run allocation harness, but assert and
	// report bytes only: wall time and allocation count are not this gate.
	small, large := bestCosts(run(200), run(800))
	ratio := float64(large.bytes) / float64(max(small.bytes, 1))
	t.Logf("200/800 schemas: %d/%d allocated bytes (%.2fx)", small.bytes, large.bytes, ratio)
	if ratio > scaleAllocBound {
		t.Errorf("4x nested schemas allocated %.2fx bytes, want at most %dx", ratio, scaleAllocBound)
	}
}
