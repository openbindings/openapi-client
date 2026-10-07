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

// Public workload: Loader follows references throughout loaded OpenAPI
// documents, including those reached through Schema references. OAS 3.1.2
// Parameter Object (4.8.12) makes example instance data. Incoming Schema
// references inside that data remain usable without inheriting its fake
// identifiers/dialect. The routing is independent of those data fields.
const (
	pendingContextBase   = "https://schemas.example.test/api/"
	pendingContextBundle = pendingContextBase + "pending-bundle.json"
	pendingContextTarget = pendingContextBase + "pending-target.json"
	pendingContextRaw    = `{"$ref":"pending-target.json#/T"}`
	pendingContextLeaf   = `{"type":"string"}`
)

type pendingContextFixture struct {
	entry                    []byte
	documents                map[string]string
	nodes                    []versionlessBundleNode
	fetches                  []string
	payloads, params, routes int
}

func pendingContextGraph(n int, sequential, foreign bool) pendingContextFixture {
	f := pendingContextFixture{documents: make(map[string]string), payloads: n, params: 1, routes: 1}
	if sequential {
		f.params, f.routes = n, n/4
	}
	dialect := dialect2020
	if foreign {
		dialect = "https://dialects.example.test/data"
	}
	var uses, bundle strings.Builder
	bundle.WriteByte('{')
	for p := range f.params {
		if p > 0 {
			bundle.WriteByte(',')
		}
		fmt.Fprintf(&bundle, `"P%06d":{"name":"q%06d","in":"query","schema":{"type":"object"},"example":{"$schema":%q,"$id":"data-only-%06d/"`, p, p, dialect, p)
		count := 1
		if !sequential {
			count = n
		}
		for j := range count {
			index := p
			member := "payload"
			if !sequential {
				index, member = j, fmt.Sprintf("s%06d", j)
			}
			if index > 0 {
				uses.WriteByte(',')
			}
			pointer := fmt.Sprintf("#/P%06d/example/%s", p, member)
			fmt.Fprintf(&uses, `"S%06d":{"$ref":"pending-bundle.json%s"}`, index, pointer)
			fmt.Fprintf(&bundle, `,%q:%s`, member, pendingContextRaw)
			f.nodes = append(f.nodes, versionlessBundleNode{pendingContextBundle + pointer, pendingContextRaw,
				[]schemaGraphEdge{{"/$ref", "$ref", "pending-target.json#/T", pendingContextTarget + "#/T", pendingContextTarget + "#/T"}}})
		}
		bundle.WriteString(`}}`)
	}
	bundle.WriteByte('}')
	f.documents[pendingContextBundle] = bundle.String()
	f.documents[pendingContextTarget] = `{"T":` + pendingContextLeaf + `}`
	// The cold batch contains all payloads first, then all declared schemas.
	for p := range f.params {
		f.nodes = append(f.nodes, versionlessBundleNode{source: fmt.Sprintf("%s#/P%06d/schema", pendingContextBundle, p), raw: `{"type":"object"}`})
	}
	for wave := range f.routes {
		relayName, carrierName := fmt.Sprintf("pending-relay-%06d.json", wave), fmt.Sprintf("pending-carrier-%06d.json", wave)
		f.documents[pendingContextBase+relayName] = `{"$ref":"` + carrierName + `#/components/schemas/Carrier"}`
		var params strings.Builder
		first, end := 0, 1
		if sequential {
			first, end = wave*4, (wave+1)*4
		}
		for p := first; p < end; p++ {
			if p > first {
				params.WriteByte(',')
			}
			fmt.Fprintf(&params, `{"$ref":"pending-bundle.json#/P%06d"}`, p)
		}
		schemas := `"Carrier":{}`
		if wave+1 < f.routes {
			schemas += fmt.Sprintf(`,"Next":{"$ref":"pending-relay-%06d.json"}`, wave+1)
		}
		f.documents[pendingContextBase+carrierName] = `{"openapi":"3.1.2","info":{"title":"Carrier","version":"1"},"paths":{"/carrier":{"get":{"parameters":[` + params.String() + `],"responses":{"200":{"description":"ok"}}}}},"components":{"schemas":{` + schemas + `}}}`
	}
	f.entry = []byte(schemaGraphDoc("3.1.2", uses.String()+`,"Relay":{"$ref":"pending-relay-000000.json"}`))
	for uri := range f.documents {
		f.fetches = append(f.fetches, uri)
	}
	slices.Sort(f.fetches)
	return f
}

type pendingContextRun struct {
	loader  openapi.Loader
	fetch   *memFetch
	client  *openapi.Client
	loadErr error
	reads   []versionlessBundleRead
}

// Fixture generation, Fetch construction and result storage are untimed.
func pendingContextPrepare(f pendingContextFixture) *pendingContextRun {
	fetch := newMemFetch(f.documents)
	return &pendingContextRun{loader: openapi.Loader{Fetch: fetch.fetch}, fetch: fetch, reads: make([]versionlessBundleRead, len(f.nodes))}
}

func (r *pendingContextRun) parse(ctx context.Context, f pendingContextFixture) {
	r.client, r.loadErr = r.loader.Parse(ctx, f.entry, schemaGraphEntry, nil)
}

// One cold batch: n payloads plus 1 or n declared Parameter schemas. No target
// traversal, source/Raw inspection, assertion or fixture work occurs here.
func (r *pendingContextRun) read(f pendingContextFixture) {
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

// Before any accessor, Parse must already have fetched every independent
// route and the genuine shared target. Fake data-only targets are excluded.
func (r *pendingContextRun) checkLoaded(t testing.TB, f pendingContextFixture) {
	t.Helper()
	if r.loadErr != nil || r.client == nil {
		t.Fatalf("pending-context Parse: %v", r.loadErr)
	}
	got := r.fetch.callList()
	slices.Sort(got)
	if !slices.Equal(got, f.fetches) {
		t.Fatalf("fetches=%q want %q", got, f.fetches)
	}
}

func (r *pendingContextRun) checkGraph(t testing.TB, f pendingContextFixture) {
	t.Helper()
	for i, node := range f.nodes {
		got := r.reads[i]
		if got.err != nil || got.schema == nil {
			t.Fatalf("read %s: %v", node.source, got.err)
		}
		if got.schema.Source() != node.source || got.schema.Base() != pendingContextBundle || got.schema.Dialect() != dialectOAS31 || string(got.schema.Raw()) != node.raw {
			t.Fatalf("metadata/Raw differs at %s", node.source)
		}
		schemaGraphWantEdges(t, got.refs, node.edges)
		if i < f.payloads && (got.refs[0].Target.Base() != pendingContextTarget || got.refs[0].Target.Dialect() != dialectOAS31 || string(got.refs[0].Target.Raw()) != pendingContextLeaf) {
			t.Fatal("genuine target metadata differs")
		}
	}
	// Validate all routing schemas outside both measured phases. In particular,
	// Carrier is a fragment in an OpenAPI document, not its document root.
	for wave := range f.routes {
		relay := fmt.Sprintf("%spending-relay-%06d.json", pendingContextBase, wave)
		carrier := fmt.Sprintf("%spending-carrier-%06d.json", pendingContextBase, wave)
		value := fmt.Sprintf("pending-carrier-%06d.json#/components/schemas/Carrier", wave)
		h := schemaGraphGet(t, r.client, relay)
		if strings.TrimSuffix(h.Source(), "#") != relay || h.Base() != relay || h.Dialect() != dialectOAS31 || string(h.Raw()) != f.documents[relay] {
			t.Fatal("relay metadata differs")
		}
		refs := schemaGraphRefs(t, h)
		schemaGraphWantEdges(t, refs, []schemaGraphEdge{{"/$ref", "$ref", value, carrier + "#/components/schemas/Carrier", carrier + "#/components/schemas/Carrier"}})
		if string(refs[0].Target.Raw()) != `{}` || refs[0].Target.Base() != carrier || refs[0].Target.Dialect() != dialectOAS31 || len(schemaGraphRefs(t, refs[0].Target)) != 0 {
			t.Fatal("Carrier fragment differs")
		}
		if wave+1 < f.routes {
			next := fmt.Sprintf("pending-relay-%06d.json", wave+1)
			h := schemaGraphGet(t, r.client, carrier+"#/components/schemas/Next")
			refs := schemaGraphRefs(t, h)
			if len(refs) != 1 {
				t.Fatalf("Next has %d edges, want one", len(refs))
			}
			ref := refs[0]
			if ref.At != "/$ref" || ref.Keyword != "$ref" || ref.Value != next || ref.URI != pendingContextBase+next || ref.Err != nil || ref.Target == nil {
				t.Fatalf("Next edge differs: %+v", ref)
			}
			if strings.TrimSuffix(ref.Target.Source(), "#") != pendingContextBase+next || string(ref.Target.Raw()) != f.documents[pendingContextBase+next] {
				t.Fatal("Next target differs")
			}
		}
	}
	wantURIs := append(slices.Clone(f.fetches), schemaGraphEntry)
	slices.Sort(wantURIs)
	gotURIs := r.client.DocumentURIs()
	slices.Sort(gotURIs)
	if !slices.Equal(gotURIs, wantURIs) {
		t.Fatalf("inventory=%q want %q", gotURIs, wantURIs)
	}
	r.checkLoaded(t, f)
}

func pendingContextControl(t testing.TB, f pendingContextFixture) {
	t.Helper()
	if !json.Valid(f.entry) {
		t.Fatal("invalid entry fixture")
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\n%s\n", schemaGraphEntry, f.entry)
	bytes := len(f.entry)
	for _, uri := range f.fetches {
		body := f.documents[uri]
		if !json.Valid([]byte(body)) {
			t.Fatalf("invalid fixture %s", uri)
		}
		bytes += len(body)
		fmt.Fprintf(hash, "%s\n%s\n", uri, body)
	}
	t.Logf("payloads=%d parameters=%d relay_carrier_pairs=%d fetched_docs=%d total_json_bytes=%d batch_reads=%d batch_edges=%d fixture_sha256=%x", f.payloads, f.params, f.routes, len(f.fetches), bytes, len(f.nodes), f.payloads, hash.Sum(nil))
	r := pendingContextPrepare(f)
	r.parse(t.Context(), f)
	r.checkLoaded(t, f)
	r.read(f)
	r.checkGraph(t, f)
}

func TestSchemaPendingContextWorkload(t *testing.T) {
	for _, sequential := range []bool{false, true} {
		for _, foreign := range []bool{false, true} {
			for _, n := range []int{128, 512} {
				t.Run(fmt.Sprintf("sequential=%t/foreign=%t/%d", sequential, foreign, n), func(t *testing.T) { pendingContextControl(t, pendingContextGraph(n, sequential, foreign)) })
			}
		}
	}
}

// The established five-trial, 12x-time/8x-byte gate uses 4x payloads. Fanout
// keeps one Parameter and one route; SequentialContexts grows Parameters and
// route length 4x too (four Parameters per carrier). ParseOnly measures full
// loading/discovery; GraphOnly excludes Parse and its retained state, measuring
// the cold public batch alone. It does not claim loading/retention is free.
func TestSchemaPendingContextScale(t *testing.T) {
	ctx := t.Context()
	for _, sequential := range []bool{false, true} {
		family := "Fanout"
		if sequential {
			family = "SequentialContexts"
		}
		for _, foreign := range []bool{false, true} {
			fixtures := [2]pendingContextFixture{pendingContextGraph(128, sequential, foreign), pendingContextGraph(512, sequential, foreign)}
			for _, f := range fixtures {
				pendingContextControl(t, f)
			}
			for _, phase := range []string{"ParseOnly", "GraphOnly"} {
				t.Run(fmt.Sprintf("%s/foreign=%t/%s", family, foreign, phase), func(t *testing.T) {
					var runs [2][]*pendingContextRun
					for size, f := range fixtures {
						for range scaleRuns {
							r := pendingContextPrepare(f)
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
							r.checkLoaded(t, f)
							if phase == "ParseOnly" {
								r.read(f)
							}
							r.checkGraph(t, f)
							t.Logf("trial=%d payloads=%d parameters=%d pairs=%d batch_reads=%d ns=%d bytes=%d allocs=%d", trial+1, f.payloads, f.params, f.routes, len(f.nodes), cost.d.Nanoseconds(), cost.bytes, cost.mallocs)
							best[size].d = min(best[size].d, cost.d)
							best[size].bytes = min(best[size].bytes, cost.bytes)
							best[size].mallocs = min(best[size].mallocs, cost.mallocs)
						}
					}
					byteRatio := float64(best[1].bytes) / float64(max(best[0].bytes, 1))
					timeRatio := float64(best[1].d) / float64(max(best[0].d, 100*time.Microsecond))
					t.Logf("4x payloads: time %.4fx bytes %.4fx (independent best-of-%d minima)", timeRatio, byteRatio, scaleRuns)
					if byteRatio > scaleAllocBound {
						t.Errorf("pending-context bytes %.4fx exceeds %dx", byteRatio, scaleAllocBound)
					}
					if !testing.Short() && timeRatio > scaleTimeBound {
						t.Errorf("pending-context time %.4fx exceeds %dx", timeRatio, scaleTimeBound)
					}
					runtime.KeepAlive(runs)
				})
			}
		}
	}
}
