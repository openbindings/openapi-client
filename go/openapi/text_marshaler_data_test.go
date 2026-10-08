package openapi_test

import (
	"net"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// commaList is a slice that encoding/json writes by its MarshalText, as one
// string of its items joined by commas.
type commaList []string

func (l commaList) MarshalText() ([]byte, error) { return []byte(strings.Join(l, ",")), nil }

// textValues are values encoding/json writes by their MarshalText, each with
// the string that is its JSON data: a net.IP, a named byte slice, and a
// commaList.
var textValues = []struct {
	name string
	v    any
	s    string
}{
	{"net.IP", net.IP{192, 0, 2, 1}, "192.0.2.1"},
	{"commaList", commaList{"a", "b"}, "a,b"},
}

// textMarshalerDoc is an OpenAPI 3.2 document whose operations take a
// positional multipart/mixed body whose first part is text/plain, by its
// string schema, application/json, or multipart/mixed; a positional
// multipart/form-data body; and a JSON Lines or JSON text sequence body.
const textMarshalerDoc = `
	"/text":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array","prefixItems":[{"type":"string"}]}}}}}},
	"/json":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"prefixEncoding":[{"contentType":"application/json"}]}}}}},
	"/mixed":{"post":{"requestBody":{"content":{"multipart/mixed":{"schema":{"type":"array"},"prefixEncoding":[{"contentType":"multipart/mixed"}]}}}}},
	"/form":{"post":{"requestBody":{"content":{"multipart/form-data":{"schema":{"type":"array"}}}}}},
	"/seq":{"post":{"requestBody":{"content":{"application/jsonl":{},"application/json-seq":{}}}}}`

// prepared returns what Prepare makes of in for key: a refusal's error text
// and its *RequestError, or a multipart body's parts as partTree renders them,
// or the Content-Type and bytes of any other body.
func prepared(t *testing.T, c *openapi.Client, key string, in *openapi.Input) (string, *openapi.RequestError) {
	t.Helper()
	req, err := c.Prepare(key, in)
	if err != nil {
		return "refused: " + err.Error(), asRequestError(t, err)
	}
	ct, body := req.HTTP.Header.Get("Content-Type"), editionBody(t, req)
	if strings.HasPrefix(ct, "multipart/") {
		return partTree(t, ct, body, ""), nil
	}
	return ct + "\n" + string(body), nil
}

// doc.go, Values: "The client first converts a value to JSON data as
// encoding/json would ..., then serializes that data as the document says", and
// "The JSON data of a value encoding/json writes by its MarshalText, such as a
// net.IP, is a string, whatever its Go kind"; client.go, Input.Body: "Any other
// type, a named byte-slice type included, is a value for the codec", and in a
// positional multipart body each element is "a []byte, an io.Reader, a Part, or
// a value encoded by that part's media type"; "A part whose media type is
// multipart is encoded, one level deep, from an object or list"; "For form and
// multipart media, Body is an object whose properties are the fields", and in
// OpenAPI 3.2 "may instead be a list". So a net.IP and a commaList are sent, or
// refused, exactly as their string, the control, is: as a positional element,
// one text/plain part of the string, one application/json part of it as a JSON
// string, and a multipart part refused at its place; as a whole positional
// multipart/mixed or multipart/form-data body, neither an object nor a slice,
// refused at Input.Body.
func TestTextMarshalerPartIsOneString(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", textMarshalerDoc), nil)
	for _, tv := range textValues {
		t.Run(tv.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, key string
				body      func(any) any
				want      string // the control's parts, or the Inputs key that refuses it
			}{
				{"text/plain element", "POST /text", func(v any) any { return []any{v} }, "- | text/plain | " + tv.s + "\n"},
				{"application/json element", "POST /json", func(v any) any { return []any{v} }, `- | application/json | "` + tv.s + `"` + "\n"},
				{"multipart/mixed element", "POST /mixed", func(v any) any { return []any{v} }, "Input.Body/0"},
				{"multipart/mixed body", "POST /text", func(v any) any { return v }, "Input.Body"},
				{"multipart/form-data body", "POST /form", func(v any) any { return v }, "Input.Body"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					control, re := prepared(t, c, tc.key, &openapi.Input{Body: tc.body(tv.s)})
					switch {
					case !strings.HasPrefix(tc.want, "Input.Body"):
						if control != tc.want {
							t.Fatalf("the string gives\n%s\nwant\n%s", control, tc.want)
						}
					case re == nil:
						t.Fatalf("the string gives\n%s\nwant a refusal at %s", control, tc.want)
					default:
						wantKeys(t, "Inputs", re.Inputs, true, tc.want)
					}
					if got, _ := prepared(t, c, tc.key, &openapi.Input{Body: tc.body(tv.v)}); got != control {
						t.Errorf("%T gives\n%s\nwant, as the string %q gives,\n%s", tv.v, got, tv.s, control)
					}
				})
			}
		})
	}
}

// doc.go, Values: "The JSON data of a value encoding/json writes by its
// MarshalText, such as a net.IP, is a string, whatever its Go kind"; client.go,
// Input.Body: "For a sequential media type ..., Body is a list, an iter.Seq,
// or an iter.Seq2 whose second value is an error, of any element type; each
// element is one item, encoded as that value on its own would be". So a net.IP
// and a commaList are sent, or refused, exactly as their string, the control,
// is: as a JSON Lines or JSON text sequence body, which is not a slice of
// items, refused at Input.Body; as an item, one JSON string.
func TestTextMarshalerSequentialIsOneString(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", textMarshalerDoc), nil)
	for _, tv := range textValues {
		for _, media := range []string{"application/jsonl", "application/json-seq"} {
			t.Run(tv.name+"/"+media, func(t *testing.T) {
				want, re := prepared(t, c, "POST /seq", &openapi.Input{MediaType: media, Body: tv.s})
				if re == nil {
					t.Fatalf("the string as the body gives\n%s\nwant a refusal at Input.Body", want)
				}
				wantKeys(t, "Inputs", re.Inputs, true, "Input.Body")
				if got, _ := prepared(t, c, "POST /seq", &openapi.Input{MediaType: media, Body: tv.v}); got != want {
					t.Errorf("as the body, %T gives\n%s\nwant, as the string %q gives,\n%s", tv.v, got, tv.s, want)
				}
				frame := func(item string) string { return item + "\n" }
				if media == "application/json-seq" {
					frame = func(item string) string { return "\x1e" + item + "\n" }
				}
				want, _ = prepared(t, c, "POST /seq", &openapi.Input{MediaType: media, Body: []any{tv.s, "after"}})
				if items := media + "\n" + frame(`"`+tv.s+`"`) + frame(`"after"`); want != items {
					t.Fatalf("the string as an item gives\n%q\nwant\n%q", want, items)
				}
				if got, _ := prepared(t, c, "POST /seq", &openapi.Input{MediaType: media, Body: []any{tv.v, "after"}}); got != want {
					t.Errorf("as an item, %T gives\n%s\nwant, as the string %q gives,\n%s", tv.v, got, tv.s, want)
				}
			})
		}
	}
}
