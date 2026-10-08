package openapi_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openapi-client/go/openapi"
)

// stream.go Events/Event; HTML §§9.2.5–9.2.6 and Events' dispatch of "a block
// without data, which a browser's EventSource does not dispatch". Only fields
// set in this block appear; field-only blocks
// dispatch, comment/unknown/invalid-only blocks do not, and EOF drops an
// unfinished block. UTF-8 replacement and a leading BOM follow HTML.
func TestEventsFieldParsing(t *testing.T) {
	wire := "\ufeff: comment\r\nunknown: ignore\r\n\r\n" +
		"event: first\revent: changed\rid: old\rid: fresh\rretry: 0007\rdata: a\rdata:  b:c\r\r" +
		"data\n\n" +
		"id:\n\n" +
		"retry: 0\n\n" +
		"event:\n\n" +
		"id: keep\nid: bad\x00id\nretry: 5\nretry: +8\nretry: １２\nretry: -1\nretry:\ndata: z\n\n" +
		"Data: uppercase ignored\nid: bad\x00\nretry: 1.5\n\n" +
		"data: \xff\n\n" +
		"data: unfinished\n"
	want := []openapi.Event{{Data: []byte("a\n b:c"), Event: "changed", ID: "fresh", IDSet: true, Retry: 7 * time.Millisecond, RetrySet: true}, {Data: []byte{}}, {IDSet: true}, {RetrySet: true}, {Event: ""}, {Data: []byte("z"), ID: "keep", IDSet: true, Retry: 5 * time.Millisecond, RetrySet: true}, {Data: []byte("\ufffd")}}
	for _, chunks := range [][]int{nil, {1}, {3, 1, 4, 2}} {
		r, b := streamedResponse(t, "text/event-stream; charset=utf-8", wire, nil, chunks...)
		var got []openapi.Event
		for e, err := range openapi.Events(r) {
			if err != nil {
				t.Fatal(err)
			}
			if e.Data != nil {
				e.Data = append([]byte{}, e.Data...)
			}
			got = append(got, e)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chunks %v:\ngot %#v\nwant %#v", chunks, got, want)
		}
		if b.closed.Load() == 0 {
			t.Fatal("Body not closed")
		}
	}
}

// OAS 3.2.1 §4.14.4 models data as a string, even when the field contains
// JSON. It models retry as a number. Event/ID/retry presence is per block;
// an explicit empty event/id is retained in Items' JSON object.
func TestItemsSSEEventObjects(t *testing.T) {
	wire := "data: {\"n\":1}\nevent: update\nid: a\nretry: 5\n\ndata: next\n\nevent:\nid:\n\n"
	r, _ := streamedResponse(t, "text/event-stream", wire, nil, 1)
	got, errs := streamCollect[map[string]any](r)
	streamNoErrors(t, errs)
	want := []map[string]any{{"data": `{"n":1}`, "event": "update", "id": "a", "retry": json.Number("5")}, {"data": "next"}, {"event": "", "id": ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

// The Event API stores milliseconds in time.Duration. A valid but
// unrepresentable retry value is an ErrItem for Events (stream.go, Events:
// "A valid retry integer that cannot fit in Event.Retry yields an ErrItem"),
// while the JSON representation retains the exact arbitrary-size integer.
func TestEventsRetryOverflow(t *testing.T) {
	const huge = "99999999999999999999999999999999999999999999999"
	wire := "retry: 9223372036854\n\nretry: 9223372036855\n\nretry: " + huge + "\n\ndata: after\n\n"
	r, _ := streamedResponse(t, "text/event-stream", wire, nil, 1)
	var got []openapi.Event
	var errs []error
	for v, e := range openapi.Events(r) {
		got = append(got, v)
		errs = append(errs, e)
	}
	if len(got) != 4 || errs[0] != nil || got[0].Retry != 9223372036854*time.Millisecond || !errors.Is(errs[1], openapi.ErrItem) || !errors.Is(errs[2], openapi.ErrItem) || errs[3] != nil || string(got[3].Data) != "after" {
		t.Fatalf("events %#v errors %v", got, errs)
	}
	r, _ = streamedResponse(t, "text/event-stream", "retry: "+huge+"\n\n", nil)
	objs, es := streamCollect[map[string]any](r)
	streamNoErrors(t, es)
	if len(objs) != 1 || objs[0]["retry"] != json.Number(huge) {
		t.Fatalf("objects %#v", objs)
	}
	var array []any
	if _, err := cCall7(t, "text/event-stream", "retry: "+huge+"\n\n", &array); err != nil || len(array) != 1 || array[0].(map[string]any)["retry"] != json.Number(huge) {
		t.Fatalf("array %#v error %v", array, err)
	}
}

// Events must reject another media type rather than treating arbitrary
// text as SSE, and break closes Body just as Items does (stream.go).
func TestEventsWrongTypeAndBreakClose(t *testing.T) {
	r, b := streamedResponse(t, "application/jsonl", "1\n", nil)
	var errs []error
	for _, err := range openapi.Events(r) {
		errs = append(errs, err)
	}
	if len(errs) != 1 || errs[0] == nil {
		t.Fatalf("errors %v", errs)
	}
	if b.closed.Load() == 0 {
		t.Fatal("wrong-type iteration did not close")
	}
	r, b = streamedResponse(t, "text/event-stream", "data: first\n\ndata: later\n\n", nil, 1)
	for e, err := range openapi.Events(r) {
		if err != nil || string(e.Data) != "first" {
			t.Fatalf("%v %v", e, err)
		}
		break
	}
	if b.closed.Load() == 0 {
		t.Fatal("break did not close")
	}
	for _, err := range openapi.Events(r) {
		if err == nil {
			t.Fatal("repeat succeeded")
		}
	}
}

// HTML does not case-fold field names or strip more than one leading space.
// Repeated data lines are appended with LF, not accumulated quadratically.
func TestEventsLongDataAndFieldReset(t *testing.T) {
	wire := strings.Repeat("data: x\n", 2000) + "\n" + "data:\n\n" + "id: a\n\nid:\n\ndata: b\n\n"
	r, _ := streamedResponse(t, "text/event-stream", wire, nil, 1)
	var got []openapi.Event
	for e, err := range openapi.Events(r) {
		if err != nil {
			t.Fatal(err)
		}
		if e.Data != nil {
			e.Data = append([]byte{}, e.Data...)
		}
		got = append(got, e)
	}
	if len(got) != 5 || string(got[0].Data) != strings.TrimSuffix(strings.Repeat("x\n", 2000), "\n") || got[1].Data == nil || len(got[1].Data) != 0 || !got[3].IDSet || got[3].ID != "" || got[4].IDSet {
		t.Fatalf("got %d events: %#v", len(got), got)
	}
}

// HTML field processing replaces only on valid retry values, normalizes
// leading zeros for JSON, and applies Event's duration range only after
// the block's last effective field value is known.
func TestEventsRetryLastValidValue(t *testing.T) {
	wire := "retry: 999999999999999999999999999999\nretry: 0007\nretry: nope\n\n"
	r, _ := streamedResponse(t, "text/event-stream", wire, nil, 1)
	got, errs := streamCollect[map[string]any](r)
	streamNoErrors(t, errs)
	if len(got) != 1 || got[0]["retry"] != json.Number("7") {
		t.Fatalf("objects %#v", got)
	}
	r, _ = streamedResponse(t, "text/event-stream", wire, nil, 1)
	n := 0
	for e, err := range openapi.Events(r) {
		if err != nil || !e.RetrySet || e.Retry != 7*time.Millisecond {
			t.Fatalf("event %#v error %v", e, err)
		}
		n++
	}
	if n != 1 {
		t.Fatal(n)
	}
}
