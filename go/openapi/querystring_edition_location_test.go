package openapi_test

import (
	"fmt"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// locationDoc builds a document of edition version, served by @BASE@, with
// two operations whose parameters use the location loc: "beside", a query
// parameter q and a parameter qs in loc; "two", parameters a and b in loc.
func locationDoc(version, loc string) string {
	param := func(name string) string {
		if version == "2.0" {
			return fmt.Sprintf(`{"name":%q,"in":%q,"type":"string"}`, name, loc)
		}
		return fmt.Sprintf(`{"name":%q,"in":%q,"content":{"application/x-www-form-urlencoded":{}}}`, name, loc)
	}
	query := `{"name":"q","in":"query","schema":{"type":"string"}}`
	server := `"servers":[{"url":"@BASE@"}]`
	if version == "2.0" {
		query, server = `{"name":"q","in":"query","type":"string"}`, `"host":"@HOSTPORT@","schemes":["http"]`
	}
	return editionDoc(version, `"/beside":{"get":{"operationId":"beside","parameters":[`+query+`,`+param("qs")+`],"responses":{"200":{"description":"ok"}}}},
		"/two":{"get":{"operationId":"two","parameters":[`+param("a")+`,`+param("b")+`],"responses":{"200":{"description":"ok"}}}}`, server)
}

// "in": "querystring" is a location only in OpenAPI 3.2. In Swagger 2.0,
// OpenAPI 3.0 and 3.1 it is an unknown location, handled as any other: the
// parameter has Param.Err, the operation is usable, beside a query
// parameter or another such parameter, and a call fails only when it gives
// that parameter. describe.go, Param.In: "path", "query", "header", "cookie",
// or "querystring" (3.2); doc.go, Fixed rules: "Querystring: an OpenAPI 3.2
// querystring parameter is the whole query"; Operation.Err: "A defect in an
// optional part is reported on that part instead, and fails a call only when
// the call uses it".
func TestQuerystringIsUnknownBefore32(t *testing.T) {
	for _, version := range []string{"2.0", "3.0.4", "3.1.2"} {
		for _, loc := range []string{"querystring", "elsewhere"} {
			t.Run(version+" "+loc, func(t *testing.T) {
				w := newWire(t, nil)
				c := parseFor(t, w, locationDoc(version, loc), nil)
				for _, key := range []string{"beside", "two"} {
					op := mustOp(t, c, key)
					if op.Err != nil {
						t.Errorf("%s Err = %v, want nil: a parameter in an unknown location is a defect of that parameter", key, op.Err)
					}
					for _, p := range op.Params {
						if (p.In == loc) != (p.Err != nil) {
							t.Errorf("%s parameter %s in %s: Err %v", key, p.Name, p.In, p.Err)
						}
					}
				}
				mustCall(t, c, "beside", &openapi.Input{Params: map[string]any{"q": "v"}}, nil)
				if got := w.only(t).RequestURI; got != "/beside?q=v" {
					t.Errorf("request target %q, want /beside?q=v", got)
				}
				mustCall(t, c, "two", nil, nil)
				resp, err := c.Call(t.Context(), "beside", &openapi.Input{Params: map[string]any{"q": "v", "qs": "x"}}, nil)
				re := refusedSince(t, w, 2, resp, err)
				wantKeys(t, "Inputs", re.Inputs, true, "qs")
			})
		}
	}
}

// In OpenAPI 3.2 querystring is a location, and a querystring parameter
// beside a query parameter, or beside another, is a defect of the
// operation. describe.go, Operation.Err: "a querystring parameter beside
// another query or querystring parameter".
func TestQuerystringBesideAnotherIn32(t *testing.T) {
	c := parseAt(t, locationDoc("3.2.0", "querystring"), "https://a.example", testDocURI, nil)
	for _, key := range []string{"beside", "two"} {
		if op := mustOp(t, c, key); op.Err == nil {
			t.Errorf("%s Err = nil, want the querystring defect", key)
		}
	}
}
