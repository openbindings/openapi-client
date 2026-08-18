package openapiclient

// The acceptance floor at the client engine's seams (block 8d-1): §3 part-2
// whole-source refusal at load, and the prepare-time inventory filter that
// refuses a ladder-invalid target before dispatch.

import (
	"context"
	"strings"
	"testing"
)

const floorInvalidTargetDocument = `{
  "openapi": "3.0.3",
  "info": {"title": "T", "version": "1"},
  "paths": {
    "/good": {
      "get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}
    },
    "/bad": {
      "get": {
        "operationId": "getBad",
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"type": "int"}}}}}
      }
    }
  }
}`

func TestAcceptanceFloor_LoadRefusesZeroSurvivors(t *testing.T) {
	document := `{
	  "openapi": "3.0.1",
	  "info": {"title": "T", "version": "1"}
	}`
	_, _, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	if err == nil {
		t.Fatal("expected the §3 part-2 whole-source refusal for a 3.0 document with no paths")
	}
	if !strings.Contains(err.Error(), "whole-source refusal") {
		t.Errorf("refusal should cite §3 part 2, got: %v", err)
	}
}

func TestAcceptanceFloor_PrepareRefusesInvalidTarget(t *testing.T) {
	engine := NewEngine(nil)
	_, err := engine.Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: []byte(floorInvalidTargetDocument)},
		Ref:     "#/paths/~1bad/get",
		Profile: FullProfile(),
	})
	if err == nil {
		t.Fatal("expected the prepare-time refusal of a ladder-invalid target")
	}
	execution, ok := err.(*ExecutionError)
	if !ok || execution.Code != CodeRefused {
		t.Fatalf("expected ERR_REFUSED before dispatch, got %v", err)
	}
	// The sibling target prepares.
	if _, err := engine.Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: []byte(floorInvalidTargetDocument)},
		Ref:     "#/paths/~1good/get",
		Profile: FullProfile(),
	}); err != nil {
		t.Fatalf("the sibling target must prepare: %v", err)
	}
}

func TestAcceptanceFloor_EnumerationFiltersInvalidTarget(t *testing.T) {
	client, err := Load(context.Background(), Source{Content: []byte(floorInvalidTargetDocument)}, ClientOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	operations := client.Operations()
	if len(operations) != 1 {
		t.Fatalf("expected the invalid target filtered from the inventory, got %d operations", len(operations))
	}
	if operations[0].Ref != "#/paths/~1good/get" {
		t.Errorf("surviving target %q", operations[0].Ref)
	}
}
