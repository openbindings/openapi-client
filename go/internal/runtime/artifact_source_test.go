package openapiclient

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalArtifactFileURI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space %2F#é.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	uriPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		uriPath = "/" + uriPath
	}
	encoded := (&url.URL{Scheme: "file", Path: uriPath}).String()
	u, err := url.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := localArtifactPath(u)
	if err != nil || decoded != path {
		t.Fatalf("round trip %s => %s %v", encoded, decoded, err)
	}
	data, err := os.ReadFile(decoded)
	if err != nil || string(data) != "{}" {
		t.Fatalf("read %s %v", data, err)
	}
	if !strings.Contains(encoded, "%252F%23") {
		t.Fatalf("not encoded: %s", encoded)
	}
	for _, bad := range []string{"file://untrusted.example/share/a.json", "file:relative.json"} {
		u, _ := url.Parse(bad)
		if _, err := localArtifactPath(u); err == nil {
			t.Fatalf("unsupported URI admitted: %s", bad)
		}
	}
}
