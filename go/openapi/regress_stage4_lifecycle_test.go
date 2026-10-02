package openapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Regression tests and benchmarks for the stage 4 review round (stage 4
// ledger, "Review round (6917b84)"): class ruling C4-5, lifecycle ("settle
// waits for every generation with a Read in flight, closed or not, after
// the context ends (C3-2); errors are kept while waiting"); class ruling
// C4-6 ("Compile caches are read without a document-wide lock; a lock only
// publishes a computed result, and no lock is held across recursion or
// follow (F13). Measured by a parallel first-use benchmark."); findings A1,
// F11, F13, A10/F25.

// cancelRT is Astra's A1 transport: it reads the request body on a
// goroutine of its own, as net/http's write loop does, waits until the
// caller's Read is in flight, closes the body, cancels the call's context,
// and returns. The Read it started is still running.
type cancelRT struct {
	entered  <-chan struct{}
	cancel   context.CancelFunc
	returned chan struct{}
}

func (rt *cancelRT) RoundTrip(r *http.Request) (*http.Response, error) {
	go io.Copy(io.Discard, r.Body)
	<-rt.entered
	r.Body.Close()
	rt.cancel()
	close(rt.returned)
	return nil, context.Canceled
}

// C4-5 (A1): Call does not return while a Read of the caller's body is in
// flight, though the transport has closed the body and the context has
// ended (client.go, Input: "Call has stopped reading its body when it
// returns, provided a reader body returns from Read when the call's context
// ends"; Input.Body: "Call waits for the iterator to return"; stage 3
// ledger, C3-2: "A body generation ends only when the transport has closed
// it AND no Read is in flight"). The caller's reader and iterator write
// their own state after they are released, which the test writes once Call
// returns: a race the detector reports if Call returned first. Its error
// matches the context's (doc.go, Outcomes).
func TestC45CancelWaitsForReadInFlight(t *testing.T) {
	t.Run("multipart reader", func(t *testing.T) {
		g := newGatedReader(1)
		t.Cleanup(g.open)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		rt := &cancelRT{entered: g.entered, cancel: cancel, returned: make(chan struct{})}
		c := parseAt(t, mpDoc(), "https://h.example.test", "https://h.example.test/openapi.json", &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
		done := make(chan error, 1)
		go func() {
			_, err := c.Call(ctx, "mp", &openapi.Input{Body: map[string]any{"blob": io.Reader(g)}}, nil)
			done <- err
		}()
		awaitCancelled(t, rt.returned, done, g.open)
		g.state = 0 // the caller reuses its reader's state
	})
	t.Run("iterator", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		open := func() { once.Do(func() { close(release) }) }
		t.Cleanup(open)
		state := 0
		it := func(yield func(any) bool) {
			close(entered)
			<-release
			state++ // the caller's own state, written after the gate
			yield(1)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		rt := &cancelRT{entered: entered, cancel: cancel, returned: make(chan struct{})}
		c := parseAt(t, seqDoc(), "https://h.example.test", "https://h.example.test/openapi.json", &openapi.Options{HTTPClient: &http.Client{Transport: rt}})
		done := make(chan error, 1)
		go func() {
			_, err := c.Call(ctx, "jsonl", &openapi.Input{Body: iter.Seq[any](it)}, nil)
			done <- err
		}()
		awaitCancelled(t, rt.returned, done, open)
		state = 0
	})
}

// awaitCancelled waits for the transport to return, checks that the call
// has not returned with the Read still in flight, releases the Read, and
// checks the call's error.
func awaitCancelled(t *testing.T, returned <-chan struct{}, done <-chan error, release func()) {
	t.Helper()
	select {
	case <-returned:
	case err := <-done:
		release()
		t.Fatalf("Call returned (%v) before the transport did", err)
	case <-time.After(10 * time.Second):
		release()
		t.Fatal("the transport did not return within 10s")
	}
	select {
	case err := <-done:
		release()
		t.Fatalf("Call returned (%v) while the caller's Read was in flight", err)
	case <-time.After(grace):
	}
	release()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Call = %v, want an error matching context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Call did not return within 10s of the Read returning")
	}
}

// formOpsDoc is the performance reviewer's F13 document: n operations, each
// with a multipart body of 20 properties referring to 5 shared component
// schemas, two of them with an Encoding, and a form body.
func formOpsDoc(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://h.example.test"}],"paths":{`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"/u%d":{"post":{"operationId":"up%d","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{`, i, i)
		for j := range 20 {
			if j > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `"p%d":{"$ref":"#/components/schemas/S%d"}`, j, j%5)
		}
		b.WriteString(`}},"encoding":{"p0":{"contentType":"image/png"},"p1":{"style":"form","explode":true}}},`)
		b.WriteString(`"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"array","items":{"type":"integer"}}}}}}}}}`)
	}
	b.WriteString(`},"components":{"schemas":{`)
	for j := range 5 {
		if j > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"S%d":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"array","items":{"type":"number"}}}}`, j)
	}
	b.WriteString(`}}}`)
	return []byte(b.String())
}

// firstUseAll describes every operation of a fresh Client from doc with
// workers goroutines, and returns how long that took.
func firstUseAll(tb testing.TB, doc []byte, n, workers int) time.Duration {
	c, err := openapi.Parse(context.Background(), doc, "https://h.example.test/openapi.json", nil)
	if err != nil {
		tb.Fatal(err)
	}
	start := time.Now()
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := w; i < n; i += workers {
				if _, err := c.Operation(fmt.Sprintf("up%d", i)); err != nil {
					tb.Error(err)
				}
			}
		})
	}
	wg.Wait()
	return time.Since(start)
}

// C4-6 (F13), IP4F-1: concurrent first uses of form and multipart
// operations give what a serial first use gives (IP4F-1: "the first stored
// result wins and every caller sees it"), descriptors and prepared bytes
// alike, clean under -race: 400 operations first used by 8 goroutines, each
// in its own order, three times. Stage 4 ledger, IP4F-7: "TestC46's
// wall-clock ratio fails under machine load; the parallel first-use check
// moves to the gate's benchmark pair (serial vs parallel, reported by the
// loop owner), and the test keeps only what is deterministic"; that pair is
// BenchmarkFirstUseFormsSerial and BenchmarkFirstUseFormsParallel below.
func TestC46ConcurrentFirstUse(t *testing.T) {
	const n = 400
	doc := formOpsDoc(n)
	fresh := func() *openapi.Client {
		c, err := openapi.Parse(context.Background(), doc, "https://h.example.test/openapi.json", nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	in := &openapi.Input{MediaType: "application/x-www-form-urlencoded", Body: map[string]any{"a": "x y", "b": []int{1, 2}}}
	describe := func(c *openapi.Client) string {
		var b strings.Builder
		for i := range n {
			op := mustOp(t, c, fmt.Sprintf("up%d", i))
			for _, m := range op.Body.Media {
				for _, e := range m.Encoding {
					fmt.Fprintf(&b, "%s %s %s: %q %q %v %v %d %v\n", op.Key, m.Type, e.Name, e.ContentType, e.Style, e.Explode, e.AllowReserved, len(e.Headers), e.Err)
				}
			}
			req, err := c.Prepare(op.Key, in)
			if err != nil {
				fmt.Fprintf(&b, "%s: %v\n", op.Key, err)
				continue
			}
			fmt.Fprintf(&b, "%s sends %q\n", op.Key, preparedBody(t, req))
		}
		return b.String()
	}
	want := describe(fresh())
	for round := range 3 {
		c := fresh()
		var wg sync.WaitGroup
		for w := range 8 {
			wg.Go(func() {
				for j := range n {
					key := fmt.Sprintf("up%d", (j*7+w*53)%n) // a different order on each goroutine
					if _, err := c.Prepare(key, in); err != nil {
						t.Error(err)
					}
				}
			})
		}
		wg.Wait()
		if got := describe(c); got != want {
			t.Errorf("round %d: concurrent first use differs from serial:\n%s", round, lineDiff(got, want))
			break
		}
	}
}

func benchFirstUseForms(b *testing.B, workers int) {
	const n = 400
	doc := formOpsDoc(n)
	b.ReportAllocs()
	var total time.Duration
	for b.Loop() {
		total += firstUseAll(b, doc, n, workers)
	}
	b.ReportMetric(float64(total.Nanoseconds())/float64(b.N), "ns/firstuse")
}

// BenchmarkFirstUseFormsSerial and BenchmarkFirstUseFormsParallel describe
// the 400 operations of formOpsDoc on a fresh Client from one goroutine and
// from GOMAXPROCS goroutines (C4-6: "Measured by a parallel first-use
// benchmark"). ns/firstuse excludes parsing.
func BenchmarkFirstUseFormsSerial(b *testing.B) { benchFirstUseForms(b, 1) }
func BenchmarkFirstUseFormsParallel(b *testing.B) {
	benchFirstUseForms(b, runtime.GOMAXPROCS(0))
}

// A10, F25: Part.Header's field names are checked through a set of
// canonical names, so a part with many header fields costs time linear in
// their number (at 6917b84 every name was compared with every other: 8,000
// fields took 16.6 times as long as 2,000).
func TestA10PartHeaderScales(t *testing.T) {
	c := parseAt(t, mpDoc(), "https://api.example.test", testDocURI, nil)
	wantLinear(t, "Prepare", 500, func(n int) func() {
		h := make(http.Header, n)
		for i := range n {
			h[fmt.Sprintf("X-%08d", i)] = []string{"v"}
		}
		in := &openapi.Input{Body: map[string]any{"title": openapi.Part{Content: "x", Header: h}}}
		if _, err := c.Prepare("mp", in); err != nil {
			t.Errorf("Prepare: %.200v", err)
			return func() {}
		}
		return func() { c.Prepare("mp", in) }
	})
}

// BenchmarkMemBodyFormReaderOnce streams a 1 MiB form field from a reader
// read once through the in-memory transport (F11: "keep the streaming form
// buffer's backing array and a read offset"; at 6917b84 about 3.5 bytes
// allocated per byte streamed). Compare B/op with the 1 MiB SetBytes.
func BenchmarkMemBodyFormReaderOnce(b *testing.B) {
	c, _ := bodyBenchClient(b)
	field := strings.Repeat("good dog & treats ", (1<<20)/18)
	ctx := context.Background()
	b.SetBytes(int64(len(field)))
	b.ReportAllocs()
	for b.Loop() {
		in := &openapi.Input{Body: map[string]any{"name": "Rex", "notes": newOnce(field)}}
		if _, err := c.Call(ctx, "formPet", in, nil); err != nil {
			b.Fatal(err)
		}
	}
}
