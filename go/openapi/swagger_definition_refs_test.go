package openapi_test

import (
	"errors"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// describe.go, Operation: in Swagger 2.0, "an entry of its root parameters or
// responses map, which holds Parameter or Response Objects only, is such a
// value when written as a reference [one that marks a document meant to be
// bundled before use]: a parameter entry makes each operation that uses it
// unusable, since the parameter's identity cannot be read, and a response
// entry each response that uses it"; such an Err "says the document must be
// bundled first, and does not wrap ErrUnresolved", and calling an operation
// with Err set "returns a *RequestError wrapping Err". A Reference Object
// that names an entry, the ordinary Swagger 2.0 reference, still resolves,
// whether or not another entry is written as a reference to the same target.
func TestSwaggerDefinitionEntryWrittenAsReference(t *testing.T) {
	doc := editionDoc("2.0", `
		"/p":{"get":{"operationId":"p","parameters":[{"$ref":"#/parameters/P"}],"responses":{"200":{"description":"ok"}}}},
		"/q":{"get":{"operationId":"q","parameters":[{"$ref":"#/parameters/Q"}],"responses":{"200":{"description":"ok"}}}},
		"/r":{"get":{"operationId":"r","responses":{"200":{"$ref":"#/responses/R"},"201":{"description":"own"}}}},
		"/s":{"get":{"operationId":"s","responses":{"200":{"$ref":"#/responses/S"}}}}`,
		`"host":"api.example.test"`,
		`"parameters":{"P":{"$ref":"#/parameters/Q"},"Q":{"name":"q","in":"query","type":"string"}}`,
		`"responses":{"R":{"$ref":"#/responses/S"},"S":{"description":"s"}}`)
	c := editionClient(t, doc, nil)

	p := mustOp(t, c, "p")
	wantBundled(t, "operation using #/parameters/P", p.Err)
	if _, err := c.Prepare("p", nil); !errors.Is(err, p.Err) {
		t.Errorf("Prepare(p) = %v; want an error wrapping the operation's Err", err)
	}

	q := mustOp(t, c, "q")
	if q.Err != nil || len(q.Params) != 1 || q.Params[0].Name != "q" || q.Params[0].Source != testDocURI+"#/parameters/Q" || q.Params[0].Err != nil {
		t.Errorf("operation using #/parameters/Q: Err %v, Params %+v; want the parameter q from #/parameters/Q", q.Err, q.Params)
	}
	mustPrepare(t, c, "q", &openapi.Input{Params: map[string]any{"q": "v"}})

	r := mustOp(t, c, "r")
	if r.Err != nil {
		t.Errorf("operation using #/responses/R: Err %v; want only the response unusable", r.Err)
	}
	wantBundled(t, "response using #/responses/R", response(t, r, 0).Err)
	if own := response(t, r, 1); own.Err != nil {
		t.Errorf("the operation's own response: Err %v", own.Err)
	}

	s := mustOp(t, c, "s")
	if s.Err != nil || response(t, s, 0).Err != nil || response(t, s, 0).Source != testDocURI+"#/responses/S" {
		t.Errorf("operation using #/responses/S: Err %v, response %+v; want the response from #/responses/S", s.Err, response(t, s, 0))
	}
}
