package openapiclient

// The standalone engine's half of Round R's per-target restriction. The
// mechanism, its authority, and why it needs no emission gate are stated once
// in `target_restriction.go`, which is the twin the adapter carries; this file
// is the part only this engine has a use for.
//
// This engine resolves ONE target at a time (`Artifact.ResolveOperation`), and
// it already carries a per-target document map for the 3.2 lane
// (`operationTargets` / `operationErrors`, populated by
// `buildOpenAPI32Fallback`). Those maps are consulted before any edition gate,
// so filling them from the 3.0/3.1 restriction needs no new plumbing and gives
// each excluded target its own reason instead of one document-wide error.

import (
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
)

// restrictedTargets is the per-target load's result: the targets that loaded,
// the resolution errors of those that did not, and one loaded document to carry
// as the artifact's typed image.
type restrictedTargets struct {
	document *openapi3.T
	targets  map[string]*OperationTarget
	errors   map[string]error
	used     bool
}

// buildRestrictedTargets loads each operation the acceptance floor's raw
// inventory names, in isolation.
//
// The floor's inventory is the right roster because it is computed over the
// artifact's own raw image and therefore knows every operation position the
// document declares, including the ones the failed typed load never produced.
// It is used as an INVENTORY only: which operation this pass excludes is
// decided by whether that operation's own image loads, never by the floor's
// ladder verdict, so this pass adds no exclusion the floor did not already own.
func buildRestrictedTargets(root map[string]any, edition Edition, floor *acceptanceFloor, load func([]byte) (*openapi3.T, error)) restrictedTargets {
	result := restrictedTargets{targets: map[string]*OperationTarget{}, errors: map[string]error{}}
	if root == nil || floor == nil || load == nil {
		return result
	}
	admitted := restrictedResponseDefectiveTargets(floor)
	for _, ref := range floor.OpOrder {
		op := floor.Ops[ref]
		if op == nil {
			continue
		}
		reference, err := parseOperationReference(ref, edition)
		if err != nil {
			continue
		}
		image, rendered := restrictedOperationImage(root, op.Path, op.Method)
		if !rendered {
			continue
		}
		document, loadErr := load(image)
		switch {
		case loadErr != nil:
			if !admitted[ref] {
				// A failure this pass has no ruling about; see
				// `target_restriction.go`. It decides nothing and declines whole.
				return restrictedTargets{targets: map[string]*OperationTarget{}, errors: map[string]error{}}
			}
			result.errors[ref] = &OperationResolutionError{
				Kind:    OperationTargetExcluded,
				Message: fmt.Sprintf("selected operation closure is unresolvable: %v", loadErr),
				Cause:   loadErr,
			}
			continue
		}
		pathItem := document.Paths.Find(reference.Path)
		if pathItem == nil {
			result.errors[ref] = operationResolutionError(OperationTargetExcluded, "selected operation closure produced no path %q", reference.Path)
			continue
		}
		operation := operationFromPathItem(pathItem, reference)
		if operation == nil {
			result.errors[ref] = operationResolutionError(OperationTargetExcluded, "selected operation closure produced no operation %q", ref)
			continue
		}
		result.targets[ref] = &OperationTarget{OperationReference: reference, Document: document, PathItem: pathItem, Operation: operation}
		if result.document == nil {
			result.document = document
		}
	}
	result.used = len(result.targets) > 0 || len(result.errors) > 0
	return result
}
