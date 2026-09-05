package openapiclient

import (
	"encoding/json"
	"net/url"

	"github.com/getkin/kin-openapi/openapi3"
)

// referringSecuritySchemes recovers the component-name scope carried by the
// private raw-loader marker on an externally referenced Path Item. The marker
// never crosses the public Artifact API; its typed value is copied into the
// selected OperationTarget instead.
func (a *Artifact) referringSecuritySchemes(pathItem *openapi3.PathItem) openapi3.SecuritySchemes {
	if a == nil || a.Edition.IsOpenAPI32() || pathItem == nil || pathItem.Extensions == nil {
		return nil
	}
	raw, present := pathItem.Extensions[referringSecurityMarker]
	if !present {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var schemes openapi3.SecuritySchemes
	if err := json.Unmarshal(encoded, &schemes); err != nil {
		return nil
	}
	return schemes
}

func referringSecurityScopes(entry []byte, location string, resources map[string][]byte) map[string]openapi3.SecuritySchemes {
	if len(entry) == 0 || len(resources) == 0 {
		return nil
	}
	parsed, err := parseRawOpenAPIResource(entry)
	if err != nil {
		return nil
	}
	root, _ := parsed.(map[string]any)
	paths, _ := root["paths"].(map[string]any)
	var base *url.URL
	if location != "" {
		base, _ = url.Parse(location)
	}
	result := map[string]openapi3.SecuritySchemes{}
	for path, rawItem := range paths {
		item, _ := rawItem.(map[string]any)
		refText, _ := item["$ref"].(string)
		if refText == "" {
			continue
		}
		ref, parseErr := url.Parse(refText)
		if parseErr != nil {
			continue
		}
		if !ref.IsAbs() {
			if base == nil {
				continue
			}
			ref = base.ResolveReference(ref)
		}
		data := resources[artifactResourceKey(ref)]
		resource, parseErr := parseRawOpenAPIResource(data)
		if parseErr != nil {
			continue
		}
		resourceRoot, _ := resource.(map[string]any)
		components, _ := resourceRoot["components"].(map[string]any)
		rawSchemes, _ := components["securitySchemes"].(map[string]any)
		encoded, marshalErr := json.Marshal(rawSchemes)
		if marshalErr != nil {
			continue
		}
		var schemes openapi3.SecuritySchemes
		if json.Unmarshal(encoded, &schemes) == nil && len(schemes) > 0 {
			result[path] = schemes
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (a *Artifact) materializeReferringSecurityTarget(target *OperationTarget) *OperationTarget {
	if target == nil || len(target.ReferringSecuritySchemes) > 0 {
		return target
	}
	schemes := a.referringSecurityByPath[target.Path]
	if len(schemes) == 0 {
		schemes = a.referringSecuritySchemes(target.PathItem)
	}
	if len(schemes) == 0 {
		return target
	}
	copyTarget := *target
	copyTarget.ReferringSecuritySchemes = schemes
	return &copyTarget
}

// securityDocumentForScope returns an invocation-local document view. Entry
// and referring component scopes are alternatives selected by consumer
// configuration; they are never merged and the loaded artifact is immutable.
func securityDocumentForScope(document *openapi3.T, referring openapi3.SecuritySchemes, scope ImplicitConnectionScope) *openapi3.T {
	if document == nil || scope != ImplicitConnectionReferring || len(referring) == 0 {
		return document
	}
	copyDocument := *document
	copyComponents := openapi3.Components{}
	if document.Components != nil {
		copyComponents = *document.Components
	}
	copyComponents.SecuritySchemes = make(openapi3.SecuritySchemes, len(referring))
	for name, scheme := range referring {
		copyComponents.SecuritySchemes[name] = scheme
	}
	copyDocument.Components = &copyComponents
	return &copyDocument
}

func targetForImplicitConnectionScope(target *OperationTarget, contextValue map[string]any) *OperationTarget {
	if target == nil {
		return nil
	}
	scope, _ := contextConfiguration(contextValue)["implicitConnectionScope"].(string)
	document := securityDocumentForScope(target.Document, target.ReferringSecuritySchemes, ImplicitConnectionScope(scope))
	if document == target.Document {
		return target
	}
	copyTarget := *target
	copyTarget.Document = document
	return &copyTarget
}
