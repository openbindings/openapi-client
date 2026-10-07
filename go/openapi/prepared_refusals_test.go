package openapi_test

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Sources for these tests are the package documentation. Prepare
// (client.go), RequestError (errors.go), and Configuration when the document
// is incomplete (doc.go) require actionable, independently detectable
// refusals without electing an alternative. Example_whatIsMissing is the
// runnable flow below. A part's content type is only determined after
// choosing the governing body media.
func TestPrepareNamesMissingSettingsThenSends(t *testing.T) {
	w := newWire(t, typedAnswer(http.StatusCreated, "text/plain", "created"))
	c, err := openapi.Parse(t.Context(), reportsYAML, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	op := mustOp(t, c, "uploadReport")
	file := &streamBody{reader: strings.NewReader("report bytes")}
	in := &openapi.Input{Body: map[string]any{
		"title": "Q3", "file": openapi.Part{Content: file},
	}}
	req, err := c.Prepare(op.Key, in)
	if req != nil {
		t.Fatal("Prepare returned a request before its settings were supplied")
	}
	re := asRequestError(t, err)
	wantKeys(t, "Settings", re.Settings, false, "Options.BaseURL", "Options.Security", "Input.MediaType")
	for _, name := range []string{"Options.SecurityKey", "Input.Security"} {
		if !strings.Contains(re.Error(), name) {
			t.Errorf("security refusal does not name %s: %v", name, re)
		}
	}
	w.nothingSent(t)
	if file.reads.Load() != 0 || file.closed.Load() != 0 {
		t.Fatal("refused Prepare consumed or closed the caller's reader")
	}

	// Choose the intended alternatives from the descriptions, as a caller
	// presenting a configuration form would. Their order is not a policy.
	var schemeName, fieldName, partType string
	var media *openapi.Media
	for _, alternative := range op.Security {
		if len(alternative.Schemes) == 1 && alternative.Schemes[0].Type == "apiKey" {
			in.Security = alternative.Key
			schemeName = alternative.Schemes[0].Name
			fieldName = alternative.Schemes[0].ParamName
		}
	}
	for _, offered := range op.Body.Media {
		if offered.Type == "multipart/form-data" {
			media = offered
			in.MediaType = offered.Type
			for _, encoding := range offered.Encoding {
				if encoding.Name == "file" {
					for _, offeredType := range strings.Split(encoding.ContentType, ",") {
						if strings.TrimSpace(offeredType) == "application/pdf" {
							partType = strings.TrimSpace(offeredType)
						}
					}
				}
			}
		}
	}
	if schemeName == "" || media == nil || partType == "" {
		t.Fatal("descriptions do not offer the intended security, body, and part types")
	}
	c = c.With(func(o *openapi.Options) { o.BaseURL = w.URL + "/v1" })
	req, err = c.Prepare(op.Key, in)
	if req != nil {
		t.Fatal("Prepare ignored the selected credential and part requirements")
	}
	re = asRequestError(t, err)
	wantKeys(t, "selected Settings", re.Settings, true, credKey(schemeName), "Input.Body/file")
	wantKeys(t, "selected Inputs", re.Inputs, true)

	var credentialCalls atomic.Int32
	c = c.With(func(o *openapi.Options) {
		o.Credentials[schemeName] = openapi.SecretFunc(func(context.Context) (string, error) {
			credentialCalls.Add(1)
			return "reports-secret", nil
		})
	})
	in.Body.(map[string]any)["file"] = openapi.Part{Content: file, MediaType: partType}
	req = mustPrepare(t, c, op.Key, in)
	if req.Media != media || req.Security != in.Security || req.HTTP.URL.String() != w.URL+"/v1/reports" {
		t.Fatalf("prepared metadata or destination: %+v", req)
	}
	if credentialCalls.Load() != 0 || file.reads.Load() != 0 || file.closed.Load() != 0 || req.HTTP.Header.Get(fieldName) != "" {
		t.Fatal("Prepare used credentials or consumed the caller's reader")
	}
	w.nothingSent(t)
	resp, err := req.Send(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || resp.Security != in.Security {
		t.Fatalf("response status/security: %d %q", resp.StatusCode, resp.Security)
	}
	if err := resp.WaitRequest(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := w.only(t)
	if got.Header.Get(fieldName) != "reports-secret" || credentialCalls.Load() != 1 {
		t.Fatal("Send did not apply the selected credential once")
	}
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	if len(parts) != 2 {
		t.Fatalf("sent %d parts, want 2", len(parts))
	}
	var foundFile, foundTitle bool
	for _, p := range parts {
		_, disposition, err := mime.ParseMediaType(p.header.Get("Content-Disposition"))
		if err != nil {
			t.Fatal(err)
		}
		if disposition["name"] == "file" && p.header.Get("Content-Type") == partType && string(p.body) == "report bytes" {
			foundFile = true
		}
		if disposition["name"] == "title" && string(p.body) == "Q3" {
			foundTitle = true
		}
	}
	if !foundFile || !foundTitle || file.closed.Load() != 0 {
		t.Fatalf("repaired call lost a part or closed the caller's source: file=%t title=%t closed=%d", foundFile, foundTitle, file.closed.Load())
	}
}

// RequestError.Settings states exact keys for the three server selector
// conflicts. Options.Server/ServerID/BaseURL say how clearing the named
// conflicting field makes the remaining selection usable. Older
// TestLoadRefusesOptions checks refusal but intentionally no conflict key.
func TestServerSelectorConflictKeys(t *testing.T) {
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://a.example.test"},{"url":"https://b.example.test"}],"paths":{"/x":{"get":{"operationId":"get"}}}}`
	c := parseAt(t, doc, "", testDocURI, nil)
	op := mustOp(t, c, "get")
	a, b := op.Servers[0], op.Servers[1]
	for _, tc := range []struct {
		name, key, want string
		set, fix        func(*openapi.Options)
	}{
		{"BaseURL with Server", "Options.BaseURL", b.URL + "/x", func(o *openapi.Options) { o.BaseURL, o.Server = a.URL, b.URL }, func(o *openapi.Options) { o.BaseURL = "" }},
		{"BaseURL with ServerID", "Options.BaseURL", b.URL + "/x", func(o *openapi.Options) { o.BaseURL, o.ServerID = a.URL, b.ID }, func(o *openapi.Options) { o.BaseURL = "" }},
		{"Server with ServerID", "Options.ServerID", a.URL + "/x", func(o *openapi.Options) { o.Server, o.ServerID = a.URL, b.ID }, func(o *openapi.Options) { o.ServerID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := new(openapi.Options)
			tc.set(opts)
			loaded, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, opts)
			if loaded != nil {
				t.Fatal("Parse accepted conflicting settings")
			}
			wantKeys(t, "load Settings", asRequestError(t, err).Settings, true, tc.key)
			d := c.With(tc.set)
			req, err := d.Prepare(op.Key, nil)
			if req != nil {
				t.Fatal("Prepare accepted conflicting settings")
			}
			wantKeys(t, "Prepare Settings", asRequestError(t, err).Settings, true, tc.key)
			req = mustPrepare(t, d.With(tc.fix), op.Key, nil)
			if req.HTTP.URL.String() != tc.want {
				t.Fatalf("corrected request %s, want %s", req.HTTP.URL, tc.want)
			}
		})
	}
}

// Prepare, Input.Params, RequestError.Settings/Inputs, and Error/Unwrap:
// failures of already-selected configuration and independent parameter/part
// serialization must compose, retain exact keys, and omit input values. RFC
// 6901 section 3 gives ~0/~1.
func TestPrepareIndependentRefusalsCompose(t *testing.T) {
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test/{tenant}","variables":{"tenant":{"default":"allowed","enum":["allowed"]}}}],"paths":{"/x/{id}":{"post":{"operationId":"post","security":[{"key":[]}],"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}},{"name":"Input.Body","in":"query","required":true,"schema":{"type":"string"}},{"name":"X-Trace","in":"header","schema":{"type":"string"}}],"requestBody":{"content":{"multipart/form-data":{"encoding":{"file/name~":{"contentType":"text/plain"}}}}}}}},"components":{"securitySchemes":{"key":{"type":"apiKey","in":"header","name":"X-Key"}}}}`
	var dispatches atomic.Int32
	c := parseAt(t, doc, "", testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: streamRT(func(r *http.Request) (*http.Response, error) {
		dispatches.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		r.Body.Close()
		if r.URL.Path != "/allowed/x/fixed" || r.URL.Query().Get("Input.Body") != "fixed-query" || r.Header.Get("X-Key") != "fixed-key" || r.Header.Get("X-Trace") != "fixed-header" {
			t.Errorf("corrected request values did not reach the transport")
		}
		return streamHTTP(r, 204, "", http.NoBody), nil
	})}})
	c = c.With(func(o *openapi.Options) {
		o.Variables["tenant"] = "private-tenant"
		o.Header.Set("X-Trace", "private-setting")
	})
	in := &openapi.Input{Params: map[string]any{"X-Trace": "private-header", "typo": "private-parameter"}, Body: map[string]any{
		"file/name~": openapi.Part{Content: "private-content", Filename: "private-filename", NoFilename: true},
	}}
	req, err := c.Prepare("post", in)
	if req != nil {
		t.Fatal("Prepare accepted independent invalid fields")
	}
	re := asRequestError(t, err)
	wantKeys(t, "Settings", re.Settings, true, `Options.Variables["tenant"]`, `Options.Credentials["key"]`, "Options.Header")
	wantKeys(t, "Inputs", re.Inputs, true, "id", "query.Input.Body", "typo", "Input.Body/file~1name~0")
	for _, secret := range []string{"private-tenant", "private-setting", "private-header", "private-parameter", "private-content", "private-filename"} {
		noSecrets(t, err, secret)
	}
	for _, problems := range []map[string]error{re.Settings, re.Inputs} {
		for key, problem := range problems {
			if !strings.Contains(re.Error(), key) {
				t.Errorf("diagnostic does not identify %q: %v", key, re)
			}
			if !errors.Is(re, problem) {
				t.Errorf("RequestError does not unwrap %q's problem", key)
			}
		}
	}
	if dispatches.Load() != 0 {
		t.Fatal("Prepare dispatched a refused call")
	}
	c = c.With(func(o *openapi.Options) {
		o.Variables["tenant"] = "allowed"
		o.Credentials["key"] = openapi.Secret("fixed-key")
		o.Header.Del("X-Trace")
	})
	in.Params = map[string]any{"id": "fixed", "query.Input.Body": "fixed-query", "X-Trace": "fixed-header"}
	in.Body.(map[string]any)["file/name~"] = openapi.Part{Content: "fixed-content", NoFilename: true}
	req = mustPrepare(t, c, "post", in)
	resp, err := req.Send(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := resp.WaitRequest(t.Context()); err != nil || dispatches.Load() != 1 {
		t.Fatalf("corrected send: dispatches %d, upload %v", dispatches.Load(), err)
	}
}
