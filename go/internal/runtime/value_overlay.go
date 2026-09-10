package openapiclient

import (
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

const valueOverlayMarker = "x-openapi-client-internal-value-overlay"

// Retain opaque application values before the typed loader can narrow them.
// Callers identify OAS object positions; this never interprets property names
// inside instance data as OpenAPI fields or follows a reference itself.
func (n *rawRefSiblingNormalizer) markValueObject(object map[string]any, fields ...string) bool {
	c := n.schemaOverlays
	if c == nil || object == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id, _ := object[valueOverlayMarker].(string)
	retained, owned := c.pending[id]
	values := map[string]any{}
	for key, value := range object {
		if owned && key == valueOverlayMarker {
			continue
		}
		if strings.HasPrefix(strings.ToLower(key), "x-") && !existingProcessorMarker(key) {
			values[key] = value
		}
	}
	for _, key := range fields {
		if value, present := object[key]; present {
			values[key] = value
		}
	}
	if len(values) == 0 {
		return false
	}
	if !owned {
		c.next++
		id = c.namespace + "-" + strconv.FormatUint(c.next, 10)
		retained = map[string]any{}
	}
	for key, value := range values {
		retained[key] = value
	}
	c.pending[id] = retained
	for key := range values {
		delete(object, key)
	}
	object[valueOverlayMarker] = id
	return true
}

// These named markers already have processor-owned semantics. An arbitrary
// lookalike prefix does not confer authority or exempt application data.
func existingProcessorMarker(key string) bool {
	switch key {
	case schemaOverlayMarker, referenceMetadataMarker, serverDocumentMarker,
		serverVariableDefaultMarker, serverVariableEnumMarker, serverVariableInvalidMarker, referringSecurityMarker:
		return true
	}
	return false
}

func (n *rawRefSiblingNormalizer) markValueServer(value any) bool {
	server, _ := value.(map[string]any)
	if server == nil {
		return false
	}
	changed := n.markValueMetadata(server)
	for _, variable := range rawMapValues(server["variables"]) {
		if object, ok := variable.(map[string]any); ok {
			changed = n.markValueObject(object) || changed
		}
	}
	return changed
}

func (n *rawRefSiblingNormalizer) markValueMetadata(object map[string]any) bool {
	changed := n.markValueObject(object)
	for _, key := range []string{"info", "contact", "license", "externalDocs"} {
		if child, ok := object[key].(map[string]any); ok {
			changed = n.markValueMetadata(child) || changed
		}
	}
	for _, key := range []string{"servers", "tags"} {
		if children, ok := object[key].([]any); ok {
			for _, child := range children {
				if item, ok := child.(map[string]any); ok {
					if key == "servers" {
						changed = n.markValueServer(item) || changed
					} else {
						changed = n.markValueMetadata(item) || changed
					}
				}
			}
		}
	}
	return changed
}

func (n *rawRefSiblingNormalizer) normalizeExamples(value any, semantics rawRefSemantics, base *url.URL) (bool, error) {
	changed := false
	for _, item := range rawMapValues(value) {
		c, err := n.normalizeTarget(item, rawExampleTarget, semantics, base)
		if err != nil {
			return false, err
		}
		changed = c || changed
	}
	return changed, nil
}

// The existing loader has already resolved the graph. This pass restores only
// owned correlation markers into public value fields; it does not resolve,
// evaluate, or reinterpret any OpenAPI object. Physical graph traversal is
// cycle-safe, including the three backend containers with private member maps.
func (c *rawSchemaOverlayCollector) restoreValueGraph(doc *openapi3.T) {
	seen := map[reflect.Value]bool{}
	var visit func(reflect.Value)
	visit = func(value reflect.Value) {
		if !value.IsValid() || !value.CanInterface() {
			return
		}
		switch value.Kind() {
		case reflect.Interface:
			if !value.IsNil() {
				visit(value.Elem())
			}
		case reflect.Pointer:
			if value.IsNil() || seen[value] {
				return
			}
			seen[value] = true
			switch v := value.Interface().(type) {
			case *openapi3.Paths:
				for _, member := range v.Map() {
					visit(reflect.ValueOf(member))
				}
			case *openapi3.Responses:
				for _, member := range v.Map() {
					visit(reflect.ValueOf(member))
				}
			case *openapi3.Callback:
				for _, member := range v.Map() {
					visit(reflect.ValueOf(member))
				}
			}
			visit(value.Elem())
		case reflect.Struct:
			extensionField := value.FieldByName("Extensions")
			if extensionField.IsValid() && extensionField.CanInterface() {
				if extensions, ok := extensionField.Interface().(map[string]any); ok {
					if id, ok := extensions[valueOverlayMarker].(string); ok {
						c.mu.Lock()
						overlay, owned := c.pending[id]
						if owned {
							delete(c.pending, id)
						}
						c.mu.Unlock()
						if owned {
							delete(extensions, valueOverlayMarker)
							for key, member := range overlay {
								// Retain authored null/presence even when the dependency's
								// typed any field marshaler treats nil as absent.
								extensions[key] = member
								if strings.HasPrefix(strings.ToLower(key), "x-") {
									continue
								}
								for i := 0; i < value.NumField(); i++ {
									field := value.Type().Field(i)
									if strings.Split(field.Tag.Get("json"), ",")[0] != key {
										continue
									}
									target := value.Field(i)
									if !target.CanSet() {
										continue
									}
									if member == nil {
										target.SetZero()
										continue
									}
									exact := reflect.ValueOf(member)
									if exact.Type().AssignableTo(target.Type()) {
										target.Set(exact)
									}
								}
							}
						}
					}
				}
			}
			for i := 0; i < value.NumField(); i++ {
				if value.Type().Field(i).IsExported() {
					visit(value.Field(i))
				}
			}
		case reflect.Map:
			if value.IsNil() || seen[value] {
				return
			}
			seen[value] = true
			iter := value.MapRange()
			for iter.Next() {
				visit(iter.Value())
			}
		case reflect.Slice:
			if value.IsNil() || seen[value] {
				return
			}
			seen[value] = true
			for i := 0; i < value.Len(); i++ {
				visit(value.Index(i))
			}
		}
	}
	visit(reflect.ValueOf(doc))
}
