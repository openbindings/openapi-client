package openapi_test

import (
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 3, selection (brief, Scope: "Selection"): security requirements
// from the root and the operation, the canonical key, one alternative
// selecting itself, several requiring a setting, the preference semantics
// of Options.Security and Options.SecurityKey against the exact
// Input.Security, the anonymous alternative, Request.Security and
// Response.Security. Stage 1's cases (an anonymous or absent requirement,
// several alternatives, an unlisted key, and an alternative that needs a
// credential) are kept.

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
		{"open", "{}", ""},
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

// doc.go, Credentials: "A call is refused, never sent without the
// authorization the caller selected, when a scheme in its selected
// alternative has no credential"; keyed Options.Credentials["api_key"]
// (errors.go, RequestError.Settings).
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
		re := refusedBeforeSending(t, w, resp, err)
		wantKeys(t, "Settings", re.Settings, true, credKey("api_key"))
		if req, err := c.Prepare(tt.key, tt.in); req != nil || err == nil {
			t.Errorf("%s: Prepare = %v, %v; want a refusal", tt.key, req, err)
		}
	}
}

// selDoc has root security and operations offering alternatives that
// share schemes, differ in scopes, or are anonymous.
var selDoc = doc31(`
	"/inherit":{"get":{"operationId":"inherit"}},
	"/none":{"get":{"operationId":"none","security":[]}},
	"/dup":{"get":{"operationId":"dup","security":[{"key_h":[]},{"key_h":[]}]}},
	"/choice":{"get":{"operationId":"choice","security":[{"key_h":[]},{"oauth":["read"]}]}},
	"/scopes":{"get":{"operationId":"scopes","security":[{"oauth":["read"]},{"oauth":["write"]}]}},
	"/pair":{"get":{"operationId":"pair","security":[{"key_h":[],"oauth":["read"]},{"key_h":[]}]}},
	"/twokinds":{"get":{"operationId":"twoKinds","security":[{"key_h":[]},{"key_q":[]}]}},
	"/optional":{"get":{"operationId":"optional","security":[{},{"key_h":[]}]}},
	"/anon":{"get":{"operationId":"anon","security":[{}]}}`,
	`"security":[{"key_h":[]}]`,
	`"components":{"securitySchemes":{
		"key_h":{"type":"apiKey","in":"header","name":"X-API-Key"},
		"key_q":{"type":"apiKey","in":"query","name":"api_key"},
		"oauth":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{"read":"","write":""}}}}
	}}`)

func selCreds() map[string]openapi.Credential {
	return map[string]openapi.Credential{
		"key_h": openapi.Secret(hSecret), "key_q": openapi.Secret(qSecret), "oauth": openapi.Secret(oToken),
	}
}

// selCase is one call and the alternative it must apply, or, with want
// empty and refused set, the Settings key its refusal must carry (any of
// several when the contract allows more than one).
type selCase struct {
	key     string
	in      *openapi.Input
	want    string
	refused []string
}

// runSel runs each case against c, checking Request.Security, the
// Response.Security of the call, and that the placed credentials match the
// alternative.
func runSel(t *testing.T, w *wire, c *openapi.Client, cases []selCase) {
	t.Helper()
	for _, tt := range cases {
		name := tt.key
		if tt.in != nil && tt.in.Security != "" {
			name += " " + tt.in.Security
		}
		t.Run(name, func(t *testing.T) {
			if tt.refused != nil {
				before := w.count()
				resp, err := c.Call(t.Context(), tt.key, tt.in, nil)
				re := refusedSince(t, w, before, resp, err)
				wantAnyKey(t, "Settings", re.Settings, tt.refused...)
				return
			}
			req := mustPrepare(t, c, tt.key, tt.in)
			if req.Security != tt.want {
				t.Errorf("Request.Security = %q, want %q", req.Security, tt.want)
			}
			resp := mustCall(t, c, tt.key, tt.in, nil)
			if resp.Security != tt.want {
				t.Errorf("Response.Security = %q, want %q", resp.Security, tt.want)
			}
			h := w.last(t).Header
			if strings.Contains(tt.want, `"key_h"`) {
				wantField(t, h, "X-API-Key", hSecret)
			} else {
				wantField(t, h, "X-API-Key")
			}
			if strings.Contains(tt.want, `"oauth"`) {
				wantField(t, h, "Authorization", "Bearer "+oToken)
			} else {
				wantField(t, h, "Authorization")
			}
		})
	}
}

// doc.go, Configuration: "One security alternative selects itself.
// Several require Options.Security, Options.SecurityKey or Input.Security,
// including an anonymous alternative ... credentials never select an
// alternative implicitly." describe.go, SecurityRequirement.Key:
// "Alternatives with the same key are the same requirement, and the key
// selects the first." OAS 3.1.2 section 4.8.10: an operation's security
// "overrides any declared top-level security. To remove a top-level
// security declaration, an empty array can be used."
func TestSecuritySelection(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, selDoc, &openapi.Options{Credentials: selCreds()})
	runSel(t, w, c, []selCase{
		{key: "inherit", want: `{"key_h":[]}`},
		{key: "none", want: ""},
		{key: "dup", want: `{"key_h":[]}`},
		{key: "anon", want: "{}"},
		// Credentials for every alternative, or an anonymous one, do not
		// select.
		{key: "choice", refused: []string{"Options.Security"}},
		{key: "scopes", refused: []string{"Options.Security"}},
		{key: "pair", refused: []string{"Options.Security"}},
		{key: "twoKinds", refused: []string{"Options.Security"}},
		{key: "optional", refused: []string{"Options.Security"}},
		// Input.Security selects exactly, by the key as Operation.Security
		// spells it.
		{key: "choice", in: &openapi.Input{Security: `{"oauth":["read"]}`}, want: `{"oauth":["read"]}`},
		{key: "scopes", in: &openapi.Input{Security: `{"oauth":["write"]}`}, want: `{"oauth":["write"]}`},
		{key: "pair", in: &openapi.Input{Security: `{"key_h":[],"oauth":["read"]}`}, want: `{"key_h":[],"oauth":["read"]}`},
		{key: "optional", in: &openapi.Input{Security: "{}"}, want: "{}"},
		{key: "optional", in: &openapi.Input{Security: `{"key_h":[]}`}, want: `{"key_h":[]}`},
		{key: "choice", in: &openapi.Input{Security: `{"oauth":["write"]}`}, refused: []string{"Input.Security"}},
		{key: "choice", in: &openapi.Input{Security: `{"oauth": ["read"]}`}, refused: []string{"Input.Security"}},
		{key: "choice", in: &openapi.Input{Security: "{}"}, refused: []string{"Input.Security"}},
		{key: "inherit", in: &openapi.Input{Security: "{}"}, refused: []string{"Input.Security"}},
		{key: "none", in: &openapi.Input{Security: "{}"}, want: ""},
	})
}

// client.go, Options.Security: "selects, for every operation that offers
// it, the security alternative whose schemes are exactly these, whatever
// scopes each operation asks for; it is a preference ... If several
// alternatives have the same scheme names, the call requires Input.Security
// to distinguish their scope requirements ... An operation with several
// alternatives requires Security, SecurityKey or Input.Security ...
// Input.Security overrides it for one call." doc.go, Configuration: "any
// other operation is called as if it were unset".
func TestSecurityPreference(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, selDoc, &openapi.Options{Credentials: selCreds(), Security: []string{"oauth"}})
	runSel(t, w, c, []selCase{
		{key: "choice", want: `{"oauth":["read"]}`},
		// Offered by no alternative of these: as if unset.
		{key: "inherit", want: `{"key_h":[]}`},
		{key: "dup", want: `{"key_h":[]}`},
		{key: "none", want: ""},
		{key: "anon", want: "{}"},
		{key: "twoKinds", refused: []string{"Options.Security"}},
		{key: "optional", refused: []string{"Options.Security"}},
		// {"key_h","oauth"} is not exactly {"oauth"}.
		{key: "pair", refused: []string{"Options.Security"}},
		// Two alternatives with these schemes: Input.Security distinguishes
		// them. Which key the refusal carries is not fixed by the contract.
		{key: "scopes", refused: []string{"Input.Security", "Options.Security"}},
		{key: "scopes", in: &openapi.Input{Security: `{"oauth":["write"]}`}, want: `{"oauth":["write"]}`},
		// Input.Security overrides the preference.
		{key: "choice", in: &openapi.Input{Security: `{"key_h":[]}`}, want: `{"key_h":[]}`},
	})

	// The names are a set: their order does not matter.
	c2 := parseFor(t, w, selDoc, &openapi.Options{Credentials: selCreds(), Security: []string{"oauth", "key_h"}})
	runSel(t, w, c2, []selCase{
		{key: "pair", want: `{"key_h":[],"oauth":["read"]}`},
		{key: "choice", refused: []string{"Options.Security"}},
	})
}

// client.go, Options.SecurityKey: "selects one exact SecurityRequirement.Key
// for every operation that offers it, including "{}" for an anonymous
// alternative; it is a preference, as Security is. It takes precedence over
// an absent Input.Security ... this distinguishes alternatives with the
// same schemes but different scopes." Input.Security: "overriding
// Options.SecurityKey and Options.Security".
func TestSecurityKeyPreference(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, selDoc, &openapi.Options{Credentials: selCreds(), SecurityKey: `{"oauth":["write"]}`})
	runSel(t, w, c, []selCase{
		{key: "scopes", want: `{"oauth":["write"]}`},
		{key: "choice", refused: []string{"Options.Security"}},
		{key: "inherit", want: `{"key_h":[]}`},
		{key: "scopes", in: &openapi.Input{Security: `{"oauth":["read"]}`}, want: `{"oauth":["read"]}`},
	})

	anon := parseFor(t, w, selDoc, &openapi.Options{Credentials: selCreds(), SecurityKey: "{}"})
	runSel(t, w, anon, []selCase{
		{key: "optional", want: "{}"},
		{key: "anon", want: "{}"},
		{key: "inherit", want: `{"key_h":[]}`},
		{key: "none", want: ""},
		{key: "choice", refused: []string{"Options.Security"}},
		{key: "optional", in: &openapi.Input{Security: `{"key_h":[]}`}, want: `{"key_h":[]}`},
	})
}

// client.go, Options.Security: "A set of names no alternative of the
// document uses is refused by Load." Options.SecurityKey: "Security and
// SecurityKey cannot both be set ... A key that names no alternative or
// credential-free operation is refused by Load." load.go, Load: "a Security
// or SecurityKey that matches no alternative". errors.go,
// RequestError.Settings: "A setting the document cannot use, or one that
// conflicts with another, is keyed by its field".
func TestSecurityLoadRefusals(t *testing.T) {
	base := "https://api.example.test"
	for _, tt := range []struct {
		name string
		opts openapi.Options
		keys []string
	}{
		{"Security naming an unknown scheme", openapi.Options{Security: []string{"nope"}}, []string{"Options.Security"}},
		{"Security naming no alternative's set", openapi.Options{Security: []string{"key_q", "oauth"}}, []string{"Options.Security"}},
		{"SecurityKey naming no alternative", openapi.Options{SecurityKey: `{"nope":[]}`}, []string{"Options.SecurityKey"}},
		{"SecurityKey with other scopes", openapi.Options{SecurityKey: `{"oauth":["admin"]}`}, []string{"Options.SecurityKey"}},
		{"SecurityKey not canonical", openapi.Options{SecurityKey: `{"oauth": ["read"]}`}, []string{"Options.SecurityKey"}},
		{"Security and SecurityKey", openapi.Options{Security: []string{"oauth"}, SecurityKey: `{"oauth":["read"]}`},
			[]string{"Options.Security", "Options.SecurityKey"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			err := parseErr(t, selDoc, base, &opts)
			re := asRequestError(t, err)
			wantAnyKey(t, "Settings", re.Settings, tt.keys...)
		})
	}

	// Accepted: names and keys some alternative uses; "{}" for a document
	// with an anonymous alternative or an operation without security.
	for _, o := range []openapi.Options{
		{Security: []string{"key_q"}},
		{Security: []string{"oauth", "key_h"}},
		{SecurityKey: `{"key_h":[],"oauth":["read"]}`},
		{SecurityKey: "{}"},
	} {
		if _, err := openapi.Parse(t.Context(), []byte(expand(selDoc, base)), testDocURI, &o); err != nil {
			t.Errorf("Parse with %+v: %v", o, err)
		}
	}
	credentialFree := doc31(`"/a":{"get":{}},"/b":{"get":{"security":[]}}`, `"security":[{"k":[]}]`,
		`"components":{"securitySchemes":{"k":{"type":"apiKey","in":"header","name":"K"}}}`)
	if _, err := openapi.Parse(t.Context(), []byte(expand(credentialFree, base)), testDocURI, &openapi.Options{SecurityKey: "{}"}); err != nil {
		t.Errorf("SecurityKey {} with a credential-free operation: %v", err)
	}
	keyedOnly := doc31(`"/a":{"get":{}},"/b":{"post":{}}`, `"security":[{"k":[]}]`,
		`"components":{"securitySchemes":{"k":{"type":"apiKey","in":"header","name":"K"}}}`)
	err := parseErr(t, keyedOnly, base, &openapi.Options{SecurityKey: "{}"})
	wantKeys(t, "Settings", asRequestError(t, err).Settings, true, "Options.SecurityKey")
}

// doc.go, Credentials: "Unselected alternatives have no effect on the
// call": a missing credential, or a defective or undeclared scheme, in an
// alternative not selected does not refuse it.
func TestUnselectedAlternativesHaveNoEffect(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`
		"/a":{"get":{"operationId":"a","security":[{"key_h":[]},{"oauth":[]},{"ghost":[]},{"broken":[]}]}}`,
		`"components":{"securitySchemes":{
			"key_h":{"type":"apiKey","in":"header","name":"X-API-Key"},
			"oauth":{"type":"oauth2","flows":{}},
			"broken":{"type":"apiKey","in":"body","name":"k"}
		}}`)
	c := parseFor(t, w, doc, &openapi.Options{Credentials: map[string]openapi.Credential{"key_h": openapi.Secret(hSecret)}})
	resp := mustCall(t, c, "a", &openapi.Input{Security: `{"key_h":[]}`}, nil)
	if resp.Security != `{"key_h":[]}` {
		t.Errorf("Response.Security = %q", resp.Security)
	}
	wantField(t, w.last(t).Header, "X-API-Key", hSecret)
	wantField(t, w.last(t).Header, "Authorization")
}
