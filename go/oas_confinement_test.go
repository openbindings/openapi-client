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
// ladder classifies the position, and the round CAN neutralise exactly the
// sites whose verdict CLIMBS -- but unlike (a) and (b) it AUTHORS a value the
// artifact never wrote, so it is admitted only through the EMISSION GATE:
// something has to demonstrate that what was authored never reaches the content
// the engine emits.
//
// THIS ENGINE HAS NO EMISSION. It derives no interface from a document; it
// prepares one operation at a time and builds a request at execution. So it
// passes a nil gate and the round always DECLINES, keeping the loader's own
// error -- the behaviour before the round existed. That is a deliberate
// asymmetry with `openbindings-go`, whose synthesis can answer the question,
// and it is the conservative side of it: a pass that cannot show its authored
// values are unreachable must not admit them. `oas_confinement.go` and
// `acceptance_floor.go` remain byte-identical between the two engines apart
// from the package clause and the one pre-existing `buildJSONPointerRef` line;
// the whole difference is the argument at each engine's call site.
//
// So every URef case here asserts the DECLINE, and they bite in both
// directions:
//
//   - OVER-fire: pass a gate that admits unconditionally, or drop the nil-gate
//     decline, and all five go red -- including the two whose documents this
//     engine could safely have confined, which is the price of the asymmetry
//     and is why it is stated rather than hidden.
//   - The five documents are kept exactly as they are in the `openbindings-go`
//     twin so that any future engine that gains an emission surface can be held
//     to the twin's outcomes without rewriting them.

// The elmasy shape: a success response's only media alternative carries a
// schema `$ref` that identifies no location. openbindings-go confines this and
// synthesizes the intact sibling; this engine cannot demonstrate that, so it
// declines.
func TestConfinement_URefClimbingSchemaPositionDeclinesWithoutAnEmissionSurface(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/reaching": {"get": {"operationId": "getReaching", "responses": {"200": {"description": "ok",
	      "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Missing"}}}}}}}
	  }
	}`
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("an engine with no emission surface must never admit an authored value")
	} else if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// The ensi-platform shape: a dangling reference AT a success response member.
// The ladder reads that as an invalid declaration that loses no representation
// -- it PROJECTS, and the operation survives -- so the site never enters
// `ClimbingURefSites` at all. This case is red under a sink that adds
// projections, independently of the gate.
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
// under, and the pass must decline. This is the algorand shape, and it is red
// under a whole-raw-tree sink independently of the gate.
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

// The spiceai shape: the dangling `$ref` carries a sibling. openbindings-go
// confines it and keeps the sibling; this engine declines for the same reason
// as the first case.
func TestConfinement_URefWithSiblingsDeclinesWithoutAnEmissionSurface(t *testing.T) {
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
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("an engine with no emission surface must never admit an authored value")
	} else if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// A position with TWO ROLES, and the four channels that reach a surviving unit
// without passing through the acceptance floor's closure walk. In
// `openbindings-go` these are the cases the emission gate exists for, and the
// twin's `TestConfinement_URefEmissionThroughAnUnwalkedChannelIsNeverConfined`
// is red under its removal. Here they are covered by the nil-gate decline, and
// they are kept so that the two engines answer the same documents.
func TestConfinement_URefEmissionReachableSitesAreNeverConfined(t *testing.T) {
	const climbing = `"/climb": {"get": {"operationId": "getClimb",
		  "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Shared"}}],
		  "responses": {"200": {"description": "ok"}}}}`
	const sharedSchemas = `"Shared": {"type": "object", "properties": {"x": {"$ref": "#/components/schemas/Missing"}}}`

	for _, tc := range []struct {
		name       string
		components string
		surviving  string
	}{
		{
			name:       "dual role, through a walked channel",
			components: `"schemas": {` + sharedSchemas + `}`,
			surviving: `"/survive": {"get": {"operationId": "getSurvive",
			  "responses": {"200": {"description": "ok", "content": {
			    "application/json": {"schema": {"$ref": "#/components/schemas/Shared"}},
			    "text/plain": {"schema": {"type": "string"}}
			  }}}}}`,
		},
		{
			name:       "parameter content form, operation level",
			components: `"schemas": {` + sharedSchemas + `}`,
			surviving: `"/survive": {"get": {"operationId": "getSurvive",
			  "parameters": [{"name": "q", "in": "query", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}],
			  "responses": {"200": {"description": "ok"}}}}`,
		},
		{
			name: "success response is a Reference Object",
			components: `"schemas": {` + sharedSchemas + `},
			  "responses": {"OK": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}}`,
			surviving: `"/survive": {"get": {"operationId": "getSurvive",
			  "responses": {"200": {"$ref": "#/components/responses/OK"}}}}`,
		},
		{
			name: "request body is a Reference Object",
			components: `"schemas": {` + sharedSchemas + `},
			  "requestBodies": {"Body": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}}`,
			surviving: `"/survive": {"post": {"operationId": "postSurvive",
			  "requestBody": {"$ref": "#/components/requestBodies/Body"},
			  "responses": {"200": {"description": "ok"}}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := `{
			  "openapi": "3.0.3",
			  "info": {"title": "T", "version": "1"},
			  "components": {` + tc.components + `},
			  "paths": {` + climbing + `,` + tc.surviving + `}
			}`
			if _, err := loadConfined(document); err == nil {
				t.Fatalf("a position a SURVIVING unit EMITS must never be confined, whatever channel carries it")
			} else if !strings.Contains(err.Error(), "Missing") {
				t.Errorf("the loader's original error must stand, got %q", err)
			}
		})
	}
}
