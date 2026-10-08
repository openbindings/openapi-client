package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// A sequence is how a sequential media type frames its items.
type sequence struct {
	events bool        // text/event-stream
	rs     bool        // a JSON text sequence: RS before each item
	seps   [][]byte    // what an item's JSON text cannot hold: LF or CR, which end a JSON Lines item, or RS
	item   parsedMedia // the media type of each JSON item
}

var (
	lineSeps = [][]byte{{'\n'}, {'\r'}}
	rsSeps   = [][]byte{{0x1e}}
)

func sequenceOf(m parsedMedia) sequence {
	switch {
	case strings.EqualFold(m.sub, "event-stream"):
		return sequence{events: true}
	case hasSuffixFold(m.sub, "json-seq"): // RFC 7464, and a +json-seq type of its +json items (RFC 8091)
		sub := m.sub[:len(m.sub)-len("-seq")]
		return sequence{rs: true, seps: rsSeps, item: parsedMedia{m.typ + "/" + sub, m.typ, sub, ""}}
	}
	return sequence{seps: lineSeps, item: defaultMedia[0].parsed}
}

var (
	errByteList  = errors.New("a byte slice is a base64 string in its JSON data, not a list of items or parts")
	errSeparator = errors.New("a pre-encoded item holds its framing's separator")
	errCodecSep  = errors.New("the codec's output for the item holds its framing's separator")
	errEvent     = errors.New("an event stream item is an Event, or an object of data, event and id strings and a retry integer")
	errEventLine = errors.New("an event's event and id cannot hold a line break")
	errEventNUL  = errors.New("an event's id cannot hold a NUL")
	errorType    = reflect.TypeFor[error]()
	boolType     = reflect.TypeFor[bool]()
)

// sequentialBody encodes v, a slice or an iterator of items, as a body of
// the sequential type m: a slice's items now, and an iterator's as the
// body is read.
func (c *Client) sequentialBody(m parsedMedia, v any, re *RequestError) payload {
	w := c.itemWriter(sequenceOf(m))
	if seq := iterator(v); seq != nil {
		return payload{once: &items{w: w, seq: seq}, size: -1}
	}
	x := c.doc.datum(v) // by its JSON data, whatever its Go kind, null having no items
	switch j, own := x.v.(jsonData); {
	case own && j.err != nil:
		re.input("Input.Body", j.err)
		return payload{}
	case x.bytes:
		re.input("Input.Body", errByteList)
		return payload{}
	case !x.null && !x.list:
		re.input("Input.Body", errors.New("a sequential body is a list or an iterator of its items"))
		return payload{}
	}
	if x.list {
		if t := x.items.Type().Elem(); t.Kind() != reflect.Interface {
			e := c.doc.walkOf(t, nil)
			w.static = !e.holds && e.levels > 0 && e.levels <= maxDepth // a proven bound, no reader
		}
	}
	for i := range x.len() {
		if at, err := w.write(x.item(i)); err != nil {
			re.input("Input.Body/"+strconv.Itoa(i)+at, err)
		}
	}
	return w.b.payload()
}

// An itemWriter writes the items of one sequential body, framed, into b: a
// JSON item by the caller's codec for its type, if any, or by one
// encoding/json Encoder, whose LF after each value ends it.
type itemWriter struct {
	multipart *partWriter
	fields    *formEncoding
	position  int
	c         *Client
	sq        sequence
	b         builder
	codec     Codec
	enc       *json.Encoder
	static    bool // the items' type holds no reader and nests at most 1,000 levels
}

func (c *Client) itemWriter(sq sequence) *itemWriter {
	w := &itemWriter{c: c, sq: sq}
	w.codec, _ = c.cfg.codec(sq.item)
	return w
}

// write writes the item v, returning the JSON Pointer, from v, of what it
// refuses: an event, or the JSON text v is or encodes, a reader's read as
// the body is. A typed nil is the value null, never a reader.
func (w *itemWriter) write(v any) (string, error) {
	if w.multipart != nil {
		w.multipart.position(w.fields, v, "", w.position)
		w.position++
		if err := w.multipart.re.refused(); err != nil {
			return "", err
		}
		return "", nil
	}
	b, start := &w.b, len(w.b.buf)
	if w.sq.events {
		var err error
		b.buf, err = w.c.doc.appendEvent(b.buf, v)
		return "", err
	}
	if w.sq.rs {
		b.buf = append(b.buf, 0x1e)
	}
	var at string
	var err error
	switch x := v.(type) {
	case []byte:
		err = w.text(x, errSeparator)
	default:
		r, reader := v.(io.Reader)
		j, data := v.(jsonData)
		switch {
		case reader && !null(v):
			b.add(source{payload: readerPayload(r), check: w.sq.seps})
			b.buf = append(b.buf, '\n')
		case w.codec != nil:
			var buf bytes.Buffer
			if err = w.codec.Encode(&buf, bare(v)); err == nil {
				err = w.text(bytes.TrimRight(buf.Bytes(), " \t\n\r"), errCodecSep) // trailing JSON whitespace
			}
		case data:
			var s string
			if s, err = j.text(); err == nil {
				b.buf = append(appendJSON(b.buf, s), '\n') // compact JSON text, which holds no separator
			}
		case w.static:
			_, err = w.encode(v)
		default:
			_, at, err = encodeJSON(w.c.doc, v, w.encode)
		}
	}
	if err != nil {
		b.buf = b.buf[:start]
	}
	return at, err
}

// text appends an item's JSON text, which cannot hold a separator, refused
// with err.
func (w *itemWriter) text(data []byte, err error) error {
	for _, sep := range w.sq.seps {
		if bytes.Contains(data, sep) {
			return err
		}
	}
	w.b.buf = append(append(w.b.buf, data...), '\n')
	return nil
}

// encode writes v with the Encoder, made at the first, returning what it
// wrote: JSON text, which holds no separator, then LF.
func (w *itemWriter) encode(v any) ([]byte, error) {
	if w.enc == nil {
		w.enc = json.NewEncoder(&w.b)
	}
	if h, ok := v.(held); ok {
		v = h.p.Interface() // written as json writes it where it is held
	}
	start := len(w.b.buf)
	if err := w.enc.Encode(v); err != nil {
		return nil, &encodingError{err}
	}
	return w.b.buf[start:], nil
}

// appendEvent appends v, an Event, a non-nil *Event, or an object with no
// members but data, event and id, as strings, and retry, as a non-negative
// integer, as the lines of one event: each a "field: value" line, data split
// at its line breaks, then a blank line. Invalid UTF-8 is written as U+FFFD,
// as encoding/json writes it; a line break in event or id, and a NUL in id,
// which a receiver would split at or ignore, cannot be encoded.
func (d *document) appendEvent(b []byte, v any) ([]byte, error) {
	if e, ok := v.(*Event); ok && e != nil {
		v = *e
	}
	if e, ok := v.(Event); ok {
		if err := eventField("event", e.Event); err != nil {
			return b, err
		}
		if err := eventField("id", e.ID); e.IDSet && err != nil {
			return b, err
		}
		if e.RetrySet && (e.Retry < 0 || e.Retry%time.Millisecond != 0) {
			return b, errors.New("an event's Retry is a non-negative whole number of milliseconds")
		}
		switch {
		case e.Data == nil:
		case utf8.Valid(e.Data):
			b = appendData(b, e.Data)
		default:
			b = appendData(b, jsonText(string(e.Data)))
		}
		if e.Event != "" {
			b = append(append(append(b, "event: "...), jsonText(e.Event)...), '\n')
		}
		if e.IDSet {
			b = append(append(append(b, "id: "...), jsonText(e.ID)...), '\n')
		}
		if e.RetrySet {
			b = append(strconv.AppendInt(append(b, "retry: "...), e.Retry.Milliseconds(), 10), '\n')
		}
		return append(b, '\n'), nil
	}
	s, _, err := encodeJSON(d, v, marshal) // refusing a reader and nesting past 1,000 levels
	switch {
	case err != nil:
		return b, err
	case s[0] != '{':
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
		case (name == "event" || name == "id") && quoted:
			if err := eventField(name, val); err != nil {
				return err
			}
			fallthrough
		case name == "retry" && !quoted && strings.Trim(val, "0123456789") == "":
			b = append(append(append(append(b, name...), ": "...), val...), '\n')
		default:
			return errEvent
		}
		return nil
	})
	return append(b, '\n'), err
}

// eventField returns why an event's event or id field cannot hold s: a line
// break, at which a receiver would split it, or, in id, a NUL, for which a
// receiver ignores the field.
func eventField(name, s string) error {
	switch {
	case strings.ContainsAny(s, "\r\n"):
		return errEventLine
	case name == "id" && strings.IndexByte(s, 0) >= 0:
		return errEventNUL
	}
	return nil
}

// appendData appends a data line for each line of s, split at CRLF, LF or
// CR (the HTML standard's end-of-line).
func appendData[T string | []byte](b []byte, s T) []byte {
	for {
		i := 0
		for i < len(s) && s[i] != '\r' && s[i] != '\n' {
			i++
		}
		b = append(append(append(b, "data: "...), s[:i]...), '\n')
		if i == len(s) {
			return b
		}
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
	w   *itemWriter
	seq iter.Seq2[any, error]

	mu              sync.Mutex
	reading, closed bool
	once            sync.Once // stops the iterator
	next            func() (any, error, bool)
	stop            func()
	n               int // the items yielded
	done            bool
	cur             cursor    // the item being read
	one             [1]source // its bytes, when it has no reader
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
		if it.done {
			return 0, io.EOF
		}
		if it.next == nil {
			it.next, it.stop = iter.Pull2(it.seq)
		}
		v, err, ok := it.next()
		switch {
		case !ok:
			it.done = true
			if it.w.multipart == nil {
				return 0, io.EOF
			}
			it.w.b.buf, it.w.b.parts = it.w.b.buf[:0], it.w.b.parts[:0]
			if !it.w.multipart.delimiter("--") {
				return 0, errDelimiter
			}
			p := it.w.b.payload()
			it.cur.parts = p.parts
			if p.parts == nil {
				it.one[0] = source{payload: p}
				it.cur.parts = it.one[:]
			}
			it.cur.pos = 0
			it.cur.begin()
			continue
		case err != nil:
			return 0, err
		}
		b := &it.w.b
		b.buf, b.parts = b.buf[:0], b.parts[:0]
		if at, err := it.w.write(v); err != nil {
			return 0, fmt.Errorf("%s: %w", label("item "+strconv.Itoa(it.n)+at), err)
		}
		it.n++
		p := b.payload()
		if it.cur.parts = p.parts; p.parts == nil {
			it.one[0] = source{payload: p}
			it.cur.parts = it.one[:]
		}
		it.cur.pos = 0
		it.cur.begin()
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
