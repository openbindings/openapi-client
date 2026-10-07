package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// load.go, Loader: "A referenced document with neither an openapi nor a
// swagger field uses the entry document's edition", independently of which
// explicitly versioned referrer discovers it first. The shared Reference
// Object's description therefore follows 3.2 rules.
func TestEditionsVersionlessEntryEdition(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			paths := []string{`"/legacy":{"$ref":"legacy.json#/item"}`, `"/modern":{"$ref":"modern.json#/item"}`}
			if reverse {
				slices.Reverse(paths)
			}
			docs := map[string]string{
				"https://api.example.test/legacy.json": `{"openapi":"3.0.4","paths":{},"item":{"get":{"parameters":[{"$ref":"shared.json#/P"}]}}}`,
				"https://api.example.test/modern.json": `{"openapi":"3.2.1","paths":{},"item":{"get":{"parameters":[{"$ref":"shared.json#/P"}]}}}`,
				"https://api.example.test/shared.json": `{"P":{"$ref":"#/Target","description":"entry edition"},"Target":{"name":"q","in":"query","description":"target","schema":{"type":"string"}}}`,
			}
			l := openapi.Loader{Fetch: mapFetch(docs)}
			c, err := l.Parse(t.Context(), []byte(editionDoc("3.2.1", strings.Join(paths, ","))), testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/legacy", "/modern"} {
				p := param(t, mustOp(t, c, "GET "+path), 0)
				if p.Description != "entry edition" || p.Source != "https://api.example.test/shared.json#/Target" {
					t.Errorf("%s parameter %+v", path, p)
				}
				mustPrepare(t, c, "GET "+path, &openapi.Input{Params: map[string]any{"q": "value"}})
			}
		})
	}
}

// Swagger 2.0 6.4.17/6.4.18 and OAS 3.0.4 4.7.23 ignore Reference Object
// siblings. Their schema references must neither fetch sibling schemas nor
// interpret a sibling $id as a new resolution base.
func TestEditionsLegacySchemaReferenceSiblingDiscovery(t *testing.T) {
	for _, version := range []string{"2.0", "3.0.4"} {
		t.Run(version, func(t *testing.T) {
			var mu sync.Mutex
			hits := map[string]int{}
			l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
				mu.Lock()
				hits[u]++
				mu.Unlock()
				if u == "https://api.example.test/target.json" {
					return io.NopCloser(strings.NewReader(`{"S":{"type":"string"}}`)), "", nil
				}
				return nil, "", fmt.Errorf("unexpected sibling fetch %s", u)
			}}
			schema := `{"$ref":"target.json#/S","$id":"wrong-base/","allOf":[{"$ref":"never-allof.json"}],"items":{"$ref":"never-items.json"},"properties":{"p":{"$ref":"never-properties.json"}},"additionalProperties":{"$ref":"never-additional.json"}}`
			c, err := l.Parse(t.Context(), []byte(editionPost(version, "application/json", schema)), testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			s := reqMedia(t, mustOp(t, c, "POST /x"), 0).Schema
			if s.Source() != "https://api.example.test/target.json#/S" || string(s.Raw()) != `{"type":"string"}` {
				t.Errorf("schema %s Source %s", s.Raw(), s.Source())
			}
			if len(hits) != 1 || hits["https://api.example.test/target.json"] != 1 {
				t.Errorf("fetches %v", hits)
			}
		})
	}
}

// Swagger Schema Object 6.4.18 supports items/allOf/properties/
// additionalProperties, but not the later oneOf/anyOf/not vocabulary. The
// distinct Items Object 6.4.10 only nests Items and is not a Reference
// Object, so a $ref in one marks a document meant to be bundled (describe.go,
// Operation: "It makes the nearest part holding that object unusable: the
// Err of that ... Param ... says the document must be bundled first, and
// does not wrap ErrUnresolved"). Such a $ref is never retrieved (load.go,
// Loader: the references followed are those in Reference Objects, Path
// Items and Schema Objects), and neither is one inside an Items member the
// Items Object does not define.
//
// The parameter stays optional and known: describe.go, Operation.Err: "A
// defect in an optional part is reported on that part instead, and fails a
// call only when the call uses it". A value for it is one built-in
// serialization cannot use (describe.go, Param.Err), refused at its key
// (errors.go, RequestError.Inputs: "a value its style cannot serialize ...
// The key is the Param.Key"; doc.go, Styles: "Each is refused at the
// parameter's key, with Param.Err set where the document alone decides
// it"), and a writer may still supply it (Param.Err: "A parameter with Err
// set can be supplied by Input.ParamWriters when its Key is known").
// Nested Items without a $ref serialize by their own collectionFormat
// first (doc.go, Swagger 2.0 arrays: "a nested items array by its own
// first").
func TestEditionsSwaggerSchemaAndItemsSubset(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
		mu.Lock()
		hits[u]++
		mu.Unlock()
		if strings.HasPrefix(u, "https://api.example.test/real-") {
			return io.NopCloser(strings.NewReader(`{"type":"string"}`)), "", nil
		}
		return nil, "", fmt.Errorf("unexpected unsupported-keyword fetch %s", u)
	}}
	schema := `{"type":"object","allOf":[{"$ref":"real-allof.json"}],"items":{"$ref":"real-items.json"},"properties":{"p":{"$ref":"real-property.json"}},"additionalProperties":{"$ref":"real-additional.json"},"oneOf":[{"$ref":"never-oneof.json"}],"anyOf":[{"$ref":"never-anyof.json"}],"not":{"$ref":"never-not.json"},"$defs":{"X":{"$ref":"never-defs.json"}},"$dynamicRef":"never-dynamic.json","discriminator":{"mapping":{"x":"never-mapping.json"}}}`
	doc := editionPost("2.0", "application/json", schema)
	params := []string{
		// $ref in the outer and the inner Items Object.
		`{"name":"q","in":"query","type":"array","items":{"type":"array","collectionFormat":"pipes","$ref":"never-outer-items.json","items":{"type":"string","$ref":"never-inner-items.json","allOf":[{"$ref":"never-item-allof.json"}]}}}`,
		// $ref in the innermost Items Object only.
		`{"name":"deep","in":"query","type":"array","items":{"type":"array","items":{"type":"string","$ref":"never-deep-items.json"}}}`,
		// Nested Items Objects with no $ref.
		`{"name":"n","in":"query","type":"array","items":{"type":"array","collectionFormat":"pipes","items":{"type":"string"}}}`,
	}
	doc = strings.Replace(doc, `"parameters":[`, `"parameters":[`+strings.Join(params, ",")+`,`, 1)
	c, err := l.Parse(t.Context(), []byte(doc), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 4 {
		t.Errorf("fetches %v", hits)
	}
	for _, name := range []string{"allof", "items", "property", "additional"} {
		if hits["https://api.example.test/real-"+name+".json"] != 1 {
			t.Errorf("missing supported reference %s", name)
		}
	}

	op := mustOp(t, c, "POST /x")
	if op.Err != nil {
		t.Errorf("Operation.Err = %v; want the defect on the parameter alone", op.Err)
	}
	for _, name := range []string{"q", "deep"} {
		p := paramByName(op.Params, name)
		if p == nil {
			t.Fatalf("no parameter %q", name)
		}
		if !mentionsBundling(p.Err) || errors.Is(p.Err, openapi.ErrUnresolved) {
			t.Errorf("%s: Param.Err = %v; want it to say the document must be bundled first, without ErrUnresolved", name, p.Err)
		}
	}
	if n := paramByName(op.Params, "n"); n == nil || n.Err != nil || n.CollectionFormat != "csv" {
		t.Errorf("parameter n %+v; want usable, csv by default", n)
	}

	body := map[string]int{"n": 1}
	for _, name := range []string{"q", "deep"} {
		_, err = c.Prepare(op.Key, &openapi.Input{Params: map[string]any{name: [][]string{{"a", "b"}, {"c", "d"}}}, Body: body})
		re := asRequestError(t, err)
		wantKeys(t, "Inputs", re.Inputs, true, name)
		if p := paramByName(op.Params, name); !errors.Is(re.Inputs[name], p.Err) {
			t.Errorf("%s: Inputs[%q] = %v; want its Param.Err", name, name, re.Inputs[name])
		}
	}

	req := mustPrepare(t, c, op.Key, &openapi.Input{Params: map[string]any{"n": [][]string{{"a", "b"}, {"c", "d"}}}, Body: body})
	if got := req.HTTP.URL.RawQuery; got != "n=a%7Cb,c%7Cd" {
		t.Errorf("Items serialization %q", got)
	}
	req = mustPrepare(t, c, op.Key, &openapi.Input{
		Params: map[string]any{"n": [][]string{{"a"}}},
		ParamWriters: map[string]func(*http.Request) error{"q": func(r *http.Request) error {
			r.URL.RawQuery += "&q=written"
			return nil
		}},
		Body: body,
	})
	if got := req.HTTP.URL.RawQuery; got != "n=a&q=written" {
		t.Errorf("query with a writer for q %q", got)
	}
}

// YAML and JSON have the same model. A YAML-authored 3.2 security URI is a
// discoverable reference even without a JSON "$ref" token in the document.
func TestEditionsYAMLSecurityURI(t *testing.T) {
	doc := `openapi: 3.2.1
$self: models/root.yaml
paths:
  /x:
    get:
      security:
        - schemes.yaml#/Key: []
`
	l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
		if u != "https://api.example.test/models/schemes.yaml" {
			return nil, "", fmt.Errorf("unexpected %s", u)
		}
		return io.NopCloser(strings.NewReader("Key:\n  type: apiKey\n  in: header\n  name: X-YAML-Key\n")), "", nil
	}}
	rt := &memRT{}
	c, err := l.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}, Credentials: map[string]openapi.Credential{"schemes.yaml#/Key": openapi.Secret("yaml-secret")}})
	if err != nil {
		t.Fatal(err)
	}
	s := mustOp(t, c, "GET /x").Security[0].Schemes[0]
	if s.Err != nil || s.Source != "https://api.example.test/models/schemes.yaml#/Key" {
		t.Errorf("scheme %+v", s)
	}
	mustCall(t, c, "GET /x", nil, nil)
	if got := rt.requests()[0].Header.Get("X-YAML-Key"); got != "yaml-secret" {
		t.Errorf("header %q", got)
	}
}

// Cookie style uses raw cookie names and values (doc.go Percent-encoding),
// so exploded members follow the same semicolon/control refusal as scalars.
// Undefinedness is settled before explode:false's compound-value refusal.
func TestEditionsCookieMembersAndUndefined(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"c","in":"cookie","style":"cookie","schema":{}}]},"post":{"parameters":[{"name":"c","in":"cookie","style":"cookie","explode":false,"schema":{}}]}}`), nil)
	for _, v := range []any{map[string]string{"bad;name": "value"}, map[string]string{"bad\nname": "value"}, map[string]string{"name": "bad;value"}, map[string]string{"name": "bad\x00value"}, []string{"ok", "bad;value"}} {
		_, err := c.Prepare("GET /x", &openapi.Input{Params: map[string]any{"c": v}})
		wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, "c")
	}
	for _, v := range []any{nil, []any{}, map[string]any{"a": nil, "b": []any{}}} {
		req := mustPrepare(t, c, "POST /x", &openapi.Input{Params: map[string]any{"c": v}})
		if got := req.HTTP.Header.Get("Cookie"); got != "" {
			t.Errorf("undefined cookie %q", got)
		}
	}
	// A nonempty list remains defined even when every member is undefined
	// (RFC 6570 section 2.3: a list "is considered undefined if the list
	// contains zero members"), so explode:false's refusal still applies.
	for _, v := range []any{[]any{nil}, []any{nil, []any{}, map[string]any{}}} {
		_, err := c.Prepare("POST /x", &openapi.Input{Params: map[string]any{"c": v}})
		wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, "c")
	}
	req := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"c": map[string]any{"bad;name": nil, "good": "value"}}})
	if got := req.HTTP.Header.Get("Cookie"); got != "good=value" {
		t.Errorf("skipped undefined member %q", got)
	}
}

// Each edition defines its own Security Scheme types (2.0 6.4.24, 3.0.4
// 4.7.27, 3.1.2/3.2.1 Security Scheme). A declaration from another edition
// is a scheme defect, without disabling an operation's unrelated choices.
func TestEditionsSecuritySchemeTypes(t *testing.T) {
	for _, version := range editionVersions {
		for _, kind := range []string{"basic", "http", "mutualTLS"} {
			t.Run(version+"/"+kind, func(t *testing.T) {
				schemes := `"components":{"securitySchemes":{"s":{"type":"` + kind + `","scheme":"basic"}}}`
				if version == "2.0" {
					schemes = `"securityDefinitions":{"s":{"type":"` + kind + `","scheme":"basic"}}`
				}
				c := editionClient(t, editionDoc(version, `"/x":{"get":{"security":[{"s":[]},{}]}}`, schemes), nil)
				s := mustOp(t, c, "GET /x").Security[0].Schemes[0]
				valid := kind == "basic" && version == "2.0" || kind == "http" && version != "2.0" || kind == "mutualTLS" && (version == "3.1.2" || version == "3.2.1")
				if (s.Err == nil) != valid {
					t.Errorf("scheme %+v valid=%v", s, valid)
				}
				mustPrepare(t, c, "GET /x", &openapi.Input{Security: "{}"})
			})
		}
	}
}

// Schema.Dialect uses the explicitly versioned schema document's own
// jsonSchemaDialect, not the entry document's default. This asserts only the
// basic handle, not a traversal of the whole schema graph.
func TestEditionsExternalSchemaDocumentDialect(t *testing.T) {
	const dialect = "https://json-schema.org/draft/2020-12/schema"
	l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
		if u != "https://api.example.test/external.json" {
			return nil, "", fmt.Errorf("unexpected %s", u)
		}
		return io.NopCloser(strings.NewReader(`{"openapi":"3.1.2","jsonSchemaDialect":"` + dialect + `","components":{"requestBodies":{"B":{"content":{"application/json":{"schema":{"type":"object"}}}}}}}`)), "", nil
	}}
	c, err := l.Parse(t.Context(), []byte(editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"$ref":"external.json#/components/requestBodies/B"}}}`)), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := reqMedia(t, mustOp(t, c, "POST /x"), 0).Schema
	if s.Dialect() != dialect || s.Source() != "https://api.example.test/external.json#/components/requestBodies/B/content/application~1json/schema" {
		t.Errorf("schema Source %s dialect %s", s.Source(), s.Dialect())
	}
}
