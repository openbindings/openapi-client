package openapiclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// This file implements the engine's flattened OpenAPI input model.
// openbindings.openapi-3.0@1 §§7–8 and openbindings.openapi-3.1@1 §§7–8:
// the caller-facing input value is one JSON
// object — parameters from every location and the request body merged into
// one object — and parameter serialization follows the OAS
// style/explode/allowReserved rules, incorporated wholesale.

// ---------------------------------------------------------------------------
// Effective parameter set
// ---------------------------------------------------------------------------

// effectiveParameters merges path-item and operation `parameters` (operation
// winning on same name-and-location collision, per the OAS) and drops header
// parameters named Accept, Content-Type, or Authorization: the OAS declares
// such parameter definitions SHALL be ignored.
func effectiveParameters(pathItem *openapi3.PathItem, op *openapi3.Operation) openapi3.Parameters {
	merged := mergeParameters(pathItem.Parameters, op.Parameters)
	out := make(openapi3.Parameters, 0, len(merged))
	for _, pref := range merged {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		if p.In == openapi3.ParameterInHeader {
			switch http.CanonicalHeaderKey(p.Name) {
			case "Accept", "Content-Type", "Authorization":
				continue // ignored per the OAS's parameter rules
			}
		}
		out = append(out, pref)
	}
	return out
}

// checkEffectiveParameterOwnership enforces the declaration-only portions of
// openbindings.openapi-3.0@1 §8.3 and openbindings.openapi-3.1@1 §8.3. Host
// and Content-Length are owned by the HTTP processor and
// therefore cannot be caller-routed parameters. Raw-Cookie and
// structured-cookie declarations are permitted together; their collision is
// decided from emitted invocation contributions.
func checkEffectiveParameterOwnership(params openapi3.Parameters) error {
	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		if p.In == openapi3.ParameterInHeader {
			switch http.CanonicalHeaderKey(p.Name) {
			case "Host", "Content-Length":
				return fmt.Errorf("operation declares processor-owned header parameter %q (OAPI-P-10: unresolvable)", p.Name)
			}
		}
	}
	return nil
}

// checkPathTemplateAddressability enforces openbindings.openapi-3.0@1 §§8.2,
// 10 / openbindings.openapi-3.1@1 §§8.2, 10: the target URL is
// the resolved server joined with the operation's path template, so a template
// variable that no declared path parameter can supply leaves no target to
// address and refuses before dispatch — the same ground §9.1 states for the
// neighbouring case of a declared path parameter the caller omitted ("the URL
// cannot be built"). Every accepted OAS edition requires a path template
// variable to have a corresponding `in: path` Parameter Object with
// `required: true`, so this reaches only artifacts that violate that
// requirement. Emitting the unsubstituted template instead would percent-encode
// the braces and put a meaningless segment on a live service.
//
// The predicate is the exact inverse of the substitution routeParameter
// performs (ReplaceAll of "{" + name + "}"): an expression is addressable iff a
// declared path parameter's name equals the enclosed text. It is declaration-
// only — independent of any invocation input — and so is checked before input
// consumption.
func checkPathTemplateAddressability(pathTemplate string, params openapi3.Parameters) error {
	declared := map[string]bool{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		if pref.Value.In == openapi3.ParameterInPath {
			declared[pref.Value.Name] = true
		}
	}
	var unaddressable []string
	seen := map[string]bool{}
	for _, name := range pathTemplateVariables(pathTemplate) {
		if declared[name] || seen[name] {
			continue
		}
		seen[name] = true
		unaddressable = append(unaddressable, name)
	}
	if len(unaddressable) == 0 {
		return nil
	}
	sort.Strings(unaddressable)
	return fmt.Errorf("path template variable(s) %s have no declared path parameter: the target URL cannot be built (unresolvable target)", strings.Join(unaddressable, ", "))
}

// normalizedTemplatedPathHierarchy erases template NAMES while preserving the
// literal hierarchy, so two Paths keys that differ only in what they call their
// expressions normalize to the same string. It also reports whether the key was
// templated at all: the OAS prohibition is on two TEMPLATED keys, and two
// identical literal keys cannot co-exist in one Paths Object anyway.
func normalizedTemplatedPathHierarchy(path string) (string, bool) {
	var result strings.Builder
	templated := false
	for index := 0; index < len(path); {
		if path[index] != '{' {
			result.WriteByte(path[index])
			index++
			continue
		}
		close := strings.IndexByte(path[index+1:], '}')
		if close < 0 {
			result.WriteByte(path[index])
			index++
			continue
		}
		templated = true
		result.WriteString("{}")
		index += close + 2
	}
	return result.String(), templated
}

// equivalentTemplatedPathKey returns another Paths key whose templated
// hierarchy is equivalent to the selected key, or "" when the selected key
// participates in no such pair. OAS forbids the pair outright
// (openbindings.openapi-3.0@1 §8.2, openbindings.openapi-3.1@1 §8.2,
// openbindings.openapi-3.2@1 §8.2), and supplies no unique target mapping for
// it, so every selected operation on a participating Path Item is excluded.
// The result is deterministic under an unordered candidate set: the smallest
// participating key other than the selected one.
func equivalentTemplatedPathKey(selected string, candidates []string) string {
	want, templated := normalizedTemplatedPathHierarchy(selected)
	if !templated {
		return ""
	}
	found := ""
	for _, candidate := range candidates {
		if candidate == selected {
			continue
		}
		normalized, candidateTemplated := normalizedTemplatedPathHierarchy(candidate)
		if !candidateTemplated || normalized != want {
			continue
		}
		if found == "" || candidate < found {
			found = candidate
		}
	}
	return found
}

// equivalentTemplatedPathKey answers the same question for a typed 3.0/3.1
// artifact, whose Paths keys are the ones the loader parsed.
func (a *Artifact) equivalentTemplatedPathKey(selected string) string {
	if a == nil || a.Document == nil || a.Document.Paths == nil {
		return ""
	}
	keys := make([]string, 0, a.Document.Paths.Len())
	for key := range a.Document.Paths.Map() {
		keys = append(keys, key)
	}
	return equivalentTemplatedPathKey(selected, keys)
}

// pathTemplateVariables returns the names of the brace-delimited expressions in
// a path template, in order. An unclosed "{" delimits nothing and is an
// ordinary literal; an inner "{" restarts the expression, matching the
// innermost pair the substitution would otherwise have to match.
func pathTemplateVariables(pathTemplate string) []string {
	var names []string
	open := -1
	for index, char := range pathTemplate {
		switch char {
		case '{':
			open = index
		case '}':
			if open >= 0 {
				names = append(names, pathTemplate[open+1:index])
				open = -1
			}
		}
	}
	return names
}

// unflattenableParam reports the first parameter name declared in two
// DIFFERENT locations (legal per the OAS's name-plus-location identity, but
// unrepresentable by the flattened model): such an operation is refused
// loudly at binding resolution (openbindings.openapi-3.0@1 §7;
// openbindings.openapi-3.1@1 §7). Empty string means flattenable.
func unflattenableParam(params openapi3.Parameters) string {
	locs := map[string]string{}
	headerNames := map[string]string{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		if prev, ok := locs[p.Name]; ok && prev != p.In {
			return p.Name
		}
		locs[p.Name] = p.In
		if p.In == openapi3.ParameterInHeader {
			folded := strings.ToLower(p.Name)
			if previous, ok := headerNames[folded]; ok && previous != p.Name {
				return p.Name
			}
			headerNames[folded] = p.Name
		}
	}
	return ""
}

// unflattenableParamForRevision keeps revision 1's complete flattened-model
// refusal while letting revision 2 disambiguate names across protocol
// locations. Case-distinct header declarations remain unresolvable in both
// revisions because HTTP field names themselves are case-insensitive: no
// routing envelope can create two semantically distinct wire destinations.
func unflattenableParamForRevision(params openapi3.Parameters, bindingSpec string) string {
	if !usesRoutedInput(bindingSpec) {
		return unflattenableParam(params)
	}
	headerNames := map[string]string{}
	for _, pref := range params {
		if pref == nil || pref.Value == nil || pref.Value.In != openapi3.ParameterInHeader {
			continue
		}
		name := pref.Value.Name
		folded := strings.ToLower(name)
		if previous, ok := headerNames[folded]; ok && previous != name {
			return name
		}
		headerNames[folded] = name
	}
	return ""
}

// ---------------------------------------------------------------------------
// Routing (the flatten, wire side)
// ---------------------------------------------------------------------------

// routedInput is the wire-side product of routing one flattened input object
// through the operation's declared surface.
type routedInput struct {
	resolvedPath string   // path template with path parameters substituted
	queryUnits   []string // fully percent-encoded name=value units, declaration order
	headers      [][2]string
	cookieUnits  []string // raw name=value units, declaration order

	bodyFields map[string]any // object-mode body fields
	bodyValue  any            // synthetic-mode body value (§9.1: the `body` property, unwrapped at the wire)
	bodySet    bool

	// populated records which declared parameters the caller populated, per
	// channel ("header" names canonicalized), for openbindings.openapi-3.0@1
	// §11 / openbindings.openapi-3.1@1 §11
	// credential-collision refusal.
	populated map[string]map[string]bool
}

// routeInput maps one flattened input object onto the wire per
// openbindings.openapi-3.0@1 §§7–8 / openbindings.openapi-3.1@1 §§7–8:
//
//   - declared parameters ride their location, serialized per the OAS
//     style/explode/allowReserved rules (§8.2 in both family documents);
//   - parameter/body-property collisions are rejected before this function:
//     independently declared upstream values are never collapsed or
//     duplicated;
//   - a field matching no declared parameter or body property passes through
//     into the body when a request body is declared, and is refused loudly
//     before dispatch when none is declared;
//   - a missing declared path parameter always refuses before dispatch (the
//     URL cannot be built); every other missing member is the server's
//     declared validation's business.
func routeInput(params openapi3.Parameters, input map[string]any, pathTemplate string, plan *bodyPlan) (*routedInput, error) {
	return routeInputFor(params, input, pathTemplate, plan, profileRoutedCoordinate)
}

func routeInputFor(params openapi3.Parameters, input map[string]any, pathTemplate string, plan *bodyPlan, bindingSpec string) (*routedInput, error) {
	return routeInputWithParameterOptions(params, input, pathTemplate, plan, bindingSpec, parameterSerializationOptions{})
}

func routeInputWithParameterOptions(params openapi3.Parameters, input map[string]any, pathTemplate string, plan *bodyPlan, bindingSpec string, options parameterSerializationOptions) (*routedInput, error) {
	r := &routedInput{
		resolvedPath: pathTemplate,
		bodyFields:   map[string]any{},
		populated: map[string]map[string]bool{
			"header": {}, "query": {}, ParameterInQueryString: {}, "cookie": {},
		},
	}

	consumed := map[string]bool{}
	var missingPath, missingRequired []string

	for _, pref := range params {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		value, ok := input[p.Name]
		if !ok {
			if p.In == openapi3.ParameterInPath {
				missingPath = append(missingPath, p.Name)
			} else if p.Required {
				// R2 (2026-09-01): `required` means mandatory in every accepted
				// edition and every document of the family incorporates the refusal;
				// minimality-audit M9 briefly gated this to 3.2 on implementation
				// grounds and the gate was reversed as drift.
				missingRequired = append(missingRequired, p.In+"/"+p.Name)
			}
			continue
		}
		consumed[p.Name] = true

		if err := routeParameterWithOptions(r, p, value, bindingSpec, options); err != nil {
			return nil, err
		}

	}

	if len(missingPath) > 0 {
		sort.Strings(missingPath)
		return nil, fmt.Errorf("%w(s) %s: the URL cannot be built without them", errMissingPathParam, strings.Join(missingPath, ", "))
	}
	if len(missingRequired) > 0 {
		sort.Strings(missingRequired)
		return nil, fmt.Errorf("missing required parameter(s) %s", strings.Join(missingRequired, ", "))
	}

	// Fields matching no declared parameter.
	var unmatched []string
	names := make([]string, 0, len(input))
	for name := range input {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if consumed[name] {
			continue
		}
		value := input[name]
		switch {
		case plan == nil || !plan.declared:
			unmatched = append(unmatched, name)
		case plan.synthetic || plan.wholeObject:
			if name == syntheticBodyProperty {
				r.bodyValue, r.bodySet = value, true
			} else {
				// The flattened contract of a non-object body carries only
				// parameters and the synthetic `body` property; there is no
				// object body to pass through into.
				unmatched = append(unmatched, name)
			}
		case plan.family == familyJSON || plan.props[name]:
			// Evaluation-free body passthrough: no schema evaluation
			// participates in JSON routing, while form and multipart members
			// require an artifact declaration that defines their wire carriage.
			value, prepareErr := prepareEncodingPropertyValue(plan, name, value, options.converter)
			if prepareErr != nil {
				return nil, prepareErr
			}
			if contentPropertyNullIsElided(plan, name, value) {
				continue
			}
			r.bodyFields[name] = value
		default:
			unmatched = append(unmatched, name)
		}
	}
	if len(unmatched) > 0 {
		if plan != nil && plan.declared && (plan.synthetic || plan.wholeObject) {
			return nil, fmt.Errorf("field(s) %s match no declared parameter, and the declared request body uses whole-value carriage (its flattened contract carries only the synthetic %q property)", strings.Join(unmatched, ", "), syntheticBodyProperty)
		}
		if plan != nil && plan.declared {
			return nil, fmt.Errorf("field(s) %s have no declaration-defined carriage for the %s request body", strings.Join(unmatched, ", "), plan.mediaType)
		}
		return nil, fmt.Errorf("field(s) %s match no declared parameter, and the operation declares no request body to pass them through to", strings.Join(unmatched, ", "))
	}

	return r, nil
}

// syntheticBodyProperty is the flattened contract's property for a
// non-object request body (§9.1): at the wire, its value IS the request
// body, unwrapped.
const syntheticBodyProperty = "body"

// errMissingPathParam marks the §9.1 always-refuses case — an input, absent
// or supplied, leaving a declared path parameter unfilled — so callers can
// stop candidate iteration: no other candidate can build the URL either.
// The refusal itself rides ERR_REFUSED like every pre-dispatch refusal.
var errMissingPathParam = errors.New("missing path parameter")

// routeParameter serializes one populated parameter onto its wire location.
func routeParameter(r *routedInput, p *openapi3.Parameter, value any) error {
	return routeParameterFor(r, p, value, profileRoutedCoordinate)
}

func routeParameterFor(r *routedInput, p *openapi3.Parameter, value any, bindingSpec string) error {
	return routeParameterWithOptions(r, p, value, bindingSpec, parameterSerializationOptions{})
}

func routeParameterWithOptions(r *routedInput, p *openapi3.Parameter, value any, bindingSpec string, options parameterSerializationOptions) error {
	if hasMediaFidelity(bindingSpec) {
		if err := validateRevision3ParameterSerializationForEdition(p, strings.HasPrefix(options.edition, "3.0."), options.edition); err != nil {
			return fmt.Errorf("parameter %q: %w", p.Name, err)
		}
		prepared, _, err := prepareParameterValueForEdition(p, value, options.converter, options.edition)
		if err != nil {
			return fmt.Errorf("parameter %q: %w", p.Name, err)
		}
		value = prepared
	}
	// A `content`-form parameter (schema-less, a single-entry content map)
	// serializes its value per its declared media type and rides its location as
	// that serialized string (openbindings.openapi-3.0@1 §8.3;
	// openbindings.openapi-3.1@1 §8.3).
	if len(p.Content) > 0 {
		if p.In == ParameterInQueryString {
			serialized, serializeErr := serializeQueryStringParameter(options.document, p, value, bindingSpec)
			if serializeErr != nil {
				return serializeErr
			}
			r.queryUnits = append(r.queryUnits, serialized)
			r.populated[ParameterInQueryString][p.Name] = true
			return nil
		}
		serialized, err := serializeParamContentFor(p, value, bindingSpec)
		if err != nil {
			return err
		}
		switch p.In {
		case openapi3.ParameterInPath:
			escaped := encodePathValue(serialized)
			if hasMediaFidelity(bindingSpec) {
				escaped = revision3URIEscape(serialized, false, false)
			}
			r.resolvedPath = strings.ReplaceAll(r.resolvedPath, "{"+p.Name+"}", escaped)
		case openapi3.ParameterInQuery:
			// This is the CONTENT-form lane, whose byte rule is its own pin and
			// is unconditional: "Percent-encoding a content-form parameter
			// leaves RFC 3986 unreserved bytes literal and encodes every other
			// UTF-8 byte as uppercase `%HH`" (§8.3 in all three 3.x documents).
			// `allowReserved` is a `schema`-path control -- Appendix C's
			// reserved expansion and the C.3 manual-construction rule govern
			// "a query mixing regular form EXPANSION" -- and it does not reach
			// this lane. Honoring it here leaked the whole reserved set:
			// a supplied ":/?@$,;" rode literally on every edition.
			if hasMediaFidelity(bindingSpec) {
				r.queryUnits = append(r.queryUnits, revision3URIEscape(p.Name, false, true)+"="+revision3URIEscape(serialized, false, true))
			} else {
				r.queryUnits = append(r.queryUnits, queryEscape(p.Name, false)+"="+queryEscape(serialized, p.AllowReserved))
			}
			r.populated["query"][p.Name] = true
		case openapi3.ParameterInHeader:
			r.headers = append(r.headers, [2]string{p.Name, serialized})
			r.populated["header"][http.CanonicalHeaderKey(p.Name)] = true
		case openapi3.ParameterInCookie:
			unit := p.Name + "=" + serialized
			if strings.HasPrefix(options.edition, "3.2.") {
				if err := validateOpenAPI32CookieUnits([]string{unit}); err != nil {
					return fmt.Errorf("cookie parameter %q: %w", p.Name, err)
				}
			}
			r.cookieUnits = append(r.cookieUnits, unit)
			r.populated["cookie"][p.Name] = true
		}
		return nil
	}

	sm, err := p.SerializationMethod() // compatibility defaults
	if hasMediaFidelity(bindingSpec) {
		sm, err = revision3ParameterSerializationMethodForEdition(p, options.edition)
	}
	if err != nil {
		return fmt.Errorf("parameter %q: %w", p.Name, err)
	}

	switch p.In {
	case openapi3.ParameterInPath:
		expanded, err := serializePathValueForRevision(p.Name, value, sm.Style, sm.Explode, bindingSpec)
		if err != nil {
			return fmt.Errorf("path parameter %q: %w", p.Name, err)
		}
		r.resolvedPath = strings.ReplaceAll(r.resolvedPath, "{"+p.Name+"}", expanded)
	case openapi3.ParameterInQuery:
		// Appendix C.4.2's pre-encoding set, stated identically by all three
		// 3.x documents. See the note on the other call site.
		const querySafe = true
		units, err := serializeQueryValueForRevision(p.Name, value, sm.Style, sm.Explode, p.AllowReserved, bindingSpec, querySafe, querySafe)
		if err != nil {
			return fmt.Errorf("query parameter %q: %w", p.Name, err)
		}
		r.queryUnits = append(r.queryUnits, units...)
		r.populated["query"][p.Name] = true
	case openapi3.ParameterInHeader:
		v, err := serializeHeaderValue(value, sm.Style, sm.Explode)
		if err != nil {
			return fmt.Errorf("header parameter %q: %w", p.Name, err)
		}
		r.headers = append(r.headers, [2]string{p.Name, v})
		r.populated["header"][http.CanonicalHeaderKey(p.Name)] = true
	case openapi3.ParameterInCookie:
		units, err := serializeCookieValue(p.Name, value, sm.Style, sm.Explode)
		if err != nil {
			return fmt.Errorf("cookie parameter %q: %w", p.Name, err)
		}
		// The RFC 6265 cookie-value check belongs to `style: cookie`, whose
		// contract is that values "MUST arrive already escaped" -- so it is
		// the caller's value that has to be a cookie-value. A form-style
		// contribution is percent-encoded above and is always one by
		// construction, so applying the check to every 3.2 cookie parameter
		// refused values the edition serializes perfectly well.
		if sm.Style == "cookie" {
			if err := validateOpenAPI32CookieUnits(units); err != nil {
				return fmt.Errorf("cookie parameter %q: %w", p.Name, err)
			}
		}
		r.cookieUnits = append(r.cookieUnits, units...)
		r.populated["cookie"][p.Name] = true
	default:
		return fmt.Errorf("parameter %q: unsupported location %q", p.Name, p.In)
	}
	return nil
}

func validateRevision3ParameterSerialization(p *openapi3.Parameter, is30 bool) error {
	return validateRevision3ParameterSerializationForEdition(p, is30, "")
}

func validateRevision3ParameterSerializationForEdition(p *openapi3.Parameter, is30 bool, edition string) error {
	if p == nil || len(p.Content) > 0 {
		return nil
	}
	method, err := revision3ParameterSerializationMethodForEdition(p, edition)
	if err != nil {
		return err
	}
	var schema *openapi3.Schema
	if p.Schema != nil {
		schema = p.Schema.Value
	}
	resolved := resolveDeclaration(schema, is30)
	switch p.In {
	case openapi3.ParameterInPath:
		if method.Style != openapi3.SerializationSimple && method.Style != openapi3.SerializationLabel && method.Style != openapi3.SerializationMatrix {
			return fmt.Errorf("style %q is not defined for path parameters", method.Style)
		}
	case openapi3.ParameterInHeader:
		if method.Style != openapi3.SerializationSimple {
			return fmt.Errorf("style %q is not defined for header parameters", method.Style)
		}
	case openapi3.ParameterInCookie:
		if method.Style != openapi3.SerializationForm && !(strings.HasPrefix(edition, "3.2.") && method.Style == "cookie") {
			return fmt.Errorf("style %q is not defined for cookie parameters", method.Style)
		}
	case openapi3.ParameterInQuery:
		switch method.Style {
		case openapi3.SerializationForm:
			return nil
		case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
			if method.Explode {
				return fmt.Errorf("query style %q has no explode=true cell", method.Style)
			}
			if resolved.declaresOnly("null", "boolean", "number", "integer", "string") {
				return fmt.Errorf("query style %q is defined only for arrays or objects", method.Style)
			}
		case openapi3.SerializationDeepObject:
			if !method.Explode {
				return fmt.Errorf("query style deepObject has no explode=false cell")
			}
			if resolved.declaresOnly("null", "boolean", "number", "integer", "string", "array") {
				return fmt.Errorf("query style deepObject is defined only for objects")
			}
		default:
			return fmt.Errorf("style %q is not defined for query parameters", method.Style)
		}
	}
	return nil
}

func revision3ParameterSerializationMethod(p *openapi3.Parameter) (*openapi3.SerializationMethod, error) {
	return revision3ParameterSerializationMethodForEdition(p, "")
}

func revision3ParameterSerializationMethodForEdition(p *openapi3.Parameter, edition string) (*openapi3.SerializationMethod, error) {
	if p == nil {
		return nil, fmt.Errorf("nil parameter")
	}
	style := p.Style
	switch p.In {
	case openapi3.ParameterInPath, openapi3.ParameterInHeader:
		if style == "" {
			style = openapi3.SerializationSimple
		}
	case openapi3.ParameterInQuery, openapi3.ParameterInCookie:
		if style == "" {
			style = openapi3.SerializationForm
		}
	default:
		return nil, fmt.Errorf("unexpected parameter 'in': %q", p.In)
	}
	explode := style == openapi3.SerializationForm || (strings.HasPrefix(edition, "3.2.") && style == "cookie")
	if p.Explode != nil {
		explode = *p.Explode
	}
	if strings.HasPrefix(edition, "3.2.") && style == openapi3.SerializationDeepObject {
		explode = true // the 3.2 field explicitly has no effect for deepObject
	}
	return &openapi3.SerializationMethod{Style: style, Explode: explode}, nil
}

// serializeParamContent serializes a content-form parameter's value per its
// declared media type: JSON family values JSON-serialize; text/plain carries
// a string value verbatim. Any other declared media type has no defined
// parameter carriage in revision 1 and refuses loudly.
func serializeParamContent(p *openapi3.Parameter, value any) (string, error) {
	return serializeParamContentFor(p, value, profileRoutedCoordinate)
}

func serializeParamContentFor(p *openapi3.Parameter, value any, bindingSpec string) (string, error) {
	if len(p.Content) != 1 {
		return "", fmt.Errorf("parameter %q content must contain exactly one media type", p.Name)
	}
	var mediaKey string
	for k := range p.Content {
		mediaKey = k
		break // the OAS requires exactly one entry
	}
	mt := normalizeMediaType(mediaKey)
	var parsed parsedMediaType
	if hasMediaFidelity(bindingSpec) {
		var err error
		parsed, err = parseRevision3MediaType(mediaKey)
		if err != nil {
			return "", fmt.Errorf("parameter %q declares invalid content %q: %w", p.Name, mediaKey, err)
		}
		mt = parsed.base
	}
	switch {
	case isJSONMediaType(mt):
		b, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("parameter %q: serialize as %s: %w", p.Name, mt, err)
		}
		return string(b), nil
	case mt == "text/plain":
		s, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("parameter %q declares content %q: the value must be a string, got %T", p.Name, mediaKey, value)
		}
		if hasMediaFidelity(bindingSpec) {
			encoded, err := encodeTextString(s, parsed)
			if err != nil {
				return "", fmt.Errorf("parameter %q declares content %q: %w", p.Name, mediaKey, err)
			}
			return string(encoded), nil
		}
		return s, nil
	default:
		return "", fmt.Errorf("parameter %q declares content %q: no parameter carriage is defined for that media type", p.Name, mediaKey)
	}
}

// ---------------------------------------------------------------------------
// Style/explode expansions (openbindings.openapi-3.0@1 §8.2;
// openbindings.openapi-3.1@1 §8.2).
// ---------------------------------------------------------------------------

// serializePathValue expands one path parameter per the OAS style table.
// Value pieces are percent-encoded with the encodeURIComponent byte set
// (cross-SDK URL parity); the style's structural characters (";", "=", ".",
// ",") stay literal.
func serializePathValue(name string, value any, style string, explode bool) (string, error) {
	return serializePathValueForRevision(name, value, style, explode, profileRoutedCoordinate)
}

func serializePathValueForRevision(name string, value any, style string, explode bool, bindingSpec string) (string, error) {
	esc := encodePathValue
	if hasMediaFidelity(bindingSpec) {
		esc = func(value string) string { return revision3URIEscape(value, false, false) }
	}
	switch style {
	case openapi3.SerializationSimple:
		return expandSimple(value, explode, esc)
	case openapi3.SerializationLabel:
		return expandLabel(value, explode, esc)
	case openapi3.SerializationMatrix:
		return expandMatrix(name, value, explode, esc)
	default:
		return "", fmt.Errorf("style %q is not defined for path parameters", style)
	}
}

// serializeHeaderValue expands one header parameter (simple style only).
// Header values are not percent-encoded: they are not URL components.
func serializeHeaderValue(value any, style string, explode bool) (string, error) {
	if style != openapi3.SerializationSimple {
		return "", fmt.Errorf("style %q is not defined for header parameters", style)
	}
	return expandSimple(value, explode, func(s string) string { return s })
}

// serializeQueryValue expands one query parameter into fully percent-encoded
// name=value units, per the OAS query styles. allowReserved lets RFC 3986
// reserved characters in VALUES pass unescaped.
func serializeQueryValue(name string, value any, style string, explode bool, allowReserved bool) ([]string, error) {
	return serializeQueryValueForRevision(name, value, style, explode, allowReserved, profileRoutedCoordinate, false, false)
}

func serializeQueryValueForRevision(name string, value any, style string, explode bool, allowReserved bool, bindingSpec string, formSafe, encodeStyleDelimiters bool) ([]string, error) {
	n := queryEscape(name, false)
	esc := func(s string) string { return queryEscape(s, allowReserved) }
	keyEsc := func(s string) string { return queryEscape(s, false) }
	if hasMediaFidelity(bindingSpec) {
		n = revision3URIEscape(name, false, formSafe)
		esc = func(value string) string { return revision3URIEscape(value, allowReserved, formSafe) }
		keyEsc = func(value string) string { return revision3URIEscape(value, false, formSafe) }
	}
	switch style {
	case openapi3.SerializationForm:
		return expandFormPairs(n, value, explode, esc)
	case openapi3.SerializationSpaceDelimited:
		return expandDelimited(n, value, explode, "%20", esc)
	case openapi3.SerializationPipeDelimited:
		delimiter := "|"
		if encodeStyleDelimiters {
			delimiter = "%7C"
		}
		return expandDelimited(n, value, explode, delimiter, esc)
	case openapi3.SerializationDeepObject:
		obj, ok := asObject(value)
		if !ok {
			return nil, fmt.Errorf("style deepObject is defined for objects only, got %T", value)
		}
		pairs, err := objectPairs(obj)
		if err != nil {
			return nil, err
		}
		units := make([]string, 0, len(pairs))
		leftBracket, rightBracket := "[", "]"
		if encodeStyleDelimiters {
			leftBracket, rightBracket = "%5B", "%5D"
		}
		for _, kv := range pairs {
			units = append(units, n+leftBracket+keyEsc(kv[0])+rightBracket+"="+esc(kv[1]))
		}
		return units, nil
	default:
		return nil, fmt.Errorf("style %q is not defined for query parameters", style)
	}
}

func revision3URIEscape(value string, allowReserved, formSafe bool) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		unreserved := char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~", rune(char))
		reserved := strings.ContainsRune(":/?#[]@!$&'()*+,;=", rune(char))
		if formSafe && strings.ContainsRune("&+=#[]", rune(char)) {
			reserved = false
		}
		if unreserved || allowReserved && reserved {
			out.WriteByte(char)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hex[char>>4])
		out.WriteByte(hex[char&0x0f])
	}
	return out.String()
}

// serializeCookieValue expands one cookie parameter into name=value units,
// which channel assembly (openbindings.openapi-3.0@1 §11;
// openbindings.openapi-3.1@1 §11) joins into the single Cookie header with
// "; ". Exploded array/object expansions use the cookie header's own pair
// separator rather than form's "&", which has no meaning inside a Cookie
// header.
//
// The `form` style PERCENT-ENCODES, on every accepted edition. This code
// previously did not, on the stated ground that "the OAS defines no cookie
// escaping" -- which is the sentence openbindings.openapi-3.1@1 §8.2 was
// written to refute: "No accepted edition extends that exemption to cookies,
// and this specification does not invent one: a declared cookie parameter
// serialized on the `schema` path is percent-encoded by ordinary RFC 6570
// expansion, because `allowReserved` is valid only for query parameters and no
// cookie-specific exemption exists." It goes on to dispose of the Appendix D
// remark this code was reading as a rule: that remark is "advice to artifact
// authors rather than a serialization rule". Leaving values unencoded put a
// raw ";" from a supplied value straight into the Cookie header, where it
// splits one contribution into several.
//
// OAS 3.2's `style: cookie` is the one exemption, and it is that style's, not
// the destination's: it "follows RFC 6265 Cookie syntax: contributions
// preserve exact names and values, use `; ` between pairs, and apply no
// percent-encoding or other escaping; values needing escaping MUST arrive
// already escaped" (openbindings.openapi-3.2@1 §8.2). That style exists only
// on the 3.2 line, so no other edition reaches the identity escaper.
func serializeCookieValue(name string, value any, style string, explode bool) ([]string, error) {
	if style != openapi3.SerializationForm && style != "cookie" {
		return nil, fmt.Errorf("style %q is not defined for cookie parameters", style)
	}
	escape := func(s string) string { return queryEscape(s, false) }
	if style == "cookie" {
		escape = func(s string) string { return s }
	}
	return expandFormPairs(name, value, explode, escape)
}

// expandSimple implements the OAS "simple" rows:
//
//	primitive        → v
//	array (any expl) → a,b,c
//	object false     → k1,v1,k2,v2
//	object true      → k1=v1,k2=v2
func expandSimple(value any, explode bool, esc func(string) string) (string, error) {
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return "", err
		}
		return joinEscaped(parts, ",", esc), nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return "", err
		}
		if explode {
			return joinPairs(pairs, "=", ",", esc), nil
		}
		return joinEscaped(flattenPairs(pairs), ",", esc), nil
	}
	s, err := primitiveString(value)
	if err != nil {
		return "", err
	}
	return esc(s), nil
}

// expandLabel implements the OAS "label" rows (the "." prefix, "."
// separators when exploded).
func expandLabel(value any, explode bool, esc func(string) string) (string, error) {
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return "", err
		}
		sep := ","
		if explode {
			sep = "."
		}
		return "." + joinEscaped(parts, sep, esc), nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return "", err
		}
		if explode {
			return "." + joinPairs(pairs, "=", ".", esc), nil
		}
		return "." + joinEscaped(flattenPairs(pairs), ",", esc), nil
	}
	s, err := primitiveString(value)
	if err != nil {
		return "", err
	}
	return "." + esc(s), nil
}

// expandMatrix implements the OAS "matrix" rows (";name=" prefixes; an empty
// primitive renders ";name").
func expandMatrix(name string, value any, explode bool, esc func(string) string) (string, error) {
	n := esc(name)
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return "", err
		}
		if explode {
			var b strings.Builder
			for _, p := range parts {
				b.WriteString(";" + n + "=" + esc(p))
			}
			return b.String(), nil
		}
		return ";" + n + "=" + joinEscaped(parts, ",", esc), nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return "", err
		}
		if explode {
			var b strings.Builder
			for _, kv := range pairs {
				b.WriteString(";" + esc(kv[0]) + "=" + esc(kv[1]))
			}
			return b.String(), nil
		}
		return ";" + n + "=" + joinEscaped(flattenPairs(pairs), ",", esc), nil
	}
	s, err := primitiveString(value)
	if err != nil {
		return "", err
	}
	if s == "" {
		return ";" + n, nil
	}
	return ";" + n + "=" + esc(s), nil
}

// expandFormPairs implements the OAS "form" rows as name=value units:
//
//	primitive        → [name=v]
//	array false      → [name=a,b,c]
//	array true       → [name=a name=b name=c]
//	object false     → [name=k1,v1,k2,v2]
//	object true      → [k1=v1 k2=v2]
func expandFormPairs(name string, value any, explode bool, esc func(string) string) ([]string, error) {
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return nil, err
		}
		if explode {
			units := make([]string, 0, len(parts))
			for _, p := range parts {
				units = append(units, name+"="+esc(p))
			}
			return units, nil
		}
		return []string{name + "=" + joinEscaped(parts, ",", esc)}, nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return nil, err
		}
		if explode {
			units := make([]string, 0, len(pairs))
			for _, kv := range pairs {
				units = append(units, esc(kv[0])+"="+esc(kv[1]))
			}
			return units, nil
		}
		return []string{name + "=" + joinEscaped(flattenPairs(pairs), ",", esc)}, nil
	}
	s, err := primitiveString(value)
	if err != nil {
		return nil, err
	}
	return []string{name + "=" + esc(s)}, nil
}

// expandDelimited implements spaceDelimited / pipeDelimited (defined by the
// OAS for arrays and objects, explode=false; the delimiter separates the
// escaped pieces). An exploded spaceDelimited/pipeDelimited parameter has no
// OAS-defined expansion of its own — the delimiter is unused when each value
// rides its own name=value pair — so it degrades to the form-style exploded
// expansion, matching common OpenAPI tooling. Primitives are undefined for
// these styles and refuse loudly.
func expandDelimited(name string, value any, explode bool, delim string, esc func(string) string) ([]string, error) {
	if explode {
		if _, ok := asArray(value); !ok {
			if _, isObj := asObject(value); !isObj {
				return nil, fmt.Errorf("spaceDelimited/pipeDelimited styles are not defined for primitives")
			}
		}
		return expandFormPairs(name, value, true, esc)
	}
	if arr, ok := asArray(value); ok {
		parts, err := arrayStrings(arr)
		if err != nil {
			return nil, err
		}
		return []string{name + "=" + joinEscaped(parts, delim, esc)}, nil
	}
	if obj, ok := asObject(value); ok {
		pairs, err := objectPairs(obj)
		if err != nil {
			return nil, err
		}
		return []string{name + "=" + joinEscaped(flattenPairs(pairs), delim, esc)}, nil
	}
	return nil, fmt.Errorf("spaceDelimited/pipeDelimited styles are not defined for primitives")
}

// ---------------------------------------------------------------------------
// Value shaping helpers
// ---------------------------------------------------------------------------

// primitiveString renders a JSON primitive in its defined wire form: strings
// verbatim, booleans as true/false, numbers in their canonical JSON
// rendering, null as the empty string. Arrays and objects are not primitives.
func primitiveString(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case json.Number:
		return t.String(), nil
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		b, err := json.Marshal(t)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("value of type %T is not a primitive", v)
	}
}

func asArray(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

func asObject(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, s := range t {
			out[k] = s
		}
		return out, true
	}
	return nil, false
}

// arrayStrings renders each array element as a primitive string (nested
// arrays/objects have no OAS-defined expansion inside a parameter value).
func arrayStrings(arr []any) ([]string, error) {
	out := make([]string, 0, len(arr))
	for i, e := range arr {
		s, err := primitiveString(e)
		if err != nil {
			return nil, fmt.Errorf("array element %d: %w", i, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// objectPairs renders an object's members as ordered [key, value] pairs,
// keys sorted lexicographically for a deterministic expansion (JSON objects
// carry no order).
func objectPairs(obj map[string]any) ([][2]string, error) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		s, err := primitiveString(obj[k])
		if err != nil {
			return nil, fmt.Errorf("object member %q: %w", k, err)
		}
		pairs = append(pairs, [2]string{k, s})
	}
	return pairs, nil
}

func flattenPairs(pairs [][2]string) []string {
	out := make([]string, 0, len(pairs)*2)
	for _, kv := range pairs {
		out = append(out, kv[0], kv[1])
	}
	return out
}

func joinEscaped(parts []string, sep string, esc func(string) string) string {
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = esc(p)
	}
	return strings.Join(escaped, sep)
}

func joinPairs(pairs [][2]string, kvSep, pairSep string, esc func(string) string) string {
	units := make([]string, len(pairs))
	for i, kv := range pairs {
		units[i] = esc(kv[0]) + kvSep + esc(kv[1])
	}
	return strings.Join(units, pairSep)
}

// queryEscape percent-encodes one query-string piece with the
// encodeURIComponent byte set (cross-SDK parity with the path escape); with
// allowReserved, RFC 3986 reserved characters additionally pass through
// unescaped, per the OAS allowReserved rule.
func queryEscape(s string, allowReserved bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		case allowReserved && strings.IndexByte(":/?#[]@$&+,;=", c) >= 0:
			// The full RFC 3986 reserved set is gen-delims + sub-delims;
			// !, ', (, ), and * already pass through above.
			b.WriteByte(c)
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		}
	}
	return b.String()
}

// formURLEncodedEscape percent-encodes one piece of an
// application/x-www-form-urlencoded body for the CONTENT lane — the lane the
// OAS reaches when an Encoding Object declares none of style, explode or
// allowReserved, and which it assigns to RFC 1866 Section 8.2.1 rather than to
// RFC 6570.
//
// Every accepted edition reaches RFC 1866: "the contents in the requestBody
// MUST be stringified per [RFC1866] when passed to the server" (3.0.0-3.0.3,
// 3.1.0) or "the request body MUST be encoded per [RFC1866]" (3.0.4, 3.1.1,
// 3.1.2). Only the latter three carry Appendix E, whose
// normatively-cited-standards table pairs "content-based serialization" with
// "[RFC1866] Section 8.2.1" and percent-encoding "[RFC1738]", and pairs
// "style-based serialization" with "[RFC6570]", noting that it "does not use +
// for form-urlencoded" (E.3 in 3.0.4 / 3.1.1, E.4 in 3.1.2).
//
// RFC 1866 Section 8.2.1 names the space: "The form field names and values are
// escaped: space characters are replaced by `+', and then reserved characters
// are escaped as per [URL]". The rest of the set is delegated, not stated here:
// the following gloss ("that is, non-alphanumeric characters are replaced by
// `%HH'") is stricter than the [URL] rule it presents itself as restating, and
// no accepted OAS edition asks for the stricter form. [URL] is RFC 1738, pinned
// at corpus-lab/authorities/texts/openapi/url/rfc1738.txt. Its Section 2.2
// settles the set below on every edition:
//
//   - "only alphanumerics, the special characters `$-_.+!*'(),`, and reserved
//     characters used for their reserved purposes may be used unencoded" — so
//     leaving `*`, `-`, `.` and `_` literal is permitted;
//   - "characters that are not required to be encoded (including
//     alphanumerics) may be encoded ... as long as they are not being used for
//     a reserved purpose" — so encoding more is permitted too, which is why
//     `+`, reserved by this media type for the space, is escaped in data;
//   - `~` is named among the unsafe characters, and "All unsafe characters must
//     always be encoded within a URL" — so escaping the tilde is required, not
//     a preference.
//
// The set below is the WHATWG form-urlencoded serializer set, which lies inside
// that permission on every edition. On 3.0.4, 3.1.1 and 3.1.2 it is also named
// outright: Appendix E.3.2 (E.4.2 in 3.1.2) gives a SHOULD for WHATWG's
// form-urlencoded rules, and 3.1.2 Section 4.8.12.4 names the tilde.
//
// Which member of that permitted set to pick is the IMPLEMENTATIONS'
// convention, not the binding specification's: the OpenAPI binding-specification family states
// the permitted set and does not narrow it. The pick is pinned by the shared
// twin case table (testdata/urlencoded-escaper-cases.json), executed by both Go
// engines and by openapi-client/typescript.
//
// The STYLE lane keeps revision3URIEscape and its RFC 6570 %20; the two lanes
// disagree about the space character because the OAS assigns them different
// percent-encoding specifications.
func formURLEncodedEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		}
	}
	return b.String()
}
