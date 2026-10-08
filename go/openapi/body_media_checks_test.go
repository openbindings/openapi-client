package openapi_test

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for media type selection, refusal text, sequential
// items, short replayable sources and response media.

// JSON Schema 2020-12 Validation section 6.1.1 makes an integer a number
// with a zero fractional part, so number allows every integer, and
// intersecting number with integer gives integer (OAS 3.1.2 section 4.4,
// Data Types: "Since there is no distinct JSON integer type, JSON Schema
// defines integers mathematically"; section 4.4.1: "both type: number and
// type: integer are considered to be numbers in the data model"). A property
// whose schemas allow number and integer, by a $ref with a sibling type, by
// allOf, by a type array beside allOf, and by two allOf members each
// declaring it, is text/plain (OAS 3.1.2 section 4.8.15.1.1: a primitive's
// default), under form and multipart, and 5 is sent. Before the fix the two
// types did not meet: application/octet-stream, which refuses 5.
func TestFieldNumberWithIntegerIsInteger(t *testing.T) {
	schema := `{"type":"object","properties":{
		"r":{"$ref":"#/components/schemas/Num","type":"integer"},
		"a":{"allOf":[{"type":"number"},{"type":"integer"}]},
		"m":{"type":["number","string"],"allOf":[{"type":"integer"}]}},
		"allOf":[{"properties":{"n":{"type":"number"}}},{"properties":{"n":{"type":"integer"}}}]}`
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/f":{"post":{"operationId":"numbers","requestBody":{"content":{
		"application/x-www-form-urlencoded":{"schema":`+schema+`},"multipart/form-data":{"schema":`+schema+`}}}}}`,
		`"components":{"schemas":{"Num":{"type":"number"}}}`), nil)
	op := mustOp(t, c, "numbers")
	for i := range 2 {
		m := reqMedia(t, op, i)
		if names := encodingNames(m); !slices.Equal(names, []string{"r", "a", "m", "n"}) {
			t.Errorf("%s Encoding %q", m.Type, names)
		}
		for _, e := range m.Encoding {
			if e.ContentType != "text/plain" || e.Err != nil {
				t.Errorf("%s %s: ContentType %q, Err %v; want text/plain", m.Type, e.Name, e.ContentType, e.Err)
			}
		}
	}
	body := map[string]any{"r": 5, "a": 5, "m": 5, "n": 5}
	mustCall(t, c, "numbers", &openapi.Input{Body: body, MediaType: "application/x-www-form-urlencoded"}, nil)
	if got := w.last(t); string(got.Body) != "a=5&m=5&n=5&r=5" {
		t.Errorf("form body %q, want a=5&m=5&n=5&r=5", got.Body)
	}
	mustCall(t, c, "numbers", &openapi.Input{Body: body, MediaType: "multipart/form-data"}, nil)
	got := w.last(t)
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	var want []wantPart
	for _, name := range []string{"a", "m", "n", "r"} {
		want = append(want, wantPart{disposition: formData(name), ctype: "text/plain", content: "5"})
	}
	checkParts(t, parts, want)
}

// Regression check, not contract: the refusal's text names the Media's
// Source, or, when nothing covers the type, the type as given, parameters
// included. The rest is contract: the refusal is keyed Input.MediaType
// (errors.go, RequestError.Settings: a body's media type is keyed
// "Input.MediaType" when it "is declared under an invalid key (see
// Media.Err)"), and when the Media that would govern the call (the most
// specific declared key covering its type, or the sole declared one when none
// is given) has an Err, the refusal wraps that Err (client.go,
// Input.MediaType: "A boundary in a request body's declared content key is
// used and checked the same way; an invalid one is the Media's Err";
// describe.go, Operation.Err: a part's defect "fails a call only when the call
// uses it, the *RequestError then wrapping that part's Err"; OAS 3.1.2 section
// 4.8.13: "only the most specific key is applicable").
func TestDeclaredBoundaryRefusalNamesTheMedia(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`
		"/b":{"post":{"operationId":"bad","requestBody":{"content":{"multipart/form-data; boundary=\"a \"":{}}}}},
		"/t":{"post":{"operationId":"twice","requestBody":{"content":{"multipart/form-data; boundary=a; boundary=b":{}}}}},
		"/c":{"post":{"operationId":"beside","requestBody":{"content":{"multipart/form-data; boundary=\"a \"":{},"application/json":{}}}}}`), nil)
	refusal := func(t *testing.T, key, mt string) error {
		t.Helper()
		resp, err := c.Call(t.Context(), key, &openapi.Input{Body: map[string]any{"x": "y"}, MediaType: mt}, nil)
		re := refusedBeforeSending(t, w, resp, err)
		wantKeys(t, "Settings", re.Settings, true, "Input.MediaType")
		return re.Settings["Input.MediaType"]
	}
	for _, tt := range []struct{ key, mt string }{
		{"bad", ""},
		{"bad", `multipart/form-data; boundary="a "`},
		{"twice", ""},
		{"beside", `multipart/form-data; boundary="a "`},
	} {
		t.Run(fmt.Sprintf("%s %q", tt.key, tt.mt), func(t *testing.T) {
			md := reqMedia(t, mustOp(t, c, tt.key), 0)
			if md.Err == nil {
				t.Fatalf("Media %q has no Err", md.Type)
			}
			err := refusal(t, tt.key, tt.mt)
			if !errors.Is(err, md.Err) {
				t.Errorf("refusal %q does not wrap the Media's Err %q", err, md.Err)
			}
			if !strings.Contains(err.Error(), md.Source) {
				t.Errorf("refusal %q does not name the Media's Source %s", err, md.Source)
			}
		})
	}
	for _, mt := range []string{"multipart/form-data; boundary=ok", "multipart/mixed; boundary=x; charset=utf-8", "multipart/form-data"} {
		t.Run(fmt.Sprintf("uncovered %q", mt), func(t *testing.T) {
			if err := refusal(t, "bad", mt); !strings.Contains(err.Error(), mt) {
				t.Errorf("refusal %q does not name the type as given, %s", err, mt)
			}
		})
	}
}

// A style's compile error refuses a call only where the style applies
// (application/x-www-form-urlencoded and multipart/form-data calls); the
// descriptor's Param.Err for a range stays, as the range covers form-data
// calls (OAS 3.1.2 section 4.8.15.1.2, style and explode: "This field SHALL
// be ignored if the request body media type is not
// application/x-www-form-urlencoded or multipart/form-data"). Under a
// multipart/* key whose Encodings set a style invalid for a query value and
// a delimited style exploded, a multipart/mixed call sends the parts by
// their content type, and a multipart/form-data call is still refused.
// Before the fix the multipart/mixed call was refused too.
func TestRangeStyleErrorOnlyWhereStylesApply(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/m":{"post":{"operationId":"range","requestBody":{"content":{"multipart/*":{
		"schema":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"array","items":{"type":"string"}}}},
		"encoding":{"x":{"style":"matrix"},"y":{"style":"spaceDelimited","explode":true}}}}}}}`), nil)
	for _, e := range reqMedia(t, mustOp(t, c, "range"), 0).Encoding {
		if e.Err == nil {
			t.Errorf("descriptor %s has no Err for its style", e.Name)
		}
	}
	body := map[string]any{"x": "v", "y": []string{"a", "b"}}
	mustCall(t, c, "range", &openapi.Input{Body: body, MediaType: "multipart/mixed"}, nil)
	got := w.last(t)
	_, _, parts := readMultipart(t, got.Header.Get("Content-Type"), got.Body)
	checkParts(t, parts, []wantPart{
		{disposition: formData("x"), ctype: "text/plain", content: "v"},
		{disposition: formData("y"), ctype: "text/plain", content: "a"},
		{disposition: formData("y"), ctype: "text/plain", content: "b"},
	})
	before := w.count()
	resp, err := c.Call(t.Context(), "range", &openapi.Input{Body: body, MediaType: "multipart/form-data"}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantAnyKey(t, "Inputs", re.Inputs, "Input.Body/x")
	wantAnyKey(t, "Inputs", re.Inputs, "Input.Body/y", "Input.Body/y/0")
}

// Regression check, not contract: an event-stream refusal's text says what is
// wrong (a line break in event or id, a NUL in id, nesting past 1,000
// levels), in the documentation's own words for each problem. The rest is
// contract: each is refused at Input.Body/<i> (client.go, Input.Body: "a line
// break in event or id, a NUL in id ... is an item that cannot be encoded";
// doc.go, Values: a value whose JSON "nests deeper than 1,000 levels" is
// refused "at the key of the body, field, part, sequential item or parameter
// that is or holds it").
func TestEventStreamRefusalsNameTheProblem(t *testing.T) {
	_, c := walkClient(t)
	for _, tt := range []struct {
		name string
		item any
		says string
	}{
		{"Event with LF in Event", openapi.Event{Event: "a\nb"}, "line break"},
		{"Event with CR in Event", openapi.Event{Event: "a\rb"}, "line break"},
		{"*Event with LF in ID", &openapi.Event{ID: "a\nb", IDSet: true}, "line break"},
		{"Event with NUL in ID", openapi.Event{ID: "a\x00b", IDSet: true}, "NUL"},
		{"object with CR in event", map[string]any{"event": "a\rb"}, "line break"},
		{"object with LF in id", map[string]any{"id": "a\nb"}, "line break"},
		{"object with NUL in id", map[string]any{"data": "d", "id": "x\x00"}, "NUL"},
		{"object nested past 1,000 levels", map[string]any{"data": nestMap(1200, "x")}, "1,000"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.Prepare("sse", &openapi.Input{Body: []any{openapi.Event{Data: []byte("first")}, tt.item}})
			re := asRequestError(t, err)
			wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/1")
			if e := re.Inputs["Input.Body/1"]; e != nil && !strings.Contains(e.Error(), tt.says) {
				t.Errorf("refusal %q does not say %q", e, tt.says)
			}
		})
	}
}

// Element types with a pointer-receiver MarshalJSON, directly and one level
// inside.
type (
	ptrMarshalerElem   struct{ V int }
	ptrMarshalerHolder struct {
		P ptrMarshalerElem `json:"p"`
	}
)

func (*ptrMarshalerElem) MarshalJSON() ([]byte, error) { return []byte(`"ptr"`), nil }

// Under JSON Lines and JSON text sequences, a slice and an iterator of the
// same values send the same items (client.go, Input.Body: "each element is
// one item, encoded as that value on its own would be, so a slice and an
// iterator yielding the same values send the same bytes"), each
// json.Marshal of the item on its own. A pointer-receiver MarshalJSON of the
// element type does not apply to a slice's items, as it does not to an
// iterator's: a value whose pointer has MarshalJSON, directly or one level
// inside, is written by reflection, and a pointer to it by the method.
func TestSequentialSliceAndIteratorSendSameItems(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, doc31(`"/l":{"post":{"operationId":"jsonl","requestBody":{"content":{"application/jsonl":{}}}}},
		"/s":{"post":{"operationId":"jsonseq","requestBody":{"content":{"application/json-seq":{}}}}}`), nil)
	elems := []ptrMarshalerElem{{1}, {2}}
	holders := []ptrMarshalerHolder{{ptrMarshalerElem{3}}}
	pointers := []*ptrMarshalerElem{{4}}
	anys := []any{ptrMarshalerElem{5}, ptrMarshalerHolder{ptrMarshalerElem{6}}, &ptrMarshalerElem{7}}
	for _, tt := range []struct {
		name         string
		slice, items any
		values       []any
	}{
		{"elements", elems, seqOf(elems...), []any{elems[0], elems[1]}},
		{"holders", holders, seqOf(holders...), []any{holders[0]}},
		{"pointers", pointers, seqOf(pointers...), []any{pointers[0]}},
		{"any", anys, seqOf(anys...), anys},
	} {
		var lines []string
		for _, v := range tt.values {
			lines = append(lines, string(mustMarshal(t, v)))
		}
		for _, op := range []struct{ key, want string }{
			{"jsonl", strings.Join(lines, "\n") + "\n"},
			{"jsonseq", jsonSeq(lines...)},
		} {
			t.Run(tt.name+" "+op.key, func(t *testing.T) {
				mustCall(t, c, op.key, &openapi.Input{Body: tt.slice}, nil)
				fromSlice := string(w.last(t).Body)
				mustCall(t, c, op.key, &openapi.Input{Body: tt.items}, nil)
				fromIterator := string(w.last(t).Body)
				if fromSlice != op.want || fromIterator != op.want {
					t.Errorf("slice sent %q, iterator %q; want %q", fromSlice, fromIterator, op.want)
				}
			})
		}
	}
}

// client.go, Input.Body: for a sequential type "each element is one item,
// encoded as that value on its own would be, so a list and an iterator
// yielding the same values send the same bytes"; for an OpenAPI 3.2 multipart
// type other than multipart/form-data, each part is "a value encoded by that
// part's media type as that value on its own would be, so a list and an
// iterator yielding the same values send the same parts"; doc.go, Values: "a
// positional part, whose JSON data is null is omitted". An element is taken on
// its own, as the value its list holds, whatever the list's element type: a
// json.Marshaler element holding a nil pointer is that nil pointer, which
// json.Marshal writes as null; an encoding.TextMarshaler element whose value
// also has MarshalJSON is written by MarshalJSON; and an element whose pointer
// has MarshalJSON is a copy, written by reflection. So a list, an iter.Seq and
// an iter.Seq2 of the same values send the same JSON Lines items, each
// json.Marshal of the element on its own, and the same multipart/mixed parts,
// a null element leaving none.
func TestListAndIteratorSendSameElements(t *testing.T) {
	c := editionClient(t, editionDoc("3.2.1", `"/x":{"post":{"requestBody":{"content":{
		"application/jsonl":{},
		"multipart/mixed":{"schema":{"type":"array"},"itemEncoding":{"contentType":"application/json"}}}}}}`), nil)
	marshalers := []json.Marshaler{(*nilReceiver)(nil)}
	texts := []encoding.TextMarshaler{jwBoth{}}
	pointers := []ptrMarshalerElem{{1}, {2}}
	for _, tt := range []struct {
		name            string
		list, seq, seq2 any
		elements        []any
	}{
		{"json.Marshaler holding a nil pointer", marshalers, seqOf(marshalers...), seq2Of(marshalers...), []any{marshalers[0]}},
		{"TextMarshaler with MarshalJSON", texts, seqOf(texts...), seq2Of(texts...), []any{texts[0]}},
		{"pointer-receiver MarshalJSON", pointers, seqOf(pointers...), seq2Of(pointers...), []any{pointers[0], pointers[1]}},
	} {
		var lines, parts strings.Builder
		for _, e := range tt.elements {
			data := string(mustMarshal(t, e))
			lines.WriteString(data + "\n")
			if data != "null" {
				parts.WriteString("--B\r\nContent-Type: application/json\r\n\r\n" + data + "\r\n")
			}
		}
		parts.WriteString("--B--\r\n")
		for _, media := range []struct{ name, want string }{
			{"application/jsonl", lines.String()},
			{"multipart/mixed; boundary=B", parts.String()},
		} {
			t.Run(tt.name+"/"+media.name, func(t *testing.T) {
				for _, body := range []struct {
					name  string
					value any
				}{{"list", tt.list}, {"iter.Seq", tt.seq}, {"iter.Seq2", tt.seq2}} {
					req, err := c.Prepare("POST /x", &openapi.Input{MediaType: media.name, Body: body.value})
					if err != nil {
						t.Errorf("%s refused: %v", body.name, err)
						continue
					}
					if got := string(editionBody(t, req)); got != media.want {
						t.Errorf("%s sends %q, want %q", body.name, got, media.want)
					}
				}
			})
		}
	}
}

// shortRegularFile opens a regular file whose Stat size is more than it
// yields, a sysfs attribute, or skips the test where there is none.
func shortRegularFile(t *testing.T) *os.File {
	t.Helper()
	for _, path := range []string{"/sys/kernel/cpu_byteorder", "/sys/kernel/address_bits", "/sys/devices/system/cpu/online", "/sys/kernel/uevent_seqnum"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		fi, err := f.Stat()
		content, rerr := io.ReadAll(f)
		if err == nil && rerr == nil && fi.Mode().IsRegular() && len(content) > 0 && fi.Size() > int64(len(content)) {
			if _, err := f.Seek(0, io.SeekStart); err == nil {
				t.Cleanup(func() { f.Close() })
				return f
			}
		}
		f.Close()
	}
	t.Skip("no regular file here yields fewer bytes than its size")
	return nil
}

// A replayable source that yields fewer bytes than its size ends the form
// body as the multipart path does, with io.ErrUnexpectedEOF. An *os.File
// "that Stat reports to be a regular file" is sent again "from its offset
// when the call is prepared" (client.go, Input.Body), and a file that ends
// before its size is not its content. A sysfs attribute, which Stat reports
// as a regular file of 4,096 bytes and which yields a few, stands for a file
// truncated after Stat. As a form field and as a multipart part the call
// fails with an error matching io.ErrUnexpectedEOF. Before the fix the form
// field was sent short; the multipart part already failed so.
func TestShortReplayableFileFieldFails(t *testing.T) {
	w, c := walkClient(t)
	for _, key := range []string{"form", "mp"} {
		t.Run(key, func(t *testing.T) {
			resp, err := c.Call(t.Context(), key, &openapi.Input{Body: map[string]any{"s": shortRegularFile(t)}}, nil)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				sent := ""
				if w.count() > 0 {
					sent = string(w.last(t).Body)
				}
				t.Errorf("Call = %v, %v (server received %q); want an error matching io.ErrUnexpectedEOF", resp, err, sent)
			}
		})
	}
}

// Responses are exempt from request-side boundary checks (a response's
// boundary comes from the response), so a declared response key never gets
// Media.Err for its boundary, and response content compiles no form or
// multipart field list, so reads cost what they return (OAS 3.1.2 section
// 4.8.14, encoding: "The encoding field SHALL only apply to Request Body
// Objects"). A response's multipart key with an invalid boundary or two has
// no Media.Err, and response Media of form and multipart types and ranges
// list no Encoding fields, whether the schema or an encoding map names them;
// the request body's are unchanged. Before the fix the response keys had the
// request's boundary Err and fields.
func TestResponseMediaHaveNoRequestRules(t *testing.T) {
	content := `{
		"multipart/form-data; boundary=\"a \"":{"schema":{"type":"object","properties":{"x":{"type":"string"}}}},
		"multipart/mixed; boundary=a; boundary=b":{"schema":{"type":"object","properties":{"x":{"type":"string"}}}},
		"multipart/related":{"schema":{"type":"object","properties":{"x":{"type":"string"}}},"encoding":{"x":{"contentType":"text/csv"}}},
		"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"x":{"type":"string"}}},"encoding":{"y":{"style":"form"}}},
		"multipart/*":{"schema":{"type":"object","properties":{"x":{"type":"string"}}}}}`
	c := parseAt(t, doc31(`"/r":{"post":{"operationId":"both","requestBody":{"content":`+content+`},
		"responses":{"200":{"description":"ok","content":`+content+`}}}}`), "https://api.example.test", testDocURI, nil)
	op := mustOp(t, c, "both")
	for j, m := range response(t, op, 0).Media {
		if m.Err != nil || len(m.Encoding) != 0 {
			t.Errorf("response Media %d %q: Err %v, Encoding %q; want neither", j, m.Type, m.Err, encodingNames(m))
		}
	}
	for i, m := range op.Body.Media {
		if wantErr := i < 2; (m.Err != nil) != wantErr || len(m.Encoding) == 0 {
			t.Errorf("request Media %q: Err %v, Encoding %q", m.Type, m.Err, encodingNames(m))
		}
	}
}

// mustMarshal is json.Marshal, failing on an error.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
