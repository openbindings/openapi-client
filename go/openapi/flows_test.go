package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Runnable versions of example flows in example_test.go (first call,
// create, failure, decode failure, bounds, prepare and send, environments,
// With-derived tenants without credentials), against a fake of the API
// testdata/pets.json describes.

// petsAPI serves testdata/pets.json at /openapi.json and its operations
// under /v1, the document's relative server.
type petsAPI struct {
	*wire
	mu      sync.Mutex
	order   string // how POST /v1/orders answers: "ok", "bad" or "empty"
	release chan struct{}
}

func newPetsAPI(t *testing.T) *petsAPI {
	t.Helper()
	pets := readPets(t)
	api := &petsAPI{order: "ok", release: make(chan struct{})}
	api.wire = newWire(t, func(w http.ResponseWriter, r *http.Request) { api.serve(w, r, pets) })
	t.Cleanup(func() { close(api.release) }) // before the server closes
	return api
}

func (api *petsAPI) setOrder(mode string) {
	api.mu.Lock()
	api.order = mode
	api.mu.Unlock()
}

func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(Problem{Title: title, Detail: detail})
}

func (api *petsAPI) serve(w http.ResponseWriter, r *http.Request, doc []byte) {
	path := r.URL.Path
	switch {
	case path == "/openapi.json":
		w.Header().Set("Content-Type", "application/json")
		w.Write(doc)
	case path == "/v1/pets" && r.Method == "GET":
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "c2" {
			io.WriteString(w, `{"pets":[{"id":"p-2","name":"Tom"}],"next_cursor":""}`)
			return
		}
		io.WriteString(w, `{"pets":[{"id":"p-1","name":"Rex"}],"next_cursor":"c2"}`)
	case path == "/v1/pets" && r.Method == "POST":
		var p Pet
		json.NewDecoder(r.Body).Decode(&p)
		p.ID = "p-9"
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/v1/pets/p-9")
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(p)
	case strings.HasPrefix(path, "/v1/pets/"):
		id := strings.TrimPrefix(path, "/v1/pets/")
		var p Pet
		if r.Method == "PUT" {
			json.NewDecoder(r.Body).Decode(&p)
		}
		switch {
		case id == "p-slow": // no response until the call gives up
			select {
			case <-r.Context().Done():
			case <-api.release:
			}
		case id == "p-404":
			problem(w, 404, "Not found", "no pet "+id)
		case id == "p-500":
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(500)
			io.WriteString(w, "oops")
		case id == "p-bad":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, "not json")
		case r.Method == "PUT" && p.Name == "":
			problem(w, 422, "Invalid", "name is required")
		case r.Method == "PUT":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"`+id+`","name":"`+p.Name+`","weight":12.50,"big":9007199254740993}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(Pet{ID: id, Name: "Rex", Tag: "dog"})
		}
	case path == "/v1/orders":
		api.mu.Lock()
		mode := api.order
		api.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/v1/orders/o-1")
		w.WriteHeader(201)
		switch mode {
		case "ok":
			io.WriteString(w, `{"id":"o-1"}`)
		case "bad":
			io.WriteString(w, "<html>created</html>")
		}
	default:
		http.NotFound(w, r)
	}
}

func (api *petsAPI) load(t *testing.T, opts *openapi.Options) *openapi.Client {
	t.Helper()
	c, err := openapi.Load(t.Context(), api.URL+"/openapi.json", opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

// Example_firstCall: load a document, call an operation with a path
// parameter and an integer query parameter, and decode the JSON result.
func TestFlowFirstCall(t *testing.T) {
	api := newPetsAPI(t)
	c := api.load(t, nil)
	var pet Pet
	_, err := c.Call(t.Context(), "getPet", &openapi.Input{
		Params: map[string]any{"petId": "p-7", "revision": 3},
	}, &pet)
	if err != nil {
		t.Fatal(err)
	}
	if pet.Name != "Rex" || pet.ID != "p-7" {
		t.Errorf("pet = %+v", pet)
	}
	if got := api.last(t); got.Method != "GET" || got.RequestURI != "/v1/pets/p-7?revision=3" {
		t.Errorf("sent %s %s", got.Method, got.RequestURI)
	}
}

// Example_create: send a JSON body built from a struct, and read Location
// from the 201.
func TestFlowCreate(t *testing.T) {
	api := newPetsAPI(t)
	c := api.load(t, nil)
	var created Pet
	resp, err := c.Call(t.Context(), "createPet", &openapi.Input{
		Body: Pet{Name: "Rex", Tag: "dog"},
	}, &created)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("Call returned a nil Response and no error")
	}
	if resp.StatusCode != 201 || resp.Header.Get("Location") != "/v1/pets/p-9" || created.ID != "p-9" {
		t.Errorf("got %d %q %q", resp.StatusCode, resp.Header.Get("Location"), created.ID)
	}
	got := api.last(t)
	if got.Method != "POST" || got.RequestURI != "/v1/pets" || string(trimNL(got.Body)) != `{"name":"Rex","tag":"dog"}` ||
		got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("sent %s %s %q as %q", got.Method, got.RequestURI, got.Body, got.Header.Get("Content-Type"))
	}
}

// Example_failure: a declared 404 or 422 error body, told apart from a status
// the document does not declare, a reply that did not decode, a call that
// was never sent, a cancelled context and a transport failure, testing the
// package's own types first.
func TestFlowFailure(t *testing.T) {
	api := newPetsAPI(t)
	c := api.load(t, nil)
	classify := func(ctx context.Context, c *openapi.Client, in *openapi.Input) string {
		var pet Pet
		_, err := c.Call(ctx, "updatePet", in, &pet)
		var (
			se *openapi.StatusError
			de *openapi.DecodeError
			re *openapi.RequestError
			ue *url.Error
		)
		switch {
		case err == nil:
			return "updated " + pet.Name
		case errors.As(err, &se) && se.Declaration != nil:
			var p Problem
			if err := se.Decode(&p); err != nil {
				return "unreadable declared body"
			}
			return "declared " + se.Declaration.Key + ": " + p.Title
		case errors.As(err, &se):
			return "undeclared " + se.Header.Get("Content-Type") + ": " + string(se.Content)
		case errors.As(err, &de):
			return "updated; reply unreadable"
		case errors.As(err, &re):
			return "not sent"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return "gave up"
		case errors.As(err, &ue):
			return "transport failure"
		}
		return "unclassified: " + err.Error()
	}
	tests := []struct {
		name string
		ctx  context.Context
		in   *openapi.Input
		want string
	}{
		{"updated", t.Context(), &openapi.Input{Params: map[string]any{"petId": "p-7"}, Body: Pet{Name: "Rex"}}, "updated Rex"},
		{"declared 404", t.Context(), &openapi.Input{Params: map[string]any{"petId": "p-404"}, Body: Pet{Name: ""}}, "declared 404: Not found"},
		{"declared 422", t.Context(), &openapi.Input{Params: map[string]any{"petId": "p-7"}, Body: Pet{Name: ""}}, "declared 422: Invalid"},
		{"undeclared 500", t.Context(), &openapi.Input{Params: map[string]any{"petId": "p-500"}, Body: Pet{Name: "Rex"}}, "undeclared text/plain: oops"},
		{"reply unreadable", t.Context(), &openapi.Input{Params: map[string]any{"petId": "p-bad"}, Body: Pet{Name: "Rex"}}, "updated; reply unreadable"},
		{"not sent", t.Context(), &openapi.Input{Body: Pet{Name: "Rex"}}, "not sent"},
	}
	for _, tt := range tests {
		if got := classify(tt.ctx, c, tt.in); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	// p-slow never answers: the deadline ends the call after it started.
	deadline, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if got := classify(deadline, c, &openapi.Input{Params: map[string]any{"petId": "p-slow"}, Body: Pet{Name: "Rex"}}); got != "gave up" {
		t.Errorf("deadline: %q, want gave up", got)
	}
	api.Close()
	if got := classify(t.Context(), c, &openapi.Input{Params: map[string]any{"petId": "p-7"}, Body: Pet{Name: "Rex"}}); got != "transport failure" {
		t.Errorf("closed server: %q, want transport failure", got)
	}
}

// Example_decodeFailure: a 2xx that fails to decode; the server acted, and
// the first bytes of the body are kept for the log.
func TestFlowDecodeFailure(t *testing.T) {
	api := newPetsAPI(t)
	c := api.load(t, nil)
	create := func() (*openapi.Response, order, error) {
		var o order
		resp, err := c.Call(t.Context(), "createOrder", &openapi.Input{
			Body: map[string]any{"sku": "A-1", "quantity": 2},
		}, &o)
		return resp, o, err
	}

	api.setOrder("bad")
	_, _, err := create()
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error %v, want a *DecodeError", err)
	}
	if de.StatusCode != 201 || de.Header.Get("Location") != "/v1/orders/o-1" || de.Header.Get("Content-Type") != "application/json" {
		t.Errorf("DecodeError response %d %q %q", de.StatusCode, de.Header.Get("Location"), de.Header.Get("Content-Type"))
	}
	if errors.Is(err, io.EOF) || !bytes.HasPrefix([]byte("<html>created</html>"), de.Content) || len(de.Content) == 0 {
		t.Errorf("Content %q, Err %v", de.Content, de.Err)
	}

	api.setOrder("empty")
	_, _, err = create()
	if !errors.As(err, &de) || !errors.Is(err, io.EOF) {
		t.Errorf("empty reply: %v, want a *DecodeError wrapping io.EOF", err)
	}

	api.setOrder("ok")
	resp, o, err := create()
	if err != nil || resp.StatusCode != 201 || o.ID != "o-1" {
		t.Errorf("ok: %v, %v, %+v", resp, err, o)
	}
}

// Example_bounds: bound the documents, decoded replies and kept error
// bodies, and recognize each bound when it is hit.
func TestFlowBounds(t *testing.T) {
	api := newPetsAPI(t)
	var tooBig *http.MaxBytesError

	small := openapi.Loader{MaxBytes: 100}
	if _, err := small.Load(t.Context(), api.URL+"/openapi.json", nil); !errors.As(err, &tooBig) || tooBig.Limit != 100 {
		t.Errorf("Load over MaxBytes: %v, want an *http.MaxBytesError with Limit 100", err)
	}

	loader := openapi.Loader{MaxBytes: 8 << 20}
	c, err := loader.Load(t.Context(), api.URL+"/openapi.json", &openapi.Options{
		MaxBodyBytes:  16,
		MaxErrorBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Pets []Pet `json:"pets"`
	}
	_, err = c.Call(t.Context(), "listPets", nil, &page)
	var se *openapi.StatusError
	var de *openapi.DecodeError
	if errors.As(err, &se) || !errors.As(err, &de) || !errors.As(err, &tooBig) || tooBig.Limit != 16 {
		t.Errorf("reply over MaxBodyBytes: %v", err)
	}
	_, err = c.Call(t.Context(), "getPet", &openapi.Input{Params: map[string]any{"petId": "p-404"}}, &Pet{})
	if !errors.As(err, &se) || !errors.As(se.Err, &tooBig) || len(se.Content) != 8 {
		t.Errorf("error body over MaxErrorBytes: %v", err)
	}
}

// Example_prepare: see a call exactly as it will be sent, change what the
// document cannot express, and send exactly that.
func TestFlowPrepareAndSend(t *testing.T) {
	api := newPetsAPI(t)
	c := api.load(t, nil)
	signed := c.With(func(o *openapi.Options) { o.Redirects = openapi.FollowNone })
	req, err := signed.Prepare("createOrder", &openapi.Input{
		Body: map[string]any{"sku": "A-1", "quantity": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req == nil || req.HTTP == nil || req.HTTP.GetBody == nil {
		t.Fatalf("Prepare returned %+v with no GetBody", req)
	}
	body, err := req.HTTP.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := io.ReadAll(body)
	body.Close()
	if req.HTTP.Method != "POST" || req.HTTP.URL.String() != api.URL+"/v1/orders" ||
		req.HTTP.Header.Get("Content-Type") != "application/json" || string(trimNL(encoded)) != `{"quantity":2,"sku":"A-1"}` {
		t.Errorf("prepared %s %s (%s) %q", req.HTTP.Method, req.HTTP.URL, req.HTTP.Header.Get("Content-Type"), encoded)
	}
	req.HTTP.Header.Set("X-Signature", "sig-"+req.HTTP.URL.Path)

	var o order
	if _, err := req.Call(t.Context(), &o); err != nil {
		t.Fatal(err)
	}
	if o.ID != "o-1" {
		t.Errorf("order = %+v", o)
	}
	got := api.last(t)
	if got.Header.Get("X-Signature") != "sig-/v1/orders" || !bytes.Equal(got.Body, encoded) {
		t.Errorf("sent X-Signature %q, body %q", got.Header.Get("X-Signature"), got.Body)
	}

	// Example_exactBehavior's invoke: request construction by the client,
	// classification by the wrapper.
	out, err := invoke(t.Context(), c, "PUT /pets/{petId}", &openapi.Input{
		Params:    map[string]any{"petId": "p-404"},
		Body:      []byte(`{"name":"x"}`),
		MediaType: "application/json",
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	op := mustOp(t, c, "updatePet")
	if out.Status != 404 || out.Declaration != response(t, op, 1) || out.Media != responseMedia(t, op, 1, 0) || out.BodyErr != nil {
		t.Errorf("Outcome = %d %+v %+v %v", out.Status, out.Declaration, out.Media, out.BodyErr)
	}
	var p Problem
	if err := json.Unmarshal(out.Body, &p); err != nil || p.Title != "Not found" {
		t.Errorf("Outcome body %q", out.Body)
	}
}

const envDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"servers":[
		{"url":"@BASE@/{region}/{version}","description":"Production","variables":{
			"region":{"default":"us","enum":["us","eu"],"description":"Where the data lives"},
			"version":{"default":"v1"}}},
		{"url":"@BASE@/sandbox","description":"Sandbox"}
	],
	"paths":{"/pets":{"get":{"operationId":"listPets"}}}}`

// Example_environmentsBaseURL, Example_environmentsListed and
// Example_environmentsVariables: a server by URL the document need not list;
// a listed server chosen by its URL as written, at load or per derived
// Client; server variables, a variable left out keeping its default.
func TestFlowEnvironments(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, envDoc, nil)
	op := mustOp(t, c, "listPets")
	if len(op.Servers) != 2 || op.Servers[0].Description != "Production" || op.Servers[1].URL != w.URL+"/sandbox" {
		t.Fatalf("Servers = %+v", op.Servers)
	}
	var prompts []string
	for _, v := range op.Servers[0].Variables {
		prompts = append(prompts, variablePrompt(v))
	}
	if want := []string{`region: one of ["us" "eu"], default "us": Where the data lives`, `version: one of [], default "v1": `}; strings.Join(prompts, "|") != strings.Join(want, "|") {
		t.Errorf("prompts = %q", prompts)
	}

	production := parseFor(t, w, envDoc, &openapi.Options{Server: w.URL + "/{region}/{version}"})
	sandbox := production.With(func(o *openapi.Options) { o.Server = w.URL + "/sandbox" })
	eu := production.With(func(o *openapi.Options) { o.Variables["region"] = "eu" })
	staging := c.With(func(o *openapi.Options) { o.BaseURL = w.URL + "/staging/v1" })
	for _, tt := range []struct {
		c    *openapi.Client
		want string
	}{
		{production, "/us/v1/pets"},
		{sandbox, "/sandbox/pets"},
		{eu, "/eu/v1/pets"},
		{staging, "/staging/v1/pets"},
	} {
		mustCall(t, tt.c, op.Key, nil, nil)
		if got := w.last(t).RequestURI; got != tt.want {
			t.Errorf("request target %q, want %q", got, tt.want)
		}
	}
}

const tenantDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"servers":[{"url":"@BASE@/tenants/{tenant}"}],
	"paths":{"/pets/{petId}":{"get":{"operationId":"getPet",
		"parameters":[{"name":"petId","in":"path","required":true,"schema":{"type":"string"}}],
		"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}}`

// Example_tenants, without credentials: one loaded Client used from many
// goroutines, with a Client derived per tenant; deriving copies the maps,
// so the base Client is untouched.
func TestFlowTenants(t *testing.T) {
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/tenants/"), "/pets/")
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(Pet{ID: parts[1], Name: parts[0] + ":" + parts[1]})
	})
	base := parseFor(t, w, tenantDoc, &openapi.Options{
		HTTPClient: &http.Client{Transport: labelled{api: "pets", next: http.DefaultTransport}},
	})
	tenants := []struct {
		ID     string
		PetIDs []string
	}{{"acme", []string{"p-1", "p-2"}}, {"globex", []string{"p-3"}}}

	var (
		mu  sync.Mutex
		got = map[string]string{}
		wg  sync.WaitGroup
	)
	for _, tn := range tenants {
		c := base.With(func(o *openapi.Options) { o.Variables["tenant"] = tn.ID })
		for _, id := range tn.PetIDs {
			wg.Go(func() {
				var pet Pet
				if _, err := c.Call(t.Context(), "getPet", &openapi.Input{Params: map[string]any{"petId": id}}, &pet); err != nil {
					t.Errorf("%s: %v", tn.ID, err)
					return
				}
				mu.Lock()
				got[tn.ID+" "+id] = pet.Name
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	for key, want := range map[string]string{"acme p-1": "acme:p-1", "acme p-2": "acme:p-2", "globex p-3": "globex:p-3"} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}

	// The base Client has no tenant: the undeclared variable needs a value.
	before := w.count()
	resp, err := base.Call(t.Context(), "getPet", &openapi.Input{Params: map[string]any{"petId": "p-1"}}, nil)
	re := asRequestError(t, err)
	if resp != nil || w.count() != before {
		t.Errorf("the base Client sent a call without a tenant")
	}
	wantKeys(t, "Settings", re.Settings, false, `Options.Variables["tenant"]`)
}

// Example_package, without credentials: zero values are safe, With composes
// and can reset, a mistaken out is caught before sending, and a prepared
// request can be written out.
func TestFlowPackage(t *testing.T) {
	api := newPetsAPI(t)
	var zero openapi.Client
	if len(zero.Operations()) != 0 {
		t.Errorf("zero Client has operations")
	}
	if _, err := zero.Call(t.Context(), "getPet", nil, nil); !errors.Is(err, openapi.ErrNoOperation) {
		t.Errorf("zero Client Call: %v", err)
	}

	c := api.load(t, &openapi.Options{})
	if _, err := c.Operation("getPets"); !errors.Is(err, openapi.ErrNoOperation) {
		t.Errorf("Operation(getPets): %v", err)
	}
	before := api.count()
	var pet Pet
	_, err := c.Call(t.Context(), "getPet", &openapi.Input{Params: map[string]any{"petId": "p-7"}}, pet)
	asRequestError(t, err)
	if api.count() != before {
		t.Errorf("a call with a non-pointer out was sent")
	}

	tenant := c.With(func(o *openapi.Options) { o.Header.Set("X-Tenant", "t-1") })
	anonymous := tenant.With(func(o *openapi.Options) { clear(o.Header) })
	_, err = anonymous.Prepare("getPet", &openapi.Input{Params: map[string]any{"petID": "p-7"}})
	re := asRequestError(t, err)
	wantKeys(t, "Inputs", re.Inputs, true, "petID", "petId")

	req := mustPrepare(t, anonymous, "getPet", &openapi.Input{Params: map[string]any{"petId": "p-7"}})
	var buf bytes.Buffer
	if err := req.HTTP.Write(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "GET /v1/pets/p-7 HTTP/1.1\r\n") || strings.Contains(buf.String(), "X-Tenant") {
		t.Errorf("prepared request:\n%s", buf.String())
	}
}

// Example_pagination: pages the caller drives, the cursor in each result
// going back as a parameter.
func TestFlowPagination(t *testing.T) {
	api := newPetsAPI(t)
	c := api.load(t, nil)
	params := map[string]any{"limit": 100}
	var names []string
	for range 5 {
		var page struct {
			Pets       []Pet  `json:"pets"`
			NextCursor string `json:"next_cursor"`
		}
		if _, err := c.Call(t.Context(), "listPets", &openapi.Input{Params: params}, &page); err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Pets {
			names = append(names, p.Name)
		}
		if page.NextCursor == "" {
			break
		}
		params["cursor"] = page.NextCursor
	}
	if strings.Join(names, ",") != "Rex,Tom" {
		t.Errorf("names = %q", names)
	}
	reqs := api.requests()
	if n := len(reqs); n < 2 || reqs[n-2].RequestURI != "/v1/pets?limit=100" || reqs[n-1].RequestURI != "/v1/pets?limit=100&cursor=c2" {
		t.Errorf("requests = %+v", reqs)
	}
}

// Example_dynamicCall's core: JSON arguments decoded with UseNumber, numbers
// exact both ways, input mistakes reported by key.
func TestFlowDynamicCall(t *testing.T) {
	api := newPetsAPI(t)
	c := api.load(t, nil)
	args := `{"params": {"petId": "p-7", "revision": 9007199254740993}, "body": {"name": "Rex", "weight": 12.50}}`
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	var in struct {
		Params map[string]any `json:"params"`
		Body   any            `json:"body"`
	}
	if err := dec.Decode(&in); err != nil {
		t.Fatal(err)
	}
	var out any
	// updatePet declares no revision: the mistake goes back by key.
	_, err := c.Call(t.Context(), "updatePet", &openapi.Input{Params: in.Params, Body: in.Body}, &out)
	re := asRequestError(t, err)
	wantKeys(t, "Inputs", re.Inputs, true, "revision")

	delete(in.Params, "revision")
	if _, err := c.Call(t.Context(), "updatePet", &openapi.Input{Params: in.Params, Body: in.Body}, &out); err != nil {
		t.Fatal(err)
	}
	if got := api.last(t); string(trimNL(got.Body)) != `{"name":"Rex","weight":12.50}` {
		t.Errorf("body %q, want the number as written", got.Body)
	}
	result, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"big":9007199254740993,"id":"p-7","name":"Rex","weight":12.50}` {
		t.Errorf("result %s, want numbers as received", result)
	}
}

// Example_timeouts: a per-call deadline; the error matches the context's
// error and carries its cause.
func TestFlowTimeouts(t *testing.T) {
	w := newWire(t, nil)
	h, _ := stall(t, 0, "", "")
	w.setAnswer(h)
	c := parseAt(t, string(readPets(t)), "", w.URL+"/openapi.json", nil)
	ctx, cancel := context.WithTimeoutCause(t.Context(), 100*time.Millisecond, errBudget)
	defer cancel()
	var pet Pet
	_, err := c.Call(ctx, "getPet", &openapi.Input{Params: map[string]any{"petId": "p-7"}}, &pet)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errBudget) {
		t.Errorf("error %v, want DeadlineExceeded with its cause", err)
	}
}

// Example_classifyFailures and its outcome function: every outcome of a
// call, the package's types first.
func TestFlowClassifyFailures(t *testing.T) {
	api := newPetsAPI(t)
	c := api.load(t, &openapi.Options{MaxBodyBytes: 1 << 10})
	tight := c.With(func(o *openapi.Options) { o.MaxBodyBytes = 8 })
	// Each ends a call to p-slow, which never answers, after it started.
	withDeadline := func() context.Context {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		t.Cleanup(cancel)
		return ctx
	}
	cancelledSoon := func() context.Context {
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(100*time.Millisecond, cancel)
		return ctx
	}
	get := func(c *openapi.Client, ctx context.Context, id string) error {
		_, err := c.Call(ctx, "getPet", &openapi.Input{Params: map[string]any{"petId": id}}, &Pet{})
		return err
	}
	_, unknown := c.Call(t.Context(), "getPets", nil, nil)
	_, notSent := c.Call(t.Context(), "getPet", nil, nil)
	tests := []struct {
		err  error
		want string
	}{
		{get(c, t.Context(), "p-7"), "ok"},
		{unknown, "unknown_operation"},
		{notSent, "not_sent"},
		{get(c, t.Context(), "p-500"), "server_error"},
		{get(c, t.Context(), "p-404"), "rejected"},
		{get(tight, t.Context(), "p-7"), "reply_too_large"},
		{get(c, t.Context(), "p-bad"), "reply_unreadable"},
		{get(c, withDeadline(), "p-slow"), "deadline"},
		{get(c, cancelledSoon(), "p-slow"), "cancelled"},
	}
	for i, tt := range tests {
		if got := outcome(tt.err); got != tt.want {
			t.Errorf("case %d: outcome(%v) = %q, want %q", i, tt.err, got, tt.want)
		}
	}
	api.Close()
	if got := outcome(get(c, t.Context(), "p-7")); got != "transport" {
		t.Errorf("closed server: %q, want transport", got)
	}
}
