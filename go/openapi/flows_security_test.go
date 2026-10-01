package openapi_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Runnable versions of the stage 3 example flows in example_test.go (dev
// loop, API preservation: "Examples in example_test.go become runnable
// tests against httptest servers as their stage lands"): scenarios 4a to 4h
// (credentials), 10 (tenants, with credentials) and 11 (redirects).

// securedPetsTemplate is a pets API whose listPets security is @LIST@;
// searchPets allows anonymous access or an API key, and adoptPet needs an
// OAuth token.
const securedPetsTemplate = `{"openapi":"3.1.0","info":{"title":"Pets","version":"1.0.0"},"servers":[{"url":"/v1"}],"paths":{
	"/pets":{"get":{"operationId":"listPets","security":@LIST@,
		"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}},
	"/pets/search":{"get":{"operationId":"searchPets","security":[{},{"api_key":[]}]}},
	"/pets/{petId}/adopt":{"post":{"operationId":"adoptPet","security":[{"oauth":["pets:write"]}],
		"parameters":[{"name":"petId","in":"path","required":true}]}}
},"components":{"securitySchemes":{
	"api_key":{"type":"apiKey","in":"header","name":"X-API-Key"},
	"bearerAuth":{"type":"http","scheme":"bearer"},
	"basicAuth":{"type":"http","scheme":"basic"},
	"oauth":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{"pets:read":"","pets:write":""}}}},
	"signature":{"type":"apiKey","in":"header","name":"Signature"},
	"mtls":{"type":"mutualTLS"}
}}}`

// newSecuredPets serves securedPetsTemplate, with listPets under security,
// at /openapi.json, and answers every operation with an empty page; with
// cfg, over TLS.
func newSecuredPets(t *testing.T, security string, cfg *tls.Config) *wire {
	t.Helper()
	doc := strings.Replace(securedPetsTemplate, "@LIST@", security, 1)
	w := &wire{answer: func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/openapi.json" {
			io.WriteString(rw, doc)
			return
		}
		io.WriteString(rw, `{"pets":[]}`)
	}}
	w.Server = httptest.NewUnstartedServer(http.HandlerFunc(w.serve))
	if cfg != nil {
		w.Server.TLS = cfg
		w.StartTLS()
	} else {
		w.Start()
	}
	t.Cleanup(w.Close)
	return w
}

// apiRequests returns the requests w received under /v1.
func apiRequests(w *wire) []rec {
	var out []rec
	for _, r := range w.requests() {
		if strings.HasPrefix(r.Path, "/v1/") {
			out = append(out, r)
		}
	}
	return out
}

// lastAPI returns the last request under /v1.
func lastAPI(t *testing.T, w *wire) rec {
	t.Helper()
	reqs := apiRequests(w)
	if len(reqs) == 0 {
		t.Fatalf("the API received no call")
	}
	return reqs[len(reqs)-1]
}

func loadPets(t *testing.T, w *wire, opts *openapi.Options) *openapi.Client {
	t.Helper()
	c, err := openapi.Load(t.Context(), w.URL+"/openapi.json", opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

// Example_credentialsAPIKey: an API key for the document's api_key scheme.
func TestFlowCredentialsAPIKey(t *testing.T) {
	api := newSecuredPets(t, `[{"api_key":[]}]`, nil)
	c := loadPets(t, api, &openapi.Options{Credentials: map[string]openapi.Credential{"api_key": openapi.Secret("pets-key")}})
	mustCall(t, c, "listPets", nil, nil)
	wantField(t, lastAPI(t, api).Header, "X-API-Key", "pets-key")
}

// Example_credentialsTypo: a name the document does not use, and an
// environment variable that was never set; Load refuses both, and neither
// sends anything.
func TestFlowCredentialsTypo(t *testing.T) {
	api := newSecuredPets(t, `[{"api_key":[]}]`, nil)
	_, err := openapi.Load(t.Context(), api.URL+"/openapi.json", &openapi.Options{
		Credentials: map[string]openapi.Credential{"apiKey": openapi.Secret("pets-key")},
	})
	re := asRequestError(t, err)
	wantKeys(t, "Settings", re.Settings, true, credKey("apiKey"))
	if !strings.Contains(re.Error(), credKey("apiKey")) {
		t.Errorf("error %q does not name the setting", re.Error())
	}
	noSecrets(t, err, "pets-key")

	_, err = openapi.Load(t.Context(), api.URL+"/openapi.json", &openapi.Options{
		Credentials: map[string]openapi.Credential{"api_key": openapi.Secret("")},
	})
	re = asRequestError(t, err)
	wantKeys(t, "Settings", re.Settings, true, credKey("api_key"))
	if n := len(apiRequests(api)); n != 0 {
		t.Errorf("the API received %d calls", n)
	}
}

// Example_credentialsBearer: a static bearer token.
func TestFlowCredentialsBearer(t *testing.T) {
	api := newSecuredPets(t, `[{"bearerAuth":[]}]`, nil)
	c := loadPets(t, api, &openapi.Options{Credentials: map[string]openapi.Credential{"bearerAuth": openapi.Secret("pets-token")}})
	mustCall(t, c, "listPets", nil, nil)
	wantField(t, lastAPI(t, api).Header, "Authorization", "Bearer pets-token")
}

// Example_credentialsRefreshing and Example_classifyFailures: a token that
// refreshes, asked for when a request is sent; the source's own error type
// says who failed, nothing is sent, and outcome classifies it.
func TestFlowCredentialsRefreshing(t *testing.T) {
	api := newSecuredPets(t, `[{"oauth":["pets:read"]}]`, nil)
	var mu sync.Mutex
	down, issued := true, 0
	c := loadPets(t, api, &openapi.Options{Credentials: map[string]openapi.Credential{
		"oauth": openapi.SecretFunc(func(context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if down {
				return "", fmt.Errorf("%w: %w", errIdentity, errors.New("503 from the token endpoint"))
			}
			issued++
			return fmt.Sprintf("token-%d", issued), nil
		}),
	}})
	_, err := c.Call(t.Context(), "listPets", nil, nil)
	if !errors.Is(err, errIdentity) {
		t.Errorf("error %v does not wrap errIdentity", err)
	}
	asRequestError(t, err)
	if got := outcome(err); got != "identity_down" {
		t.Errorf("outcome = %q, want identity_down", got)
	}
	if n := len(apiRequests(api)); n != 0 {
		t.Errorf("the API received %d calls while the identity provider was down", n)
	}

	mu.Lock()
	down = false
	mu.Unlock()
	mustCall(t, c, "listPets", nil, nil)
	mustCall(t, c, "listPets", nil, nil)
	reqs := apiRequests(api)
	if len(reqs) != 2 {
		t.Fatalf("the API received %d calls, want 2", len(reqs))
	}
	wantField(t, reqs[0].Header, "Authorization", "Bearer token-1")
	wantField(t, reqs[1].Header, "Authorization", "Bearer token-2")
}

// Example_credentialsBasic: HTTP Basic.
func TestFlowCredentialsBasic(t *testing.T) {
	api := newSecuredPets(t, `[{"basicAuth":[]}]`, nil)
	c := loadPets(t, api, &openapi.Options{Credentials: map[string]openapi.Credential{"basicAuth": openapi.Basic("alice", "s3cret")}})
	mustCall(t, c, "listPets", nil, nil)
	wantAuthorization(t, lastAPI(t, api).Header, "Basic", base64.StdEncoding.EncodeToString([]byte("alice:s3cret")))
}

// clientCertificate returns a self-signed certificate for TLS client
// authentication.
func clientCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pets client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Example_credentialsMutualTLS: mutual TLS through the caller's own
// http.Client; a mutualTLS scheme needs no entry in Credentials.
func TestFlowCredentialsMutualTLS(t *testing.T) {
	api := newSecuredPets(t, `[{"mtls":[]}]`, &tls.Config{ClientAuth: tls.RequireAnyClientCert})
	hc := api.Client()
	hc.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{clientCertificate(t)}
	c := loadPets(t, api, &openapi.Options{HTTPClient: hc})
	resp := mustCall(t, c, "listPets", nil, nil)
	if resp.Security != `{"mtls":[]}` {
		t.Errorf("Response.Security = %q", resp.Security)
	}
	wantNoFields(t, lastAPI(t, api).Header, "Authorization", "X-API-Key", "Signature")
}

// Example_credentialsOwnTransport: a scheme the caller's own transport
// satisfies (signer, from example_test.go, which signs only https requests
// to the API's host), marked explicitly, so a scheme merely left out is
// still refused locally.
func TestFlowCredentialsOwnTransport(t *testing.T) {
	api := newSecuredPets(t, `[{"signature":[]}]`, &tls.Config{})
	u, _ := url.Parse(api.URL)
	hc := &http.Client{Transport: signer{host: u.Host, next: api.Client().Transport}}
	c := loadPets(t, api, &openapi.Options{HTTPClient: hc, Credentials: map[string]openapi.Credential{"signature": openapi.FromTransport()}})
	mustCall(t, c, "listPets", nil, nil)
	wantField(t, lastAPI(t, api).Header, "Signature", "...")

	unmarked := loadPets(t, api, &openapi.Options{HTTPClient: hc})
	before := len(apiRequests(api))
	resp, err := unmarked.Call(t.Context(), "listPets", nil, nil)
	re := asRequestError(t, err)
	if resp != nil || len(apiRequests(api)) != before {
		t.Errorf("a scheme left out was not refused locally")
	}
	wantKeys(t, "Settings", re.Settings, true, credKey("signature"))
}

// Example_credentialsAlternatives: the caller holds credentials for two
// alternatives, selects one from the descriptors, and Response.Security
// says which was used.
func TestFlowCredentialsAlternatives(t *testing.T) {
	api := newSecuredPets(t, `[{"api_key":[]},{"oauth":["pets:read"]}]`, nil)
	c := loadPets(t, api, &openapi.Options{Credentials: map[string]openapi.Credential{
		"api_key": openapi.Secret("pets-key"), "oauth": openapi.Secret("pets-token"),
	}})
	op := mustOp(t, c, "listPets")
	in := &openapi.Input{}
	for _, alt := range op.Security {
		if slices.ContainsFunc(alt.Schemes, func(s openapi.SecurityScheme) bool {
			return s.Type == "oauth2" && slices.Contains(s.Scopes, "pets:read")
		}) {
			in.Security = alt.Key
			break
		}
	}
	if in.Security != `{"oauth":["pets:read"]}` {
		t.Fatalf("no oauth2 alternative with pets:read among %+v", op.Security)
	}
	resp := mustCall(t, c, op.Key, in, nil)
	if resp.Security != in.Security {
		t.Errorf("Response.Security = %q, want %q", resp.Security, in.Security)
	}
	r := lastAPI(t, api)
	wantField(t, r.Header, "Authorization", "Bearer pets-token")
	wantNoFields(t, r.Header, "X-API-Key")

	// Without a selection, holding both credentials selects neither.
	before := len(apiRequests(api))
	_, err := c.Call(t.Context(), "listPets", nil, nil)
	wantKeys(t, "Settings", asRequestError(t, err).Settings, false, "Options.Security")
	if len(apiRequests(api)) != before {
		t.Errorf("an unselected call was sent")
	}
}

// Example_credentialsOptionalAuth: security [{}, {api_key: []}]; the caller
// selects authenticated or anonymous access explicitly, and an
// Options.Security preference the operation does not offer leaves it to
// select its own.
func TestFlowCredentialsOptionalAuth(t *testing.T) {
	api := newSecuredPets(t, `[{"api_key":[]}]`, nil)
	c := loadPets(t, api, &openapi.Options{Credentials: map[string]openapi.Credential{"api_key": openapi.Secret("pets-key")}})

	resp := mustCall(t, c, "searchPets", &openapi.Input{Security: `{"api_key":[]}`}, nil)
	if resp.Security != `{"api_key":[]}` {
		t.Errorf("Response.Security = %q", resp.Security)
	}
	wantField(t, lastAPI(t, api).Header, "X-API-Key", "pets-key")

	resp = mustCall(t, c, "searchPets", &openapi.Input{Security: "{}"}, nil)
	if resp.Security != "{}" {
		t.Errorf("Response.Security = %q", resp.Security)
	}
	wantNoFields(t, lastAPI(t, api).Header, "X-API-Key", "Authorization")

	both := c.With(func(o *openapi.Options) {
		o.Credentials["oauth"] = openapi.Secret("pets-token")
		o.Security = []string{"oauth"}
	})
	mustCall(t, both, "searchPets", &openapi.Input{Security: `{"api_key":[]}`}, nil)
	wantField(t, lastAPI(t, api).Header, "X-API-Key", "pets-key")
	_, err := both.Call(t.Context(), "searchPets", nil, nil)
	wantKeys(t, "Settings", asRequestError(t, err).Settings, false, "Options.Security")
	// adoptPet offers oauth: the preference applies.
	mustCall(t, both, "adoptPet", &openapi.Input{Params: map[string]any{"petId": "p-1"}}, nil)
	wantField(t, lastAPI(t, api).Header, "Authorization", "Bearer pets-token")
}

// tenantCredDoc is tenantDoc with getPet under OAuth and a report only
// partners may call.
const tenantCredDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
	"servers":[{"url":"@BASE@/tenants/{tenant}"}],
	"paths":{
		"/pets/{petId}":{"get":{"operationId":"getPet","security":[{"oauth":[]}],
			"parameters":[{"name":"petId","in":"path","required":true,"schema":{"type":"string"}}],
			"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}},
		"/report":{"get":{"operationId":"partnerReport","security":[{"partnerKey":[]}]}}
	},
	"components":{"securitySchemes":{
		"oauth":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{}}}},
		"partnerKey":{"type":"apiKey","in":"header","name":"X-Partner-Key"}
	}}}`

// Example_tenants, with credentials: a Client derived per tenant adds its
// own token source and drops an inherited partner key, without touching the
// base Client.
func TestFlowTenantsCredentials(t *testing.T) {
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		tenant, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/tenants/"), "/")
		json.NewEncoder(rw).Encode(Pet{Name: tenant + " " + r.Header.Get("Authorization")})
	})
	base := parseFor(t, w, tenantCredDoc, &openapi.Options{
		HTTPClient:  &http.Client{Transport: labelled{api: "pets", next: http.DefaultTransport}},
		Credentials: map[string]openapi.Credential{"partnerKey": openapi.Secret("partner-key")},
	})
	tenants := []struct {
		ID      string
		Partner bool
		PetIDs  []string
	}{{"acme", true, []string{"p-1", "p-2"}}, {"globex", false, []string{"p-3"}}}

	var (
		mu  sync.Mutex
		got = map[string]string{}
		wg  sync.WaitGroup
	)
	derived := make([]*openapi.Client, len(tenants))
	for i, tn := range tenants {
		c := base.With(func(o *openapi.Options) {
			o.Variables["tenant"] = tn.ID
			o.Credentials["oauth"] = openapi.SecretFunc(func(context.Context) (string, error) { return "tok-" + tn.ID, nil })
			if !tn.Partner {
				delete(o.Credentials, "partnerKey")
			}
		})
		for _, id := range tn.PetIDs {
			wg.Go(func() {
				var pet Pet
				if _, err := c.Call(t.Context(), "getPet", &openapi.Input{Params: map[string]any{"petId": id}}, &pet); err != nil {
					t.Errorf("%s: %v", tn.ID, err)
					return
				}
				mu.Lock()
				got[tn.ID+" "+id] = pet.Name
				mu.Unlock()
			})
		}
		derived[i] = c
	}
	wg.Wait()
	// Only a partner keeps the partner key.
	for i, tn := range tenants {
		before := w.count()
		_, err := derived[i].Call(t.Context(), "partnerReport", nil, nil)
		if tn.Partner {
			if err != nil {
				t.Errorf("%s: partnerReport: %v", tn.ID, err)
			} else {
				wantField(t, w.last(t).Header, "X-Partner-Key", "partner-key")
			}
			continue
		}
		wantKeys(t, "Settings", asRequestError(t, err).Settings, true, credKey("partnerKey"))
		if w.count() != before {
			t.Errorf("%s: partnerReport was sent without the partner key", tn.ID)
		}
	}
	for key, want := range map[string]string{
		"acme p-1": "acme Bearer tok-acme", "acme p-2": "acme Bearer tok-acme", "globex p-3": "globex Bearer tok-globex",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}

	// The base Client is untouched: it has no oauth credential.
	_, err := base.With(func(o *openapi.Options) { o.Variables["tenant"] = "acme" }).Call(t.Context(), "getPet",
		&openapi.Input{Params: map[string]any{"petId": "p-1"}}, nil)
	wantKeys(t, "Settings", asRequestError(t, err).Settings, true, credKey("oauth"))
}

// photoDoc declares getPetPhoto's 303, as Example_redirects describes.
const photoDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@"}],
	"security":[{"api_key":[]}],
	"paths":{"/pets/{petId}/photo":{"get":{"operationId":"getPetPhoto",
		"parameters":[{"name":"petId","in":"path","required":true}],
		"responses":{"200":{"description":"the photo","content":{"image/png":{}}},"303":{"description":"the photo is elsewhere"}}}}},
	"components":{"securitySchemes":{"api_key":{"type":"apiKey","in":"header","name":"X-API-Key"}}}}`

// Example_redirects: redirects are manual by default; FollowAll opts into
// following them; a cross-origin hop drops credentials and caller fields,
// Range included, unless CheckRedirect intentionally restores one.
func TestFlowRedirects(t *testing.T) {
	cdn := newWire(t, typedAnswer(206, "image/png", "\x89PNG"))
	api := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Location", cdn.URL+"/photos/p-7.png")
		rw.WriteHeader(http.StatusSeeOther)
	})
	c := parseFor(t, api, photoDoc, &openapi.Options{Credentials: map[string]openapi.Credential{"api_key": openapi.Secret("pets-key")}})
	in := &openapi.Input{
		Params: map[string]any{"petId": "p-7"},
		Header: http.Header{"Range": {"bytes=0-1023"}},
	}

	_, err := c.Call(t.Context(), "getPetPhoto", in, nil)
	var se *openapi.StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusSeeOther {
		t.Fatalf("Call = %v, want a *StatusError for the 303", err)
	}
	if loc, err := se.Location(); err != nil || loc.String() != cdn.URL+"/photos/p-7.png" {
		t.Errorf("Location = %v, %v", loc, err)
	}
	cdn.nothingSent(t)

	var photo []byte
	follow := c.With(func(o *openapi.Options) {
		o.Redirects = openapi.FollowAll
		base := o.HTTPClient
		if base == nil {
			base = http.DefaultClient
		}
		copied := *base
		copied.CheckRedirect = func(r *http.Request, via []*http.Request) error {
			r.Header.Set("Range", "bytes=0-1023") // deliberate cross-origin forwarding
			return nil
		}
		o.HTTPClient = &copied
	})
	if _, err := follow.Call(t.Context(), "getPetPhoto", in, &photo); err != nil {
		t.Fatalf("following: %v", err)
	}
	if string(photo) != "\x89PNG" {
		t.Errorf("photo = %q", photo)
	}
	r := cdn.only(t)
	wantField(t, r.Header, "Range", "bytes=0-1023")
	wantNoFields(t, r.Header, "X-API-Key")
	if http.DefaultClient.CheckRedirect != nil {
		t.Errorf("http.DefaultClient was modified")
	}
}
