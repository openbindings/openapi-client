package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Benchmarks (stage brief, Tests; dev loop, Performance): Call against the
// same requests hand-written with net/http on the same loopback server,
// load of a large synthetic 3.1 document, and first-call preparation after
// load. Run with -benchmem.

const benchDoc = `{"openapi":"3.1.0","info":{"title":"bench","version":"1"},"servers":[{"url":"@BASE@"}],
	"paths":{
		"/pets/{petId}":{"get":{"operationId":"getPet","parameters":[
			{"name":"petId","in":"path","required":true,"schema":{"type":"string"}},
			{"name":"revision","in":"query","schema":{"type":"integer"}}],
			"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Pet"}}}}}}},
		"/pets":{"post":{"operationId":"createPet",
			"requestBody":{"required":true,"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Pet"}}}},
			"responses":{"201":{"description":"created","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Pet"}}}}}}}
	},
	"components":{"schemas":{"Pet":{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string"},"tag":{"type":"string"}}}}}}`

var petJSON = []byte(`{"id":"p-7","name":"Rex","tag":"dog"}`)

// benchServer answers GET /pets/{id} and POST /pets as the API in benchDoc.
func benchServer(b *testing.B) *httptest.Server {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(201)
		}
		w.Write(petJSON)
	}))
	b.Cleanup(srv.Close)
	return srv
}

func benchClient(b *testing.B, srv *httptest.Server) *openapi.Client {
	b.Helper()
	c, err := openapi.Parse(context.Background(), []byte(expand(benchDoc, srv.URL)), srv.URL+"/openapi.json",
		&openapi.Options{HTTPClient: srv.Client()})
	if err != nil {
		b.Fatal(err)
	}
	return c
}

func BenchmarkCallGetJSON(b *testing.B) {
	srv := benchServer(b)
	c := benchClient(b, srv)
	ctx := context.Background()
	in := &openapi.Input{Params: map[string]any{"petId": "p-7", "revision": 3}}
	b.ReportAllocs()
	for b.Loop() {
		var pet Pet
		if _, err := c.Call(ctx, "getPet", in, &pet); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNetHTTPGetJSON is BenchmarkCallGetJSON written by hand.
func BenchmarkNetHTTPGetJSON(b *testing.B) {
	srv := benchServer(b)
	hc := srv.Client()
	ctx := context.Background()
	petID, revision := "p-7", 3
	b.ReportAllocs()
	for b.Loop() {
		u := srv.URL + "/pets/" + url.PathEscape(petID) + "?revision=" + strconv.Itoa(revision)
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			b.Fatal(err)
		}
		resp, err := hc.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		var pet Pet
		if resp.StatusCode/100 != 2 {
			b.Fatal(resp.Status)
		}
		if err := json.NewDecoder(resp.Body).Decode(&pet); err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func BenchmarkCallPostJSON(b *testing.B) {
	srv := benchServer(b)
	c := benchClient(b, srv)
	ctx := context.Background()
	in := &openapi.Input{Body: Pet{Name: "Rex", Tag: "dog"}}
	b.ReportAllocs()
	for b.Loop() {
		var created Pet
		if _, err := c.Call(ctx, "createPet", in, &created); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNetHTTPPostJSON is BenchmarkCallPostJSON written by hand.
func BenchmarkNetHTTPPostJSON(b *testing.B) {
	srv := benchServer(b)
	hc := srv.Client()
	ctx := context.Background()
	pet := Pet{Name: "Rex", Tag: "dog"}
	b.ReportAllocs()
	for b.Loop() {
		body, err := json.Marshal(pet)
		if err != nil {
			b.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/pets", bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		var created Pet
		if resp.StatusCode/100 != 2 {
			b.Fatal(resp.Status)
		}
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// largeDocURI names the synthetic document.
const largeDocURI = "https://api.example.test/openapi.json"

// largeCall is one operation of the synthetic document with inputs for it.
type largeCall struct {
	key string
	in  *openapi.Input
}

// largeDoc generates an OpenAPI 3.1 document with paths Path Items of three
// operations each (get, put and delete), with path-level and operation
// parameters, request bodies, responses, local references of every kind
// stage 1 follows, and schemas component schemas that reference one
// another. It returns the document and a call for every operation.
func largeDoc(paths, schemas int) ([]byte, []largeCall) {
	var b strings.Builder
	var calls []largeCall
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"Large","version":"1.0.0"},"servers":[{"url":"https://api.example.test/v1","description":"production"}],"paths":{`)
	for i := range paths {
		if i > 0 {
			b.WriteString(",")
		}
		s := i % schemas
		fmt.Fprintf(&b, `"/r%d/items/{itemId}":{"parameters":[{"$ref":"#/components/parameters/ItemId"}],`, i)
		fmt.Fprintf(&b, `"get":{"operationId":"getItem%d","tags":["r%d"],"summary":"Get item %d","parameters":[`, i, i%10, i)
		b.WriteString(`{"name":"limit","in":"query","schema":{"type":"integer","minimum":1,"maximum":100}},`)
		b.WriteString(`{"name":"fields","in":"query","explode":false,"schema":{"type":"array","items":{"type":"string"}}},`)
		b.WriteString(`{"name":"X-Trace","in":"header","schema":{"type":"string"}}],"responses":{`)
		fmt.Fprintf(&b, `"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/S%d"}}}},`, s)
		b.WriteString(`"404":{"$ref":"#/components/responses/NotFound"},"default":{"$ref":"#/components/responses/Error"}}},`)
		fmt.Fprintf(&b, `"put":{"operationId":"putItem%d","requestBody":{"$ref":"#/components/requestBodies/B%d"},"responses":{`, i, s)
		fmt.Fprintf(&b, `"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/S%d"}}}},`, s)
		b.WriteString(`"422":{"$ref":"#/components/responses/Error"}}},`)
		fmt.Fprintf(&b, `"delete":{"operationId":"deleteItem%d","responses":{"204":{"description":"deleted"},"default":{"$ref":"#/components/responses/Error"}}}}`, i)
		calls = append(calls,
			largeCall{fmt.Sprintf("getItem%d", i), &openapi.Input{Params: map[string]any{"itemId": "x-1", "limit": 10, "fields": []string{"a", "b"}}}},
			largeCall{fmt.Sprintf("putItem%d", i), &openapi.Input{Params: map[string]any{"itemId": "x-1"}, Body: map[string]any{"id": "x-1", "name": "n"}}},
			largeCall{fmt.Sprintf("deleteItem%d", i), &openapi.Input{Params: map[string]any{"itemId": "x-1"}}},
		)
	}
	b.WriteString(`},"components":{"parameters":{"ItemId":{"name":"itemId","in":"path","required":true,"schema":{"type":"string","pattern":"^[a-z0-9-]+$"}}},"schemas":{`)
	for j := range schemas {
		if j > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"S%d":{"type":"object","required":["id","name"],"properties":{"id":{"type":"string"},"name":{"type":"string","maxLength":64},`+
			`"count":{"type":"integer","minimum":0},"price":{"type":"number","multipleOf":0.01},"tags":{"type":"array","items":{"type":"string"}},`+
			`"next":{"$ref":"#/components/schemas/S%d"},"meta":{"type":"object","additionalProperties":{"type":"string"}}}}`, j, (j+1)%schemas)
	}
	b.WriteString(`,"Problem":{"type":"object","properties":{"title":{"type":"string"},"detail":{"type":"string"}}}},"requestBodies":{`)
	for j := range schemas {
		if j > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"B%d":{"required":true,"content":{"application/json":{"schema":{"$ref":"#/components/schemas/S%d"}}}}`, j, j)
	}
	b.WriteString(`},"responses":{"NotFound":{"description":"not found","content":{"application/problem+json":{"schema":{"$ref":"#/components/schemas/Problem"}}}},` +
		`"Error":{"description":"error","content":{"application/problem+json":{"schema":{"$ref":"#/components/schemas/Problem"}}}}}}}`)
	return []byte(b.String()), calls
}

// The synthetic document is a valid 3.1 document of at least 2,000
// operations, each callable, with the parts the benchmarks mean to measure.
func TestLargeDocumentGenerator(t *testing.T) {
	doc, calls := largeDoc(700, 500)
	if !json.Valid(doc) {
		t.Fatal("the generated document is not JSON")
	}
	c, err := openapi.Parse(t.Context(), doc, largeDocURI, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ops := c.Operations()
	if len(ops) < 2000 || len(ops) != len(calls) {
		t.Fatalf("%d operations, want %d and at least 2,000", len(ops), len(calls))
	}
	var params, bodies, responses int
	for _, op := range ops {
		if op.Err != nil {
			t.Fatalf("%s: %v", op.Key, op.Err)
		}
		if len(op.Params) > 0 {
			params++
		}
		if op.Body != nil {
			bodies++
		}
		if len(op.Responses) > 0 {
			responses++
		}
	}
	if params != len(ops) || bodies != len(ops)/3 || responses != len(ops) {
		t.Errorf("operations with params %d, bodies %d, responses %d of %d", params, bodies, responses, len(ops))
	}
	if c.Document(largeDocURI+"#/components/schemas/S499") == nil {
		t.Errorf("component schemas missing")
	}
	for _, i := range []int{0, 1, 2, len(calls) - 1} {
		if _, err := c.Prepare(calls[i].key, calls[i].in); err != nil {
			t.Errorf("Prepare(%s): %v", calls[i].key, err)
		}
	}
}

// BenchmarkLoadLarge reports the time and memory to load the synthetic
// document from bytes.
func BenchmarkLoadLarge(b *testing.B) {
	doc, calls := largeDoc(700, 500)
	b.SetBytes(int64(len(doc)))
	b.ReportMetric(float64(len(calls)), "operations")
	b.ReportAllocs()
	ctx := context.Background()
	for b.Loop() {
		if _, err := openapi.Parse(ctx, doc, largeDocURI, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLoadLargeFile loads the synthetic document from a file.
func BenchmarkLoadLargeFile(b *testing.B) {
	doc, _ := largeDoc(700, 500)
	path := filepath.Join(b.TempDir(), "large.json")
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	ctx := context.Background()
	for b.Loop() {
		if _, err := openapi.Load(ctx, path, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPrepareFirstLarge reports the cost of each operation's first
// preparation after load: every Prepare here is the first for its
// operation on a freshly loaded Client.
func BenchmarkPrepareFirstLarge(b *testing.B) {
	doc, calls := largeDoc(700, 500)
	ctx := context.Background()
	var c *openapi.Client
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%len(calls) == 0 {
			b.StopTimer()
			var err error
			if c, err = openapi.Parse(ctx, doc, largeDocURI, nil); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
		}
		call := calls[i%len(calls)]
		if _, err := c.Prepare(call.key, call.in); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPrepareWarmLarge reports a repeated preparation of one operation
// of the synthetic document.
func BenchmarkPrepareWarmLarge(b *testing.B) {
	doc, calls := largeDoc(700, 500)
	c, err := openapi.Parse(context.Background(), doc, largeDocURI, nil)
	if err != nil {
		b.Fatal(err)
	}
	call := calls[1] // putItem0: path parameter and a JSON body
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Prepare(call.key, call.in); err != nil {
			b.Fatal(err)
		}
	}
}
