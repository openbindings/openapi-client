package openapi_test

import (
	"errors"
	"mime"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Swagger 2.0 formData parameters are fields of the body, never located
// parameters. describe.go, Param: "a field of a form or multipart body, or
// a Swagger 2.0 formData parameter (In and Key are empty)"; Media.Encoding
// lists "in Swagger 2.0 every formData parameter"; Operation.Params "holds
// no Swagger 2.0 body or formData parameter (see Body)"; client.go,
// Input.Params: "In Swagger 2.0, formData parameters are properties of
// Body", and "A key the operation does not declare ... refuses the call".
// So every Encoding Param of a Swagger 2.0 form or multipart body has an
// empty In and an empty Key: under each media type the operation consumes,
// a type file field included, whether the parameter is written in the
// operation, inherited from its Path Item, or reached by a $ref to the
// root parameters, and through Request.Media, which "is the same immutable
// descriptor Operation.Body.Media exposes" (client.go). The fields are
// still sent as the body, in the order doc.go, Order, gives: "a form or
// multipart body's fields, follow the order encoding/json writes members in
// (a struct's fields in declaration order, a map's keys sorted)". The
// documentation does not order the Swagger 2.0 Encoding list, so the
// fields are compared as a set.
func TestSwaggerFormDataFieldsHaveNoLocation(t *testing.T) {
	doc := editionDoc("2.0", `
		"/form":{"post":{"operationId":"form","consumes":["application/x-www-form-urlencoded"],"parameters":[
			{"name":"q","in":"query","type":"string"},
			{"name":"name","in":"formData","type":"string","required":true},
			{"name":"tags","in":"formData","type":"array","collectionFormat":"multi","items":{"type":"string"}}],`+swaggerResponse+`}},
		"/upload":{"parameters":[{"name":"note","in":"formData","type":"string"}],
			"post":{"operationId":"upload","consumes":["multipart/form-data"],"parameters":[
				{"name":"file","in":"formData","type":"file","required":true},
				{"$ref":"#/parameters/Shared"}],`+swaggerResponse+`}},
		"/both":{"post":{"operationId":"both","consumes":["multipart/form-data","application/x-www-form-urlencoded"],"parameters":[
			{"name":"file","in":"formData","type":"file"},
			{"name":"n","in":"formData","type":"integer"}],`+swaggerResponse+`}},
		"/untyped":{"post":{"operationId":"untyped","parameters":[{"name":"x","in":"formData","type":"string"}],`+swaggerResponse+`}}`,
		`"parameters":{"Shared":{"name":"shared","in":"formData","type":"string"}}`)
	c := editionClient(t, doc, nil)
	for _, tc := range []struct {
		key    string
		params []string // Operation.Params by Name
		types  []string // the Body's Media types, in document order
		fields []string // each Media's Encoding by Name, sorted
	}{
		{"form", []string{"q"}, []string{"application/x-www-form-urlencoded"}, []string{"name", "tags"}},
		{"upload", nil, []string{"multipart/form-data"}, []string{"file", "note", "shared"}},
		{"both", nil, []string{"multipart/form-data", "application/x-www-form-urlencoded"}, []string{"file", "n"}},
		{"untyped", nil, []string{""}, []string{"x"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			op := mustOp(t, c, tc.key)
			if op.Err != nil {
				t.Fatalf("Err = %v; want the operation usable", op.Err)
			}
			wantStrings(t, "Params", paramNames(op.Params), tc.params)
			if op.Body == nil {
				t.Fatalf("Body is nil; want the formData parameters as the body")
			}
			var types []string
			for _, m := range op.Body.Media {
				types = append(types, m.Type)
				wantStrings(t, "Encoding of "+m.Type, slices.Sorted(slices.Values(paramNames(m.Encoding))), tc.fields)
				for _, p := range m.Encoding {
					if p.In != "" || p.Key != "" {
						t.Errorf("%q field %s: In %q, Key %q; want both empty", m.Type, p.Name, p.In, p.Key)
					}
				}
			}
			wantStrings(t, "Media types", types, tc.types)
		})
	}

	t.Run("form call", func(t *testing.T) {
		op := mustOp(t, c, "form")
		req := mustPrepare(t, c, "form", &openapi.Input{Params: map[string]any{"q": "v"}, Body: map[string]any{"name": "a", "tags": []string{"x", "y"}}})
		if req.Media != reqMedia(t, op, 0) {
			t.Errorf("Request.Media = %p; want Operation.Body.Media[0] %p", req.Media, reqMedia(t, op, 0))
		}
		if got, want := string(editionBody(t, req)), "name=a&tags=x&tags=y"; got != want {
			t.Errorf("body %q; want %q", got, want)
		}
		if got := req.HTTP.URL.RawQuery; got != "q=v" {
			t.Errorf("query %q; want q=v", got)
		}
		_, err := c.Prepare("form", &openapi.Input{Params: map[string]any{"name": "a"}})
		wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, "name")
	})

	t.Run("multipart call", func(t *testing.T) {
		op := mustOp(t, c, "upload")
		req := mustPrepare(t, c, "upload", &openapi.Input{Body: map[string]any{"file": []byte("abc"), "note": "n", "shared": "s"}})
		if req.Media != reqMedia(t, op, 0) {
			t.Errorf("Request.Media = %p; want Operation.Body.Media[0] %p", req.Media, reqMedia(t, op, 0))
		}
		_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), editionBody(t, req))
		var names, bodies []string
		for _, p := range parts {
			_, params, err := mime.ParseMediaType(p.header.Get("Content-Disposition"))
			if err != nil {
				t.Fatalf("Content-Disposition %q: %v", p.header.Get("Content-Disposition"), err)
			}
			names = append(names, params["name"])
			bodies = append(bodies, string(p.body))
		}
		wantStrings(t, "part names", names, []string{"file", "note", "shared"})
		wantStrings(t, "part bodies", bodies, []string{"abc", "n", "s"})
	})
}

// s2Parameters is a Swagger 2.0 document, served as s2.json, whose root
// parameters are an optional formData field g, a required formData field
// f, and a body parameter b, and whose Path Items /form and /body hold
// Swagger 2.0 operations that use them.
const s2Parameters = `{"swagger":"2.0","info":{"title":"s2","version":"1"},"paths":{
	"/form":{"post":{"operationId":"form2","consumes":["application/x-www-form-urlencoded"],"parameters":[{"$ref":"#/parameters/g"},{"$ref":"#/parameters/f"}],` + swaggerResponse + `}},
	"/body":{"post":{"operationId":"body2","consumes":["application/json"],"parameters":[{"$ref":"#/parameters/b"}],` + swaggerResponse + `}}},
	"parameters":{
	"g":{"name":"g","in":"formData","type":"string"},
	"f":{"name":"f","in":"formData","type":"string","required":true},
	"b":{"name":"b","in":"body","required":true,"schema":{"type":"object","properties":{"n":{"type":"integer"}}}}}}`

// s2URI is where s2Parameters is served.
const s2URI = "https://api.example.test/s2.json"

// A Swagger 2.0 body or formData parameter that an OpenAPI 3.x operation
// reaches makes that operation unusable. describe.go, Operation.Err is set
// by "a Swagger 2.0 body or formData parameter that an OpenAPI 3.x
// operation reaches, as through a $ref into a Swagger document, since such
// an operation has no place for one", and "Calling an operation with Err
// set returns a *RequestError wrapping Err"; the reference resolves, so the
// Err does not wrap ErrUnresolved ("Wherever an Err's cause is a reference
// that could not be resolved, it wraps [ErrUnresolved]"). Operation.Params
// "holds no Swagger 2.0 body or formData parameter", while the operation's
// own 3.x parameter q is still listed. load.go, Loader: "An OpenAPI
// document uses the edition declared at its root", so a parameter written
// in s2.json is a Swagger 2.0 Parameter Object wherever it is used. The
// operation declares no requestBody, so its Body is nil (Operation.Body:
// "nil when the operation takes none"; "In Swagger 2.0 it comes from the
// body parameter, or from the formData parameters").
//
// The parameter is reached from the operation's parameters and from its
// Path Item's. A 3.x Path Item whose $ref names a Swagger 2.0 Path Item
// reaches a Swagger 2.0 operation instead, which keeps them as its Body
// (describe.go, Operation: "Swagger 2.0 body and formData parameters
// appear as Body") and stays usable, inheriting the entry document's root
// servers ("Inherited declarations (path-level parameters, the entry
// document's root servers, and root security) are already applied").
//
// Each case runs described first and called first, on fresh Clients, with
// the checks of TestDescribeOrderSameDescriptors, and Fetch is asked for
// s2.json alone.
func TestSwaggerBodyParametersInOpenAPI3Operations(t *testing.T) {
	q := `{"name":"q","in":"query","schema":{"type":"string"}}`
	entry := shapeDoc("3.1.2", `
		"/g":{"post":{"operationId":"g","parameters":[`+q+`,{"$ref":"s2.json#/parameters/g"}],`+partErrResponse+`}},
		"/f":{"post":{"operationId":"f","parameters":[`+q+`,{"$ref":"s2.json#/parameters/f"}],`+partErrResponse+`}},
		"/b":{"post":{"operationId":"b","parameters":[`+q+`,{"$ref":"s2.json#/parameters/b"}],`+partErrResponse+`}},
		"/pg":{"parameters":[{"$ref":"s2.json#/parameters/g"}],"post":{"operationId":"pg","parameters":[`+q+`],`+partErrResponse+`}},
		"/pf":{"parameters":[{"$ref":"s2.json#/parameters/f"}],"post":{"operationId":"pf","parameters":[`+q+`],`+partErrResponse+`}},
		"/pb":{"parameters":[{"$ref":"s2.json#/parameters/b"}],"post":{"operationId":"pb","parameters":[`+q+`],`+partErrResponse+`}},
		"/rf":{"$ref":"s2.json#/paths/~1form"},
		"/rb":{"$ref":"s2.json#/paths/~1body"}`)

	unusable := func(t *testing.T, op *openapi.Operation) {
		if op.Err == nil {
			t.Errorf("Err is nil; want the operation unusable")
		} else if errors.Is(op.Err, openapi.ErrUnresolved) {
			t.Errorf("Err = %v wraps ErrUnresolved; every reference resolves", op.Err)
		}
		wantStrings(t, "Params", paramNames(op.Params), []string{"q"})
		if op.Body != nil {
			t.Errorf("Body = %+v; want nil, the operation declares no requestBody", op.Body)
		}
	}
	// refused lists calls of an unusable operation, each refused wrapping
	// its Err: none, its own parameter, the Swagger parameter supplied as a
	// parameter, and a body holding it.
	refused := func(name string, body any) []refusal {
		return []refusal{
			{"no input", nil, "Err", "", opErr},
			{"its 3.x parameter", &openapi.Input{Params: map[string]any{"q": "x"}}, "Err", "", opErr},
			{"a value for " + name, &openapi.Input{Params: map[string]any{"q": "x", name: "x"}}, "Err", "", opErr},
			{"a body", &openapi.Input{Body: body, MediaType: "application/json"}, "Err", "", opErr},
		}
	}
	type caseT = editionCase
	var cases []caseT
	for _, key := range []string{"g", "f", "b"} {
		name, body := key, any(map[string]string{key: "x"})
		if key == "b" {
			body = map[string]int{"n": 1}
		}
		cases = append(cases,
			caseT{orderCase: orderCase{name: "3.1 operation, 2.0 parameter " + key, key: key, bad: refused(name, body)}, check: unusable},
			caseT{orderCase: orderCase{name: "3.1 path-level parameters, 2.0 parameter " + key, key: "p" + key, bad: refused(name, body)}, check: unusable})
	}
	cases = append(cases,
		caseT{
			orderCase: orderCase{name: "3.1 Path Item reference to a 2.0 form operation", key: "form2",
				ok:   &openapi.Input{Body: map[string]string{"f": "x", "g": "y"}},
				body: firstMedia,
				bad: []refusal{
					{"without the required f", &openapi.Input{Body: map[string]string{"g": "y"}}, "Inputs", "Input.Body/f", nil},
					{"g as a parameter", &openapi.Input{Params: map[string]any{"g": "y"}, Body: map[string]string{"f": "x"}}, "Inputs", "g", nil},
				}},
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantStrings(t, "Params", paramNames(op.Params), nil)
				if op.Body == nil || !op.Body.Required || len(op.Body.Media) != 1 || op.Body.Media[0].Type != "application/x-www-form-urlencoded" {
					t.Fatalf("Body %+v; want one required form Media", op.Body)
				}
				m := op.Body.Media[0]
				wantStrings(t, "Encoding", slices.Sorted(slices.Values(paramNames(m.Encoding))), []string{"f", "g"})
				for _, p := range m.Encoding {
					if p.In != "" || p.Key != "" {
						t.Errorf("field %s: In %q, Key %q; want both empty", p.Name, p.In, p.Key)
					}
				}
			},
			after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
				req := mustPrepare(t, c, op.Key, &openapi.Input{Body: map[string]string{"g": "y", "f": "x"}})
				if got := string(editionBody(t, req)); got != "f=x&g=y" {
					t.Errorf("body %q; want f=x&g=y", got)
				}
			},
		},
		caseT{
			orderCase: orderCase{name: "3.1 Path Item reference to a 2.0 body operation", key: "body2",
				ok:   &openapi.Input{Body: map[string]int{"n": 1}},
				body: firstMedia,
				bad:  []refusal{{"without the required body", nil, "Inputs", "Input.Body", nil}}},
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantStrings(t, "Params", paramNames(op.Params), nil)
				if op.Body == nil || !op.Body.Required || len(op.Body.Media) != 1 || op.Body.Media[0].Type != "application/json" || op.Body.Media[0].Schema == nil {
					t.Fatalf("Body %+v; want one required JSON Media with a schema", op.Body)
				}
				sameJSON(t, "Body schema", op.Body.Media[0].Schema.Raw(), []byte(`{"type":"object","properties":{"n":{"type":"integer"}}}`))
			},
			after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
				req := mustPrepare(t, c, op.Key, &openapi.Input{Body: map[string]int{"n": 1}})
				sameJSON(t, "body", editionBody(t, req), []byte(`{"n":1}`))
			},
		})
	runEditionCases(t, entry, map[string]string{s2URI: s2Parameters}, cases)
}

// An editionCase is an orderCase with checks of its descriptor and,
// optionally, of further calls.
type editionCase struct {
	orderCase
	check func(t *testing.T, op *openapi.Operation)
	after func(t *testing.T, c *openapi.Client, op *openapi.Operation)
}

// runEditionCases runs each case described first and called first, each on
// a fresh Client parsing entry with a Fetch that serves docs, with the
// checks of TestDescribeOrderSameDescriptors, and fails if Fetch is asked
// for a URI docs does not hold.
func runEditionCases(t *testing.T, entry string, docs map[string]string, cases []editionCase) {
	t.Helper()
	for _, tc := range cases {
		for _, order := range []string{"described first", "called first"} {
			t.Run(tc.name+"/"+order, func(t *testing.T) {
				mf := newMemFetch(docs)
				c, rec := recordingClient(t, entry, &openapi.Loader{Fetch: mf.fetch})
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
				for _, u := range mf.callList() {
					if _, ok := docs[u]; !ok {
						t.Errorf("Fetch was asked for %s; want only the documents referenced", u)
					}
				}
			})
		}
	}
}

// v3BodyLocations is an OpenAPI 3.1 document, served as v3.json, whose
// components.parameters are written in "formData" and "body", which are not
// OpenAPI 3.x locations: f and b optional, fr and br required.
const v3BodyLocations = `{"openapi":"3.1.0","info":{"title":"v3","version":"1"},"paths":{},"components":{"parameters":{
	"F":{"name":"f","in":"formData","schema":{"type":"string"}},
	"FR":{"name":"fr","in":"formData","required":true,"schema":{"type":"string"}},
	"B":{"name":"b","in":"body","schema":{"type":"object"}},
	"BR":{"name":"br","in":"body","required":true,"schema":{"type":"object"}}}}}`

// v3URI is where v3BodyLocations is served.
const v3URI = "https://api.example.test/v3.json"

// Whether a parameter is a Swagger 2.0 body or formData parameter follows
// the edition of the document it is written in (load.go, Loader: "An
// OpenAPI document uses the edition declared at its root"). An OpenAPI 3.x
// Parameter Object written in "formData" or "body" that a Swagger 2.0
// operation reaches through a $ref is therefore not one: it is an ordinary
// parameter whose location is not an OpenAPI 3.x location (describe.go,
// Param.In: "path", "query", "header", "cookie", or "querystring" (3.2)).
// It is listed in Params, since Params "holds no Swagger 2.0 body or
// formData parameter (see Body)" and this is neither, with its Key (its
// Name, unambiguous here), its Source in v3.json, and its own Err, as
// Param.Err is "why built-in serialization cannot use the value". It does
// not reach Body ("In Swagger 2.0 it comes from the body parameter, or
// from the formData parameters"), so the operation takes no body and a
// body given to it is refused (errors.go, RequestError.Inputs: "a body the
// operation does not take"). The operation stays usable: Operation.Err, "A
// required parameter with a known Key but unsupported serialization has
// its own Param.Err, which Input.ParamWriters may bypass", and "A defect in
// an optional part is reported on that part instead, and fails a call only
// when the call uses it, the *RequestError then wrapping that part's Err,
// whether or not the operation was described first". Param.Err: "otherwise
// a required one refuses the call"; client.go, Input.ParamWriters: "A
// writer also bypasses that parameter's serialization Err, including for a
// required parameter, when its Key is known."
//
// The parameters are reached from the operation's parameters and from its
// Path Item's. The mirror controls are Swagger 2.0 formData and body
// parameters in s2.json reached the same ways, which stay the operation's
// Body (describe.go, Operation: "Swagger 2.0 body and formData parameters
// appear as Body"). Each case runs described first and called first, on
// fresh Clients, with the checks of TestDescribeOrderSameDescriptors.
func TestOpenAPI3BodyLocationsInSwaggerOperations(t *testing.T) {
	q := `{"name":"q","in":"query","type":"string"}`
	ref := func(uri string) string { return `{"$ref":"` + uri + `"}` }
	op := func(path, id, consumes string, params ...string) string {
		return `"` + path + `":{"post":{"operationId":"` + id + `","consumes":["` + consumes + `"],"parameters":[` + strings.Join(params, ",") + `],` + swaggerResponse + `}}`
	}
	pathLevel := func(path, id, consumes, inherited string) string {
		return `"` + path + `":{"parameters":[` + inherited + `],"post":{"operationId":"` + id + `","consumes":["` + consumes + `"],"parameters":[` + q + `],` + swaggerResponse + `}}`
	}
	const formType, jsonType = "application/x-www-form-urlencoded", "application/json"
	paths := []string{
		op("/f", "f", formType, q, ref("v3.json#/components/parameters/F")),
		op("/fr", "fr", formType, q, ref("v3.json#/components/parameters/FR")),
		op("/b", "b", jsonType, q, ref("v3.json#/components/parameters/B")),
		op("/br", "br", jsonType, q, ref("v3.json#/components/parameters/BR")),
		pathLevel("/pf", "pf", formType, ref("v3.json#/components/parameters/F")),
		pathLevel("/pfr", "pfr", formType, ref("v3.json#/components/parameters/FR")),
		pathLevel("/pb", "pb", jsonType, ref("v3.json#/components/parameters/B")),
		pathLevel("/pbr", "pbr", jsonType, ref("v3.json#/components/parameters/BR")),
		op("/cg", "cg", formType, q, ref("s2.json#/parameters/g")),
		op("/cb", "cb", jsonType, q, ref("s2.json#/parameters/b")),
		pathLevel("/pcg", "pcg", formType, ref("s2.json#/parameters/g")),
		pathLevel("/pcb", "pcb", jsonType, ref("s2.json#/parameters/b")),
	}
	entry := shapeDoc("2.0", strings.Join(paths, ","))

	// listed checks an operation whose v3.json parameter name, written as
	// component, is listed with its own Err, after q or, inherited, before
	// it.
	listed := func(name, component string, inherited bool) func(t *testing.T, op *openapi.Operation) {
		return func(t *testing.T, op *openapi.Operation) {
			wantUsable(t, "Operation", op.Err)
			want := []string{"q", name}
			if inherited {
				want = []string{name, "q"}
			}
			wantStrings(t, "Params", paramNames(op.Params), want)
			if op.Body != nil {
				t.Errorf("Body = %+v; want nil, the operation has no Swagger 2.0 body or formData parameter", op.Body)
			}
			p := paramByName(op.Params, name)
			if p == nil {
				return
			}
			if p.Key != name {
				t.Errorf("%s: Key %q; want %q", name, p.Key, name)
			}
			if want := v3URI + "#/components/parameters/" + component; p.Source != want {
				t.Errorf("%s: Source %q; want %q", name, p.Source, want)
			}
			if p.Err == nil {
				t.Errorf("%s: Err is nil; want why its location cannot be used", name)
			}
		}
	}
	// writer supplies a parameter by appending to the query.
	writer := func(pair string) map[string]func(*http.Request) error {
		return map[string]func(*http.Request) error{strings.SplitN(pair, "=", 2)[0]: func(r *http.Request) error {
			if r.URL.RawQuery != "" {
				r.URL.RawQuery += "&"
			}
			r.URL.RawQuery += pair
			return nil
		}}
	}
	wrote := func(name string) func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
		return func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
			req := mustPrepare(t, c, op.Key, &openapi.Input{Params: map[string]any{"q": "x"}, ParamWriters: writer(name + "=w")})
			if got, want := req.HTTP.URL.RawQuery, "q=x&"+name+"=w"; got != want {
				t.Errorf("query %q; want %q", got, want)
			}
			if req.HTTP.Body != nil && req.HTTP.Body != http.NoBody {
				t.Errorf("a body was sent; want none")
			}
		}
	}
	var cases []editionCase
	for _, tc := range []struct {
		name, component, key string
		required, inherited  bool
	}{
		{"f", "F", "f", false, false},
		{"fr", "FR", "fr", true, false},
		{"b", "B", "b", false, false},
		{"br", "BR", "br", true, false},
		{"f", "F", "pf", false, true},
		{"fr", "FR", "pfr", true, true},
		{"b", "B", "pb", false, true},
		{"br", "BR", "pbr", true, true},
	} {
		where := "2.0 operation"
		if tc.inherited {
			where = "2.0 path-level parameters"
		}
		value := any("y")
		if strings.HasPrefix(tc.name, "b") {
			value = map[string]int{"n": 1}
		}
		supplied := refusal{"a value for " + tc.name, &openapi.Input{Params: map[string]any{"q": "x", tc.name: value}}, "Inputs", tc.name, paramNamed(tc.name)}
		asBody := refusal{"a body", &openapi.Input{Params: map[string]any{"q": "x"}, Body: map[string]any{tc.name: value}}, "Inputs", "Input.Body", nil}
		c := editionCase{
			orderCase: orderCase{name: where + ", 3.1 " + tc.component, key: tc.key},
			check:     listed(tc.name, tc.component, tc.inherited),
			after:     wrote(tc.name),
		}
		if tc.required {
			c.ok = &openapi.Input{Params: map[string]any{"q": "x"}, ParamWriters: writer(tc.name + "=w")}
			c.bad = []refusal{{"no value", &openapi.Input{Params: map[string]any{"q": "x"}}, "Inputs", tc.name, nil}, supplied, asBody}
		} else {
			c.ok = &openapi.Input{Params: map[string]any{"q": "x"}}
			c.bad = []refusal{supplied, asBody}
		}
		cases = append(cases, c)
	}

	// The mirror controls: Swagger 2.0 formData and body parameters in
	// s2.json, reached the same ways, are the Body.
	field := func(t *testing.T, op *openapi.Operation) {
		wantUsable(t, "Operation", op.Err)
		wantStrings(t, "Params", paramNames(op.Params), []string{"q"})
		if op.Body == nil || len(op.Body.Media) != 1 || op.Body.Media[0].Type != formType {
			t.Fatalf("Body %+v; want one form Media", op.Body)
		}
		m := op.Body.Media[0]
		wantStrings(t, "Encoding", paramNames(m.Encoding), []string{"g"})
		for _, p := range m.Encoding {
			if p.In != "" || p.Key != "" || p.Source != s2URI+"#/parameters/g" {
				t.Errorf("field %s: In %q, Key %q, Source %q; want In and Key empty, Source the s2.json parameter", p.Name, p.In, p.Key, p.Source)
			}
		}
	}
	sentForm := func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
		req := mustPrepare(t, c, op.Key, &openapi.Input{Params: map[string]any{"q": "x"}, Body: map[string]string{"g": "y"}})
		if got := string(editionBody(t, req)); got != "g=y" {
			t.Errorf("body %q; want g=y", got)
		}
	}
	body := func(t *testing.T, op *openapi.Operation) {
		wantUsable(t, "Operation", op.Err)
		wantStrings(t, "Params", paramNames(op.Params), []string{"q"})
		if op.Body == nil || !op.Body.Required || len(op.Body.Media) != 1 || op.Body.Media[0].Type != jsonType || op.Body.Source != s2URI+"#/parameters/b" {
			t.Fatalf("Body %+v; want one required JSON Media declared by the s2.json parameter", op.Body)
		}
	}
	sentJSON := func(t *testing.T, c *openapi.Client, op *openapi.Operation) {
		req := mustPrepare(t, c, op.Key, &openapi.Input{Params: map[string]any{"q": "x"}, Body: map[string]int{"n": 1}})
		sameJSON(t, "body", editionBody(t, req), []byte(`{"n":1}`))
	}
	for _, key := range []string{"cg", "pcg"} {
		cases = append(cases, editionCase{
			orderCase: orderCase{name: "2.0 formData control " + key, key: key,
				ok:   &openapi.Input{Params: map[string]any{"q": "x"}, Body: map[string]string{"g": "y"}},
				body: firstMedia,
				bad:  []refusal{{"g as a parameter", &openapi.Input{Params: map[string]any{"q": "x", "g": "y"}}, "Inputs", "g", nil}}},
			check: field, after: sentForm,
		})
	}
	for _, key := range []string{"cb", "pcb"} {
		cases = append(cases, editionCase{
			orderCase: orderCase{name: "2.0 body control " + key, key: key,
				ok:   &openapi.Input{Params: map[string]any{"q": "x"}, Body: map[string]int{"n": 1}},
				body: firstMedia,
				bad:  []refusal{{"without the required body", &openapi.Input{Params: map[string]any{"q": "x"}}, "Inputs", "Input.Body", nil}}},
			check: body, after: sentJSON,
		})
	}
	runEditionCases(t, entry, map[string]string{v3URI: v3BodyLocations, s2URI: s2Parameters}, cases)
}

// Several Swagger 2.0 body parameters, or body and formData parameters
// together, make the operation unusable (Swagger 2.0, Operation Object
// parameters: "There can be one "body" parameter at most"; Parameter
// Object in: form parameters "cannot be declared together with a body
// parameter for the same operation"). describe.go, Operation.Params "holds
// no Swagger 2.0 body or formData parameter (see Body)", with no exception
// for an unusable operation, and Param.Key: "No two parameters of an
// operation share a Key". So Params lists the other
// parameters exactly once each, in the order doc.go, Order, gives: "the
// path item's parameters, then the operation's, in declared order".
// Operation.Err: "Calling an operation with Err set returns a
// *RequestError wrapping Err." Each case runs described first and called
// first, on fresh Clients, with the checks of
// TestDescribeOrderSameDescriptors.
func TestSwaggerSeveralBodyParameters(t *testing.T) {
	q := `{"name":"q","in":"query","type":"string"}`
	h := `{"name":"h","in":"header","type":"string"}`
	x := `{"name":"x","in":"query","type":"string"}`
	body := func(n string) string { return `{"name":"` + n + `","in":"body","schema":{"type":"object"}}` }
	form := func(n string) string { return `{"name":"` + n + `","in":"formData","type":"string"}` }
	const consumes = `"consumes":["application/json","application/x-www-form-urlencoded"],`
	op := func(params ...string) string {
		return `"/x":{"post":{"operationId":"x",` + consumes + `"parameters":[` + strings.Join(params, ",") + `],` + swaggerResponse + `}}`
	}
	inherited := func(pathLevel, opLevel []string) string {
		return `"/x":{"parameters":[` + strings.Join(pathLevel, ",") + `],"post":{"operationId":"x",` + consumes + `"parameters":[` + strings.Join(opLevel, ",") + `],` + swaggerResponse + `}}`
	}
	refused := []refusal{
		{"no input", nil, "Err", "", opErr},
		{"a parameter", &openapi.Input{Params: map[string]any{"q": "v"}}, "Err", "", opErr},
		{"a JSON body", &openapi.Input{Body: map[string]int{"n": 1}, MediaType: "application/json"}, "Err", "", opErr},
	}
	check := func(names, ins []string) func(t *testing.T, op *openapi.Operation) {
		return func(t *testing.T, op *openapi.Operation) {
			if op.Err == nil {
				t.Errorf("Err is nil; want the operation unusable")
			} else if errors.Is(op.Err, openapi.ErrUnresolved) {
				t.Errorf("Err = %v wraps ErrUnresolved; nothing is unresolved", op.Err)
			}
			wantStrings(t, "Params", paramNames(op.Params), names)
			var gotIns []string
			keys := map[string]bool{}
			for _, p := range op.Params {
				gotIns = append(gotIns, p.In)
				if keys[p.Key] {
					t.Errorf("two parameters share the Key %q", p.Key)
				}
				keys[p.Key] = true
			}
			wantStrings(t, "Params' In", gotIns, ins)
		}
	}
	two, three := []string{"q", "h"}, []string{"q", "h", "x"}
	twoIn, threeIn := []string{"query", "header"}, []string{"query", "header", "query"}
	var cases []shapeCase
	for _, tc := range []struct {
		name, paths string
		names, ins  []string
	}{
		{"two body parameters", op(q, body("b1"), h, body("b2")), two, twoIn},
		{"three body parameters", op(body("b1"), q, body("b2"), h, body("b3")), two, twoIn},
		{"a body parameter, then formData", op(q, body("b"), h, form("f")), two, twoIn},
		{"formData, then a body parameter", op(form("f"), q, body("b"), h), two, twoIn},
		{"body parameters at path level and operation level", inherited([]string{q, body("b1"), h}, []string{body("b2"), x}), three, threeIn},
		{"formData at path level, a body parameter at operation level", inherited([]string{form("f"), q}, []string{h, body("b"), x}), three, threeIn},
	} {
		cases = append(cases, shapeCase{
			orderCase: orderCase{name: tc.name, key: "x", bad: refused},
			doc:       shapeDoc("2.0", tc.paths),
			check:     check(tc.names, tc.ins),
		})
	}
	runShapeCases(t, cases)
}

// Discovery follows a discriminator's mapping only where mapping is a map.
// load.go, Loader: "The references followed are $ref in Reference Objects,
// Path Items and Schema Objects, $dynamicRef, the values of a Discriminator
// mapping written as an object and a defaultMapping value, where they are not
// component names". describe.go,
// SchemaReference: a reference is "a value of a discriminator's mapping".
// The Discriminator Object's mapping is "Map[string, string]" (OAS 3.0.4
// section 4.7.25.1, 3.1.2 section 4.8.25.1, and the 3.2.1 Discriminator
// Object), so an array, a string or a number written as mapping has no
// mapping values: Load retrieves nothing for it, and Schema.References
// lists none of it. A mapping written as an object is followed, its value
// fetched once and listed with its target.
func TestDiscoveryMappingNotAnObject(t *testing.T) {
	const other = "https://api.example.test/other.json"
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		for _, tc := range []struct {
			name, mapping string
			followed      bool
		}{
			{"an array", `["other.json#/S"]`, false},
			{"a string", `"other.json#/S"`, false},
			{"a number", `5`, false},
			{"an object", `{"s":"other.json#/S"}`, true},
		} {
			t.Run(v+"/"+tc.name, func(t *testing.T) {
				doc := editionDoc(v, "", `"components":{"schemas":{
					"A":{"oneOf":[{"$ref":"#/components/schemas/B"}],"discriminator":{"propertyName":"k","mapping":`+tc.mapping+`}},
					"B":{"type":"object"}}}`)
				mf := newMemFetch(map[string]string{other: `{"S":{"type":"object"}}`})
				c, err := (&openapi.Loader{Fetch: mf.fetch}).Parse(t.Context(), []byte(doc), testDocURI, nil)
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				calls := mf.callList()
				for _, u := range calls {
					if u != other {
						t.Errorf("Fetch was asked for %s", u)
					}
				}
				if want := map[bool]int{true: 1, false: 0}[tc.followed]; mf.callsTo(other) != want {
					t.Errorf("Fetch was asked for %s %d times; want %d", other, mf.callsTo(other), want)
				}
				s, err := c.Schema(testDocURI + "#/components/schemas/A")
				if err != nil {
					t.Fatal(err)
				}
				refs, err := s.References()
				if err != nil {
					t.Fatal(err)
				}
				var ats []string
				for _, r := range refs {
					ats = append(ats, r.At)
				}
				if !tc.followed {
					wantStrings(t, "References at", ats, []string{"/oneOf/0/$ref"})
					return
				}
				wantStrings(t, "References at", ats, []string{"/oneOf/0/$ref", "/discriminator/mapping/s"})
				if len(refs) == 2 {
					r := refs[1]
					if r.Keyword != "mapping" || r.Value != "other.json#/S" || r.URI != other+"#/S" || r.Err != nil || r.Target == nil {
						t.Errorf("mapping reference %+v; want other.json#/S resolved", r)
					}
				}
			})
		}
	}
}
