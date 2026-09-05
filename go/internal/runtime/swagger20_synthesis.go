package openapiclient

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Swagger20SynthesisDocument is detached OpenAPI-native analysis used by thin
// binding adapters. Schemas are self-contained graph images whose authored
// assertions retain the OAS 2.0 dialect; adapters decide how to project and
// account for that dialect.
type Swagger20SynthesisDocument struct {
	Name        string
	Version     string
	Description string
	Operations  []Swagger20SynthesisOperation
}

type Swagger20SynthesisOperation struct {
	Ref          string
	Path         string
	Method       string
	OperationID  string
	Description  string
	Deprecated   bool
	Tags         []string
	Parameters   []Swagger20SynthesisParameter
	Body         *Swagger20SynthesisBody
	Responses    []Swagger20SynthesisResponse
	Alternatives []Swagger20SynthesisAlternative
	Security     []Swagger20SynthesisSecurityAlternative
	Requirements []string
	Excluded     bool
	Reason       string
}

type Swagger20SynthesisParameter struct {
	Name            string
	In              Swagger20ParameterLocation
	Required        bool
	AllowEmptyValue bool
	Schema          json.RawMessage
}

type Swagger20SynthesisBody struct {
	Required bool
	Schema   json.RawMessage
}

type Swagger20SynthesisResponse struct {
	Key           string
	SourceRef     string
	SchemaPresent bool
	Schema        json.RawMessage
	CanSucceed    bool
	Usable        bool
	Reason        string
	Headers       []Swagger20SynthesisResponseHeader
}

type Swagger20SynthesisResponseHeader struct {
	Name      string
	SourceRef string
	Usable    bool
	Reason    string
}

type Swagger20SynthesisAlternative struct {
	SourceRef    string
	Kind         string
	Index        int
	Usable       bool
	Reason       string
	Requirements []string
}

type Swagger20SynthesisSecurityAlternative struct {
	SourceRef string
	Index     int
	Anonymous bool
	Usable    bool
	Reason    string
	Schemes   []Swagger20SynthesisSecurityScheme
}

type Swagger20SynthesisSecurityScheme struct {
	Name   string
	Type   string
	Scopes []string
}

// SynthesisModel analyzes every authored path-operation position. A defect in
// one target is returned on that target rather than erasing its siblings.
func (c *Swagger20Client) SynthesisModel() (*Swagger20SynthesisDocument, error) {
	if c == nil || c.document == nil {
		return nil, fmt.Errorf("Swagger 2.0 client has no loaded document")
	}
	model := &Swagger20SynthesisDocument{}
	if info := c.document.root.object("info"); info.present && info.valid {
		model.Name = info.value.string("title").value
		model.Version = info.value.string("version").value
		model.Description = info.value.string("description").value
	}
	paths := c.document.root.object("paths")
	if !paths.present || !paths.valid {
		return model, nil
	}
	pathNames := make([]string, 0, len(paths.value))
	for path := range paths.value {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	methods := []string{"get", "put", "post", "delete", "options", "head", "patch"}
	for _, path := range pathNames {
		rawItem, itemOK := paths.value[path].(map[string]any)
		if !itemOK {
			continue
		}
		_, referencedItem := rawItem["$ref"]
		for _, method := range methods {
			_, directlyDeclared := rawItem[method]
			if !directlyDeclared && !referencedItem {
				continue
			}
			ref := "#/paths/" + escapeJSONPointerSegment(path) + "/" + method
			analyzed := Swagger20SynthesisOperation{Ref: ref, Path: path, Method: method}
			operation, _, err := resolveSwagger20Operation(c.document, ref)
			if err != nil {
				// A referenced Path Item owns its operation inventory after
				// replacement. An absent method there is not an excluded target,
				// and an unresolvable Path Item makes its operations unaddressable.
				if referencedItem {
					continue
				}
				analyzed.Excluded, analyzed.Reason = true, err.Error()
				model.Operations = append(model.Operations, analyzed)
				continue
			}
			analyzed = c.analyzeSwagger20Operation(operation, ref)
			model.Operations = append(model.Operations, analyzed)
		}
	}
	return model, nil
}

// SynthesisOperation returns the same declaration analysis for an already
// prepared target. Adapters use it for side-effect-free preflight without
// loading the artifact a second time or crossing into the OpenAPI 3.x lane.
func (p *Swagger20PreparedOperation) SynthesisOperation() (Swagger20SynthesisOperation, error) {
	if p == nil || p.document == nil {
		return Swagger20SynthesisOperation{}, fmt.Errorf("Swagger 2.0 prepared operation is nil")
	}
	client := &Swagger20Client{document: p.document}
	result := client.analyzeSwagger20Operation(p.operation, p.info.Ref)
	return result, nil
}

func (c *Swagger20Client) analyzeSwagger20Operation(operation swagger20Operation, ref string) Swagger20SynthesisOperation {
	result := Swagger20SynthesisOperation{Ref: ref, Path: operation.path, Method: operation.method}
	result.OperationID = operation.raw.string("operationId").value
	description := operation.raw.string("description")
	summary := operation.raw.string("summary")
	if description.valid && description.value != "" {
		result.Description = description.value
	} else if summary.valid {
		result.Description = summary.value
	}
	deprecated := operation.raw.boolean("deprecated")
	result.Deprecated = deprecated.valid && deprecated.value
	if tags := operation.raw.array("tags"); tags.present && tags.valid {
		for _, raw := range tags.value {
			if tag, ok := raw.(string); ok {
				result.Tags = append(result.Tags, tag)
			}
		}
	}

	parameters, err := effectiveSwagger20Parameters(c.document.graph, operation)
	if err != nil {
		result.Excluded, result.Reason = true, err.Error()
		return result
	}
	responses, err := swagger20ResponsesFor(c.document.graph, operation)
	if err != nil {
		result.Excluded, result.Reason = true, err.Error()
		return result
	}
	if _, err := resolveSwagger20Server(c.document, operation, "", nil); err != nil {
		if _, configuredErr := resolveSwagger20Server(c.document, operation, "https://configured.invalid", nil); configuredErr != nil {
			result.Excluded, result.Reason = true, err.Error()
			return result
		}
		result.Requirements = appendSwagger20Requirement(result.Requirements, "configuration.server")
	}

	for _, parameter := range parameters.nonBody {
		schema, marshalErr := swagger20ParameterSchemaImage(parameter)
		if marshalErr != nil {
			result.Excluded, result.Reason = true, marshalErr.Error()
			return result
		}
		result.Parameters = append(result.Parameters, Swagger20SynthesisParameter{
			Name: parameter.name, In: parameter.in, Required: parameter.required,
			AllowEmptyValue: parameter.allowEmptyValue, Schema: schema,
		})
		if parameter.allowEmptyValue {
			result.Requirements = appendSwagger20Requirement(result.Requirements, "configuration.emptyValueForm")
		}
		if swagger20ParameterNeedsConversion(parameter) {
			result.Requirements = appendSwagger20Requirement(result.Requirements, "configuration.parameterConversion")
		}
		if parameter.in == Swagger20ParameterHeader && strings.EqualFold(parameter.name, "Content-Encoding") && swagger20CodingDeclarationNeedsCodec(parameter.raw) {
			result.Requirements = appendSwagger20Requirement(result.Requirements, "configuration.requestContentCodings")
		}
	}
	if parameters.body != nil {
		rawSchema, _ := parameters.body.raw.member("schema")
		schema, schemaErr := materializeSwagger20Schema(c.document.graph, rawSchema, parameters.body.resource)
		if schemaErr != nil {
			result.Excluded, result.Reason = true, schemaErr.Error()
			return result
		}
		result.Body = &Swagger20SynthesisBody{Required: parameters.body.required, Schema: schema}
	}

	payload, err := swagger20PayloadFor(parameters, c.document)
	if err != nil {
		result.Excluded, result.Reason = true, err.Error()
		return result
	}
	consumes, err := effectiveSwagger20MediaSet(c.document, operation, "consumes")
	if err != nil {
		result.Excluded, result.Reason = true, err.Error()
		return result
	}
	usableConsumes := 0
	soleConcrete := false
	consumesPrefix := "#/consumes"
	if operation.raw.array("consumes").present {
		consumesPrefix = ref + "/consumes"
	}
	for index, entry := range consumes.entries {
		if payload.kind == "" {
			break
		}
		alternative := Swagger20SynthesisAlternative{SourceRef: fmt.Sprintf("%s/%d", consumesPrefix, index), Kind: "requestMedia", Index: index}
		switch {
		case entry.parseErr != nil:
			alternative.Reason = entry.parseErr.Error()
		case entry.colliding:
			alternative.Reason = "media declaration collides after normalized identity comparison"
		case entry.parsed.rangeSpecificity < 2:
			alternative.Usable = swagger20RangeHasUsableLane(entry.parsed, payload)
			if alternative.Usable {
				alternative.Requirements = append(alternative.Requirements, "configuration.requestMedia")
			} else {
				alternative.Reason = "media range selects no usable request carriage lane"
			}
		default:
			_, laneErr := swagger20LaneForConcrete(entry.parsed, payload)
			alternative.Usable = laneErr == nil
			if laneErr != nil {
				alternative.Reason = laneErr.Error()
			}
		}
		if alternative.Usable {
			usableConsumes++
			soleConcrete = usableConsumes == 1 && entry.parsed.rangeSpecificity == 2
			if entry.parsed.base == "multipart/form-data" && swagger20PayloadHasFile(payload) {
				alternative.Requirements = appendSwagger20Requirement(alternative.Requirements, "configuration.propertyMedia")
			}
		}
		result.Alternatives = append(result.Alternatives, alternative)
	}
	if payload.kind != "" {
		if usableConsumes == 0 {
			if swagger20PayloadIsRequired(payload) {
				result.Excluded, result.Reason = true, "required request payload has no usable effective consumes alternative"
				return result
			}
			result.Body = nil
			result.Parameters = swagger20WithoutFormParameters(result.Parameters)
		} else {
			if usableConsumes != 1 || !soleConcrete {
				for index := range result.Alternatives {
					if result.Alternatives[index].Kind == "requestMedia" && result.Alternatives[index].Usable {
						result.Alternatives[index].Requirements = appendSwagger20Requirement(result.Alternatives[index].Requirements, "configuration.requestMedia")
					}
				}
				if swagger20PayloadIsRequired(payload) {
					result.Requirements = appendSwagger20Requirement(result.Requirements, "configuration.requestMedia")
				}
			}
			if swagger20PayloadIsRequired(payload) && swagger20PayloadHasRequiredFile(payload) {
				result.Requirements = appendSwagger20Requirement(result.Requirements, "configuration.propertyMedia")
			}
		}
	}

	result.Security = analyzeSwagger20Security(c.document, operation, parameters)
	if len(result.Security) > 0 {
		usableSecurity := false
		for _, alternative := range result.Security {
			usableSecurity = usableSecurity || alternative.Usable
		}
		if !usableSecurity {
			result.Excluded, result.Reason = true, "effective security declaration has no usable complete alternative"
			return result
		}
	}
	if len(result.Security) > 1 {
		result.Requirements = appendSwagger20Requirement(result.Requirements, "configuration.security")
	}
	result.Alternatives = append(result.Alternatives, swagger20SecuritySynthesisAlternatives(operation, result.Security)...)
	result.Alternatives = append(result.Alternatives, swagger20ServerSynthesisAlternatives(c.document, operation)...)

	result.Responses = analyzeSwagger20Responses(c.document, operation, responses)
	for _, response := range result.Responses {
		if !response.Usable && response.SchemaPresent {
			result.Alternatives = append(result.Alternatives, Swagger20SynthesisAlternative{
				SourceRef: response.SourceRef, Kind: "response", Usable: false, Reason: response.Reason,
			})
		}
	}
	if swagger20ResponsesUseContentCoding(c.document, operation, responses) {
		result.Requirements = appendSwagger20Requirement(result.Requirements, "configuration.responseContentCodings")
	}
	sort.Strings(result.Requirements)
	return result
}

func swagger20ParameterSchemaImage(parameter *swagger20Parameter) (json.RawMessage, error) {
	if parameter == nil {
		return nil, fmt.Errorf("Swagger 2.0 synthesis parameter is nil")
	}
	keys := []string{"type", "format", "default", "multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum", "maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems", "enum", "items"}
	object := map[string]any{}
	for _, key := range keys {
		if value, present := parameter.raw.member(key); present {
			object[key] = value
		}
	}
	return json.Marshal(object)
}

func swagger20ParameterNeedsConversion(parameter *swagger20Parameter) bool {
	if parameter == nil {
		return false
	}
	if parameter.typeName != "array" {
		return parameter.typeName != "string" && parameter.typeName != "file"
	}
	items := parameter.items
	for items != nil && items.typeName == "array" {
		items = items.items
	}
	return items != nil && items.typeName != "string"
}

func swagger20PayloadHasFile(payload swagger20PayloadModel) bool {
	for _, parameter := range payload.form {
		if parameter.typeName == "file" {
			return true
		}
	}
	return false
}

func swagger20PayloadHasRequiredFile(payload swagger20PayloadModel) bool {
	for _, parameter := range payload.form {
		if parameter.typeName == "file" && parameter.required {
			return true
		}
	}
	return false
}

func swagger20WithoutFormParameters(parameters []Swagger20SynthesisParameter) []Swagger20SynthesisParameter {
	result := parameters[:0]
	for _, parameter := range parameters {
		if parameter.In != Swagger20ParameterFormData {
			result = append(result, parameter)
		}
	}
	return result
}

func analyzeSwagger20Security(document *Swagger20Document, operation swagger20Operation, parameters *swagger20ParameterSet) []Swagger20SynthesisSecurityAlternative {
	requirements, err := effectiveSwagger20Security(document, operation)
	if err != nil {
		return []Swagger20SynthesisSecurityAlternative{{Index: 0, Reason: err.Error()}}
	}
	prefix := "#/security"
	if operation.raw.array("security").present {
		prefix = operationRef(operation) + "/security"
	}
	result := make([]Swagger20SynthesisSecurityAlternative, len(requirements))
	for index, rawRequirement := range requirements {
		alternative := Swagger20SynthesisSecurityAlternative{SourceRef: fmt.Sprintf("%s/%d", prefix, index), Index: index}
		requirement, ok := rawRequirement.(map[string]any)
		if !ok {
			alternative.Reason = "Security Requirement is not an object"
			result[index] = alternative
			continue
		}
		alternative.Anonymous = len(requirement) == 0
		names := make([]string, 0, len(requirement))
		credentials := Swagger20SecurityCredentials{Basic: map[string]Swagger20BasicCredential{}, APIKeys: map[string]string{}, OAuth2: map[string]Swagger20OAuth2Credential{}}
		definitions := document.root.object("securityDefinitions")
		for name := range requirement {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			scheme := Swagger20SynthesisSecurityScheme{Name: name}
			if scopes, ok := requirement[name].([]any); ok {
				for _, rawScope := range scopes {
					if scope, ok := rawScope.(string); ok {
						scheme.Scopes = append(scheme.Scopes, scope)
					}
				}
			}
			if definitions.present && definitions.valid {
				if rawDefinition, present := definitions.value.member(name); present {
					if definition, ok := rawDefinition.(map[string]any); ok {
						scheme.Type = swagger20Object(definition).string("type").value
					}
				}
			}
			switch scheme.Type {
			case "basic":
				credentials.Basic[name] = Swagger20BasicCredential{}
			case "apiKey":
				credentials.APIKeys[name] = ""
			case "oauth2":
				credentials.OAuth2[name] = Swagger20OAuth2Credential{AccessToken: "token", Scopes: append([]string(nil), scheme.Scopes...)}
			}
			alternative.Schemes = append(alternative.Schemes, scheme)
		}
		selected := index
		_, selectionErr := selectSwagger20Security(document, operation, parameters, &selected, credentials)
		alternative.Usable = selectionErr == nil
		if selectionErr != nil {
			alternative.Reason = selectionErr.Error()
		}
		result[index] = alternative
	}
	return result
}

func swagger20SecuritySynthesisAlternatives(operation swagger20Operation, alternatives []Swagger20SynthesisSecurityAlternative) []Swagger20SynthesisAlternative {
	prefix := "#/security"
	if operation.raw.array("security").present {
		prefix = "#/paths/" + escapeJSONPointerSegment(operation.path) + "/" + operation.method + "/security"
	}
	var result []Swagger20SynthesisAlternative
	for _, security := range alternatives {
		if security.Usable {
			continue
		}
		result = append(result, Swagger20SynthesisAlternative{
			SourceRef: fmt.Sprintf("%s/%d", prefix, security.Index), Kind: "security", Index: security.Index, Reason: security.Reason,
		})
	}
	return result
}

func swagger20ServerSynthesisAlternatives(document *Swagger20Document, operation swagger20Operation) []Swagger20SynthesisAlternative {
	member := document.root.array("schemes")
	prefix := "#/schemes"
	if operationMember := operation.raw.array("schemes"); operationMember.present {
		member = operationMember
		prefix = "#/paths/" + escapeJSONPointerSegment(operation.path) + "/" + operation.method + "/schemes"
	}
	if !member.present || !member.valid {
		return nil
	}
	var result []Swagger20SynthesisAlternative
	for index, raw := range member.value {
		scheme, _ := raw.(string)
		alternative := Swagger20SynthesisAlternative{
			SourceRef: fmt.Sprintf("%s/%d", prefix, index), Kind: "server", Index: index,
		}
		alternative.Usable = scheme == "http" || scheme == "https"
		if !alternative.Usable {
			alternative.Reason = fmt.Sprintf("effective scheme %q is unusable", scheme)
		}
		result = append(result, alternative)
	}
	return result
}

func analyzeSwagger20Responses(document *Swagger20Document, operation swagger20Operation, responses swagger20ResponseSet) []Swagger20SynthesisResponse {
	keys := make([]string, 0, len(responses.values))
	for key := range responses.values {
		if key == "default" || swagger20ExactStatusKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	produces, producesErr := effectiveSwagger20MediaSet(document, operation, "produces")
	result := make([]Swagger20SynthesisResponse, 0, len(keys))
	for _, key := range keys {
		entry := Swagger20SynthesisResponse{Key: key, SourceRef: operationRef(operation) + "/responses/" + escapeJSONPointerSegment(key)}
		entry.CanSucceed = key == "default" || (len(key) == 3 && key[0] == '2')
		resolved, err := resolveSwagger20Response(document.graph, responses.values[key], responses.resource, map[string]bool{})
		if err != nil {
			entry.Reason = err.Error()
			result = append(result, entry)
			continue
		}
		entry.Headers = analyzeSwagger20ResponseHeaders(resolved.raw, entry.SourceRef)
		rawSchema, present := resolved.raw.member("schema")
		entry.SchemaPresent = present
		if !present {
			entry.Usable = true
			result = append(result, entry)
			continue
		}
		schema, err := materializeSwagger20Schema(document.graph, rawSchema, resolved.resource)
		if err != nil {
			entry.Reason = err.Error()
			result = append(result, entry)
			continue
		}
		entry.Schema = schema
		if producesErr != nil {
			entry.Reason = producesErr.Error()
			result = append(result, entry)
			continue
		}
		declaration, err := resolveSwagger20SchemaDeclaration(document.graph, rawSchema, resolved.resource, true)
		if err != nil {
			entry.Reason = err.Error()
			result = append(result, entry)
			continue
		}
		for _, media := range produces.entries {
			if media.parseErr != nil || media.colliding {
				continue
			}
			candidates := []parsedMediaType{media.parsed}
			if media.parsed.rangeSpecificity < 2 {
				candidates = nil
				for _, candidate := range []string{"application/json", "text/plain", "application/octet-stream", "image/png"} {
					parsed, _ := parseRevision3MediaType(candidate)
					if requestMediaDeclarationMatches(media.parsed, parsed) {
						candidates = append(candidates, parsed)
					}
				}
			}
			for _, candidate := range candidates {
				if _, laneErr := swagger20ResponseLane(candidate, declaration); laneErr == nil {
					entry.Usable = true
					break
				}
			}
			if entry.Usable {
				break
			}
		}
		if !entry.Usable {
			entry.Reason = "response schema and effective produces define no usable response carriage lane"
		}
		result = append(result, entry)
	}
	return result
}

func analyzeSwagger20ResponseHeaders(response swagger20Object, responseRef string) []Swagger20SynthesisResponseHeader {
	headers := response.object("headers")
	if !headers.present {
		return nil
	}
	if !headers.valid {
		return []Swagger20SynthesisResponseHeader{{
			SourceRef: responseRef + "/headers", Reason: "Response headers is not an object",
		}}
	}
	names := make([]string, 0, len(headers.value))
	identities := map[string]int{}
	for name := range headers.value {
		names = append(names, name)
		identities[strings.ToLower(name)]++
	}
	sort.Strings(names)
	result := make([]Swagger20SynthesisResponseHeader, 0, len(names))
	for _, name := range names {
		entry := Swagger20SynthesisResponseHeader{
			Name: name, SourceRef: responseRef + "/headers/" + escapeJSONPointerSegment(name),
		}
		object, ok := headers.value[name].(map[string]any)
		switch {
		case !ok:
			entry.Reason = "response Header Object is not an object"
		case identities[strings.ToLower(name)] > 1:
			entry.Reason = "response Header Object name collides under ASCII case-insensitive identity"
		default:
			entry.Reason = validateSwagger20ResponseHeaderDeclaration(swagger20Object(object))
			entry.Usable = entry.Reason == ""
		}
		result = append(result, entry)
	}
	return result
}

func validateSwagger20ResponseHeaderDeclaration(raw swagger20Object) string {
	typeName := raw.string("type")
	if !typeName.present || !typeName.valid {
		return "response Header Object requires string type"
	}
	switch typeName.value {
	case "string", "number", "integer", "boolean":
		if raw.string("collectionFormat").present {
			return "response Header Object collectionFormat applies only to arrays"
		}
	case "array":
		items := raw.object("items")
		if !items.present || !items.valid {
			return "array response Header Object requires an Items Object"
		}
		parsed, err := parseSwagger20Items(items.value)
		if err != nil {
			return err.Error()
		}
		if parsed.typeName == "array" {
			return "nested response Header Object arrays have no unambiguous collection serialization"
		}
		collection := "csv"
		if member := raw.string("collectionFormat"); member.present {
			if !member.valid {
				return "response Header Object collectionFormat is not a string"
			}
			collection = member.value
		}
		if collection != "csv" && collection != "ssv" && collection != "tsv" && collection != "pipes" {
			return fmt.Sprintf("response Header Object collectionFormat %q is not admitted", collection)
		}
	default:
		return fmt.Sprintf("response Header Object type %q is not admitted", typeName.value)
	}
	if err := validateSwagger20AssertionDeclaration(raw, typeName.value); err != nil {
		return err.Error()
	}
	if err := validateSwagger20FormatDeclaration(raw); err != nil {
		return err.Error()
	}
	if value, present := raw.member("default"); present {
		if err := validateSwagger20ValueType(value, typeName.value); err != nil {
			return "response Header Object default does not conform: " + err.Error()
		}
		if err := validateSwagger20Assertions(raw, value, typeName.value); err != nil {
			return "response Header Object default does not conform: " + err.Error()
		}
		if err := validateSwagger20Format(raw, value, typeName.value); err != nil {
			return "response Header Object default does not conform: " + err.Error()
		}
	}
	return ""
}

func swagger20ResponsesUseContentCoding(document *Swagger20Document, operation swagger20Operation, responses swagger20ResponseSet) bool {
	for _, value := range responses.values {
		resolved, err := resolveSwagger20Response(document.graph, value, responses.resource, map[string]bool{})
		if err != nil {
			continue
		}
		header, err := swagger20ResponseHeader(resolved.raw, "Content-Encoding")
		if err == nil && header != nil && swagger20CodingDeclarationNeedsCodec(header) {
			return true
		}
	}
	return false
}

func swagger20CodingDeclarationNeedsCodec(raw swagger20Object) bool {
	enum, present := raw.member("enum")
	if !present {
		return true
	}
	values, ok := enum.([]any)
	if !ok || len(values) == 0 {
		return true
	}
	for _, value := range values {
		text, ok := value.(string)
		if !ok || !strings.EqualFold(text, "identity") {
			return true
		}
	}
	return false
}

func operationRef(operation swagger20Operation) string {
	return "#/paths/" + escapeJSONPointerSegment(operation.path) + "/" + operation.method
}

func appendSwagger20Requirement(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

type swagger20SchemaMaterializer struct {
	graph *swagger20ReferenceGraph
	defs  map[string]any
	names map[string]string
	next  int
}

func materializeSwagger20Schema(graph *swagger20ReferenceGraph, value any, resource *swagger20Resource) (json.RawMessage, error) {
	m := &swagger20SchemaMaterializer{graph: graph, defs: map[string]any{}, names: map[string]string{}}
	root, err := m.schema(value, resource)
	if err != nil {
		return nil, err
	}
	if len(m.defs) > 0 {
		root["$defs"] = m.defs
	}
	return json.Marshal(root)
}

func (m *swagger20SchemaMaterializer) schema(value any, resource *swagger20Resource) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Schema Object is not an object")
	}
	raw := swagger20Object(object)
	if reference := raw.string("$ref"); reference.present {
		if !reference.valid || reference.value == "" {
			return nil, fmt.Errorf("Schema Object has an invalid $ref")
		}
		key := artifactResourceKey(resource.base()) + "\x00" + reference.value
		if name := m.names[key]; name != "" {
			return map[string]any{"$ref": "#/$defs/" + escapeJSONPointerSegment(name)}, nil
		}
		name := fmt.Sprintf("schema%d", m.next)
		m.next++
		m.names[key] = name
		m.defs[name] = map[string]any{}
		resolved, err := m.graph.resolveReference(reference.value, resource, newSwagger20ResolutionMemo())
		if err != nil {
			return nil, err
		}
		target, err := m.schema(resolved.node, resolved.resource)
		if err != nil {
			return nil, err
		}
		m.defs[name] = target
		return map[string]any{"$ref": "#/$defs/" + escapeJSONPointerSegment(name)}, nil
	}
	result := make(map[string]any, len(raw))
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		member := raw[key]
		switch key {
		case "items", "additionalProperties":
			if _, isObject := member.(map[string]any); isObject {
				child, err := m.schema(member, resource)
				if err != nil {
					return nil, err
				}
				result[key] = child
			} else {
				result[key] = member
			}
		case "properties":
			properties, ok := member.(map[string]any)
			if !ok {
				result[key] = member
				continue
			}
			projected := map[string]any{}
			propertyNames := make([]string, 0, len(properties))
			for name := range properties {
				propertyNames = append(propertyNames, name)
			}
			sort.Strings(propertyNames)
			for _, name := range propertyNames {
				child, err := m.schema(properties[name], resource)
				if err != nil {
					return nil, err
				}
				projected[name] = child
			}
			result[key] = projected
		case "allOf":
			branches, ok := member.([]any)
			if !ok {
				result[key] = member
				continue
			}
			projected := make([]any, len(branches))
			for index, branch := range branches {
				child, err := m.schema(branch, resource)
				if err != nil {
					return nil, err
				}
				projected[index] = child
			}
			result[key] = projected
		default:
			result[key] = member
		}
	}
	return result, nil
}
