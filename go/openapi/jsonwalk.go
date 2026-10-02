package openapi

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
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

// A held value is a field's value that encoding/json writes otherwise than
// the value alone: one it reaches addressably and whose pointer, which its
// methods need, is a reader, which the value is not; or one held in an
// interface type with MarshalText but not MarshalJSON, whose method json
// calls whatever MarshalJSON the value has. It is p, a pointer to the value
// where it is held, which json writes as it writes the value there; the
// field's content is the value p points to.
type held struct{ p reflect.Value }

// bare returns v, or the value it holds.
func bare(v any) any {
	if h, ok := v.(held); ok {
		return h.p.Elem().Interface()
	}
	return v
}

// marshal returns v as encoding/json writes it, but for HTML characters,
// which it leaves unescaped: the JSON data of a parameter value.
func marshal(v any) (string, error) {
	if h, ok := v.(held); ok {
		v = h.p.Interface()
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", &encodingError{err}
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// encodeJSON returns v as encode, which uses encoding/json, writes it, or
// why v cannot be sent: an io.Reader or Part that json reaches, at the JSON
// Pointer it returns, or nesting deeper than 1,000 levels. Nesting the walk
// proves is refused before anything is encoded; otherwise it is counted in
// the JSON. A value the walk finds json refuses, a cycle or a map whose keys
// it cannot write, json refuses as it does.
func encodeJSON[T string | []byte](d *document, v any, encode func(any) (T, error)) (T, string, error) {
	var b T
	w := walker{d: d}
	at, found, levels, _ := w.any(v, 1, 0)
	switch {
	case found:
		return b, at, errReader
	case levels == deeper:
		return b, "", errDepth
	}
	b, err := encode(v)
	if err == nil && (levels <= 0 || levels > maxDepth) && tooDeep(b) {
		err = errDepth
	}
	return b, "", err
}

// marshalJSON is json.Marshal, its error an *encodingError.
func marshalJSON(v any) ([]byte, error) {
	if h, ok := v.(held); ok {
		v = h.p.Interface()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, &encodingError{err}
	}
	return b, nil
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
	addr          bool        // json writes an addressable value otherwise than a copy
	ptrRead       bool        // its pointer is an io.Reader, which it is not
	fields        []jsonField // a struct's fields json writes
	levels        int         // at most as many levels as a value's JSON takes, or 0 when only its JSON tells
}

// A jsonField is a struct field encoding/json writes.
type jsonField struct {
	name                string
	index               []int
	tagged              bool
	omitZero, omitEmpty bool
	quoted              bool // the string option: a scalar written as a JSON string
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
	// An addressable value is written by its pointer's MarshalJSON, else by
	// its pointer's MarshalText, else by reflection, its fields and elements
	// addressable too; a copy by its own methods, else by reflection.
	w.addr = w.ptrJSON && !w.json || !w.ptrJSON && w.ptrText && !w.text
	switch k := t.Kind(); {
	case w.json || w.text: // json writes it by a method, never looking inside
	case k != reflect.Interface && (t == partType || t.Implements(readerType)):
		w.reader, w.holds = true, true // unless an addressable value's pointer has a method
	case k == reflect.Interface: // the value it holds tells
		w.holds, w.levels = true, 0
	case k == reflect.Pointer:
		e := d.walkOf(t.Elem(), open)
		w.holds, w.levels = e.holds, e.levels
	case k == reflect.Slice && bytesKind(t):
		// bytes, which json writes as a base64 string
	case k == reflect.Map || k == reflect.Array || k == reflect.Slice:
		e := d.walkOf(t.Elem(), open)
		w.holds, w.levels, w.addr = e.holds, nest(e.levels), w.addr || k == reflect.Array && e.addr
	case k == reflect.Struct:
		w.fields = jsonFields(t)
		for _, f := range w.fields {
			e := d.walkOf(t.FieldByIndex(f.index).Type, open)
			w.holds, w.levels, w.addr = w.holds || e.holds, deepest(w.levels, nest(e.levels)), w.addr || e.addr && inline(t, f.index)
		}
	}
	switch {
	case w.json || w.ptrJSON:
		w.levels = 0 // its MarshalJSON decides
	case w.text:
		w.levels = 1
	}
	w.ptrRead = byValue && !t.Implements(readerType) && reflect.PointerTo(t).Implements(readerType)
	got, _ := d.walks.LoadOrStore(t, w)
	return got.(*walk)
}

// inline reports whether the field of struct type t at index lies in a
// value of t, not behind an embedded pointer, so that it can be addressed
// where the value can.
func inline(t reflect.Type, index []int) bool {
	for _, i := range index[:len(index)-1] {
		if t = t.Field(i).Type; t.Kind() == reflect.Pointer {
			return false
		}
	}
	return true
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

// What a walk tells of the levels of a value's JSON, besides a bound.
const (
	unknown = 0  // only its JSON tells, as with a MarshalJSON
	deeper  = -1 // it nests deeper than maxDepth: the walk reached a value json writes past it
	refused = -2 // json refuses it: a cycle, or a map whose keys json cannot write
)

// A walker walks a value as encoding/json writes it, finding the JSON
// Pointer of an io.Reader or Part that json reaches, and at most as many
// levels as its JSON reaches, or what else it tells. Pointers and interfaces
// add no level; past maxDepth dereferences on a path it keeps the pointers on
// it, as json does past its own 1,000, and a repeat is a cycle. It stops at
// the first value that decides, past maxDepth levels, a cycle or a reader,
// returning where a value past maxDepth is held. It walks the values json
// itself creates without reflection.
type walker struct {
	d    *document
	path map[ptrKey]bool // past maxDepth dereferences, the pointers on the path
	on   []ptrKey        // and in the order they went on
}

// A ptrKey identifies a pointer as json's cycle detection does: by its type
// and address.
type ptrKey struct {
	t reflect.Type
	p uintptr
}

// any walks x, at level, after derefs dereferences.
func (w *walker) any(x any, level, derefs int) (at string, found bool, levels int, deep uintptr) {
	if level > maxDepth {
		return "", false, deeper, identity(reflect.ValueOf(x))
	}
	levels = level
	switch x := x.(type) {
	case nil, string, bool, float64, json.Number:
	case map[string]any:
		for k, v := range x {
			at, found, l, deep := w.any(v, level+1, derefs)
			switch {
			case found:
				return "/" + escapeToken(k) + at, true, 0, 0
			case l < 0:
				l, deep = around(l, deep, reflect.ValueOf(x))
				return "", false, l, deep
			}
			levels = deepest(levels, l)
		}
	case []any:
		for i, v := range x {
			at, found, l, deep := w.any(v, level+1, derefs)
			switch {
			case found:
				return "/" + strconv.Itoa(i) + at, true, 0, 0
			case l < 0:
				l, deep = around(l, deep, reflect.ValueOf(x))
				return "", false, l, deep
			}
			levels = deepest(levels, l)
		}
	case held:
		return w.value(x.p.Elem(), level, derefs)
	default:
		return w.value(reflect.ValueOf(x), level, derefs)
	}
	return "", false, levels, 0
}

// identity returns where v's content is held, for a map, slice or pointer,
// or a value that can be addressed, else 0.
func identity(v reflect.Value) uintptr {
	switch v.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer:
		return v.Pointer()
	}
	if v.CanAddr() {
		return v.Addr().Pointer()
	}
	return 0
}

// around returns what a walk found beneath the container v, and where it
// was found past maxDepth: a cycle when that is v itself, and v when it was
// a value held nowhere else, so that a container around v can tell.
func around(l int, deep uintptr, v reflect.Value) (int, uintptr) {
	switch id := identity(v); {
	case l != deeper:
	case deep == 0:
		return deeper, id
	case deep == id:
		return refused, deep
	}
	return l, deep
}

// value walks v as json reaches it, its pointer's methods applying only
// where it is addressable: through the pointers and interfaces it holds,
// without recursion, to a value json writes by a method or by reflection.
func (w *walker) value(v reflect.Value, level, derefs int) (string, bool, int, uintptr) {
	if level > maxDepth {
		return "", false, deeper, identity(v)
	}
	mark := len(w.on)
	defer w.leave(mark)
	for (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && !v.IsNil() {
		if wk := w.d.walkOf(v.Type(), nil); wk.json || wk.text || wk.reader {
			break // json writes it by a method, or it is a reader
		}
		if v.Kind() == reflect.Interface {
			x := v.Interface()
			switch x.(type) {
			case map[string]any, []any:
				return w.any(x, level, derefs+1)
			}
			v, derefs = reflect.ValueOf(x), derefs+1
			continue
		}
		if derefs++; derefs > maxDepth {
			k := ptrKey{v.Type(), v.Pointer()}
			if w.path[k] {
				return "", false, refused, 0 // a cycle
			}
			if w.path == nil {
				w.path = map[ptrKey]bool{}
			}
			w.path[k], w.on = true, append(w.on, k)
		}
		v = v.Elem()
	}
	if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() || !v.IsValid() {
		return "", false, level, 0 // null
	}
	wk := w.d.walkOf(v.Type(), nil)
	addr := v.CanAddr()
	switch {
	case wk.json || wk.ptrJSON && addr:
		return "", false, unknown, 0
	case wk.text || wk.ptrText && addr:
		return "", false, level, 0
	case v.Kind() == reflect.Map && !jsonKeys(v.Type()):
		return "", false, refused, 0 // never walked: its keys have no names
	case (v.Kind() == reflect.Map || v.Kind() == reflect.Slice) && v.IsNil():
		return "", false, level, 0 // null
	case wk.reader:
		return "", true, 0, 0
	case !wk.holds && wk.levels > 0:
		return "", false, level - 1 + wk.levels, 0
	case !wk.holds:
		return "", false, unknown, 0
	}
	levels := level
	step := func(fv reflect.Value, token func() string) (string, bool, int, uintptr, bool) {
		at, found, l, deep := w.value(fv, level+1, derefs)
		switch {
		case found:
			return "/" + token() + at, true, 0, 0, true
		case l < 0:
			l, deep = around(l, deep, v)
			return "", false, l, deep, true
		}
		levels = deepest(levels, l)
		return "", false, 0, 0, false
	}
	switch v.Kind() {
	case reflect.Struct:
		for _, f := range wk.fields {
			fv, err := v.FieldByIndexErr(f.index)
			if err != nil || f.omitZero && omitsZero(fv) || f.omitEmpty && omitsEmpty(fv) {
				continue // json writes no such field
			}
			if at, found, l, deep, stop := step(fv, func() string { return escapeToken(f.name) }); stop {
				return at, found, l, deep
			}
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			if at, found, l, deep, stop := step(it.Value(), func() string { k, _ := mapKey(it.Key()); return escapeToken(k) }); stop {
				return at, found, l, deep
			}
		}
	default: // a slice or array
		for i := range v.Len() {
			if at, found, l, deep, stop := step(v.Index(i), func() string { return strconv.Itoa(i) }); stop {
				return at, found, l, deep
			}
		}
	}
	return "", false, levels, 0
}

// leave takes off the path the pointers put on it after the first mark.
func (w *walker) leave(mark int) {
	for _, k := range w.on[mark:] {
		delete(w.path, k)
	}
	w.on = w.on[:mark]
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
	return v.IsZero()
}

// omitsEmpty reports whether json's omitempty omits v: false, 0, a nil
// pointer or interface, and an empty array, map, slice or string.
func omitsEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Struct, reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128:
		return false
	}
	return v.IsZero()
}

// jsonKeys reports whether encoding/json writes a map of type t: its keys
// are strings, integers or TextMarshalers.
func jsonKeys(t reflect.Type) bool {
	switch t.Key().Kind() {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return t.Key().Implements(textMarshalerType)
}

// mapKey returns the name encoding/json writes for a map key json accepts,
// or its MarshalText's error.
func mapKey(k reflect.Value) (string, error) {
	if k.Kind() == reflect.String {
		return k.String(), nil
	}
	if tm, ok := k.Interface().(encoding.TextMarshaler); ok {
		if k.Kind() == reflect.Pointer && k.IsNil() {
			return "", nil // as encoding/json names a nil TextMarshaler
		}
		b, err := tm.MarshalText()
		return string(b), err
	}
	if k.CanInt() {
		return strconv.FormatInt(k.Int(), 10), nil
	}
	return strconv.FormatUint(k.Uint(), 10), nil
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
				options := strings.Split(opts, ",")
				f := jsonField{name: name, index: index, tagged: name != "", omitZero: slices.Contains(options, "omitzero"),
					omitEmpty: slices.Contains(options, "omitempty"), quoted: slices.Contains(options, "string")}
				switch ft.Kind() {
				case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8,
					reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64, reflect.String:
				default:
					f.quoted = false // json applies the option to scalars only
				}
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
