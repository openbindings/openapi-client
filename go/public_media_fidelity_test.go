package openapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	openapi "github.com/openbindings/openapi-client/go"
)

const exactMediaJSON = `{"id":9007199254740993,"huge":1e400,"tiny":1e-400,"money":0.10000000000000001,"nested":[null,[],{},"9007199254740993"]}`

func exactMediaValue(t *testing.T, raw string) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPublicExactParameterAndJSONParts(t *testing.T) {
	for _, edition := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		for _, media := range []string{"multipart/form-data", "application/x-www-form-urlencoded"} {
			t.Run(edition+"/"+media, func(t *testing.T) {
				var observedQuery, observedPart string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					observedQuery = r.URL.Query().Get("q")
					_, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
					if err != nil {
						t.Error(err)
						return
					}
					if media == "multipart/form-data" {
						parts := multipart.NewReader(r.Body, parameters["boundary"])
						part, err := parts.NextPart()
						if err != nil {
							t.Error(err)
							return
						}
						if part.FormName() != "payload" {
							t.Error("unexpected part", part.FormName())
						}
						data, err := io.ReadAll(part)
						if err != nil {
							t.Error(err)
							return
						}
						observedPart = string(data)
					} else {
						data, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						fields, err := url.ParseQuery(string(data))
						if err != nil {
							t.Error(err)
							return
						}
						observedPart = fields.Get("payload")
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, exactMediaJSON)
				}))
				defer server.Close()
				source := fmt.Sprintf(`{"openapi":%q,"info":{"title":"Exact media","version":"1"},"servers":[{"url":%q}],"paths":{"/x":{"post":{"operationId":"test","parameters":[{"name":"q","in":"query","required":true,"content":{"application/json":{"schema":{}}}}],"requestBody":{"required":true,"content":{%q:{"schema":{"type":"object","properties":{"payload":{"type":"object"}}},"encoding":{"payload":{"contentType":"application/json"}}}}},"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{}}}}}}}}}`, edition, server.URL, media)
				client, err := openapi.Load(context.Background(), openapi.FromText(source), openapi.Options{})
				if err != nil {
					t.Fatal(err)
				}
				value := exactMediaValue(t, exactMediaJSON)
				result, err := client.Call(context.Background(), openapi.OperationID("test"), openapi.Input{Body: map[string]any{"payload": value}, BodyPresent: true, Parameters: openapi.Parameters{Query: map[string]any{"q": value}}})
				if err != nil || !result.OK {
					t.Fatalf("call: %#v, %v", result, err)
				}
				for name, actual := range map[string]any{"query": exactMediaValue(t, observedQuery), "part": exactMediaValue(t, observedPart), "response": result.Data} {
					if !reflect.DeepEqual(actual, value) {
						t.Errorf("%s changed: %#v", name, actual)
					}
				}
			})
		}
	}
}

func TestPublicExactExistingSequentialLanes(t *testing.T) {
	for _, media := range []string{"application/jsonl", "application/x-ndjson", "application/json-seq", "application/problem+json-seq"} {
		t.Run(media, func(t *testing.T) {
			items := []string{exactMediaJSON, "9007199254740993", "1e400", "1e-400", "null", "[]"}
			var wire strings.Builder
			for _, item := range items {
				if strings.HasSuffix(media, "json-seq") {
					wire.WriteByte(0x1e)
				}
				wire.WriteString(item + "\n")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", media)
				fmt.Fprint(w, wire.String())
			}))
			defer server.Close()
			source := fmt.Sprintf(`{"openapi":"3.2.0","info":{"title":"Exact sequence","version":"1"},"servers":[{"url":%q}],"paths":{"/x":{"get":{"operationId":"test","responses":{"200":{"content":{%q:{"itemSchema":{}}}}}}}}}`, server.URL, media)
			client, err := openapi.Load(context.Background(), openapi.FromText(source), openapi.Options{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Stream(context.Background(), openapi.OperationID("test"), openapi.Input{})
			if err != nil || !result.OK {
				t.Fatalf("stream: %#v, %v", result, err)
			}
			for _, raw := range items {
				event, open, err := result.Stream.Next(context.Background())
				if err != nil || !open || !reflect.DeepEqual(event.Data, exactMediaValue(t, raw)) {
					t.Fatalf("%s: %#v, open=%v, err=%v", raw, event, open, err)
				}
			}
			_, open, err := result.Stream.Next(context.Background())
			if open || err != nil {
				t.Fatalf("unexpected tail: %v, %v", open, err)
			}
			if err := result.Stream.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
