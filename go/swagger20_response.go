package openapiclient

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type swagger20ResponseSet struct {
	values   map[string]any
	resource *swagger20Resource
}

type swagger20ResolvedResponse struct {
	raw      swagger20Object
	resource *swagger20Resource
}

type swagger20ResponseMediaSelection struct {
	media       parsedMediaType
	declaration parsedMediaType
	lane        swagger20MediaLane
}

func swagger20ResponsesFor(graph *swagger20ReferenceGraph, operation swagger20Operation) (swagger20ResponseSet, error) {
	responses := operation.raw.object("responses")
	if !responses.present || !responses.valid {
		return swagger20ResponseSet{}, fmt.Errorf("selected Swagger 2.0 Operation requires a Responses Object")
	}
	count := 0
	for key, value := range responses.value {
		if strings.HasPrefix(strings.ToLower(key), "x-") {
			continue
		}
		if key != "default" && !swagger20ExactStatusKey(key) {
			return swagger20ResponseSet{}, fmt.Errorf("selected Swagger 2.0 Responses Object contains inadmissible key %q", key)
		}
		count++
		// The upstream-invalid governing Response Object exclusion, scoped to the
		// declaration that can govern a SUCCESS response (F1; see
		// `swagger20SuccessResponseKey`). A defective NON-SUCCESS declaration is
		// left standing here: it destroys no representation, and if it actually
		// governs an actual failure response the response rung reports it there.
		if !swagger20SuccessResponseKey(key) {
			continue
		}
		if err := swagger20ResponseObjectDefect(graph, value, operation.resource); err != nil {
			return swagger20ResponseSet{}, fmt.Errorf("selected Swagger 2.0 response %q is upstream-invalid: %w", key, err)
		}
	}
	if count == 0 {
		return swagger20ResponseSet{}, fmt.Errorf("selected Swagger 2.0 Responses Object has no exact status or default Response")
	}
	return swagger20ResponseSet{values: responses.value, resource: operation.resource}, nil
}

func swagger20ExactStatusKey(key string) bool {
	if len(key) != 3 || key[0] < '1' || key[0] > '5' {
		return false
	}
	for index := 1; index < 3; index++ {
		if key[index] < '0' || key[index] > '9' {
			return false
		}
	}
	return true
}

func (s swagger20ResponseSet) governing(graph *swagger20ReferenceGraph, status int) (*swagger20ResolvedResponse, string, error) {
	key := strconv.Itoa(status)
	value, present := s.values[key]
	if !present {
		key = "default"
		value, present = s.values[key]
	}
	if !present {
		return nil, "", nil
	}
	response, err := resolveSwagger20Response(graph, value, s.resource, map[string]bool{})
	if err != nil {
		return nil, key, err
	}
	return &response, key, nil
}

func resolveSwagger20Response(graph *swagger20ReferenceGraph, value any, resource *swagger20Resource, active map[string]bool) (swagger20ResolvedResponse, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return swagger20ResolvedResponse{}, fmt.Errorf("Response or Reference Object is not an object")
	}
	raw := swagger20Object(object)
	if reference, present := raw.member("$ref"); present {
		ref, valid := reference.(string)
		if !valid || ref == "" {
			return swagger20ResolvedResponse{}, fmt.Errorf("Response Reference Object has an invalid $ref")
		}
		key := artifactResourceKey(resource.base()) + "|response|" + ref
		if active[key] {
			return swagger20ResolvedResponse{}, fmt.Errorf("selected Response reference cycle is not resolvable")
		}
		active[key] = true
		defer delete(active, key)
		resolved, err := graph.resolveReference(ref, resource, newSwagger20ResolutionMemo())
		if err != nil {
			return swagger20ResolvedResponse{}, err
		}
		if resolved.cycle {
			return swagger20ResolvedResponse{}, fmt.Errorf("selected Response reference cycle is not resolvable")
		}
		return resolveSwagger20Response(graph, resolved.node, resolved.resource, active)
	}
	// `description` is REQUIRED and its value MUST be a string. ABSENCE is the
	// carve-out `openbindings.openapi-2.0@1` §9.4 states in the same breath as
	// the exclusion: a governing Response Object that omits it while declaring
	// no `schema` loses no representation -- nothing it says about a response
	// body is misdeclared -- so it still GOVERNS, and the omission-with-`schema`
	// case is excluded before dispatch by `swagger20ResponseObjectDefect`
	// rather than refused here. A PRESENT `description` of another kind is not
	// the carve-out: it is a fixed-field violation like any other.
	if description := raw.string("description"); description.present && !description.valid {
		return swagger20ResolvedResponse{}, fmt.Errorf("Response Object `description` is not a string")
	}
	return swagger20ResolvedResponse{raw: raw, resource: resource}, nil
}

// swagger20SuccessResponseKey reports whether a Responses key names a
// declaration that can govern a SUCCESSFUL (2xx final status) response.
//
// Round R2's F1 ruling scopes the upstream-invalid Response Object exclusion to
// the governing SUCCESS declaration, family-wide: a failure body is opaque
// application-authored data, so a defect in the declaration that governs one
// loses no representation and must not destroy a target whose success path is
// intact. It is the same "loses no representation" reasoning that justifies the
// `{}` carve-out, and it is what the 3.0/3.1 acceptance floor already performs
// by never climbing at a non-success response.
//
// `default` ALWAYS qualifies on this edition, and that is the edition
// difference rather than an oversight. OAS 2.0's Responses Object has no range
// keys, so nothing can exhaustively declare the 2xx class; `default` can
// therefore always govern some 2xx status and is always a potential governing
// success declaration. The 3.x siblings qualify `default` on the absence of a
// `2XX` range key, which is the same question asked where ranges exist.
func swagger20SuccessResponseKey(key string) bool {
	if key == "default" {
		return true
	}
	return swagger20ExactStatusKey(key) && key[0] == '2'
}

// swagger20ResponseObjectDefect reports the upstream-invalid governing Response
// Object defects `openbindings.openapi-2.0@1` §9.4 names, in this edition's own
// spelling: the member is not a Response Object at all, or it violates the
// Response Object's fixed-field constraints -- a `description` that is not a
// string, a `schema` that is not a Schema Object, a `headers` or `examples`
// value that is not a map, or a `headers` member that is not a Header Object.
//
// It is a DECIDABLE test and deliberately not a validator, exactly as the
// 3.0/3.1 floor's D16 is. Each of `headers` and `examples` is a map in this
// edition, so a present non-map is a violation no reading can rescue; a
// `headers` member is a Header Object, and OAS 2.0 names no Reference Object
// alternative at that position, so a present non-object is the same kind of
// proof. A `schema` is a Schema Object, and this edition has no boolean-literal
// schemas, so a present non-object is likewise decidable. Nothing here inspects
// a Header Object's or a Schema Object's own fields: those are declaration
// questions the response and schema rungs already own, and a wrong answer would
// cost a target its representation.
//
// A `$ref`ed Response Object governs its referencing target exactly as an
// inline one does, so the constraints are read at the position they actually
// occupy -- inside the referenced object -- which is why this resolves first.
// The 3.0/3.1 floor reads D16 inside a `$ref`ed response for the same reason.
func swagger20ResponseObjectDefect(graph *swagger20ReferenceGraph, value any, resource *swagger20Resource) error {
	resolved, err := resolveSwagger20Response(graph, value, resource, map[string]bool{})
	if err != nil {
		return err
	}
	raw := resolved.raw
	_, descriptionDeclared := raw.member("description")
	schema, schemaDeclared := raw.member("schema")
	if !descriptionDeclared && schemaDeclared {
		// D9's declared-content gate in this edition's spelling. Without a
		// `schema` this is the carve-out and never reaches here.
		return fmt.Errorf("Response Object omits its REQUIRED description while declaring a schema")
	}
	if schemaDeclared {
		if _, isObject := schema.(map[string]any); !isObject {
			return fmt.Errorf("Response Object `schema` is not a Schema Object")
		}
	}
	for _, field := range []string{"headers", "examples"} {
		raw, present := raw.member(field)
		if !present {
			continue
		}
		members, isMap := raw.(map[string]any)
		if !isMap {
			return fmt.Errorf("Response Object %q is not a map", field)
		}
		if field != "headers" {
			continue
		}
		names := make([]string, 0, len(members))
		for name := range members {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			// The specification-extension prefix is lower-case `x-`, and the
			// test is case-SENSITIVE for the same reason the 3.0/3.1 floor's is:
			// these keys are HEADER NAMES, and `X-Request-Id` is an ordinary
			// header rather than an extension. Lower-casing here would exempt
			// most real custom headers from the rule.
			if strings.HasPrefix(name, "x-") {
				continue
			}
			if _, isObject := members[name].(map[string]any); !isObject {
				return fmt.Errorf("Response Object header %q is not a Header Object", name)
			}
		}
	}
	return nil
}

func selectSwagger20ResponseMedia(set swagger20MediaSet, actual string, declaration swagger20SchemaDeclaration) (*swagger20ResponseMediaSelection, error) {
	media, err := parseRevision3MediaType(actual)
	if err != nil {
		return nil, fmt.Errorf("response Content-Type: %w", err)
	}
	type candidate struct {
		entry swagger20MediaEntry
	}
	bestSpecificity, bestParameters := -1, -1
	var best []candidate
	for _, entry := range set.entries {
		if entry.parseErr != nil || entry.colliding || !requestMediaDeclarationMatches(entry.parsed, media) {
			continue
		}
		specificity, parameters := entry.parsed.rangeSpecificity, len(entry.parsed.params)
		switch {
		case specificity > bestSpecificity || specificity == bestSpecificity && parameters > bestParameters:
			bestSpecificity, bestParameters = specificity, parameters
			best = []candidate{{entry: entry}}
		case specificity == bestSpecificity && parameters == bestParameters:
			best = append(best, candidate{entry: entry})
		}
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("response media %q matches no non-colliding effective produces declaration", media.canonical)
	}
	if len(best) > 1 {
		return nil, fmt.Errorf("response media %q ambiguously matches effective produces", media.canonical)
	}
	lane, err := swagger20ResponseLane(media, declaration)
	if err != nil {
		return nil, err
	}
	return &swagger20ResponseMediaSelection{media: media, declaration: best[0].entry.parsed, lane: lane}, nil
}

func swagger20ResponseLane(media parsedMediaType, declaration swagger20SchemaDeclaration) (swagger20MediaLane, error) {
	if isSwagger20JSONMedia(media.base) {
		return swagger20LaneJSON, nil
	}
	if declaration.artifactByteString() {
		return swagger20LaneByteString, nil
	}
	if declaration.rawOctets() {
		return swagger20LaneRawOctets, nil
	}
	if isSwagger20CharacterMedia(media.base) && declaration.admitsStringAsSoleNonNullType() {
		if err := supportedTextCharset(media); err != nil {
			return "", err
		}
		return swagger20LaneText, nil
	}
	return "", fmt.Errorf("response media %q and its resolved declaration define no response byte carriage", media.canonical)
}

func handleSwagger20Response(request *http.Request, response *http.Response, prepared *Swagger20PreparedOperation, responses swagger20ResponseSet, execution *Execution) {
	governing, responseKey, err := responses.governing(prepared.document.graph, response.StatusCode)
	if err != nil {
		swagger20FailResponse(execution, err)
		return
	}
	body, err := readSwagger20ResponseBody(response, prepared.options.MaxDeliveryUnitBytes)
	if err != nil {
		swagger20FailResponse(execution, err)
		return
	}
	if request.Method == http.MethodHead {
		body = nil
	}
	if err := decodeSwagger20ResponseContentCodings(response, governing, &body, prepared.options.ResponseContentCodings); err != nil {
		swagger20FailResponse(execution, err)
		return
	}
	execution.setHTTPResponseBody(body)
	execution.setTrailer(headerMetadata(response.Trailer))

	success := response.StatusCode >= 200 && response.StatusCode < 300
	if len(body) == 0 {
		if success {
			execution.closeOutputBoundary()
			return
		}
		execution.failExecution(swagger20HTTPFailure(response, responseKey, nil, false))
		return
	}
	if governing == nil {
		swagger20FailResponse(execution, fmt.Errorf("non-empty response status %d has no governing exact or default Response Object", response.StatusCode))
		return
	}
	schema, present := governing.raw.member("schema")
	if !present {
		swagger20FailResponse(execution, fmt.Errorf("non-empty response is governed by a Response Object without schema"))
		return
	}
	declaration, err := resolveSwagger20SchemaDeclaration(prepared.document.graph, schema, governing.resource, true)
	if err != nil {
		swagger20FailResponse(execution, fmt.Errorf("governing response schema: %w", err))
		return
	}
	contentType, err := singletonResponseHeader(response.Header, "Content-Type")
	if err != nil {
		swagger20FailResponse(execution, err)
		return
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	produces, err := effectiveSwagger20MediaSet(prepared.document, prepared.operation, "produces")
	if err != nil {
		swagger20FailResponse(execution, err)
		return
	}
	selection, err := selectSwagger20ResponseMedia(produces, contentType, declaration)
	if err != nil {
		swagger20FailResponse(execution, err)
		return
	}
	value, err := decodeSwagger20ResponseValue(selection, body)
	if err != nil {
		swagger20FailResponse(execution, err)
		return
	}
	if !success {
		execution.failExecution(swagger20HTTPFailure(response, responseKey, value, true))
		return
	}
	if err := execution.emitOutput(value); err != nil {
		return
	}
	execution.closeOutputBoundary()
}

func readSwagger20ResponseBody(response *http.Response, configured int64) ([]byte, error) {
	limit := deliveryUnitLimit(configured)
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d byte limit", limit)
	}
	return body, nil
}

func decodeSwagger20ResponseContentCodings(response *http.Response, governing *swagger20ResolvedResponse, body *[]byte, codecs map[string]ContentDecoder) error {
	raw, err := singletonResponseHeader(response.Header, "Content-Encoding")
	if err != nil || raw == "" {
		return err
	}
	if governing == nil {
		return fmt.Errorf("coded response has no governing Response Object")
	}
	header, err := swagger20ResponseHeader(governing.raw, "Content-Encoding")
	if err != nil {
		return err
	}
	if header == nil {
		return fmt.Errorf("actual response Content-Encoding has no governing Header Object")
	}
	if err := swagger20HeaderObjectAdmits(header, raw); err != nil {
		return fmt.Errorf("actual response Content-Encoding is not admitted by its governing Header Object: %w", err)
	}
	tokens, err := parsedContentCodings(raw)
	if err != nil {
		return err
	}
	for index := len(tokens) - 1; index >= 0; index-- {
		token := tokens[index]
		if token == "identity" {
			continue
		}
		codec := codecs[token]
		if codec == nil {
			return fmt.Errorf("response content-coding %q is unsupported", token)
		}
		*body, err = codec(*body)
		if err != nil {
			return fmt.Errorf("response content-coding %q failed: %w", token, err)
		}
	}
	return nil
}

func swagger20ResponseHeader(response swagger20Object, name string) (swagger20Object, error) {
	headers := response.object("headers")
	if !headers.present {
		return nil, nil
	}
	if !headers.valid {
		return nil, fmt.Errorf("governing Response headers is not an object")
	}
	var found swagger20Object
	for declared, value := range headers.value {
		if !strings.EqualFold(declared, name) {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("governing response Header Object %q is not an object", declared)
		}
		if found != nil {
			return nil, fmt.Errorf("governing response has ambiguous case-insensitive Header Objects named %q", name)
		}
		found = swagger20Object(object)
	}
	return found, nil
}

func swagger20HeaderObjectAdmits(header swagger20Object, value string) error {
	typeName := header.string("type")
	if !typeName.present || !typeName.valid || typeName.value != "string" {
		return fmt.Errorf("Header Object does not declare type string")
	}
	if err := validateSwagger20AssertionDeclaration(header, "string"); err != nil {
		return err
	}
	if err := validateSwagger20Assertions(header, value, "string"); err != nil {
		return err
	}
	return validateSwagger20Format(header, value, "string")
}

func decodeSwagger20ResponseValue(selection *swagger20ResponseMediaSelection, body []byte) (any, error) {
	switch selection.lane {
	case swagger20LaneJSON:
		var value any
		if err := parseStrictJSON(body, &value); err != nil {
			return nil, fmt.Errorf("response declares %q but body is not strict JSON: %w", selection.media.canonical, err)
		}
		return value, nil
	case swagger20LaneText:
		return decodeTextLaneFor(selection.media.canonical, body, profileFullCoordinate)
	case swagger20LaneByteString:
		if !utf8.Valid(body) {
			return nil, fmt.Errorf("format byte response is not UTF-8 Base64 characters")
		}
		text := string(body)
		if _, err := base64.StdEncoding.DecodeString(text); err != nil {
			return nil, fmt.Errorf("format byte response is not standard padded Base64: %w", err)
		}
		return text, nil
	case swagger20LaneRawOctets:
		return base64.StdEncoding.EncodeToString(body), nil
	default:
		return nil, fmt.Errorf("unknown Swagger 2.0 response lane %q", selection.lane)
	}
}

func swagger20FailResponse(execution *Execution, err error) {
	execution.failExecution(&ExecutionError{Code: CodeResponseError, Message: err.Error(), Cause: err})
}

func swagger20HTTPFailure(response *http.Response, responseKey string, details any, detailsPresent bool) *ExecutionError {
	failure := httpFailureError(response.StatusCode, response.Status)
	failure.Details = details
	failure.DetailsPresent = detailsPresent
	evidence, _ := failure.Evidence.(map[string]any)
	if evidence == nil {
		evidence = map[string]any{"status": response.StatusCode}
		failure.Evidence = evidence
		failure.Diagnostics = evidence
	}
	evidence["openapi"] = map[string]any{"declared": responseKey != "", "responseKey": responseKey}
	return failure
}
