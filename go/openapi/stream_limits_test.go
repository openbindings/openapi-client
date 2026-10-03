package openapi_test

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Precise Stage 7 owner ruling: JSONL excludes LF/CRLF; JSON-seq excludes
// RS but includes trailing JSON whitespace; SSE includes each nonblank
// line and its terminator, including ignored lines, excluding the final
// empty line and one leading BOM. Each skipped block resets the bound.
func TestStream7FramingLimitAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, ct, wire string
		limit          int64
		bad            bool
		count          int
	}{
		{"JSONL LF", "application/jsonl", "123\n", 3, false, 1},
		{"JSONL CRLF", "application/jsonl", "123\r\n", 3, false, 1},
		{"JSONL final bare CR payload", "application/jsonl", "123\r", 3, true, 0},
		{"sequence LF included", "application/json-seq", "\x1e123\n", 3, true, 0},
		{"sequence exact", "application/json-seq", "\x1e123\n", 4, false, 1},
		{"SSE LF exact", "text/event-stream", "data:x\n\n", 7, false, 1},
		{"SSE CRLF included", "text/event-stream", "data:x\r\n\r\n", 7, true, 0},
		{"SSE BOM excluded", "text/event-stream", "\ufeffdata:x\n\n", 7, false, 1},
		{"SSE ignored over limit", "text/event-stream", ":" + strings.Repeat("x", 32) + "\n\n", 8, true, 0},
		{"SSE unknown over limit", "text/event-stream", strings.Repeat("unknown:x\n", 4) + "\n", 20, true, 0},
		{"SSE ignored blocks reset", "text/event-stream", strings.Repeat(":12345\n\n", 30) + "data:x\n\n", 7, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := stream7Response(t, tc.ct, tc.wire, func(o *openapi.Options) { o.MaxItemBytes = tc.limit }, 1)
			_, errs := stream7Collect[any](r)
			if tc.bad {
				var max *http.MaxBytesError
				if len(errs) != 1 || !errors.As(errs[0], &max) || max.Limit != tc.limit || errors.Is(errs[0], openapi.ErrItem) {
					t.Fatalf("errors %v", errs)
				}
			} else {
				stream7NoErrors(t, errs)
				if len(errs) != tc.count {
					t.Fatalf("count %d want %d", len(errs), tc.count)
				}
			}
		})
	}
}

// The documented zero limit is 16 MiB, not bufio.Scanner's 64 KiB.
func TestStream7DefaultItemLimit(t *testing.T) {
	for _, n := range []int{16 << 20, (16 << 20) + 1} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			r, _ := stream7Response(t, "application/octet-stream", strings.Repeat("x", n), nil)
			got, errs := stream7Collect[[]byte](r)
			if len(errs) != 1 {
				t.Fatalf("%d errors", len(errs))
			}
			if n == 16<<20 {
				if errs[0] != nil || len(got[0]) != n {
					t.Fatalf("length %d err %v", len(got[0]), errs[0])
				}
			} else {
				var max *http.MaxBytesError
				if !errors.As(errs[0], &max) || max.Limit != 16<<20 {
					t.Fatalf("error %v", errs[0])
				}
			}
		})
	}
}

// Complete frames survive an n>0,error Read, while incomplete trailing
// frames do not become misleading success or recoverable item errors.
func TestStream7PartialReadFailure(t *testing.T) {
	boom := errors.New("wire broke")
	for _, tc := range []struct{ ct, wire string }{{"application/jsonl", "1\n2"}, {"application/json-seq", "\x1e1\n\x1e2"}, {"text/event-stream", "data: first\n\ndata: partial"}} {
		t.Run(tc.ct, func(t *testing.T) {
			body := &stream7Body{reader: &stream7Chunks{data: tc.wire, terminal: boom}}
			c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) { return stream7HTTP(r, 200, tc.ct, body), nil }), nil)
			r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, errs := stream7Collect[any](r)
			if len(errs) != 2 || errs[0] != nil || !errors.Is(errs[1], boom) || errors.Is(errs[1], openapi.ErrItem) {
				t.Fatalf("errors %v", errs)
			}
		})
	}
	// Empty reads with EOF do not manufacture an empty whole-body item.
	r, _ := stream7Response(t, "application/octet-stream", "", nil)
	p, err := io.ReadAll(r.Body)
	if len(p) != 0 || err != nil {
		t.Fatalf("read %q %v", p, err)
	}
}

// Events applies the same wire-byte bound even when a block contains no
// recognized field. MaxBodyBytes is unrelated to this item-scoped limit.
func TestStream7EventsLimit(t *testing.T) {
	for _, wire := range []string{"data: " + strings.Repeat("x", 64) + "\n\n", ":" + strings.Repeat("x", 64) + "\n\n"} {
		r, b := stream7Response(t, "text/event-stream", wire, func(o *openapi.Options) { o.MaxItemBytes = 16; o.MaxBodyBytes = -1 }, 1)
		var errs []error
		for _, e := range openapi.Events(r) {
			errs = append(errs, e)
		}
		var max *http.MaxBytesError
		if len(errs) != 1 || !errors.As(errs[0], &max) || max.Limit != 16 || errors.Is(errs[0], openapi.ErrItem) {
			t.Fatalf("errors %v", errs)
		}
		if b.closed.Load() == 0 {
			t.Fatal("oversized event did not close Body")
		}
	}
	r, _ := stream7Response(t, "text/event-stream", "data: hello\n\n", func(o *openapi.Options) { o.MaxItemBytes = 32; o.MaxBodyBytes = 1 })
	n := 0
	for e, err := range openapi.Events(r) {
		if err != nil || string(e.Data) != "hello" {
			t.Fatalf("%v %v", e, err)
		}
		n++
	}
	if n != 1 {
		t.Fatal(n)
	}
}
