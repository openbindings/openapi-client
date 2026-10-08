package openapi_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// base64Parts is a multipart body of two base64 parts: the first holds
// data, the second "QQ==", which is "A".
func base64Parts(data string) string {
	const head = "Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n"
	return "--b\r\n" + head + data + "\r\n--b\r\n" + head + "QQ==\r\n--b--\r\n"
}

// readChunks lists the read sizes the tests deliver a body in: the whole
// body as the reader asks, and every size from 1 to 8 bytes, so that a
// base64 quantum and its padding fall on every side of a read boundary.
var readChunks = [][]int{nil, {1}, {2}, {3}, {4}, {5}, {6}, {7}, {8}}

// A base64 part whose padding is missing, incomplete or misplaced, or that
// holds a character outside the alphabet, is an ErrItem however the body
// arrives, its reads cut at any byte, and the next part is read. Padding is
// misplaced anywhere but at the end of the data, where RFC 4648 section 4
// places it. stream.go, Items: "base64 is read with its padding required,
// ignoring only spaces, tabs, CR and LF, so any other character outside its
// alphabet, or missing, incomplete or misplaced padding, makes the part an
// ErrItem."
func TestBase64PaddingRefusedAtAnyReadBoundary(t *testing.T) {
	bad := []string{
		// misplaced padding
		"YQ==YQ==",
		"YWJjYQ==YWJj",
		"YQ==\r\nYQ==",
		"YWJj\r\nYQ==\r\nYWJj",
		"YQ===",
		"Y=Q=",
		"YQ=a",
		// missing or incomplete padding
		"YQ",
		"YQ=",
		"YWJjYQ",
		// a character outside the alphabet
		"YW*j",
		"YWJj!",
	}
	for _, data := range bad {
		for _, prefix := range []string{"", "AAAA", "AAAAAAAA"} {
			for _, chunks := range readChunks {
				t.Run(fmt.Sprintf("%q after %d bytes, reads of %v", data, len(prefix), chunks), func(t *testing.T) {
					r, _ := streamedResponse(t, "multipart/mixed; boundary=b", base64Parts(prefix+data), nil, chunks...)
					values, errs := streamCollect[[]byte](r)
					if len(values) != 2 || !errors.Is(errs[0], openapi.ErrItem) || errs[1] != nil || string(values[1]) != "A" {
						t.Errorf("Items[[]byte] yielded %q, %v; want an ErrItem, then \"A\"", values, errs)
					}
				})
			}
		}
	}
}

// Well-formed base64, its lines broken by CRLF, decodes at every read
// boundary: the base64 is read "ignoring only spaces, tabs, CR and LF"
// (stream.go, Items).
func TestBase64LinesDecodeAtAnyReadBoundary(t *testing.T) {
	for _, tt := range []struct{ data, want string }{
		{"YWJjZGVm", "abcdef"},
		{"YWJj\r\nZGVm\r\nYQ==", "abcdefa"},
		{"YWI=", "ab"},
	} {
		for _, chunks := range readChunks {
			t.Run(fmt.Sprintf("%q, reads of %v", tt.data, chunks), func(t *testing.T) {
				r, _ := streamedResponse(t, "multipart/mixed; boundary=b", base64Parts(tt.data), nil, chunks...)
				values, errs := streamCollect[[]byte](r)
				streamNoErrors(t, errs)
				if len(values) != 2 || string(values[0]) != tt.want || string(values[1]) != "A" {
					t.Errorf("Items[[]byte] yielded %q; want %q, then \"A\"", values, []string{tt.want, "A"})
				}
			})
		}
	}
}
