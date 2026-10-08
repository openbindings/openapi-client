package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests for memory and scaling: the Content-Length reservation,
// Load's Options checks, the compact document tree, and reference
// resolution, which ctx bounds as part of the whole load.

// allocated reports the bytes allocated while f runs.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A peer's Content-Length does not decide an up-front allocation: doc.go,
// Outcomes: "Even with no bound, the length a response body or retrieved
// document declares reserves at most 1 MiB in advance; the rest is allocated
// as its bytes arrive." client.go, Options.MaxBodyBytes and MaxErrorBytes and
// load.go, Loader.MaxBytes bound what is read, and a negative bound, "no
// limit", never overflows into a panic. Each server here declares a length
// and sends 2 bytes; the 16 MiB budget is a tolerance around the 1 MiB
// reservation.
func TestContentLengthDoesNotDecideAllocation(t *testing.T) {
	const budget = 16 << 20 // the 1 MiB reservation, with room for everything else
	doc := doc31(`"/x":{"get":{"operationId":"get","responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}`)
	head := func(status string, length int64) string {
		return fmt.Sprintf("HTTP/1.1 %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n{}", status, length)
	}
	const huge = 9223372036854775806
	tests := []struct {
		name   string
		status string
		length int64
		opts   openapi.Options
		out    func() any
	}{
		{"typed out, default bound", "200 OK", 64 << 20, openapi.Options{}, func() any { return new(any) }},
		{"*[]byte, default bound", "200 OK", 64 << 20, openapi.Options{}, func() any { return new([]byte) }},
		{"typed out, no limit", "200 OK", 1 << 30, openapi.Options{MaxBodyBytes: -1}, func() any { return new(any) }},
		{"*[]byte, no limit", "200 OK", 1 << 30, openapi.Options{MaxBodyBytes: -1}, func() any { return new([]byte) }},
		{"status error, no limit", "500 Oops", 1 << 30, openapi.Options{MaxErrorBytes: -1}, func() any { return nil }},
		{"typed out, no limit, huge length", "200 OK", huge, openapi.Options{MaxBodyBytes: -1}, func() any { return new(any) }},
		{"*[]byte, no limit, huge length", "200 OK", huge, openapi.Options{MaxBodyBytes: -1}, func() any { return new([]byte) }},
		{"status error, no limit, huge length", "500 Oops", huge, openapi.Options{MaxErrorBytes: -1}, func() any { return nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := rawHTTP(t, head(tt.status, tt.length), 50*time.Millisecond)
			opts := tt.opts
			c := parseAt(t, doc, base, base+"/openapi.json", &opts)
			var err error
			n := allocated(func() {
				noPanic(t, "Call", func() { _, err = c.Call(t.Context(), "get", nil, tt.out()) })
			})
			if err == nil {
				t.Errorf("Call succeeded on a truncated body")
			}
			if n > budget {
				t.Errorf("allocated %d MiB for a 2-byte body declared as %d bytes", n>>20, tt.length)
			}
		})
	}

	for _, tt := range []struct {
		name   string
		max    int64
		length int64
	}{
		{"default bound", 0, 64 << 20},
		{"no limit", -1, 1 << 30},
		{"no limit, huge length", -1, huge},
	} {
		t.Run("Loader "+tt.name, func(t *testing.T) {
			base := rawHTTP(t, head("200 OK", tt.length), 50*time.Millisecond)
			var err error
			n := allocated(func() {
				noPanic(t, "Load", func() { _, err = (&openapi.Loader{MaxBytes: tt.max}).Load(t.Context(), base+"/openapi.json", nil) })
			})
			if err == nil {
				t.Errorf("Load succeeded on a truncated document")
			}
			if n > budget {
				t.Errorf("allocated %d MiB for a 2-byte document declared as %d bytes", n>>20, tt.length)
			}
		})
	}
}

// countedBody yields size bytes, at most len(p) per Read, and counts them.
type countedBody struct {
	left, read int64
}

func (b *countedBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), b.left))
	clear(p[:n])
	b.left -= int64(n)
	b.read += int64(n)
	return n, nil
}

func (b *countedBody) Close() error { return nil }

// Each read is capped at the remaining allowance plus one byte, so a *[]byte
// with a large capacity cannot take in more than MaxBodyBytes+1 bytes
// (client.go, Call: "A *[]byte receives the raw bytes ... bounded by
// MaxBodyBytes"; "nil discards it, reading at most MaxBodyBytes").
func TestBodyReadsCappedAtBound(t *testing.T) {
	var body *countedBody
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body = &countedBody{left: 10 << 20}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/octet-stream"}},
			Body: body, ContentLength: -1, Request: r}, nil
	})
	doc := doc31(`"/x":{"get":{"operationId":"get"}}`)
	c := parseAt(t, doc, "https://h.example.test", "https://h.example.test/openapi.json",
		&openapi.Options{HTTPClient: &http.Client{Transport: rt}, MaxBodyBytes: 10})

	big := make([]byte, 0, 64<<20)
	_, err := c.Call(t.Context(), "get", nil, &big)
	var tooBig *http.MaxBytesError
	if !errors.As(err, &tooBig) {
		t.Errorf("error %v, want an *http.MaxBytesError", err)
	}
	if body.read > 11 {
		t.Errorf("read %d bytes into the *[]byte with MaxBodyBytes 10, want at most 11", body.read)
	}
	if _, err := c.Call(t.Context(), "get", nil, nil); err != nil {
		t.Errorf("nil out: %v", err)
	}
	if body.read > 10 {
		t.Errorf("discarded %d bytes with MaxBodyBytes 10, want at most 10", body.read)
	}
}

// Regression check, not contract: Load's Options checks do not compile every
// operation's plan, so setting Options.Server or Options.MediaType costs about
// what a plain Load costs. Checks that compiled every plan would make 8.7
// times the allocations.
func TestLoadOptionChecksStayLazy(t *testing.T) {
	doc, _ := largeDoc(700, 500)
	count := func(opts *openapi.Options) float64 {
		return testing.AllocsPerRun(2, func() {
			if _, err := openapi.Parse(context.Background(), doc, largeDocURI, opts); err != nil {
				t.Fatal(err)
			}
		})
	}
	plain := count(nil)
	for name, opts := range map[string]*openapi.Options{
		"Server":               {Server: "https://api.example.test/v1"},
		"MediaType":            {MediaType: "application/json"},
		"Server and MediaType": {Server: "https://api.example.test/v1", MediaType: "application/json"},
	} {
		if got := count(opts); got > 1.5*plain {
			t.Errorf("Load with %s: %.0f allocations, plain Load %.0f; want at most 1.5 times", name, got, plain)
		}
	}
}

// Regression check, not contract: the compact document tree's budgets: retained
// heap at most 3 times the document's bytes, and allocation during the load at
// most 6 times (measured as the total allocated, which bounds the peak).
func TestDocumentTreeMemoryBudget(t *testing.T) {
	doc, _ := largeDoc(700, 500)
	var c *openapi.Client
	var before, loaded runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var err error
	c, err = openapi.Parse(context.Background(), doc, largeDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&loaded)
	total := loaded.TotalAlloc - before.TotalAlloc
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	runtime.KeepAlive(c)
	size := float64(len(doc))
	t.Logf("document %d bytes: retained %.2fx, allocated %.2fx", len(doc), float64(retained)/size, float64(total)/size)
	if float64(retained) > 3*size {
		t.Errorf("retained %d bytes, %.1f times the document; budget 3", retained, float64(retained)/size)
	}
	if float64(total) > 6*size {
		t.Errorf("allocated %d bytes loading, %.1f times the document; budget 6", total, float64(total)/size)
	}
}

// Regression check, not contract: reference resolution is linear, at Load and
// at first use. Four times the references must cost well under the sixteen
// times a quadratic resolution costs, by the shared harness (scaling_test.go).
func TestReferenceResolutionScales(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	pathItemChain := func(n int) []byte {
		var b strings.Builder
		b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/x":{"$ref":"#/components/pathItems/p0"}},"components":{"pathItems":{`)
		for i := range n {
			fmt.Fprintf(&b, `"p%d":{"$ref":"#/components/pathItems/p%d"},`, i, i+1)
		}
		fmt.Fprintf(&b, `"p%d":{"get":{"operationId":"get"}}}}}`, n)
		return []byte(b.String())
	}
	manyTargets := func(n int) []byte {
		var b strings.Builder
		b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{`)
		for i := range n {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `"/p%d":{"$ref":"#/components/pathItems/c%d"}`, i, i)
		}
		b.WriteString(`},"components":{"pathItems":{`)
		for i := range n {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `"c%d":{"get":{"operationId":"op%d"}}`, i, i)
		}
		b.WriteString(`}}}`)
		return []byte(b.String())
	}
	paramChain := func(n int) []byte {
		var b strings.Builder
		b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://h.example.test"}],
			"paths":{"/x":{"get":{"operationId":"get","parameters":[{"$ref":"#/components/parameters/p0"}]}}},"components":{"parameters":{`)
		for i := range n {
			fmt.Fprintf(&b, `"p%d":{"$ref":"#/components/parameters/p%d"},`, i, i+1)
		}
		fmt.Fprintf(&b, `"p%d":{"name":"q","in":"query"}}}}`, n)
		return []byte(b.String())
	}
	load := func(doc []byte) func() {
		return func() {
			c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Operations()) == 0 {
				t.Fatal("no operations")
			}
		}
	}
	firstUse := func(doc []byte) func() {
		return func() {
			c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			if op, err := c.Operation("get"); err != nil || op.Err != nil || len(op.Params) != 1 {
				t.Fatalf("Operation(get) = %v, %v", op, err)
			}
		}
	}
	for _, tt := range []struct {
		name  string
		run   func(int) func()
		small int
	}{
		// Sizes where a quadratic resolution took 18x or more.
		{"Path Item chain at Load", func(n int) func() { return load(pathItemChain(n)) }, 16000},
		{"many Path Item targets at Load", func(n int) func() { return load(manyTargets(n)) }, 16000},
		{"parameter chain at first use", func(n int) func() { return firstUse(paramChain(n)) }, 2000},
	} {
		wantLinear(t, tt.name, tt.small, tt.run)
	}

	// A long chain that ends in a cycle is still detected.
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/x":{"$ref":"#/components/pathItems/p0"}},"components":{"pathItems":{`)
	for i := range 5000 {
		fmt.Fprintf(&b, `"p%d":{"$ref":"#/components/pathItems/p%d"},`, i, i+1)
	}
	b.WriteString(`"p5000":{"$ref":"#/components/pathItems/p0"}}}}`)
	c, err := openapi.Parse(context.Background(), []byte(b.String()), testDocURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ops := c.Operations(); len(ops) != 1 || ops[0].Err == nil || !errors.Is(ops[0].Err, openapi.ErrUnresolved) {
		t.Errorf("a chain ending in a cycle: Operations = %+v, want one entry whose Err wraps ErrUnresolved", ops)
	}
}

// "ctx bounds the whole load, reading and parsing included" (load.go, Load):
// a done context ends Parse with an error matching it.
func TestLoadHonorsContext(t *testing.T) {
	doc, _ := largeDoc(700, 500)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errCause)
	c, err := openapi.Parse(ctx, doc, largeDocURI, nil)
	if err == nil || c != nil {
		t.Fatalf("Parse with a cancelled context = %v, %v; want an error", c, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not match context.Canceled", err)
	}

	dctx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer dcancel()
	if _, err := (&openapi.Loader{}).Parse(dctx, doc, largeDocURI, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Loader.Parse with an expired deadline = %v, want context.DeadlineExceeded", err)
	}
}

// countValues counts the JSON values in valid JSON b: objects, arrays and
// scalars, member names excluded.
func countValues(t *testing.T, b []byte) int {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct{ obj, wantKey bool }
	var stack []*frame
	n := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return n
		}
		if err != nil {
			t.Fatal(err)
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '}' || d == ']' {
				stack = stack[:len(stack)-1]
				if k := len(stack); k > 0 && stack[k-1].obj {
					stack[k-1].wantKey = true
				}
				continue
			}
			n++
			if k := len(stack); k > 0 && stack[k-1].obj {
				stack[k-1].wantKey = false // the container is the member's value
			}
			stack = append(stack, &frame{obj: d == '{', wantKey: true})
			continue
		}
		if k := len(stack); k > 0 && stack[k-1].obj && stack[k-1].wantKey {
			stack[k-1].wantKey = false // a member name
			continue
		}
		n++
		if k := len(stack); k > 0 && stack[k-1].obj {
			stack[k-1].wantKey = true
		}
	}
}

// Regression check, not contract: any document stays within its bytes plus 16
// bytes per JSON value retained, and the node estimate does not count
// structural bytes inside strings: an array of zeros with one value per two
// bytes; one string of commas, and one of braces, which hold a handful of
// values; and an array of empty objects. Each document is about 1 MiB. The
// budget has a fixed allowance of 64 KiB, as
// TestRejectedDocumentAllocationBudget has: Go's allocator rounds a large
// allocation up to whole 8 KiB pages, so the copy of a document of few values
// alone exceeds bytes plus 16 per value. Retained memory may also add the
// decoded bytes of escaped strings, plus up to 64 bytes per such string; the
// cases with escaped member names (including an object that is indexed for a
// $ref, and escaped Paths keys) check it. (The 3x and 6x budgets stay on the
// synthetic document, TestDocumentTreeMemoryBudget.)
func TestWorstCaseRetainedMemoryBudget(t *testing.T) {
	const size = 1 << 20
	const head = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{},"components":{"x":`
	fill := func(open, item, sep, close string) []byte {
		var buf strings.Builder
		buf.WriteString(head + open + item)
		for buf.Len() < size {
			buf.WriteString(sep + item)
		}
		buf.WriteString(close + "}}")
		return []byte(buf.String())
	}
	docs := map[string][]byte{
		"array of zeros":         fill("[", "0", ",", "]"),
		"string of commas":       []byte(head + `"` + strings.Repeat(",", size) + `"}}`),
		"string of braces":       []byte(head + `"` + strings.Repeat("{", size) + `"}}`),
		"string of brackets":     []byte(head + `"` + strings.Repeat("[", size) + `"}}`),
		"array of empty objects": fill("[", "{}", ",", "]"),
		"escaped member names":   escapedMembers(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{},"components":{"x":{`, `"\u0061%x":0`, `"end":0}}}`, size),
		"indexed object with escaped names": escapedMembers(
			`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{"/a":{"$ref":"#/components/pathItems/p"}},"components":{"pathItems":{`,
			`"\u0061%x":0`, `"p":{"get":{}}}}}`, size),
		"escaped Paths keys": escapedMembers(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{`, `"\/%x":0`, `"/end":{"get":{}}}}`, size),
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			values := countValues(t, doc)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			c, err := openapi.Parse(context.Background(), doc, largeDocURI, nil)
			if err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(c)
			retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			escaped, decoded := escapedStrings(t, doc)
			budget := int64(len(doc)) + 16*int64(values) + 64<<10 + decoded + 64*int64(escaped)
			t.Logf("document %d bytes, %d values, %d escaped strings (%d bytes decoded): retained %d bytes, budget %d",
				len(doc), values, escaped, decoded, retained, budget)
			if retained > budget {
				t.Errorf("retained %d bytes, over the budget of %d (document bytes, 16 per value, 64 KiB, and the decoded bytes of escaped strings plus 64 per string)", retained, budget)
			}
		})
	}
}

// Regression check, not contract: a document rejected at its second byte pays
// no reservation beyond what a document of its length could need: its
// allocation stays within the worst-case budget for its length, the document's
// bytes plus 16 bytes for each value its bytes could hold (one per two bytes).
func TestRejectedDocumentAllocationBudget(t *testing.T) {
	doc := []byte("{" + strings.Repeat(",", 4<<20))
	var err error
	n := allocated(func() { _, err = openapi.Parse(context.Background(), doc, testDocURI, nil) })
	if err == nil {
		t.Fatal("the document was accepted")
	}
	budget := uint64(len(doc)) + 16*uint64(len(doc)/2+1) + 1<<20
	t.Logf("rejected %d bytes: allocated %d, budget %d", len(doc), n, budget)
	if n > budget {
		t.Errorf("allocated %d MiB rejecting a %d MiB document; budget %d MiB", n>>20, len(doc)>>20, budget>>20)
	}
}

// escapedMembers returns head, then members written by format with
// successive integers, separated by commas, until the document reaches
// size, then tail (which begins with the last member).
func escapedMembers(head, format, tail string, size int) []byte {
	var b strings.Builder
	b.WriteString(head)
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, format+",", i)
	}
	b.WriteString(tail)
	return []byte(b.String())
}

// escapedStrings counts the JSON strings (member names and values) of doc
// written with a backslash escape, and the bytes they decode to.
func escapedStrings(t *testing.T, doc []byte) (count int, decoded int64) {
	t.Helper()
	for i := 0; i < len(doc); i++ {
		if doc[i] != '"' {
			continue
		}
		j, esc := i+1, false
		for ; j < len(doc) && doc[j] != '"'; j++ {
			if doc[j] == '\\' {
				esc = true
				j++
			}
		}
		if esc {
			var s string
			if err := json.Unmarshal(doc[i:j+1], &s); err != nil {
				t.Fatalf("string at %d: %v", i, err)
			}
			count++
			decoded += int64(len(s))
		}
		i = j
	}
	return count, decoded
}
