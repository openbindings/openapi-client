package openapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Stream sends the operation named key with in, as Call does, but returns
// as soon as a 2xx response's headers arrive, leaving its Body open to be
// read as it arrives: with [Items] or [Events], or directly for the caller's
// own framing. Any other final status is a *StatusError, its body read as
// Call reads it. The returned Response still exposes WaitRequest if its
// upload was outstanding when that status arrived.
//
// A server may answer before it has read the whole request body, so the
// request body goes on being sent after Stream returns, until it ends, the
// response Body is closed, or ctx ends. A late request-body failure is
// observable through Response.WaitRequest independently of response reads;
// an EOF on the response does not imply that the upload completed. The
// client never presents an incomplete upload as complete.
//
// The caller must close Body. Closing it signals an outstanding upload to stop.
// An iterator that honors its yield result then stops, but a caller's arbitrary
// io.Reader may remain blocked in Read; use a source that responds to
// cancellation for long-running uploads. ctx bounds the whole stream:
// cancelling it before the headers arrive is an error from Stream, and after
// them it is observable from WaitRequest and from an affected response read.
func (c *Client) Stream(ctx context.Context, key string, in *Input) (*Response, error) {
	o, err := c.operation(key)
	if err != nil {
		return nil, err
	}
	x := &exchange{Context: ctx, cfg: c.cfg, op: o}
	re := RequestError{Err: o.Err}
	req, p, _, sec := c.newRequest(x, o, in, &re)
	if err := re.refused(); err != nil {
		return nil, err
	}
	x.selection = sec
	x.attach(req, p)
	return x.streamResponse(req, true)
}

// Stream sends r with ctx, adding credentials, and returns as
// [Client.Stream] does.
func (r *Request) Stream(ctx context.Context) (*Response, error) {
	x, req, err := r.newExchange(ctx)
	if err != nil {
		return nil, err
	}
	return x.streamResponse(req, true)
}

// ErrItem is wrapped by an error that concerns one item alone: the item was
// malformed, or did not decode into the type asked for. Items yields such
// an error in the item's place and goes on; any other error ends the
// iteration.
var ErrItem = errors.New("openapi: bad item")

// Items returns the items of r's open body as they arrive, each decoded
// into a new T, framed by the response's media type:
//
//   - application/jsonl and application/x-ndjson: one JSON value per line;
//     blank lines are skipped.
//   - application/json-seq and any +json-seq type: one per RFC 7464 record.
//     A record holding a top-level number, true, false or null not followed
//     by whitespace, which may be truncated, is dropped and reported: an
//     ErrItem here, a *DecodeError from Call.
//   - text/event-stream: one per dispatched event, as the JSON object
//     OpenAPI 3.2 defines for it, whose members are only the fields the
//     event set: "data", "event" and "id" as strings, "retry" as a number.
//     T is the type the operation's itemSchema describes; to decode each
//     event's data instead, use [Events]. Dispatch and field presence are
//     as for Events, but retry is a JSON integer without Event.Retry's
//     time.Duration range restriction.
//   - multipart types: one per part, decoded by the part's own
//     Content-Type as Call decodes a body, text/plain where it has none
//     (message/rfc822 in multipart/digest), after any base64 or
//     quoted-printable Content-Transfer-Encoding is removed. With T =
//     *multipart.Part, each item is the part as mime/multipart's NextPart
//     returns it, its body read as it arrives and valid until the next
//     iteration. Nested multipart bodies are decoded as Call decodes them,
//     without flattening their parts.
//   - any other media type: the whole body, as one item.
//
// The media type is the response's Content-Type, read as Call reads it, so
// a T of any receives a body without one as one []byte.
// An empty body yields no items. Items decode as Call decodes, so with the
// client's own JSON codec a T of any keeps numbers exact. A T of []byte
// bypasses value decoding and receives the framed item's bytes; for SSE,
// these are the event object's JSON representation. The JSON-sequence
// scalar-truncation rule still applies. An error that concerns one item
// wraps [ErrItem] and is yielded in its place, and the iteration goes on.
// Any other error
// (a read failure, the context's error, an item over Options.MaxItemBytes)
// is yielded last. Items yielded before an error stand. When the loop ends,
// by break or otherwise, Body is closed.
// A non-EOF read error discards an unfinished item, but completed items
// read with that error are yielded first. Ordinary EOF may finish a JSON
// line or JSON-sequence record; SSE dispatch requires an empty line.
//
// r must come from Stream or Request.Send, and may be iterated once; for
// any other Response, Items yields one error.
func Items[T any](r *Response) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		x, err := claimResponse(r)
		if err != nil {
			yield(zero, err)
			return
		}
		defer r.Body.Close()
		if bodiless(r.Response) {
			return
		}
		_, raw := any(&zero).(*[]byte)
		_, part := any(&zero).(**multipart.Part)
		if !raw && !part {
			if err := contentCoding(r.Header); err != nil {
				yield(zero, err)
				return
			}
		}
		ct := mediaOf(r.Header)
		bound := limit(x.cfg.MaxItemBytes, 16<<20)
		switch {
		case strings.EqualFold(ct.typ, "multipart"):
			multipartItems(x, r, ct, bound, yield)
		case ct.class() == sequentialClass:
			f := newSequenceReader(r.Body, sequenceOf(ct), bound)
			for {
				data, err := f.next()
				if err == io.EOF {
					return
				}
				var value T
				if err == nil {
					err = decodeItem(x.cfg, f.sq.item, data, &value)
				}
				if !yield(value, withContext(x, err)) || err != nil && !errors.Is(err, ErrItem) {
					return
				}
			}
		default:
			data, err := readAll(nil, r.Body, -1, bound)
			if err == nil && len(data) == 0 {
				return
			}
			if err == nil {
				err = decodeItem(x.cfg, ct, data, &zero)
			}
			yield(zero, withContext(x, err))
		}
	}
}

// Events returns the server-sent events of r's open text/event-stream body as
// they arrive, one per dispatched event, parsed as the HTML standard says.
// Its non-browser dispatch rule yields each empty-line-terminated block
// that sets at least one valid data, event, id or retry field, including a
// block without data. Fields belong to that block alone, without inheriting
// an earlier block's ID or retry. Blocks that set no valid field are skipped;
// EOF discards an unfinished block. A valid retry integer that cannot fit in
// Event.Retry yields an ErrItem, and iteration continues with the next block.
// A body of another media type, or a Response not from Stream or
// Request.Send, yields one error. Errors, and
// closing Body, are as for [Items]. The client never reconnects; to resume,
// call again with a Last-Event-ID field in Input.Header.
func Events(r *Response) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		x, err := claimResponse(r)
		if err != nil {
			yield(Event{}, err)
			return
		}
		defer r.Body.Close()
		ct := mediaOf(r.Header)
		if !strings.EqualFold(ct.typ, "text") || !strings.EqualFold(ct.sub, "event-stream") {
			yield(Event{}, errors.New("openapi: Events requires text/event-stream"))
			return
		}
		if err := contentCoding(r.Header); err != nil {
			yield(Event{}, err)
			return
		}
		if bodiless(r.Response) {
			return
		}
		f := newSequenceReader(r.Body, sequenceOf(ct), limit(x.cfg.MaxItemBytes, 16<<20))
		for {
			e, err := f.event()
			if err == io.EOF {
				return
			}
			var value Event
			if err == nil {
				value, err = e.value()
			}
			if !yield(value, withContext(x, err)) || err != nil && !errors.Is(err, ErrItem) {
				return
			}
		}
	}
}

// An Event is one server-sent event, with the fields it set.
type Event struct {
	// Data holds the event's data lines joined by "\n". It is nil when the
	// event set no data field. It is valid only until the next iteration,
	// as bufio.Scanner.Bytes is; copy it to keep it.
	Data []byte

	// Event is the event field: the event's type, where "" means
	// "message".
	Event string

	// ID is the id field, and IDSet whether the event set one: an empty ID
	// with IDSet resets the last event ID.
	ID    string
	IDSet bool

	// Retry is the retry field, in whole milliseconds, and RetrySet whether
	// the event set one: a zero Retry with RetrySet asks for no delay.
	Retry    time.Duration
	RetrySet bool
}

// decodeItem gives raw targets their own bytes; every value failure is local
// to this item, while framing and read failures remain terminal.
func decodeItem(cfg *config, ct parsedMedia, data []byte, out any) error {
	if p, ok := out.(*[]byte); ok {
		*p = append([]byte{}, data...)
		return nil
	}
	if err := cfg.decodeData(ct, nil, data, out); err != nil {
		return badItem(err)
	}
	return nil
}

type itemError struct{ err error }

func (e *itemError) Error() string        { return ErrItem.Error() + ": " + e.err.Error() }
func (e *itemError) Unwrap() error        { return e.err }
func (e *itemError) Is(target error) bool { return target == ErrItem }

func badItem(err error) error { return &itemError{err} }

func contentCoding(h http.Header) error {
	if coding := h["Content-Encoding"]; len(coding) > 0 && coding[0] != "" && !strings.EqualFold(coding[0], "identity") {
		return fmt.Errorf("cannot decode a body with Content-Encoding %q", coding[0])
	}
	return nil
}
