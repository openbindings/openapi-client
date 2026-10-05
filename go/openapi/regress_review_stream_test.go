package openapi_test

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Items promises delivery as records arrive. RFC 7464's terminating LF
// suffices without waiting for another record separator or connection EOF.
func TestReviewRepairJSONSequenceLive(t *testing.T) {
	for _, record := range []string{"{\"a\":1}\n", "{\n\"a\":[1,\n2]\n}\n", "\"a\\\"b\"\n", "123\n", "true\n", "null\n"} {
		t.Run(record, func(t *testing.T) {
			pr, pw := io.Pipe()
			defer pw.Close()
			c := stream7Client(t, "3.2.0", stream7RT(func(r *http.Request) (*http.Response, error) {
				return stream7HTTP(r, 200, "application/json-seq", pr), nil
			}), nil)
			r, err := c.Stream(t.Context(), "get", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			got := make(chan error, 1)
			go func() {
				for _, err := range openapi.Items[any](r) {
					got <- err
					return
				}
			}()
			go pw.Write([]byte("\x1e" + record))
			select {
			case err := <-got:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("complete record withheld until next separator")
			}
		})
	}
}

// RFC 2046 requires a closing delimiter. A regular delimiter followed by
// EOF is a truncated header block, for both decoded items and raw parts.
func TestReviewRepairMultipartTruncation(t *testing.T) {
	for _, tc := range []struct {
		wire string
		bad  bool
	}{
		{"--b\r\n", true},
		{"--b\r\n\r\nhello\r\n--b\r\n", true},
		{"--b\r\n\r\nhello\r\n--b--", false},
		{"--b\r\n\r\nhello\r\n--b-- \t\r\nepilogue", false},
		{"--b\r\nContent-Type: text/plain\r\n", true},
		{"--b\r\n\r\nhello\r\n--b--X\r\n", true},
		{"--b\n\nhello\n--b--\nepilogue", false},
		{"", false},
	} {
		for _, chunk := range []int{1, 2, 3, 4, 5, 4096} {
			for _, raw := range []bool{false, true} {
				r, _ := stream7Response(t, "multipart/mixed; boundary=b", tc.wire, nil, chunk)
				var errs []error
				if raw {
					_, errs = stream7Collect[*multipart.Part](r)
				} else {
					_, errs = stream7Collect[[]byte](r)
				}
				var failed bool
				for _, err := range errs {
					failed = failed || err != nil
				}
				if failed != tc.bad {
					t.Errorf("%q, chunk %d: errors %v", tc.wire, chunk, errs)
				}
			}
		}
	}
}

// A newline can end one record, but never authorizes a second JSON text
// without RS. Recovery resumes at the next RS, including after bad suffixes.
func TestReviewRepairJSONSequenceSuffix(t *testing.T) {
	r, _ := stream7Response(t, "application/json-seq", "\x1e{}\n \t\n42\n\x1e[2]\n", nil, 1)
	values, errs := stream7Collect[any](r)
	if len(values) != 3 || errs[0] != nil || !errors.Is(errs[1], openapi.ErrItem) || errs[2] != nil {
		t.Fatalf("values=%v errors=%v", values, errs)
	}
}
