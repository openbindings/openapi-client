package openapi_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Grow use count and one shared schema/operation declaration together.
// Listing descriptions must share normalized plans and defer synthetic Raw
// construction, rather than materializing shared content once per operation.
func stage6SharedForm(n int) []byte {
	var paths, values []string
	for i := range n {
		paths = append(paths, fmt.Sprintf(`"/p%d":{"post":{"parameters":[{"$ref":"#/parameters/P"}]}}`, i))
		values = append(values, fmt.Sprintf(`"%s%d"`, strings.Repeat("v", 128), i))
	}
	return []byte(editionDoc("2.0", strings.Join(paths, ","), `"consumes":["application/x-www-form-urlencoded"],"parameters":{"P":{"name":"p","in":"formData","type":"string","enum":[`+strings.Join(values, ",")+`]}}`))
}

func stage6SharedMethod(n int, additional bool) []byte {
	var paths, tags []string
	for i := range n {
		paths = append(paths, fmt.Sprintf(`"/p%d":{"$ref":"#/components/pathItems/P"}`, i))
		tags = append(tags, fmt.Sprintf(`"tag-%04d-%s"`, i, strings.Repeat("t", 52)))
	}
	op := `{"tags":[` + strings.Join(tags, ",") + `],"responses":{"200":{"description":"ok"}}}`
	item := `{"get":` + op + `}`
	if additional {
		item = `{"additionalOperations":{"CUSTOM":` + op + `}}`
	}
	return []byte(editionDoc("3.2.1", strings.Join(paths, ","), `"components":{"pathItems":{"P":`+item+`}}`))
}

func stage6DescribeShared(t *testing.T, doc []byte, n int) *openapi.Client {
	t.Helper()
	c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	ops := c.Operations()
	if len(ops) != n {
		t.Fatalf("operations %d want %d", len(ops), n)
	}
	for _, op := range ops {
		if op.Err != nil {
			t.Fatal(op.Err)
		}
	}
	return c
}

func TestStage6SharedNormalizationScale(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  func(int) []byte
	}{
		{"fixed method control", func(n int) []byte { return stage6SharedMethod(n, false) }},
		{"additional method", func(n int) []byte { return stage6SharedMethod(n, true) }},
		{"Swagger formData", stage6SharedForm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantLinearBytes(t, "load and describe", 64, func(n int) func() { doc := tc.doc(n); return func() { stage6DescribeShared(t, doc, n) } })
		})
	}
}

// Allocations discarded after compilation must not obscure a second retained
// copy per operation. Use the existing two-GC retainedBy helper and the same
// 8x bound for 4x input. This is a scaling bound, not a new realistic-corpus
// memory budget; source creation is outside the measurements.
func TestStage6SharedNormalizationRetainedScale(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  func(int) []byte
	}{
		{"fixed method control", func(n int) []byte { return stage6SharedMethod(n, false) }},
		{"additional method", func(n int) []byte { return stage6SharedMethod(n, true) }},
		{"Swagger formData", stage6SharedForm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var retained [2]int64
			for i, n := range []int{64, 256} {
				doc := tc.doc(n)
				best := int64(1<<63 - 1)
				for range 3 {
					got := retainedBy(func() any { return stage6DescribeShared(t, doc, n) })
					if got > 0 {
						best = min(best, got)
					}
				}
				if best == 1<<63-1 {
					t.Fatal("retained measurements were nonpositive")
				}
				retained[i] = best
			}
			ratio := float64(retained[1]) / float64(retained[0])
			t.Logf("retained 64=%d, 256=%d, growth %.2fx", retained[0], retained[1], ratio)
			if ratio > scaleAllocBound {
				t.Errorf("4x input retains %.2fx bytes, want at most %dx", ratio, scaleAllocBound)
			}
		})
	}
}
