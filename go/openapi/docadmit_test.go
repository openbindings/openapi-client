package openapi_test

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Admission and retrieval of referenced documents (stage 5): the default
// boundary, Origins, AllowReference, redirect hops, Fetch, MaxBytes and the
// context, stated in load.go, Loader. Every refusal disables only what
// reaches the refused document, and its Err wraps ErrUnresolved and an
// error naming the refused URI (errors.go).

// refusedRef checks that the operation named key cannot be called because
// its reference was refused: its Err wraps ErrUnresolved and names uri.
func refusedRef(t testing.TB, c *openapi.Client, key, uri string) {
	t.Helper()
	op := mustOp(t, c, key)
	if op.Err == nil || !errors.Is(op.Err, openapi.ErrUnresolved) {
		t.Errorf("%s Err = %v, want a refusal wrapping ErrUnresolved", key, op.Err)
		return
	}
	if !strings.Contains(op.Err.Error(), uri) {
		t.Errorf("%s Err %q does not name the refused URI %s", key, op.Err, uri)
	}
}

// admittedRef checks that the operation named key has no Err.
func admittedRef(t testing.TB, c *openapi.Client, key string) {
	t.Helper()
	if op := mustOp(t, c, key); op.Err != nil {
		t.Errorf("%s Err = %v, want it admitted", key, op.Err)
	}
}

// paramOps writes one operation per reference, named by the map's key, each
// whose only parameter is a Reference Object to the reference.
func paramOps(refs map[string]string) string {
	var parts []string
	for _, key := range slices.Sorted(maps.Keys(refs)) {
		parts = append(parts, `"/`+key+`":{"get":{"operationId":"`+key+`","parameters":[{"$ref":"`+refs[key]+`"}]}}`)
	}
	return strings.Join(parts, ",")
}

// load.go, Loader.AllowReference: "With nil, http and https references may
// reach the entry document's original origin and Origins"; Loader.Origins:
// "Origins lists further origins, as "https://host" or "https://host:port",
// whose documents references may reach. A reference to any other origin
// ... disables only what reaches it." Each site is its own origin; one
// refused receives no request.
func TestDefaultBoundaryAndOrigins(t *testing.T) {
	a, b, cc := newSite(t), newSite(t), newSite(t)
	a.put("/openapi.json", entry31(paramOps(map[string]string{"same": "p.json#/P", "other": b.URL + "/p.json#/P"})))
	a.put("/p.json", `{"P":{"name":"p","in":"query"}}`)
	b.put("/p.json", `{"P":{"$ref":"q.json#/Q"},"R":{"$ref":"`+cc.URL+`/p.json#/P"}}`)
	b.put("/q.json", `{"Q":{"name":"q","in":"query"}}`)
	cc.put("/p.json", `{"P":{"name":"c","in":"query"}}`)

	t.Run("default", func(t *testing.T) {
		c := mustLoad(t, nil, a.uri("/openapi.json"), nil)
		admittedRef(t, c, "same")
		refusedRef(t, c, "other", b.URL+"/p.json")
		if n := b.total(); n != 0 {
			t.Errorf("another origin received %d requests", n)
		}
	})
	t.Run("Origins", func(t *testing.T) {
		before := cc.total()
		a.put("/openapi2.json", entry31(paramOps(map[string]string{"other": b.URL + "/p.json#/P", "third": b.URL + "/p.json#/R"})))
		c := mustLoad(t, &openapi.Loader{Origins: []string{b.URL}}, a.uri("/openapi2.json"), nil)
		// b's document reaches its own q.json, resolved against it.
		admittedRef(t, c, "other")
		if n := b.count("/q.json"); n != 1 {
			t.Errorf("b's q.json fetched %d times, want once", n)
		}
		// A third origin is still outside.
		refusedRef(t, c, "third", cc.URL+"/p.json")
		if n := cc.total() - before; n != 0 {
			t.Errorf("a third origin received %d requests", n)
		}
	})
}

// load.go, Loader.AllowReference: references may reach "the entry
// document's original origin": when the entry's own retrieval is
// redirected to another origin, that origin is the entry's base (Fetch:
// the final URI "becomes that document's base") but not its original
// origin, so a relative reference resolved against it is refused unless
// Origins lists it, and the original origin stays admitted.
func TestEntryOriginalOrigin(t *testing.T) {
	a, b := newSite(t), newSite(t)
	a.handle("/openapi.json", redirect(http.StatusFound, b.uri("/spec/openapi.json")))
	a.put("/y.json", `{"Y":{"name":"y","in":"query"}}`)
	b.put("/spec/openapi.json", entry31(paramOps(map[string]string{"relative": "x.json#/X", "original": a.URL + "/y.json#/Y"})))
	b.put("/spec/x.json", `{"X":{"name":"x","in":"query"}}`)

	c := mustLoad(t, nil, a.uri("/openapi.json"), nil)
	if got := c.DocumentURIs()[0]; got != b.uri("/spec/openapi.json") {
		t.Errorf("the entry's retrieval URI is %q, want the final %q", got, b.uri("/spec/openapi.json"))
	}
	refusedRef(t, c, "relative", b.uri("/spec/x.json"))
	admittedRef(t, c, "original")
	if n := b.count("/spec/x.json"); n != 0 {
		t.Errorf("x.json fetched %d times from the redirect's origin", n)
	}

	c = mustLoad(t, &openapi.Loader{Origins: []string{b.URL}}, a.uri("/openapi.json"), nil)
	admittedRef(t, c, "relative")
	admittedRef(t, c, "original")
}

// load.go, Loader.AllowReference: "For a file entry, references may reach
// only files under the entry file's directory"; with nil, "http and https
// references may reach the entry document's original origin and Origins",
// so an http entry cannot reach a file, nor a file entry an http document.
// A callback that admits them replaces that boundary.
func TestHTTPAndFileCrossings(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "local.json")
	if err := os.WriteFile(local, []byte(`{"P":{"name":"local","in":"query"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	localURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(local)}).String()
	s := newSite(t)
	s.put("/openapi.json", entry31(paramOps(map[string]string{"file": localURI + "#/P"})))
	s.put("/p.json", `{"P":{"name":"remote","in":"query"}}`)
	entryPath := filepath.Join(dir, "openapi.json")
	if err := os.WriteFile(entryPath, []byte(strings.ReplaceAll(entry31(paramOps(map[string]string{"http": s.URL + "/p.json#/P"})), "@SELF@", s.URL)), 0o600); err != nil {
		t.Fatal(err)
	}

	c := mustLoad(t, nil, s.uri("/openapi.json"), nil)
	refusedRef(t, c, "file", localURI)
	c = mustLoad(t, nil, entryPath, nil)
	refusedRef(t, c, "http", s.uri("/p.json"))
	if n := s.count("/p.json"); n != 0 {
		t.Errorf("a file entry's reference reached the http site %d times", n)
	}

	all := &openapi.Loader{AllowReference: func(from, to string) bool { return true }}
	c = mustLoad(t, all, s.uri("/openapi.json"), nil)
	admittedRef(t, c, "file")
	c = mustLoad(t, all, entryPath, nil)
	admittedRef(t, c, "http")
}

// call is one AllowReference call.
type call struct{ from, to string }

// recorder is an AllowReference that records its calls and admits what
// admit admits.
type recorder struct {
	mu    sync.Mutex
	calls []call
	admit func(from, to string) bool
}

func (r *recorder) allow(from, to string) bool {
	r.mu.Lock()
	r.calls = append(r.calls, call{from, to})
	r.mu.Unlock()
	return r.admit(from, to)
}

func (r *recorder) list() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// load.go, Loader.AllowReference: "AllowReference, when set, decides
// whether one document may retrieve another. from is the absolute retrieval
// URI of the referring document; to is the resolved absolute URI requested
// ... Returning false disables only the reference that needs to cross that
// boundary ... A callback replaces the default boundary: it can admit an
// arbitrary trusted source graph, or restrict one further. It does not
// apply to fragment-only references within a document." Nor is it asked
// about a URI a loaded document identifies ("Only a URI no loaded document
// identifies is admitted").
func TestAllowReference(t *testing.T) {
	a, b := newSite(t), newSite(t)
	a.put("/openapi.json", entry31(paramOps(map[string]string{
		"nested":  "b.json#/P",
		"other":   b.URL + "/p.json#/P",
		"blocked": "blocked.json#/P",
		"local":   "#/components/parameters/L",
		"self":    a.URL + "/openapi.json#/components/parameters/L",
	}), `"components":{"parameters":{"L":{"name":"l","in":"query"}}}`))
	a.put("/b.json", `{"P":{"$ref":"#/Mid"},"Mid":{"$ref":"d.json#/P"}}`)
	a.put("/d.json", `{"P":{"name":"d","in":"query"}}`)
	a.put("/blocked.json", `{"P":{"name":"x","in":"query"}}`)
	b.put("/p.json", `{"P":{"name":"o","in":"query"}}`)
	r := &recorder{admit: func(from, to string) bool { return !strings.HasSuffix(to, "/blocked.json") }}
	c := mustLoad(t, &openapi.Loader{AllowReference: r.allow}, a.uri("/openapi.json"), nil)

	admittedRef(t, c, "nested")
	admittedRef(t, c, "other") // another origin, admitted by the callback
	refusedRef(t, c, "blocked", a.uri("/blocked.json"))
	admittedRef(t, c, "local")
	admittedRef(t, c, "self")
	if n := a.count("/blocked.json"); n != 0 {
		t.Errorf("a refused document was fetched %d times", n)
	}
	calls := r.list()
	entry := a.uri("/openapi.json")
	for _, want := range []call{{entry, a.uri("/b.json")}, {entry, b.uri("/p.json")}, {entry, a.uri("/blocked.json")}, {a.uri("/b.json"), a.uri("/d.json")}} {
		if !slices.Contains(calls, want) {
			t.Errorf("AllowReference(%q, %q) was not called; calls %q", want.from, want.to, calls)
		}
	}
	for _, cl := range calls {
		if strings.Contains(cl.to, "#") {
			t.Errorf("AllowReference was given %q, with a fragment; to is the URI requested", cl.to)
		}
		if cl.to == entry {
			t.Errorf("AllowReference was asked about the entry, which is loaded: %q", cl)
		}
	}
	distinct := map[call]bool{}
	for _, cl := range calls {
		distinct[cl] = true
	}
	if len(distinct) != 4 {
		t.Errorf("AllowReference was asked about %d pairs, want 4 (one for each document requested): %q", len(distinct), calls)
	}
}

// load.go, Loader.Origins: "A Loader that sets both Origins and
// AllowReference is refused by its Load and Parse."
func TestOriginsWithAllowReferenceRefused(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(`"/x":{"get":{"operationId":"x"}}`))
	l := &openapi.Loader{Origins: []string{"https://other.example.test"}, AllowReference: func(from, to string) bool { return true }}
	if c, err := l.Load(t.Context(), s.uri("/openapi.json"), nil); err == nil || c != nil {
		t.Errorf("Load = %v, %v; want a refusal", c, err)
	}
	if c, err := l.Parse(t.Context(), []byte(bare31(`"/x":{"get":{}}`)), testDocURI, nil); err == nil || c != nil {
		t.Errorf("Parse = %v, %v; want a refusal", c, err)
	}
}

// load.go, Loader.AllowReference: "The default retrieval checks every
// redirect hop"; Fetch: the final URI "becomes that document's base";
// AllowReference's to is "the resolved absolute URI requested, or a
// redirect hop/final URI. It is called before the fetch or hop". A hop to
// another origin is refused, and that origin receives nothing, even when a
// later hop would come back; Origins admits it; a document's base and
// retrieval URI are where it was finally retrieved from.
func TestRedirectHopsChecked(t *testing.T) {
	a, cc := newSite(t), newSite(t)
	a.put("/openapi.json", entry31(paramOps(map[string]string{"away": "b.json#/P", "back": "r1.json#/P", "same": "moved.json#/P"})))
	a.handle("/b.json", redirect(http.StatusFound, cc.uri("/b.json")))
	a.handle("/r1.json", redirect(http.StatusFound, a.uri("/r2.json")))
	a.handle("/r2.json", redirect(http.StatusTemporaryRedirect, cc.uri("/x.json")))
	cc.handle("/x.json", redirect(http.StatusFound, a.uri("/final.json")))
	a.put("/final.json", `{"P":{"name":"f","in":"query"}}`)
	a.handle("/moved.json", redirect(http.StatusMovedPermanently, a.uri("/new/moved.json")))
	a.put("/new/moved.json", `{"P":{"$ref":"near.json#/N"}}`)
	a.put("/new/near.json", `{"N":{"name":"n","in":"query"}}`)
	cc.put("/b.json", `{"P":{"$ref":"c2.json#/P"}}`)
	cc.put("/c2.json", `{"P":{"name":"c","in":"query"}}`)

	c := mustLoad(t, nil, a.uri("/openapi.json"), nil)
	refusedRef(t, c, "away", cc.uri("/b.json"))
	refusedRef(t, c, "back", cc.uri("/x.json"))
	admittedRef(t, c, "same")
	if n := cc.total(); n != 0 {
		t.Errorf("the other origin received %d requests", n)
	}
	if n := a.count("/final.json"); n != 0 {
		t.Errorf("final.json, behind a refused hop, was fetched %d times", n)
	}
	if op := mustOp(t, c, "same"); op.Err == nil && op.Params[0].Source != a.uri("/new/near.json#/N") {
		t.Errorf("same's parameter Source = %q; near.json resolves against the redirect's final URI", op.Params[0].Source)
	}
	if !slices.Contains(c.DocumentURIs(), a.uri("/new/moved.json")) || slices.Contains(c.DocumentURIs(), a.uri("/moved.json")) {
		t.Errorf("DocumentURIs = %q; want the final URI new/moved.json, not moved.json", c.DocumentURIs())
	}

	c = mustLoad(t, &openapi.Loader{Origins: []string{cc.URL}}, a.uri("/openapi.json"), nil)
	admittedRef(t, c, "away")
	admittedRef(t, c, "back")
	if op := mustOp(t, c, "away"); op.Err == nil && op.Params[0].Source != cc.uri("/c2.json#/P") {
		t.Errorf("away's parameter Source = %q; c2.json resolves against the final %s", op.Params[0].Source, cc.uri("/b.json"))
	}

	// The callback is asked about each hop.
	r := &recorder{admit: func(from, to string) bool { return !strings.HasPrefix(to, cc.URL) }}
	c = mustLoad(t, &openapi.Loader{AllowReference: r.allow}, a.uri("/openapi.json"), nil)
	refusedRef(t, c, "away", cc.uri("/b.json"))
	refusedRef(t, c, "back", cc.uri("/x.json"))
	calls := r.list()
	entry := a.uri("/openapi.json")
	for _, want := range []call{{entry, a.uri("/b.json")}, {entry, cc.uri("/b.json")}, {entry, a.uri("/r2.json")}, {entry, cc.uri("/x.json")}, {entry, a.uri("/new/moved.json")}} {
		if !slices.Contains(calls, want) {
			t.Errorf("AllowReference(%q, %q) was not called; calls %q", want.from, want.to, calls)
		}
	}
}

// load.go, Loader.Fetch: "Fetch, if set, retrieves each document the loader
// needs in place of the default ... It returns the content, which the loader
// closes, and the URI it was finally retrieved from after any redirects,
// which becomes that document's base; an empty final means uri ... The
// loader checks the requested URI before Fetch and checks the final URI it
// returns." Fetch is called once for each document, the entry included.
func TestFetch(t *testing.T) {
	const base = "https://docs.example.test/"
	m := newMemFetch(map[string]string{
		base + "openapi.json": bare31(paramOps(map[string]string{"moved": "b.json#/P", "plain": "plain.json#/P", "evil": "e.json#/P", "twice": "b.json#/P2"})),
		base + "b.json":       `{"P":{"$ref":"c.json#/C"},"P2":{"name":"p2","in":"query"}}`,
		base + "moved/c.json": `{"C":{"name":"c","in":"query"}}`,
		base + "plain.json":   `{"P":{"name":"plain","in":"query"}}`,
		base + "e.json":       `{"P":{"name":"e","in":"query"}}`,
	})
	m.finals[base+"b.json"] = base + "moved/b.json"
	m.finals[base+"e.json"] = "https://evil.example.test/e.json"
	c := mustLoad(t, &openapi.Loader{Fetch: m.fetch}, base+"openapi.json", nil)

	admittedRef(t, c, "moved")
	admittedRef(t, c, "plain")
	admittedRef(t, c, "twice")
	refusedRef(t, c, "evil", "https://evil.example.test/e.json")
	if op := mustOp(t, c, "moved"); op.Err == nil && op.Params[0].Source != base+"moved/c.json#/C" {
		t.Errorf("moved's parameter Source = %q, want it resolved against the final URI", op.Params[0].Source)
	}
	if op := mustOp(t, c, "twice"); op.Err == nil && op.Params[0].Source != base+"moved/b.json#/P2" {
		t.Errorf("twice's parameter Source = %q, want the final URI's document", op.Params[0].Source)
	}
	wantStrings(t, "DocumentURIs", c.DocumentURIs(), []string{base + "openapi.json", base + "moved/b.json", base + "moved/c.json", base + "plain.json"})
	if c.Document("https://evil.example.test/e.json") != nil || c.Document(base+"e.json") != nil {
		t.Errorf("a document whose final URI was refused is loaded")
	}
	for _, uri := range []string{base + "openapi.json", base + "b.json", base + "moved/c.json", base + "plain.json", base + "e.json"} {
		if n := m.callsTo(uri); n != 1 {
			t.Errorf("Fetch(%s) called %d times, want once", uri, n)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed != m.opened {
		t.Errorf("the loader closed %d of %d contents", m.closed, m.opened)
	}
}

// load.go, Loader.MaxBytes: "MaxBytes bounds the bytes one load retrieves,
// all documents together ... Past it, an entry document fails the load with
// an error wrapping *http.MaxBytesError, and a referenced document disables
// what reaches it." The disabled part's Err wraps ErrUnresolved and the
// retrieval error (errors.go), here the *http.MaxBytesError. The documents
// form a chain, so the order in which they count is fixed.
func TestMaxBytesAcrossDocuments(t *testing.T) {
	entry := bare31(paramOps(map[string]string{"near": "b.json#/P", "far": "b.json#/Q"}))
	b := `{"P":{"name":"p","in":"query"},"Q":{"$ref":"c.json#/Q"}}`
	cDoc := `{"Q":{"name":"q","in":"query"}}`
	total := int64(len(entry) + len(b) + len(cDoc))
	const base = "https://docs.example.test/"
	docs := map[string]string{base + "openapi.json": entry, base + "b.json": b, base + "c.json": cDoc}

	s := newSite(t)
	for uri, content := range docs {
		s.put(strings.TrimPrefix(uri, strings.TrimSuffix(base, "/")), content)
	}
	for _, how := range []string{"Fetch", "http"} {
		t.Run(how, func(t *testing.T) {
			load := func(max int64) (*openapi.Client, error) {
				l := &openapi.Loader{MaxBytes: max}
				uri := s.uri("/openapi.json")
				if how == "Fetch" {
					l.Fetch, uri = newMemFetch(docs).fetch, base+"openapi.json"
				}
				return l.Load(t.Context(), uri, nil)
			}
			c, err := load(total)
			if err != nil {
				t.Fatalf("MaxBytes = all the documents: %v", err)
			}
			admittedRef(t, c, "near")
			admittedRef(t, c, "far")

			c, err = load(total - 1)
			if err != nil {
				t.Fatalf("MaxBytes one short: %v; a referenced document disables only what reaches it", err)
			}
			admittedRef(t, c, "near")
			far := mustOp(t, c, "far")
			var tooBig *http.MaxBytesError
			if !errors.Is(far.Err, openapi.ErrUnresolved) || !errors.As(far.Err, &tooBig) {
				t.Errorf("far Err = %v, want one wrapping ErrUnresolved and *http.MaxBytesError", far.Err)
			}

			c, err = load(int64(len(entry)) - 1)
			if err == nil || c != nil || !errors.As(err, &tooBig) {
				t.Errorf("an entry over MaxBytes: %v, %v; want an error wrapping *http.MaxBytesError", c, err)
			}
		})
	}
}

// load.go, Load: "ctx bounds the whole load, reading and parsing included":
// a referenced document whose server never answers, or whose Fetch waits,
// ends the load with the context's error when the context ends.
func TestContextBoundsTheWholeLoad(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		s := newSite(t)
		s.put("/openapi.json", entry31(paramOps(map[string]string{"slow": "slow.json#/P"})))
		s.handle("/slow.json", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		c, err := openapi.Load(ctx, s.uri("/openapi.json"), nil)
		if err == nil || c != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Load = %v, %v; want the context's error", c, err)
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("Load returned %v after its deadline", d)
		}
	})
	t.Run("Fetch", func(t *testing.T) {
		const base = "https://docs.example.test/"
		m := newMemFetch(map[string]string{base + "openapi.json": bare31(paramOps(map[string]string{"slow": "slow.json#/P"}))})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		l := &openapi.Loader{Fetch: func(fctx context.Context, uri string) (io.ReadCloser, string, error) {
			if uri != base+"slow.json" {
				return m.fetch(fctx, uri)
			}
			cancel()
			<-fctx.Done()
			return nil, "", fctx.Err()
		}}
		c, err := l.Load(ctx, base+"openapi.json", nil)
		if err == nil || c != nil || !errors.Is(err, context.Canceled) {
			t.Errorf("Load = %v, %v; want the context's error", c, err)
		}
	})
}

// client.go, Options.HTTPClient: "HTTPClient sends every request, and
// fetches documents while loading": referenced documents too, each request
// outside any operation (OperationFromContext is nil).
func TestReferencedDocumentsUseHTTPClient(t *testing.T) {
	s := newSite(t)
	s.put("/openapi.json", entry31(paramOps(map[string]string{"p": "p.json#/P"})))
	s.put("/p.json", `{"P":{"$ref":"q.json#/Q"}}`)
	s.put("/q.json", `{"Q":{"name":"q","in":"query"}}`)
	ct := &countingTransport{}
	c := mustLoad(t, nil, s.uri("/openapi.json"), &openapi.Options{HTTPClient: &http.Client{Transport: ct}})
	admittedRef(t, c, "p")
	if n := ct.count(); n != 3 {
		t.Errorf("Options.HTTPClient carried %d requests, want 3", n)
	}
	for _, op := range ct.snapshot() {
		if op != nil {
			t.Errorf("OperationFromContext reported %s for a document fetch", op.Key)
		}
	}
}

// fileURI is the file URL of path.
func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

// load.go, Loader.AllowReference: "For a file entry, references may reach
// only files under the entry file's directory, after cleaning paths and
// resolving symlinks; Origins does not enlarge that file boundary" (RFC 8089
// file URLs; RFC 3986 section 5.2.4 removes dot segments). Inside: a
// subdirectory, a path that leaves and comes back, a symlink to a file
// inside. Outside: a sibling directory, a symlink to a file outside, a
// symlinked directory outside, a percent-encoded "..", an absolute file URL
// outside. An entry reached through a symlinked directory has the real
// directory as its boundary.
func TestFileBoundary(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		t.Helper()
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlinks: %v", err)
		}
	}
	param := `{"P":{"name":"p","in":"query"},"P2":{"name":"p2","in":"query"}}`
	write(filepath.Join(root, "schemas", "a.json"), param)
	write(filepath.Join(root, "schemas", "real.json"), param)
	write(filepath.Join(tmp, "sibling", "b.json"), param)
	write(filepath.Join(tmp, "secret.json"), param)
	write(filepath.Join(tmp, "outside", "c.json"), param)
	link(filepath.Join(tmp, "secret.json"), filepath.Join(root, "escape.json"))
	link(filepath.Join(root, "schemas", "real.json"), filepath.Join(root, "inner.json"))
	link(filepath.Join(tmp, "outside"), filepath.Join(root, "linkdir"))
	link(root, filepath.Join(tmp, "link"))
	refs := map[string]string{
		"inside":       "schemas/a.json#/P",
		"backInside":   "schemas/../schemas/a.json#/P2",
		"innerLink":    "inner.json#/P",
		"sibling":      "../sibling/b.json#/P",
		"escapeLink":   "escape.json#/P",
		"linkedDir":    "linkdir/c.json#/P",
		"encodedDots":  "%2e%2e/sibling/b.json#/P",
		"absoluteFile": fileURI(filepath.Join(tmp, "secret.json")) + "#/P",
	}
	write(filepath.Join(root, "openapi.json"), bare31(paramOps(refs)))

	for _, l := range []*openapi.Loader{nil, {Origins: []string{"https://other.example.test"}}} {
		c := mustLoad(t, l, filepath.Join(root, "openapi.json"), nil)
		for _, key := range []string{"inside", "backInside", "innerLink"} {
			admittedRef(t, c, key)
		}
		for _, key := range []string{"sibling", "escapeLink", "linkedDir", "encodedDots", "absoluteFile"} {
			op := mustOp(t, c, key)
			if !errors.Is(op.Err, openapi.ErrUnresolved) {
				t.Errorf("%s Err = %v, want a refusal wrapping ErrUnresolved", key, op.Err)
			}
		}
		refusedRef(t, c, "sibling", "sibling/b.json")
		var loaded []string
		for _, uri := range c.DocumentURIs()[1:] {
			if sameFile(t, uri, filepath.Join(root, "schemas", "a.json")) || sameFile(t, uri, filepath.Join(root, "schemas", "real.json")) {
				loaded = append(loaded, uri)
			}
		}
		if len(loaded) != len(c.DocumentURIs())-1 {
			t.Errorf("DocumentURIs = %q; want only files under %s", c.DocumentURIs(), root)
		}
	}

	// Through the symlinked directory: the boundary is the real one.
	write(filepath.Join(root, "via.json"), bare31(paramOps(map[string]string{"inside": "schemas/a.json#/P", "realPath": "../root/schemas/a.json#/P2", "sibling": "../sibling/b.json#/P"})))
	c := mustLoad(t, nil, filepath.Join(tmp, "link", "via.json"), nil)
	admittedRef(t, c, "inside")
	admittedRef(t, c, "realPath")
	refusedRef(t, c, "sibling", "sibling/b.json")
}
