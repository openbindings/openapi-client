package schema2020_test

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
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// Invariants over every schema of the documents under testdata/corpus, in
// every direction, and properties of Project as a function: "The same input
// gives the same output"; "It is safe to call concurrently, and each call
// returns what it would alone"; its Projection's "three fields are
// caller-owned copies"; and it "retrieves no documents and retains nothing
// after it returns" (Project and Projection documentation).

const corpusBase = "https://corpus.example.test/"

// corpusCase is a directory of testdata/corpus: its entry document,
// openapi.json or openapi.yaml, and the documents it refers to, served at
// https://corpus.example.test/<directory>/<file>.
type corpusCase struct {
	name, dir, entry string
}

func corpusCases(t testing.TB) []corpusCase {
	t.Helper()
	dirs, err := os.ReadDir(filepath.Join("testdata", "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	var out []corpusCase
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join("testdata", "corpus", d.Name())
		for _, name := range []string{"openapi.json", "openapi.yaml"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				out = append(out, corpusCase{d.Name(), dir, name})
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no corpus documents")
	}
	return out
}

func (cc corpusCase) uri(file string) string { return corpusBase + cc.name + "/" + file }

// fetch serves the case's files; fetched, if not nil, counts the calls.
func (cc corpusCase) fetch(fetched *atomic.Int64) func(context.Context, string) (io.ReadCloser, string, error) {
	return func(_ context.Context, uri string) (io.ReadCloser, string, error) {
		if fetched != nil {
			fetched.Add(1)
		}
		rel, ok := strings.CutPrefix(uri, corpusBase+cc.name+"/")
		if !ok || strings.Contains(rel, "..") {
			return nil, "", fmt.Errorf("no document at %s", uri)
		}
		f, err := os.Open(filepath.Join(cc.dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, "", err
		}
		return f, "", nil
	}
}

func (cc corpusCase) load(t testing.TB, fetched *atomic.Int64) *openapi.Client {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(cc.dir, cc.entry))
	if err != nil {
		t.Fatal(err)
	}
	c, err := (&openapi.Loader{Fetch: cc.fetch(fetched)}).Parse(context.Background(), content, cc.uri(cc.entry), nil)
	if err != nil {
		t.Fatalf("%s: %v", cc.name, err)
	}
	return c
}

// labeled is a schema handle and where it was found.
type labeled struct {
	label  string
	schema *openapi.Schema
}

// everySchema lists the entry document's components or definitions and
// every descriptor schema: parameters, bodies, responses, item schemas,
// Encoding fields and headers, in a stable order.
func everySchema(t testing.TB, c *openapi.Client) []labeled {
	t.Helper()
	var out []labeled
	add := func(label string, s *openapi.Schema) {
		if s != nil {
			out = append(out, labeled{label, s})
		}
	}
	var doc struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
		Components  struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(c.Document(""), &doc); err != nil {
		t.Fatal(err)
	}
	prefix, names := "#/components/schemas/", slices.Sorted(maps.Keys(doc.Components.Schemas))
	if c.Version() == v20 {
		prefix, names = "#/definitions/", slices.Sorted(maps.Keys(doc.Definitions))
	}
	entry := c.DocumentURIs()[0]
	for _, n := range names {
		uri := entry + prefix + strings.TrimPrefix(refFor(n), "#/$defs/")
		s, err := c.Schema(uri)
		if err != nil {
			continue // a component that is an unresolvable $ref in 2.0 or 3.0
		}
		add("component "+n, s)
	}
	params := func(prefix string, ps []*openapi.Param) {
		for _, p := range ps {
			add(prefix+" "+p.In+" "+p.Name, p.Schema)
			for _, h := range p.Headers {
				add(prefix+" "+p.Name+" header "+h.Name, h.Schema)
			}
		}
	}
	media := func(prefix string, ms []*openapi.Media) {
		for _, m := range ms {
			add(prefix+" "+m.Type, m.Schema)
			add(prefix+" "+m.Type+" items", m.ItemSchema)
			params(prefix+" "+m.Type+" field", m.Encoding)
		}
	}
	for _, op := range c.Operations() {
		params(op.Key+" param", op.Params)
		if op.Body != nil {
			media(op.Key+" body", op.Body.Media)
		}
		for _, r := range op.Responses {
			media(op.Key+" "+r.Key, r.Media)
			params(op.Key+" "+r.Key+" header", r.Headers)
		}
	}
	return out
}

// Every corpus schema, in every direction, gives a Projection with the
// invariants checkProjection names, with or without an *Error.
func TestProjectCorpusInvariants(t *testing.T) {
	for _, cc := range corpusCases(t) {
		t.Run(cc.name, func(t *testing.T) {
			c := cc.load(t, nil)
			schemas := everySchema(t, c)
			if len(schemas) < 3 {
				t.Fatalf("only %d schemas", len(schemas))
			}
			lossy := 0
			for _, ls := range schemas {
				for _, d := range directions {
					t.Run(ls.label+"/"+dirName(d), func(t *testing.T) {
						p, err := schema2020.Project(c, ls.schema, d)
						if err != nil {
							lossy++
						}
						checkProjection(t, c, ls.schema, d, p, err)
					})
				}
			}
			t.Logf("%d schemas, %d lossy projections", len(schemas), lossy)
		})
	}
}

// snapshot is a Projection with its Issues, in a comparable form.
type snapshot struct {
	root    string
	defs    map[string]string
	sources map[string]string
	issues  string
}

func take(p *schema2020.Projection, err error) snapshot {
	var s snapshot
	if p != nil {
		s.root = string(p.Root)
		s.defs = map[string]string{}
		for k, d := range p.Defs {
			s.defs[k] = string(d)
		}
		s.sources = maps.Clone(p.Sources)
	}
	var pe *schema2020.Error
	if errors.As(err, &pe) {
		for _, is := range pe.Issues {
			s.issues += is.Source + " " + is.At + "\n"
		}
	} else if err != nil {
		s.issues = "error: " + err.Error()
	}
	return s
}

// Two calls give the same bytes: Root, every Defs value, Sources and the
// Issues (Project: "The same input gives the same output").
func TestProjectDeterministic(t *testing.T) {
	for _, cc := range corpusCases(t) {
		t.Run(cc.name, func(t *testing.T) {
			c := cc.load(t, nil)
			for _, ls := range everySchema(t, c) {
				for _, d := range directions {
					p, err := schema2020.Project(c, ls.schema, d)
					if p == nil {
						t.Errorf("%s %s: no Projection (error %v)", ls.label, dirName(d), err)
						continue
					}
					a := take(p, err)
					b := take(schema2020.Project(c, ls.schema, d))
					if !reflect.DeepEqual(a, b) {
						t.Errorf("%s %s: two calls differ:\n%+v\n%+v", ls.label, dirName(d), a, b)
					}
				}
			}
		})
	}
}

// Concurrent calls, on one schema and on many, give what sequential calls
// give (run with -race).
func TestProjectConcurrent(t *testing.T) {
	for _, cc := range corpusCases(t) {
		t.Run(cc.name, func(t *testing.T) {
			c := cc.load(t, nil)
			schemas := everySchema(t, c)
			want := map[string]snapshot{}
			for _, ls := range schemas {
				for _, d := range directions {
					p, err := schema2020.Project(c, ls.schema, d)
					if p == nil {
						t.Fatalf("%s %s: no Projection (error %v)", ls.label, dirName(d), err)
					}
					want[ls.label+dirName(d)] = take(p, err)
				}
			}
			var wg sync.WaitGroup
			for g := 0; g < 8; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					for i := range schemas {
						ls := schemas[(i+g)%len(schemas)]
						for _, d := range directions {
							got := take(schema2020.Project(c, ls.schema, d))
							if !reflect.DeepEqual(got, want[ls.label+dirName(d)]) {
								t.Errorf("goroutine %d, %s %s: differs from a sequential call", g, ls.label, dirName(d))
							}
						}
					}
				}(g)
			}
			wg.Wait()
		})
	}
}

// Projection: "All three fields are caller-owned copies." Changing one
// Projection changes neither the authored schema nor a later Projection.
func TestProjectCallerOwnedCopies(t *testing.T) {
	for _, v := range editions {
		t.Run(v, func(t *testing.T) {
			c := load(t, "json", docText(v, "", `"R":{"type":"object","properties":{"a":{"$ref":"#C/A"}}},"A":{"type":"string","maxLength":3}`), nil)
			r := comp(t, c, v, "R")
			raw := bytes.Clone(r.Raw())
			p := project(t, c, r, schema2020.Neutral)
			want := take(p, nil)
			for i := range p.Root {
				p.Root[i] = ' '
			}
			for _, k := range slices.Collect(maps.Keys(p.Defs)) {
				d := p.Defs[k]
				for i := range d {
					d[i] = ' '
				}
				p.Defs[k+"x"] = nil
			}
			for k := range p.Sources {
				p.Sources[k] = "changed"
			}
			if !bytes.Equal(r.Raw(), raw) {
				t.Errorf("Raw changed to %s", r.Raw())
			}
			again := take(schema2020.Project(c, r, schema2020.Neutral))
			if !reflect.DeepEqual(again, want) {
				t.Errorf("a later Projection changed:\n%+v\nwant %+v", again, want)
			}
		})
	}
}

// Project: "Project is lazy, retrieves no documents". A reference to a
// document that could not be loaded stays unresolved; projecting does not
// fetch it.
func TestProjectRetrievesNothing(t *testing.T) {
	for _, cc := range corpusCases(t) {
		t.Run(cc.name, func(t *testing.T) {
			var fetched atomic.Int64
			c := cc.load(t, &fetched)
			before := fetched.Load()
			for _, ls := range everySchema(t, c) {
				for _, d := range directions {
					if p, err := schema2020.Project(c, ls.schema, d); p == nil {
						t.Fatalf("%s %s: no Projection (error %v)", ls.label, dirName(d), err)
					}
				}
			}
			if n := fetched.Load() - before; n != 0 {
				t.Errorf("Project fetched %d documents", n)
			}
		})
	}
}

// Project: "retains nothing after it returns". Projecting every schema of
// a large document in every direction leaves the live heap where it was,
// within a small allowance, after a garbage collection. The authored graph
// is walked through References first, so that any state the client builds
// lazily for its own handles exists before the measurement.
func TestProjectRetainsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a large document")
	}
	c := load(t, "json", syntheticDocument(v30, 500), nil)
	schemas := everySchema(t, c)
	warm(t, schemas)
	before := liveHeap()
	var produced int
	for _, ls := range schemas {
		for _, d := range directions {
			p, err := schema2020.Project(c, ls.schema, d)
			if err != nil {
				t.Fatalf("%s: %v", ls.label, err)
			}
			produced += len(p.Root)
			for _, def := range p.Defs {
				produced += len(def)
			}
		}
	}
	after := liveHeap()
	allowance := int64(512<<10) + int64(produced)/50
	if grew := int64(after) - int64(before); grew > allowance {
		t.Errorf("live heap grew by %d bytes after projections totalling %d bytes (allowance %d)", grew, produced, allowance)
	}
	runtime.KeepAlive(c)
}

// warm walks every schema reachable from schemas through References.
func warm(t testing.TB, schemas []labeled) {
	t.Helper()
	seen := map[string]bool{}
	var queue []*openapi.Schema
	for _, ls := range schemas {
		queue = append(queue, ls.schema)
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		_ = s.Raw()
		refs, _ := s.References()
		for _, r := range refs {
			if r.Target != nil && !seen[r.Target.Source()] {
				seen[r.Target.Source()] = true
				queue = append(queue, r.Target)
			}
		}
	}
}

func liveHeap() uint64 {
	var m runtime.MemStats
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// Project: "Project returns an error, and no Projection, when c has loaded no
// document at the URI s is written in."
func TestProjectOtherClient(t *testing.T) {
	doc := docText(v31, "", `"R":{"type":"string"}`)
	c := load(t, "json", doc, nil)
	r := comp(t, c, v31, "R")
	other, err := openapi.Parse(context.Background(), []byte(doc), "https://elsewhere.example.test/openapi.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := schema2020.Project(other, r, schema2020.Neutral)
	if err == nil || p != nil {
		t.Errorf("Project with a client that has not loaded %s = %v, %v; want an error and no Projection", r.Source(), p, err)
	}
	var pe *schema2020.Error
	if errors.As(err, &pe) {
		t.Errorf("the error is a *schema2020.Error: %v", err)
	}
}

// Project: "s must come from c or from a Client derived from c with With".
// A schema from a derived Client projects as the same schema from c does.
func TestProjectDerivedClient(t *testing.T) {
	for _, v := range editions {
		t.Run(v, func(t *testing.T) {
			c := load(t, "json", docText(v, "", `"R":{"type":"object","properties":{"a":{"$ref":"#C/B"}}},"B":{"type":"string","maxLength":2}`), nil)
			derived := c.With(func(o *openapi.Options) { o.BaseURL = "https://other.example.test/base" })
			want := take(schema2020.Project(c, comp(t, c, v, "R"), schema2020.Request))
			for name, s := range map[string]*openapi.Schema{
				"Client.Schema of the derived Client": comp(t, derived, v, "R"),
				"Client.Schema of c":                  comp(t, c, v, "R"),
			} {
				p, err := schema2020.Project(c, s, schema2020.Request)
				if err != nil || p == nil {
					t.Fatalf("%s: Project = %v, %v", name, p, err)
				}
				checkProjection(t, c, s, schema2020.Request, p, err)
				if got := take(p, err); !reflect.DeepEqual(got, want) {
					t.Errorf("%s: %+v, want %+v", name, got, want)
				}
			}
		})
	}
}

// Project "returns an error, and no Projection, when c has loaded no
// document at the URI s is written in": here c loaded an entry document at
// the same URI, but not the other document s is written in.
func TestProjectClientWithoutDocument(t *testing.T) {
	others := map[string]string{modelsURI: `{"Pet":{"type":"object","properties":{"tag":{"$ref":"#/Tag"}}},"Tag":{"type":"string"}}`}
	withModels := load(t, "json", docText(v31, "", `"R":{"properties":{"p":{"$ref":"models.json#/Pet"}}}`), others)
	pet := schemaAt(t, withModels, "models.json#/Pet")
	without := load(t, "json", docText(v31, "", `"R":{"type":"string"}`), nil)
	p, err := schema2020.Project(without, pet, schema2020.Neutral)
	if err == nil || p != nil {
		t.Errorf("Project with a client that loaded nothing at %s = %v, %v; want an error and no Projection", pet.Source(), p, err)
	}
	if p, err := schema2020.Project(withModels, pet, schema2020.Neutral); err != nil || p == nil {
		t.Errorf("Project with the client that loaded it = %v, %v", p, err)
	}
}
