package openapiclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

type swagger20MediaRoundTripper struct {
	request  *http.Request
	body     []byte
	response *http.Response
}

func (r *swagger20MediaRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	r.request = request
	if request.Body != nil {
		r.body, _ = io.ReadAll(request.Body)
	}
	response := r.response
	if response == nil {
		response = &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}
	}
	response.Request = request
	return response, nil
}

func TestSwagger20JSONBodyAndRawResponse(t *testing.T) {
	transport := &swagger20MediaRoundTripper{response: &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"image/png"}},
		Body:       io.NopCloser(strings.NewReader("\x00PNG")),
	}}
	prepared := prepareSwagger20MediaTest(t, transport, `{
  "swagger":"2.0","info":{"title":"media","version":"1"},
  "consumes":["application/json"],"produces":["image/png"],
  "paths":{"/pets":{"post":{"parameters":[{"name":"payload","in":"body","required":true,"schema":{"type":"object"}}],
  "responses":{"200":{"description":"ok","schema":{"type":"string","format":"binary"}}}}}}
}`)
	outputs, err := executeSwagger20MediaTest(t, prepared, Swagger20Input{Body: map[string]any{"name": "Ada"}, BodyPresent: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(transport.body); got != `{"name":"Ada"}` {
		t.Fatalf("request body = %q", got)
	}
	if got := transport.request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	want := base64.StdEncoding.EncodeToString([]byte("\x00PNG"))
	if len(outputs) != 1 || outputs[0] != want {
		t.Fatalf("outputs = %#v, want %q", outputs, want)
	}
}

func TestSwagger20URLEncodedAndMultipart(t *testing.T) {
	t.Run("urlencoded", func(t *testing.T) {
		transport := &swagger20MediaRoundTripper{}
		prepared := prepareSwagger20MediaTest(t, transport, `{
  "swagger":"2.0","info":{"title":"form","version":"1"},"consumes":["application/x-www-form-urlencoded"],
  "paths":{"/form":{"post":{"parameters":[
    {"name":"tag","in":"formData","type":"array","collectionFormat":"multi","items":{"type":"string"}},
    {"name":"note","in":"formData","type":"string","allowEmptyValue":true}],
  "responses":{"204":{"description":"ok"}}}}}
}`)
		input := Swagger20Input{Parameters: Swagger20Parameters{FormData: map[string]any{"tag": []any{"a b", "é"}, "note": ""}}}
		prepared.options.EmptyValueForm = Swagger20EmptyValueNameOnly
		_, err := executeSwagger20MediaTest(t, prepared, input)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(transport.body); got != "note&tag=a+b&tag=%C3%A9" {
			t.Fatalf("urlencoded body = %q", got)
		}
	})

	t.Run("multipart file", func(t *testing.T) {
		transport := &swagger20MediaRoundTripper{}
		prepared := prepareSwagger20MediaTest(t, transport, `{
  "swagger":"2.0","info":{"title":"form","version":"1"},"consumes":["multipart/form-data"],
  "paths":{"/form":{"post":{"parameters":[{"name":"upload","in":"formData","type":"file","required":true}],
  "responses":{"204":{"description":"ok"}}}}}
}`)
		prepared.options.PropertyMedia = map[string]string{"upload": "image/png"}
		input := Swagger20Input{Parameters: Swagger20Parameters{FormData: map[string]any{"upload": "AFBORw=="}}}
		_, err := executeSwagger20MediaTest(t, prepared, input)
		if err != nil {
			t.Fatal(err)
		}
		text := string(transport.body)
		for _, want := range []string{`name="upload"`, "Content-Type: image/png", "\x00PNG"} {
			if !strings.Contains(text, want) {
				t.Fatalf("multipart body lacks %q: %q", want, text)
			}
		}
		if strings.Contains(text, "filename=") || strings.Contains(text, "Content-Transfer-Encoding") {
			t.Fatalf("multipart invented prohibited headers: %q", text)
		}
	})
}

func TestSwagger20ContentCodingOrder(t *testing.T) {
	transport := &swagger20MediaRoundTripper{response: &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type":     {"text/plain"},
			"Content-Encoding": {"first, second"},
		},
		Body: io.NopCloser(strings.NewReader("response12")),
	}}
	prepared := prepareSwagger20MediaTest(t, transport, `{
  "swagger":"2.0","info":{"title":"codings","version":"1"},
  "consumes":["text/plain"],"produces":["text/plain"],
  "paths":{"/pets":{"post":{"parameters":[
    {"name":"Content-Encoding","in":"header","required":true,"type":"string","enum":["first, second"]},
    {"name":"payload","in":"body","required":true,"schema":{"type":"string"}}],
  "responses":{"200":{"description":"ok","headers":{"Content-Encoding":{"type":"string","enum":["first, second"]}},"schema":{"type":"string"}}}}}}
}`)
	prepared.options.RequestContentCodings = map[string]ContentEncoder{
		"first":  func(value []byte) ([]byte, error) { return append(value, '1'), nil },
		"second": func(value []byte) ([]byte, error) { return append(value, '2'), nil },
	}
	prepared.options.ResponseContentCodings = map[string]ContentDecoder{
		"first":  swagger20RemoveCodingSuffix('1'),
		"second": swagger20RemoveCodingSuffix('2'),
	}
	input := Swagger20Input{
		Parameters: Swagger20Parameters{Header: map[string]any{"Content-Encoding": "first, second"}},
		Body:       "request", BodyPresent: true,
	}
	outputs, err := executeSwagger20MediaTest(t, prepared, input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(transport.body, []byte("request12")) {
		t.Fatalf("coded request body = %q", transport.body)
	}
	if len(outputs) != 1 || outputs[0] != "response" {
		t.Fatalf("decoded outputs = %#v", outputs)
	}
}

func swagger20RemoveCodingSuffix(suffix byte) ContentDecoder {
	return func(value []byte) ([]byte, error) {
		if len(value) == 0 || value[len(value)-1] != suffix {
			return nil, io.ErrUnexpectedEOF
		}
		return value[:len(value)-1], nil
	}
}

func prepareSwagger20MediaTest(t *testing.T, transport http.RoundTripper, artifact string) *Swagger20PreparedOperation {
	t.Helper()
	selector := "#/paths/~1pets/post"
	if strings.Contains(artifact, `"/form"`) {
		selector = "#/paths/~1form/post"
	}
	prepared, err := NewEngine(nil).PrepareSwagger20(context.Background(), Swagger20PrepareOptions{
		Source: Swagger20Source{Content: []byte(artifact)}, Ref: selector,
		Server: "https://peer.example", HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func executeSwagger20MediaTest(t *testing.T, prepared *Swagger20PreparedOperation, input Swagger20Input) ([]any, error) {
	t.Helper()
	execution, err := prepared.Start(context.Background())
	if err != nil {
		return nil, err
	}
	if err := execution.Send(context.Background(), input); err != nil {
		return nil, err
	}
	if err := execution.FinishInput(); err != nil {
		return nil, err
	}
	var outputs []any
	for event := range execution.Events() {
		outputs = append(outputs, event.Value)
	}
	return outputs, execution.Wait()
}
