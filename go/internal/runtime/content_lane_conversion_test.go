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

// openbindings.openapi-3.0@1 §8.1 names `parameterConversion` for a §9.3 form
// or part property only where that property "must convert a JSON scalar to a
// string", and §9.3 routes a content-based property through §9.2's lane for
// its selected media type. The text/plain lane is therefore the converter's
// only content-lane site; the JSON lane serializes the supplied value as
// strict JSON and never consults it. Until 2026-09-03 the converter ran by
// declaration, so an integer bound for application/json reached the wire as
// its converted STRING (`["1","2"]`, `"true"`). The converter here is
// deliberately visible -- `n` + the scalar's own spelling -- so a converted
// scalar cannot be mistaken for its JSON image.
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
        "count":{"type":"integer"}
      }},
      "encoding":{"ids":{"contentType":"application/json"},"flag":{"contentType":"application/json"}}
    }}},
    "responses":{"204":{"description":"ok"}}
  }}}
}`, serverURL, mediaType))
}

func visibleConverter(value any) (string, error) { return "n" + fmt.Sprint(value), nil }

func TestOpenAPI30ContentLaneConverterReachesOnlyTextPlain(t *testing.T) {
	var gotContentType string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotContentType = request.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(request.Body)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	input := Input{Body: map[string]any{"ids": []any{float64(1), float64(2)}, "flag": true, "count": float64(7)}}

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
		// takes the text/plain default and is the converter's one site.
		if want := "count=n7&flag=true&ids=%5B1%2C2%5D"; string(gotBody) != want {
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
			"count": {"text/plain|n7"},
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("multipart parts = %v, want %v", got, want)
		}
	})

	t.Run("text/plain lane still requires the converter", func(t *testing.T) {
		client, err := Load(context.Background(), Source{Content: contentLaneDocument(server.URL, "application/x-www-form-urlencoded")}, ClientOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Call(context.Background(), PathOperation("/form", POST), Input{Body: map[string]any{"count": float64(7)}})
		if err == nil || !strings.Contains(err.Error(), "ParameterConverter") {
			t.Fatalf("unconfigured text/plain conversion = %v, want the converter-required refusal", err)
		}
		// The JSON lane needs no converter at all.
		if _, err := client.Call(context.Background(), PathOperation("/form", POST), Input{Body: map[string]any{"flag": true}}); err != nil {
			t.Fatalf("JSON-lane boolean without a converter: %v", err)
		}
		if string(gotBody) != "flag=true" {
			t.Fatalf("body = %q, want flag=true", gotBody)
		}
	})
}
