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

// Scaling tests for the focused second review's complexity fixes G3, G4, G6
// and G7 (ledger, "Focused second review"), and the shared harness every
// scaling test uses: four times the input must cost well under the sixteen
// times a quadratic cost takes. Hardened against CI noise (stage 2 ledger,
// "Test maintenance": "prefer allocation or work counts; for time, best of 5
// and a 12x bound"): a cost whose quadratic shows in the heap allocation
// count is checked on that count, which noise does not move; any other is
// timed, the best of 5 runs of each size, interleaved so both see the same
// machine load, against a 12x bound.

// scaleRuns is how many times the harness runs the work of each size.
const scaleRuns = 5

// Bounds for four times the input: a linear cost takes about 4 times as
// much, a quadratic one 16 times.
const (
	scaleTimeBound  = 12
	scaleAllocBound = 8
)

// scaleCost is one run's wall time and heap allocation count
// (runtime.MemStats.Mallocs).
type scaleCost struct {
	d       time.Duration
	mallocs uint64
}

func measureRun(f func()) scaleCost {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	d := time.Since(start)
	runtime.ReadMemStats(&after)
	return scaleCost{d, after.Mallocs - before.Mallocs}
}

// bestCosts runs a and b scaleRuns times each, interleaved, and returns the
// least time and the least allocation count each took.
func bestCosts(a, b func()) (ca, cb scaleCost) {
	ca = scaleCost{1<<63 - 1, 1<<64 - 1}
	cb = ca
	for range scaleRuns {
		for _, x := range []struct {
			f    func()
			best *scaleCost
		}{{a, &ca}, {b, &cb}} {
			c := measureRun(x.f)
			x.best.d = min(x.best.d, c.d)
			x.best.mallocs = min(x.best.mallocs, c.mallocs)
		}
	}
	return ca, cb
}

// wantLinear checks, by time, that run(4*small) costs at most 12 times
// run(small), where run builds its input and returns the work to measure;
// the work is run scaleRuns times for each size, so work that must start
// fresh each time (a first use) prepares scaleRuns fresh inputs.
func wantLinear(t *testing.T, name string, small int, run func(n int) func()) {
	t.Helper()
	scaleCheck(t, name, small, run, false)
}

// wantLinearAllocs is wantLinear checked on the heap allocation count, for a
// cost whose quadratic shows in allocations, against an 8x bound.
func wantLinearAllocs(t *testing.T, name string, small int, run func(n int) func()) {
	t.Helper()
	scaleCheck(t, name, small, run, true)
}

func scaleCheck(t *testing.T, name string, small int, run func(n int) func(), allocs bool) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if testing.Short() && !allocs {
			t.Skip("timing test")
		}
		ca, cb := bestCosts(run(small), run(4*small))
		timeRatio := float64(cb.d) / float64(max(ca.d, 100*time.Microsecond))
		allocRatio := float64(cb.mallocs) / float64(max(ca.mallocs, 1))
		t.Logf("%d: %v, %d allocations; %d: %v, %d allocations (time %.1fx, allocations %.1fx)",
			small, ca.d, ca.mallocs, 4*small, cb.d, cb.mallocs, timeRatio, allocRatio)
		switch {
		case allocs && allocRatio > scaleAllocBound:
			t.Errorf("four times the input made %.1f times the allocations; want linear", allocRatio)
		case !allocs && timeRatio > scaleTimeBound:
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

// G3 (#3): the member index covers objects with escaped names, and an array
// index is found by position (ledger: "member index covers escaped names
// (decoded once, sorted by decoded name); array index lookup by position,
// not a walk"). Reference resolution stays linear. Every escaped name was
// decoded again at each lookup, so that case is checked on allocations; the
// others were quadratic in time only, and their sizes are where the
// quadratic took 16x (checked against the code before the fix, aa84ce6).
func TestG3LookupScales(t *testing.T) {
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

// retainedBy reports the heap f's result keeps live after a GC.
func retainedBy(f func() any) int64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	keep := f()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(keep)
	return int64(after.HeapAlloc) - int64(before.HeapAlloc)
}

// G4 (#4, #6): Path Item and Reference Object chains are resolved once per
// document with shared tails (ledger), so Paths entries or operations that
// share one long chain cost time and retained memory linear in the input,
// at Load, with Options.Server or Options.MediaType set (whose checks
// follow the chains), and at first use; and those checks honor ctx (T1-12:
// "ctx bounds the whole load").
func TestG4SharedChainsScale(t *testing.T) {
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

// G6 (#7): an escaped string is decoded once per node and shared (ledger),
// so a shared escaped description read by many operations is kept once:
// after Operations(), the escaped document retains no more than the same
// document unescaped plus two copies of the description.
func TestG6SharedEscapedDescription(t *testing.T) {
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

// G7 (#8): a node stores its member name's offset (ledger), so reading a
// name costs the name, not the whitespace around the colon. A document
// whose whitespace and entries both grow four times costs four times as
// much, at Load and in Operations().
func TestG7WhitespaceScales(t *testing.T) {
	wantLinear(t, "Load, references resolved from the root", 2, func(k int) func() {
		return parseDoc(t, padded(k*16<<10, k*250, true), nil)
	})
	wantLinear(t, "Operations()", 2, func(k int) func() {
		return timedOperations(t, padded(k*16<<10, k*250, false))
	})
}
