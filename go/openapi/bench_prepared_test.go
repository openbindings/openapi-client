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

// This standalone fixture measures the prepared body replay path changed in
// Stage 8. It depends only on public APIs, so the identical file can be
// overlaid onto baseline and candidate archives. Request.HTTP/Request.Call
// promise the initial Body once, GetBody thereafter, and reusable sends.
const benchPrepared8URL = "https://bench.example.test/upload"
const benchPrepared8Type = "application/octet-stream"
const benchPrepared8Size = 4096
const benchPrepared8Doc = `{"openapi":"3.1.0","info":{"title":"prepared body benchmark","version":"1"},"servers":[{"url":"https://bench.example.test"}],"paths":{"/upload":{"post":{"operationId":"upload","requestBody":{"content":{"application/octet-stream":{}}},"responses":{"204":{"description":"done"}}}}}}`

func benchPrepared8Payload() []byte {
	return bytes.Repeat([]byte("0123456789abcdef"), benchPrepared8Size/16)
}

type benchPrepared8Transport struct {
	verify   bool
	verified int
}

func (tr *benchPrepared8Transport) RoundTrip(r *http.Request) (*http.Response, error) {
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
		wantHeader := http.Header{"Content-Type": {benchPrepared8Type}, "X-Benchmark": {"prepared-body"}}
		if r.Method != "POST" || r.URL.String() != benchPrepared8URL || !reflect.DeepEqual(r.Header, wantHeader) || r.ContentLength != benchPrepared8Size {
			return nil, fmt.Errorf("prepared exchange metadata: %s %s, headers %#v, ContentLength %d", r.Method, r.URL, r.Header, r.ContentLength)
		}
		// The oracle is fixed independently of either prepared request or
		// its GetBody implementation, including a separate backing slice.
		if !bytes.Equal(data, benchPrepared8Payload()) {
			return nil, fmt.Errorf("prepared exchange body differs from fixed %d-byte payload (received %d bytes)", benchPrepared8Size, len(data))
		}
		tr.verified++
	}
	return &http.Response{
		StatusCode: http.StatusNoContent, Status: "204 No Content",
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: make(http.Header), Body: http.NoBody, ContentLength: 0, Request: r,
	}, nil
}

// benchPrepared8Path constructs one actual measured path. Parse, Prepare,
// NewRequest, payload creation and the first body are all outside timing.
func benchPrepared8Path(tb testing.TB, path string) (*benchPrepared8Transport, func() (*http.Response, error)) {
	tb.Helper()
	ctx := context.Background()
	tr := &benchPrepared8Transport{verify: true}
	hc := &http.Client{Transport: tr}
	payload := benchPrepared8Payload()
	if path == "Client" {
		c, err := openapi.Parse(ctx, []byte(benchPrepared8Doc), "", &openapi.Options{
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
	req, err := http.NewRequestWithContext(ctx, "POST", benchPrepared8URL, bytes.NewReader(payload))
	if err != nil {
		tb.Fatal(err)
	}
	req.Header = http.Header{"Content-Type": {benchPrepared8Type}, "X-Benchmark": {"prepared-body"}}
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

func benchPrepared8Exchange(send func() (*http.Response, error)) error {
	r, err := send()
	if err != nil {
		if r != nil {
			r.Body.Close()
		}
		return err
	}
	return r.Body.Close()
}

func benchPrepared8Verify(tb testing.TB, tr *benchPrepared8Transport, send func() (*http.Response, error)) {
	tb.Helper()
	for range 2 {
		if err := benchPrepared8Exchange(send); err != nil {
			tb.Fatal(err)
		}
	}
	if tr.verified != 2 {
		tb.Fatalf("verified %d actual exchanges, want initial body and replay", tr.verified)
	}
	tr.verify = false
}

func TestPrepared8SendBodyBenchmarkParity(t *testing.T) {
	for _, path := range []string{"Client", "Hand"} {
		t.Run(path, func(t *testing.T) {
			tr, send := benchPrepared8Path(t, path)
			benchPrepared8Verify(t, tr, send)
		})
	}
}

func BenchmarkPrepared8SendBody(b *testing.B) {
	for _, path := range []string{"Client", "Hand"} {
		b.Run(path, func(b *testing.B) {
			b.StopTimer()
			tr, send := benchPrepared8Path(b, path)
			benchPrepared8Verify(b, tr, send)
			b.ReportAllocs()
			b.SetBytes(benchPrepared8Size)
			b.ResetTimer()
			b.StartTimer()
			for i := 0; i < b.N; i++ {
				if err := benchPrepared8Exchange(send); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
