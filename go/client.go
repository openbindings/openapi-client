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

// Edition is the exact artifact version selected during loading. The alias is
// shared with the advanced provider surface so application and generator code
// can compare editions without conversion.
type Edition = runtime.Edition

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

// ParameterInfo is one effective OpenAPI parameter identity at the native
// call boundary. InputKey is stable for generated facades and protocol
// adapters that begin with a flat parameter object.
type ParameterInfo struct {
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

// RequestBodyInfo is one admitted request representation.
type RequestBodyInfo struct {
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

// SupportDisposition is one smallest-owner protocol support outcome. It is
// reusable generator evidence and does not depend on OpenBindings Core.
type SupportDisposition struct {
	SourceRef    string
	Scope        string
	Status       string
	Code         string
	Rule         string
	Reason       string
	Requirements []string
}

type ServerAlternativeInfo struct {
	Index     int
	URL       string
	Variables []string
	Usable    bool
	Reason    string
}

type SecuritySchemeAnalysis struct {
	Name   string
	Type   string
	Scheme string
	In     string
	Scopes []string
}

type RequirementAnalysis struct {
	Type        string
	Name        string
	Durable     *bool
	Description string
	Extra       map[string]any
}

type SecurityAlternativeAnalysis struct {
	Index        int
	Anonymous    bool
	Usable       bool
	Reason       string
	Schemes      []SecuritySchemeAnalysis
	Requirements []RequirementAnalysis
}

type ResponseAlternativeAnalysis struct {
	Key        string
	SourceRef  string
	CanSucceed bool
	Usable     bool
	MediaTypes []string
	Schema     json.RawMessage
	Reason     string
}

// OperationAnalysis contains detached declaration facts for one operation.
type OperationAnalysis struct {
	Info          OperationInfo
	Description   string
	Deprecated    bool
	Parameters    []ParameterInfo
	RequestBodies []RequestBodyInfo
	Responses     []ResponseAlternativeAnalysis
	Servers       []ServerAlternativeInfo
	Security      []SecurityAlternativeAnalysis
	Requirements  []string
	Coverage      []SupportDisposition
}

// Analysis is a detached snapshot of the exact artifact loaded by a Client.
// Mutating returned slices cannot change later analysis or invocation.
type Analysis struct {
	Edition    Edition
	Location   string
	Operations []OperationAnalysis
	Coverage   []SupportDisposition
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

// Metadata is protocol-native HTTP metadata. Returned slices are detached
// from the execution engine.
type Metadata = runtime.Metadata

// HookSite identifies the resolved operation and target at an optional
// response-policy seam.
type HookSite = runtime.HookSite

// RawResult is the bounded protocol-native response presented to a hook.
type RawResult = runtime.RawResult

// Hooks optionally override response decoding or classification. handled=false
// declines the decision and continues to the deterministic builtin.
type Hooks = runtime.Hooks

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
	Hooks                      *Hooks
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
	Hooks                      *Hooks
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
	OK    bool
	Data  any
	Error any
	// ErrorPresent reports whether Error carries an HTTP failure value.
	// It is true for a JSON null value (Error == nil), false for an absent
	// failure body, and false for successful results.
	ErrorPresent bool
	Response     *http.Response
	OpenAPI      DeclarationMatch
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
	OK     bool
	Stream *Stream
	Error  any
	// ErrorPresent distinguishes a present failure value, including JSON null,
	// from an absent failure body. It is false for successful streams.
	ErrorPresent bool
	Response     *http.Response
	OpenAPI      DeclarationMatch
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
	// Details carries OpenAPI-native acquisition facts for credential
	// requirements, such as OAuth scopes, grant type, and endpoint URLs.
	Details map[string]any
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

// Analysis returns detached declaration facts from the already-loaded
// artifact. It performs no retrieval and reparses no source.
func (c *Client) Analysis() Analysis {
	if c == nil || c.inner == nil {
		return Analysis{}
	}
	return analysisValue(c.inner.Analysis())
}

// AnalyzeOperation resolves one selector and returns detached declaration
// facts from the same artifact snapshot used by invocation.
func (c *Client) AnalyzeOperation(selector OperationSelector) (OperationAnalysis, error) {
	if c == nil || c.inner == nil {
		return OperationAnalysis{}, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	value, err := c.inner.AnalyzeOperation(selector.value)
	if err != nil {
		return OperationAnalysis{}, clientError(err)
	}
	return operationAnalysisValue(value), nil
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

// Preflight performs every artifact- and configuration-derived check known
// before dispatch. A non-nil result describes complete alternative remedies.
func (c *Client) Preflight(ctx context.Context, selector OperationSelector, input Input, options ...CallOptions) (*ConfigurationRequirements, error) {
	if c == nil || c.inner == nil {
		return nil, &ClientError{Kind: ErrorInternal, Code: "NIL_CLIENT", Message: "OpenAPI client is nil"}
	}
	call, err := oneCallOptions(options)
	if err != nil {
		return nil, err
	}
	requirements, err := c.inner.Preflight(ctx, selector.value, runtimeInput(input), runtimeCallOptions(c.options, call))
	if err != nil {
		return nil, clientError(err)
	}
	return configurationRequirements(requirements), nil
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

// Preflight inspects the bound operation without dispatching it.
func (o *Operation) Preflight(ctx context.Context, input Input, options ...CallOptions) (*ConfigurationRequirements, error) {
	if o == nil || o.client == nil {
		return nil, nilClientError("OpenAPI operation is nil")
	}
	return o.client.Preflight(ctx, o.selector, input, options...)
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
		Hooks:                      runtimeHooks(options.Hooks),
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
		Hooks:                      runtimeHooks(call.Hooks),
	}
}

func runtimeHooks(hooks *Hooks) *runtime.Hooks {
	if hooks == nil {
		return nil
	}
	result := &runtime.Hooks{}
	if hooks.Decode != nil {
		result.Decode = func(site runtime.HookSite, raw runtime.RawResult) (any, bool, error) {
			return hooks.Decode(hookSiteValue(site), rawResultValue(raw))
		}
	}
	if hooks.Classify != nil {
		result.Classify = func(site runtime.HookSite, raw runtime.RawResult) (bool, bool, error) {
			return hooks.Classify(hookSiteValue(site), rawResultValue(raw))
		}
	}
	return result
}

func hookSiteValue(value runtime.HookSite) HookSite {
	return HookSite{Ref: value.Ref, Target: value.Target, Profile: value.Profile}
}

func rawResultValue(value runtime.RawResult) RawResult {
	result := RawResult{Body: append([]byte(nil), value.Body...), Meta: make(Metadata, len(value.Meta))}
	if value.Status != nil {
		status := *value.Status
		result.Status = &status
	}
	for name, values := range value.Meta {
		result.Meta[name] = append([]string(nil), values...)
	}
	return result
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
	} else {
		// An explicit follow policy selects the standard user-agent behavior
		// even when the host's reusable base client was configured for manual
		// redirects. The engine still applies its method/body and cross-origin
		// credential safety wrapper around this client.
		clone.CheckRedirect = nil
	}
	return &clone
}

func resultValue(value *runtime.Result) *Result {
	if value == nil {
		return nil
	}
	return &Result{OK: value.OK, Data: value.Data, Error: value.Error, ErrorPresent: value.ErrorPresent, Response: value.Response, OpenAPI: declaration(value.OpenAPI)}
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
	return &StreamResult{OK: value.OK, Stream: stream, Error: value.Error, ErrorPresent: value.ErrorPresent, Response: response, OpenAPI: declaration(value.OpenAPI)}
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

func analysisValue(value runtime.Analysis) Analysis {
	result := Analysis{Edition: Edition(value.Edition), Location: value.Location, Operations: make([]OperationAnalysis, len(value.Operations)), Coverage: supportDispositions(value.Coverage)}
	for index := range value.Operations {
		result.Operations[index] = operationAnalysisValue(value.Operations[index])
	}
	return result
}

func operationAnalysisValue(value runtime.OperationAnalysis) OperationAnalysis {
	parameters := make([]ParameterInfo, len(value.Parameters))
	for index, parameter := range value.Parameters {
		parameters[index] = ParameterInfo{
			Name: parameter.Name, In: parameter.In, InputKey: parameter.InputKey, Required: parameter.Required,
			Style: parameter.Style, AllowEmpty: parameter.AllowEmpty, SourceRef: parameter.SourceRef,
			Schema: append(json.RawMessage(nil), parameter.Schema...),
		}
		if parameter.Explode != nil {
			explode := *parameter.Explode
			parameters[index].Explode = &explode
		}
	}
	requestBodies := make([]RequestBodyInfo, len(value.RequestBodies))
	for index, body := range value.RequestBodies {
		requestBodies[index] = RequestBodyInfo{
			MediaType: body.MediaType, Family: body.Family, Required: body.Required, MediaRange: body.MediaRange,
			WholeValue: body.WholeValue, Base64: body.Base64,
			Base64Properties: append([]string(nil), body.Base64Properties...), Properties: append([]string(nil), body.Properties...),
			PropertyMedia: append([]string(nil), body.PropertyMedia...), Schema: append(json.RawMessage(nil), body.Schema...),
		}
	}
	responses := make([]ResponseAlternativeAnalysis, len(value.Responses))
	for index, response := range value.Responses {
		responses[index] = ResponseAlternativeAnalysis{
			Key: response.Key, SourceRef: response.SourceRef, CanSucceed: response.CanSucceed, Usable: response.Usable,
			MediaTypes: append([]string(nil), response.MediaTypes...), Schema: append(json.RawMessage(nil), response.Schema...), Reason: response.Reason,
		}
	}
	servers := make([]ServerAlternativeInfo, len(value.Servers))
	for index, server := range value.Servers {
		servers[index] = ServerAlternativeInfo{Index: server.Index, URL: server.URL, Variables: append([]string(nil), server.Variables...), Usable: server.Usable, Reason: server.Reason}
	}
	security := make([]SecurityAlternativeAnalysis, len(value.Security))
	for index, alternative := range value.Security {
		security[index] = SecurityAlternativeAnalysis{Index: alternative.Index, Anonymous: alternative.Anonymous, Usable: alternative.Usable, Reason: alternative.Reason}
		for _, scheme := range alternative.Schemes {
			security[index].Schemes = append(security[index].Schemes, SecuritySchemeAnalysis{Name: scheme.Name, Type: scheme.Type, Scheme: scheme.Scheme, In: scheme.In, Scopes: append([]string(nil), scheme.Scopes...)})
		}
		for _, requirement := range alternative.Requirements {
			security[index].Requirements = append(security[index].Requirements, requirementAnalysisValue(requirement))
		}
	}
	return OperationAnalysis{
		Info: operationInfo(value.Info), Description: value.Description, Deprecated: value.Deprecated,
		Parameters: parameters, RequestBodies: requestBodies, Responses: responses, Servers: servers, Security: security,
		Requirements: append([]string(nil), value.Requirements...), Coverage: supportDispositions(value.Coverage),
	}
}

func supportDispositions(values []runtime.SupportDisposition) []SupportDisposition {
	result := make([]SupportDisposition, len(values))
	for index, value := range values {
		result[index] = SupportDisposition{
			SourceRef: value.SourceRef, Scope: value.Scope, Status: value.Status, Code: value.Code,
			Rule: value.Rule, Reason: value.Reason, Requirements: append([]string(nil), value.Requirements...),
		}
	}
	return result
}

func requirementAnalysisValue(value runtime.Requirement) RequirementAnalysis {
	result := RequirementAnalysis{Type: value.Type, Name: value.Name, Description: value.Description, Extra: cloneStringAnyMap(value.Extra)}
	if value.Durable != nil {
		durable := *value.Durable
		result.Durable = &durable
	}
	return result
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
				// SecurityAlternative is already the native scalar option. The
				// private engine's /index path belongs to the binding context
				// representation and must not leak into the standalone API.
				if native.Name == "SecurityAlternative" {
					native.Path = ""
				}
				if schema, ok := requirement.Extra["schema"].(map[string]any); ok {
					if allowed, ok := schema["enum"].([]any); ok {
						native.AllowedValues = cloneJSONValues(allowed)
					}
				}
			} else {
				native.Kind = RequirementCredential
				native.Name = requirement.Name
				native.Credential = strings.TrimPrefix(requirement.Type, "auth.")
				native.Details = cloneStringAnyMap(requirement.Extra)
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

func cloneStringAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var result map[string]any
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
	input.Hooks = cloneHooks(input.Hooks)
	return input
}

func cloneHooks(input *Hooks) *Hooks {
	if input == nil {
		return nil
	}
	return &Hooks{Decode: input.Decode, Classify: input.Classify}
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
