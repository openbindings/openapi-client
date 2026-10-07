package openapi_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Scaling tests for document lookups, shared reference chains, escaped
// strings and member names, and the shared harness every scaling test uses:
// four times the input must cost well under the sixteen times a quadratic
// cost takes. To resist CI noise, allocation counts are preferred to time:
// every scaling test is checked on the bytes it allocates, which noise does
// not move, against an 8x bound, and on its time, the best of 5 runs of each
// size, interleaved so both see the same machine load, against a 12x bound,
// whatever measure it names; a cost whose quadratic shows in the heap
// allocation count is checked on that too.

// scaleRuns is how many times the harness runs the work of each size.
const scaleRuns = 5

// Bounds for four times the input: a linear cost takes about 4 times as
// much, a quadratic one 16 times.
const (
	scaleTimeBound  = 12
	scaleAllocBound = 8
)

// scaleMeasure is what the harness checks.
type scaleMeasure int

const (
	byTime   scaleMeasure = iota
	byAllocs              // runtime.MemStats.Mallocs
	byBytes               // runtime.MemStats.TotalAlloc
)

// scaleCost is one run's wall time, heap allocation count and bytes
// allocated.
type scaleCost struct {
	d              time.Duration
	mallocs, bytes uint64
}

func measureRun(f func()) scaleCost {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	d := time.Since(start)
	runtime.ReadMemStats(&after)
	return scaleCost{d, after.Mallocs - before.Mallocs, after.TotalAlloc - before.TotalAlloc}
}

// bestCosts runs a and b scaleRuns times each, interleaved, and returns the
// least of each measure each took.
func bestCosts(a, b func()) (ca, cb scaleCost) {
	ca = scaleCost{1<<63 - 1, 1<<64 - 1, 1<<64 - 1}
	cb = ca
	for range scaleRuns {
		for _, x := range []struct {
			f    func()
			best *scaleCost
		}{{a, &ca}, {b, &cb}} {
			c := measureRun(x.f)
			x.best.d = min(x.best.d, c.d)
			x.best.mallocs = min(x.best.mallocs, c.mallocs)
			x.best.bytes = min(x.best.bytes, c.bytes)
		}
	}
	return ca, cb
}

// wantLinear checks that run(4*small) costs at most 12 times the time and 8
// times the bytes allocated of run(small), where run builds its input and
// returns the work to measure; the work is run scaleRuns times for each
// size, so work that must start fresh each time (a first use) prepares
// scaleRuns fresh inputs.
func wantLinear(t *testing.T, name string, small int, run func(n int) func()) {
	t.Helper()
	scaleCheck(t, name, small, run, byTime)
}

// wantLinearAllocs is wantLinear checked on the heap allocation count too,
// for a cost whose quadratic shows in allocations, against an 8x bound. It
// runs under -short, which skips only its time check.
func wantLinearAllocs(t *testing.T, name string, small int, run func(n int) func()) {
	t.Helper()
	scaleCheck(t, name, small, run, byAllocs)
}

// wantLinearBytes is wantLinear for a cost whose quadratic would show in
// copying (as in building a string by repeated concatenation), which the
// bytes check every scaling test has finds; it runs under -short, which
// skips only its time check.
func wantLinearBytes(t *testing.T, name string, small int, run func(n int) func()) {
	t.Helper()
	scaleCheck(t, name, small, run, byBytes)
}

// flatBound bounds the cost of work that must not grow with its input, at
// 16 times the input, where a cost proportional to it takes about 16 times.
const flatBound = 3

// wantFlat checks that run(large) costs at most 3 times the time and bytes
// allocated of run(small), large being 16 times small, for work whose cost
// must not depend on the size that grows (as a cost per field must not
// depend on what the field reaches); time is not checked under -short.
func wantFlat(t *testing.T, name string, small int, run func(n int) func()) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		ca, cb := bestCosts(run(small), run(16*small))
		timeRatio := float64(cb.d) / float64(max(ca.d, 100*time.Microsecond))
		byteRatio := float64(cb.bytes) / float64(max(ca.bytes, 1))
		t.Logf("%d: %v, %d bytes; %d: %v, %d bytes (time %.1fx, bytes %.1fx)", small, ca.d, ca.bytes, 16*small, cb.d, cb.bytes, timeRatio, byteRatio)
		if byteRatio > flatBound {
			t.Errorf("sixteen times the input allocated %.1f times the bytes; want a cost independent of it", byteRatio)
		}
		if !testing.Short() && timeRatio > flatBound {
			t.Errorf("sixteen times the input took %.1f times as long; want a cost independent of it", timeRatio)
		}
	})
}

// scaleCheck runs the scaling check: bytes allocated and time always (time
// but under -short), and the allocation count when m names it. A test that
// names time alone is skipped under -short.
func scaleCheck(t *testing.T, name string, small int, run func(n int) func(), m scaleMeasure) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if testing.Short() && m == byTime {
			t.Skip("timing test")
		}
		ca, cb := bestCosts(run(small), run(4*small))
		timeRatio := float64(cb.d) / float64(max(ca.d, 100*time.Microsecond))
		allocRatio := float64(cb.mallocs) / float64(max(ca.mallocs, 1))
		byteRatio := float64(cb.bytes) / float64(max(ca.bytes, 1))
		t.Logf("%d: %v, %d allocations, %d bytes; %d: %v, %d allocations, %d bytes (time %.1fx, allocations %.1fx, bytes %.1fx)",
			small, ca.d, ca.mallocs, ca.bytes, 4*small, cb.d, cb.mallocs, cb.bytes, timeRatio, allocRatio, byteRatio)
		if m == byAllocs && allocRatio > scaleAllocBound {
			t.Errorf("four times the input made %.1f times the allocations; want linear", allocRatio)
		}
		if byteRatio > scaleAllocBound {
			t.Errorf("four times the input allocated %.1f times the bytes; want linear", byteRatio)
		}
		if !testing.Short() && timeRatio > scaleTimeBound {
			t.Errorf("four times the input took %.1f times as long; want linear", timeRatio)
		}
	})
}

// freshClients parses doc n times, for work that must meet a Client whose
// operations are not yet compiled.
func freshClients(t *testing.T, doc []byte, opts func() *openapi.Options, n int) []*openapi.Client {
	t.Helper()
	cs := make([]*openapi.Client, n)
	for i := range cs {
		var o *openapi.Options
		if opts != nil {
			o = opts()
		}
		c, err := openapi.Parse(context.Background(), doc, testDocURI, o)
		if err != nil {
			t.Fatal(err)
		}
		cs[i] = c
	}
	return cs
}

// parseDoc returns work that parses doc and checks it has operations.
func parseDoc(t *testing.T, doc []byte, opts *openapi.Options) func() {
	return func() {
		o := opts
		if o != nil {
			copied := *o
			o = &copied
		}
		c, err := openapi.Parse(context.Background(), doc, testDocURI, o)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Operations()) == 0 {
			t.Fatal("no operations")
		}
	}
}

// timedOperations returns work that lists the operations of a freshly
// parsed doc, a new Client for each of the harness's runs; only
// Operations() is measured.
func timedOperations(t *testing.T, doc []byte) func() {
	docs := freshClients(t, doc, nil, scaleRuns)
	i := 0
	return func() {
		ops := docs[i%len(docs)].Operations()
		i++
		for _, op := range ops {
			if op.Err != nil {
				t.Fatalf("%s: %v", op.Key, op.Err)
			}
		}
	}
}

// refsInto returns a document with n Paths entries, each a $ref to its own
// member of components.pathItems; escape is "none", "one" (one extra member
// with an escaped name) or "all" (every member name escaped).
func refsInto(n int, escape string) []byte {
	const bs = `\`
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{`)
	for i := range n {
		fmt.Fprintf(&b, `"/p%d":{"$ref":"#/components/pathItems/c%d"},`, i, i)
	}
	b.WriteString(`"/z":{"get":{}}},"components":{"pathItems":{`)
	if escape == "one" {
		b.WriteString(`"` + bs + `u0065nd":{},`)
	}
	for i := range n {
		if escape == "all" {
			fmt.Fprintf(&b, `"%su0063%d":{"get":{}},`, bs, i) // "c<i>", spelled with an escape
		} else {
			fmt.Fprintf(&b, `"c%d":{"get":{}},`, i)
		}
	}
	b.WriteString(`"zz":{}}}}`)
	return []byte(b.String())
}

// The member index covers objects with escaped names (decoded once, sorted
// by decoded name), and an array index is found by position, not by a walk.
// Reference resolution stays linear. Decoding every escaped name again at
// each lookup would show in allocations, so that case is checked on them;
// the others would be quadratic in time only, and their sizes are where a
// quadratic implementation takes 16x.
func TestReferenceLookupScales(t *testing.T) {
	wantLinear(t, "one escaped member name", 4000, func(n int) func() { return parseDoc(t, refsInto(n, "one"), nil) })
	wantLinearAllocs(t, "every member name escaped", 250, func(n int) func() { return parseDoc(t, refsInto(n, "all"), nil) })
	wantLinear(t, "array index pointers", 6000, func(n int) func() {
		var b strings.Builder
		b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{`)
		for i := range n {
			fmt.Fprintf(&b, `"/p%d":{"$ref":"#/x-items/%d"},`, i, n-1-i%2)
		}
		b.WriteString(`"/z":{"get":{}}},"x-items":[`)
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"get":{}}`)
		}
		b.WriteString(`]}`)
		return parseDoc(t, []byte(b.String()), nil)
	})
	wantLinear(t, "parameter references at first use, one escaped name", 4000, func(n int) func() {
		var b strings.Builder
		b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://h.example.test"}],"paths":{"/x":{"get":{"operationId":"op","parameters":[`)
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"$ref":"#/components/parameters/p%d"}`, i)
		}
		b.WriteString(`]}}},"components":{"parameters":{"` + `\` + `/q":{"name":"q","in":"query"},`)
		for i := range n {
			fmt.Fprintf(&b, `"p%d":{"name":"p%d","in":"query"},`, i, i)
		}
		b.WriteString(`"end":{"name":"end","in":"query"}}}}`)
		return timedOperations(t, []byte(b.String()))
	})
}

// pathChainFanIn returns a document with n Paths entries, each a $ref to
// the head of one chain of n Path Items, the last of which defines get.
func pathChainFanIn(n int, server bool) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},`)
	if server {
		b.WriteString(`"servers":[{"url":"https://api.example.test"}],`)
	}
	b.WriteString(`"paths":{`)
	for i := range n {
		fmt.Fprintf(&b, `"/p%d":{"$ref":"#/components/pathItems/c0"},`, i)
	}
	b.WriteString(`"/z":{"get":{}}},"components":{"pathItems":{`)
	for i := range n {
		fmt.Fprintf(&b, `"c%d":{"$ref":"#/components/pathItems/c%d"},`, i, i+1)
	}
	fmt.Fprintf(&b, `"c%d":{"get":{}}}}}`, n)
	return []byte(b.String())
}

// refChainFanIn returns a document with n operations, each whose parameter
// (kind "parameters") or request body (kind "requestBodies") is a Reference
// Object to the head of one chain of n.
func refChainFanIn(n int, kind string) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.test"}],"paths":{`)
	for i := range n {
		if kind == "parameters" {
			fmt.Fprintf(&b, `"/p%d":{"get":{"parameters":[{"$ref":"#/components/parameters/c0"}]}},`, i)
		} else {
			fmt.Fprintf(&b, `"/p%d":{"post":{"requestBody":{"$ref":"#/components/requestBodies/c0"}}},`, i)
		}
	}
	b.WriteString(`"/z":{"get":{}}},"components":{"` + kind + `":{`)
	for i := range n {
		fmt.Fprintf(&b, `"c%d":{"$ref":"#/components/%s/c%d"},`, i, kind, i+1)
	}
	if kind == "parameters" {
		fmt.Fprintf(&b, `"c%d":{"name":"q","in":"query"}}}}`, n)
	} else {
		fmt.Fprintf(&b, `"c%d":{"content":{"application/json":{}}}}}}`, n)
	}
	return []byte(b.String())
}

// retainedBy reports the heap f's result keeps live after a GC. Each
// reading follows two collections, so objects that an earlier test left in
// a sync.Pool, which survive one collection in its victim cache, are gone
// from both readings rather than freed between them.
func retainedBy(f func() any) int64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)
	keep := f()
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(keep)
	return int64(after.HeapAlloc) - int64(before.HeapAlloc)
}

// Path Item and Reference Object chains are resolved once per document with
// shared tails, so Paths entries or operations that share one long chain cost
// time and retained memory linear in the input, at Load, with Options.Server
// or Options.MediaType set (whose checks follow the chains), and at first
// use; and those checks honor ctx (Load: "ctx bounds the whole load").
func TestSharedReferenceChainsScale(t *testing.T) {
	wantLinear(t, "Paths entries sharing a Path Item chain, Load", 150, func(n int) func() {
		return parseDoc(t, pathChainFanIn(n, false), nil)
	})
	wantLinear(t, "the same with Options.Server", 150, func(n int) func() {
		return parseDoc(t, pathChainFanIn(n, true), &openapi.Options{Server: "https://api.example.test"})
	})
	wantLinear(t, "operations sharing a parameter chain, Operations()", 150, func(n int) func() {
		return timedOperations(t, refChainFanIn(n, "parameters"))
	})
	wantLinear(t, "operations sharing a request body chain, Load with Options.MediaType", 150, func(n int) func() {
		return parseDoc(t, refChainFanIn(n, "requestBodies"), &openapi.Options{MediaType: "application/json"})
	})

	t.Run("retained memory of a shared Path Item chain", func(t *testing.T) {
		keep := func(n int) int64 {
			doc := pathChainFanIn(n, false)
			return retainedBy(func() any {
				c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
				if err != nil {
					t.Fatal(err)
				}
				return c
			})
		}
		small, large := keep(250), keep(1000)
		ratio := float64(large) / float64(max(small, 64<<10))
		t.Logf("250: %d bytes, 1000: %d bytes (%.1fx)", small, large, ratio)
		if ratio > 8 {
			t.Errorf("four times the input retained %.1f times the memory; want linear", ratio)
		}
	})

	t.Run("Load with Options.MediaType honors a deadline", func(t *testing.T) {
		doc := refChainFanIn(1000, "requestBodies")
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := openapi.Parse(ctx, doc, testDocURI, &openapi.Options{MediaType: "application/xml"})
		if err == nil {
			t.Errorf("Parse accepted Options.MediaType that no operation declares")
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("Parse with a 100ms deadline returned after %v", d.Round(time.Millisecond))
		}
	})
}

// An escaped string is decoded once per node and shared, so a shared escaped
// description read by many operations is kept once: after Operations(), the
// escaped document retains no more than the same document unescaped plus two
// copies of the description.
func TestSharedEscapedDescriptionRetainedOnce(t *testing.T) {
	const refs = 200
	const descLen = 256 << 10
	build := func(escaped bool) []byte {
		desc := strings.Repeat("a", descLen)
		if escaped {
			desc = `\n` + desc // one escape, as any multi-line description has
		}
		var b strings.Builder
		b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{`)
		for i := range refs {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `"/p%d":{"get":{"responses":{"200":{"$ref":"#/components/responses/r"}}}}`, i)
		}
		b.WriteString(`},"components":{"responses":{"r":{"description":"` + desc + `"}}}}`)
		return []byte(b.String())
	}
	keep := func(doc []byte) int64 {
		c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
		if err != nil {
			t.Fatal(err)
		}
		return retainedBy(func() any { return c.Operations() })
	}
	plain, escaped := keep(build(false)), keep(build(true))
	t.Logf("retained after Operations(): unescaped %d bytes, escaped %d bytes", plain, escaped)
	if escaped > plain+2*descLen {
		t.Errorf("the escaped description is kept %d times over; want once", (escaped-plain)/descLen)
	}
}

// padded returns a document whose root holds 12 extension members, each
// with pad spaces after its colon, and n Paths entries: each a $ref to one
// shared Path Item when refs is set, else an operation of its own.
func padded(pad, n int, refs bool) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},`)
	for i := range 12 {
		fmt.Fprintf(&b, `"x-pad%d":%s0,`, i, strings.Repeat(" ", pad))
	}
	b.WriteString(`"paths":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		if refs {
			fmt.Fprintf(&b, `"/p%d":{"$ref":"#/components/pathItems/a"}`, i)
		} else {
			fmt.Fprintf(&b, `"/p%d":{"get":{}}`, i)
		}
	}
	b.WriteString(`},"components":{"pathItems":{"a":{"get":{}}}}}`)
	return []byte(b.String())
}

// A node stores its member name's offset, so reading a name costs the name,
// not the whitespace around the colon. A document whose whitespace and
// entries both grow four times costs four times as much, at Load and in
// Operations().
func TestWhitespaceAroundMemberNamesScales(t *testing.T) {
	wantLinear(t, "Load, references resolved from the root", 2, func(k int) func() {
		return parseDoc(t, padded(k*16<<10, k*250, true), nil)
	})
	wantLinear(t, "Operations()", 2, func(k int) func() {
		return timedOperations(t, padded(k*16<<10, k*250, false))
	})
}
