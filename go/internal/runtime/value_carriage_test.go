package openapiclient

import (
	"encoding/json"
	"testing"
)

func TestTypedGenericInputCarriage(t *testing.T) {
	object, ok := toStringAnyMap(struct {
		N uint64 `json:"n"`
	}{18446744073709551615})
	if !ok || object["n"] != json.Number("18446744073709551615") {
		t.Fatalf("object changed: %#v", object)
	}
	items, ok := toAnySlice([]uint64{18446744073709551615})
	if !ok || items[0] != json.Number("18446744073709551615") {
		t.Fatalf("array changed: %#v", items)
	}
	cloned := cloneStringAnyMap(map[string]any{"nested": map[string]any{"n": json.Number("1e400")}})
	if cloned["nested"].(map[string]any)["n"] != json.Number("1e400") {
		t.Fatalf("clone changed: %#v", cloned)
	}
}

func TestMalformedNumericCarriersNeverBecomeWireValues(t *testing.T) {
	for _, token := range []string{"", "01", "NaN", "1 2"} {
		value := json.Number(token)
		for _, input := range []any{value, map[string]any{"n": value}, []any{value}} {
			if data, err := marshalRequestJSON(input); err == nil {
				t.Errorf("malformed number %q encoded as %s", token, data)
			}
		}
		if value, err := primitiveString(value); err == nil {
			t.Errorf("malformed primitive %q serialized as %q", token, value)
		}
	}
	for _, token := range []string{"0", "9007199254740993", "1e400", "1e-400"} {
		if data, err := marshalRequestJSON(json.Number(token)); err != nil || string(data) != token {
			t.Errorf("valid number %s changed: %s (%v)", token, data, err)
		}
	}
}
