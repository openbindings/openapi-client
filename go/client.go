package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	runtime "github.com/openbindings/openapi-client/go/internal/runtime"
)

// Edition is the exact artifact version selected during loading.
type Edition string

const (
	Swagger20  Edition = "2.0"
	OpenAPI300 Edition = "3.0.0"
	OpenAPI301 Edition = "3.0.1"
	OpenAPI302 Edition = "3.0.2"
	OpenAPI303 Edition = "3.0.3"
	OpenAPI304 Edition = "3.0.4"
	OpenAPI310 Edition = "3.1.0"
	OpenAPI311 Edition = "3.1.1"
	OpenAPI312 Edition = "3.1.2"
	OpenAPI320 Edition = "3.2.0"
)

// Source identifies one OpenAPI artifact. Content, when present, is JSON or
// YAML; Location supplies its reference base and is retrieved when Content is
// absent.
type Source struct {
	Location string
	Content  []byte
}

// FromURL creates a location-only source.
func FromURL(location string) Source { return Source{Location: location} }

// FromBytes creates a content source. Load snapshots the bytes.
func FromBytes(content []byte) Source { return Source{Content: content} }

// FromText creates a UTF-8 JSON or YAML content source.
func FromText(content string) Source { return Source{Content: []byte(content)} }

// Method is an authored OpenAPI fixed-operation field.
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
	QUERY   Method = "query"
)

// OperationSelector selects an operation without exposing parser internals.
type OperationSelector struct{ value runtime.OperationSelector }

// OperationID selects the unique operation whose operationId equals value.
func OperationID(value string) OperationSelector {
	return OperationSelector{value: runtime.OperationID(value)}
}

// OperationRef selects an operation by its canonical local OpenAPI reference.
func OperationRef(value string) OperationSelector {
	return OperationSelector{value: runtime.OperationRef(value)}
}

// PathOperation selects a fixed operation field at path.
func PathOperation(path string, method Method) OperationSelector {
	return OperationSelector{value: runtime.PathOperation(path, runtime.Method(method))}
}

// AdditionalOperation selects a case-sensitive OAS 3.2 additionalOperations
// method token.
func AdditionalOperation(path, method string) OperationSelector {
	return OperationSelector{value: runtime.AdditionalOperation(path, method)}
}

// OperationInfo is immutable caller-facing operation metadata.
type OperationInfo struct {
	Ref         string
	Path        string
	Method      string
	WireMethod  string
	Additional  bool
	OperationID string
	Summary     string
	Tags        []string
}

// Parameters keeps OpenAPI wire locations distinct even when their names are
// equal. QueryString is the OAS 3.2 whole-query-component location.
type Parameters struct {
	Path        map[string]any
	Query       map[string]any
	QueryString map[string]any
	Header      map[string]any
	Cookie      map[string]any
}

// Input is one native application input. BodyPresent distinguishes an
// explicit JSON null from omission; every non-nil Body is present regardless.
type Input struct {
	Parameters         Parameters
	Body               any
	BodyPresent        bool
	MediaType          string
	PropertyMediaTypes map[string]string
}

// BasicCredential is a scheme-named HTTP Basic credential.
type BasicCredential struct {
	Username string
	Password string
}

// SecuritySchemeInfo is a detached, read-only view of one authored security
// scheme. JSON is a snapshot, not a parser-owned model.
type SecuritySchemeInfo struct {
	Type             string
	Scheme           string
	Name             string
	In               string
	BearerFormat     string
	OpenIDConnectURL string
	Description      string
	JSON             []byte
}

// SecurityHandler owns one artifact-authored scheme the built-ins do not.
type SecurityHandler func(*http.Request, SecurityHandlerContext) error

// SecurityHandlerContext describes the scheme and operation for a custom
// security handler. Its values are detached from parser-owned state.
type SecurityHandlerContext struct {
	SchemeName string
	Scheme     SecuritySchemeInfo
	Operation  OperationInfo
}

type credentialKind uint8

const (
	credentialToken credentialKind = iota + 1
	credentialBasic
	credentialHandler
)

// Credential is a typed scheme-named credential value.
type Credential struct {
	kind    credentialKind
	token   string
	basic   BasicCredential
	handler SecurityHandler
}

// Token supplies the string credential consumed by an API-key, Bearer,
// OAuth 2, or OpenID Connect scheme according to the artifact declaration.
func Token(value string) Credential { return Credential{kind: credentialToken, token: value} }

// Basic supplies a Basic credential without exposing a polymorphic any value.
func Basic(username, password string) Credential {
	return Credential{kind: credentialBasic, basic: BasicCredential{Username: username, Password: password}}
}

// CustomSecurity installs the native handler that satisfies and applies one
// otherwise unsupported authored security scheme.
func CustomSecurity(handler SecurityHandler) Credential {
	return Credential{kind: credentialHandler, handler: handler}
}

// Credentials are keyed by names authored in securityDefinitions or
// components.securitySchemes.
type Credentials map[string]Credential

// ServerSelection is constructed by Server or ServerURL so invalid mixed
// selection shapes cannot be expressed.
type ServerSelection interface {
	runtimeServerSelection() *runtime.ServerSelection
}

type serverIndexSelection struct {
	index     int
	variables map[string]string
}

func (s serverIndexSelection) runtimeServerSelection() *runtime.ServerSelection {
	return runtime.ServerByIndex(s.index, cloneStrings(s.variables))
}

type serverVariablesSelection map[string]string

func (s serverVariablesSelection) runtimeServerSelection() *runtime.ServerSelection {
	return &runtime.ServerSelection{Variables: cloneStrings(s)}
}

type serverURLSelection string

func (s serverURLSelection) runtimeServerSelection() *runtime.ServerSelection {
	return runtime.ServerURL(string(s))
}

// Server selects one zero-based authored effective server and its variables.
func Server(index int, variables map[string]string) ServerSelection {
	return serverIndexSelection{index: index, variables: cloneStrings(variables)}
}

// ServerVariables supplies variables for the sole or default effective server
// without selecting one by index.
func ServerVariables(variables map[string]string) ServerSelection {
	return serverVariablesSelection(cloneStrings(variables))
}

// ServerURL replaces the artifact-derived base with one complete URL.
func ServerURL(value string) ServerSelection { return serverURLSelection(value) }

// RedirectPolicy controls invocation redirect handling. The zero value
// defaults to RedirectManual.
type RedirectPolicy string

const (
	RedirectManual RedirectPolicy = "manual"
	RedirectFollow RedirectPolicy = "follow"
)

// EmptyValueForm selects the Swagger 2.0 wire spelling of an empty value when
// the artifact leaves that choice open.
type EmptyValueForm string

const (
	EmptyValueNameOnly EmptyValueForm = "name-only"
	EmptyValueEmpty    EmptyValueForm = "empty"
)

// ImplicitConnectionScope selects which document owns an unqualified OpenAPI
// 3.0 security-scheme name across an external reference boundary.
type ImplicitConnectionScope string

const (
	ConnectionEntry     ImplicitConnectionScope = "entry"
	ConnectionReferring ImplicitConnectionScope = "referring"
)

// ParameterConverter converts an application boolean or number when the
// binding requires the host to choose its exact string representation.
type ParameterConverter func(any) (string, error)

// ContentEncoder applies one named request Content-Encoding coding.
type ContentEncoder func([]byte) ([]byte, error)

// ContentDecoder removes one named response Content-Encoding coding.
type ContentDecoder func([]byte) ([]byte, error)

// CharacterEncoder encodes request character data for one named charset.
type CharacterEncoder func(string) ([]byte, error)

// CharacterDecoder decodes response character data for one named charset.
type CharacterDecoder func([]byte) (string, error)

// Options are immutable defaults for a loaded client.
type Options struct {
	// DocumentHTTPClient retrieves the entry artifact and external references.
	// Its redirect policy is independent from invocation redirects.
	DocumentHTTPClient *http.Client
	// HTTPClient dispatches API operations. A nil value uses the package's
	// default invocation client.
	HTTPClient *http.Client
	// Redirect defaults to RedirectManual.
	Redirect RedirectPolicy
	// Auth contains credentials keyed by authored security-scheme name.
	Auth Credentials
	// Server selects an authored server or supplies a complete replacement URL.
	Server ServerSelection
	// Headers are caller-owned ordinary defaults merged into each request.
	Headers http.Header
	// MaxDeliveryUnitBytes bounds each decoded value; zero uses the package
	// default of 10 MiB.
	MaxDeliveryUnitBytes int64
	// SecurityAlternative selects one zero-based authored OR alternative.
	SecurityAlternative     *int
	ImplicitConnectionScope ImplicitConnectionScope
	EmptyValueForm          EmptyValueForm
	ParameterConverter      ParameterConverter
	// Coding and character maps are keyed case-insensitively by coding or
	// charset name after package normalization.
	RequestContentCodings      map[string]ContentEncoder
	ResponseContentCodings     map[string]ContentDecoder
	RequestCharacterEncodings  map[string]CharacterEncoder
	ResponseCharacterEncodings map[string]CharacterDecoder
}

// CallOptions override client defaults for one invocation. An empty Redirect
// inherits the client policy.
type CallOptions struct {
	// HTTPClient overrides the invocation client for this call.
	HTTPClient *http.Client
	Redirect   RedirectPolicy
	// Auth overlays client credentials by authored scheme name.
	Auth   Credentials
	Server ServerSelection
	// Headers replace same-named client defaults and add new ordinary fields.
	Headers                    http.Header
	MaxDeliveryUnitBytes       int64
	SecurityAlternative        *int
	ImplicitConnectionScope    ImplicitConnectionScope
	EmptyValueForm             EmptyValueForm
	ParameterConverter         ParameterConverter
	RequestContentCodings      map[string]ContentEncoder
	ResponseContentCodings     map[string]ContentDecoder
	RequestCharacterEncodings  map[string]CharacterEncoder
	ResponseCharacterEncodings map[string]CharacterDecoder
}

// DeclarationMatch identifies the response declaration that governed an HTTP
// outcome. Declared is false when no Response Object governed the status.
type DeclarationMatch struct {
	Declared    bool
	ResponseKey string
	MediaType   string
}

// Result is an HTTP application outcome. Non-2xx outcomes return OK=false;
// local, transport, and protocol failures return an error from Call.
type Result struct {
	OK       bool
	Data     any
	Error    any
	Response *http.Response
	OpenAPI  DeclarationMatch
}

// SSEMetadata retains Server-Sent Events framing metadata when present.
type SSEMetadata struct {
	Event string
	ID    string
	Retry *int
}

// StreamEvent is one application value in response order.
type StreamEvent struct {
	Data any
	SSE  *SSEMetadata
}

// Stream is the sole consumer of a successful streaming response body.
type Stream struct{ inner *runtime.Stream }

// Next returns the next event. open is false after terminal completion; callers
// should then call Wait to distinguish clean completion from terminal failure.
func (s *Stream) Next(ctx context.Context) (StreamEvent, bool, error) {
	if s == nil || s.inner == nil {
		return StreamEvent{}, false, nilClientError("OpenAPI stream is nil")
	}
	value, ok, err := s.inner.Next(ctx)
	if err != nil {
		return StreamEvent{}, false, clientError(err)
	}
	return StreamEvent{Data: value.Data, SSE: copySSE(value.SSE)}, ok, nil
}

// Cancel stops response consumption and the underlying request.
func (s *Stream) Cancel() {
	if s != nil && s.inner != nil {
		s.inner.Cancel()
	}
}

// Done closes after the stream reaches a terminal outcome.
func (s *Stream) Done() <-chan struct{} {
	if s == nil || s.inner == nil {
		return alreadyDone
	}
	return s.inner.Done()
}

// Wait blocks for and returns the stream's terminal outcome.
func (s *Stream) Wait() error {
	if s == nil || s.inner == nil {
		return nilClientError("OpenAPI stream is nil")
	}
	return clientError(s.inner.Wait())
}

var alreadyDone = func() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}()

// StreamResult is either a successful stream or an HTTP application failure.
// On success Response contains metadata only and Stream owns the body. On an
// HTTP failure Response.Body is a bounded replay.
type StreamResult struct {
	OK       bool
	Stream   *Stream
	Error    any
	Response *http.Response
	OpenAPI  DeclarationMatch
}

// ErrorKind is the stable coarse category of a ClientError.
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

// CodeConfigurationRequired identifies an actionable missing artifact choice.
const CodeConfigurationRequired = "CONFIGURATION_REQUIRED"

// ConfigurationRequirementKind identifies which public surface supplies a
// missing requirement.
type ConfigurationRequirementKind string

const (
	RequirementOption     ConfigurationRequirementKind = "option"
	RequirementInput      ConfigurationRequirementKind = "input"
	RequirementCredential ConfigurationRequirementKind = "credential"
)

// ConfigurationRequirement describes one missing native input, option, or
// credential. Name is an Input field for Kind=input, an Options/CallOptions
// field for Kind=option, and an authored security-scheme name for
// Kind=credential.
type ConfigurationRequirement struct {
	Kind          ConfigurationRequirementKind
	Name          string
	Path          string
	Credential    string
	AllowedValues []any
	Description   string
}

// ConfigurationAlternative is conjunctive: every requirement must be
// supplied. ConfigurationRequirements.Alternatives is disjunctive.
type ConfigurationAlternative struct {
	Requirements []ConfigurationRequirement
}

// ConfigurationRequirements describes disjunctive complete remedies for one
// target. Supplying every member of any one alternative is sufficient.
type ConfigurationRequirements struct {
	Target       string
	Alternatives []ConfigurationAlternative
}

// ClientError is a typed non-HTTP failure.
type ClientError struct {
	Kind         ErrorKind
	Code         string
	Message      string
	Details      any
	Requirements *ConfigurationRequirements
	Cause        error
}

// Error returns the diagnostic message, falling back to Code.
func (e *ClientError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

// Unwrap returns the underlying private or host failure, when one exists.
func (e *ClientError) Unwrap() error { return e.Cause }

// Client is an immutable loaded OpenAPI artifact plus native defaults.
type Client struct {
	inner   *runtime.Client
	options Options
}

// Load retrieves or parses source, resolves its edition, snapshots mutable
// caller input, and returns an immutable client.
func Load(ctx context.Context, source Source, options Options) (*Client, error) {
	if err := validRedirect(options.Redirect, true); err != nil {
		return nil, err
	}
	options = snapshotOptions(options)
	inner, err := runtime.Load(ctx, runtime.Source{
		Location: source.Location,
		Content:  append([]byte(nil), source.Content...),
	}, runtimeClientOptions(options))
	if err != nil {
		return nil, clientError(err)
	}
	return &Client{inner: inner, options: options}, nil
}

// Edition returns the exact supported version selected during loading.
func (c *Client) Edition() Edition {
	if c == nil || c.inner == nil {
		return ""
	}
	return Edition(c.inner.Edition())
}

// Location is the resolved entry-document location, when the source had one.
func (c *Client) Location() string {
	if c == nil || c.inner == nil {
		return ""
	}
	return c.inner.Location()
}

// Operations returns a stable detached inventory of addressable operations.
func (c *Client) Operations() []OperationInfo {
	if c == nil || c.inner == nil {
		return nil
	}
	operations := c.inner.Operations()
	result := make([]OperationInfo, len(operations))
	for index := range operations {
		result[index] = operationInfo(operations[index])
	}
	return result
}

// Operation binds a selector once for repeated calls.
func (c *Client) Operation(selector OperationSelector) (*Operation, error) {
	if c == nil || c.inner == nil {
		return nil, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	info, err := c.inner.SelectOperationInfo(selector.value)
	if err != nil {
		return nil, clientError(err)
	}
	return &Operation{client: c, selector: selector, info: operationInfo(info)}, nil
}

// Call invokes selector and returns one unary HTTP application outcome.
func (c *Client) Call(ctx context.Context, selector OperationSelector, input Input, options ...CallOptions) (*Result, error) {
	if c == nil || c.inner == nil {
		return nil, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	call, err := oneCallOptions(options)
	if err != nil {
		return nil, err
	}
	result, err := c.inner.Call(ctx, selector.value, runtimeInput(input), runtimeCallOptions(c.options, call))
	if err != nil {
		return nil, clientError(err)
	}
	return resultValue(result), nil
}

// Stream invokes selector and returns an ordered response stream or an HTTP
// application failure.
func (c *Client) Stream(ctx context.Context, selector OperationSelector, input Input, options ...CallOptions) (*StreamResult, error) {
	if c == nil || c.inner == nil {
		return nil, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	call, err := oneCallOptions(options)
	if err != nil {
		return nil, err
	}
	result, err := c.inner.Stream(ctx, selector.value, runtimeInput(input), runtimeCallOptions(c.options, call))
	if err != nil {
		return nil, clientError(err)
	}
	return streamResultValue(result), nil
}

// Operation is a selector resolved against one loaded client.
type Operation struct {
	client   *Client
	selector OperationSelector
	info     OperationInfo
}

// Info returns detached metadata for the bound operation.
func (o *Operation) Info() OperationInfo {
	if o == nil {
		return OperationInfo{}
	}
	return cloneOperationInfo(o.info)
}

// Call invokes the bound operation as a unary exchange.
func (o *Operation) Call(ctx context.Context, input Input, options ...CallOptions) (*Result, error) {
	if o == nil || o.client == nil {
		return nil, nilClientError("OpenAPI operation is nil")
	}
	return o.client.Call(ctx, o.selector, input, options...)
}

// Stream invokes the bound operation as an ordered response stream.
func (o *Operation) Stream(ctx context.Context, input Input, options ...CallOptions) (*StreamResult, error) {
	if o == nil || o.client == nil {
		return nil, nilClientError("OpenAPI operation is nil")
	}
	return o.client.Stream(ctx, o.selector, input, options...)
}

func nilClientError(message string) *ClientError {
	return &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: message}
}

func oneCallOptions(options []CallOptions) (CallOptions, error) {
	if len(options) > 1 {
		return CallOptions{}, &ClientError{Kind: ErrorConfiguration, Code: "TOO_MANY_CALL_OPTIONS", Message: "Call accepts at most one CallOptions value"}
	}
	if len(options) == 0 {
		return CallOptions{}, nil
	}
	if err := validRedirect(options[0].Redirect, false); err != nil {
		return CallOptions{}, err
	}
	return options[0], nil
}

func validRedirect(policy RedirectPolicy, client bool) error {
	if policy == "" || policy == RedirectManual || policy == RedirectFollow {
		return nil
	}
	owner := "call"
	if client {
		owner = "client"
	}
	return &ClientError{Kind: ErrorConfiguration, Code: "INVALID_REDIRECT_POLICY", Message: fmt.Sprintf("%s redirect policy %q is invalid", owner, policy)}
}

func runtimeClientOptions(options Options) runtime.ClientOptions {
	auth, handlers := runtimeCredentials(options.Auth)
	return runtime.ClientOptions{
		LoadHTTPClient:             options.DocumentHTTPClient,
		HTTPClient:                 redirectClient(options.HTTPClient, normalizedRedirect(options.Redirect)),
		Auth:                       auth,
		Server:                     runtimeServer(options.Server),
		Headers:                    options.Headers.Clone(),
		MaxDeliveryUnitBytes:       options.MaxDeliveryUnitBytes,
		SecurityAlternative:        cloneOptionalInt(options.SecurityAlternative),
		ImplicitConnectionScope:    runtime.ImplicitConnectionScope(options.ImplicitConnectionScope),
		EmptyValueForm:             runtime.Swagger20EmptyValueForm(options.EmptyValueForm),
		SecurityHandlers:           handlers,
		ParameterConverter:         runtime.ParameterConverter(options.ParameterConverter),
		RequestContentCodings:      contentEncoders(options.RequestContentCodings),
		ResponseContentCodings:     contentDecoders(options.ResponseContentCodings),
		RequestCharacterEncodings:  characterEncoders(options.RequestCharacterEncodings),
		ResponseCharacterEncodings: characterDecoders(options.ResponseCharacterEncodings),
	}
}

func runtimeCallOptions(client Options, call CallOptions) runtime.CallOptions {
	auth, handlers := runtimeCredentials(call.Auth)
	var httpClient *http.Client
	if call.HTTPClient != nil || call.Redirect != "" {
		base := call.HTTPClient
		if base == nil {
			base = client.HTTPClient
		}
		policy := call.Redirect
		if policy == "" {
			policy = normalizedRedirect(client.Redirect)
		}
		httpClient = redirectClient(base, policy)
	}
	return runtime.CallOptions{
		HTTPClient:                 httpClient,
		Auth:                       auth,
		Server:                     runtimeServer(call.Server),
		Headers:                    call.Headers.Clone(),
		MaxDeliveryUnitBytes:       call.MaxDeliveryUnitBytes,
		SecurityAlternative:        cloneOptionalInt(call.SecurityAlternative),
		ImplicitConnectionScope:    runtime.ImplicitConnectionScope(call.ImplicitConnectionScope),
		EmptyValueForm:             runtime.Swagger20EmptyValueForm(call.EmptyValueForm),
		SecurityHandlers:           handlers,
		ParameterConverter:         runtime.ParameterConverter(call.ParameterConverter),
		RequestContentCodings:      contentEncoders(call.RequestContentCodings),
		ResponseContentCodings:     contentDecoders(call.ResponseContentCodings),
		RequestCharacterEncodings:  characterEncoders(call.RequestCharacterEncodings),
		ResponseCharacterEncodings: characterDecoders(call.ResponseCharacterEncodings),
	}
}

func runtimeCredentials(values Credentials) (map[string]any, map[string]runtime.SecurityHandler) {
	auth := map[string]any{}
	handlers := map[string]runtime.SecurityHandler{}
	for name, value := range values {
		switch value.kind {
		case credentialToken:
			auth[name] = value.token
		case credentialBasic:
			auth[name] = runtime.BasicCredential{Username: value.basic.Username, Password: value.basic.Password}
		case credentialHandler:
			if value.handler != nil {
				handler := value.handler
				handlers[name] = func(request *http.Request, context runtime.SecurityHandlerContext) error {
					return handler(request, SecurityHandlerContext{
						SchemeName: context.SchemeName,
						Scheme:     securitySchemeInfo(context.Scheme),
						Operation:  operationInfo(context.Operation),
					})
				}
			}
		}
	}
	return auth, handlers
}

func securitySchemeInfo(value any) SecuritySchemeInfo {
	data, _ := json.Marshal(value)
	fields := struct {
		Type             string `json:"type"`
		Scheme           string `json:"scheme"`
		Name             string `json:"name"`
		In               string `json:"in"`
		BearerFormat     string `json:"bearerFormat"`
		OpenIDConnectURL string `json:"openIdConnectUrl"`
		Description      string `json:"description"`
	}{}
	_ = json.Unmarshal(data, &fields)
	return SecuritySchemeInfo{
		Type: fields.Type, Scheme: fields.Scheme, Name: fields.Name, In: fields.In,
		BearerFormat: fields.BearerFormat, OpenIDConnectURL: fields.OpenIDConnectURL,
		Description: fields.Description, JSON: append([]byte(nil), data...),
	}
}

func runtimeInput(input Input) runtime.Input {
	return runtime.Input{
		Parameters: runtime.Parameters{
			Path: input.Parameters.Path, Query: input.Parameters.Query,
			QueryString: input.Parameters.QueryString, Header: input.Parameters.Header,
			Cookie: input.Parameters.Cookie,
		},
		Body: input.Body, BodyPresent: input.BodyPresent, MediaType: input.MediaType,
		PropertyMediaTypes: input.PropertyMediaTypes,
	}
}

func runtimeServer(selection ServerSelection) *runtime.ServerSelection {
	if selection == nil {
		return nil
	}
	return selection.runtimeServerSelection()
}

func normalizedRedirect(policy RedirectPolicy) RedirectPolicy {
	if policy == "" {
		return RedirectManual
	}
	return policy
}

func redirectClient(client *http.Client, policy RedirectPolicy) *http.Client {
	if client == nil && policy == RedirectManual {
		return nil
	}
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	if policy == RedirectManual {
		clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return &clone
}

func resultValue(value *runtime.Result) *Result {
	if value == nil {
		return nil
	}
	return &Result{OK: value.OK, Data: value.Data, Error: value.Error, Response: value.Response, OpenAPI: declaration(value.OpenAPI)}
}

func streamResultValue(value *runtime.StreamResult) *StreamResult {
	if value == nil {
		return nil
	}
	var stream *Stream
	if value.Stream != nil {
		stream = &Stream{inner: value.Stream}
	}
	response := value.Response
	if value.OK {
		response = responseMetadata(value.Response)
	}
	return &StreamResult{OK: value.OK, Stream: stream, Error: value.Error, Response: response, OpenAPI: declaration(value.OpenAPI)}
}

func responseMetadata(response *http.Response) *http.Response {
	if response == nil {
		return nil
	}
	clone := new(http.Response)
	*clone = *response
	clone.Header = response.Header.Clone()
	clone.Trailer = response.Trailer.Clone()
	clone.Body = nil
	if response.Request != nil {
		clone.Request = response.Request.Clone(response.Request.Context())
	}
	return clone
}

func declaration(value runtime.DeclarationMatch) DeclarationMatch {
	return DeclarationMatch{Declared: value.Declared, ResponseKey: value.ResponseKey, MediaType: value.MediaType}
}

func operationInfo(value runtime.OperationInfo) OperationInfo {
	return OperationInfo{
		Ref: value.Ref, Path: value.Path, Method: string(value.Method), WireMethod: value.WireMethod,
		Additional: value.Additional, OperationID: value.OperationID, Summary: value.Summary,
		Tags: append([]string(nil), value.Tags...),
	}
}

func cloneOperationInfo(value OperationInfo) OperationInfo {
	value.Tags = append([]string(nil), value.Tags...)
	return value
}

func copySSE(value *runtime.SSEMetadata) *SSEMetadata {
	if value == nil {
		return nil
	}
	result := &SSEMetadata{Event: value.Event, ID: value.ID}
	if value.Retry != nil {
		retry := *value.Retry
		result.Retry = &retry
	}
	return result
}

func clientError(err error) error {
	if err == nil {
		return nil
	}
	var value *runtime.ClientError
	if errors.As(err, &value) {
		code := value.Code
		details := value.Details
		var requirements *ConfigurationRequirements
		if code == "CONTEXT_REQUIRED" {
			code = CodeConfigurationRequired
			requirements = configurationRequirements(value.Details)
			details = requirements
		}
		return &ClientError{
			Kind: ErrorKind(value.Kind), Code: code, Message: value.Message,
			Details: details, Requirements: requirements, Cause: value.Cause,
		}
	}
	// Keep the private runtime behind the public error contract even if a new
	// internal path has not yet classified its failure. Callers should never
	// need to know which implementation layer produced an error.
	return &ClientError{Kind: ErrorInternal, Code: "INTERNAL_ERROR", Message: err.Error(), Cause: err}
}

func configurationRequirements(value any) *ConfigurationRequirements {
	internal, ok := value.(*runtime.Prerequisites)
	if !ok || internal == nil {
		if concrete, concreteOK := value.(runtime.Prerequisites); concreteOK {
			internal = &concrete
		} else {
			return nil
		}
	}
	result := &ConfigurationRequirements{Target: internal.Target, Alternatives: make([]ConfigurationAlternative, len(internal.Alternatives))}
	for alternativeIndex, alternative := range internal.Alternatives {
		requirements := make([]ConfigurationRequirement, len(alternative.Requirements))
		for requirementIndex, requirement := range alternative.Requirements {
			native := ConfigurationRequirement{Description: requirement.Description}
			if requirement.Type == "config.value" {
				point, _ := requirement.Extra["point"].(string)
				native.Kind, native.Name = nativeConfigurationPoint(point)
				if native.Kind == "" {
					return nil
				}
				native.Path, _ = requirement.Extra["path"].(string)
				if schema, ok := requirement.Extra["schema"].(map[string]any); ok {
					if allowed, ok := schema["enum"].([]any); ok {
						native.AllowedValues = cloneJSONValues(allowed)
					}
				}
			} else {
				native.Kind = RequirementCredential
				native.Name = requirement.Name
				native.Credential = strings.TrimPrefix(requirement.Type, "auth.")
			}
			requirements[requirementIndex] = native
		}
		result.Alternatives[alternativeIndex] = ConfigurationAlternative{Requirements: requirements}
	}
	return result
}

func nativeConfigurationPoint(point string) (ConfigurationRequirementKind, string) {
	switch point {
	case "requestMedia":
		return RequirementInput, "MediaType"
	case "propertyMedia":
		return RequirementInput, "PropertyMediaTypes"
	case "security":
		return RequirementOption, "SecurityAlternative"
	case "parameterConversion":
		return RequirementOption, "ParameterConverter"
	case "server":
		return RequirementOption, "Server"
	case "emptyValueForm":
		return RequirementOption, "EmptyValueForm"
	case "requestContentCodings":
		return RequirementOption, "RequestContentCodings"
	case "responseContentCodings":
		return RequirementOption, "ResponseContentCodings"
	case "requestCharacterEncodings":
		return RequirementOption, "RequestCharacterEncodings"
	case "responseCharacterEncodings":
		return RequirementOption, "ResponseCharacterEncodings"
	default:
		return "", ""
	}
}

func cloneJSONValues(input []any) []any {
	data, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var result []any
	if json.Unmarshal(data, &result) != nil {
		return nil
	}
	return result
}

func cloneStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func snapshotOptions(input Options) Options {
	input.Auth = cloneCredentials(input.Auth)
	input.Headers = input.Headers.Clone()
	input.SecurityAlternative = cloneOptionalInt(input.SecurityAlternative)
	input.RequestContentCodings = cloneMap(input.RequestContentCodings)
	input.ResponseContentCodings = cloneMap(input.ResponseContentCodings)
	input.RequestCharacterEncodings = cloneMap(input.RequestCharacterEncodings)
	input.ResponseCharacterEncodings = cloneMap(input.ResponseCharacterEncodings)
	return input
}

func cloneCredentials(input Credentials) Credentials {
	if input == nil {
		return nil
	}
	result := make(Credentials, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneOptionalInt(input *int) *int {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func cloneMap[T any](input map[string]T) map[string]T {
	if input == nil {
		return nil
	}
	result := make(map[string]T, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func contentEncoders(input map[string]ContentEncoder) map[string]runtime.ContentEncoder {
	result := make(map[string]runtime.ContentEncoder, len(input))
	for key, value := range input {
		result[key] = runtime.ContentEncoder(value)
	}
	return result
}
func contentDecoders(input map[string]ContentDecoder) map[string]runtime.ContentDecoder {
	result := make(map[string]runtime.ContentDecoder, len(input))
	for key, value := range input {
		result[key] = runtime.ContentDecoder(value)
	}
	return result
}
func characterEncoders(input map[string]CharacterEncoder) map[string]runtime.CharacterEncoder {
	result := make(map[string]runtime.CharacterEncoder, len(input))
	for key, value := range input {
		result[key] = runtime.CharacterEncoder(value)
	}
	return result
}
func characterDecoders(input map[string]CharacterDecoder) map[string]runtime.CharacterDecoder {
	result := make(map[string]runtime.CharacterDecoder, len(input))
	for key, value := range input {
		result[key] = runtime.CharacterDecoder(value)
	}
	return result
}
