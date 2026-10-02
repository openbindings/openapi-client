package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Body benchmarks (stage 4 brief, Tests: "Benchmarks against the same
// request built by hand: a form body with several fields, a multipart body
// with a file part and fields, a JSON Lines body from a slice, each with
// latency and allocations"; Budgets: "body benchmarks within 15 percent of
// hand-written in memory, allocations reported"), over the in-memory
// transport of bench_review_test.go (cannedRT, which reads and discards the
// request body). Each BenchmarkMemBodyX has a BenchmarkMemBodyXHand
// counterpart; TestBodyBenchRequestsMatch checks that the two send the same
// body. Run with -benchmem.

const bodyBenchDoc = `{"openapi":"3.1.0","info":{"title":"bench","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{
	"/pets/form":{"post":{"operationId":"formPet","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{
		"name":{"type":"string"},"tag":{"type":"string"},"age":{"type":"integer"},"vaccinated":{"type":"boolean"},"notes":{"type":"string"}}}}}}}},
	"/documents":{"post":{"operationId":"upload","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"title":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"file":{}}},
		"encoding":{"file":{"contentType":"application/pdf"}}}}}}},
	"/pets/import":{"post":{"operationId":"importPets","requestBody":{"content":{"application/jsonl":{}}}}}
}}`

// benchForm is a form body; a struct keeps its fields in order.
type benchForm struct {
	Name       string `json:"name"`
	Tag        string `json:"tag"`
	Age        int    `json:"age"`
	Vaccinated bool   `json:"vaccinated"`
	Notes      string `json:"notes"`
}

var (
	benchFormValue = benchForm{"Rex", "good dog", 3, true, "likes walks & treats"}
	benchFile      = bytes.Repeat([]byte("%PDF-1.7 0123456789abcdef"), 640) // 16 KiB
	benchPets      = manyPets(100)
)

// bodyBenches are the client's inputs and the hand-written body for each.
var bodyBenches = []struct {
	name string
	key  string
	in   func() *openapi.Input
	hand func() (ctype string, body []byte) // the body built by hand
}{
	{"Form", "formPet", func() *openapi.Input { return &openapi.Input{Body: benchFormValue} }, func() (string, []byte) {
		f := benchFormValue
		var b strings.Builder
		b.WriteString("name=")
		b.WriteString(url.QueryEscape(f.Name))
		b.WriteString("&tag=")
		b.WriteString(url.QueryEscape(f.Tag))
		b.WriteString("&age=")
		b.WriteString(strconv.Itoa(f.Age))
		b.WriteString("&vaccinated=")
		b.WriteString(strconv.FormatBool(f.Vaccinated))
		b.WriteString("&notes=")
		b.WriteString(url.QueryEscape(f.Notes))
		return "application/x-www-form-urlencoded", []byte(b.String())
	}},
	{"Multipart", "upload", func() *openapi.Input {
		return &openapi.Input{Body: map[string]any{
			"title": "Q3 report",
			"tags":  []string{"finance", "q3"},
			"file":  openapi.Part{Content: benchFile, MediaType: "application/pdf", Filename: "q3.pdf"},
		}}
	}, func() (string, []byte) {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		part := func(disposition, ctype string, content []byte) {
			h := textproto.MIMEHeader{"Content-Disposition": {disposition}, "Content-Type": {ctype}}
			p, _ := w.CreatePart(h)
			p.Write(content)
		}
		part(`form-data; name="file"; filename="q3.pdf"`, "application/pdf", benchFile)
		part(`form-data; name="tags"`, "text/plain", []byte("finance"))
		part(`form-data; name="tags"`, "text/plain", []byte("q3"))
		part(`form-data; name="title"`, "text/plain", []byte("Q3 report"))
		w.Close()
		return w.FormDataContentType(), b.Bytes()
	}},
	{"JSONLines", "importPets", func() *openapi.Input { return &openapi.Input{Body: benchPets} }, func() (string, []byte) {
		var b bytes.Buffer
		for _, p := range benchPets {
			j, _ := json.Marshal(p)
			b.Write(j)
			b.WriteByte('\n')
		}
		return "application/jsonl", b.Bytes()
	}},
}

func bodyBenchClient(tb testing.TB) (*openapi.Client, *http.Client) {
	tb.Helper()
	hc := &http.Client{Transport: cannedRT{}}
	c, err := openapi.Parse(context.Background(), []byte(expand(bodyBenchDoc, cannedBase)), cannedBase+"/openapi.json",
		&openapi.Options{HTTPClient: hc})
	if err != nil {
		tb.Fatal(err)
	}
	return c, hc
}

func benchBodyCall(b *testing.B, i int) {
	c, _ := bodyBenchClient(b)
	bb := bodyBenches[i]
	in := bb.in()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Call(ctx, bb.key, in, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// benchBodyHand builds the body by hand each time, as a caller of net/http
// would, and sends it.
func benchBodyHand(b *testing.B, i int) {
	_, hc := bodyBenchClient(b)
	bb := bodyBenches[i]
	ctx := context.Background()
	path := map[string]string{"formPet": "/pets/form", "upload": "/documents", "importPets": "/pets/import"}[bb.key]
	b.ReportAllocs()
	for b.Loop() {
		ct, body := bb.hand()
		req, err := http.NewRequestWithContext(ctx, "POST", cannedBase+path, bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Content-Type", ct)
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

func BenchmarkMemBodyForm(b *testing.B)          { benchBodyCall(b, 0) }
func BenchmarkMemBodyFormHand(b *testing.B)      { benchBodyHand(b, 0) }
func BenchmarkMemBodyMultipart(b *testing.B)     { benchBodyCall(b, 1) }
func BenchmarkMemBodyMultipartHand(b *testing.B) { benchBodyHand(b, 1) }
func BenchmarkMemBodyJSONLines(b *testing.B)     { benchBodyCall(b, 2) }
func BenchmarkMemBodyJSONLinesHand(b *testing.B) { benchBodyHand(b, 2) }

// Each benchmark's client request carries the body its hand-written
// counterpart builds: the form and the JSON Lines body byte for byte, the
// multipart body part by part (its boundary is random on either side).
func TestBodyBenchRequestsMatch(t *testing.T) {
	c, _ := bodyBenchClient(t)
	for _, bb := range bodyBenches {
		t.Run(bb.name, func(t *testing.T) {
			req, err := c.Prepare(bb.key, bb.in())
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			got, gotCT := preparedBody(t, req), req.HTTP.Header.Get("Content-Type")
			wantCT, want := bb.hand()
			switch bb.name {
			case "Multipart":
				_, _, gp := readMultipart(t, gotCT, got)
				_, _, wp := readMultipart(t, wantCT, want)
				var wantParts []wantPart
				for _, p := range wp {
					wantParts = append(wantParts, wantPart{disposition: p.header.Get("Content-Disposition"), ctype: p.header.Get("Content-Type"), content: string(p.body)})
				}
				checkParts(t, gp, wantParts)
			case "JSONLines":
				if gotCT != wantCT {
					t.Errorf("Content-Type %q, hand-written %q", gotCT, wantCT)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("client sends %q, hand-written %q", got, want)
				}
			default:
				if gotCT != wantCT || !bytes.Equal(got, want) {
					t.Errorf("client sends %q as %q, hand-written %q as %q", got, gotCT, want, wantCT)
				}
			}
		})
	}
}
