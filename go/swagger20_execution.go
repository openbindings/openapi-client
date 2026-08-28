package openapiclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Start invokes the selected Swagger 2.0 operation through the edition-native
// parameter and representation lanes.
func (p *Swagger20PreparedOperation) Start(ctx context.Context) (*Execution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	parameters, err := p.parameters()
	if err != nil {
		return nil, err
	}
	serverBase, err := resolveSwagger20Server(p.document, p.operation, p.options.Server, p.options.ServerSchemeIndex)
	if err != nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
	security, err := selectSwagger20Security(p.document, p.operation, parameters, p.options.SecurityAlternative, p.options.SecurityCredentials)
	if err != nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
	if p.options.EmptyValueForm != "" && p.options.EmptyValueForm != Swagger20EmptyValueNameOnly && p.options.EmptyValueForm != Swagger20EmptyValueEmpty {
		return nil, &ExecutionError{Code: CodeRefused, Message: "emptyValueForm must be name-only or empty"}
	}
	payload, err := swagger20PayloadFor(parameters, p.document)
	if err != nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
	if swagger20PayloadIsRequired(payload) {
		consumes, mediaErr := effectiveSwagger20MediaSet(p.document, p.operation, "consumes")
		if mediaErr != nil {
			return nil, &ExecutionError{Code: CodeRefused, Message: mediaErr.Error(), Cause: mediaErr}
		}
		if _, mediaErr := selectSwagger20RequestMedia(consumes, payload, p.options.RequestMedia); mediaErr != nil {
			return nil, &ExecutionError{Code: CodeRefused, Message: mediaErr.Error(), Cause: mediaErr}
		}
	}
	responses, err := swagger20ResponsesFor(p.operation)
	if err != nil {
		return nil, &ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err}
	}
	execution := newExecution(ctx)
	client := p.options.HTTPClient
	if client == nil {
		client = defaultInvocationHTTPClient()
	}
	go func() {
		runSwagger20(execution.ctx, client, p, parameters, responses, serverBase, security, execution)
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

func swagger20PayloadIsRequired(model swagger20PayloadModel) bool {
	if model.body != nil {
		return model.body.required
	}
	for _, parameter := range model.form {
		if parameter.required {
			return true
		}
	}
	return false
}

func runSwagger20(ctx context.Context, client *http.Client, prepared *Swagger20PreparedOperation, parameters *swagger20ParameterSet, responses swagger20ResponseSet, serverBase string, security []swagger20CredentialPlacement, execution *Execution) {
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
	applySwagger20Security(&routed, security)
	payloadPresent := routed.bodyPresent || routed.formPresent
	if payloadPresent && swagger20MethodExcludesPayload(prepared.operation.method) {
		execution.failExecution(&ExecutionError{Code: CodeRefused, Message: fmt.Sprintf("Swagger 2.0 %s operations exclude the payload lane", prepared.operation.method)})
		return
	}
	var requestBody []byte
	contentType := ""
	if payloadPresent {
		model, modelErr := swagger20PayloadFor(parameters, prepared.document)
		if modelErr != nil {
			execution.failExecution(&ExecutionError{Code: CodeRefused, Message: modelErr.Error(), Cause: modelErr})
			return
		}
		consumes, mediaErr := effectiveSwagger20MediaSet(prepared.document, prepared.operation, "consumes")
		if mediaErr != nil {
			execution.failExecution(&ExecutionError{Code: CodeRefused, Message: mediaErr.Error(), Cause: mediaErr})
			return
		}
		selection, mediaErr := selectSwagger20RequestMedia(consumes, model, prepared.options.RequestMedia)
		if mediaErr != nil {
			execution.failExecution(&ExecutionError{Code: CodeRefused, Message: mediaErr.Error(), Cause: mediaErr})
			return
		}
		requestBody, contentType, mediaErr = encodeSwagger20RequestPayload(selection, model, routed, prepared.options)
		if mediaErr != nil {
			execution.failExecution(&ExecutionError{Code: CodeRefused, Message: mediaErr.Error(), Cause: mediaErr})
			return
		}
	}
	requestURL, err := AssembleRequestURL(serverBase, routed.resolvedPath, swagger20RawQuery(routed.query))
	if err != nil {
		execution.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err})
		return
	}
	var bodyReader io.Reader
	if payloadPresent {
		bodyReader = bytes.NewReader(requestBody)
	}
	request, err := http.NewRequestWithContext(ctx, swagger20HTTPMethod(prepared.operation.method), requestURL.String(), bodyReader)
	if err != nil {
		execution.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err})
		return
	}
	for _, header := range routed.headers {
		request.Header.Add(header.name, header.value)
	}
	if payloadPresent {
		request.Header.Set("Content-Type", contentType)
		if err := applySwagger20RequestContentCodings(request, parameters, prepared.options.RequestContentCodings); err != nil {
			execution.failExecution(&ExecutionError{Code: CodeRefused, Message: err.Error(), Cause: err})
			return
		}
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
	handleSwagger20Response(request, response, prepared, responses, execution)
}

func swagger20MethodExcludesPayload(method string) bool {
	switch method {
	case "get", "head", "delete", "options":
		return true
	default:
		return false
	}
}

func applySwagger20RequestContentCodings(request *http.Request, parameters *swagger20ParameterSet, codecs map[string]ContentEncoder) error {
	raw := strings.Join(request.Header.Values("Content-Encoding"), ",")
	if raw == "" {
		return nil
	}
	governing := swagger20HeaderParameter(parameters, "Content-Encoding")
	if governing == nil {
		return fmt.Errorf("request Content-Encoding has no effective governing Header Parameter")
	}
	if governing.typeName != "string" {
		return fmt.Errorf("request Content-Encoding governing Header Parameter is not a string declaration")
	}
	if _, err := governing.validateAndConvert(raw, nil); err != nil {
		return fmt.Errorf("request Content-Encoding is not admitted by its governing Header Parameter: %w", err)
	}
	tokens, err := parsedContentCodings(raw)
	if err != nil {
		return err
	}
	body, err := readRequestBody(request)
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if token == "identity" {
			continue
		}
		codec := codecs[token]
		if codec == nil {
			return fmt.Errorf("request content-coding %q is unsupported", token)
		}
		body, err = codec(body)
		if err != nil {
			return fmt.Errorf("request content-coding %q failed: %w", token, err)
		}
	}
	replaceRequestBody(request, body)
	return nil
}

func swagger20HeaderParameter(parameters *swagger20ParameterSet, name string) *swagger20Parameter {
	if parameters == nil {
		return nil
	}
	var found *swagger20Parameter
	for _, parameter := range parameters.nonBody {
		if parameter.in != Swagger20ParameterHeader || !strings.EqualFold(parameter.name, name) {
			continue
		}
		if found != nil {
			return nil
		}
		found = parameter
	}
	return found
}
