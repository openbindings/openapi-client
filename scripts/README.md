# Legacy qualification utilities

All executable utilities in this directory serve the preserved Go/TypeScript
line, which is legacy and is not the maintained Rust client. Root pnpm
commands retain their original behavior. The legacy CI TypeScript job invokes
`pnpm qualify:release`, including source/corpus verification, boundary tests,
API snapshots and clean-consumer checks. `pnpm authority:sync` is the explicit
source-pin maintenance command.

These utilities, their package policies and their upstream prerequisites
establish no Rust authority, gate, interface or migration obligation. See
[LEGACY.md](../LEGACY.md) for commands and inputs and
[Rust qualification](../docs/release-qualification.md) for the maintained line.
