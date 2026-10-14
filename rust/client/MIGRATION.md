# Checked input revision (O6 stage 3, unpublished candidate)

Use `operation.request().parameter(location, name, &value).json(&body).prepare()`.
Serde admission runs in setters, with one private encoder and aggregate byte/node
budgets. Builders retain owned data and latch the first refusal. Set cancellation
before setters when callbacks must be suppressed. Duplicates precede cancellation;
a later token cannot undo callbacks that already ran.

`exact::Value` replaces ExactJson and Value with one owning node handle. Parse and
project the handle directly; there is no root() step and projection uses the
selected subtree. Use `parameter_exact`/`json_exact` for exact tokens. Value does
not implement Serialize. `from_serializable` remains for reusable exact values.

The public OrdinaryValue, Argument, Input, ParameterInput, Body, Selection, Form,
FormValue, ParseFailure, RequestError and DeserializationError routes are removed.
Use `server`, `media`, `security`, `server_variable`, `raw_body` and `no_body` for
explicit assignments. All failures return Diagnostic, with retained source and
explicit untrusted projection detail. Generated input positions refer to each
setter's owner, without the former shared array's `/0` prefix.

Use `form(&model)` for ordinary objects, and `form_fields` with FormField's explicit
`value`, `exact`, `part` and `parts` constructors for finite mixed uploads. Borrowed
bytes are copied during the setter; shared binary owners remain shared. Clone
models, exact values or field collections for reuse. Field cloning adds Arc
references rather than cloning the former public Form's outer storage.

CredentialValue is private. `Credential::bearer(token, origins)`,
`basic(username, password, origins)` and `api_key(value, origins)` require explicit
origin lists. Origin checking, elections, execution and response policies retain
their existing behavior.

Advanced types now live in `exact`, `inspect`, `source`, `graph`, `schema`, `form`,
`transport` and `diagnostics`, without old root aliases. Root Parameters and the
response/policy carriers remain for later implementation stages. The sections
below record earlier revisions; this section supersedes their input API examples.

# Carrier reduction (unpublished candidate)

Custom callbacks passed to `PreparedRequest::invoke` or `invoke_with_replay` now
receive an owned `PreparedRequest`. Read `method()`, `target()`, `headers()` and
`response_admission()` through its immutable getters. `body()` returns
`Option<&Arc<[u8]>>`; clone the Arc for a shared payload or use
`body().map(AsRef::as_ref)` for an optional byte slice. The separate
`DispatchRequest` type is removed.

Saving the callback request retains its operation, compiled declarations and
entire source graph after invocation. Release it after extracting the transport
facts needed by the host. A cloned body Arc alone does not retain that graph.
Neither cloning nor retaining a request authorizes another execution.

`DeliveredBody` is removed. Pass its three facts directly to
`ResponseInput::new(status, headers, bytes, body_state, provenance)` and access
`bytes`, `body_state` and `provenance` directly on the input. Safe Debug shows the
byte length, never the payload. Unknown header fidelity and Unspecified delivery
remain the defaults, with the same JSON acceptance behavior.

`SchemaHandle::references()` now returns an anonymous exact-size iterator that
owns its edges and can outlive both the handle and Document. No `SchemaReferences`
import is needed. Replace `DialectCapability` comparisons with
`schema.keywords_supported()`, which reports keyword support, not validation.
Replace `parameter.schema()` with `parameter.schema_description().map(|s| s.value)`.

`Part::headers` and its input plumbing are removed. Required unsupported headers
in authored multipart encoding declarations still refuse. Optional declarations
remain inspectable. Filename and media metadata keep their duplicate and framing
checks. The public Form owner and current builder/outcome routes remain in this
stage of the migration.

# Host-delivered body revision (unpublished candidate)

**Changed:** exhaustive matches on `Provenance` must handle `HostDelivered`; enum
exhaustiveness otherwise stays unchanged. This declaration selects interpretation
of exactly the bytes exposed by a host body stream, without claiming wire equality
or decompression. Set it at the adapter boundary before parsing, consistently,
rather than deriving it from labels, magic bytes or JSON success.

For this variant, the existing JSON methods parse delivered bytes directly.
`Content-Encoding` remains observable metadata and does not veto parsing. Valid
JSON with a stale gzip or unknown coding label intentionally succeeds if all
other guards pass; encoded binary still fails exact JSON parsing. There is no
sniffing, decompression, retry or second JSON API. Raw bytes, exact tokens and
positions are relative to the delivered source. Wire, Decoded and Unknown retain
their meanings and coding behavior; the native companion remains Wire.

The `CompletedJson.json` and refusal `decoded` fields hold parsed exact JSON;
the historical names do not establish network decompression. Status, replay,
upload, cancellation, metadata, media, body completeness and resource bounds stay
independent. This core contract alone does not qualify a Fetch or TypeScript host.

# Response admission revision (unpublished candidate)

**Changed:** callbacks now receive the immutable `PreparedRequest` described
above. External struct literals cannot construct it. Obtain genuine prepared
requests through preparation and dispatch through those callbacks. Replace
`dispatch.response_limit` with
`dispatch.response_admission().body_bytes()`. Read the same immutable view before
invocation through `prepared.response_admission()`. There is no public admission
constructor or mutable per-dispatch limits field.

`ResponseAdmission` also exposes `header_entries()` and `header_bytes()`.
The mappings are `Limits::body_bytes`, `Limits::headers` and
`Limits::document_bytes`, respectively. The last is the existing shared policy,
not a new dedicated response-header tuning option. Count UTF-8 name bytes plus
value bytes, excluding HTTP framing, before allocating copied metadata.

`ResponseInput::new` additionally sets `header_delivery` to
`HeaderDelivery::Unspecified`. Existing constructor calls retain their acceptance
behavior. Set `Complete` only after observing iterator EOF, or `Incomplete` after
a known bounded cutoff/conversion failure. This describes host-exposed enumeration,
not wire fidelity or hidden/filtered fields. The response getter preserves this
host fact even when core limits omit entries; `omitted_header_lines()` still counts
only actual supplied entries omitted by core, never an estimated adapter tail.

**Changed:** known incomplete metadata now refuses JSON with
`Code::IncompleteHeaders` and alone does not set `Outcome::error`. Existing code
that expected `InvalidHostEvidence`/transport failure for core omissions must
inspect decoding or acceptance instead. Raw finite evidence remains available.
Opaque responses still take precedence, then invalid status/retained fields or
contradictory `ExactFields` plus `Incomplete`; uninspected omitted fields are
unclassified. Incomplete metadata precedes body, media and coding decode checks.
Application policy retains cancellation/dispatch/transport/status/upload priority.
The native companion reports `Complete` or `Incomplete` after bounded collection;
its replay declaration and upload evidence remain unchanged.

See the executable `response_admission` example and [HOSTS.md](HOSTS.md).

# Supplied-host contract revision (unpublished candidate)

**Changed:** `ResponseInput` is non-exhaustive. Replace external struct literals
with `ResponseInput::new(status, headers, body)`, then assign `opaque_redirect`
and `header_fidelity` explicitly where the host supplies that evidence. The
constructor sets `false` and `HeaderFidelity::Unknown`; it never silently claims
exact fields. The native HeaderMap and Fetch-derived bridge metadata explicitly
use `HostProcessed`.

`Response::headers()` exposes retained delivered entries in host delivery order.
`header_fidelity()` describes their source; `omitted_header_lines()` retains its
historical name but counts entries omitted by core bounds. Zero core omissions
cannot establish absence of host filtering. `ExactFields` describes structured
field metadata, never raw HTTP framing. Body provenance remains separate.

`admit` and `invoke` retain strict defaults. Explicit `*_with_replay` methods take
`ReplayPolicy`; replay assurance is privately recorded on callback entry and
exposed by `Outcome::replay_assurance()`. Acceptance and mutations of public
outcome fields do not rewrite that record. Native execution remains strict.
External adapters can convert `HostRefusal` into a fixed HostCapability Diagnostic
with its controlled Host reason; no generic mutable reason setter is introduced.

Header and host Debug now omit arbitrary header names. Use explicit accessors
for names/values and treat them as untrusted private data. See [HOSTS.md](HOSTS.md)
for precedence, declaration limits and the separate Fetch/TypeScript work.

# Resource graph revision (unpublished candidate)

`Document::parse` and existing request calls keep their signatures. Add multiple
caller-supplied resources with `DocumentBuilder` and `ResourceInput`; incomplete
protocol dependencies expose `Diagnostic::resource_need()` uniformly. Recovery
publishes a new snapshot and preserves old values/caches. See [GRAPH.md](GRAPH.md).

**Changed:** direct reference inspection first obtains a typed site through
`Document::protocol_at(pointer, kind)` or a resource handle's `protocol_at`, then
calls `Document::resolve_protocol(&site)`. The old raw Value/kind resolver is
replaced by this one context-checked path. Sites and needs cannot be transferred
between snapshots. References to nonstandard declaration positions refuse.

**Changed:** operation-ID lookup refuses unresolved mounted Path Item discovery
because unknown mounts can introduce duplicate IDs. Explicit path/method lookup
can still select unaffected operations. Visible local validation errors alone do
not block ID lookup. Graph issues retain authored path order and source locations.

**Changed:** `Limits::descriptor_work` replaces the former aggregate descriptor
use of `reference_steps`; reference chains keep their own `reference_steps` cap.
Callers testing/customizing descriptor width should set `descriptor_work`.
New graph limits independently bound resources and URI/layout metadata.

**Changed:** an OpenAPI 3.2 security key absent from entry-root securitySchemes is
a URI reference, with typed recovery for missing resources. Exact names retain
precedence, including URI-looking names. Earlier families retain undefined-name
refusal. Encoding Header references use the same graph during finite form
preparation; required/custom header restrictions remain unchanged.

# Edition and document-format revision (unpublished candidate)

`Document::parse` now accepts original JSON or YAML documents from Swagger 2.0
and OpenAPI 3.0, 3.1 and 3.2 through the same request flow. Applications do not
choose a parser or rewrite version fields. `version()` retains the authored
version; `edition()` and `format()` are optional inspection metadata. These
changes extend the finite preparation profile, not every capability in each
specification.

Use `Operation::describe()` for effective server, parameter, request-body,
response-media and security descriptions. `inspect()` continues to expose
authored structure. Schema descriptions retain edition/dialect context rather
than translating schemas into another edition. Opaque body fields do not imply
an empty form or a fully interpreted schema.

**Changed:** `Method` is `Clone` rather than `Copy` so it can retain validated
extension method tokens. Clone a borrowed method when independent ownership is
needed. `Method::from_token` validates a wire token; `Method::parse` recognizes
fixed operation field names. OpenAPI 3.2 QUERY and `additionalOperations` use
the same operation/request model.

**Changed:** YAML values have `SourceOrigin::Normalized` for their semantic JSON
representation. `Document::source()` retains the original YAML. Use
`Value::source_trace()` to pair representation ranges with original authored
ranges and alias use sites; never apply a normalized range directly to the
original YAML. Exact JSON input keeps its existing token/range behavior, and
`ExactJson::parse` remains a JSON-only API. See the API guide for the bounded
YAML profile and new format-specific diagnostics.

## Corpus diagnostic revision

**Changed:** preparation and effective operation inspection refuse direct
Operation Object `$ref` fields with `InvalidDeclaration` and the typed reason
`UnsupportedReferencePosition`. OpenAPI does not admit references at this
position; previously the field could be ignored and an incomplete operation
prepared. Operation inventory and authored `inspect()`/`value()` remain available.
This targeted guard does not introduce whole-document validation. Valid Path Item
reference behavior and inert references inside extensions or schemas are unchanged.

**Added:** undefined security schemes report `UndefinedSecurityScheme` at the
named security requirement entry, for both `describe()` and preparation. Original
JSON/YAML source ownership and alias provenance remain available. Scheme names and
authored reference values are not included in default diagnostic formatting.

## Previous connection reuse revision

Public Rust signatures are unchanged. **Changed:** `HostCapabilities::no_retries`
now explicitly permits recovery only when a host proves the original request
was never serialized. It still forbids automatic replay once serialization has
begun or may have begun, including partial writes, status responses and ambiguous
failures. Hosts that cannot uphold that distinction must not assert this capability.
The core still invokes its supplied callback once and performs no retry itself.

The optional native companion now shares eligible HTTP/1.1 connections between a
client and its clones. It retains at most eight idle connections per origin with
a ninety-second idle timeout; this is not a limit on active concurrency or the number of origins.
An unusable pooled connection can cause recovery of a provably unsent request
within the original total deadline. This does not establish upload completion:
upload evidence after backend execution starts remains `Unknown`.

Connection reuse follows complete HTTP framing and backend readiness, separately
from application acceptance. A complete non-2xx response or a retention refusal
does not necessarily evict the connection. Cancellation still stops the local
wait without promising remote rollback.

## Previous real HTTP consumer revision

The optional `dynamic-openapi-client-reqwest` companion now supplies native HTTP execution without application-written callbacks. Core `PreparedRequest::invoke` remains available and runtime-independent. This adds no OpenAPI editions, schema evaluation or acquisition.

`Cancellation::cancelled().await` wakes pending local waits and supports multiple waiters. Existing polling/checkpoint APIs remain. `ExactJson::deserialize` explicitly selects standard Serde projection of the original bytes; typed floating-point results can round, while exact source remains unchanged. Projection errors hide arbitrary Serde text by default and expose it only through `detail()`.

Select `policies::received_2xx_json` to accept a complete 2xx JSON response without requiring known complete upload. It retains all Outcome evidence. The existing `complete_2xx_json` policy still refuses Unknown/Incomplete upload.

**Changed:** completed literal dot-only operation-path segments refuse with InvalidDestination as an explicit safety policy. Empty path segments remain supported. Automatic media election now filters by body compatibility; no compatible concrete media yields UnsupportedMedia. Multiple compatible media still require an election. Multiple security alternatives still require an explicit index.

**Changed:** public Diagnostic Display/Debug begins with a human-readable explanation. Structured codes/reasons remain the programmatic interface. An opt-in `display_with_location()` includes escaped, bounded document-controlled pointers/names. Default output still omits them. The serializer's associated error formatting is deliberately unchanged because a custom Serialize implementation can embed it into successful JSON output.

## Previous API quality revision


This pre-stabilization revision preserves the JSON/OpenAPI 3.1 finite supplied-transport profile. It does not add HTTP acquisition, a production transport or schema evaluation.

Diagnostic fields are now read-only. Replace `.code`, `.location`, `.related` and `.setting` reads with `code()`, `location()`, `related()` and `setting()`. Location access borrows: clone it explicitly when an independent value is needed. Keep `reason()`, `context()` and `source_context()`. Add application context with a wrapper implementing `std::error::Error` rather than rewriting a diagnostic's source range.

ParseFailure's source and diagnostic are also an immutable pair. Use `diagnostic()` and `source_bytes()`. `std::error::Error::source` still exposes the Diagnostic. Over-byte-limit admission still retains no oversized source. Construction of a category-only Diagnostic remains available through `Diagnostic::new(code)`.

An operation's `parameters()` supplies effective parameter declarations independently of request-body, media and security compilation. Returned values retain authored/reference/target sources after the original Document or Operation is dropped. Use raw `inspect()` or `value()` when structured interpretation refuses. `schema_description() == None` does not mean a parameter has no content declaration; inspect its resolved raw declaration.

For ordinary calls, use `operation.request().parameter(ParameterLocation::Query, "limit", 25).prepare()`. String names and server variables accept standard borrowed strings through `Cow`; primitive signed/unsigned integers through 64 bits, including default `i32` and platform `usize`, widen exactly. JSON arrays/objects can use borrowed `OrdinaryValue`; exact `Value` arguments retain their tokens and source owners. Builder assignments do not implicitly serialize arbitrary Rust data or narrow numbers. `Input` plus `Operation::prepare` remains the controlled exact-owner path.

For nested application structs or `serde_json::Value`, replace caller `serde_json::to_vec` plus `ExactJson::parse` glue with `ExactJson::from_serializable(&payload, limits)`, then pass `owner.root()` to the existing `.json(...)`. A mirrored `from_serializable_with_cancellation` checks the token before caller Serialize work. This eager constructor returns the existing `ParseFailure` and a reusable Constructed owner, with no extra Clone, Send, Sync or static input bound. It supports exact 128-bit integers and finite floats, refuses nonfinite values and implicit numeric map-key coercion, and validates the pinned arbitrary_precision Number protocol. RawValue remains an explicit parse input. Construction limits and later operation body limits are separate. Existing `from_ordinary` retains its narrow contract.

For existing numeric- or bool-key maps, convert keys to strings explicitly or use a key newtype whose Serialize implementation calls `serializer.collect_str(...)`. Checked construction adds admission work to provide its limits and refusal guarantees. Its depth cap counts emitted JSON containers; transparent Some/newtype recursion remains caller work and can exhaust a thread's stack even for shallow JSON output.

The earlier private-wrapper guarantee was too broad. Number validation and RawValue/unknown-protocol refusal apply when the wrapper's named Serde callbacks reach the constructor. An adapter such as `#[serde(flatten)]` can discard that name and forward ordinary map members instead: flattened RawValue becomes a `$serde_json::private::RawValue` string member, and arbitrary_precision Number can become a `$serde_json::private::Number` string member. This is unsupported wrapper composition whose earlier identity cannot be recovered. Literal application keys with those spellings remain supported and are never interpreted as protocol instructions. This clarification changes no runtime behavior. Ordinary Value member order follows the active serde_json map representation, including downstream `preserve_order` feature unification.

New `Code::Serialization` and `DiagnosticReason::Serialization(SerializationReason)` identify checked conversion refusals; limit and cancellation categories remain unchanged. Custom serializer/formatter messages are not formatted or retained. Early failures have Construction context without source/locations; complete generated-source parse failures retain their actual bytes and Constructed identity. See the API guide for first-error fusion and arbitrary caller-work boundaries.

The builder checks collection counts and byte lower bounds before its own growth/copies, then fully admits and serializes ordinary values once into one generated JSON owner when consumed. Generated wrapper bytes/nodes count against the same limits. Existing caller allocations and a shared exact value's entire backing source are separate from builder-owned storage. Custom `Into` implementations run caller code; standard `&str`/`&String` inputs borrow. The first refusal clears retained drafts and prevents later setter conversions. A latched refusal precedes cancellation; otherwise cancellation is checked before ordinary serialization. Each new builder starts healthy.

Assignments are explicit: populated selection fields compose, `None`/missing entries assign nothing, and distinct fields commute. Repeated assignment of any parameter identity, body (including explicit `Body::Absent`), selection field, server variable or credential scheme refuses even if equal. Header parameter names compare without ASCII case; extra header lines append under existing multivalue policy. Bulk selection events occur in server, media, security, then lexical variable order. `DiagnosticReason::DuplicateAssignment` records zero-based first/repeated event ordinals with typed context and no fictitious JSON location. Oversized caller context names are omitted with `context_omitted_for_limit() == true`.

`RequestError::Input(ParseFailure)` preserves generated-source construction failures; `RequestError::Preparation(Diagnostic)` preserves builder assignment/admission or protocol refusal. Both implement standard error chaining; `diagnostic()` provides the structured correction facts. No callback runs until a prepared request is explicitly invoked.

Select `policies::complete_2xx_json(outcome)` to require dispatched, uncancelled, transport-error-free, 2xx, complete-upload JSON. It replaces the copied example policy. `CompletedJson`/`JsonCallRefusal` retain the full Outcome; refusals retain decoded data or a decode diagnostic. `CompleteJsonRefusal` identifies the primary failed condition in documented order. Raw `invoke -> Outcome` is unchanged and remains the boundary for custom policies, local non-Send callbacks and native Send futures. No production transport is added.

The named policy's standard `Error::source` follows its primary reason when represented by a Diagnostic. Transport/preflight points to `outcome.error`, Decode to `decode_error`, and Cancelled only to an actual Cancelled diagnostic. Other conditions return no source; independent secondary errors remain available through the retained fields. In particular, a late transport error accompanied by malformed JSON chains to the transport diagnostic while keeping the decode diagnostic separately.

## JSON/YAML source provenance

`Document::parse` now accepts the documented YAML profile through the same bytes/base/limits entrypoint. `ExactJson::parse` remains exact JSON-only. `Document::source()` returns original input text and `Document::format()` reports the detected serialization.

`Value::raw()` and `location()` still identify indexed JSON. YAML's exact semantic representation has the new `SourceOrigin::Normalized` variant; update exhaustive matches on `SourceOrigin`. Use the additive `Value::source_trace()` to access the original YAML definition and alias use sites. Numeric source spelling belongs to the authored trace, while numeric token conveniences consume the exact normalized JSON token. Constructed caller values have no authored trace.

Malformed non-JSON input is now diagnosed through YAML admission and can yield `InvalidYaml` or `UnsupportedYaml` with a typed `DiagnosticReason::Yaml`; callers should inspect codes/reasons rather than assuming every document parse failure is `InvalidJson`. See [the loading profile](LOADING.md) for key identity, scalar, alias and limit policies.

## Finite named forms (unpublished)

Use `operation.request().form([FormField::new("title", "hello")])` for named forms.
Use `Part::bytes(slice).filename("file.bin")` for finite resource content, and
`Part::value(exact.root())` when an ordinary/exact value needs part metadata.
`FormValue::Parts(&parts)` carries per-item metadata. Existing JSON/raw body,
selection, credentials, cancellation and invocation APIs are unchanged.

**Changed:** authoritative form fields moved from `RequestBodyDescription.fields`
to `MediaDescription.fields`. Each representation retains its own schemas and
encoding objects. Replace `BodyFields::Opaque` patterns with `Opaque(issue)`;
handle `Partial { fields, issue }` without treating it as complete. Issues expose a
typed `FieldShapeReason` and original source handle. Replace a field's `required`
bool with `requirement: FieldRequirement`: `ProtocolRequired` is enforced,
`SchemaRequired` is a validation hint, and `Optional` is neither.

`Limits` adds `form_fields`, `form_parts`, `form_metadata_bytes`, `form_work` and
`boundary_attempts`; struct literals should use `..Limits::default()` when they
intend the defaults. `LimitKind` and `DiagnosticReason::Form(FormReason)` expose
corrective refusals. Debug remains redacted. This release implements the bounded
profile in [FORMS.md](FORMS.md), not every form/multipart feature.


## Independent compatibility review repairs

OAuth flow names now follow the authored OpenAPI minor family. OpenAPI 3.2
`deviceAuthorization` uses the same supplied bearer-token preparation path and
adds `OAuthFlowDescription::device_authorization_url`; this is a returned
descriptor field, not token acquisition. Swagger `application`/`accessCode`
normalize to `clientCredentials`/`authorizationCode` only in Swagger documents.

The applied 3.2.1 clarification profile uses the published default schema dialect
`https://spec.openapis.org/oas/3.2/dialect/2026-02-26` for every 3.2 patch spelling.
Authored `$schema` and `jsonSchemaDialect` still take precedence. The original
version spelling is retained; patch versions do not fork the caller's API.

Swagger `formData` with absent or empty effective `consumes` now exposes both
protocol-defined form representations, including each representation's fields.
Choose one with the existing `Selection::media` field. The default is ambiguous,
not an invented server preference. Such a body is constrained by `formData` and
is not `media_unrestricted`; JSON cannot substitute for its form encoding.
The contributing form parameter owns each implicit representation's provenance.

Multipart collision scanning now preserves overlapping prefixes across chunks.
No public boundary spelling or new builder path is introduced.
