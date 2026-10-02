package openapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 4 fuzz targets (stage brief, Tests: "a form body target comparing
// the client's encoding with a reference built from net/url and the RFC
// 6570 oracle in the suite; a multipart round trip target through
// mime/multipart"). Both prepare the body and read it from GetBody, so they
// send nothing. A string that is not valid UTF-8 is sent as given in a field
// serialized by a content type that is not JSON (stage 4 ledger, Q9), and as
// JSON data holds it, each invalid byte U+FFFD, in a JSON field or one
// written by a style (ledger, QQ1), which is what json.Marshal and the
// oracle's jsonData give. Property names stay valid.

const fuzzFormDoc = `{"openapi":"3.1.0","info":{"title":"f","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{
	"/f":{"post":{"operationId":"f","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"c":{"type":"string"},"j":{"type":"object"},"l":{"type":"array","items":{"type":"string"}}}},
		"encoding":{
			"sf":{"style":"form","explode":false},
			"se":{"style":"form","explode":true},
			"sr":{"allowReserved":true},
			"sd":{"style":"deepObject"},
			"ss":{"style":"spaceDelimited"},
			"sp":{"style":"pipeDelimited","explode":false,"allowReserved":true}}}}}}}}}`

// fuzzStyled is each styled field of fuzzFormDoc: its RFC 6570 style,
// explode and allowReserved.
var fuzzStyled = map[string]struct {
	style             string
	explode, reserved bool
}{
	"sf": {"form", false, false},
	"se": {"form", true, false},
	"sr": {"form", true, true},
	"sd": {"deepObject", true, false},
	"ss": {"spaceDelimited", false, false},
	"sp": {"pipeDelimited", false, true},
}

// FuzzFormBody: for arbitrary strings, a form body of a text/plain field, a
// JSON field, an array field, a field under every RFC 6570 configuration
// and a field with an arbitrary name is prepared, and its bytes are compared with what formEnc (net/url, adjusted
// to the WHATWG set and checked against it) and styledField (the RFC 6570
// oracle) derive, the fields in sorted key order (doc.go, Fixed rules, Order
// and Form bodies).
func FuzzFormBody(f *testing.F) {
	c, err := openapi.Parse(context.Background(), []byte(fuzzFormDoc), fuzzURI, nil)
	if err != nil {
		f.Fatalf("Parse: %v", err)
	}
	for _, s := range [][6]string{
		{"blue", "black", "R", "100", "G", "200"},
		{"", "", "", "", "a", ""},
		{"a b+c", "d&e=f", "g~h", "i*j", "k/l", "m%n"},
		{"é", "日本", "ü", "ß", "\u2028", "\U0001F600"},
		{"\x00", "\r\n", " x", "x ", "\t", "\x7f"},
		{"a\xffb", "\xfe", "k\xff", "\xc3", "k\xfe", "n"},
		{"~-._", "!*'()", "$@:", "|^`", "\"<>\\", "{}[]#?"},
		{"%41", "%zz", "%", "%2f", "[x]", "#?"},
	} {
		f.Add(s[0], s[1], s[2], s[3], s[4], s[5], s[0]+"\xff"+s[1])
	}
	f.Fuzz(func(t *testing.T, a, b, k1, v1, k2, n, x string) {
		if !utf8.ValidString(n) || k1 != k2 && asJSON(k1) == asJSON(k2) {
			t.Skip() // a name kept valid; two members that JSON data would merge
		}
		body := map[string]any{
			"c":  x,
			"j":  map[string]string{k1: v1},
			"l":  []string{a, b},
			"sf": []string{a, b},
			"se": map[string]string{k1: v1, k2: b},
			"sr": a,
			"sd": map[string]string{k1: v1, k2: b},
			"ss": []string{a, b},
			"sp": []string{a, b},
		}
		if _, fixed := body[n]; !fixed {
			body[n] = v1
		}
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		slices.Sort(keys) // encoding/json's order for map keys
		var fields []string
		for _, k := range keys {
			switch cfg, styled := fuzzStyled[k]; {
			case styled:
				s, o := styledField(t, k, cfg.style, cfg.explode, cfg.reserved, body[k])
				if o == refused {
					t.Fatalf("the oracle refuses %s = %#v", k, body[k])
				}
				fields = append(fields, s)
			case k == "j":
				j, err := json.Marshal(body[k])
				if err != nil {
					t.Fatal(err)
				}
				fields = append(fields, "j="+formEnc(string(j)))
			case k == "l":
				fields = append(fields, formPairs("l", a, "l", b))
			default:
				fields = append(fields, formPairs(k, body[k].(string)))
			}
		}
		want := formJoin(fields...)
		req, err := c.Prepare("f", &openapi.Input{Body: body})
		if err != nil {
			t.Fatalf("Prepare(%#v): %v", body, err)
		}
		if got := string(preparedBody(t, req)); got != want {
			t.Fatalf("body for %#v:\n%q\nwant\n%q", body, got, want)
		}
	})
}

const fuzzMultipartDoc = `{"openapi":"3.1.0","info":{"title":"f","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{
	"/m":{"post":{"operationId":"m","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"t":{"type":"string"}}}}}}}}}}`

// fuzzPartTypes are the media types a fuzzed part is given.
var fuzzPartTypes = []string{"application/octet-stream", "image/png", "text/plain; charset=utf-8", "application/vnd.x+json", "multipart/mixed; boundary=inner"}

// controlChar reports a control character other than a tab, or DEL, which
// a quoted-string cannot carry (RFC 9110 section 5.6.4) and client.go,
// Part, refuses in a name or filename.
func controlChar(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 && r != '\t' || r == 0x7f })
}

// FuzzMultipartRoundTrip: an arbitrary part (property name, filename, bytes,
// media type, NoFilename) and a text field are prepared as
// multipart/form-data, and read back with mime/multipart: each part's
// Content-Disposition is formData's (client.go, Part: "written in its
// Content-Disposition as given, each as a quoted-string with \ and "
// escaped, never as filename*"; Filename "Empty means the default: the
// part's name for a []byte or io.Reader Content whose media type is not
// multipart, and none otherwise"), its Content-Type the one given or the
// schema's (text/plain for t), its content the bytes as given (doc.go, Fixed
// rules, Form bodies: "Multipart/form-data fields are never URI
// percent-encoded"; stage 4 ledger, Q9, for a text value that is not valid
// UTF-8). A control character other than a tab in the name or filename
// (client.go, Part: "a control character other than a tab in either is
// refused"; ledger, Q11, DEL included), or Filename with NoFilename, is
// refused at the property's Inputs key.
func FuzzMultipartRoundTrip(f *testing.F) {
	c, err := openapi.Parse(context.Background(), []byte(fuzzMultipartDoc), fuzzURI, nil)
	if err != nil {
		f.Fatalf("Parse: %v", err)
	}
	f.Add("file", "q3.pdf", []byte("%PDF-1.7"), "Q3", uint8(0), false)
	f.Add(`a"b\c`, `d"e\f.txt`, []byte("--x\r\n\r\n--x--"), "é\r\n", uint8(2), false)
	f.Add("x/y~z", "", []byte{0, 0xff}, "", uint8(1), true)
	f.Add("a\rb", "f", []byte("v"), "t", uint8(3), false)
	f.Add("n", "f\nx", []byte("v"), "t", uint8(4), false)
	f.Add("n", "f", []byte("v"), "t", uint8(0), true)
	f.Add("ü 日本", "../../etc/passwd", []byte("\r\n--"), "--", uint8(2), false)
	f.Add("a\x00b", "f", []byte("v"), "\xff", uint8(0), false)
	f.Add("n", "f\x7f", []byte("v"), "t", uint8(1), false)
	f.Add("a\tb", "c\td", []byte("v"), "t", uint8(4), false)
	f.Add("nested", "", []byte("--inner--"), "t", uint8(4), false)
	f.Fuzz(func(t *testing.T, name, filename string, content []byte, text string, ct uint8, noFilename bool) {
		if name == "t" || !utf8.ValidString(name) || !utf8.ValidString(filename) {
			t.Skip()
		}
		mt := fuzzPartTypes[int(ct)%len(fuzzPartTypes)]
		part := openapi.Part{Content: content, MediaType: mt, Filename: filename, NoFilename: noFilename}
		req, err := c.Prepare("m", &openapi.Input{Body: map[string]any{name: part, "t": text}})
		key := "Input.Body/" + jsonPtr(name)
		if controlChar(name) || controlChar(filename) || noFilename && filename != "" {
			var re *openapi.RequestError
			if !errors.As(err, &re) || len(re.Inputs) != 1 || re.Inputs[key] == nil {
				t.Fatalf("Prepare(%q, filename %q, NoFilename %t) = %v, want a refusal at Inputs[%q]", name, filename, noFilename, err, key)
			}
			return
		}
		multi := strings.HasPrefix(mt, "multipart/")
		if name == "" && filename == "" && !noFilename && !multi {
			return // the default filename would be the empty name: not settled
		}
		if err != nil {
			t.Fatalf("Prepare(%q, filename %q): %v", name, filename, err)
		}
		body := preparedBody(t, req)
		if req.HTTP.ContentLength != int64(len(body)) {
			t.Errorf("ContentLength %d for %d bytes", req.HTTP.ContentLength, len(body))
		}
		disposition := formData(name)
		switch {
		case noFilename:
		case filename != "":
			disposition = formData(name, filename)
		case !multi:
			disposition = formData(name, name)
		}
		file := wantPart{disposition: disposition, ctype: mt, content: string(content)}
		textPart := wantPart{disposition: formData("t"), ctype: "text/plain", content: text}
		want := []wantPart{file, textPart}
		if "t" < name {
			want = []wantPart{textPart, file}
		}
		_, _, parts := readMultipart(t, req.HTTP.Header.Get("Content-Type"), body)
		checkParts(t, parts, want)
	})
}
