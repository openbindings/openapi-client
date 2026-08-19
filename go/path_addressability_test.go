package openapiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// §9.3 (OAPI-P-05): the target URL is the resolved server joined with the
// operation's path template, so a template variable no declared path parameter
// can supply leaves no target to address. The artifact is invalid under every
// accepted OAS edition — a path template variable MUST have a corresponding
// `in: path` parameter — and the refusal must precede dispatch rather than
// putting a percent-encoded `%7Bname%7D` segment on a live service.
//
// The corpus original is spree/spree's `/api/v2/storefront/policies/{policy_slug}`
// `show-policy`, which declares no `parameters` at all.
func TestInvokeRefusesUnaddressablePathTemplateVariableBeforeDispatch(t *testing.T) {
	dispatched := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dispatched <- request.URL.RequestURI()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	document := testDocument(server.URL, `{
  "/api/v2/storefront/policies/{policy_slug}":{"get":{"operationId":"show-policy",
    "responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}
}`)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), OperationID("show-policy"), Input{}); err == nil {
		t.Fatal("call succeeded, want a pre-dispatch refusal")
	} else if !strings.Contains(err.Error(), `path template variable(s) policy_slug have no declared path parameter`) {
		t.Fatalf("refusal = %q, want it to name policy_slug", err.Error())
	}
	// Supplying the variable as an ordinary field does not manufacture the
	// missing declaration either: the artifact, not the input, owns the defect.
	if _, err := client.Call(context.Background(), OperationID("show-policy"), Input{
		Parameters: Parameters{Path: map[string]any{"policy_slug": "returns"}},
	}); err == nil {
		t.Fatal("call with a supplied value succeeded, want the same refusal")
	}
	select {
	case target := <-dispatched:
		t.Fatalf("refused call still put %q on the wire", target)
	default:
	}
}

// Every declared variable must still be named, and the report is ordered so
// two conformant runs agree.
func TestInvokeNamesEveryUnaddressablePathTemplateVariable(t *testing.T) {
	document := testDocument("http://addressability.invalid", `{
  "/{tenant}/reports/{report_id}/{section}":{"get":{"operationId":"showSection",
    "parameters":[{"name":"report_id","in":"path","required":true,"schema":{"type":"string"}}],
    "responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}
}`)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Call(context.Background(), OperationID("showSection"), Input{
		Parameters: Parameters{Path: map[string]any{"report_id": "r1"}},
	})
	if err == nil {
		t.Fatal("call succeeded, want a pre-dispatch refusal")
	}
	if !strings.Contains(err.Error(), "path template variable(s) section, tenant have no declared path parameter") {
		t.Fatalf("refusal = %q, want both undeclared variables in code-point order", err.Error())
	}
}

// The refusal reaches exactly the unaddressable case. Every declaration that
// CAN supply its variable still dispatches, and a brace that delimits no
// expression is an ordinary path literal.
func TestInvokeKeepsAddressablePathTemplatesDispatchable(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		path    string
		paths   string
		input   Input
		wantURI string
	}{
		{
			name:    "operation-level declaration",
			paths:   `{"/items/{id}":{"get":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}`,
			input:   Input{Parameters: Parameters{Path: map[string]any{"id": "42"}}},
			wantURI: "/items/42",
		},
		{
			name:    "path-item-level declaration",
			paths:   `{"/items/{id}":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],"get":{"operationId":"op","responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}`,
			input:   Input{Parameters: Parameters{Path: map[string]any{"id": "42"}}},
			wantURI: "/items/42",
		},
		{
			name:    "referenced declaration",
			paths:   `{"/items/{id}":{"get":{"operationId":"op","parameters":[{"$ref":"#/components/parameters/ItemID"}],"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}`,
			input:   Input{Parameters: Parameters{Path: map[string]any{"id": "42"}}},
			wantURI: "/items/42",
		},
		{
			// A lone brace delimits no expression, so there is no variable to
			// address and nothing to refuse: the segment is a path literal.
			name:    "unpaired brace is a literal",
			paths:   `{"/items/a{b":{"get":{"operationId":"op","responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}`,
			wantURI: "/items/a%7Bb",
		},
		{
			name:    "unpaired closing brace is a literal",
			paths:   `{"/items/a}b":{"get":{"operationId":"op","responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}`,
			wantURI: "/items/a%7Db",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var observed string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				observed = request.URL.EscapedPath()
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			document := []byte(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},` +
				`"servers":[{"url":"` + server.URL + `"}],` +
				`"components":{"parameters":{"ItemID":{"name":"id","in":"path","required":true,"schema":{"type":"string"}}}},` +
				`"paths":` + testCase.paths + `}`)
			client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Call(context.Background(), OperationID("op"), testCase.input)
			if err != nil {
				t.Fatal(err)
			}
			if !result.OK {
				t.Fatalf("result = %#v", result)
			}
			if observed != testCase.wantURI {
				t.Fatalf("request target = %q, want %q", observed, testCase.wantURI)
			}
		})
	}
}

// The neighbouring §9.1 case is untouched: a DECLARED path parameter the
// caller omitted keeps its own refusal, which states the missing INPUT rather
// than an artifact defect. Both spellings of it survive — the required
// declaration refuses on bare close, the (OAS-invalid but tolerated) optional
// one refuses while routing.
func TestInvokeStillRefusesOmittedDeclaredPathParameter(t *testing.T) {
	for _, testCase := range []struct{ name, required, want string }{
		{name: "required", required: `,"required":true`, want: `operation requires parameter "id"`},
		{name: "optional", required: "", want: "missing path parameter(s) id"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := testDocument("http://addressability.invalid", `{
  "/items/{id}":{"get":{"operationId":"op",
    "parameters":[{"name":"id","in":"path"`+testCase.required+`,"schema":{"type":"string"}}],
    "responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}
}`)
			client, err := Load(context.Background(), Source{Content: document}, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Call(context.Background(), OperationID("op"), Input{})
			if err == nil {
				t.Fatal("call succeeded, want a pre-dispatch refusal")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("refusal = %q, want it to contain %q", err.Error(), testCase.want)
			}
		})
	}
}

func TestPathTemplateVariablesReadsBraceDelimitedExpressions(t *testing.T) {
	for _, testCase := range []struct {
		template string
		want     []string
	}{
		{template: "/items", want: nil},
		{template: "/items/{id}", want: []string{"id"}},
		{template: "/{tenant}/items/{id}", want: []string{"tenant", "id"}},
		{template: "/items/{id}.{format}", want: []string{"id", "format"}},
		{template: "/items/a{b", want: nil},
		{template: "/items/a}b", want: nil},
		{template: "/items/{{id}", want: []string{"id"}},
		{template: "/items/{}", want: []string{""}},
	} {
		if got := pathTemplateVariables(testCase.template); !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("pathTemplateVariables(%q) = %#v, want %#v", testCase.template, got, testCase.want)
		}
	}
}
