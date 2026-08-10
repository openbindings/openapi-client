# OpenBindings adapter contract

The OpenBindings OpenAPI binding package is an adapter over this repository's execution engine. Its job is translation, not a second OpenAPI implementation.

## Inputs to the engine

The adapter resolves the selected OBI binding and supplies:

- the real OpenAPI artifact location and/or content;
- the canonical OpenAPI operation reference;
- the artifact-native input produced from the abstract operation input;
- resolved configuration and credentials;
- cancellation, size limits, and optional diagnostic hooks.

The engine remains authoritative for `$ref` resolution, servers, parameter serialization, request bodies, security placement, HTTP, responses, and SSE.

## Results from the engine

The engine reports an SDK-neutral event sequence and terminal outcome:

- ordered decoded application values;
- normal completion or a typed engine failure;
- protocol-native response evidence;
- cancellation and backpressure behavior.

The adapter maps those facts into the OpenBindings invocation frame:

- application values become operation outputs;
- application-authored error bodies may become portable failure details under the binding rules;
- transport/protocol/decoding/configuration failures become terminal invocation failures;
- protocol evidence may be retained as explicit diagnostics;
- HTTP status and headers never become required abstract output fields.

## Class and package isolation

The engine must not construct or return `@openbindings/sdk` classes. In particular, it must not leak a second copy of `InvocationError`: the Core SDK uses class identity when recognizing context challenges and flow signals.

The adapter therefore owns a small mechanical bridge:

1. convert engine error records into the SDK's `InvocationError` class;
2. copy `code`, portable `details`, and optional diagnostics without changing their meaning;
3. expose the SDK's invocation interface while forwarding writes, close, cancellation, outputs, and lifecycle;
4. translate hook callbacks at the package boundary.

No request planning or OpenAPI response logic belongs in this bridge.

## Compatibility profiles

Historical `openbindings.openapi@1` through `@7` identifiers are immutable binding contracts. The adapter selects the corresponding execution capability profile. The standalone native client selects the fullest current fidelity profile and does not expose binding-spec identifiers.

The profile is an engine input, not synthesis authority. Synthesis still creates OBI operations and private correspondence transforms; it does not decide how OpenAPI behaves on the wire.

## Cutover gate

The adapter may switch from its current in-repository engine mirror only when:

- all artifact-execution tests pass against this repository;
- binding-only synthesis and operation-layer tests pass through the bridge;
- context challenges remain recognizable by the Core SDK;
- partial outputs and terminal failures retain drain-before-terminal behavior;
- package inspection proves the standalone public entry point has no Core dependency;
- no duplicated OpenAPI request/response implementation remains in the adapter package.

The TypeScript cutover satisfies this gate: 579 adapter tests pass through the
standalone engine, package declarations preserve SDK class ownership, and the
obsolete in-workspace execution mirror has been removed. Synthesis-only schema
projection/dialect modules remain in the adapter by design; shared artifact
analysis comes from the standalone `./analysis` entry point.
