package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Values written as a reference where the edition defines no Reference
// Object. describe.go, Operation: "A value written as a reference, an object
// whose $ref member is a string where an object, list or map belongs but the
// edition defines no Reference Object, such as an Operation Object, a headers
// or encoding map, or a parameters or servers list written as a reference to
// another file, marks a document meant to be bundled before use. It makes the
// nearest part holding that value unusable: the Err of that Operation, Param,
// Message, Media, Server or SecurityScheme says the document must be bundled
// first, a cause that does not wrap ErrUnresolved. A parameters list so
// written makes every operation it applies to unusable, and a servers list so
// written is described as one Server with that Err, which Options.BaseURL can
// replace as it can any unusable server ... Extension values are not such
// values, nor are values that only document the API and in which the edition
// defines no Reference Object, such as info, tags, externalDocs and example
// values. In a map of objects a member named $ref whose value is an
// object is an entry like any other, such as a header named $ref".
//
// Operation.Err lists among its causes, "a value written as a reference where
// the edition defines none (see Operation): the Operation Object, its Responses
// Object, a parameters list that applies to it, or another value whose nearest
// part is the operation", and says "Calling an operation
// with Err set returns a *RequestError wrapping Err. A defect in an optional
// part is reported on that part instead, and fails a call only when the call
// uses it, the *RequestError then wrapping that part's Err, whether or not
// the operation was described first."
//
// Such a value is never retrieved (load.go, Loader: the references followed
// are those in Reference Objects, Path Items and Schema Objects, and the
// others it lists), so every document here is loaded with a Fetch that
// records what it is asked for and must be asked for nothing.
//
// Each case runs described first and called first, on fresh Clients, with
// the checks of TestDescribeOrderSameDescriptors.

// fetchLog fails every retrieval and records the URIs asked for.
type fetchLog struct {
	mu   sync.Mutex
	uris []string
}

func (f *fetchLog) fetch(_ context.Context, u string) (io.ReadCloser, string, error) {
	f.mu.Lock()
	f.uris = append(f.uris, u)
	f.mu.Unlock()
	return nil, "", fmt.Errorf("no document %s", u)
}

func (f *fetchLog) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.uris)
}

// A shapeCase is an orderCase in its own document, with checks of its
// descriptor and, optionally, of further calls.
type shapeCase struct {
	orderCase
	doc   string
	check func(t *testing.T, op *openapi.Operation)
	after func(t *testing.T, c *openapi.Client, op *openapi.Operation)
}

// shapeDoc is editionDoc with orderBase as the root server in OpenAPI 3.x.
func shapeDoc(version, paths string, extra ...string) string {
	if version != "2.0" {
		extra = append(extra, `"servers":[{"url":"`+orderBase+`"}]`)
	}
	return editionDoc(version, paths, extra...)
}

func wantBundled(t *testing.T, what string, err error) {
	t.Helper()
	if !mentionsBundling(err) || errors.Is(err, openapi.ErrUnresolved) {
		t.Errorf("%s: Err = %v; want it to say the document must be bundled first, without ErrUnresolved", what, err)
	}
}

func wantUsable(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: Err = %v; want only the nearest part unusable", what, err)
	}
}

func mediaAt(i int) func(*openapi.Operation) *openapi.Media {
	return func(op *openapi.Operation) *openapi.Media {
		if op.Body == nil || i >= len(op.Body.Media) {
			return nil
		}
		return op.Body.Media[i]
	}
}

func mediaErrAt(i int) func(*openapi.Operation) error {
	return func(op *openapi.Operation) error {
		if m := mediaAt(i)(op); m != nil {
			return m.Err
		}
		return nil
	}
}

func opErr(op *openapi.Operation) error { return op.Err }

func serverErr(op *openapi.Operation) error {
	if len(op.Servers) == 0 {
		return nil
	}
	return op.Servers[0].Err
}

// paramNames returns the Names of ps.
func paramNames(ps []*openapi.Param) []string {
	var s []string
	for _, p := range ps {
		s = append(s, p.Name)
	}
	return s
}

func runShapeCases(t *testing.T, cases []shapeCase) {
	for _, sc := range cases {
		for _, order := range []string{"described first", "called first"} {
			t.Run(sc.name+"/"+order, func(t *testing.T) {
				fl := &fetchLog{}
				c, rec := recordingClient(t, sc.doc, &openapi.Loader{Fetch: fl.fetch})
				var desc *openapi.Operation
				if order == "described first" {
					desc = mustOp(t, c, sc.key)
					if sc.check != nil {
						sc.check(t, desc)
					}
				}
				o := sc.exercise(t, c, rec)
				if desc == nil {
					desc = mustOp(t, c, sc.key)
					if sc.check != nil {
						sc.check(t, desc)
					}
				}
				if again := mustOp(t, c, sc.key); again != desc {
					t.Errorf("Client.Operation returned %p, then %p", desc, again)
				}
				sc.verify(t, desc, o)
				if sc.after != nil {
					sc.after(t, c, desc)
				}
				if got := fl.all(); len(got) > 0 {
					t.Errorf("Fetch was asked for %q; want nothing retrieved", got)
				}
			})
		}
	}
}

// rawFormData is a pre-encoded multipart/form-data body with boundary b.
const rawFormData = "--b\r\nContent-Disposition: form-data; name=\"ok\"\r\n\r\nx\r\n--b--\r\n"

// rawMixed is a pre-encoded multipart/mixed body with boundary b.
const rawMixed = "--b\r\n\r\nraw\r\n--b--\r\n"

// Before OpenAPI 3.2 a content map's values are Media Type Objects, never
// references (OAS 3.0.4 and 3.1.2, Parameter Object and Header Object:
// content is "Map[string, Media Type Object]"). A parameter's or a header's
// content has no descriptor of its own, so the nearest part holding it is
// that Param; a header is not its response, so Message.Err stays nil. A
// value for the parameter is refused at its key (errors.go,
// RequestError.Inputs: "The key is the Param.Key"); a response header is not
// part of the request, so the call is sent.
func TestBundleValuesContentMapValue(t *testing.T) {
	var cases []shapeCase
	for _, v := range []string{"3.0.4", "3.1.2"} {
		doc := shapeDoc(v, `
			"/param":{"get":{"operationId":"param","parameters":[
				{"name":"q","in":"query","content":{"application/json":{"$ref":"other.yaml#/m"}}},
				{"name":"r","in":"query","schema":{"type":"string"}}],`+partErrResponse+`}},
			"/header":{"get":{"operationId":"header","responses":{"200":{"description":"ok",
				"headers":{"X-H":{"content":{"application/json":{"$ref":"other.yaml#/m"}}},"X-Ok":{"schema":{"type":"string"}}},
				"content":{"application/json":{"schema":{"type":"object"}}}}}}}`)
		cases = append(cases, shapeCase{
			orderCase: orderCase{name: v + " parameter", key: "param", ok: &openapi.Input{Params: map[string]any{"r": "x"}},
				bad: []refusal{{"a value", &openapi.Input{Params: map[string]any{"q": map[string]int{"a": 1}}}, "Inputs", "q", paramNamed("q")}}},
			doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantBundled(t, "parameter q", paramNamed("q")(op))
				wantUsable(t, "Operation", op.Err)
				wantUsable(t, "parameter r", paramNamed("r")(op))
			},
		}, shapeCase{
			orderCase: orderCase{name: v + " response header", key: "header", ok: &openapi.Input{}},
			doc:       doc,
			check: func(t *testing.T, op *openapi.Operation) {
				m := response(t, op, 0)
				wantUsable(t, "Operation", op.Err)
				wantUsable(t, "response 200", m.Err)
				h, ok := paramByName(m.Headers, "X-H"), paramByName(m.Headers, "X-Ok")
				if h == nil || ok == nil {
					t.Fatalf("headers %q", paramNames(m.Headers))
				}
				wantBundled(t, "header X-H", h.Err)
				wantUsable(t, "header X-Ok", ok.Err)
			},
		})
	}
	runShapeCases(t, cases)
}

// An encoding map, and in OpenAPI 3.2 a prefixEncoding list, are never
// references (OAS 3.x Media Type Object: encoding is "Map[string, Encoding
// Object]", prefixEncoding "[Encoding Object]"). Written as one, it sets
// Media.Err (describe.go, Media.Err: "an encoding map or prefixEncoding list
// written as a reference ... (see Operation), under any media type, which
// leaves no Encoding Param to report it"), and Media.Encoding names no field
// "$ref" and no part "0" (Media.Encoding: the parts are "those of
// prefixEncoding in order"). A structured body is checked against Media.Err and
// refused at the body (client.go, Input.Body: a pre-encoded body is not checked
// "against ... a Media.Err its key does not cause"), and a pre-encoded body
// is sent under that Media.
func TestBundleValuesEncodingMap(t *testing.T) {
	var cases []shapeCase
	check := func(t *testing.T, op *openapi.Operation) {
		m := mediaAt(0)(op)
		if m == nil {
			t.Fatal("no request body Media")
		}
		wantBundled(t, "Media "+m.Type, m.Err)
		wantUsable(t, "Operation", op.Err)
		wantUsable(t, "Body", op.Body.Err)
		for _, n := range []string{"$ref", "0"} {
			if p := paramByName(m.Encoding, n); p != nil {
				t.Errorf("Media.Encoding describes %q: %+v", n, p)
			}
		}
	}
	structured := func(body any) []refusal {
		return []refusal{{"a structured body", &openapi.Input{Body: body}, "Inputs", "Input.Body", mediaErrAt(0)}}
	}
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		paths := `
			"/form":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{` + orderFields + `,"encoding":{"$ref":"other.yaml#/enc"}}}},` + partErrResponse + `}},
			"/multipart":{"post":{"operationId":"multipart","requestBody":{"content":{"multipart/form-data":{` + orderFields + `,"encoding":{"$ref":"other.yaml#/enc"}}}},` + partErrResponse + `}}`
		if v == "3.2.1" {
			paths += `,
			"/positional":{"post":{"operationId":"positional","requestBody":{"content":{"multipart/mixed":{"prefixEncoding":{"$ref":"other.yaml#/list"}}}},` + partErrResponse + `}}`
		}
		doc := shapeDoc(v, paths)
		cases = append(cases,
			shapeCase{orderCase: orderCase{name: v + " form encoding map", key: "form",
				ok: &openapi.Input{Body: []byte("ok=x")}, body: firstMedia, bad: structured(map[string]any{"ok": "x"})}, doc: doc, check: check},
			shapeCase{orderCase: orderCase{name: v + " multipart encoding map", key: "multipart",
				ok: &openapi.Input{Body: []byte(rawFormData), MediaType: "multipart/form-data; boundary=b"}, body: firstMedia, bad: structured(map[string]any{"ok": "x"})}, doc: doc, check: check},
		)
		if v == "3.2.1" {
			cases = append(cases, shapeCase{orderCase: orderCase{name: v + " prefixEncoding list", key: "positional",
				ok: &openapi.Input{Body: []byte(rawMixed), MediaType: "multipart/mixed; boundary=b"}, body: firstMedia, bad: structured([]any{"x"})}, doc: doc, check: check})
		}
	}
	runShapeCases(t, cases)
}

// A Swagger 2.0 Items Object is never a reference (Swagger 2.0 section
// 6.4.10), so a formData array whose items are written as one has a
// Param.Err saying the document must be bundled, in every form Media it is
// described under (describe.go, Media.Encoding: "in Swagger 2.0 every
// formData parameter"; Message.Media: formData is paired "with the form types
// among them"). Giving it a value is refused under each form type at its
// pointer in the body, wrapping that Param.Err.
func TestBundleValuesSwaggerFormDataItems(t *testing.T) {
	doc := editionDoc("2.0", `"/x":{"post":{"operationId":"x","consumes":["application/x-www-form-urlencoded","multipart/form-data"],"parameters":[
		{"name":"csv","in":"formData","type":"array","items":{"$ref":"other.yaml#/items"}},
		{"name":"multi","in":"formData","type":"array","collectionFormat":"multi","items":{"$ref":"other.yaml#/items"}},
		{"name":"ok","in":"formData","type":"string"}],`+swaggerResponse+`}}`)
	types := []string{"application/x-www-form-urlencoded", "multipart/form-data"}
	var cases []shapeCase
	for i, typ := range types {
		cases = append(cases, shapeCase{
			orderCase: orderCase{name: typ, key: "x", ok: &openapi.Input{Body: map[string]any{"ok": "x"}, MediaType: typ}, body: mediaAt(i),
				bad: []refusal{
					{"csv", &openapi.Input{Body: map[string]any{"csv": []string{"a", "b"}}, MediaType: typ}, "Inputs", "Input.Body/csv", encodingErr(i, "csv")},
					{"multi", &openapi.Input{Body: map[string]any{"multi": []string{"a", "b"}}, MediaType: typ}, "Inputs", "Input.Body/multi", encodingErr(i, "multi")},
				}},
			doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				if op.Body == nil || len(op.Body.Media) != 2 {
					t.Fatalf("Body %+v; want a Media for each form type", op.Body)
				}
				for j, m := range op.Body.Media {
					if m.Type != types[j] {
						t.Errorf("Media %d is %q; want %q", j, m.Type, types[j])
					}
					wantBundled(t, m.Type+" field csv", encodingErr(j, "csv")(op))
					wantBundled(t, m.Type+" field multi", encodingErr(j, "multi")(op))
					wantUsable(t, m.Type+" field ok", encodingErr(j, "ok")(op))
				}
				wantUsable(t, "Operation", op.Err)
			},
		})
	}
	runShapeCases(t, cases)
}

// An OpenAPI 3.2 querystring parameter is serialized by its content (doc.go,
// Querystring: under application/x-www-form-urlencoded "its value is an
// object written by the form-body rules, Encoding included"). An Encoding
// Object, or the encoding map, written as a reference has no descriptor of
// its own there, so the nearest part holding it is the parameter, and any
// value for it is refused at its key, wrapping its Err.
func TestBundleValuesQuerystringEncoding(t *testing.T) {
	object := `"schema":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`
	doc := shapeDoc("3.2.1", `
		"/qs":{"get":{"operationId":"qs","parameters":[{"name":"qs","in":"querystring","content":{"application/x-www-form-urlencoded":{`+object+`,"encoding":{"a":{"$ref":"other.yaml#/e"}}}}}],`+partErrResponse+`}},
		"/qsMap":{"get":{"operationId":"qsMap","parameters":[{"name":"qs","in":"querystring","content":{"application/x-www-form-urlencoded":{`+object+`,"encoding":{"$ref":"other.yaml#/enc"}}}}],`+partErrResponse+`}}`)
	var cases []shapeCase
	for _, key := range []string{"qs", "qsMap"} {
		cases = append(cases, shapeCase{
			orderCase: orderCase{name: key, key: key, ok: &openapi.Input{},
				bad: []refusal{
					{"the field written as a reference", &openapi.Input{Params: map[string]any{"qs": map[string]string{"a": "x"}}}, "Inputs", "qs", paramNamed("qs")},
					{"another field", &openapi.Input{Params: map[string]any{"qs": map[string]string{"b": "y"}}}, "Inputs", "qs", paramNamed("qs")},
				}},
			doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantBundled(t, "parameter qs", paramNamed("qs")(op))
				wantUsable(t, "Operation", op.Err)
			},
		})
	}
	runShapeCases(t, cases)
}

// OpenAPI 3.2.1 section 4.14.4.2, Encoding By Position: prefixEncoding gives
// Encoding Objects "applied to the value at the same position in the data
// array, and itemEncoding applying its single Encoding Object to all
// remaining items in the array". describe.go, Media.Encoding names the parts
// "those of prefixEncoding in order, named "0", "1" and so on, then that of
// itemEncoding, named "*"". So with prefixEncoding shorter than the body,
// every later position is governed by "*", whatever prefixItems the schema
// lists (doc.go, Configuration: the schema's prefixItems entry gives only a
// part's default type), and each such part is refused at its pointer
// wrapping the Err of "*".
func TestBundleValuesPositionalItemEncoding(t *testing.T) {
	schema := `"schema":{"type":"array","prefixItems":[{"type":"string"},{"type":"string"}]}`
	doc := shapeDoc("3.2.1", `
		"/pos0":{"post":{"operationId":"pos0","requestBody":{"content":{"multipart/mixed":{`+schema+`,"prefixEncoding":[],"itemEncoding":{"contentType":"not a media type"}}}},`+partErrResponse+`}},
		"/pos1":{"post":{"operationId":"pos1","requestBody":{"content":{"multipart/mixed":{`+schema+`,"prefixEncoding":[{"contentType":"text/plain"}],"itemEncoding":{"contentType":"not a media type"}}}},`+partErrResponse+`}}`)
	abc := &openapi.Input{Body: []any{"a", "b", "c"}}
	at := func(i int) refusal {
		return refusal{fmt.Sprintf("position %d", i), abc, "Inputs", fmt.Sprintf("Input.Body/%d", i), fieldNamed("*")}
	}
	encodingIs := func(want ...string) func(*testing.T, *openapi.Operation) {
		return func(t *testing.T, op *openapi.Operation) {
			m := mediaAt(0)(op)
			if m == nil {
				t.Fatal("no request body Media")
			}
			if got := paramNames(m.Encoding); !slices.Equal(got, want) {
				t.Errorf("Media.Encoding %q; want %q", got, want)
			}
			if fieldNamed("*")(op) == nil {
				t.Error(`Encoding "*" reports no defect`)
			}
			wantUsable(t, "Media", m.Err)
		}
	}
	runShapeCases(t, []shapeCase{{
		orderCase: orderCase{name: "prefixEncoding empty", key: "pos0",
			ok: &openapi.Input{Body: []byte(rawMixed), MediaType: "multipart/mixed; boundary=b"}, body: firstMedia,
			bad: []refusal{at(0), at(1), at(2)}},
		doc: doc, check: encodingIs("*"),
	}, {
		orderCase: orderCase{name: "prefixEncoding shorter", key: "pos1",
			ok: &openapi.Input{Body: []any{"a"}}, body: firstMedia,
			bad: []refusal{at(1), at(2)}},
		doc: doc, check: encodingIs("0", "*"),
		after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
			_, err := c.Prepare(op.Key, abc)
			if _, ok := asRequestError(t, err).Inputs["Input.Body/0"]; ok {
				t.Error("position 0, which prefixEncoding governs, is refused")
			}
		},
	}})
}

// A deepObject value whose array holds only undefined items is defined:
// doc.go, Values: "null, an empty array and an object whose members are all
// undefined are undefined", and only those, so [null] is defined, and so is
// an object with a member [null]; "an undefined required one is missing", a
// defined one is not. doc.go, Styles: "the refusals here apply to defined
// values ... and so are an array as or in a deepObject value unless
// Options.DeepObjectArrays says how to write it"; client.go,
// Options.DeepObjectArrays: such an array "is refused at the value's key in
// RequestError.Inputs, and RequestError.Settings names
// "Options.DeepObjectArrays"". Under BracketArrays and IndexArrays an
// undefined item is skipped (DeepObjectArrays: "an item skipped as undefined
// takes none"), so the defined value writes nothing and the call is sent. An
// object whose only member is [] is undefined, so the required parameter is
// missing under every setting, never refused as an array.
func TestDeepObjectArraysDefinedValues(t *testing.T) {
	doc := shapeDoc("3.1.2", `"/deep":{"get":{"operationId":"deep","parameters":[{"name":"p","in":"query","style":"deepObject","required":true,"schema":{}}],`+partErrResponse+`}}`)
	values := []struct {
		name    string
		v       any
		defined bool
	}{
		{"an object with a member [null]", map[string]any{"a": []any{nil}}, true},
		{"[null]", []any{nil}, true},
		{"[null, null]", []any{nil, nil}, true},
		{"an object with a member []", map[string]any{"a": []any{}}, false},
	}
	settings := []struct {
		name string
		s    openapi.DeepObjectArrays
	}{{"RefuseArrays", openapi.RefuseArrays}, {"BracketArrays", openapi.BracketArrays}, {"IndexArrays", openapi.IndexArrays}}
	for _, st := range settings {
		for _, order := range []string{"described first", "called first"} {
			t.Run(st.name+"/"+order, func(t *testing.T) {
				base, _ := recordingClient(t, doc, &openapi.Loader{})
				c := base.With(func(o *openapi.Options) { o.DeepObjectArrays = st.s })
				var desc *openapi.Operation
				if order == "described first" {
					desc = mustOp(t, c, "deep")
				}
				type result struct {
					req *openapi.Request
					err error
				}
				results := make([]result, len(values))
				for i, v := range values {
					results[i].req, results[i].err = c.Prepare("deep", &openapi.Input{Params: map[string]any{"p": v.v}})
				}
				if desc == nil {
					desc = mustOp(t, c, "deep")
				}
				for i, v := range values {
					req, err := results[i].req, results[i].err
					switch {
					case !v.defined:
						re := asRequestError(t, err)
						wantKeys(t, "Inputs", re.Inputs, true, "p")
						if _, ok := re.Settings["Options.DeepObjectArrays"]; ok {
							t.Errorf("%s: Settings names Options.DeepObjectArrays for an undefined value", v.name)
						}
					case st.s == openapi.RefuseArrays:
						if err == nil {
							t.Errorf("%s: sent; want the array refused", v.name)
							continue
						}
						re := asRequestError(t, err)
						wantKeys(t, "Inputs", re.Inputs, true, "p")
						wantKeys(t, "Settings", re.Settings, true, "Options.DeepObjectArrays")
					default:
						if err != nil {
							t.Errorf("%s: refused with %v; want it sent, a defined value writing nothing", v.name, err)
							continue
						}
						if q := req.HTTP.URL.RawQuery; q != "" {
							t.Errorf("%s: query %q; want nothing written", v.name, q)
						}
						if got := openapi.OperationFromContext(req.HTTP.Context()); got != desc {
							t.Errorf("%s: OperationFromContext = %p; want the described Operation %p", v.name, got, desc)
						}
					}
				}
			})
		}
	}
}

// In a map of objects a member named $ref whose value is an object is an
// entry like any other (describe.go, Operation): a response header, a
// multipart Encoding header, and a Swagger 2.0 response header named $ref
// are described as headers, and nothing is unusable.
func TestBundleValuesHeaderNamedRef(t *testing.T) {
	var cases []shapeCase
	hdrCheck := func(t *testing.T, op *openapi.Operation) {
		m := response(t, op, 0)
		wantUsable(t, "Operation", op.Err)
		wantUsable(t, "response 200", m.Err)
		h := paramByName(m.Headers, "$ref")
		if h == nil {
			t.Fatalf("headers %q; want one named $ref", paramNames(m.Headers))
		}
		if h.In != "header" || h.Schema == nil {
			t.Errorf("header $ref %+v", h)
		}
		wantUsable(t, "header $ref", h.Err)
	}
	for _, v := range []string{"2.0", "3.0.4", "3.1.2", "3.2.1"} {
		if v == "2.0" {
			doc := editionDoc(v, `"/hdr":{"get":{"operationId":"hdr","produces":["application/json"],"responses":{"200":{"description":"ok","schema":{"type":"object"},"headers":{"$ref":{"type":"string"}}}}}}`)
			cases = append(cases, shapeCase{orderCase: orderCase{name: v + " response header", key: "hdr", ok: &openapi.Input{}}, doc: doc, check: hdrCheck})
			continue
		}
		doc := shapeDoc(v, `
			"/hdr":{"get":{"operationId":"hdr","responses":{"200":{"description":"ok","headers":{"$ref":{"schema":{"type":"string"}}},"content":{"application/json":{"schema":{"type":"object"}}}}}}},
			"/part":{"post":{"operationId":"part","requestBody":{"content":{"multipart/form-data":{`+orderFields+`,"encoding":{"f":{"headers":{"$ref":{"schema":{"type":"string"}}}}}}}},`+partErrResponse+`}}`)
		cases = append(cases,
			shapeCase{orderCase: orderCase{name: v + " response header", key: "hdr", ok: &openapi.Input{}}, doc: doc, check: hdrCheck},
			shapeCase{orderCase: orderCase{name: v + " multipart Encoding header", key: "part", ok: &openapi.Input{Body: map[string]any{"f": "x"}}, body: firstMedia}, doc: doc,
				check: func(t *testing.T, op *openapi.Operation) {
					m := mediaAt(0)(op)
					if m == nil {
						t.Fatal("no request body Media")
					}
					wantUsable(t, "Operation", op.Err)
					wantUsable(t, "Media", m.Err)
					f := paramByName(m.Encoding, "f")
					if f == nil {
						t.Fatalf("Media.Encoding %q", paramNames(m.Encoding))
					}
					wantUsable(t, "field f", f.Err)
					h := paramByName(f.Headers, "$ref")
					if h == nil {
						t.Fatalf("field f headers %q; want one named $ref", paramNames(f.Headers))
					}
					wantUsable(t, "field f header $ref", h.Err)
				}},
		)
	}
	runShapeCases(t, cases)
}

// Parts and values beyond those the earlier bundling tests cover:
//
//   - an OAuth Flow Object is never a reference (OAS 3.x OAuth Flows Object:
//     each flow is an "OAuth Flow Object"), so its SecurityScheme's Err says
//     to bundle; doc.go, Credentials: FromTransport "satisfies a scheme a
//     requirement names but the document ... declares defectively";
//   - a Server Object's variables map is never a reference (OAS 3.x Server
//     Object: "Map[string, Server Variable Object]"), so that Server's Err
//     says to bundle;
//   - describe.go, Operation: "a servers list so written is described as one
//     Server with that Err, which Options.BaseURL can replace as it can any
//     unusable server", at the operation, the Path Item or the root, while an
//     operation with its own servers does not inherit the root's; such a sole
//     server refuses a call as doc.go, Configuration says: one usable server
//     selects itself, "and none requires BaseURL";
//   - describe.go, Operation: "A parameters list so written makes every
//     operation it applies to unusable"; Operation.Err lists "a parameters
//     list that applies to it", and "Calling an operation with Err set returns
//     a *RequestError wrapping Err", with BaseURL too. What Params such an
//     operation lists is not specified, so it is not checked.
func TestBundleValuesNarrowParts(t *testing.T) {
	var cases []shapeCase
	baseURL := func(o *openapi.Options) { o.BaseURL = orderBase }
	operationBundled := func(t *testing.T, op *openapi.Operation) { wantBundled(t, "Operation", op.Err) }
	refusedOp := func(name, key, doc string) []shapeCase {
		return []shapeCase{
			{orderCase: orderCase{name: name, key: key, bad: []refusal{{"a call", nil, "Err", "", opErr}}}, doc: doc, check: operationBundled},
			{orderCase: orderCase{name: name + " with BaseURL", key: key, bad: []refusal{{"a call", nil, "Err", "", opErr}}, badWith: baseURL}, doc: doc, check: operationBundled},
		}
	}
	// soleServer is a case whose one Server is unusable for its shape: a call
	// is refused at Options.BaseURL wrapping its Err, and sent with BaseURL.
	soleServer := func(name, key, doc string) shapeCase {
		return shapeCase{orderCase: orderCase{name: name, key: key, ok: &openapi.Input{}, with: baseURL,
			bad: []refusal{{"its sole server", nil, "Settings", "Options.BaseURL", serverErr}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				if len(op.Servers) != 1 {
					t.Fatalf("Servers %+v; want one Server", op.Servers)
				}
				wantBundled(t, "Server", op.Servers[0].Err)
			}}
	}
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		doc := shapeDoc(v, `
			"/oauth":{"get":{"operationId":"oauth","security":[{"oauth":[]}],`+partErrResponse+`}},
			"/srv":{"get":{"operationId":"srv","servers":[{"url":"https://{host}/v1","variables":{"$ref":"other.yaml#/vars"}}],`+partErrResponse+`}},
			"/params":{"get":{"operationId":"params","parameters":{"$ref":"other.yaml#/params"},`+partErrResponse+`}},
			"/pathparams":{"parameters":{"$ref":"other.yaml#/params"},
				"get":{"operationId":"pathparamsGet",`+partErrResponse+`},
				"post":{"operationId":"pathparamsPost",`+partErrResponse+`}},
			"/opsrv":{"get":{"operationId":"opsrv","servers":{"$ref":"other.yaml#/servers"},`+partErrResponse+`}},
			"/pathsrv":{"servers":{"$ref":"other.yaml#/servers"},"get":{"operationId":"pathsrv",`+partErrResponse+`}}`,
			`"components":{"securitySchemes":{"oauth":{"type":"oauth2","flows":{"authorizationCode":{"$ref":"other.yaml#/flow"}}}}}`)
		schemeBundled := func(t *testing.T, op *openapi.Operation) {
			wantBundled(t, "SecurityScheme oauth", schemeErr(op))
			wantUsable(t, "Operation", op.Err)
		}
		fromTransport := func(o *openapi.Options) {
			o.Credentials = map[string]openapi.Credential{"oauth": openapi.FromTransport()}
		}
		cases = append(cases,
			shapeCase{orderCase: orderCase{name: v + " OAuth Flow Object, no credential", key: "oauth", ok: &openapi.Input{}, with: fromTransport,
				bad: []refusal{{"no credential", nil, "Settings", `Options.Credentials["oauth"]`, schemeErr}}}, doc: doc, check: schemeBundled},
			shapeCase{orderCase: orderCase{name: v + " OAuth Flow Object, a secret", key: "oauth",
				badWith: func(o *openapi.Options) {
					o.Credentials = map[string]openapi.Credential{"oauth": openapi.Secret("tok")}
				},
				bad: []refusal{{"a secret", nil, "Settings", `Options.Credentials["oauth"]`, schemeErr}}}, doc: doc, check: schemeBundled},
			soleServer(v+" Server variables", "srv", doc),
			soleServer(v+" Operation servers list", "opsrv", doc),
			soleServer(v+" Path Item servers list", "pathsrv", doc),
		)
		cases = append(cases, refusedOp(v+" Operation parameters list", "params", doc)...)
		cases = append(cases, refusedOp(v+" Path Item parameters list, get", "pathparamsGet", doc)...)
		cases = append(cases, refusedOp(v+" Path Item parameters list, post", "pathparamsPost", doc)...)
		root := editionDoc(v, `
			"/rootsrv":{"get":{"operationId":"rootsrv",`+partErrResponse+`}},
			"/own":{"get":{"operationId":"own","servers":[{"url":"`+orderBase+`"}],`+partErrResponse+`}}`,
			`"servers":{"$ref":"other.yaml#/servers"}`)
		cases = append(cases,
			soleServer(v+" root servers list", "rootsrv", root),
			shapeCase{orderCase: orderCase{name: v + " root servers list, own servers", key: "own", ok: &openapi.Input{}}, doc: root,
				check: func(t *testing.T, op *openapi.Operation) {
					wantUsable(t, "Operation", op.Err)
					if len(op.Servers) != 1 || op.Servers[0].URL != orderBase || op.Servers[0].Err != nil {
						t.Errorf("Servers %+v; want its own usable server", op.Servers)
					}
				}},
		)
	}
	runShapeCases(t, cases)
}

// describe.go, Operations: listed with an empty Key are "a Paths Object
// written as a reference (see Operation): listed once, with no Path or Method,
// and an Err saying the document must be bundled first" and "an OpenAPI 3.2
// additionalOperations map written as a reference: listed, with the same Err,
// once for each Paths entry whose nearest such map it is (see Operation), with
// that entry's Path and no Method". The Path
// Item's other operations are listed and called as usual.
func TestBundleValuesListedEntries(t *testing.T) {
	// listed returns the entries of c with an empty Key.
	listed := func(c *openapi.Client) []*openapi.Operation {
		var ops []*openapi.Operation
		for _, op := range c.Operations() {
			if op.Key == "" {
				ops = append(ops, op)
			}
		}
		return ops
	}
	wantEntry := func(t *testing.T, ops []*openapi.Operation, path string) {
		t.Helper()
		if len(ops) != 1 {
			t.Fatalf("%d entries with an empty Key; want 1", len(ops))
		}
		if ops[0].Path != path {
			t.Errorf("entry Path %q; want %q", ops[0].Path, path)
		}
		wantBundled(t, "entry", ops[0].Err)
	}
	t.Run("3.2.1 additionalOperations", func(t *testing.T) {
		doc := shapeDoc("3.2.1", `"/x":{"get":{"operationId":"g",`+partErrResponse+`},"additionalOperations":{"$ref":"other.yaml#/ops"}}`)
		for _, order := range []string{"described first", "called first"} {
			t.Run(order, func(t *testing.T) {
				fl := &fetchLog{}
				c, rec := recordingClient(t, doc, &openapi.Loader{Fetch: fl.fetch})
				var entries []*openapi.Operation
				var g *openapi.Operation
				if order == "described first" {
					entries, g = listed(c), findOp(t, c.Operations(), "g")
				}
				mustCall(t, c, "g", nil, nil)
				if entries == nil {
					entries, g = listed(c), findOp(t, c.Operations(), "g")
				}
				wantEntry(t, entries, "/x")
				wantUsable(t, "operation g", g.Err)
				if ops := rec.all(); len(ops) != 1 || ops[0] != g {
					t.Errorf("OperationFromContext in the transport %v; want the described g %p", ops, g)
				}
				if got := fl.all(); len(got) > 0 {
					t.Errorf("Fetch was asked for %q; want nothing retrieved", got)
				}
			})
		}
	})
	for _, v := range editionVersions {
		t.Run(v+" Paths Object", func(t *testing.T) {
			field := "openapi"
			if v == "2.0" {
				field = "swagger"
			}
			doc := fmt.Sprintf(`{%q:%q,"info":{"title":"t","version":"1"},"paths":{"$ref":"other.yaml#/paths"}}`, field, v)
			fl := &fetchLog{}
			c, _ := recordingClient(t, doc, &openapi.Loader{Fetch: fl.fetch})
			if ops := c.Operations(); len(ops) != 1 {
				t.Errorf("Operations lists %d entries; want the Paths Object alone", len(ops))
			}
			wantEntry(t, listed(c), "")
			if got := fl.all(); len(got) > 0 {
				t.Errorf("Fetch was asked for %q; want nothing retrieved", got)
			}
		})
	}
}

// A value written as a reference is "an object whose $ref member is a
// string" (describe.go, Operation), so an Operation Object whose $ref member
// is a number or an object is not one, and nothing marks it unusable.
func TestBundleValuesNonStringRef(t *testing.T) {
	var cases []shapeCase
	for _, v := range []string{"2.0", "3.1.2"} {
		resp := partErrResponse
		if v == "2.0" {
			resp = swaggerResponse
		}
		doc := shapeDoc(v, `
			"/num":{"get":{"operationId":"num","$ref":5,`+resp+`}},
			"/obj":{"get":{"operationId":"obj","$ref":{"x":"other.yaml"},`+resp+`}}`)
		for _, key := range []string{"num", "obj"} {
			cases = append(cases, shapeCase{orderCase: orderCase{name: v + " " + key, key: key, ok: &openapi.Input{}}, doc: doc,
				check: func(t *testing.T, op *openapi.Operation) { wantUsable(t, "Operation", op.Err) }})
		}
	}
	runShapeCases(t, cases)
}
