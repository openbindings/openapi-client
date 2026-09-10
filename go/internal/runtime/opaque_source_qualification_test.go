package openapiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExactOpaqueObjectPositions(t *testing.T) {
	for _, edition := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		t.Run(edition, func(t *testing.T) {
			const token = "9007199254740993"
			source := fmt.Sprintf(`{
 "openapi":%q,"x-id":%s,
 "info":{"title":"Exact","version":"1","x-id":%s,"contact":{"x-id":%s},"license":{"name":"test","x-id":%s}},
 "servers":[{"url":"https://example.invalid/{v}","x-id":%s,"variables":{"v":{"default":"v1","x-id":%s}}}],
 "tags":[{"name":"test","x-id":%s}],"externalDocs":{"url":"https://example.invalid","x-id":%s},
 "components":{"x-id":%s,"securitySchemes":{"auth":{"type":"apiKey","in":"header","name":"key","x-id":%s}},
 "examples":{"shared":{"value":{"x-id":%s}}},"links":{"shared":{"operationId":"test","parameters":{"id":%s},"x-id":%s}}},
 "paths":{"x-id":%s,"/x":{"x-id":%s,"post":{"operationId":"test","x-id":%s,
 "parameters":[{"name":"q","in":"query","x-id":%s,"example":null,"schema":{"type":"string"},"examples":{"shared":{"$ref":"#/components/examples/shared"}}}],
 "requestBody":{"x-id":%s,"content":{"multipart/form-data":{"schema":{"type":"object"},"x-id":%s,"encoding":{"part":{"x-id":%s}}}}},
 "responses":{"x-id":%s,"200":{"description":"ok","x-id":%s,"links":{"shared":{"$ref":"#/components/links/shared"}},"headers":{"h":{"x-id":%s,"example":null,"schema":{"type":"string"}}},"content":{"application/json":{"x-id":%s,"example":null,"examples":{"shared":{"$ref":"#/components/examples/shared"}}}}}}}}}
}`, edition, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token, token)
			artifact, _, err := loadArtifact(context.Background(), nil, Source{Content: []byte(source)}, false)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(artifact.Document)
			if err != nil {
				t.Fatal(err)
			}
			var image any
			decoder := json.NewDecoder(strings.NewReader(string(encoded)))
			decoder.UseNumber()
			if err := decoder.Decode(&image); err != nil {
				t.Fatal(err)
			}
			var check func(any, string)
			seen := 0
			check = func(value any, path string) {
				switch value := value.(type) {
				case map[string]any:
					for key, child := range value {
						if key == "x-id" || key == "id" {
							seen++
							if child != json.Number(token) {
								t.Errorf("%s/%s = %#v", path, key, child)
							}
						}
						check(child, path+"/"+key)
					}
				case []any:
					for i, child := range value {
						check(child, fmt.Sprintf("%s/%d", path, i))
					}
				}
			}
			check(image, "")
			if seen != 24 {
				t.Errorf("retained %d value-bearing fields; want 24", seen)
			}
			for _, fragment := range []string{`"example":null`} {
				if strings.Count(string(encoded), fragment) != 3 {
					t.Errorf("authored null presence lost: %s", encoded)
				}
			}
		})
	}
}

func TestExactExternalExampleAndMarkerLookalike(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"value":9007199254740993}`)
	}))
	defer server.Close()
	for _, edition := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		source := fmt.Sprintf(`{"openapi":%q,"info":{"title":"Exact","version":"1","x-openapi-client-internal-value-overlay":9007199254740993,"x-openapi-client-internal-author-field":9007199254740993},"paths":{"/x":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"examples":{"external":{"$ref":%q}}}}}}}}}}`, edition, server.URL+"/example")
		artifact, _, err := loadArtifact(context.Background(), nil, Source{Content: []byte(source)}, false)
		if err != nil {
			t.Fatal(err)
		}
		media := artifact.Document.Paths.Find("/x").Get.Responses.Status(200).Value.Content["application/json"]
		for name, value := range map[string]any{"external": media.Examples["external"].Value.Value, "marker": artifact.Document.Info.Extensions[valueOverlayMarker], "lookalike": artifact.Document.Info.Extensions["x-openapi-client-internal-author-field"]} {
			encoded, err := json.Marshal(value)
			if err != nil || string(encoded) != "9007199254740993" {
				t.Errorf("%s/%s=%s (%v)", edition, name, encoded, err)
			}
		}
	}
}

func TestExactNestedOpaqueMetadata(t *testing.T) {
	for _, edition := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		source := fmt.Sprintf(`{"openapi":%q,"info":{"title":"Exact","version":"1"},"paths":{"/x":{"get":{"responses":{"204":{"description":"ok"}}}}},"components":{
"securitySchemes":{"auth":{"type":"oauth2","flows":{"x-id":9007199254740993,
"implicit":{"authorizationUrl":"https://example.invalid/auth","scopes":{},"x-id":9007199254740993},
"password":{"tokenUrl":"https://example.invalid/token","scopes":{},"x-id":9007199254740993},
"clientCredentials":{"tokenUrl":"https://example.invalid/token","scopes":{},"x-id":9007199254740993},
"authorizationCode":{"authorizationUrl":"https://example.invalid/auth","tokenUrl":"https://example.invalid/token","scopes":{},"x-id":9007199254740993}}}},
"links":{"next":{"operationId":"next","server":{"url":"https://example.invalid/{v}","x-id":9007199254740993,"variables":{"v":{"default":"v1","x-id":9007199254740993}}}}},
"schemas":{"Value":{"type":"string","xml":{"name":"Value","x-id":9007199254740993},"externalDocs":{"url":"https://example.invalid/docs","x-id":9007199254740993}}}}}`, edition)
		artifact, _, err := loadArtifact(context.Background(), nil, Source{Content: []byte(source)}, false)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(artifact.Document)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(encoded), `"x-id":9007199254740993`) != 9 {
			t.Errorf("%s lost nested metadata: %s", edition, encoded)
		}
	}
}
