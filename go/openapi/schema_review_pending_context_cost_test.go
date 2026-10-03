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
	review9PendingBase   = "https://schemas.example.test/api/"
	review9PendingBundle = review9PendingBase + "pending-bundle.json"
	review9PendingTarget = review9PendingBase + "pending-target.json"
	review9PendingRaw    = `{"$ref":"pending-target.json#/T"}`
	review9PendingLeaf   = `{"type":"string"}`
)

type review9PendingFixture struct {
	entry                    []byte
	documents                map[string]string
	nodes                    []review9VersionlessNode
	fetches                  []string
	payloads, params, routes int
}

func review9PendingGraph(n int, sequential, foreign bool) review9PendingFixture {
	f := review9PendingFixture{documents: make(map[string]string), payloads: n, params: 1, routes: 1}
	if sequential {
		f.params, f.routes = n, n/4
	}
	dialect := schema9JSON
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
			fmt.Fprintf(&bundle, `,%q:%s`, member, review9PendingRaw)
			f.nodes = append(f.nodes, review9VersionlessNode{review9PendingBundle + pointer, review9PendingRaw,
				[]schema9Edge{{"/$ref", "$ref", "pending-target.json#/T", review9PendingTarget + "#/T", review9PendingTarget + "#/T"}}})
		}
		bundle.WriteString(`}}`)
	}
	bundle.WriteByte('}')
	f.documents[review9PendingBundle] = bundle.String()
	f.documents[review9PendingTarget] = `{"T":` + review9PendingLeaf + `}`
	// The cold batch contains all payloads first, then all declared schemas.
	for p := range f.params {
		f.nodes = append(f.nodes, review9VersionlessNode{source: fmt.Sprintf("%s#/P%06d/schema", review9PendingBundle, p), raw: `{"type":"object"}`})
	}
	for wave := range f.routes {
		relayName, carrierName := fmt.Sprintf("pending-relay-%06d.json", wave), fmt.Sprintf("pending-carrier-%06d.json", wave)
		f.documents[review9PendingBase+relayName] = `{"$ref":"` + carrierName + `#/components/schemas/Carrier"}`
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
		f.documents[review9PendingBase+carrierName] = `{"openapi":"3.1.2","info":{"title":"Carrier","version":"1"},"paths":{"/carrier":{"get":{"parameters":[` + params.String() + `],"responses":{"200":{"description":"ok"}}}}},"components":{"schemas":{` + schemas + `}}}`
	}
	f.entry = []byte(schema9Doc("3.1.2", uses.String()+`,"Relay":{"$ref":"pending-relay-000000.json"}`))
	for uri := range f.documents {
		f.fetches = append(f.fetches, uri)
	}
	slices.Sort(f.fetches)
	return f
}

type review9PendingRun struct {
	loader  openapi.Loader
	fetch   *memFetch
	client  *openapi.Client
	loadErr error
	reads   []review9VersionlessRead
}

// Fixture generation, Fetch construction and result storage are untimed.
func review9PendingPrepare(f review9PendingFixture) *review9PendingRun {
	fetch := newMemFetch(f.documents)
	return &review9PendingRun{loader: openapi.Loader{Fetch: fetch.fetch}, fetch: fetch, reads: make([]review9VersionlessRead, len(f.nodes))}
}

func (r *review9PendingRun) parse(ctx context.Context, f review9PendingFixture) {
	r.client, r.loadErr = r.loader.Parse(ctx, f.entry, schema9Entry, nil)
}

// One cold batch: n payloads plus 1 or n declared Parameter schemas. No target
// traversal, source/Raw inspection, assertion or fixture work occurs here.
func (r *review9PendingRun) read(f review9PendingFixture) {
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
func (r *review9PendingRun) checkLoaded(t testing.TB, f review9PendingFixture) {
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

func (r *review9PendingRun) checkGraph(t testing.TB, f review9PendingFixture) {
	t.Helper()
	for i, node := range f.nodes {
		got := r.reads[i]
		if got.err != nil || got.schema == nil {
			t.Fatalf("read %s: %v", node.source, got.err)
		}
		if got.schema.Source() != node.source || got.schema.Base() != review9PendingBundle || got.schema.Dialect() != schema9OAS31 || string(got.schema.Raw()) != node.raw {
			t.Fatalf("metadata/Raw differs at %s", node.source)
		}
		schema9WantEdges(t, got.refs, node.edges)
		if i < f.payloads && (got.refs[0].Target.Base() != review9PendingTarget || got.refs[0].Target.Dialect() != schema9OAS31 || string(got.refs[0].Target.Raw()) != review9PendingLeaf) {
			t.Fatal("genuine target metadata differs")
		}
	}
	// Validate all routing schemas outside both measured phases. In particular,
	// Carrier is a fragment in an OpenAPI document, not its document root.
	for wave := range f.routes {
		relay := fmt.Sprintf("%spending-relay-%06d.json", review9PendingBase, wave)
		carrier := fmt.Sprintf("%spending-carrier-%06d.json", review9PendingBase, wave)
		value := fmt.Sprintf("pending-carrier-%06d.json#/components/schemas/Carrier", wave)
		h := schema9Get(t, r.client, relay)
		if strings.TrimSuffix(h.Source(), "#") != relay || h.Base() != relay || h.Dialect() != schema9OAS31 || string(h.Raw()) != f.documents[relay] {
			t.Fatal("relay metadata differs")
		}
		refs := schema9Refs(t, h)
		schema9WantEdges(t, refs, []schema9Edge{{"/$ref", "$ref", value, carrier + "#/components/schemas/Carrier", carrier + "#/components/schemas/Carrier"}})
		if string(refs[0].Target.Raw()) != `{}` || refs[0].Target.Base() != carrier || refs[0].Target.Dialect() != schema9OAS31 || len(schema9Refs(t, refs[0].Target)) != 0 {
			t.Fatal("Carrier fragment differs")
		}
		if wave+1 < f.routes {
			next := fmt.Sprintf("pending-relay-%06d.json", wave+1)
			h := schema9Get(t, r.client, carrier+"#/components/schemas/Next")
			refs := schema9Refs(t, h)
			if len(refs) != 1 {
				t.Fatalf("Next has %d edges, want one", len(refs))
			}
			ref := refs[0]
			if ref.At != "/$ref" || ref.Keyword != "$ref" || ref.Value != next || ref.URI != review9PendingBase+next || ref.Err != nil || ref.Target == nil {
				t.Fatalf("Next edge differs: %+v", ref)
			}
			if strings.TrimSuffix(ref.Target.Source(), "#") != review9PendingBase+next || string(ref.Target.Raw()) != f.documents[review9PendingBase+next] {
				t.Fatal("Next target differs")
			}
		}
	}
	wantURIs := append(slices.Clone(f.fetches), schema9Entry)
	slices.Sort(wantURIs)
	gotURIs := r.client.DocumentURIs()
	slices.Sort(gotURIs)
	if !slices.Equal(gotURIs, wantURIs) {
		t.Fatalf("inventory=%q want %q", gotURIs, wantURIs)
	}
	r.checkLoaded(t, f)
}

func review9PendingControl(t testing.TB, f review9PendingFixture) {
	t.Helper()
	if !json.Valid(f.entry) {
		t.Fatal("invalid entry fixture")
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\n%s\n", schema9Entry, f.entry)
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
	r := review9PendingPrepare(f)
	r.parse(t.Context(), f)
	r.checkLoaded(t, f)
	r.read(f)
	r.checkGraph(t, f)
}

func TestReview9PendingContextFixtureControls(t *testing.T) {
	for _, sequential := range []bool{false, true} {
		for _, foreign := range []bool{false, true} {
			for _, n := range []int{128, 512} {
				t.Run(fmt.Sprintf("sequential=%t/foreign=%t/%d", sequential, foreign, n), func(t *testing.T) { review9PendingControl(t, review9PendingGraph(n, sequential, foreign)) })
			}
		}
	}
}

// The established five-trial, 12x-time/8x-byte gate uses 4x payloads. Fanout
// keeps one Parameter and one route; SequentialContexts grows Parameters and
// route length 4x too (four Parameters per carrier). ParseOnly measures full
// loading/discovery; GraphOnly excludes Parse and its retained state, measuring
// the cold public batch alone. It does not claim loading/retention is free.
func TestReview9PendingContextScale(t *testing.T) {
	ctx := t.Context()
	for _, sequential := range []bool{false, true} {
		family := "Fanout"
		if sequential {
			family = "SequentialContexts"
		}
		for _, foreign := range []bool{false, true} {
			fixtures := [2]review9PendingFixture{review9PendingGraph(128, sequential, foreign), review9PendingGraph(512, sequential, foreign)}
			for _, f := range fixtures {
				review9PendingControl(t, f)
			}
			for _, phase := range []string{"ParseOnly", "GraphOnly"} {
				t.Run(fmt.Sprintf("%s/foreign=%t/%s", family, foreign, phase), func(t *testing.T) {
					var runs [2][]*review9PendingRun
					for size, f := range fixtures {
						for range scaleRuns {
							r := review9PendingPrepare(f)
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
