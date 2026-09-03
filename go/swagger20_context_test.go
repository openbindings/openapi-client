package openapiclient

// openbindings.openapi-2.0@1 §3.2 gives a pre-dispatch refusal two species:
// context-required, where a named §12.1 configuration point or a declared
// credential is awaited and the refusal carries its own resolution path, and
// plain refusal, where no supplied context could change the answer. This file
// pins which condition carries which species, and pins that the choice of
// species never moves the boundary between refusing and dispatching.

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

const swagger20SpeciesHost = `"swagger":"2.0","info":{"title":"species","version":"1"},"host":"api.example","schemes":["https"]`

func swagger20SpeciesPrepare(t *testing.T, document, ref string, configure func(*Swagger20PrepareOptions)) (*Swagger20PreparedOperation, *swagger20CaptureTransport) {
	t.Helper()
	transport := &swagger20CaptureTransport{}
	options := Swagger20PrepareOptions{
		Source:     Swagger20Source{Location: "https://api.example/swagger.json", Content: []byte(document)},
		Ref:        ref,
		HTTPClient: &http.Client{Transport: transport},
	}
	if configure != nil {
		configure(&options)
	}
	prepared, err := NewEngine(options.HTTPClient).PrepareSwagger20(context.Background(), options)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return prepared, transport
}

func TestSwagger20RefusalSpecies(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		document   string
		ref        string
		input      Swagger20Input
		configure  func(*Swagger20PrepareOptions)
		code       string
		point      string
		auth       string
		schemeName string
	}{
		{
			name:     "emptyValueForm awaited",
			document: `{` + swagger20SpeciesHost + `,"paths":{"/p":{"get":{"parameters":[{"name":"q","in":"query","type":"string","allowEmptyValue":true}],"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/get",
			input:    Swagger20Input{Parameters: Swagger20Parameters{Query: map[string]any{"q": ""}}},
			code:     CodeContextRequired, point: "emptyValueForm",
		},
		{
			name:      "emptyValueForm supplied dispatches the same invocation",
			document:  `{` + swagger20SpeciesHost + `,"paths":{"/p":{"get":{"parameters":[{"name":"q","in":"query","type":"string","allowEmptyValue":true}],"responses":{"204":{"description":"ok"}}}}}}`,
			ref:       "#/paths/~1p/get",
			input:     Swagger20Input{Parameters: Swagger20Parameters{Query: map[string]any{"q": ""}}},
			configure: func(o *Swagger20PrepareOptions) { o.EmptyValueForm = Swagger20EmptyValueNameOnly },
		},
		{
			name:     "requestMedia awaited before input consumption",
			document: `{` + swagger20SpeciesHost + `,"consumes":["application/json","text/plain"],"paths":{"/p":{"post":{"parameters":[{"name":"b","in":"body","required":true,"schema":{"type":"string"}}],"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/post",
			input:    Swagger20Input{Body: "x", BodyPresent: true},
			code:     CodeContextRequired, point: "requestMedia",
		},
		{
			name:     "propertyMedia awaited, keyed by the parameter",
			document: `{` + swagger20SpeciesHost + `,"consumes":["multipart/form-data"],"paths":{"/p":{"post":{"parameters":[{"name":"f","in":"formData","required":true,"type":"file"}],"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/post",
			input:    Swagger20Input{Parameters: Swagger20Parameters{FormData: map[string]any{"f": "QUFB"}}},
			code:     CodeContextRequired, point: "propertyMedia",
		},
		{
			name:     "server awaited on two usable schemes",
			document: `{"swagger":"2.0","info":{"title":"species","version":"1"},"host":"api.example","schemes":["http","https"],"paths":{"/p":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/get",
			code:     CodeContextRequired, point: "server",
		},
		{
			name:     "server awaited where §10 names a configured URL as the recovery",
			document: `{"swagger":"2.0","info":{"title":"species","version":"1"},"host":"api.example","schemes":["ws","wss"],"paths":{"/p":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/get",
			code:     CodeContextRequired, point: "server",
		},
		{
			name:     "security selection awaited",
			document: `{` + swagger20SpeciesHost + `,"securityDefinitions":{"k":{"type":"apiKey","name":"X-Key","in":"header"},"b":{"type":"basic"}},"security":[{"k":[]},{"b":[]}],"paths":{"/p":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/get",
			code:     CodeContextRequired, point: "security",
		},
		{
			name:     "apiKey credential awaited",
			document: `{` + swagger20SpeciesHost + `,"securityDefinitions":{"k":{"type":"apiKey","name":"X-Key","in":"header"}},"security":[{"k":[]}],"paths":{"/p":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/get",
			code:     CodeContextRequired, auth: "auth.apiKey", schemeName: "k",
		},
		{
			name:     "two ANDed credentials are carried together",
			document: `{` + swagger20SpeciesHost + `,"securityDefinitions":{"k":{"type":"apiKey","name":"X-Key","in":"header"},"b":{"type":"basic"}},"security":[{"k":[],"b":[]}],"paths":{"/p":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/get",
			code:     CodeContextRequired, auth: "auth.basic", schemeName: "b",
		},
		{
			name:     "an empty string the declaration never admits stays plain",
			document: `{` + swagger20SpeciesHost + `,"paths":{"/p":{"get":{"parameters":[{"name":"q","in":"query","type":"string"}],"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/get",
			input:    Swagger20Input{Parameters: Swagger20Parameters{Query: map[string]any{"q": ""}}},
			code:     CodeRefused,
		},
		{
			name:      "a supplied emptyValueForm outside the admissible set stays plain",
			document:  `{` + swagger20SpeciesHost + `,"paths":{"/p":{"get":{"parameters":[{"name":"q","in":"query","type":"string","allowEmptyValue":true}],"responses":{"204":{"description":"ok"}}}}}}`,
			ref:       "#/paths/~1p/get",
			input:     Swagger20Input{Parameters: Swagger20Parameters{Query: map[string]any{"q": ""}}},
			configure: func(o *Swagger20PrepareOptions) { o.EmptyValueForm = Swagger20EmptyValueForm("sometimes") },
			code:      CodeRefused,
		},
		{
			name:     "a supplied OAuth2 token this lane cannot use stays plain",
			document: `{` + swagger20SpeciesHost + `,"securityDefinitions":{"o":{"type":"oauth2","flow":"implicit","authorizationUrl":"https://auth.example/a","scopes":{}}},"security":[{"o":[]}],"paths":{"/p":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			ref:      "#/paths/~1p/get",
			configure: func(o *Swagger20PrepareOptions) {
				o.SecurityCredentials = Swagger20SecurityCredentials{OAuth2: map[string]Swagger20OAuth2Credential{"o": {AccessToken: "not a token"}}}
			},
			code: CodeRefused,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			prepared, transport := swagger20SpeciesPrepare(t, testCase.document, testCase.ref, testCase.configure)
			err := runSwagger20TestInput(t, prepared, testCase.input)
			if testCase.code == "" {
				if err != nil {
					t.Fatalf("error = %v, want dispatch", err)
				}
				if len(transport.requests) != 1 {
					t.Fatalf("dispatched %d requests, want 1", len(transport.requests))
				}
				return
			}
			var executionError *ExecutionError
			if !errors.As(err, &executionError) || executionError.Code != testCase.code {
				t.Fatalf("error = %#v, want %s", err, testCase.code)
			}
			// Both species are pre-dispatch and neither has an observable side
			// effect; that is what §3.2 fixes and what the species must not move.
			if len(transport.requests) != 0 {
				t.Fatalf("dispatched %d requests, want 0", len(transport.requests))
			}
			if testCase.code != CodeContextRequired {
				if executionError.DetailsPresent {
					t.Fatalf("a plain refusal carries details: %#v", executionError.Details)
				}
				return
			}
			prerequisites, ok := executionError.Details.(*Prerequisites)
			if !ok || len(prerequisites.Alternatives) != 1 {
				t.Fatalf("details = %#v, want one alternative", executionError.Details)
			}
			if prerequisites.Target != "https://api.example/swagger.json" {
				t.Fatalf("target = %q, want the asserted source scope", prerequisites.Target)
			}
			requirements := prerequisites.Alternatives[0].Requirements
			if testCase.point != "" {
				if len(requirements) != 1 || requirements[0].Type != "config.value" || requirements[0].Extra["point"] != testCase.point {
					t.Fatalf("requirements = %#v, want config.value at %q", requirements, testCase.point)
				}
				return
			}
			found := false
			for _, requirement := range requirements {
				if requirement.Type == testCase.auth && requirement.Name == testCase.schemeName {
					found = true
				}
			}
			if !found {
				t.Fatalf("requirements = %#v, want %s named %q", requirements, testCase.auth, testCase.schemeName)
			}
		})
	}
}

func TestSwagger20CredentialAlternativeIsCarriedWhole(t *testing.T) {
	// The binding-invoker contract makes an alternative an AND of requirements.
	// A challenge naming one of two ANDed credentials is not a resolution path,
	// so the whole alternative is carried even though the lane stops at the
	// first missing one.
	document := `{` + swagger20SpeciesHost + `,"securityDefinitions":{"k":{"type":"apiKey","name":"X-Key","in":"header"},"b":{"type":"basic"}},"security":[{"k":[],"b":[]}],"paths":{"/p":{"get":{"responses":{"204":{"description":"ok"}}}}}}`
	prepared, _ := swagger20SpeciesPrepare(t, document, "#/paths/~1p/get", nil)
	err := runSwagger20TestInput(t, prepared, Swagger20Input{})
	var executionError *ExecutionError
	if !errors.As(err, &executionError) || executionError.Code != CodeContextRequired {
		t.Fatalf("error = %#v, want %s", err, CodeContextRequired)
	}
	prerequisites := executionError.Details.(*Prerequisites)
	got := map[string]string{}
	for _, requirement := range prerequisites.Alternatives[0].Requirements {
		got[requirement.Name] = requirement.Type
	}
	if len(got) != 2 || got["b"] != "auth.basic" || got["k"] != "auth.apiKey" {
		t.Fatalf("requirements = %#v, want both declared schemes", prerequisites.Alternatives[0].Requirements)
	}
}
