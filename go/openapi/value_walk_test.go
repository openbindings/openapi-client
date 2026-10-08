package openapi_test

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the value walk, which reaches a verdict on everything
// encoding/json reaches. The walk visits exactly the values json writes:
// pointer-receiver MarshalJSON and MarshalText apply wherever json applies
// them (an addressable value: every slice element and every field or element
// of an addressable struct or array, at any depth), not only at a field's own
// type; a map type whose key json refuses is refused as json refuses it, never
// walked (its keys never reach mapKey). Dereferences never end the walk:
// doc.go's Values counts the levels of "a value whose JSON, a MarshalJSON's
// output included, nests deeper than 1,000 levels", to which pointers add
// none, so a chain of pointers around a reader is walked to the
// reader and refused. Past 1,000 dereferences on one path the walk records the
// pointers on that path, as encoding/json does past its own 1,000
// (startDetectingCyclesAfter), and a repeat ends the whole walk: the value is
// refused as the cycle json would refuse. The walk stops at the first path
// that decides, so a cyclic value costs what is visited before the repeat,
// never one walk per path. The authority is encoding/json itself (doc.go,
// Values: "The client first converts a value to JSON data as encoding/json
// would"): expected bytes and refusals are derived from json.Marshal wherever
// the shape allows.

// walkPaths has one operation per class the client encodes a value in: a
// JSON body, a text body, a form body and a multipart body with a JSON
// field o and a text field s, JSON Lines and event stream items, and a query
// parameter written by its style.
const walkPaths = `
	"/j":{"post":{"operationId":"json","requestBody":{"content":{"application/json":{}}}}},
	"/t":{"post":{"operationId":"text","requestBody":{"content":{"text/plain":{}}}}},
	"/f":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{
		"schema":{"type":"object","properties":{"o":{"type":"object"},"s":{"type":"string"}}}}}}}},
	"/m":{"post":{"operationId":"mp","requestBody":{"content":{"multipart/form-data":{
		"schema":{"type":"object","properties":{"o":{"type":"object"},"s":{"type":"string"}}}}}}}},
	"/l":{"post":{"operationId":"jsonl","requestBody":{"content":{"application/jsonl":{}}}}},
	"/e":{"post":{"operationId":"sse","requestBody":{"content":{"text/event-stream":{}}}}},
	"/q":{"get":{"operationId":"query","parameters":[{"name":"q","in":"query","schema":{}}]}}`

func walkClient(t *testing.T) (*wire, *openapi.Client) {
	t.Helper()
	w := newWire(t, nil)
	return w, parseFor(t, w, doc31(walkPaths), nil)
}

// A walkCase is a value given to one class of encoding, and the key its
// refusal is reported at (errors.go, RequestError.Inputs: "Input.Body"
// followed by a JSON Pointer to the part of Body concerned, or a
// parameter's name).
type walkCase struct {
	name string
	key  string // the operation
	in   *openapi.Input
	at   string
}

// inEveryClass places v in every class the client encodes: the whole JSON
// and text body, a JSON and a text form field, a JSON and a text multipart
// part, a JSON Lines item and an event stream item, and a styled query
// parameter.
func inEveryClass(name string, v any) []walkCase {
	body := func(b any) *openapi.Input { return &openapi.Input{Body: b} }
	return []walkCase{
		{name + ", JSON body", "json", body(v), "Input.Body"},
		{name + ", text body", "text", body(v), "Input.Body"},
		{name + ", form JSON field", "form", body(map[string]any{"o": v}), "Input.Body/o"},
		{name + ", form text field", "form", body(map[string]any{"s": v}), "Input.Body/s"},
		{name + ", multipart JSON part", "mp", body(map[string]any{"o": v}), "Input.Body/o"},
		{name + ", multipart text part", "mp", body(map[string]any{"s": v}), "Input.Body/s"},
		{name + ", JSON Lines item", "jsonl", body([]any{1, v}), "Input.Body/1"},
		{name + ", event stream item", "sse", body([]any{v}), "Input.Body/0"},
		{name + ", styled query parameter", "query", &openapi.Input{Params: map[string]any{"q": v}}, "q"},
	}
}

// Values that hold themselves along two paths at every level.
type (
	jwTwo      struct{ A, B any }
	jwIfacePtr struct {
		A any
		P *jwIfacePtr
	}
)

// branchingCycles returns cycles that branch: a struct{A, B any} whose fields
// both point back at it, a map whose two keys hold pointers to it, and a
// struct with an interface field and a pointer field back. Each JSON level
// costs one or two dereferences, so the dereference bound comes before the
// level bound, and each level branches twice.
func branchingCycles() []struct {
	name string
	v    any
} {
	two := &jwTwo{}
	two.A, two.B = two, two
	m := map[string]any{}
	m["a"], m["b"] = &m, &m
	ip := &jwIfacePtr{}
	ip.A, ip.P = ip, ip
	return []struct {
		name string
		v    any
	}{
		{"struct{A, B any}", two},
		{"a map of two pointers to itself", m},
		{"struct{A any; P *T}", ip},
	}
}

// A cyclic value branching at every level is refused promptly, at its key, in
// every class the client encodes: a repeat ends the whole walk, the value
// refused as the cycle json would refuse, and the walk stops at the first path
// that decides, so a cyclic value costs what is visited before the repeat,
// never one walk per path. An earlier walk visited about 2^500
// paths and Prepare never returned, so every case runs at once on its own
// goroutine, all bounded by one 30 s wait, in a child process, which ends the
// walks still running when it fails.
func TestBranchingCyclesRefusedPromptly(t *testing.T) {
	if !inChild(t) {
		return
	}
	_, c := walkClient(t)
	var cases []walkCase
	for _, cy := range branchingCycles() {
		cases = append(cases, inEveryClass(cy.name, cy.v)...)
	}
	done := make([]chan error, len(cases))
	for i, tt := range cases {
		done[i] = make(chan error, 1)
		go func() {
			_, err := c.Prepare(tt.key, tt.in)
			done[i] <- err
		}()
	}
	expired := make(chan struct{})
	timer := time.AfterFunc(30*time.Second, func() { close(expired) })
	defer timer.Stop()
	for i, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			select {
			case err := <-done[i]:
				re := asRequestError(t, err)
				wantKeys(t, "Inputs", re.Inputs, true, tt.at)
			case <-expired:
				t.Fatalf("Prepare did not return within 30s")
			}
		})
	}
}

// derefChain returns v behind n pointers to interfaces: 2n dereferences,
// none of them a level of the JSON encoding/json writes.
func derefChain(v any, n int) any {
	for range n {
		x := v
		v = &x
	}
	return v
}

// Dereferences never end the walk, so a chain of pointers around a reader is
// walked to the reader and refused (client.go, Input.Body: "A Part or
// io.Reader inside a value the client encodes with encoding/json is refused
// with an Inputs entry at its place in Body"; pointers add no JSON Pointer
// token). A reader or Part behind 1,200 and 3,000 dereferences, a non-cyclic
// chain of *any, is refused at its key in JSON, form and multipart bodies, and
// nothing is sent: previously the walk gave up past 1,000 dereferences and
// encoding/json wrote the reader as {}.
func TestReaderBehindManyDereferencesRefused(t *testing.T) {
	w, c := walkClient(t)
	for _, hidden := range []struct {
		name string
		v    func() any
	}{
		{"reader", func() any { return strings.NewReader("secret") }},
		{"Part", func() any { return openapi.Part{Content: "secret"} }},
	} {
		for _, hops := range []int{600, 1500} {
			for _, tt := range []struct {
				name, key string
				body      func(x any) any
				at        string
			}{
				{"the JSON body", "json", func(x any) any { return x }, "Input.Body"},
				{"a JSON body member", "json", func(x any) any { return map[string]any{"a": x, "b": "x"} }, "Input.Body/a"},
				{"a form JSON field", "form", func(x any) any { return map[string]any{"o": x, "s": "x"} }, "Input.Body/o"},
				{"a form text field", "form", func(x any) any { return map[string]any{"s": x} }, "Input.Body/s"},
				{"a multipart JSON part", "mp", func(x any) any { return map[string]any{"o": x, "s": "x"} }, "Input.Body/o"},
				{"a multipart text part", "mp", func(x any) any { return map[string]any{"s": x} }, "Input.Body/s"},
			} {
				t.Run(fmt.Sprintf("%s behind %d dereferences in %s", hidden.name, 2*hops, tt.name), func(t *testing.T) {
					before := w.count()
					resp, err := c.Call(t.Context(), tt.key, &openapi.Input{Body: tt.body(derefChain(hidden.v(), hops))}, nil)
					if err == nil {
						t.Fatalf("sent %q", w.last(t).Body)
					}
					re := refusedSince(t, w, before, resp, err)
					wantKeys(t, "Inputs", re.Inputs, true, tt.at)
				})
			}
		}
	}
}

// refusedKeyMaps are maps whose key type encoding/json refuses, each holding
// a reader or Part.
func refusedKeyMaps() []struct {
	name string
	v    func() any
} {
	return []struct {
		name string
		v    func() any
	}{
		{"map[any]any holding a Part", func() any { return map[any]any{"file": openapi.Part{Content: "x"}} }},
		{"map[any]any holding a reader", func() any { return map[any]any{"k": strings.NewReader("x")} }},
		{"map[float64]any holding a reader", func() any { return map[float64]any{1.5: strings.NewReader("x")} }},
		{"map[bool]any holding a Part", func() any { return map[bool]any{true: openapi.Part{Content: "x"}} }},
	}
}

// unsupportedType reports whether err keeps encoding/json's refusal of a
// type, the encoder's error being kept for errors.As.
func unsupportedType(err error) bool {
	var ut *json.UnsupportedTypeError
	return errors.As(err, &ut)
}

// A map type whose key json refuses is refused as json refuses it, never
// walked (its keys never reach mapKey): json.Marshal returns an
// *UnsupportedTypeError for such a map, whatever it holds. Held in a JSON or
// text body, a form or multipart field, a JSON Lines item or a styled query
// parameter, a map[any]any, map[float64]any or map[bool]any holding a reader
// or Part is refused at its key with json's error. Previously the walk named
// the map key after finding the reader and panicked (reflect.Value.Uint on an
// interface Value); panics are reported as failures, each case on a fresh
// Client.
func TestUnsupportedMapKeysHoldingReadersRefused(t *testing.T) {
	for _, m := range refusedKeyMaps() {
		body := func(b any) *openapi.Input { return &openapi.Input{Body: b} }
		for _, tt := range []walkCase{
			{"JSON body", "json", body(m.v()), "Input.Body"},
			{"inside a JSON body", "json", body(map[string]any{"a": m.v()}), "Input.Body"},
			{"text body", "text", body(m.v()), "Input.Body"},
			{"form JSON field", "form", body(map[string]any{"o": m.v()}), "Input.Body/o"},
			{"multipart JSON part", "mp", body(map[string]any{"o": m.v()}), "Input.Body/o"},
			{"JSON Lines item", "jsonl", body([]any{m.v()}), "Input.Body/0"},
			{"styled query parameter", "query", &openapi.Input{Params: map[string]any{"q": m.v()}}, "q"},
		} {
			t.Run(m.name+", "+tt.name, func(t *testing.T) {
				noPanic(t, "Call", func() {
					w, c := walkClient(t)
					resp, err := c.Call(t.Context(), tt.key, tt.in, nil)
					re := refusedBeforeSending(t, w, resp, err)
					wantKeys(t, "Inputs", re.Inputs, true, tt.at)
					if !unsupportedType(err) {
						t.Errorf("refusal %v does not keep encoding/json's *UnsupportedTypeError", err)
					}
				})
			})
		}
	}
}

// The same maps yielded by an iterator under JSON Lines abort the body with
// encoding/json's error, reported by Call (client.go, Input.Body: "An error
// from an iter.Seq2, an item that cannot be encoded ... aborts the body and is
// reported by Call"), from the fast path for iter.Seq[any] and from an
// iterator of a typed element. Previously a panic on net/http's write loop
// ended the process, so the cases run in a child process.
func TestUnsupportedMapKeyFromIteratorAbortsBody(t *testing.T) {
	if !inChild(t) {
		return
	}
	_, c := walkClient(t)
	for _, m := range refusedKeyMaps() {
		for _, body := range []struct {
			name string
			seq  any
		}{
			{"iter.Seq[any]", iter.Seq[any](func(yield func(any) bool) { yield(m.v()) })},
			{"iter.Seq2[any, error]", iter.Seq2[any, error](func(yield func(any, error) bool) { yield(m.v(), nil) })},
			{"iter.Seq[map]", func(yield func(any) bool) { yield(m.v()) }},
		} {
			t.Run(m.name+", "+body.name, func(t *testing.T) {
				r := awaitCall(t, callAsync(t.Context(), c, "jsonl", &openapi.Input{Body: body.seq}), "Call")
				if !unsupportedType(r.err) || isRequestError(r.err) {
					t.Errorf("Call = %v (%T), want the body aborted with encoding/json's *UnsupportedTypeError", r.err, r.err)
				}
			})
		}
	}
}

// Types whose JSON encoding/json decides by a method, by their pointer's
// only where it can address them.
type (
	jwValJSON     struct{ V int }
	jwPtrJSON     struct{ V int }
	jwValText     struct{ V int }
	jwPtrText     struct{ V int }
	jwPtrTextList []string // a slice whose pointer has MarshalText
	jwPtrJSONList []int
	jwInner       struct { // the methods one level inside a value
		J jwPtrJSON `json:"j"`
		T jwPtrText `json:"t"`
	}
	jwZeroVal struct{ N int }
	jwZeroPtr struct{ N int }
)

func (jwValJSON) MarshalJSON() ([]byte, error)        { return []byte(`"vj"`), nil }
func (*jwPtrJSON) MarshalJSON() ([]byte, error)       { return []byte(`"pj"`), nil }
func (jwValText) MarshalText() ([]byte, error)        { return []byte("vt"), nil }
func (*jwPtrText) MarshalText() ([]byte, error)       { return []byte("pt"), nil }
func (l *jwPtrTextList) MarshalText() ([]byte, error) { return []byte(strings.Join(*l, ",")), nil }
func (*jwPtrJSONList) MarshalJSON() ([]byte, error)   { return []byte(`"pjl"`), nil }
func (z jwZeroVal) IsZero() bool                      { return z.N == 7 }
func (z *jwZeroPtr) IsZero() bool                     { return z.N == 7 }

// jwSpy is an io.Reader that encoding/json, writing it by reflection,
// writes as {"Spy":"reached"}: the oracle for whether json reaches it.
type jwSpy struct{ Spy string }

func (jwSpy) Read([]byte) (int, error) { return 0, io.EOF }

const jwReached = `{"Spy":"reached"}`

func spy() jwSpy { return jwSpy{"reached"} }

// jwBoth has both of json's methods; jwMarshalers is an interface type that
// embeds both.
type (
	jwBoth       struct{}
	jwMarshalers interface {
		json.Marshaler
		encoding.TextMarshaler
	}
)

func (jwBoth) MarshalJSON() ([]byte, error) { return []byte(`"json"`), nil }
func (jwBoth) MarshalText() ([]byte, error) { return []byte("text"), nil }

// Readers behind methods: json reaches R only where it writes the holder by
// reflection.
type (
	jwPtrJSONReader struct{ R io.Reader } // pointer-receiver MarshalJSON
	jwPtrTextReader struct{ R io.Reader } // pointer-receiver MarshalText
	jwValJSONReader struct{ R io.Reader } // value-receiver MarshalJSON
	jwPlainReader   struct{ R io.Reader } // none: json reaches R
)

func (*jwPtrJSONReader) MarshalJSON() ([]byte, error) { return []byte(`"pjr"`), nil }
func (*jwPtrTextReader) MarshalText() ([]byte, error) { return []byte("ptr"), nil }
func (jwValJSONReader) MarshalJSON() ([]byte, error)  { return []byte(`"vjr"`), nil }

// Embedded structs, whose fields encoding/json's typeFields chooses: two
// untagged A at one depth cancel, the outer B dominates, and a tag C
// dominates an untagged C at its depth.
type (
	JwEmbedded struct {
		A, B, C int
	}
	jwEmbedded2 struct {
		A int
		X int `json:"x"`
		D int `json:"C"`
	}
)

// More of typeFields' choices: a type embedded at two depths, whose
// shallower fields dominate, and at one depth twice, whose fields cancel.
type (
	JwBase  struct{ K, L int }
	JwLeft  struct{ JwBase }
	JwRight struct{ JwBase }
)

// jwZeroer is an interface type with IsZero, which omitzero calls on what
// it holds (encoding/json: "If the field type has an IsZero() bool method,
// that will be used").
type jwZeroer interface{ IsZero() bool }

// Map keys of every kind encoding/json accepts: a string kind (whose
// MarshalText json does not use), integers, and TextMarshalers by value and
// by pointer.
type (
	jwStringKey string
	jwIntKey    int
	jwStructKey struct{ K string }
	jwPtrKey    string
)

func (k jwStringKey) MarshalText() ([]byte, error) { return []byte("unused-" + k), nil }
func (k jwIntKey) MarshalText() ([]byte, error)    { return []byte(fmt.Sprint("i", int(k))), nil }
func (k jwStructKey) MarshalText() ([]byte, error) { return []byte("s-" + k.K), nil }
func (k *jwPtrKey) MarshalText() ([]byte, error)   { return []byte("p-" + *k), nil }

// walkShapes are the shapes TestValueWalkMatchesEncodingJSON lists, each a
// fresh value.
func walkShapes() []struct {
	name string
	v    any
} {
	five := 5
	pk := jwPtrKey("q")
	return []struct {
		name string
		v    any
	}{
		{"value MarshalJSON", jwValJSON{1}},
		{"pointer MarshalJSON", jwPtrJSON{1}},
		{"value MarshalText", jwValText{1}},
		{"pointer MarshalText", jwPtrText{1}},
		{"slice with pointer MarshalText", jwPtrTextList{"a", "b"}},
		{"slice with pointer MarshalJSON", jwPtrJSONList{1, 2}},
		{"struct holding pointer methods", jwInner{}},
		{"omitempty", struct {
			A float64        `json:"a,omitempty"`
			B [0]int         `json:"b,omitempty"`
			S []int          `json:"s,omitempty"`
			M map[string]int `json:"m,omitempty"`
			Z struct{}       `json:"z,omitempty"`
			P *int           `json:"p,omitempty"`
			I any            `json:"i,omitempty"`
			T string         `json:"t,omitempty"`
			K bool           `json:"k,omitempty"`
		}{S: []int{}, M: map[string]int{}}},
		{"omitzero", struct {
			A jwZeroVal  `json:"a,omitzero"`
			B jwZeroPtr  `json:"b,omitzero"`
			C jwZeroPtr  `json:"c,omitzero"`
			P *jwZeroVal `json:"p,omitzero"`
			Q *jwZeroPtr `json:"q,omitzero"`
			T time.Time  `json:"t,omitzero"`
			U time.Time  `json:"u,omitzero"`
			I any        `json:"i,omitzero"`
			N int        `json:"n,omitzero"`
		}{A: jwZeroVal{7}, B: jwZeroPtr{7}, C: jwZeroPtr{1}, P: &jwZeroVal{7}, Q: &jwZeroPtr{7},
			U: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), I: jwZeroVal{7}}},
		{"the string option", struct {
			A int       `json:"a,string"`
			B *int      `json:"b,string"`
			C bool      `json:"c,string"`
			D string    `json:"d,string"`
			F float64   `json:"f,string"`
			M jwValJSON `json:"m,string"`
			L []int     `json:"l,string"`
			N *int      `json:"n,string"`
		}{A: 1, B: &five, C: true, D: "s", F: 1.5, L: []int{1}}},
		{"embedded structs", struct {
			JwEmbedded
			jwEmbedded2
			B string
		}{JwEmbedded{1, 2, 3}, jwEmbedded2{4, 5, 6}, "b"}},
		{"embedded at two depths", struct {
			JwBase
			JwLeft
		}{JwBase{1, 2}, JwLeft{JwBase{3, 4}}}},
		{"embedded twice at one depth", struct {
			JwLeft
			JwRight
			M int
		}{JwLeft{JwBase{1, 2}}, JwRight{JwBase{3, 4}}, 5}},
		{"a tag naming another field, after it", struct {
			X int
			Y int `json:"X"`
		}{1, 2}},
		{"a tag naming another field, before it", struct {
			Y int `json:"X"`
			X int
		}{1, 2}},
		{"a tag json does not take", struct {
			A int `json:"a'b"`
			B int `json:"a\\b"`
		}{1, 2}},
		{"omitzero on an interface with IsZero", struct {
			A jwZeroer `json:"a,omitzero"`
			B jwZeroer `json:"b,omitzero"`
			C jwZeroer `json:"c,omitzero"`
			D jwZeroer `json:"d,omitzero"`
			E jwZeroer `json:"e,omitzero"`
		}{B: (*jwZeroPtr)(nil), C: jwZeroVal{7}, D: jwZeroVal{1}, E: &jwZeroPtr{7}}},
		{"embedded nil pointer", struct {
			*JwEmbedded
			X int `json:"x"`
		}{X: 1}},
		{"embedded pointer", struct{ *JwEmbedded }{&JwEmbedded{1, 2, 3}}},
		{"string keys", map[string]int{"b": 1, "a": 2}},
		{"string-kind keys with MarshalText", map[jwStringKey]int{"b": 1, "a": 2}},
		{"int keys", map[int]string{2: "b", 10: "a", -3: "c"}},
		{"int8 keys", map[int8]int{-1: 1, 2: 2}},
		{"int16 keys", map[int16]int{300: 1}},
		{"int32 keys", map[int32]int{-70000: 1}},
		{"int64 keys", map[int64]int{1 << 40: 1}},
		{"uint keys", map[uint]int{7: 1, 70: 2}},
		{"uint8 keys", map[uint8]int{255: 1}},
		{"uint16 keys", map[uint16]int{65535: 1}},
		{"uint32 keys", map[uint32]int{1 << 31: 1}},
		{"uint64 keys", map[uint64]int{1 << 63: 1}},
		{"uintptr keys", map[uintptr]int{9: 1}},
		{"int-kind TextMarshaler keys", map[jwIntKey]int{2: 1, 10: 2}},
		{"struct TextMarshaler keys", map[jwStructKey]int{{"b"}: 1, {"a"}: 2}},
		{"pointer TextMarshaler keys", map[*jwPtrKey]int{&pk: 1, nil: 2}},
		{"typed nils", struct {
			P *int             `json:"p"`
			M map[string]int   `json:"m"`
			S []int            `json:"s"`
			I any              `json:"i"`
			R io.Reader        `json:"r"`
			F *jwSpy           `json:"f"`
			J *jwValJSON       `json:"j"`
			K *jwPtrJSON       `json:"k"`
			Q []byte           `json:"q"`
			L *jwPtrTextList   `json:"l"`
			X *jwPtrJSONReader `json:"x"`
		}{R: (*jwSpy)(nil)}},
		{"interface fields whose types have methods", struct {
			T  encoding.TextMarshaler `json:"t"`
			J  json.Marshaler         `json:"j"`
			B  jwMarshalers           `json:"b"`
			A  any                    `json:"a"`
			TP encoding.TextMarshaler `json:"tp"`
			TN encoding.TextMarshaler `json:"tn"`
			JN json.Marshaler         `json:"jn"`
		}{T: jwBoth{}, J: jwBoth{}, B: jwBoth{}, A: jwBoth{}, TP: &jwBoth{}}},
		{"a slice of encoding.TextMarshaler", []encoding.TextMarshaler{jwBoth{}, &jwBoth{}, nil}},
		{"a map of encoding.TextMarshaler", map[string]encoding.TextMarshaler{"k": jwBoth{}, "n": nil}},
		{"reader in a pointer MarshalJSON", jwPtrJSONReader{spy()}},
		{"reader in a pointer MarshalText", jwPtrTextReader{spy()}},
		{"reader in a value MarshalJSON", jwValJSONReader{spy()}},
		{"reader in a plain struct", jwPlainReader{spy()}},
	}
}

// A walkPos places a shape somewhere in a body: s is the shape's value,
// which cannot be addressed.
type walkPos struct {
	name string
	wrap func(s reflect.Value) any
}

func walkPositions() []walkPos {
	field := func(t reflect.Type, name, tag string) reflect.Type {
		return reflect.StructOf([]reflect.StructField{{Name: name, Type: t, Tag: reflect.StructTag(`json:"` + tag + `"`)}})
	}
	newOf := func(t reflect.Type) reflect.Value { return reflect.New(t) }
	return []walkPos{
		{"top level", func(s reflect.Value) any { return s.Interface() }},
		{"top level, by pointer", func(s reflect.Value) any {
			p := newOf(s.Type())
			p.Elem().Set(s)
			return p.Interface()
		}},
		{"a field of a struct by pointer", func(s reflect.Value) any {
			p := newOf(field(s.Type(), "A", "a"))
			p.Elem().Field(0).Set(s)
			return p.Interface()
		}},
		{"a field of a struct by value", func(s reflect.Value) any {
			p := newOf(field(s.Type(), "A", "a"))
			p.Elem().Field(0).Set(s)
			return p.Elem().Interface()
		}},
		{"inside a field of a struct by pointer", func(s reflect.Value) any {
			p := newOf(field(field(s.Type(), "G", "g"), "A", "a"))
			p.Elem().Field(0).Field(0).Set(s)
			return p.Interface()
		}},
		{"inside a field of a struct by value", func(s reflect.Value) any {
			p := newOf(field(field(s.Type(), "G", "g"), "A", "a"))
			p.Elem().Field(0).Field(0).Set(s)
			return p.Elem().Interface()
		}},
		{"a slice element", func(s reflect.Value) any {
			l := reflect.MakeSlice(reflect.SliceOf(s.Type()), 1, 1)
			l.Index(0).Set(s)
			return map[string]any{"a": l.Interface()}
		}},
		{"a slice element inside a field of a struct by value", func(s reflect.Value) any {
			l := reflect.MakeSlice(reflect.SliceOf(s.Type()), 1, 1)
			l.Index(0).Set(s)
			p := newOf(field(field(l.Type(), "G", "g"), "A", "a"))
			p.Elem().Field(0).Field(0).Set(l)
			return p.Elem().Interface()
		}},
		{"an array element of a struct by pointer", func(s reflect.Value) any {
			p := newOf(field(reflect.ArrayOf(1, s.Type()), "A", "a"))
			p.Elem().Field(0).Index(0).Set(s)
			return p.Interface()
		}},
		{"an array element in a map", func(s reflect.Value) any {
			a := newOf(reflect.ArrayOf(1, s.Type())).Elem()
			a.Index(0).Set(s)
			return map[string]any{"a": a.Interface()}
		}},
		{"a map value", func(s reflect.Value) any {
			m := reflect.MakeMap(reflect.MapOf(reflect.TypeFor[string](), s.Type()))
			m.SetMapIndex(reflect.ValueOf("a"), s)
			return m.Interface()
		}},
		{"a map value inside a property", func(s reflect.Value) any {
			m := reflect.MakeMap(reflect.MapOf(reflect.TypeFor[string](), s.Type()))
			m.SetMapIndex(reflect.ValueOf("g"), s)
			return map[string]any{"a": m.Interface()}
		}},
		{"behind a pointer in a property", func(s reflect.Value) any {
			p := newOf(s.Type())
			p.Elem().Set(s)
			return map[string]any{"a": p.Interface()}
		}},
	}
}

// jsonMember is one member of a JSON object, its value's JSON text.
type jsonMember struct {
	name string
	raw  json.RawMessage
}

// jsonMembersOf returns the members of the JSON object b in order.
func jsonMembersOf(t *testing.T, b []byte) []jsonMember {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("%s is not an object", b)
	}
	var ms []jsonMember
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		ms = append(ms, jsonMember{tok.(string), raw})
	}
	return ms
}

// fieldsOf is what a form or multipart body whose every field is JSON
// carries for the JSON object b (doc.go, Values: "A form or multipart
// property or array item, or a positional part ..., whose JSON data is null
// is omitted"; client.go, Input.Body: "a property whose value is an array sends
// one field or part per item under the property's name"): each member's JSON
// text, an array one per item, null left out.
func fieldsOf(t *testing.T, b []byte) []jsonMember {
	t.Helper()
	var out []jsonMember
	for _, m := range jsonMembersOf(t, b) {
		switch {
		case string(m.raw) == "null":
		case m.raw[0] == '[':
			var items []json.RawMessage
			if err := json.Unmarshal(m.raw, &items); err != nil {
				t.Fatal(err)
			}
			for _, it := range items {
				if string(it) != "null" {
					out = append(out, jsonMember{m.name, it})
				}
			}
		default:
			out = append(out, m)
		}
	}
	return out
}

// spyPointer returns the JSON Pointer, from the root of the JSON text b, of
// the one jwSpy encoding/json wrote in it, and whether there is one.
func spyPointer(t *testing.T, b []byte) (string, bool) {
	t.Helper()
	if !bytes.Contains(b, []byte(jwReached)) {
		return "", false
	}
	if bytes.Count(b, []byte(jwReached)) > 1 {
		t.Fatalf("test bug: %s reaches two readers", b)
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		t.Fatal(err)
	}
	var find func(v any) (string, bool)
	find = func(v any) (string, bool) {
		switch v := v.(type) {
		case map[string]any:
			if len(v) == 1 && v["Spy"] == "reached" {
				return "", true
			}
			for k, x := range v {
				if p, ok := find(x); ok {
					return "/" + jsonPtr(k) + p, true
				}
			}
		case []any:
			for i, x := range v {
				if p, ok := find(x); ok {
					return fmt.Sprintf("/%d%s", i, p), true
				}
			}
		}
		return "", false
	}
	p, ok := find(tree)
	if !ok {
		t.Fatalf("test bug: no reader found in %s", b)
	}
	return p, true
}

// sentBody returns the body a prepared request sends: none when it is empty,
// else what GetBody gives, the body being one the client encodes, which can
// be sent again (client.go, Input.Body: "every value the client encodes").
func sentBody(t *testing.T, req *openapi.Request) []byte {
	t.Helper()
	if req.HTTP.Body == nil || req.HTTP.Body == http.NoBody {
		return nil
	}
	return preparedBody(t, req)
}

// jwFieldsDoc is a document with a JSON body, and a form and a multipart
// body whose properties are names, each an object, so a JSON field
// (OpenAPI 3.1.2 section 4.8.15.1.1: application/json for an object).
func jwFieldsDoc(names []string) string {
	var props []string
	for _, n := range names {
		q, _ := json.Marshal(n)
		props = append(props, string(q)+`:{"type":"object"}`)
	}
	schema := `{"type":"object","properties":{` + strings.Join(props, ",") + `}}`
	return doc31(`"/j":{"post":{"operationId":"json","requestBody":{"content":{"application/json":{}}}}},
		"/f":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":` + schema + `}}}}},
		"/m":{"post":{"operationId":"mp","requestBody":{"content":{"multipart/form-data":{"schema":` + schema + `}}}}}`)
}

// A differential table against json.Marshal. Every shape (value and pointer
// receiver MarshalJSON and MarshalText, omitempty, omitzero with IsZero on
// value and pointer receivers, ",string", embedded structs, every map key kind
// json accepts, typed nils, readers behind methods) at every position (top
// level, a field, inside a field, a slice element, an array element, a map
// value, behind a pointer; addressable and not) is sent as encoding/json
// writes it:
//
//   - a JSON body is json.Marshal's bytes (doc.go, Values: "a JSON type is
//     written as json.Marshal writes the value");
//   - a form or multipart body whose fields are JSON carries, field by field,
//     the JSON data json.Marshal gives each property (doc.go, Values: "The
//     client first converts a value to JSON data as encoding/json would"),
//     an array property one field per item, null left out; a value json does
//     not write as an object is refused at Input.Body;
//   - a reader json reaches is refused at its place (client.go, Input.Body),
//     and one json never reaches, inside a type json writes by a pointer
//     method where it is addressable, is not refused.
//
// json.Marshal's refusals are the client's. Previously the walk lost a
// pointer method one level inside a field's value, or under a pointer or
// addressable array element, and split a slice whose pointer has
// MarshalText.
func TestValueWalkMatchesEncodingJSON(t *testing.T) {
	for _, sh := range walkShapes() {
		for _, pos := range walkPositions() {
			t.Run(sh.name+" at "+pos.name, func(t *testing.T) { againstJSON(t, pos.wrap(reflect.ValueOf(sh.v))) })
		}
	}
}

// againstJSON sends body as a JSON, a form and a multipart body, checking
// each against what json.Marshal writes for it, as TestValueWalkMatchesEncodingJSON
// says.
func againstJSON(t *testing.T, body any) {
	t.Helper()
	want, jerr := json.Marshal(body)
	spyAt, reached := "", false
	if jerr == nil {
		spyAt, reached = spyPointer(t, want)
	}
	object := jerr == nil && want[0] == '{'
	var names []string
	if object {
		for _, m := range jsonMembersOf(t, want) {
			names = append(names, m.name)
		}
	}
	c := parseAt(t, jwFieldsDoc(names), "https://api.example.test", testDocURI, nil)
	check := func(key string, f func(req *openapi.Request)) {
		t.Helper()
		req, err := c.Prepare(key, &openapi.Input{Body: body})
		switch {
		case key != "json" && reached && strings.Count(spyAt, "/") == 1:
			// The reader is a property of the body, which a form or
			// multipart body sends as its content (client.go,
			// Input.Body: "A property may be a []byte, an io.Reader
			// or a [Part]"), not as JSON.
		case jerr != nil:
			if !isRequestError(err) {
				t.Errorf("%s: json.Marshal refuses (%v); Prepare = %v", key, jerr, err)
			}
		case key != "json" && !object:
			if err == nil {
				t.Errorf("%s: json writes %s, no object; Prepare sent %q", key, want, sentBody(t, req))
				return
			}
			wantKeys(t, key+" Inputs", asRequestError(t, err).Inputs, true, "Input.Body")
		case reached:
			if err == nil {
				t.Errorf("%s: json reaches the reader (%s); Prepare sent %q", key, want, sentBody(t, req))
				return
			}
			wantKeys(t, key+" Inputs", asRequestError(t, err).Inputs, true, "Input.Body"+spyAt)
		case err != nil:
			t.Errorf("%s: json writes %s; Prepare refused: %v", key, want, err)
		default:
			f(req)
		}
	}
	check("json", func(req *openapi.Request) {
		if got := sentBody(t, req); !bytes.Equal(got, want) {
			t.Errorf("JSON body %s, json.Marshal writes %s", got, want)
		}
	})
	check("form", func(req *openapi.Request) {
		var pairs []string
		for _, f := range fieldsOf(t, want) {
			pairs = append(pairs, formPairs(f.name, string(f.raw)))
		}
		if got, w := string(sentBody(t, req)), strings.Join(pairs, "&"); got != w {
			t.Errorf("form body %q, want %q from json.Marshal's %s", got, w, want)
		}
	})
	check("mp", func(req *openapi.Request) {
		var parts []wantPart
		for _, f := range fieldsOf(t, want) {
			parts = append(parts, wantPart{disposition: formData(f.name), ctype: "application/json", content: string(f.raw)})
		}
		_, _, got := readMultipart(t, req.HTTP.Header.Get("Content-Type"), sentBody(t, req))
		checkParts(t, got, parts)
	})
}

// jwReadByPtr is no io.Reader, but its pointer is, and it holds a type whose
// pointer has MarshalJSON.
type jwReadByPtr struct {
	J jwPtrJSON `json:"j"`
	V int       `json:"v"`
}

func (*jwReadByPtr) Read([]byte) (int, error) { return 0, io.EOF }

// A struct that is not a reader but whose pointer is, held addressably with a
// pointer-method type inside, is written as json writes it, the reader test
// applying to the value json writes and never to the pointer handed for its
// methods. At every position that holds the value, addressable or not, the
// JSON, form and multipart bodies are what json.Marshal writes
// (TestValueWalkMatchesEncodingJSON's checks). A position where the caller
// gives the pointer itself, an io.Reader, is the raw-content case of
// Input.Body and is not among these.
func TestPointerReaderHeldAddressablyIsValue(t *testing.T) {
	for _, pos := range walkPositions() {
		if pos.name == "top level, by pointer" || pos.name == "behind a pointer in a property" {
			continue
		}
		t.Run(pos.name, func(t *testing.T) { againstJSON(t, pos.wrap(reflect.ValueOf(jwReadByPtr{V: 1}))) })
	}
	b, _ := json.Marshal(&struct {
		A jwReadByPtr `json:"a"`
	}{})
	if string(b) != `{"a":{"j":"pj","v":0}}` {
		t.Fatalf("test bug: json.Marshal writes %s", b)
	}
}

// Keep the reflection helpers honest: a slice element and a field of a
// struct by pointer are addressable to encoding/json, a map value and a
// field of a struct by value are not, as the table assumes.
func TestValueWalkPositionsAddressability(t *testing.T) {
	want := map[string]string{
		"top level":                                           `{"V":1}`,
		"top level, by pointer":                               `"pj"`,
		"a field of a struct by pointer":                      `{"a":"pj"}`,
		"a field of a struct by value":                        `{"a":{"V":1}}`,
		"inside a field of a struct by pointer":               `{"a":{"g":"pj"}}`,
		"inside a field of a struct by value":                 `{"a":{"g":{"V":1}}}`,
		"a slice element":                                     `{"a":["pj"]}`,
		"a slice element inside a field of a struct by value": `{"a":{"g":["pj"]}}`,
		"an array element of a struct by pointer":             `{"a":["pj"]}`,
		"an array element in a map":                           `{"a":[{"V":1}]}`,
		"a map value":                                         `{"a":{"V":1}}`,
		"a map value inside a property":                       `{"a":{"g":{"V":1}}}`,
		"behind a pointer in a property":                      `{"a":"pj"}`,
	}
	positions := walkPositions()
	if len(positions) != len(want) {
		t.Fatalf("%d positions, %d expectations", len(positions), len(want))
	}
	for _, pos := range positions {
		got, err := json.Marshal(pos.wrap(reflect.ValueOf(jwPtrJSON{1})))
		if err != nil || string(got) != want[pos.name] {
			t.Errorf("%s: json.Marshal = %s, %v; want %s", pos.name, got, err, want[pos.name])
		}
	}
}
