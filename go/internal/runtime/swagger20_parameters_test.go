package openapiclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type swagger20CaptureTransport struct {
	requests []*http.Request
}

func (t *swagger20CaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, request)
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}

func prepareSwagger20TestOperation(t *testing.T, document string, transport *swagger20CaptureTransport, configure func(*Swagger20PrepareOptions)) *Swagger20PreparedOperation {
	t.Helper()
	options := Swagger20PrepareOptions{
		Source: Swagger20Source{Content: []byte(document)},
		Ref:    "#/paths/~1pets~1{id}/get",
		Server: "https://peer.example/root",
		HTTPClient: &http.Client{
			Transport: transport,
		},
	}
	if configure != nil {
		configure(&options)
	}
	prepared, err := NewEngine(options.HTTPClient).PrepareSwagger20(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func runSwagger20TestInput(t *testing.T, prepared *Swagger20PreparedOperation, input Swagger20Input) error {
	t.Helper()
	execution, err := prepared.Start(context.Background())
	if err != nil {
		return err
	}
	if execution.InputRequested() {
		if err := execution.Send(context.Background(), input); err != nil {
			return err
		}
		_ = execution.FinishInput()
	}
	for range execution.Events() {
	}
	return execution.Wait()
}

func TestSwagger20QualifiedParametersAndWireSerialization(t *testing.T) {
	transport := &swagger20CaptureTransport{}
	prepared := prepareSwagger20TestOperation(t, `{
  "swagger":"2.0","info":{"title":"parameters","version":"1"},
  "paths":{"/pets/{id}":{
    "parameters":[{"name":"id","in":"path","required":true,"type":"string"}],
    "get":{"parameters":[
      {"name":"id","in":"query","type":"string"},
      {"name":"tags","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"multi"},
      {"name":"modes","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"pipes"},
      {"name":"X-Enabled","in":"header","type":"boolean"}
    ],"responses":{"204":{"description":"ok"}}}
  }}
}`, transport, func(options *Swagger20PrepareOptions) {
		options.ParameterConverter = func(value any) (string, error) {
			if value == true {
				return "yes", nil
			}
			return "", errors.New("unconfigured value")
		}
	})

	infos, err := prepared.Parameters()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 5 || infos[0].Name != "id" || infos[0].In != Swagger20ParameterPath {
		t.Fatalf("effective parameters = %#v", infos)
	}
	err = runSwagger20TestInput(t, prepared, Swagger20Input{Parameters: Swagger20Parameters{
		Path: map[string]any{"id": "a/b"},
		Query: map[string]any{
			"id": "two words", "tags": []any{"x/y", "z"}, "modes": []any{"fast", "safe"},
		},
		Header: map[string]any{"X-Enabled": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(transport.requests))
	}
	request := transport.requests[0]
	if got, want := request.URL.String(), "https://peer.example/root/pets/a%2Fb?id=two%20words&modes=fast|safe&tags=x%2Fy&tags=z"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	if got := request.Header.Get("X-Enabled"); got != "yes" {
		t.Fatalf("X-Enabled = %q, want yes", got)
	}
	if got := request.Header.Get("Accept"); got != "" {
		t.Fatalf("implicit Accept = %q", got)
	}
}

func TestSwagger20EmptyValueForms(t *testing.T) {
	for _, testCase := range []struct {
		name string
		form Swagger20EmptyValueForm
		want string
	}{
		{name: "name only", form: Swagger20EmptyValueNameOnly, want: "https://peer.example/root/pets/x?flag"},
		{name: "present empty", form: Swagger20EmptyValueEmpty, want: "https://peer.example/root/pets/x?flag="},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &swagger20CaptureTransport{}
			prepared := prepareSwagger20TestOperation(t, `{
  "swagger":"2.0","info":{"title":"empty","version":"1"},
  "paths":{"/pets/{id}":{"get":{"parameters":[
    {"name":"id","in":"path","required":true,"type":"string"},
    {"name":"flag","in":"query","type":"string","allowEmptyValue":true}
  ],"responses":{"204":{"description":"ok"}}}}}
}`, transport, func(options *Swagger20PrepareOptions) { options.EmptyValueForm = testCase.form })
			if err := runSwagger20TestInput(t, prepared, Swagger20Input{Parameters: Swagger20Parameters{
				Path: map[string]any{"id": "x"}, Query: map[string]any{"flag": ""},
			}}); err != nil {
				t.Fatal(err)
			}
			if got := transport.requests[0].URL.String(); got != testCase.want {
				t.Fatalf("URL = %q, want %q", got, testCase.want)
			}
		})
	}
}

// A supplied array with ZERO members is an empty value under every
// collectionFormat, `multi` included -- not an omission -- so it reaches the
// same emptyValueForm choice a supplied empty string does.
func TestSwagger20ZeroMemberArrayIsAnEmptyValue(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		collectionFormat string
		form             Swagger20EmptyValueForm
		want             string
	}{
		{name: "csv name-only", collectionFormat: "csv", form: Swagger20EmptyValueNameOnly, want: "https://peer.example/root/pets/x?tags"},
		{name: "csv empty", collectionFormat: "csv", form: Swagger20EmptyValueEmpty, want: "https://peer.example/root/pets/x?tags="},
		{name: "multi name-only", collectionFormat: "multi", form: Swagger20EmptyValueNameOnly, want: "https://peer.example/root/pets/x?tags"},
		{name: "multi empty", collectionFormat: "multi", form: Swagger20EmptyValueEmpty, want: "https://peer.example/root/pets/x?tags="},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &swagger20CaptureTransport{}
			prepared := prepareSwagger20TestOperation(t, `{
  "swagger":"2.0","info":{"title":"empty array","version":"1"},
  "paths":{"/pets/{id}":{"get":{"parameters":[
    {"name":"id","in":"path","required":true,"type":"string"},
    {"name":"tags","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"`+testCase.collectionFormat+`","allowEmptyValue":true}
  ],"responses":{"204":{"description":"ok"}}}}}
}`, transport, func(options *Swagger20PrepareOptions) { options.EmptyValueForm = testCase.form })
			if err := runSwagger20TestInput(t, prepared, Swagger20Input{Parameters: Swagger20Parameters{
				Path: map[string]any{"id": "x"}, Query: map[string]any{"tags": []any{}},
			}}); err != nil {
				t.Fatal(err)
			}
			if got := transport.requests[0].URL.String(); got != testCase.want {
				t.Fatalf("URL = %q, want %q", got, testCase.want)
			}
		})
	}

	// Without allowEmptyValue the same supplied value refuses before dispatch
	// rather than vanishing.
	transport := &swagger20CaptureTransport{}
	prepared := prepareSwagger20TestOperation(t, `{
  "swagger":"2.0","info":{"title":"empty array","version":"1"},
  "paths":{"/pets/{id}":{"get":{"parameters":[
    {"name":"id","in":"path","required":true,"type":"string"},
    {"name":"tags","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"csv"}
  ],"responses":{"204":{"description":"ok"}}}}}
}`, transport, nil)
	err := runSwagger20TestInput(t, prepared, Swagger20Input{Parameters: Swagger20Parameters{
		Path: map[string]any{"id": "x"}, Query: map[string]any{"tags": []any{}},
	}})
	if err == nil || !strings.Contains(err.Error(), "empty value") {
		t.Fatalf("err = %v, want an empty-value refusal", err)
	}
	if len(transport.requests) != 0 {
		t.Fatalf("dispatched %d requests, want 0", len(transport.requests))
	}
}

func TestSwagger20ValueRefusalsPrecedeDispatch(t *testing.T) {
	document := `{
  "swagger":"2.0","info":{"title":"refusal","version":"1"},
  "paths":{"/pets/{id}":{"get":{"parameters":[
    {"name":"id","in":"path","required":true,"type":"string"},
    {"name":"count","in":"query","type":"integer","minimum":2},
    {"name":"labels","in":"query","type":"array","items":{"type":"string"}},
    {"name":"note","in":"header","type":"string"},
    {"name":"empty","in":"query","type":"string","allowEmptyValue":true}
  ],"responses":{"204":{"description":"ok"}}}}}
}`
	// openbindings.openapi-2.0@1 §3.2 gives an unusable target's pre-dispatch
	// refusal two species. Both refuse before dispatch and neither has an
	// observable side effect -- what this test is named for, and what every row
	// still asserts. `code` records which species each condition carries: the
	// context-required one where a named §12.1 point is awaited and the refusal
	// names it, the plain one where no supplied context could change the answer.
	// `parameterConversion` is deliberately plain: the converter is a runtime
	// capability, not a value the invocation context can carry, so naming it
	// would emit a satisfiable-looking challenge no runtime could satisfy
	// (binding-invoker, "Requirement types").
	for _, testCase := range []struct {
		name      string
		parameter Swagger20Parameters
		configure func(*Swagger20PrepareOptions)
		code      string
		point     string
	}{
		{name: "literal integer exponent", code: CodeRefused, parameter: Swagger20Parameters{Path: map[string]any{"id": "x"}, Query: map[string]any{"count": json.Number("2e0")}}},
		{name: "assertion", code: CodeRefused, parameter: Swagger20Parameters{Path: map[string]any{"id": "x"}, Query: map[string]any{"count": json.Number("1")}}},
		{name: "delimiter collision", code: CodeRefused, parameter: Swagger20Parameters{Path: map[string]any{"id": "x"}, Query: map[string]any{"labels": []any{"a,b"}}}},
		{name: "header CRLF", code: CodeRefused, parameter: Swagger20Parameters{Path: map[string]any{"id": "x"}, Header: map[string]any{"note": "ok\r\nbad"}}},
		{name: "empty choice absent", code: CodeContextRequired, point: "emptyValueForm", parameter: Swagger20Parameters{Path: map[string]any{"id": "x"}, Query: map[string]any{"empty": ""}}},
		{name: "unknown native key", code: CodeRefused, parameter: Swagger20Parameters{Path: map[string]any{"id": "x"}, Query: map[string]any{"other": "x"}}},
		{name: "number conversion absent", code: CodeRefused, parameter: Swagger20Parameters{Path: map[string]any{"id": "x"}, Query: map[string]any{"count": json.Number("2")}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &swagger20CaptureTransport{}
			prepared := prepareSwagger20TestOperation(t, document, transport, testCase.configure)
			err := runSwagger20TestInput(t, prepared, Swagger20Input{Parameters: testCase.parameter})
			var executionError *ExecutionError
			if !errors.As(err, &executionError) || executionError.Code != testCase.code {
				t.Fatalf("error = %#v, want %s", err, testCase.code)
			}
			if testCase.point != "" {
				prerequisites, ok := executionError.Details.(*Prerequisites)
				if !ok || len(prerequisites.Alternatives) != 1 || len(prerequisites.Alternatives[0].Requirements) != 1 {
					t.Fatalf("details = %#v, want one requirement", executionError.Details)
				}
				requirement := prerequisites.Alternatives[0].Requirements[0]
				if requirement.Type != "config.value" || requirement.Extra["point"] != testCase.point {
					t.Fatalf("requirement = %#v, want config.value at %q", requirement, testCase.point)
				}
			}
			if len(transport.requests) != 0 {
				t.Fatalf("dispatched %d requests", len(transport.requests))
			}
		})
	}
}

func TestSwagger20DeclarationDefectsExcludeSelectedOperation(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		path       string
		parameters string
	}{
		{name: "body and formData", path: "/pets", parameters: `[{"name":"payload","in":"body","schema":{}},{"name":"name","in":"formData","type":"string"}]`},
		{name: "duplicate identity", path: "/pets", parameters: `[{"name":"q","in":"query","type":"string"},{"name":"q","in":"query","type":"string"}]`},
		{name: "case-only headers", path: "/pets", parameters: `[{"name":"X-ID","in":"header","type":"string"},{"name":"x-id","in":"header","type":"string"}]`},
		{name: "path template mismatch", path: "/pets/{id}", parameters: `[{"name":"other","in":"path","required":true,"type":"string"}]`},
		{name: "multi header", path: "/pets", parameters: `[{"name":"X-IDs","in":"header","type":"array","items":{"type":"string"},"collectionFormat":"multi"}]`},
		{name: "owned header", path: "/pets", parameters: `[{"name":"Content-Type","in":"header","type":"string"}]`},
		{name: "invalid default", path: "/pets", parameters: `[{"name":"limit","in":"query","type":"integer","default":"ten"}]`},
		{name: "nested array", path: "/pets", parameters: `[{"name":"matrix","in":"query","type":"array","items":{"type":"array","items":{"type":"string"}}}]`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := `{"swagger":"2.0","info":{"title":"invalid","version":"1"},"paths":{"` + testCase.path + `":{"get":{"parameters":` + testCase.parameters + `,"responses":{"204":{"description":"ok"}}}}}}`
			selector := "#/paths/" + swagger20TestEscape(testCase.path) + "/get"
			prepared, err := NewEngine(nil).PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
				Source: Swagger20Source{Content: []byte(document)}, Ref: selector,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prepared.Parameters(); err == nil {
				t.Fatal("expected declaration refusal")
			}
		})
	}
}

func TestSwagger20ParameterReferencesUseDraft03ReplacementAndCyclePin(t *testing.T) {
	prepared, err := NewEngine(nil).PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
		Source: Swagger20Source{Content: []byte(`{
  "swagger":"2.0","info":{"title":"parameter ref","version":"1"},
  "parameters":{"Shared":{"name":"q","in":"query","type":"string"}},
  "paths":{"/pets":{"get":{"parameters":[
    {"$ref":"#/parameters/Shared","name":"ignored","in":"header","type":"integer"}
  ],"responses":{"204":{"description":"ok"}}}}}
}`)},
		Ref: "#/paths/~1pets/get",
	})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := prepared.Parameters()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "q" || infos[0].In != Swagger20ParameterQuery || infos[0].Type != "string" {
		t.Fatalf("parameters = %#v", infos)
	}

	cyclic, err := NewEngine(nil).PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
		Source: Swagger20Source{Content: []byte(`{
  "swagger":"2.0","info":{"title":"parameter cycle","version":"1"},
  "parameters":{"A":{"$ref":"#/parameters/B"},"B":{"$ref":"#/parameters/A"}},
  "paths":{"/pets":{"get":{"parameters":[{"$ref":"#/parameters/A"}],"responses":{"204":{"description":"ok"}}}}}
}`)},
		Ref: "#/paths/~1pets/get",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cyclic.Parameters(); err == nil {
		t.Fatal("expected selected Parameter reference cycle to refuse")
	}
}

func TestSwagger20CollectionDelimitersAndUTF8Encoding(t *testing.T) {
	for format, delimiter := range map[string]string{"csv": ",", "ssv": " ", "tsv": "\t", "pipes": "|"} {
		parameter := &swagger20Parameter{
			name: "values", typeName: "array", collectionFormat: format,
			items: &swagger20Items{typeName: "string"},
		}
		contributions, err := parameter.serialize([]any{"a", "é"}, nil, "")
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if got, want := contributions[0].value, "a"+delimiter+"é"; got != want {
			t.Fatalf("%s value = %q, want %q", format, got, want)
		}
		if got, want := swagger20EncodeContribution(contributions[0]), "a"+delimiter+"%C3%A9"; got != want {
			t.Fatalf("%s encoded = %q, want %q", format, got, want)
		}
	}
}

func swagger20TestEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
