# Go OpenAPI client: design

This directory holds the public API of the Go client, designed before any
implementation: the exported declarations with their documentation, and
`Example` functions for every caller scenario in `design/scenarios.md`. The
function bodies are stubs. `design/requirements.md` records what the OpenAPI
specifications and RFCs require of a client, and the default this client
takes wherever OpenAPI is silent.

Import path `github.com/openbindings/openapi-client/go/openapi`.

## The model

`Load` (or `Parse`, for bytes) reads a document once into an immutable,
concurrency-safe `Client`, and fails fast on Options the document cannot
use. `Options` (zero value: the defaults) says where calls go, as whom and
how; `With(func(*Options))` derives a Client sharing the document with an
edited private copy. `Call(ctx, key, in, out)` sends an operation named by
operationId or `"METHOD /path"` with an `Input` (parameters by name, body,
per-call choices) and decodes a 2xx from the connection into `out`. `Stream`
leaves the body open for `Items[T]`, `Events` or raw reads. `Prepare` is
Call without sending: its `Request` holds the editable `*http.Request` that
will be sent, credentials added only then, the declared media type that
governs its body, and a `Choice` for each decision the document left open.
Outcomes: `*RequestError` (not sent), net/http's errors, `*StatusError`
(non-2xx), `*DecodeError` (2xx unusable), success. `Operations` describes
the document, with `Err` and `Source` on its parts and lazy `Schema`
handles.

## Where each rule lives

Every rule has one home; other docs point to it.
Package doc: values, outcomes and their test order, the defaults for open
choices, serialization rules (URL joining, bodies by method, parameter
order, percent-encoding, forms, cookies, empty values, Accept, header
fields, content codings), and credentials (naming, placement, transport
security, refusals). `Redirects`: which 3xx are followed and what a hop
strips or re-places. `Response.MediaType`: media matching, for requests too.
`Input.Body`: body shapes, replay, lifetime, iterators. `Call`: decoding by
target. `Loader`: document acceptance and reference resolution. `Load`:
fatal defects and the fail-fast Options list. `SecurityRequirement.Key`: the
canonical key. `Operation`: how descriptions follow references.

## Size

206 exported identifiers (203 without 3 embedded fields): loading 15,
calling 56, errors 21, streaming 12, credentials 5, description 97. Most of
the description's are fields that mirror OpenAPI's own objects.

The implementation is estimated at about 8,900 production lines (range
about 7,300 to 10,850), built as one engine working lazily on the parsed
document, with Swagger 2.0 normalized into the same per-operation plan, no
schema validation, net/http for all HTTP, and a YAML parser as the only
dependency. Tests will likely add as much again. The first stage measures
the estimate.

## Implementation order

Each stage lands a user-visible capability with its tests, and is reviewed
as code before the next begins. Estimates are production lines.

1. **Vertical slice** (about 1,400): `Load` of a 3.1 JSON document over
   http or from a file, local `$ref` resolution, the operation index
   (operationId and `"METHOD /path"`), one server with variable defaults,
   path and query parameters in their default styles, `Call` decoding JSON
   into a struct, `*any`, `*[]byte` and `io.Writer`, and the outcomes
   (`RequestError`, `StatusError`, `DecodeError`, `Response`) with the
   bounds. Tests against `httptest` servers; a benchmark of `Call` against
   the same call written by hand with net/http (allocations and latency).
2. **Parameters, fully** (about 800): every style, `explode`,
   `allowReserved`, content parameters, headers, cookies, 3.2 querystring.
3. **Credentials, security and redirects** (about 1,150): alternatives and
   the default, placement, the plain-http rule, `Redirects` and hop
   stripping, `FromTransport`.
4. **Request bodies** (about 1,235): JSON, text, form, multipart with
   Encoding and parts, octets, replay rules, 3.2 sequential and iterator
   bodies.
5. **Documents** (about 1,300): YAML, references across documents, the
   origin rules, the bounds, confinement, `Loader`.
6. **Editions** (about 950): Swagger 2.0 normalization and 3.0 and 3.2
   specifics, run against every scenario so far.
7. **Streaming** (about 540): `Stream`, `Items`, `Events`, positional
   multipart reading.
8. **Prepare and choices** (about 350): `Prepare`, `Request`, `Choice`
   records, and the refusals that name their fix.
9. **Description** (about 1,670): the descriptors and the lazy 2020-12
   view.

Stage 1 measures the estimate: if the slice's real size or speed departs
from it, the remaining stages are re-estimated before they start.
