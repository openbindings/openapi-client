package openapi_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Descriptions: Client.Operations, Client.Operation and the descriptors.

func opKeys(ops []*openapi.Operation) []string {
	keys := make([]string, len(ops))
	for i, op := range ops {
		keys[i] = op.Key
	}
	return keys
}

// describe.go, Client.Operations: "in document order: paths as listed,
// within a path the methods in the order get, put, post, delete, options,
// head, patch, trace, query". In OpenAPI 3.1 a Path Item has only the
// eight fixed methods (OAS 3.1.2 section 4.8.9.1), so a "query" member is
// not an operation.
func TestOperationsOrder(t *testing.T) {
	doc := bare31(`"/zeta":{"patch":{},"get":{},"trace":{},"query":{},"delete":{},"post":{},"x-ext":{},"put":{},"options":{},"head":{}},
		"/alpha":{"get":{}}`)
	c := parseAt(t, doc, "", testDocURI, nil)
	want := []string{"GET /zeta", "PUT /zeta", "POST /zeta", "DELETE /zeta", "OPTIONS /zeta", "HEAD /zeta", "PATCH /zeta", "TRACE /zeta", "GET /alpha"}
	ops := c.Operations()
	if got := opKeys(ops); !slices.Equal(got, want) {
		t.Fatalf("Operations keys = %q, want %q", got, want)
	}
	for _, op := range ops {
		if op.Method+" "+op.Path != op.Key {
			t.Errorf("%s: Method %q, Path %q", op.Key, op.Method, op.Path)
		}
		if op.Err != nil {
			t.Errorf("%s: Err = %v", op.Key, op.Err)
		}
	}
	if _, err := c.Operation("QUERY /zeta"); !errors.Is(err, openapi.ErrNoOperation) {
		t.Errorf("Operation(\"QUERY /zeta\") error = %v, want ErrNoOperation", err)
	}
}

// describe.go, Client.Operations: "The slice is new on each call".
func TestOperationsNewSlice(t *testing.T) {
	c := parseAt(t, bare31(`"/a":{"get":{}},"/b":{"get":{}}`), "", testDocURI, nil)
	ops := c.Operations()
	if len(ops) != 2 {
		t.Fatalf("Operations keys = %q", opKeys(ops))
	}
	ops[0], ops[1] = ops[1], nil
	if got := opKeys(c.Operations()); !slices.Equal(got, []string{"GET /a", "GET /b"}) {
		t.Errorf("Operations keys = %q after changing an earlier slice", got)
	}
}

// describe.go, Operation.Key and Client.Operation; client.go, Client.Call:
// an operation is named by its operationId when that names it alone and is
// not of the method-and-path form, else by its method and Paths key; a key
// of the method-and-path form always means a method and a Paths key.
func TestOperationKeys(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/pets":{"get":{"operationId":"listPets"},"post":{"operationId":"dup"}},
		"/other":{"get":{"operationId":"dup"},"put":{},"post":{"operationId":"GET /pets"}}`)
	c := parseFor(t, w, doc, nil)

	type row struct{ key, id, method, path string }
	want := []row{
		{"listPets", "listPets", "GET", "/pets"},
		// Duplicate operationIds (OAS 3.1.2: the id MUST be unique): each is
		// reached by its method and path.
		{"POST /pets", "dup", "POST", "/pets"},
		{"GET /other", "dup", "GET", "/other"},
		{"PUT /other", "", "PUT", "/other"},
		// An operationId of the method-and-path form is not a Key.
		{"POST /other", "GET /pets", "POST", "/other"},
	}
	ops := c.Operations()
	if len(ops) != len(want) {
		t.Fatalf("Operations keys = %q", opKeys(ops))
	}
	for i, w := range want {
		op := ops[i]
		if got := (row{op.Key, op.ID, op.Method, op.Path}); got != w {
			t.Errorf("operation %d = %+v, want %+v", i, got, w)
		}
	}

	// Every key reaches the operation it names.
	for key, wantKey := range map[string]string{
		"listPets":    "listPets",
		"GET /pets":   "listPets",
		"POST /pets":  "POST /pets",
		"GET /other":  "GET /other",
		"PUT /other":  "PUT /other",
		"POST /other": "POST /other",
	} {
		if op := mustOp(t, c, key); op.Key != wantKey {
			t.Errorf("Operation(%q).Key = %q, want %q", key, op.Key, wantKey)
		}
	}

	// describe.go, Client.Operation: no operation, or a shared operationId,
	// is an error wrapping ErrNoOperation.
	for _, key := range []string{
		"dup",          // shared by two operations
		"nope",         // no such operationId
		"get /pets",    // client.go, Call: "a fixed method in uppercase"
		"GET  /pets",   // "separated by one space"
		"GET /pets/",   // the Paths key as written
		"DELETE /pets", // no such method on the path
		"",
	} {
		op, err := c.Operation(key)
		if !errors.Is(err, openapi.ErrNoOperation) {
			t.Errorf("Operation(%q) = %v, %v; want an error wrapping ErrNoOperation", key, op, err)
		}
		// errors.go, ErrNoOperation: "Both Client.Operation and a refused
		// call return it".
		resp, err := c.Call(t.Context(), key, nil, nil)
		re := refusedBeforeSending(t, w, resp, err)
		if !errors.Is(re, openapi.ErrNoOperation) {
			t.Errorf("Call(%q) error %v does not wrap ErrNoOperation", key, err)
		}
		if _, err := c.Prepare(key, nil); !errors.Is(err, openapi.ErrNoOperation) {
			t.Errorf("Prepare(%q) error %v does not wrap ErrNoOperation", key, err)
		}
	}

	// A method-and-path key sends that method to that path.
	mustCall(t, c, "GET /pets", nil, nil)
	if got := w.only(t); got.Method != "GET" || got.RequestURI != "/pets" {
		t.Errorf("sent %s %s, want GET /pets", got.Method, got.RequestURI)
	}
}

// client.go, Client: "The zero Client has no operations: it describes
// nothing and refuses every call with a *RequestError wrapping
// ErrNoOperation." client.go, Request: "A Request that Prepare did not
// make, the zero Request included, is refused with a *RequestError
// wrapping ErrNoOperation."
func TestZeroClientAndRequest(t *testing.T) {
	var zero openapi.Client
	if n := len(zero.Operations()); n != 0 {
		t.Errorf("zero Client has %d operations", n)
	}
	if _, err := zero.Operation("getPet"); !errors.Is(err, openapi.ErrNoOperation) {
		t.Errorf("zero Client Operation error = %v", err)
	}
	resp, err := zero.Call(t.Context(), "getPet", nil, nil)
	re := refusedBeforeSending(t, nil, resp, err)
	if !errors.Is(re, openapi.ErrNoOperation) {
		t.Errorf("zero Client Call error %v does not wrap ErrNoOperation", err)
	}
	if req, err := zero.Prepare("getPet", nil); req != nil || !errors.Is(err, openapi.ErrNoOperation) {
		t.Errorf("zero Client Prepare = %v, %v", req, err)
	}

	w := newWire(t, nil)
	hr, err := http.NewRequest("GET", w.URL+"/pets", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]*openapi.Request{
		"zero":           {},
		"not by Prepare": {HTTP: hr},
	} {
		resp, err := r.Send(t.Context())
		re := refusedBeforeSending(t, w, resp, err)
		if !errors.Is(re, openapi.ErrNoOperation) {
			t.Errorf("%s Request Send error %v does not wrap ErrNoOperation", name, err)
		}
		resp, err = r.Call(t.Context(), nil)
		re = refusedBeforeSending(t, w, resp, err)
		if !errors.Is(re, openapi.ErrNoOperation) {
			t.Errorf("%s Request Call error %v does not wrap ErrNoOperation", name, err)
		}
	}
}

// describe.go, Operation and Param: the fields of one operation and its
// parameters, path-level parameters merged in the order the package
// documentation gives (doc.go, Fixed rules, Order), the ignored header
// parameters left out (Operation.Params; OAS 3.1.2 section 4.8.12.2), and
// style and explode defaulted by location (OAS 3.1.2 section 4.8.12.2.2).
func TestOperationDescriptor(t *testing.T) {
	uri := "https://pets.example.test/openapi.json"
	doc := bare31(`"/pets/{petId}":{
		"summary":"path summary",
		"parameters":[
			{"name":"petId","in":"path","required":true,"description":"The pet","schema":{"type":"string"}},
			{"name":"trace","in":"header","deprecated":true,"schema":{"type":"string"}}
		],
		"get":{
			"operationId":"getPet","summary":"Get a pet","description":"Returns one pet.",
			"tags":["pets","read"],"deprecated":true,
			"parameters":[
				{"name":"fields","in":"query","explode":false,"allowReserved":true,"schema":{"type":"array","items":{"type":"string"}}},
				{"name":"trace","in":"header","description":"overridden","schema":{"type":"integer"}},
				{"name":"Accept","in":"header","schema":{"type":"string"}},
				{"name":"content-type","in":"header","schema":{"type":"string"}},
				{"name":"AUTHORIZATION","in":"header","schema":{"type":"string"}},
				{"name":"verbose","in":"query","style":"form","explode":true,"schema":{"type":"boolean"}}
			]
		}
	}`)
	c := parseAt(t, doc, "", uri, nil)
	op := mustOp(t, c, "getPet")

	if op.Key != "getPet" || op.ID != "getPet" || op.Method != "GET" || op.Path != "/pets/{petId}" {
		t.Errorf("Key, ID, Method, Path = %q, %q, %q, %q", op.Key, op.ID, op.Method, op.Path)
	}
	if op.Summary != "Get a pet" || op.Description != "Returns one pet." {
		t.Errorf("Summary, Description = %q, %q", op.Summary, op.Description)
	}
	if !slices.Equal(op.Tags, []string{"pets", "read"}) || !op.Deprecated {
		t.Errorf("Tags, Deprecated = %q, %t", op.Tags, op.Deprecated)
	}
	if want := uri + "#/paths/~1pets~1%7BpetId%7D/get"; op.Source != want {
		t.Errorf("Source = %q, want %q", op.Source, want)
	}
	if op.Err != nil {
		t.Errorf("Err = %v", op.Err)
	}
	if op.Body != nil {
		t.Errorf("Body = %+v, want nil", op.Body)
	}

	type param struct {
		Key, Name, In, Description string
		Required, Deprecated       bool
		Style                      string
		Explode, ExplodeSet        bool
		AllowReserved              bool
		Source                     string
	}
	base := uri + "#/paths/~1pets~1%7BpetId%7D"
	want := []param{
		{"petId", "petId", "path", "The pet", true, false, "simple", false, false, false, base + "/parameters/0"},
		// The operation's trace overrides the path item's, in its place.
		{"trace", "trace", "header", "overridden", false, false, "simple", false, false, false, base + "/get/parameters/1"},
		{"fields", "fields", "query", "", false, false, "form", false, true, true, base + "/get/parameters/0"},
		{"verbose", "verbose", "query", "", false, false, "form", true, true, false, base + "/get/parameters/5"},
	}
	if len(op.Params) != len(want) {
		names := []string{}
		for _, p := range op.Params {
			names = append(names, p.In+" "+p.Name)
		}
		t.Fatalf("Params = %q, want %d", names, len(want))
	}
	for i, p := range op.Params {
		got := param{p.Key, p.Name, p.In, p.Description, p.Required, p.Deprecated, p.Style, p.Explode, p.ExplodeSet, p.AllowReserved, p.Source}
		if got != want[i] {
			t.Errorf("Params[%d] = %+v\nwant %+v", i, got, want[i])
		}
		if p.Err != nil {
			t.Errorf("Params[%d].Err = %v", i, p.Err)
		}
		if p.Schema == nil {
			t.Errorf("Params[%d].Schema = nil", i)
		}
	}
	// describe.go, Param.Schema and Schema.Raw: the declared schema, exactly
	// as written.
	if s := op.Params[0].Schema; s != nil {
		if got := compact(t, s.Raw()); got != `{"type":"string"}` {
			t.Errorf("petId Schema.Raw = %s", got)
		}
		if want := base + "/parameters/0/schema"; s.Source() != want {
			t.Errorf("petId Schema.Source = %q, want %q", s.Source(), want)
		}
	}
}

// describe.go, Param.AllowReserved: "the effective allowReserved: false
// where the edition or the media type ignores it"; doc.go, Fixed rules, Percent-encoding:
// in 3.1 it applies to query parameters only (OAS 3.1.2 section
// 4.8.12.2.2: "This field only applies to parameters with an in value of
// query").
func TestParamAllowReservedEffective(t *testing.T) {
	doc := bare31(`"/r/{p}":{"get":{"operationId":"r","parameters":[
		{"name":"p","in":"path","required":true,"allowReserved":true,"schema":{"type":"string"}},
		{"name":"h","in":"header","allowReserved":true,"schema":{"type":"string"}},
		{"name":"q","in":"query","allowReserved":true,"schema":{"type":"string"}},
		{"name":"d","in":"query","schema":{"type":"string"}}
	]}}`)
	op := mustOp(t, parseAt(t, doc, "", testDocURI, nil), "r")
	want := map[string]bool{"p": false, "h": false, "q": true, "d": false}
	for _, p := range op.Params {
		if p.AllowReserved != want[p.Name] {
			t.Errorf("%s %s AllowReserved = %t, want %t", p.In, p.Name, p.AllowReserved, want[p.Name])
		}
	}
}

// client.go, Input.Params and describe.go, Param.Key: "A parameter's key is
// its name, unless another parameter Operation.Params lists has the same
// name, or the name is empty, begins with "/" or "Input.Body", or begins
// with a location and a dot ... then its key is its location, a dot and its
// name". Parameters are given by those keys, and a key the operation does
// not declare refuses the call.
func TestParamKeys(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/k":{"get":{"operationId":"k","parameters":[
		{"name":"id","in":"query","schema":{"type":"string"}},
		{"name":"id","in":"header","schema":{"type":"string"}},
		{"name":"/photo","in":"query","schema":{"type":"string"}},
		{"name":"Input.Body","in":"query","schema":{"type":"string"}},
		{"name":"Input.Bodyguard","in":"query","schema":{"type":"string"}},
		{"name":"query.x","in":"header","schema":{"type":"string"}},
		{"name":"path.y","in":"query","schema":{"type":"string"}},
		{"name":"cookie.w","in":"query","schema":{"type":"string"}},
		{"name":"header.z","in":"header","schema":{"type":"string"}},
		{"name":"","in":"query","schema":{"type":"string"}},
		{"name":"plain","in":"query","schema":{"type":"string"}}
	]}}`)
	c := parseFor(t, w, doc, nil)
	want := []string{"query.id", "header.id", "query./photo", "query.Input.Body", "query.Input.Bodyguard",
		"header.query.x", "query.path.y", "query.cookie.w", "header.header.z", "query.", "plain"}
	op := mustOp(t, c, "k")
	var got []string
	for _, p := range op.Params {
		got = append(got, p.Key)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Param keys = %q, want %q", got, want)
	}

	mustCall(t, c, "k", &openapi.Input{Params: map[string]any{
		"query.id": "1", "header.id": "2", "query./photo": "p", "header.query.x": "x", "plain": "v",
	}}, nil)
	sent := w.only(t)
	// doc.go, Fixed rules, Percent-encoding: names encode every byte outside
	// the unreserved set.
	if sent.RequestURI != "/k?id=1&%2Fphoto=p&plain=v" {
		t.Errorf("request target %q, want /k?id=1&%%2Fphoto=p&plain=v", sent.RequestURI)
	}
	if sent.Header.Get("Id") != "2" || sent.Header.Get("Query.x") != "x" {
		t.Errorf("header Id %q, Query.x %q", sent.Header.Get("Id"), sent.Header.Get("Query.x"))
	}

	// The unqualified name of a qualified parameter is not its key.
	resp, err := c.Call(t.Context(), "k", &openapi.Input{Params: map[string]any{"id": "1"}}, nil)
	re := asRequestError(t, err)
	if resp != nil || w.count() != 1 {
		t.Errorf("an undeclared key was sent")
	}
	wantKeys(t, "Inputs", re.Inputs, true, "id")
}

// describe.go, Message and Media: a request body's fields, and
// Media.Sequential for "a sequential or multipart type (see Values in the
// package documentation), or a range that covers only such types".
func TestRequestBodyDescriptor(t *testing.T) {
	uri := "https://pets.example.test/openapi.json"
	doc := bare31(`"/pets":{"post":{"operationId":"createPet","requestBody":{"description":"A pet","required":true,"content":{
		"application/json":{"schema":{"$ref":"#/components/schemas/Pet"}},
		"application/jsonl":{"schema":{"type":"object"}},
		"multipart/form-data":{},
		"text/event-stream":{},
		"application/geo+json-seq":{},
		"application/x-ndjson":{},
		"application/json-seq":{},
		"multipart/*":{},
		"application/*":{},
		"*/*":{},
		"text/plain":{}
	}}}}`, `"components":{"schemas":{"Pet":{"type":"object"}}}`)
	op := mustOp(t, parseAt(t, doc, "", uri, nil), "createPet")
	b := op.Body
	if b == nil {
		t.Fatal("Body = nil")
	}
	body := uri + "#/paths/~1pets/post/requestBody"
	if b.Key != "" || b.Description != "A pet" || !b.Required || b.Source != body || b.Err != nil {
		t.Errorf("Body Key %q, Description %q, Required %t, Source %q, Err %v", b.Key, b.Description, b.Required, b.Source, b.Err)
	}
	want := []struct {
		typ        string
		sequential bool
		pointer    string
	}{
		{"application/json", false, "application~1json"},
		{"application/jsonl", true, "application~1jsonl"},
		{"multipart/form-data", true, "multipart~1form-data"},
		{"text/event-stream", true, "text~1event-stream"},
		{"application/geo+json-seq", true, "application~1geo+json-seq"},
		{"application/x-ndjson", true, "application~1x-ndjson"},
		{"application/json-seq", true, "application~1json-seq"},
		{"multipart/*", true, "multipart~1*"},
		{"application/*", false, "application~1*"},
		{"*/*", false, "*~1*"},
		{"text/plain", false, "text~1plain"},
	}
	if len(b.Media) != len(want) {
		t.Fatalf("len(Media) = %d, want %d", len(b.Media), len(want))
	}
	for i, m := range b.Media {
		w := want[i]
		if m.Type != w.typ || m.Sequential != w.sequential {
			t.Errorf("Media[%d] Type %q Sequential %t, want %q %t", i, m.Type, m.Sequential, w.typ, w.sequential)
		}
		if src := body + "/content/" + w.pointer; m.Source != src {
			t.Errorf("Media[%d].Source = %q, want %q", i, m.Source, src)
		}
		if m.Err != nil {
			t.Errorf("Media[%d].Err = %v", i, m.Err)
		}
	}
	// describe.go, Schema: in OpenAPI 3.1 a handle "stays at that site,
	// whether or not a $ref there has siblings".
	s := b.Media[0].Schema
	if s == nil {
		t.Fatal("Media[0].Schema = nil")
	}
	if got := compact(t, s.Raw()); got != `{"$ref":"#/components/schemas/Pet"}` {
		t.Errorf("Schema.Raw = %s", got)
	}
	if want := body + "/content/application~1json/schema"; s.Source() != want {
		t.Errorf("Schema.Source = %q, want %q", s.Source(), want)
	}
	// describe.go, Media.Schema: "nil when none is declared".
	if b.Media[2].Schema != nil {
		t.Errorf("multipart/form-data Schema = %v, want nil", b.Media[2].Schema)
	}
}

// describe.go, Operation.Responses and Message: declared responses in
// document order; Message.Err is set "for a response whose key is not a
// status code, a range such as "4XX", or "default"", and client.go,
// Response.Declaration: a lowercase range "is reported on its Message".
func TestResponsesDescriptor(t *testing.T) {
	uri := "https://pets.example.test/openapi.json"
	doc := bare31(`"/r":{"get":{"operationId":"r","responses":{
		"200":{"description":"OK","content":{"application/json":{"schema":{"type":"object"}}}},
		"4XX":{"description":"Client error"},
		"2xx":{"description":"lowercase"},
		"abc":{"description":"not a status"},
		"default":{"description":"Other"}
	}}}`)
	op := mustOp(t, parseAt(t, doc, "", uri, nil), "r")
	want := []struct {
		key, description string
		err              bool
	}{
		{"200", "OK", false},
		{"4XX", "Client error", false},
		{"2xx", "lowercase", true},
		{"abc", "not a status", true},
		{"default", "Other", false},
	}
	if len(op.Responses) != len(want) {
		t.Fatalf("len(Responses) = %d, want %d", len(op.Responses), len(want))
	}
	for i, r := range op.Responses {
		w := want[i]
		if r.Key != w.key || r.Description != w.description || (r.Err != nil) != w.err {
			t.Errorf("Responses[%d] = %q %q Err %v; want %q %q Err set %t", i, r.Key, r.Description, r.Err, w.key, w.description, w.err)
		}
		if src := uri + "#/paths/~1r/get/responses/" + w.key; r.Source != src {
			t.Errorf("Responses[%d].Source = %q, want %q", i, r.Source, src)
		}
	}
	if op.Err != nil {
		t.Errorf("a bad response key set the operation's Err: %v", op.Err)
	}
	ok := op.Responses[0]
	if len(ok.Media) != 1 || ok.Media[0].Type != "application/json" || ok.Media[0].Sequential {
		t.Fatalf("200 Media = %+v", ok.Media)
	}
	if src := uri + "#/paths/~1r/get/responses/200/content/application~1json"; ok.Media[0].Source != src {
		t.Errorf("200 Media Source = %q, want %q", ok.Media[0].Source, src)
	}
	if len(op.Responses[1].Media) != 0 {
		t.Errorf("4XX declares no content but has Media")
	}
}

// describe.go, Operation.Servers, Server and Variable: effective servers
// with inheritance (OAS 3.1.2 section 4.8.9.1: path-level and
// operation-level servers override), stable IDs for an inherited
// declaration, distinct IDs for distinct declarations, and variables in the
// order they appear in the URL.
func TestServersDescriptor(t *testing.T) {
	uri := "https://pets.example.test/openapi.json"
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"servers":[
		{"url":"https://{region}.api.example.test/{version}","description":"Main","variables":{
			"version":{"default":"v1"},
			"unused":{"default":"x"},
			"region":{"default":"us","enum":["us","eu"],"description":"Region"}}},
		{"url":"https://api.example.test/{tenant}"},
		{"url":"https://api.example.test/{tenant}"}
	],
	"paths":{
		"/a":{"get":{"operationId":"a"}},
		"/b":{"get":{"operationId":"b"}},
		"/c":{"get":{"operationId":"c","servers":[{"url":"https://c.example.test"}]}},
		"/d":{"servers":[{"url":"https://d.example.test"}],"get":{"operationId":"d"},"put":{"operationId":"dput","servers":[]}},
		"/e":{"servers":[],"get":{"operationId":"e"}}
	}}`
	c := parseAt(t, doc, "", uri, nil)
	a, b := mustOp(t, c, "a"), mustOp(t, c, "b")
	if len(a.Servers) != 3 {
		t.Fatalf("a has %d servers, want the root's 3", len(a.Servers))
	}
	s := a.Servers[0]
	if s.URL != "https://{region}.api.example.test/{version}" || s.Description != "Main" || s.Name != "" || s.Err != nil {
		t.Errorf("server 0 = URL %q Description %q Name %q Err %v", s.URL, s.Description, s.Name, s.Err)
	}
	if s.Source != uri+"#/servers/0" {
		t.Errorf("server 0 Source = %q", s.Source)
	}
	if len(s.Variables) != 2 {
		t.Fatalf("server 0 Variables = %+v, want region and version", s.Variables)
	}
	region, version := s.Variables[0], s.Variables[1]
	if region.Name != "region" || !region.Declared || region.Default != "us" || !region.DefaultSet ||
		!slices.Equal(region.Enum, []string{"us", "eu"}) || region.Description != "Region" {
		t.Errorf("region = %+v", region)
	}
	if version.Name != "version" || !version.Declared || version.Default != "v1" || !version.DefaultSet || version.Enum != nil {
		t.Errorf("version = %+v", version)
	}
	// describe.go, Variable.Declared: "a {name} in the URL that it does not
	// declare only needs a value in Options.Variables".
	if v := a.Servers[1].Variables; len(v) != 1 || v[0].Name != "tenant" || v[0].Declared || v[0].DefaultSet {
		t.Errorf("server 1 Variables = %+v, want one undeclared tenant", v)
	}

	// describe.go, Server.ID: "unique among distinct server declarations ...
	// including entries with the same URL and name. An inherited declaration
	// keeps its ID across operations."
	ids := map[string]bool{}
	for i, s := range a.Servers {
		if s.ID == "" || ids[s.ID] {
			t.Errorf("server %d ID %q is empty or repeated", i, s.ID)
		}
		ids[s.ID] = true
		if bs := server(t, b, i); bs.ID != s.ID {
			t.Errorf("server %d has ID %q in a and %q in b", i, s.ID, bs.ID)
		}
	}

	tests := []struct {
		key, url, source string
	}{
		{"c", "https://c.example.test", uri + "#/paths/~1c/get/servers/0"},
		{"d", "https://d.example.test", uri + "#/paths/~1d/servers/0"},
		// doc.go, Fixed rules, URL: "An empty servers array on a path item
		// or operation ... [is] read as absent".
		{"dput", "https://d.example.test", uri + "#/paths/~1d/servers/0"},
		{"e", "https://{region}.api.example.test/{version}", uri + "#/servers/0"},
	}
	for _, tt := range tests {
		op := mustOp(t, c, tt.key)
		if len(op.Servers) == 0 || op.Servers[0].URL != tt.url || op.Servers[0].Source != tt.source {
			t.Errorf("%s Servers = %+v, want first %q at %q", tt.key, op.Servers, tt.url, tt.source)
		}
	}
	if n := len(mustOp(t, c, "c").Servers); n != 1 {
		t.Errorf("c has %d servers, want 1", n)
	}
	if n := len(mustOp(t, c, "e").Servers); n != 3 {
		t.Errorf("e has %d servers, want the root's 3", n)
	}
}

// describe.go, Server.Err and doc.go, Fixed rules, URL: "After substitution,
// a server URL that url.Parse refuses ... cannot be used either, nor can one
// whose host is empty, with or without a port, whose port is above 65535, or
// that has userinfo, a query or a fragment (Server.Err, where the document
// alone decides it)".
func TestServerErrUnusableURL(t *testing.T) {
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[
		{"url":"https://api.example.test/v1"},
		{"url":"https://user@api.example.test/v1"},
		{"url":"https://api.example.test/v1?x=1"},
		{"url":"https://api.example.test/v1#top"}
	],"paths":{"/a":{"get":{"operationId":"a"}}}}`
	op := mustOp(t, parseAt(t, doc, "", testDocURI, nil), "a")
	if len(op.Servers) != 4 {
		t.Fatalf("len(Servers) = %d", len(op.Servers))
	}
	for i, s := range op.Servers {
		if (s.Err != nil) != (i > 0) {
			t.Errorf("server %q Err = %v", s.URL, s.Err)
		}
	}
	if op.Err != nil {
		t.Errorf("an unusable server set the operation's Err: %v", op.Err)
	}
}

// describe.go, SecurityRequirement.Key: the canonical JSON form (sorted
// scheme names and scopes by UTF-8 bytes, duplicates removed, strings
// escaped as RFC 8785 escapes them, {} for the anonymous alternative), and
// Operation.Security with root inheritance (OAS 3.1.2 section 4.8.10.1:
// an operation's security overrides the root's; [] removes it).
func TestSecurityDescriptor(t *testing.T) {
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"security":[{"api_key":[]}],
	"paths":{
		"/a":{"get":{"operationId":"a"}},
		"/b":{"get":{"operationId":"b","security":[]}},
		"/c":{"get":{"operationId":"c","security":[{"oauth":["write","read"],"api_key":[]},{}]}},
		"/d":{"get":{"operationId":"d","security":[{"z\"q\\\t\u0001<&>\u007f\u2028\u00e9":["\u00e9","a","B","a","l\b\f\n\r","x\u001f"]}]}},
		"/e":{"get":{"operationId":"e","security":[{"b":[],"C":["s"]}]}}
	},
	"components":{"securitySchemes":{
		"api_key":{"type":"apiKey","in":"header","name":"X-API-Key"},
		"oauth":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{"read":"","write":""}}}},
		"z\"q\\\t\u0001<&>\u007f\u2028\u00e9":{"type":"apiKey","in":"query","name":"z"},
		"b":{"type":"apiKey","in":"query","name":"b"},
		"C":{"type":"apiKey","in":"query","name":"c"}
	}}}`
	c := parseAt(t, doc, "", testDocURI, nil)

	keys := func(op *openapi.Operation) []string {
		var k []string
		for _, s := range op.Security {
			k = append(k, s.Key)
		}
		return k
	}
	tests := []struct {
		op   string
		want []string
	}{
		{"a", []string{`{"api_key":[]}`}},
		{"b", nil},
		{"c", []string{`{"api_key":[],"oauth":["read","write"]}`, `{}`}},
		// " and \ escaped, U+0009 as \t, U+0001 as \u0001; <, &, >, U+007F,
		// U+2028 and é as themselves; scopes sorted by bytes (B < a < l < x <
		// é) with the repeated "a" removed; \b \f \n \r short escapes and
		// U+001F as \u001f with lowercase hex.
		{"d", []string{`{"z\"q\\\t\u0001<&>` + "\u007f\u2028\u00e9" + `":["B","a","l\b\f\n\r","x\u001f","é"]}`}},
		// "C" (0x43) sorts before "b" (0x62).
		{"e", []string{`{"C":["s"],"b":[]}`}},
	}
	for _, tt := range tests {
		if got := keys(mustOp(t, c, tt.op)); !slices.Equal(got, tt.want) {
			t.Errorf("%s Security keys = %q\nwant %q", tt.op, got, tt.want)
		}
	}

	// describe.go, SecurityRequirement.Schemes: "in document order, each
	// with the scopes or roles the alternative requires of it. It is empty
	// for the anonymous alternative {}."
	sec := mustOp(t, c, "c").Security
	if len(sec) == 2 {
		s := sec[0].Schemes
		if len(s) != 2 || s[0].Name != "oauth" || !slices.Equal(s[0].Scopes, []string{"write", "read"}) ||
			s[1].Name != "api_key" || len(s[1].Scopes) != 0 {
			t.Errorf("c alternative 0 Schemes = %+v", s)
		}
		if len(sec[1].Schemes) != 0 {
			t.Errorf("anonymous alternative Schemes = %+v", sec[1].Schemes)
		}
	}
	if s := mustOp(t, c, "a").Security; len(s) == 1 && (len(s[0].Schemes) != 1 || s[0].Schemes[0].Name != "api_key") {
		t.Errorf("a Schemes = %+v", s[0].Schemes)
	}
}

// describe.go, Schema.Raw, Source, Base and Dialect for OpenAPI 3.1: Raw
// exactly as written; Base from the nearest $id, resolved against the base
// outside it; Dialect from $schema at a schema resource root, else
// jsonSchemaDialect, else OpenAPI's default dialect; in a resource that
// declares another dialect, "identifiers are not interpreted, ... Base is the
// base outside the resource".
func TestSchemaHandles(t *testing.T) {
	const uri = "https://pets.example.test/specs/openapi.json"
	const oasDialect = "https://spec.openapis.org/oas/3.1/dialect/base"
	schemas := []struct {
		name, schema    string
		base, dialect   string
		dialectDocument string // jsonSchemaDialect, or ""
	}{
		// Numbers keep their spelling; no $id: the document's URI.
		{"plain", `{"type":"number","minimum":1.50,"x-order":{"b":1,"a":2}}`, uri, oasDialect, ""},
		// A $ref with a sibling stays as written.
		{"ref", `{"$ref":"#/components/schemas/Pet","description":"sibling"}`, uri, oasDialect, ""},
		{"absolute id", `{"$id":"https://schemas.example.test/pet","type":"object"}`, "https://schemas.example.test/pet", oasDialect, ""},
		// RFC 3986 section 5.2 against the document's URI.
		{"relative id", `{"$id":"../schemas/pet.json","type":"object"}`, "https://pets.example.test/schemas/pet.json", oasDialect, ""},
		{"2020-12", `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://schemas.example.test/a","type":"object"}`,
			"https://schemas.example.test/a", "https://json-schema.org/draft/2020-12/schema", ""},
		{"other dialect", `{"$schema":"https://example.test/custom-dialect","$id":"https://schemas.example.test/b","type":"object"}`,
			uri, "https://example.test/custom-dialect", ""},
		{"document dialect", `{"type":"object"}`, uri, "https://json-schema.org/draft/2020-12/schema", "https://json-schema.org/draft/2020-12/schema"},
	}
	for _, tt := range schemas {
		t.Run(tt.name, func(t *testing.T) {
			extra := []string{`"components":{"schemas":{"Pet":{"type":"object"}}}`}
			if tt.dialectDocument != "" {
				extra = append(extra, `"jsonSchemaDialect":"`+tt.dialectDocument+`"`)
			}
			doc := bare31(`"/s":{"post":{"operationId":"s","requestBody":{"content":{"application/json":{"schema":`+tt.schema+`}}}}}`, extra...)
			op := mustOp(t, parseAt(t, doc, "", uri, nil), "s")
			if op.Body == nil || len(op.Body.Media) != 1 || op.Body.Media[0].Schema == nil {
				t.Fatalf("no schema handle")
			}
			s := op.Body.Media[0].Schema
			if got := compact(t, s.Raw()); got != compact(t, []byte(tt.schema)) {
				t.Errorf("Raw = %s, want %s", got, tt.schema)
			}
			if want := uri + "#/paths/~1s/post/requestBody/content/application~1json/schema"; s.Source() != want {
				t.Errorf("Source = %q, want %q", s.Source(), want)
			}
			if s.Base() != tt.base {
				t.Errorf("Base = %q, want %q", s.Base(), tt.base)
			}
			if s.Dialect() != tt.dialect {
				t.Errorf("Dialect = %q, want %q", s.Dialect(), tt.dialect)
			}
		})
	}
}

// describe.go, Schema.Raw: "a copy". Changing it changes nothing.
func TestSchemaRawIsACopy(t *testing.T) {
	doc := bare31(`"/s":{"get":{"operationId":"s","parameters":[{"name":"q","in":"query","schema":{"type":"string"}}]}}`)
	op := mustOp(t, parseAt(t, doc, "", testDocURI, nil), "s")
	p := param(t, op, 0)
	if p.Schema == nil {
		t.Fatal("Schema = nil")
	}
	raw := p.Schema.Raw()
	if len(raw) > 0 {
		raw[0] = 'X'
	}
	if got := compact(t, p.Schema.Raw()); got != `{"type":"string"}` {
		t.Errorf("Raw = %s after changing an earlier copy", got)
	}
}
