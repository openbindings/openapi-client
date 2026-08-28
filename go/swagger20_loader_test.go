package openapiclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func swagger20Document(paths string) []byte {
	return []byte(`{"swagger":"2.0","info":{"title":"t","version":"1"},"paths":` + paths + `}`)
}

func TestLoadSwagger20ExactGateAndRawPreservation(t *testing.T) {
	client, err := LoadSwagger20(context.Background(), Swagger20Source{Content: []byte(`
swagger: "2.0"
openapi: "3.2.0"
info: {title: t, version: "1"}
paths: {}
x-raw: {empty: "", explicit: false}
`)}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.Document().Swagger(); got != "2.0" {
		t.Fatalf("Swagger() = %q", got)
	}
	encoded, err := json.Marshal(client.Document())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["openapi"] != "3.2.0" {
		t.Fatalf("dual discriminator was not retained as an inert unknown field: %s", encoded)
	}
	extension := raw["x-raw"].(map[string]any)
	if extension["empty"] != "" || extension["explicit"] != false {
		t.Fatalf("presence-sensitive raw members changed: %#v", extension)
	}
}

func TestLoadSwagger20UsesYAML122CoreResolution(t *testing.T) {
	client, err := LoadSwagger20(context.Background(), Swagger20Source{Content: []byte(`
swagger: "2.0"
info: {title: YAML 1.2.2, version: "1"}
paths: {}
x-legacy-octal: 0777
x-binary-looking: 0b10
x-octal: 0o10
x-signed-octal-looking: -0o10
x-date: 2026-08-28
x-yes: yes
`)}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(client.Document())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["x-legacy-octal"] != float64(777) || raw["x-binary-looking"] != "0b10" || raw["x-octal"] != float64(8) || raw["x-signed-octal-looking"] != "-0o10" || raw["x-date"] != "2026-08-28" || raw["x-yes"] != "yes" {
		t.Fatalf("YAML 1.2.2 scalar resolution changed: %#v", raw)
	}
}

func TestLoadSwagger20ClosedLoadGates(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"root array", `[]`},
		{"missing swagger", `{"info":{"title":"t","version":"1"},"paths":{}}`},
		{"numeric swagger", `{"swagger":2.0,"info":{"title":"t","version":"1"},"paths":{}}`},
		{"nearby swagger", `{"swagger":"2.0.0","info":{"title":"t","version":"1"},"paths":{}}`},
		{"openapi marker only", `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{}}`},
		{"duplicate key", "swagger: \"2.0\"\ninfo: {title: t, version: '1'}\npaths: {}\nswagger: \"2.0\"\n"},
		{"non-string key", "swagger: \"2.0\"\ninfo: {title: t, version: '1'}\npaths: {}\ntrue: value\n"},
		{"non-json tag", "swagger: \"2.0\"\ninfo: {title: !duration 10m, version: '1'}\npaths: {}\n"},
		{"non-json scalar", "swagger: \"2.0\"\ninfo: {title: .nan, version: '1'}\npaths: {}\n"},
		{"multiple documents", "swagger: \"2.0\"\ninfo: {title: t, version: '1'}\npaths: {}\n---\n{}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadSwagger20(context.Background(), Swagger20Source{Content: []byte(test.content)}, ClientOptions{}); err == nil {
				t.Fatal("expected load refusal")
			}
		})
	}
}

func TestPrepareSwagger20PathsDefectsRefuseAfterClosedLoadGates(t *testing.T) {
	for _, content := range []string{
		`{"swagger":"2.0","info":{"title":"t","version":"1"}}`,
		`{"swagger":"2.0","info":{"title":"t","version":"1"},"paths":[]}`,
	} {
		client, err := LoadSwagger20(context.Background(), Swagger20Source{Content: []byte(content)}, ClientOptions{})
		if err != nil {
			t.Fatalf("paths declaration defect was incorrectly classified as a load gate: %v", err)
		}
		_, err = NewEngine(nil).PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
			Source: Swagger20Source{Document: client.Document()}, Ref: "#/paths/~1pets/get",
		})
		var execution *ExecutionError
		if !errors.As(err, &execution) || execution.Code != CodeRefused {
			t.Fatalf("prepare error = %#v, want %s", err, CodeRefused)
		}
	}
}

func TestPrepareSwagger20SelectorGrammarAndDirectOperation(t *testing.T) {
	document := swagger20Document(`{"/pets/{id}":{"get":{"operationId":"getPet","responses":{"204":{"description":"ok"}}}}}`)
	engine := NewEngine(nil)
	prepared, err := engine.PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
		Source: Swagger20Source{Content: document}, Ref: "#/paths/~1pets~1{id}/get",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.Info(); got.Path != "/pets/{id}" || got.Method != GET || got.OperationID != "getPet" {
		t.Fatalf("operation info = %#v", got)
	}

	invalid := []string{
		"#/paths/~1pets~1{id}/GET",
		"#/paths/~1pets~1%7Bid%7D/get",
		"#/paths//pets/{id}/get",
		"#/paths/~2pets/get",
		"#/paths/~1pets~1{id}/trace",
		"#/paths/~1pets~1{id}",
		"/paths/~1pets~1{id}/get",
	}
	for _, selector := range invalid {
		if _, err := engine.PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
			Source: Swagger20Source{Content: document}, Ref: selector,
		}); err == nil {
			t.Errorf("selector %q was accepted", selector)
		}
	}

	client, err := LoadSwagger20(context.Background(), Swagger20Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	operations := client.Operations()
	if len(operations) != 1 || operations[0].Ref != "#/paths/~1pets~1{id}/get" {
		t.Fatalf("operations = %#v", operations)
	}
}

type swagger20RoundTripper func(*http.Request) (*http.Response, error)

func (f swagger20RoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func swagger20Response(request *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestPrepareSwagger20Draft03ReferencesAndRetrievalBase(t *testing.T) {
	var requested []string
	client := &http.Client{Transport: swagger20RoundTripper(func(request *http.Request) (*http.Response, error) {
		requested = append(requested, request.URL.String())
		switch request.URL.String() {
		case "https://docs.example/entry":
			finalRequest := request.Clone(request.Context())
			finalRequest.URL, _ = finalRequest.URL.Parse("https://cdn.example/root/swagger.json")
			return swagger20Response(finalRequest, `
swagger: "2.0"
info: {title: redirected, version: "1"}
paths:
  /pets:
    $ref: "parts/path.yaml#/aliases/PetPath"
`), nil
		case "https://cdn.example/root/parts/path.yaml":
			return swagger20Response(request, `
aliases:
  PetPath:
    $ref: "#/real"
real:
  get:
    operationId: externalPet
    responses:
      "204": {description: ok}
`), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("missing")), Request: request}, nil
		}
	})}

	prepared, err := NewEngine(client).PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
		Source: Swagger20Source{Location: "https://docs.example/entry"}, Ref: "#/paths/~1pets/get",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Info().OperationID != "externalPet" {
		t.Fatalf("operation = %#v", prepared.Info())
	}
	if len(requested) != 2 || requested[1] != "https://cdn.example/root/parts/path.yaml" {
		t.Fatalf("requested = %v", requested)
	}
}

func TestPrepareSwagger20RebindsLoadedDocumentToPreparationClient(t *testing.T) {
	loaded, err := LoadSwagger20(context.Background(), Swagger20Source{
		Location: "https://docs.example/swagger.json",
		Content:  swagger20Document(`{"/pets":{"$ref":"parts/path.json#/item"}}`),
	}, ClientOptions{HTTPClient: &http.Client{Transport: swagger20RoundTripper(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("LoadSwagger20 retrieved an unselected reference: %s", request.URL)
		return nil, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	prepareClient := &http.Client{Transport: swagger20RoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://docs.example/parts/path.json" {
			t.Fatalf("reference URI = %s", request.URL)
		}
		return swagger20Response(request, `{"item":{"get":{"responses":{"204":{"description":"ok"}}}}}`), nil
	})}
	if _, err := NewEngine(prepareClient).PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
		Source: Swagger20Source{Document: loaded.Document()}, Ref: "#/paths/~1pets/get",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareSwagger20ReferenceConfinementAndCycles(t *testing.T) {
	document := []byte(`{
  "swagger":"2.0",
  "info":{"title":"refs","version":"1"},
  "base":{"get":{"operationId":"baseGet","responses":{"204":{"description":"ok"}}},"post":{"responses":{"204":{"description":"ok"}}},"parameters":[]},
  "a":{"$ref":"#/b"},
  "b":{"$ref":"#/a"},
  "paths":{
    "/ok":{"$ref":"#/base","post":{"operationId":"ignoredCollision","responses":{"204":{"description":"ok"}}}},
    "/bad":{"$ref":"#/base","get":{"responses":{"204":{"description":"ok"}}}},
    "/bad-parameters":{"$ref":"#/base","parameters":[]},
    "/cycle":{"$ref":"#/a"},
    "/unreachable":{"$ref":"#/missing"}
  }
}`)
	engine := NewEngine(nil)
	if _, err := engine.PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
		Source: Swagger20Source{Content: document}, Ref: "#/paths/~1ok/get",
	}); err != nil {
		t.Fatalf("unused collision or unreachable defect destroyed target: %v", err)
	}
	for _, selector := range []string{"#/paths/~1bad/get", "#/paths/~1bad-parameters/get", "#/paths/~1cycle/get"} {
		if _, err := engine.PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
			Source: Swagger20Source{Content: document}, Ref: selector,
		}); err == nil {
			t.Errorf("%s: expected confined refusal", selector)
		}
	}
}

func TestPrepareSwagger20EmbeddedContentWithoutLocationIsSelfContained(t *testing.T) {
	document := swagger20Document(`{"/pets":{"$ref":"https://parts.example/path.json"}}`)
	client := &http.Client{Transport: swagger20RoundTripper(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("self-contained source dispatched artifact retrieval: %s", request.URL)
		return nil, nil
	})}
	_, err := NewEngine(client).PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
		Source: Swagger20Source{Content: document}, Ref: "#/paths/~1pets/get",
	})
	if err == nil {
		t.Fatal("expected self-contained reference refusal")
	}
}
