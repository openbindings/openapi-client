package openapiclient

import "fmt"

// RecognizeRepresentation inspects only the entry discriminator, not operation
// validity. Recognition therefore survives a malformed operation declaration.
// A false claim must not prevent other artifact families from inspecting bytes.
func RecognizeRepresentation(content []byte) (Edition, bool, error) {
	root, err := parseSwagger20Resource(content)
	if err != nil {
		return "", false, err
	}
	object, ok := root.(map[string]any)
	if !ok {
		return "", false, nil
	}
	if version, present := object["swagger"]; present {
		if version != "2.0" {
			return "", true, fmt.Errorf("unsupported Swagger edition: expected 2.0")
		}
		return EditionSwagger20, true, nil
	}
	if _, present := object["openapi"]; !present {
		return "", false, nil
	}
	edition, err := ClassifyOpenAPIEdition(content)
	return edition, true, err
}
