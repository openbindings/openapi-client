# Maintained client qualification

## Candidate and authority

The maintained candidate is the unpublished Rust `dynamic-openapi-client`
core and its optional native HTTP and Wasm Fetch companions. It interprets
Swagger 2.0 and OpenAPI 3.0.x, 3.1.x and 3.2.x under the OpenAPI specifications
and the RFCs they rely on. These are the only behavioral authority. Open client
choices are recorded in [policies.md](policies.md), with their existing
configuration and code-backed evidence.

Edition acceptance, source landing and local qualification are distinct from
complete support, stabilization and publication. The
[development checkpoint](../rust/DEVELOPMENT-STATUS.md) records scope, known
failures and unfinished qualification. External integration, synthesis and
consumer migration are not release gates for this client.

## Retained Rust checks

From the repository root, with the documented toolchain:

```sh
python3 rust/qualification/verify.py
```

The existing runner checks formatting, lint, `cargo test --locked --workspace`,
two native fixture inventories, the dynamic example, a packaged core consumer
across Serde feature graphs and a native companion package consumer. See
[Rust verification](../rust/README.md) for prerequisites and commands. Native
CI exercises Linux, macOS and Windows. Host CI builds the qualification bridge
and runs real Chromium and local workerd probes. Those probes are qualification
machinery, not a supported TypeScript package.

Socket-dependent tests require loopback listeners. An environment refusal is
reported by test name and is neither a product failure nor a passing socket
qualification. Unexecuted host/resource controls remain unqualified. Existing
workflows and runners perform no package publication or deployment.

## Review and claim discipline

Evidence identifies source, commands, environment and observed boundaries.
Review checks OpenAPI/RFC interpretation and documented policy, request-target
and credential safety, bounds, cancellation, ownership, response/provenance
facts, public ergonomics, packaged consumers and documentation accuracy.
A regression fixture cannot overrule a standard.

Fixes belong at the lowest owning layer and receive relevant verification.
Partial HTTP evidence survives errors and application-policy refusal. Neither
an available response nor local cancellation proves remote completion. Full
standards coverage, corpus convergence, performance and complete host
qualification require separate evidence. Documentation changes cannot turn
existing failures into passes.

## Legacy checks

Go and TypeScript are legacy and are not the maintained client.
`.github/workflows/ci.yml` and root pnpm scripts preserve their historical
qualification, including `pnpm qualify:release`. That script name refers only
to the legacy line. Source locks, corpora, API snapshots, boundary checks and
clean-consumer tests are legacy inputs, not Rust authority or release gates.
See [LEGACY.md](../LEGACY.md). No legacy gate is removed or disabled.

## Host and acquisition policy

Acquisition remains caller-owned; loading supplied bytes authorizes no
retrieval. Invocation hosts are a separate capability. The core enforces its
preparation/admission boundaries and companions declare stricter platform
profiles. Caller extensions remain subject to those profiles and establish
no additional behavioral authority.
