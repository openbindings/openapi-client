# Extraction ledger

This ledger prevents a cosmetically standalone package from being mistaken for a completed architectural extraction.

| Area | State | Evidence / next action |
| --- | --- | --- |
| TypeScript native client API | implemented | `OpenAPIClient.load`, selection, `call`, `stream`, native auth/middleware/results |
| TypeScript Core runtime dependency | removed | package has no `@openbindings/sdk` dependency; public declarations contain no Core types |
| TypeScript artifact execution tests | cut over | 226 standalone tests plus the adapter's 579-test revision/conformance/corpus suite execute through this engine |
| Optional empty body distinction | fixed in extracted engine | explicit body-presence marker; native regression test |
| Unsupported security schemes | native extension seam implemented | scheme-keyed handler; OR alternatives do not combine handlers |
| Historical binding profiles | isolated in adapter | engine accepts capability profiles; adapter alone maps immutable `openbindings.openapi@1`–`@7` identifiers and private route markers |
| TypeScript OpenBindings adapter cutover | complete | SDK-class/error/hook/lifecycle bridge passes all 579 package tests; obsolete in-workspace runtime mirror retired |
| Native client engine cutover | complete | `OpenAPIClient` and the OpenBindings adapter enter through the same prepared-operation engine path |
| Go SDK-neutral engine | pending | extract from `openbindings-go/formats/openapi`; do not publish a wrapper-first substitute |
| Go native client | pending on neutral engine | implement and run shared conformance |
| OBI synthesis | intentionally remains in binding packages | no synthesis authority added to the native client |
| OpenBindings Core spec/model | unchanged | no Core change made or required by this extraction |
