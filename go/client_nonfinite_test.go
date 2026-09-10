package openapi_test

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	openapi "github.com/openbindings/openapi-client/go"
)

func TestNativeNonfiniteJSONNeverDispatches(t *testing.T) {
	for _, edition := range []string{"2.0", "3.0.4", "3.1.2", "3.2.0"} {
		for _, value := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
			t.Run(edition+"/"+stringValue(value), func(t *testing.T) {
				schema := map[string]any{"type": "object", "additionalProperties": true}
				operation := map[string]any{"operationId": "createItem", "responses": map[string]any{"204": map[string]any{"description": "ok"}}}
				document := map[string]any{"info": map[string]any{"title": "Nonfinite", "version": "1"}, "paths": map[string]any{"/items": map[string]any{"post": operation}}}
				if edition == "2.0" {
					document["swagger"], document["host"], document["schemes"], document["consumes"] = edition, "fixture.invalid", []string{"https"}, []string{"application/json"}
					operation["parameters"] = []any{map[string]any{"name": "body", "in": "body", "required": true, "schema": schema}}
				} else {
					document["openapi"], document["servers"] = edition, []any{map[string]any{"url": "https://fixture.invalid"}}
					operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
				}
				raw, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				dispatches := 0
				client, err := openapi.Load(context.Background(), openapi.FromText(string(raw)), openapi.Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					dispatches++
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.Call(context.Background(), openapi.OperationID("createItem"), openapi.Input{Body: map[string]any{"nested": []any{value}}})
				if err == nil || dispatches != 0 {
					t.Fatalf("error=%v dispatches=%d", err, dispatches)
				}
			})
		}
	}
}

func stringValue(value float64) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	return "-Infinity"
}
