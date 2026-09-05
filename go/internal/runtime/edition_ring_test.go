package openapiclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The four cells of the 2026-09-01 edition-gating round. Each was
// byte-observable, each was reached by no scenario, and two of the four were
// not edition-gated at all but simply absent. The table runs every accepted
// 3.x line so a rule stated identically by three documents cannot be
// re-implemented on one of them.

type editionRingTransport struct{ reqs []*http.Request }

func (t *editionRingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.reqs = append(t.reqs, r)
	return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

func editionRingDispatch(t *testing.T, doc string, in Input) (*http.Request, error) {
	t.Helper()
	tr := &editionRingTransport{}
	client, err := Load(context.Background(), Source{Content: []byte(doc)}, ClientOptions{HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		return nil, err
	}
	if _, callErr := client.Call(context.Background(), PathOperation("/p", GET), in); callErr != nil {
		return nil, callErr
	}
	return tr.reqs[0], nil
}

func editionRingEditions() []string { return []string{"3.0.0", "3.0.4", "3.1.0", "3.1.2", "3.2.0"} }

// Appendix C.4.2's pre-encoding set. All three 3.x documents state it
// identically: "`[`, `]`, `#`, `&`, `=`, and `+` are pre-percent-encoded where
// Appendix C requires". Gated to the 3.2 line, a supplied "a#b" left the
// engine as "a" -- the literal "#" made the rest of the URL a fragment, so the
// value was silently truncated on 3.0 and 3.1.
func TestEditionRingAllowReservedPreEncodesTheQueryStructuralBytes(t *testing.T) {
	for _, edition := range editionRingEditions() {
		t.Run(edition, func(t *testing.T) {
			doc := fmt.Sprintf(`{"openapi":%q,"info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],"paths":{"/p":{"get":{"operationId":"g","parameters":[{"name":"q","in":"query","allowReserved":true,"schema":{"type":"string"}}],"responses":{"204":{"description":"ok"}}}}}}`, edition)
			req, err := editionRingDispatch(t, doc, Input{Parameters: Parameters{Query: map[string]any{"q": "a#b[c]d&e=f+g"}}})
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			const want = "q=a%23b%5Bc%5Dd%26e%3Df%2Bg"
			if req.URL.RawQuery != want {
				t.Errorf("query = %q, want %q", req.URL.RawQuery, want)
			}
			if req.URL.Fragment != "" {
				t.Errorf("fragment = %q, want none: the value must not escape into the fragment", req.URL.Fragment)
			}
		})
	}
}

// The content-form lane's byte rule is its own pin and is unconditional:
// "Percent-encoding a content-form parameter leaves RFC 3986 unreserved bytes
// literal and encodes every other UTF-8 byte as uppercase `%HH`". Honoring
// `allowReserved` here -- a `schema`-path control -- leaked the whole reserved
// set on every edition.
func TestEditionRingContentFormIgnoresAllowReserved(t *testing.T) {
	for _, edition := range editionRingEditions() {
		t.Run(edition, func(t *testing.T) {
			doc := fmt.Sprintf(`{"openapi":%q,"info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],"paths":{"/p":{"get":{"operationId":"g","parameters":[{"name":"q","in":"query","allowReserved":true,"content":{"text/plain":{"schema":{"type":"string"}}}}],"responses":{"204":{"description":"ok"}}}}}}`, edition)
			req, err := editionRingDispatch(t, doc, Input{Parameters: Parameters{Query: map[string]any{"q": "a:/?@$,;b~c"}}})
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			// `~` is RFC 3986 unreserved and stays literal; every other byte
			// here is not, and rides as uppercase %HH.
			const want = "q=a%3A%2F%3F%40%24%2C%3Bb~c"
			if req.URL.RawQuery != want {
				t.Errorf("query = %q, want %q", req.URL.RawQuery, want)
			}
		})
	}
}

// "A declared cookie parameter serialized on the `schema` path is
// percent-encoded by ordinary RFC 6570 expansion" (openbindings.openapi-3.1@1
// §8.2, which states the rule and refutes the Appendix D reading the engines
// had adopted). Unencoded, a supplied ";" split one contribution into several
// on the wire.
func TestEditionRingFormStyleCookieParameterPercentEncodes(t *testing.T) {
	for _, edition := range editionRingEditions() {
		t.Run(edition, func(t *testing.T) {
			doc := fmt.Sprintf(`{"openapi":%q,"info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],"paths":{"/p":{"get":{"operationId":"g","parameters":[{"name":"q","in":"cookie","schema":{"type":"string"}}],"responses":{"204":{"description":"ok"}}}}}}`, edition)
			req, err := editionRingDispatch(t, doc, Input{Parameters: Parameters{Cookie: map[string]any{"q": "a;evil=1"}}})
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			const want = "q=a%3Bevil%3D1"
			if got := req.Header.Get("Cookie"); got != want {
				t.Errorf("Cookie = %q, want %q", got, want)
			}
		})
	}
}

// OAS 3.2's `style: cookie` is the one exemption, and it belongs to that
// style rather than to the edition: it "applies no percent-encoding or other
// escaping; values needing escaping MUST arrive already escaped", so the
// caller's value has to BE an RFC 6265 cookie-value. Applying that check to
// every 3.2 cookie parameter refused form-style values the edition serializes
// perfectly well.
func TestEditionRingCookieStyleIsThreeTwoOnlyAndChecksTheCallerValue(t *testing.T) {
	for _, edition := range editionRingEditions() {
		t.Run(edition, func(t *testing.T) {
			doc := fmt.Sprintf(`{"openapi":%q,"info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],"paths":{"/p":{"get":{"operationId":"g","parameters":[{"name":"q","in":"cookie","style":"cookie","schema":{"type":"string"}}],"responses":{"204":{"description":"ok"}}}}}}`, edition)
			req, err := editionRingDispatch(t, doc, Input{Parameters: Parameters{Cookie: map[string]any{"q": "plain"}}})
			if edition != "3.2.0" {
				if err == nil {
					t.Fatalf("style: cookie dispatched on %s, want a refusal: the style is 3.2's own", edition)
				}
				return
			}
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if got := req.Header.Get("Cookie"); got != "q=plain" {
				t.Errorf("Cookie = %q, want %q (the style preserves the exact value)", got, "q=plain")
			}
			if _, err := editionRingDispatch(t, doc, Input{Parameters: Parameters{Cookie: map[string]any{"q": "a;evil=1"}}}); err == nil {
				t.Error("style: cookie accepted a value that is not an RFC 6265 cookie-value, want a refusal")
			}
		})
	}
}

// "A completed target whose scheme is not `http` or `https` refuses before
// dispatch" (openbindings.openapi-3.0@1 §10, openbindings.openapi-3.2@1 §10;
// -3.1@1 §10 states the same restriction as a static exclusion). No engine
// implemented any of them: ftp://, file:// and ws:// all reached the
// transport, on every line.
func TestEditionRingNonHTTPSchemeRefusesBeforeDispatch(t *testing.T) {
	for _, edition := range editionRingEditions() {
		t.Run(edition, func(t *testing.T) {
			for _, scheme := range []string{"ftp", "file", "ws", "wss", "gopher"} {
				doc := fmt.Sprintf(`{"openapi":%q,"info":{"title":"t","version":"1"},"servers":[{"url":"%s://api.example"}],"paths":{"/p":{"get":{"operationId":"g","responses":{"204":{"description":"ok"}}}}}}`, edition, scheme)
				if req, err := editionRingDispatch(t, doc, Input{}); err == nil {
					t.Errorf("%s:// dispatched to %s, want a refusal before dispatch", scheme, req.URL)
				}
			}
			for _, scheme := range []string{"http", "https"} {
				doc := fmt.Sprintf(`{"openapi":%q,"info":{"title":"t","version":"1"},"servers":[{"url":"%s://api.example"}],"paths":{"/p":{"get":{"operationId":"g","responses":{"204":{"description":"ok"}}}}}}`, edition, scheme)
				if _, err := editionRingDispatch(t, doc, Input{}); err != nil {
					t.Errorf("%s:// refused: %v", scheme, err)
				}
			}
		})
	}
}
