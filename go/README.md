# Go OpenAPI client

A client library for every version of OpenAPI: Swagger 2.0 and OpenAPI 3.0,
3.1, and 3.2. Point it at a document and call its operations with ordinary
Go values; no generated code is required. The callable operations are the
outbound ones under `paths`; webhook and callback declarations remain
available in the authored documents.

## Charter

- **Authorities.** The OpenAPI specifications and the RFCs and standards
  they rely on are the library's only authorities. Nothing else defines its
  behavior.
- **Configurability.** The library follows OpenAPI and HTTP rules where they
  determine behavior. When a consequential alternative remains open, the
  caller selects it through ordinary options or per-call input. Raw request
  and response paths allow policies above the client.
- **Quality bar.** Supremely good developer experience and performance
  within that scope, with the smallest surface and the least code that
  achieve it.

Optional schema conversion lives under `openapi/schema2020`; the invocation
package exposes authored schemas and does not depend on that conversion.

## Status

This branch starts the library over from its public API. The package holds
the exported declarations, their documentation, and compiling examples;
the method bodies are stubs. See `DESIGN.md`.
