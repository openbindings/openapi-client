package openapiclient

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"strings"
)

func encodeSwagger20URLEncoded(contributions []swagger20WireContribution) []byte {
	parts := make([]string, len(contributions))
	for index, contribution := range contributions {
		parts[index] = swagger20FormEncode(contribution.name)
		if contribution.valuePresent {
			parts[index] += "=" + swagger20FormEncode(contribution.value)
		}
	}
	return []byte(strings.Join(parts, "&"))
}

// swagger20FormEncode is the HTML 4.01 application/x-www-form-urlencoded
// byte algorithm: UTF-8 input, SPACE as '+', and uppercase percent triplets.
func swagger20FormEncode(value string) string {
	const hexadecimal = "0123456789ABCDEF"
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character == ' ':
			result.WriteByte('+')
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9':
			result.WriteByte(character)
		case strings.ContainsRune("-._~", rune(character)):
			result.WriteByte(character)
		default:
			result.WriteByte('%')
			result.WriteByte(hexadecimal[character>>4])
			result.WriteByte(hexadecimal[character&15])
		}
	}
	return result.String()
}

func swagger20SafeMultipartName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character != '\t' && (character < 0x20 || character == 0x7f) {
			return false
		}
	}
	return true
}

func encodeSwagger20Multipart(contributions []swagger20WireContribution, media parsedMediaType, propertyMedia map[string]string) ([]byte, string, error) {
	partMedia := make(map[string]string)
	for _, contribution := range contributions {
		if contribution.parameter == nil || contribution.parameter.typeName != "file" {
			continue
		}
		configured := propertyMedia[contribution.name]
		parsed, err := parseRevision3MediaType(configured)
		if configured == "" || err != nil || parsed.rangeSpecificity != 2 {
			return nil, "", fmt.Errorf("file formData parameter %q requires a concrete configuration.propertyMedia value", contribution.name)
		}
		partMedia[contribution.name] = parsed.canonical
	}

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if boundary, present := media.params["boundary"]; present {
		if err := writer.SetBoundary(boundary); err != nil {
			return nil, "", fmt.Errorf("multipart boundary: %w", err)
		}
	} else {
		for attempt := 0; attempt < 32 && swagger20BoundaryOccurs(writer.Boundary(), contributions); attempt++ {
			writer = multipart.NewWriter(&buffer)
		}
		if swagger20BoundaryOccurs(writer.Boundary(), contributions) {
			return nil, "", fmt.Errorf("could not choose a multipart boundary absent from representation content")
		}
	}
	if swagger20BoundaryOccurs(writer.Boundary(), contributions) {
		return nil, "", fmt.Errorf("multipart boundary occurs in representation content")
	}

	for _, contribution := range contributions {
		headers := make(textproto.MIMEHeader)
		headers.Set("Content-Disposition", `form-data; name="`+swagger20QuoteMultipartName(contribution.name)+`"`)
		content := []byte(contribution.value)
		if contribution.parameter != nil && contribution.parameter.typeName == "file" {
			headers.Set("Content-Type", partMedia[contribution.name])
			content = contribution.octets
		} else {
			headers.Set("Content-Type", "text/plain; charset=utf-8")
		}
		part, err := writer.CreatePart(headers)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(content); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	parameters := make(map[string]string, len(media.renderedParams)+1)
	for name, value := range media.renderedParams {
		parameters[name] = value
	}
	parameters["boundary"] = writer.Boundary()
	contentType := formatHTTPMediaType(media.base, parameters)
	return buffer.Bytes(), contentType, nil
}

func swagger20BoundaryOccurs(boundary string, contributions []swagger20WireContribution) bool {
	needle := []byte(boundary)
	for _, contribution := range contributions {
		if bytes.Contains([]byte(contribution.name), needle) || bytes.Contains([]byte(contribution.value), needle) || bytes.Contains(contribution.octets, needle) {
			return true
		}
	}
	return false
}

func swagger20QuoteMultipartName(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
