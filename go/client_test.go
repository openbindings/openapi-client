package openapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	openapi "github.com/openbindings/openapi-client/go"
)

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
