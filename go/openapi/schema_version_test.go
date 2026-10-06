package openapi_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Schema.Version on every kind of handle: "the swagger or openapi value at
// the root of its document or, for a referenced document that declares no
// version this client reads, the entry document's (see Client.Version and
// Loader). Raw and Dialect are read under that edition's rules."

const (
	verEntry   = "https://versions.example.test/api/openapi.json"
	verOAS31   = "https://spec.openapis.org/oas/3.1/dialect/base"
	verOAS32   = "https://spec.openapis.org/oas/3.2/dialect/2025-09-17"
	verDocBase = "https://versions.example.test/api/"
)

func verParse(t testing.TB, doc string, others map[string]string, yaml bool) *openapi.Client {
	t.Helper()
	content := []byte(doc)
	if yaml {
		content = toYAML(t, content, false, yamlStyle{})
		converted := make(map[string]string, len(others))
		for uri, d := range others {
			converted[uri] = string(toYAML(t, []byte(d), false, yamlStyle{}))
		}
		others = converted
	}
	c, err := (&openapi.Loader{Fetch: newMemFetch(others).fetch}).Parse(t.Context(), content, verEntry, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// verDescriptorSchemas lists every schema handle the descriptions expose,
// labelled by where it was found.
func verDescriptorSchemas(c *openapi.Client) map[string]*openapi.Schema {
	out := map[string]*openapi.Schema{}
	add := func(label string, s *openapi.Schema) {
		if s != nil {
			out[label] = s
		}
	}
	params := func(prefix string, ps []*openapi.Param) {
		for _, p := range ps {
			add(prefix+" param "+p.In+" "+p.Name, p.Schema)
			for _, h := range p.Headers {
				add(prefix+" field "+p.Name+" header "+h.Name, h.Schema)
			}
		}
	}
	media := func(prefix string, ms []*openapi.Media) {
		for _, m := range ms {
			add(prefix+" "+m.Type+" schema", m.Schema)
			add(prefix+" "+m.Type+" itemSchema", m.ItemSchema)
			params(prefix+" "+m.Type+" encoding", m.Encoding)
		}
	}
	for _, op := range c.Operations() {
		params(op.Key, op.Params)
		if op.Body != nil {
			media(op.Key+" body", op.Body.Media)
		}
		for _, r := range op.Responses {
			media(op.Key+" response "+r.Key, r.Media)
			params(op.Key+" response "+r.Key+" header", r.Headers)
		}
	}
	return out
}

// verDocument returns a document of version v declaring every kind of
// descriptor schema the edition has.
func verDocument(v string) string {
	if v == "2.0" {
		return `{"swagger":"2.0","info":{"title":"V","version":"1"},"host":"api.example.test",
			"paths":{
				"/items/{id}":{
					"put":{"consumes":["application/json"],"produces":["application/json"],"parameters":[
						{"name":"id","in":"path","required":true,"type":"string","format":"byte"},
						{"name":"tags","in":"query","type":"array","items":{"type":"integer","minimum":1,"exclusiveMinimum":true}},
						{"name":"X-Trace","in":"header","type":"string"},
						{"name":"item","in":"body","schema":{"$ref":"#/definitions/Item"}}],
						"responses":{"200":{"description":"ok","schema":{"type":"array","items":{"$ref":"#/definitions/Item"}},
							"headers":{"X-Rate":{"type":"integer"}}}}},
					"post":{"consumes":["multipart/form-data"],"parameters":[
						{"name":"id","in":"path","required":true,"type":"string"},
						{"name":"file","in":"formData","type":"file","required":true},
						{"name":"note","in":"formData","type":"string","format":"binary"}],
						"responses":{"204":{"description":"none"}}}}},
			"definitions":{"Item":{"type":"object","properties":{"id":{"type":"string","readOnly":true}}}}}`
	}
	doc := `{"openapi":"` + v + `","info":{"title":"V","version":"1"},
		"paths":{"/items/{id}":{"put":{
			"parameters":[
				{"name":"id","in":"path","required":true,"schema":{"type":"string"}},
				{"name":"filter","in":"query","content":{"application/json":{"schema":{"type":"object"}}}}],
			"requestBody":{"content":{
				"application/json":{"schema":{"$ref":"#/components/schemas/Item"}},
				"multipart/form-data":{"schema":{"type":"object","properties":{"file":{"type":"string","format":"binary"},"meta":{"$ref":"#/components/schemas/Item"}}},
					"encoding":{"file":{"headers":{"X-Part":{"schema":{"type":"integer"}}}}}}`
	if strings.HasPrefix(v, "3.2") {
		doc += `,"application/jsonl":{"itemSchema":{"$ref":"#/components/schemas/Item"}}`
	}
	doc += `}},
			"responses":{"200":{"description":"ok",
				"headers":{"X-Rate":{"schema":{"type":"integer"}}},
				"content":{"application/json":{"schema":{"type":"array","items":{"$ref":"#/components/schemas/Item"}}}}}}}}},
		"components":{"schemas":{"Item":{"type":"object","properties":{"id":{"type":"string","readOnly":true}}}}}}`
	return doc
}

// Every descriptor schema of a document has the version its document
// declares, in every edition and whether the document is JSON or YAML.
// A Swagger 2.0 parameter-made schema and formData schema included (Param.Schema
// and Media.Schema: "one made from every schema field it declares";
// "For Swagger 2.0 formData, it is an object whose properties are the
// fields").
func TestSchemaVersionDescriptors(t *testing.T) {
	for _, v := range []string{"2.0", "3.0.0", "3.0.4", "3.1.0", "3.1.2", "3.2.0", "3.2.1"} {
		for _, yaml := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/yaml=%v", v, yaml), func(t *testing.T) {
				c := verParse(t, verDocument(v), nil, yaml)
				if c.Version() != v {
					t.Fatalf("Client.Version() = %q, want %q", c.Version(), v)
				}
				schemas := verDescriptorSchemas(c)
				min := 9
				if v == "2.0" {
					min = 10
				}
				if len(schemas) < min {
					t.Fatalf("found %d descriptor schemas, want at least %d: %v", len(schemas), min, verLabels(schemas))
				}
				for label, s := range schemas {
					if got := s.Version(); got != v {
						t.Errorf("%s (%s): Version() = %q, want %q", label, s.Source(), got, v)
					}
				}
				comp := verEntry + "#/components/schemas/Item"
				if v == "2.0" {
					comp = verEntry + "#/definitions/Item"
				}
				for _, uri := range []string{comp, comp + "/properties/id"} {
					s, err := c.Schema(uri)
					if err != nil {
						t.Fatalf("Schema(%q): %v", uri, err)
					}
					if s.Version() != v {
						t.Errorf("Schema(%q).Version() = %q, want %q", uri, s.Version(), v)
					}
				}
			})
		}
	}
}

func verLabels(m map[string]*openapi.Schema) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The Swagger 2.0 handles that no Schema Object in the document holds:
// each parameter-made schema, the formData object, and each formData
// field's schema, have the version of the document declaring the
// parameters. A parameter declared in a referenced document that declares
// no version takes the entry document's ("2.0").
func TestSchemaVersionSwaggerParameterSchemas(t *testing.T) {
	others := map[string]string{
		verDocBase + "params.json": `{"Limit":{"name":"limit","in":"query","type":"integer","maximum":100,"exclusiveMaximum":true},
			"Upload":{"name":"upload","in":"formData","type":"file"}}`,
	}
	doc := `{"swagger":"2.0","info":{"title":"V","version":"1"},"host":"api.example.test","paths":{
		"/a":{"get":{"parameters":[{"$ref":"params.json#/Limit"},{"name":"q","in":"query","type":"string"}],"responses":{"204":{"description":"none"}}}},
		"/b":{"post":{"consumes":["application/x-www-form-urlencoded","multipart/form-data"],
			"parameters":[{"$ref":"params.json#/Upload"},{"name":"n","in":"formData","type":"number","minimum":0}],
			"responses":{"204":{"description":"none"}}}}}}`
	for _, yaml := range []bool{false, true} {
		t.Run(fmt.Sprintf("yaml=%v", yaml), func(t *testing.T) {
			c := verParse(t, doc, others, yaml)
			get := mustOp(t, c, "GET /a")
			if len(get.Params) != 2 {
				t.Fatalf("GET /a has %d params", len(get.Params))
			}
			for _, p := range get.Params {
				if p.Schema == nil {
					t.Fatalf("param %s has no schema", p.Name)
				}
				if p.Schema.Version() != "2.0" {
					t.Errorf("param %s (%s): Version() = %q, want 2.0", p.Name, p.Schema.Source(), p.Schema.Version())
				}
			}
			post := mustOp(t, c, "POST /b")
			if post.Body == nil || len(post.Body.Media) == 0 {
				t.Fatal("POST /b has no formData body")
			}
			for _, m := range post.Body.Media {
				if m.Schema == nil {
					t.Fatalf("%s: no formData schema", m.Type)
				}
				if m.Schema.Version() != "2.0" {
					t.Errorf("%s formData schema: Version() = %q, want 2.0", m.Type, m.Schema.Version())
				}
				if len(m.Encoding) != 2 {
					t.Fatalf("%s: %d fields, want 2", m.Type, len(m.Encoding))
				}
				for _, f := range m.Encoding {
					if f.Schema == nil || f.Schema.Version() != "2.0" {
						t.Errorf("%s field %s: schema %v", m.Type, f.Name, f.Schema)
					}
				}
			}
		})
	}
}

// verOthers are referenced documents, each declaring an edition, none, or
// one the client does not read; every one keeps a schema at the same
// places so that any entry can refer to it.
var verOthers = map[string]string{
	verDocBase + "v20.json":   `{"swagger":"2.0","info":{"title":"o","version":"1"},"paths":{},"definitions":{"X":{"type":"object","properties":{"p":{"type":"integer"}}}}}`,
	verDocBase + "v30.json":   `{"openapi":"3.0.3","info":{"title":"o","version":"1"},"paths":{},"components":{"schemas":{"X":{"type":"object","properties":{"p":{"type":"integer"}}}}}}`,
	verDocBase + "v31.json":   `{"openapi":"3.1.0","info":{"title":"o","version":"1"},"paths":{},"components":{"schemas":{"X":{"$id":"https://versions.example.test/ids/x31.json","type":"object","properties":{"p":{"$anchor":"p31","type":"integer"}}}}}}`,
	verDocBase + "v32.json":   `{"openapi":"3.2.0","$self":"https://versions.example.test/self/v32.json","info":{"title":"o","version":"1"},"paths":{},"components":{"schemas":{"X":{"type":"object","properties":{"p":{"type":"integer"}}}}}}`,
	verDocBase + "plain.json": `{"components":{"schemas":{"X":{"type":"object","properties":{"p":{"type":"integer"}}}}}}`,
	verDocBase + "odd.json":   `{"openapi":"4.0.0","definitions":{"X":{"type":"object","properties":{"p":{"type":"integer"}}}},"components":{"schemas":{"X":{"type":"object","properties":{"p":{"type":"integer"}}}}}}`,
	verDocBase + "old.json":   `{"swagger":"1.2","definitions":{"X":{"type":"object","properties":{"p":{"type":"integer"}}}},"components":{"schemas":{"X":{"type":"object","properties":{"p":{"type":"integer"}}}}}}`,
}

// verOtherPaths says where each referenced document keeps X. A document
// declaring a version the client does not read is read under the entry's
// edition, so X is referred to where that edition keeps reusable schemas
// (an empty ptr).
var verOtherPaths = []struct{ file, ptr, declared string }{
	{"v20.json", "#/definitions/X", "2.0"},
	{"v30.json", "#/components/schemas/X", "3.0.3"},
	{"v31.json", "#/components/schemas/X", "3.1.0"},
	{"v32.json", "#/components/schemas/X", "3.2.0"},
	{"plain.json", "#/components/schemas/X", ""}, // declares no version
	{"odd.json", "", ""},                         // a version this client does not read
	{"old.json", "", ""},                         // a version this client does not read
}

// verPtr is where o keeps X for an entry of version entry.
func verPtr(entry string, o struct{ file, ptr, declared string }) string {
	switch {
	case o.ptr != "":
		return o.ptr
	case entry == "2.0":
		return "#/definitions/X"
	}
	return "#/components/schemas/X"
}

// verDialect is the Dialect a schema of edition v has when nothing
// declares one (Schema.Dialect).
func verDialect(v string) string {
	switch {
	case strings.HasPrefix(v, "3.1"):
		return verOAS31
	case strings.HasPrefix(v, "3.2"):
		return verOAS32
	}
	return ""
}

// A reference target, and a Client.Schema lookup, in another document has
// that document's version, or the entry document's when the document
// declares none this client reads; Dialect follows that version.
func TestSchemaVersionReferencedDocuments(t *testing.T) {
	for _, entry := range []string{"2.0", "3.0.4", "3.1.2", "3.2.1"} {
		for _, yaml := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/yaml=%v", entry, yaml), func(t *testing.T) {
				var props []string
				for i, o := range verOtherPaths {
					props = append(props, fmt.Sprintf(`"r%d":{"$ref":"%s%s"}`, i, o.file, verPtr(entry, o)))
				}
				root := `{"type":"object","properties":{` + strings.Join(props, ",") + `}}`
				var doc, rootURI string
				if entry == "2.0" {
					doc = `{"swagger":"2.0","info":{"title":"V","version":"1"},"paths":{},"definitions":{"R":` + root + `}}`
					rootURI = verEntry + "#/definitions/R"
				} else {
					doc = `{"openapi":"` + entry + `","info":{"title":"V","version":"1"},"paths":{},"components":{"schemas":{"R":` + root + `}}}`
					rootURI = verEntry + "#/components/schemas/R"
				}
				c := verParse(t, doc, verOthers, yaml)
				r, err := c.Schema(rootURI)
				if err != nil {
					t.Fatal(err)
				}
				if r.Version() != entry {
					t.Errorf("R: Version() = %q, want %q", r.Version(), entry)
				}
				refs, err := r.References()
				if err != nil {
					t.Fatal(err)
				}
				if len(refs) != len(verOtherPaths) {
					t.Fatalf("R has %d references, want %d", len(refs), len(verOtherPaths))
				}
				for i, o := range verOtherPaths {
					want := o.declared
					if want == "" {
						want = entry
					}
					ref := refs[i]
					if ref.Err != nil || ref.Target == nil {
						t.Errorf("%s: reference error %v", o.file, ref.Err)
						continue
					}
					uri := verDocBase + o.file + verPtr(entry, o)
					lookedUp, err := c.Schema(uri)
					if err != nil {
						t.Errorf("Schema(%q): %v", uri, err)
						continue
					}
					sub, err := c.Schema(uri + "/properties/p")
					if err != nil {
						t.Errorf("Schema(%q): %v", uri+"/properties/p", err)
						continue
					}
					for what, s := range map[string]*openapi.Schema{"reference target": ref.Target, "Client.Schema": lookedUp, "subschema": sub} {
						if s.Version() != want {
							t.Errorf("%s %s (%s): Version() = %q, want %q", o.file, what, s.Source(), s.Version(), want)
						}
						if s.Dialect() != verDialect(want) {
							t.Errorf("%s %s: Dialect() = %q, want %q (read under %s)", o.file, what, s.Dialect(), verDialect(want), want)
						}
					}
				}
			})
		}
	}
}

// Client.Schema reaches a schema by a $id resource URI, a plain-name
// $anchor and an OpenAPI 3.2 $self URI; each handle has its document's
// version, not the entry's.
func TestSchemaVersionLookupByIdentifier(t *testing.T) {
	doc := `{"openapi":"3.0.4","info":{"title":"V","version":"1"},"paths":{},"components":{"schemas":{
		"R":{"type":"object","properties":{"a":{"$ref":"v31.json#/components/schemas/X"},"b":{"$ref":"v32.json#/components/schemas/X"},"c":{"type":"string"}}}}}}`
	c := verParse(t, doc, verOthers, false)
	for uri, want := range map[string]string{
		"https://versions.example.test/ids/x31.json":                                     "3.1.0",
		"https://versions.example.test/ids/x31.json#p31":                                 "3.1.0",
		"https://versions.example.test/ids/x31.json#/properties/p":                       "3.1.0",
		"https://versions.example.test/self/v32.json#/components/schemas/X":              "3.2.0",
		"https://versions.example.test/self/v32.json#/components/schemas/X/properties/p": "3.2.0",
		verEntry + "#/components/schemas/R/properties/c":                                 "3.0.4",
	} {
		s, err := c.Schema(uri)
		if err != nil {
			t.Errorf("Schema(%q): %v", uri, err)
			continue
		}
		if s.Version() != want {
			t.Errorf("Schema(%q) (%s): Version() = %q, want %q", uri, s.Source(), s.Version(), want)
		}
	}
}

// Schema: "In Swagger 2.0 and OpenAPI 3.0 ... it follows $ref to a schema
// that is not only a $ref", and "Source identifies the resulting handle".
// The handle is then written in the referenced document, and Version is
// that document's. In OpenAPI 3.1 and 3.2 the handle stays at the site and
// keeps the entry's version, while its reference target has the other
// document's.
func TestSchemaVersionFollowedReference(t *testing.T) {
	cases := []struct {
		name, doc         string
		siteVersion       string
		targetVersion     string // the version of the reference's target, for 3.1 and 3.2
		followedSourceDoc string // the document a followed handle is written in
	}{
		{
			name: "3.0 to 3.1",
			doc: `{"openapi":"3.0.4","info":{"title":"V","version":"1"},"paths":{"/x":{"get":{"responses":{"200":{"description":"ok",
				"content":{"application/json":{"schema":{"$ref":"v31.json#/components/schemas/X"}}}}}}}}}`,
			siteVersion:       "3.1.0",
			followedSourceDoc: verDocBase + "v31.json",
		},
		{
			name: "2.0 to 3.0",
			doc: `{"swagger":"2.0","info":{"title":"V","version":"1"},"host":"api.example.test","produces":["application/json"],
				"paths":{"/x":{"get":{"responses":{"200":{"description":"ok","schema":{"$ref":"v30.json#/components/schemas/X"}}}}}}}`,
			siteVersion:       "3.0.3",
			followedSourceDoc: verDocBase + "v30.json",
		},
		{
			name: "3.1 to 3.0",
			doc: `{"openapi":"3.1.2","info":{"title":"V","version":"1"},"paths":{"/x":{"get":{"responses":{"200":{"description":"ok",
				"content":{"application/json":{"schema":{"$ref":"v30.json#/components/schemas/X"}}}}}}}}}`,
			siteVersion:   "3.1.2",
			targetVersion: "3.0.3",
		},
		{
			name: "3.2 to 2.0",
			doc: `{"openapi":"3.2.1","info":{"title":"V","version":"1"},"paths":{"/x":{"get":{"responses":{"200":{"description":"ok",
				"content":{"application/json":{"schema":{"$ref":"v20.json#/definitions/X"}}}}}}}}}`,
			siteVersion:   "3.2.1",
			targetVersion: "2.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := verParse(t, tc.doc, verOthers, false)
			m := responseMedia(t, mustOp(t, c, "GET /x"), 0, 0)
			s := m.Schema
			if s == nil {
				t.Fatal("no schema")
			}
			if s.Version() != tc.siteVersion {
				t.Errorf("Version() = %q, want %q (Source %s)", s.Version(), tc.siteVersion, s.Source())
			}
			if tc.followedSourceDoc != "" {
				if !strings.HasPrefix(s.Source(), tc.followedSourceDoc+"#") {
					t.Errorf("Source = %s, want a schema in %s", s.Source(), tc.followedSourceDoc)
				}
				return
			}
			refs, err := s.References()
			if err != nil || len(refs) != 1 || refs[0].Target == nil {
				t.Fatalf("References = %+v, %v", refs, err)
			}
			if got := refs[0].Target.Version(); got != tc.targetVersion {
				t.Errorf("target Version() = %q, want %q", got, tc.targetVersion)
			}
		})
	}
}
