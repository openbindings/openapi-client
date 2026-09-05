package openapiclient

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestCredentialWireGrammars(t *testing.T) {
	if validBearerToken("bad token") || validBearerToken("=") || !validBearerToken("abc-._~+/==") {
		t.Fatal("RFC 6750 b64token grammar is not enforced")
	}
	if validBasicCredentialText("bad\nvalue") || validBasicCredentialText("é") || !validBasicCredentialText("plain") {
		t.Fatal("RFC 7617 credential text grammar is not enforced")
	}
	if validRFC6265CookieValue("bad;value") || !validRFC6265CookieValue("good-value") {
		t.Fatal("RFC 6265 cookie-value grammar is not enforced")
	}
	if got, want := percentEncodeCredentialQuery("a/b? c&d=é"), "a%2Fb%3F%20c%26d%3D%C3%A9"; got != want {
		t.Fatalf("credential query encoding = %q, want %q", got, want)
	}
}

func TestValidateSecurityRequirementCarriage(t *testing.T) {
	requirement := openapi3.SecurityRequirement{"key": nil}
	schemes := openapi3.SecuritySchemes{"key": {Value: &openapi3.SecurityScheme{Type: "apiKey", In: "header", Name: "X-Key"}}}
	parameters := openapi3.Parameters{{Value: &openapi3.Parameter{Name: "x-key", In: "header", Required: true}}}
	if err := ValidateSecurityRequirementCarriage(requirement, schemes, parameters); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("security carriage collision = %v", err)
	}
}
