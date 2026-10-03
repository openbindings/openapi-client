package openapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

type stream7BenchRecord struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
type stream7BenchResult struct{ Count, Sum int }

func stream7BenchmarkFixture(kind string) (ct, wire string, want stream7BenchResult) {
	const n = 256
	var b strings.Builder
	for i := range n {
		record := fmt.Sprintf(`{"id":%d,"name":"pet-%04d"}`, i, i)
		want.Count++
		want.Sum += i
		switch kind {
		case "JSONL", "EarlyBreak":
			ct = "application/jsonl"
			b.WriteString(record + "\n")
		case "JSONSeq":
			ct = "application/json-seq"
			b.WriteString("\x1e" + record + "\n")
		case "Events":
			ct = "text/event-stream"
			b.WriteString("data: " + record + "\nid: " + strconv.Itoa(i) + "\n\n")
		case "Multipart", "RawMultipart":
			ct = "multipart/mixed; boundary=B"
			b.WriteString("--B\r\nContent-Type: application/json\r\n\r\n" + record + "\r\n")
		}
	}
	if kind == "Multipart" || kind == "RawMultipart" {
		b.WriteString("--B--\r\n")
	}
	if kind == "EarlyBreak" {
		want = stream7BenchResult{Count: 1}
	}
	if kind == "RawMultipart" || kind == "Events" {
		want.Sum = 0
		for i := range n {
			want.Sum += len(fmt.Sprintf(`{"id":%d,"name":"pet-%04d"}`, i, i))
			if kind == "Events" {
				want.Sum += len(strconv.Itoa(i)) + 1
			}
		}
	}
	return ct, b.String(), want
}

// Controls deliberately use only standard-library framing/decoding for
// these identical fixtures. The scanner buffer is large enough for each
// known short record. A fresh response/target belongs to every operation.
func stream7HandRead(kind string, body io.ReadCloser) (result stream7BenchResult, err error) {
	defer body.Close()
	if kind == "Multipart" || kind == "RawMultipart" {
		mr := multipart.NewReader(body, "B")
		for {
			p, e := mr.NextPart()
			if e == io.EOF {
				return result, nil
			}
			if e != nil {
				return result, e
			}
			result.Count++
			if kind == "RawMultipart" {
				n, e := io.Copy(io.Discard, p)
				result.Sum += int(n)
				if e != nil {
					return result, e
				}
			} else {
				var v stream7BenchRecord
				if e = json.NewDecoder(p).Decode(&v); e != nil {
					return result, e
				}
				result.Sum += v.ID
			}
		}
	}
	sc := bufio.NewScanner(body)
	var event openapi.Event
	for sc.Scan() {
		line := sc.Bytes()
		if kind == "Events" {
			if bytes.HasPrefix(line, []byte("data: ")) {
				event.Data = append(event.Data[:0], line[6:]...)
			}
			if bytes.HasPrefix(line, []byte("id: ")) {
				event.ID = string(line[4:])
				event.IDSet = true
			}
			if len(line) == 0 {
				result.Count++
				result.Sum += len(event.Data) + len(event.ID)
				if event.IDSet {
					result.Sum++
				}
				event = openapi.Event{Data: event.Data[:0]}
			}
			continue
		}
		if kind == "JSONSeq" {
			if len(line) == 0 || line[0] != 0x1e {
				return result, fmt.Errorf("fixture missing RS")
			}
			line = line[1:]
		}
		var v stream7BenchRecord
		if e := json.Unmarshal(line, &v); e != nil {
			return result, e
		}
		result.Count++
		result.Sum += v.ID
		if kind == "EarlyBreak" {
			return result, nil
		}
	}
	return result, sc.Err()
}
func stream7APIRead(kind string, r *openapi.Response) (result stream7BenchResult, err error) {
	switch kind {
	case "Events":
		for ev, e := range openapi.Events(r) {
			if e != nil {
				return result, e
			}
			result.Count++
			result.Sum += len(ev.Data) + len(ev.ID)
			if ev.IDSet {
				result.Sum++
			}
		}
	case "RawMultipart":
		for p, e := range openapi.Items[*multipart.Part](r) {
			if e != nil {
				return result, e
			}
			n, e := io.Copy(io.Discard, p)
			if e != nil {
				return result, e
			}
			result.Count++
			result.Sum += int(n)
		}
	default:
		for v, e := range openapi.Items[stream7BenchRecord](r) {
			if e != nil {
				return result, e
			}
			result.Count++
			result.Sum += v.ID
			if kind == "EarlyBreak" {
				break
			}
		}
	}
	return result, nil
}

// Verify generated workload/control parity separately from timings, so a
// faster control cannot silently do less work. Fixture expectations are
// computed from records before framing, independent of either reader.
func TestStream7BenchmarkFixtures(t *testing.T) {
	for _, kind := range []string{"JSONL", "JSONSeq", "Events", "Multipart", "RawMultipart", "EarlyBreak"} {
		t.Run(kind, func(t *testing.T) {
			_, wire, want := stream7BenchmarkFixture(kind)
			got, e := stream7HandRead(kind, io.NopCloser(strings.NewReader(wire)))
			if e != nil || got != want {
				t.Fatalf("control %v %v want %v", got, e, want)
			}
		})
	}
}

func stream7Benchmark(b *testing.B, kind string, hand bool) {
	ct, wire, want := stream7BenchmarkFixture(kind)
	rt := stream7RT(func(r *http.Request) (*http.Response, error) {
		return stream7HTTP(r, 200, ct, io.NopCloser(strings.NewReader(wire))), nil
	})
	c := stream7Client(b, "3.1.2", rt, nil)
	req := mustPrepare(b, c, "get", nil)
	hc := &http.Client{Transport: rt}
	ctx := context.Background()
	run := func() (stream7BenchResult, error) {
		if hand {
			r, e := hc.Do(req.HTTP.WithContext(ctx))
			if e != nil {
				return stream7BenchResult{}, e
			}
			return stream7HandRead(kind, r.Body)
		}
		r, e := req.Send(ctx)
		if e != nil {
			return stream7BenchResult{}, e
		}
		return stream7APIRead(kind, r)
	}
	// This call is outside the benchmark timer and validates the actual path.
	got, e := run()
	if e != nil || got != want {
		b.Fatalf("result %v %v want %v", got, e, want)
	}
	if kind == "EarlyBreak" {
		b.SetBytes(int64(strings.IndexByte(wire, '\n') + 1))
	} else {
		b.SetBytes(int64(len(wire)))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got, e := run()
		if e != nil || got != want {
			b.Fatalf("result %v %v want %v", got, e, want)
		}
	}
}
func BenchmarkStream7JSONL(b *testing.B)            { stream7Benchmark(b, "JSONL", false) }
func BenchmarkStream7HandJSONL(b *testing.B)        { stream7Benchmark(b, "JSONL", true) }
func BenchmarkStream7JSONSeq(b *testing.B)          { stream7Benchmark(b, "JSONSeq", false) }
func BenchmarkStream7HandJSONSeq(b *testing.B)      { stream7Benchmark(b, "JSONSeq", true) }
func BenchmarkStream7Events(b *testing.B)           { stream7Benchmark(b, "Events", false) }
func BenchmarkStream7HandEvents(b *testing.B)       { stream7Benchmark(b, "Events", true) }
func BenchmarkStream7Multipart(b *testing.B)        { stream7Benchmark(b, "Multipart", false) }
func BenchmarkStream7HandMultipart(b *testing.B)    { stream7Benchmark(b, "Multipart", true) }
func BenchmarkStream7RawMultipart(b *testing.B)     { stream7Benchmark(b, "RawMultipart", false) }
func BenchmarkStream7HandRawMultipart(b *testing.B) { stream7Benchmark(b, "RawMultipart", true) }
func BenchmarkStream7EarlyBreak(b *testing.B)       { stream7Benchmark(b, "EarlyBreak", false) }
func BenchmarkStream7HandEarlyBreak(b *testing.B)   { stream7Benchmark(b, "EarlyBreak", true) }
