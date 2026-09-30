package openapi

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// maxDepth is how deeply a document may nest, the outermost value being
// level 1.
const maxDepth = 1000

// A node is one JSON value of a document, with its byte span in the source,
// so that Document and Schema.Raw return values exactly as written.
type node struct {
	kind       byte   // '{', '[', '"', '0' (a number), 't', 'f' or 'n'
	start, end int    // the value's bytes in the source
	key        string // the member name of an object's member
	text       string // a string's value, or a number as written
	kids       []node // an object's members or an array's items, in order
}

// get returns the member of an object named key, or nil.
func (n *node) get(key string) *node {
	if n == nil || n.kind != '{' {
		return nil
	}
	for i := range n.kids {
		if n.kids[i].key == key {
			return &n.kids[i]
		}
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

// strs returns the strings of array n, or nil.
func (n *node) strs() []string {
	if n == nil || n.kind != '[' {
		return nil
	}
	s := make([]string, 0, len(n.kids))
	for _, k := range n.kids {
		if k.kind == '"' {
			s = append(s, k.text)
		}
	}
	return s
}

// parseTree reads src, a JSON text (RFC 8259), as a document retrieved from
// uri. Strings without escapes share src's memory.
func parseTree(src, uri string) (node, error) {
	p := scanner{src: src, uri: uri}
	if !utf8.ValidString(src) {
		i := 0
		for {
			if r, size := utf8.DecodeRuneInString(src[i:]); r != utf8.RuneError || size != 1 {
				i += size
				continue
			}
			break
		}
		return node{}, p.errorAt(i, "invalid UTF-8")
	}
	p.space()
	root, err := p.value(1)
	if err != nil {
		return node{}, err
	}
	if p.space(); p.i < len(src) {
		return node{}, p.errorAt(p.i, "data after the document")
	}
	return root, nil
}

// A scanner reads a JSON text into a tree.
type scanner struct {
	src, uri string
	i        int    // the read position
	stack    []node // the members of the containers being read
	arena    []node // storage for the members of containers read
}

func (p *scanner) value(depth int) (node, error) {
	if depth > maxDepth {
		return node{}, p.errorAt(p.i, "nesting deeper than 1,000 levels")
	}
	n := node{start: p.i}
	if p.i == len(p.src) {
		return n, p.errorAt(p.i, "unexpected end of the document")
	}
	var err error
	switch c := p.src[p.i]; {
	case c == '{' || c == '[':
		return p.container(depth)
	case c == '"':
		n.kind = '"'
		n.text, err = p.string()
	case c == '-' || '0' <= c && c <= '9':
		n.kind = '0'
		err = p.number()
		n.text = p.src[n.start:p.i]
	case strings.HasPrefix(p.src[p.i:], "true"), strings.HasPrefix(p.src[p.i:], "null"):
		n.kind = c
		p.i += 4
	case strings.HasPrefix(p.src[p.i:], "false"):
		n.kind = c
		p.i += 5
	default:
		return n, p.errorAt(p.i, fmt.Sprintf("invalid character %q", c))
	}
	n.end = p.i
	return n, err
}

func (p *scanner) container(depth int) (node, error) {
	n := node{kind: p.src[p.i], start: p.i}
	end := n.kind + 2 // '}' or ']'
	p.i++
	base := len(p.stack)
	var names map[string]bool // for an object too large to search
	for first := true; ; first = false {
		p.space()
		if first && p.i < len(p.src) && p.src[p.i] == end {
			break
		}
		var key string
		if n.kind == '{' {
			at := p.i
			if p.i == len(p.src) || p.src[p.i] != '"' {
				return n, p.errorAt(p.i, "expected a member name")
			}
			var err error
			if key, err = p.string(); err != nil {
				return n, err
			}
			members := p.stack[base:]
			if names == nil && len(members) > 16 {
				names = make(map[string]bool, 2*len(members))
				for _, m := range members {
					names[m.key] = true
				}
			}
			if names[key] || names == nil && p.has(members, key) {
				return n, p.errorAt(at, "duplicate key "+strconv.Quote(key))
			}
			if names != nil {
				names[key] = true
			}
			if p.space(); p.i == len(p.src) || p.src[p.i] != ':' {
				return n, p.errorAt(p.i, "expected a colon")
			}
			p.i++
			p.space()
		}
		kid, err := p.value(depth + 1)
		if err != nil {
			return n, err
		}
		kid.key = key
		p.stack = append(p.stack, kid)
		if p.space(); p.i < len(p.src) && p.src[p.i] == end {
			break
		}
		if p.i == len(p.src) || p.src[p.i] != ',' {
			return n, p.errorAt(p.i, fmt.Sprintf("expected a comma or %q", end))
		}
		p.i++
	}
	p.i++
	n.end = p.i
	if k := len(p.stack) - base; k > 0 {
		if cap(p.arena)-len(p.arena) < k {
			p.arena = make([]node, 0, max(k, 2*cap(p.arena), 64))
		}
		n.kids = append(p.arena[len(p.arena):len(p.arena):len(p.arena)+k], p.stack[base:]...)
		p.arena = p.arena[:len(p.arena)+k]
		p.stack = p.stack[:base]
	}
	return n, nil
}

func (p *scanner) has(members []node, key string) bool {
	for i := range members {
		if members[i].key == key {
			return true
		}
	}
	return false
}

func (p *scanner) space() {
	for p.i < len(p.src) && (p.src[p.i] == ' ' || p.src[p.i] == '\t' || p.src[p.i] == '\n' || p.src[p.i] == '\r') {
		p.i++
	}
}

// string reads the string at p.i and returns its value.
func (p *scanner) string() (string, error) {
	start := p.i + 1
	for i := start; i < len(p.src); i++ {
		switch c := p.src[i]; {
		case c == '"':
			p.i = i + 1
			return p.src[start:i], nil
		case c == '\\':
			return p.unescape(start)
		case c < ' ':
			return "", p.errorAt(i, "control character in a string")
		}
	}
	return "", p.errorAt(len(p.src), "unexpected end of the document")
}

// unescape reads the string beginning at start, which holds an escape, as
// encoding/json does: an unpaired surrogate is U+FFFD.
func (p *scanner) unescape(start int) (string, error) {
	var b strings.Builder
	for i := start; i < len(p.src); {
		c := p.src[i]
		switch {
		case c == '"':
			p.i = i + 1
			return b.String(), nil
		case c < ' ':
			return "", p.errorAt(i, "control character in a string")
		case c != '\\':
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 == len(p.src) {
			break
		}
		if e := strings.IndexByte(`"\/bfnrt`, p.src[i+1]); e >= 0 {
			b.WriteByte("\"\\/\b\f\n\r\t"[e])
			i += 2
			continue
		}
		r, ok := hex4(p.src[i:])
		if !ok {
			return "", p.errorAt(i, "invalid escape in a string")
		}
		i += 6
		if utf16.IsSurrogate(r) {
			if r2, ok := hex4(p.src[i:]); ok && utf16.DecodeRune(r, r2) != utf8.RuneError {
				r, i = utf16.DecodeRune(r, r2), i+6
			} else {
				r = utf8.RuneError
			}
		}
		b.WriteRune(r)
	}
	return "", p.errorAt(len(p.src), "unexpected end of the document")
}

// hex4 reads the \uXXXX escape at the start of s.
func hex4(s string) (rune, bool) {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return 0, false
	}
	r, err := strconv.ParseUint(s[2:6], 16, 16)
	return rune(r), err == nil
}

// number reads the number at p.i.
func (p *scanner) number() error {
	s, i := p.src, p.i
	digits := func() bool {
		j := i
		for i < len(s) && '0' <= s[i] && s[i] <= '9' {
			i++
		}
		return i > j
	}
	if s[i] == '-' {
		i++
	}
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case !digits():
		return p.errorAt(i, "invalid number")
	}
	if i < len(s) && s[i] == '.' {
		if i++; !digits() {
			return p.errorAt(i, "invalid number")
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		if i++; i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if !digits() {
			return p.errorAt(i, "invalid number")
		}
	}
	p.i = i
	return nil
}

// errorAt reports a rejection at byte offset i, by line and column, both
// counted from 1, the column in bytes.
func (p *scanner) errorAt(i int, msg string) error {
	line := 1 + strings.Count(p.src[:i], "\n")
	col := i - strings.LastIndexByte(p.src[:i], '\n')
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
