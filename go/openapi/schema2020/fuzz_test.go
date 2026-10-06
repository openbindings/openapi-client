package schema2020_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// FuzzProject builds a small valid document of any edition from the fuzz
// input, read as a stream of choices, and projects every schema it holds
// in every direction. Project must not panic, every Projection must keep
// the invariants checkProjection names, and a second call must give the
// same result (Project: "The same input gives the same output"). The
// generator keeps each lost reference the only declaration of its property
// name, and marks only leaf property schemas readOnly or writeOnly, so
// that no loss falls inside a property Swagger 2.0 replaces by false.
func FuzzProject(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 3, 2, 2, 1, 0, 4, 1, 3, 2, 0, 1, 1, 7},
		{1, 3, 2, 4, 3, 1, 0, 2, 2, 5, 1, 1, 0, 6, 3},
		{2, 3, 5, 5, 6, 7, 2, 1, 0, 3, 4, 8, 1, 2, 0, 9},
		{3, 3, 8, 4, 2, 6, 1, 5, 0, 7, 2, 3, 1, 1, 4, 4},
		{2, 1, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9},
		{3, 2, 7, 8, 1, 3, 3, 3, 0, 0, 1, 2, 5, 5, 2, 8, 0},
		{0, 2, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4},
		{1, 3, 4, 4, 4, 0, 0, 0, 1, 1, 1, 9, 9, 9, 2, 2, 2},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		g := &docGen{data: data}
		doc, others := g.document()
		c, err := (&openapi.Loader{Fetch: serve(others)}).Parse(context.Background(), []byte(doc), entryURI, nil)
		if err != nil {
			t.Fatalf("generated document rejected: %v\n%s\n%v", err, doc, others)
		}
		for _, ls := range everySchema(t, c) {
			for _, d := range directions {
				p, err := schema2020.Project(c, ls.schema, d)
				checkProjection(t, c, ls.schema, d, p, err)
				again := take(schema2020.Project(c, ls.schema, d))
				if first := take(p, err); !reflect.DeepEqual(first, again) {
					t.Errorf("%s %s: a second call differs:\n%+v\n%+v", ls.label, dirName(d), first, again)
				}
			}
		}
		if t.Failed() {
			t.Logf("document:\n%s\nothers: %v", doc, others)
		}
	})
}

// docGen reads choices from fuzz input; past its end every choice is 0.
type docGen struct {
	data []byte
	i    int

	version string
	names   []string
	ext     bool // whether ext.json is referenced
	meta    bool // whether S0 declares $dynamicAnchor "meta"
	unique  int  // counter for names that must not repeat
}

func (g *docGen) next() int {
	if g.i >= len(g.data) {
		return 0
	}
	b := g.data[g.i]
	g.i++
	return int(b)
}

func (g *docGen) n(k int) int    { return g.next() % k }
func (g *docGen) one(k int) bool { return g.n(k) == 0 }

func (g *docGen) id() int { g.unique++; return g.unique }

func (g *docGen) modern() bool { return isModern(g.version) }

// obj writes a JSON object from alternating keys and JSON values, skipping
// pairs whose value is empty.
func obj(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			parts = append(parts, jstr(kv[i])+":"+kv[i+1])
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

var fuzzNames = []string{"S0", "S1", "a b", "x/y", "é"}

func (g *docGen) document() (string, map[string]string) {
	g.version = editions[g.n(len(editions))]
	g.names = fuzzNames[:1+g.n(len(fuzzNames))]
	g.ext = g.one(3)
	g.meta = g.modern() && g.one(2)
	var comps []string
	for i, name := range g.names {
		s := g.schema(3, false)
		if i == 0 && g.meta {
			if s == "{}" {
				s = `{"$dynamicAnchor":"meta"}`
			} else {
				s = `{"$dynamicAnchor":"meta",` + s[1:]
			}
		}
		if i == 0 && g.modern() && g.one(8) {
			s = obj("$schema", jstr(dialectDraft7), "type", `"string"`)
		}
		comps = append(comps, jstr(name)+":"+s)
	}
	others := map[string]string{}
	if g.ext {
		others[modelsURI] = obj("X", obj("type", `"object"`, "properties", obj("back", obj("$ref", jstr("openapi.json"+compPath(g.version)+"S0")), "n", g.leaf(true))))
	}
	return docText(g.version, g.paths(), strings.Join(comps, ",")), others
}

// ref is a reference to a component, or to the other document.
func (g *docGen) ref() string {
	if g.ext && g.one(4) {
		return obj("$ref", `"models.json#/X"`)
	}
	return obj("$ref", jstr(compPath(g.version)+authored(g.names[g.n(len(g.names))])))
}

// schema writes a schema; prop says it is a property's or an item's.
func (g *docGen) schema(depth int, prop bool) string {
	if depth <= 0 {
		if g.one(2) {
			return g.ref()
		}
		return g.leaf(prop)
	}
	switch g.n(10) {
	case 0, 1:
		return g.leaf(prop)
	case 2:
		r := g.ref()
		if g.one(3) {
			// Members beside $ref: ignored in 2.0 and 3.0, kept in 3.1 and 3.2.
			r = strings.TrimSuffix(r, "}") + `,"description":"beside","readOnly":true}`
		}
		return r
	case 3, 4:
		return g.object(depth)
	case 5:
		return obj("type", `"array"`, "items", g.schema(depth-1, true), "maxItems", fmt.Sprint(1+g.n(9)))
	case 6:
		return obj("allOf", "["+g.ref()+","+g.object(depth-1)+"]")
	case 7:
		if g.modern() {
			n := g.id()
			// A nested resource: $id sets the base, so #/$defs/d is its own
			// entry and #aN its root; the unused entry is dropped.
			d, unused := g.leaf(false), g.leaf(false)
			return obj("$id", jstr(fmt.Sprintf("r%d.json", n)), "$anchor", jstr(fmt.Sprintf("a%d", n)),
				"$defs", obj("d", d, "unused", unused),
				"type", `"object"`, "properties", obj("v", `{"$ref":"#/$defs/d"}`, "w", obj("$ref", jstr(fmt.Sprintf("#a%d", n)))))
		}
		return g.object(depth - 1)
	case 8:
		if g.version != v20 {
			return g.discriminated()
		}
		return g.leaf(prop)
	}
	return g.object(depth - 1)
}

// object writes an object schema whose properties may include a lost
// reference, a $dynamicRef or a resource in another dialect, each under a
// name of its own.
func (g *docGen) object(depth int) string {
	var props, names []string
	for k := 0; k <= g.n(3); k++ {
		name := string(rune('a' + k))
		props = append(props, jstr(name)+":"+g.schema(depth-1, true))
		names = append(names, name)
	}
	var extra []string
	switch g.n(12) {
	case 0:
		props = append(props, jstr(fmt.Sprintf("broken%d", g.id()))+":"+obj("$ref", jstr(compPath(g.version)+"Missing")))
	case 1:
		props = append(props, jstr(fmt.Sprintf("gone%d", g.id()))+`:{"type":"array","items":{"$ref":"absent.json#/X"}}`)
	case 2:
		// Every $dynamicRef is lost, whatever its fragment; in Swagger 2.0
		// and OpenAPI 3.0 it is a keyword the edition does not define.
		target := []string{`"#nowhere"`, `"#meta"`, jstr(compPath(g.version) + authored(g.names[0]))}[g.n(3)]
		props = append(props, jstr(fmt.Sprintf("dyn%d", g.id()))+":"+obj("$dynamicRef", target))
	case 3:
		if g.modern() {
			n := g.id()
			props = append(props, jstr(fmt.Sprintf("custom%d", n))+":"+obj("$id", jstr(fmt.Sprintf("c%d.json", n)), "$schema", jstr(dialectDraft7), "type", `"string"`))
		}
	case 4, 5:
		if !g.modern() {
			// A keyword the edition does not define is removed with an Issue.
			set := removedKeywords(g.version)
			kw := set[g.n(len(set))]
			val := keywordValues[kw]
			if kw == "oneOf" || kw == "anyOf" {
				val = "[" + g.ref() + "]"
			}
			extra = append(extra, kw, val)
		}
	case 6:
		// items as an array: removed with an Issue in Swagger 2.0 and
		// OpenAPI 3.0, kept as authored in OpenAPI 3.1 and 3.2.
		extra = append(extra, "items", "["+g.ref()+","+g.leaf(false)+"]")
	case 7:
		// A $ref that is not a string, which References does not report.
		props = append(props, jstr(fmt.Sprintf("ns%d", g.id()))+`:{"$ref":5,"description":"beside"}`)
	case 8:
		if g.modern() {
			// A resource in another dialect in an unreferenced $defs
			// entry, dropped with it.
			n := g.id()
			props = append(props, jstr(fmt.Sprintf("fd%d", n))+":"+obj("$id", jstr(fmt.Sprintf("fd%d.json", n)),
				"$defs", obj("old", obj("$schema", jstr(dialectDraft7), "type", `"string"`)), "type", `"object"`, "properties", `{"v":`+g.ref()+`}`))
		}
	}
	var required []string
	for _, n := range names {
		if g.one(2) {
			required = append(required, jstr(n))
		}
	}
	req := ""
	if len(required) > 0 {
		req = "[" + strings.Join(required, ",") + "]"
	}
	return obj(append([]string{"type", `"object"`, "required", req, "properties", "{" + strings.Join(props, ",") + "}"}, extra...)...)
}

func (g *docGen) discriminated() string {
	a, b := g.names[g.n(len(g.names))], g.names[g.n(len(g.names))]
	mapping := obj("one", jstr(compPath(g.version)+authored(a)), "two", jstr(compPath(g.version)+authored(b)))
	if g.one(2) {
		mapping = obj("one", `"S0"`, "two", jstr(compPath(g.version)+authored(b)))
	}
	if g.one(6) {
		mapping = strings.TrimSuffix(mapping, "}") + `,"ghost":"Ghost"}`
	}
	def := ""
	if g.version == v32 && g.one(2) {
		def = jstr(compPath(g.version) + authored(b))
	}
	return obj("oneOf", "["+obj("$ref", jstr(compPath(g.version)+authored(a)))+","+obj("$ref", jstr(compPath(g.version)+authored(b)))+"]",
		"discriminator", obj("propertyName", `"kind"`, "mapping", mapping, "defaultMapping", def))
}

// leaf writes a schema with no subschemas, using the keywords each edition
// translates or keeps.
func (g *docGen) leaf(prop bool) string {
	legacy := !g.modern()
	var kv []string
	add := func(k, v string) { kv = append(kv, k, v) }
	switch g.n(5) {
	case 0, 1:
		typ := []string{`"integer"`, `"number"`}[g.n(2)]
		add("type", typ)
		if g.one(2) {
			add("minimum", fmt.Sprint(g.n(5)))
			if legacy && g.one(2) {
				add("exclusiveMinimum", fmt.Sprint(g.one(2)))
			}
		} else if legacy && g.one(3) {
			add("exclusiveMinimum", "true") // no bound
		} else if !legacy && g.one(2) {
			add("exclusiveMinimum", fmt.Sprint(g.n(5)))
		}
		if g.one(2) {
			add("maximum", fmt.Sprint(10+g.n(90)))
			if legacy && g.one(2) {
				add("exclusiveMaximum", fmt.Sprint(g.one(2)))
			}
		}
		if g.one(8) {
			add("format", `"byte"`) // contentEncoding whatever the type
		}
	case 2, 3:
		add("type", `"string"`)
		switch g.n(5) {
		case 0:
			add("format", `"byte"`)
		case 1:
			add("format", `"binary"`)
		case 2:
			add("format", `"date-time"`)
		}
		if g.one(2) {
			add("maxLength", fmt.Sprint(1+g.n(20)))
		}
		if g.one(4) {
			add("enum", `["x","y"]`)
		}
	case 4:
		if g.version == v20 && prop && g.one(2) {
			add("type", `"file"`)
		} else if !legacy && g.one(2) {
			add("type", `["boolean","null"]`)
		} else {
			add("type", `"boolean"`)
		}
	}
	if g.version != v31 && g.version != v32 && g.one(3) {
		// nullable: OpenAPI 3.0 reads it after format: binary; in Swagger
		// 2.0 it is an unknown keyword and stays.
		add("nullable", fmt.Sprint(g.one(2)))
	}
	if prop {
		switch g.n(4) {
		case 0:
			add("readOnly", "true")
		case 1:
			if g.version != v20 {
				add("writeOnly", "true")
			}
		}
	}
	if g.one(4) {
		add("description", `"leaf"`)
	}
	if g.one(6) {
		add("x-note", `{"$id":"data","n":[1,2]}`)
	}
	return obj(kv...)
}

// paths writes one operation that uses the components through every kind
// of descriptor schema of the edition.
func (g *docGen) paths() string {
	first := compPath(g.version) + authored(g.names[0])
	last := compPath(g.version) + authored(g.names[len(g.names)-1])
	if g.version == v20 {
		return `"/items":{"get":{"produces":["application/json"],"parameters":[
				{"name":"n","in":"query","type":"integer","minimum":` + fmt.Sprint(g.n(3)) + `,"exclusiveMinimum":` + fmt.Sprint(g.one(2)) + `},
				{"name":"tags","in":"query","type":"array","items":{"type":"string","format":"byte"}}],
				"responses":{"200":{"description":"ok","schema":{"$ref":` + jstr(first) + `}}}},
			"post":{"consumes":["multipart/form-data"],"parameters":[
				{"name":"f","in":"formData","type":"file","required":true},
				{"name":"s","in":"formData","type":"number","maximum":9,"exclusiveMaximum":true}],
				"responses":{"201":{"description":"ok","schema":{"type":"array","items":{"$ref":` + jstr(last) + `}}}}}}`
	}
	return `"/items":{"get":{"parameters":[{"name":"n","in":"query","schema":` + g.leaf(false) + `}],
			"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":` + jstr(first) + `}}}}}},
		"post":{"requestBody":{"content":{
				"application/json":{"schema":{"type":"array","items":{"$ref":` + jstr(last) + `}}},
				"multipart/form-data":{"schema":{"type":"object","properties":{"file":{"type":"string","format":"binary"},"meta":{"$ref":` + jstr(first) + `}}}}}},
			"responses":{"204":{"description":"none"}}}}`
}
