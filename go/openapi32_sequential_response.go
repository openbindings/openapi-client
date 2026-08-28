package openapiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// OpenAPI32SequentialKind identifies one of the response item-framing forms
// incorporated by OpenAPI 3.2. The empty value denotes a unary media lane.
type OpenAPI32SequentialKind string

const (
	OpenAPI32SequentialJSONLines OpenAPI32SequentialKind = "json-lines"
	OpenAPI32SequentialJSONSeq   OpenAPI32SequentialKind = "json-seq"
	OpenAPI32SequentialSSE       OpenAPI32SequentialKind = "sse"
	OpenAPI32SequentialMultipart OpenAPI32SequentialKind = "multipart"
)

// ClassifyOpenAPI32SequentialResponse reports the framing selected by one
// concrete response media type and its governing Media Type Object. A
// declaration carrying itemSchema on a media type with no incorporated
// framing is rejected rather than treated as unary or sniffed.
func ClassifyOpenAPI32SequentialResponse(mediaType string, media *openapi3.MediaType) (OpenAPI32SequentialKind, error) {
	parsed, err := parseRevision3MediaType(mediaType)
	if err != nil {
		return "", err
	}
	switch {
	case parsed.base == "application/jsonl", parsed.base == "application/x-ndjson":
		return OpenAPI32SequentialJSONLines, nil
	case parsed.base == "application/json-seq", strings.HasSuffix(parsed.base, "+json-seq"):
		return OpenAPI32SequentialJSONSeq, nil
	case parsed.base == "text/event-stream":
		return OpenAPI32SequentialSSE, nil
	case strings.HasPrefix(parsed.base, "multipart/") && openAPI32PositionalMultipart(media, nil):
		return OpenAPI32SequentialMultipart, nil
	case media != nil && media.ItemSchema != nil:
		return "", fmt.Errorf("response media %q declares itemSchema but has no incorporated sequential framing", parsed.base)
	default:
		return "", nil
	}
}

func streamOpenAPI32Sequential(ctx context.Context, response *http.Response, args *executionArgs, site HookSite, inv executionHandle[any, any], kind OpenAPI32SequentialKind, media *openapi3.MediaType) {
	switch kind {
	case OpenAPI32SequentialSSE:
		streamSSE(ctx, response, args, site, inv, true)
	case OpenAPI32SequentialJSONLines:
		streamOpenAPI32JSONLines(ctx, response, args, inv)
	case OpenAPI32SequentialJSONSeq:
		streamOpenAPI32JSONSequence(ctx, response, args, inv)
	case OpenAPI32SequentialMultipart:
		streamOpenAPI32Multipart(ctx, response, args, inv, media)
	default:
		_ = response.Body.Close()
		inv.failExecution(&ExecutionError{Code: CodeProtocol, Message: fmt.Sprintf("unknown OpenAPI 3.2 sequential response kind %q", kind)})
	}
}

func streamOpenAPI32JSONLines(ctx context.Context, response *http.Response, args *executionArgs, inv executionHandle[any, any]) {
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	configureSequentialScanner(scanner, args.DeliveryUnitLimit())
	index := 0
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		item := bytes.TrimSuffix(scanner.Bytes(), []byte{'\r'})
		if int64(len(item)) > args.DeliveryUnitLimit() {
			failOpenAPI32Sequential(inv, fmt.Errorf("sequential response item exceeds %d byte limit", args.DeliveryUnitLimit()))
			return
		}
		if len(item) == 0 {
			failOpenAPI32Sequential(inv, fmt.Errorf("JSON Lines item %d is empty", index))
			return
		}
		value, err := decodeOpenAPI32JSONItem(item, index, "JSON Lines")
		if err != nil {
			failOpenAPI32Sequential(inv, err)
			return
		}
		if err := inv.emitOutput(value); err != nil {
			return
		}
		index++
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return
		}
		failOpenAPI32Sequential(inv, sequentialScannerError(err, args.DeliveryUnitLimit()))
		return
	}
	inv.closeOutputBoundary()
}

func streamOpenAPI32JSONSequence(ctx context.Context, response *http.Response, args *executionArgs, inv executionHandle[any, any]) {
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	configureSequentialScanner(scanner, args.DeliveryUnitLimit())
	scanner.Split(scanOpenAPI32JSONSequence)
	index := 0
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		item := bytes.TrimSuffix(scanner.Bytes(), []byte{'\n'})
		item = bytes.TrimSuffix(item, []byte{'\r'})
		if int64(len(item)) > args.DeliveryUnitLimit() {
			failOpenAPI32Sequential(inv, fmt.Errorf("sequential response item exceeds %d byte limit", args.DeliveryUnitLimit()))
			return
		}
		if len(bytes.TrimSpace(item)) == 0 {
			failOpenAPI32Sequential(inv, fmt.Errorf("JSON text sequence item %d is empty", index))
			return
		}
		value, err := decodeOpenAPI32JSONItem(item, index, "JSON text sequence")
		if err != nil {
			failOpenAPI32Sequential(inv, err)
			return
		}
		if err := inv.emitOutput(value); err != nil {
			return
		}
		index++
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return
		}
		failOpenAPI32Sequential(inv, sequentialScannerError(err, args.DeliveryUnitLimit()))
		return
	}
	inv.closeOutputBoundary()
}

func scanOpenAPI32JSONSequence(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if len(data) == 0 {
		return 0, nil, nil
	}
	if data[0] != 0x1e {
		return 0, nil, fmt.Errorf("JSON text sequence does not begin with RS")
	}
	if next := bytes.IndexByte(data[1:], 0x1e); next >= 0 {
		return next + 1, data[1 : next+1], nil
	}
	if atEOF {
		return len(data), data[1:], nil
	}
	return 0, nil, nil
}

func streamOpenAPI32Multipart(ctx context.Context, response *http.Response, args *executionArgs, inv executionHandle[any, any], media *openapi3.MediaType) {
	defer response.Body.Close()
	_, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || parameters["boundary"] == "" {
		failOpenAPI32Sequential(inv, fmt.Errorf("positional multipart response has no valid boundary"))
		return
	}
	reader := multipart.NewReader(response.Body, parameters["boundary"])
	for index := 0; ; index++ {
		if ctx.Err() != nil {
			return
		}
		part, partErr := reader.NextRawPart()
		if partErr == io.EOF {
			inv.closeOutputBoundary()
			return
		}
		if partErr != nil {
			failOpenAPI32Sequential(inv, fmt.Errorf("positional multipart item %d framing: %w", index, partErr))
			return
		}
		body, readErr := readBoundedSequentialItem(part, args.DeliveryUnitLimit())
		_ = part.Close()
		if readErr != nil {
			failOpenAPI32Sequential(inv, fmt.Errorf("positional multipart item %d: %w", index, readErr))
			return
		}
		body, err = decodeMultipartTransferEncoding(body, part.Header.Get("Content-Transfer-Encoding"))
		if err != nil {
			failOpenAPI32Sequential(inv, fmt.Errorf("positional multipart item %d: %w", index, err))
			return
		}
		itemSchema := openAPI32PositionalItemSchema(media, index)
		contentType := part.Header.Get("Content-Type")
		if contentType == "" {
			if inferred, ok := defaultRevision3PartContentType(itemSchema, false); ok {
				contentType = inferred
			} else {
				contentType = "application/octet-stream"
			}
		}
		value, err := decodeOpenAPI32SequentialPart(contentType, body, itemSchema)
		if err != nil {
			failOpenAPI32Sequential(inv, fmt.Errorf("positional multipart item %d: %w", index, err))
			return
		}
		if err := inv.emitOutput(value); err != nil {
			return
		}
	}
}

func configureSequentialScanner(scanner *bufio.Scanner, maxUnit int64) {
	maximum := int64(int(^uint(0) >> 1))
	if maxUnit < maximum-2 {
		maximum = maxUnit + 2
	}
	initial := int64(64 * 1024)
	if maximum < initial {
		initial = maximum
	}
	if initial < 1 {
		initial = 1
	}
	scanner.Buffer(make([]byte, int(initial)), int(maximum))
}

func sequentialScannerError(err error, maxUnit int64) error {
	if errorsIsScannerTooLong(err) {
		return fmt.Errorf("sequential response item exceeds %d byte limit", maxUnit)
	}
	return err
}

// bufio.Scanner exposes ErrTooLong only through its error text.
func errorsIsScannerTooLong(err error) bool {
	return err != nil && strings.Contains(err.Error(), "token too long")
}

func decodeOpenAPI32JSONItem(item []byte, index int, framing string) (any, error) {
	var value any
	if err := json.Unmarshal(item, &value); err != nil {
		return nil, fmt.Errorf("%s item %d is malformed JSON: %w", framing, index, err)
	}
	return value, nil
}

func readBoundedSequentialItem(reader io.Reader, maxUnit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxUnit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxUnit {
		return nil, fmt.Errorf("sequential response item exceeds %d byte limit", maxUnit)
	}
	return body, nil
}

func decodeMultipartTransferEncoding(body []byte, coding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(coding)) {
	case "", "binary", "7bit", "8bit":
		return body, nil
	case "base64":
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(body)))
		if err != nil {
			return nil, fmt.Errorf("invalid base64 Content-Transfer-Encoding: %w", err)
		}
		return decoded, nil
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		if err != nil {
			return nil, fmt.Errorf("invalid quoted-printable Content-Transfer-Encoding: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("unsupported Content-Transfer-Encoding %q", coding)
	}
}

func openAPI32PositionalItemSchema(media *openapi3.MediaType, index int) *openapi3.Schema {
	if media == nil {
		return nil
	}
	if media.ItemSchema != nil && media.ItemSchema.Value != nil {
		return media.ItemSchema.Value
	}
	root := mediaSchema(media)
	if root == nil {
		return nil
	}
	if index < len(root.PrefixItems) && root.PrefixItems[index] != nil {
		return root.PrefixItems[index].Value
	}
	if root.Items != nil {
		return root.Items.Value
	}
	return nil
}

func decodeOpenAPI32SequentialPart(contentType string, body []byte, schema *openapi3.Schema) (any, error) {
	parsed, err := parseRevision3MediaType(contentType)
	if err != nil {
		return nil, err
	}
	if isJSONMediaType(parsed.base) {
		var value any
		if err := json.Unmarshal(body, &value); err != nil {
			return nil, fmt.Errorf("part declares %q but is not valid JSON: %w", contentType, err)
		}
		return value, nil
	}
	if isCharacterDataMedia(parsed.base) {
		return decodeTextLaneFor(contentType, body, profileFullCoordinate)
	}
	if resolveDeclaration(schema, false).typeless() {
		return base64.StdEncoding.EncodeToString(body), nil
	}
	return nil, fmt.Errorf("part media %q and its resolved declaration select no incorporated carriage lane", contentType)
}

func failOpenAPI32Sequential(inv executionHandle[any, any], err error) {
	inv.failExecution(&ExecutionError{Code: CodeResponseError, Message: err.Error(), Cause: err})
}
