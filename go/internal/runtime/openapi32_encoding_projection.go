package openapiclient

import "github.com/getkin/kin-openapi/openapi3"

// The typed loader does not resolve Encoding Header references, but its
// InternalizeRefs assumes every HeaderRef has a Value. Remove only those
// unresolved entries from the private projection graph before internalization.
// This is not evidence that an authored header is absent or resolved: the raw
// snapshot is unchanged, and request materialization resolves applicable headers
// (or excludes their media owner) before exposing an executable target.
func prepareOpenAPI32EncodingProjection(doc *openapi3.T) {
	seenMedia := map[*openapi3.MediaType]bool{}
	seenPaths := map[*openapi3.PathItem]bool{}
	var content func(openapi3.Content)
	var headers func(openapi3.Headers)
	headers = func(values openapi3.Headers) {
		for _, ref := range values {
			if ref != nil && ref.Value != nil {
				content(ref.Value.Content)
			}
		}
	}
	content = func(values openapi3.Content) {
		for _, media := range values {
			if media == nil || seenMedia[media] {
				continue
			}
			seenMedia[media] = true
			for _, encoding := range media.Encoding {
				if encoding == nil {
					continue
				}
				for name, ref := range encoding.Headers {
					if ref == nil || ref.Value == nil {
						delete(encoding.Headers, name)
					}
				}
				headers(encoding.Headers)
			}
		}
	}
	parameters := func(values openapi3.Parameters) {
		for _, ref := range values {
			if ref != nil && ref.Value != nil {
				content(ref.Value.Content)
			}
		}
	}
	body := func(ref *openapi3.RequestBodyRef) {
		if ref != nil && ref.Value != nil {
			content(ref.Value.Content)
		}
	}
	responses := func(values openapi3.ResponseBodies) {
		for _, ref := range values {
			if ref != nil && ref.Value != nil {
				headers(ref.Value.Headers)
				content(ref.Value.Content)
			}
		}
	}
	var paths func(map[string]*openapi3.PathItem)
	callbacks := func(values openapi3.Callbacks) {
		for _, ref := range values {
			if ref != nil && ref.Value != nil {
				paths(ref.Value.Map())
			}
		}
	}
	paths = func(values map[string]*openapi3.PathItem) {
		for _, item := range values {
			if item == nil || seenPaths[item] {
				continue
			}
			seenPaths[item] = true
			parameters(item.Parameters)
			for _, operation := range item.Operations() {
				if operation == nil {
					continue
				}
				parameters(operation.Parameters)
				body(operation.RequestBody)
				callbacks(operation.Callbacks)
				if operation.Responses != nil {
					responses(operation.Responses.Map())
				}
			}
		}
	}
	// Match the content-bearing owners walked by the pinned InternalizeRefs.
	if components := doc.Components; components != nil {
		for _, ref := range components.Parameters {
			parameters(openapi3.Parameters{ref})
		}
		headers(components.Headers)
		for _, ref := range components.RequestBodies {
			body(ref)
		}
		responses(components.Responses)
		callbacks(components.Callbacks)
	}
	if doc.Paths != nil {
		paths(doc.Paths.Map())
	}
}
