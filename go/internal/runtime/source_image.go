package openapiclient

import (
	"encoding/json"
	"fmt"
)

// SourceJSONImage returns the exact JSON object represented by JSON/YAML source
// text. This is the existing source grammar gate, not OAS validation or reference
// resolution; fragment documents need not declare an OpenAPI version.
func SourceJSONImage(data []byte) (json.RawMessage, error) {
	value, err := parseRawOpenAPIResource(data)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("OpenAPI source image must be an object")
	}
	return json.Marshal(value)
}
