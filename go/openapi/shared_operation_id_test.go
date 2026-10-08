package openapi_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// describe.go, Client.Operation: "when several share it as their
// operationId, the error wraps ErrNoOperation too and names those operations,
// each once by its Operation.Key, and no other, and each is still reached by
// its Key"; errors.go, ErrNoOperation: "Both Client.Operation and a refused
// call return it". An OpenAPI 3.2 additional operation whose method is a fixed
// one is not an operation (describe.go, Client.Operations: "which OpenAPI
// forbids"), has no Key, and is not named, even when it repeats a shared
// operationId; nor does it make an operationId of one operation shared.
func TestSharedOperationIDNamesOnlyOperations(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.0", `
		"/a":{"get":{"operationId":"dup"},"additionalOperations":{"GET":{"operationId":"dup"}}},
		"/b":{"post":{"operationId":"dup"}},
		"/c":{"get":{"operationId":"one"},"additionalOperations":{"PUT":{"operationId":"one"}}}`), nil)
	_, opErr := c.Operation("dup")
	_, callErr := c.Prepare("dup", nil)
	for what, err := range map[string]error{"Operation": opErr, "Prepare": callErr} {
		if !errors.Is(err, openapi.ErrNoOperation) {
			t.Fatalf("%s(dup) = %v; want an error wrapping ErrNoOperation", what, err)
		}
		text := err.Error()
		for _, key := range []string{"GET /a", "POST /b"} {
			if n := strings.Count(text, key); n != 1 {
				t.Errorf("%s(dup): %q names %s %d times, want once", what, text, key, n)
			}
		}
		for _, op := range []string{"GET /a", "POST /b"} {
			mustOp(t, c, op)
		}
	}
	if op := mustOp(t, c, "one"); op.Key != "one" || op.Path != "/c" || op.Method != "GET" {
		t.Errorf("Operation(one) = %s %s, Key %q; want GET /c, Key one", op.Method, op.Path, op.Key)
	}
}
