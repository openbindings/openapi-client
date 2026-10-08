package schema2020_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// Benchmarks and scaling. A synthetic document of about 2,000 operations
// shares components among them; each projection reaches a bounded
// cluster of components, as typical documents do.

// syntheticDocument returns an OpenAPI 3.x document of version v with ops
// operations (four per path) and ops/10 components in clusters of five
// that refer to each other, a shared Base reached through allOf, and the
// keywords each edition translates or keeps.
func syntheticDocument(v string, ops int) string {
	legacy := !isModern(v)
	bound := `"exclusiveMinimum":0`
	nullable := `"type":["string","null"]`
	if legacy {
		bound = `"minimum":0,"exclusiveMinimum":true`
		nullable = `"type":"string","nullable":true`
	}
	n := ops / 10
	if n < 5 {
		n = 5
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"openapi":%q,"info":{"title":"Synthetic","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{`, v)
	for p := 0; p < ops/4; p++ {
		if p > 0 {
			b.WriteByte(',')
		}
		e := (p * 7) % n
		fmt.Fprintf(&b, `"/r%d/{id}":{"parameters":[{"name":"id","in":"path","required":true,"description":"The identifier of resource %d.","schema":{"type":"string","format":"uuid","maxLength":36}}],`, p, p)
		fmt.Fprintf(&b, `"get":{"operationId":"get%d","summary":"Get one resource of its kind","parameters":[{"name":"q","in":"query","schema":{"$ref":"#/components/schemas/Query"}},{"name":"X-Page","in":"header","schema":{"type":"integer",%s}}],`, p, bound)
		fmt.Fprintf(&b, `"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"array","items":{"$ref":"#/components/schemas/E%d"}}}}},"default":{"description":"error","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Error"}}}}}},`, e)
		fmt.Fprintf(&b, `"post":{"operationId":"post%d","summary":"Post one resource of its kind","requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/E%d"}},"multipart/form-data":{"schema":{"type":"object","properties":{"file":{"type":"string","format":"binary"},"meta":{"$ref":"#/components/schemas/E%d"}}}}}},`, p, e, (e+1)%n)
		fmt.Fprintf(&b, `"responses":{"201":{"description":"created","content":{"application/json":{"schema":{"$ref":"#/components/schemas/E%d"}}}}}},`, e)
		fmt.Fprintf(&b, `"put":{"operationId":"put%d","summary":"Put one resource of its kind","requestBody":{"content":{"application/json":{"schema":{"type":"object","required":["value"],"properties":{"value":{"$ref":"#/components/schemas/E%d"},"note":{%s}}}}}},"responses":{"204":{"description":"done"}}},`, p, (e+2)%n, nullable)
		fmt.Fprintf(&b, `"delete":{"operationId":"delete%d","summary":"Delete one resource of its kind","responses":{"204":{"description":"done"}}}}`, p)
	}
	b.WriteString(`},"components":{"schemas":{`)
	fmt.Fprintf(&b, `"Base":{"type":"object","required":["id","created"],"properties":{"id":{"type":"string","readOnly":true},"created":{"type":"string","format":"date-time","readOnly":true},"etag":{"type":"string","writeOnly":true}}},`)
	fmt.Fprintf(&b, `"Query":{"type":"object","properties":{"text":{%s},"limit":{"type":"integer",%s,"maximum":100}}},`, nullable, bound)
	fmt.Fprintf(&b, `"Error":{"type":"object","required":["code"],"properties":{"code":{"type":"integer"},"message":{"type":"string"},"cause":{"$ref":"#/components/schemas/Error"}}}`)
	for i := 0; i < n; i++ {
		next := i - i%5 + (i+1)%5
		fmt.Fprintf(&b, `,"E%d":{"allOf":[{"$ref":"#/components/schemas/Base"}],"type":"object","required":["id","name","secret"],"properties":{`+
			`"name":{"type":"string","minLength":1,"maxLength":64},"secret":{"type":"string","writeOnly":true},"size":{"type":"number",%s,"maximum":1e9},`+
			`"blob":{"type":"string","format":"byte"},"note":{%s},"next":{"$ref":"#/components/schemas/E%d"},`+
			`"items":{"type":"array","items":{"type":"object","properties":{"sku":{"type":"string","pattern":"^[A-Z0-9-]+$"},"qty":{"type":"integer",%s}}}},`+
			`"tags":{"type":"array","items":{"type":"string","enum":["a","b","c"]}}}}`, i, bound, nullable, next, bound)
	}
	b.WriteString(`}}}`)
	return b.String()
}

// contentAndParameterSchemas lists the schemas of every parameter and
// every request and response content of c.
func contentAndParameterSchemas(c *openapi.Client) []*openapi.Schema {
	var out []*openapi.Schema
	for _, op := range c.Operations() {
		for _, p := range op.Params {
			if p.Schema != nil {
				out = append(out, p.Schema)
			}
		}
		var media []*openapi.Media
		if op.Body != nil {
			media = append(media, op.Body.Media...)
		}
		for _, r := range op.Responses {
			media = append(media, r.Media...)
		}
		for _, m := range media {
			if m.Schema != nil {
				out = append(out, m.Schema)
			}
		}
	}
	return out
}

func parseSynthetic(tb testing.TB, doc string) *openapi.Client {
	tb.Helper()
	c, err := openapi.Parse(context.Background(), []byte(doc), entryURI, nil)
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

// projectAll projects every schema in direction d and fails on any error.
func projectAll(tb testing.TB, c *openapi.Client, schemas []*openapi.Schema, d schema2020.Direction) {
	for _, s := range schemas {
		if _, err := schema2020.Project(c, s, d); err != nil {
			tb.Fatalf("Project(%s): %v", s.Source(), err)
		}
	}
}

// The synthetic document is about 1 MB with about 2,000 operations.
func TestSyntheticDocumentSize(t *testing.T) {
	for _, v := range []string{v30, v31} {
		doc := syntheticDocument(v, 2000)
		c := parseSynthetic(t, doc)
		if n := len(c.Operations()); n != 2000 {
			t.Errorf("%s: %d operations", v, n)
		}
		if len(doc) < 800<<10 || len(doc) > 1300<<10 {
			t.Errorf("%s: %d bytes, want about 1 MB", v, len(doc))
		}
	}
}

// One schema, projected on a fresh client (the first call) and again on
// the same client (warm).
func BenchmarkProjectOneSchema(b *testing.B) {
	for _, v := range []string{v30, v31} {
		doc := syntheticDocument(v, 40)
		b.Run(v+"/first", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				c := parseSynthetic(b, doc)
				s, err := c.Schema(entryURI + "#/components/schemas/E0")
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if _, err := schema2020.Project(c, s, schema2020.Request); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(v+"/warm", func(b *testing.B) {
			c := parseSynthetic(b, doc)
			s, err := c.Schema(entryURI + "#/components/schemas/E0")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := schema2020.Project(c, s, schema2020.Request); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := schema2020.Project(c, s, schema2020.Request); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Every content and parameter schema of the synthetic document, in each
// direction.
func BenchmarkProjectDocument(b *testing.B) {
	for _, v := range []string{v30, v31} {
		c := parseSynthetic(b, syntheticDocument(v, 2000))
		schemas := contentAndParameterSchemas(c)
		for _, d := range directions {
			b.Run(v+"/"+dirName(d), func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(len(schemas)), "schemas/op")
				for i := 0; i < b.N; i++ {
					projectAll(b, c, schemas, d)
				}
			})
		}
	}
}

// deepDocument returns a 3.0 document whose component Deep nests depth
// object schemas, each through properties (two JSON levels per schema,
// within the Loader's 1,000-level bound), and whose component C0 starts a
// chain of chain components, each referring to the next.
func deepDocument(depth, chain int) string {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.0.4","info":{"title":"Deep","version":"1"},"paths":{},"components":{"schemas":{"Deep":`)
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&b, `{"type":"object","required":["c"],"properties":{"n%d":{"type":"integer","minimum":0,"exclusiveMinimum":true,"nullable":true},"c":`, i)
	}
	b.WriteString(`{"type":"string"}`)
	for i := 0; i < depth; i++ {
		b.WriteString(`}}`)
	}
	for i := 0; i < chain; i++ {
		fmt.Fprintf(&b, `,"C%d":{"type":"object","required":["id"],"properties":{"id":{"type":"string","readOnly":true},"next":{"$ref":"#/components/schemas/C%d"}}}`, i, (i+1)%chain)
	}
	b.WriteString(`}}}`)
	return b.String()
}

// perOp is the best of three measurements of f's time per call, each over
// enough calls to take at least 50ms.
func perOp(tb testing.TB, f func()) time.Duration {
	tb.Helper()
	best := time.Duration(0)
	for round := 0; round < 3; round++ {
		n := 0
		start := time.Now()
		for time.Since(start) < 50*time.Millisecond {
			f()
			n++
		}
		d := time.Since(start) / time.Duration(n)
		if round == 0 || d < best {
			best = d
		}
	}
	return best
}

// Regression check, not contract: a timing check, skipped under -short. Four
// times the input costs at most about ten times the time: one deep schema, one
// long reference chain, a whole document, and the readOnly/writeOnly discovery
// through allOf (Project: a property's "declarations are those in the object's
// properties and in the properties of every schema the object reaches through
// allOf"), for one long chain and for many objects sharing one.
func TestProjectScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	cases := []struct {
		name  string
		small func(tb testing.TB) func()
		large func(tb testing.TB) func()
	}{
		{"deep schema", deepCase(100), deepCase(400)},
		{"reference chain", chainCase(500), chainCase(2000)},
		{"whole document", documentCase(250), documentCase(1000)},
		{"allOf chain", allOfChainCase(100), allOfChainCase(400)},
		{"objects sharing an allOf chain", sharedChainCase(50), sharedChainCase(200)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			small, large := perOp(t, tc.small(t)), perOp(t, tc.large(t))
			ratio := float64(large) / float64(small)
			t.Logf("%v for 1x, %v for 4x: %.1fx", small, large, ratio)
			if ratio > 10 {
				t.Errorf("4x input took %.1fx the time", ratio)
			}
		})
	}
}

func deepCase(depth int) func(testing.TB) func() {
	return func(tb testing.TB) func() {
		c := parseSynthetic(tb, deepDocument(depth, 1))
		s, err := c.Schema(entryURI + "#/components/schemas/Deep")
		if err != nil {
			tb.Fatal(err)
		}
		return func() { projectAll(tb, c, []*openapi.Schema{s}, schema2020.Request) }
	}
}

func chainCase(n int) func(testing.TB) func() {
	return func(tb testing.TB) func() {
		c := parseSynthetic(tb, deepDocument(1, n))
		s, err := c.Schema(entryURI + "#/components/schemas/C0")
		if err != nil {
			tb.Fatal(err)
		}
		return func() { projectAll(tb, c, []*openapi.Schema{s}, schema2020.Request) }
	}
}

func documentCase(ops int) func(testing.TB) func() {
	return func(tb testing.TB) func() {
		c := parseSynthetic(tb, syntheticDocument(v30, ops))
		schemas := contentAndParameterSchemas(c)
		return func() { projectAll(tb, c, schemas, schema2020.Response) }
	}
}

// allOfDocument returns an OpenAPI 3.0 document with a chain of links
// L0 to L(links-1), each reaching the next through allOf and declaring
// properties, some readOnly or writeOnly, and a required list that names
// a property declared one link further; and objects O0 to O(objects-1),
// each reaching L0 through allOf and requiring properties declared at
// both ends of the chain.
func allOfDocument(links, objects int) string {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.0.4","info":{"title":"Chain","version":"1"},"paths":{},"components":{"schemas":{`)
	for i := 0; i < links; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		allOf := ""
		if i+1 < links {
			allOf = fmt.Sprintf(`"allOf":[{"$ref":"#/components/schemas/L%d"}],`, i+1)
		}
		fmt.Fprintf(&b, `"L%d":{%s"type":"object","properties":{"p%d":{"type":"string","readOnly":%v},"w%d":{"type":"string","writeOnly":%v}},"required":["p%d","w%d","p%d"]}`,
			i, allOf, i, i%2 == 0, i, i%3 == 0, i, i, i+1)
	}
	for j := 0; j < objects; j++ {
		fmt.Fprintf(&b, `,"O%d":{"allOf":[{"$ref":"#/components/schemas/L0"}],"type":"object","properties":{"q%d":{"type":"integer"}},"required":["q%d","p0","p%d"]}`, j, j, j, links-1)
	}
	b.WriteString(`}}}`)
	return b.String()
}

// allOfChainCase projects the head of a chain of links links for a
// request.
func allOfChainCase(links int) func(testing.TB) func() {
	return func(tb testing.TB) func() {
		c := parseSynthetic(tb, allOfDocument(links, 0))
		s, err := c.Schema(entryURI + "#/components/schemas/L0")
		if err != nil {
			tb.Fatal(err)
		}
		return func() { projectAll(tb, c, []*openapi.Schema{s}, schema2020.Request) }
	}
}

// sharedChainCase projects, for a request, each of objects objects that
// share one chain of 50 links.
func sharedChainCase(objects int) func(testing.TB) func() {
	return func(tb testing.TB) func() {
		c := parseSynthetic(tb, allOfDocument(50, objects))
		var schemas []*openapi.Schema
		for j := 0; j < objects; j++ {
			s, err := c.Schema(fmt.Sprintf("%s#/components/schemas/O%d", entryURI, j))
			if err != nil {
				tb.Fatal(err)
			}
			schemas = append(schemas, s)
		}
		return func() { projectAll(tb, c, schemas, schema2020.Request) }
	}
}
