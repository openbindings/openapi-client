package openapiclient

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// OpenAPI32ResponseSelection is the artifact-governed response declaration
// for one final HTTP status. A nil Response means that the operation declares
// no exact, range, or default response for that status. Success always follows
// the native final-status 2xx class and is never changed by the declaration.
type OpenAPI32ResponseSelection struct {
	StatusCode  int
	Success     bool
	ResponseKey string
	Response    *openapi3.Response
}

// SelectOpenAPI32Response applies OpenAPI 3.2's exact, class-range, default
// lookup to an already resolved operation target. ResolveOperation performs
// the edition's closed response-key admission before this method can succeed.
func (a *Artifact) SelectOpenAPI32Response(target *OperationTarget, statusCode int) (OpenAPI32ResponseSelection, error) {
	selection := OpenAPI32ResponseSelection{
		StatusCode: statusCode,
		Success:    statusCode >= 200 && statusCode < 300,
	}
	if a == nil || !a.Edition.IsOpenAPI32() {
		return selection, fmt.Errorf("OpenAPI 3.2 response selection requires a 3.2 artifact")
	}
	if target == nil || target.Operation == nil {
		return selection, fmt.Errorf("OpenAPI 3.2 response selection requires a resolved operation target")
	}
	if a.openAPI32 == nil {
		return selection, fmt.Errorf("OpenAPI 3.2 artifact has no raw-resource overlay")
	}
	if err := a.openAPI32.validateSelectedResponseDeclarations(target.OperationReference); err != nil {
		return selection, err
	}
	match := governingResponse(target.Operation, statusCode)
	if match != nil {
		selection.ResponseKey = match.key
		selection.Response = match.response
	}
	return selection, nil
}

func (o *OpenAPI32Overlay) validateSelectedResponseDeclarations(reference OperationReference) error {
	if o == nil {
		return fmt.Errorf("OpenAPI 3.2 artifact has no raw-resource overlay")
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	operationNode, ok := o.selectedRawOperationLocked(reference, map[string]bool{})
	if !ok {
		return nil
	}
	operation, _ := operationNode.value.(map[string]any)
	if operation == nil {
		return fmt.Errorf("operation %q is not an object", reference.Ref)
	}
	rawResponses, present := operation["responses"]
	if !present {
		return nil
	}
	responses, ok := rawResponses.(map[string]any)
	if !ok {
		return fmt.Errorf("operation %q Responses declaration is not an object", reference.Ref)
	}
	if len(responses) == 0 {
		return fmt.Errorf("operation %q has a present empty Responses Object", reference.Ref)
	}
	for key := range responses {
		if !admittedOpenAPI32ResponseKey(key) {
			return fmt.Errorf("operation %q has inadmissible Responses key %q", reference.Ref, key)
		}
	}
	return nil
}

func admittedOpenAPI32ResponseKey(key string) bool {
	if key == "default" || strings.HasPrefix(key, "x-") {
		return true
	}
	if len(key) != 3 || key[0] < '1' || key[0] > '5' {
		return false
	}
	if key[1] == 'X' && key[2] == 'X' {
		return true
	}
	return key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9'
}
