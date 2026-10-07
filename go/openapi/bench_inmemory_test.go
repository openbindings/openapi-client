package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Further benchmarks: an in-memory transport next to the loopback
// benchmarks, so the client's own cost is resolvable; the document tree's
// memory; and performance properties no functional test covers: Load's
// Options checks, Path Item $ref chains, compiling many parameters and
// server variables, array parameter values, server variables given in
// Options, With for one call, a non-JSON body into a *any, and the body
// walk's per-type cache.

// cannedRT answers every request in memory with petJSON (201 for POST),
// reading and closing any request body first.
type cannedRT struct{}

type cannedBody struct{ bytes.Reader }

func (*cannedBody) Close() error { return nil }

func (cannedRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	b := &cannedBody{}
	b.Reset(petJSON)
	code, status := 200, "200 OK"
	if r.Method == "POST" {
		code, status = 201, "201 Created"
	}
	return &http.Response{StatusCode: code, Status: status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: b, ContentLength: int64(len(petJSON)), Request: r}, nil
}

const cannedBase = "https://api.example.test"

func cannedClient(b *testing.B) (*openapi.Client, *http.Client) {
	b.Helper()
	hc := &http.Client{Transport: cannedRT{}}
	c, err := openapi.Parse(context.Background(), []byte(expand(benchDoc, cannedBase)), cannedBase+"/openapi.json",
		&openapi.Options{HTTPClient: hc})
	if err != nil {
		b.Fatal(err)
	}
	return c, hc
}

func callGet(b *testing.B, c *openapi.Client, in *openapi.Input) {
	var pet Pet
	if _, err := c.Call(context.Background(), "getPet", in, &pet); err != nil {
		b.Fatal(err)
	}
}

func handGet(b *testing.B, hc *http.Client, base string) {
	u := base + "/pets/" + url.PathEscape("p-7") + "?revision=" + strconv.Itoa(3)
	req, err := http.NewRequestWithContext(context.Background(), "GET", u, nil)
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

func callPost(b *testing.B, c *openapi.Client, in *openapi.Input) {
	var created Pet
	if _, err := c.Call(context.Background(), "createPet", in, &created); err != nil {
		b.Fatal(err)
	}
}

func handPost(b *testing.B, hc *http.Client, base string) {
	body, err := json.Marshal(Pet{Name: "Rex", Tag: "dog"})
	if err != nil {
		b.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), "POST", base+"/pets", bytes.NewReader(body))
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

var (
	getInput  = &openapi.Input{Params: map[string]any{"petId": "p-7", "revision": 3}}
	postInput = &openapi.Input{Body: Pet{Name: "Rex", Tag: "dog"}}
)

// Call against the same requests written by hand, over an in-memory
// transport, serially and in parallel. These isolate the client's own cost
// from the network; the loopback ones stay the end-to-end check.
func BenchmarkMemCallGetJSON(b *testing.B) {
	c, _ := cannedClient(b)
	b.ReportAllocs()
	for b.Loop() {
		callGet(b, c, getInput)
	}
}

func BenchmarkMemNetHTTPGetJSON(b *testing.B) {
	_, hc := cannedClient(b)
	b.ReportAllocs()
	for b.Loop() {
		handGet(b, hc, cannedBase)
	}
}

func BenchmarkMemCallPostJSON(b *testing.B) {
	c, _ := cannedClient(b)
	b.ReportAllocs()
	for b.Loop() {
		callPost(b, c, postInput)
	}
}

func BenchmarkMemNetHTTPPostJSON(b *testing.B) {
	_, hc := cannedClient(b)
	b.ReportAllocs()
	for b.Loop() {
		handPost(b, hc, cannedBase)
	}
}

func BenchmarkMemParallelCallGetJSON(b *testing.B) {
	c, _ := cannedClient(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			callGet(b, c, getInput)
		}
	})
}

func BenchmarkMemParallelNetHTTPGetJSON(b *testing.B) {
	_, hc := cannedClient(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			handGet(b, hc, cannedBase)
		}
	})
}

func BenchmarkMemParallelCallPostJSON(b *testing.B) {
	c, _ := cannedClient(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			callPost(b, c, postInput)
		}
	})
}

func BenchmarkMemParallelNetHTTPPostJSON(b *testing.B) {
	_, hc := cannedClient(b)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			handPost(b, hc, cannedBase)
		}
	})
}

// reportLoadMemory loads doc once more after the timed loop and reports the
// heap it retains after a GC, and what the load allocated, per document
// byte. TestDocumentTreeMemoryBudget enforces limits on both.
func reportLoadMemory(b *testing.B, doc []byte) {
	b.Helper()
	var before, loaded, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	c, err := openapi.Parse(context.Background(), doc, largeDocURI, nil)
	if err != nil {
		b.Fatal(err)
	}
	runtime.ReadMemStats(&loaded)
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(c)
	size := float64(len(doc))
	b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/size, "retained-B/doc-B")
	b.ReportMetric(float64(loaded.TotalAlloc-before.TotalAlloc)/size, "alloc-B/doc-B")
}

// BenchmarkLoadLargeMemory: the synthetic 2,100-operation document.
func BenchmarkLoadLargeMemory(b *testing.B) {
	doc, _ := largeDoc(700, 500)
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := openapi.Parse(context.Background(), doc, largeDocURI, nil); err != nil {
			b.Fatal(err)
		}
	}
	reportLoadMemory(b, doc)
}

// BenchmarkLoadArrayMemory: a pathological document, an 8 MiB array of
// zeros (one value per two bytes).
func BenchmarkLoadArrayMemory(b *testing.B) {
	const size = 8 << 20
	var buf strings.Builder
	buf.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{},"components":{"x":[0`)
	for buf.Len() < size {
		buf.WriteString(",0")
	}
	buf.WriteString("]}}")
	doc := []byte(buf.String())
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := openapi.Parse(context.Background(), doc, largeDocURI, nil); err != nil {
			b.Fatal(err)
		}
	}
	reportLoadMemory(b, doc)
}

// Load with Options that name servers or media types, against
// BenchmarkLoadLarge.
func BenchmarkLoadLargeWithServer(b *testing.B) {
	benchLoadWith(b, &openapi.Options{Server: "https://api.example.test/v1"})
}

func BenchmarkLoadLargeWithMediaType(b *testing.B) {
	benchLoadWith(b, &openapi.Options{MediaType: "application/json"})
}

func benchLoadWith(b *testing.B, opts *openapi.Options) {
	doc, _ := largeDoc(700, 500)
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := openapi.Parse(context.Background(), doc, largeDocURI, opts); err != nil {
			b.Fatal(err)
		}
	}
}

// A Path Item $ref chain resolved at Load.
func BenchmarkLoadPathItemChain(b *testing.B) {
	var sb strings.Builder
	sb.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/x":{"$ref":"#/components/pathItems/p0"}},"components":{"pathItems":{`)
	const n = 8000
	for i := range n {
		fmt.Fprintf(&sb, `"p%d":{"$ref":"#/components/pathItems/p%d"},`, i, i+1)
	}
	fmt.Fprintf(&sb, `"p%d":{"get":{"operationId":"get"}}}}}`, n)
	doc := []byte(sb.String())
	b.ReportAllocs()
	for b.Loop() {
		if _, err := openapi.Parse(context.Background(), doc, testDocURI, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// benchFirstUse times the first Operation of key on a freshly loaded doc.
func benchFirstUse(b *testing.B, doc []byte, key string) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, err := c.Operation(key); err != nil {
			b.Fatal(err)
		}
	}
}

// Compiling an operation with many parameters, and a server with many
// variables.
func BenchmarkFirstUseManyParams(b *testing.B) {
	var sb strings.Builder
	sb.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://h.example.test"}],"paths":{"/x":{"get":{"operationId":"op","parameters":[`)
	for i := range 2000 {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"name":"q%d","in":"query","schema":{"type":"string"}}`, i)
	}
	sb.WriteString(`]}}}}`)
	benchFirstUse(b, []byte(sb.String()), "op")
}

func BenchmarkFirstUseManyServerVariables(b *testing.B) {
	var u, vars strings.Builder
	u.WriteString("https://h.example.test")
	for i := range 2000 {
		fmt.Fprintf(&u, "/{v%d}", i)
		if i > 0 {
			vars.WriteString(",")
		}
		fmt.Fprintf(&vars, `"v%d":{"default":"x"}`, i)
	}
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"` + u.String() + `","variables":{` + vars.String() + `}}],
		"paths":{"/x":{"get":{"operationId":"op"}}}}`
	benchFirstUse(b, []byte(doc), "op")
}

// An array parameter value against a scalar one.
func BenchmarkPrepareArrayParam(b *testing.B) {
	benchPrepareLarge(b, &openapi.Input{Params: map[string]any{"itemId": "x-1", "limit": 10, "fields": []string{"a", "b"}}})
}

func BenchmarkPrepareScalarParam(b *testing.B) {
	benchPrepareLarge(b, &openapi.Input{Params: map[string]any{"itemId": "x-1", "limit": 10}})
}

func benchPrepareLarge(b *testing.B, in *openapi.Input) {
	doc, _ := largeDoc(10, 10)
	c, err := openapi.Parse(context.Background(), doc, largeDocURI, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Prepare("getItem0", in); err != nil {
			b.Fatal(err)
		}
	}
}

// A server variable given in Options.Variables against its default.
const variableServerDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"servers":[{"url":"https://{region}.api.example.test/v1","variables":{"region":{"default":"us","enum":["us","eu"]}}}],
	"paths":{"/pets/{petId}":{"get":{"operationId":"getPet","parameters":[{"name":"petId","in":"path","required":true,"schema":{"type":"string"}}]}}}}`

func benchVariables(b *testing.B, opts *openapi.Options) {
	c, err := openapi.Parse(context.Background(), []byte(variableServerDoc), testDocURI, opts)
	if err != nil {
		b.Fatal(err)
	}
	in := &openapi.Input{Params: map[string]any{"petId": "p-7"}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Prepare("getPet", in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrepareServerDefault(b *testing.B) { benchVariables(b, nil) }

func BenchmarkPrepareServerVariable(b *testing.B) {
	benchVariables(b, &openapi.Options{Variables: map[string]string{"region": "eu"}})
}

// With for one call.
func BenchmarkWith(b *testing.B) {
	c, _ := cannedClient(b)
	b.ReportAllocs()
	for b.Loop() {
		d := c.With(func(o *openapi.Options) { o.Header.Set("X-Tenant", "t-1") })
		runtime.KeepAlive(d)
	}
}

// octetsRT answers with 64 KiB of application/octet-stream.
type octetsRT struct{ data []byte }

func (rt octetsRT) RoundTrip(r *http.Request) (*http.Response, error) {
	b := &cannedBody{}
	b.Reset(rt.data)
	return &http.Response{StatusCode: 200, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/octet-stream"}}, Body: b, ContentLength: int64(len(rt.data)), Request: r}, nil
}

// A non-JSON body into a *any keeps the buffer it read (one body-sized
// allocation, not two).
func BenchmarkMemCallAnyOctets(b *testing.B) {
	rt := octetsRT{data: bytes.Repeat([]byte{7}, 64<<10)}
	c, err := openapi.Parse(context.Background(), []byte(expand(benchDoc, cannedBase)), cannedBase+"/openapi.json",
		&openapi.Options{HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(rt.data)))
	b.ReportAllocs()
	for b.Loop() {
		var v any
		if _, err := c.Call(context.Background(), "getPet", getInput, &v); err != nil {
			b.Fatal(err)
		}
	}
}

// Preparing a large JSON body against json.Marshal alone; the reader walk's
// per-type cache keeps the difference small.
var (
	bigPets = func() []Pet {
		s := make([]Pet, 2000)
		for i := range s {
			s[i] = Pet{ID: "p-" + strconv.Itoa(i), Name: "Rex", Tag: "dog"}
		}
		return s
	}()
	bigAny = func() []any {
		s := make([]any, 2000)
		for i := range s {
			s[i] = map[string]any{"id": "p-" + strconv.Itoa(i), "name": "Rex", "tag": "dog"}
		}
		return s
	}()
)

func benchPrepareBody(b *testing.B, body any) {
	c, _ := cannedClient(b)
	in := &openapi.Input{Body: body}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Prepare("createPet", in); err != nil {
			b.Fatal(err)
		}
	}
}

func benchMarshal(b *testing.B, body any) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrepareBigBody(b *testing.B)    { benchPrepareBody(b, bigPets) }
func BenchmarkMarshalBigBody(b *testing.B)    { benchMarshal(b, bigPets) }
func BenchmarkPrepareBigAnyBody(b *testing.B) { benchPrepareBody(b, bigAny) }
func BenchmarkMarshalBigAnyBody(b *testing.B) { benchMarshal(b, bigAny) }
