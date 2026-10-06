package openapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The first Schema lookup in a deeply nested schema resource costs time and
// bytes linear in its depth, as a read costs O(size of what it returns or
// compares) (Client.Schema; Schema.Source, Base, Raw and References; JSON
// Schema 2020-12 core section 8.2.1, $id). Fixture generation and
// validation use only public values.
const review9CostEntry = "https://stage9-cost.example.test/openapi.json"
const review9CostRoot = review9CostEntry + "#/components/schemas/S"

type review9DeepFixture struct{ doc, raw, rootBase, leafBase, leafSource, leafURI string }

func review9Deep(depth int, nested bool) review9DeepFixture {
	var b strings.Builder
	b.WriteString(`{"$id":"root.json",`)
	for range depth {
		b.WriteString(`"not":{`)
		if nested {
			b.WriteString(`"$id":"directory-segment/child.json",`)
		}
	}
	b.WriteString(`"$anchor":"leaf","type":"string"`)
	b.WriteString(strings.Repeat("}", depth+1))
	f := review9DeepFixture{raw: b.String(), rootBase: "https://stage9-cost.example.test/root.json"}
	f.leafBase = f.rootBase
	if nested {
		f.leafBase = "https://stage9-cost.example.test/" + strings.Repeat("directory-segment/", depth) + "child.json"
	}
	f.leafURI = f.leafBase + "#leaf"
	f.leafSource = review9CostRoot + strings.Repeat("/not", depth)
	f.doc = `{"openapi":"3.1.2","info":{"title":"Independent cost probe","version":"1"},"paths":{},"components":{"schemas":{"S":` + f.raw + `}}}`
	return f
}

func review9DeepParse(t *testing.T, f review9DeepFixture) *openapi.Client {
	t.Helper()
	fetch := newMemFetch(nil)
	c, err := (&openapi.Loader{Fetch: fetch.fetch}).Parse(t.Context(), []byte(f.doc), review9CostEntry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetch.callList()) != 0 {
		t.Fatal("cost fixture attempted retrieval")
	}
	return c
}

func review9DeepControl(t *testing.T, f review9DeepFixture) {
	t.Helper()
	if !json.Valid([]byte(f.doc)) {
		t.Fatal("invalid deep fixture")
	}
	t.Logf("fixture bytes=%d sha256=%x", len(f.doc), sha256.Sum256([]byte(f.doc)))
	c := review9DeepParse(t, f)
	s := schema9Get(t, c, review9CostRoot)
	if s.Source() != review9CostRoot || s.Base() != f.rootBase || !bytes.Equal(s.Raw(), []byte(f.raw)) {
		t.Fatal("root fixture metadata differs")
	}
	leaf := schema9Get(t, c, f.leafURI)
	if leaf.Source() != f.leafSource || leaf.Base() != f.leafBase || leaf.Dialect() != schema9OAS31 {
		t.Fatal("leaf fixture metadata differs")
	}
	wantRaw := `{"$anchor":"leaf","type":"string"}`
	if strings.HasSuffix(f.leafBase, "/child.json") {
		wantRaw = `{"$id":"directory-segment/child.json","$anchor":"leaf","type":"string"}`
	}
	if string(leaf.Raw()) != wantRaw {
		t.Fatal("leaf fixture Raw differs")
	}
	if refs := schema9Refs(t, s); len(refs) != 0 {
		t.Error("reference-free fixture has edges")
	}
}

// An untimed functional control checks the workload's public values without
// entering the separate scaling measurement below.
func TestReview9DeepResourceFixtureControls(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, depth := range []int{128, 512} {
			t.Run(fmt.Sprintf("nested=%t/depth=%d", nested, depth), func(t *testing.T) { review9DeepControl(t, review9Deep(depth, nested)) })
		}
	}
}

func TestReview9FirstDeepResourceScale(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "FixedBase"
		if nested {
			name = "RelativeBases"
		}
		t.Run(name, func(t *testing.T) {
			fixtures := [2]review9DeepFixture{review9Deep(128, nested), review9Deep(512, nested)}
			var clients [2][]*openapi.Client
			for size, f := range fixtures {
				review9DeepControl(t, f)
				for range scaleRuns {
					clients[size] = append(clients[size], review9DeepParse(t, f))
				}
			}
			best := [2]scaleCost{{1<<63 - 1, ^uint64(0), ^uint64(0)}, {1<<63 - 1, ^uint64(0), ^uint64(0)}}
			for trial := range scaleRuns {
				order := [2]int{0, 1}
				if trial%2 == 1 {
					order = [2]int{1, 0}
				}
				for _, size := range order {
					var s *openapi.Schema
					var err error
					cost := measureRun(func() { s, err = clients[size][trial].Schema(review9CostRoot) })
					// No Source/Raw/Base/Dialect access runs in the timed interval.
					if err != nil || s == nil || s.Source() != review9CostRoot {
						t.Fatalf("first lookup differs: %v", err)
					}
					t.Logf("trial=%d depth=%d ns=%d bytes=%d allocs=%d", trial+1, []int{128, 512}[size], cost.d.Nanoseconds(), cost.bytes, cost.mallocs)
					best[size].d = min(best[size].d, cost.d)
					best[size].bytes = min(best[size].bytes, cost.bytes)
					best[size].mallocs = min(best[size].mallocs, cost.mallocs)
				}
			}
			byteRatio := float64(best[1].bytes) / float64(max(best[0].bytes, 1))
			timeRatio := float64(best[1].d) / float64(max(best[0].d, 100*time.Microsecond))
			t.Logf("4x depth: time %.4fx bytes %.4fx (independent best-of-%d minima)", timeRatio, byteRatio, scaleRuns)
			if byteRatio > scaleAllocBound {
				t.Errorf("first deep lookup bytes %.4fx exceeds %dx", byteRatio, scaleAllocBound)
			}
			if !testing.Short() && timeRatio > scaleTimeBound {
				t.Errorf("first deep lookup time %.4fx exceeds %dx", timeRatio, scaleTimeBound)
			}
			runtime.KeepAlive(clients)
		})
	}
}
