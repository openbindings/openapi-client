//go:build linux || darwin || freebsd || netbsd || openbsd

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

// load.go, Load: "ctx bounds the whole load, reading and parsing included:
// when it is done before the load completes, Load returns no Client and an
// error that matches ctx.Err() with errors.Is", a file that never delivers its
// content, such as a FIFO nobody writes to, included.
func TestLoadOfSilentFIFOEndsWithContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	for name, cancelled := range map[string]bool{"deadline": false, "cancelled": true} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if cancelled {
				cancel()
			}
			type result struct {
				c   *openapi.Client
				err error
			}
			done := make(chan result, 1)
			go func() {
				c, err := openapi.Load(ctx, path, nil)
				done <- result{c, err}
			}()
			select {
			case r := <-done:
				if r.c != nil || !errors.Is(r.err, ctx.Err()) {
					t.Errorf("Load of a silent FIFO = %v, %v; want no Client "+
						"and an error matching %v", r.c, r.err, ctx.Err())
				}
			case <-time.After(2 * time.Second):
				t.Errorf("Load of a silent FIFO was still blocked 2s after its context ended")
				// Release the reader so the goroutine ends.
				if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
					f.Close()
				}
				<-done
			}
		})
	}
}
