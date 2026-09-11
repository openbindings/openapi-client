package openapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	openapi "github.com/openbindings/openapi-client/go"
)

func TestPublicEncodingHeaderClosure(t *testing.T) {
	for _, which := range []string{"control", "ignored-key", "fixed-header", "chained-header", "ignored-content-type", "positional-content-type", "unavailable-header"} {
		t.Run(which, func(t *testing.T) {
			positional := which == "positional-content-type"
			fixed := which == "fixed-header" || which == "chained-header"
			name := "X-Fixed"
			if strings.Contains(which, "content-type") {
				name = "cOnTeNt-TyPe"
			}
			encoding := map[string]any{"headers": map[string]any{name: map[string]any{"$ref": "https://other.example/header"}}}
			key := "x"
			if which == "ignored-key" {
				key = "ghost"
			}
			encodings := map[string]any{key: encoding}
			if which == "control" {
				encodings = map[string]any{}
			}
			media := map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}, "encoding": encodings}
			mediaType := "multipart/form-data"
			var input any = map[string]any{"x": "value"}
			if positional {
				mediaType = "multipart/mixed"
				input = []any{"value"}
				media = map[string]any{"itemSchema": map[string]any{"type": "string"}, "itemEncoding": encoding}
			}
			source := map[string]any{"openapi": "3.2.0", "info": map[string]any{"title": which, "version": "1"}, "servers": []any{map[string]any{"url": "https://api.example"}}, "paths": map[string]any{"/x": map[string]any{"post": map[string]any{"requestBody": map[string]any{"required": true, "content": map[string]any{mediaType: media}}, "responses": map[string]any{"204": map[string]any{"description": "ok"}}}}}}
			sourceBytes, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			header := map[string]any{"schema": map[string]any{"type": "string", "const": "fixed"}}
			if !fixed {
				header["required"] = true
			}
			headerBytes, err := json.Marshal(header)
			if err != nil {
				t.Fatal(err)
			}
			var bodies, reads []string
			client, err := openapi.Load(context.Background(), openapi.FromBytes(sourceBytes), openapi.Options{
				DocumentHTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					reads = append(reads, r.URL.String())
					body := string(headerBytes)
					if which == "chained-header" && r.URL.Path == "/header" {
						body = `{"$ref":"https://other.example/final-header"}`
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})},
				HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					body, readErr := io.ReadAll(r.Body)
					if readErr != nil {
						return nil, readErr
					}
					bodies = append(bodies, string(body))
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Call(context.Background(), openapi.PathOperation("/x", openapi.POST), openapi.Input{BodyPresent: true, Body: input})
			if which == "unavailable-header" {
				if err == nil || len(bodies) != 0 || len(reads) != 1 {
					t.Fatalf("active unavailable header: err=%v bodies=%q reads=%q", err, bodies, reads)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(bodies) != 1 || strings.Contains(bodies[0], "X-Fixed: fixed\r\n") != fixed {
				t.Fatalf("wire header: %q", bodies)
			}
			wantReads := 0
			if fixed {
				wantReads = 1
			}
			if which == "chained-header" {
				wantReads = 2
			}
			if len(reads) != wantReads {
				t.Fatalf("resource reads=%q want %d", reads, wantReads)
			}
		})
	}
}
