package openapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// cancelAfterSnapshot deterministically models cancellation between an Err
// call reading its result and returning it. Done and all later Err calls
// observe the underlying context's actual canceled state.
type cancelAfterSnapshot struct {
	context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelAfterSnapshot) Err() error {
	err := c.Context.Err()
	if err == nil {
		c.once.Do(c.cancel)
	}
	return err
}

// load.go, Load: "ctx bounds the whole load, reading and parsing included";
// Loader.Parse follows the package Parse, which fails as Load does. A
// cancellation after the first check must return the context's error, even
// when YAML parsing has not begun. It must not become a document defect.
func TestYAMLParseCancellationAfterInitialCheck(t *testing.T) {
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &cancelAfterSnapshot{Context: base, cancel: cancel}
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("Parse panicked after cancellation: %v", p)
		}
	}()
	c, err := (&openapi.Loader{}).Parse(ctx, []byte(yamlHead), testDocURI, nil)
	if c != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Parse after cancellation = %v, %v; want nil, context.Canceled", c, err)
	}
}
