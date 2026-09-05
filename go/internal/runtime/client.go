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
	operationID      string
	path             string
	method           Method
	additionalMethod string
	ref              string
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
	WireMethod  string
	Additional  bool
	OperationID string
	Summary     string
	Tags        []string
}

type Parameters struct {
	Path        map[string]any
	Query       map[string]any
	QueryString map[string]any
	Header      map[string]any
	Cookie      map[string]any
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
	Variables map[string]string
}

func ServerByIndex(index int, variables map[string]string) *ServerSelection {
	return &ServerSelection{Index: &index, Variables: variables}
}

func ServerURL(url string) *ServerSelection { return &ServerSelection{URL: url} }

type ClientOptions struct {
	// LoadHTTPClient retrieves the entry document and external references.
	// Invocation redirect policy is controlled independently by HTTPClient.
	LoadHTTPClient             *http.Client
	HTTPClient                 *http.Client
	Auth                       map[string]any
	Server                     *ServerSelection
	Headers                    http.Header
	MaxDeliveryUnitBytes       int64
	SecurityAlternative        *int
	ImplicitConnectionScope    ImplicitConnectionScope
	EmptyValueForm             Swagger20EmptyValueForm
	SecurityHandlers           map[string]SecurityHandler
	ParameterConverter         ParameterConverter
	RequestContentCodings      map[string]ContentEncoder
	ResponseContentCodings     map[string]ContentDecoder
	RequestCharacterEncodings  map[string]CharacterEncoder
	ResponseCharacterEncodings map[string]CharacterDecoder
}

type CallOptions struct {
	HTTPClient                 *http.Client
	Auth                       map[string]any
	Server                     *ServerSelection
	Headers                    http.Header
	MaxDeliveryUnitBytes       int64
	SecurityAlternative        *int
	ImplicitConnectionScope    ImplicitConnectionScope
	EmptyValueForm             Swagger20EmptyValueForm
	SecurityHandlers           map[string]SecurityHandler
	ParameterConverter         ParameterConverter
	RequestContentCodings      map[string]ContentEncoder
	ResponseContentCodings     map[string]ContentDecoder
	RequestCharacterEncodings  map[string]CharacterEncoder
	ResponseCharacterEncodings map[string]CharacterDecoder
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
	sequential  bool
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
	artifact  *Artifact
	document  *openapi3.T
	swagger20 *Swagger20Client
	edition   Edition
	floor     *acceptanceFloor
	source    Source
	options   ClientOptions
}

func Load(ctx context.Context, source Source, options ClientOptions) (*Client, error) {
	loadClient := options.LoadHTTPClient
	if loadClient == nil {
		loadClient = defaultHTTPClient()
	}
	if source.Artifact == nil && source.Document == nil {
		materialized, err := materializeClientSource(ctx, loadClient, source)
		if err != nil {
			return nil, &ClientError{Kind: ErrorSource, Code: "SOURCE_LOAD_FAILED", Message: err.Error(), Cause: err}
		}
		source = materialized
		swagger20Source, err := classifyClientSource(source.Content)
		if err != nil {
			return nil, &ClientError{Kind: ErrorSource, Code: "SOURCE_LOAD_FAILED", Message: err.Error(), Cause: err}
		}
		if swagger20Source {
			loadOptions := options
			loadOptions.HTTPClient = loadClient
			swagger20, loadErr := LoadSwagger20(ctx, Swagger20Source{Location: source.Location, Content: source.Content}, loadOptions)
			if loadErr != nil {
				return nil, loadErr
			}
			return &Client{
				swagger20: swagger20, edition: EditionSwagger20, source: source, options: options,
			}, nil
		}
	}
	artifact, floor, err := loadArtifact(ctx, loadClient, source, true)
	if err != nil {
		return nil, &ClientError{Kind: ErrorSource, Code: "SOURCE_LOAD_FAILED", Message: err.Error(), Cause: err}
	}
	source.Artifact = artifact
	source.Document = artifact.Document
	source.Content = nil
	return &Client{artifact: artifact, document: artifact.Document, edition: artifact.Edition, floor: floor, source: source, options: options}, nil
}

func materializeClientSource(ctx context.Context, client *http.Client, source Source) (Source, error) {
	if source.Content != nil {
		source.Content = append([]byte(nil), source.Content...)
		return source, nil
	}
	if source.Location == "" {
		return Source{}, fmt.Errorf("OpenAPI source requires location or content")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.Location, nil)
	if err != nil {
		return Source{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return Source{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Source{}, fmt.Errorf("load OpenAPI source %q: HTTP %d", source.Location, response.StatusCode)
	}
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return Source{}, err
	}
	location := source.Location
	if response.Request != nil && response.Request.URL != nil {
		location = response.Request.URL.String()
	}
	return Source{Location: location, Content: content}, nil
}

func classifyClientSource(content []byte) (bool, error) {
	root, err := parseSwagger20Resource(content)
	if err != nil {
		return false, err
	}
	object, ok := root.(map[string]any)
	if !ok {
		return false, fmt.Errorf("OpenAPI entry resource must be a JSON object")
	}
	_, hasSwagger := object["swagger"]
	if hasSwagger {
		if value, ok := object["swagger"].(string); !ok || value != "2.0" {
			return false, fmt.Errorf("unsupported Swagger version: expected exact string %q", "2.0")
		}
		// Exact `swagger: "2.0"` owns this source lane even if an `openapi`
		// member is also present. The co-present member has no authority to
		// redirect a Swagger artifact into an OAS 3.x loader.
		return true, nil
	}
	if _, hasOpenAPI := object["openapi"]; hasOpenAPI {
		_, ok := object["openapi"].(string)
		if !ok {
			return false, fmt.Errorf("OpenAPI entry resource openapi field must be a string")
		}
		if _, err := ClassifyOpenAPIEdition(content); err != nil {
			return false, err
		}
		return false, nil
	}
	return false, fmt.Errorf("entry resource has no swagger or openapi discriminator")
}

// Edition reports the exact artifact edition selected during Load.
func (c *Client) Edition() Edition {
	if c == nil {
		return ""
	}
	return c.edition
}

func (c *Client) Location() string {
	if c == nil {
		return ""
	}
	return c.source.Location
}

func (c *Client) Document() *openapi3.T { return c.document }
func (c *Client) Artifact() *Artifact   { return c.artifact }

func (c *Client) Operations() []OperationInfo {
	if c == nil {
		return nil
	}
	if c.swagger20 != nil {
		operations := c.swagger20.Operations()
		for index := range operations {
			operations[index].WireMethod = strings.ToUpper(string(operations[index].Method))
		}
		return operations
	}
	operations := enumerateOperationsWithFloor(c.artifact, c.floor)
	result := make([]OperationInfo, 0, len(operations))
	for index := range operations {
		if operations[index].unusable != nil {
			continue
		}
		result = append(result, operations[index].info)
	}
	return result
}

// ResolveOperationInfo resolves a native selector without dispatching. It is
// the shared basis of the public bound-operation convenience surface.
func (c *Client) ResolveOperationInfo(selector OperationSelector) (OperationInfo, error) {
	if c == nil {
		return OperationInfo{}, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	if c.swagger20 != nil {
		return resolveOperationInfo(c.Operations(), selector)
	}
	resolved, err := resolveOperation(c.artifact, c.floor, selector)
	if err != nil {
		return OperationInfo{}, err
	}
	if resolved.unusable != nil {
		return OperationInfo{}, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: resolved.unusable.Error(), Cause: resolved.unusable}
	}
	return resolved.info, nil
}

// SelectOperationInfo resolves only an operation's authored identity. It does
// not promote a target-level binding refusal into selection failure; invoking
// the returned identity still performs the complete target checks. This is the
// basis of the standalone facade's bound-operation convenience.
func (c *Client) SelectOperationInfo(selector OperationSelector) (OperationInfo, error) {
	if c == nil {
		return OperationInfo{}, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	if c.swagger20 != nil {
		return resolveOperationInfo(c.Operations(), selector)
	}
	if exclusion := c.artifact.SourceExclusion(); exclusion != nil {
		return OperationInfo{}, &ClientError{Kind: ErrorOperation, Code: "SOURCE_EXCLUDED", Message: exclusion.Error(), Cause: exclusion}
	}
	resolved, err := resolveOperation(c.artifact, c.floor, selector)
	if err == nil {
		return resolved.info, nil
	}
	if selector.ref == "" || !c.edition.IsOpenAPI32() || c.artifact.Refusal() == nil {
		return OperationInfo{}, err
	}
	reference, parseErr := parseOperationReference(selector.ref, c.edition)
	if parseErr != nil {
		var resolution *OperationResolutionError
		if !errors.As(parseErr, &resolution) || resolution.Kind != OperationTargetExcluded {
			return OperationInfo{}, err
		}
		reference, parseErr = parseExcludedAdditionalOperationReference(selector.ref)
		if parseErr != nil {
			return OperationInfo{}, err
		}
	}
	return OperationInfo{
		Ref: reference.Ref, Path: reference.Path, Method: Method(reference.Method),
		WireMethod: reference.WireMethod(), Additional: reference.Additional,
	}, nil
}

func parseExcludedAdditionalOperationReference(ref string) (OperationReference, error) {
	const prefix = "#/paths/"
	parts := strings.Split(strings.TrimPrefix(ref, prefix), "/")
	if !strings.HasPrefix(ref, prefix) || len(parts) != 3 || parts[1] != "additionalOperations" ||
		!wellFormedPointerToken(parts[0]) || !wellFormedPointerToken(parts[2]) {
		return OperationReference{}, operationResolutionError(OperationReferenceInvalid, "invalid additional-operation ref %q", ref)
	}
	return OperationReference{
		Ref: ref, Path: unescapeJSONPointerSegment(parts[0]),
		Method: unescapeJSONPointerSegment(parts[2]), Additional: true,
	}, nil
}

// Operations returns every addressable operation in the artifact in stable
// OpenAPI order. OpenAPI 3.2 enumeration includes the fixed QUERY field and
// case-exact additionalOperations entries; excluded targets are omitted.
//
// This artifact-level surface lets native adapters synthesize from the same
// edition-aware inventory the execution engine resolves, without exposing the
// private raw-resource overlay or request-body planning types.
func (a *Artifact) Operations() []OperationInfo {
	operations := enumerateOperationsWithFloor(a, nil)
	result := make([]OperationInfo, 0, len(operations))
	for index := range operations {
		if operations[index].unusable != nil {
			continue
		}
		result = append(result, operations[index].info)
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
	if streamResult.sequential || isSSEContentTypeFor(streamResult.Response.Header.Get("Content-Type"), profileFullCoordinate) {
		streamResult.Stream.Cancel()
		return nil, &ClientError{Kind: ErrorResponse, Code: "STREAMING_RESPONSE", Message: "operation returned sequential media; use Client.Stream"}
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
	if c == nil {
		return nil, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	if c.swagger20 != nil {
		return c.streamSwagger20(ctx, selector, input, options...)
	}
	if c.artifact != nil {
		if refusal := c.artifact.Refusal(); refusal != nil {
			return nil, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: refusal.Error(), Cause: refusal}
		}
	}
	resolved, err := resolveOperation(c.artifact, c.floor, selector)
	if err != nil {
		return nil, err
	}
	if resolved.unusable != nil {
		return nil, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: resolved.unusable.Error(), Cause: resolved.unusable}
	}
	if c.artifact != nil && c.artifact.Edition.IsOpenAPI32() && resolved.additional && resolved.info.WireMethod == http.MethodConnect {
		return nil, &ClientError{Kind: ErrorInput, Code: CodeRefused, Message: "CONNECT establishes a tunnel outside the OpenAPI invocation model"}
	}
	if methodIgnoresRequestBody(c.artifact.Edition, resolved.additional, string(resolved.info.Method)) {
		operation := *resolved.operation
		operation.RequestBody = nil
		resolved.operation = &operation
	}
	native, err := nativeInput(c.artifact, resolved.info.Ref, resolved.document, resolved.pathItem, resolved.operation, input)
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
	prepared, err := prepareArtifactWithFloor(c.artifact, nil, prepare)
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
	return &StreamResult{
		OK: true, Stream: &Stream{execution: execution}, Response: response, OpenAPI: declaration,
		rawBoundary: nativeResponseUsesRawBoundary(resolved.document, resolved.operation, response),
		sequential:  nativeOpenAPI32SequentialResponse(c.artifact, resolved.operation, response),
	}, nil
}

func (c *Client) streamSwagger20(ctx context.Context, selector OperationSelector, input Input, options ...CallOptions) (*StreamResult, error) {
	if len(options) > 1 {
		return nil, &ClientError{Kind: ErrorConfiguration, Code: "TOO_MANY_CALL_OPTIONS", Message: "Call accepts at most one CallOptions value"}
	}
	call := CallOptions{}
	if len(options) == 1 {
		call = options[0]
	}
	info, err := resolveOperationInfo(c.Operations(), selector)
	if err != nil {
		return nil, err
	}
	httpClient := call.HTTPClient
	if httpClient == nil {
		httpClient = c.options.HTTPClient
	}
	if httpClient == nil {
		httpClient = defaultInvocationHTTPClient()
	}
	headers := c.options.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	for name, values := range call.Headers {
		headers[name] = append([]string(nil), values...)
	}
	if len(headers) > 0 {
		httpClient = httpClientWithHeaders(httpClient, headers)
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
	maxBytes := call.MaxDeliveryUnitBytes
	if maxBytes == 0 {
		maxBytes = c.options.MaxDeliveryUnitBytes
	}
	auth := map[string]any{}
	for name, value := range c.options.Auth {
		auth[name] = value
	}
	for name, value := range call.Auth {
		auth[name] = value
	}
	credentials, err := swagger20ClientCredentials(c.swagger20.document, auth)
	if err != nil {
		return nil, err
	}
	requestCodings := call.RequestContentCodings
	if requestCodings == nil {
		requestCodings = c.options.RequestContentCodings
	}
	responseCodings := call.ResponseContentCodings
	if responseCodings == nil {
		responseCodings = c.options.ResponseContentCodings
	}
	requestCharacters := call.RequestCharacterEncodings
	if requestCharacters == nil {
		requestCharacters = c.options.RequestCharacterEncodings
	}
	responseCharacters := call.ResponseCharacterEncodings
	if responseCharacters == nil {
		responseCharacters = c.options.ResponseCharacterEncodings
	}
	converter := call.ParameterConverter
	if converter == nil {
		converter = c.options.ParameterConverter
	}
	prepare := Swagger20PrepareOptions{
		Source: Swagger20Source{Location: c.source.Location, Document: c.swagger20.document},
		Ref:    info.Ref, HTTPClient: httpClient, SecurityAlternative: securityAlternative,
		SecurityCredentials: credentials, RequestMedia: input.MediaType,
		PropertyMedia: input.PropertyMediaTypes, ParameterConverter: converter,
		EmptyValueForm: emptyValueForm, RequestContentCodings: requestCodings,
		ResponseContentCodings: responseCodings, RequestCharacterEncodings: requestCharacters,
		ResponseCharacterEncodings: responseCharacters, MaxDeliveryUnitBytes: maxBytes,
	}
	if server != nil {
		prepare.Server = server.URL
		prepare.ServerSchemeIndex = server.Index
	}
	prepared, err := NewEngine(httpClient).PrepareSwagger20(ctx, prepare)
	if err != nil {
		return nil, clientError(err)
	}
	parameters, err := prepared.Parameters()
	if err != nil {
		return nil, clientError(err)
	}
	native, supplied, err := swagger20ClientInput(parameters, input)
	if err != nil {
		return nil, err
	}
	execution, err := prepared.Start(ctx)
	if err != nil {
		return nil, clientError(err)
	}
	if supplied {
		if err := execution.Send(ctx, native); err != nil {
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
	declaration := DeclarationMatch{}
	if responses, responseErr := swagger20ResponsesFor(c.swagger20.document.graph, prepared.operation); responseErr == nil {
		if _, key, governingErr := responses.governing(c.swagger20.document.graph, response.StatusCode); governingErr == nil && key != "" {
			declaration.Declared = true
			declaration.ResponseKey = key
		}
	}
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
	return &StreamResult{OK: true, Stream: &Stream{execution: execution}, Response: response, OpenAPI: declaration}, nil
}

func resolveOperationInfo(operations []OperationInfo, selector OperationSelector) (OperationInfo, error) {
	if selector.operationID != "" {
		matches := make([]OperationInfo, 0, 1)
		for _, operation := range operations {
			if operation.OperationID == selector.operationID {
				matches = append(matches, operation)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return OperationInfo{}, &ClientError{Kind: ErrorOperation, Code: "DUPLICATE_OPERATION_ID", Message: fmt.Sprintf("operationId %q is not unique", selector.operationID)}
		}
		return OperationInfo{}, &ClientError{Kind: ErrorOperation, Code: "OPERATION_NOT_FOUND", Message: fmt.Sprintf("operationId %q was not found", selector.operationID)}
	}
	if selector.ref != "" {
		for _, operation := range operations {
			if operation.Ref == selector.ref {
				return operation, nil
			}
		}
		return OperationInfo{}, &ClientError{Kind: ErrorOperation, Code: "OPERATION_NOT_FOUND", Message: fmt.Sprintf("operation ref %q was not found", selector.ref)}
	}
	for _, operation := range operations {
		if operation.Path != selector.path {
			continue
		}
		if selector.additionalMethod != "" && operation.Additional && string(operation.Method) == selector.additionalMethod {
			return operation, nil
		}
		if selector.additionalMethod == "" && !operation.Additional && operation.Method == selector.method {
			return operation, nil
		}
	}
	return OperationInfo{}, &ClientError{Kind: ErrorOperation, Code: "OPERATION_NOT_FOUND", Message: fmt.Sprintf("%s %s was not found", strings.ToUpper(string(selector.method)), selector.path)}
}

func swagger20ClientCredentials(document *Swagger20Document, auth map[string]any) (Swagger20SecurityCredentials, error) {
	credentials := Swagger20SecurityCredentials{}
	definitions := document.root.object("securityDefinitions")
	for name, supplied := range auth {
		if !definitions.present || !definitions.valid {
			return Swagger20SecurityCredentials{}, &ClientError{Kind: ErrorConfiguration, Code: "UNKNOWN_SECURITY_SCHEME", Message: fmt.Sprintf("security scheme %q was not found", name)}
		}
		raw, present := definitions.value.member(name)
		definition, ok := raw.(map[string]any)
		if !present || !ok {
			return Swagger20SecurityCredentials{}, &ClientError{Kind: ErrorConfiguration, Code: "UNKNOWN_SECURITY_SCHEME", Message: fmt.Sprintf("security scheme %q was not found", name)}
		}
		typeName := swagger20Object(definition).string("type")
		if !typeName.valid {
			return Swagger20SecurityCredentials{}, &ClientError{Kind: ErrorConfiguration, Code: "INVALID_SECURITY_SCHEME", Message: fmt.Sprintf("security scheme %q has no string type", name)}
		}
		switch typeName.value {
		case "apiKey":
			value, ok := supplied.(string)
			if !ok {
				return Swagger20SecurityCredentials{}, &ClientError{Kind: ErrorConfiguration, Code: "INVALID_CREDENTIAL", Message: fmt.Sprintf("security scheme %q requires a string API key", name)}
			}
			if credentials.APIKeys == nil {
				credentials.APIKeys = map[string]string{}
			}
			credentials.APIKeys[name] = value
		case "basic":
			value, ok := supplied.(BasicCredential)
			if !ok {
				return Swagger20SecurityCredentials{}, &ClientError{Kind: ErrorConfiguration, Code: "INVALID_CREDENTIAL", Message: fmt.Sprintf("security scheme %q requires BasicCredential", name)}
			}
			if credentials.Basic == nil {
				credentials.Basic = map[string]Swagger20BasicCredential{}
			}
			credentials.Basic[name] = Swagger20BasicCredential{UserID: value.Username, Password: value.Password}
		case "oauth2":
			value, ok := supplied.(string)
			if !ok {
				return Swagger20SecurityCredentials{}, &ClientError{Kind: ErrorConfiguration, Code: "INVALID_CREDENTIAL", Message: fmt.Sprintf("security scheme %q requires an access-token string", name)}
			}
			if credentials.OAuth2 == nil {
				credentials.OAuth2 = map[string]Swagger20OAuth2Credential{}
			}
			credentials.OAuth2[name] = Swagger20OAuth2Credential{AccessToken: value}
		default:
			return Swagger20SecurityCredentials{}, &ClientError{Kind: ErrorConfiguration, Code: "UNSUPPORTED_SECURITY_SCHEME", Message: fmt.Sprintf("security scheme %q uses unsupported Swagger 2.0 type %q", name, typeName.value)}
		}
	}
	return credentials, nil
}

func swagger20ClientInput(parameters []Swagger20ParameterInfo, input Input) (Swagger20Input, bool, error) {
	if len(input.Parameters.Cookie) > 0 {
		return Swagger20Input{}, false, inputError("UNKNOWN_PARAMETER_LOCATION", "Swagger 2.0 has no cookie Parameter location", nil)
	}
	if len(input.Parameters.QueryString) > 0 {
		return Swagger20Input{}, false, inputError("UNKNOWN_PARAMETER_LOCATION", "Swagger 2.0 has no querystring Parameter location", nil)
	}
	result := Swagger20Input{Parameters: Swagger20Parameters{
		Path: input.Parameters.Path, Query: input.Parameters.Query, Header: input.Parameters.Header,
	}}
	supplied := len(input.Parameters.Path) > 0 || len(input.Parameters.Query) > 0 || len(input.Parameters.Header) > 0
	bodyPresent := input.BodyPresent || input.Body != nil
	if !bodyPresent {
		return result, supplied, nil
	}
	form := false
	for _, parameter := range parameters {
		if parameter.In == Swagger20ParameterFormData {
			form = true
			break
		}
	}
	if form {
		value, ok := toStringAnyMap(input.Body)
		if !ok {
			return Swagger20Input{}, false, inputError("OBJECT_BODY_REQUIRED", "Swagger 2.0 formData requires an object body", nil)
		}
		result.Parameters.FormData = value
	} else {
		result.Body = input.Body
		result.BodyPresent = true
	}
	return result, true, nil
}

type clientRoundTripper func(*http.Request) (*http.Response, error)

func (f clientRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func httpClientWithHeaders(client *http.Client, extra http.Header) *http.Client {
	clone := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = clientRoundTripper(func(request *http.Request) (*http.Response, error) {
		copy := request.Clone(request.Context())
		copy.Header = request.Header.Clone()
		for name, values := range extra {
			copy.Header.Del(name)
			for _, value := range values {
				copy.Header.Add(name, value)
			}
		}
		return base.RoundTrip(copy)
	})
	return &clone
}

func nativeOpenAPI32SequentialResponse(artifact *Artifact, operation *openapi3.Operation, response *http.Response) bool {
	if artifact == nil || !artifact.Edition.IsOpenAPI32() || response == nil {
		return false
	}
	governing := governingResponse(operation, response.StatusCode)
	if governing == nil {
		return false
	}
	matched, err := governingResponseMediaMatchFor(governing.response, response.Header.Get("Content-Type"), profileFullCoordinate)
	if err != nil {
		return false
	}
	kind, err := ClassifyOpenAPI32SequentialResponse(response.Header.Get("Content-Type"), matched.media)
	return err == nil && kind != ""
}

type resolvedOperation struct {
	info                     OperationInfo
	document                 *openapi3.T
	pathItem                 *openapi3.PathItem
	operation                *openapi3.Operation
	additional               bool
	unusable                 error
	referringSecuritySchemes openapi3.SecuritySchemes
}

func enumerateOperations(document *openapi3.T) []resolvedOperation {
	if document == nil {
		return nil
	}
	return enumerateOperationsWithFloor(&Artifact{Document: document, Edition: Edition(document.OpenAPI)}, nil)
}

// enumerateOperationsWithFloor applies the acceptance-floor inventory filter
// (§3.2 of `openbindings.openapi-3.1@1` and its siblings): a ladder-invalid target is not addressed and
// is not enumerated as invocable.
func enumerateOperationsWithFloor(artifact *Artifact, floor *acceptanceFloor) []resolvedOperation {
	if artifact == nil || artifact.Refusal() != nil || artifact.SourceExclusion() != nil {
		return nil
	}
	document := artifact.Document
	if document == nil {
		return nil
	}
	if artifact.Edition.IsOpenAPI32() {
		if artifact.openAPI32 == nil {
			return nil
		}
		result := []resolvedOperation{}
		for _, reference := range artifact.openAPI32.operationReferences() {
			if verdict := floor.opVerdict(reference.Ref); verdict != nil && verdict.Disposition == "invalid" {
				continue
			}
			target, err := artifact.ResolveOperation(reference.Ref)
			if err != nil {
				result = append(result, resolvedOperation{
					info: OperationInfo{
						Ref: reference.Ref, Path: reference.Path, Method: Method(reference.Method),
						WireMethod: reference.WireMethod(), Additional: reference.Additional,
					},
					unusable: err,
				})
				continue
			}
			result = append(result, resolvedOperation{
				document: target.Document, pathItem: target.PathItem, operation: target.Operation,
				additional: reference.Additional, referringSecuritySchemes: target.ReferringSecuritySchemes,
				info: OperationInfo{
					Ref: reference.Ref, Path: reference.Path, Method: Method(reference.Method),
					WireMethod: reference.WireMethod(), Additional: reference.Additional,
					OperationID: target.Operation.OperationID, Summary: target.Operation.Summary,
					Tags: append([]string(nil), target.Operation.Tags...),
				},
			})
		}
		return result
	}
	if document.Paths == nil {
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
			referringSecurity := artifact.referringSecurityByPath[path]
			if len(referringSecurity) == 0 {
				referringSecurity = artifact.referringSecuritySchemes(item)
			}
			result = append(result, resolvedOperation{document: document, pathItem: item, operation: operation, referringSecuritySchemes: referringSecurity, info: OperationInfo{
				Ref: "#/paths/" + escapeJSONPointerSegment(path) + "/" + method, Path: path, Method: Method(method),
				WireMethod: strings.ToUpper(method), Additional: false,
				OperationID: operation.OperationID, Summary: operation.Summary, Tags: append([]string(nil), operation.Tags...),
			}})
		}
	}
	return result
}

func resolveOperation(artifact *Artifact, floor *acceptanceFloor, selector OperationSelector) (resolvedOperation, error) {
	operations := enumerateOperationsWithFloor(artifact, floor)
	// Addressability and usability are intentionally separate. The public
	// operation list contains invocable targets, while selector resolution also
	// retains structurally declared targets that the binding accounts invalid.
	// The latter resolve successfully and refuse before dispatch.
	if floor != nil {
		for _, ref := range floor.OpOrder {
			op := floor.Ops[ref]
			if op == nil || op.Disposition != "invalid" {
				continue
			}
			operations = append(operations, resolvedOperation{
				info: OperationInfo{
					Ref: ref, Path: op.Path, Method: Method(op.Method), WireMethod: strings.ToUpper(op.Method),
				},
				unusable: invalidFloorOperationError(artifact, op),
			})
		}
	}
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
		// A well-formed inventory position can be addressable even when its
		// declaration is unusable. Preserve that distinction for callers: a
		// selected excluded target resolves and is then refused by Stream before
		// dispatch, while malformed selector syntax remains a resolution error.
		for _, operation := range operations {
			if operation.info.Ref != selector.ref || operation.unusable == nil {
				continue
			}
			var resolution *OperationResolutionError
			if errors.As(operation.unusable, &resolution) && resolution.Kind == OperationTargetExcluded {
				return operation, nil
			}
		}
		reference, err := parseOperationReference(selector.ref, artifact.Edition)
		if err != nil {
			return resolvedOperation{}, &ClientError{Kind: ErrorOperation, Code: "INVALID_OPERATION_REF", Message: err.Error(), Cause: err}
		}
		selector.path, selector.method = reference.Path, Method(reference.Method)
		if reference.Additional {
			selector.additionalMethod = reference.Method
		}
	}
	for _, operation := range operations {
		if selector.additionalMethod != "" {
			if operation.info.Path == selector.path && operation.info.Method == Method(selector.additionalMethod) && strings.Contains(operation.info.Ref, "/additionalOperations/") {
				return operation, nil
			}
			continue
		}
		if operation.info.Path == selector.path && operation.info.Method == Method(strings.ToLower(string(selector.method))) && !strings.Contains(operation.info.Ref, "/additionalOperations/") {
			return operation, nil
		}
	}
	return resolvedOperation{}, &ClientError{Kind: ErrorOperation, Code: "OPERATION_NOT_FOUND", Message: fmt.Sprintf("%s %s was not found", strings.ToUpper(string(selector.method)), selector.path)}
}

func invalidFloorOperationError(artifact *Artifact, op *floorOp) error {
	if artifact != nil {
		if err := artifact.operationErrors[op.Ref]; err != nil {
			return err
		}
	}
	if len(op.Defects) > 0 {
		return fmt.Errorf("selected operation %q is invalid at %s", op.Ref, op.Defects[0].Position)
	}
	return fmt.Errorf("selected operation %q is invalid under the governing OpenAPI edition", op.Ref)
}

type nativeInvocationInput struct {
	supplied           bool
	value              any
	mediaType          string
	propertyMediaTypes map[string]string
}

func nativeInput(artifact *Artifact, ref string, document *openapi3.T, pathItem *openapi3.PathItem, operation *openapi3.Operation, input Input) (nativeInvocationInput, error) {
	parameters := effectiveParameters(pathItem, operation)
	bodyPresent := input.BodyPresent || input.Body != nil
	bypassOpenAPI32Media := artifact != nil && artifact.Edition.IsOpenAPI32() && !bodyPresent &&
		(!hasRequestBody(operation) || !operation.RequestBody.Value.Required)
	var plans []*bodyPlan
	var err error
	if bypassOpenAPI32Media {
		plans = nil
	} else if artifact != nil && artifact.Edition.IsOpenAPI32() {
		reference, parseErr := ParseOperationReference(ref, artifact.Edition)
		if parseErr != nil {
			return nativeInvocationInput{}, inputError("BODY_UNSUPPORTED", parseErr.Error(), parseErr)
		}
		plans, err = planRequestBodiesForArtifact(artifact, &OperationTarget{OperationReference: reference, Document: document, PathItem: pathItem, Operation: operation}, profileFullCoordinate)
	} else {
		plans, err = planRequestBodiesFor(document, operation, profileFullCoordinate)
	}
	if err != nil {
		return nativeInvocationInput{}, inputError("BODY_UNSUPPORTED", err.Error(), err)
	}
	if input.MediaType == "" && hasRequestBody(operation) && operation.RequestBody.Value.Required && len(plans) > 0 && soleConcreteRequestPlan(operation, plans) == nil {
		durable := true
		return nativeInvocationInput{}, clientError(newContextRequiredError(
			"OpenAPI operation requires invocation context",
			&Prerequisites{Alternatives: []RequirementAlternative{{Requirements: []Requirement{newConfigValueRequirementCompat(
				"requestMedia", "", requestMediaRequirementDescription, nil, &durable,
			)}}}},
		))
	}
	selected := []*bodyPlan{}
	if bypassOpenAPI32Media {
		selected = nil
	} else if input.MediaType != "" {
		selected, err = configuredRequestPlansFor(document, operation, plans, map[string]any{"configuration": map[string]any{"requestMedia": input.MediaType}}, profileFullCoordinate)
		if err != nil {
			return nativeInvocationInput{}, inputError("REQUEST_MEDIA_NOT_DECLARED", err.Error(), err)
		}
		if len(selected) == 0 {
			return nativeInvocationInput{}, inputError("REQUEST_MEDIA_NOT_DECLARED", fmt.Sprintf("request media %q is not a supported declaration", input.MediaType), nil)
		}
	} else if plan := soleConcreteRequestPlan(operation, plans); plan != nil {
		selected = []*bodyPlan{plan}
	}
	routes := planAbstractInputRoutes(parameters, selected)
	value := map[string]any{}
	supplied := false
	locations := []struct {
		name   string
		values map[string]any
	}{{"path", input.Parameters.Path}, {"query", input.Parameters.Query}, {ParameterInQueryString, input.Parameters.QueryString}, {"header", input.Parameters.Header}, {"cookie", input.Parameters.Cookie}}
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
	mediaType := ""
	if !bypassOpenAPI32Media {
		mediaType = input.MediaType
		if mediaType == "" && len(selected) > 0 {
			mediaType = selected[0].mediaType
		}
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
	document := operation.document
	if document == nil {
		document = c.document
	}
	client := call.HTTPClient
	if client == nil {
		client = c.options.HTTPClient
	}
	if client == nil {
		client = defaultInvocationHTTPClient()
	}
	maxBytes := call.MaxDeliveryUnitBytes
	if maxBytes == 0 {
		maxBytes = c.options.MaxDeliveryUnitBytes
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
	securityAlternative := call.SecurityAlternative
	if securityAlternative == nil {
		securityAlternative = c.options.SecurityAlternative
	}
	if securityAlternative != nil {
		configuration["security"] = map[string]any{"index": *securityAlternative}
	}
	implicitScope := call.ImplicitConnectionScope
	if implicitScope == "" {
		implicitScope = c.options.ImplicitConnectionScope
	}
	if implicitScope != "" {
		if implicitScope != ImplicitConnectionEntry && implicitScope != ImplicitConnectionReferring {
			return PrepareOptions{}, &ClientError{Kind: ErrorConfiguration, Code: "INVALID_IMPLICIT_CONNECTION_SCOPE", Message: fmt.Sprintf("implicit connection scope %q must be entry or referring", implicitScope)}
		}
		configuration["implicitConnectionScope"] = string(implicitScope)
	}
	document = securityDocumentForScope(document, operation.referringSecuritySchemes, implicitScope)
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
		scheme, ok := securityScheme(document, name)
		if !ok {
			return PrepareOptions{}, &ClientError{Kind: ErrorConfiguration, Code: "UNKNOWN_SECURITY_SCHEME", Message: fmt.Sprintf("security scheme %q was not found", name)}
		}
		handler, err := credentialHandler(name, scheme, credential)
		if err != nil {
			var clientError *ClientError
			if errors.As(err, &clientError) && clientError.Code == "UNUSABLE_CREDENTIAL" {
				// A supplied Basic value outside the binding's selected
				// character repertoire is not a credential fragment. Leave the
				// alternative unsatisfied so normal context negotiation can ask
				// for a usable value without consuming input or dispatching.
				continue
			}
			return PrepareOptions{}, err
		}
		handlers[name] = handler
	}
	for name := range handlers {
		if _, ok := securityScheme(document, name); !ok {
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
	requestCharacters := call.RequestCharacterEncodings
	if requestCharacters == nil {
		requestCharacters = c.options.RequestCharacterEncodings
	}
	responseCharacters := call.ResponseCharacterEncodings
	if responseCharacters == nil {
		responseCharacters = c.options.ResponseCharacterEncodings
	}
	responseCodings, err := normalizeContentDecoders(responseCodings)
	if err != nil {
		return PrepareOptions{}, &ClientError{Kind: ErrorConfiguration, Code: "INVALID_RESPONSE_CONTENT_CODINGS", Message: err.Error(), Cause: err}
	}
	contextValue = contextWithSecurityHandlers(contextValue, handlers)
	return PrepareOptions{
		Source: c.source, Ref: operation.info.Ref, Profile: FullProfile(), Context: contextValue,
		HTTPClient: client, MaxDeliveryUnitBytes: maxBytes, SecurityHandlers: handlers,
		ParameterConverter: converter, RequestContentCodings: requestCodings, ResponseContentCodings: responseCodings,
		RequestCharacterEncodings: requestCharacters, ResponseCharacterEncodings: responseCharacters,
		// The published 3.0/3.1 binding is unary even for text/event-stream;
		// only 3.2's explicit sequential media constructs create a stream.
		BufferEventStreams: !c.edition.IsOpenAPI32(),
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
		if scheme.In == "header" && !validSerializedHeaderFieldValue(value) {
			return wireError(fmt.Sprintf("header credential %q is not a complete RFC 9110 field-value", name))
		}
		if scheme.In == "header" && strings.EqualFold(scheme.Name, "Cookie") && !validRFC6265CookieString(value) {
			return wireError(fmt.Sprintf("header credential %q is not an RFC 6265 cookie-string", name))
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
				return nil, &ClientError{Kind: ErrorConfiguration, Code: "UNUSABLE_CREDENTIAL", Message: fmt.Sprintf("basic credential %q violates RFC 7617 user-id or character-encoding constraints", name)}
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

func normalizeServerSelection(selection *ServerSelection) any {
	if selection == nil {
		return nil
	}
	result := map[string]any{}
	if selection.Index != nil {
		result["index"] = *selection.Index
	}
	if selection.URL != "" {
		result["baseUrl"] = selection.URL
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
		// Failure bodies ride the same lanes as success bodies, so they parse
		// under the same §9.2 profile; what the profile will not decode stays
		// opaque application-authored bytes rather than silently altered text.
		if parseStrictJSON(body, &value) == nil {
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
	contentType := response.Header.Get("Content-Type")
	if contentType == "" && document != nil && document.OpenAPI == string(EditionOpenAPI320) {
		// A 3.2 non-empty response without Content-Type is governed as
		// application/octet-stream. This predicate is consulted only for an
		// emitted value; an empty response emits nothing regardless.
		contentType = "application/octet-stream"
	}
	declaration := governingResponse(operation, response.StatusCode)
	if declaration == nil {
		return false
	}
	match, err := governingResponseMediaMatchFor(declaration.response, contentType, profileFullCoordinate)
	return err == nil && responseUsesRawBoundary(document, match.media, contentType, profileFullCoordinate, match.declared.rangeSpecificity == 2)
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
		case CodeRefused, CodeMissingInput, CodeValidationFailed, CodeInputClosed, CodeInvocationClosed:
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
