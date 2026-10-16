# Dynamic OpenAPI for Rust

Load an OpenAPI description at runtime, inspect its operations, and call them
with ordinary Rust data. The maintained packages are unpublished (`0.0.0`,
`publish = false`): [the core client](client/README.md),
[native HTTP](reqwest/README.md), and [Wasm Fetch](fetch/README.md).
Start with the native HTTP path in the [client tutorial](client/README.md).

## Four jobs

1. **Load.** `Document::parse` takes supplied JSON/YAML bytes, an optional
   retrieval base, and `Limits`. It accepts Swagger 2.0 and OpenAPI 3.0, 3.1 and
   3.2 families without changing their edition. [Run `load`](client/examples/load.rs).
2. **List and describe.** Enumerate `operations()`, check `discovery_complete()`
   and `graph_issues()`, then read each operation's effective `describe()` or
   focused `parameters()`. [Run `inspect`](client/examples/inspect.rs).
3. **Build and send natively.** Create a native `Client`, build with
   `operation.request().parameter(..., &value).json(&model)`, and await
   `client.send(request)`. [Run `send`](reqwest/examples/send.rs).
4. **Read the outcome.** Keep the immutable outcome for status, headers, bytes,
   upload and errors. Read a typed received 2xx with the compiled excerpt below;
   `body_json()` also admits non-2xx bodies. [Run `read_response`](reqwest/examples/read_response.rs).

<!-- include: rust/reqwest/examples/read_response.rs#success-body -->
```rust,ignore
let created: support::Created = outcome.received_2xx_json()?;
assert_eq!(created.id, 9_007_199_254_740_993);
println!("created: {}", created.name);
```
<!-- endinclude -->

For a refusal, inspect `Diagnostic::reason()` and `context()`, then build a fresh
request with the corrected input. [Run `correction`](client/examples/correction.rs).
The tutorial includes the short correction example and complete run commands.
These excerpts are included from compiled examples and checked by
`python3 rust/qualification/docs.py`; they need the example's surrounding setup.

## Scope and verification

OpenAPI and its cited RFCs are the only behavioral authority. Our choices are
labelled client policies. The single [capability and policy matrix](client/CAPABILITIES.md)
links every row to implementation evidence and records verification scope.
Edition admission does not establish complete feature coverage. Acquisition,
schema evaluation, streaming and remaining codecs are open capabilities.

Advanced contracts live in [exact values and Serde](client/EXACT.md),
[loading](client/LOADING.md), [graphs](client/GRAPH.md), [schemas](client/SCHEMAS.md),
[forms](client/FORMS.md), [parameter serialization](client/SERIALIZATION.md), and
[custom hosts](client/HOSTS.md). Existing consumers use the coordinated
[migration guide](client/MIGRATION.md).

From the repository root with Rust 1.99.0, Python 3.13+ and locked dependencies,
run `python3 rust/qualification/verify.py`. It checks excerpts, matrix evidence,
formatting, lint, tests/doctests, fixtures and packaged consumers. Native tests
and examples need loopback sockets. Browser/Worker qualification has additional
tooling and runtime gates in [the release guide](../docs/release-qualification.md).
Use cached dependencies and `CARGO_NET_OFFLINE=true` for offline replay.
CI also checks the locked dependencies against the RustSec advisory database
with `cargo deny check advisories` under [the advisory policy](deny.toml).
The non-default `qualification` feature is unsupported test instrumentation;
ordinary consumers omit it. [Development status](DEVELOPMENT-STATUS.md) retains
the broader qualification history and open work.
