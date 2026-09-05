package openapiclient

// Upstream-invalid Swagger 2.0 Response members are confined to their smallest
// response projections. Admitted keys retain precedence and the operation
// remains represented; actual status/body consequences are runtime facts.

import (
	"context"
	"encoding/json"
	"testing"
)

func swagger20ResponseDoc(t *testing.T, code string, response any, alsoSuccess bool) []byte {
	t.Helper()
	responses := map[string]any{code: response}
	if alsoSuccess {
		responses["200"] = map[string]any{"description": "ok"}
	}
	doc := map[string]any{
		"swagger": "2.0",
		"info":    map[string]any{"title": "R2", "version": "1"},
		"host":    "api.example",
		"schemes": []any{"https"},
		"paths": map[string]any{
			"/broken": map[string]any{"get": map[string]any{"responses": responses}},
			"/intact": map[string]any{"get": map[string]any{"responses": map[string]any{"204": map[string]any{"description": "ok"}}}},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func swagger20Analyze(t *testing.T, data []byte) map[string]Swagger20SynthesisOperation {
	t.Helper()
	client, err := LoadSwagger20(context.Background(), Swagger20Source{Content: data}, ClientOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	model, err := client.SynthesisModel()
	if err != nil {
		t.Fatalf("synthesis: %v", err)
	}
	out := map[string]Swagger20SynthesisOperation{}
	for _, operation := range model.Operations {
		out[operation.Ref] = operation
	}
	return out
}

var swagger20DefectiveShapes = []struct {
	name     string
	response any
}{
	{"the member is not a Response Object at all", "ok"},
	{"`description` is present and is not a string", map[string]any{"description": 123}},
	{"`headers` is present and is not a map", map[string]any{"description": "ok", "headers": "nope"}},
	{"`schema` is present and is not a Schema Object", map[string]any{"description": "ok", "schema": "nope"}},
	{"`examples` is present and is not a map", map[string]any{"description": "ok", "examples": "nope"}},
	{"a `headers` member is not a Header Object", map[string]any{"description": "ok", "headers": map[string]any{"X-Thing": "nope"}}},
	{"`description` is omitted while `schema` is declared", map[string]any{"schema": map[string]any{"type": "object"}}},
}

func TestSwagger20_UpstreamInvalidSuccessResponseObjectKeepsItsTarget(t *testing.T) {
	for _, shape := range swagger20DefectiveShapes {
		t.Run(shape.name, func(t *testing.T) {
			operations := swagger20Analyze(t, swagger20ResponseDoc(t, "200", shape.response, false))
			broken := operations["#/paths/~1broken/get"]
			if broken.Excluded {
				t.Fatalf("response defect escaped its smallest projection: %+v", broken)
			}
			intact := operations["#/paths/~1intact/get"]
			if intact.Excluded {
				t.Fatalf("the sibling operation was excluded with it: %s", intact.Reason)
			}
		})
	}
}

func TestSwagger20_UpstreamInvalidDefaultResponseObjectKeepsItsTarget(t *testing.T) {
	for _, shape := range swagger20DefectiveShapes {
		t.Run(shape.name, func(t *testing.T) {
			operations := swagger20Analyze(t, swagger20ResponseDoc(t, "default", shape.response, false))
			if broken := operations["#/paths/~1broken/get"]; broken.Excluded {
				t.Fatalf("default response defect escaped its smallest projection: %+v", broken)
			}
		})
	}
}

// F1: a defective NON-SUCCESS Response Object loses no representation and must
// not destroy a target whose success path is intact.
func TestSwagger20_UpstreamInvalidNonSuccessResponseObjectKeepsItsTarget(t *testing.T) {
	for _, shape := range swagger20DefectiveShapes {
		t.Run(shape.name, func(t *testing.T) {
			operations := swagger20Analyze(t, swagger20ResponseDoc(t, "404", shape.response, true))
			if broken := operations["#/paths/~1broken/get"]; broken.Excluded {
				t.Fatalf("a non-success declaration destroyed its target: %s", broken.Reason)
			}
		})
	}
}

// The carve-out `openbindings.openapi-2.0@1` §9.4 states in the same breath: a
// governing Response Object that omits its REQUIRED `description` while
// declaring no `schema` loses no representation and stays represented.
func TestSwagger20_DescriptionlessResponseWithoutSchemaStaysRepresented(t *testing.T) {
	operations := swagger20Analyze(t, swagger20ResponseDoc(t, "200", map[string]any{}, false))
	if broken := operations["#/paths/~1broken/get"]; broken.Excluded {
		t.Fatalf("the `{}` carve-out was excluded: %s", broken.Reason)
	}
}

// The same carve-out at the response rung: the declaration GOVERNS, and an
// empty actual body loses nothing.
func TestSwagger20_DescriptionlessResponseWithoutSchemaGoverns(t *testing.T) {
	data := swagger20ResponseDoc(t, "200", map[string]any{}, false)
	client, err := LoadSwagger20(context.Background(), Swagger20Source{Content: data}, ClientOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	operation, _, err := resolveSwagger20Operation(client.document, "#/paths/~1broken/get")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	responses, err := swagger20ResponsesFor(client.document.graph, operation)
	if err != nil {
		t.Fatalf("responses: %v", err)
	}
	governing, key, err := responses.governing(client.document.graph, 200)
	if err != nil {
		t.Fatalf("a `description`-less Response Object with no `schema` must govern: %v", err)
	}
	if key != "200" || governing == nil {
		t.Fatalf("governing lookup returned %q / %v", key, governing)
	}
}

// A `$ref`ed Response Object confines defects the same way as an inline one.
func TestSwagger20_UpstreamInvalidReferencedResponseObjectKeepsItsTarget(t *testing.T) {
	doc := map[string]any{
		"swagger": "2.0",
		"info":    map[string]any{"title": "R2", "version": "1"},
		"host":    "api.example",
		"schemes": []any{"https"},
		"responses": map[string]any{
			"Broken": map[string]any{"description": "ok", "headers": "nope"},
		},
		"paths": map[string]any{
			"/broken": map[string]any{"get": map[string]any{"responses": map[string]any{
				"200": map[string]any{"$ref": "#/responses/Broken"}}}},
			"/intact": map[string]any{"get": map[string]any{"responses": map[string]any{
				"204": map[string]any{"description": "ok"}}}},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	operations := swagger20Analyze(t, data)
	if broken := operations["#/paths/~1broken/get"]; broken.Excluded {
		t.Fatalf("a `$ref`ed response defect escaped its projection: %+v", broken)
	}
	if intact := operations["#/paths/~1intact/get"]; intact.Excluded {
		t.Fatalf("the sibling operation was excluded with it: %s", intact.Reason)
	}
}
