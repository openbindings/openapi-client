package openapiclient

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAPI32ResponseContentCodingStackUsesGoverningHeaderAndReverseOrder(t *testing.T) {
	order := []string{}
	decoders := map[string]ContentDecoder{
		"FIRST": func(value []byte) ([]byte, error) {
			order = append(order, "first")
			return unwrapContentCoding(value, "first")
		},
		"second": func(value []byte) ([]byte, error) {
			order = append(order, "second")
			return unwrapContentCoding(value, "second")
		},
	}
	transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
		StatusCode: 200, Status: "200 OK", Header: http.Header{
			"Content-Type":     {"text/plain"},
			"Content-Encoding": {"first, second"},
		}, Body: io.NopCloser(strings.NewReader("second(first(payload))")),
	}}}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response codings, version: "1"}
servers: [{url: https://api.example}]
paths:
  /x:
    get:
      responses:
        '200':
          headers:
            Content-Encoding: {required: true, schema: {type: string, enum: ['first, second']}}
          content:
            text/plain: {schema: {type: string}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}, ResponseContentCodings: decoders})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), PathOperation("/x", GET), Input{})
	if err != nil || !result.OK || result.Data != "payload" {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	if !reflect.DeepEqual(order, []string{"second", "first"}) {
		t.Fatalf("decoder order = %v", order)
	}
}

func TestOpenAPI32ResponseContentCodingRefusalsAndIdentity(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		headerDecl  string
		actual      string
		body        string
		decoders    map[string]ContentDecoder
		want        string
		wantErrPart string
	}{
		{
			name: "unsupported token", headerDecl: `Content-Encoding: {schema: {type: string, enum: [zip]}}`,
			actual: "zip", body: "payload", wantErrPart: `content-coding "zip" is unsupported`,
		},
		{
			name: "undeclared coding", actual: "zip", body: "payload",
			decoders:    map[string]ContentDecoder{"zip": func(value []byte) ([]byte, error) { return value, nil }},
			wantErrPart: "no governing Header Object",
		},
		{
			name: "unadmitted value", headerDecl: `Content-Encoding: {schema: {type: string, enum: [gzip]}}`,
			actual: "zip", body: "payload",
			decoders:    map[string]ContentDecoder{"zip": func(value []byte) ([]byte, error) { return value, nil }},
			wantErrPart: "not admitted",
		},
		{
			name: "identity", headerDecl: `Content-Encoding: {schema: {type: string, enum: [identity]}}`,
			actual: "identity", body: "payload", want: "payload",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			headers := ""
			if testCase.headerDecl != "" {
				headers = "headers:\n            " + testCase.headerDecl + "\n          "
			}
			transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
				StatusCode: 200, Status: "200 OK", Header: http.Header{
					"Content-Type": {"text/plain"}, "Content-Encoding": {testCase.actual},
				}, Body: io.NopCloser(strings.NewReader(testCase.body)),
			}}}
			client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response coding case, version: "1"}
servers: [{url: https://api.example}]
paths:
  /x:
    get:
      responses:
        '200':
          ` + headers + `content:
            text/plain: {schema: {type: string}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}, ResponseContentCodings: testCase.decoders})
			if err != nil {
				t.Fatal(err)
			}
			result, callErr := client.Call(context.Background(), PathOperation("/x", GET), Input{})
			if testCase.wantErrPart != "" {
				if callErr == nil || !strings.Contains(callErr.Error(), testCase.wantErrPart) {
					t.Fatalf("error = %v, want %q", callErr, testCase.wantErrPart)
				}
				return
			}
			if callErr != nil || result == nil || result.Data != testCase.want {
				t.Fatalf("result = %#v, err %v", result, callErr)
			}
		})
	}
}

func TestOpenAPI32ResponseContentCodingHeaderDeclarationCollisionIsLoud(t *testing.T) {
	transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
		StatusCode: 200, Status: "200 OK", Header: http.Header{
			"Content-Type": {"text/plain"}, "Content-Encoding": {"identity"},
		}, Body: io.NopCloser(strings.NewReader("payload")),
	}}}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response coding collision, version: "1"}
servers: [{url: https://api.example}]
paths:
  /x:
    get:
      responses:
        '200':
          headers:
            Content-Encoding: {schema: {type: string}}
            content-encoding: {schema: {type: string}}
          content:
            text/plain: {schema: {type: string}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), PathOperation("/x", GET), Input{}); err == nil || !strings.Contains(err.Error(), "no governing Header Object") {
		t.Fatalf("collision error = %v", err)
	}
}

func TestOpenAPI32ResponseContentCodingCapabilityCollisionRefusesBeforeDispatch(t *testing.T) {
	transport := &openAPI32ResponseTransport{}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: response coding capability collision, version: "1"}
servers: [{url: https://api.example}]
paths:
  /x:
    get:
      responses:
        '204': {}
`)}, ClientOptions{
		HTTPClient: &http.Client{Transport: transport},
		ResponseContentCodings: map[string]ContentDecoder{
			"zip": func(value []byte) ([]byte, error) { return value, nil },
			"ZIP": func(value []byte) ([]byte, error) { return value, nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), PathOperation("/x", GET), Input{}); err == nil || !strings.Contains(err.Error(), `collide at "zip"`) {
		t.Fatalf("capability collision error = %v", err)
	}
	if transport.requests != 0 {
		t.Fatalf("transport requests = %d, want 0", transport.requests)
	}
}
