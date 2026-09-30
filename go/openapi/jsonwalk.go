package openapi

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Where the client encodes a value with encoding/json, json's output and
// behavior govern: this file finds what json reaches in a value, whether it
// holds an io.Reader or a Part, and how deeply its JSON nests.

var (
	errReader = errors.New("a JSON value cannot hold an io.Reader or a Part")
	errDepth  = errors.New("the value nests deeper than 1,000 levels")
)

var (
	readerType        = reflect.TypeFor[io.Reader]()
	partType          = reflect.TypeFor[Part]()
	marshalerType     = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	zeroerType        = reflect.TypeFor[zeroer]()
)

type zeroer interface{ IsZero() bool }

// marshal returns v as encoding/json writes it, but for HTML characters,
// which it leaves unescaped: the JSON data of a parameter value.
func marshal(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", &encodingError{err}
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// checkJSON returns why v cannot be sent as b, the JSON encoding/json writes
// for it: an io.Reader or Part that json reaches, at the JSON Pointer it
// returns, or nesting deeper than 1,000 levels, counted in b unless the walk
// bounds it.
func checkJSON[T string | []byte](d *document, v any, b T) (string, error) {
	switch at, found, levels := d.findReader(v, 1); {
	case found:
		return at, errReader
	case (levels == 0 || levels > maxDepth) && tooDeep(b):
		return "", errDepth
	}
	return "", nil
}

// tooDeep reports whether the JSON text b, as encoding/json writes it, nests
// deeper than 1,000 levels, the outermost value being level 1: whether an
// array or object at level 1,000 has a member.
func tooDeep[T string | []byte](b T) bool {
	var opens int // counting brackets in strings too
	switch b := any(b).(type) {
	case string:
		opens = strings.Count(b, "[") + strings.Count(b, "{")
	case []byte:
		opens = bytes.Count(b, []byte("[")) + bytes.Count(b, []byte("{"))
	}
	if opens < maxDepth {
		return false
	}
	depth := 0 // the arrays and objects open
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			for i++; b[i] != '"'; i++ {
				if b[i] == '\\' {
					i++
				}
			}
		case '[', '{':
			if depth++; depth == maxDepth && b[i+1] != ']' && b[i+1] != '}' {
				return true
			}
		case ']', '}':
			depth--
		}
	}
	return false
}

// A walk is how encoding/json writes the values of a type: whether json
// reaches a reader or Part in them, by which of its methods json writes them,
// and at most how many levels their JSON takes.
type walk struct {
	reader        bool        // the type is an io.Reader or Part, refused where json encodes it by reflection
	json, ptrJSON bool        // json calls its MarshalJSON, or its pointer's on an addressable value
	text, ptrText bool        // likewise MarshalText
	holds         bool        // a value json looks inside can hold a reader, or nests as only it tells
	fields        []jsonField // a struct's fields json writes
	levels        int         // at most as many levels as a value's JSON takes, or 0 when only its JSON tells
}

// A jsonField is a struct field encoding/json writes.
type jsonField struct {
	name     string
	index    []int
	tagged   bool
	omitZero bool
}

// walkOf returns the walk for t, from the document's cache. open holds the
// types being examined; one reached again, through a recursive type, holds
// what only its values tell.
func (d *document) walkOf(t reflect.Type, open map[reflect.Type]bool) *walk {
	if w, ok := d.walks.Load(t); ok {
		return w.(*walk)
	}
	if open[t] {
		return &walk{holds: true}
	}
	if open == nil {
		open = map[reflect.Type]bool{}
	}
	open[t] = true
	defer delete(open, t)
	byValue := t.Kind() != reflect.Pointer
	w := &walk{
		json: t.Implements(marshalerType), ptrJSON: byValue && reflect.PointerTo(t).Implements(marshalerType),
		text: t.Implements(textMarshalerType), ptrText: byValue && reflect.PointerTo(t).Implements(textMarshalerType),
		levels: 1,
	}
	switch k := t.Kind(); {
	case w.json || w.text: // json writes it by a method, never looking inside
	case k != reflect.Interface && (t == partType || t.Implements(readerType)):
		w.reader, w.holds = true, true // unless an addressable value's pointer has a method
	case k == reflect.Interface: // the value it holds tells
		w.holds, w.levels = true, 0
	case k == reflect.Pointer:
		e := d.walkOf(t.Elem(), open)
		w.holds, w.levels = e.holds, e.levels
	case k == reflect.Slice && t.Elem().Kind() == reflect.Uint8 &&
		!reflect.PointerTo(t.Elem()).Implements(marshalerType) && !reflect.PointerTo(t.Elem()).Implements(textMarshalerType):
		// bytes, which json writes as a base64 string
	case k == reflect.Map || k == reflect.Array || k == reflect.Slice:
		e := d.walkOf(t.Elem(), open)
		w.holds, w.levels = e.holds, nest(e.levels)
	case k == reflect.Struct:
		w.fields = jsonFields(t)
		for _, f := range w.fields {
			e := d.walkOf(t.FieldByIndex(f.index).Type, open)
			w.holds, w.levels = w.holds || e.holds, deepest(w.levels, nest(e.levels))
		}
	}
	switch {
	case w.json || w.ptrJSON:
		w.levels = 0 // its MarshalJSON decides
	case w.text:
		w.levels = 1
	}
	d.walks.Store(t, w)
	return w
}

// deepest returns the greater of two level counts, or 0, unknown, when
// either is.
func deepest(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	return max(a, b)
}

// nest returns the levels of a container whose items take l.
func nest(l int) int {
	if l == 0 {
		return 0
	}
	return l + 1
}

// findReader walks x, at level, as encoding/json writes it, returning the
// JSON Pointer, from x, of an io.Reader or Part that json reaches, and at
// most as many levels as its JSON reaches, or 0 when only the JSON tells. It
// walks the values json itself creates without reflection.
func (d *document) findReader(x any, level int) (string, bool, int) {
	levels := level
	switch x := x.(type) {
	case nil, string, bool, float64, json.Number:
	case map[string]any:
		for k, v := range x {
			at, found, l := d.findReader(v, level+1)
			if found {
				return "/" + escapeToken(k) + at, true, 0
			}
			levels = deepest(levels, l)
		}
	case []any:
		for i, v := range x {
			at, found, l := d.findReader(v, level+1)
			if found {
				return "/" + strconv.Itoa(i) + at, true, 0
			}
			levels = deepest(levels, l)
		}
	default:
		return d.findValue(reflect.ValueOf(x), level)
	}
	return "", false, levels
}

// findValue is findReader for a value of any type, as json reaches it: its
// pointer's methods apply only where it is addressable.
func (d *document) findValue(v reflect.Value, level int) (string, bool, int) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return "", false, level // null
		}
	}
	w := d.walkOf(v.Type(), nil)
	addr := v.CanAddr()
	switch {
	case w.json || w.ptrJSON && addr:
		return "", false, 0
	case w.text || w.ptrText && addr:
		return "", false, level
	case w.reader:
		return "", true, 0
	case !w.holds && w.levels > 0:
		return "", false, level - 1 + w.levels
	case !w.holds:
		return "", false, 0
	}
	levels := level
	switch v.Kind() {
	case reflect.Interface:
		if v.CanInterface() {
			return d.findReader(v.Interface(), level)
		}
		return d.findValue(v.Elem(), level)
	case reflect.Pointer:
		return d.findValue(v.Elem(), level)
	case reflect.Struct:
		for _, f := range w.fields {
			fv, err := v.FieldByIndexErr(f.index)
			if err != nil || f.omitZero && omitsZero(fv) {
				continue // json writes no such field
			}
			at, found, l := d.findValue(fv, level+1)
			if found {
				return "/" + escapeToken(f.name) + at, true, 0
			}
			levels = deepest(levels, l)
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			at, found, l := d.findValue(it.Value(), level+1)
			if found {
				return "/" + escapeToken(mapKey(it.Key())) + at, true, 0
			}
			levels = deepest(levels, l)
		}
	default: // a slice or array
		for i := range v.Len() {
			at, found, l := d.findValue(v.Index(i), level+1)
			if found {
				return "/" + strconv.Itoa(i) + at, true, 0
			}
			levels = deepest(levels, l)
		}
	}
	return "", false, levels
}

// omitsZero reports whether json's omitzero omits v by its IsZero method; a
// zero value, which it omits too, holds no reader.
func omitsZero(v reflect.Value) bool {
	switch t := v.Type(); {
	case t.Kind() == reflect.Interface && t.Implements(zeroerType):
		return v.IsNil() || v.Elem().Kind() == reflect.Pointer && v.Elem().IsNil() || v.Interface().(zeroer).IsZero()
	case t.Kind() == reflect.Pointer && t.Implements(zeroerType):
		return v.IsNil() || v.Interface().(zeroer).IsZero()
	case t.Implements(zeroerType):
		return v.Interface().(zeroer).IsZero()
	case reflect.PointerTo(t).Implements(zeroerType):
		if !v.CanAddr() {
			c := reflect.New(t).Elem()
			c.Set(v)
			v = c
		}
		return v.Addr().Interface().(zeroer).IsZero()
	}
	return false
}

// mapKey returns the name encoding/json writes for a map key.
func mapKey(k reflect.Value) string {
	switch {
	case k.Kind() == reflect.String:
		return k.String()
	case k.Kind() == reflect.Pointer && k.IsNil():
		return "" // as encoding/json names a nil TextMarshaler, the only nil key it writes
	}
	if tm, ok := k.Interface().(encoding.TextMarshaler); ok {
		b, _ := tm.MarshalText()
		return string(b)
	}
	return fmt.Sprint(k.Interface())
}

// jsonFields returns the fields of struct type t that encoding/json writes,
// in order, chosen as its typeFields chooses them: breadth first through
// embedded structs, each type once, a shallower field, or else the one named
// by a tag, dominating others of its name, and two equal ones none.
func jsonFields(t reflect.Type) []jsonField {
	type embedded struct {
		index []int
		typ   reflect.Type
	}
	var fields []jsonField
	current, next := []embedded{}, []embedded{{typ: t}}
	var count, nextCount map[reflect.Type]int
	visited := map[reflect.Type]bool{}
	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[reflect.Type]int{}
		for _, e := range current {
			if visited[e.typ] {
				continue
			}
			visited[e.typ] = true
			for i := range e.typ.NumField() {
				sf := e.typ.Field(i)
				embed := sf.Type
				if embed.Kind() == reflect.Pointer {
					embed = embed.Elem()
				}
				tag := sf.Tag.Get("json")
				if tag == "-" || !sf.IsExported() && (!sf.Anonymous || embed.Kind() != reflect.Struct) {
					continue
				}
				name, opts, _ := strings.Cut(tag, ",")
				if !validTag(name) {
					name = ""
				}
				index := append(slices.Clip(e.index), i)
				ft := sf.Type
				if ft.Name() == "" && ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if name == "" && sf.Anonymous && ft.Kind() == reflect.Struct {
					if nextCount[ft]++; nextCount[ft] == 1 {
						next = append(next, embedded{index, ft})
					}
					continue
				}
				f := jsonField{name: name, index: index, tagged: name != "", omitZero: slices.Contains(strings.Split(opts, ","), "omitzero")}
				if f.name == "" {
					f.name = sf.Name
				}
				fields = append(fields, f)
				if count[e.typ] > 1 { // a duplicate, which annihilates both
					fields = append(fields, f)
				}
			}
		}
	}
	slices.SortFunc(fields, func(a, b jsonField) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		if c := len(a.index) - len(b.index); c != 0 {
			return c
		}
		if a.tagged != b.tagged {
			if a.tagged {
				return -1
			}
			return 1
		}
		return slices.Compare(a.index, b.index)
	})
	out := fields[:0]
	for i := 0; i < len(fields); {
		j := i + 1
		for j < len(fields) && fields[j].name == fields[i].name {
			j++
		}
		if j == i+1 || len(fields[i].index) < len(fields[i+1].index) || fields[i].tagged != fields[i+1].tagged {
			out = append(out, fields[i])
		}
		i = j
	}
	slices.SortFunc(out, func(a, b jsonField) int { return slices.Compare(a.index, b.index) })
	return out
}

// validTag reports whether encoding/json takes s as the name a tag gives.
func validTag(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c) && !unicode.IsLetter(c) && !unicode.IsDigit(c) {
			return false
		}
	}
	return s != ""
}
