package openapiclient

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// QUERY is the OpenAPI 3.2 fixed QUERY operation selector value. It is kept
// out of the legacy httpMethods table.
const QUERY Method = "query"

var openAPI32FixedMethods = []string{
	"get", "put", "post", "delete", "options", "head", "patch", "trace", "query",
}

var openAPI32HTTPToken = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

type OperationResolutionKind string

const (
	OperationReferenceInvalid OperationResolutionKind = "invalid-reference"
	OperationTargetNotFound   OperationResolutionKind = "not-found"
	OperationTargetExcluded   OperationResolutionKind = "excluded"
)

// OperationResolutionError classifies selector grammar, lookup, and
// edition-authoritative exclusion outcomes without imposing OBI vocabulary.
type OperationResolutionError struct {
	Kind    OperationResolutionKind
	Message string
	Cause   error
}

func (e *OperationResolutionError) Error() string { return e.Message }
func (e *OperationResolutionError) Unwrap() error { return e.Cause }

func operationResolutionError(kind OperationResolutionKind, format string, args ...any) error {
	return &OperationResolutionError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// OperationReference is the parsed OpenAPI-native address of one operation.
type OperationReference struct {
	Ref        string
	Path       string
	Method     string
	Additional bool
}

// OperationTarget pairs an operation address with the resolved typed model.
type OperationTarget struct {
	OperationReference
	Document  *openapi3.T
	PathItem  *openapi3.PathItem
	Operation *openapi3.Operation
}

// AdditionalOperation creates a selector for a 3.2 additionalOperations key.
func AdditionalOperation(path, method string) OperationSelector {
	return OperationSelector{path: path, additionalMethod: method}
}

func parseOperationReference(ref string, edition Edition) (OperationReference, error) {
	const prefix = "#/paths/"
	if !strings.HasPrefix(ref, prefix) {
		return OperationReference{}, operationResolutionError(OperationReferenceInvalid, "operation ref %q must begin #/paths/", ref)
	}
	parts := strings.Split(ref[len(prefix):], "/")
	if edition.IsOpenAPI32() && len(parts) == 3 && parts[1] == "additionalOperations" {
		if !wellFormedPointerToken(parts[0]) || !wellFormedPointerToken(parts[2]) {
			return OperationReference{}, operationResolutionError(OperationReferenceInvalid, "operation ref %q is not a well-formed RFC 6901 pointer", ref)
		}
		method := unescapeJSONPointerSegment(parts[2])
		if method == "" || !openAPI32HTTPToken.MatchString(method) {
			return OperationReference{}, operationResolutionError(OperationReferenceInvalid, "additional operation method %q is not an HTTP token", method)
		}
		for _, fixed := range openAPI32FixedMethods {
			if strings.EqualFold(method, fixed) {
				return OperationReference{}, operationResolutionError(OperationTargetExcluded, "additional operation method %q collides with fixed operation field %q", method, fixed)
			}
		}
		path := unescapeJSONPointerSegment(parts[0])
		return OperationReference{Ref: ref, Path: path, Method: method, Additional: true}, nil
	}
	if len(parts) != 2 || !wellFormedPointerToken(parts[0]) || !wellFormedPointerToken(parts[1]) {
		return OperationReference{}, operationResolutionError(OperationReferenceInvalid, "operation ref %q must address one escaped path and operation field", ref)
	}
	method := parts[1]
	valid := validRefMethods[method]
	if edition.IsOpenAPI32() && method == "query" {
		valid = true
	}
	if !valid {
		return OperationReference{}, operationResolutionError(OperationReferenceInvalid, "invalid fixed operation field %q in ref", method)
	}
	return OperationReference{Ref: ref, Path: unescapeJSONPointerSegment(parts[0]), Method: method}, nil
}

// ParseOperationReference validates and parses an operation selector under an
// exact OpenAPI edition without requiring a loaded document.
func ParseOperationReference(ref string, edition Edition) (OperationReference, error) {
	return parseOperationReference(ref, edition)
}

// ResolveOperation resolves an exact selector under the artifact's edition.
func (a *Artifact) ResolveOperation(ref string) (*OperationTarget, error) {
	if a == nil || a.Document == nil {
		return nil, operationResolutionError(OperationTargetNotFound, "OpenAPI artifact is nil")
	}
	if a.sourceRefusal != "" {
		return nil, operationResolutionError(OperationTargetExcluded, "OpenAPI artifact is refused: %s", a.sourceRefusal)
	}
	if a.sourceExclusion != "" {
		return nil, operationResolutionError(OperationTargetExcluded, "OpenAPI artifact is excluded: %s", a.sourceExclusion)
	}
	reference, err := parseOperationReference(ref, a.Edition)
	if err != nil {
		return nil, err
	}
	if target := a.operationTargets[reference.Ref]; target != nil {
		if target.Operation.Responses != nil && target.Operation.Responses.Len() == 0 {
			return nil, operationResolutionError(OperationTargetExcluded, "operation %q has a present empty Responses Object", ref)
		}
		return target, nil
	}
	if resolutionErr := a.operationErrors[reference.Ref]; resolutionErr != nil {
		return nil, resolutionErr
	}
	if a.Edition.IsOpenAPI32() {
		if a.openAPI32 == nil {
			return nil, operationResolutionError(OperationTargetExcluded, "OpenAPI 3.2 artifact has no raw-resource overlay")
		}
		_, _, err = a.openAPI32.selectedPathItem(reference.Path, reference.Method, reference.Additional)
		if err != nil {
			return nil, &OperationResolutionError{Kind: OperationTargetExcluded, Message: err.Error(), Cause: err}
		}
	}
	if a.Document.Paths == nil {
		return nil, operationResolutionError(OperationTargetNotFound, "OpenAPI document has no paths defined")
	}
	pathItem := a.Document.Paths.Find(reference.Path)
	if pathItem == nil {
		return nil, operationResolutionError(OperationTargetNotFound, "path %q not in OpenAPI document", reference.Path)
	}
	operation := operationFromPathItem(pathItem, reference)
	if operation == nil {
		return nil, operationResolutionError(OperationTargetNotFound, "operation %q was not found", ref)
	}
	if a.Edition.IsOpenAPI32() && operation.Responses != nil && operation.Responses.Len() == 0 {
		return nil, operationResolutionError(OperationTargetExcluded, "operation %q has a present empty Responses Object", ref)
	}
	return &OperationTarget{OperationReference: reference, Document: a.Document, PathItem: pathItem, Operation: operation}, nil
}
