package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// maxDepth is how deeply a document may nest, the outermost value being
// level 1.
const maxDepth = 1000

// A tree is a parsed JSON text: its source, and one node per value in
// document order, each container followed by its members. Nodes hold only
// offsets; names, strings and numbers are read from the source when used.
type tree struct {
	src   string
	nodes []node

	mu     sync.Mutex
	sorted map[int32][]int32 // members of large objects by name, built on first lookup
}

// A node is one JSON value: its bytes in the source, and the index of the
// node after its last descendant.
type node struct {
	start, end, next uint32
}

// A value is one value of a tree. The zero value is absent.
type value struct {
	t *tree
	i int32
}

func (v value) ok() bool { return v.t != nil }

// hasMembers reports whether v is an object or array that is not empty.
func (v value) hasMembers() bool {
	k := v.kind()
	return (k == '{' || k == '[') && v.t.nodes[v.i].next > uint32(v.i)+1
}

// kind returns the value's first byte, '0' for a number, or 0 when absent.
func (v value) kind() byte {
	if v.t == nil {
		return 0
	}
	c := v.t.src[v.t.nodes[v.i].start]
	if c == '-' || '0' <= c && c <= '9' {
		return '0'
	}
	return c
}

// raw returns the value exactly as written.
func (v value) raw() string {
	n := v.t.nodes[v.i]
	return v.t.src[n.start:n.end]
}

// text returns a string's value, or any other value as written.
func (v value) text() string {
	if v.kind() != '"' {
		return v.raw()
	}
	return jsonString(v.raw())
}

// str returns the string member named key, or "".
func (v value) str(key string) string { return v.get(key).string() }

// string returns a string's value, or "" for any other value.
func (v value) string() string {
	if v.kind() == '"' {
		return v.text()
	}
	return ""
}

// flag reports whether the member named key is true.
func (v value) flag(key string) bool { return v.get(key).kind() == 't' }

// strs returns the strings of an array, or nil.
func (v value) strs() []string {
	if v.kind() != '[' {
		return nil
	}
	var s []string
	for _, item := range v.members() {
		if item.kind() == '"' {
			s = append(s, item.text())
		}
	}
	return s
}

// members returns an object's members by name, or an array's items with
// empty names, in order.
func (v value) members() iter.Seq2[string, value] {
	return func(yield func(string, value) bool) {
		k := v.kind()
		if k != '{' && k != '[' {
			return
		}
		t := v.t
		for c := v.i + 1; uint32(c) < t.nodes[v.i].next; c = int32(t.nodes[c].next) {
			var name string
			if k == '{' {
				name = t.name(c)
			}
			if !yield(name, value{t, c}) {
				return
			}
		}
	}
}

// get returns the member of an object named key, or an absent value.
func (v value) get(key string) value {
	if v.kind() != '{' {
		return value{}
	}
	t, n := v.t, 0
	for c := v.i + 1; uint32(c) < t.nodes[v.i].next; c = int32(t.nodes[c].next) {
		if n++; n > 16 { // many members: search them sorted, if they can be
			if sorted := t.sortedMembers(v.i); sorted != nil {
				j, found := slices.BinarySearchFunc(sorted, key, func(c int32, key string) int {
					return strings.Compare(t.rawName(c), key)
				})
				if found {
					return value{t, sorted[j]}
				}
				return value{}
			}
			n = -1 << 31
		}
		if raw := t.rawName(c); raw == key || strings.IndexByte(raw, '\\') >= 0 && t.name(c) == key {
			return value{t, c}
		}
	}
	return value{}
}

// sortedMembers returns the members of object i sorted by name, when it has
// many and none of their names is escaped, or nil.
func (t *tree) sortedMembers(i int32) []int32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.sorted[i]; ok {
		return s
	}
	var s []int32
	for c := i + 1; uint32(c) < t.nodes[i].next; c = int32(t.nodes[c].next) {
		if strings.IndexByte(t.rawName(c), '\\') >= 0 {
			s = nil
			break
		}
		s = append(s, c)
	}
	if len(s) > 16 {
		slices.SortFunc(s, func(a, b int32) int { return strings.Compare(t.rawName(a), t.rawName(b)) })
	} else {
		s = nil
	}
	if t.sorted == nil {
		t.sorted = map[int32][]int32{}
	}
	t.sorted[i] = s
	return s
}

// rawName returns the name of member c as written, without its quotes: the
// string before the colon that precedes the member's value.
func (t *tree) rawName(c int32) string {
	s, j := t.src, int(t.nodes[c].start)-1
	for s[j] != ':' {
		j--
	}
	for j--; s[j] != '"'; j-- {
	}
	end := j
	for j--; ; j-- {
		if s[j] == '"' {
			k := j
			for k > 0 && s[k-1] == '\\' {
				k--
			}
			if (j-k)%2 == 0 { // not an escaped quote
				return s[j+1 : end]
			}
		}
	}
}

// name returns the name of member c.
func (t *tree) name(c int32) string {
	raw := t.rawName(c)
	if strings.IndexByte(raw, '\\') < 0 {
		return raw
	}
	return jsonString(`"` + raw + `"`)
}

// jsonString returns the value of s, a valid JSON string, decoded as
// encoding/json decodes it.
func jsonString(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s[1 : len(s)-1]
	}
	var v string
	json.Unmarshal([]byte(s), &v)
	return v
}

// at returns the value a JSON Pointer names under v, or an absent value.
func (v value) at(ptr string) value {
	for ptr != "" && v.ok() {
		if ptr[0] != '/' {
			return value{}
		}
		tok := ptr[1:]
		if i := strings.IndexByte(tok, '/'); i >= 0 {
			tok, ptr = tok[:i], tok[i:]
		} else {
			ptr = ""
		}
		tok, ok := unescapeToken(tok)
		switch {
		case !ok:
			return value{}
		case v.kind() == '{':
			v = v.get(tok)
		case v.kind() == '[':
			n, err := strconv.Atoi(tok)
			if err != nil || n < 0 || strconv.Itoa(n) != tok {
				return value{}
			}
			w := value{}
			for _, item := range v.members() {
				if n == 0 {
					w = item
					break
				}
				n--
			}
			v = w
		default:
			return value{}
		}
	}
	return v
}

// escapeToken escapes a JSON Pointer reference token (RFC 6901 section 4).
func escapeToken(s string) string {
	if strings.IndexAny(s, "~/") < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '~':
			b.WriteString("~0")
		case '/':
			b.WriteString("~1")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// unescapeToken reads a reference token, reporting whether every "~" is
// followed by "0" or "1".
func unescapeToken(s string) (string, bool) {
	if strings.IndexByte(s, '~') < 0 {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '~' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 == len(s) || s[i+1] != '0' && s[i+1] != '1' {
			return "", false
		}
		i++
		b.WriteByte("~/"[s[i]-'0'])
	}
	return b.String(), true
}

// parseTree reads src, a JSON text (RFC 8259), as the document at uri,
// stopping when ctx is done.
func parseTree(ctx context.Context, src, uri string) (*tree, error) {
	p := scanner{ctx: ctx, src: src, uri: uri}
	if len(src) >= math.MaxUint32 {
		return nil, p.errorAt(0, "the document is 4 GiB or larger")
	}
	if !utf8.ValidString(src) {
		i := 0
		for r, n := utf8.DecodeRuneInString(src); r != utf8.RuneError || n != 1; r, n = utf8.DecodeRuneInString(src[i:]) {
			i += n
		}
		return nil, p.errorAt(i, "invalid UTF-8")
	}
	// Each value but the outermost follows a "{", "[" or "," in its
	// container, so these bound the number of nodes.
	p.nodes = make([]node, 0, 1+strings.Count(src, "{")+strings.Count(src, "[")+strings.Count(src, ","))
	p.space()
	if err := p.value(1); err != nil {
		return nil, err
	}
	if p.space(); p.i < len(src) {
		return nil, p.errorAt(p.i, "data after the document")
	}
	return &tree{src: src, nodes: p.nodes}, nil
}

// A scanner reads a JSON text into a tree's nodes.
type scanner struct {
	ctx      context.Context
	src, uri string
	i        int      // the read position
	nodes    []node   // the nodes read
	names    []string // the member names of the objects being read
}

func (p *scanner) value(depth int) error {
	if depth > maxDepth {
		return p.errorAt(p.i, "nesting deeper than 1,000 levels")
	}
	if len(p.nodes)&0xffff == 0xffff && p.ctx.Err() != nil {
		return fmt.Errorf("openapi: %s: %w", p.uri, p.ctx.Err())
	}
	if p.i == len(p.src) {
		return p.errorAt(p.i, "unexpected end of the document")
	}
	at := len(p.nodes)
	p.nodes = append(p.nodes, node{start: uint32(p.i)})
	var err error
	switch c, rest := p.src[p.i], p.src[p.i:]; {
	case c == '{' || c == '[':
		err = p.container(depth)
	case c == '"':
		err = p.string()
	case c == '-' || '0' <= c && c <= '9':
		err = p.number()
	case strings.HasPrefix(rest, "true"), strings.HasPrefix(rest, "null"):
		p.i += 4
	case strings.HasPrefix(rest, "false"):
		p.i += 5
	default:
		err = p.errorAt(p.i, fmt.Sprintf("invalid character %q", c))
	}
	p.nodes[at].end, p.nodes[at].next = uint32(p.i), uint32(len(p.nodes))
	return err
}

func (p *scanner) container(depth int) error {
	end := p.src[p.i] + 2 // '}' or ']'
	p.i++
	base := len(p.names)
	defer func() { p.names = p.names[:base] }()
	var many map[string]bool // the member names of an object too large to search
	for first := true; ; first = false {
		p.space()
		if first && p.i < len(p.src) && p.src[p.i] == end {
			break
		}
		if end == '}' {
			at := p.i
			if p.i == len(p.src) || p.src[p.i] != '"' {
				return p.errorAt(p.i, "expected a member name")
			}
			if err := p.string(); err != nil {
				return err
			}
			name := jsonString(p.src[at:p.i])
			if many == nil && len(p.names)-base == 16 {
				many = make(map[string]bool, 32)
				for _, n := range p.names[base:] {
					many[n] = true
				}
			}
			if many[name] || many == nil && slices.Contains(p.names[base:], name) {
				return p.errorAt(at, "duplicate key "+strconv.Quote(name))
			}
			if many != nil {
				many[name] = true
			} else {
				p.names = append(p.names, name)
			}
			if p.space(); p.i == len(p.src) || p.src[p.i] != ':' {
				return p.errorAt(p.i, "expected a colon")
			}
			p.i++
			p.space()
		}
		if err := p.value(depth + 1); err != nil {
			return err
		}
		if p.space(); p.i < len(p.src) && p.src[p.i] == end {
			break
		}
		if p.i == len(p.src) || p.src[p.i] != ',' {
			return p.errorAt(p.i, fmt.Sprintf("expected a comma or %q", end))
		}
		p.i++
	}
	p.i++
	return nil
}

func (p *scanner) space() {
	for p.i < len(p.src) && (p.src[p.i] == ' ' || p.src[p.i] == '\t' || p.src[p.i] == '\n' || p.src[p.i] == '\r') {
		p.i++
	}
}

// string reads the string at p.i, checking its escapes.
func (p *scanner) string() error {
	for i := p.i + 1; i < len(p.src); i++ {
		switch c := p.src[i]; {
		case c == '"':
			p.i = i + 1
			return nil
		case c < ' ':
			return p.errorAt(i, "control character in a string")
		case c != '\\':
		case i+1 < len(p.src) && strings.IndexByte(`"\/bfnrt`, p.src[i+1]) >= 0:
			i++
		case i+5 < len(p.src) && p.src[i+1] == 'u' && hexDigit(p.src[i+2]) && hexDigit(p.src[i+3]) && hexDigit(p.src[i+4]) && hexDigit(p.src[i+5]):
			i += 5
		default:
			return p.errorAt(i, "invalid escape in a string")
		}
	}
	return p.errorAt(len(p.src), "unexpected end of the document")
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
