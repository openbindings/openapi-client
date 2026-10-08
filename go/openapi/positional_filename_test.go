package openapi_test

import (
	"io"
	"iter"
	"net/http"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// client.go, Part.Filename: "A part without a name, as in positional
// multipart, takes its Content-Disposition only from Header, and Filename is
// refused on it." A Filename on a Part given as an element of an OpenAPI 3.2
// positional body is refused at the element's key, never dropped: under
// multipart/mixed with and without prefixEncoding, and under
// multipart/form-data, where the Part's Header gives its Content-Disposition.
// Given by an iterator, which a positional body of any multipart type but
// multipart/form-data may be, the Part is found only as the body is sent, and
// aborts it (client.go, Input.Body: "Content that cannot be encoded ...
// refuses the call when it is prepared, unless a reader or an iterator
// supplies it; then it is found only as it is sent, and aborts the body").
func TestPositionalPartFilenameRefused(t *testing.T) {
	named := http.Header{"Content-Disposition": {`form-data; name="a"`}}
	for _, tc := range []struct {
		name, media, encoding string
		part                  openapi.Part
	}{
		{"mixed with prefixEncoding", "multipart/mixed", `"prefixEncoding":[{"contentType":"text/plain"}]`, openapi.Part{Content: "x", Filename: "x.txt"}},
		{"mixed with itemEncoding", "multipart/mixed", `"itemEncoding":{"contentType":"text/plain"}`, openapi.Part{Content: "x", Filename: "x.txt"}},
		{"mixed without Encoding", "multipart/mixed", `"x-none":true`, openapi.Part{Content: []byte("x"), MediaType: "text/plain", Filename: "x.txt"}},
		{"form-data", "multipart/form-data", `"prefixEncoding":[{"contentType":"text/plain"}]`, openapi.Part{Content: "x", Filename: "x.txt", Header: named}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := editionClient(t, positionalDoc(tc.media, tc.encoding), nil)
			// The same Part without a Filename is sent.
			ok := tc.part
			ok.Filename = ""
			mustPrepare(t, c, "POST /x", &openapi.Input{Body: []any{ok}})

			_, err := c.Prepare("POST /x", &openapi.Input{Body: []any{tc.part}})
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "Input.Body/0")

			if tc.media == "multipart/form-data" {
				return // a positional form-data body is a slice, never an iterator
			}
			seq := iter.Seq[any](func(yield func(any) bool) { yield(tc.part) })
			req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: seq})
			if _, err := io.ReadAll(req.HTTP.Body); err == nil {
				t.Errorf("an iterator's Part with a Filename was sent without it")
			}
		})
	}
}
