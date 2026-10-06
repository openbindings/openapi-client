package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Comparable per-edition load/prepare/call benchmarks use one JSON operation
// and the same body and response transport. The 3.1 branch also runs on the
// pre-editions baseline. New-edition baseline failures remain visible, not
// skipped.
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
var editionBenchObject = map[string]int{"n": 1}

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
		return &openapi.Input{MediaType: "multipart/mixed; boundary=edition-bench", Body: []any{"metadata", editionBenchObject}}
	}
}

// Independent hand-written controls use standards encoders and the same
// HTTPClient/transport. Positional content is rebuilt per request just as the
// client prepares replayable bytes per call, with a fixed boundary for parity.
func editionBenchHand(kind string) (*http.Request, error) {
	base := "https://api.example.test/x"
	switch kind {
	case "Collection":
		var target strings.Builder
		target.WriteString(base + "?tags=")
		for i, v := range editionBenchValues {
			if i > 0 {
				target.WriteString("%7C")
			}
			target.WriteString(strings.ReplaceAll(url.QueryEscape(v), "+", "%20"))
		}
		return http.NewRequest("GET", target.String(), nil)
	case "Querystring":
		var target strings.Builder
		target.WriteString(base + "?")
		for i, v := range editionBenchValues {
			if i > 0 {
				target.WriteByte('&')
			}
			target.WriteString("tags=")
			target.WriteString(formEnc(v))
		}
		return http.NewRequest("GET", target.String(), nil)
	default:
		encoded, err := json.Marshal(editionBenchObject)
		if err != nil {
			return nil, err
		}
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		if err := w.SetBoundary("edition-bench"); err != nil {
			return nil, err
		}
		for i := range 2 {
			media := "text/plain"
			if i == 1 {
				media = "application/json"
			}
			p, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {media}})
			if err != nil {
				return nil, err
			}
			if i == 0 {
				_, err = io.WriteString(p, "metadata")
			} else {
				_, err = p.Write(encoded)
			}
			if err != nil {
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

// The hand controls are also checked on the pre-editions baseline, where
// client/hand parity cannot run past unsupported-edition admission.
func TestEditionHandControls(t *testing.T) {
	for _, tc := range []struct{ kind, target string }{
		{"Collection", "/x?tags=red%20shoes%7Csale%2Fclearance%7Cnew"},
		{"Querystring", "/x?tags=red+shoes&tags=sale%2Fclearance&tags=new"},
		{"Positional", "/x"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			req, err := editionBenchHand(tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if req.URL.RequestURI() != tc.target {
				t.Errorf("target %q want %q", req.URL.RequestURI(), tc.target)
			}
			if tc.kind != "Positional" {
				return
			}
			body, err := io.ReadAll(req.Body)
			req.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			_, _, parts := readMultipart(t, req.Header.Get("Content-Type"), body)
			if len(parts) != 2 {
				t.Fatalf("parts %d", len(parts))
			}
			want, err := json.Marshal(editionBenchObject)
			if err != nil {
				t.Fatal(err)
			}
			if string(parts[0].body) != "metadata" || !bytes.Equal(parts[1].body, want) {
				t.Errorf("parts %#v", parts)
			}
		})
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
