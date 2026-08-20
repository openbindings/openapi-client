package openapiclient

import (
	"errors"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// config.value schema (2026-08-20 working-draft amendment): a config.value
// requirement MAY carry an engine-asserted `schema` (JSON Schema) for the
// value at (point, path); `choices` is removed everywhere. This engine emits
// enum-only schemas where the admissible set is already computed at the
// emission site, absent otherwise.

func TestMultiServerChallengeCarriesEnumSchema(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3", Servers: openapi3.Servers{
		{URL: "https://a.example.test"},
		{URL: "https://b.example.test"},
	}}
	_, err := resolveServer(doc, nil, nil, nil, "")
	var cr *configRequired
	if !errors.As(err, &cr) {
		t.Fatalf("expected *configRequired for an unselected multi-server list, got %v", err)
	}
	members, _ := cr.schema["enum"].([]any)
	if len(members) != 2 || members[0] != "https://a.example.test" || members[1] != "https://b.example.test" {
		t.Errorf("schema = %v, want an enum of the declared server URLs", cr.schema)
	}
}

func TestUndefaultedVariableChallengeSchemaFollowsDeclaredEnum(t *testing.T) {
	withEnum := &openapi3.T{OpenAPI: "3.0.3", Servers: openapi3.Servers{{
		URL:       "https://{env}.example.test",
		Variables: map[string]*openapi3.ServerVariable{"env": {Enum: []string{"prod", "staging"}}},
	}}}
	_, err := resolveServer(withEnum, nil, nil, nil, "")
	var cr *configRequired
	if !errors.As(err, &cr) {
		t.Fatalf("expected *configRequired for an undefaulted variable, got %v", err)
	}
	members, _ := cr.schema["enum"].([]any)
	if len(members) != 2 || members[0] != "prod" || members[1] != "staging" {
		t.Errorf("schema = %v, want the artifact-declared enum", cr.schema)
	}

	withoutEnum := &openapi3.T{OpenAPI: "3.0.3", Servers: openapi3.Servers{{
		URL:       "https://{env}.example.test",
		Variables: map[string]*openapi3.ServerVariable{"env": {}},
	}}}
	_, err = resolveServer(withoutEnum, nil, nil, nil, "")
	if !errors.As(err, &cr) {
		t.Fatalf("expected *configRequired, got %v", err)
	}
	if cr.schema != nil {
		t.Errorf("schema = %v, want absent when the artifact declares no enum", cr.schema)
	}
}

// Stage 0 scope assertion: the challenge target is the source location
// already threaded into resolveServer — the artifact-bound identity a point
// preceding destination resolution naturally scopes to. A content-only
// source has no location and asserts nothing.
func TestConfigOrSourceErrorAssertsThreadedTarget(t *testing.T) {
	signal := &configRequired{point: "server", path: "/url", description: "select one"}

	terminal := configOrSourceError(signal, "https://example.com/openapi.json")
	prereq, ok := terminal.Details.(*Prerequisites)
	if !ok || prereq.Target != "https://example.com/openapi.json" {
		t.Fatalf("details = %#v, want the threaded source location as target", terminal.Details)
	}
	requirement := prereq.Alternatives[0].Requirements[0]
	if _, present := requirement.Extra["choices"]; present {
		t.Error("choices is removed from the contract; nothing may emit it")
	}

	terminal = configOrSourceError(signal, "")
	if prereq, ok = terminal.Details.(*Prerequisites); !ok || prereq.Target != "" {
		t.Fatalf("details = %#v, want an empty target for a content-only source", terminal.Details)
	}
}

func TestConfigValueSatisfactionEnforcesEnumSchema(t *testing.T) {
	challenge := func(schema map[string]any) *Prerequisites {
		extra := map[string]any{"point": "server", "path": "/url"}
		if schema != nil {
			extra["schema"] = schema
		}
		return &Prerequisites{
			Target: "https://example.com/openapi.json",
			Alternatives: []RequirementAlternative{{Requirements: []Requirement{
				{Type: "config.value", Extra: extra},
			}}},
		}
	}
	stored := func(value any) map[string]any {
		return map[string]any{"configuration": map[string]any{
			"server": map[string]any{"url": value},
		}}
	}

	enum := map[string]any{"enum": []any{"https://a.example.test", "https://b.example.test"}}
	if !contextSatisfies(stored("https://a.example.test"), challenge(enum)) {
		t.Error("a value inside the closed enum satisfies")
	}
	if contextSatisfies(stored("https://c.example.test"), challenge(enum)) {
		t.Error("a value outside the closed enum must not satisfy")
	}
	if !contextSatisfies(stored("https://c.example.test"), challenge(nil)) {
		t.Error("without a schema, presence of a non-empty value satisfies (unconstrained)")
	}
	// Twin divergence by necessity (see requirementSatisfied): a non-enum
	// schema member is carried but not enforced here — presence still
	// satisfies.
	if !contextSatisfies(stored("anything"), challenge(map[string]any{"type": "string"})) {
		t.Error("a non-enum schema is not enforced by this engine's satisfaction check")
	}
}
