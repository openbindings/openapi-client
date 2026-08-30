package openapiclient

// Round R2: the upstream-invalid governing Response Object rule on the
// Swagger 2.0 lane, in that edition's own spelling.
//
// `openbindings.openapi-2.0@1` §9.4 states the rule and its one carve-out. The
// lane implemented neither before this round: every defective shape dispatched
// and failed at response time, and the carve-out -- a Response Object that
// omits its REQUIRED `description` while declaring no `schema` -- failed too,
// because `resolveSwagger20Response` demanded a string `description` before any
// body was consulted.
//
// The success scope is Round R2's F1 ruling: the rule reaches the Response
// Object GOVERNING a successful (2xx final status) response, which on this
// edition means an exact 2xx key plus `default` -- 2.0 has no range keys, so
// `default` can always govern some 2xx status. A defective NON-SUCCESS
// declaration loses no representation (its failure body is opaque application
// data) and must not destroy a target whose success path is intact.

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

// A defective governing SUCCESS Response Object excludes its own target and
// leaves every sibling operation addressable.
func TestSwagger20_UpstreamInvalidSuccessResponseObjectExcludesItsTarget(t *testing.T) {
	for _, shape := range swagger20DefectiveShapes {
		t.Run(shape.name, func(t *testing.T) {
			operations := swagger20Analyze(t, swagger20ResponseDoc(t, "200", shape.response, false))
			broken := operations["#/paths/~1broken/get"]
			if !broken.Excluded {
				t.Fatalf("selected target was not excluded: %+v", broken)
			}
			intact := operations["#/paths/~1intact/get"]
			if intact.Excluded {
				t.Fatalf("the sibling operation was excluded with it: %s", intact.Reason)
			}
		})
	}
}

// `default` can govern a 2xx status on this edition -- there are no range keys
// -- so a defective `default` Response Object is a governing success
// declaration and excludes.
func TestSwagger20_UpstreamInvalidDefaultResponseObjectExcludesItsTarget(t *testing.T) {
	for _, shape := range swagger20DefectiveShapes {
		t.Run(shape.name, func(t *testing.T) {
			operations := swagger20Analyze(t, swagger20ResponseDoc(t, "default", shape.response, false))
			if broken := operations["#/paths/~1broken/get"]; !broken.Excluded {
				t.Fatalf("selected target was not excluded: %+v", broken)
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

// A `$ref`ed Response Object governs its referencing target exactly as an
// inline one does, so the rule is read at the position the defect occupies.
func TestSwagger20_UpstreamInvalidReferencedResponseObjectExcludesItsTarget(t *testing.T) {
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
	if broken := operations["#/paths/~1broken/get"]; !broken.Excluded {
		t.Fatalf("a `$ref`ed defective Response Object did not exclude its target: %+v", broken)
	}
	if intact := operations["#/paths/~1intact/get"]; intact.Excluded {
		t.Fatalf("the sibling operation was excluded with it: %s", intact.Reason)
	}
}
