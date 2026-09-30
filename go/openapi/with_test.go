package openapi_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Client.With. client.go, With: "With returns a Client that shares c's
// document, with c's Options changed by f. f receives a copy: its maps are
// always non-nil and belong to the new Client, so f may add, replace or
// delete entries, and may reset any field to its zero value to restore the
// default. Nothing f does affects c ... the new Client keeps a copy of the
// Options f leaves, maps and slices included. With(nil) returns c. With
// returns no error: a derived Client skips Load's checks for names the
// document never uses (in Credentials, Variables and MediaType), and any
// other Options the document cannot use refuse each call they affect."

const withDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"servers":[
		{"url":"@BASE@/{v}","variables":{"v":{"default":"one"}}},
		{"url":"@BASE@/second"}
	],
	"paths":{
		"/pets":{"get":{"operationId":"listPets"},"post":{"operationId":"createPet","requestBody":{"content":{"application/json":{},"application/xml":{}}}}},
		"/only":{"get":{"operationId":"onlyFirst","servers":[{"url":"@BASE@/only"}]}}
	}}`

func TestWithNil(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, withDoc, nil)
	if c.With(nil) != c {
		t.Errorf("With(nil) did not return c")
	}
}

// f's maps are non-nil copies; what f does reaches the new Client only.
func TestWithCopies(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, withDoc, &openapi.Options{
		Server:    w.URL + "/{v}",
		Header:    http.Header{"X-Base": {"b"}},
		Variables: map[string]string{"v": "x"},
	})
	var captured http.Header
	d := c.With(func(o *openapi.Options) {
		if o.Header == nil || o.Variables == nil || o.Codecs == nil || o.Credentials == nil {
			t.Errorf("f received nil maps: %+v", o)
		}
		if o.Header.Get("X-Base") != "b" || o.Variables["v"] != "x" || o.Server != w.URL+"/{v}" {
			t.Errorf("f did not receive c's Options: %+v", o)
		}
		o.Header.Del("X-Base")
		o.Header.Set("X-Derived", "d")
		o.Variables["v"] = "y"
		captured = o.Header
	})
	// Changing the maps f saw, after With returns, changes nothing.
	if captured != nil {
		captured.Set("X-Late", "late")
	}

	mustCall(t, c, "listPets", nil, nil)
	base := w.last(t)
	mustCall(t, d, "listPets", nil, nil)
	derived := w.last(t)
	if base.RequestURI != "/x/pets" || base.Header.Get("X-Base") != "b" || base.Header.Get("X-Derived") != "" {
		t.Errorf("c sent %s with X-Base %q, X-Derived %q; f affected c", base.RequestURI, base.Header.Get("X-Base"), base.Header.Get("X-Derived"))
	}
	if derived.RequestURI != "/y/pets" || derived.Header.Get("X-Base") != "" || derived.Header.Get("X-Derived") != "d" {
		t.Errorf("derived sent %s with X-Base %q, X-Derived %q", derived.RequestURI, derived.Header.Get("X-Base"), derived.Header.Get("X-Derived"))
	}
	if derived.Header.Get("X-Late") != "" {
		t.Errorf("a change after With reached the derived Client")
	}

	// Deriving again starts from the derived Options.
	e := d.With(func(o *openapi.Options) { o.Header.Set("X-Third", "3") })
	mustCall(t, e, "listPets", nil, nil)
	if got := w.last(t); got.Header.Get("X-Derived") != "d" || got.Header.Get("X-Third") != "3" || got.RequestURI != "/y/pets" {
		t.Errorf("a Client derived twice sent %s with %v", got.RequestURI, got.Header)
	}
	mustCall(t, d, "listPets", nil, nil)
	if w.last(t).Header.Get("X-Third") != "" {
		t.Errorf("deriving from d changed d")
	}
}

// Resetting a field to its zero value restores the default.
func TestWithResetsToDefault(t *testing.T) {
	w := newWire(t, jsonAnswer(200, `"0123456789"`))
	c := parseFor(t, w, withDoc, &openapi.Options{Server: w.URL + "/second", MaxBodyBytes: 4})
	var s string
	if _, err := c.Call(t.Context(), "listPets", nil, &s); err == nil {
		t.Errorf("MaxBodyBytes 4 did not bound the body")
	}
	d := c.With(func(o *openapi.Options) { o.MaxBodyBytes = 0 })
	mustCall(t, d, "listPets", nil, &s)

	// With Server reset, the two usable servers need a selection again.
	before := w.count()
	resp, err := c.With(func(o *openapi.Options) { o.Server = "" }).Call(t.Context(), "listPets", nil, nil)
	re := asRequestError(t, err)
	if resp != nil || w.count() != before {
		t.Errorf("a call with several usable servers was sent")
	}
	wantAnyKey(t, "Settings", re.Settings, "Options.Server", "Options.ServerID", "Options.BaseURL")
}

// A derived Client skips Load's checks for unused names in Variables and
// MediaType; such names have no effect (Options.MediaType is a preference:
// doc.go, Configuration).
func TestWithSkipsUnusedNameChecks(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, withDoc, &openapi.Options{Server: w.URL + "/second"})
	d := c.With(func(o *openapi.Options) {
		o.Variables["nowhere"] = "x"
		o.MediaType = "application/never"
	})
	mustCall(t, d, "listPets", nil, nil)
	if got := w.last(t).RequestURI; got != "/second/pets" {
		t.Errorf("request target %q", got)
	}
	// The unused MediaType is a preference no operation offers: createPet
	// still needs its own selection.
	before := w.count()
	resp, err := d.Call(t.Context(), "createPet", &openapi.Input{Body: map[string]int{"a": 1}}, nil)
	re := asRequestError(t, err)
	if resp != nil || w.count() != before {
		t.Errorf("a body with two declared types and no selection was sent")
	}
	wantAnyKey(t, "Settings", re.Settings, "Input.MediaType", "Options.MediaType")
}

// Other Options the document cannot use refuse each call they affect.
func TestWithUnusableOptionsRefuseCalls(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, withDoc, nil)
	tests := []struct {
		name string
		f    func(*openapi.Options)
		key  string // a Settings key, or "" to check only the type
	}{
		{"Server matches none", func(o *openapi.Options) { o.Server = "https://nowhere.example.test" }, "Options.Server"},
		{"ServerID matches none", func(o *openapi.Options) { o.ServerID = "no-such-id" }, "Options.ServerID"},
		{"BaseURL relative", func(o *openapi.Options) { o.BaseURL = "/v1" }, "Options.BaseURL"},
		{"BaseURL with query", func(o *openapi.Options) { o.BaseURL = w.URL + "/v1?x=1" }, "Options.BaseURL"},
		{"Server with ServerID", func(o *openapi.Options) { o.Server = w.URL + "/second"; o.ServerID = "x" }, ""},
		{"Header always refused", func(o *openapi.Options) { o.Header.Set("Content-Type", "text/plain") }, "Options.Header"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := c.With(tt.f).Call(t.Context(), "listPets", nil, nil)
			re := refusedBeforeSending(t, w, resp, err)
			if tt.key != "" {
				wantKeys(t, "Settings", re.Settings, false, tt.key)
			}
		})
	}
}

// describe.go, Server.ID: "It is stable for the same loaded document and
// retained by derived Clients." client.go, Options.ServerID: "With
// preserves IDs from the shared document."
func TestWithPreservesServerIDs(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, withDoc, nil)
	d := c.With(func(o *openapi.Options) { o.Header.Set("X", "1") })
	a, b := mustOp(t, c, "listPets").Servers, mustOp(t, d, "listPets").Servers
	if len(a) != 2 || len(b) != 2 || a[0].ID != b[0].ID || a[1].ID != b[1].ID {
		t.Fatalf("server IDs differ between c and a derived Client")
	}
	mustCall(t, d.With(func(o *openapi.Options) { o.ServerID = a[1].ID }), "listPets", nil, nil)
	if got := w.last(t).RequestURI; got != "/second/pets" {
		t.Errorf("request target %q, want /second/pets", got)
	}
}

// client.go, Options.HTTPClient replaced for a derived Client only.
func TestWithHTTPClient(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, withDoc, &openapi.Options{Server: w.URL + "/second"})
	ct := &countingTransport{}
	d := c.With(func(o *openapi.Options) { o.HTTPClient = &http.Client{Transport: ct} })
	mustCall(t, c, "listPets", nil, nil)
	if ct.count() != 0 {
		t.Errorf("c used the derived Client's transport")
	}
	mustCall(t, d, "listPets", nil, nil)
	if ct.count() != 1 {
		t.Errorf("the derived Client's transport carried %d requests, want 1", ct.count())
	}
}

// client.go, Client: "It is safe for concurrent use"; With "is cheap enough
// to call for each tenant or each call". Run with -race.
func TestWithConcurrent(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, withDoc, &openapi.Options{Server: w.URL + "/second", Header: http.Header{"X-Base": {"b"}}})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			d := c.With(func(o *openapi.Options) {
				o.Header.Set("X-N", string(rune('a'+i)))
				o.Variables["v"] = "z"
			})
			if _, err := d.Call(t.Context(), "listPets", nil, nil); err != nil {
				t.Errorf("Call: %v", err)
			}
			if _, err := c.Call(t.Context(), "listPets", nil, nil); err != nil {
				t.Errorf("Call: %v", err)
			}
		})
	}
	wg.Wait()
	if w.count() != 32 {
		t.Errorf("server received %d requests, want 32", w.count())
	}
	for _, r := range w.requests() {
		if r.Header.Get("X-Base") != "b" {
			t.Errorf("a request lost X-Base")
		}
	}
}
