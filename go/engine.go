package openapiclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
)

type Engine struct {
	client           *http.Client
	invocationClient *http.Client
	mu               sync.RWMutex
	cache            map[string]cachedDocument
}

// cachedDocument pairs a location-cached document with its acceptance floor,
// so PrepareCached applies the same inventory filter as a fresh load.
type cachedDocument struct {
	document *openapi3.T
	floor    *acceptanceFloor
}

func NewEngine(client *http.Client) *Engine {
	if client == nil {
		return &Engine{
			client:           defaultHTTPClient(),
			invocationClient: defaultInvocationHTTPClient(),
			cache:            map[string]cachedDocument{},
		}
	}
	return &Engine{client: client, invocationClient: client, cache: map[string]cachedDocument{}}
}

type PreparedOperation struct {
	document      *openapi3.T
	options       PrepareOptions
	prerequisites *Prerequisites
}

func (p *PreparedOperation) Ref() string      { return p.options.Ref }
func (p *PreparedOperation) Profile() Profile { return p.options.Profile }
func (p *PreparedOperation) Prerequisites() *Prerequisites {
	return clonePrerequisites(p.prerequisites)
}

func (e *Engine) Prepare(ctx context.Context, options PrepareOptions) (*PreparedOperation, error) {
	options.Profile = normalizedProfile(options.Profile)
	options.Context = contextWithSecurityHandlers(options.Context, options.SecurityHandlers)
	client := options.HTTPClient
	if client == nil {
		client = e.client
	}
	loadClient := client
	if options.HTTPClient == nil {
		options.HTTPClient = e.invocationClient
	}
	allowExternal := true
	if options.AllowExternalRefs != nil {
		allowExternal = *options.AllowExternalRefs
	}
	document, floor, err := loadDocument(ctx, loadClient, options.Source, allowExternal)
	if err != nil {
		return nil, &ExecutionError{Code: CodeSourceLoadFailed, Message: err.Error(), Cause: err}
	}
	if options.Source.Location != "" {
		e.mu.Lock()
		e.cache[options.Source.Location] = cachedDocument{document: document, floor: floor}
		e.mu.Unlock()
	}
	return prepareDocumentWithFloor(document, floor, options)
}

func (e *Engine) PrepareCached(ctx context.Context, options PrepareOptions) (*PreparedOperation, error) {
	options.Profile = normalizedProfile(options.Profile)
	if options.Source.Content != nil {
		allow := false
		options.AllowExternalRefs = &allow
		return e.Prepare(ctx, options)
	}
	if options.Source.Location == "" {
		return nil, nil
	}
	e.mu.RLock()
	cached := e.cache[options.Source.Location]
	e.mu.RUnlock()
	if cached.document == nil {
		return nil, nil
	}
	if options.HTTPClient == nil {
		options.HTTPClient = e.invocationClient
	}
	options.Context = contextWithSecurityHandlers(options.Context, options.SecurityHandlers)
	return prepareDocumentWithFloor(cached.document, cached.floor, options)
}

// Custom security satisfaction is engine configuration, not caller context.
// Derive the private compatibility view only from handlers installed on this
// prepare so a caller cannot spoof an unsupported scheme as satisfied.
func contextWithSecurityHandlers(contextValue map[string]any, handlers map[string]SecurityHandler) map[string]any {
	result := make(map[string]any, len(contextValue)+1)
	for key, value := range contextValue {
		if key != "$openapiSecurity" {
			result[key] = value
		}
	}
	configured := map[string]bool{}
	for name, handler := range handlers {
		if handler != nil {
			configured[name] = true
		}
	}
	if len(configured) > 0 {
		result["$openapiSecurity"] = configured
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func prepareDocument(document *openapi3.T, options PrepareOptions) (*PreparedOperation, error) {
	return prepareDocumentWithFloor(document, nil, options)
}

func prepareDocumentWithFloor(document *openapi3.T, floor *acceptanceFloor, options PrepareOptions) (*PreparedOperation, error) {
	// The acceptance-floor inventory filter (openbindings.openapi@1 §3): a
	// ladder-invalid target is not addressed, and its invocation is refused
	// before dispatch -- provably no interaction side effect.
	if verdict := floor.opVerdict(options.Ref); verdict != nil && verdict.Disposition == "invalid" {
		return nil, &ExecutionError{Code: CodeRefused, Message: floorInvalidTargetMessage(len(verdict.Defects)) + " (" + options.Ref + ")"}
	}
	path, method, err := parseRef(options.Ref)
	if err != nil {
		return nil, &ExecutionError{Code: CodeInvalidRef, Message: err.Error(), Cause: err}
	}
	if document.Paths == nil {
		return nil, &ExecutionError{Code: CodeSourceConfigError, Message: "OpenAPI document has no paths defined"}
	}
	pathItem := document.Paths.Find(path)
	if pathItem == nil || pathItem.GetOperation(strings.ToUpper(method)) == nil {
		return nil, &ExecutionError{Code: CodeRefNotFound, Message: fmt.Sprintf("operation %q was not found", options.Ref)}
	}
	prerequisites, err := preflightPrerequisites(document, options)
	if err != nil {
		return nil, err
	}
	return &PreparedOperation{document: document, options: options, prerequisites: prerequisites}, nil
}

func preflightPrerequisites(document *openapi3.T, options PrepareOptions) (*Prerequisites, error) {
	path, method, err := parseRef(options.Ref)
	if err != nil || document.Paths == nil {
		return nil, nil
	}
	pathItem := document.Paths.Find(path)
	if pathItem == nil {
		return nil, nil
	}
	operation := pathItem.GetOperation(strings.ToUpper(method))
	if operation == nil {
		return nil, nil
	}
	baseURL, err := resolveServer(document, pathItem, operation, options.Context, options.Source.Location)
	if err != nil {
		return nil, nil
	}
	parameters := effectiveParameters(pathItem, operation)
	security := requiredContext(document, operation, options.Context, baseURL, parameters)
	media, err := requiredRequestMediaContext(document, operation, profileCoordinate(options.Profile), options.Context)
	if err != nil {
		return nil, &ExecutionError{Code: CodeSourceConfigError, Message: err.Error(), Cause: err}
	}
	return mergeRequirements(security, media), nil
}

func (p *PreparedOperation) Start(ctx context.Context) (*Execution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	execution := newExecution(ctx)
	client := p.options.HTTPClient
	if client == nil {
		client = defaultInvocationHTTPClient()
	}
	args := newExecutionArgs(p.options)
	go func() {
		runBinding(execution.ctx, client, args, execution, p.document)
		execution.finishAfterRun()
	}()
	select {
	case <-execution.ready:
		return execution, nil
	case <-execution.done:
		return nil, execution.Wait()
	case <-ctx.Done():
		execution.Cancel()
		return nil, executionError(CodeCancelled, ctx.Err())
	}
}

func clonePrerequisites(value *Prerequisites) *Prerequisites {
	if value == nil {
		return nil
	}
	out := &Prerequisites{Target: value.Target, Alternatives: make([]RequirementAlternative, len(value.Alternatives))}
	for i, alternative := range value.Alternatives {
		out.Alternatives[i].Requirements = append([]Requirement(nil), alternative.Requirements...)
	}
	return out
}
