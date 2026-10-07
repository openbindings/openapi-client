package openapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Items promises context errors are terminal. A decoder may fail while the
// original call is canceled; the resulting item error preserves both causes
// and cannot become recoverable merely because it also wraps ErrItem.
func TestItemsCancelledCodecErrorEndsIteration(t *testing.T) {
	for _, tc := range []struct{ name, ct, wire string }{
		{"JSONL", "application/jsonl", "1\n2\n3\n"},
		{"JSONseq", "application/json-seq", "\x1e1\n\x1e2\n\x1e3\n"},
		{"SSE", "text/event-stream", "data:first\n\ndata:second\n\n"},
		{"multipart", "multipart/mixed; boundary=B", streamMultipart("Content-Type: application/json\r\n\r\n1", "Content-Type: application/json\r\n\r\n2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause, itemErr := errors.New("original call canceled"), errors.New("item codec failed")
			calls := 0
			codec := streamCodec{decode: func(r io.Reader, v any) error {
				calls++
				if calls == 1 {
					cancel(cause)
					return itemErr
				}
				return json.NewDecoder(r).Decode(v)
			}}
			body := &streamBody{reader: strings.NewReader(tc.wire)}
			c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) {
				return streamHTTP(r, 200, tc.ct, body), nil
			}), func(o *openapi.Options) { o.Codecs = map[string]openapi.Codec{"application/json": codec} })
			r, err := mustPrepare(t, c, "get", nil).Send(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			_, errs := streamCollect[any](r)
			if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) || !errors.Is(errs[0], cause) || !errors.Is(errs[0], itemErr) || !errors.Is(errs[0], openapi.ErrItem) {
				t.Fatalf("want one terminal context/cause/item error; calls=%d errors=%v", calls, errs)
			}
			if calls != 1 {
				t.Fatalf("codec called %d times after terminal error", calls)
			}
			if body.closed.Load() == 0 {
				t.Fatal("terminal error did not close response")
			}
		})
	}
}

// Events has no caller codec. Cancel within a successful Read that supplies
// a complete overflowing-retry block and a later valid block. Whether the
// cancellation is noticed before or during parsing the first block, a yielded
// context/cause error must be last; the later buffered block cannot follow it.
func TestEventsCancellationPreemptsBufferedBlocks(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cause := errors.New("event read canceled original call")
	wire := "retry: 999999999999999999999999999999\n\ndata: after\n\n"
	reader := strings.NewReader(wire)
	body := &streamBody{reader: streamReadFunc(func(p []byte) (int, error) {
		n, err := reader.Read(p)
		if n > 0 {
			cancel(cause)
		}
		return n, err
	})}
	c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) {
		return streamHTTP(r, 200, "text/event-stream", body), nil
	}), nil)
	r, err := mustPrepare(t, c, "get", nil).Send(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var errs []error
	for _, err := range openapi.Events(r) {
		errs = append(errs, err)
	}
	if len(errs) != 1 || !errors.Is(errs[0], context.Canceled) || !errors.Is(errs[0], cause) {
		t.Fatalf("want one terminal context/cause error; got %v", errs)
	}
	if body.closed.Load() == 0 {
		t.Fatal("terminal error did not close response")
	}
}
