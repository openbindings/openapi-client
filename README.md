# Dynamic OpenAPI Client

A standalone Rust client for Swagger 2.0 and OpenAPI 3.0.x, 3.1.x and 3.2.x
documents. The OpenAPI specifications and the RFCs they rely on are its only
behavioral authorities. Where those standards leave a choice open, the client
documents its own policy and available caller configuration.

Current development uses [`rust/client`](rust/client). See the
[architecture](docs/architecture.md), [public API contract](docs/public-api-v1.md)
and [policy register](docs/policies.md).

The unpublished `dynamic-openapi-client` foundation accepts JSON and YAML
Swagger 2.0 and OpenAPI 3.0, 3.1 and 3.2 documents through one API. The current
development checkpoint includes edition-aware inspection, caller-supplied resource
graphs, schema inspection and finite JSON/raw/named-form request preparation;
it does not implement every feature in those editions.
The optional [`dynamic-openapi-client-reqwest`](rust/reqwest/README.md)
companion provides a reusable native HTTP client and a runnable getting-started example. The [Rust API guide](rust/client/README.md) shows complete dynamic
calls, diagnostics, ownership and application outcome policy. The optional
[Rust Fetch companion](rust/fetch/README.md) provides Browser/Worker profiles on
Wasm with explicit host-managed replay; host qualification remains in progress.

Resource acquisition, schema evaluation, remaining serialization/coding profiles
and streaming codecs remain open work.
Browser/workerd test glue is qualification machinery, not a supported
TypeScript package. See [supported scope and verification](rust/README.md) and
the [development checkpoint and remaining qualification](rust/DEVELOPMENT-STATUS.md).

## Develop and verify

Use Rust 1.99.0 and Python 3.13+, then run:

```sh
python3 rust/qualification/verify.py
```

Native CI exercises Linux, macOS and Windows. Host CI builds the Wasm bridge and
runs real Chromium and local workerd probes. There are no publication or
deployment steps in Rust qualification.

## Legacy implementations

The [Go](go/README.md) and [TypeScript](typescript/README.md) implementations
are legacy and are not the maintained client. Their [legacy gates](LEGACY.md)
remain available for transitional consumers and historical reference. Capability
coverage differs from the Rust implementation. Migrating a caller requires
checking its actual needs; source landing does not establish full parity or
replace an installed package. Legacy source, tags and preserved history remain
intact while consumers migrate.
