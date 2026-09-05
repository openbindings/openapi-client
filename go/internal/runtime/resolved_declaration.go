package openapiclient

import (
	"sort"

	"github.com/dlclark/regexp2"
	"github.com/getkin/kin-openapi/openapi3"
)

// SchemaDeclaration is a resolved, edition-aware view of the OpenAPI Schema
// Object facts needed to choose a deterministic wire carriage. It combines
// allOf constraints and collapses a choice only when exactly one non-null
// branch remains.
type SchemaDeclaration struct {
	declaration resolvedDeclaration
}

// ResolveSchemaDeclaration analyzes a Schema Object under the supplied
// OpenAPI edition. The supported edition surface remains OpenAPI 3.0 and 3.1.
func ResolveSchemaDeclaration(schema *openapi3.Schema, openAPIVersion string) SchemaDeclaration {
	return SchemaDeclaration{declaration: resolveDeclaration(schema, isOpenAPI30(majorMinor(openAPIVersion)))}
}

func (d SchemaDeclaration) Ambiguous() bool { return d.declaration.ambiguous }

// Types returns the proved type set in lexical order. An empty slice means no
// type was proved; use Typeless to distinguish an unconstrained declaration
// from an unsatisfiable or conflicting declaration.
func (d SchemaDeclaration) Types() []string {
	types := make([]string, 0, len(d.declaration.types))
	for member := range d.declaration.types {
		types = append(types, member)
	}
	sort.Strings(types)
	return types
}

func (d SchemaDeclaration) Conjuncts() []*openapi3.Schema {
	return append([]*openapi3.Schema(nil), d.declaration.conjuncts...)
}

func (d SchemaDeclaration) DeclaresOnly(allowed ...string) bool {
	return d.declaration.declaresOnly(allowed...)
}

func (d SchemaDeclaration) AdmitsStringAsSoleNonNullType() bool {
	return d.declaration.admitsStringAsSoleNonNullType()
}

func (d SchemaDeclaration) Typeless() bool { return d.declaration.typeless() }

// AdmitsNoInstance reports a resolved declaration no instance satisfies: a
// boolean `false` schema, or §5.2's empty `allOf` intersection.
func (d SchemaDeclaration) AdmitsNoInstance() bool { return d.declaration.admitsNoInstance() }
func (d SchemaDeclaration) AdmitsNull() bool       { return d.declaration.admitsNull() }
func (d SchemaDeclaration) SoleNonNullType() (string, bool) {
	return d.declaration.soleNonNullType()
}
func (d SchemaDeclaration) Format() (string, bool) { return d.declaration.format() }
func (d SchemaDeclaration) KeywordString(key string) (string, bool) {
	return d.declaration.keywordString(key)
}
func (d SchemaDeclaration) PropertyNames() []string { return d.declaration.propertyNames() }
func (d SchemaDeclaration) Property(name string) SchemaDeclaration {
	return SchemaDeclaration{declaration: d.declaration.property(name)}
}
func (d SchemaDeclaration) RequiresProperty(name string) bool {
	return d.declaration.requiresProperty(name)
}
func (d SchemaDeclaration) Items() SchemaDeclaration {
	return SchemaDeclaration{declaration: d.declaration.items()}
}

// resolvedDeclaration is the single declaration-only view used by the
// OpenAPI 3.0/3.1 binding siblings' lane, style, shape, and member-inspection
// rules. It deliberately is not a JSON Schema evaluator: it follows already
// resolved references, conjoins allOf, collapses only a choice with exactly
// one non-null branch, ignores not/conditionals, and keeps an absent type
// typeless.
type resolvedDeclaration struct {
	conjuncts []*openapi3.Schema
	types     map[string]bool
	ambiguous bool
	oas30     bool
}

func resolveDeclaration(schema *openapi3.Schema, oas30 bool) resolvedDeclaration {
	conjuncts, ambiguous := resolvedDeclarationConjuncts(schema, oas30, map[*openapi3.Schema]bool{})
	result := resolvedDeclaration{conjuncts: conjuncts, ambiguous: ambiguous, oas30: oas30}
	if ambiguous {
		return result
	}
	var constrained bool
	for _, conjunct := range conjuncts {
		candidate, present := declarationTypeSet(conjunct, oas30)
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
	if !constrained {
		result.types = nil
	}
	return result
}

func resolvedDeclarationConjuncts(schema *openapi3.Schema, oas30 bool, seen map[*openapi3.Schema]bool) ([]*openapi3.Schema, bool) {
	if schema == nil || seen[schema] {
		return nil, false
	}
	if literal, boolean := booleanSchemaLiteral(schema); boolean {
		if literal {
			return []*openapi3.Schema{{}}, false
		}
		// Preserve the false literal as an explicit unsatisfiable conjunct.
		// It has no lane and must not collapse into the same typeless view as
		// an omitted or assertion-free declaration.
		return []*openapi3.Schema{schema}, false
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
		members, ambiguous := resolvedDeclarationConjuncts(selected, oas30, seen)
		if ambiguous {
			return nil, true
		}
		conjuncts = append(conjuncts, members...)
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		members, ambiguous := resolvedDeclarationConjuncts(member.Value, oas30, seen)
		if ambiguous {
			return nil, true
		}
		conjuncts = append(conjuncts, members...)
	}
	return conjuncts, false
}

func declarationTypeSet(schema *openapi3.Schema, oas30 bool) (map[string]bool, bool) {
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

// declaresOnly implements §5.2's named predicate: the resolved type set is
// nonempty and every member belongs to allowed.
func (d resolvedDeclaration) declaresOnly(allowed ...string) bool {
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

// admitsStringAsSoleNonNullType implements §5.2's other named predicate.
func (d resolvedDeclaration) admitsStringAsSoleNonNullType() bool {
	if d.ambiguous || len(d.types) == 0 || !d.types["string"] {
		return false
	}
	for member := range d.types {
		if member != "string" && member != "null" {
			return false
		}
	}
	return true
}

func (d resolvedDeclaration) typeless() bool {
	if d.ambiguous || len(d.types) != 0 {
		return false
	}
	for _, conjunct := range d.conjuncts {
		if literal, boolean := booleanSchemaLiteral(conjunct); boolean && !literal {
			return false
		}
		if conjunct != nil && conjunct.Type != nil {
			// An explicit empty type set, or mutually contradictory conjoined
			// type sets, admits no instance. It is not an absent declaration.
			return false
		}
	}
	return true
}

func (d resolvedDeclaration) admitsNull() bool {
	return !d.ambiguous && d.types["null"]
}

func (d resolvedDeclaration) soleNonNullType() (string, bool) {
	if d.ambiguous || len(d.types) == 0 {
		return "", false
	}
	member := ""
	for candidate := range d.types {
		if candidate == "null" {
			continue
		}
		if member != "" {
			return "", false
		}
		member = candidate
	}
	return member, member != ""
}

// format returns the one declaration-level format contributed by the
// resolved conjuncts. Conflicting values have no single carriage meaning.
func (d resolvedDeclaration) format() (string, bool) {
	values := map[string]bool{}
	for _, conjunct := range d.conjuncts {
		if conjunct != nil && conjunct.Format != "" {
			values[conjunct.Format] = true
		}
	}
	if len(values) > 1 {
		return "", true
	}
	for value := range values {
		return value, false
	}
	return "", false
}

// keywordString returns the one resolved string annotation used by §9.
// OAS 3.0 callers deliberately do not consult contentEncoding or
// contentMediaType because those keywords are outside that line's dialect.
func (d resolvedDeclaration) keywordString(key string) (string, bool) {
	if d.oas30 && (key == "contentEncoding" || key == "contentMediaType") {
		return "", false
	}
	values := map[string]bool{}
	for _, conjunct := range d.conjuncts {
		if conjunct == nil {
			continue
		}
		var value string
		switch key {
		case "contentEncoding":
			value = conjunct.ContentEncoding
		case "contentMediaType":
			value = conjunct.ContentMediaType
		}
		if value == "" && conjunct.Extensions != nil {
			value, _ = conjunct.Extensions[key].(string)
		}
		if value != "" {
			values[value] = true
		}
	}
	if len(values) > 1 {
		return "", true
	}
	for value := range values {
		return value, false
	}
	return "", false
}

func (d resolvedDeclaration) propertyNames() []string {
	if d.ambiguous {
		return nil
	}
	present := map[string]bool{}
	var names []string
	for _, conjunct := range d.conjuncts {
		for name := range conjunct.Properties {
			if !present[name] {
				present[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}

func (d resolvedDeclaration) property(name string) resolvedDeclaration {
	if d.ambiguous {
		return resolvedDeclaration{ambiguous: true, oas30: d.oas30}
	}
	var matches []*openapi3.Schema
	for _, conjunct := range d.conjuncts {
		matched := false
		if ref := conjunct.Properties[name]; ref != nil && ref.Value != nil {
			matches = append(matches, ref.Value)
			matched = true
		}
		if !d.oas30 {
			patterns := make([]string, 0, len(conjunct.PatternProperties))
			for pattern := range conjunct.PatternProperties {
				patterns = append(patterns, pattern)
			}
			sort.Strings(patterns)
			for _, pattern := range patterns {
				re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
				if err != nil {
					continue
				}
				ok, err := re.MatchString(name)
				if err != nil || !ok {
					continue
				}
				matched = true
				if ref := conjunct.PatternProperties[pattern]; ref != nil && ref.Value != nil {
					matches = append(matches, ref.Value)
				}
			}
		}
		if matched {
			continue
		}
		switch {
		case conjunct.AdditionalProperties.Schema != nil && conjunct.AdditionalProperties.Schema.Value != nil:
			if literal, boolean := booleanSchemaLiteral(conjunct.AdditionalProperties.Schema.Value); !boolean || literal {
				matches = append(matches, conjunct.AdditionalProperties.Schema.Value)
			}
		case conjunct.AdditionalProperties.Has == nil || *conjunct.AdditionalProperties.Has:
			matches = append(matches, &openapi3.Schema{})
		}
	}
	return resolveDeclaration(allOfSchema(matches), d.oas30)
}

func (d resolvedDeclaration) requiresProperty(name string) bool {
	if d.ambiguous {
		return false
	}
	for _, conjunct := range d.conjuncts {
		for _, required := range conjunct.Required {
			if required == name {
				return true
			}
		}
	}
	return false
}

func (d resolvedDeclaration) items() resolvedDeclaration {
	if d.ambiguous {
		return resolvedDeclaration{ambiguous: true, oas30: d.oas30}
	}
	var matches []*openapi3.Schema
	for _, conjunct := range d.conjuncts {
		if conjunct.Items != nil && conjunct.Items.Value != nil {
			matches = append(matches, conjunct.Items.Value)
		}
	}
	return resolveDeclaration(allOfSchema(matches), d.oas30)
}

func (d resolvedDeclaration) admitsNoInstance() bool {
	if d.ambiguous {
		return false
	}
	for _, conjunct := range d.conjuncts {
		if literal, boolean := booleanSchemaLiteral(conjunct); boolean && !literal {
			return true
		}
	}
	// A nil map means no conjunct contributed a type set (an OAS 3.0
	// array-valued `type` is malformed on that line and is refused by its own
	// rule, not here); an empty non-nil map is §5.2's empty intersection.
	return d.types != nil && len(d.types) == 0
}
