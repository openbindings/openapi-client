# Maintained client API contract

Status: the unpublished Rust development line. This is not an API stabilization
or full-edition conformance claim. Go and TypeScript are legacy and are not the
maintained client; their preserved API snapshots describe that separate line.

## Product promise

The client interprets Swagger 2.0 and OpenAPI 3.0.x, 3.1.x and 3.2.x documents
under their own edition semantics. The OpenAPI specifications and the RFCs
they rely on are the only behavioral authority. Unspecified choices are the
client's [documented policies](policies.md), configurable where the existing
API supports it. Downstream specifications cannot introduce client vocabulary,
errors, invocation shapes or release gates.

## Current public flow

The [Rust API guide](../rust/client/README.md) documents the actual surface:

- `Document::parse` accepts supplied JSON/YAML bytes, an optional retrieval
  identity and limits. It performs no I/O.
- `graph::DocumentBuilder` creates immutable snapshots from supplied resources.
  Missing resources produce corrective needs; callers supply bytes and build
  a new snapshot.
- Document and operation handles expose inventory, authored inspection and
  effective descriptions. Operation selection uses IDs or explicit path/method
  selection; ambiguous IDs are refused.
- `Operation::request` builds calls through owning checked setters:
  `parameter(location, name, &value)`, `json(&value)` and `form(&value)`.
  `parameter_exact` and `json_exact` retain `exact::Value` handles for exact data;
  `form_fields` accepts explicit `form::FormField` value/exact/part/parts inputs.
  `prepare()` returns a `transport::PreparedRequest` or a `Diagnostic`.
- Use `server`, `media`, `security` and `server_variable` for explicit elections.
  Supply credentials with `Credential::bearer(token, origins)`,
  `basic(username, password, origins)` or `api_key(value, origins)`, then
  `credential(scheme, credential)`. Constructors require an explicit origin list.
- Preparation resolves supported servers, parameters, finite JSON/raw/named
  form bodies and scheme-named, origin-scoped supplied credentials. Ambiguous
  server, body-compatible media and security alternatives require selection.
- `PreparedRequest::invoke` admits a supplied host and enters its asynchronous
  callback at most once. Optional native HTTP and Fetch companions supply hosts.
- `Outcome` keeps dispatch, upload, cancellation, errors and available response
  evidence independent. `Response` retains status, admitted headers, delivered
  bytes and fidelity/provenance facts, including unsuccessful HTTP responses.

The [graph](../rust/client/GRAPH.md),
[schema inspection](../rust/client/SCHEMAS.md),
[serialization](../rust/client/SERIALIZATION.md),
[forms](../rust/client/FORMS.md) and [host](../rust/client/HOSTS.md) contracts
define the implemented profiles. Owning values preserve source and diagnostic
locations after originating handles are dropped. Inspection is not a promise
that every declaration can be prepared or executed.

The caller supplies the indices and concrete media that match its operation:

```rust
use dynamic_openapi_client::{Credential, Diagnostic, Operation, ParameterLocation,
    exact::Value, transport::PreparedRequest};
# fn prepare(operation: &Operation, exact: &Value, token: &str, origin: String)
#     -> Result<PreparedRequest, Diagnostic> {
operation.request()
    .server(0)
    .server_variable("region", "us")
    .media("application/json")
    .security(0)
    .credential("token", Credential::bearer(token, vec![origin]))
    .parameter(ParameterLocation::Query, "limit", &25)
    .json_exact(exact)
    .prepare()
# }
```

## Application policy

Invocation returns native evidence rather than imposing a universal application
success/failure mapping. `policies::complete_2xx_json` requires known complete
upload and a complete, uncancelled, transport-error-free 2xx JSON response.
`policies::received_2xx_json` permits unknown or incomplete upload while retaining
that fact. A caller may instead inspect `Outcome` directly. These optional
policies do not retry, validate schemas or establish remote completion.

Exact JSON tokens are preserved. Ordinary Serde projection has the numeric
semantics of the selected target type; callers retain exact values when
precision matters. No schema-driven default insertion or implicit retrieval is
part of preparation.

## Qualification boundary

Evidence must exercise supported Rust APIs, packaged consumers, relevant feature
graphs and actual hosts. Examples and tests must agree with implemented
profiles, refusals, ownership and native evidence. Full standards coverage,
streaming, acquisition, schema evaluation, complete host qualification and a
supported TypeScript facade remain open. The
[release contract](release-qualification.md) separates these claims from legacy
qualification and downstream integration schedules.
