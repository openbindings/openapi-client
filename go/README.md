# Go OpenAPI client

A client library for every version of OpenAPI: Swagger 2.0 and OpenAPI 3.0,
3.1, and 3.2. Point it at a document and call its operations with ordinary
Go values; no generated code is required.

## Charter

- **Authorities.** The OpenAPI specifications and the RFCs and standards
  they rely on are the library's only authorities. Nothing else defines its
  behavior.
- **Configurability.** Where OpenAPI gives too little guidance for a client
  to function, the library makes a sensible, documented choice and lets the
  caller configure it.
- **Quality bar.** Supremely good developer experience and performance
  within that scope, with the smallest surface and the least code that
  achieve it.

## Status

This branch starts the library over from its public API. The API is
designed first, as exported declarations with documentation and example
callers, and nothing is implemented beneath it until the design is settled.
