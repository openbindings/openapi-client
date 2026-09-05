# Architecture

## Product boundary

The product is the **OpenBindings OpenAPI Client**. Technically, it is a **document-driven OpenAPI client runtime**: a long-lived component loads an artifact and executes authored operations from it. “Client” is the user-facing noun; “execution engine” names the implementation below the public API.

The repository has three intentional layers:

1. **Native client API** — OpenAPI-native sources, operation selectors, grouped parameters, bodies, scheme-named credentials, HTTP outcomes, middleware, and streams.
2. **Execution engine** — document loading/resolution, request planning, serialization, security placement, HTTP, declaration matching, decoding, and lifecycle mechanics.
3. **OpenBindings adapter (separate package)** — OBI source resolution, binding selection integration, OpenBindings context negotiation, abstract input translation, and protocol-independent output/error/lifecycle translation.

Only the first two belong to the standalone product. The adapter consumes the
supported native client package; neither the client nor its private engine may
import the adapter or require an OBI.

## Dependency direction

```text
direct OpenAPI application ─────────────┐
generated typed facade ────────────────┼──► native OpenAPI client API
OpenBindings Core ─► OpenAPI adapter ──┘               │
                                                       ▼
                                      private execution engine ─► HTTP service
```

The adapter consumes the same supported native package as an unrelated
application; it does not import private engine files. Adding OpenBindings adds
selection and abstraction around the native client, not a second OpenAPI
implementation. Removing OpenBindings leaves an ordinary OpenAPI client with
the same wire behavior.

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
| Expose raw protocol evidence to protocol-aware callers | native client / execution engine |
| Keep raw protocol evidence outside abstract invocation frames | OpenBindings adapter |
| Synthesize OBI operations and binding references | OpenBindings adapter/synthesizer |

## No synthesis dependency

The engine executes references already present in an OpenAPI document. It does
not need to synthesize or understand an OBI. If OBI synthesis needs additional
document facts, they first become a clean OpenAPI-native capability of the
standalone package, with the same semantics in TypeScript and Go. The adapter
must not import private files or make synthesis authoritative over invocation.

## Implementation authority

The four release/0.2 family specifications are the implementation authority:

- `openbindings.openapi-2.0@1`;
- `openbindings.openapi-3.0@1`;
- `openbindings.openapi-3.1@1`;
- `openbindings.openapi-3.2@1`.

Their exact source revision and portable conformance corpus are vendored and
hash-locked under `authority/` and `conformance/upstream/`. They are test and
release inputs, never runtime dependencies. `docs/public-api-v1.md` defines the
new native product contract built over that authority.

The old pre-release APIs, development profiles, withdrawn unified binding ID,
and current adapter/CLI integration are not compatibility constraints. The
implementation converges from the specifications outward: edition engines,
one native surface in TypeScript and Go, thin OpenBindings adapters, then the
SDK and OB CLI.
