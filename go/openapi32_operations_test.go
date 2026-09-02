package openapiclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type openAPI32OperationTransport struct {
	requests []*http.Request
	bodies   [][]byte
}

func (t *openAPI32OperationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
	}
	t.requests = append(t.requests, request)
	t.bodies = append(t.bodies, body)
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}

func TestOpenAPI32FixedAndAdditionalMethodsReachTheWireExactly(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: methods, version: "1"}
servers: [{url: https://api.example}]
paths:
  /query:
    query:
      requestBody:
        required: true
        content:
          application/json:
            schema: {type: object}
      responses: {'204': {description: ok}}
  /custom:
    additionalOperations:
      MiXeD:
        requestBody:
          required: true
          content:
            application/json:
              schema: {type: object}
        responses: {'204': {description: ok}}
      F~O:
        responses: {'204': {description: ok}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Call(context.Background(), PathOperation("/query", QUERY), Input{
		Body: map[string]any{"term": "one"}, MediaType: "application/json",
	}); err != nil {
		t.Fatalf("QUERY call: %v", err)
	}
	if _, err := client.Call(context.Background(), AdditionalOperation("/custom", "MiXeD"), Input{
		Body: map[string]any{"term": "two"}, MediaType: "application/json",
	}); err != nil {
		t.Fatalf("MiXeD call: %v", err)
	}
	if _, err := client.Call(context.Background(), OperationRef("#/paths/~1custom/additionalOperations/F~0O"), Input{}); err != nil {
		t.Fatalf("F~O call: %v", err)
	}

	if len(transport.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(transport.requests))
	}
	for index, want := range []struct {
		method string
		path   string
		body   string
	}{{"QUERY", "/query", `{"term":"one"}`}, {"MiXeD", "/custom", `{"term":"two"}`}, {"F~O", "/custom", ""}} {
		request := transport.requests[index]
		if request.Method != want.method || request.URL.Path != want.path || string(transport.bodies[index]) != want.body {
			t.Errorf("request %d = %s %s %q, want %s %s %q", index, request.Method, request.URL.Path, transport.bodies[index], want.method, want.path, want.body)
		}
	}
}

func TestOpenAPI32TRACEDeclarationNeverCreatesARequestBody(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: trace, version: "1"}
servers: [{url: https://api.example}]
paths:
  /trace:
    trace:
      requestBody:
        required: true
        content:
          application/json:
            schema: {type: object}
      responses: {'204': {description: ok}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), PathOperation("/trace", TRACE), Input{}); err != nil {
		t.Fatalf("body-free TRACE: %v", err)
	}
	if len(transport.requests) != 1 || transport.requests[0].Method != "TRACE" || len(transport.bodies[0]) != 0 {
		t.Fatalf("TRACE dispatch = %#v body %q", transport.requests, transport.bodies[0])
	}
	if _, err := client.Call(context.Background(), PathOperation("/trace", TRACE), Input{
		Body: map[string]any{"forbidden": true}, BodyPresent: true, MediaType: "application/json",
	}); err == nil {
		t.Fatal("TRACE with a supplied body unexpectedly dispatched")
	}
	if len(transport.requests) != 1 {
		t.Fatalf("body-bearing TRACE reached transport: %d requests", len(transport.requests))
	}
}

func TestOpenAPI32AdditionalOperationIdentityAndPathItemComposition(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: additional identity, version: "1"}
components:
  pathItems:
    Shared:
      additionalOperations:
        MiXeD: {operationId: referenced}
        UPPER: {operationId: upper}
paths:
  /composed:
    $ref: '#/components/pathItems/Shared'
    additionalOperations:
      lower: {operationId: adjacent}
  /collision:
    $ref: '#/components/pathItems/Shared'
    additionalOperations:
      MiXeD: {operationId: collision}
  /keys:
    additionalOperations:
      mixed: {operationId: lowerCase}
      MiXeD: {operationId: mixedCase}
      QuErY: {operationId: caseDistinctFromQUERY}
      QUERY: {operationId: fixedCollision}
      'BAD METHOD': {operationId: invalidToken}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{
		"#/paths/~1composed/additionalOperations/MiXeD",
		"#/paths/~1composed/additionalOperations/UPPER",
		"#/paths/~1composed/additionalOperations/lower",
		"#/paths/~1keys/additionalOperations/mixed",
		"#/paths/~1keys/additionalOperations/MiXeD",
		// §6.1: `QuErY` spells a method token the fixed `query` field does not
		// send, so it is admitted; only the byte-exact `QUERY` is the defect.
		"#/paths/~1keys/additionalOperations/QuErY",
	} {
		if _, err := artifact.ResolveOperation(ref); err != nil {
			t.Errorf("ResolveOperation(%q): %v", ref, err)
		}
	}
	for _, ref := range []string{
		"#/paths/~1composed/additionalOperations/MIXED",
		"#/paths/~1collision/additionalOperations/MiXeD",
		"#/paths/~1keys/additionalOperations/QUERY",
		"#/paths/~1keys/additionalOperations/BAD METHOD",
	} {
		if _, err := artifact.ResolveOperation(ref); err == nil {
			t.Errorf("ResolveOperation(%q) unexpectedly succeeded", ref)
		}
	}
}

func TestOperationReferenceWireMethod(t *testing.T) {
	for _, testCase := range []struct {
		ref  OperationReference
		want string
	}{
		{OperationReference{Method: "query"}, "QUERY"},
		{OperationReference{Method: "post"}, "POST"},
		{OperationReference{Method: "MiXeD", Additional: true}, "MiXeD"},
	} {
		if got := testCase.ref.WireMethod(); got != testCase.want {
			t.Errorf("WireMethod(%#v) = %q, want %q", testCase.ref, got, testCase.want)
		}
	}
}
