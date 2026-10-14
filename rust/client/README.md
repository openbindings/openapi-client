# Dynamic OpenAPI client

An independent Rust client for dynamic OpenAPI documents and finite requests through caller-supplied transport. This unpublished compatibility candidate accepts Swagger 2.0 and OpenAPI 3.0, 3.1 and 3.2 in JSON or YAML; its implemented feature profile is explicit below.

`Document::parse` detects format and edition, retains source and provides immutable owned handles. Inspect `Document::operations`, effective `Operation::describe` or `Operation::parameters`, then use `Operation::request` for ordinary calls. `Operation::inspect` and raw `Value` handles expose authored declarations. Use `Operation::prepare` with explicit `Input` when you already manage exact values and selections. `PreparedRequest::invoke` accepts a host capability declaration and an async callback receiving an owned PreparedRequest. Saving that request retains its source graph; clone only its body Arc when the payload alone is needed. Native concurrent and host-local callbacks can use the same semantic types without a required executor.

Preparation interprets each supported edition's own server, parameter, body, media and security declarations. The current profile includes source-preserving local and supplied-resource JSON-Pointer protocol references, explicit server/media/security choices, Swagger primitive and collection parameters, supported 3.x logical parameter styles and JSON/text content parameters, scoped supplied Basic/Bearer/API-key credentials, supplied OAuth2/OpenID Connect bearer tokens and finite JSON/raw bodies and named URL-encoded/multipart forms. OAuth token acquisition remains caller-owned. Response metadata, delivered bytes/provenance, upload state, cancellation and decode failures remain independent.

Edition coverage is not full-feature coverage. Custom serialization conventions, content codings, positional/nested multipart, 3.2 cookie/querystring styles, richer content codecs, resource acquisition, streaming and WebSocket connections remain unqualified or unimplemented. Effective descriptors retain unsupported declarations for discovery; an available descriptor does not guarantee preparation or execution support.

For multiple supplied resources, use `DocumentBuilder` and the same Document/Operation request APIs. Missing bytes or explicit fragment layouts yield typed corrective needs; publish a new snapshot after supplying them. See [immutable resource graphs](GRAPH.md) for recovery, identity, discovery and current limits.

The [document loading guide](LOADING.md) documents YAML scalar/key policies, exact numeric normalization, alias bounds and original source traces. `ExactJson::parse` remains JSON-only; the version-neutral entrypoint for OpenAPI documents is `Document::parse`.

Schemas have owning typed [inspection handles and direct reference resolution](SCHEMAS.md); form preparation consumes supported static declaration facts without evaluating instances. Unknown fields remain inspection data. A direct `$ref` on an Operation Object is an explicit exception: OpenAPI does not define that reference position, and preparation refuses with `UnsupportedReferencePosition` instead of silently sending an incomplete request. Raw inspection remains available. The core performs no schema evaluation/default insertion, acquisition, automatic redirect/retry or streaming codec. For native HTTP calls, use the optional [`dynamic-openapi-client-reqwest` companion](../reqwest/README.md); it owns the HTTP stack and configuration while this crate remains runtime-independent. The optional [Rust Fetch companion](../fetch/README.md) supplies explicit Browser/Worker profiles on Wasm and is under qualification; it is not a TypeScript/npm client.

Source and decoded strings are exact for Unicode scalar values. Unpaired surrogate escapes return a located limitation with source preserved. Numeric tokens remain exact; integer conveniences are checked, and binary64 conversion currently accepts exactly representable integer lexemes only. Mathematical conversion of decimal/exponent tokens remains explicit unsupported conversion. Null, empty-container and nested style-based parameter values receive located refusals; empty strings are supported. JSON content parameters accept these JSON shapes without schema evaluation.

Supplied hosts must enforce no ambient authentication or implicit redirects, disclose target/header restrictions and content decoding, and bound delivered response bytes. Default admission and invocation require a no-replay declaration. An explicit `ReplayPolicy::AllowHostManaged` permits a host without that declaration, while retaining every other admission rule. The `no_retries` capability permits recovery only when the host proves the original request was never serialized, such as after encountering a stale pooled connection. Under that declaration, once serialization has begun or may have begun, status responses, partial writes and ambiguous failures must not trigger another attempt. The core itself never retries. The core can validate its own data and returned evidence; it cannot limit an arbitrary callback's hidden allocation or undo remote effects. Use explicit accessors carefully: raw targets, header bytes, source values and host error details can contain secrets. SDK Debug/Display views omit arbitrary header names as well as values and opaque host details.

## Calling a real service

Use the optional [native HTTP companion](../reqwest/README.md) to configure a client and origin-scoped credentials once. It supplies `client.request(&operation)` and `client.execute(&prepared, cancellation)`, so application code uses the same request builder without implementing a transport callback. Its getting-started example runs against a local HTTP server and includes typed input and output.

A native HTTP response does not prove complete upload. Select `policies::received_2xx_json(outcome)` when your application accepts a complete, uncancelled, transport-error-free 2xx JSON response while retaining upload uncertainty. The stricter `complete_2xx_json` below requires known complete upload. Both return the same evidence-bearing types; neither policy changes invocation or retries.

## A dynamic Rust call

The complete outcome is the application-policy boundary. This example requires dispatched, uncancelled execution without a transport error, a 2xx response, known complete upload and valid JSON. Unknown upload is a refusal **for this example**. A different application may accept an early response while preserving/reporting uncertainty. Success and failure both retain Outcome; failure also retains any decode error and successfully decoded data. A response alone does not establish successful execution.

Select `policies::complete_2xx_json(outcome)` to use that policy. It returns `CompletedJson` or a boxed `JsonCallRefusal`, both retaining the complete Outcome. Refusals retain a successfully decoded value or a decode diagnostic; decoding is attempted at most once. The primary reason order is cancellation, non-dispatch, transport, missing response, non-2xx, upload not known complete, decode. Independent facts remain inspectable, including a useful error response accompanied by a late transport failure. `Error::source` follows the primary reason when it has a diagnostic: transport/preflight, decode, or an actual cancellation diagnostic. Status, upload uncertainty, missing response, or cancellation without a matching diagnostic have no source; inspect retained fields for independent errors.

Apply it after invoking the supplied transport:

```rust
use dynamic_openapi_client::*;
async fn inspect_and_call(text: &[u8]) -> Result<CompletedJson, Box<dyn std::error::Error>> {
    let document = Document::parse(text, Some("https://api.example/openapi.json"), Limits::default())?;
    let operation = document.operation_id("listItems")?;
    let request = operation.request().prepare()?;
    drop(document); // The operation and request retain their owners.
    let outcome = request.invoke(
        Cancellation::default(), &HostCapabilities::programmable(),
        |request, cancellation| async move {
            // Finite fixture transport; supply a qualified host for real I/O.
            assert!(!cancellation.is_cancelled());
            assert_eq!(request.method(), Method::Get);
            TransportResult {
                response: Some(ResponseInput::new(
                    200, vec![Header::new("Content-Type", "application/json")],
                    br#"{"count":9007199254740993}"#.as_slice().into(),
                    BodyState::Complete, Provenance::Wire,
                )), upload: UploadState::Complete, error_detail: None,
            }
        },
    ).await;
    // The boxed application failure keeps the full outcome and decode diagnostic.
    Ok(policies::complete_2xx_json(outcome)?)
}
```

`HostCapabilities::programmable()` is a caller promise for controlled callbacks, not proof about an arbitrary HTTP stack. `cargo run --example dynamic` executes the complete fixture flow. Real asynchronous I/O requires the caller's host/executor. `Response::json()` parses on each call: retain the returned ExactJson when reusing decoded data. For a different application policy, inspect raw `Outcome` directly and choose your own order/acceptance rule. Preserve dispatch, upload, cancellation, error and response facts; a response alone does not establish execution success. Calling the named policy is never implicit in `invoke`.

## Shared parameter and form styles

Defined label/matrix path, space/pipe query/form and flat `deepObject` styles use
the existing logical parameter/form inputs. Single-media JSON/text content
parameters use the same `.parameter(...)` method. Style-based spaces use `%20`;
content query parameters and URL forms use `+`.
See [SERIALIZATION.md](SERIALIZATION.md) for applicability, exact-value ownership,
corrective refusals and the real provider preparation example.

## Supplied-host evidence

`invoke` and `HostCapabilities::admit` remain strict defaults. The corresponding
`invoke_with_replay` and `admit_with_replay` methods accept the explicit caller
policy. `Outcome::replay_assurance()` records the admitted declaration at callback
entry: `NoReplayDeclared` or `HostManaged`. It is absent on pre-dispatch outcomes
and survives later caller mutation of public outcome fields. It proves neither
network attempts nor server receipt. Native reqwest retains its strict path and
exposes no replay opt-in. See [HOSTS.md](HOSTS.md).

Construct delivered responses with `ResponseInput::new(status, headers, body)`.
Header fidelity starts `Unknown`; adapters set `HostProcessed` or `ExactFields`
only when their supplied evidence supports that declaration. `ExactFields` means
received structured fields, including names, bytes, duplicates and order, without
host omission; it does not mean raw framing or whitespace capture. Core omission
counts and body provenance remain independent. Valid processed JSON metadata can
be decoded without claiming exact headers or upload completion.

## Sending logical forms and finite files

Use the same `.form(...)` call for a declared URL-encoded or multipart/form-data
request. The operation selects a unique compatible representation; use the
existing `Selection::media` when more than one applies. Field names are unique;
ordinary/exact arrays express repeated values. The library owns URL escaping,
part framing and collision-checked multipart boundaries.

```rust
use dynamic_openapi_client::*;
# fn upload(operation: &Operation, file: &[u8]) -> Result<PreparedRequest, RequestError> {
let metadata = ExactJson::from_serializable(
    &serde_json::json!({"title": "Résumé", "revision": 7}), Limits::default()
).map_err(RequestError::Input)?;
operation.request().form([
    FormField::new("caption", "Résumé + notes"),
    FormField::new("metadata", metadata.root()),
    FormField::new("file", Part::bytes(file).filename("résumé.pdf")),
]).prepare()
# }
```

This example expects the declaration to describe text, JSON metadata and a binary
file. `Part::media_type("image/png")` elects one declared part representation when
needed; the library does not sniff content. A filename is metadata and causes no
filesystem access. `FiniteBytes` borrows a slice or shares an existing `Arc<[u8]>`.
`FormValue::Parts(&parts)` supplies an array with individual file/part metadata.
`Part` has no embedded field name, so content can later compose with other body
models. Prepared requests own finite wire bytes and report `body_kind() == "form"`.

Nested Rust models and `serde_json::Value` use the same
`ExactJson::from_serializable` bridge as `.json`. Existing exact owners can be
reused directly. This explicit conversion is still required; `.form` does not
add another arbitrary-Serialize conversion path.

Each `MediaDescription::fields` describes only that representation. `Declared`,
`Partial`, `Opaque` and `NotApplicable` remain distinct. `FieldRequirement`
distinguishes Swagger protocol requirements from OpenAPI schema-required hints;
schema hints do not activate validation, defaults, coercion or projection.
An explicitly JSON-encoded field can use opaque schema content without resolving
unrelated schema assertions. An unresolved fact needed to choose its wire codec
refuses with a source-aware `DiagnosticReason::Form` and field context.

Read [the finite form profile](FORMS.md) for edition semantics, resource bounds,
header policy and remaining capabilities. [CHANGELOG.md](CHANGELOG.md) and
[MIGRATION.md](MIGRATION.md) describe the unpublished descriptor revision.

## Reading an application type

After accepting the outcome, project the retained exact JSON into a Rust type:

```rust
use dynamic_openapi_client::{ExactJson, Limits};
use serde::Deserialize;
#[derive(Deserialize)]
struct Item { id: u64, name: String }
let json = ExactJson::parse(br#"{"id":9007199254740993,"name":"sample"}"#, Limits::default())?;
let item: Item = json.deserialize()?;
assert_eq!(item.id, 9_007_199_254_740_993);
assert_eq!(item.name, "sample");
assert_eq!(json.root().get("id").unwrap().raw(), "9007199254740993");
# Ok::<(), Box<dyn std::error::Error>>(())
```

`deserialize` uses standard Serde JSON conversion directly on the original bytes, without an intermediate `serde_json::Value`. Integer destinations reject overflow and support i128/u128. Choosing f32/f64 explicitly selects ordinary floating-point rounding; it does **not** make the promise of `Number::to_f64_exact`. Serde token rules also apply: `-0` refuses for an `i64` destination, while the exact `Number::to_i64()` convenience returns zero and an `f64` projection preserves the negative sign. Keep the exact owner when original spelling or mathematical precision matters. Borrowed output can borrow unescaped strings from the owner. Custom Deserialize implementations are caller work; projection applies Serde's own recursion policy and performs no schema evaluation.

`DeserializationError` safely reports the conversion failure and line/column. Its `detail()` is explicit access to the underlying Serde error, which can include response values or custom error text; default Display, Debug and error chaining omit that text. A projection failure leaves the exact JSON and original Outcome available to the caller.

Response decoding may defer construction of the full dynamic value index. Validation still finishes before `Response::json()` or a JSON policy accepts the response, including duplicate names, Unicode and resource limits. Typed projection reads the original bytes without forcing index construction. The response retains compact admission data; later dynamic access reuses the checked strings and object metadata without parsing them again. Cloned owners share the resulting index. Public `ExactJson::parse`, document parsing and input construction remain eager.

`retained_bytes()` reports accounted source and index storage without forcing index construction. Its count can increase after dynamic access. During concurrent construction it reports the admitted-storage snapshot until the completed index is published; temporary construction overlap, allocator overhead and RSS are outside this estimate.

This estimate is not a physical heap measurement: shared member-name text is counted in multiple roles in the materialized index, and opaque platform synchronization storage is outside the estimate. Compare allocation measurements when evaluating memory savings between representations. Deferred response indexing reduces typed-only allocation, with extra allocation when dynamic access later materializes the index; the repository's `rust/qualification/response-indexing.md` records the measured tradeoff and qualification limits.

## Constructing ordinary inputs

For a normal dynamic call, the operation owns a per-call builder using its configured limits:

```rust
use dynamic_openapi_client::*;
use serde::Serialize;
# fn build(operation: &Operation) -> Result<PreparedRequest, RequestError> {
#[derive(Serialize)]
struct Item<'a> { count: u64, tags: Vec<&'a str> }
let payload = Item { count: 9_007_199_254_740_993, tags: vec!["red", "blue"] };
let body = ExactJson::from_serializable(&payload, Limits::default())
    .map_err(RequestError::Input)?;
let request = operation.request()
    .parameter(ParameterLocation::Path, "id", "A/B")
    .parameter(ParameterLocation::Query, "limit", 25)
    .json(body.root())
    .prepare()?;
# Ok(request)
# }
```

Standard string names/server variables borrow through `Cow`; values accept borrowed strings, booleans, signed/unsigned primitive integers through 64 bits (including default `i32` and `usize`), borrowed `OrdinaryValue` arrays/objects, or exact `Value` handles. No float or 128-bit narrowing occurs. Ordinary values serialize once into one generated owner at consuming prepare; exact arguments preserve their owner/token identity. The builder checks collection counts and byte lower bounds before its own growth/copy, then fully checks depth/nodes/escaped bytes at preparation. Caller allocations, custom `Into` work and a shared exact handle's full backing owner are separate resource domains.

Use `ExactJson::from_serializable(&value, limits)` for a nested application struct, collection, or `serde_json::Value`. It emits bounded JSON bytes and then uses the same exact parser/indexer once. Pass `owner.root()` or retained child handles to the existing builder or `Input`; retain the owner when repeated requests should reuse the body. Construction accepts borrowed, non-Clone and host-local Serialize implementations without requiring Send, Sync or static input. Returned exact owners and existing builders retain their thread-safety behavior. An independently constructed body and ordinary builder parameters may retain separate owners.

The checked Serde profile supports exact signed/unsigned integers through 128 bits, finite f32/f64, strings/chars, sequences/tuples, structs, string-key maps, standard enum representations and bytes as arrays of numbers. Float spelling comes from the pinned JSON formatter and describes the supplied binary float, including negative zero; it cannot recover decimal precision already lost by the caller. NaN and infinity refuse. Numeric/bool/container map keys refuse instead of becoming strings; char, unit enum, string-emitting newtype and explicit `collect_str` keys are supported. None/unit become null; a skipped Serde field is absent. Duplicate emitted names refuse through the exact parser with complete generated source. No schema defaults or evaluation occur.

`serde_json` feature unification with `arbitrary_precision` is supported by strictly checking its pinned Number protocol and preserving the number token when the protocol's named Serde callbacks reach the constructor. At that same boundary, RawValue and unknown private protocols refuse; pass exact raw JSON to `parse`. Ordinary `serde_json::Value` member order follows its active map representation: sorted keys by default and insertion order with `preserve_order`. The constructor preserves that emitted order.

Serde adapters can erase a private wrapper's identity before this boundary. For example, `#[serde(flatten)]` applied to RawValue containing `{"x":1}` emits the ordinary object `{"$serde_json::private::RawValue":"{\"x\":1}"}`; flattening an arbitrary_precision Number similarly emits a marker-keyed object. Flattening these wrappers is unsupported composition whose original identity cannot be detected after the adapter erases it. These objects have the same observable representation as legitimate application maps with those literal keys. The constructor preserves ordinary map entries literally and never interprets their names as raw-JSON or number instructions. This scope corrects the earlier blanket statement that every private wrapper would refuse.

A checked construction refusal reports `Code::Serialization` and `DiagnosticReason::Serialization`, distinguishing nonfinite values, non-string keys, unsupported or invalid representations, and custom serializer/formatter failure. Custom error text is neither formatted nor retained. Limits and cancellation keep their existing structured categories. Before a complete representation exists, failures have Construction context and no source bytes, source identity or invented location. Complete parse failures retain Constructed bytes and locations.

Construction is eager. Use `from_serializable_with_cancellation(&value, limits, &token)` to check cancellation before Serialize and at emission/indexing checkpoints; a later builder token cannot cancel earlier construction. Document byte/node limits and container depth `min(limits.depth, 128)` apply here. The operation's body limit is checked separately during request preparation. First detected construction refusal is latched: ignored errors cannot resume library writes or later child callbacks. Sequence hints do not drive allocation. Arbitrary caller Serialize/Display execution, recursion and allocation remain caller work and cannot be preempted by this synchronous API.

The depth cap counts emitted JSON containers. Transparent `Some` and newtype wrappers do not add JSON containers, so their recursion is outside that cap. Deep wrapper chains can exhaust a thread's stack even when they emit a single scalar; the constructor does not bound that caller-controlled recursion.

Every explicit field assignment counts, including `Body::Absent`; `Selection::default()` assigns nothing and clears nothing. Distinct fields commute; repeats refuse even when equal. Header parameter identities are case-insensitive, while additional `header` calls append ordered lines. Bulk selections assign server, media, security and lexical variables in that order. Duplicate diagnostics include typed context and zero-based assignment ordinals. A first refusal drops prior drafts and ignores later setter conversions. It precedes cancellation; otherwise consuming prepare checks cancellation before serialization. Correction starts a fresh builder on the same healthy operation.

`RequestError::Input` retains a generated-source `ParseFailure`; `RequestError::Preparation` retains a builder or protocol `Diagnostic`. Both compose with ordinary Rust error chains. Construction/preparation performs no transport. Controlled callers can construct `Input` and call `Operation::prepare` for parameters and JSON/raw bodies. Named forms currently require the builder's `.form(...)` method and use an internal extension of the same preparation pipeline; public `Input` cannot yet represent a form.

Use `ExactJson::from_ordinary` for the deliberately narrow borrowed `OrdinaryValue` representation. It checks limits, serializes generated JSON bytes, then parses/indexes once. It is fallible and not zero-copy. This narrow API accepts no floats or arbitrary Serialize implementations; use `from_serializable` for the checked Serde profile above. Use exact authored JSON tokens through `parse` when those are the input; conversion conveniences never change stored tokens.

```rust
use dynamic_openapi_client::*;
let owner = ExactJson::from_ordinary(OrdinaryValue::Object(&[
    ("id", OrdinaryValue::String("A/\"B")),
    ("active", OrdinaryValue::Bool(true)),
    ("limit", OrdinaryValue::U64(9_007_199_254_740_993)),
    ("offset", OrdinaryValue::I64(-2)),
    ("tags", OrdinaryValue::Array(&[OrdinaryValue::String("red"), OrdinaryValue::String("blue")])),
    ("explicit_null", OrdinaryValue::Null),
]), Limits::default())?;
let root = owner.root();
assert_eq!(root.get("limit").unwrap().raw(), "9007199254740993");
assert_eq!(root.get("explicit_null").unwrap().kind(), ValueKind::Null);
assert!(root.get("absent").is_none());
assert_eq!(root.source_context().origin, SourceOrigin::Constructed);
let parameter = ParameterInput {
    location: ParameterLocation::Path, name: "id".into(), value: root.get("id").unwrap(),
};
drop(owner); // The parameter's handle retains its constructed owner.
# Ok::<(), ParseFailure>(())
```

Generated ranges identify this constructed JSON, never the OpenAPI document. `SourceContext.id` distinguishes owners within one process/Wasm instance and is not a persistent or cross-instance identifier. Exact JSON parsing marks externally supplied bytes Authored; this name is not a claim about their historical origin. YAML document values use Normalized representation ranges paired with authored source positions through `source_trace()`. Pre-serialization failures carry Construction context and no complete source; parse failures after serialization retain the generated source. Null is distinct from omitted members and Body::Absent. Parameter policy can still refuse null or nested/empty containers even though the JSON owner represents them faithfully.

## Effective parameter inspection

`operation.parameters()?` resolves effective path/operation declarations with the same graph-reference, override and required-default rules as preparation, without compiling unrelated body/media/security requirements. Iterate the returned `Parameters` and use `name()`, `location()`, `required()`, `authored()`, `resolved()` and `schema_description()`. Each retained parameter keeps its original source owners independently of the operation. Reference inspection distinguishes authored use site, sibling overrides, chain and target. `schema_description() == None` does not imply no `content` declaration; resolved raw values remain available. Unsupported editions or invalid typed declarations refuse; raw `Operation::inspect`/`value` remain available. These views do not assert full document validity or schema conformance.

## Correcting a refusal

Use `Diagnostic::reason()`, `context()` and `source_context()` along with Code. `DiagnosticReason::Limit` identifies the configured limit and actual/maximum counts; Numeric distinguishes lexical form, signed/unsigned range and precision. F64Exact intentionally first requires an integer lexeme fitting i64: even the exactly representable 2^63 is outside that convenience's range. Conversion-character limits are a Limit(NumberCharacters). Ordinary i64/u64 construction is exact and does not route through f64.

A caller parameter refusal identifies its vector index, namespace and explicit-access name; an election identifies server/media/security/variable context. Located diagnostics include the owner/origin of their byte ranges. Caller-only or derived-target failures have no fictitious document location. Names, keys, pointers and explicitly accessed bytes can be sensitive. Default Number, ordinary-input and diagnostic Debug/Display exclude those values. ParseFailure exposes its Diagnostic through Error::source for ordinary Rust error chaining.

Diagnostic metadata is immutable: use `code()`, `location()`, `related()` and `setting()` accessors, and `Diagnostic::new(code)` for a category-only diagnostic. `ParseFailure::diagnostic()` and `source_bytes()` expose its immutable source pair; standard `Error::source` remains available. Wrap errors to add application context. Bounded duplicate context names may be omitted; `context_omitted_for_limit()` makes that explicit. See [migration notes](MIGRATION.md) for this unpublished API revision.

For a value-located diagnostic, `source_position()` returns an owning `SourcePosition`
that remains usable after the error, input and document are dropped. Its `raw()`
accessor selects the exact range from its `source()`, using authored YAML positions
when available. It shares the complete source text without retaining its value
index or document graph; keeping a diagnostic can therefore keep that text alive.
`source_context()` identifies a source but does not by itself promise retained
bytes. Parser and unlocated diagnostics can return no position; use the enclosing
`ParseFailure::source_bytes()` for admitted parser input, including invalid UTF-8.

## Bounds and deliberate policies

Each effective description or compiled operation has a shared allocation-admission ledger. Copied strings are bounded by `document_bytes`, descriptor entries and strings by `nodes`, and aggregate declarations/reference work by `descriptor_work`; each reference chain is independently bounded by `reference_steps`. These are separate budgets per construction: repeated shared OAuth or dialect metadata is charged for every copy before allocation; retaining a raw `Value` shares its existing owner. Located limit reasons distinguish `DescriptorBytes`, `DescriptorNodes`, and `DescriptorWork`. Caller-requested DTO clones and allocator overhead are outside these construction budgets. Supplied-token preparation validates security metadata through borrowed views without materializing OAuth acquisition descriptions.

Defaults admit 8 MiB documents/caller metadata, nesting 128, 500,000 JSON nodes, 20,000 operations, 128 reference steps, 500,000 descriptor work units, 4 MiB bodies, 64 KiB targets, 256 header lines and 1,024 cached operations. Caller input counts and security requirements are bounded by the reference-work budget. Limits are per owner/call and may be configured; depth above 128 still refuses pending qualification. A byte-limit refusal does not copy an oversized input: `ParseFailure::source_bytes()` is empty in that case. Other admitted parse failures retain exact bytes. JSON indexing checks node admission before growing its owned work queue. Intermediate encoding allocation is bounded by admitted input bytes, even when the resulting target subsequently exceeds its smaller limit.

Cancellation is cooperative. Cancellable parse/prepare methods check owned work; invocation checks before dispatch and records cancellation after the callback returns. A callback can select `cancellation.cancelled().await` against its I/O to interrupt a pending local wait. Each wait has its own registration; cancellation wakes all waiters and dropping a wait unregisters it. The native companion does this automatically. Cancellation is irreversible and repeated requests are harmless. Future drop establishes no remote outcome. Response metadata over the count/byte limits preserves an explicit omitted-line count; available status and bounded body remain inspectable, and JSON convenience decoding refuses with `IncompleteHeaders`. Malformed retained evidence remains `InvalidHostEvidence`.

Media election supports concrete types and matching declaration ranges, with explicit concrete selection required for range-only declarations. Malformed keys do not govern. More-specific matching declarations take precedence; equivalent best matches are ambiguous. Quoted media parameters are parsed as HTTP field parameters. Duplicate parameter names are refused. No codec or encoding metadata is applied to opaque schemas or raw caller bytes. UTF-8 is the explicit Basic credential policy. Header controls and surrounding whitespace are refused; header parameter values are not URI encoded. Cookies use `; ` between pairs.

`retained_bytes()` is a storage estimate for the immutable JSON owner, including retained YAML source mappings when present, and excluding document descriptors, compiled caches, allocator metadata and reserved pages. Qualification reports measure allocator traffic, live allocation sizes and process RSS separately. Exact value handles use the source owner's identity and do not imply equality across distinct documents.

The non-default `qualification` Cargo feature is reserved for qualification
runners, benchmarks and instrumented tests. Its hidden hooks are unsupported and
are not part of the client API. Ordinary builds contain no global JSON-owner
accounting. `Limits::cached_operations` remains the cache-size admission limit.

## Corrective diagnostics and request choices

Default diagnostic text explains the failure in plain language and retains its structured code/reason; it omits document-controlled names and values. For an interactive UI that deliberately displays source locations, use `error.display_with_location()`. This opt-in formatter includes escaped JSON pointers and caller parameter/variable/scheme names, limited to 256 characters per displayed field. Names can contain sensitive data; the full source and typed facts remain available through explicit accessors. Default logging behavior remains value-safe.

Automatic request media selection considers the supplied body: `.json(...)` can uniquely select a JSON media type alongside incompatible XML/form choices. Multiple compatible JSON media types still require `Selection::media`. Raw bodies can use any supported concrete media and therefore retain their election requirements. Security is a separate policy: more than one authored alternative requires explicit `Selection::security`, even when only one credential is supplied or an anonymous alternative exists.

Completed operation-path segments equal to `.` or `..` refuse before dispatch, including segments assembled from template literals and parameter values. This is a preparation safety policy because normalizing HTTP stacks can route such paths elsewhere. Empty values/segments and embedded dots remain supported. Server-reference resolution is unchanged; transports still disclose and check percent-encoded-dot support and other URL normalization restrictions.

Target composition uses an explicit boundary policy: resolve and expand the effective server first, then append the complete operation path. When the server ends with `/` and the path begins with `/`, remove exactly one final server slash. Implicit and explicit root servers behave identically. Other repeated slashes and the operation's trailing slash remain intact; the path is never resolved as a relative URL or new authority. For example, `https://api.example/v1/` plus `/items/` yields `https://api.example/v1/items/`, while `/v1//` plus `/items/` preserves `/v1//items/`. OpenAPI specifies appending but leaves the exact boundary-byte rule ambiguous; this is the client's documented interpretation, not a claim that every service treats repeated slashes equivalently.

### Reusable finite forms and selected field inspection

`Form::new(fields, limits)` admits one immutable owned logical form for
`Input { body: Body::Form(form), ..Input::default() }`. Cloning a form shares its
storage. Exact values retain their original source and numeric tokens. Ordinary
values share one generated source. Constructing an owned form from borrowed bytes
copies those bytes once; supplying `Arc<[u8]>` retains that owner. The fluent
`.form(fields)` route keeps borrowed binary data through preparation and uses the
same private admitted view and encoder. Reuse checks the receiving operation's
logical limits without reparsing an existing exact owner. Cancellation is checked
before caller iterator conversion, before each pull, and throughout owned work;
it cannot preempt caller iterator or serialization code.

`operation.describe_form_field(media, name)` reports the selected field's
applicable default representation and original schema contributors. Both it and
`describe()` have cancellation counterparts. Missing schema resources return the
same `Diagnostic::resource_need()` as direct and fluent preparation. Supply bytes
or add layout through `DocumentBuilder`, build a new immutable snapshot, and
reacquire the operation. Old snapshots and their needs keep their original meaning.

The unpublished descriptor API now uses `SchemaDescription.profile` instead of
`edition`/`dialect`, and `BodyFieldDescription.schemas` instead of `schema`.
`BodyFields::Declared { fields, openness }` exposes an explicit openness hint.
`Partial` retains incomplete directly authored applicable sources; it does not
claim complete property selection. `FieldRequirement::UnknownHint` distinguishes
uncertainty from supported static nonmembership (`Optional`). Schema required
hints never enforce missing form fields. Successful descriptors retain source
owners, not the document graph; outward recovery needs intentionally own the
originating snapshot.

`representation: None` means representation facts were not requested.
`content_encoding: None` inside a representation means no applicable effective
coding: supported content paths prove absence, while an applicable style makes
coding irrelevant. Media election cannot erase unresolved relevant coding facts.
Supported schema references and compatible type unions supply finite declaration
facts; this is not instance validation or branch evaluation. Encoding algorithms,
MIME transfer headers, streams and general composition remain separate work.
Finite `f64` uses the existing explicit `ExactJson::from_serializable` boundary;
no direct floating-point `FormField` conversion was added.
### Response admission

Supplied transports receive immutable response bounds through
`dispatch.response_admission()`; the same view is available on prepared requests.
Adapters can report known metadata cutoff with `HeaderDelivery::Incomplete` while
preserving the finite response. JSON then refuses with `IncompleteHeaders`; no
transport failure is invented. See [HOSTS.md](HOSTS.md), the unpublished
[construction migration](MIGRATION.md), and `cargo run -p dynamic-openapi-client
--example response_admission` from the Rust workspace.

`Provenance::HostDelivered` explicitly interprets the host-exposed body. It permits
exact JSON parsing independently of Content-Encoding labels, without claiming
that the host decompressed them. Wire/Decoded/Unknown remain distinct and unchanged;
see [HOSTS.md](HOSTS.md) for the evidence boundaries and [MIGRATION.md](MIGRATION.md)
for the new exhaustive-match arm.
