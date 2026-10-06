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

The invocation package exposes authored schemas and resolved references.
The optional `openapi/schema2020` package converts any of them to a
standalone JSON Schema 2020-12 schema for a request or a response;
invocation does not depend on it.

## Status

The invocation engine, authored-schema graph and schema projection are
implemented for all four editions, including JSON/YAML loading, parameters, credentials, redirects,
request bodies, response decoding, streaming, prepared requests, descriptors
and loaded-document inventory. The examples document caller flows; executable
tests exercise those flows with synthetic documents and test transports.

Schema projection is separate from invocation, and the client does not
validate values against schemas.
See [DESIGN.md](DESIGN.md) for the model and implementation scope, and
[DEVELOPMENT.md](DEVELOPMENT.md) for the contract and qualification rules.
