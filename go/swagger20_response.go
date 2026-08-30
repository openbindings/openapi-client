package openapiclient

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
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

func swagger20ResponsesFor(operation swagger20Operation) (swagger20ResponseSet, error) {
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
		if _, ok := value.(map[string]any); !ok {
			return swagger20ResponseSet{}, fmt.Errorf("selected Swagger 2.0 response %q is not a Response or Reference Object", key)
		}
		count++
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
	description := raw.string("description")
	if !description.present || !description.valid {
		return swagger20ResolvedResponse{}, fmt.Errorf("Response Object requires string description")
	}
	return swagger20ResolvedResponse{raw: raw, resource: resource}, nil
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
