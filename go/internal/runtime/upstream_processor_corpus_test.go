package openapiclient_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	openapi "github.com/openbindings/openapi-client/go"
)

type CallOptions = openapi.CallOptions
type CharacterDecoder = openapi.CharacterDecoder
type CharacterEncoder = openapi.CharacterEncoder
type ClientError = openapi.ClientError
type ContentDecoder = openapi.ContentDecoder
type ContentEncoder = openapi.ContentEncoder
type Edition = openapi.Edition
type ImplicitConnectionScope = openapi.ImplicitConnectionScope
type Input = openapi.Input
type Parameters = openapi.Parameters
type Source = openapi.Source

const (
	CodeContextRequired  = openapi.CodeConfigurationRequired
	CodeExecutionFailed  = "ERR_EXECUTION_FAILED"
	CodeRefused          = "ERR_REFUSED"
	ErrorOperation       = openapi.ErrorOperation
	ErrorSource          = openapi.ErrorSource
	EditionSwagger20     = openapi.Swagger20
)

var (
	Load         = openapi.Load
	OperationRef = openapi.OperationRef
	ServerURL    = openapi.ServerURL
)

// This is deliberately a black-box qualification of the standalone Go
// client. It does not import the OpenBindings SDK or route calls through its
// invocation API. The published processor corpus is authority; this adapter
// only translates its language-neutral fixtures into the native client API.

type processorCorpusFile struct {
	Format    string              `json:"format"`
	Family    string              `json:"family"`
	Scenarios []processorScenario `json:"scenarios"`
}

type processorScenario struct {
	ID       string              `json:"id"`
	Given    processorGiven      `json:"given"`
	Expected []processorExpected `json:"expected"`
}

type processorGiven struct {
	Source        map[string]any `json:"source"`
	Binding       map[string]any `json:"binding"`
	Configuration map[string]any `json:"configuration"`
	Invocation    map[string]any `json:"invocation"`
	Peer          map[string]any `json:"peer"`
	Runtime       map[string]any `json:"runtime"`
	Resources     map[string]any `json:"resources"`
}

type processorExpected struct {
	Disposition string               `json:"disposition"`
	Phase       string               `json:"phase"`
	Assertions  []processorAssertion `json:"assertions"`
}

type processorAssertion struct {
	Path           string `json:"path"`
	Absent         bool   `json:"absent"`
	Equals         any    `json:"equals"`
	OneOf          []any  `json:"oneOf"`
	SetEquals      []any  `json:"setEquals"`
	Contains       any    `json:"contains"`
	NotContains    any    `json:"notContains"`
	SemanticEquals any    `json:"semanticEquals"`
	raw            map[string]json.RawMessage
}

func (a *processorAssertion) UnmarshalJSON(data []byte) error {
	type plain processorAssertion
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*a = processorAssertion(decoded)
	return json.Unmarshal(data, &a.raw)
}

type processorObservation struct {
	Disposition string
	Phase       string
	Data        map[string]any
}

func TestUpstreamProcessorCorpus(t *testing.T) {
	if os.Getenv("OPENAPI_UPSTREAM_CORPUS") != "1" {
		t.Skip("set OPENAPI_UPSTREAM_CORPUS=1 to run the pinned binding corpus")
	}
	root := os.Getenv("OPENAPI_UPSTREAM_CORPUS_ROOT")
	if root == "" {
		root = filepath.Join("..", "..", "..", "conformance", "upstream", "openbindings-0.2", "processor")
	}
	for _, family := range []string{"openapi-2.0", "openapi-3.0", "openapi-3.1", "openapi-3.2"} {
		family := family
		t.Run(family, func(t *testing.T) {
			file := loadProcessorCorpus(t, filepath.Join(root, family+".json"), family)
			for _, scenario := range file.Scenarios {
				scenario := scenario
				t.Run(scenario.ID, func(t *testing.T) {
					got := runProcessorScenario(t, scenario, family)
					if err := matchProcessorObservation(got, scenario.Expected); err != nil {
						t.Fatalf("%v\nobservation: %#v", err, got)
					}
				})
			}
		})
	}
}

func loadProcessorCorpus(t *testing.T, path, family string) processorCorpusFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var file processorCorpusFile
	if err := decoder.Decode(&file); err != nil {
		t.Fatal(err)
	}
	if file.Format != "openbindings.binding-spec-processor-scenarios@5" || file.Family != family || len(file.Scenarios) == 0 {
		t.Fatalf("invalid %s processor corpus header", family)
	}
	return file
}

type processorRoundTripper struct {
	peer       map[string]any
	resources  map[string]any
	dispatches []map[string]any
	index      int
}

func (r *processorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if resource, ok := r.resources[request.URL.String()]; ok {
		body := resourceBytes(resource)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	}
	body := []byte{}
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
	}
	dispatch := map[string]any{
		"method":  request.Method,
		"url":     request.URL.String(),
		"headers": normalizedProcessorHeaders(request.Header),
	}
	if len(body) > 0 {
		dispatch["bodyBase64"] = base64.StdEncoding.EncodeToString(body)
		dispatch["bodyByteLength"] = len(body)
		dispatch["byteLength"] = len(body)
		var parsed any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		var trailing any
		if decoder.Decode(&parsed) == nil && decoder.Decode(&trailing) == io.EOF {
			dispatch["body"] = parsed
		} else {
			dispatch["body"] = string(body)
		}
	}
	r.dispatches = append(r.dispatches, dispatch)

	peer := r.peer
	if responses, ok := peer["responses"].([]any); ok && len(responses) > 0 {
		selected := r.index
		if selected >= len(responses) {
			selected = len(responses) - 1
		}
		peer, _ = responses[selected].(map[string]any)
	}
	r.index++
	status := 599
	if value, ok := processorInteger(peer["status"]); ok {
		status = value
	}
	headers := http.Header{}
	if raw, ok := peer["headers"].(map[string]any); ok {
		for name, value := range raw {
			headers.Set(name, fmt.Sprint(value))
		}
	}
	responseBody := []byte{}
	if encoded, ok := peer["bodyBase64"].(string); ok {
		responseBody, _ = base64.StdEncoding.DecodeString(encoded)
	} else if value, ok := peer["body"].(string); ok {
		responseBody = []byte(value)
	}
	// net/http's client-side representation of a HEAD response never exposes
	// payload octets to the caller. Model that HTTP message boundary even
	// though this in-memory RoundTripper receives a scripted peer body.
	if request.Method == http.MethodHead {
		responseBody = nil
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Request:    request,
	}, nil
}

func runProcessorScenario(t *testing.T, scenario processorScenario, family string) processorObservation {
	t.Helper()
	roundTripper := &processorRoundTripper{peer: scenario.Given.Peer, resources: scenario.Given.Resources}
	httpClient := &http.Client{Transport: roundTripper, CheckRedirect: processorRedirectPolicy(scenario)}
	source := Source{}
	if location, ok := scenario.Given.Source["location"].(string); ok {
		source.Location = location
	}
	if content, present := scenario.Given.Source["content"]; present {
		if text, ok := content.(string); ok {
			source.Content = []byte(text)
		} else {
			raw, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			source.Content = raw
		}
	}
	client, err := Load(context.Background(), source, openapi.Options{
		DocumentHTTPClient: httpClient,
		HTTPClient:         httpClient,
	})
	if err != nil {
		return processorTerminal(err, "load", roundTripper.dispatches, nil)
	}
	if !processorEditionMatches(client.Edition(), family) {
		return processorTerminal(fmt.Errorf("artifact edition %q does not match %q", client.Edition(), family), "load", roundTripper.dispatches, nil)
	}
	selectorText, _ := scenario.Given.Binding["selector"].(string)
	selector := OperationRef(selectorText)
	_, resolutionErr := client.Operation(selector)
	if resolutionErr != nil {
		return processorTerminal(resolutionErr, processorSelectorFailurePhase(source.Content, selectorText), roundTripper.dispatches, nil)
	}
	input, err := processorInput(scenario, source.Content)
	if err != nil {
		return processorTerminal(err, "pre-dispatch", roundTripper.dispatches, nil)
	}
	options, err := processorCallOptions(scenario, httpClient)
	if err != nil {
		return processorTerminal(err, "pre-dispatch", roundTripper.dispatches, nil)
	}
	stream, err := client.Stream(context.Background(), selector, input, options)
	if err != nil {
		return processorTerminal(err, processorErrorPhase(err, len(roundTripper.dispatches) > 0), roundTripper.dispatches, nil)
	}
	outputs := []any{}
	if !stream.OK {
		return processorObservation{Disposition: "error", Phase: "response", Data: processorData(roundTripper.dispatches, outputs)}
	}
	for {
		event, open, nextErr := stream.Stream.Next(context.Background())
		if nextErr != nil {
			return processorTerminal(nextErr, "response", roundTripper.dispatches, outputs)
		}
		if !open {
			break
		}
		value := processorOutput(event.Data)
		if event.SSE != nil && family != "openapi-3.2" {
			sse := map[string]any{"data": value, "event": event.SSE.Event, "id": event.SSE.ID}
			if event.SSE.Retry != nil {
				sse["retry"] = *event.SSE.Retry
			}
			value = sse
		}
		outputs = append(outputs, value)
	}
	if err := stream.Stream.Wait(); err != nil {
		return processorTerminal(err, "response", roundTripper.dispatches, outputs)
	}
	return processorObservation{Disposition: "complete", Phase: "completion", Data: processorData(roundTripper.dispatches, outputs)}
}

func processorData(dispatches []map[string]any, outputs []any) map[string]any {
	if outputs == nil {
		outputs = []any{}
	}
	data := map[string]any{"outputs": outputs}
	if len(dispatches) > 0 {
		data["dispatch"] = dispatches[0]
		all := make([]any, len(dispatches))
		for i := range dispatches {
			all[i] = dispatches[i]
		}
		data["dispatches"] = all
	}
	return data
}

func processorTerminal(err error, phase string, dispatches []map[string]any, outputs []any) processorObservation {
	data := processorData(dispatches, outputs)
	data["error"] = map[string]any{
		"code":    map[bool]string{true: CodeExecutionFailed, false: CodeRefused}[len(dispatches) > 0],
		"message": err.Error(),
	}
	disposition := "refusal"
	var clientError *ClientError
	if errors.As(err, &clientError) && clientError.Code == CodeContextRequired {
		disposition = "context-required"
	} else if len(dispatches) > 0 {
		disposition = "error"
	}
	return processorObservation{Disposition: disposition, Phase: phase, Data: data}
}

func processorErrorPhase(err error, dispatched bool) string {
	if dispatched {
		return "response"
	}
	var clientError *ClientError
	if errors.As(err, &clientError) {
		if clientError.Kind == ErrorSource {
			return "load"
		}
		if clientError.Kind == ErrorOperation {
			return "resolution"
		}
	}
	return "pre-dispatch"
}

func processorSelectorFailurePhase(content []byte, selector string) string {
	var root map[string]any
	if json.Unmarshal(content, &root) != nil {
		return "resolution"
	}
	parts := strings.Split(strings.TrimPrefix(selector, "#/paths/"), "/")
	if len(parts) < 2 {
		return "resolution"
	}
	pathName := strings.ReplaceAll(strings.ReplaceAll(parts[0], "~1", "/"), "~0", "~")
	paths, _ := root["paths"].(map[string]any)
	item, _ := paths[pathName].(map[string]any)
	if _, hasReference := item["$ref"]; hasReference {
		return "pre-dispatch"
	}
	return "resolution"
}

func processorCallOptions(scenario processorScenario, client *http.Client) (CallOptions, error) {
	configuration := scenario.Given.Configuration
	runtime := scenario.Given.Runtime
	options := CallOptions{HTTPClient: client}
	if server, present := configuration["server"]; present {
		switch value := server.(type) {
		case string:
			options.Server = ServerURL(value)
		case map[string]any:
			if baseURL, ok := value["baseUrl"].(string); ok {
				options.Server = ServerURL(baseURL)
			} else {
				variables, _ := value["variables"].(map[string]any)
				if index, ok := processorInteger(value["index"]); ok {
					options.Server = openapi.Server(index, processorStringMap(variables))
				} else {
					options.Server = openapi.ServerVariables(processorStringMap(variables))
				}
			}
		}
	}
	if security, ok := configuration["security"].(map[string]any); ok {
		if index, ok := processorInteger(security["index"]); ok {
			options.SecurityAlternative = &index
		}
	}
	if scope, ok := configuration["implicitConnectionScope"].(string); ok {
		options.ImplicitConnectionScope = ImplicitConnectionScope(scope)
	}
	if value, ok := configuration["emptyValueForm"].(string); ok {
		options.EmptyValueForm = openapi.EmptyValueForm(value)
	}
	if conversions, ok := configuration["parameterConversion"].(map[string]any); ok {
		options.ParameterConverter = func(value any) (string, error) {
			encoded, _ := json.Marshal(value)
			converted, ok := conversions[string(encoded)].(string)
			if !ok {
				return "", fmt.Errorf("parameterConversion has no result for %s", encoded)
			}
			return converted, nil
		}
	}
	if credentials, ok := runtime["credentials"].(map[string]any); ok {
		var err error
		options.Auth, err = processorCredentials(credentials)
		if err != nil {
			return CallOptions{}, err
		}
	}
	if policy, _ := runtime["redirectPolicy"].(string); policy == "follow" || policy == "ordinary-user-agent" {
		options.Redirect = openapi.RedirectFollow
	} else {
		options.Redirect = openapi.RedirectManual
	}
	if limit, ok := processorInteger(runtime["maxDeliveryUnitBytes"]); ok {
		options.MaxDeliveryUnitBytes = int64(limit)
	}
	options.RequestContentCodings = processorEncoders(runtime["requestContentCodings"])
	options.ResponseContentCodings = processorDecoders(runtime["responseContentCodings"])
	options.RequestCharacterEncodings = processorCharacterEncoders(runtime["requestCharacterEncodings"])
	options.ResponseCharacterEncodings = processorCharacterDecoders(runtime["responseCharacterEncodings"])
	return options, nil
}

func processorInput(scenario processorScenario, content []byte) (Input, error) {
	abstract := processorClone(scenario.Given.Invocation["input"])
	if abstract == nil {
		abstract = map[string]any{}
	}
	root, ok := abstract.(map[string]any)
	if !ok {
		return Input{}, fmt.Errorf("native OpenAPI input must be an object")
	}
	for name := range root {
		if name != "parameters" && name != "body" {
			return Input{}, fmt.Errorf("unknown OpenAPI input member %q", name)
		}
	}
	if materializations, ok := scenario.Given.Invocation["inputMaterializations"].([]any); ok {
		for _, raw := range materializations {
			materialization, _ := raw.(map[string]any)
			path, _ := materialization["path"].(string)
			units, _ := materialization["codeUnits"].([]any)
			codeUnits := make([]uint16, len(units))
			for index, unit := range units {
				integer, _ := processorInteger(unit)
				codeUnits[index] = uint16(integer)
			}
			if err := processorSetPointer(root, path, processorUTF16String(codeUnits)); err != nil {
				return Input{}, err
			}
		}
	}
	result := Input{}
	if media, ok := scenario.Given.Configuration["requestMedia"].(string); ok {
		result.MediaType = media
	}
	if properties, ok := scenario.Given.Configuration["propertyMedia"].(map[string]any); ok {
		result.PropertyMediaTypes = processorStringMap(properties)
	}
	if body, present := root["body"]; present {
		result.Body, result.BodyPresent = body, true
		if encoded, ok := body.(string); ok && processorRawOAS3Request(content, scenario.Given.Binding["selector"], result.MediaType) {
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded {
				return Input{}, fmt.Errorf("raw-octet input is not canonical Base64")
			}
			result.Body = decoded
		}
	}
	parameterValues, parametersOK := root["parameters"].(map[string]any)
	if _, present := root["parameters"]; present && !parametersOK {
		return Input{}, fmt.Errorf("OpenAPI input parameters must be an object")
	}
	declarations := processorParameterDeclarations(content, scenario.Given.Binding["selector"])
	locations := map[string]map[string]any{}
	form := map[string]any{}
	for field, value := range parameterValues {
		location, name := processorParameterIdentity(field, declarations)
		if location == "formData" {
			form[name] = value
			continue
		}
		if location == "" {
			location = "query"
		}
		if locations[location] == nil {
			locations[location] = map[string]any{}
		}
		locations[location][name] = value
	}
	result.Parameters = Parameters{
		Path: locations["path"], Query: locations["query"], QueryString: locations["querystring"],
		Header: locations["header"], Cookie: locations["cookie"],
	}
	if len(form) > 0 {
		result.Body, result.BodyPresent = form, true
	}
	return result, nil
}

func processorUTF16String(units []uint16) string {
	result := []byte{}
	for index := 0; index < len(units); index++ {
		unit := units[index]
		if 0xD800 <= unit && unit <= 0xDBFF && index+1 < len(units) && 0xDC00 <= units[index+1] && units[index+1] <= 0xDFFF {
			result = utf8.AppendRune(result, utf16.DecodeRune(rune(unit), rune(units[index+1])))
			index++
			continue
		}
		if 0xD800 <= unit && unit <= 0xDFFF {
			// Go strings can retain the original code unit only as its three-byte
			// UTF-8-shaped sequence. It is intentionally invalid UTF-8 so the
			// native JSON boundary can reject it rather than substitute U+FFFD.
			result = append(result, byte(0xE0|unit>>12), byte(0x80|(unit>>6)&0x3F), byte(0x80|unit&0x3F))
			continue
		}
		result = utf8.AppendRune(result, rune(unit))
	}
	return string(result)
}

type processorParameter struct {
	name     string
	in       string
	required bool
}

func processorParameterDeclarations(content []byte, selector any) []processorParameter {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return nil
	}
	selected, ok := selector.(string)
	if !ok {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(selected, "#/paths/"), "/")
	if len(parts) < 2 {
		return nil
	}
	pathName := strings.ReplaceAll(strings.ReplaceAll(parts[0], "~1", "/"), "~0", "~")
	paths, _ := root["paths"].(map[string]any)
	item, _ := paths[pathName].(map[string]any)
	var operation map[string]any
	if len(parts) >= 3 && parts[1] == "additionalOperations" {
		additional, _ := item["additionalOperations"].(map[string]any)
		operation, _ = additional[parts[2]].(map[string]any)
	} else {
		operation, _ = item[parts[1]].(map[string]any)
	}
	seen := map[string]processorParameter{}
	for _, owner := range []map[string]any{item, operation} {
		values, _ := owner["parameters"].([]any)
		for _, raw := range values {
			value, _ := raw.(map[string]any)
			name, nameOK := value["name"].(string)
			location, locationOK := value["in"].(string)
			if nameOK && locationOK {
				required, _ := value["required"].(bool)
				if location == "header" && !required && !processorHTTPToken(name) {
					continue
				}
				seen[location+"\x00"+name] = processorParameter{name: name, in: location, required: required}
			}
		}
	}
	result := make([]processorParameter, 0, len(seen))
	for _, value := range seen {
		result = append(result, value)
	}
	return result
}

func processorParameterIdentity(field string, declarations []processorParameter) (string, string) {
	locationsByName := map[string]map[string]bool{}
	for _, declaration := range declarations {
		if locationsByName[declaration.name] == nil {
			locationsByName[declaration.name] = map[string]bool{}
		}
		locationsByName[declaration.name][declaration.in] = true
	}
	qualified := false
	for _, locations := range locationsByName {
		if len(locations) > 1 {
			qualified = true
			break
		}
	}
	location, name := "", field
	if split := strings.IndexByte(field, '/'); split > 0 {
		location, name = field[:split], field[split+1:]
	}
	name = strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
	if location != "" && !qualified {
		return "", field
	}
	if location == "" && qualified {
		return "", field
	}
	matches := []processorParameter{}
	for _, candidate := range declarations {
		if candidate.name == name && (location == "" || location == candidate.in) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return "", field
	}
	return matches[0].in, name
}

func processorHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if !(char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(char))) {
			return false
		}
	}
	return true
}

func processorCredentials(values map[string]any) (openapi.Credentials, error) {
	result := openapi.Credentials{}
	for name, raw := range values {
		if object, ok := raw.(map[string]any); ok {
			if user, userOK := object["userId"].(string); userOK {
				password, passwordOK := object["password"].(string)
				if !passwordOK {
					return nil, fmt.Errorf("credential %q has no string password", name)
				}
				result[name] = openapi.Basic(user, password)
				continue
			}
			if token, tokenOK := object["accessToken"].(string); tokenOK && (object["tokenType"] == nil || object["tokenType"] == "Bearer") {
				result[name] = openapi.Token(token)
				continue
			}
			return nil, fmt.Errorf("credential %q has no supported public credential shape", name)
		}
		if token, ok := raw.(string); ok {
			result[name] = openapi.Token(token)
		} else {
			return nil, fmt.Errorf("credential %q has no supported public credential shape", name)
		}
	}
	return result, nil
}

func processorEncoders(raw any) map[string]ContentEncoder {
	values, _ := raw.(map[string]any)
	result := map[string]ContentEncoder{}
	for name, action := range values {
		if action == "unavailable" {
			continue
		}
		name, action := name, action
		result[name] = func(input []byte) ([]byte, error) {
			switch action {
			case "fail":
				return nil, fmt.Errorf("sentinel codec invoked")
			case "identity":
				return append([]byte(nil), input...), nil
			case "reverse":
				output := append([]byte(nil), input...)
				for left, right := 0, len(output)-1; left < right; left, right = left+1, right-1 {
					output[left], output[right] = output[right], output[left]
				}
				return output, nil
			default:
				return nil, fmt.Errorf("unknown request codec action %q for %s", action, name)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func processorDecoders(raw any) map[string]ContentDecoder {
	values, _ := raw.(map[string]any)
	result := map[string]ContentDecoder{}
	for name, action := range values {
		if action == "unavailable" {
			continue
		}
		name, action := name, action
		result[name] = func(input []byte) ([]byte, error) {
			switch action {
			case "fail":
				return nil, fmt.Errorf("sentinel codec invoked")
			case "identity":
				return append([]byte(nil), input...), nil
			case "reverse":
				output := append([]byte(nil), input...)
				for left, right := 0, len(output)-1; left < right; left, right = left+1, right-1 {
					output[left], output[right] = output[right], output[left]
				}
				return output, nil
			case "unwrap":
				prefix := strings.ToLower(name) + "("
				if !strings.HasPrefix(string(input), prefix) || !strings.HasSuffix(string(input), ")") {
					return nil, fmt.Errorf("malformed codec wrapper")
				}
				return append([]byte(nil), input[len(prefix):len(input)-1]...), nil
			default:
				return nil, fmt.Errorf("unknown response codec action %q for %s", action, name)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func processorCharacterEncoders(raw any) map[string]CharacterEncoder {
	values, _ := raw.(map[string]any)
	result := map[string]CharacterEncoder{}
	for name, action := range values {
		if action == "unavailable" {
			continue
		}
		name, action := strings.ToLower(name), action
		result[name] = func(input string) ([]byte, error) {
			switch action {
			case "fail":
				return nil, fmt.Errorf("sentinel character encoder invoked")
			case "identity":
				return []byte(input), nil
			default:
				return nil, fmt.Errorf("unknown request character codec action %q for %s", action, name)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func processorCharacterDecoders(raw any) map[string]CharacterDecoder {
	values, _ := raw.(map[string]any)
	result := map[string]CharacterDecoder{}
	for name, action := range values {
		if action == "unavailable" {
			continue
		}
		name, action := strings.ToLower(name), action
		result[name] = func(input []byte) (string, error) {
			switch action {
			case "fail":
				return "", fmt.Errorf("sentinel character decoder invoked")
			case "identity":
				return string(input), nil
			default:
				return "", fmt.Errorf("unknown response character codec action %q for %s", action, name)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func processorRedirectPolicy(scenario processorScenario) func(*http.Request, []*http.Request) error {
	policy, _ := scenario.Given.Runtime["redirectPolicy"].(string)
	if policy == "follow" || policy == "ordinary-user-agent" {
		return nil
	}
	return func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
}

func processorRawOAS3Request(content []byte, selector any, configuredMedia string) bool {
	var root map[string]any
	if json.Unmarshal(content, &root) != nil {
		return false
	}
	version, _ := root["openapi"].(string)
	if !strings.HasPrefix(version, "3.") {
		return false
	}
	operation := processorOperationObject(root, selector)
	requestBody, _ := operation["requestBody"].(map[string]any)
	contentMap, _ := requestBody["content"].(map[string]any)
	media := configuredMedia
	if media == "" && len(contentMap) == 1 {
		for name := range contentMap {
			if !strings.Contains(name, "*") {
				media = name
			}
		}
	}
	base := strings.ToLower(strings.TrimSpace(strings.Split(media, ";")[0]))
	if base == "" || base == "application/json" || strings.HasSuffix(base, "+json") || base == "application/x-www-form-urlencoded" || base == "multipart/form-data" {
		return false
	}
	mediaObject, _ := contentMap[media].(map[string]any)
	schema, present := mediaObject["schema"]
	if !present {
		return true
	}
	types, formats, typed := processorSchemaSignature(schema, root, map[string]bool{})
	return !typed || len(types) == 1 && types["string"] && len(formats) == 1 && formats["binary"]
}

func processorOperationObject(root map[string]any, selector any) map[string]any {
	selected, _ := selector.(string)
	parts := strings.Split(strings.TrimPrefix(selected, "#/paths/"), "/")
	if len(parts) < 2 {
		return nil
	}
	pathName := strings.ReplaceAll(strings.ReplaceAll(parts[0], "~1", "/"), "~0", "~")
	paths, _ := root["paths"].(map[string]any)
	item, _ := paths[pathName].(map[string]any)
	if len(parts) >= 3 && parts[1] == "additionalOperations" {
		additional, _ := item["additionalOperations"].(map[string]any)
		value, _ := additional[parts[2]].(map[string]any)
		return value
	}
	value, _ := item[parts[1]].(map[string]any)
	return value
}

func processorSchemaSignature(value any, root map[string]any, active map[string]bool) (map[string]bool, map[string]bool, bool) {
	object, _ := value.(map[string]any)
	if reference, ok := object["$ref"].(string); ok && strings.HasPrefix(reference, "#/") && !active[reference] {
		active[reference] = true
		resolved, _, _ := processorPointer(root, strings.TrimPrefix(reference, "#"))
		types, formats, typed := processorSchemaSignature(resolved, root, active)
		delete(active, reference)
		return types, formats, typed
	}
	types, formats, typed := map[string]bool{}, map[string]bool{}, false
	switch raw := object["type"].(type) {
	case string:
		types[raw], typed = true, true
	case []any:
		for _, value := range raw {
			if name, ok := value.(string); ok {
				types[name], typed = true, true
			}
		}
	}
	if format, ok := object["format"].(string); ok {
		formats[format] = true
	}
	return types, formats, typed
}

func processorEditionMatches(edition Edition, family string) bool {
	return strings.HasPrefix(string(edition), strings.TrimPrefix(family, "openapi-")) || family == "openapi-2.0" && edition == EditionSwagger20
}

func processorOutput(value any) any {
	if bytesValue, ok := value.([]byte); ok {
		return base64.StdEncoding.EncodeToString(bytesValue)
	}
	return value
}

func matchProcessorObservation(got processorObservation, expected []processorExpected) error {
	failures := []string{}
	for index, alternative := range expected {
		if got.Disposition != alternative.Disposition || got.Phase != alternative.Phase {
			failures = append(failures, fmt.Sprintf("alternative %d: got %s/%s, want %s/%s", index+1, got.Disposition, got.Phase, alternative.Disposition, alternative.Phase))
			continue
		}
		if err := matchProcessorAssertions(got.Data, alternative.Assertions); err == nil {
			return nil
		} else {
			failures = append(failures, fmt.Sprintf("alternative %d: %v", index+1, err))
		}
	}
	return fmt.Errorf("matched no expected alternative:\n%s", strings.Join(failures, "\n"))
}

func matchProcessorAssertions(root any, assertions []processorAssertion) error {
	for _, assertion := range assertions {
		actual, present, err := processorPointer(root, assertion.Path)
		if err != nil {
			return err
		}
		if assertion.Absent {
			if present {
				return fmt.Errorf("%s is present", assertion.Path)
			}
			continue
		}
		if !present {
			return fmt.Errorf("%s is absent", assertion.Path)
		}
		switch {
		case assertion.raw["equals"] != nil:
			if !processorJSONEqual(actual, assertion.Equals) {
				return fmt.Errorf("%s = %s, want %s", assertion.Path, processorPrintable(actual), processorPrintable(assertion.Equals))
			}
		case assertion.OneOf != nil:
			matched := false
			for _, candidate := range assertion.OneOf {
				matched = matched || processorJSONEqual(actual, candidate)
			}
			if !matched {
				return fmt.Errorf("%s = %s, want oneOf %s", assertion.Path, processorPrintable(actual), processorPrintable(assertion.OneOf))
			}
		case assertion.SetEquals != nil:
			if !processorSetEqual(actual, assertion.SetEquals) {
				return fmt.Errorf("%s = %s, want set %s", assertion.Path, processorPrintable(actual), processorPrintable(assertion.SetEquals))
			}
		case assertion.raw["contains"] != nil:
			if !processorContains(actual, assertion.Contains) {
				return fmt.Errorf("%s = %s, want contains %s", assertion.Path, processorPrintable(actual), processorPrintable(assertion.Contains))
			}
		case assertion.raw["notContains"] != nil:
			if processorContains(actual, assertion.NotContains) {
				return fmt.Errorf("%s = %s, want notContains %s", assertion.Path, processorPrintable(actual), processorPrintable(assertion.NotContains))
			}
		case assertion.SemanticEquals != nil:
			interpreted, err := processorSemanticValue(actual, assertion.SemanticEquals)
			if err != nil {
				return fmt.Errorf("%s: %w", assertion.Path, err)
			}
			semantic := assertion.SemanticEquals.(map[string]any)
			if !processorJSONEqual(interpreted, semantic["value"]) {
				return fmt.Errorf("%s semantic value = %s, want %s", assertion.Path, processorPrintable(interpreted), processorPrintable(semantic["value"]))
			}
		default:
			return fmt.Errorf("%s has no comparison", assertion.Path)
		}
	}
	return nil
}

func processorSemanticValue(actual, raw any) (any, error) {
	assertion, _ := raw.(map[string]any)
	kind, _ := assertion["as"].(string)
	switch kind {
	case "json-lines":
		text, ok := actual.(string)
		if !ok || !strings.HasSuffix(text, "\n") {
			return nil, fmt.Errorf("invalid JSON Lines framing")
		}
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		values := make([]any, len(lines))
		for index, line := range lines {
			if err := processorDecodeJSON([]byte(line), &values[index]); err != nil {
				return nil, err
			}
		}
		return values, nil
	case "json-sequence":
		text, ok := actual.(string)
		if !ok {
			return nil, fmt.Errorf("invalid JSON sequence body")
		}
		values := []any{}
		for len(text) > 0 {
			if text[0] != 0x1e {
				return nil, fmt.Errorf("JSON sequence frame omits RS")
			}
			end := strings.IndexByte(text[1:], '\n')
			if end < 0 {
				return nil, fmt.Errorf("JSON sequence frame omits LF")
			}
			end++
			var value any
			if err := processorDecodeJSON([]byte(text[1:end]), &value); err != nil {
				return nil, err
			}
			values = append(values, value)
			text = text[end+1:]
		}
		return values, nil
	case "querystring-json":
		text, ok := actual.(string)
		mark := strings.IndexByte(text, '?')
		if !ok || mark < 0 {
			return nil, fmt.Errorf("URL has no query component")
		}
		decoded, err := processorPercentDecode(text[mark+1:], false)
		if err != nil {
			return nil, err
		}
		var value any
		return value, processorDecodeJSON([]byte(decoded), &value)
	case "query-json-parameter", "form-json-field":
		text, ok := actual.(string)
		if !ok {
			return nil, fmt.Errorf("named assertion requires a string")
		}
		if kind == "query-json-parameter" {
			mark := strings.IndexByte(text, '?')
			if mark < 0 {
				return nil, fmt.Errorf("URL has no query")
			}
			text = text[mark+1:]
		}
		units, err := processorNamedUnits(text, kind == "form-json-field")
		if err != nil {
			return nil, err
		}
		if err := processorCheckNames(units, assertion); err != nil {
			return nil, err
		}
		unit, err := processorSelectedUnit(units, assertion["name"])
		if err != nil {
			return nil, err
		}
		var value any
		return value, processorDecodeJSON([]byte(unit.value), &value)
	case "multipart-json-part":
		dispatch, ok := actual.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("multipart assertion requires a dispatch")
		}
		headers, _ := dispatch["headers"].(map[string]any)
		contentType := fmt.Sprint(headers["content-type"])
		boundary := processorBoundary(contentType)
		body, _ := dispatch["body"].(string)
		if boundary == "" || body == "" {
			return nil, fmt.Errorf("invalid multipart wrapper")
		}
		parts, err := processorMultipartParts(body, boundary)
		if err != nil {
			return nil, err
		}
		if err := processorCheckNames(parts, assertion); err != nil {
			return nil, err
		}
		part, err := processorSelectedUnit(parts, assertion["name"])
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(part.contentType, "application/json") {
			return nil, fmt.Errorf("multipart JSON part has wrong Content-Type")
		}
		var value any
		return value, processorDecodeJSON([]byte(part.value), &value)
	default:
		return nil, fmt.Errorf("unknown semantic interpreter %q", kind)
	}
}

type processorNamedUnit struct{ name, value, contentType string }

func processorNamedUnits(raw string, plusAsSpace bool) ([]processorNamedUnit, error) {
	if raw == "" {
		return nil, nil
	}
	result := []processorNamedUnit{}
	for _, rawUnit := range strings.Split(raw, "&") {
		name, value := rawUnit, ""
		if split := strings.IndexByte(rawUnit, '='); split >= 0 {
			name, value = rawUnit[:split], rawUnit[split+1:]
		}
		decodedName, err := processorPercentDecode(name, plusAsSpace)
		if err != nil {
			return nil, err
		}
		decodedValue, err := processorPercentDecode(value, plusAsSpace)
		if err != nil {
			return nil, err
		}
		result = append(result, processorNamedUnit{name: decodedName, value: decodedValue})
	}
	return result, nil
}

func processorPercentDecode(value string, plusAsSpace bool) (string, error) {
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			continue
		}
		if index+2 >= len(value) || !strings.ContainsRune("0123456789ABCDEF", rune(value[index+1])) || !strings.ContainsRune("0123456789ABCDEF", rune(value[index+2])) {
			return "", fmt.Errorf("percent triplets must use uppercase hex")
		}
		index += 2
	}
	if plusAsSpace {
		value = strings.ReplaceAll(value, "+", " ")
	}
	return url.PathUnescape(value)
}

func processorBoundary(contentType string) string {
	for _, part := range strings.Split(contentType, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "boundary=") {
			return strings.Trim(strings.TrimSpace(part[len("boundary="):]), "\"")
		}
	}
	return ""
}

var processorPartName = regexp.MustCompile(`(?i); name="((?:[^"\\]|\\.)*)"$`)

func processorMultipartParts(body, boundary string) ([]processorNamedUnit, error) {
	rawParts := strings.Split(body, "--"+boundary)
	result := []processorNamedUnit{}
	for _, raw := range rawParts[1:] {
		if raw == "--\r\n" || raw == "--" || raw == "" {
			continue
		}
		raw = strings.TrimPrefix(raw, "\r\n")
		split := strings.Index(raw, "\r\n\r\n")
		if split < 0 {
			return nil, fmt.Errorf("multipart part has no header boundary")
		}
		headers := strings.Split(raw[:split], "\r\n")
		disposition, contentType := "", ""
		for _, header := range headers {
			if strings.HasPrefix(strings.ToLower(header), "content-disposition:") {
				disposition = header
			}
			if strings.HasPrefix(strings.ToLower(header), "content-type:") {
				contentType = strings.TrimSpace(header[len("content-type:"):])
			}
		}
		match := processorPartName.FindStringSubmatch(disposition)
		if len(match) != 2 || strings.Contains(strings.ToLower(disposition), "filename=") || strings.Contains(strings.ToLower(disposition), "filename*=") {
			return nil, fmt.Errorf("multipart part name is not exact generated form")
		}
		value := strings.TrimSuffix(raw[split+4:], "\r\n")
		name := strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(match[1])
		result = append(result, processorNamedUnit{name: name, value: value, contentType: contentType})
	}
	return result, nil
}

func processorCheckNames(units []processorNamedUnit, assertion map[string]any) error {
	wantedRaw, _ := assertion["names"].([]any)
	wanted := make([]string, len(wantedRaw))
	for i := range wantedRaw {
		wanted[i] = fmt.Sprint(wantedRaw[i])
	}
	actual := make([]string, len(units))
	for i := range units {
		actual[i] = units[i].name
	}
	sort.Strings(actual)
	sort.Strings(wanted)
	if !reflect.DeepEqual(actual, wanted) {
		return fmt.Errorf("contribution names = %v, want %v", actual, wanted)
	}
	return nil
}

func processorSelectedUnit(units []processorNamedUnit, rawName any) (processorNamedUnit, error) {
	name := fmt.Sprint(rawName)
	selected := []processorNamedUnit{}
	for _, unit := range units {
		if unit.name == name {
			selected = append(selected, unit)
		}
	}
	if len(selected) != 1 {
		return processorNamedUnit{}, fmt.Errorf("expected one %q contribution, got %d", name, len(selected))
	}
	return selected[0], nil
}

func processorPointer(root any, path string) (any, bool, error) {
	if path == "" {
		return root, true, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, false, fmt.Errorf("invalid JSON Pointer %q", path)
	}
	current := root
	for _, raw := range strings.Split(path[1:], "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			var present bool
			current, present = value[segment]
			if !present {
				return nil, false, nil
			}
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false, nil
			}
			current = value[index]
		default:
			return nil, false, nil
		}
	}
	return current, true, nil
}

func processorSetPointer(root map[string]any, path string, replacement any) error {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := any(root)
	for index, raw := range parts {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		if index == len(parts)-1 {
			object, ok := current.(map[string]any)
			if !ok {
				return fmt.Errorf("input materialization path %q is not an object path", path)
			}
			object[part] = replacement
			return nil
		}
		switch value := current.(type) {
		case map[string]any:
			current = value[part]
		case []any:
			position, err := strconv.Atoi(part)
			if err != nil || position < 0 || position >= len(value) {
				return fmt.Errorf("invalid input materialization path %q", path)
			}
			current = value[position]
		default:
			return fmt.Errorf("invalid input materialization path %q", path)
		}
	}
	return nil
}

func processorJSONEqual(left, right any) bool {
	return reflect.DeepEqual(processorNormalized(left), processorNormalized(right))
}

func processorNormalized(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&normalized) != nil {
		return value
	}
	return normalized
}

func processorSetEqual(actual any, expected []any) bool {
	values, ok := processorNormalized(actual).([]any)
	if !ok || len(values) != len(expected) {
		return false
	}
	used := make([]bool, len(values))
	for _, wanted := range expected {
		found := false
		for index, got := range values {
			if !used[index] && processorJSONEqual(got, wanted) {
				used[index], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func processorContains(actual, needle any) bool {
	switch value := processorNormalized(actual).(type) {
	case string:
		text, ok := processorNormalized(needle).(string)
		return ok && strings.Contains(value, text)
	case []any:
		for _, member := range value {
			if processorJSONEqual(member, needle) {
				return true
			}
		}
	case map[string]any:
		key, ok := needle.(string)
		_, present := value[key]
		return ok && present
	}
	return false
}

func processorPrintable(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func processorDecodeJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func processorClone(value any) any {
	raw, _ := json.Marshal(value)
	var cloned any
	_ = processorDecodeJSON(raw, &cloned)
	return cloned
}

func normalizedProcessorHeaders(headers http.Header) map[string]any {
	result := map[string]any{}
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		value := strings.Join(values, ", ")
		result[name] = value
		result[strings.ToLower(name)] = value
	}
	return result
}

func processorStringMap(values map[string]any) map[string]string {
	result := make(map[string]string, len(values))
	for name, value := range values {
		result[name] = fmt.Sprint(value)
	}
	return result
}

func processorInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case json.Number:
		integer, err := typed.Int64()
		return int(integer), err == nil
	case float64:
		return int(typed), typed == float64(int(typed))
	default:
		return 0, false
	}
}

func resourceBytes(value any) []byte {
	if text, ok := value.(string); ok {
		return []byte(text)
	}
	raw, _ := json.Marshal(value)
	return raw
}
