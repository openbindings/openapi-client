package openapiclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Start invokes the selected Swagger 2.0 operation through the edition-native
// parameter lane. Request and response representation bodies are completed by
// the media pass; this pass admits bodyless 204/205 exchanges.
func (p *Swagger20PreparedOperation) Start(ctx context.Context) (*Execution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	parameters, err := p.parameters()
	if err != nil {
		return nil, err
	}
	if err := validateSwagger20ServerOverride(p.options.Server); err != nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
	if p.options.EmptyValueForm != "" && p.options.EmptyValueForm != Swagger20EmptyValueNameOnly && p.options.EmptyValueForm != Swagger20EmptyValueEmpty {
		return nil, &ExecutionError{Code: CodeRefused, Message: "emptyValueForm must be name-only or empty"}
	}
	execution := newExecution(ctx)
	client := p.options.HTTPClient
	if client == nil {
		client = defaultInvocationHTTPClient()
	}
	go func() {
		runSwagger20(execution.ctx, client, p, parameters, execution)
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

func runSwagger20(ctx context.Context, client *http.Client, prepared *Swagger20PreparedOperation, parameters *swagger20ParameterSet, execution *Execution) {
	input := Swagger20Input{}
	// Even a parameter-free operation observes the optional caller envelope:
	// an authored unknown member must refuse rather than race a dispatch. EOF
	// is the sole spelling of an absent envelope.
	execution.requestInput()
	execution.signalReady()
	value, present, err := execution.nextInput(ctx)
	if err != nil {
		execution.failExecution(err)
		return
	}
	if present {
		switch typed := value.(type) {
		case Swagger20Input:
			input = typed
		case *Swagger20Input:
			if typed == nil {
				execution.failExecution(&ExecutionError{Code: CodeRefused, Message: "Swagger 2.0 input is nil"})
				return
			}
			input = *typed
		default:
			execution.failExecution(&ExecutionError{Code: CodeRefused, Message: fmt.Sprintf("Swagger 2.0 input has type %T, want Swagger20Input", value)})
			return
		}
	}
	execution.closeInput()

	routed, err := routeSwagger20Input(parameters, prepared.operation.path, input, prepared.options)
	if err != nil {
		execution.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err})
		return
	}
	if routed.bodyPresent {
		execution.failExecution(&ExecutionError{Code: CodeRefused, Message: "Swagger 2.0 body request media carriage is not yet available"})
		return
	}
	if len(routed.formData) > 0 {
		execution.failExecution(&ExecutionError{Code: CodeRefused, Message: "Swagger 2.0 formData request media carriage is not yet available"})
		return
	}
	requestURL, err := AssembleRequestURL(prepared.options.Server, routed.resolvedPath, swagger20RawQuery(routed.query))
	if err != nil {
		execution.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err})
		return
	}
	request, err := http.NewRequestWithContext(ctx, swagger20HTTPMethod(prepared.operation.method), requestURL.String(), nil)
	if err != nil {
		execution.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err})
		return
	}
	for _, header := range routed.headers {
		request.Header.Add(header.name, header.value)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			execution.failExecution(executionError(CodeCancelled, ctx.Err()))
			return
		}
		execution.failExecution(&ExecutionError{Code: CodeConnectFailed, Message: err.Error(), Cause: err})
		return
	}
	defer response.Body.Close()
	execution.setHTTPResponse(response)
	execution.setHeader(headerMetadata(response.Header))
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusResetContent {
		execution.failExecution(&ExecutionError{
			Code: CodeResponseError, Message: fmt.Sprintf("Swagger 2.0 response status %d requires the response-governance pass", response.StatusCode),
		})
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	execution.setTrailer(headerMetadata(response.Trailer))
	execution.closeOutputBoundary()
}

func validateSwagger20ServerOverride(value string) error {
	if value == "" {
		return fmt.Errorf("Swagger 2.0 execution requires a complete consumer server override in this pass")
	}
	if err := validateServerBaseSpelling(value); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("Swagger 2.0 consumer server scheme %q is not HTTP", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("Swagger 2.0 consumer server override is not an absolute authority URL")
	}
	return nil
}
