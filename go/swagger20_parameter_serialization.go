package openapiclient

import (
	"fmt"
	"strings"
)

type swagger20WireContribution struct {
	name                string
	value               string
	valuePresent        bool
	structuralDelimiter string
	parameter           *swagger20Parameter
	octets              []byte
}

type swagger20RoutedInput struct {
	resolvedPath string
	query        []swagger20WireContribution
	headers      []swagger20WireContribution
	formData     []swagger20WireContribution
	body         any
	bodyPresent  bool
	formPresent  bool
}

func routeSwagger20Input(set *swagger20ParameterSet, path string, input Swagger20Input, options Swagger20PrepareOptions) (swagger20RoutedInput, error) {
	routed := swagger20RoutedInput{resolvedPath: path, body: input.Body, bodyPresent: input.BodyPresent}
	provided := map[Swagger20ParameterLocation]map[string]any{
		Swagger20ParameterPath:     input.Parameters.Path,
		Swagger20ParameterQuery:    input.Parameters.Query,
		Swagger20ParameterHeader:   input.Parameters.Header,
		Swagger20ParameterFormData: input.Parameters.FormData,
	}
	for location, values := range provided {
		for name := range values {
			if set.byWire[location][name] == nil {
				return swagger20RoutedInput{}, fmt.Errorf("unknown %s parameter %q", location, name)
			}
		}
	}
	if set.body == nil && input.BodyPresent {
		return swagger20RoutedInput{}, fmt.Errorf("body was supplied but the operation has no body parameter")
	}
	if set.body != nil && set.body.required && !input.BodyPresent {
		return swagger20RoutedInput{}, fmt.Errorf("required body is missing")
	}

	parameters := append([]*swagger20Parameter(nil), set.nonBody...)
	swagger20ParameterSort(parameters)
	for _, parameter := range parameters {
		values := provided[parameter.in]
		value, present := values[parameter.name]
		if !present {
			if parameter.required {
				return swagger20RoutedInput{}, fmt.Errorf("required %s parameter %q is missing", parameter.in, parameter.name)
			}
			continue
		}
		contributions, err := parameter.serialize(value, options.ParameterConverter, options.EmptyValueForm)
		if err != nil {
			return swagger20RoutedInput{}, err
		}
		switch parameter.in {
		case Swagger20ParameterPath:
			if len(contributions) != 1 || !contributions[0].valuePresent {
				return swagger20RoutedInput{}, fmt.Errorf("path parameter %q did not produce one value", parameter.name)
			}
			routed.resolvedPath = strings.ReplaceAll(
				routed.resolvedPath, "{"+parameter.name+"}", swagger20EncodeContribution(contributions[0]),
			)
		case Swagger20ParameterQuery:
			for _, contribution := range contributions {
				contribution.name = swagger20PercentEncode(contribution.name)
				if contribution.valuePresent {
					contribution.value = swagger20EncodeContribution(contribution)
				}
				routed.query = append(routed.query, contribution)
			}
		case Swagger20ParameterHeader:
			for _, contribution := range contributions {
				if !swagger20HTTPFieldValue(contribution.value) {
					return swagger20RoutedInput{}, fmt.Errorf("header parameter %q contains a field-invalid byte", parameter.name)
				}
				routed.headers = append(routed.headers, contribution)
			}
		case Swagger20ParameterFormData:
			routed.formPresent = true
			routed.formData = append(routed.formData, contributions...)
		}
	}
	return routed, nil
}

func (p *swagger20Parameter) serialize(value any, converter ParameterConverter, emptyForm Swagger20EmptyValueForm) ([]swagger20WireContribution, error) {
	if p.typeName == "file" {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("parameter %q requires a canonical Base64 file value", p.name)
		}
		octets, err := canonicalBase64BoundaryBytes("parameter "+p.name, text)
		if err != nil {
			return nil, err
		}
		return []swagger20WireContribution{{name: p.name, valuePresent: true, parameter: p, octets: octets}}, nil
	}
	converted, err := p.validateAndConvert(value, converter)
	if err != nil {
		return nil, err
	}
	// A supplied array with zero members is an empty value under every
	// collectionFormat, `multi` included: the four join formats join over no
	// members and `multi` contributes one instance carrying no value, so a
	// zero-member array never becomes indistinguishable from absence. Where
	// the flag reaches -- `query` and `formData` -- it is the same empty value
	// a supplied empty string is, and `emptyValueForm` selects its spelling.
	// At `path` and `header` the flag is inapplicable and the join below
	// substitutes zero characters (openbindings.openapi-2.0@1 §§8.1-8.2).
	emptyArray := p.typeName == "array" && len(converted) == 0 &&
		(p.in == Swagger20ParameterQuery || p.in == Swagger20ParameterFormData)
	if emptyArray || (p.typeName != "array" && len(converted) == 1 && converted[0] == "") {
		if !p.allowEmptyValue {
			if emptyArray {
				return nil, fmt.Errorf("parameter %q does not admit an empty value", p.name)
			}
			return nil, fmt.Errorf("parameter %q does not admit an empty string", p.name)
		}
		switch emptyForm {
		case Swagger20EmptyValueNameOnly:
			return []swagger20WireContribution{{name: p.name, parameter: p}}, nil
		case Swagger20EmptyValueEmpty:
			return []swagger20WireContribution{{name: p.name, valuePresent: true, parameter: p}}, nil
		default:
			// The declaration admits two empty spellings that produce distinct
			// bytes, and openbindings.openapi-2.0@1 §8.1 states that no binding
			// default prefers one. The choice is awaited, so the refusal names
			// it (§3.2's context-required species).
			return nil, swagger20ConfigRequired("emptyValueForm", "")
		}
	}
	if p.typeName != "array" {
		return []swagger20WireContribution{{name: p.name, value: converted[0], valuePresent: true, parameter: p}}, nil
	}
	if p.collectionFormat == "multi" {
		result := make([]swagger20WireContribution, len(converted))
		for index, member := range converted {
			result[index] = swagger20WireContribution{name: p.name, value: member, valuePresent: true, parameter: p}
		}
		return result, nil
	}
	delimiter := swagger20CollectionDelimiter(p.collectionFormat)
	for index, member := range converted {
		if strings.Contains(member, delimiter) {
			return nil, fmt.Errorf("parameter %q array member %d contains its %s structural delimiter", p.name, index, p.collectionFormat)
		}
	}
	return []swagger20WireContribution{{
		name: p.name, value: strings.Join(converted, delimiter), valuePresent: true, structuralDelimiter: delimiter, parameter: p,
	}}, nil
}

func swagger20CollectionDelimiter(collectionFormat string) string {
	switch collectionFormat {
	case "ssv":
		return " "
	case "tsv":
		return "\t"
	case "pipes":
		return "|"
	default:
		return ","
	}
}

func swagger20RawQuery(contributions []swagger20WireContribution) string {
	parts := make([]string, len(contributions))
	for index, contribution := range contributions {
		parts[index] = contribution.name
		if contribution.valuePresent {
			parts[index] += "=" + contribution.value
		}
	}
	return strings.Join(parts, "&")
}

func swagger20PercentEncode(value string) string {
	const hexadecimal = "0123456789ABCDEF"
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-._~", rune(character)) {
			result.WriteByte(character)
			continue
		}
		result.WriteByte('%')
		result.WriteByte(hexadecimal[character>>4])
		result.WriteByte(hexadecimal[character&15])
	}
	return result.String()
}

func swagger20EncodeContribution(contribution swagger20WireContribution) string {
	if contribution.structuralDelimiter == "" {
		return swagger20PercentEncode(contribution.value)
	}
	members := strings.Split(contribution.value, contribution.structuralDelimiter)
	for index := range members {
		members[index] = swagger20PercentEncode(members[index])
	}
	return strings.Join(members, contribution.structuralDelimiter)
}

func swagger20HTTPFieldValue(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '\t' || (character >= 0x20 && character != 0x7f) {
			continue
		}
		return false
	}
	return true
}
