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
// bodyiter_test.go.

// Stage brief, Loading: "Any other edition, YAML (first significant byte not
// '{') ... is a refusal with a clear error (YAML is stage 5, other editions
// stage 6)."
func TestStage1RefusesOtherDocuments(t *testing.T) {
	docs := map[string]string{
		"YAML":        "openapi: 3.1.0\ninfo: {title: t, version: \"1\"}\npaths: {}\n",
		"Swagger 2.0": `{"swagger":"2.0","info":{"title":"t","version":"1"},"paths":{}}`,
		"OpenAPI 3.0": `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"paths":{}}`,
		"OpenAPI 3.2": `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"paths":{}}`,
	}
	for name, doc := range docs {
		c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, nil)
		if err == nil || c != nil {
			t.Errorf("%s: Parse = %v, %v; want a refusal in stage 1", name, c, err)
		}
	}
}
