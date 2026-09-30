package openapi_test

import (
	"net/http"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 3, credential and parameter destinations (brief, Scope: "Credential
// and parameter destinations"): a declared parameter at the credential's
// destination, header settings conflicting with the credential's field, and
// FromTransport, which takes part in no destination rule.

// destDoc declares, for each apiKey location, an operation with a required
// parameter at the credential's destination, and an anonymous alternative
// that leaves the parameter to the caller.
var destDoc = doc31(`
	"/qd":{"get":{"operationId":"queryDest","security":[{"key_q":[]},{}],"parameters":[
		{"name":"api_key","in":"query","required":true},{"name":"p","in":"query"}]}},
	"/hd":{"get":{"operationId":"headerDest","security":[{"key_h":[]},{}],"parameters":[
		{"name":"x-api-key","in":"header","required":true}]}},
	"/cd":{"get":{"operationId":"cookieDest","security":[{"key_c":[]},{}],"parameters":[
		{"name":"sid","in":"cookie","required":true},{"name":"c1","in":"cookie"}]}},
	"/bearer":{"get":{"operationId":"bearer","security":[{"bearer":[]},{}]}},
	"/cookie":{"get":{"operationId":"cookieKey","security":[{"key_c":[]}]}}`,
	`"components":{"securitySchemes":{
		"key_q":{"type":"apiKey","in":"query","name":"api_key"},
		"key_h":{"type":"apiKey","in":"header","name":"X-API-Key"},
		"key_c":{"type":"apiKey","in":"cookie","name":"sid"},
		"bearer":{"type":"http","scheme":"bearer"}
	}}`)

func destClient(t *testing.T, w *wire) *openapi.Client {
	t.Helper()
	return parseFor(t, w, destDoc, &openapi.Options{Credentials: map[string]openapi.Credential{
		"key_q": openapi.Secret(qSecret), "key_h": openapi.Secret(hSecret),
		"key_c": openapi.Secret(cSecret), "bearer": openapi.Secret(bToken),
	}})
}

// doc.go, Credentials: "A credential and a parameter never share a
// destination. A declared parameter at the header field, query name or
// cookie name the applied credential sets is supplied by the credential: a
// required one counts as given, and a call that also supplies it is refused
// at the parameter's key." client.go, Input.Params: a required parameter
// counts as given when "the applied credential supplies [it]";
// Input.ParamWriters: "Credential-destination collisions are still
// refused." Header names compare without regard to case (RFC 9110 section
// 5.1). Order: "query credentials last"; Cookies: "parameters in declared
// order, then credentials".
func TestCredentialSuppliesItsDestination(t *testing.T) {
	w := newWire(t, nil)
	c := destClient(t, w)
	key := func(alt string) *openapi.Input { return &openapi.Input{Security: alt} }

	mustCall(t, c, "queryDest", key(`{"key_q":[]}`), nil)
	wantURI(t, w.last(t), "/qd?api_key="+qSecret)
	mustCall(t, c, "queryDest", &openapi.Input{Security: `{"key_q":[]}`, Params: map[string]any{"p": "1"}}, nil)
	wantURI(t, w.last(t), "/qd?p=1&api_key="+qSecret)

	mustCall(t, c, "headerDest", key(`{"key_h":[]}`), nil)
	wantField(t, w.last(t).Header, "X-API-Key", hSecret)

	mustCall(t, c, "cookieDest", &openapi.Input{Security: `{"key_c":[]}`, Params: map[string]any{"c1": "v"}}, nil)
	wantField(t, w.last(t).Header, "Cookie", "c1=v; sid="+cSecret)

	writer := func(*http.Request) error { return nil }
	for _, tt := range []struct {
		key, alt, param string
		in              openapi.Input
	}{
		{"queryDest", `{"key_q":[]}`, "api_key", openapi.Input{Params: map[string]any{"api_key": "caller"}}},
		{"headerDest", `{"key_h":[]}`, "x-api-key", openapi.Input{Params: map[string]any{"x-api-key": "caller"}}},
		{"cookieDest", `{"key_c":[]}`, "sid", openapi.Input{Params: map[string]any{"sid": "caller"}}},
		{"queryDest", `{"key_q":[]}`, "api_key", openapi.Input{ParamWriters: map[string]func(*http.Request) error{"api_key": writer}}},
		{"headerDest", `{"key_h":[]}`, "x-api-key", openapi.Input{ParamWriters: map[string]func(*http.Request) error{"x-api-key": writer}}},
		{"cookieDest", `{"key_c":[]}`, "sid", openapi.Input{ParamWriters: map[string]func(*http.Request) error{"sid": writer}}},
	} {
		in := tt.in
		in.Security = tt.alt
		before := w.count()
		resp, err := c.Call(t.Context(), tt.key, &in, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Inputs", re.Inputs, false, tt.param)
		noSecrets(t, err, qSecret, hSecret, cSecret)
	}
}

// doc.go, Credentials: "Unselected alternatives have no effect on the
// call": with the anonymous alternative selected, the parameter is the
// caller's, and a required one is missing when not given (client.go,
// Input.Params: "a missing required parameter, refuses the call").
func TestUnselectedCredentialLeavesDestination(t *testing.T) {
	w := newWire(t, nil)
	c := destClient(t, w)
	anon := func(params map[string]any) *openapi.Input { return &openapi.Input{Security: "{}", Params: params} }

	for key, param := range map[string]string{"queryDest": "api_key", "headerDest": "x-api-key", "cookieDest": "sid"} {
		before := w.count()
		resp, err := c.Call(t.Context(), key, anon(nil), nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Inputs", re.Inputs, true, param)
	}
	mustCall(t, c, "queryDest", anon(map[string]any{"api_key": "mine"}), nil)
	wantURI(t, w.last(t), "/qd?api_key=mine")
	mustCall(t, c, "headerDest", anon(map[string]any{"x-api-key": "mine"}), nil)
	wantField(t, w.last(t).Header, "X-API-Key", "mine")
	mustCall(t, c, "cookieDest", anon(map[string]any{"sid": "mine"}), nil)
	wantField(t, w.last(t).Header, "Cookie", "sid=mine")
}

// doc.go, Credentials: "A FromTransport scheme places nothing, so it takes
// part in no destination rule": the parameter stays the caller's, required
// or not, and a header setting may set the field.
func TestFromTransportLeavesDestination(t *testing.T) {
	w := newWire(t, nil)
	c := destClient(t, w).With(func(o *openapi.Options) {
		for _, name := range []string{"key_q", "key_h", "key_c", "bearer"} {
			o.Credentials[name] = openapi.FromTransport()
		}
	})
	for key, alt := range map[string]string{"queryDest": `{"key_q":[]}`, "headerDest": `{"key_h":[]}`, "cookieDest": `{"key_c":[]}`} {
		before := w.count()
		resp, err := c.Call(t.Context(), key, &openapi.Input{Security: alt}, nil)
		refusedSince(t, w, before, resp, err)
	}
	mustCall(t, c, "queryDest", &openapi.Input{Security: `{"key_q":[]}`, Params: map[string]any{"api_key": "mine"}}, nil)
	wantURI(t, w.last(t), "/qd?api_key=mine")
	mustCall(t, c, "headerDest", &openapi.Input{Security: `{"key_h":[]}`, Header: http.Header{"X-Api-Key": {"mine"}}}, nil)
	wantField(t, w.last(t).Header, "X-API-Key", "mine")
	mustCall(t, c, "bearer", &openapi.Input{Security: `{"bearer":[]}`, Header: http.Header{"Authorization": {"Bearer mine"}}}, nil)
	wantField(t, w.last(t).Header, "Authorization", "Bearer mine")
	mustCall(t, c, "cookieKey", &openapi.Input{Header: http.Header{"Cookie": {"sid=mine"}}}, nil)
	wantField(t, w.last(t).Header, "Cookie", "sid=mine")
}

// doc.go, Header fields: "At either level, ... [refused is] a field that a
// header parameter the call supplies, or the call's credential, sets";
// Cookies: "A Cookie field in Options.Header or Input.Header is refused when
// the call sends cookie parameters or a cookie credential". errors.go,
// RequestError.Settings: "a header field that a supplied header parameter
// or the credential sets [is keyed] by the Header that set it". Names
// compare without regard to case. Without the credential (the anonymous
// alternative), the field is the caller's.
func TestHeaderSettingConflictsWithCredential(t *testing.T) {
	w := newWire(t, nil)
	c := destClient(t, w)
	tests := []struct {
		key, alt string
		field    http.Header
	}{
		{"bearer", `{"bearer":[]}`, http.Header{"Authorization": {"Bearer mine"}}},
		{"bearer", `{"bearer":[]}`, http.Header{"authorization": {"Bearer mine"}}},
		{"headerDest", `{"key_h":[]}`, http.Header{"X-Api-Key": {"mine"}}},
		{"headerDest", `{"key_h":[]}`, http.Header{"x-api-key": {"mine"}}},
		{"cookieKey", "", http.Header{"Cookie": {"a=1"}}},
		{"cookieDest", `{"key_c":[]}`, http.Header{"Cookie": {"a=1"}}},
	}
	for _, tt := range tests {
		for _, level := range []string{"Options.Header", "Input.Header"} {
			var d *openapi.Client
			in := &openapi.Input{Security: tt.alt}
			if level == "Options.Header" {
				d = c.With(func(o *openapi.Options) { o.Header = tt.field.Clone() })
			} else {
				d, in.Header = c, tt.field.Clone()
			}
			before := w.count()
			resp, err := d.Call(t.Context(), tt.key, in, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, level)
			noSecrets(t, err, bToken, hSecret, cSecret)
		}
	}

	// The anonymous alternative applies no credential.
	mustCall(t, c, "bearer", &openapi.Input{Security: "{}", Header: http.Header{"Authorization": {"Bearer mine"}}}, nil)
	wantField(t, w.last(t).Header, "Authorization", "Bearer mine")
	d := c.With(func(o *openapi.Options) { o.Header = http.Header{"X-Api-Key": {"mine"}} })
	mustCall(t, d, "headerDest", &openapi.Input{Security: "{}"}, nil)
	wantField(t, w.last(t).Header, "X-API-Key", "mine")
}
