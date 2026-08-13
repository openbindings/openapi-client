# OpenBindings adapter contract

The OpenBindings OpenAPI binding package is an adapter over this repository's execution engine. Its job is translation, not a second OpenAPI implementation.

## Inputs to the engine

The adapter resolves the selected OBI binding and supplies:

- the real OpenAPI artifact location and/or content;
- the canonical OpenAPI operation reference;
- the artifact-native input produced from the abstract operation input;
- resolved configuration and credentials;
- cancellation, size limits, and optional diagnostic hooks.

The engine is the adapter's single implementation substrate for `$ref`
resolution, servers, parameter serialization, request bodies, security
placement, HTTP, responses, and SSE. Portable OpenBindings meaning still comes
from the selected binding specification; this split prevents the adapter from
silently creating a second OpenAPI execution policy.

## Results from the engine

The engine reports an SDK-neutral event sequence and terminal outcome:

- ordered decoded application values;
- normal completion or a typed engine failure;
- protocol-native response evidence;
- cancellation and backpressure behavior.

The adapter maps those facts into the OpenBindings invocation frame:

- application values become operation outputs;
- application-authored error bodies may become portable failure data under the binding rules;
- transport/protocol/decoding/configuration failures become unsuccessful invocation completion with protocol-independent codes;
- the engine's concrete error text and protocol evidence remain available only on this standalone runtime's native surface or in protocol-native tooling; they do not cross the abstract invocation boundary;
- HTTP status and headers never become required abstract output fields.

## Class and package isolation

The engine must not construct or return `@openbindings/sdk` classes. In particular, it must not leak a second copy of `InvocationError`: the Core SDK uses class identity when recognizing context challenges and flow signals.

The adapter therefore owns a small mechanical bridge:

1. convert engine error records into the SDK's `InvocationError` class;
2. normalize the error code and copy `data` only where the governing binding rule admits an application-authored value or defines the code's data;
3. expose the SDK's invocation interface while forwarding writes, close, cancellation, outputs, and lifecycle;
4. translate hook callbacks at the package boundary.

No request planning or OpenAPI response logic belongs in this bridge.

## Development capability profiles

No OpenAPI binding specification has been published. The adapter accepts only
the unreleased first `openbindings.openapi@1` candidate and maps it to the
engine's fullest capability profile. The engine's other named profiles record
development and migration stages; they have never been binding-specification
identifiers or revisions. The standalone native client selects the fullest
profile and does not expose binding-spec identifiers.

The profile is an engine input, not synthesis authority. Synthesis still creates OBI operations and private correspondence transforms; it does not decide how OpenAPI behaves on the wire.

## Cutover gate

The adapter may switch from its current in-repository engine mirror only when:

- all artifact-execution tests pass against this repository;
- binding-only synthesis and operation-layer tests pass through the bridge;
- context challenges remain recognizable by the Core SDK;
- partial outputs and terminal failures retain drain-before-terminal behavior;
- package inspection proves the standalone public entry point has no Core dependency;
- no duplicated OpenAPI request/response implementation remains in the adapter package.

Both cutovers satisfy this gate. TypeScript package declarations preserve SDK
class ownership and the adapter suite passes through the standalone engine.
The Go adapter converts neutral prerequisites, hooks, metadata, failures,
inputs, outputs, cancellation, and completion while the existing binding
suite passes unchanged, including under the race detector. The displaced Go
HTTP/SSE execution loop has been removed. Synthesis-only schema projection and
dialect modules remain in each adapter by design.
