# OpenAPI release qualification

## Frozen candidate

The release candidate accepts exactly OpenAPI 3.0.0–3.0.4 and
3.1.0–3.1.2 client-invoked Path Item operations. It includes the native
TypeScript and Go clients and the `openbindings.openapi@1` adapters. OpenAPI
3.2, Swagger 2.0, code generation, link traversal, callbacks, webhooks, and
uninstalled vendor-extension behavior are not part of this release boundary.

The loop may fix an implementation defect in the lowest owning layer. It may
not add a Core field, alter the OBI document model, expand an immutable binding
profile, or infer behavior from one corpus producer. Evidence of such a need
stops release qualification for explicit design review.

## Local release gate

`pnpm qualify:release` is the repository-local gate. It requires:

1. TypeScript type checking and the complete standalone test suite;
2. the complete Go suite under the race detector;
3. production TypeScript builds and SDK-boundary inspection;
4. installation of the packed npm artifact into a clean external project;
5. both ESM and CommonJS consumption from that installed artifact; and
6. consumption of the Go package from a clean external module.

The consumer gate deliberately executes installed package entry points rather
than importing the workspace source tree. It catches missing files, invalid
exports, accidental workspace dependencies, and source-only success.

## System qualification gate

The OpenBindings workspace additionally runs the authority-derived wire
differential, TypeScript/Go adapter suites, exact synthesis-and-coverage parity,
the varied development corpus, sealed holdouts, and comparator mutation
self-checks. Those system reports remain in `corpus-lab`; they do not become a
runtime dependency of this standalone repository.

A release is qualified only when both the local and system gates are green,
unsupported semantics refuse before dispatch when knowable, and every new
failure is classified as artifact invalidity, boundary exclusion, engine
defect, adapter defect, binding-spec defect, or a separately reviewed possible
Core limitation.

## Current qualification result

The current candidate passes the local gate with 237 TypeScript tests, the Go
race suite, production boundary inspection, clean npm ESM/CommonJS installs,
and a clean external Go-module consumer. The OpenBindings system gate passes
580 TypeScript adapter tests and the full Go adapter race suite.

The authority suite contains 17 cross-layer wire cases. The standalone and
OpenBindings TypeScript lanes have zero wire or application mismatches, the Go
standalone and adapter lanes pass 17/17, and all four seeded comparator
mutations are detected. The independent Swagger witness disagrees on two
known, authority-adjudicated seams; it is not a production authority.

All 70 authority-derived semantic cells are assigned exactly once in the
release evidence ledger, with executable evidence in both languages. Across
the 170-artifact corpus, all 152 artifacts in the supported comparison
envelope have exact TypeScript/Go synthesis and coverage parity. Three sealed
holdout cohorts contain 66 supported artifacts with exact parity plus four
invalid-upstream tolerance observations, and no goal-relevant mismatch.

## Host policy and hostile artifacts

The clients propagate cancellation through artifact retrieval, external
reference closure, invocation, and streaming. They refuse malformed sources,
unsupported editions and URI schemes, duplicate YAML mapping keys, unresolved
or baseless references, ambiguous operations, media collisions, and invalid
configuration. Response delivery-unit limits prevent unbounded unary values or
individual SSE events.

Artifact fetch policy is intentionally host-owned. Applications processing
untrusted documents must provide a restricted `fetch` implementation or
`http.Client` for network allowlists, source-size limits, proxy policy, TLS,
and environment-specific file access. The runtime must not invent one global
security policy and present it as OpenAPI semantics.
