package openapiclient

import (
	"context"
	"net/http"
	"testing"
)

func TestSwagger20TargetSchemeSelectionAndSecurityCarriage(t *testing.T) {
	transport := &swagger20MediaRoundTripper{}
	serverIndex := 1
	securityIndex := 1
	prepared, err := NewEngine(nil).PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
		Source: Swagger20Source{Content: []byte(`{
  "swagger":"2.0","info":{"title":"target and security","version":"1"},
  "schemes":["http","https"],"host":"api.example","basePath":"/v1",
  "securityDefinitions":{
    "bad":{"type":"apiKey","in":"query","name":"id"},
    "good":{"type":"apiKey","in":"header","name":"X-Good"}},
  "security":[{"bad":[]},{"good":[]}],
  "paths":{"/x":{"get":{"parameters":[{"name":"id","in":"query","type":"string"}],
  "responses":{"204":{"description":"ok"}}}}}
}`)},
		Ref:                 "#/paths/~1x/get",
		ServerSchemeIndex:   &serverIndex,
		SecurityAlternative: &securityIndex,
		SecurityCredentials: Swagger20SecurityCredentials{APIKeys: map[string]string{"bad": "bad", "good": "good"}},
		HTTPClient:          &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := prepared.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Send(context.Background(), Swagger20Input{Parameters: Swagger20Parameters{Query: map[string]any{"id": "42"}}}); err != nil {
		t.Fatal(err)
	}
	if err := execution.FinishInput(); err != nil {
		t.Fatal(err)
	}
	for range execution.Events() {
	}
	if err := execution.Wait(); err != nil {
		t.Fatal(err)
	}
	if got, want := transport.request.URL.String(), "https://api.example/v1/x?id=42"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	if got := transport.request.Header.Get("X-Good"); got != "good" {
		t.Fatalf("X-Good = %q, want good", got)
	}
}

func TestSwagger20RetrievalDefaultsPreservePortAndSlash(t *testing.T) {
	document, err := loadSwagger20Document(context.Background(), nil, Swagger20Source{
		Location: "https://docs.example:8443/swagger.json",
		Content:  []byte(`{"swagger":"2.0","info":{"title":"defaults","version":"1"},"paths":{"/x":{"get":{"responses":{"204":{"description":"ok"}}}}}}`),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	operation, _, err := resolveSwagger20Operation(document, "#/paths/~1x/get")
	if err != nil {
		t.Fatal(err)
	}
	base, err := resolveSwagger20Server(document, operation, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := base, "https://docs.example:8443/"; got != want {
		t.Fatalf("base = %q, want %q", got, want)
	}
}
