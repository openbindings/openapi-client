package openapiclient

import (
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Content-based form and multipart properties are serialized by their media
// lanes. parameterConversion belongs only to schema-form parameters and an
// explicitly RFC 6570-style Encoding path. The converter here is deliberately
// visible -- `n` + the scalar's own spelling -- so accidental consultation is
// observable.
func contentLaneDocument(serverURL, mediaType string) []byte {
	return []byte(fmt.Sprintf(`{
  "openapi":"3.0.4",
  "info":{"title":"content lane","version":"1"},
  "servers":[{"url":%q}],
  "paths":{"/form":{"post":{
    "requestBody":{"required":true,"content":{%q:{
      "schema":{"type":"object","properties":{
        "ids":{"type":"array","items":{"type":"integer"}},
        "flag":{"type":"boolean"},
        "count":{"type":"integer"},
        "styled":{"type":"integer"}
      }},
      "encoding":{"ids":{"contentType":"application/json"},"flag":{"contentType":"application/json"},"styled":{"explode":true}}
    }}},
    "responses":{"204":{"description":"ok"}}
  }}}
}`, serverURL, mediaType))
}

func visibleConverter(value any) (string, error) { return "n" + fmt.Sprint(value), nil }

func TestOpenAPI30ContentLaneNeverUsesParameterConverter(t *testing.T) {
	var gotContentType string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotContentType = request.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(request.Body)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	input := Input{Body: map[string]any{"ids": []any{float64(1), float64(2)}, "flag": true, "count": float64(1000)}}

	t.Run("urlencoded", func(t *testing.T) {
		client, err := Load(context.Background(), Source{Content: contentLaneDocument(server.URL, "application/x-www-form-urlencoded")},
			ClientOptions{ParameterConverter: visibleConverter})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Call(context.Background(), PathOperation("/form", POST), input); err != nil {
			t.Fatal(err)
		}
		// `ids` and `flag` ride the JSON lane as their JSON images; `count`
		// takes the text/plain default and uses its binding-fixed lexical form.
		if want := "count=1e3&flag=true&ids=%5B1%2C2%5D"; string(gotBody) != want {
			t.Fatalf("urlencoded body = %q, want %q", gotBody, want)
		}
	})

	t.Run("multipart", func(t *testing.T) {
		client, err := Load(context.Background(), Source{Content: contentLaneDocument(server.URL, "multipart/form-data")},
			ClientOptions{ParameterConverter: visibleConverter})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Call(context.Background(), PathOperation("/form", POST), input); err != nil {
			t.Fatal(err)
		}
		_, params, err := mime.ParseMediaType(gotContentType)
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(strings.NewReader(string(gotBody)), params["boundary"])
		got := map[string][]string{}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(part)
			got[part.FormName()] = append(got[part.FormName()], part.Header.Get("Content-Type")+"|"+string(data))
		}
		want := map[string][]string{
			"ids":   {"application/json|1", "application/json|2"},
			"flag":  {"application/json|true"},
			"count": {"text/plain|1e3"},
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("multipart parts = %v, want %v", got, want)
		}
	})

	t.Run("content text/plain lane needs no converter", func(t *testing.T) {
		client, err := Load(context.Background(), Source{Content: contentLaneDocument(server.URL, "application/x-www-form-urlencoded")}, ClientOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = client.Call(context.Background(), PathOperation("/form", POST), Input{Body: map[string]any{"count": float64(1000)}}); err != nil {
			t.Fatalf("content-based text/plain without a converter: %v", err)
		}
		if string(gotBody) != "count=1e3" {
			t.Fatalf("body = %q, want count=1e3", gotBody)
		}
	})

	t.Run("explicit style lane still requires the converter", func(t *testing.T) {
		client, err := Load(context.Background(), Source{Content: contentLaneDocument(server.URL, "application/x-www-form-urlencoded")}, ClientOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Call(context.Background(), PathOperation("/form", POST), Input{Body: map[string]any{"styled": float64(7)}})
		if err == nil || !strings.Contains(err.Error(), "ParameterConverter") {
			t.Fatalf("unconfigured style conversion = %v, want the converter-required refusal", err)
		}
	})
}
