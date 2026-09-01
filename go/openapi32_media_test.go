package openapiclient

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAPI32SequentialAndNonJSONTextRequests(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: sequential requests, version: "1"}
servers: [{url: https://api.example}]
paths:
  /jsonl:
    post:
      requestBody:
        required: true
        content:
          application/jsonl:
            itemSchema: {type: object}
      responses: {'204': {description: ok, content: {application/json: {schema: {type: object}}}}}
  /json-seq:
    post:
      requestBody:
        required: true
        content:
          application/problem+json-seq:
            schema: {type: array}
            itemSchema: {type: [boolean, number]}
      responses: {'204': {description: ok}}
  /text:
    post:
      requestBody:
        required: true
        content:
          text/plain; charset=utf-8:
            schema: {type: [boolean, number]}
      responses: {'204': {description: ok}}
  /range-text:
    post:
      requestBody:
        required: true
        content:
          text/*:
            schema: {type: [boolean, number]}
      responses: {'204': {description: ok}}
  /range-jsonl:
    post:
      requestBody:
        required: true
        content:
          application/*:
            itemSchema: {type: object}
      responses: {'204': {description: ok}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}

	for _, call := range []struct {
		path      string
		body      any
		mediaType string
		want      []byte
	}{
		{"/jsonl", []any{map[string]any{"n": 1}, map[string]any{"n": 2}}, "application/jsonl", []byte("{\"n\":1}\n{\"n\":2}\n")},
		{"/json-seq", []any{true, json.Number("12.50")}, "application/problem+json-seq", []byte("\x1etrue\n\x1e12.50\n")},
		{"/text", json.Number("1000.00"), "text/plain; charset=utf-8", []byte("1e3")},
		{"/range-text", true, "text/plain", []byte("true")},
		{"/range-jsonl", []any{map[string]any{"n": 1}}, "application/jsonl", []byte("{\"n\":1}\n")},
	} {
		if _, err := client.Call(context.Background(), PathOperation(call.path, POST), Input{BodyPresent: true, Body: call.body, MediaType: call.mediaType}); err != nil {
			t.Fatalf("%s: %v", call.path, err)
		}
		index := len(transport.bodies) - 1
		if got := transport.bodies[index]; !reflect.DeepEqual(got, call.want) {
			t.Errorf("%s body = %q, want %q", call.path, got, call.want)
		}
		if got := transport.requests[index].Header.Get("Accept"); got != "" {
			t.Errorf("%s Accept = %q, want omitted", call.path, got)
		}
	}
}

func TestNormalizeOpenAPI32JSONNumber(t *testing.T) {
	for input, want := range map[string]string{
		"-0": "0", "1000.00": "1e3", "1.2300e+03": "1230",
		"0.000001": "1e-6", "1e100000000000000000000": "1e100000000000000000000",
	} {
		got, err := normalizeOpenAPI32JSONNumber(input)
		if err != nil || got != want {
			t.Errorf("normalize(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

// R5 (ratified 2026-09-01): contentEncoding never produces a
// Content-Transfer-Encoding header. OAS 3.2.0 §4.15.4.2 and RFC 7578 §4.7
// ("Senders SHOULD NOT generate any parts with a Content-Transfer-Encoding
// header field") both forbid the emission.
func TestOpenAPI32PositionalMultipartEncodingOmitsTransferHeader(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: positional multipart, version: "1"}
servers: [{url: https://api.example}]
components:
  schemas:
    PositionalItem: {type: string, contentEncoding: base64}
  mediaTypes:
    Parts:
      itemSchema: {$ref: '#/components/schemas/PositionalItem'}
      prefixEncoding:
        - contentType: application/octet-stream
          headers:
            Content-Disposition: {schema: {type: string, const: 'form-data; name="first"'}}
            X-Part: {schema: {type: string, enum: [prefix]}}
            Content-Type: {schema: {type: string, const: text/ignored}}
      itemEncoding:
        contentType: application/octet-stream
        headers:
          Content-Disposition: {schema: {type: string, const: 'form-data; name="rest"'}}
paths:
  /parts:
    post:
      requestBody:
        required: true
        content:
          multipart/form-data: {$ref: '#/components/mediaTypes/Parts'}
      responses: {'204': {description: ok}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), PathOperation("/parts", POST), Input{
		BodyPresent: true,
		Body:        []any{"QUJD", "REVG"},
		MediaType:   "multipart/form-data",
	}); err != nil {
		t.Fatal(err)
	}
	request := transport.requests[0]
	base, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || base != "multipart/form-data" {
		t.Fatalf("Content-Type = %q (%v)", request.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(strings.NewReader(string(transport.bodies[0])), params["boundary"])
	var names, bodies, transfers, extras []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(part)
		names = append(names, part.FormName())
		bodies = append(bodies, string(body))
		transfers = append(transfers, part.Header.Get("Content-Transfer-Encoding"))
		extras = append(extras, part.Header.Get("X-Part"))
	}
	if !reflect.DeepEqual(names, []string{"first", "rest"}) || !reflect.DeepEqual(bodies, []string{"QUJD", "REVG"}) ||
		!reflect.DeepEqual(transfers, []string{"", ""}) || !reflect.DeepEqual(extras, []string{"prefix", ""}) {
		t.Fatalf("parts names=%v bodies=%v transfers=%v extras=%v", names, bodies, transfers, extras)
	}
}

func TestOpenAPI32MultipartAlternativeConfinementAndFieldContradiction(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: multipart confinement, version: "1"}
servers: [{url: https://api.example}]
paths:
  /choice:
    post:
      requestBody:
        required: true
        content:
          multipart/form-data:
            schema: {type: array, items: {type: string}}
            encoding: {}
            itemEncoding: {}
          application/json: {schema: {type: array}}
      responses: {'204': {description: ok}}
  /contradiction:
    post:
      requestBody:
        required: true
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                bad: {type: string, contentEncoding: base64}
                good: {type: string}
            encoding:
              bad:
                headers:
                  Content-Transfer-Encoding: {schema: {type: string, const: gzip}}
      responses: {'204': {description: ok}}
  /sse:
    post:
      requestBody:
        required: true
        content:
          text/event-stream: {itemSchema: {type: object}}
      responses: {'204': {description: ok}}
  /nested-invalid:
    post:
      requestBody:
        required: true
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                bundle: {type: array, items: {type: string}}
            encoding:
              bundle:
                contentType: multipart/mixed
                encoding: {}
                itemEncoding: {}
          application/json: {schema: {type: object}}
      responses: {'204': {description: ok}}
  /optional-sse:
    post:
      requestBody:
        content:
          text/event-stream: {itemSchema: {type: object}}
      responses: {'204': {description: ok}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), PathOperation("/choice", POST), Input{
		BodyPresent: true, Body: []any{"a"}, MediaType: "application/json",
	}); err != nil {
		t.Fatalf("surviving JSON sibling: %v", err)
	}
	if _, err := client.Call(context.Background(), PathOperation("/nested-invalid", POST), Input{
		BodyPresent: true, Body: map[string]any{"bundle": []any{"a"}},
	}); err != nil {
		t.Fatalf("nested-invalid multipart alternative was not confined: %v", err)
	}
	if got := transport.requests[len(transport.requests)-1].Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("nested-invalid sibling Content-Type = %q, want application/json", got)
	}
	if _, err := client.Call(context.Background(), PathOperation("/optional-sse", POST), Input{}); err != nil {
		t.Fatalf("body-free optional unavailable media: %v", err)
	}
	optionalRequest := transport.requests[len(transport.requests)-1]
	if got := optionalRequest.Header.Get("Content-Type"); got != "" {
		t.Fatalf("body-free optional Content-Type = %q, want omitted", got)
	}
	if _, err := client.Call(context.Background(), PathOperation("/contradiction", POST), Input{
		BodyPresent: true, Body: map[string]any{"good": "ok"}, MediaType: "multipart/form-data",
	}); err != nil {
		t.Fatalf("unreached contradictory field: %v", err)
	}
	before := len(transport.requests)
	if _, err := client.Call(context.Background(), PathOperation("/contradiction", POST), Input{
		BodyPresent: true, Body: map[string]any{"bad": "QUJD"}, MediaType: "multipart/form-data",
	}); err == nil {
		t.Fatal("contradictory field unexpectedly dispatched")
	}
	if _, err := client.Call(context.Background(), PathOperation("/sse", POST), Input{
		BodyPresent: true, Body: []any{map[string]any{"data": "x"}}, MediaType: "text/event-stream",
	}); err == nil {
		t.Fatal("SSE request unexpectedly dispatched")
	}
	if len(transport.requests) != before {
		t.Fatalf("refused requests reached transport: before=%d after=%d", before, len(transport.requests))
	}
}

func TestOpenAPI32OneNestedEncodingLevel(t *testing.T) {
	transport := &openAPI32OperationTransport{}
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: nested multipart, version: "1"}
servers: [{url: https://api.example}]
paths:
  /nested:
    post:
      requestBody:
        required: true
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                bundle: {type: array, items: {type: string}}
            encoding:
              bundle:
                contentType: multipart/mixed
                prefixEncoding:
                  - {contentType: text/plain}
                itemEncoding: {contentType: text/plain}
      responses: {'204': {description: ok}}
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), PathOperation("/nested", POST), Input{
		BodyPresent: true, Body: map[string]any{"bundle": []any{"a", "b"}}, MediaType: "multipart/form-data",
	}); err != nil {
		t.Fatal(err)
	}
	_, outerParams, _ := mime.ParseMediaType(transport.requests[0].Header.Get("Content-Type"))
	outer := multipart.NewReader(strings.NewReader(string(transport.bodies[0])), outerParams["boundary"])
	bundle, err := outer.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	base, innerParams, err := mime.ParseMediaType(bundle.Header.Get("Content-Type"))
	if err != nil || base != "multipart/mixed" {
		t.Fatalf("nested Content-Type = %q (%v)", bundle.Header.Get("Content-Type"), err)
	}
	innerBytes, _ := io.ReadAll(bundle)
	inner := multipart.NewReader(strings.NewReader(string(innerBytes)), innerParams["boundary"])
	var values []string
	for {
		part, err := inner.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		value, _ := io.ReadAll(part)
		values = append(values, string(value))
	}
	if !reflect.DeepEqual(values, []string{"a", "b"}) {
		t.Fatalf("nested values = %v", values)
	}
}
