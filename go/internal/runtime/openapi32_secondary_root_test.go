package openapiclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSecondaryRootRequestMediaConfinement(t *testing.T) {
	transport := &openAPI32ResourceTransport{resources: map[string]string{"https://example.test/other": `{"name":"q","in":"query","required":true,"schema":{"type":"string"}}`}}
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`{"openapi":"3.2.0","info":{"title":"N2","version":"1"},"paths":{"/x":{"post":{"requestBody":{"required":true,"content":{"text/plain":{"schema":{"$ref":"https://example.test/other#/schema"}},"application/json":{"schema":{}}}},"responses":{"204":{"description":"ok"}}}},"/safe":{"get":{"responses":{"204":{"description":"ok"}}}}}}`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/post")
	if err != nil {
		t.Fatal(err)
	}
	content := target.Operation.RequestBody.Value.Content
	if len(content) != 1 || content["application/json"] == nil {
		t.Fatalf("media confinement: %#v", content)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1safe/get"); err != nil {
		t.Fatal(err)
	}
}

func TestSecondaryRootCanonicalSchemaScope(t *testing.T) {
	overlay := newOpenAPI32Overlay()
	retrieval, _ := url.Parse("https://example.test/other")
	if err := overlay.capture([]byte(`{"required":true,"$defs":{"Value":{"$id":"https://example.test/value","type":"string"}}}`), retrieval, retrieval, false); err != nil {
		t.Fatal(err)
	}
	resolved, _ := url.Parse("https://example.test/value")
	_, _, err := overlay.openAPI32ResponseSchemaTarget(resolved.String(), resolved, nil)
	var excluded *openAPI32RootExclusion
	if !errors.As(err, &excluded) {
		t.Fatalf("canonical scope bypassed document root: %v", err)
	}
}

func TestSecondaryRootOptionalEmptyBodyInvocation(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client := loadOpenAPI32ParameterClient(t, transport, `
  /x:
    post:
      requestBody:
        content:
          application/json:
            schema: {$schema: 'https://unsupported.example/dialect', type: string}
      responses: {'204': {description: ok}}
`)
	if _, err := client.Call(context.Background(), PathOperation("/x", POST), Input{}); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 1 || transport.requests[0].Header.Get("Content-Type") != "" || len(transport.bodies[0]) != 0 {
		t.Fatal("omitted optional body emitted content")
	}
	if _, err := client.Call(context.Background(), PathOperation("/x", POST), Input{BodyPresent: true, Body: "value"}); err == nil {
		t.Fatal("supplied unavailable body succeeded")
	}
	if len(transport.requests) != 1 {
		t.Fatal("unavailable body dispatched")
	}
}

func TestRequestMediaFallbackHydratesExternalEncodingHeader(t *testing.T) {
	// An unrelated malformed operation forces private-image loading. The selected
	// body/header/schema chain has not been populated by that image's typed loader.
	resources := &openAPI32ResourceTransport{resources: map[string]string{
		"https://example.test/body":   `{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"x":{"type":"string"}}},"encoding":{"x":{"headers":{"X-Fixed":{"$ref":"https://example.test/header"}}}}}}}`,
		"https://example.test/header": `{"schema":{"$ref":"https://example.test/schema"}}`,
		"https://example.test/schema": `{"type":"string","const":"retained"}`,
	}}
	wire := &openAPI32OperationTransport{}
	client, err := Load(context.Background(), Source{Content: []byte(`{"openapi":"3.2.0","info":{"title":"hydrate","version":"1"},"servers":[{"url":"https://api.example"}],"paths":{"/x":{"post":{"requestBody":{"$ref":"https://example.test/body"},"responses":{"204":{"description":"ok"}}}},"/broken":{"get":{"responses":1}}}}`)}, ClientOptions{HTTPClient: &http.Client{Transport: wire}, LoadHTTPClient: &http.Client{Transport: resources}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Call(context.Background(), PathOperation("/x", POST), Input{BodyPresent: true, Body: map[string]any{"x": "value"}}); err != nil {
		t.Fatal(err)
	}
	if len(wire.bodies) != 1 || !strings.Contains(string(wire.bodies[0]), "X-Fixed: retained\r\n") {
		t.Fatalf("external header lost: %q", wire.bodies)
	}
}

func TestSecondaryRootIdentityClassification(t *testing.T) {
	for _, tc := range []struct {
		name, root, fragment string
		kind                 OperationResolutionKind
	}{
		{"other", `{"name":"q","in":"query","required":true,"schema":{"type":"string"}}`, "", OperationTargetExcluded},
		{"alias", `{"name":"q","in":"query","required":true,"schema":{"type":"string"},"$self":"https://example.test/canonical"}`, "", OperationTargetInvalid},
		{"wrong target", `[1]`, "#/0", OperationTargetInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &openAPI32ResourceTransport{resources: map[string]string{"https://example.test/other": tc.root}}
			artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`{"openapi":"3.2.0","info":{"title":"N2","version":"1"},"paths":{"/x":{"get":{"parameters":[{"$ref":"https://example.test/other` + tc.fragment + `"}],"responses":{"204":{"description":"ok"}}}},"/safe":{"get":{"responses":{"204":{"description":"ok"}}}}}}`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = artifact.ResolveOperation("#/paths/~1x/get")
			var resolution *OperationResolutionError
			if !errors.As(err, &resolution) || resolution.Kind != tc.kind {
				t.Fatalf("got %v; want %s", err, tc.kind)
			}
			if _, err = artifact.ResolveOperation("#/paths/~1safe/get"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
