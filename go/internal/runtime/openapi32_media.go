package openapiclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// openAPI32MediaOverlay is the artifact-local image of the Media Type Object
// fields that kin-openapi does not model losslessly. In particular, Encoding
// Object boolean presence is semantically significant in 3.2, and positional
// and nested encodings have no typed representation in kin-openapi v0.149.0.
// The overlay is selected through the operation target; it is never stored in
// a process-global map keyed by typed-model pointers.
type openAPI32MediaOverlay struct {
	itemSchemaPresent bool
	encodingPresent   bool
	encoding          map[string]*openAPI32EncodingOverlay
	prefixPresent     bool
	prefixEncoding    []*openAPI32EncodingOverlay
	itemPresent       bool
	itemEncoding      *openAPI32EncodingOverlay
	materialized      *openapi3.MediaType
	invalid           string
}

type openAPI32EncodingOverlay struct {
	contentType          string
	style                string
	stylePresent         bool
	explode              bool
	explodePresent       bool
	allowReserved        bool
	allowReservedPresent bool
	headers              map[string]openAPI32HeaderOverlay
	encodingPresent      bool
	encoding             map[string]*openAPI32EncodingOverlay
	prefixPresent        bool
	prefixEncoding       []*openAPI32EncodingOverlay
	itemPresent          bool
	itemEncoding         *openAPI32EncodingOverlay
	nestedDepthExceeded  bool
}

type openAPI32HeaderOverlay struct {
	fixed     string
	fixedOK   bool
	strings   map[string]bool
	unbounded bool
	required  bool
}

func (h openAPI32HeaderOverlay) admitsFold(value string) bool {
	if h.unbounded {
		return true
	}
	for candidate := range h.strings {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

type openAPI32RawNode struct {
	value    any
	resource *openAPI32RawResource
}

func openAPI32EffectiveMedia(media *openapi3.MediaType, overlay *openAPI32MediaOverlay) *openapi3.MediaType {
	if overlay != nil && overlay.materialized != nil {
		return overlay.materialized
	}
	return media
}

func (a *Artifact) openAPI32RequestMediaOverlays(target *OperationTarget) map[string]*openAPI32MediaOverlay {
	if a == nil || !a.Edition.IsOpenAPI32() || a.openAPI32 == nil || target == nil {
		return nil
	}
	o := a.openAPI32
	o.mu.RLock()
	defer o.mu.RUnlock()

	operation, ok := o.selectedRawOperationLocked(target.OperationReference, map[string]bool{})
	if !ok {
		return nil
	}
	operationObject, _ := operation.value.(map[string]any)
	requestBody, present := operationObject["requestBody"]
	if !present {
		return nil
	}
	body, ok := o.resolveRawObjectLocked(openAPI32RawNode{value: requestBody, resource: operation.resource}, map[string]bool{})
	if !ok {
		return nil
	}
	bodyObject, _ := body.value.(map[string]any)
	content, _ := bodyObject["content"].(map[string]any)
	if len(content) == 0 {
		return nil
	}

	result := make(map[string]*openAPI32MediaOverlay, len(content))
	for mediaType, raw := range content {
		rawObject, _ := raw.(map[string]any)
		_, mediaReference := rawObject["$ref"]
		mediaNode, resolved := o.resolveRawObjectLocked(openAPI32RawNode{value: raw, resource: body.resource}, map[string]bool{})
		if !resolved {
			continue
		}
		mediaObject, _ := mediaNode.value.(map[string]any)
		overlay := &openAPI32MediaOverlay{}
		if mediaReference {
			if encoded, err := json.Marshal(mediaObject); err == nil {
				var materialized openapi3.MediaType
				if json.Unmarshal(encoded, &materialized) == nil {
					materialized.Schema = o.materializeOpenAPI32SchemaRefLocked(mediaObject["schema"], mediaNode.resource, mediaNode.resource.base, map[string]bool{})
					materialized.ItemSchema = o.materializeOpenAPI32SchemaRefLocked(mediaObject["itemSchema"], mediaNode.resource, mediaNode.resource.base, map[string]bool{})
					overlay.materialized = &materialized
				}
			}
		}
		parsedMedia, _ := parseMediaDeclaration(mediaType)
		multipartMedia := strings.HasPrefix(parsedMedia.base, "multipart/")
		nameBasedMedia := multipartMedia || parsedMedia.base == "application/x-www-form-urlencoded"
		_, overlay.itemSchemaPresent = mediaObject["itemSchema"]
		if rawEncoding, exists := mediaObject["encoding"]; exists {
			overlay.encodingPresent = true
			if nameBasedMedia {
				overlay.encoding, overlay.invalid = o.parseOpenAPI32EncodingMapLocked(rawEncoding, mediaNode.resource, 0, multipartMedia)
			}
		}
		if rawPrefix, exists := mediaObject["prefixEncoding"]; exists {
			overlay.prefixPresent = true
			if multipartMedia && overlay.invalid == "" {
				overlay.prefixEncoding, overlay.invalid = o.parseOpenAPI32EncodingListLocked(rawPrefix, mediaNode.resource, 0, true)
			}
		}
		if rawItem, exists := mediaObject["itemEncoding"]; exists {
			overlay.itemPresent = true
			if multipartMedia && overlay.invalid == "" {
				overlay.itemEncoding, overlay.invalid = o.parseOpenAPI32EncodingLocked(rawItem, mediaNode.resource, 0, true)
			}
		}
		result[mediaType] = overlay
	}
	return result
}

func (o *OpenAPI32Overlay) materializeOpenAPI32SchemaRefLocked(raw any, owner *openAPI32RawResource, base *url.URL, seen map[string]bool) *openapi3.SchemaRef {
	if raw == nil {
		return nil
	}
	object, isObject := raw.(map[string]any)
	if !isObject {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		var ref openapi3.SchemaRef
		if json.Unmarshal(encoded, &ref) != nil {
			return nil
		}
		return &ref
	}
	if id, ok := object["$id"].(string); ok {
		if parsed, err := url.Parse(id); err == nil {
			switch {
			case parsed.IsAbs():
				base = parsed
			case base != nil:
				base = base.ResolveReference(parsed)
			}
		}
	}
	if refText, _ := object["$ref"].(string); refText != "" {
		resolved, resolvedBase, key, ok := o.resolveOpenAPI32SchemaNodeLocked(refText, owner, base)
		if !ok || seen[key] {
			return &openapi3.SchemaRef{Ref: refText}
		}
		seen[key] = true
		target := o.materializeOpenAPI32SchemaRefLocked(resolved, owner, resolvedBase, seen)
		delete(seen, key)
		if target == nil {
			return &openapi3.SchemaRef{Ref: refText}
		}
		target.Ref = refText
		if len(object) == 1 {
			return target
		}
		siblings := make(map[string]any, len(object)-1)
		for name, value := range object {
			if name != "$ref" {
				siblings[name] = value
			}
		}
		return &openapi3.SchemaRef{Value: &openapi3.Schema{AllOf: openapi3.SchemaRefs{
			target,
			o.materializeOpenAPI32SchemaRefLocked(siblings, owner, base, seen),
		}}}
	}

	encoded, err := json.Marshal(object)
	if err != nil {
		return nil
	}
	var schema openapi3.Schema
	if json.Unmarshal(encoded, &schema) != nil {
		return nil
	}
	materializeMap := func(name string, destination *openapi3.Schemas) {
		rawMap, _ := object[name].(map[string]any)
		if rawMap == nil {
			return
		}
		result := make(openapi3.Schemas, len(rawMap))
		for key, value := range rawMap {
			result[key] = o.materializeOpenAPI32SchemaRefLocked(value, owner, base, seen)
		}
		*destination = result
	}
	materializeList := func(name string, destination *openapi3.SchemaRefs) {
		rawList, _ := object[name].([]any)
		if rawList == nil {
			return
		}
		result := make(openapi3.SchemaRefs, len(rawList))
		for index, value := range rawList {
			result[index] = o.materializeOpenAPI32SchemaRefLocked(value, owner, base, seen)
		}
		*destination = result
	}
	materializeOne := func(name string, destination **openapi3.SchemaRef) {
		if value, present := object[name]; present {
			*destination = o.materializeOpenAPI32SchemaRefLocked(value, owner, base, seen)
		}
	}
	materializeMap("properties", &schema.Properties)
	materializeMap("patternProperties", &schema.PatternProperties)
	materializeMap("dependentSchemas", &schema.DependentSchemas)
	materializeMap("$defs", &schema.Defs)
	materializeList("allOf", &schema.AllOf)
	materializeList("anyOf", &schema.AnyOf)
	materializeList("oneOf", &schema.OneOf)
	materializeList("prefixItems", &schema.PrefixItems)
	materializeOne("items", &schema.Items)
	materializeOne("contains", &schema.Contains)
	materializeOne("not", &schema.Not)
	materializeOne("if", &schema.If)
	materializeOne("then", &schema.Then)
	materializeOne("else", &schema.Else)
	materializeOne("propertyNames", &schema.PropertyNames)
	materializeOne("contentSchema", &schema.ContentSchema)
	if value, present := object["additionalProperties"]; present {
		if _, isSchema := value.(map[string]any); isSchema {
			schema.AdditionalProperties.Schema = o.materializeOpenAPI32SchemaRefLocked(value, owner, base, seen)
		}
	}
	if value, present := object["unevaluatedItems"]; present {
		if _, isSchema := value.(map[string]any); isSchema {
			schema.UnevaluatedItems.Schema = o.materializeOpenAPI32SchemaRefLocked(value, owner, base, seen)
		}
	}
	if value, present := object["unevaluatedProperties"]; present {
		if _, isSchema := value.(map[string]any); isSchema {
			schema.UnevaluatedProperties.Schema = o.materializeOpenAPI32SchemaRefLocked(value, owner, base, seen)
		}
	}
	return &openapi3.SchemaRef{Value: &schema}
}

func (o *OpenAPI32Overlay) resolveOpenAPI32SchemaNodeLocked(refText string, owner *openAPI32RawResource, base *url.URL) (any, *url.URL, string, bool) {
	parsed, err := url.Parse(refText)
	if err != nil {
		return nil, nil, "", false
	}
	var resolved *url.URL
	switch {
	case strings.HasPrefix(refText, "#"):
		resolved = cloneURL(base)
		if resolved == nil && owner != nil {
			resolved = cloneURL(owner.base)
		}
		if resolved == nil {
			resolved = &url.URL{}
		}
		resolved.Fragment = parsed.Fragment
	case parsed.IsAbs():
		resolved = parsed
	case base != nil:
		resolved = base.ResolveReference(parsed)
	case owner != nil && owner.base != nil:
		resolved = owner.base.ResolveReference(parsed)
	default:
		return nil, nil, "", false
	}
	key := resolved.String()
	if scope := o.schemaScopes[artifactResourceKey(resolved)]; scope != nil {
		if value, targetBase, ok := scope.fragment(resolved.Fragment); ok {
			return value, targetBase, key, true
		}
	}
	resource := o.resources[artifactResourceKey(resolved)]
	if resource == nil && strings.HasPrefix(refText, "#") {
		resource = owner
	}
	if resource == nil {
		return nil, nil, key, false
	}
	target, ok := rawFragmentTarget(resource.root, resolved.Fragment, rawSchemaTarget)
	return target, cloneURL(resource.base), key, ok
}

func (o *OpenAPI32Overlay) selectedRawOperationLocked(reference OperationReference, seen map[string]bool) (openAPI32RawNode, bool) {
	if o == nil || o.entry == nil {
		return openAPI32RawNode{}, false
	}
	root, _ := o.entry.root.(map[string]any)
	paths, _ := root["paths"].(map[string]any)
	pathItem, _ := paths[reference.Path].(map[string]any)
	if pathItem == nil {
		return openAPI32RawNode{}, false
	}
	return o.operationFromRawPathItemLocked(openAPI32RawNode{value: pathItem, resource: o.entry}, reference, seen)
}

func (o *OpenAPI32Overlay) operationFromRawPathItemLocked(node openAPI32RawNode, reference OperationReference, seen map[string]bool) (openAPI32RawNode, bool) {
	pathItem, _ := node.value.(map[string]any)
	if pathItem == nil {
		return openAPI32RawNode{}, false
	}
	if reference.Additional {
		operations, _ := pathItem["additionalOperations"].(map[string]any)
		if operation, present := operations[reference.Method]; present {
			return openAPI32RawNode{value: operation, resource: node.resource}, true
		}
	} else if operation, present := pathItem[reference.Method]; present {
		return openAPI32RawNode{value: operation, resource: node.resource}, true
	}
	refText, _ := pathItem["$ref"].(string)
	if refText == "" {
		return openAPI32RawNode{}, false
	}
	referenced, ok := o.resolveRawReferenceLocked(refText, node.resource, seen)
	if !ok {
		return openAPI32RawNode{}, false
	}
	return o.operationFromRawPathItemLocked(referenced, reference, seen)
}

func (o *OpenAPI32Overlay) resolveRawObjectLocked(node openAPI32RawNode, seen map[string]bool) (openAPI32RawNode, bool) {
	object, _ := node.value.(map[string]any)
	if object == nil {
		return openAPI32RawNode{}, false
	}
	refText, _ := object["$ref"].(string)
	if refText == "" {
		return node, true
	}
	return o.resolveRawReferenceLocked(refText, node.resource, seen)
}

func (o *OpenAPI32Overlay) resolveRawReferenceLocked(refText string, owner *openAPI32RawResource, seen map[string]bool) (openAPI32RawNode, bool) {
	parsed, err := url.Parse(refText)
	if err != nil {
		return openAPI32RawNode{}, false
	}
	var resolved *url.URL
	switch {
	case strings.HasPrefix(refText, "#"):
		if owner != nil && owner.base != nil {
			resolved = cloneURL(owner.base)
		} else {
			resolved = &url.URL{}
		}
		resolved.Fragment = parsed.Fragment
	case parsed.IsAbs():
		resolved = parsed
	case owner != nil && owner.base != nil:
		resolved = owner.base.ResolveReference(parsed)
	default:
		return openAPI32RawNode{}, false
	}
	key := resolved.String()
	if seen[key] {
		return openAPI32RawNode{}, false
	}
	seen[key] = true
	resource := o.resources[artifactResourceKey(resolved)]
	if resource == nil && strings.HasPrefix(refText, "#") {
		resource = owner
	}
	if resource == nil {
		return openAPI32RawNode{}, false
	}
	target, ok := rawFragmentTarget(resource.root, resolved.Fragment, rawRequestBodyTarget)
	if !ok {
		return openAPI32RawNode{}, false
	}
	return openAPI32RawNode{value: target, resource: resource}, true
}

func (o *OpenAPI32Overlay) parseOpenAPI32EncodingMapLocked(raw any, resource *openAPI32RawResource, depth int, allowNested bool) (map[string]*openAPI32EncodingOverlay, string) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, "encoding must be an object"
	}
	result := make(map[string]*openAPI32EncodingOverlay, len(object))
	for name, value := range object {
		encoding, err := o.parseOpenAPI32EncodingLocked(value, resource, depth, allowNested)
		if err != "" {
			return nil, fmt.Sprintf("encoding %q: %s", name, err)
		}
		result[name] = encoding
	}
	return result, ""
}

func (o *OpenAPI32Overlay) parseOpenAPI32EncodingListLocked(raw any, resource *openAPI32RawResource, depth int, allowNested bool) ([]*openAPI32EncodingOverlay, string) {
	list, ok := raw.([]any)
	if !ok {
		return nil, "prefixEncoding must be an array"
	}
	result := make([]*openAPI32EncodingOverlay, len(list))
	for index, value := range list {
		encoding, err := o.parseOpenAPI32EncodingLocked(value, resource, depth, allowNested)
		if err != "" {
			return nil, fmt.Sprintf("prefixEncoding[%d]: %s", index, err)
		}
		result[index] = encoding
	}
	return result, ""
}

func (o *OpenAPI32Overlay) parseOpenAPI32EncodingLocked(raw any, resource *openAPI32RawResource, depth int, allowNested bool) (*openAPI32EncodingOverlay, string) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, "Encoding Object must be an object"
	}
	result := &openAPI32EncodingOverlay{}
	if value, present := object["contentType"]; present {
		var valid bool
		result.contentType, valid = value.(string)
		if !valid {
			return nil, "contentType must be a string"
		}
		if result.contentType == "" {
			return nil, "contentType must not be empty when present"
		}
	}
	if value, present := object["style"]; present {
		result.stylePresent = true
		var valid bool
		result.style, valid = value.(string)
		if !valid {
			return nil, "style must be a string"
		}
		if result.style == "" {
			return nil, "style must not be empty when present"
		}
	}
	if value, present := object["explode"]; present {
		result.explodePresent = true
		var valid bool
		result.explode, valid = value.(bool)
		if !valid {
			return nil, "explode must be a boolean"
		}
	}
	if value, present := object["allowReserved"]; present {
		result.allowReservedPresent = true
		var valid bool
		result.allowReserved, valid = value.(bool)
		if !valid {
			return nil, "allowReserved must be a boolean"
		}
	}
	if headers, present := object["headers"]; present {
		headerMap, valid := headers.(map[string]any)
		if !valid {
			return nil, "headers must be an object"
		}
		result.headers = make(map[string]openAPI32HeaderOverlay, len(headerMap))
		for name, header := range headerMap {
			result.headers[name] = o.parseOpenAPI32HeaderLocked(header, resource)
		}
	}

	nestedAllowed := allowNested && openAPI32EncodingMaySelectMultipart(result.contentType)
	if value, present := object["encoding"]; present && nestedAllowed {
		result.encodingPresent = true
		if depth >= 1 {
			result.nestedDepthExceeded = true
		} else {
			var err string
			result.encoding, err = o.parseOpenAPI32EncodingMapLocked(value, resource, depth+1, true)
			if err != "" {
				return nil, err
			}
		}
	}
	if value, present := object["prefixEncoding"]; present && nestedAllowed {
		result.prefixPresent = true
		if depth >= 1 {
			result.nestedDepthExceeded = true
		} else {
			var err string
			result.prefixEncoding, err = o.parseOpenAPI32EncodingListLocked(value, resource, depth+1, true)
			if err != "" {
				return nil, err
			}
		}
	}
	if value, present := object["itemEncoding"]; present && nestedAllowed {
		result.itemPresent = true
		if depth >= 1 {
			result.nestedDepthExceeded = true
		} else {
			var err string
			result.itemEncoding, err = o.parseOpenAPI32EncodingLocked(value, resource, depth+1, true)
			if err != "" {
				return nil, err
			}
		}
	}
	if result.encodingPresent && (result.prefixPresent || result.itemPresent) {
		return nil, "name-based encoding is mutually exclusive with prefixEncoding and itemEncoding"
	}
	return result, ""
}

func openAPI32EncodingMaySelectMultipart(contentType string) bool {
	if contentType == "" {
		return false
	}
	members, err := splitHTTPList(contentType)
	if err != nil {
		return true // the enclosing contentType check will report the malformed declaration
	}
	for _, member := range members {
		parsed, err := parseMediaDeclaration(member)
		if err != nil {
			return true
		}
		if parsed.base == "*/*" || strings.HasPrefix(parsed.base, "multipart/") {
			return true
		}
	}
	return false
}

func (o *OpenAPI32Overlay) parseOpenAPI32HeaderLocked(raw any, resource *openAPI32RawResource) openAPI32HeaderOverlay {
	node, ok := o.resolveRawObjectLocked(openAPI32RawNode{value: raw, resource: resource}, map[string]bool{})
	if !ok {
		return openAPI32HeaderOverlay{}
	}
	header, _ := node.value.(map[string]any)
	schema := header["schema"]
	if schema == nil {
		if content, ok := header["content"].(map[string]any); ok && len(content) == 1 {
			for _, media := range content {
				if mediaObject, ok := media.(map[string]any); ok {
					schema = mediaObject["schema"]
				}
			}
		}
	}
	values, bounded := o.openAPI32FixedStringValuesLocked(schema, node.resource, map[string]bool{})
	required, _ := header["required"].(bool)
	result := openAPI32HeaderOverlay{strings: values, unbounded: !bounded, required: required}
	if len(values) == 1 {
		for value := range values {
			result.fixed, result.fixedOK = value, true
		}
	}
	return result
}

func (o *OpenAPI32Overlay) openAPI32FixedStringValuesLocked(raw any, resource *openAPI32RawResource, seen map[string]bool) (map[string]bool, bool) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	if refText, _ := object["$ref"].(string); refText != "" {
		if referenced, resolved := o.resolveRawReferenceLocked(refText, resource, seen); resolved {
			return o.openAPI32FixedStringValuesLocked(referenced.value, referenced.resource, seen)
		}
	}
	if value, present := object["const"]; present {
		text, stringValue := value.(string)
		if !stringValue {
			return map[string]bool{}, true
		}
		return map[string]bool{text: true}, true
	}
	if rawEnum, present := object["enum"]; present {
		members, valid := rawEnum.([]any)
		if !valid {
			return map[string]bool{}, true
		}
		values := map[string]bool{}
		for _, member := range members {
			if text, stringValue := member.(string); stringValue {
				values[text] = true
			}
		}
		return values, true
	}
	return nil, false
}

func (e *openAPI32EncodingOverlay) usesSerialization() bool {
	return e != nil && (e.stylePresent || e.explodePresent || e.allowReservedPresent)
}

func openAPI32SequentialRequestKind(base string, media *openapi3.MediaType, overlay *openAPI32MediaOverlay) (string, bool, error) {
	itemSchema := media != nil && media.ItemSchema != nil || overlay != nil && overlay.itemSchemaPresent
	positional := openAPI32PositionalMultipart(media, overlay)
	sequentialIdentity := ""
	switch {
	case base == "application/jsonl", base == "application/x-ndjson":
		sequentialIdentity = "json-lines"
	case base == "application/json-seq", strings.HasSuffix(base, "+json-seq"):
		sequentialIdentity = "json-seq"
	case base == "text/event-stream":
		sequentialIdentity = "sse"
	case strings.HasPrefix(base, "multipart/") && positional:
		sequentialIdentity = "multipart"
	}
	if sequentialIdentity == "sse" {
		return "", true, fmt.Errorf("text/event-stream has no incorporated request write algorithm")
	}
	if sequentialIdentity != "" {
		return sequentialIdentity, true, nil
	}
	if itemSchema {
		return "", true, fmt.Errorf("media type %q declares itemSchema but has no incorporated sequential request framing", base)
	}
	return "", false, nil
}

func openAPI32PositionalMultipart(media *openapi3.MediaType, overlay *openAPI32MediaOverlay) bool {
	if overlay != nil && (overlay.prefixPresent || overlay.itemPresent) {
		return true
	}
	if media != nil && media.ItemSchema != nil {
		return true
	}
	return media != nil && schemaTypeIs(mediaSchema(media), "array", map[*openapi3.Schema]bool{})
}

func openAPI32NonJSONTextSchema(schema *openapi3.Schema) bool {
	types, constrained, ambiguous := openAPI32ResolvedTypes(schema, map[*openapi3.Schema]bool{})
	if ambiguous || !constrained {
		return false
	}
	nonNull := false
	for member := range types {
		switch member {
		case "null":
		case "string", "boolean", "number", "integer":
			nonNull = true
		default:
			return false
		}
	}
	return nonNull
}

func openAPI32ResolvedTypes(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) (map[string]bool, bool, bool) {
	if schema == nil || seen[schema] {
		return nil, false, false
	}
	seen[schema] = true
	defer delete(seen, schema)
	var result map[string]bool
	constrained := false
	intersect := func(candidate map[string]bool, present bool) {
		if !present {
			return
		}
		if !constrained {
			result = candidate
			constrained = true
			return
		}
		for member := range result {
			if !candidate[member] {
				delete(result, member)
			}
		}
	}
	if schema.Type != nil {
		candidate := map[string]bool{}
		for _, member := range schema.Type.Slice() {
			candidate[member] = true
		}
		intersect(candidate, true)
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		candidate, present, ambiguous := openAPI32ResolvedTypes(member.Value, seen)
		if ambiguous {
			return nil, false, true
		}
		intersect(candidate, present)
	}
	for _, choice := range []openapi3.SchemaRefs{schema.AnyOf, schema.OneOf} {
		if len(choice) == 0 {
			continue
		}
		union := map[string]bool{}
		for _, branch := range choice {
			if branch == nil || branch.Value == nil {
				return nil, false, true
			}
			candidate, present, ambiguous := openAPI32ResolvedTypes(branch.Value, seen)
			if ambiguous || !present {
				return nil, false, true
			}
			for member := range candidate {
				union[member] = true
			}
		}
		intersect(union, true)
	}
	return result, constrained, false
}

func openAPI32EncodingAsTyped(raw *openAPI32EncodingOverlay) *openapi3.Encoding {
	if raw == nil {
		return nil
	}
	encoding := &openapi3.Encoding{ContentType: raw.contentType}
	if raw.usesSerialization() {
		encoding.Style = raw.style
		if encoding.Style == "" {
			encoding.Style = openapi3.SerializationForm
		}
		if raw.explodePresent {
			value := raw.explode
			encoding.Explode = &value
		}
		encoding.AllowReserved = raw.allowReserved
	}
	return encoding
}

func openAPI32MediaWithEncoding(media *openapi3.MediaType, overlay *openAPI32MediaOverlay, controls bool) *openapi3.MediaType {
	if media == nil || overlay == nil || !overlay.encodingPresent {
		return media
	}
	copy := *media
	copy.Encoding = make(openapi3.Encodings, len(overlay.encoding))
	for name, raw := range overlay.encoding {
		encoding := openAPI32EncodingAsTyped(raw)
		if !controls && encoding != nil {
			encoding.Style = ""
			encoding.Explode = nil
			encoding.AllowReserved = false
		}
		copy.Encoding[name] = encoding
	}
	return &copy
}

func validateOpenAPI32URLEncodedMedia(doc *openapi3.T, media *openapi3.MediaType, overlay *openAPI32MediaOverlay) error {
	schema := mediaSchema(media)
	if schema == nil {
		return fmt.Errorf("schema-omitted urlencoded media has no application-value caller route")
	}
	_, props, err := resolvedBodyShape(schema, map[*openapi3.Schema]bool{})
	if err != nil {
		return err
	}
	for name := range props {
		propertySchema := resolvedMultipartProperty(schema, name, map[*openapi3.Schema]bool{})
		if resolveDeclaration(propertySchema, false).admitsNoInstance() {
			continue // an unsatisfiable property is unreachable and destroys nothing (§3.2)
		}
		propertySchema, _ = effectiveRevision3PartSchema(propertySchema, false)
		encoding := openAPI32NamedEncoding(media, overlay, name)
		if encoding != nil && encoding.usesSerialization() {
			if err := validateMultipartSerializationMethod(name, propertySchema, openAPI32EncodingAsTyped(encoding), false); err != nil {
				return err
			}
			continue
		}
		if encodingRequiresPropertyMedia(openAPI32EncodingAsTyped(encoding)) {
			continue
		}
		contentType, err := revision3PartContentType(propertySchema, openAPI32EncodingAsTyped(encoding), false)
		if err != nil {
			return fmt.Errorf("urlencoded property %q: %w", name, err)
		}
		if _, err := revision3PropertyCarriage(propertySchema, contentType, false, true); err != nil {
			return fmt.Errorf("urlencoded property %q: %w", name, err)
		}
	}
	return nil
}

func validateOpenAPI32MultipartMedia(doc *openapi3.T, media *openapi3.MediaType, overlay *openAPI32MediaOverlay, formData ...bool) error {
	if overlay != nil {
		if overlay.encodingPresent && (overlay.prefixPresent || overlay.itemPresent) {
			return fmt.Errorf("name-based encoding is mutually exclusive with prefixEncoding and itemEncoding")
		}
		if (overlay.prefixPresent || overlay.itemPresent) && media != nil && media.ItemSchema == nil &&
			!schemaTypeIs(mediaSchema(media), "array", map[*openapi3.Schema]bool{}) {
			return fmt.Errorf("positional multipart encoding requires itemSchema or an array schema")
		}
	}
	if openAPI32PositionalMultipart(media, overlay) {
		if media == nil || media.ItemSchema == nil && !schemaTypeIs(mediaSchema(media), "array", map[*openapi3.Schema]bool{}) {
			return fmt.Errorf("positional multipart requires itemSchema or an array schema")
		}
		itemSchema := media.ItemSchema
		if itemSchema == nil {
			itemSchema = &openapi3.SchemaRef{Value: resolvedMultipartItems(mediaSchema(media), map[*openapi3.Schema]bool{})}
		}
		if overlay != nil {
			for index, encoding := range overlay.prefixEncoding {
				if err := validateOpenAPI32NestedEncoding(itemSchema.Value, encoding); err != nil {
					return fmt.Errorf("prefixEncoding[%d]: %w", index, err)
				}
			}
			if err := validateOpenAPI32NestedEncoding(itemSchema.Value, overlay.itemEncoding); err != nil {
				return fmt.Errorf("itemEncoding: %w", err)
			}
		}
		return nil
	}
	controls := len(formData) == 0 || formData[0]
	validationMedia := openAPI32MediaWithEncoding(media, overlay, controls)
	if validationMedia != nil && overlay != nil {
		copy := *validationMedia
		copy.Encoding = make(openapi3.Encodings, len(validationMedia.Encoding))
		for name, encoding := range validationMedia.Encoding {
			if openAPI32EncodingHasNested(overlay.encoding[name]) {
				propertySchema := resolvedMultipartPropertyFor(mediaSchema(media), name, map[*openapi3.Schema]bool{}, true, false)
				if err := validateOpenAPI32NestedEncoding(propertySchema, overlay.encoding[name]); err != nil {
					return fmt.Errorf("encoding %q: %w", name, err)
				}
				copy.Encoding[name] = nil // the nested writer validates this field's active carriage
			} else {
				copy.Encoding[name] = encoding
			}
		}
		validationMedia = &copy
	}
	return validateRevision3MultipartMedia(doc, validationMedia)
}

func validateOpenAPI32NestedEncoding(schema *openapi3.Schema, encoding *openAPI32EncodingOverlay) error {
	if !openAPI32EncodingHasNested(encoding) {
		return nil
	}
	if openAPI32EncodingSelectsConcreteMultipart(encoding.contentType) && openAPI32EncodingExceedsSupportedDepth(encoding) {
		return fmt.Errorf("more than one nested Encoding level is not supported")
	}
	if encoding.encodingPresent {
		if !schemaTypeIs(schema, "object", map[*openapi3.Schema]bool{}) {
			return fmt.Errorf("nested name-based encoding requires an object property schema")
		}
		return nil
	}
	if !schemaTypeIs(schema, "array", map[*openapi3.Schema]bool{}) {
		return fmt.Errorf("nested positional encoding requires an array property schema")
	}
	return nil
}

func openAPI32EncodingSelectsConcreteMultipart(contentType string) bool {
	members, err := splitHTTPList(contentType)
	if err != nil || len(members) != 1 || isMediaRange(members[0]) {
		return false
	}
	parsed, err := parseMediaDeclaration(members[0])
	return err == nil && strings.HasPrefix(parsed.base, "multipart/")
}

func openAPI32EncodingExceedsSupportedDepth(encoding *openAPI32EncodingOverlay) bool {
	if encoding == nil {
		return false
	}
	if encoding.nestedDepthExceeded {
		return true
	}
	for _, child := range encoding.encoding {
		if openAPI32EncodingExceedsSupportedDepth(child) {
			return true
		}
	}
	for _, child := range encoding.prefixEncoding {
		if openAPI32EncodingExceedsSupportedDepth(child) {
			return true
		}
	}
	return openAPI32EncodingExceedsSupportedDepth(encoding.itemEncoding)
}

func buildOpenAPI32SequentialBody(kind string, value any) ([]byte, error) {
	items, ok := asArray(value)
	if !ok {
		return nil, fmt.Errorf("sequential request body must be one JSON array, got %T", value)
	}
	var body bytes.Buffer
	for index, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("sequential item %d: %w", index, err)
		}
		switch kind {
		case "json-lines":
			body.Write(encoded)
			body.WriteByte('\n')
		case "json-seq":
			body.WriteByte(0x1e)
			body.Write(encoded)
			body.WriteByte('\n')
		default:
			return nil, fmt.Errorf("unsupported sequential request framing %q", kind)
		}
	}
	return body.Bytes(), nil
}

func buildOpenAPI32URLEncodedBody(plan *bodyPlan, fields map[string]any) (string, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	root := mediaSchema(plan.media)
	rootDeclaration := resolveDeclaration(root, false)
	var units []string
	for _, name := range names {
		value := fields[name]
		schema := resolvedMultipartPropertyFor(root, name, map[*openapi3.Schema]bool{}, true, false)
		schema, nullable := effectiveRevision3PartSchema(schema, false)
		encoding := openAPI32NamedEncoding(plan.media, plan.openAPI32, name)
		if encoding != nil && encoding.usesSerialization() {
			typed := openAPI32EncodingAsTyped(encoding)
			method := revision3EncodingSerializationMethod(typed)
			serialized, err := serializeQueryValueForRevision(name, value, method.Style, method.Explode, encoding.allowReserved, plan.bindingSpec, true, false)
			if err != nil {
				return "", fmt.Errorf("form field %q: %w", name, err)
			}
			units = append(units, serialized...)
			continue
		}
		if nullable && value == nil {
			if rootDeclaration.requiresProperty(name) {
				return "", fmt.Errorf("form field %q is required and cannot be elided for null content-based encoding", name)
			}
			continue
		}
		contentType, mode, err := openAPI32PartCarriage(schema, encoding, value)
		if err != nil {
			return "", fmt.Errorf("form field %q: %w", name, err)
		}
		var body []byte
		switch mode {
		case revision3PropertyRawOctets:
			text, ok := value.(string)
			if !ok {
				return "", fmt.Errorf("form field %q raw value must be a canonical Base64 string, got %T", name, value)
			}
			body, err = canonicalBase64BoundaryBytes(name, text)
		case revision3PropertyJSON:
			body, err = json.Marshal(value)
		case revision3PropertyArtifactEncoded:
			text, ok := value.(string)
			if !ok {
				return "", fmt.Errorf("form field %q artifact-encoded value must be a string, got %T", name, value)
			}
			body, err = encodeTextString(text, contentType)
		case revision3PropertyText:
			var text string
			text, err = serializeOpenAPI32NonJSONText(schema, value)
			if err == nil {
				body, err = encodeTextString(text, contentType)
			}
		}
		if err != nil {
			return "", fmt.Errorf("form field %q: %w", name, err)
		}
		units = append(units, formURLEncodedEscape(name)+"="+formURLEncodedEscape(string(body)))
	}
	return strings.Join(units, "&"), nil
}

func serializeOpenAPI32NonJSONText(schema *openapi3.Schema, value any) (string, error) {
	if value == nil {
		return "", fmt.Errorf("non-JSON text serialization has no null lexical form")
	}
	kind := openAPI32JSONValueType(value)
	if kind == "" || !openAPI32SchemaAdmitsRuntimeType(schema, kind) {
		return "", fmt.Errorf("supplied %T does not determine one permitted non-JSON serialization type", value)
	}
	switch kind {
	case "string":
		return value.(string), nil
	case "boolean":
		return strconv.FormatBool(value.(bool)), nil
	case "integer", "number":
		return shortestOpenAPI32JSONNumber(value)
	default:
		return "", fmt.Errorf("JSON %s has no non-JSON text serialization", kind)
	}
}

func openAPI32JSONValueType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float32, float64:
		return "number"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "integer"
	case []any, []string:
		return "array"
	case map[string]any:
		return "object"
	}
	return ""
}

func openAPI32SchemaAdmitsRuntimeType(schema *openapi3.Schema, kind string) bool {
	types, constrained, ambiguous := openAPI32ResolvedTypes(schema, map[*openapi3.Schema]bool{})
	if ambiguous || !constrained {
		return false
	}
	if types[kind] {
		return true
	}
	return (kind == "integer" || kind == "number") && (types["number"] || types["integer"])
}

func shortestOpenAPI32JSONNumber(value any) (string, error) {
	raw, err := primitiveString(value)
	if err != nil {
		return "", err
	}
	return normalizeOpenAPI32JSONNumber(raw)
}

// normalizeOpenAPI32JSONNumber preserves the exact finite decimal denoted by
// an RFC 8259 spelling and chooses the shortest equivalent spelling. A tie
// prefers non-exponent form, and the exponent form uses lowercase e without
// a plus sign or leading zeroes.
func normalizeOpenAPI32JSONNumber(raw string) (string, error) {
	if !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("%q is not an RFC 8259 number", raw)
	}
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = raw[1:]
	}
	mantissa, exponentText, hasExponent := raw, "", false
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		mantissa, exponentText, hasExponent = raw[:index], raw[index+1:], true
	}
	exponent := new(big.Int)
	if hasExponent {
		if _, ok := exponent.SetString(exponentText, 10); !ok {
			return "", fmt.Errorf("number exponent %q is unsupported", exponentText)
		}
	}
	integer, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(integer+fraction, "0")
	if digits == "" {
		return "0", nil
	}
	scale := new(big.Int).Sub(big.NewInt(int64(len(fraction))), exponent)
	trailing := int64(0)
	for strings.HasSuffix(digits, "0") {
		digits = strings.TrimSuffix(digits, "0")
		trailing++
	}
	scale.Sub(scale, big.NewInt(trailing))
	prefix := ""
	if negative {
		prefix = "-"
	}
	exponentialExponent := new(big.Int).Sub(big.NewInt(int64(len(digits)-1)), scale)
	mantissa = digits[:1]
	if len(digits) > 1 {
		mantissa += "." + digits[1:]
	}
	exponential := prefix + mantissa + "e" + exponentialExponent.String()

	prefixLength := int64(len(prefix))
	plainLength := new(big.Int)
	switch {
	case scale.Sign() <= 0:
		plainLength.Add(big.NewInt(prefixLength+int64(len(digits))), new(big.Int).Neg(new(big.Int).Set(scale)))
	case scale.Cmp(big.NewInt(int64(len(digits)))) >= 0:
		plainLength.Add(big.NewInt(prefixLength+2), scale)
	default:
		plainLength.SetInt64(prefixLength + int64(len(digits)) + 1)
	}
	if plainLength.Cmp(big.NewInt(int64(len(exponential)))) > 0 {
		return exponential, nil
	}
	scaleValue := int(scale.Int64()) // the length comparison proves this is small enough to materialize
	var plain string
	switch {
	case scaleValue <= 0:
		plain = digits + strings.Repeat("0", -scaleValue)
	case scaleValue >= len(digits):
		plain = "0." + strings.Repeat("0", scaleValue-len(digits)) + digits
	default:
		plain = digits[:len(digits)-scaleValue] + "." + digits[len(digits)-scaleValue:]
	}
	plain = prefix + plain
	return plain, nil
}

func buildOpenAPI32MultipartBody(doc *openapi3.T, plan *bodyPlan, routed *routedInput) (io.Reader, string, error) {
	if plan == nil || plan.media == nil {
		return nil, "", fmt.Errorf("OpenAPI 3.2 multipart plan has no Media Type Object")
	}
	selected, err := parseRevision3MediaType(plan.mediaType)
	if err != nil {
		return nil, "", err
	}
	if !strings.HasPrefix(selected.base, "multipart/") {
		return nil, "", fmt.Errorf("multipart encoder received concrete media type %q", plan.mediaType)
	}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if boundary, present := selected.renderedParams["boundary"]; present {
		if err := writer.SetBoundary(boundary); err != nil {
			return nil, "", fmt.Errorf("invalid multipart boundary: %w", err)
		}
	}
	formData := selected.base == "multipart/form-data"
	if openAPI32PositionalMultipart(plan.media, plan.openAPI32) {
		if !routed.bodySet {
			if !plan.required {
				return nil, "", nil
			}
			return nil, "", fmt.Errorf("positional multipart request requires one array body")
		}
		items, ok := asArray(routed.bodyValue)
		if !ok {
			return nil, "", fmt.Errorf("positional multipart request body must be an array, got %T", routed.bodyValue)
		}
		itemSchema := plan.itemSchema
		if itemSchema == nil {
			itemSchema = resolvedMultipartItems(mediaSchema(plan.media), map[*openapi3.Schema]bool{})
		}
		for index, value := range items {
			encoding := openAPI32PositionalEncoding(plan.openAPI32, index)
			name, err := openAPI32PositionalPartName(encoding, formData)
			if err != nil {
				return nil, "", fmt.Errorf("multipart item %d: %w", index, err)
			}
			if err := writeOpenAPI32MultipartPart(writer, name, value, itemSchema, encoding, formData, 0); err != nil {
				return nil, "", fmt.Errorf("multipart item %d: %w", index, err)
			}
		}
	} else {
		fields, err := objectBodyFields(plan, routed)
		if err != nil {
			return nil, "", err
		}
		if len(fields) == 0 && !plan.required {
			return nil, "", nil
		}
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		sort.Strings(names)
		root := mediaSchema(plan.media)
		rootDeclaration := resolveDeclaration(root, false)
		for _, name := range names {
			if !multipartPropertyNameSafe(name) {
				return nil, "", fmt.Errorf("multipart property name %q contains CR or LF", name)
			}
			value := fields[name]
			schema := resolvedMultipartPropertyFor(root, name, map[*openapi3.Schema]bool{}, true, false)
			schema, nullable := effectiveRevision3PartSchema(schema, false)
			if nullable && value == nil {
				if rootDeclaration.requiresProperty(name) {
					return nil, "", fmt.Errorf("multipart part %q is required and cannot be elided for null content-based encoding", name)
				}
				continue
			}
			encoding := openAPI32NamedEncoding(plan.media, plan.openAPI32, name)
			if formData && encoding != nil && encoding.usesSerialization() {
				units, err := serializeMultipartValue(name, value, openAPI32EncodingAsTyped(encoding))
				if err != nil {
					return nil, "", fmt.Errorf("multipart part %q: %w", name, err)
				}
				for _, unit := range units {
					if err := writeOpenAPI32SerializedPart(writer, unit, encoding); err != nil {
						return nil, "", err
					}
				}
				continue
			}
			if schemaTypeIs(schema, "array", map[*openapi3.Schema]bool{}) && !openAPI32EncodingHasNested(encoding) {
				items, ok := asArray(value)
				if !ok {
					return nil, "", fmt.Errorf("multipart part %q is declared as an array but the supplied value is %T", name, value)
				}
				itemSchema := resolvedMultipartItems(schema, map[*openapi3.Schema]bool{})
				for _, item := range items {
					if err := writeOpenAPI32MultipartPart(writer, name, item, itemSchema, encoding, formData, 0); err != nil {
						return nil, "", err
					}
				}
				continue
			}
			if err := writeOpenAPI32MultipartPart(writer, name, value, schema, encoding, formData, 0); err != nil {
				return nil, "", err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	params := make(map[string]string, len(selected.renderedParams)+1)
	for key, value := range selected.renderedParams {
		params[key] = value
	}
	if _, present := params["boundary"]; !present {
		params["boundary"] = writer.Boundary()
	}
	return &buf, formatHTTPMediaType(selected.base, params), nil
}

func openAPI32NamedEncoding(media *openapi3.MediaType, overlay *openAPI32MediaOverlay, name string) *openAPI32EncodingOverlay {
	if overlay != nil && overlay.encodingPresent {
		return overlay.encoding[name]
	}
	if media == nil || media.Encoding[name] == nil {
		return nil
	}
	typed := media.Encoding[name]
	result := &openAPI32EncodingOverlay{contentType: typed.ContentType, style: typed.Style, stylePresent: typed.Style != "", allowReserved: typed.AllowReserved, allowReservedPresent: typed.AllowReserved}
	if typed.Explode != nil {
		result.explode, result.explodePresent = *typed.Explode, true
	}
	return result
}

func openAPI32PositionalEncoding(overlay *openAPI32MediaOverlay, index int) *openAPI32EncodingOverlay {
	if overlay == nil {
		return nil
	}
	if index < len(overlay.prefixEncoding) {
		return overlay.prefixEncoding[index]
	}
	return overlay.itemEncoding
}

func openAPI32PositionalPartName(encoding *openAPI32EncodingOverlay, formData bool) (string, error) {
	if !formData {
		return "", nil
	}
	header, ok := openAPI32Header(encoding, "Content-Disposition")
	if !ok || !header.fixedOK {
		return "", fmt.Errorf("positional multipart/form-data requires an artifact-fixed Content-Disposition header")
	}
	disposition, params, err := mime.ParseMediaType(header.fixed)
	if err != nil || !strings.EqualFold(disposition, "form-data") || params["name"] == "" {
		return "", fmt.Errorf("fixed Content-Disposition %q does not supply a form-data part name", header.fixed)
	}
	return params["name"], nil
}

func openAPI32Header(encoding *openAPI32EncodingOverlay, name string) (openAPI32HeaderOverlay, bool) {
	if encoding == nil {
		return openAPI32HeaderOverlay{}, false
	}
	for candidate, header := range encoding.headers {
		if strings.EqualFold(candidate, name) {
			return header, true
		}
	}
	return openAPI32HeaderOverlay{}, false
}

func writeOpenAPI32SerializedPart(writer *multipart.Writer, unit multipartSerializedUnit, encoding *openAPI32EncodingOverlay) error {
	headers, err := openAPI32PartHeaders(unit.name, encoding, false)
	if err != nil {
		return err
	}
	part, err := writer.CreatePart(headers)
	if err != nil {
		return err
	}
	_, err = io.WriteString(part, unit.value)
	return err
}

func writeOpenAPI32MultipartPart(writer *multipart.Writer, name string, value any, schema *openapi3.Schema, encoding *openAPI32EncodingOverlay, formData bool, depth int) error {
	contentType, mode, err := openAPI32PartCarriage(schema, encoding, value)
	if err != nil {
		return err
	}
	var body []byte
	if openAPI32EncodingHasNested(encoding) && strings.HasPrefix(contentType.base, "multipart/") {
		if depth >= 1 {
			return fmt.Errorf("more than one nested Encoding level is not supported")
		}
		body, contentType, err = encodeOpenAPI32NestedMultipart(value, schema, encoding, contentType, depth+1)
	} else {
		switch mode {
		case revision3PropertyRawOctets:
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("raw multipart part requires a canonical Base64 string, got %T", value)
			}
			body, err = canonicalBase64BoundaryBytes(name, text)
		case revision3PropertyJSON:
			body, err = json.Marshal(value)
		case revision3PropertyArtifactEncoded:
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("artifact-encoded multipart part requires a string, got %T", value)
			}
			body, err = encodeTextString(text, contentType)
		case revision3PropertyText:
			var text string
			text, err = serializeOpenAPI32NonJSONText(schema, value)
			if err == nil {
				body, err = encodeTextString(text, contentType)
			}
		}
	}
	if err != nil {
		return err
	}
	headers, err := openAPI32PartHeaders(name, encoding, formData)
	if err != nil {
		return err
	}
	headers.Set("Content-Type", contentType.canonical)
	if contentEncoding, conflict := resolvedSchemaKeywordString(schema, "contentEncoding"); conflict {
		return fmt.Errorf("resolved schema declares conflicting contentEncoding values")
	} else if resolveDeclaration(schema, false).admitsStringAsSoleNonNullType() && contentEncoding != "" {
		if explicit, present := openAPI32Header(encoding, "Content-Transfer-Encoding"); present && !explicit.admitsFold(contentEncoding) {
			return fmt.Errorf("explicit Content-Transfer-Encoding Header disallows contentEncoding %q", contentEncoding)
		}
		// R5 (2026-09-01): the edition's equivalence describes what the
		// declaration MEANS, not a field a serializer adds, and RFC 7578 §4.7
		// says senders SHOULD NOT generate the field. No emission; the
		// declared equivalence still governs the conflict check above and
		// parsing. Matches the 3.0/3.1 lanes.
	}
	part, err := writer.CreatePart(headers)
	if err != nil {
		return err
	}
	_, err = part.Write(body)
	return err
}

func openAPI32PartHeaders(name string, encoding *openAPI32EncodingOverlay, formData bool) (textproto.MIMEHeader, error) {
	headers := make(textproto.MIMEHeader)
	if name != "" {
		headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, name))
	} else if formData {
		headers.Set("Content-Disposition", `form-data; name=""`)
	}
	if encoding == nil {
		return headers, nil
	}
	type member struct {
		name   string
		header openAPI32HeaderOverlay
	}
	groups := map[string][]member{}
	for headerName, header := range encoding.headers {
		if strings.EqualFold(headerName, "Content-Type") {
			continue
		}
		if !httpToken(headerName) {
			return nil, fmt.Errorf("Encoding header name %q is not an HTTP token", headerName)
		}
		folded := strings.ToLower(headerName)
		groups[folded] = append(groups[folded], member{name: headerName, header: header})
	}
	for _, members := range groups {
		sort.Slice(members, func(i, j int) bool { return members[i].name < members[j].name })
		required := false
		fixed, hasFixed := "", false
		for _, candidate := range members {
			required = required || candidate.header.required
			if !candidate.header.unbounded && len(candidate.header.strings) == 0 {
				return nil, fmt.Errorf("Encoding header group %q has an empty exact string domain", candidate.name)
			}
			if candidate.header.fixedOK {
				if hasFixed && fixed != candidate.header.fixed {
					return nil, fmt.Errorf("case-equivalent Encoding headers fix conflicting values %q and %q", fixed, candidate.header.fixed)
				}
				fixed, hasFixed = candidate.header.fixed, true
			}
		}
		if hasFixed {
			for _, candidate := range members {
				if !candidate.header.unbounded && !candidate.header.strings[fixed] {
					return nil, fmt.Errorf("Encoding header %q rejects the group's fixed value %q", candidate.name, fixed)
				}
			}
			if !validSerializedHeaderFieldValue(fixed) {
				return nil, fmt.Errorf("Encoding header %q fixes an invalid HTTP field value", members[0].name)
			}
			if formData && strings.EqualFold(members[0].name, "Content-Disposition") {
				if hasHTTPParameterName(fixed, "filename*") {
					return nil, fmt.Errorf("Encoding Content-Disposition uses forbidden filename*")
				}
				disposition, parameters, parseErr := mime.ParseMediaType(fixed)
				if parseErr != nil || !strings.EqualFold(disposition, "form-data") || name != "" && parameters["name"] != name {
					return nil, fmt.Errorf("Encoding Content-Disposition does not preserve form-data name %q", name)
				}
			}
			headers.Set(members[0].name, fixed)
			continue
		}
		if required {
			return nil, fmt.Errorf("required Encoding header group %q fixes no exact value", members[0].name)
		}
	}
	return headers, nil
}

func openAPI32PartCarriage(schema *openapi3.Schema, encoding *openAPI32EncodingOverlay, value any) (parsedMediaType, revision3PropertyCarriageMode, error) {
	typed := openAPI32EncodingAsTyped(encoding)
	contentType, err := revision3PartContentType(schema, typed, false)
	if err != nil {
		// 3.2 can resolve a non-JSON text union from the supplied JSON type.
		kind := openAPI32JSONValueType(value)
		if encoding != nil && encoding.contentType != "" && openAPI32SchemaAdmitsRuntimeType(schema, kind) {
			contentType, err = parseRevision3MediaType(encoding.contentType)
		} else if openAPI32SchemaAdmitsRuntimeType(schema, kind) {
			switch kind {
			case "string", "boolean", "number", "integer":
				contentType, err = parseRevision3MediaType("text/plain")
			case "object", "array":
				contentType, err = parseRevision3MediaType("application/json")
			}
		}
		if err != nil {
			return parsedMediaType{}, 0, err
		}
	}
	if openAPI32EncodingHasNested(encoding) && strings.HasPrefix(contentType.base, "multipart/") {
		return contentType, revision3PropertyJSON, nil
	}
	mode, err := revision3PropertyCarriage(schema, contentType, false, true)
	if err != nil {
		kind := openAPI32JSONValueType(value)
		if !openAPI32SchemaAdmitsRuntimeType(schema, kind) {
			return parsedMediaType{}, 0, err
		}
		switch {
		case isJSONMediaType(contentType.base):
			mode = revision3PropertyJSON
		case isCharacterDataMedia(contentType.base) && (kind == "string" || kind == "boolean" || kind == "number" || kind == "integer"):
			mode = revision3PropertyText
		default:
			return parsedMediaType{}, 0, err
		}
	}
	return contentType, mode, nil
}

func openAPI32EncodingHasNested(encoding *openAPI32EncodingOverlay) bool {
	return encoding != nil && (encoding.encodingPresent || encoding.prefixPresent || encoding.itemPresent)
}

func encodeOpenAPI32NestedMultipart(value any, schema *openapi3.Schema, encoding *openAPI32EncodingOverlay, contentType parsedMediaType, depth int) ([]byte, parsedMediaType, error) {
	if encoding.encodingPresent && (encoding.prefixPresent || encoding.itemPresent) {
		return nil, parsedMediaType{}, fmt.Errorf("nested name-based and positional encodings are mutually exclusive")
	}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if boundary, present := contentType.renderedParams["boundary"]; present {
		if err := writer.SetBoundary(boundary); err != nil {
			return nil, parsedMediaType{}, err
		}
	}
	formData := contentType.base == "multipart/form-data"
	if encoding.encodingPresent {
		object, ok := asObject(value)
		if !ok {
			return nil, parsedMediaType{}, fmt.Errorf("nested name-based multipart value must be an object, got %T", value)
		}
		names := make([]string, 0, len(object))
		for name := range object {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			childSchema := resolvedMultipartPropertyFor(schema, name, map[*openapi3.Schema]bool{}, true, false)
			if err := writeOpenAPI32MultipartPart(writer, name, object[name], childSchema, encoding.encoding[name], formData, depth); err != nil {
				return nil, parsedMediaType{}, err
			}
		}
	} else {
		items, ok := asArray(value)
		if !ok {
			return nil, parsedMediaType{}, fmt.Errorf("nested positional multipart value must be an array, got %T", value)
		}
		itemSchema := resolvedMultipartItems(schema, map[*openapi3.Schema]bool{})
		for index, item := range items {
			var child *openAPI32EncodingOverlay
			if index < len(encoding.prefixEncoding) {
				child = encoding.prefixEncoding[index]
			} else {
				child = encoding.itemEncoding
			}
			name, err := openAPI32PositionalPartName(child, formData)
			if err != nil {
				return nil, parsedMediaType{}, err
			}
			if err := writeOpenAPI32MultipartPart(writer, name, item, itemSchema, child, formData, depth); err != nil {
				return nil, parsedMediaType{}, err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, parsedMediaType{}, err
	}
	params := make(map[string]string, len(contentType.renderedParams)+1)
	for key, value := range contentType.renderedParams {
		params[key] = value
	}
	if _, present := params["boundary"]; !present {
		params["boundary"] = writer.Boundary()
	}
	canonical := formatHTTPMediaType(contentType.base, params)
	parsed, err := parseRevision3MediaType(canonical)
	return buf.Bytes(), parsed, err
}
