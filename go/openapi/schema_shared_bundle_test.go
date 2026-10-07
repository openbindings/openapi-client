package openapi_test

import (
	"fmt"
	"slices"
	"testing"
)

// Loader gives a versionless reference target its reference's object type;
// Param.Schema identifies its declared schema, and Client.Schema exposes that
// schema in the loaded graph without fetching. OAS 3.1.2 section 4.3.2 likewise
// assigns reference targets their source context. Distinct Parameter fragments
// reached from distinct external Path Items must both retain their contexts.
// These schemas have no identifiers or references that could seed their type
// independently. URI/member order is varied without controlling goroutines.
func TestSchemaSharedBundleParameterContexts(t *testing.T) {
	const aURI = "https://schemas.example.test/api/a.json"
	const bURI = "https://schemas.example.test/api/b.json"
	const rawA = `{"type":"string"}`
	const rawB = `{"type":"integer"}`
	for _, version := range []string{"3.1.2", "3.2.1"} {
		for _, bundleName := range []string{"0-bundle.json", "z.json"} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/reverse=%t", version, bundleName, reverse), func(t *testing.T) {
					bundle := "https://schemas.example.test/api/" + bundleName
					fetch := newMemFetch(map[string]string{
						aURI:   `{"get":{"operationId":"a","parameters":[{"$ref":"` + bundle + `#/A"}],"responses":{"200":{"description":"ok"}}}}`,
						bURI:   `{"get":{"operationId":"b","parameters":[{"$ref":"` + bundle + `#/B"}],"responses":{"200":{"description":"ok"}}}}`,
						bundle: `{"A":{"name":"a","in":"query","schema":` + rawA + `},"B":{"name":"b","in":"query","schema":` + rawB + `}}`,
					})
					aPath, bPath := `"/a":{"$ref":"a.json"}`, `"/b":{"$ref":"b.json"}`
					paths := aPath + "," + bPath
					if reverse {
						paths = bPath + "," + aPath
					}
					c := schemaGraphParse(t, `{"openapi":"`+version+`","info":{"title":"Shared context","version":"1"},"paths":{`+paths+`}}`, fetch)
					before := fetch.callList()
					uris, wantURIs := c.DocumentURIs(), []string{schemaGraphEntry, aURI, bURI, bundle}
					slices.Sort(uris)
					slices.Sort(wantURIs)
					if !slices.Equal(uris, wantURIs) {
						t.Fatalf("loaded documents=%q want %q", uris, wantURIs)
					}
					dialect := dialectOAS31
					if version == "3.2.1" {
						dialect = dialectOAS32
					}
					cases := []struct{ name, operation, raw string }{{"A", "a", rawA}, {"B", "b", rawB}}
					// Direct lookup precedes every descriptor read, so descriptors
					// cannot first establish a missing schema context as a side effect.
					for range 2 {
						for _, tt := range cases {
							source := bundle + "#/" + tt.name + "/schema"
							s, err := c.Schema(source)
							if err != nil || s == nil {
								t.Errorf("Schema(%q)=%v, %v", source, s, err)
								continue
							}
							if s.Source() != source || s.Base() != bundle || s.Dialect() != dialect || string(s.Raw()) != tt.raw {
								t.Errorf("%s metadata: Source=%q Base=%q Dialect=%q Raw=%s", tt.name, s.Source(), s.Base(), s.Dialect(), s.Raw())
							}
							if refs := schemaGraphRefs(t, s); len(refs) != 0 {
								t.Errorf("%s reference-free schema has edges: %+v", tt.name, refs)
							}
						}
					}
					for _, tt := range cases {
						op := mustOp(t, c, tt.operation)
						if op.Err != nil || len(op.Params) != 1 || op.Params[0].Schema == nil {
							t.Fatalf("%s descriptor=%+v", tt.name, op)
						}
						p := op.Params[0]
						if p.Err != nil || p.Source != bundle+"#/"+tt.name || p.Schema.Source() != p.Source+"/schema" || string(p.Schema.Raw()) != tt.raw {
							t.Errorf("%s descriptor schema differs: param Source=%q Err=%v schema Source=%q Raw=%s", tt.name, p.Source, p.Err, p.Schema.Source(), p.Schema.Raw())
						}
					}
					if !slices.Equal(fetch.callList(), before) {
						t.Errorf("accessors fetched: before=%q after=%q", before, fetch.callList())
					}
				})
			}
		}
	}
}
