package openapiclient

import (
	"context"
	"encoding/json"

	"github.com/getkin/kin-openapi/openapi3"
)

func loadDocumentCompat(location string, content json.RawMessage) (*openapi3.T, error) {
	document, _, err := loadDocument(context.Background(), nil, Source{Location: location, Content: content}, false)
	return document, err
}

func opWithRequestBody(content openapi3.Content, required bool) *openapi3.Operation {
	return &openapi3.Operation{RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{Required: required, Content: content}}}
}
