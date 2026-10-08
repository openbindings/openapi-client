package openapi_test

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Input.ParamWriters (client.go): "overrides the serialization of individual
// parameters, keyed by Param.Key. A writer counts as supplying a required
// parameter; an unknown key, a nil writer, and the same key in Params and
// ParamWriters, are refused at that key. After the client serializes the other
// parameters and the body, Prepare calls writers in the operation's parameter
// order with its unsigned *http.Request. The writer may edit URL.Path,
// URL.RawPath, URL.RawQuery, headers or cookies ... A path parameter's {name}
// remains in both URL.Path and URL.RawPath until its writer replaces it in
// both, keeping RawPath an encoding of Path; an unresolved path token after all
// writers refuses preparation. ... A writer also bypasses that parameter's
// serialization Err, including for a required parameter, when its Key is known.
// It cannot bypass a defect in the operation or an unresolved parameter
// reference whose Key cannot be determined ... return an error on failure; that
// error is reported at RequestError.Inputs[key]." errors.go,
// RequestError.Inputs: "a ParamWriters failure or conflict", keyed by
// Param.Key.

type writers = map[string]func(*http.Request) error

// appendQuery is a writer that appends pair to the query.
func appendQuery(pair string) func(*http.Request) error {
	return func(r *http.Request) error {
		if r.URL.RawQuery != "" {
			r.URL.RawQuery += "&"
		}
		r.URL.RawQuery += pair
		return nil
	}
}

const writerDoc = `
	"/u/{user}/items/{id}":{"post":{"operationId":"path","parameters":[
		{"name":"user","in":"path","required":true,"schema":{}},
		{"name":"id","in":"path","required":true,"schema":{}},
		{"name":"q","in":"query","schema":{}}],
		"requestBody":{"content":{"application/json":{}}}}},
	"/k":{"get":{"operationId":"keys","parameters":[
		{"name":"dup","in":"query","schema":{}},
		{"name":"dup","in":"header","schema":{}},
		{"name":"X-H","in":"header","schema":{}},
		{"name":"a","in":"cookie","schema":{}},
		{"name":"b","in":"cookie","schema":{}}]}},
	"/r":{"get":{"operationId":"required","parameters":[{"name":"need","in":"query","required":true,"schema":{}}]}},
	"/e":{"get":{"operationId":"errs","parameters":[
		{"name":"se","in":"query","required":true,"style":"spaceDelimited","explode":true,"schema":{}},
		{"name":"mx","in":"query","required":true,"style":"matrix","schema":{}},
		{"name":"two","in":"query","required":true,"content":{"application/json":{},"text/plain":{}}}]}},
	"/bad":{"get":{"operationId":"badRef","parameters":[
		{"name":"q","in":"query","schema":{}},
		{"$ref":"#/components/parameters/Missing"}]}},
	"/o":{
		"parameters":[{"name":"p1","in":"query","schema":{}},{"name":"p2","in":"header","schema":{}},{"name":"p3","in":"query","schema":{}}],
		"get":{"operationId":"order","parameters":[
			{"name":"p4","in":"cookie","schema":{}},{"name":"p2","in":"header","explode":true,"schema":{}},
			{"name":"p5","in":"query","schema":{}},{"name":"p6","in":"query","schema":{}},
			{"name":"p7","in":"header","schema":{}},{"name":"p8","in":"query","schema":{}}]}}`

// Writers keyed by Param.Key, including a location-qualified key (client.go,
// Input.Params: "query.id"); header, cookie and query edits are sent.
func TestParamWritersKeysAndEdits(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(writerDoc), nil)
	var keys []string
	for _, p := range mustOp(t, c, "keys").Params {
		keys = append(keys, p.Key)
	}
	if !slices.Equal(keys, []string{"query.dup", "header.dup", "X-H", "a", "b"}) {
		t.Fatalf("keys %q", keys)
	}
	mustCall(t, c, "keys", &openapi.Input{
		Params: map[string]any{"header.dup": "h", "a": "1"},
		ParamWriters: writers{
			"query.dup": appendQuery("dup=%5Bw%5D"),
			"X-H":       func(r *http.Request) error { r.Header.Set("X-H", "from writer"); return nil },
			"b":         func(r *http.Request) error { r.AddCookie(&http.Cookie{Name: "b", Value: "2"}); return nil },
		},
	}, nil)
	got := w.last(t)
	if got.RequestURI != "/k?dup=%5Bw%5D" {
		t.Errorf("request target %q, want /k?dup=%%5Bw%%5D", got.RequestURI)
	}
	if v := got.Header.Values("Dup"); !slices.Equal(v, []string{"h"}) {
		t.Errorf("dup header = %q, want [h]", v)
	}
	if v := got.Header.Values("X-H"); !slices.Equal(v, []string{"from writer"}) {
		t.Errorf("X-H = %q, want [from writer]", v)
	}
	// The client's cookie pair first; the writer, run after, adds its own.
	if v := got.Header.Values("Cookie"); !slices.Equal(v, []string{"a=1; b=2"}) {
		t.Errorf("Cookie = %q, want [a=1; b=2]", v)
	}
}

// A path writer finds its {name} in both URL.Path and URL.RawPath, the other
// path parameters, the query and the body already serialized, and replaces
// the token in both.
func TestParamWritersPathToken(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(writerDoc), nil)
	var seen struct {
		path, raw, query, ct, body string
	}
	req := mustPrepare(t, c, "path", &openapi.Input{
		Params: map[string]any{"user": "x y", "q": "a b"},
		Body:   map[string]int{"n": 1},
		ParamWriters: writers{"id": func(r *http.Request) error {
			seen.path, seen.raw, seen.query, seen.ct = r.URL.Path, r.URL.RawPath, r.URL.RawQuery, r.Header.Get("Content-Type")
			if r.GetBody != nil {
				if b, err := r.GetBody(); err == nil {
					data, _ := io.ReadAll(b)
					seen.body = string(data)
				}
			}
			r.URL.Path = strings.Replace(r.URL.Path, "{id}", "a/b", 1)
			r.URL.RawPath = strings.Replace(r.URL.RawPath, "{id}", "a%2Fb", 1)
			return nil
		}},
	})
	if seen.path != "/u/x y/items/{id}" {
		t.Errorf("URL.Path at the writer = %q, want /u/x y/items/{id}", seen.path)
	}
	if seen.raw != "/u/x%20y/items/{id}" {
		t.Errorf("URL.RawPath at the writer = %q, want /u/x%%20y/items/{id}", seen.raw)
	}
	if seen.query != "q=a%20b" {
		t.Errorf("URL.RawQuery at the writer = %q, want q=a%%20b", seen.query)
	}
	if seen.ct != "application/json" || string(trimNL([]byte(seen.body))) != `{"n":1}` {
		t.Errorf("body at the writer: Content-Type %q, GetBody %q; want the encoded body", seen.ct, seen.body)
	}
	if got := req.HTTP.URL.EscapedPath(); got != "/u/x%20y/items/a%2Fb" {
		t.Errorf("prepared path %q, want /u/x%%20y/items/a%%2Fb", got)
	}
	sendAndClose(t, req)
	if got := w.last(t).RequestURI; got != "/u/x%20y/items/a%2Fb?q=a%20b" {
		t.Errorf("request target %q, want /u/x%%20y/items/a%%2Fb?q=a%%20b", got)
	}
}

// "an unresolved path token after all writers refuses preparation": a
// writer that leaves the token, or replaces it in URL.Path only, fails as a
// ParamWriters failure, at its key.
func TestParamWritersUnresolvedToken(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(writerDoc), nil)
	for name, fn := range map[string]func(*http.Request) error{
		"left":      func(r *http.Request) error { return nil },
		"Path only": func(r *http.Request) error { r.URL.Path = strings.Replace(r.URL.Path, "{id}", "7", 1); return nil },
	} {
		in := &openapi.Input{Params: map[string]any{"user": "u"}, Body: map[string]int{}, ParamWriters: writers{"id": fn}}
		req, err := c.Prepare("path", in)
		if req != nil {
			t.Errorf("%s: Prepare returned a Request", name)
		}
		wantKeys(t, name+" Inputs", asRequestError(t, err).Inputs, true, "id")
		resp, err := c.Call(t.Context(), "path", in, nil)
		refusedBeforeSending(t, w, resp, err)
	}
}

// A writer counts as supplying a required parameter.
func TestParamWritersSupplyRequired(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(writerDoc), nil)
	mustCall(t, c, "required", &openapi.Input{ParamWriters: writers{"need": appendQuery("need=1")}}, nil)
	if got := w.last(t).RequestURI; got != "/r?need=1" {
		t.Errorf("request target %q, want /r?need=1", got)
	}
	before := w.count()
	resp, err := c.Call(t.Context(), "required", nil, nil)
	wantKeys(t, "Inputs", refusedSince(t, w, before, resp, err).Inputs, true, "need")
}

// A writer bypasses its parameter's serialization Err, including for a
// required parameter: an undefined combination (spaceDelimited with explode
// true), a style the location does not allow (matrix in a query), and a
// content map with two entries. Without writers each required parameter is
// refused.
func TestParamWritersBypassSerializationErr(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(writerDoc), nil)
	for _, p := range mustOp(t, c, "errs").Params {
		if p.Err == nil {
			t.Errorf("%s: Param.Err = nil", p.Key)
		}
	}
	before := w.count()
	resp, err := c.Call(t.Context(), "errs", nil, nil)
	wantKeys(t, "Inputs", refusedSince(t, w, before, resp, err).Inputs, true, "se", "mx", "two")
	mustCall(t, c, "errs", &openapi.Input{ParamWriters: writers{
		"se":  appendQuery("se=a&se=b"),
		"mx":  appendQuery("mx=1"),
		"two": appendQuery("two=%22x%22"),
	}}, nil)
	if got := w.last(t).RequestURI; got != "/e?se=a&se=b&mx=1&two=%22x%22" {
		t.Errorf("request target %q", got)
	}
}

// "It cannot bypass a defect in the operation or an unresolved parameter
// reference whose Key cannot be determined": the call is refused with an
// error wrapping the operation's Err (describe.go, Operation.Err: "an
// unresolvable parameter ... reference, whose identity and requiredness
// cannot be known").
func TestParamWritersCannotBypassOperationErr(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(writerDoc), nil)
	op := mustOp(t, c, "badRef")
	if op.Err == nil {
		t.Fatal("Operation.Err = nil for an unresolvable parameter reference")
	}
	_, err := c.Prepare("badRef", &openapi.Input{ParamWriters: writers{"q": appendQuery("q=1")}})
	if !errors.Is(err, op.Err) {
		t.Errorf("Prepare = %v, want an error wrapping Operation.Err", err)
	}
	resp, err := c.Call(t.Context(), "badRef", &openapi.Input{ParamWriters: writers{"q": appendQuery("q=1")}}, nil)
	refusedBeforeSending(t, w, resp, err)
	if !errors.Is(err, op.Err) {
		t.Errorf("Call = %v, want an error wrapping Operation.Err", err)
	}
}

// Writers run in the operation's parameter order (doc.go, Fixed rules,
// Order: the path item's parameters, then the operation's, an overriding
// parameter taking the place of the one it overrides), whatever the map's
// order.
func TestParamWritersOrder(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(writerDoc), nil)
	var want []string
	for _, p := range mustOp(t, c, "order").Params {
		want = append(want, p.Key)
	}
	if !slices.Equal(want, []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8"}) {
		t.Fatalf("parameter order %q", want)
	}
	for range 20 {
		var ran []string
		ws := writers{}
		for _, k := range want {
			ws[k] = func(r *http.Request) error { ran = append(ran, k); return nil }
		}
		mustPrepare(t, c, "order", &openapi.Input{ParamWriters: ws})
		if !slices.Equal(ran, want) {
			t.Fatalf("writers ran in the order %q, want %q", ran, want)
		}
	}
}

// Refusals: an unknown key; the same key in Params and ParamWriters; a
// writer's error, reported at Inputs[key] and wrapped. All are reported
// together, before anything is sent, and Prepare returns no Request.
func TestParamWritersRefusals(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(writerDoc), nil)
	errWriter := errors.New("writer failed")
	tests := []struct {
		name string
		in   *openapi.Input
		keys []string
	}{
		{"unknown key", &openapi.Input{ParamWriters: writers{"nope": appendQuery("x=1")}}, []string{"nope"}},
		{"name of a qualified key", &openapi.Input{ParamWriters: writers{"dup": appendQuery("x=1")}}, []string{"dup"}},
		{"key in Params too", &openapi.Input{Params: map[string]any{"X-H": "v"}, ParamWriters: writers{"X-H": appendQuery("x=1")}}, []string{"X-H"}},
		{"writer error", &openapi.Input{ParamWriters: writers{"a": func(*http.Request) error { return errWriter }}}, []string{"a"}},
		{"several", &openapi.Input{
			Params:       map[string]any{"b": "1"},
			ParamWriters: writers{"nope": appendQuery("x=1"), "b": appendQuery("b=1")},
		}, []string{"nope", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := c.Prepare("keys", tt.in)
			if req != nil {
				t.Errorf("Prepare returned a Request")
			}
			re := asRequestError(t, err)
			wantKeys(t, "Inputs", re.Inputs, true, tt.keys...)
			resp, err := c.Call(t.Context(), "keys", tt.in, nil)
			refusedBeforeSending(t, w, resp, err)
		})
	}
	_, err := c.Prepare("keys", &openapi.Input{ParamWriters: writers{"a": func(*http.Request) error { return errWriter }}})
	if re := asRequestError(t, err); !errors.Is(re.Inputs["a"], errWriter) || !errors.Is(err, errWriter) {
		t.Errorf("Inputs[\"a\"] = %v: the writer's error is not wrapped", re.Inputs["a"])
	}
}

// Example_parameterWriter as a runnable flow: a required parameter with a
// spelling the built-in serializer cannot produce (a bracketed name), given
// by its location-qualified key, while the client serializes the others.
func TestFlowParameterWriter(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/pets/search":{"get":{"operationId":"searchPets","parameters":[
		{"name":"filter","in":"query","required":true,"style":"form","schema":{"type":"object"}},
		{"name":"filter","in":"header","schema":{"type":"string"}},
		{"name":"limit","in":"query","schema":{"type":"integer"}}]}}`)
	c := parseFor(t, w, doc, nil)
	req, err := c.Prepare("searchPets", &openapi.Input{
		Params: map[string]any{"limit": 10},
		ParamWriters: writers{
			"query.filter": func(r *http.Request) error {
				if r.URL.RawQuery != "" {
					r.URL.RawQuery += "&"
				}
				r.URL.RawQuery += "filter%5Bname%5D=Rex"
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	resp := sendAndClose(t, req)
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if got := w.last(t).RequestURI; got != "/pets/search?limit=10&filter%5Bname%5D=Rex" {
		t.Errorf("request target %q, want /pets/search?limit=10&filter%%5Bname%%5D=Rex", got)
	}
}
