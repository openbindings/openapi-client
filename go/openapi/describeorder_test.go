package openapi_test

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Describing an operation and calling it report the same descriptors,
// whichever comes first. The documentation this checks:
//
//   - client.go, Request.Media: "It is the same immutable descriptor
//     Operation.Body.Media exposes."
//   - client.go, Response.Declaration: "It is the same immutable descriptor
//     Operation.Responses exposes." Response.Media: "It is the same
//     immutable descriptor Declaration.Media exposes."
//   - client.go, OperationFromContext: "returns the operation being sent";
//     Request.HTTP: "Its context carries the operation for
//     OperationFromContext".
//   - describe.go, Operations: "the Operations it points to are shared by
//     every caller".
//   - describe.go, Operation.Err: "Calling an operation with Err set
//     returns a *RequestError wrapping Err." errors.go, RequestError: why a
//     call was refused is told by "errors.Is(err, op.Err), for a defective
//     operation; Settings and Inputs, for what the caller must supply or
//     correct".
//   - client.go, Input.Body: a pre-encoded body is not checked "against a
//     field's Param.Err, required formData fields, or a Media.Err its key
//     does not cause", so a structured body is checked against the field's
//     Param.Err; describe.go,
//     Param.Err: "Err is why built-in serialization cannot use the value";
//     Media.Err: an Encoding defect "is reported by ... the relevant
//     Encoding Param.Err".
//   - describe.go, Server.Err: "why the document alone makes the server
//     unusable";
//     SecurityScheme.Err: "why the scheme cannot be used ... Alternatives
//     that use it can be applied only when FromTransport satisfies it."
//
// A refusal caused by a defect a descriptor reports must therefore satisfy
// errors.Is(err, thatDescriptor.Err) for the descriptor describing returns,
// in either order. Each defect here has an Err the document alone decides
// and that names something in it (a contentType, a style, a path template,
// a server URL, a scheme name), so its identity, not only its text, is what
// the check compares.

// orderBase is the root server of orderDoc.
const orderBase = "https://api.example.test"

// orderResponses is a 200 response with JSON content, which memRT answers.
const orderResponses = `"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}`

// orderFields is an object schema with the fields f and ok.
const orderFields = `"schema":{"type":"object","properties":{"f":{"type":"string"},"ok":{"type":"string"}}}`

// orderShared is a Path Item with one operation, holding a defective
// parameter and a defective inline request body field.
const orderShared = `{"post":{
	"parameters":[{"name":"q","in":"query","style":"matrix","schema":{"type":"string"}},{"name":"r","in":"query","schema":{"type":"string"}}],
	"requestBody":{"content":{"multipart/form-data":{` + orderFields + `,"encoding":{"f":{"contentType":"not a media type"}}}}},
	` + orderResponses + `}}`

// orderDoc is an OpenAPI 3.1.2 document with one operation per defect, and
// two Paths entries reaching one Operation Object through one Path Item
// reference.
func orderDoc() string {
	return editionDoc("3.1.2", `
		"/multipart":{"post":{"operationId":"multipart","requestBody":{"content":{"multipart/form-data":{`+orderFields+`,"encoding":{"f":{"contentType":"not a media type"}}}}},`+orderResponses+`}},
		"/form":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{`+orderFields+`,"encoding":{"f":{"contentType":"not a media type"}}}}},`+orderResponses+`}},
		"/styled":{"post":{"operationId":"styled","requestBody":{"content":{"multipart/*":{`+orderFields+`,"encoding":{"f":{"style":"form","contentType":"not a media type"}}}}},`+orderResponses+`}},
		"/badStyle":{"post":{"operationId":"badStyle","requestBody":{"content":{"application/x-www-form-urlencoded":{`+orderFields+`,"encoding":{"f":{"style":"matrix"}}}}},`+orderResponses+`}},
		"/param":{"get":{"operationId":"param","parameters":[{"name":"q","in":"query","style":"matrix","schema":{"type":"string"}},{"name":"r","in":"query","schema":{"type":"string"}}],`+orderResponses+`}},
		"/broken/{nope}":{"get":{"operationId":"broken",`+orderResponses+`}},
		"/server":{"get":{"operationId":"server","servers":[{"url":"`+orderBase+`/v1?x=1"}],`+orderResponses+`}},
		"/secure":{"get":{"operationId":"secure","security":[{"undeclared":[]}],`+orderResponses+`}},
		"/one":{"$ref":"#/components/pathItems/Shared"},
		"/two":{"$ref":"#/components/pathItems/Shared"}`,
		`"servers":[{"url":"`+orderBase+`"}]`,
		`"components":{"pathItems":{"Shared":`+orderShared+`}}`)
}

// opRecorder records the operation OperationFromContext reports inside the
// transport for each request sent.
type opRecorder struct {
	mu  sync.Mutex
	ops []*openapi.Operation
}

func (o *opRecorder) all() []*openapi.Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]*openapi.Operation(nil), o.ops...)
}

// orderClient parses orderDoc into a fresh Client whose transport answers
// every request with 200 and a JSON object, recording the operation.
func orderClient(t testing.TB) (*openapi.Client, *opRecorder) {
	t.Helper()
	return recordingClient(t, orderDoc(), &openapi.Loader{})
}

// recordingClient parses doc with l into a fresh Client, as orderClient
// does.
func recordingClient(t testing.TB, doc string, l *openapi.Loader) (*openapi.Client, *opRecorder) {
	t.Helper()
	rec := &opRecorder{}
	rt := &memRT{answer: func(r *http.Request) (*http.Response, error) {
		op := openapi.OperationFromContext(r.Context())
		rec.mu.Lock()
		rec.ops = append(rec.ops, op)
		rec.mu.Unlock()
		return memResponse(r, 200, http.Header{"Content-Type": {"application/json"}}, "{}"), nil
	}}
	c, err := l.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c, rec
}

// A refusal is a call refused for a defect a descriptor reports.
type refusal struct {
	name  string
	in    *openapi.Input
	field string // "Inputs", "Settings" or "Err": where the RequestError holds it
	key   string // its key in Inputs or Settings
	// defect returns the descriptor Err the refusal must wrap, or is nil
	// when the documentation names none.
	defect func(op *openapi.Operation) error
}

// An orderCase is an operation, a call it accepts, and calls it refuses.
type orderCase struct {
	name string
	key  string
	ok   *openapi.Input                          // a call that is sent, or nil
	with func(*openapi.Options)                  // Options for the call that is sent, or nil
	body func(*openapi.Operation) *openapi.Media // the Media governing its body, or nil for none
	bad  []refusal

	badWith func(*openapi.Options) // Options for the refused calls, or nil
}

// fieldErr returns the Err of the field f of the request body's first Media.
func fieldErr(op *openapi.Operation) error {
	if op.Body == nil || len(op.Body.Media) == 0 {
		return nil
	}
	if p := paramByName(op.Body.Media[0].Encoding, "f"); p != nil {
		return p.Err
	}
	return nil
}

// paramErr returns the Err of the parameter q.
func paramErr(op *openapi.Operation) error {
	if p := paramByName(op.Params, "q"); p != nil {
		return p.Err
	}
	return nil
}

func firstMedia(op *openapi.Operation) *openapi.Media {
	if op.Body == nil || len(op.Body.Media) == 0 {
		return nil
	}
	return op.Body.Media[0]
}

func badField(name string) refusal {
	return refusal{name, &openapi.Input{Body: map[string]string{"f": "x"}}, "Inputs", "Input.Body/f", fieldErr}
}

func orderCases() []orderCase {
	okBody := &openapi.Input{Body: map[string]string{"ok": "x"}}
	return []orderCase{
		{name: "multipart field Encoding", key: "multipart", ok: okBody, body: firstMedia, bad: []refusal{badField("the field")}},
		{name: "form field Encoding", key: "form", ok: okBody, body: firstMedia, bad: []refusal{badField("the field")}},
		// describe.go, Param.ContentType: under multipart/form-data, an
		// Encoding that sets style makes contentType "ignored there". Under
		// a multipart range the defective contentType therefore refuses a
		// call of another multipart type only.
		{name: "styled field with a defective contentType", key: "styled",
			ok:   &openapi.Input{Body: map[string]string{"f": "x"}, MediaType: "multipart/form-data"},
			body: firstMedia,
			bad:  []refusal{{"under multipart/mixed", &openapi.Input{Body: map[string]string{"f": "x"}, MediaType: "multipart/mixed"}, "Inputs", "Input.Body/f", fieldErr}},
		},
		// A style OpenAPI does not allow for a form field (doc.go, Styles:
		// refused "with Param.Err set where the document alone decides it").
		{name: "form field style", key: "badStyle", ok: okBody, body: firstMedia, bad: []refusal{badField("the field")}},
		{name: "parameter", key: "param", ok: &openapi.Input{Params: map[string]any{"r": "x"}},
			bad: []refusal{{"the parameter", &openapi.Input{Params: map[string]any{"q": "x"}}, "Inputs", "q", paramErr}}},
		{name: "operation", key: "broken",
			bad: []refusal{{"any call", nil, "Err", "", func(op *openapi.Operation) error { return op.Err }}}},
		// doc.go, Configuration: one usable server selects itself, "and none
		// requires BaseURL".
		{name: "server", key: "server", with: func(o *openapi.Options) { o.BaseURL = orderBase },
			bad: []refusal{{"its sole server", nil, "Settings", "Options.BaseURL", func(op *openapi.Operation) error {
				if len(op.Servers) == 0 {
					return nil
				}
				return op.Servers[0].Err
			}}}},
		// doc.go, Credentials: "FromTransport is what satisfies a scheme a
		// requirement names but the document never declares".
		{name: "security scheme", key: "secure", with: func(o *openapi.Options) {
			o.Credentials = map[string]openapi.Credential{"undeclared": openapi.FromTransport()}
		},
			bad: []refusal{{"without FromTransport", nil, "Settings", `Options.Credentials["undeclared"]`, func(op *openapi.Operation) error {
				if len(op.Security) == 0 || len(op.Security[0].Schemes) == 0 {
					return nil
				}
				return op.Security[0].Schemes[0].Err
			}}}},
	}
}

// An orderOutcome is what a case's calls reported.
type orderOutcome struct {
	sent     *openapi.Operation // OperationFromContext in the transport
	prepared *openapi.Operation // OperationFromContext of Request.HTTP's context
	media    *openapi.Media     // Request.Media
	resp     *openapi.Response  // the response to the call that was sent
	refused  [][2]error         // each refusal's Call and Prepare errors
}

// exercise makes the case's calls on c: the accepted one through Call and
// Prepare, and each refused one through both.
func (cs orderCase) exercise(t *testing.T, c *openapi.Client, rec *opRecorder) orderOutcome {
	t.Helper()
	var o orderOutcome
	if cs.ok != nil {
		cc := c
		if cs.with != nil {
			cc = c.With(cs.with)
		}
		before := len(rec.all())
		resp, err := cc.Call(t.Context(), cs.key, cs.ok, nil)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		ops := rec.all()
		if len(ops) != before+1 {
			t.Fatalf("Call sent %d requests, want 1", len(ops)-before)
		}
		o.sent, o.resp = ops[before], resp
		req, err := cc.Prepare(cs.key, cs.ok)
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		o.prepared, o.media = openapi.OperationFromContext(req.HTTP.Context()), req.Media
	}
	bc := c
	if cs.badWith != nil {
		bc = c.With(cs.badWith)
	}
	for _, b := range cs.bad {
		_, callErr := bc.Call(t.Context(), cs.key, b.in, nil)
		_, prepErr := bc.Prepare(cs.key, b.in)
		o.refused = append(o.refused, [2]error{callErr, prepErr})
	}
	return o
}

// verify checks o against desc, the descriptor describing returns.
func (cs orderCase) verify(t *testing.T, desc *openapi.Operation, o orderOutcome) {
	t.Helper()
	if cs.ok != nil {
		if o.sent != desc {
			t.Errorf("OperationFromContext in the transport = %p; want the described Operation %p", o.sent, desc)
		}
		if o.prepared != desc {
			t.Errorf("OperationFromContext of Request.HTTP = %p; want the described Operation %p", o.prepared, desc)
		}
		var want *openapi.Media
		if cs.body != nil {
			if want = cs.body(desc); want == nil {
				t.Fatalf("%s describes no request body Media", desc.Key)
			}
		}
		if o.media != want {
			t.Errorf("Request.Media = %p; want Operation.Body.Media %p", o.media, want)
		}
		decl := response(t, desc, 0)
		if o.resp.Declaration != decl {
			t.Errorf("Response.Declaration = %p; want Operation.Responses[0] %p", o.resp.Declaration, decl)
		}
		if m := responseMedia(t, desc, 0, 0); o.resp.Media != m {
			t.Errorf("Response.Media = %p; want Declaration.Media[0] %p", o.resp.Media, m)
		}
	}
	for i, b := range cs.bad {
		var want error // nil when no descriptor Err is named: then only the key is checked
		if b.defect != nil {
			if want = b.defect(desc); want == nil {
				t.Fatalf("%s: the descriptor reports no defect", b.name)
			}
		}
		for j, err := range o.refused[i] {
			via := [2]string{"Call", "Prepare"}[j]
			re := asRequestError(t, err)
			var held error
			switch b.field {
			case "Inputs":
				held = re.Inputs[b.key]
			case "Settings":
				held = re.Settings[b.key]
			default:
				held = re.Err
			}
			if held == nil {
				t.Errorf("%s via %s: no %s entry %q; refused with %v", b.name, via, b.field, b.key, err)
			}
			if want != nil && (!errors.Is(err, want) || held != nil && !errors.Is(held, want)) {
				t.Errorf("%s via %s: %v does not wrap the described Err %v (%p)", b.name, via, err, want, want)
			}
		}
	}
}

// findOp returns the operation of ops whose Key is key.
func findOp(t testing.TB, ops []*openapi.Operation, key string) *openapi.Operation {
	t.Helper()
	for _, op := range ops {
		if op.Key == key {
			return op
		}
	}
	t.Fatalf("Operations lists no %q", key)
	return nil
}

// Each case in three orders, each on a fresh Client: Operations, then
// calls; Client.Operation, then calls; calls, then Client.Operation. In
// every order the calls report the described descriptors and their Errs,
// and describing again returns the same Operation.
func TestDescribeOrderSameDescriptors(t *testing.T) {
	for _, cs := range orderCases() {
		for _, order := range []string{"Operations first", "Operation first", "call first"} {
			t.Run(cs.name+"/"+order, func(t *testing.T) {
				c, rec := orderClient(t)
				var desc *openapi.Operation
				switch order {
				case "Operations first":
					desc = findOp(t, c.Operations(), cs.key)
				case "Operation first":
					desc = mustOp(t, c, cs.key)
				}
				o := cs.exercise(t, c, rec)
				if desc == nil {
					desc = mustOp(t, c, cs.key)
				}
				if again := mustOp(t, c, cs.key); again != desc {
					t.Errorf("Client.Operation returned %p, then %p", desc, again)
				}
				if listed := findOp(t, c.Operations(), cs.key); listed != desc {
					t.Errorf("Operations lists %p; Client.Operation returned %p", listed, desc)
				}
				cs.verify(t, desc, o)
			})
		}
	}
}

// groupCase is the case for one Paths entry reaching orderShared.
func groupCase(path string) orderCase {
	return orderCase{
		name: path,
		key:  "POST " + path,
		ok:   &openapi.Input{Params: map[string]any{"r": "x"}, Body: map[string]string{"ok": "x"}},
		body: firstMedia,
		bad: []refusal{
			{"the parameter", &openapi.Input{Params: map[string]any{"q": "x"}, Body: map[string]string{"ok": "x"}}, "Inputs", "q", paramErr},
			{"the field", &openapi.Input{Body: map[string]string{"f": "x"}}, "Inputs", "Input.Body/f", fieldErr},
		},
	}
}

// Two Paths entries reaching one Operation Object through one Path Item
// reference are two operations (describe.go, Operation.Key: "else its
// Method and Path"), each with its own Path and Key. Described and called
// in mixed orders on a fresh Client, each entry's calls report that
// entry's own descriptors and their Errs, as TestDescribeOrderSameDescriptors
// requires of one entry.
func TestDescribeOrderSharedOperationObject(t *testing.T) {
	cases := [2]orderCase{groupCase("/one"), groupCase("/two")}
	entry := map[string]int{"/one": 0, "/two": 1}
	for _, steps := range [][]string{
		{"describe /one", "call /two", "call /one", "describe /two"},
		{"call /one", "describe /two", "call /two", "describe /one"},
		{"describe /one", "describe /two", "call /one", "call /two"},
		{"call /one", "call /two", "describe /one", "describe /two"},
		{"describe /two", "call /one", "describe /one", "call /two"},
	} {
		t.Run(strings.Join(steps, ", "), func(t *testing.T) {
			c, rec := orderClient(t)
			var descs [2]*openapi.Operation
			var outs [2]orderOutcome
			for _, step := range steps {
				action, path, _ := strings.Cut(step, " ")
				i := entry[path]
				if action == "describe" {
					descs[i] = mustOp(t, c, cases[i].key)
				} else {
					outs[i] = cases[i].exercise(t, c, rec)
				}
			}
			for i, cs := range cases {
				desc := descs[i]
				if desc.Path != cs.name || desc.Key != cs.key {
					t.Errorf("entry %d: Path %q Key %q", i, desc.Path, desc.Key)
				}
				if again := mustOp(t, c, cs.key); again != desc {
					t.Errorf("%s: Client.Operation returned %p, then %p", cs.key, desc, again)
				}
				t.Run(cs.key, func(t *testing.T) { cs.verify(t, desc, outs[i]) })
			}
			if descs[0] == descs[1] {
				t.Error("the two entries share one Operation")
			}
		})
	}
}

// Describing and preparing the same operations at once, from several
// goroutines on a fresh Client, every caller sees one Operation per entry
// (describe.go, Operations: "the Operations it points to are shared by every
// caller"; client.go: a Client "is safe for concurrent use"), and every call
// reports it and its descriptors. Run with -race. Several rounds vary the
// interleaving; the checks are grouped by what they compare.
func TestDescribeOrderConcurrent(t *testing.T) {
	const rounds, perKind = 24, 3
	cases := [2]orderCase{groupCase("/one"), groupCase("/two")}
	type observation struct {
		entry   int
		desc    *openapi.Operation // described, or nil
		out     orderOutcome       // a call's, when desc is nil
		hasCall bool
	}
	var all [][]observation
	var clients []*openapi.Client
	for range rounds {
		c, rec := orderClient(t)
		clients = append(clients, c)
		var mu sync.Mutex
		var obs []observation
		var wg sync.WaitGroup
		start := make(chan struct{})
		add := func(o observation) {
			mu.Lock()
			obs = append(obs, o)
			mu.Unlock()
		}
		for entry, cs := range cases {
			for range perKind {
				wg.Add(4)
				go func() {
					defer wg.Done()
					<-start
					for _, op := range c.Operations() {
						if op.Key == cs.key {
							add(observation{entry: entry, desc: op})
							return
						}
					}
					t.Errorf("Operations lists no %q", cs.key)
				}()
				go func() {
					defer wg.Done()
					<-start
					op, err := c.Operation(cs.key)
					if err != nil {
						t.Errorf("Operation(%q): %v", cs.key, err)
						return
					}
					add(observation{entry: entry, desc: op})
				}()
				go func() {
					defer wg.Done()
					<-start
					var o orderOutcome
					req, err := c.Prepare(cs.key, cs.ok)
					if err != nil {
						t.Errorf("Prepare(%q): %v", cs.key, err)
						return
					}
					o.prepared, o.media = openapi.OperationFromContext(req.HTTP.Context()), req.Media
					resp, err := c.Call(t.Context(), cs.key, cs.ok, nil)
					if err != nil {
						t.Errorf("Call(%q): %v", cs.key, err)
						return
					}
					o.resp = resp
					add(observation{entry: entry, out: o, hasCall: true})
				}()
				go func() {
					defer wg.Done()
					<-start
					var o orderOutcome
					for _, b := range cs.bad {
						_, prepErr := c.Prepare(cs.key, b.in)
						_, callErr := c.Call(t.Context(), cs.key, b.in, nil)
						o.refused = append(o.refused, [2]error{callErr, prepErr})
					}
					add(observation{entry: entry, out: o})
				}()
			}
		}
		close(start)
		wg.Wait()
		// What OperationFromContext reported in the transport.
		for _, op := range rec.all() {
			obs = append(obs, observation{entry: -1, desc: op})
		}
		all = append(all, obs)
	}

	// descriptor returns entry's Operation in round r, as describing it
	// after every goroutine returned.
	descriptor := func(t *testing.T, r, entry int) *openapi.Operation {
		return mustOp(t, clients[r], cases[entry].key)
	}
	// each runs check on every observation of each round, a subtest per
	// round.
	each := func(t *testing.T, check func(t *testing.T, r int, o observation)) {
		for r, obs := range all {
			t.Run(fmt.Sprintf("round %d", r), func(t *testing.T) {
				for _, o := range obs {
					check(t, r, o)
				}
			})
		}
	}
	t.Run("one Operation per entry", func(t *testing.T) {
		each(t, func(t *testing.T, r int, o observation) {
			switch {
			case o.entry >= 0 && o.desc != nil:
				if want := descriptor(t, r, o.entry); o.desc != want {
					t.Errorf("%s: described %p and %p", cases[o.entry].key, o.desc, want)
				}
			case o.entry < 0: // sent: its Path tells the entry
				i := slices.IndexFunc(cases[:], func(cs orderCase) bool { return o.desc != nil && o.desc.Path == cs.name })
				if i < 0 {
					t.Errorf("OperationFromContext in the transport = %+v; want one of the entries", o.desc)
				} else if want := descriptor(t, r, i); o.desc != want {
					t.Errorf("%s: OperationFromContext in the transport = %p; want the described Operation %p", cases[i].key, o.desc, want)
				}
			}
		})
	})
	t.Run("calls report the descriptors", func(t *testing.T) {
		each(t, func(t *testing.T, r int, o observation) {
			if !o.hasCall {
				return
			}
			desc := descriptor(t, r, o.entry)
			o.out.sent = desc // the transport's are checked with the descriptors
			cs := cases[o.entry]
			cs.bad = nil
			cs.verify(t, desc, o.out)
		})
	})
	for k, kind := range []string{"parameter Err", "field Err"} {
		t.Run("refusals wrap the described "+kind, func(t *testing.T) {
			each(t, func(t *testing.T, r int, o observation) {
				if o.hasCall || o.desc != nil || o.entry < 0 {
					return
				}
				cs := cases[o.entry]
				cs.ok, cs.bad = nil, cs.bad[k:k+1]
				cs.verify(t, descriptor(t, r, o.entry), orderOutcome{refused: o.out.refused[k : k+1]})
			})
		})
	}
}
