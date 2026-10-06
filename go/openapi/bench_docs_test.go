package openapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Document benchmarks: load time and retained memory for a large YAML
// document against the same document as JSON, and for a document split into
// many files against the same document whole. The JSON counterparts of
// the YAML benchmarks are BenchmarkLoadLarge and BenchmarkLoadLargeMemory.
// The split and whole loads both read from memory through Fetch.

// writeJNode writes n as compact JSON, member order and number spellings
// kept.
func writeJNode(b *strings.Builder, n jnode) {
	switch n.kind {
	case 'o', 'a':
		open, end := byte('['), byte(']')
		if n.kind == 'o' {
			open, end = '{', '}'
		}
		b.WriteByte(open)
		for i, c := range n.items {
			if i > 0 {
				b.WriteByte(',')
			}
			if n.kind == 'o' {
				k, _ := json.Marshal(n.names[i])
				b.Write(k)
				b.WriteByte(':')
			}
			writeJNode(b, c)
		}
		b.WriteByte(end)
	case 's':
		s, _ := json.Marshal(n.text)
		b.Write(s)
	case 'z':
		b.WriteString("null")
	default:
		b.WriteString(n.text)
	}
}

func jnodeText(n jnode) string {
	var b strings.Builder
	writeJNode(&b, n)
	return b.String()
}

// prefixRefs returns n with prefix put before every "$ref" value that
// begins with "#/components/", so it reaches the components document.
func prefixRefs(n jnode, prefix string) jnode {
	out := n
	out.items = make([]jnode, len(n.items))
	for i, c := range n.items {
		if n.kind == 'o' && n.names[i] == "$ref" && c.kind == 's' && strings.HasPrefix(c.text, "#/components/") {
			c.text = prefix + c.text
		}
		out.items[i] = prefixRefs(c, prefix)
	}
	return out
}

// splitDoc writes the synthetic document (largeDoc) whole and as many
// documents under base: the entry, holding the root and the paths, each
// Path Item in a document of its own under paths/, and the components in
// components.json, each reference to a component rewritten to reach it
// there. It returns the whole document and the split documents by URI.
func splitDoc(tb testing.TB, base string, paths, schemas int) (string, map[string]string) {
	tb.Helper()
	whole, _ := largeDoc(paths, schemas)
	root, err := readJSON(whole)
	if err != nil {
		tb.Fatal(err)
	}
	split := map[string]string{}
	entry := jnode{kind: 'o'}
	for i, name := range root.names {
		v := root.items[i]
		switch name {
		case "paths":
			for j := range v.items {
				file := fmt.Sprintf("paths/p%d.json", j)
				split[base+file] = jnodeText(prefixRefs(v.items[j], "../components.json"))
				v.items[j] = jnode{kind: 'o', names: []string{"$ref"}, items: []jnode{{kind: 's', text: file}}}
			}
		case "components":
			split[base+"components.json"] = jnodeText(jnode{kind: 'o', names: []string{"components"}, items: []jnode{v}})
			continue
		}
		entry.names, entry.items = append(entry.names, name), append(entry.items, v)
	}
	split[base+"openapi.json"] = jnodeText(entry)
	return string(whole), split
}

// The split document describes what the whole one does: the same
// operations, each callable, with Sources in the documents they moved to.
func TestSplitDocumentGenerator(t *testing.T) {
	const base = "https://api.example.test/"
	whole, split := splitDoc(t, base, 20, 10)
	if len(split) != 22 {
		t.Fatalf("%d documents, want 22", len(split))
	}
	cw := mustLoad(t, &openapi.Loader{Fetch: mapFetch(map[string]string{base + "openapi.json": whole})}, base+"openapi.json", nil)
	cs := mustLoad(t, &openapi.Loader{Fetch: mapFetch(split)}, base+"openapi.json", nil)
	ow, osp := cw.Operations(), cs.Operations()
	if len(osp) != len(ow) || len(osp) != 60 {
		t.Fatalf("%d operations split, %d whole; want 60", len(osp), len(ow))
	}
	for i, op := range osp {
		if op.Err != nil || op.Key != ow[i].Key || len(op.Params) != len(ow[i].Params) || (op.Body == nil) != (ow[i].Body == nil) {
			t.Errorf("%s split: Err %v, %d params; whole: %s, %d params", op.Key, op.Err, len(op.Params), ow[i].Key, len(ow[i].Params))
		}
	}
	if want := base + "paths/p0.json#/get"; osp[0].Source != want {
		t.Errorf("Source = %q, want %q", osp[0].Source, want)
	}
	if got := len(cs.DocumentURIs()); got != 22 {
		t.Errorf("DocumentURIs lists %d documents, want 22", got)
	}
}

// reportRetained reports the heap a load's Client keeps, and the bytes it
// allocates, per byte of its documents.
func reportRetained(b *testing.B, size int, load func() *openapi.Client) {
	b.Helper()
	var before, loaded, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	c := load()
	runtime.ReadMemStats(&loaded)
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(c)
	b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/float64(size), "retained-B/doc-B")
	b.ReportMetric(float64(loaded.TotalAlloc-before.TotalAlloc)/float64(size), "alloc-B/doc-B")
}

// largeYAML is the synthetic document written as block YAML.
func largeYAML(b *testing.B) []byte {
	doc, _ := largeDoc(700, 500)
	return toYAML(b, doc, false, yamlStyle{pick: func(n int) int { return n - 1 }})
}

// BenchmarkLoadLargeYAML loads the synthetic document written as YAML,
// against BenchmarkLoadLarge's JSON.
func BenchmarkLoadLargeYAML(b *testing.B) {
	doc := largeYAML(b)
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	ctx := context.Background()
	for b.Loop() {
		if _, err := openapi.Parse(ctx, doc, largeDocURI, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLoadLargeYAMLMemory reports the YAML document's retained memory,
// against BenchmarkLoadLargeMemory's JSON (both per byte of the JSON
// document, so the two compare).
func BenchmarkLoadLargeYAMLMemory(b *testing.B) {
	doc := largeYAML(b)
	jsonDoc, _ := largeDoc(700, 500)
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := openapi.Parse(context.Background(), doc, largeDocURI, nil); err != nil {
			b.Fatal(err)
		}
	}
	reportRetained(b, len(jsonDoc), func() *openapi.Client {
		c, err := openapi.Parse(context.Background(), doc, largeDocURI, nil)
		if err != nil {
			b.Fatal(err)
		}
		return c
	})
}

// benchLoad loads base+"openapi.json" from docs in memory each iteration
// and reports retained memory per byte of all the documents.
func benchLoad(b *testing.B, base string, docs map[string]string) {
	size := 0
	for _, d := range docs {
		size += len(d)
	}
	l := &openapi.Loader{Fetch: mapFetch(docs)}
	load := func() *openapi.Client {
		c, err := l.Load(context.Background(), base+"openapi.json", nil)
		if err != nil {
			b.Fatal(err)
		}
		return c
	}
	ops := load().Operations()
	if len(ops) != 2100 {
		b.Fatalf("%d operations", len(ops))
	}
	for _, op := range ops {
		if op.Err != nil {
			b.Fatalf("%s: %v", op.Key, op.Err)
		}
	}
	b.SetBytes(int64(size))
	b.ReportAllocs()
	for b.Loop() {
		load()
	}
	reportRetained(b, size, load)
}

// BenchmarkLoadWhole loads the synthetic document in one document.
func BenchmarkLoadWhole(b *testing.B) {
	const base = "https://api.example.test/"
	whole, _ := splitDoc(b, base, 700, 500)
	benchLoad(b, base, map[string]string{base + "openapi.json": whole})
}

// BenchmarkLoadSplit loads the synthetic document split into 702
// documents.
func BenchmarkLoadSplit(b *testing.B) {
	const base = "https://api.example.test/"
	_, split := splitDoc(b, base, 700, 500)
	benchLoad(b, base, split)
}
