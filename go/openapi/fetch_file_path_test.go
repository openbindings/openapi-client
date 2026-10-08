package openapi_test

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// load.go, Loader.Fetch: "A uri given to Load as a file path reaches Fetch as
// the file URL of its absolute path (filepath.Abs), the URI under which the
// default reads it." A relative path, an absolute one and one holding a space
// reach Fetch as the file URL the zero Loader names the same file by
// (Client.DocumentURIs), and that URL, as Fetch's empty final, is the
// document's base; nothing reaches Fetch as a path.
func TestFetchReceivesFilePathAsFileURL(t *testing.T) {
	const doc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{}}`
	dir := filepath.Join(t.TempDir(), "a dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "openapi.json")
	if err := os.WriteFile(abs, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := openapi.Load(context.Background(), abs, nil)
	if err != nil {
		t.Fatalf("zero Loader: %v", err)
	}
	defaultURI := c.DocumentURIs()[0]

	rel := filepath.Join("testdata", "no-such-dir", "openapi.json")
	relAbs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	relSlash := filepath.ToSlash(relAbs)
	if !strings.HasPrefix(relSlash, "/") {
		relSlash = "/" + relSlash
	}
	for path, want := range map[string]string{
		abs: defaultURI,
		rel: (&url.URL{Scheme: "file", Path: relSlash}).String(),
	} {
		var mu sync.Mutex
		var got []string
		l := openapi.Loader{Fetch: func(_ context.Context, uri string) (io.ReadCloser, string, error) {
			mu.Lock()
			got = append(got, uri)
			mu.Unlock()
			return io.NopCloser(strings.NewReader(doc)), "", nil
		}}
		c, err := l.Load(context.Background(), path, nil)
		if err != nil {
			t.Errorf("path %q: %v", path, err)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("path %q: Fetch received %q, want [%s]", path, got, want)
		}
		if uris := c.DocumentURIs(); len(uris) != 1 || uris[0] != want {
			t.Errorf("path %q: DocumentURIs %q, want [%s]", path, uris, want)
		}
	}
}
