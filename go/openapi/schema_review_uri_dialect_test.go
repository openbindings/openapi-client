package openapi_test

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

func review9EditionDocument(version, schemas string) string {
	if version == "2.0" {
		return `{"swagger":"2.0","info":{"title":"URI graph","version":"1"},"paths":{},"definitions":{` + schemas + `}}`
	}
	return schema9Doc(version, schemas)
}

// Parse permits self-reference with no provided URI and names that
// document with a generated URN. A fragment-only reference retains the
// effective base, including opaque path and query (RFC3986 5.2.2). Legacy
// subset references obey the same resolution rule as modern SchemaReference.
func TestReview9OpaqueDocumentFragments(t *testing.T) {
	for _, version := range []string{"2.0", "3.0.4", "3.1.2", "3.2.1"} {
		for _, uri := range []string{"", "urn:example:schema-review-document?revision=1", schema9Entry} {
			t.Run(version+"/"+uri, func(t *testing.T) {
				prefix := "#/components/schemas/"
				if version == "2.0" {
					prefix = "#/definitions/"
				}
				target := prefix + "T"
				fetch := newMemFetch(nil)
				c, err := (&openapi.Loader{Fetch: fetch.fetch}).Parse(t.Context(), []byte(review9EditionDocument(version, `"S":{"type":"array","items":{"$ref":"`+target+`"}},"T":{"type":"string"}`)), uri, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(c.DocumentURIs()) != 1 {
					t.Fatalf("inventory=%q", c.DocumentURIs())
				}
				physical := c.DocumentURIs()[0]
				if uri == "" && !strings.HasPrefix(physical, "urn:uuid:") {
					t.Errorf("anonymous Source base=%q", physical)
				}
				if uri != "" && physical != uri {
					t.Errorf("retrieval URI changed to %q", physical)
				}
				s := schema9Get(t, c, physical+prefix+"S")
				control := schema9Get(t, c, physical+target)
				if string(control.Raw()) != `{"type":"string"}` {
					t.Error("loaded target control differs")
				}
				if s.Base() != physical {
					t.Errorf("Base=%q want %q", s.Base(), physical)
				}
				schema9WantEdges(t, schema9Refs(t, s), []schema9Edge{{"/items/$ref", "$ref", target, physical + target, physical + target}})
				if len(fetch.callList()) != 0 {
					t.Errorf("fragment caused retrieval: %q", fetch.callList())
				}
			})
		}
	}
}

// Core 8.2.1/8.2.2/8.2.3 and RFC3986 5.2.2: references within nested URN
// resources use that effective base, not the physical document URI. The
// dynamic reference exposes its lexical fallback; empty resource references
// keep the resource itself. Mapping/defaultMapping use the same URI rule.
func TestReview9NestedURNReferences(t *testing.T) {
	const resource = "urn:example:schema-review-child?revision=2"
	const source = schema9Entry + "#/components/schemas/S/$defs/Child"
	for _, version := range []string{"3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			discriminator := `"discriminator":{"propertyName":"kind","mapping":{"leaf":"#leaf"}`
			if version == "3.2.1" {
				discriminator += `,"defaultMapping":"#leaf"`
			}
			discriminator += `}`
			child := `{"$id":"` + resource + `","$dynamicAnchor":"scope","$defs":{"Leaf":{"$anchor":"leaf","type":"string"}},"properties":{"ordinary":{"$ref":"#leaf"},"dynamic":{"$dynamicRef":"#scope"},"pointer":{"$ref":"#/$defs/Leaf"},"self":{"$ref":""}},"oneOf":[{"type":"object"}],"required":["kind"],` + discriminator + `}`
			fetch := newMemFetch(nil)
			c := schema9Parse(t, schema9Doc(version, `"S":{"$id":"urn:example:schema-review-parent","$defs":{"Child":`+child+`}}`), fetch)
			s := schema9Get(t, c, resource)
			if s.Source() != source || s.Base() != resource {
				t.Errorf("resource metadata=%q %q", s.Source(), s.Base())
			}
			leaf := schema9Get(t, c, resource+"#leaf")
			if leaf.Source() != source+"/$defs/Leaf" {
				t.Error("independent anchor control differs")
			}
			want := []schema9Edge{
				{"/properties/ordinary/$ref", "$ref", "#leaf", resource + "#leaf", source + "/$defs/Leaf"},
				{"/properties/dynamic/$dynamicRef", "$dynamicRef", "#scope", resource + "#scope", source},
				{"/properties/pointer/$ref", "$ref", "#/$defs/Leaf", resource + "#/$defs/Leaf", source + "/$defs/Leaf"},
				{"/properties/self/$ref", "$ref", "", resource, source},
				{"/discriminator/mapping/leaf", "mapping", "#leaf", resource + "#leaf", source + "/$defs/Leaf"},
			}
			if version == "3.2.1" {
				want = append(want, schema9Edge{"/discriminator/defaultMapping", "defaultMapping", "#leaf", resource + "#leaf", source + "/$defs/Leaf"})
			}
			schema9WantEdges(t, schema9Refs(t, s), want)
			if len(fetch.callList()) != 0 {
				t.Errorf("URN references fetched %q", fetch.callList())
			}
		})
	}
}

// Loader.Parse explicitly gives an anonymous document no relative external
// base. An opaque declared URI likewise cannot supply a hierarchical base.
// Correct fragment support must not accidentally enable unrelated retrieval.
func TestReview9OpaqueRelativePathRefusal(t *testing.T) {
	for _, version := range []string{"2.0", "3.0.4", "3.1.2", "3.2.1"} {
		for _, uri := range []string{"", "urn:example:relative-path-control"} {
			t.Run(version+"/"+uri, func(t *testing.T) {
				fetch := newMemFetch(nil)
				c, err := (&openapi.Loader{Fetch: fetch.fetch}).Parse(t.Context(), []byte(review9EditionDocument(version, `"S":{"type":"array","items":{"$ref":"./elsewhere.json"}}`)), uri, nil)
				if err != nil {
					t.Fatal(err)
				}
				prefix := "#/components/schemas/S"
				if version == "2.0" {
					prefix = "#/definitions/S"
				}
				r := schema9Refs(t, schema9Get(t, c, c.DocumentURIs()[0]+prefix))
				if len(r) != 1 || r[0].Target != nil || !errors.Is(r[0].Err, openapi.ErrUnresolved) {
					t.Fatalf("relative path edge=%+v", r)
				}
				if len(fetch.callList()) != 0 {
					t.Errorf("relative path fetched %q", fetch.callList())
				}
			})
		}
	}
}

// Dialect identity is exact, not prefix recognition. These known IDs come
// from OAS 3.1's normative base and the official publication index at
// https://spec.openapis.org/oas/ (checked 2026-10-03), plus JSON Schema
// 2020-12. Explicit support for a later publication does not change the
// pinned public 3.2 default 2025-09-17. Both discovery and inspection must
// honor these IDs.
func TestReview9KnownDialectIdentifiers(t *testing.T) {
	known := []struct{ version, dialect string }{
		{"3.1.2", schema9OAS31},
		{"3.1.2", "https://spec.openapis.org/oas/3.1/dialect/2024-10-25"},
		{"3.1.2", "https://spec.openapis.org/oas/3.1/dialect/2024-11-10"},
		{"3.2.1", schema9OAS32},
		{"3.2.1", "https://spec.openapis.org/oas/3.2/dialect/2026-02-26"},
		{"3.1.2", schema9JSON},
	}
	const resource = "https://schemas.example.test/api/models/root.json"
	const external = "https://schemas.example.test/api/models/target.json"
	const source = schema9Entry + "#/components/schemas/S"
	for _, tt := range known {
		for _, mode := range []string{"resource", "document"} {
			t.Run(tt.dialect+"/"+mode, func(t *testing.T) {
				fields := `"$id":"models/root.json","$defs":{"Leaf":{"$anchor":"leaf","type":"string"}},"properties":{"local":{"$ref":"#leaf"},"remote":{"$ref":"./target.json#/T"}}`
				if mode == "resource" {
					fields = fmt.Sprintf(`"$schema":%q,`, tt.dialect) + fields
				}
				doc := schema9Doc(tt.version, `"S":{`+fields+`}`)
				if mode == "document" {
					doc = strings.Replace(doc, `"info":`, fmt.Sprintf(`"jsonSchemaDialect":%q,"info":`, tt.dialect), 1)
				}
				fetch := newMemFetch(map[string]string{external: `{"T":{"type":"integer"}}`})
				c := schema9Parse(t, doc, fetch)
				if !slices.Equal(fetch.callList(), []string{external}) {
					t.Errorf("known dialect discovery=%q", fetch.callList())
				}
				s := schema9Get(t, c, source)
				if s.Dialect() != tt.dialect || s.Base() != resource {
					t.Errorf("metadata=%q %q", s.Dialect(), s.Base())
				}
				schema9WantEdges(t, schema9Refs(t, s), []schema9Edge{
					{"/properties/local/$ref", "$ref", "#leaf", resource + "#leaf", source + "/$defs/Leaf"},
					{"/properties/remote/$ref", "$ref", "./target.json#/T", external + "#/T", external + "#/T"},
				})
			})
		}
	}
	c := schema9Parse(t, schema9Doc("3.2.1", `"S":{}`), nil)
	if d := schema9Get(t, c, source).Dialect(); d != schema9OAS32 {
		t.Errorf("default drifted to%q", d)
	}
}

// Similar paths and query-bearing variants name other dialects. Core8.1.1,
// Schema's opaque-dialect contract: no ID interpretation, no reference
// discovery, Base outside the resource, Raw retained and References refuses.
func TestReview9UnknownDialectLookalikes(t *testing.T) {
	for _, dialect := range []string{
		"https://spec.openapis.org/oas/3.1/dialect/private-vocabulary",
		"https://spec.openapis.org/oas/3.1/dialect/base-extra",
		"https://spec.openapis.org/oas/3.1/dialect/2024-11-10?private=1",
		"https://spec.openapis.org/oas/3.2/dialect/private-vocabulary",
		"https://spec.openapis.org/oas/3.2/dialect/2026-02-26/extra",
		"https://spec.openapis.org/oas/3.2/dialect/2025-09-17?private=1",
		schema9JSON + "?private=1",
	} {
		for _, mode := range []string{"resource", "document"} {
			t.Run(dialect+"/"+mode, func(t *testing.T) {
				fields := `"$id":"ignored.json","$ref":"./never.json","customRef":"also-never.json"`
				if mode == "resource" {
					fields = fmt.Sprintf(`"$schema":%q,`, dialect) + fields
				}
				doc := schema9Doc("3.2.1", `"S":{`+fields+`}`)
				if mode == "document" {
					doc = strings.Replace(doc, `"info":`, fmt.Sprintf(`"jsonSchemaDialect":%q,"info":`, dialect), 1)
				}
				fetch := newMemFetch(nil)
				c := schema9Parse(t, doc, fetch)
				if len(fetch.callList()) != 0 {
					t.Errorf("unknown vocabulary was fetched: %q", fetch.callList())
				}
				s := schema9Get(t, c, schema9Entry+"#/components/schemas/S")
				if s.Dialect() != dialect || s.Base() != schema9Entry || !bytes.Equal(s.Raw(), []byte("{"+fields+"}")) {
					t.Error("foreign metadata interpretation differs")
				}
				if _, err := s.References(); err == nil {
					t.Error("unknown dialect silently interpreted")
				}
				if found, err := c.Schema("https://schemas.example.test/api/ignored.json"); found != nil || err == nil {
					t.Error("foreign identifier interpreted")
				}
			})
		}
	}
}

// jsonSchemaDialect belongs to an OpenAPI Object. On a standalone schema it
// is ordinary annotation; only $schema or inherited/default dialect governs
// vocabulary (Core 4.3.1/8.1.1 and OAS Schema Object).
func TestReview9StandaloneDialectAnnotation(t *testing.T) {
	const external = "https://schemas.example.test/api/standalone.json"
	const resource = "https://schemas.example.test/api/models/standalone.json"
	for _, version := range []string{"3.1.2", "3.2.1"} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/explicit=%t", version, explicit), func(t *testing.T) {
				fields := `"jsonSchemaDialect":"https://dialects.example.test/annotation","$id":"models/standalone.json","properties":{"next":{"jsonSchemaDialect":"https://dialects.example.test/another-annotation","$ref":"#/$defs/T"}},"$defs":{"T":{"type":"integer"}}`
				want := schema9OAS31
				if version == "3.2.1" {
					want = schema9OAS32
				}
				if explicit {
					fields = `"$schema":"` + schema9JSON + `",` + fields
					want = schema9JSON
				}
				fetch := newMemFetch(map[string]string{external: "{" + fields + "}"})
				c := schema9Parse(t, schema9Doc(version, `"S":{"$ref":"standalone.json"}`), fetch)
				s := schema9Get(t, c, external)
				if s.Dialect() != want || s.Base() != resource {
					t.Errorf("standalone metadata=%q %q", s.Dialect(), s.Base())
				}
				schema9WantEdges(t, schema9Refs(t, s), []schema9Edge{{"/properties/next/$ref", "$ref", "#/$defs/T", resource + "#/$defs/T", external + "#/$defs/T"}})
				child := schema9Get(t, c, external+"#/properties/next")
				if child.Dialect() != want || child.Base() != resource {
					t.Error("child annotation changed inherited dialect/base")
				}
				if !slices.Equal(fetch.callList(), []string{external}) {
					t.Errorf("fetches=%q", fetch.callList())
				}
			})
		}
	}
}
