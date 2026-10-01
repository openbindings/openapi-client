package openapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Stage 4, sequential bodies, in any edition. client.go, Input.Body: "For
// a sequential media type (JSON Lines, JSON text sequences, server-sent
// events), in any edition, Body is a slice, an iter.Seq, or an iter.Seq2
// whose second value is an error, of any element type; each element is one
// item. Under text/event-stream an item is an object with no members but
// data, event and id, as strings, and retry, as a non-negative integer, or
// an [Event], of which only the fields it sets are used. It is written as
// those fields, data as one data line per line, then a blank line; any
// other member or type, or a line break in event or id, is an item that
// cannot be encoded." Stage brief, Scope: "each item encoded as its own body
// would be". The framing is each authority's: JSON Lines ("Each Line is a
// Valid JSON Value"; "Line Separator is '\n'"), RFC 7464 section 2.2
// ("JSON-sequence = *(RS JSON-text LF)"), and the HTML standard's event
// stream (section 9.2.5). Whether a JSON Lines body ends with "\n" is not
// settled (wantLines accepts both).

const seqPaths = `
	"/jsonl":{"post":{"operationId":"jsonl","requestBody":{"content":{"application/jsonl":{}}}}},
	"/ndjson":{"post":{"operationId":"ndjson","requestBody":{"content":{"application/x-ndjson":{}}}}},
	"/seq":{"post":{"operationId":"seq","requestBody":{"content":{"application/json-seq":{}}}}},
	"/geoseq":{"post":{"operationId":"geoseq","requestBody":{"content":{"application/geo+json-seq":{}}}}},
	"/sse":{"post":{"operationId":"sse","requestBody":{"content":{"text/event-stream":{}}}}},
	"/import":{"post":{"operationId":"importPets","requestBody":{"required":true,"content":{"application/jsonl":{"schema":{"type":"array"}}}}}}`

func seqDoc() string { return doc31(seqPaths) }

// petList is a named slice type, a slice all the same.
type petList []Pet

// seqOf and seq2Of turn a slice into the iterators Input.Body takes.
func seqOf[T any](items ...T) iter.Seq[T] {
	return func(yield func(T) bool) {
		for _, it := range items {
			if !yield(it) {
				return
			}
		}
	}
}

func seq2Of[T any](items ...T) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for _, it := range items {
			if !yield(it, nil) {
				return
			}
		}
	}
}

// jsonItemBodies is every body shape of the same items, with the JSON text
// json.Marshal writes for each (stage 1 ledger, Q6: "as encoding/json
// writes" means json.Marshal, HTML escaping included).
func jsonItemBodies() []struct {
	name  string
	body  any
	items []string
} {
	pets := []Pet{{Name: "Rex"}, {Name: "Tom", Tag: "<cat>"}}
	petsJSON := []string{`{"name":"Rex"}`, `{"name":"Tom","tag":"` + htmlEsc("003c") + `cat` + htmlEsc("003e") + `"}`}
	anys := []any{1, "x", nil, map[string]any{"a": []int{}}, true, json.Number("1.50"), 2.5}
	anysJSON := []string{`1`, `"x"`, `null`, `{"a":[]}`, `true`, `1.50`, `2.5`}
	return []struct {
		name  string
		body  any
		items []string
	}{
		{"slice of structs", pets, petsJSON},
		{"named slice type", petList(pets), petsJSON},
		{"slice of any", anys, anysJSON},
		{"slice of maps", []map[string]int{{"b": 2, "a": 1}}, []string{`{"a":1,"b":2}`}},
		{"json.RawMessage items", []json.RawMessage{json.RawMessage(`{"raw":1}`), json.RawMessage(`[2]`)}, []string{`{"raw":1}`, `[2]`}},
		{"iter.Seq of structs", seqOf(pets...), petsJSON},
		{"iter.Seq2 of structs", seq2Of(pets...), petsJSON},
		{"iter.Seq[any]", seqOf(anys...), anysJSON},
		{"iter.Seq2[any, error]", seq2Of(anys...), anysJSON},
		{"iter.Seq of pointers", seqOf(&pets[0], (*Pet)(nil)), []string{petsJSON[0], `null`}},
		{"empty slice", []Pet{}, nil},
		{"empty iterator", seqOf[any](), nil},
	}
}

// JSON Lines and NDJSON: each item as its own JSON body is written, then a
// line separator.
func TestJSONLinesBodies(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	for _, key := range []string{"jsonl", "ndjson"} {
		for _, tt := range jsonItemBodies() {
			t.Run(key+" "+tt.name, func(t *testing.T) {
				mustCall(t, c, key, &openapi.Input{Body: tt.body}, nil)
				got := w.last(t)
				wantLines(t, got.Body, tt.items...)
				ct := map[string]string{"jsonl": "application/jsonl", "ndjson": "application/x-ndjson"}[key]
				if v := got.Header.Values("Content-Type"); !slices.Equal(v, []string{ct}) {
					t.Errorf("Content-Type = %q, want %q", v, ct)
				}
			})
		}
	}
}

// JSON text sequences, application/json-seq and any +json-seq type (doc.go,
// Values: sequential types are "JSON Lines, JSON text sequences, server-sent
// events"; client.go, Items: "application/json-seq and any +json-seq type:
// one per RFC 7464 record"): each item preceded by RS and followed by LF,
// exactly (RFC 7464 section 2.2), which also gives a top-level number the
// whitespace after it that parsers check for.
func TestJSONTextSequenceBodies(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	for _, key := range []string{"seq", "geoseq"} {
		for _, tt := range jsonItemBodies() {
			t.Run(key+" "+tt.name, func(t *testing.T) {
				mustCall(t, c, key, &openapi.Input{Body: tt.body}, nil)
				if got := w.last(t); string(got.Body) != jsonSeq(tt.items...) {
					t.Errorf("body %q, want %q", got.Body, jsonSeq(tt.items...))
				}
			})
		}
	}
}

// Pre-encoded JSON items: stage brief, Scope, "each item encoded as its own
// body would be", and a []byte body is "sent as its bytes" (client.go,
// Input.Body).
func TestSequentialPreEncodedItems(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	items := [][]byte{[]byte(`{"pre":"encoded"}`), []byte(`[1, 2]`)}
	mustCall(t, c, "jsonl", &openapi.Input{Body: items}, nil)
	wantLines(t, w.last(t).Body, `{"pre":"encoded"}`, `[1, 2]`)
	mustCall(t, c, "seq", &openapi.Input{Body: seqOf(items...)}, nil)
	if got := w.last(t); string(got.Body) != jsonSeq(`{"pre":"encoded"}`, `[1, 2]`) {
		t.Errorf("json-seq body %q", got.Body)
	}
	// A whole body given as bytes is pre-encoded under a sequential type
	// too, as under any type (client.go, Input.Body).
	mustCall(t, c, "jsonl", &openapi.Input{Body: []byte("{}\n{}\n")}, nil)
	if got := w.last(t); string(got.Body) != "{}\n{}\n" {
		t.Errorf("pre-encoded body %q", got.Body)
	}
}

// An sseCase is a body and the events it must produce.
type sseCase struct {
	name    string
	body    any
	events  [][]sseField
	ordered bool // the item's fields follow member order (an object)
}

// Server-sent events: each item's fields, data as one data line per line,
// then a blank line, read back by the HTML standard's rules (sseEvents). An
// object's fields follow its members' order (doc.go, Fixed rules, Order: an
// object value's members "follow the order encoding/json writes members
// in"); an Event's fields are checked as a set, data lines in order. An
// Event writes only the fields it sets: Data when not nil, Event when not
// "", ID when IDSet, Retry when RetrySet, in whole milliseconds (stream.go,
// Event: "Retry is the retry field, in whole milliseconds"); retry is
// written in base ten digits alone (HTML standard, section 9.2.6: "If the
// field value consists of only ASCII digits").
func TestEventStreamBodies(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	f := func(kv ...string) []sseField {
		var fs []sseField
		for i := 0; i < len(kv); i += 2 {
			fs = append(fs, sseField{kv[i], kv[i+1]})
		}
		return fs
	}
	maps := []map[string]any{
		{"data": "a\nb", "event": "e", "id": "1", "retry": 1000},
		{"data": "only"},
		{"event": "ping"},
		{"id": "", "data": " lead"},
		{"retry": 0, "data": "x"},
	}
	mapEvents := [][]sseField{
		f("data", "a", "data", "b", "event", "e", "id", "1", "retry", "1000"),
		f("data", "only"),
		f("event", "ping"),
		f("data", " lead", "id", ""),
		f("data", "x", "retry", "0"),
	}
	events := []openapi.Event{
		{Data: []byte("x"), Event: "upd", ID: "7", IDSet: true, Retry: 1500 * time.Millisecond, RetrySet: true},
		{Data: []byte("y\nz")},
		{ID: "", IDSet: true, Data: []byte("reset")},
		{Data: []byte("w"), ID: "not set"},
		{Retry: 0, RetrySet: true, Data: []byte("now")},
	}
	eventEvents := [][]sseField{
		f("data", "x", "event", "upd", "id", "7", "retry", "1500"),
		f("data", "y", "data", "z"),
		f("data", "reset", "id", ""),
		f("data", "w"),
		f("data", "now", "retry", "0"),
	}
	type sseItem struct {
		Data  string `json:"data"`
		Event string `json:"event,omitempty"`
	}
	for _, tt := range []sseCase{
		{"slice of maps", maps, mapEvents, true},
		{"iter.Seq2 of maps", seq2Of(maps...), mapEvents, true},
		{"slice of Events", events, eventEvents, false},
		{"iter.Seq of Events", seqOf(events...), eventEvents, false},
		{"iter.Seq[any] of Events", seqOf[any](events[0], events[1]), eventEvents[:2], false},
		{"slice of any, mixed", []any{maps[0], events[1]}, [][]sseField{mapEvents[0], eventEvents[1]}, false},
		{"struct items", []sseItem{{Data: "d"}, {Data: "e", Event: "n"}}, [][]sseField{f("data", "d"), f("data", "e", "event", "n")}, true},
		{"empty", []openapi.Event{}, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, c, "sse", &openapi.Input{Body: tt.body}, nil)
			got := w.last(t)
			if ct := got.Header.Values("Content-Type"); !slices.Equal(ct, []string{"text/event-stream"}) {
				t.Errorf("Content-Type = %q", ct)
			}
			evs := sseEvents(t, string(got.Body))
			if len(evs) != len(tt.events) {
				t.Fatalf("%d events in %q, want %d", len(evs), got.Body, len(tt.events))
			}
			for i := range evs {
				g, want := evs[i], tt.events[i]
				if !tt.ordered {
					g, want = sortedFields(g), sortedFields(want)
				}
				if !slices.Equal(g, want) {
					t.Errorf("event %d: fields %q, want %q (body %q)", i, g, want, got.Body)
				}
			}
		})
	}
}

// sortedFields orders fields by name, keeping the data lines' own order.
func sortedFields(fs []sseField) []sseField {
	fs = slices.Clone(fs)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].name < fs[j].name })
	return fs
}

// unencodable is a channel, which encoding/json cannot encode.
var unencodable = make(chan int)

// Items that cannot be encoded, in a slice, which the client encodes when
// the call is prepared: refused before sending at the item's Inputs key,
// "Input.Body" followed by the JSON Pointer to it (errors.go,
// RequestError.Inputs; stage brief, Refusals: "a property or part value its
// media type cannot encode ... each at its Inputs key"). Event stream items
// as client.go, Input.Body, lists them; JSON items as encoding/json fails on
// them, or holding a reader or Part (client.go, Input.Body: "A Part or
// io.Reader inside a JSON value is refused with an Inputs entry at its place
// in Body").
func TestSequentialItemRefusals(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	ok := map[string]any{"data": "fine"}
	for _, tt := range []struct {
		name, key string
		body      any
		input     string
	}{
		{"data not a string", "sse", []any{ok, map[string]any{"data": 5}}, "Input.Body/1"},
		{"another member", "sse", []any{ok, map[string]any{"data": "x", "comment": "y"}}, "Input.Body/1"},
		{"LF in event", "sse", []any{ok, map[string]any{"event": "a\nb"}}, "Input.Body/1"},
		{"CR in event", "sse", []any{ok, map[string]any{"event": "a\rb"}}, "Input.Body/1"},
		{"LF in id", "sse", []any{ok, map[string]any{"id": "a\nb"}}, "Input.Body/1"},
		{"CR in id", "sse", []any{ok, map[string]any{"id": "a\rb"}}, "Input.Body/1"},
		{"negative retry", "sse", []any{ok, map[string]any{"retry": -1}}, "Input.Body/1"},
		{"fractional retry", "sse", []any{ok, map[string]any{"retry": 1.5}}, "Input.Body/1"},
		{"retry as a string", "sse", []any{ok, map[string]any{"retry": "1000"}}, "Input.Body/1"},
		{"event not a string", "sse", []any{ok, map[string]any{"event": true}}, "Input.Body/1"},
		{"a string item", "sse", []any{ok, "data: x"}, "Input.Body/1"},
		{"a number item", "sse", []any{ok, 5}, "Input.Body/1"},
		{"an array item", "sse", []any{ok, []string{"x"}}, "Input.Body/1"},
		{"a Part item", "sse", []any{ok, openapi.Part{Content: "x"}}, "Input.Body/1"},
		{"Event with LF in Event", "sse", []openapi.Event{{Data: []byte("x")}, {Event: "a\nb"}}, "Input.Body/1"},
		{"Event with CR in ID", "sse", []openapi.Event{{Data: []byte("x")}, {ID: "a\rb", IDSet: true}}, "Input.Body/1"},
		{"JSON: infinity", "jsonl", []any{1, math.Inf(1)}, "Input.Body/1"},
		{"JSON: a channel", "seq", []any{unencodable}, "Input.Body/0"},
		{"JSON: a reader inside", "jsonl", []any{map[string]any{"r": strings.NewReader("x")}}, "Input.Body/0/r"},
		{"JSON: a Part item", "jsonl", []any{1, 2, openapi.Part{Content: "x"}}, "Input.Body/2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := w.count()
			resp, err := c.Call(t.Context(), tt.key, &openapi.Input{Body: tt.body}, nil)
			re := refusedSince(t, w, before, resp, err)
			wantKeys(t, "Inputs", re.Inputs, true, tt.input)
		})
	}
}

// The items of a sequential type use the codec for their own type
// (client.go, Options.Codecs: Load refuses a codec key "that names a
// sequential or multipart type, whose framing stays the client's; their
// items and parts use the codec for their own type", and entries for
// "application/json" "replace encoding/json everywhere"). The codec
// receives each item as given (doc.go, Values); an Encode error refuses the
// call at the item's Inputs key.
func TestSequentialItemsUseCodecs(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), &openapi.Options{Codecs: map[string]openapi.Codec{"application/json": tagCodec{tag: "J"}}})
	mustCall(t, c, "jsonl", &openapi.Input{Body: []any{1, "x"}}, nil)
	wantLines(t, w.last(t).Body, "J(1)", "J(x)")
	mustCall(t, c, "seq", &openapi.Input{Body: seqOf(Pet{Name: "Rex"})}, nil)
	if got := w.last(t); string(got.Body) != jsonSeq("J({ Rex })") {
		t.Errorf("json-seq body %q", got.Body)
	}
	errEncode := errors.New("cannot encode")
	c = parseFor(t, w, seqDoc(), &openapi.Options{Codecs: map[string]openapi.Codec{"application/json": tagCodec{tag: "J", encodeErr: errEncode}}})
	before := w.count()
	resp, err := c.Call(t.Context(), "jsonl", &openapi.Input{Body: []int{1}}, nil)
	re := refusedSince(t, w, before, resp, err)
	wantKeys(t, "Inputs", re.Inputs, true, "Input.Body/0")
	if !errors.Is(err, errEncode) {
		t.Errorf("the refusal %v does not wrap the codec's error", err)
	}
}

// A slice body is a value the client encodes, so it can be sent again, with
// Content-Length; an iterator is read once and sent without (client.go,
// Input.Body: "every value the client encodes" can be sent again, "an
// iterator, is read once"; Request.HTTP: "GetBody is set when the body can
// be sent again"; doc.go, Fixed rules, Header fields).
func TestSequentialBodiesPrepared(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	req := mustPrepare(t, c, "jsonl", &openapi.Input{Body: []int{1, 2, 3}})
	body := preparedBody(t, req)
	wantLines(t, body, "1", "2", "3")
	if req.HTTP.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength %d for %d bytes", req.HTTP.ContentLength, len(body))
	}
	for range 2 {
		if _, err := req.Call(t.Context(), nil); err != nil {
			t.Fatalf("Request.Call: %v", err)
		}
		if got := w.last(t); string(got.Body) != string(body) || got.ContentLength != int64(len(body)) {
			t.Errorf("sent %q with Content-Length %d", got.Body, got.ContentLength)
		}
	}

	called := 0 // written by the iterator; read after Call, which waits for it (checked by -race)
	it := func(yield func(int) bool) {
		called++
		for i := range 3 {
			if !yield(i) {
				return
			}
		}
	}
	req = mustPrepare(t, c, "jsonl", &openapi.Input{Body: iter.Seq[int](it)})
	if called != 0 {
		t.Errorf("Prepare ran the iterator %d times; it never reads one (client.go, Prepare)", called)
	}
	if req.HTTP.GetBody != nil {
		t.Errorf("GetBody set for an iterator, which is read once")
	}
	before := w.count()
	if _, err := req.Call(t.Context(), nil); err != nil {
		t.Fatalf("Request.Call: %v", err)
	}
	got := w.last(t)
	wantLines(t, got.Body, "0", "1", "2")
	if !slices.Contains(got.TransferEncoding, "chunked") {
		t.Errorf("an iterator body sent with Transfer-Encoding %q, Content-Length %d", got.TransferEncoding, got.ContentLength)
	}
	resp, err := req.Call(t.Context(), nil)
	refusedSince(t, w, before+1, resp, err)
	if called != 1 {
		t.Errorf("the iterator ran %d times, want once", called)
	}
}

// A missing required body is refused at Inputs["Input.Body"] (client.go,
// Input.Body: "A nil Body where the request body is required is refused at
// Inputs["Input.Body"]"); an empty slice is a body of no items.
func TestSequentialRequiredBody(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	resp, err := c.Call(t.Context(), "importPets", &openapi.Input{}, nil)
	re := refusedBeforeSending(t, w, resp, err)
	wantKeys(t, "Inputs", re.Inputs, true, "Input.Body")
	mustCall(t, c, "importPets", &openapi.Input{Body: []Pet{}}, nil)
	if got := w.last(t); len(got.Body) != 0 || got.Header.Get("Content-Type") != "application/jsonl" {
		t.Errorf("an empty slice sent %q as %q", got.Body, got.Header.Get("Content-Type"))
	}
}

// manyPets returns n pets, for the scaling tests and benchmarks.
func manyPets(n int) []Pet {
	ps := make([]Pet, n)
	for i := range ps {
		ps[i] = Pet{ID: fmt.Sprint("p-", i), Name: "Rex", Tag: "dog"}
	}
	return ps
}
