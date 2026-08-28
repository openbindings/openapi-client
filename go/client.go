package openapiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

type Method string

const (
	GET     Method = "get"
	PUT     Method = "put"
	POST    Method = "post"
	DELETE  Method = "delete"
	OPTIONS Method = "options"
	HEAD    Method = "head"
	PATCH   Method = "patch"
	TRACE   Method = "trace"
)

type OperationSelector struct {
	operationID string
	path        string
	method      Method
	ref         string
}

func OperationID(value string) OperationSelector { return OperationSelector{operationID: value} }
func PathOperation(path string, method Method) OperationSelector {
	return OperationSelector{path: path, method: method}
}
func OperationRef(value string) OperationSelector { return OperationSelector{ref: value} }

type OperationInfo struct {
	Ref         string
	Path        string
	Method      Method
	OperationID string
	Summary     string
	Tags        []string
}

type Parameters struct {
	Path   map[string]any
	Query  map[string]any
	Header map[string]any
	Cookie map[string]any
}

type Input struct {
	Parameters Parameters
	// Body is the native application body. Raw media accepts []byte or string;
	// callers never provide the engine's private Base64 carriage form.
	Body any
	// BodyPresent distinguishes an authored JSON null body from omission.
	// Non-nil Body values are present without setting this field.
	BodyPresent bool
	MediaType   string
	// PropertyMediaTypes supplies concrete media types for multipart or form
	// properties whose Encoding contentType is a list/range, and for the OAS
	// 3.0 typeless multipart cell that has no artifact default.
	PropertyMediaTypes map[string]string
}

type BasicCredential struct {
	Username string
	Password string
}

type ServerSelection struct {
	Index     *int
	URL       string
	BaseURL   string
	Variables map[string]string
}

type ClientOptions struct {
	HTTPClient             *http.Client
	Auth                   map[string]any
	Server                 any
	Headers                http.Header
	MaxResponseBytes       int64
	SecurityHandlers       map[string]SecurityHandler
	ParameterConverter     ParameterConverter
	RequestContentCodings  map[string]ContentEncoder
	ResponseContentCodings map[string]ContentDecoder
}

type CallOptions struct {
	HTTPClient             *http.Client
	Auth                   map[string]any
	Server                 any
	Headers                http.Header
	MaxResponseBytes       int64
	SecurityHandlers       map[string]SecurityHandler
	ParameterConverter     ParameterConverter
	RequestContentCodings  map[string]ContentEncoder
	ResponseContentCodings map[string]ContentDecoder
}

type DeclarationMatch struct {
	Declared    bool
	ResponseKey string
	MediaType   string
}

type Result struct {
	OK       bool
	Data     any
	Error    any
	Response *http.Response
	OpenAPI  DeclarationMatch
}

type StreamEvent struct {
	Data any
	SSE  *SSEMetadata
}

type SSEMetadata struct {
	Event string
	ID    string
	Retry *int
}

type Stream struct {
	execution *Execution
}

func (s *Stream) Next(ctx context.Context) (StreamEvent, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case event, open := <-s.execution.Events():
		if !open {
			if err := s.execution.Wait(); err != nil {
				return StreamEvent{}, false, clientError(err)
			}
			return StreamEvent{}, false, nil
		}
		return StreamEvent{Data: event.Value, SSE: sseMetadata(event.Metadata)}, true, nil
	case <-ctx.Done():
		return StreamEvent{}, false, &ClientError{Kind: ErrorCancelled, Code: CodeCancelled, Message: "stream receive cancelled", Cause: ctx.Err()}
	}
}
func (s *Stream) Cancel()               { s.execution.Cancel() }
func (s *Stream) Done() <-chan struct{} { return s.execution.Done() }
func (s *Stream) Wait() error           { return clientError(s.execution.Wait()) }

type StreamResult struct {
	OK          bool
	Stream      *Stream
	Error       any
	Response    *http.Response
	OpenAPI     DeclarationMatch
	rawBoundary bool
}

type ErrorKind string

const (
	ErrorSource        ErrorKind = "source"
	ErrorOperation     ErrorKind = "operation"
	ErrorInput         ErrorKind = "input"
	ErrorConfiguration ErrorKind = "configuration"
	ErrorTransport     ErrorKind = "transport"
	ErrorProtocol      ErrorKind = "protocol"
	ErrorResponse      ErrorKind = "response"
	ErrorCancelled     ErrorKind = "cancelled"
	ErrorInternal      ErrorKind = "internal"
)

type ClientError struct {
	Kind    ErrorKind
	Code    string
	Message string
	Details any
	Cause   error
}

func (e *ClientError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}
func (e *ClientError) Unwrap() error { return e.Cause }

type Client struct {
	document *openapi3.T
	floor    *acceptanceFloor
	source   Source
	options  ClientOptions
}

func Load(ctx context.Context, source Source, options ClientOptions) (*Client, error) {
	client := options.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}
	document, floor, err := loadDocument(ctx, client, source, true)
	if err != nil {
		return nil, &ClientError{Kind: ErrorSource, Code: "SOURCE_LOAD_FAILED", Message: err.Error(), Cause: err}
	}
	source.Document = document
	source.Content = nil
	return &Client{document: document, floor: floor, source: source, options: options}, nil
}

func (c *Client) Document() *openapi3.T { return c.document }

func (c *Client) Operations() []OperationInfo {
	operations := enumerateOperationsWithFloor(c.document, c.floor)
	result := make([]OperationInfo, len(operations))
	for index := range operations {
		result[index] = operations[index].info
	}
	return result
}

func (c *Client) Call(ctx context.Context, selector OperationSelector, input Input, options ...CallOptions) (*Result, error) {
	streamResult, err := c.Stream(ctx, selector, input, options...)
	if err != nil {
		return nil, err
	}
	if !streamResult.OK {
		return &Result{OK: false, Error: streamResult.Error, Response: streamResult.Response, OpenAPI: streamResult.OpenAPI}, nil
	}
	if isSSEContentTypeFor(streamResult.Response.Header.Get("Content-Type"), profileFullCoordinate) {
		streamResult.Stream.Cancel()
		return nil, &ClientError{Kind: ErrorResponse, Code: "STREAMING_RESPONSE", Message: "operation returned text/event-stream; use Client.Stream"}
	}
	var values []any
	for {
		event, open, err := streamResult.Stream.Next(ctx)
		if err != nil {
			return nil, err
		}
		if !open {
			break
		}
		values = append(values, event.Data)
	}
	if len(values) > 1 {
		return nil, &ClientError{Kind: ErrorResponse, Code: "STREAMING_RESPONSE", Message: "operation produced multiple values; use Client.Stream"}
	}
	response, err := streamResult.Stream.execution.Response(ctx)
	if err == nil {
		streamResult.Response = response
	}
	var data any
	if len(values) == 1 {
		data = nativeSuccessValue(values[0], streamResult.rawBoundary)
	}
	return &Result{OK: true, Data: data, Response: streamResult.Response, OpenAPI: streamResult.OpenAPI}, nil
}

func (c *Client) Stream(ctx context.Context, selector OperationSelector, input Input, options ...CallOptions) (*StreamResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := resolveOperation(c.document, c.floor, selector)
	if err != nil {
		return nil, err
	}
	native, err := nativeInput(c.document, resolved.pathItem, resolved.operation, input)
	if err != nil {
		return nil, err
	}
	call := CallOptions{}
	if len(options) > 1 {
		return nil, &ClientError{Kind: ErrorConfiguration, Code: "TOO_MANY_CALL_OPTIONS", Message: "Call accepts at most one CallOptions value"}
	}
	if len(options) == 1 {
		call = options[0]
	}
	prepare, err := c.prepareOptions(resolved, native, call)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareDocument(c.document, prepare)
	if err != nil {
		return nil, clientError(err)
	}
	execution, err := prepared.Start(ctx)
	if err != nil {
		return nil, clientError(err)
	}
	if native.supplied {
		if err := execution.Send(ctx, native.value); err != nil {
			execution.Cancel()
			return nil, clientError(err)
		}
	}
	if err := execution.FinishInput(); err != nil {
		execution.Cancel()
		return nil, clientError(err)
	}
	response, err := execution.Response(ctx)
	if err != nil {
		return nil, clientError(err)
	}
	declaration := declarationMatch(resolved.operation, response)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		terminal := execution.Wait()
		if replay, replayErr := execution.Response(ctx); replayErr == nil {
			response = replay
		}
		failure := decodeFailure(responseBody(response), response.Header.Get("Content-Type"))
		if evidence, ok := FailureEvidenceFrom(terminal); ok {
			failure = decodeFailure(evidence.HTTPResponse.Body, headerValue(evidence.HTTPResponse.Headers, "content-type"))
			declaration = DeclarationMatch{Declared: evidence.OpenAPI.Declared, ResponseKey: evidence.OpenAPI.ResponseKey, MediaType: evidence.OpenAPI.GoverningMedia}
		}
		return &StreamResult{OK: false, Error: failure, Response: response, OpenAPI: declaration}, nil
	}
	return &StreamResult{OK: true, Stream: &Stream{execution: execution}, Response: response, OpenAPI: declaration, rawBoundary: nativeResponseUsesRawBoundary(c.document, resolved.operation, response)}, nil
}

type resolvedOperation struct {
	info      OperationInfo
	pathItem  *openapi3.PathItem
	operation *openapi3.Operation
}

func enumerateOperations(document *openapi3.T) []resolvedOperation {
	return enumerateOperationsWithFloor(document, nil)
}

// enumerateOperationsWithFloor applies the acceptance-floor inventory filter
// (openbindings.openapi@1 §3): a ladder-invalid target is not addressed and
// is not enumerated as invocable.
func enumerateOperationsWithFloor(document *openapi3.T, floor *acceptanceFloor) []resolvedOperation {
	if document == nil || document.Paths == nil {
		return nil
	}
	paths := document.Paths.Map()
	names := make([]string, 0, len(paths))
	for path := range paths {
		names = append(names, path)
	}
	sort.Strings(names)
	result := []resolvedOperation{}
	for _, path := range names {
		item := paths[path]
		if item == nil {
			continue
		}
		for _, method := range httpMethods {
			operation := item.GetOperation(strings.ToUpper(method))
			if operation == nil {
				continue
			}
			if verdict := floor.opVerdict("#/paths/" + escapeJSONPointerSegment(path) + "/" + method); verdict != nil && verdict.Disposition == "invalid" {
				continue
			}
			result = append(result, resolvedOperation{pathItem: item, operation: operation, info: OperationInfo{
				Ref: "#/paths/" + escapeJSONPointerSegment(path) + "/" + method, Path: path, Method: Method(method),
				OperationID: operation.OperationID, Summary: operation.Summary, Tags: append([]string(nil), operation.Tags...),
			}})
		}
	}
	return result
}

func resolveOperation(document *openapi3.T, floor *acceptanceFloor, selector OperationSelector) (resolvedOperation, error) {
	operations := enumerateOperationsWithFloor(document, floor)
	if selector.operationID != "" {
		matches := []resolvedOperation{}
		for _, operation := range operations {
			if operation.info.OperationID == selector.operationID {
				matches = append(matches, operation)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return resolvedOperation{}, &ClientError{Kind: ErrorOperation, Code: "DUPLICATE_OPERATION_ID", Message: fmt.Sprintf("operationId %q is not unique", selector.operationID)}
		}
		return resolvedOperation{}, &ClientError{Kind: ErrorOperation, Code: "OPERATION_NOT_FOUND", Message: fmt.Sprintf("operationId %q was not found", selector.operationID)}
	}
	if selector.ref != "" {
		path, method, err := parseRef(selector.ref)
		if err != nil {
			return resolvedOperation{}, &ClientError{Kind: ErrorOperation, Code: "INVALID_OPERATION_REF", Message: err.Error(), Cause: err}
		}
		selector.path, selector.method = path, Method(method)
	}
	for _, operation := range operations {
		if operation.info.Path == selector.path && operation.info.Method == Method(strings.ToLower(string(selector.method))) {
			return operation, nil
		}
	}
	return resolvedOperation{}, &ClientError{Kind: ErrorOperation, Code: "OPERATION_NOT_FOUND", Message: fmt.Sprintf("%s %s was not found", strings.ToUpper(string(selector.method)), selector.path)}
}

type nativeInvocationInput struct {
	supplied           bool
	value              any
	mediaType          string
	propertyMediaTypes map[string]string
}

func nativeInput(document *openapi3.T, pathItem *openapi3.PathItem, operation *openapi3.Operation, input Input) (nativeInvocationInput, error) {
	parameters := effectiveParameters(pathItem, operation)
	plans, err := planRequestBodiesFor(document, operation, profileFullCoordinate)
	if err != nil {
		return nativeInvocationInput{}, inputError("BODY_UNSUPPORTED", err.Error(), err)
	}
	selected := []*bodyPlan{}
	if input.MediaType != "" {
		selected, err = configuredRequestPlansFor(document, operation, plans, map[string]any{"configuration": map[string]any{"requestMedia": input.MediaType}}, profileFullCoordinate)
		if err != nil {
			return nativeInvocationInput{}, inputError("REQUEST_MEDIA_NOT_DECLARED", err.Error(), err)
		}
		if len(selected) == 0 {
			return nativeInvocationInput{}, inputError("REQUEST_MEDIA_NOT_DECLARED", fmt.Sprintf("request media %q is not a supported declaration", input.MediaType), nil)
		}
	} else {
		for _, plan := range plans {
			if !plan.mediaRange {
				selected = []*bodyPlan{plan}
				break
			}
		}
	}
	routes := planAbstractInputRoutes(parameters, selected)
	value := map[string]any{}
	supplied := false
	locations := []struct {
		name   string
		values map[string]any
	}{{"path", input.Parameters.Path}, {"query", input.Parameters.Query}, {"header", input.Parameters.Header}, {"cookie", input.Parameters.Cookie}}
	for _, location := range locations {
		for name, member := range location.values {
			found := false
			for _, parameter := range parameters {
				if parameter != nil && parameter.Value != nil && parameter.Value.In == location.name && parameter.Value.Name == name {
					found = true
					break
				}
			}
			if !found {
				return nativeInvocationInput{}, inputError("UNKNOWN_PARAMETER", fmt.Sprintf("operation does not declare %s parameter %q", location.name, name), nil)
			}
			value[routes.parameterField(location.name, name)] = member
			supplied = true
		}
	}
	bodyPresent := input.BodyPresent || input.Body != nil
	if bodyPresent {
		if len(selected) == 0 {
			return nativeInvocationInput{}, inputError("BODY_NOT_DECLARED", "operation does not declare a supported request body", nil)
		}
		plan := selected[0]
		bodyValue, bodyErr := nativeRequestBody(plan, input.Body)
		if bodyErr != nil {
			return nativeInvocationInput{}, bodyErr
		}
		if plan.synthetic || plan.wholeObject {
			value[routes.wholeBodyField] = bodyValue
		} else {
			body, ok := toStringAnyMap(bodyValue)
			if !ok {
				return nativeInvocationInput{}, inputError("OBJECT_BODY_REQUIRED", fmt.Sprintf("%s requires an object body", plan.mediaType), nil)
			}
			for name, member := range body {
				value[routes.bodyField(name)] = member
			}
		}
		supplied = true
	}
	routeParameters := make([]any, 0, len(routes.parameters))
	for _, route := range routes.parameters {
		routeParameters = append(routeParameters, map[string]any{"in": route.In, "name": route.Name, "field": route.Field})
	}
	body := map[string]any{}
	if len(routes.bodyFields) > 0 {
		properties := map[string]any{}
		for name, field := range routes.bodyFields {
			properties[name] = field
		}
		body["properties"] = properties
	}
	if routes.wholeBodyField != "" {
		body["whole"] = routes.wholeBodyField
	}
	if bodyPresent {
		body["present"] = true
	}
	mediaType := input.MediaType
	if mediaType == "" && len(selected) > 0 {
		mediaType = selected[0].mediaType
	}
	profile := FullProfile()
	propertyMediaTypes := make(map[string]string, len(input.PropertyMediaTypes))
	for name, selected := range input.PropertyMediaTypes {
		propertyMediaTypes[name] = selected
	}
	return nativeInvocationInput{supplied: supplied, mediaType: mediaType, propertyMediaTypes: propertyMediaTypes, value: []any{map[string]any{
		profile.InputRouteKey: profile.InputRouteMarker, "value": value, "parameters": routeParameters, "body": body,
	}}}, nil
}

func nativeRequestBody(plan *bodyPlan, body any) (any, error) {
	if plan == nil || !plan.rawBoundary {
		return body, nil
	}
	var value []byte
	switch typed := body.(type) {
	case []byte:
		value = typed
	case json.RawMessage:
		value = []byte(typed)
	case string:
		value = []byte(typed)
	default:
		return nil, inputError("RAW_BODY_BYTES_REQUIRED", fmt.Sprintf("%s requires a []byte or string body", plan.mediaType), nil)
	}
	return base64.StdEncoding.EncodeToString(value), nil
}

func (c *Client) prepareOptions(operation resolvedOperation, input nativeInvocationInput, call CallOptions) (PrepareOptions, error) {
	client := call.HTTPClient
	if client == nil {
		client = c.options.HTTPClient
	}
	if client == nil {
		client = defaultInvocationHTTPClient()
	}
	maxBytes := call.MaxResponseBytes
	if maxBytes == 0 {
		maxBytes = c.options.MaxResponseBytes
	}
	headers := c.options.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	for name, values := range call.Headers {
		headers[name] = append([]string(nil), values...)
	}
	server := call.Server
	if server == nil {
		server = c.options.Server
	}
	configuration := map[string]any{}
	if server != nil {
		configuration["server"] = normalizeServerSelection(server)
	}
	if input.mediaType != "" {
		configuration["requestMedia"] = input.mediaType
	}
	if len(input.propertyMediaTypes) > 0 {
		configuration["propertyMedia"] = input.propertyMediaTypes
	}
	contextValue := map[string]any{}
	if len(configuration) > 0 {
		contextValue["configuration"] = configuration
	}
	if len(headers) > 0 {
		flattened := map[string]string{}
		for name, values := range headers {
			if len(values) > 0 {
				flattened[name] = values[len(values)-1]
			}
		}
		contextValue["headers"] = flattened
	}
	auth := map[string]any{}
	for name, value := range c.options.Auth {
		auth[name] = value
	}
	for name, value := range call.Auth {
		auth[name] = value
	}
	handlers := map[string]SecurityHandler{}
	for name, handler := range c.options.SecurityHandlers {
		handlers[name] = handler
	}
	for name, handler := range call.SecurityHandlers {
		handlers[name] = handler
	}
	for name, credential := range auth {
		scheme, ok := securityScheme(c.document, name)
		if !ok {
			return PrepareOptions{}, &ClientError{Kind: ErrorConfiguration, Code: "UNKNOWN_SECURITY_SCHEME", Message: fmt.Sprintf("security scheme %q was not found", name)}
		}
		handler, err := credentialHandler(name, scheme, credential)
		if err != nil {
			return PrepareOptions{}, err
		}
		handlers[name] = handler
	}
	for name := range handlers {
		if _, ok := securityScheme(c.document, name); !ok {
			return PrepareOptions{}, &ClientError{Kind: ErrorConfiguration, Code: "UNKNOWN_SECURITY_SCHEME", Message: fmt.Sprintf("security scheme %q was not found", name)}
		}
	}
	converter := call.ParameterConverter
	if converter == nil {
		converter = c.options.ParameterConverter
	}
	requestCodings := call.RequestContentCodings
	if requestCodings == nil {
		requestCodings = c.options.RequestContentCodings
	}
	responseCodings := call.ResponseContentCodings
	if responseCodings == nil {
		responseCodings = c.options.ResponseContentCodings
	}
	contextValue = contextWithSecurityHandlers(contextValue, handlers)
	return PrepareOptions{
		Source: c.source, Ref: operation.info.Ref, Profile: FullProfile(), Context: contextValue,
		HTTPClient: client, MaxDeliveryUnitBytes: maxBytes, SecurityHandlers: handlers,
		ParameterConverter: converter, RequestContentCodings: requestCodings, ResponseContentCodings: responseCodings,
	}, nil
}

func securityScheme(document *openapi3.T, name string) (*openapi3.SecurityScheme, bool) {
	if document == nil || document.Components == nil || document.Components.SecuritySchemes == nil {
		return nil, false
	}
	ref, ok := document.Components.SecuritySchemes[name]
	if !ok || ref == nil || ref.Value == nil {
		return nil, false
	}
	return ref.Value, true
}

func credentialHandler(name string, scheme *openapi3.SecurityScheme, credential any) (SecurityHandler, error) {
	configurationError := func(expected string) (SecurityHandler, error) {
		return nil, &ClientError{Kind: ErrorConfiguration, Code: "INVALID_CREDENTIAL", Message: fmt.Sprintf("security scheme %q requires %s", name, expected)}
	}
	wireError := func(message string) (SecurityHandler, error) {
		return nil, &ClientError{Kind: ErrorConfiguration, Code: "INVALID_CREDENTIAL", Message: message}
	}
	switch scheme.Type {
	case "apiKey":
		value, ok := credential.(string)
		if !ok {
			return configurationError("a string API key")
		}
		if scheme.In == "cookie" && !validRFC6265CookieValue(value) {
			return wireError(fmt.Sprintf("cookie credential %q cannot be carried as an RFC 6265 cookie-value", name))
		}
		return func(request *http.Request, _ SecurityHandlerContext) error {
			switch scheme.In {
			case "header":
				request.Header.Set(scheme.Name, value)
			case "query":
				unit := percentEncodeCredentialQuery(scheme.Name) + "=" + percentEncodeCredentialQuery(value)
				if request.URL.RawQuery == "" {
					request.URL.RawQuery = unit
				} else {
					request.URL.RawQuery += "&" + unit
				}
			case "cookie":
				request.AddCookie(&http.Cookie{Name: scheme.Name, Value: value})
			default:
				return fmt.Errorf("unsupported apiKey location %q", scheme.In)
			}
			return nil
		}, nil
	case "http":
		switch strings.ToLower(scheme.Scheme) {
		case "basic":
			value, ok := credential.(BasicCredential)
			if !ok {
				return configurationError("BasicCredential")
			}
			if strings.Contains(value.Username, ":") || !validBasicCredentialText(value.Username) || !validBasicCredentialText(value.Password) {
				return wireError(fmt.Sprintf("basic credential %q violates RFC 7617 user-id or character-encoding constraints", name))
			}
			return func(request *http.Request, _ SecurityHandlerContext) error {
				request.SetBasicAuth(value.Username, value.Password)
				return nil
			}, nil
		case "bearer":
			value, ok := credential.(string)
			if !ok {
				return configurationError("a token string")
			}
			if !validBearerToken(value) {
				return wireError(fmt.Sprintf("bearer credential %q is not an RFC 6750 b64token", name))
			}
			return func(request *http.Request, _ SecurityHandlerContext) error {
				request.Header.Set("Authorization", "Bearer "+value)
				return nil
			}, nil
		default:
			return nil, &ClientError{Kind: ErrorConfiguration, Code: "UNSUPPORTED_SECURITY_SCHEME", Message: fmt.Sprintf("security scheme %q requires a custom SecurityHandler", name)}
		}
	case "oauth2", "openIdConnect":
		value, ok := credential.(string)
		if !ok {
			return configurationError("an access-token string")
		}
		if !validBearerToken(value) {
			return wireError(fmt.Sprintf("access token for %q is not an RFC 6750 b64token", name))
		}
		return func(request *http.Request, _ SecurityHandlerContext) error {
			request.Header.Set("Authorization", "Bearer "+value)
			return nil
		}, nil
	default:
		return nil, &ClientError{Kind: ErrorConfiguration, Code: "UNSUPPORTED_SECURITY_SCHEME", Message: fmt.Sprintf("security scheme %q requires a custom SecurityHandler", name)}
	}
}

func normalizeServerSelection(value any) any {
	selection, ok := value.(ServerSelection)
	if !ok {
		return value
	}
	result := map[string]any{}
	if selection.Index != nil {
		result["index"] = *selection.Index
	}
	if selection.URL != "" {
		result["url"] = selection.URL
	}
	if selection.BaseURL != "" {
		result["baseUrl"] = selection.BaseURL
	}
	if len(selection.Variables) > 0 {
		variables := map[string]any{}
		for name, member := range selection.Variables {
			variables[name] = member
		}
		result["variables"] = variables
	}
	return result
}

func declarationMatch(operation *openapi3.Operation, response *http.Response) DeclarationMatch {
	match := governingResponse(operation, response.StatusCode)
	if match == nil {
		return DeclarationMatch{}
	}
	result := DeclarationMatch{Declared: true, ResponseKey: match.key}
	if media, err := governingResponseMediaFor(match.response, response.Header.Get("Content-Type"), profileFullCoordinate); err == nil {
		result.MediaType = media.canonical
	}
	return result
}

func decodeFailure(body []byte, contentType string) any {
	if len(body) == 0 {
		return nil
	}
	if isJSONContentTypeFor(contentType, profileFullCoordinate) {
		var value any
		if json.Unmarshal(body, &value) == nil {
			return value
		}
		return append([]byte(nil), body...)
	}
	if value, err := decodeTextLaneFor(contentType, body, profileFullCoordinate); err == nil {
		return value
	}
	return append([]byte(nil), body...)
}

func nativeSuccessValue(value any, rawBoundary bool) any {
	if encoded, ok := value.(string); ok && rawBoundary {
		if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
			return decoded
		}
	}
	return value
}

func nativeResponseUsesRawBoundary(document *openapi3.T, operation *openapi3.Operation, response *http.Response) bool {
	if response == nil {
		return false
	}
	declaration := governingResponse(operation, response.StatusCode)
	if declaration == nil {
		return false
	}
	match, err := governingResponseMediaMatchFor(declaration.response, response.Header.Get("Content-Type"), profileFullCoordinate)
	return err == nil && responseUsesRawBoundary(document, match.media, response.Header.Get("Content-Type"), profileFullCoordinate, match.declared.rangeSpecificity == 2)
}

func responseBody(response *http.Response) []byte {
	if response == nil || response.Body == nil {
		return nil
	}
	defer response.Body.Close()
	value, _ := io.ReadAll(response.Body)
	return value
}

func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func sseMetadata(metadata Metadata) *SSEMetadata {
	result := &SSEMetadata{}
	if values := metadata["x-sse-event"]; len(values) > 0 {
		result.Event = values[0]
	}
	if values := metadata["x-sse-id"]; len(values) > 0 {
		result.ID = values[0]
	}
	if values := metadata["x-sse-retry"]; len(values) > 0 {
		var retry int
		if _, err := fmt.Sscanf(values[0], "%d", &retry); err == nil {
			result.Retry = &retry
		}
	}
	if result.Event == "" && result.ID == "" && result.Retry == nil {
		return nil
	}
	return result
}

func inputError(code, message string, cause error) error {
	return &ClientError{Kind: ErrorInput, Code: code, Message: message, Cause: cause}
}

func clientError(err error) error {
	if err == nil {
		return nil
	}
	var client *ClientError
	if errors.As(err, &client) {
		return client
	}
	var execution *ExecutionError
	if errors.As(err, &execution) {
		kind := ErrorInternal
		switch execution.Code {
		case CodeSourceLoadFailed:
			kind = ErrorSource
		case CodeInvalidRef, CodeRefNotFound:
			kind = ErrorOperation
		case CodeMissingInput, CodeValidationFailed:
			kind = ErrorInput
		case CodeSourceConfigError, CodeContextRequired:
			kind = ErrorConfiguration
		case CodeConnectFailed:
			kind = ErrorTransport
		case CodeProtocol:
			kind = ErrorProtocol
		case CodeResponseError, CodeStreamError:
			kind = ErrorResponse
		case CodeCancelled, CodeTimeout:
			kind = ErrorCancelled
		}
		return &ClientError{Kind: kind, Code: execution.Code, Message: execution.Message, Details: execution.Details, Cause: err}
	}
	return &ClientError{Kind: ErrorInternal, Code: "INTERNAL_ERROR", Message: err.Error(), Cause: err}
}
