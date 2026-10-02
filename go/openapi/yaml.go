package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// The YAML step. A YAML stream's document becomes the client's JSON text and
// tree, as the Loader says: a YAML 1.2 parser reads the stream into nodes
// (see yamlNode), and yamlTree reads plain scalars under the Core schema,
// checks keys and tags, and expands aliases within the bounds, recording
// where each value starts in the stream for a rejection.

// parseYAML reads src, a YAML stream in UTF-8 that took unit bytes per code
// unit, and size bytes in all, as retrieved, as the document at uri. A %YAML
// directive for a major version other than 1 rejects it (YAML 1.2.2 section
// 6.8.1), and one for 1.x becomes a comment, so the stream is read as YAML
// 1.2. The parser that reads the stream into the nodes yamlTree takes, with
// the stream's second document if it has one, which rejects it, awaits its
// approval.
func parseYAML(ctx context.Context, src, uri string, unit, size int) (*tree, error) {
	for i := 0; i < len(src); {
		line, _, _ := strings.Cut(src[i:], "\n")
		if v, ok := strings.CutPrefix(line, "%YAML"); ok && v != "" && (v[0] == ' ' || v[0] == '\t') {
			v, _, _ = strings.Cut(v, "#")
			if major, _, _ := strings.Cut(strings.TrimSpace(v), "."); major != "1" {
				return nil, rejection(uri, src, i, unit, "a %YAML directive for a version other than 1.x")
			}
			src = src[:i] + "#" + src[i+1:]
		} else if s := strings.TrimLeft(line, " \t\r"); line != "" && line[0] != '%' && s != "" && s[0] != '#' {
			break // the directives end where the document begins
		}
		i += len(line) + 1
	}
	return nil, fmt.Errorf("openapi: %s: YAML documents are not implemented yet: %w", uri, errors.ErrUnsupported)
}

// A yamlNode is a node of a YAML document as a parser gives it, with the
// fields of go.yaml.in/yaml/v3's Node: its kind; its style, which says
// whether its tag was written; its tag; a scalar's value; the node an alias
// names; a collection's content, a mapping's keys and values alternating;
// and where it starts, by line and column counted from 1, the column in
// characters.
type yamlNode struct {
	Kind         yamlKind
	Style        yamlStyle
	Tag, Value   string
	Alias        *yamlNode
	Content      []*yamlNode
	Line, Column int
}

type yamlKind uint32

const (
	yamlDocumentNode yamlKind = 1 << iota
	yamlSequenceNode
	yamlMappingNode
	yamlScalarNode
	yamlAliasNode
)

type yamlStyle uint32

const (
	yamlTagged yamlStyle = 1 << iota
	yamlDoubleQuoted
	yamlSingleQuoted
	yamlLiteral
	yamlFolded
)

// A yamlWriter writes a YAML document's nodes as the client's JSON text and
// tree.
type yamlWriter struct {
	ctx                context.Context
	uri, src           string
	unit               int
	b                  strings.Builder
	nodes              []node
	escapes            []uint32
	names              []string // the keys of the mappings being written
	named              map[*yamlNode]*written
	added, addedBytes  int // what aliases added
	maxAdded, maxBytes int
	reaches, declares  bool // see tree
}

// A written is what a node an alias names was written as: its nodes, its
// text, and how many levels it nests, once written.
type written struct {
	first, next int32
	start, end  int
	levels      int
	done        bool
}

// yamlTree writes root, the root node of the YAML document read from src (see
// parseYAML), as the client's JSON text and tree.
func yamlTree(ctx context.Context, root *yamlNode, src, uri string, unit, size int) (*tree, error) {
	w := &yamlWriter{ctx: ctx, uri: uri, src: src, unit: unit, named: map[*yamlNode]*written{}}
	own := w.count(root)
	w.maxAdded, w.maxBytes = min(1_000_000, 100*own), 100*size
	if _, err := w.value(root, 1, 0); err != nil {
		return nil, err
	}
	return &tree{src: w.b.String(), nodes: w.nodes, escapes: w.escapes, decoded: make([]atomic.Pointer[string], len(w.escapes)),
		reaches: w.reaches, declares: w.declares}, nil
}

// count returns the nodes of n, each alias one, noting the nodes aliases
// name.
func (w *yamlWriter) count(n *yamlNode) int {
	if n.Kind == yamlAliasNode && n.Alias != nil {
		w.named[n.Alias] = &written{}
	}
	c := 1
	for _, m := range n.Content {
		c += w.count(m)
	}
	return c
}

// value writes n, a value at level depth, the member whose name's opening
// quote is at offset name if not 0, and returns how many levels it nests.
func (w *yamlWriter) value(n *yamlNode, depth int, name uint32) (int, error) {
	switch {
	case depth > maxDepth:
		return 0, w.reject(n, "nesting deeper than 1,000 levels")
	case len(w.nodes)&0xffff == 0xffff && w.ctx.Err() != nil:
		return 0, fmt.Errorf("openapi: %s: %w", w.uri, w.ctx.Err())
	case n.Kind == yamlAliasNode:
		return w.alias(n, depth, name)
	}
	at, levels := len(w.nodes), 1
	a := w.named[n]
	if a != nil {
		a.first, a.start = int32(at), w.b.Len()
	}
	w.nodes = append(w.nodes, node{start: uint32(w.b.Len()), name: name})
	switch n.Kind {
	case yamlScalarNode:
		s, quoted, err := yamlScalar(n)
		switch {
		case err != nil:
			return 0, w.reject(n, err.Error())
		case quoted:
			w.quote(s)
		default:
			w.b.WriteString(s)
		}
	case yamlMappingNode, yamlSequenceNode:
		tag, write := "!!seq", w.sequence
		if n.Kind == yamlMappingNode {
			tag, write = "!!map", w.mapping
		}
		if n.Style&yamlTagged != 0 && n.Tag != tag {
			return 0, w.reject(n, "the tag "+n.Tag+" is outside the Core schema")
		}
		var err error
		if levels, err = write(n, depth); err != nil {
			return 0, err
		}
	default:
		return 0, w.reject(n, "not a YAML value")
	}
	w.nodes[at].next = uint32(len(w.nodes))
	if a != nil {
		a.next, a.end, a.levels, a.done = int32(len(w.nodes)), w.b.Len(), levels, true
	}
	return levels, nil
}

// sequence writes the sequence n, at level depth, returning how many levels
// it nests.
func (w *yamlWriter) sequence(n *yamlNode, depth int) (int, error) {
	levels := 1
	w.b.WriteByte('[')
	for i, m := range n.Content {
		if i > 0 {
			w.b.WriteByte(',')
		}
		l, err := w.value(m, depth+1, 0)
		if err != nil {
			return 0, err
		}
		levels = max(levels, l+1)
	}
	w.b.WriteByte(']')
	return levels, nil
}

// mapping writes the mapping n, at level depth, each key as the string it
// spells, returning how many levels it nests.
func (w *yamlWriter) mapping(n *yamlNode, depth int) (int, error) {
	levels, base := 1, len(w.names)
	defer func() { w.names = w.names[:base] }()
	var seen map[string]bool // the keys of a mapping with many
	w.b.WriteByte('{')
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		key := k
		if key.Kind == yamlAliasNode && key.Alias != nil {
			if key = key.Alias; key.Kind == yamlScalarNode {
				if err := w.add(k, 0, len(key.Value)); err != nil {
					return 0, err
				}
			}
		}
		if key.Kind != yamlScalarNode {
			return 0, w.reject(k, "a key that is not a scalar")
		}
		if seen == nil && len(w.names)-base == many {
			seen = make(map[string]bool, 2*many)
			for _, s := range w.names[base:] {
				seen[s] = true
			}
		}
		if seen[key.Value] || seen == nil && slices.Contains(w.names[base:], key.Value) {
			return 0, w.reject(k, "duplicate key "+strconv.Quote(key.Value))
		}
		if seen != nil {
			seen[key.Value] = true
		} else {
			w.names = append(w.names, key.Value)
		}
		if i > 0 {
			w.b.WriteByte(',')
		}
		at := w.b.Len()
		w.quote(key.Value)
		w.b.WriteByte(':')
		start := w.b.Len()
		l, err := w.value(v, depth+1, uint32(at))
		if err != nil {
			return 0, err
		}
		if levels = max(levels, l+1); len(key.Value) > 1 && (key.Value[0] == '$' || key.Value[0] == 'm') {
			note(key.Value, w.b.String()[start:], &w.reaches, &w.declares)
		}
	}
	w.b.WriteByte('}')
	return levels, nil
}

// alias writes again what the node the alias n names was written as, at
// level depth, the member whose name is at name if not 0, unless the alias
// is inside that node, or the aliases would add more than the bounds allow.
// A scalar named only as a key is written first.
func (w *yamlWriter) alias(n *yamlNode, depth int, name uint32) (int, error) {
	a := w.named[n.Alias]
	key := a != nil && !a.done && n.Alias.Kind == yamlScalarNode
	switch {
	case key:
		if _, err := w.value(n.Alias, depth, name); err != nil {
			return 0, err
		}
	case a == nil || !a.done:
		return 0, w.reject(n, "an alias inside the node it names")
	case depth+a.levels-1 > maxDepth:
		return 0, w.reject(n, "nesting deeper than 1,000 levels")
	}
	if err := w.add(n, int(a.next-a.first), a.end-a.start); err != nil || key {
		return 1, err
	}
	text, shift, at := w.b.String(), uint32(w.b.Len()-a.start), int32(len(w.nodes))
	w.b.WriteString(text[a.start:a.end])
	for _, m := range w.nodes[a.first:a.next] {
		if m.start += shift; m.name != 0 {
			m.name += shift
		}
		m.next += uint32(at - a.first)
		w.nodes = append(w.nodes, m)
	}
	w.nodes[at].name = name
	i, _ := slices.BinarySearch(w.escapes, uint32(a.start))
	j, _ := slices.BinarySearch(w.escapes, uint32(a.end))
	for _, e := range w.escapes[i:j] {
		w.escapes = append(w.escapes, e+shift)
	}
	return a.levels, nil
}

// add counts what the alias n adds, rejecting it when the aliases would add
// more than the bounds allow.
func (w *yamlWriter) add(n *yamlNode, nodes, bytes int) error {
	w.added += nodes
	if w.addedBytes += bytes; w.added > w.maxAdded || w.addedBytes > w.maxBytes {
		return w.reject(n, "aliases that add more than 1,000,000 nodes, or 100 times the document's own nodes or bytes")
	}
	return nil
}

// quote writes s as a JSON string, as encoding/json writes it without HTML
// escaping: as it is when no character of it needs an escape, and otherwise
// recorded as one that may have one.
func (w *yamlWriter) quote(s string) {
	at := w.b.Len()
	if !strings.ContainsFunc(s, func(r rune) bool {
		return r < ' ' || r == '"' || r == '\\' || r == utf8.RuneError || r == '\u2028' || r == '\u2029'
	}) {
		w.b.WriteByte('"')
		w.b.WriteString(s)
		w.b.WriteByte('"')
		return
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	w.b.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	w.escapes = append(w.escapes, uint32(at))
}

// reject reports a defect of the node n, at the line and column where it
// starts.
func (w *yamlWriter) reject(n *yamlNode, msg string) error {
	i := 0
	for line := 1; line < n.Line && i < len(w.src); line++ {
		j := strings.IndexByte(w.src[i:], '\n')
		if j < 0 {
			i = len(w.src)
			break
		}
		i += j + 1
	}
	for col := 1; col < n.Column && i < len(w.src) && w.src[i] != '\n'; col++ {
		_, size := utf8.DecodeRuneInString(w.src[i:])
		i += size
	}
	return rejection(w.uri, w.src, i, w.unit, msg)
}

// yamlFloat matches the integers and floats of YAML 1.2's Core schema
// written in base 10 (section 10.3.2).
var yamlFloat = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)

// yamlScalarNode returns the JSON text of the scalar n as YAML 1.2's Core schema
// reads it (section 10.3.2), or, when it is a string, the string: a plain
// scalar is resolved by its spelling, a quoted or block scalar is a string,
// and one with a Core tag is what the tag says; anything else, and a value
// JSON cannot hold, is an error.
func yamlScalar(n *yamlNode) (text string, quoted bool, err error) {
	s, tag := n.Value, ""
	switch {
	case n.Style&yamlTagged != 0:
		tag = n.Tag
	case n.Style&(yamlDoubleQuoted|yamlSingleQuoted|yamlLiteral|yamlFolded) != 0:
		tag = "!!str"
	}
	number, numeric, err := yamlNumber(s)
	integer := numeric && (strings.HasPrefix(s, "0o") || strings.HasPrefix(s, "0x") || !strings.ContainsAny(s, ".eE"))
	switch {
	case tag == "!" || tag == "!!str":
		return s, true, nil
	case (tag == "" || tag == "!!null") && (s == "" || s == "~" || s == "null" || s == "Null" || s == "NULL"):
		return "null", false, nil
	case (tag == "" || tag == "!!bool") && (s == "true" || s == "True" || s == "TRUE" || s == "false" || s == "False" || s == "FALSE"):
		return strings.ToLower(s), false, nil
	case (tag == "" || tag == "!!float") && err != nil:
		return "", false, err
	case tag == "" && numeric, tag == "!!float" && numeric, tag == "!!int" && integer:
		return number, false, nil
	case tag == "":
		return s, true, nil
	}
	return "", false, fmt.Errorf("%s %q: a tag outside the Core schema, or a value that is not of its tag", tag, s)
}

// yamlNumber returns the JSON spelling of s when it is an integer or float of
// YAML 1.2's Core schema, keeping the exact value written: a leading + and
// zeros leading a whole part of more than one digit are dropped, a 0 is
// written before a leading point, a point with no digit after it is dropped,
// and an octal or hexadecimal integer is written in decimal. Infinities and
// NaN, which JSON cannot hold, are an error.
func yamlNumber(s string) (string, bool, error) {
	if s == "" || strings.IndexByte("+-.0123456789", s[0]) < 0 {
		return "", false, nil
	}
	switch s {
	case ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF", "-.inf", "-.Inf", "-.INF", ".nan", ".NaN", ".NAN":
		return "", false, fmt.Errorf("JSON cannot hold %s", s)
	}
	if len(s) > 2 && s[0] == '0' && (s[1] == 'o' || s[1] == 'x') {
		base := 8
		if s[1] == 'x' {
			base = 16
		}
		if i, ok := new(big.Int).SetString(s[2:], base); ok && s[2] != '+' && s[2] != '-' {
			return i.String(), true, nil
		}
		return "", false, nil
	}
	if !yamlFloat.MatchString(s) {
		return "", false, nil
	}
	sign := ""
	switch s[0] {
	case '-':
		sign, s = "-", s[1:]
	case '+':
		s = s[1:]
	}
	whole, frac, exp := s, "", ""
	if i := strings.IndexAny(whole, "eE"); i >= 0 {
		whole, exp = whole[:i], whole[i:]
	}
	if i := strings.IndexByte(whole, '.'); i >= 0 {
		if whole, frac = whole[:i], whole[i:]; frac == "." {
			frac = ""
		}
	}
	if whole = strings.TrimLeft(whole, "0"); whole == "" {
		whole = "0"
	}
	return sign + whole + frac + exp, true, nil
}
