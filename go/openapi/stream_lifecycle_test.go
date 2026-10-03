package openapi_test

import (
	"context"
	"errors"
	"io"
	"iter"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

func stream7Await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("channel handshake timed out")
		var zero T
		return zero
	}
}

type stream7ReadFunc func([]byte) (int, error)

func (f stream7ReadFunc) Read(p []byte) (int, error) { return f(p) }

// Stream returns at successful headers without attempting a body read.
// The first framed item must be delivered before a later read can finish.
// No timer or sleep is used to release the producer; only receiving the
// item permits the remainder of the response to become available.
func TestStream7IncrementalHeadersAndItems(t *testing.T) {
	for _, tc := range []struct{ ct, first string }{{"application/jsonl", "1\n"}, {"application/json-seq", "\x1e1\n\x1e"}, {"text/event-stream", "data: first\n\n"}} {
		t.Run(tc.ct, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			open := func() { once.Do(func() { close(release) }) }
			defer open()
			prefix := tc.first
			body := &stream7Body{reader: stream7ReadFunc(func(p []byte) (int, error) {
				if prefix != "" {
					n := copy(p, prefix)
					prefix = prefix[n:]
					return n, nil
				}
				<-release
				return 0, io.EOF
			})}
			c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) { return stream7HTTP(r, 200, tc.ct, body), nil }), nil)
			type result struct {
				r   *openapi.Response
				err error
			}
			headers := make(chan result, 1)
			go func() { r, e := c.Stream(t.Context(), "get", nil); headers <- result{r, e} }()
			h := stream7Await(t, headers)
			if h.err != nil {
				t.Fatal(h.err)
			}
			defer h.r.Body.Close()
			if body.reads.Load() != 0 {
				t.Fatalf("Stream read Body before return (%d reads)", body.reads.Load())
			}
			first := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				for _, err := range openapi.Items[any](h.r) {
					first <- err
					break
				}
			}()
			if err := stream7Await(t, first); err != nil {
				t.Fatal(err)
			}
			open()
			stream7Await(t, finished)
			if body.closed.Load() == 0 {
				t.Fatal("break did not close response")
			}
		})
	}
}

// Stream applies Call's status policy and bounded StatusError buffering;
// Request.Stream shares the preparation and response description path.
func TestStream7StatusAndEditions(t *testing.T) {
	for _, version := range editionVersions {
		t.Run(version, func(t *testing.T) {
			for _, prepared := range []bool{false, true} {
				body := &stream7Body{reader: strings.NewReader("12345\n")}
				c := stream7Client(t, version, stream7RT(func(r *http.Request) (*http.Response, error) {
					return stream7HTTP(r, 200, "application/jsonl", body), nil
				}), nil)
				var r *openapi.Response
				var err error
				if prepared {
					r, err = mustPrepare(t, c, "get", nil).Stream(t.Context())
				} else {
					r, err = c.Stream(t.Context(), "get", nil)
				}
				if err != nil || r == nil || r.StatusCode != 200 || r.Declaration == nil {
					t.Fatalf("response %v error %v", r, err)
				}
				got, errs := stream7Collect[int](r)
				stream7NoErrors(t, errs)
				if len(got) != 1 || got[0] != 12345 {
					t.Fatal(got)
				}
			}
		})
	}
	for _, status := range []int{302, 400, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := &stream7Body{reader: strings.NewReader("0123456789")}
			c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) {
				h := stream7HTTP(r, status, "text/plain", body)
				h.Header.Set("Location", "/other")
				return h, nil
			}), func(o *openapi.Options) { o.MaxErrorBytes = 4; o.MaxBodyBytes = 1; o.MaxItemBytes = 1 })
			r, err := c.Stream(t.Context(), "get", nil)
			var se *openapi.StatusError
			var max *http.MaxBytesError
			if r == nil || !errors.As(err, &se) || r.StatusCode != status || string(se.Content) != "0123" || !errors.As(se.Err, &max) || max.Limit != 4 {
				t.Fatalf("response %v error %#v", r, err)
			}
			if body.closed.Load() == 0 {
				t.Fatal("error body not closed")
			}
			p, e := io.ReadAll(r.Body)
			if e != nil || string(p) != "0123" {
				t.Fatalf("replay %q %v", p, e)
			}
		})
	}
	t.Run("read error retains status", func(t *testing.T) {
		boom := errors.New("status body failed")
		c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) {
			return stream7HTTP(r, 503, "text/plain", io.NopCloser(&stream7Chunks{data: "partial", terminal: boom})), nil
		}), nil)
		r, err := c.Stream(t.Context(), "get", nil)
		var se *openapi.StatusError
		if r == nil || !errors.As(err, &se) || !errors.Is(err, boom) || string(se.Content) != "partial" {
			t.Fatalf("%v %v", r, err)
		}
	})
}

func stream7UploadClient(t testing.TB, rt http.RoundTripper) *openapi.Client {
	t.Helper()
	return parseAt(t, doc31(`"/x":{"post":{"operationId":"post","requestBody":{"content":{"application/jsonl":{}}},"responses":{"200":{"description":"ok"}}}}`), "https://stream.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
}

// stream.go Stream and client.go WaitRequest: response EOF and upload
// completion are independent; canceling one wait does not stop the upload.
// Late source, iterator, and encoding errors remain observable, repeatedly
// and concurrently, after the stream's response has already ended.
func TestStream7LateUploadErrors(t *testing.T) {
	boom := errors.New("late source failure")
	for _, kind := range []string{"reader", "iterator", "encoding"} {
		t.Run(kind, func(t *testing.T) {
			entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			open := func() { once.Do(func() { close(release) }) }
			defer open()
			var source any
			switch kind {
			case "reader":
				source = stream7ReadFunc(func([]byte) (int, error) { close(entered); <-release; return 0, boom })
			case "iterator":
				source = iter.Seq2[any, error](func(yield func(any, error) bool) { close(entered); <-release; yield(nil, boom) })
			case "encoding":
				source = iter.Seq[any](func(yield func(any) bool) { close(entered); <-release; yield(make(chan int)) })
			}
			rt := stream7RT(func(r *http.Request) (*http.Response, error) {
				go func() { defer close(stopped); io.Copy(io.Discard, r.Body); r.Body.Close() }()
				<-entered
				return stream7HTTP(r, 200, "application/jsonl", io.NopCloser(strings.NewReader(""))), nil
			})
			c := stream7UploadClient(t, rt)
			r, err := stream7Headers(t, func() (*openapi.Response, error) { return c.Stream(t.Context(), "post", &openapi.Input{Body: source}) })
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			if p, err := io.ReadAll(r.Body); len(p) != 0 || err != nil {
				t.Fatalf("response %q %v", p, err)
			}
			waitCause := errors.New("this wait ended")
			waitCtx, cancel := context.WithCancelCause(t.Context())
			cancel(waitCause)
			err = r.WaitRequest(waitCtx)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, waitCause) {
				t.Fatalf("wait cancellation %v", err)
			}
			open()
			stream7Await(t, stopped)
			var wg sync.WaitGroup
			for range 6 {
				wg.Go(func() {
					e := r.WaitRequest(t.Context())
					if e == nil || (kind != "encoding" && !errors.Is(e, boom)) {
						t.Errorf("late error %v", e)
					}
				})
			}
			wg.Wait()
		})
	}
}

// Closing the response and canceling the original context each stop an
// outstanding cooperative iterator. The transport waits between reads;
// the iterator's yield is the only gate, so its return proves ownership
// was settled. Cancellation preserves both Err and Cause (doc.go Outcomes).
func TestStream7StopCooperativeUpload(t *testing.T) {
	for _, action := range []string{"close", "cancel"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			ready, stopped, transportDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			source := iter.Seq[any](func(yield func(any) bool) {
				defer close(stopped)
				for i := 0; yield(i); i++ {
				}
			})
			rt := stream7RT(func(r *http.Request) (*http.Response, error) {
				go func() {
					defer close(transportDone)
					defer r.Body.Close()
					var one [1]byte
					_, _ = r.Body.Read(one[:])
					close(ready)
					select {
					case <-r.Context().Done():
					case <-stopped:
					}
				}()
				<-ready
				return stream7HTTP(r, 200, "application/jsonl", io.NopCloser(strings.NewReader(""))), nil
			})
			c := stream7UploadClient(t, rt)
			r, err := stream7Headers(t, func() (*openapi.Response, error) { return c.Stream(ctx, "post", &openapi.Input{Body: source}) })
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("stop stream")
			if action == "close" {
				r.Body.Close()
			} else {
				cancel(cause)
			}
			stream7Await(t, transportDone)
			stream7Await(t, stopped)
			err = r.WaitRequest(t.Context())
			if err == nil {
				t.Fatal("incomplete upload reported complete")
			}
			if action == "cancel" && (!errors.Is(err, context.Canceled) || !errors.Is(err, cause)) {
				t.Fatalf("cancel error %v", err)
			}
			r.Body.Close()
		})
	}
}

// Context errors before headers return no Response, while after headers
// they are yielded last, preserving completed items and context.Cause.
func TestStream7ContextBeforeAndAfterHeaders(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := errors.New("stream budget")
			entered := make(chan struct{})
			rt := stream7RT(func(r *http.Request) (*http.Response, error) {
				close(entered)
				if !after {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				first := true
				body := io.NopCloser(stream7ReadFunc(func(p []byte) (int, error) {
					if first {
						first = false
						return copy(p, "1\n"), nil
					}
					<-r.Context().Done()
					return 0, r.Context().Err()
				}))
				return stream7HTTP(r, 200, "application/jsonl", body), nil
			})
			c := stream7Client(t, "3.1.2", rt, nil)
			if !after {
				type result struct {
					r *openapi.Response
					e error
				}
				done := make(chan result, 1)
				go func() { r, e := c.Stream(ctx, "get", nil); done <- result{r, e} }()
				stream7Await(t, entered)
				cancel(cause)
				got := stream7Await(t, done)
				if got.r != nil || !errors.Is(got.e, context.Canceled) || !errors.Is(got.e, cause) {
					t.Fatalf("%v %v", got.r, got.e)
				}
				return
			}
			r, err := stream7Headers(t, func() (*openapi.Response, error) { return c.Stream(ctx, "get", nil) })
			if err != nil {
				t.Fatal(err)
			}
			i := 0
			for v, e := range openapi.Items[int](r) {
				if i == 0 {
					if v != 1 || e != nil {
						t.Fatalf("first %d %v", v, e)
					}
					cancel(cause)
				} else if !errors.Is(e, context.Canceled) || !errors.Is(e, cause) || errors.Is(e, openapi.ErrItem) {
					t.Fatalf("terminal %v", e)
				}
				i++
			}
			if i != 2 {
				t.Fatalf("%d items", i)
			}
		})
	}
}

// Call and Decode must drain a finite duplex response before waiting for
// the upload, then wait for its source before returning (client.go).
func TestStream7FiniteDuplexDecode(t *testing.T) {
	for _, path := range []string{"Call", "Decode"} {
		t.Run(path, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			open := func() { once.Do(func() { close(release) }) }
			defer open()
			finished := make(chan struct{})
			var sourceDone atomic.Bool
			source := iter.Seq[any](func(yield func(any) bool) { defer sourceDone.Store(true); <-release; yield(1) })
			rt := stream7RT(func(r *http.Request) (*http.Response, error) {
				go func() { defer close(finished); io.Copy(io.Discard, r.Body); r.Body.Close() }()
				prefix := "1\n2\n"
				body := &stream7Body{reader: stream7ReadFunc(func(p []byte) (int, error) {
					if prefix != "" {
						n := copy(p, prefix)
						prefix = prefix[n:]
						return n, nil
					}
					open()
					return 0, io.EOF
				})}
				return stream7HTTP(r, 200, "application/jsonl", body), nil
			})
			c := stream7UploadClient(t, rt)
			done := make(chan error, 1)
			var out []int
			go func() {
				if path == "Call" {
					_, err := c.Call(t.Context(), "post", &openapi.Input{Body: source}, &out)
					done <- err
				} else {
					r, err := mustPrepare(t, c, "post", &openapi.Input{Body: source}).Send(t.Context())
					if err == nil {
						err = r.Decode(&out)
					}
					done <- err
				}
			}()
			if err := stream7Await(t, done); err != nil {
				t.Fatal(err)
			}
			if !sourceDone.Load() {
				t.Fatal("decode returned while source active")
			}
			stream7Await(t, finished)
			if len(out) != 2 || out[0] != 1 || out[1] != 2 {
				t.Fatal(out)
			}
		})
	}
}

// Stream uses the same redirect/signing and prepared-request replay rules
// as Call. Every send refreshes credentials; a replayable prepared request
// remains concurrently usable after its first stream closes.
func TestStream7PreparedRedirectsAndCredentials(t *testing.T) {
	var secrets atomic.Int32
	var calls atomic.Int32
	rt := stream7RT(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "" {
			t.Error("missing bearer credential")
		}
		if r.URL.Path == "/x" {
			res := stream7HTTP(r, 307, "", io.NopCloser(strings.NewReader("")))
			res.Header.Set("Location", "/final")
			return res, nil
		}
		return stream7HTTP(r, 200, "application/jsonl", io.NopCloser(strings.NewReader("1\n"))), nil
	})
	doc := doc31(`"/x":{"get":{"operationId":"get","security":[{"token":[]}],"responses":{"200":{"description":"ok"}}}}`, `"components":{"securitySchemes":{"token":{"type":"http","scheme":"bearer"}}}`)
	c := parseAt(t, doc, "https://stream.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}, Redirects: openapi.FollowAll, Credentials: map[string]openapi.Credential{"token": openapi.SecretFunc(func(context.Context) (string, error) { secrets.Add(1); return "secret", nil })}})
	req := mustPrepare(t, c, "get", nil)
	if secrets.Load() != 0 {
		t.Fatal("Prepare consulted credentials")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			r, err := req.Stream(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			if r.Request.Header.Get("Authorization") != "" {
				t.Error("response request exposed secret")
			}
			for n, e := range openapi.Items[int](r) {
				if n != 1 || e != nil {
					t.Errorf("%d %v", n, e)
				}
			}
		})
	}
	wg.Wait()
	if calls.Load() != 8 || secrets.Load() != 8 {
		t.Fatalf("calls %d secrets %d", calls.Load(), secrets.Load())
	}
}

// Redirects and prepared replays wait for every consumed request body.
// Stream must use the existing replay policy, not claim a one-shot upload
// can be resent merely because its first response has arrived.
func TestStream7RedirectUploadAndReplay(t *testing.T) {
	var requests atomic.Int32
	rt := stream7RT(func(r *http.Request) (*http.Response, error) {
		p, e := io.ReadAll(r.Body)
		r.Body.Close()
		if e != nil || string(p) != "1\n2\n" {
			t.Errorf("upload %q %v", p, e)
		}
		requests.Add(1)
		if r.URL.Path == "/x" {
			resp := stream7HTTP(r, 307, "", io.NopCloser(strings.NewReader("")))
			resp.Header.Set("Location", "/final")
			return resp, nil
		}
		return stream7HTTP(r, 200, "application/jsonl", io.NopCloser(strings.NewReader("3\n"))), nil
	})
	c := stream7UploadClient(t, rt).With(func(o *openapi.Options) { o.Redirects = openapi.FollowAll })
	req := mustPrepare(t, c, "post", &openapi.Input{Body: []int{1, 2}})
	for range 2 {
		r, e := req.Stream(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		got, errs := stream7Collect[int](r)
		stream7NoErrors(t, errs)
		if len(got) != 1 || got[0] != 3 {
			t.Fatal(got)
		}
		if e = r.WaitRequest(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
	if requests.Load() != 4 {
		t.Fatalf("%d requests", requests.Load())
	}
	source := iter.Seq[any](func(yield func(any) bool) {
		if yield(1) {
			yield(2)
		}
	})
	r, e := c.Stream(t.Context(), "post", &openapi.Input{Body: source})
	var se *openapi.StatusError
	if r == nil || !errors.As(e, &se) || r.StatusCode != 307 {
		t.Fatalf("one-shot response %v error %v", r, e)
	}
	if requests.Load() != 5 {
		t.Fatalf("one-shot upload replayed (%d requests)", requests.Load())
	}
	if e = r.WaitRequest(t.Context()); e != nil {
		t.Fatal(e)
	}
}

// A non-2xx Stream returns its bounded StatusError while a caller-owned
// blocking source may still be in Read; WaitRequest remains the independent
// completion observation promised by Stream's status paragraph.
func TestStream7StatusRetainsOutstandingUpload(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	defer open()
	boom := errors.New("late read after status")
	source := stream7ReadFunc(func([]byte) (int, error) { close(entered); <-release; return 0, boom })
	rt := stream7RT(func(req *http.Request) (*http.Response, error) {
		go func() { defer close(done); io.Copy(io.Discard, req.Body); req.Body.Close() }()
		<-entered
		return stream7HTTP(req, 413, "text/plain", io.NopCloser(strings.NewReader("too large"))), nil
	})
	c := stream7UploadClient(t, rt)
	type result struct {
		r *openapi.Response
		e error
	}
	returned := make(chan result, 1)
	go func() { r, e := c.Stream(t.Context(), "post", &openapi.Input{Body: source}); returned <- result{r, e} }()
	got := stream7Await(t, returned)
	var se *openapi.StatusError
	if got.r == nil || !errors.As(got.e, &se) || string(se.Content) != "too large" {
		t.Fatalf("%v %v", got.r, got.e)
	}
	open()
	stream7Await(t, done)
	if e := got.r.WaitRequest(t.Context()); e == nil {
		t.Fatal("incomplete upload lost after status")
	}
}

// stream7Headers uses a timeout only as a failure guard; producer progress
// remains controlled by the enclosing test's explicit channel handshakes.
func stream7Headers(t *testing.T, call func() (*openapi.Response, error)) (*openapi.Response, error) {
	t.Helper()
	type result struct {
		r *openapi.Response
		e error
	}
	done := make(chan result, 1)
	go func() { r, e := call(); done <- result{r, e} }()
	got := stream7Await(t, done)
	return got.r, got.e
}
