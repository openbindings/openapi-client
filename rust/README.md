# Dynamic OpenAPI Rust foundation

The maintained Rust implementation is `client/`, package
`dynamic-openapi-client`. It is an unpublished foundation with bounded
qualification and independently verified API repairs. Version `0.0.0` is not a
registry release. All packages retain `publish = false`.

The supported profile is exact JSON/OpenAPI 3.1.0–3.1.2 parsing and inspection,
local JSON-Pointer protocol references, request preparation and finite JSON/raw
invocation through caller-supplied transport. Refer to [the API guide](client/README.md)
for parameter/media/security coverage and explicit limitations. Schema evaluation,
external document acquisition, production transports, streaming, additional
OpenAPI editions and a supported npm API remain separate work.

## Source and evidence

The implementation imports preserved candidate
`df15ab3ae79cf1a4897fe22496c61a04c9332453`. Original qualification source,
packages, assertions and independent repair review are preserved by the project.
The import adds repository documentation and portable host/CI replay without
changing the engine's semantics or fixture expectations. Original benchmark and
robustness results retain their recorded host, workload and source boundaries;
new CI results identify their own checked-out source.

## Verification

Rust 1.99.0, Python 3.13+ and the committed Cargo lock are required. From the
repository root, `python3 rust/qualification/verify.py` runs formatting, lint,
native tests/doctests, both native fixture inventories, the dynamic example and a
fresh external consumer of a locally packaged crate. Outputs go to
`rust/target/qualification` by default; an explicit output path is accepted.

The API quality revision resolves the six previously retained Clippy warnings.
Workspace lint now passes without warnings. `lint.py` still rejects warnings
outside its historical exact-code/source-location baseline, and compiler errors
fail the check; no lint category is suppressed.

For host qualification, install the `wasm32-unknown-unknown` target and
wasm-bindgen-cli 0.2.129. Build `oac-host-qualification` for that target with the
workspace's locked dependencies, then generate web glue into
`qualification/bridge/generated` with wasm-bindgen. In `qualification/host`, run
`npm ci`, `npx playwright-core install --with-deps chromium`, and
`node run.mjs <output-directory>`. The checked-in workflow gives the complete
commands. `CHROMIUM_EXECUTABLE` can select an installed browser;
`PLAYWRIGHT_BROWSERS_PATH` can select a Playwright installation. The default uses
Playwright's installed Chromium. Wrangler runs locally, uses loopback fixtures
and never deploys a Worker.

The qualification bridge is not a public SDK. Initial dependency/tool downloads
need network access. Success requires actual commands and host assertions; a
workflow definition alone is not a pass. Package publication, API stabilization,
legacy retirement and application migration are separate decisions.
