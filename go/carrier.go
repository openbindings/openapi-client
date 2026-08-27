package openapiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
)

// These identifiers are private migration coordinates for the extracted
// implementation. Public callers select artifact capabilities with Profile;
// Adapter binding identifiers never enter this module's API or coordinates.
const (
	profileBaseCoordinate          = "base"
	profileRoutedCoordinate        = "routed"
	profileMediaCoordinate         = "media"
	profileResponseCoordinate      = "response"
	profileDynamicObjectCoordinate = "dynamic-object"
	profileWholeJSONCoordinate     = "whole-json"
	profileFullCoordinate          = "full"
)

func profileCoordinate(profile Profile) string {
	switch {
	case profile.SchemaOmittedOAS30ByteCarriage:
		return profileFullCoordinate
	case profile.WholeJSONCarriage:
		return profileWholeJSONCoordinate
	case profile.DynamicObjectCarriage:
		return profileDynamicObjectCoordinate
	case profile.ResponseFidelity:
		return profileResponseCoordinate
	case profile.MediaFidelity:
		return profileMediaCoordinate
	case profile.RoutedInputs:
		return profileRoutedCoordinate
	default:
		return profileBaseCoordinate
	}
}

func hasRoutedInputs(spec string) bool { return spec != profileBaseCoordinate }
func hasMediaFidelity(spec string) bool {
	return spec == profileMediaCoordinate || spec == profileResponseCoordinate || spec == profileDynamicObjectCoordinate || spec == profileWholeJSONCoordinate || spec == profileFullCoordinate
}
func hasResponseFidelity(spec string) bool {
	return spec == profileResponseCoordinate || spec == profileDynamicObjectCoordinate || spec == profileWholeJSONCoordinate || spec == profileFullCoordinate
}
func hasDynamicObjectCarriage(spec string) bool {
	return spec == profileDynamicObjectCoordinate || spec == profileWholeJSONCoordinate || spec == profileFullCoordinate
}
func hasWholeJSONCarriage(spec string) bool {
	return spec == profileWholeJSONCoordinate || spec == profileFullCoordinate
}
func hasSchemaOmittedOAS30ByteCarriage(spec string) bool { return spec == profileFullCoordinate }

type executionSource struct {
	Capability string
	Location   string
	Content    []byte
}

type executionArgs struct {
	Source               executionSource
	Ref                  string
	Profile              string
	InputRouteKey        string
	InputRouteMarker     string
	Context              map[string]any
	Hooks                *invokeHooks
	Site                 *HookSite
	MaxDeliveryUnitBytes int64
	SecurityHandlers     map[string]SecurityHandler
	ParameterConverter   ParameterConverter
}

func newExecutionArgs(options PrepareOptions) *executionArgs {
	profile := normalizedProfile(options.Profile)
	return &executionArgs{
		Source: executionSource{
			Capability: profileCoordinate(profile),
			Location:   options.Source.Location,
			Content:    options.Source.Content,
		},
		Ref:                  options.Ref,
		Profile:              profile.Name,
		InputRouteKey:        profile.InputRouteKey,
		InputRouteMarker:     profile.InputRouteMarker,
		Context:              options.Context,
		Hooks:                newInvokeHooks(options.Hooks),
		MaxDeliveryUnitBytes: options.MaxDeliveryUnitBytes,
		SecurityHandlers:     options.SecurityHandlers,
		ParameterConverter:   options.ParameterConverter,
	}
}

func (a *executionArgs) DeliveryUnitLimit() int64 {
	if a == nil {
		return defaultMaxDeliveryUnitBytes
	}
	return deliveryUnitLimit(a.MaxDeliveryUnitBytes)
}

type outputDecoder func(HookSite, RawResult) (any, error)
type resultClassifier func(HookSite, RawResult) (bool, error)

type invokeHooks struct {
	hooks             *Hooks
	mu                sync.Mutex
	decodeDecidedBy   string
	classifyDecidedBy string
}

func newInvokeHooks(hooks *Hooks) *invokeHooks { return &invokeHooks{hooks: hooks} }

func (h *invokeHooks) DecodeOutput(site HookSite, raw RawResult, builtin outputDecoder) (any, error) {
	if h != nil && h.hooks != nil && h.hooks.Decode != nil {
		value, handled, err := h.hooks.Decode(site, raw)
		if err != nil {
			return nil, asExecutionError(CodeResponseError, err)
		}
		if handled {
			h.mu.Lock()
			h.decodeDecidedBy = "hook"
			h.mu.Unlock()
			return value, nil
		}
	}
	if builtin == nil {
		return nil, &ExecutionError{Code: CodeRuntime, Message: "OpenAPI execution has no response decoder"}
	}
	if h != nil {
		h.mu.Lock()
		h.decodeDecidedBy = "builtin"
		h.mu.Unlock()
	}
	return builtin(site, raw)
}

func (h *invokeHooks) Classify(site HookSite, raw RawResult, builtin resultClassifier) (bool, error) {
	if h != nil && h.hooks != nil && h.hooks.Classify != nil {
		value, handled, err := h.hooks.Classify(site, raw)
		if err != nil {
			return false, asExecutionError(CodeExecutionFailed, err)
		}
		if handled {
			h.mu.Lock()
			h.classifyDecidedBy = "hook"
			h.mu.Unlock()
			return value, nil
		}
	}
	if builtin == nil {
		return false, &ExecutionError{Code: CodeRuntime, Message: "OpenAPI execution has no response classifier"}
	}
	if h != nil {
		h.mu.Lock()
		h.classifyDecidedBy = "builtin"
		h.mu.Unlock()
	}
	return builtin(site, raw)
}

func (h *invokeHooks) DecodeDecidedBy() string {
	if h == nil {
		return "builtin"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.decodeDecidedBy
}

func (h *invokeHooks) ClassifyDecidedBy() string {
	if h == nil {
		return "builtin"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.classifyDecidedBy
}

type executionHandle[I, O any] interface {
	Done() <-chan struct{}
	closeInputBoundary() error
	readInputBoundary(context.Context) (I, error)
	setLeadingMetadata(Metadata) error
	setTrailingMetadata(Metadata) error
	emitOutput(O) error
	emitOutputWithMetadata(O, Metadata) error
	setHTTPResponse(*http.Response)
	setHTTPResponseBody([]byte)
	closeOutputBoundary() error
	failExecution(error)
}

func (e *Execution) closeInputBoundary() error { e.signalReady(); e.closeInput(); return nil }
func (e *Execution) readInputBoundary(ctx context.Context) (any, error) {
	e.requestInput()
	e.signalReady()
	value, ok, err := e.nextInput(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, io.EOF
	}
	return value, nil
}
func (e *Execution) setLeadingMetadata(value Metadata) error  { e.setHeader(value); return nil }
func (e *Execution) setTrailingMetadata(value Metadata) error { e.setTrailer(value); return nil }
func (e *Execution) emitOutput(value any) error               { return e.emit(e.ctx, value, nil) }
func (e *Execution) emitOutputWithMetadata(value any, metadata Metadata) error {
	return e.emit(e.ctx, value, metadata)
}
func (e *Execution) setHTTPResponse(value *http.Response) { e.setResponse(value) }
func (e *Execution) setHTTPResponseBody(value []byte)     { e.setResponseBody(value) }
func (e *Execution) closeOutputBoundary() error           { e.finish(nil); return nil }
func (e *Execution) failExecution(err error)              { e.finish(normalizeExecutionError(err)) }

func doneContext(parent context.Context, done <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func newConfigValueRequirementCompat(point, path, description string, schema map[string]any, durable *bool) Requirement {
	return newConfigValueRequirement(point, path, description, schema, durable)
}

func newContextRequiredError(message string, details *Prerequisites) *ExecutionError {
	return &ExecutionError{Code: CodeContextRequired, Message: message, Details: details, DetailsPresent: true}
}

func normalizeExecutionError(err error) *ExecutionError {
	return asExecutionError(CodeExecutionFailed, err)
}

func asExecutionError(code string, err error) *ExecutionError {
	if err == nil {
		return nil
	}
	var existing *ExecutionError
	if errors.As(err, &existing) {
		return existing
	}
	return &ExecutionError{Code: code, Message: err.Error(), Cause: err}
}

func httpFailureError(statusCode int, _ string) *ExecutionError {
	evidence := map[string]any{"status": statusCode}
	return &ExecutionError{Code: CodeExecutionFailed, Message: "Invocation completed unsuccessfully", Evidence: evidence, Diagnostics: evidence}
}
