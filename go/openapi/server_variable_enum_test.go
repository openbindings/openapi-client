package openapi_test

import (
	"slices"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// describe.go, Variable.Enum: "An enum written as anything but an array of
// strings, a variable's values being strings, makes the server unusable
// (Server.Err), and one written as a reference marks a document meant to be
// bundled first (see Operation)"; describe.go, Operation: such a value "makes
// the nearest part holding that value unusable: the Err of that ... Server
// says the document must be bundled first, and does not wrap ErrUnresolved".
// An unusable sole server leaves a call needing Options.BaseURL (doc.go,
// Configuration: "none requires BaseURL"). An array of strings, empty
// included, is an enum, and the server stays usable.
func TestServerVariableEnumShapes(t *testing.T) {
	doc := func(enum string) string {
		return editionDoc("3.1.0", `"/x":{"get":{"operationId":"x"}}`,
			`"servers":[{"url":"https://{h}.example.test","variables":{"h":{"default":"us","enum":`+enum+`}}}]`)
	}
	for _, enum := range []string{`{"$ref":"#/components/x"}`, `{"a":"us"}`, `"us"`, `5`, `true`, `null`, `[1,2]`, `["us",1]`, `["us",null]`, `["us",["eu"]]`} {
		t.Run(enum, func(t *testing.T) {
			c := editionClient(t, doc(enum), nil)
			s := server(t, mustOp(t, c, "x"), 0)
			if enum == `{"$ref":"#/components/x"}` {
				wantBundled(t, "Server", s.Err)
			} else if s.Err == nil {
				t.Errorf("Server.Err is nil; want the server unusable")
			}
			_, err := c.Prepare("x", nil)
			wantKeys(t, "Settings", asRequestError(t, err).Settings, false, "Options.BaseURL")
		})
	}
	for enum, want := range map[string][]string{`["us","eu"]`: {"us", "eu"}, `[]`: {}} {
		c := editionClient(t, doc(enum), nil)
		s := server(t, mustOp(t, c, "x"), 0)
		if s.Err != nil || s.Variables[0].Enum == nil || !slices.Equal(s.Variables[0].Enum, want) {
			t.Errorf("enum %s: Server.Err %v, Enum %#v; want no Err and Enum %q", enum, s.Err, s.Variables[0].Enum, want)
		}
		mustPrepare(t, c, "x", &openapi.Input{})
	}
}
