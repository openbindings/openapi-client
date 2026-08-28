package openapiclient

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// openAPI32Fallback confines a typed-loader failure by loading every raw
// operation in isolation. Each image contains only that operation and the
// transitive internal-reference closure it composes; an unreachable defect
// therefore cannot become a document-wide kin-openapi failure. This fallback
// is edition-specific and runs only after the ordinary complete load fails.
type openAPI32Fallback struct {
	document *openapi3.T
	targets  map[string]*OperationTarget
	errors   map[string]error
	used     bool
}

func buildOpenAPI32Fallback(overlay *OpenAPI32Overlay, load func([]byte) (*openapi3.T, error)) openAPI32Fallback {
	if overlay == nil {
		return openAPI32Fallback{}
	}
	return buildOpenAPI32Targets(overlay, overlay.operationReferences(), load)
}

func buildOpenAPI32Targets(overlay *OpenAPI32Overlay, references []OperationReference, load func([]byte) (*openapi3.T, error)) openAPI32Fallback {
	result := openAPI32Fallback{
		targets: map[string]*OperationTarget{},
		errors:  map[string]error{},
	}
	if overlay == nil || load == nil {
		return result
	}
	for _, reference := range references {
		var document *openapi3.T
		var err error
		for _, pruneComponents := range []bool{false, true} {
			var image []byte
			image, err = overlay.operationImage(reference, pruneComponents)
			if err != nil {
				break
			}
			document, err = load(image)
			if err == nil {
				break
			}
		}
		if err != nil {
			result.errors[reference.Ref] = &OperationResolutionError{
				Kind: OperationTargetExcluded, Message: fmt.Sprintf("selected operation closure is unresolvable: %v", err), Cause: err,
			}
			continue
		}
		pathItem := document.Paths.Find(reference.Path)
		if pathItem == nil {
			result.errors[reference.Ref] = operationResolutionError(OperationTargetExcluded, "selected operation closure produced no path %q", reference.Path)
			continue
		}
		operation := operationFromPathItem(pathItem, reference)
		if operation == nil {
			result.errors[reference.Ref] = operationResolutionError(OperationTargetExcluded, "selected operation closure produced no operation %q", reference.Ref)
			continue
		}
		target := &OperationTarget{OperationReference: reference, Document: document, PathItem: pathItem, Operation: operation}
		result.targets[reference.Ref] = target
		if result.document == nil {
			result.document = document
		}
	}
	result.used = len(result.targets) > 0 || len(result.errors) > 0
	return result
}

func (o *OpenAPI32Overlay) composedOperationReferences() []OperationReference {
	references := o.operationReferences()
	if len(references) == 0 {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	root, _ := o.entry.root.(map[string]any)
	paths, _ := root["paths"].(map[string]any)
	result := make([]OperationReference, 0, len(references))
	for _, reference := range references {
		adjacent, _ := paths[reference.Path].(map[string]any)
		if _, hasRef := adjacent["$ref"]; !hasRef {
			continue
		}
		result = append(result, reference)
	}
	return result
}

func operationFromPathItem(pathItem *openapi3.PathItem, reference OperationReference) *openapi3.Operation {
	if pathItem == nil {
		return nil
	}
	switch {
	case reference.Additional:
		return pathItem.AdditionalOperations[reference.Method]
	case reference.Method == "query":
		return pathItem.Query
	default:
		return pathItem.GetOperation(strings.ToUpper(reference.Method))
	}
}

func (o *OpenAPI32Overlay) operationReferences() []OperationReference {
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
	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	var result []OperationReference
	for _, path := range pathNames {
		adjacent, _ := paths[path].(map[string]any)
		if adjacent == nil {
			continue
		}
		referenced, _, _ := o.referencedPathItemLocked(adjacent, o.entry.base)
		for _, method := range openAPI32FixedMethods {
			if rawOperationPresent(adjacent, method, false) || rawOperationPresent(referenced, method, false) {
				result = append(result, OperationReference{
					Ref: "#/paths/" + escapeJSONPointerSegment(path) + "/" + method, Path: path, Method: method,
				})
			}
		}
		additional := map[string]bool{}
		for _, pathItem := range []map[string]any{referenced, adjacent} {
			operations, _ := pathItem["additionalOperations"].(map[string]any)
			for method := range operations {
				additional[method] = true
			}
		}
		methods := make([]string, 0, len(additional))
		for method := range additional {
			methods = append(methods, method)
		}
		sort.Strings(methods)
		for _, method := range methods {
			result = append(result, OperationReference{
				Ref:        "#/paths/" + escapeJSONPointerSegment(path) + "/additionalOperations/" + escapeJSONPointerSegment(method),
				Path:       path,
				Method:     method,
				Additional: true,
			})
		}
	}
	return result
}

func rawOperationPresent(pathItem map[string]any, method string, additional bool) bool {
	if pathItem == nil {
		return false
	}
	if additional {
		operations, _ := pathItem["additionalOperations"].(map[string]any)
		_, present := operations[method]
		return present
	}
	_, present := pathItem[method]
	return present
}

func (o *OpenAPI32Overlay) referencedPathItemLocked(adjacent map[string]any, base *url.URL) (map[string]any, *url.URL, error) {
	refText, _ := adjacent["$ref"].(string)
	if refText == "" {
		return nil, nil, nil
	}
	parsed, err := url.Parse(refText)
	if err != nil {
		return nil, nil, fmt.Errorf("Path Item reference %q is invalid", refText)
	}
	var resolved *url.URL
	switch {
	case strings.HasPrefix(refText, "#"):
		resolved = cloneURL(base)
		if resolved == nil {
			resolved = &url.URL{}
		}
		resolved.Fragment = parsed.Fragment
	case parsed.IsAbs():
		resolved = parsed
	case base != nil:
		resolved = base.ResolveReference(parsed)
	default:
		return nil, nil, fmt.Errorf("Path Item reference %q has no document base", refText)
	}
	resource := o.resources[artifactResourceKey(resolved)]
	if resource == nil && strings.HasPrefix(refText, "#") {
		resource = o.entry
	}
	if resource == nil {
		return nil, nil, fmt.Errorf("Path Item reference %q is unresolvable", refText)
	}
	if resource.selfError != "" {
		return nil, nil, fmt.Errorf("Path Item reaches a resource with unusable %s", resource.selfError)
	}
	if resource.self != nil && artifactResourceKey(resolved) != artifactResourceKey(resource.self) {
		return nil, nil, fmt.Errorf("Path Item reference uses retrieval alias %q instead of declared $self identity %q", artifactResourceKey(resolved), artifactResourceKey(resource.self))
	}
	target, ok := rawFragmentTarget(resource.root, resolved.Fragment, rawPathItemTarget)
	if !ok {
		return nil, nil, fmt.Errorf("Path Item reference %q names no target", refText)
	}
	referenced, _ := target.(map[string]any)
	if referenced == nil {
		return nil, nil, fmt.Errorf("Path Item reference %q does not name an object", refText)
	}
	return referenced, resource.base, nil
}

func (o *OpenAPI32Overlay) operationImage(reference OperationReference, pruneComponents bool) ([]byte, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.entry == nil {
		return nil, fmt.Errorf("OpenAPI 3.2 overlay has no entry resource")
	}
	root, _ := o.entry.root.(map[string]any)
	paths, _ := root["paths"].(map[string]any)
	adjacent, _ := paths[reference.Path].(map[string]any)
	if adjacent == nil {
		return nil, fmt.Errorf("path %q is absent", reference.Path)
	}
	referenced, referencedBase, refErr := o.referencedPathItemLocked(adjacent, o.entry.base)
	if refErr != nil {
		return nil, refErr
	}
	if err := selectedPathItemCollision(adjacent, referenced, reference); err != nil {
		return nil, err
	}
	if err := o.validateSelectedRequestReferences(referenced, referencedBase, reference.Method, reference.Additional); err != nil {
		return nil, err
	}
	if err := o.validateSelectedRequestReferences(adjacent, o.entry.base, reference.Method, reference.Additional); err != nil {
		return nil, err
	}
	rawOperation := rawOperationValue(adjacent, reference)
	if rawOperation == nil {
		rawOperation = rawOperationValue(referenced, reference)
	}
	if rawOperation == nil {
		return nil, fmt.Errorf("operation %q is absent", reference.Ref)
	}

	pathItem := map[string]any{}
	for _, field := range []string{"summary", "description", "parameters", "servers"} {
		if value, present := referenced[field]; present {
			pathItem[field] = cloneOverlayValue(value, map[uintptr]any{})
		}
		if value, present := adjacent[field]; present {
			pathItem[field] = cloneOverlayValue(value, map[uintptr]any{})
		}
	}
	operationCopy := cloneOverlayValue(rawOperation, map[uintptr]any{})
	if operation, ok := operationCopy.(map[string]any); ok {
		// Dependencies are an explicit M6 seam. They cannot make the M5 request
		// image unparseable, and they are preserved in the artifact overlay.
		delete(operation, "callbacks")
	}
	if reference.Additional {
		pathItem["additionalOperations"] = map[string]any{reference.Method: operationCopy}
	} else {
		pathItem[reference.Method] = operationCopy
	}

	variant := map[string]any{}
	for _, field := range []string{"openapi", "$self", "info", "jsonSchemaDialect", "servers", "security", "tags", "externalDocs"} {
		if value, present := root[field]; present {
			variant[field] = cloneOverlayValue(value, map[uintptr]any{})
		}
	}
	variant["paths"] = map[string]any{reference.Path: pathItem}
	if !pruneComponents {
		if components, present := root["components"]; present {
			variant["components"] = cloneOverlayValue(components, map[uintptr]any{})
		}
	} else {
		if err := copyOpenAPI32InternalReferenceClosure(root, variant); err != nil {
			return nil, err
		}
		copyOpenAPI32ImplicitSecuritySchemes(root, variant)
	}
	return json.Marshal(variant)
}

func rawOperationValue(pathItem map[string]any, reference OperationReference) any {
	if pathItem == nil {
		return nil
	}
	if reference.Additional {
		operations, _ := pathItem["additionalOperations"].(map[string]any)
		return operations[reference.Method]
	}
	return pathItem[reference.Method]
}

func selectedPathItemCollision(adjacent, referenced map[string]any, reference OperationReference) error {
	if referenced == nil {
		return nil
	}
	for _, field := range []string{"parameters", "servers"} {
		if _, left := adjacent[field]; left {
			if _, right := referenced[field]; right {
				return fmt.Errorf("selected Path Item $ref has undefined adjacent collision at %q", field)
			}
		}
	}
	if rawOperationPresent(adjacent, reference.Method, reference.Additional) && rawOperationPresent(referenced, reference.Method, reference.Additional) {
		if reference.Additional {
			return fmt.Errorf("selected Path Item $ref has undefined adjacent collision at additional operation %q", reference.Method)
		}
		return fmt.Errorf("selected Path Item $ref has undefined adjacent collision at %q", reference.Method)
	}
	return nil
}

func copyOpenAPI32InternalReferenceClosure(original, variant map[string]any) error {
	retained := &retainedPointers{}
	seen := map[string]bool{}
	var queue []string
	enqueue := func(value any) {
		rawReferenceStrings(value, func(ref string) {
			if strings.HasPrefix(ref, "#") && !seen[ref] {
				seen[ref] = true
				queue = append(queue, ref)
			}
		})
	}
	enqueue(variant)
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		parsed, err := url.Parse(ref)
		if err != nil {
			return fmt.Errorf("selected operation reference %q is invalid", ref)
		}
		fragment := parsed.Fragment
		var target any
		var pointer string
		var ok bool
		if strings.HasPrefix(fragment, "/") {
			target, ok = rawFragmentTarget(original, fragment, rawSchemaTarget)
			pointer = fragment
		} else {
			target, pointer, ok = rawAnchoredNode(original, fragment)
		}
		if !ok {
			return fmt.Errorf("selected operation reference %q names no value", ref)
		}
		retained.add(pointer)
		enqueue(target)
	}
	if retained.root == nil {
		return nil
	}
	closure, _ := pruneToRetained(original, retained.root)
	mergeOpenAPI32RawMaps(variant, closure)
	return nil
}

func mergeOpenAPI32RawMaps(target map[string]any, value any) {
	source, _ := value.(map[string]any)
	for key, child := range source {
		if existing, present := target[key]; present {
			existingMap, existingIsMap := existing.(map[string]any)
			childMap, childIsMap := child.(map[string]any)
			if existingIsMap && childIsMap {
				mergeOpenAPI32RawMaps(existingMap, childMap)
			}
			continue
		}
		target[key] = cloneOverlayValue(child, map[uintptr]any{})
	}
}

func copyOpenAPI32ImplicitSecuritySchemes(original, variant map[string]any) {
	names := map[string]bool{}
	collect := func(value any) {
		requirements, _ := value.([]any)
		for _, item := range requirements {
			requirement, _ := item.(map[string]any)
			for name := range requirement {
				names[name] = true
			}
		}
	}
	collect(variant["security"])
	paths, _ := variant["paths"].(map[string]any)
	for _, pathValue := range paths {
		pathItem, _ := pathValue.(map[string]any)
		for _, method := range openAPI32FixedMethods {
			if operation, ok := pathItem[method].(map[string]any); ok {
				collect(operation["security"])
			}
		}
		additional, _ := pathItem["additionalOperations"].(map[string]any)
		for _, operationValue := range additional {
			if operation, ok := operationValue.(map[string]any); ok {
				collect(operation["security"])
			}
		}
	}
	if len(names) == 0 {
		return
	}
	components, _ := original["components"].(map[string]any)
	schemes, _ := components["securitySchemes"].(map[string]any)
	selected := map[string]any{}
	for name := range names {
		if scheme, present := schemes[name]; present {
			selected[name] = cloneOverlayValue(scheme, map[uintptr]any{})
		}
	}
	if len(selected) == 0 {
		return
	}
	variantComponents, _ := variant["components"].(map[string]any)
	if variantComponents == nil {
		variantComponents = map[string]any{}
		variant["components"] = variantComponents
	}
	variantSchemes, _ := variantComponents["securitySchemes"].(map[string]any)
	if variantSchemes == nil {
		variantSchemes = map[string]any{}
		variantComponents["securitySchemes"] = variantSchemes
	}
	for name, scheme := range selected {
		variantSchemes[name] = scheme
	}
}
