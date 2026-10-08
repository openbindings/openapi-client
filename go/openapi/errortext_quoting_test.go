package openapi_test

import (
	"context"
	"io"
	"iter"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openbindings/openapi-client/go/openapi"
)

// wantQuotedText fails unless text, an error's text, holds only printable
// characters and holds each of escapes, the spelling strconv.Quote gives the
// characters a test put into a document or a caller's value.
func wantQuotedText(t testing.TB, what, text string, escapes ...string) {
	t.Helper()
	if !utf8.ValidString(text) || strings.ContainsFunc(text, func(r rune) bool { return !strconv.IsPrint(r) }) {
		t.Errorf("%s: text %q holds a character that is not printable", what, text)
	}
	for _, e := range escapes {
		if !strings.Contains(text, e) {
			t.Errorf("%s: text %q does not hold %s, as strconv.Quote writes it", what, text, e)
		}
	}
}

// doc.go, Outcomes: "Text such an error takes from the document, a caller's
// value or a server, a media type and a YAML tag included, is quoted, as
// strconv.Quote quotes it, whenever it holds invalid UTF-8 or a character that
// strconv.IsPrint reports is not printable, such as a line feed or an escape,
// so that it cannot forge a line of a log or reach a terminal as control
// codes." A YAML tag is percent-decoded (YAML 1.2.2 section 6.8.2), so a
// document can spell a line feed and an escape in one; the rejection that
// names the tag (load.go, Loader: a document "rejects the document, and so,
// in every edition, does a value JSON cannot hold (.inf, .nan, or a tag
// outside the Core schema, such as !!timestamp)") quotes it.
func TestYAMLTagQuotedInError(t *testing.T) {
	doc := "openapi: 3.1.0\ninfo: {title: t, version: '1'}\npaths: {}\nx-a: !e%0AFAKE%1B[31m value\n"
	_, err := openapi.Parse(context.Background(), []byte(doc), testDocURI, nil)
	if err == nil {
		t.Fatal("Parse accepted a tag outside the Core schema")
	}
	wantQuotedText(t, "YAML tag", err.Error(), strconv.Quote("!e\nFAKE\x1b[31m"))
}

// doc.go, Outcomes, as for TestYAMLTagQuotedInError, for the media types an
// error takes from a caller's value or the document: an Input.MediaType the
// operation does not declare, an Options.MediaType no operation declares
// (client.go, Options.MediaType: "Load refuses a type that no such Media of
// any operation matches"), and an Encoding contentType list that requires
// Part.MediaType (doc.go, Configuration: "a list or a range requires
// Part.MediaType"). A line feed or an escape cannot reach these errors, since a
// media type that holds one does not parse (RFC 9110 section 5.6.4 admits only
// a tab, a space, visible characters and obs-text in a quoted-string), so the
// tab and the C1 controls U+0085 and U+009B, which do parse, stand for them.
func TestMediaTypeQuotedInError(t *testing.T) {
	const tab = "text/csv;\tx=1"
	const nel = "text/csv; x=\"\u0085\""
	const csi = "text/csv; x=\"\u009b[31m\""
	doc := doc31(`"/x":{"post":{"operationId":"x","requestBody":{"content":{"application/json":{}}}}},
		"/f":{"post":{"operationId":"f","requestBody":{"content":{"multipart/form-data":{
			"schema":{"type":"object","properties":{"f":{}}},
			"encoding":{"f":{"contentType":"text/plain;\tcharset=utf-8, text/csv; x=\"\u0085\", image/png; x=\"\u009b[31m\""}}}}}}}`)
	c := parseAt(t, doc, "https://api.example.test", testDocURI, nil)

	for _, mt := range []string{tab, nel, csi} {
		_, err := c.Prepare("x", &openapi.Input{Body: []byte("a"), MediaType: mt})
		re := asRequestError(t, err)
		wantKeys(t, "Settings", re.Settings, true, "Input.MediaType")
		escape := strconv.Quote(mt)
		escape = escape[1 : len(escape)-1]
		wantQuotedText(t, "Input.MediaType "+strconv.Quote(mt), err.Error(), escape)

		_, err = openapi.Parse(context.Background(), []byte(expand(doc, "https://api.example.test")), testDocURI, &openapi.Options{MediaType: mt})
		re = asRequestError(t, err)
		wantKeys(t, "Settings", re.Settings, true, "Options.MediaType")
		wantQuotedText(t, "Options.MediaType "+strconv.Quote(mt), err.Error(), escape)
	}

	_, err := c.Prepare("f", &openapi.Input{Body: map[string]any{"f": "abc"}})
	re := asRequestError(t, err)
	wantKeys(t, "Settings", re.Settings, true, "Input.Body/f")
	wantQuotedText(t, "Encoding contentType", err.Error(), `\t`, `\u0085`, `\u009b`)
}

// doc.go, Outcomes, as for TestYAMLTagQuotedInError, for the place in an
// iterator's item that a body abort names (client.go, Input.Body: an item
// that cannot be encoded "aborts the body"): a map key of the caller's value
// holding a line feed and an escape is quoted there as it is in an Inputs key
// (errors.go, RequestError.Inputs) when a slice gives the same item.
func TestIteratorItemPlaceQuotedInError(t *testing.T) {
	c := editionClient(t, editionPost("3.1.0", "application/jsonl", `{"type":"array"}`), nil)
	item := map[string]any{"k\x1b[31m\nFAKE": strings.NewReader("x")}
	_, err := c.Prepare("POST /x", &openapi.Input{Body: []any{item}})
	wantQuotedText(t, "slice item", asRequestError(t, err).Error(), `\x1b`, `\n`)

	seq := iter.Seq[any](func(yield func(any) bool) { yield(item) })
	req := mustPrepare(t, c, "POST /x", &openapi.Input{Body: seq})
	_, err = io.ReadAll(req.HTTP.Body)
	if err == nil {
		t.Fatal("an iterator's item holding a reader was sent")
	}
	wantQuotedText(t, "iterator item", err.Error(), `\x1b`, `\n`)
}
