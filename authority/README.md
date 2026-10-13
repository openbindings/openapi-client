# Legacy source pins and locks

Every artifact in this directory belongs to retained Go/TypeScript regression
qualification. Those implementations are legacy and are not the maintained
Rust client. Historical source identifiers and binding-specification prose
are legacy gate inputs, not Rust behavioral authority.

The root `pnpm authority:verify` script checks the source pin, lock, generated
ledger and upstream fixture bytes. `pnpm authority:sync` is the retained
maintenance command that refreshes those inputs from an explicitly selected
source checkout. The legacy CI TypeScript job runs verification through
`pnpm qualify:release`. See [LEGACY.md](../LEGACY.md) and
[conformance scope](../conformance/README.md).

Rust authority is the OpenAPI specifications and the RFCs they rely on;
its own choices are recorded in [policies.md](../docs/policies.md).
