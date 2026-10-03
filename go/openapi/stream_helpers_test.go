package openapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// The stage 7 harness operates entirely at the caller's RoundTripper seam.
// Bodies can fragment reads and return bytes together with EOF or another
// error, both expressly permitted by io.Reader's contract.
type stream7RT func(*http.Request) (*http.Response, error)

func (f stream7RT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type stream7Body struct {
	reader io.Reader
	closed atomic.Int32
	reads  atomic.Int64
}

func (r *stream7Body) Read(p []byte) (int, error) { r.reads.Add(1); return r.reader.Read(p) }
func (r *stream7Body) Close() error               { r.closed.Add(1); return nil }

type stream7Chunks struct {
	data     string
	chunks   []int
	next     int
	terminal error
}

func (r *stream7Chunks) Read(p []byte) (int, error) {
	if r.data == "" {
		if r.terminal != nil {
			return 0, r.terminal
		}
		return 0, io.EOF
	}
	size := len(p)
	if len(r.chunks) > 0 {
		size = min(size, r.chunks[r.next%len(r.chunks)])
		r.next++
	}
	n := copy(p[:size], r.data)
	r.data = r.data[n:]
	if r.data == "" && r.terminal != nil {
		return n, r.terminal
	}
	return n, nil
}

func stream7Doc(version string) string {
	return editionDoc(version, `"/x":{"get":{"operationId":"get","responses":{"200":{"description":"ok","content":{"application/jsonl":{}}},"default":{"description":"other"}}},"post":{"operationId":"post","responses":{"200":{"description":"ok"}}}}`)
}
func stream7Client(t testing.TB, version string, rt http.RoundTripper, change func(*openapi.Options)) *openapi.Client {
	t.Helper()
	o := &openapi.Options{BaseURL: "https://stream.example.test", HTTPClient: &http.Client{Transport: rt}}
	if change != nil {
		change(o)
	}
	return editionClient(t, stream7Doc(version), o)
}
func stream7HTTP(r *http.Request, status int, ct string, body io.ReadCloser) *http.Response {
	h := make(http.Header)
	if ct != "" {
		h.Set("Content-Type", ct)
	}
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: h, Body: body, Request: r, ContentLength: -1}
}
func stream7Response(t testing.TB, ct, data string, change func(*openapi.Options), chunks ...int) (*openapi.Response, *stream7Body) {
	t.Helper()
	body := &stream7Body{reader: &stream7Chunks{data: data, chunks: chunks}}
	c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) { return stream7HTTP(r, 200, ct, body), nil }), change)
	req := mustPrepare(t, c, "get", nil)
	resp, err := req.Send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, body
}
func stream7Collect[T any](r *openapi.Response) ([]T, []error) {
	var values []T
	var errs []error
	for v, err := range openapi.Items[T](r) {
		values = append(values, v)
		errs = append(errs, err)
	}
	return values, errs
}
func stream7NoErrors(t testing.TB, errs []error) {
	t.Helper()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
	}
}
func stream7CallClient(t testing.TB, ct, data string, status int, change func(*openapi.Options)) *openapi.Client {
	t.Helper()
	return stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) {
		return stream7HTTP(r, status, ct, io.NopCloser(strings.NewReader(data))), nil
	}), change)
}
