package openapi_test

import (
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Text and other media types. doc.go, Values: "a type with a caller's codec
// takes a value of any Go type, which that codec encodes; a JSON type is
// written as encoding/json writes the value; and any other type takes only a
// string, as its UTF-8 bytes, and a text type also a number or boolean, in
// its JSON spelling"; "The client first converts a value to JSON data as
// encoding/json would (struct tags, MarshalJSON, TextMarshaler map keys)".
// So XML without a caller's codec takes a string. doc.go, Fixed rules,
// Header fields: the client generates Content-Type, and Content-Length for a
// body that can be sent again, which every value it encodes can (client.go,
// Input.Body).

const textDoc = `
	"/text":{"post":{"operationId":"text","requestBody":{"content":{"text/plain":{}}}}},
	"/csv":{"post":{"operationId":"csv","requestBody":{"content":{"text/csv; charset=utf-8":{}}}}},
	"/octets":{"post":{"operationId":"octets","requestBody":{"content":{"application/octet-stream":{}}}}},
	"/png":{"post":{"operationId":"png","requestBody":{"content":{"image/png":{}}}}},
	"/xml":{"post":{"operationId":"xml","requestBody":{"content":{"application/xml":{}}}}},
	"/textxml":{"post":{"operationId":"textXML","requestBody":{"content":{"text/xml":{}}}}},
	"/atom":{"post":{"operationId":"atom","requestBody":{"content":{"application/atom+xml":{}}}}},
	"/html":{"post":{"operationId":"html","requestBody":{"content":{"text/html":{}}}}}`

// textMarshaler is encoded by encoding/json as the string its MarshalText
// returns.
type textMarshaler struct{ s string }

func (m textMarshaler) MarshalText() ([]byte, error) { return []byte(m.s), nil }

// A text type takes a string, a number or a boolean: the value is first
// converted to JSON data as encoding/json would, so a pointer to a string,
// a MarshalJSON or MarshalText result that is a JSON string, and a
// json.Number are text too. A string is sent as its bytes, never escaped
// (doc.go, Values: "a string or json.Number as it is"); a number in its
// JSON spelling as encoding/json writes it (1e21 is 1e+21).
func TestTextBodies(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(textDoc), nil)
	s := "pointed"
	tests := []struct {
		name, key string
		body      any
		want, ct  string
	}{
		{"string", "text", "hello, world", "hello, world", "text/plain"},
		{"empty string", "text", "", "", "text/plain"},
		{"HTML characters as given", "text", `<a href="x">&amp;</a>`, `<a href="x">&amp;</a>`, "text/plain"},
		{"non-ASCII", "text", "é ☃\n", "é ☃\n", "text/plain"},
		{"integer", "text", 42, "42", "text/plain"},
		{"negative float", "text", -2.5, "-2.5", "text/plain"},
		{"exponent", "text", 1e21, "1e+21", "text/plain"},
		{"boolean", "text", false, "false", "text/plain"},
		{"json.Number as it is", "text", json.Number("12.50"), "12.50", "text/plain"},
		{"pointer to string", "text", &s, "pointed", "text/plain"},
		{"MarshalJSON string", "text", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), "2026-10-01T12:00:00Z", "text/plain"},
		{"MarshalText", "text", textMarshaler{"as text"}, "as text", "text/plain"},
		{"TextMarshaler net.IP", "text", net.IPv4(192, 0, 2, 1), "192.0.2.1", "text/plain"},
		{"named string type", "text", label("named"), "named", "text/plain"},
		{"declared parameters sent", "csv", "a,b\n1,2\n", "a,b\n1,2\n", "text/csv; charset=utf-8"},
		{"text/html", "html", 7, "7", "text/html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, c, tt.key, &openapi.Input{Body: tt.body}, nil)
			got := w.last(t)
			if string(got.Body) != tt.want {
				t.Errorf("body %q, want %q", got.Body, tt.want)
			}
			if ct := got.Header.Values("Content-Type"); !slices.Equal(ct, []string{tt.ct}) {
				t.Errorf("Content-Type = %q, want %q", ct, tt.ct)
			}
			if got.ContentLength != int64(len(tt.want)) || len(got.TransferEncoding) != 0 {
				t.Errorf("Content-Length %d, Transfer-Encoding %q, for %d bytes", got.ContentLength, got.TransferEncoding, len(tt.want))
			}
		})
	}
}

// An invalid-UTF-8 string under a non-JSON content type is sent as its
// bytes as given (a content parameter "is encoded as a body of its media
// type is"; doc.go, Values: "a string ... as it is").
func TestTextBodyInvalidUTF8(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(textDoc), nil)
	for _, key := range []string{"text", "octets"} {
		mustCall(t, c, key, &openapi.Input{Body: "a\xffb\xfe"}, nil)
		if got := w.last(t); string(got.Body) != "a\xffb\xfe" {
			t.Errorf("%s: body %q, want the bytes as given", key, got.Body)
		}
	}
}

// Any other type without a codec takes only a string, as its UTF-8 bytes;
// XML without a codec, application/xml, text/xml and +xml alike, takes a
// string; every other value is refused at Inputs["Input.Body"] (errors.go,
// RequestError.Inputs: "a body the operation does not take" ... "a reader or
// Part where the media type cannot carry one"). A text type takes no array,
// object or null (a typed nil is null: doc.go, Values).
func TestOtherTypeBodies(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(textDoc), nil)
	for _, tt := range []struct{ key, body, ct string }{
		{"octets", "raw text", "application/octet-stream"},
		{"png", "\x89PNG\r\n", "image/png"},
		{"xml", "<pet><name>Rex</name></pet>", "application/xml"},
		{"textXML", "<a/>", "text/xml"},
		{"atom", "<feed/>", "application/atom+xml"},
	} {
		mustCall(t, c, tt.key, &openapi.Input{Body: tt.body}, nil)
		got := w.last(t)
		if string(got.Body) != tt.body || got.Header.Get("Content-Type") != tt.ct || got.ContentLength != int64(len(tt.body)) {
			t.Errorf("%s: sent %q as %q (Content-Length %d), want %q as %q", tt.key, got.Body, got.Header.Get("Content-Type"), got.ContentLength, tt.body, tt.ct)
		}
	}
	for _, tt := range []struct {
		key  string
		body any
	}{
		{"octets", 5},
		{"octets", true},
		{"octets", map[string]string{"a": "b"}},
		{"octets", []string{"a"}},
		{"png", json.Number("1")},
		{"xml", xmlPet{Name: "Rex"}},
		{"xml", 5},
		// text/xml is XML class, the classes being taken in order (doc.go,
		// Values: "taking the first that applies").
		{"textXML", 5},
		{"textXML", true},
		{"atom", map[string]any{"feed": "x"}},
		{"text", map[string]string{"a": "b"}},
		{"text", []any{1, 2}},
		{"text", (*string)(nil)},
		{"text", map[string]any(nil)},
		{"text", openapi.Part{Content: "x"}},
		{"html", struct{ A int }{1}},
	} {
		t.Run(fmt.Sprintf("%s %T", tt.key, tt.body), func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), tt.key, &openapi.Input{Body: tt.body}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, "Input.Body")
		})
	}
}

// client.go, Options.Codecs: a codec "applies wherever the client encodes
// ... a value of its type: request bodies"; doc.go, Values: "a type with a
// caller's codec takes a value of any Go type, which that codec encodes",
// receiving "the value as given". Under XML with a codec, a struct is
// encoded by it; under text/plain with a codec, so is a map.
func TestCodecTextAndXMLBodies(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(textDoc), &openapi.Options{Codecs: map[string]openapi.Codec{
		"application/xml": tagCodec{tag: "XML"},
		"+xml":            tagCodec{tag: "SUFFIX"},
		"text/plain":      tagCodec{tag: "TXT"},
	}})
	for _, tt := range []struct {
		key  string
		body any
		want string
	}{
		{"xml", xmlPet{Name: "Rex"}, "XML({{ } Rex})"},
		{"atom", 5, "SUFFIX(5)"},
		{"text", map[string]int{"a": 1}, "TXT(map[a:1])"},
		{"text", "s", "TXT(s)"},
	} {
		mustCall(t, c, tt.key, &openapi.Input{Body: tt.body}, nil)
		if got := w.last(t); string(got.Body) != tt.want {
			t.Errorf("%s %T: body %q, want %q", tt.key, tt.body, got.Body, tt.want)
		}
	}
}
