package provider_test

import (
	"context"
	"testing"

	"github.com/openbindings/openapi-client/go/provider"
)

func TestAnalyzeProjectionIsDetachedAndComplete(t *testing.T) {
	source := provider.Source{Content: []byte(`
openapi: 3.1.2
info: {title: Projection, version: "1"}
servers: [{url: https://api.example.test}]
paths:
  /widgets/{id}:
    post:
      operationId: createWidget
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties: {name: {type: string}}
              required: [name]
      responses:
        "201":
          description: created
          content: {application/json: {schema: {type: object}}}
`)}
	first, err := provider.AnalyzeProjection(context.Background(), source, provider.ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.OpenAPI3 == nil || first.OpenAPI3.Name != "Projection" {
		t.Fatalf("projection = %#v", first)
	}
	operation, ok := first.OpenAPI3.Operations["createWidget"]
	if !ok || operation.Input == nil || operation.Output == nil {
		t.Fatalf("operation = %#v, found=%v", operation, ok)
	}
	binding := first.OpenAPI3.Bindings["createWidget"]
	if binding.Selector != "#/paths/~1widgets~1{id}/post" {
		t.Fatalf("binding = %#v", binding)
	}
	first.OpenAPI3.Operations["createWidget"] = provider.ProjectionOperation{}
	second, err := provider.AnalyzeProjection(context.Background(), source, provider.ClientOptions{})
	if err != nil || second.OpenAPI3 == nil || second.OpenAPI3.Operations["createWidget"].Input == nil {
		t.Fatalf("detached second projection = %#v, err=%v", second, err)
	}
}
