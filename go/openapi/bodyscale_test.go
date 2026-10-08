package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Body scaling tests, with the harness of scaling_test.go (four times
// the input within 12 times the time, best of 5, or 8 times the allocations
// or bytes). Every compiled or decoded form is computed at most once per
// document node, and a read costs O(size of what it returns or compares): a
// document with many properties and encodings compiles linearly; a body with
// many fields or items costs O(its encoded size); a deep value is refused at
// 1,000 levels before large allocation. The tests cover many properties and
// encodings (compile), a 100k-field form and a 100k-item sequential body
// (per call, O(size)), and a deep value.

// manyEncodings is a document whose one operation, "op", takes a
// multipart/form-data body of n properties, each with an Encoding Object
// (a content type list or range, two headers, or a style), and an
// application/x-www-form-urlencoded body of n properties whose Encodings
// cycle through every RFC 6570 configuration and a content type.
func manyEncodings(n int) []byte {
	var props, encs, fprops, fencs []string
	types := []string{`{"type":"string"}`, `{"type":"integer"}`, `{"type":"object"}`, `{"type":"array","items":{"type":"string"}}`, `{}`, `{"type":"string","contentEncoding":"base64"}`}
	mpEnc := []string{
		`{"contentType":"image/png, image/jpeg","headers":{"X-A":{"schema":{"type":"integer"}},"X-B":{"description":"b","schema":{}}}}`,
		`{"contentType":"image/*"}`,
		`{"contentType":"application/xml; charset=utf-8"}`,
		`{"style":"form","explode":false}`,
	}
	formEnc := []string{
		`{"style":"form","explode":false}`, `{"explode":true}`, `{"allowReserved":true}`, `{"style":"deepObject"}`,
		`{"style":"spaceDelimited"}`, `{"style":"pipeDelimited","explode":false}`, `{"contentType":"application/json"}`,
	}
	for i := range n {
		props = append(props, fmt.Sprintf(`"m%d":%s`, i, types[i%len(types)]))
		encs = append(encs, fmt.Sprintf(`"m%d":%s`, i, mpEnc[i%len(mpEnc)]))
		fprops = append(fprops, fmt.Sprintf(`"f%d":{}`, i))
		fencs = append(fencs, fmt.Sprintf(`"f%d":%s`, i, formEnc[i%len(formEnc)]))
	}
	return []byte(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{"/x":{"post":{"operationId":"op","requestBody":{"content":{` +
		`"multipart/form-data":{"schema":{"type":"object","properties":{` + strings.Join(props, ",") + `}},"encoding":{` + strings.Join(encs, ",") + `}},` +
		`"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{` + strings.Join(fprops, ",") + `}},"encoding":{` + strings.Join(fencs, ",") + `}}` +
		`}}}}}}`)
}

// manyEncodingsInput gives every form field of manyEncodings(n) a value its
// Encoding takes.
func manyEncodingsInput(n int) *openapi.Input {
	body := make(map[string]any, n)
	for i := range n {
		var v any
		switch i % 7 {
		case 2:
			v = "a/b c"
		case 3, 6:
			v = map[string]string{"k": "v"}
		default:
			v = []string{"a", "b c"}
		}
		body["f"+strconv.Itoa(i)] = v
	}
	return &openapi.Input{Body: body, MediaType: "application/x-www-form-urlencoded"}
}

// Regression check, not contract: compiling many properties and Encoding
// Objects is linear, at first use (Operations, which describes every Encoding),
// and at a first Prepare that gives every form field.
func TestEncodingCompileScale(t *testing.T) {
	wantLinear(t, "Operations()", 960, func(n int) func() { return timedOperations(t, manyEncodings(n)) })
	wantLinearBytes(t, "Operations() bytes", 960, func(n int) func() { return timedOperations(t, manyEncodings(n)) })
	wantLinear(t, "first Prepare", 960, func(n int) func() {
		doc, in := manyEncodings(n), manyEncodingsInput(n)
		clients := freshClients(t, doc, nil, scaleRuns+1)
		if _, err := clients[scaleRuns].Prepare("op", in); err != nil {
			t.Errorf("%.300v", err)
			return func() {}
		}
		i := 0
		return func() {
			clients[i%scaleRuns].Prepare("op", in)
			i++
		}
	})
}

// sharedRequestBody is a document whose one component Request Body, of n
// multipart properties each with an Encoding Object, is referenced by 16*n
// operations.
func sharedRequestBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{`)
	for i := range 16 * n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"/p%d":{"post":{"requestBody":{"$ref":"#/components/requestBodies/Upload"}}}`, i)
	}
	b.WriteString(`},"components":{"requestBodies":{"Upload":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"f%d":{"type":"string"}`, i)
	}
	b.WriteString(`}},"encoding":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"f%d":{"contentType":"text/plain, text/csv","headers":{"X-F%d":{"schema":{}}}}`, i, i)
	}
	b.WriteString(`}}}}}}}`)
	return []byte(b.String())
}

// Regression check, not contract: one Request Body with many Encodings,
// referenced by many operations, is compiled once, as every compiled or decoded
// form is computed at most once per document node: Operations() costs time,
// allocated bytes and retained memory linear in the document. Compiled per
// reference, n Encodings for 16*n operations would be 16 times the work at four
// times the input.
func TestSharedEncodingsScale(t *testing.T) {
	wantLinear(t, "Operations()", 64, func(n int) func() { return timedOperations(t, sharedRequestBody(n)) })
	wantLinearBytes(t, "Operations() bytes", 64, func(n int) func() { return timedOperations(t, sharedRequestBody(n)) })
	t.Run("retained after Operations()", func(t *testing.T) {
		keep := func(n int) int64 {
			c, err := openapi.Parse(context.Background(), sharedRequestBody(n), testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			return retainedBy(func() any {
				ops := c.Operations()
				if len(ops) == 0 || ops[0].Body == nil || len(ops[0].Body.Media) == 0 || len(ops[0].Body.Media[0].Encoding) != n {
					t.Errorf("the shared body's Encoding is not described")
				}
				return ops
			})
		}
		small, large := keep(64), keep(256)
		ratio := float64(large) / float64(max(small, 64<<10))
		t.Logf("64 x 1,024: %d bytes, 256 x 4,096: %d bytes (%.1fx)", small, large, ratio)
		if ratio > 8 {
			t.Errorf("four times the input retained %.1f times the memory; want linear", ratio)
		}
	})
}

// bodyScaleDoc has a form, a multipart and a sequential operation, and a
// form operation whose array field is exploded by RFC 6570.
const bodyScaleDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{
	"/form":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"a":{"type":"array","items":{"type":"string"}}}}}}}}},
	"/styled":{"post":{"operationId":"styled","requestBody":{"content":{"application/x-www-form-urlencoded":{"encoding":{"a":{"style":"form","explode":true}}}}}}},
	"/mp":{"post":{"operationId":"mp","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"a":{"type":"array","items":{"type":"string"}}}}}}}}},
	"/jsonl":{"post":{"operationId":"jsonl","requestBody":{"content":{"application/jsonl":{}}}}},
	"/sse":{"post":{"operationId":"sse","requestBody":{"content":{"text/event-stream":{}}}}}
}}`

// Regression check, not contract: a body of many fields or items costs time and
// allocated bytes linear in its encoded size, per call: a 100,000-field form
// (fields of a map, or items of an array, content-encoded or exploded by RFC
// 6570), a multipart body of as many parts, and a sequential body of 100,000
// items from a slice, prepared, and from an iterator, sent through an in-memory
// transport.
func TestBodySizeScale(t *testing.T) {
	c, err := openapi.Parse(context.Background(), []byte(bodyScaleDoc), testDocURI, &openapi.Options{HTTPClient: &http.Client{Transport: cannedRT{}}})
	if err != nil {
		t.Fatal(err)
	}
	fields := func(n int) map[string]string {
		m := make(map[string]string, n)
		for i := range n {
			m["k"+strconv.Itoa(i)] = "v " + strconv.Itoa(i)
		}
		return m
	}
	items := func(n int) []string {
		s := make([]string, n)
		for i := range s {
			s[i] = "it/" + strconv.Itoa(i)
		}
		return s
	}
	events := func(n int) []openapi.Event {
		evs := make([]openapi.Event, n)
		for i := range evs {
			evs[i] = openapi.Event{Data: []byte("line " + strconv.Itoa(i) + "\nmore"), ID: strconv.Itoa(i), IDSet: true}
		}
		return evs
	}
	prepare := func(key string, body any) func() {
		in := &openapi.Input{Body: body}
		if _, err := c.Prepare(key, in); err != nil {
			t.Errorf("%s: %.200v", key, err)
			return func() {}
		}
		return func() { c.Prepare(key, in) }
	}
	for _, tt := range []struct {
		name, key string
		body      func(n int) any
	}{
		{"form fields", "form", func(n int) any { return fields(n) }},
		{"form array items", "form", func(n int) any { return map[string]any{"a": items(n)} }},
		{"form exploded by RFC 6570", "styled", func(n int) any { return map[string]any{"a": items(n)} }},
		{"multipart fields", "mp", func(n int) any { return fields(n) }},
		{"multipart array items", "mp", func(n int) any { return map[string]any{"a": items(n)} }},
		{"JSON Lines slice", "jsonl", func(n int) any { return manyPets(n) }},
		{"event stream slice", "sse", func(n int) any { return events(n) }},
	} {
		wantLinear(t, tt.name, 25000, func(n int) func() { return prepare(tt.key, tt.body(n)) })
		wantLinearBytes(t, tt.name+", bytes", 25000, func(n int) func() { return prepare(tt.key, tt.body(n)) })
	}
	call := func(n int) func() {
		ps := manyPets(n)
		in := func() *openapi.Input { return &openapi.Input{Body: iter.Seq[Pet](seqOf(ps...))} }
		if _, err := c.Call(context.Background(), "jsonl", in(), nil); err != nil {
			t.Errorf("Call: %.200v", err)
			return func() {}
		}
		return func() { c.Call(context.Background(), "jsonl", in(), nil) }
	}
	wantLinear(t, "JSON Lines iterator, Call", 25000, call)
	wantLinearBytes(t, "JSON Lines iterator, Call, bytes", 25000, call)
}

// depthDocPaths has a form field, a multipart part and a sequential item
// that are each encoded as JSON.
const depthBodyDoc = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{
	"/form":{"post":{"operationId":"form","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"o":{"type":"object"}}}}}}}},
	"/mp":{"post":{"operationId":"mp","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"o":{"type":"object"}}}}}}}},
	"/jsonl":{"post":{"operationId":"jsonl","requestBody":{"content":{"application/jsonl":{}}}}}
}}`

// Regression check, not contract: refusing costs time and bytes linear in the
// depth, and so does accepting. The rest is contract: doc.go, Values: a value
// "whose JSON, a MarshalJSON's output included, nests deeper than 1,000 levels.
// Levels count within each body, field, part, sequential item or parameter,
// its outermost value being level 1" is refused "at the key of the body,
// field, part, sequential item or parameter that is or holds it". A scalar leaf
// counts as a level: 999 objects around a leaf in a field, a part or an item
// are sent, 1,000 are refused at its key.
func TestBodyDepthLimit(t *testing.T) {
	c, err := openapi.Parse(context.Background(), []byte(depthBodyDoc), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	bodies := []struct {
		key  string
		body func(v any) any
		at   string
	}{
		{"form", func(v any) any { return map[string]any{"o": v} }, "Input.Body/o"},
		{"mp", func(v any) any { return map[string]any{"o": v} }, "Input.Body/o"},
		{"jsonl", func(v any) any { return []any{v} }, "Input.Body/0"},
	}
	for _, b := range bodies {
		// nestMap(n, leaf) is n objects around a leaf: n+1 levels.
		if _, err := c.Prepare(b.key, &openapi.Input{Body: b.body(nestMap(999, "x"))}); err != nil {
			t.Errorf("%s: 1,000 levels refused: %.200v", b.key, err)
		}
		_, err := c.Prepare(b.key, &openapi.Input{Body: b.body(nestMap(1000, "x"))})
		var re *openapi.RequestError
		if !errors.As(err, &re) {
			t.Errorf("%s: 1,001 levels = %v, want a refusal", b.key, err)
			continue
		}
		wantKeys(t, b.key+" Inputs", re.Inputs, true, b.at)
	}
	for _, b := range bodies {
		refuse := func(n int) func() {
			in := &openapi.Input{Body: b.body(nestMap(n, "x"))}
			return func() {
				if _, err := c.Prepare(b.key, in); err == nil {
					t.Errorf("%s: %d levels sent", b.key, n)
				}
			}
		}
		wantLinear(t, b.key+": refusing depth", 2500, refuse)
		wantLinearBytes(t, b.key+": refusing depth, bytes", 2500, refuse)
		accept := func(n int) func() {
			in := &openapi.Input{Body: b.body(nestMap(n, "x"))}
			return func() { c.Prepare(b.key, in) }
		}
		wantLinear(t, b.key+": accepting depth", 240, accept)
		wantLinearBytes(t, b.key+": accepting depth, bytes", 240, accept)
	}
}
