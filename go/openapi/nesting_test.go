package openapi_test

import (
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// doc.go, Values: a value "whose JSON, a MarshalJSON's output included, nests
// deeper than 1,000 levels, the outermost value being level 1" is refused "at
// the key of the body, field, part, sequential item or parameter that is or
// holds it", the bound documents have too (load.go, Loader: "nests deeper
// than 1,000 levels (the outermost value being level 1)"): a parameter or body
// value up to 1,000 levels deep is serialized, and one 1,001 levels deep is
// refused at its key (a parameter's Param.Key, or "Input.Body").
//
// Levels are counted as the Loader counts them for documents: the outermost
// value is level 1, and each value is one level deeper than its container,
// a scalar leaf included. So n nested empty
// arrays are n levels, and n objects around a leaf are n+1: 999 objects
// around a leaf (1,000 levels) are sent, and 1,000 (1,001 levels) are
// refused at "p".
func TestValueNestingLimit(t *testing.T) {
	c := parseAt(t, doc31(`
		"/d":{"get":{"operationId":"deep","parameters":[{"name":"p","in":"query","style":"deepObject","schema":{}}]}},
		"/q":{"get":{"operationId":"content","parameters":[{"name":"p","in":"query","content":{"application/json":{}}}]}},
		"/b":{"post":{"operationId":"body","requestBody":{"content":{"application/json":{}}}}}`), "https://api.example.test", testDocURI, nil)
	arrays := func(n int) any {
		var v any = []any{}
		for range n - 1 {
			v = []any{v}
		}
		return v
	}
	objects := func(n int) any {
		var v any = "leaf"
		for range n {
			v = map[string]any{"a": v}
		}
		return v
	}
	refusedAt := func(t *testing.T, key string, in *openapi.Input, want string) {
		t.Helper()
		req, err := c.Prepare(key, in)
		if req != nil {
			t.Errorf("%s: prepared a value deeper than 1,000 levels", key)
		}
		if err == nil {
			return
		}
		wantKeys(t, key+" Inputs", asRequestError(t, err).Inputs, true, want)
	}

	t.Run("deepObject", func(t *testing.T) {
		req := mustPrepare(t, c, "deep", &openapi.Input{Params: map[string]any{"p": objects(999)}})
		if want := "/d?p" + strings.Repeat("%5Ba%5D", 999) + "=leaf"; req.HTTP.URL.RequestURI() != want {
			t.Errorf("999 objects: request target %.80q..., want %.80q...", req.HTTP.URL.RequestURI(), want)
		}
		refusedAt(t, "deep", &openapi.Input{Params: map[string]any{"p": objects(1000)}}, "p")
	})
	t.Run("JSON content parameter", func(t *testing.T) {
		req := mustPrepare(t, c, "content", &openapi.Input{Params: map[string]any{"p": arrays(1000)}})
		if want := "/q?p=" + strings.Repeat("%5B", 1000) + strings.Repeat("%5D", 1000); req.HTTP.URL.RequestURI() != want {
			t.Errorf("1,000 levels: request target %.80q..., want %.80q...", req.HTTP.URL.RequestURI(), want)
		}
		refusedAt(t, "content", &openapi.Input{Params: map[string]any{"p": arrays(1001)}}, "p")
	})
	t.Run("JSON body", func(t *testing.T) {
		req := mustPrepare(t, c, "body", &openapi.Input{Body: arrays(1000)})
		body, err := io.ReadAll(req.HTTP.Body)
		if want := strings.Repeat("[", 1000) + strings.Repeat("]", 1000); err != nil || string(trimNL(body)) != want {
			t.Errorf("1,000 levels: %.80q..., %v; want %.80q...", body, err, want)
		}
		refusedAt(t, "body", &openapi.Input{Body: arrays(1001)}, "Input.Body")
	})
}
