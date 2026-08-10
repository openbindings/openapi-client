package openapiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

type sharedNativeCase struct {
	Name        string         `json:"name"`
	Document    map[string]any `json:"document"`
	OperationID string         `json:"operationId"`
	Auth        map[string]any `json:"auth"`
	Input       struct {
		Parameters Parameters      `json:"parameters"`
		Body       json.RawMessage `json:"body"`
		BodyBase64 string          `json:"bodyBase64"`
		MediaType  string          `json:"mediaType"`
	} `json:"input"`
	Response struct {
		Status     int               `json:"status"`
		Headers    map[string]string `json:"headers"`
		Body       json.RawMessage   `json:"body"`
		BodyBase64 string            `json:"bodyBase64"`
	} `json:"response"`
	Expect struct {
		Method       string            `json:"method"`
		Path         string            `json:"path"`
		Query        string            `json:"query"`
		Headers      map[string]string `json:"headers"`
		BodyJSON     json.RawMessage   `json:"bodyJSON"`
		BodyBase64   string            `json:"bodyBase64"`
		OK           bool              `json:"ok"`
		Value        any               `json:"value"`
		ValueBase64  string            `json:"valueBase64"`
		ValueOmitted bool              `json:"valueOmitted"`
		ResponseKey  string            `json:"responseKey"`
		MediaType    string            `json:"mediaType"`
	} `json:"expect"`
}

func TestSharedNativeWireConformance(t *testing.T) {
	data, err := os.ReadFile("../conformance/cases/native-wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []sharedNativeCase
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != fixture.Expect.Method || request.URL.Path != fixture.Expect.Path || request.URL.RawQuery != fixture.Expect.Query {
					t.Errorf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
				}
				for name, value := range fixture.Expect.Headers {
					if request.Header.Get(name) != value {
						t.Errorf("header %s = %q, want %q", name, request.Header.Get(name), value)
					}
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
				if fixture.Expect.BodyBase64 != "" {
					body, _ := io.ReadAll(request.Body)
					want, decodeErr := base64.StdEncoding.DecodeString(fixture.Expect.BodyBase64)
					if decodeErr != nil {
						t.Errorf("expected request base64: %v", decodeErr)
					}
					if !reflect.DeepEqual(body, want) {
						t.Errorf("request body = %#v, want %#v", body, want)
					}
				}
				for name, value := range fixture.Response.Headers {
					writer.Header().Set(name, value)
				}
				writer.WriteHeader(fixture.Response.Status)
				if fixture.Response.BodyBase64 != "" {
					body, decodeErr := base64.StdEncoding.DecodeString(fixture.Response.BodyBase64)
					if decodeErr != nil {
						t.Errorf("response base64: %v", decodeErr)
					}
					_, _ = writer.Write(body)
				} else if fixture.Response.Body != nil && string(fixture.Response.Body) != "null" {
					_, _ = writer.Write(fixture.Response.Body)
				}
			}))
			defer server.Close()
			servers := fixture.Document["servers"].([]any)
			servers[0].(map[string]any)["url"] = server.URL
			document, _ := json.Marshal(fixture.Document)
			client, err := Load(context.Background(), Source{Content: document}, ClientOptions{Auth: fixture.Auth})
			if err != nil {
				t.Fatal(err)
			}
			input := Input{Parameters: fixture.Input.Parameters, MediaType: fixture.Input.MediaType}
			if fixture.Input.Body != nil {
				input.BodyPresent = true
				if err := json.Unmarshal(fixture.Input.Body, &input.Body); err != nil {
					t.Fatal(err)
				}
			}
			if fixture.Input.BodyBase64 != "" {
				input.Body, err = base64.StdEncoding.DecodeString(fixture.Input.BodyBase64)
				if err != nil {
					t.Fatal(err)
				}
				input.BodyPresent = true
			}
			result, err := client.Call(context.Background(), OperationID(fixture.OperationID), input)
			if err != nil {
				t.Fatal(err)
			}
			if result.OK != fixture.Expect.OK {
				t.Fatalf("ok = %v, want %v", result.OK, fixture.Expect.OK)
			}
			value := result.Data
			if !result.OK {
				value = result.Error
			}
			if fixture.Expect.ValueBase64 != "" {
				want, _ := base64.StdEncoding.DecodeString(fixture.Expect.ValueBase64)
				if !reflect.DeepEqual(value, want) {
					t.Fatalf("value = %#v, want bytes %#v", value, want)
				}
			} else if !fixture.Expect.ValueOmitted && !reflect.DeepEqual(value, fixture.Expect.Value) {
				t.Fatalf("value = %#v, want %#v", value, fixture.Expect.Value)
			}
			if fixture.Expect.ResponseKey != "" && result.OpenAPI.ResponseKey != fixture.Expect.ResponseKey {
				t.Fatalf("response key = %q", result.OpenAPI.ResponseKey)
			}
			if fixture.Expect.MediaType != "" && result.OpenAPI.MediaType != fixture.Expect.MediaType {
				t.Fatalf("media type = %q", result.OpenAPI.MediaType)
			}
		})
	}
}
