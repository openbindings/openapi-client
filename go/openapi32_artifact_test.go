package openapiclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

type openAPI32ResourceTransport struct {
	resources map[string]string
	requests  []string
}

func TestClassifyOpenAPIEditionClosedRepresentationGates(t *testing.T) {
	if edition, err := ClassifyOpenAPIEdition([]byte("openapi: 3.2.0\npaths: {}\n")); err != nil || edition != EditionOpenAPI320 {
		t.Fatalf("valid YAML classification = %q, %v", edition, err)
	}
	for _, testCase := range []struct {
		name string
		data string
	}{
		{name: "duplicate key", data: "openapi: 3.2.0\nopenapi: 3.2.0\npaths: {}\n"},
		{name: "multiple documents", data: "openapi: 3.2.0\npaths: {}\n---\nopenapi: 3.2.0\npaths: {}\n"},
		{name: "no JSON image", data: "openapi: 3.2.0\npaths: {}\nx-bad:\n  ? [a, b]\n  : value\n"},
		{name: "non-object root", data: "- openapi\n- 3.2.0\n"},
		{name: "missing edition", data: "paths: {}\n"},
		{name: "non-string edition", data: "openapi: 32\npaths: {}\n"},
		{name: "future edition", data: "openapi: 3.2.1\npaths: {}\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ClassifyOpenAPIEdition([]byte(testCase.data)); err == nil {
				t.Fatal("classification unexpectedly succeeded")
			}
		})
	}
}

func (t *openAPI32ResourceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, request.URL.String())
	body, present := t.resources[request.URL.String()]
	status := http.StatusOK
	if !present {
		status = http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}, nil
}

func TestOpenAPI32EditionIsClassifiedBeforeReferenceResolution(t *testing.T) {
	t.Run("embedded content", func(t *testing.T) {
		transport := &openAPI32ResourceTransport{resources: map[string]string{
			"https://resources.example/path-item.yaml": `get: {responses: {"204": {description: ok}}}`,
		}}
		_, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.1
info: {title: future, version: "1"}
paths:
  /x:
    $ref: https://resources.example/path-item.yaml
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
		if err == nil || !strings.Contains(err.Error(), `unsupported OpenAPI version "3.2.1"`) {
			t.Fatalf("error = %v, want exact edition refusal", err)
		}
		if len(transport.requests) != 0 {
			t.Fatalf("reference resolution ran before edition discrimination: %v", transport.requests)
		}
	})

	t.Run("location source", func(t *testing.T) {
		transport := &openAPI32ResourceTransport{resources: map[string]string{
			"https://resources.example/openapi.yaml": `openapi: 3.2.1
info: {title: future, version: "1"}
paths:
  /x:
    $ref: path-item.yaml
`,
			"https://resources.example/path-item.yaml": `get: {responses: {"204": {description: ok}}}`,
		}}
		_, err := LoadArtifact(context.Background(), Source{Location: "https://resources.example/openapi.yaml"}, ArtifactLoadOptions{
			HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true,
		})
		if err == nil || !strings.Contains(err.Error(), `unsupported OpenAPI version "3.2.1"`) {
			t.Fatalf("error = %v, want exact edition refusal", err)
		}
		if len(transport.requests) != 1 || transport.requests[0] != "https://resources.example/openapi.yaml" {
			t.Fatalf("requests before edition discrimination = %v, want entry only", transport.requests)
		}
	})
}

func TestOpenAPI32ArtifactCarriesSelfOverlayAndUsesItAsReferenceBase(t *testing.T) {
	transport := &openAPI32ResourceTransport{resources: map[string]string{
		"https://identity.example/descriptions/path-item.yaml": `get: {responses: {"204": {description: ok}}}`,
	}}
	artifact, err := LoadArtifact(context.Background(), Source{
		Location: "https://retrieval.example/openapi.yaml",
		Content: []byte(`
openapi: 3.2.0
$self: https://identity.example/descriptions/root.yaml
info: {title: self, version: "1"}
paths:
  /x:
    $ref: path-item.yaml
`),
	}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Edition != EditionOpenAPI320 {
		t.Fatalf("edition = %q", artifact.Edition)
	}
	entry, present := artifact.OpenAPI32().Entry()
	if !present || entry.RetrievalURI != "https://retrieval.example/openapi.yaml" || entry.IdentityURI != "https://identity.example/descriptions/root.yaml" {
		t.Fatalf("entry overlay = %#v, present=%v", entry, present)
	}
	if len(transport.requests) != 1 || transport.requests[0] != "https://identity.example/descriptions/path-item.yaml" {
		t.Fatalf("resource requests = %v", transport.requests)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1x/get"); err != nil {
		t.Fatalf("resolve operation through $self-relative Path Item: %v", err)
	}
}

func TestOpenAPI32AbsoluteSelfProvidesBaseForContentWithoutLocation(t *testing.T) {
	transport := &openAPI32ResourceTransport{resources: map[string]string{
		"https://identity.example/descriptions/path-item.yaml": `get: {responses: {"204": {description: ok}}}`,
	}}
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
$self: https://identity.example/descriptions/root.yaml
info: {title: self-contained, version: "1"}
paths:
  /x:
    $ref: path-item.yaml
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 1 || transport.requests[0] != "https://identity.example/descriptions/path-item.yaml" {
		t.Fatalf("resource requests = %v", transport.requests)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1x/get"); err != nil {
		t.Fatalf("resolve operation through content-only absolute $self: %v", err)
	}
}

func TestOpenAPI32SelectedReferenceRequiresDeclaredSelfIdentity(t *testing.T) {
	external := `
openapi: 3.2.0
$self: https://identity.example/library.yaml
info: {title: library, version: "1"}
paths:
  /target: {get: {responses: {"204": {description: ok}}}}
`
	for _, testCase := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "retrieval alias", ref: "https://retrieval.example/library.yaml#/paths/~1target", want: "retrieval alias"},
		{name: "self identity", ref: "https://identity.example/library.yaml#/paths/~1target"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &openAPI32ResourceTransport{resources: map[string]string{
				strings.Split(testCase.ref, "#")[0]: external,
			}}
			artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: entry, version: "1"}
paths:
  /x:
    $ref: ` + testCase.ref + `
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = artifact.ResolveOperation("#/paths/~1x/get")
			if testCase.want == "" && err != nil {
				t.Fatalf("canonical reference refused: %v", err)
			}
			if testCase.want != "" && (err == nil || !strings.Contains(err.Error(), testCase.want)) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestOpenAPI32UnusableSelfDoesNotFallBackSemanticallyToRetrieval(t *testing.T) {
	t.Run("entry relative reference", func(t *testing.T) {
		transport := &openAPI32ResourceTransport{resources: map[string]string{
			"https://retrieval.example/path-item.yaml": `get: {}`,
		}}
		artifact, err := LoadArtifact(context.Background(), Source{
			Location: "https://retrieval.example/openapi.yaml",
			Content: []byte(`
openapi: 3.2.0
$self: 42
info: {title: invalid self, version: "1"}
paths:
  /x: {$ref: path-item.yaml}
`),
		}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := artifact.ResolveOperation("#/paths/~1x/get"); err == nil || !strings.Contains(err.Error(), "no document base") {
			t.Fatalf("relative reference with unusable $self error = %v", err)
		}
	})

	t.Run("referenced document", func(t *testing.T) {
		transport := &openAPI32ResourceTransport{resources: map[string]string{
			"https://retrieval.example/library.yaml": `
openapi: 3.2.0
$self: 42
info: {title: invalid self, version: "1"}
paths: {/target: {get: {}}}
`,
		}}
		artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: entry, version: "1"}
paths:
  /x: {$ref: 'https://retrieval.example/library.yaml#/paths/~1target'}
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := artifact.ResolveOperation("#/paths/~1x/get"); err == nil || !strings.Contains(err.Error(), "unusable $self") {
			t.Fatalf("referenced document unusable $self error = %v", err)
		}
	})
}

func TestOpenAPI32PathItemRefCollisionConfinesToSelectedFields(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: collisions, version: "1"}
components:
  pathItems:
    Shared:
      get: {responses: {"204": {description: ok}}}
      post: {responses: {"204": {description: ok}}}
paths:
  /selected:
    $ref: '#/components/pathItems/Shared'
    get: {responses: {"200": {description: adjacent}}}
  /unused:
    $ref: '#/components/pathItems/Shared'
    post: {responses: {"200": {description: adjacent}}}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1selected/get"); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("selected collision error = %v", err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1unused/get"); err != nil {
		t.Fatalf("unused post collision excluded get: %v", err)
	}
}

func TestOpenAPI32NoncollidingAdjacentOperationKeepsResolvedNestedReferences(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: adjacent composition, version: "1"}
components:
  pathItems:
    Shared: {get: {}}
  requestBodies:
    Payload:
      content:
        application/json:
          schema: {type: object}
paths:
  /x:
    $ref: '#/components/pathItems/Shared'
    post:
      requestBody: {$ref: '#/components/requestBodies/Payload'}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/post")
	if err != nil {
		t.Fatal(err)
	}
	if target.Document == artifact.Document {
		t.Fatal("adjacent operation did not receive its target-specific typed image")
	}
	if target.Operation.RequestBody == nil || target.Operation.RequestBody.Value == nil {
		t.Fatal("adjacent operation request-body reference was not resolved")
	}
	if _, err := artifact.ResolveOperation("#/paths/~1x/get"); err != nil {
		t.Fatalf("referenced sibling operation was lost: %v", err)
	}
}

func TestOpenAPI32UnreachableBrokenReferenceDoesNotDestroySelectedTarget(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: confinement, version: "1"}
paths:
  /good: {get: {}}
  /broken:
    post:
      requestBody: {$ref: '#/components/requestBodies/Missing'}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1good/get"); err != nil {
		t.Fatalf("unreachable defect destroyed selected target: %v", err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1broken/post"); err == nil {
		t.Fatal("selected broken reference was not confined as an excluded operation")
	}
	if operations := enumerateOperationsWithFloor(artifact, nil); len(operations) != 1 || operations[0].info.Ref != "#/paths/~1good/get" {
		t.Fatalf("confined operation inventory = %#v", operations)
	}
}

func TestOpenAPI32ConfinementBuildsTargetSpecificTypedImages(t *testing.T) {
	t.Run("missing external reference in sibling operation", func(t *testing.T) {
		artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: external confinement, version: "1"}
paths:
  /good: {get: {}}
  /broken:
    post:
      requestBody: {$ref: 'https://missing.example/body.yaml'}
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: &openAPI32ResourceTransport{}}, AllowExternalRefs: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := artifact.ResolveOperation("#/paths/~1good/get"); err != nil {
			t.Fatalf("missing external sibling destroyed target: %v", err)
		}
	})

	t.Run("unreachable reusable component", func(t *testing.T) {
		artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: component confinement, version: "1"}
components:
  schemas:
    Broken: {$ref: '#/components/schemas/Missing'}
paths:
  /good: {get: {}}
`)}, ArtifactLoadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := artifact.ResolveOperation("#/paths/~1good/get"); err != nil {
			t.Fatalf("unreachable component destroyed target: %v", err)
		}
	})

	t.Run("referenced path item", func(t *testing.T) {
		artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: referenced path confinement, version: "1"}
components:
  pathItems:
    Good: {get: {}}
paths:
  /good: {$ref: '#/components/pathItems/Good'}
  /broken:
    post:
      requestBody: {$ref: '#/components/requestBodies/Missing'}
`)}, ArtifactLoadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := artifact.ResolveOperation("#/paths/~1good/get"); err != nil {
			t.Fatalf("referenced path target did not survive confinement: %v", err)
		}
	})
}

func TestOpenAPI32SelectedRequestClosureEnforcesReferenceIdentityAndSchemaID(t *testing.T) {
	t.Run("request body retrieval alias", func(t *testing.T) {
		transport := &openAPI32ResourceTransport{resources: map[string]string{
			"https://retrieval.example/library.yaml": `
openapi: 3.2.0
$self: https://identity.example/library.yaml
info: {title: library, version: "1"}
components:
  requestBodies:
    Payload: {content: {application/json: {schema: {type: object}}}}
`,
		}}
		artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: request alias, version: "1"}
paths:
  /x:
    post:
      requestBody: {$ref: 'https://retrieval.example/library.yaml#/components/requestBodies/Payload'}
  /good: {get: {}}
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := artifact.ResolveOperation("#/paths/~1x/post"); err == nil || !strings.Contains(err.Error(), "retrieval alias") {
			t.Fatalf("request-body alias error = %v", err)
		}
	})

	t.Run("schema pointer crosses id", func(t *testing.T) {
		artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: schema identity, version: "1"}
components:
  schemas:
    Resource:
      $id: https://schemas.example/resource
      type: object
      properties: {name: {type: string}}
paths:
  /x:
    post:
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Resource/properties/name'}
  /good: {get: {}}
`)}, ArtifactLoadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := artifact.ResolveOperation("#/paths/~1x/post"); err == nil || !strings.Contains(err.Error(), "$id") {
			t.Fatalf("schema identity error = %v", err)
		}
	})
}

func TestOpenAPI32ResponseReferenceRequiresDeclaredSelfIdentity(t *testing.T) {
	transport := &openAPI32ResourceTransport{resources: map[string]string{
		"https://retrieval.example/responses.yaml": `
openapi: 3.2.0
$self: https://identity.example/responses.yaml
info: {title: responses, version: "1"}
components:
  responses:
    Empty: {description: ok}
`,
	}}
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response seam, version: "1"}
paths:
  /x:
    get:
      responses:
        '204': {$ref: 'https://retrieval.example/responses.yaml#/components/responses/Empty'}
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1x/get"); err == nil || !strings.Contains(err.Error(), "retrieval alias") {
		t.Fatalf("response identity error = %v", err)
	}
}

func TestOpenAPI32ResponseClosureMaterializesCanonicalExternalResources(t *testing.T) {
	transport := &openAPI32ResourceTransport{resources: map[string]string{
		"https://identity.example/responses.yaml": `
openapi: 3.2.0
$self: https://identity.example/responses.yaml
info: {title: responses, version: "1"}
components:
  responses:
    Value:
      content:
        application/json:
          schema: {type: string}
`,
	}}
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response closure, version: "1"}
paths:
  /x:
    get:
      responses:
        '200': {$ref: 'https://identity.example/responses.yaml#/components/responses/Value'}
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/get")
	if err != nil {
		t.Fatal(err)
	}
	response := target.Operation.Responses.Value("200")
	media := response.Value.Content["application/json"]
	if media == nil || media.Schema == nil || media.Schema.Value == nil || !media.Schema.Value.Type.Is("string") {
		t.Fatalf("materialized response media = %#v", media)
	}
}

func TestOpenAPI32ResponseMediaReferenceIdentityConfinesToAlternative(t *testing.T) {
	transport := &openAPI32ResourceTransport{resources: map[string]string{
		"https://retrieval.example/media.yaml": `
openapi: 3.2.0
$self: https://identity.example/media.yaml
info: {title: media, version: "1"}
components:
  mediaTypes:
    Value: {schema: {type: object}}
`,
	}}
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response media closure, version: "1"}
paths:
  /x:
    get:
      responses:
        '200':
          content:
            application/json: {$ref: 'https://retrieval.example/media.yaml#/components/mediaTypes/Value'}
            text/plain: {schema: {type: string}}
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/get")
	if err != nil {
		t.Fatalf("media-local identity failure excluded target: %v", err)
	}
	response := target.Operation.Responses.Value("200").Value
	if response.Content["application/json"] != nil || response.Content["text/plain"] == nil {
		t.Fatalf("confined response content = %#v", response.Content)
	}
	if len(target.ResponseMediaExclusions) != 1 || target.ResponseMediaExclusions[0].MediaType != "application/json" ||
		!strings.Contains(target.ResponseMediaExclusions[0].Reason, "retrieval alias") {
		t.Fatalf("response media exclusions = %#v", target.ResponseMediaExclusions)
	}
}

func TestOpenAPI32ResponseSchemaIdentityConfinesToAlternative(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response schema identity, version: "1"}
components:
  schemas:
    Resource:
      $id: https://schemas.example/resource
      type: object
      properties:
        name: {type: string}
paths:
  /x:
    get:
      responses:
        '200':
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Resource/properties/name'}
            text/plain: {schema: {type: string}}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/get")
	if err != nil {
		t.Fatalf("schema-local identity failure excluded target: %v", err)
	}
	response := target.Operation.Responses.Value("200").Value
	if response.Content["application/json"] != nil || response.Content["text/plain"] == nil {
		t.Fatalf("confined response content = %#v", response.Content)
	}
	if len(target.ResponseMediaExclusions) != 1 || !strings.Contains(target.ResponseMediaExclusions[0].Reason, "$id") {
		t.Fatalf("response media exclusions = %#v", target.ResponseMediaExclusions)
	}
}

func TestOpenAPI32ResponseReferenceIgnoresAddedSiblings(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response reference siblings, version: "1"}
components:
  responses:
    Empty: {}
paths:
  /x:
    get:
      responses:
        '204':
          $ref: '#/components/responses/Empty'
          content:
            application/json:
              schema: {$ref: 'https://unused.example/schema'}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target, err := artifact.ResolveOperation("#/paths/~1x/get")
	if err != nil {
		t.Fatal(err)
	}
	if response := target.Operation.Responses.Value("204"); response == nil || response.Value == nil || len(response.Value.Content) != 0 {
		t.Fatalf("Reference Object sibling became response behavior: %#v", response)
	}
}

func TestOpenAPI32ResponseReferenceCycleTerminates(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response cycle, version: "1"}
components:
  responses:
    A: {$ref: '#/components/responses/B'}
    B: {$ref: '#/components/responses/A'}
paths:
  /x: {get: {responses: {'204': {$ref: '#/components/responses/A'}}}}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1x/get"); err != nil {
		t.Fatalf("resolvable response cycle refused: %v", err)
	}
}

func TestOpenAPI32ResponseHeaderAndLinkReferencesShareResponseIdentity(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		component   string
		declaration string
		target      string
	}{
		{name: "header", component: "headers", declaration: "headers: {X-Trace: {$ref: 'https://retrieval.example/library.yaml#/components/headers/Target'}}", target: "required: true\n      schema: {type: string}"},
		{name: "link", component: "links", declaration: "links: {next: {$ref: 'https://retrieval.example/library.yaml#/components/links/Target'}}", target: "operationId: next"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &openAPI32ResourceTransport{resources: map[string]string{
				"https://retrieval.example/library.yaml": `
openapi: 3.2.0
$self: https://identity.example/library.yaml
info: {title: response library, version: "1"}
components:
  ` + testCase.component + `:
    Target:
      ` + testCase.target + `
`,
			}}
			artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response nested identity, version: "1"}
paths:
  /x:
    get:
      responses:
        '204':
          ` + testCase.declaration + `
`)}, ArtifactLoadOptions{HTTPClient: &http.Client{Transport: transport}, AllowExternalRefs: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := artifact.ResolveOperation("#/paths/~1x/get"); err == nil || !strings.Contains(err.Error(), "retrieval alias") {
				t.Fatalf("%s response identity error = %v", testCase.name, err)
			}
		})
	}
}

func TestPreloadedOpenAPI32DocumentRequiresArtifactOverlay(t *testing.T) {
	document := &openapi3.T{OpenAPI: "3.2.0", Paths: openapi3.NewPaths()}
	_, _, err := loadArtifact(context.Background(), nil, Source{Document: document}, false)
	if err == nil || !strings.Contains(err.Error(), "Source.Artifact") {
		t.Fatalf("error = %v, want overlay-preservation refusal", err)
	}
}

func TestOpenAPI32PostLoadRefusalAndDialectExclusionStayDistinct(t *testing.T) {
	noSurfaces, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: no surfaces, version: "1"}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if noSurfaces.Refusal() == nil || noSurfaces.SourceExclusion() != nil {
		t.Fatalf("no-surfaces disposition: refusal=%v exclusion=%v", noSurfaces.Refusal(), noSurfaces.SourceExclusion())
	}

	dialect, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
jsonSchemaDialect: https://json-schema.org/draft/2020-12/schema
info: {title: dialect, version: "1"}
paths: {}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if dialect.Refusal() != nil || dialect.SourceExclusion() == nil {
		t.Fatalf("dialect disposition: refusal=%v exclusion=%v", dialect.Refusal(), dialect.SourceExclusion())
	}
}

func TestOpenAPI32SelectorGrammarAndResolution(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: selectors, version: "1"}
paths:
  /pets/{petId}:
    parameters:
      - {name: petId, in: path, required: true, schema: {type: string}}
    query: {}
    connect: {operationId: ignoredKinFixedField}
    additionalOperations:
      COPY: {operationId: copy}
      CONNECT: {operationId: customConnect}
      get: {operationId: colliding}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{
		"#/paths/~1pets~1{petId}/query",
		"#/paths/~1pets~1{petId}/additionalOperations/COPY",
		"#/paths/~1pets~1{petId}/additionalOperations/CONNECT",
	} {
		if _, err := artifact.ResolveOperation(ref); err != nil {
			t.Errorf("ResolveOperation(%q): %v", ref, err)
		}
	}
	for _, ref := range []string{
		"#/paths/~1pets~1%7BpetId%7D/query",
		"#/paths/~1pets~1{petId}/QUERY",
		"#/paths/~1pets~1{petId}/connect",
		"#/paths/~1pets~1{petId}/additionalOperations/get",
		"#/paths/~1pets~1{petId}/additionalOperations/GeT",
	} {
		if _, err := artifact.ResolveOperation(ref); err == nil {
			t.Errorf("ResolveOperation(%q) unexpectedly succeeded", ref)
		}
	}
	operations := enumerateOperationsWithFloor(artifact, nil)
	if len(operations) != 3 {
		t.Fatalf("enumerated operations = %d, want query + COPY + CONNECT; %#v", len(operations), operations)
	}
}

func TestOpenAPI32ResponsesOmissionIsAddressableButPresentEmptyIsNot(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: responses, version: "1"}
paths:
  /omitted: {get: {}}
  /empty: {get: {responses: {}}}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1omitted/get"); err != nil {
		t.Fatalf("omitted responses must remain addressable: %v", err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1empty/get"); err == nil || !strings.Contains(err.Error(), "present empty") {
		t.Fatalf("present-empty responses error = %v", err)
	}
}

func TestOpenAPI32SelectedSchemaResourceDialectIsConfinedToItsClosure(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: schema dialect, version: "1"}
paths:
  /selected:
    post:
      requestBody:
        content:
          application/json:
            schema:
              $schema: https://json-schema.org/draft/2020-12/schema
              type: object
  /unrelated: {get: {}}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1selected/post"); err == nil || !strings.Contains(err.Error(), "$schema") {
		t.Fatalf("selected schema dialect error = %v", err)
	}
	if _, err := artifact.ResolveOperation("#/paths/~1unrelated/get"); err != nil {
		t.Fatalf("schema dialect leaked outside selected closure: %v", err)
	}
}
