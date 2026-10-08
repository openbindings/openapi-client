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
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// References across documents, over httptest sites. load.go, Load: "Load
// reads the document at uri, and every document its references reach";
// Loader: "The references followed are $ref in Reference Objects, Path Items
// and Schema Objects, $dynamicRef, the values of a Discriminator mapping
// written as an object and a defaultMapping value, where they are not
// component names ..., and OpenAPI 3.2 security
// requirement URIs, anywhere in a document, webhooks and callbacks included,
// ...; operationRef and externalValue are not retrieved. They resolve against
// each document's base". OpenAPI 3.1.2 section 4.3 (multi-document
// descriptions), 4.6 (relative references resolve against the referring
// document's base, inside schemas the nearest $id), 4.8.23 (Reference
// Object), 4.8.24 (Schema Object); RFC 3986 section 5.

// entry31 is doc31 whose root server is the site itself.
func entry31(paths string, extra ...string) string {
	return strings.ReplaceAll(doc31(paths, extra...), "@BASE@", "@SELF@")
}

// A description split over nine documents on one origin, JSON and YAML,
// in two directories: every part each Operation describes is written in
// another document, and every Source names the document the part is
// written in, with a JSON Pointer from that document's root, percent-encoded
// as RFC 6901 section 6 says (describe.go, the Source fields; load.go,
// Client.Document). Client.Document returns any loaded document, and the
// node each Source names. Calls use the entry's root server and a relative
// server URL resolved against the document holding it (doc.go, Fixed rules,
// URL: "against the URI of the document that contains the Server Object").
func TestSplitDescription(t *testing.T) {
	s := newSite(t)
	s.put("/api/openapi.json", `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"@SELF@/v1"}],
		"paths":{
			"/pets":{"$ref":"paths/pets.yaml"},
			"/pets/{id}":{"$ref":"paths/pet.json#/item"}
		},
		"components":{"securitySchemes":{"key":{"$ref":"shared/security.json#/apiKey"}}}}`)
	s.put("/api/paths/pets.yaml", `parameters:
  - $ref: '../shared/params.json#/Limit'
get:
  operationId: listPets
  security:
    - key: []
  parameters:
    - $ref: '../shared/params.json#/components/parameters/Cursor'
  responses:
    '200':
      $ref: '../shared/responses.yaml#/OK'
post:
  operationId: createPet
  servers:
    - url: ../v2
  requestBody:
    $ref: '../shared/bodies.json#/Pet'
  responses:
    '201':
      description: created
`)
	s.put("/api/paths/pet.json", `{"item":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"$ref":"../shared/schemas.json#/Id"}}],
		"get":{"operationId":"getPet","responses":{"200":{"description":"ok"}}}}}`)
	s.put("/api/shared/params.json", `{"Limit":{"name":"limit","in":"query","schema":{"type":"integer"}},
		"components":{"parameters":{"Cursor":{"name":"cursor","in":"query","schema":{"type":"string"}}}}}`)
	s.put("/api/shared/responses.yaml", `OK:
  description: a page of pets
  headers:
    X-Next:
      $ref: 'headers.json#/Next'
  content:
    application/json:
      schema:
        $ref: 'schemas.json#/Page'
`)
	s.put("/api/shared/headers.json", `{"Next":{"description":"the next page","schema":{"type":"string"}}}`)
	s.put("/api/shared/bodies.json", `{"Pet":{"required":true,"content":{"multipart/form-data":{"schema":{"$ref":"schemas.json#/NewPet"}}}}}`)
	s.put("/api/shared/schemas.json", `{"Id":{"type":"string"},"Page":{"type":"array"},
		"NewPet":{"type":"object","properties":{"name":{"type":"string"},"photo":{"type":"string","contentEncoding":"base64"}}}}`)
	s.put("/api/shared/security.json", `{"apiKey":{"type":"apiKey","in":"header","name":"X-Key"}}`)

	at := func(p string) string { return s.uri("/api/" + p) }
	c := mustLoad(t, nil, at("openapi.json"), &openapi.Options{Credentials: map[string]openapi.Credential{"key": openapi.Secret("k1")}})

	// load.go, DocumentURIs: "every document the Client loaded ... by
	// retrieval URI. The entry is first; the rest are sorted by URI".
	want := []string{at("openapi.json"), at("paths/pet.json"), at("paths/pets.yaml"), at("shared/bodies.json"), at("shared/headers.json"),
		at("shared/params.json"), at("shared/responses.yaml"), at("shared/schemas.json"), at("shared/security.json")}
	wantStrings(t, "DocumentURIs", c.DocumentURIs(), want)
	for _, uri := range want {
		path := strings.TrimPrefix(uri, s.URL)
		if n := s.count(path); n != 1 {
			t.Errorf("%s fetched %d times, want once", path, n)
		}
		if c.Document(uri) == nil {
			t.Errorf("Document(%q) = nil", uri)
		}
	}
	// A YAML document is returned as the JSON the client read.
	sameJSON(t, "pets.yaml", c.Document(at("paths/pets.yaml")+"#/get/security"), []byte(`[{"key":[]}]`))

	var sources []string
	source := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s Source = %q, want %q", what, got, want)
		}
		sources = append(sources, got)
	}

	list := mustOp(t, c, "listPets")
	if list.Err != nil || len(list.Params) != 2 || len(list.Responses) != 1 || len(list.Security) != 1 || len(list.Servers) != 1 {
		t.Fatalf("listPets = %+v", list)
	}
	source("listPets", list.Source, at("paths/pets.yaml#/get"))
	source("limit", list.Params[0].Source, at("shared/params.json#/Limit"))
	source("cursor", list.Params[1].Source, at("shared/params.json#/components/parameters/Cursor"))
	source("limit schema", list.Params[0].Schema.Source(), at("shared/params.json#/Limit/schema"))
	ok := list.Responses[0]
	source("200", ok.Source, at("shared/responses.yaml#/OK"))
	if ok.Key != "200" || ok.Description != "a page of pets" || len(ok.Headers) != 1 || len(ok.Media) != 1 {
		t.Fatalf("200 = %+v", ok)
	}
	source("X-Next", ok.Headers[0].Source, at("shared/headers.json#/Next"))
	if ok.Headers[0].Description != "the next page" {
		t.Errorf("X-Next Description = %q", ok.Headers[0].Description)
	}
	source("200 media", ok.Media[0].Source, at("shared/responses.yaml#/OK/content/application~1json"))
	source("200 schema", ok.Media[0].Schema.Source(), at("shared/responses.yaml#/OK/content/application~1json/schema"))
	source("root server", list.Servers[0].Source, at("openapi.json#/servers/0"))
	sch := list.Security[0].Schemes
	if len(sch) != 1 || sch[0].Type != "apiKey" || sch[0].ParamName != "X-Key" || sch[0].Err != nil {
		t.Fatalf("listPets security = %+v", list.Security)
	}
	// describe.go, Operation: a security scheme that is a Reference Object
	// is described by its target, "while Source still names the target".
	source("key", sch[0].Source, at("shared/security.json#/apiKey"))

	create := mustOp(t, c, "createPet")
	if create.Err != nil || create.Body == nil || len(create.Body.Media) != 1 || len(create.Servers) != 1 {
		t.Fatalf("createPet = %+v", create)
	}
	source("createPet", create.Source, at("paths/pets.yaml#/post"))
	source("createPet server", create.Servers[0].Source, at("paths/pets.yaml#/post/servers/0"))
	source("body", create.Body.Source, at("shared/bodies.json#/Pet"))
	source("body media", create.Body.Media[0].Source, at("shared/bodies.json#/Pet/content/multipart~1form-data"))
	wantStrings(t, "createPet fields", encodingOf(t, c, "createPet"), []string{
		"name=text/plain@" + at("shared/schemas.json#/NewPet/properties/name"),
		"photo=application/octet-stream@" + at("shared/schemas.json#/NewPet/properties/photo"),
	})
	if !create.Body.Required {
		t.Errorf("createPet body not required; bodies.json says it is")
	}

	get := mustOp(t, c, "getPet")
	if get.Err != nil || len(get.Params) != 1 {
		t.Fatalf("getPet = %+v", get)
	}
	source("getPet", get.Source, at("paths/pet.json#/item/get"))
	source("id", get.Params[0].Source, at("paths/pet.json#/item/parameters/0"))
	source("id schema", get.Params[0].Schema.Source(), at("paths/pet.json#/item/parameters/0/schema"))
	// describe.go, Schema.Base: "that of Source's document".
	if b := get.Params[0].Schema.Base(); b != at("paths/pet.json") {
		t.Errorf("id schema Base = %q, want %q", b, at("paths/pet.json"))
	}

	// load.go, Client.Document: with a fragment, "a copy of only that
	// node"; each Source names one.
	for _, src := range sources {
		if c.Document(src) == nil {
			t.Errorf("Document(%q) = nil", src)
		}
	}

	mustCall(t, c, "listPets", &openapi.Input{Params: map[string]any{"limit": 2, "cursor": "c"}}, nil)
	if got := s.lastCall(t); got.RequestURI != "/v1/pets?limit=2&cursor=c" || got.Header.Get("X-Key") != "k1" {
		t.Errorf("listPets sent %s with X-Key %q", got.RequestURI, got.Header.Get("X-Key"))
	}
	mustCall(t, c, "createPet", &openapi.Input{Body: map[string]any{"name": "Rex"}}, nil)
	if got := s.lastCall(t); got.RequestURI != "/api/v2/pets" || !strings.HasPrefix(got.Header.Get("Content-Type"), "multipart/form-data") {
		t.Errorf("createPet sent %s as %q; want /api/v2/pets, resolved against pets.yaml", got.RequestURI, got.Header.Get("Content-Type"))
	}
	mustCall(t, c, "getPet", &openapi.Input{Params: map[string]any{"id": "7"}}, nil)
	if got := s.lastCall(t); got.RequestURI != "/v1/pets/7" {
		t.Errorf("getPet sent %s", got.RequestURI)
	}
}

// RFC 3986 section 5.2 (OpenAPI 3.1.2 section 4.6): a relative reference
// resolves against the base of the document that holds it, its retrieval
// URI, not the entry's: dot segments, an absolute path and a network-path
// reference, each from a document in a subdirectory. Each target is
// fetched where the resolution puts it, and nowhere else.
func TestReferencesResolveAgainstTheirDocument(t *testing.T) {
	s := newSite(t)
	host := strings.TrimPrefix(s.URL, "http:")
	s.put("/a/openapi.json", entry31(`"/p":{"$ref":"b/item.json"}`))
	s.put("/a/b/item.json", `{"get":{"operationId":"p","parameters":[
		{"$ref":"../c/params.json#/One"},
		{"$ref":"./near.json#/Two"},
		{"$ref":"/root.json#/Three"},
		{"$ref":"`+host+`/a/b/deep/../far.json#/Four"},
		{"$ref":"d/../../c/params.json#/Five"}]}}`)
	s.put("/a/c/params.json", `{"One":{"name":"one","in":"query"},"Five":{"name":"five","in":"query"}}`)
	s.put("/a/b/near.json", `{"Two":{"name":"two","in":"query"}}`)
	s.put("/root.json", `{"Three":{"name":"three","in":"query"}}`)
	s.put("/a/b/far.json", `{"Four":{"name":"four","in":"query"}}`)
	c := mustLoad(t, nil, s.uri("/a/openapi.json"), nil)
	op := mustOp(t, c, "p")
	if op.Err != nil {
		t.Fatalf("Err = %v", op.Err)
	}
	var names []string
	for _, p := range op.Params {
		names = append(names, p.Name)
	}
	wantStrings(t, "parameters", names, []string{"one", "two", "three", "four", "five"})
	for path, n := range map[string]int{"/a/c/params.json": 1, "/a/b/near.json": 1, "/root.json": 1, "/a/b/far.json": 1,
		"/a/params.json": 0, "/c/params.json": 0, "/a/near.json": 0, "/a/root.json": 0, "/a/b/deep/far.json": 0} {
		if got := s.count(path); got != n {
			t.Errorf("%s fetched %d times, want %d", path, got, n)
		}
	}
}

// One document is fetched once however many references reach it and
// however they spell it (load.go, Loader: "Only a URI no loaded document
// identifies is admitted and fetched"; a document is identified "by its
// retrieval URI"), and a reference back to the entry by its URI is not a
// second fetch either.
func TestDocumentFetchedOnce(t *testing.T) {
	s := newSite(t)
	var params []string
	for _, ref := range []string{"shared.json#/P0", "./shared.json#/P1", "sub/../shared.json#/P2", "@SELF@/shared.json#/P3", "other.json#/Q"} {
		params = append(params, fmt.Sprintf(`{"$ref":%q}`, ref))
	}
	s.put("/openapi.json", entry31(`"/p":{"get":{"operationId":"p","parameters":[`+strings.Join(params, ",")+`]}},
		"/q":{"get":{"operationId":"q","parameters":[{"$ref":"other.json#/Q2"},{"$ref":"other.json#/Q3"}]}}`,
		`"components":{"parameters":{"Local":{"name":"local","in":"query"}}}`))
	s.put("/shared.json", `{"P0":{"name":"p0","in":"query"},"P1":{"name":"p1","in":"query"},"P2":{"name":"p2","in":"query"},
		"P3":{"name":"p3","in":"query"},"P4":{"name":"p4","in":"query"}}`)
	s.put("/other.json", `{"Q":{"$ref":"shared.json#/P4"},"Q2":{"$ref":"openapi.json#/components/parameters/Local"},
		"Q3":{"$ref":"@SELF@/shared.json#/P0"}}`)
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	for _, key := range []string{"p", "q"} {
		if op := mustOp(t, c, key); op.Err != nil {
			t.Errorf("%s Err = %v", key, op.Err)
		}
	}
	for _, path := range []string{"/openapi.json", "/shared.json", "/other.json"} {
		if n := s.count(path); n != 1 {
			t.Errorf("%s fetched %d times, want once", path, n)
		}
	}
	if n := s.total(); n != 3 {
		t.Errorf("the site received %d requests, want 3", n)
	}
}

// waitBoth returns handlers for two documents that each serve only once
// the other's request has arrived, so the load completes only when the two
// are fetched at the same time; a handler that waits too long serves a 503,
// failing what reaches its document.
func waitBoth(content map[string]string) (http.HandlerFunc, http.HandlerFunc, func() bool) {
	a, b := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	timedOut := false
	serve := func(mine, other chan struct{}, body string) http.HandlerFunc {
		var once sync.Once
		return func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() { close(mine) })
			select {
			case <-other:
				io.WriteString(w, body)
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
				mu.Lock()
				timedOut = true
				mu.Unlock()
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}
	}
	return serve(a, b, content["a"]), serve(b, a, content["b"]), func() bool {
		mu.Lock()
		defer mu.Unlock()
		return timedOut
	}
}

// The documents of one load are fetched in parallel (load.go, Loader.Fetch:
// the loader may call Fetch "from several goroutines at once, so that a
// document split into several files loads in parallel"). Two documents the
// entry references are each served only once the other's request has
// arrived.
func TestDocumentsFetchedInParallel(t *testing.T) {
	entry := entry31(`"/a":{"$ref":"a.json"},"/b":{"$ref":"b.json"}`)
	content := map[string]string{"a": `{"get":{"operationId":"a"}}`, "b": `{"get":{"operationId":"b"}}`}
	check := func(t *testing.T, c *openapi.Client, timedOut bool) {
		t.Helper()
		if timedOut {
			t.Errorf("a document's request waited ten seconds for the other's: the documents were not fetched in parallel")
		}
		for _, key := range []string{"a", "b"} {
			if op := mustOp(t, c, key); op.Err != nil {
				t.Errorf("%s Err = %v", key, op.Err)
			}
		}
	}

	t.Run("default retrieval", func(t *testing.T) {
		s := newSite(t)
		s.put("/openapi.json", entry)
		ha, hb, timedOut := waitBoth(content)
		s.handle("/a.json", ha)
		s.handle("/b.json", hb)
		check(t, mustLoad(t, nil, s.uri("/openapi.json"), nil), timedOut())
	})

	t.Run("Fetch", func(t *testing.T) {
		const base = "https://docs.example.test/"
		m := newMemFetch(map[string]string{base + "openapi.json": entry, base + "a.json": content["a"], base + "b.json": content["b"]})
		arrived := map[string]chan struct{}{base + "a.json": make(chan struct{}), base + "b.json": make(chan struct{})}
		closers := map[string]*sync.Once{base + "a.json": {}, base + "b.json": {}}
		var mu sync.Mutex
		timedOut := false
		m.before = func(uri string) {
			mine, ok := arrived[uri]
			if !ok {
				return
			}
			closers[uri].Do(func() { close(mine) })
			other := arrived[base+"a.json"]
			if uri == base+"a.json" {
				other = arrived[base+"b.json"]
			}
			select {
			case <-other:
			case <-time.After(10 * time.Second):
				mu.Lock()
				timedOut = true
				mu.Unlock()
			}
		}
		c := mustLoad(t, &openapi.Loader{Fetch: m.fetch}, base+"openapi.json", nil)
		mu.Lock()
		defer mu.Unlock()
		check(t, c, timedOut)
	})
}

// load.go, DocumentURIs: "The entry is first; the rest are sorted by URI,
// independent of concurrent fetch order", and the list "includes loaded
// documents whose declarations are not exposed by Operations": one reached
// only by an unused component, one only by a webhook. The last document to
// be referenced is served first and the first last.
func TestDocumentURIsOrder(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		t.Run(fmt.Sprintf("reversed=%t", reversed), func(t *testing.T) {
			s := newSite(t)
			s.put("/openapi.json", entry31(`"/z":{"$ref":"z.json"},"/m":{"$ref":"m.json"},"/a":{"$ref":"a.json"}`,
				`"webhooks":{"ping":{"$ref":"hooks/ping.json"}}`,
				`"components":{"schemas":{"Unused":{"$ref":"unused/schema.json"}}}`))
			first, second := "/z.json", "/a.json"
			if reversed {
				first, second = second, first
			}
			served := make(chan struct{})
			var once sync.Once
			s.handle(first, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"get":{}}`)
				once.Do(func() { close(served) })
			})
			s.handle(second, func(w http.ResponseWriter, r *http.Request) {
				select { // serve after the other, when the two are fetched together
				case <-served:
				case <-time.After(2 * time.Second):
				case <-r.Context().Done():
				}
				io.WriteString(w, `{"get":{}}`)
			})
			s.put("/m.json", `{"get":{},"post":{"requestBody":{"$ref":"deep/body.json"}}}`)
			s.put("/deep/body.json", `{"content":{"application/json":{}}}`)
			s.put("/hooks/ping.json", `{"post":{"requestBody":{"$ref":"../hooks.json#/Body"}}}`)
			s.put("/hooks.json", `{"Body":{"content":{"application/json":{}}}}`)
			s.put("/unused/schema.json", `{"type":"object"}`)
			c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
			want := []string{s.uri("/openapi.json"), s.uri("/a.json"), s.uri("/deep/body.json"), s.uri("/hooks.json"),
				s.uri("/hooks/ping.json"), s.uri("/m.json"), s.uri("/unused/schema.json"), s.uri("/z.json")}
			wantStrings(t, "DocumentURIs", c.DocumentURIs(), want)
			if len(c.Operations()) != 4 {
				t.Errorf("%d operations, want 4: webhooks are not operations", len(c.Operations()))
			}
		})
	}
}

// load.go, Client.Document: a document is named by "the URI it was
// retrieved from"; "With a JSON Pointer fragment ... a copy of only that
// node, or nil when there is none; a Reference Object there is returned as
// written"; a fragment is a JSON Pointer percent-encoded as RFC 6901
// section 6 says; nil "when no document was loaded from uri". load.go,
// Loader: "A fragment is percent-decoded as UTF-8 before it is read as a
// JSON Pointer": references into keys with a space and a non-ASCII letter
// resolve.
func TestDocumentOfReferencedDocuments(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/p":{"get":{"operationId":"p","parameters":[
		{"$ref":"defs.yaml#/params/a%20b"},{"$ref":"defs.yaml#/params/%C3%A9t%C3%A9"}]}},
		"/q":{"get":{"operationId":"q","parameters":[{"$ref":"defs.yaml#/params/alias"}]}}`))
	s.put("/defs.yaml", "params:\n  a b: {name: spaced, in: query}\n  été: {name: summer, in: query}\n  alias:\n    $ref: '#/params/a%20b'\n")
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	op, q := mustOp(t, c, "p"), mustOp(t, c, "q")
	if op.Err != nil || len(op.Params) != 2 || q.Err != nil || len(q.Params) != 1 {
		t.Fatalf("p = %+v, q = %+v", op, q)
	}
	defs := s.uri("/defs.yaml")
	for i, p := range []*openapi.Param{op.Params[0], op.Params[1], q.Params[0]} {
		if want := []string{defs + "#/params/a%20b", defs + "#/params/%C3%A9t%C3%A9", defs + "#/params/a%20b"}[i]; p.Source != want {
			t.Errorf("parameter %d Source = %q, want %q", i, p.Source, want)
		}
	}
	sameJSON(t, "defs.yaml", c.Document(defs), []byte(`{"params":{"a b":{"name":"spaced","in":"query"},"été":{"name":"summer","in":"query"},"alias":{"$ref":"#/params/a%20b"}}}`))
	sameJSON(t, "a node", c.Document(defs+"#/params/%C3%A9t%C3%A9/name"), []byte(`"summer"`))
	sameJSON(t, "a Reference Object as written", c.Document(defs+"#/params/alias"), []byte(`{"$ref":"#/params/a%20b"}`))
	for _, uri := range []string{defs + "#/params/nobody", s.uri("/never.json"), s.uri("/never.json#/x")} {
		if got := c.Document(uri); got != nil {
			t.Errorf("Document(%q) = %s, want nil", uri, got)
		}
	}
	if got := c.Document(""); got == nil || c.Document(s.uri("/openapi.json")) == nil {
		t.Errorf("Document of the entry = nil")
	}
}

// The nearest $id sets the base inside a schema (load.go, Loader: "Inside a
// 3.1 or 3.2 schema, the nearest $id sets the base, as JSON Schema 2020-12
// says"; JSON Schema 2020-12 core section 8.2.1; OpenAPI 3.1.2 section 4.6:
// "Relative references in Schema Objects, including any that appear as $id
// values, use the nearest parent $id as a Base URI"). An $id outside a
// schema, on a Parameter Object, is no base. The targets are fetched where
// the bases put them; a schema reached shows in Media.Encoding, whose
// fields are the properties of the schemas a body's schema reaches by $ref
// (describe.go, Media.Encoding), each with the default content type of the
// schema reached.
func TestSchemaIDSetsTheBase(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/u":{"post":{"operationId":"u","requestBody":{"content":{"multipart/form-data":{"schema":
			{"$id":"@SELF@/schemas/root/","type":"object","properties":{
				"a":{"$ref":"a.json"},
				"b":{"$id":"sub/","$ref":"b.json"},
				"c":{"type":"object","properties":{"x":{"$ref":"c.json"}}}}}}}}}},
		"/p":{"get":{"operationId":"p","parameters":[{"$ref":"#/components/parameters/P"}]}}`,
		`"components":{"parameters":{"P":{"$id":"@SELF@/nope/","name":"p","in":"query","schema":{"$ref":"p.json"}}}}`))
	s.put("/schemas/root/a.json", `{"type":"object"}`)
	s.put("/schemas/root/sub/b.json", `{"type":"integer"}`)
	s.put("/schemas/root/c.json", `{"type":"string"}`)
	s.put("/p.json", `{"type":"string"}`)
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	wantStrings(t, "fields", encodingOf(t, c, "u"), []string{
		"a=application/json@" + s.uri("/openapi.json#/paths/~1u/post/requestBody/content/multipart~1form-data/schema/properties/a"),
		"b=text/plain@" + s.uri("/openapi.json#/paths/~1u/post/requestBody/content/multipart~1form-data/schema/properties/b"),
		"c=application/json@" + s.uri("/openapi.json#/paths/~1u/post/requestBody/content/multipart~1form-data/schema/properties/c"),
	})
	for path, n := range map[string]int{"/schemas/root/a.json": 1, "/schemas/root/sub/b.json": 1, "/schemas/root/c.json": 1, "/p.json": 1,
		"/a.json": 0, "/b.json": 0, "/schemas/root/b.json": 0, "/c.json": 0, "/nope/p.json": 0} {
		if got := s.count(path); got != n {
			t.Errorf("%s fetched %d times, want %d", path, got, n)
		}
	}
	// A schema in a referenced document with its own absolute $id resolves
	// its references against it: here another origin, whose admission the
	// callback sees.
	s.put("/other.json", `{"S":{"$id":"https://ids.example.test/pets/s","properties":{"x":{"$ref":"x.json"}}}}`)
	s.put("/openapi2.json", entry31(`"/v":{"post":{"operationId":"v","requestBody":{"content":{"application/json":{"schema":{"$ref":"other.json#/S"}}}}}}`))
	var mu sync.Mutex
	var asked []string
	l := &openapi.Loader{AllowReference: func(from, to string) bool {
		mu.Lock()
		asked = append(asked, to)
		mu.Unlock()
		return strings.HasPrefix(to, s.URL)
	}}
	mustLoad(t, l, s.uri("/openapi2.json"), nil)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(asked, "https://ids.example.test/pets/x.json") {
		t.Errorf("AllowReference was asked about %q; want https://ids.example.test/pets/x.json among them", asked)
	}
}

// load.go, Loader: "A reference resolves first to what loaded documents
// identify: a document by its retrieval URI ..., a schema by $id, a plain
// name by $anchor or $dynamicAnchor. This is decided once every document
// reached is parsed. Only a URI no loaded document identifies is admitted
// and fetched, and the fetched document is then searched the same way."
// (OpenAPI 3.1.2 section 4.3.1 and JSON Schema 2020-12 core section 9.1.2:
// a schema already available under a URI is used; section 8.2.1: $id "is
// an identifier and not necessarily a network locator".) Each identified
// URI here is outside the default boundary or unretrievable, so it resolves
// only by identification, and the callback is never asked about it.
func TestIdentifiedURIsResolveFirst(t *testing.T) {
	s := newSite(t)
	body := func(op, ref string) string {
		return fmt.Sprintf(`"/%s":{"post":{"operationId":"%s","requestBody":{"content":{"multipart/form-data":{"schema":{"$ref":%q}}}}}}`, op, op, ref)
	}
	s.put("/openapi.json", entry31(strings.Join([]string{
		body("byID", "https://ids.example.test/up"),
		body("byURN", "urn:example:pet"),
		body("byLaterID", "https://ids.example.test/thing"),
		body("byAnchor", "defs.json#Pet"),
		body("byEncodedAnchor", "defs.json#P%65t"),
		body("byDynamicAnchor", "defs.json#node"),
		body("byRelativeID", "#/components/schemas/Bar"),
		`"/p":{"get":{"operationId":"p","parameters":[{"$ref":"defs.json#/P"}]}}`,
	}, ","), `"components":{"schemas":{
		"Up":{"$id":"https://ids.example.test/up","type":"object","properties":{"n":{"type":"integer"}}},
		"Pet":{"$id":"urn:example:pet","properties":{"o":{"type":"object"}}},
		"Foo":{"$id":"https://example.com/api/schemas/foo","type":"object"},
		"Bar":{"$id":"https://example.com/api/schemas/bar","properties":{"f":{"$ref":"foo"}}}}}`))
	s.put("/defs.json", `{"P":{"name":"p","in":"query","schema":{"$ref":"@SELF@/openapi.json#/components/schemas/Up"}},
		"Thing":{"$id":"https://ids.example.test/thing","properties":{"t":{"type":"integer"}}},
		"PetDef":{"$anchor":"Pet","properties":{"a":{"type":"string"}}},
		"Node":{"$dynamicAnchor":"node","properties":{"d":{"type":"object"}}}}`)
	var mu sync.Mutex
	var asked []string
	l := &openapi.Loader{AllowReference: func(from, to string) bool {
		mu.Lock()
		asked = append(asked, to)
		mu.Unlock()
		return strings.HasPrefix(to, s.URL)
	}}
	c := mustLoad(t, l, s.uri("/openapi.json"), nil)
	entry, defs := s.uri("/openapi.json"), s.uri("/defs.json")
	for key, want := range map[string][]string{
		"byID":      {"n=text/plain@" + entry + "#/components/schemas/Up/properties/n"},
		"byURN":     {"o=application/json@" + entry + "#/components/schemas/Pet/properties/o"},
		"byLaterID": {"t=text/plain@" + defs + "#/Thing/properties/t"},
		"byAnchor":  {"a=text/plain@" + defs + "#/PetDef/properties/a"},
		// load.go, Loader: "A fragment is percent-decoded as UTF-8 before it
		// is read as a JSON Pointer or a plain name."
		"byEncodedAnchor": {"a=text/plain@" + defs + "#/PetDef/properties/a"},
		"byDynamicAnchor": {"d=application/json@" + defs + "#/Node/properties/d"},
		// "foo" resolves against Bar's $id to Foo's $id (an object).
		"byRelativeID": {"f=application/json@" + entry + "#/components/schemas/Bar/properties/f"},
	} {
		wantStrings(t, key, encodingOf(t, c, key), want)
	}
	if n := s.count("/openapi.json"); n != 1 {
		t.Errorf("the entry was fetched %d times; a reference to it by its URI is not a fetch", n)
	}
	if n := s.count("/defs.json"); n != 1 {
		t.Errorf("defs.json fetched %d times, want once", n)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, to := range asked {
		if !strings.HasPrefix(to, s.URL) || strings.HasPrefix(to, entry) {
			t.Errorf("AllowReference was asked about %q, which a loaded document identifies", to)
		}
	}
	if !slices.Contains(asked, defs) {
		t.Errorf("AllowReference was asked about %q; want %q among them", asked, defs)
	}
}

// load.go, Loader: "A URI claimed by two different documents or schemas is
// unresolvable, and the error names both" (JSON Schema 2020-12 core
// section 9.1.2: "there is no way for a URI to identify more than one
// schema"). Two schemas in two documents claim one $id; a document's
// retrieval URI is claimed by a schema too. The error is the Err of the
// part the reference reaches, wrapping ErrUnresolved (errors.go); it names
// each claimant by where it is written.
func TestURIClaimedTwice(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/dup":{"$ref":"https://ids.example.test/dup"},
		"/u":{"post":{"operationId":"u","requestBody":{"content":{"multipart/form-data":{"schema":{"$ref":"https://ids.example.test/dup"}}}}}},
		"/p":{"get":{"operationId":"p","parameters":[{"$ref":"params.json#/Limit"}]}},
		"/q":{"post":{"operationId":"q","requestBody":{"$ref":"late.json#/Body"}}}`,
		`"components":{"schemas":{"A":{"$id":"https://ids.example.test/dup","properties":{"a":{"type":"string"}}}}}`))
	s.put("/params.json", `{"Limit":{"name":"limit","in":"query"}}`)
	s.put("/late.json", `{"Body":{"content":{"application/json":{"schema":{"allOf":[{"$ref":"other.json#/B"},{"$ref":"#/X"}]}}}},
		"X":{"$id":"@SELF@/params.json","type":"object"}}`)
	s.put("/other.json", `{"B":{"$id":"https://ids.example.test/dup","properties":{"b":{"type":"string"}}}}`)
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)

	if got := encodingOf(t, c, "u"); len(got) != 0 {
		t.Errorf("a schema reference to a URI two schemas claim reached %q; want it unresolvable", got)
	}
	dup := mustOp(t, c, "GET /dup")
	if dup.Err == nil || !errors.Is(dup.Err, openapi.ErrUnresolved) {
		t.Fatalf("/dup Err = %v, want one wrapping ErrUnresolved", dup.Err)
	}
	for _, claimant := range []string{s.uri("/openapi.json#/components/schemas/A"), s.uri("/other.json#/B")} {
		if !strings.Contains(dup.Err.Error(), claimant) {
			t.Errorf("/dup Err %q does not name the claimant %s", dup.Err, claimant)
		}
	}
	p := mustOp(t, c, "p")
	if p.Err == nil || !errors.Is(p.Err, openapi.ErrUnresolved) {
		t.Fatalf("p Err = %v; params.json is claimed by its document and by a schema", p.Err)
	}
	for _, claimant := range []string{s.uri("/params.json"), s.uri("/late.json#/X")} {
		if !strings.Contains(p.Err.Error(), claimant) {
			t.Errorf("p Err %q does not name the claimant %s", p.Err, claimant)
		}
	}
	if op := mustOp(t, c, "q"); op.Err != nil {
		t.Errorf("q Err = %v; nothing it reaches is claimed twice", op.Err)
	}
}

// Reference cycles across documents are detected, never followed forever
// (load.go, Loader), and wrap ErrUnresolved (errors.go: "A reference cycle
// cannot be resolved: a chain of Reference Objects or of Path Item $refs, in
// any edition, ... that leads back into itself"). Each document of a cycle is
// fetched once.
func TestReferenceCyclesAcrossDocuments(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/a":{"$ref":"a.json"},"/ok":{"get":{"operationId":"ok"}},
		"/p":{"get":{"operationId":"p","parameters":[{"$ref":"p.json#/A"}]}}`))
	s.put("/a.json", `{"$ref":"b.json"}`)
	s.put("/b.json", `{"$ref":"a.json"}`)
	s.put("/p.json", `{"A":{"$ref":"q.json#/B"}}`)
	s.put("/q.json", `{"B":{"$ref":"p.json#/A"}}`)
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	if e := mustOp(t, c, "GET /a"); e.Err == nil || !errors.Is(e.Err, openapi.ErrUnresolved) {
		t.Errorf("/a Err = %v, want the cycle, wrapping ErrUnresolved", e.Err)
	}
	if op := mustOp(t, c, "p"); op.Err == nil || !errors.Is(op.Err, openapi.ErrUnresolved) {
		t.Errorf("p Err = %v, want the cycle, wrapping ErrUnresolved", op.Err)
	}
	if op := mustOp(t, c, "ok"); op.Err != nil {
		t.Errorf("ok Err = %v", op.Err)
	}
	for _, path := range []string{"/a.json", "/b.json", "/p.json", "/q.json"} {
		if n := s.count(path); n != 1 {
			t.Errorf("%s fetched %d times, want once", path, n)
		}
	}
}

// Regression check, not contract: a reference cycle's error text is
// deterministic. Two operations enter one cycle of parameter references at
// different points; each operation's Err is the same text whichever compiles
// first, and when they compile at once, in one document and across two. That
// each Err wraps ErrUnresolved stays asserted (errors.go, ErrUnresolved: "A
// reference cycle cannot be resolved: a chain of Reference Objects ... that
// leads back into itself").
func TestCycleNamedDeterministically(t *testing.T) {
	local := doc31(`"/x":{"get":{"operationId":"x","parameters":[{"$ref":"#/components/parameters/A"}]}},
		"/y":{"get":{"operationId":"y","parameters":[{"$ref":"#/components/parameters/B"}]}}`,
		`"components":{"parameters":{"A":{"$ref":"#/components/parameters/B"},"B":{"$ref":"#/components/parameters/C"},"C":{"$ref":"#/components/parameters/A"}}}`)
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/x":{"get":{"operationId":"x","parameters":[{"$ref":"p.json#/A"}]}},
		"/y":{"get":{"operationId":"y","parameters":[{"$ref":"q.json#/B"}]}}`))
	s.put("/p.json", `{"A":{"$ref":"q.json#/B"}}`)
	s.put("/q.json", `{"B":{"$ref":"p.json#/A"}}`)

	for name, load := range map[string]func() *openapi.Client{
		"one document":  func() *openapi.Client { return parseAt(t, local, "https://h.example.test", testDocURI, nil) },
		"two documents": func() *openapi.Client { return mustLoad(t, nil, s.uri("/openapi.json"), nil) },
	} {
		t.Run(name, func(t *testing.T) {
			errText := func(c *openapi.Client, key string) string {
				op := mustOp(t, c, key)
				if op.Err == nil || !errors.Is(op.Err, openapi.ErrUnresolved) {
					t.Fatalf("%s Err = %v, want the cycle, wrapping ErrUnresolved", key, op.Err)
				}
				return op.Err.Error()
			}
			c1 := load()
			x1, y1 := errText(c1, "x"), errText(c1, "y")
			c2 := load()
			y2, x2 := errText(c2, "y"), errText(c2, "x")
			if x1 != x2 || y1 != y2 {
				t.Errorf("the cycle is named by compile order:\nx first: x %q, y %q\ny first: x %q, y %q", x1, y1, x2, y2)
			}
			for range 4 {
				c3 := load()
				var wg sync.WaitGroup
				var x3, y3 string
				wg.Add(2)
				go func() { defer wg.Done(); x3 = fmt.Sprint(mustOpErr(c3, "x")) }()
				go func() { defer wg.Done(); y3 = fmt.Sprint(mustOpErr(c3, "y")) }()
				wg.Wait()
				if x3 != x1 || y3 != y1 {
					t.Errorf("concurrent first uses: x %q, y %q; want x %q, y %q", x3, y3, x1, y1)
				}
			}
		})
	}
}

// mustOpErr returns the Err of the operation named key, or the lookup's
// error; it is safe to call from any goroutine.
func mustOpErr(c *openapi.Client, key string) error {
	op, err := c.Operation(key)
	if err != nil {
		return err
	}
	return op.Err
}

// A failure disables only what reaches it (load.go, Loader.Origins: "A
// reference to any other origin, or its retrieval failing, disables only
// what reaches it"; Load fails only when "the document is unusable as a
// whole"): a document that is missing, one that cannot be parsed, and one
// on another origin each disable the operation, response or Paths entry
// that reaches them, whose Err wraps ErrUnresolved and names the reference
// (errors.go). A schema reaching one disables nothing (doc.go, Raw
// invocation boundary). The rest of the description stays callable.
func TestFailureDisablesOnlyWhatReachesIt(t *testing.T) {
	s, other := newSite(t), newSite(t)
	s.put("/openapi.json", entry31(`"/ok":{"get":{"operationId":"ok","parameters":[{"$ref":"good.json#/P"}],
			"responses":{"200":{"description":"ok"},"404":{"$ref":"missing.json#/R"}}}},
		"/missing":{"get":{"operationId":"missing","parameters":[{"$ref":"missing.json#/P"}]}},
		"/broken":{"get":{"operationId":"broken","parameters":[{"$ref":"bad.yaml#/P"}]}},
		"/elsewhere":{"get":{"operationId":"elsewhere","parameters":[{"$ref":"`+other.URL+`/p.json#/P"}]}},
		"/item":{"$ref":"missing.json#/Item"},
		"/schema":{"get":{"operationId":"schema","parameters":[{"name":"q","in":"query","schema":{"$ref":"missing.json#/S"}}]}}`))
	s.put("/good.json", `{"P":{"name":"p","in":"query"}}`)
	s.handle("/missing.json", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	s.put("/bad.yaml", "P:\n  name: p\n  name: again\n")
	other.put("/p.json", `{"P":{"name":"p","in":"query"}}`)

	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	ok := mustOp(t, c, "ok")
	if ok.Err != nil || len(ok.Params) != 1 || len(ok.Responses) != 2 {
		t.Fatalf("ok = %+v", ok)
	}
	if r := ok.Responses[1]; r.Err == nil || !errors.Is(r.Err, openapi.ErrUnresolved) {
		t.Errorf("ok 404 Err = %v, want one wrapping ErrUnresolved", r.Err)
	}
	for key, ref := range map[string]string{
		"missing":   "missing.json#/P",
		"broken":    "bad.yaml#/P",
		"elsewhere": other.URL + "/p.json#/P",
		"GET /item": "missing.json#/Item",
	} {
		op := mustOp(t, c, key)
		if op.Err == nil || !errors.Is(op.Err, openapi.ErrUnresolved) {
			t.Errorf("%s Err = %v, want one wrapping ErrUnresolved", key, op.Err)
			continue
		}
		if !strings.Contains(op.Err.Error(), ref) {
			t.Errorf("%s Err %q does not name the reference %s", key, op.Err, ref)
		}
		before := len(s.apiCalls())
		if _, err := c.Call(t.Context(), key, nil, nil); !errors.Is(err, op.Err) {
			t.Errorf("Call(%s) = %v, want a refusal wrapping its Err", key, err)
		}
		if len(s.apiCalls()) != before {
			t.Errorf("Call(%s) was sent", key)
		}
	}
	if n := other.total(); n != 0 {
		t.Errorf("another origin received %d requests", n)
	}
	mustCall(t, c, "ok", &openapi.Input{Params: map[string]any{"p": "v"}}, nil)
	if got := s.lastCall(t).RequestURI; got != "/ok?p=v" {
		t.Errorf("ok sent %s", got)
	}
	if op := mustOp(t, c, "schema"); op.Err != nil || op.Params[0].Err != nil {
		t.Errorf("schema: Err %v, param Err %v; an unresolved schema disables nothing", op.Err, op.Params[0].Err)
	}
	mustCall(t, c, "schema", &openapi.Input{Params: map[string]any{"q": "v"}}, nil)
	if c.Document(s.uri("/missing.json")) != nil || c.Document(s.uri("/bad.yaml")) != nil {
		t.Errorf("a document that failed is listed by Document")
	}
	for _, uri := range c.DocumentURIs() {
		if uri == s.uri("/missing.json") || uri == s.uri("/bad.yaml") || strings.HasPrefix(uri, other.URL) {
			t.Errorf("DocumentURIs lists %s, which was not loaded", uri)
		}
	}
}

// errors.go, ErrUnresolved: "The Err names the reference, and wraps the
// retrieval error too when fetching or reading its document failed ... or an
// error naming the refused URI when admission refused it, so a caller can
// tell a fixable fetch or admission ... from a broken document." The
// retrieval error is the Fetch error itself, or the http.Client's *url.Error.
func TestErrUnresolvedWrapsTheCause(t *testing.T) {
	errDown := errors.New("the partner's store is down")
	const base = "https://docs.example.test/"
	m := newMemFetch(map[string]string{
		base + "openapi.json": bare31(`"/f":{"get":{"operationId":"f","parameters":[{"$ref":"down.json#/P"}]}},
			"/a":{"get":{"operationId":"a","parameters":[{"$ref":"https://elsewhere.example.test/p.json#/P"}]}}`),
	})
	m.errs[base+"down.json"] = errDown
	c := mustLoad(t, &openapi.Loader{Fetch: m.fetch}, base+"openapi.json", nil)
	f := mustOp(t, c, "f")
	if !errors.Is(f.Err, openapi.ErrUnresolved) || !errors.Is(f.Err, errDown) {
		t.Errorf("f Err = %v, want one wrapping ErrUnresolved and the Fetch error", f.Err)
	}
	a := mustOp(t, c, "a")
	if !errors.Is(a.Err, openapi.ErrUnresolved) || !strings.Contains(fmt.Sprint(a.Err), "https://elsewhere.example.test/p.json") {
		t.Errorf("a Err = %v, want one wrapping ErrUnresolved naming the refused URI", a.Err)
	}
	if n := m.callsTo("https://elsewhere.example.test/p.json"); n != 0 {
		t.Errorf("Fetch was called %d times for a refused URI", n)
	}

	// The default retrieval's failure: nothing listens at the port.
	closed := newSite(t)
	gone := closed.URL
	closed.Close()
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/g":{"get":{"operationId":"g","parameters":[{"$ref":"`+gone+`/p.json#/P"}]}}`))
	c = mustLoad(t, &openapi.Loader{AllowReference: func(from, to string) bool { return true }}, s.uri("/openapi.json"), nil)
	g := mustOp(t, c, "g")
	var ue interface{ Timeout() bool } // *url.Error, among others
	if !errors.Is(g.Err, openapi.ErrUnresolved) || !errors.As(g.Err, &ue) {
		t.Errorf("g Err = %v (%T), want one wrapping ErrUnresolved and the retrieval error", g.Err, g.Err)
	}
}

// Discovery follows the OpenAPI object model, not the text (load.go, Loader:
// references "anywhere in a document, webhooks and callbacks included, ...;
// operationRef and externalValue are not retrieved"): every Reference
// Object, Path Item $ref and Schema Object $ref is followed, in webhooks,
// callbacks, links, examples, headers and unused components, and in every
// schema keyword whose value is a schema (JSON Schema 2020-12 core sections
// 8.2.4, $defs, 10, the applicators, and 11, unevaluated locations;
// validation section 8.5, contentSchema); a "$ref" in data (an example's
// value, an extension, a schema's default, const, enum, examples or
// extension keyword) is not a reference, nor are operationRef, externalValue
// and other URLs.
func TestDiscoveryFollowsTheModel(t *testing.T) {
	s := newSite(t)
	followed := []string{}
	ref := func(name string) string {
		followed = append(followed, "/f/"+name+".json")
		return fmt.Sprintf(`{"$ref":"f/%s.json"}`, name)
	}
	not := func(name string) string { return fmt.Sprintf(`{"$ref":"n/%s.json"}`, name) }
	var props []string
	for _, kw := range []string{"items", "not", "if", "then", "else", "contains", "additionalProperties", "propertyNames",
		"unevaluatedItems", "unevaluatedProperties", "contentSchema"} {
		props = append(props, fmt.Sprintf(`"%s":{"%s":%s}`, kw, kw, ref("schema-"+kw)))
	}
	for _, kw := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		props = append(props, fmt.Sprintf(`"%s":{"%s":[{"type":"object"},%s]}`, kw, kw, ref("schema-"+kw)))
	}
	for _, kw := range []string{"properties", "patternProperties", "dependentSchemas", "$defs"} {
		props = append(props, fmt.Sprintf(`"%s":{"%s":{"k":%s}}`, strings.TrimPrefix(kw, "$"), kw, ref("schema-"+strings.TrimPrefix(kw, "$"))))
	}
	props = append(props,
		`"dynamic":{"$dynamicRef":"f/schema-dynamicRef.json#node"}`,
		`"default":{"default":`+not("schema-default")+`}`,
		`"const":{"const":`+not("schema-const")+`}`,
		`"enum":{"enum":[`+not("schema-enum")+`]}`,
		`"examples":{"examples":[`+not("schema-examples")+`]}`,
		`"example":{"example":`+not("schema-example")+`}`,
		`"extension":{"x-meta":`+not("schema-extension")+`}`,
		`"mapped":{"oneOf":[{"$ref":"#/components/schemas/Cat"}],"discriminator":{"propertyName":"kind","mapping":{"dog":"./f/mapping-dog.json","cat":"Cat"}}}`,
	)
	followed = append(followed, "/f/schema-dynamicRef.json", "/f/mapping-dog.json")
	s.put("/openapi.json", entry31(`"/p":{"get":{"operationId":"p",
			"parameters":[{"name":"q","in":"query","schema":{"properties":{`+strings.Join(props, ",")+`}},
				"examples":{"value":{"value":`+not("example-value")+`},"ref":`+ref("example-object")+`,"external":{"externalValue":"n/external-value.json"}}}],
			"callbacks":{"onEvent":{"{$request.query.cb}":`+ref("callback-path-item")+`},"shared":`+ref("callback-object")+`},
			"responses":{"200":{"description":"ok","headers":{"X-H":`+ref("header")+`},
				"links":{"next":`+ref("link-object")+`,"by":{"operationRef":"n/operation-ref.json#/paths/~1x/get"}},
				"content":{"application/json":{"example":`+not("media-example")+`},"text/plain":{"examples":{"e":`+ref("media-example-object")+`}}}}},
			"x-ext":`+not("operation-extension")+`,
			"externalDocs":{"url":"n/external-docs.json"}}}`,
		`"webhooks":{"ping":`+ref("webhook")+`}`,
		`"x-root":`+not("root-extension"),
		`"components":{"schemas":{"Cat":{"type":"object"},"Unused":`+ref("unused-schema")+`},
			"parameters":{"Unused":`+ref("unused-parameter")+`},
			"responses":{"Unused":`+ref("unused-response")+`},
			"pathItems":{"Unused":`+ref("unused-path-item")+`},
			"examples":{"Unused":{"value":`+not("component-example-value")+`}}}`))
	for _, path := range followed {
		s.put(path, `{}`)
	}
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	if op := mustOp(t, c, "p"); op.Err != nil {
		t.Errorf("p Err = %v", op.Err)
	}
	uris := c.DocumentURIs()
	for _, path := range followed {
		if n := s.count(path); n != 1 {
			t.Errorf("%s fetched %d times, want once", path, n)
		}
		if !slices.Contains(uris, s.uri(path)) {
			t.Errorf("DocumentURIs does not list %s", path)
		}
	}
	for path, n := range s.hitsCopy() {
		if strings.HasPrefix(path, "/n/") || path == "/Cat" {
			t.Errorf("%s, which nothing references, was fetched %d times", path, n)
		}
	}
}

// hitsCopy returns the request count of every path.
func (s *site) hitsCopy() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.hits))
	for k, v := range s.hits {
		out[k] = v
	}
	return out
}

// A 3.2-only rule is not applied to a 3.1 document, which uses the edition
// declared at its root (load.go, Loader): a 3.1 document's $self is no base
// and no alias (load.go, Loader: the base is "its OpenAPI 3.2 $self ... or
// else" the retrieval URI; Document "also accepts an OpenAPI 3.2 document's
// $self"); defaultMapping, a 3.2 Discriminator field, is not followed; a
// Media Type Object's "$ref" is not a reference before 3.2; and a security
// requirement name that is not a component name is "in earlier editions ...
// a defect of the requirement" (load.go, SchemeLookup), never a URI to
// fetch.
func TestThreeTwoRulesNotAppliedToThreeOne(t *testing.T) {
	s := newSite(t)
	s.put("/api/openapi.json", `{"openapi":"3.1.0","$self":"https://self.example.test/base/openapi.json","info":{"title":"t","version":"1"},
		"servers":[{"url":"@SELF@"}],
		"paths":{
			"/p":{"get":{"operationId":"p","parameters":[{"$ref":"params.json#/P"}],
				"security":[{"schemes.json#/components/securitySchemes/k":[]}],
				"responses":{"200":{"description":"ok","content":{"application/json":{"$ref":"media.json#/M"}}}}}},
			"/d":{"post":{"operationId":"d","requestBody":{"content":{"application/json":{"schema":
				{"oneOf":[{"type":"object"}],"discriminator":{"propertyName":"k","defaultMapping":"default.json#/D"}}}}}}}}}`)
	s.put("/api/params.json", `{"P":{"name":"p","in":"query"}}`)
	c := mustLoad(t, nil, s.uri("/api/openapi.json"), nil)
	if op := mustOp(t, c, "p"); op.Err != nil || len(op.Params) != 1 || op.Params[0].Source != s.uri("/api/params.json#/P") {
		t.Errorf("p = Err %v, Params %+v; params.json resolves against the retrieval URI", op.Err, op.Params)
	}
	if c.Document("https://self.example.test/base/openapi.json") != nil {
		t.Errorf("Document accepts a 3.1 document's $self")
	}
	wantStrings(t, "DocumentURIs", c.DocumentURIs(), []string{s.uri("/api/openapi.json"), s.uri("/api/params.json")})
	for _, path := range []string{"/api/schemes.json", "/api/media.json", "/api/default.json"} {
		if n := s.count(path); n != 0 {
			t.Errorf("%s fetched %d times; the rule that would reach it is OpenAPI 3.2's", path, n)
		}
	}
	op := mustOp(t, c, "p")
	if len(op.Security) != 1 || len(op.Security[0].Schemes) != 1 || op.Security[0].Schemes[0].Err == nil || op.Security[0].Schemes[0].Source != "" {
		t.Errorf("p Security = %+v; want the URI-like name a defect of the requirement, with no Source", op.Security)
	}
}

// load.go, SchemeLookup: "A SchemeLookup says where the component names that
// a security requirement in a referenced document uses are looked up ...
// SchemesInEntry and SchemesInReferrer each follow one exactly. The default,
// SchemesInEntryFirst, looks in the entry document, then, for a name it does
// not define, in the referring one ... in earlier editions it is a defect of
// the requirement." OpenAPI 3.1.2 section 4.3.3 and Appendix F, whose
// documents these are: the entry defines MySecurity as bearer, the
// referenced document "other" as basic. OnlyRef is defined only in other,
// OnlyEntry only in the entry. describe.go, SecurityScheme.Source: "It tells
// which document a scheme name was found in. It is empty for a scheme the
// document never declares."
func TestSchemeLookup(t *testing.T) {
	s := newSite(t)
	s.put("/api/description/openapi", `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@SELF@"}],
		"components":{"securitySchemes":{
			"MySecurity":{"type":"http","scheme":"bearer","bearerFormat":"JWT"},
			"OnlyEntry":{"type":"apiKey","in":"header","name":"X-Entry"}}},
		"paths":{"/foo":{"$ref":"other#/components/pathItems/Foo"},
			"/bar":{"get":{"operationId":"bar","security":[{"MySecurity":[]}]}}}}`)
	s.put("/api/description/other", `components:
  securitySchemes:
    MySecurity:
      type: http
      scheme: basic
    OnlyRef:
      type: apiKey
      in: header
      name: X-Ref
  pathItems:
    Foo:
      get:
        operationId: foo
        security:
          - MySecurity: []
          - OnlyRef: []
          - OnlyEntry: []
`)
	entry, other := s.uri("/api/description/openapi"), s.uri("/api/description/other")
	type found struct{ typ, scheme, source string } // source "" for a defect
	bearer := found{"http", "bearer", entry + "#/components/securitySchemes/MySecurity"}
	basic := found{"http", "basic", other + "#/components/securitySchemes/MySecurity"}
	onlyRef := found{"apiKey", "", other + "#/components/securitySchemes/OnlyRef"}
	onlyEntry := found{"apiKey", "", entry + "#/components/securitySchemes/OnlyEntry"}
	defect := found{}
	for _, tt := range []struct {
		lookup openapi.SchemeLookup
		name   string
		want   [3]found // MySecurity, OnlyRef, OnlyEntry
	}{
		{openapi.SchemesInEntryFirst, "SchemesInEntryFirst", [3]found{bearer, onlyRef, onlyEntry}},
		{openapi.SchemesInEntry, "SchemesInEntry", [3]found{bearer, defect, onlyEntry}},
		{openapi.SchemesInReferrer, "SchemesInReferrer", [3]found{basic, onlyRef, defect}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := mustLoad(t, &openapi.Loader{SchemeLookup: tt.lookup}, entry, nil)
			op := mustOp(t, c, "foo")
			if op.Err != nil || len(op.Security) != 3 {
				t.Fatalf("foo = Err %v, Security %+v", op.Err, op.Security)
			}
			for i, w := range tt.want {
				sc := op.Security[i].Schemes[0]
				got := found{sc.Type, sc.Scheme, sc.Source}
				if w == defect {
					if sc.Err == nil || sc.Source != "" {
						t.Errorf("%s: %+v, want a defect (Err set, no Source)", sc.Name, sc)
					}
					continue
				}
				if got != w || sc.Err != nil {
					t.Errorf("%s: %+v, Err %v; want %+v", sc.Name, got, sc.Err, w)
				}
			}
			// A requirement in the entry is looked up in the entry, whichever
			// lookup: the entry is the referring document.
			if bar := mustOp(t, c, "bar"); bar.Security[0].Schemes[0].Source != bearer.source {
				t.Errorf("bar's MySecurity is %q, want the entry's", bar.Security[0].Schemes[0].Source)
			}

			// The call places the scheme that was found.
			cred := openapi.Secret("tok")
			if tt.lookup == openapi.SchemesInReferrer {
				cred = openapi.Basic("u", "p")
			}
			cc := mustLoad(t, &openapi.Loader{SchemeLookup: tt.lookup}, entry, &openapi.Options{
				Credentials: map[string]openapi.Credential{"MySecurity": cred},
				SecurityKey: `{"MySecurity":[]}`,
			})
			mustCall(t, cc, "foo", nil, nil)
			auth := s.lastCall(t).Header.Get("Authorization")
			if wantPrefix := map[bool]string{true: "Basic ", false: "Bearer tok"}[tt.lookup == openapi.SchemesInReferrer]; !strings.HasPrefix(auth, wantPrefix) {
				t.Errorf("Authorization %q, want %q...", auth, wantPrefix)
			}
		})
	}

	// doc.go, Credentials: Load refuses "any credential but FromTransport
	// for a name all of whose schemes are ... undeclared or declared
	// defectively"; a name used only in the referenced document is used
	// (not "a Credentials name the document never uses").
	if _, err := (&openapi.Loader{}).Load(t.Context(), entry, &openapi.Options{Credentials: map[string]openapi.Credential{"OnlyRef": openapi.Secret("r")}}); err != nil {
		t.Errorf("OnlyRef, used in other: %v", err)
	}
	_, err := (&openapi.Loader{SchemeLookup: openapi.SchemesInEntry}).Load(t.Context(), entry, &openapi.Options{Credentials: map[string]openapi.Credential{"OnlyRef": openapi.Secret("r")}})
	if re := asRequestError(t, err); re.Settings[`Options.Credentials["OnlyRef"]`] == nil {
		t.Errorf("SchemesInEntry, a static credential for the undeclared OnlyRef: %v, want it refused at its key", err)
	}
	if _, err := (&openapi.Loader{SchemeLookup: openapi.SchemesInEntry}).Load(t.Context(), entry, &openapi.Options{Credentials: map[string]openapi.Credential{"OnlyRef": openapi.FromTransport()}}); err != nil {
		t.Errorf("SchemesInEntry, FromTransport for OnlyRef: %v", err)
	}
}

// describe.go, Server.ID: "an opaque identifier unique among distinct server
// declarations in one Client, including entries with the same URL and name.
// An inherited declaration keeps its ID across operations. It is stable for
// the same loaded document"; client.go, Options.ServerID: "An ID not used
// anywhere in the document is refused by Load". Servers declared alike in
// two documents have distinct IDs; each selects its own server, at Load too,
// where only an ID from an earlier load of the same documents can be given,
// so IDs do not depend on the order in which documents arrive.
func TestServerIDAcrossDocuments(t *testing.T) {
	const base = "https://docs.example.test/"
	docs := map[string]string{
		base + "openapi.json": `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],
			"paths":{"/a":{"$ref":"a.json"},"/b":{"$ref":"b.json"}}}`,
		base + "a.json": `{"servers":[{"url":"https://api.example.test"},{"url":"https://other.example.test"}],"get":{"operationId":"getA"},"post":{"operationId":"postA"}}`,
		base + "b.json": `{"servers":[{"url":"https://api.example.test"},{"url":"https://other.example.test"}],"get":{"operationId":"getB"}}`,
	}
	// load returns a Client whose a.json arrives before b.json, or after.
	load := func(t *testing.T, aFirst bool, opts *openapi.Options) (*openapi.Client, error) {
		m := newMemFetch(docs)
		first, second := base+"a.json", base+"b.json"
		if !aFirst {
			first, second = second, first
		}
		done := make(chan struct{})
		var once sync.Once
		m.before = func(uri string) {
			if uri == second {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
			}
		}
		l := &openapi.Loader{Fetch: func(ctx context.Context, uri string) (io.ReadCloser, string, error) {
			r, final, err := m.fetch(ctx, uri)
			if uri == first {
				once.Do(func() { close(done) })
			}
			return r, final, err
		}}
		return l.Load(t.Context(), base+"openapi.json", opts)
	}
	c, err := load(t, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	getA, postA, getB := mustOp(t, c, "getA"), mustOp(t, c, "postA"), mustOp(t, c, "getB")
	if len(getA.Servers) != 2 || len(postA.Servers) != 2 || len(getB.Servers) != 2 {
		t.Fatalf("servers: %d, %d, %d", len(getA.Servers), len(postA.Servers), len(getB.Servers))
	}
	ids := map[string]string{}
	for _, sv := range []*openapi.Server{getA.Servers[0], getA.Servers[1], getB.Servers[0], getB.Servers[1]} {
		if prev, dup := ids[sv.ID]; dup {
			t.Errorf("servers %s and %s share the ID %q", prev, sv.Source, sv.ID)
		}
		ids[sv.ID] = sv.Source
	}
	for i := range 2 {
		if getA.Servers[i].ID != postA.Servers[i].ID {
			t.Errorf("a.json's server %d has IDs %q and %q in its two operations", i, getA.Servers[i].ID, postA.Servers[i].ID)
		}
	}
	c2, err := load(t, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustOp(t, c2, "getB").Servers[0].ID; got != getB.Servers[0].ID {
		t.Errorf("b.json's first server is %q when b.json arrives first, %q when a.json does", got, getB.Servers[0].ID)
	}
	if got := mustOp(t, c2, "getA").Servers[1].ID; got != getA.Servers[1].ID {
		t.Errorf("a.json's second server is %q when b.json arrives first, %q when a.json does", got, getA.Servers[1].ID)
	}

	// Each ID selects its own server, given to Load.
	for _, sv := range []*openapi.Server{getA.Servers[1], getB.Servers[0]} {
		c3, err := load(t, false, &openapi.Options{ServerID: sv.ID})
		if err != nil {
			t.Errorf("Load with the ServerID of %s: %v", sv.Source, err)
			continue
		}
		key := "getA"
		if strings.HasPrefix(sv.Source, base+"b.json") {
			key = "getB"
		}
		req, err := c3.Prepare(key, nil)
		if err != nil {
			t.Errorf("Prepare(%s) with ServerID %q: %v", key, sv.ID, err)
			continue
		}
		if !strings.HasPrefix(req.HTTP.URL.String(), sv.URL) {
			t.Errorf("Prepare(%s) with the ID of %s went to %s", key, sv.Source, req.HTTP.URL)
		}
	}
	// Derived Clients keep them.
	if got := mustOp(t, c.With(func(o *openapi.Options) {}), "getB").Servers[0].ID; got != getB.Servers[0].ID {
		t.Errorf("With changed an ID: %q, was %q", got, getB.Servers[0].ID)
	}
}

// describe.go, Operation: "Inherited declarations (path-level parameters,
// the entry document's root servers, and root security) are already
// applied"; OpenAPI 3.1.2 section 4.3.3: "only the entry document's Paths
// Object contributes URLs to the described API". An operation in a
// referenced OpenAPI document takes the entry's root servers and security,
// not its own document's.
func TestRootDeclarationsComeFromTheEntry(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@SELF@/v1"}],
		"security":[{"key":[]}],
		"components":{"securitySchemes":{"key":{"type":"apiKey","in":"header","name":"X-Key"}}},
		"paths":{"/foo":{"$ref":"other.json#/components/pathItems/Foo"}}}`)
	s.put("/other.json", `{"openapi":"3.1.0","info":{"title":"o","version":"1"},"servers":[{"url":"https://wrong.example.test"}],
		"security":[{"other":[]}],
		"components":{"securitySchemes":{"other":{"type":"apiKey","in":"query","name":"o"}},
			"pathItems":{"Foo":{"get":{"operationId":"foo"}}}},
		"paths":{"/never":{"get":{"operationId":"never"}}}}`)
	c := mustLoad(t, nil, s.uri("/openapi.json"), &openapi.Options{Credentials: map[string]openapi.Credential{"key": openapi.Secret("k")}})
	op := mustOp(t, c, "foo")
	if len(op.Servers) != 1 || op.Servers[0].Source != s.uri("/openapi.json#/servers/0") {
		t.Errorf("foo Servers = %+v, want the entry's root server", op.Servers)
	}
	if len(op.Security) != 1 || op.Security[0].Key != `{"key":[]}` {
		t.Errorf("foo Security = %+v, want the entry's root security", op.Security)
	}
	// Only the entry's Paths Object describes operations.
	if _, err := c.Operation("never"); !errors.Is(err, openapi.ErrNoOperation) {
		t.Errorf("Operation(never) = %v; a referenced document's paths are not operations", err)
	}
	mustCall(t, c, "foo", nil, nil)
	if got := s.lastCall(t); got.RequestURI != "/v1/foo" || got.Header.Get("X-Key") != "k" {
		t.Errorf("foo sent %s with X-Key %q", got.RequestURI, got.Header.Get("X-Key"))
	}
}

// load.go, Parse: "The uri, if not empty, is the absolute URI ... the
// document is meant to live at, which stands for the URI it was retrieved
// from and is never fetched itself"; Loader.Parse: l's settings "govern the
// documents its references reach". References resolve against the uri and
// are fetched; one back to the uri is not.
func TestParseFollowsReferences(t *testing.T) {
	s := newSite(t)
	content := entry31(`"/p":{"get":{"operationId":"p","parameters":[{"$ref":"params.json#/P"}]}}`,
		`"components":{"parameters":{"L":{"name":"l","in":"query"}}}`)
	s.put("/params.json", `{"P":{"$ref":"openapi.json#/components/parameters/L"}}`)
	c, err := (&openapi.Loader{}).Parse(t.Context(), []byte(strings.ReplaceAll(content, "@SELF@", s.URL)), s.uri("/openapi.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	op := mustOp(t, c, "p")
	if op.Err != nil || len(op.Params) != 1 || op.Params[0].Source != s.uri("/openapi.json#/components/parameters/L") {
		t.Errorf("p = Err %v, Params %+v", op.Err, op.Params)
	}
	if n := s.count("/openapi.json"); n != 0 {
		t.Errorf("Parse fetched its own uri %d times", n)
	}
	wantStrings(t, "DocumentURIs", c.DocumentURIs(), []string{s.uri("/openapi.json"), s.uri("/params.json")})
}

// Loader.Parse: "With an empty uri, absolute external references can be
// fetched only when Origins or AllowReference admits them; relative external
// references have no base unless an OpenAPI 3.2 absolute $self supplies
// one." Parse: "With an empty uri, the document may reference only itself".
func TestParseEmptyURIReferences(t *testing.T) {
	s := newSite(t)
	s.put("/params.json", `{"P":{"name":"p","in":"query"}}`)
	content := []byte(bare31(`"/a":{"get":{"operationId":"a","parameters":[{"$ref":"` + s.URL + `/params.json#/P"}]}},
		"/r":{"get":{"operationId":"r","parameters":[{"$ref":"params.json#/P"}]}}`))
	c, err := openapi.Parse(t.Context(), content, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "r"} {
		if op := mustOp(t, c, key); !errors.Is(op.Err, openapi.ErrUnresolved) {
			t.Errorf("%s Err = %v, want unresolved", key, op.Err)
		}
	}
	if n := s.total(); n != 0 {
		t.Errorf("an empty-uri Parse fetched %d documents without AllowReference", n)
	}
	var mu sync.Mutex
	var froms []string
	c, err = (&openapi.Loader{AllowReference: func(from, to string) bool {
		mu.Lock()
		froms = append(froms, from)
		mu.Unlock()
		return true
	}}).Parse(t.Context(), content, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if op := mustOp(t, c, "a"); op.Err != nil {
		t.Errorf("a Err = %v with AllowReference admitting it", op.Err)
	}
	if op := mustOp(t, c, "r"); !errors.Is(op.Err, openapi.ErrUnresolved) {
		t.Errorf("r Err = %v; a relative reference has no base", op.Err)
	}
	if n := s.count("/params.json"); n != 1 {
		t.Errorf("params.json fetched %d times, want once", n)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, f := range froms {
		if f != c.DocumentURIs()[0] {
			t.Errorf("AllowReference from %q, want the document's derived URI %q", f, c.DocumentURIs()[0])
		}
	}
}

// doc.go, Raw invocation boundary: "A broken or inaccessible reference that
// hides one of those facts can prevent preparation. A caller may configure
// Loader.Fetch and AllowReference to supply trusted referenced documents."
// A required parameter on another origin keeps the operation from being
// prepared; the caller's Fetch, admitting that origin, supplies it.
func TestTrustedDocumentsSuppliedByTheCaller(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/p/{id}":{"get":{"operationId":"p","parameters":[{"$ref":"https://partner.example.test/params.json#/Id"}]}}`))
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	if _, err := c.Prepare("p", &openapi.Input{Params: map[string]any{"id": "7"}}); err == nil {
		t.Errorf("Prepare succeeded with the parameter's document out of reach")
	}
	partner := map[string]string{"https://partner.example.test/params.json": `{"Id":{"name":"id","in":"path","required":true}}`}
	l := &openapi.Loader{
		Fetch: func(ctx context.Context, uri string) (io.ReadCloser, string, error) {
			if doc, ok := partner[uri]; ok {
				return io.NopCloser(strings.NewReader(doc)), uri, nil
			}
			req, err := http.NewRequestWithContext(ctx, "GET", uri, nil)
			if err != nil {
				return nil, "", err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, "", err
			}
			return resp.Body, uri, nil
		},
		AllowReference: func(from, to string) bool {
			_, ok := partner[to]
			return ok || strings.HasPrefix(to, s.URL)
		},
	}
	c = mustLoad(t, l, s.uri("/openapi.json"), nil)
	mustCall(t, c, "p", &openapi.Input{Params: map[string]any{"id": "7"}}, nil)
	if got := s.lastCall(t).RequestURI; got != "/p/7" {
		t.Errorf("sent %s", got)
	}
}

// Load's checks of Options against the document (client.go, Options:
// "A value that matches no server of the document is refused by Load"; "A
// name that appears in no server URL of the document is refused by Load";
// "A type no operation declares is refused by Load"; SecurityKey: "A key
// that names no alternative ... is refused by Load"; doc.go, Credentials:
// "Load refuses a Credentials name the document never uses") cover every
// operation the Client describes, those written in referenced documents
// included. A misspelling is still refused.
func TestLoadChecksReachReferencedDocuments(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/a":{"$ref":"a.json"}`,
		`"components":{"securitySchemes":{"akey":{"type":"apiKey","in":"header","name":"X-A"}}}`))
	s.put("/a.json", `{"servers":[{"url":"https://{region}.example.test","variables":{"region":{"default":"us"}}}],
		"post":{"operationId":"a","security":[{"akey":[]}],"requestBody":{"content":{"application/vnd.a+json":{}}}}}`)
	opts := func() *openapi.Options {
		return &openapi.Options{
			Server:      "https://{region}.example.test",
			Variables:   map[string]string{"region": "eu"},
			MediaType:   "application/vnd.a+json",
			SecurityKey: `{"akey":[]}`,
			Credentials: map[string]openapi.Credential{"akey": openapi.Secret("k")},
		}
	}
	c := mustLoad(t, nil, s.uri("/openapi.json"), opts())
	req, err := c.Prepare("a", &openapi.Input{Body: map[string]any{"x": 1}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := req.HTTP.URL.String(); got != "https://eu.example.test/a" {
		t.Errorf("prepared %s", got)
	}
	o := opts()
	o.Variables = map[string]string{"regoin": "eu"}
	_, err = openapi.Load(t.Context(), s.uri("/openapi.json"), o)
	wantKeys(t, "Settings", asRequestError(t, err).Settings, true, `Options.Variables["regoin"]`)
}

// describe.go, Operation: "A Path Item's $ref and the Path Item's own fields
// are read together: a field on one side only is used, and a field on both
// sides ... sets Err on each operation whose request it affects"; "where a
// parameter ... is a Reference Object that gives a description, that
// description replaces the target's, the Reference Object nearest the use
// site winning, while Source still names the target": across documents as
// within one.
func TestPathItemsAndDescriptionsAcrossDocuments(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/both":{"$ref":"item.json","post":{"operationId":"localPost"},"get":{"operationId":"localGet"},
			"parameters":[{"$ref":"params.json#/Mid","description":"use site"}]}`))
	s.put("/item.json", `{"get":{"operationId":"targetGet"},"put":{"operationId":"targetPut","parameters":[{"$ref":"params.json#/Mid"}]}}`)
	s.put("/params.json", `{"Mid":{"$ref":"more.json#/Target","description":"middle"}}`)
	s.put("/more.json", `{"Target":{"name":"t","in":"query","description":"target"}}`)
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	if op := mustOp(t, c, "GET /both"); op.Err == nil {
		t.Errorf("GET /both, defined on both sides, has no Err")
	}
	post := mustOp(t, c, "localPost")
	if post.Err != nil || len(post.Params) != 1 || post.Params[0].Description != "use site" || post.Params[0].Source != s.uri("/more.json#/Target") {
		t.Errorf("localPost = Err %v, Params %+v", post.Err, post.Params)
	}
	put := mustOp(t, c, "targetPut")
	if put.Err != nil || put.Source != s.uri("/item.json#/put") {
		t.Errorf("targetPut = Err %v, Source %q", put.Err, put.Source)
	}
	// The path-level parameter, from the use site, then the operation's own,
	// the same parameter, which overrides it (doc.go, Fixed rules, Order).
	if len(put.Params) != 1 || put.Params[0].Description != "middle" {
		t.Errorf("targetPut Params = %+v, want the operation's parameter, described by the nearest Reference Object", put.Params)
	}
}

// load.go, Loader: "A reference that ... reaches a schema by a JSON
// Pointer crossing a nearer $id, still resolves; it stays visible as
// written where it is written, in a Schema's Raw". JSON Schema 2020-12 core
// section 9.2.1 leaves such pointers to the implementation.
func TestPointerCrossingANearerID(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/u":{"post":{"operationId":"u","requestBody":{"content":{"multipart/form-data":{"schema":
		{"$ref":"defs.json#/Outer/properties/inner"}}}}}}`))
	s.put("/defs.json", `{"Outer":{"$id":"https://ids.example.test/outer","properties":{"inner":{"properties":{"n":{"type":"integer"}}}}}}`)
	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	wantStrings(t, "fields", encodingOf(t, c, "u"), []string{"n=text/plain@" + s.uri("/defs.json#/Outer/properties/inner/properties/n")})
	sameJSON(t, "Raw", reqMedia(t, mustOp(t, c, "u"), 0).Schema.Raw(), []byte(`{"$ref":"defs.json#/Outer/properties/inner"}`))
}
