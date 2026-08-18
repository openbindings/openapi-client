package openapiclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// The style-lane composite-member case table is SHARED, byte-for-byte, with
// three other engines: openbindings-go/formats/openapi, openapi-client/
// typescript and openbindings-ts/packages/openapi. Each cell pins the
// ADMISSION decision all four must reach for one style-lane declaration, so a
// divergence in any one of them fails the others' suites.
//
// This engine ships no synthesizer, so it executes the cells through the
// shipped admission predicates themselves — styleLaneUndefinedExpansionParamFor
// for a parameter cell and planRequestBodiesFor for a body cell — and it is
// one of the two engines that additionally assert the MEMBER the predicate
// names, which the coverage-level assertion in the two synthesizing engines
// cannot see.
//
// Authority: styleLaneUndefinedExpansionMember in media.go reads the style
// table per edition. Package:
// design/openapi-style-lane-composite-member-ruling.md, RULED 2026-08-18.
const styleLaneCompositeMemberCasesDigest = "1ea1045c75039b00c1035a2e2c3d09e440644e32a5fa1c3689be6add1eac7673"

type styleLaneCompositeMemberCase struct {
	Name     string          `json:"name"`
	OpenAPI  string          `json:"openapi"`
	Position string          `json:"position"`
	In       string          `json:"in"`
	Style    string          `json:"style"`
	Explode  *bool           `json:"explode"`
	Media    string          `json:"media"`
	Encoding map[string]any  `json:"encoding"`
	Schema   json.RawMessage `json:"schema"`
	Expect   string          `json:"expect"`
	Member   string          `json:"member"`
	Basis    string          `json:"basis"`
}

func loadStyleLaneCompositeMemberCases(t *testing.T) []styleLaneCompositeMemberCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/style-lane-composite-member-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != styleLaneCompositeMemberCasesDigest {
		t.Fatalf("case table digest = %s, want %s (the table is shared byte-for-byte with three twin engines)", got, styleLaneCompositeMemberCasesDigest)
	}
	var table struct {
		Cases []styleLaneCompositeMemberCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table.Cases
}

// styleLaneCompositeMemberDocument renders one cell as a WHOLE OpenAPI
// document, byte-corresponding with the twin engines' renderer. The document,
// and not a hand-built model object, is what the engine has to be given: the
// shipped loader normalizes the raw tree before the typed model exists.
func styleLaneCompositeMemberDocument(t *testing.T, c styleLaneCompositeMemberCase) []byte {
	t.Helper()
	var schema any
	if len(c.Schema) > 0 && string(c.Schema) != "null" {
		if err := json.Unmarshal(c.Schema, &schema); err != nil {
			t.Fatalf("%s: parse cell schema: %v", c.Name, err)
		}
	}

	var paths map[string]any
	if c.Position == "parameter" {
		parameter := map[string]any{"name": "filter", "in": c.In}
		if c.Style != "" {
			parameter["style"] = c.Style
		}
		if c.Explode != nil {
			parameter["explode"] = *c.Explode
		}
		if schema != nil {
			parameter["schema"] = schema
		} else {
			parameter["content"] = map[string]any{"application/json": map[string]any{
				"schema": map[string]any{"type": "object", "properties": map[string]any{"where": map[string]any{"type": "object"}}},
			}}
		}
		template := "/q"
		if c.In == "path" {
			template = "/q/{filter}"
			parameter["required"] = true
		}
		paths = map[string]any{template: map[string]any{"get": map[string]any{
			"operationId": "query",
			"parameters":  []any{parameter},
			"responses":   map[string]any{"200": map[string]any{"description": "ok"}},
		}}}
	} else {
		media := map[string]any{"schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"field": schema},
		}}
		if c.Encoding != nil {
			media["encoding"] = map[string]any{"field": c.Encoding}
		}
		paths = map[string]any{"/form": map[string]any{"post": map[string]any{
			"operationId": "postForm",
			"requestBody": map[string]any{"content": map[string]any{c.Media: media}},
			"responses":   map[string]any{"200": map[string]any{"description": "ok"}},
		}}}
	}

	encoded, err := json.Marshal(map[string]any{
		"openapi": c.OpenAPI,
		"info":    map[string]any{"title": "style lane composite member case table", "version": "1.0.0"},
		"servers": []any{map[string]any{"url": "https://api.example.test"}},
		"paths":   paths,
	})
	if err != nil {
		t.Fatalf("%s: marshal document: %v", c.Name, err)
	}
	return encoded
}

func TestStyleLaneCompositeMemberCaseTable(t *testing.T) {
	for _, testCase := range loadStyleLaneCompositeMemberCases(t) {
		t.Run(testCase.Name, func(t *testing.T) {
			doc, err := loadDocument(context.Background(), nil, Source{Content: styleLaneCompositeMemberDocument(t, testCase)}, false)
			if err != nil {
				t.Fatalf("load document: %v", err)
			}
			is30 := isOpenAPI30(majorMinor(doc.OpenAPI))

			if testCase.Position == "parameter" {
				template := "/q"
				if testCase.In == "path" {
					template = "/q/{filter}"
				}
				item := doc.Paths.Find(template)
				if item == nil || item.Get == nil {
					t.Fatalf("loaded document has no query operation")
				}
				member := styleLaneUndefinedExpansionParamFor(effectiveParameters(item, item.Get), profileFullCoordinate, is30)
				decision := "admitted"
				if member != "" {
					decision = "refused"
				}
				if decision != testCase.Expect {
					t.Fatalf("decision = %s (member %q), want %s", decision, member, testCase.Expect)
				}
				if member != testCase.Member {
					t.Fatalf("member = %q, want %q", member, testCase.Member)
				}
				return
			}

			item := doc.Paths.Find("/form")
			if item == nil || item.Post == nil {
				t.Fatalf("loaded document has no form operation")
			}
			plans, planErr := planRequestBodiesFor(doc, item.Post, profileFullCoordinate)
			decision := "admitted"
			if planErr != nil || len(plans) == 0 {
				decision = "refused"
			}
			if decision != testCase.Expect {
				t.Fatalf("decision = %s (plan error %v), want %s", decision, planErr, testCase.Expect)
			}

			// The member the shipped predicate names, read off the same
			// resolved property schema the admission gate reads. A cell with
			// no Encoding Object is on the CONTENT path, where this predicate
			// is never consulted, so only the style-lane cells assert it.
			if testCase.Encoding == nil {
				return
			}
			media := item.Post.RequestBody.Value.Content[testCase.Media]
			if media == nil {
				t.Fatalf("loaded document has no %s media type", testCase.Media)
			}
			property := resolvedMultipartProperty(mediaSchema(media), "field", map[*openapi3.Schema]bool{})
			property, _ = effectiveRevision3PartSchema(property, is30)
			if member := styleLaneUndefinedExpansionMember(property, is30); member != testCase.Member {
				t.Fatalf("member = %q, want %q", member, testCase.Member)
			}
		})
	}
}
