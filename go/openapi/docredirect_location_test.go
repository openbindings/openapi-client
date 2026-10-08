package openapi_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// A redirect whose Location url.Parse rejects ends the retrieval of the
// document, the entry or a referenced one, and the error does not show that
// Location's userinfo or query. errors.go, ErrUnresolved: "Where the client
// names a URI, a reference or a redirect's Location included, it omits
// userinfo and query. A retrieval error that is or wraps a *url.Error is shown
// as that *url.Error, each URL without userinfo or query"; doc.go, Outcomes:
// "an error from retrieving a document, the entry document included, that is
// or wraps a *url.Error is shown as that *url.Error, each URL without
// userinfo or query". Each status net/http
// follows is tried, with the Location on the first response and after a hop
// that is followed, absolute with userinfo and a port url.Parse rejects, and
// relative with an invalid escape.
func TestDocumentUnparsableRedirectLocation(t *testing.T) {
	const user, pass, query = "u53r", "s3cr3t-pw", "q5ecr3t"
	for name, loc := range map[string]string{
		"absolute": "http://" + user + ":" + pass + "@127.0.0.1:bad/x.json?token=" + query,
		"relative": "/x%zz.json?token=" + query,
	} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("%s %d", name, status), func(t *testing.T) {
				s := newSite(t)
				s.handle("/bad.json", redirect(status, loc))
				s.handle("/hop.json", redirect(status, "/bad.json"))
				s.put("/good.json", `{"P":{"name":"g","in":"query"}}`)
				s.put("/main.json", bare31(paramOps(map[string]string{"bad": "bad.json#/P", "hop": "hop.json#/P", "good": "good.json#/P"})))
				hidden := func(what string, err error) {
					t.Helper()
					if text := err.Error(); strings.Contains(text, user) || strings.Contains(text, pass) || strings.Contains(text, query) {
						t.Errorf("%s: error %q shows the Location's userinfo or query", what, text)
					}
				}

				for _, path := range []string{"/bad.json", "/hop.json"} {
					c, err := openapi.Load(t.Context(), s.uri(path), nil)
					if err == nil || c != nil {
						t.Fatalf("Load(%s) = %v, %v; want an error", path, c, err)
					}
					hidden("Load "+path, err)
				}

				c := mustLoad(t, nil, s.uri("/main.json"), nil)
				admittedRef(t, c, "good")
				for _, key := range []string{"bad", "hop"} {
					op := mustOp(t, c, key)
					if !errors.Is(op.Err, openapi.ErrUnresolved) {
						t.Errorf("%s Err = %v, want unresolved", key, op.Err)
						continue
					}
					hidden(key, op.Err)
				}
				if n := s.count("/bad.json"); n != 4 {
					t.Errorf("bad.json requested %d times, want 4: twice directly and twice by the hop followed", n)
				}
			})
		}
	}
}
