# OpenAPI client release qualification

## Candidate boundary

The standalone candidate is the TypeScript package and Go module for direct
Swagger 2.0 and OpenAPI 3.0, 3.1, and 3.2 invocation. It includes the native
client-engine facades, edition mechanics, transport integration, codecs,
streaming, and native HTTP outcome model.

It includes the supported advanced provider surface used by generators and
protocol adapters. OpenBindings adapters and OBI projection are qualified in
their own repositories; the OpenBindings SDK, OB CLI integration, and generated
schema types remain downstream consumers or separate products. Compatibility
with this repository's earlier pre-release APIs is not a goal.

The behavioral authority is the four `openbindings.openapi-*.@1` binding
specifications on the OpenBindings 0.2 release line. Their source and portable
corpus are pinned under `authority/` and `conformance/upstream/`.

## Local release gate

`pnpm qualify:release` is the repository-local gate. At one exact clean
revision it requires:

1. authority source, file hashes, rule inventory, and corpus counts verify;
2. all 896 processor scenarios pass through the public TypeScript and Go
   client behavior;
3. TypeScript type checking, native tests, and production ESM/CommonJS builds
   pass;
4. the complete Go suite passes under the race detector;
5. boundary inspection finds exactly the application root and advanced provider
   TypeScript exports, their Go counterparts, and no OpenBindings runtime
   dependency or public compatibility type;
6. reviewed root and provider TypeScript declarations and Go documentation
   match the intentional public API snapshot;
7. a packed npm artifact works from clean ESM and CommonJS projects and its
   installed declarations compile for both module forms;
8. the package bundles for a browser target without a Node-only static
   dependency;
9. a clean external Go module loads and invokes every supported edition; and
10. formatting and repository integrity checks pass.

The corpus gate uses the public client facade. Internal helper tests are
necessary but cannot substitute for it.

## Current authority gate

The hash-locked authority revision is
`ee5291ee5a5a23f7068d8c1d256792274a21167f` on `release/0.2`. It includes the
smallest-owner correction to `OAPI32-PS-66`, the content-based `text/plain`
correction to `OAPI30-PS-127`, and the complete provider-projection closure
batch. No candidate erratum overlay or scenario-ID exception remains.

## Adversarial review gate

After the mechanical gate is green, a fresh review must find no unresolved P0,
P1, or P2 issue in:

- binding-spec alignment and cross-edition confinement;
- TypeScript/Go behavioral parity;
- request-target, credential, header, cookie, redirect, and resource-loading
  safety;
- cancellation, partial delivery, terminal outcomes, and size bounds;
- source, operation, input, configuration, transport, protocol, response,
  cancellation, and internal error classification, including confinement of
  otherwise-unclassified private failures behind the public error type;
- immutability and concurrent-client use;
- public naming, discoverability, defaults, and generated-facade viability;
- package contents, browser/Node/CommonJS behavior, and external Go use; and
- documentation accuracy.

A finding is fixed at its lowest owning layer and receives a discriminating
test. The review reruns against the changed snapshot. Qualification stops only
when the mechanical gates remain green and a final review accepts the exact
same bytes.

## Adapter gate

Full OpenBindings synthesis is deliberately not a standalone-client release
gate. The separate TypeScript and Go adapter qualification must derive every
portable OAS-family synthesis scenario from native provider facts and prove
that the adapters contain neither a second OpenAPI executor nor a second
declaration planner. The current adapters pass all 154 OAS-family synthesis
scenarios and have removed both kinds of duplicate implementation. Adapter
differentials also exercise the same native execution path and compare HTTP
exchange and application values. SDK and OB CLI cutover remains a later
integration gate.

## Host security policy

Artifact retrieval policy is host-owned. Applications loading untrusted
descriptions should provide a restricted `documentFetch` or
`DocumentHTTPClient` for network allowlists, source-size limits, proxy policy,
TLS policy, and environment-specific file access. Invocation transport is a
separate capability.

The clients themselves enforce binding-defined target, header, cookie,
credential, content, redirect, and response boundaries. A custom transport,
middleware, or security handler is an explicit native extension point; its
additional behavior is the caller's responsibility.
