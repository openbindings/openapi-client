package openapi_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// HTML's UTF-8 decode delegates to WHATWG Encoding §8.1.1. Replacement
// consumes a valid partial prefix together, but reprocesses an out-of-range
// continuation or ASCII byte. This is SSE decoding, not encoding/json's
// independent handling of malformed UTF-8 in JSON string literals.
func TestSSEInvalidUTF8Replacement(t *testing.T) {
	for _, tc := range []struct{ name, wire, want string }{
		{"partial prefix", "\xe1\x80", "\ufffd"},
		{"invalid leads", "\xff\xff", "\ufffd\ufffd"},
		{"overlong", "\xe0\x80", "\ufffd\ufffd"},
		{"surrogate", "\xed\xa0\x80", "\ufffd\ufffd\ufffd"},
		{"outside Unicode", "\xf4\x90\x80\x80", "\ufffd\ufffd\ufffd\ufffd"},
		{"ASCII after prefix", "\xe1\x80A", "\ufffdA"},
		{"valid", "é中😀", "é中😀"},
	} {
		for _, chunks := range [][]int{nil, {1}, {2, 1, 3}} {
			t.Run(fmt.Sprintf("%s/chunks=%v", tc.name, chunks), func(t *testing.T) {
				wire := "data:" + tc.wire + "\nevent:" + tc.wire + "\nid:" + tc.wire + "\n\n"
				want := []map[string]string{{"data": tc.want, "event": tc.want, "id": tc.want}}
				t.Run("Events", func(t *testing.T) {
					r, _ := streamedResponse(t, "text/event-stream", wire, nil, chunks...)
					count := 0
					for ev, err := range openapi.Events(r) {
						if err != nil || string(ev.Data) != tc.want || ev.Event != tc.want || !ev.IDSet || ev.ID != tc.want {
							t.Fatalf("event %#v, error %v; want fields %q", ev, err, tc.want)
						}
						count++
					}
					if count != 1 {
						t.Fatalf("events %d", count)
					}
				})
				t.Run("Items", func(t *testing.T) {
					r, _ := streamedResponse(t, "text/event-stream", wire, nil, chunks...)
					got, errs := streamCollect[map[string]string](r)
					streamNoErrors(t, errs)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("items %#v, want %#v", got, want)
					}
				})
				t.Run("aggregate", func(t *testing.T) {
					c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) {
						return streamHTTP(r, 200, "text/event-stream", io.NopCloser(&streamChunks{data: wire, chunks: chunks})), nil
					}), nil)
					var got []map[string]string
					_, err := c.Call(t.Context(), "get", nil, &got)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("aggregate %#v, error %v; want %#v", got, err, want)
					}
				})
			})
		}
	}
}

// RFC 2045 §6.4 requires an unrecognized transfer encoding to be treated
// as application/octet-stream regardless of the declared Content-Type.
// The public raw target, codec, per-item error and size rules still apply.
func TestItemsMultipartUnknownTransferEncoding(t *testing.T) {
	const ct = "multipart/mixed; boundary=B"
	const payload = "a=3Db\x00\xff"
	unknown := "Content-Type: application/json\r\nContent-Transfer-Encoding: x-private\r\n\r\n" + payload
	wire := streamMultipart(unknown, "Content-Type: application/json\r\n\r\n7")
	t.Run("any stays opaque", func(t *testing.T) {
		r, _ := streamedResponse(t, ct, wire, nil, 1)
		got, errs := streamCollect[any](r)
		streamNoErrors(t, errs)
		if len(got) != 2 {
			t.Fatalf("items %#v", got)
		}
		p, ok := got[0].([]byte)
		if !ok || !bytes.Equal(p, []byte(payload)) {
			t.Fatalf("unknown encoding became %#v", got[0])
		}
		if fmt.Sprint(got[1]) != "7" {
			t.Fatalf("next part %#v", got[1])
		}
	})
	t.Run("raw bytes", func(t *testing.T) {
		r, _ := streamedResponse(t, ct, wire, nil, 1)
		got, errs := streamCollect[[]byte](r)
		streamNoErrors(t, errs)
		if !reflect.DeepEqual(got, [][]byte{[]byte(payload), []byte("7")}) {
			t.Fatalf("raw %q", got)
		}
	})
	t.Run("typed refusal recovers", func(t *testing.T) {
		r, _ := streamedResponse(t, ct, wire, nil, 1)
		got, errs := streamCollect[int](r)
		if len(errs) != 2 || !errors.Is(errs[0], openapi.ErrItem) || errs[1] != nil || got[1] != 7 {
			t.Fatalf("items %v errors %v", got, errs)
		}
	})
	t.Run("octet codec", func(t *testing.T) {
		calls := 0
		codec := streamCodec{decode: func(r io.Reader, v any) error {
			p, err := io.ReadAll(r)
			calls++
			*v.(*string) = "opaque:" + string(p)
			return err
		}}
		r, _ := streamedResponse(t, ct, streamMultipart(unknown), func(o *openapi.Options) { o.Codecs = map[string]openapi.Codec{"application/octet-stream": codec} }, 1)
		got, errs := streamCollect[string](r)
		streamNoErrors(t, errs)
		if calls != 1 || !reflect.DeepEqual(got, []string{"opaque:" + payload}) {
			t.Fatalf("calls %d values %q", calls, got)
		}
	})
	for _, limit := range []int64{int64(len(payload) - 1), int64(len(payload))} {
		t.Run(fmt.Sprintf("encoded byte limit %d", limit), func(t *testing.T) {
			r, _ := streamedResponse(t, ct, streamMultipart(unknown), func(o *openapi.Options) { o.MaxItemBytes = limit }, 1)
			got, errs := streamCollect[[]byte](r)
			if len(errs) != 1 {
				t.Fatalf("errors %v", errs)
			}
			if limit < int64(len(payload)) {
				var bound *http.MaxBytesError
				if !errors.As(errs[0], &bound) || bound.Limit != limit || errors.Is(errs[0], openapi.ErrItem) {
					t.Fatalf("bound error %v", errs[0])
				}
			} else if errs[0] != nil || !bytes.Equal(got[0], []byte(payload)) {
				t.Fatalf("items %q errors %v", got, errs)
			}
		})
	}
	t.Run("raw Part matches NextPart", func(t *testing.T) {
		oracle := multipart.NewReader(strings.NewReader(wire), "B")
		r, _ := streamedResponse(t, ct, wire, func(o *openapi.Options) { o.MaxItemBytes = 1 }, 1)
		count := 0
		for part, err := range openapi.Items[*multipart.Part](r) {
			if err != nil {
				t.Fatal(err)
			}
			want, e := oracle.NextPart()
			if e != nil {
				t.Fatal(e)
			}
			gotBytes, ge := io.ReadAll(part)
			wantBytes, we := io.ReadAll(want)
			if ge != nil || we != nil || !reflect.DeepEqual(part.Header, want.Header) || !bytes.Equal(gotBytes, wantBytes) {
				t.Fatalf("raw part headers %v/%v bytes %q/%q errors %v/%v", part.Header, want.Header, gotBytes, wantBytes, ge, we)
			}
			count++
		}
		if _, err := oracle.NextPart(); err != io.EOF || count != 2 {
			t.Fatalf("parts %d remaining error %v", count, err)
		}
	})
}

type streamCloseHook struct {
	io.Reader
	close func() error
}

func (r *streamCloseHook) Close() error { return r.close() }

// Send/Stream response Close stops an outstanding cooperative upload. The
// underlying response Close is allowed to depend on that upload stopping;
// waiting for it before signaling stop would deadlock this valid transport.
func TestBodyCloseStopsUploadBeforeTransportClose(t *testing.T) {
	for _, mode := range []string{"Send", "Stream", "Send101 cancel", "Send101 Items"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stopped, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			open := func() { once.Do(func() { close(release) }) }
			defer open()
			source := iter.Seq[any](func(yield func(any) bool) {
				defer close(stopped)
				for i := 0; yield(i); i++ {
				}
			})
			var rawBody *streamCloseHook
			raw := strings.HasPrefix(mode, "Send101")
			rt := streamRT(func(req *http.Request) (*http.Response, error) {
				var one [1]byte
				if _, err := io.ReadFull(req.Body, one[:]); err != nil {
					return nil, err
				}
				body := &streamCloseHook{Reader: strings.NewReader(""), close: func() error {
					select {
					case <-stopped:
					case <-release:
					}
					return req.Body.Close()
				}}
				rawBody = body
				status := 200
				if raw {
					status = http.StatusSwitchingProtocols
				}
				return streamHTTP(req, status, "application/jsonl", body), nil
			})
			c := streamUploadClient(t, rt)
			req := mustPrepare(t, c, "post", &openapi.Input{Body: source})
			var r *openapi.Response
			var err error
			if mode != "Stream" {
				r, err = req.Send(ctx)
			} else {
				r, err = req.Stream(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if raw && r.Body != rawBody {
				t.Fatal("Send replaced the 101 response body")
			}
			// Failure cleanup releases the transport before attempting another Close.
			t.Cleanup(func() { open(); cancel(); r.Body.Close() })
			done := make(chan error, 1)
			go func() {
				switch mode {
				case "Send101 cancel":
					// The unchanged raw Body cannot observe arbitrary Close.
					// Cancellation remains the client's upload stop path.
					cancel()
					<-stopped
					done <- r.Body.Close()
				case "Send101 Items":
					// Iteration owns termination even for an unchanged body.
					_, errs := streamCollect[any](r)
					if len(errs) != 0 {
						done <- fmt.Errorf("empty iteration errors: %v", errs)
					} else {
						done <- nil
					}
				default:
					done <- r.Body.Close()
				}
			}()
			if err := streamAwait(t, done); err != nil {
				t.Fatal(err)
			}
			streamAwait(t, stopped)
			if err := r.WaitRequest(t.Context()); err == nil {
				t.Fatal("unfinished upload reported successful")
			}
		})
	}
}

// A positive configured limit remains a bound even at MaxInt64: adding
// framing or EOF lookahead must not wrap it into an immediate refusal.
func TestMaxBytesAcceptMaxInt64(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("JSONL/%q", ending), func(t *testing.T) {
			r, _ := streamedResponse(t, "application/jsonl", "7"+ending, func(o *openapi.Options) { o.MaxItemBytes = math.MaxInt64 }, 1)
			got, errs := streamCollect[int](r)
			streamNoErrors(t, errs)
			if !reflect.DeepEqual(got, []int{7}) {
				t.Fatalf("items %v", got)
			}
		})
	}
	t.Run("whole item", func(t *testing.T) {
		r, _ := streamedResponse(t, "application/octet-stream", "small", func(o *openapi.Options) { o.MaxItemBytes = math.MaxInt64 }, 1)
		got, errs := streamCollect[[]byte](r)
		streamNoErrors(t, errs)
		if !reflect.DeepEqual(got, [][]byte{[]byte("small")}) {
			t.Fatalf("items %q", got)
		}
	})
	t.Run("whole body", func(t *testing.T) {
		c := streamCallClient(t, "application/json", "7", 200, func(o *openapi.Options) { o.MaxBodyBytes = math.MaxInt64 })
		var got int
		_, err := c.Call(t.Context(), "get", nil, &got)
		if err != nil || got != 7 {
			t.Fatalf("value %d error %v", got, err)
		}
	})
}
