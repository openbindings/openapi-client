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

type prepared8Tunnel struct {
	bytes.Buffer
	closeErr error
	closed   int
}

func (b *prepared8Tunnel) Close() error {
	b.closed++
	return b.closeErr
}

// Public contract: stage-7/validation/static-8d469a7/openapi-api.log,
// Request.HTTP (1668-1683), Send (1712-1730), and doc.go (220-226).
// A CONNECT operation uses caller-owned authority-form edits and retains
// its transport's exact duplex Body. Stage 7's Tier 2 Close correction
// (stage-7/ledger.md, "owner accepts one Tier 2 correction") explicitly
// assigns raw tunnel Close behavior to that transport, including its error.
func TestPrepared8ConnectTunnelOwnership(t *testing.T) {
	closeErr := errors.New("transport tunnel close")
	body := &prepared8Tunnel{closeErr: closeErr}
	body.WriteString("from peer")
	var sent bool
	doc := editionDoc("3.2.1", `"/tunnel":{"additionalOperations":{"CONNECT":{"operationId":"connect","responses":{"200":{"description":"tunnel"}}}}}`)
	c := editionClient(t, doc, &openapi.Options{BaseURL: "https://tunnel.example.test", HTTPClient: &http.Client{Transport: stream7RT(func(r *http.Request) (*http.Response, error) {
		sent = true
		if r.Method != "CONNECT" || r.URL.Host != "tunnel.example.test:443" || r.URL.Path != "" || r.Header.Get("X-Tunnel") != "requested" {
			t.Errorf("caller edits not preserved: %s %s %v", r.Method, r.URL, r.Header)
		}
		if op := openapi.OperationFromContext(r.Context()); op == nil || op.Key != "connect" {
			t.Errorf("missing CONNECT operation in transport context")
		}
		return stream7HTTP(r, 200, "", body), nil
	})}})
	req := mustPrepare(t, c, "connect", nil)
	if sent {
		t.Fatal("Prepare sent the tunnel request")
	}
	req.HTTP.URL.Host = "tunnel.example.test:443"
	req.HTTP.URL.Path, req.HTTP.URL.RawPath = "", ""
	req.HTTP.Header.Set("X-Tunnel", "requested")
	resp, err := req.Send(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Body != body || body.closed != 0 {
		t.Fatalf("Send changed or closed the tunnel body: %T, closed %d", resp.Body, body.closed)
	}
	rw, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatal("Send lost the tunnel writer")
	}
	got := make([]byte, len("from peer"))
	if _, err := io.ReadFull(rw, got); err != nil || string(got) != "from peer" {
		t.Fatalf("tunnel read %q, %v", got, err)
	}
	if _, err := rw.Write([]byte("to peer")); err != nil || body.String() != "to peer" {
		t.Fatalf("tunnel write %q, %v", body.String(), err)
	}
	if err := resp.WaitRequest(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != closeErr || body.closed != 1 {
		t.Fatalf("raw Close = %v, count %d; want transport's exact result", err, body.closed)
	}
}

// Public contract: the same saved API, Call no-body/empty rules (560-568)
// and Response.Decode (1849-1864). Decode applies those rules to raw Send
// responses at any status, without StatusError; a forbidden body supplied
// by a custom transport must remain unread even if its media claims JSON.
func TestPrepared8DecodeNoBody(t *testing.T) {
	doc := editionDoc("3.2.1", `"/x":{"get":{"operationId":"get"},"head":{"operationId":"head"},"additionalOperations":{"CONNECT":{"operationId":"connect"}}}`)
	for _, tc := range []struct {
		key    string
		status int
	}{
		{"get", 101}, {"get", 204}, {"get", 205}, {"get", 304},
		{"head", 200}, {"connect", 200},
	} {
		t.Run(tc.key+" "+http.StatusText(tc.status), func(t *testing.T) {
			body := &stream7Body{reader: strings.NewReader("not a JSON body")}
			c := editionClient(t, doc, &openapi.Options{BaseURL: "https://body.example.test", HTTPClient: &http.Client{Transport: stream7RT(func(r *http.Request) (*http.Response, error) {
				return stream7HTTP(r, tc.status, "application/json", body), nil
			})}})
			req := mustPrepare(t, c, tc.key, nil)
			resp, err := req.Send(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if body.reads.Load() != 0 || body.closed.Load() != 0 {
				t.Fatal("Send read or closed the response")
			}
			out := struct{ Value string }{Value: "retained"}
			if err := resp.Decode(&out); err != nil {
				t.Fatalf("Decode for %s %d: %v", tc.key, tc.status, err)
			}
			if out.Value != "retained" || body.reads.Load() != 0 || body.closed.Load() == 0 {
				t.Fatalf("no-body Decode: out %+v, reads %d, closes %d", out, body.reads.Load(), body.closed.Load())
			}
		})
	}
}
