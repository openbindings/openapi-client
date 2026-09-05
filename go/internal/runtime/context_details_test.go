package openapiclient

import (
	"context"
	"errors"
	"testing"
)

// The preflight's media requirements carry the same durability and prompt
// text the SDK adapter's own challenge carries, so one point reads the same
// on both surfaces and in both engines.
func TestPreflightMediaRequirementsAreDurableWithCanonicalText(t *testing.T) {
	engine := NewEngine(nil)
	cases := []struct {
		name, document, ref, point, path, description string
	}{
		{
			name: "requestMedia",
			document: `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],
			"paths":{"/p":{"post":{"requestBody":{"required":true,"content":{
			"application/json":{"schema":{"type":"string"}},"text/plain":{"schema":{"type":"string"}}}},
			"responses":{"204":{"description":"ok"}}}}}}`,
			ref: "#/paths/~1p/post", point: "requestMedia", path: "",
			description: "select the concrete request media type for this non-sole-concrete declaration",
		},
		{
			name: "propertyMedia",
			document: `{"openapi":"3.1.2","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],
			"paths":{"/upload":{"post":{"requestBody":{"required":true,"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"profile":{"type":"string"}}},
			"encoding":{"profile":{"contentType":"image/*"}}}}},
			"responses":{"204":{"description":"stored"}}}}}}`,
			ref: "#/paths/~1upload/post", point: "propertyMedia", path: "/profile",
			description: "select one concrete media type for this form or multipart property",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepared, err := engine.Prepare(context.Background(), PrepareOptions{
				Source:  Source{Content: []byte(tc.document)},
				Ref:     tc.ref,
				Profile: FullProfile(),
			})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			prerequisites := prepared.Prerequisites()
			if prerequisites == nil || len(prerequisites.Alternatives) != 1 || len(prerequisites.Alternatives[0].Requirements) != 1 {
				t.Fatalf("expected exactly one requirement, got %+v", prerequisites)
			}
			requirement := prerequisites.Alternatives[0].Requirements[0]
			if requirement.Type != "config.value" || requirement.Extra["point"] != tc.point || requirement.Extra["path"] != tc.path {
				t.Fatalf("expected config.value %s %q, got %+v", tc.point, tc.path, requirement)
			}
			if requirement.Durable == nil || !*requirement.Durable {
				t.Errorf("%s requirement must be durable: the choice is made once per binding and reused", tc.point)
			}
			if requirement.Description != tc.description {
				t.Errorf("description %q, want %q", requirement.Description, tc.description)
			}
		})
	}
}

// The server-list message names the configuration point the way the
// TypeScript engine and the Swagger 2.0 lane of both engines already do.
func TestServerListRequirementNamesTheConfigurationPoint(t *testing.T) {
	engine := NewEngine(nil)
	document := `{"openapi":"3.0.4","info":{"title":"t","version":"1"},
	"servers":[{"url":"https://a.example"},{"url":"https://b.example"}],
	"paths":{"/ping":{"get":{"responses":{"204":{"description":"ok"}}}}}}`
	prepared, err := engine.Prepare(context.Background(), PrepareOptions{
		Source:  Source{Content: []byte(document)},
		Ref:     "#/paths/~1ping/get",
		Profile: FullProfile(),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	_, err = prepared.Start(context.Background())
	var execErr *ExecutionError
	if !errors.As(err, &execErr) || execErr.Code != CodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED, got %v", err)
	}
	prerequisites, ok := execErr.Details.(*Prerequisites)
	if !ok || len(prerequisites.Alternatives) != 1 || len(prerequisites.Alternatives[0].Requirements) != 1 {
		t.Fatalf("expected one server requirement, got %+v", execErr.Details)
	}
	got := prerequisites.Alternatives[0].Requirements[0].Description
	want := "the effective server list has 2 alternatives; configuration.server must select one"
	if got != want {
		t.Errorf("description %q, want %q", got, want)
	}
}
