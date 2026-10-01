package openapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A sequence is how a sequential media type frames its items.
type sequence struct {
	events bool        // text/event-stream
	sep    byte        // what ends an item, LF, or for a JSON text sequence RS, which also begins one
	item   parsedMedia // the media type of each JSON item
}

func sequenceOf(m parsedMedia) sequence {
	switch {
	case strings.EqualFold(m.sub, "event-stream"):
		return sequence{events: true}
	case hasSuffixFold(m.sub, "json-seq"): // RFC 7464, and a +json-seq type of its +json items (RFC 8091)
		sub := m.sub[:len(m.sub)-len("-seq")]
		return sequence{sep: 0x1e, item: parsedMedia{m.typ + "/" + sub, m.typ, sub, ""}}
	}
	return sequence{sep: '\n', item: parsedMedia{"application/json", "application", "json", ""}}
}

var (
	errSeparator = errors.New("a pre-encoded item holds its framing's separator")
	errEvent     = errors.New("an event stream item is an Event, or an object of data, event and id strings and a retry integer")
	errorType    = reflect.TypeFor[error]()
	boolType     = reflect.TypeFor[bool]()
)

// sequentialBody encodes v, a slice or an iterator of items, as a body of
// the sequential type m: a slice's items now, and an iterator's as the
// body is read.
func (c *Client) sequentialBody(m parsedMedia, v any, re *RequestError) payload {
	sq := sequenceOf(m)
	if seq := iterator(v); seq != nil {
		return payload{items: &items{c: c, sq: sq, seq: seq}, size: -1}
	}
	rv := reflect.ValueOf(v)
	if k := rv.Kind(); k != reflect.Slice && k != reflect.Array {
		re.input("Input.Body", errors.New("a sequential body is a slice or an iterator of its items"))
		return payload{}
	}
	var b builder
	for i := range rv.Len() {
		item := rv.Index(i).Interface()
		if at, err := c.item(&b, sq, item); err != nil {
			re.input("Input.Body/"+strconv.Itoa(i)+at, err)
		}
	}
	return b.payload()
}

// item appends the item v, framed, to b, returning the JSON Pointer, from
// v, of what it refuses: an event, or the JSON text v is or encodes, a
// reader's read as the body is.
func (c *Client) item(b *builder, sq sequence, v any) (string, error) {
	if sq.events {
		var err error
		b.buf, err = appendEvent(b.buf, v)
		return "", err
	}
	if sq.sep == 0x1e {
		b.buf = append(b.buf, 0x1e)
	}
	switch x := v.(type) {
	case io.Reader:
		p := readerPayload(x)
		p.check = []string{string(sq.sep)}
		b.add(p)
	default:
		data, ok := v.([]byte)
		if !ok {
			var at string
			var err error
			if data, at, _, err = c.encodeContent(sq.item, jsonClass, v); err != nil {
				return at, err
			}
		}
		if bytes.IndexByte(data, sq.sep) >= 0 {
			return "", errSeparator
		}
		b.buf = append(b.buf, data...)
	}
	b.buf = append(b.buf, '\n')
	return "", nil
}

// appendEvent appends v, an Event or an object with no members but data,
// event and id, as strings, and retry, as a non-negative integer, as the
// lines of one event: each a "field: value" line, data split at its line
// breaks, then a blank line.
func appendEvent(b []byte, v any) ([]byte, error) {
	if e, ok := v.(Event); ok {
		switch {
		case strings.ContainsAny(e.Event, "\r\n") || e.IDSet && strings.ContainsAny(e.ID, "\r\n"):
			return b, errEvent
		case e.RetrySet && (e.Retry < 0 || e.Retry%time.Millisecond != 0):
			return b, errors.New("an event's Retry is a non-negative whole number of milliseconds")
		}
		if e.Data != nil {
			b = appendData(b, string(e.Data))
		}
		if e.Event != "" {
			b = append(append(append(b, "event: "...), e.Event...), '\n')
		}
		if e.IDSet {
			b = append(append(append(b, "id: "...), e.ID...), '\n')
		}
		if e.RetrySet {
			b = append(strconv.AppendInt(append(b, "retry: "...), e.Retry.Milliseconds(), 10), '\n')
		}
		return append(b, '\n'), nil
	}
	s, err := marshal(v)
	if _, reader := v.(io.Reader); err != nil || reader || s[0] != '{' {
		return b, errEvent
	}
	r := &jsonReader{s: s}
	err = r.each(func(name string) error {
		k := r.s[r.i]
		if k == '{' || k == '[' {
			return errEvent
		}
		quoted, val := k == '"', r.scalar()
		switch {
		case name == "data" && quoted:
			b = appendData(b, val)
		case (name == "event" || name == "id") && quoted && !strings.ContainsAny(val, "\r\n"),
			name == "retry" && !quoted && strings.Trim(val, "0123456789") == "":
			b = append(append(append(append(b, name...), ": "...), val...), '\n')
		default:
			return errEvent
		}
		return nil
	})
	return append(b, '\n'), err
}

// appendData appends a data line for each line of s, split at CRLF, LF or
// CR (the HTML standard's end-of-line).
func appendData(b []byte, s string) []byte {
	for {
		i := strings.IndexAny(s, "\r\n")
		if i < 0 {
			return append(append(append(b, "data: "...), s...), '\n')
		}
		b = append(append(append(b, "data: "...), s[:i]...), '\n')
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			i++
		}
		s = s[i+1:]
	}
}

// iterator returns v as a sequence of items and errors when it is an
// iter.Seq, or an iter.Seq2 whose second value is an error, of any element
// type, and otherwise nil.
func iterator(v any) iter.Seq2[any, error] {
	switch s := v.(type) {
	case iter.Seq2[any, error]:
		return s
	case iter.Seq[any]:
		if s == nil {
			return nil
		}
		return func(yield func(any, error) bool) { s(func(v any) bool { return yield(v, nil) }) }
	}
	f := reflect.ValueOf(v)
	if f.Kind() != reflect.Func || f.IsNil() || f.Type().NumIn() != 1 || f.Type().NumOut() != 0 {
		return nil
	}
	y := f.Type().In(0)
	if y.Kind() != reflect.Func || y.NumOut() != 1 || y.Out(0) != boolType || y.NumIn() != 1 && (y.NumIn() != 2 || y.In(1) != errorType) {
		return nil
	}
	return func(yield func(any, error) bool) {
		f.Call([]reflect.Value{reflect.MakeFunc(y, func(in []reflect.Value) []reflect.Value {
			var err error
			if len(in) == 2 {
				err, _ = in[1].Interface().(error)
			}
			return []reflect.Value{reflect.ValueOf(yield(in[0].Interface(), err))}
		})})
	}
}

// items is an iterator body, read once: the iterator runs as the body is
// read, on the reader's goroutine, each item it yields encoded and framed
// then, and stops, its yield returning false, once the body is closed and
// no Read is in flight.
type items struct {
	c   *Client
	sq  sequence
	seq iter.Seq2[any, error]

	mu              sync.Mutex
	reading, closed bool
	once            sync.Once // stops the iterator
	next            func() (any, error, bool)
	stop            func()
	n               int        // the items yielded
	cur             cursor     // the item being read
	b               builder    // where it is encoded
	one             [1]payload // its bytes, when it has no reader
}

func (it *items) Read(buf []byte) (int, error) {
	it.mu.Lock()
	if it.closed {
		it.mu.Unlock()
		return 0, errClosedEarly
	}
	it.reading = true
	it.mu.Unlock()
	n, err := it.read(buf)
	it.mu.Lock()
	it.reading = false
	closed := it.closed
	it.mu.Unlock()
	if closed {
		it.halt()
	}
	return n, err
}

func (it *items) read(buf []byte) (int, error) {
	for {
		if n, err := it.cur.Read(buf); n > 0 || err != io.EOF {
			return n, err
		}
		if it.next == nil {
			it.next, it.stop = iter.Pull2(it.seq)
		}
		v, err, ok := it.next()
		switch {
		case !ok:
			return 0, io.EOF
		case err != nil:
			return 0, err
		}
		it.b.buf, it.b.parts = it.b.buf[:0], it.b.parts[:0]
		if at, err := it.c.item(&it.b, it.sq, v); err != nil {
			return 0, fmt.Errorf("item %d%s: %w", it.n, at, err)
		}
		it.n++
		p := it.b.payload()
		if it.cur.parts = p.parts; p.parts == nil {
			it.one[0] = p
			it.cur.parts = it.one[:]
		}
		it.cur.pos, it.cur.tail = 0, append(it.cur.tail[:0], lead(it.cur.parts[0].check)...)
	}
}

// Close stops the iterator, now or, when a Read is in flight, as it returns.
func (it *items) Close() error {
	it.mu.Lock()
	it.closed = true
	reading := it.reading
	it.mu.Unlock()
	if !reading {
		it.halt()
	}
	return nil
}

// halt stops the iterator, if it started, once; a concurrent call waits for
// it to return.
func (it *items) halt() {
	it.once.Do(func() {
		if it.stop != nil {
			it.stop()
		}
	})
}
