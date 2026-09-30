package openapi_test

import (
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Security alternatives in stage 1: an operation with no requirement, or
// whose sole or selected alternative is anonymous, proceeds; a selected
// alternative that needs a scheme is refused before sending (stage 1 has no
// credential placement, and later stages refuse it without a credential).

const securityDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],"paths":{
	"/open":{"get":{"operationId":"open"}},
	"/anon":{"get":{"operationId":"anon","security":[{}]}},
	"/optional":{"get":{"operationId":"optional","security":[{},{"api_key":[]}]}},
	"/keyed":{"get":{"operationId":"keyed","security":[{"api_key":[]}]}}
},"components":{"securitySchemes":{"api_key":{"type":"apiKey","in":"header","name":"X-API-Key"}}}}`

// client.go, Request.Security and Response.Security: "the Key of the
// security alternative ... "{}" for the anonymous one, or empty when the
// operation takes no credentials". client.go, Input.Security: "A key the
// operation does not list refuses the call, except that "{}" also permits
// an operation whose Security is empty."
func TestAnonymousSecurity(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, securityDoc, nil)
	tests := []struct {
		key, security, want string
	}{
		{"open", "", ""},
		{"anon", "", "{}"},
		{"anon", "{}", "{}"},
		{"optional", "{}", "{}"},
	}
	for _, tt := range tests {
		in := &openapi.Input{Security: tt.security}
		req := mustPrepare(t, c, tt.key, in)
		if req.Security != tt.want {
			t.Errorf("%s with %q: Request.Security = %q, want %q", tt.key, tt.security, req.Security, tt.want)
		}
		resp := mustCall(t, c, tt.key, in, nil)
		if resp.Security != tt.want {
			t.Errorf("%s with %q: Response.Security = %q, want %q", tt.key, tt.security, resp.Security, tt.want)
		}
		if v := w.last(t).Header.Values("X-API-Key"); v != nil {
			t.Errorf("%s: sent X-API-Key %q", tt.key, v)
		}
	}
	// "{}" permits an operation with no requirement.
	mustCall(t, c, "open", &openapi.Input{Security: "{}"}, nil)
}

// doc.go, Configuration: "One security alternative selects itself. Several
// require Options.Security, Options.SecurityKey or Input.Security,
// including an anonymous alternative." errors.go, RequestError.Settings:
// "Several security alternatives with none selected are keyed
// "Options.Security", the error naming Options.SecurityKey and
// Input.Security too."
func TestSeveralAlternativesNeedASelection(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, securityDoc, nil)
	resp, err := c.Call(t.Context(), "optional", nil, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Settings", re.Settings, false, "Options.Security")
	for _, name := range []string{"Options.SecurityKey", "Input.Security"} {
		if !strings.Contains(re.Error(), name) {
			t.Errorf("error %q does not name %s", re.Error(), name)
		}
	}
}

// client.go, Input.Security: "A key the operation does not list refuses the
// call"; keyed Input.Security (errors.go, RequestError.Settings).
func TestUnlistedSecurityKeyRefused(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, securityDoc, nil)
	resp, err := c.Call(t.Context(), "anon", &openapi.Input{Security: `{"api_key":[]}`}, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Settings", re.Settings, false, "Input.Security")
}

// Stage brief, Security: "an operation whose selected alternative needs any
// scheme is refused". doc.go, Credentials: "A call is refused, never sent
// without the authorization the caller selected, when a scheme in its
// selected alternative has no credential".
func TestAlternativeNeedingASchemeRefused(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, securityDoc, nil)
	for _, tt := range []struct {
		key string
		in  *openapi.Input
	}{
		{"keyed", nil},
		{"optional", &openapi.Input{Security: `{"api_key":[]}`}},
	} {
		resp, err := c.Call(t.Context(), tt.key, tt.in, nil)
		refusedBeforeSending(t, w, resp, err)
		if req, err := c.Prepare(tt.key, tt.in); req != nil || err == nil {
			t.Errorf("%s: Prepare = %v, %v; want a refusal", tt.key, req, err)
		}
	}
}
