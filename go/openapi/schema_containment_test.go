package openapi_test

import (
	"fmt"
	"slices"
	"testing"
)

// References follows schema-bearing vocabulary locations, not every
// independently classified schema physically inside Raw. Core 4.3.1/9.4.2;
// Loader assigns the object type of a referenced fragment from its context.
// A separately reached annotation remains independently usable while it is
// excluded from the enclosing schema's traversal, including foreign
// dialects.
func TestSchemaReferencesAnnotationContainment(t *testing.T) {
	const external = "https://schemas.example.test/api/annotation-bundle.json"
	const target = external + "#/Target"
	for _, annotation := range []struct {
		key, suffix string
		array       bool
	}{
		{"default", "/default", false}, {"const", "/const", false},
		{"example", "/example", false}, {"examples", "/examples/0", true},
		{"x-data", "/x-data", false},
	} {
		for _, foreign := range []bool{false, true} {
			for _, annotationFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/foreign=%t/annotation-first=%t", annotation.key, foreign, annotationFirst), func(t *testing.T) {
					child := `{"$ref":"#/Target"}`
					if foreign {
						child = `{"$schema":"https://dialects.example.test/foreign","customRef":"never.json"}`
					}
					value := child
					if annotation.array {
						value = "[" + child + "]"
					}
					data := fmt.Sprintf("%q:%s", annotation.key, value)
					valid := `"properties":{"z":{"$ref":"#/Target"},"a":{"$ref":"#/Target"}}`
					fields := valid + "," + data
					uses := `"Parent":{"$ref":"annotation-bundle.json#/S"},"Separate":{"$ref":"annotation-bundle.json#/S` + annotation.suffix + `"}`
					if annotationFirst {
						fields = data + "," + valid
						uses = `"Separate":{"$ref":"annotation-bundle.json#/S` + annotation.suffix + `"},"Parent":{"$ref":"annotation-bundle.json#/S"}`
					}
					fetch := newMemFetch(map[string]string{external: `{"S":{` + fields + `},"Target":{"type":"string"}}`})
					c := schemaGraphParse(t, schemaGraphDoc("3.1.2", uses), fetch)
					parent := schemaGraphGet(t, c, external+"#/S")
					separate := schemaGraphGet(t, c, external+"#/S"+annotation.suffix)
					want := []schemaGraphEdge{{"/properties/z/$ref", "$ref", "#/Target", target, target}, {"/properties/a/$ref", "$ref", "#/Target", target, target}}
					// Both accessor orders must preserve the same semantic containment.
					checkParent := func() { schemaGraphWantEdges(t, schemaGraphRefs(t, parent), want) }
					checkSeparate := func() {
						if foreign {
							if _, err := separate.References(); err == nil {
								t.Error("independent foreign schema was not refused")
							}
						} else {
							schemaGraphWantEdges(t, schemaGraphRefs(t, separate), []schemaGraphEdge{{"/$ref", "$ref", "#/Target", target, target}})
						}
					}
					if annotationFirst {
						checkSeparate()
						checkParent()
					} else {
						checkParent()
						checkSeparate()
					}
					checkParent()
					if !slices.Equal(fetch.callList(), []string{external}) {
						t.Errorf("fetches=%q", fetch.callList())
					}
				})
			}
		}
	}
}

// The root is independently referenced as a Schema and a Parameter. The
// package's contextual Loader and References contracts govern this case;
// OAS leaves multiple-object-type interpretation implementation-defined.
// The Parameter's schema field is ordinary annotation in the root Schema,
// while its properties field is a real schema-bearing location. Vary both
// entry object order and root member order, with known/foreign child schemas.
func TestSchemaSharedParameterSchemaContainment(t *testing.T) {
	const external = "https://schemas.example.test/api/shared-context.json"
	const target = schemaGraphEntry + "#/components/schemas/T"
	for _, version := range []string{"3.1.2", "3.2.1"} {
		for _, foreign := range []bool{false, true} {
			for _, schemaFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/foreign=%t/schema-first=%t", version, foreign, schemaFirst), func(t *testing.T) {
					child := `{"$ref":"` + target + `"}`
					if foreign {
						child = `{"$schema":"https://dialects.example.test/foreign","customRef":"never.json"}`
					}
					parameter := `"name":"q","in":"query","schema":` + child
					properties := `"properties":{"actual":{"$ref":"` + target + `"}}`
					shared := parameter + "," + properties
					paths := `"paths":{"/x":{"get":{"operationId":"x","parameters":[{"$ref":"shared-context.json"}],"responses":{"200":{"description":"ok"}}}}}`
					components := `"components":{"schemas":{"Use":{"$ref":"shared-context.json"},"T":{"type":"string"}}}`
					members := paths + "," + components
					if schemaFirst {
						shared = properties + "," + parameter
						members = components + "," + paths
					}
					fetch := newMemFetch(map[string]string{external: "{" + shared + "}"})
					c := schemaGraphParse(t, `{"openapi":"`+version+`","info":{"title":"Contexts","version":"1"},`+members+`}`, fetch)
					op := mustOp(t, c, "x")
					if op.Err != nil || len(op.Params) != 1 || op.Params[0].Schema == nil {
						t.Fatalf("parameter descriptor=%+v", op)
					}
					root := schemaGraphGet(t, c, external)
					for range 2 {
						schemaGraphWantEdges(t, schemaGraphRefs(t, root), []schemaGraphEdge{{"/properties/actual/$ref", "$ref", target, target, target}})
						nested := op.Params[0].Schema
						if nested.Source() != external+"#/schema" {
							t.Errorf("parameter schema Source=%q", nested.Source())
						}
						if foreign {
							if _, err := nested.References(); err == nil {
								t.Error("parameter's foreign schema was not refused")
							}
						} else {
							schemaGraphWantEdges(t, schemaGraphRefs(t, nested), []schemaGraphEdge{{"/$ref", "$ref", target, target, target}})
						}
					}
					if !slices.Equal(fetch.callList(), []string{external}) {
						t.Errorf("fetches=%q", fetch.callList())
					}
				})
			}
		}
	}
}
