package openapiclient

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// materializeOpenAPI32ResponseTarget builds the response side of one selected
// operation from the artifact's raw-resource closure. kin-openapi remains the
// typed parser, while the overlay owns the 3.2-only identity rule and Media
// Type Reference Object positions that its current typed model cannot load.
func (a *Artifact) materializeOpenAPI32ResponseTarget(target *OperationTarget) (*OperationTarget, error) {
	if a == nil || a.openAPI32 == nil || target == nil || target.Operation == nil {
		return target, nil
	}
	o := a.openAPI32
	o.mu.RLock()
	operationNode, found := o.selectedRawOperationLocked(target.OperationReference, map[string]bool{})
	var rawResponses map[string]any
	if found {
		operation, _ := operationNode.value.(map[string]any)
		rawResponses, _ = operation["responses"].(map[string]any)
	}
	o.mu.RUnlock()
	if !found || rawResponses == nil {
		return target, nil
	}

	responses := openapi3.NewResponsesWithCapacity(len(rawResponses))
	var exclusions []OpenAPI32ResponseMediaExclusion
	for key, raw := range rawResponses {
		if strings.HasPrefix(key, "x-") {
			if responses.Extensions == nil {
				responses.Extensions = map[string]any{}
			}
			responses.Extensions[key] = cloneOverlayValue(raw, map[uintptr]any{})
			continue
		}
		node, err := o.resolveOpenAPI32ObjectNode(
			openAPI32RawNode{value: raw, resource: operationNode.resource},
			rawResponseTarget, "Response Object", map[string]bool{},
		)
		if err != nil {
			return nil, fmt.Errorf("response %q reference is unresolvable: %w", key, err)
		}
		response, mediaExclusions, err := o.materializeOpenAPI32Response(key, node)
		if err != nil {
			return nil, fmt.Errorf("response %q closure is unresolvable: %w", key, err)
		}
		exclusions = append(exclusions, mediaExclusions...)
		responses.Set(key, &openapi3.ResponseRef{Value: response})
	}

	copyOperation := *target.Operation
	copyOperation.Responses = responses
	copyTarget := *target
	copyTarget.Operation = &copyOperation
	copyTarget.ResponseMediaExclusions = exclusions
	return &copyTarget, nil
}

func (o *OpenAPI32Overlay) materializeOpenAPI32Response(key string, node openAPI32RawNode) (*openapi3.Response, []OpenAPI32ResponseMediaExclusion, error) {
	object, _ := node.value.(map[string]any)
	if object == nil {
		return nil, nil, fmt.Errorf("Response Object is not an object")
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, nil, err
	}
	var response openapi3.Response
	if err := json.Unmarshal(encoded, &response); err != nil {
		return nil, nil, err
	}

	if rawHeaders, present := object["headers"].(map[string]any); present {
		response.Headers = make(openapi3.Headers, len(rawHeaders))
		for name, raw := range rawHeaders {
			headerNode, resolveErr := o.resolveOpenAPI32ObjectNode(
				openAPI32RawNode{value: raw, resource: node.resource},
				rawHeaderTarget, "Header Object", map[string]bool{},
			)
			if resolveErr != nil {
				return nil, nil, fmt.Errorf("header %q: %w", name, resolveErr)
			}
			header, materializeErr := o.materializeOpenAPI32ResponseHeader(headerNode)
			if materializeErr != nil {
				return nil, nil, fmt.Errorf("header %q: %w", name, materializeErr)
			}
			response.Headers[name] = &openapi3.HeaderRef{Value: header}
		}
	}
	if rawLinks, present := object["links"].(map[string]any); present {
		response.Links = make(openapi3.Links, len(rawLinks))
		for name, raw := range rawLinks {
			linkNode, resolveErr := o.resolveOpenAPI32ObjectNode(
				openAPI32RawNode{value: raw, resource: node.resource},
				rawResponseTarget, "Link Object", map[string]bool{},
			)
			if resolveErr != nil {
				return nil, nil, fmt.Errorf("link %q: %w", name, resolveErr)
			}
			linkObject, _ := linkNode.value.(map[string]any)
			encodedLink, marshalErr := json.Marshal(linkObject)
			if marshalErr != nil {
				return nil, nil, fmt.Errorf("link %q: %w", name, marshalErr)
			}
			var link openapi3.Link
			if unmarshalErr := json.Unmarshal(encodedLink, &link); unmarshalErr != nil {
				return nil, nil, fmt.Errorf("link %q: %w", name, unmarshalErr)
			}
			response.Links[name] = &openapi3.LinkRef{Value: &link}
		}
	}

	var exclusions []OpenAPI32ResponseMediaExclusion
	if rawContent, present := object["content"].(map[string]any); present {
		response.Content = make(openapi3.Content, len(rawContent))
		for mediaType, raw := range rawContent {
			media, materializeErr := o.materializeOpenAPI32ResponseMedia(
				openAPI32RawNode{value: raw, resource: node.resource},
			)
			if materializeErr != nil {
				exclusions = append(exclusions, OpenAPI32ResponseMediaExclusion{
					ResponseKey: key, MediaType: mediaType, Reason: materializeErr.Error(),
				})
				continue
			}
			response.Content[mediaType] = media
		}
	}
	return &response, exclusions, nil
}

func (o *OpenAPI32Overlay) materializeOpenAPI32ResponseHeader(node openAPI32RawNode) (*openapi3.Header, error) {
	object, _ := node.value.(map[string]any)
	if object == nil {
		return nil, fmt.Errorf("Header Object is not an object")
	}
	if raw, present := object["schema"]; present {
		if err := o.validateOpenAPI32ResponseSchema(raw, node.resource, node.resource.base, map[string]bool{}); err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	var header openapi3.Header
	if err := json.Unmarshal(encoded, &header); err != nil {
		return nil, err
	}
	o.mu.RLock()
	header.Schema = o.materializeOpenAPI32SchemaRefLocked(object["schema"], node.resource, node.resource.base, map[string]bool{})
	o.mu.RUnlock()
	if rawContent, present := object["content"].(map[string]any); present {
		header.Content = make(openapi3.Content, len(rawContent))
		for mediaType, raw := range rawContent {
			media, materializeErr := o.materializeOpenAPI32ResponseMedia(openAPI32RawNode{value: raw, resource: node.resource})
			if materializeErr != nil {
				return nil, fmt.Errorf("content %q: %w", mediaType, materializeErr)
			}
			header.Content[mediaType] = media
		}
	}
	return &header, nil
}

func (o *OpenAPI32Overlay) materializeOpenAPI32ResponseMedia(node openAPI32RawNode) (*openapi3.MediaType, error) {
	resolved, err := o.resolveOpenAPI32ObjectNode(node, rawRequestBodyTarget, "Media Type Object", map[string]bool{})
	if err != nil {
		return nil, err
	}
	object, _ := resolved.value.(map[string]any)
	if object == nil {
		return nil, fmt.Errorf("Media Type Object is not an object")
	}
	for _, field := range []string{"schema", "itemSchema"} {
		if raw, present := object[field]; present {
			if err := o.validateOpenAPI32ResponseSchema(raw, resolved.resource, resolved.resource.base, map[string]bool{}); err != nil {
				return nil, fmt.Errorf("%s reference is unresolvable: %w", field, err)
			}
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	var media openapi3.MediaType
	if err := json.Unmarshal(encoded, &media); err != nil {
		return nil, err
	}
	o.mu.RLock()
	media.Schema = o.materializeOpenAPI32SchemaRefLocked(object["schema"], resolved.resource, resolved.resource.base, map[string]bool{})
	media.ItemSchema = o.materializeOpenAPI32SchemaRefLocked(object["itemSchema"], resolved.resource, resolved.resource.base, map[string]bool{})
	o.mu.RUnlock()
	return &media, nil
}

// resolveOpenAPI32ObjectNode follows Reference Objects at an OAS object
// position. Reference Object siblings are ignored, and a repeated target ends
// a legitimate cycle without exhausting the resource graph.
func (o *OpenAPI32Overlay) resolveOpenAPI32ObjectNode(node openAPI32RawNode, kind rawRefTargetKind, label string, seen map[string]bool) (openAPI32RawNode, error) {
	object, _ := node.value.(map[string]any)
	if object == nil {
		return openAPI32RawNode{}, fmt.Errorf("%s is not an object", label)
	}
	refText, _ := object["$ref"].(string)
	if refText == "" {
		return node, nil
	}
	resolved, err := openAPI32ResolvedReference(refText, node.resource, nil)
	if err != nil {
		return openAPI32RawNode{}, err
	}
	key := resolved.String()
	if seen[key] {
		return openAPI32RawNode{value: map[string]any{}, resource: node.resource}, nil
	}
	seen[key] = true
	resource, err := o.openAPI32ReferenceResource(refText, resolved, node.resource)
	if err != nil {
		return openAPI32RawNode{}, err
	}
	target, ok := rawFragmentTarget(resource.root, resolved.Fragment, kind)
	if !ok {
		return openAPI32RawNode{}, fmt.Errorf("reference %q names no %s", refText, label)
	}
	return o.resolveOpenAPI32ObjectNode(openAPI32RawNode{value: target, resource: resource}, kind, label, seen)
}

func (o *OpenAPI32Overlay) openAPI32ReferenceResource(refText string, resolved *url.URL, owner *openAPI32RawResource) (*openAPI32RawResource, error) {
	resourceKey := artifactResourceKey(resolved)
	o.mu.RLock()
	resource := o.resources[resourceKey]
	resolve := o.resolve
	o.mu.RUnlock()
	if resource == nil && strings.HasPrefix(refText, "#") {
		resource = owner
	}
	if resource == nil && resolve != nil && !strings.HasPrefix(refText, "#") {
		request := cloneURL(resolved)
		request.Fragment = ""
		data, retrieval, err := resolve(request)
		if err == nil {
			if captureErr := o.capture(data, request, retrieval, false); captureErr != nil {
				return nil, captureErr
			}
			o.mu.RLock()
			resource = o.resources[resourceKey]
			o.mu.RUnlock()
		}
	}
	if resource == nil {
		return nil, fmt.Errorf("reference %q is unresolvable", refText)
	}
	if resource.selfError != "" {
		return nil, fmt.Errorf("reference %q reaches a resource with unusable %s", refText, resource.selfError)
	}
	if resource.self != nil && resourceKey != artifactResourceKey(resource.self) {
		return nil, fmt.Errorf("reference uses retrieval alias %q instead of declared $self identity %q", resourceKey, artifactResourceKey(resource.self))
	}
	return resource, nil
}

func openAPI32ResolvedReference(refText string, owner *openAPI32RawResource, base *url.URL) (*url.URL, error) {
	parsed, err := url.Parse(refText)
	if err != nil {
		return nil, fmt.Errorf("reference %q is not a URI-reference", refText)
	}
	if base == nil && owner != nil {
		base = owner.base
	}
	switch {
	case strings.HasPrefix(refText, "#"):
		resolved := cloneURL(base)
		if resolved == nil {
			resolved = &url.URL{}
		}
		resolved.Fragment = parsed.Fragment
		return resolved, nil
	case parsed.IsAbs():
		return parsed, nil
	case base != nil:
		return base.ResolveReference(parsed), nil
	default:
		return nil, fmt.Errorf("reference %q has no document base", refText)
	}
}

func (o *OpenAPI32Overlay) validateOpenAPI32ResponseSchema(raw any, owner *openAPI32RawResource, base *url.URL, seen map[string]bool) error {
	object, isObject := raw.(map[string]any)
	if !isObject { // boolean schemas are complete and carry no references
		return nil
	}
	if dialect, present := object["$schema"]; present {
		value, ok := dialect.(string)
		if !ok || value != "https://spec.openapis.org/oas/3.1/dialect/base" {
			return fmt.Errorf("Schema Object resource uses unsupported $schema dialect %q", value)
		}
	}
	if id, ok := object["$id"].(string); ok {
		parsed, err := url.Parse(id)
		if err != nil {
			return fmt.Errorf("Schema Object $id %q is not a URI-reference", id)
		}
		switch {
		case parsed.IsAbs():
			base = parsed
		case base != nil:
			base = base.ResolveReference(parsed)
		default:
			return fmt.Errorf("Schema Object $id %q has no document base", id)
		}
	}
	if refText, _ := object["$ref"].(string); refText != "" {
		resolved, err := openAPI32ResolvedReference(refText, owner, base)
		if err != nil {
			return err
		}
		key := resolved.String()
		if !seen[key] {
			seen[key] = true
			target, targetBase, resolveErr := o.openAPI32ResponseSchemaTarget(refText, resolved, owner)
			if resolveErr != nil {
				return resolveErr
			}
			if err := o.validateOpenAPI32ResponseSchema(target, owner, targetBase, seen); err != nil {
				return err
			}
		}
	}

	validateOne := func(value any) error {
		return o.validateOpenAPI32ResponseSchema(value, owner, base, seen)
	}
	for key := range rawSchemaMapKeywords {
		members, _ := object[key].(map[string]any)
		for _, child := range members {
			if err := validateOne(child); err != nil {
				return err
			}
		}
	}
	for key := range rawSchemaArrayKeywords {
		members, _ := object[key].([]any)
		for _, child := range members {
			if err := validateOne(child); err != nil {
				return err
			}
		}
	}
	for key := range rawSchemaSingleKeywords {
		if child, present := object[key]; present {
			if err := validateOne(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *OpenAPI32Overlay) openAPI32ResponseSchemaTarget(refText string, resolved *url.URL, owner *openAPI32RawResource) (any, *url.URL, error) {
	resourceKey := artifactResourceKey(resolved)
	o.mu.RLock()
	resource := o.resources[resourceKey]
	scope := o.schemaScopes[resourceKey]
	o.mu.RUnlock()
	if resource == nil && strings.HasPrefix(refText, "#") {
		resource = owner
	}
	if resource != nil {
		if resource.selfError != "" {
			return nil, nil, fmt.Errorf("Schema Object reference reaches a resource with unusable %s", resource.selfError)
		}
		if resource.self != nil && resourceKey != artifactResourceKey(resource.self) {
			return nil, nil, fmt.Errorf("Schema Object reference uses retrieval alias %q instead of declared $self identity %q", resourceKey, artifactResourceKey(resource.self))
		}
		if rawPointerCrossesSchemaResource(resource.root, resolved.Fragment) {
			return nil, nil, fmt.Errorf("Schema Object reference crosses a nearer $id resource boundary noncanonically")
		}
	}
	if scope != nil {
		if target, targetBase, ok := scope.fragment(resolved.Fragment); ok {
			return target, targetBase, nil
		}
	}
	if resource == nil {
		var err error
		resource, err = o.openAPI32ReferenceResource(refText, resolved, owner)
		if err != nil {
			return nil, nil, err
		}
		o.mu.RLock()
		scope = o.schemaScopes[resourceKey]
		o.mu.RUnlock()
		if scope != nil {
			if target, targetBase, ok := scope.fragment(resolved.Fragment); ok {
				return target, targetBase, nil
			}
		}
	}
	target, ok := rawFragmentTarget(resource.root, resolved.Fragment, rawSchemaTarget)
	if !ok {
		return nil, nil, fmt.Errorf("Schema Object reference %q names no target", refText)
	}
	return target, cloneURL(resource.base), nil
}
