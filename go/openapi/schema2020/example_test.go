package schema2020_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/openbindings/openapi-client/go/openapi"
	"github.com/openbindings/openapi-client/go/openapi/schema2020"
)

// An OpenAPI 3.0 component projected for a request: the readOnly id leaves
// required, nullable becomes a type list, and the referenced Owner is a
// Defs entry keyed by its component name.
func ExampleProject() {
	const doc = `{"openapi":"3.0.4","info":{"title":"Pets","version":"1"},"paths":{},
		"components":{"schemas":{
			"Pet":{"type":"object","required":["id","name"],"properties":{
				"id":{"type":"integer","readOnly":true},
				"name":{"type":"string","nullable":true},
				"owner":{"$ref":"#/components/schemas/Owner"}}},
			"Owner":{"type":"object","properties":{"name":{"type":"string"}}}}}}`
	const uri = "https://api.example.com/openapi.json"
	c, err := openapi.Parse(context.Background(), []byte(doc), uri, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	pet, err := c.Schema(uri + "#/components/schemas/Pet")
	if err != nil {
		fmt.Println(err)
		return
	}
	p, err := schema2020.Project(c, pet, schema2020.Request)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Root:", sortedJSON(p.Root))
	for _, key := range slices.Sorted(maps.Keys(p.Defs)) {
		fmt.Printf("%s: %s (from %s)\n", key, sortedJSON(p.Defs[key]), p.Sources[key])
	}
	// Output:
	// Root: {"properties":{"id":{"readOnly":true,"type":"integer"},"name":{"type":["string","null"]},"owner":{"$ref":"#/$defs/Owner"}},"required":["name"],"type":"object"}
	// Owner: {"properties":{"name":{"type":"string"}},"type":"object"} (from https://api.example.com/openapi.json#/components/schemas/Owner)
}

// sortedJSON writes a JSON text with its object members sorted.
func sortedJSON(b []byte) string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err.Error()
	}
	out, _ := json.Marshal(v)
	return string(out)
}
