# Extraction ledger

This ledger prevents a cosmetically standalone package from being mistaken for a completed architectural extraction.

| Area | State | Evidence / next action |
| --- | --- | --- |
| TypeScript native client API | implemented | `OpenAPIClient.load`, selection, `call`, `stream`, native auth/middleware/results |
| TypeScript Core runtime dependency | removed | package has no `@openbindings/sdk` dependency; public declarations contain no Core types |
| TypeScript artifact execution tests | cut over | 237 standalone tests plus the adapter's 574-test conformance/corpus suite execute through this engine |
| Optional empty body distinction | fixed in extracted engine | explicit body-presence marker; native regression test |
| Unsupported security schemes | native extension seam implemented | scheme-keyed handler; OR alternatives do not combine handlers |
| Development capability profiles | isolated from binding identity | engine profiles preserve implementation/migration stages; only the unreleased `openbindings.openapi@1` candidate is accepted by the adapter and maps to the full profile |
| TypeScript OpenBindings adapter cutover | complete | SDK-class/error/hook/lifecycle bridge passes all 574 package tests; obsolete in-workspace runtime mirror retired |
| Native client engine cutover | complete | `OpenAPIClient` and the OpenBindings adapter enter through the same prepared-operation engine path |
| Go SDK-neutral engine | implemented | no OpenBindings imports; prepared-operation and cardinality-agnostic execution contracts; development capability profiles remain private migration coordinates, never binding-spec revisions |
| Go native client | implemented | grouped parameters, scheme-named auth, native HTTP results/failures, streaming, parsed/bytes/location sources |
| Redirect policy | qualified and configurable | invocation redirects remain observable by default in both languages; explicit standalone user-agent following is tested; artifact retrieval follows redirects independently |
| Shared TypeScript/Go conformance | implemented | language-neutral OpenAPI 3.0/3.1 fixtures assert exact request, application value, and failure declaration behavior in both clients |
| Go OpenBindings adapter cutover | complete | existing binding conformance suite and race detector pass through the standalone engine; dead HTTP/SSE execution loop retired |
| OBI synthesis | intentionally remains in binding packages | no synthesis authority added to the native client |
| OpenBindings Core spec/model | unchanged | no Core change made or required by this extraction |
