package openapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Tests for the implementer's points the stage 5 ledger rules under
// "Implementation (5ec6fe5)": SIP1 and SIP5.

// load.go, Loader.AllowReference (de98c7d, SIP1): "A document several
// documents refer to is retrieved when any of them may retrieve it,
// whatever order they are read in, and every reference to it then
// resolves, as to any loaded document." Three documents refer to one
// shared document; the callback lets only b.json retrieve it. The three are
// read in a forced order, the admitting one first or last, by a Fetch that
// returns each only after the one before it has returned, several times
// each (and under -race). With every referrer refused, the shared document
// is never fetched and every reference to it is unresolvable, naming it.
func TestSharedDocumentAnyReferrerMayRetrieve(t *testing.T) {
	const base = "https://docs.example.test/"
	shared := base + "shared.json"
	docs := map[string]string{
		base + "openapi.json": bare31(paramOps(map[string]string{"a": "a.json#/P", "b": "b.json#/P", "c": "c.json#/P"})),
		base + "a.json":       `{"P":{"$ref":"shared.json#/A"}}`,
		base + "b.json":       `{"P":{"$ref":"shared.json#/B"}}`,
		base + "c.json":       `{"P":{"$ref":"shared.json#/C"}}`,
		shared:                `{"A":{"name":"a","in":"query"},"B":{"name":"b","in":"query"},"C":{"name":"c","in":"query"}}`,
	}
	// load reads the referrers in order: each one's Fetch returns only once
	// the one before it in order has returned (or two seconds have passed,
	// should the loader not fetch them together).
	load := func(t *testing.T, order []string, allow func(from, to string) bool) (*openapi.Client, *memFetch) {
		m := newMemFetch(docs)
		done := map[string]chan struct{}{}
		once := map[string]*sync.Once{}
		prev := map[string]string{}
		for i, name := range order {
			done[base+name], once[base+name] = make(chan struct{}), &sync.Once{}
			if i > 0 {
				prev[base+name] = base + order[i-1]
			}
		}
		l := &openapi.Loader{AllowReference: allow, Fetch: func(ctx context.Context, uri string) (io.ReadCloser, string, error) {
			if p, ok := prev[uri]; ok {
				select {
				case <-done[p]:
				case <-ctx.Done():
				case <-time.After(2 * time.Second):
				}
			}
			r, final, err := m.fetch(ctx, uri)
			if o, ok := once[uri]; ok {
				o.Do(func() { close(done[uri]) })
			}
			return r, final, err
		}}
		return mustLoad(t, l, base+"openapi.json", nil), m
	}

	onlyB := func(from, to string) bool { return to != shared || from == base+"b.json" }
	for _, order := range [][]string{{"b.json", "a.json", "c.json"}, {"a.json", "c.json", "b.json"}, {"c.json", "b.json", "a.json"}} {
		t.Run(strings.Join(order, ","), func(t *testing.T) {
			for range 5 {
				c, m := load(t, order, onlyB)
				for key, want := range map[string]string{"a": shared + "#/A", "b": shared + "#/B", "c": shared + "#/C"} {
					op := mustOp(t, c, key)
					if op.Err != nil || len(op.Params) != 1 || op.Params[0].Source != want {
						t.Errorf("%s = Err %v, Params %+v; want its parameter at %s", key, op.Err, op.Params, want)
					}
				}
				if n := m.callsTo(shared); n != 1 {
					t.Errorf("shared.json fetched %d times, want once", n)
				}
			}
		})
	}

	t.Run("every referrer refused", func(t *testing.T) {
		for _, order := range [][]string{{"a.json", "b.json", "c.json"}, {"c.json", "b.json", "a.json"}} {
			c, m := load(t, order, func(from, to string) bool { return to != shared })
			for _, key := range []string{"a", "b", "c"} {
				refusedRef(t, c, key, shared)
			}
			if n := m.callsTo(shared); n != 0 {
				t.Errorf("shared.json fetched %d times; no referrer may retrieve it", n)
			}
			if c.Document(shared) != nil {
				t.Errorf("Document(shared.json) is not nil")
			}
		}
	})
}

// fuzzBases are document URIs of several shapes: RFC 3986 section 5.4's
// base, a root, a directory, a file in a directory, one with a port, one
// with a query, a percent-encoded path, a host with no path, and a file
// URL.
var fuzzBases = []string{
	"http://a/b/c/d;p?q",
	"https://h.example.test/",
	"https://h.example.test/dir/",
	"https://h.example.test/a/b/openapi.json",
	"http://h.example.test:8080/x/y/z.yaml",
	"https://h.example.test/a/b/openapi.json?v=1",
	"https://h.example.test/a%20b/c%2Fd/openapi.json",
	"https://h.example.test",
	"file:///srv/specs/v1/openapi.json",
}

// rfc3986Examples are RFC 3986 section 5.4.1's normal and 5.4.2's abnormal
// examples, against the base "http://a/b/c/d;p?q".
var rfc3986Examples = []string{
	"g:h", "g", "./g", "g/", "/g", "//g", "?y", "g?y", "#s", "g#s", "g?y#s", ";x", "g;x", "g;x?y#s", "", ".", "./",
	"..", "../", "../g", "../..", "../../", "../../g",
	"../../../g", "../../../../g", "/./g", "/../g", "g.", ".g", "g..", "..g", "./../g", "./g/.", "g/./h", "g/../h",
	"g;x=1/./y", "g;x=1/../y", "g?y/./x", "g?y/../x", "g#s/./x", "g#s/../x", "http:g",
}

// SIP5 (stage 5 ledger, "Implementation (5ec6fe5)": "the fast path for
// plain relative references gets a differential test against net/url's
// ResolveReference (P4: mirrors of a standard parser match it exactly)");
// load.go, Loader: references "resolve against each document's base"
// (RFC 3986 section 5.2). A document whose one parameter is a Reference
// Object to a generated reference is loaded with a Fetch that records what
// is requested and an AllowReference admitting everything: the one URI
// requested beside the entry is base.ResolveReference(ref) without its
// fragment, exactly as net/url writes it, and nothing is requested when that
// is the entry's own URI. A result with leading or trailing whitespace is
// requested not at all, and the operation's Err wraps ErrUnresolved (SQ14;
// load.go, Loader: "A reference to a URI with userinfo or with leading or
// trailing whitespace ... is unresolvable and never fetched"). References
// net/url cannot parse, or whose result another rule decides (a scheme
// other than http, https and file, an http URI without a host, userinfo, a
// file URL naming a host: SQ9), are left out.
func FuzzRelativeReferences(f *testing.F) {
	for _, ref := range rfc3986Examples {
		f.Add(uint8(0), ref)
	}
	// The input that found SQ14's case, and others like it.
	f.Add(uint8(8), "? ")
	f.Add(uint8(3), "x.json?a ")
	f.Add(uint8(0), "g? y")
	for i := range fuzzBases {
		for _, ref := range []string{"g", "../x.json", "./a//b/../c.json", "%2e%2e/x", "a%2Fb/c", "g%20h", "..//g", "/a/b/../../..",
			"g?#s", "?", "//other.example.test/x.json", "x.json?v=2#/P", "../../../../../x", "é.json", "a b.json", ";p/../q"} {
			f.Add(uint8(i), ref)
		}
	}
	f.Fuzz(func(t *testing.T, b uint8, ref string) {
		baseURI := fuzzBases[int(b)%len(fuzzBases)]
		base, err := url.Parse(baseURI)
		if err != nil {
			t.Fatal(err)
		}
		r, err := url.Parse(ref)
		if err != nil || len(ref) > 512 || !utf8.ValidString(ref) {
			return
		}
		want := base.ResolveReference(r)
		want.Fragment, want.RawFragment = "", ""
		switch {
		case want.Scheme != "http" && want.Scheme != "https" && want.Scheme != "file",
			want.Scheme != "file" && want.Host == "",
			want.Opaque != "",
			want.User != nil,
			want.Scheme == "file" && want.Host != "" && want.Host != "localhost":
			return
		}
		wantURI := want.String()
		blank := strings.TrimSpace(wantURI) != wantURI // SQ14: unresolvable, never fetched

		var mu sync.Mutex
		var requested []string
		l := &openapi.Loader{
			AllowReference: func(from, to string) bool { return true },
			Fetch: func(ctx context.Context, uri string) (io.ReadCloser, string, error) {
				content := `{"P":{"name":"p","in":"query"}}`
				if uri == baseURI {
					content = bare31(`"/x":{"get":{"operationId":"x","parameters":[{"$ref":` + jsonString(ref) + `}]}}`)
				} else {
					mu.Lock()
					requested = append(requested, uri)
					mu.Unlock()
				}
				return io.NopCloser(strings.NewReader(content)), uri, nil
			},
		}
		c, err := l.Load(context.Background(), baseURI, nil)
		if err != nil {
			t.Fatalf("Load(%q) with $ref %q: %v", baseURI, ref, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if blank {
			op, err := c.Operation("x")
			if len(requested) != 0 || err != nil || !errors.Is(op.Err, openapi.ErrUnresolved) {
				t.Fatalf("base %q, $ref %q resolves to %q, with surrounding whitespace: requested %q, Operation %v; want nothing requested and an Err wrapping ErrUnresolved",
					baseURI, ref, wantURI, requested, err)
			}
			return
		}
		if wantURI == baseURI {
			if len(requested) != 0 {
				t.Fatalf("base %q, $ref %q: requested %q; the reference names the entry itself", baseURI, ref, requested)
			}
			return
		}
		if len(requested) != 1 || requested[0] != wantURI {
			op, _ := c.Operation("x")
			var opErr error
			if op != nil {
				opErr = op.Err
			}
			t.Fatalf("base %q, $ref %q: requested %q, want [%q] (net/url's ResolveReference); Err %v", baseURI, ref, requested, wantURI, opErr)
		}
	})
}

// jsonString writes s as a JSON string.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
