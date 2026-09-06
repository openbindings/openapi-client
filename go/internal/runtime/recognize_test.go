package openapiclient

import (
	"strings"
	"testing"
)

func TestRecognitionDoesNotDependOnOperationValidity(t *testing.T) {
	for _, version := range []string{"3.0.4", "3.1.2", "3.2.0"} {
		edition, claimed, err := RecognizeRepresentation([]byte(`{"openapi":"` + version + `","paths":"invalid"}`))
		if err != nil || !claimed || string(edition) != version {
			t.Fatalf("%s: %s %t %v", version, edition, claimed, err)
		}
	}
	edition, claimed, err := RecognizeRepresentation([]byte(`swagger: "2.0"
paths: invalid`))
	if err != nil || !claimed || edition != EditionSwagger20 {
		t.Fatalf("%s %t %v", edition, claimed, err)
	}
	for _, data := range []string{`{"openapi":"9.0.0"}`, `{"swagger":"3.0"}`} {
		if _, claimed, err := RecognizeRepresentation([]byte(data)); !claimed || err == nil {
			t.Fatalf("unsupported edition lost: %s", data)
		}
	}
	if _, claimed, err := RecognizeRepresentation([]byte(`{"unrelated":true}`)); claimed || err != nil {
		t.Fatalf("false claim: %t %v", claimed, err)
	}
	if _, _, err := RecognizeRepresentation([]byte(`{oops`)); err == nil || strings.Contains(err.Error(), "Swagger 2.0") {
		t.Fatalf("wrong parser edition: %v", err)
	}
}
