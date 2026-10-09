# Dynamic OpenAPI Client

An independent dynamic OpenAPI engine. Current development uses Rust in
[`rust/client`](rust/client); it has no OpenBindings SDK dependency.

The unpublished `dynamic-openapi-client` foundation supports JSON/OpenAPI
3.1.0–3.1.2, exact-document inspection, local protocol references, request
preparation and finite JSON/raw invocation through caller-supplied async
transport. The [Rust API guide](rust/client/README.md) shows complete dynamic
calls, diagnostics, ownership and application outcome policy.

The current Rust profile does not provide YAML or other OpenAPI editions,
external acquisition, schema evaluation, production HTTP integration or streaming
codecs. Browser/workerd test glue is qualification machinery, not a supported
TypeScript package. See [supported scope and verification](rust/README.md).

## Develop and verify

Use Rust 1.99.0 and Python 3.13+, then run:

```sh
python3 rust/qualification/verify.py
```

Native CI exercises Linux, macOS and Windows. Host CI builds the Wasm bridge and
runs real Chromium and local workerd probes. There are no publication or
deployment steps in Rust qualification.

## Legacy implementations

Existing [Go](go/README.md) and [TypeScript documentation](LEGACY.md) remain
available for transitional consumers and historical reference. They cover more
editions and capabilities than the Rust foundation. Migrating a caller requires
checking its actual needs; source landing does not establish full parity or
replace an installed package. Legacy source, tags and preserved history remain
intact while consumers migrate.
