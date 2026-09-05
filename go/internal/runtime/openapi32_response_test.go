package openapiclient

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type openAPI32ResponseTransport struct {
	responses map[string]*http.Response
	requests  int
}

func (t *openAPI32ResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests++
	template := t.responses[request.URL.Path]
	if template == nil {
		template = &http.Response{StatusCode: http.StatusNoContent, Status: "204 No Content", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
	}
	response := *template
	response.Header = template.Header.Clone()
	if template.Body != nil {
		body, _ := io.ReadAll(template.Body)
		template.Body = io.NopCloser(bytes.NewReader(body))
		response.Body = io.NopCloser(bytes.NewReader(body))
	}
	response.Request = request
	return &response, nil
}

func TestOpenAPI32ResponseSelectionUsesExactRangeDefaultAndNativeStatus(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: responses, version: "1"}
servers: [{url: https://api.example}]
paths:
  /x:
    get:
      responses:
        '206': {content: {application/json: {schema: {type: object}}}}
        2XX: {content: {text/plain: {schema: {type: string}}}}
        default: {content: {application/problem+json: {schema: {type: object}}}}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/get")
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		status int
		key    string
		ok     bool
	}{{206, "206", true}, {207, "2XX", true}, {404, "default", false}} {
		selection, selectErr := artifact.SelectOpenAPI32Response(target, testCase.status)
		if selectErr != nil {
			t.Fatal(selectErr)
		}
		if selection.ResponseKey != testCase.key || selection.Success != testCase.ok || selection.Response == nil {
			t.Errorf("status %d selection = %#v, want key %q success %v", testCase.status, selection, testCase.key, testCase.ok)
		}
	}
}

func TestOpenAPI32InvalidResponseKeyExcludesBeforeDispatch(t *testing.T) {
	for _, key := range []string{"2xx", "600", "20X", "200-299"} {
		transport := &openAPI32ResponseTransport{}
		client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: invalid response key, version: "1"}
servers: [{url: https://api.example}]
paths:
  /x:
    get:
      responses:
        '` + key + `': {}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatalf("key %q load: %v", key, err)
		}
		if _, err := client.Call(context.Background(), PathOperation("/x", GET), Input{}); err == nil {
			t.Errorf("key %q error = %v", key, err)
		}
		if transport.requests != 0 {
			t.Errorf("key %q dispatched %d requests", key, transport.requests)
		}
	}
}

func TestOpenAPI32ResponseGovernanceValueBoundariesAndFailureData(t *testing.T) {
	transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{
		"/exact": {
			StatusCode: 206, Status: "206 Partial Content", Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"lane":"exact"}`)),
		},
		"/range": {
			StatusCode: 207, Status: "207 Multi-Status", Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
			Body: io.NopCloser(strings.NewReader("range")),
		},
		"/failure": {
			StatusCode: 404, Status: "404 Not Found", Header: http.Header{"Content-Type": {"application/problem+json"}},
			Body: io.NopCloser(strings.NewReader(`{"code":"missing"}`)),
		},
		"/binary": {
			StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(bytes.NewReader([]byte{0, 1, 2})),
		},
		"/empty": {
			StatusCode: 204, Status: "204 No Content", Header: http.Header{"X-Ignored-Output": {"transport"}}, Body: io.NopCloser(strings.NewReader("")),
		},
		"/required": {
			StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		},
	}}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response governance, version: "1"}
servers: [{url: https://api.example}]
paths:
  /exact: {get: {responses: {'206': {content: {application/json: {schema: {type: object}}}}, 2XX: {content: {text/plain: {schema: {type: string}}}}}}}
  /range: {get: {responses: {2XX: {content: {text/plain: {schema: {type: string}}}}, default: {content: {application/json: {schema: {type: object}}}}}}}
  /failure: {get: {responses: {default: {content: {application/problem+json: {schema: {type: object}}}}}}}
  /binary: {get: {responses: {'200': {content: {application/octet-stream: {}}}}}}
  /empty: {get: {responses: {'204': {headers: {X-Ignored-Output: {required: true, schema: {type: string}}}, links: {ignored: {operationId: elsewhere}}}}}}
  /required: {get: {responses: {'200': {headers: {X-Required: {required: true, schema: {type: string}}}, content: {application/json: {schema: {type: object}}}}}}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}

	exact, err := client.Call(context.Background(), PathOperation("/exact", GET), Input{})
	if err != nil || !exact.OK || !reflect.DeepEqual(exact.Data, map[string]any{"lane": "exact"}) || exact.OpenAPI.ResponseKey != "206" {
		t.Fatalf("exact result = %#v, err %v", exact, err)
	}
	ranged, err := client.Call(context.Background(), PathOperation("/range", GET), Input{})
	if err != nil || !ranged.OK || ranged.Data != "range" || ranged.OpenAPI.ResponseKey != "2XX" {
		t.Fatalf("range result = %#v, err %v", ranged, err)
	}
	failure, err := client.Call(context.Background(), PathOperation("/failure", GET), Input{})
	if err != nil || failure.OK || !reflect.DeepEqual(failure.Error, map[string]any{"code": "missing"}) || failure.OpenAPI.ResponseKey != "default" {
		t.Fatalf("failure result = %#v, err %v", failure, err)
	}
	binary, err := client.Call(context.Background(), PathOperation("/binary", GET), Input{})
	if err != nil || !binary.OK || !reflect.DeepEqual(binary.Data, []byte{0, 1, 2}) {
		t.Fatalf("binary result = %#v, err %v", binary, err)
	}
	empty, err := client.Call(context.Background(), PathOperation("/empty", GET), Input{})
	if err != nil || !empty.OK || empty.Data != nil {
		t.Fatalf("empty result = %#v, err %v", empty, err)
	}
	if _, err := client.Call(context.Background(), PathOperation("/required", GET), Input{}); err == nil || !strings.Contains(err.Error(), "required response header") {
		t.Fatalf("missing required header error = %v", err)
	}
}

func TestOpenAPI32UndeclaredEmptyResponseCompletesButNonEmptyIsProtocolError(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		ok   bool
	}{{"empty", "", true}, {"non-empty", "x", false}} {
		transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
			StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader(testCase.body)),
		}}}
		client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: omitted responses, version: "1"}
servers: [{url: https://api.example}]
paths: {/x: {get: {}}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatal(err)
		}
		result, callErr := client.Call(context.Background(), PathOperation("/x", GET), Input{})
		if testCase.ok {
			if callErr != nil || result == nil || !result.OK || result.Data != nil {
				t.Errorf("%s result = %#v, err %v", testCase.name, result, callErr)
			}
		} else if callErr == nil || !strings.Contains(callErr.Error(), "no governing Response Object") {
			t.Errorf("%s error = %v", testCase.name, callErr)
		}
	}
}
