package openapiclient

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
)

func validateSwagger20AssertionDeclaration(raw swagger20Object, typeName string) error {
	for _, name := range []string{"multipleOf", "maximum", "minimum"} {
		if value, present := raw.member(name); present {
			number, ok := swagger20NumberRat(value)
			if !ok {
				return fmt.Errorf("%s is not a finite number", name)
			}
			if name == "multipleOf" && number.Sign() <= 0 {
				return fmt.Errorf("multipleOf is not greater than zero")
			}
		}
	}
	for _, name := range []string{"exclusiveMaximum", "exclusiveMinimum", "uniqueItems"} {
		if member := raw.boolean(name); member.present && !member.valid {
			return fmt.Errorf("%s is not a boolean", name)
		}
	}
	if exclusive := raw.boolean("exclusiveMaximum"); exclusive.present && exclusive.value {
		if _, present := raw.member("maximum"); !present {
			return fmt.Errorf("exclusiveMaximum requires maximum")
		}
	}
	if exclusive := raw.boolean("exclusiveMinimum"); exclusive.present && exclusive.value {
		if _, present := raw.member("minimum"); !present {
			return fmt.Errorf("exclusiveMinimum requires minimum")
		}
	}
	for _, name := range []string{"maxLength", "minLength", "maxItems", "minItems"} {
		if value, present := raw.member(name); present {
			if _, ok := swagger20NonnegativeInteger(value); !ok {
				return fmt.Errorf("%s is not a nonnegative integer", name)
			}
		}
	}
	if pattern := raw.string("pattern"); pattern.present {
		if !pattern.valid {
			return fmt.Errorf("pattern is not a string")
		}
		if _, err := regexp2.Compile(pattern.value, regexp2.ECMAScript); err != nil {
			return fmt.Errorf("pattern is not an ECMA-262 regular expression: %w", err)
		}
	}
	if enum, present := raw.member("enum"); present {
		values, ok := enum.([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("enum is not a nonempty array")
		}
		for left := range values {
			for right := 0; right < left; right++ {
				if swagger20JSONEqual(values[left], values[right]) {
					return fmt.Errorf("enum contains duplicate JSON values")
				}
			}
		}
	}
	_ = typeName
	return nil
}

func (p *swagger20Parameter) validateAndConvert(value any, converter ParameterConverter) ([]string, error) {
	if p.typeName == "array" {
		array, ok := swagger20Array(value)
		if !ok {
			return nil, fmt.Errorf("parameter %q requires a JSON array", p.name)
		}
		if err := validateSwagger20Assertions(p.raw, value, "array"); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", p.name, err)
		}
		converted := make([]string, len(array))
		for index, member := range array {
			if member != nil {
				if err := validateSwagger20ValueType(member, p.items.typeName); err != nil {
					return nil, fmt.Errorf("parameter %q array member %d: %w", p.name, index, err)
				}
				if err := validateSwagger20Assertions(p.items.raw, member, p.items.typeName); err != nil {
					return nil, fmt.Errorf("parameter %q array member %d: %w", p.name, index, err)
				}
				if err := validateSwagger20Format(p.items.raw, member, p.items.typeName); err != nil {
					return nil, fmt.Errorf("parameter %q array member %d: %w", p.name, index, err)
				}
			}
			text, err := convertSwagger20Scalar(member, converter)
			if err != nil {
				return nil, fmt.Errorf("parameter %q array member %d: %w", p.name, index, err)
			}
			converted[index] = text
		}
		return converted, nil
	}
	if value != nil {
		if err := validateSwagger20ValueType(value, p.typeName); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", p.name, err)
		}
		if err := validateSwagger20Assertions(p.raw, value, p.typeName); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", p.name, err)
		}
		if err := validateSwagger20Format(p.raw, value, p.typeName); err != nil {
			return nil, fmt.Errorf("parameter %q: %w", p.name, err)
		}
	}
	text, err := convertSwagger20Scalar(value, converter)
	if err != nil {
		return nil, fmt.Errorf("parameter %q: %w", p.name, err)
	}
	return []string{text}, nil
}

func (p *swagger20Parameter) validateDeclaredValue(value any) error {
	if p.typeName == "file" {
		return fmt.Errorf("file has no JSON default representation")
	}
	if p.typeName == "array" {
		array, ok := swagger20Array(value)
		if !ok {
			return fmt.Errorf("value is not an array")
		}
		if err := validateSwagger20Assertions(p.raw, value, "array"); err != nil {
			return err
		}
		for index, member := range array {
			if err := p.items.validateDeclaredValue(member); err != nil {
				return fmt.Errorf("member %d: %w", index, err)
			}
		}
		return nil
	}
	if value == nil {
		return fmt.Errorf("null does not satisfy %s", p.typeName)
	}
	if err := validateSwagger20ValueType(value, p.typeName); err != nil {
		return err
	}
	if err := validateSwagger20Assertions(p.raw, value, p.typeName); err != nil {
		return err
	}
	return validateSwagger20Format(p.raw, value, p.typeName)
}

func (i *swagger20Items) validateDeclaredValue(value any) error {
	if value == nil {
		return fmt.Errorf("null does not satisfy %s", i.typeName)
	}
	if err := validateSwagger20ValueType(value, i.typeName); err != nil {
		return err
	}
	if err := validateSwagger20Assertions(i.raw, value, i.typeName); err != nil {
		return err
	}
	if err := validateSwagger20Format(i.raw, value, i.typeName); err != nil {
		return err
	}
	if i.typeName == "array" {
		array, _ := swagger20Array(value)
		for index, member := range array {
			if err := i.items.validateDeclaredValue(member); err != nil {
				return fmt.Errorf("member %d: %w", index, err)
			}
		}
	}
	return nil
}

func validateSwagger20FormatDeclaration(raw swagger20Object) error {
	format := raw.string("format")
	if format.present && !format.valid {
		return fmt.Errorf("format is not a string")
	}
	return nil
}

func validateSwagger20Format(raw swagger20Object, value any, typeName string) error {
	format := raw.string("format")
	if !format.present || !format.valid {
		return nil
	}
	switch typeName + "/" + format.value {
	case "integer/int32":
		number, _ := swagger20NumberRat(value)
		if number.Cmp(big.NewRat(-2147483648, 1)) < 0 || number.Cmp(big.NewRat(2147483647, 1)) > 0 {
			return fmt.Errorf("value is outside int32")
		}
	case "integer/int64":
		number, _ := swagger20NumberRat(value)
		minimum := new(big.Rat).SetInt(new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 63)))
		maximum := new(big.Rat).SetInt(new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 63), big.NewInt(1)))
		if number.Cmp(minimum) < 0 || number.Cmp(maximum) > 0 {
			return fmt.Errorf("value is outside int64")
		}
	case "number/float":
		if !swagger20FitsIEEE(value, 32) {
			return fmt.Errorf("value is outside finite float")
		}
	case "number/double":
		if !swagger20FitsIEEE(value, 64) {
			return fmt.Errorf("value is outside finite double")
		}
	case "string/byte":
		if _, err := base64.StdEncoding.DecodeString(value.(string)); err != nil {
			return fmt.Errorf("value is not Base64 characters")
		}
	case "string/date":
		if _, err := time.Parse("2006-01-02", value.(string)); err != nil {
			return fmt.Errorf("value is not an RFC 3339 full-date")
		}
	case "string/date-time":
		if _, err := time.Parse(time.RFC3339, value.(string)); err != nil {
			return fmt.Errorf("value is not an RFC 3339 date-time")
		}
	}
	return nil
}

func swagger20FitsIEEE(value any, bitSize int) bool {
	text, ok := swagger20NumberText(value)
	if !ok {
		return false
	}
	number, err := strconv.ParseFloat(text, bitSize)
	return err == nil && !math.IsInf(number, 0) && !math.IsNaN(number)
}

func swagger20NumberText(value any) (string, bool) {
	switch typed := value.(type) {
	case json.Number:
		return typed.String(), true
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), !math.IsInf(typed, 0) && !math.IsNaN(typed)
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32), !math.IsInf(float64(typed), 0) && !math.IsNaN(float64(typed))
	case int:
		return strconv.FormatInt(int64(typed), 10), true
	case int8:
		return strconv.FormatInt(int64(typed), 10), true
	case int16:
		return strconv.FormatInt(int64(typed), 10), true
	case int32:
		return strconv.FormatInt(int64(typed), 10), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case uint:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint64:
		return strconv.FormatUint(typed, 10), true
	default:
		return "", false
	}
}

func convertSwagger20Scalar(value any, converter ParameterConverter) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	if !swagger20ConvertibleScalar(value) {
		return "", fmt.Errorf("value of type %T is outside the JSON scalar conversion domain", value)
	}
	if converter == nil {
		return "", fmt.Errorf("JSON boolean, number, or null requires parameterConversion")
	}
	text, err := converter(value)
	if err != nil {
		return "", fmt.Errorf("parameterConversion: %w", err)
	}
	return text, nil
}

func swagger20ConvertibleScalar(value any) bool {
	if value == nil {
		return true
	}
	switch value.(type) {
	case bool, json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func validateSwagger20ValueType(value any, typeName string) error {
	switch typeName {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("value of type %T is not string", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("value of type %T is not boolean", value)
		}
	case "number":
		if _, ok := swagger20NumberRat(value); !ok {
			return fmt.Errorf("value of type %T is not a finite number", value)
		}
	case "integer":
		if !swagger20LiteralInteger(value) {
			return fmt.Errorf("value %v is not a literal JSON integer", value)
		}
	case "array":
		if _, ok := swagger20Array(value); !ok {
			return fmt.Errorf("value of type %T is not array", value)
		}
	default:
		return fmt.Errorf("unsupported declared type %q", typeName)
	}
	return nil
}

func validateSwagger20Assertions(raw swagger20Object, value any, typeName string) error {
	if enum, present := raw.member("enum"); present {
		matched := false
		for _, candidate := range enum.([]any) {
			if swagger20JSONEqual(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("value is outside enum")
		}
	}
	switch typeName {
	case "number", "integer":
		number, _ := swagger20NumberRat(value)
		if multiple, present := raw.member("multipleOf"); present {
			divisor, _ := swagger20NumberRat(multiple)
			if !new(big.Rat).Quo(number, divisor).IsInt() {
				return fmt.Errorf("value is not a multipleOf %v", multiple)
			}
		}
		if maximum, present := raw.member("maximum"); present {
			limit, _ := swagger20NumberRat(maximum)
			comparison := number.Cmp(limit)
			exclusive := raw.boolean("exclusiveMaximum")
			if comparison > 0 || (comparison == 0 && exclusive.present && exclusive.value) {
				return fmt.Errorf("value exceeds maximum")
			}
		}
		if minimum, present := raw.member("minimum"); present {
			limit, _ := swagger20NumberRat(minimum)
			comparison := number.Cmp(limit)
			exclusive := raw.boolean("exclusiveMinimum")
			if comparison < 0 || (comparison == 0 && exclusive.present && exclusive.value) {
				return fmt.Errorf("value is below minimum")
			}
		}
	case "string":
		text := value.(string)
		length := utf8.RuneCountInString(text)
		if maximum, present := raw.member("maxLength"); present {
			limit, _ := swagger20NonnegativeInteger(maximum)
			if uint64(length) > limit {
				return fmt.Errorf("string exceeds maxLength")
			}
		}
		if minimum, present := raw.member("minLength"); present {
			limit, _ := swagger20NonnegativeInteger(minimum)
			if uint64(length) < limit {
				return fmt.Errorf("string is below minLength")
			}
		}
		if pattern := raw.string("pattern"); pattern.present {
			compiled, _ := regexp2.Compile(pattern.value, regexp2.ECMAScript)
			matched, err := compiled.MatchString(text)
			if err != nil {
				return fmt.Errorf("pattern evaluation failed: %w", err)
			}
			if !matched {
				return fmt.Errorf("string does not match pattern")
			}
		}
	case "array":
		array, _ := swagger20Array(value)
		if maximum, present := raw.member("maxItems"); present {
			limit, _ := swagger20NonnegativeInteger(maximum)
			if uint64(len(array)) > limit {
				return fmt.Errorf("array exceeds maxItems")
			}
		}
		if minimum, present := raw.member("minItems"); present {
			limit, _ := swagger20NonnegativeInteger(minimum)
			if uint64(len(array)) < limit {
				return fmt.Errorf("array is below minItems")
			}
		}
		if unique := raw.boolean("uniqueItems"); unique.present && unique.value {
			for left := range array {
				for right := 0; right < left; right++ {
					if swagger20JSONEqual(array[left], array[right]) {
						return fmt.Errorf("array violates uniqueItems")
					}
				}
			}
		}
	}
	return nil
}

func swagger20Array(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []string:
		result := make([]any, len(typed))
		for index, member := range typed {
			result[index] = member
		}
		return result, true
	default:
		return nil, false
	}
}

func swagger20LiteralInteger(value any) bool {
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		if text == "0" {
			return true
		}
		if strings.HasPrefix(text, "-") {
			text = text[1:]
		}
		if text == "0" {
			return true
		}
		if text == "" || text[0] == '0' {
			return false
		}
		for index := range text {
			if text[index] < '0' || text[index] > '9' {
				return false
			}
		}
		return true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func swagger20NumberRat(value any) (*big.Rat, bool) {
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case float64:
		if math.IsInf(typed, 0) || math.IsNaN(typed) {
			return nil, false
		}
		text = strconv.FormatFloat(typed, 'g', -1, 64)
	case float32:
		if math.IsInf(float64(typed), 0) || math.IsNaN(float64(typed)) {
			return nil, false
		}
		text = strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case int:
		text = strconv.FormatInt(int64(typed), 10)
	case int8:
		text = strconv.FormatInt(int64(typed), 10)
	case int16:
		text = strconv.FormatInt(int64(typed), 10)
	case int32:
		text = strconv.FormatInt(int64(typed), 10)
	case int64:
		text = strconv.FormatInt(typed, 10)
	case uint:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint8:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint16:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint32:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		text = strconv.FormatUint(typed, 10)
	default:
		return nil, false
	}
	rational := new(big.Rat)
	if _, ok := rational.SetString(text); !ok {
		return nil, false
	}
	return rational, true
}

func swagger20NonnegativeInteger(value any) (uint64, bool) {
	if !swagger20LiteralInteger(value) {
		return 0, false
	}
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return 0, false
		}
		text = string(encoded)
	}
	if strings.HasPrefix(text, "-") {
		return 0, false
	}
	result, err := strconv.ParseUint(text, 10, 64)
	return result, err == nil
}

func swagger20JSONEqual(left, right any) bool {
	if leftNumber, leftOK := swagger20NumberRat(left); leftOK {
		if rightNumber, rightOK := swagger20NumberRat(right); rightOK {
			return leftNumber.Cmp(rightNumber) == 0
		}
	}
	leftArray, leftIsArray := swagger20Array(left)
	rightArray, rightIsArray := swagger20Array(right)
	if leftIsArray || rightIsArray {
		if !leftIsArray || !rightIsArray || len(leftArray) != len(rightArray) {
			return false
		}
		for index := range leftArray {
			if !swagger20JSONEqual(leftArray[index], rightArray[index]) {
				return false
			}
		}
		return true
	}
	leftObject, leftIsObject := left.(map[string]any)
	rightObject, rightIsObject := right.(map[string]any)
	if leftIsObject || rightIsObject {
		if !leftIsObject || !rightIsObject || len(leftObject) != len(rightObject) {
			return false
		}
		for name, value := range leftObject {
			other, present := rightObject[name]
			if !present || !swagger20JSONEqual(value, other) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(left, right)
}
