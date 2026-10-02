package openapi_test

import (
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 1 refusals of features later stages implement. The stage brief
// says each is refused, before anything is sent, until its stage lands;
// these tests assert only that (not the wording), and each later stage
// replaces the cases it implements with tests of the feature itself. Stage
// 2 replaced the parameter refusals (styles, cookies, content parameters,
// allowReserved and ParamWriters) with styles_test.go, cookies_test.go,
// contentparam_test.go, reserved_test.go and paramwriters_test.go; the
// OpenAPI 3.2 querystring comes with its edition in stage 6. Stage 3
// replaced the refusals of credentials, Options.Security,
// Options.SecurityKey and FollowAll with credentials_test.go,
// security_test.go and redirects_test.go. Stage 4 replaced the refusal of
// form, multipart, text, XML and sequential bodies with bodytext_test.go,
// bodyform_test.go, bodymultipart_test.go, bodyseq_test.go and
// bodyiter_test.go. Stage 5 replaced the refusal of YAML with
// docyaml_test.go. Stage 6 replaces the edition refusals with the edition
// test files, including querystring; only unsupported versions remain here.

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
