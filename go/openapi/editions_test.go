package openapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

var editionVersions = []string{"2.0", "3.0.4", "3.1.2", "3.2.1"}

func editionDoc(version, paths string, extra ...string) string {
	field := "openapi"
	if version == "2.0" {
		field = "swagger"
	}
	s := fmt.Sprintf(`{%q:%q,"info":{"title":"editions","version":"1"},"paths":{%s}`, field, version, paths)
	for _, e := range extra {
		s += "," + e
	}
	return s + "}"
}

func editionClient(t testing.TB, doc string, opts *openapi.Options) *openapi.Client {
	t.Helper()
	c, err := openapi.Parse(context.Background(), []byte(doc), testDocURI, opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func editionBody(t testing.TB, req *openapi.Request) []byte {
	t.Helper()
	if req.HTTP.Body == nil {
		return nil
	}
	b, err := io.ReadAll(req.HTTP.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := req.HTTP.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	return b
}

// load.go Load defines accepted minor lines and required roots. 3.1 is an
// existing-behavior pin; admitting the other editions must not relax roots.
func TestEditionsAdmission(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			for _, root := range []string{`"paths":{}`, `"components":{}`, `"webhooks":{}`} {
				field := "openapi"
				if version == "2.0" {
					field = "swagger"
				}
				doc := fmt.Sprintf(`{%q:%q,%s}`, field, version, root)
				_, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, nil)
				want := strings.HasPrefix(root, `"paths"`) || strings.HasPrefix(version, "3.1") || strings.HasPrefix(version, "3.2")
				if (err == nil) != want {
					t.Errorf("root %s: error %v, accepted=%v", root, err, want)
				}
			}
		})
	}
	for _, version := range []string{"1.2", "2.1", "3.3.0", "4.0.0", "3.0", "3.2.bad"} {
		if _, err := openapi.Parse(t.Context(), []byte(editionDoc(version, "")), testDocURI, nil); err == nil {
			t.Errorf("accepted %q", version)
		}
	}
}

// describe.go Operations fixes method order and exact case. OAS 3.2.1 Path
// Item Object alone defines query and additionalOperations.
func TestEditionsOperationMethods(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, editionDoc(version, `"/x":{"post":{},"get":{},"query":{},"additionalOperations":{"PURGE":{},"Post":{},"GET":{},"CONNECT":{}}}`), nil)
			ops := c.Operations()
			want := []string{"GET", "POST"}
			if version == "3.2.1" {
				want = append(want, "QUERY", "PURGE", "Post", "GET", "CONNECT")
			}
			var got []string
			for _, op := range ops {
				got = append(got, op.Method)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("methods %q, want %q", got, want)
			}
			if version == "3.2.1" {
				if ops[5].Key != "" || ops[5].Err == nil {
					t.Errorf("forbidden GET: %+v", ops[5])
				}
				if op := mustOp(t, c, "GET /x"); op != ops[0] {
					t.Error("fixed GET did not win lookup")
				}
				for _, method := range []string{"QUERY", "PURGE", "Post", "CONNECT"} {
					if req := mustPrepare(t, c, method+" /x", nil); req.HTTP.Method != method {
						t.Errorf("method %q", req.HTTP.Method)
					}
				}
			}
		})
	}
}

// Owner stage 6 interpretation of J2: additionalOperations is one Path Item
// field, like parameters/servers. Keep the nearest whole map; duplicate maps
// affect its additional operations, without unioning farther methods or
// disabling unrelated fixed operations. Forbidden fixed-name entries retain
// the empty-Key defect required by describe.go Operations.
func TestEditionsAdditionalOperationsMapConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, own, middle, far string
		methods                []string
		duplicate              bool
		source                 string
	}{
		{"own map", `,"additionalOperations":{"OWN":{},"GET":{}}`, `,"additionalOperations":{"MID":{}}`, `,"additionalOperations":{"FAR":{}}`, []string{"GET", "POST", "OWN", "GET"}, true, "#/paths/~1x/additionalOperations/OWN"},
		{"middle map", "", `,"additionalOperations":{"MID":{}}`, `,"additionalOperations":{"FAR":{}}`, []string{"GET", "POST", "MID"}, true, "#/components/pathItems/Mid/additionalOperations/MID"},
		{"empty nearest map", `,"additionalOperations":{}`, `,"additionalOperations":{"MID":{}}`, `,"additionalOperations":{"FAR":{}}`, []string{"GET", "POST"}, true, ""},
		{"one inherited map", "", "", `,"additionalOperations":{"FAR":{}}`, []string{"GET", "POST", "FAR"}, false, "#/components/pathItems/Base/additionalOperations/FAR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := editionDoc("3.2.1", `"/x":{"$ref":"#/components/pathItems/Mid"`+tc.own+`}`, `"components":{"pathItems":{"Mid":{"$ref":"#/components/pathItems/Base","post":{}`+tc.middle+`},"Base":{"get":{}`+tc.far+`}}}`)
			c := editionClient(t, doc, nil)
			ops := c.Operations()
			var methods []string
			for _, op := range ops {
				methods = append(methods, op.Method)
			}
			if !slices.Equal(methods, tc.methods) {
				t.Fatalf("methods %q want %q", methods, tc.methods)
			}
			for _, method := range []string{"GET", "POST"} {
				op := mustOp(t, c, method+" /x")
				if op.Err != nil {
					t.Errorf("unrelated fixed %s Err %v", method, op.Err)
				}
				mustPrepare(t, c, op.Key, nil)
			}
			for i, op := range ops[2:] {
				if op.Method == "GET" {
					if op.Key != "" || op.Err == nil {
						t.Errorf("forbidden additional GET %+v", op)
					}
					continue
				}
				if (op.Err != nil) != tc.duplicate {
					t.Errorf("additional %+v duplicate=%v", op, tc.duplicate)
				}
				if i == 0 && op.Source != testDocURI+tc.source {
					t.Errorf("Source %s want %s", op.Source, testDocURI+tc.source)
				}
				if tc.duplicate {
					req, err := c.Prepare(op.Key, nil)
					if req != nil || !errors.Is(err, op.Err) {
						t.Errorf("duplicate-map Prepare %v %v", req, err)
					}
				} else {
					mustPrepare(t, c, op.Key, nil)
				}
			}
			for _, method := range []string{"MID", "FAR"} {
				if slices.Contains(tc.methods, method) {
					continue
				}
				if _, err := c.Operation(method + " /x"); !errors.Is(err, openapi.ErrNoOperation) {
					t.Errorf("farther method %s was unioned: %v", method, err)
				}
			}
		})
	}
}

// doc.go Bodies by method; OAS 3.0.4 Operation requestBody excludes methods
// without defined body semantics. TRACE and CONNECT are always ignored.
func TestEditionsBodiesByMethod(t *testing.T) {
	for _, version := range editionVersions {
		for _, method := range []string{"get", "head", "delete", "options", "post", "put", "patch", "trace"} {
			t.Run(version+"/"+method, func(t *testing.T) {
				if version == "2.0" && method == "trace" {
					return
				} // Swagger has no TRACE field.
				body := `"requestBody":{"required":true,"content":{"text/plain":{}}}`
				if version == "2.0" {
					body = `"consumes":["text/plain"],"parameters":[{"name":"payload","in":"body","required":true,"schema":{"type":"string"}}]`
				}
				c := editionClient(t, editionDoc(version, fmt.Sprintf(`"/x":{%q:{%s}}`, method, body)), nil)
				op := mustOp(t, c, strings.ToUpper(method)+" /x")
				ignored := method == "trace" || version == "3.0.4" && slices.Contains([]string{"get", "head", "delete", "options"}, method)
				if (op.Body == nil) != ignored {
					t.Fatalf("Body nil=%v, ignored=%v", op.Body == nil, ignored)
				}
				if ignored {
					mustPrepare(t, c, op.Key, nil)
					return
				}
				if req, err := c.Prepare(op.Key, nil); err == nil || req != nil {
					t.Error("required body not refused")
				}
				if got := string(editionBody(t, mustPrepare(t, c, op.Key, &openapi.Input{Body: "payload"}))); got != "payload" {
					t.Errorf("body %q", got)
				}
			})
		}
	}
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"additionalOperations":{"CONNECT":{"requestBody":{"required":true,"content":{"text/plain":{}}}}}}`), nil)
	if mustOp(t, c, "CONNECT /x").Body != nil {
		t.Error("CONNECT has body")
	}
	mustPrepare(t, c, "CONNECT /x", nil)
}

// Swagger Object and Operation Object schemes plus doc.go URL defaults.
func TestEditionsSwaggerServers(t *testing.T) {
	for _, tc := range []struct{ name, extra, operation, want string }{
		{"defaults", `"schemes":[]`, "", "https://api.example.test/x"},
		{"host-port", `"host":"api.example.test:9443","basePath":"/v1"`, "", "https://api.example.test:9443/v1/x"},
		{"override", `"host":"api.example.test","basePath":"/v1","schemes":["http","https"]`, `"schemes":["https"]`, "https://api.example.test/v1/x"},
		{"empty-inherits", `"host":"api.example.test","schemes":["http"]`, `"schemes":[]`, "http://api.example.test/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := editionClient(t, editionDoc("2.0", `"/x":{"get":{`+tc.operation+`}}`, tc.extra), nil)
			op := mustOp(t, c, "GET /x")
			if server(t, op, 0).Source != "" {
				t.Error("assembled server has Source")
			}
			if got := mustPrepare(t, c, op.Key, nil).HTTP.URL.String(); got != tc.want {
				t.Errorf("URL %q want %q", got, tc.want)
			}
		})
	}
	for _, scheme := range []string{"ws", "wss"} {
		t.Run(scheme, func(t *testing.T) {
			rt := &memRT{}
			c := editionClient(t, editionDoc("2.0", `"/x":{"get":{}}`, `"host":"socket.example.test","schemes":["`+scheme+`"]`), &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
			mustCall(t, c, "GET /x", nil, nil)
			if got := rt.requests()[0].URL.Scheme; got != scheme {
				t.Errorf("scheme %q", got)
			}
		})
	}
}

// Reference Object sibling semantics and Source in describe.go Operation.
// This deliberately includes malformed sibling references: ignored data may
// neither replace the target nor cause an unnecessary document fetch.
func TestEditionsReferenceSiblings(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			root, ref := `"components":{"parameters":{"P":{"name":"q","in":"query","description":"target","schema":{"type":"string"}}}}`, "#/components/parameters/P"
			if version == "2.0" {
				root = `"parameters":{"P":{"name":"q","in":"query","description":"target","type":"string"}}`
				ref = "#/parameters/P"
			}
			c := editionClient(t, editionDoc(version, `"/x":{"get":{"parameters":[{"$ref":"`+ref+`","name":"wrong","description":"use"}]}}`, root), nil)
			p := param(t, mustOp(t, c, "GET /x"), 0)
			want := "target"
			if version == "3.1.2" || version == "3.2.1" {
				want = "use"
			}
			if p.Name != "q" || p.Description != want || p.Source != testDocURI+ref {
				t.Errorf("parameter %+v", p)
			}
			if got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"q": "a b"}}).HTTP.URL.RawQuery; got != "q=a%20b" {
				t.Errorf("query %q", got)
			}
		})
	}
}

// Stage 5 reference ownership with 3.2 $self (load.go Loader and Document):
// retrieval/request aliases identify unchanged bytes, whereas API URLs still
// resolve against retrieval. Versionless fragments use the entry edition.
func TestEditionsSelfAndRetrievalBases(t *testing.T) {
	const requestURI = "https://api.example.test/start.json"
	const finalURI = "https://api.example.test/retrieved/root.json"
	const selfURI = "https://api.example.test/models/root.json"
	doc := editionDoc("3.2.1", `"/x":{"$ref":"part.json"}`, `"$self":"../models/root.json","servers":[{"url":"./v1"}],"components":{"parameters":{"P":{"name":"X-Ref","in":"header","schema":{"type":"string"}}}}`)
	var mu sync.Mutex
	seen := map[string]int{}
	l := openapi.Loader{Fetch: func(_ context.Context, uri string) (io.ReadCloser, string, error) {
		mu.Lock()
		seen[uri]++
		mu.Unlock()
		switch uri {
		case requestURI:
			return io.NopCloser(strings.NewReader(doc)), finalURI, nil
		case "https://api.example.test/models/part.json":
			return io.NopCloser(strings.NewReader(`{"query":{"operationId":"op","parameters":[{"name":"q","in":"querystring","content":{"text/plain":{}}},{"$ref":"https://api.example.test/start.json#/components/parameters/P"}]}}`)), "", nil
		}
		return nil, "", fmt.Errorf("unexpected URI %s", uri)
	}}
	c, err := l.Load(t.Context(), requestURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := mustPrepare(t, c, "op", &openapi.Input{Params: map[string]any{"q": "a/b", "X-Ref": "retrieval alias"}})
	if got := req.HTTP.URL.String(); got != "https://api.example.test/retrieved/v1/x?a%2Fb" {
		t.Errorf("URL %q", got)
	}
	if req.HTTP.Header.Get("X-Ref") != "retrieval alias" {
		t.Error("requested URI did not resolve to loaded entry")
	}
	for _, uri := range []string{finalURI, selfURI} {
		b := c.Document(uri)
		if !json.Valid(b) {
			t.Errorf("alias %s: %s", uri, b)
		}
	}
	if got := c.DocumentURIs(); !slices.Equal(got, []string{finalURI, "https://api.example.test/models/part.json"}) {
		t.Errorf("documents %q", got)
	}
	if seen[requestURI] != 1 || len(seen) != 2 {
		t.Errorf("fetches %v", seen)
	}
}

// load.go Parse permits absolute $self with an empty retrieval URI. Earlier
// editions ignore $self, defaultMapping and security URI feature-shaped data.
func TestEditionsDiscoveryBoundaries(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			var mu sync.Mutex
			fetched := map[string]int{}
			l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
				mu.Lock()
				fetched[u]++
				mu.Unlock()
				return io.NopCloser(strings.NewReader(`{"type":"object"}`)), "", nil
			}}
			doc := editionDoc(version, `"/x":{"get":{}}`, `"$self":"https://api.example.test/model/root.json","components":{"schemas":{"S":{"discriminator":{"propertyName":"kind","defaultMapping":"./other.json"},"example":{"$ref":"never.json"}}}}`)
			c, err := l.Parse(t.Context(), []byte(doc), testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if version == "3.2.1" {
				want = 1
			}
			if len(fetched) != want {
				t.Errorf("fetches %v want %d", fetched, want)
			}
			if want == 1 && fetched["https://api.example.test/model/other.json"] != 1 {
				t.Errorf("base fetches %v", fetched)
			}
			mustPrepare(t, c, "GET /x", nil)
		})
	}
	l := openapi.Loader{AllowReference: func(_, to string) bool { return to == "https://api.example.test/part.json" }, Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
		if u != "https://api.example.test/part.json" {
			return nil, "", errors.New("wrong base")
		}
		return io.NopCloser(strings.NewReader(`{"get":{}}`)), "", nil
	}}
	c, err := l.Parse(t.Context(), []byte(editionDoc("3.2.1", `"/x":{"$ref":"part.json"}`, `"$self":"https://api.example.test/root.json"`)), "", &openapi.Options{BaseURL: "https://api.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	mustPrepare(t, c, "GET /x", nil)
	// A reference base does not grant retrieval-origin admission.
	l.AllowReference = nil
	c, err = l.Parse(t.Context(), []byte(editionDoc("3.2.1", `"/x":{"$ref":"part.json"}`, `"$self":"https://api.example.test/root.json"`)), "", &openapi.Options{BaseURL: "https://api.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if op := mustOp(t, c, "GET /x"); op.Err == nil {
		t.Error("absolute self granted reference admission")
	}
}
