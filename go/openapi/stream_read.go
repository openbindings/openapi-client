package openapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// responseStream is the consumption claim and close policy for one actual
// response from Send or Stream. Value copies of Response share this claim;
// a fabricated http.Response carrying the request context does not.
type responseStream struct {
	io.ReadCloser
	x        *exchange
	response *http.Response
	taken    atomic.Bool
	closed   atomic.Bool
	once     sync.Once
	err      error
	stop     func() bool // guarded by x.mu; unregisters the context callback
}

func (x *exchange) streamResponse(req *http.Request, policy bool) (*Response, error) {
	r, _, err := x.send(req)
	if r == nil || err != nil {
		if x.returned {
			x.stopUpload()
		}
		return r, err
	}
	s := &responseStream{ReadCloser: r.Body, x: x, response: r.Response}
	// Upgraded connections and CONNECT tunnels retain the exact body the
	// transport supplied, including its writing capabilities.
	if policy || r.StatusCode != http.StatusSwitchingProtocols && !(req.Method == "CONNECT" && r.StatusCode/100 == 2) {
		r.Body = s
	}
	x.mu.Lock()
	x.stream = s
	if !x.done {
		s.stop = context.AfterFunc(x.Context, x.stopUpload)
	}
	x.mu.Unlock()
	if policy && r.StatusCode/100 != 2 {
		return r, x.status(r)
	}
	return r, nil
}

func (s *responseStream) Read(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, errors.New("openapi: response body is closed")
	}
	if err := s.x.Err(); err != nil {
		return 0, withContext(s.x, err)
	}
	n, err := s.ReadCloser.Read(p)
	return n, withContext(s.x, err)
}

func (s *responseStream) Close() error {
	s.once.Do(func() {
		s.closed.Store(true)
		s.x.stopUpload()
		s.err = s.ReadCloser.Close()
	})
	return s.err
}

// stopUpload requests cancellation without waiting for an arbitrary reader
// already in Read. sentBody ends only when both Read and Close have returned.
func (x *exchange) stopUpload() {
	x.mu.Lock()
	var open []*sentBody
	canceled := x.Err() != nil
	for b := x.last; b != nil; b = b.prev {
		if !b.closed && (!b.finished || canceled) {
			open = append(open, b)
		}
	}
	x.mu.Unlock()
	for _, b := range open {
		b.Close()
	}
}

func claimResponse(r *Response) (*exchange, error) {
	if r != nil && r.Response != nil {
		x := exchangeOf(r.Response)
		if x != nil && x.stream != nil && x.stream.response == r.Response && r.Body != nil && !x.stream.closed.Load() && !x.stream.taken.Swap(true) {
			return x, nil
		}
	}
	return nil, errors.New("openapi: iteration requires an unconsumed response from Stream or Send")
}

// closeStream also stops uploads when an iterator owns an unwrapped tunnel
// body; a caller closing that raw body directly is governed by its transport.
func closeStream(x *exchange, r *Response) {
	if r.Body != x.stream {
		x.stopUpload()
	}
	r.Body.Close()
}

// multipartItems follows NextPart for raw parts; decoded parts use
// NextRawPart so transfer errors can be separated from wire-read errors.
func multipartItems[T any](x *exchange, r *Response, ct parsedMedia, bound int64, yield func(T, error) bool) {
	var zero T
	boundary, _ := ct.param("boundary")
	ending := &multipartEnding{Reader: r.Body, marker: "--" + boundary + "--"}
	wire := &partSource{Reader: ending}
	mr := multipart.NewReader(wire, boundary)
	_, raw := any(&zero).(**multipart.Part)
	for {
		var part *multipart.Part
		var err error
		if raw {
			part, err = mr.NextPart()
		} else {
			part, err = mr.NextRawPart()
		}
		if err == io.EOF {
			if ending.complete() {
				return
			}
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			if !wire.seen && wire.err == io.EOF && errors.Is(err, io.EOF) {
				return
			}
			if wire.err != nil && wire.err != io.EOF && errors.Is(err, wire.err) {
				err = wire.err
			} else {
				err = decoded(err)
			}
			yield(zero, withContext(x, err))
			return
		}
		var value T
		if raw {
			*any(&value).(**multipart.Part) = part
		} else {
			var data []byte
			var opaque bool
			data, opaque, err = readPart(part, bound)
			if err == nil {
				if _, bytesTarget := any(&value).(*[]byte); !bytesTarget {
					err = contentCoding(http.Header(part.Header))
					if err != nil {
						err = badItem(decoded(err))
					}
				}
				if err == nil {
					pt := mediaOf(http.Header(part.Header))
					if len(part.Header["Content-Type"]) == 0 {
						pt = parsedMedia{full: "text/plain", typ: "text", sub: "plain"}
						if strings.EqualFold(ct.sub, "digest") {
							pt = parsedMedia{full: "message/rfc822", typ: "message", sub: "rfc822"}
						}
					}
					if opaque { // RFC 2045 section 6.4: unknown transfer encoding
						pt = octetStream
					}
					err = decodeItem(x.cfg, pt, data, &value)
					if err != nil {
						err = badItem(decoded(err))
					}
				}
			}
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

// mime/multipart can report EOF both for a closing delimiter and for an
// empty, truncated header block. Recognize closing delimiter lines with
// constant storage so those outcomes remain distinct, even with epilogues
// and arbitrarily fragmented reads. The MIME reader still parses all parts.
type multipartEnding struct {
	io.Reader
	marker    string
	matched   int
	cr, ended bool
}

func (r *multipartEnding) complete() bool {
	return r.ended || r.matched == len(r.marker) && !r.cr
}

func (r *multipartEnding) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if r.ended {
		return n, err
	}
	for i := 0; i < n; {
		if r.matched < 0 {
			// Skip ordinary header/payload bytes in bulk; only a line
			// starting with two hyphens can be a closing delimiter.
			j := bytes.Index(p[i:n], []byte("\n--"))
			if j < 0 {
				if p[n-1] == '\n' {
					r.matched, r.cr = 0, false
				} else if n-i >= 2 && p[n-2] == '\n' && p[n-1] == '-' {
					r.matched, r.cr = 1, false
				}
				break
			}
			i += j + 3
			r.matched, r.cr = 2, false
			continue
		}
		c := p[i]
		i++
		if c == '\n' {
			if r.matched == len(r.marker) {
				r.ended = true
				break
			}
			r.matched, r.cr = 0, false
		} else if r.matched >= 0 && r.matched < len(r.marker) && c == r.marker[r.matched] {
			r.matched++
		} else if r.matched == len(r.marker) && !r.cr && (c == ' ' || c == '\t') {
		} else if r.matched == len(r.marker) && !r.cr && c == '\r' {
			r.cr = true
		} else {
			r.matched = -1
		}
	}
	return n, err
}

// partSource remembers wire bytes and read errors, distinguishing them from
// MIME parser and transfer decoder failures without inspecting error text.
type partSource struct {
	io.Reader
	err  error
	seen bool
}

func (r *partSource) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.Reader.Read(p)
	r.seen = r.seen || n > 0
	r.err = err
	return n, err
}

func readPart(p *multipart.Part, bound int64) ([]byte, bool, error) {
	source := &partSource{Reader: p}
	var r io.Reader = source
	var opaque bool
	switch strings.ToLower(strings.TrimSpace(p.Header.Get("Content-Transfer-Encoding"))) {
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, mimeBase64{source})
	case "quoted-printable":
		r = quotedprintable.NewReader(source)
	case "", "7bit", "8bit", "binary":
	default:
		opaque = true
	}
	data, err := readAll(nil, r, -1, bound)
	var size *http.MaxBytesError
	if err != nil && !errors.As(err, &size) {
		// Recovery requires the complete part boundary. A source failure
		// before it takes precedence over the saved transfer syntax error.
		io.Copy(io.Discard, source)
		err = badItem(decoded(err))
	}
	if source.err != nil && source.err != io.EOF {
		return nil, opaque, readFailed(source.err)
	}
	return data, opaque, err
}

// MIME base64 ignores SP and HTAB in addition to the standard decoder's
// CR/LF handling. Filter in place before decoding and decoded-byte limiting.
type mimeBase64 struct{ io.Reader }

func (r mimeBase64) Read(p []byte) (int, error) {
	for {
		n, err := r.Reader.Read(p)
		used := 0
		for _, c := range p[:n] {
			if c != ' ' && c != '\t' {
				p[used] = c
				used++
			}
		}
		if used > 0 || n == 0 || err != nil {
			return used, err
		}
	}
}

// decodeSequence assembles syntax, not decoded values. The one final decode
// preserves the target's own UnmarshalJSON behavior and the item-type codec.
func (cfg *config) decodeSequence(ct parsedMedia, data []byte, out any) error {
	f := newSequenceReader(bytes.NewReader(data), sequenceOf(ct), limit(-1, 0))
	array := []byte{'['}
	codec, _ := cfg.codec(f.sq.item)
	for {
		item, err := f.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if item, ok := err.(*itemError); ok {
				err = item.err
			}
			return err
		}
		if codec == nil && !f.sq.events && !json.Valid(item) {
			// Obtain encoding/json's syntax error without decoding a value.
			var invalid json.RawMessage
			return decoded(json.Unmarshal(item, &invalid))
		}
		if len(array) > 1 {
			array = append(array, ',')
		}
		array = append(array, item...)
	}
	return cfg.decodeData(f.sq.item, nil, append(array, ']'), out)
}
