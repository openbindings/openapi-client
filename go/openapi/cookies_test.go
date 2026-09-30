package openapi_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Cookie parameters in OpenAPI 3.1: style form (OAS 3.1.2 section
// 4.8.12.2.2: the default "for "cookie" - "form""; explode true by default),
// written into one field (doc.go, Fixed rules, Cookies: "one Cookie field,
// pairs joined by "; ", parameters in declared order"; OAS 3.1.2 Appendix
// D.1: the form "?" prefix is stripped, and name=value pairs in cookies "are
// delimited by a semicolon followed by a space character rather than &";
// RFC 6265 section 4.2.1: cookie-string = cookie-pair *( ";" SP
// cookie-pair )), with values and names percent-encoded (doc.go, Fixed
// rules, Percent-encoding: "path, query and cookie values ... and parameter
// and member names encode every byte outside RFC 3986's unreserved set").

const cookieDoc = `"/c":{
	"parameters":[{"name":"a","in":"cookie","schema":{}}],
	"get":{"operationId":"c","parameters":[
		{"name":"b","in":"cookie","explode":false,"schema":{}},
		{"name":"c","in":"cookie","schema":{}},
		{"name":"d","in":"cookie","style":"form","explode":true,"schema":{}},
		{"name":"X-H","in":"header","schema":{}}]}},
	"/plain":{"get":{"operationId":"plain"}},
	"/o":{
		"parameters":[{"name":"a","in":"cookie","schema":{}},{"name":"b","in":"cookie","schema":{}}],
		"get":{"operationId":"override","parameters":[{"name":"c","in":"cookie","schema":{}},{"name":"a","in":"cookie","explode":false,"schema":{}}]}},
	"/hc":{"get":{"operationId":"cookieHeader","parameters":[{"name":"Cookie","in":"header","schema":{}}]}}`

// cookieOf returns the request's Cookie field values.
func cookieOf(r rec) []string { return r.Header.Values("Cookie") }

func TestCookieParams(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(cookieDoc), nil)
	tests := []struct {
		name   string
		params map[string]any
		want   string // the single Cookie field, or "" for none
	}{
		{"every style and shape",
			map[string]any{"a": "1", "b": []string{"x", "y"}, "c": []string{"p", "q"}, "d": map[string]string{"k": "v", "l": "w"}},
			"a=1; b=x,y; c=p; c=q; k=v; l=w"},
		{"exploded struct", map[string]any{"d": colorRGB}, "R=100; G=200; B=150"},
		{"object without explode", map[string]any{"b": map[string]string{"k": "v"}}, "b=k,v"},
		{"some", map[string]any{"a": "1", "d": map[string]string{"k": "v"}}, "a=1; k=v"},
		{"values encoded", map[string]any{"a": "x y,z/é", "b": []string{"a b", "c,d"}}, "a=x%20y%2Cz%2F%C3%A9; b=a%20b,c%2Cd"},
		{"member names encoded", map[string]any{"d": map[string]string{"k y": "v"}}, "k%20y=v"},
		{"empty string", map[string]any{"a": ""}, "a="},
		{"numbers and booleans", map[string]any{"a": 1.5, "c": []any{true, 2}}, "a=1.5; c=true; c=2"},
		{"undefined skipped", map[string]any{"a": nil, "b": []int{}, "c": "x", "d": map[string]any{}}, "c=x"},
		{"none", map[string]any{"X-H": "h"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, c, "c", &openapi.Input{Params: tt.params}, nil)
			got := cookieOf(w.last(t))
			if tt.want == "" {
				if got != nil {
					t.Errorf("Cookie = %q, want none", got)
				}
				return
			}
			if !slices.Equal(got, []string{tt.want}) {
				t.Errorf("Cookie = %q, want one field [%q]", got, tt.want)
			}
		})
	}
	mustCall(t, c, "override", &openapi.Input{Params: map[string]any{"c": "y", "b": "x", "a": []int{1, 2}}}, nil)
	if got := cookieOf(w.last(t)); !slices.Equal(got, []string{"a=1,2; b=x; c=y"}) {
		t.Errorf("override: Cookie = %q, want [\"a=1,2; b=x; c=y\"] (doc.go, Fixed rules, Order)", got)
	}
}

// doc.go, Fixed rules, Cookies: "A Cookie field in Options.Header or
// Input.Header is refused when the call sends cookie parameters ..., and a
// value for a header parameter named Cookie, whose effect OpenAPI leaves
// undefined, is refused at its key" (OAS 3.1.2 section 4.8.12.2.1: "the
// effect of defining a cookie parameter that way is undefined"). The
// conflicting field is keyed by the Header that set it (errors.go,
// RequestError.Settings: "A setting the document cannot use, or one that
// conflicts with another, is keyed by its field"). A call that sends no
// cookie parameter keeps the field.
func TestCookieFieldConflicts(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(cookieDoc), nil)
	given := map[string]any{"a": "1"}

	before := w.count()
	resp, err := c.Call(t.Context(), "c", &openapi.Input{Params: given, Header: http.Header{"Cookie": {"s=1"}}}, nil)
	wantKeys(t, "Settings", refusedSince(t, w, before, resp, err).Settings, false, "Input.Header")

	for name, opts := range map[string]*openapi.Client{
		"Load": parseFor(t, w, doc31(cookieDoc), &openapi.Options{Header: http.Header{"Cookie": {"s=1"}}}),
		"With": c.With(func(o *openapi.Options) { o.Header.Set("Cookie", "s=1") }),
	} {
		before := w.count()
		resp, err := opts.Call(t.Context(), "c", &openapi.Input{Params: given}, nil)
		wantKeys(t, name+" Settings", refusedSince(t, w, before, resp, err).Settings, false, "Options.Header")

		// No cookie parameter sent: the field is used as given.
		mustCall(t, opts, "c", &openapi.Input{Params: map[string]any{"a": nil}}, nil)
		if got := cookieOf(w.last(t)); !slices.Equal(got, []string{"s=1"}) {
			t.Errorf("%s: Cookie = %q, want [s=1]", name, got)
		}
		mustCall(t, opts, "plain", nil, nil)
		if got := cookieOf(w.last(t)); !slices.Equal(got, []string{"s=1"}) {
			t.Errorf("%s plain: Cookie = %q, want [s=1]", name, got)
		}
	}
	mustCall(t, c, "plain", &openapi.Input{Header: http.Header{"Cookie": {"s=2"}}}, nil)
	if got := cookieOf(w.last(t)); !slices.Equal(got, []string{"s=2"}) {
		t.Errorf("plain: Cookie = %q, want [s=2]", got)
	}

	before = w.count()
	resp, err = c.Call(t.Context(), "cookieHeader", &openapi.Input{Params: map[string]any{"Cookie": "s=1"}}, nil)
	wantKeys(t, "Inputs", refusedSince(t, w, before, resp, err).Inputs, true, "Cookie")
	mustCall(t, c, "cookieHeader", nil, nil)
}

// doc.go, Fixed rules, Percent-encoding: cookie values are percent-encoded,
// and "a cookie value holding a ";" or a control character is refused". For
// an OpenAPI 3.1 form-style cookie the contract does not say which applies
// (contract question), so either is accepted; never is a raw ";" or control
// character written into the field.
func TestCookieSeparatorsNeverRaw(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(cookieDoc), nil)
	for _, tt := range []struct {
		v       any
		encoded string
	}{
		{"x;y", "a=x%3By"},
		{"x; b=2", "a=x%3B%20b%3D2"},
		{"x\ty", "a=x%09y"},
		{"x\x01y", "a=x%01y"},
		{"x\x7fy", "a=x%7Fy"},
		{[]string{"p;q", "r"}, "a=p%3Bq; a=r"},
	} {
		got, re := callOne(t, w, c, "c", "a", tt.v)
		if re != nil {
			wantKeys(t, "Inputs", re.Inputs, true, "a")
			continue
		}
		if v := cookieOf(got); !slices.Equal(v, []string{tt.encoded}) {
			t.Errorf("%q: Cookie = %q, want [%q] or a refusal at Inputs[\"a\"]", tt.v, v, tt.encoded)
		}
	}
}
