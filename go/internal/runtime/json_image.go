package openapiclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Decode an ordinary JSON image without assigning native floats to interface
// members. Typed protocol fields keep their own explicit decoder contracts.
func unmarshalJSONImage(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON content")
	}
	return nil
}
