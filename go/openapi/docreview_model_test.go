package openapi_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Loader identifies schemas, not their instance data. JSON Schema 2020-12
// Core sections 4.3.1 and 9.4.2 distinguish known schema-bearing locations
// from annotations and other data. The cases include an annotation-free
// control.
func TestDocumentSchemaDataCannotClaimIdentifiers(t *testing.T) {
	const base = "https://docs.example.test/"
	const entry = `{"openapi":"3.1.2","paths":{"/u":{"post":{"operationId":"u","requestBody":{"content":{"multipart/form-data":{"schema":{"$ref":"schema.json"}}}}}}}}`
	for _, annotation := range []string{"", "default", "const", "example", "x-data"} {
		t.Run("annotation="+annotation, func(t *testing.T) {
			data := ""
			if annotation != "" {
				data = fmt.Sprintf(`,%q:{"$id":"schema.json"}`, annotation)
			}
			m := newMemFetch(map[string]string{base + "schema.json": `{"$id":"schema.json","type":"object","properties":{"p":{"type":"string"}}` + data + `}`})
			c, err := (&openapi.Loader{Fetch: m.fetch}).Parse(t.Context(), []byte(entry), base+"openapi.json", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := encodingOf(t, c, "u"); len(got) != 1 || !strings.HasPrefix(got[0], "p=") {
				t.Errorf("encoding = %v, want field p; data must not claim the schema's URI", got)
			}
		})
	}
}

// Known schema locations remain schemas through $defs, even when a child
// has no identifier of its own. JSON Schema 2020-12 Core sections 4.3.1,
// 8.2.4 and 9.4.2 do not make a sibling schema's instance data an
// identifier claimant. The reference names a schema, never a pointer into
// the data.
func TestDocumentNestedSchemaDataCannotClaimIdentifiers(t *testing.T) {
	const base = "https://docs.example.test/"
	const entry = `{"openapi":"3.1.2","paths":{"/u":{"post":{"operationId":"u","requestBody":{"content":{"multipart/form-data":{"schema":{"$ref":"schema.json#/$defs/Used"}}}}}}}}`
	for _, annotation := range []string{"default", "const"} {
		t.Run(annotation, func(t *testing.T) {
			schema := fmt.Sprintf(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"schema.json","$defs":{"Used":{"properties":{"p":{"type":"string"}}},"Other":{"type":"object",%q:{"$id":"schema.json"}}}}`, annotation)
			m := newMemFetch(map[string]string{base + "schema.json": schema})
			c, err := (&openapi.Loader{Fetch: m.fetch}).Parse(t.Context(), []byte(entry), base+"openapi.json", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := encodingOf(t, c, "u"); len(got) != 1 || !strings.HasPrefix(got[0], "p=") {
				t.Errorf("encoding = %v, want field p; sibling data must not claim the schema's URI", got)
			}
		})
	}
}

// OAS 3.1.2 section 4.8.24.5 makes jsonSchemaDialect the document default;
// an explicit resource $schema overrides it. Schema's contract does not
// interpret identifiers in a foreign dialect. The cases include two
// explicit-override controls.
func TestDocumentEffectiveSchemaDialect(t *testing.T) {
	const base = "https://docs.example.test/"
	const foreign = "https://example.test/custom-dialect"
	const own = "https://json-schema.org/draft/2020-12/schema"
	for _, tt := range []struct {
		name, defaultDialect, override string
		fetches                        int
	}{
		{"foreign default", foreign, "", 0},
		{"own override", foreign, own, 1},
		{"foreign override", own, foreign, 0},
		{"own default", own, "", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newMemFetch(map[string]string{base + "target.json": `{}`})
			override := ""
			if tt.override != "" {
				override = fmt.Sprintf(`"$schema":%q,`, tt.override)
			}
			doc := fmt.Sprintf(`{"openapi":"3.1.2","jsonSchemaDialect":%q,"components":{"schemas":{"S":{%s"$ref":"target.json"}}}}`, tt.defaultDialect, override)
			if _, err := (&openapi.Loader{Fetch: m.fetch}).Parse(t.Context(), []byte(doc), base+"openapi.json", nil); err != nil {
				t.Fatal(err)
			}
			if got := m.callsTo(base + "target.json"); got != tt.fetches {
				t.Errorf("target fetched %d times, want %d", got, tt.fetches)
			}
		})
	}
	t.Run("foreign identifier cannot intercept path item", func(t *testing.T) {
		m := newMemFetch(map[string]string{base + "target.json": `{"get":{"operationId":"p"}}`})
		doc := `{"openapi":"3.1.2","jsonSchemaDialect":"` + foreign + `","paths":{"/p":{"$ref":"target.json"}},"components":{"schemas":{"S":{"$id":"target.json"}}}}`
		c, err := (&openapi.Loader{Fetch: m.fetch}).Parse(t.Context(), []byte(doc), base+"openapi.json", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.callsTo(base + "target.json"); got != 1 {
			t.Errorf("path item target fetched %d times, want 1", got)
		}
		if _, err := c.Operation("p"); err != nil {
			t.Errorf("Operation(p): %v", err)
		}
	})
}

// OAS 3.1.2 section 4.8.7.1 permits x-prefixed component names.
// Loader follows declarations throughout the document, even when unused.
// Genuine extensions still contain data, not references to retrieve.
func TestDocumentXPrefixedComponentNames(t *testing.T) {
	const base = "https://docs.example.test/"
	for _, kind := range []string{"responses", "pathItems"} {
		t.Run(kind, func(t *testing.T) {
			m := newMemFetch(map[string]string{base + "target.json": `{}`, base + "ignored.json": `{}`})
			doc := fmt.Sprintf(`{"openapi":"3.1.2","paths":{"x-extension":{"$ref":"ignored.json"}},"components":{"%s":{"x-valid":{"$ref":"target.json"}},"x-extension":{"$ref":"ignored.json"}}}`, kind)
			if _, err := (&openapi.Loader{Fetch: m.fetch}).Parse(t.Context(), []byte(doc), base+"openapi.json", nil); err != nil {
				t.Fatal(err)
			}
			if got := m.callsTo(base + "target.json"); got != 1 {
				t.Errorf("%s/x-valid target fetched %d times, want 1", kind, got)
			}
			if got := m.callsTo(base + "ignored.json"); got != 0 {
				t.Errorf("extension data fetched %d times, want 0", got)
			}
		})
	}
}
