package openapi_test

import (
	"bytes"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// codedClient serves every request with a 200 of type ct and body, each
// element of lines written as a Content-Encoding field line of its own,
// to an operation "get" whose 200 response declares */*.
func codedClient(t *testing.T, ct, body string, lines []string) *openapi.Client {
	t.Helper()
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", ct)
		rw.Header()["Content-Encoding"] = lines
		rw.WriteHeader(200)
		rw.Write([]byte(body))
	})
	return parseFor(t, w, doc31(`"/x":{"get":{"operationId":"get","responses":{"200":{"description":"ok","content":{"*/*":{}}}}}}`), nil)
}

// Every coding in every Content-Encoding field line counts, as RFC 9110
// section 5.3 makes the field lines of one name a single list: a coding
// other than identity in any of them leaves the body undecodable where the
// docs say, and passes it through unchanged where they say. The bodies are
// uncoded bytes that would decode, so only reading every field line refuses
// them. Net/http's transport removes gzip only when the first field line is
// exactly gzip, which none of these is. doc.go, Fixed rules, Content
// codings: "A body whose remaining Content-Encoding names a coding other than
// identity passes through unchanged to a *[]byte or io.Writer, is discarded
// for a nil out, and is framed with its coding still applied by Items with a
// T of []byte, or of *multipart.Part for a multipart body; any other target,
// Items with any other T, and Events report a non-identity Content-Encoding
// error."
func TestContentCodingInAnyFieldLine(t *testing.T) {
	for _, lines := range [][]string{
		{"identity", "gzip"},
		{"", "gzip"},
		{"gzip, identity"},
		{"identity, gzip"},
		{"identity", "identity, br"},
	} {
		t.Run(strings.Join(lines, "|"), func(t *testing.T) {
			t.Run("text", func(t *testing.T) {
				c := codedClient(t, "text/plain", "hello", lines)
				var raw []byte
				resp := mustCall(t, c, "get", nil, &raw)
				if got := resp.Header["Content-Encoding"]; !slices.Equal(got, lines) {
					t.Fatalf("Content-Encoding field lines received %q, want %q", got, lines)
				}
				if string(raw) != "hello" {
					t.Errorf("*[]byte = %q, want the body unchanged", raw)
				}
				var buf bytes.Buffer
				mustCall(t, c, "get", nil, &buf)
				if buf.String() != "hello" {
					t.Errorf("io.Writer received %q, want the body unchanged", buf.String())
				}
				mustCall(t, c, "get", nil, nil)

				var s string
				_, err := c.Call(t.Context(), "get", nil, &s)
				wantCodingError(t, "*string", err)
				var a any
				_, err = c.Call(t.Context(), "get", nil, &a)
				wantCodingError(t, "*any", err)
			})
			t.Run("json", func(t *testing.T) {
				c := codedClient(t, "application/json", `{"name":"Rex"}`, lines)
				var m map[string]any
				_, err := c.Call(t.Context(), "get", nil, &m)
				wantCodingError(t, "*map[string]any", err)
				var a any
				_, err = c.Call(t.Context(), "get", nil, &a)
				wantCodingError(t, "*any", err)
			})
			t.Run("items", func(t *testing.T) {
				c := codedClient(t, "application/jsonl", "1\n2\n", lines)
				values, errs := streamItems[int](t, c)
				if len(errs) == 0 || slices.Contains(errs, nil) {
					t.Errorf("Items[int] yielded %v, %v; want only the coding error", values, errs)
				}
				raw, errs := streamItems[[]byte](t, c)
				if len(raw) != 2 || string(raw[0]) != "1" || string(raw[1]) != "2" || errs[0] != nil || errs[1] != nil {
					t.Errorf("Items[[]byte] yielded %q, %v; want the two lines as they are", raw, errs)
				}
			})
			t.Run("events", func(t *testing.T) {
				c := codedClient(t, "text/event-stream", "data: hello\n\n", lines)
				r, err := c.Stream(t.Context(), "get", nil)
				if err != nil {
					t.Fatalf("Stream: %v", err)
				}
				defer r.Body.Close()
				n := 0
				for ev, err := range openapi.Events(r) {
					n++
					if err == nil {
						t.Errorf("Events yielded the event %q", ev.Data)
					}
				}
				if n == 0 {
					t.Errorf("Events yielded nothing; want the coding error")
				}
			})
		})
	}
}

// The same holds of a multipart part's own header: a part with a coding
// other than identity in any of its Content-Encoding field lines is an
// ErrItem for a T other than []byte or *multipart.Part, and the next part is
// read; a []byte receives the part as it is. stream.go, Items: multipart
// types give "one per part, decoded by the part's own Content-Type as Call
// decodes a body"; "An error that concerns one item wraps [ErrItem] and is
// yielded in its place, and the iteration goes on."
func TestPartContentCodingInAnyFieldLine(t *testing.T) {
	for _, lines := range [][]string{
		{"identity", "gzip"},
		{"gzip, identity"},
		{"identity, gzip"},
	} {
		t.Run(strings.Join(lines, "|"), func(t *testing.T) {
			var head strings.Builder
			head.WriteString("Content-Type: text/plain\r\n")
			for _, l := range lines {
				head.WriteString("Content-Encoding: " + l + "\r\n")
			}
			body := "--b\r\n" + head.String() + "\r\nhello\r\n--b\r\nContent-Type: text/plain\r\n\r\nok\r\n--b--\r\n"
			c := codedClient(t, "multipart/mixed; boundary=b", body, nil)
			values, errs := streamItems[string](t, c)
			if len(values) != 2 || !errors.Is(errs[0], openapi.ErrItem) || errs[1] != nil || values[1] != "ok" {
				t.Errorf("Items[string] yielded %q, %v; want an ErrItem, then \"ok\"", values, errs)
			}
			raw, errs := streamItems[[]byte](t, c)
			if len(raw) != 2 || string(raw[0]) != "hello" || string(raw[1]) != "ok" || errs[0] != nil || errs[1] != nil {
				t.Errorf("Items[[]byte] yielded %q, %v; want the parts as they are", raw, errs)
			}
		})
	}
}

// Identity in every field line, whatever its case, names no other coding:
// the body decodes (doc.go, Fixed rules, Content codings, as above).
func TestIdentityInEveryFieldLine(t *testing.T) {
	lines := []string{"identity", "IDENTITY, Identity"}
	c := codedClient(t, "text/plain", "hello", lines)
	var s string
	mustCall(t, c, "get", nil, &s)
	if s != "hello" {
		t.Errorf("*string = %q, want hello", s)
	}
	c = codedClient(t, "application/jsonl", "1\n2\n", lines)
	values, errs := streamItems[int](t, c)
	if !slices.Equal(values, []int{1, 2}) || errs[0] != nil || errs[1] != nil {
		t.Errorf("Items[int] yielded %v, %v; want 1 and 2", values, errs)
	}
}

// wantCodingError fails unless err is the *DecodeError that reports a body
// the client cannot decode.
func wantCodingError(t *testing.T, target string, err error) {
	t.Helper()
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Errorf("Call into %s = %v; want a *DecodeError for the coded body", target, err)
	}
}

// streamItems streams "get" from c and collects Items[T].
func streamItems[T any](t *testing.T, c *openapi.Client) ([]T, []error) {
	t.Helper()
	r, err := c.Stream(t.Context(), "get", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer r.Body.Close()
	return streamCollect[T](r)
}
