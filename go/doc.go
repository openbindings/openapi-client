// Package openapiclient provides a document-driven OpenAPI 3.0 and 3.1
// client and its lower-level execution engine.
//
// The native Client works directly from an OpenAPI artifact: it selects
// authored operations, accepts grouped parameter and body inputs, applies
// scheme-named credentials, returns HTTP-native success or failure results,
// and exposes server-sent events as an ordered cancellable stream. It does
// not require code generation or an OpenBindings Interface.
//
// Engine is the lower-level prepared-operation substrate used by adapters.
// It exposes neutral values, prerequisites, events, diagnostics, and typed
// failures without importing an OpenBindings SDK.
package openapiclient
