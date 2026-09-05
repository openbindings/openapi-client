package openapiclient

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

func openAPIInboundOperationInventory(doc *openapi3.T, artifact *Artifact) []InboundOperationDisposition {
	if artifact != nil && artifact.Edition.IsOpenAPI32() {
		return artifact.InboundOperationInventory()
	}
	return DocumentInboundOperationInventory(doc)
}

func synthesizeInboundDependencies(
	iface *ProjectionDocument,
	doc *openapi3.T,
	artifact *Artifact,
	bindingSpec, formatVersion string,
	usedOperationKeys map[string]bool,
	refRegistry map[string]any,
	requestGraph, responseGraph *directionGraph,
	namer *cutPointNamer,
	schemaOverlays *rawSchemaOverlayCollector,
	onUnrealizable func(unrealizableTarget),
) error {
	if iface == nil || bindingSpec == "projection:swagger-2.0-r1" {
		return nil
	}
	if iface.Dependencies == nil {
		iface.Dependencies = map[string]ProjectionDependency{}
	}
	usedDependencyKeys := map[string]bool{}
	for key := range iface.Dependencies {
		usedDependencyKeys[key] = true
	}

	for _, disposition := range openAPIInboundOperationInventory(doc, artifact) {
		if disposition.Err != nil || disposition.Target == nil || disposition.Target.Operation == nil {
			continue
		}
		target := disposition.Target
		opKey := deriveInboundOperationKey(target, usedOperationKeys)
		usedOperationKeys[opKey] = true

		obiOperation, err := projectInboundOperation(
			target, opKey, bindingSpec, formatVersion,
			refRegistry, requestGraph, responseGraph, namer, schemaOverlays,
		)
		if err != nil {
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector: target.Ref, operationKey: opKey,
					reasonCode: "openapi.inbound_dependency_excluded",
					rule:       openAPIRule(bindingSpec, "P-03"), message: err.Error(),
				})
				continue
			}
			return fmt.Errorf("cannot synthesize inbound OpenAPI operation at %q: %w", target.Ref, err)
		}

		dependencyKey := deriveInboundDependencyKey(target, usedDependencyKeys)
		usedDependencyKeys[dependencyKey] = true
		iface.Operations[opKey] = obiOperation
		iface.Dependencies[dependencyKey] = ProjectionDependency{Operation: opKey}
	}
	return nil
}

func deriveInboundOperationKey(target *InboundOperationTarget, used map[string]bool) string {
	if target.Operation.OperationID != "" {
		candidate := projectionSanitizeKey(target.Operation.OperationID)
		if !used[candidate] {
			return candidate
		}
	}
	return projectionUniqueKey(projectionSanitizeKey("inbound."+strings.TrimPrefix(target.Ref, "#/")), used)
}

func deriveInboundDependencyKey(target *InboundOperationTarget, used map[string]bool) string {
	prefix := string(target.Kind)
	return projectionUniqueKey(projectionSanitizeKey(prefix+"."+strings.TrimPrefix(target.Ref, "#/")), used)
}

func projectInboundOperation(
	target *InboundOperationTarget,
	opKey, bindingSpec, formatVersion string,
	refRegistry map[string]any,
	requestGraph, responseGraph *directionGraph,
	namer *cutPointNamer,
	schemaOverlays *rawSchemaOverlayCollector,
) (ProjectionOperation, error) {
	op := target.Operation
	params := effectiveParameters(target.PathItem, op)
	if duplicate := duplicateEffectiveParameterIdentity(params); duplicate != "" {
		return ProjectionOperation{}, fmt.Errorf("parameter identity %q is declared more than once", duplicate)
	}
	if parameter := malformedEffectiveParameterFor(params, bindingSpec); parameter != "" {
		return ProjectionOperation{}, fmt.Errorf("effective parameter %q violates the closed Parameter Object declaration list", parameter)
	}
	if parameter := unsupportedParameterContentFor(params, bindingSpec); parameter != "" {
		return ProjectionOperation{}, fmt.Errorf("parameter %q declares content with no faithful candidate carriage", parameter)
	}
	if member := styleLaneUndefinedExpansionParamFor(params, bindingSpec, isOpenAPI30(formatVersion)); member != "" {
		return ProjectionOperation{}, fmt.Errorf("parameter member %q has no expansion defined by its governing OAS style row", member)
	}
	if parameter := formStyleCookieMultiValueParamFor(params, isOpenAPI30(formatVersion)); parameter != "" {
		return ProjectionOperation{}, fmt.Errorf("cookie parameter %q statically proves multi-pair form expansion", parameter)
	}

	inputOperation := op
	if requestBodyIgnoredForBindingSpec(bindingSpec, target.Method) {
		copyOperation := *op
		copyOperation.RequestBody = nil
		inputOperation = &copyOperation
	}
	var requestPlans []*bodyPlan
	if inputOperation.RequestBody != nil && inputOperation.RequestBody.Value != nil {
		plans, planErr := planRequestBodiesFor(target.Document, op, bindingSpec)
		if planErr == nil {
			for _, plan := range plans {
				if usesRoutedInput(bindingSpec) || !candidateCollides(params, plan) {
					requestPlans = append(requestPlans, plan)
				}
			}
		}
		if inputOperation.RequestBody.Value.Required && (planErr != nil || len(requestPlans) == 0) {
			if planErr != nil {
				return ProjectionOperation{}, planErr
			}
			return ProjectionOperation{}, fmt.Errorf("required request body has no faithful candidate carriage")
		}
	}
	if formatVersion == "3.1" {
		if err := validateProjectedOperationDialects(target.Document, op, params, requestPlans, bindingSpec); err != nil {
			return ProjectionOperation{}, err
		}
	}

	obiOperation := ProjectionOperation{
		Description: operationDescription(op),
		Deprecated:  op.Deprecated,
	}
	if len(op.Tags) > 0 {
		obiOperation.Tags = append([]string(nil), op.Tags...)
	}

	opPointer := "#/operations/" + escapeJSONPointerSegment(opKey)
	routes := planAbstractInputRoutes(params, requestPlans)
	inputSchema := buildInputSchemaForPlans(inputOperation, params, requestPlans, requestGraph, routes, schemaOverlays)
	if inputSchema != nil {
		projected := projectOpenAPISchemaWithRegistry(inputSchema, openAPIRequestSchema, requestSchemaProjectionExemptions(routes), refRegistry)
		inlined, hoisted := inlineRefsInOperationSchema(projected, requestGraph.registry, requestGraph.cyclic, opPointer+"/input", namer)
		restored := restoreBooleanSchemas(pruneUnreachableDefs(inlined, opPointer+"/input", hoisted))
		if object, ok := restored.(map[string]any); ok {
			obiOperation.Input = translateSchemaDialect(object, formatVersion)
		} else {
			obiOperation.Input = restored
		}
	}

	outputSchema := buildOutputSchemaWithCyclicRefs(op, schemaOverlays, bindingSpec, responseGraph, target.Document.OpenAPI)
	if outputSchema != nil {
		projected := projectOpenAPISchemaWithRegistry(outputSchema, openAPIResponseSchema, nil, refRegistry)
		inlined, hoisted := inlineRefsInOperationSchema(projected, responseGraph.registry, responseGraph.cyclic, opPointer+"/output", namer)
		restored := restoreBooleanSchemas(pruneUnreachableDefs(inlined, opPointer+"/output", hoisted))
		if object, ok := restored.(map[string]any); ok {
			obiOperation.Output = translateSchemaDialect(object, formatVersion)
		} else {
			obiOperation.Output = restored
		}
	}
	return obiOperation, nil
}
