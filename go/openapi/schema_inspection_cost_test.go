package openapi_test

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for the cost of schema inspection, which costs what the
// document holds and depends on nothing else. A schema node's shape (the types
// it and the schemas its $ref and allOf reach allow, its contentEncoding, its
// items, its properties) is computed once per node and reused by every field,
// operation and nested part that reaches it; a field combines its roots'
// shapes. First-use work for a document with no $ref or allOf cycle is linear
// in its size however many fields share a target. Keywords are read by keyed
// lookup, never by scanning every member (reference() included). Items chains
// are followed iteratively with each node's set memoized and no length bound:
// a long chain gets its true default, and only an items cycle contributes the
// absent type. A length bound would make a stored result depend on where the
// first walk entered. Concurrent first uses store with LoadOrStore, and the
// first stored result wins. A call never computes a closure: a nested part's
// or form-typed field's encoding is looked up before any shape is computed.
// Each scaling test is checked on time and bytes allocated
// (scaling_test.go), with the inputs growing together so that work per
// field proportional to what the field reaches is quadratic.

const inspectHead = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{`

// emptyMembers returns k empty schemas, comma separated.
func emptyMembers(k int) string {
	return strings.TrimSuffix(strings.Repeat("{},", k), ",")
}

// fanoutDoc is a fan-out document: one operation whose form and multipart
// bodies have n properties, each a $ref to Big, an object with k empty allOf
// members.
func fanoutDoc(n, k int) []byte {
	var props []string
	for i := range n {
		props = append(props, fmt.Sprintf(`"p%d":{"$ref":"#/components/schemas/Big"}`, i))
	}
	schema := `{"type":"object","properties":{` + strings.Join(props, ",") + `}}`
	return []byte(inspectHead + `"/up":{"post":{"operationId":"up","requestBody":{"content":{` +
		`"multipart/form-data":{"schema":` + schema + `},"application/x-www-form-urlencoded":{"schema":` + schema + `}}}}}},` +
		`"components":{"schemas":{"Big":{"type":"object","allOf":[` + emptyMembers(k) + `]}}}}`)
}

// sharedAllOfDoc is a document of n operations whose form body's schema is
// a $ref to Big, an object of one property with k empty allOf members.
func sharedAllOfDoc(n, k int) []byte {
	var b strings.Builder
	b.WriteString(inspectHead)
	for i := range n {
		fmt.Fprintf(&b, `"/p%d":{"post":{"operationId":"op%d","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"$ref":"#/components/schemas/Big"}}}}}},`, i, i)
	}
	b.WriteString(`"/z":{"get":{}}},"components":{"schemas":{"Big":{"type":"object","properties":{"a":{"type":"string"}},"allOf":[` + emptyMembers(k) + `]}}}}`)
	return []byte(b.String())
}

// sharedTargetDoc is a document whose one multipart and form body has n
// properties, each a $ref to T, a string schema with k extension members,
// or, with types set, whose type is an array of k entries.
func sharedTargetDoc(n, k int, types bool) []byte {
	var props []string
	for i := range n {
		props = append(props, fmt.Sprintf(`"p%d":{"$ref":"#/components/schemas/T"}`, i))
	}
	schema := `{"type":"object","properties":{` + strings.Join(props, ",") + `}}`
	var target string
	if types {
		target = `{"type":[` + strings.TrimSuffix(strings.Repeat(`"string",`, k), ",") + `]}`
	} else {
		var keys []string
		for i := range k {
			keys = append(keys, fmt.Sprintf(`"x-k%d":%d`, i, i))
		}
		target = `{"type":"string",` + strings.Join(keys, ",") + `}`
	}
	return []byte(inspectHead + `"/up":{"post":{"operationId":"up","requestBody":{"content":{` +
		`"multipart/form-data":{"schema":` + schema + `},"application/x-www-form-urlencoded":{"schema":` + schema + `}}}}}},` +
		`"components":{"schemas":{"T":` + target + `}}}`)
}

// First use is linear in the document however many fields share a target, a
// shape being computed once per node and reused by every field, operation and
// nested part that reaches it. Each case grows the fields and what they share
// together, from 250 to 1,000: n form and multipart properties each a $ref to
// one schema with n allOf members, and n operations whose body schema is a
// $ref to one schema with n allOf members. An earlier implementation walked
// the n members for each property or operation: sixteen times the work.
func TestSharedSchemaTargetsFirstUseLinear(t *testing.T) {
	wantLinear(t, "properties sharing a $ref to allOf members", 250, func(n int) func() { return timedOperations(t, fanoutDoc(n, n)) })
	wantLinear(t, "operations sharing a $ref to allOf members", 250, func(n int) func() { return timedOperations(t, sharedAllOfDoc(n, n)) })
}

// Keywords are read by keyed lookup, never by scanning every member, and a
// shape is computed once per node, so the cost of each of 2,000 properties
// sharing a target does not depend on the target's size: a target with 250 or
// 4,000 extension members, or a type array of 250 or 4,000 entries, costs the
// same per property (wantFlat: at most 3 times at 16 times the size).
// Previously every property scanned every member and every type entry.
func TestSharedSchemaTargetCostIndependentOfSize(t *testing.T) {
	wantFlat(t, "a target with many keys", 250, func(k int) func() { return timedOperations(t, sharedTargetDoc(2000, k, false)) })
	wantFlat(t, "a target with a long type array", 250, func(k int) func() { return timedOperations(t, sharedTargetDoc(2000, k, true)) })
}

// The shared targets are described as their schemas say (OpenAPI 3.1.2
// section 4.8.15.1.1: application/json for an object, text/plain for a
// string), so the scaling cases measure real work.
func TestSharedSchemaTargetsDescribed(t *testing.T) {
	for _, tt := range []struct {
		name string
		doc  []byte
		key  string
		want string
	}{
		{"allOf fanout", fanoutDoc(3, 3), "up", "application/json"},
		{"extension keys", sharedTargetDoc(3, 3, false), "up", "text/plain"},
		{"type array", sharedTargetDoc(3, 3, true), "up", "text/plain"},
		{"shared allOf", sharedAllOfDoc(3, 3), "op2", "text/plain"},
	} {
		c, err := openapi.Parse(context.Background(), tt.doc, testDocURI, nil)
		if err != nil {
			t.Fatal(err)
		}
		op := mustOp(t, c, tt.key)
		for _, m := range op.Body.Media {
			if len(m.Encoding) == 0 {
				t.Errorf("%s %s: no fields", tt.name, m.Type)
			}
			for _, e := range m.Encoding {
				if e.ContentType != tt.want || e.Err != nil {
					t.Errorf("%s %s %s: ContentType %q, Err %v; want %s", tt.name, m.Type, e.Name, e.ContentType, e.Err, tt.want)
				}
			}
		}
	}
}

// nestedAllOfDoc is a multipart field bundle, a $ref to Arr, an array of Obj
// with k empty allOf members, whose Encoding contentType is ctype, a nested
// multipart type or the form type.
func nestedAllOfDoc(k int, ctype string) []byte {
	return []byte(inspectHead + `"/up":{"post":{"operationId":"up","requestBody":{"content":{"multipart/form-data":{` +
		`"schema":{"type":"object","properties":{"bundle":{"$ref":"#/components/schemas/Arr"}}},` +
		`"encoding":{"bundle":{"contentType":"` + ctype + `"}}}}}}}},"components":{"schemas":{` +
		`"Obj":{"type":"object","properties":{"a":{"type":"string"}}},` +
		`"Arr":{"type":"array","items":{"$ref":"#/components/schemas/Obj"},"allOf":[` + emptyMembers(k) + `]}}}}`)
}

// A call never computes a closure: a nested part's or form-typed field's
// encoding is looked up before any shape is computed. A warm call whose field,
// a nested multipart part or a form-typed part, holds n items, its schema
// reaching n allOf members, costs linear in n (previously each item computed
// the field's closure over the n members on every call).
func TestNestedPartWarmPrepareLinear(t *testing.T) {
	for _, ctype := range []string{"multipart/mixed", "application/x-www-form-urlencoded"} {
		wantLinear(t, ctype+" items, warm Prepare", 250, func(n int) func() {
			c, err := openapi.Parse(context.Background(), nestedAllOfDoc(n, ctype), testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			items := make([]any, n)
			for i := range items {
				items[i] = map[string]any{"a": fmt.Sprint(i)}
			}
			in := &openapi.Input{Body: map[string]any{"bundle": items}}
			if _, err := c.Prepare("up", in); err != nil { // the first use, untimed
				t.Errorf("Prepare: %.200v", err)
				return func() {}
			}
			return func() { c.Prepare("up", in) }
		})
	}
}

// longChainDoc is a long items chain: schemas S0 to S(n-1), each an array
// whose items is the next, Sn a string, and two operations whose multipart
// field enters the chain at different depths: a's f at S0, b's g at S(mid).
func longChainDoc(n, mid int) []byte {
	var b strings.Builder
	b.WriteString(inspectHead)
	fmt.Fprintf(&b, `"/a":{"post":{"operationId":"a","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"f":{"$ref":"#/components/schemas/S0"}}}}}}}},`)
	fmt.Fprintf(&b, `"/b":{"post":{"operationId":"b","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"g":{"$ref":"#/components/schemas/S%d"}}}}}}}}`, mid)
	b.WriteString(`},"components":{"schemas":{`)
	for i := range n {
		fmt.Fprintf(&b, `"S%d":{"type":"array","items":{"$ref":"#/components/schemas/S%d"}},`, i, i+1)
	}
	fmt.Fprintf(&b, `"S%d":{"type":"string"}}}}`, n)
	return []byte(b.String())
}

// Items chains are followed iteratively with each node's set memoized and no
// length bound, so a long chain gets its true default, and results do not
// depend on which operation is used first. A 1,600-link chain ending in a
// string is entered by a at its head and by b 700 links in: whichever
// operation is described and called first, both fields are text/plain
// (OpenAPI 3.1.2 section 4.8.15.1.1: an array's default is its items'; a
// string's is text/plain) and the same parts are sent. Previously, calling a
// first stored application/octet-stream, the 1,000-link cut, for every link on
// its path, while calling b first gave text/plain.
func TestLongItemsChainOrderIndependent(t *testing.T) {
	doc := longChainDoc(1600, 700)
	type outcome struct{ f, g, sentA, sentB string }
	use := func(order ...string) outcome {
		c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
		if err != nil {
			t.Fatal(err)
		}
		var o outcome
		for _, key := range order {
			name := map[string]string{"a": "f", "b": "g"}[key]
			e := encodingByName(t, reqMedia(t, mustOp(t, c, key), 0))[name]
			req, err := c.Prepare(key, &openapi.Input{Body: map[string]any{name: "x"}, MediaType: "multipart/form-data; boundary=B"})
			sent := fmt.Sprint(err)
			if err == nil {
				sent = string(preparedBody(t, req))
			}
			if key == "a" {
				o.f, o.sentA = e.ContentType, sent
			} else {
				o.g, o.sentB = e.ContentType, sent
			}
		}
		return o
	}
	ab, ba := use("a", "b"), use("b", "a")
	if ab != ba {
		t.Errorf("the result depends on the order of first use:\na, b: %#v\nb, a: %#v", ab, ba)
	}
	part := func(name string) string {
		return "--B\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\nContent-Type: text/plain\r\n\r\nx\r\n--B--\r\n"
	}
	want := outcome{"text/plain", "text/plain", part("f"), part("g")}
	for _, got := range []outcome{ab, ba} {
		if got != want {
			t.Errorf("got %#v\nwant %#v", got, want)
		}
	}
}

// No 1,000-link cut applies: a single chain of 1,500 links ending in a string
// is text/plain, under multipart/form-data and as a form field's own type.
func TestLongItemsChainTrueDefault(t *testing.T) {
	c, err := openapi.Parse(context.Background(), chainDoc(1500), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e := encodingByName(t, reqMedia(t, mustOp(t, c, "up"), 0))["f"]; e == nil || e.ContentType != "text/plain" {
		t.Errorf("f = %+v, want ContentType text/plain", e)
	}
}

// inspectRaceDoc has operations o0 to o(ops-1) whose multipart bodies share
// schemas through $ref and allOf, with items cycles, a nested multipart field
// n and a form-typed field fm, and the operations a and b of a 1,200-link
// items chain entered at its head and 500 links in.
func inspectRaceDoc(ops int) []byte {
	var b strings.Builder
	b.WriteString(inspectHead)
	for i := range ops {
		ref := []string{"A", "B", "C", "Cyc"}[i%4]
		fmt.Fprintf(&b, `"/o%d":{"post":{"operationId":"o%d","requestBody":{"content":{"multipart/form-data":{"schema":{"allOf":[{"$ref":"#/components/schemas/%s"}],"properties":{"own%d":{"type":"integer"}}},"encoding":{"n":{"contentType":"multipart/mixed"},"fm":{"contentType":"application/x-www-form-urlencoded"}}}}}}},`, i, i, ref, i)
	}
	b.WriteString(`"/a":{"post":{"operationId":"a","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"f":{"$ref":"#/components/schemas/L0"}}}}}}}},`)
	b.WriteString(`"/b":{"post":{"operationId":"b","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"g":{"$ref":"#/components/schemas/L500"}}}}}}}}`)
	b.WriteString(`},"components":{"schemas":{
	"A":{"type":"object","properties":{"n":{"$ref":"#/components/schemas/B"},"arr":{"type":"array","items":{"$ref":"#/components/schemas/Cyc"}}},"allOf":[{"$ref":"#/components/schemas/B"}]},
	"B":{"$ref":"#/components/schemas/C","properties":{"fm":{"type":"object","properties":{"x":{"type":"string"}}},"b":{"type":["string","null"]}}},
	"C":{"type":"object","allOf":[{"$ref":"#/components/schemas/A"}],"properties":{"c":{"type":"array","items":{"type":"array","items":{"$ref":"#/components/schemas/Cyc"}}}}},
	"Cyc":{"type":"array","items":{"$ref":"#/components/schemas/Cyc2"}},
	"Cyc2":{"type":"array","items":{"$ref":"#/components/schemas/Cyc"}},`)
	for i := range 1200 {
		fmt.Fprintf(&b, `"L%d":{"type":"array","items":{"$ref":"#/components/schemas/L%d"}},`, i, i+1)
	}
	b.WriteString(`"L1200":{"type":"string"}}}}`)
	return []byte(b.String())
}

// raceInput is the body the operation key of inspectRaceDoc is prepared
// with, its boundary given so that the bytes are the same every time.
func raceInput(key string) *openapi.Input {
	body := map[string]any{
		"n":  map[string]any{"b": "x", "fm": map[string]any{"x": "y"}},
		"fm": map[string]any{"x": "1"},
		"b":  "s", "c": []any{"z"},
	}
	switch key {
	case "a":
		body = map[string]any{"f": "x"}
	case "b":
		body = map[string]any{"g": "x"}
	}
	return &openapi.Input{MediaType: "multipart/form-data; boundary=B", Body: body}
}

// describeRace returns, for every operation of c in keys' order, each
// field's descriptor and the bytes of raceInput prepared, or its refusal.
func describeRace(t *testing.T, c *openapi.Client, keys []string) string {
	t.Helper()
	var b strings.Builder
	for _, k := range keys {
		op, err := c.Operation(k)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", k, err)
			continue
		}
		for _, e := range op.Body.Media[0].Encoding {
			fmt.Fprintf(&b, "%s.%s=%s,%v;", k, e.Name, e.ContentType, e.Err)
		}
		req, err := c.Prepare(k, raceInput(k))
		if err != nil {
			fmt.Fprintf(&b, " refused: %v\n", err)
			continue
		}
		fmt.Fprintf(&b, " sent %q\n", generatedBoundaries(t, preparedBody(t, req), "B"))
	}
	return b.String()
}

var boundaryParam = regexp.MustCompile(`boundary="?([^";\r\n]+)`)

// generatedBoundaries returns body with every boundary the client generated
// (each one not among given) written as <generated>, as a generated boundary
// is random (body.go, newBoundary) and content is not scanned for it. Each
// must still be one RFC 2046 section 5.1.1 allows.
func generatedBoundaries(t *testing.T, body []byte, given ...string) []byte {
	t.Helper()
	for _, m := range boundaryParam.FindAllSubmatch(body, -1) {
		b := string(m[1])
		if slices.Contains(given, b) {
			continue
		}
		if !validBoundary(b) {
			t.Errorf("generated boundary %q is not one RFC 2046 section 5.1.1 allows", b)
		}
		body = bytes.ReplaceAll(body, []byte(b), []byte("<generated>"))
	}
	return body
}

// Concurrent first uses in random order give what a serial first use gives,
// and a serial first use gives the same whichever order it takes (results do
// not depend on which operation compiles first), for descriptors and the bytes
// sent, every boundary the client generated for a nested part normalized; run
// under -race. Previously the serial orders differed on the long chain, a
// first using a's 1,200-link walk and storing application/octet-stream.
func TestSchemaInspectionConcurrentFirstUseMatchesSerial(t *testing.T) {
	const ops = 40
	doc := inspectRaceDoc(ops)
	keys := []string{"a", "b"}
	for i := range ops {
		keys = append(keys, fmt.Sprintf("o%d", i))
	}
	fresh := func() *openapi.Client {
		c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	want := describeRace(t, fresh(), keys)
	if !strings.Contains(want, "<generated>") {
		t.Fatalf("no nested part was sent with a generated boundary:\n%s", want)
	}
	reversed := slices.Clone(keys)
	slices.Reverse(reversed)
	c := fresh()
	for _, k := range reversed {
		mustOp(t, c, k)
	}
	if got := describeRace(t, c, keys); got != want {
		t.Errorf("first use in reverse order differs from first use in order:\n%s", lineDiff(got, want))
	}
	rng := rand.New(rand.NewPCG(4, 8))
	for round := range 8 {
		c := fresh()
		var wg sync.WaitGroup
		for range 8 {
			order := slices.Clone(keys)
			rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
			wg.Go(func() {
				for _, k := range order {
					c.Prepare(k, raceInput(k))
				}
			})
		}
		wg.Wait()
		if got := describeRace(t, c, keys); got != want {
			t.Errorf("round %d: concurrent first use differs from serial:\n%s", round, lineDiff(got, want))
			break
		}
	}
}
