package openapiclient

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSwagger20SynthesisModelPreservesNativeOperationSurface(t *testing.T) {
	document := []byte(`{
  "swagger":"2.0",
  "info":{"title":"Pet service","version":"7","description":"native"},
  "host":"example.test",
  "schemes":["https"],
  "consumes":["application/json","application/*"],
  "produces":["application/json"],
  "securityDefinitions":{
    "key":{"type":"apiKey","name":"X-Key","in":"header"},
    "bad":{"type":"apiKey","name":"q","in":"query"}
  },
  "definitions":{"Node":{"type":"object","properties":{"next":{"$ref":"#/definitions/Node"}}}},
  "paths":{
    "/pets/{id}":{
      "get":{
        "operationId":"read pet",
        "summary":"Read one",
        "parameters":[
          {"name":"id","in":"path","required":true,"type":"string"},
          {"name":"q","in":"query","type":"integer","allowEmptyValue":true},
          {"name":"payload","in":"body","required":true,"schema":{"$ref":"#/definitions/Node"}}
        ],
        "security":[{"key":[]},{"bad":[]}],
        "responses":{"200":{"description":"ok","schema":{"$ref":"#/definitions/Node"}}}
      }
    }
  }
}`)
	client, err := LoadSwagger20(context.Background(), Swagger20Source{Content: document}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.SynthesisModel()
	if err != nil {
		t.Fatal(err)
	}
	if model.Name != "Pet service" || model.Version != "7" || len(model.Operations) != 1 {
		t.Fatalf("model = %#v", model)
	}
	op := model.Operations[0]
	if op.Ref != "#/paths/~1pets~1{id}/get" || op.OperationID != "read pet" || op.Description != "Read one" || op.Excluded {
		t.Fatalf("operation = %#v", op)
	}
	for _, requirement := range []string{"configuration.emptyValueForm", "configuration.parameterConversion", "configuration.requestMedia", "configuration.security"} {
		if !containsString(op.Requirements, requirement) {
			t.Errorf("requirements %v omit %q", op.Requirements, requirement)
		}
	}
	if op.Body == nil || !op.Body.Required || !json.Valid(op.Body.Schema) || !strings.Contains(string(op.Body.Schema), `"$defs"`) {
		t.Fatalf("body = %#v", op.Body)
	}
	if len(op.Security) != 2 || !op.Security[0].Usable || op.Security[1].Usable {
		t.Fatalf("security = %#v", op.Security)
	}
	if len(op.Responses) != 1 || !op.Responses[0].Usable || !op.Responses[0].CanSucceed {
		t.Fatalf("responses = %#v", op.Responses)
	}
}

func TestSwagger20SynthesisModelAccountsForExcludedTargets(t *testing.T) {
	client, err := LoadSwagger20(context.Background(), Swagger20Source{Content: []byte(`{
  "swagger":"2.0","info":{"title":"x","version":"1"},"host":"example.test","schemes":["https"],
  "paths":{"/x":{"get":{"security":[{"missing":[]}],"responses":{"2XX":{"description":"bad"}}}}}
}`)}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.SynthesisModel()
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Operations) != 1 || !model.Operations[0].Excluded || !strings.Contains(model.Operations[0].Reason, "inadmissible key") {
		t.Fatalf("operations = %#v", model.Operations)
	}
}

func TestSwagger20SynthesisModelInventoriesReferencedPathItemOperations(t *testing.T) {
	client, err := LoadSwagger20(context.Background(), Swagger20Source{Content: []byte(`{
  "swagger":"2.0","info":{"title":"x","version":"1"},"host":"example.test","schemes":["https"],
  "paths":{"/x":{"$ref":"#/x-path"}},
  "x-path":{"get":{"operationId":"fromRef","responses":{"204":{"description":"ok"}}}}
}`)}, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.SynthesisModel()
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Operations) != 1 || model.Operations[0].OperationID != "fromRef" || model.Operations[0].Ref != "#/paths/~1x/get" {
		t.Fatalf("operations = %#v", model.Operations)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
