package openapi_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

type documentServiceFailure struct{}

func (*documentServiceFailure) Error() string { return "synthetic document service unavailable" }

// doc.go Outcomes/Credential and ErrUnresolved: generated reference
// diagnostics do not expose configured query credentials, but retain typed
// caller causes, a useful redacted reference/refused URI, and both physical
// locations for ambiguous claims. Explicit Raw, Value, URI and inventory
// retain authored identifiers. A constant caller error avoids the exception
// for caller-written error text that itself contains credentials.
func TestSchemaReferenceDiagnosticsRedactQueryCredentials(t *testing.T) {
	const secret = "synthetic-reference-query-credential"
	for _, kind := range []string{"missing-document", "denied-document", "loaded-missing-fragment", "duplicate-claim"} {
		t.Run(kind, func(t *testing.T) {
			resource := "https://schemas.example.test/api/" + kind + ".json?api_key=" + secret
			if kind == "denied-document" {
				resource = "https://denied.example.test/private.json?api_key=" + secret
			}
			uri := resource
			if kind == "loaded-missing-fragment" {
				uri += "#/Missing"
			}
			raw := fmt.Sprintf(`{"$ref":%q}`, uri)
			schemas := ""
			if kind == "duplicate-claim" {
				schemas = fmt.Sprintf(`"A":{"$id":%q,"type":"string"},"B":{"$id":%q,"type":"integer"}`, resource, resource)
			}
			doc := `{"openapi":"3.1.2","info":{"title":"Diagnostic classes","version":"1"},"paths":{"/inspect":{"get":{"operationId":"inspect","security":[{"queryKey":[]}],"parameters":[{"name":"q","in":"query","schema":` + raw + `}]}}},"components":{"securitySchemes":{"queryKey":{"type":"apiKey","in":"query","name":"api_key"}},"schemas":{` + schemas + `}}}`
			cause := &documentServiceFailure{}
			fetch := newMemFetch(nil)
			if kind == "missing-document" {
				fetch.errs[resource] = cause
			}
			loaded := `{"Present":{"type":"string"}}`
			if kind == "loaded-missing-fragment" {
				fetch.docs = map[string]string{resource: loaded}
			}
			c, err := (&openapi.Loader{Fetch: fetch.fetch}).Parse(t.Context(), []byte(doc), schemaGraphEntry, &openapi.Options{Credentials: map[string]openapi.Credential{"queryKey": openapi.Secret(secret)}})
			if err != nil {
				t.Fatal(err)
			}
			op := mustOp(t, c, "inspect")
			if op.Err != nil || len(op.Params) != 1 || op.Params[0].Schema == nil {
				t.Fatalf("unusable schema descriptor: %+v", op)
			}
			s := op.Params[0].Schema
			before := len(fetch.callList())
			wantFetches := 0
			if kind == "missing-document" || kind == "loaded-missing-fragment" {
				wantFetches = 1
			}
			if before != wantFetches {
				t.Fatalf("fetch count=%d want%d", before, wantFetches)
			}
			checkError := func(err error) {
				t.Helper()
				if !errors.Is(err, openapi.ErrUnresolved) {
					t.Fatalf("error does not classify unresolved reference: %v", err)
				}
				noSecrets(t, err, secret)
				// No prescribed marker/prose: the safe host/path still names
				// the failing reference or the refused retrieval destination.
				safe := strings.Split(resource, "?")[0]
				if !strings.Contains(err.Error(), safe) {
					t.Errorf("diagnostic lost the reference/refused URI: %v", err)
				}
				if kind == "missing-document" {
					var typed *documentServiceFailure
					if !errors.Is(err, cause) || !errors.As(err, &typed) || typed != cause {
						t.Errorf("typed caller cause identity lost: %v", err)
					}
					if cause.Error() != "synthetic document service unavailable" {
						t.Error("caller message changed")
					}
				}
				if kind == "duplicate-claim" {
					for _, name := range []string{"A", "B"} {
						if !strings.Contains(err.Error(), schemaGraphEntry+"#/components/schemas/"+name) {
							t.Errorf("ambiguous claim lost physical location%s: %v", name, err)
						}
					}
				}
			}
			for range 2 {
				r := schemaGraphRefs(t, s)
				if len(r) != 1 || r[0].Target != nil || r[0].At != "/$ref" || r[0].Keyword != "$ref" || r[0].Value != uri || r[0].URI != uri {
					t.Fatalf("explicit reference metadata=%+v", r)
				}
				checkError(r[0].Err)
				if target, lookupErr := c.Schema(uri); target != nil {
					t.Error("failed lookup returned target")
				} else {
					checkError(lookupErr)
				}
				if !bytes.Equal(s.Raw(), []byte(raw)) {
					t.Error("diagnostic sanitization changed Raw")
				}
			}
			if kind == "loaded-missing-fragment" {
				if string(c.Document(resource)) != loaded {
					t.Error("explicit query-bearing document address changed")
				}
				found := false
				for _, got := range c.DocumentURIs() {
					found = found || got == resource
				}
				if !found {
					t.Error("inventory scrubbed loaded retrieval URI")
				}
			}
			if len(fetch.callList()) != before {
				t.Error("schema access retried a retained failure")
			}
		})
	}
}
