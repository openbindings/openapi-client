package openapiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// ParameterAnalysis is one effective OpenAPI parameter identity at the
// native call boundary. InputKey is the stable flat key a protocol adapter or
// generated facade can use without reproducing parameter-override rules.
type ParameterAnalysis struct {
	Name       string
	In         string
	InputKey   string
	Required   bool
	Style      string
	Explode    *bool
	AllowEmpty bool
	SourceRef  string
	Schema     json.RawMessage
}

// RequestBodyAnalysis is one admitted request representation. MediaType is a
// normalized concrete declaration or the authored range awaiting selection.
// Base64 is true when a protocol-neutral adapter must carry the native bytes
// as canonical Base64 text.
type RequestBodyAnalysis struct {
	MediaType        string
	Family           string
	Required         bool
	MediaRange       bool
	WholeValue       bool
	Base64           bool
	Base64Properties []string
	Properties       []string
	PropertyMedia    []string
	Schema           json.RawMessage
}

// SupportDisposition is one smallest-owner declaration outcome. It is
// protocol-native generator evidence, not an OpenBindings coverage type.
type SupportDisposition struct {
	SourceRef    string
	Scope        string
	Status       string
	Code         string
	Rule         string
	Reason       string
	Requirements []string
}

// ServerAlternativeAnalysis is one effective authored server alternative.
// URL remains the authored template; selection and substitution are per-call.
type ServerAlternativeAnalysis struct {
	Index     int
	URL       string
	Variables []string
	Usable    bool
	Reason    string
}

// SecuritySchemeAnalysis is one scheme in an ANDed Security Requirement.
type SecuritySchemeAnalysis struct {
	Name   string
	Type   string
	Scheme string
	In     string
	Scopes []string
}

// SecurityAlternativeAnalysis is one authored OR alternative.
type SecurityAlternativeAnalysis struct {
	Index        int
	Anonymous    bool
	Usable       bool
	Reason       string
	Schemes      []SecuritySchemeAnalysis
	Requirements []Requirement
}

// ResponseAlternativeAnalysis records one exact, range, or default lookup
// entry without selecting a response for a particular invocation.
type ResponseAlternativeAnalysis struct {
	Key        string
	SourceRef  string
	CanSucceed bool
	Usable     bool
	MediaTypes []string
	Schema     json.RawMessage
	Reason     string
}

// OperationAnalysis contains detached facts for one selected operation.
type OperationAnalysis struct {
	Info          OperationInfo
	Description   string
	Deprecated    bool
	Parameters    []ParameterAnalysis
	RequestBodies []RequestBodyAnalysis
	Responses     []ResponseAlternativeAnalysis
	Servers       []ServerAlternativeAnalysis
	Security      []SecurityAlternativeAnalysis
	Requirements  []string
	Coverage      []SupportDisposition
}

// Analysis is a detached, immutable-by-ownership view of the exact artifact
// loaded by a Client. Every method returns fresh slices and maps.
type Analysis struct {
	Edition    Edition
	Location   string
	Operations []OperationAnalysis
	Coverage   []SupportDisposition
}

// Analysis returns stable declaration facts from the client's already-loaded
// artifact. It performs no retrieval and reparses no source.
func (c *Client) Analysis() Analysis {
	if c == nil {
		return Analysis{}
	}
	c.analysisOnce.Do(func() {
		result := Analysis{Edition: c.Edition(), Location: c.Location()}
		for _, info := range c.Operations() {
			operation, err := c.AnalyzeOperation(OperationRef(info.Ref))
			if err != nil {
				// Per-target invalidity remains addressable for invocation, but it
				// cannot make analysis of surviving sibling operations fail.
				result.Operations = append(result.Operations, OperationAnalysis{Info: cloneOperationInfo(info)})
				continue
			}
			result.Operations = append(result.Operations, operation)
		}
		c.analysis = result
	})
	return cloneAnalysis(c.analysis)
}

// AnalyzeOperation resolves selector and returns detached facts from the same
// artifact snapshot used by invocation.
func (c *Client) AnalyzeOperation(selector OperationSelector) (OperationAnalysis, error) {
	if c == nil {
		return OperationAnalysis{}, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	if c.swagger20 != nil {
		info, err := resolveOperationInfo(c.Operations(), selector, c.edition)
		if err != nil {
			return OperationAnalysis{}, err
		}
		operation, _, err := resolveSwagger20Operation(c.swagger20.document, info.Ref)
		if err != nil {
			return OperationAnalysis{}, clientError(err)
		}
		set, err := effectiveSwagger20Parameters(c.swagger20.document.graph, operation)
		if err != nil {
			return OperationAnalysis{}, clientError(swagger20RefusalError(err, c.Location()))
		}
		parameters := make([]ParameterAnalysis, 0, len(set.nonBody)+1)
		for _, parameter := range set.nonBody {
			parameters = append(parameters, ParameterAnalysis{
				Name: parameter.name, In: string(parameter.in), Required: parameter.required,
			})
		}
		if set.body != nil {
			parameters = append(parameters, ParameterAnalysis{Name: set.body.name, In: string(set.body.in), Required: set.body.required})
		}
		qualifyParameterInputKeys(parameters)
		requestBodies := []RequestBodyAnalysis{}
		if set.body != nil {
			base64 := set.body.typeName == "string"
			if format := set.body.raw.string("format"); !format.valid || format.value != "binary" {
				base64 = false
			}
			requestBodies = append(requestBodies, RequestBodyAnalysis{Base64: base64})
		}
		analysis := OperationAnalysis{Info: info, Parameters: parameters, RequestBodies: requestBodies}
		if model, modelErr := c.swagger20.SynthesisModel(); modelErr == nil {
			for _, candidate := range model.Operations {
				if candidate.Ref == info.Ref {
					analysis = enrichSwagger20Analysis(analysis, candidate)
					break
				}
			}
		}
		return cloneOperationAnalysis(analysis), nil
	}

	resolved, err := resolveOperation(c.artifact, c.floor, selector)
	if err != nil {
		return OperationAnalysis{}, err
	}
	if resolved.unusable != nil {
		return OperationAnalysis{}, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: resolved.unusable.Error(), Cause: resolved.unusable}
	}
	if duplicate := duplicateDeclaredParameterIdentity(resolved.pathItem, resolved.operation); duplicate != "" {
		return OperationAnalysis{}, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: fmt.Sprintf("parameter identity %q is declared more than once in one parameter list", duplicate)}
	}
	parameters := make([]ParameterAnalysis, 0)
	for _, reference := range effectiveParameters(resolved.pathItem, resolved.operation) {
		if reference == nil || reference.Value == nil || reference.Value.Name == "" {
			continue
		}
		parameters = append(parameters, ParameterAnalysis{
			Name: reference.Value.Name, In: reference.Value.In, Required: reference.Value.Required,
		})
	}
	qualifyParameterInputKeys(parameters)
	reference, referenceErr := ParseOperationReference(resolved.info.Ref, c.artifact.Edition)
	if referenceErr != nil {
		return OperationAnalysis{}, &ClientError{Kind: ErrorOperation, Code: "INVALID_OPERATION_REF", Message: referenceErr.Error(), Cause: referenceErr}
	}
	plans, planErr := planRequestBodiesForArtifact(c.artifact, &OperationTarget{
		OperationReference: reference,
		Document:           resolved.document,
		PathItem:           resolved.pathItem,
		Operation:          resolved.operation,
	}, profileFullCoordinate)
	if planErr != nil {
		if resolved.operation.RequestBody == nil || resolved.operation.RequestBody.Value == nil || resolved.operation.RequestBody.Value.Required {
			return OperationAnalysis{}, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: planErr.Error(), Cause: planErr}
		}
		plans = nil
	}
	requestBodies := make([]RequestBodyAnalysis, 0, len(plans))
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		mediaType := plan.mediaType
		if plan.mediaRange {
			mediaType = plan.mediaKey
		}
		rawProperties := make([]string, 0, len(plan.rawProperties))
		for name := range plan.rawProperties {
			rawProperties = append(rawProperties, name)
		}
		sort.Strings(rawProperties)
		requestBodies = append(requestBodies, RequestBodyAnalysis{MediaType: mediaType, Base64: plan.rawBoundary, Base64Properties: rawProperties})
	}
	sort.Slice(requestBodies, func(i, j int) bool { return requestBodies[i].MediaType < requestBodies[j].MediaType })
	analysis := OperationAnalysis{Info: resolved.info, Parameters: parameters, RequestBodies: requestBodies}
	analysis = enrichOpenAPIAnalysis(c, resolved, plans, analysis)
	return cloneOperationAnalysis(analysis), nil
}

func enrichSwagger20Analysis(result OperationAnalysis, operation Swagger20SynthesisOperation) OperationAnalysis {
	result.Description = operation.Description
	result.Deprecated = operation.Deprecated
	result.Requirements = append(result.Requirements, operation.Requirements...)
	for index := range result.Parameters {
		if index >= len(operation.Parameters) {
			break
		}
		parameter := operation.Parameters[index]
		result.Parameters[index].AllowEmpty = parameter.AllowEmptyValue
		result.Parameters[index].Schema = append(json.RawMessage(nil), parameter.Schema...)
		result.Parameters[index].SourceRef = operation.Ref + "/parameters/" + fmt.Sprint(index)
	}
	if operation.Body != nil && len(result.RequestBodies) > 0 {
		result.RequestBodies[0].Required = operation.Body.Required
		result.RequestBodies[0].Schema = append(json.RawMessage(nil), operation.Body.Schema...)
	}
	for _, response := range operation.Responses {
		result.Responses = append(result.Responses, ResponseAlternativeAnalysis{
			Key: response.Key, SourceRef: response.SourceRef, CanSucceed: response.CanSucceed,
			Usable: response.Usable, Schema: append(json.RawMessage(nil), response.Schema...), Reason: response.Reason,
		})
	}
	for _, alternative := range operation.Alternatives {
		result.Coverage = append(result.Coverage, SupportDisposition{
			SourceRef: alternative.SourceRef, Scope: "alternative", Status: supportStatus(alternative.Usable, alternative.Disposition),
			Rule: alternative.Rule, Reason: alternative.Reason, Requirements: append([]string(nil), alternative.Requirements...),
		})
	}
	for _, alternative := range operation.Security {
		security := SecurityAlternativeAnalysis{Index: alternative.Index, Anonymous: alternative.Anonymous, Usable: alternative.Usable, Reason: alternative.Reason}
		for _, scheme := range alternative.Schemes {
			security.Schemes = append(security.Schemes, SecuritySchemeAnalysis{Name: scheme.Name, Type: scheme.Type, Scopes: append([]string(nil), scheme.Scopes...)})
		}
		result.Security = append(result.Security, security)
	}
	if operation.Excluded {
		result.Coverage = append(result.Coverage, SupportDisposition{SourceRef: operation.Ref, Scope: "target", Status: operation.Disposition, Rule: operation.Rule, Reason: operation.Reason})
	}
	return result
}

func supportStatus(usable bool, disposition string) string {
	if usable {
		return "represented"
	}
	if disposition != "" {
		return disposition
	}
	return "excluded"
}

func enrichOpenAPIAnalysis(client *Client, resolved resolvedOperation, plans []*bodyPlan, result OperationAnalysis) OperationAnalysis {
	operation := resolved.operation
	if operation == nil {
		return result
	}
	result.Description = operation.Description
	result.Deprecated = operation.Deprecated
	parameters := effectiveParameters(resolved.pathItem, operation)
	for index := range result.Parameters {
		if index >= len(parameters) || parameters[index] == nil || parameters[index].Value == nil {
			continue
		}
		parameter := parameters[index].Value
		result.Parameters[index].Style = parameter.Style
		if parameter.Explode != nil {
			value := *parameter.Explode
			result.Parameters[index].Explode = &value
		}
		result.Parameters[index].AllowEmpty = parameter.AllowEmptyValue
		result.Parameters[index].SourceRef = resolved.info.Ref + "/parameters/" + fmt.Sprint(index)
		result.Parameters[index].Schema = schemaImage(client.artifact, parameter.Schema)
	}
	for index := range result.RequestBodies {
		if index >= len(plans) || plans[index] == nil {
			continue
		}
		plan := plans[index]
		body := &result.RequestBodies[index]
		body.Family = plan.family
		body.Required = plan.required
		body.MediaRange = plan.mediaRange
		body.WholeValue = plan.synthetic || plan.wholeObject
		body.Schema = schemaImage(client.artifact, plan.media.Schema)
		body.Properties = sortedBoolKeys(plan.props)
		body.PropertyMedia = append([]string(nil), plan.propertyMedia...)
	}
	if set, err := EffectiveServerSet(resolved.document, resolved.pathItem, operation, client.Location()); err == nil {
		for index, server := range set.Servers() {
			if server == nil {
				continue
			}
			variables := make([]string, 0, len(server.Variables))
			for name := range server.Variables {
				variables = append(variables, name)
			}
			sort.Strings(variables)
			result.Servers = append(result.Servers, ServerAlternativeAnalysis{Index: index, URL: server.URL, Variables: variables, Usable: true})
		}
		if _, resolveErr := set.Resolve(nil); resolveErr != nil {
			var required *ServerResolutionRequiredError
			if errors.As(resolveErr, &required) {
				result.Requirements = appendUnique(result.Requirements, "configuration.server")
			}
		}
	} else {
		result.Coverage = append(result.Coverage, SupportDisposition{SourceRef: resolved.info.Ref + "/servers", Scope: "alternative", Status: "excluded", Code: "openapi.server_url_excluded", Reason: err.Error()})
	}
	result.Security = analyzeSecurityAlternatives(resolved.document, operation, parameters)
	if len(result.Security) > 1 {
		result.Requirements = appendUnique(result.Requirements, "configuration.security")
	}
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		if plan.mediaRange {
			result.Requirements = appendUnique(result.Requirements, "configuration.requestMedia")
		}
		if len(plan.propertyMedia) > 0 {
			result.Requirements = appendUnique(result.Requirements, "configuration.propertyMedia")
		}
	}
	result.Responses = analyzeResponses(client.artifact, operation, resolved.info.Ref)
	if verdict := client.floor.opVerdict(resolved.info.Ref); verdict != nil {
		result.Coverage = append(result.Coverage, floorAnalysis(verdict)...)
	}
	return result
}

func analyzeSecurityAlternatives(document *openapi3.T, operation *openapi3.Operation, parameters openapi3.Parameters) []SecurityAlternativeAnalysis {
	requirements := effectiveSecurityRequirements(document, operation)
	if requirements == nil {
		return nil
	}
	result := make([]SecurityAlternativeAnalysis, 0, len(*requirements))
	for index, requirement := range *requirements {
		alternative := SecurityAlternativeAnalysis{Index: index, Anonymous: len(requirement) == 0, Usable: false}
		plans := viableSecurityPlans(document, operation, "", parameters)
		for _, plan := range plans {
			if plan.alternativeIndex != index {
				continue
			}
			alternative.Usable = true
			alternative.Requirements = cloneRequirements(plan.context.Requirements)
			for _, named := range plan.schemes {
				requiredScopes := append([]string(nil), requirement[named.name]...)
				alternative.Schemes = append(alternative.Schemes, SecuritySchemeAnalysis{
					Name: named.name, Type: named.scheme.Type, Scheme: named.scheme.Scheme, In: named.scheme.In, Scopes: requiredScopes,
				})
			}
			break
		}
		if !alternative.Usable {
			alternative.Reason = "security requirement has no usable complete alternative"
		}
		result = append(result, alternative)
	}
	return result
}

func analyzeResponses(artifact *Artifact, operation *openapi3.Operation, operationRef string) []ResponseAlternativeAnalysis {
	if operation == nil || operation.Responses == nil {
		return nil
	}
	keys := make([]string, 0, operation.Responses.Len())
	for key := range operation.Responses.Map() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]ResponseAlternativeAnalysis, 0, len(keys))
	for _, key := range keys {
		responseRef := operation.Responses.Value(key)
		alternative := ResponseAlternativeAnalysis{Key: key, SourceRef: operationRef + "/responses/" + escapeJSONPointerSegment(key), CanSucceed: responseKeyCanSucceed(key, operation.Responses), Usable: responseRef != nil && responseRef.Value != nil}
		if !alternative.Usable {
			alternative.Reason = "response declaration is not a usable Response Object"
			result = append(result, alternative)
			continue
		}
		mediaKeys := make([]string, 0, len(responseRef.Value.Content))
		for mediaType := range responseRef.Value.Content {
			mediaKeys = append(mediaKeys, mediaType)
		}
		sort.Strings(mediaKeys)
		alternative.MediaTypes = mediaKeys
		for _, mediaType := range mediaKeys {
			if media := responseRef.Value.Content[mediaType]; media != nil && media.Schema != nil {
				alternative.Schema = schemaImage(artifact, media.Schema)
				break
			}
		}
		result = append(result, alternative)
	}
	return result
}

func responseKeyCanSucceed(key string, responses *openapi3.Responses) bool {
	if key == "default" {
		for status := 200; status <= 299; status++ {
			if responses.Value(fmt.Sprint(status)) == nil && responses.Value("2XX") == nil {
				return true
			}
		}
		return false
	}
	if key == "2XX" {
		return true
	}
	return len(key) == 3 && key[0] == '2'
}

func floorAnalysis(verdict *floorOp) []SupportDisposition {
	if verdict == nil {
		return nil
	}
	result := []SupportDisposition{}
	if verdict.Disposition == "invalid" {
		result = append(result, SupportDisposition{SourceRef: verdict.Ref, Scope: "target", Status: "invalid", Code: "openapi.target_invalid", Reason: floorInvalidTargetMessage(len(verdict.Defects))})
	}
	for _, ref := range verdict.AltOrder {
		result = append(result, SupportDisposition{SourceRef: ref, Scope: "alternative", Status: "invalid", Code: "openapi.request_media_invalid"})
	}
	for _, ref := range verdict.ProjOrder {
		for _, defect := range verdict.Projections[ref] {
			result = append(result, SupportDisposition{SourceRef: defect.Position, Scope: "projection", Status: "invalid", Code: "openapi.projection_invalid", Reason: defect.Authority})
		}
	}
	return result
}

func schemaImage(artifact *Artifact, reference *openapi3.SchemaRef) json.RawMessage {
	if reference == nil || reference.Value == nil {
		return nil
	}
	data, err := reference.Value.MarshalJSON()
	if err != nil {
		return nil
	}
	if artifact != nil && artifact.schemaOverlays != nil {
		var image map[string]any
		if json.Unmarshal(data, &image) == nil {
			delete(image, "__origin__")
			artifact.schemaOverlays.apply(reference, image)
			data, err = json.Marshal(image)
			if err != nil {
				return nil
			}
		}
	}
	return append(json.RawMessage(nil), data...)
}

func sortedBoolKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func cloneRequirements(values []Requirement) []Requirement {
	result := make([]Requirement, len(values))
	for index, value := range values {
		result[index] = value
		if value.Durable != nil {
			durable := *value.Durable
			result[index].Durable = &durable
		}
		result[index].Extra = cloneStringAnyMap(value.Extra)
	}
	return result
}

func cloneStringAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

// Preflight performs every artifact- and configuration-derived check known
// before dispatch. A non-nil result describes complete alternative remedies;
// the method performs no HTTP interaction.
func (c *Client) Preflight(ctx context.Context, selector OperationSelector, input Input, options ...CallOptions) (*Prerequisites, error) {
	if c == nil {
		return nil, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	call, err := oneRuntimeCallOptions(options)
	if err != nil {
		return nil, err
	}
	if c.swagger20 != nil {
		return c.preflightSwagger20(ctx, selector, input, call)
	}
	if refusal := c.artifact.Refusal(); refusal != nil {
		return nil, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: refusal.Error(), Cause: refusal}
	}
	resolved, err := resolveOperation(c.artifact, c.floor, selector)
	if err != nil {
		return nil, err
	}
	if resolved.unusable != nil {
		return nil, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: resolved.unusable.Error(), Cause: resolved.unusable}
	}
	if c.artifact.Edition.IsOpenAPI32() && resolved.additional && resolved.info.WireMethod == "CONNECT" {
		return nil, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: "CONNECT establishes a tunnel outside the OpenAPI invocation model"}
	}
	if methodIgnoresRequestBody(c.artifact.Edition, resolved.additional, string(resolved.info.Method)) {
		if input.BodyPresent || input.Body != nil {
			return nil, &ClientError{Kind: ErrorInput, Code: "BODY_FORBIDDEN_FOR_METHOD", Message: fmt.Sprintf("method %q cannot carry the supplied request body", resolved.info.WireMethod)}
		}
		operation := *resolved.operation
		operation.RequestBody = nil
		resolved.operation = &operation
	}
	native, err := nativeInput(c.artifact, resolved.info.Ref, resolved.document, resolved.pathItem, resolved.operation, input)
	if err != nil {
		return requirementsFromClientError(err)
	}
	prepare, err := c.prepareOptions(resolved, native, call)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareArtifactWithFloor(c.artifact, nil, prepare)
	if err != nil {
		return requirementsFromClientError(clientError(err))
	}
	return clonePrerequisites(prepared.Prerequisites()), nil
}

func requirementsFromClientError(err error) (*Prerequisites, error) {
	var client *ClientError
	if errorsAsClient(err, &client) && client.Code == CodeContextRequired {
		if requirements, ok := client.Details.(*Prerequisites); ok {
			return clonePrerequisites(requirements), nil
		}
	}
	return nil, err
}

func errorsAsClient(err error, target **ClientError) bool {
	for current := err; current != nil; {
		if client, ok := current.(*ClientError); ok {
			*target = client
			return true
		}
		next, ok := current.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		current = next.Unwrap()
	}
	return false
}

func oneRuntimeCallOptions(options []CallOptions) (CallOptions, error) {
	if len(options) > 1 {
		return CallOptions{}, &ClientError{Kind: ErrorConfiguration, Code: "TOO_MANY_CALL_OPTIONS", Message: "Preflight accepts at most one CallOptions value"}
	}
	if len(options) == 1 {
		return options[0], nil
	}
	return CallOptions{}, nil
}

func (c *Client) preflightSwagger20(ctx context.Context, selector OperationSelector, input Input, call CallOptions) (*Prerequisites, error) {
	info, err := resolveOperationInfo(c.Operations(), selector, c.edition)
	if err != nil {
		return nil, err
	}
	prepared, err := NewEngine(nil).PrepareSwagger20(ctx, Swagger20PrepareOptions{
		Source: Swagger20Source{Location: c.Location(), Document: c.swagger20.document},
		Ref:    info.Ref,
	})
	if err != nil {
		return nil, clientError(err)
	}
	operation, err := prepared.SynthesisOperation()
	if err != nil {
		return nil, clientError(err)
	}
	if operation.Excluded {
		return nil, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: operation.Reason}
	}

	server := call.Server
	if server == nil {
		server = c.options.Server
	}
	securityAlternative := call.SecurityAlternative
	if securityAlternative == nil {
		securityAlternative = c.options.SecurityAlternative
	}
	emptyValueForm := call.EmptyValueForm
	if emptyValueForm == "" {
		emptyValueForm = c.options.EmptyValueForm
	}
	converter := call.ParameterConverter
	if converter == nil {
		converter = c.options.ParameterConverter
	}
	auth := map[string]any{}
	for name, value := range c.options.Auth {
		auth[name] = value
	}
	for name, value := range call.Auth {
		auth[name] = value
	}

	requirements := []Requirement{}
	for _, declared := range operation.Requirements {
		point := strings.TrimPrefix(declared, "configuration.")
		// These are host capabilities or invocation-conditional spellings, not
		// portable context values. Their absence is a native refusal when the
		// selected call actually reaches the corresponding lane.
		switch point {
		case "parameterConversion", "emptyValueForm", "requestContentCodings", "responseContentCodings", "requestCharacterEncodings", "responseCharacterEncodings":
			continue
		}
		if swagger20RequirementSatisfied(point, input, server, securityAlternative, emptyValueForm, converter, operation) {
			continue
		}
		durable := true
		description := "supply the Swagger 2.0 " + point + " option"
		path := ""
		var schema map[string]any
		switch point {
		case "requestMedia":
			description = "select one concrete request media type"
			schema = map[string]any{"type": "string"}
		case "propertyMedia":
			description = "select one concrete media type for each required file form parameter"
			if name := requiredSwagger20FileParameter(operation); name != "" {
				path = "/" + escapeJSONPointerSegment(name)
			}
			schema = map[string]any{"type": "string"}
		case "security":
			description = "select one complete declared security alternative"
			allowed := []any{}
			for _, alternative := range operation.Security {
				if alternative.Usable {
					allowed = append(allowed, alternative.Index)
				}
			}
			schema = map[string]any{"enum": allowed}
		}
		requirements = append(requirements, newConfigValueRequirementCompat(point, path, description, schema, &durable))
	}

	if !hasSwagger20Requirement(operation.Requirements, "configuration.security") || securityAlternative != nil {
		selected, selectErr := selectedSwagger20SecurityAlternative(operation, securityAlternative)
		if selectErr != nil {
			return nil, selectErr
		}
		if selected != nil && !selected.Anonymous {
			for _, scheme := range selected.Schemes {
				if _, supplied := auth[scheme.Name]; supplied {
					continue
				}
				credential := ""
				switch scheme.Type {
				case "basic":
					credential = "basic"
				case "apiKey":
					credential = "apiKey"
				case "oauth2":
					credential = "oauth2"
				default:
					return nil, &ClientError{Kind: ErrorConfiguration, Code: "UNSUPPORTED_SECURITY_SCHEME", Message: fmt.Sprintf("unsupported Swagger 2.0 security scheme %q", scheme.Name)}
				}
				requirement := Requirement{Type: "auth." + credential, Name: scheme.Name}
				if len(scheme.Scopes) > 0 {
					requirement.Extra = map[string]any{"scopes": append([]string(nil), scheme.Scopes...)}
				}
				requirements = append(requirements, requirement)
			}
		}
	}
	if len(requirements) == 0 {
		return nil, nil
	}
	return &Prerequisites{Target: c.Location(), Alternatives: []RequirementAlternative{{Requirements: requirements}}}, nil
}

func swagger20RequirementSatisfied(point string, input Input, server *ServerSelection, securityAlternative *int, emptyValueForm Swagger20EmptyValueForm, converter ParameterConverter, operation Swagger20SynthesisOperation) bool {
	switch point {
	case "server":
		return server != nil
	case "security":
		return securityAlternative != nil
	case "requestMedia":
		return input.MediaType != ""
	case "propertyMedia":
		for _, parameter := range operation.Parameters {
			if parameter.In != Swagger20ParameterFormData || !parameter.Required || !swagger20SynthesisParameterIsFileAnalysis(parameter) {
				continue
			}
			if _, present := input.PropertyMediaTypes[parameter.Name]; !present {
				return false
			}
		}
		return true
	case "parameterConversion":
		return converter != nil
	case "emptyValueForm":
		return emptyValueForm != ""
	default:
		return true
	}
}

func swagger20SynthesisParameterIsFileAnalysis(parameter Swagger20SynthesisParameter) bool {
	var schema map[string]any
	return json.Unmarshal(parameter.Schema, &schema) == nil && schema["type"] == "file"
}

func requiredSwagger20FileParameter(operation Swagger20SynthesisOperation) string {
	for _, parameter := range operation.Parameters {
		if parameter.In == Swagger20ParameterFormData && parameter.Required && swagger20SynthesisParameterIsFileAnalysis(parameter) {
			return parameter.Name
		}
	}
	return ""
}

func hasSwagger20Requirement(requirements []string, wanted string) bool {
	for _, requirement := range requirements {
		if requirement == wanted {
			return true
		}
	}
	return false
}

func selectedSwagger20SecurityAlternative(operation Swagger20SynthesisOperation, selected *int) (*Swagger20SynthesisSecurityAlternative, error) {
	if len(operation.Security) == 0 {
		return nil, nil
	}
	if selected != nil {
		if *selected < 0 || *selected >= len(operation.Security) || !operation.Security[*selected].Usable {
			return nil, &ClientError{Kind: ErrorConfiguration, Code: "INVALID_SECURITY_ALTERNATIVE", Message: "selected Swagger 2.0 security alternative is unusable"}
		}
		return &operation.Security[*selected], nil
	}
	for index := range operation.Security {
		if operation.Security[index].Usable {
			return &operation.Security[index], nil
		}
	}
	return nil, nil
}

func qualifyParameterInputKeys(parameters []ParameterAnalysis) {
	locations := map[string]string{}
	qualified := false
	for _, parameter := range parameters {
		if previous, seen := locations[parameter.Name]; seen && previous != parameter.In {
			qualified = true
		} else {
			locations[parameter.Name] = parameter.In
		}
	}
	for index := range parameters {
		parameters[index].InputKey = parameters[index].Name
		if qualified {
			parameters[index].InputKey = parameters[index].In + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(parameters[index].Name)
		}
	}
}

func cloneAnalysis(value Analysis) Analysis {
	result := Analysis{Edition: value.Edition, Location: value.Location, Operations: make([]OperationAnalysis, len(value.Operations)), Coverage: cloneSupportDispositions(value.Coverage)}
	for index := range value.Operations {
		result.Operations[index] = cloneOperationAnalysis(value.Operations[index])
	}
	return result
}

func cloneOperationAnalysis(value OperationAnalysis) OperationAnalysis {
	result := OperationAnalysis{
		Info: cloneOperationInfo(value.Info), Description: value.Description, Deprecated: value.Deprecated,
		Parameters: append([]ParameterAnalysis(nil), value.Parameters...), RequestBodies: append([]RequestBodyAnalysis(nil), value.RequestBodies...),
		Responses: append([]ResponseAlternativeAnalysis(nil), value.Responses...), Servers: append([]ServerAlternativeAnalysis(nil), value.Servers...),
		Security: append([]SecurityAlternativeAnalysis(nil), value.Security...), Requirements: append([]string(nil), value.Requirements...),
		Coverage: cloneSupportDispositions(value.Coverage),
	}
	for index := range result.Parameters {
		if result.Parameters[index].Explode != nil {
			explode := *result.Parameters[index].Explode
			result.Parameters[index].Explode = &explode
		}
		result.Parameters[index].Schema = append(json.RawMessage(nil), result.Parameters[index].Schema...)
	}
	for index := range result.RequestBodies {
		result.RequestBodies[index].Base64Properties = append([]string(nil), result.RequestBodies[index].Base64Properties...)
		result.RequestBodies[index].Properties = append([]string(nil), result.RequestBodies[index].Properties...)
		result.RequestBodies[index].PropertyMedia = append([]string(nil), result.RequestBodies[index].PropertyMedia...)
		result.RequestBodies[index].Schema = append(json.RawMessage(nil), result.RequestBodies[index].Schema...)
	}
	for index := range result.Responses {
		result.Responses[index].MediaTypes = append([]string(nil), result.Responses[index].MediaTypes...)
		result.Responses[index].Schema = append(json.RawMessage(nil), result.Responses[index].Schema...)
	}
	for index := range result.Servers {
		result.Servers[index].Variables = append([]string(nil), result.Servers[index].Variables...)
	}
	for index := range result.Security {
		result.Security[index].Schemes = append([]SecuritySchemeAnalysis(nil), result.Security[index].Schemes...)
		for schemeIndex := range result.Security[index].Schemes {
			result.Security[index].Schemes[schemeIndex].Scopes = append([]string(nil), result.Security[index].Schemes[schemeIndex].Scopes...)
		}
		result.Security[index].Requirements = cloneRequirements(result.Security[index].Requirements)
	}
	return result
}

func cloneSupportDispositions(values []SupportDisposition) []SupportDisposition {
	result := append([]SupportDisposition(nil), values...)
	for index := range result {
		result[index].Requirements = append([]string(nil), result[index].Requirements...)
	}
	return result
}

func cloneOperationInfo(value OperationInfo) OperationInfo {
	value.Tags = append([]string(nil), value.Tags...)
	return value
}
