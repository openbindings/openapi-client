package openapi_test

import "testing"

// YAML 1.2.2 section 5.4 treats NEL, LS and PS as non-break characters in
// syntax as well as scalar content. Sections 6.6, 6.8.2, 6.9.2 and 7.1
// govern comments, tag directives, anchors and aliases. These cases cover
// scanner contexts the existing scalar/position tests do not exercise.
func TestYAMLNonBreakCharactersInSyntax(t *testing.T) {
	const chars = "\u0085\u2028\u2029"
	for _, tt := range []struct{ name, doc, want string }{
		{"anchor and alias", yamlHead + "x-a: &a" + chars + " value\nx-v: *a" + chars + "\n", `"value"`},
		{"comment remains comment", yamlHead + "# note " + chars + "x-b: .inf\nx-v: true\n", `true`},
		{"tag directive comment", "%TAG !core! tag:yaml.org,2002: # note " + chars + "\n---\n" + yamlHead + "x-v: !core!str 12\n", `"12"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := parsed(t, []byte(tt.doc))
			sameJSON(t, tt.name, c.Document(testDocURI+"#/x-v"), []byte(tt.want))
		})
	}
}
