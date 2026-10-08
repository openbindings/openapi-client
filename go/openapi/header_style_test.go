package openapi_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// describe.go, Param.Style: "the RFC 6570 style the value is serialized with,
// as declared or as OpenAPI defaults it for the location (... "simple" for
// path and header), or empty when the value is serialized by ContentType
// instead, and in Swagger 2.0"; Param.Explode: "the effective explode ..., and
// ExplodeSet whether the document writes it". A response header and an
// Encoding header described by a schema are header values, so Style is
// "simple" and Explode false, or true where the Header Object writes explode
// true (OAS 3.1.2 section 4.8.21: in a Header Object, "style, if used, MUST
// be limited to "simple""); one described by content has no Style, nor has a
// Swagger 2.0 header.
func TestHeaderParamsReportSimpleStyle(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		t.Run(version, func(t *testing.T) {
			c := editionClient(t, editionDoc(version, `"/x":{"post":{"operationId":"x",
				"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"f":{}}},
					"encoding":{"f":{"headers":{"X-E":{"schema":{"type":"string"}}}}}}}},
				"responses":{"200":{"description":"d","headers":{
					"X-H":{"schema":{"type":"string"}},
					"X-X":{"schema":{"type":"array","items":{"type":"string"}},"explode":true},
					"X-C":{"content":{"text/plain":{}}}}}}}}`), nil)
			op := mustOp(t, c, "x")
			for _, tc := range []struct {
				p                  *openapi.Param
				style              string
				explode, explodeOK bool
			}{
				{headerNamed(t, response(t, op, 0).Headers, "X-H"), "simple", false, false},
				{headerNamed(t, response(t, op, 0).Headers, "X-X"), "simple", true, true},
				{headerNamed(t, response(t, op, 0).Headers, "X-C"), "", false, false},
				{headerNamed(t, reqMedia(t, op, 0).Encoding[0].Headers, "X-E"), "simple", false, false},
			} {
				if tc.p.Style != tc.style || tc.p.Explode != tc.explode || tc.p.ExplodeSet != tc.explodeOK {
					t.Errorf("%s: Style %q, Explode %t, ExplodeSet %t; want %q, %t, %t", tc.p.Name, tc.p.Style, tc.p.Explode, tc.p.ExplodeSet, tc.style, tc.explode, tc.explodeOK)
				}
			}
		})
	}
	c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"operationId":"x","responses":{"200":{"description":"d","headers":{"X-H":{"type":"string"}}}}}}`), nil)
	if h := headerNamed(t, response(t, mustOp(t, c, "x"), 0).Headers, "X-H"); h.Style != "" {
		t.Errorf("Swagger 2.0 header: Style %q, want none", h.Style)
	}
}

// describe.go, Param.Style: "A response or Encoding header described by a
// schema has "simple", the only style a Header Object allows, even where it
// declares another style, which Err then reports as not allowed" (OAS 3.0.4
// section 4.7.21, 3.1.2 section 4.8.21 and 3.2.1 section 4.21, Header Object:
// "style, if used, MUST be limited to "simple""; its style field: "The default
// (and only legal value for headers) is "simple""). A response header that
// declares "form" and an Encoding header that declares "matrix" have Style
// "simple" and an Err naming the declared style; one that declares "simple"
// has none. describe.go, Operation.Err: "A defect in an optional part is
// reported on that part instead, and fails a call only when the call uses
// it", so the operation has no Err; a response header is not part of the
// request, so the call is sent, and doc.go, Outcomes, classifies its
// response: "A 2xx, decoded: a nil error".
func TestHeaderDeclaredStyleIsErr(t *testing.T) {
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-F", "a,b")
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, `{"ok":true}`)
	})
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		t.Run(version, func(t *testing.T) {
			c := parseFor(t, w, editionDoc(version, `"/x":{"post":{"operationId":"x",
				"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"f":{}}},
					"encoding":{"f":{"headers":{"X-E":{"style":"matrix","schema":{"type":"string"}}}}}}}},
				"responses":{"200":{"description":"d","content":{"application/json":{}},"headers":{
					"X-F":{"style":"form","schema":{"type":"array","items":{"type":"string"}}},
					"X-S":{"style":"simple","schema":{"type":"string"}}}}}}}`, `"servers":[{"url":"@BASE@"}]`), nil)
			op := mustOp(t, c, "x")
			if op.Err != nil {
				t.Errorf("Operation.Err %v; want none", op.Err)
			}
			for _, tc := range []struct {
				p        *openapi.Param
				declared string // a style other than "simple", or empty
			}{
				{headerNamed(t, response(t, op, 0).Headers, "X-F"), "form"},
				{headerNamed(t, reqMedia(t, op, 0).Encoding[0].Headers, "X-E"), "matrix"},
				{headerNamed(t, response(t, op, 0).Headers, "X-S"), ""},
			} {
				if tc.p.Style != "simple" {
					t.Errorf("%s: Style %q; want \"simple\"", tc.p.Name, tc.p.Style)
				}
				switch {
				case tc.declared == "" && tc.p.Err != nil:
					t.Errorf("%s: Err %v; want none", tc.p.Name, tc.p.Err)
				case tc.declared != "" && (tc.p.Err == nil || !strings.Contains(tc.p.Err.Error(), strconv.Quote(tc.declared))):
					t.Errorf("%s: Err %v; want one naming the declared style %q", tc.p.Name, tc.p.Err, tc.declared)
				}
			}
			before := w.count()
			var out map[string]any
			resp, err := c.Call(t.Context(), "x", nil, &out)
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if n := w.count() - before; n != 1 {
				t.Errorf("server received %d requests; want 1", n)
			}
			if resp.Declaration != response(t, op, 0) || out["ok"] != true {
				t.Errorf("Declaration %p, out %v; want response 200 %p and the decoded body", resp.Declaration, out, response(t, op, 0))
			}
		})
	}
}
