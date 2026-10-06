package openapi_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Hostile-input cost for documents. Every document-driven path is covered
// by the scaling-test harness: every compiled or decoded form is computed at
// most once per document node, and a read costs O(size of what it returns
// or compares). Many documents, long reference chains and cycles across
// documents, one external URI referenced many times (fetched once), a URI
// claimed by many schemas, alias bombs, deep YAML and many keys are each
// linear, in time and bytes. Documents are served from memory by a Fetch,
// so the transport adds no noise.

// docsBase is the URI under which the scaling documents are served.
const docsBase = "https://docs.example.test/"

// mapFetch is a Fetch serving docs by URI.
func mapFetch(docs map[string]string) func(context.Context, string) (io.ReadCloser, string, error) {
	return func(ctx context.Context, uri string) (io.ReadCloser, string, error) {
		content, ok := docs[uri]
		if !ok {
			return nil, "", fmt.Errorf("no document at %s", uri)
		}
		return io.NopCloser(strings.NewReader(content)), uri, nil
	}
}

// once reports the first failure of a scaling run, which the harness
// repeats, and tells the run to stop.
type once struct {
	t      *testing.T
	failed bool
}

func (o *once) fail(format string, args ...any) {
	if !o.failed {
		o.failed = true
		o.t.Errorf(format, args...)
	}
}

// loadDocs returns work that loads the entry of docs (docsBase +
// "openapi.json") with a Fetch from memory and describes every operation,
// failing when an operation's Err differs from wantErr.
func loadDocs(t *testing.T, docs map[string]string, wantErr bool) func() {
	l := &openapi.Loader{Fetch: mapFetch(docs)}
	o := &once{t: t}
	return func() {
		c, err := l.Load(context.Background(), docsBase+"openapi.json", nil)
		if err != nil {
			o.fail("Load: %v", err)
			return
		}
		ops := c.Operations()
		if len(ops) == 0 {
			o.fail("no operations")
		}
		for _, op := range ops {
			if (op.Err != nil) != wantErr {
				o.fail("%s %s: Err %v", op.Method, op.Path, op.Err)
				return
			}
		}
	}
}

// entryWith returns an entry document whose Paths Object holds paths.
func entryWith(paths string, extra ...string) string {
	return bare31(paths, extra...)
}

// Many documents, each a Path Item the entry references, all referencing
// one shared document; one Path Item chain through many documents; a
// cycle through many documents entered at every point, as Path Items and
// as parameters; one external URI referenced many times.
func TestDocumentsScale(t *testing.T) {
	wantLinear(t, "many documents", 100, func(n int) func() {
		docs := map[string]string{docsBase + "shared.json": `{"P":{"name":"p","in":"query"}}`}
		var paths []string
		for i := range n {
			paths = append(paths, fmt.Sprintf(`"/p%d":{"$ref":"d%d.json"}`, i, i))
			docs[fmt.Sprintf("%sd%d.json", docsBase, i)] = fmt.Sprintf(`{"get":{"operationId":"op%d","parameters":[{"$ref":"shared.json#/P"}]}}`, i)
		}
		docs[docsBase+"openapi.json"] = entryWith(strings.Join(paths, ","))
		return loadDocs(t, docs, false)
	})
	wantLinear(t, "a Path Item chain through many documents", 100, func(n int) func() {
		docs := map[string]string{docsBase + "openapi.json": entryWith(`"/p":{"$ref":"c0.json"},"/q":{"$ref":"c0.json"}`)}
		for i := range n {
			docs[fmt.Sprintf("%sc%d.json", docsBase, i)] = fmt.Sprintf(`{"$ref":"c%d.json"}`, i+1)
		}
		docs[fmt.Sprintf("%sc%d.json", docsBase, n)] = `{"get":{}}`
		return loadDocs(t, docs, false)
	})
	wantLinear(t, "a Path Item cycle through many documents, entered everywhere", 100, func(n int) func() {
		docs := map[string]string{}
		var paths []string
		for i := range n {
			paths = append(paths, fmt.Sprintf(`"/p%d":{"$ref":"c%d.json"}`, i, i))
			docs[fmt.Sprintf("%sc%d.json", docsBase, i)] = fmt.Sprintf(`{"$ref":"c%d.json"}`, (i+1)%n)
		}
		docs[docsBase+"openapi.json"] = entryWith(strings.Join(paths, ","))
		return loadDocs(t, docs, true)
	})
	wantLinear(t, "a parameter cycle through many documents, entered everywhere", 100, func(n int) func() {
		docs := map[string]string{}
		var paths []string
		for i := range n {
			paths = append(paths, fmt.Sprintf(`"/p%d":{"get":{"parameters":[{"$ref":"c%d.json#/P"}]}}`, i, i))
			docs[fmt.Sprintf("%sc%d.json", docsBase, i)] = fmt.Sprintf(`{"P":{"$ref":"c%d.json#/P"}}`, (i+1)%n)
		}
		docs[docsBase+"openapi.json"] = entryWith(strings.Join(paths, ","))
		return loadDocs(t, docs, true)
	})
	wantLinear(t, "one external URI referenced many times", 1000, func(n int) func() {
		var paths []string
		for i := range n {
			paths = append(paths, fmt.Sprintf(`"/p%d":{"get":{"parameters":[{"$ref":"shared.json#/P%d"}],"responses":{"200":{"$ref":"shared.json#/R"}}}}`, i, i%10))
		}
		var params []string
		for i := range 10 {
			params = append(params, fmt.Sprintf(`"P%d":{"name":"p%d","in":"query"}`, i, i))
		}
		docs := map[string]string{
			docsBase + "openapi.json": entryWith(strings.Join(paths, ",")),
			docsBase + "shared.json":  `{` + strings.Join(params, ",") + `,"R":{"description":"ok"}}`,
		}
		return loadDocs(t, docs, false)
	})
	t.Run("the shared document is fetched once", func(t *testing.T) {
		var paths []string
		for i := range 500 {
			paths = append(paths, fmt.Sprintf(`"/p%d":{"get":{"parameters":[{"$ref":"shared.json#/P"}]}}`, i))
		}
		m := newMemFetch(map[string]string{docsBase + "openapi.json": entryWith(strings.Join(paths, ",")), docsBase + "shared.json": `{"P":{"name":"p","in":"query"}}`})
		c := mustLoad(t, &openapi.Loader{Fetch: m.fetch}, docsBase+"openapi.json", nil)
		if len(c.Operations()) != 500 {
			t.Errorf("%d operations", len(c.Operations()))
		}
		if n := m.callsTo(docsBase + "shared.json"); n != 1 {
			t.Errorf("shared.json fetched %d times, want once", n)
		}
	})
}

// loadForms returns work that loads the entry of docs and describes every
// operation, each a multipart body whose fields (Media.Encoding) need the
// schema its reference reaches, failing unless each has fields fields.
func loadForms(t *testing.T, docs map[string]string, fields int) func() {
	l := &openapi.Loader{Fetch: mapFetch(docs)}
	o := &once{t: t}
	return func() {
		c, err := l.Load(context.Background(), docsBase+"openapi.json", nil)
		if err != nil {
			o.fail("Load: %v", err)
			return
		}
		for _, op := range c.Operations() {
			if op.Err != nil || op.Body == nil || len(op.Body.Media) != 1 {
				o.fail("%s: %v", op.Path, op.Err)
				return
			}
			if n := len(op.Body.Media[0].Encoding); n != fields {
				o.fail("%s: %d fields, want %d", op.Path, n, fields)
				return
			}
		}
	}
}

// formPaths writes n operations whose multipart body's schema is a $ref to
// ref(i).
func formPaths(n int, ref func(i int) string) string {
	var paths []string
	for i := range n {
		paths = append(paths, fmt.Sprintf(`"/p%d":{"post":{"requestBody":{"content":{"multipart/form-data":{"schema":{"$ref":%q}}}}}}`, i, ref(i)))
	}
	return strings.Join(paths, ",")
}

// The identifier index: one $id claimed by many schemas and referenced many
// times (each reference unresolvable, naming two claimants); many distinct
// $ids each referenced; a schema chain through many documents, each adding
// a field (load.go, Loader: identified URIs first; schema inspection costs
// what the document holds).
func TestIdentifiersScale(t *testing.T) {
	wantLinear(t, "a URI claimed by many schemas", 500, func(n int) func() {
		var schemas []string
		for i := range n {
			schemas = append(schemas, fmt.Sprintf(`"S%d":{"$id":"https://ids.example.test/dup","properties":{"f%d":{"type":"string"}}}`, i, i))
		}
		docs := map[string]string{docsBase + "openapi.json": entryWith(formPaths(n, func(int) string { return "https://ids.example.test/dup" }),
			`"components":{"schemas":{`+strings.Join(schemas, ",")+`}}`)}
		return loadForms(t, docs, 0)
	})
	wantLinear(t, "many identifiers, each referenced", 500, func(n int) func() {
		var schemas []string
		for i := range n {
			schemas = append(schemas, fmt.Sprintf(`"S%d":{"$id":"https://ids.example.test/s%d","properties":{"f":{"type":"string"}}}`, i, i))
		}
		// The identifiers live in a document only an unused component
		// reaches.
		docs := map[string]string{
			docsBase + "openapi.json": entryWith(formPaths(n, func(i int) string { return fmt.Sprintf("https://ids.example.test/s%d", i) }),
				`"components":{"schemas":{"All":{"$ref":"ids.json#/all"}}}`),
			docsBase + "ids.json": `{"all":{"$defs":{` + strings.Join(schemas, ",") + `}}}`,
		}
		return loadForms(t, docs, 1)
	})
	wantLinear(t, "a schema chain through many documents", 100, func(n int) func() {
		docs := map[string]string{docsBase + "openapi.json": entryWith(formPaths(4, func(int) string { return "s0.json" }))}
		for i := range n {
			docs[fmt.Sprintf("%ss%d.json", docsBase, i)] = fmt.Sprintf(`{"allOf":[{"$ref":"s%d.json"}],"properties":{"f%d":{"type":"string"}}}`, i+1, i)
		}
		docs[fmt.Sprintf("%ss%d.json", docsBase, n)] = `{"type":"object"}`
		return loadForms(t, docs, n)
	})
}

// yamlParse returns work that parses a YAML document.
func yamlParse(t *testing.T, doc string) func() {
	b := []byte(doc)
	o := &once{t: t}
	return func() {
		if _, err := openapi.Parse(context.Background(), b, testDocURI, nil); err != nil {
			o.fail("Parse: %v", err)
		}
	}
}

// YAML's shapes: many keys, block and flow; many anchors each aliased;
// many nests close to the depth bound; a long escaped scalar; a long block
// scalar.
func TestYAMLScale(t *testing.T) {
	wantLinear(t, "many keys, block", 5000, func(n int) func() {
		var b strings.Builder
		b.WriteString(yamlHead + "x-many:\n")
		for i := range n {
			fmt.Fprintf(&b, "  k%d: %d\n", i, i)
		}
		return yamlParse(t, b.String())
	})
	wantLinear(t, "many keys, flow", 5000, func(n int) func() {
		var b strings.Builder
		b.WriteString(yamlHead + "x-many: {")
		for i := range n {
			fmt.Fprintf(&b, "k%d: %d, ", i, i)
		}
		b.WriteString("end: 0}\n")
		return yamlParse(t, b.String())
	})
	wantLinear(t, "many anchors, each aliased", 2000, func(n int) func() {
		var b strings.Builder
		b.WriteString(yamlHead)
		for i := range n {
			fmt.Fprintf(&b, "x-a%d: &a%d [1, 2, 3]\nx-b%d: *a%d\n", i, i, i, i)
		}
		return yamlParse(t, b.String())
	})
	wantLinear(t, "nests close to the bound", 10, func(n int) func() {
		var b strings.Builder
		b.WriteString(yamlHead)
		for i := range n {
			fmt.Fprintf(&b, "x-d%d: %s%s\n", i, strings.Repeat("[", 990), strings.Repeat("]", 990))
		}
		return yamlParse(t, b.String())
	})
	wantLinear(t, "a long escaped scalar", 1<<16, func(n int) func() {
		return yamlParse(t, yamlHead+"x-s: \""+strings.Repeat(`a\tbé`, n/8)+"\"\n")
	})
	wantLinear(t, "a long block scalar", 4000, func(n int) func() {
		return yamlParse(t, yamlHead+"x-s: |\n"+strings.Repeat("  a line of text\n", n)+"x-end: 0\n")
	})
}

// sharedNameDoc is a document whose one component Parameter Object, named
// by nameKiB KiB, is referenced by refs operations.
func sharedNameDoc(refs, nameKiB int) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{`)
	for i := range refs {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"/p%d":{"get":{"parameters":[{"$ref":"#/components/parameters/P"}]}}`, i)
	}
	fmt.Fprintf(&b, `},"components":{"parameters":{"P":{"name":"%s","in":"query","schema":{}}}}}`, strings.Repeat("a", nameKiB<<10))
	return []byte(b.String())
}

// A shared parameter's identity is computed once per node, not once per
// referencing operation: every compiled or decoded form is computed at most
// once per document node. With the references fixed, describing the
// operations costs the same whatever the shared name's length: sixteen times
// the name, at most three times the time and bytes.
func TestSharedParameterIdentityOncePerNode(t *testing.T) {
	wantFlat(t, "a shared name 16 times longer", 8, func(n int) func() { return timedOperations(t, sharedNameDoc(4000, n)) })
}
