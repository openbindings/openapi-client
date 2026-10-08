package openapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Sequential bodies, in any edition. client.go, Input.Body: "For a
// sequential media type (JSON Lines, JSON text sequences, server-sent
// events), in any edition, Body is a list, an iter.Seq, or an iter.Seq2
// whose second value is an error, of any element type; each element is one
// item ... Under text/event-stream an item is an object with no members but
// data, event and id, as strings, and retry, as a non-negative integer, or
// an [Event] (or a non-nil *Event), of which only the fields it sets are
// used. It is written as those fields, each as a "field: value" line ending
// in LF, data as one data line per line (split at CRLF, LF or CR), then a
// blank line; any other member or type, a line break in event or id, a NUL
// in id, or a retry that is not whole milliseconds, is an item that cannot
// be encoded; invalid UTF-8 is written as U+FFFD, as an event stream is
// UTF-8. Under JSON Lines and JSON text sequences, a []byte or io.Reader
// item is the item's JSON text, written as given and framed (a JSON Lines
// item is followed by LF; a sequence item has RS before it and LF after);
// one holding the framing's separator (LF or CR, or RS) cannot be encoded,
// and a codec's output is framed after its trailing JSON whitespace is
// trimmed." Each item is "encoded as that value on its own would be"
// (Input.Body). The framing is each authority's: JSON Lines ("Each Line is a
// Valid JSON Value"; "Line Separator is '\n'"), RFC 7464 section 2.2
// ("JSON-sequence = *(RS JSON-text LF)"), and the HTML standard's event
// stream (section 9.2.5). Every JSON Lines item is followed by LF, the last
// included.

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
// json.Marshal writes for each: "as encoding/json writes" (doc.go, Values)
// means json.Marshal, HTML escaping included.
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
// events"; stream.go, Items: "application/json-seq and any +json-seq type:
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

// Pre-encoded items (client.go, Input.Body: under JSON Lines and JSON text
// sequences "a []byte or io.Reader item is the item's JSON text, written as
// given and framed"), from a slice and from iterators; a sequence item may
// hold LF, which only JSON Lines uses as its separator. A whole body given
// as bytes is pre-encoded under a sequential type too, as under any type.
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
	readers := func() []io.Reader { return []io.Reader{strings.NewReader(`{"r":1}`), newOnce(`"once"`)} }
	mustCall(t, c, "ndjson", &openapi.Input{Body: readers()}, nil)
	wantLines(t, w.last(t).Body, `{"r":1}`, `"once"`)
	mustCall(t, c, "geoseq", &openapi.Input{Body: seqOf(readers()...)}, nil)
	if got := w.last(t); string(got.Body) != jsonSeq(`{"r":1}`, `"once"`) {
		t.Errorf("json-seq body %q", got.Body)
	}
	mustCall(t, c, "jsonl", &openapi.Input{Body: []any{[]byte(`1`), Pet{Name: "Rex"}, strings.NewReader(`[]`)}}, nil)
	wantLines(t, w.last(t).Body, `1`, `{"name":"Rex"}`, `[]`)
	mustCall(t, c, "seq", &openapi.Input{Body: [][]byte{[]byte("[1,\n2]")}}, nil)
	if got := w.last(t); string(got.Body) != jsonSeq("[1,\n2]") {
		t.Errorf("json-seq body %q, want an item holding LF framed", got.Body)
	}
	mustCall(t, c, "jsonl", &openapi.Input{Body: []byte("{}\n{}\n")}, nil)
	if got := w.last(t); string(got.Body) != "{}\n{}\n" {
		t.Errorf("pre-encoded body %q", got.Body)
	}
}

// A reader item that holds the framing's separator ends the body as an
// upload error when the separator is found, since reader content, replayable
// or not, is checked as it streams (client.go, Input.Body: "an item that
// cannot be encoded ... aborts the body and is reported by Call or
// Response.WaitRequest"): the server never receives a complete body, and
// Call's error is not a *RequestError. Every kind of reader, replayable or
// not.
func TestSequentialReaderItemHoldingSeparator(t *testing.T) {
	for _, tt := range []struct {
		key  string
		item string
	}{
		{"jsonl", "{}\n{}"},
		{"ndjson", "1\n"},
		{"seq", "\x1e1"},
		{"geoseq", "[1,\x1e2]"},
	} {
		for _, r := range readerKinds(t, tt.item) {
			t.Run(tt.key+" "+r.name, func(t *testing.T) {
				srv := newBodyServer(t, false, "")
				c := parseAt(t, seqDoc(), srv.URL, srv.URL+"/openapi.json", nil)
				ctx, _ := gateCtx(t)
				res := awaitCall(t, callAsync(ctx, c, tt.key, &openapi.Input{Body: []io.Reader{strings.NewReader("{}"), r.reader}}), "Call")
				if res.err == nil || isRequestError(res.err) {
					t.Fatalf("Call = %v, want an upload error, not a *RequestError", res.err)
				}
				if _, err := srv.finished(t); err == nil {
					t.Errorf("the server read a complete body")
				}
			})
		}
	}
}

// An sseCase is a body and the event stream it must be: each event's lines.
type sseCase struct {
	name   string
	body   any
	events [][]string
}

// Server-sent events, byte for byte: each field a "field: value" line with
// one space, ending in LF, then a blank line (client.go, Input.Body), always
// one space, so a value starting with a space survives the parser's removal
// of one (HTML standard, section 9.2.6: "If value starts with a U+0020 SPACE
// character, remove it from value"); data split at CRLF, LF and CR, the
// standard's line endings, one data line each (HTML standard, section 9.2.5:
// "end-of-line = ( cr lf / cr / lf )"), so "a\n" is a data line "a" and an
// empty one, which the standard's parser reads back as "a\n"; data ""
// written as "data: "; an item that sets no field a blank line alone. An
// object's fields follow its members' order as encoding/json orders them
// (doc.go, Fixed rules, Order); an Event's follow its field order, the data
// lines, then event, id and retry. An Event writes only the fields it sets:
// Data when not nil, Event when not "", ID when IDSet, Retry when RetrySet,
// in whole milliseconds (stream.go, Event: "Retry is the retry field, in
// whole milliseconds"), in base ten digits (HTML standard, section 9.2.6:
// "If the field value consists of only ASCII digits").
func TestEventStreamBodies(t *testing.T) {
	w := newWire(t, nil)
	c := parseFor(t, w, seqDoc(), nil)
	maps := []map[string]any{
		{"data": "a\nb", "event": "e", "id": "1", "retry": 1000},
		{"data": "only"},
		{"event": "ping"},
		{"id": "", "data": " lead"},
		{"retry": 0, "data": "x"},
		{"data": "a\r\nb\rc\nd"},
		{"data": ""},
		{"data": "a\n"},
		{},
	}
	mapEvents := [][]string{
		{"data: a", "data: b", "event: e", "id: 1", "retry: 1000"},
		{"data: only"},
		{"event: ping"},
		{"data:  lead", "id: "},
		{"data: x", "retry: 0"},
		{"data: a", "data: b", "data: c", "data: d"},
		{"data: "},
		{"data: a", "data: "},
		{},
	}
	events := []openapi.Event{
		{Data: []byte("x"), Event: "upd", ID: "7", IDSet: true, Retry: 1500 * time.Millisecond, RetrySet: true},
		{Data: []byte("y\nz")},
		{ID: "", IDSet: true, Data: []byte("reset")},
		{Data: []byte("w"), ID: "not set"},
		{Retry: 0, RetrySet: true, Data: []byte("now")},
		{Data: []byte{}},
		{},
		{Data: []byte("p\r\nq\rr")},
	}
	eventEvents := [][]string{
		{"data: x", "event: upd", "id: 7", "retry: 1500"},
		{"data: y", "data: z"},
		{"data: reset", "id: "},
		{"data: w"},
		{"data: now", "retry: 0"},
		{"data: "},
		{},
		{"data: p", "data: q", "data: r"},
	}
	type sseItem struct {
		Event string `json:"event,omitempty"`
		Data  string `json:"data"`
	}
	// An Event in its field order whatever order the literal names them in.
	events = append(events, openapi.Event{RetrySet: true, Retry: time.Second, IDSet: true, ID: "9", Event: "late", Data: []byte("first\nsecond")})
	eventEvents = append(eventEvents, []string{"data: first", "data: second", "event: late", "id: 9", "retry: 1000"})
	for _, tt := range []sseCase{
		{"slice of maps", maps, mapEvents},
		{"iter.Seq2 of maps", seq2Of(maps...), mapEvents},
		{"slice of Events", events, eventEvents},
		{"iter.Seq of Events", seqOf(events...), eventEvents},
		{"iter.Seq[any] of Events", seqOf[any](events[0], events[1]), eventEvents[:2]},
		{"slice of any, mixed", []any{maps[0], events[1]}, [][]string{mapEvents[0], eventEvents[1]}},
		{"struct items in field order", []sseItem{{Data: "d"}, {Data: "e", Event: "n"}}, [][]string{{"data: d"}, {"event: n", "data: e"}}},
		{"empty", []openapi.Event{}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mustCall(t, c, "sse", &openapi.Input{Body: tt.body}, nil)
			got := w.last(t)
			if ct := got.Header.Values("Content-Type"); !slices.Equal(ct, []string{"text/event-stream"}) {
				t.Errorf("Content-Type = %q", ct)
			}
			if want := eventStream(tt.events); string(got.Body) != want {
				t.Errorf("body %q, want %q", got.Body, want)
			}
		})
	}
}

// eventStream is the event stream of events, each the lines it writes: each
// line then LF, each event then a blank line (client.go, Input.Body).
func eventStream(events [][]string) string {
	var b strings.Builder
	for _, ev := range events {
		for _, line := range ev {
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// unencodable is a channel, which encoding/json cannot encode.
var unencodable = make(chan int)

// Items that cannot be encoded, in a slice, which the client encodes when the
// call is prepared: refused before sending at the item's Inputs key,
// "Input.Body" followed by the JSON Pointer to it (errors.go,
// RequestError.Inputs: "The key is the Param.Key or, for the body, "Input.Body"
// followed by a JSON Pointer to the part of Body concerned"). Event stream
// items as client.go, Input.Body, lists them ("any other member or type, a line
// break in event or id, a NUL in id, or a retry that is not whole
// milliseconds"), a negative Retry too (retry is "a non-negative integer"), and
// under text/event-stream an item is only an object or an Event; JSON items as
// encoding/json fails on them, or holding a reader or Part (client.go,
// Input.Body: "A Part or io.Reader inside a JSON value is refused with an
// Inputs entry at its place in Body"); a pre-encoded item holding its framing's
// separator (Input.Body: "one holding the framing's separator (LF or CR, or RS)
// cannot be encoded").
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
		{"data an object", "sse", []any{ok, map[string]any{"data": map[string]int{"a": 1}}}, "Input.Body/1"},
		{"id an array", "sse", []any{ok, map[string]any{"id": []string{"a"}}}, "Input.Body/1"},
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
		{"Event with a Retry not in whole milliseconds", "sse", []openapi.Event{{Data: []byte("x")}, {Retry: 1500 * time.Microsecond, RetrySet: true}}, "Input.Body/1"},
		{"Event with a negative Retry", "sse", []openapi.Event{{Data: []byte("x")}, {Retry: -time.Second, RetrySet: true}}, "Input.Body/1"},
		{"a []byte item", "sse", []any{ok, []byte("data: x\n\n")}, "Input.Body/1"},
		{"a reader item", "sse", []any{ok, strings.NewReader("data: x\n\n")}, "Input.Body/1"},
		{"JSON Lines: a []byte item holding LF", "jsonl", []any{[]byte("{}\n{}")}, "Input.Body/0"},
		{"JSON Lines: a []byte item ending in LF", "ndjson", [][]byte{[]byte("1"), []byte("2\n")}, "Input.Body/1"},
		{"sequence: a []byte item holding RS", "seq", []any{[]byte("1"), []byte("\x1e2")}, "Input.Body/1"},
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
// sequential, multipart or application/x-www-form-urlencoded type, whose
// framing and field encoding stay the client's, as OpenAPI's Encoding Object
// governs them; their items and parts use the codec for their own type", and
// entries for "application/json" "replace encoding/json wherever a value is
// encoded or decoded as content of a JSON type"). The
// codec receives each item as given (doc.go, Values); an Encode error
// refuses the call at the item's Inputs key.
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
// Input.Body: "every value the client encodes" can be sent again, and "Any
// other reader ... is read once, and so is an iterator"; Request.HTTP: "GetBody
// is set when the body can be sent again"; doc.go, Fixed rules, Header fields).
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
