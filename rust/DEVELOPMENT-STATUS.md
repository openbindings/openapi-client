# Development checkpoint — 2026-10-10

This checkpoint adopts the integrated Rust implementation as the development
line. It is unpublished, has incomplete product qualification, and is not an
API stabilization or release declaration. The autonomous development loop is
paused at the maintainer's direction. No package, tag or deployment is included.

The implementation source is commit
`244c6c5b3b932a847495709c2dd9baec3fd8d7ef`; the landing adds status documentation.

## Implemented scope

- One JSON/YAML document API for Swagger 2.0 and OpenAPI 3.0, 3.1 and 3.2,
  retaining authored source and edition-specific interpretation.
- Immutable caller-supplied resource graphs, source-aware diagnostics and typed
  missing-resource/layout recovery; schema identities, dialects, anchors and
  bounded declaration-fact inspection.
- Finite JSON/raw bodies, owned or borrowed named URL-encoded/multipart forms,
  supported logical parameter styles and JSON/text content parameters through
  the same preparation and invocation APIs.
- Explicit replay, response-metadata and body-provenance contracts; optional
  native HTTP and Browser/Worker Fetch companions. Fetch qualification is partial.

The [API guide](client/README.md), [forms](client/FORMS.md),
[serialization](client/SERIALIZATION.md), [graphs](client/GRAPH.md),
[schemas](client/SCHEMAS.md) and [host contracts](client/HOSTS.md) describe the
supported profiles. Edition acceptance does not imply complete feature coverage.

## Existing verification and limits

At the implementation commit, recorded verification includes 393 passing
workspace tests/doctests, 357 passing combined-feature core tests/doctests,
strict Clippy, formatting, a Fetch Wasm compile, and 844 passing observations
from consumers of the actual packaged crate across four Serde feature graphs.
The separately selected provider-preparation test also passed. These results
apply to their recorded source, environment and workloads; landing CI supplies
fresh repository-level checks.

Additional bounded allocation, ownership, retained-source and binary-form reuse
checks pass. They do not close the complete forms, host or performance matrices.
The corrected public semantic tranche has 48 passing cases; its original four
harness expectation failures remain preserved. Its independent final review is
unfinished.

The fresh six-case public loading timing comparison has five passing cases and
one stable failure: empty-document median ratio **1.065129** exceeds the original
**1.05** ceiling. No distribution triggered the registered noise rule. This
failure is retained; allocation improvements do not turn it into a timing pass.
Earlier core-loading, serialization and large-form performance failures remain
open or require qualification against the integrated source.

Fetch evidence includes successful browser/worker executions and warm cycles
returning to baseline, but recorded cold-memory residuals and substantial
unexecuted lifecycle/resource controls remain. There is no complete host pass.

The full-product capability ledger remains **21/214**, with **0/4** eligible
converged corpus waves. These are the last qualified ledger entries, not a
percentage estimate of implementation completeness. The prior qualified ledger
candidate remains `90650da07a9f2ae453040ea3bc46c7492aa35537`; source adoption does
not promote its qualification record.

## Preserved unfinished work

The final three-site empty-loading optimization is preserved separately on
`codex/openapi-empty-load-unqualified`, commit
`39ae7fa9ef34aab5e2a5f5a931050f6994d53162`.
It is excluded from this landing: its test build ran out of disk space before
tests executed, its source review is incomplete, and performance is unmeasured.
The original patch, build failure and partial review are preserved.

The workspace evidence remains under
`design/openapi-development-loop-2026-10-10/`, including `state.json`, the
round-three qualification packets and `closeout-2026-10-10/`. Those local records
retain source hashes, commands, failed attempts, exact denominators and review
scope. They are not published packages or a complete portable release kit.

## Remaining product work

Full standards/capability coverage, fresh corpus convergence, remaining forms
and serialization profiles, resource acquisition, schema evaluation, streaming,
native content decoding and complete host qualification remain open. A supported
first-class TypeScript API is not yet implemented. Public API ergonomics and
performance qualification remain separate from this source landing.

Future work requires a new assignment; this checkpoint schedules no continuation.
