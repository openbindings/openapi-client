package openapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Supplemental public workloads: Loader's versionless contextual typing,
// Param.Schema, Client.Schema and References; OAS 3.1.2 section 4.3.2, with
// cost linear in the input. Incoming Parameter contexts alone establish
// schema fields; their local references establish shared/chained targets.
// No root OpenAPI/$schema/$id/anchor seeds the versionless bundle's schema map.
const versionlessBundleURI = "https://schemas.example.test/api/versionless.json"

type versionlessBundleNode struct {
	source, raw string
	edges       []schemaGraphEdge
}

type versionlessBundleFixture struct {
	entry  []byte
	bundle string
	nodes  []versionlessBundleNode
}

func versionlessBundleGraph(n int, chained bool) versionlessBundleFixture {
	var params, bundle strings.Builder
	f := versionlessBundleFixture{}
	value := "#/Leaf"
	if chained {
		value = "#/C000000"
	}
	raw := `{"$ref":"` + value + `"}`
	bundle.WriteByte('{')
	for i := range n {
		if i > 0 {
			params.WriteByte(',')
		}
		fmt.Fprintf(&params, `{"$ref":"versionless.json#/P%06d"}`, i)
		fmt.Fprintf(&bundle, `"P%06d":{"name":"q%06d","in":"query","schema":%s},`, i, i, raw)
		f.nodes = append(f.nodes, versionlessBundleNode{
			fmt.Sprintf("%s#/P%06d/schema", versionlessBundleURI, i), raw,
			[]schemaGraphEdge{{"/$ref", "$ref", value, versionlessBundleURI + value, versionlessBundleURI + value}},
		})
	}
	if chained {
		for i := range n {
			if i > 0 {
				bundle.WriteByte(',')
			}
			node := versionlessBundleNode{source: fmt.Sprintf("%s#/C%06d", versionlessBundleURI, i), raw: `{"type":"string"}`}
			if i+1 < n {
				next := fmt.Sprintf("#/C%06d", i+1)
				node.raw = `{"$ref":"` + next + `"}`
				node.edges = []schemaGraphEdge{{"/$ref", "$ref", next, versionlessBundleURI + next, versionlessBundleURI + next}}
			}
			fmt.Fprintf(&bundle, `"C%06d":%s`, i, node.raw)
			f.nodes = append(f.nodes, node)
		}
	} else {
		bundle.WriteString(`"Leaf":{"type":"string"}`)
		f.nodes = append(f.nodes, versionlessBundleNode{source: versionlessBundleURI + "#/Leaf", raw: `{"type":"string"}`})
	}
	bundle.WriteByte('}')
	f.bundle = bundle.String()
	f.entry = []byte(`{"openapi":"3.1.2","info":{"title":"Versionless closure cost","version":"1"},"paths":{"/probe":{"get":{"operationId":"probe","parameters":[` + params.String() + `],"responses":{"200":{"description":"ok"}}}}}}`)
	return f
}

type versionlessBundleRead struct {
	schema *openapi.Schema
	refs   []openapi.SchemaReference
	err    error
}

type versionlessBundleRun struct {
	loader  openapi.Loader
	fetch   *memFetch
	client  *openapi.Client
	loadErr error
	reads   []versionlessBundleRead
}

// Harness storage and the in-memory Fetch are prepared outside measurements.
func versionlessBundlePrepare(f versionlessBundleFixture) *versionlessBundleRun {
	fetch := newMemFetch(map[string]string{versionlessBundleURI: f.bundle})
	return &versionlessBundleRun{loader: openapi.Loader{Fetch: fetch.fetch}, fetch: fetch, reads: make([]versionlessBundleRead, len(f.nodes))}
}

func (r *versionlessBundleRun) parse(ctx context.Context, f versionlessBundleFixture) {
	r.client, r.loadErr = r.loader.Parse(ctx, f.entry, schemaGraphEntry, nil)
}

// One cold graph-read batch visits every authored schema once. It follows no target
// recursively: each References call has zero or one independently known edge.
// This exposes repeated whole-bundle work across many small public reads.
func (r *versionlessBundleRun) read(f versionlessBundleFixture) {
	if r.loadErr != nil || r.client == nil {
		return
	}
	for i, node := range f.nodes {
		got := &r.reads[i]
		got.schema, got.err = r.client.Schema(node.source)
		if got.err == nil && got.schema != nil {
			got.refs, got.err = got.schema.References()
		}
	}
}

// All Source/Raw/Base/Dialect, edge and retrieval checks stay outside timing.
func (r *versionlessBundleRun) check(t testing.TB, f versionlessBundleFixture) {
	t.Helper()
	if r.loadErr != nil || r.client == nil {
		t.Fatalf("versionless Parse: %v", r.loadErr)
	}
	for i, want := range f.nodes {
		got := r.reads[i]
		if got.err != nil || got.schema == nil {
			t.Fatalf("read %s: %v", want.source, got.err)
		}
		if got.schema.Source() != want.source || got.schema.Base() != versionlessBundleURI || got.schema.Dialect() != dialectOAS31 || string(got.schema.Raw()) != want.raw {
			t.Fatalf("metadata/Raw differs at %s", want.source)
		}
		schemaGraphWantEdges(t, got.refs, want.edges)
	}
	if !slices.Equal(r.fetch.callList(), []string{versionlessBundleURI}) {
		t.Fatalf("graph access retrieved beyond the one loaded bundle: %q", r.fetch.callList())
	}
	uris := r.client.DocumentURIs()
	slices.Sort(uris)
	if !slices.Equal(uris, []string{schemaGraphEntry, versionlessBundleURI}) {
		t.Fatalf("loaded inventory=%q", uris)
	}
}

func versionlessBundleControl(t testing.TB, f versionlessBundleFixture) {
	t.Helper()
	if !json.Valid(f.entry) || !json.Valid([]byte(f.bundle)) {
		t.Fatal("invalid versionless cost fixture")
	}
	t.Logf("entry bytes=%d sha256=%x bundle bytes=%d sha256=%x schemas=%d", len(f.entry), sha256.Sum256(f.entry), len(f.bundle), sha256.Sum256([]byte(f.bundle)), len(f.nodes))
	r := versionlessBundlePrepare(f)
	r.parse(t.Context(), f)
	if !slices.Equal(r.fetch.callList(), []string{versionlessBundleURI}) {
		t.Fatalf("Parse did not load the bundle before graph access: %q", r.fetch.callList())
	}
	r.read(f)
	r.check(t, f)
}

func TestSchemaVersionlessBundleWorkload(t *testing.T) {
	for _, chained := range []bool{false, true} {
		for _, n := range []int{128, 512} {
			t.Run(fmt.Sprintf("chain=%t/%d", chained, n), func(t *testing.T) { versionlessBundleControl(t, versionlessBundleGraph(n, chained)) })
		}
	}
}

// The unchanged 4x-input, five-trial, 12x-time/8x-byte gate is applied to both
// phases. ParseAndGraph includes loading, contextual reference discovery and
// a cold graph-read batch. GraphOnly retains fresh parsed clients but excludes
// loading work and its retained incoming-context state; it does not claim
// that work or retention is free. Root must reserve the timing window.
func TestSchemaVersionlessBundleScale(t *testing.T) {
	ctx := t.Context()
	for _, chained := range []bool{false, true} {
		family := "SharedLeaf"
		if chained {
			family = "SharedChain"
		}
		fixtures := [2]versionlessBundleFixture{versionlessBundleGraph(128, chained), versionlessBundleGraph(512, chained)}
		for _, f := range fixtures {
			versionlessBundleControl(t, f)
		}
		for _, phase := range []string{"ParseAndGraph", "GraphOnly"} {
			t.Run(family+"/"+phase, func(t *testing.T) {
				var runs [2][]*versionlessBundleRun
				for size, f := range fixtures {
					for range scaleRuns {
						r := versionlessBundlePrepare(f)
						if phase == "GraphOnly" {
							r.parse(ctx, f)
							if r.loadErr != nil {
								t.Fatal(r.loadErr)
							}
						}
						runs[size] = append(runs[size], r)
					}
				}
				best := [2]scaleCost{{1<<63 - 1, ^uint64(0), ^uint64(0)}, {1<<63 - 1, ^uint64(0), ^uint64(0)}}
				for trial := range scaleRuns {
					order := [2]int{0, 1}
					if trial%2 == 1 {
						order = [2]int{1, 0}
					}
					for _, size := range order {
						r, f := runs[size][trial], fixtures[size]
						cost := measureRun(func() {
							if phase == "ParseAndGraph" {
								r.parse(ctx, f)
							}
							r.read(f)
						})
						r.check(t, f)
						t.Logf("trial=%d contexts=%d ns=%d bytes=%d allocs=%d", trial+1, []int{128, 512}[size], cost.d.Nanoseconds(), cost.bytes, cost.mallocs)
						best[size].d = min(best[size].d, cost.d)
						best[size].bytes = min(best[size].bytes, cost.bytes)
						best[size].mallocs = min(best[size].mallocs, cost.mallocs)
					}
				}
				byteRatio := float64(best[1].bytes) / float64(max(best[0].bytes, 1))
				timeRatio := float64(best[1].d) / float64(max(best[0].d, 100*time.Microsecond))
				t.Logf("4x contexts: time %.4fx bytes %.4fx (independent best-of-%d minima)", timeRatio, byteRatio, scaleRuns)
				if byteRatio > scaleAllocBound {
					t.Errorf("versionless bytes %.4fx exceeds %dx", byteRatio, scaleAllocBound)
				}
				if !testing.Short() && timeRatio > scaleTimeBound {
					t.Errorf("versionless time %.4fx exceeds %dx", timeRatio, scaleTimeBound)
				}
				runtime.KeepAlive(runs)
			})
		}
	}
}

// Absolute cost supplements, with no Hand comparator. GraphOnly needs a fixed
// iteration count (for example -benchtime=100x) because each iteration parses
// a fresh client outside timing. ParseAndGraph can use duration calibration.
// Fixture controls and every result check remain outside measured intervals.
func benchmarkVersionlessBundle(b *testing.B, parseInside bool) {
	ctx := b.Context()
	for _, chained := range []bool{false, true} {
		family := "SharedLeaf"
		if chained {
			family = "SharedChain"
		}
		for _, n := range []int{128, 512} {
			b.Run(fmt.Sprintf("%s/%d", family, n), func(b *testing.B) {
				b.StopTimer()
				f := versionlessBundleGraph(n, chained)
				versionlessBundleControl(b, f)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					r := versionlessBundlePrepare(f)
					if !parseInside {
						r.parse(ctx, f)
						if r.loadErr != nil {
							b.Fatal(r.loadErr)
						}
					}
					b.StartTimer()
					if parseInside {
						r.parse(ctx, f)
					}
					r.read(f)
					b.StopTimer()
					r.check(b, f)
				}
			})
		}
	}
}

func BenchmarkSchemaVersionlessParseAndGraph(b *testing.B) { benchmarkVersionlessBundle(b, true) }
func BenchmarkSchemaVersionlessGraphOnly(b *testing.B)     { benchmarkVersionlessBundle(b, false) }
