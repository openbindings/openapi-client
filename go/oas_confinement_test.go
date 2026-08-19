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

// The rail that makes this a confinement and not salvage. A `components.schemas`
// member that is a bare STRING is one of the shape table's `hits: []` cells
// (`C2-component-value-string`): no shipped class names the position, so
// nothing attributes it and the load keeps the loader's own error.
func TestConfinement_UnattributedDefectRefusesWithTheOriginalError(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Thing": "not a schema"}},
	  "paths": {"/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}}}
	}`
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("a defect no shipped class attributes must never be confined")
	} else if !strings.Contains(err.Error(), "failed to unmarshal") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// Block 8d-3: the registry-scoped class D15 -- a Schema Object keyword whose
// value violates the governing dialect's declared JSON type. `properties/member`
// holds an ARRAY, which kin refuses at unmarshal; the ladder now owns the
// position, so the pass neutralises it and the operation whose closure REACHES
// it carries the invalid verdict while the one that does not reach it survives.
func TestConfinement_D15SchemaKeywordConfinesAndReachIsAttributed(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Thing": {"type": "object", "properties": {"member": []}}}},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/reaching": {"get": {"operationId": "getReaching", "responses": {"200": {"description": "ok",
	      "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Thing"}}}}}}}
	  }
	}`
	doc, floor, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	if err != nil {
		t.Fatalf("a D15 position the ladder owns must confine: %v", err)
	}
	if doc == nil || doc.Paths == nil || doc.Paths.Value("/good") == nil {
		t.Fatalf("the non-reaching sibling must survive the confined load")
	}
	if verdict := floor.opVerdict("#/paths/~1good/get"); verdict == nil || verdict.Disposition != "represented" {
		t.Fatalf("the non-reaching sibling must stay represented, got %+v", verdict)
	}
	verdict := floor.opVerdict("#/paths/~1reaching/get")
	if verdict == nil || verdict.Disposition != "invalid" {
		t.Fatalf("the reaching operation must carry an invalid ladder verdict, got %+v", verdict)
	}
	found := false
	for _, d := range verdict.Defects {
		if d.Class == floorD15 && d.Position == "#/components/schemas/Thing/properties/member" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the climbing defect must name the D15 position, got %+v", verdict.Defects)
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

// Seam C, schema position, the 3.0 gate's OTHER half. The target here is a
// description-less RESPONSE Object -- it carries `content`. It is a D6 hit
// (D6's test is only the absence of a string `description`), but it is not
// Schema-shaped, so the 3.0 line must NOT inline it: a Response Object body
// standing where an operation's output SCHEMA belongs, on a line whose dialect
// has no `content` keyword, is not something D6 licenses. The load keeps the
// loader's original error.
//
// This is the case the gate admitted before record 84's F2 narrowing; it
// reddens if `isFloorSchemaShaped` is widened back to "is an object".
func TestConfinement_SeamCSchemaPositionRefusesAResponseShapedTarget(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"responses": {"ThingResponse": {"content": {"application/json": {"schema": {"type": "object"}}}}}},
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
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("a Response-shaped target must not be inlined into a schema position on the 3.0 line")
	} else if !strings.Contains(err.Error(), "expecting ref to schema object") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// ---- block 8g: the URef round ---------------------------------------------
//
// Mechanism (c). A reference RESOLUTION failure reaches neither earlier
// mechanism: kin accepts `{"$ref": "#/x"}` at the unmarshal oracle and fails
// later while resolving it, with a report that matches no seam-C pattern. The
// ladder already classifies the position (URef at the referencing site), so
// the round neutralises exactly the sites whose verdict CLIMBS.
//
// These five cases bite in both directions:
//
//   - UNDER-fire: delete the (c) block and the first and fourth cases go red,
//     because the loader refuses the whole artifact again.
//   - OVER-fire: populate ClimbingURefSites from the PROJECTING sink as well
//     and the second case goes red; populate it from the whole raw tree rather
//     than the ladder's own closure walk and the third goes red; remove the
//     post-loop subtraction of positions a SURVIVING unit projects and the
//     fifth goes red. Every one of those turns a confinement into salvage.
//
// The fifth case is the one the first four cannot reach. Each of them perturbs
// a position with exactly ONE role, so all four are satisfied by a set that
// ignores the per-unit split that this set's per-position keying creates.

// The elmasy shape: a success response's only media alternative carries a
// schema `$ref` that identifies no location. The defect climbs, so the
// operation is an invalid target and the intact sibling survives.
func TestConfinement_URefClimbingSchemaPositionConfines(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/reaching": {"get": {"operationId": "getReaching", "responses": {"200": {"description": "ok",
	      "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Missing"}}}}}}}
	  }
	}`
	doc, floor, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	if err != nil {
		t.Fatalf("a climbing URef position must confine: %v", err)
	}
	if doc == nil || doc.Paths == nil || doc.Paths.Value("/good") == nil {
		t.Fatalf("the intact sibling path item must survive the confined load")
	}
	verdict := floor.opVerdict("#/paths/~1reaching/get")
	if verdict == nil || verdict.Disposition != "invalid" {
		t.Fatalf("the reaching operation must carry an invalid ladder verdict, got %+v", verdict)
	}
	good := floor.opVerdict("#/paths/~1good/get")
	if good == nil || good.Disposition != "represented" {
		t.Fatalf("the intact sibling must stay represented, got %+v", good)
	}
}

// The ensi-platform shape: a dangling reference AT a success response member.
// The ladder reads that as an invalid declaration that loses no representation
// -- it PROJECTS, and the operation survives -- so neutralising it would put an
// authored value into shipped content. The pass must decline and the loader's
// own error must stand.
func TestConfinement_URefProjectingPositionIsNeverConfined(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/proj": {"get": {"operationId": "getProj", "responses": {"200": {"$ref": "#/components/responses/Missing"}}}}
	  }
	}`
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("a URef position whose unit SURVIVES must never be confined")
	} else if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// A dangling reference inside a component no unit's closure walk reaches. The
// ladder classifies nothing there, so there is no attribution to confine
// under, and the pass must decline. This is the algorand shape.
func TestConfinement_URefUnreachedByAnyUnitIsNeverConfined(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Orphan": {"type": "object", "properties": {"p": {"$ref": "#/components/schemas/Missing"}}}}},
	  "paths": {"/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}}}
	}`
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("a URef position no unit reaches must never be confined")
	} else if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// The spiceai shape: the dangling `$ref` carries a sibling. Seam C's
// bare-Reference-Object restriction does not carry over -- the target does not
// exist, so there is no composition to discard -- and the sibling is left where
// it is rather than the position being rewritten.
func TestConfinement_URefWithSiblingsConfinesAndKeepsTheSiblings(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/sib": {"get": {"operationId": "getSib",
	      "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Missing", "description": "kept"}}],
	      "responses": {"200": {"description": "ok"}}}}
	  }
	}`
	doc, floor, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	if err != nil {
		t.Fatalf("a climbing URef position carrying siblings must confine: %v", err)
	}
	if doc == nil || doc.Paths == nil || doc.Paths.Value("/good") == nil {
		t.Fatalf("the intact sibling path item must survive the confined load")
	}
	verdict := floor.opVerdict("#/paths/~1sib/get")
	if verdict == nil || verdict.Disposition != "invalid" {
		t.Fatalf("the operation carrying the dangling reference must be invalid, got %+v", verdict)
	}
}

// A position with TWO ROLES. `ClimbingURefSites` is keyed by POSITION while the
// ladder's verdict is per UNIT, so one position inside a shared component can
// climb for one unit and PROJECT on another unit that survives and is emitted.
//
// The three single-role cases above are structurally blind to this: each
// perturbs a position that has exactly one role, so each can be satisfied by a
// set that ignores the per-unit split entirely. Without the subtraction that
// removes every position a surviving unit names, the confined load succeeds
// here and `/survive` keeps a response schema whose `$ref` member the pass
// deleted -- an authored value inside a unit that survives and is emitted.
//
// The nocodb shape: the corpus reported this class as an unpredicted refusal
// text move, and record 95 read the signal as a prediction miss.
func TestConfinement_URefDualRolePositionIsNeverConfined(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {
	    "Shared": {"type": "object", "properties": {"x": {"$ref": "#/components/schemas/Missing"}}}
	  }},
	  "paths": {
	    "/climb": {"get": {"operationId": "getClimb",
	      "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Shared"}}],
	      "responses": {"200": {"description": "ok"}}}},
	    "/survive": {"get": {"operationId": "getSurvive",
	      "responses": {"200": {"description": "ok", "content": {
	        "application/json": {"schema": {"$ref": "#/components/schemas/Shared"}},
	        "text/plain": {"schema": {"type": "string"}}
	      }}}}}
	  }
	}`
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("a URef position a SURVIVING unit still emits must never be confined")
	} else if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}
