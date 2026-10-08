package openapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Fuzz targets for YAML documents: one writes random JSON documents as block
// and flow YAML (the test's own emitter) and requires the same Client
// descriptors and Document bytes as the JSON; one checks robustness (no
// panic, bounded time and memory).

// A fuzzGen draws choices from fuzz input, then zeros.
type fuzzGen struct {
	b []byte
	i int
}

func (g *fuzzGen) intn(n int) int {
	if n <= 1 {
		return 0
	}
	var c byte
	if g.i < len(g.b) {
		c = g.b[g.i]
		g.i++
	}
	return int(c) % n
}

// Fragments of strings and keys: plain words, spellings the Core schema
// reads as other types, YAML indicators, escapes and characters a YAML
// writer must escape.
var fuzzFragments = []string{
	"a", "pets", "Pet", "id", "200", "true", "null", "yes", "No", "on", "~", "0x1F", "0o17", "1.0", "007", "1e3", ".inf",
	"é", "日本", "😀", " ", "\t", "\n", "\r", "\"", "\\", "'", "#", ": ", "- ", "{", "}", "[", "]", ",", "&a", "*a",
	"!tag", "%", "@", "`", "|", ">", "?", "\x7f", "\u0085", "\u2028", "\ufeff", "\x00", "\x1b", "/", "{id}", "<<", "---", "...",
}

// Number spellings JSON allows, beyond float64 too.
var fuzzNumbers = []string{"0", "-0", "1", "-17", "1.5", "0.1", "1e3", "1E-2", "-2.5e+10", "123456789012345678901234567890",
	"1e400", "1.000000000000000000001", "18446744073709551615", "0.000001"}

func (g *fuzzGen) str() string {
	var b strings.Builder
	for range g.intn(4) {
		b.WriteString(fuzzFragments[g.intn(len(fuzzFragments))])
	}
	return b.String()
}

func (g *fuzzGen) num() jnode { return jnode{kind: 'p', text: fuzzNumbers[g.intn(len(fuzzNumbers))]} }

func jstr(s string) jnode { return jnode{kind: 's', text: s} }

// obj builds an object from name, value pairs, names made unique.
func obj(pairs ...any) jnode {
	n := jnode{kind: 'o'}
	seen := map[string]bool{}
	for i := 0; i+1 < len(pairs); i += 2 {
		name := pairs[i].(string)
		for seen[name] {
			name += "_"
		}
		seen[name] = true
		n.names = append(n.names, name)
		n.items = append(n.items, pairs[i+1].(jnode))
	}
	return n
}

func arr(items ...jnode) jnode { return jnode{kind: 'a', items: items} }

// value is any JSON value, nesting at most depth levels more.
func (g *fuzzGen) value(depth int) jnode {
	k := g.intn(7)
	if depth == 0 && k >= 5 {
		k = 0
	}
	switch k {
	case 0:
		return jstr(g.str())
	case 1:
		return g.num()
	case 2:
		return jnode{kind: 'p', text: []string{"true", "false"}[g.intn(2)]}
	case 3:
		return jnode{kind: 'z'}
	case 4:
		return jstr(g.str())
	case 5:
		var items []jnode
		for range g.intn(4) {
			items = append(items, g.value(depth-1))
		}
		return arr(items...)
	}
	var pairs []any
	for range g.intn(4) {
		pairs = append(pairs, g.str(), g.value(depth-1))
	}
	return obj(pairs...)
}

// schema is a Schema Object: a type, maybe properties, items, a local
// reference or a number bound.
func (g *fuzzGen) schema(depth int) jnode {
	var pairs []any
	types := []string{"string", "integer", "number", "boolean", "object", "array", "null"}
	switch g.intn(4) {
	case 0:
		pairs = append(pairs, "type", jstr(types[g.intn(len(types))]))
	case 1:
		pairs = append(pairs, "type", arr(jstr(types[g.intn(len(types))]), jstr(types[g.intn(len(types))])))
	case 2:
		pairs = append(pairs, "$ref", jstr("#/components/schemas/S"))
	}
	if depth > 0 && g.intn(2) == 1 {
		var props []any
		for range g.intn(3) {
			props = append(props, g.str(), g.schema(depth-1))
		}
		pairs = append(pairs, "properties", obj(props...))
	}
	if depth > 0 && g.intn(3) == 1 {
		pairs = append(pairs, "items", g.schema(depth-1))
	}
	if g.intn(3) == 1 {
		pairs = append(pairs, "maximum", g.num(), "description", jstr(g.str()))
	}
	if g.intn(4) == 1 {
		pairs = append(pairs, "x-data", g.value(2))
	}
	return obj(pairs...)
}

// document is an OpenAPI 3.1 document: servers, a few Path Items of a few
// operations with parameters, bodies and responses, components, and an
// extension of any value.
func (g *fuzzGen) document() jnode {
	var paths []any
	for i := range 1 + g.intn(3) {
		var ops []any
		path := fmt.Sprintf("/p%d", i)
		templated := g.intn(2) == 1
		if templated {
			path += "/{id}"
		}
		for _, method := range []string{"get", "post", "delete"}[:1+g.intn(3)] {
			var params []jnode
			if templated {
				params = append(params, obj("name", jstr("id"), "in", jstr("path"), "required", jnode{kind: 'p', text: "true"}, "schema", g.schema(1)))
			}
			for range g.intn(3) {
				in := []string{"query", "header", "cookie"}[g.intn(3)]
				p := []any{"name", jstr(g.str() + fmt.Sprint(len(params))), "in", jstr(in), "description", jstr(g.str())}
				if g.intn(2) == 1 {
					p = append(p, "required", jnode{kind: 'p', text: "true"})
				}
				if g.intn(2) == 1 {
					p = append(p, "explode", jnode{kind: 'p', text: []string{"true", "false"}[g.intn(2)]})
				}
				if g.intn(4) == 1 {
					p = append(p, "content", obj("application/json", obj("schema", g.schema(1))))
				} else {
					p = append(p, "schema", g.schema(2))
				}
				params = append(params, obj(p...))
			}
			op := []any{"operationId", jstr(fmt.Sprintf("%s%s%d", g.str(), method, i)), "summary", jstr(g.str()),
				"tags", arr(jstr(g.str()), jstr(g.str())), "parameters", arr(params...)}
			if method == "post" {
				ct := []string{"application/json", "multipart/form-data", "application/x-www-form-urlencoded", "text/plain"}[g.intn(4)]
				op = append(op, "requestBody", obj("required", jnode{kind: 'p', text: "true"}, "content", obj(ct, obj("schema", g.schema(2)))))
			}
			op = append(op, "responses", obj(
				[]string{"200", "201", "2XX", "default"}[g.intn(4)], obj("description", jstr(g.str()), "content", obj("application/json", obj("schema", g.schema(1)))),
				"404", obj("$ref", jstr("#/components/responses/R")),
			))
			if g.intn(3) == 1 {
				op = append(op, "x-extra", g.value(3))
			}
			ops = append(ops, method, obj(op...))
		}
		paths = append(paths, path, obj(ops...))
	}
	return obj(
		"openapi", jstr("3.1.0"),
		"info", obj("title", jstr(g.str()), "version", jstr(g.str())),
		"servers", arr(obj("url", jstr("https://api.example.test/v1"), "description", jstr(g.str()))),
		"paths", obj(paths...),
		"components", obj(
			"schemas", obj("S", g.schema(2)),
			"responses", obj("R", obj("description", jstr(g.str()))),
		),
		"x-data", g.value(4),
	)
}

// FuzzYAMLEquivalence: a random OpenAPI 3.1 JSON document, written as
// block and as flow YAML by the tests' own emitter in scalar styles the
// input chooses, describes the same Client as the JSON (every descriptor)
// and Document is the same JSON, numbers by exact value (load.go, Loader:
// the Core schema's reading, "Numbers keep the exact value written";
// client.go, Document: "a YAML document converted as the client read
// it"). The member order is the document's in both.
func FuzzYAMLEquivalence(f *testing.F) {
	for _, seed := range []string{"", "\x00", "\x01\x02\x03\x04\x05\x06\x07\x08", "\xff\xfe\xfd\xfc\xfb\xfa", "seed with words in it",
		strings.Repeat("\x05\x06", 64), strings.Repeat("\x03", 200)} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		g := &fuzzGen{b: data}
		doc := []byte(jnodeText(g.document()))
		want, err := openapi.Parse(context.Background(), doc, fuzzURI, nil)
		if err != nil {
			t.Fatalf("the generated JSON document is rejected: %v\n%s", err, doc)
		}
		wantDesc := describeAll(want)
		wantDoc, err := canonJSON(want.Document(""))
		if err != nil {
			t.Fatal(err)
		}
		for _, flow := range []bool{false, true} {
			y := toYAML(t, doc, flow, yamlStyle{pick: g.intn})
			got, err := openapi.Parse(context.Background(), y, fuzzURI, nil)
			if err != nil {
				t.Fatalf("flow=%t: the YAML is rejected: %v\n%s", flow, err, y)
			}
			if d := describeAll(got); d != wantDesc {
				t.Fatalf("flow=%t: descriptors differ from the JSON document's:\n%s\nYAML:\n%s", flow, firstDiff(d, wantDesc), y)
			}
			gotDoc, err := canonJSON(got.Document(""))
			if err != nil || gotDoc != wantDoc {
				t.Fatalf("flow=%t: Document differs (%v):\n got %s\nwant %s", flow, err, gotDoc, wantDoc)
			}
		}
	})
}

// fuzzYAMLSeeds are YAML documents, valid and not, around every rule of
// the Loader: anchors and aliases, merge keys, tags, block scalars, flow
// and block nesting, directives and markers, comments, encodings.
var fuzzYAMLSeeds = []string{
	yamlHead,
	yamlHead + "x-a: &a [1, 2]\nx-b: *a\nx-c: {<<: *a, k: v}\n",
	yamlHead + "x-l0: &l0 [a, a, a, a]\nx-l1: &l1 [*l0, *l0, *l0, *l0]\nx-l2: &l2 [*l1, *l1, *l1, *l1]\nx-l3: [*l2, *l2, *l2, *l2]\n",
	yamlHead + "x-a: &a [*a]\n",
	yamlHead + "x-t: !!timestamp 2001-12-14\nx-s: !!str 5\nx-i: !!int '7'\nx-b: !!binary aGk=\n",
	yamlHead + "x-v: |\n  a\n  b\nx-w: >-\n  c\n  d\nx-k: |+\n  e\n\n",
	yamlHead + "x-v: [.inf, -.Inf, .nan, 0x1F, 0o17, 1e400, 007, ~, yes, 'no']\n",
	yamlHead + "x-v:\n  ? [a]\n  : 1\n",
	yamlHead + "x-v: {a: 1, a: 2}\n",
	yamlHead + "---\nx: 1\n",
	"%YAML 1.2\n%TAG !e! tag:example.com,2000:\n---\n" + yamlHead + "x-e: !e!thing x\n...\n",
	"{openapi: 3.1.0, info: {title: t, version: '1'}, paths: {}}",
	"# c\n{\"openapi\": \"3.1.0\", \"info\": {\"title\": \"t\", \"version\": \"1\"}, \"paths\": {}}",
	yamlHead + "x-deep: " + strings.Repeat("[", 120) + strings.Repeat("]", 120) + "\n",
	yamlHead + "x-v: \"\\x41\\u00e9\\U0001F600\\0\\e\\N\\_\\L\\P\"\n",
	yamlHead + "x-v:\t1\n",
	yamlHead + "x-v:\n- a\n -b\n",
	"openapi: 3.1.0\npaths:\n  /p/{id}:\n    get:\n      operationId: g\n      parameters:\n        - {name: id, in: path, required: true}\n      responses:\n        200: {description: ok}\n",
	"\xEF\xBB\xBF" + yamlHead,
	string(utf16Text(yamlHead, true, true)),
	string(utf16Text(yamlHead, false, false)),
	string(utf32Text(yamlHead, false, true)),
	yamlHead + "x-v: \"a\xffb\"\n",
}

// Regression check, not contract: each input takes at most 10 s and allocates
// at most 256 MiB, a budget the bounds allow (load.go, Loader: aliases add at
// most 1,000,000 nodes, 100 times the document's own node count and 100 times
// its own size in bytes, and nesting stops at 1,000 levels, so an input of at
// most 4 KiB grows by at most 400 KB). The rest is contract: any input, YAML
// or not, is read or rejected with an error, never a panic; a document that
// loads describes its operations, and its Document is JSON (load.go, Loader:
// "Document and Raw are JSON").
func FuzzYAMLRobustness(f *testing.F) {
	for _, seed := range fuzzYAMLSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4<<10 {
			return
		}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		c, err := openapi.Parse(context.Background(), data, fuzzURI, nil)
		if err == nil {
			describeAll(c)
			if d := c.Document(""); !json.Valid(d) {
				t.Fatalf("Document is not JSON: %q", d)
			}
		}
		d := time.Since(start)
		runtime.ReadMemStats(&after)
		if d > 10*time.Second {
			t.Fatalf("a %d-byte input took %v", len(data), d)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > 256<<20 {
			t.Fatalf("a %d-byte input allocated %d bytes", len(data), n)
		}
	})
}
