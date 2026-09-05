# OpenBindings SDK and OB CLI migration

## Governing decision

The standalone OpenAPI clients are the substrate. Existing OpenBindings SDK,
binding-package, and OB CLI APIs do not constrain their public shape and do not
receive compatibility aliases in this repository. Downstream integrations are
rewritten after the native surface passes its release gate.

## Target stack

```text
OB CLI
  └─ OpenBindings SDK: source/configuration/lifecycle orchestration
       └─ one adapter for the selected OAS-family binding identifier
            ├─ OpenAPI-native analysis: operation and coverage projection
            └─ OpenAPI-native client: call / stream
                 └─ private edition engine and HTTP transport
```

The four binding identifiers select edition-specific specification behavior,
but they share one adapter architecture and one native client implementation
per language. No layer above the native package serializes an OpenAPI
parameter, constructs a target URL, chooses a security alternative, follows a
redirect, selects response media, or parses a stream.

## Migration sequence

1. Accept and version the native TypeScript and Go clients against the portable
   processor corpus and package-quality gates.
2. Design one immutable OpenAPI-native analysis model shared semantically by
   both languages. It exposes the facts needed by generators and adapters,
   without OBI operations, binding IDs, or Core vocabulary.
3. Prove that native analysis and invocation use the same resolved artifact,
   effective declarations, and smallest-owner dispositions. The analysis path
   may not reparse the document under a second policy.
4. Implement OBI synthesis as projection from native analysis. Pass all 154
   portable OAS-family synthesis scenarios, including exhaustive coverage and
   configuration requirements.
5. Implement invocation adapters as lifecycle/value translation over native
   `operation`, `call`, and `stream`. Differential tests must observe the same
   request, application values, partial delivery, cancellation, and terminal
   outcome through the direct and adapted paths.
6. Replace SDK binding registration and selection with the new adapters. Any
   old API that makes the adapter own OpenAPI behavior is removed rather than
   shimmed.
7. Update OB CLI configuration and diagnostics to the SDK's new adapter
   surface. CLI code may collect credentials, media choices, server choices,
   and codec capabilities; it may not interpret OpenAPI declarations itself.
8. Remove displaced engine mirrors only after the adapter, SDK, and CLI suites
   pass at one revision in both languages.

## Native-to-OpenBindings translation

| Native fact | Adapter responsibility |
| --- | --- |
| Operation reference and metadata | Emit the binding reference and projected OBI operation identity |
| Parameter/body declaration analysis | Project abstract input contracts and correspondence transforms |
| Response alternatives | Project abstract output/failure contracts where the binding admits them |
| Native configuration requirements | Translate to Core context requirements without changing alternatives |
| HTTP non-2xx result | Preserve only binding-admitted failure data; do not invent protocol-independent status fields |
| Source/input/transport/protocol/response error | Map to the SDK-owned invocation error class and permitted code/data |
| Ordered stream events and terminal promise | Forward lifecycle with drain-before-terminal behavior |
| HTTP response evidence | Keep on protocol-native diagnostics; do not add it to abstract operation values |

## Cutover gate

Cutover stops unless all of the following hold:

- no OpenBindings dependency or public type exists in either native package;
- no adapter imports a private engine path;
- all native processor and adapter synthesis scenarios pass;
- direct-versus-adapted differential tests pass for all four editions;
- adapters contain no OpenAPI wire or response-selection logic;
- the SDK and CLI expose every actionable native configuration requirement;
- cancellation, streaming, redirects, credentials, and size limits retain the
  native behavior; and
- deleting the displaced integration code leaves no second OpenAPI executor.
