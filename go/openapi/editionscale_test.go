package openapi_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Every normalization plan is computed once per node. Grow both use count
// and a shared declaration's size to expose n*n work. The best-of-five
// harness bounds 4x input at 12x time and 8x bytes.
func editionSharedDoc(n int) []byte {
	var paths, values []string
	for i := range n {
		paths = append(paths, fmt.Sprintf(`"/p%d":{"get":{"parameters":[{"$ref":"#/parameters/P"}]}}`, i))
		values = append(values, fmt.Sprintf(`"v%d"`, i))
	}
	return []byte(editionDoc("2.0", strings.Join(paths, ","), `"parameters":{"P":{"name":"q","in":"query","type":"array","collectionFormat":"pipes","items":{"type":"string","enum":[`+strings.Join(values, ",")+`]}}}`))
}

func editionAdditionalDoc(n int) []byte {
	var methods []string
	for i := range n {
		methods = append(methods, fmt.Sprintf(`"M%d":{"parameters":[{"$ref":"#/components/parameters/P"}]}`, i))
	}
	return []byte(editionDoc("3.2.1", `"/x":{"additionalOperations":{`+strings.Join(methods, ",")+`}}`, `"components":{"parameters":{"P":{"name":"p","in":"query","schema":{"type":"string"}}}}`))
}

// Regression check, not contract: loading and describing are linear in
// allocations and bytes for a Swagger 2.0 parameter many operations share and
// for many additional operations sharing a parameter, every normalization
// plan being computed once per node.
func TestEditionsNormalizationScale(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  func(int) []byte
	}{{"shared Swagger parameter", editionSharedDoc}, {"additional operations", editionAdditionalDoc}} {
		t.Run(tc.name, func(t *testing.T) {
			wantLinearAllocs(t, "load and describe", 250, func(n int) func() {
				doc := tc.doc(n)
				o := &once{t: t}
				return func() {
					c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
					if err != nil {
						o.fail("Parse: %v", err)
						return
					}
					ops := c.Operations()
					if len(ops) != n {
						o.fail("operations %d want %d", len(ops), n)
						return
					}
					for _, op := range ops {
						if op.Err != nil || len(op.Params) != 1 || op.Params[0].Err != nil {
							o.fail("operation %+v", op)
							return
						}
					}
				}
			})
		})
	}
}

// Regression check, not contract: a deeply nested Items chain and nested value
// must cost proportional to the chain, even though each level's
// collectionFormat is independently applied.
func TestEditionsNestedCollectionScale(t *testing.T) {
	wantLinearBytes(t, "nested Items", 64, func(n int) func() {
		schema := `{"type":"string"}`
		var value any = "leaf"
		for range n {
			schema = `{"type":"array","collectionFormat":"pipes","items":` + schema + `}`
			value = []any{value}
		}
		decl := `{"name":"p","in":"query",` + strings.TrimPrefix(schema, "{")
		doc := []byte(editionDoc("2.0", `"/x":{"get":{"parameters":[`+decl+`]}}`))
		o := &once{t: t}
		return func() {
			c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
			if err != nil {
				o.fail("Parse: %v", err)
				return
			}
			req, err := c.Prepare("GET /x", &openapi.Input{Params: map[string]any{"p": value}})
			if err != nil {
				o.fail("Prepare: %v", err)
				return
			}
			if req.HTTP.URL.RawQuery != "p=leaf" {
				o.fail("query %q", req.HTTP.URL.RawQuery)
			}
		}
	})
}

// Regression check, not contract: a large scheme body is not copied for every
// operation that describes it. That repeated URI requirements fetch their
// shared scheme once is contract (load.go, Loader: "Only a URI no loaded
// document identifies is admitted and fetched").
func TestEditionsSecurityURIScale(t *testing.T) {
	wantLinearAllocs(t, "shared URI security", 200, func(n int) func() {
		var paths, fields []string
		for i := range n {
			paths = append(paths, fmt.Sprintf(`"/p%d":{"get":{"security":[{"scheme.json#/S":[]}]}}`, i))
			fields = append(fields, fmt.Sprintf(`"x-%d":%d`, i, i))
		}
		doc := []byte(editionDoc("3.2.1", strings.Join(paths, ",")))
		scheme := `{"S":{"type":"apiKey","in":"header","name":"X-Key",` + strings.Join(fields, ",") + `}}`
		o := &once{t: t}
		return func() {
			fetches := 0
			l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
				fetches++
				if u != "https://api.example.test/scheme.json" {
					return nil, "", fmt.Errorf("unexpected URI %s", u)
				}
				return io.NopCloser(strings.NewReader(scheme)), "", nil
			}}
			c, err := l.Parse(context.Background(), doc, testDocURI, nil)
			if err != nil {
				o.fail("Parse: %v", err)
				return
			}
			ops := c.Operations()
			if len(ops) != n || fetches != 1 {
				o.fail("operations %d fetches %d", len(ops), fetches)
				return
			}
			for _, op := range ops {
				if op.Security[0].Schemes[0].Err != nil {
					o.fail("scheme: %v", op.Security[0].Schemes[0].Err)
					return
				}
			}
		}
	})
}

// Regression check, not contract: inspecting this input has bounded cost
// rather than copying accumulated methods at every chain level. The rest is
// contract: describe.go, Operation: "For additionalOperations, the nearest map
// supplies the operations and a duplicate field sets Err on each of them." The
// nearest whole map is selected, and farther maps are not unioned.
func TestEditionsAdditionalOperationsReferenceChainScale(t *testing.T) {
	wantLinearAllocs(t, "additionalOperations chain", 200, func(n int) func() {
		var items []string
		for i := range n {
			ref := ""
			if i+1 < n {
				ref = fmt.Sprintf(`"$ref":"#/components/pathItems/P%d",`, i+1)
			}
			items = append(items, fmt.Sprintf(`"P%d":{%s"additionalOperations":{"M%d":{}}}`, i, ref, i))
		}
		doc := []byte(editionDoc("3.2.1", `"/x":{"$ref":"#/components/pathItems/P0"}`, `"components":{"pathItems":{`+strings.Join(items, ",")+`}}`))
		o := &once{t: t}
		return func() {
			c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
			if err != nil {
				o.fail("Parse: %v", err)
				return
			}
			ops := c.Operations()
			if len(ops) != 1 || ops[0].Method != "M0" || ops[0].Err == nil {
				o.fail("nearest map must yield one defective M0 operation, got %+v", ops)
			}
		}
	})
}
