package openapiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestParameterConverterAndUndefinedCells(t *testing.T) {
	converted, err := convertParameterScalars(map[string]any{
		"flag": true,
		"list": []any{json.Number("2.5"), "already-text"},
	}, func(value any) (string, error) {
		return fmt.Sprintf("converted<%v>", value), nil
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"flag": "converted<true>",
		"list": []any{"converted<2.5>", "already-text"},
	}
	if !reflect.DeepEqual(converted, want) {
		t.Fatalf("converted = %#v, want %#v", converted, want)
	}
	if _, err := convertParameterScalars(float64(7), nil, false); err == nil || !strings.Contains(err.Error(), "ParameterConverter") {
		t.Fatalf("missing converter error = %v", err)
	}
	if _, err := convertParameterScalars([]any{"ok", nil}, nil, false); err == nil || !strings.Contains(err.Error(), "null array/object member") {
		t.Fatalf("null member error = %v", err)
	}

	for _, testCase := range []struct {
		style string
		wire  string
	}{
		{style: openapi3.SerializationMatrix, wire: ";id"},
		{style: openapi3.SerializationLabel, wire: "."},
		{style: openapi3.SerializationSimple, wire: ""},
		{style: openapi3.SerializationForm, wire: "id="},
	} {
		prepared, err := prepareParameterStyleValue("id", nil, testCase.style, nil)
		if err != nil {
			t.Fatal(err)
		}
		var wire string
		if testCase.style == openapi3.SerializationForm {
			units, err := serializeQueryValueForRevision("id", prepared, testCase.style, true, false, profileFullCoordinate, false, false)
			if err != nil {
				t.Fatal(err)
			}
			wire = strings.Join(units, "&")
		} else {
			wire, err = serializePathValueForRevision("id", prepared, testCase.style, false, profileFullCoordinate)
			if err != nil {
				t.Fatal(err)
			}
		}
		if wire != testCase.wire {
			t.Fatalf("style %q wire = %q, want %q", testCase.style, wire, testCase.wire)
		}
	}
	for _, style := range []string{openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited, openapi3.SerializationDeepObject} {
		if _, err := prepareParameterStyleValue("id", nil, style, nil); err == nil {
			t.Errorf("null unexpectedly admitted for style %q", style)
		}
	}
}

// RFC 6570 §2.3's undefined set has three JSON images here, not one: JSON
// null, a zero-member array and a zero-member object. All three take the
// effective style's undefined cell, on both explode rows.
func TestUndefinedValueSetIsThreeJSONImages(t *testing.T) {
	esc := func(value string) string { return value }
	for _, supplied := range []struct {
		name  string
		value any
	}{
		{"null", nil},
		{"empty array", []any{}},
		{"empty object", map[string]any{}},
	} {
		for _, explode := range []bool{false, true} {
			prepared, err := prepareParameterStyleValue("q", supplied.value, openapi3.SerializationForm, nil)
			if err != nil {
				t.Fatalf("%s form: %v", supplied.name, err)
			}
			units, err := expandFormPairs("q", prepared, explode, esc)
			if err != nil || len(units) != 1 || units[0] != "q=" {
				t.Fatalf("%s form explode=%v: units=%q err=%v, want [q=]", supplied.name, explode, units, err)
			}
			prepared, err = prepareParameterStyleValue("p", supplied.value, openapi3.SerializationMatrix, nil)
			if err != nil {
				t.Fatalf("%s matrix: %v", supplied.name, err)
			}
			wire, err := expandMatrix("p", prepared, explode, esc)
			if err != nil || wire != ";p" {
				t.Fatalf("%s matrix explode=%v: wire=%q err=%v, want ;p", supplied.name, explode, wire, err)
			}
		}
		// A style whose undefined cell is n/a refuses before dispatch.
		for _, style := range []string{
			openapi3.SerializationSpaceDelimited,
			openapi3.SerializationPipeDelimited,
			openapi3.SerializationDeepObject,
		} {
			if _, err := prepareParameterStyleValue("q", supplied.value, style, nil); err == nil ||
				!strings.Contains(err.Error(), "undefined cell") {
				t.Fatalf("%s style %s: err = %v, want an n/a undefined-cell refusal", supplied.name, style, err)
			}
		}
	}
	// The empty string is expressly NOT undefined.
	if undefinedStyleValue("") {
		t.Fatal("the empty string must not be an undefined value")
	}
}

func TestParameterDelimiterHeaderAndCookieRefusals(t *testing.T) {
	for _, testCase := range []struct {
		style string
		name  string
		value any
	}{
		{style: openapi3.SerializationSpaceDelimited, name: "q", value: []any{"contains space"}},
		{style: openapi3.SerializationPipeDelimited, name: "q", value: []any{"contains|pipe"}},
		{style: openapi3.SerializationDeepObject, name: "filter", value: map[string]any{"a&b": "value"}},
		{style: openapi3.SerializationDeepObject, name: "filter", value: map[string]any{"key": "a=b"}},
	} {
		if _, err := prepareParameterStyleValue(testCase.name, testCase.value, testCase.style, nil); err == nil || !strings.Contains(err.Error(), "structural delimiter") {
			t.Errorf("style %q value %#v error = %v", testCase.style, testCase.value, err)
		}
	}

	header := &openapi3.Parameter{Name: "X-Test", In: openapi3.ParameterInHeader, Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}}
	for _, value := range []string{"safe\r\nInjected: yes", "safe\x00unsafe"} {
		if _, _, err := prepareParameterValue(header, value, nil); err == nil || !strings.Contains(err.Error(), "RFC 9110 field-value") {
			t.Errorf("header value %q error = %v", value, err)
		}
	}

	cookie := &openapi3.Parameter{Name: "parts", In: openapi3.ParameterInCookie, Schema: &openapi3.SchemaRef{Value: &openapi3.Schema{}}}
	if _, _, err := prepareParameterValue(cookie, []any{"a", "b"}, nil); err == nil || !strings.Contains(err.Error(), "multi-value Cookie representation") {
		t.Fatalf("multi-pair cookie error = %v", err)
	}
}

func TestClientParameterConverterAndNativeRawCookiePath(t *testing.T) {
	var queries []string
	var cookies []string
	dispatches := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dispatches++
		queries = append(queries, request.URL.RawQuery)
		cookies = append(cookies, request.Header.Get("Cookie"))
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	document := testDocument(server.URL, `{
	  "/x":{"get":{"parameters":[
	    {"name":"q","in":"query","schema":{"type":"number"}},
	    {"name":"Cookie","in":"header","schema":{"type":"string"}},
	    {"name":"session","in":"cookie","schema":{"type":"string"}}
	  ],"responses":{"204":{"description":"ok"}}}}
	}`)
	client, err := Load(context.Background(), Source{Content: document}, ClientOptions{
		ParameterConverter: func(any) (string, error) { return "configured-seven", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Call(context.Background(), PathOperation("/x", GET), Input{Parameters: Parameters{
		Query:  map[string]any{"q": float64(7)},
		Header: map[string]any{"Cookie": "raw=1"},
	}})
	if err != nil {
		t.Fatalf("raw-only call: %v", err)
	}
	if queries[0] != "q=configured-seven" || cookies[0] != "raw=1" {
		t.Fatalf("first request query/cookie = %q / %q", queries[0], cookies[0])
	}

	_, err = client.Call(context.Background(), PathOperation("/x", GET), Input{Parameters: Parameters{
		Cookie: map[string]any{"session": "structured"},
	}})
	if err != nil {
		t.Fatalf("structured-only call: %v", err)
	}
	if cookies[1] != "session=structured" {
		t.Fatalf("structured Cookie = %q", cookies[1])
	}

	_, err = client.Call(context.Background(), PathOperation("/x", GET), Input{Parameters: Parameters{
		Header: map[string]any{"Cookie": "raw=1"},
		Cookie: map[string]any{"session": "structured"},
	}})
	if err == nil {
		t.Fatal("raw and structured cookie contributions unexpectedly dispatched")
	}
	if dispatches != 2 {
		t.Fatalf("collision dispatched; total dispatches = %d", dispatches)
	}
}
