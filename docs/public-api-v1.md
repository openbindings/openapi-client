# Native client and engine contract

Status: implementation contract for the next public major version. Compatibility
with the repository's pre-release APIs is intentionally not a constraint.

## Product promise

The TypeScript package and Go module implement one document-driven OpenAPI
client in two idiomatic languages. A caller can load an exact Swagger 2.0,
OpenAPI 3.0, OpenAPI 3.1, or OpenAPI 3.2 artifact and invoke its operations
without an OpenBindings document, SDK, adapter, or CLI.

The four OpenBindings OpenAPI binding specifications are the behavioral
authority. They close OpenAPI's client-side choices; they do not become runtime
dependencies or public client vocabulary. The pinned authority and portable
corpus are development and release evidence only.

## Layer boundary

```text
direct application ─────────────────────┐
generated typed facade (optional) ─────┼──► native client: load / inspect / call / stream
OpenBindings SDK ─► OpenAPI adapter ────┘                         |
                                                               private engine
                                                                    |
                                                 editions / transport / codecs
```

Neither the client nor the engine may import OpenBindings Core, use an OBI as
execution authority, or expose binding identifiers in its API. The adapter
consumes only supported native package surface; it may not import private
engine files or reimplement OpenAPI request or response behavior. OB CLI
reaches the same client through the OpenBindings SDK and adapter.

## Shared semantic model

Both languages expose the same concepts and outcomes:

- one loader for all four editions;
- an immutable loaded artifact with an exact `edition`;
- stable operation inventory and selection by unique operation ID, canonical
  reference, or path plus method;
- exact support for OAS 3.2 `query` and case-sensitive
  `additionalOperations` method tokens;
- grouped path, query, header, cookie, and query-string parameters;
- one application body plus an explicit presence bit where the host language
  needs it, a concrete request media choice, and property media choices;
- scheme-named credentials and an explicit security-alternative choice;
- discriminated server selection: authored index plus variables, or a complete
  replacement URL;
- request and response content-coding capabilities;
- unary `call` and streaming `stream` convenience over the same prepared
  execution;
- a success/failure result split for HTTP application outcomes, while source,
  configuration, input, transport, protocol, cancellation, and internal
  failures are typed client errors;
- protocol-native response evidence and declaration-match evidence;
- typed pre-dispatch failures and protocol-native response evidence;
- native `CONFIGURATION_REQUIRED` alternatives for missing server, media, and
  credential choices, without exposing OpenBindings context vocabulary;
- cancellation, backpressure, and exactly one terminal outcome.

Unary and unsuccessful outcomes expose a replayable, delivery-bounded native
response body. A successful stream has exactly one body consumer: the engine.
Its public response retains native status, header, URL, and redirect metadata
but cannot be read as a second body stream. This is the same ownership rule in
both languages and prevents response observation from defeating backpressure.

Wire behavior and outcome classification must be identical across TypeScript
and Go. Host-language spelling, option construction, iteration, and error
inspection should be idiomatic rather than textually identical.

## TypeScript surface

The package root is deliberately small:

```ts
import {
  OpenAPIClient,
  OpenAPIClientError,
  type OpenAPICallInput,
  type OpenAPICallOptions,
  type OpenAPIClientOptions,
  type OpenAPIEdition,
  type OpenAPIOperationInfo,
  type OpenAPIOperationSelector,
  type OpenAPIResult,
  type OpenAPIStreamResult,
} from "@openbindings/openapi-client";

const client = await OpenAPIClient.load(source, options);
client.edition;
client.operations();
client.operation(selector);
await client.call(selector, input, callOptions);
await client.stream(selector, input, callOptions);
```

`OpenAPIOperationInfo.method` is a string because OAS 3.2 can author extension
method tokens. It also reports `wireMethod` and whether the operation is a
fixed field or an additional operation. Selectors preserve an authored
additional method byte-for-byte; only known fixed fields use their defined
lowercase spelling.

`OpenAPICallInput.parameters.querystring` represents OAS 3.2's whole query
component. `body` is present when the property exists, including for `null`,
`false`, `0`, and the empty string. Swagger 2.0 `formData` is presented as the
native application body object rather than leaking a second public input
shape.

The package has one supported entry point. Internal artifact models, edition
helper functions, development profiles, routed-input markers, and
binding-adapter synthesis types are deliberately not exported. A lower-level
surface will be published only if it can be expressed as an OpenAPI-native
contract shared by both languages; current OpenBindings integration shapes do
not qualify.

## Go surface

The Go module uses the same native concepts:

```go
client, err := openapi.Load(ctx, source, options)
edition := client.Edition()
operations := client.Operations()
result, err := client.Call(ctx, openapi.OperationID("getPet"), input, call)
stream, err := client.Stream(ctx, openapi.OperationRef("#/paths/~1events/get"), input, call)
```

Go uses explicit `Present` fields where a zero value cannot distinguish
omission. `Server`, `ServerVariables`, and `ServerURL` construct the three
server-selection forms without an ambiguous options object. Options use typed
discriminated structs rather than `any`. Loaded
document internals are not returned as mutable pointers. Advanced preparation
and analysis APIs may be added once doing so improves package clarity;
the root package is judged by its exported identifiers, not by preserving the
current single-package layout.

## Deliberate public decisions

1. `maxDeliveryUnitBytes` is the name of the per-value bound. It is not called
   `maxResponseBytes`, because a streamed response can contain many bounded
   delivery units.
2. Redirect following defaults to manual. Following is explicit, and
   cross-origin credential behavior follows the binding specifications.
3. Custom security handlers are scheme-scoped and receive the planned native
   request. They cannot claim that another alternative is satisfied.
4. Middleware observes native planned requests and responses. Its model must
   carry every valid HTTP method token; WHATWG `Request` limitations cannot
   make an otherwise supported operation inaccessible.
5. Generic TypeScript result parameters are assertions by the caller, not
   runtime validation and not generated typing. Generated schema-derived
   clients are a separate facade over this runtime.
6. Loading snapshots caller-owned parsed input. Mutating the original object
   after `load` cannot change later inventory or wire behavior.
7. Native configuration requirements name the public remedy, not the binding
   specifications' internal configuration point: `mediaType` and
   `propertyMediaTypes` are call inputs; `server`, `securityAlternative`, codec
   maps, and conversion are options; security scheme names are credentials.
8. No compatibility aliases or deprecated shims are required for the current
   pre-release client, engine, OpenBindings adapters, SDKs, or OB CLI.

## Release gates for the surface

The public surface freezes only when:

- clean ESM, CommonJS, browser-compatible TypeScript, and external Go consumers
  exercise all four editions;
- the exported API is captured by an intentional manifest/API snapshot;
- examples cover load, inspect, configure, call, stream, cancel, custom
  transport, custom security, and configuration-required recovery;
- no OpenBindings package, type, identifier, or routed-input marker leaks;
- error categories and result variants have exhaustive contract tests, while
  exported code contracts are tested and the code space remains extensible;
- a generated facade can be implemented without bypassing the native client;
- the OpenBindings adapters and OB CLI can be rewritten as consumers of this
  surface without adding OpenAPI wire logic.
