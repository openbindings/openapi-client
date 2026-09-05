package openapiclient

import (
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"
)

// marshalRequestJSON preserves the binding's JSON-value boundary. Go's
// encoding/json silently substitutes U+FFFD for invalid UTF-8 in strings;
// that would change the caller's value on the wire. Reject such values before
// handing them to the standard encoder.
func marshalRequestJSON(value any) ([]byte, error) {
	if err := validateJSONStrings(reflect.ValueOf(value), map[visit]bool{}); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

type visit struct {
	typeName reflect.Type
	pointer  uintptr
}

func validateJSONStrings(value reflect.Value, active map[visit]bool) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return validateJSONStrings(value.Elem(), active)
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		key := visit{typeName: value.Type(), pointer: value.Pointer()}
		if active[key] {
			return nil
		}
		active[key] = true
		defer delete(active, key)
		return validateJSONStrings(value.Elem(), active)
	}
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return fmt.Errorf("request JSON value contains an invalid Unicode string")
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		key := visit{typeName: value.Type(), pointer: value.Pointer()}
		if active[key] {
			return nil
		}
		active[key] = true
		defer delete(active, key)
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateJSONStrings(iterator.Key(), active); err != nil {
				return err
			}
			if err := validateJSONStrings(iterator.Value(), active); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		key := visit{typeName: value.Type(), pointer: value.Pointer()}
		if active[key] {
			return nil
		}
		active[key] = true
		defer delete(active, key)
		for index := 0; index < value.Len(); index++ {
			if err := validateJSONStrings(value.Index(index), active); err != nil {
				return err
			}
		}
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateJSONStrings(value.Index(index), active); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			field := value.Field(index)
			if field.CanInterface() {
				if err := validateJSONStrings(field, active); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
