package openapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

const review8UploadDoc = `"/x":{"post":{"operationId":"post","requestBody":{"content":{"application/octet-stream":{}}}}}`

// Request.HTTP and Request.Call, saved public API lines 1668-1710; Stage 8
// regression-brief.md class 1. Current HTTP.Body is the first body; current
// GetBody and ContentLength govern replays even if Body itself is unchanged.
func TestReview8CurrentPreparedReplay(t *testing.T) {
	for _, mode := range []string{"read-once", "reusable", "replace Body control"} {
		t.Run(mode, func(t *testing.T) {
			const first, replay = "first", "replayed-body"
			var mu sync.Mutex
			var bodies []string
			c := parseAt(t, doc31(review8UploadDoc), "https://api.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: stream7RT(func(r *http.Request) (*http.Response, error) {
				b, err := io.ReadAll(r.Body)
				r.Body.Close()
				if err != nil {
					return nil, err
				}
				if r.ContentLength != int64(len(b)) {
					t.Errorf("current ContentLength = %d for %d bytes", r.ContentLength, len(b))
				}
				mu.Lock()
				bodies = append(bodies, string(b))
				mu.Unlock()
				return stream7HTTP(r, 204, "", http.NoBody), nil
			})}})
			var input any = newOnce(first)
			if mode == "reusable" {
				input = []byte(first)
			}
			req := mustPrepare(t, c, "post", &openapi.Input{Body: input})
			if mode == "replace Body control" {
				req.HTTP.Body = io.NopCloser(strings.NewReader(first))
			}
			var copies atomic.Int32
			req.HTTP.GetBody = func() (io.ReadCloser, error) {
				copies.Add(1)
				return io.NopCloser(strings.NewReader(replay)), nil
			}
			req.HTTP.ContentLength = int64(len(first))
			send := func() error {
				r, err := req.Send(t.Context())
				if err != nil {
					return err
				}
				err = r.WaitRequest(t.Context())
				r.Body.Close()
				return err
			}
			if err := send(); err != nil {
				t.Fatal(err)
			}
			if copies.Load() != 0 || len(bodies) != 1 || bodies[0] != first {
				t.Fatalf("first send bodies %q, GetBody calls %d", bodies, copies.Load())
			}
			req.HTTP.ContentLength = int64(len(replay))
			if err := send(); err != nil {
				t.Fatal(err)
			}
			if copies.Load() != 1 || len(bodies) != 2 || bodies[1] != replay {
				t.Fatalf("replay bodies %q, GetBody calls %d", bodies, copies.Load())
			}
			// Establish the sequential replay contract before concurrent use,
			// so a broken baseline never races a read-once caller's reader.
			var wg sync.WaitGroup
			for range 3 {
				wg.Go(func() {
					if err := send(); err != nil {
						t.Errorf("concurrent replay: %v", err)
					}
				})
			}
			wg.Wait()
			if copies.Load() != 4 || len(bodies) != 5 {
				t.Fatalf("replays %q, GetBody calls %d", bodies, copies.Load())
			}
			for _, b := range bodies[1:] {
				if b != replay {
					t.Errorf("replay body %q", b)
				}
			}
		})
	}
}

// The same contract, plus RequestError.Err (saved API 1776-1784): a current
// GetBody error is a pre-dispatch RequestError preserving its exact cause;
// clearing GetBody turns an existing prepared body into a read-once body.
func TestReview8PreparedReplayRefusals(t *testing.T) {
	for _, mode := range []string{"GetBody error", "clear GetBody"} {
		t.Run(mode, func(t *testing.T) {
			var dispatches int
			c := parseAt(t, doc31(review8UploadDoc), "https://api.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: stream7RT(func(r *http.Request) (*http.Response, error) {
				dispatches++
				if mode == "clear GetBody" && r.GetBody != nil {
					t.Error("cleared GetBody was restored on the transport request")
				}
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				return stream7HTTP(r, 204, "", http.NoBody), nil
			})}})
			req := mustPrepare(t, c, "post", &openapi.Input{Body: []byte("first")})
			cause := errors.New("caller GetBody could not reopen")
			var copies int
			if mode == "GetBody error" {
				req.HTTP.GetBody = func() (io.ReadCloser, error) { copies++; return nil, cause }
			} else {
				req.HTTP.GetBody = nil
			}
			r, err := req.Send(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			r.Body.Close()
			if err := r.WaitRequest(t.Context()); err != nil {
				t.Fatal(err)
			}
			r, err = req.Send(t.Context())
			if r != nil {
				r.Body.Close()
				t.Error("refused replay returned a response")
			}
			asRequestError(t, err)
			if dispatches != 1 {
				t.Errorf("replay refusal dispatched %d total requests", dispatches)
			}
			if mode == "GetBody error" && (!errors.Is(err, cause) || copies != 1) {
				t.Errorf("replay error %v, GetBody calls %d", err, copies)
			}
		})
	}
}

// Request.Call's "a send refused with a *RequestError does not count" also
// holds after adding GetBody to a prepared reader (class 1). Credential
// acquisition must fail without reading either the first body or a replay.
func TestReview8EditedReplayAfterRefusal(t *testing.T) {
	var credentials, copies, dispatches int
	var bodies []string
	cause := errors.New("credential source unavailable")
	doc := doc31(`"/x":{"post":{"operationId":"post","security":[{"key":[]}],"requestBody":{"content":{"application/octet-stream":{}}}}}`,
		`"components":{"securitySchemes":{"key":{"type":"apiKey","in":"header","name":"X-Key"}}}`)
	c := parseAt(t, doc, "https://api.example.test", testDocURI, &openapi.Options{
		Credentials: map[string]openapi.Credential{"key": openapi.SecretFunc(func(context.Context) (string, error) {
			credentials++
			if credentials == 1 {
				return "", cause
			}
			return "synthetic-key", nil
		})},
		HTTPClient: &http.Client{Transport: stream7RT(func(r *http.Request) (*http.Response, error) {
			dispatches++
			b, err := io.ReadAll(r.Body)
			r.Body.Close()
			bodies = append(bodies, string(b))
			return stream7HTTP(r, 204, "", http.NoBody), err
		})},
	})
	source := &stream7Body{reader: strings.NewReader("first")}
	req := mustPrepare(t, c, "post", &openapi.Input{Body: source})
	req.HTTP.GetBody = func() (io.ReadCloser, error) { copies++; return io.NopCloser(strings.NewReader("again")), nil }
	r, err := req.Send(t.Context())
	asRequestError(t, err)
	if r != nil || !errors.Is(err, cause) || dispatches != 0 || copies != 0 || source.reads.Load() != 0 || source.closed.Load() != 0 {
		t.Fatalf("refusal consumed the first send: response %v, error %v, sends/copies %d/%d", r, err, dispatches, copies)
	}
	for range 2 {
		r, err = req.Send(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := r.WaitRequest(t.Context()); err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
	}
	if len(bodies) != 2 || bodies[0] != "first" || bodies[1] != "again" || copies != 1 || source.closed.Load() != 0 {
		t.Fatalf("after refusal: bodies %q, GetBody %d, caller closes %d", bodies, copies, source.closed.Load())
	}
}
