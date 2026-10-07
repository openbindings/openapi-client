package openapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// An inferred schema resource that retrieval later reveals to be instance
// data (Loader: "references whose scope depends on that inference are
// unresolvable. The documents already retrieved remain available, and parts
// independent of that inference remain usable"), ErrUnresolved, and the
// Schema/Document contracts.
// Each inferred example resource discovers the Parameter context that disproves
// it. Unlike the independent-route workloads, these enter the refusal path.
// Every fourth example has an explicitly reached, fully independent resource
// (absolute $id and supported $schema; Core 8.2.1/8.1.1) that must stay usable.
const disprovedInferenceBase = "https://schemas.example.test/api/"

type disprovedInferenceNode struct {
	kind, uri, source, base, dialect, raw, value string
	edges                                        []schemaGraphEdge
}

type disprovedInferenceFixture struct {
	entry     []byte
	documents map[string]string
	nodes     []disprovedInferenceNode
	mandatory []string
	n, holes  int
}

func disprovedInferenceGraph(n int) disprovedInferenceFixture {
	const bundleURI = disprovedInferenceBase + "feedback-bundle.json"
	f := disprovedInferenceFixture{documents: make(map[string]string), n: n, holes: n / 4, mandatory: []string{bundleURI}}
	var bundle, uses strings.Builder
	bundle.WriteByte('{')
	for i := range n {
		if i > 0 {
			bundle.WriteByte(',')
			uses.WriteByte(',')
		}
		parameter := fmt.Sprintf("P%06d", i)
		pointer := "#/" + parameter + "/example/payload"
		parentID := fmt.Sprintf("%sdata-only-%06d/", disprovedInferenceBase, i)
		value := "feedback-bundle.json" + pointer
		fmt.Fprintf(&uses, `"S%06d":{"$ref":%q}`, i, value)
		fmt.Fprintf(&bundle, `%q:{"name":"q%06d","in":"query","schema":{"type":"object"},"example":{"$schema":%q,"$id":"data-only-%06d/","payload":{"$ref":"carrier.json#/components/schemas/C"}`, parameter, i, dialect2020, i)
		f.nodes = append(f.nodes,
			disprovedInferenceNode{kind: "affected", uri: bundleURI + pointer, value: "carrier.json#/components/schemas/C"},
			disprovedInferenceNode{kind: "affected", uri: fmt.Sprintf("%s#/components/schemas/S%06d", schemaGraphEntry, i), value: value},
			disprovedInferenceNode{kind: "alias", uri: parentID},
			disprovedInferenceNode{kind: "alias", uri: parentID + "#/payload"},
			disprovedInferenceNode{kind: "valid", uri: bundleURI + "#/" + parameter + "/schema", source: bundleURI + "#/" + parameter + "/schema", base: bundleURI, dialect: dialectOAS31, raw: `{"type":"object"}`})
		if i%4 == 0 {
			id := fmt.Sprintf("%sfeedback-independent-%06d.json", disprovedInferenceBase, i)
			source := bundleURI + "#/" + parameter + "/example/Independent"
			raw := `{"$id":"` + id + `","$schema":"` + dialect2020 + `","$defs":{"Leaf":{"type":"string"}},"properties":{"v":{"$ref":"#/$defs/Leaf"}}}`
			bundle.WriteString(`,"Independent":` + raw)
			fmt.Fprintf(&uses, `,"I%06d":{"$ref":"feedback-bundle.json#/%s/example/Independent"}`, i, parameter)
			for _, uri := range []string{source, id} {
				f.nodes = append(f.nodes, disprovedInferenceNode{kind: "independent", uri: uri, source: source, base: id, dialect: dialect2020, raw: raw,
					edges: []schemaGraphEdge{{"/properties/v/$ref", "$ref", "#/$defs/Leaf", id + "#/$defs/Leaf", source + "/$defs/Leaf"}}})
			}
			f.nodes = append(f.nodes, disprovedInferenceNode{kind: "alias", uri: parentID + "#/Independent"})
		}
		bundle.WriteString(`}}`)
		carrier := parentID + "carrier.json"
		f.mandatory = append(f.mandatory, carrier)
		f.documents[carrier] = `{"openapi":"3.1.2","info":{"title":"Feedback carrier","version":"1"},"paths":{"/carrier":{"get":{"parameters":[{"$ref":"../feedback-bundle.json#/` + parameter + `"}],"responses":{"200":{"description":"ok"}}}}},"components":{"schemas":{"C":{}}}}`
	}
	bundle.WriteByte('}')
	f.documents[bundleURI] = bundle.String()
	// This alternative target is supplied without requiring or forbidding its
	// retrieval. Previously inferred retrievals cannot be retroactively avoided.
	f.documents[disprovedInferenceBase+"carrier.json"] = `{"openapi":"3.1.2","info":{"title":"Other target","version":"1"},"paths":{},"components":{"schemas":{"C":{"type":"string"}}}}`
	f.entry = []byte(schemaGraphDoc("3.1.2", uses.String()+`,"Stable":{"type":"integer"}`))
	f.nodes = append(f.nodes, disprovedInferenceNode{kind: "valid", uri: schemaGraphEntry + "#/components/schemas/Stable", source: schemaGraphEntry + "#/components/schemas/Stable", base: schemaGraphEntry, dialect: dialectOAS31, raw: `{"type":"integer"}`})
	return f
}

type disprovedInferenceRead struct {
	schema             *openapi.Schema
	lookupErr, refsErr error
	refs               []openapi.SchemaReference
	refsCalled         bool
}

type disprovedInferenceRun struct {
	loader    openapi.Loader
	fetch     *memFetch
	client    *openapi.Client
	loadErr   error
	reads     []disprovedInferenceRead
	before    []string
	inventory []string
}

func disprovedInferencePrepare(f disprovedInferenceFixture) *disprovedInferenceRun {
	fetch := newMemFetch(f.documents)
	return &disprovedInferenceRun{loader: openapi.Loader{Fetch: fetch.fetch}, fetch: fetch, reads: make([]disprovedInferenceRead, len(f.nodes))}
}

func (r *disprovedInferenceRun) parse(ctx context.Context, f disprovedInferenceFixture) {
	r.client, r.loadErr = r.loader.Parse(ctx, f.entry, schemaGraphEntry, nil)
}

// Fixed lookup batch; References is attempted after successful non-alias
// lookups. Which part reports an error may vary, so the number of
// References calls varies; it is recorded outside timing. No oracle or
// metadata inspection occurs in this batch.
func (r *disprovedInferenceRun) read(f disprovedInferenceFixture) {
	if r.loadErr != nil || r.client == nil {
		return
	}
	for i, node := range f.nodes {
		got := &r.reads[i]
		got.schema, got.lookupErr = r.client.Schema(node.uri)
		if node.kind != "alias" && got.lookupErr == nil && got.schema != nil {
			got.refsCalled = true
			got.refs, got.refsErr = got.schema.References()
		}
	}
}

// Run before graph access. Require each intended carrier, while retaining
// every successfully reached fixture document and imposing no exact fetch set.
func (r *disprovedInferenceRun) checkLoaded(t testing.TB, f disprovedInferenceFixture) {
	t.Helper()
	if r.loadErr != nil || r.client == nil {
		t.Fatalf("feedback Parse: %v", r.loadErr)
	}
	r.before, r.inventory = r.fetch.callList(), r.client.DocumentURIs()
	for _, uri := range f.mandatory {
		if !slices.Contains(r.before, uri) || !slices.Contains(r.inventory, uri) || len(r.client.Document(uri)) == 0 {
			t.Fatalf("intended feedback carrier/bundle not retained: %s", uri)
		}
	}
	for _, uri := range r.before {
		if _, supplied := f.documents[uri]; supplied && (!slices.Contains(r.inventory, uri) || len(r.client.Document(uri)) == 0) {
			t.Fatalf("reached document was discarded: %s", uri)
		}
	}
}

func disprovedInferenceWantUnresolved(t testing.TB, node disprovedInferenceNode, got disprovedInferenceRead) {
	t.Helper()
	if got.lookupErr != nil {
		if !errors.Is(got.lookupErr, openapi.ErrUnresolved) {
			t.Errorf("affected lookup %s: %v", node.uri, got.lookupErr)
		}
		return
	}
	if got.schema == nil || !got.refsCalled {
		t.Fatalf("affected lookup %s returned no handle or error", node.uri)
	}
	failed := errors.Is(got.refsErr, openapi.ErrUnresolved)
	if got.refsErr != nil && !failed {
		t.Errorf("affected References %s: %v", node.uri, got.refsErr)
	}
	if got.refsErr == nil && len(got.refs) != 1 {
		t.Errorf("affected References %s: %d edges without a top-level error", node.uri, len(got.refs))
	}
	for _, ref := range got.refs {
		if ref.At != "/$ref" || ref.Keyword != "$ref" || ref.Value != node.value || ref.Target != nil {
			t.Errorf("affected reference succeeded or changed: %s %+v", node.uri, ref)
		}
		if ref.Err != nil && !errors.Is(ref.Err, openapi.ErrUnresolved) {
			t.Errorf("affected edge error: %v", ref.Err)
		}
		failed = failed || errors.Is(ref.Err, openapi.ErrUnresolved)
	}
	if !failed {
		t.Errorf("affected schema %s reported no ErrUnresolved", node.uri)
	}
}

func (r *disprovedInferenceRun) checkGraph(t testing.TB, f disprovedInferenceFixture) (int, int) {
	t.Helper()
	refsCalls, returnedEdges := 0, 0
	for i, node := range f.nodes {
		got := r.reads[i]
		if got.refsCalled {
			refsCalls++
		}
		returnedEdges += len(got.refs)
		switch node.kind {
		case "alias":
			if got.lookupErr == nil {
				t.Errorf("disproved parent alias succeeded: %s", node.uri)
			}
		case "affected":
			disprovedInferenceWantUnresolved(t, node, got)
		default:
			if got.lookupErr != nil || got.refsErr != nil || got.schema == nil || !got.refsCalled {
				t.Fatalf("independent lookup %s failed: %v / %v", node.uri, got.lookupErr, got.refsErr)
			}
			if got.schema.Source() != node.source || got.schema.Base() != node.base || got.schema.Dialect() != node.dialect || string(got.schema.Raw()) != node.raw {
				t.Fatalf("independent metadata differs at %s", node.uri)
			}
			schemaGraphWantEdges(t, got.refs, node.edges)
			if node.kind == "independent" && (got.refs[0].Target.Base() != node.base || got.refs[0].Target.Dialect() != dialect2020 || string(got.refs[0].Target.Raw()) != `{"type":"string"}`) {
				t.Fatal("independent leaf inherited rejected ancestry")
			}
		}
	}
	if !slices.Equal(r.client.DocumentURIs(), r.inventory) || !slices.Equal(r.fetch.callList(), r.before) {
		t.Error("graph access changed inventory or fetched documents")
	}
	return refsCalls, returnedEdges
}

func disprovedInferenceControl(t testing.TB, f disprovedInferenceFixture) {
	t.Helper()
	if !json.Valid(f.entry) {
		t.Fatal("invalid entry fixture")
	}
	keys := make([]string, 0, len(f.documents))
	for uri := range f.documents {
		keys = append(keys, uri)
	}
	slices.Sort(keys)
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\n%s\n", schemaGraphEntry, f.entry)
	bytes := len(f.entry)
	for _, uri := range keys {
		body := f.documents[uri]
		if !json.Valid([]byte(body)) {
			t.Fatalf("invalid fixture %s", uri)
		}
		bytes += len(body)
		fmt.Fprintf(hash, "%s\n%s\n", uri, body)
	}
	r := disprovedInferencePrepare(f)
	r.parse(t.Context(), f)
	r.checkLoaded(t, f)
	r.read(f)
	refsCalls, returnedEdges := r.checkGraph(t, f)
	t.Logf("payloads=%d holes=%d supplied_json_bytes=%d supplied_docs=%d fetched_attempts=%d inventory_docs=%d batch_lookups=%d batch_references=%d returned_edges=%d fixture_sha256=%x", f.n, f.holes, bytes, len(f.documents)+1, len(r.before), len(r.inventory), len(f.nodes), refsCalls, returnedEdges, hash.Sum(nil))
}

func TestSchemaDisprovedInferenceWorkload(t *testing.T) {
	for _, n := range []int{128, 512} {
		t.Run(fmt.Sprint(n), func(t *testing.T) { disprovedInferenceControl(t, disprovedInferenceGraph(n)) })
	}
}

// Four times the feedback contexts and independent holes, under the
// harness's five-trial 12x-time/8x-byte bounds. ParseOnly includes final
// refusal/discovery work; GraphOnly excludes Parse and its retained state.
// Harness, validation and fixture work remain outside measurement.
func TestSchemaDisprovedInferenceScale(t *testing.T) {
	ctx := t.Context()
	fixtures := [2]disprovedInferenceFixture{disprovedInferenceGraph(128), disprovedInferenceGraph(512)}
	for _, f := range fixtures {
		disprovedInferenceControl(t, f)
	}
	for _, phase := range []string{"ParseOnly", "GraphOnly"} {
		t.Run(phase, func(t *testing.T) {
			var runs [2][]*disprovedInferenceRun
			for size, f := range fixtures {
				for range scaleRuns {
					r := disprovedInferencePrepare(f)
					if phase == "GraphOnly" {
						r.parse(ctx, f)
						r.checkLoaded(t, f)
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
						if phase == "ParseOnly" {
							r.parse(ctx, f)
						} else {
							r.read(f)
						}
					})
					if phase == "ParseOnly" {
						r.checkLoaded(t, f)
						r.read(f)
					}
					refsCalls, returnedEdges := r.checkGraph(t, f)
					t.Logf("trial=%d contexts=%d holes=%d fetched_attempts=%d batch_lookups=%d batch_references=%d returned_edges=%d ns=%d bytes=%d allocs=%d", trial+1, f.n, f.holes, len(r.before), len(f.nodes), refsCalls, returnedEdges, cost.d.Nanoseconds(), cost.bytes, cost.mallocs)
					best[size].d = min(best[size].d, cost.d)
					best[size].bytes = min(best[size].bytes, cost.bytes)
					best[size].mallocs = min(best[size].mallocs, cost.mallocs)
				}
			}
			byteRatio := float64(best[1].bytes) / float64(max(best[0].bytes, 1))
			timeRatio := float64(best[1].d) / float64(max(best[0].d, 100*time.Microsecond))
			t.Logf("4x contexts: time %.4fx bytes %.4fx (independent best-of-%d minima)", timeRatio, byteRatio, scaleRuns)
			if byteRatio > scaleAllocBound {
				t.Errorf("feedback bytes %.4fx exceeds %dx", byteRatio, scaleAllocBound)
			}
			if !testing.Short() && timeRatio > scaleTimeBound {
				t.Errorf("feedback time %.4fx exceeds %dx", timeRatio, scaleTimeBound)
			}
			runtime.KeepAlive(runs)
		})
	}
}
