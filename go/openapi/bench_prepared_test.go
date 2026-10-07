package openapi_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// This standalone fixture measures the prepared body replay path. It depends
// only on public APIs, so the identical file can be copied into two versions
// of the package to compare them. Request.HTTP/Request.Call promise the
// initial Body once, GetBody thereafter, and reusable sends.
const benchPreparedURL = "https://bench.example.test/upload"
const benchPreparedType = "application/octet-stream"
const benchPreparedSize = 4096
const benchPreparedDoc = `{"openapi":"3.1.0","info":{"title":"prepared body benchmark","version":"1"},"servers":[{"url":"https://bench.example.test"}],"paths":{"/upload":{"post":{"operationId":"upload","requestBody":{"content":{"application/octet-stream":{}}},"responses":{"204":{"description":"done"}}}}}}`

func benchPreparedPayload() []byte {
	return bytes.Repeat([]byte("0123456789abcdef"), benchPreparedSize/16)
}

type benchPreparedTransport struct {
	verify   bool
	verified int
}

func (tr *benchPreparedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var data []byte
	var err error
	if tr.verify {
		data, err = io.ReadAll(r.Body)
	} else {
		_, err = io.Copy(io.Discard, r.Body)
	}
	closeErr := r.Body.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if tr.verify {
		wantHeader := http.Header{"Content-Type": {benchPreparedType}, "X-Benchmark": {"prepared-body"}}
		if r.Method != "POST" || r.URL.String() != benchPreparedURL || !reflect.DeepEqual(r.Header, wantHeader) || r.ContentLength != benchPreparedSize {
			return nil, fmt.Errorf("prepared exchange metadata: %s %s, headers %#v, ContentLength %d", r.Method, r.URL, r.Header, r.ContentLength)
		}
		// The oracle is fixed independently of either prepared request or
		// its GetBody implementation, including a separate backing slice.
		if !bytes.Equal(data, benchPreparedPayload()) {
			return nil, fmt.Errorf("prepared exchange body differs from fixed %d-byte payload (received %d bytes)", benchPreparedSize, len(data))
		}
		tr.verified++
	}
	return &http.Response{
		StatusCode: http.StatusNoContent, Status: "204 No Content",
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: make(http.Header), Body: http.NoBody, ContentLength: 0, Request: r,
	}, nil
}

// benchPreparedPath constructs one actual measured path. Parse, Prepare,
// NewRequest, payload creation and the first body are all outside timing.
func benchPreparedPath(tb testing.TB, path string) (*benchPreparedTransport, func() (*http.Response, error)) {
	tb.Helper()
	ctx := context.Background()
	tr := &benchPreparedTransport{verify: true}
	hc := &http.Client{Transport: tr}
	payload := benchPreparedPayload()
	if path == "Client" {
		c, err := openapi.Parse(ctx, []byte(benchPreparedDoc), "", &openapi.Options{
			HTTPClient: hc, Header: http.Header{"X-Benchmark": {"prepared-body"}},
		})
		if err != nil {
			tb.Fatal(err)
		}
		req, err := c.Prepare("upload", &openapi.Input{Body: payload})
		if err != nil {
			tb.Fatal(err)
		}
		return tr, func() (*http.Response, error) {
			r, err := req.Send(ctx)
			if r == nil {
				return nil, err
			}
			return r.Response, err
		}
	}
	if path != "Hand" {
		tb.Fatalf("unknown prepared benchmark path %q", path)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", benchPreparedURL, bytes.NewReader(payload))
	if err != nil {
		tb.Fatal(err)
	}
	req.Header = http.Header{"Content-Type": {benchPreparedType}, "X-Benchmark": {"prepared-body"}}
	first := true
	return tr, func() (*http.Response, error) {
		// Preserve the prepared request and its original body. Each exchange
		// shallow-clones it, taking GetBody after the first exchange, without
		// encoding or rebuilding the URL and headers.
		r := new(http.Request)
		*r = *req
		if first {
			first = false
		} else {
			var err error
			r.Body, err = req.GetBody()
			if err != nil {
				return nil, err
			}
		}
		return hc.Do(r)
	}
}

func benchPreparedExchange(send func() (*http.Response, error)) error {
	r, err := send()
	if err != nil {
		if r != nil {
			r.Body.Close()
		}
		return err
	}
	return r.Body.Close()
}

func benchPreparedVerify(tb testing.TB, tr *benchPreparedTransport, send func() (*http.Response, error)) {
	tb.Helper()
	for range 2 {
		if err := benchPreparedExchange(send); err != nil {
			tb.Fatal(err)
		}
	}
	if tr.verified != 2 {
		tb.Fatalf("verified %d actual exchanges, want initial body and replay", tr.verified)
	}
	tr.verify = false
}

func TestPreparedSendBodyMatchesHand(t *testing.T) {
	for _, path := range []string{"Client", "Hand"} {
		t.Run(path, func(t *testing.T) {
			tr, send := benchPreparedPath(t, path)
			benchPreparedVerify(t, tr, send)
		})
	}
}

func BenchmarkPreparedSendBody(b *testing.B) {
	for _, path := range []string{"Client", "Hand"} {
		b.Run(path, func(b *testing.B) {
			b.StopTimer()
			tr, send := benchPreparedPath(b, path)
			benchPreparedVerify(b, tr, send)
			b.ReportAllocs()
			b.SetBytes(benchPreparedSize)
			b.ResetTimer()
			b.StartTimer()
			for i := 0; i < b.N; i++ {
				if err := benchPreparedExchange(send); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
