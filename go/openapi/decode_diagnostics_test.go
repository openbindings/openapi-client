package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

const diagnosticSecret = "synthetic-request-secret"

func securedResponseClient(t *testing.T, answer func(*http.Request) (*http.Response, error), edit func(*openapi.Options)) *openapi.Client {
	t.Helper()
	doc := doc31(`"/x":{"get":{"operationId":"get","security":[{"key":[]}],"responses":{"default":{"description":"result"}}}}`,
		`"components":{"securitySchemes":{"key":{"type":"apiKey","in":"header","name":"X-Key"}}}`)
	o := &openapi.Options{Credentials: map[string]openapi.Credential{"key": openapi.Secret(diagnosticSecret)}, HTTPClient: &http.Client{Transport: streamRT(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Key") != diagnosticSecret {
			t.Error("the synthetic credential did not reach the transport")
		}
		return answer(r)
	})}}
	if edit != nil {
		edit(o)
	}
	return parseAt(t, doc, "https://api.example.test", testDocURI, o)
}

// doc.go Outcomes' no-credential-in-generated-text rule,
// Call/Response.Decode/StatusError.Decode, and DecodeError.Error, which
// returns "never the body or response-controlled media and encoding text,
// which may reflect credentials". Response-controlled type, subtype,
// parameters, coding and charset remain explicit metadata, not generated
// diagnostics. Each reflected value comes from a placed key.
func TestDecodeErrorOmitsReflectedMetadata(t *testing.T) {
	for _, field := range []string{"type", "subtype", "parameter", "malformed Content-Type", "coding", "charset", "invalid target"} {
		for _, mode := range []string{"Send Decode", "Call", "StatusError Decode"} {
			if field == "invalid target" && mode == "Call" {
				continue // Call refuses before any response metadata exists.
			}
			t.Run(field+"/"+mode, func(t *testing.T) {
				status := 422
				if mode == "Call" {
					status = 200
				}
				var wantHeader http.Header
				var payload string
				var body *streamBody
				c := securedResponseClient(t, func(r *http.Request) (*http.Response, error) {
					reflected := r.Header.Get("X-Key")
					wantHeader = http.Header{"Content-Type": {"application/json"}}
					payload = `{"n":"` + reflected + `"` // incomplete JSON
					switch field {
					case "type":
						wantHeader.Set("Content-Type", reflected+"/octets")
					case "subtype":
						wantHeader.Set("Content-Type", "application/"+reflected+"+json")
					case "parameter", "invalid target":
						wantHeader.Set("Content-Type", "application/json; note="+reflected)
					case "malformed Content-Type":
						wantHeader.Set("Content-Type", `application/json; note="`+reflected)
					case "coding":
						wantHeader.Set("Content-Encoding", reflected)
					case "charset":
						wantHeader.Set("Content-Type", "application/xml; charset="+reflected)
						payload = "<n>" + reflected + "</n>"
					}
					body = &streamBody{reader: strings.NewReader(payload)}
					resp := streamHTTP(r, status, "", body)
					resp.Header = wantHeader.Clone()
					return resp, nil
				}, nil)
				var out any = new(struct{ N string })
				if field == "invalid target" {
					out = 42
				}
				var r *openapi.Response
				var err error
				var se *openapi.StatusError
				switch mode {
				case "Call":
					r, err = c.Call(t.Context(), "get", nil, out)
				case "Send Decode":
					r, err = mustPrepare(t, c, "get", nil).Send(t.Context())
					if err == nil {
						err = r.Decode(out)
					}
				case "StatusError Decode":
					r, err = c.Call(t.Context(), "get", nil, nil)
					if !errors.As(err, &se) || se.Err != nil || string(se.Content) != payload {
						t.Fatalf("status capture: %v", err)
					}
					noSecrets(t, se, diagnosticSecret)
					err = se.Decode(out)
				}
				if r == nil {
					t.Fatalf("lost response: %v", err)
				}
				defer r.Body.Close()
				var de *openapi.DecodeError
				if !errors.As(err, &de) {
					t.Fatalf("decode failure class: %v", err)
				}
				noSecrets(t, err, diagnosticSecret)
				for name, values := range wantHeader {
					if r.Header.Get(name) != values[0] || de.Header.Get(name) != values[0] {
						t.Errorf("diagnostic sanitization changed explicit %s metadata", name)
					}
				}
				if field == "invalid target" {
					if len(de.Content) != 0 {
						t.Errorf("invalid-out error captured body %q", de.Content)
					}
					if mode == "Send Decode" && (body.reads.Load() != 0 || body.closed.Load() != 0) {
						t.Error("invalid-out diagnostic consumed response")
					}
				} else {
					// A coding refusal may precede a body read. Preserve the
					// actual captured prefix, without forcing a diagnostic read.
					if len(de.Content) > 4096 || !strings.HasPrefix(payload, string(de.Content)) {
						t.Errorf("Content is not a bounded captured prefix: %q", de.Content)
					}
					captured, e := io.ReadAll(de.Body)
					de.Body.Close()
					if e != nil || !bytes.Equal(captured, de.Content) {
						t.Errorf("error Body = %q, %v; captured Content %q", captured, e, de.Content)
					}
				}
				if se != nil && string(se.Content) != payload {
					t.Error("decoding altered explicit StatusError.Content")
				}
				if field == "subtype" || field == "parameter" {
					var syntax *json.SyntaxError
					if !errors.As(err, &syntax) {
						t.Errorf("JSON syntax cause was lost: %v", err)
					}
				}
			})
		}
	}
}

// Generated error text omits arbitrary server-controlled text, not merely
// strings matching retained credentials. No credential is configured in this
// control.
func TestDecodeErrorOmitsServerMediaType(t *testing.T) {
	const serverValue = "peer-controlled-private-type"
	c := streamClient(t, "3.1.2", streamRT(func(r *http.Request) (*http.Response, error) {
		return streamHTTP(r, 200, "application/"+serverValue, io.NopCloser(strings.NewReader("payload"))), nil
	}), nil)
	_, err := c.Call(t.Context(), "get", nil, new(struct{ N int }))
	var de *openapi.DecodeError
	if !errors.As(err, &de) {
		t.Fatal(err)
	}
	noSecrets(t, err, serverValue)
	if de.Header.Get("Content-Type") != "application/"+serverValue || string(de.Content) != "payload" {
		t.Fatal("explicit response metadata or captured content changed")
	}
}

type callerCauseError struct{ message string }

func (e *callerCauseError) Error() string { return e.message }

// doc.go Outcomes and DecodeError.Unwrap preserve caller errors; codec
// messages remain absent from generated text, while caller reader errors
// remain unchanged. These controls guard against blanket redaction of causes.
func TestDecodeErrorKeepsCallerCauses(t *testing.T) {
	for _, kind := range []string{"reader", "codec"} {
		for _, mode := range []string{"Send Decode", "Call", "StatusError Decode"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				cause := &callerCauseError{message: "caller diagnostic " + diagnosticSecret}
				status := 422
				if mode == "Call" {
					status = 200
				}
				c := securedResponseClient(t, func(r *http.Request) (*http.Response, error) {
					var reader io.Reader = strings.NewReader(`{}`)
					if kind == "reader" {
						reader = streamReadFunc(func([]byte) (int, error) { return 0, cause })
					}
					return streamHTTP(r, status, "application/json", io.NopCloser(reader)), nil
				}, func(o *openapi.Options) {
					if kind == "codec" {
						o.Codecs = map[string]openapi.Codec{"application/json": streamCodec{decode: func(io.Reader, any) error { return cause }}}
					}
				})
				var err error
				var out any
				switch mode {
				case "Call":
					_, err = c.Call(t.Context(), "get", nil, &out)
				case "Send Decode":
					r, e := mustPrepare(t, c, "get", nil).Send(t.Context())
					if e != nil {
						t.Fatal(e)
					}
					defer r.Body.Close()
					err = r.Decode(&out)
				case "StatusError Decode":
					_, e := c.Call(t.Context(), "get", nil, nil)
					var se *openapi.StatusError
					if !errors.As(e, &se) {
						t.Fatal(e)
					}
					err = se.Decode(&out)
				}
				var got *callerCauseError
				if !errors.Is(err, cause) || !errors.As(err, &got) || got != cause {
					t.Fatalf("caller error identity was lost: %v", err)
				}
				if kind == "reader" && !strings.Contains(err.Error(), cause.Error()) {
					t.Errorf("caller reader error text changed: %v", err)
				}
				if kind == "codec" {
					noSecrets(t, err, diagnosticSecret)
				}
			})
		}
	}
}
