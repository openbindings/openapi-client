package openapi_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// doc.go Outcomes and Credential: no credential appears in client-created
// error text. The inherited document userinfo tests and Stage 8 response
// diagnostic tests apply this to generated presentation while preserving
// explicitly inspectable metadata. Schema/References must reject an unknown
// dialect, while Schema.Raw and Dialect preserve the authored declaration.
// Both URI sentinels are configured credentials, so this does not invent a
// rule that every arbitrary query value is a credential. No caller-supplied
// error is involved, and no network request is needed to inspect the schema.
func TestSchema9UnknownDialectDiagnosticRedaction(t *testing.T) {
	const password = "synthetic-schema9-dialect-password"
	const token = "synthetic-schema9-dialect-bearer-token"
	const dialect = "https://schema-reader:" + password + "@dialects.example.test/custom?access_token=" + token
	schema := fmt.Sprintf(`{"$schema":%q,"type":"string","description":"authored schema stays inspectable"}`, dialect)
	doc := `{"openapi":"3.1.2","info":{"title":"Private dialect","version":"1"},"paths":{"/inspect":{"get":{"operationId":"inspect","security":[{"basic":[],"bearer":[]}],"parameters":[{"name":"q","in":"query","schema":` + schema + `}]}}},"components":{"securitySchemes":{"basic":{"type":"http","scheme":"basic"},"bearer":{"type":"http","scheme":"bearer"}}}}`
	fetch := newMemFetch(nil)
	c, err := (&openapi.Loader{Fetch: fetch.fetch}).Parse(t.Context(), []byte(doc), schema9Entry, &openapi.Options{
		Credentials: map[string]openapi.Credential{
			"basic":  openapi.Basic("schema-reader", password),
			"bearer": openapi.Secret(token),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	op := mustOp(t, c, "inspect")
	if op.Err != nil || len(op.Params) != 1 || op.Params[0].Schema == nil {
		t.Fatalf("missing usable schema descriptor: %+v", op)
	}
	s := op.Params[0].Schema
	inspect := func() {
		t.Helper()
		if s.Dialect() != dialect || !bytes.Equal(s.Raw(), []byte(schema)) {
			t.Error("explicit dialect metadata or authored Raw was changed")
		}
	}
	inspect()
	for range 2 {
		_, err := s.References()
		if err == nil {
			t.Fatal("unknown dialect reference traversal succeeded")
		}
		noSecrets(t, err, password, token)
		inspect()
	}
	if calls := fetch.callList(); len(calls) != 0 {
		t.Errorf("schema inspection fetched %d documents", len(calls))
	}
}
