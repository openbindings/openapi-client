# Dynamic OpenAPI client

Load an OpenAPI description at runtime and call an operation with Rust data.
The core accepts supplied JSON/YAML descriptions in Swagger 2.0 and OpenAPI
3.0, 3.1 and 3.2 families. For native HTTP, pair it with
[`dynamic-openapi-client-reqwest`](../reqwest/README.md) and Tokio.
Both packages are unpublished; use Cargo path dependencies to `rust/client`
and `rust/reqwest` in your checkout. Typed models use Serde derives.

The four jobs below use six executable examples. The excerpts need the imports,
models and fixture setup in those files;
the native examples start their own loopback service and need no real credentials.
Run the commands from the repository root.

## 1. Load

[`load`](examples/load.rs) supplies description bytes and their retrieval
base. Loading does no I/O. The base resolves references and relative server URLs;
it is not a request-target override.

<!-- include: rust/client/examples/load.rs#load -->
```rust,ignore
let bytes = support::document("https://api.example");
let document = Document::parse(
    bytes,
    Some("https://api.example/openapi.json"),
    Limits::default(),
)?;
println!("discovery complete: {}", document.discovery_complete());
```
<!-- endinclude -->

Run `cargo run --manifest-path rust/Cargo.toml -p dynamic-openapi-client --example load`.
Values retain original JSON tokens or normalized YAML plus authored source traces.
For several supplied resources or a missing-reference correction, use
[the graph reference](GRAPH.md). [Loading](LOADING.md) defines the finite YAML profile.

## 2. List and describe

[`inspect`](examples/inspect.rs) enumerates `document.operations()` and
prints effective parameters, server variables, request media, responses and
security alternatives. For each operation:

<!-- include: rust/client/examples/inspect.rs#inspect -->
```rust,ignore
let d = operation.describe()?;
println!("{} {} {:?}", d.method.as_str(), d.path, d.operation_id);
```
<!-- endinclude -->

Run `cargo run --manifest-path rust/Cargo.toml -p dynamic-openapi-client --example inspect`.
Check `discovery_complete()` and `graph_issues()` alongside the known inventory.
An unresolved Path Item can hide duplicate IDs, so `operation_id(...)` refuses
incomplete discovery. Explicit path/method lookup can still select unaffected
operations. `parameters()` inspects effective parameters without compiling
unrelated body or security declarations. `inspect()` exposes authored data.
Descriptions can retain unsupported declarations; they do not guarantee a call
can be prepared. See [graphs](GRAPH.md) and [schemas](SCHEMAS.md).

## 3. Build and send natively

[`send`](../reqwest/examples/send.rs) selects `document.operation_id("create")?`
and creates a native `Client::new()?`. Inside its two-call loop:

<!-- include: rust/reqwest/examples/send.rs#send -->
```rust,ignore
let request = operation
    .request()
    .parameter(ParameterLocation::Path, "id", &7_u64)
    .parameter(ParameterLocation::Query, "mode", &mode)
    .parameter(ParameterLocation::Header, "X-Trace", &"trace-1")
    .json(&support::Create { name: "example" })
    .credential(
        "token",
        Credential::bearer("local-token", vec![origin.clone()]),
    );
let outcome = client.send(request).await;
```
<!-- endinclude -->

Run `cargo run --manifest-path rust/Cargo.toml -p dynamic-openapi-client-reqwest --example send`.
`parameter`, `json` and `form` check Serde input immediately and retain owned
values. Set `.cancellation(token)` before input setters to suppress callbacks
when already cancelled. The first refusal stays latched; a corrected call needs
a new builder. `.parameter_exact` and `.json_exact` preserve an existing
`exact::Value` without projection. See [exact values and Serde](EXACT.md).

Multiple usable servers, compatible media or security alternatives require
`.server(index)`, `.media(concrete_type)` or `.security(index)`. Credentials
are supplied by scheme and permitted origins; they never elect a security
alternative. These election and credential rules are client policies.
Configure shared credentials, deadlines and TLS roots in the
[native companion](../reqwest/README.md). For named forms and finite uploads,
use [forms](FORMS.md). Custom transports use the [host reference](HOSTS.md).

## 4. Read the outcome

[`read_response`](../reqwest/examples/read_response.rs) keeps the outcome and prints
status, retained headers/body and upload evidence before reading JSON.
For a successful received response:

<!-- include: rust/reqwest/examples/read_response.rs#success-body -->
```rust,ignore
let created: support::Created = outcome.received_2xx_json()?;
assert_eq!(created.id, 9_007_199_254_740_993);
println!("created: {}", created.name);
```
<!-- endinclude -->

For a non-2xx response, admit and project its body independently:

<!-- include: rust/reqwest/examples/read_response.rs#error-body -->
```rust,ignore
let body = outcome
    .body_json()
    .and_then(|json| json.deserialize::<support::ApiError>());
```
<!-- endinclude -->

Run `cargo run --manifest-path rust/Cargo.toml -p dynamic-openapi-client-reqwest --example read_response`.
`received_2xx_json::<T>()` requires admitted complete JSON, a 2xx status, and no
execution error or cancellation. Unknown or Incomplete upload is accepted and
remains visible; require `transport::UploadState::Complete` explicitly when your
application needs it. `received_2xx_json_exact()` applies the same rule and
returns an exact value. `body_json()` is independent of status and execution.
The example checks execution error/cancellation before treating a decoded error
body as a handled call. These acceptance rules are client policy.

Reads borrow the immutable outcome. A refused read leaves partial evidence
available. `status()`, `headers()` and `raw()` return `None` when no response
arrived. Empty bytes remain a received body. Actual execution diagnostics precede
cancellation, then invalid evidence, HTTP status and JSON/projection refusals.
Typed floating-point projection can round; exact tokens remain unchanged.
See [host evidence and body admission](HOSTS.md) and [Serde projection](EXACT.md).

## Follow a redirect

Neither the client nor its hosts follow redirects on their own. [`redirect`](../reqwest/examples/redirect.rs)
prepares a call and, for each redirect outcome, derives the next request and sends it as a new call:

<!-- include: rust/reqwest/examples/redirect.rs#follow-redirect -->
```rust,ignore
let mut request = prepared;
let mut outcome = advanced::execute(&client, &request, cancellation.clone()).await;
while let Some(next) = request.follow_redirect(&outcome)? {
    request = next;
    outcome = advanced::execute(&client, &request, cancellation.clone()).await;
}
```
<!-- endinclude -->

Run `cargo run --manifest-path rust/Cargo.toml -p dynamic-openapi-client-reqwest --example redirect`.
`follow_redirect` sends nothing and returns `None` when the outcome is final: no
response, a status other than 301, 302, 303, 307 or 308, or no `Location`. It
applies each status's method and content rule, keeps credentials on the same
origin, removes every credential and `Cookie` field on another origin, refuses
https to http and stops past `Limits::redirects` (default 20). These are client
policies ([CP25](../../docs/policies.md#cp25-explicit-redirect-step)). The loop
starts from a `PreparedRequest`, so supply credentials on the builder: the native
companion applies its configured credentials only in `send`. A browser exposes a
manual redirect only as an opaque response, which the step refuses with
`OpaqueRedirect`; native, Worker and custom hosts deliver real 3xx responses.
See [following a redirect](HOSTS.md#following-a-redirect).

## Correct a refusal

[`correction`](examples/correction.rs) deliberately supplies the unknown
query parameter `typo`. After preparation refuses and the document is dropped,
the owning diagnostic still identifies the correction:

<!-- include: rust/client/examples/correction.rs#correction -->
```rust,ignore
assert!(matches!(
    diagnostic.reason(),
    Some(DiagnosticReason::UnknownParameter)
));
assert!(
    matches!(diagnostic.context(), Some(DiagnosticContext::Parameter { name, .. }) if name == "typo")
);
```
<!-- endinclude -->

The example rebuilds with the declared `mode` parameter and verifies the target.
Run `cargo run --manifest-path rust/Cargo.toml -p dynamic-openapi-client --example correction`.
Use `code()`, `reason()` and `context()` for decisions. Located errors retain
owning source positions; `resource_need()` identifies missing bytes/layout.
Default diagnostic formatting omits names, values and arbitrary host/Serde text.
Explicit source, context, `detail()` and location display can expose sensitive data.

## Reference and supported profile

OpenAPI and its cited RFCs are the only behavioral authority. The single
[capability and policy matrix](CAPABILITIES.md) separates normative semantics
from client choices, links every row to a test or case, and records verification
scope. Edition admission is not full feature coverage. Preparation reads bounded
static schema facts for form representations; it does no instance validation,
default insertion or branch selection. Acquisition, remaining codecs,
nested/positional multipart and streaming remain open work.

Reference pages: [exact values and Serde](EXACT.md), [loading and source](LOADING.md),
[resource graphs](GRAPH.md), [schema inspection](SCHEMAS.md), [forms](FORMS.md),
[parameter serialization](SERIALIZATION.md), and [custom host contracts](HOSTS.md).
Their modules include the corresponding reference in rustdoc. Existing callers
should follow [one coordinated migration](MIGRATION.md).

For documentation changes, `python3 rust/qualification/docs.py` checks excerpts
against compiled sources and all matrix evidence paths. Use `--write` to refresh
excerpts after editing an example. The repository verifier runs this check.
