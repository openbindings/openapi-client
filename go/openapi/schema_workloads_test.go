package openapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Synthetic schema graph workloads. First and warm costs are absolute
// measurements, not a ratio to another implementation or to hand-written
// code. Sources/edges are derived from the authored fixture, outside the
// timer. Run First cases with a fixed iteration count (e.g.
// -benchtime=100x): each timed first read needs a fresh client built outside
// the timer. Warm cases may use duration-based calibration.
// Authority: Client.Schema, SchemaReference, References; JSON Schema
// 2020-12 Core sections 8.2 and 9.2.1.
type schema9Workload struct {
	doc         string
	root        string
	queries     [3]string // physical pointer, resource URI, anchor URI
	querySource string
	edges       []schema9Edge
}

func schema9WorkloadGraph(n int) schema9Workload {
	const root = schema9Entry + "#/components/schemas/Graph"
	const base = "https://schemas.example.test/api/graph/"
	var b strings.Builder
	b.WriteString(`"Graph":{"$id":"graph/root.json","$defs":{`)
	f := schema9Workload{root: root}
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		next := (i + 1) % n
		fmt.Fprintf(&b, `"N%d":{"$id":"node-%d.json","$anchor":"node","type":"object","properties":{"next":{"$ref":"node-%d.json#node"},"value":{"type":"integer"}},"default":{"$ref":"not-a-reference.json"}}`, i, i, next)
		f.edges = append(f.edges, schema9Edge{fmt.Sprintf("/$defs/N%d/properties/next/$ref", i), "$ref", fmt.Sprintf("node-%d.json#node", next), fmt.Sprintf("%snode-%d.json#node", base, next), fmt.Sprintf("%s/$defs/N%d", root, next)})
	}
	b.WriteString(`}}`)
	f.doc = schema9Doc("3.1.2", b.String())
	f.querySource = fmt.Sprintf("%s/$defs/N%d", root, n/2)
	f.queries = [3]string{f.querySource, fmt.Sprintf("%snode-%d.json", base, n/2), fmt.Sprintf("%snode-%d.json#node", base, n/2)}
	return f
}

func schema9CheckWorkload(t testing.TB, f schema9Workload) {
	t.Helper()
	c := schema9Parse(t, f.doc, nil)
	for _, q := range f.queries {
		s := schema9Get(t, c, q)
		if s.Source() != f.querySource {
			t.Fatalf("fixture query %q source=%q want %q", q, s.Source(), f.querySource)
		}
	}
	schema9WantEdges(t, schema9Refs(t, schema9Get(t, c, f.root)), f.edges)
}

func TestSchema9BenchmarkFixture(t *testing.T) {
	var fixtures []schema9Workload
	for _, n := range []int{8, 32, 128, 256, 2048} {
		f := schema9WorkloadGraph(n)
		if !json.Valid([]byte(f.doc)) {
			t.Fatal("invalid fixture")
		}
		t.Logf("nodes=%d bytes=%d sha256=%x", n, len(f.doc), sha256.Sum256([]byte(f.doc)))
		fixtures = append(fixtures, f)
	}
	for _, f := range fixtures {
		schema9CheckWorkload(t, f)
	}
}

var schema9Sink *openapi.Schema
var schema9ReferenceSink []openapi.SchemaReference

func BenchmarkSchema9LookupFirst(b *testing.B) {
	for _, n := range []int{128, 2048} {
		f := schema9WorkloadGraph(n)
		for k, name := range []string{"Pointer", "Resource", "Anchor"} {
			b.Run(fmt.Sprintf("%s/%d", name, n), func(b *testing.B) {
				b.StopTimer()
				schema9CheckWorkload(b, f)
				b.ReportAllocs()
				for range b.N {
					c := schema9Parse(b, f.doc, nil)
					b.StartTimer()
					s, err := c.Schema(f.queries[k])
					b.StopTimer()
					if err != nil || s == nil || s.Source() != f.querySource {
						b.Fatalf("first lookup: %v %v", s, err)
					}
					schema9Sink = s
				}
			})
		}
	}
}

func BenchmarkSchema9LookupWarm(b *testing.B) {
	for _, n := range []int{128, 2048} {
		f := schema9WorkloadGraph(n)
		for k, name := range []string{"Pointer", "Resource", "Anchor"} {
			b.Run(fmt.Sprintf("%s/%d", name, n), func(b *testing.B) {
				b.StopTimer()
				schema9CheckWorkload(b, f)
				c := schema9Parse(b, f.doc, nil)
				s := schema9Get(b, c, f.queries[k])
				if s.Source() != f.querySource {
					b.Fatal("wrong warmup target")
				}
				b.ReportAllocs()
				b.ResetTimer()
				b.StartTimer()
				for range b.N {
					var err error
					schema9Sink, err = c.Schema(f.queries[k])
					if err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				if schema9Sink == nil || schema9Sink.Source() != f.querySource {
					b.Fatal("wrong final target")
				}
			})
		}
	}
}

func BenchmarkSchema9ReferencesFirst(b *testing.B) {
	for _, n := range []int{32, 256, 2048} {
		f := schema9WorkloadGraph(n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.StopTimer()
			schema9CheckWorkload(b, f)
			b.ReportAllocs()
			for range b.N {
				c := schema9Parse(b, f.doc, nil)
				s := schema9Get(b, c, f.root)
				b.StartTimer()
				r, err := s.References()
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				schema9WantEdges(b, r, f.edges)
				schema9ReferenceSink = r
			}
		})
	}
}

func BenchmarkSchema9ReferencesWarm(b *testing.B) {
	for _, n := range []int{32, 256, 2048} {
		f := schema9WorkloadGraph(n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.StopTimer()
			schema9CheckWorkload(b, f)
			c := schema9Parse(b, f.doc, nil)
			s := schema9Get(b, c, f.root)
			schema9WantEdges(b, schema9Refs(b, s), f.edges)
			b.ReportAllocs()
			b.ResetTimer()
			b.StartTimer()
			for range b.N {
				var err error
				schema9ReferenceSink, err = s.References()
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			schema9WantEdges(b, schema9ReferenceSink, f.edges)
		})
	}
}

// Hostile-input scaling, under the shared 4x-input / 8x-bytes / 12x-time
// harness: many identifier lookups, a broad graph with distinct targets,
// ancestry depth, and many entry points into one legacy reference chain.
// These exercise public graph reads, excluding Parse from measured work.
// The first-use cases provision one fresh client per harness sample.
func TestSchema9GraphScale(t *testing.T) {
	wantLinearBytes(t, "first lookup of every resource", 256, func(n int) func() {
		f := schema9WorkloadGraph(n)
		clients := make([]*openapi.Client, scaleRuns)
		for i := range clients {
			clients[i] = schema9Parse(t, f.doc, nil)
		}
		run := 0
		return func() {
			c := clients[run]
			run++
			for i := range n {
				u := fmt.Sprintf("https://schemas.example.test/api/graph/node-%d.json#node", i)
				s := schema9Get(t, c, u)
				if s.Source() != fmt.Sprintf("%s/$defs/N%d", f.root, i) {
					t.Fatal("wrong scale lookup")
				}
			}
		}
	})
	wantLinearBytes(t, "first reference walk", 256, func(n int) func() {
		f := schema9WorkloadGraph(n)
		handles := make([]*openapi.Schema, scaleRuns)
		for i := range handles {
			handles[i] = schema9Get(t, schema9Parse(t, f.doc, nil), f.root)
		}
		run := 0
		return func() { r := schema9Refs(t, handles[run]); run++; schema9WantEdges(t, r, f.edges) }
	})
	wantLinearBytes(t, "warm reference copies", 256, func(n int) func() {
		f := schema9WorkloadGraph(n)
		s := schema9Get(t, schema9Parse(t, f.doc, nil), f.root)
		schema9WantEdges(t, schema9Refs(t, s), f.edges)
		return func() {
			for range 8 {
				schema9WantEdges(t, schema9Refs(t, s), f.edges)
			}
		}
	})
	wantFlat(t, "warm anchor lookup independent of sibling count", 128, func(n int) func() {
		f := schema9WorkloadGraph(n)
		c := schema9Parse(t, f.doc, nil)
		u := "https://schemas.example.test/api/graph/node-0.json#node"
		schema9Get(t, c, u)
		return func() {
			for range 500 {
				if s := schema9Get(t, c, u); s.Source() != f.root+"/$defs/N0" {
					t.Fatal("wrong flat lookup")
				}
			}
		}
	})
	wantLinearBytes(t, "nested resource ancestry", 64, func(n int) func() {
		var b strings.Builder
		b.WriteString(`"S":{"$id":"root.json",`)
		for range n {
			b.WriteString(`"properties":{"x":{"$id":"d/child.json",`)
		}
		b.WriteString(`"$ref":"#leaf","$defs":{"Leaf":{"$anchor":"leaf","type":"string"}}`)
		for range n {
			b.WriteString(`}}`)
		}
		b.WriteByte('}')
		doc := schema9Doc("3.1.2", b.String())
		source := schema9Entry + "#/components/schemas/S" + strings.Repeat("/properties/x", n)
		handles := make([]*openapi.Schema, scaleRuns)
		for i := range handles {
			handles[i] = schema9Get(t, schema9Parse(t, doc, nil), schema9Entry+"#/components/schemas/S")
		}
		run := 0
		return func() {
			r := schema9Refs(t, handles[run])
			run++
			if len(r) != 1 || r[0].Err != nil || r[0].Target == nil || r[0].Target.Source() != source+"/$defs/Leaf" {
				t.Fatalf("deep graph=%+v", r)
			}
		}
	})
	wantLinearBytes(t, "legacy shared chain lookups", 128, func(n int) func() {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, `"U%d":{"$ref":"#/components/schemas/C0"},"C%d":{"$ref":"#/components/schemas/C%d"},`, i, i, i+1)
		}
		fmt.Fprintf(&b, `"C%d":{"type":"string"}`, n)
		doc := schema9Doc("3.0.4", b.String())
		clients := make([]*openapi.Client, scaleRuns)
		for i := range clients {
			clients[i] = schema9Parse(t, doc, nil)
		}
		run := 0
		return func() {
			c := clients[run]
			run++
			for i := range n {
				if s := schema9Get(t, c, fmt.Sprintf("%s#/components/schemas/U%d", schema9Entry, i)); s.Source() != fmt.Sprintf("%s#/components/schemas/C%d", schema9Entry, n) {
					t.Fatal("wrong chain terminal")
				}
			}
		}
	})
}

// A bounded synthetic graph fuzzer, not arbitrary unbounded document input.
// Bytes select 1..24 nodes, cyclic target indices, escaped property names,
// optional missing targets and annotations with deceptive identifiers.
// The oracle is the explicit generated edge vector, not another resolver.
// Core 8.2.3/9.4.2 and the Schema/References contracts govern the assertions.
func FuzzSchema9BoundedGraph(f *testing.F) {
	for _, seed := range [][]byte{{0}, {3, 0, 1, 2, 3}, {23, 255, 1, 18, 2, 0}, {7, 4, 0, 255, 255, 3}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		if len(data) > 256 {
			data = data[:256]
		}
		n := 1 + int(data[0]%24)
		at := func(i int) byte { return data[i%len(data)] }
		const root = schema9Entry + "#/components/schemas/Graph"
		var b strings.Builder
		b.WriteString(`"Graph":{"$defs":{`)
		var want []schema9Edge
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			next := int(at(i+1)) % n
			name := fmt.Sprintf("p/~ %d", i)
			value := fmt.Sprintf("#/components/schemas/Graph/$defs/N%d", next)
			source := fmt.Sprintf("%s/$defs/N%d", root, next)
			if at(i+2) == 255 {
				value = "#/components/schemas/Absent"
				source = ""
			}
			fmt.Fprintf(&b, `"N%d":{"properties":{%q:{"$ref":%q}},"default":{"$id":"decoy.json","$ref":"not-followed.json"}}`, i, name, value)
			want = append(want, schema9Edge{fmt.Sprintf("/$defs/N%d/properties/p~1~0 %d/$ref", i, i), "$ref", value, schema9Entry + value, source})
		}
		b.WriteString(`}}`)
		m := newMemFetch(nil)
		c := schema9Parse(t, schema9Doc("3.1.2", b.String()), m)
		s := schema9Get(t, c, root)
		r := schema9Refs(t, s)
		schema9WantEdges(t, r, want)
		for i, w := range want {
			if w.source == "" {
				continue
			}
			target := schema9Get(t, c, r[i].URI)
			if target.Source() != w.source || !bytes.Equal(target.Raw(), r[i].Target.Raw()) {
				t.Fatal("lookup/edge inconsistency")
			}
		}
		if len(m.callList()) != 0 {
			t.Fatalf("data/reference traversal fetched %q", m.callList())
		}
		if len(r) > 0 {
			r[0] = openapi.SchemaReference{}
			schema9WantEdges(t, schema9Refs(t, s), want)
		}
	})
}
