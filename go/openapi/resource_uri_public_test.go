package openapi_test

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Keep load-time URI storage costs visible alongside the existing first-lookup
// deep-resource gate. Its fixtures also verify exact Base, Source and Raw values.
func BenchmarkResourceURIParse(b *testing.B) {
	for _, depth := range []int{128, 512} {
		f := review9Deep(depth, true)
		raw := []byte(f.doc)
		b.Run(fmt.Sprint(depth), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c, err := openapi.Parse(context.Background(), raw, review9CostEntry, nil)
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(c)
			}
		})
	}
}
