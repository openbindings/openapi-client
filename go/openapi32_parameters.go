package openapiclient

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// validateOpenAPI32Target is the edition-authoritative parameter admission
// gate. kin-openapi deliberately remains a parser on this lane: its validator
// does not know querystring and its SerializationMethod applies pre-3.2 cells.
func (a *Artifact) validateOpenAPI32Target(target *OperationTarget) (*OperationTarget, error) {
	if a == nil || !a.Edition.IsOpenAPI32() || target == nil || target.Operation == nil {
		return target, nil
	}
	if a.openAPI32 == nil {
		return nil, operationResolutionError(OperationTargetExcluded, "OpenAPI 3.2 artifact has no raw-resource overlay")
	}
	if err := a.openAPI32.validateSelectedParameterDeclarations(target.OperationReference); err != nil {
		return nil, &OperationResolutionError{Kind: OperationTargetExcluded, Message: err.Error(), Cause: err}
	}

	params := effectiveParameters(target.PathItem, target.Operation)
	if identity := duplicateEffectiveParameterIdentity32(params); identity != "" {
		return nil, operationResolutionError(OperationTargetExcluded, "operation has duplicate effective parameter identity %q", identity)
	}
	if name := unflattenableParamForRevision(params, profileFullCoordinate); name != "" {
		return nil, operationResolutionError(OperationTargetExcluded, "operation has no distinct wire identity for parameter %q", name)
	}
	if err := checkEffectiveParameterOwnership(params); err != nil {
		return nil, &OperationResolutionError{Kind: OperationTargetExcluded, Message: err.Error(), Cause: err}
	}
	if err := checkOpenAPI32PathTemplateDeclaration(target.Path, params); err != nil {
		return nil, &OperationResolutionError{Kind: OperationTargetExcluded, Message: err.Error(), Cause: err}
	}
	if other := a.openAPI32.equivalentTemplatedPath(target.Path); other != "" {
		return nil, operationResolutionError(OperationTargetExcluded, "path %q has the same templated hierarchy as %q", target.Path, other)
	}

	queryCount, queryStringCount := 0, 0
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		parameter := ref.Value
		switch parameter.In {
		case openapi3.ParameterInQuery:
			queryCount++
		case ParameterInQueryString:
			queryStringCount++
		}
		if err := validateOpenAPI32TypedParameter(target.Document, parameter); err != nil {
			return nil, &OperationResolutionError{Kind: OperationTargetExcluded, Message: fmt.Sprintf("parameter %q: %v", parameter.Name, err), Cause: err}
		}
	}
	if queryStringCount > 1 {
		return nil, operationResolutionError(OperationTargetExcluded, "operation has more than one effective querystring parameter")
	}
	if queryStringCount > 0 && queryCount > 0 {
		return nil, operationResolutionError(OperationTargetExcluded, "querystring and ordinary query parameters are mutually exclusive")
	}
	return target, nil
}

func duplicateEffectiveParameterIdentity32(params openapi3.Parameters) string {
	seen := map[string]bool{}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		parameter := ref.Value
		identity := parameter.In + "\x00" + parameter.Name
		if seen[identity] {
			return parameter.In + "/" + escapeJSONPointerSegment(parameter.Name)
		}
		seen[identity] = true
	}
	return ""
}

func validateOpenAPI32TypedParameter(document *openapi3.T, parameter *openapi3.Parameter) error {
	if parameter == nil {
		return fmt.Errorf("declaration is absent")
	}
	switch parameter.In {
	case openapi3.ParameterInPath, openapi3.ParameterInQuery, ParameterInQueryString, openapi3.ParameterInHeader, openapi3.ParameterInCookie:
	default:
		return fmt.Errorf("location %q is outside the closed 3.2 location set", parameter.In)
	}
	if parameter.In == openapi3.ParameterInPath && !parameter.Required {
		return fmt.Errorf("path parameter does not declare required: true")
	}
	if (parameter.Schema != nil) == (parameter.Content != nil) {
		return fmt.Errorf("declaration must use exactly one of schema or content")
	}
	if parameter.Content != nil && len(parameter.Content) != 1 {
		return fmt.Errorf("content must contain exactly one media type")
	}
	if parameter.In == ParameterInQueryString {
		if parameter.Schema != nil || parameter.Content == nil {
			return fmt.Errorf("querystring requires content-form carriage")
		}
		return validateOpenAPI32QueryStringMedia(document, parameter)
	}
	if parameter.Schema == nil {
		return nil
	}
	if err := validateRevision3ParameterSerializationForEdition(parameter, false, string(EditionOpenAPI320)); err != nil {
		return err
	}
	if member := parameterStyleLaneUndefinedExpansionMember(parameter, false); member != "" {
		return fmt.Errorf("resolved compound member %q has no defined style expansion", member)
	}
	if openAPI32FormCookieAlwaysProducesMultiplePairs(parameter) {
		return fmt.Errorf("form-style exploded cookie declaration always produces multiple cookie pairs")
	}
	return nil
}

func validateOpenAPI32QueryStringMedia(document *openapi3.T, parameter *openapi3.Parameter) error {
	for mediaType, media := range parameter.Content {
		parsed, err := parseRevision3MediaType(mediaType)
		if err != nil {
			return fmt.Errorf("querystring content %q is invalid: %w", mediaType, err)
		}
		if media != nil && media.ItemSchema != nil {
			return fmt.Errorf("querystring content %q selects a sequential representation", mediaType)
		}
		switch {
		case isJSONMediaType(parsed.base), parsed.base == "text/plain":
			return nil
		case parsed.base == "application/x-www-form-urlencoded":
			if media == nil || media.Schema == nil || media.Schema.Value == nil {
				return fmt.Errorf("querystring form content has no application-value schema")
			}
			resolved := resolveDeclaration(media.Schema.Value, false)
			if resolved.declaresOnly("null", "boolean", "number", "integer", "string", "array") {
				return fmt.Errorf("querystring form content requires an object application value")
			}
			return nil
		default:
			return fmt.Errorf("querystring content %q has no incorporated serialization", mediaType)
		}
	}
	return fmt.Errorf("querystring content is absent")
}

func openAPI32FormCookieAlwaysProducesMultiplePairs(parameter *openapi3.Parameter) bool {
	if parameter == nil || parameter.In != openapi3.ParameterInCookie || parameter.Schema == nil || parameter.Schema.Value == nil {
		return false
	}
	method, err := revision3ParameterSerializationMethodForEdition(parameter, string(EditionOpenAPI320))
	if err != nil || method.Style != openapi3.SerializationForm || !method.Explode {
		return false
	}
	resolved := resolveDeclaration(parameter.Schema.Value, false)
	return resolved.declaresOnly("array") || resolved.declaresOnly("object") && len(resolved.propertyNames()) > 0
}

func checkOpenAPI32PathTemplateDeclaration(path string, params openapi3.Parameters) error {
	declared := map[string]bool{}
	for _, ref := range params {
		if ref != nil && ref.Value != nil && ref.Value.In == openapi3.ParameterInPath {
			declared[ref.Value.Name] = true
		}
	}
	seen := map[string]bool{}
	var missing, duplicates []string
	for _, name := range pathTemplateVariables(path) {
		if seen[name] {
			duplicates = append(duplicates, name)
		}
		seen[name] = true
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("path template expression(s) %s have no effective path parameter", strings.Join(missing, ", "))
	}
	if len(duplicates) > 0 {
		sort.Strings(duplicates)
		return fmt.Errorf("path template expression(s) %s occur more than once", strings.Join(duplicates, ", "))
	}
	var unmatched []string
	for name := range declared {
		if !seen[name] {
			unmatched = append(unmatched, name)
		}
	}
	if len(unmatched) > 0 {
		sort.Strings(unmatched)
		return fmt.Errorf("effective path parameter(s) %s have no template expression", strings.Join(unmatched, ", "))
	}
	return nil
}

func normalizedOpenAPI32PathHierarchy(path string) (string, bool) {
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

func (o *OpenAPI32Overlay) equivalentTemplatedPath(selected string) string {
	if o == nil {
		return ""
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	want, templated := normalizedOpenAPI32PathHierarchy(selected)
	if !templated || o.entry == nil {
		return ""
	}
	root, _ := o.entry.root.(map[string]any)
	paths, _ := root["paths"].(map[string]any)
	for candidate := range paths {
		if candidate == selected {
			continue
		}
		if normalized, candidateTemplated := normalizedOpenAPI32PathHierarchy(candidate); candidateTemplated && normalized == want {
			return candidate
		}
	}
	return ""
}

type openAPI32RawParameter struct {
	value any
	base  *url.URL
}

func (o *OpenAPI32Overlay) validateSelectedParameterDeclarations(reference OperationReference) error {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.entry == nil {
		return nil
	}
	root, _ := o.entry.root.(map[string]any)
	paths, _ := root["paths"].(map[string]any)
	adjacent, _ := paths[reference.Path].(map[string]any)
	referenced, referencedBase, err := o.referencedPathItemLocked(adjacent, o.entry.base)
	if err != nil && adjacent != nil {
		return err
	}
	pathOwner, pathBase := adjacent, o.entry.base
	if _, present := adjacent["parameters"]; !present && referenced != nil {
		pathOwner, pathBase = referenced, referencedBase
	}
	operation := rawOperationValue(adjacent, reference)
	operationBase := o.entry.base
	if operation == nil {
		operation = rawOperationValue(referenced, reference)
		operationBase = referencedBase
	}
	operationObject, _ := operation.(map[string]any)

	pathRows, err := rawOpenAPI32ParameterRows(pathOwner["parameters"], pathBase)
	if err != nil {
		return err
	}
	operationRows, err := rawOpenAPI32ParameterRows(operationObject["parameters"], operationBase)
	if err != nil {
		return err
	}
	overridden := map[string]bool{}
	for _, row := range operationRows {
		parameter, resolveErr := o.resolveRawParameterLocked(row, map[string]bool{})
		if resolveErr != nil {
			return resolveErr
		}
		if identity, ok := rawOpenAPI32ParameterIdentity(parameter); ok {
			overridden[identity] = true
		}
	}
	effective := make([]openAPI32RawParameter, 0, len(pathRows)+len(operationRows))
	for _, row := range pathRows {
		parameter, resolveErr := o.resolveRawParameterLocked(row, map[string]bool{})
		if resolveErr != nil {
			return resolveErr
		}
		if identity, ok := rawOpenAPI32ParameterIdentity(parameter); ok && overridden[identity] {
			continue
		}
		effective = append(effective, row)
	}
	effective = append(effective, operationRows...)

	queryCount, queryStringCount := 0, 0
	for _, row := range effective {
		parameter, resolveErr := o.resolveRawParameterLocked(row, map[string]bool{})
		if resolveErr != nil {
			return resolveErr
		}
		if rawOpenAPI32IgnoredHeader(parameter) {
			continue
		}
		if err := validateRawOpenAPI32Parameter(parameter); err != nil {
			return err
		}
		switch parameter["in"] {
		case openapi3.ParameterInQuery:
			queryCount++
		case ParameterInQueryString:
			queryStringCount++
		}
	}
	if queryStringCount > 1 {
		return fmt.Errorf("operation has more than one effective querystring parameter")
	}
	if queryStringCount > 0 && queryCount > 0 {
		return fmt.Errorf("querystring and ordinary query parameters are mutually exclusive")
	}
	return nil
}

func rawOpenAPI32ParameterRows(value any, base *url.URL) ([]openAPI32RawParameter, error) {
	if value == nil {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("parameters field is not an array")
	}
	result := make([]openAPI32RawParameter, len(values))
	for index, parameter := range values {
		result[index] = openAPI32RawParameter{value: parameter, base: base}
	}
	return result, nil
}

func rawOpenAPI32ParameterIdentity(parameter map[string]any) (string, bool) {
	name, nameOK := parameter["name"].(string)
	in, inOK := parameter["in"].(string)
	return in + "\x00" + name, nameOK && inOK
}

func rawOpenAPI32IgnoredHeader(parameter map[string]any) bool {
	if parameter["in"] != openapi3.ParameterInHeader {
		return false
	}
	name, _ := parameter["name"].(string)
	canonical := http.CanonicalHeaderKey(name)
	return canonical == "Accept" || canonical == "Content-Type" || canonical == "Authorization"
}

func validateRawOpenAPI32Parameter(parameter map[string]any) error {
	if parameter == nil {
		return fmt.Errorf("effective Parameter Object is absent")
	}
	name, nameOK := parameter["name"].(string)
	in, inOK := parameter["in"].(string)
	if !nameOK || !inOK {
		return fmt.Errorf("effective Parameter Object must declare string name and in fields")
	}
	switch in {
	case openapi3.ParameterInPath, openapi3.ParameterInQuery, ParameterInQueryString, openapi3.ParameterInHeader, openapi3.ParameterInCookie:
	default:
		return fmt.Errorf("parameter %q declares unsupported location %q", name, in)
	}
	_, hasSchema := parameter["schema"]
	content, hasContent := parameter["content"]
	if hasSchema == hasContent {
		return fmt.Errorf("parameter %q must use exactly one of schema or content", name)
	}
	if in == openapi3.ParameterInPath && parameter["required"] != true {
		return fmt.Errorf("path parameter %q must declare required: true", name)
	}
	if hasContent {
		contentMap, ok := content.(map[string]any)
		if !ok || len(contentMap) != 1 {
			return fmt.Errorf("parameter %q content must contain exactly one media type", name)
		}
	}
	if in == ParameterInQueryString {
		if !hasContent {
			return fmt.Errorf("querystring parameter %q must use content", name)
		}
		for _, field := range []string{"schema", "style", "explode", "allowReserved", "allowEmptyValue"} {
			if _, present := parameter[field]; present {
				return fmt.Errorf("querystring parameter %q declares forbidden schema-form field %q", name, field)
			}
		}
	}
	return nil
}

func (o *OpenAPI32Overlay) resolveRawParameterLocked(row openAPI32RawParameter, seen map[string]bool) (map[string]any, error) {
	parameter, ok := row.value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Parameter Object is not an object")
	}
	refText, hasRef := parameter["$ref"].(string)
	if !hasRef {
		return parameter, nil
	}
	parsed, err := url.Parse(refText)
	if err != nil {
		return nil, fmt.Errorf("Parameter reference %q is invalid", refText)
	}
	var resolved *url.URL
	switch {
	case strings.HasPrefix(refText, "#"):
		resolved = cloneURL(row.base)
		if resolved == nil {
			resolved = &url.URL{}
		}
		resolved.Fragment = parsed.Fragment
	case parsed.IsAbs():
		resolved = parsed
	case row.base != nil:
		resolved = row.base.ResolveReference(parsed)
	default:
		return nil, fmt.Errorf("Parameter reference %q has no document base", refText)
	}
	key := resolved.String()
	if seen[key] {
		return nil, fmt.Errorf("Parameter reference cycle at %q has no concrete declaration", refText)
	}
	seen[key] = true
	resource := o.resources[artifactResourceKey(resolved)]
	if resource == nil && strings.HasPrefix(refText, "#") {
		resource = o.entry
	}
	if resource == nil {
		return nil, fmt.Errorf("Parameter reference %q is unresolvable", refText)
	}
	target, found := rawFragmentTarget(resource.root, resolved.Fragment, rawParameterTarget)
	if !found {
		return nil, fmt.Errorf("Parameter reference %q names no Parameter Object", refText)
	}
	return o.resolveRawParameterLocked(openAPI32RawParameter{value: target, base: resource.base}, seen)
}

func serializeQueryStringParameter(document *openapi3.T, parameter *openapi3.Parameter, value any, bindingSpec string) (string, error) {
	if parameter == nil || len(parameter.Content) != 1 {
		return "", fmt.Errorf("querystring parameter must declare exactly one content media type")
	}
	for mediaType, media := range parameter.Content {
		parsed, err := parseRevision3MediaType(mediaType)
		if err != nil {
			return "", err
		}
		if parsed.base == "application/x-www-form-urlencoded" {
			fields, ok := asObject(value)
			if !ok {
				return "", fmt.Errorf("querystring form content requires an object value, got %T", value)
			}
			return buildURLEncodedBodyForRevision(document, media, fields, bindingSpec)
		}
		serialized, err := serializeParamContentFor(parameter, value, bindingSpec)
		if err != nil {
			return "", err
		}
		return revision3URIEscape(serialized, false, false), nil
	}
	return "", fmt.Errorf("querystring parameter content is absent")
}

func validateOpenAPI32CookieUnits(units []string) error {
	for _, unit := range units {
		separator := strings.IndexByte(unit, '=')
		if separator <= 0 {
			return fmt.Errorf("serialized cookie contribution %q is not name=value", unit)
		}
		if !httpToken(unit[:separator]) {
			return fmt.Errorf("serialized cookie name %q is not an RFC 6265 cookie-name", unit[:separator])
		}
		if !validRFC6265CookieValue(unit[separator+1:]) {
			return fmt.Errorf("serialized cookie value for %q is not an RFC 6265 cookie-value", unit[:separator])
		}
	}
	return nil
}
