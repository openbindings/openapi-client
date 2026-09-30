package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxDepth is how deeply a document may nest, the outermost value being
// level 1.
const maxDepth = 1000

// A node is one JSON value of a document, with its byte span in the source,
// so that Document and Schema.Raw return values exactly as written.
type node struct {
	kind       byte     // '{', '[', '"', '0' (a number), 't', 'f' or 'n'
	start, end int      // the value's bytes in the source
	text       string   // a string's value, or a number as written
	keys       []string // an object's member names, in order
	kids       []node   // an object's member values, or an array's items
}

// get returns the member of an object named key, or nil.
func (n *node) get(key string) *node {
	if n == nil || n.kind != '{' {
		return nil
	}
	if i := slices.Index(n.keys, key); i >= 0 {
		return &n.kids[i]
	}
	return nil
}

// str returns the string member named key, or "".
func (n *node) str(key string) string {
	if m := n.get(key); m != nil && m.kind == '"' {
		return m.text
	}
	return ""
}

// flag reports whether the member named key is true.
func (n *node) flag(key string) bool {
	m := n.get(key)
	return m != nil && m.kind == 't'
}

// strs returns the strings of the array member named key, or nil.
func (n *node) strs(key string) []string {
	m := n.get(key)
	if m == nil || m.kind != '[' {
		return nil
	}
	s := make([]string, 0, len(m.kids))
	for _, k := range m.kids {
		if k.kind == '"' {
			s = append(s, k.text)
		}
	}
	return s
}

// parseTree reads src, a JSON text, as a document retrieved from uri.
func parseTree(src []byte, uri string) (node, error) {
	p := treeParser{src: src, uri: uri, dec: json.NewDecoder(bytes.NewReader(src))}
	if !utf8.Valid(src) {
		i := 0
		for i < len(src) {
			r, size := utf8.DecodeRune(src[i:])
			if r == utf8.RuneError && size == 1 {
				break
			}
			i += size
		}
		return node{}, p.errorAt(i, "invalid UTF-8")
	}
	p.dec.UseNumber()
	root, err := p.value(1)
	if err != nil {
		return node{}, err
	}
	if i := p.skip(int(p.dec.InputOffset())); i < len(src) {
		return node{}, p.errorAt(i, "data after the document")
	}
	return root, nil
}

type treeParser struct {
	src []byte
	uri string
	dec *json.Decoder
}

func (p *treeParser) value(depth int) (node, error) {
	start := p.skip(int(p.dec.InputOffset()))
	if depth > maxDepth {
		return node{}, p.errorAt(start, "nesting deeper than 1,000 levels")
	}
	tok, err := p.dec.Token()
	if err != nil {
		return node{}, p.tokenError(err)
	}
	n := node{start: start}
	switch t := tok.(type) {
	case json.Delim:
		n.kind = byte(t)
		if err := p.children(&n, depth); err != nil {
			return node{}, err
		}
	case string:
		n.kind, n.text = '"', t
	case json.Number:
		n.kind, n.text = '0', string(t)
	case bool:
		n.kind = 'f'
		if t {
			n.kind = 't'
		}
	default:
		n.kind = 'n'
	}
	n.end = int(p.dec.InputOffset())
	return n, nil
}

func (p *treeParser) children(n *node, depth int) error {
	for p.dec.More() {
		if n.kind == '{' {
			at := p.skip(int(p.dec.InputOffset()))
			tok, err := p.dec.Token()
			if err != nil {
				return p.tokenError(err)
			}
			key := tok.(string)
			if slices.Contains(n.keys, key) {
				return p.errorAt(at, "duplicate key "+strconv.Quote(key))
			}
			n.keys = append(n.keys, key)
		}
		kid, err := p.value(depth + 1)
		if err != nil {
			return err
		}
		n.kids = append(n.kids, kid)
	}
	if _, err := p.dec.Token(); err != nil {
		return p.tokenError(err)
	}
	return nil
}

// skip returns the offset of the first byte at or after i that is not
// whitespace or a separator.
func (p *treeParser) skip(i int) int {
	for i < len(p.src) && strings.IndexByte(" \t\r\n:,", p.src[i]) >= 0 {
		i++
	}
	return i
}

func (p *treeParser) tokenError(err error) error {
	var se *json.SyntaxError
	switch {
	case errors.As(err, &se):
		return p.errorAt(int(se.Offset), se.Error())
	case err == io.EOF || err == io.ErrUnexpectedEOF:
		return p.errorAt(len(p.src), "unexpected end of the document")
	}
	return p.errorAt(int(p.dec.InputOffset()), err.Error())
}

// errorAt reports a rejection at byte offset i, by line and column, both
// counted from 1, the column in bytes.
func (p *treeParser) errorAt(i int, msg string) error {
	i = min(i, len(p.src))
	line := 1 + bytes.Count(p.src[:i], []byte{'\n'})
	col := i - bytes.LastIndexByte(p.src[:i], '\n')
	return fmt.Errorf("openapi: %s:%d:%d: %s", p.uri, line, col, msg)
}

// pointerAt returns the node a JSON Pointer names under root, or nil.
func pointerAt(root *node, ptr string) *node {
	n := root
	for ptr != "" {
		if ptr[0] != '/' {
			return nil
		}
		tok := ptr[1:]
		if i := strings.IndexByte(tok, '/'); i >= 0 {
			tok, ptr = tok[:i], tok[i:]
		} else {
			ptr = ""
		}
		tok = unescapeToken(tok)
		switch n.kind {
		case '{':
			n = n.get(tok)
		case '[':
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(n.kids) || strconv.Itoa(i) != tok {
				return nil
			}
			n = &n.kids[i]
		default:
			return nil
		}
		if n == nil {
			return nil
		}
	}
	return n
}

var tokenEscaper = strings.NewReplacer("~", "~0", "/", "~1")
var tokenUnescaper = strings.NewReplacer("~1", "/", "~0", "~")

// escapeToken escapes a JSON Pointer reference token (RFC 6901 section 4).
func escapeToken(s string) string { return tokenEscaper.Replace(s) }

func unescapeToken(s string) string {
	if strings.IndexByte(s, '~') < 0 {
		return s
	}
	return tokenUnescaper.Replace(s)
}

// fragment percent-encodes a JSON Pointer as a URI fragment (RFC 6901
// section 6): every byte but those RFC 3986 allows in a fragment.
func fragment(ptr string) string {
	var b strings.Builder
	b.Grow(len(ptr) + 1)
	b.WriteByte('#')
	for i := 0; i < len(ptr); i++ {
		c := ptr[i]
		if unreserved(c) || strings.IndexByte("!$&'()*+,;=:@/?", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&15])
		}
	}
	return b.String()
}
