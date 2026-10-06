package openapi_test

import (
	"fmt"
	"testing"
)

// Schema.Source: "for the object Media.Schema makes from Swagger 2.0
// formData parameters, it is the Operation's Source", while each field's
// own schema "points to the parameter". This holds with formData
// parameters declared on the path, on the operation, and through a
// Reference Object into another document.
func TestSchemaSourceSwaggerFormData(t *testing.T) {
	others := map[string]string{
		verDocBase + "params.json": `{"Upload":{"name":"upload","in":"formData","type":"file","required":true}}`,
	}
	doc := `{"swagger":"2.0","info":{"title":"F","version":"1"},"host":"api.example.test","paths":{
		"/files/{id}":{
			"parameters":[{"name":"id","in":"path","required":true,"type":"string"},{"name":"tag","in":"formData","type":"string"}],
			"post":{"consumes":["multipart/form-data","application/x-www-form-urlencoded"],
				"parameters":[{"$ref":"params.json#/Upload"},{"name":"n","in":"formData","type":"integer","minimum":1}],
				"responses":{"204":{"description":"none"}}},
			"put":{"consumes":["application/x-www-form-urlencoded"],
				"parameters":[{"name":"n","in":"formData","type":"integer"}],
				"responses":{"204":{"description":"none"}}}}}}`
	for _, yaml := range []bool{false, true} {
		t.Run(fmt.Sprintf("yaml=%v", yaml), func(t *testing.T) {
			c := verParse(t, doc, others, yaml)
			for _, key := range []string{"POST /files/{id}", "PUT /files/{id}"} {
				op := mustOp(t, c, key)
				if op.Body == nil || len(op.Body.Media) == 0 {
					t.Fatalf("%s: no formData body", key)
				}
				for _, m := range op.Body.Media {
					if m.Schema == nil {
						t.Fatalf("%s %s: no schema", key, m.Type)
					}
					if got := m.Schema.Source(); got != op.Source {
						t.Errorf("%s %s: formData schema Source = %q, want the Operation's %q", key, m.Type, got, op.Source)
					}
					if m.Schema.Version() != "2.0" {
						t.Errorf("%s %s: Version() = %q", key, m.Type, m.Schema.Version())
					}
					for _, field := range m.Encoding {
						if field.Schema == nil {
							t.Errorf("%s %s field %s: no schema", key, m.Type, field.Name)
							continue
						}
						if field.Schema.Source() != field.Source {
							t.Errorf("%s %s field %s: Source = %q, want the parameter's %q", key, m.Type, field.Name, field.Schema.Source(), field.Source)
						}
					}
				}
			}
		})
	}
}
