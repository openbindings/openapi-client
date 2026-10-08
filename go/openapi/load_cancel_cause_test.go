package openapi_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// errLoadCause is the cause a test gives the load's context when it cancels.
var errLoadCause = errors.New("load cancelled by the test")

// load.go, Load: "ctx bounds the whole load, reading and parsing included:
// when it is done before the load completes, Load returns no Client and an
// error that matches ctx.Err() with errors.Is, and also context.Cause(ctx),
// even while a referenced document, whose failure would otherwise disable
// only what reaches it, is being read"; Parse "fails as Load does". The
// context is cancelled with a cause while Loader.Fetch reads a referenced
// document, while it reads the entry document, and before Parse begins.
func TestLoadCancelMatchesCause(t *testing.T) {
	const entry = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/x":{"$ref":"other.json#/P"}}}`
	check := func(what string, c *openapi.Client, err error) {
		t.Helper()
		if c != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, errLoadCause) {
			t.Errorf("%s: Client %v, error %v; want no Client and an error matching context.Canceled and the cause", what, c != nil, err)
		}
	}
	for _, at := range []string{"https://api.example.test/other.json", "https://api.example.test/openapi.json"} {
		ctx, cancel := context.WithCancelCause(context.Background())
		l := openapi.Loader{Fetch: func(fctx context.Context, uri string) (io.ReadCloser, string, error) {
			if uri == at {
				cancel(errLoadCause)
				<-fctx.Done()
				return nil, "", fctx.Err()
			}
			return io.NopCloser(strings.NewReader(entry)), "", nil
		}}
		c, err := l.Load(ctx, "https://api.example.test/openapi.json", nil)
		check("cancelled while reading "+at, c, err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errLoadCause)
	c, err := openapi.Parse(ctx, []byte(entry), testDocURI, nil)
	check("Parse with a done context", c, err)
}
