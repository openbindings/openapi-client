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
// cancelling it before the headers arrive is an error from Stream when the
// transport honors the request's context, as net/http's does, and after them
// it is observable from WaitRequest and from an affected response read.
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
// malformed, or did not decode into the type asked for. Items yields such an
// error in the item's place and goes on, unless the call's context has ended
// (see Items); any other error ends the iteration.
var ErrItem = errors.New("openapi: bad item")

// Items returns the items of r's open body as they arrive, each decoded
// into a new T, framed by the response's media type:
//
//   - application/jsonl and application/x-ndjson: one JSON value per line;
//     blank lines are skipped.
//   - application/json-seq and any +json-seq type: one per RFC 7464 record,
//     which is one JSON text and ends at the first LF after that text, or else
//     at the next RS; a []byte item is the record's bytes after its RS.
//     Whitespace after the LF is skipped. Any other text before the next RS,
//     and any text at all before the first RS, is a malformed record of its
//     own, as is a record holding a top-level number, true, false or null not
//     followed by whitespace, which may be truncated. A malformed record is
//     dropped and reported, an ErrItem here and a *DecodeError from Call, and
//     reading goes on at the next RS. Under a text/* type whose items have
//     no caller's codec, a T of any or string receives each record's bytes
//     as text, as Call takes a text/* body, so a record that is not a JSON
//     text is yielded, not reported; a malformed record is still dropped
//     and reported.
//   - text/event-stream: one per dispatched event, as the JSON object
//     OpenAPI 3.2 defines for it, whose members are only the fields the
//     event set: "data", "event" and "id" as strings, "retry" as a number.
//     T is the type the operation's itemSchema describes; to decode each
//     event's data instead, use [Events]. Dispatch, decoding and field presence
//     are as for Events, but retry is a JSON integer without Event.Retry's
//     time.Duration range restriction.
//   - multipart types: one per part, decoded by the part's own Content-Type as
//     Call decodes a body, text/plain where it has none (message/rfc822 in
//     multipart/digest), after any base64 or quoted-printable
//     Content-Transfer-Encoding is removed; base64 is read with its padding
//     required, ignoring only spaces, tabs, CR and LF, so any other character
//     outside its alphabet, or missing, incomplete or misplaced padding, makes
//     the part an ErrItem. An unknown transfer encoding makes the effective
//     type application/octet-stream and leaves its bytes encoded, as RFC 2045
//     requires. With T = *multipart.Part, each item is the part as
//     mime/multipart's NextPart returns it, its body read as it arrives and
//     valid until the next iteration. Nested multipart bodies are decoded as
//     Call decodes them, without flattening their parts. The body is framed
//     as RFC 2046 says, except for line breaks. A header line may end in LF
//     alone. If the first delimiter line ends in LF alone, LF alone replaces
//     CRLF before and after each delimiter. Otherwise the close delimiter's
//     line may still end in LF alone. A body that cannot be framed, as with
//     no delimiter, a malformed part header, or an end before the close
//     delimiter, ends the iteration with an error that is not an ErrItem,
//     wrapping mime/multipart's where it gives one.
//   - any other media type: the whole body, as one item.
//
// The media type is the response's Content-Type, read as Call reads it, so a T
// of any receives a body without one as one []byte. An empty body yields no
// items. Items decode as Call decodes, so with the client's own JSON codec a T
// of any keeps numbers exact. A T of []byte bypasses value decoding and
// receives the framed item's bytes; for SSE, these are the event object's JSON
// representation. The JSON-sequence scalar-truncation rule still applies. A T
// of *multipart.Part reads only a multipart body: under any other media type,
// no item decodes into it, so each is an ErrItem. An error that concerns one
// item wraps [ErrItem] and is yielded in its place, and the iteration goes on.
// Any other error (a read failure, the context's error, an item over
// Options.MaxItemBytes) is yielded last, except that a multipart body ends at
// the line break that ends its close delimiter's line, so a read failure that
// comes with or after that line break, or the context's ending after it, is
// not reported. Items yielded before an error stand. Once the call's context
// has ended, the next read of Body fails with the context's error, and an
// error that concerns one item is joined with it; either ends the iteration,
// though items whose bytes were already read may still be yielded first. When
// the loop ends, by break or otherwise, the client closes Body and signals any
// outstanding upload to stop, even for an unchanged upgrade or tunnel body,
// whose own Close does not. Body is not read again after a non-EOF read error,
// which discards an unfinished item, but completed items read with that error
// are yielded first. Ordinary EOF may finish a JSON line or JSON-sequence
// record; SSE dispatch requires an empty line.
//
// r must come from Stream or Request.Send, and may be iterated once, by Items
// or Events, through r or any copy of it; any later iteration, and one of any
// other Response, yields one error and no item.
func Items[T any](r *Response) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		x, err := claimResponse(r)
		if err != nil {
			yield(zero, err)
			return
		}
		defer closeStream(x, r)
		if bodiless(r.Response) {
			return
		}
		_, raw := any(&zero).(*[]byte)
		_, part := any(&zero).(**multipart.Part)
		if !raw && !part {
			if x.cfg.codecsErr != nil { // as Response.Decode reports it, the body left unread
				yield(zero, invalidDecodeError(r, fmt.Errorf("Options.Codecs: %w", x.cfg.codecsErr)))
				return
			}
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
				terminal := false
				if err != nil {
					err = withContext(x, err)
					terminal = itemTerminal(x, err)
				}
				if !yield(value, err) || terminal {
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
// they arrive, one per dispatched event, parsed as the HTML standard says. It
// dispatches each empty-line-terminated block that sets at least one valid
// data, event, id or retry field, including a block without data, which a
// browser's EventSource does not dispatch. Fields belong to that block alone,
// without inheriting an earlier block's ID or retry. Blocks that set no valid
// field are skipped; EOF discards an unfinished block. A valid retry integer
// that cannot fit in Event.Retry yields an ErrItem, and iteration continues
// with the next block. The body is decoded by the WHATWG Encoding standard's
// UTF-8 decode, as the HTML standard requires: one leading byte order mark is
// dropped, and each maximal subpart of ill-formed UTF-8 becomes one U+FFFD. A
// body of another media type, a Response not from Stream or Request.Send, or
// one already iterated, yields one error. Errors, and closing Body, are as for
// [Items]. The client never reconnects; to resume, call again with a
// Last-Event-ID field in Input.Header.
func Events(r *Response) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		x, err := claimResponse(r)
		if err != nil {
			yield(Event{}, err)
			return
		}
		defer closeStream(x, r)
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
		var e eventFields
		for {
			err := f.event(&e)
			if err == io.EOF {
				return
			}
			var value Event
			if err == nil {
				err = e.value(&value)
			}
			terminal := false
			if err != nil {
				err = withContext(x, err)
				terminal = itemTerminal(x, err)
			}
			if !yield(value, err) || terminal {
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

// readError preserves source error text and identity while marking its origin.
// In particular, a source's ErrItem must not turn a read failure into recovery.
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

func readFailed(err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	return &readError{err}
}

func contentCoding(h http.Header) error {
	if coding := h["Content-Encoding"]; len(coding) > 0 && coding[0] != "" && !strings.EqualFold(coding[0], "identity") {
		return errors.New("cannot decode a body with non-identity Content-Encoding")
	}
	return nil
}

// itemTerminal classifies the error actually yielded, before yield can
// change the context. A call-context error ends iteration even when an item
// decoder failed at the same time; a decoder's own context stays local.
func itemTerminal(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	cause := ctx.Err()
	if cause != nil && errors.Is(err, cause) {
		return true
	}
	if e, ok := err.(*contextError); ok {
		err = e.err
	}
	_, local := err.(*itemError)
	return !local
}
