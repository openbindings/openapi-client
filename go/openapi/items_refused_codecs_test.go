package openapi_test

import (
	"errors"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// client.go, Options.Codecs: "A key Load would refuse, given through
// Client.With, ... With such a key, when the response has a body (see
// Client.Call), decoding it into such a pointer with Response.Decode, or with
// StatusError.Decode when its Err is nil, returns a *DecodeError naming
// Options.Codecs, and Items, for a T other than []byte or *multipart.Part,
// yields that error as its only result, not wrapping ErrItem." Items yields it
// for JSON Lines, for a multipart body's parts and for a body read as one item,
// and yields no item decoded with the built-in rules; a []byte or
// *multipart.Part T bypasses codecs and gets every item.
func TestItemsReportsRefusedCodecsKey(t *testing.T) {
	const mixed = "--B\r\nContent-Type: application/json\r\n\r\n{\"a\":1}\r\n--B\r\nContent-Type: application/json\r\n\r\n{\"a\":2}\r\n--B--\r\n"
	for _, tc := range []struct {
		name, ct, body string
		items          int
	}{
		{"JSON Lines", "application/jsonl", "{\"a\":1}\n{\"a\":2}\n", 2},
		{"multipart", "multipart/mixed; boundary=B", mixed, 2},
		{"one item", "application/json", `{"a":1}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWire(t, typedAnswer(200, tc.ct, tc.body))
			c := parseFor(t, w, doc31(`"/s":{"get":{"operationId":"s"}}`), nil).With(func(o *openapi.Options) {
				o.Codecs["not a media type"] = jsonCodec{}
			})
			r, err := c.Stream(t.Context(), "s", nil)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			var results []error
			for v, err := range openapi.Items[any](r) {
				if err == nil {
					t.Errorf("an item was decoded despite the refused key: %v", v)
				}
				results = append(results, err)
			}
			if len(results) != 1 {
				t.Fatalf("Items yielded %d results %v, want one error", len(results), results)
			}
			var de *openapi.DecodeError
			if err := results[0]; !errors.As(err, &de) || errors.Is(err, openapi.ErrItem) || !strings.Contains(err.Error(), "Options.Codecs") {
				t.Errorf("Items yielded %v (%T); want a *DecodeError naming Options.Codecs, not wrapping ErrItem", err, err)
			}

			r, err = c.Stream(t.Context(), "s", nil)
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			n := 0
			if strings.HasPrefix(tc.ct, "multipart/") {
				for p, err := range openapi.Items[*multipart.Part](r) {
					if err != nil || p == nil {
						t.Fatalf("*multipart.Part item %d: %v", n, err)
					}
					n++
				}
			} else {
				for _, err := range openapi.Items[[]byte](r) {
					if err != nil {
						t.Fatalf("[]byte item %d: %v", n, err)
					}
					n++
				}
			}
			if n != tc.items {
				t.Errorf("a raw T got %d items, want %d", n, tc.items)
			}
		})
	}
}
