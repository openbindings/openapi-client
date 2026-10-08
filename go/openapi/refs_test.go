package openapi_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Local references: Path Item $ref and Reference Objects for parameters,
// request bodies and responses, chains, cycles, broken references, and
// references to other documents.

// describe.go, Operation: "The descriptions follow references. A Path
// Item's $ref and the Path Item's own fields are read together: a field on
// one side only is used, and a field on both sides, which OpenAPI leaves
// undefined, sets Err on each operation whose request it affects".
// describe.go, Client.Operations lists, "each with an empty Key", "a Paths
// entry that cannot be read, because its key does not begin with "/" or its
// $ref cannot be followed: listed once, with its Path and no Method".
// describe.go, Client.Operation: such an entry is returned "for a
// method-and-path key with that path and any method; calling with the same
// key is refused with an error wrapping that Err".
func TestPathItemRef(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/pets":{"$ref":"#/components/pathItems/Pets"},
		"/both":{"$ref":"#/components/pathItems/Both","get":{"operationId":"bothGetLocal"},"post":{"operationId":"bothPost"}},
		"/broken":{"$ref":"#/components/pathItems/Nope"},
		"/cycle":{"$ref":"#/components/pathItems/CycleA"}`,
		`"components":{"pathItems":{
			"Pets":{"get":{"operationId":"listPets"},"parameters":[{"name":"limit","in":"query","schema":{"type":"integer"}}]},
			"Both":{"get":{"operationId":"bothGetTarget"},"put":{"operationId":"bothPut"}},
			"CycleA":{"$ref":"#/components/pathItems/CycleB"},
			"CycleB":{"$ref":"#/components/pathItems/CycleA"}
		}}`)
	c := parseFor(t, w, doc, nil)
	uri := w.URL + "/openapi.json"

	ops := c.Operations()
	var paths []string
	for _, op := range ops {
		paths = append(paths, op.Path)
	}
	wantPaths := []string{"/pets", "/both", "/both", "/both", "/broken", "/cycle"}
	if len(ops) != len(wantPaths) {
		t.Fatalf("Operations paths = %q, want %q", paths, wantPaths)
	}
	for i, p := range wantPaths {
		if paths[i] != p {
			t.Errorf("Operations paths = %q, want %q", paths, wantPaths)
			break
		}
	}

	// The target's operation, written at the target.
	pets := mustOp(t, c, "listPets")
	if pets.Path != "/pets" || pets.Method != "GET" || pets.Err != nil {
		t.Errorf("listPets = %s %s, Err %v", pets.Method, pets.Path, pets.Err)
	}
	if want := uri + "#/components/pathItems/Pets/get"; pets.Source != want {
		t.Errorf("listPets Source = %q, want %q", pets.Source, want)
	}
	if len(pets.Params) != 1 || pets.Params[0].Source != uri+"#/components/pathItems/Pets/parameters/0" {
		t.Errorf("listPets Params = %+v", pets.Params)
	}
	mustCall(t, c, "listPets", &openapi.Input{Params: map[string]any{"limit": 5}}, nil)
	if got := w.last(t).RequestURI; got != "/pets?limit=5" {
		t.Errorf("listPets sent %q", got)
	}

	// get on both sides: that operation has Err; put and post, on one side
	// each, are used.
	if op := mustOp(t, c, "GET /both"); op.Err == nil {
		t.Errorf("GET /both, defined on both sides, has no Err")
	}
	if op := mustOp(t, c, "bothPut"); op.Err != nil || op.Method != "PUT" {
		t.Errorf("bothPut = %s, Err %v", op.Method, op.Err)
	}
	if op := mustOp(t, c, "bothPost"); op.Err != nil || op.Method != "POST" {
		t.Errorf("bothPost = %s, Err %v", op.Method, op.Err)
	}

	// The unreadable entries.
	for _, e := range ops[4:] {
		if e.Key != "" || e.Err == nil {
			t.Errorf("defect entry %s: Key %q, Err %v; want empty Key and an Err", e.Path, e.Key, e.Err)
		}
	}
	// errors.go, ErrUnresolved: wrapped by "the Err of a part, or of a
	// SchemaReference, whose defect is a reference that cannot be resolved".
	if !errors.Is(ops[4].Err, openapi.ErrUnresolved) {
		t.Errorf("/broken Err %v does not wrap ErrUnresolved", ops[4].Err)
	}
	before := w.count()
	for _, key := range []string{"GET /broken", "DELETE /broken"} {
		e := mustOp(t, c, key)
		if e.Err == nil || e.Path != "/broken" {
			t.Errorf("Operation(%q) = %s, Err %v", key, e.Path, e.Err)
			continue
		}
		resp, err := c.Call(t.Context(), key, nil, nil)
		re := asRequestError(t, err)
		if resp != nil || !errors.Is(re, e.Err) {
			t.Errorf("Call(%q) = %v, %v; want a refusal wrapping the entry's Err", key, resp, err)
		}
	}
	if w.count() != before {
		t.Errorf("a call to an unreadable path was sent")
	}
}

// describe.go, Operation: "where a parameter ... is a Reference Object that
// gives a description, that description replaces the target's, the
// Reference Object nearest the use site winning, while Source still names
// the target". describe.go, Param.Source: the Parameter Object's location.
// load.go, Loader: "A fragment is percent-decoded as UTF-8 before it is
// read as a JSON Pointer"; RFC 6901 section 4: "~1" is "/".
func TestParameterRefs(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/p/{id}":{"get":{"operationId":"refs","parameters":[
			{"$ref":"#/components/parameters/Id"},
			{"$ref":"#/components/parameters/LimitAlias"},
			{"$ref":"#/components/parameters/Near","description":"near"},
			{"$ref":"#/components/parameters/a%20b"},
			{"$ref":"#/components/parameters/x~1y"}
		]}},
		"/q":{"get":{"operationId":"mid","parameters":[{"$ref":"#/components/parameters/Near"}]}}`,
		`"components":{"parameters":{
			"Id":{"name":"id","in":"path","required":true,"schema":{"type":"string"}},
			"LimitAlias":{"$ref":"#/components/parameters/Limit"},
			"Limit":{"name":"limit","in":"query","description":"target","schema":{"type":"integer"}},
			"Near":{"$ref":"#/components/parameters/Mid"},
			"Mid":{"$ref":"#/components/parameters/Target","description":"mid"},
			"Target":{"name":"t","in":"query","description":"target","schema":{"type":"string"}},
			"a b":{"name":"spaced","in":"query","schema":{"type":"string"}},
			"x/y":{"name":"slashed","in":"query","schema":{"type":"string"}}
		}}`)
	c := parseFor(t, w, doc, nil)
	uri := w.URL + "/openapi.json"
	op := mustOp(t, c, "refs")
	if op.Err != nil {
		t.Fatalf("Err = %v", op.Err)
	}
	want := []struct{ name, in, description, source string }{
		{"id", "path", "", uri + "#/components/parameters/Id"},
		// A chain reaches the target.
		{"limit", "query", "target", uri + "#/components/parameters/Limit"},
		// The use site's description wins over the middle one's and the
		// target's.
		{"t", "query", "near", uri + "#/components/parameters/Target"},
		// load.go, Client.Document: a Source's fragment is percent-encoded as
		// RFC 6901 section 6 says, so the space is %20.
		{"spaced", "query", "", uri + "#/components/parameters/a%20b"},
		{"slashed", "query", "", uri + "#/components/parameters/x~1y"},
	}
	if len(op.Params) != len(want) {
		t.Fatalf("len(Params) = %d, want %d", len(op.Params), len(want))
	}
	for i, p := range op.Params {
		w := want[i]
		if p.Name != w.name || p.In != w.in || p.Description != w.description || p.Source != w.source {
			t.Errorf("Params[%d] = %s %s %q at %q\nwant %s %s %q at %q", i, p.In, p.Name, p.Description, p.Source, w.in, w.name, w.description, w.source)
		}
		if p.Err != nil {
			t.Errorf("Params[%d].Err = %v", i, p.Err)
		}
	}
	// With no description at the use site, the nearest Reference Object that
	// gives one wins.
	if p := mustOp(t, c, "mid").Params; len(p) != 1 || p[0].Description != "mid" {
		t.Errorf("mid Params = %+v, want the description \"mid\"", p)
	}

	mustCall(t, c, "refs", &openapi.Input{Params: map[string]any{"id": "7", "limit": 2, "t": "x", "spaced": "s", "slashed": "l"}}, nil)
	if got := w.only(t).RequestURI; got != "/p/7?limit=2&t=x&spaced=s&slashed=l" {
		t.Errorf("request target %q", got)
	}
}

// describe.go, Operation.Err: "a reference that cannot be followed to a
// parameter, or to the request body of an operation that takes one ..., as
// then the parameter's identity or the body's requiredness cannot be known"
// makes the operation uncallable, and Operation:
// "A part written as a Reference Object that cannot be resolved has that
// Reference Object's location as its Source"; "Wherever an Err's cause is a
// reference that could not be resolved, it wraps ErrUnresolved". Calling it
// "returns a *RequestError wrapping Err" (errors.go, RequestError:
// errors.Is(err, op.Err)).
func TestBrokenParameterAndBodyRefs(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/param":{"get":{"operationId":"param","parameters":[{"$ref":"#/components/parameters/Nope"}]}},
		"/body":{"post":{"operationId":"body","requestBody":{"$ref":"#/components/requestBodies/Nope"}}},
		"/pcycle":{"get":{"operationId":"pcycle","parameters":[{"$ref":"#/components/parameters/A"}]}},
		"/bcycle":{"post":{"operationId":"bcycle","requestBody":{"$ref":"#/components/requestBodies/A"}}}`,
		`"components":{
			"parameters":{"A":{"$ref":"#/components/parameters/B"},"B":{"$ref":"#/components/parameters/A"}},
			"requestBodies":{"A":{"$ref":"#/components/requestBodies/B"},"B":{"$ref":"#/components/requestBodies/A"}}
		}`)
	c := parseFor(t, w, doc, nil)
	uri := w.URL + "/openapi.json"

	param := mustOp(t, c, "param")
	if param.Err == nil || !errors.Is(param.Err, openapi.ErrUnresolved) {
		t.Errorf("param Err = %v, want one wrapping ErrUnresolved", param.Err)
	}
	found := false
	for _, p := range param.Params {
		if p.Source == uri+"#/paths/~1param/get/parameters/0" {
			found = true
		}
	}
	if !found {
		t.Errorf("no Param has the reference's own location as its Source: %+v", param.Params)
	}
	body := mustOp(t, c, "body")
	if body.Err == nil || !errors.Is(body.Err, openapi.ErrUnresolved) {
		t.Errorf("body Err = %v, want one wrapping ErrUnresolved", body.Err)
	}

	// A reference cycle is detected, never followed forever (load.go,
	// Loader), and the operation cannot be called. (Whether a cycle counts as
	// ErrUnresolved is not stated, so it is not checked.)
	for _, key := range []string{"pcycle", "bcycle"} {
		if op := mustOp(t, c, key); op.Err == nil {
			t.Errorf("%s Err = nil, want the cycle reported", key)
		}
	}

	for _, key := range []string{"param", "body", "pcycle", "bcycle"} {
		op := mustOp(t, c, key)
		in := &openapi.Input{}
		if key == "body" || key == "bcycle" {
			in.Body = map[string]any{"a": 1}
			in.MediaType = "application/json"
		}
		resp, err := c.Call(t.Context(), key, in, nil)
		re := asRequestError(t, err)
		if resp != nil || (op.Err != nil && !errors.Is(re, op.Err)) {
			t.Errorf("Call(%q) = %v, %v; want a refusal wrapping %v", key, resp, err, op.Err)
		}
	}
	w.nothingSent(t)
}

// describe.go, Operation: a request body Reference Object that gives a
// description replaces the target's; Source names the target (Message.
// Source), and the target's Media are the body's.
func TestRequestBodyRef(t *testing.T) {
	w := newWire(t, jsonAnswer(201, `{}`))
	doc := doc31(`
		"/a":{"post":{"operationId":"a","requestBody":{"$ref":"#/components/requestBodies/Alias","description":"use site"}}},
		"/b":{"post":{"operationId":"b","requestBody":{"$ref":"#/components/requestBodies/Pet"}}}`,
		`"components":{"requestBodies":{
			"Alias":{"$ref":"#/components/requestBodies/Pet"},
			"Pet":{"description":"target","required":true,"content":{"application/json":{"schema":{"type":"object"}}}}
		}}`)
	c := parseFor(t, w, doc, nil)
	uri := w.URL + "/openapi.json"
	for key, description := range map[string]string{"a": "use site", "b": "target"} {
		op := mustOp(t, c, key)
		b := op.Body
		if op.Err != nil || b == nil {
			t.Fatalf("%s: Err %v, Body %v", key, op.Err, b)
		}
		if b.Description != description || !b.Required || b.Source != uri+"#/components/requestBodies/Pet" {
			t.Errorf("%s Body = %q, required %t, at %q", key, b.Description, b.Required, b.Source)
		}
		if len(b.Media) != 1 || b.Media[0].Source != uri+"#/components/requestBodies/Pet/content/application~1json" {
			t.Errorf("%s Media = %+v", key, b.Media)
		}
		mustCall(t, c, key, &openapi.Input{Body: map[string]any{"name": "Rex"}}, nil)
		if got := w.last(t); string(trimNL(got.Body)) != `{"name":"Rex"}` || got.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s sent %q as %q", key, got.Body, got.Header.Get("Content-Type"))
		}
	}
}

// describe.go, Operation: response Reference Objects are followed, a
// use-site description wins, and Source names the target. Operation.Err:
// "A defect in an optional part is reported on that part instead": a
// response whose reference cannot be resolved has its Message.Err, wrapping
// ErrUnresolved, and the operation stays callable.
func TestResponseRefs(t *testing.T) {
	w := newWire(t, jsonAnswer(200, `{}`))
	doc := doc31(`"/r":{"get":{"operationId":"r","responses":{
			"200":{"$ref":"#/components/responses/OKAlias"},
			"404":{"$ref":"#/components/responses/NotFound","description":"no such thing"},
			"409":{"$ref":"#/components/responses/Nope"},
			"410":{"$ref":"#/components/responses/CycleA"}
		}}}`,
		`"components":{"responses":{
			"OKAlias":{"$ref":"#/components/responses/OK"},
			"OK":{"description":"fine","content":{"application/json":{"schema":{"type":"object"}}}},
			"NotFound":{"description":"target","content":{"application/problem+json":{}}},
			"CycleA":{"$ref":"#/components/responses/CycleB"},
			"CycleB":{"$ref":"#/components/responses/CycleA"}
		}}`)
	c := parseFor(t, w, doc, nil)
	uri := w.URL + "/openapi.json"
	op := mustOp(t, c, "r")
	if op.Err != nil {
		t.Fatalf("Err = %v", op.Err)
	}
	if len(op.Responses) != 4 {
		t.Fatalf("len(Responses) = %d", len(op.Responses))
	}
	ok, nf, broken, cycle := op.Responses[0], op.Responses[1], op.Responses[2], op.Responses[3]
	if ok.Key != "200" || ok.Description != "fine" || ok.Source != uri+"#/components/responses/OK" || ok.Err != nil {
		t.Errorf("200 = %q %q at %q, Err %v", ok.Key, ok.Description, ok.Source, ok.Err)
	}
	if len(ok.Media) != 1 || ok.Media[0].Source != uri+"#/components/responses/OK/content/application~1json" {
		t.Errorf("200 Media = %+v", ok.Media)
	}
	if nf.Key != "404" || nf.Description != "no such thing" || nf.Source != uri+"#/components/responses/NotFound" {
		t.Errorf("404 = %q %q at %q", nf.Key, nf.Description, nf.Source)
	}
	if broken.Key != "409" || broken.Err == nil || !errors.Is(broken.Err, openapi.ErrUnresolved) {
		t.Errorf("409 = %q, Err %v; want an Err wrapping ErrUnresolved", broken.Key, broken.Err)
	}
	if cycle.Key != "410" || cycle.Err == nil {
		t.Errorf("410 = %q, Err %v; want the cycle reported", cycle.Key, cycle.Err)
	}
	var out map[string]any
	resp := mustCall(t, c, "r", nil, &out)
	if resp.Declaration != ok {
		t.Errorf("Declaration = %p, want the 200 Message %p", resp.Declaration, ok)
	}
}

// load.go, Loader.AllowReference: "With nil, http and https references may
// reach the entry document's original origin and Origins", so a reference
// to another origin is not followed, and the part it reaches carries an Err
// wrapping ErrUnresolved (errors.go: a reference that cannot be resolved).
// Nothing is fetched.
func TestReferenceToAnotherDocument(t *testing.T) {
	ct := &countingTransport{}
	doc := bare31(`
		"/p":{"get":{"operationId":"p","parameters":[{"$ref":"https://elsewhere.example.test/params.json#/Limit"}]}},
		"/r":{"get":{"operationId":"r","responses":{"200":{"$ref":"https://elsewhere.example.test/responses.json#/OK"}}}}`)
	c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: ct}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ct.count() != 0 {
		t.Errorf("loading fetched %d documents from another origin", ct.count())
	}
	if op := mustOp(t, c, "p"); op.Err == nil || !errors.Is(op.Err, openapi.ErrUnresolved) {
		t.Errorf("p Err = %v, want one wrapping ErrUnresolved", op.Err)
	}
	op := mustOp(t, c, "r")
	if op.Err != nil {
		t.Errorf("r Err = %v; an unresolved response does not disable the operation", op.Err)
	}
	if len(op.Responses) != 1 || op.Responses[0].Err == nil || !errors.Is(op.Responses[0].Err, openapi.ErrUnresolved) {
		t.Errorf("r Responses = %+v, want one whose Err wraps ErrUnresolved", op.Responses)
	}
}

// doc.go, Raw invocation boundary: "A resolvable operation does not depend on
// the client's ability to interpret its schemas." A broken schema reference
// neither disables the operation nor its parameter.
func TestSchemasNotInterpreted(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/s":{"post":{"operationId":"s",
		"parameters":[{"name":"q","in":"query","schema":{"$ref":"#/components/schemas/Nope"}}],
		"requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Missing"}}}}}}`)
	c := parseFor(t, w, doc, nil)
	op := mustOp(t, c, "s")
	p, m := param(t, op, 0), reqMedia(t, op, 0)
	if op.Err != nil || p.Err != nil || m.Err != nil {
		t.Errorf("a broken schema reference set Err: op %v, param %v, media %v", op.Err, p.Err, m.Err)
	}
	if p.Schema == nil {
		t.Fatal("Schema = nil")
	}
	if got := compact(t, p.Schema.Raw()); got != `{"$ref":"#/components/schemas/Nope"}` {
		t.Errorf("Raw = %s", got)
	}
	mustCall(t, c, "s", &openapi.Input{Params: map[string]any{"q": "v"}, Body: map[string]any{"any": []int{1}}}, nil)
	if got := w.only(t); got.RequestURI != "/s?q=v" || string(trimNL(got.Body)) != `{"any":[1]}` {
		t.Errorf("sent %s with %q", got.RequestURI, got.Body)
	}
}

// describe.go, Operation.Err: "a path template that has an unclosed { or
// names a parameter that no path parameter declares" makes the operation
// uncallable.
func TestPathTemplateWithoutParameter(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/pets/{petId}":{"get":{"operationId":"getPet"}}`), nil)
	op := mustOp(t, c, "getPet")
	if op.Err == nil {
		t.Fatalf("Err = nil for a template with no parameter")
	}
	resp, err := c.Call(t.Context(), "getPet", &openapi.Input{Params: map[string]any{"petId": "p"}}, nil)
	re := refusedBeforeSending(t, w, resp, err)
	if !errors.Is(re, op.Err) {
		t.Errorf("Call error %v does not wrap op.Err", err)
	}
}
