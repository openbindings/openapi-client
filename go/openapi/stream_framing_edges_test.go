package openapi_test

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Items promises delivery as records arrive. RFC 7464's terminating LF
// suffices without waiting for another record separator or connection EOF.
func TestJSONSequenceRecordDeliveredAtLF(t *testing.T) {
	for _, record := range []string{"{\"a\":1}\n", "{\n\"a\":[1,\n2]\n}\n", "\"a\\\"b\"\n", "123\n", "true\n", "null\n"} {
		t.Run(record, func(t *testing.T) {
			pr, pw := io.Pipe()
			defer pw.Close()
			c := streamClient(t, "3.2.0", streamRT(func(r *http.Request) (*http.Response, error) {
				return streamHTTP(r, 200, "application/json-seq", pr), nil
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
func TestMultipartMissingCloseDelimiter(t *testing.T) {
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
				r, _ := streamedResponse(t, "multipart/mixed; boundary=b", tc.wire, nil, chunk)
				var errs []error
				if raw {
					_, errs = streamCollect[*multipart.Part](r)
				} else {
					_, errs = streamCollect[[]byte](r)
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
func TestJSONSequenceSuffixWithoutRS(t *testing.T) {
	r, _ := streamedResponse(t, "application/json-seq", "\x1e{}\n \t\n42\n\x1e[2]\n", nil, 1)
	values, errs := streamCollect[any](r)
	if len(values) != 3 || errs[0] != nil || !errors.Is(errs[1], openapi.ErrItem) || errs[2] != nil {
		t.Fatalf("values=%v errors=%v", values, errs)
	}
}

// Raw records also stop at the completed text's LF. Extra JSON whitespace
// is consumed before the next RS, independently of transport read sizes.
func TestJSONSequenceRawRecordWhitespace(t *testing.T) {
	for _, chunk := range []int{1, 3, 4096} {
		r, _ := streamedResponse(t, "application/json-seq", "\x1e{}\n  \t\n\x1e[]\n", nil, chunk)
		got, errs := streamCollect[[]byte](r)
		streamNoErrors(t, errs)
		if !reflect.DeepEqual(got, [][]byte{[]byte("{}\n"), []byte("[]\n")}) {
			t.Fatalf("chunk %d: records %q", chunk, got)
		}
	}
}
