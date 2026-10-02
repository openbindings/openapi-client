package openapi_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Runnable versions of the stage 5 example flows in example_test.go (dev
// loop: "Examples in example_test.go become runnable tests against httptest
// servers as their stage lands"): scenarios 8 (Example_sharedTransport, its
// documents YAML), 12 (Example_bounds, "all documents together") and 18c
// (Example_loadExactly). Scenario 18b (Example_schemasAsWritten) needs
// Schema.References, which comes with stage 9.

// Example_loadExactly: a library loads documents exactly as it chooses:
// only from its own copies, each at the URI it was published at;
// references allowed to reach one more origin, where the partner keeps
// shared schemas; and security scheme names looked up in exactly one
// document, or the default, pinned by name.
func TestFlowLoadExactly(t *testing.T) {
	api := newWire(t, jsonAnswer(200, `{"orders":[]}`))
	specs := map[string][]byte{
		"https://partner.example.com/openapi/root.yaml": []byte(`openapi: 3.1.0
info: {title: Partner, version: '2'}
servers:
  - url: ` + api.URL + `
components:
  securitySchemes:
    key: {type: apiKey, in: header, name: X-Root}
paths:
  /orders:
    $ref: 'paths/orders.yaml#/components/pathItems/Orders'
  /legacy:
    $ref: 'https://elsewhere.example.com/legacy.json'
`),
		"https://partner.example.com/openapi/paths/orders.yaml": []byte(`components:
  securitySchemes:
    key: {type: apiKey, in: header, name: X-Orders}
  pathItems:
    Orders:
      get:
        operationId: listOrders
        security:
          - key: []
        parameters:
          - $ref: 'https://shared.example.com/params.json#/Page'
        responses:
          '200':
            description: the orders
            content:
              application/json:
                schema:
                  $ref: 'https://shared.example.com/schemas.json#/Orders'
`),
		"https://shared.example.com/params.json":  []byte(`{"Page":{"name":"page","in":"query","schema":{"type":"integer"}}}`),
		"https://shared.example.com/schemas.json": []byte(`{"Orders":{"type":"object","properties":{"orders":{"type":"array"}}}}`),
	}
	var mu sync.Mutex
	var fetched []string
	for _, tt := range []struct {
		lookup openapi.SchemeLookup
		header string
	}{
		{openapi.SchemesInEntry, "X-Root"},
		{openapi.SchemesInReferrer, "X-Orders"},
		{openapi.SchemesInEntryFirst, "X-Root"},
	} {
		loader := openapi.Loader{
			Fetch: func(ctx context.Context, uri string) (io.ReadCloser, string, error) {
				mu.Lock()
				fetched = append(fetched, uri)
				mu.Unlock()
				content, ok := specs[uri]
				if !ok {
					return nil, "", fmt.Errorf("%s is not shipped with this library", uri)
				}
				return io.NopCloser(bytes.NewReader(content)), uri, nil
			},
			AllowReference: func(from, to string) bool {
				_, ok := specs[to] // this library trusts only the documents it ships
				return ok
			},
			SchemeLookup: tt.lookup,
		}
		c, err := loader.Load(t.Context(), "https://partner.example.com/openapi/root.yaml", &openapi.Options{
			Credentials: map[string]openapi.Credential{"key": openapi.Secret("k-1")},
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Version() != "3.1.0" || len(c.Operations()) != 2 {
			t.Errorf("Version %q, %d operations", c.Version(), len(c.Operations()))
		}
		if legacy := mustOp(t, c, "GET /legacy"); !errors.Is(legacy.Err, openapi.ErrUnresolved) {
			t.Errorf("/legacy Err = %v; its document is not shipped", legacy.Err)
		}
		before := api.count()
		mustCall(t, c, "listOrders", &openapi.Input{Params: map[string]any{"page": 2}}, nil)
		got := api.last(t)
		if api.count() != before+1 || got.RequestURI != "/orders?page=2" || got.Header.Get(tt.header) != "k-1" {
			t.Errorf("SchemeLookup %d: sent %s with %s %q; headers %v", tt.lookup, got.RequestURI, tt.header, got.Header.Get(tt.header), got.Header)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, uri := range fetched {
		if _, ok := specs[uri]; !ok {
			t.Errorf("Fetch was asked for %s, which AllowReference refuses", uri)
		}
	}
}

// apiLabels counts the requests of one API on a shared transport.
type apiLabels struct {
	api  string
	next http.RoundTripper
	mu   *sync.Mutex
	seen map[string]int
}

func (l apiLabels) RoundTrip(r *http.Request) (*http.Response, error) {
	l.mu.Lock()
	op := "document"
	if o := openapi.OperationFromContext(r.Context()); o != nil {
		op = o.Key
	}
	l.seen[l.api+" "+op]++
	l.mu.Unlock()
	return l.next.RoundTrip(r)
}

// Example_sharedTransport: many Clients share one Transport, each through a
// small http.Client whose RoundTripper labels requests by API and
// operation, since operation keys repeat across APIs; each API's document
// is YAML, and its referenced documents come through the same client.
func TestFlowSharedTransport(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	shared := http.DefaultTransport
	sites := map[string]*site{"pets": newSite(t), "stores": newSite(t)}
	for name, s := range sites {
		s.put("/openapi.yaml", "openapi: 3.1.0\ninfo: {title: "+name+", version: '1'}\nservers:\n  - url: '@SELF@'\npaths:\n  /health:\n    $ref: 'health.yaml'\n")
		s.put("/health.yaml", "get:\n  operationId: getHealth\n  responses:\n    '200': {description: ok}\n")
	}
	clients := map[string]*openapi.Client{}
	for name, s := range sites {
		clients[name] = mustLoad(t, nil, s.uri("/openapi.yaml"), &openapi.Options{
			HTTPClient: &http.Client{Transport: apiLabels{api: name, next: shared, mu: &mu, seen: seen}},
		})
	}
	for name, c := range clients {
		mustCall(t, c, "getHealth", nil, nil)
		if got := sites[name].lastCall(t).RequestURI; got != "/health" {
			t.Errorf("%s sent %s", name, got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"pets", "stores"} {
		if seen[name+" document"] != 2 || seen[name+" getHealth"] != 1 {
			t.Errorf("%s: %d document requests and %d calls, want 2 and 1", name, seen[name+" document"], seen[name+" getHealth"])
		}
	}
}

// Example_bounds: "all documents together": the bound covers every document
// one load retrieves (load.go, Loader.MaxBytes), and the error is
// recognized by its type.
func TestFlowBoundsAllDocuments(t *testing.T) {
	s := newSite(t)
	entry := entry31(`"/pets":{"$ref":"pets.json"}`)
	pets := `{"get":{"operationId":"listPets","description":"` + strings.Repeat("x", 4096) + `"}}`
	s.put("/openapi.json", entry)
	s.put("/pets.json", pets)
	size := int64(len(strings.ReplaceAll(entry, "@SELF@", s.URL)))

	c := mustLoad(t, &openapi.Loader{MaxBytes: 8 << 20}, s.uri("/openapi.json"), nil)
	mustCall(t, c, "listPets", nil, nil)

	c = mustLoad(t, &openapi.Loader{MaxBytes: size + 100}, s.uri("/openapi.json"), nil)
	var tooBig *http.MaxBytesError
	if e := mustOp(t, c, "GET /pets"); !errors.As(e.Err, &tooBig) {
		t.Errorf("/pets Err = %v, want an *http.MaxBytesError", e.Err)
	}
}
