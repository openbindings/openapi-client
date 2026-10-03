package openapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// describe.go SecurityScheme/Flow normalize Swagger securityDefinitions.
// Credentials retain authored names and are placed only during Send.
func TestEditionsSwaggerSecurity(t *testing.T) {
	for _, tc := range []struct {
		name, decl, flow string
		cred             openapi.Credential
		want             string
	}{
		{"basic", `{"type":"basic"}`, "", openapi.Basic("alice", "secret"), "Basic YWxpY2U6c2VjcmV0"},
		{"implicit", `{"type":"oauth2","flow":"implicit","authorizationUrl":"/authorize","scopes":{"read":"Read"}}`, "implicit", openapi.Secret("token"), "Bearer token"},
		{"password", `{"type":"oauth2","flow":"password","tokenUrl":"/token","scopes":{"read":"Read"}}`, "password", openapi.Secret("token"), "Bearer token"},
		{"application", `{"type":"oauth2","flow":"application","tokenUrl":"/token","scopes":{"read":"Read"}}`, "clientCredentials", openapi.Secret("token"), "Bearer token"},
		{"accessCode", `{"type":"oauth2","flow":"accessCode","authorizationUrl":"/authorize","tokenUrl":"/token","scopes":{"read":"Read"}}`, "authorizationCode", openapi.Secret("token"), "Bearer token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &memRT{}
			c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"security":[{"auth":["read"]}]}}`, `"securityDefinitions":{"auth":`+tc.decl+`}`), &openapi.Options{HTTPClient: &http.Client{Transport: rt}, Credentials: map[string]openapi.Credential{"auth": tc.cred}})
			op := mustOp(t, c, "GET /x")
			s := op.Security[0].Schemes[0]
			if s.Name != "auth" || s.Source != testDocURI+"#/securityDefinitions/auth" || s.Err != nil {
				t.Errorf("scheme %+v", s)
			}
			if tc.flow == "" {
				if s.Type != "http" || s.Scheme != "basic" {
					t.Errorf("basic %+v", s)
				}
			} else {
				if s.Type != "oauth2" || len(s.Flows) != 1 || s.Flows[0].Type != tc.flow || s.Flows[0].Scopes["read"] != "Read" {
					t.Errorf("OAuth %+v", s)
				}
			}
			req := mustPrepare(t, c, op.Key, nil)
			if req.HTTP.Header.Get("Authorization") != "" {
				t.Error("credential during Prepare")
			}
			sendAndClose(t, req)
			if got := rt.requests()[0].Header.Get("Authorization"); got != tc.want {
				t.Errorf("auth %q want %q", got, tc.want)
			}
		})
	}
}

// SchemeLookup governs component lookup only; OAS 3.2.1 Security Requirement
// URI names resolve against the document holding the requirement, including
// its $self. The Credential remains keyed by the authored relative URI.
func TestEditionsSecurityRequirementURI(t *testing.T) {
	for _, lookup := range []openapi.SchemeLookup{openapi.SchemesInEntryFirst, openapi.SchemesInEntry, openapi.SchemesInReferrer} {
		t.Run(fmt.Sprint(lookup), func(t *testing.T) {
			const name = "schemes.json#/Key"
			l := openapi.Loader{SchemeLookup: lookup, Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
				switch u {
				case "https://api.example.test/other.json":
					return io.NopCloser(strings.NewReader(`{"openapi":"3.2.1","$self":"models/other.json","paths":{},"item":{"get":{"security":[{"schemes.json#/Key":[]}]}}}`)), "", nil
				case "https://api.example.test/models/schemes.json":
					return io.NopCloser(strings.NewReader(`{"Key":{"type":"apiKey","in":"header","name":"X-URI-Key"}}`)), "", nil
				}
				return nil, "", fmt.Errorf("unexpected fetch %q", u)
			}}
			rt := &memRT{}
			c, err := l.Parse(t.Context(), []byte(editionDoc("3.2.1", `"/x":{"$ref":"other.json#/item"}`)), testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}, Credentials: map[string]openapi.Credential{name: openapi.Secret("secret")}})
			if err != nil {
				t.Fatal(err)
			}
			s := mustOp(t, c, "GET /x").Security[0].Schemes[0]
			if s.Name != name || s.Source != "https://api.example.test/models/schemes.json#/Key" || s.Err != nil {
				t.Errorf("scheme %+v", s)
			}
			mustCall(t, c, "GET /x", nil, nil)
			if got := rt.requests()[0].Header.Get("X-URI-Key"); got != "secret" {
				t.Errorf("credential %q", got)
			}
		})
	}
}

func TestEditionsSecurityComponentPrecedence(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			fetched := 0
			l := openapi.Loader{Fetch: func(_ context.Context, u string) (io.ReadCloser, string, error) {
				fetched++
				return nil, "", fmt.Errorf("unexpected %s", u)
			}}
			doc := editionDoc(version, `"/x":{"get":{"security":[{"key":[]}]}}`, `"components":{"securitySchemes":{"key":{"type":"apiKey","in":"header","name":"X-Key"}}}`)
			c, err := l.Parse(t.Context(), []byte(doc), testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mustOp(t, c, "GET /x").Security[0].Schemes[0].Err != nil || fetched != 0 {
				t.Error("component treated as URI")
			}
			missing := editionDoc(version, `"/x":{"get":{"security":[{"missing.json#/Key":[]}]}}`)
			c, err = l.Parse(t.Context(), []byte(missing), testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mustOp(t, c, "GET /x").Security[0].Schemes[0].Err == nil {
				t.Error("missing scheme not diagnosed")
			}
			want := 0
			if version == "3.2.1" {
				want = 1
			}
			if fetched != want {
				t.Errorf("fetches %d want %d", fetched, want)
			}
		})
	}
}

// Server.Name and OAuth metadata/deprecated/device flow are 3.2 fields.
// Feature-shaped data in older documents is inert, not enabled by name.
func TestEditionsMetadata(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, editionDoc(version, `"/x":{"get":{"security":[{"oauth":[]}]}}`, `"servers":[{"url":"https://api.example.test/v1","name":"production"}],"components":{"securitySchemes":{"oauth":{"type":"oauth2","deprecated":true,"oauth2MetadataUrl":"/metadata","flows":{"clientCredentials":{"tokenUrl":"/token","scopes":{}},"deviceAuthorization":{"deviceAuthorizationUrl":"/device","tokenUrl":"/token","scopes":{"read":"Read"}}}}}}`), nil)
			op := mustOp(t, c, "GET /x")
			s := op.Security[0].Schemes[0]
			n := server(t, op, 0).Name
			if version == "3.2.1" {
				if n != "production" || !s.Deprecated || s.OAuth2MetadataURL != "/metadata" || len(s.Flows) != 2 || s.Flows[1].Type != "deviceAuthorization" || s.Flows[1].DeviceAuthorizationURL != "/device" {
					t.Errorf("server name %q scheme %+v", n, s)
				}
				derived := c.With(func(o *openapi.Options) { o.Server = "production" })
				if mustOp(t, derived, "GET /x").Servers[0].ID != op.Servers[0].ID {
					t.Error("server identity changed")
				}
			} else {
				if n != "" || s.Deprecated || s.OAuth2MetadataURL != "" || len(s.Flows) != 1 {
					t.Errorf("earlier metadata name %q scheme %+v", n, s)
				}
			}
		})
	}
}
