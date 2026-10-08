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

func decodeResponseClient(t *testing.T, status int, body io.ReadCloser) *openapi.Client {
	t.Helper()
	doc := doc31(`"/x":{"get":{"operationId":"get","responses":{"default":{"description":"result","content":{"application/json":{}}}}}}`)
	return parseAt(t, doc, "https://api.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: streamRT(func(r *http.Request) (*http.Response, error) {
		return streamHTTP(r, status, "application/json", body), nil
	})}})
}

// DecodeError's response-copy, Content and ContentLength promises (errors.go,
// DecodeError), Response.Decode's invalid-out guarantee (an invalid out
// leaves Body unread and open for a retry), and the same promises for an
// invalid target passed to StatusError.Decode.
// Scalar fields test both response copies without requiring deep-cloned maps.
func TestDecodeInvalidTargetCopiesResponse(t *testing.T) {
	for _, mode := range []string{"Response.Decode", "StatusError.Decode"} {
		t.Run(mode, func(t *testing.T) {
			const payload = `{"n":7}`
			body := &streamBody{reader: strings.NewReader(payload)}
			c := decodeResponseClient(t, 422, body)
			var original *openapi.Response
			var decode func(any) error
			if mode == "Response.Decode" {
				r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				original, decode = r, r.Decode
			} else {
				_, err := c.Call(t.Context(), "get", nil, nil)
				var se *openapi.StatusError
				if !errors.As(err, &se) {
					t.Fatal(err)
				}
				original, decode = se.Response, se.Decode
			}
			defer original.Body.Close()
			reads, closes := body.reads.Load(), body.closed.Load()
			var de *openapi.DecodeError
			if err := decode(42); !errors.As(err, &de) {
				t.Fatalf("invalid target: %v", err)
			}
			if de.Response == original || de.Response.Response == original.Response {
				t.Error("DecodeError aliases the original outer or HTTP response")
			}
			original.StatusCode, original.Security = 499, "caller changed metadata"
			if de.StatusCode != 422 || de.Security != "" {
				t.Errorf("DecodeError metadata changed: status %d security %q", de.StatusCode, de.Security)
			}
			if len(de.Content) != 0 || de.ContentLength != 0 {
				t.Errorf("invalid-target capture has Content %q, length %d", de.Content, de.ContentLength)
			}
			captured, err := io.ReadAll(de.Body)
			de.Body.Close()
			if err != nil || len(captured) != 0 {
				t.Errorf("error Body reads %q, %v; want empty capture", captured, err)
			}
			var raw []byte
			var another *openapi.DecodeError
			if err := decode(42); !errors.As(err, &another) {
				t.Fatal(err)
			}
			if err := another.Decode(&raw); err != nil || len(raw) != 0 {
				t.Errorf("error's promoted Decode reads %q, %v", raw, err)
			}
			if body.reads.Load() != reads || body.closed.Load() != closes {
				t.Error("using the error's Body consumed or closed the original response")
			}
			var out struct{ N int }
			if err := decode(&out); err != nil || out.N != 7 {
				t.Errorf("original valid retry = %+v, %v", out, err)
			}
		})
	}
}

// client.go, Options.Codecs: "A key Load would refuse, given through
// Client.With, refuses at Settings "Options.Codecs" each call that encodes a
// value as a body or content-serialized parameter, or whose out is a pointer
// to decode into (see Client.Call), a *any included. With such a key, when the
// response has a body (see Client.Call), decoding it into such a pointer with
// Response.Decode, or with StatusError.Decode when its Err is nil, returns a
// *DecodeError naming Options.Codecs". So a decoding target makes malformed
// Codecs relevant after raw Send too, rather than falling back to built-in
// decoding, and raw targets bypass the map; configuration-error read timing is
// not pinned.
func TestWithMalformedCodecsDecodeTargets(t *testing.T) {
	for _, mode := range []string{"Send 200", "Send 422", "StatusError 422"} {
		for _, target := range []string{"typed", "any", "nil", "bytes", "writer"} {
			t.Run(mode+"/"+target, func(t *testing.T) {
				const payload = `{"n":7}`
				status := 422
				if mode == "Send 200" {
					status = 200
				}
				body := &streamBody{reader: strings.NewReader(payload)}
				c := decodeResponseClient(t, status, body).With(func(o *openapi.Options) {
					o.Codecs["application/json; charset=utf-8"] = jsonCodec{}
				})
				var decode func(any) error
				if strings.HasPrefix(mode, "Send") {
					r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer r.Body.Close()
					decode = r.Decode
				} else {
					_, err := c.Call(t.Context(), "get", nil, nil)
					var se *openapi.StatusError
					if !errors.As(err, &se) || se.Err != nil || string(se.Content) != payload {
						t.Fatalf("status capture: %v", err)
					}
					decode = se.Decode
				}
				var raw []byte
				var buf bytes.Buffer
				var out any
				switch target {
				case "typed":
					out = new(struct{ N int })
				case "any":
					out = new(any)
				case "bytes":
					out = &raw
				case "writer":
					out = &buf
				}
				count := 1
				if mode == "StatusError 422" && (target == "typed" || target == "any") {
					count = 2 // Each decoding of retained Content uses the same rules.
				}
				for range count {
					err := decode(out)
					if target == "typed" || target == "any" {
						var de *openapi.DecodeError
						var se *openapi.StatusError
						if !errors.As(err, &de) || errors.As(err, &se) || !strings.Contains(err.Error(), "Options.Codecs") {
							t.Errorf("late codec configuration error: %v", err)
						}
					} else if err != nil {
						t.Errorf("raw target used codecs: %v", err)
					}
				}
				if target == "bytes" && string(raw) != payload || target == "writer" && buf.String() != payload {
					t.Fatalf("raw bypass bytes %q, writer %q", raw, buf.String())
				}
			})
		}
	}
}

// Controls for malformed Codecs: an invalid out retains its specific
// unread/open promise; known no-body responses have no codec work after
// target validation.
func TestWithMalformedCodecsNoBodyAndInvalidTarget(t *testing.T) {
	for _, status := range []int{200, 204, 304} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := &streamBody{reader: strings.NewReader(`{"n":7}`)}
			c := decodeResponseClient(t, status, body).With(func(o *openapi.Options) { o.Codecs["not a media type"] = jsonCodec{} })
			// Call cannot know the eventual no-body status at its preflight.
			_, err := c.Call(t.Context(), "get", nil, new(any))
			wantKeys(t, "Call Settings", asRequestError(t, err).Settings, true, "Options.Codecs")
			r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			var de *openapi.DecodeError
			if err := r.Decode(42); !errors.As(err, &de) || body.reads.Load() != 0 || body.closed.Load() != 0 {
				t.Fatalf("invalid target consumed response: %v", err)
			}
			if status == 200 {
				var raw []byte
				if err := r.Decode(&raw); err != nil || string(raw) != `{"n":7}` {
					t.Fatalf("raw retry %q, %v", raw, err)
				}
			} else {
				out := 7
				if err := r.Decode(&out); err != nil || out != 7 || body.reads.Load() != 0 {
					t.Fatalf("no-body decode %d, %v, reads %d", out, err, body.reads.Load())
				}
			}
		})
	}
	t.Run("StatusError no-body", func(t *testing.T) {
		body := &streamBody{reader: strings.NewReader("must not read")}
		c := decodeResponseClient(t, 304, body).With(func(o *openapi.Options) { o.Codecs["not a media type"] = jsonCodec{} })
		_, err := c.Call(t.Context(), "get", nil, nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) {
			t.Fatal(err)
		}
		out := 7
		if err := se.Decode(&out); err != nil || out != 7 || body.reads.Load() != 0 {
			t.Fatalf("no-body status decode %d, %v, reads %d", out, err, body.reads.Load())
		}
	})
}
