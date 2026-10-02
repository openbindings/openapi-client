package openapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the stage 4 verification pass's performance findings
// (stage 4 ledger, "Verification: performance").

// vppPaths is one form body with a field for each way a field takes a
// value: text/plain, application/json, application/octet-stream, the form
// type, an RFC 6570 style, and two types.
const vppPaths = `"/f":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{
	"encoding":{"tp":{"contentType":"text/plain"},"js":{"contentType":"application/json"},
		"oc":{"contentType":"application/octet-stream"},"fm":{"contentType":"application/x-www-form-urlencoded"},
		"st":{"style":"form","explode":true},"mt":{"contentType":"text/plain, application/json"}}}}}}}`

// Named types: a string, an int, and a type encoding/json writes by its
// MarshalText.
type (
	vppS string
	vppN int
	vppT struct{ s string }
)

func (t vppT) MarshalText() ([]byte, error) { return []byte(t.s), nil }

// jsonCodec is a caller codec writing "C" and the value as json.Marshal
// writes it.
type jsonCodec struct{}

func (jsonCodec) Encode(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err == nil {
		_, err = w.Write(append([]byte("C"), b...))
	}
	return err
}
func (jsonCodec) Decode(io.Reader, any) error { return nil }

// VPP1: "each field records at compile whether a value of exactly string,
// int or bool can be written straight into the body (no style, no Err, one
// concrete type that is not the form type, a text class for numbers and
// booleans, not JSON for strings), and with no caller codec configured such
// a value is written by strconv and the form encoder; anything else, a
// named type included, takes the existing path ... the shortcut's boundary
// is covered by black-box tests (each class, named types, a caller codec,
// maps and pointers) against exact bytes." Each value, a string, an int, a
// bool, a named string, a named int and a MarshalText type, is given to
// each field from a map, a struct and a pointer to the struct, and the body
// is exactly what the contract makes of it: under text/plain its text
// (doc.go, Values: "a number or boolean is written in its JSON spelling ...
// and a string ... as it is"), under application/json its JSON, under
// application/octet-stream a string only ("any other type takes only a
// string", its JSON data a string for the named string and the MarshalText
// type), under the form type an object only, under a style RFC 6570's form
// expansion, under two types a choice by Part.MediaType, and with a caller
// codec for text/plain what the codec writes (client.go, Options.Codecs).
// These pin behavior that holds at b4872f9, for the shortcut to keep.
func TestVPP1FormScalarBoundary(t *testing.T) {
	doc := doc31(vppPaths)
	plain := parseAt(t, doc, "https://api.example.test", testDocURI, nil)
	coded := parseAt(t, doc, "https://api.example.test", testDocURI, &openapi.Options{Codecs: map[string]openapi.Codec{"text/plain": jsonCodec{}}})
	values := []struct {
		name string
		v    any
		text string // its text, as a text/plain field writes it
		json string // its JSON
		str  bool   // its JSON data is a string
	}{
		{"string", "a b+c/d", "a b+c/d", `"a b+c/d"`, true},
		{"int", 5, "5", "5", false},
		{"bool", true, "true", "true", false},
		{"named string", vppS("a b"), "a b", `"a b"`, true},
		{"named int", vppN(7), "7", "7", false},
		{"MarshalText type", vppT{"t x"}, "t x", `"t x"`, true},
	}
	type outcome struct {
		body     string // the body sent, or "" for a refusal
		settings bool   // a refusal in Settings, else in Inputs
	}
	for _, val := range values {
		fields := []struct {
			name string
			c    *openapi.Client
			want outcome
		}{
			{"tp", plain, outcome{body: formPairs("tp", val.text)}},
			{"js", plain, outcome{body: formPairs("js", val.json)}},
			{"oc", plain, outcome{body: map[bool]string{true: formPairs("oc", val.text)}[val.str]}},
			{"fm", plain, outcome{}},
			{"st", plain, outcome{body: "st=" + pctName(val.text)}},
			{"mt", plain, outcome{settings: true}},
			{"tp", coded, outcome{body: formPairs("tp", "C"+val.json)}},
		}
		for _, f := range fields {
			codec := ""
			if f.c == coded {
				codec = " with a codec"
			}
			for _, body := range vppBodies(f.name, val.v) {
				t.Run(fmt.Sprintf("%s in %s%s from %s", val.name, f.name, codec, body.name), func(t *testing.T) {
					req, err := f.c.Prepare("form", &openapi.Input{Body: body.v})
					switch {
					case f.want.body != "":
						if err != nil {
							t.Fatalf("Prepare: %v", err)
						}
						if got := string(preparedBody(t, req)); got != f.want.body {
							t.Errorf("body %q, want %q", got, f.want.body)
						}
					case f.want.settings:
						wantKeys(t, "Settings", asRequestError(t, err).Settings, true, "Input.Body/"+f.name)
					default:
						wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "Input.Body/"+f.name)
					}
				})
			}
		}
	}
}

// vppBodies returns v as the property name of a map[string]any, of a struct
// and of a pointer to that struct.
func vppBodies(name string, v any) []struct {
	name string
	v    any
} {
	st := reflect.StructOf([]reflect.StructField{{Name: "F", Type: reflect.TypeOf(v), Tag: reflect.StructTag(`json:"` + name + `"`)}})
	p := reflect.New(st)
	p.Elem().Field(0).Set(reflect.ValueOf(v))
	return []struct {
		name string
		v    any
	}{
		{"a map", map[string]any{name: v}},
		{"a struct", p.Elem().Interface()},
		{"a pointer to a struct", p.Interface()},
	}
}

// VPP2: "payload.appendForm grows the builder by the source's size before
// reading (F21 as written)": preparing a form body whose field is a 1 MiB
// *bytes.Reader, read when the call is prepared (doc.go, Fixed rules, Form
// bodies: "a file in a field is read into memory then"), allocates at most
// 2.5 times the field's size (bytes allocated, as the scaling harness
// counts them). At b4872f9 it allocated about 5.2 times.
func TestVPP2ReplayableFormFieldAllocation(t *testing.T) {
	c := parseAt(t, doc31(vppPaths), "https://api.example.test", testDocURI, nil)
	if _, err := c.Prepare("form", &openapi.Input{Body: map[string]any{"oc": bytes.NewReader([]byte("first use"))}}); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("abcdefgh"), 1<<17) // 1 MiB that the form encoder writes as it is
	var req *openapi.Request
	var err error
	n := allocatedBy(func() {
		req, err = c.Prepare("form", &openapi.Input{Body: map[string]any{"oc": bytes.NewReader(data)}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := preparedBody(t, req); len(got) != len(data)+3 || !bytes.Equal(got[3:], data) {
		t.Fatalf("the body is not oc= and the field")
	}
	t.Logf("%d bytes allocated for a %d-byte field (%.2fx)", n, len(data), float64(n)/float64(len(data)))
	if n > uint64(len(data))*5/2 {
		t.Errorf("preparing a %d-byte field allocated %d bytes, %.1f times its size; want at most 2.5", len(data), n, float64(n)/float64(len(data)))
	}
}
