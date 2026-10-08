package openapi_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Cookie parameters in OpenAPI 3.1: style form (OAS 3.1.2 section
// 4.8.12.2.2: the default "for "cookie" - "form""; explode true by default),
// written into one field (doc.go, Fixed rules, Cookies: "one Cookie field
// holding, joined by "; ", the parameters in declared order"; OAS 3.1.2
// Appendix D.1: the form "?" prefix is stripped, and name=value pairs in
// cookies "are delimited by a semicolon followed by a space character rather
// than &"; RFC 6265 section 4.2.1: cookie-string = cookie-pair *( ";" SP
// cookie-pair )), with values and names percent-encoded (doc.go, Fixed rules,
// Percent-encoding: "path and query values (content-serialized ones included,
// application/x-www-form-urlencoded too), form-style cookie values, and
// parameter and member names encode every byte outside RFC 3986's unreserved
// set as %XX in uppercase hex").

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

// An OpenAPI 3.1 form-style cookie value holding a ";" or a control
// character is percent-encoded, not refused (doc.go, Fixed rules,
// Percent-encoding: form-style cookie values "encode every byte outside RFC
// 3986's unreserved set"; only "a cookie value written as given that holds a
// ";" or an ASCII control character is refused").
func TestCookieValuesEncoded(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(cookieDoc), nil)
	for _, tt := range []struct {
		key  string
		v    any
		want string
	}{
		{"a", "x;y", "a=x%3By"},
		{"a", "x; b=2", "a=x%3B%20b%3D2"},
		{"a", "x\ty", "a=x%09y"},
		{"a", "x\x01y", "a=x%01y"},
		{"a", "x\x7fy", "a=x%7Fy"},
		{"a", "x\r\ny", "a=x%0D%0Ay"},
		{"a", []string{"p;q", "r"}, "a=p%3Bq; a=r"},
		{"b", []string{"p;q", "r\n"}, "b=p%3Bq,r%0A"},
		{"d", map[string]string{"k;1": "v;2"}, "k%3B1=v%3B2"},
	} {
		got, re := callOne(t, w, c, "c", tt.key, tt.v)
		if re != nil {
			t.Errorf("%q: refused: %v; want %q", tt.v, re, tt.want)
			continue
		}
		if v := cookieOf(got); !slices.Equal(v, []string{tt.want}) {
			t.Errorf("%q: Cookie = %q, want [%q]", tt.v, v, tt.want)
		}
	}
}

// doc.go, Fixed rules, Cookies: "A required cookie parameter is given in
// Params or by a writer; a Cookie field never supplies it". With a Cookie
// field in Input.Header or Options.Header and no value, a required cookie
// parameter is missing, at Inputs[key]; the Cookie field itself is not
// refused, since no cookie parameter is sent. A writer supplies it; then the
// field is refused, as for any call that sends cookie parameters.
func TestCookieFieldNeverSuppliesRequired(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/r":{"get":{"operationId":"r","parameters":[{"name":"k","in":"cookie","required":true,"schema":{}}]}}`)
	c := parseFor(t, w, doc, nil)
	field := http.Header{"Cookie": {"k=1"}}
	for name, tt := range map[string]struct {
		c  *openapi.Client
		in *openapi.Input
	}{
		"Input.Header":   {c, &openapi.Input{Header: field}},
		"Options.Header": {c.With(func(o *openapi.Options) { o.Header.Set("Cookie", "k=1") }), nil},
	} {
		before := w.count()
		resp, err := tt.c.Call(t.Context(), "r", tt.in, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, name+" Inputs", re.Inputs, true, "k")
		if _, ok := re.Settings[name]; ok {
			t.Errorf("%s: the Cookie field is refused, but no cookie parameter is sent", name)
		}
	}
	writer := map[string]func(*http.Request) error{"k": func(r *http.Request) error {
		r.AddCookie(&http.Cookie{Name: "k", Value: "2"})
		return nil
	}}
	mustCall(t, c, "r", &openapi.Input{ParamWriters: writer}, nil)
	if got := cookieOf(w.last(t)); !slices.Equal(got, []string{"k=2"}) {
		t.Errorf("Cookie = %q, want [k=2] from the writer", got)
	}
	mustCall(t, c, "r", &openapi.Input{Params: map[string]any{"k": "3"}}, nil)
	if got := cookieOf(w.last(t)); !slices.Equal(got, []string{"k=3"}) {
		t.Errorf("Cookie = %q, want [k=3]", got)
	}
	before := w.count()
	resp, err := c.Call(t.Context(), "r", &openapi.Input{Params: map[string]any{"k": "3"}, Header: field}, nil)
	wantKeys(t, "Settings", refusedSince(t, w, before, resp, err).Settings, false, "Input.Header")
}
