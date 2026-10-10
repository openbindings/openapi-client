# Supplied-host contract

The core enters a supplied `FnOnce` callback at most once. A callback may use a
platform that retransmits or repeats requests; callback entry alone cannot count
network attempts or prove server receipt. The host declaration, caller replay
permission, recorded admission, upload evidence, cancellation and application
acceptance are independent facts.

`HostCapabilities::admit` and `PreparedRequest::invoke` require
`ReplayPolicy::RequireNoReplay`. The explicit `admit_with_replay` and
`invoke_with_replay` methods accept a caller policy and share the same
implementation as those defaults. `AllowHostManaged` admits a false `no_retries`
declaration without modifying that declaration or weakening other capabilities.
A host that declares `no_retries` still records `NoReplayDeclared` under either
policy. Otherwise explicit permission records `HostManaged`.

```rust
use dynamic_openapi_client::*;
# async fn call(request: &PreparedRequest) -> Outcome {
let mut host = HostCapabilities::programmable();
host.no_retries = false; // This supplied callback cannot promise strict replay.
request.invoke_with_replay(
    Cancellation::default(), &host, ReplayPolicy::AllowHostManaged,
    |_, _| async {
        // Synthetic evidence; a real adapter declares its own fidelity.
        TransportResult {
            response: Some(ResponseInput::new(200, vec![], DeliveredBody {
                bytes: b"".as_slice().into(), state: BodyState::Complete,
                provenance: Provenance::Unknown,
            })),
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
and callback failures. Caller mutation of public outcome fields does not rewrite
it. Acceptance policies preserve it and never manufacture upload completion or
remote cancellation. Arbitrary adapter detail remains explicit-access only.
External adapters may construct a controlled refusal using
`let error: Diagnostic = HostRefusal::ForbiddenHeader.into();`.

`HeaderFidelity` describes supplied structured metadata. `Unknown` asserts no
completeness, spelling/order or combination properties. `HostProcessed` permits
normalization, combination, ordering and filtering by the host. `ExactFields`
is an explicit assertion that received structured field names, value bytes,
duplicates and order are preserved without host omission. It does not promise
raw HTTP framing, whitespace, transport-protocol bytes or response trailers.

Core metadata bounds may omit supplied entries under any fidelity declaration;
`omitted_header_lines()` counts those entries despite its historical name. Its
zero value does not rule out filtering upstream. JSON decode refuses incomplete
retained metadata. Complete decoded JSON can succeed with Unknown/HostProcessed;
a combined invalid Content-Type is not heuristically split. Body `Provenance`
independently determines whether listed content codings remain unresolved.
Nonopaque 3xx responses are ordinary evidence, not opaque redirects.

The native companion uses strict defaults and retains its existing never-replay
policy after serialization may have begun. Its proven-unsent connection recovery
exception is unchanged. HeaderMap evidence is HostProcessed. The disposable
Fetch qualification bridge also marks delivered metadata HostProcessed, but its
historical programmable declaration is not qualified as a Fetch replay promise.
A truthful supported Fetch companion, runtime characterization and TypeScript
ownership/recovery APIs remain separate work. No H0 callback test establishes
those host or product capabilities.
