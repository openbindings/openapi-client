package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

const (
	schemaGraphEntry = "https://schemas.example.test/api/openapi.json"
	dialect2020      = "https://json-schema.org/draft/2020-12/schema"
	dialectOAS31     = "https://spec.openapis.org/oas/3.1/dialect/base"
	dialectOAS32     = "https://spec.openapis.org/oas/3.2/dialect/2025-09-17"
)

func schemaGraphParse(t testing.TB, doc string, fetch *memFetch) *openapi.Client {
	t.Helper()
	if fetch == nil {
		fetch = newMemFetch(nil) // synthetic documents never use the network
	}
	c, err := (&openapi.Loader{Fetch: fetch.fetch}).Parse(t.Context(), []byte(doc), schemaGraphEntry, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func schemaGraphDoc(version, schemas string) string {
	return `{"openapi":"` + version + `","info":{"title":"Graph","version":"1"},"paths":{},"components":{"schemas":{` + schemas + `}}}`
}

func schemaGraphGet(t testing.TB, c *openapi.Client, uri string) *openapi.Schema {
	t.Helper()
	s, err := c.Schema(uri)
	if err != nil || s == nil {
		t.Fatalf("Schema(%q) = %v, %v", uri, s, err)
	}
	return s
}

func schemaGraphRefs(t testing.TB, s *openapi.Schema) []openapi.SchemaReference {
	t.Helper()
	r, err := s.References()
	if err != nil {
		t.Fatalf("References(%s): %v", s.Source(), err)
	}
	return r
}

type schemaGraphEdge struct{ at, keyword, value, uri, source string }

func schemaGraphWantEdges(t testing.TB, got []openapi.SchemaReference, want []schemaGraphEdge) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("references = %+v; want %d edges", got, len(want))
	}
	for i, w := range want {
		r := got[i]
		if r.At != w.at || r.Keyword != w.keyword || r.Value != w.value || r.URI != w.uri {
			t.Errorf("edge %d = {%q %q %q %q}, want %+v", i, r.At, r.Keyword, r.Value, r.URI, w)
		}
		if w.source == "" {
			if r.Err == nil || r.Target != nil {
				t.Errorf("edge %d target=%v error=%v; want failed resolution", i, r.Target, r.Err)
			}
		} else if r.Err != nil || r.Target == nil {
			t.Fatalf("edge %d target=%v error=%v; want source %q", i, r.Target, r.Err, w.source)
		} else if r.Target.Source() != w.source {
			t.Errorf("edge %d source=%q; want %q", i, r.Target.Source(), w.source)
		}
	}
}

// Client.Schema, Schema.Source/Base/Dialect/Raw and Loader identifier aliases;
// RFC 6901 sections 3, 4, 6; JSON Schema 2020-12 Core sections 8.2 and 9.2.1.
// A physical pointer crosses resource boundaries; Source never becomes $id.
func TestSchemaLookupAddresses(t *testing.T) {
	const resource = "https://schemas.example.test/models/tree.json"
	const root = schemaGraphEntry + "#/components/schemas/Tree"
	c := schemaGraphParse(t, schemaGraphDoc("3.2.1", `"Tree":{"$id":"../models/tree.json","$schema":"`+dialect2020+`","$anchor":"root","$defs":{"a/b~ c":{"$anchor":"leaf","type":"string","minimum":1.50},"Bool":false,"Nested":{"$id":"child.json","properties":{"x":{"type":"number"}}}}}`), nil)
	cases := []struct{ uri, source, base, raw string }{
		{resource, root, resource, ""},
		{resource + "#root", root, resource, ""},
		{root, root, resource, ""},
		{resource + "#leaf", root + "/$defs/a~1b~0%20c", resource, `{"$anchor":"leaf","type":"string","minimum":1.50}`},
		{resource + "#/$defs/a~1b~0%20c", root + "/$defs/a~1b~0%20c", resource, `{"$anchor":"leaf","type":"string","minimum":1.50}`},
		{root + "/$defs/a~1b~0%20c", root + "/$defs/a~1b~0%20c", resource, `{"$anchor":"leaf","type":"string","minimum":1.50}`},
		{root + "/$defs/Bool", root + "/$defs/Bool", resource, `false`},
		{root + "/$defs/Nested/properties/x", root + "/$defs/Nested/properties/x", "https://schemas.example.test/models/child.json", `{"type":"number"}`},
		{"https://schemas.example.test/models/child.json#/properties/x", root + "/$defs/Nested/properties/x", "https://schemas.example.test/models/child.json", `{"type":"number"}`},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			s := schemaGraphGet(t, c, tc.uri)
			if s.Source() != tc.source || s.Base() != tc.base || s.Dialect() != dialect2020 {
				t.Errorf("Source/Base/Dialect = %q/%q/%q, want %q/%q/%q", s.Source(), s.Base(), s.Dialect(), tc.source, tc.base, dialect2020)
			}
			if tc.raw != "" && string(s.Raw()) != tc.raw {
				t.Errorf("Raw=%s; want %s", s.Raw(), tc.raw)
			}
			if !bytes.Equal(c.Document(s.Source()), s.Raw()) {
				t.Errorf("physical Source does not recover Raw")
			}
		})
	}
}

// Client.Schema rejects non-schema, absent and non-absolute targets. Core
// 4.3.1, 9.4.2: objects in instance data are not subschemas. This is a
// classification test, not general validation of malformed schema keywords.
func TestSchemaLookupRejectsNonSchemas(t *testing.T) {
	c := schemaGraphParse(t, schemaGraphDoc("3.1.2", `"S":{"type":"object","properties":{"p":{}},"default":{"type":"string"},"examples":[{"$id":"fake.json"}],"x-data":{"type":"integer"}}`), nil)
	for _, uri := range []string{
		"", "#/components/schemas/S", "other.json", "/api/openapi.json",
		schemaGraphEntry, schemaGraphEntry + "#/info", schemaGraphEntry + "#/components/schemas",
		schemaGraphEntry + "#/components/schemas/S/properties",
		schemaGraphEntry + "#/components/schemas/S/type",
		schemaGraphEntry + "#/components/schemas/S/default",
		schemaGraphEntry + "#/components/schemas/S/examples/0",
		schemaGraphEntry + "#/components/schemas/S/x-data",
		schemaGraphEntry + "#/components/schemas/Missing", schemaGraphEntry + "#missing",
		schemaGraphEntry + "#/%zz", schemaGraphEntry + "#/components/schemas/S/properties/~2",
		"https://schemas.example.test/not-loaded.json",
	} {
		t.Run(uri, func(t *testing.T) {
			s, err := c.Schema(uri)
			if err == nil || s != nil {
				t.Errorf("Schema(%q) = %v, %v; want nil and error", uri, s, err)
			}
		})
	}
}

// Loader and Client.Schema: distinct claims are ambiguous; a retrieval URI
// and root $id for the same node are aliases, as are requested/final URIs.
func TestSchemaLookupClaimsAndRetrievalAliases(t *testing.T) {
	t.Run("distinct identifiers", func(t *testing.T) {
		c := schemaGraphParse(t, schemaGraphDoc("3.1.2", `"A":{"$id":"same.json"},"B":{"$id":"same.json"},"Anchors":{"properties":{"a":{"$anchor":"dup"},"b":{"$anchor":"dup"}}}`), nil)
		for _, uri := range []string{"https://schemas.example.test/api/same.json", schemaGraphEntry + "#dup"} {
			if s, err := c.Schema(uri); err == nil || s != nil {
				t.Errorf("ambiguous Schema(%q) = %v, %v", uri, s, err)
			}
		}
		// Ambiguity of a canonical name does not make a physical node vanish.
		for _, name := range []string{"A", "B"} {
			schemaGraphGet(t, c, schemaGraphEntry+"#/components/schemas/"+name)
		}
	})
	t.Run("same node", func(t *testing.T) {
		const requested = "https://schemas.example.test/api/start.json"
		const final = "https://schemas.example.test/saved/schema.json"
		m := newMemFetch(map[string]string{requested: `{"$id":"` + final + `","$anchor":"same","type":"string"}`})
		m.finals[requested] = final
		c := schemaGraphParse(t, schemaGraphDoc("3.1.2", `"Use":{"$ref":"start.json"}`), m)
		for _, uri := range []string{requested, final, final + "#same"} {
			s := schemaGraphGet(t, c, uri)
			if strings.TrimSuffix(s.Source(), "#") != final || s.Base() != final {
				t.Errorf("alias %s: Source=%q Base=%q", uri, s.Source(), s.Base())
			}
		}
		if m.callsTo(requested) != 1 {
			t.Errorf("requested URI fetched %d times", m.callsTo(requested))
		}
	})
}

// Schema resource dialect inheritance: Core 4.3.5, 8.1.1, 9.3.2 and OAS
// 3.1.2 4.8.24.5 / 3.2.1 4.24.5. Unknown resources stay opaque under Schema's
// explicit contract, even if an identifier looks like a supported URI.
func TestSchemaResourceDialect(t *testing.T) {
	const foreign = "https://dialects.example.test/unknown"
	const p = schemaGraphEntry + "#/components/schemas/"
	for _, version := range []string{"3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			m := newMemFetch(nil)
			doc := schemaGraphDoc(version, `"Parent":{"$id":"parent.json","$schema":"`+dialect2020+`","properties":{"plain":{"type":"string"},"Other":{"$id":"ignored.json","$schema":"`+foreign+`","$anchor":"hidden","$ref":"never.json","customRef":"also-never.json"}}},"Default":{}`)
			c := schemaGraphParse(t, doc, m)
			plain := schemaGraphGet(t, c, p+"Parent/properties/plain")
			if plain.Base() != "https://schemas.example.test/api/parent.json" || plain.Dialect() != dialect2020 {
				t.Errorf("child lost resource inheritance: Base=%q Dialect=%q", plain.Base(), plain.Dialect())
			}
			other := schemaGraphGet(t, c, p+"Parent/properties/Other")
			if other.Base() != "https://schemas.example.test/api/parent.json" || other.Dialect() != foreign {
				t.Errorf("foreign resource: Base=%q Dialect=%q", other.Base(), other.Dialect())
			}
			if _, err := other.References(); err == nil {
				t.Error("unknown dialect References silently succeeded")
			}
			if !bytes.Equal(other.Raw(), c.Document(other.Source())) {
				t.Error("unknown dialect Raw must stay available")
			}
			for _, uri := range []string{"https://schemas.example.test/api/ignored.json", "https://schemas.example.test/api/parent.json#hidden"} {
				if s, err := c.Schema(uri); s != nil || err == nil {
					t.Errorf("foreign identifier was interpreted: %q => %v %v", uri, s, err)
				}
			}
			wantDefault := dialectOAS31
			if version == "3.2.1" {
				wantDefault = dialectOAS32
			}
			if got := schemaGraphGet(t, c, p+"Default").Dialect(); got != wantDefault {
				t.Errorf("default dialect=%q want %q", got, wantDefault)
			}
			if calls := m.callList(); len(calls) != 0 {
				t.Errorf("opaque resource caused fetches %q", calls)
			}
		})
	}
}

// References traverses authored schema positions, depth first in document
// order, without traversing targets. Core 8.2.3/8.2.4, sections 10/11;
// Validation 8.5 for contentSchema; data-bearing annotations are opaque.
func TestSchemaReferencesVocabularyAndOrder(t *testing.T) {
	const target = schemaGraphEntry + "#/components/schemas/T"
	const ref = `{"$ref":"#/components/schemas/T"}`
	fields := []struct{ field, at string }{
		{`"properties":{"z/ ~":` + ref + `,"a":` + ref + `}`, "/properties/z~1 ~0/$ref"},
		{`"$defs":{"D":` + ref + `}`, "/$defs/D/$ref"},
		{`"patternProperties":{"^x":` + ref + `}`, "/patternProperties/^x/$ref"},
		{`"dependentSchemas":{"x":` + ref + `}`, "/dependentSchemas/x/$ref"},
		{`"allOf":[` + ref + `]`, "/allOf/0/$ref"},
		{`"anyOf":[` + ref + `]`, "/anyOf/0/$ref"},
		{`"oneOf":[` + ref + `]`, "/oneOf/0/$ref"},
		{`"prefixItems":[` + ref + `]`, "/prefixItems/0/$ref"},
	}
	for _, keyword := range []string{"items", "contains", "additionalProperties", "propertyNames", "not", "if", "then", "else", "unevaluatedItems", "unevaluatedProperties", "contentSchema"} {
		fields = append(fields, struct{ field, at string }{fmt.Sprintf("%q:%s", keyword, ref), "/" + keyword + "/$ref"})
	}
	var parts []string
	var want []schemaGraphEdge
	for i, f := range fields {
		parts = append(parts, f.field)
		want = append(want, schemaGraphEdge{f.at, "$ref", "#/components/schemas/T", target, target})
		if i == 0 {
			want = append(want, schemaGraphEdge{"/properties/a/$ref", "$ref", "#/components/schemas/T", target, target})
		}
	}
	parts = append(parts, `"contentMediaType":"application/json"`, `"$ref":"#/components/schemas/T"`, `"default":`+ref, `"const":`+ref, `"enum":[`+ref+`]`, `"examples":[`+ref+`]`, `"example":`+ref, `"x-custom":`+ref, `"unknownKeyword":`+ref)
	want = append(want, schemaGraphEdge{"/$ref", "$ref", "#/components/schemas/T", target, target})
	c := schemaGraphParse(t, schemaGraphDoc("3.1.2", `"S":{`+strings.Join(parts, ",")+`},"T":{"$ref":"#/components/schemas/U"},"U":true`), nil)
	s := schemaGraphGet(t, c, schemaGraphEntry+"#/components/schemas/S")
	r := schemaGraphRefs(t, s)
	schemaGraphWantEdges(t, r, want)
	// The returned slice and Raw bytes are owned by the caller.
	r[0] = openapi.SchemaReference{}
	raw := s.Raw()
	raw[0] = '!'
	schemaGraphWantEdges(t, schemaGraphRefs(t, s), want)
	if !json.Valid(s.Raw()) {
		t.Error("mutating Raw changed the schema")
	}
	leaf := schemaGraphGet(t, c, schemaGraphEntry+"#/components/schemas/U")
	if refs := schemaGraphRefs(t, leaf); len(refs) != 0 {
		t.Errorf("boolean schema refs=%v", refs)
	}
}

// SchemaReference.URI accounts for the base at the keyword; dynamicRef is
// only the static fallback (Core 8.2.3.2), including recursive graphs.
func TestSchemaReferencesNestedBasesAndDynamicFallback(t *testing.T) {
	const base = "https://schemas.example.test/api/tree.json"
	const p = schemaGraphEntry + "#/components/schemas/Tree"
	c := schemaGraphParse(t, schemaGraphDoc("3.1.2", `"Tree":{"$id":"tree.json","$dynamicAnchor":"node","properties":{"next":{"$dynamicRef":"#node"},"child":{"$id":"child.json","$dynamicAnchor":"node","allOf":[{"$dynamicRef":"tree.json#node"}],"properties":{"next":{"$dynamicRef":"#node"}}}},"$defs":{"Again":{"$ref":"#node"}}}`), nil)
	s := schemaGraphGet(t, c, base)
	schemaGraphWantEdges(t, schemaGraphRefs(t, s), []schemaGraphEdge{
		{"/properties/next/$dynamicRef", "$dynamicRef", "#node", base + "#node", p},
		{"/properties/child/allOf/0/$dynamicRef", "$dynamicRef", "tree.json#node", base + "#node", p},
		{"/properties/child/properties/next/$dynamicRef", "$dynamicRef", "#node", "https://schemas.example.test/api/child.json#node", p + "/properties/child"},
		{"/$defs/Again/$ref", "$ref", "#node", base + "#node", p},
	})
}

// Per-edge Err and retained retrieval causes: SchemaReference.Target/Err,
// ErrUnresolved, Loader.Fetch. One bad edge does not erase adjacent edges.
func TestSchemaReferenceFailuresAndNoFetch(t *testing.T) {
	cause := errors.New("synthetic document unavailable")
	m := newMemFetch(nil)
	m.errs["https://schemas.example.test/api/missing.json"] = cause
	c := schemaGraphParse(t, schemaGraphDoc("3.1.2", `"S":{"allOf":[{"$ref":"missing.json"},{"$ref":"#/components/schemas/Absent"},{"$ref":"#/info"},{"$ref":"%zz"},{"$ref":"#/components/schemas/T"}]},"T":{"type":"string"}`), m)
	before := m.callList()
	s := schemaGraphGet(t, c, schemaGraphEntry+"#/components/schemas/S")
	r := schemaGraphRefs(t, s)
	if len(r) != 5 {
		t.Fatalf("references=%+v want 5", r)
	}
	for i := 0; i < 4; i++ {
		if r[i].Target != nil || r[i].Err == nil {
			t.Errorf("edge %d = %+v; want error only", i, r[i])
		}
	}
	if !errors.Is(r[0].Err, cause) || !errors.Is(r[0].Err, openapi.ErrUnresolved) {
		t.Errorf("retrieval cause lost: %v", r[0].Err)
	}
	if r[4].Err != nil || r[4].Target == nil {
		t.Errorf("good edge lost: %+v", r[4])
	}
	for range 3 {
		_ = s.Raw()
		_ = s.Source()
		_ = s.Base()
		_ = s.Dialect()
		_ = schemaGraphRefs(t, s)
		_, _ = c.Schema("https://schemas.example.test/api/missing.json")
		_, _ = c.Schema("https://schemas.example.test/api/not-discovered.json")
		_ = c.DocumentURIs()
		_ = c.Document(schemaGraphEntry)
	}
	if after := m.callList(); !slices.Equal(before, after) {
		t.Errorf("accessors fetched: before %q after %q", before, after)
	}
}

// Client.Schema and Client's immutable shared-state concurrency contract.
// Exercise lookup aliases, ancestor resource metadata, fresh result copies
// and reference traversal concurrently, without mutating shared descriptors.
func TestSchemaGraphConcurrentReads(t *testing.T) {
	c := schemaGraphParse(t, schemaGraphDoc("3.1.2", `"S":{"$id":"s.json","$defs":{"T":{"$anchor":"t","type":"string"}},"properties":{"v":{"$ref":"#t"}}}`), nil)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			for range 20 {
				s, err := c.Schema("https://schemas.example.test/api/s.json")
				if err != nil || s == nil {
					t.Errorf("Schema: %v %v", s, err)
					return
				}
				r, err := s.References()
				if err != nil || len(r) != 1 || r[0].Err != nil || r[0].Target == nil {
					t.Errorf("References: %+v %v", r, err)
					return
				}
				if r[0].Target.Source() != schemaGraphEntry+"#/components/schemas/S/$defs/T" || r[0].Target.Base() != "https://schemas.example.test/api/s.json" || r[0].Target.Dialect() != dialectOAS31 {
					t.Error("metadata changed")
				}
				r[0] = openapi.SchemaReference{}
				copy := s.Raw()
				copy[0] = '!'
			}
		})
	}
	wg.Wait()
}
