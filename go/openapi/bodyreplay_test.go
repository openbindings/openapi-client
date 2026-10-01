package openapi_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 4, replay: which bodies can be sent again, by a redirect, a second
// send of a Request, or a transport retry through GetBody, and which are
// read once. client.go, Input.Body: "A body can be sent again, by a
// redirect, a retry or a second send of a Request, when every source in it
// can: a []byte, a *bytes.Buffer, a *bytes.Reader and a *strings.Reader,
// from the bytes they hold when the call is prepared, without being
// drained; an *os.File that Stat reports to be a regular file, from its
// offset when the call is prepared; and every value the client encodes. Any
// other reader, such as a pipe or os.Stdin, and an iterator, is read once."
// "The client never closes a reader it is given." Request.HTTP: "GetBody is
// set when the body can be sent again"; Request.Call: such a Request "may
// be sent any number of times", and otherwise "it may be sent once, and
// sending it again is refused with a *RequestError, nothing sent";
// Redirects: "a hop that must resend a body that cannot be sent again (see
// Input.Body) is not followed" and "A 3xx not followed is the outcome, a
// *StatusError". doc.go, Fixed rules, Form bodies: "A body is encoded once,
// when the call is prepared, so HTTP.Body and every GetBody give the same
// bytes; a file in a field is read into memory then"; Header fields: the
// client generates "Content-Length for a body that can be sent again".
// Stage 4 ledger, Q10: "a form body whose every source can be sent again is
// encoded once when prepared (replayable readers read by ReadAt, not
// drained), so it has Content-Length; one holding a reader read once is
// encoded as the transport reads it, without Content-Length".

const replayPaths = `
	"/r/text":{"post":{"operationId":"text","requestBody":{"content":{"text/plain":{}}}}},
	"/r/form":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"s":{"type":"string"},"raw":{}}}}}}}},
	"/r/mp":{"post":{"operationId":"mp","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"title":{"type":"string"},"file":{}}}}}}}},
	"/r/jsonl":{"post":{"operationId":"jsonl","requestBody":{"content":{"application/jsonl":{}}}}}`

// replayCase is a body built afresh for each use, and what the bytes sent
// must be.
type replayCase struct {
	name  string
	key   string
	body  func(t *testing.T) any
	once  bool // read once
	sized bool // sent with Content-Length
	check func(t *testing.T, ct string, body []byte)
}

// mpFile checks a multipart body of a title part "T" and a file part.
func mpFile(content string) func(t *testing.T, ct string, body []byte) {
	return func(t *testing.T, ct string, body []byte) {
		t.Helper()
		_, _, parts := readMultipart(t, ct, body)
		checkParts(t, parts, []wantPart{
			{disposition: formData("file", "file"), ctype: "application/octet-stream", content: content},
			{disposition: formData("title"), ctype: "text/plain", content: "T"},
		})
	}
}

func exactly(want string) func(t *testing.T, ct string, body []byte) {
	return func(t *testing.T, ct string, body []byte) {
		t.Helper()
		if string(body) != want {
			t.Errorf("body %q, want %q", body, want)
		}
	}
}

func replayCases(t *testing.T) []replayCase {
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	return []replayCase{
		{"text value", "text", func(*testing.T) any { return "hello" }, false, true, exactly("hello")},
		{"form values", "form", func(*testing.T) any { return map[string]any{"s": "a b"} }, false, true, exactly("s=a+b")},
		{"form with a strings.Reader", "form", func(*testing.T) any {
			return map[string]any{"raw": strings.NewReader("r r"), "s": "x"}
		}, false, true, exactly("raw=r+r&s=x")},
		{"form with a read-once reader", "form", func(*testing.T) any {
			return map[string]any{"raw": newOnce("r r"), "s": "x"}
		}, true, false, exactly("raw=r+r&s=x")},
		{"multipart with bytes", "mp", func(*testing.T) any {
			return map[string]any{"title": "T", "file": []byte("bytes")}
		}, false, true, mpFile("bytes")},
		{"multipart with a bytes.Buffer", "mp", func(*testing.T) any {
			b := bytes.NewBufferString("0123456789")
			b.Next(3)
			return map[string]any{"title": "T", "file": b}
		}, false, true, mpFile("3456789")},
		{"multipart with a bytes.Reader", "mp", func(*testing.T) any {
			r := bytes.NewReader([]byte("abcdef"))
			r.Read(make([]byte, 2))
			return map[string]any{"title": "T", "file": openapi.Part{Content: r}}
		}, false, true, mpFile("cdef")},
		{"multipart with a strings.Reader", "mp", func(*testing.T) any {
			return map[string]any{"title": "T", "file": strings.NewReader("uvwxyz")}
		}, false, true, mpFile("uvwxyz")},
		{"multipart with a regular file", "mp", func(t *testing.T) any {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.Close() })
			f.Seek(4, io.SeekStart)
			return map[string]any{"title": "T", "file": f}
		}, false, true, mpFile("456789")},
		{"multipart with a read-once reader", "mp", func(*testing.T) any {
			return map[string]any{"title": "T", "file": newOnce("once")}
		}, true, false, mpFile("once")},
		{"multipart with bytes and a read-once reader", "mp", func(*testing.T) any {
			return map[string]any{"title": "T", "file": []any{[]byte("a"), newOnce("b")}}
		}, true, false, func(t *testing.T, ct string, body []byte) {
			_, _, parts := readMultipart(t, ct, body)
			checkParts(t, parts, []wantPart{
				{disposition: formData("file", "file"), ctype: "application/octet-stream", content: "a"},
				{disposition: formData("file", "file"), ctype: "application/octet-stream", content: "b"},
				{disposition: formData("title"), ctype: "text/plain", content: "T"},
			})
		}},
		{"JSON Lines slice", "jsonl", func(*testing.T) any { return []int{1, 2} }, false, true, func(t *testing.T, _ string, body []byte) {
			wantLines(t, body, "1", "2")
		}},
		{"JSON Lines iterator", "jsonl", func(*testing.T) any { return seqOf(1, 2) }, true, false, func(t *testing.T, _ string, body []byte) {
			wantLines(t, body, "1", "2")
		}},
	}
}

// A prepared Request: GetBody set exactly when the body can be sent again,
// giving the same bytes each time; sent twice, the same bytes and
// Content-Type (so the same multipart boundary) each time; a read-once
// body sent once, and a second send refused with nothing sent.
func TestBodyReplayPrepared(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(replayPaths), nil)
	for _, tt := range replayCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			req := mustPrepare(t, c, tt.key, &openapi.Input{Body: tt.body(t)})
			if (req.HTTP.GetBody == nil) != tt.once {
				t.Fatalf("GetBody set: %t, for a body read once: %t", req.HTTP.GetBody != nil, tt.once)
			}
			ct := req.HTTP.Header.Get("Content-Type")
			var first []byte
			if !tt.once {
				first = preparedBody(t, req)
				if again := preparedBody(t, req); !bytes.Equal(again, first) {
					t.Errorf("GetBody gave %q, then %q", first, again)
				}
				tt.check(t, ct, first)
				if tt.sized && req.HTTP.ContentLength != int64(len(first)) {
					t.Errorf("ContentLength %d for %d bytes", req.HTTP.ContentLength, len(first))
				}
			}
			before := w.count()
			sends := 2
			if tt.once {
				sends = 1
			}
			for range sends {
				if _, err := req.Call(t.Context(), nil); err != nil {
					t.Fatalf("Request.Call: %v", err)
				}
			}
			reqs := w.requests()[before:]
			if len(reqs) != sends {
				t.Fatalf("server received %d requests, want %d", len(reqs), sends)
			}
			for _, r := range reqs {
				if r.Header.Get("Content-Type") != ct {
					t.Errorf("sent as %q, prepared as %q", r.Header.Get("Content-Type"), ct)
				}
				tt.check(t, ct, r.Body)
				if first != nil && !bytes.Equal(r.Body, first) {
					t.Errorf("sent %q, GetBody gave %q", r.Body, first)
				}
				if tt.sized && (r.ContentLength != int64(len(r.Body)) || len(r.TransferEncoding) != 0) {
					t.Errorf("sent with Content-Length %d, Transfer-Encoding %q, for %d bytes", r.ContentLength, r.TransferEncoding, len(r.Body))
				}
				if tt.once && !slices.Contains(r.TransferEncoding, "chunked") {
					t.Errorf("a body read once sent with Content-Length %d, Transfer-Encoding %q; want no length", r.ContentLength, r.TransferEncoding)
				}
			}
			if tt.once {
				resp, err := req.Call(t.Context(), nil)
				refusedSince(t, w, before+1, resp, err)
			}
		})
	}
}

// A 307 resends a body that can be sent again, the same bytes under the
// same Content-Type; one read once is not followed, and the 307 is the
// outcome. A transport that retries through GetBody reads the same bytes
// again.
func TestBodyReplayRedirectAndRetry(t *testing.T) {
	w := newWire(t, func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/r/") {
			rw.Header().Set("Location", "/final")
			rw.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		io.WriteString(rw, "{}")
	})
	c := parseFor(t, w, doc31(replayPaths), &openapi.Options{Redirects: openapi.FollowAll})
	for _, tt := range replayCases(t) {
		t.Run(tt.name+", 307", func(t *testing.T) {
			before := w.count()
			_, err := c.Call(t.Context(), tt.key, &openapi.Input{Body: tt.body(t)}, nil)
			reqs := w.requests()[before:]
			if tt.once {
				var se *openapi.StatusError
				if !errors.As(err, &se) || se.StatusCode != http.StatusTemporaryRedirect || len(reqs) != 1 {
					t.Errorf("Call = %v after %d requests, want the 307 as a *StatusError after 1", err, len(reqs))
				}
				return
			}
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if len(reqs) != 2 || reqs[1].Path != "/final" {
				t.Fatalf("requests %d, want the original and /final", len(reqs))
			}
			ct := reqs[0].Header.Get("Content-Type")
			if !bytes.Equal(reqs[0].Body, reqs[1].Body) || reqs[1].Header.Get("Content-Type") != ct {
				t.Errorf("the hop sent %q as %q, the first request %q as %q", reqs[1].Body, reqs[1].Header.Get("Content-Type"), reqs[0].Body, ct)
			}
			tt.check(t, ct, reqs[1].Body)
		})
	}
	for _, tt := range replayCases(t) {
		if tt.once {
			continue
		}
		t.Run(tt.name+", transport retry", func(t *testing.T) {
			rt := &replayRT{reads: []int{-1, -1}}
			rc := parseAt(t, doc31(replayPaths), "https://h.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
			req := mustPrepare(t, rc, tt.key, &openapi.Input{Body: tt.body(t)})
			if _, err := req.Call(t.Context(), nil); err != nil {
				t.Fatalf("Call: %v", err)
			}
			rt.mu.Lock()
			gens := slices.Clone(rt.generation)
			rt.mu.Unlock()
			if len(gens) != 2 || !bytes.Equal(gens[0], gens[1]) {
				t.Fatalf("generations %q, want two the same", gens)
			}
			tt.check(t, req.HTTP.Header.Get("Content-Type"), gens[1])
		})
	}
}

// The sources are left as they were: bytes.Buffer and bytes.Reader not
// drained, and no reader closed (client.go, Input.Body).
func TestBodySourcesUntouched(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(replayPaths), nil)
	buf := bytes.NewBufferString("buffered")
	br := bytes.NewReader([]byte("reader"))
	once := &closeRecorder{Reader: strings.NewReader("once")}
	mustCall(t, c, "mp", &openapi.Input{Body: map[string]any{"title": "T", "file": []any{buf, br, once}}}, nil)
	if buf.Len() != len("buffered") || br.Len() != len("reader") {
		t.Errorf("the client drained a source: Buffer.Len %d, Reader.Len %d", buf.Len(), br.Len())
	}
	if once.wasClosed() {
		t.Errorf("the client closed a reader it was given")
	}
	_, _, parts := readMultipart(t, w.last(t).Header.Get("Content-Type"), w.last(t).Body)
	if len(parts) != 4 || string(parts[0].body) != "buffered" || string(parts[1].body) != "reader" || string(parts[2].body) != "once" {
		t.Errorf("parts %d", len(parts))
	}
	sr := strings.NewReader("form reader")
	mustCall(t, c, "form", &openapi.Input{Body: map[string]any{"raw": []any{sr, buf, br}}}, nil)
	if sr.Len() != len("form reader") || buf.Len() != len("buffered") || br.Len() != len("reader") {
		t.Errorf("a form body drained a source: %d, %d, %d", sr.Len(), buf.Len(), br.Len())
	}
	if got := string(w.last(t).Body); got != "raw=form+reader&raw=buffered&raw=reader" {
		t.Errorf("form body %q", got)
	}
}

// tailGate is a caller's reader of n bytes: it returns them as fast as it
// is read, then blocks its final Read until gate is closed (or done), and
// returns io.EOF. It records a Close, which the client must never make.
type tailGate struct {
	n, given int
	gate     <-chan struct{}
	done     <-chan struct{}
	closed   atomic.Bool
}

func (g *tailGate) Read(p []byte) (int, error) {
	if g.given < g.n {
		m := min(len(p), g.n-g.given)
		for i := range m {
			p[i] = 'z'
		}
		g.given += m
		return m, nil
	}
	select {
	case <-g.gate:
	case <-g.done:
	}
	return 0, io.EOF
}

func (g *tailGate) Close() error { g.closed.Store(true); return nil }

// A reader in a multipart body is streamed as it is read, never held: a 4
// MiB part whose reader returns io.EOF only after the server has received 2
// MiB of the body (stage brief, Scope: "readers streamed without being
// held"; Example_filesUpload: "The file is streamed from disk when the
// request is sent"). A client that read the reader to its end before
// sending would never let the server see those bytes, and the call would
// not end.
func TestMultipartReaderStreamed(t *testing.T) {
	srv := newBodyServer(t, false, "")
	c := parseAt(t, mpDoc(), srv.URL, srv.URL+"/openapi.json", nil)
	ctx, _ := gateCtx(t)
	g := &tailGate{n: 4 << 20, gate: srv.seenBytes(2 << 20), done: ctx.Done()}
	r := awaitCall(t, callAsync(ctx, c, "mp", &openapi.Input{Body: map[string]any{"blob": openapi.Part{Content: g}}}), "Call")
	if r.err != nil {
		t.Fatalf("Call: %v", r.err)
	}
	body, err := srv.finished(t)
	if err != nil {
		t.Fatalf("the server's read ended with %v", err)
	}
	srv.mu.Lock()
	ct := srv.header.Get("Content-Type")
	srv.mu.Unlock()
	_, _, parts := readMultipart(t, ct, body)
	if len(parts) != 1 {
		t.Fatalf("%d parts, want 1", len(parts))
	}
	if p := parts[0]; len(p.body) != 4<<20 || strings.Trim(string(p.body), "z") != "" || p.header.Get("Content-Disposition") != formData("blob", "blob") {
		t.Errorf("part %q of %d bytes, want 4 MiB of z", p.header.Get("Content-Disposition"), len(p.body))
	}
	if g.closed.Load() {
		t.Errorf("the client closed a reader it was given")
	}
}
