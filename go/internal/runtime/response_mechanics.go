package openapiclient

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

func normalizeContentEncoders(input map[string]ContentEncoder) (map[string]ContentEncoder, error) {
	result := make(map[string]ContentEncoder, len(input))
	for token, codec := range input {
		normalized := strings.ToLower(strings.TrimSpace(token))
		if codec == nil {
			return nil, fmt.Errorf("invalid request content-coding capability %q", token)
		}
		if _, duplicate := result[normalized]; duplicate {
			return nil, fmt.Errorf("request content-coding capabilities collide at %q", normalized)
		}
		result[normalized] = codec
	}
	return result, nil
}

func normalizeContentDecoders(input map[string]ContentDecoder) (map[string]ContentDecoder, error) {
	result := make(map[string]ContentDecoder, len(input))
	for token, codec := range input {
		normalized := strings.ToLower(strings.TrimSpace(token))
		if codec == nil {
			return nil, fmt.Errorf("invalid response content-coding capability %q", token)
		}
		if _, duplicate := result[normalized]; duplicate {
			return nil, fmt.Errorf("response content-coding capabilities collide at %q", normalized)
		}
		result[normalized] = codec
	}
	return result, nil
}

func parsedContentCodings(raw string) ([]string, error) {
	members, err := splitHTTPList(raw)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(members))
	for index, member := range members {
		token := strings.ToLower(strings.TrimSpace(member))
		if !httpToken(token) {
			return nil, fmt.Errorf("content-coding %q is not an HTTP token", member)
		}
		result[index] = token
	}
	return result, nil
}

func applyRequestContentCodings(request *http.Request, parameters openapi3.Parameters, edition string, codecs map[string]ContentEncoder) error {
	raw := strings.Join(request.Header.Values("Content-Encoding"), ",")
	if raw == "" {
		return nil
	}
	if (request.Body == nil || request.Body == http.NoBody) && request.Header.Get("Content-Type") == "" {
		return fmt.Errorf("request Content-Encoding cannot be supplied when the invocation emits no request representation")
	}
	parameter := effectiveContentEncodingParameter(parameters)
	if parameter == nil {
		return fmt.Errorf("request Content-Encoding has no effective governing Header Parameter")
	}
	if !schemaAdmitsHeaderValue(parameter.Schema, raw, edition) {
		return fmt.Errorf("request Content-Encoding is not admitted by its governing Header Parameter")
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

func effectiveContentEncodingParameter(parameters openapi3.Parameters) *openapi3.Parameter {
	var found *openapi3.Parameter
	for _, ref := range parameters {
		if ref == nil || ref.Value == nil || ref.Value.In != openapi3.ParameterInHeader || !strings.EqualFold(ref.Value.Name, "Content-Encoding") {
			continue
		}
		if found != nil {
			return nil
		}
		found = ref.Value
	}
	return found
}

func schemaAdmitsHeaderValue(ref *openapi3.SchemaRef, value, edition string) bool {
	if ref == nil || ref.Value == nil {
		return true
	}
	declaration := resolveDeclaration(ref.Value, strings.HasPrefix(edition, "3.0."))
	if declaration.ambiguous || (!declaration.typeless() && !declaration.admitsStringAsSoleNonNullType()) {
		return false
	}
	for _, conjunct := range declaration.conjuncts {
		if len(conjunct.Enum) == 0 {
			continue
		}
		admitted := false
		for _, candidate := range conjunct.Enum {
			if text, ok := candidate.(string); ok && text == value {
				admitted = true
				break
			}
		}
		if !admitted {
			return false
		}
	}
	return true
}

func readRequestBody(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(request.Body)
	_ = request.Body.Close()
	return body, err
}

func replaceRequestBody(request *http.Request, body []byte) {
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

func applyResponseMechanics(request *http.Request, response *http.Response, document *openapi3.T, operation *openapi3.Operation, profile string, codecs map[string]ContentDecoder, bufferEventStreams bool) (*http.Response, error) {
	if response == nil {
		return response, nil
	}
	if response.StatusCode == http.StatusSwitchingProtocols {
		closeResponse(response)
		return nil, fmt.Errorf("101 Switching Protocols is outside this binding's HTTP invocation model")
	}
	governing := governingResponse(operation, response.StatusCode)
	if governing != nil {
		if err := requireGovernedResponseHeaders(governing.response, response.Header); err != nil {
			closeResponse(response)
			return nil, err
		}
	}
	rawCoding := strings.Join(response.Header.Values("Content-Encoding"), ",")
	noContent := responseHasNoContentSemantics(request.Method, response.StatusCode)
	buffer := bufferEventStreams || rawCoding != "" || noContent
	if !buffer {
		return response, nil
	}
	body, err := readResponseBody(response)
	if err != nil {
		return nil, err
	}
	if rawCoding != "" {
		if governing != nil {
			edition := ""
			if document != nil {
				edition = document.OpenAPI
			}
			if !responseContentEncodingAdmitted(governing.response, rawCoding, edition) {
				return nil, fmt.Errorf("actual response Content-Encoding is not admitted by every governing Header Object")
			}
		}
		tokens, parseErr := parsedContentCodings(rawCoding)
		if parseErr != nil {
			return nil, parseErr
		}
		// HEAD and the status codes below carry representation metadata but no
		// response content. Validate the field grammar and governing finite
		// domain, but do not require or run a content decoder for bytes that
		// HTTP says cannot exist.
		if noContent {
			if len(body) != 0 {
				return nil, fmt.Errorf("HTTP response to %s with status %d carries forbidden content", request.Method, response.StatusCode)
			}
			replaceResponseBody(response, nil)
			return response, nil
		}
		for index := len(tokens) - 1; index >= 0; index-- {
			token := tokens[index]
			if token == "identity" {
				continue
			}
			codec := codecs[token]
			if codec == nil {
				return nil, fmt.Errorf("response content-coding %q is unsupported", token)
			}
			body, parseErr = codec(body)
			if parseErr != nil {
				return nil, fmt.Errorf("response content-coding %q failed: %w", token, parseErr)
			}
		}
	}
	if noContent {
		if len(body) != 0 {
			return nil, fmt.Errorf("HTTP response to %s with status %d carries forbidden content", request.Method, response.StatusCode)
		}
		replaceResponseBody(response, nil)
		return response, nil
	}
	if len(body) > 0 {
		if governing == nil {
			return nil, fmt.Errorf("non-empty response has no governing Response Object")
		}
		contentType := response.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
			response.Header.Set("Content-Type", contentType)
		}
		match, matchErr := governingResponseMediaMatchFor(governing.response, contentType, profile)
		if matchErr != nil {
			return nil, matchErr
		}
		if laneErr := validateResponseMediaLane(document, match.media, contentType, profile); laneErr != nil {
			return nil, laneErr
		}
	}
	replaceResponseBody(response, body)
	return response, nil
}

func responseContentEncodingAdmitted(response *openapi3.Response, value, edition string) bool {
	if response == nil {
		return true
	}
	is30 := strings.HasPrefix(edition, "3.0.")
	for name, ref := range response.Headers {
		if !strings.EqualFold(name, "Content-Encoding") || ref == nil || ref.Value == nil {
			continue
		}
		domain, known := exactHeaderStringDomain(ref.Value.Schema, is30)
		if known && !domain[value] {
			return false
		}
	}
	return true
}

func responseHasNoContentSemantics(method string, status int) bool {
	return method == http.MethodHead || (status >= 100 && status < 200) || status == http.StatusNoContent || status == http.StatusResetContent || status == http.StatusNotModified
}

func requireGovernedResponseHeaders(response *openapi3.Response, actual http.Header) error {
	if response == nil {
		return nil
	}
	for name, ref := range response.Headers {
		if strings.EqualFold(name, "Content-Type") || ref == nil || ref.Value == nil || !ref.Value.Required {
			continue
		}
		if len(actual.Values(name)) == 0 {
			return fmt.Errorf("required response header %q is absent", name)
		}
	}
	return nil
}

func responseHeader(response *openapi3.Response, name string) *openapi3.Header {
	if response == nil {
		return nil
	}
	var found *openapi3.Header
	for declared, ref := range response.Headers {
		if !strings.EqualFold(declared, name) || ref == nil || ref.Value == nil {
			continue
		}
		if found != nil {
			return nil
		}
		found = ref.Value
	}
	return found
}

func readResponseBody(response *http.Response) ([]byte, error) {
	if response.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	return body, err
}

func replaceResponseBody(response *http.Response, body []byte) {
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func closeResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
}

func validateResponseMediaLane(document *openapi3.T, media *openapi3.MediaType, contentType, profile string) error {
	parsed, err := parseRevision3MediaType(contentType)
	if err != nil {
		return err
	}
	if document != nil && strings.HasPrefix(document.OpenAPI, "3.2.") {
		kind, sequentialErr := ClassifyOpenAPI32SequentialResponse(contentType, media)
		if sequentialErr != nil {
			return sequentialErr
		}
		if kind != "" {
			if kind == OpenAPI32SequentialSSE {
				if charset, present := parsed.params["charset"]; present && !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "utf8") {
					return fmt.Errorf("text/event-stream requires UTF-8, not charset %q", charset)
				}
			}
			return nil
		}
	}
	if isJSONMediaType(parsed.base) {
		return nil
	}
	if parsed.base == "text/event-stream" {
		if charset, present := parsed.params["charset"]; present && !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "utf8") {
			return fmt.Errorf("text/event-stream requires UTF-8, not charset %q", charset)
		}
	}
	oas30 := document != nil && isOpenAPI30(majorMinor(document.OpenAPI))
	declaration := resolveDeclaration(mediaSchema(media), oas30)
	if isCharacterDataMedia(parsed.base) && declaration.admitsStringAsSoleNonNullType() {
		return supportedTextCharset(parsed)
	}
	if declaration.typeless() {
		return nil
	}
	if declaration.admitsStringAsSoleNonNullType() {
		format, conflict := declaration.format()
		if conflict {
			return fmt.Errorf("response declaration has conflicting format annotations")
		}
		encoding, conflict := declaration.keywordString("contentEncoding")
		if conflict {
			return fmt.Errorf("response declaration has conflicting contentEncoding annotations")
		}
		if (oas30 && (format == "binary" || format == "byte")) || (!oas30 && encoding != "") {
			return nil
		}
	}
	return fmt.Errorf("response media %q and its resolved declaration select no incorporated carriage lane", contentType)
}
