# Checkpoint CI reconciliation

The first run for PR #73, at `949e636270dd5bac76ce9826c8e2c1e9c4fea781`,
passed formatting, lint and workspace tests but failed eight comparisons in
`fixtures/cases.json`. This amendment reconciles those comparisons with the
already integrated contracts. It changes no product implementation or API.
The original fixtures remain in that commit; the failed job and artifact are
retained in [the original CI run](https://github.com/openbindings/openapi-client/actions/runs/38099532781).
Historical case IDs are retained, including the two `unsupported` labels.

| Cases | Original expectation | Current expectation and basis |
| --- | --- | --- |
| `reference-anchor` | `InvalidPointer` | `InvalidReference`: the protocol Reference Object resolver requires a JSON Pointer fragment. `#named` is not a protocol pointer; schema anchors use the separate schema resolver. |
| `reference-external` | `ExternalReference` | `MissingResourceBase`: caller-supplied external resources are now supported, but anonymous source bytes cannot resolve `other.json#/P`. An API server URL does not supply a document retrieval base. |
| `parameter-{path,query,header,cookie}-empty-array` | `UnsupportedValue` | `UnsupportedParameter`: the shared style serializer deliberately reports `UnsupportedStyleCombination` at the parameter declaration when a collection is empty. Empty parameter collections remain refused; empty form collections may emit nothing. |
| `parameter-unsupported-content` | `UnsupportedParameter` | A successful GET with target `https://api.example/v1/items?q=v`, no headers and no body: the supported single-media text codec serializes the supplied scalar. |
| `parameter-unsupported-allowReserved` | `UnsupportedParameter` | The same successful request: supported reserved expansion leaves the unreserved scalar `v` unchanged. |

Each refusal now additionally asserts its exact declaration/reference source
range and pointer. The two successful cases assert method, complete target,
headers, body absence and body bytes. No case is deleted, skipped or converted
into an unconditional pass.

The implementation evidence is `Graph::target_address` / `Graph::address` and
the shared `style::walk` refusal branch. The existing
`empty_collections_and_completed_dot_segments_keep_existing_policy` test in
`client/tests/serialization_s2a_boundaries.rs` explicitly asserts the selected
empty-collection reason. This landing retains that classification; changing its
ergonomics would be a separate API decision. The supported wire profiles are
documented in [GRAPH.md](../client/GRAPH.md) and
[SERIALIZATION.md](../client/SERIALIZATION.md), with existing positive/negative
tests in `serialization_s2a.rs`, `shared_serialization.rs` and
`serialization_repairs.rs`.

OpenAPI 3.1.2 defines both parameter `content` and query `allowReserved` in its
[Parameter Object](https://spec.openapis.org/oas/v3.1.2.html#parameter-object).
Diagnostic enum names, refusal precedence and unsupported empty-collection
policy are this library's contract, not enum values prescribed by OpenAPI.

Existing performance failures and incomplete product qualification remain in
[DEVELOPMENT-STATUS.md](../DEVELOPMENT-STATUS.md). This reconciliation grants no
additional capability-ledger or corpus-convergence credit.

## Subsequent inventory

After the 217-case inventory passed, the next 45-case inventory exposed one
additional stale comparison, `response-metadata-count-bound`. Its supplied
response exceeds the core's one-header allowance. The current
[response-admission contract](../client/HOSTS.md) preserves the raw body and
records one omitted header; omission alone creates no transport/outcome error,
while JSON interpretation refuses with `IncompleteHeaders`. Existing
`client/tests/response_admission.rs` asserts this distinction.

The fixture now asserts absence of an outcome error and the separate decode
refusal, retaining its omission count and exact raw-body assertions. The
qualification adapter explicitly emits `error: null` for error-free outcomes so
absence is checked rather than left unasserted; real errors still replace that
field. No product behavior or host policy changes.
