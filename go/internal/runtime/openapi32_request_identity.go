package openapiclient

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Request Body owns its outer reference; each Media Type owns its own closure.
// Reuse the response-side raw Object/Schema materializers so confinement never
// requires rebasing an external body into the entry document or another resolver.
func (a *Artifact) materializeOpenAPI32RequestTarget(target *OperationTarget) (*OperationTarget, error) {
	if !target.Additional && target.Method == "trace" {
		return target, nil
	}
	o := a.openAPI32
	o.mu.RLock()
	operation, found := o.selectedRawOperationLocked(target.OperationReference, map[string]bool{})
	o.mu.RUnlock()
	if !found {
		return target, nil
	}
	object, _ := operation.value.(map[string]any)
	raw, present := object["requestBody"]
	if !present {
		return target, nil
	}
	node, err := o.resolveOpenAPI32ObjectNode(openAPI32RawNode{value: raw, resource: operation.resource}, rawRequestBodyTarget, "Request Body Object", map[string]bool{})
	if err != nil {
		return nil, err
	}
	bodyObject, _ := node.value.(map[string]any)
	metadata := make(map[string]any, len(bodyObject))
	for key, value := range bodyObject {
		if key != "content" {
			metadata[key] = value
		}
	}
	bytes, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	var body openapi3.RequestBody
	if err = json.Unmarshal(bytes, &body); err != nil {
		return nil, openAPI32ReferenceError("Request Body Object: %v", err)
	}
	content, ok := bodyObject["content"].(map[string]any)
	if !ok {
		return nil, openAPI32ReferenceError("Request Body content is not an object")
	}
	body.Content = make(openapi3.Content, len(content))
	for key, rawMedia := range content {
		mediaNode, resolveErr := o.resolveOpenAPI32ObjectNode(openAPI32RawNode{value: rawMedia, resource: node.resource}, rawRequestBodyTarget, "Media Type Object", map[string]bool{})
		if resolveErr != nil {
			continue
		}
		media, mediaErr := o.materializeOpenAPI32ResponseMedia(mediaNode)
		if mediaErr != nil {
			continue
		}
		parsed, parseErr := parseMediaDeclaration(key)
		if parseErr == nil && strings.HasPrefix(parsed.base, "multipart/") {
			declaration := resolveDeclaration(mediaSchema(media), false)
			item := declaration.items()
			if media.ItemSchema != nil {
				item = resolveDeclaration(media.ItemSchema.Value, false)
			}
			if err := o.hydrateOpenAPI32EncodingHeaders(mediaNode, 0, declaration, item); err != nil {
				continue
			}
		}
		body.Content[key] = media
	}
	if body.Required && len(body.Content) == 0 {
		return nil, operationResolutionError(OperationTargetExcluded, "required Request Body has no available media alternative")
	}
	operationCopy := *target.Operation
	operationCopy.RequestBody = &openapi3.RequestBodyRef{Value: &body}
	targetCopy := *target
	targetCopy.Operation = &operationCopy
	return &targetCopy, nil
}

// Only multipart Encoding headers have wire meaning. Walk the already admitted
// nesting depth, hydrating through existing fetch-capable resolvers before the
// request overlay reads its cache. Form headers and deeper ignored fields stay out.
func (o *OpenAPI32Overlay) hydrateOpenAPI32EncodingHeaders(node openAPI32RawNode, depth int, declaration, itemDeclaration resolvedDeclaration) error {
	object, _ := node.value.(map[string]any)
	type entry struct {
		raw         any
		declaration resolvedDeclaration
	}
	var entries []entry
	if encoding, ok := object["encoding"].(map[string]any); ok {
		for _, name := range declaration.propertyNames() {
			if value, present := encoding[name]; present {
				property := declaration.property(name)
				if property.declaresOnly("array") {
					property = property.items()
				}
				entries = append(entries, entry{value, property})
			}
		}
	}
	if prefix, ok := object["prefixEncoding"].([]any); ok {
		for _, value := range prefix {
			entries = append(entries, entry{value, itemDeclaration})
		}
	}
	if item, present := object["itemEncoding"]; present {
		entries = append(entries, entry{item, itemDeclaration})
	}
	for _, selected := range entries {
		encoding, _ := selected.raw.(map[string]any)
		headers, _ := encoding["headers"].(map[string]any)
		for name, rawHeader := range headers {
			if strings.EqualFold(name, "Content-Type") {
				continue
			}
			headerNode, err := o.resolveOpenAPI32ObjectNode(openAPI32RawNode{value: rawHeader, resource: node.resource}, rawHeaderTarget, "Header Object", map[string]bool{})
			if err != nil {
				return fmt.Errorf("Encoding header %q: %w", name, err)
			}
			if _, err = o.materializeOpenAPI32ResponseHeader(headerNode); err != nil {
				return fmt.Errorf("Encoding header %q: %w", name, err)
			}
		}
		contentType, _ := encoding["contentType"].(string)
		if depth < 1 && openAPI32EncodingMaySelectMultipart(contentType) {
			if err := o.hydrateOpenAPI32EncodingHeaders(openAPI32RawNode{value: encoding, resource: node.resource}, depth+1, selected.declaration, selected.declaration.items()); err != nil {
				return err
			}
		}
	}
	return nil
}
