package openapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// RFC 6265 4.1.1 requires token cookie names. 3.2 cookie style sends
// authored names and defined exploded member keys raw, so both must satisfy
// that boundary. Form style continues percent-encoding names instead.
func TestStage6RawCookieNameTokens(t *testing.T) {
	for _, name := range []string{"sid=other", "sid; other", "a b", "a/b", "a\r\nX-Field", "a\x00b", "é"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			nameJSON, _ := json.Marshal(name)
			decl := `{"name":` + string(nameJSON) + `,"in":"cookie","style":"cookie","schema":{}}`
			c := editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[`+decl+`]}}`), nil)
			p := param(t, mustOp(t, c, "GET /x"), 0)
			if p.Err == nil {
				t.Error("invalid authored raw cookie name lacks Param.Err")
			}
			_, err := c.Prepare("GET /x", &openapi.Input{Params: map[string]any{name: "value"}})
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, name)
			mustPrepare(t, c, "GET /x", nil)
			c = editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[{"name":"c","in":"cookie","style":"cookie","schema":{}}]}}`), nil)
			_, err = c.Prepare("GET /x", &openapi.Input{Params: map[string]any{"c": map[string]string{name: "value"}}})
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, "c")
			if got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"c": map[string]any{name: nil, "safe": "value"}}}).HTTP.Header.Get("Cookie"); got != "safe=value" {
				t.Errorf("undefined key not skipped: %q", got)
			}
			decl = `{"name":` + string(nameJSON) + `,"in":"cookie","style":"form","schema":{}}`
			c = editionClient(t, editionDoc("3.2.1", `"/x":{"get":{"parameters":[`+decl+`]}}`), nil)
			if got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{name: "value"}}).HTTP.Header.Get("Cookie"); got != pctName(name)+"=value" {
				t.Errorf("form name %q", got)
			}
		})
	}
}

// doc.go's whole-dot-segment refusal still applies when 3.2 reserved
// expansion emits slashes or retains encoded dots. Check assembled segments,
// not just the raw value or one isolated parameter's whole expansion.
func TestStage6ReservedPathDotSegments(t *testing.T) {
	for _, path := range []string{"/files/{p}", "/files/{p}/tail"} {
		t.Run(path, func(t *testing.T) {
			c := editionClient(t, editionDoc("3.2.1", fmt.Sprintf(`%q:{"get":{"parameters":[{"name":"p","in":"path","required":true,"allowReserved":true,"schema":{"type":"string"}}]}}`, path)), nil)
			for _, value := range []string{".", "..", "safe/../admin", "../admin", "safe/./admin", "%2e", "%2e%2E", ".%2e", "safe/%2e%2e/admin", "safe/%2E/admin", "safe/.."} {
				_, err := c.Prepare("GET "+path, &openapi.Input{Params: map[string]any{"p": value}})
				wantKeys(t, "Inputs", asRequestError(t, err).Inputs, false, "p")
			}
			for _, value := range []string{"safe/child", "...", ".name", "name..", "%252e%252e", "safe%2Fchild"} {
				mustPrepare(t, c, "GET "+path, &openapi.Input{Params: map[string]any{"p": value}})
			}
		})
	}
	for _, tc := range []struct{ path, p, q string }{
		{"/files/{p}{q}", "safe/.", "."},
		{"/files/{p}{q}", "safe/", "%2e%2e"},
		{"/files/.{p}", "%2e", ""},
		{"/files/{p}./tail", "%2e", ""},
	} {
		t.Run(tc.path+tc.p+tc.q, func(t *testing.T) {
			params := `{"name":"p","in":"path","required":true,"allowReserved":true,"schema":{"type":"string"}}`
			values := map[string]any{"p": tc.p}
			if strings.Contains(tc.path, "{q}") {
				params += `,{"name":"q","in":"path","required":true,"allowReserved":true,"schema":{"type":"string"}}`
				values["q"] = tc.q
			}
			c := editionClient(t, editionDoc("3.2.1", fmt.Sprintf(`%q:{"get":{"parameters":[%s]}}`, tc.path, params)), nil)
			_, err := c.Prepare("GET "+tc.path, &openapi.Input{Params: values})
			wantAnyKey(t, "Inputs", asRequestError(t, err).Inputs, "p", "q")
		})
	}
	// Existing non-reserved expansion encodes slashes as data, so it does
	// not create an internal URI segment even when decoded Path contains '/'.
	for _, version := range []string{"3.1.2", "3.2.1"} {
		c := editionClient(t, editionDoc(version, `"/files/{p}":{"get":{"parameters":[{"name":"p","in":"path","required":true,"schema":{"type":"string"}}]}}`), nil)
		for _, value := range []string{"safe/../admin", "../admin", "%2e%2e"} {
			req := mustPrepare(t, c, "GET /files/{p}", &openapi.Input{Params: map[string]any{"p": value}})
			if got := req.HTTP.URL.EscapedPath(); got != "/files/"+pctName(value) {
				t.Errorf("ordinary encoded path %q", got)
			}
		}
	}
}

// Generated error text never shows a URI's userinfo, and that applies to
// the labels of URI security names too (doc.go, Outcomes: "No credential
// appears in the text of an error the client creates"). Programmatic
// identity and Settings keys still use the exact authored name; caller
// errors are not rewritten.
func TestStage6URISecurityNameErrorPresentation(t *testing.T) {
	const name = "https://private-user:private-password@api.example.test/scheme.json#/Key"
	encoded, _ := json.Marshal(name)
	doc := editionDoc("3.2.1", `"/x":{"get":{"security":[{`+string(encoded)+`:[]}]}}`)
	fetched := 0
	l := openapi.Loader{Fetch: func(context.Context, string) (io.ReadCloser, string, error) {
		fetched++
		return nil, "", fmt.Errorf("unexpected fetch")
	}}
	c, err := l.Parse(t.Context(), []byte(doc), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := mustOp(t, c, "GET /x").Security[0].Schemes[0]
	if s.Name != name {
		t.Errorf("scheme identity %q", s.Name)
	}
	if s.Err == nil {
		t.Fatal("userinfo URI was not refused")
	}
	_, err = c.Prepare("GET /x", nil)
	re := asRequestError(t, err)
	key := fmt.Sprintf("Options.Credentials[%q]", name)
	wantKeys(t, "Settings", re.Settings, false, key)
	for _, text := range []string{err.Error(), s.Err.Error()} {
		if strings.Contains(text, "private-user") || strings.Contains(text, "private-password") {
			t.Errorf("userinfo in generated error %q", text)
		}
	}
	if fetched != 0 {
		t.Errorf("userinfo URI fetched %d times", fetched)
	}
	// Exact authored identity remains usable for the transport escape hatch.
	c, err = l.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{Credentials: map[string]openapi.Credential{name: openapi.FromTransport()}})
	if err != nil {
		t.Fatal(err)
	}
	mustPrepare(t, c, "GET /x", nil)
}
