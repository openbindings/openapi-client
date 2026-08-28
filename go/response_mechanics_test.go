package openapiclient

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestContentCodingStacksAndResponseGovernance(t *testing.T) {
	document, _, err := loadDocument(context.Background(), nil, Source{Content: []byte(`{
      "openapi":"3.1.1","info":{"title":"t","version":"1"},
      "paths":{"/x":{"post":{
        "parameters":[{"name":"Content-Encoding","in":"header","schema":{"type":"string","enum":["first, second"]}}],
        "responses":{"200":{"description":"ok","headers":{
          "Content-Encoding":{"required":true,"schema":{"type":"string","enum":["first, second"]}},
          "X-Required":{"required":true,"schema":{"type":"string"}}
        },"content":{"text/plain":{"schema":{"type":"string"}}}}}
      }}}}`)}, false)
	if err != nil {
		t.Fatal(err)
	}
	operation := document.Paths.Find("/x").Post
	request, _ := http.NewRequest(http.MethodPost, "https://example.test/x", strings.NewReader("payload"))
	request.Header.Set("Content-Encoding", "first, second")
	encoders := map[string]ContentEncoder{
		"first":  func(value []byte) ([]byte, error) { return []byte("first(" + string(value) + ")"), nil },
		"second": func(value []byte) ([]byte, error) { return []byte("second(" + string(value) + ")"), nil },
	}
	if err := applyRequestContentCodings(request, operation.Parameters, document.OpenAPI, encoders); err != nil {
		t.Fatal(err)
	}
	encoded, _ := io.ReadAll(request.Body)
	if string(encoded) != "second(first(payload))" {
		t.Fatalf("encoded request = %q", encoded)
	}

	order := []string{}
	decoders := map[string]ContentDecoder{
		"first": func(value []byte) ([]byte, error) {
			order = append(order, "first")
			return unwrapContentCoding(value, "first")
		},
		"second": func(value []byte) ([]byte, error) {
			order = append(order, "second")
			return unwrapContentCoding(value, "second")
		},
	}
	response := &http.Response{StatusCode: 200, Header: http.Header{
		"Content-Type": {"text/plain"}, "Content-Encoding": {"first, second"}, "X-Required": {"yes"},
	}, Body: io.NopCloser(strings.NewReader("second(first(payload))")), Request: request}
	response, err = applyResponseMechanics(request, response, document, operation, profileFullCoordinate, decoders, true)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := io.ReadAll(response.Body)
	if string(decoded) != "payload" || !reflect.DeepEqual(order, []string{"second", "first"}) {
		t.Fatalf("decoded response/order = %q, %v", decoded, order)
	}
}

func TestHEADResponseIsEmptyAndRequiredHeadersAreGoverned(t *testing.T) {
	document, _, err := loadDocument(context.Background(), nil, Source{Content: []byte(`{
      "openapi":"3.1.1","info":{"title":"t","version":"1"},
      "paths":{"/x":{"head":{"responses":{"200":{"description":"ok","headers":{"X-Required":{"required":true,"schema":{"type":"string"}}},"content":{"application/octet-stream":{}}}}}}}}`)}, false)
	if err != nil {
		t.Fatal(err)
	}
	operation := document.Paths.Find("/x").Head
	request, _ := http.NewRequest(http.MethodHead, "https://example.test/x", nil)
	missing := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ignored")), Request: request}
	if _, err := applyResponseMechanics(request, missing, document, operation, profileFullCoordinate, nil, true); err == nil || !strings.Contains(err.Error(), "X-Required") {
		t.Fatalf("required response header error = %v", err)
	}
	response := &http.Response{StatusCode: 200, Header: http.Header{"X-Required": {"yes"}}, Body: io.NopCloser(strings.NewReader("ignored")), Request: request}
	governed, err := applyResponseMechanics(request, response, document, operation, profileFullCoordinate, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(governed.Body)
	if len(body) != 0 {
		t.Fatalf("HEAD response body = %q", body)
	}
}

func unwrapContentCoding(value []byte, name string) ([]byte, error) {
	prefix, suffix := name+"(", ")"
	if !strings.HasPrefix(string(value), prefix) || !strings.HasSuffix(string(value), suffix) {
		return nil, io.ErrUnexpectedEOF
	}
	return value[len(prefix) : len(value)-len(suffix)], nil
}
