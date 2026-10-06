package openapi_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Sequential framing boundaries are kept independently of value codecs.
// RFC 7464 section 2.1 requires RS before each possible-JSON octet string.
func TestStream7BoundarySequencePrefix(t *testing.T) {
	for _, prefix := range []string{"{}", " \t\r\n", "\xef\xbb\xbf", "garbage"} {
		t.Run(prefix, func(t *testing.T) {
			for _, suffix := range []string{"", "\x1e1\n"} {
				wire := prefix + suffix
				for _, raw := range []bool{false, true} {
					r, body := stream7Response(t, "application/json-seq", wire, nil, 1)
					var es []error
					var recovered string
					if raw {
						var got [][]byte
						got, es = stream7Collect[[]byte](r)
						if len(got) == 2 {
							recovered = string(got[1])
						}
					} else {
						var got []int
						got, es = stream7Collect[int](r)
						if len(got) == 2 {
							recovered = fmt.Sprint(got[1])
						}
					}
					want := 1
					if suffix != "" {
						want++
					}
					if len(es) != want || !errors.Is(es[0], openapi.ErrItem) || want == 2 && es[1] != nil {
						t.Fatalf("raw=%v wire=%q errors=%v", raw, wire, es)
					}
					if suffix != "" && recovered != map[bool]string{false: "1", true: "1\n"}[raw] {
						t.Fatalf("raw=%v recovered %q", raw, recovered)
					}
					if body.closed.Load() == 0 {
						t.Fatal("body not closed at end")
					}
				}
				var out []int
				_, err := cCall7(t, "application/json-seq", wire, &out)
				var de *openapi.DecodeError
				if !errors.As(err, &de) || errors.Is(err, errors.ErrUnsupported) {
					t.Fatalf("whole decode %q: %v", wire, err)
				}
			}
		})
	}
	t.Run("bounded prefix", func(t *testing.T) {
		r, _ := stream7Response(t, "application/json-seq", "xxxx\x1e1\n", func(o *openapi.Options) { o.MaxItemBytes = 3 }, 1)
		_, es := stream7Collect[[]byte](r)
		var bound *http.MaxBytesError
		if len(es) != 1 || !errors.As(es[0], &bound) || bound.Limit != 3 || errors.Is(es[0], openapi.ErrItem) {
			t.Fatalf("errors %v", es)
		}
	})
	t.Run("failed read before prefix boundary", func(t *testing.T) {
		boom := errors.New("prefix read failed")
		body := &stream7Body{reader: &stream7Chunks{data: "bad", terminal: boom}}
		c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) {
			return stream7HTTP(r, 200, "application/json-seq", body), nil
		}), nil)
		r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		_, es := stream7Collect[any](r)
		if len(es) != 1 || !errors.Is(es[0], boom) || errors.Is(es[0], openapi.ErrItem) {
			t.Fatalf("errors %v", es)
		}
	})
}

// HTML 9.2.5/9.2.6 accepts CR, LF and CRLF line endings. Stream's
// incremental contract must dispatch an ended block before the next block
// is available. A CR is itself a line ending; optional following LF is not
// a reason to wait for more input before delivering an already-ended block.
func TestStream7BoundarySSEIncrementalEndings(t *testing.T) {
	for _, ending := range []string{"\n", "\r", "\r\n"} {
		for _, events := range []bool{false, true} {
			t.Run(strings.ReplaceAll(strings.ReplaceAll(ending, "\r", "CR"), "\n", "LF")+map[bool]string{false: "Items", true: "Events"}[events], func(t *testing.T) {
				release := make(chan struct{})
				var once sync.Once
				open := func() { once.Do(func() { close(release) }) }
				defer open()
				prefix := "data: first" + ending + ending
				body := &stream7Body{reader: stream7ReadFunc(func(p []byte) (int, error) {
					if len(prefix) > 0 {
						n := copy(p[:min(1, len(p))], prefix)
						prefix = prefix[n:]
						return n, nil
					}
					<-release
					return 0, io.EOF
				})}
				c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) {
					return stream7HTTP(r, 200, "text/event-stream", body), nil
				}), nil)
				r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer r.Body.Close()
				type result struct {
					data string
					err  error
				}
				first := make(chan result, 1)
				done := make(chan struct{})
				go func() {
					defer close(done)
					if events {
						for ev, e := range openapi.Events(r) {
							first <- result{string(ev.Data), e}
							return
						}
					} else {
						for v, e := range openapi.Items[map[string]any](r) {
							data, _ := v["data"].(string)
							first <- result{data, e}
							return
						}
					}
					first <- result{err: errors.New("no dispatched event")}
				}()
				got := stream7Await(t, first)
				if got.err != nil || got.data != "first" {
					t.Fatalf("first event %+v", got)
				}
				open()
				stream7Await(t, done)
				if body.closed.Load() == 0 {
					t.Fatal("body not closed on break")
				}
			})
		}
	}
}

// Array reconstruction must not make malformed records valid by merging
// their syntax. Caller codecs still own value syntax (client.go,
// Options.Codecs): TestSequentialItemsUseCodecs also admits non-JSON codec
// bytes.
func TestStream7BoundaryAggregateRecordSyntax(t *testing.T) {
	for _, ct := range []string{"application/jsonl", "application/json-seq"} {
		t.Run(ct, func(t *testing.T) {
			for _, item := range []string{"1,2", "[", "]", `"one","two"`} {
				wire := item + "\n"
				if ct == "application/json-seq" {
					wire = "\x1e" + wire
				}
				var out []any
				_, err := cCall7(t, ct, wire, &out)
				var de *openapi.DecodeError
				if !errors.As(err, &de) || errors.Is(err, errors.ErrUnsupported) {
					t.Fatalf("malformed record %q became %#v: %v", wire, out, err)
				}
			}
			var inputs []string
			codec := stream7Codec{decode: func(r io.Reader, out any) error {
				p, err := io.ReadAll(r)
				if err != nil {
					return err
				}
				text := strings.TrimSpace(string(p))
				inputs = append(inputs, text)
				*out.(*string) = text
				return nil
			}}
			wire := "J(1)\nJ(x)\n"
			if ct == "application/json-seq" {
				wire = "\x1eJ(1)\n\x1eJ(x)\n"
			}
			change := func(o *openapi.Options) { o.Codecs = map[string]openapi.Codec{"application/json": codec} }
			r, _ := stream7Response(t, ct, wire, change, 1)
			got, es := stream7Collect[string](r)
			stream7NoErrors(t, es)
			if len(got) != 2 || got[0] != "J(1)" || got[1] != "J(x)" {
				t.Fatalf("custom items %q", got)
			}
			inputs = nil
			var out string
			_, err := stream7CallClient(t, ct, wire, 200, change).Call(t.Context(), "get", nil, &out)
			if err != nil || len(inputs) != 1 || strings.Join(strings.Fields(out), "") != "[J(1),J(x)]" {
				t.Fatalf("aggregate out=%q codec inputs=%q err=%v", out, inputs, err)
			}
		})
	}
}

// RFC 7464 section 2.4 scalar truncation checks apply to valid JSON scalars
// (Items). A record that is not one is left to a raw []byte target and to
// custom codecs.
func TestStream7BoundaryRawScalarCanary(t *testing.T) {
	for _, tc := range []struct {
		item string
		bad  bool
	}{{"1", true}, {"true", true}, {"false", true}, {"null", true},
		{"not-json", false}, {"n", false}, {"123custom", false}} {
		t.Run(tc.item, func(t *testing.T) {
			wire := "\x1e" + tc.item + "\x1e"
			r, _ := stream7Response(t, "application/json-seq", wire, nil, 1)
			got, es := stream7Collect[[]byte](r)
			if len(es) != 1 || errors.Is(es[0], openapi.ErrItem) != tc.bad {
				t.Fatalf("raw %q errors=%v", tc.item, es)
			}
			if !tc.bad && (es[0] != nil || string(got[0]) != tc.item) {
				t.Fatalf("raw got=%q errors=%v", got, es)
			}
			codec := stream7Codec{decode: func(r io.Reader, out any) error {
				p, err := io.ReadAll(r)
				*out.(*string) = string(p)
				return err
			}}
			r, _ = stream7Response(t, "application/json-seq", wire,
				func(o *openapi.Options) { o.Codecs = map[string]openapi.Codec{"application/json": codec} }, 1)
			custom, es := stream7Collect[string](r)
			if len(es) != 1 || errors.Is(es[0], openapi.ErrItem) != tc.bad {
				t.Fatalf("custom %q errors=%v", tc.item, es)
			}
			if !tc.bad && (es[0] != nil || custom[0] != tc.item) {
				t.Fatalf("custom got=%q errors=%v", custom, es)
			}
		})
	}
}

// HTML discards an unfinished block at EOF; that does not exempt its wire
// bytes from MaxItemBytes. The public bound excludes only one leading BOM
// and the terminating empty line, which is absent in this fixture.
func TestStream7BoundarySSEUnterminatedLimit(t *testing.T) {
	const payload = "data:x"
	for _, bom := range []string{"", "\ufeff"} {
		for _, limit := range []int64{int64(len(payload) - 1), int64(len(payload)), int64(len(payload) + 1)} {
			for _, events := range []bool{false, true} {
				t.Run(fmt.Sprintf("BOM=%t/limit=%d/events=%t", bom != "", limit, events), func(t *testing.T) {
					r, body := stream7Response(t, "text/event-stream", bom+payload, func(o *openapi.Options) { o.MaxItemBytes = limit }, 1)
					var errs []error
					if events {
						for _, err := range openapi.Events(r) {
							errs = append(errs, err)
						}
					} else {
						_, errs = stream7Collect[map[string]any](r)
					}
					if limit < int64(len(payload)) {
						var bound *http.MaxBytesError
						if len(errs) != 1 || !errors.As(errs[0], &bound) || bound.Limit != limit || errors.Is(errs[0], openapi.ErrItem) {
							t.Fatalf("unterminated oversize block: %v", errs)
						}
					} else if len(errs) != 0 {
						t.Fatalf("unterminated block dispatched: %v", errs)
					}
					if body.closed.Load() == 0 {
						t.Fatal("body not closed at end")
					}
				})
			}
		}
	}
}
