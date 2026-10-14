# Optional native HTTP transport

`dynamic-openapi-client-reqwest` connects the independent core's checked request builders to native Tokio HTTP/1.1 and HTTPS. Configure it once, use `Operation::request`, then send without per-call transport callbacks.

These packages are unpublished. From an external project, point Cargo at your checkout (replace the example paths):

```toml
[dependencies]
dynamic-openapi-client = { path = "../openapi-client/rust/client" }
dynamic-openapi-client-reqwest = { path = "../openapi-client/rust/reqwest" }
serde = { version = "1", features = ["derive"] }
tokio = { version = "1", features = ["macros", "rt-multi-thread"] }
```

The short caller flow uses a runtime-loaded OpenAPI document and ordinary typed data. The full executable includes a controlled local server:

```no_run
use dynamic_openapi_client::{Credential, Document, Limits, ParameterLocation};
use dynamic_openapi_client_reqwest::Client;
use serde::{Deserialize, Serialize};

#[derive(Serialize)]
struct Create<'a> { name: &'a str, labels: [&'a str; 2] }
#[derive(Deserialize)]
struct Created { id: u64, name: String }

# async fn run(source: &[u8], origin: String) -> Result<(), Box<dyn std::error::Error>> {
let document = Document::parse(source, None, Limits::default())?;
let operation = document.operation_id("create")?;
let client = Client::builder().credential("bearer", Credential::bearer("explicit-caller-token", vec![origin])).build()?;
let body = Create { name: "example", labels: ["local", "typed"] };
let request = operation.request()
    .parameter(ParameterLocation::Query, "mode", &"normal")
    .json(&body);
let outcome = client.send(request).await;
match outcome.received_2xx_json::<Created>() {
    Ok(created) => println!("{}: {}", created.id, created.name),
    Err(refused) => {
        // Status, headers, bytes and Unknown upload remain on outcome.
        // Decode a typed non-2xx body with outcome.body_json()?.deserialize().
        eprintln!("{}", refused);
    }
}
# Ok(()) }
```

Typed projection follows ordinary Serde numeric semantics: floating-point targets can round exact JSON numbers, and out-of-range integer targets refuse. Use `outcome.received_2xx_json_exact()` and its exact tokens when that distinction matters. Finite float input serializes its existing value; conversion cannot recover precision already lost before the call.

Cancel an in-flight call by selecting a cloned `Cancellation` with `request.cancellation(token)` before `send` and calling `cancel()` on the retained handle from another task. This ends the local wait and preserves available response evidence. The complete example triggers cancellation only after its local server receives the request:

```sh
cargo run --manifest-path rust/Cargo.toml -p dynamic-openapi-client-reqwest --example real_http
```

Run this command from the repository root. The [example source](examples/real_http.rs) separates the caller flow from server setup and needs no external service or credentials.

`send` consumes the builder, applies configured credentials once, and prepares and executes with its selected cancellation token. Preparation failures return a non-dispatched Outcome whose JSON acceptance read returns the actual corrective Diagnostic. `Client::new` and `ClientBuilder::build` also return Diagnostic; TLS roots use `tls::Certificate`.

Credentials enter core preparation and its origin checks. The adapter never injects authentication afterward. Repeated configured credentials or a configured/per-call assignment for one scheme retain the core's duplicate-assignment refusal. Choose security alternatives and per-call inputs through the existing request builder. Callers provide a Tokio runtime; the core itself requires no HTTP client or executor.

Request setters check and serialize eagerly, before `send` applies configured credentials in their configured order. A body serializer can therefore run once before a duplicate default or configured/per-call credential refuses; preparation does not run it again. The first setter failure stays primary, so a non-finite JSON body refuses before duplicate defaults are considered. Assignment indices and aggregate-input byte accounting follow this same order: adding a default after an admitted body can exceed the input budget at the credential setter (`CallerInput`), instead of during body serialization (`Construction`). These are client input policies, not OpenAPI requirements.

For deliberate execution after inspection, use `advanced::execute(&client, &prepared, cancellation)`. It applies no client credential defaults; supply them before preparation.

The client reuses eligible HTTP/1.1 connections, configuration and TLS state. Clones share the pool; independently built clients have separate pools and trust settings. Each origin retains at most eight idle connections, with a 90-second idle timeout. This does not cap active concurrency, the number of origins, or total connections across origins. Completing an application call does not promise that its connection is already idle; the backend can race pool checkout with connection establishment.

The backend may recover an original request that it proves was never serialized when an idle connection is unusable. It never automatically repeats a request after serialization began or may have begun. Proven-unsent recovery can involve more than one connection or internal dispatch attempt within the existing total deadline. It does not authorize retries of status responses, partial writes, body failures, deadline failures after start or ambiguous outcomes. The outer reqwest retry policy remains disabled. Exact reqwest, hyper and hyper-util dependency constraints keep the qualified request-ownership behavior in ordinary consumer resolution; backend upgrades require renewed source and socket qualification.

Redirect following, system proxies, automatic cookies/authentication, referrer generation, TLS early data and transparent gzip/brotli/deflate/zstd decoding remain disabled. Normal certificate and hostname validation remains enabled, with optional explicit additional trust roots. Default deadlines are 30 seconds through body delivery and 10 seconds for connection establishment.

Before dispatch, the adapter refuses targets that its URL parser would change, including normalized encoded dot segments, altered host/scheme spelling and removed explicit default ports. It also refuses caller-controlled Host, Content-Length, Transfer-Encoding, Connection, Proxy-Connection, Proxy-Authorization, Keep-Alive, Upgrade, TE, Trailer and Expect fields. The backend generates Host/body framing and adds its default `Accept: */*` when none was prepared. Admitted request path/query and entity bytes are preserved. Header names are normalized; header values and duplicate values are exposed through the HTTP stack's HeaderMap order. Original wire header spelling/global order and response trailers are not available. Delivered responses explicitly report `HeaderFidelity::HostProcessed`; this is an explicit metadata fidelity boundary.

`Dispatched` means the core host callback was entered, not that a server received or executed the operation. Upload state is `Unknown` after backend execution starts, even when a complete response arrives. Preflight refusal and pre-cancellation have `NotStarted` upload evidence. The borrowed received-2xx reads accept complete incoming JSON without requiring complete upload. Applications requiring complete upload must check that evidence explicitly.

The companion continues to use strict `admit`/`invoke` and offers no host-managed replay option. Entered callbacks report `ReplayAssurance::NoReplayDeclared`; native preflight refusals report no assurance. This records the admitted declaration, not observed request attempts.

Response status, headers and retained bytes survive non-2xx results, redirects, truncation, late read failures, deadlines and cancellation. No `error_for_status` conversion discards the response. Retained body length is bounded by the prepared request's `Limits::body_bytes`; a further data/EOF observation distinguishes exact-limit completion from truncation. Network/TLS/parser buffering is separate from this retained-byte bound. The HTTP header parser has a 256-field cap. Before copying its borrowed names/values, the adapter applies `prepared.response_admission().header_entries()` and `.header_bytes()` (the existing `Limits::headers` and `Limits::document_bytes` policy). It retains a prefix, stops at the first inadmissible entry, and reports `HeaderDelivery::Incomplete`; exact-limit EOF reports `Complete`. An already buffered oversized backend field is not copied into a core Header. The core independently applies final admission. The delivery fact describes enumeration of the backend's exposed fields, independently of HostProcessed fidelity and body completion. Truthful metadata cutoff alone creates no transport error: JSON refuses with `IncompleteHeaders`, and raw bytes/status/retained headers remain usable. Core omission counts do not estimate an uninspected backend tail.

Connection eligibility follows the HTTP backend's framing and lifecycle checks. A fully consumed, correctly framed response can leave a reusable connection even if the application refuses its status, JSON, metadata or retained-byte limit. Dropping an incomplete body lets the backend drain immediately available framing or close the connection; unread response bytes must not contaminate a later response. Cancellation or adapter refusal does not promise unconditional socket eviction.

Delivered bytes are unchanged entity representation bytes (`Provenance::Wire`), after normal HTTP transfer framing removal. Content codings remain encoded; gzip bytes do not silently become JSON. Cancellation races the actual request/body waits via `Cancellation::cancelled()`, preserves available response evidence and ends the local wait. It never asserts remote rollback or upload completion. Arbitrary backend error text, targets and header values are not formatted by adapter failures; fixed explicit-access detail labels identify the failure stage.

This unpublished optional companion supports the same finite OpenAPI operation scope as the core. It adds no schema evaluator, OAuth acquisition, protocol expansion, application retry policy or performance guarantee. The proven-unsent recovery exception above is the only permitted automatic request recovery.
