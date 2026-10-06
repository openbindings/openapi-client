package jsonschematest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
	"github.com/openbindings/openapi-client/go/openapi/schema2020"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Projections checked by an independent JSON Schema 2020-12 evaluator:
// each compiles, which includes validation against the 2020-12
// meta-schema (Projection: "a composable JSON Schema 2020-12 view"), and
// gives the instance verdicts the OpenAPI editions call for.

const entryURI = "https://api.example.test/v1/openapi.json"

var directions = []schema2020.Direction{schema2020.Neutral, schema2020.Request, schema2020.Response}

func dirName(d schema2020.Direction) string {
	return [...]string{"Neutral", "Request", "Response"}[d]
}

// standalone is the projection as one schema document: Root with Defs
// embedded under $defs, where every reference "#/$defs/KEY" points.
func standalone(t testing.TB, p *schema2020.Projection) any {
	t.Helper()
	root, err := jsonschema.UnmarshalJSON(bytes.NewReader(p.Root))
	if err != nil {
		t.Fatalf("Root: %v: %s", err, p.Root)
	}
	defs := map[string]any{}
	for k, d := range p.Defs {
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(d))
		if err != nil {
			t.Fatalf("Defs[%q]: %v: %s", k, err, d)
		}
		defs[k] = v
	}
	if obj, ok := root.(map[string]any); ok {
		if _, has := obj["$defs"]; has {
			t.Errorf("Root carries $defs")
		}
		obj["$defs"] = defs
		return obj
	}
	return map[string]any{"allOf": []any{root}, "$defs": defs}
}

// compileProjection compiles p as a JSON Schema 2020-12 document.
func compileProjection(t testing.TB, p *schema2020.Projection) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("urn:test:projection", standalone(t, p)); err != nil {
		t.Fatalf("AddResource: %v", err)
	}
	s, err := c.Compile("urn:test:projection")
	if err != nil {
		t.Fatalf("the projection does not compile as JSON Schema 2020-12: %v\nRoot %s\nDefs %s", err, p.Root, defsText(p))
	}
	return s
}

func defsText(p *schema2020.Projection) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(p.Defs)) {
		fmt.Fprintf(&b, "\n  %q: %s", k, p.Defs[k])
	}
	return b.String()
}

func accepts(t testing.TB, s *jsonschema.Schema, instance string) bool {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(instance))
	if err != nil {
		t.Fatalf("instance %s: %v", instance, err)
	}
	return s.Validate(v) == nil
}

// verdicts checks that s accepts each of ok and rejects each of bad.
func verdicts(t testing.TB, what string, s *jsonschema.Schema, ok, bad []string) {
	t.Helper()
	for _, i := range ok {
		if !accepts(t, s, i) {
			t.Errorf("%s rejects %s", what, i)
		}
	}
	for _, i := range bad {
		if accepts(t, s, i) {
			t.Errorf("%s accepts %s", what, i)
		}
	}
}

// project projects s, requiring a Projection, with or without an *Error.
func project(t testing.TB, c *openapi.Client, s *openapi.Schema, d schema2020.Direction) *schema2020.Projection {
	t.Helper()
	p, err := schema2020.Project(c, s, d)
	var pe *schema2020.Error
	if err != nil && !errors.As(err, &pe) {
		t.Fatalf("Project(%s, %s): %v", s.Source(), dirName(d), err)
	}
	if p == nil {
		t.Fatalf("Project(%s, %s): no Projection", s.Source(), dirName(d))
	}
	return p
}

func parse(t testing.TB, doc string, others map[string]string) *openapi.Client {
	t.Helper()
	fetch := func(_ context.Context, uri string) (io.ReadCloser, string, error) {
		d, ok := others[uri]
		if !ok {
			return nil, "", fmt.Errorf("no document at %s", uri)
		}
		return io.NopCloser(strings.NewReader(d)), "", nil
	}
	c, err := (&openapi.Loader{Fetch: fetch}).Parse(context.Background(), []byte(doc), entryURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func doc(version, schemas string) string {
	if version == "2.0" {
		return `{"swagger":"2.0","info":{"title":"T","version":"1"},"paths":{},"definitions":{` + schemas + `}}`
	}
	return `{"openapi":"` + version + `","info":{"title":"T","version":"1"},"paths":{},"components":{"schemas":{` + schemas + `}}}`
}

func compPath(version string) string {
	if version == "2.0" {
		return "#/definitions/"
	}
	return "#/components/schemas/"
}

// compiled projects the component name of a document of version and
// compiles the result.
func compiled(t testing.TB, version, schemas, name string, d schema2020.Direction) *jsonschema.Schema {
	t.Helper()
	c := parse(t, doc(version, strings.ReplaceAll(schemas, "#C/", compPath(version))), nil)
	s, err := c.Schema(entryURI + compPath(version) + name)
	if err != nil {
		t.Fatal(err)
	}
	return compileProjection(t, project(t, c, s, d))
}

// ---- every corpus projection compiles ----

const corpusDir = "../openapi/schema2020/testdata/corpus"
const corpusBase = "https://corpus.example.test/"

// Every schema of every corpus document, in every direction, projects to a
// schema the evaluator compiles against the 2020-12 meta-schema, Issues or
// not, and the compiled schema evaluates the authored examples without
// failing.
func TestCorpusProjectionsCompile(t *testing.T) {
	dirs, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	cases := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(corpusDir, d.Name())
		for _, entry := range []string{"openapi.json", "openapi.yaml"} {
			content, err := os.ReadFile(filepath.Join(dir, entry))
			if err != nil {
				continue
			}
			cases++
			t.Run(d.Name(), func(t *testing.T) {
				c := loadCorpus(t, d.Name(), dir, entry, content)
				schemas := corpusSchemas(t, c)
				if len(schemas) == 0 {
					t.Fatal("no schemas")
				}
				for _, s := range schemas {
					for _, dn := range directions {
						t.Run(s.Source()+"/"+dirName(dn), func(t *testing.T) {
							sch := compileProjection(t, project(t, c, s, dn))
							for _, instance := range []string{`null`, `{}`, `"x"`, `1`, `[]`} {
								accepts(t, sch, instance) // evaluation must not fail to run
							}
						})
					}
				}
			})
		}
	}
	if cases == 0 {
		t.Fatal("no corpus documents")
	}
}

func loadCorpus(t testing.TB, name, dir, entry string, content []byte) *openapi.Client {
	t.Helper()
	base := corpusBase + name + "/"
	fetch := func(_ context.Context, uri string) (io.ReadCloser, string, error) {
		rel, ok := strings.CutPrefix(uri, base)
		if !ok || strings.Contains(rel, "..") {
			return nil, "", fmt.Errorf("no document at %s", uri)
		}
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, "", err
		}
		return f, "", nil
	}
	c, err := (&openapi.Loader{Fetch: fetch}).Parse(context.Background(), content, base+entry, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// corpusSchemas lists the entry's components and every descriptor schema.
func corpusSchemas(t testing.TB, c *openapi.Client) []*openapi.Schema {
	t.Helper()
	var out []*openapi.Schema
	var d struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
		Components  struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(c.Document(""), &d); err != nil {
		t.Fatal(err)
	}
	prefix, names := "#/components/schemas/", slices.Sorted(maps.Keys(d.Components.Schemas))
	if c.Version() == "2.0" {
		prefix, names = "#/definitions/", slices.Sorted(maps.Keys(d.Definitions))
	}
	for _, n := range names {
		tok := strings.ReplaceAll(strings.ReplaceAll(n, "~", "~0"), "/", "~1")
		s, err := c.Schema(c.DocumentURIs()[0] + prefix + (&urlFragment{tok}).String())
		if err == nil {
			out = append(out, s)
		}
	}
	add := func(s *openapi.Schema) {
		if s != nil {
			out = append(out, s)
		}
	}
	params := func(ps []*openapi.Param) {
		for _, p := range ps {
			add(p.Schema)
			for _, h := range p.Headers {
				add(h.Schema)
			}
		}
	}
	media := func(ms []*openapi.Media) {
		for _, m := range ms {
			add(m.Schema)
			add(m.ItemSchema)
			params(m.Encoding)
		}
	}
	for _, op := range c.Operations() {
		params(op.Params)
		if op.Body != nil {
			media(op.Body.Media)
		}
		for _, r := range op.Responses {
			media(r.Media)
			params(r.Headers)
		}
	}
	return out
}

// urlFragment percent-encodes a JSON Pointer token for a URI fragment
// (RFC 6901 section 6).
type urlFragment struct{ tok string }

func (f *urlFragment) String() string {
	var b strings.Builder
	for i := 0; i < len(f.tok); i++ {
		c := f.tok[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~!$&'()*+,;=:@/?", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// ---- instance verdicts ----

// OAS 3.0.4 section 4.7.24.2, nullable: "A true value indicates that both
// null values and values of the type specified by type are allowed. Other
// Schema Object constraints retain their defined behavior, and therefore
// may disallow the use of null as a value."
func TestNullable30(t *testing.T) {
	s := compiled(t, "3.0.4", `"S":{"type":"string","nullable":true}`, "S", schema2020.Neutral)
	verdicts(t, "nullable string", s, []string{`null`, `"x"`}, []string{`1`, `{}`})
	s = compiled(t, "3.0.4", `"S":{"type":"string","nullable":true,"enum":["a","b"]}`, "S", schema2020.Neutral)
	verdicts(t, "nullable enum", s, []string{`"a"`}, []string{`null`, `"c"`})
	s = compiled(t, "3.0.4", `"S":{"type":"string","nullable":false}`, "S", schema2020.Neutral)
	verdicts(t, "nullable false", s, []string{`"x"`}, []string{`null`})
	s = compiled(t, "3.0.4", `"S":{"type":"object","properties":{"a":{"$ref":"#C/N"}}},"N":{"type":"integer","nullable":true}`, "S", schema2020.Request)
	verdicts(t, "nullable through $ref", s, []string{`{"a":null}`, `{"a":3}`}, []string{`{"a":"3"}`})
}

// JSON Schema Wright-00 boolean bounds (OAS 3.0.4 section 4.7.24.1, OAS
// 2.0 section 6.4.18): exclusiveMinimum: true with minimum 5 excludes 5.
func TestExclusiveBounds(t *testing.T) {
	for _, v := range []string{"2.0", "3.0.4"} {
		t.Run(v, func(t *testing.T) {
			s := compiled(t, v, `"S":{"type":"integer","minimum":5,"exclusiveMinimum":true}`, "S", schema2020.Neutral)
			verdicts(t, "exclusive minimum", s, []string{`6`}, []string{`5`, `4`})
			s = compiled(t, v, `"S":{"type":"number","maximum":10,"exclusiveMaximum":true}`, "S", schema2020.Neutral)
			verdicts(t, "exclusive maximum", s, []string{`9.5`}, []string{`10`, `11`})
			s = compiled(t, v, `"S":{"type":"integer","minimum":5,"exclusiveMinimum":false,"maximum":7,"exclusiveMaximum":false}`, "S", schema2020.Neutral)
			verdicts(t, "inclusive bounds", s, []string{`5`, `7`}, []string{`4`, `8`})
			s = compiled(t, v, `"S":{"type":"integer","exclusiveMinimum":true}`, "S", schema2020.Neutral)
			verdicts(t, "no bound", s, []string{`-1000`, `0`}, []string{`"0"`})
		})
	}
	// In OpenAPI 3.1 the numeric form is the authored one.
	s := compiled(t, "3.1.2", `"S":{"type":"integer","exclusiveMinimum":5}`, "S", schema2020.Neutral)
	verdicts(t, "3.1 exclusiveMinimum", s, []string{`6`}, []string{`5`})
}

// OAS 2.0 section 6.4.18.1, readOnly: "MAY be sent as part of a response
// but MUST NOT be sent as part of the request". A Request projection
// rejects an object carrying a readOnly property, declared directly or
// through $ref; other directions accept it.
func TestReadOnlyRequest20(t *testing.T) {
	schemas := `"Account":{"type":"object","required":["id","name"],"properties":{"id":{"type":"string","readOnly":true},"name":{"type":"string"},"owner":{"$ref":"#C/Owner"}}},
		"Owner":{"type":"object","readOnly":true}`
	req := compiled(t, "2.0", schemas, "Account", schema2020.Request)
	verdicts(t, "2.0 Request", req, []string{`{"name":"n"}`}, []string{`{"id":"1","name":"n"}`, `{"name":"n","owner":{}}`})
	for _, d := range []schema2020.Direction{schema2020.Neutral, schema2020.Response} {
		s := compiled(t, "2.0", schemas, "Account", d)
		verdicts(t, "2.0 "+dirName(d), s, []string{`{"id":"1","name":"n","owner":{}}`}, []string{`{"name":"n"}`})
	}
}

// OAS 3.0.4 section 4.7.24.2: a required readOnly property's "required will
// take effect on the response only", and a required writeOnly one's "on the
// request only"; "SHOULD NOT be sent" does not forbid either.
func TestRequiredByDirection30(t *testing.T) {
	schemas := `"Account":{"type":"object","required":["id","name","secret"],"properties":{
		"id":{"type":"string","readOnly":true},"name":{"type":"string"},"secret":{"type":"string","writeOnly":true}}}`
	req := compiled(t, "3.0.4", schemas, "Account", schema2020.Request)
	verdicts(t, "3.0 Request", req,
		[]string{`{"name":"n","secret":"s"}`, `{"id":"1","name":"n","secret":"s"}`},
		[]string{`{"id":"1","name":"n"}`})
	resp := compiled(t, "3.0.4", schemas, "Account", schema2020.Response)
	verdicts(t, "3.0 Response", resp,
		[]string{`{"id":"1","name":"n"}`, `{"id":"1","name":"n","secret":"s"}`},
		[]string{`{"name":"n","secret":"s"}`})
	neutral := compiled(t, "3.0.4", schemas, "Account", schema2020.Neutral)
	verdicts(t, "3.0 Neutral", neutral,
		[]string{`{"id":"1","name":"n","secret":"s"}`},
		[]string{`{"name":"n","secret":"s"}`, `{"id":"1","name":"n"}`})
}

// Project: a property's declarations include "the properties of every
// schema the object reaches through allOf".
func TestAllOfReadOnly30(t *testing.T) {
	schemas := `"Base":{"type":"object","properties":{"id":{"type":"string","readOnly":true}}},
		"Pet":{"allOf":[{"$ref":"#C/Base"}],"required":["id","name"],"properties":{"name":{"type":"string"}}}`
	verdicts(t, "Request", compiled(t, "3.0.4", schemas, "Pet", schema2020.Request), []string{`{"name":"x"}`}, []string{`{}`})
	verdicts(t, "Response", compiled(t, "3.0.4", schemas, "Pet", schema2020.Response), []string{`{"id":"1","name":"x"}`}, []string{`{"name":"x"}`})
	verdicts(t, "2.0 Request", compiled(t, "2.0", schemas, "Pet", schema2020.Request), []string{`{"name":"x"}`}, []string{`{"id":"1","name":"x"}`})
}

// OAS 3.1.2 section 4.8.24.3.2: readOnly and writeOnly are annotations in
// 3.1, so the required list holds in every direction.
func TestReadOnlyAnnotation31(t *testing.T) {
	schemas := `"Account":{"type":"object","required":["id","name"],"properties":{"id":{"type":"string","readOnly":true},"name":{"type":"string"}}}`
	for _, v := range []string{"3.1.2", "3.2.1"} {
		for _, d := range directions {
			verdicts(t, v+" "+dirName(d), compiled(t, v, schemas, "Account", d), []string{`{"id":"1","name":"n"}`}, []string{`{"name":"n"}`})
		}
	}
}

// format: binary says nothing JSON Schema can check about a JSON value, so
// its projection accepts anything; format: byte becomes contentEncoding,
// an annotation in JSON Schema 2020-12 (Validation section 8.3), and the
// value stays a string.
func TestBinaryAndByte(t *testing.T) {
	for _, v := range []string{"2.0", "3.0.4"} {
		t.Run(v, func(t *testing.T) {
			s := compiled(t, v, `"S":{"type":"string","format":"binary"}`, "S", schema2020.Request)
			verdicts(t, "binary", s, []string{`"x"`, `5`, `{}`}, nil)
			s = compiled(t, v, `"S":{"type":"string","format":"byte"}`, "S", schema2020.Request)
			verdicts(t, "byte", s, []string{`"aGk="`}, []string{`5`})
			s = compiled(t, v, `"S":{"type":"object","properties":{"file":{"type":"string","format":"binary"},"sig":{"type":"string","format":"byte"}}}`, "S", schema2020.Request)
			verdicts(t, "multipart", s, []string{`{"file":1,"sig":"aGk="}`}, []string{`{"sig":1}`})
		})
	}
	s := compiled(t, "2.0", `"S":{"type":"file"}`, "S", schema2020.Response)
	verdicts(t, "file", s, []string{`"x"`, `5`}, nil)
}

// Keys escaped in references (Project: "a key is escaped as a JSON Pointer
// token and then as RFC 3986 section 3.5 requires of a fragment") resolve
// in the evaluator to the right Defs entry.
func TestKeysResolve(t *testing.T) {
	names := []struct{ name, typ, good, bad string }{
		{"a b", "string", `"s"`, `1`},
		{"a/b", "integer", `1`, `"s"`},
		{"a~b", "object", `{}`, `[]`},
		{"é", "boolean", `true`, `1`},
		{"100%", "null", `null`, `0`},
		{"a#b", "array", `[]`, `{}`},
		{"x:y@z", "number", `1.5`, `"1"`},
		{"q?r", "string", `"s"`, `null`},
		{"a!$&'()*+,;=b", "integer", `2`, `2.5`},
		{"{x}|^`", "boolean", `false`, `"false"`},
	}
	for _, v := range []string{"2.0", "3.0.4", "3.1.2", "3.2.1"} {
		t.Run(v, func(t *testing.T) {
			var props, comps []string
			var good, bad []string
			for i, n := range names {
				tok := strings.ReplaceAll(strings.ReplaceAll(n.name, "~", "~0"), "/", "~1")
				props = append(props, fmt.Sprintf(`"p%d":{"$ref":"%s%s"}`, i, compPath(v), (&urlFragment{tok}).String()))
				key, _ := json.Marshal(n.name)
				comps = append(comps, fmt.Sprintf(`%s:{"type":%q}`, key, n.typ))
				good = append(good, fmt.Sprintf(`"p%d":%s`, i, n.good))
				bad = append(bad, fmt.Sprintf(`{"p%d":%s}`, i, n.bad))
			}
			schemas := `"R":{"type":"object","properties":{` + strings.Join(props, ",") + `}},` + strings.Join(comps, ",")
			s := compiled(t, v, schemas, "R", schema2020.Neutral)
			verdicts(t, "keys", s, []string{"{" + strings.Join(good, ",") + "}"}, bad)
		})
	}
}

// A self-reference and a cycle validate an instance of any depth.
func TestCycles(t *testing.T) {
	for _, v := range []string{"2.0", "3.0.4", "3.1.2"} {
		s := compiled(t, v, `"Node":{"type":"object","properties":{"v":{"type":"integer"},"next":{"$ref":"#C/Node"}}}`, "Node", schema2020.Neutral)
		verdicts(t, v+" cycle", s, []string{`{"v":1,"next":{"v":2,"next":{"v":3}}}`}, []string{`{"next":{"next":{"v":"x"}}}`})
	}
}

// References across documents and through a nested $id resource resolve
// within the one projection.
func TestReferencesAcrossDocuments(t *testing.T) {
	others := map[string]string{
		"https://api.example.test/v1/models.json": `{"Pet":{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"tag":{"$ref":"#/Tag"}}},"Tag":{"type":"string","maxLength":3}}`,
	}
	c := parse(t, doc("3.1.2", `"R":{"type":"object","properties":{"pet":{"$ref":"models.json#/Pet"},
		"n":{"$id":"https://api.example.test/schemas/n.json","$defs":{"d":{"type":"integer"}},"$ref":"#/$defs/d"}}}`), others)
	s, err := c.Schema(entryURI + "#/components/schemas/R")
	if err != nil {
		t.Fatal(err)
	}
	sch := compileProjection(t, project(t, c, s, schema2020.Neutral))
	verdicts(t, "across documents", sch,
		[]string{`{"pet":{"name":"x","tag":"abc"},"n":3}`},
		[]string{`{"pet":{}}`, `{"pet":{"name":"x","tag":"abcd"}}`, `{"n":"3"}`})
}

// A lost site is true, the schema that accepts anything (Project).
func TestLostSiteAcceptsAnything(t *testing.T) {
	for _, v := range []string{"2.0", "3.0.4", "3.1.2"} {
		c := parse(t, doc(v, `"R":{"type":"object","required":["a"],"properties":{"a":{"$ref":"`+compPath(v)+`Missing"},"b":{"type":"string"}}}`), nil)
		s, err := c.Schema(entryURI + compPath(v) + "R")
		if err != nil {
			t.Fatal(err)
		}
		p, err := schema2020.Project(c, s, schema2020.Neutral)
		var pe *schema2020.Error
		if !errors.As(err, &pe) || p == nil {
			t.Fatalf("%s: Project = %v, %v; want a Projection and *Error", v, p, err)
		}
		verdicts(t, v+" lost site", compileProjection(t, p), []string{`{"a":1}`, `{"a":{"x":[]},"b":"s"}`}, []string{`{"b":"s"}`, `{"a":1,"b":2}`})
	}
}

// Project: a keyword the edition's Schema Object does not define and that
// JSON Schema 2020-12 treats as an applicator or an assertion "is removed
// with an Issue, since it has no meaning in the authored edition", so it
// constrains nothing; an annotation the edition lacks stays and constrains
// nothing either. In Swagger 2.0 nullable is such an annotation.
func TestUndefinedKeywordsConstrainNothing(t *testing.T) {
	lossy := func(version, schemas, name string) *jsonschema.Schema {
		t.Helper()
		c := parse(t, doc(version, schemas), nil)
		s, err := c.Schema(entryURI + compPath(version) + name)
		if err != nil {
			t.Fatal(err)
		}
		p, err := schema2020.Project(c, s, schema2020.Neutral)
		var pe *schema2020.Error
		if !errors.As(err, &pe) || p == nil {
			t.Fatalf("Project = %v, %v; want a Projection and *Error", p, err)
		}
		return compileProjection(t, p)
	}
	s := lossy("2.0", `"S":{"type":"string","oneOf":[{"maxLength":1}],"const":"x","not":{"type":"string"}}`, "S")
	verdicts(t, "2.0 oneOf, const and not", s, []string{`"abc"`}, []string{`1`})
	s = lossy("3.0.4", `"S":{"type":"object","const":{"a":1},"patternProperties":{"^x":{"type":"integer"}},"propertyNames":{"maxLength":1}}`, "S")
	verdicts(t, "3.0 const, patternProperties and propertyNames", s, []string{`{"xyz":"s"}`}, []string{`"s"`})
	s = compiled(t, "2.0", `"S":{"type":"string","nullable":true}`, "S", schema2020.Neutral)
	verdicts(t, "2.0 nullable", s, []string{`"x"`}, []string{`null`})
}

// Project: "A required list left empty is removed"; the object still
// accepts the value without the property.
func TestEmptiedRequired(t *testing.T) {
	schemas := `"Token":{"type":"object","required":["id"],"properties":{"id":{"type":"string","readOnly":true}}}`
	verdicts(t, "3.0 Request", compiled(t, "3.0.4", schemas, "Token", schema2020.Request), []string{`{}`, `{"id":"1"}`}, []string{`[]`})
	verdicts(t, "3.0 Response", compiled(t, "3.0.4", schemas, "Token", schema2020.Response), []string{`{"id":"1"}`}, []string{`{}`})
	verdicts(t, "2.0 Request", compiled(t, "2.0", schemas, "Token", schema2020.Request), []string{`{}`}, []string{`{"id":"1"}`})
}

// Project: binary and file translation comes first ("format: binary with
// type: string ... lose both keywords"), and nullable adds "null" only
// "where the schema then has a type"; format: byte "becomes
// contentEncoding: base64" whatever the type.
func TestBinaryNullableAndByteTypes(t *testing.T) {
	s := compiled(t, "3.0.4", `"S":{"type":"string","format":"binary","nullable":true}`, "S", schema2020.Request)
	verdicts(t, "nullable binary", s, []string{`null`, `5`, `"x"`}, nil)
	s = compiled(t, "3.0.4", `"S":{"type":"string","format":"byte","nullable":true}`, "S", schema2020.Request)
	verdicts(t, "nullable byte", s, []string{`null`, `"aGk="`}, []string{`5`})
	s = compiled(t, "2.0", `"S":{"type":"integer","format":"byte"}`, "S", schema2020.Request)
	verdicts(t, "byte on an integer", s, []string{`5`}, []string{`"5"`})
}

// Project: a lost reference "is dropped"; in OpenAPI 3.1 the members
// beside it still apply, and in OpenAPI 3.0, where they are ignored, the
// schema is left with no keywords and accepts anything.
func TestLostReferenceSiblings(t *testing.T) {
	for version, ok := range map[string][]string{
		"3.1.2": {`"abc"`},
		"3.0.4": {`"abc"`, `"abcd"`, `5`},
	} {
		c := parse(t, doc(version, `"R":{"properties":{"a":{"$ref":"#/components/schemas/Missing","type":"string","maxLength":3}}}`), nil)
		s, err := c.Schema(entryURI + "#/components/schemas/R")
		if err != nil {
			t.Fatal(err)
		}
		p, err := schema2020.Project(c, s, schema2020.Neutral)
		var pe *schema2020.Error
		if !errors.As(err, &pe) || p == nil {
			t.Fatalf("%s: Project = %v, %v", version, p, err)
		}
		sch := compileProjection(t, p)
		var good, bad []string
		for _, v := range ok {
			good = append(good, `{"a":`+v+`}`)
		}
		if version == "3.1.2" {
			bad = []string{`{"a":"abcd"}`, `{"a":5}`}
		}
		verdicts(t, version+" lost $ref", sch, good, bad)
	}
}

// Project: items written as an array, "a form neither edition defines", is
// removed with an Issue, so it constrains nothing and leaves no reference
// behind.
func TestArrayItemsConstrainNothing(t *testing.T) {
	for _, v := range []string{"2.0", "3.0.4"} {
		c := parse(t, doc(v, `"S":{"type":"array","maxItems":2,"items":[{"$ref":"`+compPath(v)+`A"},{"type":"string"}]},"A":{"type":"integer"}`), nil)
		s, err := c.Schema(entryURI + compPath(v) + "S")
		if err != nil {
			t.Fatal(err)
		}
		p, err := schema2020.Project(c, s, schema2020.Request)
		var pe *schema2020.Error
		if !errors.As(err, &pe) || p == nil {
			t.Fatalf("%s: Project = %v, %v; want a Projection and *Error", v, p, err)
		}
		verdicts(t, v+" array items", compileProjection(t, p), []string{`["x",1]`, `[]`}, []string{`[1,2,3]`, `{}`})
	}
}

// Project: nullable "adds "null" to it as a type list unless it already
// allows null"; the result is a JSON Schema 2020-12 type list.
func TestNullableTypeLists(t *testing.T) {
	s := compiled(t, "3.0.4", `"S":{"type":["string","integer"],"nullable":true}`, "S", schema2020.Neutral)
	verdicts(t, "nullable type list", s, []string{`null`, `"x"`, `1`}, []string{`true`, `{}`})
	s = compiled(t, "3.0.4", `"S":{"type":"null","nullable":true}`, "S", schema2020.Neutral)
	verdicts(t, "nullable null", s, []string{`null`}, []string{`"x"`, `0`})
	s = compiled(t, "3.0.4", `"S":{"type":["string","null"],"nullable":true}`, "S", schema2020.Neutral)
	verdicts(t, "nullable list with null", s, []string{`null`, `"x"`}, []string{`1`})
}

// Project: a reference "that openapi.Schema.References does not report
// (such as a $ref in a Swagger 2.0 items object, or one that is not a
// string)" is removed with an Issue, so the output compiles and the lost
// part accepts anything.
func TestUnreportedReferencesCompile(t *testing.T) {
	c := parse(t, `{"swagger":"2.0","info":{"title":"T","version":"1"},"paths":{"/x":{"get":{"parameters":[
		{"name":"q","in":"query","type":"array","maxItems":2,"items":{"$ref":"#/definitions/D"}}],"responses":{"204":{"description":"none"}}}}},
		"definitions":{"D":{"type":"string"}}}`, nil)
	op, err := c.Operation("GET /x")
	if err != nil {
		t.Fatal(err)
	}
	p, err := schema2020.Project(c, op.Params[0].Schema, schema2020.Request)
	var pe *schema2020.Error
	if !errors.As(err, &pe) || p == nil {
		t.Fatalf("Project = %v, %v; want a Projection and *Error", p, err)
	}
	verdicts(t, "2.0 items $ref", compileProjection(t, p), []string{`[1,"x"]`}, []string{`[1,2,3]`, `"x"`})

	for _, v := range []string{"2.0", "3.0.4", "3.1.2"} {
		c := parse(t, doc(v, `"A":{"type":"object","properties":{"x":{"$ref":5,"type":"string"},"y":{"$ref":{"a":1}}}}`), nil)
		s, err := c.Schema(entryURI + compPath(v) + "A")
		if err != nil {
			t.Fatal(err)
		}
		p, err := schema2020.Project(c, s, schema2020.Neutral)
		if !errors.As(err, &pe) || p == nil {
			t.Fatalf("%s: Project = %v, %v; want a Projection and *Error", v, p, err)
		}
		verdicts(t, v+" $ref not a string", compileProjection(t, p), []string{`{"y":[]}`, `{"x":"s"}`}, []string{`[]`})
	}
}
