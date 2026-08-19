package openapiclient

// Load-path confinement (block 8d-2) at the client engine's own load seam.
// Written to bite in BOTH directions, and without any dependence on the
// shared 66-cell case table, which block 8d-1 proved cannot redden under an
// over-firing mutation (record 80, FIX 7):
//
//   - UNDER-fire: disable the pass and the cases that require it to fire go
//     red, because the loader refuses the whole artifact again.
//   - OVER-fire: remove the ladder-attribution rail and
//     TestConfinement_UnattributedDefectRefusesWithTheOriginalError goes red,
//     because an unrostered defect would then be silently neutralised.
//
// BLOCK 8h CHANGED WHAT THIS ENGINE CONFINES, and the change is a widening of
// a cost this engine already carried rather than a new kind of it. Every
// mechanism that AUTHORS now registers with one `confinementLedger` and every
// exit goes through one `confinementAdmit`, which admits an authored value only
// on an emission gate's word. This engine derives no interface from a document
// -- it prepares one operation at a time and builds a request at execution --
// so it passes a nil gate and DECLINES every confinement that authors, not only
// the URef round it already declined.
//
// The remaining confinement here is the one that authors nothing: seam C's
// schema-position inline, where the value that lands is the artifact's own at
// the place its own pointer denotes. The measured cost is eight corpus
// specimens this engine loaded at `76174c4` and no longer loads, five of which
// `openbindings-go` still synthesizes -- so the SDK yields an OBI this client
// cannot load and therefore cannot invoke. That is record 100's filing V3, one
// measurement wider, and it is recorded at
// `corpus-lab/openapi-runtime/103-block-8h-*` §8.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func loadConfined(document string) (*openapi3.T, error) {
	doc, _, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	return doc, err
}

// The Kong shape: an HTTP-method member that is an empty ARRAY (D2b, P3).
// Mechanism (a) authors here, this engine cannot gate it, and it declines.
func TestConfinement_MethodMemberArrayAuthorsAndThisEngineDeclines(t *testing.T) {
	document := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/bad": {"get": []}
	  }
	}`
	_, _, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	if err == nil {
		t.Fatalf("an engine with no emission must not admit an authored value")
	}
	if !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("the loader's ORIGINAL error must stand, got %q", err)
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
// holds an ARRAY, which kin refuses at unmarshal.
//
// Block 8h: neutralising that position AUTHORS, and this engine has no emission
// to certify it against. In `openbindings-go` the position has a markable
// anchor (`#/components/schemas/Thing`) and the gate decides it on the
// emitter's word; here there is no gate to ask, so the load keeps the loader's
// original error. This is the widened V3 asymmetry stated as a test rather than
// as prose: the SAME document synthesizes in the SDK and refuses here.
func TestConfinement_D15SchemaKeywordAuthorsAndThisEngineDeclines(t *testing.T) {
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
	_, _, err := loadDocument(context.Background(), nil, Source{Content: []byte(document)}, false)
	if err == nil {
		t.Fatalf("an engine with no emission must not admit an authored value")
	}
	if !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("the loader's ORIGINAL error must stand, got %q", err)
	}
	// The ladder's own verdicts are unaffected by the pass declining: D15 is
	// still owned, still climbs, and is still readable from the raw image.
	floor := computeAcceptanceFloorFromBytes([]byte(document))
	if verdict := floor.opVerdict("#/paths/~1good/get"); verdict == nil || verdict.Disposition != "represented" {
		t.Fatalf("the non-reaching sibling must stay represented in the ladder, got %+v", verdict)
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

// Seam C, response position (the tsuru shape): the D7 member is removed, which
// AUTHORS, and this engine cannot certify it -- so the load keeps its original
// error. `openbindings-go` declines this shape too, for a different reason: a
// Responses Object has no markable anchor there either.
func TestConfinement_SeamCResponsePositionAuthorsAndThisEngineDeclines(t *testing.T) {
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
	if _, err := loadConfined(document); err == nil {
		t.Fatalf("removing a D7 response member is authoring and must not be admitted here")
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

// The accounting obligation, exercised directly because a mechanism that
// authors without a ledger cannot be shipped in order to test it. This is the
// property that replaced a claimed compile obligation the language does not
// supply: `confinementUnledgeredDifference` compares the tree about to be handed
// back against a second parse of the artifact's own bytes.
//
// It goes red if the walk stops descending, if prefix coverage is widened to
// "anything under the root", or if the check is skipped when the ledger is
// empty -- which is exactly the state a forgetful mechanism leaves behind.
func TestConfinement_UnledgeredAuthoringIsNotAccounted(t *testing.T) {
	entry := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},"paths":{"/x":{"get":{"operationId":"getX","responses":{"200":{"description":"ok"}}}}}}`)
	parse := func() map[string]any {
		tree, ok := parseRawResource(entry)
		if !ok {
			t.Fatal("entry must parse")
		}
		root, ok := tree.(map[string]any)
		if !ok {
			t.Fatal("entry must be an object")
		}
		return root
	}

	// Unchanged: nothing to account for, with an empty ledger.
	if where, unaccounted := confinementUnledgeredDifference(entry, parse(), newConfinementLedger()); unaccounted {
		t.Errorf("an unchanged tree has nothing unaccounted, got %q", where)
	}

	// A fifth mechanism's change, with no ledger entry anywhere.
	fifth := parse()
	fifth["info"].(map[string]any)["title"] = "AUTHORED BY A MECHANISM WITH NO LEDGER"
	where, unaccounted := confinementUnledgeredDifference(entry, fifth, newConfinementLedger())
	if !unaccounted {
		t.Fatalf("an unledgered change must not be accounted")
	}
	if where != "#/info/title" {
		t.Errorf("the decline must name the position, got %q", where)
	}

	// The same change, recorded. An entry accounts for the position and for
	// everything beneath it, and for nothing else.
	ledger := newConfinementLedger()
	ledger.author("#/info/title")
	if where, unaccounted := confinementUnledgeredDifference(entry, fifth, ledger); unaccounted {
		t.Errorf("a recorded position is accounted, got %q", where)
	}
	elsewhere := newConfinementLedger()
	elsewhere.author("#/info/version")
	if _, unaccounted := confinementUnledgeredDifference(entry, fifth, elsewhere); !unaccounted {
		t.Errorf("an entry at another position accounts for nothing here")
	}

	// A removed member is a difference AT the removed position.
	removed := parse()
	delete(removed["paths"].(map[string]any)["/x"].(map[string]any), "get")
	if where, _ := confinementUnledgeredDifference(entry, removed, newConfinementLedger()); where != "#/paths/~1x/get" {
		t.Errorf("a removal is a difference at the removed position, got %q", where)
	}
}

// RED PROOF FOR THE ACCOUNTING CHECK'S CALL SITE, and the reason it exists is a
// verification finding rather than a design one.
//
// The case above proves that `confinementUnledgeredDifference` COMPUTES
// differences. It calls the helper directly, four times, with hand-built trees,
// and it cannot notice if the pass stops calling it: with
// `confinementAdmit`'s call replaced by `if false`, the entire `formats/openapi`
// suite stayed GREEN (record 107 §6.2). That is exactly the failure block 8h's
// re-run reported about its own perturbation A -- "an unfaithful perturbation
// that passes is worth nothing" -- in the perturbation it did not re-check.
//
// This case exercises `confinementAdmit`, which is the pass's ONE admission
// point and the function the obligation is stated over. It is two-sided on
// purpose:
//
//   - an unledgered difference must DECLINE. Red if the call site is removed,
//     which is the regression the case above cannot see.
//   - the SAME difference, recorded, must ADMIT. Red if the check is widened to
//     decline whatever it is handed, which would make the first half pass for
//     the wrong reason.
//
// What it cannot do is drive the difference in through `confineEntryDocument`,
// because a mechanism that authors without a ledger is by definition one this
// engine does not ship. This is the tightest faithful proof available without
// shipping the defect in order to test it, and it is red under the exact
// perturbation that motivated it.
func TestConfinement_AdmissionCallsTheAccountingCheck(t *testing.T) {
	entry := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},"paths":{"/x":{"get":{"operationId":"getX","responses":{"200":{"description":"ok"}}}}}}`)
	authored := func() map[string]any {
		tree, ok := parseRawResource(entry)
		if !ok {
			t.Fatal("entry must parse")
		}
		root, ok := tree.(map[string]any)
		if !ok {
			t.Fatal("entry must be an object")
		}
		// A fifth mechanism's change, reaching into the raw tree the way Go will
		// always let one reach into it.
		root["info"].(map[string]any)["title"] = "AUTHORED BY A MECHANISM WITH NO LEDGER"
		return root
	}
	reload := func(data []byte) (*openapi3.T, error) {
		loader := openapi3.NewLoader()
		return loader.LoadFromData(data)
	}
	admittingGate := func(shipped, marked []byte, floor *acceptanceFloor) (bool, bool) { return true, true }

	tree := authored()
	floor := computeAcceptanceFloor(tree)
	if floor == nil {
		t.Fatal("the edition gate must read this artifact")
	}
	shipped, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	loaded, err := reload(shipped)
	if err != nil {
		t.Fatalf("the authored tree must load: %v", err)
	}

	if _, _, ok := confinementAdmit(entry, tree, newConfinementLedger(), floor, reload, admittingGate, loaded); ok {
		t.Fatalf("admission must consult the accounting check: an unledgered change was ADMITTED")
	}

	ledger := newConfinementLedger()
	ledger.author("#/info/title")
	doc, admitErr, ok := confinementAdmit(entry, tree, ledger, floor, reload, admittingGate, loaded)
	if !ok || admitErr != nil || doc == nil {
		t.Fatalf("a recorded position an admitting gate clears must be admitted, got ok=%v err=%v", ok, admitErr)
	}
}

// RED PROOF FOR THE DENOTATION EXEMPTION, at the half a signature cannot carry.
//
// `confinementInlineDenoted` takes a POINTER rather than a value, so no caller
// can MINT through it. That was claimed to make the exemption structural rather
// than declared, and it does not: nothing in the signature says `site` has
// anything to do with `target`, so any caller could RELOCATE the artifact's own
// value to a position the artifact never put it, past an admitting gate,
// accounted as denoted. A verifier shipped `info.title = "getSurvive"` that way
// in one line (record 107 §6.3).
//
// The site is now checked to BE a Reference Object whose `$ref` names `target`,
// which is what the exemption's justification always asserted. Two-sided:
// relocation must be refused and the shipped seam-C shape must still inline.
func TestConfinement_DenotationRequiresTheSiteToDenoteTheTarget(t *testing.T) {
	entry := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},
	  "components":{"schemas":{"S":{"type":"object"}}},
	  "paths":{"/survive":{"get":{"operationId":"getSurvive",
	    "responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/S"}}}}}}}}}`)
	parse := func() map[string]any {
		tree, ok := parseRawResource(entry)
		if !ok {
			t.Fatal("entry must parse")
		}
		root, ok := tree.(map[string]any)
		if !ok {
			t.Fatal("entry must be an object")
		}
		return root
	}

	// RELOCATION: the artifact's own value, at a position that denotes nothing.
	relocate := parse()
	ledger := newConfinementLedger()
	if confinementInlineDenoted(relocate, "#/info/title", "#/paths/~1survive/get/operationId", ledger) {
		t.Fatalf("a site that does not denote the target must never be inlined")
	}
	if title := relocate["info"].(map[string]any)["title"]; title != "T" {
		t.Fatalf("nothing may move at a site that denotes nothing, got %v", title)
	}
	if len(ledger.denoted) != 0 {
		t.Fatalf("a refused inline records nothing, got %v", ledger.denoted)
	}

	// The shipped shape: a bare Reference Object at the position, whose own
	// `$ref` names the target.
	inline := parse()
	site := "#/paths/~1survive/get/responses/200/content/application~1json/schema"
	if !confinementInlineDenoted(inline, site, "#/components/schemas/S", ledger) {
		t.Fatalf("a Reference Object must still be inlined with the value its own pointer names")
	}
	landed, ok := confinementResolveRaw(inline, site)
	if !ok {
		t.Fatalf("the site must still hold a value")
	}
	if landedMap, isMap := landed.(map[string]any); !isMap || landedMap["type"] != "object" {
		t.Fatalf("the target's own value must land, got %v", landed)
	}
	if !ledger.denoted[site] {
		t.Fatalf("an inlined position is recorded as denoted, got %v", ledger.denoted)
	}
	// Both spellings of the target name it.
	fragment := parse()
	if !confinementInlineDenoted(fragment, site, "/components/schemas/S", newConfinementLedger()) {
		t.Fatalf("the fragment spelling must name the same target")
	}
}
