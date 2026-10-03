package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// frameReader scans each input byte once, retaining at most the current
// record plus a fixed read buffer. A delimiter can finish a frame even when
// the read that supplied it also failed; unfinished data retains that error.
type frameReader struct {
	r       io.Reader
	buf     [2048]byte
	lo, hi  int
	err     error
	scratch []byte
}

func (r *frameReader) fill() error {
	if r.lo < r.hi {
		return nil
	}
	if r.err != nil {
		return r.err
	}
	for range 100 {
		r.lo = 0
		r.hi, r.err = r.r.Read(r.buf[:])
		r.err = readFailed(r.err)
		if r.hi > 0 {
			return nil
		}
		if r.err != nil {
			return r.err
		}
	}
	r.err = readFailed(io.ErrNoProgress)
	return r.err
}

// until returns bytes preceding sep, or CR when cr is true, and consumes
// the separator. Its bytes remain valid until the next call. bound excludes
// the separator; callers account for the format's delimiters separately.
func (r *frameReader) until(sep byte, cr bool, bound int64) ([]byte, byte, error) {
	r.scratch = r.scratch[:0]
	for {
		if err := r.fill(); err != nil {
			return r.scratch, 0, err
		}
		p := r.buf[r.lo:r.hi]
		i := -1
		if cr {
			for j, c := range p {
				if c == sep || c == '\r' {
					i = j
					break
				}
			}
		} else {
			i = bytes.IndexByte(p, sep)
		}
		n := len(p)
		if i >= 0 {
			n = i
		}
		if int64(len(r.scratch))+int64(n) > bound {
			return nil, 0, &http.MaxBytesError{Limit: bound}
		}
		r.lo += n
		if i >= 0 {
			r.lo++
			if len(r.scratch) == 0 {
				return p[:i], p[i], nil
			}
			r.scratch = append(r.scratch, p[:i]...)
			return r.scratch, p[i], nil
		}
		r.scratch = append(r.scratch, p...)
	}
}

func (r *frameReader) optionalLF() (bool, error) {
	if err := r.fill(); err != nil {
		return false, err
	}
	if r.buf[r.lo] != '\n' {
		return false, nil
	}
	r.lo++
	return true, nil
}

// sequenceReader owns only one record's framing and an SSE block's fields.
// Consumers choose recovery: Items continues after ErrItem, aggregate decode
// fails, and Events alone imposes time.Duration's range on the retry field.
type sequenceReader struct {
	wire         frameReader
	sq           sequence
	bound        int64
	started      bool
	afterCR      bool
	chargeLF     bool
	data, object []byte
}

func newSequenceReader(r io.Reader, sq sequence, bound int64) *sequenceReader {
	if sq.events {
		sq.item = defaultMedia[0].parsed
	}
	return &sequenceReader{wire: frameReader{r: r}, sq: sq, bound: bound}
}

func (f *sequenceReader) next() ([]byte, error) {
	if f.sq.events {
		e, err := f.event()
		if err != nil {
			return nil, err
		}
		f.object = e.appendJSON(f.object[:0])
		return f.object, nil
	}
	for {
		sep, bound := byte('\n'), f.bound
		if f.sq.rs {
			sep = '\x1e'
		} else if bound < 1<<63-1 {
			bound++ // a possible CR in the terminating CRLF
		}
		data, end, err := f.wire.until(sep, false, bound)
		if err != nil && err != io.EOF {
			if _, ok := err.(*http.MaxBytesError); ok {
				err = &http.MaxBytesError{Limit: f.bound}
			}
			return nil, err
		}
		if !f.sq.rs && end == '\n' && len(data) > 0 && data[len(data)-1] == '\r' {
			data = data[:len(data)-1]
		}
		if int64(len(data)) > f.bound {
			return nil, &http.MaxBytesError{Limit: f.bound}
		}
		if f.sq.rs && !f.started {
			f.started = true
			if len(data) > 0 {
				return nil, badItem(errors.New("JSON sequence data precedes its first record separator"))
			}
		} else if f.sq.rs && len(data) > 0 {
			if scalarTruncated(data) {
				return nil, badItem(errors.New("JSON sequence scalar lacks trailing whitespace"))
			}
			return data, nil
		} else if !f.sq.rs && len(bytes.Trim(data, " \t\r\n")) > 0 {
			return data, nil
		}
		if err == io.EOF {
			return nil, io.EOF
		}
	}
}

func scalarTruncated(data []byte) bool {
	t := bytes.TrimLeft(data, " \t\r\n")
	if len(t) == 0 {
		return false
	}
	switch t[0] {
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 't', 'f', 'n':
		last := data[len(data)-1]
		return last != ' ' && last != '\t' && last != '\r' && last != '\n' && json.Valid(t)
	}
	return false
}

// eventFields retains field presence independently of empty values. Retry
// remains decimal text until a consumer chooses its representation.
type eventFields struct {
	data           []byte
	typ, id, retry string
	typeSet, idSet bool
}

func (f *sequenceReader) event() (eventFields, error) {
	var e eventFields
	f.data = f.data[:0]
	var used int64
	for {
		if f.afterCR {
			lf, err := f.wire.optionalLF()
			f.afterCR = false
			if err != nil {
				return eventFields{}, err
			}
			if lf && f.chargeLF {
				used++
			}
		}
		if used > f.bound {
			return eventFields{}, &http.MaxBytesError{Limit: f.bound}
		}
		left := f.bound - used
		if !f.started {
			left = min(left, 1<<63-4) + 3 // one leading BOM is outside the bound
		}
		line, end, err := f.wire.until('\n', true, left)
		if !f.started {
			line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
			f.started = true
			if int64(len(line)) > f.bound-used {
				return eventFields{}, &http.MaxBytesError{Limit: f.bound}
			}
		}
		if err != nil {
			if _, ok := err.(*http.MaxBytesError); ok {
				err = &http.MaxBytesError{Limit: f.bound}
			}
			return eventFields{}, err // even EOF drops an unterminated block
		}
		f.afterCR, f.chargeLF = end == '\r', len(line) > 0
		if len(line) == 0 {
			used = 0
			if e.data != nil || e.typeSet || e.idSet || e.retry != "" {
				return e, nil
			}
			continue
		}
		used += int64(len(line)) + 1
		if used > f.bound {
			return eventFields{}, &http.MaxBytesError{Limit: f.bound}
		}
		name, value, _ := bytes.Cut(line, []byte{':'})
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch string(name) {
		case "data":
			if e.data != nil {
				f.data = append(f.data, '\n')
			}
			if utf8.Valid(value) {
				f.data = append(f.data, value...)
			} else {
				f.data = append(f.data, eventText(string(value))...)
			}
			if f.data == nil {
				f.data = []byte{}
			}
			e.data = f.data
		case "event":
			e.typ, e.typeSet = eventText(string(value)), true
		case "id":
			if bytes.IndexByte(value, 0) < 0 {
				e.id, e.idSet = eventText(string(value)), true
			}
		case "retry":
			if len(value) > 0 && len(bytes.Trim(value, "0123456789")) == 0 {
				value = bytes.TrimLeft(value, "0")
				if len(value) == 0 {
					e.retry = "0"
				} else {
					e.retry = string(value)
				}
			}
		}
	}
}

func (e eventFields) value() (Event, error) {
	v := Event{Data: e.data, Event: e.typ, ID: e.id, IDSet: e.idSet, RetrySet: e.retry != ""}
	if v.RetrySet {
		n, err := strconv.ParseUint(e.retry, 10, 64)
		if err != nil || n > uint64((1<<63-1)/time.Millisecond) {
			return Event{}, badItem(errors.New("event retry exceeds time.Duration's millisecond range"))
		}
		v.Retry = time.Duration(n) * time.Millisecond
	}
	return v, nil
}

func (e eventFields) appendJSON(b []byte) []byte {
	b = append(b, '{')
	field := func(name, value string) {
		if b[len(b)-1] != '{' {
			b = append(b, ',')
		}
		b = append(append(b, name...), ':')
		quoted, _ := json.Marshal(value)
		b = append(b, quoted...)
	}
	if e.data != nil {
		field(`"data"`, string(e.data))
	}
	if e.typeSet {
		field(`"event"`, e.typ)
	}
	if e.idSet {
		field(`"id"`, e.id)
	}
	if e.retry != "" {
		if b[len(b)-1] != '{' {
			b = append(b, ',')
		}
		b = append(append(b, `"retry":`...), e.retry...)
	}
	return append(b, '}')
}

// eventText applies the HTML UTF-8 decoder's replacement rule: a malformed
// sequence consumes its valid prefix, leaving the first invalid continuation
// for the next decoding step. JSON's per-byte replacement differs here.
func eventText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && n == 1 {
			need, low, high := 0, byte(0x80), byte(0xbf)
			switch c := s[0]; {
			case c >= 0xc2 && c <= 0xdf:
				need = 2
			case c >= 0xe0 && c <= 0xef:
				need = 3
				if c == 0xe0 {
					low = 0xa0
				} else if c == 0xed {
					high = 0x9f
				}
			case c >= 0xf0 && c <= 0xf4:
				need = 4
				if c == 0xf0 {
					low = 0x90
				} else if c == 0xf4 {
					high = 0x8f
				}
			}
			if need > 0 && len(s) > 1 && s[1] >= low && s[1] <= high {
				n = 2
				for n < need && n < len(s) && s[n]&0xc0 == 0x80 {
					n++
				}
			}
		}
		b.WriteRune(r)
		s = s[n:]
	}
	return b.String()
}
