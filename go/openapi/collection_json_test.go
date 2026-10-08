package openapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Caller-owned JSON is returned directly; this method itself neither builds
// nor copies it. This separates caller allocation from client work below.
type callerReturnedJSON struct {
	data []byte
	err  error
}

func (v callerReturnedJSON) MarshalJSON() ([]byte, error) { return v.data, v.err }

type pointerReceiverJSON []string

func (*pointerReceiverJSON) MarshalJSON() ([]byte, error) {
	return []byte(`["pointer result", ""]`), nil
}

// doc.go, Values: optimized collection traversal must keep encoding/json's
// number grammar and spelling, method selection, addressability, JSON
// whitespace and escaped-string decoding. JSON data, not the Go type's
// underlying kind, governs serialization. The oracle uses the standard
// encoder and decoder (UseNumber), then the established RFC byte encoder.
func TestCollectionItemsFollowJSONData(t *testing.T) {
	custom := marshalerStrings{"ignored"}
	customPointer := &custom
	pointerMethod := pointerReceiverJSON{"ordinary", ""}
	addressable := [2]ptrJSON{"first", "second"}
	spaced := callerReturnedJSON{data: []byte(" \n\t[ \"a\\u0020b\", \"quote\\\"slash\\\\\", \"\\uD83D\\uDE00\", 12.50, 1e+2, null ]\r\n")}
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"json.Number slice", []json.Number{"", "12.50", "1e+2", "-0"}},
		{"json.Number interface", []any{json.Number(""), json.Number("9007199254740993"), json.Number("1E-2")}},
		{"custom array", custom},
		{"pointer custom array", customPointer},
		{"wrapped custom array", &customPointer},
		{"pointer-method value", pointerMethod},
		{"pointer-method pointer", &pointerMethod},
		{"addressable slice elements", []ptrJSON{"first", "second"}},
		{"unaddressable array elements", addressable},
		{"addressable array elements", &addressable},
		{"custom whitespace and escapes", spaced},
		{"pointer whitespace and escapes", &spaced},
	} {
		for _, cf := range []string{"csv", "multi"} {
			t.Run(tc.name+"/"+cf, func(t *testing.T) {
				raw, err := json.Marshal(tc.v)
				if err != nil {
					t.Fatal(err)
				}
				dec := json.NewDecoder(strings.NewReader(string(raw)))
				dec.UseNumber()
				var values []any
				if err := dec.Decode(&values); err != nil {
					t.Fatal(err)
				}
				var encoded []string
				for _, value := range values {
					if value == nil {
						continue
					}
					encoded = append(encoded, pctName(fmt.Sprint(value)))
				}
				separator := ","
				if cf == "multi" {
					separator = "&p="
				}
				want := "p=" + strings.Join(encoded, separator)
				c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"p","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"`+cf+`"}]}}`), nil)
				got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"p": tc.v}}).HTTP.URL.RawQuery
				if got != want {
					t.Errorf("query %q want %q from JSON %s", got, want, raw)
				}
			})
		}
	}
}

// Standard JSON errors remain observable; a malformed number or invalid
// custom JSON is not accepted as a string merely because its Go kind is one.
func TestCollectionJSONErrorsRefused(t *testing.T) {
	cause := errors.New("caller JSON failure")
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"leading zero", []json.Number{"01"}},
		{"not a number", []any{json.Number("NaN")}},
		{"empty exponent", []json.Number{"1e"}},
		{"invalid escape", callerReturnedJSON{data: []byte(`["bad\q"]`)}},
		{"trailing input", callerReturnedJSON{data: []byte(`["ok"] false`)}},
		{"truncated array", callerReturnedJSON{data: []byte(`["ok",`)}},
		{"caller error", callerReturnedJSON{err: cause}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, standard := json.Marshal(tc.v)
			if standard == nil {
				t.Fatal("invalid test value accepted by encoding/json")
			}
			c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"p","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"csv"}]}}`), nil)
			_, err := c.Prepare("GET /x", &openapi.Input{Params: map[string]any{"p": tc.v}})
			re := asRequestError(t, err)
			wantKeys(t, "Inputs", re.Inputs, true, "p")
			var standardMarshaler *json.MarshalerError
			if errors.As(standard, &standardMarshaler) {
				var got *json.MarshalerError
				if !errors.As(err, &got) {
					t.Errorf("missing standard MarshalerError: %v", err)
				}
			}
			if tc.name == "caller error" && !errors.Is(err, cause) {
				t.Errorf("caller error lost: %v", err)
			}
			// Generated presentation redacts encoding details; the original
			// standard error remains available by unwrapping the input error.
			found := false
			for cause := re.Inputs["p"]; cause != nil; cause = errors.Unwrap(cause) {
				found = found || cause.Error() == standard.Error()
			}
			if !found {
				t.Errorf("standard error %q not retained in input error chain", standard)
			}
		})
	}
}

// The undefined-value rules (doc.go, Values; RFC 6570 section 2.3) and
// TestEditionsNestedCollectionAndUndefined apply equally when the nested JSON
// arrives from a custom marshaler. Empty arrays/all-undefined objects
// disappear, but a nonempty [null] contributes the empty final collection
// member and therefore its outer delimiter.
func TestCustomCollectionUndefinedNestedMembers(t *testing.T) {
	v := callerReturnedJSON{data: []byte(` [null, ["a","b"], [], {"skip":null}, [null]] `)}
	p := &v
	c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"p","in":"query","type":"array","collectionFormat":"pipes","items":{"type":"array","collectionFormat":"ssv","items":{"type":"string"}}}]}}`), nil)
	for _, value := range []any{v, p, &p} {
		got := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"p": value}}).HTTP.URL.RawQuery
		if got != "p=a%20b%7C" {
			t.Errorf("query %q", got)
		}
	}
}

// Regression check, not contract: the 16 MiB refusal allocation ceiling. The
// rest is contract: the 1 MiB serialization stop applies after custom JSON is
// available, through pointer wrappers too (doc.go, Values: a parameter past 1
// MiB "is refused at its key, and its serialization stops there"). The caller
// returns prebuilt bytes, so measured allocation is the client's.
func TestCustomCollectionStopsAtTargetLimit(t *testing.T) {
	var b strings.Builder
	b.Grow((8192+3)*4096 + 2)
	b.WriteByte('[')
	chunk := strings.Repeat("x", 8192)
	for i := range 4096 {
		if i != 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(chunk)
		b.WriteByte('"')
	}
	b.WriteByte(']')
	v := callerReturnedJSON{data: []byte(b.String())}
	p := &v
	malformed := append(append([]byte(nil), v.data...), '!')
	number := json.Number(strings.Repeat("1", 32<<20))
	for _, tc := range []struct {
		name string
		v    any
	}{{"direct", v}, {"pointer wrapped", &p}, {"late malformed JSON", callerReturnedJSON{data: malformed}}, {"large json.Number", number}} {
		t.Run(tc.name, func(t *testing.T) {
			c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"p","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"csv"}]}}`), nil)
			mustOp(t, c, "GET /x")
			in := &openapi.Input{Params: map[string]any{"p": tc.v}}
			var req *openapi.Request
			var err error
			cost := allocatedBy(func() { req, err = c.Prepare("GET /x", in) })
			if req != nil || err == nil {
				t.Fatal("oversized custom collection prepared")
			}
			wantKeys(t, "Inputs", asRequestError(t, err).Inputs, true, "p")
			if cost > 16<<20 {
				t.Errorf("refusal allocated %d bytes; existing request-limit ceiling is 16 MiB", cost)
			}
		})
	}
}

// encoding/json stops at a nil marshaler pointer, even behind another
// pointer, without calling its method. pointerReceiverJSON's method would
// produce a defined array if wrongly called, so omission is observable.
func TestNilMarshalerPointerCollectionOmitted(t *testing.T) {
	var p *pointerReceiverJSON
	c := editionClient(t, editionDoc("2.0", `"/x":{"get":{"parameters":[{"name":"p","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"csv"}]}}`), nil)
	for _, v := range []any{p, &p} {
		raw, err := json.Marshal(v)
		if err != nil || string(raw) != "null" {
			t.Fatalf("standard JSON %s: %v", raw, err)
		}
		req := mustPrepare(t, c, "GET /x", &openapi.Input{Params: map[string]any{"p": v}})
		if got := req.HTTP.URL.RawQuery; got != "" {
			t.Errorf("nil marshaler query %q", got)
		}
	}
}
