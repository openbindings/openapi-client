# Go OpenAPI client: design

This directory holds the Go invocation client, its public contract and tests.
The client covers loading, calls, prepared requests, streaming, descriptors
and the lazy authored-schema graph. `Example` functions
document caller scenarios in `design/scenarios.md`; executable tests exercise
the corresponding flows. The optional `schema2020.Project` helper remains
unimplemented and is outside the invocation engine.

`design/requirements.md` records source material and candidate rules. Where
it differs from the public package documentation, the package documentation
is the current design.
`design/boundary.md` records the product boundary and the escape hatches
that reach every valid use of each edition.

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
optional `openapi/schema2020` contract reserves a directional 2020-12 view
only when conversion is faithful; its implementation is separate,
and invocation does not depend on it.

## Where each rule lives

Every rule has one home; other docs point to it.
Package doc: the governing patch of each edition, values (absent and
typed nil), codec classes and empty values, outcomes and their test order,
the required selections, client-wide preferences against exact per-call
selections, and default part types, serialization rules (URL building,
bodies by method, order, percent-encoding, styles, querystring, forms,
Swagger 2.0 arrays, cookies, header fields, content codings), and
credentials (naming, placement, destinations, transport security,
refusals). `Options.Codecs`: codec keys and where a caller's codec
applies. `Redirects`: which 3xx are followed, how method and body change,
and what a hop strips or re-places. `Response.Declaration` and
`Response.Media`: the governing response and media matching, for requests
too. `Input.Body`: body shapes, what is pre-encoded, replay, lifetime,
iterators. `Part`: part names and filenames. `Options.Variables`: server
variable values. `Call` and `Response.Decode`: decoding by target, the
representation a typed target needs, and responses without a body.
`Items`: response item framing. `RequestError.Settings` and `Inputs`: the
refusal key grammar. `Loader`: document acceptance, which references are
followed, how they resolve, and admission. `SchemeLookup`: security scheme
names. `Load`: fatal defects and the fail-fast Options list; `With`: which
of those checks a derived Client skips. `SecurityRequirement.Key`: the
canonical key. `Operation`: how descriptions follow references, Path Item
`$ref` included. `Flow`: security URLs. `Client.Document`: the spelling of
Source URIs and reading one node. `Schema`: the handle, other dialects;
`SchemaReference`: reference edges, discriminator mappings included. The
optional `schema2020` package owns projection fidelity.

## Implementation scope

The implementation is built as one engine working lazily on the parsed
document, with Swagger 2.0 normalized into the same per-operation plan, no
schema validation, net/http for all HTTP, and a YAML parser as the only
dependency. The description surface mirrors OpenAPI objects so a dynamic
caller, a code generator, or another library can apply its own conventions.

## Capabilities

The invocation engine covers these capabilities, each with independent
contract tests. The optional schema projection is separate.

1. **Vertical slice**: `Load` and `Parse` of 3.1 JSON documents
   with local references, the operation index and descriptors, servers and
   the URL rule, path, query and header parameters in their default
   styles, JSON and pre-encoded bodies, caller codecs, `Call`, `Prepare`,
   `Request.Send`, `Response.Decode`, `WaitRequest`, the outcomes and
   bounds, and `With`, with benchmarks against hand-written net/http.
2. **Parameters, fully** (for the editions that load): every style and
   `explode` value, `allowReserved`, content parameters, header and
   cookie parameters, and the per-parameter writer escape hatch. The
   OpenAPI 3.2 querystring and cookie style, 3.2's wider `allowReserved`,
   and Swagger 2.0's `collectionFormat` and `allowEmptyValue` come with
   their editions (6).
3. **Credentials, security and redirects**: explicit and exact alternatives,
   stable server identity, credential placement, the plain-http rule,
   `Redirects` and hop stripping, `FromTransport`.
4. **Request bodies**: JSON, text, form, multipart with
   Encoding and parts, octets, caller codecs, replay rules, 3.2
   sequential and iterator bodies.
5. **Documents**: YAML, references across documents, the
   admission callback, origin and local-file defaults, bounds, `Loader`.
6. **Editions**: Swagger 2.0 normalization and 3.0 and 3.2
   specifics, their edition-specific parameters included, run against
   every scenario so far.
7. **Streaming**: `Stream`, `Items`, `Events`, positional
   multipart reading, and independent upload completion.
8. **Prepare and raw send**: `Prepare`, `Request.Send`, `Response.Decode`,
   and refusals that name the setting that fixes them.
9. **Description**: descriptors, loaded-document inventory and lazy raw
   schema graph. The optional `schema2020` projection helper is not yet
   implemented.

Benchmarks compare calls with the same requests written by hand with
net/http, and cover the workloads of each capability.
