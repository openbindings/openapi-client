package openapi_test

import (
	"errors"
	"fmt"
	"mime"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// A request body declared under a media range is prepared the same way
// whether or not the operation was described first. describe.go,
// Operation.Err: a defect in an optional part fails a call only when the
// call uses it, "the *RequestError then wrapping that part's Err, whether or
// not the operation was described first"; client.go, Request.Media: "It is
// the same immutable descriptor Operation.Body.Media exposes."
//
// What each call does follows from:
//
//   - client.go, Input.Body: "A nil Body where the request body is required
//     is refused at Inputs["Input.Body"]"; "For form and multipart media,
//     Body is an object (a map or a struct) whose properties are the
//     fields"; a []byte "is sent as its bytes, under whatever media type is
//     chosen".
//   - doc.go, Configuration: "A body uses the declared request media type
//     when exactly one is declared and it is concrete; otherwise, a range
//     counting as an alternative, it requires Options.MediaType or
//     Input.MediaType"; errors.go, RequestError.Settings: "an undetermined or
//     unusable request media type "Input.MediaType"".
//   - client.go, Input.MediaType: "a concrete type matching one the
//     operation declares, by the rules on Response.Media"; Response.Media:
//     "The most specific match wins: a concrete type over type/*, type/*
//     over */*".
//   - client.go, Request.Media: "nil when there is no body".
//   - describe.go, Message.Media: in Swagger 2.0 "the schema of a body
//     parameter ... is paired with each consumes or produces entry, and
//     formData with the form types among them"; Media.Encoding: "in Swagger
//     2.0 every formData parameter".
//
// Each operation's calls run on a fresh Client described first (by
// Operations, then Prepare) and on another called first (Prepare, then
// Operations); the two must report the same outcome for every call, none
// may panic, and each must match the documentation above.

// A rangeCall is a call and what it must do.
type rangeCall struct {
	name string
	in   *openapi.Input
	// For a call that is sent: media is the index in Body.Media of the Media
	// governing it, or -1 for no body, and check, if set, checks what is sent.
	media int
	check func(t *testing.T, ctype string, body []byte)
	// For a refused call: field and key locate the refusal, and defect, if
	// set, returns the described Err it must wrap.
	field, key string
	defect     func(op *openapi.Operation) error
}

func rangeSent(name string, in *openapi.Input, media int, check func(*testing.T, string, []byte)) rangeCall {
	return rangeCall{name: name, in: in, media: media, check: check}
}

func rangeRefused(name string, in *openapi.Input, field, key string, defect func(*openapi.Operation) error) rangeCall {
	return rangeCall{name: name, in: in, field: field, key: key, defect: defect}
}

// bodyIs checks that the body sent is want.
func bodyIs(want string) func(*testing.T, string, []byte) {
	return func(t *testing.T, _ string, body []byte) {
		t.Helper()
		if got := string(trimNL(body)); got != want {
			t.Errorf("body %q; want %q", got, want)
		}
	}
}

// partIs checks that the body sent is multipart with one part, name,
// holding want.
func partIs(name, want string) func(*testing.T, string, []byte) {
	return func(t *testing.T, ctype string, body []byte) {
		t.Helper()
		_, _, parts := readMultipart(t, ctype, body)
		if len(parts) != 1 {
			t.Fatalf("%d parts; want one part %q holding %q", len(parts), name, want)
		}
		_, disp, _ := mime.ParseMediaType(parts[0].header.Get("Content-Disposition"))
		if disp["name"] != name || string(parts[0].body) != want {
			t.Errorf("part %q holding %q; want %q holding %q", disp["name"], parts[0].body, name, want)
		}
	}
}

// encodingErr returns the Err of the field name of the request body's i-th
// Media.
func encodingErr(i int, name string) func(*openapi.Operation) error {
	return func(op *openapi.Operation) error {
		if op.Body == nil || i >= len(op.Body.Media) {
			return nil
		}
		if p := paramByName(op.Body.Media[i].Encoding, name); p != nil {
			return p.Err
		}
		return nil
	}
}

// A rangeOp is an operation and its calls.
type rangeOp struct {
	name, doc, key string
	types          []string // the Media types the operation describes, in order
	calls          []rangeCall
}

// rangeObject is an object schema with the properties a and f.
const rangeObject = `"schema":{"type":"object","properties":{"a":{"type":"string"},"f":{"type":"string"}}}`

func rangeOps() []rangeOp {
	obj := func() map[string]any { return map[string]any{"a": "x"} }
	in := func(body any, typ string) *openapi.Input { return &openapi.Input{Body: body, MediaType: typ} }
	ops := []rangeOp{{
		name: "2.0 body under */*",
		doc: editionDoc("2.0", `"/x":{"post":{"operationId":"x","consumes":["*/*"],"parameters":[
			{"name":"body","in":"body","required":true,"schema":{"type":"object","properties":{"a":{"type":"string"}}}}],`+swaggerResponse+`}}`),
		key: "x", types: []string{"*/*"},
		calls: []rangeCall{
			rangeRefused("no body", nil, "Inputs", "Input.Body", nil),
			rangeRefused("no media type", in(obj(), ""), "Settings", "Input.MediaType", nil),
			rangeSent("JSON", in(obj(), "application/json"), 0, bodyIs(`{"a":"x"}`)),
			rangeSent("form", in(obj(), "application/x-www-form-urlencoded"), 0, bodyIs("a=x")),
			rangeSent("multipart", in(obj(), "multipart/form-data"), 0, partIs("a", "x")),
			rangeSent("pre-encoded", in([]byte("raw"), "application/octet-stream"), 0, bodyIs("raw")),
		},
	}, {
		name: "2.0 formData under multipart/form-data and */*",
		doc: editionDoc("2.0", `"/x":{"post":{"operationId":"x","consumes":["multipart/form-data","*/*"],"parameters":[
			{"name":"a","in":"formData","type":"string"},
			{"name":"tags","in":"formData","type":"array","collectionFormat":"bogus","items":{"type":"string"}}],`+swaggerResponse+`}}`),
		key: "x", types: []string{"multipart/form-data"},
		calls: []rangeCall{
			rangeSent("no body", nil, -1, nil),
			rangeSent("a field", in(obj(), ""), 0, partIs("a", "x")),
			rangeRefused("a defective field", in(map[string]any{"tags": []any{"t"}}, ""), "Inputs", "Input.Body/tags", encodingErr(0, "tags")),
			rangeRefused("an undeclared type", in(obj(), "application/json"), "Settings", "Input.MediaType", nil),
		},
	}}
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		media := map[string]string{
			"*/*":           `"*/*":{` + rangeObject + `,"encoding":{"a":{"contentType":"text/plain"}}}`,
			"multipart/*":   `"multipart/*":{` + rangeObject + `,"encoding":{"f":{"contentType":"not a media type"}}}`,
			"application/*": `"application/*":{` + rangeObject + `,"encoding":{"a":{"contentType":"text/plain"}}}`,
		}
		op := func(types ...string) string {
			var content []string
			for _, typ := range types {
				content = append(content, media[typ])
			}
			return `{"post":{"operationId":"x","requestBody":{"required":true,"content":{` + strings.Join(content, ",") + `}},` + partErrResponse + `}}`
		}
		doc := func(types ...string) string {
			return editionDoc(version, `"/x":`+op(types...), `"servers":[{"url":"`+orderBase+`"}]`)
		}
		ops = append(ops, rangeOp{
			name: version + " */*", doc: doc("*/*"), key: "x", types: []string{"*/*"},
			calls: []rangeCall{
				rangeRefused("no body", nil, "Inputs", "Input.Body", nil),
				rangeRefused("no media type", in(obj(), ""), "Settings", "Input.MediaType", nil),
				rangeSent("JSON", in(obj(), "application/json"), 0, bodyIs(`{"a":"x"}`)),
				rangeSent("form", in(obj(), "application/x-www-form-urlencoded"), 0, bodyIs("a=x")),
				rangeSent("multipart", in(obj(), "multipart/form-data"), 0, partIs("a", "x")),
				rangeSent("pre-encoded", in([]byte("raw"), "application/octet-stream"), 0, bodyIs("raw")),
			},
		}, rangeOp{
			name: version + " multipart/*", doc: doc("multipart/*"), key: "x", types: []string{"multipart/*"},
			calls: []rangeCall{
				rangeRefused("no body", nil, "Inputs", "Input.Body", nil),
				rangeRefused("no media type", in(obj(), ""), "Settings", "Input.MediaType", nil),
				rangeSent("multipart", in(obj(), "multipart/form-data"), 0, partIs("a", "x")),
				rangeRefused("a defective field", in(map[string]any{"f": "x"}, "multipart/form-data"), "Inputs", "Input.Body/f", encodingErr(0, "f")),
				rangeRefused("an undeclared type", in(obj(), "application/json"), "Settings", "Input.MediaType", nil),
			},
		}, rangeOp{
			name: version + " application/*", doc: doc("application/*"), key: "x", types: []string{"application/*"},
			calls: []rangeCall{
				rangeRefused("no body", nil, "Inputs", "Input.Body", nil),
				rangeSent("JSON", in(obj(), "application/json"), 0, bodyIs(`{"a":"x"}`)),
				rangeSent("form", in(obj(), "application/x-www-form-urlencoded"), 0, bodyIs("a=x")),
				rangeRefused("an undeclared type", in(obj(), "multipart/form-data"), "Settings", "Input.MediaType", nil),
			},
		}, rangeOp{
			name: version + " all three", doc: doc("*/*", "multipart/*", "application/*"), key: "x", types: []string{"*/*", "multipart/*", "application/*"},
			calls: []rangeCall{
				rangeRefused("no body", nil, "Inputs", "Input.Body", nil),
				rangeSent("JSON", in(obj(), "application/json"), 2, bodyIs(`{"a":"x"}`)),
				rangeSent("form", in(obj(), "application/x-www-form-urlencoded"), 2, bodyIs("a=x")),
				rangeSent("multipart", in(obj(), "multipart/form-data"), 1, partIs("a", "x")),
				rangeRefused("a defective field", in(map[string]any{"f": "x"}, "multipart/form-data"), "Inputs", "Input.Body/f", encodingErr(1, "f")),
				rangeSent("text", in("hello", "text/plain"), 0, bodyIs("hello")),
			},
		})
	}
	return ops
}

// prepareSafely prepares key with in, turning a panic into a value.
func prepareSafely(c *openapi.Client, key string, in *openapi.Input) (req *openapi.Request, err error, panicked any) {
	defer func() { panicked = recover() }()
	req, err = c.Prepare(key, in)
	return req, err, nil
}

// rangeOutcome describes what a call did, comparable across Clients: its
// refusal's text, or the Content-Type, body and Media index sent, a
// multipart boundary written as BOUNDARY.
func rangeOutcome(t *testing.T, desc *openapi.Operation, cl rangeCall, req *openapi.Request, err error) string {
	t.Helper()
	if err != nil {
		return "refused: " + err.Error()
	}
	ctype := req.HTTP.Header.Get("Content-Type")
	body := editionBody(t, req)
	if cl.check != nil {
		cl.check(t, ctype, body)
	}
	if _, params, err := mime.ParseMediaType(ctype); err == nil && params["boundary"] != "" {
		ctype = strings.ReplaceAll(ctype, params["boundary"], "BOUNDARY")
		body = []byte(strings.ReplaceAll(string(body), params["boundary"], "BOUNDARY"))
	}
	media := -1
	if desc.Body != nil {
		for i, m := range desc.Body.Media {
			if m == req.Media {
				media = i
			}
		}
	}
	return fmt.Sprintf("sent %q %q, Media %d", ctype, body, media)
}

// Regression check, not contract: a refusal's text is the same whether or not
// the operation was described first. What the documentation promises stays
// asserted too: the refusal's key, and its wrapping of the described Err
// (describe.go, Operation.Err: a part's defect "fails a call only when the
// call uses it, the *RequestError then wrapping that part's Err, whether or
// not the operation was described first"); and the Content-Type, body and
// Media index a sent call gets, the same in either order, since a Client
// "never changes once made" (client.go, Client).
func TestMediaRangeDescribeOrder(t *testing.T) {
	for _, op := range rangeOps() {
		t.Run(op.name, func(t *testing.T) {
			outcomes := map[string][]string{}
			orders := []string{"described first", "called first"}
			for _, order := range orders {
				t.Run(order, func(t *testing.T) {
					c, _ := recordingClient(t, op.doc, &openapi.Loader{})
					var desc *openapi.Operation
					if order == "described first" {
						desc = findOp(t, c.Operations(), op.key)
					}
					type result struct {
						req      *openapi.Request
						err      error
						panicked any
					}
					results := make([]result, len(op.calls))
					for i, cl := range op.calls {
						req, err, panicked := prepareSafely(c, op.key, cl.in)
						results[i] = result{req, err, panicked}
					}
					if desc == nil {
						desc = findOp(t, c.Operations(), op.key)
					}
					var types []string
					if desc.Body != nil {
						for _, m := range desc.Body.Media {
							types = append(types, m.Type)
						}
					}
					if strings.Join(types, " ") != strings.Join(op.types, " ") {
						t.Fatalf("Body.Media types %q; want %q", types, op.types)
					}
					for i, cl := range op.calls {
						r := results[i]
						if r.panicked != nil {
							t.Errorf("%s: Prepare panicked: %v", cl.name, r.panicked)
							outcomes[order] = append(outcomes[order], "panic")
							continue
						}
						outcomes[order] = append(outcomes[order], rangeOutcome(t, desc, cl, r.req, r.err))
						if cl.field == "" {
							if r.err != nil {
								t.Errorf("%s: refused with %v; want it sent", cl.name, r.err)
								continue
							}
							var want *openapi.Media
							if cl.media >= 0 {
								want = desc.Body.Media[cl.media]
							}
							if r.req.Media != want {
								t.Errorf("%s: Request.Media = %p; want Operation.Body.Media[%d] %p", cl.name, r.req.Media, cl.media, want)
							}
							continue
						}
						if r.err == nil {
							t.Errorf("%s: sent; want it refused at %s[%q]", cl.name, cl.field, cl.key)
							continue
						}
						re := asRequestError(t, r.err)
						held := re.Inputs[cl.key]
						if cl.field == "Settings" {
							held = re.Settings[cl.key]
						}
						if held == nil {
							t.Errorf("%s: refused with %v; want an entry at %s[%q]", cl.name, r.err, cl.field, cl.key)
						}
						if cl.defect != nil {
							want := cl.defect(desc)
							if want == nil {
								t.Errorf("%s: the descriptor reports no defect", cl.name)
							} else if !errors.Is(r.err, want) || held != nil && !errors.Is(held, want) {
								t.Errorf("%s: %v does not wrap the described Err %v", cl.name, r.err, want)
							}
						}
					}
				})
			}
			a, b := outcomes[orders[0]], outcomes[orders[1]]
			for i := range min(len(a), len(b)) {
				if a[i] != b[i] {
					t.Errorf("%s: %s %s; %s %s", op.calls[i].name, orders[0], a[i], orders[1], b[i])
				}
			}
		})
	}
}
