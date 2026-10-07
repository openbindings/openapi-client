package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// A refusal over a defective part reports that part's Err, also when the
// value given for it could not be serialized either. describe.go,
// Operation.Err: "A defect in an optional part is reported on that part
// instead, and fails a call only when the call uses it, the *RequestError
// then wrapping that part's Err, whether or not the operation was described
// first." The key is the part's: errors.go, RequestError.Inputs, "The key is
// the Param.Key or, for the body, "Input.Body" followed by a JSON Pointer to
// the part of Body concerned"; for a security scheme, the credential's
// setting (errors.go, RequestError.Settings, "Options.<Field>[<name>], for
// Credentials").
//
// Each case is run described first and called first, on fresh Clients, with
// the checks of TestDescribeOrderSameDescriptors: the calls that are sent
// report the described descriptors, and each refusal, through Call and
// Prepare, holds its key and wraps the described Err.

// partErrResponse is a 200 response with JSON content in OpenAPI 3.x.
const partErrResponse = `"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}`

// swaggerResponse is a 200 response with JSON content in Swagger 2.0.
const swaggerResponse = `"produces":["application/json"],"responses":{"200":{"description":"ok","schema":{"type":"object"}}}`

// errRetrieval is the error the Loader's Fetch returns for every document.
var errRetrieval = errors.New("the document cannot be retrieved")

// paramNamed returns the Err of the parameter name.
func paramNamed(name string) func(*openapi.Operation) error {
	return func(op *openapi.Operation) error {
		if p := paramByName(op.Params, name); p != nil {
			return p.Err
		}
		return nil
	}
}

// fieldNamed returns the Err of the field or part name of the request
// body's first Media.
func fieldNamed(name string) func(*openapi.Operation) error {
	return func(op *openapi.Operation) error {
		if op.Body == nil || len(op.Body.Media) == 0 {
			return nil
		}
		if p := paramByName(op.Body.Media[0].Encoding, name); p != nil {
			return p.Err
		}
		return nil
	}
}

// schemeErr returns the Err of the first scheme of the first security
// alternative.
func schemeErr(op *openapi.Operation) error {
	if len(op.Security) == 0 || len(op.Security[0].Schemes) == 0 {
		return nil
	}
	return op.Security[0].Schemes[0].Err
}

// partErrDoc is a document and its cases.
type partErrDoc struct {
	name  string
	doc   string
	cases []orderCase
}

func partErrDocs() []partErrDoc {
	const uri = "schemes.json#/K" // resolved against testDocURI, where Fetch fails
	return []partErrDoc{{
		// describe.go, Param.CollectionFormat: "csv" (its default), "ssv",
		// "tsv", "pipes" or "multi"; any other value is a defect of the
		// parameter, or of the formData field (describe.go, Media.Encoding:
		// "in Swagger 2.0 every formData parameter").
		name: "Swagger 2.0",
		doc: editionDoc("2.0", `
			"/query":{"get":{"operationId":"query","parameters":[
				{"name":"q","in":"query","type":"array","collectionFormat":"bogus","items":{"type":"string"}},
				{"name":"r","in":"query","type":"string"}],`+swaggerResponse+`}},
			"/formData":{"post":{"operationId":"formData","consumes":["application/x-www-form-urlencoded"],"parameters":[
				{"name":"f","in":"formData","type":"array","collectionFormat":"bogus","items":{"type":"string"}},
				{"name":"ok","in":"formData","type":"string"}],`+swaggerResponse+`}}`),
		cases: []orderCase{
			{name: "query parameter collectionFormat", key: "query", ok: &openapi.Input{Params: map[string]any{"r": "x"}},
				bad: []refusal{
					{"an array", &openapi.Input{Params: map[string]any{"q": []any{"a", "b"}}}, "Inputs", "q", paramNamed("q")},
					{"a []string", &openapi.Input{Params: map[string]any{"q": []string{"a", "b"}}}, "Inputs", "q", paramNamed("q")},
					{"a nested array", &openapi.Input{Params: map[string]any{"q": [][]string{{"a"}, {"b"}}}}, "Inputs", "q", paramNamed("q")},
				}},
			{name: "formData field collectionFormat", key: "formData", ok: &openapi.Input{Body: map[string]any{"ok": "x"}}, body: firstMedia,
				bad: []refusal{
					{"an array", &openapi.Input{Body: map[string]any{"f": []any{"a", "b"}}}, "Inputs", "Input.Body/f", fieldNamed("f")},
					{"a nested array", &openapi.Input{Body: map[string]any{"f": [][]string{{"a"}, {"b"}}}}, "Inputs", "Input.Body/f", fieldNamed("f")},
				}},
		},
	}, {
		// A style OpenAPI does not define for the location is a defect the
		// document alone decides (doc.go, Styles: refused "with Param.Err set
		// where the document alone decides it"); a primitive for deepObject,
		// and nesting in a style but deepObject, are values the styles cannot
		// serialize either (doc.go, Styles).
		name: "OpenAPI 3.1",
		doc: editionDoc("3.1.2", `
			"/header":{"get":{"operationId":"header","parameters":[
				{"name":"X-Deep","in":"header","style":"deepObject","schema":{"type":"object"}},
				{"name":"r","in":"query","schema":{"type":"string"}}],`+partErrResponse+`}},
			"/matrix":{"get":{"operationId":"matrix","parameters":[
				{"name":"q","in":"query","style":"matrix","schema":{"type":"array"}},
				{"name":"r","in":"query","schema":{"type":"string"}}],`+partErrResponse+`}},
			"/both":{"post":{"operationId":"both","requestBody":{"content":{"multipart/*":{`+orderFields+`,
				"encoding":{"f":{"style":"matrix","contentType":"not a media type"}}}}},`+partErrResponse+`}}`,
			`"servers":[{"url":"`+orderBase+`"}]`),
		cases: []orderCase{
			{name: "deepObject header parameter", key: "header", ok: &openapi.Input{Params: map[string]any{"r": "x"}},
				bad: []refusal{
					{"a number", &openapi.Input{Params: map[string]any{"X-Deep": 5}}, "Inputs", "X-Deep", paramNamed("X-Deep")},
					{"a string", &openapi.Input{Params: map[string]any{"X-Deep": "x"}}, "Inputs", "X-Deep", paramNamed("X-Deep")},
					{"an object", &openapi.Input{Params: map[string]any{"X-Deep": map[string]string{"a": "b"}}}, "Inputs", "X-Deep", paramNamed("X-Deep")},
				}},
			{name: "matrix query parameter", key: "matrix", ok: &openapi.Input{Params: map[string]any{"r": "x"}},
				bad: []refusal{
					{"a nested array", &openapi.Input{Params: map[string]any{"q": [][]string{{"a", "b"}}}}, "Inputs", "q", paramNamed("q")},
					{"an array", &openapi.Input{Params: map[string]any{"q": []string{"a"}}}, "Inputs", "q", paramNamed("q")},
				}},
			// The field's style and its contentType are both defective. Under a
			// multipart range its style governs a multipart/form-data call and
			// its contentType any other (describe.go, Param.ContentType); the
			// field describes one Err, which every call using it wraps.
			{name: "field with a style and a contentType defect", key: "both",
				ok:   &openapi.Input{Body: map[string]any{"ok": "x"}, MediaType: "multipart/form-data"},
				body: firstMedia,
				bad: []refusal{
					{"multipart/form-data, a string", &openapi.Input{Body: map[string]any{"f": "x"}, MediaType: "multipart/form-data"}, "Inputs", "Input.Body/f", fieldNamed("f")},
					{"multipart/form-data, a nested array", &openapi.Input{Body: map[string]any{"f": [][]string{{"a"}}}, MediaType: "multipart/form-data"}, "Inputs", "Input.Body/f", fieldNamed("f")},
					{"multipart/mixed, a string", &openapi.Input{Body: map[string]any{"f": "x"}, MediaType: "multipart/mixed"}, "Inputs", "Input.Body/f", fieldNamed("f")},
				}},
		},
	}, {
		// describe.go, Media.Encoding: "For a positional multipart type in
		// OpenAPI 3.2, it describes the parts: those of prefixEncoding in
		// order, named "0", "1" and so on". load.go, SchemeLookup: "In OpenAPI
		// 3.2, a name that is not a component name where it is looked up is a
		// URI reference to a Security Scheme Object"; doc.go, Credentials:
		// "FromTransport is what satisfies a scheme a requirement names but the
		// document never declares, or declares defectively".
		name: "OpenAPI 3.2",
		doc: editionDoc("3.2.1", `
			"/positional":{"post":{"operationId":"positional","requestBody":{"content":{"multipart/mixed":{
				"prefixEncoding":[{"contentType":"not a media type"},{"contentType":"text/plain"}]}}},`+partErrResponse+`}},
			"/named":{"post":{"operationId":"named","requestBody":{"content":{"multipart/form-data":{`+orderFields+`,
				"encoding":{"f":{"contentType":"not a media type"}}}}},`+partErrResponse+`}},
			"/secure":{"get":{"operationId":"secure","security":[{"`+uri+`":[]}],`+partErrResponse+`}}`,
			`"servers":[{"url":"`+orderBase+`"}]`),
		cases: []orderCase{
			// client.go, Input.Body: a pre-encoded body is not checked
			// "against ... a field's Param.Err", so it is sent.
			{name: "positional part", key: "positional",
				ok:   &openapi.Input{Body: []byte("--b\r\n\r\nraw\r\n--b--\r\n"), MediaType: "multipart/mixed; boundary=b"},
				body: firstMedia,
				bad: []refusal{
					{"a string", &openapi.Input{Body: []any{"x"}}, "Inputs", "Input.Body/0", fieldNamed("0")},
					// client.go, Part.Filename: "A part without a name, as in
					// positional multipart, ... Filename is refused on it."
					{"a Part with a Filename", &openapi.Input{Body: []any{openapi.Part{Content: "x", Filename: "x.txt"}}}, "Inputs", "Input.Body/0", fieldNamed("0")},
				}},
			{name: "named field", key: "named", ok: &openapi.Input{Body: map[string]any{"ok": "x"}}, body: firstMedia,
				bad: []refusal{
					{"a string", &openapi.Input{Body: map[string]any{"f": "x"}}, "Inputs", "Input.Body/f", fieldNamed("f")},
					// client.go, Part.NoFilename: "Setting it with Filename is
					// refused."
					{"a Part with Filename and NoFilename", &openapi.Input{Body: map[string]any{"f": openapi.Part{Content: "x", Filename: "x.txt", NoFilename: true}}}, "Inputs", "Input.Body/f", fieldNamed("f")},
				}},
			{name: "security scheme URI, no credential", key: "secure",
				with: func(o *openapi.Options) { o.Credentials = map[string]openapi.Credential{uri: openapi.FromTransport()} },
				bad:  []refusal{{"no credential", nil, "Settings", `Options.Credentials["` + uri + `"]`, schemeErr}}},
			{name: "security scheme URI, a secret", key: "secure",
				badWith: func(o *openapi.Options) { o.Credentials = map[string]openapi.Credential{uri: openapi.Secret("s")} },
				bad:     []refusal{{"a secret", nil, "Settings", `Options.Credentials["` + uri + `"]`, schemeErr}}},
		},
	}}
}

func TestPartErrRefusals(t *testing.T) {
	l := &openapi.Loader{Fetch: func(context.Context, string) (io.ReadCloser, string, error) { return nil, "", errRetrieval }}
	for _, d := range partErrDocs() {
		for _, cs := range d.cases {
			for _, described := range []bool{true, false} {
				order := map[bool]string{true: "described first", false: "called first"}[described]
				t.Run(fmt.Sprintf("%s/%s/%s", d.name, cs.name, order), func(t *testing.T) {
					c, rec := recordingClient(t, d.doc, l)
					var desc *openapi.Operation
					if described {
						desc = mustOp(t, c, cs.key)
					}
					o := cs.exercise(t, c, rec)
					if desc == nil {
						desc = mustOp(t, c, cs.key)
					}
					if again := mustOp(t, c, cs.key); again != desc {
						t.Errorf("Client.Operation returned %p, then %p", desc, again)
					}
					if desc.Err != nil {
						t.Errorf("Operation.Err = %v; want the defect on the part alone", desc.Err)
					}
					cs.verify(t, desc, o)
				})
			}
		}
	}
}

// The scheme a 3.2 requirement names by a URI whose document cannot be
// retrieved reports why: describe.go, Operation.Err, "Wherever an Err's cause
// is a reference that could not be resolved, it wraps [ErrUnresolved], and the
// retrieval error when retrieving a document failed."
func TestPartErrUnretrievableSchemeURI(t *testing.T) {
	l := &openapi.Loader{Fetch: func(context.Context, string) (io.ReadCloser, string, error) { return nil, "", errRetrieval }}
	docs := partErrDocs()
	c, _ := recordingClient(t, docs[len(docs)-1].doc, l)
	err := schemeErr(mustOp(t, c, "secure"))
	if !errors.Is(err, openapi.ErrUnresolved) || !errors.Is(err, errRetrieval) {
		t.Errorf("SecurityScheme.Err = %v; want it to wrap ErrUnresolved and the retrieval error", err)
	}
}
