package openapiclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type openAPI32RoundTripperFunc func(*http.Request) (*http.Response, error)

func (f openAPI32RoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestArtifactOperationsUsesOpenAPI32EditionInventory(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: inventory, version: "1"}
paths:
  /x:
    query: {operationId: queryX}
    additionalOperations:
      MiXeD: {operationId: mixedX}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	operations := artifact.Operations()
	if len(operations) != 2 || operations[0].Ref != "#/paths/~1x/query" || operations[1].Ref != "#/paths/~1x/additionalOperations/MiXeD" {
		t.Fatalf("Operations() = %#v", operations)
	}
}

func TestOpenAPI32OperationServerUsesDeclaringRetrievalLocationNotSelf(t *testing.T) {
	transport := openAPI32RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := `$self: https://identity.example/library/path-item.yaml
query:
  servers: [{url: ./api}]
additionalOperations:
  MiXeD:
    servers: [{url: ../custom}]
`
		retrieved := request.Clone(request.Context())
		retrieved.URL, _ = request.URL.Parse("https://retrieval.example/library/path-item.yaml")
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: retrieved}, nil
	})
	artifact, err := LoadArtifact(context.Background(), Source{
		Location: "https://retrieval.example/root.yaml",
		Content: []byte(`
openapi: 3.2.0
$self: https://identity.example/descriptions/root.yaml
info: {title: server origins, version: "1"}
paths:
  /x: {$ref: https://identity.example/library/path-item.yaml}
`),
	}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		ref  string
		want string
	}{
		{"#/paths/~1x/query", "https://retrieval.example/library/api"},
		{"#/paths/~1x/additionalOperations/MiXeD", "https://retrieval.example/custom"},
	} {
		target, resolveErr := artifact.ResolveOperation(test.ref)
		if resolveErr != nil {
			t.Fatalf("ResolveOperation(%q): %v", test.ref, resolveErr)
		}
		set, setErr := EffectiveServerSet(target.Document, target.PathItem, target.Operation, "https://retrieval.example/root.yaml")
		if setErr != nil {
			t.Fatalf("EffectiveServerSet(%q): %v", test.ref, setErr)
		}
		got, resolveServerErr := set.Resolve(nil)
		if resolveServerErr != nil || got != test.want {
			t.Errorf("server(%q) = %q, %v; want %q", test.ref, got, resolveServerErr, test.want)
		}
	}
}

type openAPI32SecurityTransport struct {
	requests []*http.Request
}

func (t *openAPI32SecurityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, request)
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}

func TestOpenAPI32SecurityURIIdentityAndMalformedAlternativeConfinement(t *testing.T) {
	const uriName = "https://identity.example/security.yaml#/components/securitySchemes/Key"
	transport := &openAPI32SecurityTransport{}
	// Supply the URI resource through a preloaded overlay alias by making the
	// entry's identity itself the URI document. The fragment then resolves to
	// the entry component while retaining the exact URI requirement name.
	artifact, err := LoadArtifact(context.Background(), Source{
		Location: "https://identity.example/security.yaml",
		Content: []byte(`
openapi: 3.2.0
info: {title: security URI, version: "1"}
servers: [{url: https://api.example}]
components:
  securitySchemes:
    Broken: {type: apiKey, in: nowhere}
    Key: {type: apiKey, in: header, name: X-URI-Key}
paths:
  /x:
    get:
      security:
        - Broken: []
        - 'https://identity.example/security.yaml#/components/securitySchemes/Key': [operator, auditor]
      responses: {'204': {description: ok}}
`),
	}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{
		artifact: artifact, document: artifact.Document,
		source:  Source{Location: "https://identity.example/security.yaml", Artifact: artifact},
		options: ClientOptions{HTTPClient: &http.Client{Transport: transport}},
	}
	result, err := client.Call(context.Background(), PathOperation("/x", GET), Input{}, CallOptions{
		Auth: map[string]any{uriName: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(transport.requests) != 1 || transport.requests[0].Header.Get("X-URI-Key") != "secret" {
		t.Fatalf("URI security dispatch = result %#v, requests %#v", result, transport.requests)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/get")
	if err != nil {
		t.Fatal(err)
	}
	ref := target.Document.Components.SecuritySchemes[uriName]
	if ref == nil || ref.Value == nil || ref.Value.Name != "X-URI-Key" {
		t.Fatalf("URI scheme was not materialized under exact identity: %#v", ref)
	}
	plans := securityPlans(target.Document, target.Operation, "")
	if len(plans) != 1 || len(plans[0].context.Requirements) != 1 {
		t.Fatalf("URI security plans = %#v", plans)
	}
	roles, _ := plans[0].context.Requirements[0].Extra["roles"].([]string)
	if strings.Join(roles, ",") != "operator,auditor" {
		t.Fatalf("security roles = %#v", roles)
	}
}

func TestOpenAPI32RelativeSecurityURIIsLoadedAsAnArtifactResource(t *testing.T) {
	transport := &openAPI32ResourceTransport{resources: map[string]string{
		"https://identity.example/security.yaml": `Key: {type: apiKey, in: query, name: access_key}`,
	}}
	artifact, err := LoadArtifact(context.Background(), Source{
		Location: "https://identity.example/root.yaml",
		Content: []byte(`
openapi: 3.2.0
info: {title: relative security URI, version: "1"}
paths:
  /x:
    get:
      security: [{'./security.yaml#/Key': []}]
`),
	}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/get")
	if err != nil {
		t.Fatal(err)
	}
	ref := target.Document.Components.SecuritySchemes["./security.yaml#/Key"]
	if ref == nil || ref.Value == nil || ref.Value.Name != "access_key" {
		t.Fatalf("relative URI scheme = %#v", ref)
	}
}

func TestOpenAPI32SecurityURIReferenceClosureIsLoaded(t *testing.T) {
	transport := &openAPI32ResourceTransport{resources: map[string]string{
		"https://identity.example/security.yaml": `Key: {$ref: './actual.yaml#/Actual'}`,
		"https://identity.example/actual.yaml":   `Actual: {type: apiKey, in: header, name: X-Nested-Key}`,
	}}
	artifact, err := LoadArtifact(context.Background(), Source{
		Location: "https://identity.example/root.yaml",
		Content: []byte(`
openapi: 3.2.0
info: {title: security URI reference closure, version: "1"}
paths:
  /x:
    get:
      security: [{'./security.yaml#/Key': []}]
`),
	}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/get")
	if err != nil {
		t.Fatal(err)
	}
	ref := target.Document.Components.SecuritySchemes["./security.yaml#/Key"]
	if ref == nil || ref.Value == nil || ref.Value.Name != "X-Nested-Key" {
		t.Fatalf("nested URI security scheme = %#v", ref)
	}
}

func TestOpenAPI32ReferringComponentSecurityRemainsImplicitConnection(t *testing.T) {
	transport := &openAPI32ResourceTransport{resources: map[string]string{
		"https://identity.example/library.yaml": `
openapi: 3.2.0
info: {title: library, version: "1"}
components:
  securitySchemes:
    LocalKey: {type: apiKey, in: header, name: X-Local-Key}
paths:
  /remote:
    get:
      security: [{LocalKey: []}]
`,
	}}
	artifact, err := LoadArtifact(context.Background(), Source{
		Location: "https://identity.example/root.yaml",
		Content: []byte(`
openapi: 3.2.0
info: {title: implicit connection, version: "1"}
paths:
  /x: {$ref: './library.yaml#/paths/~1remote'}
`),
	}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/get")
	if err != nil {
		t.Fatal(err)
	}
	ref := target.ReferringSecuritySchemes["LocalKey"]
	if ref == nil || ref.Value == nil || ref.Value.Name != "X-Local-Key" {
		t.Fatalf("referring security scheme = %#v", ref)
	}
	if target.Document.Components != nil {
		if entryRef := target.Document.Components.SecuritySchemes["LocalKey"]; entryRef != nil {
			t.Fatalf("referring component leaked into entry scope: %#v", entryRef)
		}
	}
}
