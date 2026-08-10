# Architecture

## Product boundary

The product is the **OpenBindings OpenAPI Client**. Technically, it is a **document-driven OpenAPI client runtime**: a long-lived component loads an artifact and executes authored operations from it. “Client” is the user-facing noun; “execution engine” names the implementation below the public API.

The repository has three intentional layers:

1. **Native client API** — OpenAPI-native sources, operation selectors, grouped parameters, bodies, scheme-named credentials, HTTP outcomes, middleware, and streams.
2. **Execution engine** — document loading/resolution, request planning, serialization, security placement, HTTP, declaration matching, decoding, and lifecycle mechanics.
3. **OpenBindings adapter (separate package)** — OBI source resolution, binding selection integration, OpenBindings context negotiation, abstract input translation, and protocol-independent output/error/lifecycle translation.

Only the first two belong to the standalone product. The adapter consumes the engine; the engine must never import the adapter or require an OBI.

## Dependency direction

```text
application using OpenAPI only
        │
        ▼
native OpenAPI client API
        │
        ▼
OpenAPI execution engine ───► HTTP service
        ▲
        │
OpenBindings OpenAPI adapter
        ▲
        │
OpenBindings Core invocation
```

An application may enter at either top edge. Adding OpenBindings should add selection and abstraction above the engine, not replace the OpenAPI implementation below it. Removing OpenBindings should leave an ordinary OpenAPI client with the same wire behavior.

## Ownership rule

If a behavior can be decided from the OpenAPI document, native call options, and HTTP exchange, it belongs to the client/engine. If it requires an OBI operation, binding selection, Core transforms, context-store orchestration, or protocol-independent outcome mapping, it belongs to the adapter or Core SDK.

| Concern | Owner |
| --- | --- |
| Resolve an OpenAPI `$ref` | execution engine |
| Serialize `style: deepObject` | execution engine |
| Choose an OpenAPI server and variables | native client / execution engine |
| Place a named API-key scheme | execution engine |
| Preserve HTTP status and headers | native client result |
| Select an OBI binding | OpenBindings Core SDK |
| Apply an OBI `inputTransform` | OpenBindings Core SDK |
| Map HTTP-native failure into abstract invocation failure | OpenBindings adapter |
| Expose raw protocol evidence as optional diagnostics | OpenBindings adapter |
| Synthesize OBI operations and binding references | OpenBindings adapter/synthesizer |

## No synthesis dependency

The engine executes references already present in an OpenAPI document. It does not need to synthesize or understand an OBI. OBI synthesis may use the same document-analysis utilities, but synthesis has no authority over the engine's OpenAPI behavior.

## Extraction state

The TypeScript package is dependency-isolated from `@openbindings/sdk`. It
publishes three deliberate entry points: the native client, the SDK-neutral
execution engine, and reusable document analysis. Engine behavior is selected
by named capability profiles; it contains no OpenBindings binding identifier.
The separate adapter maps immutable OpenBindings OpenAPI revisions to those
profiles and supplies the binding-private routed-input marker. The native
client selects the fullest profile directly.

The former in-workspace runtime mirror has been retired after the native client
and all 579 TypeScript adapter tests passed through this engine. A Go-neutral
engine and native Go client remain future parity work.

The Go parity port will consume the same language-neutral conformance cases. It must not expose `openbindings.Invocation` or OpenBindings context shapes from its public native API.
