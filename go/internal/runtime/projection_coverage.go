package openapiclient

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

type synthesisBindingIdentity struct {
	operationKey string
	selector     string
}

// openAPISynthesisCoverage inventories the interaction units that matter to
// OpenAPI synthesis fidelity in revision 1:
//   - every client-invoked paths operation;
//   - every independently selectable request media declaration;
//   - unusable server/security alternatives whose smallest-owner exclusions
//     must remain visible beside a represented sibling;
//   - callbacks and 3.1 webhooks, which are upstream interactions but are
//     explicitly outside this revision's caller-to-service direction.
//
// Incorporated parameter serialization and response selection are behavior of
// a represented target, not independently addressable units.
func openAPISynthesisCoverage(doc *openapi3.T, artifact *Artifact, iface *ProjectionDocument, unrealizable map[string]unrealizableTarget, floor *acceptanceFloor, sourceLocation, bindingSpec string) []ProjectionCoverageEntry {
	if doc == nil || iface == nil {
		return []ProjectionCoverageEntry{}
	}

	bySelector := make(map[string]synthesisBindingIdentity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		bySelector[binding.Selector] = synthesisBindingIdentity{operationKey: binding.Operation, selector: binding.Selector}
	}
	if artifact != nil && artifact.Edition.IsOpenAPI32() {
		return openAPI32SynthesisCoverage(artifact, iface, bySelector, unrealizable, floor, sourceLocation, bindingSpec)
	}

	// The walk is driven from the UNION of the loaded document's path×method
	// inventory and the acceptance floor's raw-tree inventory (block 8d
	// design §3): a ladder-invalid operation may be absent from the loaded
	// document (confined) or present (a loadable defect); either way its
	// invalid entry is owed. Deterministic order: sorted paths × the
	// httpMethods order.
	pathSet := map[string]bool{}
	if doc.Paths != nil {
		for path := range doc.Paths.Map() {
			pathSet[path] = true
		}
	}
	if floor != nil {
		for _, ref := range floor.OpOrder {
			pathSet[floor.Ops[ref].Path] = true
		}
	}

	var entries []ProjectionCoverageEntry
	{
		pathKeys := make([]string, 0, len(pathSet))
		for path := range pathSet {
			pathKeys = append(pathKeys, path)
		}
		sort.Strings(pathKeys)
		for _, path := range pathKeys {
			var pathItem *openapi3.PathItem
			if doc.Paths != nil {
				pathItem = doc.Paths.Find(path)
			}
			for _, method := range httpMethods {
				var op *openapi3.Operation
				if pathItem != nil {
					op = pathItem.GetOperation(strings.ToUpper(method))
				}
				selector := buildJSONPointerSelector(path, method)
				verdict := floor.opVerdict(selector)
				if op == nil && verdict == nil {
					continue
				}
				if verdict != nil && verdict.Disposition == "invalid" {
					// A ladder-invalid target: one invalid target entry
					// carrying the owning unit and its defects, then the
					// operation's projection entries.
					entries = append(entries, ProjectionCoverageEntry{
						SourceIndex: 0,
						SourceRef:   selector,
						Scope:       "target",
						Status:      "invalid",
						ReasonCode:  invalidUnitReasonCode,
						Message:     floorInvalidTargetMessage(len(verdict.Defects)),
						Details:     map[string]any{"defects": floorDefectDetails(verdict.Defects)},
					})
					entries = append(entries, floorProjectionEntries(verdict)...)
					continue
				}
				if op == nil {
					// Raw-inventory-only and not ladder-invalid: nothing the
					// loaded document can account further (a confined or
					// unloadable position without a ladder verdict).
					continue
				}
				identity, ok := bySelector[selector]
				if !ok {
					// Tolerant synthesis skipped this operation with a
					// recorded, spec-governed reason: a per-operation
					// exclusion, not an implementation defect. Anything else
					// genuinely missing remains an implementation invariant
					// violation.
					if skipped, recorded := unrealizable[selector]; recorded {
						status := skipped.status
						if status == "" {
							status = "excluded"
						}
						entries = append(entries, ProjectionCoverageEntry{
							SourceIndex: 0,
							SourceRef:   selector,
							Scope:       "target",
							Status:      status,
							ReasonCode:  skipped.reasonCode,
							Rule:        skipped.rule,
							Message:     skipped.message,
						})
						// An excluded target is still ADDRESSED; its
						// ladder-invalid request media alternatives and
						// projection entries are owed regardless.
						entries = append(entries, floorInvalidAlternativeEntries(verdict)...)
						entries = append(entries, floorProjectionEntries(verdict)...)
						continue
					}
					entries = append(entries, ProjectionCoverageEntry{
						SourceIndex: 0,
						SourceRef:   selector,
						Scope:       "target",
						Status:      "implementation-unsupported",
						ReasonCode:  "openapi.missing_emitted_binding",
						Message:     "the synthesizer returned without emitting this admitted paths operation",
					})
					continue
				}
				allParameters := declaredEffectiveParameters(pathItem, op)
				confinedParameters := confineEffectiveParameters(allParameters, bindingSpec).parameters
				targetRequirements := openAPIServerRequirements(doc, pathItem, op, sourceLocation)
				targetRequirements = append(targetRequirements, openAPISecurityRequirements(doc, op, confinedParameters, method == "trace")...)
				if !requestBodyIgnoredForBindingSpec(bindingSpec, method) {
					targetRequirements = append(targetRequirements, openAPIRequestMediaRequirements(doc, pathItem, op, bindingSpec)...)
				}
				entries = append(entries, ProjectionCoverageEntry{
					SourceIndex:     0,
					SourceRef:       selector,
					Scope:           "target",
					Status:          "represented",
					OperationKey:    identity.operationKey,
					BindingSelector: identity.selector,
					Requirements:    targetRequirements,
				})
				entries = append(entries, openAPIServerAlternativeCoverage(doc, pathItem, op, selector, identity.operationKey, bindingSpec, sourceLocation)...)
				entries = append(entries, openAPISecurityAlternativeCoverage(doc, op, confinedParameters, selector, identity.operationKey, bindingSpec, method == "trace")...)
				if !requestBodyIgnoredForBindingSpec(bindingSpec, method) {
					entries = append(entries, openAPIRequestMediaCoverage(doc, op, pathItem, identity, bindingSpec, verdict)...)
				}
				responseEntries := openAPIResponseConfinementCoverage(op, identity, bindingSpec)
				entries = append(entries, responseEntries...)
				entries = append(entries, openAPIParameterConfinementCoverage(pathItem, op, selector, bindingSpec)...)
				entries = append(entries, floorProjectionEntriesExceptResponse(verdict, responseEntries)...)
			}
		}
	}
	entries = append(entries, openAPIInboundDependencyCoverage(doc, nil, iface, unrealizable, bindingSpec)...)
	return entries
}

func openAPIParameterConfinementCoverage(pathItem *openapi3.PathItem, op *openapi3.Operation, selector, bindingSpec string) []ProjectionCoverageEntry {
	if pathItem == nil || op == nil {
		return nil
	}
	rows := declaredEffectiveParameters(pathItem, op)
	var entries []ProjectionCoverageEntry
	for _, ref := range rows {
		if ref == nil || ref.Value == nil {
			continue
		}
		parameter := ref.Value
		invalidHeader := parameter.In == openapi3.ParameterInHeader && !projectionHTTPFieldName.MatchString(parameter.Name)
		invalidCookie := parameter.In == openapi3.ParameterInCookie && !projectionHTTPFieldName.MatchString(parameter.Name)
		if (!invalidHeader && !invalidCookie) || parameter.Required {
			continue
		}
		sourceRef := selector
		found := false
		for index, direct := range op.Parameters {
			if direct == ref || direct != nil && direct.Value == parameter {
				sourceRef += fmt.Sprintf("/parameters/%d", index)
				found = true
				break
			}
		}
		if !found {
			pathSelector := selector[:strings.LastIndex(selector, "/")]
			for index, inherited := range pathItem.Parameters {
				if inherited == ref || inherited != nil && inherited.Value == parameter {
					sourceRef = fmt.Sprintf("%s/parameters/%d", pathSelector, index)
					break
				}
			}
		}
		sameNameSurvivor := false
		for _, candidateRef := range rows {
			if candidateRef == nil || candidateRef.Value == nil || candidateRef.Value == parameter || candidateRef.Value.Name != parameter.Name {
				continue
			}
			candidate := candidateRef.Value
			if candidate.In != openapi3.ParameterInHeader || projectionHTTPFieldName.MatchString(candidate.Name) {
				sameNameSurvivor = true
			}
		}
		rule := "P-15"
		if invalidCookie {
			rule = "S-04"
		} else if sameNameSurvivor {
			rule = "S-17"
			if bindingSpec == projectionOpenAPI30 {
				rule = "S-18"
			}
		} else if bindingSpec == projectionOpenAPI30 {
			rule = "P-14"
		}
		entries = append(entries, ProjectionCoverageEntry{
			SourceIndex: 0, SourceRef: sourceRef, Scope: "projection",
			Status: "excluded", ReasonCode: "openapi.parameter_projection_excluded",
			Rule: openAPIRule(bindingSpec, rule), Message: fmt.Sprintf("%s parameter name %q cannot be emitted safely", parameter.In, parameter.Name),
		})
	}
	return entries
}

func openAPI32SynthesisCoverage(
	artifact *Artifact,
	iface *ProjectionDocument,
	bySelector map[string]synthesisBindingIdentity,
	unrealizable map[string]unrealizableTarget,
	floor *acceptanceFloor,
	sourceLocation, bindingSpec string,
) []ProjectionCoverageEntry {
	var entries []ProjectionCoverageEntry
	for _, disposition := range artifact.OperationInventory() {
		selector := disposition.Reference.Ref
		verdict := floor.opVerdict(selector)
		if verdict != nil && verdict.Disposition == "invalid" {
			rule := ""
			if operationHasEmptyResponses(artifact.Document, disposition.Reference.Path, disposition.Reference.Method) {
				rule = openAPIRule(bindingSpec, "P-05")
			}
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0, SourceRef: selector, Scope: "target",
				Status: "invalid", ReasonCode: invalidUnitReasonCode, Rule: rule,
				Message: floorInvalidTargetMessage(len(verdict.Defects)), Details: map[string]any{"defects": floorDefectDetails(verdict.Defects)},
			})
			entries = append(entries, floorProjectionEntries(verdict)...)
			continue
		}
		identity, represented := bySelector[selector]
		if disposition.Err != nil || !represented {
			if skipped, recorded := unrealizable[selector]; recorded {
				status := skipped.status
				if status == "" {
					status = "excluded"
				}
				entries = append(entries, ProjectionCoverageEntry{
					SourceIndex: 0, SourceRef: selector, Scope: "target",
					Status: status, ReasonCode: skipped.reasonCode, Rule: skipped.rule, Message: skipped.message,
				})
			} else if disposition.Err != nil {
				status := "excluded"
				rule := openAPIRule(bindingSpec, "P-01")
				var resolution *OperationResolutionError
				if errors.As(disposition.Err, &resolution) && resolution.Kind == OperationTargetInvalid {
					status = "invalid"
					rule = ""
					if strings.Contains(disposition.Err.Error(), "empty Responses Object") {
						rule = openAPIRule(bindingSpec, "P-05")
					}
				} else if disposition.Reference.Additional && disposition.Reference.Method == "CONNECT" {
					rule = openAPIRule(bindingSpec, "P-07")
				} else if strings.Contains(strings.ToLower(disposition.Err.Error()), "required cookie parameter") {
					rule = openAPIRule(bindingSpec, "S-04")
				} else if strings.Contains(strings.ToLower(disposition.Err.Error()), "http field-name") || strings.Contains(strings.ToLower(disposition.Err.Error()), "http token destination") {
					rule = openAPIRule(bindingSpec, "P-17")
				} else if strings.Contains(strings.ToLower(disposition.Err.Error()), "processor-owned") {
					rule = openAPIRule(bindingSpec, "P-18")
				}
				entries = append(entries, ProjectionCoverageEntry{
					SourceIndex: 0, SourceRef: selector, Scope: "target",
					Status: status, ReasonCode: "openapi.target_excluded", Rule: rule, Message: disposition.Err.Error(),
				})
			} else {
				entries = append(entries, ProjectionCoverageEntry{
					SourceIndex: 0, SourceRef: selector, Scope: "target",
					Status: "implementation-unsupported", ReasonCode: "openapi.missing_emitted_binding",
					Message: "the synthesizer returned without emitting this admitted paths operation",
				})
			}
			entries = append(entries, floorInvalidAlternativeEntries(verdict)...)
			entries = append(entries, floorProjectionEntries(verdict)...)
			continue
		}
		target := disposition.Target
		if target == nil {
			continue
		}
		operation := target.Operation
		if len(target.ReferringSecuritySchemes) > 0 {
			copyOperation := *operation
			copyOperation.Extensions = make(map[string]any, len(operation.Extensions)+1)
			for name, value := range operation.Extensions {
				copyOperation.Extensions[name] = value
			}
			copyOperation.Extensions[referringSecuritySchemesMarker] = target.ReferringSecuritySchemes
			operation = &copyOperation
		}
		allParameters := declaredEffectiveParameters(target.PathItem, operation)
		params := confineEffectiveParameters(allParameters, bindingSpec).parameters
		requirements := openAPIServerRequirements(target.Document, target.PathItem, operation, sourceLocation)
		isTrace := !disposition.Reference.Additional && disposition.Reference.Method == "trace"
		requirements = append(requirements, openAPISecurityRequirements(target.Document, operation, params, isTrace)...)
		if !requestBodyIgnoredForBindingSpec(bindingSpec, disposition.Reference.Method) {
			requirements = append(requirements, openAPIRequestMediaRequirements(target.Document, target.PathItem, operation, bindingSpec)...)
		}
		entries = append(entries, ProjectionCoverageEntry{
			SourceIndex: 0, SourceRef: selector, Scope: "target",
			Status: "represented", OperationKey: identity.operationKey,
			BindingSelector: identity.selector, Requirements: requirements,
		})
		entries = append(entries, openAPIServerAlternativeCoverage(target.Document, target.PathItem, operation, selector, identity.operationKey, bindingSpec, sourceLocation)...)
		entries = append(entries, openAPISecurityAlternativeCoverage(target.Document, operation, params, selector, identity.operationKey, bindingSpec, isTrace)...)
		if !requestBodyIgnoredForBindingSpec(bindingSpec, disposition.Reference.Method) {
			entries = append(entries, openAPIRequestMediaCoverage(target.Document, operation, target.PathItem, identity, bindingSpec, verdict)...)
		}
		responseEntries := openAPIResponseConfinementCoverage(operation, identity, bindingSpec)
		entries = append(entries, responseEntries...)
		entries = append(entries, openAPIParameterConfinementCoverage(target.PathItem, operation, selector, bindingSpec)...)
		for _, exclusion := range target.ResponseMediaExclusions {
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0,
				SourceRef:   selector + "/responses/" + escapeJSONPointerToken(exclusion.ResponseKey) + "/content/" + escapeJSONPointerToken(exclusion.MediaType),
				Scope:       "alternative", Status: "invalid",
				ReasonCode: "openapi.response_media_excluded", Rule: openAPIRule(bindingSpec, "P-01"), Message: exclusion.Reason,
				Details: map[string]any{"mediaType": exclusion.MediaType, "responseKey": exclusion.ResponseKey},
			})
		}
		entries = append(entries, floorProjectionEntriesExceptResponse(verdict, responseEntries)...)
	}
	entries = append(entries, openAPIInboundDependencyCoverage(artifact.Document, artifact, iface, unrealizable, bindingSpec)...)
	return entries
}

func operationHasEmptyResponses(doc *openapi3.T, path, method string) bool {
	if doc == nil || doc.Paths == nil {
		return false
	}
	pathItem := doc.Paths.Find(path)
	if pathItem == nil {
		return false
	}
	operation := pathItem.GetOperation(strings.ToUpper(method))
	return operation != nil && operation.Responses != nil && operation.Responses.Len() == 0
}

func openAPIResponseConfinementCoverage(op *openapi3.Operation, identity synthesisBindingIdentity, bindingSpec string) []ProjectionCoverageEntry {
	if op == nil || op.Responses == nil {
		return nil
	}
	var entries []ProjectionCoverageEntry
	for responseKey, responseRef := range op.Responses.Map() {
		if strings.HasPrefix(responseKey, "x-") {
			continue
		}
		responseSourceRef := identity.selector + "/responses/" + escapeJSONPointerToken(responseKey)
		response := (*openapi3.Response)(nil)
		if responseRef != nil {
			response = responseRef.Value
		}
		successKey := len(responseKey) == 3 && responseKey[0] == '2' || responseKey == "2XX"
		if response == nil || response.Description == nil && !successKey {
			rule := "P-11"
			if bindingSpec == projectionOpenAPI30 {
				rule = "P-10"
			}
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0, SourceRef: responseSourceRef, Scope: "projection",
				Status: "invalid", ReasonCode: "openapi.response_projection_invalid",
				Rule: openAPIRule(bindingSpec, rule), Message: "failure response projection is malformed and contributes no failure data",
			})
		}
		if response != nil && (responseKey == "204" || responseKey == "205" || responseKey == "304") && response.Content != nil {
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0, SourceRef: responseSourceRef + "/content", Scope: "projection",
				Status: "excluded", ReasonCode: "openapi.no_content_projection_excluded",
				Rule: openAPIRule(bindingSpec, "S-03"), Message: responseKey + " never projects response content",
			})
		}
		if response != nil && responseKey == "default" && len(op.Responses.Map()) == 1 {
			mediaNames := make([]string, 0, len(response.Content))
			for mediaType := range response.Content {
				mediaNames = append(mediaNames, mediaType)
			}
			sort.Strings(mediaNames)
			for _, mediaType := range mediaNames {
				media := response.Content[mediaType]
				if media == nil || media.Schema == nil {
					continue
				}
				entries = append(entries, ProjectionCoverageEntry{
					SourceIndex: 0,
					SourceRef:   responseSourceRef + "/content/" + escapeJSONPointerToken(mediaType) + "/schema",
					Scope:       "projection", Status: "represented",
					OperationKey: identity.operationKey, BindingSelector: identity.selector,
				})
			}
		}
		if response != nil {
			entries = append(entries, openAPIResponseMediaParameterCoverage(response, responseSourceRef, identity, bindingSpec)...)
			entries = append(entries, openAPIResponseHeaderGroupCoverage(response, responseSourceRef, identity, bindingSpec)...)
			headerNames := make([]string, 0, len(response.Headers))
			for name := range response.Headers {
				headerNames = append(headerNames, name)
			}
			sort.Strings(headerNames)
			for _, name := range headerNames {
				if projectionHTTPFieldName.MatchString(name) {
					continue
				}
				headerRef := response.Headers[name]
				required := headerRef != nil && headerRef.Value != nil && headerRef.Value.Required
				entry := ProjectionCoverageEntry{
					SourceIndex: 0, SourceRef: responseSourceRef + "/headers/" + escapeJSONPointerToken(name),
					Scope: "projection", Status: "excluded",
					ReasonCode: "openapi.response_header_excluded", Rule: openAPIRule(bindingSpec, "S-04"),
					Message: fmt.Sprintf("response header name %q is not an HTTP field-name", name),
				}
				if required {
					entry.SourceRef = responseSourceRef
					entry.Scope = "alternative"
					entry.ReasonCode = "openapi.response_alternative_excluded"
					entry.Message = fmt.Sprintf("required response header name %q cannot be observed on the wire", name)
				}
				entries = append(entries, entry)
			}
		}
	}
	return entries
}

func openAPIResponseMediaParameterCoverage(response *openapi3.Response, responseSourceRef string, identity synthesisBindingIdentity, bindingSpec string) []ProjectionCoverageEntry {
	hasQ := false
	for mediaType := range response.Content {
		hasQ = hasQ || mediaTypeHasQParameter(mediaType)
	}
	if !hasQ {
		return nil
	}
	mediaTypes := make([]string, 0, len(response.Content))
	for mediaType := range response.Content {
		mediaTypes = append(mediaTypes, mediaType)
	}
	sort.Strings(mediaTypes)
	entries := make([]ProjectionCoverageEntry, 0, len(mediaTypes))
	for _, mediaType := range mediaTypes {
		entry := ProjectionCoverageEntry{
			SourceIndex: 0, SourceRef: responseSourceRef + "/content/" + escapeJSONPointerToken(mediaType),
			Scope: "alternative", Status: "represented",
			OperationKey: identity.operationKey, BindingSelector: identity.selector,
		}
		if mediaTypeHasQParameter(mediaType) {
			rule := "S-19"
			if bindingSpec == projectionOpenAPI30 {
				rule = "S-20"
			}
			entry.Status = "excluded"
			entry.OperationKey, entry.BindingSelector = "", ""
			entry.ReasonCode = "openapi.response_media_q_excluded"
			entry.Rule = openAPIRule(bindingSpec, rule)
			entry.Message = "response media declaration carries reserved q parameter"
		}
		entries = append(entries, entry)
	}
	return entries
}

func mediaTypeHasQParameter(mediaType string) bool {
	parts := strings.Split(mediaType, ";")
	for _, part := range parts[1:] {
		name, _, found := strings.Cut(part, "=")
		if found && strings.EqualFold(strings.TrimSpace(name), "q") {
			return true
		}
	}
	return false
}

func openAPIResponseHeaderGroupCoverage(response *openapi3.Response, responseSourceRef string, identity synthesisBindingIdentity, bindingSpec string) []ProjectionCoverageEntry {
	groups := map[string][]*openapi3.Header{}
	for name, ref := range response.Headers {
		if strings.EqualFold(name, "Content-Type") || ref == nil || ref.Value == nil || !projectionHTTPFieldName.MatchString(name) {
			continue
		}
		groups[strings.ToLower(name)] = append(groups[strings.ToLower(name)], ref.Value)
	}
	keys := make([]string, 0, len(groups))
	for key, members := range groups {
		if len(members) > 1 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var entries []ProjectionCoverageEntry
	for _, key := range keys {
		members := groups[key]
		required := false
		var intersection map[string]bool
		domainCount := 0
		for _, header := range members {
			required = required || header.Required
			if key != "content-encoding" {
				continue
			}
			domain, understood := exactHeaderStringDomain(header.Schema, bindingSpec == projectionOpenAPI30)
			if !understood {
				continue
			}
			domainCount++
			if intersection == nil {
				intersection = domain
				continue
			}
			for value := range intersection {
				if !domain[value] {
					delete(intersection, value)
				}
			}
		}
		rule := "P-31"
		if bindingSpec == projectionOpenAPI30 {
			rule = "P-30"
		}
		entry := ProjectionCoverageEntry{
			SourceIndex: 0, SourceRef: responseSourceRef, Scope: "alternative",
			Status: "represented", OperationKey: identity.operationKey, BindingSelector: identity.selector,
		}
		if key == "content-encoding" && required && domainCount > 0 && len(intersection) == 0 {
			entry.Status = "excluded"
			entry.OperationKey, entry.BindingSelector = "", ""
			entry.ReasonCode = "openapi.response_header_group_excluded"
			entry.Rule = openAPIRule(bindingSpec, rule)
			entry.Message = "required case-folded Content-Encoding group has no common exact string value"
		}
		entries = append(entries, entry)
	}
	return entries
}

func floorProjectionEntriesExceptResponse(verdict *floorOp, responseEntries []ProjectionCoverageEntry) []ProjectionCoverageEntry {
	entries := floorProjectionEntries(verdict)
	if verdict == nil {
		return entries
	}
	hasResponseProjection := false
	for _, entry := range responseEntries {
		hasResponseProjection = hasResponseProjection || entry.Scope == "projection"
	}
	if !hasResponseProjection {
		return entries
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.SourceRef != verdict.Ref || entry.Scope != "projection" {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func openAPIRequestMediaRequirements(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, bindingSpec string) []string {
	if !hasMediaFidelity(bindingSpec) || op == nil || op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
		return nil
	}
	plans, err := planRequestBodiesFor(doc, op, bindingSpec)
	if err != nil {
		return nil
	}
	if len(plans) == 1 && len(plans[0].propertyMedia) > 0 {
		// Retain the corpus's established synthesis requirement label while
		// runtime configuration uses the family specification's propertyMedia
		// point.
		return []string{"configuration.propertyMedia"}
	}
	params := declaredEffectiveParameters(pathItem, op)
	hasRange := false
	for _, plan := range plans {
		if !usesRoutedInput(bindingSpec) && candidateCollides(params, plan) {
			continue
		}
		if plan.mediaRange {
			hasRange = true
			continue
		}
		return nil
	}
	if hasRange {
		return []string{"configuration.requestMedia"}
	}
	return nil
}

func openAPIServerRequirements(
	doc *openapi3.T,
	pathItem *openapi3.PathItem,
	op *openapi3.Operation,
	sourceLocation string,
) []string {
	if sourceLocation == "" && (op == nil || op.Servers == nil || len(*op.Servers) == 0) &&
		(pathItem == nil || len(pathItem.Servers) == 0) && (doc == nil || len(doc.Servers) == 0) {
		return []string{"configuration.server"}
	}
	if _, err := resolveServer(doc, pathItem, op, nil, sourceLocation); err != nil {
		var required *configRequired
		if !errors.As(err, &required) {
			return nil
		}
		return []string{"configuration.server"}
	}
	return nil
}

func openAPISecurityRequirements(doc *openapi3.T, op *openapi3.Operation, params openapi3.Parameters, isTrace bool) []string {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	var result []string
	viableAlternatives := 0
	addressableAlternatives := 0
	for index := range *requirements {
		status := classifySecurityAlternative(doc, op, params, index, isTrace).status
		if status == "represented" {
			viableAlternatives++
		}
		if status != "excluded" {
			addressableAlternatives++
		}
	}
	if doc != nil && doc.OpenAPI == "3.2.0" && addressableAlternatives > 1 || doc != nil && doc.OpenAPI != "3.2.0" && viableAlternatives > 1 {
		result = append(result, "configuration.security")
	}
	entryPlans := viableSecurityPlansWithContext(doc, op, contextWithConfigurationPoint(nil, "implicitConnectionScope", "entry"), "", params)
	referringPlans := viableSecurityPlansWithContext(doc, op, contextWithConfigurationPoint(nil, "implicitConnectionScope", "referring"), "", params)
	if len(entryPlans) == 0 && len(referringPlans) > 0 {
		result = append(result, "configuration.implicitConnectionScope")
	}
	return result
}

func openAPIServerAlternativeCoverage(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, selector, operationKey, bindingSpec, sourceLocation string) []ProjectionCoverageEntry {
	servers, prefix := effectiveServerDeclaration(doc, pathItem, op, selector)
	if len(servers) == 0 {
		return nil
	}
	entries := make([]ProjectionCoverageEntry, 0, len(servers))
	classifications := make([]*serverAlternativeClassification, len(servers))
	hasSurvivingAlternative := false
	for index, server := range servers {
		classifications[index] = classifyServerAlternative(server, doc.OpenAPI)
		hasSurvivingAlternative = hasSurvivingAlternative || classifications[index] == nil
	}
	for index := range servers {
		classification := classifications[index]
		if classification == nil {
			continue
		}
		entry := ProjectionCoverageEntry{
			SourceIndex: 0, SourceRef: fmt.Sprintf("%s/%d", prefix, index), Scope: "alternative",
			Status: classification.status, ReasonCode: "openapi.server_" + string(classification.status), Message: classification.message,
		}
		if classification.rule != "" {
			entry.Rule = openAPIRule(bindingSpec, classification.rule)
		}
		if classification.status == "invalid" && hasSurvivingAlternative {
			entry.OperationKey, entry.BindingSelector = operationKey, selector
		}
		entries = append(entries, entry)
	}
	return entries
}

type serverAlternativeClassification struct {
	status  string
	rule    string
	message string
}

func classifyServerAlternative(server *openapi3.Server, version string) *serverAlternativeClassification {
	variableRule := "P-51"
	boundaryRule := "P-38"
	if strings.HasPrefix(version, "3.0") {
		variableRule, boundaryRule = "P-46", "P-39"
	} else if strings.HasPrefix(version, "3.1") {
		variableRule = "P-47"
	}
	if server == nil || server.URL == "" {
		return &serverAlternativeClassification{"invalid", variableRule, "Server Object has no string url"}
	}
	expanded := server.URL
	for {
		start := strings.IndexByte(expanded, '{')
		if start < 0 {
			break
		}
		endOffset := strings.IndexByte(expanded[start+1:], '}')
		if endOffset < 0 {
			return nil
		}
		end := start + 1 + endOffset
		name := expanded[start+1 : end]
		variable, ok := server.Variables[name]
		if !ok || variable == nil {
			return nil
		}
		if variable.Default == "" {
			return &serverAlternativeClassification{"invalid", variableRule, fmt.Sprintf("Server variable %q has no string default", name)}
		}
		if variable.Enum != nil {
			if len(variable.Enum) == 0 {
				status, rule := "invalid", variableRule
				if strings.HasPrefix(version, "3.0") {
					status, rule = "excluded", "P-47"
				}
				return &serverAlternativeClassification{status, rule, fmt.Sprintf("Server variable %q has no admitted enum domain", name)}
			}
			found := false
			for _, candidate := range variable.Enum {
				found = found || candidate == variable.Default
			}
			if !found {
				status, rule := "invalid", variableRule
				if strings.HasPrefix(version, "3.0") {
					status, rule = "excluded", "P-47"
				}
				return &serverAlternativeClassification{status, rule, fmt.Sprintf("Server variable %q default is outside its enum", name)}
			}
		}
		expanded = expanded[:start] + variable.Default + expanded[end+1:]
	}
	parsed, err := url.Parse(expanded)
	if err != nil {
		if strings.HasPrefix(strings.ToLower(server.URL), "http:") || strings.HasPrefix(strings.ToLower(server.URL), "https:") {
			return &serverAlternativeClassification{"excluded", boundaryRule, "Server URL is not a dispatchable absolute HTTP target"}
		}
		return nil
	}
	if parsed.IsAbs() && (parsed.User != nil || parsed.Hostname() == "") {
		return &serverAlternativeClassification{"excluded", boundaryRule, "Server URL has unusable authority"}
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		if strings.HasPrefix(version, "3.0") {
			return &serverAlternativeClassification{"excluded", boundaryRule, "Server URL carries query or fragment bytes"}
		}
		rule := ""
		if strings.HasPrefix(version, "3.1") {
			rule = boundaryRule
		}
		return &serverAlternativeClassification{"invalid", rule, "Server URL carries query or fragment bytes"}
	}
	return nil
}

func effectiveServerDeclaration(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, selector string) (openapi3.Servers, string) {
	if op != nil && op.Servers != nil && len(*op.Servers) > 0 {
		return *op.Servers, selector + "/servers"
	}
	if pathItem != nil && len(pathItem.Servers) > 0 {
		return pathItem.Servers, selector[:strings.LastIndex(selector, "/")] + "/servers"
	}
	if doc != nil && len(doc.Servers) > 0 {
		return doc.Servers, "#/servers"
	}
	return nil, ""
}

func openAPISecurityAlternativeCoverage(doc *openapi3.T, op *openapi3.Operation, params openapi3.Parameters, selector, operationKey, bindingSpec string, isTrace bool) []ProjectionCoverageEntry {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	prefix := "#/security"
	if op != nil && op.Security != nil {
		prefix = selector + "/security"
	}
	entries := make([]ProjectionCoverageEntry, 0, len(*requirements))
	for index := range *requirements {
		disposition := classifySecurityAlternative(doc, op, params, index, isTrace)
		if disposition.status == "represented" {
			continue
		}
		entry := ProjectionCoverageEntry{
			SourceIndex: 0, SourceRef: fmt.Sprintf("%s/%d", prefix, index), Scope: "alternative",
			Status: disposition.status, ReasonCode: "openapi.security_alternative_unusable", Message: disposition.message,
		}
		if disposition.rule != "" {
			entry.Rule = openAPIRule(bindingSpec, disposition.rule)
		}
		if disposition.status == "invalid" {
			entry.OperationKey, entry.BindingSelector = operationKey, selector
		}
		entries = append(entries, entry)
	}
	return entries
}

type securityAlternativeClassification struct {
	status  string
	rule    string
	message string
}

func classifySecurityAlternative(doc *openapi3.T, op *openapi3.Operation, params openapi3.Parameters, index int, isTrace bool) securityAlternativeClassification {
	requirements := effectiveSecurityRequirements(doc, op)
	malformed := func(message string) securityAlternativeClassification {
		if doc != nil && doc.OpenAPI == "3.2.0" {
			return securityAlternativeClassification{"invalid", "", message}
		}
		return securityAlternativeClassification{"excluded", "P-04", message}
	}
	if requirements == nil || index < 0 || index >= len(*requirements) {
		return malformed("security alternative is absent")
	}
	requirement := (*requirements)[index]
	if len(requirement) == 0 {
		return securityAlternativeClassification{status: "represented"}
	}
	if isTrace && securityRequirementEmitsCredential(doc, op, requirement) {
		rule := "P-39"
		if doc != nil && strings.HasPrefix(doc.OpenAPI, "3.0") {
			rule = "P-38"
		} else if doc != nil && doc.OpenAPI == "3.2.0" {
			rule = "P-41"
		}
		return securityAlternativeClassification{"excluded", rule, "TRACE cannot emit binding-carried credentials"}
	}
	for name := range requirement {
		scheme, ok := securitySchemeForOperation(doc, op, name, nil)
		if !ok || malformedSecurityScheme(scheme) != nil {
			return malformed(fmt.Sprintf("security alternative names malformed or unresolved scheme %q", name))
		}
	}
	plans := append(
		securityPlansWithContext(doc, op, "", contextWithConfigurationPoint(nil, "implicitConnectionScope", "entry")),
		securityPlansWithContext(doc, op, "", contextWithConfigurationPoint(nil, "implicitConnectionScope", "referring"))...,
	)
	var firstCarriageError error
	for _, plan := range plans {
		if plan.alternativeIndex != index {
			continue
		}
		if err := securityPlanCarriageError(plan, params); err == nil {
			return securityAlternativeClassification{status: "represented"}
		} else if firstCarriageError == nil {
			firstCarriageError = err
		}
	}
	if firstCarriageError != nil && strings.Contains(firstCarriageError.Error(), "raw Cookie") {
		rule := "P-29"
		if doc != nil && strings.HasPrefix(doc.OpenAPI, "3.0") {
			rule = "P-28"
		}
		return securityAlternativeClassification{"excluded", rule, firstCarriageError.Error()}
	}
	if firstCarriageError != nil && strings.Contains(firstCarriageError.Error(), "collides") {
		rule := "P-33"
		if doc != nil && strings.HasPrefix(doc.OpenAPI, "3.0") {
			rule = "P-32"
		}
		return securityAlternativeClassification{"excluded", rule, firstCarriageError.Error()}
	}
	return securityAlternativeClassification{"excluded", "P-04", "security alternative is not executable"}
}

func securityRequirementEmitsCredential(doc *openapi3.T, op *openapi3.Operation, requirement openapi3.SecurityRequirement) bool {
	for name := range requirement {
		scheme, ok := securitySchemeForOperation(doc, op, name, nil)
		if !ok || scheme == nil {
			continue
		}
		switch scheme.Type {
		case "apiKey", "oauth2", "openIdConnect":
			return true
		case "http":
			if strings.EqualFold(scheme.Scheme, "basic") || strings.EqualFold(scheme.Scheme, "bearer") {
				return true
			}
		}
	}
	return false
}

func openAPIRequestMediaCoverage(doc *openapi3.T, op *openapi3.Operation, pathItem *openapi3.PathItem, identity struct {
	operationKey string
	selector     string
}, bindingSpec string, verdict *floorOp) []ProjectionCoverageEntry {
	if op.RequestBody == nil || op.RequestBody.Value == nil || len(op.RequestBody.Value.Content) == 0 {
		return nil
	}
	params := declaredEffectiveParameters(pathItem, op)
	plans, planErr := planRequestBodiesFor(doc, op, bindingSpec)
	// §9.2's normalized collision confines to the colliding parsed identity:
	// the colliding keys are excluded alternatives naming that identity, and
	// the map's non-colliding siblings stay represented beside them.
	colliding := normalizedMediaCollisions(op.RequestBody.Value.Content, bindingSpec)
	planned := make(map[string]bool, len(plans))
	represented := make(map[string]bool, len(plans))
	requiresRequestMedia := make(map[string]bool, len(plans))
	requiresPropertyMedia := make(map[string]bool, len(plans))
	for _, plan := range plans {
		planned[plan.mediaKey] = true
		if usesRoutedInput(bindingSpec) || !candidateCollides(params, plan) {
			represented[plan.mediaKey] = true
			requiresRequestMedia[plan.mediaKey] = plan.mediaRange
			requiresPropertyMedia[plan.mediaKey] = len(plan.propertyMedia) > 0
		}
	}
	mediaKeys := make([]string, 0, len(op.RequestBody.Value.Content))
	for mediaKey := range op.RequestBody.Value.Content {
		mediaKeys = append(mediaKeys, mediaKey)
	}
	sort.Strings(mediaKeys)
	entries := make([]ProjectionCoverageEntry, 0, len(mediaKeys))
	for _, mediaKey := range mediaKeys {
		sourceRef := identity.selector + "/requestBody/content/" + escapeJSONPointerToken(mediaKey)
		if verdict != nil {
			if defects, invalid := verdict.InvalidAlternatives[sourceRef]; invalid {
				// The ladder invalidates this alternative: `invalid`, not
				// `excluded` -- the unit is malformed under its upstream
				// authority, not declined by the revision.
				entries = append(entries, ProjectionCoverageEntry{
					SourceIndex: 0,
					SourceRef:   sourceRef,
					Scope:       "alternative",
					Status:      "invalid",
					ReasonCode:  invalidUnitReasonCode,
					Message:     floorInvalidAlternativeMessage(len(defects)),
					Details: map[string]any{
						"defects":   floorDefectDetails(defects),
						"mediaType": mediaKey,
					},
				})
				continue
			}
		}
		if media := op.RequestBody.Value.Content[mediaKey]; multipartMediaHasUnsafePropertyName(mediaKey, media) {
			ruleName := "P-40"
			if bindingSpec == projectionOpenAPI30 {
				ruleName = "P-39"
			} else if bindingSpec == projectionOpenAPI32 {
				ruleName = "P-42"
			}
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0, SourceRef: sourceRef, Scope: "alternative",
				Status: "excluded", ReasonCode: "openapi.multipart_name_excluded",
				Rule: openAPIRule(bindingSpec, ruleName), Message: "multipart property name cannot be emitted in the pinned Content-Disposition form",
			})
			continue
		}
		if media := op.RequestBody.Value.Content[mediaKey]; multipartMediaHasFilenameStar(mediaKey, media, bindingSpec) {
			ruleName := "S-18"
			if bindingSpec == projectionOpenAPI30 {
				ruleName = "S-19"
			}
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0, SourceRef: sourceRef, Scope: "alternative",
				Status: "excluded", ReasonCode: "openapi.multipart_filename_star_excluded",
				Rule: openAPIRule(bindingSpec, ruleName), Message: "multipart Content-Disposition filename* is outside the pinned filename form",
			})
			continue
		}
		if media := op.RequestBody.Value.Content[mediaKey]; encodingHeaderGroupExcluded(media, bindingSpec) {
			rule := "P-32"
			if bindingSpec == projectionOpenAPI30 {
				rule = "P-31"
			}
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0, SourceRef: sourceRef, Scope: "alternative",
				Status: "excluded", ReasonCode: "openapi.encoding_header_group_excluded",
				Rule: openAPIRule(bindingSpec, rule), Message: "case-folded Encoding Header group cannot emit one coherent fixed value",
			})
			continue
		}
		if represented[mediaKey] {
			var requirements []string
			if requiresRequestMedia[mediaKey] {
				requirements = []string{"configuration.requestMedia"}
			}
			if requiresPropertyMedia[mediaKey] {
				requirements = append(requirements, "configuration.propertyMedia")
			}
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex:     0,
				SourceRef:       sourceRef,
				Scope:           "alternative",
				Status:          "represented",
				OperationKey:    identity.operationKey,
				BindingSelector: identity.selector,
				Requirements:    requirements,
			})
			continue
		}
		reasonCode := "openapi.request_media_excluded"
		message := "request media alternative has no faithful candidate carriage"
		rule := openAPIRule(bindingSpec, effectiveRequestBodySynthesisRule(bindingSpec))
		if planned[mediaKey] {
			reasonCode = "openapi.flattening_collision"
			message = "request media alternative collides with an independently declared parameter in the candidate's application boundary"
			rule = openAPIRule(bindingSpec, "P-02")
		} else if identity, collides := colliding[mediaKey]; collides {
			message = fmt.Sprintf("request media alternative denotes the parsed media identity %s, which another declaration in this content map also denotes; no selection may land on a normalized-colliding identity", identity)
		} else if planErr != nil {
			message = planErr.Error()
			if strings.Contains(message, "multipart property name") {
				ruleName := "P-40"
				if bindingSpec == projectionOpenAPI30 {
					ruleName = "P-39"
				} else if bindingSpec == projectionOpenAPI32 {
					ruleName = "P-42"
				}
				rule = openAPIRule(bindingSpec, ruleName)
			}
		}
		entries = append(entries, ProjectionCoverageEntry{
			SourceIndex: 0,
			SourceRef:   sourceRef,
			Scope:       "alternative",
			Status:      "excluded",
			ReasonCode:  reasonCode,
			Rule:        rule,
			Message:     message,
			Details: map[string]any{
				"mediaType": mediaKey,
			},
		})
	}
	return entries
}

func multipartMediaHasUnsafePropertyName(mediaKey string, media *openapi3.MediaType) bool {
	if media == nil || media.Schema == nil || media.Schema.Value == nil || !strings.EqualFold(strings.TrimSpace(strings.Split(mediaKey, ";")[0]), "multipart/form-data") {
		return false
	}
	for name := range media.Schema.Value.Properties {
		if !multipartPropertyNameSafe(name) {
			return true
		}
	}
	return false
}

func multipartMediaHasFilenameStar(mediaKey string, media *openapi3.MediaType, bindingSpec string) bool {
	if media == nil || !strings.EqualFold(strings.TrimSpace(strings.Split(mediaKey, ";")[0]), "multipart/form-data") {
		return false
	}
	for _, encoding := range media.Encoding {
		if encoding == nil {
			continue
		}
		for name, ref := range encoding.Headers {
			if !strings.EqualFold(name, "Content-Disposition") || ref == nil || ref.Value == nil {
				continue
			}
			domain, understood := exactHeaderStringDomain(ref.Value.Schema, bindingSpec == projectionOpenAPI30)
			if !understood {
				continue
			}
			for value := range domain {
				if strings.Contains(strings.ToLower(value), "filename*=") {
					return true
				}
			}
		}
	}
	return false
}

func encodingHeaderGroupExcluded(media *openapi3.MediaType, bindingSpec string) bool {
	if media == nil {
		return false
	}
	for _, encoding := range media.Encoding {
		if encoding == nil {
			continue
		}
		groups := map[string][]*openapi3.Header{}
		for name, ref := range encoding.Headers {
			if ref == nil || ref.Value == nil || strings.EqualFold(name, "Content-Type") {
				continue
			}
			groups[strings.ToLower(name)] = append(groups[strings.ToLower(name)], ref.Value)
		}
		for _, members := range groups {
			if len(members) < 2 {
				continue
			}
			fixed := ""
			for _, header := range members {
				domain, understood := exactHeaderStringDomain(header.Schema, bindingSpec == projectionOpenAPI30)
				if !understood || len(domain) != 1 {
					if header.Required {
						return true
					}
					continue
				}
				value := ""
				for candidate := range domain {
					value = candidate
				}
				if fixed != "" && fixed != value {
					return true
				}
				fixed = value
			}
		}
	}
	return false
}

func openAPIInboundDependencyCoverage(doc *openapi3.T, artifact *Artifact, iface *ProjectionDocument, unrealizable map[string]unrealizableTarget, bindingSpec string) []ProjectionCoverageEntry {
	// Every unit this function reports comes from the INBOUND inventory --
	// callbacks and webhooks -- so all of them are dependency-kind units
	// whatever became of them. Scope names what the source unit is; status
	// names its disposition. Filing the removed ones under `target` said they
	// were addressable operations, which they never were.
	inventory := openAPIInboundOperationInventory(doc, artifact)
	if len(inventory) == 0 {
		return nil
	}
	usedOperationKeys := map[string]bool{}
	for _, binding := range iface.Bindings {
		usedOperationKeys[binding.Operation] = true
	}
	dependencyOperations := map[string]bool{}
	for _, dependency := range iface.Dependencies {
		dependencyOperations[dependency.Operation] = true
	}
	var entries []ProjectionCoverageEntry
	for _, disposition := range inventory {
		if disposition.Err != nil || disposition.Target == nil {
			message := "inbound OpenAPI operation is unresolvable"
			if disposition.Err != nil {
				message = disposition.Err.Error()
			}
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0, SourceRef: disposition.Reference.Ref,
				Scope: "dependency", Status: "excluded",
				ReasonCode: "openapi.inbound_dependency_excluded", Rule: openAPIRule(bindingSpec, "P-01"), Message: message,
			})
			continue
		}
		opKey := deriveInboundOperationKey(disposition.Target, usedOperationKeys)
		usedOperationKeys[opKey] = true
		if skipped, ok := unrealizable[disposition.Reference.Ref]; ok {
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0, SourceRef: disposition.Reference.Ref,
				Scope: "dependency", Status: "excluded",
				ReasonCode: skipped.reasonCode, Rule: skipped.rule, Message: skipped.message,
			})
			continue
		}
		if dependencyOperations[opKey] {
			entries = append(entries, ProjectionCoverageEntry{
				SourceIndex: 0, SourceRef: disposition.Reference.Ref,
				Scope: "dependency", Status: "represented",
			})
			continue
		}
		entries = append(entries, ProjectionCoverageEntry{
			SourceIndex: 0, SourceRef: disposition.Reference.Ref,
			Scope: "dependency", Status: "implementation-unsupported",
			ReasonCode: "openapi.missing_emitted_dependency", Rule: openAPIRule(bindingSpec, "S-01"),
			Message: "the synthesizer returned without emitting this supported inbound dependency contract",
		})
	}
	return entries
}

func escapeJSONPointerToken(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func formatCoverageRef(parts ...string) string {
	return fmt.Sprintf("#/%s", strings.Join(parts, "/"))
}

// floorInvalidAlternativeEntries renders a ladder-invalid operation's or an
// excluded operation's invalid request media alternatives.
func floorInvalidAlternativeEntries(verdict *floorOp) []ProjectionCoverageEntry {
	if verdict == nil || len(verdict.AltOrder) == 0 {
		return nil
	}
	entries := make([]ProjectionCoverageEntry, 0, len(verdict.AltOrder))
	for _, altRef := range verdict.AltOrder {
		defects := verdict.InvalidAlternatives[altRef]
		entries = append(entries, ProjectionCoverageEntry{
			SourceIndex: 0,
			SourceRef:   altRef,
			Scope:       "alternative",
			Status:      "invalid",
			ReasonCode:  invalidUnitReasonCode,
			Message:     floorInvalidAlternativeMessage(len(defects)),
			Details: map[string]any{
				"defects":   floorDefectDetails(defects),
				"mediaType": unescapeJSONPointerToken(altRef[strings.LastIndex(altRef, "/")+1:]),
			},
		})
	}
	return entries
}

// floorProjectionEntries renders one projection-scope entry per unit whose
// emitted closure reaches, or whose response rungs record, invalid positions
// that cost it nothing.
func floorProjectionEntries(verdict *floorOp) []ProjectionCoverageEntry {
	if verdict == nil || len(verdict.ProjOrder) == 0 {
		return nil
	}
	entries := make([]ProjectionCoverageEntry, 0, len(verdict.ProjOrder))
	for _, unit := range verdict.ProjOrder {
		defects := verdict.Projections[unit]
		entries = append(entries, ProjectionCoverageEntry{
			SourceIndex: 0,
			SourceRef:   unit,
			Scope:       "projection",
			Status:      "invalid",
			ReasonCode:  invalidUnitReasonCode,
			Message:     floorProjectionMessage(len(defects)),
			Details:     map[string]any{"defects": floorDefectDetails(defects)},
		})
	}
	return entries
}

func unescapeJSONPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~1", "/")
	return strings.ReplaceAll(token, "~0", "~")
}
