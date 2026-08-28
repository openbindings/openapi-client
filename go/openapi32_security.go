package openapiclient

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

func (o *OpenAPI32Overlay) hydrateSecurityRequirementURIs(
	requested *url.URL,
	allowExternal bool,
	read func(*url.URL) ([]byte, *url.URL, error),
) {
	if o == nil || !allowExternal || read == nil {
		return
	}
	o.mu.RLock()
	owner := o.resources[artifactResourceKey(requested)]
	if owner == nil && requested == nil {
		owner = o.entry
	}
	entrySchemes := rawOpenAPI32SecuritySchemes(o.entry)
	ownerSchemes := rawOpenAPI32SecuritySchemes(owner)
	names := openAPI32SecurityRequirementNames(owner)
	var missing []*url.URL
	var identities []*url.URL
	for _, name := range names {
		if _, exactEntryComponent := entrySchemes[name]; exactEntryComponent {
			continue
		}
		if owner != o.entry && !strings.HasPrefix(name, "./") {
			if _, exactReferringComponent := ownerSchemes[name]; exactReferringComponent {
				// A component-name implicit connection in a referenced document
				// is resolved through implicitConnectionScope, not fetched as a
				// same-spelled relative URI. The ./ spelling is the explicit URI
				// disambiguator and deliberately bypasses this branch.
				continue
			}
		}
		parsed, err := url.Parse(name)
		if err != nil {
			continue
		}
		var resolved *url.URL
		switch {
		case parsed.IsAbs():
			resolved = parsed
		case owner != nil && owner.base != nil:
			resolved = owner.base.ResolveReference(parsed)
		}
		if resolved == nil {
			continue
		}
		identity := *resolved
		identities = append(identities, &identity)
		resourceURL := cloneURL(resolved)
		resourceURL.Fragment = ""
		if o.resources[artifactResourceKey(resourceURL)] == nil {
			missing = append(missing, resourceURL)
		}
	}
	o.mu.RUnlock()

	seen := map[string]bool{}
	for _, resourceURL := range missing {
		key := artifactResourceKey(resourceURL)
		if seen[key] {
			continue
		}
		seen[key] = true
		data, retrieval, err := read(resourceURL)
		if err != nil {
			// A Security Requirement URI owns only its containing alternative;
			// an unavailable resource never poisons sibling alternatives or the
			// whole description.
			continue
		}
		if o.capture(data, resourceURL, retrieval, false) == nil {
			o.hydrateSecurityRequirementURIs(resourceURL, allowExternal, read)
		}
	}
	for _, identity := range identities {
		o.hydrateOpenAPI32SecuritySchemeReference(identity, read)
	}
}

// hydrateOpenAPI32SecuritySchemeReference follows Reference Objects reached
// only through a Security Requirement URI. kin-openapi does not necessarily
// see this graph edge because the requirement name, rather than a typed $ref
// field, owns the first hop. Each failed hop remains confined to the security
// alternative that named it.
func (o *OpenAPI32Overlay) hydrateOpenAPI32SecuritySchemeReference(
	identity *url.URL,
	read func(*url.URL) ([]byte, *url.URL, error),
) {
	if o == nil || identity == nil || read == nil {
		return
	}
	current := *identity
	seen := map[string]bool{}
	for {
		key := current.String()
		if seen[key] {
			return
		}
		seen[key] = true

		o.mu.RLock()
		resource := o.resources[artifactResourceKey(&current)]
		var refText string
		var base *url.URL
		if resource != nil {
			target, ok := rawFragmentTarget(resource.root, current.Fragment, rawSecuritySchemeTarget)
			object, _ := target.(map[string]any)
			if ok && object != nil {
				refText, _ = object["$ref"].(string)
				base = cloneURL(resource.base)
			}
		}
		o.mu.RUnlock()

		if resource == nil {
			resourceURL := cloneURL(&current)
			data, retrieval, err := read(resourceURL)
			if err != nil || o.capture(data, resourceURL, retrieval, false) != nil {
				return
			}
			continue
		}
		if refText == "" {
			return
		}
		parsed, err := url.Parse(refText)
		if err != nil {
			return
		}
		switch {
		case parsed.IsAbs():
			current = *parsed
		case base != nil:
			current = *base.ResolveReference(parsed)
		default:
			return
		}
	}
}

func openAPI32SecurityRequirementNames(resource *openAPI32RawResource) []string {
	if resource == nil {
		return nil
	}
	root, _ := resource.root.(map[string]any)
	set := map[string]bool{}
	collect := func(raw any) {
		requirements, _ := raw.([]any)
		for _, member := range requirements {
			requirement, _ := member.(map[string]any)
			for name := range requirement {
				set[name] = true
			}
		}
	}
	collect(root["security"])
	collectOperations := func(pathItem map[string]any) {
		for _, method := range openAPI32FixedMethods {
			operation, _ := pathItem[method].(map[string]any)
			if operation != nil {
				collect(operation["security"])
			}
		}
		additional, _ := pathItem["additionalOperations"].(map[string]any)
		for _, rawOperation := range additional {
			operation, _ := rawOperation.(map[string]any)
			if operation != nil {
				collect(operation["security"])
			}
		}
	}
	if paths, _ := root["paths"].(map[string]any); paths != nil {
		for _, rawPathItem := range paths {
			pathItem, _ := rawPathItem.(map[string]any)
			collectOperations(pathItem)
		}
	} else {
		collectOperations(root)
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// materializeOpenAPI32ServerTarget restores the retrieval location of Server
// Objects declared by a referenced Path Item resource. Reference resolution
// follows $self identity, but API Server URLs explicitly do not: they remain
// relative to the resource's retrieval URI. Composed target images otherwise
// look entry-local to the typed parser, so the overlay reapplies that one
// provenance fact to target-local copies.
func (a *Artifact) materializeOpenAPI32ServerTarget(target *OperationTarget) *OperationTarget {
	if a == nil || a.openAPI32 == nil || target == nil {
		return target
	}
	o := a.openAPI32
	o.mu.RLock()
	defer o.mu.RUnlock()
	node, ok := o.selectedRawOperationLocked(target.OperationReference, map[string]bool{})
	if !ok || node.resource == nil || node.resource.retrieval == nil {
		return target
	}
	location := artifactResourceKey(node.resource.retrieval)
	if location == "" {
		return target
	}
	copyTarget := *target
	if target.PathItem != nil && len(target.PathItem.Servers) > 0 {
		copyPathItem := *target.PathItem
		copyPathItem.Servers = openAPI32ServersWithDocument(target.PathItem.Servers, location)
		copyTarget.PathItem = &copyPathItem
	}
	if target.Operation != nil && target.Operation.Servers != nil && len(*target.Operation.Servers) > 0 {
		copyOperation := *target.Operation
		servers := openAPI32ServersWithDocument(*target.Operation.Servers, location)
		copyOperation.Servers = &servers
		copyTarget.Operation = &copyOperation
	}
	return &copyTarget
}

func openAPI32ServersWithDocument(servers openapi3.Servers, location string) openapi3.Servers {
	result := make(openapi3.Servers, len(servers))
	for index, server := range servers {
		if server == nil {
			continue
		}
		copyServer := *server
		copyServer.Extensions = make(map[string]any, len(server.Extensions)+1)
		for name, value := range server.Extensions {
			copyServer.Extensions[name] = value
		}
		copyServer.Extensions[serverDocumentMarker] = location
		result[index] = &copyServer
	}
	return result
}

// materializeOpenAPI32SecurityTarget projects only the Security Scheme
// identities needed by one selected operation out of the artifact-local raw
// resource overlay. Security Requirement URI names are installed under their
// exact authored spelling in a target-local document copy. Component-name
// requirements from a non-entry referring document are returned separately
// for the adapter's implicitConnectionScope configuration point.
func (a *Artifact) materializeOpenAPI32SecurityTarget(target *OperationTarget) *OperationTarget {
	if a == nil || a.openAPI32 == nil || target == nil || target.Operation == nil || target.Document == nil {
		return target
	}
	o := a.openAPI32
	o.mu.RLock()
	defer o.mu.RUnlock()

	operationNode, operationFound := o.selectedRawOperationLocked(target.OperationReference, map[string]bool{})
	root, _ := o.entry.root.(map[string]any)
	if root == nil {
		return target
	}
	requirementNode := openAPI32RawNode{value: root["security"], resource: o.entry}
	if operationFound {
		if operation, _ := operationNode.value.(map[string]any); operation != nil {
			if security, present := operation["security"]; present {
				requirementNode = openAPI32RawNode{value: security, resource: operationNode.resource}
			}
		}
	}
	requirements, _ := requirementNode.value.([]any)
	if len(requirements) == 0 {
		return target
	}

	entryComponents := rawOpenAPI32SecuritySchemes(o.entry)
	referringComponents := rawOpenAPI32SecuritySchemes(requirementNode.resource)
	uriSchemes := openapi3.SecuritySchemes{}
	referringSchemes := openapi3.SecuritySchemes{}
	for _, rawRequirement := range requirements {
		requirement, _ := rawRequirement.(map[string]any)
		for name := range requirement {
			referringMatch := false
			if requirementNode.resource != nil && requirementNode.resource != o.entry && !strings.HasPrefix(name, "./") {
				if rawScheme, match := referringComponents[name]; match {
					referringMatch = true
					if scheme := o.parseOpenAPI32SecuritySchemeLocked(openAPI32RawNode{value: rawScheme, resource: requirementNode.resource}); scheme != nil {
						referringSchemes[name] = &openapi3.SecuritySchemeRef{Value: scheme}
					}
				}
			}
			if _, entryMatch := entryComponents[name]; entryMatch {
				continue
			}
			if referringMatch {
				continue
			}
			// With no exact entry-component match, every admitted name is a URI
			// reference. URI identity bypasses implicitConnectionScope.
			if scheme := o.resolveOpenAPI32SecurityURINameLocked(name, requirementNode.resource); scheme != nil {
				uriSchemes[name] = &openapi3.SecuritySchemeRef{Value: scheme}
			}
		}
	}
	if len(uriSchemes) == 0 && len(referringSchemes) == 0 {
		return target
	}

	copyTarget := *target
	copyTarget.ReferringSecuritySchemes = referringSchemes
	if len(uriSchemes) == 0 {
		return &copyTarget
	}
	copyDocument := *target.Document
	copyComponents := openapi3.Components{}
	if target.Document.Components != nil {
		copyComponents = *target.Document.Components
	}
	copyComponents.SecuritySchemes = openapi3.SecuritySchemes{}
	if target.Document.Components != nil {
		for name, scheme := range target.Document.Components.SecuritySchemes {
			copyComponents.SecuritySchemes[name] = scheme
		}
	}
	for name, scheme := range uriSchemes {
		copyComponents.SecuritySchemes[name] = scheme
	}
	copyDocument.Components = &copyComponents
	copyTarget.Document = &copyDocument
	return &copyTarget
}

func rawOpenAPI32SecuritySchemes(resource *openAPI32RawResource) map[string]any {
	if resource == nil {
		return nil
	}
	root, _ := resource.root.(map[string]any)
	components, _ := root["components"].(map[string]any)
	schemes, _ := components["securitySchemes"].(map[string]any)
	return schemes
}

func (o *OpenAPI32Overlay) resolveOpenAPI32SecurityURINameLocked(name string, owner *openAPI32RawResource) *openapi3.SecurityScheme {
	parsed, err := url.Parse(name)
	if err != nil {
		return nil
	}
	var resolved *url.URL
	switch {
	case parsed.IsAbs():
		resolved = parsed
	case owner != nil && owner.base != nil:
		resolved = owner.base.ResolveReference(parsed)
	default:
		return nil
	}
	resource := o.resources[artifactResourceKey(resolved)]
	if resource == nil && strings.HasPrefix(name, "#") {
		resource = owner
	}
	if resource == nil {
		return nil
	}
	target, ok := rawFragmentTarget(resource.root, resolved.Fragment, rawSecuritySchemeTarget)
	if !ok {
		return nil
	}
	return o.parseOpenAPI32SecuritySchemeLocked(openAPI32RawNode{value: target, resource: resource})
}

func (o *OpenAPI32Overlay) parseOpenAPI32SecuritySchemeLocked(node openAPI32RawNode) *openapi3.SecurityScheme {
	resolved, ok := o.resolveOpenAPI32SecuritySchemeObjectLocked(node, map[string]bool{})
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(resolved.value)
	if err != nil {
		return nil
	}
	var scheme openapi3.SecurityScheme
	if json.Unmarshal(encoded, &scheme) != nil || malformedOpenAPI32SecurityScheme(&scheme) {
		return nil
	}
	return &scheme
}

func (o *OpenAPI32Overlay) resolveOpenAPI32SecuritySchemeObjectLocked(node openAPI32RawNode, seen map[string]bool) (openAPI32RawNode, bool) {
	object, _ := node.value.(map[string]any)
	if object == nil {
		return openAPI32RawNode{}, false
	}
	refText, _ := object["$ref"].(string)
	if refText == "" {
		return node, true
	}
	parsed, err := url.Parse(refText)
	if err != nil {
		return openAPI32RawNode{}, false
	}
	var resolved *url.URL
	switch {
	case strings.HasPrefix(refText, "#") && node.resource != nil && node.resource.base != nil:
		resolved = cloneURL(node.resource.base)
		resolved.Fragment = parsed.Fragment
	case parsed.IsAbs():
		resolved = parsed
	case node.resource != nil && node.resource.base != nil:
		resolved = node.resource.base.ResolveReference(parsed)
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
		resource = node.resource
	}
	if resource == nil {
		return openAPI32RawNode{}, false
	}
	target, ok := rawFragmentTarget(resource.root, resolved.Fragment, rawSecuritySchemeTarget)
	if !ok {
		return openAPI32RawNode{}, false
	}
	return o.resolveOpenAPI32SecuritySchemeObjectLocked(openAPI32RawNode{value: target, resource: resource}, seen)
}

func malformedOpenAPI32SecurityScheme(scheme *openapi3.SecurityScheme) bool {
	if scheme == nil {
		return true
	}
	switch scheme.Type {
	case "apiKey":
		return scheme.Name == "" || (scheme.In != "query" && scheme.In != "header" && scheme.In != "cookie")
	case "http":
		return scheme.Scheme == ""
	case "oauth2":
		return scheme.Flows == nil
	case "openIdConnect":
		return scheme.OpenIdConnectUrl == ""
	case "mutualTLS":
		return false
	default:
		return true
	}
}
