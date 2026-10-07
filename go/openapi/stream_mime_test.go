package openapi_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// RFC 2045 §6.8 requires MIME base64 whitespace to be ignored. The
// MaxItemBytes bound counts decoded bytes; raw Parts continue to follow
// NextPart exactly.
func TestItemsMultipartBase64Whitespace(t *testing.T) {
	for _, tc := range []struct{ encoded, want string }{
		{"Y WJj", "abc"}, {"YW\tJj", "abc"}, {"Y\r\n W\tJj", "abc"}, {" Y Q =\t=\r\n", "a"},
	} {
		for _, chunk := range []int{1, 2048} {
			t.Run(fmt.Sprintf("%q/chunk=%d", tc.encoded, chunk), func(t *testing.T) {
				wire := streamMultipart("Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\n"+tc.encoded, "Content-Type: text/plain\r\n\r\nx")
				r, _ := streamedResponse(t, "multipart/mixed; boundary=B", wire, func(o *openapi.Options) { o.MaxItemBytes = int64(len(tc.want)) }, chunk)
				got, errs := streamCollect[string](r)
				streamNoErrors(t, errs)
				if !reflect.DeepEqual(got, []string{tc.want, "x"}) {
					t.Fatalf("decoded %q", got)
				}
				// Base64 remains encoded for the raw standard-library Part path.
				r, _ = streamedResponse(t, "multipart/mixed; boundary=B", wire, func(o *openapi.Options) { o.MaxItemBytes = 1 }, chunk)
				oracle := multipart.NewReader(strings.NewReader(wire), "B")
				count := 0
				for part, err := range openapi.Items[*multipart.Part](r) {
					if err != nil {
						t.Fatal(err)
					}
					want, e := oracle.NextPart()
					if e != nil {
						t.Fatal(e)
					}
					p, pe := io.ReadAll(part)
					q, qe := io.ReadAll(want)
					if pe != nil || qe != nil || !bytes.Equal(p, q) || !reflect.DeepEqual(part.Header, want.Header) {
						t.Fatalf("raw part %q/%q headers %v/%v errors %v/%v", p, q, part.Header, want.Header, pe, qe)
					}
					count++
				}
				if count != 2 {
					t.Fatalf("raw parts %d", count)
				}
			})
		}
	}
	t.Run("decoded bound", func(t *testing.T) {
		r, _ := streamedResponse(t, "multipart/mixed; boundary=B", streamMultipart("Content-Transfer-Encoding: base64\r\n\r\nY \tWJj"), func(o *openapi.Options) { o.MaxItemBytes = 2 }, 1)
		_, errs := streamCollect[[]byte](r)
		var bound *http.MaxBytesError
		if len(errs) != 1 || !errors.As(errs[0], &bound) || bound.Limit != 2 || errors.Is(errs[0], openapi.ErrItem) {
			t.Fatalf("bound error %v", errs)
		}
	})
	for _, encoded := range []string{"%%%", "YQ=", "YQ===", "Y=Q="} {
		t.Run("malformed "+encoded, func(t *testing.T) {
			r, _ := streamedResponse(t, "multipart/mixed; boundary=B", streamMultipart("Content-Transfer-Encoding: base64\r\n\r\n"+encoded, "\r\nafter"), nil, 1)
			got, errs := streamCollect[[]byte](r)
			if len(errs) != 2 || !errors.Is(errs[0], openapi.ErrItem) || errs[1] != nil || string(got[1]) != "after" {
				t.Fatalf("parts %q errors %v", got, errs)
			}
		})
	}
	t.Run("source error before MIME end", func(t *testing.T) {
		boom := errors.New("caller source ended mid-base64")
		wire := "--B\r\nContent-Transfer-Encoding: base64\r\n\r\nY \t%%%"
		body := &streamBody{reader: &streamChunks{data: wire, terminal: boom}}
		c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) {
			return streamHTTP(r, 200, "multipart/mixed; boundary=B", body), nil
		}), nil)
		r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		_, errs := streamCollect[[]byte](r)
		if len(errs) != 1 || !errors.Is(errs[0], boom) || errors.Is(errs[0], openapi.ErrItem) {
			t.Fatalf("source error lost or recovered: %v", errs)
		}
	})
}

// Items' empty-body promise means zero wire bytes, not an empty real part
// and not nonempty malformed MIME. Check both ordinary and raw consumers.
func TestItemsMultipartEmptyBodyAndEmptyPart(t *testing.T) {
	for _, ct := range []string{"multipart/mixed; boundary=B", "multipart/digest; boundary=B"} {
		for _, raw := range []bool{false, true} {
			for _, tc := range []struct {
				name, wire string
				count      int
				bad        bool
			}{
				{"zero wire bytes", "", 0, false},
				{"empty real part", streamMultipart("Content-Type: text/plain\r\n\r\n"), 1, false},
				{"nonempty preamble", "x", 1, true},
			} {
				t.Run(fmt.Sprintf("%s/raw=%t/%s", ct, raw, tc.name), func(t *testing.T) {
					r, body := streamedResponse(t, ct, tc.wire, nil, 1)
					var errs []error
					if raw {
						for part, err := range openapi.Items[*multipart.Part](r) {
							errs = append(errs, err)
							if err == nil {
								p, e := io.ReadAll(part)
								if e != nil || len(p) != 0 {
									t.Fatalf("empty part bytes %q error %v", p, e)
								}
							}
						}
					} else {
						got, es := streamCollect[[]byte](r)
						errs = es
						if !tc.bad && len(got) == 1 && len(got[0]) != 0 {
							t.Fatalf("empty part bytes %q", got)
						}
					}
					if len(errs) != tc.count {
						t.Fatalf("yields %d want %d: %v", len(errs), tc.count, errs)
					}
					if tc.bad {
						oracle := multipart.NewReader(strings.NewReader(tc.wire), "B")
						var oracleErr error
						if raw {
							_, oracleErr = oracle.NextRawPart()
						} else {
							_, oracleErr = oracle.NextPart()
						}
						if oracleErr == nil || oracleErr == io.EOF {
							t.Fatalf("fixture must produce a non-EOF parser error: %v", oracleErr)
						}
						if errs[0] == nil || errors.Is(errs[0], openapi.ErrItem) {
							t.Fatalf("nonempty malformed body must fail terminally: %v", errs)
						}
					} else {
						streamNoErrors(t, errs)
					}
					if body.closed.Load() == 0 {
						t.Fatal("body not closed")
					}
				})
			}
		}
	}
}
