package openapi_test

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// A $ref where the edition defines no Reference Object. describe.go,
// Operation: "A value written as a reference, an object whose $ref member is a
// string, where the edition defines no Reference Object, such as an Operation
// Object, a headers or encoding map, or a parameters or servers list written
// as a reference to another file, marks a document meant to be bundled before
// use. It makes the nearest part holding that value unusable: the Err of that
// Operation, Param, Message, Media, Server or SecurityScheme says the document
// must be bundled first, and does not wrap ErrUnresolved." "Extension values
// and examples are not such values." Operation.Err: "Calling an operation with
// Err set returns a *RequestError wrapping Err. A defect in an optional part
// is reported on that part instead, and fails a call only when the call uses
// it".
//
// Where each edition defines a Reference Object, from its field tables
// (a Path Item's own $ref field aside, which every edition defines):
//
//   - Swagger 2.0: the entries of a Responses Object, Parameter Objects,
//     and Schema Objects. An Operation Object, a Responses Object, a
//     Headers Object, a Header Object, an Items Object and a Security
//     Scheme Object in securityDefinitions are not references (sections
//     6.4.7, 6.4.11, 6.4.12, 6.4.13, 6.4.15, 6.4.10, 6.4.23).
//   - OpenAPI 3.0.4 and 3.1.2: Parameter, Request Body, Response, Header,
//     Example, Link, Callback and Security Scheme Objects (and Schema
//     Objects); a content map's values are Media Type Objects, never
//     references (Map[string, Media Type Object]); an Operation Object, a
//     Responses Object, an Encoding Object, a Server Object and an OAuth
//     Flows Object are not references, nor is any map itself.
//   - OpenAPI 3.2.1: as 3.1.2, and a content map's values are "Media Type
//     Object | Reference Object" (Parameter Object section 4.12.2.3,
//     Request Body Object 4.13.1, Response Object 4.17.1, Header Object
//     4.21.1); encoding, prefixEncoding and itemEncoding remain Encoding
//     Objects (Media Type Object 4.14.1).
//
// The diagnostic is about the document's shape, so it must not depend on
// whether the referenced file loads: each document is loaded once with every
// referenced file served and once with none, and the two must report the
// same Err text.

const bundleEntry = "https://parts.example.test/openapi.json"

const bundleDir = "https://parts.example.test/"

// bundleFiles serves, for each file the documents reference, content that
// would be a plausible target for the object written as a reference, so
// following it would succeed.
func bundleFiles(version string) map[string]string {
	if version == "2.0" {
		return map[string]string{
			bundleDir + "other.yml":     `{"operationId":"remote","responses":{"200":{"description":"ok"}}}`,
			bundleDir + "responses.yml": `{"200":{"description":"ok"}}`,
			bundleDir + "headers.yml":   `{"X-Rate":{"type":"integer"}}`,
			bundleDir + "header.yml":    `{"type":"integer"}`,
			bundleDir + "items.yml":     `{"type":"string"}`,
			bundleDir + "scheme.yml":    `{"type":"apiKey","in":"header","name":"X-Key"}`,
		}
	}
	return map[string]string{
		bundleDir + "other.yml":     `{"operationId":"remote","responses":{"200":{"description":"ok"}}}`,
		bundleDir + "responses.yml": `{"200":{"description":"ok"}}`,
		bundleDir + "headers.yml":   `{"X-Rate":{"schema":{"type":"integer"}}}`,
		bundleDir + "content.yml":   `{"application/json":{"schema":{"type":"object"}}}`,
		bundleDir + "media.yml":     `{"schema":{"type":"object"}}`,
		bundleDir + "encoding.yml":  `{"contentType":"text/plain"}`,
		bundleDir + "server.yml":    `{"url":"https://elsewhere.example.test"}`,
		bundleDir + "flows.yml":     `{"clientCredentials":{"tokenUrl":"https://auth.example.test/token","scopes":{}}}`,
	}
}

// bundleDoc is a document with one operation per object written as a
// reference where the edition defines none, beside parts that are not.
func bundleDoc(version string) string {
	if version == "2.0" {
		return editionDoc(version, `
			"/op":{"get":{"$ref":"other.yml"},"put":{"operationId":"sibling","responses":{"200":{"description":"ok"}}}},
			"/responses":{"get":{"operationId":"responsesRef","responses":{"$ref":"responses.yml"}}},
			"/respHeaders":{"get":{"operationId":"respHeaders","responses":{"200":{"description":"ok","headers":{"$ref":"headers.yml"}},"404":{"description":"plain"}}}},
			"/header":{"get":{"operationId":"headerRef","responses":{"200":{"description":"ok","headers":{"X-Rate":{"$ref":"header.yml"},"X-Ok":{"type":"string"}}}}}},
			"/items":{"get":{"operationId":"itemsRef","parameters":[{"name":"ids","in":"query","type":"array","items":{"$ref":"items.yml"}},{"name":"r","in":"query","type":"string"}],"responses":{"200":{"description":"ok"}}}},
			"/scheme":{"get":{"operationId":"schemeRef","security":[{"key":[]}],"responses":{"200":{"description":"ok"}}}}`,
			`"host":"@HOSTPORT@"`, `"schemes":["http"]`,
			`"securityDefinitions":{"key":{"$ref":"scheme.yml"},"ok":{"type":"apiKey","in":"header","name":"X-Ok"}}`)
	}
	positional := ""
	if version == "3.2.1" {
		positional = `,
			"/positional":{"post":{"operationId":"positionalRef","requestBody":{"content":{"multipart/mixed":{
				"prefixEncoding":[{"$ref":"encoding.yml"},{"contentType":"text/plain"}],"itemEncoding":{"$ref":"encoding.yml"}}}},"responses":{"200":{"description":"ok"}}}}`
	}
	return editionDoc(version, `
		"/op":{"get":{"$ref":"other.yml"},"put":{"operationId":"sibling","responses":{"200":{"description":"ok"}}}},
		"/responses":{"get":{"operationId":"responsesRef","responses":{"$ref":"responses.yml"}}},
		"/respHeaders":{"get":{"operationId":"respHeaders","responses":{"200":{"description":"ok","headers":{"$ref":"headers.yml"}},"404":{"description":"plain"}}}},
		"/respContent":{"get":{"operationId":"respContent","responses":{"200":{"description":"ok","content":{"$ref":"content.yml"}},"404":{"description":"plain"}}}},
		"/headerContent":{"get":{"operationId":"headerContent","responses":{"200":{"description":"ok","headers":{"X-H":{"content":{"$ref":"content.yml"}},"X-Ok":{"schema":{"type":"string"}}}}}}},
		"/bodyContent":{"post":{"operationId":"bodyContent","requestBody":{"content":{"$ref":"content.yml"}},"responses":{"200":{"description":"ok"}}}},
		"/paramContent":{"get":{"operationId":"paramContent","parameters":[{"name":"q","in":"query","content":{"$ref":"content.yml"}},{"name":"r","in":"query","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}},
		"/media":{"post":{"operationId":"mediaRef","requestBody":{"content":{"application/json":{"$ref":"media.yml"}}},"responses":{"200":{"description":"ok","content":{"application/json":{"$ref":"media.yml"}}}}}},
		"/encoding":{"post":{"operationId":"encodingRef","requestBody":{"content":{"application/x-www-form-urlencoded":{
			"schema":{"type":"object","properties":{"file":{"type":"string"},"note":{"type":"string"}}},"encoding":{"file":{"$ref":"encoding.yml"}}}}},"responses":{"200":{"description":"ok"}}}},
		"/encodingHeaders":{"post":{"operationId":"encodingHeaders","requestBody":{"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"file":{"type":"string"},"note":{"type":"string"}}},"encoding":{"file":{"headers":{"$ref":"headers.yml"}}}}}},"responses":{"200":{"description":"ok"}}}},
		"/servers":{"get":{"operationId":"serverRef","servers":[{"url":"@BASE@"},{"$ref":"server.yml"}],"responses":{"200":{"description":"ok"}}}},
		"/flows":{"get":{"operationId":"flowsRef","security":[{"oauth":[]}],"responses":{"200":{"description":"ok"}}}}`+positional,
		`"servers":[{"url":"@BASE@"}]`,
		`"components":{"securitySchemes":{"oauth":{"type":"oauth2","flows":{"$ref":"flows.yml"}}}}`)
}

// bundleLoad loads doc, expanded against base, as bundleEntry, with files
// served by Loader.Fetch.
//
// Calls go only to a loopback host: a request for any other host fails in
// the transport, so a server read from a reference's target is never
// contacted.
func bundleLoad(t *testing.T, doc, base string, files map[string]string) *openapi.Client {
	t.Helper()
	c, _ := bundleLoadFetch(t, doc, base, files)
	return c
}

// bundleLoadFetch is bundleLoad, also returning the Fetch, which records
// every URI the loader asks for.
func bundleLoadFetch(t *testing.T, doc, base string, files map[string]string) (*openapi.Client, *memFetch) {
	t.Helper()
	m := newMemFetch(files)
	l := &openapi.Loader{Fetch: m.fetch}
	loopbackOnly := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() != "127.0.0.1" {
			return nil, fmt.Errorf("test transport: %s is not the test server", r.URL.Host)
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	c, err := l.Parse(t.Context(), []byte(expand(doc, base)), bundleEntry, &openapi.Options{HTTPClient: loopbackOnly})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c, m
}

// A part locates the descriptor that must report the defect, and the
// descriptor holding it, which must not.
type bundlePart struct {
	name string // the case, and the subtest name
	key  string // the operation
	// at returns the Err of the part that must say the document must be
	// bundled, and the Errs of parts that must stay usable, by name. ok
	// is false when the part is missing.
	at func(op *openapi.Operation) (err error, usable map[string]error, ok bool)
}

func respByKey(op *openapi.Operation, key string) *openapi.Message {
	for _, m := range op.Responses {
		if m.Key == key {
			return m
		}
	}
	return nil
}

func paramByName(ps []*openapi.Param, name string) *openapi.Param {
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func bundleParts(version string) []bundlePart {
	opPart := func(name, key string) bundlePart {
		return bundlePart{name, key, func(op *openapi.Operation) (error, map[string]error, bool) {
			return op.Err, nil, true
		}}
	}
	responseHeaders := bundlePart{"response headers map", "respHeaders", func(op *openapi.Operation) (error, map[string]error, bool) {
		m, other := respByKey(op, "200"), respByKey(op, "404")
		if m == nil || other == nil {
			return nil, nil, false
		}
		return m.Err, map[string]error{"Operation": op.Err, "response 404": other.Err}, true
	}}
	parts := []bundlePart{
		// The nearest part holding an Operation Object is that Operation.
		opPart("Operation Object", "GET /op"),
		// A Responses Object is held by its Operation, the nearest part.
		opPart("Responses Object", "responsesRef"),
		responseHeaders,
	}
	if version == "2.0" {
		return append(parts,
			bundlePart{"Header Object", "headerRef", func(op *openapi.Operation) (error, map[string]error, bool) {
				m := respByKey(op, "200")
				if m == nil {
					return nil, nil, false
				}
				h, ok := paramByName(m.Headers, "X-Rate"), paramByName(m.Headers, "X-Ok")
				if h == nil || ok == nil {
					return nil, nil, false
				}
				return h.Err, map[string]error{"Operation": op.Err, "response 200": m.Err, "header X-Ok": ok.Err}, true
			}},
			bundlePart{"Items Object", "itemsRef", func(op *openapi.Operation) (error, map[string]error, bool) {
				p, r := paramByName(op.Params, "ids"), paramByName(op.Params, "r")
				if p == nil || r == nil {
					return nil, nil, false
				}
				return p.Err, map[string]error{"Operation": op.Err, "param r": r.Err}, true
			}},
			bundlePart{"Security Scheme Object in securityDefinitions", "schemeRef", func(op *openapi.Operation) (error, map[string]error, bool) {
				if len(op.Security) != 1 || len(op.Security[0].Schemes) != 1 {
					return nil, nil, false
				}
				return op.Security[0].Schemes[0].Err, map[string]error{"Operation": op.Err}, true
			}},
		)
	}
	parts = append(parts,
		bundlePart{"response content map", "respContent", func(op *openapi.Operation) (error, map[string]error, bool) {
			m, other := respByKey(op, "200"), respByKey(op, "404")
			if m == nil || other == nil {
				return nil, nil, false
			}
			return m.Err, map[string]error{"Operation": op.Err, "response 404": other.Err}, true
		}},
		bundlePart{"Header Object content map", "headerContent", func(op *openapi.Operation) (error, map[string]error, bool) {
			m := respByKey(op, "200")
			if m == nil {
				return nil, nil, false
			}
			h, ok := paramByName(m.Headers, "X-H"), paramByName(m.Headers, "X-Ok")
			if h == nil || ok == nil {
				return nil, nil, false
			}
			return h.Err, map[string]error{"Operation": op.Err, "response 200": m.Err, "header X-Ok": ok.Err}, true
		}},
		bundlePart{"request body content map", "bodyContent", func(op *openapi.Operation) (error, map[string]error, bool) {
			if op.Body == nil {
				return nil, nil, false
			}
			return op.Body.Err, map[string]error{"Operation": op.Err}, true
		}},
		bundlePart{"Parameter content map", "paramContent", func(op *openapi.Operation) (error, map[string]error, bool) {
			q, r := paramByName(op.Params, "q"), paramByName(op.Params, "r")
			if q == nil || r == nil {
				return nil, nil, false
			}
			return q.Err, map[string]error{"Operation": op.Err, "param r": r.Err}, true
		}},
		// describe.go, Media.Err: "Any other schema or Encoding defect that
		// affects structured value encoding does not set Media.Err: it is
		// reported by Schema.References or the relevant Encoding
		// Param.Err."
		bundlePart{"Encoding Object", "encodingRef", func(op *openapi.Operation) (error, map[string]error, bool) {
			if op.Body == nil || len(op.Body.Media) != 1 {
				return nil, nil, false
			}
			md := op.Body.Media[0]
			f, n := paramByName(md.Encoding, "file"), paramByName(md.Encoding, "note")
			if f == nil || n == nil {
				return nil, nil, false
			}
			return f.Err, map[string]error{"Operation": op.Err, "body": op.Body.Err, "media": md.Err, "field note": n.Err}, true
		}},
		bundlePart{"Encoding Object headers map", "encodingHeaders", func(op *openapi.Operation) (error, map[string]error, bool) {
			if op.Body == nil || len(op.Body.Media) != 1 {
				return nil, nil, false
			}
			md := op.Body.Media[0]
			f, n := paramByName(md.Encoding, "file"), paramByName(md.Encoding, "note")
			if f == nil || n == nil {
				return nil, nil, false
			}
			return f.Err, map[string]error{"Operation": op.Err, "body": op.Body.Err, "media": md.Err, "field note": n.Err}, true
		}},
		bundlePart{"Server Object", "serverRef", func(op *openapi.Operation) (error, map[string]error, bool) {
			if len(op.Servers) != 2 {
				return nil, nil, false
			}
			return op.Servers[1].Err, map[string]error{"Operation": op.Err, "server 0": op.Servers[0].Err}, true
		}},
		bundlePart{"OAuth Flows Object", "flowsRef", func(op *openapi.Operation) (error, map[string]error, bool) {
			if len(op.Security) != 1 || len(op.Security[0].Schemes) != 1 {
				return nil, nil, false
			}
			return op.Security[0].Schemes[0].Err, map[string]error{"Operation": op.Err}, true
		}},
	)
	if version != "3.2.1" {
		// OpenAPI 3.0 and 3.1 define no Reference Object for a Media Type
		// Object: the Media reports it, request and response alike.
		parts = append(parts,
			bundlePart{"request Media Type Object", "mediaRef", func(op *openapi.Operation) (error, map[string]error, bool) {
				if op.Body == nil || len(op.Body.Media) != 1 {
					return nil, nil, false
				}
				return op.Body.Media[0].Err, map[string]error{"Operation": op.Err, "body": op.Body.Err}, true
			}},
			bundlePart{"response Media Type Object", "mediaRef", func(op *openapi.Operation) (error, map[string]error, bool) {
				m := respByKey(op, "200")
				if m == nil || len(m.Media) != 1 {
					return nil, nil, false
				}
				return m.Media[0].Err, map[string]error{"Operation": op.Err, "response 200": m.Err}, true
			}},
		)
	} else {
		// prefixEncoding and itemEncoding hold Encoding Objects, never
		// references (OAS 3.2.1 section 4.14.1).
		positional := func(name, part string) bundlePart {
			return bundlePart{name, "positionalRef", func(op *openapi.Operation) (error, map[string]error, bool) {
				if op.Body == nil || len(op.Body.Media) != 1 {
					return nil, nil, false
				}
				md := op.Body.Media[0]
				p, second := paramByName(md.Encoding, part), paramByName(md.Encoding, "1")
				if p == nil || second == nil {
					return nil, nil, false
				}
				return p.Err, map[string]error{"Operation": op.Err, "body": op.Body.Err, "media": md.Err, "part 1": second.Err}, true
			}}
		}
		parts = append(parts, positional("prefixEncoding item", "0"), positional("itemEncoding", "*"))
	}
	return parts
}

// mentionsBundling reports whether err says the document must be bundled.
func mentionsBundling(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "bundle")
}

// Each part's Err says the document must be bundled, does not wrap
// ErrUnresolved (describe.go, Operation: "says the document must be bundled
// first, and does not wrap ErrUnresolved"), has the same text whether or
// not the referenced file loads, and the parts holding it stay usable.
func TestBundleDiagnostics(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			doc := bundleDoc(version)
			present := bundleLoad(t, doc, "https://api.example.test", bundleFiles(version))
			absent := bundleLoad(t, doc, "https://api.example.test", map[string]string{})
			for _, part := range bundleParts(version) {
				t.Run(part.name, func(t *testing.T) {
					var texts [2]string
					for i, c := range []*openapi.Client{present, absent} {
						variant := [2]string{"files served", "files missing"}[i]
						op := mustOp(t, c, part.key)
						err, usable, ok := part.at(op)
						if !ok {
							t.Fatalf("%s: %s does not describe the part", variant, part.key)
						}
						if !mentionsBundling(err) {
							t.Errorf("%s: Err = %v; want it to say the document must be bundled first", variant, err)
						}
						for name, e := range usable {
							if e != nil {
								t.Errorf("%s: %s Err = %v; want only the nearest part unusable", variant, name, e)
							}
						}
						if errors.Is(err, openapi.ErrUnresolved) {
							t.Errorf("%s: Err %v wraps ErrUnresolved", variant, err)
						}
						if err != nil {
							texts[i] = err.Error()
						}
					}
					if texts[0] != texts[1] {
						t.Errorf("the diagnostic depends on whether the file loads:\n served  %q\n missing %q", texts[0], texts[1])
					}
				})
			}
		})
	}
}

// An Operation unusable for its own shape, the Operation Object or its
// Responses Object written as a reference, refuses every call with a
// *RequestError wrapping its Err, sending nothing; the Path Item's other
// operation is called as usual.
func TestBundleOperationRefused(t *testing.T) {
	w := newWire(t, nil)
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			for _, files := range []map[string]string{bundleFiles(version), {}} {
				c := bundleLoad(t, bundleDoc(version), w.URL, files)
				for _, key := range []string{"GET /op", "responsesRef"} {
					op := mustOp(t, c, key)
					if op.Err == nil {
						t.Errorf("%s: Err = nil", key)
						continue
					}
					before := w.count()
					resp, err := c.Call(t.Context(), key, nil, nil)
					re := refusedSince(t, w, before, resp, err)
					if !errors.Is(re, op.Err) {
						t.Errorf("%s: error %v does not wrap the operation's Err", key, err)
					}
					if _, err := c.Prepare(key, nil); !errors.Is(err, op.Err) {
						t.Errorf("%s: Prepare error %v does not wrap the operation's Err", key, err)
					}
				}
				mustCall(t, c, "sibling", nil, nil)
				if got := w.last(t); got.Method != "PUT" || got.RequestURI != "/op" {
					t.Errorf("sibling: sent %s %s", got.Method, got.RequestURI)
				}
			}
		})
	}
}

// A part unusable for its shape fails a call only when the call uses it
// (describe.go, Operation.Err: "A defect in an optional part is reported on
// that part instead, and fails a call only when the call uses it"): a
// response's or a header's defect does not stop the request; a parameter,
// body, field, server or scheme stops only the calls that need it.
func TestBundlePartsFailOnlyWhenUsed(t *testing.T) {
	w := newWire(t, nil)
	type call struct {
		name    string
		key     string
		in      *openapi.Input
		with    func(*openapi.Options)
		refused bool
	}
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			for vi, files := range []map[string]string{bundleFiles(version), {}} {
				variant := [2]string{"files served", "files missing"}[vi]
				c := bundleLoad(t, bundleDoc(version), w.URL, files)
				calls := []call{
					{"response headers map: the request is built", "respHeaders", nil, nil, false},
				}
				if version == "2.0" {
					calls = append(calls,
						call{"Header Object: the request is built", "headerRef", nil, nil, false},
						call{"Items Object: a call without the parameter", "itemsRef", &openapi.Input{Params: map[string]any{"r": "x"}}, nil, false},
						call{"Items Object: a call with the parameter", "itemsRef", &openapi.Input{Params: map[string]any{"ids": []string{"a"}}}, nil, true},
						// describe.go, SecurityScheme.Err: "Alternatives that
						// use it can be applied only when FromTransport
						// satisfies it."
						call{"Security Scheme Object: a secret", "schemeRef", nil, func(o *openapi.Options) {
							o.Credentials = map[string]openapi.Credential{"key": openapi.Secret("k")}
						}, true},
						call{"Security Scheme Object: FromTransport", "schemeRef", nil, func(o *openapi.Options) {
							o.Credentials = map[string]openapi.Credential{"key": openapi.FromTransport()}
						}, false},
					)
				} else {
					calls = append(calls,
						call{"response content map: the request is built", "respContent", nil, nil, false},
						call{"Header Object content map: the request is built", "headerContent", nil, nil, false},
						call{"request body content map: no body", "bodyContent", nil, nil, false},
						call{"request body content map: a body", "bodyContent", &openapi.Input{Body: []byte(`{}`), MediaType: "application/json"}, nil, true},
						call{"Parameter content map: without the parameter", "paramContent", &openapi.Input{Params: map[string]any{"r": "x"}}, nil, false},
						call{"Parameter content map: with the parameter", "paramContent", &openapi.Input{Params: map[string]any{"q": map[string]any{"a": 1}}}, nil, true},
						call{"Encoding Object: another field", "encodingRef", &openapi.Input{Body: map[string]any{"note": "x"}}, nil, false},
						call{"Encoding Object: its field", "encodingRef", &openapi.Input{Body: map[string]any{"file": "x"}}, nil, true},
						// client.go, Input.Body: a pre-encoded body is not
						// checked against "a field's Param.Err".
						call{"Encoding Object: a pre-encoded body", "encodingRef", &openapi.Input{Body: []byte("file=x")}, nil, false},
						call{"Encoding Object headers map: another field", "encodingHeaders", &openapi.Input{Body: map[string]any{"note": "x"}}, nil, false},
						call{"Encoding Object headers map: its field", "encodingHeaders", &openapi.Input{Body: map[string]any{"file": "x"}}, nil, true},
						// doc.go, Configuration: "One usable server selects
						// itself."
						call{"Server Object: the usable server", "serverRef", nil, nil, false},
						call{"OAuth Flows Object: a secret", "flowsRef", nil, func(o *openapi.Options) {
							o.Credentials = map[string]openapi.Credential{"oauth": openapi.Secret("tok")}
						}, true},
						call{"OAuth Flows Object: FromTransport", "flowsRef", nil, func(o *openapi.Options) {
							o.Credentials = map[string]openapi.Credential{"oauth": openapi.FromTransport()}
						}, false},
					)
					if version != "3.2.1" {
						// describe.go, Media.Err: "A pre-encoded []byte or
						// io.Reader body is checked against neither".
						calls = append(calls,
							call{"Media Type Object: a structured body", "mediaRef", &openapi.Input{Body: map[string]any{"a": 1}}, nil, true},
							call{"Media Type Object: a pre-encoded body", "mediaRef", &openapi.Input{Body: []byte(`{"a":1}`)}, nil, false},
						)
					}
				}
				for _, cl := range calls {
					t.Run(variant+"/"+cl.name, func(t *testing.T) {
						cc := c
						if cl.with != nil {
							cc = c.With(cl.with)
						}
						before := w.count()
						if cl.refused {
							resp, err := cc.Call(t.Context(), cl.key, cl.in, nil)
							refusedSince(t, w, before, resp, err)
							return
						}
						// Send builds and sends the request without
						// classifying or decoding the response.
						req, err := cc.Prepare(cl.key, cl.in)
						if err != nil {
							t.Fatalf("Prepare: %v", err)
						}
						sendAndClose(t, req)
						if w.count() != before+1 {
							t.Fatalf("server received %d requests, want 1", w.count()-before)
						}
					})
				}
				if version == "2.0" {
					continue
				}
				// A server unusable for its shape cannot be selected.
				t.Run(variant+"/Server Object: selecting it", func(t *testing.T) {
					op := mustOp(t, c, "serverRef")
					if len(op.Servers) != 2 || op.Servers[1].ID == "" {
						t.Fatalf("serverRef servers %+v", op.Servers)
					}
					before := w.count()
					resp, err := c.With(func(o *openapi.Options) { o.ServerID = op.Servers[1].ID }).Call(t.Context(), "serverRef", nil, nil)
					refusedSince(t, w, before, resp, err)
				})
			}
		})
	}
}

// The loader follows only the references its documentation lists (load.go,
// Loader: "The references followed are $ref in Reference Objects, Path
// Items and Schema Objects, $dynamicRef, the values of a Discriminator
// mapping written as an object and a defaultMapping value, ..., and OpenAPI
// 3.2 security requirement URIs"),
// so a $ref where the edition defines no Reference Object is never
// retrieved: none of the files the bundling cases name is asked of Fetch,
// whether or not it exists. In OpenAPI 3.2 a Media Type Object written as a
// reference is a Reference Object, and its file is retrieved.
func TestBundleReferencesNotFetched(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			for vi, files := range []map[string]string{bundleFiles(version), {}} {
				variant := [2]string{"files served", "files missing"}[vi]
				c, m := bundleLoadFetch(t, bundleDoc(version), "https://api.example.test", files)
				c.Operations()
				for uri := range bundleFiles(version) {
					if version == "3.2.1" && uri == bundleDir+"media.yml" {
						if m.callsTo(uri) == 0 {
							t.Errorf("%s: the 3.2 Media Type reference %s was not retrieved", variant, uri)
						}
						continue
					}
					if n := m.callsTo(uri); n != 0 {
						t.Errorf("%s: Fetch was asked for %s %d times; want never", variant, uri, n)
					}
				}
			}
		})
	}
}

// OpenAPI 3.2 defines a Reference Object for a Media Type Object, so one
// written as a reference is followed like any other: with its file served,
// its Media is usable and a structured body is encoded by its schema; with
// its file missing, its Err is an unresolved reference (errors.go,
// ErrUnresolved), not a bundling diagnostic.
func TestBundleMediaTypeReference32(t *testing.T) {
	w := newWire(t, nil)
	doc := bundleDoc("3.2.1")
	c := bundleLoad(t, doc, w.URL, bundleFiles("3.2.1"))
	op := mustOp(t, c, "mediaRef")
	req, resp := reqMedia(t, op, 0), responseMedia(t, op, 0, 0)
	for name, m := range map[string]*openapi.Media{"request": req, "response": resp} {
		if m.Err != nil || m.Schema == nil || !strings.HasPrefix(m.Source, bundleDir+"media.yml") {
			t.Errorf("%s Media: Err %v, Schema %v, Source %q; want the referenced Media Type Object", name, m.Err, m.Schema, m.Source)
		}
	}
	mustCall(t, c, "mediaRef", &openapi.Input{Body: map[string]any{"a": 1}}, nil)
	if got := string(trimNL(w.last(t).Body)); got != `{"a":1}` {
		t.Errorf("body %q", got)
	}

	missing := bundleLoad(t, doc, w.URL, map[string]string{})
	op = mustOp(t, missing, "mediaRef")
	for name, m := range map[string]*openapi.Media{"request": reqMedia(t, op, 0), "response": responseMedia(t, op, 0, 0)} {
		if !errors.Is(m.Err, openapi.ErrUnresolved) || mentionsBundling(m.Err) {
			t.Errorf("%s Media with its file missing: Err %v; want an unresolved reference", name, m.Err)
		}
	}
}

// allowedFiles serves the files the allowed references reach. nowhere.yml,
// named only inside extension values and examples, is never served.
func allowedFiles(version string) map[string]string {
	files := map[string]string{
		bundleDir + "pathitem.yml": `{"get":{"operationId":"remoteGet","responses":{"200":{"description":"ok"}}}}`,
		bundleDir + "schemas.yml":  `{"S":{"type":"string"}}`,
	}
	if version == "2.0" {
		files[bundleDir+"params.yml"] = `{"q":{"name":"q","in":"query","type":"string"}}`
		return files
	}
	files[bundleDir+"params.yml"] = `{"q":{"name":"q","in":"query","schema":{"type":"string"}}}`
	files[bundleDir+"schemes.yml"] = `{"key":{"type":"apiKey","in":"header","name":"X-Key"}}`
	files[bundleDir+"media.yml"] = `{"schema":{"type":"object"}}`
	return files
}

// allowedDoc uses every reference the edition defines, and $ref members
// inside extension values and examples, which are data.
func allowedDoc(version string) string {
	if version == "2.0" {
		return editionDoc(version, `
			"/pi":{"$ref":"pathitem.yml"},
			"/fine/{id}":{
				"x-ext":{"$ref":"nowhere.yml"},
				"parameters":[{"$ref":"#/parameters/Id"}],
				"post":{
					"operationId":"fine",
					"x-codegen":{"$ref":"nowhere.yml","nested":[{"$ref":"#/nowhere"}]},
					"parameters":[{"$ref":"params.yml#/q"},{"name":"body","in":"body","schema":{"$ref":"#/definitions/S"},"x-example":{"$ref":"nowhere.yml"}}],
					"responses":{
						"200":{"$ref":"#/responses/R"},
						"x-resp":{"$ref":"nowhere.yml"},
						"default":{"description":"d","schema":{"$ref":"schemas.yml#/S"},"examples":{"application/json":{"$ref":"nowhere.yml"}},"headers":{"X-H":{"type":"string","x-h":{"$ref":"nowhere.yml"}}}}}}}`,
			`"host":"api.example.test"`,
			`"parameters":{"Id":{"name":"id","in":"path","required":true,"type":"string"}}`,
			`"responses":{"R":{"description":"r","schema":{"$ref":"#/definitions/S"}}}`,
			`"definitions":{"S":{"type":"object","example":{"$ref":"nowhere.yml"},"properties":{"r":{"$ref":"schemas.yml#/S"}}}}`,
			`"securityDefinitions":{"key":{"type":"apiKey","in":"header","name":"X-Key","x-s":{"$ref":"nowhere.yml"}}}`,
			`"security":[{"key":[]}]`,
			`"x-root":{"$ref":"nowhere.yml"}`)
	}
	media := `"application/json":{"schema":{"$ref":"schemas.yml#/S"},"example":{"$ref":"nowhere.yml"},"examples":{"x":{"value":{"$ref":"nowhere.yml"}},"y":{"$ref":"#/components/examples/E"}}}`
	if version == "3.2.1" {
		media += `,"application/xml":{"$ref":"#/components/mediaTypes/M"},"text/plain":{"$ref":"media.yml"}`
	}
	components := `"components":{
		"parameters":{"Id":{"name":"id","in":"path","required":true,"schema":{"type":"string"}}},
		"requestBodies":{"B":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/S"},"example":{"$ref":"nowhere.yml"}},
			"multipart/form-data":{"schema":{"$ref":"#/components/schemas/S"},"encoding":{"r":{"headers":{"X-P":{"$ref":"#/components/headers/H"}}}}}}}},
		"responses":{"R":{"description":"r","headers":{"X-H":{"$ref":"#/components/headers/H"}},"content":{"application/json":{"schema":{"$ref":"#/components/schemas/S"}}}}},
		"headers":{"H":{"schema":{"type":"string"},"example":{"$ref":"nowhere.yml"}}},
		"schemas":{"S":{"type":"object","properties":{"r":{"$ref":"schemas.yml#/S"}},"x-meta":{"$ref":"nowhere.yml"}}},
		"examples":{"E":{"value":{"$ref":"nowhere.yml"}}},
		"securitySchemes":{"key":{"$ref":"schemes.yml#/key"}}`
	if version == "3.2.1" {
		components += `,"mediaTypes":{"M":{"schema":{"type":"object"}}}`
	}
	components += `}`
	return editionDoc(version, `
		"/pi":{"$ref":"pathitem.yml"},
		"/fine/{id}":{
			"x-ext":{"$ref":"nowhere.yml"},
			"parameters":[{"$ref":"#/components/parameters/Id"}],
			"get":{
				"operationId":"fine",
				"x-codegen":{"$ref":"nowhere.yml","nested":[{"$ref":"#/nowhere"}]},
				"parameters":[
					{"$ref":"params.yml#/q"},
					{"name":"e","in":"query","schema":{"$ref":"#/components/schemas/S"},"example":{"$ref":"nowhere.yml"},
						"examples":{"inline":{"value":{"$ref":"nowhere.yml"}},"byRef":{"$ref":"#/components/examples/E"}}}],
				"responses":{
					"200":{"$ref":"#/components/responses/R"},
					"x-resp":{"$ref":"nowhere.yml"},
					"default":{"description":"d","headers":{"X-H":{"$ref":"#/components/headers/H"}},"content":{`+media+`}}}},
			"put":{"operationId":"finePut","requestBody":{"$ref":"#/components/requestBodies/B"},"responses":{"204":{"description":"ok"}}}}`,
		`"servers":[{"url":"https://api.example.test","x-s":{"$ref":"nowhere.yml"}}]`,
		components,
		`"security":[{"key":[]}]`,
		`"x-root":{"$ref":"nowhere.yml"}`)
}

// What stays allowed: Path Item $ref, Parameter, Request Body, Response and
// Header (3.x) references, Schema references, Example references, Security
// Scheme references in components, Media Type references in 3.2, and $ref
// inside extension values and examples, set no Err on any descriptor.
func TestBundleAllowedReferences(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			c := bundleLoad(t, allowedDoc(version), "https://api.example.test", allowedFiles(version))
			ops := c.Operations()
			keys := map[string]bool{}
			for _, op := range ops {
				keys[op.Key] = true
			}
			want := []string{"remoteGet", "fine"}
			if version != "2.0" {
				want = append(want, "finePut")
			}
			for _, k := range want {
				if !keys[k] {
					t.Errorf("no operation %q; keys %v", k, sortedSet(keys))
				}
			}
			walkOps(ops, func(where string, err error) {
				if err != nil {
					t.Errorf("%s: Err = %v", where, err)
				}
			})
			fine := mustOp(t, c, "fine")
			if q := paramByName(fine.Params, "q"); q == nil || q.Schema == nil {
				t.Errorf("fine: the external parameter reference was not followed: %+v", q)
			}
		})
	}
}

func sortedSet(m map[string]bool) []string {
	return slices.Sorted(maps.Keys(m))
}
