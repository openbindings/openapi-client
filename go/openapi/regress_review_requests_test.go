package openapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// RFC 6570 reserved expansion preserves percent triplets; unrelated raw
// query/fragment delimiters in a path cannot discard those triplets.
func TestReviewRepairReservedPath(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.0", `"/x/{p}":{"get":{"parameters":[{"name":"p","in":"path","required":true,"allowReserved":true,"schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}`), &openapi.Options{BaseURL: "https://example.test"})
	for _, suffix := range []string{"?c", "#", " c", "[c]", "é"} {
		r := mustPrepare(t, c, "GET /x/{p}", &openapi.Input{Params: map[string]any{"p": "a%2Fb" + suffix}})
		if !strings.HasPrefix(r.HTTP.URL.EscapedPath(), "/x/a%2Fb") {
			t.Errorf("%q became %s", suffix, r.HTTP.URL)
		}
	}
}

// Prepared Send honors caller edits and lets transports check the body
// against ContentLength. Reaching that length must not hide excess bytes.
func TestReviewRepairPreparedLength(t *testing.T) {
	for _, data := range []string{"abc", "abcde", "abcdefghijklmnop"} {
		for _, readAll := range []bool{false, true} {
			c := stream7Client(t, "3.2.0", stream7RT(func(r *http.Request) (*http.Response, error) {
				if readAll {
					io.Copy(io.Discard, r.Body)
				} else {
					io.CopyN(io.Discard, r.Body, 5)
				}
				r.Body.Close()
				return stream7HTTP(r, 200, "", http.NoBody), nil
			}), nil)
			r := mustPrepare(t, c, "post", nil)
			r.HTTP.Body = io.NopCloser(strings.NewReader(data))
			r.HTTP.GetBody = nil
			r.HTTP.ContentLength = 5
			resp, err := r.Send(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = resp.WaitRequest(t.Context())
			resp.Body.Close()
			bad := len(data) < 5 || readAll && len(data) > 5
			if (err != nil) != bad {
				t.Errorf("data %q readAll=%v: %v", data, readAll, err)
			}
		}
	}
}

// Redirects rebuild generated body metadata while removing caller header
// edits after crossing origin, even edits to Content-Type itself.
func TestReviewRepairRedirectContentType(t *testing.T) {
	calls := 0
	doc := editionDoc("3.1.2", `"/p":{"post":{"requestBody":{"content":{"application/json":{}}},"responses":{"200":{"description":"ok"}}}}`)
	c := editionClient(t, doc, &openapi.Options{BaseURL: "https://first.example.test", Redirects: openapi.FollowAll, HTTPClient: &http.Client{Transport: stream7RT(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"a":1}` {
			t.Errorf("body %q", body)
		}
		if calls == 1 {
			resp := stream7HTTP(r, 307, "", http.NoBody)
			resp.Header.Set("Location", "https://second.example.test/p")
			return resp, nil
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("redirect Content-Type=%q", got)
		}
		return stream7HTTP(r, 200, "", http.NoBody), nil
	})}})
	r := mustPrepare(t, c, "POST /p", &openapi.Input{Body: map[string]any{"a": 1}})
	r.HTTP.Header.Set("Content-Type", "application/json; secret=caller-value")
	resp, err := r.Send(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}
