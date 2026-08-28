package openapiclient

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func loadOpenAPI32ParameterClient(t *testing.T, transport *openAPI32OperationTransport, paths string) *Client {
	t.Helper()
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: parameters, version: "1"}
servers: [{url: https://api.example}]
paths:
` + paths)}, ClientOptions{HTTPClient: httpClientForOpenAPI32OperationTransport(transport)})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func httpClientForOpenAPI32OperationTransport(transport *openAPI32OperationTransport) *http.Client {
	return &http.Client{Transport: transport}
}

func TestOpenAPI32QueryStringOwnsTheCompleteQueryComponent(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client := loadOpenAPI32ParameterClient(t, transport, `
  /form:
    get:
      parameters:
        - name: authored-name-is-not-serialized
          in: querystring
          required: true
          content:
            application/x-www-form-urlencoded:
              schema:
                type: object
                properties:
                  page: {type: string}
                  tag: {type: string}
      responses: {'204': {description: ok}}
  /json:
    get:
      parameters:
        - name: whole
          in: querystring
          content:
            application/json:
              schema: {type: object}
      responses: {'204': {description: ok}}
`)
	if _, err := client.Call(context.Background(), PathOperation("/form", GET), Input{Parameters: Parameters{
		QueryString: map[string]any{"authored-name-is-not-serialized": map[string]any{"page": "a b", "tag": "x/y"}},
	}}); err != nil {
		t.Fatalf("form querystring: %v", err)
	}
	if _, err := client.Call(context.Background(), PathOperation("/json", GET), Input{Parameters: Parameters{
		QueryString: map[string]any{"whole": map[string]any{"a": "x&y"}},
	}}); err != nil {
		t.Fatalf("JSON querystring: %v", err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(transport.requests))
	}
	if got := transport.requests[0].URL.RawQuery; got != "page=a+b&tag=x%2Fy" {
		t.Errorf("form querystring = %q", got)
	}
	if got := transport.requests[1].URL.RawQuery; got != "%7B%22a%22%3A%22x%5Cu0026y%22%7D" {
		t.Errorf("JSON querystring = %q", got)
	}
}

func TestOpenAPI32StyleAndUndefinedCells(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client := loadOpenAPI32ParameterClient(t, transport, `
  /matrix/{id}:
    get:
      parameters: [{name: id, in: path, required: true, style: matrix, schema: {type: [string, 'null']}}]
      responses: {'204': {description: ok}}
  /deep:
    get:
      parameters:
        - name: filter
          in: query
          style: deepObject
          explode: false
          schema: {type: object, properties: {kind: {type: string}}}
      responses: {'204': {description: ok}}
  /reserved:
    get:
      parameters: [{name: q, in: query, allowReserved: true, schema: {type: string}}]
      responses: {'204': {description: ok}}
  /cookies:
    get:
      parameters:
        - name: parts
          in: cookie
          style: cookie
          schema: {type: array, items: {type: string}}
      responses: {'204': {description: ok}}
  /cookie-null:
    get:
      parameters:
        - name: session
          in: cookie
          style: cookie
          schema: {type: [string, 'null']}
      responses: {'204': {description: ok}}
`)
	calls := []struct {
		path  string
		input Input
	}{
		{"/matrix/{id}", Input{Parameters: Parameters{Path: map[string]any{"id": nil}}}},
		{"/deep", Input{Parameters: Parameters{Query: map[string]any{"filter": map[string]any{"kind": "value"}}}}},
		{"/reserved", Input{Parameters: Parameters{Query: map[string]any{"q": "a/b?c#d&e=f+g[h]"}}}},
		{"/cookies", Input{Parameters: Parameters{Cookie: map[string]any{"parts": []any{"a", "b"}}}}},
		{"/cookie-null", Input{Parameters: Parameters{Cookie: map[string]any{"session": nil}}}},
	}
	for _, call := range calls {
		if _, err := client.Call(context.Background(), PathOperation(call.path, GET), call.input); err != nil {
			t.Fatalf("call %s: %v", call.path, err)
		}
	}
	if got := transport.requests[0].URL.EscapedPath(); got != "/matrix/;id" {
		t.Errorf("matrix undefined path = %q", got)
	}
	if got := transport.requests[1].URL.RawQuery; got != "filter%5Bkind%5D=value" {
		t.Errorf("deepObject query = %q", got)
	}
	if got := transport.requests[2].URL.RawQuery; got != "q=a/b?c%23d%26e%3Df%2Bg%5Bh%5D" {
		t.Errorf("allowReserved query = %q", got)
	}
	if got := transport.requests[3].Header.Get("Cookie"); got != "parts=a; parts=b" {
		t.Errorf("cookie style = %q", got)
	}
	if got := transport.requests[4].Header.Get("Cookie"); got != "session=" {
		t.Errorf("cookie undefined value = %q", got)
	}
}

func TestOpenAPI32ParameterRuntimeRefusalsPrecedeDispatch(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client := loadOpenAPI32ParameterClient(t, transport, `
  /required:
    get:
      parameters: [{name: q, in: query, required: true, schema: {type: string}}]
      responses: {'204': {description: ok}}
  /space:
    get:
      parameters: [{name: q, in: query, style: spaceDelimited, explode: false, schema: {type: array, items: {type: [string, 'null']}}}]
      responses: {'204': {description: ok}}
  /header:
    get:
      parameters: [{name: X-Note, in: header, schema: {type: string}}]
      responses: {'204': {description: ok}}
  /cookie-name:
    get:
      parameters: [{name: bad name, in: cookie, style: cookie, schema: {type: string}}]
      responses: {'204': {description: ok}}
`)
	for _, testCase := range []struct {
		path  string
		input Input
	}{
		{"/required", Input{}},
		{"/space", Input{Parameters: Parameters{Query: map[string]any{"q": nil}}}},
		{"/space", Input{Parameters: Parameters{Query: map[string]any{"q": []any{"a", nil}}}}},
		{"/header", Input{Parameters: Parameters{Header: map[string]any{"X-Note": "safe\r\nInjected: yes"}}}},
		{"/cookie-name", Input{Parameters: Parameters{Cookie: map[string]any{"bad name": "value"}}}},
	} {
		if _, err := client.Call(context.Background(), PathOperation(testCase.path, GET), testCase.input); err == nil {
			t.Errorf("%s unexpectedly dispatched", testCase.path)
		}
	}
	if len(transport.requests) != 0 {
		t.Fatalf("runtime refusals dispatched %d request(s)", len(transport.requests))
	}
}

func TestOpenAPI32ParameterDeclarationExclusions(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		parameter string
		path      string
	}{
		{"query collision", `{name: whole, in: querystring, content: {application/json: {schema: {type: object}}}}, {name: q, in: query, schema: {type: string}}`, "/x"},
		{"two querystrings", `{name: one, in: querystring, content: {application/json: {schema: {type: object}}}}, {name: two, in: querystring, content: {application/json: {schema: {type: object}}}}`, "/x"},
		{"querystring schema", `{name: whole, in: querystring, schema: {type: string}}`, "/x"},
		{"querystring schema field", `{name: whole, in: querystring, allowReserved: false, content: {application/json: {schema: {type: object}}}}`, "/x"},
		{"querystring sequential media", `{name: whole, in: querystring, content: {application/json: {itemSchema: {type: object}}}}`, "/x"},
		{"querystring unsupported media", `{name: whole, in: querystring, content: {image/png: {schema: {type: string}}}}`, "/x"},
		{"undefined style cell", `{name: q, in: query, style: spaceDelimited, explode: true, schema: {type: array, items: {type: string}}}`, "/x"},
		{"compound member", `{name: q, in: query, style: form, explode: false, schema: {type: object, properties: {nested: {type: object}}}}`, "/x"},
		{"form cookie static", `{name: c, in: cookie, style: form, explode: true, schema: {type: array, items: {type: string}}}`, "/x"},
		{"unmatched path parameter", `{name: id, in: path, required: true, schema: {type: string}}`, "/x"},
		{"duplicate expression", `{name: id, in: path, required: true, schema: {type: string}}`, "/{id}/{id}"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: exclusion, version: "1"}
paths:
  ` + testCase.path + `:
    get:
      parameters: [` + testCase.parameter + `]
  /survivor: {get: {}}
`)}, ArtifactLoadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if artifact.Refusal() != nil {
				t.Fatalf("target-local parameter defect became source refusal: %v", artifact.Refusal())
			}
			ref := "#/paths/" + escapeJSONPointerSegment(testCase.path) + "/get"
			if _, err := artifact.ResolveOperation(ref); err == nil {
				t.Fatal("malformed parameter target was addressable")
			}
			if _, err := artifact.ResolveOperation("#/paths/~1survivor/get"); err != nil {
				t.Fatalf("sibling target did not survive: %v", err)
			}
		})
	}
}

func TestOpenAPI32EquivalentTemplatedPathsAreExcluded(t *testing.T) {
	artifact, err := LoadArtifact(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: ambiguous paths, version: "1"}
paths:
  /items/{id}:
    get:
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
  /items/{name}:
    get:
      parameters: [{name: name, in: path, required: true, schema: {type: string}}]
  /survivor: {get: {}}
`)}, ArtifactLoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"#/paths/~1items~1{id}/get", "#/paths/~1items~1{name}/get"} {
		if _, err := artifact.ResolveOperation(ref); err == nil || !strings.Contains(err.Error(), "same templated hierarchy") {
			t.Errorf("ResolveOperation(%q) = %v", ref, err)
		}
	}
}

func TestOpenAPI32ClosedParameterStyleTable(t *testing.T) {
	schemas := map[string]*openapi3.Schema{
		"primitive": {Type: &openapi3.Types{"string"}},
		"array":     {Type: &openapi3.Types{"array"}, Items: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}},
		"object": {Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
			"member": &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
		}},
	}
	type allowedRow struct {
		location string
		style    string
		shapes   []string
		explodes []bool
	}
	rows := []allowedRow{
		{openapi3.ParameterInPath, openapi3.SerializationMatrix, []string{"primitive", "array", "object"}, []bool{false, true}},
		{openapi3.ParameterInPath, openapi3.SerializationLabel, []string{"primitive", "array", "object"}, []bool{false, true}},
		{openapi3.ParameterInPath, openapi3.SerializationSimple, []string{"primitive", "array", "object"}, []bool{false, true}},
		{openapi3.ParameterInQuery, openapi3.SerializationForm, []string{"primitive", "array", "object"}, []bool{false, true}},
		{openapi3.ParameterInQuery, openapi3.SerializationSpaceDelimited, []string{"array", "object"}, []bool{false}},
		{openapi3.ParameterInQuery, openapi3.SerializationPipeDelimited, []string{"array", "object"}, []bool{false}},
		{openapi3.ParameterInQuery, openapi3.SerializationDeepObject, []string{"object"}, []bool{false, true}},
		{openapi3.ParameterInHeader, openapi3.SerializationSimple, []string{"primitive", "array", "object"}, []bool{false, true}},
		{openapi3.ParameterInCookie, openapi3.SerializationForm, []string{"primitive", "array", "object"}, []bool{false, true}},
		{openapi3.ParameterInCookie, "cookie", []string{"primitive", "array", "object"}, []bool{false, true}},
	}
	for _, row := range rows {
		for _, shape := range row.shapes {
			for _, exploded := range row.explodes {
				explode := exploded
				parameter := &openapi3.Parameter{
					Name: "value", In: row.location, Style: row.style, Explode: &explode,
					Schema: &openapi3.SchemaRef{Value: schemas[shape]},
				}
				if err := validateRevision3ParameterSerializationForEdition(parameter, false, string(EditionOpenAPI320)); err != nil {
					t.Errorf("allowed %s/%s/%s/explode=%t: %v", row.location, row.style, shape, exploded, err)
				}
			}
		}
	}

	disallowed := []struct {
		location string
		style    string
		shape    string
		explode  bool
	}{
		{openapi3.ParameterInPath, openapi3.SerializationForm, "primitive", false},
		{openapi3.ParameterInHeader, openapi3.SerializationLabel, "primitive", false},
		{openapi3.ParameterInCookie, openapi3.SerializationSimple, "primitive", false},
		{openapi3.ParameterInQuery, openapi3.SerializationMatrix, "primitive", false},
		{openapi3.ParameterInQuery, openapi3.SerializationSpaceDelimited, "primitive", false},
		{openapi3.ParameterInQuery, openapi3.SerializationSpaceDelimited, "array", true},
		{openapi3.ParameterInQuery, openapi3.SerializationPipeDelimited, "object", true},
		{openapi3.ParameterInQuery, openapi3.SerializationDeepObject, "array", true},
	}
	for _, cell := range disallowed {
		explode := cell.explode
		parameter := &openapi3.Parameter{
			Name: "value", In: cell.location, Style: cell.style, Explode: &explode,
			Schema: &openapi3.SchemaRef{Value: schemas[cell.shape]},
		}
		if err := validateRevision3ParameterSerializationForEdition(parameter, false, string(EditionOpenAPI320)); err == nil {
			t.Errorf("disallowed %s/%s/%s/explode=%t was admitted", cell.location, cell.style, cell.shape, cell.explode)
		}
	}
}
