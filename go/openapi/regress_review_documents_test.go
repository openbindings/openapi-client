package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regressions from the independent review of 4058c2c. Loader reference
// isolation and JSON Schema 2020-12 section 8.2.1 govern these cases.
// zzFS is an in-memory Fetch that records every URI requested.
type zzFS struct {
	mu    sync.Mutex
	docs  map[string]string
	calls []string
}

func (f *zzFS) fetch(ctx context.Context, uri string) (io.ReadCloser, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, uri)
	f.mu.Unlock()
	if s, ok := f.docs[uri]; ok {
		return io.NopCloser(strings.NewReader(s)), "", nil
	}
	return nil, "", fmt.Errorf("zz: no document %s", uri)
}

func (f *zzFS) fetched(uri string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == uri {
			return true
		}
	}
	return false
}

func TestReviewRepairOneLetterSchemeReference(t *testing.T) {
	fs := &zzFS{docs: map[string]string{
		"x://example.test/openapi.json": `{"openapi":"3.1.0","paths":{},"components":{"schemas":{"A":{"$ref":"other.json"}}}}`,
		"x://example.test/other.json":   `{"type":"string"}`,
	}}
	var asked []string
	var mu sync.Mutex
	l := openapi.Loader{Fetch: fs.fetch, AllowReference: func(from, to string) bool {
		mu.Lock()
		asked = append(asked, from+" -> "+to)
		mu.Unlock()
		return true
	}}
	c, err := l.Load(t.Context(), "x://example.test/openapi.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AllowReference calls: %q; Fetch calls: %q; DocumentURIs: %q", asked, fs.calls, c.DocumentURIs())
	s, err := c.Schema("x://example.test/openapi.json#/components/schemas/A")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := s.References()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Err != nil {
		t.Fatalf("A's reference = %+v, want the admitted x://example.test/other.json to resolve", refs)
	}
}

func TestReviewRepairVersionlessSelfIsNotABase(t *testing.T) {
	fs := &zzFS{docs: map[string]string{
		"https://h.test/api/openapi.json": `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{"P":{"$ref":"models.json#/Pet"}}}}`,
		"https://h.test/api/models.json":  `{"$self":"https://elsewhere.test/x/","Pet":{"type":"object","properties":{"c":{"$ref":"c.json"}}}}`,
		"https://h.test/api/c.json":       `{"type":"string"}`,
		"https://elsewhere.test/x/c.json": `{"type":"integer"}`,
	}}
	l := openapi.Loader{Fetch: fs.fetch, AllowReference: func(from, to string) bool { return true }}
	c, err := l.Load(t.Context(), "https://h.test/api/openapi.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Fetch calls: %q", fs.calls)
	s, err := c.Schema("https://h.test/api/models.json#/Pet")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Base(); got != "https://h.test/api/models.json" {
		t.Errorf("Pet Base = %q, want the retrieval URI https://h.test/api/models.json", got)
	}
	refs, err := s.References()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		t.Logf("ref At=%s Value=%s URI=%s Err=%v", r.At, r.Value, r.URI, r.Err)
		if r.URI != "https://h.test/api/c.json" {
			t.Errorf("ref %s URI = %q, want https://h.test/api/c.json", r.Value, r.URI)
		}
	}
	if fs.fetched("https://elsewhere.test/x/c.json") {
		t.Errorf("fetched https://elsewhere.test/x/c.json, a URI resolved against a non-OpenAPI $self member")
	}
	if got := c.Document("https://elsewhere.test/x/"); got != nil {
		t.Errorf("Document($self of a versionless document) = %d bytes, want nil", len(got))
	}
}

func TestReviewRepairFragmentIDBreaksLocalReferences(t *testing.T) {
	const uri = "https://h.test/openapi.json"
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"paths":{"/p":{"post":{"operationId":"p","requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/B"}}}},"responses":{"200":{"description":"ok"}}}}},
	"components":{"schemas":{"A":{"$id":"#foo","type":"string"},"B":{"type":"object"}}}}`
	l := openapi.Loader{}
	c, err := l.Parse(t.Context(), []byte(doc), uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.Operation("p")
	if err != nil {
		t.Fatal(err)
	}
	if op.Err != nil {
		t.Errorf("operation Err = %v", op.Err)
	}
	if op.Body != nil {
		for _, m := range op.Body.Media {
			if m.Schema == nil {
				continue
			}
			refs, err := m.Schema.References()
			if err != nil {
				t.Errorf("body schema References: %v", err)
			}
			for _, r := range refs {
				if r.Err != nil {
					t.Errorf("local ref %q: %v", r.Value, r.Err)
				}
			}
		}
	}
	if _, err := c.Schema(uri + "#/components/schemas/B"); err != nil {
		t.Errorf("Schema(#/components/schemas/B): %v", err)
	}
	// Control: without the invalid $id everything resolves.
	ctl, err := l.Parse(t.Context(), []byte(strings.Replace(doc, `"$id":"#foo",`, ``, 1)), uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.Schema(uri + "#/components/schemas/B"); err != nil {
		t.Errorf("control: %v", err)
	}
}

func TestReviewRepairFragmentIDBreaksReferenceObjects(t *testing.T) {
	const uri = "https://h.test/openapi.json"
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"paths":{"/p":{"get":{"operationId":"p","parameters":[{"$ref":"#/components/parameters/Q"}],"responses":{"200":{"description":"ok"}}}}},
	"components":{"parameters":{"Q":{"name":"q","in":"query","schema":{"type":"string"}}},"schemas":{"A":{"$id":"#foo","type":"string"}}}}`
	for _, d := range []string{doc, strings.Replace(doc, `"$id":"#foo",`, ``, 1)} {
		c, err := openapi.Parse(t.Context(), []byte(d), uri, nil)
		if err != nil {
			t.Fatal(err)
		}
		op, err := c.Operation("p")
		if err != nil {
			t.Fatal(err)
		}
		var perr error
		for _, p := range op.Params {
			perr = errors.Join(perr, p.Err)
		}
		t.Logf("fragment $id present=%v: op.Err=%v params=%d paramErrs=%v", strings.Contains(d, "#foo"), op.Err, len(op.Params), perr)
		if op.Err != nil || perr != nil {
			t.Errorf("operation unusable: %v %v", op.Err, perr)
		}
	}
}

func TestReviewRepairRetrievalURIAnchorIn32Self(t *testing.T) {
	fs := &zzFS{docs: map[string]string{
		"https://h.test/api/openapi.json": `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{
			"ByPointer":{"$ref":"b.json#/components/schemas/X"},
			"ByAnchor":{"$ref":"b.json#xa"},
			"BySelfAnchor":{"$ref":"https://canon.test/b.json#xa"}}}}`,
		"https://h.test/api/b.json": `{"openapi":"3.2.0","$self":"https://canon.test/b.json","info":{"title":"b","version":"1"},"components":{"schemas":{"X":{"$anchor":"xa","type":"string"}}}}`,
	}}
	l := openapi.Loader{Fetch: fs.fetch}
	c, err := l.Load(t.Context(), "https://h.test/api/openapi.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Fetch calls: %q", fs.calls)
	for _, name := range []string{"ByPointer", "ByAnchor", "BySelfAnchor"} {
		s, err := c.Schema("https://h.test/api/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		refs, err := s.References()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range refs {
			if r.Err != nil {
				t.Errorf("%s: %q -> %s: %v", name, r.Value, r.URI, r.Err)
			} else {
				t.Logf("%s: %q -> %s -> %s", name, r.Value, r.URI, r.Target.Source())
			}
		}
	}
	if _, err := c.Schema("https://h.test/api/b.json#xa"); err != nil {
		t.Errorf("Schema(retrieval URI#anchor): %v", err)
	}
}
