package openapi

import (
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

// collectionWriter emits Swagger collection data with a bound shared by all
// nesting levels. A child starts its parent only when it has defined data.
type collectionWriter struct {
	c     *Client
	b     *strings.Builder
	p     *param
	empty bool
}

func (w *collectionWriter) scalar(s string, depth int, start func() error) (bool, error) {
	if len(s) > maxLength-w.b.Len() {
		return false, errTooLong
	}
	s = jsonText(s)
	w.empty = depth == 1 && s == ""
	if err := start(); err != nil {
		return false, err
	}
	if len(s) > maxLength-w.b.Len() || w.p.set != nil && escapedSize(s, w.p.set, false) > maxLength-w.b.Len() {
		return false, errTooLong
	}
	escapeTo(w.b, s, w.p.set)
	return true, nil
}

func (w *collectionWriter) array(schema value, n int, each func(func() error) error, start func() error) (bool, error) {
	if n == 0 {
		return false, nil
	}
	cf := schema.str("collectionFormat")
	delim := map[string]string{"": ",", "csv": ",", "ssv": " ", "tsv": "\t", "pipes": "|", "multi": "&"}[cf]
	if delim == "" {
		return false, errors.New("unsupported collectionFormat")
	}
	if w.p.set != nil && cf != "multi" && delim != "," {
		delim = escape(delim, w.p.set)
	}
	begun := false
	next := func() error {
		if !begun {
			begun = true
			return start()
		}
		s := delim
		if cf == "multi" && w.p.In == "query" {
			s += w.p.name + "="
		}
		if len(s) > maxLength-w.b.Len() {
			return errTooLong
		}
		w.b.WriteString(s)
		return nil
	}
	err := each(next)
	if err == nil && !begun && cf != "multi" {
		err = next()
	}
	return true, err
}

func (w *collectionWriter) value(v any, schema value, depth int, start func() error) (bool, error) {
	if depth > maxDepth {
		return false, errDepth
	}
	if null(v) {
		return false, nil
	}
	rv := reflect.ValueOf(v)
	if h, ok := v.(held); ok {
		rv = h.p.Elem()
	}
	rv, ok := w.c.doc.deref(rv)
	if !ok {
		return false, errDepth
	}
	if (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && rv.IsNil() {
		return false, nil
	}
	walk := w.c.doc.walkOf(rv.Type(), nil)
	if rv.CanAddr() && (walk.ptrJSON || !walk.json && walk.ptrText) {
		rv = rv.Addr()
		walk = w.c.doc.walkOf(rv.Type(), nil)
	}
	if !walk.json && !walk.text && !walk.reader {
		switch rv.Kind() {
		case reflect.String:
			if rv.Len() > maxLength-w.b.Len() {
				return false, errTooLong
			}
			if rv.Type() != reflect.TypeFor[json.Number]() {
				return w.scalar(rv.String(), depth, start)
			}
		case reflect.Array, reflect.Slice:
			if rv.Kind() != reflect.Slice || !bytesKind(rv.Type()) {
				return w.array(schema, rv.Len(), func(next func() error) error {
					for i := range rv.Len() {
						if _, err := w.value(w.c.doc.elem(rv.Index(i)), schema.get("items"), depth+1, next); err != nil {
							return err
						}
					}
					return nil
				}, start)
			}
			if rv.Len() > (maxLength-w.b.Len())/4*3 {
				return false, errTooLong
			}
		}
	}
	// Methods retain encoding/json's precedence and element addressability.
	// Validate a custom JSON result in place, without a second full copy.
	method := rv.Interface()
	if m, ok := method.(json.Marshaler); ok && walk.json {
		data, err := m.MarshalJSON()
		if err != nil {
			return false, &encodingError{&json.MarshalerError{Type: rv.Type(), Err: err}}
		}
		if !json.Valid(data) {
			var invalid any
			err = json.Unmarshal(data, &invalid)
			return false, &encodingError{&json.MarshalerError{Type: rv.Type(), Err: err}}
		}
		i := 0
		return collectionJSON(w, data, &i, schema, depth, start)
	}
	if m, ok := method.(encoding.TextMarshaler); ok && walk.text {
		data, err := m.MarshalText()
		if err != nil {
			_, wrapped := json.Marshal(collectionTextError{err})
			wrapped.(*json.MarshalerError).Type = rv.Type()
			return false, &encodingError{wrapped}
		}
		if len(data) > maxLength-w.b.Len() {
			return false, errTooLong
		}
		return w.scalar(string(data), depth, start)
	}
	s, _, err := encodeJSON(w.c.doc, v, marshal)
	if err != nil {
		return false, err
	}
	i := 0
	return collectionJSON(w, s, &i, schema, depth, start)
}

// collectionJSON reads valid JSON, including whitespace from MarshalJSON,
// consuming scalars only after their maximum decoded size has been checked.
func collectionJSON[T string | []byte](w *collectionWriter, s T, i *int, schema value, depth int, start func() error) (bool, error) {
	if depth > maxDepth {
		return false, errDepth
	}
	space := func() {
		for *i < len(s) && (s[*i] == ' ' || s[*i] == '\n' || s[*i] == '\r' || s[*i] == '\t') {
			*i++
		}
	}
	space()
	switch s[*i] {
	case 'n':
		*i += 4
		return false, nil
	case '[':
		*i++
		space()
		if s[*i] == ']' {
			*i++
			return false, nil
		}
		return w.array(schema, 1, func(next func() error) error {
			for {
				if _, err := collectionJSON(w, s, i, schema.get("items"), depth+1, next); err != nil {
					return err
				}
				space()
				ch := s[*i]
				*i++
				if ch == ']' {
					return nil
				}
			}
		}, start)
	case '{':
		*i++
		space()
		for s[*i] != '}' {
			collectionSkipString(s, i)
			space()
			*i++
			defined, err := collectionJSON(w, s, i, value{}, depth+1, func() error { return errors.New("Swagger parameters cannot serialize objects") })
			if err != nil {
				return false, err
			}
			if defined {
				return false, errors.New("Swagger parameters cannot serialize objects")
			}
			space()
			if s[*i] == ',' {
				*i++
				space()
			}
		}
		*i++
		return false, nil
	default:
		begin := *i
		var value string
		if s[*i] == '"' {
			collectionSkipString(s, i)
			if *i-begin > 6*(maxLength-w.b.Len())+2 {
				return false, errTooLong
			}
			if err := json.Unmarshal([]byte(s[begin:*i]), &value); err != nil {
				return false, err
			}
		} else {
			for *i < len(s) && s[*i] != ',' && s[*i] != ']' && s[*i] != '}' && s[*i] != ' ' && s[*i] != '\n' && s[*i] != '\r' && s[*i] != '\t' {
				*i++
			}
			if *i-begin > maxLength-w.b.Len() {
				return false, errTooLong
			}
			value = string(s[begin:*i])
		}
		return w.scalar(value, depth, start)
	}
}

func collectionSkipString[T string | []byte](s T, i *int) {
	for *i++; s[*i] != '"'; *i++ {
		if s[*i] == '\\' {
			*i++
		}
	}
	*i++
}

// collectionTextError lets encoding/json label a saved MarshalText failure
// without invoking the caller's method twice.
type collectionTextError struct{ cause error }

func (e collectionTextError) MarshalText() ([]byte, error) { return nil, e.cause }
