package openapiclient

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"golang.org/x/net/http/httpguts"
)

type parameterSerializationOptions struct {
	edition   string
	document  *openapi3.T
	converter ParameterConverter
}

// prepareParameterValue applies the OpenAPI 3.0/3.1 scalar and undefined-
// value mechanics before style expansion (openbindings.openapi-3.0@1 §§8.1–
// 8.3; openbindings.openapi-3.1@1 §§8.1–8.3).
func prepareParameterValue(parameter *openapi3.Parameter, value any, converter ParameterConverter) (any, bool, error) {
	return prepareParameterValueForEdition(parameter, value, converter, "")
}

func prepareParameterValueForEdition(parameter *openapi3.Parameter, value any, converter ParameterConverter, edition string) (any, bool, error) {
	if parameter == nil {
		return nil, false, fmt.Errorf("parameter has no effective declaration")
	}
	if len(parameter.Content) > 0 {
		if parameter.In == ParameterInQueryString {
			return value, false, nil
		}
		serialized, err := serializeParamContentFor(parameter, value, profileFullCoordinate)
		if err != nil {
			return nil, false, err
		}
		if parameter.In == openapi3.ParameterInHeader && !validSerializedHeaderFieldValue(serialized) {
			return nil, false, fmt.Errorf("serialized header is not a complete RFC 9110 field-value")
		}
		if parameter.In == openapi3.ParameterInHeader && strings.EqualFold(parameter.Name, "Cookie") && !validRFC6265CookieString(serialized) {
			return nil, false, fmt.Errorf("serialized raw Cookie header is not an RFC 6265 cookie-string")
		}
		return value, parameter.In == openapi3.ParameterInCookie, nil
	}

	method, err := revision3ParameterSerializationMethodForEdition(parameter, edition)
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
		if !validSerializedHeaderFieldValue(serialized) {
			return nil, false, fmt.Errorf("serialized header is not a complete RFC 9110 field-value")
		}
		if strings.EqualFold(parameter.Name, "Cookie") && !validRFC6265CookieString(serialized) {
			return nil, false, fmt.Errorf("serialized raw Cookie header is not an RFC 6265 cookie-string")
		}
	case openapi3.ParameterInCookie:
		is32 := strings.HasPrefix(edition, "3.2.")
		if method.Style == openapi3.SerializationForm && !is32 {
			if array, ok := asArray(prepared); ok && len(array) > 1 {
				return nil, false, fmt.Errorf("supplied array would produce a multi-value Cookie representation")
			}
			if object, ok := asObject(prepared); ok && len(object) > 1 {
				return nil, false, fmt.Errorf("supplied object would produce a multi-value Cookie representation")
			}
		}
		units, err := serializeCookieValue(parameter.Name, prepared, method.Style, method.Explode)
		if err != nil {
			return nil, false, err
		}
		if len(units) > 1 && method.Style == openapi3.SerializationForm && (!is32 || method.Explode) {
			return nil, false, fmt.Errorf("supplied value would produce multiple cookie pairs")
		}
		return prepared, len(units) > 0, nil
	}
	return prepared, false, nil
}

func validSerializedHeaderFieldValue(value string) bool {
	if value != "" && (value[0] == ' ' || value[0] == '\t' || value[len(value)-1] == ' ' || value[len(value)-1] == '\t') {
		return false
	}
	return httpguts.ValidHeaderFieldValue(value)
}

// undefinedStyleValue reports whether a supplied JSON value is one of RFC 6570
// §2.3's undefined values as this binding maps them: JSON null, an array with
// zero members, or an object with zero members. The empty string is expressly
// not undefined.
func undefinedStyleValue(value any) bool {
	if value == nil {
		return true
	}
	if arr, ok := asArray(value); ok {
		return len(arr) == 0
	}
	if obj, ok := asObject(value); ok {
		return len(obj) == 0
	}
	return false
}

func prepareParameterStyleValue(name string, value any, style string, converter ParameterConverter) (any, error) {
	// RFC 6570 §2.3's undefined values, mapped onto this binding's supplied
	// JSON values, are exactly three: JSON null, an array with zero members,
	// and an object with zero members. All three take the effective style's
	// `undefined` cell, which is the same on both explode rows, so they are
	// normalised to one representation here and expanded once below
	// (openbindings.openapi-3.0@1 §8.2; openbindings.openapi-3.1@1 §8.2;
	// openbindings.openapi-3.2@1 §8.2).
	if undefinedStyleValue(value) {
		switch style {
		case openapi3.SerializationMatrix, openapi3.SerializationLabel, openapi3.SerializationSimple, openapi3.SerializationForm, "cookie":
			return nil, nil
		default:
			return nil, fmt.Errorf("undefined value has n/a in style %q's undefined cell", style)
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

// prepareEncodingPropertyValue applies the OpenAPI form/multipart scalar and
// style mechanics before the selected body writer expands the property.
func prepareEncodingPropertyValue(plan *bodyPlan, name string, value any, converter ParameterConverter) (any, error) {
	if plan == nil || plan.media == nil {
		return value, nil
	}
	encoding := plan.media.Encoding[name]
	if !encodingUsesSerializationForPlan(plan, encoding) {
		// Content-based form and multipart properties are serialized by the
		// selected media lane. They never enter parameterConversion; that
		// configuration point belongs only to schema-form parameters and this
		// function's explicit RFC 6570-style Encoding branch below.
		return value, nil
	}
	method := revision3EncodingSerializationMethod(encoding)
	prepared, err := prepareParameterStyleValue(name, value, method.Style, converter)
	if err != nil {
		return nil, fmt.Errorf("body property %q: %w", name, err)
	}
	return prepared, nil
}

// contentPropertyNullIsElided implements the OAS 3.0 content-form cell where
// an optional nullable property contributes no form unit for JSON null.
func contentPropertyNullIsElided(plan *bodyPlan, name string, value any) bool {
	if value != nil || plan == nil || plan.media == nil || !plan.oas30 {
		return false
	}
	if encodingUsesSerializationForPlan(plan, plan.media.Encoding[name]) {
		return false
	}
	root := resolveDeclaration(mediaSchema(plan.media), true)
	return !root.requiresProperty(name) && root.property(name).admitsNull()
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
