package openapiclient

import (
	"fmt"
	"strings"
)

type swagger20SchemaDeclaration struct {
	typed   bool
	types   map[string]bool
	formats map[string]bool
}

func resolveSwagger20SchemaDeclaration(graph *swagger20ReferenceGraph, value any, resource *swagger20Resource, allowFile bool) (swagger20SchemaDeclaration, error) {
	return resolveSwagger20SchemaDeclarationActive(graph, value, resource, allowFile, map[string]bool{})
}

func resolveSwagger20SchemaDeclarationActive(graph *swagger20ReferenceGraph, value any, resource *swagger20Resource, allowFile bool, active map[string]bool) (swagger20SchemaDeclaration, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return swagger20SchemaDeclaration{}, fmt.Errorf("Schema Object is not an object")
	}
	raw := swagger20Object(object)
	if reference, present := raw.member("$ref"); present {
		ref, valid := reference.(string)
		if !valid || ref == "" {
			return swagger20SchemaDeclaration{}, fmt.Errorf("Schema Object has an invalid $ref")
		}
		key := artifactResourceKey(resource.base()) + "|schema|" + ref
		if active[key] {
			return swagger20SchemaDeclaration{}, fmt.Errorf("selected Schema reference cycle does not resolve a carriage declaration")
		}
		active[key] = true
		defer delete(active, key)
		resolved, err := graph.resolveReference(ref, resource, newSwagger20ResolutionMemo())
		if err != nil {
			return swagger20SchemaDeclaration{}, err
		}
		if resolved.cycle {
			return swagger20SchemaDeclaration{}, fmt.Errorf("selected Schema reference cycle does not resolve a carriage declaration")
		}
		return resolveSwagger20SchemaDeclarationActive(graph, resolved.node, resolved.resource, allowFile, active)
	}

	declaration := swagger20SchemaDeclaration{}
	if typeValue, present := raw.member("type"); present {
		types, err := swagger20SchemaTypes(typeValue, allowFile)
		if err != nil {
			return swagger20SchemaDeclaration{}, err
		}
		declaration.typed = true
		declaration.types = types
	}
	if format := raw.string("format"); format.present {
		if !format.valid {
			return swagger20SchemaDeclaration{}, fmt.Errorf("Schema Object format is not a string")
		}
		declaration.formats = map[string]bool{format.value: true}
	}
	if branches := raw.array("allOf"); branches.present {
		if !branches.valid || len(branches.value) == 0 {
			return swagger20SchemaDeclaration{}, fmt.Errorf("Schema Object allOf is not a nonempty array")
		}
		for index, branch := range branches.value {
			resolved, err := resolveSwagger20SchemaDeclarationActive(graph, branch, resource, allowFile, active)
			if err != nil {
				return swagger20SchemaDeclaration{}, fmt.Errorf("Schema Object allOf member %d: %w", index, err)
			}
			declaration = conjoinSwagger20SchemaDeclarations(declaration, resolved)
		}
	}
	return declaration, nil
}

func swagger20SchemaTypes(value any, allowFile bool) (map[string]bool, error) {
	values := []any{value}
	if array, ok := value.([]any); ok {
		if len(array) == 0 {
			return nil, fmt.Errorf("Schema Object type array is empty")
		}
		values = array
	}
	result := make(map[string]bool, len(values))
	for _, member := range values {
		name, ok := member.(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("Schema Object type contains a non-string or empty member")
		}
		switch name {
		case "null", "boolean", "object", "array", "number", "string", "integer":
		case "file":
			if !allowFile {
				return nil, fmt.Errorf("Schema Object type file is admitted only at a response root")
			}
		default:
			return nil, fmt.Errorf("Schema Object type %q is not admitted", name)
		}
		if result[name] {
			return nil, fmt.Errorf("Schema Object type repeats %q", name)
		}
		result[name] = true
	}
	return result, nil
}

func conjoinSwagger20SchemaDeclarations(left, right swagger20SchemaDeclaration) swagger20SchemaDeclaration {
	result := swagger20SchemaDeclaration{}
	switch {
	case left.typed && right.typed:
		result.typed = true
		result.types = map[string]bool{}
		for name := range left.types {
			if right.types[name] {
				result.types[name] = true
			}
		}
	case left.typed:
		result.typed = true
		result.types = cloneSwagger20StringSet(left.types)
	case right.typed:
		result.typed = true
		result.types = cloneSwagger20StringSet(right.types)
	}
	result.formats = cloneSwagger20StringSet(left.formats)
	if result.formats == nil && len(right.formats) > 0 {
		result.formats = map[string]bool{}
	}
	for format := range right.formats {
		result.formats[format] = true
	}
	return result
}

func cloneSwagger20StringSet(input map[string]bool) map[string]bool {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]bool, len(input))
	for name := range input {
		result[name] = true
	}
	return result
}

func (d swagger20SchemaDeclaration) admitsStringAsSoleNonNullType() bool {
	if !d.typed || !d.types["string"] {
		return false
	}
	for name := range d.types {
		if name != "string" && name != "null" {
			return false
		}
	}
	return true
}

func (d swagger20SchemaDeclaration) format() (string, bool) {
	if len(d.formats) == 0 {
		return "", false
	}
	if len(d.formats) != 1 {
		return "", true
	}
	for format := range d.formats {
		return format, false
	}
	return "", false
}

func (d swagger20SchemaDeclaration) rawOctets() bool {
	if d.typed && len(d.types) == 1 && d.types["file"] {
		return true
	}
	format, conflict := d.format()
	return !conflict && d.admitsStringAsSoleNonNullType() && format == "binary"
}

func (d swagger20SchemaDeclaration) artifactByteString() bool {
	format, conflict := d.format()
	return !conflict && d.admitsStringAsSoleNonNullType() && format == "byte"
}

func isSwagger20JSONMedia(base string) bool {
	if base == "application/json" {
		return true
	}
	_, subtype, ok := strings.Cut(base, "/")
	return ok && strings.HasSuffix(subtype, "+json")
}

func isSwagger20CharacterMedia(base string) bool {
	primary, subtype, ok := strings.Cut(base, "/")
	if !ok {
		return false
	}
	if primary == "text" {
		return true
	}
	return base == "application/xml" || strings.HasSuffix(subtype, "+xml")
}
