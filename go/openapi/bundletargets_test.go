package openapi_test

import (
	"errors"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Nested encodings, unread members, references into a value written as a
// reference, scopes, and encodings that do not apply. describe.go,
// Operation: "Bundling replaces such a value whole, so its other members are
// not read, and a reference whose target lies inside it cannot be followed
// before bundling either: it makes its own nearest part unusable the same
// way ... In a map of objects a member named $ref whose value is an object
// is an entry like any other, such as a header named $ref; in a map of
// strings, such as OAuth scopes, a string $ref member is a reference."
//
// The harness, and the checks of each call in both orders and of nothing
// being fetched, are those of bundlevalues_test.go.

// An OpenAPI 3.2 Encoding Object may itself hold encoding, prefixEncoding
// and itemEncoding for a part whose media type is multipart (OAS 3.2.1
// Encoding Object), which client.go, Input.Body encodes "one level deep". A
// value written as a reference anywhere inside a field's Encoding, a nested
// Encoding's headers map, or an Encoding Object two levels down, has no
// descriptor nearer than that field's Encoding Param (describe.go,
// Media.Encoding describes the fields), so the field's Param.Err says to
// bundle, Media.Err does not (Media.Err: "Any other schema or Encoding
// defect ... is reported by ... the relevant Encoding Param.Err"), and a
// call using the field is refused at its pointer wrapping that Err.
func TestBundleTargetsNestedEncoding(t *testing.T) {
	schema := `"schema":{"type":"object","properties":{"ok":{"type":"string"},
		"f":{"type":"object","properties":{"g":{"type":"object","properties":{"h":{"type":"string"}}}}}}}`
	doc := shapeDoc("3.2.1", `
		"/headers":{"post":{"operationId":"headers","requestBody":{"content":{"multipart/form-data":{`+schema+`,
			"encoding":{"f":{"contentType":"multipart/mixed","encoding":{"g":{"contentType":"text/plain","headers":{"$ref":"h.yaml#/h"}}}}}}}},`+partErrResponse+`}},
		"/deep":{"post":{"operationId":"deep","requestBody":{"content":{"multipart/form-data":{`+schema+`,
			"encoding":{"f":{"contentType":"multipart/mixed","encoding":{"g":{"contentType":"multipart/mixed","encoding":{"h":{"$ref":"e.yaml#/e"}}}}}}}}},`+partErrResponse+`}}`)
	check := func(t *testing.T, op *openapi.Operation) {
		wantUsable(t, "Operation", op.Err)
		m := mediaAt(0)(op)
		if m == nil {
			t.Fatal("no request body Media")
		}
		wantUsable(t, "Media", m.Err)
		wantBundled(t, "field f", fieldNamed("f")(op))
		wantUsable(t, "field ok", fieldNamed("ok")(op))
	}
	var cases []shapeCase
	for key, value := range map[string]any{
		"headers": map[string]any{"g": "x"},
		"deep":    map[string]any{"g": map[string]any{"h": "x"}},
	} {
		cases = append(cases, shapeCase{orderCase: orderCase{name: key, key: key,
			ok: &openapi.Input{Body: map[string]any{"ok": "x"}}, body: firstMedia,
			bad: []refusal{{"a value for f", &openapi.Input{Body: map[string]any{"f": value}}, "Inputs", "Input.Body/f", fieldNamed("f")}}},
			doc: doc, check: check})
	}
	runShapeCases(t, cases)
}

// Members beside a reference are not read:
//
//   - before OpenAPI 3.2 a content entry is a Media Type Object, never a
//     reference (OAS 3.0.4 and 3.1.2, Parameter and Header Objects: content
//     is "Map[string, Media Type Object]"), so a parameter's or a header's
//     Param.Err says to bundle and its Schema, which describe.go,
//     Param.Schema takes from "the schema of the single content entry", is
//     not built from the schema beside the reference;
//   - a Server Variable Object is never a reference (OAS 3.x Server Object:
//     variables is "Map[string, Server Variable Object]"), so its Server's
//     Err says to bundle, and the default, enum and description beside the
//     reference describe nothing (describe.go, Variable).
func TestBundleTargetsSiblingsNotRead(t *testing.T) {
	var cases []shapeCase
	for _, v := range []string{"3.0.4", "3.1.2"} {
		doc := shapeDoc(v, `
			"/param":{"get":{"operationId":"param","parameters":[
				{"name":"q","in":"query","content":{"application/json":{"$ref":"m.yaml#/m","schema":{"type":"integer"}}}},
				{"name":"form","in":"query","content":{"application/x-www-form-urlencoded":{"$ref":"m.yaml#/m","schema":{"type":"object","properties":{"a":{"type":"string"}}}}}},
				{"name":"r","in":"query","schema":{"type":"string"}}],`+partErrResponse+`}},
			"/header":{"get":{"operationId":"header","responses":{"200":{"description":"ok",
				"headers":{"X":{"content":{"text/plain":{"$ref":"m.yaml#/m","schema":{"type":"integer"}}}}},
				"content":{"application/json":{"schema":{"type":"object"}}}}}}},
			"/var":{"get":{"operationId":"var","servers":[{"url":"https://{h}.example.test",
				"variables":{"h":{"$ref":"v.yaml#/v","default":"api","enum":["api","x"],"description":"beside"}}}],`+partErrResponse+`}}`)
		notBuilt := func(t *testing.T, what string, p *openapi.Param) {
			t.Helper()
			if p == nil {
				t.Fatalf("no %s", what)
			}
			wantBundled(t, what, p.Err)
			if p.Schema != nil {
				t.Errorf("%s: Schema %s built from beside the reference", what, p.Schema.Raw())
			}
		}
		cases = append(cases,
			shapeCase{orderCase: orderCase{name: v + " parameter content entry", key: "param", ok: &openapi.Input{Params: map[string]any{"r": "x"}},
				bad: []refusal{
					{"q", &openapi.Input{Params: map[string]any{"q": 1}}, "Inputs", "q", paramNamed("q")},
					{"form", &openapi.Input{Params: map[string]any{"form": map[string]string{"a": "x"}}}, "Inputs", "form", paramNamed("form")},
				}}, doc: doc,
				check: func(t *testing.T, op *openapi.Operation) {
					wantUsable(t, "Operation", op.Err)
					notBuilt(t, "parameter q", paramByName(op.Params, "q"))
					notBuilt(t, "parameter form", paramByName(op.Params, "form"))
				}},
			shapeCase{orderCase: orderCase{name: v + " response header content entry", key: "header", ok: &openapi.Input{}}, doc: doc,
				check: func(t *testing.T, op *openapi.Operation) {
					wantUsable(t, "Operation", op.Err)
					m := response(t, op, 0)
					wantUsable(t, "response 200", m.Err)
					notBuilt(t, "header X", paramByName(m.Headers, "X"))
				}},
			shapeCase{orderCase: orderCase{name: v + " Server Variable", key: "var", ok: &openapi.Input{}, with: func(o *openapi.Options) { o.BaseURL = orderBase },
				bad: []refusal{{"its sole server", nil, "Settings", "Options.BaseURL", serverErr}}}, doc: doc,
				check: func(t *testing.T, op *openapi.Operation) {
					wantUsable(t, "Operation", op.Err)
					if len(op.Servers) != 1 {
						t.Fatalf("Servers %+v; want one Server", op.Servers)
					}
					s := op.Servers[0]
					wantBundled(t, "Server", s.Err)
					for _, vr := range s.Variables {
						if vr.DefaultSet || vr.Default != "" || vr.Enum != nil || vr.Description != "" {
							t.Errorf("variable %+v; want nothing read from beside the reference", vr)
						}
					}
				}},
		)
	}
	runShapeCases(t, cases)
}

// load.go, Load fails "with a *RequestError, on Options the document cannot
// use: ... a Variables name no server URL uses, a MediaType no operation
// declares, ... a Server or ServerID that matches no server, a Security or
// SecurityKey that matches no alternative", and a setting the document cannot
// use is keyed by its field (errors.go, RequestError.Settings). Members beside
// a reference are not read, so a server URL, a variable, or a media type
// given only there is none of the document's.
func TestBundleTargetsLoadChecks(t *testing.T) {
	refServer := `"servers":[{"$ref":"s.yaml#/s","url":"https://{v}.sibling.example.test"},{"url":"` + orderBase + `"}]`
	doc := editionDoc("3.1.2", `"/a":{"get":{"operationId":"a",`+partErrResponse+`}}`, refServer)
	content := editionDoc("3.1.2", `"/b":{"post":{"operationId":"b","requestBody":{"content":{"$ref":"c.yaml#/c","application/xml":{}}},`+partErrResponse+`}}`,
		`"servers":[{"url":"`+orderBase+`"}]`)
	for _, tc := range []struct {
		name, doc, key string
		opts           openapi.Options
	}{
		{"Server naming a URL beside a reference", doc, "Options.Server", openapi.Options{Server: "https://{v}.sibling.example.test"}},
		{"Variables used only beside a reference", doc, `Options.Variables["v"]`, openapi.Options{Variables: map[string]string{"v": "x"}}},
		{"MediaType declared only beside a reference", content, "Options.MediaType", openapi.Options{MediaType: "application/xml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fl := &fetchLog{}
			l := &openapi.Loader{Fetch: fl.fetch}
			c, err := l.Parse(t.Context(), []byte(tc.doc), testDocURI, &tc.opts)
			if c != nil {
				t.Errorf("Load returned a Client; want the setting refused")
			}
			wantKeys(t, "Settings", asRequestError(t, err).Settings, true, tc.key)
			if got := fl.all(); len(got) > 0 {
				t.Errorf("Fetch was asked for %q; want nothing retrieved", got)
			}
		})
	}
}

// A root security value written as a reference is inherited by each
// operation that declares none, its nearest part, which Operation.Err makes
// unusable ("a security value, the operation's or the root's it inherits",
// written as a reference, "another value whose nearest part is the
// operation"); every call is refused wrapping it. A SecurityKey naming an
// alternative the document does not hold is refused by Load (load.go:
// "a Security or SecurityKey that matches no alternative").
func TestBundleTargetsRootSecurity(t *testing.T) {
	doc := editionDoc("3.1.2", `"/a":{"get":{"operationId":"a",`+partErrResponse+`}}`,
		`"servers":[{"url":"`+orderBase+`"}]`, `"security":{"$ref":"s.yaml#/s"}`)
	runShapeCases(t, []shapeCase{{
		orderCase: orderCase{name: "inherited", key: "a", bad: []refusal{{"a call", nil, "Err", "", opErr}}}, doc: doc,
		check: func(t *testing.T, op *openapi.Operation) { wantBundled(t, "Operation", op.Err) },
	}})
	t.Run("SecurityKey", func(t *testing.T) {
		_, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{SecurityKey: `{"k":[]}`})
		wantKeys(t, "Settings", asRequestError(t, err).Settings, true, "Options.SecurityKey")
	})
}

// A Components Object is never a reference (OAS 3.x OpenAPI Object:
// components is a "Components Object"). Written as one, its other members are
// not read, so a reference to "#/components/..." has its target inside it
// and cannot be followed before bundling: a parameter reference, whose
// identity cannot then be known, makes the Operation unusable (describe.go,
// Operation.Err: "an unresolvable parameter ... reference, whose identity
// and requiredness cannot be known"), and a security requirement naming a
// scheme among those members makes its SecurityScheme unusable; neither Err
// wraps ErrUnresolved, and the schema reference inside the unread parameter
// is never retrieved.
func TestBundleTargetsComponentsObject(t *testing.T) {
	var cases []shapeCase
	for _, v := range []string{"3.0.4", "3.1.2"} {
		doc := shapeDoc(v, `
			"/p":{"get":{"operationId":"p","parameters":[{"$ref":"#/components/parameters/P"}],`+partErrResponse+`}},
			"/s":{"get":{"operationId":"s","security":[{"k":[]}],`+partErrResponse+`}}`,
			`"components":{"$ref":"c.yaml#/components",
				"securitySchemes":{"k":{"type":"apiKey","in":"header","name":"X-K"}},
				"parameters":{"P":{"name":"q","in":"query","schema":{"$ref":"sib.yaml#/s"}}}}`)
		cases = append(cases,
			shapeCase{orderCase: orderCase{name: v + " parameter reference", key: "p", bad: []refusal{{"a call", nil, "Err", "", opErr}}}, doc: doc,
				check: func(t *testing.T, op *openapi.Operation) { wantBundled(t, "Operation", op.Err) }},
			shapeCase{orderCase: orderCase{name: v + " security requirement", key: "s", ok: &openapi.Input{},
				with: func(o *openapi.Options) { o.Credentials = map[string]openapi.Credential{"k": openapi.FromTransport()} },
				bad:  []refusal{{"no credential", nil, "Settings", `Options.Credentials["k"]`, schemeErr}}}, doc: doc,
				check: func(t *testing.T, op *openapi.Operation) {
					wantUsable(t, "Operation", op.Err)
					wantBundled(t, "SecurityScheme k", schemeErr(op))
				}},
		)
	}
	runShapeCases(t, cases)
}

// In OpenAPI 3.2 a requirement's scheme name is looked up among the
// components first, and "a name that is not a component name where it is
// looked up is a URI reference to a Security Scheme Object" (load.go,
// SchemeLookup). When the Components Object, or its securitySchemes map
// alone, is written as a reference, where the name would be looked up
// cannot be read before bundling, so the lookup's target lies inside a value
// written as a reference and "cannot be followed before bundling either: it
// makes its own nearest part unusable the same way" (describe.go,
// Operation). The SecurityScheme's Err says to bundle, and the name is not
// read as a URI reference, so nothing is fetched. A call is refused at the
// scheme's credential wrapping that Err, and FromTransport satisfies it.
func TestBundleTargetsComponentsSchemeName32(t *testing.T) {
	scheme := `"k":{"type":"apiKey","in":"header","name":"X-K"}`
	var cases []shapeCase
	for _, tc := range []struct{ name, components string }{
		{"Components Object", `"components":{"$ref":"c.yaml#/components","securitySchemes":{` + scheme + `}}`},
		{"securitySchemes map, k beside it", `"components":{"securitySchemes":{"$ref":"s.yaml#/schemes",` + scheme + `}}`},
		{"securitySchemes map, nothing beside it", `"components":{"securitySchemes":{"$ref":"s.yaml#/schemes"}}`},
	} {
		name, components := tc.name, tc.components
		doc := shapeDoc("3.2.1", `"/s":{"get":{"operationId":"s","security":[{"k":[]}],`+partErrResponse+`}}`, components)
		cases = append(cases, shapeCase{orderCase: orderCase{name: name, key: "s", ok: &openapi.Input{},
			with: func(o *openapi.Options) { o.Credentials = map[string]openapi.Credential{"k": openapi.FromTransport()} },
			bad:  []refusal{{"no credential", nil, "Settings", `Options.Credentials["k"]`, schemeErr}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantBundled(t, "SecurityScheme k", schemeErr(op))
			}})
	}
	runShapeCases(t, cases)
}

// A reference whose target lies among the unread members of an Operation
// Object written as a reference cannot be followed either: a parameter
// reference into them makes the referring Operation unusable, as above, and
// a response reference makes its Message unusable, the response not being
// part of the request (describe.go, Operation.Err: a defect in an optional
// part "fails a call only when the call uses it").
func TestBundleTargetsIntoOperationObject(t *testing.T) {
	doc := shapeDoc("3.1.2", `
		"/x":{"get":{"$ref":"other.yaml#/op",
			"parameters":[{"name":"q","in":"query","schema":{"type":"string"}}],
			"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}},
		"/y":{"get":{"operationId":"y","parameters":[{"$ref":"#/paths/~1x/get/parameters/0"}],`+partErrResponse+`}},
		"/z":{"get":{"operationId":"z","responses":{"200":{"$ref":"#/paths/~1x/get/responses/200"}}}}`)
	runShapeCases(t, []shapeCase{
		{orderCase: orderCase{name: "parameter reference", key: "y", bad: []refusal{{"a call", nil, "Err", "", opErr}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) { wantBundled(t, "Operation", op.Err) }},
		{orderCase: orderCase{name: "response reference", key: "z"}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				m := respByKey(op, "200")
				if m == nil {
					t.Fatal("no response 200")
				}
				wantBundled(t, "response 200", m.Err)
			},
			after: func(t *testing.T, c *openapi.Client, op *openapi.Operation) { sendDeclared(t, c, op, nil, "200") }},
	})
}

// "in a map of strings, such as OAuth scopes, a string $ref member is a
// reference": a scopes map with one is a value written as a reference, held
// by its flow, whose nearest part is the SecurityScheme. A call is refused
// at the scheme's credential wrapping its Err, and FromTransport satisfies it
// (doc.go, Credentials: FromTransport "satisfies a scheme a requirement
// names but the document ... declares defectively").
func TestBundleTargetsScopesMap(t *testing.T) {
	scopes := `"scopes":{"$ref":"Read references","write":"w"}`
	docs := map[string]string{
		"2.0": editionDoc("2.0", `"/a":{"get":{"operationId":"a","security":[{"o":["write"]}],`+swaggerResponse+`}}`,
			`"securityDefinitions":{"o":{"type":"oauth2","flow":"application","tokenUrl":"https://t.example.test/token",`+scopes+`}}`),
	}
	for _, v := range []string{"3.0.4", "3.1.2", "3.2.1"} {
		docs[v] = shapeDoc(v, `"/a":{"get":{"operationId":"a","security":[{"o":["write"]}],`+partErrResponse+`}}`,
			`"components":{"securitySchemes":{"o":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://t.example.test/token",`+scopes+`}}}}}`)
	}
	var cases []shapeCase
	for _, v := range editionVersions {
		cases = append(cases, shapeCase{orderCase: orderCase{name: v, key: "a", ok: &openapi.Input{},
			with: func(o *openapi.Options) { o.Credentials = map[string]openapi.Credential{"o": openapi.FromTransport()} },
			bad:  []refusal{{"no credential", nil, "Settings", `Options.Credentials["o"]`, schemeErr}}}, doc: docs[v],
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantBundled(t, "SecurityScheme o", schemeErr(op))
			}})
	}
	runShapeCases(t, cases)
}

// An encoding written as a reference counts even where OpenAPI ignores it
// (describe.go, Operation: "A value so written makes its part unusable even
// where OpenAPI says the value is ignored, such as an encoding under a JSON
// media type ..., since the document still needs bundling"). An encoding map
// so written sets Media.Err "under any media type" (Media.Err), so under
// application/json too. An entry so written under application/json has no
// Encoding Param to hold it, since Media.Encoding describes only "the fields
// of form or multipart content", so its nearest part is the Media as well. In
// a query parameter's content, the nearest part is that Param. A structured
// body is refused at the body wrapping Media.Err, and a pre-encoded body,
// which "is checked against neither" (Media.Err), is sent; a value for the
// parameter is refused at its key wrapping its Err.
func TestBundleTargetsEncodingNotApplying(t *testing.T) {
	object := `"schema":{"type":"object","properties":{"f":{"type":"string"}}}`
	doc := shapeDoc("3.1.2", `
		"/jsonMap":{"post":{"operationId":"jsonMap","requestBody":{"content":{"application/json":{`+object+`,"encoding":{"$ref":"e.yaml#/e"}}}},`+partErrResponse+`}},
		"/jsonEntry":{"post":{"operationId":"jsonEntry","requestBody":{"content":{"application/json":{`+object+`,"encoding":{"f":{"$ref":"e.yaml#/f"}}}}},`+partErrResponse+`}},
		"/query":{"get":{"operationId":"query","parameters":[{"name":"q","in":"query","content":{"application/x-www-form-urlencoded":{`+object+`,"encoding":{"$ref":"e.yaml#/e"}}}}],`+partErrResponse+`}},
		"/form":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{`+object+`,"encoding":{"$ref":"e.yaml#/e"}}}},`+partErrResponse+`}}`)
	mediaBundled := func(t *testing.T, op *openapi.Operation) {
		wantUsable(t, "Operation", op.Err)
		wantUsable(t, "Body", op.Body.Err)
		wantBundled(t, "Media", mediaErrAt(0)(op))
	}
	structured := []refusal{{"a structured body", &openapi.Input{Body: map[string]string{"f": "1"}}, "Inputs", "Input.Body", mediaErrAt(0)}}
	runShapeCases(t, []shapeCase{
		{orderCase: orderCase{name: "JSON body, encoding map", key: "jsonMap", ok: &openapi.Input{Body: []byte(`{"f":"1"}`)}, body: firstMedia, bad: structured}, doc: doc, check: mediaBundled},
		{orderCase: orderCase{name: "JSON body, encoding entry", key: "jsonEntry", ok: &openapi.Input{Body: []byte(`{"f":"1"}`)}, body: firstMedia, bad: structured}, doc: doc, check: mediaBundled},
		{orderCase: orderCase{name: "query parameter form content", key: "query", ok: &openapi.Input{},
			bad: []refusal{{"a value", &openapi.Input{Params: map[string]any{"q": map[string]string{"f": "1"}}}, "Inputs", "q", paramNamed("q")}}}, doc: doc,
			check: func(t *testing.T, op *openapi.Operation) {
				wantUsable(t, "Operation", op.Err)
				wantBundled(t, "parameter q", paramNamed("q")(op))
			}},
		{orderCase: orderCase{name: "form body", key: "form", ok: &openapi.Input{Body: []byte("f=1")}, body: firstMedia, bad: structured}, doc: doc, check: mediaBundled},
	})
}

// Swagger 2.0 schemes written as a reference "is described as a servers list
// is": one Server, whose Source is empty, as describe.go, Server.Source says
// of every Swagger 2.0 server ("Empty for a Swagger 2.0 server, which the
// client assembles from host, basePath and schemes").
func TestBundleTargetsSwaggerSchemesSource(t *testing.T) {
	ref := `"schemes":{"$ref":"other.yaml#/schemes"}`
	for name, doc := range map[string]string{
		"operation": editionDoc("2.0", `"/s":{"get":{"operationId":"s",`+ref+`,`+swaggerResponse+`}}`),
		"root":      editionDoc("2.0", `"/s":{"get":{"operationId":"s",`+swaggerResponse+`}}`, ref),
	} {
		t.Run(name, func(t *testing.T) {
			fl := &fetchLog{}
			c, _ := recordingClient(t, doc, &openapi.Loader{Fetch: fl.fetch})
			op := mustOp(t, c, "s")
			if len(op.Servers) != 1 {
				t.Fatalf("Servers %+v; want one Server", op.Servers)
			}
			if s := op.Servers[0]; s.Source != "" || !mentionsBundling(s.Err) || errors.Is(s.Err, openapi.ErrUnresolved) {
				t.Errorf("Server Source %q Err %v; want an empty Source and the bundling Err", s.Source, s.Err)
			}
			if got := fl.all(); len(got) > 0 {
				t.Errorf("Fetch was asked for %q; want nothing retrieved", got)
			}
		})
	}
}
