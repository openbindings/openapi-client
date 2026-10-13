# Conformance evidence and legacy fixtures

The maintained Rust client takes behavioral authority only from the OpenAPI
specifications and the RFCs they rely on. Its own unspecified choices are in
[the policy register](../docs/policies.md). A corpus supplies regression
evidence and cannot establish additional client rules.

Rust native tests live under `rust/client/tests`, `rust/reqwest/tests` and
`rust/fetch/tests`; portable Rust inventories live under
`rust/qualification/fixtures`. See [qualification](../docs/release-qualification.md)
for the existing runners and limits. Edition acceptance and finite fixture
success do not imply full feature or host coverage.

## This directory is legacy

All fixtures and ledgers under `conformance/`, including the entire `upstream/`
subtree, belong to the legacy Go/TypeScript line. Those implementations are
not the maintained client. The pinned corpus, schemas and historical upstream
README remain byte-for-byte locked for `pnpm authority:verify`; the processor
files also serve `pnpm test:corpus` through the public legacy TypeScript client.
The synthesis fixtures are retained lock inputs, not a Rust synthesis gate.

`cases/native-wire.json`, `cases/required-declarations.json` and
`json-response-numbers.json` supply legacy tests. The generated ledger is
read by the legacy lock verifier. None of these artifacts is Rust authority,
a Rust release gate or a migration obligation. The original upstream README
uses historical authority and product terminology only within this legacy
scope; its upstream links are not current repository governance.

See [LEGACY.md](../LEGACY.md) for retained jobs and exact commands. No gate or
locked fixture is deleted, disabled or relaxed.
