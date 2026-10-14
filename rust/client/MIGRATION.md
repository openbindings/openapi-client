# Migrate the previous call API in one pass

This unpublished revision removes the old routes without compatibility aliases.
Update input, execution, outcome reads and imports together, then build examples
and run qualification. The [newcomer tutorial](README.md) uses the compiled
`load`, `inspect`, `send`, `read_response` and `correction` sources.

## Input and ordinary calls

| Old route | New route |
| --- | --- |
| `Input`, `ParameterInput`, `Body`, `Operation::prepare(input)` | `operation.request()` with setters, then native `client.send(builder).await` or explicit `builder.prepare()` |
| `OrdinaryValue` / `Argument` adapters | Borrow Serde data into `parameter(location, name, &value)`, `json(&model)` or `form(&model)` |
| `ExactJson::parse(...).root()` plus `Value` | `exact::Value::parse(...)`; the result is already a node handle |
| `ExactJson::from_serializable(...).root()` | `exact::Value::from_serializable(&model, limits)` |
| Exact input through ordinary setters | `parameter_exact(location, name, &value)` / `json_exact(&value)`; Value has no Serialize implementation |
| `Selection` and bulk assignment | `server(index)`, `media(type)`, `security(index)`, `server_variable(name, value)` |
| `Body::Absent` / raw body | `no_body()` / `raw_body(bytes)` |
| Public `Form`, `FormValue` and `FormField::new` | `form(&model)` or `form_fields` with `FormField::value`, `exact`, `part`, `parts` |
| `Part::value` / caller part headers | `FormField::exact` / supported Part bytes, filename and media metadata; no arbitrary part-header setter |
| Public `CredentialValue` construction | `Credential::bearer(token, origins)`, `basic(user, password, origins)`, `api_key(value, origins)`, then `credential(scheme, value)` |
| ParseFailure / RequestError / DeserializationError wrappers | `Diagnostic` everywhere; use `code`, `reason`, `context`, source accessors and explicit projection `detail()` |

Setters now admit and serialize eagerly, own the result, and share aggregate
input budgets. Set cancellation before setters when cancelled input must suppress
callbacks. Duplicate assignments refuse; the first refusal stays latched and
later work is suppressed. Correct with a fresh builder. Generated locations
belong to each setter's constructed source; remove assumptions about a shared
wrapper's `/0` path prefix. See [EXACT.md](EXACT.md) and [FORMS.md](FORMS.md).

## Execution and outcomes

| Old route | New route |
| --- | --- |
| Native `client.request(operation)` | `operation.request()` |
| Native `client.execute(prepared, token)` | Ordinary `client.send(builder).await`; inspected execution uses `reqwest::advanced::execute(&client, &prepared, token)` |
| Fetch `execute` / `execute_with_replay` | `client.send(builder, replay_policy)` or `fetch::advanced::execute(&client, &prepared, token, replay_policy)` |
| `companion::prepare` | Core `advanced::prepare(builder)` returns preparation Result and selected Cancellation, including on failure |
| Callback `DispatchRequest` | Owned `transport::PreparedRequest`; immutable getters, body is `Option<&Arc<[u8]>>` |
| `DeliveredBody` | `ResponseInput::new(status, headers, bytes, body_state, provenance)`; fidelity defaults Unknown and delivery Unspecified |
| Public Outcome fields / separate `Response` | Immutable Outcome accessors: `dispatch`, `upload`, `cancelled`, `error`, optional `status`, `headers`, `raw`, body/metadata facts |
| `CompletedJson`, JsonCallRefusal, CompleteJsonRefusal / composite policies | `received_2xx_json::<T>()`, `received_2xx_json_exact()` or independent `body_json()` |
| Mutable/custom diagnostic reasons | Wrap Diagnostic for application context; controlled `HostRefusal` converts into Diagnostic |
| Root native `Certificate` | `dynamic_openapi_client_reqwest::tls::Certificate` |

Native `send` appends configured credentials after eager caller setters, in
configured order; duplicates refuse instead of overriding. Advanced prepared
execution applies no defaults. Custom hosts follow [HOSTS.md](HOSTS.md).
Keeping the callback request retains its graph; clone only its body Arc when
payload retention alone is needed. Fetch requires an explicit replay election.
No companion handoff module or generic transport trait is needed.

Keep Outcome while reading: a refusal never consumes partial evidence. Actual
execution diagnostics now precede cancellation; without one, priority is
cancellation, invalid evidence, non-2xx status, body admission and projection.
Received-2xx reads accept Unknown/Incomplete upload. Applications requiring
Complete upload check it explicitly and define their own refusal priority.
`body_json()` can project a typed non-2xx body even when call acceptance refuses.
All reads share exact decoding; typed projection reruns. Update exhaustive
provenance matches for `HostDelivered`, which interprets host-exposed bytes without
claiming wire equality or decompression. See [the outcome example](../reqwest/examples/read_response.rs).

## Imports and inspection

Advanced names moved from root reexports into `exact`, `inspect`, `source`,
`graph`, `schema`, `form`, `transport` and `diagnostics`. The root retains
Document, Operation, RequestBuilder, Credential, ParameterLocation, Header,
Outcome, Cancellation, Diagnostic and Limits.

| Old route | New route |
| --- | --- |
| `Parameters` iterator wrapper | `Vec<inspect::Parameter>` from `parameters()` and effective descriptions |
| `parameter.schema()` | `parameter.schema_description().map(|description| description.value)` |
| SchemaReferences / DialectCapability | Owning `references()` iterator / `keywords_supported()` boolean, which does not assert validation |
| SchemaDescription edition/dialect fields | `SchemaDescription.profile` |
| RequestBodyDescription fields / BodyFieldDescription schema | Per-media `MediaDescription.fields` / contributor list `BodyFieldDescription.schemas` |
| Boolean form required / undifferentiated opaque fields | `FieldRequirement` including UnknownHint; `BodyFields::Declared { fields, openness }`, Partial, Opaque(issue), NotApplicable |
| Old raw-value/kind protocol resolver | `protocol_at(pointer, kind)`, then `document.resolve_protocol(&site)` |
| Descriptor width charged to reference_steps | `Limits::descriptor_work`; reference_steps still bounds each chain |
| Public retained-byte/owner counters | Unsupported non-default `qualification` hooks; no product telemetry replacement |

Resource needs use optional `requested_uri`, `reference_kind`, `edition` and
`target_pointer`, plus typed `interpretation` and `fragment`. Schema and protocol
needs share recovery: supply bytes/layout with `graph::DocumentBuilder`, publish
a new snapshot and reacquire operations/sites/handles. Old needs and sites stay
bound to the old snapshot. Incomplete mounted discovery blocks ID lookup; explicit
path/method lookup can select unaffected operations. See [GRAPH.md](GRAPH.md).

Loading stays `Document::parse(bytes, retrieval_base, limits)`. Values from YAML
use normalized JSON ranges and separate authored source traces; never apply
`Value::location()` directly to original YAML. Direct Operation Object `$ref`
refuses with UnsupportedReferencePosition; raw inspection remains available.
Form descriptions and preparation now consume supported static schema facts,
without evaluation or enforcing schema required hints. See [SCHEMAS.md](SCHEMAS.md).

Build your migrated callers and examples, run their semantic cases, then follow
[the repository verifier](../README.md). Retain wire expectations and outcome
facts unless an OpenAPI/RFC rule or explicit client policy justifies a change.
