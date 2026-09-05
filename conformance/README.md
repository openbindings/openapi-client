# Conformance

Conformance is behavioral, not snapshot-based. A processor case supplies an
OpenAPI artifact, a native call, and observable facts at four boundaries:

1. operation selection and pre-dispatch refusal;
2. the HTTP request;
3. the native result or stream; and
4. lifecycle and cancellation.

The standalone clients support Swagger 2.0 and OpenAPI 3.0, 3.1, and 3.2 in
JSON and YAML, including local and external references. Product code must
never special-case a fixture name, repository, generator signature, operation
ID, vendor extension, or other corpus accident.

## Authoritative portable corpus

`upstream/openbindings-0.2/processor/` is the hash-locked portable processor
corpus for the four OpenBindings OpenAPI binding specifications. Both the
TypeScript and Go clients execute every scenario through their public native
client facades. A passing helper or adapter test cannot substitute for that
public-boundary run.

The upstream synthesis corpus is also vendored. It is not a standalone-client
release gate: synthesis projects OpenAPI facts into OpenBindings operations,
requirements, and coverage, so it belongs to the later OpenBindings adapter.
The adapter must derive those results from the native client substrate and may
not reimplement OpenAPI wire behavior.

The current lock includes the published corrections to `OAPI30-PS-199`,
`OAPI31-PS-188`, `OAPI32-PS-237`, and `OAPI32-PS-243`. Both clients pass the
unchanged pinned bytes; no fixture overlay, scenario-ID branch, or relaxed
operation/input enforcement participates in qualification.

## Native suites

Language-native tests add coverage that the portable format cannot conveniently
express, including:

- exact-edition loading and reference closure;
- operation inventory and all selector forms;
- request serialization, media lanes, content coding, and character coding;
- security alternatives and scheme-named credential placement;
- server configuration and complete-URL recovery;
- declared success and failure responses with raw HTTP evidence;
- redirects, transport separation, middleware, size bounds, and cancellation;
- SSE and sequential streaming, ordering, backpressure, and terminal outcomes;
- immutable loaded state and detached extension metadata; and
- typed error kind and code classification.

`cases/native-wire.json` remains a compact language-neutral floor exercised by
both implementations. Corpus discoveries are promoted there only when a stable
cross-language oracle can be stated; edition-specific and lifecycle-heavy
cases remain in their native suites.

## Evidence discipline

Every behavioral change receives a discriminating test at the lowest owning
layer. A scenario must fail an implementation that omits or reverses the rule;
mere execution coverage is not enough. Authority files and portable fixtures
remain development and release evidence only and are never shipped as runtime
dependencies.
