package openapi_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests. The comment on each test names the contract line or
// specification section it checks.

// inChild runs the calling test in a child process of the test binary and
// fails it when the child fails or crashes, so that a crash (a fatal stack
// overflow cannot be recovered) fails only this test. It returns true in
// the child, where the test body runs.
func inChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv("OPENAPI_TEST_CHILD") == t.Name() {
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.timeout=2m")
	cmd.Env = append(os.Environ(), "OPENAPI_TEST_CHILD="+t.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 4000 {
			out = append(out[:2000:2000], append([]byte("\n...\n"), out[len(out)-2000:]...)...)
		}
		t.Fatalf("in a child process: %v\n%s", err, out)
	}
	return false
}

// noPanic runs f and reports a panic as a failure instead of ending the
// test binary.
func noPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s panicked: %v", what, r)
		}
	}()
	f()
}

// rawHTTP serves each connection by reading the request head, writing
// response verbatim, and closing the connection after linger.
func rawHTTP(t *testing.T, response string, linger time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
					return
				}
				io.WriteString(conn, response)
				time.Sleep(linger)
			}()
		}
	}()
	return "http://" + ln.Addr().String()
}

const jsonBodyOp = `"/x":{"post":{"operationId":"create","requestBody":{"content":{"application/json":{}}}}}`

type cycleNode struct {
	Name string     `json:"name"`
	Next *cycleNode `json:"next"`
}

// A cyclic Body is refused as json.Marshal refuses it, at
// Inputs["Input.Body"] (client.go, Options.Codecs: "An Encode error refuses
// the call at the body's ... Inputs key"), never a crash, and the encoding
// error keeps its cause for errors.As. Run in a child process: the failure
// mode is a fatal stack overflow.
func TestCyclicJSONBodyRefused(t *testing.T) {
	if !inChild(t) {
		return
	}
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(jsonBodyOp), nil)
	n := &cycleNode{Name: "a"}
	n.Next = n
	m := map[string]any{"a": 1}
	m["self"] = m
	for name, body := range map[string]any{"struct pointer": n, "map": m} {
		before := w.count()
		resp, err := c.Call(t.Context(), "create", &openapi.Input{Body: body}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, name+" Inputs", re.Inputs, true, "Input.Body")
		var uve *json.UnsupportedValueError
		if !errors.As(err, &uve) {
			t.Errorf("%s: errors.As finds no *json.UnsupportedValueError in %v", name, err)
		}
	}
}

// attachments is an unexported non-struct type; embedded, encoding/json
// ignores it.
type attachments map[string]io.Reader

type withAttachments struct {
	Name string `json:"name"`
	attachments
}

type fileID struct{ N int }

func (i fileID) MarshalText() ([]byte, error) { return []byte(fmt.Sprintf("id-%d", i.N)), nil }

type readerInner struct {
	File io.Reader `json:"file"`
}

// shadowed embeds a reader field that an outer field of the same JSON name
// dominates, so encoding/json never writes the reader.
type shadowed struct {
	readerInner
	File string `json:"file"`
}

// promoted embeds a reader field that encoding/json promotes.
type promoted struct {
	readerInner
	Name string `json:"name"`
}

type skipped struct {
	R    io.Reader `json:"-"`
	Name string    `json:"name"`
}

// The reader walk follows encoding/json's field rules. A reader
// encoding/json would not write is no refusal, and the body is sent as
// encoding/json writes it; a reader it would write is refused at its place,
// a TextMarshaler key named by its text (client.go, Input.Body: "A Part or
// io.Reader inside a JSON value is refused with an Inputs entry at its place
// in Body").
func TestReaderInJSONBodyFollowsEncodingJSONFields(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(jsonBodyOp), nil)
	sent := []struct {
		name string
		body any
		want string
	}{
		{"unexported embedded non-struct", withAttachments{Name: "a", attachments: attachments{"f": strings.NewReader("x")}}, `{"name":"a"}`},
		{"dominated embedded field", shadowed{readerInner{strings.NewReader("x")}, "kept"}, `{"file":"kept"}`},
		{"field tagged -", skipped{R: strings.NewReader("x"), Name: "a"}, `{"name":"a"}`},
	}
	for _, tt := range sent {
		noPanic(t, tt.name, func() {
			before := w.count()
			if _, err := c.Call(t.Context(), "create", &openapi.Input{Body: tt.body}, nil); err != nil {
				t.Errorf("%s: %v", tt.name, err)
				return
			}
			if w.count() != before+1 || string(trimNL(w.last(t).Body)) != tt.want {
				t.Errorf("%s: sent %q, want %s", tt.name, w.last(t).Body, tt.want)
			}
		})
	}
	refused := []struct {
		name string
		body any
		key  string
	}{
		{"TextMarshaler key", map[fileID]io.Reader{{7}: strings.NewReader("x")}, "Input.Body/id-7"},
		{"promoted embedded field", promoted{readerInner{strings.NewReader("x")}, "a"}, "Input.Body/file"},
	}
	for _, tt := range refused {
		noPanic(t, tt.name, func() {
			before := w.count()
			resp, err := c.Call(t.Context(), "create", &openapi.Input{Body: tt.body}, nil)
			re := asRequestError(t, err)
			if resp != nil || w.count() != before {
				t.Errorf("%s: a refused call was sent", tt.name)
			}
			wantKeys(t, tt.name+" Inputs", re.Inputs, true, tt.key)
		})
	}
}

// The Paths key's literal text is percent-encoded once, so the path is
// always a valid escaped path and a value's encoding survives (doc.go, Fixed
// rules, Percent-encoding; RFC 3986 section 3.3: pchar is unreserved,
// pct-encoded, sub-delims, ":" and "@"). A valid %XX triple in the key is
// kept; any other byte outside pchar is encoded, in uppercase hex (RFC 3986
// section 2.1).
func TestPathTemplateLiteralPercentEncoding(t *testing.T) {
	const value = "x/y;z"
	tests := []struct{ key, want string }{
		{"/files/{id}", "/files/x%2Fy%3Bz"},
		{"/café/{id}", "/caf%C3%A9/x%2Fy%3Bz"},
		{"/a b/{id}", "/a%20b/x%2Fy%3Bz"},
		{"/a|b/{id}", "/a%7Cb/x%2Fy%3Bz"},
		{"/a^b/{id}", "/a%5Eb/x%2Fy%3Bz"},
		{"/100%/{id}", "/100%25/x%2Fy%3Bz"},
		{"/a%20b/{id}", "/a%20b/x%2Fy%3Bz"},
		{"/x:y@z;w=v/{id}", "/x:y@z;w=v/x%2Fy%3Bz"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			w := newWire(t, nil)
			doc := doc31(`"` + tt.key + `":{"get":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}`)
			c := parseFor(t, w, doc, nil)
			req := mustPrepare(t, c, "op", &openapi.Input{Params: map[string]any{"id": value}})
			if got := req.HTTP.URL.EscapedPath(); got != tt.want {
				t.Errorf("EscapedPath %q, want %q", got, tt.want)
			}
			if want, _ := url.PathUnescape(tt.want); req.HTTP.URL.Path != want {
				t.Errorf("URL.Path %q, want %q", req.HTTP.URL.Path, want)
			}
			mustCall(t, c, "op", &openapi.Input{Params: map[string]any{"id": value}}, nil)
			if got := w.last(t).RequestURI; got != tt.want {
				t.Errorf("request target %q, want %q", got, tt.want)
			}
		})
	}

	// The same holds for a server URL's path.
	w := newWire(t, nil)
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@/café"}],
		"paths":{"/{id}":{"get":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}}}`
	mustCall(t, parseFor(t, w, doc, nil), "op", &openapi.Input{Params: map[string]any{"id": value}}, nil)
	if got := w.last(t).RequestURI; got != "/caf%C3%A9/x%2Fy%3Bz" {
		t.Errorf("server path: request target %q, want /caf%%C3%%A9/x%%2Fy%%3Bz", got)
	}
}

// Server usability is judged on the substituted values; only a defect
// independent of the values sets Server.Err (describe.go, Server.Err; OAS
// 3.1.2 section 4.8.5, whose own example uses {port}).
func TestServerUsabilityJudgedOnSubstitutedValues(t *testing.T) {
	w := newWire(t, nil)
	host, port, _ := net.SplitHostPort(w.hostport())

	t.Run("port variable", func(t *testing.T) {
		doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},
		"servers":[{"url":"http://` + host + `:{port}/v1","variables":{"port":{"default":"` + port + `","enum":["` + port + `","443"]}}}],
		"paths":{"/pets":{"get":{"operationId":"listPets"}}}}`
		c := parseFor(t, w, doc, nil)
		if s := server(t, mustOp(t, c, "listPets"), 0); s.Err != nil {
			t.Errorf("{port} server Err = %v", s.Err)
		}
		mustCall(t, c, "listPets", nil, nil)
		if got := w.last(t).RequestURI; got != "/v1/pets" {
			t.Errorf("request target %q", got)
		}
	})

	t.Run("whole URL variable from a file", func(t *testing.T) {
		endpoint := bare31(`"/a":{"get":{"operationId":"op"}}`, `"servers":[{"url":"{endpoint}","variables":{"endpoint":{"default":""}}}]`)
		path := filepath.Join(t.TempDir(), "doc.json")
		if err := os.WriteFile(path, []byte(endpoint), 0o600); err != nil {
			t.Fatal(err)
		}
		fc, err := openapi.Load(t.Context(), path, &openapi.Options{Variables: map[string]string{"endpoint": w.URL}})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s := server(t, mustOp(t, fc, "op"), 0); s.Err != nil {
			t.Errorf("{endpoint} server Err = %v", s.Err)
		}
		mustCall(t, fc, "op", nil, nil)
		if got := w.last(t).RequestURI; got != "/a" {
			t.Errorf("request target %q, want /a", got)
		}
	})

	t.Run("leading variable, no document URI", func(t *testing.T) {
		base := bare31(`"/a":{"get":{"operationId":"op"}}`, `"servers":[{"url":"{base}/v1","variables":{"base":{"default":"@BASE@"}}}]`)
		pc, err := openapi.Parse(t.Context(), []byte(expand(base, w.URL)), "", nil)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		mustCall(t, pc, "op", nil, nil)
		if got := w.last(t).RequestURI; got != "/v1/a" {
			t.Errorf("request target %q, want /v1/a", got)
		}
	})

	// Defects the literal text alone decides still set Server.Err.
	for _, u := range []string{"https://user@{host}/v1", "https://{host}/v1?x=1", "https://{host}/v1#f"} {
		doc := bare31(`"/a":{"get":{"operationId":"op"}}`, `"servers":[{"url":"`+u+`","variables":{"host":{"default":"h.example.test"}}}]`)
		if s := server(t, mustOp(t, parseAt(t, doc, "", testDocURI, nil), "op"), 0); s.Err == nil {
			t.Errorf("server %q: Err = nil for a defect in its literal text", u)
		}
	}
}

// Call's out must be "a non-nil *[]byte" (client.go, Call); a nil one is
// refused before sending. Response.Decode and StatusError.Decode report it
// as a *DecodeError (client.go, Response.Decode: "An invalid out is a
// *DecodeError without consuming or closing Body").
func TestNilBytePointerOutRefused(t *testing.T) {
	w := newWire(t, jsonAnswer(200, `{"a":1}`))
	c := parseFor(t, w, doc31(`"/x":{"post":{"operationId":"create"}}`), &openapi.Options{MaxErrorBytes: 4})
	noPanic(t, "Call", func() {
		resp, err := c.Call(t.Context(), "create", nil, (*[]byte)(nil))
		re := refusedBeforeSending(t, w, resp, err)
		if re.Err == nil {
			t.Errorf("RequestError.Err = nil for an out that cannot receive a result")
		}
	})
	noPanic(t, "Response.Decode", func() {
		resp, err := mustPrepare(t, c, "create", nil).Send(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var de *openapi.DecodeError
		if err := resp.Decode((*[]byte)(nil)); !errors.As(err, &de) {
			t.Errorf("Response.Decode((*[]byte)(nil)) = %v, want a *DecodeError", err)
		}
		if b, _ := io.ReadAll(resp.Body); string(b) != `{"a":1}` {
			t.Errorf("Body after the refused Decode reads %q", b)
		}
	})
	for _, body := range []string{`{}`, `{"long":"body"}`} { // complete, then cut by MaxErrorBytes
		w.setAnswer(jsonAnswer(500, body))
		_, err := c.Call(t.Context(), "create", nil, nil)
		var se *openapi.StatusError
		if !errors.As(err, &se) {
			t.Fatalf("error %v, want a *StatusError", err)
		}
		noPanic(t, "StatusError.Decode", func() {
			var de *openapi.DecodeError
			if err := se.Decode((*[]byte)(nil)); !errors.As(err, &de) {
				t.Errorf("StatusError.Decode((*[]byte)(nil)) with Content %q = %v, want a *DecodeError", se.Content, err)
			}
		})
	}
}

// A (location, style) pair OAS 3.1.2 section 4.8.12.3 (Style Values) does not
// allow is a document defect on Param.Err, and fails a call only when the call
// uses the parameter (describe.go, Operation.Err: "A defect in an optional part
// is reported on that part instead"). Every valid pair is serialized, so its
// Param.Err is nil; explode false, since explode true with spaceDelimited or
// pipeDelimited is an undefined combination (doc.go, Fixed rules, Styles;
// TestStyleRefusals).
func TestStyleLocationPairValidity(t *testing.T) {
	w := newWire(t, nil)
	invalid := []struct{ path, in, style string }{
		{"/q", "query", "simple"},
		{"/h", "header", "form"},
		{"/p/{v}", "path", "form"},
		{"/c", "cookie", "simple"},
		{"/l", "header", "label"},
		{"/m", "query", "matrix"},
		{"/d/{v}", "path", "deepObject"},
		{"/f", "query", "foo"},
	}
	for _, tt := range invalid {
		t.Run(tt.in+" "+tt.style, func(t *testing.T) {
			required := tt.in == "path"
			doc := doc31(fmt.Sprintf(`"%s":{"get":{"operationId":"op","parameters":[{"name":"v","in":"%s","required":%t,"style":"%s","schema":{}}]}}`,
				tt.path, tt.in, required, tt.style))
			c := parseFor(t, w, doc, nil)
			p := param(t, mustOp(t, c, "op"), 0)
			if p.Err == nil {
				t.Errorf("Param.Err = %v, want a document defect", p.Err)
			}
			before := w.count()
			resp, err := c.Call(t.Context(), "op", &openapi.Input{Params: map[string]any{"v": []string{"a", "b"}}}, nil)
			re := asRequestError(t, err)
			if resp != nil || w.count() != before {
				t.Errorf("a value for a defective parameter was sent")
			}
			if e := re.Inputs["v"]; e == nil {
				t.Errorf("Inputs[\"v\"] = %v, want the defect", e)
			}
			if !required {
				mustCall(t, c, "op", nil, nil) // not used: the call proceeds
			}
		})
	}
	valid := []struct{ path, in, style string }{
		{"/p/{v}", "path", "matrix"},
		{"/p/{v}", "path", "label"},
		{"/q", "query", "spaceDelimited"},
		{"/q", "query", "pipeDelimited"},
		{"/q", "query", "deepObject"},
		{"/c", "cookie", "form"},
	}
	for _, tt := range valid {
		doc := doc31(fmt.Sprintf(`"%s":{"get":{"operationId":"op","parameters":[{"name":"v","in":"%s","required":%t,"style":"%s","explode":false,"schema":{}}]}}`,
			tt.path, tt.in, tt.in == "path", tt.style))
		c := parseFor(t, w, doc, nil)
		if p := param(t, mustOp(t, c, "op"), 0); p.Err != nil {
			t.Errorf("%s %s: Param.Err = %v for a valid pair", tt.in, tt.style, p.Err)
		}
	}
}

// An undeclared key is refused however often the template names a parameter
// (client.go, Input.Params: "A key the operation does not declare ...
// refuses the call").
func TestRepeatedPathTemplateVariable(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/a/{id}/b/{id}":{"get":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}`), nil)
	resp, err := c.Call(t.Context(), "op", &openapi.Input{Params: map[string]any{"id": "x", "bogus": 1}}, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Inputs", re.Inputs, true, "bogus")
	mustCall(t, c, "op", &openapi.Input{Params: map[string]any{"id": "x"}}, nil)
	if got := w.only(t).RequestURI; got != "/a/x/b/x" {
		t.Errorf("request target %q, want /a/x/b/x", got)
	}
}

// A Load or Parse URI with userinfo is refused (load.go, Load: "one with
// userinfo, which RFC 9110 section 4.2.4 forbids a sender to generate"),
// before anything is fetched, and the password never appears in error text
// (doc.go, Outcomes: "No credential appears in the text of an error the
// client creates"). A BaseURL that fails to parse is refused without quoting
// its userinfo.
func TestDocumentURIUserinfoRefused(t *testing.T) {
	w := newWire(t, typedAnswer(200, "application/json", string(readPets(t))))
	withUser := strings.Replace(w.URL, "http://", "http://user:s3cret@", 1) + "/openapi.json"
	for _, uri := range []string{withUser, strings.Replace(w.URL, "http://", "http://user@", 1) + "/openapi.json"} {
		c, err := openapi.Load(t.Context(), uri, nil)
		if err == nil || c != nil {
			t.Errorf("Load(%q) = %v, %v; want a refusal", uri, c, err)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("Load error text holds the password: %v", err)
		}
	}
	w.nothingSent(t)
	c, err := openapi.Parse(t.Context(), readPets(t), "https://user:pw-s3cret@api.example.test/openapi.json", nil)
	if err == nil || c != nil {
		t.Errorf("Parse with a userinfo URI = %v, %v; want a refusal", c, err)
	} else if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("Parse error text holds the password: %v", err)
	}
	_, err = openapi.Parse(t.Context(), readPets(t), testDocURI, &openapi.Options{BaseURL: "https://user:s3cret@api.example.test/%zz"})
	re := asRequestError(t, err)
	wantKeys(t, "Settings", re.Settings, false, "Options.BaseURL")
	if strings.Contains(re.Error(), "s3cret") {
		t.Errorf("BaseURL refusal text holds the password: %v", re)
	}
}

// Header names must be RFC 9110 tokens (section 5.1) and values cannot carry
// CR, LF, NUL, other controls, or leading or trailing whitespace (section 5.5):
// doc.go, Fixed rules, Header fields: "A field name, in a Header
// (Options.Header, Input.Header or Part.Header) or of a header parameter, must
// be an RFC 9110 token, and a field value, a header parameter's included, may
// hold no control character but a tab and no leading or trailing whitespace"; a
// header parameter "whose name is not a token, has Param.Err, and any other
// breach of these rules is refused at the key of what gave it, such as Settings
// "Options.Header" or "Input.Header", or a Part's or header parameter's Inputs
// key", before sending. Input.MediaType is validated as a media type (section
// 8.3.1, parameters quoted as section 5.6.4 says; client.go, Response.Media:
// "That grammar governs every media type the client reads (a content key, an
// Encoding contentType, Input.MediaType, Part.MediaType)").
func TestHeaderFieldNameAndValueValidation(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/h":{"get":{"operationId":"h","parameters":[{"name":"X-V","in":"header","schema":{}},{"name":"X C","in":"header","schema":{}}]}},
		"/b":{"post":{"operationId":"b","requestBody":{"content":{"application/octet-stream":{}}}}}`)
	c := parseFor(t, w, doc, nil)

	t.Run("parameter name", func(t *testing.T) {
		// A header parameter whose name is not a token is defective.
		op := mustOp(t, c, "h")
		for _, p := range op.Params {
			if (p.Name == "X C") != (p.Err != nil) {
				t.Errorf("parameter %q Err = %v", p.Name, p.Err)
			}
		}
		before := w.count()
		resp, err := c.Call(t.Context(), "h", &openapi.Input{Params: map[string]any{"X C": "v"}}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, "Inputs", re.Inputs, false, "X C")
	})

	for _, v := range []string{" a", "a ", "\ta", "a\t", " a ", "a\x01b", "a\rb"} {
		t.Run(fmt.Sprintf("parameter value %q", v), func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "h", &openapi.Input{Params: map[string]any{"X-V": v}}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, "X-V")
		})
	}
	t.Run("inner whitespace", func(t *testing.T) {
		mustCall(t, c, "h", &openapi.Input{Params: map[string]any{"X-V": "a b\tc"}}, nil)
		if got := w.last(t).Header.Get("X-V"); got != "a b\tc" {
			t.Errorf("X-V = %q", got)
		}
	})

	// Input.Header, Options.Header at Load, and Options.Header from With.
	bad := []http.Header{
		{"X-C": {"v\r\nX-Injected: 1"}},
		{"X-C": {"a\x00b"}},
		{"X-C": {"a\x01b"}},
		{"X-C": {" a"}},
		{"X-C": {"a "}},
		{"X C": {"v"}},
		{"X-C\r\nX-Injected": {"v"}},
	}
	for _, h := range bad {
		t.Run(fmt.Sprintf("Header %q", h), func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "h", &openapi.Input{Header: h}, nil)
			re := asRequestError(t, err)
			if resp != nil || w.count() != before {
				t.Errorf("Input.Header was sent")
			}
			wantKeys(t, "Input.Header Settings", re.Settings, false, "Input.Header")

			_, err = openapi.Parse(t.Context(), []byte(expand(doc, w.URL)), w.URL+"/openapi.json", &openapi.Options{Header: h})
			wantKeys(t, "Load Settings", asRequestError(t, err).Settings, false, "Options.Header")

			resp, err = c.With(func(o *openapi.Options) { o.Header = h }).Call(t.Context(), "h", nil, nil)
			re = asRequestError(t, err)
			if resp != nil || w.count() != before {
				t.Errorf("Options.Header from With was sent")
			}
			wantKeys(t, "With Settings", re.Settings, false, "Options.Header")
		})
	}

	// Input.MediaType with a control byte in a quoted parameter, or a
	// quoted-pair escaping one.
	for _, mt := range []string{
		"application/octet-stream; a=\"\r\nX-Injected: 1\"",
		"application/octet-stream; a=\"\x01\"",
		"application/octet-stream; a=\"\\\x01\"",
		"application/octet-stream; a=\x7f",
	} {
		t.Run(fmt.Sprintf("MediaType %q", mt), func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "b", &openapi.Input{Body: []byte("x"), MediaType: mt}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, "Input.MediaType")
		})
	}
	t.Run("quoted-pair", func(t *testing.T) {
		mustCall(t, c, "b", &openapi.Input{Body: []byte("x"), MediaType: `application/octet-stream; a="b \"c\""`}, nil)
		if got := w.last(t).Header.Get("Content-Type"); got != `application/octet-stream; a="b \"c\""` {
			t.Errorf("Content-Type %q", got)
		}
	})
}

// An empty servers array on either side of a Path Item $ref is read as
// absent (doc.go, Fixed rules, URL: "An empty servers array on a path item
// or operation ... [is] read as absent"; describe.go, Operation: "a field on
// one side only is used").
func TestEmptyServersBesidePathItemRef(t *testing.T) {
	w := newWire(t, nil)
	doc := bare31(`"/a":{"$ref":"#/components/pathItems/A","servers":[]},
		"/b":{"$ref":"#/components/pathItems/B","servers":[{"url":"@BASE@/p"}]}`,
		`"servers":[{"url":"https://root.invalid"}]`,
		`"components":{"pathItems":{
			"A":{"servers":[{"url":"@BASE@/t"}],"get":{"operationId":"a"}},
			"B":{"servers":[],"get":{"operationId":"b"}}}}`)
	c := parseFor(t, w, doc, nil)
	for key, want := range map[string]string{"a": "/t/a", "b": "/p/b"} {
		t.Run(key, func(t *testing.T) {
			if op := mustOp(t, c, key); op.Err != nil {
				t.Fatalf("Err = %v", op.Err)
			}
			mustCall(t, c, key, nil, nil)
			if got := w.last(t).RequestURI; got != want {
				t.Errorf("sent to %q, want %q", got, want)
			}
		})
	}
}

// "A server with a variable that has none [no default] is not usable until
// Options.Variables gives it a value" (doc.go, Configuration), so the one
// other server selects itself; with the value, both are usable and the call
// needs a selection, keyed Options.Server (errors.go,
// RequestError.Settings).
func TestServerVariableWithoutDefaultUnusable(t *testing.T) {
	w := newWire(t, nil)
	for name, second := range map[string]string{
		"declared without default": `{"url":"https://{region}.example.test","variables":{"region":{"enum":["eu"]}}}`,
		"undeclared":               `{"url":"https://{region}.example.test"}`,
	} {
		doc := bare31(`"/a":{"get":{"operationId":"op"}}`, `"servers":[{"url":"@BASE@"},`+second+`]`)
		c := parseFor(t, w, doc, nil)
		before := w.count()
		if _, err := c.Call(t.Context(), "op", nil, nil); err != nil || w.count() != before+1 {
			t.Errorf("%s: %v; want the one usable server to select itself", name, err)
		}
		d := c.With(func(o *openapi.Options) { o.Variables["region"] = "eu" })
		resp, err := d.Call(t.Context(), "op", nil, nil)
		re := asRequestError(t, err)
		if resp != nil || w.count() != before+1 {
			t.Errorf("%s: a call with two usable servers was sent", name)
		}
		wantKeys(t, name+" Settings", re.Settings, false, "Options.Server")
	}
}

// "A *string and a *any take any text/* type as text, whatever its codec
// class (text/xml and text/event-stream included): a *string its bytes as
// sent ... and a *any a string" (client.go, Call).
func TestTextXMLDecodesAsText(t *testing.T) {
	body := `<a>hi<b>x</b></a>`
	for _, ct := range []string{"text/xml", "text/xml; charset=utf-8", "text/event-stream"} {
		w := newWire(t, typedAnswer(200, ct, body))
		c := parseFor(t, w, doc31(`"/a":{"get":{"operationId":"op","responses":{"200":{"description":"ok","content":{"text/xml":{}}}}}}`), nil)
		var s string
		mustCall(t, c, "op", nil, &s)
		if s != body {
			t.Errorf("%s into *string = %q, want the bytes as sent", ct, s)
		}
		var a any
		mustCall(t, c, "op", nil, &a)
		if a != body {
			t.Errorf("%s into *any = %#v, want the string", ct, a)
		}
	}
}

// The XML charset paths. client.go, Call: encoding/xml "reads UTF-8,
// US-ASCII and ISO-8859-1 documents, taking the encoding from a byte order
// mark, else the Content-Type's charset, else the document's own
// declaration"; any other is a *DecodeError.
func TestXMLCharsets(t *testing.T) {
	tests := []struct {
		name, ct, body string
		want           string // xmlPet.Name, or "" for a *DecodeError
	}{
		{"latin1 charset", "application/xml; charset=iso-8859-1", "<pet><name>\xe9t\xe9</name></pet>", "été"},
		{"latin1 charset case", "application/xml; charset=ISO-8859-1", "<pet><name>\xe9t\xe9</name></pet>", "été"},
		{"latin1 text/xml", "text/xml; charset=iso-8859-1", "<pet><name>\xe9t\xe9</name></pet>", "été"},
		{"BOM over charset", "application/xml; charset=iso-8859-1", "\xEF\xBB\xBF<pet><name>été</name></pet>", "été"},
		{"declaration", "application/xml", `<?xml version="1.0" encoding="ISO-8859-1"?><pet><name>` + "\xe9t\xe9" + `</name></pet>`, "été"},
		{"charset over declaration", "application/xml; charset=utf-8", `<?xml version="1.0" encoding="ISO-8859-1"?><pet><name>été</name></pet>`, "été"},
		{"us-ascii", "application/xml; charset=us-ascii", "<pet><name>ete</name></pet>", "ete"},
		{"utf-8 default", "application/xml", "<pet><name>été</name></pet>", "été"},
		{"unsupported charset", "application/xml; charset=shift_jis", "<pet><name>x</name></pet>", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, c := respClient(t, typedAnswer(200, tt.ct, tt.body), nil)
			var p xmlPet
			_, err := c.Call(t.Context(), "xml", nil, &p)
			if tt.want == "" {
				var de *openapi.DecodeError
				if !errors.As(err, &de) {
					t.Errorf("error %v, want a *DecodeError", err)
				}
				return
			}
			if err != nil || p.Name != tt.want {
				t.Errorf("Name = %q, %v; want %q", p.Name, err, tt.want)
			}
		})
	}
}

// A "~" not followed by "0" or "1" makes a JSON Pointer invalid (RFC 6901
// section 3), in Client.Document and in references.
func TestInvalidJSONPointerEscapeUnresolved(t *testing.T) {
	doc := bare31(`"/ok":{"get":{"operationId":"ok","parameters":[{"$ref":"#/components/parameters/a~02b"}]}},
		"/bad":{"get":{"operationId":"bad","parameters":[{"$ref":"#/components/parameters/a~2b"}]}}`,
		`"components":{"parameters":{"a~2b":{"name":"q","in":"query","schema":{}}}}`)
	c := parseAt(t, doc, "", testDocURI, nil)
	if got := c.Document(testDocURI + "#/components/parameters/a~2b"); got != nil {
		t.Errorf("Document with an invalid ~2 escape = %s, want nil", got)
	}
	if got := c.Document(testDocURI + "#/components/parameters/a~02b"); got == nil {
		t.Errorf("Document with the valid spelling ~02 = nil")
	}
	if op := mustOp(t, c, "ok"); op.Err != nil {
		t.Errorf("ok: Err = %v", op.Err)
	}
	if op := mustOp(t, c, "bad"); op.Err == nil || !errors.Is(op.Err, openapi.ErrUnresolved) {
		t.Errorf("bad: Err = %v, want one wrapping ErrUnresolved", op.Err)
	}
}

// Media specificity is compared by type specificity first, then parameter
// count (client.go, Response.Media: "a concrete type over type/*, type/*
// over */*, then more parameters over fewer"), so no number of parameters
// lets a range beat a concrete type.
func TestResponseMediaSpecificityOrder(t *testing.T) {
	var params strings.Builder
	for i := range 1001 {
		fmt.Fprintf(&params, "; p%d=v", i)
	}
	rangeType, _ := json.Marshal("application/*" + params.String())
	doc := doc31(`"/m":{"get":{"operationId":"m","responses":{"200":{"description":"ok","content":{` +
		string(rangeType) + `:{},"application/json":{}}}}}}`)
	w := newWire(t, typedAnswer(200, "application/json"+params.String(), "{}"))
	c := parseFor(t, w, doc, nil)
	resp := sendAndClose(t, mustPrepare(t, c, "m", nil))
	if want := responseMedia(t, mustOp(t, c, "m"), 0, 1); resp.Media != want {
		got := "nil"
		if resp.Media != nil {
			got = resp.Media.Type[:min(len(resp.Media.Type), 20)]
		}
		t.Errorf("Media = %s..., want application/json", got)
	}
}

// Raw document text is quoted in error text, so a hostile document cannot
// forge a log line, and a caller's value never appears (errors.go,
// RequestError.Error: "never a credential, an input's value, or a value given
// for a server variable or a header field"); an encoding
// error keeps its cause.
func TestErrorTextQuotesDocumentAndOmitsValues(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/x\n{a\nFAKE: line}":{"get":{"operationId":"forged"}},
		"/v":{"get":{"operationId":"v","parameters":[{"name":"q","in":"query","style":"foo\nFAKE: style","schema":{}}]}},` + jsonBodyOp)
	c := parseFor(t, w, doc, nil)
	if op := mustOp(t, c, "forged"); op.Err == nil || strings.Contains(op.Err.Error(), "\nFAKE") {
		t.Errorf("Operation.Err = %q, want the document's text quoted", op.Err)
	}
	if p := param(t, mustOp(t, c, "v"), 0); p.Err == nil || strings.Contains(p.Err.Error(), "\nFAKE") {
		t.Errorf("Param.Err = %q, want the document's text quoted", p.Err)
	}
	for _, key := range []string{"forged", "v"} {
		in := &openapi.Input{Params: map[string]any{"q": "x"}}
		if key == "forged" {
			in = nil
		}
		_, err := c.Call(t.Context(), key, in, nil)
		if err == nil || strings.Contains(err.Error(), "\nFAKE") {
			t.Errorf("%s: call error %q, want the document's text quoted", key, err)
		}
	}

	_, err := c.Call(t.Context(), "create", &openapi.Input{Body: map[string]any{"password": json.Number("hunter2-SECRETBODY")}}, nil)
	re := asRequestError(t, err)
	wantKeys(t, "Inputs", re.Inputs, true, "Input.Body")
	if strings.Contains(re.Error(), "SECRET") {
		t.Errorf("error text holds the body's value: %v", re)
	}
	c2 := parseFor(t, w, doc31(`"/q":{"get":{"operationId":"q","parameters":[{"name":"q","in":"query","schema":{}}]}}`), nil)
	_, err = c2.Call(t.Context(), "q", &openapi.Input{Params: map[string]any{"q": json.Number("sk_live_SECRETPARAM")}}, nil)
	re = asRequestError(t, err)
	wantKeys(t, "Inputs", re.Inputs, true, "q")
	if strings.Contains(re.Error(), "SECRET") {
		t.Errorf("error text holds the parameter's value: %v", re)
	}
	w.nothingSent(t)
}

// A file URL naming a host other than localhost is refused (load.go, Load;
// RFC 8089 section 2).
func TestFileURLWithRemoteHostRefused(t *testing.T) {
	abs, _ := filepath.Abs("testdata/pets.json")
	p := filepath.ToSlash(abs)
	if c, err := openapi.Load(t.Context(), (&url.URL{Scheme: "file", Host: "evil.example", Path: p}).String(), nil); err == nil || c != nil {
		t.Errorf("Load of file://evil.example/... = %v, %v; want a refusal", c, err)
	}
	if _, err := openapi.Load(t.Context(), (&url.URL{Scheme: "file", Host: "localhost", Path: p}).String(), nil); err != nil {
		t.Errorf("Load of file://localhost/...: %v", err)
	}
}

// An invalid media key is not a codec class, so it forces no Accept
// (client.go, Call: a typed out is refused only when 2xx responses "declare
// concrete media types of more than one codec class").
func TestInvalidMediaKeyForcesNoAccept(t *testing.T) {
	w := newWire(t, jsonAnswer(200, `{"a":1}`))
	c := parseFor(t, w, doc31(`"/a":{"get":{"operationId":"op","responses":{"200":{"description":"ok","content":{"application/json":{},"json":{}}}}}}`), nil)
	var out map[string]any
	mustCall(t, c, "op", nil, &out)
}

// "It is nil when the operation declares nothing for the status, or the
// status is outside 100 to 599" (client.go, Response.Declaration), even with
// a "default".
func TestDeclarationNilForStatusOutsideRange(t *testing.T) {
	doc := doc31(`"/a":{"get":{"operationId":"op","responses":{"200":{"description":"ok"},"2xx":{"description":"bad key"},"default":{"description":"other"}}}}`)
	for _, status := range []string{"099", "000", "600", "999"} {
		base := rawHTTP(t, "HTTP/1.1 "+status+" Odd\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", 0)
		c := parseAt(t, doc, base, base+"/openapi.json", nil)
		resp, err := mustPrepare(t, c, "op", nil).Send(t.Context())
		if err != nil {
			t.Errorf("%s: Send: %v", status, err)
			continue
		}
		resp.Body.Close()
		if resp.Declaration != nil {
			t.Errorf("status %s: Declaration %q, want nil", status, resp.Declaration.Key)
		}
	}
}

// A server URL with no host, or ending in "#", cannot be used (doc.go, Fixed
// rules, URL); the call is refused before sending rather than failing in the
// transport, and BaseURL "http://h/#" is refused.
func TestHostlessOrFragmentServerUnusable(t *testing.T) {
	for _, u := range []string{"https:///v1", "https:v1", "https://api.example.test/v#"} {
		t.Run(u, func(t *testing.T) {
			doc := bare31(`"/a":{"get":{"operationId":"op"}}`, `"servers":[{"url":"`+u+`"}]`)
			c := parseAt(t, doc, "", testDocURI, nil)
			if s := server(t, mustOp(t, c, "op"), 0); s.Err == nil {
				t.Errorf("Server.Err = nil")
			}
			resp, err := c.Call(t.Context(), "op", nil, nil)
			refusedBeforeSending(t, nil, resp, err)
		})
	}
	_, err := openapi.Parse(t.Context(), readPets(t), testDocURI, &openapi.Options{BaseURL: "http://h.example.test/#"})
	wantKeys(t, "Settings", asRequestError(t, err).Settings, false, "Options.BaseURL")
}

// Load checks Server and Variables against every server of the document
// (load.go, Load: "a Server or ServerID that matches no server"; client.go,
// Options.Variables: "A name that appears in no server URL of the document
// is refused by Load"), root servers every operation overrides and documents
// without operations included.
func TestLoadChecksServerOptionsAgainstEveryServer(t *testing.T) {
	componentsOnly := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test/{v}","variables":{"v":{"default":"1"}}}],"components":{}}`
	if _, err := openapi.Parse(t.Context(), []byte(componentsOnly), testDocURI,
		&openapi.Options{Server: "https://api.example.test/{v}", Variables: map[string]string{"v": "2"}}); err != nil {
		t.Errorf("components-only document: %v", err)
	}
	overridden := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://root.example.test"}],
		"paths":{"/a":{"servers":[{"url":"https://path.example.test"}],"get":{"operationId":"a","servers":[{"url":"https://op.example.test"}]}}}}`
	for _, s := range []string{"https://root.example.test", "https://path.example.test", "https://op.example.test"} {
		if _, err := openapi.Parse(t.Context(), []byte(overridden), testDocURI, &openapi.Options{Server: s}); err != nil {
			t.Errorf("Server %q: %v", s, err)
		}
	}
}

// Specification Extensions of the Paths and Responses Objects are not
// entries (OAS 3.1.2 sections 4.8.8 and 4.8.16: both "MAY be extended with
// Specification Extensions").
func TestExtensionsAreNotPathsOrResponses(t *testing.T) {
	doc := bare31(`"x-shared":{"$ref":"other.json#/x"},"/a":{"get":{"operationId":"op","responses":{"200":{"description":"ok"},"x-codegen":{"flag":true}}}}`)
	c := parseAt(t, doc, "", testDocURI, nil)
	if keys := opKeys(c.Operations()); len(keys) != 1 || keys[0] != "op" {
		t.Errorf("Operations = %q, want only op", keys)
	}
	if r := mustOp(t, c, "op").Responses; len(r) != 1 || r[0].Key != "200" {
		t.Errorf("Responses = %d, want only 200", len(r))
	}
}

// A Paths key that does not begin with "/" (OAS 3.1.2 section 4.8.8.1: "The
// field name MUST begin with a forward slash") is a defect on its entry,
// never a callable operation.
func TestPathsKeyWithoutSlashIsDefect(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"pets":{"get":{}}`), nil)
	ops := c.Operations()
	if len(ops) != 1 || ops[0].Path != "pets" || ops[0].Err == nil {
		t.Fatalf("Operations = %+v, want one entry for pets with Err", ops)
	}
	resp, err := c.Call(t.Context(), "GET pets", nil, nil)
	refusedBeforeSending(t, w, resp, err)
}

// A 3.1 Server Object has no "name" (OAS 3.1.2 section 4.8.5; describe.go,
// Server.Name: "the OpenAPI 3.2 server name"), so it neither sets
// Server.Name nor matches Options.Server.
func TestServerNameOnlyInOpenAPI32(t *testing.T) {
	doc := bare31(`"/a":{"get":{"operationId":"op"}}`, `"servers":[{"url":"https://api.example.test","name":"prod"}]`)
	c := parseAt(t, doc, "", testDocURI, nil)
	if s := server(t, mustOp(t, c, "op"), 0); s.Name != "" {
		t.Errorf("Server.Name = %q in a 3.1 document", s.Name)
	}
	_, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{Server: "prod"})
	wantKeys(t, "Settings", asRequestError(t, err).Settings, false, "Options.Server")
}

// A Codecs key with surrounding whitespace is not a media type without
// parameters (client.go, Options.Codecs: "Load refuses any other key").
func TestCodecKeyWithWhitespaceRefused(t *testing.T) {
	for _, key := range []string{" application/cbor", "application/cbor ", "\tapplication/cbor", " +cbor", "+cbor "} {
		_, err := openapi.Parse(t.Context(), []byte(expand(bodyDoc, "https://api.example.test")), testDocURI,
			&openapi.Options{Codecs: map[string]openapi.Codec{key: tagCodec{tag: "X"}}})
		if err == nil {
			t.Errorf("Codecs key %q accepted", key)
			continue
		}
		wantKeys(t, fmt.Sprintf("Settings for %q", key), asRequestError(t, err).Settings, false, "Options.Codecs")
	}
}

// "Prepare returns a *RequestError listing every problem at once"
// (client.go, Prepare): the Accept requirement beside a missing parameter,
// and the out and a derived Client's unusable Options beside a defective
// operation's Err.
func TestRequestErrorListsEveryProblem(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/a":{"get":{"operationId":"op","parameters":[{"name":"q","in":"query","required":true,"schema":{}}],
			"responses":{"200":{"description":"ok","content":{"application/json":{},"application/xml":{}}}}}},
		"/broken":{"get":{"operationId":"broken","parameters":[{"$ref":"#/components/parameters/Nope"}]}}`)
	c := parseFor(t, w, doc, nil)
	resp, err := c.Call(t.Context(), "op", nil, new(Pet))
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Inputs", re.Inputs, false, "q")
	wantKeys(t, "Settings", re.Settings, false, "Options.Header")

	op := mustOp(t, c, "broken")
	before := w.count()
	resp, err = c.With(func(o *openapi.Options) { o.Server = "https://nowhere.example.test" }).Call(t.Context(), "broken", nil, Pet{})
	re = refusedSince(t, w, before, resp, err)
	if op.Err == nil || !errors.Is(err, op.Err) {
		t.Errorf("error %v does not wrap the operation's Err", err)
	}
	wantKeys(t, "Settings", re.Settings, false, "Options.Server")
	joined, ok := re.Err.(interface{ Unwrap() []error })
	if !ok || len(joined.Unwrap()) < 2 {
		t.Errorf("RequestError.Err = %v, want the operation's Err joined with the out's", re.Err)
	}
}

// As client.go, With says, "any other Options the document cannot use
// refuse each call they affect": a media range in Options.MediaType affects
// only calls that send a body, and a Codecs key only calls that encode or
// decode with a codec.
func TestWithOptionsRefuseOnlyAffectedCalls(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, bodyDoc, nil)
	d := c.With(func(o *openapi.Options) { o.MediaType = "application/*" })
	mustCall(t, d, "json", nil, nil)
	before := w.count()
	resp, err := d.Call(t.Context(), "json", &openapi.Input{Body: map[string]int{"a": 1}}, nil)
	re := asRequestError(t, err)
	if resp != nil || w.count() != before {
		t.Errorf("a body under a media range was sent")
	}
	wantKeys(t, "Settings", re.Settings, false, "Options.MediaType")

	e := c.With(func(o *openapi.Options) { o.Codecs["application/json; x=1"] = tagCodec{tag: "X"} })
	mustCall(t, e, "json", nil, nil)
	resp, err = e.Call(t.Context(), "json", &openapi.Input{Body: map[string]int{"a": 1}}, nil)
	re = asRequestError(t, err)
	if resp != nil || w.count() != before+1 {
		t.Errorf("a body with an unusable Codecs key was sent")
	}
	wantKeys(t, "Settings", re.Settings, false, "Options.Codecs")

	// A malformed Codecs key refuses only calls that would use a codec: a
	// structured body or a decoded out. A decoded out is refused before
	// sending; a *[]byte, which bypasses codecs, is not.
	before = w.count()
	resp, err = e.Call(t.Context(), "json", nil, new(Pet))
	re = refusedSince(t, w, before, resp, err)
	wantKeys(t, "Settings", re.Settings, false, "Options.Codecs")
	var raw []byte
	mustCall(t, e, "json", nil, &raw)
}

// A declared path parameter the template does not name (OAS 3.1.2 section
// 4.8.12.2.1: its name "MUST correspond to a template expression") has
// Param.Err; a required one refuses the call (describe.go, Param.Err), and a
// value given for it is refused at its key.
func TestPathParameterMissingFromTemplate(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/a":{"get":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}`), nil)
	if p := param(t, mustOp(t, c, "op"), 0); p.Err == nil {
		t.Errorf("Param.Err = nil")
	}
	resp, err := c.Call(t.Context(), "op", &openapi.Input{Params: map[string]any{"id": "7"}}, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Inputs", re.Inputs, false, "id")
	before := w.count()
	resp, err = c.Call(t.Context(), "op", nil, nil)
	refusedSince(t, w, before, resp, err)
}

// Header parameter identity compares names case-insensitively, so a
// path-level "X-Id" is overridden by an operation-level "x-id" (OAS 3.1.2
// section 3.8: names that map to HTTP concepts follow HTTP's case rules):
// Operation.Params lists one header parameter, the operation's, and the call
// writes the field once. A required parameter that was given is not also
// reported missing: the given required content parameter is sent, its JSON
// written as given (doc.go, Values and Fixed rules, Percent-encoding).
func TestHeaderParameterOverrideIgnoresCase(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/a":{"parameters":[{"name":"X-Id","in":"header","schema":{}}],"get":{"operationId":"op","parameters":[{"name":"x-id","in":"header","description":"operation level","schema":{}}]}},
		"/c":{"get":{"operationId":"content","parameters":[{"name":"X-V","in":"header","required":true,"content":{"application/json":{}}}]}}`)
	c := parseFor(t, w, doc, nil)
	params := mustOp(t, c, "op").Params
	if len(params) != 1 {
		var names []string
		for _, p := range params {
			names = append(names, p.In+" "+p.Name)
		}
		t.Fatalf("Params = %q, want the one operation-level header parameter", names)
	}
	if p := params[0]; p.Name != "x-id" || p.Key != "x-id" || p.In != "header" || p.Description != "operation level" ||
		p.Source != w.URL+"/openapi.json#/paths/~1a/get/parameters/0" {
		t.Errorf("Params[0] = %s %q (key %q, %q) at %q, want the operation's x-id", p.In, p.Name, p.Key, p.Description, p.Source)
	}
	mustCall(t, c, "op", &openapi.Input{Params: map[string]any{"x-id": "2"}}, nil)
	if v := w.last(t).Header.Values("X-Id"); len(v) != 1 || v[0] != "2" {
		t.Errorf("X-Id sent as %q, want once, as [\"2\"]", v)
	}
	// The overridden parameter's name is no key (client.go, Input.Params:
	// values are "by Param.Key").
	before := w.count()
	resp, err := c.Call(t.Context(), "op", &openapi.Input{Params: map[string]any{"X-Id": "1"}}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "Inputs", re.Inputs, true, "X-Id")

	mustCall(t, c, "content", &openapi.Input{Params: map[string]any{"X-V": "a"}}, nil)
	if v := w.last(t).Header.Values("X-V"); len(v) != 1 || v[0] != `"a"` {
		t.Errorf("X-V sent as %q, want [%q]", v, `"a"`)
	}
}

// "A Header holding two spellings of one field is refused" (doc.go, Fixed
// rules, Header fields), at Load for Options.Header and at a call for
// Input.Header.
func TestHeaderTwoSpellingsRefused(t *testing.T) {
	w := newWire(t, nil)
	h := http.Header{"x-tenant": {"lower"}, "X-Tenant": {"canonical"}}
	_, err := openapi.Parse(t.Context(), []byte(expand(headerDoc, w.URL)), w.URL+"/openapi.json", &openapi.Options{Header: h})
	wantKeys(t, "Load Settings", asRequestError(t, err).Settings, false, "Options.Header")
	c := parseFor(t, w, headerDoc, nil)
	resp, err := c.Call(t.Context(), "plain", &openapi.Input{Header: h}, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Settings", re.Settings, false, "Input.Header")
}

// "A uri with a fragment is refused" (load.go, Load), and Parse's uri is
// "without a fragment" (load.go, Parse).
func TestDocumentURIFragmentRefused(t *testing.T) {
	w := newWire(t, typedAnswer(200, "application/json", string(readPets(t))))
	if c, err := openapi.Load(t.Context(), w.URL+"/openapi.json#section", nil); err == nil || c != nil {
		t.Errorf("Load with a fragment = %v, %v; want a refusal", c, err)
	}
	w.nothingSent(t)
	if c, err := openapi.Parse(t.Context(), readPets(t), testDocURI+"#frag", nil); err == nil || c != nil {
		t.Errorf("Parse with a fragment = %v, %v; want a refusal", c, err)
	}
}

// A field with no values removes it, "a User-Agent included, so net/http
// adds none" (doc.go, Fixed rules, Header fields).
func TestHeaderWithoutValuesRemovesUserAgent(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, headerDoc, &openapi.Options{Header: http.Header{"User-Agent": nil}})
	mustCall(t, c, "plain", nil, nil)
	if v := w.last(t).Header.Values("User-Agent"); len(v) != 0 {
		t.Errorf("Options.Header: User-Agent sent %q, want none", v)
	}
	c = parseFor(t, w, headerDoc, nil)
	mustCall(t, c, "plain", &openapi.Input{Header: http.Header{"User-Agent": {}}}, nil)
	if v := w.last(t).Header.Values("User-Agent"); len(v) != 0 {
		t.Errorf("Input.Header: User-Agent sent %q, want none", v)
	}
}

// "A path parameter value that would form a whole "." or ".." segment is
// refused, since RFC 3986 section 5.2.4 removes such segments" (doc.go,
// Fixed rules, Percent-encoding). Other dotted values are sent.
func TestPathValueDotSegmentsRefused(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/users/{id}/sessions":{"delete":{"operationId":"op","parameters":[{"name":"id","in":"path","required":true,"schema":{}}]}}`), nil)
	for _, v := range []any{".", "..", []string{".."}} {
		t.Run(fmt.Sprintf("refused %q", v), func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "op", &openapi.Input{Params: map[string]any{"id": v}}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, "id")
		})
	}
	for v, want := range map[string]string{"...": "/users/.../sessions", ".a": "/users/.a/sessions", "a.": "/users/a./sessions", "./x": "/users/.%2Fx/sessions"} {
		t.Run(fmt.Sprintf("sent %q", v), func(t *testing.T) {
			before := w.count()
			mustCall(t, c, "op", &openapi.Input{Params: map[string]any{"id": v}}, nil)
			if got := w.last(t).RequestURI; w.count() != before+1 || got != want {
				t.Errorf("sent as %q, want %q", got, want)
			}
		})
	}
}

// A Variables value substituted into the authority "may not hold "/", "?",
// "#", "@" or "\\"" (client.go, Options.Variables; RFC 3986 section 3.2), so
// a value cannot move the request to another host. Each value here would
// move the request to the test server, which must receive nothing.
func TestAuthorityVariableValuesCannotMoveHost(t *testing.T) {
	w := newWire(t, nil)
	host := w.hostport()
	doc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"http://{tenant}.api.example.test/v1","variables":{"tenant":{"default":"acme"}}}],
		"paths":{"/x":{"get":{"operationId":"op"}}}}`
	for _, v := range []string{host + "/", host + "/#", host + "?", "a@" + host + "/", host + "\\"} {
		c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, &openapi.Options{Variables: map[string]string{"tenant": v}})
		if err != nil {
			asRequestError(t, err)
			continue
		}
		before := w.count()
		resp, err := c.Call(t.Context(), "op", nil, nil)
		refusedSince(t, w, before, resp, err)
	}
	w.nothingSent(t)
	// A value in the path may hold "/" (substituted as given).
	pathDoc := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"@BASE@/{base}","variables":{"base":{"default":"v1"}}}],
		"paths":{"/x":{"get":{"operationId":"op"}}}}`
	mustCall(t, parseFor(t, w, pathDoc, &openapi.Options{Variables: map[string]string{"base": "api/v2"}}), "op", nil, nil)
	if got := w.last(t).RequestURI; got != "/api/v2/x" {
		t.Errorf("request target %q, want /api/v2/x", got)
	}
}

// The Loader doc: a rejection's line and column are "both counted from 1,
// the column in the document's own bytes ... after any byte order mark", so
// a BOM does not move the column.
func TestColumnsAfterBOM(t *testing.T) {
	doc := `{"openapi":"3.1.0","paths":{},"paths":{}}`
	_, plain := openapi.Parse(t.Context(), []byte(doc), testDocURI, nil)
	_, bom := openapi.Parse(t.Context(), []byte("\xEF\xBB\xBF"+doc), testDocURI, nil)
	if plain == nil || bom == nil {
		t.Fatalf("errors %v, %v; want both rejected", plain, bom)
	}
	namesURIAndLine(t, plain, testDocURI, 1)
	// The second "paths" key begins at byte 31 of line 1.
	if !regexp.MustCompile(`(^|[^0-9])31([^0-9]|$)`).MatchString(plain.Error()) {
		t.Errorf("error %q does not name column 31, where the repeated key begins", plain)
	}
	if bom.Error() != plain.Error() {
		t.Errorf("with a BOM: %q, without: %q; want the same position", bom, plain)
	}
}

// Call's out must not be "a nil pointer of any type" (client.go, Call:
// "anything else, a nil pointer of any type included, is refused before
// sending"). A nil *bytes.Buffer is an io.Writer, and still refused.
func TestNilPointerOutOfAnyTypeRefused(t *testing.T) {
	w := newWire(t, jsonAnswer(200, `{"a":1}`))
	c := parseFor(t, w, doc31(`"/x":{"post":{"operationId":"create"}}`), nil)
	for name, out := range map[string]any{
		"*bytes.Buffer":   (*bytes.Buffer)(nil),
		"*map[string]any": (*map[string]any)(nil),
		"*any":            (*any)(nil),
		"*string":         (*string)(nil),
	} {
		noPanic(t, name, func() {
			before := w.count()
			resp, err := c.Call(t.Context(), "create", nil, out)
			re := refusedSince(t, w, before, resp, err)
			if re.Err == nil {
				t.Errorf("%s: RequestError.Err = nil for an out that cannot receive a result", name)
			}
		})
	}
	// Response.Decode applies Call's target rules: an invalid out is a
	// *DecodeError (client.go, Response.Decode).
	noPanic(t, "Response.Decode", func() {
		resp, err := mustPrepare(t, c, "create", nil).Send(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var de *openapi.DecodeError
		if err := resp.Decode((*bytes.Buffer)(nil)); !errors.As(err, &de) {
			t.Errorf("Response.Decode((*bytes.Buffer)(nil)) = %v, want a *DecodeError", err)
		}
	})
}

// A Host field or a header parameter named Host is refused, since net/http
// derives Host from the URL (doc.go, Fixed rules, Header fields):
// Options.Header by Load (load.go, Load: "a Header field that is always
// refused") at Settings["Options.Header"], Input.Header at a call at
// Settings["Input.Header"], and a value for a header parameter named Host at
// Inputs[its Param.Key] (errors.go, RequestError: the refusal key grammar).
func TestHostHeaderAndParameterRefused(t *testing.T) {
	w := newWire(t, nil)
	doc := doc31(`"/plain":{"get":{"operationId":"plain"}},
		"/host":{"get":{"operationId":"host","parameters":[{"name":"Host","in":"header","schema":{}}]}},
		"/lower":{"get":{"operationId":"lower","parameters":[{"name":"host","in":"header","schema":{}}]}}`)
	for _, name := range []string{"Host", "host"} {
		h := http.Header{name: {"evil.example.test"}}
		t.Run("Options.Header "+name, func(t *testing.T) {
			c, err := openapi.Parse(t.Context(), []byte(expand(doc, w.URL)), w.URL+"/openapi.json", &openapi.Options{Header: h})
			if c != nil {
				t.Errorf("Load returned a Client")
			}
			wantKeys(t, "Settings", asRequestError(t, err).Settings, false, "Options.Header")
		})
		t.Run("Input.Header "+name, func(t *testing.T) {
			c := parseFor(t, w, doc, nil)
			before := w.count()
			resp, err := c.Call(t.Context(), "plain", &openapi.Input{Header: h}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, "Input.Header")
		})
		t.Run("With Options.Header "+name, func(t *testing.T) {
			c := parseFor(t, w, doc, nil).With(func(o *openapi.Options) { o.Header = h })
			before := w.count()
			resp, err := c.Call(t.Context(), "plain", nil, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, "Options.Header")
		})
	}
	for _, key := range []string{"host", "lower"} {
		t.Run("parameter "+key, func(t *testing.T) {
			c := parseFor(t, w, doc, nil)
			p := param(t, mustOp(t, c, key), 0)
			before := w.count()
			resp, err := c.Call(t.Context(), key, &openapi.Input{Params: map[string]any{p.Key: "evil.example.test"}}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, false, p.Key)
		})
	}
	for _, r := range w.requests() {
		if r.Host == "evil.example.test" {
			t.Errorf("a request went out with Host %q", r.Host)
		}
	}
}

// The dot-segment rule applies to the resulting segment, however many values
// form it: in "/{a}{b}", a="." and b="." form "..", which is refused; so is
// a="" and b="..". Values that form any other segment are sent.
func TestDotSegmentFormedByTwoValues(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/x/{a}{b}/y":{"get":{"operationId":"op","parameters":[
		{"name":"a","in":"path","required":true,"schema":{}},
		{"name":"b","in":"path","required":true,"schema":{}}]}}`), nil)
	for _, v := range [][2]string{{".", "."}, {"", ".."}, {"..", ""}, {"", "."}} {
		t.Run(fmt.Sprintf("refused %q+%q", v[0], v[1]), func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "op", &openapi.Input{Params: map[string]any{"a": v[0], "b": v[1]}}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantAnyKey(t, "Inputs", re.Inputs, "a", "b")
		})
	}
	for v, want := range map[[2]string]string{{".", "x"}: "/x/.x/y", {".", ".."}: "/x/.../y", {"a", "."}: "/x/a./y"} {
		t.Run(fmt.Sprintf("sent %q+%q", v[0], v[1]), func(t *testing.T) {
			before := w.count()
			mustCall(t, c, "op", &openapi.Input{Params: map[string]any{"a": v[0], "b": v[1]}}, nil)
			if got := w.last(t).RequestURI; w.count() != before+1 || got != want {
				t.Errorf("sent as %q, want %q", got, want)
			}
		})
	}
}
