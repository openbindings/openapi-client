package openapi_test

import (
	"context"
	"io"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Parameters across editions. load.go, Loader: "An OpenAPI document uses the
// edition declared at its root." describe.go, Operation.Params holds "no
// header parameter that OpenAPI 3.x tells clients to ignore (Accept,
// Content-Type, Authorization, matched without regard to case)", while "A
// Swagger 2.0 Content-Type header parameter is listed" and "A Swagger 2.0
// Accept parameter's value is sent as the Accept field". So whether a header
// parameter is dropped follows the edition of the document the parameter is
// written in, not the operation's. A dropped parameter is not declared: a
// value for it refuses the call (client.go, Input.Params: "A key the
// operation does not declare ... refuses the call"), and a value written as
// a reference in it is held by nothing nearer than the operation
// (Operation.Err: "another value whose nearest part is the operation"). An
// operation's own parameter still overrides an inherited one with the same
// location and name (doc.go, Order), whatever the editions. The harness, and
// the checks of each call in both orders and of nothing more being fetched
// than the documents referenced, are those of bundlevalues_test.go and
// describeorder_test.go.

// v3Parameters is an OpenAPI 3.1.0 document of components.parameters: a
// required Authorization header, and an Accept whose content's encoding map
// is written as a reference.
const v3Parameters = `{"openapi":"3.1.0","info":{"title":"o","version":"1"},"paths":{},"components":{"parameters":{
	"Auth":{"name":"Authorization","in":"header","required":true,"schema":{"type":"string"}},
	"AcceptBad":{"name":"Accept","in":"header","content":{"multipart/mixed":{"schema":{"type":"object"},"encoding":{"$ref":"e.yaml"}}}}}}}`

// v2Document is a Swagger 2.0 document with a Path Item /y, holding operation
// y, and a parameters entry Accept.
const v2Document = `{"swagger":"2.0","info":{"title":"o","version":"1"},"paths":{
	"/y":{"get":{"operationId":"y","produces":["application/json"],"responses":{"200":{"description":"ok","schema":{"type":"object"}}}}}},
	"parameters":{"Accept":{"name":"Accept","in":"header","type":"string"}}}`

// editionsLoader serves the referenced documents and records every URI
// asked for.
func editionsLoader() (*openapi.Loader, *fetchLog) {
	serve := mapFetch(map[string]string{
		"https://api.example.test/v3.json": v3Parameters,
		"https://api.example.test/v2.json": v2Document,
	})
	log := &fetchLog{}
	return &openapi.Loader{Fetch: func(ctx context.Context, u string) (io.ReadCloser, string, error) {
		log.fetch(ctx, u)
		return serve(ctx, u)
	}}, log
}

func TestBundleEditionsOfParameters(t *testing.T) {
	entry2 := editionDoc("2.0", `
		"/c":{"get":{"operationId":"c","parameters":[{"$ref":"v3.json#/components/parameters/Auth"}],`+swaggerResponse+`}},
		"/b":{"get":{"operationId":"b","parameters":[{"$ref":"v3.json#/components/parameters/AcceptBad"}],`+swaggerResponse+`}},
		"/d":{"parameters":[{"$ref":"v3.json#/components/parameters/AcceptBad"}],
			"get":{"operationId":"d","parameters":[{"name":"accept","in":"header","type":"string"}],`+swaggerResponse+`}}`)
	entry3 := shapeDoc("3.1.2", `
		"/x":{"$ref":"v2.json#/paths/~1y","parameters":[
			{"name":"Authorization","in":"header","required":true,"schema":{"type":"string"}},
			{"name":"Accept","in":"header","content":{"multipart/mixed":{"schema":{"type":"object"},"encoding":{"$ref":"e.yaml"}}}}]},
		"/a":{"get":{"operationId":"a","parameters":[{"$ref":"v2.json#/parameters/Accept"}],`+partErrResponse+`}}`)
	noParams := func(t *testing.T, op *openapi.Operation) {
		if len(op.Params) != 0 {
			t.Errorf("Params %q; want the 3.x header parameter dropped", paramNames(op.Params))
		}
	}
	bundled := func(t *testing.T, op *openapi.Operation) {
		noParams(t, op)
		wantBundled(t, "Operation", op.Err)
	}
	refusedOp := []refusal{{"a call", nil, "Err", "", opErr}}
	cases := []struct {
		shapeCase
		entry string
	}{
		{shapeCase{orderCase: orderCase{name: "2.0 operation, 3.1 Authorization", key: "c", ok: &openapi.Input{},
			bad: []refusal{{"a value for Authorization", &openapi.Input{Params: map[string]any{"Authorization": "Bearer x"}}, "Inputs", "Authorization", nil}}},
			check: func(t *testing.T, op *openapi.Operation) { wantUsable(t, "Operation", op.Err); noParams(t, op) }}, entry2},
		{shapeCase{orderCase: orderCase{name: "2.0 operation, 3.1 Accept holding a reference", key: "b", bad: refusedOp}, check: bundled}, entry2},
		{shapeCase{orderCase: orderCase{name: "2.0 operation's accept overriding a 3.1 path-level Accept", key: "d",
			ok: &openapi.Input{Params: map[string]any{"accept": "text/x"}}},
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				if len(op.Params) != 1 || op.Params[0].Name != "accept" || op.Params[0].CollectionFormat != "" || op.Params[0].Err != nil {
					t.Errorf("Params %+v; want the 2.0 accept alone", op.Params)
				}
			},
			after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
				req := mustPrepare(t, c, op.Key, &openapi.Input{Params: map[string]any{"accept": "text/x"}})
				if got := req.HTTP.Header.Get("Accept"); got != "text/x" {
					t.Errorf("Accept %q; want text/x", got)
				}
			}}, entry2},
		{shapeCase{orderCase: orderCase{name: "2.0 operation under 3.1 path-level parameters", key: "y", bad: refusedOp}, check: bundled}, entry3},
		{shapeCase{orderCase: orderCase{name: "3.1 operation, 2.0 Accept", key: "a", ok: &openapi.Input{Params: map[string]any{"Accept": "text/x"}}},
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				if len(op.Params) != 1 || op.Params[0].Name != "Accept" || op.Params[0].In != "header" || op.Params[0].Err != nil {
					t.Errorf("Params %+v; want the 2.0 Accept listed", op.Params)
				}
			},
			after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
				req := mustPrepare(t, c, op.Key, &openapi.Input{Params: map[string]any{"Accept": "text/x"}})
				if got := req.HTTP.Header.Get("Accept"); got != "text/x" {
					t.Errorf("Accept %q; want text/x", got)
				}
			}}, entry3},
	}
	for _, tc := range cases {
		for _, order := range []string{"described first", "called first"} {
			t.Run(tc.name+"/"+order, func(t *testing.T) {
				l, log := editionsLoader()
				c, rec := recordingClient(t, tc.entry, l)
				var desc *openapi.Operation
				if order == "described first" {
					desc = mustOp(t, c, tc.key)
					tc.check(t, desc)
				}
				o := tc.exercise(t, c, rec)
				if desc == nil {
					desc = mustOp(t, c, tc.key)
					tc.check(t, desc)
				}
				if again := mustOp(t, c, tc.key); again != desc {
					t.Errorf("Client.Operation returned %p, then %p", desc, again)
				}
				tc.verify(t, desc, o)
				if tc.after != nil {
					tc.after(t, c, desc)
				}
				for _, u := range log.all() {
					if u != "https://api.example.test/v3.json" && u != "https://api.example.test/v2.json" {
						t.Errorf("Fetch was asked for %s; want only the documents referenced", u)
					}
				}
			})
		}
	}
}
