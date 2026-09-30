package openapi_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Server selection and the URL rule.

// doc.go, Fixed rules, URL: "server variables are substituted first, as
// given. A relative result then resolves, by RFC 3986 section 5.2, against
// the URI of the document that contains the Server Object ... The path is
// then appended as written, except that one "/" is dropped where the URL
// ends with one and the path begins with one."
func TestServerURLRule(t *testing.T) {
	w := newWire(t, nil)
	docURI := w.URL + "/specs/openapi.json"
	tests := []struct {
		name   string
		server string // the Server Object
		want   string // the request target for the path /pets
	}{
		{"origin", `{"url":"@BASE@"}`, "/pets"},
		{"origin slash", `{"url":"@BASE@/"}`, "/pets"},
		{"path", `{"url":"@BASE@/v1"}`, "/v1/pets"},
		{"path slash", `{"url":"@BASE@/v1/"}`, "/v1/pets"},
		// Only one "/" is dropped.
		{"double slash", `{"url":"@BASE@/v1//"}`, "/v1//pets"},
		// RFC 3986 section 5.2 against .../specs/openapi.json.
		{"absolute-path reference", `{"url":"/v1"}`, "/v1/pets"},
		{"relative-path reference", `{"url":"v1"}`, "/specs/v1/pets"},
		{"dot-dot", `{"url":"../v1"}`, "/v1/pets"},
		{"dot", `{"url":"."}`, "/specs/pets"},
		{"network-path reference", `{"url":"//@HOSTPORT@/net"}`, "/net/pets"},
		// Variables are substituted as given, before resolution.
		{"variable default", `{"url":"@BASE@/{v}","variables":{"v":{"default":"v2"}}}`, "/v2/pets"},
		{"variable with slash", `{"url":"@BASE@/{base}","variables":{"base":{"default":"api/v2"}}}`, "/api/v2/pets"},
		{"variable scheme", `{"url":"{scheme}://@HOSTPORT@/x","variables":{"scheme":{"default":"http"}}}`, "/x/pets"},
		{"relative variable", `{"url":"/{v}/","variables":{"v":{"default":"v3"}}}`, "/v3/pets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[` + tt.server + `],
				"paths":{"/pets":{"get":{"operationId":"listPets"}}}}`
			c := parseAt(t, doc, w.URL, docURI, nil)
			before := w.count()
			mustCall(t, c, "listPets", nil, nil)
			if w.count() != before+1 {
				t.Fatalf("no request reached the server")
			}
			if got := w.last(t).RequestURI; got != tt.want {
				t.Errorf("request target %q, want %q", got, tt.want)
			}
			// client.go, Request.HTTP: the prepared request holds the URL.
			req := mustPrepare(t, c, "listPets", nil)
			if got := req.HTTP.URL.String(); got != w.URL+tt.want {
				t.Errorf("prepared URL %q, want %q", got, w.URL+tt.want)
			}
		})
	}
}

// doc.go, Configuration: "One usable server selects itself. Several require
// Options.Server, Options.ServerID or Options.BaseURL, and none requires
// BaseURL." A server that cannot be used (doc.go, Fixed rules, URL) is not
// a usable one.
func TestServerSelection(t *testing.T) {
	w := newWire(t, nil)
	two := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"@BASE@/a"},{"url":"@BASE@/b"}],
		"paths":{"/pets":{"get":{"operationId":"listPets"}}}}`

	c := parseFor(t, w, two, nil)
	resp, err := c.Call(t.Context(), "listPets", nil, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantAnyKey(t, "Settings", re.Settings, "Options.Server", "Options.ServerID", "Options.BaseURL")

	tests := []struct {
		name string
		opts func(*openapi.Options)
		want string
	}{
		// client.go, Options.Server: "by its URL as written (with its
		// {variables})".
		{"Server", func(o *openapi.Options) { o.Server = w.URL + "/b" }, "/b/pets"},
		// client.go, Options.ServerID: "an exact Server.ID from
		// Operation.Servers".
		{"ServerID", func(o *openapi.Options) { o.ServerID = mustOp(t, c, "listPets").Servers[0].ID }, "/a/pets"},
		// client.go, Options.BaseURL: "replaces every server of every
		// operation ... joined to the operation's path".
		{"BaseURL", func(o *openapi.Options) { o.BaseURL = w.URL + "/base/" }, "/base/pets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := w.count()
			mustCall(t, c.With(tt.opts), "listPets", nil, nil)
			if w.count() != before+1 {
				t.Fatalf("no request reached the server")
			}
			if got := w.last(t).RequestURI; got != tt.want {
				t.Errorf("request target %q, want %q", got, tt.want)
			}
		})
	}

	// Only one server is usable: it selects itself.
	oneUsable := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"http://user@@HOSTPORT@/bad"},{"url":"@BASE@/good"}],
		"paths":{"/pets":{"get":{"operationId":"listPets"}}}}`
	before := w.count()
	mustCall(t, parseFor(t, w, oneUsable, nil), "listPets", nil, nil)
	if w.count() != before+1 || w.last(t).RequestURI != "/good/pets" {
		t.Errorf("the one usable server was not selected")
	}

	// None usable requires BaseURL.
	noneUsable := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"http://user@@HOSTPORT@/bad"},{"url":"@BASE@/v1?x=1"},{"url":"@BASE@/v1#f"}],
		"paths":{"/pets":{"get":{"operationId":"listPets"}}}}`
	before = w.count()
	resp, err = parseFor(t, w, noneUsable, nil).Call(t.Context(), "listPets", nil, nil)
	re = asRequestError(t, err)
	if resp != nil || w.count() != before {
		t.Errorf("a call with no usable server was sent")
	}
	wantKeys(t, "Settings", re.Settings, false, "Options.BaseURL")
}

// doc.go, Configuration: "Options.Server and Options.ServerID refuse such an
// operation too, since falling back would change where credentials go";
// client.go, Options.Server: "a call to an operation that does not list it
// is refused". OAS 3.1.2 section 4.8.10.1: an operation's servers override
// the path's and the root's.
func TestServerOverrideLevels(t *testing.T) {
	w := newWire(t, nil)
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"@BASE@/root"}],
		"paths":{
			"/r":{"get":{"operationId":"root"}},
			"/p":{"servers":[{"url":"@BASE@/path"}],"get":{"operationId":"path"},
				"put":{"operationId":"op","servers":[{"url":"@BASE@/op"}]},
				"post":{"operationId":"empty","servers":[]}}
		}}`
	c := parseFor(t, w, doc, nil)
	for key, want := range map[string]string{
		"root":  "/root/r",
		"path":  "/path/p",
		"op":    "/op/p",
		"empty": "/path/p", // an empty array is read as absent
	} {
		mustCall(t, c, key, nil, nil)
		if got := w.last(t).RequestURI; got != want {
			t.Errorf("%s sent to %q, want %q", key, got, want)
		}
	}

	pinned := c.With(func(o *openapi.Options) { o.Server = w.URL + "/root" })
	mustCall(t, pinned, "root", nil, nil)
	before := w.count()
	resp, err := pinned.Call(t.Context(), "op", nil, nil)
	re := asRequestError(t, err)
	if resp != nil || w.count() != before {
		t.Errorf("an operation that does not offer Options.Server was sent")
	}
	wantKeys(t, "Settings", re.Settings, false, "Options.Server")

	id := mustOp(t, c, "root").Servers[0].ID
	resp, err = c.With(func(o *openapi.Options) { o.ServerID = id }).Call(t.Context(), "op", nil, nil)
	re = asRequestError(t, err)
	if resp != nil || w.count() != before {
		t.Errorf("an operation that does not offer Options.ServerID was sent")
	}
	wantKeys(t, "Settings", re.Settings, false, "Options.ServerID")
}

// client.go, Options.Server: "A value that matches two servers ... is
// refused as ambiguous; use ServerID to name one exactly." Options.ServerID:
// "IDs tell apart authored servers with the same URL or name."
func TestServerIDDisambiguates(t *testing.T) {
	w := newWire(t, nil)
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[
			{"url":"@BASE@/{v}","variables":{"v":{"default":"one"}}},
			{"url":"@BASE@/{v}","variables":{"v":{"default":"two"}}}
		],
		"paths":{"/pets":{"get":{"operationId":"listPets"}}}}`
	c := parseFor(t, w, doc, nil)
	servers := mustOp(t, c, "listPets").Servers
	if len(servers) != 2 || servers[0].ID == servers[1].ID {
		t.Fatalf("Servers = %+v, want two with distinct IDs", servers)
	}

	byURL := c.With(func(o *openapi.Options) { o.Server = w.URL + "/{v}" })
	resp, err := byURL.Call(t.Context(), "listPets", nil, nil)
	refusedBeforeSending(t, w, resp, err)

	mustCall(t, c.With(func(o *openapi.Options) { o.ServerID = servers[1].ID }), "listPets", nil, nil)
	if got := w.only(t).RequestURI; got != "/two/pets" {
		t.Errorf("request target %q, want /two/pets", got)
	}
}

// client.go, Options.Variables: "A variable without a value takes its
// declared default ... The enum limits other values: one outside it refuses
// the call, and an empty enum permits only the default ... Values are
// substituted as given." describe.go, Variable: a declared variable without
// a default, or an undeclared {name}, needs Options.Variables. Refusals are
// keyed Options.Variables["name"] (errors.go, RequestError.Settings).
func TestServerVariables(t *testing.T) {
	w := newWire(t, nil)
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"@BASE@/{region}/{version}/{tenant}/{plan}/{free}","variables":{
			"region":{"default":"us","enum":["us","eu"]},
			"version":{"default":"v1"},
			"tenant":{"description":"no default"},
			"plan":{"default":"basic","enum":[]}
		}}],
		"paths":{"/pets":{"get":{"operationId":"listPets"}}}}`
	c := parseFor(t, w, doc, nil)
	key := func(name string) string { return fmt.Sprintf("Options.Variables[%q]", name) }

	tests := []struct {
		name    string
		vars    map[string]string
		want    string   // the request target, or ""
		refused []string // Settings keys when refused
	}{
		{"defaults and given", map[string]string{"tenant": "acme", "free": "f"}, "/us/v1/acme/basic/f/pets", nil},
		{"override", map[string]string{"region": "eu", "version": "v2", "tenant": "t", "free": "x"}, "/eu/v2/t/basic/x/pets", nil},
		{"outside enum", map[string]string{"region": "ap", "tenant": "t", "free": "x"}, "", []string{key("region")}},
		{"empty enum permits the default", map[string]string{"plan": "basic", "tenant": "t", "free": "x"}, "/us/v1/t/basic/x/pets", nil},
		{"empty enum refuses another", map[string]string{"plan": "pro", "tenant": "t", "free": "x"}, "", []string{key("plan")}},
		{"no default", map[string]string{"free": "x"}, "", []string{key("tenant")}},
		{"undeclared", map[string]string{"tenant": "t"}, "", []string{key("free")}},
		{"both missing", nil, "", []string{key("tenant"), key("free")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := c.With(func(o *openapi.Options) {
				for k, v := range tt.vars {
					o.Variables[k] = v
				}
			})
			before := w.count()
			resp, err := d.Call(t.Context(), "listPets", nil, nil)
			if tt.refused != nil {
				re := asRequestError(t, err)
				if resp != nil || w.count() != before {
					t.Errorf("refused call was sent")
				}
				wantKeys(t, "Settings", re.Settings, false, tt.refused...)
				return
			}
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if got := w.last(t).RequestURI; got != tt.want {
				t.Errorf("request target %q, want %q", got, tt.want)
			}
		})
	}
}

// load.go, Load: "Load also fails, with a *RequestError, on Options the
// document cannot use: ... a Variables name no server URL uses, a MediaType
// no operation declares, a Codecs key Options.Codecs refuses, a Server or
// ServerID that matches no server, ... a BaseURL without a scheme and host,
// with userinfo, a query or a fragment, or set with Server or ServerID,
// conflicting exact and name selectors, or a Header field that is always
// refused." Settings is keyed "by the Go setting that fixes it"
// (errors.go).
func TestLoadRefusesOptions(t *testing.T) {
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"https://api.example.test/{v}","variables":{"v":{"default":"v1"}}},{"url":"https://other.example.test"}],
		"paths":{"/pets":{"post":{"operationId":"createPet","requestBody":{"content":{"application/json":{}}}}}}}`
	tests := []struct {
		name string
		opts openapi.Options
		key  string // the Settings key, or "" to check only the type
	}{
		{"Server matches none", openapi.Options{Server: "https://nowhere.example.test"}, "Options.Server"},
		{"ServerID matches none", openapi.Options{ServerID: "no-such-id"}, "Options.ServerID"},
		{"Variables name unused", openapi.Options{Variables: map[string]string{"nope": "x"}}, `Options.Variables["nope"]`},
		{"MediaType undeclared", openapi.Options{MediaType: "application/xml"}, "Options.MediaType"},
		{"BaseURL relative", openapi.Options{BaseURL: "/v1"}, "Options.BaseURL"},
		{"BaseURL without scheme", openapi.Options{BaseURL: "api.example.test/v1"}, "Options.BaseURL"},
		{"BaseURL without host", openapi.Options{BaseURL: "https:///v1"}, "Options.BaseURL"},
		{"BaseURL userinfo", openapi.Options{BaseURL: "https://user:pw@api.example.test"}, "Options.BaseURL"},
		{"BaseURL query", openapi.Options{BaseURL: "https://api.example.test/v1?x=1"}, "Options.BaseURL"},
		{"BaseURL fragment", openapi.Options{BaseURL: "https://api.example.test/v1#f"}, "Options.BaseURL"},
		// Which of the conflicting settings is keyed is not stated.
		{"BaseURL with Server", openapi.Options{BaseURL: "https://api.example.test", Server: "https://other.example.test"}, ""},
		{"BaseURL with ServerID", openapi.Options{BaseURL: "https://api.example.test", ServerID: "x"}, ""},
		{"Server with ServerID", openapi.Options{Server: "https://other.example.test", ServerID: "x"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &opts)
			if c != nil {
				t.Errorf("Parse returned a Client with Options it refuses")
			}
			re := asRequestError(t, err)
			if tt.key != "" {
				wantKeys(t, "Settings", re.Settings, false, tt.key)
				if !strings.Contains(re.Error(), tt.key) {
					t.Errorf("error text %q does not name %s", re.Error(), tt.key)
				}
			}
		})
	}

	// The same Options that match are accepted.
	for name, opts := range map[string]openapi.Options{
		"Server":                    {Server: "https://other.example.test"},
		"Server URL with variables": {Server: "https://api.example.test/{v}"},
		"Variables":                 {Variables: map[string]string{"v": "v2"}},
		"MediaType":                 {MediaType: "application/json"},
		"BaseURL":                   {BaseURL: "https://api.example.test/v9/"},
	} {
		if _, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &opts); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
