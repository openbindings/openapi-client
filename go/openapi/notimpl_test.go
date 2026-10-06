package openapi_test

import (
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Refusals of what the client does not support: only document versions
// outside Swagger 2.0 and OpenAPI 3.0, 3.1 and 3.2 remain refused, before
// anything is sent; these tests assert only that (not the wording). Each
// supported feature is tested on its own: parameter styles, cookies,
// content parameters, allowReserved and ParamWriters in styles_test.go,
// cookies_test.go, contentparam_test.go, reserved_test.go and
// paramwriters_test.go; credentials, Options.Security, Options.SecurityKey
// and FollowAll in credentials_test.go, security_test.go and
// redirects_test.go; form, multipart, text, XML and sequential bodies in
// bodytext_test.go, bodyform_test.go, bodymultipart_test.go,
// bodyseq_test.go and bodyiter_test.go; YAML in docyaml_test.go; and each
// edition, the OpenAPI 3.2 querystring included, in the edition test
// files.

// load.go Load accepts only Swagger 2.0 and OpenAPI 3.0.x, 3.1.x and 3.2.x.
func TestRefusesUnsupportedDocumentVersions(t *testing.T) {
	docs := map[string]string{
		"Swagger 1.2": `{"swagger":"1.2","info":{"title":"t","version":"1"},"paths":{}}`,
		"Swagger 2.1": `{"swagger":"2.1","info":{"title":"t","version":"1"},"paths":{}}`,
		"OpenAPI 3.3": `{"openapi":"3.3.0","info":{"title":"t","version":"1"},"paths":{}}`,
		"OpenAPI 4.0": `{"openapi":"4.0.0","info":{"title":"t","version":"1"},"paths":{}}`,
	}
	for name, doc := range docs {
		c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, nil)
		if err == nil || c != nil {
			t.Errorf("%s: Parse = %v, %v; want an unsupported-version refusal", name, c, err)
		}
	}
}
