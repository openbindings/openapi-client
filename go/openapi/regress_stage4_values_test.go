package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the stage 4 review round (stage 4 ledger, "Review
// round (6917b84)"), class ruling C4-1, one authoritative value traversal:
// "encoding/json decides how a Go value becomes JSON data (P4). The client's
// walk is a conservative fast path, only for shapes whose equivalence is
// proven (omitempty, omitzero with the IsZero fallback and pointer
// receivers, ",string", addressable pointer-receiver MarshalJSON and
// MarshalText, map keys exactly as encoding/json accepts them and their
// MarshalText errors), and defers to json.Marshal plus the JSON-data reader
// everywhere else. Every pointer or interface dereference counts toward the
// 1,000 bound independently of JSON depth; past it the walk defers to
// encoding/json, whose cycle detection decides. The depth walk runs before
// every client encoding, in every class (F16). A typed nil is a value, never
// a reader (F5, F10)". Findings A2, A5, F5, F10, F15, F16, F28, F35, and the
// *Part, *Event and nil-iterator notes. Expected form bytes are derived from
// json.Marshal itself (jsonForm), the authority, wherever the shape allows.
// The verification pass's C4-7 amends the dereference sentence ("Dereferences
// never end the walk ... Past 1,000 dereferences on one path the walk
// records the pointers on that path ... and a repeat ends the whole walk");
// its tests are in regress_stage4_walk_test.go.

const valuesPaths = `
	"/f":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"n":{"type":"string"},"t":{"type":"string"},"s":{"type":"string"},"z":{"type":"string"},
			"p":{"type":"string"},"x":{"type":"string"},"k":{"type":"string"},"o":{"type":"object"},"r":{},"arr":{"type":"array","items":{"type":"string"}},
			"j":{"type":"integer"},"b":{"type":"boolean"}}},
		"encoding":{"j":{"contentType":"application/json"},"b":{"contentType":"application/json"}}}}}}},
	"/m":{"post":{"operationId":"mp","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"n":{"type":"string"},"t":{"type":"string"},"s":{"type":"string"},"z":{"type":"string"},
			"p":{"type":"string"},"x":{"type":"string"},"k":{"type":"string"},"o":{"type":"object"},"r":{},"arr":{"type":"array","items":{"type":"string"}},
			"j":{"type":"integer"},"b":{"type":"boolean"}}},
		"encoding":{"j":{"contentType":"application/json"},"b":{"contentType":"application/json"}}}}}}},
	"/bf":{"post":{"operationId":"bareForm","requestBody":{"content":{"application/x-www-form-urlencoded":{}}}}},
	"/bm":{"post":{"operationId":"bareMp","requestBody":{"content":{"multipart/form-data":{}}}}},
	"/j":{"post":{"operationId":"json","requestBody":{"content":{"application/json":{}}}}},
	"/l":{"post":{"operationId":"jsonl","requestBody":{"content":{"application/jsonl":{}}}}},
	"/t":{"post":{"operationId":"text","requestBody":{"content":{"text/plain":{}}}}},
	"/o":{"post":{"operationId":"octets","requestBody":{"content":{"application/octet-stream":{}}}}},
	"/e":{"post":{"operationId":"sse","requestBody":{"content":{"text/event-stream":{}}}}},
	"/sf":{"post":{"operationId":"styledForm","requestBody":{"content":{"application/x-www-form-urlencoded":{"encoding":{"a":{"style":"form","explode":true}}}}}}},
	"/sm":{"post":{"operationId":"styledMp","requestBody":{"content":{"multipart/form-data":{"encoding":{"a":{"style":"form","explode":true}}}}}}}`

func valuesClient(t *testing.T) (*wire, *openapi.Client) {
	t.Helper()
	w := newWire(t, nil)
	return w, parseFor(t, w, doc31(valuesPaths), nil)
}

// jsonForm is the form body of v's JSON data, as json.Marshal makes it:
// its members in order, null ones omitted, scalars as text, arrays one
// field per item, objects as JSON text (doc.go, Values; Fixed rules, Form
// bodies).
func jsonForm(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("%s is not an object", b)
	}
	var fields []string
	for dec.More() {
		tok, _ := dec.Token()
		name := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		switch raw[0] {
		case 'n':
		case '[':
			var items []any
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			d.Decode(&items)
			for _, it := range items {
				switch it := it.(type) {
				case nil:
				case string:
					fields = append(fields, formPairs(name, it))
				default:
					fields = append(fields, formPairs(name, fmt.Sprint(it)))
				}
			}
		case '"':
			var s string
			json.Unmarshal(raw, &s)
			fields = append(fields, formPairs(name, s))
		case '{':
			fields = append(fields, formPairs(name, string(raw)))
		default:
			fields = append(fields, formPairs(name, string(raw)))
		}
	}
	return strings.Join(fields, "&")
}

// Types whose encoding encoding/json decides: IsZero by value and by
// pointer receiver; pointer-receiver MarshalJSON and MarshalText, used only
// where the value is addressable (encoding/json, "condAddrEncoder"); a map
// key whose MarshalText fails.
type (
	alwaysZero struct{ V string }
	ptrZero    struct{ V string }
	ptrJSON    string
	ptrText    string
	badKey     int
	bodyJSON   struct{}
	arrayJSON  struct{}
)

var errBadKey = errors.New("bad key")

func (alwaysZero) IsZero() bool                { return true }
func (*ptrZero) IsZero() bool                  { return true }
func (*ptrJSON) MarshalJSON() ([]byte, error)  { return []byte(`"custom"`), nil }
func (*ptrText) MarshalText() ([]byte, error)  { return []byte("ctext"), nil }
func (badKey) MarshalText() ([]byte, error)    { return nil, errBadKey }
func (bodyJSON) MarshalJSON() ([]byte, error)  { return []byte(`{"s":"1","n":"2"}`), nil }
func (arrayJSON) MarshalJSON() ([]byte, error) { return []byte(`["a",null,"b"]`), nil }

type omitZeroBody struct {
	N int        `json:"n,omitzero"`
	T time.Time  `json:"t,omitzero"`
	Z alwaysZero `json:"z,omitzero"`
	P ptrZero    `json:"p,omitzero"`
	S string     `json:"s"`
}

type marshalerBody struct {
	X ptrJSON `json:"x"`
	K ptrText `json:"k"`
	S string  `json:"s,omitempty"`
}

// C4-1 (A5): the form and multipart field walk gives exactly what
// encoding/json gives: omitzero through IsZero (a value or pointer
// receiver, a time.Time); a pointer-receiver MarshalJSON or MarshalText
// used for an addressable field (the body given by pointer) and not for an
// unaddressable one; ",string"; a MarshalJSON body walked by its JSON
// members, and a MarshalJSON property whose JSON is an array split into
// items (F35: lines no test reached).
func TestC41EncodingJSONEquivalence(t *testing.T) {
	w, c := valuesClient(t)
	cases := []struct {
		name string
		body any
		want string // the form body, checked against jsonForm(body)
	}{
		{"omitzero, all zero", omitZeroBody{Z: alwaysZero{"v"}, P: ptrZero{"v"}, S: "x"}, "s=x"},
		{"omitzero, set", omitZeroBody{N: 3, T: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Z: alwaysZero{"v"}, P: ptrZero{"v"}, S: "x"}, "n=3&t=2026-01-02T03%3A04%3A05Z&s=x"},
		{"pointer receivers, addressable", &marshalerBody{X: "raw", K: "raw"}, "x=custom&k=ctext"},
		{"pointer receivers, not addressable", marshalerBody{X: "raw", K: "raw"}, "x=raw&k=raw"},
		{"a MarshalJSON body", bodyJSON{}, "s=1&n=2"},
		{"a MarshalJSON array property", map[string]any{"arr": arrayJSON{}}, "arr=a&arr=b"},
		{"int map keys as encoding/json sorts them", map[int]string{2: "b", 10: "a"}, "10=a&2=b"},
		{"the string option", struct {
			N int  `json:"n,string"`
			P bool `json:"p,string"`
		}{5, true}, "n=5&p=true"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if ref := jsonForm(t, tt.body); ref != tt.want {
				t.Fatalf("test bug: json.Marshal gives %q, the case says %q", ref, tt.want)
			}
			key := "form"
			if strings.Contains(tt.name, "map keys") {
				key = "bareForm"
			}
			mustCall(t, c, key, &openapi.Input{Body: tt.body}, nil)
			if got := w.last(t); string(got.Body) != tt.want {
				t.Errorf("form body %q, want %q", got.Body, tt.want)
			}
		})
	}
	// The same walk names the parts of a multipart body.
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", &marshalerBody{X: "raw", K: "raw"})
	checkParts(t, parts, []wantPart{
		{disposition: formData("x"), ctype: "text/plain", content: "custom"},
		{disposition: formData("k"), ctype: "text/plain", content: "ctext"},
	})
	_, parts = sendMultipart(t, w, c, "mp", "multipart/form-data", omitZeroBody{Z: alwaysZero{"v"}, P: ptrZero{"v"}, S: "x"})
	checkParts(t, parts, []wantPart{{disposition: formData("s"), ctype: "text/plain", content: "x"}})
}

// C4-1, stage 4 ledger RQ3: under a JSON-typed field a ",string" member
// holds its JSON data, the string encoding/json makes of it, so the JSON
// content is `"5"` and `"true"` (doc.go, Values: "The client first converts
// a value to JSON data as encoding/json would (struct tags ...)"), in a form
// body percent-encoded, in a multipart part as it is.
func TestRQ3StringOptionUnderJSONField(t *testing.T) {
	w, c := valuesClient(t)
	body := struct {
		J int  `json:"j,string"`
		B bool `json:"b,string"`
	}{5, true}
	mustCall(t, c, "form", &openapi.Input{Body: body}, nil)
	if got := w.last(t); string(got.Body) != "j=%225%22&b=%22true%22" {
		t.Errorf("form body %q, want j=%%225%%22&b=%%22true%%22", got.Body)
	}
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", body)
	checkParts(t, parts, []wantPart{
		{disposition: formData("j"), ctype: "application/json", content: `"5"`},
		{disposition: formData("b"), ctype: "application/json", content: `"true"`},
	})
}

// C4-1 (A5): map keys exactly as encoding/json accepts them: a map whose
// key type json.Marshal refuses, and a key whose MarshalText fails, are
// refused at their key, the encoder's error kept for errors.Is/As (stage 1
// ledger, F24), in form and multipart bodies and their properties.
func TestC41MapKeys(t *testing.T) {
	w, c := valuesClient(t)
	for _, key := range []string{"bareForm", "bareMp", "form", "mp"} {
		for _, tt := range []struct {
			name string
			body any
			at   string
			is   func(error) bool
		}{
			{"bool keys", map[bool]string{true: "x"}, "Input.Body", func(err error) bool {
				var ut *json.UnsupportedTypeError
				return errors.As(err, &ut)
			}},
			{"float keys", map[float64]string{1.5: "x"}, "Input.Body", func(err error) bool {
				var ut *json.UnsupportedTypeError
				return errors.As(err, &ut)
			}},
			{"a failing MarshalText key", map[badKey]string{1: "x"}, "Input.Body", func(err error) bool { return errors.Is(err, errBadKey) }},
			{"bool keys in a property", map[string]any{"o": map[bool]string{true: "x"}}, "Input.Body/o", func(err error) bool {
				var ut *json.UnsupportedTypeError
				return errors.As(err, &ut)
			}},
		} {
			if strings.HasPrefix(key, "bare") && tt.at != "Input.Body" {
				continue
			}
			t.Run(key+" "+tt.name, func(t *testing.T) {
				before := w.count()
				resp, err := c.Call(t.Context(), key, &openapi.Input{Body: tt.body}, nil)
				re := refusedSince(t, w, before, resp, err)
				wantKeys(t, "Inputs", re.Inputs, true, tt.at)
				if !tt.is(err) {
					t.Errorf("refusal %v does not keep encoding/json's error", err)
				}
			})
		}
	}
}

// cycle returns a value that holds itself through an interface and a
// pointer: var v any; v = &v (Astra, A2).
func cycle() any {
	var v any
	v = &v
	return v
}

// selfNode is a struct that points to itself.
type selfNode struct {
	Next *selfNode `json:"next"`
}

// C4-1 (A2): a pointer or interface cycle is refused, never a hang or a
// stack overflow: "Every pointer or interface dereference counts toward the
// 1,000 bound independently of JSON depth; past it the walk defers to
// encoding/json, whose cycle detection decides" (encoding/json: "JSON
// cannot represent cyclic data structures and Marshal does not handle
// them", reported as an *UnsupportedValueError). As a whole body under
// form, multipart, JSON, text and JSON Lines, as a property, and as an
// item, each refused at its key. The cases run in a child process, each
// under a 10 s guard, as a stack overflow or a hang would end the test
// binary or hold a core.
func TestC41CyclesRefused(t *testing.T) {
	if !inChild(t) {
		return
	}
	w, c := valuesClient(t)
	node := &selfNode{}
	node.Next = node
	for _, tt := range []struct {
		key  string
		body any
		at   string
	}{
		{"bareForm", cycle(), "Input.Body"},
		{"bareMp", cycle(), "Input.Body"},
		{"json", cycle(), "Input.Body"},
		{"text", cycle(), "Input.Body"},
		{"octets", cycle(), "Input.Body"},
		{"json", node, "Input.Body"},
		{"form", map[string]any{"o": cycle()}, "Input.Body/o"},
		{"mp", map[string]any{"o": cycle()}, "Input.Body/o"},
		{"form", map[string]any{"o": node}, "Input.Body/o"},
		{"mp", map[string]any{"s": cycle()}, "Input.Body/s"},
		{"jsonl", []any{1, cycle()}, "Input.Body/1"},
		{"sse", []any{cycle()}, "Input.Body/0"},
	} {
		t.Run(fmt.Sprintf("%s %T at %s", tt.key, tt.body, tt.at), func(t *testing.T) {
			var resp *openapi.Response
			var err error
			before := w.count()
			promptly(t, 10*time.Second, "Call with a cycle", func() {
				resp, err = c.Call(t.Context(), tt.key, &openapi.Input{Body: tt.body}, nil)
			})
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, tt.at)
		})
	}
}

// C4-1 (F16): "The depth walk runs before every client encoding, in every
// class": an object 50,000 levels deep is refused at its key under text,
// octet-stream, a form text field, a multipart text part and an event
// stream item, without encoding it first (stage brief: "a deep value is
// refused at 1,000 levels before large allocation"; json.Marshal of it
// allocates several megabytes): each refusal allocates under 1 MiB.
func TestC41DepthWalkInEveryClass(t *testing.T) {
	_, c := valuesClient(t)
	deep := nestMap(50000, "x")
	for _, tt := range []struct {
		key  string
		body any
		at   string
	}{
		{"text", deep, "Input.Body"},
		{"octets", deep, "Input.Body"},
		{"form", map[string]any{"s": deep}, "Input.Body/s"},
		{"mp", map[string]any{"s": deep}, "Input.Body/s"},
		{"sse", []any{deep}, "Input.Body/0"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			var err error
			in := &openapi.Input{Body: tt.body}
			n := allocatedBy(func() { _, err = c.Prepare(tt.key, in) })
			var re *openapi.RequestError
			if !errors.As(err, &re) {
				t.Fatalf("Prepare = %v, want a refusal", err)
			}
			wantKeys(t, "Inputs", re.Inputs, true, tt.at)
			if n > 1<<20 {
				t.Errorf("refusing a 50,000-level value allocated %d bytes; the depth walk stops at 1,000 levels", n)
			}
		})
	}
}

// C4-1 (F5, F10): "A typed nil is a value, never a reader: as a form or
// multipart property or item it is omitted (Values: null), as a sequential
// item it is null, as a body it is encoded as null or refused where its
// type takes only strings" (client.go, Input.Body: "A typed nil is a value,
// never a reader, so a property or item holding one is omitted as null").
// Panics are reported as failures (noPanic).
func TestC41TypedNilReaders(t *testing.T) {
	w, c := valuesClient(t)
	run := func(name string, f func(t *testing.T)) {
		t.Run(name, func(t *testing.T) { noPanic(t, name, func() { f(t) }) })
	}
	run("form", func(t *testing.T) {
		mustCall(t, c, "form", &openapi.Input{Body: map[string]any{
			"r": (*strings.Reader)(nil), "s": "x", "arr": []io.Reader{(*os.File)(nil), strings.NewReader("y")},
		}}, nil)
		if got := w.last(t); string(got.Body) != "arr=y&s=x" {
			t.Errorf("form body %q, want arr=y&s=x", got.Body)
		}
	})
	run("multipart", func(t *testing.T) {
		_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{
			"r": (*bytes.Buffer)(nil), "s": "x", "arr": []io.Reader{(*os.File)(nil), (*bytes.Reader)(nil), strings.NewReader("y")},
		})
		checkParts(t, parts, []wantPart{
			{disposition: formData("arr", "arr"), ctype: "text/plain", content: "y"},
			{disposition: formData("s"), ctype: "text/plain", content: "x"},
		})
	})
	run("JSON Lines slice of any", func(t *testing.T) {
		mustCall(t, c, "jsonl", &openapi.Input{Body: []any{(*bytes.Buffer)(nil), 1}}, nil)
		wantLines(t, w.last(t).Body, "null", "1")
	})
	run("JSON Lines slice of readers", func(t *testing.T) {
		mustCall(t, c, "jsonl", &openapi.Input{Body: []io.Reader{(*strings.Reader)(nil), (*bytes.Reader)(nil)}}, nil)
		wantLines(t, w.last(t).Body, "null", "null")
	})
	run("JSON body", func(t *testing.T) {
		mustCall(t, c, "json", &openapi.Input{Body: (*bytes.Buffer)(nil)}, nil)
		if got := w.last(t); string(got.Body) != "null" {
			t.Errorf("JSON body %q, want null", got.Body)
		}
	})
	for _, key := range []string{"octets", "text"} {
		run(key+" body", func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), key, &openapi.Input{Body: (*strings.Reader)(nil)}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, "Input.Body")
		})
	}
}

// C4-1 (F5): a typed-nil reader yielded by an iterator is the item null.
// At 6917b84 it crashed the process from net/http's write loop, so the case
// runs in a child process.
func TestC41TypedNilReaderFromIterator(t *testing.T) {
	if !inChild(t) {
		return
	}
	w, c := valuesClient(t)
	it := func(yield func(any) bool) {
		if yield((*strings.Reader)(nil)) {
			yield(2)
		}
	}
	mustCall(t, c, "jsonl", &openapi.Input{Body: iter.Seq[any](it)}, nil)
	wantLines(t, w.last(t).Body, "null", "2")
}

// F15: a Part, or a non-nil *Part, as the whole form or multipart Body is
// refused at Input.Body (client.go, Input.Body: "For form and multipart
// media, Body is an object (a map or a struct)"; errors.go,
// RequestError.Inputs: "a reader or Part where the media type cannot carry
// one"), never sent as an empty body.
func TestF15PartAsBodyRefused(t *testing.T) {
	w, c := valuesClient(t)
	for _, key := range []string{"bareForm", "bareMp"} {
		for _, body := range []any{openapi.Part{Content: "x", MediaType: "text/plain"}, &openapi.Part{Content: []byte("x")}} {
			before := w.count()
			resp, err := c.Call(t.Context(), key, &openapi.Input{Body: body}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, fmt.Sprintf("%s %T Inputs", key, body), re.Inputs, true, "Input.Body")
		}
	}
}

// F28 (T1, client.go, Input.Body: "A field an Encoding style serializes
// takes JSON data, so a []byte there is a base64 string and a Part or
// reader is refused"): in a form body the base64 is percent-encoded as any
// RFC 6570 value; in a multipart part it is sent as it is.
func TestF28StyledFieldTakesJSONData(t *testing.T) {
	w, c := valuesClient(t)
	mustCall(t, c, "styledForm", &openapi.Input{Body: map[string]any{"a": []byte("raw bytes?")}}, nil)
	if got := w.last(t); string(got.Body) != "a=cmF3IGJ5dGVzPw%3D%3D" {
		t.Errorf("form body %q, want a=cmF3IGJ5dGVzPw%%3D%%3D", got.Body)
	}
	_, parts := sendMultipart(t, w, c, "styledMp", "multipart/form-data", map[string]any{"a": []byte("raw bytes?")})
	checkParts(t, parts, []wantPart{{disposition: formData("a"), ctype: "text/plain", content: "cmF3IGJ5dGVzPw=="}})
	for _, key := range []string{"styledForm", "styledMp"} {
		for _, v := range []any{openapi.Part{Content: "x"}, &openapi.Part{Content: "x"}, strings.NewReader("x")} {
			before := w.count()
			resp, err := c.Call(t.Context(), key, &openapi.Input{Body: map[string]any{"a": v}}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, fmt.Sprintf("%s %T Inputs", key, v), re.Inputs, true, "Input.Body/a")
		}
	}
}

// A non-nil *Part is a Part and a non-nil *Event an Event (client.go,
// Input.Body: "a [Part] (or a non-nil *Part)", "an [Event] (or a non-nil
// *Event)"; stage 4 ledger, IP4-6 and the review's contract notes); a nil
// *Part is a typed nil, omitted, and a nil *Event cannot be encoded.
func TestPointerPartsAndEvents(t *testing.T) {
	w, c := valuesClient(t)
	_, parts := sendMultipart(t, w, c, "mp", "multipart/form-data", map[string]any{
		"r": &openapi.Part{Content: []byte("x"), Filename: "a.txt", MediaType: "image/png"},
		"s": (*openapi.Part)(nil),
	})
	checkParts(t, parts, []wantPart{{disposition: formData("r", "a.txt"), ctype: "image/png", content: "x"}})
	mustCall(t, c, "form", &openapi.Input{Body: map[string]any{"s": &openapi.Part{Content: "v w", MediaType: "text/plain"}, "t": (*openapi.Part)(nil)}}, nil)
	if got := w.last(t); string(got.Body) != "s=v+w" {
		t.Errorf("form body %q, want s=v+w", got.Body)
	}
	mustCall(t, c, "sse", &openapi.Input{Body: []*openapi.Event{{Data: []byte("x")}, {Event: "e", ID: "1", IDSet: true}}}, nil)
	if got := w.last(t); string(got.Body) != "data: x\n\nevent: e\nid: 1\n\n" {
		t.Errorf("event stream %q", got.Body)
	}
	before := w.count()
	resp, err := c.Call(t.Context(), "sse", &openapi.Input{Body: []*openapi.Event{{Data: []byte("x")}, nil}}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/1")
}

// IP4-6 (test gap): a nil iterator is refused at Input.Body; a nil slice is
// an empty body.
func TestNilIteratorRefused(t *testing.T) {
	w, c := valuesClient(t)
	for _, body := range []any{iter.Seq[int](nil), iter.Seq2[any, error](nil)} {
		before := w.count()
		resp, err := c.Call(t.Context(), "jsonl", &openapi.Input{Body: body}, nil)
		re := refusedSince(t, w, before, resp, err)
		wantKeys(t, fmt.Sprintf("%T Inputs", body), re.Inputs, true, "Input.Body")
	}
	mustCall(t, c, "jsonl", &openapi.Input{Body: []Pet(nil)}, nil)
	if got := w.last(t); len(got.Body) != 0 {
		t.Errorf("a nil slice sent %q, want an empty body", got.Body)
	}
}
