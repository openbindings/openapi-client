package openapi_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// F14 (#14, A3, A4, A6, #18): the upload state machine, and the review's
// additions for the body-rewind and blocking-wait paths (#26). The
// contract: client.go, Response.WaitRequest ("It returns nil for a bodyless
// request or when the body reached EOF, or the encoding, iterator, read,
// premature-close or cancellation error that stopped it"), Call ("Call
// drains a successful response and waits for complete consumption of its
// request body before closing the response body. This lets a peer make
// progress on both sides of a finite duplex exchange. If request-body
// consumption fails, the error wraps its cause and the Response is still
// returned ... A successful response does not hide an incomplete upload"),
// and Request.Call (a replayable Request may be sent again).

const uploadDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://h.example.test"}],"paths":{
	"/p":{"put":{"operationId":"put","requestBody":{"content":{"application/json":{}}},"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}},
	"/o":{"post":{"operationId":"octets","requestBody":{"content":{"application/octet-stream":{}}}}},
	"/h":{"head":{"operationId":"head","responses":{"200":{"description":"ok","content":{"application/json":{}}}}}},
	"/u":{"get":{"operationId":"upper","responses":{"200":{"description":"ok","content":{"application/x-upper":{}}}}}}
}}`

func uploadClient(t *testing.T, rt http.RoundTripper, opts *openapi.Options) *openapi.Client {
	t.Helper()
	if opts == nil {
		opts = &openapi.Options{}
	}
	opts.HTTPClient = &http.Client{Transport: rt}
	return parseAt(t, uploadDoc, "", "https://h.example.test/openapi.json", opts)
}

// replayRT reads each request body as a retrying transport does: for each
// entry of reads, the body (the first) or a fresh one from GetBody (the
// rest) is read for that many bytes (-1 for all) and closed. It records
// what each generation read.
type replayRT struct {
	reads      []int
	noRequest  bool // leave Response.Request nil
	mu         sync.Mutex
	generation [][]byte
}

func (rt *replayRT) RoundTrip(req *http.Request) (*http.Response, error) {
	body := req.Body
	var got [][]byte
	for i, n := range rt.reads {
		if i > 0 {
			if req.GetBody == nil {
				return nil, errors.New("replayRT: no GetBody to replay")
			}
			b, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			body = b
		}
		var data []byte
		if n < 0 {
			data, _ = io.ReadAll(body)
		} else {
			data = make([]byte, n)
			m, _ := io.ReadFull(body, data)
			data = data[:m]
		}
		body.Close()
		got = append(got, data)
	}
	rt.mu.Lock()
	rt.generation = got
	rt.mu.Unlock()
	resp := &http.Response{StatusCode: 200, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("{}")), ContentLength: 2}
	if !rt.noRequest {
		resp.Request = req
	}
	return resp, nil
}

func (rt *replayRT) last() []byte {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.generation) == 0 {
		return nil
	}
	return rt.generation[len(rt.generation)-1]
}

// waitResult waits up to two seconds and reports the result; a timeout is
// reported as a failure of its own.
func waitResult(t *testing.T, resp *openapi.Response) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := resp.WaitRequest(ctx)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
		t.Errorf("WaitRequest did not return within 2s")
	}
	return err
}

// Replay after a transport retry (A3, #26 body rewind): the result is the
// final generation's. A final generation read to EOF is complete, whatever
// earlier ones did; a final generation closed early is a premature close,
// even after an earlier one completed; with no rewind, closing early is a
// premature close.
func TestF14ReplayGenerations(t *testing.T) {
	const full = `{"k":"abcdef"}`
	bodies := map[string]func() any{
		"bytes":          func() any { return []byte(full) },
		"strings.Reader": func() any { return strings.NewReader(full) },
		"bytes.Buffer":   func() any { return bytes.NewBufferString(full) },
		"encoded":        func() any { return map[string]string{"k": "abcdef"} },
	}
	scenarios := []struct {
		name      string
		reads     []int
		noRequest bool
		complete  bool
	}{
		{"read once", []int{-1}, false, true},
		{"rewound after a partial read", []int{2, -1}, false, true},
		{"closed early", []int{2}, false, false},
		{"replay closed early after a complete read", []int{-1, 2}, false, false},
		{"replay closed early, Response.Request nil", []int{-1, 2}, true, false},
		{"rewound, Response.Request nil", []int{2, -1}, true, true},
	}
	for _, sc := range scenarios {
		for name, body := range bodies {
			t.Run(sc.name+"/"+name, func(t *testing.T) {
				rt := &replayRT{reads: sc.reads, noRequest: sc.noRequest}
				c := uploadClient(t, rt, nil)

				resp, err := mustPrepare(t, c, "put", &openapi.Input{Body: body()}).Send(t.Context())
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				resp.Body.Close()
				werr := waitResult(t, resp)
				if sc.complete && werr != nil {
					t.Errorf("WaitRequest = %v, want nil", werr)
				}
				if !sc.complete && werr == nil {
					t.Errorf("WaitRequest = nil, want the premature close of the final generation")
				}
				if sc.complete && string(rt.last()) != full {
					t.Errorf("the final generation read %q, want %q", rt.last(), full)
				}

				resp, err = c.Call(t.Context(), "put", &openapi.Input{Body: body()}, nil)
				if resp == nil {
					t.Fatalf("Call returned no Response: %v", err)
				}
				if sc.complete && err != nil {
					t.Errorf("Call = %v, want nil", err)
				}
				if !sc.complete && err == nil {
					t.Errorf("Call = nil, want the incomplete upload reported")
				}
			})
		}
	}
}

// slowRT returns the response at once and reads the body on its own
// goroutine, one byte at a time, then closes it.
type slowRT struct {
	done atomic.Bool
}

func (rt *slowRT) RoundTrip(req *http.Request) (*http.Response, error) {
	go func() {
		b := make([]byte, 1)
		for {
			time.Sleep(5 * time.Millisecond)
			if _, err := req.Body.Read(b); err != nil {
				break
			}
		}
		rt.done.Store(true)
		req.Body.Close()
	}()
	return &http.Response{StatusCode: 200, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("{}")), ContentLength: 2, Request: req}, nil
}

// The blocking-wait path (#26): WaitRequest blocks while a slow transport is
// still reading and returns nil once it has read to EOF; "A cancellation of
// ctx ends only this wait".
func TestF14BlockingWait(t *testing.T) {
	for name, body := range map[string]func() any{
		"read once": func() any { return newOnce(`{"k":"abcdefgh"}`) },
		"bytes":     func() any { return []byte(`{"k":"abcdefgh"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			rt := &slowRT{}
			c := uploadClient(t, rt, nil)
			resp, err := mustPrepare(t, c, "put", &openapi.Input{Body: body()}).Send(t.Context())
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			defer resp.Body.Close()

			short, cancel := context.WithTimeout(t.Context(), time.Millisecond)
			defer cancel()
			if err := resp.WaitRequest(short); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("a wait cut by its own context = %v, want its context's error", err)
			}
			if err := waitResult(t, resp); err != nil {
				t.Errorf("WaitRequest = %v, want nil", err)
			}
			if !rt.done.Load() {
				t.Errorf("WaitRequest returned before the transport finished reading")
			}
		})
	}
}

// The body-rewind path through a real connection: a Request with a
// replayable body sent twice sends the full body both times, each send's
// WaitRequest nil (client.go, Request.Call).
func TestF14ResendPrepared(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/p":{"put":{"operationId":"put","requestBody":{"content":{"application/json":{}}}}}`), nil)
	for name, body := range map[string]any{"bytes": []byte(`{"a":1}`), "strings.Reader": strings.NewReader(`{"a":1}`), "encoded": map[string]int{"a": 1}} {
		req := mustPrepare(t, c, "put", &openapi.Input{Body: body})
		for i := range 2 {
			resp, err := req.Send(t.Context())
			if err != nil {
				t.Fatalf("%s send %d: %v", name, i, err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err := waitResult(t, resp); err != nil {
				t.Errorf("%s send %d: WaitRequest = %v", name, i, err)
			}
			if got := string(trimNL(w.last(t).Body)); got != `{"a":1}` {
				t.Errorf("%s send %d: server got %q", name, i, got)
			}
		}
	}
}

// duplexPeer is an HTTP/1.1 server that, for each request, reads the head,
// writes a respLen-byte response in full, and only then reads the request
// body: a finite duplex peer.
func duplexPeer(t *testing.T, respLen int64) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", respLen)
				if _, err := io.CopyN(conn, zeros{}, respLen); err != nil {
					return
				}
				io.Copy(io.Discard, req.Body)
			}()
		}
	}()
	return "http://" + ln.Addr().String()
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// Response cutoff (A4): with a nil out and a small MaxBodyBytes, Call stops
// reading at the bound ("nil discards it, reading at most MaxBodyBytes
// before closing the connection"), so it must close the response before
// waiting for an upload that a finite duplex peer consumes only after its
// response is read. It returns promptly, reporting the incomplete upload.
func TestF14CutoffClosesBeforeWaiting(t *testing.T) {
	const size = 64 << 20 // larger than the loopback socket buffers
	base := duplexPeer(t, size)
	doc := doc31(`"/o":{"post":{"operationId":"octets","requestBody":{"content":{"application/octet-stream":{}}}}}`)
	c := parseAt(t, doc, base, base+"/openapi.json", &openapi.Options{MaxBodyBytes: 1024})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := c.Call(ctx, "octets", &openapi.Input{Body: &onceReader{io.LimitReader(zeros{}, size)}}, nil)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call waited for the upload before closing the response (deadlocked for %v)", time.Since(start).Round(time.Millisecond))
	}
	if resp == nil {
		t.Errorf("Call returned no Response: %v", err)
	}
	if err == nil {
		t.Errorf("Call = nil, want the incomplete upload reported")
	}
}

// T1 (client.go, Input): "Call has stopped reading its body when it
// returns, provided a reader body returns from Read when the call's context
// ends or its connection closes." A peer that answers without reading the
// body and closes: Call reports the incomplete upload, and the reader is not
// read after Call returns.
func TestCallStopsReadingBody(t *testing.T) {
	base := rawHTTP(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", 100*time.Millisecond)
	doc := doc31(`"/o":{"post":{"operationId":"octets","requestBody":{"content":{"application/octet-stream":{}}}}}`)
	c := parseAt(t, doc, base, base+"/openapi.json", nil)
	r := &endlessReader{}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := c.Call(ctx, "octets", &openapi.Input{Body: r}, nil)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call did not return after the peer closed")
	}
	if err == nil {
		t.Errorf("Call = nil, want the incomplete upload reported")
	}
	n := r.reads.Load()
	time.Sleep(100 * time.Millisecond)
	if m := r.reads.Load(); m != n {
		t.Errorf("the body was read %d more times after Call returned", m-n)
	}
}

// endlessReader returns data at once from every Read, and counts reads.
type endlessReader struct{ reads atomic.Int64 }

func (r *endlessReader) Read(p []byte) (int, error) {
	r.reads.Add(1)
	clear(p)
	return len(p), nil
}

// A transport that leaves Response.Request nil (#18): Load works, and a
// Response from Send still carries the Client's settings and upload state:
// client.go, Response: "Its Request is the last request sent".
func TestF14ResponseRequestNil(t *testing.T) {
	noRequest := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(string(readPets(t))))}, nil
	})
	noPanic(t, "Load", func() {
		c, err := openapi.Load(t.Context(), "https://h.example.test/openapi.json", &openapi.Options{HTTPClient: &http.Client{Transport: noRequest}})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if u := c.DocumentURIs(); len(u) != 1 || u[0] != "https://h.example.test/openapi.json" {
			t.Errorf("DocumentURIs = %q", u)
		}
	})

	answer := func(ct, body string) roundTripFunc {
		return func(r *http.Request) (*http.Response, error) {
			if r.Body != nil {
				go func() { io.Copy(io.Discard, r.Body); r.Body.Close() }()
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}},
				Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
		}
	}

	// Codecs apply to Decode.
	c := uploadClient(t, answer("application/x-upper", "abc"), &openapi.Options{Codecs: map[string]openapi.Codec{"application/x-upper": tagCodec{tag: "UP"}}})
	resp, err := mustPrepare(t, c, "upper", nil).Send(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Request == nil {
		t.Errorf("Response.Request = nil")
	}
	var s string
	if err := resp.Decode(&s); err != nil || s != "UP:abc" {
		t.Errorf("Decode = %q, %v; want the Client's codec", s, err)
	}

	// MaxBodyBytes applies to Decode.
	c = uploadClient(t, answer("application/json", `"0123456789"`), &openapi.Options{MaxBodyBytes: 4})
	resp, err = mustPrepare(t, c, "upper", nil).Send(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var tooBig *http.MaxBytesError
	if err := resp.Decode(&s); !errors.As(err, &tooBig) {
		t.Errorf("Decode over MaxBodyBytes = %v, want an *http.MaxBytesError", err)
	}

	// HEAD stays bodiless.
	c = uploadClient(t, answer("application/json", "not json"), nil)
	pet := Pet{Name: "kept"}
	if _, err := c.Call(t.Context(), "head", nil, &pet); err != nil || pet.Name != "kept" {
		t.Errorf("HEAD: %v, out %+v; want no body read", err, pet)
	}

	// An upload still outstanding is not reported as done.
	pr, pw := io.Pipe()
	defer pw.Close()
	c = uploadClient(t, answer("application/json", "{}"), nil)
	resp, err = mustPrepare(t, c, "octets", &openapi.Input{Body: pr}).Send(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := resp.WaitRequest(ctx); err == nil {
		t.Errorf("WaitRequest = nil while the upload (an open pipe) is outstanding")
	}
}

// countConn counts the Write calls the transport makes on a connection.
type countConn struct {
	net.Conn
	writes *atomic.Int64
}

func (c countConn) Write(b []byte) (int, error) { c.writes.Add(1); return c.Conn.Write(b) }

func countingHTTPClient(writes *atomic.Int64) *http.Client {
	d := &net.Dialer{}
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return countConn{c, writes}, nil
	}}}
}

// The single-write small POST (F14: "in-memory replayable bodies handed to
// net/http as the reader types it treats as in-memory ... so small POSTs go
// out in one write"): a small in-memory body costs no more socket writes
// than the same request written by hand with a *bytes.Reader.
func TestF14SmallPostOneWrite(t *testing.T) {
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(201)
		rw.Write(petJSON)
	})
	var writes atomic.Int64
	hc := countingHTTPClient(&writes)
	c := parseAt(t, benchDoc, w.URL, w.URL+"/openapi.json", &openapi.Options{HTTPClient: hc})
	encoded := []byte(`{"name":"Rex","tag":"dog"}`)
	const n = 20
	perRequest := func(f func()) float64 {
		f() // warm the connection
		writes.Store(0)
		for range n {
			f()
		}
		return float64(writes.Load()) / n
	}
	baseline := perRequest(func() {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", w.URL+"/pets", bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	})
	for name, body := range map[string]func() any{
		"encoded":        func() any { return Pet{Name: "Rex", Tag: "dog"} },
		"bytes":          func() any { return encoded },
		"strings.Reader": func() any { return strings.NewReader(string(encoded)) },
		"bytes.Reader":   func() any { return bytes.NewReader(encoded) },
		"bytes.Buffer":   func() any { return bytes.NewBuffer(encoded) },
	} {
		got := perRequest(func() {
			var p Pet
			resp, err := c.Call(t.Context(), "createPet", &openapi.Input{Body: body()}, &p)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if err := resp.WaitRequest(t.Context()); err != nil {
				t.Errorf("%s: WaitRequest = %v", name, err)
			}
		})
		if got > baseline {
			t.Errorf("%s: %.2f writes per request, net/http with a *bytes.Reader %.2f", name, got, baseline)
		}
	}
}
