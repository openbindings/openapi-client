package openapiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// String content parses as YAML 1.2.2, whose core tag resolution (§10.3.2)
// resolves the null/bool/int/float patterns and makes every other plain
// scalar a string. The restriction is the artifact authority's: every
// accepted OAS edition requires that "Tags MUST be limited to those allowed
// by [YAML's] JSON schema ruleset" (§4.2), and YAML 1.1's timestamp tag is
// outside that set. The bare-date and space-separated spellings are the
// sensitive ones — a timestamp resolution does not even round-trip their
// text. The TypeScript peer pins the same table in
// typescript/src/util.test.ts.
func TestLoadDocument_YAMLCoreScalarResolution(t *testing.T) {
	cases := map[string]any{
		"2020-01-01T12:00:00Z": "2020-01-01T12:00:00Z",
		"2020-01-01":           "2020-01-01",
		"2020-01-01 12:00:00":  "2020-01-01 12:00:00",
		"12:30:45":             "12:30:45",
		"yes":                  "yes",
		"~":                    nil,
		"0o17":                 float64(15),
		"0x1F":                 float64(31),
		"+12.3":                12.3,
	}
	for spelling, want := range cases {
		t.Run(spelling, func(t *testing.T) {
			document, _, err := loadDocument(context.Background(), nil, Source{
				Location: "https://scalars.example/openapi.yaml",
				Content:  []byte(yamlScalarDocument(spelling)),
			}, false)
			if err != nil {
				t.Fatalf("load %s: %v", spelling, err)
			}
			operation := document.Paths.Find("/probe").Get
			got := operation.Parameters[0].Value.Schema.Value.Example
			if !jsonEqual(t, got, want) {
				t.Fatalf("example %s decoded as %#v, want %#v", spelling, got, want)
			}
		})
	}
}

// ±.inf and .nan resolve to floats JSON cannot spell, and the OpenAPI
// document model is JSON: the artifact is refused rather than silently
// carrying a value with no JSON image. The TypeScript peer refuses the same
// documents at its own parse boundary.
func TestLoadDocument_YAMLScalarWithoutJSONImageRefuses(t *testing.T) {
	for _, spelling := range []string{".inf", "-.inf", ".nan"} {
		t.Run(spelling, func(t *testing.T) {
			if _, _, err := loadDocument(context.Background(), nil, Source{
				Location: "https://scalars.example/openapi.yaml",
				Content:  []byte(yamlScalarDocument(spelling)),
			}, false); err == nil {
				t.Fatalf("expected refusal for example %s", spelling)
			}
		})
	}
}

func yamlScalarDocument(spelling string) string {
	return fmt.Sprintf(`openapi: 3.0.0
info:
  title: scalars
  version: 1.0.0
servers:
  - url: https://scalars.example
paths:
  /probe:
    get:
      operationId: probe
      parameters:
        - name: value
          in: query
          schema:
            type: string
            example: %s
      responses:
        "200":
          description: ok
`, spelling)
}

func jsonEqual(t *testing.T, got, want any) bool {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal decoded example: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal expected example: %v", err)
	}
	return string(gotJSON) == string(wantJSON)
}
