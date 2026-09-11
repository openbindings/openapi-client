package openapiclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

const (
	projectionOpenAPI30 = "projection:openapi-3.0-r1"
	projectionOpenAPI31 = "projection:openapi-3.1-r1"
	projectionOpenAPI32 = "projection:openapi-3.2-r1"
)

const referringSecuritySchemesMarker = "x-openapi-client-referring-security-schemes"

// ProjectionDocument is the OpenAPI-native, detached generator contract.
// It deliberately has no OpenBindings source, binding, or interface type.
type ProjectionDocument struct {
	Name         string
	Version      string
	Description  string
	Operations   map[string]ProjectionOperation
	Bindings     map[string]ProjectionBinding
	Dependencies map[string]ProjectionDependency
}

type ProjectionOperation struct {
	Description string
	Deprecated  bool
	Tags        []string
	Input       any
	Output      any
}

type ProjectionBinding struct {
	Operation string
	Selector  string
	Input     *ProjectionInputCorrespondence
}

// ProjectionInputCorrespondence describes how one flat, protocol-neutral
// operation value maps to the native caller envelope. A protocol adapter owns
// rendering this correspondence into its own transform language.
type ProjectionInputCorrespondence struct {
	Parameters     []ProjectionParameterRoute
	BodyProperties map[string]string
	WholeBodyField string
	OpenBody       bool
	BodyRequired   bool
}

type ProjectionParameterRoute struct {
	In        string
	Name      string
	Field     string
	CallerKey string
}

type ProjectionDependency struct{ Operation string }

type ProjectionWarning struct {
	Code    string
	Message string
	Path    string
}

type ProjectionCoverageEntry struct {
	SourceIndex     int
	SourceRef       string
	Scope           string
	Status          string
	OperationKey    string
	BindingKey      string
	BindingSelector string
	ReasonCode      string
	Rule            string
	Message         string
	Requirements    []string
	Details         map[string]any
}

type ProjectionFailure struct {
	SourceRef    string
	OperationKey string
	ReasonCode   string
	Status       string
	Rule         string
	Message      string
}

// ProjectionAnalysis is the generator-grade view of one loaded artifact.
// Exactly one of Swagger20 or OpenAPI3 is populated.
type ProjectionAnalysis struct {
	Edition    Edition
	Location   string
	Swagger20  *Swagger20SynthesisDocument
	OpenAPI3   *ProjectionDocument
	Coverage   []ProjectionCoverageEntry
	Failures   []ProjectionFailure
	Warnings   []ProjectionWarning
	SourceRef  string
	SourceKind string
}

// Projection derives generator contracts from the same loaded artifact and
// native planners used by invocation. The result is memoized and defensively
// copied; it performs no source retrieval or reparsing.
func (c *Client) Projection() (ProjectionAnalysis, error) {
	if c == nil {
		return ProjectionAnalysis{}, fmt.Errorf("OpenAPI client is nil")
	}
	c.projectionOnce.Do(func() {
		result := ProjectionAnalysis{Edition: c.Edition(), Location: c.Location(), SourceRef: "#", SourceKind: "openapi"}
		if c.swagger20 != nil {
			model, err := c.swagger20.SynthesisModel()
			if err != nil {
				c.projectionErr = err
				return
			}
			result.Swagger20 = model
			result.SourceKind = "swagger"
			c.projection = result
			return
		}
		if c.artifact == nil || c.artifact.Document == nil {
			c.projectionErr = fmt.Errorf("loaded OpenAPI artifact is unavailable")
			return
		}
		if exclusion := c.artifact.SourceExclusion(); exclusion != nil {
			result.Failures = append(result.Failures, ProjectionFailure{
				SourceRef: "#", ReasonCode: "openapi.source_excluded", Status: "excluded",
				Rule: openAPIRule(projectionProfileForEdition(c.edition), "P-05"), Message: exclusion.Error(),
			})
			c.projection = result
			return
		}
		profile := projectionProfileForEdition(c.edition)
		unrealizable := map[string]unrealizableTarget{}
		var warnings []ProjectionWarning
		document, err := convertArtifactToInterfaceWithOverlay(
			c.artifact.Document, c.artifact, c.Location(), profile,
			func(warning ProjectionWarning) { warnings = append(warnings, warning) },
			func(target unrealizableTarget) { unrealizable[target.selector] = target },
			c.artifact.schemaOverlays, c.floor,
		)
		if err != nil {
			c.projectionErr = err
			return
		}
		result.OpenAPI3 = &document
		result.Warnings = warnings
		selectors := make([]string, 0, len(unrealizable))
		for selector := range unrealizable {
			selectors = append(selectors, selector)
		}
		sort.Strings(selectors)
		for _, selector := range selectors {
			target := unrealizable[selector]
			status := target.status
			if status == "" {
				status = "excluded"
			}
			result.Failures = append(result.Failures, ProjectionFailure{
				SourceRef: target.selector, OperationKey: target.operationKey,
				ReasonCode: target.reasonCode, Status: status, Rule: target.rule, Message: target.message,
			})
		}
		result.Coverage = openAPISynthesisCoverage(c.artifact.Document, c.artifact, &document, unrealizable, c.floor, c.Location(), profile)
		c.projection = result
	})
	return cloneProjectionAnalysis(c.projection), c.projectionErr
}

func projectionProfileForEdition(edition Edition) string {
	switch {
	case strings.HasPrefix(string(edition), "3.0."):
		return projectionOpenAPI30
	case strings.HasPrefix(string(edition), "3.1."):
		return projectionOpenAPI31
	case edition == EditionOpenAPI320:
		return projectionOpenAPI32
	default:
		return ""
	}
}

func cloneProjectionAnalysis(value ProjectionAnalysis) ProjectionAnalysis {
	data, err := json.Marshal(value)
	if err != nil {
		return ProjectionAnalysis{}
	}
	var result ProjectionAnalysis
	if unmarshalJSONImage(data, &result) != nil {
		return ProjectionAnalysis{}
	}
	return result
}

type parameterConfinementResult struct {
	parameters openapi3.Parameters
	targetRule string
	message    string
}

func confineEffectiveParameters(params openapi3.Parameters, profile string) parameterConfinementResult {
	result := parameterConfinementResult{parameters: make(openapi3.Parameters, 0, len(params))}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		parameter := ref.Value
		if parameter.In == openapi3.ParameterInHeader {
			if projectionProcessorOwnedHeader(strings.ToLower(parameter.Name)) {
				result.targetRule = "P-18"
				if profile == projectionOpenAPI30 {
					result.targetRule = "P-17"
				}
				result.message = fmt.Sprintf("header parameter %q names a processor-owned field", parameter.Name)
				return result
			}
			if !projectionHTTPFieldName.MatchString(parameter.Name) {
				if parameter.Required {
					result.targetRule = "P-17"
					if profile == projectionOpenAPI30 {
						result.targetRule = "P-16"
					}
					result.message = fmt.Sprintf("required header parameter %q is not an HTTP field-name", parameter.Name)
					return result
				}
				continue
			}
		}
		if parameter.In == openapi3.ParameterInCookie && !projectionHTTPFieldName.MatchString(parameter.Name) {
			if parameter.Required {
				result.targetRule = "S-04"
				result.message = fmt.Sprintf("required cookie parameter %q cannot form a Cookie pair", parameter.Name)
				return result
			}
			continue
		}
		result.parameters = append(result.parameters, ref)
	}
	return result
}

func duplicateEffectiveParameterIdentity(params openapi3.Parameters) string {
	seen := map[string]bool{}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		identity := ref.Value.In + "\x00" + ref.Value.Name
		if seen[identity] {
			return ref.Value.In + "/" + escapeJSONPointerSegment(ref.Value.Name)
		}
		seen[identity] = true
	}
	return ""
}

func requestBodyIgnoredForBindingSpec(profile, method string) bool {
	method = strings.ToLower(method)
	if profile == projectionOpenAPI31 || profile == projectionOpenAPI32 {
		return method == "trace"
	}
	if profile != projectionOpenAPI30 {
		return false
	}
	switch method {
	case "get", "head", "delete", "options", "trace":
		return true
	default:
		return false
	}
}

func checkPathTemplateDeclaration(path string, params openapi3.Parameters, profile string) error {
	if err := checkPathTemplateAddressability(path, params); err != nil {
		return err
	}
	if profile != projectionOpenAPI30 && profile != projectionOpenAPI31 {
		return nil
	}
	expressions := map[string]bool{}
	for _, name := range pathTemplateVariables(path) {
		expressions[name] = true
	}
	var unmatched []string
	for _, ref := range params {
		if ref != nil && ref.Value != nil && ref.Value.In == openapi3.ParameterInPath && !expressions[ref.Value.Name] {
			unmatched = append(unmatched, ref.Value.Name)
		}
	}
	if len(unmatched) == 0 {
		return nil
	}
	sort.Strings(unmatched)
	return fmt.Errorf("declared path parameter(s) %s have no path template expression", strings.Join(unmatched, ", "))
}

func equivalentPathTemplateCollision(paths *openapi3.Paths, selected string) string {
	if paths == nil {
		return ""
	}
	candidates := make([]string, 0, paths.Len())
	for candidate := range paths.Map() {
		candidates = append(candidates, candidate)
	}
	return equivalentTemplatedPathKey(selected, candidates)
}

func formStyleCookieMultiValueProof(parameter *openapi3.Parameter, is30 bool) bool {
	if parameter == nil || parameter.In != openapi3.ParameterInCookie || len(parameter.Content) > 0 || parameter.Schema == nil || parameter.Schema.Value == nil {
		return false
	}
	method, err := revision3ParameterSerializationMethod(parameter)
	if err != nil || method.Style != openapi3.SerializationForm || !method.Explode {
		return false
	}
	resolved := resolveDeclaration(parameter.Schema.Value, is30)
	return resolved.declaresOnly("array") || resolved.declaresOnly("object") && len(resolved.propertyNames()) > 0
}

func formStyleCookieMultiValueParamFor(params openapi3.Parameters, is30 bool) string {
	for _, ref := range params {
		if ref != nil && formStyleCookieMultiValueProof(ref.Value, is30) {
			return ref.Value.Name
		}
	}
	return ""
}

func malformedEffectiveParameterFor(params openapi3.Parameters, profile string) string {
	if profile != projectionOpenAPI30 && profile != projectionOpenAPI31 {
		return ""
	}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		parameter := ref.Value
		if parameter.Name == "" || parameter.In != openapi3.ParameterInPath && parameter.In != openapi3.ParameterInQuery &&
			parameter.In != openapi3.ParameterInHeader && parameter.In != openapi3.ParameterInCookie ||
			(parameter.Schema != nil) == (parameter.Content != nil) ||
			parameter.In == openapi3.ParameterInPath && !parameter.Required ||
			parameter.Content != nil && len(parameter.Content) != 1 {
			if parameter.Name == "" {
				return "<unnamed>"
			}
			return parameter.Name
		}
	}
	return ""
}

var projectionNonKeyChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)
var projectionHTTPFieldName = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

func projectionProcessorOwnedHeader(name string) bool {
	switch name {
	case "accept", "connection", "content-length", "content-type", "host", "proxy-authorization", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func projectionSanitizeKey(name string) string {
	key := strings.Trim(projectionNonKeyChars.ReplaceAllString(name, "_"), "_")
	if key == "" {
		return "unnamed"
	}
	first := key[0]
	if !(first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z' || first == '_') {
		key = "_" + key
	}
	return key
}

func projectionUniqueKey(key string, used map[string]bool) string {
	if !used[key] {
		return key
	}
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s_%d", key, index)
		if !used[candidate] {
			return candidate
		}
	}
}

func openAPIRule(profile, suffix string) string {
	prefix := "OAPI31"
	switch profile {
	case projectionOpenAPI30:
		prefix = "OAPI30"
	case projectionOpenAPI32:
		prefix = "OAPI32"
	}
	return prefix + "-" + suffix
}

// unrealizableTarget records a paths operation admitted by the artifact but
// unrepresentable at the sibling's synthesizable operation boundary. Reported instead of
// returned as an error when the caller opts into per-operation tolerance
// (the coverage and inspection surfaces), so one unrepresentable operation
// narrows coverage rather than vetoing the document (core §10's posture;
// interface-synthesizer contract's "sound partial OBI").
type unrealizableTarget struct {
	selector     string
	operationKey string
	reasonCode   string
	rule         string
	message      string
	status       string
}

// convertDocToInterface converts a loaded OpenAPI document into an
// OpenBindings interface.
//
// When onUnrealizable is non-nil, an operation whose synthesizable operation
// boundary cannot be represented is reported and skipped — no operation, no
// binding — and synthesis continues (tolerant mode). When nil, the same
// condition returns an error (strict mode: SynthesizeInterface), preserving
// the convenient strict surface's guarantee that it never returns a
// statically unbindable partial interface without evidence.
func convertDocToInterface(doc *openapi3.T, location, bindingSpec string, warn func(ProjectionWarning), onUnrealizable func(unrealizableTarget)) (ProjectionDocument, error) {
	return convertArtifactToInterfaceWithOverlay(doc, nil, location, bindingSpec, warn, onUnrealizable, nil, nil)
}

func convertDocToInterfaceWithOverlay(doc *openapi3.T, location, bindingSpec string, warn func(ProjectionWarning), onUnrealizable func(unrealizableTarget), schemaOverlays *rawSchemaOverlayCollector, floor *acceptanceFloor) (ProjectionDocument, error) {
	return convertArtifactToInterfaceWithOverlay(doc, nil, location, bindingSpec, warn, onUnrealizable, schemaOverlays, floor)
}

type synthesisOperationTarget struct {
	document  *openapi3.T
	pathItem  *openapi3.PathItem
	operation *openapi3.Operation
	path      string
	method    string
	selector  string
	referring openapi3.SecuritySchemes
}

func synthesisOperationTargets(doc *openapi3.T, artifact *Artifact) []synthesisOperationTarget {
	if artifact != nil && artifact.Edition.IsOpenAPI32() {
		inventory := artifact.OperationInventory()
		result := make([]synthesisOperationTarget, 0, len(inventory))
		for _, disposition := range inventory {
			target := disposition.Target
			if disposition.Err != nil || target == nil {
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
			result = append(result, synthesisOperationTarget{
				document: target.Document, pathItem: target.PathItem, operation: operation,
				path: target.Path, method: target.Method, selector: target.Ref,
				referring: target.ReferringSecuritySchemes,
			})
		}
		return result
	}
	if doc == nil || doc.Paths == nil {
		return nil
	}
	pathKeys := make([]string, 0, doc.Paths.Len())
	for path := range doc.Paths.Map() {
		pathKeys = append(pathKeys, path)
	}
	sort.Strings(pathKeys)
	var result []synthesisOperationTarget
	for _, path := range pathKeys {
		pathItem := doc.Paths.Find(path)
		if pathItem == nil {
			continue
		}
		for _, method := range httpMethods {
			operation := pathItem.GetOperation(strings.ToUpper(method))
			if operation != nil {
				result = append(result, synthesisOperationTarget{document: doc, pathItem: pathItem, operation: operation, path: path, method: method, selector: buildJSONPointerSelector(path, method)})
			}
		}
	}
	return result
}

func convertArtifactToInterfaceWithOverlay(doc *openapi3.T, artifact *Artifact, location, bindingSpec string, warn func(ProjectionWarning), onUnrealizable func(unrealizableTarget), schemaOverlays *rawSchemaOverlayCollector, floor *acceptanceFloor) (ProjectionDocument, error) {
	// The schema-dialect translation keys off the artifact's own declared
	// version (3.0 vs 3.1); the identifier stays exact and version-free.
	formatVersion := majorMinor(doc.OpenAPI)

	result := ProjectionDocument{
		Operations:   map[string]ProjectionOperation{},
		Dependencies: map[string]ProjectionDependency{},
		Bindings:     map[string]ProjectionBinding{},
	}

	if doc.Info != nil {
		result.Name = doc.Info.Title
		result.Version = doc.Info.Version
		result.Description = doc.Info.Description
	}

	if doc.Paths == nil && artifact == nil {
		return result, nil
	}

	// Build a registry of `$ref → resolved schema` from
	// doc.Components.Schemas. Used to inline every `$ref` that survives
	// kin-openapi's MarshalJSON pass on operation input/output schemas.
	// See inlineRefs / buildRefRegistry above for the rationale.
	refRegistry := buildRefRegistry(doc, schemaOverlays)
	// Cut points are decided per direction, over the registry as that direction
	// will emit it: read/write projection can remove the only edge that closed a
	// cycle, and a cycle that is not in the emitted graph must not be cut there.
	// See cut_points.go for the convention and its TypeScript twin.
	requestGraph, responseGraph := newDirectionGraphs(refRegistry)
	namer := newCutPointNamer(location, schemaOverlays.externalComponents())

	usedKeys := map[string]bool{}

	for _, target := range synthesisOperationTargets(doc, artifact) {
		path, method, selector := target.path, target.method, target.selector
		pathItem, op, operationDocument := target.pathItem, target.operation, target.document
		if operationDocument == nil {
			operationDocument = doc
		}

		// The acceptance floor (the registered OpenAPI binding family §3): a
		// ladder-invalid target is not addressed. Tolerant surfaces skip
		// it (its invalid coverage entry is emitted by the coverage
		// walk); the strict surface refuses, preserving its guarantee.
		// Skipped BEFORE key derivation, in every engine identically.
		verdict := floor.opVerdict(selector)
		if verdict != nil && verdict.Disposition == "invalid" {
			if onUnrealizable != nil {
				continue
			}
			return result, fmt.Errorf("cannot synthesize OpenAPI operation at %q: %s; synthesis would return a statically unbindable partial interface", selector, floorInvalidTargetMessage(len(verdict.Defects)))
		}

		opKey := deriveOperationKey(op, path, method, usedKeys)
		usedKeys[opKey] = true
		if op.Responses != nil && op.Responses.Len() == 0 {
			reason := "the present Responses Object has no admitted response declaration"
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector: selector, operationKey: opKey,
					reasonCode: "openapi.responses_invalid", rule: openAPIRule(bindingSpec, "P-05"),
					message: reason, status: "invalid",
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}

		params := declaredEffectiveParameters(pathItem, op)
		confinement := confineEffectiveParameters(params, bindingSpec)
		if confinement.targetRule != "" {
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector: selector, operationKey: opKey,
					reasonCode: "openapi.parameter_excluded", rule: openAPIRule(bindingSpec, confinement.targetRule),
					message: confinement.message,
				})
				continue
			}
			return result, unrealizableOperation(opKey, confinement.message)
		}
		params = confinement.parameters
		if method == "trace" && traceTargetHasNoSafeInvocation(operationDocument, op, params) {
			rule := "P-39"
			if bindingSpec == projectionOpenAPI30 {
				rule = "P-38"
			} else if bindingSpec == projectionOpenAPI32 {
				rule = "P-41"
			}
			reason := "TRACE target has no security alternative that can execute without emitting credentials or Cookie"
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector: selector, operationKey: opKey,
					reasonCode: "openapi.trace_credentials_excluded", rule: openAPIRule(bindingSpec, rule), message: reason,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}
		if requiredRequestContentEncoding(params) && !operationHasRequestRepresentation(operationDocument, op, bindingSpec, method) {
			rule := "P-42"
			if bindingSpec == projectionOpenAPI30 {
				rule = "P-41"
			} else if bindingSpec == projectionOpenAPI32 {
				rule = "P-45"
			}
			reason := "required Content-Encoding parameter has no surviving request representation lane"
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector: selector, operationKey: opKey,
					reasonCode: "openapi.bodyless_content_encoding_excluded", rule: openAPIRule(bindingSpec, rule), message: reason,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}
		if _, serverErr := EffectiveServerSet(operationDocument, pathItem, op, location); serverErr != nil {
			reason := serverErr.Error()
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector:     selector,
					operationKey: opKey,
					reasonCode:   "openapi.server_url_excluded",
					rule:         openAPIRule(bindingSpec, "P-04"),
					message:      reason,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}
		securityRequirements := effectiveSecurityRequirements(operationDocument, op)
		if securityRequirements != nil && len(*securityRequirements) > 0 {
			entryPlans := viableSecurityPlans(securityDocumentForScope(operationDocument, target.referring, ImplicitConnectionEntry), op, "", params)
			referringPlans := viableSecurityPlans(securityDocumentForScope(operationDocument, target.referring, ImplicitConnectionReferring), op, "", params)
			if len(entryPlans) == 0 && len(referringPlans) == 0 {
				reason := "the effective security declaration has no usable complete alternative"
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						selector: selector, operationKey: opKey,
						reasonCode: "openapi.security_alternative_unusable", rule: openAPIRule(bindingSpec, "P-04"), message: reason,
					})
					continue
				}
				return result, unrealizableOperation(opKey, reason)
			}
		}
		if duplicate := duplicateEffectiveParameterIdentity(params); duplicate != "" {
			reason := fmt.Sprintf("parameter identity %q is declared more than once", duplicate)
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector:     selector,
					operationKey: opKey,
					reasonCode:   "openapi.duplicate_parameter_identity",
					rule:         openAPIRule(bindingSpec, "P-02"),
					message:      reason,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}
		if field := unflattenableParamForRevision(params, bindingSpec); field != "" {
			reason := fmt.Sprintf("parameter %q has no unique flattened identity", field)
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector:     selector,
					operationKey: opKey,
					reasonCode:   "openapi.flattening_collision",
					rule:         openAPIRule(bindingSpec, "P-02"),
					message:      reason,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}
		if parameter := malformedEffectiveParameterFor(params, bindingSpec); parameter != "" {
			reason := fmt.Sprintf("effective parameter %q violates the closed Parameter Object declaration list", parameter)
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector:     selector,
					operationKey: opKey,
					reasonCode:   "openapi.parameter_declaration_excluded",
					rule:         openAPIRule(bindingSpec, "P-02"),
					message:      reason,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}
		if err := checkPathTemplateDeclaration(path, params, bindingSpec); err != nil || bindingSpec == projectionOpenAPI31 && equivalentPathTemplateCollision(doc.Paths, path) != "" {
			reason := "the selected path declaration is ambiguous or does not correspond one-to-one with its effective path parameters"
			if err != nil {
				reason = err.Error()
			}
			if onUnrealizable != nil {
				status := ""
				rule := openAPIRule(bindingSpec, "P-02")
				if err != nil {
					status = "invalid"
					rule = ""
				}
				onUnrealizable(unrealizableTarget{
					selector:     selector,
					operationKey: opKey,
					reasonCode:   "openapi.path_correspondence_excluded",
					rule:         rule,
					message:      reason,
					status:       status,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}

		if parameter := unsupportedParameterContentFor(params, bindingSpec); parameter != "" {
			reason := fmt.Sprintf("parameter %q declares content with no faithful candidate carriage", parameter)
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector:     selector,
					operationKey: opKey,
					reasonCode:   "openapi.parameter_content_excluded",
					rule:         openAPIRule(bindingSpec, "P-02"),
					message:      reason,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}
		// The serialization-validity check formerly ran INSIDE the content
		// check above and inherited its reason code and message -- false
		// evidence for a style-lane parameter, which declares no content.
		// It is its own exclusion, spelled as the TS twin already spells it
		// (SS-46's recorded disagreement, resolved 2026-08-31).
		if parameter, serErr := invalidParameterSerializationFor(params, bindingSpec); parameter != "" {
			reason := serErr
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector:     selector,
					operationKey: opKey,
					reasonCode:   "openapi.parameter_serialization_excluded",
					rule:         openAPIRule(bindingSpec, "P-02"),
					message:      reason,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}

		// A style-lane parameter declaring a member with no defined
		// expansion can never be populated faithfully: every value
		// conforming to the declaration carries that member as a
		// composite, and the governing OAS style row defines no
		// representation for one. The refusal is decided by the
		// DECLARATION, so the operation is excluded here with durable
		// evidence rather than published as represented and refused at
		// invocation. See styleLaneUndefinedExpansionMember in media.go
		// for the per-edition authority reading.
		if member := styleLaneUndefinedExpansionParamFor(params, bindingSpec, isOpenAPI30(formatVersion)); member != "" {
			reason := fmt.Sprintf("parameter member %q has no expansion defined by its governing OAS style row", member)
			if onUnrealizable != nil {
				onUnrealizable(unrealizableTarget{
					selector:     selector,
					operationKey: opKey,
					reasonCode:   "openapi.parameter_style_expansion_excluded",
					rule:         openAPIRule(bindingSpec, "P-02"),
					message:      reason,
				})
				continue
			}
			return result, unrealizableOperation(opKey, reason)
		}

		inputOperation := op
		if requestBodyIgnoredForBindingSpec(bindingSpec, method) {
			copy := *op
			copy.RequestBody = nil
			inputOperation = &copy
		}
		var requestPlans []*bodyPlan
		invalidPropertyMediaCandidate := false
		if inputOperation.RequestBody != nil && inputOperation.RequestBody.Value != nil {
			plans, planErr := planRequestBodiesFor(operationDocument, op, bindingSpec)
			for _, plan := range plans {
				invalidPropertyMediaCandidate = invalidPropertyMediaCandidate || len(plan.propertyMedia) > 0
			}
			// The acceptance floor (the registered OpenAPI binding family §3): a
			// ladder-invalid request media ALTERNATIVE is a unit that is
			// malformed under its upstream authority, so it is not a
			// candidate the operation may carry. It never climbs -- the
			// operation survives on its remaining alternatives, and a
			// REQUIRED body left with none falls to the existing
			// unresolvable-request-body exclusion below (OAPI-P-04),
			// carried and not reopened. Applied BEFORE the candidate count
			// the exclusion reason is chosen from, so a body whose only
			// alternative the ladder invalidated is not misreported as a
			// flattening collision.
			plans = filterLadderInvalidAlternatives(plans, verdict, selector)
			plannedCount := len(plans)
			if planErr == nil {
				for _, plan := range plans {
					if usesRoutedInput(bindingSpec) || !candidateCollides(params, plan) {
						requestPlans = append(requestPlans, plan)
					}
				}
			}
			requiredBody := inputOperation.RequestBody.Value.Required
			if requiredBody && (planErr != nil || len(requestPlans) == 0) {
				reason := "no artifact-declared request media candidate can realize its required flattened input"
				if planErr != nil {
					reason = planErr.Error()
				}
				if onUnrealizable != nil {
					// Every plannable candidate colliding with an
					// independently declared parameter is the
					// flattening-identity refusal (OAPI-P-03); a candidate
					// set that never planned is the media-carriage refusal
					// (OAPI-P-04).
					allCollided := planErr == nil && plannedCount > 0
					code := "openapi.unresolvable_request_body"
					rule := openAPIRule(bindingSpec, effectiveRequestBodySynthesisRule(bindingSpec))
					if allCollided {
						code = "openapi.flattening_collision"
						rule = openAPIRule(bindingSpec, "P-02")
					} else {
						var dme *degenerateMediaError
						if errors.As(planErr, &dme) {
							code = "openapi.media_schema_mismatch"
						} else if invalidPropertyMediaCandidate {
							code = "openapi.media_schema_mismatch"
						}
					}
					onUnrealizable(unrealizableTarget{
						selector:     selector,
						operationKey: opKey,
						reasonCode:   code,
						rule:         rule,
						message:      reason + "; the required request body has no faithful candidate carriage",
					})
					continue
				}
				return result, unrealizableOperation(opKey, reason)
			}
			if len(requestPlans) == 0 && warn != nil {
				code := "openapi.unresolvable_request_body"
				var dme *degenerateMediaError
				if errors.As(planErr, &dme) {
					code = "openapi.media_schema_mismatch"
				}
				reason := "no artifact-declared request media candidate can realize its flattened input"
				if planErr != nil {
					reason = planErr.Error()
				}
				warn(ProjectionWarning{Code: code, Message: reason + "; optional body omitted from the synthesized contract", Path: fmt.Sprintf("operations.%s.input", opKey)})
			}
		}
		if formatVersion == "3.1" {
			if dialectErr := validateProjectedOperationDialects(operationDocument, op, params, requestPlans, bindingSpec); dialectErr != nil {
				reason := dialectErr.Error()
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{selector: selector, operationKey: opKey, reasonCode: "openapi.unsupported_schema_dialect", rule: "OBI-D-06", message: reason})
					continue
				}
				return result, unrealizableOperation(opKey, reason)
			}
		}

		obiOp := ProjectionOperation{
			Description: operationDescription(op),
			Deprecated:  op.Deprecated,
		}

		if len(op.Tags) > 0 {
			obiOp.Tags = op.Tags
		}

		opPointer := "#/operations/" + escapeJSONPointerSegment(opKey)
		routes := planAbstractInputRoutes(params, requestPlans)
		inputSchema := buildInputSchemaForPlans(inputOperation, params, requestPlans, &requestGraph, routes, schemaOverlays)
		if inputSchema != nil {
			// Project, then decycle — the TypeScript engine's order, and the
			// one the cut-point question is asked in: the decycler walks the
			// graph this direction actually emits. Projecting the root reads
			// annotations through the unprojected registry, because a
			// reference has not been inlined yet.
			projected := projectOpenAPISchemaWithRegistry(inputSchema, openAPIRequestSchema, requestSchemaProjectionExemptions(routes), refRegistry)
			inlined, hoisted := inlineRefsInOperationSchema(projected, requestGraph.registry, requestGraph.cyclic, opPointer+"/input", namer)
			restored := restoreBooleanSchemas(pruneUnreachableDefs(inlined, opPointer+"/input", hoisted))
			if object, ok := restored.(map[string]any); ok {
				obiOp.Input = translateSchemaDialect(object, formatVersion)
			} else {
				obiOp.Input = restored
			}
		}

		var outputSchema map[string]any
		if !floorInvalidOutputProjection(verdict) {
			outputSchema = buildOutputSchemaWithCyclicRefs(op, schemaOverlays, bindingSpec, &responseGraph, operationDocument.OpenAPI)
		}
		if outputSchema != nil {
			projected := projectOpenAPISchemaWithRegistry(outputSchema, openAPIResponseSchema, nil, refRegistry)
			inlined, hoisted := inlineRefsInOperationSchema(projected, responseGraph.registry, responseGraph.cyclic, opPointer+"/output", namer)
			restored := restoreBooleanSchemas(pruneUnreachableDefs(inlined, opPointer+"/output", hoisted))
			if object, ok := restored.(map[string]any); ok {
				obiOp.Output = translateSchemaDialect(object, formatVersion)
			} else {
				obiOp.Output = restored
			}
		}

		result.Operations[opKey] = obiOp

		bindingKey := opKey
		binding := ProjectionBinding{
			Operation: opKey,
			Selector:  selector,
		}
		if usesRoutedInput(bindingSpec) && (len(routes.parameters) > 0 || len(routes.bodyFields) > 0 || routes.wholeBodyField != "" || routes.openBody || routes.bodyRequired) {
			binding.Input = projectionInputCorrespondence(params, routes)
		}
		result.Bindings[bindingKey] = binding
	}

	if err := synthesizeInboundDependencies(
		&result, doc, artifact, bindingSpec, formatVersion, usedKeys,
		refRegistry, &requestGraph, &responseGraph, namer,
		schemaOverlays, onUnrealizable,
	); err != nil {
		return result, err
	}

	return result, nil
}

func projectionInputCorrespondence(params openapi3.Parameters, routes abstractInputRoutes) *ProjectionInputCorrespondence {
	locations := map[string]string{}
	qualified := false
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		parameter := ref.Value
		if previous, present := locations[parameter.Name]; present && previous != parameter.In {
			qualified = true
		}
		locations[parameter.Name] = parameter.In
	}
	result := &ProjectionInputCorrespondence{
		BodyProperties: make(map[string]string, len(routes.bodyFields)),
		WholeBodyField: routes.wholeBodyField,
		OpenBody:       routes.openBody,
		BodyRequired:   routes.bodyRequired,
	}
	for _, route := range routes.parameters {
		callerKey := route.Name
		if qualified {
			callerKey = route.In + "/" + escapeJSONPointerSegment(route.Name)
		}
		result.Parameters = append(result.Parameters, ProjectionParameterRoute{
			In: route.In, Name: route.Name, Field: route.Field, CallerKey: callerKey,
		})
	}
	for name, field := range routes.bodyFields {
		result.BodyProperties[name] = field
	}
	return result
}

func traceTargetHasNoSafeInvocation(doc *openapi3.T, op *openapi3.Operation, params openapi3.Parameters) bool {
	for _, ref := range params {
		if ref != nil && ref.Value != nil && ref.Value.Required &&
			(ref.Value.In == openapi3.ParameterInCookie || ref.Value.In == openapi3.ParameterInHeader && strings.EqualFold(ref.Value.Name, "Cookie")) {
			return true
		}
	}
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return false
	}
	for _, requirement := range *requirements {
		if len(requirement) == 0 || !securityRequirementEmitsCredential(doc, op, requirement) {
			return false
		}
	}
	return true
}

func contextWithConfigurationPoint(base map[string]any, point string, value any) map[string]any {
	result := map[string]any{}
	for key, member := range base {
		result[key] = member
	}
	result[point] = value
	return result
}

func securityDocumentForProjection(document *openapi3.T, operation *openapi3.Operation, context map[string]any) *openapi3.T {
	scope, _ := context["implicitConnectionScope"].(string)
	if scope != string(ImplicitConnectionReferring) || operation == nil || operation.Extensions == nil {
		return document
	}
	schemes, _ := operation.Extensions[referringSecuritySchemesMarker].(openapi3.SecuritySchemes)
	return securityDocumentForScope(document, schemes, ImplicitConnectionReferring)
}

func viableSecurityPlansWithContext(document *openapi3.T, operation *openapi3.Operation, context map[string]any, baseURL string, params openapi3.Parameters) []securityPlan {
	return viableSecurityPlans(securityDocumentForProjection(document, operation, context), operation, baseURL, params)
}

func securityPlansWithContext(document *openapi3.T, operation *openapi3.Operation, baseURL string, context map[string]any) []securityPlan {
	return securityPlans(securityDocumentForProjection(document, operation, context), operation, baseURL)
}

func securityPlanCarriageError(plan securityPlan, params openapi3.Parameters) error {
	return checkCredentialCollisions(credentialDestinations(plan), params, nil)
}

func securitySchemeForOperation(document *openapi3.T, operation *openapi3.Operation, name string, context map[string]any) (*openapi3.SecurityScheme, bool) {
	selected := securityDocumentForProjection(document, operation, context)
	if selected == nil || selected.Components == nil {
		return nil, false
	}
	ref, found := selected.Components.SecuritySchemes[name]
	return refValue(ref), found && ref != nil && ref.Value != nil
}

func refValue(ref *openapi3.SecuritySchemeRef) *openapi3.SecurityScheme {
	if ref == nil {
		return nil
	}
	return ref.Value
}

func malformedSecurityScheme(scheme *openapi3.SecurityScheme) error {
	if scheme == nil {
		return fmt.Errorf("Security Scheme Object is absent")
	}
	switch scheme.Type {
	case "apiKey":
		if scheme.Name == "" || scheme.In != "header" && scheme.In != "query" && scheme.In != "cookie" {
			return fmt.Errorf("apiKey Security Scheme requires a name and supported location")
		}
	case "http":
		if scheme.Scheme == "" {
			return fmt.Errorf("HTTP Security Scheme requires scheme")
		}
	case "oauth2":
		if scheme.Flows == nil {
			return fmt.Errorf("OAuth2 Security Scheme requires flows")
		}
	case "openIdConnect":
		if scheme.OpenIdConnectUrl == "" {
			return fmt.Errorf("OpenID Connect Security Scheme requires a URL")
		}
	case "mutualTLS":
	default:
		return fmt.Errorf("unsupported Security Scheme type %q", scheme.Type)
	}
	return nil
}

func requiredRequestContentEncoding(params openapi3.Parameters) bool {
	for _, ref := range params {
		if ref != nil && ref.Value != nil && ref.Value.Required && ref.Value.In == openapi3.ParameterInHeader && strings.EqualFold(ref.Value.Name, "Content-Encoding") {
			return true
		}
	}
	return false
}

func operationHasRequestRepresentation(doc *openapi3.T, op *openapi3.Operation, bindingSpec, method string) bool {
	if requestBodyIgnoredForBindingSpec(bindingSpec, method) || op == nil || op.RequestBody == nil || op.RequestBody.Value == nil || len(op.RequestBody.Value.Content) == 0 {
		return false
	}
	plans, err := planRequestBodiesFor(doc, op, bindingSpec)
	return err == nil && len(plans) > 0
}

func effectiveRequestBodySynthesisRule(profile string) string {
	if profile == projectionOpenAPI30 {
		return "S-22"
	}
	return "S-21"
}

func filterLadderInvalidAlternatives(plans []*bodyPlan, verdict *floorOp, operationRef string) []*bodyPlan {
	if verdict == nil || len(verdict.InvalidAlternatives) == 0 {
		return plans
	}
	kept := plans[:0:0]
	for _, plan := range plans {
		if plan != nil && plan.declared {
			alternativeRef := operationRef + "/requestBody/content/" + escapeJSONPointerSegment(plan.mediaKey)
			if _, invalid := verdict.InvalidAlternatives[alternativeRef]; invalid {
				continue
			}
		}
		kept = append(kept, plan)
	}
	return kept
}

// floorInvalidOutputProjection reports an upstream-invalid success body
// schema. The operation and governing Response survive, but the invalid body
// projection cannot be copied into an OBI JSON Schema. Other response-member
// defects (description, headers, links, examples) do not suppress a valid body
// sibling and therefore are deliberately ignored here.
func floorInvalidOutputProjection(verdict *floorOp) bool {
	if verdict == nil {
		return false
	}
	for _, defect := range verdict.Projections[verdict.Ref] {
		if defect.Class == floorD1 || defect.Class == floorD1s || defect.Class == floorD15 || defect.Class == floorURef {
			return true
		}
	}
	return false
}

func restoreBooleanSchemas(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if literal, ok := structuralBooleanSchemaLiteral(typed); ok {
			return literal
		}
		for key, child := range typed {
			typed[key] = restoreBooleanSchemas(child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = restoreBooleanSchemas(child)
		}
		return typed
	default:
		return value
	}
}

// unrealizableOperation builds the error returned when an operation has no
// faithful carriage and the caller supplied no unrealizable sink. Callers that
// do supply one (the coverage path) instead record the position as an excluded
// coverage entry carrying its reason code, rule and artifact pointer, and
// synthesis continues past the operation. Without that sink there is nowhere to
// disclose the exclusion, so synthesis fails rather than return an interface
// whose operation set is silently narrower than the artifact declared.
func unrealizableOperation(operationKey, reason string) error {
	return fmt.Errorf("cannot synthesize OpenAPI operation %q: %s; synthesis would return a statically unbindable partial interface", operationKey, reason)
}

// loadDocument loads and discriminates an OpenAPI source per
// the registered OpenAPI binding family §3-§6: `content`, when present, is the artifact
// (content primacy), with a co-present `location` serving as the embedded
// artifact's BASE URI — relative $refs resolve against it exactly as they
// would had the document been retrieved from that address (OAPI-D-01/D-02,
// §6). Embedded content with no location has no base and must be
// self-contained: a relative external $ref then fails with a readable error
// (absolute http(s) $refs still resolve — they need no base). The artifact's
// own `openapi` field discriminates the accepted editions (OAPI-P-01).
//
// String content parses as YAML 1.2 (JSON being a valid subset); duplicate
// mapping keys are refused loudly by the YAML layer itself, satisfying the
// §3 duplicate-key pin.
func deriveOperationKey(op *openapi3.Operation, path, method string, used map[string]bool) string {
	if op.OperationID != "" {
		key := projectionSanitizeKey(op.OperationID)
		if !used[key] {
			return key
		}
	}

	segments := strings.Split(strings.Trim(path, "/"), "/")
	var parts []string
	for _, seg := range segments {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue
		}
		if seg != "" {
			parts = append(parts, seg)
		}
	}

	key := strings.Join(parts, ".") + "." + strings.ToLower(method)
	key = projectionSanitizeKey(key)
	return projectionUniqueKey(key, used)
}

func operationDescription(op *openapi3.Operation) string {
	if op.Description != "" {
		return op.Description
	}
	return op.Summary
}

func buildJSONPointerSelector(path, method string) string {
	escaped := strings.ReplaceAll(path, "~", "~0")
	escaped = strings.ReplaceAll(escaped, "/", "~1")
	return "#/paths/" + escaped + "/" + strings.ToLower(method)
}

func buildInputSchemaForPlans(op *openapi3.Operation, allParams openapi3.Parameters, requestPlans []*bodyPlan, graph *directionGraph, routes abstractInputRoutes, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return buildInputSchema(op, allParams, nil, graph, routes, schemaOverlays)
	}
	var variants []map[string]any
	if !op.RequestBody.Value.Required {
		if parameterOnly := buildInputSchema(op, allParams, nil, graph, routes, schemaOverlays); parameterOnly != nil {
			variants = append(variants, parameterOnly)
		}
	}
	for _, plan := range requestPlans {
		if schema := buildInputSchema(op, allParams, plan, graph, routes, schemaOverlays); schema != nil {
			variants = append(variants, schema)
		}
	}
	seen := map[string]bool{}
	unique := make([]map[string]any, 0, len(variants))
	for _, schema := range variants {
		encoded, _ := json.Marshal(schema)
		key := string(encoded)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, schema)
		}
	}
	if len(unique) == 0 {
		return nil
	}
	if len(unique) == 1 {
		return unique[0]
	}
	anyOf := make([]any, len(unique))
	for i, schema := range unique {
		anyOf[i] = schema
	}
	return map[string]any{"anyOf": anyOf}
}

func buildInputSchema(op *openapi3.Operation, allParams openapi3.Parameters, requestPlan *bodyPlan, graph *directionGraph, routes abstractInputRoutes, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	properties := map[string]any{}
	var required []string
	// Only JSON-family object candidates can carry undeclared fields. The
	// parameter-only, multipart/form, and scalar-body surfaces stay closed.
	hasOpenBody := planAllowsObjectPassthrough(requestPlan)

	for _, paramRef := range allParams {
		if paramRef == nil || paramRef.Value == nil {
			continue
		}
		param := paramRef.Value

		prop := paramToSchema(param, graph, schemaOverlays)
		field := routes.parameterField(param.In, param.Name)
		if prop != nil {
			properties[field] = prop
		}

		if param.Required {
			required = append(required, field)
		}
	}

	if op.RequestBody != nil && op.RequestBody.Value != nil && requestPlan != nil {
		rb := op.RequestBody.Value
		var bodySchema map[string]any
		// A schema that asserts nothing is the same declaration as an omitted
		// one (§9.2), so the byte lane it selects synthesizes the canonical
		// boundary schema rather than decorating an empty declaration.
		assertionFreeByteLane := requestPlan.rawBoundary && requestPlan.media != nil &&
			requestPlan.media.Schema != nil && schemaAssertsNothing(requestPlan.media.Schema.Value)
		if requestPlan.media != nil && requestPlan.media.Schema != nil && !assertionFreeByteLane {
			bodySchema = graph.declaredForm(requestPlan.media.Schema, schemaOverlays)
		} else if requestPlan.media != nil && requestPlan.media.ItemSchema != nil &&
			(requestPlan.family == familySequential || requestPlan.family == familyMultipart ||
				requestPlan.mediaRange && requestPlan.bindingSpec == projectionOpenAPI32) {
			bodySchema = map[string]any{
				"type":  "array",
				"items": graph.declaredForm(requestPlan.media.ItemSchema, schemaOverlays),
			}
		} else if requestPlan.rawBoundary {
			bodySchema = map[string]any{"type": "string", "contentEncoding": "base64"}
		} else if hasMediaFidelity(requestPlan.bindingSpec) && requestPlan.synthetic {
			// A schema-omitted revision-3 JSON/range declaration allows any
			// application JSON value. Preserve that unconstrained whole-body
			// value under the protocol-neutral body field.
			bodySchema = map[string]any{}
		}
		if bodySchema != nil {
			// Resolve a $ref body BEFORE the flatten decision: bodies
			// declared by reference are the production norm, and wrapping
			// the unresolved {"$ref"} in a phantom "body" property emits a
			// contract the invoker then sends literally onto the wire.
			if _, isRef := bodySchema["$ref"]; isRef && !graph.hoists(requestPlan.media.Schema) {
				// nil decycle context: this expansion only informs the
				// flatten decision; the embedded schema is decycled later by
				// inlineRefsInOperationSchema.
				if resolved, ok := inlineRefs(bodySchema, graph.registryOrNil(), map[string]bool{}, nil).(map[string]any); ok {
					bodySchema = resolved
				}
			}
			if requestPlan.rawBoundary {
				// This is a caller-boundary annotation, not an application-schema
				// replacement: retain every authored keyword and add only the
				// Base64 spelling required to carry raw bytes through JSON values.
				bodySchema["contentEncoding"] = "base64"
			}
			var bodyObject bool
			var bodyProps map[string]any
			var bodyRequired map[string]bool
			if requestPlan.media != nil && requestPlan.media.Schema != nil {
				bodyObject, bodyProps, bodyRequired = resolvedSynthesisBodyShape(requestPlan.media.Schema.Value, map[*openapi3.Schema]bool{}, graph, schemaOverlays)
			} else {
				bodyProps = map[string]any{}
				bodyRequired = map[string]bool{}
			}
			hasProps := len(bodyProps) > 0
			if hasMediaFidelity(requestPlan.bindingSpec) && requestPlan.oas30 && requestPlan.family == familyMultipart {
				for _, property := range bodyProps {
					if propertySchema, ok := property.(map[string]any); ok {
						decorateMultipartPartBinaryBoundary(propertySchema)
					}
				}
			}
			switch {
			case !bodyObject || requestPlan.wholeObject:
				// A non-object body, an explicitly dynamic object, or a
				// declaration-complex JSON body rides as one
				// protocol-independent application value. Non-object
				// schemas include array, scalar, binary, or
				// TYPELESS (neither `properties` nor an explicit object
				// type; §9.1's determination is declaration-only): the
				// flattened contract carries it under the synthetic
				// `body` property, unwrapped at the wire.
				field := routes.wholeBodyField
				if field == "" {
					field = syntheticBodyProperty
				}
				properties[field] = bodySchema
				if rb.Required {
					required = append(required, field)
				}
			case hasProps:
				for k, v := range bodyProps {
					properties[routes.bodyField(k)] = v
				}
				for name := range bodyRequired {
					required = append(required, routes.bodyField(name))
				}
			default:
				// A free-form object body (type object, no named
				// properties): the flattened model passes unmatched input
				// fields through into the body (the registered OpenAPI binding family
				// §9.1), so the flattened surface stays an OPEN object —
				// the synthetic `body` wrap is reserved for NON-object
				// body schemas, and wrapping here would describe a field
				// the conformant invoker refuses as unmatched.
				// hasOpenBody was determined by the selected candidate's family.
			}
		}
	}

	if len(properties) == 0 {
		if hasOpenBody {
			return map[string]any{"type": "object"}
		}
		if requestPlan != nil && op.RequestBody != nil && op.RequestBody.Value != nil && op.RequestBody.Value.Required {
			return map[string]any{"type": "object", "additionalProperties": false}
		}
		return nil
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if !hasOpenBody {
		schema["additionalProperties"] = false
	}
	if len(required) > 0 {
		sort.Strings(required)
		requiredValues := make([]any, len(required))
		for i, name := range required {
			requiredValues[i] = name
		}
		schema["required"] = requiredValues
	}
	return schema
}

func decorateMultipartPartBinaryBoundary(schema map[string]any) {
	if schema == nil {
		return
	}
	if projectedSchemaTypeIs(schema, "string") && projectedSchemaFormatIs(schema, "binary") {
		schema["contentEncoding"] = "base64"
		return
	}
	if !projectedSchemaTypeIs(schema, "array") {
		return
	}
	for _, candidate := range append([]map[string]any{schema}, projectedSchemaList(schema["allOf"])...) {
		items, _ := candidate["items"].(map[string]any)
		if items != nil && projectedSchemaTypeIs(items, "string") && projectedSchemaFormatIs(items, "binary") {
			items["contentEncoding"] = "base64"
		}
	}
}

func projectedSchemaTypeIs(schema map[string]any, want string) bool {
	if value, ok := schema["type"].(string); ok && value == want {
		return true
	}
	for _, child := range projectedSchemaList(schema["allOf"]) {
		if projectedSchemaTypeIs(child, want) {
			return true
		}
	}
	return false
}

func projectedSchemaFormatIs(schema map[string]any, want string) bool {
	if value, ok := schema["format"].(string); ok && value == want {
		return true
	}
	for _, child := range projectedSchemaList(schema["allOf"]) {
		if projectedSchemaFormatIs(child, want) {
			return true
		}
	}
	return false
}

func projectedSchemaList(value any) []map[string]any {
	values, _ := value.([]any)
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if schema, ok := value.(map[string]any); ok {
			out = append(out, schema)
		}
	}
	return out
}

// resolvedSynthesisBodyShape resolves the declaration-only object surface
// used by OAPI-P-03 synthesis. allOf contributes its recursive property and
// required-name union; wrapping the allOf node as a synthetic whole body
// would publish a contract that the invoker (correctly) routes as object
// properties.
func resolvedSynthesisBodyShape(schema *openapi3.Schema, seen map[*openapi3.Schema]bool, graph *directionGraph, schemaOverlays *rawSchemaOverlayCollector) (bool, map[string]any, map[string]bool) {
	properties := map[string]any{}
	required := map[string]bool{}
	if schema == nil || seen[schema] {
		return false, properties, required
	}
	seen[schema] = true
	defer delete(seen, schema)

	object := schema.Type.Is("object") || schema.Properties != nil
	for name, property := range schema.Properties {
		properties[name] = graph.declaredForm(property, schemaOverlays)
	}
	for _, name := range schema.Required {
		required[name] = true
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		memberObject, memberProperties, memberRequired := resolvedSynthesisBodyShape(member.Value, seen, graph, schemaOverlays)
		object = object || memberObject
		for name, property := range memberProperties {
			if existing, present := properties[name]; present {
				properties[name] = map[string]any{"allOf": []any{existing, property}}
			} else {
				properties[name] = property
			}
		}
		for name := range memberRequired {
			required[name] = true
		}
	}
	return object, properties, required
}

func paramToSchema(param *openapi3.Parameter, graph *directionGraph, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	if param.Schema != nil && param.Schema.Value != nil {
		// A Parameter Object description belongs to the PARAMETER, and merging it
		// produces a schema that is no longer the referenced component — so the
		// merged schema is not that cut point, and the component still cuts
		// wherever it is referenced inside. Both engines read it that way; the
		// TypeScript twin's merge allocates a new node for the same reason
		// (packages/openapi/src/synthesize.ts, paramToSchema).
		schema := graph.parameterForm(param.Schema, param.Description != "", schemaOverlays)
		if param.Description != "" {
			if schema == nil {
				schema = map[string]any{}
			}
			schema = schemaWithParameterDescription(schema, param.Description)
		}
		return schema
	}
	if len(param.Content) > 0 {
		keys := make([]string, 0, len(param.Content))
		for key := range param.Content {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if media := param.Content[keys[0]]; media != nil && media.Schema != nil {
			schema := graph.parameterForm(media.Schema, param.Description != "", schemaOverlays)
			if param.Description != "" {
				schema = schemaWithParameterDescription(schema, param.Description)
			}
			return schema
		}
	}

	prop := map[string]any{"type": "string"}
	if param.Description != "" {
		prop["description"] = param.Description
	}
	return prop
}

func schemaWithParameterDescription(schema map[string]any, description string) map[string]any {
	if _, boolean := structuralBooleanSchemaLiteral(schema); boolean {
		// A JSON Schema boolean cannot carry siblings. Preserve both authorial
		// facts by wrapping the literal in allOf, matching the TypeScript SDK's
		// reversible projection.
		return map[string]any{"allOf": []any{schema}, "description": description}
	}
	schema["description"] = description
	return schema
}

// unsupportedParameterContentFor returns the first content-form parameter
// whose single media declaration the named binding family cannot serialize.
// Creation-time soundness requires excluding the target rather than emitting
// an operation that is statically guaranteed to refuse when that parameter is
// populated.

// invalidParameterSerializationFor reports the first effective parameter
// whose declared serialization method the governing edition defines no
// expansion for, with the validator's own message as the evidence.
func invalidParameterSerializationFor(params openapi3.Parameters, bindingSpec string) (string, string) {
	if !hasMediaFidelity(bindingSpec) || bindingSpec == projectionOpenAPI32 {
		return "", ""
	}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		if err := validateRevision3ParameterSerialization(ref.Value, bindingSpec == projectionOpenAPI30); err != nil {
			return ref.Value.Name, fmt.Sprintf("parameter %q: %s", ref.Value.Name, err.Error())
		}
	}
	return "", ""
}

func unsupportedParameterContentFor(params openapi3.Parameters, bindingSpec string) string {
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		param := ref.Value
		if len(param.Content) == 0 {
			continue
		}
		if len(param.Content) != 1 {
			return param.Name
		}
		for mediaKey := range param.Content {
			var parsed parsedMediaType
			var err error
			if hasMediaFidelity(bindingSpec) {
				parsed, err = parseRevision3MediaType(mediaKey)
			} else {
				parsed, err = parseMediaType(mediaKey)
			}
			queryStringForm := bindingSpec == projectionOpenAPI32 &&
				param.In == ParameterInQueryString &&
				parsed.base == "application/x-www-form-urlencoded"
			if err != nil || (!queryStringForm && !isJSONMediaType(parsed.base) && parsed.base != "text/plain") {
				return param.Name
			}
			if hasMediaFidelity(bindingSpec) && parsed.base == "text/plain" && supportedTextCharset(parsed) != nil {
				return param.Name
			}
		}
	}
	return ""
}

func buildOutputSchema(op *openapi3.Operation, schemaOverlays *rawSchemaOverlayCollector, bindingSpec string, openapiVersions ...string) map[string]any {
	return buildOutputSchemaWithCyclicRefs(op, schemaOverlays, bindingSpec, nil, openapiVersions...)
}

func buildOutputSchemaWithCyclicRefs(op *openapi3.Operation, schemaOverlays *rawSchemaOverlayCollector, bindingSpec string, graph *directionGraph, openapiVersions ...string) map[string]any {
	openapiVersion := "3.0"
	if len(openapiVersions) > 0 {
		openapiVersion = openapiVersions[0]
	}
	if op.Responses == nil {
		return nil
	}

	responses := op.Responses.Map()
	keys := make([]string, 0, len(responses))
	hasRange := false
	exactSuccesses := 0
	for key := range responses {
		keys = append(keys, key)
		if key == "2XX" {
			hasRange = true
		}
		if len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9' {
			exactSuccesses++
		}
	}
	sort.Strings(keys)
	var schemas []map[string]any
	seen := map[string]bool{}
	cyclicRootRefs := map[string]string{}
	appendSchema := func(schema map[string]any, cyclicRootRef string) {
		encoded, _ := json.Marshal(schema)
		identity := string(encoded)
		if cyclicRootRef != "" {
			cyclicRootRefs[identity] = cyclicRootRef
		}
		if !seen[identity] {
			seen[identity] = true
			schemas = append(schemas, schema)
		}
	}
	for _, key := range keys {
		isExact := len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9'
		if !isExact && key != "2XX" && !(key == "default" && !hasRange && exactSuccesses < 100) {
			continue
		}
		ref := responses[key]
		if ref == nil || ref.Value == nil || len(ref.Value.Content) == 0 {
			continue // this outcome emits no value
		}
		mediaKeys := make([]string, 0, len(ref.Value.Content))
		for mediaKey := range ref.Value.Content {
			mediaKeys = append(mediaKeys, mediaKey)
		}
		sort.Strings(mediaKeys)
		for _, mediaKey := range mediaKeys {
			var parsed parsedMediaType
			var err error
			if hasMediaFidelity(bindingSpec) {
				parsed, err = parseMediaDeclaration(mediaKey)
			} else {
				parsed, err = parseMediaType(mediaKey)
			}
			if err != nil || (parsed.rangeSpecificity < 2 && !hasResponseFidelity(bindingSpec)) {
				continue
			}
			media := ref.Value.Content[mediaKey]
			if bindingSpec == projectionOpenAPI32 {
				kind, sequentialErr := ClassifyOpenAPI32SequentialResponse(mediaKey, media)
				if sequentialErr != nil {
					continue
				}
				if kind != "" {
					if media == nil || media.ItemSchema == nil {
						return nil // the artifact makes no per-item operation-value claim
					}
					cyclicRootRef := ""
					if media.ItemSchema.Ref != "" && graph.isCyclic(media.ItemSchema.Ref) {
						cyclicRootRef = media.ItemSchema.Ref
					}
					appendSchema(graph.rootForm(media.ItemSchema, schemaOverlays), cyclicRootRef)
					continue
				}
			}
			admitsJSON := isJSONMediaType(parsed.base) || (parsed.rangeSpecificity < 2 && (parsed.base == "application/*" || parsed.base == "*/*"))
			if admitsJSON {
				if media == nil || media.Schema == nil {
					return nil // an unconstrained JSON success can emit any JSON value
				}
				cyclicRootRef := ""
				if media.Schema.Ref != "" && graph.isCyclic(media.Schema.Ref) {
					cyclicRootRef = media.Schema.Ref
				}
				appendSchema(graph.rootForm(media.Schema, schemaOverlays), cyclicRootRef)
			}
			admitsNonJSON := !isJSONMediaType(parsed.base) || parsed.rangeSpecificity < 2
			if admitsNonJSON {
				// The 3.2 scalar codec returns the declaration-selected
				// application value, so its operation contract must retain
				// that schema instead of unconditionally claiming string.
				if bindingSpec == projectionOpenAPI32 && mediaSchema(media) != nil && openAPI32NonJSONTextKind(mediaSchema(media)) != "" {
					cyclicRootRef := ""
					if media.Schema.Ref != "" && graph.isCyclic(media.Schema.Ref) {
						cyclicRootRef = media.Schema.Ref
					}
					appendSchema(graph.rootForm(media.Schema, schemaOverlays), cyclicRootRef)
					continue
				}
				rawBoundary := hasResponseFidelity(bindingSpec) && !strings.HasPrefix(parsed.base, "text/") &&
					((isOpenAPI30(majorMinor(openapiVersion)) &&
						((hasSchemaOmittedOAS30ByteCarriage(bindingSpec) && parsed.rangeSpecificity == 2 && mediaSchema(media) == nil) || binarySignaled(mediaSchema(media), true))) ||
						(!isOpenAPI30(majorMinor(openapiVersion)) && media != nil && media.Schema == nil))
				// The revision-1 builtin non-JSON lane emits text, including
				// one text value per SSE event. The artifact-authorized
				// byte lane uses canonical Base64 at the operation boundary.
				if rawBoundary {
					appendSchema(map[string]any{"type": "string", "contentEncoding": "base64"}, "")
				} else {
					appendSchema(map[string]any{"type": "string"}, "")
				}
			}
		}
	}
	if len(schemas) == 0 {
		return nil
	}
	if len(schemas) == 1 {
		return schemas[0]
	}
	anyOf := make([]any, len(schemas))
	for i, schema := range schemas {
		encoded, _ := json.Marshal(schema)
		if ref := cyclicRootRefs[string(encoded)]; ref != "" {
			// The TypeScript processor retains dereferenced object identity:
			// a cyclic component below an anyOf root is hoisted as a unit. Go's
			// typed marshal would otherwise inline that first occurrence and
			// hoist only its nested back-edge. Keep the artifact ref long enough
			// for inlineRefsInOperationSchema to make the same $defs projection.
			anyOf[i] = map[string]any{"$ref": ref}
		} else {
			anyOf[i] = schema
		}
	}
	return map[string]any{"anyOf": anyOf}
}

// stringSlice extracts a string list from a schema field that may be
// []string (Go-built schemas) or []any (anything that round-tripped
// through JSON — the required-ness of body fields was silently dropped
// when only []string was handled).
func stringSlice(v any) []string {
	switch vals := v.(type) {
	case []string:
		return vals
	case []any:
		out := make([]string, 0, len(vals))
		for _, item := range vals {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func schemaRefToMap(ref *openapi3.SchemaRef, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	if ref == nil || ref.Value == nil {
		return nil
	}

	// The loader has already resolved ref.Value. Marshal that resolved schema,
	// not the SchemaRef wrapper: the wrapper intentionally serializes as the
	// original `$ref`, which is meaningful inside the OpenAPI artifact but
	// dangles once this schema is projected into an operation-local OBI
	// contract. Nested refs remain visible and are handled by inlineRefs.
	data, err := ref.Value.MarshalJSON()
	if err != nil {
		return map[string]any{"type": "object", "x-conversion-error": err.Error()}
	}

	var result map[string]any
	if err := unmarshalJSONImage(data, &result); err != nil {
		return map[string]any{"type": "object", "x-conversion-error": err.Error()}
	}

	delete(result, "__origin__")
	if schemaOverlays != nil {
		schemaOverlays.apply(ref, result)
	}

	return result
}

// buildRefRegistry constructs a map of `$ref string → fully-marshaled
// resolved schema` from `doc.Components.Schemas`. The resulting values
// are themselves the OUTPUT of marshaling each component schema with
// kin-openapi (which still leaves nested `$ref` strings in place);
// inlineRefs walks them recursively to fully flatten.
//
// This is used to post-process operation input/output schemas built
// by buildInputSchema / buildOutputSchema, which serialize via
// kin-openapi's `SchemaRef.MarshalJSON` and thus carry `$ref` strings
// pointing into `#/components/schemas/X`. The OBI consumer (codegen)
// has no `components/schemas/` namespace of its own, so any unresolved
// ref becomes `unknown` in the generated client. Inlining everything
// at create time keeps the OBI self-contained.
func buildRefRegistry(doc *openapi3.T, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	registry := make(map[string]any)
	if doc == nil || doc.Components == nil {
		return registry
	}
	for name, schemaRef := range doc.Components.Schemas {
		if schemaRef == nil || schemaRef.Value == nil {
			continue
		}
		// Marshal the resolved component value. A SchemaRef wrapper whose
		// component is itself an alias would otherwise register only the alias
		// `$ref` and fail to materialize the schema at an operation boundary.
		data, err := schemaRef.Value.MarshalJSON()
		if err != nil {
			continue
		}
		var v map[string]any
		if err := unmarshalJSONImage(data, &v); err != nil {
			continue
		}
		delete(v, "__origin__")
		if schemaOverlays != nil {
			schemaOverlays.apply(schemaRef, v)
		}
		registry["#/components/schemas/"+escapeJSONPointerSegment(name)] = v
	}
	return registry
}

// schemaTraversalPosition keeps JSON-shaped annotation and extension data
// opaque while walking a JSON Schema. A key named "$ref" has reference
// semantics only at a schema position; the same spelling in default,
// example, enum, const, or an extension is ordinary application data.
type schemaTraversalPosition uint8

const (
	schemaObjectPosition schemaTraversalPosition = iota
	schemaMapPosition
	schemaArrayPosition
	schemaDataPosition
)

func schemaChildPosition(key string) schemaTraversalPosition {
	switch {
	case schemaBearingSingleKeys[key]:
		return schemaObjectPosition
	case schemaBearingMapKeys[key]:
		return schemaMapPosition
	case schemaBearingArrayKeys[key]:
		return schemaArrayPosition
	default:
		return schemaDataPosition
	}
}

func validateProjectedOperationDialects(doc *openapi3.T, op *openapi3.Operation, params openapi3.Parameters, requestPlans []*bodyPlan, bindingSpec string) error {
	documentDialect := doc.JSONSchemaDialect
	if documentDialect == "" {
		documentDialect = "https://spec.openapis.org/oas/3.1/dialect/base"
	}
	seen := map[struct {
		schema  *openapi3.Schema
		dialect string
	}]bool{}
	validate := func(side string, ref *openapi3.SchemaRef) error {
		if err := validateSchemaRefDialect(ref, documentDialect, seen); err != nil {
			return fmt.Errorf("%s schema uses %w", side, err)
		}
		return nil
	}
	for _, parameterRef := range params {
		if parameterRef == nil || parameterRef.Value == nil {
			continue
		}
		parameter := parameterRef.Value
		if parameter.Schema != nil {
			if err := validate("input", parameter.Schema); err != nil {
				return err
			}
			continue
		}
		keys := make([]string, 0, len(parameter.Content))
		for key := range parameter.Content {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			if media := parameter.Content[keys[0]]; media != nil && media.Schema != nil {
				if err := validate("input", media.Schema); err != nil {
					return err
				}
			}
		}
	}
	for _, plan := range requestPlans {
		if plan != nil && plan.media != nil && plan.media.Schema != nil {
			if err := validate("input", plan.media.Schema); err != nil {
				return err
			}
		}
	}
	for _, ref := range projectedOutputSchemaRefs(op, bindingSpec) {
		if err := validate("output", ref); err != nil {
			return err
		}
	}
	return nil
}

func validateSchemaRefDialect(ref *openapi3.SchemaRef, inherited string, seen map[struct {
	schema  *openapi3.Schema
	dialect string
}]bool) error {
	if ref == nil || ref.Value == nil {
		return nil
	}
	schema := ref.Value
	dialect := inherited
	if schema.SchemaDialect != "" {
		dialect = schema.SchemaDialect
	}
	if !supportedComposingDialect(dialect) {
		return fmt.Errorf("unsupported OpenAPI 3.1 schema dialect %q; portable OBI synthesis is pinned to JSON Schema 2020-12 (OBI-D-06)", dialect)
	}
	key := struct {
		schema  *openapi3.Schema
		dialect string
	}{schema: schema, dialect: dialect}
	if seen[key] {
		return nil
	}
	seen[key] = true
	children := make([]*openapi3.SchemaRef, 0, len(schema.OneOf)+len(schema.AnyOf)+len(schema.AllOf)+len(schema.Properties)+len(schema.PatternProperties)+len(schema.DependentSchemas)+len(schema.Defs)+12)
	children = append(children, schema.OneOf...)
	children = append(children, schema.AnyOf...)
	children = append(children, schema.AllOf...)
	children = append(children, schema.Not, schema.Items, schema.Contains, schema.PropertyNames, schema.If, schema.Then, schema.Else, schema.ContentSchema)
	children = append(children, schema.AdditionalProperties.Schema, schema.UnevaluatedItems.Schema, schema.UnevaluatedProperties.Schema)
	children = append(children, schema.PrefixItems...)
	for _, schemas := range []openapi3.Schemas{schema.Properties, schema.PatternProperties, schema.DependentSchemas, schema.Defs} {
		for _, child := range schemas {
			children = append(children, child)
		}
	}
	for _, child := range children {
		if err := validateSchemaRefDialect(child, dialect, seen); err != nil {
			return err
		}
	}
	return nil
}

func projectedOutputSchemaRefs(op *openapi3.Operation, bindingSpec string) []*openapi3.SchemaRef {
	if op == nil || op.Responses == nil {
		return nil
	}
	responses := op.Responses.Map()
	keys := make([]string, 0, len(responses))
	hasRange := false
	exactSuccesses := 0
	for key := range responses {
		keys = append(keys, key)
		if key == "2XX" {
			hasRange = true
		}
		if len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9' {
			exactSuccesses++
		}
	}
	sort.Strings(keys)
	var refs []*openapi3.SchemaRef
	for _, key := range keys {
		isExact := len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9'
		if !isExact && key != "2XX" && !(key == "default" && !hasRange && exactSuccesses < 100) {
			continue
		}
		responseRef := responses[key]
		if responseRef == nil || responseRef.Value == nil {
			continue
		}
		for mediaKey, media := range responseRef.Value.Content {
			var parsed parsedMediaType
			var err error
			if hasMediaFidelity(bindingSpec) {
				parsed, err = parseMediaDeclaration(mediaKey)
			} else {
				parsed, err = parseMediaType(mediaKey)
			}
			if err == nil && bindingSpec == projectionOpenAPI32 {
				kind, sequentialErr := ClassifyOpenAPI32SequentialResponse(mediaKey, media)
				if sequentialErr == nil && kind != "" {
					if media == nil || media.ItemSchema == nil {
						return nil
					}
					refs = append(refs, media.ItemSchema)
					continue
				}
			}
			admitsJSON := err == nil && (isJSONMediaType(parsed.base) || (hasResponseFidelity(bindingSpec) && parsed.rangeSpecificity < 2 && (parsed.base == "application/*" || parsed.base == "*/*")))
			scalar := err == nil && bindingSpec == projectionOpenAPI32 && mediaSchema(media) != nil && openAPI32NonJSONTextKind(mediaSchema(media)) != ""
			if !admitsJSON && !scalar {
				continue
			}
			if media == nil || media.Schema == nil {
				return nil // buildOutputSchema publishes no contract in this case
			}
			refs = append(refs, media.Schema)
		}
	}
	return refs
}

// defNameForRef derives the $defs key for a cyclic ref: the component name
// for `#/components/schemas/X`, else the sanitized trailing pointer segment.
func defNameForRef(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return unescapeJSONPointerSegment(ref[i+1:])
	}
	return ref
}

// escapeJSONPointerSegment escapes a string for use as an RFC 6901 segment.

// resolveRegistryRef resolves both a named component ref and a JSON Pointer
// into a position below that component. OpenAPI permits refs such as
// `#/components/schemas/Envelope/properties/id`; registering only component
// roots leaves those valid source-artifact pointers dangling after the schema
// is projected into an operation-local OBI contract.
func resolveRegistryRef(ref string, registry map[string]any) (any, bool) {
	if value, found := registry[ref]; found {
		return value, true
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return nil, false
	}
	segments := strings.Split(strings.TrimPrefix(ref, prefix), "/")
	if len(segments) < 2 {
		return nil, false
	}
	rootRef := prefix + segments[0]
	current, found := registry[rootRef]
	if !found {
		return nil, false
	}
	for _, encoded := range segments[1:] {
		segment := unescapeJSONPointerSegment(encoded)
		switch node := current.(type) {
		case map[string]any:
			current, found = node[segment]
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current, found = node[index], true
		default:
			return nil, false
		}
		if !found {
			return nil, false
		}
	}
	return current, true
}

// inlineRefs walks `node` recursively and replaces every `{"$ref":
// "#/components/schemas/X"}` object with the resolved schema from
// `registry`. Resolution is iterative on the resolved value too, so
// chains of refs (X → Y → Z) flatten in a single pass.
//
// `seen` tracks refs currently being expanded in the call stack to
// avoid infinite recursion on cyclic schemas. When a cycle is hit
// the ref is left in place (the node keeps `{"$ref": "..."}`); the
// codegen falls back to `unknown` for that field, which is the same
// behavior the user would have seen before this fix.
type decycleContext struct {
	cyclic  map[string]bool
	refBase string
	defs    map[string]any
}

func inlineRefs(node any, registry map[string]any, seen map[string]bool, ctx *decycleContext) any {
	return inlineRefsAt(node, schemaObjectPosition, registry, seen, ctx)
}

func inlineRefsAt(node any, position schemaTraversalPosition, registry map[string]any, seen map[string]bool, ctx *decycleContext) any {
	if position == schemaDataPosition {
		return node
	}
	switch v := node.(type) {
	case map[string]any:
		// Check if this object IS a ref.
		if ref, ok := v["$ref"].(string); position == schemaObjectPosition && ok {
			var expanded any
			if ctx != nil && ctx.cyclic[ref] {
				// A cycle participant: every occurrence becomes a
				// same-document reference to a hoisted $defs entry (the
				// dialect's own recursion mechanism — OBI-D-16-resolvable
				// from the OBI root). Mirrors the TS SDK's decycleSchema.
				// Hoisting is keyed by the ref's own identity, and the `$defs`
				// key is assigned afterwards over the complete set of cut
				// points (finalizeHoistedNames). Naming during the walk would
				// make the key a function of traversal order.
				if _, materialized := ctx.defs[ref]; !materialized {
					ctx.defs[ref] = nil // reserve before expansion: terminates self-reference
					if resolved, found := resolveRegistryRef(ref, registry); found {
						ctx.defs[ref] = inlineRefsAt(resolved, schemaObjectPosition, registry, seen, ctx)
					}
				}
				expanded = map[string]any{"$ref": provisionalDefPointer(ctx.refBase, ref)}
			} else if seen[ref] {
				// Cycle outside the registry graph: leave the ref in place.
				expanded = map[string]any{"$ref": ref}
			} else if resolved, found := resolveRegistryRef(ref, registry); found {
				// Mark this ref as being expanded, recurse to inline
				// any nested refs in the resolved value, then unmark.
				seen[ref] = true
				expanded = inlineRefsAt(resolved, schemaObjectPosition, registry, seen, ctx)
				delete(seen, ref)
			} else {
				expanded = map[string]any{"$ref": ref}
			}

			// JSON Schema 2020-12 permits `$ref` siblings. Preserve their
			// intersection semantics when moving the schema into OBI: merge
			// non-conflicting keywords directly, and use allOf when the resolved
			// target declares the same keyword. This also retains descriptive
			// siblings found in common OpenAPI 3.0 documents without leaking the
			// source-artifact reference.
			siblings := make(map[string]any, len(v)-1)
			for key, value := range v {
				if key != "$ref" {
					siblings[key] = inlineRefsAt(value, schemaChildPosition(key), registry, seen, ctx)
				}
			}
			if len(siblings) == 0 {
				return expanded
			}
			if base, ok := expanded.(map[string]any); ok {
				merged := make(map[string]any, len(base)+len(siblings))
				conflict := false
				for key, value := range base {
					merged[key] = value
				}
				for key, value := range siblings {
					if _, present := merged[key]; present {
						conflict = true
						break
					}
					merged[key] = value
				}
				if !conflict {
					return merged
				}
			}
			return map[string]any{"allOf": []any{expanded, siblings}}
		}
		// Recurse only through JSON Schema-bearing keywords. Annotation and
		// extension payloads are data even when they contain schema-like keys.
		out := make(map[string]any, len(v))
		for k, val := range v {
			childPosition := schemaDataPosition
			switch position {
			case schemaObjectPosition:
				childPosition = schemaChildPosition(k)
			case schemaMapPosition:
				childPosition = schemaObjectPosition
			}
			out[k] = inlineRefsAt(val, childPosition, registry, seen, ctx)
		}
		return out
	case []any:
		out := make([]any, len(v))
		childPosition := schemaDataPosition
		if position == schemaArrayPosition {
			childPosition = schemaObjectPosition
		}
		for i, item := range v {
			out[i] = inlineRefsAt(item, childPosition, registry, seen, ctx)
		}
		return out
	default:
		return v
	}
}

// inlineRefsInOperationSchema applies inlineRefs to a single operation
// input or output schema (a map[string]any built by schemaRefToMap or
// buildInputSchema/buildOutputSchema). Returns the input map mutated
// in place (and also returned, for chaining).
// The second return value names the definitions this pass minted, as
// distinct from any `$defs` the artifact's own Schema Object declared. Only
// minted definitions are subject to the reachability closure applied after
// direction projection (pruneUnreachableDefs): an authorial definition is
// declared artifact content and stands whether or not anything references it.
func inlineRefsInOperationSchema(schema map[string]any, registry map[string]any, cyclic map[string]bool, refBase string, namer *cutPointNamer) (map[string]any, map[string]bool) {
	if schema == nil {
		return nil, nil
	}
	ctx := &decycleContext{cyclic: cyclic, refBase: refBase, defs: map[string]any{}}
	result := inlineRefs(schema, registry, map[string]bool{}, ctx)
	m, ok := result.(map[string]any)
	if !ok {
		return schema, nil
	}
	return finalizeHoistedNames(m, ctx, namer)
}

// provisionalDefPointer is the pointer emitted while walking, before the set of
// cut points is complete. It embeds the ref's own identity, so it is unique and
// cannot be forged by artifact content.
func provisionalDefPointer(refBase, ref string) string {
	return refBase + "/$defs/" + escapeJSONPointerSegment(ref)
}

// finalizeHoistedNames assigns the `$defs` keys once the complete set of cut
// points minted for this operation schema is known, then rewrites the
// provisional pointers the walk emitted. The rewrite is position-aware, so a
// `$ref` spelling that is ordinary application data (a default, an example, an
// extension payload) is never touched.
func finalizeHoistedNames(m map[string]any, ctx *decycleContext, namer *cutPointNamer) (map[string]any, map[string]bool) {
	if len(ctx.defs) == 0 {
		return m, nil
	}
	refs := make([]string, 0, len(ctx.defs))
	for ref := range ctx.defs {
		refs = append(refs, ref)
	}
	names := namer.assign(refs)
	rewrite := make(map[string]string, len(refs))
	defs := make(map[string]any, len(refs))
	hoisted := make(map[string]bool, len(refs))
	for _, ref := range refs {
		name := names[ref]
		rewrite[provisionalDefPointer(ctx.refBase, ref)] = ctx.refBase + "/$defs/" + escapeJSONPointerSegment(name)
		defs[name] = ctx.defs[ref]
		hoisted[name] = true
	}
	rewriteHoistedPointers(m, schemaObjectPosition, rewrite)
	for _, value := range defs {
		rewriteHoistedPointers(value, schemaObjectPosition, rewrite)
	}
	m["$defs"] = defs
	return m, hoisted
}

func rewriteHoistedPointers(node any, position schemaTraversalPosition, rewrite map[string]string) {
	if position == schemaDataPosition {
		return
	}
	switch v := node.(type) {
	case map[string]any:
		if position == schemaObjectPosition {
			if ref, ok := v["$ref"].(string); ok {
				if replacement, found := rewrite[ref]; found {
					v["$ref"] = replacement
				}
			}
		}
		for key, value := range v {
			childPosition := schemaDataPosition
			switch position {
			case schemaObjectPosition:
				childPosition = schemaChildPosition(key)
			case schemaMapPosition:
				childPosition = schemaObjectPosition
			}
			rewriteHoistedPointers(value, childPosition, rewrite)
		}
	case []any:
		childPosition := schemaDataPosition
		if position == schemaArrayPosition {
			childPosition = schemaObjectPosition
		}
		for _, item := range v {
			rewriteHoistedPointers(item, childPosition, rewrite)
		}
	}
}

// majorMinor reduces an artifact version string to its major.minor form
// ("3.1.0" → "3.1") for dialect decisions.
