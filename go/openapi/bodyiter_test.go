package openapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Iterator bodies, tested deterministically: every wait is on a channel the
// server or the iterator closes (bodyServer.seen, gates), never on time alone;
// where a test must show that something has NOT happened yet, it waits the
// suite's grace period on top of the channel that proves the state, and the
// race detector checks the rest (the iterator writes caller state the test then
// writes, as gatedReader does in upload_inflight_read_test.go). Run with -race.
// client.go, Input.Body: "An iterator is written one item at a time as it
// yields, so a large body is never held ... It runs on a goroutine of its
// own, in step with the transport's reads, from the transport's first Read of
// the body, so a body closed unread never runs it; its yield returns false
// once the body is no longer wanted. Call waits for the iterator to return;
// Send and Stream may return at response headers while it is still running. An
// error from an iter.Seq2, an item that cannot be encoded, or the context
// ending before the iterator returns aborts the body and is reported by Call or
// Response.WaitRequest." Input: "closing Response.Body stops an outstanding
// upload." Stream: "An iterator that honors its yield result then stops".
//
// Note on net/http: once a response body has been read to its end, the
// HTTP/1 transport closes the connection unless the request body was
// written within 50 ms (net/http transport.go, wroteRequest,
// maxWriteWaitBeforeConnReuse). The tests that let an upload run on after a
// complete answer therefore read the response only after the upload ends,
// or assert only what holds either way.

// itemJSON and itemMark are JSON item i and the bytes that show it has
// arrived.
func itemJSON(i int) map[string]int { return map[string]int{"item": i} }
func itemMark(i int) string         { return fmt.Sprintf(`"item":%d}`, i) }

// seqClient parses seqDoc against srv.
func seqClient(t *testing.T, srv *bodyServer) *openapi.Client {
	t.Helper()
	return parseAt(t, seqDoc(), srv.URL, srv.URL+"/openapi.json", nil)
}

// endless yields items until yield returns false, then records that in
// stopped. More than 1<<22 items means yield never returned false.
func endless(ctx context.Context, stopped chan<- bool) iter.Seq[any] {
	return func(yield func(any) bool) {
		for i := range 1 << 22 {
			if ctx.Err() != nil {
				stopped <- false
				return
			}
			if !yield(itemJSON(i)) {
				stopped <- true
				return
			}
		}
		stopped <- false
	}
}

// awaitStopped waits for the iterator's report, failing unless its yield
// returned false.
func awaitStopped(t *testing.T, stopped <-chan bool, what string) {
	t.Helper()
	select {
	case ok := <-stopped:
		if !ok {
			t.Errorf("%s: the iterator's yield never returned false", what)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: the iterator did not stop within 10s", what)
	}
}

// Each item reaches the server before the next is yielded: the iterator
// waits, after each yield, until the server has received that item. A
// client that held items back (to fill a buffer, or the whole body) would
// never let the server see one, and the call would not end.
func TestIteratorWritesItemsAsTheyYield(t *testing.T) {
	sse := func(i int) openapi.Event { return openapi.Event{Data: []byte(fmt.Sprintf("item-%d", i))} }
	for _, tt := range []struct {
		key  string
		mark func(int) string
		body func(ctx context.Context, srv *bodyServer) any
		want func(t *testing.T, body []byte)
	}{
		{"jsonl", itemMark, func(ctx context.Context, srv *bodyServer) any {
			return iter.Seq[map[string]int](func(yield func(map[string]int) bool) {
				for i := 1; i <= 3; i++ {
					if !yield(itemJSON(i)) {
						return
					}
					select {
					case <-srv.seen(itemMark(i)):
					case <-ctx.Done():
						return
					}
				}
			})
		}, func(t *testing.T, body []byte) {
			wantLines(t, body, `{"item":1}`, `{"item":2}`, `{"item":3}`)
		}},
		{"seq", itemMark, func(ctx context.Context, srv *bodyServer) any {
			return iter.Seq2[any, error](func(yield func(any, error) bool) {
				for i := 1; i <= 3; i++ {
					if !yield(itemJSON(i), nil) {
						return
					}
					select {
					case <-srv.seen(itemMark(i)):
					case <-ctx.Done():
						return
					}
				}
			})
		}, func(t *testing.T, body []byte) {
			if want := jsonSeq(`{"item":1}`, `{"item":2}`, `{"item":3}`); string(body) != want {
				t.Errorf("body %q, want %q", body, want)
			}
		}},
		{"sse", func(i int) string { return fmt.Sprintf("item-%d", i) }, func(ctx context.Context, srv *bodyServer) any {
			return iter.Seq[openapi.Event](func(yield func(openapi.Event) bool) {
				for i := 1; i <= 3; i++ {
					if !yield(sse(i)) {
						return
					}
					select {
					case <-srv.seen(fmt.Sprintf("item-%d", i)):
					case <-ctx.Done():
						return
					}
				}
			})
		}, func(t *testing.T, body []byte) {
			if want := "data: item-1\n\ndata: item-2\n\ndata: item-3\n\n"; string(body) != want {
				t.Errorf("body %q, want %q", body, want)
			}
		}},
	} {
		t.Run(tt.key, func(t *testing.T) {
			srv := newBodyServer(t, false, "")
			ctx, _ := gateCtx(t)
			r := awaitCall(t, callAsync(ctx, seqClient(t, srv), tt.key, &openapi.Input{Body: tt.body(ctx, srv)}), "Call")
			if r.err != nil {
				t.Fatalf("Call: %v", r.err)
			}
			body, err := srv.finished(t)
			if err != nil {
				t.Errorf("the server's read ended with %v", err)
			}
			tt.want(t, body)
		})
	}
}

// Call returns only after the iterator returns: here the server has
// answered in full before the iterator's last step, which waits on a gate.
// Whether the upload then completes is net/http's affair (see the note
// above); that Call waits is the contract's.
func TestCallWaitsForTheIterator(t *testing.T) {
	srv := newBodyServer(t, true, "")
	c := seqClient(t, srv)
	ctx, _ := gateCtx(t)
	yielded, gate, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	state := 0
	it := func(yield func(any, error) bool) {
		defer close(returned)
		for i := 1; i <= 2; i++ {
			if !yield(itemJSON(i), nil) {
				return
			}
		}
		close(yielded)
		select {
		case <-gate:
		case <-ctx.Done():
			return
		}
		state++ // the caller's own state, written after the gate
	}
	type result struct {
		waited bool
		err    error
	}
	done := make(chan result, 1)
	go func() {
		_, err := c.Call(ctx, "jsonl", &openapi.Input{Body: iter.Seq2[any, error](it)}, nil)
		select {
		case <-returned:
			done <- result{true, err}
		default:
			done <- result{false, err}
		}
	}()
	for _, ev := range []struct {
		ch   <-chan struct{}
		what string
	}{{yielded, "the last yield"}, {srv.answered, "the answer"}, {srv.seen(itemMark(2)), "the server receiving item 2"}} {
		select {
		case <-ev.ch:
		case r := <-done:
			t.Fatalf("Call returned (%v) before %s", r.err, ev.what)
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not happen within 10s", ev.what)
		}
	}
	select {
	case r := <-done:
		t.Fatalf("Call returned (%v) while the iterator was still running", r.err)
	case <-time.After(grace):
	}
	close(gate)
	var r result
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Call did not return within 10s of the iterator returning")
	}
	if !r.waited {
		t.Errorf("Call returned before the iterator did")
	}
	state = 0 // the caller reuses its state: a race if Call returned first
}

// Send returns at the response headers while the iterator is still
// running, and WaitRequest reports nil once it has returned and every item
// was consumed (client.go, Response.WaitRequest: "nil when its body was
// consumed completely").
func TestSendReturnsWhileTheIteratorRuns(t *testing.T) {
	srv := newBodyServer(t, true, "")
	c := seqClient(t, srv)
	ctx, _ := gateCtx(t)
	yielded, gate, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	it := func(yield func(map[string]int) bool) {
		defer close(returned)
		if !yield(itemJSON(1)) {
			return
		}
		close(yielded)
		select {
		case <-gate:
		case <-ctx.Done():
			return
		}
		yield(itemJSON(2))
	}
	req := mustPrepare(t, c, "jsonl", &openapi.Input{Body: iter.Seq[map[string]int](it)})
	sent := make(chan callResult, 1)
	go func() {
		resp, err := req.Send(ctx)
		sent <- callResult{resp, err}
	}()
	r := awaitCall(t, sent, "Send, with the iterator waiting on its gate,")
	if r.err != nil {
		t.Fatalf("Send: %v", r.err)
	}
	defer r.resp.Body.Close()
	awaitOr(t, yielded, nil, "the first yield")
	select {
	case <-returned:
		t.Fatalf("the iterator returned before its gate opened")
	default:
	}
	close(gate)
	wctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := r.resp.WaitRequest(wctx); err != nil {
		t.Errorf("WaitRequest = %v, want nil", err)
	}
	select {
	case <-returned:
	default:
		t.Errorf("WaitRequest returned before the iterator did")
	}
	body, err := srv.finished(t)
	if err != nil {
		t.Errorf("the server's read ended with %v", err)
	}
	wantLines(t, body, `{"item":1}`, `{"item":2}`)
	if b, _ := io.ReadAll(r.resp.Body); string(b) != "{}" {
		t.Errorf("response body %q", b)
	}
}

// pickyCodec writes J(v), and fails on the int 13.
type pickyCodec struct{ err error }

func (c pickyCodec) Encode(w io.Writer, v any) error {
	if n, ok := v.(int); ok && n == 13 {
		return c.err
	}
	_, err := fmt.Fprintf(w, "J(%v)", v)
	return err
}

func (pickyCodec) Decode(io.Reader, any) error { return errors.New("not used") }

// errCursor is an iterator's own error.
var errCursor = errors.New("the cursor failed")

// An error from an iter.Seq2 aborts the body: the server never receives a
// complete one (its read ends in an error), and Call's error, or
// WaitRequest's after Send, matches it with errors.Is (doc.go, Outcomes:
// "An upload error may be joined with a response error; errors.As can find
// both").
func TestIteratorErrorAbortsTheBody(t *testing.T) {
	t.Run("Call", func(t *testing.T) {
		srv := newBodyServer(t, false, "")
		ctx, _ := gateCtx(t)
		it := func(yield func(any, error) bool) {
			if !yield(itemJSON(1), nil) {
				return
			}
			select {
			case <-srv.seen(itemMark(1)):
			case <-ctx.Done():
				return
			}
			yield(nil, errCursor)
		}
		r := awaitCall(t, callAsync(ctx, seqClient(t, srv), "jsonl", &openapi.Input{Body: iter.Seq2[any, error](it)}), "Call")
		if !errors.Is(r.err, errCursor) || isRequestError(r.err) {
			t.Fatalf("Call = %v, want the iterator's error, not a *RequestError", r.err)
		}
		if _, err := srv.finished(t); err == nil {
			t.Errorf("the server read a complete body")
		}
	})
	t.Run("WaitRequest", func(t *testing.T) {
		srv := newBodyServer(t, true, "")
		ctx, _ := gateCtx(t)
		gate := make(chan struct{})
		it := func(yield func(Pet, error) bool) {
			if !yield(Pet{Name: "Rex"}, nil) {
				return
			}
			select {
			case <-gate:
			case <-ctx.Done():
				return
			}
			yield(Pet{}, errCursor)
		}
		req := mustPrepare(t, seqClient(t, srv), "jsonl", &openapi.Input{Body: iter.Seq2[Pet, error](it)})
		resp, err := req.Send(ctx)
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		defer resp.Body.Close()
		close(gate)
		wctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if err := resp.WaitRequest(wctx); !errors.Is(err, errCursor) {
			t.Errorf("WaitRequest = %v, want the iterator's error", err)
		}
		if _, err := srv.finished(t); err == nil {
			t.Errorf("the server read a complete body")
		}
	})
}

// An item that cannot be encoded aborts the body as an iterator's error does:
// a JSON item encoding/json refuses (its error reachable with errors.As:
// encoding errors get safe text with the cause kept for errors.Is/As), an
// event stream item client.go, Input.Body, rules out, and an item whose
// caller codec fails (client.go, Options.Codecs: "An Encode error ... aborts
// the body for an iterator's item").
func TestUnencodableItemAbortsTheBody(t *testing.T) {
	errEncode := errors.New("cannot encode")
	for _, tt := range []struct {
		name, key string
		codecs    map[string]openapi.Codec
		bad       any
		check     func(error) bool
	}{
		{"JSON infinity", "jsonl", nil, math.Inf(1), func(err error) bool {
			var uv *json.UnsupportedValueError
			return errors.As(err, &uv)
		}},
		{"event with a line break", "sse", nil, openapi.Event{Event: "a\nb"}, func(err error) bool { return err != nil }},
		{"event member", "sse", nil, map[string]any{"comment": "x"}, func(err error) bool { return err != nil }},
		{"codec error", "seq", map[string]openapi.Codec{"application/json": pickyCodec{errEncode}}, 13, func(err error) bool {
			return errors.Is(err, errEncode)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newBodyServer(t, false, "")
			c := parseAt(t, seqDoc(), srv.URL, srv.URL+"/openapi.json", &openapi.Options{Codecs: tt.codecs})
			ctx, _ := gateCtx(t)
			first := any(openapi.Event{Data: []byte("first")})
			mark := "first"
			if tt.key != "sse" {
				first, mark = "first", `"first"`
			}
			if tt.codecs != nil {
				first, mark = "x", "J(x)"
			}
			it := func(yield func(any) bool) {
				if !yield(first) {
					return
				}
				select {
				case <-srv.seen(mark):
				case <-ctx.Done():
					return
				}
				yield(tt.bad)
			}
			r := awaitCall(t, callAsync(ctx, c, tt.key, &openapi.Input{Body: iter.Seq[any](it)}), "Call")
			if !tt.check(r.err) || isRequestError(r.err) {
				t.Fatalf("Call = %v (%T), want the encoding failure, not a *RequestError", r.err, r.err)
			}
			if _, err := srv.finished(t); err == nil {
				t.Errorf("the server read a complete body")
			}
		})
	}
}

// The context ending before the iterator returns aborts the body: the
// iterator's yield then returns false, Call returns after the iterator,
// with an error matching ctx.Err() (doc.go, Outcomes: "When the call's
// context is done before the call completes, the error matches both ctx.Err()
// and context.Cause(ctx) with errors.Is"), and after Send, WaitRequest reports
// it.
func TestIteratorContextEnds(t *testing.T) {
	t.Run("Call", func(t *testing.T) {
		srv := newBodyServer(t, false, "")
		c := seqClient(t, srv)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		resume, returned := make(chan struct{}), make(chan struct{})
		stopped := make(chan bool, 1)
		it := func(yield func(any) bool) {
			defer close(returned)
			if !yield(itemJSON(0)) {
				stopped <- true
				return
			}
			<-resume
			endless(context.Background(), stopped)(yield)
		}
		type result struct {
			waited bool
			err    error
		}
		done := make(chan result, 1)
		go func() {
			_, err := c.Call(ctx, "jsonl", &openapi.Input{Body: iter.Seq[any](it)}, nil)
			select {
			case <-returned:
				done <- result{true, err}
			default:
				done <- result{false, err}
			}
		}()
		select {
		case <-srv.seen(itemMark(0)):
		case r := <-done:
			close(resume)
			t.Fatalf("Call returned (%v) before the server received an item", r.err)
		case <-time.After(10 * time.Second):
			close(resume)
			t.Fatal("the server received no item within 10s")
		}
		cancel()
		close(resume)
		awaitStopped(t, stopped, "after cancellation")
		select {
		case r := <-done:
			if !r.waited {
				t.Errorf("Call returned before the iterator did")
			}
			if !errors.Is(r.err, context.Canceled) {
				t.Errorf("Call = %v, want an error matching context.Canceled", r.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Call did not return within 10s")
		}
	})
	t.Run("WaitRequest", func(t *testing.T) {
		srv := newBodyServer(t, true, "")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		resume := make(chan struct{})
		stopped := make(chan bool, 1)
		it := func(yield func(any) bool) {
			if !yield(itemJSON(0)) {
				stopped <- true
				return
			}
			<-resume
			endless(context.Background(), stopped)(yield)
		}
		req := mustPrepare(t, seqClient(t, srv), "jsonl", &openapi.Input{Body: iter.Seq[any](it)})
		resp, err := req.Send(ctx)
		if err != nil {
			close(resume)
			t.Fatalf("Send: %v", err)
		}
		defer resp.Body.Close()
		// Send promises response headers, not iterator entry. Establish an
		// active source; a body stopped before entry may never run.
		select {
		case <-srv.seen(itemMark(0)):
		case <-time.After(10 * time.Second):
			close(resume)
			t.Fatal("the server received no item within 10s")
		}
		cancel()
		close(resume)
		awaitStopped(t, stopped, "after cancellation")
		wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer wcancel()
		if err := resp.WaitRequest(wctx); !errors.Is(err, context.Canceled) {
			t.Errorf("WaitRequest = %v, want an error matching context.Canceled", err)
		}
	})
}

// When the body is no longer wanted, yield returns false: after Send, when
// the caller closes Response.Body (client.go, Input: "closing Response.Body
// stops an outstanding upload"), and under Call, when the server answers and
// stops reading. WaitRequest, and Call, report the incomplete upload
// (client.go, Call: "A successful response does not hide an incomplete
// upload").
func TestIteratorStopsWhenTheBodyIsUnwanted(t *testing.T) {
	t.Run("Response.Body closed", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NewResponseController(w).EnableFullDuplex()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select { // never reads the request body
			case <-r.Context().Done():
			case <-release:
			}
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) }) // runs first
		c := parseAt(t, seqDoc(), srv.URL, srv.URL+"/openapi.json", nil)
		ctx, _ := gateCtx(t)
		stopped := make(chan bool, 1)
		req := mustPrepare(t, c, "jsonl", &openapi.Input{Body: endless(ctx, stopped)})
		resp, err := req.Send(ctx)
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		resp.Body.Close()
		awaitStopped(t, stopped, "after Response.Body.Close")
		wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := resp.WaitRequest(wctx); err == nil {
			t.Errorf("WaitRequest = nil for an upload stopped by closing Response.Body")
		}
	})
	t.Run("server stops reading", func(t *testing.T) {
		srv := newBodyServer(t, false, itemMark(2))
		c := seqClient(t, srv)
		ctx, _ := gateCtx(t)
		stopped := make(chan bool, 1)
		r := awaitCall(t, callAsync(ctx, c, "jsonl", &openapi.Input{Body: endless(ctx, stopped)}), "Call")
		if isRequestError(r.err) {
			t.Fatalf("Call refused: %v", r.err)
		}
		awaitStopped(t, stopped, "after the server stopped reading")
		if r.err == nil || isRequestError(r.err) {
			t.Errorf("Call = %v, want the incomplete upload reported", r.err)
		}
		if r.resp == nil || r.resp.StatusCode != 200 {
			t.Errorf("Call returned no 200 Response: %+v", r.resp)
		}
	})
}

// An iterator body is read once: a redirect that must send it again is not
// followed (Redirects: "a hop that must resend a body that cannot be sent
// again (see Input.Body) is not followed"; "A 3xx not followed is the
// outcome, a *StatusError"), and a prepared Request with one is sent once
// (TestSequentialBodiesPrepared).
func TestIteratorBodyNotSentAgain(t *testing.T) {
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/jsonl") {
			rw.Header().Set("Location", "/final")
			rw.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, "{}")
	})
	c := parseFor(t, w, seqDoc(), &openapi.Options{Redirects: openapi.FollowAll})
	_, err := c.Call(t.Context(), "jsonl", &openapi.Input{Body: seqOf(1, 2)}, nil)
	var se *openapi.StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("Call = %v, want a *StatusError for the 307", err)
	}
	if n := w.count(); n != 1 {
		t.Errorf("server received %d requests, want 1", n)
	}
}
