package openapi_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Swagger 2.0 lists written as a reference, and the other members of a
// value written as a reference. describe.go, Operation: "In Swagger 2.0,
// consumes so written makes the request body unusable, produces each
// response, and schemes is described as a servers list is ... Bundling
// replaces such a value whole, so its other members are not read". A
// servers list so written "is described as one Server with that Err, which
// Options.BaseURL can replace as it can any unusable server". Operation.Err:
// "A defect in an optional part is reported on that part instead, and fails
// a call only when the call uses it, the *RequestError then wrapping that
// part's Err, whether or not the operation was described first."
//
// The helpers, and the checks of each call in both orders, are those of
// bundlevalues_test.go and describeorder_test.go.

// sendDeclared prepares and sends key with in on c, and checks that the
// request carries desc and the response is governed by desc's response
// status, as client.go, Request.HTTP ("Its context carries the operation for
// OperationFromContext") and Response.Declaration ("the same immutable
// descriptor Operation.Responses exposes") say. Send neither classifies the
// status nor decodes the body (client.go, Request.Send).
func sendDeclared(t *testing.T, c *openapi.Client, desc *openapi.Operation, in *openapi.Input, status string) {
	t.Helper()
	req, err := c.Prepare(desc.Key, in)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := openapi.OperationFromContext(req.HTTP.Context()); got != desc {
		t.Errorf("OperationFromContext = %p; want the described Operation %p", got, desc)
	}
	resp := sendAndClose(t, req)
	if want := respByKey(desc, status); want == nil || resp.Declaration != want {
		t.Errorf("Response.Declaration = %p; want response %s %p", resp.Declaration, status, want)
	}
}

// consumes so written makes the request body unusable: Body.Err says to
// bundle. A call that sends a body uses it and is refused at the body
// (errors.go, RequestError.Inputs: "for the body, "Input.Body""), a
// pre-encoded body too, since Input.Body exempts a pre-encoded body only from
// "a field's Param.Err, required formData fields, or a Media.Err other than an
// invalid key's" and Message.Err is "why the request body ... cannot be used".
// A call without a body, which the operation does not require, does not use it
// and is sent. Root consumes is inherited by an operation that declares none;
// one that declares its own is unaffected, and so is one without a body.
func TestSwaggerConsumesReference(t *testing.T) {
	bodyBundled := func(t *testing.T, op *openapi.Operation) {
		wantUsable(t, "Operation", op.Err)
		if op.Body == nil {
			t.Fatal("no request body")
		}
		wantBundled(t, "Body", op.Body.Err)
	}
	bodyErr := func(op *openapi.Operation) error {
		if op.Body == nil {
			return nil
		}
		return op.Body.Err
	}
	refusals := func(structured any) []refusal {
		return []refusal{
			{"a structured body", &openapi.Input{Body: structured}, "Inputs", "Input.Body", bodyErr},
			{"a pre-encoded body", &openapi.Input{Body: []byte(`{"a":1}`), MediaType: "application/json"}, "Inputs", "Input.Body", bodyErr},
		}
	}
	ref := `"consumes":{"$ref":"other.yaml#/consumes"}`
	body := `"parameters":[{"name":"payload","in":"body","schema":{"type":"object"}}]`
	form := `"parameters":[{"name":"a","in":"formData","type":"string"}]`
	opDoc := editionDoc("2.0", `
		"/body":{"post":{"operationId":"body",`+ref+`,`+body+`,`+swaggerResponse+`}},
		"/form":{"post":{"operationId":"form",`+ref+`,`+form+`,`+swaggerResponse+`}}`)
	rootDoc := editionDoc("2.0", `
		"/body":{"post":{"operationId":"body",`+body+`,`+swaggerResponse+`}},
		"/own":{"post":{"operationId":"own","consumes":["application/json"],`+body+`,`+swaggerResponse+`}},
		"/none":{"get":{"operationId":"none",`+swaggerResponse+`}}`, ref)
	runShapeCases(t, []shapeCase{
		{orderCase: orderCase{name: "operation, body parameter", key: "body", ok: &openapi.Input{}, bad: refusals(map[string]int{"a": 1})}, doc: opDoc, check: bodyBundled},
		{orderCase: orderCase{name: "operation, formData", key: "form", ok: &openapi.Input{}, bad: refusals(map[string]string{"a": "x"})}, doc: opDoc, check: bodyBundled},
		{orderCase: orderCase{name: "root, inherited", key: "body", ok: &openapi.Input{}, bad: refusals(map[string]int{"a": 1})}, doc: rootDoc, check: bodyBundled},
		{orderCase: orderCase{name: "root, own consumes", key: "own", ok: &openapi.Input{Body: map[string]int{"a": 1}}, body: firstMedia}, doc: rootDoc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				if op.Body == nil {
					t.Fatal("no request body")
				}
				wantUsable(t, "Body", op.Body.Err)
			}},
		{orderCase: orderCase{name: "root, no body", key: "none", ok: &openapi.Input{}}, doc: rootDoc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				if op.Body != nil {
					t.Errorf("Body %+v; want none", op.Body)
				}
			}},
	})
}

// produces so written makes each response unusable: every response
// Message.Err says to bundle, a response without a schema included, as the
// text names each response. A response is not part of the request, so the
// call is prepared and sent, and Response.Declaration is the described
// Message. Whether Call reports such a response's Err, or decodes its body,
// is not specified, so the response is read through Send only.
func TestSwaggerProducesReference(t *testing.T) {
	ref := `"produces":{"$ref":"other.yaml#/produces"}`
	responses := `"responses":{"200":{"description":"ok","schema":{"type":"object"}},"404":{"description":"none"}}`
	docs := map[string]string{
		"operation": editionDoc("2.0", `"/out":{"get":{"operationId":"out",`+ref+`,`+responses+`}}`),
		"root": editionDoc("2.0", `
			"/out":{"get":{"operationId":"out",`+responses+`}},
			"/own":{"get":{"operationId":"own","produces":["application/json"],`+responses+`}}`, ref),
	}
	for _, level := range []string{"operation", "root"} {
		for _, order := range []string{"described first", "called first"} {
			t.Run(level+"/"+order, func(t *testing.T) {
				fl := &fetchLog{}
				c, _ := recordingClient(t, docs[level], &openapi.Loader{Fetch: fl.fetch})
				var desc *openapi.Operation
				if order == "described first" {
					desc = mustOp(t, c, "out")
				}
				req, err := c.Prepare("out", nil)
				if err != nil {
					t.Fatalf("Prepare: %v", err)
				}
				resp := sendAndClose(t, req)
				if desc == nil {
					desc = mustOp(t, c, "out")
				}
				wantUsable(t, "Operation", desc.Err)
				if len(desc.Responses) != 2 {
					t.Fatalf("Responses %d; want 2", len(desc.Responses))
				}
				for _, m := range desc.Responses {
					wantBundled(t, "response "+m.Key, m.Err)
				}
				if got := openapi.OperationFromContext(req.HTTP.Context()); got != desc {
					t.Errorf("OperationFromContext = %p; want the described Operation %p", got, desc)
				}
				if want := respByKey(desc, "200"); resp.Declaration != want {
					t.Errorf("Response.Declaration = %p; want response 200 %p", resp.Declaration, want)
				}
				if level == "root" {
					own := mustOp(t, c, "own")
					wantUsable(t, "operation own", own.Err)
					for _, m := range own.Responses {
						wantUsable(t, "operation own, response "+m.Key, m.Err)
					}
				}
				if got := fl.all(); len(got) > 0 {
					t.Errorf("Fetch was asked for %q; want nothing retrieved", got)
				}
			})
		}
	}
}

// schemes so written "is described as a servers list is": one Server whose
// Err says to bundle, Operation.Err nil. A call is refused as for a sole
// unusable server (doc.go, Configuration: one usable server selects itself,
// "and none requires BaseURL"), and Options.BaseURL replaces it. An
// operation with its own schemes does not inherit the root's.
func TestSwaggerSchemesReference(t *testing.T) {
	ref := `"schemes":{"$ref":"other.yaml#/schemes"}`
	soleServer := func(name, doc string) shapeCase {
		return shapeCase{orderCase: orderCase{name: name, key: "s", ok: &openapi.Input{}, with: func(o *openapi.Options) { o.BaseURL = orderBase },
			bad: []refusal{{"its sole server", nil, "Settings", "Options.BaseURL", serverErr}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				if len(op.Servers) != 1 {
					t.Fatalf("Servers %+v; want one Server", op.Servers)
				}
				wantBundled(t, "Server", op.Servers[0].Err)
			}}
	}
	rootDoc := editionDoc("2.0", `
		"/s":{"get":{"operationId":"s",`+swaggerResponse+`}},
		"/own":{"get":{"operationId":"own","schemes":["https"],`+swaggerResponse+`}}`, ref)
	runShapeCases(t, []shapeCase{
		soleServer("operation", editionDoc("2.0", `"/s":{"get":{"operationId":"s",`+ref+`,`+swaggerResponse+`}}`)),
		soleServer("root", rootDoc),
		{orderCase: orderCase{name: "root, own schemes", key: "own", ok: &openapi.Input{}}, doc: rootDoc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				if len(op.Servers) != 1 || op.Servers[0].Err != nil {
					t.Errorf("Servers %+v; want its own usable server", op.Servers)
				}
			}},
	})
}

// "Bundling replaces such a value whole, so its other members are not
// read": an Operation Object, a Responses Object, a response headers map, an
// encoding map and a Server Object's variables map written as a reference
// contribute nothing from their other members. The Operation Object and the
// Responses Object make the Operation unusable (Operation.Err); the headers
// map its Message, the encoding map its Media (describe.go, Media.Err: "an
// encoding map or prefixEncoding list written as a reference"), and the
// variables map its Server.
func TestBundleSiblingsNotRead(t *testing.T) {
	doc := shapeDoc("3.1.2", `
		"/op":{"get":{"$ref":"other.yaml#/op","operationId":"sib","summary":"s",
			"parameters":[{"name":"q","in":"query","schema":{"type":"string"}}],`+partErrResponse+`}},
		"/resp":{"get":{"operationId":"resp","responses":{"$ref":"other.yaml#/r",
			"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}},
		"/hdr":{"get":{"operationId":"hdr","responses":{"200":{"description":"ok",
			"headers":{"$ref":"h.yaml","X-A":{"schema":{"type":"string"}}},
			"content":{"application/json":{"schema":{"type":"object"}}}}}}},
		"/enc":{"post":{"operationId":"enc","requestBody":{"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"a":{"type":"string"}}},
			"encoding":{"$ref":"e.yaml","f":{"contentType":"text/plain"}}}}},`+partErrResponse+`}},
		"/var":{"get":{"operationId":"var","servers":[{"url":"https://{host}/v1",
			"variables":{"$ref":"v.yaml","host":{"default":"api.example.test"}}}],`+partErrResponse+`}}`)
	swaggerDoc := editionDoc("2.0", `"/hdr":{"get":{"operationId":"hdr","produces":["application/json"],"responses":{"200":{"description":"ok","schema":{"type":"object"},
		"headers":{"$ref":"h.yaml","X-A":{"type":"string"}}}}}}`)
	headersCheck := func(t *testing.T, op *openapi.Operation) {
		wantUsable(t, "Operation", op.Err)
		m := respByKey(op, "200")
		if m == nil {
			t.Fatal("no response 200")
		}
		wantBundled(t, "response 200", m.Err)
		if len(m.Headers) != 0 {
			t.Errorf("headers %q; want none read from beside the reference", paramNames(m.Headers))
		}
	}
	headersSent := func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
		sendDeclared(t, c, op, nil, "200")
	}
	runShapeCases(t, []shapeCase{
		{orderCase: orderCase{name: "Operation Object", key: "GET /op", bad: []refusal{{"a call", nil, "Err", "", opErr}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantBundled(t, "Operation", op.Err)
				if op.Key != "GET /op" || op.ID != "" || op.Summary != "" || len(op.Params) != 0 || len(op.Responses) != 0 || op.Body != nil {
					t.Errorf("Operation Key %q ID %q Summary %q, %d Params, %d Responses, Body %v; want nothing read from beside the reference",
						op.Key, op.ID, op.Summary, len(op.Params), len(op.Responses), op.Body)
				}
			},
			after: func(t *testing.T, c *openapi.Client, _ *openapi.Operation) {
				if _, err := c.Operation("sib"); !errors.Is(err, openapi.ErrNoOperation) {
					t.Errorf("Operation(\"sib\") error %v; want ErrNoOperation, the operationId beside the reference not being read", err)
				}
			}},
		{orderCase: orderCase{name: "Responses Object", key: "resp", bad: []refusal{{"a call", nil, "Err", "", opErr}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantBundled(t, "Operation", op.Err)
				if len(op.Responses) != 0 {
					t.Errorf("%d Responses; want none read from beside the reference", len(op.Responses))
				}
			}},
		{orderCase: orderCase{name: "response headers map", key: "hdr"}, doc: doc, check: headersCheck, after: headersSent},
		{orderCase: orderCase{name: "Swagger 2.0 response headers map", key: "hdr"}, doc: swaggerDoc, check: headersCheck, after: headersSent},
		{orderCase: orderCase{name: "encoding map", key: "enc",
			ok: &openapi.Input{Body: []byte(rawFormData), MediaType: "multipart/form-data; boundary=b"}, body: firstMedia,
			bad: []refusal{{"a structured body", &openapi.Input{Body: map[string]string{"a": "x"}}, "Inputs", "Input.Body", mediaErrAt(0)}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				m := mediaAt(0)(op)
				if m == nil {
					t.Fatal("no request body Media")
				}
				wantBundled(t, "Media", m.Err)
				for _, n := range []string{"f", "$ref"} {
					if p := paramByName(m.Encoding, n); p != nil {
						t.Errorf("Media.Encoding describes %q, read from beside the reference: %+v", n, p)
					}
				}
			}},
		{orderCase: orderCase{name: "Server variables map", key: "var", ok: &openapi.Input{}, with: func(o *openapi.Options) { o.BaseURL = orderBase },
			bad: []refusal{{"its sole server", nil, "Settings", "Options.BaseURL", serverErr}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				if len(op.Servers) != 1 {
					t.Fatalf("Servers %+v; want one Server", op.Servers)
				}
				s := op.Servers[0]
				wantBundled(t, "Server", s.Err)
				// describe.go, Server.Variables: "the variables in URL"; Variable.Declared
				// "reports that the server declares the variable".
				for _, v := range s.Variables {
					if v.Declared || v.DefaultSet {
						t.Errorf("variable %+v; want nothing read from beside the reference", v)
					}
				}
			}},
	})
}

// A Paths Object written as a reference is listed once with an empty Key
// and Path (describe.go, Operations), and its other members are not read, so
// no path beside the reference is an operation.
func TestBundleSiblingsPathsObject(t *testing.T) {
	for _, v := range []string{"2.0", "3.1.2"} {
		t.Run(v, func(t *testing.T) {
			field, resp := "openapi", partErrResponse
			if v == "2.0" {
				field, resp = "swagger", swaggerResponse
			}
			doc := fmt.Sprintf(`{%q:%q,"info":{"title":"t","version":"1"},"paths":{"$ref":"other.yaml#/paths","/a":{"get":{"operationId":"a",%s}}}}`, field, v, resp)
			fl := &fetchLog{}
			c, _ := recordingClient(t, doc, &openapi.Loader{Fetch: fl.fetch})
			ops := c.Operations()
			if len(ops) != 1 {
				keys := make([]string, len(ops))
				for i, op := range ops {
					keys[i] = op.Key
				}
				t.Fatalf("Operations lists %q; want the Paths Object alone", keys)
			}
			if ops[0].Key != "" || ops[0].Path != "" {
				t.Errorf("entry Key %q Path %q; want both empty", ops[0].Key, ops[0].Path)
			}
			wantBundled(t, "entry", ops[0].Err)
			for _, key := range []string{"GET /a", "a"} {
				if _, err := c.Operation(key); !errors.Is(err, openapi.ErrNoOperation) {
					t.Errorf("Operation(%q) error %v; want ErrNoOperation", key, err)
				}
			}
			if got := fl.all(); len(got) > 0 {
				t.Errorf("Fetch was asked for %q; want nothing retrieved", got)
			}
		})
	}
}

// An OpenAPI 3.2 querystring parameter under form content is "an object
// written by the form-body rules, Encoding included" (doc.go, Querystring).
// A field whose Encoding has a contentType that is not a media type is a
// defect of that field, not a value written as a reference, so it "fails a
// call only when the call uses it" (describe.go, Operation.Err): Param.Err
// stays nil, since Param.Err is why built-in serialization "cannot use the
// value" at all; a value without the field is sent; a value with it is
// refused at the parameter's key (errors.go, RequestError.Inputs). The field
// has no descriptor of its own, so which Err the refusal wraps is not
// specified, and only the key is checked.
func TestQuerystringFieldDefect(t *testing.T) {
	doc := shapeDoc("3.2.1", `"/qs":{"get":{"operationId":"qs","parameters":[{"name":"qs","in":"querystring","content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}},
		"encoding":{"a":{"contentType":"not a media type"}}}}}],`+partErrResponse+`}}`)
	b := &openapi.Input{Params: map[string]any{"qs": map[string]string{"b": "y"}}}
	runShapeCases(t, []shapeCase{{
		orderCase: orderCase{name: "3.2.1", key: "qs", ok: b,
			bad: []refusal{{"a value with the field", &openapi.Input{Params: map[string]any{"qs": map[string]string{"a": "x"}}}, "Inputs", "qs", nil}}},
		doc: doc,
		check: func(t *testing.T, op *openapi.Operation) {
			wantUsable(t, "Operation", op.Err)
			wantUsable(t, "parameter qs", paramNamed("qs")(op))
		},
		after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
			req := mustPrepare(t, c, op.Key, b)
			if got := req.HTTP.URL.RawQuery; got != "b=y" {
				t.Errorf("query %q; want b=y", got)
			}
		},
	}})
}
