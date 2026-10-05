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
	crAt    int
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
		r.crAt = -1
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
		if r.lo == r.hi {
			if err := r.fill(); err != nil {
				return r.scratch, 0, err
			}
		}
		p := r.buf[r.lo:r.hi]
		i := -1
		if p[0] == sep || cr && p[0] == '\r' {
			i = 0
		} else if cr {
			// Cache the next CR; search LF only up to it. Neither a long
			// LF-only suffix nor a CR-only suffix is repeatedly scanned.
			if r.crAt < r.lo {
				r.crAt = bytes.IndexByte(p, '\r')
				if r.crAt < 0 {
					r.crAt = r.hi
				} else {
					r.crAt += r.lo
				}
			}
			i = bytes.IndexByte(p[:r.crAt-r.lo], sep)
			if i < 0 && r.crAt < r.hi {
				i = r.crAt - r.lo
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

// jsonRecord stops at RS, or at LF outside a top-level JSON value. This
// lexical scan is linear even for multiline records. Decoding still owns
// syntax validation; malformed input can always recover at the next RS.
func (r *frameReader) jsonRecord(bound int64) ([]byte, byte, error) {
	r.scratch = r.scratch[:0]
	depth := 0
	var quoted, escape, begun, invalid bool
	for {
		if err := r.fill(); err != nil {
			return r.scratch, 0, err
		}
		p := r.buf[r.lo:r.hi]
		n, end := len(p), byte(0)
		for i, c := range p {
			if c == '\x1e' {
				n, end = i, c
				break
			}
			if quoted {
				if escape {
					escape = false
				} else if c == '\\' {
					escape = true
				} else if c == '"' {
					quoted = false
				}
			} else {
				switch c {
				case '"':
					quoted, begun = true, true
				case '{', '[':
					depth++
					begun = true
				case '}', ']':
					depth--
					begun = true
				case ' ', '\t', '\r', '\n':
				default:
					begun = true
				}
				if c == '\n' && begun && depth <= 0 && !invalid {
					n, end = i+1, c
					break
				}
			}
		}
		if int64(len(r.scratch))+int64(n) > bound {
			return nil, 0, &http.MaxBytesError{Limit: bound}
		}
		r.lo += n
		if end == '\x1e' {
			r.lo++
		}
		if end != 0 && len(r.scratch) == 0 {
			if end != '\n' || json.Valid(p[:n]) {
				return p[:n], end, nil
			}
			invalid = true
		}
		r.scratch = append(r.scratch, p[:n]...)
		if end != 0 {
			if end != '\n' || !invalid && json.Valid(r.scratch) {
				return r.scratch, end, nil
			}
			invalid = true // do not rescan malformed records at every LF
		}
	}
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
	rsTail       bool
	rsUsed       int64
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
		var e eventFields
		if err := f.event(&e); err != nil {
			return nil, err
		}
		f.object = e.appendJSON(f.object[:0])
		return f.object, nil
	}
	if f.sq.rs {
		return f.nextJSONRecord()
	}
	for {
		sep, bound := byte('\n'), f.bound
		if bound < 1<<63-1 {
			bound++ // a possible CR in the terminating CRLF
		}
		data, end, err := f.wire.until(sep, false, bound)
		if err != nil && err != io.EOF {
			if _, ok := err.(*http.MaxBytesError); ok {
				err = &http.MaxBytesError{Limit: f.bound}
			}
			return nil, err
		}
		if end == '\n' && len(data) > 0 && data[len(data)-1] == '\r' {
			data = data[:len(data)-1]
		}
		if int64(len(data)) > f.bound {
			return nil, &http.MaxBytesError{Limit: f.bound}
		}
		if len(bytes.Trim(data, " \t\r\n")) > 0 {
			return data, nil
		}
		if err == io.EOF {
			return nil, io.EOF
		}
	}
}

func (f *sequenceReader) nextJSONRecord() ([]byte, error) {
	if !f.started || f.rsTail {
		data, _, err := f.wire.until('\x1e', false, f.bound-f.rsUsed)
		if err != nil && err != io.EOF {
			if _, ok := err.(*http.MaxBytesError); ok {
				err = &http.MaxBytesError{Limit: f.bound}
			}
			return nil, err
		}
		bad := len(data) > 0
		if f.started {
			bad = len(bytes.Trim(data, " \t\r\n")) > 0
		}
		f.started, f.rsTail, f.rsUsed = true, false, 0
		if bad {
			return nil, badItem(errors.New("JSON sequence data outside a record"))
		}
		if err == io.EOF {
			return nil, err
		}
	}
	for {
		data, end, err := f.wire.jsonRecord(f.bound)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if end == '\n' {
			f.rsTail, f.rsUsed = true, int64(len(data))
		}
		if len(data) > 0 {
			if scalarTruncated(data) {
				return nil, badItem(errors.New("JSON sequence scalar lacks trailing whitespace"))
			}
			return data, nil
		}
		if err == io.EOF {
			return nil, err
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

func (f *sequenceReader) event(e *eventFields) error {
	*e = eventFields{}
	f.data = f.data[:0]
	var used int64
	for {
		if f.afterCR {
			lf, err := f.wire.optionalLF()
			f.afterCR = false
			if err != nil {
				return err
			}
			if lf && f.chargeLF {
				used++
			}
		}
		if used > f.bound {
			return &http.MaxBytesError{Limit: f.bound}
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
				return &http.MaxBytesError{Limit: f.bound}
			}
		}
		if err != nil {
			if _, ok := err.(*http.MaxBytesError); ok {
				err = &http.MaxBytesError{Limit: f.bound}
			}
			return err // even EOF drops an unterminated block
		}
		f.afterCR, f.chargeLF = end == '\r', len(line) > 0
		if len(line) == 0 {
			used = 0
			if e.data != nil || e.typeSet || e.idSet || e.retry != "" {
				return nil
			}
			continue
		}
		used += int64(len(line)) + 1
		if used > f.bound {
			return &http.MaxBytesError{Limit: f.bound}
		}
		name, value := line, []byte(nil)
		for i, c := range line {
			if c == ':' {
				name, value = line[:i], line[i+1:]
				break
			}
		}
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

func (e *eventFields) value(v *Event) error {
	var retry time.Duration
	if e.retry != "" {
		n, err := strconv.ParseUint(e.retry, 10, 64)
		if err != nil || n > uint64((1<<63-1)/time.Millisecond) {
			return badItem(errors.New("event retry exceeds time.Duration's millisecond range"))
		}
		retry = time.Duration(n) * time.Millisecond
	}
	*v = Event{Data: e.data, Event: e.typ, ID: e.id, IDSet: e.idSet, Retry: retry, RetrySet: e.retry != ""}
	return nil
}

func (e *eventFields) appendJSON(b []byte) []byte {
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
