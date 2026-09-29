// Package schema2020 offers an optional JSON Schema 2020-12 view of
// OpenAPI schemas. It does not participate in loading or calling operations.
// Callers that need another dialect or cannot use a projection can inspect
// openapi.Schema.Raw, References, Source, Base and Dialect instead.
//
// This package currently specifies a proposed helper API; Project is not
// implemented yet.
package schema2020

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Direction says which use of a schema a projection represents.
type Direction uint8

const (
	Neutral  Direction = iota // preserve authored readOnly/writeOnly annotations
	Request                   // a value the client sends
	Response                  // a value the server sends
)

// Projection is a composable JSON Schema 2020-12 view. Root has no $schema
// or $defs; references in Root and Defs point to #/$defs/KEY. Defs contains
// the complete transitive closure. All three fields are caller-owned
// copies. Keys are stable within one openapi.Client and direction, so
// projections of the same direction may merge their Defs without
// collision. A key may have a different value in another direction.
type Projection struct {
	Root json.RawMessage
	Defs map[string]json.RawMessage

	// Sources maps each Defs key to the openapi.Schema.Source of the
	// authored schema it came from.
	Sources map[string]string
}

// Issue names one schema location that cannot be translated faithfully.
type Issue struct {
	Source string // the openapi.Schema.Source of the containing schema
	At     string // JSON Pointer from that schema's Raw to the problem
	Err    error
}

// Error reports every independently detectable loss in a projection.
// Issues distinguish unresolved references, dynamic scope, and unsupported
// dialect semantics.
type Error struct {
	Issues []Issue
}

// Error names each Source and At with its reason.
func (e *Error) Error() string {
	if e == nil {
		return "openapi/schema2020: nil projection error"
	}
	var parts []string
	for _, issue := range e.Issues {
		parts = append(parts, fmt.Sprintf("%s%s: %v", issue.Source, issue.At, issue.Err))
	}
	return "openapi/schema2020: " + strings.Join(parts, "; ")
}

// Unwrap returns each issue's Err in order.
func (e *Error) Unwrap() []error {
	if e == nil {
		return nil
	}
	var out []error
	for _, issue := range e.Issues {
		if issue.Err != nil {
			out = append(out, issue.Err)
		}
	}
	return out
}

// Project converts a supported schema to JSON Schema 2020-12 for direction.
// It applies the edition's request/response rules for readOnly, writeOnly
// and required, including through references; in Swagger 2.0, whose
// readOnly properties must not be sent, a Request projection forbids them.
// In Swagger 2.0 and OpenAPI 3.0, members beside $ref are dropped, and a
// boolean exclusiveMinimum or exclusiveMaximum becomes a number; OpenAPI
// 3.0 nullable becomes a type list where the schema has a type. In those
// editions format: binary drops type: string, and Swagger 2.0 type: file
// drops its type; for either, the use site's single concrete media type
// becomes contentMediaType at Root only, and Defs entries state none.
// format: byte becomes contentEncoding: base64. It never validates an
// instance or invents schema facts.
//
// Project returns an *Error when a reference cannot be resolved,
// $dynamicRef needs dynamic scope, or a custom dialect or vocabulary cannot
// be translated without loss, and with it the Projection, each Issue's
// site replaced by true, the schema that accepts anything. Callers that
// need the lost parts can use the authored graph through openapi.Schema
// and openapi.Client.DocumentURIs with their own dialect-aware processor.
// Project is lazy and retrieves no documents.
func Project(s *openapi.Schema, direction Direction) (*Projection, error) {
	panic("unimplemented")
}
