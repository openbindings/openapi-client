package openapi_test

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// countedJSON is a value whose MarshalJSON returns data, counting its calls
// in n.
type countedJSON struct {
	n    *atomic.Int32
	data string
}

func (v countedJSON) MarshalJSON() ([]byte, error) {
	v.n.Add(1)
	return []byte(v.data), nil
}

// countedReader is an io.Reader with its own MarshalJSON, which counts its
// calls.
type countedReader struct {
	countedJSON
	r *strings.Reader
}

func (v countedReader) Read(p []byte) (int, error) { return v.r.Read(p) }

// Regression check, not contract: doc.go, Values: "The client first converts
// a value to JSON data as encoding/json would (struct tags, MarshalJSON,
// TextMarshaler map keys), then serializes that data as the document says".
// Each value is converted to JSON data once, so preparing a call and reading
// its body runs a value's MarshalJSON at most once: a form field, a
// multipart/form-data field, an OpenAPI 3.2 positional multipart/mixed part,
// a querystring parameter's application/x-www-form-urlencoded value and a
// member of it, and a JSON Lines item. The value's data is checked as sent,
// so it ran at least once. A reader with its own MarshalJSON given as a
// positional part is sent as its bytes, as TestMarshalingReaderIsAReader
// says, and its MarshalJSON runs at most once too.
func TestMarshalJSONRunsOncePerSend(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `
		"/f":{"post":{"requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"f":{"type":"string"}}}}}}}},
		"/m":{"post":{"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"f":{"type":"string"}}}}}}}},
		"/p":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array","items":{"type":"string"}}}}}}},
		"/q":{"get":{"parameters":[{"name":"qs","in":"querystring","content":{"application/x-www-form-urlencoded":{
			"schema":{"type":"object","properties":{"a":{"type":"string"}}}}}}]}},
		"/s":{"post":{"requestBody":{"content":{"application/jsonl":{}}}}}`), nil)
	for _, tc := range []struct {
		name, key, data string
		in              func(v any) *openapi.Input
		query, body     string // what is sent
	}{
		{"form field", "POST /f", `"v"`, func(v any) *openapi.Input {
			return &openapi.Input{Body: map[string]any{"f": v}}
		}, "", "f=v"},
		{"multipart field", "POST /m", `"v"`, func(v any) *openapi.Input {
			return &openapi.Input{MediaType: "multipart/form-data; boundary=B", Body: map[string]any{"f": v}}
		}, "", "--B\r\nContent-Disposition: form-data; name=\"f\"\r\nContent-Type: text/plain\r\n\r\nv\r\n--B--\r\n"},
		{"positional part", "POST /p", `"v"`, func(v any) *openapi.Input {
			return &openapi.Input{MediaType: "multipart/mixed; boundary=B", Body: []any{v}}
		}, "", "--B\r\nContent-Type: text/plain\r\n\r\nv\r\n--B--\r\n"},
		{"querystring value", "GET /q", `{"a":"v"}`, func(v any) *openapi.Input {
			return &openapi.Input{Params: map[string]any{"qs": v}}
		}, "a=v", ""},
		{"querystring member", "GET /q", `"v"`, func(v any) *openapi.Input {
			return &openapi.Input{Params: map[string]any{"qs": map[string]any{"a": v}}}
		}, "a=v", ""},
		{"JSON Lines item", "POST /s", `"v"`, func(v any) *openapi.Input {
			return &openapi.Input{Body: []any{v}}
		}, "", "\"v\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			req, err := c.Prepare(tc.key, tc.in(countedJSON{&n, tc.data}))
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if query, body := req.HTTP.URL.RawQuery, string(editionBody(t, req)); query != tc.query || body != tc.body {
				t.Errorf("query %q, body %q; want %q, %q", query, body, tc.query, tc.body)
			}
			if got := n.Load(); got > 1 {
				t.Errorf("MarshalJSON ran %d times; want at most once", got)
			}
		})
	}
	t.Run("positional reader", func(t *testing.T) {
		var n atomic.Int32
		r := countedReader{countedJSON{&n, `"v"`}, strings.NewReader("raw")}
		req, err := c.Prepare("POST /p", &openapi.Input{MediaType: "multipart/mixed; boundary=B", Body: []any{r}})
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		if body, want := string(editionBody(t, req)), "--B\r\nContent-Type: text/plain\r\n\r\nraw\r\n--B--\r\n"; body != want {
			t.Errorf("body %q, want %q", body, want)
		}
		if got := n.Load(); got > 1 {
			t.Errorf("MarshalJSON ran %d times; want at most once", got)
		}
	})
}

// htmlItems returns a json.RawMessage array of n strings, each holding "<",
// ">" and "&" when html is set, and as many other characters otherwise.
func htmlItems(n int, html bool) json.RawMessage {
	mark := "axbc"
	if html {
		mark = "<x>&"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"` + mark + strconv.Itoa(i) + `"`)
	}
	b.WriteByte(']')
	return json.RawMessage(b.String())
}

// Regression check, not contract: doc.go, Values: "The client first converts
// a value to JSON data as encoding/json would ..., then serializes that data
// as the document says". The characters encoding/json escapes for HTML, "<",
// ">" and "&", are data like any other, so a json.RawMessage array of 1,000
// strings holding them costs no more to send than one of strings without
// them: as a JSON Lines body, as a multipart/form-data property, one part per
// item, and as an OpenAPI 3.2 positional multipart/mixed body, preparing the
// call and reading its body allocates at most 1.5 times as often.
func TestHTMLCharactersInJSONDataCostNoMore(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `
		"/s":{"post":{"requestBody":{"content":{"application/jsonl":{}}}}},
		"/m":{"post":{"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"l":{"type":"array","items":{"type":"string"}}}}}}}}},
		"/p":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array","items":{"type":"string"}}}}}}}`), nil)
	for _, tc := range []struct {
		name, key, media string
		body             func(json.RawMessage) any
	}{
		{"JSON Lines body", "POST /s", "", func(r json.RawMessage) any { return r }},
		{"multipart property", "POST /m", "multipart/form-data; boundary=B", func(r json.RawMessage) any { return map[string]any{"l": r} }},
		{"positional body", "POST /p", "multipart/mixed; boundary=B", func(r json.RawMessage) any { return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var allocs [2]float64
			for i, html := range []bool{false, true} {
				in := &openapi.Input{MediaType: tc.media, Body: tc.body(htmlItems(1000, html))}
				allocs[i] = testing.AllocsPerRun(5, func() {
					req, err := c.Prepare(tc.key, in)
					if err != nil {
						t.Fatalf("refused: %v", err)
					}
					if _, err := io.Copy(io.Discard, req.HTTP.Body); err != nil {
						t.Fatalf("read body: %v", err)
					}
				})
			}
			if allocs[1] > 1.5*allocs[0] {
				t.Errorf("%.0f allocations with \"<x>&\" items, %.0f without; want at most 1.5 times as many", allocs[1], allocs[0])
			}
		})
	}
}

// Regression check, not contract: doc.go, Values: "The client first converts
// a value to JSON data as encoding/json would ..., then serializes that data
// as the document says". A JSON Lines body given a json.RawMessage array of
// 10,000 strings takes each item's JSON text from the array, which costs
// three allocations an item, and writes that text as it is, which costs none,
// so preparing the call and reading its body allocates at most three times
// per item, plus a hundred for the call.
func TestJSONLinesFromRawArrayAllocations(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `"/s":{"post":{"requestBody":{"content":{"application/jsonl":{}}}}}`), nil)
	const n = 10000
	var b strings.Builder
	b.WriteByte('[')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"item-` + strconv.Itoa(i) + `"`)
	}
	b.WriteByte(']')
	in := &openapi.Input{Body: json.RawMessage(b.String())}
	allocs := testing.AllocsPerRun(3, func() {
		req, err := c.Prepare("POST /s", in)
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		if _, err := io.Copy(io.Discard, req.HTTP.Body); err != nil {
			t.Fatalf("read body: %v", err)
		}
	})
	if limit := float64(3*n + 100); allocs > limit {
		t.Errorf("%.0f allocations for %d items; want at most %.0f", allocs, n, limit)
	}
}
