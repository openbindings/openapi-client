package openapi_test

import (
	"errors"
	"mime/multipart"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Items with T = *multipart.Part over a body that is not multipart yields
// one ErrItem for each item the media type frames, and never a Part.
// stream.go, Items: "A T of *multipart.Part reads only a multipart body:
// under any other media type, ... otherwise no item decodes into it, so each
// is an ErrItem. An error that concerns one item wraps [ErrItem] and is yielded
// in its place, and the iteration goes on."
func TestItemsPartRefusedForNonMultipartBody(t *testing.T) {
	for _, tt := range []struct {
		ct, body string
		items    int
	}{
		{"application/json", `{"a":1}`, 1},
		{"application/jsonl", "{\"a\":1}\n{\"a\":2}\n", 2},
		{"application/json-seq", "\x1e{\"a\":1}\n\x1e2\n", 2},
		{"text/event-stream", "data: x\n\ndata: y\n\n", 2},
		{"text/plain", "hello", 1},
		{"application/octet-stream", "\x00\x01", 1},
	} {
		t.Run(tt.ct, func(t *testing.T) {
			c := codedClient(t, tt.ct, tt.body, nil)
			parts, errs := streamItems[*multipart.Part](t, c)
			if len(errs) != tt.items {
				t.Errorf("Items[*multipart.Part] yielded %d results, want %d, one per item", len(errs), tt.items)
			}
			for i, p := range parts {
				if p != nil || !errors.Is(errs[i], openapi.ErrItem) {
					t.Errorf("result %d: Part %v, error %v; want no Part and an ErrItem", i, p, errs[i])
				}
			}
		})
	}
}
