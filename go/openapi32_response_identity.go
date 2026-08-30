package openapiclient

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
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
	success := openAPI32SuccessResponseKeys(rawResponses)
	var exclusions []OpenAPI32ResponseMediaExclusion
	for key, raw := range rawResponses {
		if strings.HasPrefix(key, "x-") {
			if responses.Extensions == nil {
				responses.Extensions = map[string]any{}
			}
			responses.Extensions[key] = cloneOverlayValue(raw, map[uintptr]any{})
			continue
		}
		// F1: a defect in a declaration that can never govern a success loses no
		// representation, so it must not destroy the target. The member is left
		// OUT of the materialized Responses rather than reported: that is the
		// same state the 3.0/3.1 confinement pass reaches when it neutralises the
		// defective raw position, so an actual failure response then finds no
		// governing declaration on every lane alike. Only a declaration that can
		// govern a SUCCESS is judged.
		if !success[key] && openAPI32NonSuccessResponseIsDefective(o, raw, operationNode.resource) {
			continue
		}
		node, err := o.resolveOpenAPI32ObjectNode(
			openAPI32RawNode{value: raw, resource: operationNode.resource},
			rawResponseTarget, "Response Object", map[string]bool{},
		)
		if err != nil {
			return nil, fmt.Errorf("response %q reference is unresolvable: %w", key, err)
		}
		if success[key] {
			if defect := openAPI32ResponseObjectDefect(node.value); defect != nil {
				return nil, fmt.Errorf("response %q is upstream-invalid: %w", key, defect)
			}
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
	// The upstream-invalid test is NOT applied here. It is success-scoped (F1),
	// and only the caller knows which key this Response Object is declared at;
	// judging every materialized response here would re-impose the exclusion on
	// the non-success declarations the ruling exempts.
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

// openAPI32NonSuccessResponseIsDefective reports whether a NON-SUCCESS
// Responses member is one F1 exempts from the exclusion: a member that is not a
// Response Object at all, one whose reference names none, or one violating the
// fixed-field constraints. Such a member is dropped from the materialized
// Responses instead of costing its target, which leaves every lane in the same
// state -- an actual failure response with no governing declaration.
//
// A member this cannot judge (an unreachable external reference) is left
// standing, which is the conservative direction: the ordinary path then reports
// it exactly as it did before.
func openAPI32NonSuccessResponseIsDefective(o *OpenAPI32Overlay, raw any, resource *openAPI32RawResource) bool {
	node, err := o.resolveOpenAPI32ObjectNode(
		openAPI32RawNode{value: raw, resource: resource},
		rawResponseTarget, "Response Object", map[string]bool{},
	)
	if err != nil {
		return true
	}
	return openAPI32ResponseObjectDefect(node.value) != nil
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

// openAPI32ResponseObjectDefect reports the upstream-invalid governing Response
// Object defects `openbindings.openapi-3.2@1` §9.6 names: a `description` that
// is not a string, a `content`, `headers`, or `links` value that is not a map,
// or a `headers` member that is not a Header Object. It is the 3.0/3.1 floor's
// D16 predicate, stated on this lane in this edition's own terms.
//
// The 3.0/3.1 lanes reach D16 through the acceptance floor, which does not
// accept the 3.2 edition at all: the 3.2 lane asks its declaration questions
// over its own raw overlay, so a sibling rule has to be stated here or it is not
// stated at all. Round R measured what that cost: `description: 123` excluded
// the target on 3.0/3.1, excluded it in Go's 3.2 lane only by accident
// (kin-openapi refusing the value), and COMPLETED THE INVOCATION in
// TypeScript's. One rule, three answers, inside one family.
//
// Round R2 finished the job for the other four kinds, which had the same
// defect: they excluded, but because kin-openapi could not decode the value and
// the per-target fallback then isolated the target -- an outcome that followed
// from a parser's limits rather than from a rule, and that said nothing at all
// about the non-success declarations the same parser also refused.
//
// WHY OMISSION IS NOT A DEFECT HERE, and it is an AUTHORITY difference rather
// than a gap. OAS 3.2.0 DROPPED the `REQUIRED` marker that OAS 3.0.4 and OAS
// 3.1.2 carry on the Response Object's `description`, and added an optional
// `summary` beside it:
//
//	OAS 3.0.4 §4.7.17.1  description | string | REQUIRED. A description ...
//	OAS 3.1.2 §4.8.17.1  description | string | REQUIRED. A description ...
//	OAS 3.2.0 §4.17.1    summary     | string | A short summary ...
//	                     description | string | A description ...   <- no REQUIRED
//
// So a 3.2 Response Object that omits `description` is CONFORMANT and governs
// normally, while the same omission is upstream-invalid on the 3.0/3.1 lines
// (the shared case table pins that as S1). The two lines answering differently
// is correct, and `openbindings.openapi-3.2@1` §9.6 states it as the edition
// difference it is. What 3.2 still fixes is the KIND, which is the whole of what
// this function tests.
//
// Round R nearly got this wrong in the safe direction's opposite: it implemented
// the omission check too, and 25 shipped tests in this package went red. They
// were not stale fixtures. They were legal OAS 3.2 documents, and the authority
// was refusing a rule it does not impose.
//
// DECISION, recorded where a reader of this rule will look: THIS LANE GETS NO
// ACCEPTANCE FLOOR, and that is a finding rather than deferred debt. Round R2
// scouted one and measured why it would be the wrong instrument. The floor's own
// primitive `isFloorResponseObject` DEFINES a Response Object by the presence of
// `description` -- the exact constraint OAS 3.2.0 removed -- so D7 and D6 would
// both be wrong on this line before any other class fired; its `httpMethods`
// inventory predates `query` and `additionalOperations`, so the raw operation
// inventory would be wrong too; and fifteen further classes would begin firing
// on a lane that reaches every verdict through the overlay, changing coverage
// emission, the confinement pass's attribution and §3 part 2's whole-source
// refusal, and forcing 3.2 cells into the digest-pinned shared case table. A
// ladder built on a presence predicate the edition deleted is not a smaller
// version of the right instrument; it is the wrong one. The complaint the debt
// note recorded -- that the outcome followed from the absence of a rule -- is
// discharged by STATING the rule here, which is what this function does.
func openAPI32ResponseObjectDefect(value any) error {
	object, isObject := value.(map[string]any)
	if !isObject {
		return fmt.Errorf("Response Object is not an object")
	}
	if raw, present := object["description"]; present {
		if _, isString := raw.(string); !isString {
			return fmt.Errorf("Response Object `description` is not a string")
		}
	}
	for _, field := range []string{"content", "headers", "links"} {
		raw, present := object[field]
		if !present {
			continue
		}
		members, isMap := raw.(map[string]any)
		if !isMap {
			return fmt.Errorf("Response Object %q is not a map", field)
		}
		if field != "headers" {
			continue
		}
		names := make([]string, 0, len(members))
		for name := range members {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			// Case-SENSITIVE, exactly as the 3.0/3.1 floor's test is: these keys
			// are HEADER NAMES, and `X-Request-Id` is an ordinary header rather
			// than a specification extension.
			if strings.HasPrefix(name, "x-") {
				continue
			}
			if _, isObject := members[name].(map[string]any); !isObject {
				return fmt.Errorf("Response Object header %q is not a Header Object", name)
			}
		}
	}
	return nil
}

// openAPI32SuccessResponseKeys returns the Responses keys whose declaration can
// govern a SUCCESSFUL (2xx final status) response.
//
// Round R2's F1 ruling scopes the upstream-invalid Response Object exclusion to
// the governing SUCCESS declaration, family-wide: a failure body is opaque
// application-authored data (§9.6), so a defect in the declaration that governs
// one loses no representation and must not destroy a target whose success path
// is intact. It is the same reasoning that carves out a Response Object which
// declares no content, and it is what the 3.0/3.1 acceptance floor already
// performs by never climbing at a non-success response.
//
// `default` qualifies only when no `2XX` range key is declared: a `2XX` key
// covers the whole success class, so `default` can then never govern one. That
// is the same question `swagger20SuccessResponseKey` answers with an
// unconditional yes, because OAS 2.0 has no range keys at all.
func openAPI32SuccessResponseKeys(responses map[string]any) map[string]bool {
	_, hasSuccessRange := responses["2XX"]
	out := make(map[string]bool, len(responses))
	for key := range responses {
		switch {
		case strings.HasPrefix(key, "x-"):
		case floorSuccessCodeRE.MatchString(key), key == "2XX":
			out[key] = true
		case key == "default" && !hasSuccessRange:
			out[key] = true
		}
	}
	return out
}
