# Legacy Go and TypeScript implementations

Go (`go/`) and TypeScript (`typescript/`) are preserved legacy implementations.
They are not the maintained client. Current development uses the unpublished
[Rust client](rust/README.md); see the [root README](README.md). Legacy feature
coverage and API snapshots are not Rust support or compatibility promises.

The [Go guide](go/README.md), [Go design](go/DESIGN.md),
[Go development contract](go/DEVELOPMENT.md) and
[TypeScript guide](typescript/README.md) describe historical consumer APIs.
A caller migration requires checking its actual requirements against the
Rust profile. No migration is a release obligation for the maintained client.

## Preserved gates and inputs

The workflow `.github/workflows/ci.yml` is **Legacy CI**. Its TypeScript job
builds its pinned neutral JSON/schema prerequisites and runs the root
`pnpm qualify:release` script. Its Go jobs run native/race, independent schema,
32-bit and Windows checks. These gates remain enabled and their commands are
unchanged.

All root pnpm scripts describe this legacy line. `pnpm verify` retains source
lock verification, the processor corpus, lint, tests, Go race tests, boundary
checks, browser bundling and API snapshots. `pnpm qualify:release` additionally
runs packed ESM/CommonJS/type consumers and an external Go consumer.
`pnpm authority:sync` remains the explicit maintenance command for the pinned
legacy inputs; it is not part of Rust qualification.

| Retained artifact | Legacy use |
| --- | --- |
| `authority/` source and lock JSON | `pnpm authority:verify` checks pinned identity, hashes and counts |
| `conformance/openapi-authority-ledger.json` | Legacy source/rule inventory read by the same verifier |
| `conformance/upstream/` | Locked source README, schemas and processor/synthesis fixtures; processor behavior uses `pnpm test:corpus` |
| `conformance/cases/` and number fixtures | Native legacy tests |
| `api/public-api-v1.json` | `pnpm api:verify` checks TypeScript declarations and Go package documentation |
| `scripts/` | Legacy verification, corpus maintenance, boundary and clean-consumer utilities |
| Root and TypeScript package manifests and pnpm lock | Legacy build, test, pack and prerequisite graph |

The source pins, upstream specifications, identifiers and embedded historical
prose in these artifacts belong solely to legacy regression qualification.
They are not behavioral authority, release gates or migration obligations for
the Rust client. Locked upstream bytes are retained without relabeling their
internal fields. Their complete legacy scope is stated here and in the
[authority notice](authority/README.md) and [conformance notice](conformance/README.md).

Retirement is a separate maintainer decision. Making this repository free of
legacy semantic dependencies would require retiring both implementations,
their CI jobs, root package graph, corpora, snapshots and verification tools.
This change retains them all. The unused npm lockfile and ungated historical
relationship/qualification narratives were archived outside the repository.
