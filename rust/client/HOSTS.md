# Supplied-host contract

The core enters a supplied `FnOnce` callback at most once. A callback may use a
platform that retransmits or repeats requests; callback entry alone cannot count
network attempts or prove server receipt. The host declaration, caller replay
permission, recorded admission, upload evidence, cancellation and application
acceptance are independent facts.

## Preparing a builder in a custom transport

Any transport can consume a checked `RequestBuilder` through the public
`advanced::prepare(request) -> (Result<PreparedRequest, Diagnostic>, Cancellation)`.
Native and Fetch use exactly this route; it needs no feature or privileged access.
Keeping the token outside the Result preserves independent cancellation evidence
even when preparation fails. It is the builder's selected token, or its fresh
uncancelled default. Preparation does not rerun user serialization or perform I/O.

Apply any transport-wide scoped credentials with `request.credential(...)` before
this call. Duplicate defaults/per-call assignments refuse instead of overriding.
The native companion applies configured credentials, in their configured order,
inside `send`, after the caller's eager setters and before `advanced::prepare`.
Custom transports using defaults should follow this order. Serialization can run
once before a credential duplicate is detected; preparation never reruns it.
The first setter failure remains primary, so a non-finite body takes precedence
over duplicate defaults. Assignment indices and aggregate-input byte accounting
also follow setter order: a default added after a body can exceed the budget in
`CallerInput` rather than the body's `Construction` context. This ordering is
client policy, not an OpenAPI requirement.
On success, custom hosts work from PreparedRequest and pass the returned token to
`invoke` or `invoke_with_replay`; retain the actual preparation Diagnostic on
failure. This strict callback example supports both Send and local host futures:

```rust
use dynamic_openapi_client::{advanced, Cancellation, Outcome, RequestBuilder};
use dynamic_openapi_client::transport::{HostCapabilities, PreparedRequest, TransportResult};
use std::future::Future;

async fn send<F, Fut>(request: RequestBuilder, capabilities: &HostCapabilities, host: F) -> Outcome
where
    F: FnOnce(PreparedRequest, Cancellation) -> Fut,
    Fut: Future<Output = TransportResult>,
{
    let (prepared, cancellation) = advanced::prepare(request);
    match prepared {
        Ok(prepared) => prepared.invoke(cancellation, capabilities, host).await,
        Err(error) => Outcome::not_dispatched(error, cancellation.is_cancelled(), None),
    }
}
```

A host needing host-managed replay must instead take an explicit ReplayPolicy
and use `invoke_with_replay`. Preparation grants no replay permission. An
application that prepares requests itself can still call `RequestBuilder::prepare`
and later choose an explicit execution token when handing them to a transport.

`HostCapabilities::admit` and `PreparedRequest::invoke` require
`ReplayPolicy::RequireNoReplay`. The explicit `admit_with_replay` and
`invoke_with_replay` methods accept a caller policy and share the same
implementation as those defaults. `AllowHostManaged` admits a false `no_retries`
declaration without modifying that declaration or weakening other capabilities.
A host that declares `no_retries` still records `NoReplayDeclared` under either
policy. Otherwise explicit permission records `HostManaged`.

```rust
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, exact::*, form::*, graph::*, inspect::*, schema::*, source::*, transport::*};
# async fn call(request: &PreparedRequest) -> Outcome {
let mut host = HostCapabilities::programmable();
host.no_retries = false; // This supplied callback cannot promise strict replay.
request.invoke_with_replay(
    Cancellation::default(), &host, ReplayPolicy::AllowHostManaged,
    |_, _| async {
        // Synthetic evidence; a real adapter declares its own fidelity.
        TransportResult {
            response: Some(ResponseInput::new(
                200, vec![], b"".as_slice().into(),
                BodyState::Complete, Provenance::Unknown,
            )),
            upload: UploadState::Unknown, error_detail: None,
        }
    },
).await
# }
```

Admission checks redirects, ambient authentication, target preservation, encoded
dot segments and forbidden supplied headers before replay; it also validates
representable target/header data before replay. `DiagnosticReason::Host` carries
controlled reasons, with no names or values. A Replay refusal identifies the
remaining **core** replay-policy obstacle; an adapter's subsequent preflight can
still refuse a method, body or backend representation. Cancellation already
observed before invocation takes precedence over admission.

`Outcome::replay_assurance()` is private recorded evidence. It is absent before
callback entry, including `Outcome::not_dispatched`, and retained after entry
for successful responses, HTTP errors, opaque redirects, missing/invalid evidence
and callback failures. Outcome is immutable; its borrowed reads preserve that
record and never manufacture upload completion or
remote cancellation. Arbitrary adapter detail remains explicit-access only.
External adapters may construct a controlled refusal using
`let error: Diagnostic = HostRefusal::ForbiddenHeader.into();`.

`HeaderFidelity` describes supplied structured metadata. `Unknown` asserts no
completeness, spelling/order or combination properties. `HostProcessed` permits
normalization, combination, ordering and filtering by the host. `ExactFields`
is an explicit assertion that received structured field names, value bytes,
duplicates and order are preserved without host omission. It does not promise
raw HTTP framing, whitespace, transport-protocol bytes or response trailers.

Callbacks receive an owned clone of `PreparedRequest`. Its body getter exposes
`Option<&Arc<[u8]>>`, so native hosts can share the payload without copying bytes.
Saving this request extends the lifetime of its operation, compiled declarations
and source graph. Extract needed transport facts and release the request when
those owners are unnecessary; retaining only a body Arc does not retain the graph.

`PreparedRequest::response_admission()` returns one immutable view for adapters: retained body bytes, header entries and
header bytes. These map to `Limits::body_bytes`, `Limits::headers` and the existing
`Limits::document_bytes` policy. Header bytes sum UTF-8 name bytes plus value bytes,
excluding HTTP framing. Inspect host-owned sizes before making core copies. An
exact count/byte limit requires observing EOF or one extra entry to distinguish
completion; stop on the first inadmissible entry. Host/parser buffering is separate.

`HeaderDelivery` is independent evidence about enumerating the fields exposed by
the host. `Unspecified` is the constructor default and adds no acceptance refusal;
`Complete` records observed EOF; `Incomplete` records a known cutoff. It cannot
establish absence of CORS filtering, combination, or other host transformations.
`Outcome::header_delivery()` preserves this supplied fact after core admission.
Core bounds may omit supplied entries under any fidelity declaration;
`omitted_header_lines()` counts only those actual entries despite its historical
name. Its zero value does not imply complete adapter delivery or wire fidelity.

A truthful adapter cutoff or any core omission leaves raw evidence usable and
alone does not create an `Outcome::error()` diagnostic or transport detail. JSON refuses with
`IncompleteHeaders`, including when the core omission count is zero. Omitted,
uninspected fields remain unclassified. Opaque evidence takes priority, followed
by invalid status/retained fields or the contradictory `ExactFields` plus
`Incomplete` assertion (`InvalidHostEvidence`); then known metadata incompleteness,
body completeness, media and coding checks. The named JSON reads retain
their separate execution-diagnostic/cancellation/evidence/status/decode priority and preserve
the decode result. Metadata cutoff/conversion failure alone must not be passed as
`TransportResult::error_detail`, which reports transport failure.

Complete decoded JSON can succeed with Unknown/HostProcessed fidelity and
Unspecified delivery. A combined invalid Content-Type is not heuristically split.
Body `Provenance` independently determines whether listed codings remain unresolved.
Nonopaque 3xx responses are ordinary evidence, not opaque redirects.

The native companion uses strict defaults and retains its existing never-replay
policy after serialization may have begun. Its proven-unsent connection recovery
exception is unchanged. HeaderMap evidence is HostProcessed. The disposable
Fetch qualification bridge also marks delivered metadata HostProcessed, but its
historical programmable declaration is not qualified as a Fetch replay promise.
The optional [Fetch companion](../fetch/README.md) declares Browser/Worker
restrictions and requires an explicit replay policy. Its complete host/resource
qualification and a supported TypeScript API remain open work. No H0 callback
test establishes those host or product capabilities.

The controlled `Unavailable`, `Method` and `RequestBody` host refusal reasons
support adapter preflight without arbitrary platform rejection text or fabricated
authored ranges. Prepared operation/source inspection remains available to callers.
The executable `response_admission` example demonstrates bounded supplied evidence
and corrective access without network I/O.

`Provenance::HostDelivered` interprets bytes exactly as exposed by the selected
host body stream, without claiming wire equality or content decompression. An
adapter selects it before parsing, consistently for all its delivered bodies.
The existing JSON methods retain Content-Encoding as metadata and parse these
bytes directly: valid JSON with stale gzip or an unknown label can succeed;
encoded binary fails exact parsing. No sniffing or decompression retry occurs.
All other admission and outcome guards apply. Exact JSON positions are relative
to these delivered bytes. Wire, Decoded and Unknown keep their existing semantics;
the native companion keeps Wire. A successful parse never upgrades provenance.

## Custom callback example

Apply it after invoking the supplied transport:

```rust
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, exact::*, form::*, graph::*, inspect::*, schema::*, source::*, transport::*};
async fn inspect_and_call(text: &[u8]) -> Result<(Outcome, Value), Box<dyn std::error::Error>> {
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
    let json = outcome.received_2xx_json_exact()?;
    Ok((outcome, json))
}
```

These are client host and acceptance policies. HTTP field/coding/status semantics
come from [RFC9110 §§5, 8, 15](https://www.rfc-editor.org/rfc/rfc9110#section-5);
2xx means successful HTTP status, not proven complete upload.
Matrix rows H01–H09 and R01–R02 in [CAPABILITIES.md](CAPABILITIES.md) distinguish
core evidence tests, native socket gates and Fetch runtime qualification.
