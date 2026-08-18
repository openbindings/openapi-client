package openapiclient

// Load-path confinement (block 8d-2) at the client engine's own load seam.
// Written to bite in BOTH directions, and without any dependence on the
// shared 66-cell case table, which block 8d-1 proved cannot redden under an
// over-firing mutation (record 80, FIX 7):
//
//   - UNDER-fire: disable the pass and the four cases that require it to fire
//     go red, because the loader refuses the whole artifact again.
//   - OVER-fire: remove the ladder-attribution rail and
//     TestConfinement_UnattributedDefectRefusesWithTheOriginalError goes red,
//     because an unrostered defect would then be silently neutralised.

import (
	"context"
	"github.com/getkin/kin-openapi/openapi3"
	"strings"
	"testing"
)

func loadConfined(document string) (*openapi3.T, error) {
	doc, _, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	return doc, err
}

// The Kong shape: an HTTP-method member that is an empty ARRAY (D2b, P3).
func TestConfinement_MethodMemberArrayConfinesAndSiblingSurvives(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/bad": {"get": []}
	  }
	}`
	doc, floor, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	if err != nil {
		t.Fatalf("confinement must let the intact sibling load: %v", err)
	}
	if doc == nil || doc.Paths == nil || doc.Paths.Value("/good") == nil {
		t.Fatalf("the intact sibling path item must survive the confined load")
	}
	if doc.Paths.Value("/bad") != nil && len(doc.Paths.Value("/bad").Operations()) != 0 {
		t.Fatalf("the confined target must not survive as an operation")
	}
	verdict := floor.opVerdict("#/paths/~1bad/get")
	if verdict == nil || verdict.Disposition != "invalid" {
		t.Fatalf("the confined position must carry an invalid ladder verdict, got %+v", verdict)
	}
}

// The rail that makes this a confinement and not salvage.
func TestConfinement_UnattributedDefectRefusesWithTheOriginalError(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Thing": {"type": "object", "properties": {"member": []}}}},
	  "paths": {"/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}}}
	}`
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("a defect no shipped class attributes must never be confined")
	} else if !strings.Contains(err.Error(), "Schema.properties") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// Seam C, schema position (the C4 shape): the referencing site is inlined
// with the value its pointer denotes.
func TestConfinement_SeamCSchemaPositionInlinesTheDenotedValue(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"responses": {"ThingResponse": {"type": "object", "properties": {"id": {"type": "string"}}}}},
	  "paths": {
	    "/things": {
	      "get": {
	        "operationId": "listThings",
	        "responses": {
	          "200": {
	            "description": "ok",
	            "content": {"application/json": {"schema": {"$ref": "#/components/responses/ThingResponse"}}}
	          }
	        }
	      }
	    }
	  }
	}`
	doc, err := loadConfined(document)
	if err != nil {
		t.Fatalf("seam C must close this load: %v", err)
	}
	item := doc.Paths.Value("/things")
	if item == nil || item.Get == nil {
		t.Fatalf("the operation must survive")
	}
	schema := item.Get.Responses.Value("200").Value.Content.Get("application/json").Schema
	if schema == nil || schema.Value == nil || schema.Value.Properties["id"] == nil {
		t.Fatalf("the denoted value must be inlined at the referencing site")
	}
}

// Seam C, response position (the tsuru shape): the D7 member is removed and
// the operation keeps its explicit 2xx.
func TestConfinement_SeamCResponsePositionRemovesTheMember(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Error": {"type": "object"}}},
	  "paths": {
	    "/info": {
	      "get": {
	        "operationId": "getInfo",
	        "responses": {
	          "200": {"description": "ok", "content": {"application/json": {"schema": {"type": "string"}}}},
	          "default": {"$ref": "#/components/schemas/Error"}
	        }
	      }
	    }
	  }
	}`
	doc, err := loadConfined(document)
	if err != nil {
		t.Fatalf("seam C must close this load: %v", err)
	}
	item := doc.Paths.Value("/info")
	if item == nil || item.Get == nil {
		t.Fatalf("the operation must survive")
	}
	if item.Get.Responses.Value("default") != nil {
		t.Errorf("the D7 response member must be removed by the pass")
	}
	if item.Get.Responses.Value("200") == nil {
		t.Errorf("the explicit 2xx must be untouched")
	}
}

// The whole-source case: `paths` is a Reference Object (D5), so §3 part 2
// refuses with the floor's reason rather than kin's unmarshal diagnostic.
func TestConfinement_PathsReferenceObjectRefusesWithThePart2Reason(t *testing.T) {
	document := `{
	  "openapi": "3.1.0",
	  "info": {"title": "T", "version": "1"},
	  "paths": {"$ref": "./routes.yml"}
	}`
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("a source with no addressable target must refuse")
	} else if !strings.Contains(err.Error(), "whole-source refusal (OAPI-P-01") {
		t.Errorf("want the part-2 refusal, got %q", err)
	}
}

// The fast path is untouched.
func TestConfinement_FastPathUnchanged(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {"/x": {"get": {"operationId": "getX", "responses": {"200": {"description": "ok"}}}}}
	}`
	doc, floor, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	if err != nil {
		t.Fatalf("conforming document must load: %v", err)
	}
	if doc.Paths.Value("/x") == nil {
		t.Fatalf("conforming document must keep its path item")
	}
	if verdict := floor.opVerdict("#/paths/~1x/get"); verdict == nil || verdict.Disposition != "represented" {
		t.Fatalf("conforming operation must be represented, got %+v", verdict)
	}
}
