# Client policy register

The OpenAPI specifications and the RFCs they rely on are the client's only
behavioral authority. This register records existing Rust choices where those
standards do not determine a unique client behavior or host representation.
It introduces no new behavior. The single [capability and policy matrix](../rust/client/CAPABILITIES.md) records supported shapes, normative authority, implementation evidence and verification scope. Standard-mandated edition interpretation is
outside this register. Every entry cites current documentation and/or existing
implementation/tests. Unsupported profiles are limits, not complete-edition
conformance claims.

Configuration means an existing caller control, not a proposed switch. Some
choices are fixed; the API does not currently make every policy configurable.
A host declaration records capabilities and cannot make an unfaithful host
faithful. See [the API contract](public-api-v1.md) for the supported call flow.

## P01

No implicit redirect following. Core admission requires a host that does not follow redirects; native HTTP and Fetch use manual redirects. Nonopaque 3xx responses remain evidence.

Configuration: Fixed for admitted invocation; there is no redirect-follow switch. The caller can inspect a response, derive the next request with `PreparedRequest::follow_redirect` under [CP25](#cp25-explicit-redirect-step), and invoke it as another admitted call.

Evidence: [Host contract](../rust/client/HOSTS.md), [host restriction tests](../rust/client/tests/host_contract.rs) (`non_replay_restrictions_precede_replay_and_opt_in_never_relaxes_them`), [native redirect test](../rust/reqwest/tests/real_http.rs) (`redirects_are_evidence_and_never_followed`), [redirect step tests](../rust/client/tests/redirect_step.rs) (`each_hop_is_a_separate_admitted_call_and_the_step_never_sends`).

## P02

No ambient authentication. Credentials are explicitly supplied by scheme name and are scoped to permitted origins. Native automatic cookies/authentication and system proxies are disabled; Fetch omits ambient credentials.

Configuration: Use `Credential::bearer(token, origins)`, `basic(username, password, origins)` or `api_key(value, origins)`, then `credential(scheme, credential)`. Ambient authentication cannot be enabled through core admission or these companions.

Evidence: [API guide](../rust/client/README.md), [native host profile](../rust/reqwest/README.md), [Fetch profile](../rust/fetch/README.md), [host admission tests](../rust/client/tests/host_contract.rs).

## P03

Core enters a supplied callback at most once and never retries. Default admission requires a no-replay declaration. Such a declaration permits proven-unsent recovery, but no repetition after serialization began or may have begun.

Configuration: `ReplayPolicy::AllowHostManaged` explicitly admits a host without the no-replay declaration, preserving all other restrictions and recording host-managed assurance. It does not bound network attempts or enable a core retry loop.

Evidence: [Host contract](../rust/client/HOSTS.md), [replay tests](../rust/client/tests/host_contract.rs), [native recovery profile](../rust/reqwest/README.md).

## P04

Automatic server election accepts exactly one usable effective server. Multiple usable servers produce `AmbiguousSelection`; authored order is not a tie-breaker.

Configuration: Use builder `server(index)` and `server_variable(name, value)` setters. The automatic election rule is fixed.

Evidence: [Selection and server implementation](../rust/client/src/prepare.rs) (`choose_server`), [selection recovery tests](../rust/client/tests/quality_repairs.rs) (`r09_corrective_input_selection_and_limits`).

## P05

Automatic request media election counts concrete declarations compatible with the supplied body. A unique compatible declaration is selected; ambiguous choices refuse. A media range requires a concrete choice.

Configuration: Use builder `media(concrete_type)`. Selected media still must match the declaration and body representation.

Evidence: [Preparation policy tests](../rust/client/tests/preparation_policy.rs) (`automatic_media_election_counts_body_compatible_choices`, `explicit_media_selection_keeps_range_and_body_compatibility_rules`).

## P06

Multiple authored security alternatives require an explicit election, including an anonymous alternative. Supplied credentials never elect an OR branch.

Configuration: Use builder `security(index)` and provide every credential required by the selected alternative. The no-credential-election rule is fixed.

Evidence: [Preparation policy tests](../rust/client/tests/preparation_policy.rs) (`security_alternatives_require_an_election_independent_of_credentials`).

## P07

Duplicate decoded object/mapping keys refuse. JSON syntax fallback to YAML cannot bypass duplicate-name refusal. Caller-serialized JSON duplicate keys use the same exact-parser refusal. Direct forms reject repeated logical fields before their value callbacks with `InvalidSelection` / `Form(DuplicateField)`.

Configuration: Fixed; no first-key/last-key-wins mode.

Evidence: [Loading profile](../rust/client/LOADING.md), [exact parser](../rust/client/src/value.rs) (`exact::Value::parse`), [YAML duplicate branch](../rust/client/src/yaml.rs), [Serde duplicate test](../rust/client/tests/serializable.rs) (`duplicates_use_existing_exact_parser_with_complete_constructed_source`).

## P08

URI reference resolution applies RFC 3986 literal-dot removal without general normalization. Completed operation path segments equal to `.` or `..` refuse, including segments composed from template and input bytes. Encoded dots remain distinct and are preserved by the core.

Configuration: Literal completed-segment refusal is fixed. `HostCapabilities::encoded_dot_segments` declares whether a supplied host preserves encoded segments. Native HTTP refuses any target its URL parser changes, including normalized encoded dots; this has no opt-out.

Evidence: [URI implementation](../rust/client/src/uri.rs), [dot-segment tests](../rust/client/tests/preparation_policy.rs) (`complete_literal_dot_segments_refuse_including_composed_values`, `empty_embedded_and_encoded_segments_keep_their_exact_targets`), [native profile](../rust/reqwest/README.md).

## P09

JSON number tokens remain exact; YAML numeric normalization preserves mathematical value without binary floating-point conversion. Numeric conveniences are checked. Typed Serde projection follows the caller's target numeric type, which may round or refuse.

Configuration: Exact retention is fixed. Callers choose exact values, checked numeric conversion or ordinary typed projection and can configure conversion-work limits. No implicit lossy-conversion switch exists.

Evidence: [Loading profile](../rust/client/LOADING.md), [numeric conversion tests](../rust/client/tests/quality_repairs.rs) (`r08_numeric_reasons_and_exact_policy`), [Serde numeric tests](../rust/client/tests/serializable.rs) (`numbers_keep_exact_integer_and_finite_float_representation`), [native typed example](../rust/reqwest/README.md).

## P10

Loading uses the documented finite YAML 1.2 Core-schema profile: one document, string mapping keys, JSON-compatible standard tags and acyclic bounded aliases. Unpaired JSON surrogate escapes refuse. YAML 1.1 boolean spellings remain strings; merge tags are unsupported.

Configuration: Profile and Unicode refusal are fixed. Configure source/normalized-byte, node, depth, alias-step and number-conversion limits.

Evidence: [Loading profile](../rust/client/LOADING.md), [YAML tests](../rust/client/tests/yaml_loading.rs), [YAML adapter](../rust/client/src/yaml.rs), [JSON parser contract](../rust/client/src/value.rs).

## P11

Style-based URI spaces use `%20`; content query parameters and URL forms use `+`. Tuple encoding and style encoding retain their documented distinct spellings. Reserved expansion is component-safe and never recursively decodes percent triples.

Configuration: Fixed spelling/profile; authored style and applicable `allowReserved` choose the supported OpenAPI mode, not an arbitrary escaper.

Evidence: [Serialization profile](../rust/client/SERIALIZATION.md), [shared serialization tests](../rust/client/tests/shared_serialization.rs), [serializer](../rust/client/src/style.rs).

## P12

Ambiguous style delimiters refuse rather than inferring a service convention: brackets in deepObject names, raw dots in exploded labels and active space/pipe delimiters in collection members/keys.

Configuration: Fixed; no custom-delimiter inference or override. Choose a supported declaration/value representation.

Evidence: [Serialization profile](../rust/client/SERIALIZATION.md), [boundary tests](../rust/client/tests/serialization_s2a_boundaries.rs), [serializer](../rust/client/src/style.rs).

## P13

Empty style-based parameter collections refuse; empty form collections emit nothing. Empty scalars remain supported subject to completed path-segment checks.

Configuration: Fixed in the supported serializer; no empty-collection convention switch.

Evidence: [Serialization profile](../rust/client/SERIALIZATION.md), [boundary test](../rust/client/tests/serialization_s2a_boundaries.rs) (`empty_collections_and_completed_dot_segments_keep_existing_policy`).

## P14

Work and retained data have explicit finite admission bounds. Source bytes and normalized YAML bytes are independently limited. Retained response bodies and headers have independent admission facts; host/parser allocation is outside these retained-byte promises.

Configuration: Set `Limits` and `GraphLimits` for their documented scopes. Adapters consume the immutable `response_admission()` view; they cannot turn it into a host-memory bound.

Evidence: [Loading bounds](../rust/client/LOADING.md), [graph bounds](../rust/client/GRAPH.md), [host admission contract](../rust/client/HOSTS.md), [limits implementation](../rust/client/src/value.rs), [response admission tests](../rust/client/tests/response_admission.rs).

## P15

Invocation preserves immutable native evidence. Borrowed JSON reads return actual execution diagnostics before cancellation, then invalid evidence, HttpStatus and body/projection errors. Body admission retains Unspecified-header acceptance; known metadata cutoff refuses with `IncompleteHeaders`.

Configuration: Choose `received_2xx_json` or `received_2xx_json_exact` for received 2xx acceptance with uncertain upload; `body_json` for independent body admission. Check Complete upload explicitly when required.

Evidence: [Outcome reads](../rust/client/src/transport.rs), [private strict policy example](../rust/client/tests/support/strict_json.rs), [outcome tests](../rust/client/tests/response_reads.rs), [host metadata contract](../rust/client/HOSTS.md), [admission tests](../rust/client/tests/response_admission.rs).

## P16

Loading and reference inspection use supplied bytes only. Missing resources produce corrective needs; loading never authorizes file/network retrieval or reuses invocation credentials. Preparation performs no schema evaluation or default insertion.

Configuration: Supply resources and retrieval identities through `graph::DocumentBuilder`, then build a new snapshot. Acquisition and any application validation/default policy remain caller-owned; there is no implicit-acquisition/default switch.

Evidence: [Loading profile](../rust/client/LOADING.md), [resource graph](../rust/client/GRAPH.md), [graph tests](../rust/client/tests/resource_graph.rs), [API guide](../rust/client/README.md).

## CP17: Corrective diagnostics

Structured reasons/context, owning source positions and safe formatting are client
contracts. Actual diagnostics survive Outcome reads; source/detail/location access
is explicit. Wrap errors to add application context. See matrix D01 and R02.

## CP18: Immutable ownership

Values and descriptors own their sources; graph snapshots stay immutable. Callback
requests retain the graph and share their body Arc. Borrowed Outcome reads share
exact decoding and run user projection outside the cache lock. API names, ownership
and cache layout are client choices. See matrix V01, G01, S01 and H01.

## CP19: Checked caller input

Eager Serde admission, aggregate bounds, duplicate refusal and first-error latching
are client choices. Native defaults are appended after caller setters without
implicit overriding. Select cancellation before setters; caller callbacks cannot
be preempted. See matrix V02, V03 and H02.

## CP20: Finite serialization and declaration facts

Only the documented flat style/content/form shapes are supported. Unsupported
codings, nested/positional multipart and unavailable relevant schema facts refuse.
Static facts do not validate instances, select branches or insert defaults. Within
an admitted minor family, the applied patch clarification is fixed and original
version spelling is retained. See matrix L01, S01, E01, T01, T02 and F01.

## CP21: Host representation and lifecycle

The native HTTP/1.1 and Fetch Browser/Worker profiles are client platform policies.
Deadlines, trust roots, bounded delivery, pool/recovery, replay elections and local
cleanup do not prove wire identity, remote completion or remote rollback. See
matrix H01–H09 for configuration and measured qualification scope.

## CP22: Inspection and discovery

Bounded inventory, snapshot identity, explicit fragment layouts, resource needs,
unknown-field tolerance and Path Item conflict handling are client policies around
the edition's declarations. Unknown schema dialects stay opaque. Supply missing
bytes/layout and reacquire handles after rebuilding. See matrix I01, G01 and S01.

## CP23: Application acceptance

Received-2xx reads permit uncertain upload; independent body reads permit non-2xx
inspection. Diagnostic/cancellation/evidence/status/decode priority is a client
contract. Applications can require Complete upload explicitly. See matrix R01/R02.

## CP24: Server/path boundary bytes

Resolve and expand the server, then append the complete operation path. At a
slash/slash boundary remove exactly one final server slash; preserve all other
repeated/trailing slashes. OpenAPI specifies appending without fixing those bytes.
This is client policy, configurable only through server/variable election. See B01.

## CP25: Explicit redirect step

`PreparedRequest::follow_redirect(&outcome)` derives the request that follows a 301, 302, 303, 307 or 308 response and sends nothing. No response, any other status (including 300, 304 and 305) or no `Location` field returns `None`: the outcome is final. 301 and 302 turn POST into GET without content and keep any other method and content; 303 turns every method but HEAD into GET without content, and HEAD stays HEAD; 307 and 308 keep the method and content. Dropping content also drops the `Content-Type`, `Content-Encoding`, `Content-Language` and `Content-Location` fields. These are the Fetch standard's rules, which [RFC9110 §15.4](https://www.rfc-editor.org/rfc/rfc9110#section-15.4) describes as where practice converged; following a redirect is optional there.

`Location` resolves against the request target as an [RFC3986 §5.2](https://www.rfc-editor.org/rfc/rfc3986#section-5.2) reference with [P08](#p08)'s literal dot-segment removal and no other normalization. The fragment is dropped, and the result must be an absolute http or https URI with a host and no userinfo. A redirect from https to http refuses.

On the same origin (scheme, host and effective port, compared as credential origins are) the request keeps its credentials, still within their origin grants. Query credentials are reapplied after the `Location`'s own query, so a redirect that preserves the query does not repeat them; a `Location` pair that names a query credential with another value refuses with `CredentialCollision`, as at preparation. On another origin the step removes every credential the request carries, wherever it was applied (header, query or cookie), together with every field the caller marked sensitive, every `Authorization` field (whose value consists of credentials, [RFC9110 §11.6.2](https://www.rfc-editor.org/rfc/rfc9110#section-11.6.2)) and every `Cookie` field. A removed credential does not return on a later hop.

Each derived request records how many redirects led to it; deriving one past `Limits::redirects` (default 20, the Fetch standard's limit) refuses. The step requires `HeaderDelivery::Complete` evidence with no core omission, so a truncated header list cannot hide or forge a `Location`, and it does not read the body. A Browser opaque redirect has no status or `Location`; the step refuses it with `OpaqueRedirect` instead of guessing.

Configuration: `Limits::redirects` bounds a chain. The method, content, credential, cookie and downgrade rules are fixed; to send a credential to another origin, prepare a new request with it. Each derived request is a separate call the caller chooses to make, through the same host admission as any request.

Evidence: [redirect step tests](../rust/client/tests/redirect_step.rs), [native chain](../rust/reqwest/tests/real_http.rs) (`redirect_step_follows_a_native_chain_explicitly`), [Fetch host probe](../rust/qualification/host/fetch-probes.mjs) (`redirect-step`), [host contract](../rust/client/HOSTS.md).

The optional companions impose additional documented host restrictions. Native
HTTP timeout and trust-root configuration is in the
[native guide](../rust/reqwest/README.md); Browser/Worker representation and
replay limits are in the [Fetch guide](../rust/fetch/README.md). A configurable
deadline or admission bound does not waive a fidelity restriction.
