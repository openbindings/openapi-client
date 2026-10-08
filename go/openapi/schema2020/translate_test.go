package schema2020_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// Keyword translation in Swagger 2.0 and OpenAPI 3.0. Project: "In Swagger 2.0
// and OpenAPI 3.0, members beside a reported $ref are dropped. type: string
// with format: binary loses both keywords, and Swagger 2.0 type: file loses its
// type and any format other than byte; format: byte becomes contentEncoding:
// base64, which replaces any contentEncoding the schema has. A boolean
// exclusiveMinimum or exclusiveMaximum that is true becomes exclusiveMinimum or
// exclusiveMaximum with the value of minimum or maximum, which is removed; one
// that is false or has no bound is removed. In OpenAPI 3.0, nullable is removed
// and, where the schema then has a type, true adds "null" to it as a type list
// unless it already allows null; enum is unchanged. ... Every other keyword is
// kept as an annotation. Project never validates an instance, invents a fact or
// repairs a schema." A schema these rules leave with no keywords compares equal
// to true.

type translation struct {
	name     string
	editions []string // nil means 2.0 and 3.0
	authored string   // a schema; "#C/Plain" names {"type":"string"}
	want     string
}

var translations = []translation{
	// OAS 3.0.4 section 4.7.23, Reference Object: "any properties added
	// SHALL be ignored".
	{name: "members beside $ref", authored: `{"properties":{"a":{"$ref":"#C/Plain","description":"d","readOnly":true,"x-y":1,"type":"integer"}}}`,
		want: `{"properties":{"a":{"$ref":"=>#C/Plain"}}}`},
	{name: "beside $ref in items", authored: `{"type":"array","items":{"$ref":"#C/Plain","maxLength":2}}`,
		want: `{"type":"array","items":{"$ref":"=>#C/Plain"}}`},

	// JSON Schema Wright-00 boolean bounds (OAS 3.0.4 section 4.7.24.1)
	// to JSON Schema 2020-12 numeric ones (Validation section 6.2).
	{name: "exclusiveMinimum true", authored: `{"type":"number","minimum":1.50,"exclusiveMinimum":true}`,
		want: `{"type":"number","exclusiveMinimum":1.5}`},
	{name: "exclusiveMaximum true", authored: `{"type":"integer","maximum":10,"exclusiveMaximum":true}`,
		want: `{"type":"integer","exclusiveMaximum":10}`},
	{name: "both bounds true", authored: `{"minimum":-1e2,"exclusiveMinimum":true,"maximum":0,"exclusiveMaximum":true}`,
		want: `{"exclusiveMinimum":-100,"exclusiveMaximum":0}`},
	{name: "bounds false", authored: `{"type":"integer","minimum":0,"exclusiveMinimum":false,"maximum":9,"exclusiveMaximum":false}`,
		want: `{"type":"integer","minimum":0,"maximum":9}`},
	{name: "true without bound", authored: `{"type":"integer","exclusiveMinimum":true,"exclusiveMaximum":true}`,
		want: `{"type":"integer"}`},
	{name: "bounds nested", authored: `{"type":"object","properties":{"n":{"type":"number","minimum":0,"exclusiveMinimum":true}},
		"additionalProperties":{"maximum":5,"exclusiveMaximum":true},"allOf":[{"minimum":2,"exclusiveMinimum":false}]}`,
		want: `{"type":"object","properties":{"n":{"type":"number","exclusiveMinimum":0}},"additionalProperties":{"exclusiveMaximum":5},"allOf":[{"minimum":2}]}`},

	// OAS 3.0.4 section 4.7.24.2, nullable: "A true value indicates that
	// both null values and values of the type specified by type are
	// allowed. Other Schema Object constraints retain their defined
	// behavior".
	{name: "nullable true", editions: []string{v30}, authored: `{"type":"string","nullable":true}`,
		want: `{"type":["string","null"]}`},
	{name: "nullable object", editions: []string{v30}, authored: `{"type":"object","nullable":true,"properties":{"a":{"type":"integer","nullable":true}}}`,
		want: `{"type":["object","null"],"properties":{"a":{"type":["integer","null"]}}}`},
	{name: "nullable false", editions: []string{v30}, authored: `{"type":"string","nullable":false}`,
		want: `{"type":"string"}`},
	{name: "nullable without type", editions: []string{v30}, authored: `{"nullable":true,"description":"any"}`,
		want: `{"description":"any"}`},
	{name: "nullable keeps enum", editions: []string{v30}, authored: `{"type":"string","nullable":true,"enum":["a","b"]}`,
		want: `{"type":["string","null"],"enum":["a","b"]}`},
	{name: "nullable with null in enum", editions: []string{v30}, authored: `{"type":"string","nullable":true,"enum":["a",null]}`,
		want: `{"type":["string","null"],"enum":["a",null]}`},
	{name: "nullable binary", editions: []string{v30}, authored: `{"type":"string","format":"binary","nullable":true,"description":"d"}`,
		want: `{"description":"d"}`},
	{name: "nullable byte", editions: []string{v30}, authored: `{"type":"string","format":"byte","nullable":true}`,
		want: `{"type":["string","null"],"contentEncoding":"base64"}`},
	{name: "nullable is not a 2.0 keyword", editions: []string{v20}, authored: `{"type":"string","nullable":true,"x-nullable":true}`,
		want: `{"type":"string","nullable":true,"x-nullable":true}`},
	{name: "nothing left", authored: `{"exclusiveMinimum":true,"exclusiveMaximum":false}`, want: `{}`},
	// Project: true adds "null" to the type "unless it already allows
	// null". A type list is written as authored, though OpenAPI 3.0 asks
	// for a string (section 4.7.24.1), since Project never repairs a schema.
	{name: "nullable type list", editions: []string{v30}, authored: `{"type":["string","integer"],"nullable":true}`,
		want: `{"type":["string","integer","null"]}`},
	{name: "nullable type null", editions: []string{v30}, authored: `{"type":"null","nullable":true}`, want: `{"type":"null"}`},
	{name: "nullable type list with null", editions: []string{v30}, authored: `{"type":["string","null"],"nullable":true}`,
		want: `{"type":["string","null"]}`},
	{name: "nullable false type list", editions: []string{v30}, authored: `{"type":["string","integer"],"nullable":false}`,
		want: `{"type":["string","integer"]}`},
	{name: "nullable in items", editions: []string{v30}, authored: `{"type":"array","items":{"type":"boolean","nullable":true}}`,
		want: `{"type":"array","items":{"type":["boolean","null"]}}`},

	// OAS 3.0.4 section 4.4.1 and OAS 2.0 section 6.3, formats binary
	// ("any sequence of octets") and byte ("base64 encoded characters");
	// OAS 2.0 section 6.3, the "file" type.
	{name: "binary", authored: `{"type":"string","format":"binary"}`, want: `{}`},
	{name: "binary keeps the rest", authored: `{"type":"string","format":"binary","description":"d","maxLength":5}`,
		want: `{"description":"d","maxLength":5}`},
	{name: "binary in items", authored: `{"type":"array","items":{"type":"string","format":"binary"}}`,
		want: `{"type":"array","items":{}}`},
	{name: "file", editions: []string{v20}, authored: `{"type":"file","description":"upload"}`, want: `{"description":"upload"}`},
	{name: "byte", authored: `{"type":"string","format":"byte"}`, want: `{"type":"string","contentEncoding":"base64"}`},
	{name: "byte without type", authored: `{"format":"byte","maxLength":8}`, want: `{"contentEncoding":"base64","maxLength":8}`},
	{name: "byte with another type", authored: `{"type":"integer","format":"byte"}`, want: `{"type":"integer","contentEncoding":"base64"}`},
	{name: "byte keeps the rest", authored: `{"type":"string","format":"byte","maxLength":8,"pattern":"^[A-Za-z0-9+/=]*$"}`,
		want: `{"type":"string","contentEncoding":"base64","maxLength":8,"pattern":"^[A-Za-z0-9+/=]*$"}`},

	// Every other keyword is kept.
	{name: "annotations kept", authored: `{"type":"string","format":"date-time","title":"t","description":"d","default":"x","example":"y",
		"xml":{"name":"n"},"externalDocs":{"url":"https://docs.example.test"},"readOnly":true,"x-ext":{"a":[1,2]},"unknownKeyword":5}`,
		want: `{"type":"string","format":"date-time","title":"t","description":"d","default":"x","example":"y",
		"xml":{"name":"n"},"externalDocs":{"url":"https://docs.example.test"},"readOnly":true,"x-ext":{"a":[1,2]},"unknownKeyword":5}`},
	{name: "deprecated and writeOnly kept", editions: []string{v30}, authored: `{"type":"string","deprecated":true,"writeOnly":true}`,
		want: `{"type":"string","deprecated":true,"writeOnly":true}`},
	{name: "validation kept", authored: `{"type":"object","required":["a"],"minProperties":1,"maxProperties":3,"additionalProperties":false,
		"properties":{"a":{"type":"string","minLength":1,"maxLength":4,"pattern":"^a"},"b":{"type":"array","minItems":1,"maxItems":2,"uniqueItems":true,"items":{"type":"integer","multipleOf":3,"enum":[3,6]}}}}`,
		want: `{"type":"object","required":["a"],"minProperties":1,"maxProperties":3,"additionalProperties":false,
		"properties":{"a":{"type":"string","minLength":1,"maxLength":4,"pattern":"^a"},"b":{"type":"array","minItems":1,"maxItems":2,"uniqueItems":true,"items":{"type":"integer","multipleOf":3,"enum":[3,6]}}}}`},
	{name: "composition kept", editions: []string{v30}, authored: `{"oneOf":[{"type":"string"},{"type":"integer"}],"anyOf":[{"minimum":0}],"not":{"type":"boolean"}}`,
		want: `{"oneOf":[{"type":"string"},{"type":"integer"}],"anyOf":[{"minimum":0}],"not":{"type":"boolean"}}`},
}

func TestProjectTranslation(t *testing.T) {
	for _, tr := range translations {
		eds := tr.editions
		if eds == nil {
			eds = legacyEditions
		}
		for _, v := range eds {
			for _, f := range formats {
				t.Run(tr.name+"/"+v+"/"+f, func(t *testing.T) {
					c := load(t, f, docText(v, "", `"S":`+tr.authored+`,"Plain":{"type":"string"}`), nil)
					for _, d := range directions {
						p := project(t, c, comp(t, c, v, "S"), d)
						w := want{root: tr.want}
						if strings.Contains(tr.want, "=>#C/Plain") {
							w.defs = map[string]string{"#C/Plain": `{"type":"string"}`}
						}
						edAll(v, w).check(t, c, p)
						noContentMediaType(t, p)
					}
				})
			}
		}
	}
}

// In OpenAPI 3.1 and 3.2 the same keywords are the authored schema's own:
// nothing is translated, siblings of $ref stay, and a 3.0 spelling such as
// nullable or a boolean exclusiveMinimum stays as written, since Project
// never repairs a schema.
func TestProjectNoTranslationModern(t *testing.T) {
	for _, tr := range translations {
		for _, v := range modernEditions {
			t.Run(tr.name+"/"+v, func(t *testing.T) {
				c := load(t, "json", docText(v, "", `"S":`+tr.authored+`,"Plain":{"type":"string"}`), nil)
				p := project(t, c, comp(t, c, v, "S"), schema2020.Request)
				w := want{root: strings.ReplaceAll(tr.authored, "#C/Plain", "=>#C/Plain")}
				if strings.Contains(tr.authored, "#C/Plain") {
					w.defs = map[string]string{"#C/Plain": `{"type":"string"}`}
				}
				edAll(v, w).check(t, c, p)
			})
		}
	}
}

// noContentMediaType fails if contentMediaType appears anywhere in p:
// Project adds none, at Root or in Defs.
func noContentMediaType(t testing.TB, p *schema2020.Projection) {
	t.Helper()
	if bytes.Contains(p.Root, []byte("contentMediaType")) {
		t.Errorf("Root carries contentMediaType: %s", p.Root)
	}
	for k, d := range p.Defs {
		if bytes.Contains(d, []byte("contentMediaType")) {
			t.Errorf("Defs[%q] carries contentMediaType: %s", k, d)
		}
	}
}

// Binary content at Root and in multipart properties: in OpenAPI 3.0 the
// binary schema loses type and format at Root, in each multipart property,
// and in each Encoding field's own schema, while a byte property gains
// contentEncoding; nothing gains contentMediaType. In OpenAPI 3.1 and 3.2
// the authored schema is kept, contentMediaType included where written.
func TestProjectBinaryContent(t *testing.T) {
	paths := `"/files":{"post":{
		"requestBody":{"content":{
			"application/octet-stream":{"schema":{"type":"string","format":"binary"}},
			"multipart/form-data":{"schema":{"type":"object","required":["file"],"properties":{
				"file":{"type":"string","format":"binary"},
				"meta":{"type":"string","format":"byte"},
				"files":{"type":"array","items":{"type":"string","format":"binary"}},
				"pet":{"$ref":"#C/Upload"}}}}}},
		"responses":{"200":{"description":"ok","content":{"image/png":{"schema":{"type":"string","format":"binary","maxLength":1048576}}}}}}}`
	schemas := `"Upload":{"type":"object","properties":{"blob":{"type":"string","format":"binary"},"note":{"type":"string"}}}`
	for _, v := range []string{v30, v31, v32} {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, paths, schemas), nil)
				op, err := c.Operation("POST /files")
				if err != nil {
					t.Fatal(err)
				}
				byType := map[string]*openapi.Media{}
				for _, m := range op.Body.Media {
					byType[m.Type] = m
				}
				octet, multipart := byType["application/octet-stream"], byType["multipart/form-data"]
				png := op.Responses[0].Media[0]
				if octet == nil || multipart == nil || png.Type != "image/png" {
					t.Fatalf("media %v %v %v", octet, multipart, png)
				}
				wants := map[*openapi.Schema]want{}
				if isModern(v) {
					wants[octet.Schema] = want{root: `{"type":"string","format":"binary"}`}
					wants[png.Schema] = want{root: `{"type":"string","format":"binary","maxLength":1048576}`}
					wants[multipart.Schema] = edAll(v, want{
						root: `{"type":"object","required":["file"],"properties":{"file":{"type":"string","format":"binary"},"meta":{"type":"string","format":"byte"},
							"files":{"type":"array","items":{"type":"string","format":"binary"}},"pet":{"$ref":"=>#C/Upload"}}}`,
						defs: map[string]string{"#C/Upload": `{"type":"object","properties":{"blob":{"type":"string","format":"binary"},"note":{"type":"string"}}}`},
					})
				} else {
					wants[octet.Schema] = want{root: `{}`}
					wants[png.Schema] = want{root: `{"maxLength":1048576}`}
					wants[multipart.Schema] = edAll(v, want{
						root: `{"type":"object","required":["file"],"properties":{"file":{},"meta":{"type":"string","contentEncoding":"base64"},
							"files":{"type":"array","items":{}},"pet":{"$ref":"=>#C/Upload"}}}`,
						defs: map[string]string{"#C/Upload": `{"type":"object","properties":{"blob":{},"note":{"type":"string"}}}`},
					})
				}
				fields := map[string]string{"file": `{}`, "meta": `{"type":"string","contentEncoding":"base64"}`}
				if isModern(v) {
					fields = map[string]string{"file": `{"type":"string","format":"binary"}`, "meta": `{"type":"string","format":"byte"}`}
				}
				for _, field := range multipart.Encoding {
					if w, ok := fields[field.Name]; ok && field.Schema != nil {
						wants[field.Schema] = want{root: w}
					}
				}
				if len(wants) != 5 {
					t.Fatalf("found %d schemas, want 5 (Encoding %v)", len(wants), multipart.Encoding)
				}
				for s, w := range wants {
					for _, d := range directions {
						p := project(t, c, s, d)
						w.check(t, c, p)
						if !isModern(v) {
							noContentMediaType(t, p)
						}
					}
				}
			})
		}
	}
	t.Run("authored contentMediaType", func(t *testing.T) {
		c := load(t, "json", docText(v31, `"/f":{"put":{"requestBody":{"content":{"image/png":{"schema":{"type":"string","contentMediaType":"image/png","contentEncoding":"base64"}}}},"responses":{"204":{"description":"none"}}}}`, ""), nil)
		op, err := c.Operation("PUT /f")
		if err != nil {
			t.Fatal(err)
		}
		p := project(t, c, op.Body.Media[0].Schema, schema2020.Request)
		want{root: `{"type":"string","contentMediaType":"image/png","contentEncoding":"base64"}`}.check(t, c, p)
	})
}

// Swagger 2.0 schemas made from parameters (Param.Schema: "one made from
// every schema field it declares") and from formData (Media.Schema: "an
// object whose properties are the fields and whose required list names
// the required ones") are translated as any Swagger 2.0 schema: a file
// field and a binary field lose type and format, a byte value gains
// contentEncoding, and boolean bounds become numeric, at Root, in items and
// in the formData object's properties. Each formData field's own schema is
// projected the same way.
func TestProjectSwaggerParameterSchemas(t *testing.T) {
	paths := `
		"/items/{id}":{"get":{"produces":["application/json"],"parameters":[
			{"name":"id","in":"path","required":true,"type":"string","format":"byte"},
			{"name":"tags","in":"query","type":"array","items":{"type":"integer","minimum":1,"exclusiveMinimum":true},"collectionFormat":"csv"},
			{"name":"X-Limit","in":"header","type":"integer","maximum":100,"exclusiveMaximum":true,"minimum":0}],
			"responses":{"204":{"description":"none"}}},
		"post":{"consumes":["multipart/form-data"],"parameters":[
			{"name":"id","in":"path","required":true,"type":"string"},
			{"name":"file","in":"formData","type":"file","required":true},
			{"name":"raw","in":"formData","type":"string","format":"binary"},
			{"name":"sig","in":"formData","type":"string","format":"byte","required":true},
			{"name":"n","in":"formData","type":"number","minimum":0,"exclusiveMinimum":true}],
			"responses":{"204":{"description":"none"}}}}`
	for _, f := range formats {
		t.Run(f, func(t *testing.T) {
			c := load(t, f, docText(v20, paths, ""), nil)
			get, err := c.Operation("GET /items/{id}")
			if err != nil {
				t.Fatal(err)
			}
			params := map[string]string{
				"id":      `{"type":"string","contentEncoding":"base64"}`,
				"tags":    `{"type":"array","items":{"type":"integer","exclusiveMinimum":1}}`,
				"X-Limit": `{"type":"integer","exclusiveMaximum":100,"minimum":0}`,
			}
			for _, prm := range get.Params {
				w, ok := params[prm.Name]
				if !ok || prm.Schema == nil {
					t.Fatalf("parameter %s: schema %v", prm.Name, prm.Schema)
				}
				for _, d := range directions {
					p := project(t, c, prm.Schema, d)
					want{root: w}.check(t, c, p)
					if len(p.Defs) != 0 {
						t.Errorf("%s: Defs %v", prm.Name, p.Sources)
					}
				}
			}

			post, err := c.Operation("POST /items/{id}")
			if err != nil {
				t.Fatal(err)
			}
			if post.Body == nil || len(post.Body.Media) != 1 {
				t.Fatalf("formData body %+v", post.Body)
			}
			m := post.Body.Media[0]
			fieldWant := map[string]string{
				"file": `{}`,
				"raw":  `{}`,
				"sig":  `{"type":"string","contentEncoding":"base64"}`,
				"n":    `{"type":"number","exclusiveMinimum":0}`,
			}
			raw := mustDecode(t, "formData Raw", m.Schema.Raw()).(map[string]any)
			props := map[string]any{}
			for name, w := range fieldWant {
				props[name] = mustDecode(t, name, []byte(w))
			}
			raw["properties"] = props
			for _, d := range directions {
				p := project(t, c, m.Schema, d)
				sameSchema(t, "formData Root", p.Root, raw)
				noContentMediaType(t, p)
			}
			if len(m.Encoding) != len(fieldWant) {
				t.Fatalf("%d formData fields, want %d", len(m.Encoding), len(fieldWant))
			}
			for _, field := range m.Encoding {
				p := project(t, c, field.Schema, schema2020.Request)
				want{root: fieldWant[field.Name]}.check(t, c, p)
			}
		})
	}
}

// A Swagger 2.0 formData object (Media.Schema: "an object whose properties
// are the fields and whose required list names the required ones"; its
// Source "is the Operation's Source", Schema.Source) projects like any
// Swagger 2.0 schema, in every direction, with nothing in Defs.
func TestProjectSwaggerFormData(t *testing.T) {
	paths := `"/files/{id}":{
		"parameters":[{"name":"id","in":"path","required":true,"type":"string"},{"name":"note","in":"formData","type":"string","maxLength":10,"default":"x"}],
		"post":{"consumes":["multipart/form-data","application/x-www-form-urlencoded"],"parameters":[
			{"name":"file","in":"formData","type":"file","required":true},
			{"name":"ids","in":"formData","type":"array","items":{"type":"integer","minimum":1,"exclusiveMinimum":true},"collectionFormat":"multi"},
			{"name":"sig","in":"formData","type":"string","format":"byte","required":true}],
			"responses":{"204":{"description":"none"}}}}`
	fields := map[string]string{
		"file": `{}`,
		"ids":  `{"type":"array","items":{"type":"integer","exclusiveMinimum":1}}`,
		"sig":  `{"type":"string","contentEncoding":"base64"}`,
		"note": `{"type":"string","maxLength":10,"default":"x"}`,
	}
	for _, f := range formats {
		t.Run(f, func(t *testing.T) {
			c := load(t, f, docText(v20, paths, ""), nil)
			op, err := c.Operation("POST /files/{id}")
			if err != nil {
				t.Fatal(err)
			}
			if op.Body == nil || len(op.Body.Media) != 2 {
				t.Fatalf("formData body %+v", op.Body)
			}
			for _, m := range op.Body.Media {
				raw := mustDecode(t, "formData Raw", m.Schema.Raw()).(map[string]any)
				props := map[string]any{}
				for name, w := range fields {
					props[name] = mustDecode(t, name, []byte(w))
				}
				raw["properties"] = props
				for _, d := range directions {
					p := project(t, c, m.Schema, d)
					sameSchema(t, m.Type+" formData Root", p.Root, raw)
					if len(p.Defs) != 0 {
						t.Errorf("%s: Defs %v", m.Type, p.Sources)
					}
				}
			}
		})
	}
}
