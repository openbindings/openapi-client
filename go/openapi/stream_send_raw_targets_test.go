package openapi_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

type streamTunnel struct {
	bytes.Buffer
	closed bool
}

func (b *streamTunnel) Close() error { b.closed = true; return nil }

// Stream and sequence decoding preserve Send's status neutrality and the
// original upgrade body capabilities, plus Call/Decode raw and text-target
// precedence and writer-limit exemptions.
func TestStreamPreservesSendAndRawTargets(t *testing.T) {
	t.Run("Send status neutral", func(t *testing.T) {
		c := streamCallClient(t, "application/jsonl", "1\n2\n", 503, nil)
		r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
		if err != nil || r.StatusCode != 503 {
			t.Fatalf("%v %v", r, err)
		}
		p, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil || string(p) != "1\n2\n" {
			t.Fatalf("%q %v", p, err)
		}
	})
	t.Run("upgrade exact Body", func(t *testing.T) {
		body := &streamTunnel{}
		body.WriteString("upgrade bytes")
		c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) { return streamHTTP(r, 101, "", body), nil }), nil)
		r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
		if err != nil || r.Body != body {
			t.Fatalf("Body changed (%T), error %v", r.Body, err)
		}
		if _, ok := r.Body.(io.ReadWriteCloser); !ok {
			t.Fatal("lost tunnel writer")
		}
		r.Body.Close()
	})
	for _, ct := range []string{"application/jsonl", "application/json-seq", "text/event-stream"} {
		t.Run("raw "+ct, func(t *testing.T) {
			wire := "framing intentionally invalid\n"
			c := streamCallClient(t, ct, wire, 200, func(o *openapi.Options) { o.MaxBodyBytes = 1; o.MaxItemBytes = 1 })
			var out bytes.Buffer
			if _, err := c.Call(t.Context(), "get", nil, &out); err != nil || out.String() != wire {
				t.Fatalf("writer %q %v", out.String(), err)
			}
			c = c.With(func(o *openapi.Options) { o.MaxBodyBytes = -1 })
			var raw []byte
			if _, err := c.Call(t.Context(), "get", nil, &raw); err != nil || string(raw) != wire {
				t.Fatalf("raw %q %v", raw, err)
			}
		})
	}
	t.Run("text SSE precedence", func(t *testing.T) {
		wire := "data: x\n\n"
		var out any
		if _, err := cCall7(t, "text/event-stream", wire, &out); err != nil || out != wire {
			t.Fatalf("any %#v %v", out, err)
		}
		var text string
		if _, err := cCall7(t, "text/event-stream", wire, &text); err != nil || text != wire {
			t.Fatalf("string %q %v", text, err)
		}
	})
	t.Run("multipart Call remains bytes", func(t *testing.T) {
		wire := streamMultipart("\r\nhello")
		var out any
		if _, err := cCall7(t, "multipart/mixed; boundary=B", wire, &out); err != nil || !bytes.Equal(out.([]byte), []byte(wire)) {
			t.Fatalf("%#v %v", out, err)
		}
	})
	t.Run("Decode invalid target retry", func(t *testing.T) {
		r, b := streamedResponse(t, "application/jsonl", "1\n2\n", nil)
		var nilTarget *int
		err := r.Decode(nilTarget)
		var de *openapi.DecodeError
		if !errors.As(err, &de) || b.reads.Load() != 0 || b.closed.Load() != 0 {
			t.Fatalf("error %v reads %d closed %d", err, b.reads.Load(), b.closed.Load())
		}
		var raw []byte
		if err := r.Decode(&raw); err != nil || string(raw) != "1\n2\n" {
			t.Fatalf("%q %v", raw, err)
		}
	})
}

// Stream inherits Call's no-body statuses; a response body forbidden by
// HTTP must not be read even if a custom transport supplies one.
func TestStreamNoBodyStatusUnread(t *testing.T) {
	body := &streamBody{reader: strings.NewReader("must not be consumed")}
	c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) {
		return streamHTTP(r, 204, "application/jsonl", body), nil
	}), nil)
	r, err := c.Stream(t.Context(), "get", nil)
	if err != nil || r == nil {
		t.Fatalf("%v %v", r, err)
	}
	if body.reads.Load() != 0 {
		t.Fatal("204 Body was read")
	}
	r.Body.Close()
}
