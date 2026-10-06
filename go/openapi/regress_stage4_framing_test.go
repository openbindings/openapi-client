package openapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for framing and media selection. Framing checks refuse
// every separator form common receivers recognize, which costs no conformant
// content: with a given boundary, "--" + boundary at the start of content or
// after a CR or LF; a media type with two boundary parameters is refused; a
// pre-encoded JSON Lines item may not hold CR or LF; an event id holding NUL
// cannot be encoded; event fields with invalid UTF-8 are written as U+FFFD.
// Every Media a call can select has its compiled plan. client.go,
// Input.MediaType: "a boundary given for a multipart body the client encodes
// is used, a part whose content holds its delimiter, or "--" and the
// boundary after a CR or LF, being an input that cannot be encoded (RFC 2046
// section 5.1.1). A boundary in the declared content key is used and checked
// the same way; an invalid one is the Media's Err. Two boundary parameters
// are refused."

// With a boundary the caller gives, content holding "--" and the boundary
// after a lone LF or a lone CR is refused at its key before sending, as
// after CRLF (receivers in wide use split parts at either); one streamed
// from any reader, which is checked as it streams, ends the body as an
// upload error.
func TestC43LoneLineBreakDelimiters(t *testing.T) {
	const mt = "multipart/form-data; boundary=b0und4ry"
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	inject := "hello\n--b0und4ry\nContent-Disposition: form-data; name=\"admin\"\n\ntrue\n--b0und4ry--\n"
	for _, tt := range []struct {
		name string
		body map[string]any
		at   string
	}{
		{"LF in bytes", map[string]any{"blob": []byte("x\n--b0und4ry\ny")}, "Input.Body/blob"},
		{"CR in bytes", map[string]any{"blob": []byte("x\r--b0und4ry")}, "Input.Body/blob"},
		{"LF in text", map[string]any{"title": "a\n--b0und4ry--"}, "Input.Body/title"},
		{"CR in text", map[string]any{"title": "a\r--b0und4ry"}, "Input.Body/title"},
		{"an injected field", map[string]any{"title": inject}, "Input.Body/title"},
		{"LF in an item", map[string]any{"tags": []string{"ok", "z\n--b0und4ry"}}, "Input.Body/tags/1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "mp", &openapi.Input{Body: tt.body, MediaType: mt}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, tt.at)
		})
	}
	for _, content := range []string{"x\n--b0und4ry\ny", "x\r--b0und4ry"} {
		for _, r := range readerKinds(t, content) {
			t.Run(fmt.Sprintf("%s %q", r.name, content), func(t *testing.T) {
				srv := newBodyServer(t, false, "")
				sc := parseAt(t, mpDoc(), srv.URL, srv.URL+"/openapi.json", nil)
				ctx, _ := gateCtx(t)
				res := awaitCall(t, callAsync(ctx, sc, "mp", &openapi.Input{Body: map[string]any{"blob": r.reader}, MediaType: mt}), "Call")
				if res.err == nil || isRequestError(res.err) {
					t.Fatalf("Call = %v, want an upload error, not a *RequestError", res.err)
				}
				if _, err := srv.finished(t); err == nil {
					t.Errorf("the server read a complete body")
				}
			})
		}
	}
}

// boundaryDoc has operations whose declared content keys carry a boundary:
// a valid one, two, and an empty quoted one.
func boundaryDoc() string {
	key := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return doc31(fmt.Sprintf(`
		"/k":{"post":{"operationId":"keyed","requestBody":{"content":{%s:{"schema":{"type":"object","properties":{"title":{"type":"string"}}}}}}}},
		"/d":{"post":{"operationId":"dupKey","requestBody":{"content":{%s:{}}}}},
		"/e":{"post":{"operationId":"emptyKey","requestBody":{"content":{%s:{
			"schema":{"type":"object","properties":{"n":{"type":"object"}}},"encoding":{"n":{"contentType":"multipart/mixed"}}}}}}}`,
		key("multipart/form-data; boundary=docb"),
		key("multipart/form-data; boundary=A; boundary=B"),
		key(`multipart/form-data; boundary=""`)))
}

// A boundary in the declared content key is used as given and checked as a
// given one is; two boundary parameters, in Input.MediaType or the content
// key, are refused (RFC 6838 section 4.3: a parameter given more than once
// is an error), in Input.MediaType at Settings ["Input.MediaType"] and in
// the key as the Media's Err.
func TestC43DeclaredAndDuplicateBoundaries(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, boundaryDoc(), nil)
	t.Run("declared boundary used", func(t *testing.T) {
		mustCall(t, c, "keyed", &openapi.Input{Body: map[string]any{"title": "x"}}, nil)
		got := w.last(t)
		if ct := got.Header.Get("Content-Type"); ct != "multipart/form-data; boundary=docb" {
			t.Errorf("Content-Type %q, want the declared key as given", ct)
		}
		_, _, parts := readMultipart(t, "multipart/form-data; boundary=docb", got.Body)
		checkParts(t, parts, []wantPart{{disposition: formData("title"), ctype: "text/plain", content: "x"}})
	})
	for _, content := range []string{"x\r\n--docb", "x\n--docb", "--docb--"} {
		t.Run(fmt.Sprintf("declared boundary checked %q", content), func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "keyed", &openapi.Input{Body: map[string]any{"title": content}}, nil)
			wantKeys(t, "Inputs", refusedSince(t, w, before, resp, err).Inputs, true, "Input.Body/title")
		})
	}
	mc := parseFor(t, w, mpDoc(), nil)
	for _, body := range []any{map[string]any{"title": "x"}, []byte("--A\r\n\r\nx\r\n--A--\r\n")} {
		t.Run(fmt.Sprintf("two boundaries in Input.MediaType, %T", body), func(t *testing.T) {
			before := w.count()
			resp, err := mc.Call(t.Context(), "raw", &openapi.Input{Body: body, MediaType: "multipart/form-data; boundary=A; boundary=B"}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Settings", re.Settings, false, "Input.MediaType")
		})
	}
	t.Run("two boundaries in the key", func(t *testing.T) {
		if m := reqMedia(t, mustOp(t, c, "dupKey"), 0); m.Err == nil {
			t.Errorf("a content key with two boundary parameters has no Err")
		}
	})
}

// An empty quoted boundary, given in Input.MediaType or declared in the
// content key, is refused, never a hang: Input.MediaType's at Settings
// ["Input.MediaType"], the key's as the Media's Err (client.go,
// Input.MediaType: "A boundary in the declared content key is used and
// checked the same way; an invalid one is the Media's Err"), a document
// defect, not "Input.MediaType", with a nested multipart part whose own
// boundary would be generated. Since a regression would make Prepare spin
// forever, the cases run in a child process under a 10 s guard.
func TestF1EmptyBoundaryRefused(t *testing.T) {
	if !inChild(t) {
		return
	}
	w := newWire(t, nil)
	mc := parseFor(t, w, mpDoc(), nil)
	var err error
	for _, body := range []map[string]any{{"bundle": map[string]any{"a": "x"}}, {"title": "x"}} {
		promptly(t, 10*time.Second, "Prepare with Input.MediaType boundary=\"\"", func() {
			_, err = mc.Prepare("mp", &openapi.Input{Body: body, MediaType: `multipart/form-data; boundary=""`})
		})
		var re *openapi.RequestError
		if !errors.As(err, &re) {
			t.Fatalf("Prepare = %v, want a refusal", err)
		}
		wantKeys(t, "Settings", re.Settings, false, "Input.MediaType")
	}
	c := parseFor(t, w, boundaryDoc(), nil)
	nested := map[string]any{"n": map[string]any{"x": "v"}}
	if m := reqMedia(t, mustOp(t, c, "emptyKey"), 0); m.Err == nil {
		t.Errorf("a content key with an empty boundary has no Err")
	}
	var resp *openapi.Response
	promptly(t, 10*time.Second, "Call under the key boundary=\"\"", func() {
		resp, err = c.Call(t.Context(), "emptyKey", &openapi.Input{Body: nested}, nil)
	})
	refusedBeforeSending(t, w, resp, err)
}

// A pre-encoded JSON Lines item may not hold a CR either (client.go,
// Input.Body: "one holding the framing's separator (LF or CR, or RS) cannot
// be encoded"), which some receivers end a line at: refused at its key from
// a slice; from a reader, the body ends as an upload error.
func TestC43JSONLinesItemHoldingCR(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	for _, tt := range []struct {
		key  string
		body any
		at   string
	}{
		{"jsonl", [][]byte{[]byte(`{"note":"hi"}` + "\r" + `{"admin":true}`)}, "Input.Body/0"},
		{"ndjson", []any{[]byte("1"), []byte("2\r")}, "Input.Body/1"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), tt.key, &openapi.Input{Body: tt.body}, nil)
			wantKeys(t, "Inputs", refusedSince(t, w, before, resp, err).Inputs, true, tt.at)
		})
	}
	for _, r := range readerKinds(t, "{}\r{}") {
		t.Run(r.name, func(t *testing.T) {
			srv := newBodyServer(t, false, "")
			sc := parseAt(t, seqDoc(), srv.URL, srv.URL+"/openapi.json", nil)
			ctx, _ := gateCtx(t)
			res := awaitCall(t, callAsync(ctx, sc, "jsonl", &openapi.Input{Body: []io.Reader{r.reader}}), "Call")
			if res.err == nil || isRequestError(res.err) {
				t.Fatalf("Call = %v, want an upload error", res.err)
			}
			if _, err := srv.finished(t); err == nil {
				t.Errorf("the server read a complete body")
			}
		})
	}
}

// An event id holding NUL cannot be encoded (HTML standard, section 9.2.6,
// the id field: "If the field value does not contain U+0000 NULL, then set
// the last event ID buffer to the field value. Otherwise, ignore the field";
// client.go, Input.Body: "a NUL in id"), and an Event's invalid UTF-8 is
// written as U+FFFD, one per invalid byte, as the object path's JSON data
// has it ("invalid UTF-8 is written as U+FFFD, as an event stream is UTF-8";
// HTML standard: "Event streams in this specification must always be encoded
// as UTF-8"), a run of invalid bytes as one U+FFFD each, as encoding/json
// writes it.
func TestC43EventStreamNULAndUTF8(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	for _, body := range []any{
		[]openapi.Event{{ID: "a\x00b", IDSet: true}},
		[]any{map[string]any{"data": "x", "id": "a\x00b"}},
	} {
		t.Run(fmt.Sprintf("NUL in id, %T", body), func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), "sse", &openapi.Input{Body: body}, nil)
			wantKeys(t, "Inputs", refusedSince(t, w, before, resp, err).Inputs, true, "Input.Body/0")
		})
	}
	mustCall(t, c, "sse", &openapi.Input{Body: []any{
		openapi.Event{Data: []byte("\xffa\nb\xfe"), Event: "e\xfe", ID: "i\xff", IDSet: true},
		map[string]any{"data": "m\xffn"},
		openapi.Event{Data: []byte("r\xff\xfe\xc3s"), Event: "\xc0\xaf", ID: "\xed\xa0\x80", IDSet: true},
		map[string]any{"data": "t\xff\xfe"},
	}}, nil)
	const fffd = "\xef\xbf\xbd" // U+FFFD in UTF-8
	want := "data: " + fffd + "a\ndata: b" + fffd + "\nevent: e" + fffd + "\nid: i" + fffd + "\n\ndata: m" + fffd + "n\n\n" +
		"data: r" + strings.Repeat(fffd, 3) + "s\nevent: " + strings.Repeat(fffd, 2) + "\nid: " + strings.Repeat(fffd, 3) + "\n\n" +
		"data: t" + strings.Repeat(fffd, 2) + "\n\n"
	// encoding/json writes one U+FFFD for each of those invalid bytes.
	if ref, _ := json.Marshal("\xff\xfe\xc3\xc0\xaf\xed\xa0\x80"); string(ref) != `"`+strings.Repeat(`\ufffd`, 8)+`"` {
		t.Fatalf("test bug: encoding/json writes %s", ref)
	}
	if got := w.last(t); string(got.Body) != want {
		t.Errorf("event stream %q, want %q", got.Body, want)
	}
}

// encoderCodec is the standard codec shape: json.Encoder, which ends every
// value with LF; crlfCodec ends it with CRLF; indentCodec writes LF inside.
type (
	encoderCodec struct{}
	crlfCodec    struct{}
	indentCodec  struct{}
)

func (encoderCodec) Encode(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func (encoderCodec) Decode(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }
func (crlfCodec) Encode(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	w.Write(append(b, " \r\n"...))
	return err
}
func (crlfCodec) Decode(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }
func (indentCodec) Encode(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	w.Write(b)
	return err
}
func (indentCodec) Decode(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }

// A codec's output is framed after its trailing JSON whitespace (SP, HTAB,
// LF, CR; RFC 8259 section 2: "JSON-text = ws value ws") is trimmed
// (client.go, Input.Body: "a codec's output is framed after its trailing
// JSON whitespace is trimmed"), so json.Encoder works under JSON Lines and
// sequences; a separator left inside is refused at the item's key, with an
// error naming the codec's output.
func TestF8CodecOutputFramed(t *testing.T) {
	w := newWire(t, nil)
	items := []any{map[string]any{"a": 1}, 2}
	for _, codec := range []openapi.Codec{encoderCodec{}, crlfCodec{}} {
		c := parseFor(t, w, seqDoc(), &openapi.Options{Codecs: map[string]openapi.Codec{"application/json": codec}})
		mustCall(t, c, "jsonl", &openapi.Input{Body: items}, nil)
		wantLines(t, w.last(t).Body, `{"a":1}`, `2`)
		mustCall(t, c, "seq", &openapi.Input{Body: items}, nil)
		if got := w.last(t); string(got.Body) != jsonSeq(`{"a":1}`, `2`) {
			t.Errorf("%T: json-seq body %q", codec, got.Body)
		}
	}
	c := parseFor(t, w, seqDoc(), &openapi.Options{Codecs: map[string]openapi.Codec{"application/json": indentCodec{}}})
	before := w.count()
	resp, err := c.Call(t.Context(), "jsonl", &openapi.Input{Body: items}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/0")
	if e := re.Inputs["Input.Body/0"]; e == nil || !strings.Contains(e.Error(), "codec") {
		t.Errorf("the refusal %v does not name the codec's output", e)
	}
	mustCall(t, c, "seq", &openapi.Input{Body: items}, nil)
	if got := w.last(t); string(got.Body) != jsonSeq("{\n  \"a\": 1\n}", "2") {
		t.Errorf("json-seq body %q, want the indented item framed", got.Body)
	}
}

// A part typed application/x-www-form-urlencoded is encoded by the
// client's form encoder, as a form body or content parameter is, from the
// part's object and its schema's properties (Options.Codecs: Load refuses a
// key naming application/x-www-form-urlencoded, "whose framing and field
// encoding stay the client's").
func TestF20FormTypedPart(t *testing.T) {
	doc := doc31(`"/p":{"post":{"operationId":"formPart","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"a":{"type":"object","properties":{"k":{"type":"string"},"n":{"type":"integer"}}}}},
		"encoding":{"a":{"contentType":"application/x-www-form-urlencoded"}}}}}}}`)
	w := newWire(t, nil)
	c := parseFor(t, w, doc, nil)
	_, parts := sendMultipart(t, w, c, "formPart", "multipart/form-data", map[string]any{"a": map[string]any{"k": "v w&", "n": 1}})
	checkParts(t, parts, []wantPart{{disposition: formData("a"), ctype: "application/x-www-form-urlencoded", content: "k=v+w%26&n=1"}})
}

// client.go, Input.Body: "A multipart object with no fields sends the close
// delimiter alone ("--" boundary "--" CRLF), as browsers do" (the WHATWG
// form-data algorithm, the body starting at the dash-boundary as RFC 2046's
// multipart-body does): an empty object, one whose every property is null,
// and a Part with nil Content (omitted like null) each send exactly "--B--"
// CRLF, which mime/multipart reads as no parts.
func TestF27EmptyMultipart(t *testing.T) {
	const mt = "multipart/form-data; boundary=B"
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	for _, body := range []map[string]any{{}, {"title": nil}, {"any": openapi.Part{MediaType: "text/plain"}}} {
		mustCall(t, c, "mp", &openapi.Input{Body: body, MediaType: mt}, nil)
		got := w.last(t)
		if b := string(got.Body); b != "--B--\r\n" {
			t.Errorf("%v: body %q, want %q", body, b, "--B--\r\n")
		}
		if _, _, parts := readMultipart(t, mt, got.Body); len(parts) != 0 {
			t.Errorf("%v: %d parts", body, len(parts))
		}
	}
}

// Part.Header is written canonical and sorted, after Content-Disposition
// and Content-Type: sorted by the canonical name written, whatever the
// caller's spelling.
func TestF30PartHeaderOrder(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, mpDoc(), nil)
	h := http.Header{"c-Z": {"3"}, "a-x": {"1"}, "B-y": {"2"}}
	mustCall(t, c, "mp", &openapi.Input{Body: map[string]any{"title": openapi.Part{Content: "v", Header: h}}, MediaType: "multipart/form-data; boundary=B"}, nil)
	body := string(w.last(t).Body)
	start := strings.Index(body, "--B\r\n")
	end := strings.Index(body, "\r\n\r\n")
	if start < 0 || end < start {
		t.Fatalf("no part head in %q", body)
	}
	lines := strings.Split(body[start+len("--B\r\n"):end], "\r\n")
	want := []string{`Content-Disposition: form-data; name="title"`, "Content-Type: text/plain", "A-X: 1", "B-Y: 2", "C-Z: 3"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("part head %q, want %q", lines, want)
	}
}

// Every Media a call can select has its compiled plan: a declared range that
// covers a form or multipart type compiles an encoding from its schema with
// no Encoding Object. A form or multipart type selected under */* or
// application/*, by Input.MediaType or Options.MediaType, encodes its body
// (its schema's types applying), never a panic (OAS 3.1.2 section 4.8.13: a
// content key may be a media type range; client.go, Input.MediaType: "a
// concrete type matching one the operation declares").
func TestC44RangesSelectFormAndMultipart(t *testing.T) {
	doc := doc31(`
		"/a":{"post":{"operationId":"appRange","requestBody":{"content":{"application/*":{"schema":{"type":"object","properties":{"x":{"type":"string"},"n":{"type":"integer"}}}}}}}},
		"/s":{"post":{"operationId":"anyRange","requestBody":{"content":{"*/*":{"schema":{"type":"object","properties":{"x":{"type":"string"},"n":{"type":"integer"}}}}}}}},
		"/m":{"post":{"operationId":"formDataAndAny","requestBody":{"content":{"multipart/form-data":{},"*/*":{}}}}}`)
	w := newWire(t, nil)
	c := parseFor(t, w, doc, nil)
	body := map[string]any{"x": "a b", "n": 5}
	for _, tt := range []struct {
		name, key, mediaType string
		opts                 string // Options.MediaType instead of Input.MediaType
		form                 bool
	}{
		{"form under application/*", "appRange", "application/x-www-form-urlencoded", "", true},
		{"form under */*", "anyRange", "application/x-www-form-urlencoded", "", true},
		{"multipart under */*", "anyRange", "multipart/form-data", "", false},
		{"Options.MediaType form under */*", "anyRange", "", "application/x-www-form-urlencoded", true},
		{"Options.MediaType multipart under */*", "anyRange", "", "multipart/form-data", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cl := c
			if tt.opts != "" {
				cl = c.With(func(o *openapi.Options) { o.MediaType = tt.opts })
			}
			noPanic(t, tt.name, func() {
				mustCall(t, cl, tt.key, &openapi.Input{Body: body, MediaType: tt.mediaType}, nil)
				got := w.last(t)
				if tt.form {
					if string(got.Body) != "n=5&x=a+b" {
						t.Errorf("form body %q, want n=5&x=a+b", got.Body)
					}
					return
				}
				_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
				checkParts(t, parts, []wantPart{
					{disposition: formData("n"), ctype: "text/plain", content: "5"},
					{disposition: formData("x"), ctype: "text/plain", content: "a b"},
				})
			})
		})
	}
	// multipart/mixed is covered by */* only; no schema: untyped fields.
	t.Run("multipart/mixed under */* beside multipart/form-data", func(t *testing.T) {
		noPanic(t, "multipart/mixed", func() {
			mustCall(t, c, "formDataAndAny", &openapi.Input{Body: map[string]any{"x": "y"}, MediaType: "multipart/mixed"}, nil)
			got := w.last(t)
			_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
			checkParts(t, parts, []wantPart{{disposition: formData("x"), ctype: "application/octet-stream", content: "y"}})
		})
	})
}
