package openapiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testDocument(serverURL, paths string) []byte {
	return []byte(fmt.Sprintf(`{
  "openapi":"3.1.0",
  "info":{"title":"test","version":"1"},
  "servers":[{"url":%q}],
  "paths":%s
}`, serverURL, paths))
}

func TestClientCallPreservesSameNamedParameterAndBodyIdentities(t *testing.T) {
	var request struct{ method, path, query, contentType, body string }
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		request.method, request.path, request.query = incoming.Method, incoming.URL.Path, incoming.URL.RawQuery
		request.contentType = incoming.Header.Get("Content-Type")
		body, _ := io.ReadAll(incoming.Body)
		request.body = string(body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	document := testDocument(server.URL, `{
  "/items/{id}":{"post":{"operationId":"replaceItem","parameters":[
    {"name":"id","in":"path","required":true,"schema":{"type":"string"}},
    {"name":"id","in":"query","schema":{"type":"string"}}
  ],"requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"id":{"type":"string"}}}}}},
  "responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}
}`)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), OperationID("replaceItem"), Input{
		Parameters: Parameters{Path: map[string]any{"id": "path"}, Query: map[string]any{"id": "query"}},
		Body:       map[string]any{"id": "body"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || !reflect.DeepEqual(result.Data, map[string]any{"ok": true}) {
		t.Fatalf("result = %#v", result)
	}
	if request.method != "POST" || request.path != "/items/path" || request.query != "id=query" {
		t.Fatalf("request = %#v", request)
	}
	if request.contentType != "application/json" || request.body != `{"id":"body"}` {
		t.Fatalf("request media = %#v", request)
	}
}

func TestClientCallPreservesFalsyWholeJSONBodies(t *testing.T) {
	for _, value := range []any{false, float64(0), "", nil} {
		t.Run(fmt.Sprintf("%v", value), func(t *testing.T) {
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				body, _ = io.ReadAll(incoming.Body)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`true`))
			}))
			defer server.Close()
			document := testDocument(server.URL, `{ "/value":{"post":{"operationId":"setValue","requestBody":{"required":true,"content":{"application/json":{"schema":{}}}},"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}} }`)
			client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Call(context.Background(), OperationID("setValue"), Input{Body: value, BodyPresent: true})
			if err != nil {
				t.Fatal(err)
			}
			if !result.OK {
				t.Fatalf("result = %#v", result)
			}
			want, _ := json.Marshal(value)
			if string(body) != string(want) {
				t.Fatalf("body = %q, want %q", body, want)
			}
		})
	}
}

func TestClientKeepsRedirectsObservableByDefaultAndAllowsConfiguredFollowing(t *testing.T) {
	type observation struct{ method, body string }
	observed := map[string]observation{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		body, _ := io.ReadAll(incoming.Body)
		observed[incoming.URL.Path] = observation{method: incoming.Method, body: string(body)}
		switch incoming.URL.Path {
		case "/rewrite":
			http.Redirect(writer, incoming, "/rewrite-final", http.StatusSeeOther)
		case "/preserve":
			http.Redirect(writer, incoming, "/preserve-final", http.StatusTemporaryRedirect)
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	document := testDocument(server.URL, `{
  "/rewrite":{"post":{"operationId":"rewrite","requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object"}}}},"responses":{"204":{"description":"done"}}}},
  "/preserve":{"post":{"operationId":"preserve","requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object"}}}},"responses":{"204":{"description":"done"}}}}
}`)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, operationID := range []string{"rewrite", "preserve"} {
		result, callErr := client.Call(context.Background(), OperationID(operationID), Input{
			Body:        map[string]any{"value": operationID},
			BodyPresent: true,
		})
		if callErr != nil || result.OK {
			t.Fatalf("%s: result=%#v err=%v", operationID, result, callErr)
		}
	}
	if _, present := observed["/rewrite-final"]; present {
		t.Fatal("default client followed a 303 response")
	}
	if _, present := observed["/preserve-final"]; present {
		t.Fatal("default client followed a 307 response")
	}
	following, err := Load(context.Background(), Source{Content: document}, ClientOptions{HTTPClient: &http.Client{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, operationID := range []string{"rewrite", "preserve"} {
		result, callErr := following.Call(context.Background(), OperationID(operationID), Input{
			Body:        map[string]any{"value": operationID},
			BodyPresent: true,
		})
		if callErr != nil || !result.OK {
			t.Fatalf("follow %s: result=%#v err=%v", operationID, result, callErr)
		}
	}
	if got := observed["/rewrite-final"]; got != (observation{method: http.MethodGet}) {
		t.Fatalf("303 final request = %#v", got)
	}
	if got := observed["/preserve-final"]; got != (observation{method: http.MethodPost, body: `{"value":"preserve"}`}) {
		t.Fatalf("307 final request = %#v", got)
	}
}

func TestClientAcceptsNativeBytesForRawRequestMedia(t *testing.T) {
	want := []byte{0, 1, 254, 255}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.Header.Get("Content-Type") != "image/png" {
			t.Errorf("content type = %q", incoming.Header.Get("Content-Type"))
		}
		got, _ := io.ReadAll(incoming.Body)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("body = %#v, want %#v", got, want)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	document := testDocument(server.URL, `{ "/image":{"put":{"operationId":"uploadImage","requestBody":{"required":true,"content":{"image/png":{}}},"responses":{"204":{"description":"stored"}}}} }`)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), OperationID("uploadImage"), Input{Body: want, BodyPresent: true, MediaType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientReturnsDeclaredHTTPFailureAsNativeResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.Header().Set("X-Trace", "trace-1")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"code":"denied"}`))
	}))
	defer server.Close()
	document := testDocument(server.URL, `{ "/secret":{"get":{"operationId":"secret","responses":{"403":{"description":"denied","content":{"application/problem+json":{}}}}}} }`)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), OperationID("secret"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Response.StatusCode != 403 || result.Response.Header.Get("X-Trace") != "trace-1" {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(result.Error, map[string]any{"code": "denied"}) {
		t.Fatalf("failure = %#v", result.Error)
	}
	if !result.OpenAPI.Declared || result.OpenAPI.ResponseKey != "403" || result.OpenAPI.MediaType != "application/problem+json" {
		t.Fatalf("declaration = %#v", result.OpenAPI)
	}
}

func TestClientKeysCredentialsByAuthoredSchemeName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.Header.Get("X-Tenant") != "tenant-value" || incoming.URL.Query().Get("session") != "session-value" {
			t.Errorf("credentials were not applied by scheme: headers=%v url=%s", incoming.Header, incoming.URL)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	document := testDocument(server.URL, `{ "/secure":{"get":{"operationId":"secure","security":[{"tenant":[],"session":[]}],"responses":{"204":{"description":"ok"}}}},
  "components-placeholder":{} }`)
	var raw map[string]any
	if err := json.Unmarshal(document, &raw); err != nil {
		t.Fatal(err)
	}
	raw["components"] = map[string]any{"securitySchemes": map[string]any{
		"tenant":  map[string]any{"type": "apiKey", "in": "header", "name": "X-Tenant"},
		"session": map[string]any{"type": "apiKey", "in": "query", "name": "session"},
	}}
	document, _ = json.Marshal(raw)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{Auth: map[string]any{"tenant": "tenant-value", "session": "session-value"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), OperationID("secure"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
}

func TestEngineDerivesCustomSecurityOnlyFromInstalledHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.Header.Get("Cookie") != "session=ready" {
			t.Errorf("handler did not observe final cookie header: %q", incoming.Header.Get("Cookie"))
		}
		if incoming.Header.Get("Authorization") != "Digest engine-proof" {
			t.Errorf("authorization = %q", incoming.Header.Get("Authorization"))
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	document := testDocument(server.URL, `{ "/secure":{"get":{"security":[{"digest":[]}],"responses":{"204":{"description":"ok"}}}} }`)
	var raw map[string]any
	if err := json.Unmarshal(document, &raw); err != nil {
		t.Fatal(err)
	}
	raw["components"] = map[string]any{"securitySchemes": map[string]any{
		"digest": map[string]any{"type": "http", "scheme": "digest"},
	}}
	document, _ = json.Marshal(raw)
	engine := NewEngine(nil)

	spoofed, err := engine.Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: document},
		Ref:     "#/paths/~1secure/get",
		Profile: FullProfile(),
		Context: map[string]any{"$openapiSecurity": map[string]bool{"digest": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spoofed.Prerequisites() == nil {
		t.Fatal("caller-authored security marker bypassed prerequisite discovery")
	}
	if _, err := spoofed.Start(context.Background()); err == nil {
		t.Fatal("caller-authored security marker bypassed execution refusal")
	} else {
		var executionErr *ExecutionError
		if !errors.As(err, &executionErr) || executionErr.Code != CodeContextRequired {
			t.Fatalf("spoofed marker error = %#v", err)
		}
	}

	prepared, err := engine.Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: document},
		Ref:     "#/paths/~1secure/get",
		Profile: FullProfile(),
		Context: map[string]any{"cookies": map[string]any{"session": "ready"}},
		SecurityHandlers: map[string]SecurityHandler{
			"digest": func(request *http.Request, context SecurityHandlerContext) error {
				if context.SchemeName != "digest" {
					return fmt.Errorf("scheme = %q", context.SchemeName)
				}
				if request.Header.Get("Cookie") != "session=ready" {
					return fmt.Errorf("handler observed cookie %q", request.Header.Get("Cookie"))
				}
				request.Header.Set("Authorization", "Digest engine-proof")
				return nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Prerequisites() != nil {
		t.Fatalf("installed handler left prerequisites: %#v", prepared.Prerequisites())
	}
	execution, err := prepared.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for range execution.Events() {
		t.Fatal("204 response emitted an output")
	}
	if err := execution.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestEnginePreservesDeclaredJSONFailureValuesIncludingNull(t *testing.T) {
	values := []any{map[string]any{"reason": "missing"}, []any{"missing"}, "missing", float64(0), false, nil}
	for _, value := range values {
		value := value
		t.Run(fmt.Sprintf("%T-%v", value, value), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/problem+json")
				writer.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(writer).Encode(value)
			}))
			defer server.Close()

			engine := NewEngine(nil)
			prepared, err := engine.Prepare(context.Background(), PrepareOptions{
				Source: Source{Content: testDocument(server.URL, `{
					"/widgets":{"get":{"responses":{"404":{"description":"missing","content":{"application/problem+json":{}}}}}}
				}`)},
				Ref: "#/paths/~1widgets/get", Profile: FullProfile(),
			})
			if err != nil {
				t.Fatal(err)
			}
			execution, err := prepared.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for range execution.Events() {
				t.Fatal("unsuccessful response emitted an output")
			}
			var terminal *ExecutionError
			if err := execution.Wait(); !errors.As(err, &terminal) {
				t.Fatalf("terminal = %#v", err)
			}
			if !terminal.DetailsPresent || !reflect.DeepEqual(terminal.Details, value) {
				t.Fatalf("details = present %v, %#v; want %#v", terminal.DetailsPresent, terminal.Details, value)
			}
		})
	}
}

// A declared failure body decodes through the same response lanes as a
// successful body (openbindings.openapi §9.5, ruled 2026-08-13): a declared
// JSON lane refuses a malformed body (nothing promoted), the text lane
// carries the string, and a declared raw-byte lane carries canonical Base64.
func TestEngineFailureBodiesDecodeThroughSuccessLanes(t *testing.T) {
	expectations := map[string]struct {
		present bool
		details any
	}{
		"application/json":         {present: false},
		"text/plain":               {present: true, details: "not-json"},
		"application/octet-stream": {present: true, details: base64.StdEncoding.EncodeToString([]byte("not-json"))},
	}
	for _, contentType := range []string{"application/json", "text/plain", "application/octet-stream"} {
		contentType := contentType
		t.Run(contentType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", contentType)
				writer.WriteHeader(http.StatusNotFound)
				_, _ = writer.Write([]byte("not-json"))
			}))
			defer server.Close()
			engine := NewEngine(nil)
			prepared, err := engine.Prepare(context.Background(), PrepareOptions{
				Source: Source{Content: testDocument(server.URL, `{
					"/widgets":{"get":{"responses":{"404":{"description":"missing","content":{"application/json":{},"text/plain":{},"application/octet-stream":{}}}}}}
				}`)},
				Ref: "#/paths/~1widgets/get", Profile: FullProfile(),
			})
			if err != nil {
				t.Fatal(err)
			}
			execution, err := prepared.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for range execution.Events() {
			}
			var terminal *ExecutionError
			if err := execution.Wait(); !errors.As(err, &terminal) {
				t.Fatalf("terminal = %#v", err)
			}
			want := expectations[contentType]
			if terminal.DetailsPresent != want.present {
				t.Fatalf("details present = %v (%#v); want %v", terminal.DetailsPresent, terminal.Details, want.present)
			}
			if want.present && !reflect.DeepEqual(terminal.Details, want.details) {
				t.Fatalf("details = %#v; want %#v", terminal.Details, want.details)
			}
		})
	}
}

func TestClientSSEPreservesOrderingFramingAndCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "id: 7\nevent: update\nretry: 50\ndata: first\n\ndata: second\n\n")
	}))
	defer server.Close()
	document := testDocument(server.URL, `{ "/events":{"get":{"operationId":"events","responses":{"200":{"description":"ok","content":{"text/event-stream":{}}}}}} }`)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Stream(context.Background(), OperationID("events"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	var events []StreamEvent
	for {
		event, open, err := result.Stream.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !open {
			break
		}
		events = append(events, event)
	}
	if len(events) != 2 || events[0].Data != "first" || events[1].Data != "second" {
		t.Fatalf("events = %#v", events)
	}
	if events[0].SSE == nil || events[0].SSE.Event != "update" || events[0].SSE.ID != "7" || events[0].SSE.Retry == nil || *events[0].SSE.Retry != 50 {
		t.Fatalf("first framing = %#v", events[0].SSE)
	}
	if events[1].SSE == nil || events[1].SSE.ID != "7" {
		t.Fatalf("second framing = %#v", events[1].SSE)
	}
}

func TestStreamPreservesPartialOutputBeforeCancellation(t *testing.T) {
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: partial\n\n")
		writer.(http.Flusher).Flush()
		select {
		case <-incoming.Context().Done():
		case <-released:
		}
	}))
	defer func() { close(released); server.Close() }()
	document := testDocument(server.URL, `{ "/events":{"get":{"operationId":"events","responses":{"200":{"description":"ok","content":{"text/event-stream":{}}}}}} }`)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Stream(context.Background(), OperationID("events"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	receiveContext, stopReceiving := context.WithTimeout(context.Background(), time.Second)
	defer stopReceiving()
	event, open, err := result.Stream.Next(receiveContext)
	if err != nil || !open || event.Data != "partial" {
		t.Fatalf("partial event = %#v, open = %v, err = %v", event, open, err)
	}
	result.Stream.Cancel()
	select {
	case <-result.Stream.Done():
	case <-time.After(time.Second):
		t.Fatal("cancellation did not complete")
	}
	err = result.Stream.Wait()
	var clientErr *ClientError
	if !errors.As(err, &clientErr) || clientErr.Kind != ErrorCancelled {
		t.Fatalf("cancel error = %#v", err)
	}
	if _, open, err := result.Stream.Next(context.Background()); open || !errors.As(err, &clientErr) || clientErr.Kind != ErrorCancelled {
		t.Fatalf("terminal receive = open %v, err %#v", open, err)
	}
}

func TestExecutionBareHalfCloseRefusesRequiredInput(t *testing.T) {
	engine := NewEngine(nil)
	prepared, err := engine.Prepare(context.Background(), PrepareOptions{
		Source: Source{Content: testDocument("https://api.example.test", `{
			"/widgets/{id}":{"get":{
				"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
				"responses":{"204":{"description":"done"}}
			}}
		}`)},
		Ref:     "#/paths/~1widgets~1{id}/get",
		Profile: FullProfile(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 100; attempt++ {
		execution, err := prepared.Start(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !execution.InputRequested() {
			t.Fatalf("attempt %d: required input was not requested before Start returned", attempt)
		}
		if err := execution.FinishInput(); err != nil {
			t.Fatal(err)
		}
		for range execution.Events() {
			t.Fatal("missing-input refusal emitted an output")
		}
		err = execution.Wait()
		var executionErr *ExecutionError
		// A required-input refusal is pre-dispatch: it carries the
		// never-dispatched guarantee (ERR_REFUSED, ruled 2026-08-14).
		if !errors.As(err, &executionErr) || executionErr.Code != CodeRefused {
			t.Fatalf("attempt %d: terminal error = %#v", attempt, err)
		}
	}
}

func TestEnginePrepareRefusesInvalidConfiguredRequestMedia(t *testing.T) {
	engine := NewEngine(nil)
	_, err := engine.Prepare(context.Background(), PrepareOptions{
		Source: Source{Content: testDocument("https://api.example.test", `{
			"/widgets":{"post":{
				"requestBody":{"required":true,"content":{"application/*":{"schema":{"type":"object"}}}},
				"responses":{"204":{"description":"done"}}
			}}
		}`)},
		Ref:     "#/paths/~1widgets/post",
		Profile: MediaProfile(),
		Context: map[string]any{"configuration": map[string]any{"requestMedia": ""}},
	})
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != CodeSourceConfigError {
		t.Fatalf("prepare error = %#v", err)
	}
}

func TestEnginePrepareUsesWholePointPathForRequestMedia(t *testing.T) {
	engine := NewEngine(nil)
	prepared, err := engine.Prepare(context.Background(), PrepareOptions{
		Source: Source{Content: testDocument("https://api.example.test", `{
			"/widgets":{"post":{
				"requestBody":{"required":true,"content":{"application/*":{"schema":{"type":"object"}}}},
				"responses":{"204":{"description":"done"}}
			}}
		}`)},
		Ref:     "#/paths/~1widgets/post",
		Profile: MediaProfile(),
	})
	if err != nil {
		t.Fatal(err)
	}
	details := prepared.Prerequisites()
	if details == nil || len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 {
		t.Fatalf("prerequisites = %#v", details)
	}
	requirement := details.Alternatives[0].Requirements[0]
	if requirement.Type != "config.value" || requirement.Extra["point"] != "requestMedia" || requirement.Extra["path"] != "" {
		t.Fatalf("requestMedia requirement = %#v", requirement)
	}
}

func TestExactSupportedVersions(t *testing.T) {
	accepted := []string{"3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2"}
	for _, version := range accepted {
		document := []byte(fmt.Sprintf(`{"openapi":%q,"info":{"title":"t","version":"1"},"paths":{}}`, version))
		if _, err := Load(context.Background(), Source{Content: document}, ClientOptions{}); err != nil {
			t.Fatalf("%s: %v", version, err)
		}
	}
	for _, version := range []string{"3.0.5", "3.1.3", "3.2.0", "2.0"} {
		document := []byte(fmt.Sprintf(`{"openapi":%q,"info":{"title":"t","version":"1"},"paths":{}}`, version))
		if _, err := Load(context.Background(), Source{Content: document}, ClientOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported OpenAPI version") {
			t.Fatalf("%s: %v", version, err)
		}
	}
}

func TestLoaderUsesFinalRetrievalURIForRedirectedExternalRefs(t *testing.T) {
	requested := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requested = append(requested, request.URL.Path)
		switch request.URL.Path {
		case "/entry":
			http.Redirect(writer, request, "/root/openapi.json", http.StatusFound)
		case "/root/openapi.json":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{"openapi":"3.1.0","info":{"title":"redirect","version":"1"},"servers":[{"url":%q}],"paths":{"/thing":{"$ref":"parts/path.json"}}}`, serverURL(request))
		case "/root/parts/path.json":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"get":{"operationId":"redirected","responses":{"204":{"description":"ok"}}}}`))
		case "/thing":
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := Load(context.Background(), Source{Location: server.URL + "/entry"}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), OperationID("redirected"), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("result = %#v", result)
	}
	want := []string{"/entry", "/root/openapi.json", "/root/parts/path.json", "/thing"}
	if !reflect.DeepEqual(requested, want) {
		t.Fatalf("requests = %#v, want %#v", requested, want)
	}
}

func serverURL(request *http.Request) string {
	return "http://" + request.Host
}

func TestEmbeddedRelativeExternalRefRequiresLocation(t *testing.T) {
	document := []byte(`{"openapi":"3.1.0","info":{"title":"relative","version":"1"},"paths":{"/thing":{"$ref":"parts/path.json"}}}`)
	_, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err == nil || !strings.Contains(err.Error(), "self-contained") {
		t.Fatalf("error = %v", err)
	}
}

// A multi-alternative effective server list without a selection challenges
// CONTEXT_REQUIRED (config.value, point server) instead of refusing
// terminally (openbindings.openapi §9.3, ruled 2026-08-13).
func TestEngineMultiServerWithoutSelectionChallengesContextRequired(t *testing.T) {
	document := []byte(`{
  "openapi":"3.1.0",
  "info":{"title":"test","version":"1"},
  "servers":[{"url":"https://a.example.test"},{"url":"https://b.example.test"}],
  "paths":{"/things":{"get":{"responses":{"204":{"description":"ok"}}}}}
}`)
	engine := NewEngine(nil)
	prepared, err := engine.Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: document},
		Ref:     "#/paths/~1things/get",
		Profile: FullProfile(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepared.Start(context.Background())
	if err == nil {
		t.Fatal("an unselected multi-server list must challenge before dispatch")
	}
	var terminal *ExecutionError
	if !errors.As(err, &terminal) {
		t.Fatalf("terminal = %#v", err)
	}
	if terminal.Code != CodeContextRequired {
		t.Fatalf("code = %q, want %q (details: %#v)", terminal.Code, CodeContextRequired, terminal.Details)
	}
	prereq, ok := terminal.Details.(*Prerequisites)
	if !ok || len(prereq.Alternatives) == 0 {
		t.Fatalf("details = %#v, want prerequisites with alternatives", terminal.Details)
	}
	requirement := prereq.Alternatives[0].Requirements[0]
	if requirement.Type != "config.value" || requirement.Extra["point"] != "server" {
		t.Fatalf("requirement = %#v, want config.value/server", requirement)
	}

	// Selecting a member resolves the challenge; dispatch proceeds.
	configured, err := NewEngine(nil).Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: document},
		Ref:     "#/paths/~1things/get",
		Profile: FullProfile(),
		Context: map[string]any{"configuration": map[string]any{"server": map[string]any{"index": 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configured.Start(context.Background()); err != nil {
		var challenge *ExecutionError
		if errors.As(err, &challenge) && challenge.Code == CodeContextRequired {
			t.Fatalf("configured selection still challenged: %#v", challenge.Details)
		}
		// Network failure is fine — the challenge is resolved and dispatch
		// was attempted against the configured target.
	}
}

// §9.2's string-carriage lane, ruled 2026-08-15 and scope-corrected the same
// day: a concrete non-JSON, non-form selection carries the caller's string as
// the body under the artifact's own header when its GOVERNING SCHEMA resolves
// to type: string AND its media type is character data. Both halves are
// derived — the OAS decides the value is a string, the media-type
// registration decides whether a string has an octet image — so the lane is
// never selected by the caller's value.
func TestClientCarriesStringDeclaredNonJSONBodies(t *testing.T) {
	for _, media := range []string{"text/csv", "text/x-markdown", "application/xml", "text/xml", "image/svg+xml"} {
		t.Run(media, func(t *testing.T) {
			var request struct{ contentType, body string }
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				request.contentType = incoming.Header.Get("Content-Type")
				body, _ := io.ReadAll(incoming.Body)
				request.body = string(body)
				writer.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			document := testDocument(server.URL, fmt.Sprintf(`{
  "/upload":{"put":{"operationId":"upload","requestBody":{"required":true,"content":{%q:{"schema":{"type":"string"}}}},
  "responses":{"204":{"description":"stored"}}}}
}`, media))
			client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := "a,b\n1,2\n"
			result, err := client.Call(context.Background(), OperationID("upload"), Input{Body: want, BodyPresent: true})
			if err != nil {
				t.Fatal(err)
			}
			if !result.OK {
				t.Fatalf("result = %#v", result)
			}
			if request.contentType != media || request.body != want {
				t.Fatalf("wire = %#v, want %q carrying the supplied string verbatim", request, media)
			}
		})
	}
}

// A media type whose registration establishes no character encoding has no
// defined string-to-octets mapping, so a `type: string` declaration on it is
// NOT string carriage: it keeps the artifact-authorized byte boundary. Under
// OAS 3.1 `format: binary` is an annotation with no assertion force, so an
// arrow-stream declaration resolves to a bare `type: string` and is decided
// entirely by its media type. An Arrow IPC stream is octets; a caller cannot
// supply them faithfully as characters.
func TestClientDoesNotStringCarryNonCharacterDataMedia(t *testing.T) {
	for _, schema := range []string{`{"type":"string"}`, `{"type":"string","format":"binary"}`} {
		t.Run(schema, func(t *testing.T) {
			// A local server so that a wrongly-admitted string lane would
			// dispatch successfully rather than failing on the network.
			dispatched := false
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				dispatched = true
				writer.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			document := testDocument(server.URL, fmt.Sprintf(`{
  "/write":{"put":{"operationId":"write","requestBody":{"required":true,"content":{"application/vnd.apache.arrow.stream":{"schema":%s}}},
  "responses":{"204":{"description":"stored"}}}}
}`, schema))
			client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Call(context.Background(), OperationID("write"), Input{Body: "arrow bytes", BodyPresent: true})
			if err == nil {
				t.Fatal("a string-declared binary media type must not gain the string-carriage lane")
			}
			if !strings.Contains(err.Error(), "selects a request carriage lane") {
				t.Fatalf("error = %v", err)
			}
			if dispatched {
				t.Fatal("refusal must happen before dispatch")
			}
		})
	}
}

// A schema that asserts nothing makes no claim the body is a string at all,
// so it is the same declaration as an omitted `schema` and takes the
// artifact-authorized byte lane — canonical Base64 in, exact octets out.
func TestClientCarriesUnconstrainedDeclarationsAtTheByteBoundary(t *testing.T) {
	for _, schema := range []string{"true", "{}"} {
		t.Run(schema, func(t *testing.T) {
			var request struct {
				contentType string
				body        []byte
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				request.contentType = incoming.Header.Get("Content-Type")
				request.body, _ = io.ReadAll(incoming.Body)
				writer.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			document := testDocument(server.URL, fmt.Sprintf(`{
  "/upload":{"put":{"operationId":"upload","requestBody":{"required":true,"content":{"text/plain":{"schema":%s}}},
  "responses":{"204":{"description":"stored"}}}}
}`, schema))
			client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			// The native surface takes the octets themselves and performs the
			// Base64 boundary encoding on the caller's behalf.
			result, err := client.Call(context.Background(), OperationID("upload"), Input{Body: []byte("a,b\n"), BodyPresent: true})
			if err != nil {
				t.Fatal(err)
			}
			if !result.OK || request.contentType != "text/plain" || string(request.body) != "a,b\n" {
				t.Fatalf("wire = %#v (ok=%v), want the exact octets under text/plain", request, result.OK)
			}
		})
	}
}

// No lane builds a document from an object model, so an object-declared
// selection outside the JSON and form lanes selects nothing — which is what
// keeps schema-guided XML generation out of this revision while string
// carriage of XML documents is admitted above.
func TestClientRefusesObjectDeclaredNonJSONBodies(t *testing.T) {
	for _, media := range []string{"application/xml", "text/xml", "text/json", "text/csv"} {
		t.Run(media, func(t *testing.T) {
			document := testDocument("https://api.example.test", fmt.Sprintf(`{
  "/upload":{"put":{"operationId":"upload","requestBody":{"required":true,"content":{%q:{"schema":{"type":"object","properties":{"a":{"type":"string"}}}}}},
  "responses":{"204":{"description":"stored"}}}}
}`, media))
			client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Call(context.Background(), OperationID("upload"), Input{Body: map[string]any{"a": "b"}, BodyPresent: true})
			if err == nil {
				t.Fatalf("object-declared %s must refuse before dispatch", media)
			}
			if !strings.Contains(err.Error(), "selects a request carriage lane") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
