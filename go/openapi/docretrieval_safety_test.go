package openapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The Loader never retrieves a URI with userinfo, a redirect hop's included,
// and error text excludes it. load.go, Loader: "A reference is unresolvable
// and never fetched, ... if that URI has userinfo ..., as Load refuses such a
// uri. Whatever AllowReference says, so is one whose retrieval a redirect
// would take to such a URI; the default retrieval never requests that hop";
// errors.go, ErrUnresolved: "Where the client names a URI,
// a reference or a redirect's Location included, it omits userinfo and query",
// and a *url.Error is shown "each URL without userinfo or query". Admission
// callbacks do not waive URI validity. The cases are redirects to such URIs,
// with an admitted redirect as a control, fragment stripping and the
// ErrUnresolved wrapping contract.
func TestDocumentRedirectURIValidity(t *testing.T) {
	const base = "https://docs.example.test/"
	for _, tt := range []struct {
		name, location string
		allowed        bool
	}{
		{"userinfo same origin", "https://u53r:s3cr3t-pw@docs.example.test/final", false},
		{"userinfo other origin", "https://u53r:s3cr3t-pw@other.example.test/final", false},
		{"ordinary same origin", base + "final#unused", true},
	} {
		for _, callback := range []bool{false, true} {
			name := tt.name + "/default"
			if callback {
				name = tt.name + "/callback"
			}
			t.Run(name, func(t *testing.T) {
				var finals atomic.Int32
				hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					h := make(http.Header)
					code := http.StatusOK
					if r.URL.Path == "/child" {
						code = http.StatusFound
						h.Set("Location", tt.location)
					} else {
						finals.Add(1)
					}
					return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(`{"P":{"name":"p","in":"query"}}`)), Request: r}, nil
				})}
				l := openapi.Loader{}
				if callback {
					l.AllowReference = func(from, to string) bool { return true }
				}
				c, err := l.Parse(t.Context(), []byte(bare31(paramOps(map[string]string{"x": "child#/P"}))), base+"openapi.json", &openapi.Options{HTTPClient: hc})
				if err != nil {
					t.Fatal(err)
				}
				op := mustOp(t, c, "x")
				if tt.allowed {
					if op.Err != nil || finals.Load() != 1 {
						t.Fatalf("ordinary redirect: final fetches=%d, error=%v", finals.Load(), op.Err)
					}
					wantStrings(t, "loaded URIs", c.DocumentURIs(), []string{base + "openapi.json", base + "final"})
					return
				}
				if finals.Load() != 0 {
					t.Error("prohibited redirect reached transport")
				}
				_, prepErr := c.Prepare("x", nil)
				for _, err := range []error{op.Err, prepErr} {
					if !errors.Is(err, openapi.ErrUnresolved) {
						t.Errorf("error = %v, want ErrUnresolved", err)
					}
					if err != nil && (strings.Contains(err.Error(), "u53r") || strings.Contains(err.Error(), "s3cr3t-pw")) {
						t.Error("redirect error text exposes userinfo")
					}
				}
			})
		}
	}
}

// Malformed references must not expose userinfo just because url.Parse
// cannot produce a URL to sanitize. No retrieval is needed.
func TestDocumentMalformedReferenceHidesUserinfo(t *testing.T) {
	for _, ref := range []string{"https://u53r:s3cr3t-pw@docs.example.test/%GG#/P", "https://u53r:s3cr3t-pw@docs.example.test:bad/x#/P"} {
		c := parsed(t, []byte(bare31(paramOps(map[string]string{"x": ref}))))
		_, err := c.Prepare("x", nil)
		if !errors.Is(err, openapi.ErrUnresolved) {
			t.Fatalf("error = %v, want ErrUnresolved", err)
		}
		if strings.Contains(err.Error(), "u53r") || strings.Contains(err.Error(), "s3cr3t-pw") {
			t.Error("malformed reference error exposes userinfo")
		}
	}
}

// Sanitizing a nested transport URL error must preserve the retrieval
// cause (ErrUnresolved's wrapping contract), including errors.Is/As.
func TestDocumentTransportErrorKeepsCauseWithoutUserinfo(t *testing.T) {
	cause := errors.New("transport refused")
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://u53r:s3cr3t-pw@docs.example.test/child", Err: cause}
	})}
	c, err := openapi.Parse(t.Context(), []byte(bare31(paramOps(map[string]string{"x": "child#/P"}))), testDocURI, &openapi.Options{HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Prepare("x", nil)
	var urlErr *url.Error
	if !errors.Is(err, cause) || !errors.Is(err, openapi.ErrUnresolved) || !errors.As(err, &urlErr) {
		t.Fatalf("retrieval error lost its wrapped cause or type: %v", err)
	}
	if strings.Contains(err.Error(), "u53r") || strings.Contains(err.Error(), "s3cr3t-pw") {
		t.Error("transport error text exposes userinfo")
	}
}

// Loader's default file boundary applies at retrieval, including a
// concurrent replacement of an admitted symlink, tried over at most 256
// loads. Both files are synthetic test fixtures; no network or existing
// files are involved.
func TestDocumentFileBoundarySurvivesSymlinkReplacement(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	if err := os.Mkdir(allowed, 0700); err != nil {
		t.Fatal(err)
	}
	inside, outside := filepath.Join(allowed, "inside.json"), filepath.Join(root, "outside.json")
	for path, name := range map[string]string{inside: "inside", outside: "outside-marker"} {
		if err := os.WriteFile(path, []byte(`{"P":{"name":"`+name+`","in":"query"}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(allowed, "link.json")
	if err := os.Symlink(inside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	uri := fileURI(filepath.Join(allowed, "openapi.json"))
	content := []byte(bare31(paramOps(map[string]string{"x": "link.json#/P"})))
	parse := func(l *openapi.Loader) *openapi.Client {
		t.Helper()
		c, err := l.Parse(t.Context(), content, uri, nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// An ordinary absolute symlink inside the boundary remains supported.
	l := &openapi.Loader{}
	admittedRef(t, parse(l), "x")
	// An explicit callback replaces the default boundary and may admit
	// the caller's trusted file graph outside the entry directory.
	trusted := &openapi.Loader{AllowReference: func(from, to string) bool { return true }}
	c, err := trusted.Parse(t.Context(), []byte(bare31(paramOps(map[string]string{"x": "../outside.json#/P"}))), uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	admittedRef(t, c, "x")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var replacements int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			for _, target := range []string{outside, inside} {
				select {
				case <-stop:
					return
				default:
				}
				if err := os.Symlink(target, link+".tmp"); err != nil {
					t.Errorf("prepare replacement: %v", err)
					return
				}
				if err := os.Rename(link+".tmp", link); err != nil {
					// Windows can deny replacing a link while a reader has
					// it open. That prevents the attack rather than exposing
					// data; retry once the reader releases it.
					if runtime.GOOS == "windows" && os.IsPermission(err) {
						if err := os.Remove(link + ".tmp"); err != nil {
							t.Errorf("remove refused replacement: %v", err)
							return
						}
						continue
					}
					t.Errorf("replace symlink: %v", err)
					return
				}
				replacements++
			}
		}
	}()
	var stopped sync.Once
	finish := func() { stopped.Do(func() { close(stop); wg.Wait() }) }
	defer finish()
	for range 256 {
		c := parse(l)
		if strings.Contains(string(c.Document(fileURI(link))), "outside-marker") {
			t.Fatal("default file retrieval crossed the entry directory")
		}
	}
	finish()
	if replacements == 0 {
		t.Skip("filesystem prevented every concurrent symlink replacement")
	}
	t.Logf("exercised %d successful concurrent symlink replacements", replacements)
}

// Both requested and final URIs identify the redirected entry (load.go,
// Loader: a reference resolves to a document "by its retrieval URI (and the
// URI requested, when a redirect led there)").
// Resolving an already loaded document requires no admission or refetch;
// its Source and DocumentURIs continue to name the final retrieval URI.
func TestDocumentRedirectedEntryIsAlreadyIdentified(t *testing.T) {
	const requested = "https://docs.example.test/openapi.json"
	const final = "https://docs.example.test/moved/openapi.json"
	const content = `{"openapi":"3.1.0","paths":{"/p":{"get":{"operationId":"p","parameters":[{"$ref":"https://docs.example.test/openapi.json#/components/parameters/P"}]}}},"components":{"parameters":{"P":{"name":"p","in":"query"}}}}`
	for _, allow := range []bool{true, false} {
		var fetches, admissions atomic.Int32
		l := openapi.Loader{
			Fetch: func(ctx context.Context, uri string) (io.ReadCloser, string, error) {
				fetches.Add(1)
				return io.NopCloser(strings.NewReader(content)), final, nil
			},
			AllowReference: func(from, to string) bool { admissions.Add(1); return allow },
		}
		c := mustLoad(t, &l, requested, nil)
		op := mustOp(t, c, "p")
		if op.Err != nil || fetches.Load() != 1 || admissions.Load() != 0 {
			t.Errorf("allow=%t: error=%v, fetches=%d, admissions=%d; want resolved, 1, 0", allow, op.Err, fetches.Load(), admissions.Load())
		}
		wantStrings(t, "loaded URIs", c.DocumentURIs(), []string{final})
		if !strings.HasPrefix(op.Source, final+"#") || c.Document(op.Source) == nil {
			t.Errorf("operation Source = %q, want a node in final retrieval URI", op.Source)
		}
	}
}
