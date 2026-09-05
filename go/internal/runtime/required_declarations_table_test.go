package openapiclient

// The §9.1 required-declarations case table, shared byte-identically with
// the TypeScript engine (conformance/cases/required-declarations.json) and
// exercised through the shipped engine invocation path. Exactly two
// conditions refuse before dispatch — a required request body with no value
// to carry and an unsupplied path parameter — each applied to absent and
// supplied-but-incomplete input alike; every other missing declaration is
// sent as supplied, the server's validation authoritative.

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

type requiredDeclarationsCase struct {
	Name     string         `json:"name"`
	Document map[string]any `json:"document"`
	Ref      string         `json:"ref"`
	Input    struct {
		Present bool           `json:"present"`
		Value   map[string]any `json:"value"`
	} `json:"input"`
	Expect struct {
		Dispatched      bool            `json:"dispatched"`
		Code            string          `json:"code"`
		MessageContains string          `json:"messageContains"`
		Method          string          `json:"method"`
		Path            string          `json:"path"`
		Query           string          `json:"query"`
		BodyJSON        json.RawMessage `json:"bodyJSON"`
	} `json:"expect"`
}

func TestSharedRequiredDeclarationsConformance(t *testing.T) {
	data, err := os.ReadFile("../../../conformance/cases/required-declarations.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []requiredDeclarationsCase
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			var dispatched atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				dispatched.Add(1)
				if fixture.Expect.Method != "" && request.Method != fixture.Expect.Method {
					t.Errorf("method = %q, want %q", request.Method, fixture.Expect.Method)
				}
				if fixture.Expect.Path != "" && request.URL.EscapedPath() != fixture.Expect.Path {
					t.Errorf("path = %q, want %q", request.URL.EscapedPath(), fixture.Expect.Path)
				}
				if request.URL.RawQuery != fixture.Expect.Query {
					t.Errorf("query = %q, want %q", request.URL.RawQuery, fixture.Expect.Query)
				}
				if fixture.Expect.BodyJSON != nil {
					body, _ := io.ReadAll(request.Body)
					var got, want any
					if err := json.Unmarshal(body, &got); err != nil {
						t.Errorf("request body: %v", err)
					}
					if err := json.Unmarshal(fixture.Expect.BodyJSON, &want); err != nil {
						t.Errorf("expected body: %v", err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("body = %#v, want %#v", got, want)
					}
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			servers := fixture.Document["servers"].([]any)
			servers[0].(map[string]any)["url"] = server.URL
			document, err := json.Marshal(fixture.Document)
			if err != nil {
				t.Fatal(err)
			}

			engine := NewEngine(nil)
			prepared, err := engine.Prepare(context.Background(), PrepareOptions{
				Source:  Source{Content: document},
				Ref:     fixture.Ref,
				Profile: FullProfile(),
			})
			if err != nil {
				t.Fatal(err)
			}
			execution, err := prepared.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if fixture.Input.Present {
				if err := execution.Send(context.Background(), fixture.Input.Value); err != nil {
					t.Fatal(err)
				}
			}
			if err := execution.FinishInput(); err != nil {
				t.Fatal(err)
			}
			for range execution.Events() {
			}
			terminal := execution.Wait()

			if fixture.Expect.Dispatched {
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
				t.Fatalf("terminal error = %#v, want a pre-dispatch refusal", terminal)
			}
			if executionErr.Code != fixture.Expect.Code {
				t.Errorf("code = %q, want %q", executionErr.Code, fixture.Expect.Code)
			}
			if !strings.Contains(executionErr.Message, fixture.Expect.MessageContains) {
				t.Errorf("message = %q, want it to contain %q", executionErr.Message, fixture.Expect.MessageContains)
			}
			if dispatched.Load() != 0 {
				t.Errorf("refusal must precede dispatch: %d requests hit the server", dispatched.Load())
			}
		})
	}
}
