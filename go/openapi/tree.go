package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// maxDepth is how deeply a document may nest, the outermost value being
// level 1.
const maxDepth = 1000

// many is how many members a container may have before its lookups use an
// index rather than a walk.
const many = 16

// A tree is a loaded document, parsed: the URI it was retrieved from, its
// JSON text, and one node per value in document order, each container
// followed by its members. Nodes hold only offsets; names, strings and
// numbers are read from the text when used. What lookups derive is kept,
// each fact once: the index of a container with many members, and the value
// of an escaped string.
type tree struct {
	edition   int                    // 20, 30, 31 or 32; versionless fragments inherit the entry model
	refbase   *url.URL               // reference base, including a 3.2 $self
	uri       string                 // the URI the document was retrieved from
	base      *url.URL               // uri, parsed
	dir       string                 // uri up to its last slash, when it reaches others and has a path and no query
	canonical atomic.Pointer[string] // the URI of its directory, as net/url writes it (see canonicalDir)
	src       string
	nodes     []node
	escapes   []uint32 // the offsets of the strings written with an escape, in order
	off       int32    // the number of its first node among the nodes of every document loaded

	// What discovery reads it for: a reference that may reach another
	// document (one not to a fragment of itself, or a discriminator mapping
	// value); an identifier ($id, $anchor or $dynamicAnchor). And where each
	// reference discovery read leads, by node, but one to a fragment of the
	// document itself.
	reaches, declares bool
	editionRefs       bool
	dialects          bool // a $schema occurs; distinct from identifier declarations
	openAPI           bool // the root carries an OpenAPI or Swagger version field
	located           map[int32]location
	schemaRoots       uint16            // root object contexts reached in a versionless fragment
	schemaContexts    []uint32          // discovery bitmap, retained only for unscoped non-root contexts
	schemaSeeds       []item            // fragment contexts whose identifier scope needs a physical path
	schemaIntents     map[string]uint16 // authored fragments whose pure-local context is deferred
	schemasOnce       sync.Once
	schemas           *schemaGraph
	resourceBases     map[int32]*url.URL // $id resolutions shared by discovery and inspection

	// Read without a lock, each computed unlocked and kept as first stored.
	indexes sync.Map                 // container to []int32: an object's members sorted by name, an array's items
	decoded []atomic.Pointer[string] // the value of each string of escapes, once read
}

// A node is one JSON value: the offset of its first byte, the index of the
// node after its last descendant, and for an object member the offset of
// its name's opening quote. The end of a value is found from these when a
// caller needs the value as written.
type node struct {
	start, next, name uint32
}

// A value is one value of a tree. The zero value is absent.
type value struct {
	t *tree
	i int32
}

func (v value) ok() bool { return v.t != nil }

// id returns the value's number among the nodes of every document loaded,
// by which what it compiles to is kept.
func (v value) id() int32 { return v.t.off + v.i }

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
	return v.t.src[v.t.nodes[v.i].start:v.t.end(v.i)]
}

// end returns the offset just past value i. A container ends at the bracket
// after its last member, whitespace alone lying between.
func (t *tree) end(i int32) int {
	s, j := t.src, int(t.nodes[i].start)
	switch c := s[j]; c {
	case '"':
		if t.escaped(uint32(j)) {
			return closingQuote(s, j) + 1
		}
		return j + 1 + strings.IndexByte(s[j+1:], '"') + 1
	case '{', '[':
		if t.nodes[i].next > uint32(i)+1 {
			last := i + 1
			for t.nodes[last].next < t.nodes[i].next {
				last = int32(t.nodes[last].next)
			}
			j = t.end(last) - 1
		}
		return j + 1 + strings.IndexByte(s[j+1:], c+2) + 1 // '}' or ']'
	case 't', 'n':
		return j + 4
	case 'f':
		return j + 5
	}
	for j++; j < len(s) && strings.IndexByte("0123456789+-.eE", s[j]) >= 0; j++ {
	}
	return j
}

// closingQuote returns the offset of the quote that closes the string whose
// opening quote is at q in s: the next quote not escaped by an odd run of
// backslashes.
func closingQuote(s string, q int) int {
	for {
		q += 1 + strings.IndexByte(s[q+1:], '"')
		k := q
		for s[k-1] == '\\' {
			k--
		}
		if (q-k)%2 == 0 {
			return q
		}
	}
}

// escaped reports whether the string whose opening quote is at q is
// written with an escape.
func (t *tree) escaped(q uint32) bool {
	_, found := slices.BinarySearch(t.escapes, q)
	return found
}

// str returns the value of the string whose opening quote is at q, decoding
// an escaped one once.
func (t *tree) str(q uint32) string {
	i, escaped := slices.BinarySearch(t.escapes, q)
	if !escaped {
		s := t.src[q+1:]
		return s[:strings.IndexByte(s, '"')]
	}
	if s := t.decoded[i].Load(); s != nil {
		return *s
	}
	s := jsonString(t.src[q : closingQuote(t.src, int(q))+1])
	t.decoded[i].CompareAndSwap(nil, &s)
	return s
}

// compareName compares the name of member c with key, reading an unescaped
// name no further than key needs.
func (t *tree) compareName(c int32, key string) int {
	q := t.nodes[c].name
	if t.escaped(q) {
		return strings.Compare(t.str(q), key)
	}
	name := t.src[q+1 : min(len(t.src), int(q)+2+len(key))]
	if i := strings.IndexByte(name, '"'); i >= 0 {
		name = name[:i]
	}
	return strings.Compare(name, key)
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

// text returns a string's value.
func (v value) text() string { return v.t.str(v.t.nodes[v.i].start) }

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

// name returns the name of member c.
func (t *tree) name(c int32) string { return t.str(t.nodes[c].name) }

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

// get returns the member of an object named key, compared as decoded, or an
// absent value.
func (v value) get(key string) value {
	if v.kind() != '{' {
		return value{}
	}
	t, n := v.t, 0
	for c := v.i + 1; uint32(c) < t.nodes[v.i].next; c = int32(t.nodes[c].next) {
		if n++; n > many {
			sorted := t.index(v.i)
			if j, found := slices.BinarySearchFunc(sorted, key, t.compareName); found {
				return value{t, sorted[j]}
			}
			return value{}
		}
		if t.compareName(c, key) == 0 {
			return value{t, c}
		}
	}
	return value{}
}

// item returns the nth item of an array, or an absent value.
func (v value) item(n int) value {
	t, k := v.t, 0
	for c := v.i + 1; uint32(c) < t.nodes[v.i].next; c = int32(t.nodes[c].next) {
		if k == n {
			return value{t, c}
		}
		if k++; k > many {
			if items := t.index(v.i); n < len(items) {
				return value{t, items[n]}
			}
			return value{}
		}
	}
	return value{}
}

// index returns the members of container i, an object's sorted by name and
// an array's in order, building the index once.
func (t *tree) index(i int32) []int32 {
	if s, ok := t.indexes.Load(i); ok {
		return s.([]int32)
	}
	var s []int32
	for c := i + 1; uint32(c) < t.nodes[i].next; c = int32(t.nodes[c].next) {
		s = append(s, c)
	}
	if t.src[t.nodes[i].start] == '{' {
		type member struct {
			name string
			c    int32
		}
		m := make([]member, len(s))
		for j, c := range s {
			m[j] = member{t.name(c), c}
		}
		slices.SortFunc(m, func(a, b member) int { return strings.Compare(a.name, b.name) })
		for j := range m {
			s[j] = m[j].c
		}
	}
	kept, _ := t.indexes.LoadOrStore(i, s)
	return kept.([]int32)
}

// at returns the value a JSON Pointer names under v, or an absent value.
func (v value) at(ptr string) value {
	for ptr != "" && v.ok() {
		tok, rest, ok := nextToken(ptr)
		if !ok {
			return value{}
		}
		v, ptr = v.step(tok), rest
	}
	return v
}

// nextToken splits the first reference token, still escaped, from a JSON
// Pointer, reporting false when ptr does not begin with "/".
func nextToken(ptr string) (tok, rest string, ok bool) {
	if ptr[0] != '/' {
		return "", "", false
	}
	if i := strings.IndexByte(ptr[1:], '/'); i >= 0 {
		return ptr[1 : 1+i], ptr[1+i:], true
	}
	return ptr[1:], "", true
}

// step returns the member of v an escaped reference token names, or an
// absent value.
func (v value) step(tok string) value {
	tok, ok := unescapeToken(tok)
	switch {
	case !ok:
	case v.kind() == '{':
		return v.get(tok)
	case v.kind() == '[':
		n, err := strconv.Atoi(tok)
		if err != nil || tok[0] < '0' || tok[0] > '9' || tok[0] == '0' && len(tok) > 1 { // digits, without a leading zero
			return value{}
		}
		return v.item(n)
	}
	return value{}
}

var (
	tokenEscaper   = strings.NewReplacer("~", "~0", "/", "~1")
	tokenUnescaper = strings.NewReplacer("~1", "/", "~0", "~")
)

// escapeToken escapes a JSON Pointer reference token (RFC 6901 section 4).
func escapeToken(s string) string { return tokenEscaper.Replace(s) }

// unescapeToken reads a reference token, reporting whether every "~" is
// followed by "0" or "1".
func unescapeToken(s string) (string, bool) {
	if strings.IndexByte(s, '~') < 0 {
		return s, true
	}
	return tokenUnescaper.Replace(s), strings.Count(s, "~") == strings.Count(s, "~0")+strings.Count(s, "~1")
}

// parseTree reads src, a JSON text (RFC 8259) in UTF-8 that took unit bytes
// per code unit as retrieved, as the document at uri, stopping when ctx is
// done.
func parseTree(ctx context.Context, src, uri string, unit int) (*tree, error) {
	var names [many]string
	p := scanner{ctx: ctx, src: src, uri: uri, unit: unit, names: names[:0]}
	if uint64(len(src)) >= math.MaxUint32 {
		return nil, p.errorAt(0, "the document is 4 GiB or larger")
	}
	// Each value but the outermost follows a "{", "[" or "," in its
	// container, and takes two bytes with its separator, so both bound the
	// number of nodes. The first counts those bytes inside strings too; what
	// it reserved beyond an eighth of the nodes is given back.
	n := 1 + strings.Count(src, "{") + strings.Count(src, "[") + strings.Count(src, ",")
	p.nodes = make([]node, 0, min(n, len(src)/2+1))
	p.space()
	if err := p.value(1, 0); err != nil {
		return nil, err
	}
	if p.space(); p.i < len(src) {
		return nil, p.errorAt(p.i, "data after the document")
	}
	if cap(p.nodes)-len(p.nodes) > len(p.nodes)/8 {
		p.nodes = slices.Clone(p.nodes)
	}
	return &tree{src: src, nodes: p.nodes, escapes: p.escapes, decoded: make([]atomic.Pointer[string], len(p.escapes)), reaches: p.reaches, declares: p.declares, editionRefs: p.editionRefs, dialects: p.dialects}, nil
}

// A scanner reads a JSON text into a tree's nodes.
type scanner struct {
	ctx               context.Context
	src, uri          string
	unit              int      // the bytes a code unit took as retrieved
	i                 int      // the read position
	nodes             []node   // the nodes read
	escapes           []uint32 // the offsets of the strings read with an escape
	names             []string // the member names of the objects being read
	reaches, declares bool     // see tree
	editionRefs       bool
	dialects          bool // a $schema occurs; distinct from identifier declarations
}

// note records what a member named name, whose value's text begins v, tells
// discovery (see tree).
func note(name, v string, reaches, declares, editionRefs, dialects *bool) {
	switch name {
	case "$ref", "$dynamicRef":
		*reaches = *reaches || !strings.HasPrefix(v, `"#`)
	case "mapping":
		*reaches = true
	case "defaultMapping", "security":
		*editionRefs = true
	case "$id", "$anchor", "$dynamicAnchor", "$self":
		*declares = true
	case "$schema":
		*dialects = true
	}
}

// value reads the value at p.i, the member named at offset name, if not 0.
func (p *scanner) value(depth int, name uint32) error {
	if depth > maxDepth {
		return p.errorAt(p.i, "nesting deeper than 1,000 levels")
	}
	if len(p.nodes)&0xffff == 0xffff && p.ctx.Err() != nil {
		return fmt.Errorf("openapi: %s: %w", safeURI(p.uri), p.ctx.Err())
	}
	if p.i == len(p.src) {
		return p.errorAt(p.i, "unexpected end of the document")
	}
	at := len(p.nodes)
	p.nodes = append(p.nodes, node{start: uint32(p.i), name: name})
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
	p.nodes[at].next = uint32(len(p.nodes))
	return err
}

func (p *scanner) container(depth int) error {
	end := p.src[p.i] + 2 // '}' or ']'
	p.i++
	base := len(p.names)
	defer func() { p.names = p.names[:base] }()
	var seen map[string]bool // the member names of an object with many
	for first := true; ; first = false {
		p.space()
		if first && p.i < len(p.src) && p.src[p.i] == end {
			break
		}
		at := 0 // the member's name, in an object
		if end == '}' {
			at = p.i
			if p.i == len(p.src) || p.src[p.i] != '"' {
				return p.errorAt(p.i, "expected a member name")
			}
			if err := p.string(); err != nil {
				return err
			}
			name := jsonString(p.src[at:p.i])
			if seen == nil && len(p.names)-base == many {
				seen = make(map[string]bool, 2*many)
				for _, n := range p.names[base:] {
					seen[n] = true
				}
			}
			if seen != nil && seen[name] || seen == nil && slices.Contains(p.names[base:], name) {
				return p.errorAt(at, "duplicate key "+strconv.Quote(name))
			}
			if seen != nil {
				seen[name] = true
			} else {
				p.names = append(p.names, name)
			}
			if p.space(); p.i == len(p.src) || p.src[p.i] != ':' {
				return p.errorAt(p.i, "expected a colon")
			}
			p.i++
			if p.space(); len(name) > 1 && (name[0] == '$' || name[0] == 'm' || name[0] == 'd' || name[0] == 's') {
				note(name, p.src[p.i:], &p.reaches, &p.declares, &p.editionRefs, &p.dialects)
			}
		}
		if err := p.value(depth+1, uint32(at)); err != nil {
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
	src, escaped := p.src, false
	for i := p.i + 1; i < len(src); i++ {
		switch c := src[i]; {
		case c == '"':
			if escaped {
				p.escapes = append(p.escapes, uint32(p.i))
			}
			p.i = i + 1
			return nil
		case c < ' ':
			return p.errorAt(i, "control character in a string")
		case c != '\\':
			continue
		case i+1 < len(src) && strings.IndexByte(`"\/bfnrt`, src[i+1]) >= 0:
			i++
		case i+5 < len(src) && src[i+1] == 'u' && hexDigit(src[i+2]) && hexDigit(src[i+3]) && hexDigit(src[i+4]) && hexDigit(src[i+5]):
			i += 5
		default:
			return p.errorAt(i, "invalid escape in a string")
		}
		escaped = true
	}
	return p.errorAt(len(src), "unexpected end of the document")
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

func (p *scanner) errorAt(i int, msg string) error { return rejection(p.uri, p.src, i, p.unit, msg) }
