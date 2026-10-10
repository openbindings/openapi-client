# Dynamic OpenAPI client

An independent Rust client for exact JSON/OpenAPI 3.1 documents and finite requests through caller-supplied transport. This unpublished foundation has bounded qualification and independently verified API repairs; its supported scope is explicit below.

`Document::parse` retains exact source and immutable owned handles. Inspect `Document::operations`, effective `Operation::parameters`, `Operation::inspect` and raw `Value` handles, then use `Operation::request` for ordinary calls. Use `Operation::prepare` with explicit `Input` when you already manage exact values and selections. `PreparedRequest::invoke` accepts a host capability declaration and an async callback. Native concurrent and host-local callbacks can use the same semantic types without a required executor.

Supported preparation covers OpenAPI 3.1.0–3.1.2, local JSON-Pointer protocol references, server/media/security elections, path/header simple and query/cookie form parameters, supplied scoped Basic/Bearer/API-key credentials, and finite exact JSON/raw bodies. Response metadata, delivered bytes/provenance, upload state, cancellation and decode failures remain independent.

Schemas and unknown fields are inspection data. The core performs no schema evaluation/default insertion, acquisition, automatic redirect/retry, streaming codec or OpenBindings adaptation. For native HTTP calls, use the optional [`dynamic-openapi-client-reqwest` companion](../reqwest/README.md); it owns the HTTP stack and configuration while this crate remains runtime-independent. Browser/workerd qualification glue is separate and is not a supported npm client.

Source and decoded strings are exact for Unicode scalar values. Unpaired surrogate escapes return a located limitation with source preserved. Numeric tokens remain exact; integer conveniences are checked, and binary64 conversion currently accepts exactly representable integer lexemes only. Mathematical conversion of decimal/exponent tokens remains explicit unsupported conversion. Null, empty-container and nested parameter values receive located refusals; empty strings are supported.

Supplied hosts must enforce no ambient authentication, retries or redirects, disclose target/header restrictions and content decoding, and bound delivered response bytes. The core can validate its own data and returned evidence; it cannot limit an arbitrary callback's hidden allocation or undo remote effects. Use explicit accessors carefully: raw targets, header bytes, source values and host error details can contain secrets. SDK Debug/Display views omit credential values.

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
            assert_eq!(request.method, Method::Get);
            TransportResult {
                response: Some(ResponseInput {
                    status: 200,
                    headers: vec![Header::new("Content-Type", "application/json")],
                    body: DeliveredBody {
                        bytes: br#"{"count":9007199254740993}"#.as_slice().into(),
                        state: BodyState::Complete, provenance: Provenance::Wire,
                    }, opaque_redirect: false,
                }), upload: UploadState::Complete, error_detail: None,
            }
        },
    ).await;
    // The boxed application failure keeps the full outcome and decode diagnostic.
    Ok(policies::complete_2xx_json(outcome)?)
}
```

`HostCapabilities::programmable()` is a caller promise for controlled callbacks, not proof about an arbitrary HTTP stack. `cargo run --example dynamic` executes the complete fixture flow. Real asynchronous I/O requires the caller's host/executor. `Response::json()` parses on each call: retain the returned ExactJson when reusing decoded data. For a different application policy, inspect raw `Outcome` directly and choose your own order/acceptance rule. Preserve dispatch, upload, cancellation, error and response facts; a response alone does not establish execution success. Calling the named policy is never implicit in `invoke`.

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

`RequestError::Input` retains a generated-source `ParseFailure`; `RequestError::Preparation` retains a builder or protocol `Diagnostic`. Both compose with ordinary Rust error chains. Construction/preparation performs no transport. Controlled callers continue to construct `Input` and call `Operation::prepare`; the builder lowers into that same primitive.

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

Generated ranges identify this constructed JSON, never the OpenAPI document. `SourceContext.id` distinguishes owners within one process/Wasm instance and is not a persistent or cross-instance identifier. Parse accepts externally supplied bytes and marks them Authored; this name is not a claim about their historical origin. Pre-serialization failures carry Construction context and no complete source; parse failures after serialization retain the generated source. Null is distinct from omitted members and Body::Absent. Parameter policy can still refuse null or nested/empty containers even though the JSON owner represents them faithfully.

## Effective parameter inspection

`operation.parameters()?` resolves effective path/operation declarations with the same local-reference, override and required-default rules as preparation, without compiling unrelated body/media/security requirements. Iterate the returned `Parameters` and use `name()`, `location()`, `required()`, `authored()`, `resolved()` and `schema()`. Each retained parameter keeps its original source owners independently of the operation. Reference inspection distinguishes authored use site, sibling overrides, chain and target. `schema() == None` does not imply no `content` declaration; resolved raw values remain available. Unsupported editions or invalid typed declarations refuse; raw `Operation::inspect`/`value` remain available. These views do not assert full document validity or schema conformance.

## Correcting a refusal

Use `Diagnostic::reason()`, `context()` and `source_context()` along with Code. `DiagnosticReason::Limit` identifies the configured limit and actual/maximum counts; Numeric distinguishes lexical form, signed/unsigned range and precision. F64Exact intentionally first requires an integer lexeme fitting i64: even the exactly representable 2^63 is outside that convenience's range. Conversion-character limits are a Limit(NumberCharacters). Ordinary i64/u64 construction is exact and does not route through f64.

A caller parameter refusal identifies its vector index, namespace and explicit-access name; an election identifies server/media/security/variable context. Located diagnostics include the owner/origin of their byte ranges. Caller-only or derived-target failures have no fictitious document location. Names, keys, pointers and explicitly accessed bytes can be sensitive. Default Number, ordinary-input and diagnostic Debug/Display exclude those values. ParseFailure exposes its Diagnostic through Error::source for ordinary Rust error chaining.

Diagnostic metadata is immutable: use `code()`, `location()`, `related()` and `setting()` accessors, and `Diagnostic::new(code)` for a category-only diagnostic. `ParseFailure::diagnostic()` and `source_bytes()` expose its immutable source pair; standard `Error::source` remains available. Wrap errors to add application context. Bounded duplicate context names may be omitted; `context_omitted_for_limit()` makes that explicit. See [migration notes](MIGRATION.md) for this unpublished API revision.

## Bounds and deliberate policies

Defaults admit 8 MiB documents/caller metadata, nesting 128, 500,000 JSON nodes, 20,000 operations, 128 reference steps, 4 MiB bodies, 64 KiB targets, 256 header lines and 1,024 cached operations. Caller input counts and security requirements are bounded by the reference-work budget. Limits are per owner/call and may be configured; depth above 128 still refuses pending qualification. A byte-limit refusal does not copy an oversized input: `ParseFailure::source_bytes()` is empty in that case. Other admitted parse failures retain exact bytes. JSON indexing checks node admission before growing its owned work queue. Intermediate encoding allocation is bounded by admitted input bytes, even when the resulting target subsequently exceeds its smaller limit.

Cancellation is cooperative. Cancellable parse/prepare methods check owned work; invocation checks before dispatch and records cancellation after the callback returns. A callback can select `cancellation.cancelled().await` against its I/O to interrupt a pending local wait. Each wait has its own registration; cancellation wakes all waiters and dropping a wait unregisters it. The native companion does this automatically. Cancellation is irreversible and repeated requests are harmless. Future drop establishes no remote outcome. Response metadata over the count/byte limits produces `InvalidHostEvidence` and an explicit omitted-line count; available status and bounded body remain inspectable, and JSON convenience decoding refuses incomplete metadata.

Media election supports concrete types and matching declaration ranges, with explicit concrete selection required for range-only declarations. Malformed keys do not govern. More-specific matching declarations take precedence; equivalent best matches are ambiguous. Quoted media parameters are parsed as HTTP field parameters. Duplicate parameter names are refused. No codec or encoding metadata is applied to opaque schemas or raw caller bytes. UTF-8 is the explicit Basic credential policy. Header controls and surrounding whitespace are refused; header parameter values are not URI encoded. Cookies use `; ` between pairs.

`retained_bytes()` is a storage estimate for the immutable JSON owner, excluding document descriptors, compiled caches, allocator metadata and reserved pages. Qualification reports measure allocator traffic, live allocation sizes and process RSS separately. Exact value handles use the source owner's identity and do not imply equality across distinct documents.

## Corrective diagnostics and request choices

Default diagnostic text explains the failure in plain language and retains its structured code/reason; it omits document-controlled names and values. For an interactive UI that deliberately displays source locations, use `error.display_with_location()`. This opt-in formatter includes escaped JSON pointers and caller parameter/variable/scheme names, limited to 256 characters per displayed field. Names can contain sensitive data; the full source and typed facts remain available through explicit accessors. Default logging behavior remains value-safe.

Automatic request media selection considers the supplied body: `.json(...)` can uniquely select a JSON media type alongside incompatible XML/form choices. Multiple compatible JSON media types still require `Selection::media`. Raw bodies can use any supported concrete media and therefore retain their election requirements. Security is a separate policy: more than one authored alternative requires explicit `Selection::security`, even when only one credential is supplied or an anonymous alternative exists.

Completed operation-path segments equal to `.` or `..` refuse before dispatch, including segments assembled from template literals and parameter values. This is a preparation safety policy because normalizing HTTP stacks can route such paths elsewhere. Empty values/segments and embedded dots remain supported. Server-reference resolution is unchanged; transports still disclose and check percent-encoded-dot support and other URL normalization restrictions.
