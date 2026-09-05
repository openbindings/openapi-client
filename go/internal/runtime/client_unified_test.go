package openapiclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type unifiedClientRoundTripper func(*http.Request) (*http.Response, error)

func (f unifiedClientRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestUnifiedClientLoadsAndInvokesSwagger20(t *testing.T) {
	var observed *http.Request
	httpClient := &http.Client{Transport: unifiedClientRoundTripper(func(request *http.Request) (*http.Response, error) {
		observed = request.Clone(request.Context())
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"name":"Ada"}`)),
			Request:    request,
		}, nil
	})}
	client, err := Load(context.Background(), Source{Content: []byte(`
swagger: "2.0"
info: {title: Swagger client, version: "1"}
schemes: [https]
host: api.example.test
basePath: /v1
produces: [application/json]
paths:
  /pets/{id}:
    get:
      operationId: getPet
      parameters:
        - {name: id, in: path, required: true, type: string}
      responses:
        "200": {description: pet, schema: {type: object}}
`)}, ClientOptions{HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	if client.Edition() != EditionSwagger20 {
		t.Fatalf("edition = %q", client.Edition())
	}
	operations := client.Operations()
	if len(operations) != 1 || operations[0].OperationID != "getPet" || operations[0].WireMethod != "GET" || operations[0].Additional {
		t.Fatalf("operations = %#v", operations)
	}
	result, err := client.Call(context.Background(), OperationID("getPet"), Input{
		Parameters: Parameters{Path: map[string]any{"id": "a/b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.OpenAPI.ResponseKey != "200" {
		t.Fatalf("result = %#v", result)
	}
	if observed == nil || observed.Method != "GET" || observed.URL.String() != "https://api.example.test/v1/pets/a%2Fb" {
		t.Fatalf("request = %#v", observed)
	}
}

func TestUnifiedClientReportsExactOpenAPI32EditionAndMethods(t *testing.T) {
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: Methods, version: "1"}
paths:
  /cache:
    query:
      operationId: findCached
      responses: {"204": {description: done}}
    additionalOperations:
      PURGE:
        operationId: purgeCache
        responses: {"204": {description: done}}
`)}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if client.Edition() != EditionOpenAPI320 {
		t.Fatalf("edition = %q", client.Edition())
	}
	operations := client.Operations()
	if len(operations) != 2 {
		t.Fatalf("operations = %#v", operations)
	}
	seen := map[string]OperationInfo{}
	for _, operation := range operations {
		seen[operation.OperationID] = operation
	}
	if seen["findCached"].WireMethod != "QUERY" || seen["findCached"].Additional {
		t.Fatalf("QUERY = %#v", seen["findCached"])
	}
	if seen["purgeCache"].WireMethod != "PURGE" || !seen["purgeCache"].Additional {
		t.Fatalf("PURGE = %#v", seen["purgeCache"])
	}
}
