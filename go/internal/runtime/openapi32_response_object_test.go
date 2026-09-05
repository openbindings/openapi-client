package openapiclient

// Upstream-invalid Response members are confined to their smallest response
// projections. The admitted key keeps lookup precedence and the operation
// remains addressable; actual success/failure consequences are runtime facts.

import (
	"context"
	"encoding/json"
	"testing"
)

var openAPI32DefectiveResponseShapes = []struct {
	name     string
	response any
}{
	{"the member is not a Response Object at all", "ok"},
	{"`description` is present and is not a string", map[string]any{"description": 123}},
	{"`headers` is present and is not a map", map[string]any{"description": "ok", "headers": "nope"}},
	{"`content` is present and is not a map", map[string]any{"description": "ok", "content": "application/json"}},
	{"`links` is present and is not a map", map[string]any{"description": "ok", "links": "nope"}},
	{"a `headers` member is not a Header Object", map[string]any{"description": "ok", "headers": map[string]any{"X-Thing": "nope"}}},
}

func openAPI32ResponseDoc(t *testing.T, code string, response any, alsoSuccess bool) []byte {
	t.Helper()
	responses := map[string]any{code: response}
	if alsoSuccess {
		responses["200"] = map[string]any{"description": "ok"}
	}
	doc := map[string]any{
		"openapi": "3.2.0",
		"info":    map[string]any{"title": "R2", "version": "1"},
		"servers": []any{map[string]any{"url": "https://api.example"}},
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

func openAPI32Resolve(t *testing.T, data []byte, selector string) error {
	t.Helper()
	artifact, err := LoadArtifact(context.Background(), Source{Content: data}, ArtifactLoadOptions{})
	if err != nil {
		return err
	}
	_, resolveErr := artifact.ResolveOperation(selector)
	return resolveErr
}

func TestOpenAPI32_UpstreamInvalidSuccessResponseObjectKeepsItsTarget(t *testing.T) {
	for _, shape := range openAPI32DefectiveResponseShapes {
		t.Run(shape.name, func(t *testing.T) {
			data := openAPI32ResponseDoc(t, "200", shape.response, false)
			if err := openAPI32Resolve(t, data, "#/paths/~1broken/get"); err != nil {
				t.Fatalf("the response defect escaped its projection: %v", err)
			}
			if err := openAPI32Resolve(t, data, "#/paths/~1intact/get"); err != nil {
				t.Fatalf("the sibling operation was excluded with it: %v", err)
			}
		})
	}
}

// A defective non-success Response follows the same confinement rule.
func TestOpenAPI32_UpstreamInvalidNonSuccessResponseObjectKeepsItsTarget(t *testing.T) {
	for _, shape := range openAPI32DefectiveResponseShapes {
		t.Run(shape.name, func(t *testing.T) {
			data := openAPI32ResponseDoc(t, "404", shape.response, true)
			if err := openAPI32Resolve(t, data, "#/paths/~1broken/get"); err != nil {
				t.Fatalf("a non-success declaration destroyed its target: %v", err)
			}
			if err := openAPI32Resolve(t, data, "#/paths/~1intact/get"); err != nil {
				t.Fatalf("the sibling operation was excluded with it: %v", err)
			}
		})
	}
}

// An admitted `default` key remains addressable whether or not a `2XX` sibling
// masks its successful-status reach.
func TestOpenAPI32_DefaultResponseObjectDefectStaysConfined(t *testing.T) {
	defect := map[string]any{"description": "ok", "headers": "nope"}

	withoutRange := openAPI32ResponseDoc(t, "default", defect, false)
	if err := openAPI32Resolve(t, withoutRange, "#/paths/~1broken/get"); err != nil {
		t.Fatalf("an unmasked default response defect escaped its projection: %v", err)
	}

	doc := map[string]any{
		"openapi": "3.2.0",
		"info":    map[string]any{"title": "R2", "version": "1"},
		"servers": []any{map[string]any{"url": "https://api.example"}},
		"paths": map[string]any{
			"/broken": map[string]any{"get": map[string]any{"responses": map[string]any{
				"2XX":     map[string]any{"description": "ok"},
				"default": defect,
			}}},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := openAPI32Resolve(t, data, "#/paths/~1broken/get"); err != nil {
		t.Fatalf("a `2XX` range key covers the success class, so `default` governs no success: %v", err)
	}
}

// OAS 3.2.0 dropped the REQUIRED marker from the Response Object's
// `description`, so an omission is conformant here with or without declared
// content. This is the authority difference Round R's shape-8 ruling
// established and it must survive the rule added beside it.
func TestOpenAPI32_OmittedDescriptionIsNotADefect(t *testing.T) {
	for _, shape := range []struct {
		name     string
		response any
	}{
		{"with no declared content", map[string]any{}},
		{"with declared content", map[string]any{"content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}},
		{"with only a summary", map[string]any{"summary": "ok"}},
	} {
		t.Run(shape.name, func(t *testing.T) {
			data := openAPI32ResponseDoc(t, "200", shape.response, false)
			if err := openAPI32Resolve(t, data, "#/paths/~1broken/get"); err != nil {
				t.Fatalf("an omitted `description` is conformant on the 3.2 line: %v", err)
			}
		})
	}
}
