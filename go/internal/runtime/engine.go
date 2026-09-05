package openapiclient

import (
	"context"
	"errors"
	"net/http"
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
	artifact *Artifact
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
	artifact      *Artifact
	options       PrepareOptions
	prerequisites *Prerequisites
}

func (p *PreparedOperation) Ref() string      { return p.options.Ref }
func (p *PreparedOperation) Profile() Profile { return p.options.Profile }
func (p *PreparedOperation) Prerequisites() *Prerequisites {
	return clonePrerequisites(p.prerequisites)
}

func (e *Engine) Prepare(ctx context.Context, options PrepareOptions) (*PreparedOperation, error) {
	if err := normalizePrepareContentCodings(&options); err != nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
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
	artifact, floor, err := loadArtifact(ctx, loadClient, options.Source, allowExternal)
	if err != nil {
		return nil, &ExecutionError{Code: CodeSourceLoadFailed, Message: err.Error(), Cause: err}
	}
	if options.Source.Location != "" {
		e.mu.Lock()
		e.cache[options.Source.Location] = cachedDocument{artifact: artifact, floor: floor}
		e.mu.Unlock()
	}
	return prepareArtifactWithFloor(artifact, floor, options)
}

func (e *Engine) PrepareCached(ctx context.Context, options PrepareOptions) (*PreparedOperation, error) {
	if err := normalizePrepareContentCodings(&options); err != nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
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
	if cached.artifact == nil {
		return nil, nil
	}
	if options.HTTPClient == nil {
		options.HTTPClient = e.invocationClient
	}
	options.Context = contextWithSecurityHandlers(options.Context, options.SecurityHandlers)
	return prepareArtifactWithFloor(cached.artifact, cached.floor, options)
}

func normalizePrepareContentCodings(options *PrepareOptions) error {
	request, err := normalizeContentEncoders(options.RequestContentCodings)
	if err != nil {
		return err
	}
	response, err := normalizeContentDecoders(options.ResponseContentCodings)
	if err != nil {
		return err
	}
	options.RequestContentCodings = request
	options.ResponseContentCodings = response
	return nil
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
	if document == nil {
		return prepareArtifactWithFloor(nil, nil, options)
	}
	return prepareArtifactWithFloor(&Artifact{Document: document, Edition: Edition(document.OpenAPI)}, nil, options)
}

func prepareDocumentWithFloor(document *openapi3.T, floor *acceptanceFloor, options PrepareOptions) (*PreparedOperation, error) {
	if document == nil {
		return prepareArtifactWithFloor(nil, floor, options)
	}
	return prepareArtifactWithFloor(&Artifact{Document: document, Edition: Edition(document.OpenAPI)}, floor, options)
}

func prepareArtifactWithFloor(artifact *Artifact, floor *acceptanceFloor, options PrepareOptions) (*PreparedOperation, error) {
	// The acceptance-floor inventory filter (§3.2 of `openbindings.openapi-3.1@1` and its siblings): a
	// ladder-invalid target is not addressed, and its invocation is refused
	// before dispatch -- provably no interaction side effect.
	if verdict := floor.opVerdict(options.Ref); verdict != nil && verdict.Disposition == "invalid" {
		return nil, &ExecutionError{Code: CodeRefused, Message: floorInvalidTargetMessage(len(verdict.Defects)) + " (" + options.Ref + ")"}
	}
	if artifact == nil || artifact.Document == nil {
		return nil, &ExecutionError{Code: CodeSourceConfigError, Message: "OpenAPI artifact is nil"}
	}
	target, err := artifact.ResolveOperation(options.Ref)
	if err != nil {
		return nil, executionErrorForOperationResolution(err)
	}
	target = requestTargetForEdition(target, artifact.Edition)
	prerequisites, err := preflightPrerequisitesForArtifactTarget(artifact, target, options)
	if err != nil {
		return nil, err
	}
	return &PreparedOperation{artifact: artifact, options: options, prerequisites: prerequisites}, nil
}

func executionErrorForOperationResolution(err error) *ExecutionError {
	code := CodeInvalidRef
	var resolution *OperationResolutionError
	if errors.As(err, &resolution) {
		switch resolution.Kind {
		case OperationTargetNotFound:
			code = CodeRefNotFound
		case OperationTargetExcluded:
			code = CodeRefused
		}
	}
	return &ExecutionError{Code: code, Message: err.Error(), Cause: err}
}

func preflightPrerequisites(document *openapi3.T, options PrepareOptions) (*Prerequisites, error) {
	if document == nil {
		return nil, nil
	}
	artifact := &Artifact{Document: document, Edition: Edition(document.OpenAPI)}
	target, err := artifact.ResolveOperation(options.Ref)
	if err != nil {
		return nil, nil
	}
	return preflightPrerequisitesForTarget(document, target, options)
}

func preflightPrerequisitesForTarget(document *openapi3.T, target *OperationTarget, options PrepareOptions) (*Prerequisites, error) {
	return preflightPrerequisitesForArtifactTarget(nil, target, options)
}

func preflightPrerequisitesForArtifactTarget(artifact *Artifact, target *OperationTarget, options PrepareOptions) (*Prerequisites, error) {
	target = targetForImplicitConnectionScope(target, options.Context)
	document := target.Document
	pathItem := target.PathItem
	operation := target.Operation
	baseURL, err := resolveServer(document, pathItem, operation, options.Context, options.Source.Location)
	if err != nil {
		return nil, nil
	}
	parameters := effectiveParameters(pathItem, operation)
	security := requiredContext(document, operation, options.Context, baseURL, parameters)
	var plans []*bodyPlan
	openAPI32 := artifact != nil && artifact.Edition.IsOpenAPI32()
	if openAPI32 && hasRequestBody(operation) && operation.RequestBody.Value.Required {
		plans, err = planRequestBodiesForArtifact(artifact, target, profileCoordinate(options.Profile))
		if err != nil {
			return nil, &ExecutionError{Code: CodeSourceConfigError, Message: err.Error(), Cause: err}
		}
	}
	media, err := requiredRequestMediaContextWithPlans(document, operation, profileCoordinate(options.Profile), options.Context, plans)
	if err != nil {
		return nil, &ExecutionError{Code: CodeSourceConfigError, Message: err.Error(), Cause: err}
	}
	propertyMedia, err := requiredPropertyMediaContextWithPlans(document, operation, profileCoordinate(options.Profile), options.Context, plans)
	if err != nil {
		return nil, &ExecutionError{Code: CodeSourceConfigError, Message: err.Error(), Cause: err}
	}
	return mergeRequirements(mergeRequirements(security, media), propertyMedia), nil
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
		runBinding(execution.ctx, client, args, execution, p.artifact)
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
