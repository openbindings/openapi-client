package openapiclient

import (
	"fmt"
	"net/url"
	"strings"
)

type swagger20Resolution struct {
	node     any
	resource *swagger20Resource
	cycle    bool
}

type swagger20ResolutionMemo struct {
	active     map[string]bool
	pathActive map[string]bool
	done       map[string]swagger20Resolution
}

func newSwagger20ResolutionMemo() *swagger20ResolutionMemo {
	return &swagger20ResolutionMemo{
		active: map[string]bool{}, pathActive: map[string]bool{}, done: map[string]swagger20Resolution{},
	}
}

func (g *swagger20ReferenceGraph) resolveReference(raw string, from *swagger20Resource, memo *swagger20ResolutionMemo) (swagger20Resolution, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return swagger20Resolution{}, fmt.Errorf("Swagger 2.0 reference %q is not a URI: %w", raw, err)
	}
	base := from.base()
	fragmentOnly := parsed.Scheme == "" && parsed.Host == "" && parsed.Path == "" && parsed.RawQuery == ""

	var target *url.URL
	switch {
	case fragmentOnly:
		if base != nil {
			target = base.ResolveReference(parsed)
		} else {
			target = &url.URL{Fragment: parsed.Fragment, RawFragment: parsed.RawFragment}
		}
	case base != nil:
		target = base.ResolveReference(parsed)
	case parsed.IsAbs():
		if g.selfContained {
			return swagger20Resolution{}, fmt.Errorf("reference %q cannot resolve: embedded Swagger 2.0 content with no co-present location must be self-contained", raw)
		}
		target = parsed
	default:
		return swagger20Resolution{}, fmt.Errorf("relative reference %q has no absolute Swagger 2.0 artifact base", raw)
	}

	resourceURI := cloneURL(target)
	canonical := artifactResourceKey(resourceURI) + "#" + target.EscapedFragment()
	if result, ok := memo.done[canonical]; ok {
		return result, nil
	}
	if memo.active[canonical] {
		return swagger20Resolution{resource: from, cycle: true}, nil
	}
	memo.active[canonical] = true
	defer delete(memo.active, canonical)

	resource, err := g.resourceForReference(resourceURI, from)
	if err != nil {
		return swagger20Resolution{}, err
	}
	result := swagger20Resolution{node: resource.root, resource: resource}
	if target.Fragment != "" {
		if !strings.HasPrefix(target.Fragment, "/") {
			return swagger20Resolution{}, fmt.Errorf("Swagger 2.0 reference %q has unsupported non-JSON-Pointer fragment", raw)
		}
		result, err = g.resolvePointer(resource, target.Fragment, memo)
		if err != nil {
			return swagger20Resolution{}, fmt.Errorf("resolve Swagger 2.0 reference %q: %w", raw, err)
		}
	}
	memo.done[canonical] = result
	return result, nil
}

func (g *swagger20ReferenceGraph) resourceForReference(uri *url.URL, from *swagger20Resource) (*swagger20Resource, error) {
	key := artifactResourceKey(uri)
	if key == "" {
		return from, nil
	}
	if from != nil && (key == artifactResourceKey(from.requested) || key == artifactResourceKey(from.retrieval)) {
		return from, nil
	}
	g.mu.RLock()
	cached := g.resources[key]
	g.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	if g.selfContained {
		return nil, fmt.Errorf("reference %q cannot resolve: embedded Swagger 2.0 content with no co-present location must be self-contained", uri)
	}
	if !g.allowExternalRefs {
		return nil, fmt.Errorf("external Swagger 2.0 reference %q is disabled", uri)
	}
	data, err := g.read(uri)
	if err != nil {
		return nil, err
	}
	root, err := parseSwagger20Resource(data)
	if err != nil {
		return nil, err
	}
	return g.rememberResource(uri, root), nil
}

// resolvePointer implements RFC 6901 over the referenced document. JSON
// Reference draft-03 transclusion is honored when a pointer token runs below
// a Reference Object: the reference target is substituted and traversal
// continues at the same token.
func (g *swagger20ReferenceGraph) resolvePointer(resource *swagger20Resource, pointer string, memo *swagger20ResolutionMemo) (swagger20Resolution, error) {
	if pointer == "" {
		return swagger20Resolution{node: resource.root, resource: resource}, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return swagger20Resolution{}, fmt.Errorf("fragment %q is not an RFC 6901 JSON Pointer", pointer)
	}
	current := swagger20Resolution{node: resource.root, resource: resource}
	for _, encoded := range strings.Split(pointer[1:], "/") {
		if !wellFormedPointerToken(encoded) {
			return swagger20Resolution{}, fmt.Errorf("fragment %q contains malformed RFC 6901 escape", pointer)
		}
		token := decodePointerToken(encoded)
		for {
			switch typed := current.node.(type) {
			case map[string]any:
				if child, present := typed[token]; present {
					current.node = child
					goto nextToken
				}
				ref, hasRef := typed["$ref"].(string)
				if !hasRef {
					return swagger20Resolution{}, fmt.Errorf("JSON Pointer token %q identifies no object member", token)
				}
				resolved, err := g.resolveReference(ref, current.resource, memo)
				if err != nil {
					return swagger20Resolution{}, err
				}
				if resolved.cycle {
					return resolved, nil
				}
				current = resolved
			case []any:
				index, ok := rawSequenceIndex(token, len(typed))
				if !ok {
					return swagger20Resolution{}, fmt.Errorf("JSON Pointer token %q identifies no array member", token)
				}
				current.node = typed[index]
				goto nextToken
			default:
				return swagger20Resolution{}, fmt.Errorf("JSON Pointer token %q cannot be applied to a scalar", token)
			}
		}
	nextToken:
	}
	return current, nil
}

func (g *swagger20ReferenceGraph) resolvePathItem(item swagger20PathItem, method string, memo *swagger20ResolutionMemo) (swagger20PathItem, error) {
	ref := item.reference()
	if !ref.present {
		return item, nil
	}
	if !ref.valid || ref.value == "" {
		return swagger20PathItem{}, fmt.Errorf("selected Swagger 2.0 Path Item has an invalid $ref")
	}
	key := artifactResourceKey(item.resource.base()) + "|" + ref.value + "|" + method
	if memo.pathActive[key] {
		return swagger20PathItem{}, fmt.Errorf("selected Swagger 2.0 Path Item reference cycle leaves %s unresolved", method)
	}
	memo.pathActive[key] = true
	defer delete(memo.pathActive, key)
	resolved, err := g.resolveReference(ref.value, item.resource, memo)
	if err != nil {
		return swagger20PathItem{}, err
	}
	if resolved.cycle {
		return swagger20PathItem{}, fmt.Errorf("selected Swagger 2.0 Path Item reference cycle leaves %s unresolved", method)
	}
	targetObject, ok := resolved.node.(map[string]any)
	if !ok {
		return swagger20PathItem{}, fmt.Errorf("selected Swagger 2.0 Path Item $ref does not resolve to an object")
	}
	target, err := g.resolvePathItem(swagger20PathItem{raw: swagger20Object(targetObject), resource: resolved.resource}, method, memo)
	if err != nil {
		return swagger20PathItem{}, err
	}

	if _, adjacentMethod := item.raw.member(method); adjacentMethod {
		if _, referencedMethod := target.raw.member(method); referencedMethod {
			return swagger20PathItem{}, fmt.Errorf("selected Swagger 2.0 Path Item has undefined adjacent/ref collision at %s", method)
		}
	}
	if _, adjacentParameters := item.raw.member("parameters"); adjacentParameters {
		if _, referencedParameters := target.raw.member("parameters"); referencedParameters {
			return swagger20PathItem{}, fmt.Errorf("selected Swagger 2.0 Path Item has undefined adjacent/ref collision at parameters")
		}
	}

	merged := make(swagger20Object, len(target.raw)+len(item.raw))
	memberResources := make(map[string]*swagger20Resource, len(target.raw)+len(item.raw))
	for name, value := range target.raw {
		merged[name] = value
		memberResources[name] = target.resourceFor(name)
	}
	for name, value := range item.raw {
		if name == "$ref" {
			continue
		}
		if _, collision := merged[name]; collision {
			// Collisions in methods and fields unused by this selected target are
			// confined there; the referenced declaration remains authoritative
			// for this target's resolved view.
			continue
		}
		merged[name] = value
		memberResources[name] = item.resourceFor(name)
	}
	return swagger20PathItem{raw: merged, resource: target.resource, memberResources: memberResources}, nil
}
