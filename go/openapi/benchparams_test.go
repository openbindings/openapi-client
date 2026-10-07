package openapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Parameter benchmarks: a query with several form parameters, an exploded
// object, a deepObject, a matrix path, a JSON content parameter, each against
// the same request built by hand with net/url and strings.Builder, over the
// in-memory transport of bench_inmemory_test.go (cannedRT), so the client's own
// cost is resolvable. Each BenchmarkMemParamX has a BenchmarkMemParamXHand
// counterpart; TestParamBenchRequestsMatch checks that the two send the same
// request. Run with -benchmem.

const paramBenchDoc = `{"openapi":"3.1.0","info":{"title":"bench","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{
	"/search":{"get":{"operationId":"search","parameters":[
		{"name":"q","in":"query","schema":{"type":"string"}},
		{"name":"limit","in":"query","schema":{"type":"integer"}},
		{"name":"tag","in":"query","schema":{"type":"array","items":{"type":"string"}}},
		{"name":"fields","in":"query","explode":false,"schema":{"type":"array","items":{"type":"string"}}}]}},
	"/filter":{"get":{"operationId":"filter","parameters":[
		{"name":"filter","in":"query","schema":{"type":"object","additionalProperties":{"type":"string"}}}]}},
	"/deep":{"get":{"operationId":"deep","parameters":[
		{"name":"filter","in":"query","style":"deepObject","schema":{"type":"object","additionalProperties":{"type":"string"}}}]}},
	"/items{id}":{"get":{"operationId":"matrix","parameters":[
		{"name":"id","in":"path","required":true,"style":"matrix","schema":{"type":"array","items":{"type":"string"}}}]}},
	"/json":{"get":{"operationId":"json","parameters":[
		{"name":"q","in":"query","content":{"application/json":{"schema":{"type":"object"}}}}]}}
}}`

// benchFilter is an object parameter; a struct keeps its members in order.
type benchFilter struct {
	Color string `json:"color"`
	Size  string `json:"size"`
	Sort  string `json:"sort"`
}

var paramBenches = []struct {
	name string
	key  string
	in   *openapi.Input
	hand func(b *strings.Builder, base string) // writes the request URL by hand
}{
	{"FormQuery", "search", &openapi.Input{Params: map[string]any{
		"q": "shoes", "limit": 20, "tag": []string{"red", "sale"}, "fields": []string{"id", "name", "price"},
	}}, func(b *strings.Builder, base string) {
		b.WriteString(base)
		b.WriteString("/search?q=")
		b.WriteString(url.QueryEscape("shoes"))
		b.WriteString("&limit=")
		b.WriteString(strconv.Itoa(20))
		for _, t := range []string{"red", "sale"} {
			b.WriteString("&tag=")
			b.WriteString(url.QueryEscape(t))
		}
		b.WriteString("&fields=")
		for i, f := range []string{"id", "name", "price"} {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(url.QueryEscape(f))
		}
	}},
	{"ExplodedObject", "filter", &openapi.Input{Params: map[string]any{
		"filter": benchFilter{"red", "L", "name"},
	}}, func(b *strings.Builder, base string) {
		b.WriteString(base)
		b.WriteString("/filter?")
		f := benchFilter{"red", "L", "name"}
		for i, kv := range [][2]string{{"color", f.Color}, {"size", f.Size}, {"sort", f.Sort}} {
			if i > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(kv[0]))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(kv[1]))
		}
	}},
	{"DeepObject", "deep", &openapi.Input{Params: map[string]any{
		"filter": benchFilter{"red", "L", "name"},
	}}, func(b *strings.Builder, base string) {
		b.WriteString(base)
		b.WriteString("/deep?")
		f := benchFilter{"red", "L", "name"}
		for i, kv := range [][2]string{{"color", f.Color}, {"size", f.Size}, {"sort", f.Sort}} {
			if i > 0 {
				b.WriteByte('&')
			}
			b.WriteString("filter%5B")
			b.WriteString(url.QueryEscape(kv[0]))
			b.WriteString("%5D=")
			b.WriteString(url.QueryEscape(kv[1]))
		}
	}},
	{"MatrixPath", "matrix", &openapi.Input{Params: map[string]any{
		"id": []string{"a1", "b2", "c3"},
	}}, func(b *strings.Builder, base string) {
		b.WriteString(base)
		b.WriteString("/items;id=")
		for i, id := range []string{"a1", "b2", "c3"} {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(url.PathEscape(id))
		}
	}},
	{"JSONContent", "json", &openapi.Input{Params: map[string]any{
		"q": map[string]any{"a": 1, "b": "x"},
	}}, func(b *strings.Builder, base string) {
		b.WriteString(base)
		b.WriteString("/json?q=")
		j, _ := json.Marshal(map[string]any{"a": 1, "b": "x"})
		b.WriteString(url.QueryEscape(string(j)))
	}},
}

func paramBenchClient(tb testing.TB) (*openapi.Client, *http.Client) {
	tb.Helper()
	hc := &http.Client{Transport: cannedRT{}}
	c, err := openapi.Parse(context.Background(), []byte(expand(paramBenchDoc, cannedBase)), cannedBase+"/openapi.json",
		&openapi.Options{HTTPClient: hc})
	if err != nil {
		tb.Fatal(err)
	}
	return c, hc
}

func benchParamCall(b *testing.B, i int) {
	c, _ := paramBenchClient(b)
	pb := paramBenches[i]
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Call(ctx, pb.key, pb.in, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func benchParamHand(b *testing.B, i int) {
	_, hc := paramBenchClient(b)
	pb := paramBenches[i]
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		var sb strings.Builder
		pb.hand(&sb, cannedBase)
		req, err := http.NewRequestWithContext(ctx, "GET", sb.String(), nil)
		if err != nil {
			b.Fatal(err)
		}
		resp, err := hc.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		if resp.StatusCode/100 != 2 {
			b.Fatal(resp.Status)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func BenchmarkMemParamFormQuery(b *testing.B)          { benchParamCall(b, 0) }
func BenchmarkMemParamFormQueryHand(b *testing.B)      { benchParamHand(b, 0) }
func BenchmarkMemParamExplodedObject(b *testing.B)     { benchParamCall(b, 1) }
func BenchmarkMemParamExplodedObjectHand(b *testing.B) { benchParamHand(b, 1) }
func BenchmarkMemParamDeepObject(b *testing.B)         { benchParamCall(b, 2) }
func BenchmarkMemParamDeepObjectHand(b *testing.B)     { benchParamHand(b, 2) }
func BenchmarkMemParamMatrixPath(b *testing.B)         { benchParamCall(b, 3) }
func BenchmarkMemParamMatrixPathHand(b *testing.B)     { benchParamHand(b, 3) }
func BenchmarkMemParamJSONContent(b *testing.B)        { benchParamCall(b, 4) }
func BenchmarkMemParamJSONContentHand(b *testing.B)    { benchParamHand(b, 4) }

// Each benchmark's client request is the request its hand-written
// counterpart builds: the same request target.
func TestParamBenchRequestsMatch(t *testing.T) {
	c, _ := paramBenchClient(t)
	for _, pb := range paramBenches {
		t.Run(pb.name, func(t *testing.T) {
			req, err := c.Prepare(pb.key, pb.in)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			var sb strings.Builder
			pb.hand(&sb, cannedBase)
			hand, err := http.NewRequest("GET", sb.String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := req.HTTP.URL.RequestURI(), hand.URL.RequestURI(); got != want {
				t.Errorf("client request target %q, hand-written %q", got, want)
			}
		})
	}
}
