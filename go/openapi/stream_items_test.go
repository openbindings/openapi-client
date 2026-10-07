package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// stream.go Items; RFC 7464 §§2.1–2.4: records are separated by RS,
// repeated RS is not an empty record, malformed records are recoverable,
// and top-level scalars require trailing JSON whitespace. JSON Lines has
// exactly one value per nonblank line. Final non-LF JSON lines remain lines.
func TestItemsJSONFraming(t *testing.T) {
	cases := []struct {
		name, ct, wire string
		want           []any
		bad            []int
	}{
		{"jsonl", "application/jsonl", "\n \t\r\n{\"n\":9007199254740993}\r\n[1,\"é\\n\\u001e\"]\nnull", []any{map[string]any{"n": json.Number("9007199254740993")}, []any{json.Number("1"), "é\n\x1e"}, nil}, nil},
		{"ndjson errors recover", "application/x-ndjson", "1\n{broken}\n2 3\n4\n{", []any{json.Number("1"), nil, nil, json.Number("4"), nil}, []int{1, 2, 4}},
		{"sequence multiline", "application/json-seq", "\x1e\x1e{\n\"x\":1\n}\n\x1e[2]\x1e\"a\\u001eb\"\x1e", []any{map[string]any{"x": json.Number("1")}, []any{json.Number("2")}, "a\x1eb"}, nil},
		{"sequence scalar truncation", "application/json-seq", "\x1e1\x1etrue\x1efalse\x1enull\x1e2 \x1etrue\t\x1efalse\r\x1enull\n", []any{nil, nil, nil, nil, json.Number("2"), true, false, nil}, []int{0, 1, 2, 3}},
		{"suffix", "application/geo+json-seq; charset=utf-8", "\x1e{\"coordinates\":[1,2]}\n\x1e{bad\x1e{\"coordinates\":[3,4]}", []any{map[string]any{"coordinates": []any{json.Number("1"), json.Number("2")}}, nil, map[string]any{"coordinates": []any{json.Number("3"), json.Number("4")}}}, []int{1}},
		{"empty jsonl", "application/jsonl", "", nil, nil},
		{"blank jsonl", "application/jsonl", "\n\r\n \t\n", nil, nil},
		{"empty sequence", "application/json-seq", "\x1e\x1e", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, chunks := range [][]int{nil, {1}, {2, 1, 7, 3}} {
				r, body := streamedResponse(t, tc.ct, tc.wire, nil, chunks...)
				got, errs := streamCollect[any](r)
				if len(got) != len(tc.want) {
					t.Fatalf("chunks %v: got %d items, want %d: %v", chunks, len(got), len(tc.want), errs)
				}
				bad := map[int]bool{}
				for _, i := range tc.bad {
					bad[i] = true
				}
				for i := range got {
					if bad[i] {
						if !errors.Is(errs[i], openapi.ErrItem) {
							t.Errorf("item %d error %v, want ErrItem", i, errs[i])
						}
						continue
					}
					if errs[i] != nil || !reflect.DeepEqual(got[i], tc.want[i]) {
						t.Errorf("item %d=(%#v,%v), want %#v", i, got[i], errs[i], tc.want[i])
					}
				}
				if body.closed.Load() == 0 {
					t.Error("iteration did not close Body")
				}
			}
		})
	}
}

// Each successful item is decoded into a fresh T (stream.go Items); target
// errors wrap ErrItem and do not poison the decoder for the next record.
func TestItemsFreshTargetPerItem(t *testing.T) {
	type item struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	for _, ct := range []string{"application/jsonl", "application/json-seq"} {
		t.Run(ct, func(t *testing.T) {
			wire := "{\"a\":1}\n{\"a\":\"bad\"}\n{\"b\":2}\n"
			if ct == "application/json-seq" {
				wire = "\x1e" + strings.ReplaceAll(strings.TrimSuffix(wire, "\n"), "\n", "\n\x1e") + "\n"
			}
			r, _ := streamedResponse(t, ct, wire, nil, 1)
			got, errs := streamCollect[*item](r)
			if len(got) != 3 {
				t.Fatalf("got %d items", len(got))
			}
			if errs[0] != nil || errs[2] != nil || !errors.Is(errs[1], openapi.ErrItem) {
				t.Fatalf("errors: %v", errs)
			}
			if got[0] == got[2] || *got[0] != (item{A: 1}) || *got[2] != (item{B: 2}) {
				t.Fatalf("targets reused: %#v %#v", got[0], got[2])
			}
		})
	}
}

// Call media and target precedence also governs whole-body Items. A
// missing, repeated or malformed Content-Type is application/octet-stream;
// response schemas never supply a missing HTTP media type.
func TestItemsWholeBodyFallback(t *testing.T) {
	for _, tc := range []struct {
		ct, wire string
		want     any
	}{
		{"application/json", `{"n":12345678901234567890}`, map[string]any{"n": json.Number("12345678901234567890")}},
		{"text/plain; charset=ISO-8859-1", "caf\xe9", "caf\xe9"},
		{"text/xml", "<x>1</x>", "<x>1</x>"},
		{"", "{\"a\":1}\n", []byte("{\"a\":1}\n")},
		{"invalid media", "x", []byte("x")},
		{"application/octet-stream", "abc", []byte("abc")},
	} {
		t.Run(tc.ct, func(t *testing.T) {
			r, _ := streamedResponse(t, tc.ct, tc.wire, nil, 1)
			got, errs := streamCollect[any](r)
			streamNoErrors(t, errs)
			if len(got) != 1 || !reflect.DeepEqual(got[0], tc.want) {
				t.Fatalf("got %#v want %#v", got, tc.want)
			}
		})
	}
	t.Run("repeated header", func(t *testing.T) {
		r, _ := streamedResponse(t, "application/jsonl", "1\n2\n", nil)
		r.Header.Add("Content-Type", "application/jsonl")
		got, errs := streamCollect[any](r)
		streamNoErrors(t, errs)
		if len(got) != 1 || !bytes.Equal(got[0].([]byte), []byte("1\n2\n")) {
			t.Fatalf("got %#v", got)
		}
	})
	for _, ct := range []string{"", "application/json", "text/plain"} {
		t.Run("empty "+ct, func(t *testing.T) {
			r, _ := streamedResponse(t, ct, "", nil)
			got, errs := streamCollect[any](r)
			if len(got) != 0 || len(errs) != 0 {
				t.Fatalf("empty body yielded %v %v", got, errs)
			}
		})
	}
	t.Run("typed XML", func(t *testing.T) {
		r, _ := streamedResponse(t, "application/xml", "<x><n>3</n></x>", nil, 1)
		type x struct {
			N int `xml:"n"`
		}
		got, errs := streamCollect[x](r)
		streamNoErrors(t, errs)
		if len(got) != 1 || got[0].N != 3 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("raw bypass", func(t *testing.T) {
		r, _ := streamedResponse(t, "application/jsonl", "1\n{bad}\n", nil)
		got, errs := streamCollect[[]byte](r)
		streamNoErrors(t, errs)
		if len(got) != 2 || string(got[0]) != "1" || string(got[1]) != "{bad}" {
			t.Fatalf("raw items %q", got)
		}
	})
}

// io.Reader requires callers to process n>0 before err. Completed records
// before a read failure survive; the failure is terminal, not ErrItem.
func TestItemsReadErrorAfterCompleteItems(t *testing.T) {
	boom := errors.New("response read failed")
	for _, terminal := range []error{io.EOF, boom} {
		t.Run(terminal.Error(), func(t *testing.T) {
			body := &streamBody{reader: &streamChunks{data: "1\n2\n", terminal: terminal}}
			c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) {
				return streamHTTP(r, 200, "application/jsonl", body), nil
			}), nil)
			r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			got, errs := streamCollect[int](r)
			want := 2
			if terminal != io.EOF {
				want++
			}
			if len(got) != want || got[0] != 1 || got[1] != 2 || errs[0] != nil || errs[1] != nil {
				t.Fatalf("got %v errors %v", got, errs)
			}
			if terminal != io.EOF && (!errors.Is(errs[2], boom) || errors.Is(errs[2], openapi.ErrItem)) {
				t.Fatalf("terminal %v", errs[2])
			}
			if body.closed.Load() == 0 {
				t.Error("Body not closed")
			}
		})
	}
}

// Items/Events accept only a live Stream/Send response and one iteration.
// A value copy refers to the same underlying exchange, not a second stream.
func TestItemsSingleIteration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response func(*testing.T) *openapi.Response
	}{
		{"zero", func(*testing.T) *openapi.Response { return &openapi.Response{} }},
		{"constructed", func(*testing.T) *openapi.Response {
			return &openapi.Response{Response: &http.Response{Header: http.Header{"Content-Type": {"application/jsonl"}}, Body: io.NopCloser(strings.NewReader("1\n"))}}
		}},
		{"Call", func(t *testing.T) *openapi.Response {
			return mustCall(t, streamCallClient(t, "application/jsonl", "1\n", 200, nil), "get", nil, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := streamCollect[any](tc.response(t))
			if len(errs) != 1 || errs[0] == nil {
				t.Fatalf("errors %v, want exactly one", errs)
			}
		})
	}
	t.Run("break then another iterator", func(t *testing.T) {
		r, b := streamedResponse(t, "application/jsonl", "1\n2\n", nil, 1)
		copyR := *r
		seq := openapi.Items[int](r)
		for v, err := range seq {
			if err != nil || v != 1 {
				t.Fatalf("first (%d,%v)", v, err)
			}
			break
		}
		if b.closed.Load() == 0 {
			t.Fatal("break did not close Body")
		}
		for _, again := range []func() []error{func() []error {
			var es []error
			for _, e := range seq {
				es = append(es, e)
			}
			return es
		}, func() []error { _, es := streamCollect[int](&copyR); return es }} {
			es := again()
			if len(es) != 1 || es[0] == nil {
				t.Fatalf("repeat errors %v", es)
			}
		}
	})
	t.Run("Events shares ownership", func(t *testing.T) {
		r, _ := streamedResponse(t, "text/event-stream", "data: x\n\n", nil)
		for _, err := range openapi.Events(r) {
			if err != nil {
				t.Fatal(err)
			}
		}
		_, errs := streamCollect[any](r)
		if len(errs) != 1 || errs[0] == nil {
			t.Fatalf("errors %v", errs)
		}
	})
}

// MaxItemBytes is independent of MaxBodyBytes, and a size error is terminal.
// Raw direct Body reads are deliberately exempt (client.go Options).
func TestItemsMaxItemBytes(t *testing.T) {
	for _, ct := range []string{"application/jsonl", "application/json-seq", "application/octet-stream"} {
		t.Run(ct, func(t *testing.T) {
			wire := "1\n\"" + strings.Repeat("x", 256) + "\"\n2\n"
			if ct == "application/json-seq" {
				wire = "\x1e" + strings.ReplaceAll(strings.TrimSuffix(wire, "\n"), "\n", "\n\x1e") + "\n"
			}
			r, b := streamedResponse(t, ct, wire, func(o *openapi.Options) { o.MaxItemBytes = 32; o.MaxBodyBytes = 1 }, 1)
			_, errs := streamCollect[any](r)
			want := 2
			if ct == "application/octet-stream" {
				want = 1
			}
			if len(errs) != want {
				t.Fatalf("errors %v", errs)
			}
			var limit *http.MaxBytesError
			if !errors.As(errs[len(errs)-1], &limit) || limit.Limit != 32 || errors.Is(errs[len(errs)-1], openapi.ErrItem) {
				t.Fatalf("limit error %v", errs)
			}
			if b.closed.Load() == 0 {
				t.Fatal("not closed")
			}
		})
	}
	t.Run("whole-body exact bound", func(t *testing.T) {
		r, _ := streamedResponse(t, "application/octet-stream", "1234", func(o *openapi.Options) { o.MaxItemBytes = 4 })
		got, errs := streamCollect[[]byte](r)
		streamNoErrors(t, errs)
		if len(got) != 1 || string(got[0]) != "1234" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("negative unlimited", func(t *testing.T) {
		r, _ := streamedResponse(t, "application/jsonl", `"`+strings.Repeat("x", 1<<17)+"\"\n", func(o *openapi.Options) { o.MaxItemBytes = -1; o.MaxBodyBytes = 1 })
		got, errs := streamCollect[string](r)
		streamNoErrors(t, errs)
		if len(got) != 1 || len(got[0]) != 1<<17 {
			t.Fatalf("got %d items", len(got))
		}
	})
	t.Run("direct Body unbounded", func(t *testing.T) {
		r, _ := streamedResponse(t, "application/octet-stream", "123456789", func(o *openapi.Options) { o.MaxItemBytes = 1; o.MaxBodyBytes = 1 })
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != "123456789" {
			t.Fatalf("read %q %v", got, err)
		}
	})
	t.Run("body limit does not cap Items", func(t *testing.T) {
		r, _ := streamedResponse(t, "application/jsonl", "12345\n", func(o *openapi.Options) { o.MaxItemBytes = 32; o.MaxBodyBytes = 1 })
		got, errs := streamCollect[int](r)
		streamNoErrors(t, errs)
		if !reflect.DeepEqual(got, []int{12345}) {
			t.Fatal(got)
		}
	})
}

// Stream applies Call's pre-dispatch policy, including zero values.
func TestStreamZeroClientAndRequestRefused(t *testing.T) {
	for _, run := range []func() (*openapi.Response, error){func() (*openapi.Response, error) {
		return new(openapi.Client).Stream(context.Background(), "missing", nil)
	}, func() (*openapi.Response, error) { return new(openapi.Request).Stream(context.Background()) }} {
		r, err := run()
		if r != nil || !errors.Is(err, openapi.ErrNoOperation) {
			t.Fatalf("(%v,%v)", r, err)
		}
		asRequestError(t, err)
	}
}
