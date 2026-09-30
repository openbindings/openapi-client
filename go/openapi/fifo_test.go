//go:build linux || darwin || freebsd || netbsd || openbsd

package openapi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// G16 (#18), H10 and T1-12: "ctx bounds the whole load, reading and parsing
// included" (load.go, Load), a file that never delivers its content, such
// as a FIFO nobody writes to, included.
func TestG16LoadFIFOHonorsContext(t *testing.T) {
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
			done := make(chan error, 1)
			go func() {
				_, err := openapi.Load(ctx, path, nil)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
					t.Errorf("Load of a silent FIFO = %v, want the context's error", err)
				}
				// H10 (ledger, "Verification pass"): the text says the
				// context ended.
				if err != nil && !strings.Contains(err.Error(), ctx.Err().Error()) {
					t.Errorf("error text %q does not say the context ended (%q)", err, ctx.Err())
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
