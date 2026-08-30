package openapiclient

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func jsonText(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// §9.2's strict-JSON profile, stated identically by all four sibling binding
// specifications. Corpus pins: OAPI31-PS-118 (duplicate names),
// OAPI31-PS-121 (leading byte-order mark), OAPI31-PS-122 (lone surrogate).
// This table covers the cases around those three that a portable scenario
// cannot reach: a byte-order mark that is not leading, well-formed surrogate
// pairs that must NOT be refused, and a backslash sequence that only looks
// like an escape.
func TestStrictJSONProfile(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		want    string
		refused string
	}{
		{name: "leading BOM ignored", body: "\ufeff{\"a\":1}", want: `{"a":1}`},
		{name: "no BOM", body: `{"a":1}`, want: `{"a":1}`},
		{
			name: "interior BOM stays in the string value",
			body: "{\"a\":\"x\ufeffy\"}",
			want: "{\"a\":\"x\ufeffy\"}",
		},
		{name: "duplicate names take the last member", body: `{"x":1,"x":2}`, want: `{"x":2}`},
		{name: "well-formed surrogate pair", body: `{"e":"😀"}`, want: `{"e":"😀"}`},
		{name: "non-surrogate escape", body: `{"e":"é"}`, want: `{"e":"é"}`},
		{
			name: "escaped backslash before u is not an escape",
			body: `{"e":"\\ud800"}`,
			want: `{"e":"\\ud800"}`,
		},
		{name: "lone high surrogate", body: `{"e":"\ud800"}`, refused: `\uD800`},
		{name: "lone low surrogate", body: `{"e":"\udc00"}`, refused: `\uDC00`},
		{
			name:    "high surrogate followed by a non-surrogate escape",
			body:    `{"e":"\ud800A"}`,
			refused: `\uD800`,
		},
		{name: "high surrogate at end of string", body: `{"e":"\ud83d"}`, refused: `\uD83D`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value any
			err := parseStrictJSON([]byte(test.body), &value)
			if test.refused != "" {
				if err == nil {
					t.Fatalf("parsed %s, want the lone-surrogate protocol error", test.body)
				}
				if !strings.Contains(err.Error(), test.refused) {
					t.Fatalf("error = %v, want it to name %s", err, test.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := parseStrictJSON([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			if got, expected := jsonText(t, value), jsonText(t, want); got != expected {
				t.Fatalf("value = %s, want %s", got, expected)
			}
		})
	}
}

// The gate's PS-119 parity finding: an upstream-invalid Response Object is
// owned by the selected target, so it excludes that target and nothing else.
// The corpus scenario pins the refusal's phase; this pins the other half —
// an intact sibling operation in the same source still resolves.
func TestInvalidResponseObjectExcludesOnlyItsOwnTarget(t *testing.T) {
	document := []byte(`{
  "openapi":"3.1.2",
  "info":{"title":"invalid response object","version":"1"},
  "servers":[{"url":"https://api.example"}],
  "paths":{
    "/broken":{"get":{"responses":{"200":{"content":{"application/json":{"schema":{}}}}}}},
    "/intact":{"get":{"responses":{"204":{"description":"ok"}}}}
  }
}`)
	artifact, err := LoadArtifact(context.Background(), Source{Content: document}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatalf("whole source refused at load, want target-level confinement: %v", err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1intact/get"); err != nil {
		t.Fatalf("intact sibling operation: %v", err)
	}
}
