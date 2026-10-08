package openapi_test

import (
	"fmt"
	"slices"
	"testing"
)

// A multipart body whose first delimiter line ends in CRLF may end its close
// delimiter's line in LF alone: the parts before it are yielded and the
// iteration ends without an error, an epilogue after it ignored, whatever
// the read boundaries. stream.go, Items: "The body is framed as RFC 2046
// says, except for line breaks. A header line may end in LF alone. If the
// first delimiter line ends in LF alone, LF alone replaces CRLF before and
// after each delimiter. Otherwise the close delimiter's line may still end in
// LF alone."
func TestMultipartCloseDelimiterLineEndingInLF(t *testing.T) {
	const x = "--b\r\nContent-Type: text/plain\r\n\r\nx\r\n"
	const y = "--b\r\nContent-Type: text/plain\r\n\r\ny\r\n"
	for _, tt := range []struct {
		name, body string
		want       []string
	}{
		{"one part", x + "--b--\n", []string{"x"}},
		{"two parts", x + y + "--b--\n", []string{"x", "y"}},
		{"epilogue", x + "--b--\nepilogue\r\n", []string{"x"}},
		{"transport padding", x + "--b-- \t\n", []string{"x"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := codedClient(t, "multipart/mixed; boundary=b", tt.body, nil)
			values, errs := streamItems[string](t, c)
			streamNoErrors(t, errs)
			if !slices.Equal(values, tt.want) {
				t.Errorf("Items[string] yielded %q, want %q", values, tt.want)
			}
			for _, chunks := range readChunks[1:] {
				t.Run(fmt.Sprintf("reads of %v", chunks), func(t *testing.T) {
					r, _ := streamedResponse(t, "multipart/mixed; boundary=b", tt.body, nil, chunks...)
					values, errs := streamCollect[string](r)
					streamNoErrors(t, errs)
					if !slices.Equal(values, tt.want) {
						t.Errorf("Items[string] yielded %q, want %q", values, tt.want)
					}
				})
			}
		})
	}
}
