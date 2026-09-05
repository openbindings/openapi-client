package openapiclient

import (
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func artifactResourceKey(value *url.URL) string {
	if value == nil {
		return ""
	}
	copy := *value
	copy.Fragment = ""
	return copy.String()
}

func cloneURL(value *url.URL) *url.URL {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Fragment = ""
	return &copy
}

func unescapeJSONPointerSegment(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
}

func escapeJSONPointerSegment(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

func mergeParameters(pathParameters, operationParameters openapi3.Parameters) openapi3.Parameters {
	if len(pathParameters) == 0 {
		return operationParameters
	}
	if len(operationParameters) == 0 {
		return pathParameters
	}
	overridden := map[string]bool{}
	for _, parameter := range operationParameters {
		if parameter != nil && parameter.Value != nil {
			overridden[parameter.Value.In+":"+parameter.Value.Name] = true
		}
	}
	merged := make(openapi3.Parameters, 0, len(pathParameters)+len(operationParameters))
	for _, parameter := range pathParameters {
		if parameter != nil && parameter.Value != nil && !overridden[parameter.Value.In+":"+parameter.Value.Name] {
			merged = append(merged, parameter)
		}
	}
	return append(merged, operationParameters...)
}
