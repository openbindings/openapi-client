package openapiclient

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// propertyMediaMap reads the engine's private compatibility context. Public
// Client callers provide the same OpenAPI-native choices through
// Input.PropertyMediaTypes.
func propertyMediaMap(contextValue map[string]any) (map[string]any, bool, error) {
	raw, present := contextConfiguration(contextValue)["propertyMedia"]
	if !present || raw == nil {
		return nil, false, nil
	}
	switch value := raw.(type) {
	case map[string]any:
		return value, true, nil
	case map[string]string:
		result := make(map[string]any, len(value))
		for name, mediaType := range value {
			result[name] = mediaType
		}
		return result, true, nil
	default:
		return nil, true, fmt.Errorf("property media choices must be an object keyed by property name")
	}
}

func configuredPropertyMedia(plan *bodyPlan, contextValue map[string]any) (map[string]string, error) {
	if plan == nil || len(plan.propertyMedia) == 0 {
		return nil, nil
	}
	configured, _, err := propertyMediaMap(contextValue)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(plan.propertyMedia))
	for _, name := range plan.propertyMedia {
		raw, present := configured[name]
		if !present || raw == nil {
			return nil, &configRequired{
				point:       "propertyMedia",
				path:        "/" + escapeJSONPointerSegment(name),
				description: fmt.Sprintf("OpenAPI property %q requires one concrete media type choice", name),
			}
		}
		choice, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("property media choice %q must be a concrete media-type string", name)
		}
		selected, selectErr := selectPropertyMedia(plan, name, choice)
		if selectErr != nil {
			return nil, selectErr
		}
		result[name] = selected
	}
	return result, nil
}

func selectPropertyMedia(plan *bodyPlan, name, choice string) (string, error) {
	wanted, err := parseRevision3MediaType(choice)
	if err != nil || wanted.rangeSpecificity != 2 {
		if err == nil {
			err = fmt.Errorf("media ranges are not concrete")
		}
		return "", fmt.Errorf("property media choice %q: %w", name, err)
	}
	declaredContentType := plan.propertyMediaDeclarations[name]
	if declaredContentType == "" {
		return wanted.canonical, nil
	}
	members, err := splitHTTPList(declaredContentType)
	if err != nil {
		return "", fmt.Errorf("property %q Encoding contentType: %w", name, err)
	}
	identities := map[string]int{}
	parsedMembers := make([]parsedMediaType, 0, len(members))
	for _, member := range members {
		declared, parseErr := parseMediaDeclaration(member)
		if parseErr != nil {
			return "", fmt.Errorf("property %q Encoding contentType: %w", name, parseErr)
		}
		identities[declared.identity]++
		parsedMembers = append(parsedMembers, declared)
	}
	var best []parsedMediaType
	bestSpecificity, bestParams := -1, -1
	for _, declared := range parsedMembers {
		if identities[declared.identity] > 1 || !requestMediaDeclarationMatches(declared, wanted) {
			continue
		}
		specificity, parameterCount := declared.rangeSpecificity, len(declared.params)
		switch {
		case specificity > bestSpecificity || specificity == bestSpecificity && parameterCount > bestParams:
			bestSpecificity, bestParams = specificity, parameterCount
			best = []parsedMediaType{declared}
		case specificity == bestSpecificity && parameterCount == bestParams:
			best = append(best, declared)
		}
	}
	if len(best) == 0 {
		return "", fmt.Errorf("property media choice %q for %q matches no declared Encoding contentType member", choice, name)
	}
	if len(best) > 1 {
		labels := make([]string, len(best))
		for index, candidate := range best {
			labels[index] = candidate.canonical
		}
		sort.Strings(labels)
		return "", fmt.Errorf("property media choice %q for %q ambiguously matches %s", choice, name, strings.Join(labels, ", "))
	}
	return wanted.canonical, nil
}

// planWithPropertyMedia returns an invocation-local plan whose Encoding
// content types contain the selected concrete members. It never mutates the
// loaded OpenAPI document, so concurrent executions may select independently.
func planWithPropertyMedia(plan *bodyPlan, contextValue map[string]any) (*bodyPlan, error) {
	selected, err := configuredPropertyMedia(plan, contextValue)
	if err != nil || len(selected) == 0 {
		return plan, err
	}
	copyPlan := *plan
	copyMedia := *plan.media
	copyMedia.Encoding = make(openapi3.Encodings, len(plan.media.Encoding)+len(selected))
	for name, encoding := range plan.media.Encoding {
		if encoding == nil {
			copyMedia.Encoding[name] = nil
			continue
		}
		copyEncoding := *encoding
		copyMedia.Encoding[name] = &copyEncoding
	}
	for name, mediaType := range selected {
		encoding := copyMedia.Encoding[name]
		if encoding == nil {
			encoding = &openapi3.Encoding{}
			copyMedia.Encoding[name] = encoding
		}
		encoding.ContentType = mediaType
	}
	copyPlan.media = &copyMedia
	if plan.openAPI32 != nil {
		copyOverlay := *plan.openAPI32
		copyOverlay.encoding = make(map[string]*openAPI32EncodingOverlay, len(plan.openAPI32.encoding))
		for name, encoding := range plan.openAPI32.encoding {
			if encoding == nil {
				copyOverlay.encoding[name] = nil
				continue
			}
			copyEncoding := *encoding
			copyOverlay.encoding[name] = &copyEncoding
		}
		for name, mediaType := range selected {
			encoding := copyOverlay.encoding[name]
			if encoding == nil {
				encoding = &openAPI32EncodingOverlay{}
				copyOverlay.encoding[name] = encoding
			}
			encoding.contentType = mediaType
		}
		copyPlan.openAPI32 = &copyOverlay
	}
	return &copyPlan, nil
}

func requiredPropertyMediaContext(document *openapi3.T, operation *openapi3.Operation, profile string, contextValue map[string]any) (*Prerequisites, error) {
	return requiredPropertyMediaContextWithPlans(document, operation, profile, contextValue, nil)
}

func requiredPropertyMediaContextWithPlans(document *openapi3.T, operation *openapi3.Operation, profile string, contextValue map[string]any, plans []*bodyPlan) (*Prerequisites, error) {
	if !hasMediaFidelity(profile) || operation == nil || operation.RequestBody == nil || operation.RequestBody.Value == nil || !operation.RequestBody.Value.Required {
		return nil, nil
	}
	if plans == nil {
		var err error
		plans, err = planRequestBodiesFor(document, operation, profile)
		if err != nil {
			return nil, err
		}
	}
	var err error
	if requestMediaUnconfigured(contextValue) {
		sole := soleConcreteRequestPlan(operation, plans)
		if sole == nil {
			return nil, nil
		}
		plans = []*bodyPlan{sole}
	} else {
		plans, err = configuredRequestPlansFor(document, operation, plans, contextValue, profile)
		if err != nil {
			return nil, err
		}
	}
	configured, _, err := propertyMediaMap(contextValue)
	if err != nil {
		return nil, err
	}
	var requirements []Requirement
	seen := map[string]bool{}
	for _, plan := range plans {
		for _, name := range plan.propertyMedia {
			raw, present := configured[name]
			if !present || raw == nil {
				if !seen[name] {
					seen[name] = true
					requirements = append(requirements, newConfigValueRequirementCompat(
						"propertyMedia", "/"+escapeJSONPointerSegment(name),
						"select one concrete media type for this form or multipart property", nil, nil,
					))
				}
				continue
			}
			choice, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("property media choice %q must be a concrete media-type string", name)
			}
			if _, err := selectPropertyMedia(plan, name, choice); err != nil {
				return nil, err
			}
		}
	}
	if len(requirements) == 0 {
		return nil, nil
	}
	return &Prerequisites{Alternatives: []RequirementAlternative{{Requirements: requirements}}}, nil
}
