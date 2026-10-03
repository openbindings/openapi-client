package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

type stream7ArrayTarget struct {
	Raw   []byte
	Calls int
}

func (v *stream7ArrayTarget) UnmarshalJSON(p []byte) error {
	v.Calls++
	v.Raw = append(v.Raw[:0], p...)
	return nil
}

type stream7Codec struct{ decode func(io.Reader, any) error }

func (c stream7Codec) Encode(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func (c stream7Codec) Decode(r io.Reader, v any) error { return c.decode(r, v) }

// client.go Call/Response.Decode/StatusError.Decode; OAS 3.2.1 §4.14.3.1:
// sequential bodies decode as a JSON array once into the actual target,
// preserving encoding/json target behavior and exact dynamic numbers.
func TestStream7SequentialDecode(t *testing.T) {
	for _, ct := range []string{"application/jsonl", "application/x-ndjson", "application/json-seq", "application/geo+json-seq", "text/event-stream"} {
		t.Run(ct, func(t *testing.T) {
			wire := "{\"n\":9007199254740993}\n{\"n\":2}\n"
			want := `[{"n":9007199254740993},{"n":2}]`
			if strings.HasSuffix(ct, "json-seq") {
				wire = "\x1e{\"n\":9007199254740993}\n\x1e{\"n\":2}\n"
			}
			if ct == "text/event-stream" {
				wire = "data: 9007199254740993\n\ndata: 2\n\n"
				want = `[{"data":"9007199254740993"},{"data":"2"}]`
			}
			for _, path := range []string{"Call", "Decode", "StatusError.Decode"} {
				t.Run(path, func(t *testing.T) {
					c := stream7CallClient(t, ct, wire, 200, nil)
					var out stream7ArrayTarget
					switch path {
					case "Call":
						if _, err := c.Call(t.Context(), "get", nil, &out); err != nil {
							t.Fatal(err)
						}
					case "Decode":
						r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						if err = r.Decode(&out); err != nil {
							t.Fatal(err)
						}
					case "StatusError.Decode":
						c = stream7CallClient(t, ct, wire, 422, nil)
						_, err := c.Call(t.Context(), "get", nil, nil)
						var status *openapi.StatusError
						if !errors.As(err, &status) {
							t.Fatalf("status %v", err)
						}
						if err = status.Decode(&out); err != nil {
							t.Fatal(err)
						}
					}
					if out.Calls != 1 {
						t.Fatalf("UnmarshalJSON called %d times", out.Calls)
					}
					var a, b any
					if err := json.Unmarshal(out.Raw, &a); err != nil {
						t.Fatal(err)
					}
					json.Unmarshal([]byte(want), &b)
					if !reflect.DeepEqual(a, b) {
						t.Fatalf("array %s want %s", out.Raw, want)
					}
				})
			}
			if ct != "text/event-stream" {
				var out any
				if _, err := cCall7(t, ct, wire, &out); err != nil {
					t.Fatal(err)
				}
				wantV := []any{map[string]any{"n": json.Number("9007199254740993")}, map[string]any{"n": json.Number("2")}}
				if !reflect.DeepEqual(out, wantV) {
					t.Fatalf("dynamic %#v", out)
				}
			}
		})
	}
}
func cCall7(t *testing.T, ct, wire string, out any) (*openapi.Response, error) {
	t.Helper()
	return stream7CallClient(t, ct, wire, 200, nil).Call(t.Context(), "get", nil, out)
}

// Empty sequences become [], including a target with a custom JSON hook.
// Raw targets and text/* string/any retain Call's precedence over framing.
func TestStream7SequentialEmptyAndRawPrecedence(t *testing.T) {
	for _, ct := range []string{"application/jsonl", "application/json-seq", "text/event-stream"} {
		t.Run(ct, func(t *testing.T) {
			var out []map[string]any
			if _, err := cCall7(t, ct, "", &out); err != nil {
				t.Fatal(err)
			}
			if out == nil || len(out) != 0 {
				t.Fatalf("empty array %#v", out)
			}
			wire := "1\n2\n"
			if ct == "application/json-seq" {
				wire = "\x1e1\n\x1e2\n"
			}
			if ct == "text/event-stream" {
				wire = "data: x\n\n"
			}
			var raw []byte
			if _, err := cCall7(t, ct, wire, &raw); err != nil || string(raw) != wire {
				t.Fatalf("raw %q %v", raw, err)
			}
			var writer bytes.Buffer
			if _, err := cCall7(t, ct, wire, &writer); err != nil || writer.String() != wire {
				t.Fatalf("writer %q %v", writer.String(), err)
			}
		})
	}
	for _, target := range []any{new(string), new(any)} {
		if _, err := cCall7(t, "text/event-stream", "data: {\"x\":1}\n\n", target); err != nil {
			t.Fatal(err)
		}
		switch v := target.(type) {
		case *string:
			if *v != "data: {\"x\":1}\n\n" {
				t.Fatal(*v)
			}
		case *any:
			if *v != "data: {\"x\":1}\n\n" {
				t.Fatal(*v)
			}
		}
	}
}

// Whole-body errors are DecodeError rather than recoverable ErrItem. The
// response is retained, MaxBodyBytes bounds raw consumed bytes, and
// MaxItemBytes applies only to Items/Events (client.go Options).
func TestStream7SequentialDecodeFailuresAndBounds(t *testing.T) {
	for _, tc := range []struct{ ct, wire string }{{"application/jsonl", "1\n{bad}\n2\n"}, {"application/json-seq", "\x1e1\x1e2\n"}, {"application/json-seq", "\x1e{\"x\":"}} {
		t.Run(tc.ct+tc.wire, func(t *testing.T) {
			var out []any
			r, err := cCall7(t, tc.ct, tc.wire, &out)
			var de *openapi.DecodeError
			if r == nil || !errors.As(err, &de) {
				t.Fatalf("(%v,%v)", r, err)
			}
			if de.Response == nil {
				t.Fatal("DecodeError omitted Response")
			}
		})
	}
	t.Run("standard target type error", func(t *testing.T) {
		var out []int
		_, err := cCall7(t, "application/jsonl", "1\n\"x\"\n", &out)
		var de *openapi.DecodeError
		var te *json.UnmarshalTypeError
		if !errors.As(err, &de) || !errors.As(err, &te) {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("body bounded", func(t *testing.T) {
		c := stream7CallClient(t, "application/jsonl", "1\n2\n3\n", 200, func(o *openapi.Options) { o.MaxBodyBytes = 4; o.MaxItemBytes = -1 })
		var out []int
		r, err := c.Call(t.Context(), "get", nil, &out)
		var de *openapi.DecodeError
		var max *http.MaxBytesError
		if r == nil || !errors.As(err, &de) || !errors.As(err, &max) || max.Limit != 4 {
			t.Fatalf("(%v,%v)", r, err)
		}
	})
	t.Run("item limit does not cap Call", func(t *testing.T) {
		c := stream7CallClient(t, "application/jsonl", "12345\n", 200, func(o *openapi.Options) { o.MaxItemBytes = 1; o.MaxBodyBytes = 6 })
		var out []int
		if _, err := c.Call(t.Context(), "get", nil, &out); err != nil || !reflect.DeepEqual(out, []int{12345}) {
			t.Fatalf("%v %v", out, err)
		}
	})
}

// Options.Codecs and Stage 7 owner ruling: Items decodes each item's own
// media type. Typed whole-body decoding passes the reconstructed array to
// that codec once. +json-seq uses its corresponding +json type (Stage 4
// ledger IP4-5 / RFC 8091), and raw targets bypass all codecs.
func TestStream7Codecs(t *testing.T) {
	for _, tc := range []struct{ ct, key, wire string }{{"application/jsonl", "application/json", "1\n2\n"}, {"application/json-seq", "application/json", "\x1e1\n\x1e2\n"}, {"application/geo+json-seq", "application/geo+json", "\x1e1\n\x1e2\n"}, {"text/event-stream", "application/json", "data: x\n\ndata: y\n\n"}} {
		t.Run(tc.ct, func(t *testing.T) {
			var inputs []string
			codec := stream7Codec{decode: func(r io.Reader, v any) error {
				p, err := io.ReadAll(r)
				if err != nil {
					return err
				}
				inputs = append(inputs, string(p))
				return json.Unmarshal(p, v)
			}}
			change := func(o *openapi.Options) { o.Codecs = map[string]openapi.Codec{tc.key: codec} }
			r, _ := stream7Response(t, tc.ct, tc.wire, change)
			got, errs := stream7Collect[any](r)
			stream7NoErrors(t, errs)
			if len(got) != 2 || len(inputs) != 2 {
				t.Fatalf("got %v codec calls %q", got, inputs)
			}
			if tc.ct != "text/event-stream" {
				if _, ok := got[0].(float64); !ok {
					t.Fatalf("codec dynamic result overridden: %T", got[0])
				}
			}
			inputs = nil
			var out stream7ArrayTarget
			c := stream7CallClient(t, tc.ct, tc.wire, 200, change)
			if _, err := c.Call(t.Context(), "get", nil, &out); err != nil {
				t.Fatal(err)
			}
			if len(inputs) != 1 || !strings.HasPrefix(strings.TrimSpace(inputs[0]), "[") {
				t.Fatalf("aggregate codec inputs %q", inputs)
			}
			inputs = nil
			var raw []byte
			if _, err := c.Call(t.Context(), "get", nil, &raw); err != nil || len(inputs) != 0 {
				t.Fatalf("raw codec calls %q err %v", inputs, err)
			}
		})
	}
	t.Run("custom errors recover", func(t *testing.T) {
		boom := errors.New("codec rejected item")
		codec := stream7Codec{decode: func(r io.Reader, v any) error {
			p, _ := io.ReadAll(r)
			if strings.TrimSpace(string(p)) == "2" {
				return boom
			}
			return json.Unmarshal(p, v)
		}}
		r, _ := stream7Response(t, "application/jsonl", "1\n2\n3\n", func(o *openapi.Options) { o.Codecs = map[string]openapi.Codec{"application/json": codec} })
		got, errs := stream7Collect[int](r)
		if len(got) != 3 || got[0] != 1 || got[2] != 3 || errs[0] != nil || errs[2] != nil || !errors.Is(errs[1], boom) || !errors.Is(errs[1], openapi.ErrItem) {
			t.Fatalf("%v %v", got, errs)
		}
	})
	t.Run("exact type wins suffix", func(t *testing.T) {
		codec := func(value string) openapi.Codec {
			return stream7Codec{decode: func(_ io.Reader, out any) error { *out.(*string) = value; return nil }}
		}
		r, _ := stream7Response(t, "application/geo+json-seq", "\x1e{}\n", func(o *openapi.Options) {
			o.Codecs = map[string]openapi.Codec{"+json": codec("suffix"), "application/geo+json": codec("exact")}
		})
		got, errs := stream7Collect[string](r)
		stream7NoErrors(t, errs)
		if !reflect.DeepEqual(got, []string{"exact"}) {
			t.Fatal(got)
		}
	})
}
