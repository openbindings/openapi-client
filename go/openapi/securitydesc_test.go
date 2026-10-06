package openapi_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Security descriptors: SecurityRequirement Key and Schemes, SecurityScheme
// and Flow fields for every OpenAPI 3.1 scheme type, and Operation.Security.
// The canonical Key and root inheritance are also in describe_test.go
// (TestSecurityDescriptor).

const descSecurityDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"security":[{"api_key":[]}],
	"paths":{
		"/all":{"get":{"operationId":"all","security":[
			{"api_key":["admin"],"basic":[]},
			{"bearer":[]},
			{"oauth":["write:pets","read:pets"]},
			{"oidc":["openid","email"]},
			{"mtls":[]},
			{"cookie_key":[],"query_key":[]},
			{"alias":[]},
			{"alias_plain":[]},
			{"ghost":[]},
			{"broken":[]},
			{}
		]}},
		"/inherit":{"get":{"operationId":"inherit"}},
		"/none":{"get":{"operationId":"none","security":[]}}
	},
	"components":{"securitySchemes":{
		"api_key":{"type":"apiKey","name":"X-API-Key","in":"header","description":"A key."},
		"cookie_key":{"type":"apiKey","name":"session","in":"cookie"},
		"query_key":{"type":"apiKey","name":"key","in":"query"},
		"basic":{"type":"http","scheme":"Basic","description":"Basic auth."},
		"bearer":{"type":"http","scheme":"bearer","bearerFormat":"JWT"},
		"oauth":{"type":"oauth2","description":"OAuth.","flows":{
			"implicit":{"authorizationUrl":"https://auth.example.test/authorize","refreshUrl":"/refresh","scopes":{"read:pets":"Read","write:pets":"Write"}},
			"password":{"tokenUrl":"/token","scopes":{}},
			"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{"read:pets":"Read"}},
			"authorizationCode":{"authorizationUrl":"https://auth.example.test/authorize","tokenUrl":"https://auth.example.test/token","refreshUrl":"https://auth.example.test/refresh","scopes":{"write:pets":"Write"}},
			"deviceAuthorization":{"deviceAuthorizationUrl":"https://auth.example.test/device","tokenUrl":"https://auth.example.test/token","scopes":{}}
		},"oauth2MetadataUrl":"https://auth.example.test/.well-known/oauth-authorization-server","deprecated":true},
		"oidc":{"type":"openIdConnect","openIdConnectUrl":"https://auth.example.test/.well-known/openid-configuration"},
		"mtls":{"type":"mutualTLS","description":"Cert must be signed by example.com CA"},
		"alias":{"$ref":"#/components/securitySchemes/bearer","description":"The bearer, by another name."},
		"alias_plain":{"$ref":"#/components/securitySchemes/mtls"},
		"broken":{"type":"apiKey","name":"k"}
	}}}`

// schemeOf returns the scheme named name in alternative i of op.
func schemeOf(t *testing.T, op *openapi.Operation, i int, name string) openapi.SecurityScheme {
	t.Helper()
	if i >= len(op.Security) {
		t.Fatalf("%s has %d alternatives, want at least %d", op.Key, len(op.Security), i+1)
	}
	for _, s := range op.Security[i].Schemes {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("%s alternative %d has no scheme %q", op.Key, i, name)
	return openapi.SecurityScheme{}
}

// describe.go, SecurityScheme: "the scheme's declaration, and the scopes or
// roles the alternative requires"; Name "as the requirement writes it";
// Type "apiKey", "http", "mutualTLS", "oauth2" or "openIdConnect"; In and
// ParamName for an apiKey; Scheme "as written" and BearerFormat for http;
// Flows for oauth2; OpenIDConnectURL; OAuth2MetadataURL and Deprecated are
// OpenAPI 3.2's, so a 3.1 document's are not read; Source "a JSON Pointer to
// its Security Scheme Object", "empty for a scheme the document never
// declares"; Err "a defective or missing declaration". Operation: a Reference
// Object's description replaces the target's, "while Source still names the
// target". OAS 3.1.2 sections 4.8.27 to 4.8.30.
func TestSecuritySchemeDescriptors(t *testing.T) {
	c := parseAt(t, descSecurityDoc, "", testDocURI, nil)
	op := mustOp(t, c, "all")
	const src = testDocURI + "#/components/securitySchemes/"

	if got := len(op.Security); got != 11 {
		t.Fatalf("all has %d alternatives, want 11", got)
	}
	if k := op.Security[0].Key; k != `{"api_key":["admin"],"basic":[]}` {
		t.Errorf("alternative 0 Key = %q", k)
	}
	if n := names(op.Security[0].Schemes); !slices.Equal(n, []string{"api_key", "basic"}) {
		t.Errorf("alternative 0 Schemes = %q, want document order", n)
	}

	want := func(i int, name string, w openapi.SecurityScheme) {
		t.Helper()
		got := schemeOf(t, op, i, name)
		if (got.Err != nil) != (w.Err != nil) {
			t.Errorf("%s: Err = %v, want set %v", name, got.Err, w.Err != nil)
		}
		got.Err, w.Err = nil, nil
		got.Flows, w.Flows = nil, nil // compared on their own below
		w.Name = name
		if !equalScheme(got, w) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, w)
		}
	}
	want(0, "api_key", openapi.SecurityScheme{Scopes: []string{"admin"}, Type: "apiKey", Description: "A key.",
		In: "header", ParamName: "X-API-Key", Source: src + "api_key"})
	want(0, "basic", openapi.SecurityScheme{Type: "http", Description: "Basic auth.", Scheme: "Basic", Source: src + "basic"})
	want(1, "bearer", openapi.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT", Source: src + "bearer"})
	want(2, "oauth", openapi.SecurityScheme{Scopes: []string{"write:pets", "read:pets"}, Type: "oauth2", Description: "OAuth.",
		Source: src + "oauth"})
	want(3, "oidc", openapi.SecurityScheme{Scopes: []string{"openid", "email"}, Type: "openIdConnect",
		OpenIDConnectURL: "https://auth.example.test/.well-known/openid-configuration", Source: src + "oidc"})
	want(4, "mtls", openapi.SecurityScheme{Type: "mutualTLS", Description: "Cert must be signed by example.com CA", Source: src + "mtls"})
	want(5, "cookie_key", openapi.SecurityScheme{Type: "apiKey", In: "cookie", ParamName: "session", Source: src + "cookie_key"})
	want(5, "query_key", openapi.SecurityScheme{Type: "apiKey", In: "query", ParamName: "key", Source: src + "query_key"})
	want(6, "alias", openapi.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT",
		Description: "The bearer, by another name.", Source: src + "bearer"})
	want(7, "alias_plain", openapi.SecurityScheme{Type: "mutualTLS", Description: "Cert must be signed by example.com CA", Source: src + "mtls"})
	want(8, "ghost", openapi.SecurityScheme{Err: errSet})
	if s := schemeOf(t, op, 9, "broken"); s.Err == nil || s.Source != src+"broken" {
		t.Errorf("broken: Err %v, Source %q; want Err set and its Source", s.Err, s.Source)
	}
	if k := op.Security[10]; k.Key != "{}" || len(k.Schemes) != 0 {
		t.Errorf("anonymous alternative = %+v", k)
	}
	for i, alt := range op.Security[:8] {
		for _, s := range alt.Schemes {
			if s.Err != nil {
				t.Errorf("alternative %d scheme %s: Err %v", i, s.Name, s.Err)
			}
		}
	}

	// describe.go, Flow: Type "implicit", "password", "clientCredentials",
	// "authorizationCode" or, in OpenAPI 3.2, "deviceAuthorization"; URLs
	// "as written"; Scopes "maps each scope the flow offers to its
	// description". Their order is not fixed by the contract.
	flows := map[string]openapi.Flow{}
	for _, f := range schemeOf(t, op, 2, "oauth").Flows {
		if _, dup := flows[f.Type]; dup {
			t.Errorf("flow %s listed twice", f.Type)
		}
		flows[f.Type] = f
	}
	wantFlows := map[string]openapi.Flow{
		"implicit": {Type: "implicit", AuthorizationURL: "https://auth.example.test/authorize", RefreshURL: "/refresh",
			Scopes: map[string]string{"read:pets": "Read", "write:pets": "Write"}},
		"password":          {Type: "password", TokenURL: "/token", Scopes: map[string]string{}},
		"clientCredentials": {Type: "clientCredentials", TokenURL: "https://auth.example.test/token", Scopes: map[string]string{"read:pets": "Read"}},
		"authorizationCode": {Type: "authorizationCode", AuthorizationURL: "https://auth.example.test/authorize",
			TokenURL: "https://auth.example.test/token", RefreshURL: "https://auth.example.test/refresh", Scopes: map[string]string{"write:pets": "Write"}},
	}
	if len(flows) != len(wantFlows) {
		t.Errorf("flows %q, want %q (deviceAuthorization is OpenAPI 3.2's)", slices.Sorted(maps.Keys(flows)), slices.Sorted(maps.Keys(wantFlows)))
	}
	for typ, w := range wantFlows {
		got, ok := flows[typ]
		if !ok {
			t.Errorf("no %s flow", typ)
			continue
		}
		if got.Type != w.Type || got.AuthorizationURL != w.AuthorizationURL || got.TokenURL != w.TokenURL ||
			got.RefreshURL != w.RefreshURL || got.DeviceAuthorizationURL != "" || !maps.Equal(got.Scopes, w.Scopes) {
			t.Errorf("%s flow:\n got %+v\nwant %+v", typ, got, w)
		}
	}
	for _, s := range op.Security[1].Schemes {
		if len(s.Flows) != 0 {
			t.Errorf("%s (not oauth2) has Flows %+v", s.Name, s.Flows)
		}
	}

	// Operation.Security: root security applies where the operation has
	// none, and an empty array removes it (OAS 3.1.2 section 4.8.10: "To
	// remove a top-level security declaration, an empty array can be
	// used").
	inherit := mustOp(t, c, "inherit")
	if len(inherit.Security) != 1 || inherit.Security[0].Key != `{"api_key":[]}` {
		t.Errorf("inherit Security = %+v", inherit.Security)
	} else {
		want := openapi.SecurityScheme{Name: "api_key", Type: "apiKey", Description: "A key.", In: "header", ParamName: "X-API-Key", Source: src + "api_key"}
		if got := inherit.Security[0].Schemes; len(got) != 1 || !equalScheme(got[0], want) {
			t.Errorf("inherit Schemes = %+v", got)
		}
	}
	if s := mustOp(t, c, "none").Security; len(s) != 0 {
		t.Errorf("none Security = %+v, want empty", s)
	}
}

// errSet marks a wanted Err as set, whatever it is.
var errSet = errorString("set")

type errorString string

func (e errorString) Error() string { return string(e) }

func names(schemes []openapi.SecurityScheme) []string {
	var n []string
	for _, s := range schemes {
		n = append(n, s.Name)
	}
	return n
}

// equalScheme compares the fields of two SecurityScheme values other than
// Flows and Err; nil and empty Scopes are equal.
func equalScheme(a, b openapi.SecurityScheme) bool {
	return a.Name == b.Name && slices.Equal(a.Scopes, b.Scopes) && a.Type == b.Type && a.Description == b.Description &&
		a.In == b.In && a.ParamName == b.ParamName && a.Scheme == b.Scheme && a.BearerFormat == b.BearerFormat &&
		a.OpenIDConnectURL == b.OpenIDConnectURL && a.OAuth2MetadataURL == b.OAuth2MetadataURL &&
		a.Deprecated == b.Deprecated && a.Source == b.Source
}

// describe.go, SecurityScheme.Err: "a defective or missing declaration",
// for each REQUIRED field OAS 3.1.2 section 4.8.27.1 lists for the type,
// and a type outside the five.
func TestSecuritySchemeDefects(t *testing.T) {
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/a":{"get":{"operationId":"a","security":[
		{"no_type":[]},{"bad_type":[]},{"no_in":[]},{"bad_in":[]},{"no_name":[]},{"no_scheme":[]},{"not_object":[]},{"ok":[]}
	]}}},"components":{"securitySchemes":{
		"no_type":{"name":"k","in":"header"},
		"bad_type":{"type":"apikey","name":"k","in":"header"},
		"no_in":{"type":"apiKey","name":"k"},
		"bad_in":{"type":"apiKey","name":"k","in":"body"},
		"no_name":{"type":"apiKey","in":"header"},
		"no_scheme":{"type":"http"},
		"not_object":"bearer",
		"ok":{"type":"apiKey","name":"k","in":"header"}
	}}}`
	c := parseAt(t, doc, "", testDocURI, nil)
	op := mustOp(t, c, "a")
	if op.Err != nil {
		t.Errorf("a defective scheme set the operation's Err: %v", op.Err)
	}
	for i, alt := range op.Security {
		if len(alt.Schemes) != 1 {
			t.Fatalf("alternative %d has %d schemes", i, len(alt.Schemes))
		}
		s := alt.Schemes[0]
		if (s.Err == nil) != (s.Name == "ok") {
			t.Errorf("%s: Err = %v", s.Name, s.Err)
		}
	}
}
