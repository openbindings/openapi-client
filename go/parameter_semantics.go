package openapiclient

import (
	"encoding/json"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
	"golang.org/x/net/http/httpguts"
)

type parameterSerializationOptions struct {
	edition   string
	converter ParameterConverter
}

// prepareParameterValue applies the OpenAPI 3.0/3.1 scalar and undefined-
// value mechanics before style expansion (openbindings.openapi-3.0@1 §§8.1–
// 8.3; openbindings.openapi-3.1@1 §§8.1–8.3).
func prepareParameterValue(parameter *openapi3.Parameter, value any, converter ParameterConverter) (any, bool, error) {
	if parameter == nil {
		return nil, false, fmt.Errorf("parameter has no effective declaration")
	}
	if len(parameter.Content) > 0 {
		serialized, err := serializeParamContentFor(parameter, value, profileFullCoordinate)
		if err != nil {
			return nil, false, err
		}
		if parameter.In == openapi3.ParameterInHeader && !httpguts.ValidHeaderFieldValue(serialized) {
			return nil, false, fmt.Errorf("serialized header contains an invalid HTTP field byte")
		}
		return value, parameter.In == openapi3.ParameterInCookie, nil
	}

	method, err := revision3ParameterSerializationMethod(parameter)
	if err != nil {
		return nil, false, err
	}
	prepared, err := prepareParameterStyleValue(parameter.Name, value, method.Style, converter)
	if err != nil {
		return nil, false, err
	}
	switch parameter.In {
	case openapi3.ParameterInHeader:
		serialized, err := serializeHeaderValue(prepared, method.Style, method.Explode)
		if err != nil {
			return nil, false, err
		}
		if !httpguts.ValidHeaderFieldValue(serialized) {
			return nil, false, fmt.Errorf("serialized header contains an invalid HTTP field byte")
		}
	case openapi3.ParameterInCookie:
		units, err := serializeCookieValue(parameter.Name, prepared, method.Style, method.Explode)
		if err != nil {
			return nil, false, err
		}
		if len(units) > 1 {
			return nil, false, fmt.Errorf("supplied value would produce multiple cookie pairs")
		}
		return prepared, len(units) > 0, nil
	}
	return prepared, false, nil
}

func prepareParameterStyleValue(name string, value any, style string, converter ParameterConverter) (any, error) {
	if value == nil {
		switch style {
		case openapi3.SerializationMatrix, openapi3.SerializationLabel, openapi3.SerializationSimple, openapi3.SerializationForm:
			return nil, nil
		default:
			return nil, fmt.Errorf("JSON null has n/a in style %q's undefined cell", style)
		}
	}
	converted, err := convertParameterScalars(value, converter, false)
	if err != nil {
		return nil, err
	}
	if delimiter := nonRFCStyleDelimiter(style); delimiter != "" {
		if containsAnyDelimiter(name, delimiter) || styleValueContainsDelimiter(converted, delimiter) {
			return nil, fmt.Errorf("value or member name contains style %q's structural delimiter", style)
		}
	}
	switch style {
	case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
		if _, array := asArray(converted); !array {
			if _, object := asObject(converted); !object {
				return nil, fmt.Errorf("style %q is defined only for arrays or objects", style)
			}
		}
	case openapi3.SerializationDeepObject:
		if _, object := asObject(converted); !object {
			return nil, fmt.Errorf("style deepObject is defined only for objects")
		}
	}
	return converted, nil
}

func convertParameterScalars(value any, converter ParameterConverter, member bool) (any, error) {
	if value == nil {
		if member {
			return nil, fmt.Errorf("null array/object member has no RFC 6570 representation")
		}
		return nil, nil
	}
	if array, ok := asArray(value); ok {
		result := make([]any, len(array))
		for index, item := range array {
			converted, err := convertParameterScalars(item, converter, true)
			if err != nil {
				return nil, fmt.Errorf("array member %d: %w", index, err)
			}
			result[index] = converted
		}
		return result, nil
	}
	if object, ok := asObject(value); ok {
		result := make(map[string]any, len(object))
		for name, item := range object {
			converted, err := convertParameterScalars(item, converter, true)
			if err != nil {
				return nil, fmt.Errorf("object member %q: %w", name, err)
			}
			result[name] = converted
		}
		return result, nil
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	if !jsonBooleanOrNumber(value) {
		return nil, fmt.Errorf("value of type %T is outside the JSON scalar conversion domain", value)
	}
	if converter == nil {
		return nil, fmt.Errorf("JSON boolean or number requires a ParameterConverter")
	}
	text, err := converter(value)
	if err != nil {
		return nil, fmt.Errorf("ParameterConverter: %w", err)
	}
	return text, nil
}

func jsonBooleanOrNumber(value any) bool {
	switch value.(type) {
	case bool, json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func nonRFCStyleDelimiter(style string) string {
	switch style {
	case openapi3.SerializationSpaceDelimited:
		return " "
	case openapi3.SerializationPipeDelimited:
		return "|"
	case openapi3.SerializationDeepObject:
		return "[]=&"
	default:
		return ""
	}
}

func containsAnyDelimiter(value, delimiters string) bool {
	for _, delimiter := range delimiters {
		for _, char := range value {
			if char == delimiter {
				return true
			}
		}
	}
	return false
}

func styleValueContainsDelimiter(value any, delimiters string) bool {
	if array, ok := asArray(value); ok {
		for _, item := range array {
			if text, ok := item.(string); ok && containsAnyDelimiter(text, delimiters) {
				return true
			}
		}
	}
	if object, ok := asObject(value); ok {
		for name, item := range object {
			if containsAnyDelimiter(name, delimiters) {
				return true
			}
			if text, ok := item.(string); ok && containsAnyDelimiter(text, delimiters) {
				return true
			}
		}
	}
	return false
}

// parameterDeclaration is the resolved type-set slice needed by the
// parameter style table. The complete schema-analysis view is shared with the
// media lanes in resolved_declaration.go.
type parameterDeclaration struct {
	types     map[string]bool
	ambiguous bool
}

func resolveDeclaration(schema *openapi3.Schema, oas30 bool) parameterDeclaration {
	conjuncts, ambiguous := parameterDeclarationConjuncts(schema, oas30, map[*openapi3.Schema]bool{})
	result := parameterDeclaration{ambiguous: ambiguous}
	if ambiguous {
		return result
	}
	constrained := false
	for _, conjunct := range conjuncts {
		candidate, present := parameterDeclarationTypeSet(conjunct, oas30)
		if !present {
			continue
		}
		if !constrained {
			result.types = candidate
			constrained = true
			continue
		}
		for member := range result.types {
			if !candidate[member] {
				delete(result.types, member)
			}
		}
	}
	return result
}

func parameterDeclarationConjuncts(schema *openapi3.Schema, oas30 bool, seen map[*openapi3.Schema]bool) ([]*openapi3.Schema, bool) {
	if schema == nil || seen[schema] {
		return nil, false
	}
	seen[schema] = true
	defer delete(seen, schema)
	conjuncts := []*openapi3.Schema{schema}
	for _, choice := range []openapi3.SchemaRefs{schema.AnyOf, schema.OneOf} {
		if len(choice) == 0 {
			continue
		}
		var selected *openapi3.Schema
		for _, branch := range choice {
			if branch == nil || branch.Value == nil {
				return nil, true
			}
			resolved := resolveDeclaration(branch.Value, oas30)
			if resolved.declaresOnly("null") {
				continue
			}
			if selected != nil {
				return nil, true
			}
			selected = branch.Value
		}
		if selected == nil {
			return nil, true
		}
		members, nestedAmbiguous := parameterDeclarationConjuncts(selected, oas30, seen)
		if nestedAmbiguous {
			return nil, true
		}
		conjuncts = append(conjuncts, members...)
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		members, nestedAmbiguous := parameterDeclarationConjuncts(member.Value, oas30, seen)
		if nestedAmbiguous {
			return nil, true
		}
		conjuncts = append(conjuncts, members...)
	}
	return conjuncts, false
}

func parameterDeclarationTypeSet(schema *openapi3.Schema, oas30 bool) (map[string]bool, bool) {
	if schema == nil || schema.Type == nil {
		return nil, false
	}
	types := schema.Type.Slice()
	if len(types) == 0 || oas30 && len(types) != 1 {
		return nil, false
	}
	result := make(map[string]bool, len(types)+1)
	for _, member := range types {
		result[member] = true
	}
	if oas30 && schema.Nullable {
		result["null"] = true
	}
	return result, true
}

func (d parameterDeclaration) declaresOnly(allowed ...string) bool {
	if d.ambiguous || len(d.types) == 0 {
		return false
	}
	set := make(map[string]bool, len(allowed))
	for _, member := range allowed {
		set[member] = true
	}
	for member := range d.types {
		if !set[member] {
			return false
		}
	}
	return true
}
