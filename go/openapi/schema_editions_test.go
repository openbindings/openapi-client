package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Schema's edition-specific handle contract; OAS 3.0.4 Reference Object
// 4.7.23 and Swagger 2.0 Reference Object ignore siblings. Modern $ref is
// an applicator (Core 8.2.3.1), so the authored use site remains available.
func TestSchema9EditionHandles(t *testing.T) {
	for _, version := range []string{"2.0", "3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			prefix := "#/components/schemas/"
			if version == "2.0" {
				prefix = "#/definitions/"
			}
			use := fmt.Sprintf(`{"$ref":%q,"description":"a sibling"}`, prefix+"Middle")
			terminal := `{"type":"array","items":{"$ref":"` + prefix + `Leaf"}}`
			schemas := `"Use":` + use + `,"Middle":{"$ref":"` + prefix + `End"},"End":` + terminal + `,"Leaf":{"type":"string"},"Broken":{"$ref":"` + prefix + `Missing"},"CycleA":{"$ref":"` + prefix + `CycleB"},"CycleB":{"$ref":"` + prefix + `CycleA"}`
			doc := schema9Doc(version, schemas)
			if version == "2.0" {
				doc = `{"swagger":"2.0","info":{"title":"Graph","version":"1"},"paths":{},"definitions":{` + schemas + `}}`
			}
			c := schema9Parse(t, doc, nil)
			s := schema9Get(t, c, schema9Entry+prefix+"Use")
			wantRaw, wantSource, wantAt, wantValue := use, schema9Entry+prefix+"Use", "/$ref", prefix+"Middle"
			wantTarget := schema9Entry + prefix + "Middle"
			if version == "2.0" || version == "3.0.4" {
				wantRaw, wantSource, wantAt, wantValue = terminal, schema9Entry+prefix+"End", "/items/$ref", prefix+"Leaf"
				wantTarget = schema9Entry + prefix + "Leaf"
				if s.Dialect() != "" {
					t.Errorf("legacy Dialect=%q", s.Dialect())
				}
			}
			if string(s.Raw()) != wantRaw || s.Source() != wantSource {
				t.Errorf("Raw=%s Source=%q; want %s %q", s.Raw(), s.Source(), wantRaw, wantSource)
			}
			schema9WantEdges(t, schema9Refs(t, s), []schema9Edge{{wantAt, "$ref", wantValue, schema9Entry + wantValue, wantTarget}})
			for _, name := range []string{"Broken", "CycleA"} {
				h := schema9Get(t, c, schema9Entry+prefix+name)
				if h.Source() != schema9Entry+prefix+name {
					t.Errorf("unresolved/cyclic site moved to %q", h.Source())
				}
				r := schema9Refs(t, h)
				if len(r) != 1 {
					t.Fatalf("%s refs=%+v", name, r)
				}
				// A modern cycle is an ordinary finite graph edge. Legacy
				// following cannot produce a terminal schema and reports it.
				if name == "CycleA" && version != "2.0" && version != "3.0.4" {
					if r[0].Err != nil || r[0].Target == nil {
						t.Errorf("modern cycle edge=%+v", r[0])
					}
				} else if r[0].Target != nil || !errors.Is(r[0].Err, openapi.ErrUnresolved) {
					t.Errorf("%s failed edge=%+v", name, r[0])
				}
			}
		})
	}
}

// Schema.References follows the edition's subset, not arbitrary JSON Schema
// keywords. Swagger 2.0 Schema Object; OAS 3.0.4 4.7.24.1 and 3.1.2 4.8.24.
// Unsupported keywords here are preserved data, with no validity verdict.
func TestSchema9LegacyVocabulary(t *testing.T) {
	for _, version := range []string{"2.0", "3.0.4"} {
		t.Run(version, func(t *testing.T) {
			prefix := "#/components/schemas/"
			if version == "2.0" {
				prefix = "#/definitions/"
			}
			ref := `{"$ref":"` + prefix + `T"}`
			fields := `"allOf":[` + ref + `],"properties":{"p":` + ref + `},"items":` + ref + `,"additionalProperties":` + ref
			wantAt := []string{"/allOf/0/$ref", "/properties/p/$ref", "/items/$ref", "/additionalProperties/$ref"}
			if version == "3.0.4" {
				fields += `,"anyOf":[` + ref + `],"oneOf":[` + ref + `],"not":` + ref
				wantAt = append(wantAt, "/anyOf/0/$ref", "/oneOf/0/$ref", "/not/$ref")
			}
			fields += `,"$defs":{"D":` + ref + `},"prefixItems":[` + ref + `],"dependentSchemas":{"p":` + ref + `},"if":` + ref + `,"contains":` + ref + `,"unevaluatedProperties":` + ref + `,"$dynamicRef":"` + prefix + `T","example":` + ref
			schemas := `"S":{` + fields + `},"T":{"type":"string"}`
			doc := schema9Doc(version, schemas)
			if version == "2.0" {
				doc = `{"swagger":"2.0","info":{"title":"Graph","version":"1"},"paths":{},"definitions":{` + schemas + `}}`
			}
			c := schema9Parse(t, doc, nil)
			var want []schema9Edge
			for _, at := range wantAt {
				want = append(want, schema9Edge{at, "$ref", prefix + "T", schema9Entry + prefix + "T", schema9Entry + prefix + "T"})
			}
			schema9WantEdges(t, schema9Refs(t, schema9Get(t, c, schema9Entry+prefix+"S")), want)
		})
	}
}

// SchemaReference: a component-name value selects only the entry document's
// components/schemas (OAS 3.1.2 4.3.3 / 4.8.25.3 and OAS 3.2.1 4.1.2.3 /
// Discriminator Object). A component-shaped string never falls back to a
// referrer or becomes a fetch; ./ forces URI. Security SchemeLookup does not
// alter discriminator names.
func TestSchema9DiscriminatorEntryNames(t *testing.T) {
	const external = "https://schemas.example.test/api/other.json"
	const root = external + "#/components/schemas/Union"
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		for _, mode := range []openapi.SchemeLookup{openapi.SchemesInEntryFirst, openapi.SchemesInEntry, openapi.SchemesInReferrer} {
			t.Run(fmt.Sprintf("%s/%d", version, mode), func(t *testing.T) {
				m := newMemFetch(map[string]string{
					external: schema9Doc(version, `"Union":{"oneOf":[{"type":"object"}],"required":["kind"],"discriminator":{"propertyName":"kind","mapping":{"z/ ~":"Shared","absent":"OnlyHere","file":"thing.json","explicit":"./thing.json"},"defaultMapping":"Default"}},"Shared":{"title":"referrer"},"OnlyHere":{}`),
					"https://schemas.example.test/api/thing.json": `{"title":"explicit URI"}`,
				})
				doc := schema9Doc(version, `"Use":{"$ref":"other.json#/components/schemas/Union"},"Shared":{"title":"entry"},"Default":{}`)
				c, err := (&openapi.Loader{Fetch: m.fetch, SchemeLookup: mode}).Parse(t.Context(), []byte(doc), schema9Entry, nil)
				if err != nil {
					t.Fatal(err)
				}
				want := []schema9Edge{
					{"/discriminator/mapping/z~1 ~0", "mapping", "Shared", schema9Entry + "#/components/schemas/Shared", schema9Entry + "#/components/schemas/Shared"},
					{"/discriminator/mapping/absent", "mapping", "OnlyHere", schema9Entry + "#/components/schemas/OnlyHere", ""},
					{"/discriminator/mapping/file", "mapping", "thing.json", schema9Entry + "#/components/schemas/thing.json", ""},
					{"/discriminator/mapping/explicit", "mapping", "./thing.json", "https://schemas.example.test/api/thing.json", "https://schemas.example.test/api/thing.json#"},
				}
				if version == "3.2.1" {
					want = append(want, schema9Edge{"/discriminator/defaultMapping", "defaultMapping", "Default", schema9Entry + "#/components/schemas/Default", schema9Entry + "#/components/schemas/Default"})
				}
				r := schema9Refs(t, schema9Get(t, c, root))
				schema9WantEdges(t, r[:min(3, len(r))], want[:3])
				if len(r) != len(want) {
					t.Fatalf("references=%+v want %d", r, len(want))
				}
				if r[3].At != want[3].at || r[3].Keyword != "mapping" || r[3].Value != "./thing.json" || r[3].URI != want[3].uri || r[3].Err != nil || r[3].Target == nil || strings.TrimSuffix(r[3].Target.Source(), "#") != want[3].uri {
					t.Errorf("explicit URI edge=%+v", r[3])
				}
				if version == "3.2.1" {
					schema9WantEdges(t, r[4:], want[4:])
				}
				if m.callsTo("https://schemas.example.test/api/OnlyHere") != 0 || m.callsTo("https://schemas.example.test/api/Shared") != 0 || m.callsTo("https://schemas.example.test/api/thing.json") != 1 {
					t.Errorf("fetches=%q", m.callList())
				}
			})
		}
	}
}

// References and Schema's opaque-resource rule applies inside a known schema
// too (Schema.References: "If the tree includes a schema resource in another
// dialect (see Schema), References returns an error"). No partial slice
// shape is promised.
func TestSchema9NestedForeignReferencesRefuse(t *testing.T) {
	const foreign = "https://dialects.example.test/foreign"
	c := schema9Parse(t, schema9Doc("3.1.2", `"S":{"$defs":{"Foreign":{"$id":"ignored.json","$schema":"`+foreign+`","customReference":"never.json"}}},"Data":{"default":{"$schema":"`+foreign+`"},"example":{"$schema":"`+foreign+`"},"x-data":{"$schema":"`+foreign+`"}}`), nil)
	if _, err := schema9Get(t, c, schema9Entry+"#/components/schemas/S").References(); err == nil {
		t.Error("nested foreign resource silently omitted")
	}
	if r := schema9Refs(t, schema9Get(t, c, schema9Entry+"#/components/schemas/Data")); len(r) != 0 {
		t.Errorf("instance data references=%+v", r)
	}
}

// Param.Schema and Media.Schema's Swagger synthesis promises, not a claim
// that a synthetic Source round-trips as a literal Schema Object. Swagger
// 2.0 Parameter and Items Objects define these scalar/array schema fields.
func TestSchema9SwaggerSyntheticHandles(t *testing.T) {
	doc := `{"swagger":"2.0","info":{"title":"Forms","version":"1"},"consumes":["application/x-www-form-urlencoded"],"paths":{"/f":{"post":{"operationId":"form","parameters":[{"name":"q","in":"query","description":"outside schema","type":"array","items":{"type":"string","pattern":"^[a-z]+$"},"minItems":1,"uniqueItems":true,"collectionFormat":"multi"},{"name":"z","in":"formData","required":true,"type":"array","items":{"type":"integer","minimum":1}},{"name":"a","in":"formData","type":"string","default":"value"}]}}}}`
	c := schema9Parse(t, doc, nil)
	op := mustOp(t, c, "form")
	if len(op.Params) != 1 || op.Body == nil || len(op.Body.Media) != 1 {
		t.Fatalf("unexpected descriptor: %+v", op)
	}
	q := op.Params[0].Schema
	if q == nil {
		t.Fatal("parameter schema missing")
	}
	schema9SameValue(t, "synthetic parameter", q.Raw(), []byte(`{"type":"array","items":{"type":"string","pattern":"^[a-z]+$"},"minItems":1,"uniqueItems":true}`))
	if q.Source() != schema9Entry+"#/paths/~1f/post/parameters/0" || q.Base() != schema9Entry || q.Dialect() != "" {
		t.Errorf("synthetic metadata=%q %q %q", q.Source(), q.Base(), q.Dialect())
	}
	form := op.Body.Media[0].Schema
	if form == nil {
		t.Fatal("form schema missing")
	}
	schema9SameValue(t, "synthetic form", form.Raw(), []byte(`{"type":"object","properties":{"z":{"type":"array","items":{"type":"integer","minimum":1}},"a":{"type":"string","default":"value"}},"required":["z"]}`))
	for _, s := range []*openapi.Schema{q, form} {
		if r := schema9Refs(t, s); len(r) != 0 {
			t.Errorf("synthetic refs=%+v", r)
		}
		before := slices.Clone(s.Raw())
		changed := s.Raw()
		changed[0] = '!'
		if !bytes.Equal(s.Raw(), before) {
			t.Error("synthetic Raw aliases storage")
		}
	}
}

// Client.Operations shared handles, inventory aliases/copies and Schema
// composition. OAS 3.2 $self controls Base; retrieval pointers control Source.
// ItemSchema, header/parameter and multipart property schema locations are
// all public descriptor entry points into the same loaded graph.
func TestSchema9DescriptorInventoryComposition(t *testing.T) {
	const alias = "https://schemas.example.test/canonical/api.json"
	const external = "https://schemas.example.test/canonical/unused.json"
	m := newMemFetch(map[string]string{external: `{"type":"string"}`})
	doc := `{"openapi":"3.2.1","$self":"../canonical/api.json","info":{"title":"Graph","version":"1"},"paths":{"/x":{"post":{"operationId":"x","parameters":[{"name":"q","in":"query","schema":{"type":"string"}}],"requestBody":{"content":{"multipart/form-data":{"schema":{"$id":"form.json","properties":{"field":{"type":"string"}}}}}},"responses":{"200":{"description":"ok","headers":{"Count":{"schema":{"type":"integer"}}},"content":{"application/jsonl":{"schema":{"type":"array"},"itemSchema":{"$ref":"#/components/schemas/Item"}}}}}}}},"components":{"schemas":{"Item":{},"Unused":{"$ref":"unused.json"}}},"webhooks":{"event":{"post":{"responses":{"200":{"description":"ok"}}}}}}`
	c := schema9Parse(t, doc, m)
	ops := c.Operations()
	if len(ops) != 1 {
		t.Fatalf("operations=%v", opKeys(ops))
	}
	op := mustOp(t, c, "x")
	if ops[0] != op || c.Operations()[0] != op {
		t.Error("operation handles are not shared")
	}
	if op.Err != nil {
		t.Fatalf("operation Err=%v", op.Err)
	}
	media := op.Body.Media[0]
	if len(media.Encoding) != 1 || len(op.Responses) != 1 || len(op.Responses[0].Headers) != 1 {
		t.Fatalf("incomplete descriptors: %+v", op)
	}
	for _, s := range []*openapi.Schema{op.Params[0].Schema, media.Schema, media.Encoding[0].Schema, op.Responses[0].Headers[0].Schema, op.Responses[0].Media[0].Schema, op.Responses[0].Media[0].ItemSchema} {
		if s == nil {
			t.Fatal("missing descriptor schema")
		}
		looked := schema9Get(t, c, s.Source())
		if !bytes.Equal(looked.Raw(), s.Raw()) || looked.Base() != s.Base() || looked.Dialect() != s.Dialect() {
			t.Errorf("descriptor and lookup disagree at %s", s.Source())
		}
		if !strings.HasPrefix(s.Source(), schema9Entry+"#/") {
			t.Errorf("Source uses canonical alias: %q", s.Source())
		}
	}
	if media.Encoding[0].Schema.Base() != "https://schemas.example.test/canonical/form.json" {
		t.Errorf("property Base=%q", media.Encoding[0].Schema.Base())
	}
	item := op.Responses[0].Media[0].ItemSchema
	schema9WantEdges(t, schema9Refs(t, item), []schema9Edge{{"/$ref", "$ref", "#/components/schemas/Item", alias + "#/components/schemas/Item", schema9Entry + "#/components/schemas/Item"}})
	wantURIs := []string{schema9Entry, external}
	if got := c.DocumentURIs(); !slices.Equal(got, wantURIs) {
		t.Errorf("inventory=%q want %q", got, wantURIs)
	}
	uris := c.DocumentURIs()
	uris[0] = "changed"
	if !slices.Equal(c.DocumentURIs(), wantURIs) {
		t.Error("inventory aliases earlier slice")
	}
	if !bytes.Equal(c.Document(alias), c.Document("")) {
		t.Error("$self inventory alias lost")
	}
	copy := c.Document(alias)
	copy[0] = '!'
	if !json.Valid(c.Document("")) {
		t.Error("document copy aliases inventory")
	}
}

// Runnable synthetic equivalent of Example_schemasAsWritten: copy each loaded
// document once, inspect authored response schemas and traverse graph edges.
// It deliberately requires no optional projection or validator dependency.
func TestSchema9FlowSchemasAsWritten(t *testing.T) {
	const external = "https://schemas.example.test/api/models.json"
	m := newMemFetch(map[string]string{external: `{"$defs":{"Photo":{"type":"string","contentMediaType":"image/png"}}}`})
	doc := `{"openapi":"3.1.2","info":{"title":"Photos","version":"1"},"paths":{"/photo":{"get":{"operationId":"getPetPhoto","responses":{"200":{"description":"A photo","content":{"application/json":{"schema":{"$ref":"models.json#/$defs/Photo","description":"authored use"}}}}}}}}}`
	c := schema9Parse(t, doc, m)
	documents := map[string][]byte{}
	for _, uri := range c.DocumentURIs() {
		documents[uri] = c.Document(uri)
	}
	if len(documents) != 2 || !json.Valid(documents[external]) {
		t.Fatalf("documents=%v", documents)
	}
	op := mustOp(t, c, "getPetPhoto")
	if len(op.Responses) != 1 || len(op.Responses[0].Media) != 1 {
		t.Fatal("response schema missing")
	}
	s := op.Responses[0].Media[0].Schema
	var authored struct {
		Ref         string `json:"$ref"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(s.Raw(), &authored); err != nil {
		t.Fatal(err)
	}
	if authored.Ref != "models.json#/$defs/Photo" || authored.Description != "authored use" || s.Base() != schema9Entry || s.Dialect() != schema9OAS31 {
		t.Errorf("authored schema=%+v base=%s dialect=%s", authored, s.Base(), s.Dialect())
	}
	r := schema9Refs(t, s)
	schema9WantEdges(t, r, []schema9Edge{{"/$ref", "$ref", authored.Ref, external + "#/$defs/Photo", external + "#/$defs/Photo"}})
	if string(r[0].Target.Raw()) != `{"type":"string","contentMediaType":"image/png"}` {
		t.Errorf("target Raw=%s", r[0].Target.Raw())
	}
	if follow := schema9Refs(t, r[0].Target); len(follow) != 0 {
		t.Errorf("terminal refs=%+v", follow)
	}
}

// Synthesis specifies fields, not their object member order.
func schema9SameValue(t testing.TB, label string, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s: got %s want %s", label, got, want)
	}
}

// Operation and Client.Operation error identity; listing does no schema work.
// A broken required Parameter Reference prevents preparation and retains its
// retrieval cause; schema graph failures alone do not disable an operation.
func TestSchema9DescriptorErrorIdentityAndLazySchemas(t *testing.T) {
	cause := errors.New("synthetic required parameter retrieval")
	m := newMemFetch(nil)
	m.errs["https://schemas.example.test/api/parameter.json"] = cause
	doc := `{"openapi":"3.1.2","info":{"title":"Lazy","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/bad":{"get":{"operationId":"bad","parameters":[{"$ref":"parameter.json"}]}},"/good":{"get":{"operationId":"good","parameters":[{"name":"q","in":"query","schema":{"$schema":"https://dialects.example.test/custom","$id":"ignored.json","$ref":"never.json"}}],"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Missing"}}}}}}}}}`
	c := schema9Parse(t, doc, m)
	ops := c.Operations()
	if len(ops) != 2 {
		t.Fatalf("operations=%v", opKeys(ops))
	}
	bad, good := mustOp(t, c, "bad"), mustOp(t, c, "good")
	if bad != ops[0] || good != ops[1] {
		t.Error("lookup did not retain shared descriptors")
	}
	if !errors.Is(bad.Err, cause) || !errors.Is(bad.Err, openapi.ErrUnresolved) {
		t.Errorf("bad.Err=%v", bad.Err)
	}
	if req, err := c.Prepare("bad", nil); req != nil || !errors.Is(err, bad.Err) || !errors.Is(err, cause) {
		t.Errorf("Prepare did not wrap descriptor identity: %v %v", req, err)
	}
	if good.Err != nil || good.Params[0].Err != nil || good.Responses[0].Err != nil {
		t.Errorf("schema graph disabled otherwise usable operation: %+v", good)
	}
	if _, err := c.Prepare("good", nil); err != nil {
		t.Errorf("schema inspection required for call: %v", err)
	}
	if m.callsTo("https://schemas.example.test/api/never.json") != 0 {
		t.Error("opaque schema fetched during description")
	}
}

// SchemaReference URI semantics apply to discriminator values within nested
// resources too. OAS 3.2.1 Discriminator Object + Core 8.2.1: URI mappings
// use the effective $id base while component names use the entry document.
func TestSchema9DiscriminatorNestedBase(t *testing.T) {
	const target = "https://schemas.example.test/api/models/cat.json"
	m := newMemFetch(map[string]string{target: `{"Cat":{"type":"object"}}`})
	c := schema9Parse(t, schema9Doc("3.2.1", `"S":{"$id":"models/union.json","oneOf":[{"type":"object"}],"discriminator":{"propertyName":"kind","mapping":{"dog":"Dog"},"defaultMapping":"./cat.json#/Cat"}},"Dog":{"type":"object"}`), m)
	s := schema9Get(t, c, schema9Entry+"#/components/schemas/S")
	schema9WantEdges(t, schema9Refs(t, s), []schema9Edge{
		{"/discriminator/mapping/dog", "mapping", "Dog", schema9Entry + "#/components/schemas/Dog", schema9Entry + "#/components/schemas/Dog"},
		{"/discriminator/defaultMapping", "defaultMapping", "./cat.json#/Cat", target + "#/Cat", target + "#/Cat"},
	})
	if calls := m.callList(); !slices.Equal(calls, []string{target}) {
		t.Errorf("nested discriminator fetches=%q", calls)
	}
}
