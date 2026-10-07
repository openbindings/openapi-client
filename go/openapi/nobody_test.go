package openapi_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// A body for an operation that takes none. client.go, Input.Body: "A body
// for an operation that takes none is refused at Inputs["Input.Body"]; to
// send one anyway, Prepare the call and set the body on Request.HTTP."
// Request.HTTP: "HTTP may be changed before sending, to set a header the
// document cannot express, say, or to add a raw body the operation does not
// declare ... a caller who replaces the body sets GetBody and ContentLength
// with it, or clears GetBody." errors.go, RequestError.Error: "Error
// describes every problem and the field that fixes each", so the refusal
// names the route, Prepare. Operation.Body: "nil when the operation takes
// none, as for an OpenAPI 3.0 GET (see Bodies by method in the package
// documentation)"; doc.go, Fixed rules, Bodies by method: "a request body
// declared on TRACE or CONNECT, and in OpenAPI 3.0 on GET, HEAD, DELETE or
// OPTIONS, as 3.0 says, is ignored, so the operation takes none".

// noBodyDoc has, in each edition, operations that take no body: one that
// declares none, and one whose declared body the edition's rules ignore.
func noBodyDoc(version string) (doc string, keys []string) {
	switch version {
	case "2.0":
		return editionDoc(version, `
			"/plain":{"get":{"operationId":"plain","parameters":[{"name":"q","in":"query","type":"string"}],"responses":{"200":{"description":"ok"}}}},
			"/del":{"delete":{"operationId":"del","responses":{"204":{"description":"gone"}}}}`,
			`"host":"@HOSTPORT@"`, `"schemes":["http"]`), []string{"plain", "del"}
	case "3.0.4":
		return editionDoc(version, `
			"/plain":{"get":{"operationId":"plain","responses":{"200":{"description":"ok"}}}},
			"/ignored":{"get":{"operationId":"ignored","requestBody":{"content":{"application/json":{}}},"responses":{"200":{"description":"ok"}}}}`,
			`"servers":[{"url":"@BASE@"}]`), []string{"plain", "ignored"}
	}
	return editionDoc(version, `
		"/plain":{"get":{"operationId":"plain","responses":{"200":{"description":"ok"}}}},
		"/trace":{"trace":{"operationId":"ignored","requestBody":{"content":{"application/json":{}}},"responses":{"200":{"description":"ok"}}}}`,
		`"servers":[{"url":"@BASE@"}]`), []string{"plain", "ignored"}
}

// Every kind of body is refused at Inputs["Input.Body"] alone, nothing is
// sent, and the error names Prepare; with no body the call is sent.
func TestBodyForOperationWithoutBody(t *testing.T) {
	w := newWire(t, nil)
	bodies := []struct {
		name string
		v    any
	}{
		{"a value", map[string]any{"a": 1}},
		{"a string", "x"},
		// A pre-encoded body is still a body (Input.Body: "a nil interface
		// for none").
		{"bytes", []byte(`{"a":1}`)},
		{"a reader", strings.NewReader(`{"a":1}`)},
		{"empty bytes", []byte{}},
		// "a typed nil is a value; see Values".
		{"a typed nil", (*int)(nil)},
	}
	for _, version := range editionVersions {
		doc, keys := noBodyDoc(version)
		c := parseFor(t, w, doc, nil)
		for _, key := range keys {
			if op := mustOp(t, c, key); op.Body != nil || op.Err != nil {
				t.Fatalf("%s %s: Body %+v, Err %v; want an operation that takes no body", version, key, op.Body, op.Err)
			}
			for _, b := range bodies {
				t.Run(version+"/"+key+"/"+b.name, func(t *testing.T) {
					before := w.count()
					resp, err := c.Call(t.Context(), key, &openapi.Input{Body: b.v}, nil)
					re := refusedSince(t, w, before, resp, err)
					wantKeys(t, "Inputs", re.Inputs, true, "Input.Body")
					if len(re.Settings) != 0 || re.Err != nil {
						t.Errorf("Settings %q, Err %v; want only the body refused", sortedKeys(re.Settings), re.Err)
					}
					if e := re.Inputs["Input.Body"]; e == nil || !strings.Contains(e.Error(), "Prepare") {
						t.Errorf("Inputs[\"Input.Body\"] = %v; want it to name Prepare", e)
					}
					if !strings.Contains(err.Error(), "Prepare") {
						t.Errorf("error %q does not name Prepare", err)
					}
					if _, err := c.Prepare(key, &openapi.Input{Body: b.v}); err == nil {
						t.Errorf("Prepare accepted a body for an operation that takes none")
					} else {
						wantKeys(t, "Prepare Inputs", asRequestError(t, err).Inputs, true, "Input.Body")
					}
				})
			}
			t.Run(version+"/"+key+"/no body", func(t *testing.T) {
				mustCall(t, c, key, &openapi.Input{}, nil)
				if got := w.last(t); len(got.Body) != 0 {
					t.Errorf("sent a body %q", got.Body)
				}
			})
		}
	}
}

// The documented route: Prepare the call with no body, set one on
// Request.HTTP with its ContentLength and GetBody, and Send; the server
// receives it, with the method and target Prepare built. Request.Call
// takes the same request.
func TestBodyForOperationWithoutBodyThroughPrepare(t *testing.T) {
	w := newWire(t, nil)
	const payload = `{"note":"not declared"}`
	setBody := func(req *openapi.Request) {
		req.HTTP.Body = io.NopCloser(strings.NewReader(payload))
		req.HTTP.ContentLength = int64(len(payload))
		req.HTTP.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(payload)), nil
		}
		req.HTTP.Header.Set("Content-Type", "application/json")
	}
	for _, version := range editionVersions {
		doc, keys := noBodyDoc(version)
		c := parseFor(t, w, doc, nil)
		for _, key := range keys {
			t.Run(version+"/"+key, func(t *testing.T) {
				op := mustOp(t, c, key)
				req := mustPrepare(t, c, key, nil)
				setBody(req)
				sendAndClose(t, req)
				got := w.last(t)
				if got.Method != op.Method || !bytes.Equal(got.Body, []byte(payload)) || got.ContentLength != int64(len(payload)) {
					t.Errorf("server received %s with body %q (Content-Length %d); want %s with %q", got.Method, got.Body, got.ContentLength, op.Method, payload)
				}
				if ct := got.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type %q", ct)
				}

				req = mustPrepare(t, c, key, nil)
				setBody(req)
				if _, err := req.Call(t.Context(), nil); err != nil {
					t.Fatalf("Request.Call: %v", err)
				}
				if got := w.last(t); !bytes.Equal(got.Body, []byte(payload)) {
					t.Errorf("Request.Call: server received body %q", got.Body)
				}
			})
		}
	}
}
