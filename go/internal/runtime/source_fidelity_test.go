package openapiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestExactOpenAPISchemaSource(t *testing.T) {
	for _, edition := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		for _, token := range []string{"9007199254740993", "0.10000000000000001", "1e400", "1e-400"} {
			t.Run(edition+"/"+token, func(t *testing.T) {
				source := fmt.Sprintf(`{"openapi":%q,"info":{"title":"Exact","version":"1"},"paths":{"/x":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"number","minimum":%s,"example":%s}}}}}}}}}`, edition, token, token)
				artifact, _, err := loadArtifact(context.Background(), nil, Source{Content: []byte(source)}, false)
				if err != nil {
					t.Fatal(err)
				}
				schema := artifact.Document.Paths.Find("/x").Get.Responses.Status(200).Value.Content["application/json"].Schema
				projected := schemaRefToMap(schema, artifact.schemaOverlays)
				for _, key := range []string{"minimum", "example"} {
					text, err := json.Marshal(projected[key])
					if err != nil {
						t.Fatal(err)
					}
					if string(text) != token {
						t.Fatalf("%s = %s; want %s", key, text, token)
					}
				}
			})
		}
	}
}

func TestExactOpenAPIOpaqueMetadataSource(t *testing.T) {
	for _, edition := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		for _, token := range []string{"9007199254740993", "0.10000000000000001", "1e400", "1e-400"} {
			t.Run(edition+"/"+token, func(t *testing.T) {
				source := fmt.Sprintf(`{"openapi":%q,"info":{"title":"Exact","version":"1","x-id":%s},"paths":{"/x":{"get":{"x-id":%s,"responses":{"200":{"description":"ok","content":{"application/json":{"example":%s,"examples":{"named":{"value":%s}},"schema":{"type":"number"}}}}}}}}}`, edition, token, token, token, token)
				artifact, _, err := loadArtifact(context.Background(), nil, Source{Content: []byte(source)}, false)
				if err != nil {
					t.Fatal(err)
				}
				op := artifact.Document.Paths.Find("/x").Get
				media := op.Responses.Status(200).Value.Content["application/json"]
				for key, value := range map[string]any{"info": artifact.Document.Info.Extensions["x-id"], "operation": op.Extensions["x-id"], "example": media.Example, "named": media.Examples["named"].Value.Value} {
					text, err := json.Marshal(value)
					if err != nil || string(text) != token {
						t.Fatalf("%s = %s (%v); want %s", key, text, err, token)
					}
				}
			})
		}
	}
}
