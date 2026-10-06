package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// parseYAML reads the UTF-8 text of a document whose original encoding used
// unit bytes per code unit. Only syntax is delegated to the parser: Core
// scalar resolution, JSON conversion and resource limits belong to yamlTree.
func parseYAML(ctx context.Context, src, uri string, unit, size int) (*tree, error) {
	input, directive := src, false
	for i := 0; i < len(src); {
		end := strings.IndexFunc(src[i:], yamlBreak)
		if end < 0 {
			end = len(src) - i
		}
		line := src[i : i+end]
		if v, ok := strings.CutPrefix(line, "%YAML"); ok && v != "" && (v[0] == ' ' || v[0] == '\t') {
			v, _, comment := strings.Cut(v, "#")
			if comment && (len(v) == 0 || v[len(v)-1] != ' ' && v[len(v)-1] != '\t') {
				return nil, rejection(uri, src, i, unit, "a %YAML directive comment requires separation")
			}
			v = strings.TrimSpace(v)
			major, minor, dot := strings.Cut(v, ".")
			if directive || !dot || strings.TrimLeft(major, "0") != "1" || minor == "" || strings.ContainsFunc(minor, func(r rune) bool { return r < '0' || r > '9' }) {
				return nil, rejection(uri, src, i, unit, "invalid %YAML directive or version other than 1.x")
			}
			// The parser supports the 1.1 directive. Preserve its syntax and
			// offsets while our own scalar resolver applies 1.2 to every 1.x.
			directive = true
			at := i + strings.Index(line, v)
			input = input[:at] + strings.Repeat(" ", len(v)-3) + "1.1" + input[at+len(v):]
		} else if s := strings.TrimLeft(line, " \t\r"); line != "" && line[0] != '%' && s != "" && s[0] != '#' {
			break
		}
		_, n := utf8.DecodeRuneInString(src[i+end:])
		i += end + n
	}
	dec := yaml.NewDecoder(ctxReader{ctx, strings.NewReader(input)})
	var document, next yaml.Node
	err := dec.Decode(&document)
	if err == nil {
		if err = dec.Decode(&next); err == io.EOF {
			return yamlTree(ctx, document.Content[0], src, uri, unit, size)
		}
	}
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case err == io.EOF:
		return nil, rejection(uri, src, len(src), unit, "no document in the YAML stream")
	case err != nil:
		// A syntax error, which the parser's own message describes.
		return nil, fmt.Errorf("openapi: %s: %v", safeURI(uri), err)
	}
	w := yamlWriter{uri: uri, src: src, unit: unit}
	return nil, w.reject(&next, "a second document in the YAML stream")
}

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
	named              map[*yaml.Node]*written
	added, addedBytes  int64 // what aliases added
	maxAdded, maxBytes int64
	reaches, declares  bool // see tree
	editionRefs        bool
	dialects           bool
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
func yamlTree(ctx context.Context, root *yaml.Node, src, uri string, unit, size int) (*tree, error) {
	w := &yamlWriter{ctx: ctx, uri: uri, src: src, unit: unit, named: map[*yaml.Node]*written{}}
	own := w.count(root)
	w.maxAdded = min(1_000_000, 100*int64(own))
	w.maxBytes = 100 * int64(size)
	if _, err := w.value(root, 1, 0); err != nil {
		return nil, err
	}
	return &tree{src: w.b.String(), nodes: w.nodes, escapes: w.escapes, decoded: make([]atomic.Pointer[string], len(w.escapes)),
		reaches: w.reaches, declares: w.declares, editionRefs: w.editionRefs, dialects: w.dialects}, nil
}

// count returns the nodes of n, each alias one, noting the nodes aliases
// name.
func (w *yamlWriter) count(n *yaml.Node) int {
	if n.Kind == yaml.AliasNode && n.Alias != nil && w.named[n.Alias] == nil {
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
func (w *yamlWriter) value(n *yaml.Node, depth int, name uint32) (int, error) {
	switch {
	case depth > maxDepth:
		return 0, w.reject(n, "nesting deeper than 1,000 levels")
	case w.ctx.Err() != nil:
		return 0, fmt.Errorf("openapi: %s: %w", safeURI(w.uri), w.ctx.Err())
	case n.Kind == yaml.AliasNode:
		return w.alias(n, depth, name)
	}
	at, levels := len(w.nodes), 1
	a := w.named[n]
	if a != nil {
		a.first, a.start = int32(at), w.b.Len()
	}
	w.nodes = append(w.nodes, node{start: uint32(w.b.Len()), name: name})
	switch n.Kind {
	case yaml.ScalarNode:
		s, quoted, err := yamlScalar(n)
		switch {
		case err != nil:
			return 0, w.reject(n, err.Error())
		case quoted:
			w.quote(s)
		default:
			w.b.WriteString(s)
		}
	case yaml.MappingNode, yaml.SequenceNode:
		tag, write := "!!seq", w.sequence
		if n.Kind == yaml.MappingNode {
			tag, write = "!!map", w.mapping
		}
		if n.Style&yaml.TaggedStyle != 0 && n.Tag != tag {
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
func (w *yamlWriter) sequence(n *yaml.Node, depth int) (int, error) {
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
func (w *yamlWriter) mapping(n *yaml.Node, depth int) (int, error) {
	levels, base := 1, len(w.names)
	defer func() { w.names = w.names[:base] }()
	var seen map[string]bool // the keys of a mapping with many
	w.b.WriteByte('{')
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		key := k
		if key.Kind == yaml.AliasNode && key.Alias != nil {
			key = key.Alias
		}
		if key.Kind != yaml.ScalarNode {
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
		if key != k {
			if err := w.add(k, 1, w.b.Len()-at); err != nil {
				return 0, err
			}
		}
		w.b.WriteByte(':')
		start := w.b.Len()
		l, err := w.value(v, depth+1, uint32(at))
		if err != nil {
			return 0, err
		}
		if levels = max(levels, l+1); len(key.Value) > 1 && (key.Value[0] == '$' || key.Value[0] == 'm' || key.Value[0] == 'd' || key.Value[0] == 's') {
			note(key.Value, w.b.String()[start:], &w.reaches, &w.declares, &w.editionRefs, &w.dialects)
		}
	}
	w.b.WriteByte('}')
	return levels, nil
}

// alias writes again what the node the alias n names was written as, at
// level depth, the member whose name is at name if not 0, unless the alias
// is inside that node, or the aliases would add more than the bounds allow.
// A scalar named only as a key is written first.
func (w *yamlWriter) alias(n *yaml.Node, depth int, name uint32) (int, error) {
	a := w.named[n.Alias]
	key := a != nil && !a.done && n.Alias.Kind == yaml.ScalarNode
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
	w.b.Grow(a.end - a.start)
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
func (w *yamlWriter) add(n *yaml.Node, nodes, bytes int) error {
	w.added += int64(nodes)
	if w.addedBytes += int64(bytes); w.added > w.maxAdded || w.addedBytes > w.maxBytes {
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
func (w *yamlWriter) reject(n *yaml.Node, msg string) error {
	return rejection(w.uri, w.src, yamlOffset(w.src, n.Line, n.Column), w.unit, msg)
}

// yamlOffset maps a node's line and column, as the parser counts them, to
// its offset in src: lines end at CR, LF, CRLF, NEL, LS and PS, and columns
// count characters.
func yamlOffset(src string, line, column int) int {
	i := 0
	for row := 1; row < line && i < len(src); row++ {
		j := strings.IndexFunc(src[i:], yamlBreak)
		if j < 0 {
			return len(src)
		}
		_, n := utf8.DecodeRuneInString(src[i+j:])
		i += j + n
		if src[i-1] == '\r' && i < len(src) && src[i] == '\n' {
			i++
		}
	}
	for col := 1; col < column && i < len(src); col++ {
		r, n := utf8.DecodeRuneInString(src[i:])
		if yamlBreak(r) {
			break
		}
		i += n
	}
	return i
}

// yamlBreak reports whether the parser ends a line at r: CR, LF, NEL, LS or
// PS, a CR followed by LF being one break.
func yamlBreak(r rune) bool {
	return r == '\r' || r == '\n' || r == '\u0085' || r == '\u2028' || r == '\u2029'
}

// yamlFloat matches the integers and floats of YAML 1.2's Core schema
// written in base 10 (section 10.3.2).
var yamlFloat = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)

// yamlScalar returns the JSON text of the scalar n as YAML 1.2's Core schema
// reads it (section 10.3.2), or, when it is a string, the string: a plain
// scalar is resolved by its spelling, a quoted or block scalar is a string,
// and one with a Core tag is what the tag says; anything else, and a value
// JSON cannot hold, is an error.
func yamlScalar(n *yaml.Node) (text string, quoted bool, err error) {
	s, tag := n.Value, ""
	switch {
	case n.Style&yaml.TaggedStyle != 0:
		tag = n.Tag
	case n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		tag = "!!str"
	}
	if tag == "!!str" {
		return s, true, nil
	}
	number, numeric, err := yamlNumber(s)
	integer := numeric && (strings.HasPrefix(s, "0o") || strings.HasPrefix(s, "0x") || !strings.ContainsAny(s, ".eE"))
	switch {
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
		if base == 8 {
			// math/big scans base 8 by repeatedly multiplying the growing
			// integer. Pack its three-bit digits directly into machine words
			// with linear work and storage for digit packing. Formatting the
			// resulting integer in decimal below still has superlinear cost.
			digits := strings.TrimLeft(s[2:], "0")
			words := make([]big.Word, (len(digits)*3+strconv.IntSize-1)/strconv.IntSize)
			for i, bit := len(digits)-1, 0; i >= 0; i, bit = i-1, bit+3 {
				if digits[i] < '0' || digits[i] > '7' {
					return "", false, nil
				}
				v, word, shift := big.Word(digits[i]-'0'), bit/strconv.IntSize, uint(bit%strconv.IntSize)
				words[word] |= v << shift
				if shift > strconv.IntSize-3 {
					words[word+1] |= v >> (strconv.IntSize - shift)
				}
			}
			return new(big.Int).SetBits(words).String(), true, nil
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
