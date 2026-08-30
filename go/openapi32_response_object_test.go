package openapiclient

// Round R2: the upstream-invalid governing Response Object rule on the OpenAPI
// 3.2 lane, and its F1 success scope.
//
// Before this round the 3.2 lane reached the ruled outcome for a SUCCESS
// declaration by accident -- kin-openapi refused the value and the per-target
// fallback isolated the target -- and reached the WRONG outcome for a
// non-success one, excluding a target whose success path was intact while
// 3.0/3.1 kept it. See `openAPI32ResponseObjectDefect`.

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

// A defective governing SUCCESS Response Object excludes its own target and
// leaves every sibling operation addressable.
func TestOpenAPI32_UpstreamInvalidSuccessResponseObjectExcludesItsTarget(t *testing.T) {
	for _, shape := range openAPI32DefectiveResponseShapes {
		t.Run(shape.name, func(t *testing.T) {
			data := openAPI32ResponseDoc(t, "200", shape.response, false)
			if err := openAPI32Resolve(t, data, "#/paths/~1broken/get"); err == nil {
				t.Fatal("the selected target was not excluded")
			}
			if err := openAPI32Resolve(t, data, "#/paths/~1intact/get"); err != nil {
				t.Fatalf("the sibling operation was excluded with it: %v", err)
			}
		})
	}
}

// F1: a defective NON-SUCCESS Response Object loses no representation -- its
// failure body is opaque application-authored data -- and must not destroy a
// target whose success path is intact. 3.0/3.1 already keep it.
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

// `default` governs a 2xx status only when no `2XX` range key covers the class;
// where one does, `default` can never govern a success and its defect must not
// exclude. Both halves, because the pair is the whole of the range rule.
func TestOpenAPI32_DefaultResponseObjectIsSuccessScopedByTheRangeKey(t *testing.T) {
	defect := map[string]any{"description": "ok", "headers": "nope"}

	withoutRange := openAPI32ResponseDoc(t, "default", defect, false)
	if err := openAPI32Resolve(t, withoutRange, "#/paths/~1broken/get"); err == nil {
		t.Fatal("a defective `default` with no `2XX` range key must exclude: it can govern a 2xx")
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
