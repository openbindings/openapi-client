//go:build unix

package openapi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// A document read from a file that is not a regular file, a FIFO, which may
// block (see also regress_ip4f8_test.go). load.go, Loader: file URLs are read
// "from disk"; Load: "ctx bounds the whole load":
// a FIFO a writer fills loads, and one no writer opens ends the load with
// its context (index.go:305).
func TestIP4F8LoadFromAFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := func(name string) string {
		p := filepath.Join(dir, name)
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		return p
	}
	full := fifo("full")
	go func() {
		f, err := os.OpenFile(full, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer f.Close()
		f.WriteString(bare31(`"/x":{"get":{"operationId":"x"}}`))
	}()
	c, err := openapi.Load(t.Context(), full, nil)
	if err != nil {
		t.Fatalf("Load from a FIFO: %v", err)
	}
	mustOp(t, c, "x")

	empty := fifo("empty")
	t.Cleanup(func() { // let the reader blocked in open go
		if f, err := os.OpenFile(empty, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if c, err := openapi.Load(ctx, empty, nil); !errors.Is(err, context.DeadlineExceeded) || c != nil {
		t.Errorf("Load from a FIFO no one writes = %v, %v; want an error matching context.DeadlineExceeded", c, err)
	}
}
