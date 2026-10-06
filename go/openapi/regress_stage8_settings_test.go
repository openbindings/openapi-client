package openapi_test

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Options.MediaType's per-call override (Options.MediaType:
// "Input.MediaType overrides it for one call") and With's affected-call
// rule (client.go, With: "any other Options the document cannot use refuse
// each call they affect"). A replaced default does not participate in media
// selection.
func TestReview8MediaTypeOverride(t *testing.T) {
	c := parseAt(t, doc31(`"/x":{"post":{"operationId":"post","requestBody":{"content":{"application/json":{}}}}}`), "https://api.example.test", testDocURI, nil)
	for _, kind := range []string{"raw", "structured"} {
		for _, defaultType := range []string{"not a media type", "application/*"} {
			t.Run(kind+"/"+defaultType, func(t *testing.T) {
				d := c.With(func(o *openapi.Options) { o.MediaType = defaultType })
				var body any = []byte(`{"n":7}`)
				if kind == "structured" {
					body = map[string]int{"n": 7}
				}
				in := &openapi.Input{Body: body, MediaType: "application/json"}
				req, err := d.Prepare("post", in)
				if err != nil {
					t.Errorf("valid per-call media did not override %q: %v", defaultType, err)
				} else if req.Media != reqMedia(t, mustOp(t, c, "post"), 0) || req.HTTP.Header.Get("Content-Type") != "application/json" {
					t.Error("overriding media lost declaration or concrete Content-Type")
				}
				in.MediaType = "also not a media type"
				_, err = d.Prepare("post", in)
				wantKeys(t, "bad per-call Settings", asRequestError(t, err).Settings, true, "Input.MediaType")
				in.MediaType = ""
				_, err = d.Prepare("post", in)
				wantKeys(t, "selected default Settings", asRequestError(t, err).Settings, true, "Options.MediaType")
			})
		}
	}
	// A malformed default remains irrelevant when no body is supplied.
	mustPrepare(t, c.With(func(o *openapi.Options) { o.MediaType = "application/*" }), "post", nil)
}

// Call's typed-output Accept requirement (client.go, Call: "a call whose
// request carries no Accept field is refused before sending, at Settings
// key "Options.Header""), HTTP field-name case insensitivity (RFC 9110
// section 5.1), and the caller's spelling of a field, which the client
// does not canonicalize. Direct edits and ParamWriters may add Accept under
// any spelling without normalizing their caller-owned maps.
func TestReview8EditedAcceptSpelling(t *testing.T) {
	doc := doc31(`"/x":{"get":{"operationId":"get","parameters":[{"name":"custom","in":"query","schema":{"type":"string"}}],"responses":{"200":{"description":"ok","content":{"application/json":{},"application/xml":{}}}}}}`)
	for _, edit := range []string{"HTTP", "ParamWriter"} {
		for _, spelling := range []string{"Accept", "accept", "aCcEpT", "absent"} {
			t.Run(edit+"/"+spelling, func(t *testing.T) {
				var dispatches int
				c := parseAt(t, doc, "https://api.example.test", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: stream7RT(func(r *http.Request) (*http.Response, error) {
					dispatches++
					if !reflect.DeepEqual(r.Header[spelling], []string{"application/json"}) {
						t.Errorf("caller map spelling/values changed: %#v", r.Header)
					}
					for name := range r.Header {
						if strings.EqualFold(name, "Accept") && name != spelling {
							t.Errorf("extra Accept spelling %q", name)
						}
					}
					return stream7HTTP(r, 200, "application/json", io.NopCloser(strings.NewReader(`{"n":7}`))), nil
				})}})
				write := func(r *http.Request) error {
					if spelling != "absent" {
						r.Header[spelling] = []string{"application/json"}
					}
					return nil
				}
				in := new(openapi.Input)
				if edit == "ParamWriter" {
					in.ParamWriters = map[string]func(*http.Request) error{"custom": write}
				}
				var out struct{ N int }
				var err error
				if edit == "HTTP" {
					req := mustPrepare(t, c, "get", in)
					write(req.HTTP)
					_, err = req.Call(t.Context(), &out)
				} else {
					_, err = c.Call(t.Context(), "get", in, &out)
				}
				if spelling == "absent" {
					wantKeys(t, "Settings", asRequestError(t, err).Settings, true, "Options.Header")
					if dispatches != 0 {
						t.Error("missing Accept dispatched")
					}
				} else if err != nil || dispatches != 1 || out.N != 7 {
					t.Errorf("explicit %s refused: %v, dispatches %d, out %+v", spelling, err, dispatches, out)
				}
			})
		}
	}
}
