package openapi_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Comparable per-edition load/prepare/call benchmarks use one JSON operation
// and the same body and response transport. The 3.1 branch also runs on the
// stage 5 baseline. New-edition baseline failures remain visible, not skipped.
func BenchmarkEditions(b *testing.B) {
	for _, version := range editionVersions {
		b.Run(version, func(b *testing.B) {
			doc := []byte(editionPost(version, "application/json", `{"type":"object"}`))
			opts := &openapi.Options{HTTPClient: &http.Client{Transport: cannedRT{}}}
			in := &openapi.Input{Body: map[string]int{"n": 1}}
			b.Run("Load", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(doc)))
				for b.Loop() {
					c, err := openapi.Parse(context.Background(), doc, testDocURI, opts)
					if err != nil {
						b.Fatal(err)
					}
					if len(c.Operations()) != 1 {
						b.Fatal("no operation")
					}
				}
			})
			c, err := openapi.Parse(context.Background(), doc, testDocURI, opts)
			if err != nil {
				b.Fatal(err)
			}
			c.Operations()
			b.Run("Prepare", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					req, err := c.Prepare("POST /x", in)
					if err != nil {
						b.Fatal(err)
					}
					req.HTTP.Body.Close()
				}
			})
			b.Run("Call", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := c.Call(context.Background(), "POST /x", in, nil); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

var editionBenchValues = []string{"red shoes", "sale/clearance", "new"}

func editionBenchClient(t testing.TB, kind string) *openapi.Client {
	var doc string
	switch kind {
	case "Collection":
		doc = editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"tags","in":"query","type":"array","collectionFormat":"pipes","items":{"type":"string"}}]}}`)
	case "Querystring":
		doc = editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"whole","in":"querystring","content":{"application/x-www-form-urlencoded":{}}}]}}`)
	case "Positional":
		doc = positionalDoc("multipart/mixed", `"prefixEncoding":[{"contentType":"text/plain"}],"itemEncoding":{"contentType":"application/json"}`)
	}
	return editionClient(t, doc, &openapi.Options{HTTPClient: &http.Client{Transport: cannedRT{}}})
}

func editionBenchInput(kind string) *openapi.Input {
	switch kind {
	case "Collection":
		return &openapi.Input{Params: map[string]any{"tags": editionBenchValues}}
	case "Querystring":
		return &openapi.Input{Params: map[string]any{"whole": map[string]any{"tags": editionBenchValues}}}
	default:
		return &openapi.Input{MediaType: "multipart/mixed; boundary=edition-bench", Body: []any{"metadata", map[string]int{"n": 1}}}
	}
}

// Independent hand-written controls use standards encoders and the same
// HTTPClient/transport. Positional content is rebuilt per request just as the
// client prepares replayable bytes per call, with a fixed boundary for parity.
func editionBenchHand(kind string) (*http.Request, error) {
	base := "https://api.example.test/x"
	switch kind {
	case "Collection":
		var encoded []string
		for _, v := range editionBenchValues {
			encoded = append(encoded, pctName(v))
		}
		return http.NewRequest("GET", base+"?tags="+strings.Join(encoded, "%7C"), nil)
	case "Querystring":
		var pairs []string
		for _, v := range editionBenchValues {
			pairs = append(pairs, formPairs("tags", v))
		}
		return http.NewRequest("GET", base+"?"+strings.Join(pairs, "&"), nil)
	default:
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		if err := w.SetBoundary("edition-bench"); err != nil {
			return nil, err
		}
		for i, content := range []string{"metadata", `{"n":1}`} {
			media := "text/plain"
			if i == 1 {
				media = "application/json"
			}
			p, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {media}})
			if err != nil {
				return nil, err
			}
			if _, err = io.WriteString(p, content); err != nil {
				return nil, err
			}
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		req, err := http.NewRequest("POST", base, bytes.NewReader(body.Bytes()))
		if err == nil {
			req.Header.Set("Content-Type", "multipart/mixed; boundary=edition-bench")
		}
		return req, err
	}
}

func BenchmarkEditionSerializers(b *testing.B) {
	for _, kind := range []string{"Collection", "Querystring", "Positional"} {
		b.Run(kind, func(b *testing.B) {
			b.Run("Client", func(b *testing.B) {
				c := editionBenchClient(b, kind)
				in := editionBenchInput(kind)
				key := "GET /x"
				if kind == "Positional" {
					key = "POST /x"
				}
				c.Operations()
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if _, err := c.Call(context.Background(), key, in, nil); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Hand", func(b *testing.B) {
				hc := &http.Client{Transport: cannedRT{}}
				b.ReportAllocs()
				for b.Loop() {
					req, err := editionBenchHand(kind)
					if err != nil {
						b.Fatal(err)
					}
					resp, err := hc.Do(req)
					if err != nil {
						b.Fatal(err)
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			})
		})
	}
}

// Compare controls outside timing, so a faster control must perform the same
// serialization work. Multipart is compared by parsed headers and part bytes.
func TestEditionsBenchmarkParity(t *testing.T) {
	for _, kind := range []string{"Collection", "Querystring", "Positional"} {
		t.Run(kind, func(t *testing.T) {
			c := editionBenchClient(t, kind)
			key := "GET /x"
			if kind == "Positional" {
				key = "POST /x"
			}
			req := mustPrepare(t, c, key, editionBenchInput(kind))
			hand, err := editionBenchHand(kind)
			if err != nil {
				t.Fatal(err)
			}
			if req.HTTP.Method != hand.Method || req.HTTP.URL.String() != hand.URL.String() {
				t.Errorf("request %s %s; hand %s %s", req.HTTP.Method, req.HTTP.URL, hand.Method, hand.URL)
			}
			if kind == "Positional" {
				got := editionBody(t, req)
				want, err := io.ReadAll(hand.Body)
				hand.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				_, _, gp := readMultipart(t, req.HTTP.Header.Get("Content-Type"), got)
				_, _, wp := readMultipart(t, hand.Header.Get("Content-Type"), want)
				if len(gp) != len(wp) {
					t.Fatalf("parts %d / %d", len(gp), len(wp))
				}
				for i := range gp {
					if !bytes.Equal(trimNL(gp[i].body), trimNL(wp[i].body)) || fmt.Sprint(gp[i].header) != fmt.Sprint(wp[i].header) {
						t.Errorf("part %d differs", i)
					}
				}
			}
		})
	}
}
