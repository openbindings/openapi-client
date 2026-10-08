package openapi_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for redirect chains and the requests they send: the
// chain's Timeout, a caller-set Host, request body generations and GetBody
// copies, the held Location field, cookies on a hop, transports that break
// the RoundTripper contract, origin comparison, the requests a Response
// holds, replaying a prepared Request, a nil Transport, credential
// refusals, defective oauth2 schemes, and lenient Location parsing.
// Credential value syntax cases are in TestCredentialValueSyntax.

// heldDone returns a channel closed when the test ends, for handlers that
// hold a response until the client gives up.
func heldDone(t *testing.T) <-chan struct{} {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	return done
}

// The remaining Timeout is computed immediately before each hop's Do, after
// the hop is signed and after the 3xx body is discarded; with none left, the
// hop's handed body is closed and the call returns the last Response with
// errChainTimeout. A hop's credential sources get a context bounded by the
// chain deadline (the call's values and cancellation kept), so a hanging
// source ends as a timeout. HTTPClient.Timeout bounds the whole chain, as
// net/http's own loop does (its doc: "includes connection time, any
// redirects, and reading the response body"); net/http computes its deadline
// once and reads the 3xx body under it (go1.25 client.go, Client.do). The
// last Response comes back with the error, its body closed (doc.go,
// Outcomes: "Whenever a response arrived, the *Response is returned, even
// with an error"). Each case's margins hold on a slow machine: slowness can
// only make the chain later, never less late.
func TestTimeoutChargesEverythingBeforeAHop(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}
	// wantChainTimeout checks the outcome: net/http's timeout, with the 302
	// that arrived.
	wantChainTimeout := func(t *testing.T, resp *openapi.Response, err error) {
		t.Helper()
		wantTimeout(t, err)
		if resp == nil || resp.StatusCode != 302 {
			t.Errorf("Response %v, want the 302 that arrived (TQ2)", resp)
		} else {
			wantBodyClosed(t, resp)
		}
		noSecrets(t, err, redirSecrets...)
	}
	// slowSource is a bearer source whose call on the hop first runs hop.
	slowSource := func(hop func(ctx context.Context) error) *source {
		return &source{fn: func(ctx context.Context, n int64) (string, error) {
			if n > 1 {
				if err := hop(ctx); err != nil {
					return "", err
				}
			}
			return bToken, nil
		}}
	}

	t.Run("a hop source that outlasts the Timeout", func(t *testing.T) {
		const timeout = 600 * time.Millisecond
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next"), "/next": ok}))
		src := slowSource(func(context.Context) error { time.Sleep(timeout + 200*time.Millisecond); return nil })
		o := redirOptions(src)
		o.HTTPClient = &http.Client{Timeout: timeout}
		c := parseFor(t, a, redirDoc, o)
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainTimeout(t, resp, err)
		if n := a.count(); n != 1 {
			t.Errorf("server received %d requests, want the first only: no time remained for the hop", n)
		}
	})
	t.Run("a slow hop source and a slow hop", func(t *testing.T) {
		const timeout = 600 * time.Millisecond
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next"), "/next": slowly(400*time.Millisecond, ok)}))
		src := slowSource(func(context.Context) error { time.Sleep(350 * time.Millisecond); return nil })
		o := redirOptions(src)
		o.HTTPClient = &http.Client{Timeout: timeout}
		c := parseFor(t, a, redirDoc, o)
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainTimeout(t, resp, err)
	})
	t.Run("a stalled 3xx body", func(t *testing.T) {
		const timeout = 600 * time.Millisecond
		done := heldDone(t)
		a := newWire(t, routes(map[string]http.HandlerFunc{
			"/r": func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/next")
				w.Header().Set("Content-Length", "10")
				w.WriteHeader(302)
				w.(http.Flusher).Flush()
				select { // the 10 bytes never come
				case <-r.Context().Done():
				case <-done:
				}
			},
			"/next": slowly(300*time.Millisecond, ok),
		}))
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{Timeout: timeout}
		c := parseFor(t, a, redirDoc, o)
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainTimeout(t, resp, err)
		if n := a.count(); n != 1 {
			t.Errorf("server received %d requests, want the first only", n)
		}
	})
	t.Run("a trickled 3xx body", func(t *testing.T) {
		const timeout = time.Second
		a := newWire(t, routes(map[string]http.HandlerFunc{
			"/r": func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/next")
				w.Header().Set("Content-Length", "10")
				w.WriteHeader(302)
				for range 10 { // 800 ms for the body
					select {
					case <-time.After(80 * time.Millisecond):
					case <-r.Context().Done():
						return
					}
					io.WriteString(w, "x")
					w.(http.Flusher).Flush()
				}
			},
			"/next": slowly(600*time.Millisecond, ok),
		}))
		o := redirOptions(fixedSource(bToken))
		o.HTTPClient = &http.Client{Timeout: timeout}
		c := parseFor(t, a, redirDoc, o)
		resp, err := c.Call(t.Context(), "getR", redirInput("GET"), nil)
		wantChainTimeout(t, resp, err)
	})
	t.Run("a hop source that blocks", func(t *testing.T) {
		const timeout = 300 * time.Millisecond
		type key struct{}
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next"), "/next": ok}))
		var (
			mu       sync.Mutex
			deadline time.Time
			bounded  bool
			value    any
		)
		src := slowSource(func(ctx context.Context) error {
			mu.Lock()
			deadline, bounded = ctx.Deadline()
			value = ctx.Value(key{})
			mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second): // standing in for never
				return errors.New("the hop source was never stopped")
			}
		})
		o := redirOptions(src)
		o.HTTPClient = &http.Client{Timeout: timeout}
		c := parseFor(t, a, redirDoc, o)
		start := time.Now()
		resp, err := c.Call(context.WithValue(t.Context(), key{}, "call value"), "getR", redirInput("GET"), nil)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("the call took %v with a Timeout of %v", d, timeout)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error %v, want the chain's timeout", err)
		}
		if resp == nil || resp.StatusCode != 302 {
			t.Errorf("Response %v, want the 302 that arrived (TQ2)", resp)
		} else {
			wantBodyClosed(t, resp)
		}
		mu.Lock()
		defer mu.Unlock()
		if !bounded || deadline.After(start.Add(timeout+250*time.Millisecond)) {
			t.Errorf("the hop source's context has deadline %v (set %t), want the chain's, about %v after the call began",
				deadline.Sub(start), bounded, timeout)
		}
		if value != "call value" {
			t.Errorf("the hop source's context holds %v, want the call's values", value)
		}
		if n := a.count(); n != 1 {
			t.Errorf("server received %d requests, want the first only", n)
		}
	})
	t.Run("a hop source sees the call's cancellation", func(t *testing.T) {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next"), "/next": ok}))
		entered := make(chan struct{})
		src := slowSource(func(ctx context.Context) error {
			close(entered)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("the hop source was never stopped")
			}
		})
		o := redirOptions(src)
		o.HTTPClient = &http.Client{Timeout: 10 * time.Second}
		c := parseFor(t, a, redirDoc, o)
		ctx, cancel := context.WithCancel(t.Context())
		go func() { <-entered; cancel() }()
		_, err := c.Call(ctx, "getR", redirInput("GET"), nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error %v, want the call's cancellation", err)
		}
	})
}

// A caller-set Host is kept only when the Location has no authority
// component (ref.Host == ""); a network-path reference names another
// authority (RFC 3986 section 4.2). RFC 9110 section 7.2: Host carries the
// target URI's host and port. The Host is kept across a relative Location,
// as net/http does (Go issue 22233).
func TestNetworkPathLocationDropsCallerHost(t *testing.T) {
	const host = "virtual.example.test"
	b := newWire(t, nil)
	for _, tt := range []struct {
		name string
		loc  func(a *wire) string
		kept bool
	}{
		{"a network-path reference to another origin", func(*wire) string { return "//" + b.hostport() + "/x" }, false},
		{"a network-path reference to the same origin", func(a *wire) string { return "//" + a.hostport() + "/next" }, false},
		{"an absolute path", func(*wire) string { return "/next" }, true},
		{"a relative path", func(*wire) string { return "next" }, true},
		{"a query only", func(*wire) string { return "?x=1" }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := newWire(t, nil)
			a.setAnswer(routes(map[string]http.HandlerFunc{"/r": func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery == "" {
					redirect(302, tt.loc(a))(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, "{}")
			}}))
			before := b.count()
			c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
			req := mustPrepare(t, c, "getR", nil)
			req.HTTP.Host = host
			sendAndClose(t, req)
			var hop rec
			if reqs := a.requests(); len(reqs) == 2 {
				hop = reqs[1]
			} else if b.count() == before+1 {
				hop = b.last(t)
			} else {
				t.Fatalf("the hop was not sent")
			}
			switch {
			case tt.kept && hop.Host != host:
				t.Errorf("hop Host %q, want the caller's %q kept", hop.Host, host)
			case !tt.kept && hop.Host == host:
				t.Errorf("hop Host %q: the caller's Host went to the authority the Location names", hop.Host)
			}
		})
	}
}

// lateCloser is a caller's request body that records its Close.
type lateCloser struct {
	io.Reader
	closed chan struct{}
	once   sync.Once
}

func (l *lateCloser) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

// A body generation ends at the transport's Close with no Read in flight,
// not at EOF; EOF or an error only records the result and stops later Reads
// reaching the reader. A generation ends only when the transport has closed
// it and no Read is in flight. net/http, RoundTripper: "RoundTrip must
// always close the body, including on errors, but depending on the
// implementation may do so in a separate goroutine even after RoundTrip
// returns." client.go, Response.WaitRequest: "It relies on the transport
// closing the request body". The transport here reads the body to EOF,
// answers, and closes it only when the test lets it; the caller's Close must
// have run before Call or WaitRequest returns, not after (as late as 101 ms
// after, in the failure this test guards against).
func TestCallerCloseRunsBeforeReturn(t *testing.T) {
	for _, tt := range []struct{ via, body string }{
		{"Call", "the caller's"}, {"Send", "the caller's"}, {"Call", "the client's"},
	} {
		t.Run(tt.via+", "+tt.body, func(t *testing.T) {
			gate, answered := make(chan struct{}), make(chan struct{})
			var gateOnce sync.Once
			t.Cleanup(func() { gateOnce.Do(func() { close(gate) }) })
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				io.Copy(io.Discard, r.Body)
				go func() { <-gate; r.Body.Close() }()
				close(answered)
				return memResponse(r, 200, nil, "{}"), nil
			})
			c := parseAt(t, inflightDoc, "https://api.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
			body := &lateCloser{Reader: strings.NewReader("hello"), closed: make(chan struct{})}
			var req *openapi.Request
			if tt.body == "the caller's" {
				req = mustPrepare(t, c, "up", nil)
				req.HTTP.Body, req.HTTP.GetBody, req.HTTP.ContentLength = body, nil, 5
			} else {
				req = mustPrepare(t, c, "up", &openapi.Input{Body: []byte("hello")})
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			returned := make(chan struct{})
			if tt.via == "Call" {
				go func() {
					defer close(returned)
					req.Call(ctx, nil)
				}()
				await(t, answered, "the answer")
				wantNotReturned(t, returned, "Call returned before the transport closed the body")
			} else {
				resp, err := req.Send(ctx)
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				go func() {
					defer close(returned)
					resp.WaitRequest(ctx)
				}()
				wantNotReturned(t, returned, "WaitRequest returned before the transport closed the body")
			}
			gateOnce.Do(func() { close(gate) })
			await(t, returned, tt.via+" after the transport's Close")
			if tt.body == "the caller's" {
				select {
				case <-body.closed:
				default:
					t.Errorf("%s returned before the caller's body was closed", tt.via)
				}
			}
		})
	}
}

// A copy CheckRedirect takes with GetBody is the caller's and is not waited
// for; generations are counted when handed to the transport. WaitRequest
// relies on the transport closing the body and every copy it takes with
// GetBody (the middleware case). client.go, Response.WaitRequest: "It relies
// on the transport closing the request body, as http.RoundTripper requires,
// and every copy it takes with GetBody; with a transport that neither reads
// nor closes it, the wait, and Call's, ends only with the context."
func TestCheckRedirectGetBodyCopyNotWaitedFor(t *testing.T) {
	for name, peek := range map[string]int{"dropped unread": 0, "dropped after a few bytes": 3} {
		t.Run(name, func(t *testing.T) {
			a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(307, "/next")}))
			o := &openapi.Options{Redirects: openapi.FollowAll, HTTPClient: &http.Client{CheckRedirect: func(r *http.Request, _ []*http.Request) error {
				if r.GetBody == nil {
					return errors.New("the hop has no GetBody")
				}
				rc, err := r.GetBody()
				if err == nil && peek > 0 {
					io.ReadFull(rc, make([]byte, peek))
				}
				return err // the copy is dropped
			}}}
			c := parseFor(t, a, plainRedirDoc, o)
			in := &openapi.Input{Body: map[string]any{"n": 1}}
			timed := func(what string, f func(ctx context.Context) error) {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				start := time.Now()
				if err := f(ctx); err != nil {
					t.Errorf("%s: %v", what, err)
				}
				if d := time.Since(start); d > time.Second {
					t.Errorf("%s took %v: it waited for a copy CheckRedirect took", what, d)
				}
			}
			timed("Call", func(ctx context.Context) error {
				_, err := c.Call(ctx, "postR", in, nil)
				return err
			})
			timed("Send and WaitRequest", func(ctx context.Context) error {
				resp, err := mustPrepare(t, c, "postR", in).Send(ctx)
				if err != nil {
					return err
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				return resp.WaitRequest(ctx)
			})
			if reqs := a.requests(); len(reqs) != 4 || string(reqs[1].Body) != string(reqs[0].Body) {
				t.Errorf("server received %d requests; want each hop to resend the body", len(reqs))
			}
		})
	}
	// A transport middleware that drops a copy it took is waited for, until
	// the context ends.
	t.Run("a transport middleware's copy", func(t *testing.T) {
		w := newWire(t, nil)
		peekRT := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.GetBody != nil {
				if rc, err := r.GetBody(); err == nil {
					io.ReadFull(rc, make([]byte, 4)) // logs a prefix, drops the copy
				}
			}
			return http.DefaultTransport.RoundTrip(r)
		})
		c := parseFor(t, w, plainRedirDoc, &openapi.Options{HTTPClient: &http.Client{Transport: peekRT}})
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		resp, err := c.Call(ctx, "postR", &openapi.Input{Body: map[string]any{"n": 1}}, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Call error %v, want the context's: the copy the transport took never ended", err)
		}
		if resp == nil || resp.StatusCode != 200 {
			t.Errorf("Call returned %v, want the 200 with the context's error", resp)
		}
	})
}

// When CheckRedirect installs its GetBody copy as the hop's Body, that copy
// is sent, so it counts as handed to the transport. Call and WaitRequest
// wait for it as for any hop body, and WaitRequest reports its result, since
// it is the last request that carried the body. client.go,
// Response.WaitRequest: it waits "for every request of the call that carried
// one: the first, each copy the transport takes with GetBody to send it
// again, and each redirect hop that sent it again. It reports on the last of
// them". The transport reads the first body completely, and the
// hop's as each case says; in the last case it closes the hop's body only
// when the test lets it.
func TestCheckRedirectInstalledCopyIsWaitedFor(t *testing.T) {
	const payload = "0123456789"
	install := func(r *http.Request, _ []*http.Request) error {
		rc, err := r.GetBody()
		if err != nil {
			return err
		}
		r.Body = rc
		return nil
	}
	newClient := func(t *testing.T, hop func(r *http.Request)) *openapi.Client {
		rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/up" {
				cutShort(r, -1)
				return memResponse(r, 307, http.Header{"Location": {"/next"}}, ""), nil
			}
			hop(r)
			return memResponse(r, 200, nil, "{}"), nil
		})
		return parseAt(t, inflightDoc, "https://api.example.test", testDocURI, &openapi.Options{
			HTTPClient: &http.Client{Transport: rt, CheckRedirect: install}, Redirects: openapi.FollowAll,
		})
	}
	for _, tt := range []struct {
		name    string
		read    int // bytes of the installed copy the transport reads; -1 for all
		wantErr bool
	}{{"read completely", -1, false}, {"cut short", 3, true}} {
		t.Run(tt.name, func(t *testing.T) {
			var got atomic.Value
			c := newClient(t, func(r *http.Request) {
				if tt.read < 0 {
					b, _ := io.ReadAll(r.Body)
					got.Store(string(b))
					r.Body.Close()
					return
				}
				cutShort(r, tt.read)
			})
			in := &openapi.Input{Body: []byte(payload)}
			resp, err := mustPrepare(t, c, "up", in).Send(t.Context())
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if werr := waitResult(t, resp); (werr != nil) != tt.wantErr {
				t.Errorf("WaitRequest = %v, want an error %t: it reports on the copy the hop sent", werr, tt.wantErr)
			}
			if tt.read < 0 && got.Load() != payload {
				t.Errorf("the hop sent %v, want the installed copy, %q", got.Load(), payload)
			}
			resp, err = c.Call(t.Context(), "up", in, nil)
			if resp == nil || resp.StatusCode != 200 {
				t.Fatalf("Call returned %v (error %v), want the hop's 200", resp, err)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("Call error %v, want an upload error %t", err, tt.wantErr)
			}
		})
	}
	t.Run("waited for until the transport closes it", func(t *testing.T) {
		for _, via := range []string{"Call", "Send"} {
			gate, answered := make(chan struct{}), make(chan struct{})
			var gateOnce, answerOnce sync.Once
			t.Cleanup(func() { gateOnce.Do(func() { close(gate) }) })
			c := newClient(t, func(r *http.Request) {
				io.Copy(io.Discard, r.Body)
				go func() { <-gate; r.Body.Close() }()
				answerOnce.Do(func() { close(answered) })
			})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			in := &openapi.Input{Body: []byte(payload)}
			returned := make(chan struct{})
			if via == "Call" {
				go func() {
					defer close(returned)
					c.Call(ctx, "up", in, nil)
				}()
				await(t, answered, "the hop's answer")
				wantNotReturned(t, returned, "Call returned before the transport closed the copy CheckRedirect installed")
			} else {
				resp, err := mustPrepare(t, c, "up", in).Send(ctx)
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				go func() {
					defer close(returned)
					resp.WaitRequest(ctx)
				}()
				wantNotReturned(t, returned, "WaitRequest returned before the transport closed the copy CheckRedirect installed")
			}
			gateOnce.Do(func() { close(gate) })
			await(t, returned, via+" after the transport's Close")
		}
	})
}

// scriptServer is a TCP listener that answers each request, one per
// connection, with the raw response reply gives for its path, recording the
// paths requested.
type scriptServer struct {
	ln    net.Listener
	URL   string
	mu    sync.Mutex
	paths []string
}

func newScriptServer(t *testing.T, reply func(path string) string) *scriptServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &scriptServer{ln: ln, URL: "http://" + ln.Addr().String()}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				io.Copy(io.Discard, req.Body)
				s.mu.Lock()
				s.paths = append(s.paths, req.URL.Path)
				s.mu.Unlock()
				io.WriteString(conn, reply(req.URL.Path))
			}()
		}
	}()
	return s
}

func (s *scriptServer) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

// The held Location key holds a colon, which an HTTP/1 field name cannot
// contain and HTTP/2 rejects: Location is moved under a key the wire cannot
// produce. client.go, Redirects: only a 3xx with a Location can be followed;
// "A 3xx not followed is the outcome, a *StatusError"; Response: "The
// responses in that chain hold what the server sent." Go's HTTP/1 reader
// keeps a field name with spaces as sent (net/textproto, ReadMIMEHeader;
// go.dev/issue/34540), so a server can send the old key.
func TestServerFieldNamedLikeTheHeldLocation(t *testing.T) {
	const field = "Location held by openapi"
	reply := func(fields string) func(string) string {
		return func(path string) string {
			if path == "/r" {
				return "HTTP/1.1 302 Found\r\n" + fields + "Content-Length: 0\r\nConnection: close\r\n\r\n"
			}
			return rawOK
		}
	}
	t.Run("without a Location", func(t *testing.T) {
		s := newScriptServer(t, reply(field+": /elsewhere\r\n"))
		c := parseAt(t, plainRedirDoc, s.URL, s.URL+"/openapi.json", &openapi.Options{Redirects: openapi.FollowAll})
		_, err := c.Call(t.Context(), "getR", nil, nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) || se.StatusCode != 302 {
			t.Errorf("Call error %v, want the 302 as the outcome: it has no Location", err)
		}
		if got := s.requested(); len(got) != 1 {
			t.Errorf("server received %q, want /r only", got)
		}
		d := c.With(func(o *openapi.Options) { o.Redirects = openapi.FollowNone })
		resp, err := mustPrepare(t, d, "getR", nil).Send(t.Context())
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		resp.Body.Close()
		if v, ok := resp.Header["Location"]; ok {
			t.Errorf("the Response has Location %q, which the server never sent", v)
		}
		if v := resp.Header[field]; len(v) != 1 || v[0] != "/elsewhere" {
			t.Errorf("the server's field %q = %q, want it as sent", field, v)
		}
	})
	t.Run("beside a Location", func(t *testing.T) {
		s := newScriptServer(t, reply("Location: /next\r\n"+field+": /elsewhere\r\n"))
		c := parseAt(t, plainRedirDoc, s.URL, s.URL+"/openapi.json", &openapi.Options{Redirects: openapi.FollowAll})
		resp := mustCall(t, c, "getR", nil, nil)
		if got := s.requested(); strings.Join(got, " ") != "/r /next" {
			t.Errorf("server received %q, want /r then /next, the Location", got)
		}
		prev := resp.Request.Response
		if prev == nil || prev.Header.Get("Location") != "/next" || len(prev.Header[field]) != 1 || prev.Header[field][0] != "/elsewhere" {
			t.Errorf("the 302 does not hold the fields the server sent")
		}
	})
}

// On a hop, with a jar, the carried Cookie field drops each pair whose name
// the 3xx's Set-Cookie sets, and the jar supplies it, as net/http does
// (go.dev/issue/17494); the first request is not affected. net/http's
// redirect loop removes from the carried Cookie field the cookies a 3xx set
// when the Client has a Jar (go1.25 client.go, makeHeadersCopier). Here a
// login-style 302 rotates the cookie the parameter c sent.
func TestHopDropsCookiesThe3xxSet(t *testing.T) {
	doc := doc31(`
		"/r":{"parameters":[{"name":"a","in":"cookie"},{"name":"c","in":"cookie"}],
			"get":{"operationId":"plain"},"post":{"operationId":"withCredential","security":[{"key_c":[]}]}}`, credSchemes)
	rotate := func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "c", Value: "server", Path: "/"})
		redirect(302, "/next")(w, r)
	}
	for _, tt := range []struct {
		key      string
		jar      bool
		first    []string
		hopWants []string
	}{
		{"plain", true, []string{"a=pa", "c=p"}, []string{"a=pa", "c=server"}},
		{"withCredential", true, []string{"a=pa", "c=p", "sid=" + cSecret}, []string{"a=pa", "c=server", "sid=" + cSecret}},
		{"plain", false, []string{"a=pa", "c=p"}, []string{"a=pa", "c=p"}},
		{"withCredential", false, []string{"a=pa", "c=p", "sid=" + cSecret}, []string{"a=pa", "c=p", "sid=" + cSecret}},
	} {
		t.Run(fmt.Sprintf("%s, jar %t", tt.key, tt.jar), func(t *testing.T) {
			w := newWire(t, routes(map[string]http.HandlerFunc{"/r": rotate}))
			hc := &http.Client{}
			if tt.jar {
				hc.Jar = newJar(t, w.URL)
			}
			c := parseFor(t, w, doc, &openapi.Options{HTTPClient: hc, Redirects: openapi.FollowAll,
				Credentials: map[string]openapi.Credential{"key_c": openapi.Secret(cSecret)}})
			mustCall(t, c, tt.key, &openapi.Input{Params: map[string]any{"a": "pa", "c": "p"}}, nil)
			reqs := w.requests()
			if len(reqs) != 2 {
				t.Fatalf("server received %d requests, want 2", len(reqs))
			}
			for i, want := range [][]string{tt.first, tt.hopWants} {
				if got := cookiePairs(t, reqs[i].Header); strings.Join(got, "; ") != strings.Join(want, "; ") {
					t.Errorf("request %d Cookie pairs %q, want %q", i, got, want)
				}
			}
		})
	}
}

// nilNilRT breaks the RoundTripper contract: it returns no response and no
// error.
type nilNilRT struct{}

func (nilNilRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }

// nilBodyRT breaks it another way: a response of positive length with no
// Body.
type nilBodyRT struct{}

func (nilBodyRT) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: 5, Request: r}, nil
}

// Regression check, not contract: when the inner transport breaks the
// RoundTripper contract (nil response and nil error; a positive ContentLength
// with a nil Body), the error's wording mirrors net/http's, which names the
// caller's transport type, never the client's own wrapper, noFollow. net/http
// writes these errors with the transport's type (go1.25 client.go, send:
// "http: RoundTripper implementation (%T) ..."). What doc.go, Outcomes
// promises stays asserted: a transport failure is "the *url.Error from the
// http.Client".
func TestBrokenTransportNamedInError(t *testing.T) {
	for name, rt := range map[string]http.RoundTripper{"nil response and nil error": nilNilRT{}, "a nil Body": nilBodyRT{}} {
		t.Run(name, func(t *testing.T) {
			_, refErr := (&http.Client{Transport: rt}).Get("https://api.example.test/r")
			var ref *url.Error
			if !errors.As(refErr, &ref) {
				t.Fatalf("net/http returned %v", refErr)
			}
			c := parseAt(t, plainRedirDoc, "https://api.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
			_, err := c.Call(t.Context(), "getR", nil, nil)
			var ue *url.Error
			if !errors.As(err, &ue) {
				t.Fatalf("Call error %v (%T), want the http.Client's *url.Error", err, err)
			}
			if ue.Err == nil || ue.Err.Error() != ref.Err.Error() {
				t.Errorf("the error reads %q, want net/http's %q", ue.Err, ref.Err)
			}
			if strings.Contains(err.Error(), "noFollow") {
				t.Errorf("the error names the client's own wrapper: %q", err)
			}
		})
	}
}

// Origins compare scheme and host with ASCII-only case folding (RFC 3986
// section 6.2.2.1), not strings.EqualFold, which folds Unicode, so hosts
// IDNA keeps distinct (final sigma) would compare equal and credentials
// would follow a hop to another host. Non-ASCII bytes must match exactly,
// which can only call one host two origins, never two hosts one. client.go,
// Redirects: on a hop to another origin "the client removes the credentials
// it added"; Request.HTTP: a URL changed to another origin is refused when
// the call needs credentials. UTS 46 (nontransitional) maps σ and ς to
// different ASCII labels (xn--4xa, xn--3xa). The transport dials nothing.
func TestOriginComparesHostsASCIIOnly(t *testing.T) {
	for _, tt := range []struct {
		base, loc string
		placed    bool
	}{
		{"https://api.σ.test", "https://api.ς.test/x", false},
		{"https://api.É.test", "https://api.é.test/x", false},
		{"https://api.example.test", "https://API.Example.TEST/x", true},
		{"https://api.σ.test", "https://API.σ.TEST/x", true},
	} {
		t.Run("a hop from "+tt.base+" to "+tt.loc, func(t *testing.T) {
			rt := &memRT{}
			rt.answer = func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/bearer" {
					return memResponse(r, 302, http.Header{"Location": {tt.loc}}, ""), nil
				}
				return memResponse(r, 200, nil, "{}"), nil
			}
			c := parseAt(t, credDoc, "https://unused.example.test", testDocURI, &openapi.Options{
				HTTPClient: &http.Client{Transport: rt}, BaseURL: tt.base, Redirects: openapi.FollowAll, Credentials: credSet(),
			})
			mustCall(t, c, "bearer", nil, nil)
			reqs := rt.requests()
			if len(reqs) != 2 {
				t.Fatalf("transport carried %d requests, want 2", len(reqs))
			}
			if got := reqs[1].Header.Get("Authorization") != ""; got != tt.placed {
				t.Errorf("token placed on the hop to %s: %t, want %t", reqs[1].URL.Host, got, tt.placed)
			}
		})
	}
	const sigma, final = "api.σ.test", "api.ς.test"
	t.Run("Request.HTTP moved to the other host", func(t *testing.T) {
		hc, rt := memClient()
		c := parseAt(t, credDoc, "https://unused.example.test", testDocURI, &openapi.Options{HTTPClient: hc, BaseURL: "https://" + sigma, Credentials: credSet()})
		req := mustPrepare(t, c, "bearer", nil)
		req.HTTP.URL.Host = final
		resp, err := req.Send(t.Context())
		refusedBeforeSending(t, nil, resp, err)
		if n := rt.count(); n != 0 {
			t.Errorf("transport carried %d requests with the token to another host", n)
		}
	})
	t.Run("a ParamWriter moving to the other host", func(t *testing.T) {
		hc, rt := memClient()
		c := parseAt(t, securedWriterDoc, "https://unused.example.test", testDocURI, &openapi.Options{HTTPClient: hc, BaseURL: "https://" + sigma, Credentials: writerCreds()})
		resp, err := c.Call(t.Context(), "w", writer(func(r *http.Request) { r.URL.Host = final }), nil)
		refusedBeforeSending(t, nil, resp, err)
		if n := rt.count(); n != 0 {
			t.Errorf("transport carried %d requests with the token to another host", n)
		}
	})
}

// Response.Request never holds the jar's cookies, on every path: with a jar,
// the request sent is a copy. client.go, Response: "Its Request is the last
// request sent, after any redirects, without the credentials the client
// added to its URL and header fields or the cookies the HTTPClient's Jar
// supplied, and so is every earlier request reachable from it." The eight
// paths: Call and Prepare with Send, FollowNone and FollowAll, with and
// without a credential. The request's own cookie parameter stays.
func TestResponseRequestHoldsNoJarCookies(t *testing.T) {
	doc := doc31(`
		"/r":{"get":{"operationId":"plain","parameters":[{"name":"p","in":"cookie"}]}},
		"/s":{"get":{"operationId":"withCredential","security":[{"key_c":[]}],"parameters":[{"name":"p","in":"cookie"}]}}`, credSchemes)
	for _, follow := range []openapi.Redirects{openapi.FollowNone, openapi.FollowAll} {
		for _, key := range []string{"plain", "withCredential"} {
			for _, via := range []string{"Call", "Send"} {
				t.Run(fmt.Sprintf("Redirects %d, %s, %s", follow, key, via), func(t *testing.T) {
					w := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, "/next"), "/s": redirect(302, "/next")}))
					jar := newJar(t, w.URL, &http.Cookie{Name: "j", Value: "jar-7Lp", Path: "/"})
					c := parseFor(t, w, doc, &openapi.Options{HTTPClient: &http.Client{Jar: jar}, Redirects: follow,
						Credentials: map[string]openapi.Credential{"key_c": openapi.Secret(cSecret)}})
					in := &openapi.Input{Params: map[string]any{"p": "pv"}}
					var resp *openapi.Response
					if via == "Call" {
						resp, _ = c.Call(t.Context(), key, in, nil)
					} else {
						var err error
						if resp, err = mustPrepare(t, c, key, in).Send(t.Context()); err == nil {
							resp.Body.Close()
						}
					}
					if resp == nil {
						t.Fatalf("no Response")
					}
					if got := cookiePairs(t, w.requests()[0].Header); !contains(got, "j=jar-7Lp") {
						t.Fatalf("the first request carried Cookie %q; the jar did not apply", got)
					}
					chain := requestChain(resp.Request)
					for i, r := range chain {
						for _, v := range r.Header.Values("Cookie") {
							if strings.Contains(v, "jar-7Lp") {
								t.Errorf("request %d back holds the jar's cookie: Cookie %q", i, v)
							}
						}
					}
					if first := chain[len(chain)-1]; !contains(cookiePairs(t, first.Header), "p=pv") {
						t.Errorf("the first request's own cookie parameter is gone from Response.Request's chain")
					}
					wantNoCredentialsIn(t, resp.Request)
				})
			}
		}
	}
}

// A prepared Request whose HTTP.Body and GetBody are set is sent the first
// time from HTTP.Body and every later or concurrent time from GetBody.
// client.go, Request.Call: "When r's body can be sent again (HTTP.GetBody is
// set, or there is no body), r may be sent any number of times, concurrently
// too"; Request.HTTP: "A send takes HTTP.Body the first time and GetBody for
// every replay". HTTP.Body and GetBody give bodies of one length and
// different bytes, to tell which each send took.
func TestPreparedCallerBodySentAgain(t *testing.T) {
	const first, copied = "AAAAAAAA", "BBBBBBBB"
	prepare := func(t *testing.T, c *openapi.Client) (*openapi.Request, *atomic.Int32) {
		req := mustPrepare(t, c, "postR", nil)
		var n atomic.Int32
		req.HTTP.Body = io.NopCloser(strings.NewReader(first))
		req.HTTP.ContentLength = int64(len(first))
		req.HTTP.GetBody = func() (io.ReadCloser, error) {
			n.Add(1)
			return io.NopCloser(strings.NewReader(copied)), nil
		}
		return req, &n
	}
	bodies := func(w *wire) map[string]int {
		m := map[string]int{}
		for _, r := range w.requests() {
			m[string(r.Body)]++
		}
		return m
	}
	t.Run("one after another", func(t *testing.T) {
		w := newWire(t, nil)
		c := parseFor(t, w, plainRedirDoc, nil)
		req, n := prepare(t, c)
		for i := range 3 {
			if _, err := req.Call(t.Context(), nil); err != nil {
				t.Errorf("send %d: %v", i, err)
			}
		}
		if got, want := bodies(w), map[string]int{first: 1, copied: 2}; !maps.Equal(got, want) {
			t.Errorf("the server received bodies %v, want %v", got, want)
		}
		if got := n.Load(); got != 2 {
			t.Errorf("GetBody called %d times, want once for each later send", got)
		}
	})
	t.Run("concurrently", func(t *testing.T) {
		w := newWire(t, nil)
		c := parseFor(t, w, plainRedirDoc, nil)
		req, _ := prepare(t, c)
		const sends = 8
		var wg sync.WaitGroup
		errs := make(chan error, sends)
		for range sends {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := req.Call(t.Context(), nil)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("a send: %v", err)
			}
		}
		if got, want := bodies(w), map[string]int{first: 1, copied: sends - 1}; !maps.Equal(got, want) {
			t.Errorf("the server received bodies %v, want %v", got, want)
		}
	})
}

// A nil caller Transport resolves to http.DefaultTransport at each send, as
// net/http's Client does, so replacing DefaultTransport (test mocks) keeps
// working after Load. client.go, Options.HTTPClient: "Nil means
// http.DefaultClient"; net/http, Client.Transport: "If nil, DefaultTransport
// is used." The test replaces the global and restores it.
func TestDefaultTransportResolvedAtSend(t *testing.T) {
	w := newWire(t, nil)
	for name, hc := range map[string]*http.Client{"a nil HTTPClient": nil, "an HTTPClient with no Transport": {}} {
		t.Run(name, func(t *testing.T) {
			c := parseFor(t, w, plainRedirDoc, &openapi.Options{HTTPClient: hc})
			orig := http.DefaultTransport
			var n atomic.Int32
			http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				n.Add(1)
				return orig.RoundTrip(r)
			})
			func() {
				defer func() { http.DefaultTransport = orig }()
				mustCall(t, c, "getR", nil, nil)
			}()
			if got := n.Load(); got != 1 {
				t.Errorf("the replaced http.DefaultTransport carried %d requests, want the call's", got)
			}
		})
	}
}

// A source's refused value on the first request is keyed by its scheme, and
// the error names the scheme. errors.go, RequestError.Settings: keyed in the
// form "Options.<Field>[<name>], for Credentials and Variables", and "An empty
// secret from a credential source is keyed as a missing credential is";
// RequestError.Error: "Error describes every problem and the field that fixes
// each". The hop's error, which has no Settings key, still names the scheme
// (TestHopCredentialRefusalNamesItsScheme).
func TestSourceRefusalNamesItsSchemeOnce(t *testing.T) {
	doc := doc31(`"/r":{"get":{"operationId":"getR"}}`, `"security":[{"corp_bearer":[]}]`,
		`"components":{"securitySchemes":{"corp_bearer":{"type":"http","scheme":"bearer"}}}`)
	for name, value := range map[string]string{"a value no header field carries": "bad\nZq9-7Yt", "an empty secret": ""} {
		t.Run(name, func(t *testing.T) {
			w := newWire(t, nil)
			c := parseFor(t, w, doc, &openapi.Options{Credentials: map[string]openapi.Credential{"corp_bearer": fixedSource(value).credential()}})
			resp, err := c.Call(t.Context(), "getR", nil, nil)
			re := refusedBeforeSending(t, w, resp, err)
			wantKeys(t, "Settings", re.Settings, true, credKey("corp_bearer"))
			if !strings.Contains(err.Error(), "corp_bearer") {
				t.Errorf("error %q does not name the scheme corp_bearer", err)
			}
			if value != "" {
				noSecrets(t, err, value)
			}
		})
	}
}

// A defective oauth2 scheme still describes every flow that is an object,
// with the fields it has; Err says why it cannot be used. describe.go,
// SecurityScheme: Flows for oauth2; Flow: Type, URLs "as written", Scopes;
// SecurityScheme.Err: "a defective or missing declaration". The order of
// Flows is not fixed by the contract.
func TestDefectiveOAuthSchemeListsItsFlows(t *testing.T) {
	doc := doc31(`"/o":{"get":{"operationId":"o","security":[{"o":[]}]}}`,
		`"components":{"securitySchemes":{"o":{"type":"oauth2","flows":{
			"clientCredentials":{"tokenUrl":"https://auth.example.test/t","scopes":{"read":"Read"}},
			"password":{"scopes":{"a":"b"}},
			"implicit":5,
			"authorizationCode":{"authorizationUrl":"https://auth.example.test/a","refreshUrl":"/r"}
		}}}}`)
	c := parseAt(t, doc, "https://api.example.test", testDocURI, nil)
	s := mustOp(t, c, "o").Security[0].Schemes[0]
	if s.Err == nil {
		t.Errorf("SecurityScheme.Err is nil for flows missing required fields")
	}
	flows := map[string]openapi.Flow{}
	for _, f := range s.Flows {
		flows[f.Type] = f
	}
	if len(flows) != 3 {
		t.Errorf("Flows lists %d flows, want the three that are objects (not implicit, which is 5)", len(flows))
	}
	if f := flows["clientCredentials"]; f.TokenURL != "https://auth.example.test/t" || !maps.Equal(f.Scopes, map[string]string{"read": "Read"}) {
		t.Errorf("clientCredentials = %+v, want its token URL and scopes", f)
	}
	if f, ok := flows["password"]; !ok || f.TokenURL != "" || !maps.Equal(f.Scopes, map[string]string{"a": "b"}) {
		t.Errorf("password = %+v (listed %t), want its scopes and no token URL", f, ok)
	}
	if f, ok := flows["authorizationCode"]; !ok || f.AuthorizationURL != "https://auth.example.test/a" || f.RefreshURL != "/r" || f.TokenURL != "" || len(f.Scopes) != 0 {
		t.Errorf("authorizationCode = %+v (listed %t), want the fields it has", f, ok)
	}
}

// Redirects accepts a Location that url.Parse accepts, as net/http and
// browsers follow leniently. client.go, Redirects: "Only 301, 302, 303, 307
// and 308 with a Location that url.Parse accepts can be followed, as
// net/http follows them." A Location RFC 3986 would not take but url.Parse
// does is followed, to the target net/http sends for it.
func TestLenientLocationFollowed(t *testing.T) {
	for _, loc := range []string{"/a b", "/a<b>", "/caf\u00e9", "/a\"b", "/a{b}", "/a\\b"} {
		a := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, loc)}))
		c := parseFor(t, a, plainRedirDoc, &openapi.Options{Redirects: openapi.FollowAll})
		if resp := mustCall(t, c, "getR", nil, nil); resp.StatusCode != 200 {
			t.Errorf("%q: status %d, want the hop's 200", loc, resp.StatusCode)
		}
		ref := newWire(t, routes(map[string]http.HandlerFunc{"/r": redirect(302, loc)}))
		resp, err := http.Get(ref.URL + "/r")
		if err != nil {
			t.Fatalf("net/http: %v", err)
		}
		resp.Body.Close()
		reqs, refs := a.requests(), ref.requests()
		if len(reqs) != 2 || len(refs) != 2 || reqs[1].RequestURI != refs[1].RequestURI {
			t.Errorf("%q: the client sent %d requests, net/http %d; want the same hop target", loc, len(reqs), len(refs))
		}
	}
}
