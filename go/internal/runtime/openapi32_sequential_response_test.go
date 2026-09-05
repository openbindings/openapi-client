package openapiclient

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAPI32SequentialResponseFraming(t *testing.T) {
	multipartBody, multipartType := openAPI32SequentialMultipartFixture(t)
	for _, testCase := range []struct {
		name        string
		declaration string
		actual      string
		body        string
		want        []any
	}{
		{"jsonl", "application/jsonl", "application/jsonl", "{\"n\":1}\n{\"n\":2}\n", []any{map[string]any{"n": float64(1)}, map[string]any{"n": float64(2)}}},
		{"ndjson", "application/x-ndjson", "application/x-ndjson", "true\n12\n", []any{true, float64(12)}},
		{"json-seq", "application/json-seq", "application/json-seq", "\x1e{\"n\":1}\n\x1e2\n", []any{map[string]any{"n": float64(1)}, float64(2)}},
		{"suffix-json-seq", "application/problem+json-seq", "application/problem+json-seq", "\x1efalse\n\x1enull\n", []any{false, nil}},
		{"positional-multipart", "multipart/mixed", multipartType, multipartBody, []any{map[string]any{"n": float64(1)}, "second"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
				StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {testCase.actual}},
				Body: io.NopCloser(strings.NewReader(testCase.body)),
			}}}
			client := loadOpenAPI32SequentialClient(t, transport, testCase.declaration, "itemSchema: {}", 0)
			result, err := client.Stream(context.Background(), PathOperation("/x", GET), Input{})
			if err != nil || !result.OK {
				t.Fatalf("stream result = %#v, err %v", result, err)
			}
			got, terminal := collectNativeStream(result.Stream)
			if terminal != nil || !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("values = %#v, terminal %v; want %#v", got, terminal, testCase.want)
			}
		})
	}
}

func TestOpenAPI32SSEEmitsAuthorityShapedObjects(t *testing.T) {
	transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
		StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("event: update\nid: 7\nretry: 0\ndata: first\ndata: second\n\n: ignored\ndata: tail\n\n")),
	}}}
	client := loadOpenAPI32SequentialClient(t, transport, "text/event-stream", "itemSchema: {type: object}", 0)
	result, err := client.Stream(context.Background(), PathOperation("/x", GET), Input{})
	if err != nil || !result.OK {
		t.Fatalf("stream result = %#v, err %v", result, err)
	}
	got, terminal := collectNativeStream(result.Stream)
	want := []any{
		map[string]any{"data": "first\nsecond", "event": "update", "id": "7", "retry": 0},
		map[string]any{"data": "tail"},
	}
	if terminal != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %#v, terminal %v; want %#v", got, terminal, want)
	}
}

func TestOpenAPI32SequentialMalformedItemRetainsEarlierSuccess(t *testing.T) {
	transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
		StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/jsonl"}},
		Body: io.NopCloser(strings.NewReader("{\"ok\":1}\nnot-json\n{\"never\":3}\n")),
	}}}
	client := loadOpenAPI32SequentialClient(t, transport, "application/jsonl", "itemSchema: {type: object}", 0)
	result, err := client.Stream(context.Background(), PathOperation("/x", GET), Input{})
	if err != nil {
		t.Fatal(err)
	}
	got, terminal := collectNativeStream(result.Stream)
	want := []any{map[string]any{"ok": float64(1)}}
	if !reflect.DeepEqual(got, want) || terminal == nil || !strings.Contains(terminal.Error(), "item 1 is malformed JSON") {
		t.Fatalf("values = %#v, terminal %v", got, terminal)
	}
}

func TestOpenAPI32SequentialDeliveryBoundIsPerItem(t *testing.T) {
	transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
		StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/jsonl"}},
		Body: io.NopCloser(strings.NewReader("1\n\"0123456789\"\n")),
	}}}
	client := loadOpenAPI32SequentialClient(t, transport, "application/jsonl", "itemSchema: {}", 8)
	result, err := client.Stream(context.Background(), PathOperation("/x", GET), Input{})
	if err != nil {
		t.Fatal(err)
	}
	got, terminal := collectNativeStream(result.Stream)
	if !reflect.DeepEqual(got, []any{float64(1)}) || terminal == nil || !strings.Contains(terminal.Error(), "exceeds 8 byte limit") {
		t.Fatalf("values = %#v, terminal %v", got, terminal)
	}
}

func TestOpenAPI32SequentialResponseRequiresStreamAPIEvenForOneItem(t *testing.T) {
	transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
		StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/x-ndjson"}},
		Body: io.NopCloser(strings.NewReader("1\n")),
	}}}
	client := loadOpenAPI32SequentialClient(t, transport, "application/x-ndjson", "itemSchema: {type: number}", 0)
	if _, err := client.Call(context.Background(), PathOperation("/x", GET), Input{}); err == nil || !strings.Contains(err.Error(), "use Client.Stream") {
		t.Fatalf("Call error = %v", err)
	}
}

func TestOpenAPI32ItemSchemaWithoutIncorporatedFramingFailsLoudly(t *testing.T) {
	transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
		StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/x-private"}},
		Body: io.NopCloser(strings.NewReader("payload")),
	}}}
	client := loadOpenAPI32SequentialClient(t, transport, "application/x-private", "itemSchema: {}", 0)
	result, err := client.Stream(context.Background(), PathOperation("/x", GET), Input{})
	if err != nil {
		t.Fatal(err)
	}
	_, terminal := collectNativeStream(result.Stream)
	if terminal == nil || !strings.Contains(terminal.Error(), "no incorporated sequential framing") {
		t.Fatalf("terminal = %v", terminal)
	}
}

func TestOpenAPI32NonSuccessfulSequentialResponseEmitsNoItems(t *testing.T) {
	transport := &openAPI32ResponseTransport{responses: map[string]*http.Response{"/x": {
		StatusCode: 400, Status: "400 Bad Request", Header: http.Header{"Content-Type": {"application/jsonl"}},
		Body: io.NopCloser(strings.NewReader("{\"error\":1}\n{\"error\":2}\n")),
	}}}
	client := loadOpenAPI32SequentialClient(t, transport, "application/jsonl", "itemSchema: {type: object}", 0)
	result, err := client.Call(context.Background(), PathOperation("/x", GET), Input{})
	if err != nil || result == nil || result.OK {
		t.Fatalf("result = %#v, err %v", result, err)
	}
}

func loadOpenAPI32SequentialClient(t *testing.T, transport http.RoundTripper, mediaType, declaration string, maxBytes int64) *Client {
	t.Helper()
	client, err := Load(context.Background(), Source{Content: []byte(`
openapi: 3.2.0
info: {title: sequential responses, version: "1"}
servers: [{url: https://api.example}]
paths:
  /x:
    get:
      responses:
        '200':
          content:
            ` + mediaType + `:
              ` + declaration + `
        default:
          content:
            ` + mediaType + `:
              ` + declaration + `
`)}, ClientOptions{HTTPClient: &http.Client{Transport: transport}, MaxDeliveryUnitBytes: maxBytes})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func collectNativeStream(stream *Stream) ([]any, error) {
	var values []any
	for {
		event, open, err := stream.Next(context.Background())
		if err != nil {
			return values, err
		}
		if !open {
			return values, nil
		}
		values = append(values, event.Data)
	}
}

func openAPI32SequentialMultipartFixture(t *testing.T) (string, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary("openapi32-boundary"); err != nil {
		t.Fatal(err)
	}
	for _, part := range []struct {
		contentType string
		body        string
	}{{"application/json", `{"n":1}`}, {"text/plain", "second"}} {
		header := textproto.MIMEHeader{"Content-Type": {part.contentType}}
		partWriter, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(partWriter, part.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String(), "multipart/mixed; boundary=openapi32-boundary"
}
