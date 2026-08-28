package openapiclient

import (
	"fmt"
	"regexp"
	"sort"
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

// WireMethod returns the HTTP method exactly as the selected operation sends
// it. Fixed Operation fields use their registered uppercase spelling;
// additionalOperations keys retain their authored capitalization.
func (r OperationReference) WireMethod() string {
	if r.Additional {
		return r.Method
	}
	return strings.ToUpper(r.Method)
}

// OperationTarget pairs an operation address with the resolved typed model.
type OperationTarget struct {
	OperationReference
	Document                 *openapi3.T
	PathItem                 *openapi3.PathItem
	Operation                *openapi3.Operation
	ReferringSecuritySchemes openapi3.SecuritySchemes
	ResponseMediaExclusions  []OpenAPI32ResponseMediaExclusion
}

// OpenAPI32ResponseMediaExclusion records one response media alternative
// removed from a target-local typed image because its Media Type or Schema
// reference is unresolvable under OpenAPI 3.2 reference identity. Sibling
// media remain selectable.
type OpenAPI32ResponseMediaExclusion struct {
	ResponseKey string
	MediaType   string
	Reason      string
}

// OperationDisposition is one edition-native inventory position. Target is
// populated when the operation is addressable; Err records the exact
// operation-scoped exclusion otherwise.
type OperationDisposition struct {
	Reference OperationReference
	Target    *OperationTarget
	Err       error
}

// OperationInventory returns every fixed and additional operation position
// observed by the edition-specific artifact model in deterministic order.
// Unlike Artifact.Operations, it retains excluded positions for synthesis
// coverage.
func (a *Artifact) OperationInventory() []OperationDisposition {
	if a == nil || a.Document == nil {
		return nil
	}
	if a.Edition.IsOpenAPI32() && a.openAPI32 != nil {
		references := a.openAPI32.operationReferences()
		result := make([]OperationDisposition, 0, len(references))
		for _, reference := range references {
			target, err := a.ResolveOperation(reference.Ref)
			result = append(result, OperationDisposition{Reference: reference, Target: target, Err: err})
		}
		return result
	}
	if a.Document.Paths == nil {
		return nil
	}
	paths := make([]string, 0, a.Document.Paths.Len())
	for path := range a.Document.Paths.Map() {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var result []OperationDisposition
	for _, path := range paths {
		pathItem := a.Document.Paths.Find(path)
		if pathItem == nil {
			continue
		}
		for _, method := range httpMethods {
			if pathItem.GetOperation(strings.ToUpper(method)) == nil {
				continue
			}
			ref := "#/paths/" + escapeJSONPointerSegment(path) + "/" + method
			reference, _ := parseOperationReference(ref, a.Edition)
			target, err := a.ResolveOperation(ref)
			result = append(result, OperationDisposition{Reference: reference, Target: target, Err: err})
		}
	}
	return result
}

// WithOperationTarget returns an artifact-local prepared view in which one
// already-resolved operation target replaces the corresponding inventory
// entry. It is used by adapters after resolving configuration points such as
// server and security while preserving the same 3.2 raw-resource overlay for
// request planning. The receiver is never mutated.
func (a *Artifact) WithOperationTarget(target *OperationTarget) (*Artifact, error) {
	if a == nil || target == nil || target.Ref == "" || target.Operation == nil {
		return nil, operationResolutionError(OperationReferenceInvalid, "prepared operation target is incomplete")
	}
	copyArtifact := *a
	copyArtifact.operationTargets = make(map[string]*OperationTarget, len(a.operationTargets)+1)
	for ref, current := range a.operationTargets {
		copyArtifact.operationTargets[ref] = current
	}
	copyArtifact.operationTargets[target.Ref] = target
	copyArtifact.operationErrors = make(map[string]error, len(a.operationErrors))
	for ref, current := range a.operationErrors {
		copyArtifact.operationErrors[ref] = current
	}
	delete(copyArtifact.operationErrors, target.Ref)
	return &copyArtifact, nil
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

func requestTargetForEdition(target *OperationTarget, edition Edition) *OperationTarget {
	if target == nil || target.Operation == nil || !edition.IsOpenAPI32() || target.Additional || target.Method != "trace" {
		return target
	}
	operation := *target.Operation
	operation.RequestBody = nil
	requestTarget := *target
	requestTarget.Operation = &operation
	return &requestTarget
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
		return a.validateOpenAPI32Target(target)
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
	return a.validateOpenAPI32Target(&OperationTarget{OperationReference: reference, Document: a.Document, PathItem: pathItem, Operation: operation})
}
