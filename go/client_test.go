package openapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	openapi "github.com/openbindings/openapi-client/go"
)

func TestAnalysisIsDetachedAndOperationComplete(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Analysis, version: "1"}
servers:
  - url: https://{env}.example.test
    variables: {env: {default: api}}
paths:
  /widgets/{id}:
    post:
      operationId: createWidget
      description: Creates one widget.
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties: {name: {type: string}}
      responses:
        "201":
          description: created
          content: {application/json: {schema: {type: object}}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	first := client.Analysis()
	if len(first.Operations) != 1 {
		t.Fatalf("analysis operations = %#v", first.Operations)
	}
	operation := first.Operations[0]
	if operation.Description != "Creates one widget." || len(operation.Parameters) != 1 || len(operation.RequestBodies) != 1 || len(operation.Responses) != 1 || len(operation.Servers) != 1 {
		t.Fatalf("operation analysis = %#v", operation)
	}
	if operation.RequestBodies[0].Family != "json" || !operation.RequestBodies[0].Required || operation.Responses[0].Key != "201" || !operation.Responses[0].CanSucceed {
		t.Fatalf("request/response analysis = %#v / %#v", operation.RequestBodies, operation.Responses)
	}
	var schema map[string]any
	if err := json.Unmarshal(operation.RequestBodies[0].Schema, &schema); err != nil || schema["type"] != "object" {
		t.Fatalf("body schema = %s err=%v", operation.RequestBodies[0].Schema, err)
	}
	first.Operations[0].Parameters[0].Name = "mutated"
	first.Operations[0].Servers[0].Variables[0] = "mutated"
	first.Operations[0].RequestBodies[0].Schema[0] = 'x'
	second := client.Analysis()
	if second.Operations[0].Parameters[0].Name != "id" || second.Operations[0].Servers[0].Variables[0] != "env" || second.Operations[0].RequestBodies[0].Schema[0] == 'x' {
		t.Fatalf("analysis mutation escaped: %#v", second.Operations[0])
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestPublicClientLoadsCallsAndBindsOperation(t *testing.T) {
	var request *http.Request
	transport := roundTripFunc(func(incoming *http.Request) (*http.Response, error) {
		request = incoming.Clone(incoming.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"name":"Mochi"}`)),
			Request:    incoming,
		}, nil
	})
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Public client, version: "1"}
servers: [{url: https://api.example.test/v1}]
paths:
  /pets/{id}:
    get:
      operationId: getPet
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses:
        "200":
          description: pet
          content: {application/json: {schema: {type: object}}}
`), openapi.Options{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if client.Edition() != openapi.OpenAPI312 {
		t.Fatalf("edition = %q", client.Edition())
	}
	operation, err := client.Operation(openapi.OperationID("getPet"))
	if err != nil {
		t.Fatal(err)
	}
	if operation.Info().Ref != "#/paths/~1pets~1{id}/get" {
		t.Fatalf("operation = %#v", operation.Info())
	}
	result, err := operation.Call(context.Background(), openapi.Input{
		Parameters: openapi.Parameters{Path: map[string]any{"id": "a/b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Data.(map[string]any)["name"] != "Mochi" {
		t.Fatalf("result = %#v", result)
	}
	replayed, err := io.ReadAll(result.Response.Body)
	if err != nil || string(replayed) != `{"name":"Mochi"}` {
		t.Fatalf("response body = %q err=%v", replayed, err)
	}
	if request == nil || request.URL.String() != "https://api.example.test/v1/pets/a%2Fb" {
		t.Fatalf("request = %#v", request)
	}
}

func TestPublicClientUsesTypedSchemeNamedCredentials(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-Session") != "secret" {
			t.Fatalf("credential header = %q", request.Header.Get("X-Session"))
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
	})
	client, err := openapi.Load(context.Background(), openapi.FromText(`
swagger: "2.0"
info: {title: Auth, version: "1"}
schemes: [https]
host: api.example.test
securityDefinitions:
  session: {type: apiKey, in: header, name: X-Session}
paths:
  /private:
    get:
      operationId: private
      security: [{session: []}]
      responses: {"204": {description: done}}
`), openapi.Options{
		HTTPClient: &http.Client{Transport: transport},
		Auth:       openapi.Credentials{"session": openapi.Token("secret")},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), openapi.OperationID("private"), openapi.Input{})
	if err != nil || !result.OK {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestPublicClientSnapshotsMutableDefaults(t *testing.T) {
	selected := 0
	headers := http.Header{"X-Client": []string{"original"}}
	auth := openapi.Credentials{
		"first":  openapi.Token("one"),
		"second": openapi.Token("two"),
	}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-Client") != "original" || request.Header.Get("X-First") != "one" || request.Header.Get("X-Second") != "" {
			t.Fatalf("snapshotted request headers = %#v", request.Header)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
	})
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Snapshot, version: "1"}
servers: [{url: https://api.example.test}]
components:
  securitySchemes:
    first: {type: apiKey, in: header, name: X-First}
    second: {type: apiKey, in: header, name: X-Second}
paths:
  /private:
    get:
      operationId: private
      security: [{first: []}, {second: []}]
      responses: {"204": {description: done}}
`), openapi.Options{
		HTTPClient: &http.Client{Transport: transport}, Headers: headers,
		Auth: auth, SecurityAlternative: &selected,
	})
	if err != nil {
		t.Fatal(err)
	}
	selected = 1
	headers.Set("X-Client", "mutated")
	auth["first"] = openapi.Token("mutated")
	result, err := client.Call(context.Background(), openapi.OperationID("private"), openapi.Input{})
	if err != nil || !result.OK {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestPublicClientSupportsConcurrentCalls(t *testing.T) {
	var dispatches atomic.Int64
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		dispatches.Add(1)
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
	})
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Concurrent client, version: "1"}
servers: [{url: https://api.example.test}]
paths:
  /shared:
    get:
      operationId: shared
      parameters:
        - {name: request, in: query, schema: {type: string}}
      responses: {"204": {description: done}}
`), openapi.Options{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}

	const calls = 32
	errCh := make(chan error, calls)
	var group sync.WaitGroup
	for request := 0; request < calls; request++ {
		group.Add(1)
		go func(request int) {
			defer group.Done()
			result, callErr := client.Call(context.Background(), openapi.OperationID("shared"), openapi.Input{
				Parameters: openapi.Parameters{Query: map[string]any{"request": "shared"}},
			})
			if callErr != nil {
				errCh <- callErr
				return
			}
			if !result.OK {
				errCh <- errors.New("concurrent call returned a non-success result")
			}
		}(request)
	}
	group.Wait()
	close(errCh)
	for callErr := range errCh {
		t.Error(callErr)
	}
	if got := dispatches.Load(); got != calls {
		t.Fatalf("dispatches = %d, want %d", got, calls)
	}
}

func TestPublicClientInventoriesOpenAPI32Methods(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.2.0
info: {title: Methods, version: "1"}
paths:
  /cache:
    query: {operationId: search, responses: {"204": {description: done}}}
    additionalOperations:
      PURGE: {operationId: purge, responses: {"204": {description: done}}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	operations := client.Operations()
	if len(operations) != 2 || operations[0].WireMethod != "PURGE" && operations[1].WireMethod != "PURGE" {
		t.Fatalf("operations = %#v", operations)
	}
}

func TestPublicAnalysisIsDetachedAndStable(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Detached analysis, version: "1"}
servers: [{url: https://api.example.test}]
paths:
  /pets/{id}:
    post:
      operationId: updatePet
      tags: [pets]
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      requestBody:
        content:
          application/json: {schema: {type: object}}
      responses: {"204": {description: done}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	first := client.Analysis()
	if len(first.Operations) != 1 || len(first.Operations[0].Parameters) != 1 || len(first.Operations[0].RequestBodies) != 1 {
		t.Fatalf("analysis = %#v", first)
	}
	first.Operations[0].Info.Tags[0] = "mutated"
	first.Operations[0].Parameters[0].Name = "mutated"
	first.Operations[0].RequestBodies[0].MediaType = "text/plain"

	second := client.Analysis()
	operation, err := client.AnalyzeOperation(openapi.OperationID("updatePet"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Operations[0].Info.Tags[0] != "pets" || operation.Parameters[0].Name != "id" || operation.RequestBodies[0].MediaType != "application/json" {
		t.Fatalf("mutating analysis changed the client snapshot: analysis=%#v operation=%#v", second, operation)
	}
}

func TestPublicAnalysisReportsMultipartBase64Properties(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.0.4
info: {title: Multipart analysis, version: "1"}
servers: [{url: https://api.example.test}]
paths:
  /upload:
    post:
      operationId: upload
      requestBody:
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                bytes: {type: string, format: binary}
                opaque: {}
                text: {type: string}
              allOf:
                - type: object
                  properties:
                    nestedBytes: {type: string, format: binary}
      responses: {"204": {description: done}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := client.AnalyzeOperation(openapi.OperationID("upload"))
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.RequestBodies) != 1 || analysis.RequestBodies[0].MediaType != "multipart/form-data" || analysis.RequestBodies[0].Base64 ||
		len(analysis.RequestBodies[0].Base64Properties) != 3 || analysis.RequestBodies[0].Base64Properties[0] != "bytes" || analysis.RequestBodies[0].Base64Properties[1] != "nestedBytes" || analysis.RequestBodies[0].Base64Properties[2] != "opaque" {
		t.Fatalf("request bodies = %#v", analysis.RequestBodies)
	}
}

func TestPublicPreflightReportsConfigurationWithoutDispatch(t *testing.T) {
	var dispatches atomic.Int64
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Native preflight, version: "1"}
servers:
  - {url: https://one.example.test}
  - {url: https://two.example.test}
components:
  securitySchemes:
    session: {type: apiKey, in: header, name: X-Session}
paths:
  /secured:
    get:
      operationId: secured
      security: [{session: []}]
      responses: {"204": {description: done}}
`), openapi.Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		dispatches.Add(1)
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := client.Operation(openapi.OperationID("secured"))
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := operation.Preflight(context.Background(), openapi.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if requirements == nil || len(requirements.Alternatives) == 0 {
		t.Fatalf("requirements = %#v, want server/security remedies", requirements)
	}
	foundServer := false
	for _, alternative := range requirements.Alternatives {
		for _, requirement := range alternative.Requirements {
			foundServer = foundServer || requirement.Kind == openapi.RequirementOption && requirement.Name == "Server"
		}
	}
	if !foundServer {
		t.Fatalf("requirements = %#v, want a server remedy", requirements)
	}
	if dispatches.Load() != 0 {
		t.Fatalf("preflight dispatched %d requests", dispatches.Load())
	}

	selected := 0
	credentialRequirements, err := operation.Preflight(context.Background(), openapi.Input{}, openapi.CallOptions{
		Server: openapi.Server(selected, nil),
	})
	if err != nil || credentialRequirements == nil || len(credentialRequirements.Alternatives) != 1 ||
		len(credentialRequirements.Alternatives[0].Requirements) != 1 ||
		credentialRequirements.Alternatives[0].Requirements[0].Kind != openapi.RequirementCredential ||
		credentialRequirements.Alternatives[0].Requirements[0].Name != "session" {
		t.Fatalf("credential preflight = %#v, err=%v", credentialRequirements, err)
	}
	ready, err := operation.Preflight(context.Background(), openapi.Input{}, openapi.CallOptions{
		Server: openapi.Server(selected, nil), Auth: openapi.Credentials{"session": openapi.Token("secret")},
	})
	if err != nil || ready != nil {
		t.Fatalf("configured preflight = %#v, err=%v", ready, err)
	}
	if dispatches.Load() != 0 {
		t.Fatalf("configured preflight dispatched %d requests", dispatches.Load())
	}
}

func TestPublicPreflightPreservesOAuthAcquisitionDetails(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: OAuth preflight, version: "1"}
servers: [{url: https://api.example.test}]
components:
  securitySchemes:
    oauth:
      type: oauth2
      flows:
        authorizationCode:
          authorizationUrl: /authorize
          tokenUrl: https://auth.example.test/token
          scopes: {write: modify resources}
paths:
  /secured:
    get:
      operationId: securedOAuth
      security: [{oauth: [write]}]
      responses: {"204": {description: done}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := client.Operation(openapi.OperationID("securedOAuth"))
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := operation.Preflight(context.Background(), openapi.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if requirements == nil || len(requirements.Alternatives) != 1 || len(requirements.Alternatives[0].Requirements) != 1 {
		t.Fatalf("requirements = %#v, want one OAuth alternative", requirements)
	}
	requirement := requirements.Alternatives[0].Requirements[0]
	if requirement.Kind != openapi.RequirementCredential || requirement.Name != "oauth" || requirement.Credential != "oauth2" {
		t.Fatalf("credential requirement = %#v", requirement)
	}
	if got := requirement.Details["grantType"]; got != "authorization_code" {
		t.Fatalf("grantType = %#v", got)
	}
	if got := requirement.Details["authorizeUrl"]; got != "https://api.example.test/authorize" {
		t.Fatalf("authorizeUrl = %#v", got)
	}
	if got := requirement.Details["tokenUrl"]; got != "https://auth.example.test/token" {
		t.Fatalf("tokenUrl = %#v", got)
	}
	scopes, ok := requirement.Details["scopes"].([]any)
	if !ok || len(scopes) != 1 || scopes[0] != "write" {
		t.Fatalf("scopes = %#v", requirement.Details["scopes"])
	}
}

func TestPublicClientClassifiesPreDispatchRefusalAsInput(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Required input, version: "1"}
servers: [{url: https://api.example.test}]
paths:
  /pets/{id}:
    get:
      operationId: getPet
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses: {"204": {description: done}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Call(context.Background(), openapi.OperationID("getPet"), openapi.Input{})
	var clientError *openapi.ClientError
	if !errors.As(err, &clientError) || clientError.Kind != openapi.ErrorInput || clientError.Code != "ERR_REFUSED" {
		t.Fatalf("error = %#v", err)
	}
}

func TestPublicClientReturnsNativeConfigurationRequirements(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Required credential, version: "1"}
servers: [{url: https://api.example.test}]
components:
  securitySchemes:
    session: {type: apiKey, in: header, name: X-Session}
paths:
  /secured:
    get:
      operationId: secured
      security: [{session: []}]
      responses: {"204": {description: done}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Call(context.Background(), openapi.OperationID("secured"), openapi.Input{})
	var clientError *openapi.ClientError
	if !errors.As(err, &clientError) || clientError.Kind != openapi.ErrorConfiguration || clientError.Code != openapi.CodeConfigurationRequired {
		t.Fatalf("error = %#v", err)
	}
	if clientError.Requirements == nil || len(clientError.Requirements.Alternatives) != 1 ||
		len(clientError.Requirements.Alternatives[0].Requirements) != 1 {
		t.Fatalf("requirements = %#v", clientError.Requirements)
	}
	requirement := clientError.Requirements.Alternatives[0].Requirements[0]
	if requirement.Kind != openapi.RequirementCredential || requirement.Name != "session" || requirement.Credential != "apiKey" {
		t.Fatalf("requirement = %#v", requirement)
	}
}

func TestPublicClientRequiresAnExplicitSecurityAlternativeBeforeCredentials(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Security selection, version: "1"}
servers: [{url: https://api.example.test}]
components:
  securitySchemes:
    oauth: {type: oauth2, flows: {clientCredentials: {tokenUrl: https://auth.example.test/token, scopes: {}}}}
    bearer: {type: http, scheme: bearer}
paths:
  /secured:
    get:
      operationId: securedByChoice
      security: [{oauth: []}, {bearer: []}]
      responses: {"204": {description: done}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := client.Operation(openapi.OperationID("securedByChoice"))
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := operation.Preflight(context.Background(), openapi.Input{}, openapi.CallOptions{
		Auth: openapi.Credentials{"oauth": openapi.Token("oauth-token"), "bearer": openapi.Token("bearer-token")},
	})
	if err != nil || requirements == nil || len(requirements.Alternatives) != 1 || len(requirements.Alternatives[0].Requirements) != 1 {
		t.Fatalf("security selection preflight = %#v, err=%v", requirements, err)
	}
	requirement := requirements.Alternatives[0].Requirements[0]
	if requirement.Kind != openapi.RequirementOption || requirement.Name != "SecurityAlternative" || requirement.Path != "" ||
		!reflect.DeepEqual(requirement.AllowedValues, []any{json.Number("0"), json.Number("1")}) {
		t.Fatalf("security selection requirement = %#v", requirement)
	}
	selected := 1
	ready, err := operation.Preflight(context.Background(), openapi.Input{}, openapi.CallOptions{
		SecurityAlternative: &selected,
		Auth:                openapi.Credentials{"bearer": openapi.Token("bearer-token")},
	})
	if err != nil || ready != nil {
		t.Fatalf("selected security preflight = %#v, err=%v", ready, err)
	}
}

func TestPublicClientRequiresSelectionBetweenAnonymousAndCredentialedSecurity(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Optional security selection, version: "1"}
servers: [{url: https://api.example.test}]
components:
  securitySchemes:
    bearer: {type: http, scheme: bearer}
paths:
  /optional:
    get:
      operationId: optionalSecurity
      security: [{}, {bearer: []}]
      responses: {"204": {description: done}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := client.Operation(openapi.OperationID("optionalSecurity"))
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := requirements.Preflight(context.Background(), openapi.Input{})
	if err != nil || preflight == nil || preflight.Alternatives[0].Requirements[0].Name != "SecurityAlternative" {
		t.Fatalf("anonymous security selection = %#v, err=%v", preflight, err)
	}
}

func TestPublicClientNamesRequestMediaAsNativeInput(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Required media, version: "1"}
servers: [{url: https://api.example.test}]
paths:
  /media:
    post:
      operationId: createWithMedia
      requestBody:
        required: true
        content:
          application/json: {schema: {type: object}}
          application/problem+json: {schema: {type: object}}
      responses: {"204": {description: done}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Call(context.Background(), openapi.OperationID("createWithMedia"), openapi.Input{
		Body: map[string]any{"ok": true}, BodyPresent: true,
	})
	var clientError *openapi.ClientError
	if !errors.As(err, &clientError) || clientError.Code != openapi.CodeConfigurationRequired || clientError.Requirements == nil {
		t.Fatalf("error = %#v", err)
	}
	requirement := clientError.Requirements.Alternatives[0].Requirements[0]
	if requirement.Kind != openapi.RequirementInput || requirement.Name != "MediaType" || requirement.Path != "" {
		t.Fatalf("requirement = %#v", requirement)
	}
}

func TestPublicStreamDoesNotExposeACompetingResponseBody(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    request,
		}, nil
	})
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Stream ownership, version: "1"}
servers: [{url: https://api.example.test}]
paths:
  /one:
    get:
      operationId: one
      responses:
        "200":
          description: one
          content: {application/json: {schema: {type: object}}}
`), openapi.Options{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Stream(context.Background(), openapi.OperationID("one"), openapi.Input{})
	if err != nil || !result.OK || result.Response.Body != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	event, open, err := result.Stream.Next(context.Background())
	if err != nil || !open || event.Data.(map[string]any)["ok"] != true {
		t.Fatalf("event=%#v open=%v err=%v", event, open, err)
	}
	if err := result.Stream.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicClientBindsAddressableInvalidTargetBeforeInvocationRefusal(t *testing.T) {
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Invalid target, version: "1"}
paths:
  /items:
    get:
      parameters:
        - {in: query, schema: {type: string}}
      responses: {"204": {description: done}}
`), openapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := client.Operation(openapi.OperationRef("#/paths/~1items/get"))
	if err != nil || operation.Info().Path != "/items" {
		t.Fatalf("operation=%#v err=%v", operation, err)
	}
	_, err = operation.Call(context.Background(), openapi.Input{})
	var clientError *openapi.ClientError
	if !errors.As(err, &clientError) || clientError.Kind != openapi.ErrorInput || clientError.Code != "ERR_REFUSED" {
		t.Fatalf("error = %#v", err)
	}
}

func TestPublicClientSuppliesVariablesWithoutSelectingServerIndex(t *testing.T) {
	var target string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		target = request.URL.String()
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
	})
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Server variables, version: "1"}
servers:
  - url: https://{region}.example.test
    variables:
      region: {default: us, enum: [us, eu]}
paths:
  /ping:
    get:
      operationId: ping
      responses: {"204": {description: done}}
`), openapi.Options{
		HTTPClient: &http.Client{Transport: transport},
		Server:     openapi.ServerVariables(map[string]string{"region": "eu"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), openapi.OperationID("ping"), openapi.Input{})
	if err != nil || !result.OK || target != "https://eu.example.test/ping" {
		t.Fatalf("result=%#v target=%q err=%v", result, target, err)
	}
}

func TestPublicClientSeparatesDocumentAndInvocationHTTPClients(t *testing.T) {
	entryRequests := 0
	invocationRequests := 0
	documentClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		entryRequests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
  "openapi":"3.1.2",
  "info":{"title":"Separate transports","version":"1"},
  "servers":[{"url":"https://api.example.test"}],
  "paths":{"/ping":{"get":{"operationId":"ping","responses":{"204":{"description":"done"}}}}}
}`)),
			Request: request,
		}, nil
	})}
	invocationClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		invocationRequests++
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
	})}
	client, err := openapi.Load(context.Background(), openapi.FromURL("https://documents.example.test/openapi.json"), openapi.Options{
		DocumentHTTPClient: documentClient,
		HTTPClient:         invocationClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), openapi.OperationID("ping"), openapi.Input{})
	if err != nil || !result.OK {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if entryRequests != 1 || invocationRequests != 1 {
		t.Fatalf("entry requests=%d invocation requests=%d", entryRequests, invocationRequests)
	}
}

func TestPublicClientStripsSelectedCredentialsOnCrossOriginRedirects(t *testing.T) {
	fixtures := []struct {
		name     string
		document string
		auth     openapi.Credentials
	}{
		{
			name: "Swagger 2.0",
			document: `swagger: "2.0"
info: {title: Redirect credentials, version: "1"}
schemes: [https]
host: first.example.test
securityDefinitions:
  headerKey: {type: apiKey, in: header, name: X-Secret}
  queryKey: {type: apiKey, in: query, name: querySecret}
  basic: {type: basic}
paths:
  /start:
    get:
      operationId: redirectCredentials
      security: [{headerKey: [], queryKey: [], basic: []}]
      responses: {"204": {description: done}}
`,
			auth: openapi.Credentials{
				"headerKey": openapi.Token("header-secret"),
				"queryKey":  openapi.Token("query-secret"),
				"basic":     openapi.Basic("me", "secret"),
			},
		},
		{
			name: "OpenAPI 3.1",
			document: `openapi: 3.1.2
info: {title: Redirect credentials, version: "1"}
servers: [{url: https://first.example.test}]
components:
  securitySchemes:
    headerKey: {type: apiKey, in: header, name: X-Secret}
    queryKey: {type: apiKey, in: query, name: querySecret}
    cookieKey: {type: apiKey, in: cookie, name: session}
    bearer: {type: http, scheme: bearer}
paths:
  /start:
    get:
      operationId: redirectCredentials
      security: [{headerKey: [], queryKey: [], cookieKey: [], bearer: []}]
      responses: {"204": {description: done}}
`,
			auth: openapi.Credentials{
				"headerKey": openapi.Token("header-secret"),
				"queryKey":  openapi.Token("query-secret"),
				"cookieKey": openapi.Token("cookie-secret"),
				"bearer":    openapi.Token("bearer-secret"),
			},
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var requests []*http.Request
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests = append(requests, request.Clone(request.Context()))
				if len(requests) == 1 {
					return &http.Response{
						StatusCode: http.StatusTemporaryRedirect,
						Header:     http.Header{"Location": []string{"https://second.example.test/final"}},
						Body:       http.NoBody,
						Request:    request,
					}, nil
				}
				return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
			})
			client, err := openapi.Load(context.Background(), openapi.FromText(fixture.document), openapi.Options{
				HTTPClient: &http.Client{Transport: transport}, Redirect: openapi.RedirectFollow,
				Auth: fixture.auth, Headers: http.Header{"X-Trace": []string{"ordinary"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Call(context.Background(), openapi.OperationID("redirectCredentials"), openapi.Input{})
			if err != nil || !result.OK {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if len(requests) != 2 {
				t.Fatalf("requests = %d", len(requests))
			}
			if requests[0].URL.Query().Get("querySecret") != "query-secret" || requests[0].Header.Get("X-Secret") != "header-secret" || requests[0].Header.Get("Authorization") == "" {
				t.Fatalf("initial credentials were not emitted: %s %#v", requests[0].URL, requests[0].Header)
			}
			if requests[1].URL.String() != "https://second.example.test/final" || requests[1].Header.Get("X-Secret") != "" || requests[1].Header.Get("Authorization") != "" || requests[1].Header.Get("Cookie") != "" {
				t.Fatalf("cross-origin credentials leaked: %s %#v", requests[1].URL, requests[1].Header)
			}
			if requests[1].Header.Get("X-Trace") != "ordinary" {
				t.Fatalf("ordinary header was not preserved: %#v", requests[1].Header)
			}
		})
	}
}

func TestPublicCustomSecurityReceivesDetachedSchemeInfo(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Digest native-proof" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
	})
	handler := openapi.CustomSecurity(func(request *http.Request, context openapi.SecurityHandlerContext) error {
		if context.SchemeName != "digest" || context.Scheme.Type != "http" || context.Scheme.Scheme != "digest" || !strings.Contains(string(context.Scheme.JSON), `"scheme":"digest"`) || context.Operation.OperationID != "private" {
			t.Fatalf("security context = %#v", context)
		}
		request.Header.Set("Authorization", "Digest native-proof")
		return nil
	})
	client, err := openapi.Load(context.Background(), openapi.FromText(`
openapi: 3.1.2
info: {title: Custom security, version: "1"}
servers: [{url: https://api.example.test}]
components:
  securitySchemes:
    digest: {type: http, scheme: digest, description: Native Digest}
paths:
  /private:
    get:
      operationId: private
      security: [{digest: []}]
      responses: {"204": {description: done}}
`), openapi.Options{
		HTTPClient: &http.Client{Transport: transport},
		Auth:       openapi.Credentials{"digest": handler},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), openapi.OperationID("private"), openapi.Input{})
	if err != nil || !result.OK {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
