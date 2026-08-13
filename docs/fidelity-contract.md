# Artifact fidelity contract

## Guiding goal

For every operation within the declared OpenAPI support boundary, a developer should be able to point the client at the brownfield document and obtain an invocation at least as faithful as bespoke protocol code written from that same document. The client may hide mechanical complexity, but it must not silently collapse distinctions the artifact or exchange makes relevant to correct invocation.

OpenBindings adds a protocol-independent overlay above this client. It must preserve application behavior while hiding protocol vocabulary; protocol evidence remains on this standalone client or in protocol-native tooling and does not cross the abstract invocation boundary.

## Required properties

- **Artifact authority:** operation identity, servers, parameters, media, security, responses, and streaming are decided from the real document and exchange.
- **Wire fidelity:** method, URL, parameter serialization, headers, cookies, request media, and bytes match the OpenAPI rules selected by the artifact.
- **Value fidelity:** distinct authored inputs do not collide; falsy and empty values remain distinguishable whenever the native platform can distinguish them.
- **Outcome fidelity:** successful application values and authored failure bodies survive; native HTTP evidence remains available to standalone callers.
- **Lifecycle fidelity:** ordering, partial outputs, cancellation, backpressure, and completion behavior are preserved by the execution engine. These are emergent binding/artifact behavior, not cardinality declarations added to OpenBindings Core.
- **Loud boundary:** unsupported or ambiguous declarations fail before dispatch when knowable. The client must not guess silently.
- **Abstraction discipline:** no OBI, Core SDK, binding selection, or protocol-independent failure vocabulary is required by the native client.

## Supported invocation boundary

The support claim is deliberately about client-invoked Path Item operations,
not every object an OpenAPI document can contain.

| Surface | Required behavior |
| --- | --- |
| Editions | OpenAPI 3.0.0–3.0.4 and 3.1.0–3.1.2 are accepted exactly; another edition is refused. |
| Sources | JSON/YAML text, parsed documents, bytes, absolute artifact locations, redirect-aware retrieval bases, and complete local/external reference closure. |
| Targets | Every `paths` operation using an OpenAPI HTTP method is discoverable by `operationId`, path/method, or canonical reference. Duplicate `operationId` values are refused as ambiguous. |
| Requests | Server precedence and variables; path/query/header/cookie parameters; OpenAPI styles, explode, content parameters, and reserved-character rules; JSON, structured suffix JSON, text, bytes, form, multipart, media ranges, and concrete media selection. |
| Security | Anonymous and OR/AND requirements; API keys, Basic, Bearer, OAuth 2, and OpenID Connect builtins; artifact-named extension handlers for other schemes. Credentials are applied only for one complete artifact-authorized alternative. |
| Responses | Exact/range/default response selection, governing media selection, empty responses, JSON/text/byte application values, declared unsuccessful outcomes, and protocol-native evidence. |
| Lifecycle | Unary and SSE response shape, ordered partial outputs, backpressure, cancellation, delivery-unit limits, and terminal completion. |

Callbacks and webhooks describe interactions in the reverse direction and are
not outbound client calls. Links describe possible traversal rather than an
additional wire operation. They are outside this client's invocation target
set, not silently failed path operations. Arbitrary `x-*` extension semantics
are supported only through an installed extension seam; absent such a seam,
the client must preserve the standard OpenAPI meaning and refuse before a side
effect whenever the extension makes that meaning unknowable.

This boundary is independent of OBI synthesis. The OpenBindings adapter must
make every in-boundary path operation addressable without giving the binding
specification authority over the synthesized application schema.

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

## Qualification gates

A release may claim invocation-complete support only when all of these gates
pass against both language implementations:

1. Every authority-derived semantic cell has a normative fixture, diverse
   corpus evidence, or both; corpus absence never removes an obligation.
2. Authority fixtures compare the exact wire request and application outcome.
   An independent client is a witness only where it first passes the same
   authority fixture.
3. The native TypeScript and Go clients agree on shared cases without sharing
   implementation code.
4. Synthesized-OBI invocation is wire- and application-equivalent to native
   invocation of the same artifact operation.
5. The complete synthesized interface and synthesis-coverage ledger agree
   across SDKs for every valid artifact in the supported corpus envelope.
6. Unsupported declarations are deterministic pre-dispatch refusals; there
   are no silent approximations.
7. Lifecycle stress and race tests preserve already-emitted values before a
   terminal failure.
8. A sealed holdout and two subsequent acquisition cohorts introduce no new
   unclassified semantic failure class.

The corpus is stratified by repository and semantic signature with at most one
development-slice operation per repository. Failures are reduced to a rule-level
fixture before implementation changes are accepted. Mutation self-checks must
demonstrate that wire, application, and lifecycle mismatches are detected.

## Current claim level

The TypeScript and Go native clients share the same declared support boundary
and run a language-neutral conformance floor. The adapters execute through
those standalone engines, while synthesis remains adapter-owned. Qualification
uses independently sourced artifacts, authority-authored wire transcripts, an
independently qualified request witness, exact cross-SDK synthesis comparison,
and sealed acquisition cohorts.

Passing this contract is evidence for the stated standard OpenAPI boundary; it
is not a claim to infer private vendor-extension behavior or to operate an API
whose document is itself incomplete or inaccurate.
