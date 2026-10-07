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
const deepResourceEntry = "https://nested-cost.example.test/openapi.json"
const deepResourceRoot = deepResourceEntry + "#/components/schemas/S"

type deepResourceFixture struct{ doc, raw, rootBase, leafBase, leafSource, leafURI string }

func newDeepResourceFixture(depth int, nested bool) deepResourceFixture {
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
	f := deepResourceFixture{raw: b.String(), rootBase: "https://nested-cost.example.test/root.json"}
	f.leafBase = f.rootBase
	if nested {
		f.leafBase = "https://nested-cost.example.test/" + strings.Repeat("directory-segment/", depth) + "child.json"
	}
	f.leafURI = f.leafBase + "#leaf"
	f.leafSource = deepResourceRoot + strings.Repeat("/not", depth)
	f.doc = `{"openapi":"3.1.2","info":{"title":"Independent cost probe","version":"1"},"paths":{},"components":{"schemas":{"S":` + f.raw + `}}}`
	return f
}

func deepResourceParse(t *testing.T, f deepResourceFixture) *openapi.Client {
	t.Helper()
	fetch := newMemFetch(nil)
	c, err := (&openapi.Loader{Fetch: fetch.fetch}).Parse(t.Context(), []byte(f.doc), deepResourceEntry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetch.callList()) != 0 {
		t.Fatal("cost fixture attempted retrieval")
	}
	return c
}

func deepResourceControl(t *testing.T, f deepResourceFixture) {
	t.Helper()
	if !json.Valid([]byte(f.doc)) {
		t.Fatal("invalid deep fixture")
	}
	t.Logf("fixture bytes=%d sha256=%x", len(f.doc), sha256.Sum256([]byte(f.doc)))
	c := deepResourceParse(t, f)
	s := schemaGraphGet(t, c, deepResourceRoot)
	if s.Source() != deepResourceRoot || s.Base() != f.rootBase || !bytes.Equal(s.Raw(), []byte(f.raw)) {
		t.Fatal("root fixture metadata differs")
	}
	leaf := schemaGraphGet(t, c, f.leafURI)
	if leaf.Source() != f.leafSource || leaf.Base() != f.leafBase || leaf.Dialect() != dialectOAS31 {
		t.Fatal("leaf fixture metadata differs")
	}
	wantRaw := `{"$anchor":"leaf","type":"string"}`
	if strings.HasSuffix(f.leafBase, "/child.json") {
		wantRaw = `{"$id":"directory-segment/child.json","$anchor":"leaf","type":"string"}`
	}
	if string(leaf.Raw()) != wantRaw {
		t.Fatal("leaf fixture Raw differs")
	}
	if refs := schemaGraphRefs(t, s); len(refs) != 0 {
		t.Error("reference-free fixture has edges")
	}
}

// An untimed functional control checks the workload's public values without
// entering the separate scaling measurement below.
func TestSchemaDeepResourceLookupWorkload(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, depth := range []int{128, 512} {
			t.Run(fmt.Sprintf("nested=%t/depth=%d", nested, depth), func(t *testing.T) { deepResourceControl(t, newDeepResourceFixture(depth, nested)) })
		}
	}
}

func TestSchemaFirstDeepResourceLookupScale(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "FixedBase"
		if nested {
			name = "RelativeBases"
		}
		t.Run(name, func(t *testing.T) {
			fixtures := [2]deepResourceFixture{newDeepResourceFixture(128, nested), newDeepResourceFixture(512, nested)}
			var clients [2][]*openapi.Client
			for size, f := range fixtures {
				deepResourceControl(t, f)
				for range scaleRuns {
					clients[size] = append(clients[size], deepResourceParse(t, f))
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
					cost := measureRun(func() { s, err = clients[size][trial].Schema(deepResourceRoot) })
					// No Source/Raw/Base/Dialect access runs in the timed interval.
					if err != nil || s == nil || s.Source() != deepResourceRoot {
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
