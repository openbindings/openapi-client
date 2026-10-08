package schema2020_test

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// Defs keys. Project: "A schema written at #/components/schemas/NAME of
// the entry document, or #/definitions/NAME in Swagger 2.0, has the key
// NAME when NAME contains no "#"; any other key is the schema's Source
// written as a URI reference relative to the entry document's URI, so that
// the key resolves against that URI to the Source, its dot segments removed.
// In a reference, a key is escaped as a JSON Pointer token and then as RFC 3986
// section 3.5 requires of a fragment."

var hexEscape = regexp.MustCompile(`%[0-9a-fA-F]{2}`)

// upperHex spells percent-encodings in uppercase, which RFC 3986 section
// 2.1 makes equivalent to lowercase.
func upperHex(s string) string {
	return hexEscape.ReplaceAllStringFunc(s, strings.ToUpper)
}

// authored is how a document refers to the component NAME: a JSON
// Pointer token, percent-encoded where a fragment requires (RFC 6901
// section 6).
func authored(name string) string { return strings.TrimPrefix(refFor(name), "#/$defs/") }

// Component names whose references need only unreserved characters and
// required percent-encodings (RFC 3986 sections 2.1, 2.3 and 3.5), so the
// reference has one spelling, up to the case of hexadecimal digits.
var exactNames = []struct{ name, ref string }{
	{"Pet", "#/$defs/Pet"},
	{"a.b-c_0", "#/$defs/a.b-c_0"},
	{"a b", "#/$defs/a%20b"},
	{"a/b", "#/$defs/a~1b"},
	{"a~b", "#/$defs/a~0b"},
	{"100%", "#/$defs/100%25"},
	{"é", "#/$defs/%C3%A9"},
	{`q"x`, "#/$defs/q%22x"},
	{"a{b}", "#/$defs/a%7Bb%7D"},
	{"a|b", "#/$defs/a%7Cb"},
	{"x[0]", "#/$defs/x%5B0%5D"},
	{"a^b", "#/$defs/a%5Eb"},
}

// componentsReferencing builds schema components: R, whose property pN
// refers to the Nth name, and a component of each name with title N.
func componentsReferencing(names []string) string {
	var props, comps []string
	for i, n := range names {
		props = append(props, fmt.Sprintf(`"p%d":{"$ref":"#C/%s"}`, i, authored(n)))
		comps = append(comps, fmt.Sprintf(`%s:{"type":"string","title":"%d"}`, jstr(n), i))
	}
	return `"R":{"type":"object","properties":{` + strings.Join(props, ",") + `}},` + strings.Join(comps, ",")
}

// A component of the entry document is keyed by its name, and a reference
// to it escapes the name as a JSON Pointer token, then percent-encodes
// what a fragment cannot hold.
func TestProjectKeyComponentNames(t *testing.T) {
	var names []string
	for _, n := range exactNames {
		names = append(names, n.name)
	}
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", componentsReferencing(names)), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				root := mustDecode(t, "Root", p.Root).(map[string]any)
				props, _ := root["properties"].(map[string]any)
				for i, n := range exactNames {
					src := comp(t, c, v, authored(n.name)).Source()
					if p.Sources[n.name] != src {
						t.Errorf("Sources[%q] = %q, want %q", n.name, p.Sources[n.name], src)
					}
					def, ok := p.Defs[n.name]
					if !ok {
						t.Errorf("Defs has no key %q", n.name)
					} else {
						sameValue(t, "Defs["+n.name+"]", mustDecode(t, "Defs", def), mustDecode(t, "want", []byte(fmt.Sprintf(`{"type":"string","title":"%d"}`, i))))
					}
					prop, _ := props[fmt.Sprintf("p%d", i)].(map[string]any)
					ref, _ := prop["$ref"].(string)
					if upperHex(ref) != n.ref {
						t.Errorf("reference to %q = %q, want %q", n.name, ref, n.ref)
					}
				}
				if len(p.Defs) != len(exactNames) {
					t.Errorf("Defs has %d entries, want %d", len(p.Defs), len(exactNames))
				}
			})
		}
	}
}

// Characters a fragment may hold as themselves (sub-delims, ":", "@", "?")
// may be written either way; the reference still names the key.
func TestProjectKeyFragmentCharacters(t *testing.T) {
	names := []string{"a:b", "a@b", "a?b", "a!b", "a$b", "a&b", "a'b", "a(b)", "a*b", "a+b", "a,b", "a;b", "a=b"}
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", componentsReferencing(names)), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				w := want{defs: map[string]string{}}
				var props []string
				for i, n := range names {
					props = append(props, fmt.Sprintf(`"p%d":{"$ref":"=>#C/%s"}`, i, authored(n)))
					w.defs[ed(v, "#C/"+authored(n))] = fmt.Sprintf(`{"type":"string","title":"%d"}`, i)
					if _, ok := p.Defs[n]; !ok {
						t.Errorf("Defs has no key %q", n)
					}
				}
				w.root = ed(v, `{"type":"object","properties":{`+strings.Join(props, ",")+`}}`)
				w.check(t, c, p)
			})
		}
	}
}

// A component name containing "#" is not a key; the key is the Source
// relative to the entry document, a fragment-only reference, so it cannot
// collide with any name.
func TestProjectKeyNameWithHash(t *testing.T) {
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := load(t, f, docText(v, "", `"R":{"properties":{"h":{"$ref":"#C/a%23b"},"n":{"$ref":"#C/a"}}},"a#b":{"type":"string"},"a":{"type":"integer"}`), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				src := comp(t, c, v, "a%23b").Source()
				_, frag, _ := strings.Cut(src, "#")
				key := "#" + frag
				if p.Sources[key] != src {
					t.Errorf("Sources[%q] = %q, want %q; Sources = %v", key, p.Sources[key], src, p.Sources)
				}
				wantRef := "#/$defs/%23~1components~1schemas~1a%2523b"
				if v == v20 {
					wantRef = "#/$defs/%23~1definitions~1a%2523b"
				}
				if refFor(key) != wantRef {
					t.Fatalf("Source %s: fragment is not spelled as RFC 6901 section 6 says", src)
				}
				root := mustDecode(t, "Root", p.Root).(map[string]any)
				h, _ := root["properties"].(map[string]any)["h"].(map[string]any)
				if got, _ := h["$ref"].(string); upperHex(got) != wantRef {
					t.Errorf("reference = %q, want %q", got, wantRef)
				}
				if _, ok := p.Defs["a"]; !ok {
					t.Errorf("Defs has no key \"a\"")
				}
			})
		}
	}
}

// A schema of the entry document that is not a component itself, such as
// a property of one or a schema written inline under paths, is keyed by
// its Source relative to the entry document: "#" and the Source's
// fragment.
func TestProjectKeyEntrySchemaOutsideComponents(t *testing.T) {
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				var paths, inline string
				if v == v20 {
					paths = `"/pets/{id}":{"get":{"produces":["application/json"],"parameters":[{"name":"id","in":"path","required":true,"type":"string"}],
						"responses":{"200":{"description":"ok","schema":{"type":"object","title":"inline"}}}}}`
					inline = "#/paths/~1pets~1%7Bid%7D/get/responses/200/schema"
				} else {
					paths = `"/pets/{id}":{"get":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
						"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object","title":"inline"}}}}}}}`
					inline = "#/paths/~1pets~1%7Bid%7D/get/responses/200/content/application~1json/schema"
				}
				c := load(t, f, docText(v, paths, `"R":{"properties":{"a":{"$ref":"#C/Other/properties/b"},"c":{"$ref":"`+inline+`"}}},"Other":{"properties":{"b":{"type":"boolean"}}}`), nil)
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				nested := ed(v, "#C/Other/properties/b")
				for _, uri := range []string{nested, inline} {
					src := schemaAt(t, c, uri).Source()
					_, frag, _ := strings.Cut(src, "#")
					if p.Sources["#"+frag] != src {
						t.Errorf("Sources[%q] = %q, want %q; Sources = %v", "#"+frag, p.Sources["#"+frag], src, p.Sources)
					}
				}
				w := want{
					root: `{"properties":{"a":{"$ref":"=>` + nested + `"},"c":{"$ref":"=>` + inline + `"}}}`,
					defs: map[string]string{nested: `{"type":"boolean"}`, inline: `{"type":"object","title":"inline"}`},
				}
				w.check(t, c, p)
				root := mustDecode(t, "Root", p.Root).(map[string]any)
				a, _ := root["properties"].(map[string]any)["a"].(map[string]any)
				wantRef := "#/$defs/%23~1components~1schemas~1Other~1properties~1b"
				if v == v20 {
					wantRef = "#/$defs/%23~1definitions~1Other~1properties~1b"
				}
				if got, _ := a["$ref"].(string); got != wantRef {
					t.Errorf("reference = %q, want %q", got, wantRef)
				}
			})
		}
	}
}

// A schema in another document is keyed by its Source relative to the
// entry document, even at that document's components/schemas/NAME or
// definitions/NAME, and a whole document keeps its empty fragment. The
// checker verifies that each key resolves against the entry document's
// URI to its Source.
func TestProjectKeyOtherDocuments(t *testing.T) {
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				otherComp := "other.json#/components/schemas/Pet"
				otherDoc := `{"openapi":"3.0.4","info":{"title":"o","version":"1"},"paths":{},"components":{"schemas":{"Pet":{"type":"string","title":"other"}}}}`
				if v == v20 {
					otherComp = "other.json#/definitions/Pet"
					otherDoc = `{"swagger":"2.0","info":{"title":"o","version":"1"},"paths":{},"definitions":{"Pet":{"type":"string","title":"other"}}}`
				}
				others := map[string]string{
					modelsURI:                                `{"Pet":{"type":"object","title":"models"}}`,
					commonURI:                                `{"Err":{"type":"object","title":"common"}}`,
					"https://api.example.test/v1/whole.json": `{"type":"integer","title":"whole"}`,
					"https://api.example.test/v1/other.json": otherDoc,
					cdnURI:                                   `{"T":{"type":"boolean","title":"cdn"}}`,
				}
				schemas := `"R":{"properties":{
					"a":{"$ref":"models.json#/Pet"},
					"b":{"$ref":"../shared/common.json#/Err"},
					"c":{"$ref":"whole.json"},
					"d":{"$ref":"` + cdnURI + `#/T"},
					"e":{"$ref":"` + otherComp + `"},
					"f":{"$ref":"#C/Pet"}}},
					"Pet":{"type":"string","title":"entry"}`
				c := loadWith(t, f, docText(v, "", schemas), others, []string{cdnOrigin})
				p := project(t, c, comp(t, c, v, "R"), schema2020.Neutral)
				w := want{
					root: ed(v, `{"properties":{"a":{"$ref":"=>models.json#/Pet"},"b":{"$ref":"=>../shared/common.json#/Err"},"c":{"$ref":"=>whole.json"},
						"d":{"$ref":"=>`+cdnURI+`#/T"},"e":{"$ref":"=>`+otherComp+`"},"f":{"$ref":"=>#C/Pet"}}}`),
					defs: map[string]string{
						"models.json#/Pet":           `{"type":"object","title":"models"}`,
						"../shared/common.json#/Err": `{"type":"object","title":"common"}`,
						"whole.json":                 `{"type":"integer","title":"whole"}`,
						cdnURI + "#/T":               `{"type":"boolean","title":"cdn"}`,
						otherComp:                    `{"type":"string","title":"other"}`,
						ed(v, "#C/Pet"):              `{"type":"string","title":"entry"}`,
					},
				}
				w.check(t, c, p)
				if _, ok := p.Defs["Pet"]; !ok {
					t.Errorf(`the entry's component Pet is not keyed "Pet"; Sources = %v`, p.Sources)
				}
				if k := keyFor(t, c, p, otherComp); k == "Pet" || !strings.Contains(k, "#") {
					t.Errorf("key %q for %s, a component of another document", k, otherComp)
				}
			})
		}
	}
}

// Parse: with an empty uri, "Sources name the document by a "urn:uuid:"
// URI derived from the content ... so Sources and $defs keys are the same
// on every run."
func TestProjectKeysParseWithoutURI(t *testing.T) {
	for _, v := range editions {
		t.Run(v, func(t *testing.T) {
			doc := docText(v, "", `"R":{"properties":{"a":{"$ref":"#C/A"},"b":{"$ref":"#C/A/properties/x"}}},"A":{"properties":{"x":{"type":"string"}}}`)
			var first *schema2020.Projection
			for run := 0; run < 2; run++ {
				c, err := openapi.Parse(context.Background(), []byte(doc), "", nil)
				if err != nil {
					t.Fatal(err)
				}
				uri := c.DocumentURIs()[0] + compPath(v) + "R"
				s, err := c.Schema(uri)
				if err != nil {
					t.Fatal(err)
				}
				p := project(t, c, s, schema2020.Neutral)
				if _, ok := p.Defs["A"]; !ok {
					t.Errorf("Defs has no key \"A\": %v", p.Sources)
				}
				if _, ok := p.Defs[compPath(v)+"A/properties/x"]; !ok {
					t.Errorf("Defs has no key %q: %v", compPath(v)+"A/properties/x", p.Sources)
				}
				if first == nil {
					first = p
					continue
				}
				if string(first.Root) != string(p.Root) || fmt.Sprint(first.Sources) != fmt.Sprint(p.Sources) {
					t.Errorf("runs differ:\n%s %v\n%s %v", first.Root, first.Sources, p.Root, p.Sources)
				}
				for k, d := range first.Defs {
					if string(p.Defs[k]) != string(d) {
						t.Errorf("Defs[%q] differs between runs: %s, %s", k, d, p.Defs[k])
					}
				}
			}
		})
	}
}

// Projection: "Keys are stable within one openapi.Client and direction,
// so projections of the same direction may merge their Defs without
// collision." Several roots sharing components are projected in one
// direction and their Defs merged: a shared key has one value and one
// Source.
func TestProjectDefsMerge(t *testing.T) {
	schemas := `
		"Order":{"type":"object","properties":{"item":{"$ref":"#C/Item"},"owner":{"$ref":"#C/User"}}},
		"Cart":{"type":"object","properties":{"items":{"type":"array","items":{"$ref":"#C/Item"}},"owner":{"$ref":"#C/User/properties/name"}}},
		"Item":{"type":"object","required":["id","sku"],"properties":{"id":{"type":"string","readOnly":true},"sku":{"type":"string"},"tag":{"$ref":"#C/Tag"}}},
		"User":{"type":"object","required":["name","password"],"properties":{"name":{"type":"string"},"password":{"type":"string","writeOnly":true},"tag":{"$ref":"#C/Tag"}}},
		"Tag":{"type":"string","minimum":1,"exclusiveMinimum":true}`
	for _, v := range editions {
		for _, d := range directions {
			t.Run(v+"/"+dirName(d), func(t *testing.T) {
				c := load(t, "json", docText(v, "", schemas), nil)
				merged := map[string]string{}
				sources := map[string]string{}
				for _, name := range []string{"Order", "Cart", "Item", "User", "Tag"} {
					p := project(t, c, comp(t, c, v, name), d)
					for k, def := range p.Defs {
						if prev, ok := merged[k]; ok {
							if canon(mustDecode(t, k, []byte(prev))) != canon(mustDecode(t, k, def)) {
								t.Errorf("Defs[%q] from %s = %s, another projection gave %s", k, name, def, prev)
							}
							if sources[k] != p.Sources[k] {
								t.Errorf("Sources[%q] from %s = %s, another projection gave %s", k, name, p.Sources[k], sources[k])
							}
							continue
						}
						merged[k], sources[k] = string(def), p.Sources[k]
					}
				}
				for _, k := range []string{"Item", "User", "Tag"} {
					if _, ok := merged[k]; !ok {
						t.Errorf("no projection has Defs[%q]; keys %v", k, sources)
					}
				}
			})
		}
	}
}

// loadAt parses entry as the document at uri, serving others by URI and
// admitting every reference.
func loadAt(t testing.TB, format, uri, entry string, others map[string]string) *openapi.Client {
	t.Helper()
	served := map[string]string{}
	for u, d := range others {
		served[u] = encode(t, format, d)
	}
	l := &openapi.Loader{Fetch: serve(served), AllowReference: func(string, string) bool { return true }}
	c, err := l.Parse(context.Background(), []byte(encode(t, format, entry)), uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Keys that take more than a file name (RFC 3986 section 4.2): a relative
// path whose first segment holds ":" needs "./" so that it is not read as
// a scheme; a document under another scheme can only be named by its
// absolute URI; and a query is part of the document's URI, so a document
// differing from the entry only by its query has its own key. The checker
// verifies that each key resolves against the entry URI to its Source.
func TestProjectKeyReferenceForms(t *testing.T) {
	const entry = "https://api.example.test/v1/openapi.json?v=1"
	others := map[string]string{
		"https://api.example.test/v1/a:b.json":         `{"X":{"type":"string"}}`,
		"http://plain.example.test/x.json":             `{"X":{"type":"integer"}}`,
		"https://api.example.test/v1/models.json?v=2":  `{"X":{"type":"boolean"}}`,
		"https://api.example.test/v1/openapi.json?v=2": `{"X":{"type":"null"}}`,
	}
	for _, v := range editions {
		for _, f := range formats {
			t.Run(v+"/"+f, func(t *testing.T) {
				c := loadAt(t, f, entry, docText(v, "", `"R":{"properties":{
					"a":{"$ref":"./a:b.json#/X"},
					"b":{"$ref":"http://plain.example.test/x.json#/X"},
					"c":{"$ref":"models.json?v=2#/X"},
					"d":{"$ref":"?v=2#/X"},
					"e":{"$ref":"#C/R"}}}`), others)
				r := schemaAt(t, c, entry+compPath(v)+"R")
				p := project(t, c, r, schema2020.Neutral)
				keys := map[string]string{}
				for k, src := range p.Sources {
					keys[src] = k
				}
				for _, check := range []struct{ uri, key string }{
					{"https://api.example.test/v1/a:b.json#/X", ""},
					{"http://plain.example.test/x.json#/X", "http://plain.example.test/x.json#/X"},
					{"https://api.example.test/v1/models.json?v=2#/X", ""},
					{"https://api.example.test/v1/openapi.json?v=2#/X", ""},
					{entry + compPath(v) + "R", "R"},
				} {
					k, ok := keys[schemaAt(t, c, check.uri).Source()]
					switch {
					case !ok:
						t.Errorf("no key for %s: %v", check.uri, p.Sources)
					case check.key != "" && k != check.key:
						t.Errorf("key %q for %s, want %q", k, check.uri, check.key)
					}
					if u, err := url.Parse(k); err == nil && u.Scheme != "" && !strings.HasPrefix(check.uri, "http:") {
						t.Errorf("key %q for %s has a scheme", k, check.uri)
					}
				}
			})
		}
	}
}

// Member names written with escapes (RFC 8259 section 7), in a keyword, a
// property name, a required name and a component name, mean the names they
// spell: "readOnly" is readOnly, "$ref" is $ref, and the
// component "BA" is keyed "BA".
func TestProjectEscapedMemberNames(t *testing.T) {
	schemas := `"S":{"type":"object","required":["A","a\"b","a\\b"],"properties":{
		"A":{"type":"string","readOnly":true},
		"a\"b":{"$ref":"#C/BA"},
		"a\\b":{"type":"string","nullable":true}}},
		"BA":{"type":"string"}`
	for _, v := range []string{v30, v31} {
		t.Run(v, func(t *testing.T) {
			c := load(t, "json", docText(v, "", schemas), nil)
			p := project(t, c, comp(t, c, v, "S"), schema2020.Request)
			root := `{"type":"object","required":["a\"b","a\\b"],"properties":{"A":{"type":"string","readOnly":true},"a\"b":{"$ref":"=>#C/BA"},"a\\b":{"type":["string","null"]}}}`
			if isModern(v) {
				root = `{"type":"object","required":["A","a\"b","a\\b"],"properties":{"A":{"type":"string","readOnly":true},"a\"b":{"$ref":"=>#C/BA"},"a\\b":{"type":"string","nullable":true}}}`
			}
			edAll(v, want{root: root, defs: map[string]string{"#C/BA": `{"type":"string"}`}}).check(t, c, p)
			if _, ok := p.Defs["BA"]; !ok {
				t.Errorf("Defs has no key \"BA\": %v", p.Sources)
			}
		})
	}
}
