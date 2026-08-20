package openapiclient

// The §9.2 normalized-collision CONFINEMENT case table, shared
// byte-identically with the TypeScript engine and both adapter engines
// (testdata/media-collision-confinement-cases.json) and exercised here
// through the shipped engine invocation path.
//
// Two keys in ONE content map that denote the same parsed media type are a
// normalized collision, and the defect confines to that colliding parsed
// identity -- the smallest unit that owns it. No first-key rule: no request
// selection may land on the colliding identity and no response match may be
// governed by it, while the map's non-colliding entries remain usable.
//
// Request cells drive the requestMedia configuration point and observe the
// dispatch; response cells drive the peer's Content-Type and observe both the
// decoded output and the Accept set the request advertised.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type mediaCollisionCase struct {
	Name              string         `json:"name"`
	OpenAPI           string         `json:"openapi"`
	Side              string         `json:"side"`
	Description       string         `json:"description"`
	Content           map[string]any `json:"content"`
	Select            string         `json:"select"`
	ResponseBody      string         `json:"responseBody"`
	Outcome           string         `json:"outcome"`
	Output            any            `json:"output"`
	Advertised        []string       `json:"advertised"`
	Target            string         `json:"target"`
	TargetReasonCode  string         `json:"targetReasonCode"`
	TargetRule        string         `json:"targetRule"`
	Represented       []string       `json:"represented"`
	Excluded          []string       `json:"excluded"`
	CollidingIdentity string         `json:"collidingIdentity"`
}

type mediaCollisionTable struct {
	Cases []mediaCollisionCase `json:"cases"`
}

const mediaCollisionRequestValue = `{"name":"A"}`

func loadMediaCollisionTable(t *testing.T, path string) mediaCollisionTable {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var table mediaCollisionTable
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table
}

func mediaCollisionDocument(t *testing.T, fixture mediaCollisionCase, serverURL string) []byte {
	t.Helper()
	var operation map[string]any
	if fixture.Side == "request" {
		operation = map[string]any{
			"operationId": "createItem",
			"requestBody": map[string]any{"required": true, "content": fixture.Content},
			"responses":   map[string]any{"204": map[string]any{"description": "stored"}},
		}
	} else {
		operation = map[string]any{
			"operationId": "readItem",
			"responses": map[string]any{
				"200": map[string]any{"description": "item", "content": fixture.Content},
			},
		}
	}
	method := "post"
	if fixture.Side == "response" {
		method = "get"
	}
	document := map[string]any{
		"openapi": fixture.OpenAPI,
		"info":    map[string]any{"title": "media collision confinement", "version": "1"},
		"servers": []any{map[string]any{"url": serverURL}},
		"paths":   map[string]any{"/items": map[string]any{method: operation}},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestSharedMediaCollisionConfinementConformance(t *testing.T) {
	table := loadMediaCollisionTable(t, "testdata/media-collision-confinement-cases.json")
	for _, fixture := range table.Cases {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			switch fixture.Outcome {
			case "usable", "refused":
			default:
				t.Fatalf("unknown outcome %q", fixture.Outcome)
			}
			switch fixture.Side {
			case "request":
				runMediaCollisionRequestCell(t, fixture)
			case "response":
				runMediaCollisionResponseCell(t, fixture)
			default:
				t.Fatalf("unknown side %q", fixture.Side)
			}
		})
	}
}

func runMediaCollisionRequestCell(t *testing.T, fixture mediaCollisionCase) {
	t.Helper()
	var dispatched atomic.Int64
	var contentType atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dispatched.Add(1)
		contentType.Store(request.Header.Get("Content-Type"))
		body, _ := io.ReadAll(request.Body)
		var got, want any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("request body %q: %v", body, err)
		}
		if err := json.Unmarshal([]byte(mediaCollisionRequestValue), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("body = %s, want %s", body, mediaCollisionRequestValue)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	terminal := runMediaCollisionInvocation(t, fixture, server.URL, map[string]any{
		"configuration": map[string]any{"requestMedia": fixture.Select},
	}, map[string]any{"name": "A"}, true, nil)

	if fixture.Outcome == "usable" {
		if terminal != nil {
			t.Fatalf("terminal error = %v, want a completed dispatch", terminal)
		}
		if dispatched.Load() != 1 {
			t.Fatalf("dispatch count = %d, want 1", dispatched.Load())
		}
		if got, _ := contentType.Load().(string); got != fixture.Select {
			t.Errorf("emitted Content-Type = %q, want the configured concrete value %q", got, fixture.Select)
		}
		return
	}
	if terminal == nil {
		t.Fatal("terminal error = nil, want a pre-dispatch refusal")
	}
	if dispatched.Load() != 0 {
		t.Errorf("refusal must precede dispatch: %d requests hit the server", dispatched.Load())
	}
}

func runMediaCollisionResponseCell(t *testing.T, fixture mediaCollisionCase) {
	t.Helper()
	var dispatched atomic.Int64
	var accept atomic.Value
	var acceptPresent atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dispatched.Add(1)
		values, present := request.Header["Accept"]
		acceptPresent.Store(present)
		accept.Store(strings.Join(values, ", "))
		writer.Header().Set("Content-Type", fixture.Select)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(fixture.ResponseBody))
	}))
	defer server.Close()

	var outputs []any
	terminal := runMediaCollisionInvocation(t, fixture, server.URL, nil, nil, false, func(value any) {
		outputs = append(outputs, value)
	})

	if dispatched.Load() != 1 {
		t.Fatalf("dispatch count = %d, want 1", dispatched.Load())
	}
	// The Accept set is the advertised success-media membership: a colliding
	// parsed identity can govern no match, so it is never advertised.
	wantAccept := strings.Join(fixture.Advertised, ", ")
	if len(fixture.Advertised) == 0 {
		if acceptPresent.Load() {
			t.Errorf("Accept header = %q, want the header omitted for an empty advertised set", accept.Load())
		}
	} else if got, _ := accept.Load().(string); got != wantAccept {
		t.Errorf("Accept = %q, want %q", got, wantAccept)
	}

	if fixture.Outcome == "usable" {
		if terminal != nil {
			t.Fatalf("terminal error = %v, want a decoded response", terminal)
		}
		if len(outputs) != 1 {
			t.Fatalf("outputs = %#v, want exactly one value", outputs)
		}
		if !reflect.DeepEqual(normalizeJSON(t, outputs[0]), normalizeJSON(t, fixture.Output)) {
			t.Errorf("output = %#v, want %#v", outputs[0], fixture.Output)
		}
		return
	}
	if terminal == nil {
		t.Fatal("terminal error = nil, want a loud response-media refusal")
	}
	if len(outputs) != 0 {
		t.Errorf("outputs = %#v, want none", outputs)
	}
}

func normalizeJSON(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func runMediaCollisionInvocation(
	t *testing.T,
	fixture mediaCollisionCase,
	serverURL string,
	bindCtx map[string]any,
	input any,
	sendInput bool,
	observe func(any),
) error {
	t.Helper()
	engine := NewEngine(nil)
	prepared, err := engine.Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: mediaCollisionDocument(t, fixture, serverURL)},
		Ref:     "#/paths/~1items/" + map[string]string{"request": "post", "response": "get"}[fixture.Side],
		Profile: FullProfile(),
		Context: bindCtx,
	})
	if err != nil {
		// Preflight is part of the shipped path: a knowable pre-dispatch
		// refusal may surface here rather than at Wait.
		return err
	}
	execution, err := prepared.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if sendInput {
		if err := execution.Send(context.Background(), input); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if err := execution.FinishInput(); err != nil {
		t.Fatalf("finish input: %v", err)
	}
	for event := range execution.Events() {
		if observe != nil {
			observe(event.Value)
		}
	}
	return execution.Wait()
}
