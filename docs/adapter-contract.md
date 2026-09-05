# OpenBindings adapter contract

The future OpenBindings OpenAPI binding packages are adapters over this
repository's supported native client capabilities. Their job is translation,
not a second OpenAPI implementation. Existing SDK and OB CLI APIs are migration
inputs only; preserving them is not a requirement.

## Inputs to the engine

The adapter resolves the selected OBI binding and supplies:

- the real OpenAPI artifact location and/or content;
- the canonical OpenAPI operation reference;
- the artifact-native input produced from the abstract operation input;
- resolved configuration and credentials;
- cancellation, size limits, and optional diagnostic hooks.

The native package is the adapter's single implementation substrate for `$ref`
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

## Native analysis prerequisite

Invocation adapters can use `operations`, `operation`, `call`, and `stream`
directly. Full OBI synthesis additionally needs immutable OpenAPI-native
declaration analysis: effective parameters, request and response alternatives,
security alternatives, configuration requirements, and smallest-owner
invalid/excluded dispositions. That capability must be added to both public
language packages before adapter cutover. It must not expose OBI types or make
synthesis authoritative over invocation.

## Cutover gate

The SDK and CLI may switch to the new substrate only when:

- all artifact-execution tests pass against this repository;
- binding-only synthesis and operation-layer tests pass through the bridge;
- context challenges remain recognizable by the Core SDK;
- partial outputs and terminal failures retain drain-before-terminal behavior;
- package inspection proves the standalone public entry point has no Core dependency;
- no duplicated OpenAPI request/response implementation remains in an adapter,
  SDK, or OB CLI package;
- all 154 portable synthesis scenarios pass through adapter-owned projection
  derived from the native analysis capability; and
- native and OpenBindings-adapted invocations produce the same HTTP exchange,
  application values, ordering, cancellation, and terminal outcome.

This cutover has not been performed in the current standalone-client phase.
