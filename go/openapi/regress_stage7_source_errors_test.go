package openapi_test

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// A response source error stays terminal regardless of its error chain.
// This reader records any attempt to read again after reporting that error.
type stream7ReviewSource struct {
	data   string
	err    error
	failed bool
	after  int
}

func (r *stream7ReviewSource) Read(p []byte) (int, error) {
	if r.failed {
		r.after++
		return 0, r.err
	}
	if r.data != "" {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	r.failed = true
	return 0, r.err
}

type stream7ReviewSourceError struct{ text string }

func (e *stream7ReviewSourceError) Error() string { return e.text }

func TestStream7ReviewReadErrorOriginIsTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, ct, completed, partial string
		events                       bool
	}{
		{"JSONL", "application/jsonl", "1\n", "partial", false},
		{"JSONseq", "application/json-seq", "\x1e1\n", "\x1epartial", false},
		{"SSE Items", "text/event-stream", "data:first\n\n", "data:partial", false},
		{"Events", "text/event-stream", "data:first\n\n", "data:partial", true},
		{"multipart", "multipart/mixed; boundary=B", "--B\r\nContent-Type: text/plain\r\n\r\nfirst\r\n", "--B\r\nContent-Type: text/plain\r\n\r\npartial", false},
	} {
		for _, prior := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prior=%t", tc.name, prior), func(t *testing.T) {
				sourceErr := &stream7ReviewSourceError{text: "caller source error synthetic-private-token"}
				wireErr := errors.Join(sourceErr, openapi.ErrItem)
				data := tc.partial
				if prior {
					data = tc.completed + data
				}
				source := &stream7ReviewSource{data: data, err: wireErr}
				body := &stream7Body{reader: source}
				c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) { return stream7HTTP(r, 200, tc.ct, body), nil }), nil)
				r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer r.Body.Close()
				var errs []error
				if tc.events {
					for _, err := range openapi.Events(r) {
						errs = append(errs, err)
						if len(errs) == 3 {
							break
						}
					}
				} else {
					for _, err := range openapi.Items[any](r) {
						errs = append(errs, err)
						if len(errs) == 3 {
							break
						}
					}
				}
				want := 1
				if prior {
					want++
				}
				if len(errs) != want {
					t.Fatalf("yields %d want %d: %v", len(errs), want, errs)
				}
				if prior && errs[0] != nil {
					t.Fatalf("completed item lost: %v", errs)
				}
				last := errs[len(errs)-1]
				var typed *stream7ReviewSourceError
				if !errors.Is(last, wireErr) || !errors.Is(last, sourceErr) || !errors.Is(last, openapi.ErrItem) || !errors.As(last, &typed) || typed != sourceErr || !strings.Contains(last.Error(), sourceErr.Error()) {
					t.Fatalf("caller error identity/text changed: %v", last)
				}
				if source.after != 0 {
					t.Fatalf("read %d times after source error", source.after)
				}
				if body.closed.Load() == 0 {
					t.Fatal("body not closed at terminal error")
				}
			})
		}
	}
}

// doc.go's secrecy rule covers client-generated diagnostics for server
// response data; caller source errors retain their text (tested above).
func TestStream7ReviewMIMEDiagnosticsHideReflectedCredentials(t *testing.T) {
	const secret = "synthetic-stage7-reflected-secret"
	const doc = `{"openapi":"3.1.2","components":{"securitySchemes":{"token":{"type":"http","scheme":"bearer"}}},"security":[{"token":[]}],"paths":{"/x":{"get":{"operationId":"get","responses":{"200":{"description":"ok"}}}}}}`
	for _, kind := range []string{"malformed header", "raw malformed header", "coding", "charset", "media"} {
		t.Run(kind, func(t *testing.T) {
			var wire string
			var body *stream7Body
			c := editionClient(t, doc, &openapi.Options{BaseURL: "https://stream.example.test", Credentials: map[string]openapi.Credential{"token": openapi.Secret(secret)}, HTTPClient: &http.Client{Transport: stream7RT(func(r *http.Request) (*http.Response, error) {
				reflected := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if reflected != secret {
					t.Fatal("credential was not sent")
				}
				header := reflected
				switch kind {
				case "coding":
					header = "Content-Type: application/json\r\nContent-Encoding: " + reflected
				case "charset":
					header = "Content-Type: application/xml; charset=" + reflected
				case "media":
					header = "Content-Type: application/" + reflected
				}
				wire = stream7Multipart(header+"\r\n\r\n1", "Content-Type: application/json\r\n\r\n7")
				body = &stream7Body{reader: strings.NewReader(wire)}
				return stream7HTTP(r, 200, "multipart/mixed; boundary=B", body), nil
			})}})
			r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			var errs []error
			var values []int
			if kind == "raw malformed header" {
				for _, err := range openapi.Items[*multipart.Part](r) {
					errs = append(errs, err)
				}
			} else {
				values, errs = stream7Collect[int](r)
			}
			malformed := strings.Contains(kind, "malformed")
			want := 2
			if malformed {
				want = 1
			}
			if len(errs) != want || errs[0] == nil {
				t.Fatalf("errors %v", errs)
			}
			if strings.Contains(errs[0].Error(), secret) {
				t.Fatalf("generated diagnostic quotes reflected credential: %v", errs[0])
			}
			if errors.Is(errs[0], openapi.ErrItem) == malformed {
				t.Fatalf("wrong terminal/item classification: %v", errs[0])
			}
			if malformed {
				_, oracleErr := multipart.NewReader(strings.NewReader(wire), "B").NextPart()
				var wantCause, gotCause textproto.ProtocolError
				if !errors.As(oracleErr, &wantCause) || !errors.As(errs[0], &gotCause) || gotCause != wantCause || !errors.Is(errs[0], wantCause) {
					t.Fatalf("MIME parse cause lost: %v (oracle %v)", errs[0], oracleErr)
				}
			} else if errs[1] != nil || values[1] != 7 {
				t.Fatalf("next part did not recover: %v %v", values, errs)
			}
			if body.closed.Load() == 0 {
				t.Fatal("body not closed")
			}
		})
	}
}

// Caller errors at the MIME-header parser boundary retain their identity
// and full text as well, including a caller-created ProtocolError.
func TestStream7ReviewMIMECallerProtocolErrorPreserved(t *testing.T) {
	sourceErr := textproto.ProtocolError("caller protocol failure synthetic-private-token")
	body := &stream7Body{reader: &stream7ReviewSource{err: sourceErr}}
	c := stream7Client(t, "3.1.2", stream7RT(func(r *http.Request) (*http.Response, error) {
		return stream7HTTP(r, 200, "multipart/mixed; boundary=B", body), nil
	}), nil)
	r, err := mustPrepare(t, c, "get", nil).Send(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	_, errs := stream7Collect[any](r)
	var got textproto.ProtocolError
	if len(errs) != 1 || !errors.Is(errs[0], sourceErr) || !errors.As(errs[0], &got) || got != sourceErr || !strings.Contains(errs[0].Error(), string(sourceErr)) || errors.Is(errs[0], io.EOF) {
		t.Fatalf("caller protocol error changed: %v", errs)
	}
}
