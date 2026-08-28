package openapiclient

import (
	"fmt"
	"sort"
	"strings"
)

type swagger20Parameter struct {
	raw              swagger20Object
	resource         *swagger20Resource
	name             string
	in               Swagger20ParameterLocation
	typeName         string
	required         bool
	allowEmptyValue  bool
	collectionFormat string
	items            *swagger20Items
}

type swagger20Items struct {
	raw      swagger20Object
	typeName string
	items    *swagger20Items
}

type swagger20ParameterSet struct {
	all       []*swagger20Parameter
	nonBody   []*swagger20Parameter
	body      *swagger20Parameter
	byWire    map[Swagger20ParameterLocation]map[string]*swagger20Parameter
	qualified bool
}

func (p *swagger20Parameter) info() Swagger20ParameterInfo {
	return Swagger20ParameterInfo{Name: p.name, In: p.in, Type: p.typeName, Required: p.required}
}

func effectiveSwagger20Parameters(graph *swagger20ReferenceGraph, operation swagger20Operation) (*swagger20ParameterSet, error) {
	pathParameters, err := swagger20ParameterScope(
		graph, operation.pathItem.parameters(), operation.pathItem.resourceFor("parameters"), "Path Item",
	)
	if err != nil {
		return nil, err
	}
	operationParameters, err := swagger20ParameterScope(
		graph, operation.raw.array("parameters"), operation.resource, "Operation",
	)
	if err != nil {
		return nil, err
	}

	overridden := make(map[string]bool, len(operationParameters))
	for _, parameter := range operationParameters {
		overridden[parameter.identity()] = true
	}
	effective := make([]*swagger20Parameter, 0, len(pathParameters)+len(operationParameters))
	for _, parameter := range pathParameters {
		if !overridden[parameter.identity()] {
			effective = append(effective, parameter)
		}
	}
	effective = append(effective, operationParameters...)

	set := &swagger20ParameterSet{
		all: effective,
		byWire: map[Swagger20ParameterLocation]map[string]*swagger20Parameter{
			Swagger20ParameterPath: {}, Swagger20ParameterQuery: {}, Swagger20ParameterHeader: {}, Swagger20ParameterFormData: {},
		},
	}
	names := map[string]Swagger20ParameterLocation{}
	headerNames := map[string]string{}
	bodyCount := 0
	formCount := 0
	for _, parameter := range effective {
		if err := parameter.validateDeclaration(); err != nil {
			return nil, fmt.Errorf("effective %s parameter %q: %w", parameter.in, parameter.name, err)
		}
		if parameter.in == Swagger20ParameterBody {
			bodyCount++
			set.body = parameter
			continue
		}
		if parameter.in == Swagger20ParameterFormData {
			formCount++
		}
		set.nonBody = append(set.nonBody, parameter)
		set.byWire[parameter.in][parameter.name] = parameter
		if previous, present := names[parameter.name]; present && previous != parameter.in {
			set.qualified = true
		} else {
			names[parameter.name] = parameter.in
		}
		if parameter.in == Swagger20ParameterHeader {
			folded := strings.ToLower(parameter.name)
			if previous, present := headerNames[folded]; present && previous != parameter.name {
				return nil, fmt.Errorf("effective header parameters %q and %q differ only by ASCII case", previous, parameter.name)
			}
			headerNames[folded] = parameter.name
		}
	}
	if bodyCount > 1 {
		return nil, fmt.Errorf("effective parameter set contains more than one body parameter")
	}
	if bodyCount > 0 && formCount > 0 {
		return nil, fmt.Errorf("effective parameter set mixes body and formData parameters")
	}
	if err := validateSwagger20PathParameters(operation.path, set.byWire[Swagger20ParameterPath]); err != nil {
		return nil, err
	}
	return set, nil
}

func swagger20ParameterScope(graph *swagger20ReferenceGraph, member swagger20Member[[]any], resource *swagger20Resource, owner string) ([]*swagger20Parameter, error) {
	if !member.present {
		return nil, nil
	}
	if !member.valid {
		return nil, fmt.Errorf("selected Swagger 2.0 %s parameters field is not an array", owner)
	}
	parameters := make([]*swagger20Parameter, 0, len(member.value))
	identities := map[string]bool{}
	for index, value := range member.value {
		parameter, err := resolveSwagger20Parameter(graph, value, resource, map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("selected Swagger 2.0 %s parameter %d: %w", owner, index, err)
		}
		if parameter.name == "" || parameter.in == "" {
			return nil, fmt.Errorf("selected Swagger 2.0 %s parameter %d requires nonempty name and in", owner, index)
		}
		identity := parameter.identity()
		if identities[identity] {
			return nil, fmt.Errorf("selected Swagger 2.0 %s repeats parameter identity %s", owner, identity)
		}
		identities[identity] = true
		parameters = append(parameters, parameter)
	}
	return parameters, nil
}

func resolveSwagger20Parameter(graph *swagger20ReferenceGraph, value any, resource *swagger20Resource, active map[string]bool) (*swagger20Parameter, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Parameter or Reference Object is not an object")
	}
	raw := swagger20Object(object)
	if reference, present := raw.member("$ref"); present {
		ref, valid := reference.(string)
		if !valid || ref == "" {
			return nil, fmt.Errorf("Parameter Reference Object has an invalid $ref")
		}
		key := artifactResourceKey(resource.base()) + "|parameter|" + ref
		if active[key] {
			return nil, fmt.Errorf("selected Parameter reference cycle is not resolvable")
		}
		active[key] = true
		defer delete(active, key)
		resolved, err := graph.resolveReference(ref, resource, newSwagger20ResolutionMemo())
		if err != nil {
			return nil, err
		}
		if resolved.cycle {
			return nil, fmt.Errorf("selected Parameter reference cycle is not resolvable")
		}
		return resolveSwagger20Parameter(graph, resolved.node, resolved.resource, active)
	}
	name := raw.string("name")
	in := raw.string("in")
	return &swagger20Parameter{raw: raw, resource: resource, name: name.value, in: Swagger20ParameterLocation(in.value)}, nil
}

func (p *swagger20Parameter) identity() string {
	return string(p.in) + "\x00" + p.name
}

func (p *swagger20Parameter) validateDeclaration() error {
	name := p.raw.string("name")
	in := p.raw.string("in")
	if !name.present || !name.valid || name.value == "" || !in.present || !in.valid {
		return fmt.Errorf("name and in are required with string values")
	}
	p.name, p.in = name.value, Swagger20ParameterLocation(in.value)
	switch p.in {
	case Swagger20ParameterPath, Swagger20ParameterQuery, Swagger20ParameterHeader, Swagger20ParameterFormData, Swagger20ParameterBody:
	default:
		return fmt.Errorf("in value %q is not admitted", in.value)
	}

	required := p.raw.boolean("required")
	if required.present && !required.valid {
		return fmt.Errorf("required is not a boolean")
	}
	p.required = required.present && required.value
	if p.in == Swagger20ParameterPath && !p.required {
		return fmt.Errorf("path parameters require required: true")
	}

	if p.in == Swagger20ParameterBody {
		schema, present := p.raw.member("schema")
		if !present {
			return fmt.Errorf("body parameter requires schema")
		}
		if _, valid := schema.(map[string]any); !valid {
			return fmt.Errorf("body parameter schema is not an object")
		}
		return nil
	}

	typeName := p.raw.string("type")
	if !typeName.present || !typeName.valid {
		return fmt.Errorf("non-body parameter requires string type")
	}
	p.typeName = typeName.value
	switch p.typeName {
	case "string", "number", "integer", "boolean", "array":
	case "file":
		if p.in != Swagger20ParameterFormData {
			return fmt.Errorf("type file is admitted only for formData")
		}
	default:
		return fmt.Errorf("type %q is not admitted for %s", p.typeName, p.in)
	}

	allowEmpty := p.raw.boolean("allowEmptyValue")
	if allowEmpty.present && !allowEmpty.valid {
		return fmt.Errorf("allowEmptyValue is not a boolean")
	}
	if allowEmpty.present && p.in != Swagger20ParameterQuery && p.in != Swagger20ParameterFormData {
		return fmt.Errorf("allowEmptyValue applies only to query and formData")
	}
	p.allowEmptyValue = allowEmpty.present && allowEmpty.value

	collection := p.raw.string("collectionFormat")
	if collection.present && !collection.valid {
		return fmt.Errorf("collectionFormat is not a string")
	}
	if p.typeName == "array" {
		items := p.raw.object("items")
		if !items.present || !items.valid {
			return fmt.Errorf("array parameter requires an Items Object")
		}
		var err error
		p.items, err = parseSwagger20Items(items.value)
		if err != nil {
			return err
		}
		if p.items.typeName == "array" {
			return fmt.Errorf("nested non-body arrays have no unambiguous collection serialization")
		}
		p.collectionFormat = "csv"
		if collection.present {
			p.collectionFormat = collection.value
		}
		switch p.collectionFormat {
		case "csv", "ssv", "tsv", "pipes":
		case "multi":
			if p.in != Swagger20ParameterQuery && p.in != Swagger20ParameterFormData {
				return fmt.Errorf("collectionFormat multi is not admitted for %s", p.in)
			}
		default:
			return fmt.Errorf("collectionFormat %q is not admitted", p.collectionFormat)
		}
	} else if collection.present {
		return fmt.Errorf("collectionFormat applies only to array parameters")
	}

	if p.in == Swagger20ParameterHeader {
		if !swagger20HTTPFieldName(p.name) {
			return fmt.Errorf("header name is not an HTTP field-name")
		}
		switch strings.ToLower(p.name) {
		case "host", "content-length", "content-type":
			return fmt.Errorf("header parameter %q collides with a processor-owned field", p.name)
		}
	}
	if err := validateSwagger20AssertionDeclaration(p.raw, p.typeName); err != nil {
		return err
	}
	if err := validateSwagger20FormatDeclaration(p.raw); err != nil {
		return err
	}
	if value, present := p.raw.member("default"); present {
		if err := p.validateDeclaredValue(value); err != nil {
			return fmt.Errorf("default does not conform: %w", err)
		}
	}
	return nil
}

func parseSwagger20Items(raw swagger20Object) (*swagger20Items, error) {
	typeName := raw.string("type")
	if !typeName.present || !typeName.valid {
		return nil, fmt.Errorf("Items Object requires string type")
	}
	items := &swagger20Items{raw: raw, typeName: typeName.value}
	switch items.typeName {
	case "string", "number", "integer", "boolean":
	case "array":
		nested := raw.object("items")
		if !nested.present || !nested.valid {
			return nil, fmt.Errorf("nested array Items Object requires items")
		}
		var err error
		items.items, err = parseSwagger20Items(nested.value)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("Items Object type %q is not admitted", items.typeName)
	}
	if err := validateSwagger20AssertionDeclaration(raw, items.typeName); err != nil {
		return nil, fmt.Errorf("Items Object: %w", err)
	}
	if err := validateSwagger20FormatDeclaration(raw); err != nil {
		return nil, fmt.Errorf("Items Object: %w", err)
	}
	if value, present := raw.member("default"); present {
		if err := items.validateDeclaredValue(value); err != nil {
			return nil, fmt.Errorf("Items Object default does not conform: %w", err)
		}
	}
	return items, nil
}

func validateSwagger20PathParameters(path string, parameters map[string]*swagger20Parameter) error {
	expressions, err := swagger20PathExpressions(path)
	if err != nil {
		return err
	}
	for name := range expressions {
		if parameters[name] == nil {
			return fmt.Errorf("path template expression %q has no effective path parameter", name)
		}
	}
	for name := range parameters {
		if !expressions[name] {
			return fmt.Errorf("effective path parameter %q has no matching template expression", name)
		}
	}
	return nil
}

func swagger20PathExpressions(path string) (map[string]bool, error) {
	expressions := map[string]bool{}
	for index := 0; index < len(path); {
		switch path[index] {
		case '{':
			end := strings.IndexByte(path[index+1:], '}')
			if end < 0 {
				return nil, fmt.Errorf("path template has an unterminated expression")
			}
			end += index + 1
			name := path[index+1 : end]
			if name == "" || strings.ContainsAny(name, "{}") {
				return nil, fmt.Errorf("path template has a malformed expression")
			}
			expressions[name] = true
			index = end + 1
		case '}':
			return nil, fmt.Errorf("path template has an unmatched closing brace")
		default:
			index++
		}
	}
	return expressions, nil
}

func swagger20HTTPFieldName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func swagger20ParameterSort(parameters []*swagger20Parameter) {
	sort.SliceStable(parameters, func(left, right int) bool {
		if parameters[left].in != parameters[right].in {
			return parameters[left].in < parameters[right].in
		}
		return parameters[left].name < parameters[right].name
	})
}
