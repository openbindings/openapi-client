package openapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Independent bounded framing oracle: split at the format's delimiter,
// then use encoding/json with UseNumber. It does not share a parser or
// iterator with production. JSON-seq seeds all begin with RS; the fuzzed
// records are arbitrary, including raw delimiters, malformed text and
// non-ASCII bytes. The corpus limit keeps each fuzz execution bounded.
func stream7JSONOracle(data string, seq bool) ([]any, []bool) {
	delimiter := "\n"
	if seq {
		delimiter = "\x1e"
	}
	parts := strings.Split(data, delimiter)
	if seq {
		parts = parts[1:]
	}
	var values []any
	var bad []bool
	for _, part := range parts {
		if seq && part == "" {
			continue
		}
		if !seq && strings.Trim(part, " \t\r") == "" {
			continue
		}
		var value any
		dec := json.NewDecoder(strings.NewReader(part))
		dec.UseNumber()
		err := dec.Decode(&value)
		if err == nil {
			var tail any
			if dec.Decode(&tail) == nil {
				err = errors.New("second value")
			} else if !json.Valid([]byte(part)) {
				err = errors.New("trailing junk")
			}
		}
		if seq && err == nil {
			trim := strings.TrimLeft(part, " \t\r\n")
			if len(trim) > 0 && !strings.ContainsRune("{[\"", rune(trim[0])) {
				last := part[len(part)-1]
				if last != ' ' && last != '\t' && last != '\r' && last != '\n' {
					err = errors.New("scalar missing canary")
				}
			}
		}
		if err != nil {
			value = nil
		}
		values = append(values, value)
		bad = append(bad, err != nil)
	}
	return values, bad
}
func FuzzStream7JSONFraming(f *testing.F) {
	for _, seed := range []string{"1\n{\"n\":9007199254740993}\n", "\x1e123\x1etrue \x1e[1,2]", "\n{bad}\nnull\n", "\"é\\n\\u001e\"\n", "\x1e\x1e{}\n", " ", ""} {
		f.Add(seed, byte(1), false)
		f.Add(seed, byte(3), true)
	}
	f.Fuzz(func(t *testing.T, input string, chunk byte, seq bool) {
		if len(input) > 32768 {
			t.Skip()
		}
		ct := "application/jsonl"
		wire := input
		if seq {
			ct = "application/json-seq"
			wire = "\x1e" + wire
		}
		want, bad := stream7JSONOracle(wire, seq)
		r, _ := stream7Response(t, ct, wire, nil, int(chunk)%31+1)
		got, errs := stream7Collect[any](r)
		if len(got) != len(want) {
			t.Fatalf("got %d want %d; errors %v wire %q", len(got), len(want), errs, wire)
		}
		for i := range got {
			if bad[i] {
				if !errors.Is(errs[i], openapi.ErrItem) {
					t.Fatalf("item %d error %v", i, errs[i])
				}
			} else if errs[i] != nil || !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("item %d got %#v %v want %#v", i, got[i], errs[i], want[i])
			}
		}
	})
}

// Independent SSE oracle: generated data fields have arbitrary UTF-8 data,
// escaped physical line boundaries, and vary CR/LF/CRLF terminators. JSON
// marshaling constructs expected event objects, never the production SSE
// encoder. An incomplete final block must not dispatch (HTML §9.2.6).
func FuzzStream7SSEFields(f *testing.F) {
	f.Add("Rex\nFido", uint16(0), byte(0))
	f.Add("é\xff\n", uint16(1000), byte(1))
	f.Add("", uint16(12), byte(2))
	f.Fuzz(func(t *testing.T, data string, retry uint16, ending byte) {
		if len(data) > 32768 {
			t.Skip()
		}
		data = strings.ToValidUTF8(data, "\ufffd")
		data = strings.ReplaceAll(data, "\r", "\n")
		sep := []string{"\n", "\r", "\r\n"}[int(ending)%3]
		var wire strings.Builder
		wire.WriteString("\ufeff:ignored" + sep)
		for _, line := range strings.Split(data, "\n") {
			wire.WriteString("data: " + line + sep)
		}
		wire.WriteString("id: before" + sep + "id:" + sep + "retry: ")
		num, _ := json.Marshal(retry)
		wire.Write(num)
		wire.WriteString(sep + sep + "data: unfinished")
		r, _ := stream7Response(t, "text/event-stream", wire.String(), nil, 1+int(ending)%7)
		got, errs := stream7Collect[map[string]any](r)
		stream7NoErrors(t, errs)
		want := map[string]any{"data": data, "id": "", "retry": json.Number(string(num))}
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Fatalf("got %#v want %#v wire %q", got, want, wire.String())
		}
		r, _ = stream7Response(t, "text/event-stream", wire.String(), nil, 1)
		i := 0
		for ev, err := range openapi.Events(r) {
			if err != nil || ev.Data == nil || !bytes.Equal(ev.Data, []byte(data)) || !ev.IDSet || ev.ID != "" || !ev.RetrySet || ev.Retry.Milliseconds() != int64(retry) {
				t.Fatalf("event %#v error %v", ev, err)
			}
			i++
		}
		if i != 1 {
			t.Fatalf("event count %d", i)
		}
	})
}
