package openapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the stage 3 review round, uploads: class ruling C3-2
// (stage 3 ledger, "Review round (8101e19)"; panel F2, part (a), and the
// adversarial contract note on WaitRequest). Every test gates the caller's
// reader on channels: a Read that blocks until the test releases it is in
// flight for as long as the test says, so a call that returns while it is in
// flight is caught whatever the machine's speed. Run with -race: the reader
// writes caller-owned state after it is released, and the test writes the
// same state once the call or wait has returned, which the race detector
// reports unless the return happens after the Read.

// gatedReader is a caller's body reader. Its first Read signals entered and
// blocks until release is closed; it then updates state, the caller's own
// unsynchronized data, and returns first. Every later Read returns io.EOF.
type gatedReader struct {
	entered chan struct{}
	release chan struct{}
	first   int // bytes the first Read returns: 0 for io.EOF
	reads   atomic.Int32
	state   int
	once    sync.Once
}

func newGatedReader(first int) *gatedReader {
	return &gatedReader{entered: make(chan struct{}), release: make(chan struct{}), first: first}
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if g.reads.Add(1) > 1 {
		return 0, io.EOF
	}
	close(g.entered)
	<-g.release
	g.state++
	if g.first == 0 {
		return 0, io.EOF
	}
	return copy(p, strings.Repeat("x", g.first)), nil
}

// open lets the first Read return; it is safe to call more than once.
func (g *gatedReader) open() { g.once.Do(func() { close(g.release) }) }

// await waits for ch, failing the test after 5 seconds.
func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen within 5s", what)
	}
}

// grace is how long a test lets a call that should be waiting run on, so
// that one that would return early does: a correct call cannot return at all
// before the test releases the Read, so a slow machine cannot fail it.
const grace = 150 * time.Millisecond

// wantStillWaiting fails if done is closed: the call or wait it marks
// returned while the caller's Read was in flight.
func wantStillWaiting(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	wantNotReturned(t, done, what+" returned while the transport was inside a Read of the caller's body")
}

// wantNotReturned fails with msg if done is closed after the grace period.
func wantNotReturned(t *testing.T, done <-chan struct{}, msg string) {
	t.Helper()
	time.Sleep(grace)
	select {
	case <-done:
		t.Error(msg)
	default:
	}
}

// inflightDoc has one POST operation taking any bytes.
var inflightDoc = doc31(`"/up":{"post":{"operationId":"up","requestBody":{"content":{"application/octet-stream":{}}}}}`)

// earlyAnswerServer answers POST /up with a 303 to /done before reading any
// of the request body, its handler running full duplex (net/http's
// ResponseController.EnableFullDuplex), and /done with 200. answered is
// closed once the 303 has been written, done once /done has been.
func earlyAnswerServer(t *testing.T) (srv *httptest.Server, answered, done chan struct{}) {
	answered, done = make(chan struct{}), make(chan struct{})
	var once1, once2 sync.Once
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/up":
			http.NewResponseController(w).EnableFullDuplex()
			w.Header().Set("Location", "/done")
			w.WriteHeader(http.StatusSeeOther)
			w.(http.Flusher).Flush()
			once1.Do(func() { close(answered) })
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, "{}")
			w.(http.Flusher).Flush()
			once2.Do(func() { close(done) })
		}
	}))
	t.Cleanup(srv.Close)
	return srv, answered, done
}

// C3-2: "A body generation ends only when the transport has closed it AND
// no Read is in flight; a Close during a Read defers the end until that Read
// returns ... (F2a: net/http closes a request body mid-Read after 301-303
// ...). WaitRequest and Call wait for every generation the call handed the
// transport, earlier hops included." client.go, Input: "Call has stopped
// reading its body when it returns, provided a reader body returns from Read
// when the call's context ends or its connection closes ... For Send or
// Stream, wait for Response.WaitRequest before reusing a body reader";
// Response.WaitRequest: it waits "for every request of the call that carried
// one: the first and each redirect hop that sent it again". C3-1: net/http's
// Client.do no longer acts on the 303 under FollowNone either. The server
// answers 303 before reading the POST body, as the adversarial reviewer's
// TestZA3SeeOtherLeavesUploadRunning did.
func TestReadInFlightAfterAnEarly303(t *testing.T) {
	for _, follow := range []openapi.Redirects{openapi.FollowNone, openapi.FollowAll} {
		for _, via := range []string{"Call", "Send"} {
			t.Run(fmt.Sprintf("Redirects %d, %s", follow, via), func(t *testing.T) {
				srv, answered, done := earlyAnswerServer(t)
				c := parseAt(t, inflightDoc, srv.URL, srv.URL+"/openapi.json", &openapi.Options{Redirects: follow})
				g := newGatedReader(0)
				t.Cleanup(g.open)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()

				// finished is when the call has otherwise completed: the
				// 303 under FollowNone, the hop's answer under FollowAll.
				finished := answered
				if follow == openapi.FollowAll {
					finished = done
				}
				returned := make(chan struct{})
				switch via {
				case "Call":
					go func() {
						defer close(returned)
						c.Call(ctx, "up", &openapi.Input{Body: io.Reader(g)}, nil)
					}()
					await(t, g.entered, "the first Read")
					await(t, finished, "the answer")
					wantStillWaiting(t, returned, "Call")
				case "Send":
					req := mustPrepare(t, c, "up", &openapi.Input{Body: io.Reader(g)})
					resp, err := req.Send(ctx)
					if err != nil {
						t.Fatalf("Send: %v", err)
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					await(t, g.entered, "the first Read")
					await(t, finished, "the answer")
					go func() {
						defer close(returned)
						resp.WaitRequest(ctx)
					}()
					wantStillWaiting(t, returned, "WaitRequest")
				}
				g.open()
				await(t, returned, via+" after the Read returned")
				g.state = 0 // the caller reuses its reader's state
				if n := g.reads.Load(); n != 1 {
					t.Errorf("the caller's reader was read %d times, want 1", n)
				}
			})
		}
	}
}

// C3-2: "a Close during a Read defers the end until that Read returns, and
// later Reads fail without touching the caller's reader (... RoundTrip may
// close from another goroutine)." net/http, RoundTripper: "RoundTrip must
// always close the body, including on errors, but depending on the
// implementation may do so in a separate goroutine even after RoundTrip
// returns." The transport here reads the body on one goroutine, as
// net/http's writeLoop does, and closes it from another while that Read is
// in flight.
func TestCloseDuringReadDefersTheEnd(t *testing.T) {
	for _, via := range []string{"Call", "Send"} {
		t.Run(via, func(t *testing.T) {
			g := newGatedReader(1)
			t.Cleanup(g.open)
			closing, readerDone := make(chan struct{}), make(chan struct{})
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body := r.Body
				go func() {
					defer close(readerDone)
					buf := make([]byte, 8)
					for {
						if _, err := body.Read(buf); err != nil {
							return
						}
					}
				}()
				go func() {
					<-g.entered
					close(closing)
					body.Close()
				}()
				return memResponse(r, 200, nil, "{}"), nil
			})
			c := parseAt(t, inflightDoc, "https://api.example.test", testDocURI,
				&openapi.Options{HTTPClient: &http.Client{Transport: rt}})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			returned := make(chan struct{})
			switch via {
			case "Call":
				go func() {
					defer close(returned)
					c.Call(ctx, "up", &openapi.Input{Body: io.Reader(g)}, nil)
				}()
				await(t, closing, "the transport's Close")
				wantStillWaiting(t, returned, "Call")
			case "Send":
				resp, err := mustPrepare(t, c, "up", &openapi.Input{Body: io.Reader(g)}).Send(ctx)
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				await(t, closing, "the transport's Close")
				go func() {
					defer close(returned)
					resp.WaitRequest(ctx)
				}()
				wantStillWaiting(t, returned, "WaitRequest")
			}
			g.open()
			await(t, returned, via+" after the Read returned")
			g.state = 0
			await(t, readerDone, "the transport's reader")
			if n := g.reads.Load(); n != 1 {
				t.Errorf("the caller's reader was read %d times; a Read after the transport closed the body reached it", n)
			}
		})
	}
}

// C3-2: "WaitRequest and Call wait for every generation the call handed the
// transport, earlier hops included". client.go, Response.WaitRequest:
// "for every request of the call that carried one: the first and each
// redirect hop that sent it again." A 307 answered before the first body
// was read leaves that body's Read in flight while the hop, from GetBody,
// is sent and answered.
func TestWaitRequestCoversEarlierHops(t *testing.T) {
	answered, done := make(chan struct{}), make(chan struct{})
	var once1, once2 sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/up":
			http.NewResponseController(w).EnableFullDuplex()
			w.Header().Set("Location", "/next")
			w.WriteHeader(http.StatusTemporaryRedirect)
			w.(http.Flusher).Flush()
			once1.Do(func() { close(answered) })
		default:
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, "{}")
			w.(http.Flusher).Flush()
			once2.Do(func() { close(done) })
		}
	}))
	t.Cleanup(srv.Close)
	c := parseAt(t, inflightDoc, srv.URL, srv.URL+"/openapi.json", &openapi.Options{Redirects: openapi.FollowAll})
	g := newGatedReader(0)
	t.Cleanup(g.open)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req := mustPrepare(t, c, "up", nil)
	req.HTTP.Body = io.NopCloser(g)
	req.HTTP.ContentLength = -1
	req.HTTP.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("hop body")), nil }
	resp, err := req.Send(ctx)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want the hop's 200", resp.StatusCode)
	}
	await(t, g.entered, "the first Read")
	await(t, answered, "the 307")
	await(t, done, "the hop's answer")
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		resp.WaitRequest(ctx)
	}()
	wantStillWaiting(t, returned, "WaitRequest")
	g.open()
	await(t, returned, "WaitRequest after the Read returned")
	g.state = 0
}

// cutShort reads n bytes of r's body, or all of it when n is negative, and
// closes it, as a transport does when the server answers early.
func cutShort(r *http.Request, n int) {
	if r.Body == nil {
		return
	}
	if n < 0 {
		io.Copy(io.Discard, r.Body)
	} else {
		io.ReadFull(r.Body, make([]byte, n))
	}
	r.Body.Close()
}

// TQ3 (stage 3 ledger): "WaitRequest reports on the last request that
// carried the body (a 307 resend that succeeds is nil even if the first
// upload was cut short); it still waits for all of them." client.go,
// Response.WaitRequest: "for every request of the call that carried one: the
// first and each redirect hop that sent it again. It reports on the last of
// them: nil when its body was consumed completely (read to EOF, or, for a
// body of known length, read to that length), or the encoding, iterator,
// read, premature-close or cancellation error that stopped it." Call: "If
// request-body consumption fails, the error wraps its cause and the
// Response is still returned"; Redirects: "When the server answered before
// reading a body that the hop then drops, the upload is incomplete, and Call
// reports it with the final response (see WaitRequest)." A 303, and a 301
// or 302 after a POST, send no body, so the first request is the last that
// carried it. The transport
// reads each body as told and closes it before answering, so every result
// is settled when the call returns.
func TestWaitRequestReportsTheLastRequest(t *testing.T) {
	for _, tt := range []struct {
		status          int
		first, hop      int // bytes the transport reads of each body; -1 for all
		wantUploadError bool
	}{
		{307, 3, -1, false},
		{308, 3, -1, false},
		{307, -1, 3, true},
		{307, 3, 3, true},
		{307, -1, -1, false},
		{303, 3, -1, true},
		{302, 3, -1, true},
		{301, 3, -1, true},
		{303, -1, -1, false},
	} {
		t.Run(fmt.Sprintf("%d, first read %d, hop read %d", tt.status, tt.first, tt.hop), func(t *testing.T) {
			var hopBody atomic.Bool
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/up" {
					cutShort(r, tt.first)
					return memResponse(r, tt.status, http.Header{"Location": {"/next"}}, ""), nil
				}
				if r.Body != nil && r.Body != http.NoBody {
					hopBody.Store(true)
				}
				cutShort(r, tt.hop)
				return memResponse(r, 200, nil, "{}"), nil
			})
			c := parseAt(t, inflightDoc, "https://api.example.test", testDocURI,
				&openapi.Options{HTTPClient: &http.Client{Transport: rt}, Redirects: openapi.FollowAll})
			in := &openapi.Input{Body: []byte("0123456789")}

			resp, err := mustPrepare(t, c, "up", in).Send(t.Context())
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("status %d, want the hop's 200", resp.StatusCode)
			}
			if resends := tt.status == 307 || tt.status == 308; hopBody.Load() != resends {
				t.Fatalf("the hop carried a body: %t, want %t", hopBody.Load(), resends)
			}
			werr := waitResult(t, resp)
			if (werr != nil) != tt.wantUploadError {
				t.Errorf("WaitRequest = %v, want an error %t: it reports on the last request that carried the body", werr, tt.wantUploadError)
			}

			resp, err = c.Call(t.Context(), "up", in, nil)
			if resp == nil {
				t.Fatalf("Call returned no Response (error %v)", err)
			}
			if (err != nil) != tt.wantUploadError {
				t.Errorf("Call error %v, want an upload error %t", err, tt.wantUploadError)
			}
		})
	}
}
