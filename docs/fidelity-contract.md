# Artifact fidelity contract

## Guiding goal

For every operation within the declared OpenAPI support boundary, a developer should be able to point the client at the brownfield document and obtain an invocation at least as faithful as bespoke protocol code written from that same document. The client may hide mechanical complexity, but it must not silently collapse distinctions the artifact or exchange makes relevant to correct invocation.

OpenBindings adds a protocol-independent overlay above this client. It must preserve application behavior while hiding protocol vocabulary; protocol evidence is an optional diagnostic escape hatch, never a required ordinary output.

## Required properties

- **Artifact authority:** operation identity, servers, parameters, media, security, responses, and streaming are decided from the real document and exchange.
- **Wire fidelity:** method, URL, parameter serialization, headers, cookies, request media, and bytes match the OpenAPI rules selected by the artifact.
- **Value fidelity:** distinct authored inputs do not collide; falsy and empty values remain distinguishable whenever the native platform can distinguish them.
- **Outcome fidelity:** successful application values and authored failure bodies survive; native HTTP evidence remains available to standalone callers.
- **Lifecycle fidelity:** ordering, partial outputs, cancellation, backpressure, and completion behavior are preserved by the execution engine. These are emergent binding/artifact behavior, not cardinality declarations added to OpenBindings Core.
- **Loud boundary:** unsupported or ambiguous declarations fail before dispatch when knowable. The client must not guess silently.
- **Abstraction discipline:** no OBI, Core SDK, binding selection, or protocol-independent failure vocabulary is required by the native client.

## Evaluation loop

1. Add a diverse real-world artifact or minimized behavior fixture.
2. State the artifact-native behavior independently of the implementation.
3. Execute through a protocol oracle or exact wire observer.
4. Compare request, response, values, and lifecycle.
5. Classify divergence as artifact ambiguity, support-boundary exclusion, engine defect, adapter defect, binding-spec defect, or Core limitation.
6. Fix the lowest owning layer.
7. Add a generalized conformance case and rerun the full varied corpus.
8. Check that the fix follows the specification rule rather than a corpus-specific spelling or generator habit.

No Core document-model change is permitted as an incidental implementation fix. Any evidence of a Core limitation is reported separately for explicit design review.

## Current claim level

The extracted TypeScript client proves the standalone boundary and several high-risk semantics, but it is not yet claiming universal OpenAPI-document fidelity. A stable claim requires migration of the broad existing OpenBindings OpenAPI conformance/corpus suite, public capability reporting for intentional exclusions, independent differential oracles, and TypeScript/Go parity over the language-neutral cases.
