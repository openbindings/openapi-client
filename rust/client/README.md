# Dynamic OpenAPI client

An independent Rust client for exact JSON/OpenAPI 3.1 documents and finite requests through caller-supplied transport. This unpublished foundation has bounded qualification and independently verified API repairs; its supported scope is explicit below.

`Document::parse` retains exact source and immutable owned handles. Inspect `Document::operations`, `Operation::inspect` and raw `Value` handles, then use `Operation::prepare` with explicit `Input` selections. `PreparedRequest::invoke` accepts a host capability declaration and an async callback. Native concurrent and host-local callbacks can use the same semantic types without a required executor.

Supported preparation covers OpenAPI 3.1.0–3.1.2, local JSON-Pointer protocol references, server/media/security elections, path/header simple and query/cookie form parameters, supplied scoped Basic/Bearer/API-key credentials, and finite exact JSON/raw bodies. Response metadata, delivered bytes/provenance, upload state, cancellation and decode failures remain independent.

Schemas and unknown fields are inspection data. The library performs no schema evaluation/default insertion, acquisition, automatic redirect/retry, streaming codec, production HTTP integration or OpenBindings adaptation. Browser/workerd qualification glue is separate and is not a supported npm client.

Source and decoded strings are exact for Unicode scalar values. Unpaired surrogate escapes return a located limitation with source preserved. Numeric tokens remain exact; integer conveniences are checked, and binary64 conversion currently accepts exactly representable integer lexemes only. Mathematical conversion of decimal/exponent tokens remains explicit unsupported conversion. Null, empty-container and nested parameter values receive located refusals; empty strings are supported.

Supplied hosts must enforce no ambient authentication, retries or redirects, disclose target/header restrictions and content decoding, and bound delivered response bytes. The core can validate its own data and returned evidence; it cannot limit an arbitrary callback's hidden allocation or undo remote effects. Use explicit accessors carefully: raw targets, header bytes, source values and host error details can contain secrets. SDK Debug/Display views omit credential values.

## A dynamic Rust call

The complete outcome is the application-policy boundary. This example requires dispatched, uncancelled execution without a transport error, a 2xx response, known complete upload and valid JSON. Unknown upload is a refusal **for this example**. A different application may accept an early response while preserving/reporting uncertainty. Success and failure both retain Outcome; failure also retains any decode error and successfully decoded data. A response alone does not establish successful execution.

The following policy is shipped as `examples/support/outcome_policy.rs` and exercised against successful JSON, late error, cancellation with a response, pre-dispatch cancellation, HTTP error and unknown upload:

```rust
use dynamic_openapi_client::*;

#[derive(Debug)]
pub enum PolicyRefusal {
    Cancelled,
    NotDispatched,
    Transport,
    MissingResponse,
    HttpStatus,
    UploadUncertain,
    Decode,
}
#[derive(Debug)]
pub struct CompletedCall {
    pub outcome: Outcome,
    pub json: ExactJson,
}
#[derive(Debug)]
pub struct CallFailure {
    pub reason: PolicyRefusal,
    pub outcome: Outcome,
    pub decoded: Option<ExactJson>,
    pub decode_error: Option<Diagnostic>,
}
impl std::fmt::Display for CallFailure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "application outcome policy refused: {:?}", self.reason)
    }
}
impl std::error::Error for CallFailure {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.decode_error
            .as_ref()
            .or(self.outcome.error.as_ref())
            .map(|e| e as _)
    }
}
pub fn accept(outcome: Outcome) -> Result<CompletedCall, Box<CallFailure>> {
    // Inspect/decode available data even when execution also failed. Never discard
    // Outcome: dispatch, upload, cancellation, transport and response are independent.
    let (decoded, decode_error) = match outcome.response.as_ref().map(Response::json) {
        Some(Ok(json)) => (Some(json), None),
        Some(Err(error)) => (None, Some(error)),
        None => (None, None),
    };
    let refusal = if outcome.cancelled {
        Some(PolicyRefusal::Cancelled)
    } else if outcome.dispatch != DispatchEvidence::Dispatched {
        Some(PolicyRefusal::NotDispatched)
    } else if outcome.error.is_some() {
        Some(PolicyRefusal::Transport)
    } else if outcome.response.is_none() {
        Some(PolicyRefusal::MissingResponse)
    } else if !outcome
        .response
        .as_ref()
        .is_some_and(|r| (200..300).contains(&r.status()))
    {
        Some(PolicyRefusal::HttpStatus)
    } else if outcome.upload != UploadState::Complete {
        // This application's explicit policy. Another application may accept
        // an early response while retaining/reporting upload uncertainty.
        Some(PolicyRefusal::UploadUncertain)
    } else if decode_error.is_some() {
        Some(PolicyRefusal::Decode)
    } else {
        None
    };
    if let Some(reason) = refusal {
        return Err(Box::new(CallFailure {
            reason,
            outcome,
            decoded,
            decode_error,
        }));
    }
    Ok(CompletedCall {
        outcome,
        json: decoded.expect("validated response JSON"),
    })
}
```

Apply it after invoking the supplied transport:

```rust
# mod policy { include!(concat!(env!("CARGO_MANIFEST_DIR"), "/examples/support/outcome_policy.rs")); }
# use policy::*;
use dynamic_openapi_client::*;
async fn inspect_and_call(text: &[u8]) -> Result<CompletedCall, Box<dyn std::error::Error>> {
    let document = Document::parse(text, Some("https://api.example/openapi.json"), Limits::default())?;
    let operation = document.operation_id("listItems")?;
    let request = operation.prepare(&Input::default())?;
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
    Ok(accept(outcome)?)
}
```

`HostCapabilities::programmable()` is a caller promise for controlled callbacks, not proof about an arbitrary HTTP stack. `cargo run --example dynamic` executes the complete fixture flow. Real asynchronous I/O requires the caller's host/executor. `Response::json()` parses on each call: retain the returned ExactJson when reusing decoded data.

## Constructing ordinary inputs

Use `ExactJson::from_ordinary` for the supported Rust types. It checks limits, serializes generated JSON bytes, then parses/indexes once. It is fallible and not zero-copy. This API accepts no floats or arbitrary Serialize implementations. Use exact authored JSON tokens through `parse` when those are the input; conversion conveniences never change stored tokens.

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

## Correcting a refusal

Use `Diagnostic::reason()`, `context()` and `source_context()` along with Code. `DiagnosticReason::Limit` identifies the configured limit and actual/maximum counts; Numeric distinguishes lexical form, signed/unsigned range and precision. F64Exact intentionally first requires an integer lexeme fitting i64: even the exactly representable 2^63 is outside that convenience's range. Conversion-character limits are a Limit(NumberCharacters). Ordinary i64/u64 construction is exact and does not route through f64.

A caller parameter refusal identifies its vector index, namespace and explicit-access name; an election identifies server/media/security/variable context. Located diagnostics include the owner/origin of their byte ranges. Caller-only or derived-target failures have no fictitious document location. Names, keys, pointers and explicitly accessed bytes can be sensitive. Default Number, ordinary-input and diagnostic Debug/Display exclude those values. ParseFailure exposes its Diagnostic through Error::source for ordinary Rust error chaining.

Diagnostic now has private detail storage: use `Diagnostic::new(code)` instead of external Diagnostic struct literals. Existing diagnostic fields remain readable/mutable; after changing a location manually, callers are responsible for keeping its owner metadata consistent. Consumers receiving SDK diagnostics do not need private implementation knowledge.

## Bounds and deliberate policies

Defaults admit 8 MiB documents/caller metadata, nesting 128, 500,000 JSON nodes, 20,000 operations, 128 reference steps, 4 MiB bodies, 64 KiB targets, 256 header lines and 1,024 cached operations. Caller input counts and security requirements are bounded by the reference-work budget. Limits are per owner/call and may be configured; depth above 128 still refuses pending qualification. A byte-limit refusal does not copy an oversized input: `ParseFailure::source` is empty in that case. Other admitted parse failures retain exact bytes. JSON indexing checks node admission before growing its owned work queue. Intermediate encoding allocation is bounded by admitted input bytes, even when the resulting target subsequently exceeds its smaller limit.

Cancellation is cooperative. Cancellable parse/prepare methods check owned work; invocation checks before dispatch and records cancellation after the callback returns. A callback must observe its token to interrupt its own wait. Future drop establishes no remote outcome. Response metadata over the count/byte limits produces `InvalidHostEvidence` and an explicit omitted-line count; available status and bounded body remain inspectable, and JSON convenience decoding refuses incomplete metadata.

Media election supports concrete types and matching declaration ranges, with explicit concrete selection required for range-only declarations. Malformed keys do not govern. More-specific matching declarations take precedence; equivalent best matches are ambiguous. Quoted media parameters are parsed as HTTP field parameters. Duplicate parameter names are refused. No codec or encoding metadata is applied to opaque schemas or raw caller bytes. UTF-8 is the explicit Basic credential policy. Header controls and surrounding whitespace are refused; header parameter values are not URI encoded. Cookies use `; ` between pairs.

`retained_bytes()` is a storage estimate for the immutable JSON owner, excluding document descriptors, compiled caches, allocator metadata and reserved pages. Qualification reports measure allocator traffic, live allocation sizes and process RSS separately. Exact value handles use the source owner's identity and do not imply equality across distinct documents.
