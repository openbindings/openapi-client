# Go OpenAPI client: design

This directory holds the public API of the Go client: the exported
declarations with their documentation, and `Example` functions for every
caller scenario in `design/scenarios.md`. Nothing beneath the API is
implemented yet: the method bodies are stubs, and the examples compile but
do not run. `design/requirements.md` records source material
and candidate rules. Where it differs from the public package documentation,
the package documentation is the current design.
`design/interface-boundary-2026-09-26.md` records the product boundary and
the invocation fallback boundary.

Import path `github.com/openbindings/openapi-client/go/openapi`.

## The model

`Load` (or `Parse`, for bytes) reads a document once into an immutable,
concurrency-safe `Client`, and fails fast on Options the document cannot
use. `Options` (zero value: documented transport defaults) says where calls
go, as whom and how; `With(func(*Options))` derives a Client sharing the document with an
edited private copy. `Call(ctx, key, in, out)` sends an operation named by
operationId or `"METHOD /path"` with an `Input` (parameters by name, body,
per-call settings) and decodes a 2xx from the connection into `out`.
`Stream` leaves a successful body open for `Items[T]`, `Events` or raw reads.
`Prepare` builds an editable `*http.Request` without sending; credentials
are added at send time. `Request.Send` returns every status with an open
response body for a caller's own policy. `Response.Decode` applies the
client's codecs without classifying status; `Response.WaitRequest` reports
the independent completion of a streaming request body. Consequential
ambiguity in the document is reported by `RequestError.Settings` before
dispatch, with alternatives available through `Operation` descriptors.
Exact server and security identifiers handle collisions. There is no choice
record or interaction model.
For `Call` and `Stream`, outcomes are `*RequestError` (API request not sent),
net/http's errors, `*StatusError` (non-2xx), `*DecodeError` (2xx unusable),
and success. `Send` leaves classification to its caller. `Operations` describes
the document, with `Err` and `Source` on its parts. `DocumentURIs` and lazy
`Schema` handles expose authored source and resolved references. The
optional `openapi/schema2020` package offers a directional 2020-12 view
only when conversion is faithful; invocation does not depend on it.

## Where each rule lives

Every rule has one home; other docs point to it.
Package doc: the governing patch of each edition, values, codec classes
and empty values, outcomes and their test order, the required selections
and default part types, serialization rules (URL building, bodies by
method, order, percent-encoding, styles, querystring, forms, Swagger 2.0
arrays, cookies, header fields, content codings), and credentials (naming,
placement, destinations, transport security, refusals). `Redirects`: which
3xx are followed, how method and body change, and what a hop strips or
re-places. `Response.MediaType`: media matching, for requests too.
`Input.Body`: body shapes, replay, lifetime, iterators. `Part`: part names
and filenames. `Options.Variables`: server variable values. `Call` and
`Response.Decode`: decoding by target, and responses without a body.
`Items`: response item framing. `Loader`: document acceptance, which
references are followed, how they resolve, and admission. `SchemeLookup`:
security scheme names. `Load`: fatal defects and the fail-fast Options
list. `SecurityRequirement.Key`: the canonical key. `Operation`: how
descriptions follow references, Path Item `$ref` included. `Flow`: security
URLs. `Client.Document`: the spelling of Source URIs. `Schema`: the handle,
other dialects and raw reference edges. The optional `schema2020` package
owns projection fidelity.

## Implementation scope

The implementation is built as one engine working lazily on the parsed
document, with Swagger 2.0 normalized into the same per-operation plan, no
schema validation, net/http for all HTTP, and a YAML parser as the only
dependency. The description surface mirrors OpenAPI objects so a dynamic
caller, a code generator, or another library can apply its own conventions.

## Implementation order

Each stage lands a user-visible capability with its tests and is reviewed
as code before the next begins.

1. **Vertical slice**: `Load` of a 3.1 JSON document over
   http or from a file, local `$ref` resolution, the operation index
   (operationId and `"METHOD /path"`), one server with variable defaults,
   path and query parameters in their default styles, `Call` decoding JSON
   into a struct, `*any`, `*[]byte` and `io.Writer`, and the outcomes
   (`RequestError`, `StatusError`, `DecodeError`, `Response`) with the
   bounds. Tests against `httptest` servers; a benchmark of `Call` against
   the same call written by hand with net/http (allocations and latency).
2. **Parameters, fully**: every style, `explode`,
   `allowReserved`, content parameters, headers, cookies, 3.2 querystring,
   and the per-parameter writer escape hatch.
3. **Credentials, security and redirects**: explicit and exact alternatives,
   stable server identity, credential placement, the plain-http rule,
   `Redirects` and hop stripping, `FromTransport`.
4. **Request bodies**: JSON, text, form, multipart with
   Encoding and parts, octets, EncodedBody presence, replay rules, 3.2
   sequential and iterator bodies.
5. **Documents**: YAML, references across documents, the
   admission callback, origin and local-file defaults, bounds, `Loader`.
6. **Editions**: Swagger 2.0 normalization and 3.0 and 3.2
   specifics, run against every scenario so far.
7. **Streaming**: `Stream`, `Items`, `Events`, positional
   multipart reading, and independent upload completion.
8. **Prepare and raw send**: `Prepare`, `Request.Send`, `Response.Decode`,
   and refusals that name the setting that fixes them.
9. **Description**: descriptors, loaded-document inventory and lazy raw
   schema graph. A separate optional pass can implement the `schema2020`
   projection helper after the invocation contract is settled.

Stage 1 measures the implementation cost and performance before the rest of
the engine is built.
