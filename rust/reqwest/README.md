# Optional native HTTP transport

`dynamic-openapi-client-reqwest` connects the independent core's prepared requests to native Tokio HTTP/1.1 and HTTPS. Configure it once, use the existing core builder, then execute without per-call transport callbacks.

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
use dynamic_openapi_client::{
    Cancellation, Credential, CredentialValue, Document, ExactJson, Limits,
    ParameterLocation, policies,
};
use dynamic_openapi_client_reqwest::Client;
use serde::{Deserialize, Serialize};

#[derive(Serialize)]
struct Create<'a> { name: &'a str, labels: [&'a str; 2] }
#[derive(Deserialize)]
struct Created { id: u64, name: String }

# async fn run(source: &[u8], origin: String) -> Result<(), Box<dyn std::error::Error>> {
let document = Document::parse(source, None, Limits::default())?;
let operation = document.operation_id("create")?;
let client = Client::builder().credential("bearer", Credential {
    value: CredentialValue::Bearer("explicit-caller-token".into()),
    origins: vec![origin],
}).build()?;
let body = ExactJson::from_serializable(
    &Create { name: "example", labels: ["local", "typed"] }, Limits::default(),
)?;
let request = client.request(&operation)
    .parameter(ParameterLocation::Query, "mode", "normal")
    .json(body.root()).prepare()?;
let outcome = client.execute(&request, Cancellation::default()).await;
match policies::received_2xx_json(outcome) {
    Ok(accepted) => {
        let created: Created = accepted.json.deserialize()?;
        println!("{}: {}", created.id, created.name);
        // accepted.outcome still records Unknown upload completion.
    }
    Err(refused) => {
        // Non-2xx JSON can be available in refused.decoded.
        // Raw bytes/status/headers remain in refused.outcome.response.
        eprintln!("{}", refused);
    }
}
# Ok(()) }
```

Typed projection follows ordinary Serde numeric semantics: floating-point targets can round exact JSON numbers, and out-of-range integer targets refuse. Keep `accepted.json` and its exact tokens when that distinction matters. Finite float input serializes its existing value; conversion cannot recover precision already lost before the call.

Cancel an in-flight call by passing a cloned `Cancellation` to `execute` and calling `cancel()` on the retained handle from another task. This ends the local wait and preserves available response evidence. The complete example triggers cancellation only after its local server receives the request:

```sh
cargo run --manifest-path rust/Cargo.toml -p dynamic-openapi-client-reqwest --example real_http
```

Run this command from the repository root. The [example source](examples/real_http.rs) separates the caller flow from server setup and needs no external service or credentials.

Credentials enter core preparation and its origin checks. The adapter never injects authentication afterward. Repeated configured credentials for one scheme retain the core's duplicate-assignment refusal. Choose security alternatives and per-call inputs through the existing request builder. Callers provide a Tokio runtime; the core itself requires no HTTP client or executor.

The client reuses eligible HTTP/1.1 connections, configuration and TLS state. Clones share the pool; independently built clients have separate pools and trust settings. Each origin retains at most eight idle connections, with a 90-second idle timeout. This does not cap active concurrency, the number of origins, or total connections across origins. Completing an application call does not promise that its connection is already idle; the backend can race pool checkout with connection establishment.

The backend may recover an original request that it proves was never serialized when an idle connection is unusable. It never automatically repeats a request after serialization began or may have begun. Proven-unsent recovery can involve more than one connection or internal dispatch attempt within the existing total deadline. It does not authorize retries of status responses, partial writes, body failures, deadline failures after start or ambiguous outcomes. The outer reqwest retry policy remains disabled. Exact reqwest, hyper and hyper-util dependency constraints keep the qualified request-ownership behavior in ordinary consumer resolution; backend upgrades require renewed source and socket qualification.

Redirect following, system proxies, automatic cookies/authentication, referrer generation, TLS early data and transparent gzip/brotli/deflate/zstd decoding remain disabled. Normal certificate and hostname validation remains enabled, with optional explicit additional trust roots. Default deadlines are 30 seconds through body delivery and 10 seconds for connection establishment.

Before dispatch, the adapter refuses targets that its URL parser would change, including normalized encoded dot segments, altered host/scheme spelling and removed explicit default ports. It also refuses caller-controlled Host, Content-Length, Transfer-Encoding, Connection, Proxy-Connection, Proxy-Authorization, Keep-Alive, Upgrade, TE, Trailer and Expect fields. The backend generates Host/body framing and adds its default `Accept: */*` when none was prepared. Admitted request path/query and entity bytes are preserved. Header names are normalized; header values and duplicate values are exposed through the HTTP stack's HeaderMap order. Original wire header spelling/global order and response trailers are not available. This is an explicit metadata fidelity boundary.

`Dispatched` means the core host callback was entered, not that a server received or executed the operation. Upload state is `Unknown` after backend execution starts, even when a complete response arrives. Preflight refusal and pre-cancellation have `NotStarted` upload evidence. The core's strict upload-completion acceptance policy therefore remains strict; its separate received-response policy is available when accepting a complete response without known complete upload is the caller's intent.

Response status, headers and retained bytes survive non-2xx results, redirects, truncation, late read failures, deadlines and cancellation. No `error_for_status` conversion discards the response. Retained body length is bounded by the prepared request's `Limits::body_bytes`; a further data/EOF observation distinguishes exact-limit completion from truncation. Network/TLS/parser buffering is separate from this retained-byte bound. The HTTP header parser has a 256-field cap, with backend buffer bounds and the core's final metadata admission in addition.

Connection eligibility follows the HTTP backend's framing and lifecycle checks. A fully consumed, correctly framed response can leave a reusable connection even if the application refuses its status, JSON, metadata or retained-byte limit. Dropping an incomplete body lets the backend drain immediately available framing or close the connection; unread response bytes must not contaminate a later response. Cancellation or adapter refusal does not promise unconditional socket eviction.

Delivered bytes are unchanged entity representation bytes (`Provenance::Wire`), after normal HTTP transfer framing removal. Content codings remain encoded; gzip bytes do not silently become JSON. Cancellation races the actual request/body waits via `Cancellation::cancelled()`, preserves available response evidence and ends the local wait. It never asserts remote rollback or upload completion. Arbitrary backend error text, targets and header values are not formatted by adapter failures; fixed explicit-access detail labels identify the failure stage.

This unpublished optional companion supports the same finite OpenAPI operation scope as the core. It adds no schema evaluator, OpenBindings dependency, OAuth acquisition, protocol expansion, application retry policy or performance guarantee. The proven-unsent recovery exception above is the only permitted automatic request recovery.
