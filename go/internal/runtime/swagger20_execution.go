package openapiclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	target := p.options.Source.Location
	serverBase, err := resolveSwagger20Server(p.document, p.operation, p.options.Server, p.options.ServerSchemeIndex)
	if err != nil {
		return nil, swagger20RefusalError(p.serverRefusal(err), target)
	}
	security, err := selectSwagger20Security(p.document, p.operation, parameters, p.options.SecurityAlternative, p.options.SecurityCredentials)
	if err != nil {
		return nil, swagger20RefusalError(err, target)
	}
	// A supplied value this point does not admit is the caller's own choice, so
	// no further context changes the answer: it stays the plain species.
	if p.options.EmptyValueForm != "" && p.options.EmptyValueForm != Swagger20EmptyValueNameOnly && p.options.EmptyValueForm != Swagger20EmptyValueEmpty {
		return nil, &ExecutionError{Code: CodeRefused, Message: "emptyValueForm must be name-only or empty"}
	}
	payload, err := swagger20PayloadFor(parameters, p.document)
	if err != nil {
		return nil, swagger20RefusalError(err, target)
	}
	if swagger20PayloadIsRequired(payload) {
		consumes, mediaErr := effectiveSwagger20MediaSet(p.document, p.operation, "consumes")
		if mediaErr != nil {
			return nil, swagger20RefusalError(mediaErr, target)
		}
		if _, mediaErr := selectSwagger20RequestMedia(consumes, payload, p.options.RequestMedia); mediaErr != nil {
			return nil, swagger20RefusalError(mediaErr, target)
		}
	}
	responses, err := swagger20ResponsesFor(p.document.graph, p.operation)
	if err != nil {
		return nil, swagger20RefusalError(err, target)
	}
	execution := newExecution(ctx)
	client := p.options.HTTPClient
	if client == nil {
		client = defaultInvocationHTTPClient()
	}
	client = swagger20RedirectClient(client, security)
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

func swagger20RedirectClient(client *http.Client, security []swagger20CredentialPlacement) *http.Client {
	clone := *client
	original := client.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 0 {
			prior := via[len(via)-1]
			if request.Method != prior.Method || (prior.Body != nil && prior.Body != http.NoBody && (request.Body == nil || request.Body == http.NoBody)) {
				return http.ErrUseLastResponse
			}
		}
		if len(via) > 0 && !sameHTTPOrigin(via[len(via)-1].URL, request.URL) {
			request.Header.Del("Cookie")
			request.Header.Del("Authorization")
			query := request.URL.Query()
			for _, placement := range security {
				if placement.query {
					query.Del(placement.name)
				} else {
					request.Header.Del(placement.name)
				}
			}
			request.URL.RawQuery = query.Encode()
		}
		if original != nil {
			return original(request, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &clone
}

func sameHTTPOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || !strings.EqualFold(left.Scheme, right.Scheme) || !strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	port := func(value *url.URL) string {
		if explicit := value.Port(); explicit != "" {
			return explicit
		}
		if strings.EqualFold(value.Scheme, "https") {
			return "443"
		}
		if strings.EqualFold(value.Scheme, "http") {
			return "80"
		}
		return ""
	}
	return port(left) == port(right)
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
		execution.failExecution(swagger20RefusalError(err, prepared.options.Source.Location))
		return
	}
	for _, placement := range security {
		if swagger20CredentialCollidesWithRoutedInput(placement, routed) {
			execution.failExecution(swagger20RefusalError(fmt.Errorf("Swagger 2.0 credential destination collides with a supplied Parameter contribution"), prepared.options.Source.Location))
			return
		}
	}
	applySwagger20Security(&routed, security)
	payloadPresent := routed.bodyPresent || routed.formPresent
	if !payloadPresent {
		for _, header := range routed.headers {
			if strings.EqualFold(header.name, "Content-Encoding") {
				execution.failExecution(swagger20RefusalError(fmt.Errorf("request Content-Encoding cannot be supplied when the invocation emits no request representation"), prepared.options.Source.Location))
				return
			}
		}
	}
	if payloadPresent && swagger20MethodExcludesPayload(prepared.operation.method) {
		execution.failExecution(&ExecutionError{Code: CodeRefused, Message: fmt.Sprintf("Swagger 2.0 %s operations exclude the payload lane", prepared.operation.method)})
		return
	}
	var requestBody []byte
	contentType := ""
	if payloadPresent {
		model, modelErr := swagger20PayloadFor(parameters, prepared.document)
		if modelErr != nil {
			execution.failExecution(swagger20RefusalError(modelErr, prepared.options.Source.Location))
			return
		}
		consumes, mediaErr := effectiveSwagger20MediaSet(prepared.document, prepared.operation, "consumes")
		if mediaErr != nil {
			execution.failExecution(swagger20RefusalError(mediaErr, prepared.options.Source.Location))
			return
		}
		selection, mediaErr := selectSwagger20RequestMedia(consumes, model, prepared.options.RequestMedia)
		if mediaErr != nil {
			execution.failExecution(swagger20RefusalError(mediaErr, prepared.options.Source.Location))
			return
		}
		requestBody, contentType, mediaErr = encodeSwagger20RequestPayload(selection, model, routed, prepared.options)
		if mediaErr != nil {
			execution.failExecution(swagger20RefusalError(mediaErr, prepared.options.Source.Location))
			return
		}
	}
	requestURL, err := AssembleRequestURL(serverBase, routed.resolvedPath, swagger20RawQuery(routed.query))
	if err != nil {
		execution.failExecution(swagger20RefusalError(err, prepared.options.Source.Location))
		return
	}
	var bodyReader io.Reader
	if payloadPresent {
		bodyReader = bytes.NewReader(requestBody)
	}
	request, err := http.NewRequestWithContext(ctx, swagger20HTTPMethod(prepared.operation.method), requestURL.String(), bodyReader)
	if err != nil {
		execution.failExecution(swagger20RefusalError(err, prepared.options.Source.Location))
		return
	}
	for _, header := range routed.headers {
		request.Header.Add(header.name, header.value)
	}
	accept, err := swagger20GeneratedAccept(prepared.document, prepared.operation, parameters, responses)
	if err != nil {
		execution.failExecution(swagger20RefusalError(err, prepared.options.Source.Location))
		return
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if payloadPresent {
		request.Header.Set("Content-Type", contentType)
		if err := applySwagger20RequestContentCodings(request, parameters, prepared.options.RequestContentCodings); err != nil {
			execution.failExecution(swagger20RefusalError(err, prepared.options.Source.Location))
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
