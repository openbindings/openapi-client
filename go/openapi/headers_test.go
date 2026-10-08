package openapi_test

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Header fields: Options.Header, Input.Header, what the client generates,
// and OperationFromContext.

const headerDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{
	"/h":{"get":{"operationId":"h","parameters":[
		{"name":"X-Param","in":"header","schema":{}},
		{"name":"X-Required","in":"header","required":true,"schema":{}}
	]}},
	"/plain":{"get":{"operationId":"plain"}},
	"/body":{"post":{"operationId":"body","requestBody":{"content":{"application/json":{}}}}}
}}`

// doc.go, Fixed rules, Header fields: "Options.Header and then Input.Header
// are applied over the generated fields: a field replaces the same field set
// before, and one with no values removes it".
func TestHeaderFieldsApplied(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, headerDoc, &openapi.Options{Header: http.Header{
		"X-Env":    {"staging"},
		"X-Multi":  {"a", "b"},
		"X-Remove": {"gone"},
		"X-Keep":   {"kept"},
	}})
	mustCall(t, c, "plain", &openapi.Input{Header: http.Header{
		"X-Env":    {"prod"},
		"X-Remove": {},
		"X-Call":   {"1"},
	}}, nil)
	got := w.only(t).Header
	for field, want := range map[string][]string{
		"X-Env":    {"prod"},
		"X-Multi":  {"a", "b"},
		"X-Remove": nil,
		"X-Keep":   {"kept"},
		"X-Call":   {"1"},
	} {
		if v := got.Values(field); !slices.Equal(v, want) {
			t.Errorf("%s = %q, want %q", field, v, want)
		}
	}

	// A nil value list has no values too.
	mustCall(t, c, "plain", &openapi.Input{Header: http.Header{"X-Keep": nil}}, nil)
	if v := w.last(t).Header.Values("X-Keep"); v != nil {
		t.Errorf("X-Keep = %q, want it removed", v)
	}
}

// doc.go, Fixed rules, Accept: "none is synthesized"; Header fields: "no
// User-Agent beyond net/http's". A body-less call has no Content-Type.
func TestNoSynthesizedHeaders(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, headerDoc, nil)
	mustCall(t, c, "plain", nil, nil)
	got := w.only(t)
	if v, ok := got.Header["Accept"]; ok {
		t.Errorf("Accept = %q, want none", v)
	}
	if v, ok := got.Header["Content-Type"]; ok {
		t.Errorf("Content-Type = %q on a request without a body", v)
	}
	if got.ContentLength != 0 || len(got.Body) != 0 {
		t.Errorf("a request without a body sent %d bytes", len(got.Body))
	}
	// net/http's default User-Agent.
	if ua := got.Header.Get("User-Agent"); ua != "Go-http-client/1.1" {
		t.Errorf("User-Agent = %q, want net/http's", ua)
	}

	// An Accept field the caller sets is sent as given.
	mustCall(t, c, "plain", &openapi.Input{Header: http.Header{"Accept": {"application/json, text/plain;q=0.5"}}}, nil)
	if v := w.last(t).Header.Values("Accept"); !slices.Equal(v, []string{"application/json, text/plain;q=0.5"}) {
		t.Errorf("Accept = %q", v)
	}
}

// doc.go, Fixed rules, Header fields: "At either level, Content-Type,
// Content-Length and Transfer-Encoding are refused". load.go, Load: Load
// fails on "a Header field that is always refused", keyed Options.Header;
// a call with such an Input.Header is refused at Input.Header. HTTP field
// names are case-insensitive (RFC 9110 section 5.1).
func TestHeaderFieldsAlwaysRefused(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, headerDoc, nil)
	for _, field := range []string{"Content-Type", "Content-Length", "Transfer-Encoding", "content-type"} {
		t.Run(field, func(t *testing.T) {
			h := http.Header{field: {"x"}}
			_, err := openapi.Parse(t.Context(), []byte(expand(headerDoc, w.URL)), w.URL+"/openapi.json", &openapi.Options{Header: h})
			wantKeys(t, "Load Settings", asRequestError(t, err).Settings, false, "Options.Header")

			resp, err := c.Call(t.Context(), "body", &openapi.Input{Body: map[string]any{}, Header: h}, nil)
			re := refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Settings", re.Settings, false, "Input.Header")
		})
	}
}

// doc.go, Fixed rules, Header fields: a field "that a header parameter the
// call supplies ... sets" is refused at either level; "a declared header
// parameter the call leaves unset may come from a header field".
// client.go, Input.Params: "a header parameter supplied as an Options.Header
// or Input.Header field counts as given".
func TestHeaderFieldsAndHeaderParameters(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, headerDoc, nil)

	// The required header parameter comes from Input.Header.
	mustCall(t, c, "h", &openapi.Input{Header: http.Header{"X-Required": {"from-field"}}}, nil)
	if v := w.last(t).Header.Values("X-Required"); !slices.Equal(v, []string{"from-field"}) {
		t.Errorf("X-Required = %q", v)
	}
	// Or from Options.Header.
	viaOptions := c.With(func(o *openapi.Options) { o.Header.Set("X-Required", "from-options") })
	mustCall(t, viaOptions, "h", nil, nil)
	if v := w.last(t).Header.Values("X-Required"); !slices.Equal(v, []string{"from-options"}) {
		t.Errorf("X-Required = %q", v)
	}
	// Or from Params, alongside an unrelated field.
	mustCall(t, c, "h", &openapi.Input{
		Params: map[string]any{"X-Required": "param", "X-Param": "p"},
		Header: http.Header{"X-Other": {"o"}},
	}, nil)
	got := w.last(t).Header
	if got.Get("X-Required") != "param" || got.Get("X-Param") != "p" || got.Get("X-Other") != "o" {
		t.Errorf("headers = %v", got)
	}

	// A supplied header parameter and a field for it at either level.
	before := w.count()
	for name, d := range map[string]struct {
		c  *openapi.Client
		in *openapi.Input
	}{
		"Input.Header": {c, &openapi.Input{
			Params: map[string]any{"X-Required": "r", "X-Param": "p"},
			Header: http.Header{"X-Param": {"field"}},
		}},
		"Options.Header": {c.With(func(o *openapi.Options) { o.Header.Set("X-Param", "field") }), &openapi.Input{
			Params: map[string]any{"X-Required": "r", "X-Param": "p"},
		}},
	} {
		resp, err := d.c.Call(t.Context(), "h", d.in, nil)
		re := asRequestError(t, err)
		if resp != nil {
			t.Errorf("%s: a refused call returned a Response", name)
		}
		// Which key reports the conflict (the setting or the parameter) is
		// not stated.
		if len(re.Settings) == 0 && len(re.Inputs) == 0 {
			t.Errorf("%s: the refusal names neither a setting nor an input", name)
		}
	}
	if w.count() != before {
		t.Errorf("a conflicting call was sent")
	}
}

// client.go, OperationFromContext: "returns the operation of a request that
// Prepare built or that a call sends, when ctx is that request's context or
// derives from it, and nil otherwise". client.go, Request.HTTP: "Its context
// carries the operation for OperationFromContext; the request Call, Send or
// Stream sends carries the context given to it instead, the operation still
// attached."
func TestOperationFromContext(t *testing.T) {
	w := newWire(t, nil)
	ct := &countingTransport{}
	type ctxKey struct{}
	var (
		mu   sync.Mutex
		seen []any
	)
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.Context().Value(ctxKey{}))
		mu.Unlock()
		return ct.RoundTrip(r)
	})
	c := parseFor(t, w, headerDoc, &openapi.Options{HTTPClient: &http.Client{Transport: rt}})

	mustCall(t, c, "plain", nil, nil)
	req := mustPrepare(t, c, "plain", nil)
	if op := openapi.OperationFromContext(req.HTTP.Context()); op == nil || op.Key != "plain" {
		t.Errorf("OperationFromContext(Request.HTTP.Context()) = %v, want plain", op)
	}
	ctx := context.WithValue(t.Context(), ctxKey{}, "caller value")
	resp, err := req.Send(ctx)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	resp.Body.Close()
	if _, err := req.Call(ctx, nil); err != nil {
		t.Fatalf("Request.Call: %v", err)
	}

	if ct.count() != 3 {
		t.Fatalf("transport carried %d requests, want 3", ct.count())
	}
	mu.Lock()
	defer mu.Unlock()
	for i, op := range ct.snapshot() {
		if op == nil || op.Key != "plain" {
			t.Errorf("request %d: OperationFromContext = %v, want plain", i, op)
		}
	}
	// The context given to Send and Call replaces the prepared one.
	if seen[1] != "caller value" || seen[2] != "caller value" {
		t.Errorf("transport saw context values %v, want the caller's for Send and Call", seen)
	}
	if openapi.OperationFromContext(context.Background()) != nil {
		t.Errorf("OperationFromContext(context.Background()) != nil")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
