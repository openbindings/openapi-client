package openapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// A fixed-size frame repeated without retaining the full input. It makes
// growth tests measure framing rather than a fixture-sized allocation.
type streamRepeated struct {
	frame               string
	left, offset, chunk int
}

func (r *streamRepeated) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	if r.chunk > 0 {
		p = p[:min(len(p), r.chunk)]
	}
	n := copy(p, r.frame[r.offset:])
	r.offset += n
	if r.offset == len(r.frame) {
		r.offset = 0
		r.left--
	}
	return n, nil
}

// Regression check, not contract: streams do bounded current-item work and have
// linear hostile-input costs. Use the established best-of-five 12x time / 8x
// allocated-byte rule for fourfold input. No result list or full repeated
// stream is retained.
func TestStreamFramingCostLinear(t *testing.T) {
	for _, tc := range []struct {
		name, ct, frame string
		oneByte         bool
		event           bool
	}{
		{"many JSON lines", "application/jsonl", "12345\n", false, false},
		{"one-byte JSON sequence", "application/json-seq", "\x1e12345\n", true, false},
		{"many events", "text/event-stream", "data:x\n\n", false, true},
		{"one-byte events", "text/event-stream", "data:x\n\n", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantLinearBytes(t, "iterate", 1024, func(n int) func() {
				c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) {
					reader := &streamRepeated{frame: tc.frame, left: n}
					if tc.oneByte {
						reader.chunk = 1
					}
					return streamHTTP(r, 200, tc.ct, io.NopCloser(reader)), nil
				}), nil)
				req := mustPrepare(t, c, "get", nil)
				return func() {
					r, e := req.Send(t.Context())
					if e != nil {
						t.Fatal(e)
					}
					count := 0
					if tc.event {
						for _, err := range openapi.Events(r) {
							if err != nil {
								t.Fatal(err)
							}
							count++
						}
					} else {
						for _, err := range openapi.Items[int](r) {
							if err != nil {
								t.Fatal(err)
							}
							count++
						}
					}
					if count != n {
						t.Fatalf("count %d want %d", count, n)
					}
				}
			})
		})
	}
	for _, kind := range []string{"data lines", "ignored fields", "one-byte long line"} {
		t.Run(kind, func(t *testing.T) {
			wantLinearBytes(t, "one event", 2048, func(n int) func() {
				wire := strings.Repeat("data:x\n", n) + "\n"
				chunk := 0
				if kind == "ignored fields" {
					wire = strings.Repeat("ignored:x\n", n) + "data:x\n\n"
				}
				if kind == "one-byte long line" {
					wire = "data:" + strings.Repeat("x", n) + "\n\n"
					chunk = 1
				}
				return func() {
					r, _ := streamedResponse(t, "text/event-stream", wire, func(o *openapi.Options) { o.MaxItemBytes = -1 }, max(chunk, 1))
					count := 0
					for _, err := range openapi.Events(r) {
						if err != nil {
							t.Fatal(err)
						}
						count++
					}
					if count != 1 {
						t.Fatal(count)
					}
				}
			})
		})
	}
}

// A hostile Content-Length must not cause eager body allocation: doc.go,
// Outcomes: "Even with no bound, the length a response body or retrieved
// document declares reserves at most 1 MiB in advance; the rest is allocated
// as its bytes arrive." The amount of readable data and work is fixed while
// the declaration grows from 1 GiB to 16 GiB; this also exercises 32-bit hosts
// safely. The test checks more than that bound: allocation does not grow with
// the declaration at all.
func TestStreamDeclaredLengthNoPreallocation(t *testing.T) {
	wantFlat(t, "declared response length", 1024, func(n int) func() {
		c := streamClient(t, "3.1.2", streamRT(func(req *http.Request) (*http.Response, error) {
			r := streamHTTP(req, 200, "application/jsonl", io.NopCloser(strings.NewReader("1\n")))
			r.ContentLength = int64(n) << 20
			return r, nil
		}), nil)
		req := mustPrepare(t, c, "get", nil)
		return func() {
			r, e := req.Send(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			got, errs := streamCollect[int](r)
			streamNoErrors(t, errs)
			if len(got) != 1 || got[0] != 1 {
				t.Fatal(got)
			}
		}
	})
}

// Oversized input is stopped at the current bound rather than consuming
// an unbounded hostile record. Read count is deterministic and does not
// use machine timing or keep the generated hostile body.
func TestStreamOversizeItemStopsReading(t *testing.T) {
	for _, tc := range []struct{ ct, frame string }{{"application/jsonl", "9"}, {"text/event-stream", ":"}} {
		t.Run(tc.ct, func(t *testing.T) {
			body := &streamBody{reader: &streamRepeated{frame: tc.frame, left: 1 << 24, chunk: 1}}
			c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) { return streamHTTP(r, 200, tc.ct, body), nil }), func(o *openapi.Options) { o.MaxItemBytes = 1024 })
			r, e := mustPrepare(t, c, "get", nil).Send(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			_, errs := streamCollect[any](r)
			if len(errs) != 1 || errs[0] == nil {
				t.Fatalf("%v", errs)
			}
			if reads := body.reads.Load(); reads > 2048 {
				t.Fatalf("read %d bytes after 1024-byte item bound", reads)
			}
		})
	}
}
