package openapiclient

// The §9.1 JSON-body trigger-scoping case table, shared byte-identically
// with the TypeScript engine and both adapter engines
// (testdata/json-body-trigger-scoping-cases.json) and exercised through the
// shipped engine invocation path.
//
// Two rules ride the same cells. The trigger keywords are read under the
// GOVERNING EDITION'S dialect: on the 3.0 line patternProperties, if, then,
// else, dependentSchemas and unevaluatedProperties are not in the Schema
// Object dialect at all and decide as if absent, while oneOf, anyOf, not and
// additionalProperties decide alike on both lines. And an explicit
// unevaluatedProperties triggers on ANY value, the `false` spelling
// included.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type jsonBodyTriggerCase struct {
	Name     string         `json:"name"`
	OpenAPI  string         `json:"openapi"`
	Line     string         `json:"line"`
	Keyword  string         `json:"keyword"`
	Presence string         `json:"presence"`
	Schema   map[string]any `json:"schema"`
	Expect   string         `json:"expect"`
}

type jsonBodyTriggerTable struct {
	Cases []jsonBodyTriggerCase `json:"cases"`
}

// The refusal each lane names when the cell's carriage is the other one.
const (
	flatLaneWholeRefusal    = "whole-value carriage"
	routedLaneFlatRefusal   = "routed whole-body field does not identify any admissible whole-value request-body candidate"
	triggerScopingBodyValue = `{"value":"v"}`
)

func jsonBodyTriggerDocument(t *testing.T, fixture jsonBodyTriggerCase, serverURL string) []byte {
	t.Helper()
	document := map[string]any{
		"openapi": fixture.OpenAPI,
		"info":    map[string]any{"title": "json body trigger scoping", "version": "1"},
		"servers": []any{map[string]any{"url": serverURL}},
		"paths": map[string]any{
			"/items": map[string]any{
				"post": map[string]any{
					"operationId": "createItem",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{
							"application/json": map[string]any{"schema": fixture.Schema},
						},
					},
					"responses": map[string]any{
						"204": map[string]any{"description": "stored"},
					},
				},
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestSharedJSONBodyTriggerScopingConformance(t *testing.T) {
	data, err := os.ReadFile("testdata/json-body-trigger-scoping-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var table jsonBodyTriggerTable
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	profile := FullProfile()
	routedInput := []any{map[string]any{
		profile.InputRouteKey: profile.InputRouteMarker,
		"value":               map[string]any{"payload": map[string]any{"value": "v"}},
		"parameters":          []any{},
		"body":                map[string]any{"whole": "payload"},
	}}
	flatInput := map[string]any{"value": "v"}

	for _, fixture := range table.Cases {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			whole := fixture.Expect == "whole"
			switch fixture.Expect {
			case "whole", "flattened":
			default:
				t.Fatalf("unknown expect %q", fixture.Expect)
			}
			// The flat lane carries the artifact's own named property; it
			// dispatches exactly when the cell stays flattened.
			runJSONBodyTriggerLane(t, fixture, flatInput, !whole, flatLaneWholeRefusal)
			// The routed lane names body.whole; it dispatches exactly when
			// the cell selects whole carriage.
			runJSONBodyTriggerLane(t, fixture, routedInput, whole, routedLaneFlatRefusal)
		})
	}
}

func runJSONBodyTriggerLane(t *testing.T, fixture jsonBodyTriggerCase, input any, dispatches bool, refusal string) {
	t.Helper()
	var dispatched atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dispatched.Add(1)
		body, _ := io.ReadAll(request.Body)
		var got, want any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("request body %q: %v", body, err)
		}
		if err := json.Unmarshal([]byte(triggerScopingBodyValue), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("body = %s, want %s", body, triggerScopingBodyValue)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	engine := NewEngine(nil)
	prepared, err := engine.Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: jsonBodyTriggerDocument(t, fixture, server.URL)},
		Ref:     "#/paths/~1items/post",
		Profile: FullProfile(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	execution, err := prepared.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := execution.Send(context.Background(), input); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := execution.FinishInput(); err != nil {
		t.Fatalf("finish input: %v", err)
	}
	for range execution.Events() {
	}
	terminal := execution.Wait()

	if dispatches {
		if terminal != nil {
			t.Fatalf("terminal error = %v, want a completed dispatch", terminal)
		}
		if dispatched.Load() != 1 {
			t.Fatalf("dispatch count = %d, want 1", dispatched.Load())
		}
		return
	}
	var executionErr *ExecutionError
	if !errors.As(terminal, &executionErr) {
		t.Fatalf("terminal error = %#v, want a pre-dispatch refusal naming %q", terminal, refusal)
	}
	if !strings.Contains(executionErr.Message, refusal) {
		t.Errorf("message = %q, want it to contain %q", executionErr.Message, refusal)
	}
	if dispatched.Load() != 0 {
		t.Errorf("refusal must precede dispatch: %d requests hit the server", dispatched.Load())
	}
}
