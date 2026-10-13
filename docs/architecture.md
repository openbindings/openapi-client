# Architecture

## Product and authority

The maintained product is `dynamic-openapi-client`, a standalone Rust client
for Swagger 2.0 and OpenAPI 3.0.x, 3.1.x and 3.2.x documents. Only the OpenAPI
specifications and the RFCs they rely on govern its behavior. An OpenAPI
document supplies the declarations interpreted under its edition. Where those
standards permit alternatives or leave client behavior unspecified, the
library makes the choices recorded in [policies.md](policies.md).

Neither a downstream consumer's interface nor a regression corpus establishes
an additional behavioral authority. A fixture that disagrees with a standard
or a documented client policy is evidence to investigate, not a new rule.

## Layers and dependency direction

1. **Application API:** loading, operation inspection and selection, request
   construction, preparation, invocation and native response evidence.
2. **Core:** edition interpretation, immutable supplied-resource graphs,
   schema inspection, serialization, server resolution, security placement,
   bounds and diagnostics.
3. **Optional HTTP companions:** native reqwest and Wasm Fetch hosts implementing
   the core's explicit host contract.

```text
application or generated facade -> Rust application API -> core
                                                        |
                                           supplied host callback
                                                        |
                                  native HTTP or Browser/Worker Fetch
```

The core does not depend on an executor or HTTP stack. HTTP companions depend
on the core. An external integration is an ordinary consumer of supported
public APIs; its vocabulary, lifecycle and migration schedule do not govern
this repository.

## Ownership

| Concern | Owner |
| --- | --- |
| Edition, references and effective declarations | Core |
| Server, media and security alternative selection | Caller through core APIs |
| Parameter/body serialization and credential placement | Core |
| Credential material and permitted origins | Caller |
| Network stack, host restrictions and delivery facts | Supplied host or companion |
| HTTP status, retained headers/body and execution evidence | Core outcome/response |
| Application acceptance of a response | Caller or an explicitly selected policy |
| Description and OAuth token acquisition | Caller; no core acquisition driver exists |

Inspection preserves authored source and declaration identity. Preparation
uses the supported OpenAPI profile, refuses unsupported or ambiguous choices
before dispatch when possible, and does not evaluate schemas or insert
defaults. Cancellation ends local work without asserting remote rollback.

## Scope and legacy separation

The maintained line is unpublished and supports finite requests, supplied
resource graphs and schema inspection. Streaming, schema evaluation,
acquisition and remaining serialization profiles remain open. Edition
acceptance does not mean complete feature support. See the
[Rust scope](../rust/README.md) and
[development checkpoint](../rust/DEVELOPMENT-STATUS.md).

Go and TypeScript and their qualification jobs are legacy, not the maintained
client. Their package contracts, snapshots and pinned corpora preserve that
historical line only. See [LEGACY.md](../LEGACY.md). They impose no Rust parity,
release or migration obligation.
