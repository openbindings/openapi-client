package openapi_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Loader.Fetch replaces the default retrieval and supplies an absolute
// final URI; an empty final means the requested URI. RFC 3986 section 3.1
// defines scheme = ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ), permitting
// one-letter schemes. Reusing a parsed retrieval URL must not mistake a
// custom scheme for the file/drive-path placeholder used by default loading.
func TestCustomFetchUnchangedAbsoluteURI(t *testing.T) {
	const content = `{"openapi":"3.1.0","paths":{"/p":{"get":{"operationId":"p"}}}}`
	for _, tt := range []struct{ name, uri, final string }{
		{"one letter explicit final", "x://example.test/openapi.json", "x://example.test/openapi.json"},
		{"one letter empty final", "x://example.test/openapi.json", ""},
		{"https control", "https://example.test/openapi.json", "https://example.test/openapi.json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			l := openapi.Loader{Fetch: func(ctx context.Context, uri string) (io.ReadCloser, string, error) {
				calls++
				if uri != tt.uri {
					t.Errorf("Fetch URI = %q, want %q", uri, tt.uri)
				}
				return io.NopCloser(strings.NewReader(content)), tt.final, nil
			}}
			c, err := l.Load(t.Context(), tt.uri, nil)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Errorf("Fetch calls = %d, want 1", calls)
			}
			wantStrings(t, "loaded URIs", c.DocumentURIs(), []string{tt.uri})
			if got := string(c.Document(tt.uri)); got != content {
				t.Errorf("Document = %q, want fetched content", got)
			}
			if got := mustOp(t, c, "p").Source; got != tt.uri+"#/paths/~1p/get" {
				t.Errorf("Source = %q, want the custom final URI", got)
			}
		})
	}
}
