package openapiclient

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// InboundOperationKind identifies the OpenAPI declaration that owns an
// operation initiated toward the API consumer rather than through paths.
type InboundOperationKind string

const (
	InboundOperationCallback InboundOperationKind = "callback"
	InboundOperationWebhook  InboundOperationKind = "webhook"
)

// InboundOperationReference is the OpenAPI-native declaration slot for one
// callback or webhook operation. Ref is a stable source-local JSON Pointer;
// callback runtime expressions and additional-operation methods retain their
// authored spelling after pointer escaping.
type InboundOperationReference struct {
	Ref        string
	Kind       InboundOperationKind
	ParentRef  string
	Name       string
	Expression string
	Method     string
	Additional bool
}

// WireMethod returns the method exactly as the inbound operation sends it.
func (r InboundOperationReference) WireMethod() string {
	if r.Additional {
		return r.Method
	}
	return strings.ToUpper(r.Method)
}

// InboundOperationTarget pairs an inbound declaration slot with its resolved
// OpenAPI operation model. It deliberately carries no OpenBindings meaning.
type InboundOperationTarget struct {
	InboundOperationReference
	Document  *openapi3.T
	PathItem  *openapi3.PathItem
	Operation *openapi3.Operation
}

// InboundOperationDisposition retains either a resolved inbound operation or
// the declaration-local resolution failure that prevented one.
type InboundOperationDisposition struct {
	Reference InboundOperationReference
	Target    *InboundOperationTarget
	Err       error
}

// DocumentInboundOperationInventory inventories inbound operations from an
// already-loaded OpenAPI 3.0 or 3.1 document. OpenAPI 3.2 callers use the
// Artifact method so selected-operation confinement and the raw overlay remain
// authoritative.
func DocumentInboundOperationInventory(document *openapi3.T) []InboundOperationDisposition {
	if document == nil {
		return nil
	}
	return (&Artifact{Document: document, Edition: Edition(document.OpenAPI)}).InboundOperationInventory()
}

// InboundOperationInventory returns callback operations for OpenAPI 3.0+
// artifacts and root-webhook operations for OpenAPI 3.1+ artifacts. The walk
// includes callbacks nested under callback and webhook operations, terminates
// reference cycles, and preserves a disposition for an unresolved callback
// declaration rather than excluding its parent paths operation.
func (a *Artifact) InboundOperationInventory() []InboundOperationDisposition {
	if a == nil || a.Document == nil {
		return nil
	}

	var result []InboundOperationDisposition
	activeCallbacks := map[*openapi3.Callback]bool{}
	var walkOperation func(*openapi3.T, *openapi3.Operation, string)
	walkOperation = func(document *openapi3.T, operation *openapi3.Operation, parentRef string) {
		if operation == nil || len(operation.Callbacks) == 0 {
			return
		}
		names := make([]string, 0, len(operation.Callbacks))
		for name := range operation.Callbacks {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			callbackRef := operation.Callbacks[name]
			callbackBase := parentRef + "/callbacks/" + escapeJSONPointerSegment(name)
			if callbackRef == nil || callbackRef.Value == nil {
				result = append(result, InboundOperationDisposition{
					Reference: InboundOperationReference{Ref: callbackBase, Kind: InboundOperationCallback, ParentRef: parentRef, Name: name},
					Err:       fmt.Errorf("callback %q is unresolvable", name),
				})
				continue
			}
			if activeCallbacks[callbackRef.Value] {
				continue
			}
			activeCallbacks[callbackRef.Value] = true
			expressions := make([]string, 0, callbackRef.Value.Len())
			for expression := range callbackRef.Value.Map() {
				expressions = append(expressions, expression)
			}
			sort.Strings(expressions)
			for _, expression := range expressions {
				pathItem := callbackRef.Value.Value(expression)
				expressionRef := callbackBase + "/" + escapeJSONPointerSegment(expression)
				result = appendInboundPathItem(result, document, pathItem, expressionRef, InboundOperationCallback, parentRef, name, expression, a.Edition, walkOperation)
			}
			delete(activeCallbacks, callbackRef.Value)
		}
	}

	for _, disposition := range a.OperationInventory() {
		if disposition.Target != nil {
			walkOperation(disposition.Target.Document, disposition.Target.Operation, disposition.Reference.Ref)
		}
	}

	if !a.Edition.IsOpenAPI32() && !strings.HasPrefix(string(a.Edition), "3.1.") {
		return result
	}
	webhookNames := make([]string, 0, len(a.Document.Webhooks))
	for name := range a.Document.Webhooks {
		webhookNames = append(webhookNames, name)
	}
	sort.Strings(webhookNames)
	for _, name := range webhookNames {
		pathItem := a.Document.Webhooks[name]
		base := "#/webhooks/" + escapeJSONPointerSegment(name)
		result = appendInboundPathItem(result, a.Document, pathItem, base, InboundOperationWebhook, "", name, "", a.Edition, walkOperation)
	}
	return result
}

func appendInboundPathItem(
	result []InboundOperationDisposition,
	document *openapi3.T,
	pathItem *openapi3.PathItem,
	base string,
	kind InboundOperationKind,
	parentRef, name, expression string,
	edition Edition,
	walkOperation func(*openapi3.T, *openapi3.Operation, string),
) []InboundOperationDisposition {
	if pathItem == nil {
		return append(result, InboundOperationDisposition{
			Reference: InboundOperationReference{Ref: base, Kind: kind, ParentRef: parentRef, Name: name, Expression: expression},
			Err:       fmt.Errorf("inbound Path Item at %q is unresolvable", base),
		})
	}
	methods := httpMethods
	if edition.IsOpenAPI32() {
		methods = openAPI32FixedMethods
	}
	for _, method := range methods {
		operation := pathItem.GetOperation(strings.ToUpper(method))
		if method == "query" {
			operation = pathItem.Query
		}
		if operation == nil {
			continue
		}
		ref := base + "/" + method
		reference := InboundOperationReference{Ref: ref, Kind: kind, ParentRef: parentRef, Name: name, Expression: expression, Method: method}
		result = append(result, InboundOperationDisposition{Reference: reference, Target: &InboundOperationTarget{
			InboundOperationReference: reference, Document: document, PathItem: pathItem, Operation: operation,
		}})
		walkOperation(document, operation, ref)
	}
	if edition.IsOpenAPI32() {
		additional := make([]string, 0, len(pathItem.AdditionalOperations))
		for method := range pathItem.AdditionalOperations {
			additional = append(additional, method)
		}
		sort.Strings(additional)
		for _, method := range additional {
			operation := pathItem.AdditionalOperations[method]
			if operation == nil {
				continue
			}
			ref := base + "/additionalOperations/" + escapeJSONPointerSegment(method)
			reference := InboundOperationReference{Ref: ref, Kind: kind, ParentRef: parentRef, Name: name, Expression: expression, Method: method, Additional: true}
			result = append(result, InboundOperationDisposition{Reference: reference, Target: &InboundOperationTarget{
				InboundOperationReference: reference, Document: document, PathItem: pathItem, Operation: operation,
			}})
			walkOperation(document, operation, ref)
		}
	}
	return result
}
