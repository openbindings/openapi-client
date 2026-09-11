package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openbindings/openapi-client/go/provider"
)

func TestScalarResponseProjectionRetainsDeclaredSchema(t *testing.T) {
	for _, kind := range []string{"integer", "number", "boolean", "string"} {
		for _, referenced := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ref=%v", kind, referenced), func(t *testing.T) {
				schema := map[string]any{"type": kind, "description": "declared scalar"}
				var selected any = schema
				if referenced {
					selected = map[string]any{"$ref": "#/components/schemas/Value"}
				}
				document := map[string]any{
					"openapi": "3.2.0", "info": map[string]any{"title": "Scalar", "version": "1"},
					"servers":    []any{map[string]any{"url": "https://api.example"}},
					"components": map[string]any{"schemas": map[string]any{"Value": schema}},
					"paths": map[string]any{"/scalar": map[string]any{"get": map[string]any{
						"operationId": "read", "responses": map[string]any{"200": map[string]any{
							"description": "ok", "content": map[string]any{"text/plain": map[string]any{"schema": selected}},
						}},
					}}},
				}
				content, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				analysis, err := provider.AnalyzeProjection(context.Background(), provider.Source{Content: content}, provider.ClientOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if analysis.OpenAPI3 == nil {
					t.Fatalf("missing projection: %+v", analysis)
				}
				output := analysis.OpenAPI3.Operations["read"].Output
				actual, err := json.Marshal(output)
				if err != nil {
					t.Fatal(err)
				}
				expected, _ := json.Marshal(schema)
				if string(actual) != string(expected) {
					t.Fatalf("output=%s want=%s", actual, expected)
				}
			})
		}
	}
}
