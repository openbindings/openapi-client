# OpenBindings OpenAPI Client

A document-driven OpenAPI 2.0, 3.0, and 3.1 client for invoking brownfield APIs directly from their OpenAPI documents. Swagger 2.0 has raw-preserving, edition-specific lanes in both Go and TypeScript.

The client does not generate source code and does not require an OpenBindings Interface (OBI). Load a document, select an authored operation, and call it. This client's contract deliberately follows the document and incorporated OpenAPI/HTTP rules for server resolution, parameter serialization, request-body carriage, security placement, response selection, decoding, and streaming.

This repository is also the OpenAPI execution substrate used by the OpenBindings OpenAPI binding adapter. The standalone API is deliberately OpenAPI-native; protocol abstraction belongs in the adapter, not in this client.

> Status: pre-release. The TypeScript and Go clients are runnable and tested;
> their public APIs are being stabilized before the first package releases.

## TypeScript quick start

```ts
import { OpenAPIClient } from "@openbindings/openapi-client";

const client = await OpenAPIClient.load("https://example.com/openapi.yaml", {
  auth: { session: process.env.EXAMPLE_TOKEN! },
});

const result = await client.call("getPet", {
  parameters: {
    path: { petId: "p-123" },
    query: { include: ["owner", "vaccinations"] },
  },
});

if (result.ok) {
  console.log(result.data);
} else {
  console.error(result.response.status, result.error);
}
```

## Go quick start

```go
client, err := openapiclient.Load(ctx, openapiclient.Source{
    Location: "https://example.com/openapi.yaml",
}, openapiclient.ClientOptions{
    Auth: map[string]any{"session": os.Getenv("EXAMPLE_TOKEN")},
})
if err != nil {
    log.Fatal(err)
}

result, err := client.Call(ctx, openapiclient.OperationID("getPet"), openapiclient.Input{
    Parameters: openapiclient.Parameters{
        Path:  map[string]any{"petId": "p-123"},
        Query: map[string]any{"include": []any{"owner", "vaccinations"}},
    },
})
if err != nil {
    log.Fatal(err)
}
if result.OK {
    fmt.Println(result.Data)
} else {
    fmt.Println(result.Response.StatusCode, result.Error)
}
```

Go accepts an absolute artifact URI, UTF-8 JSON/YAML bytes, or an already
parsed `*openapi3.T`. `OperationID`, `PathOperation`, and `OperationRef`
select operations without an OBI. `Client.Stream` exposes ordered SSE values,
framing metadata, cancellation, backpressure, and terminal completion through
`Stream.Next`. Values already emitted remain readable before a terminal failure
is returned.

Swagger 2.0 uses its own raw-preserving Go model and never converts through an
OpenAPI 3.x document. `LoadSwagger20` loads and inventories an exact
`swagger: "2.0"` source, `Swagger20Client.SynthesisModel` exposes native
operation analysis for thin adapters, and `Engine.PrepareSwagger20` selects
one literal path-operation reference through the edition-specific execution
lane.

The TypeScript engine mirrors that boundary with `loadSwagger20` and
`prepareSwagger20` from `@openbindings/openapi-client/engine`; its native
`Swagger20Client.synthesisModel()` supplies detached declaration analysis to
thin adapters without importing OpenBindings vocabulary into the client.

The lower-level Go surface exposes the same OpenAPI wire machinery directly.
`EffectiveServerSet`, `NewServerSet`, `ServerSet.Resolve`,
`AssembleRequestURL`, and `ValidateRequestURL` resolve and complete target
URLs; `ValidateSecurityRequirementCarriage` checks the destinations in one
Security Requirement Object. `RequestContentCodings` and
`ResponseContentCodings` on `ClientOptions`, `CallOptions`, and
`PrepareOptions` provide deterministic HTTP content codecs, while
`DecodeResponseBody` applies the built-in response media lane. Engine callers
can select unary event-stream buffering with `BufferEventStreams` or suppress
the artifact-derived `Accept` preference with `OmitAcceptHeader`.

The grouped parameter shape preserves identities that OpenAPI treats as distinct. A path parameter, query parameter, header parameter, cookie parameter, and body property may legally share a name without overwriting one another.

## Sources and operation selection

`OpenAPIClient.load` accepts:

- a document URL or filesystem path;
- a `URL`;
- an already parsed OpenAPI document; or
- `{ location, content }`, where `content` may be a parsed document, JSON, YAML, or UTF-8 bytes and `location` supplies the resolution base.

Operations can be selected by authored `operationId`, path and method, or canonical OpenAPI binding reference:

```ts
await client.call("getPet", input);
await client.call({ path: "/pets/{petId}", method: "get" }, input);
await client.call({ ref: "#/paths/~1pets~1{petId}/get" }, input);

for (const operation of client.operations()) {
  console.log(operation.operationId, operation.method, operation.path);
}
```

Duplicate `operationId` values fail loudly rather than selecting an arbitrary operation.

## Bodies and media types

The `body` member is the application body, not an OpenBindings envelope:

```ts
await client.call("replacePet", {
  parameters: { path: { petId: "p-123" } },
  body: { name: "Mochi", active: false },
});
```

`false`, `0`, `""`, `null`, and `{}` are preserved as supplied bodies. `undefined` means omitted. When a declaration uses a media range or more than one concrete request representation is admissible, select the concrete representation with `mediaType`.

```ts
await client.call("upload", {
  body: bytes,
  mediaType: "application/octet-stream",
});
```

The Go `Input.PropertyMediaTypes` map supplies a concrete media type for a
multipart or form property when its Encoding `contentType` is a range/list, or
when an OpenAPI 3.0 typeless multipart property has no artifact default. The
client validates each choice against the authored Encoding declaration before
dispatch.

## Authentication

Credentials are keyed by the names authored under `components.securitySchemes`. The client uses the document to determine whether and where each credential rides.

```ts
const client = await OpenAPIClient.load(document, {
  auth: {
    tenantKey: process.env.TENANT_KEY!,
    session: process.env.ACCESS_TOKEN!,
    adminBasic: { username: "admin", password: process.env.ADMIN_PASSWORD! },
  },
});
```

Unknown scheme names and incompatible credential shapes fail before dispatch. OpenAPI security alternatives remain authoritative; the client does not invent a protocol-independent auth policy.

For an authored scheme outside the built-in API-key, Basic, Bearer, OAuth 2, and OpenID Connect adapters, provide a scheme-keyed native handler. The explicit handler both satisfies that named OpenAPI requirement and applies its concrete request behavior; a plain string is never guessed into the wrong scheme.

```ts
await client.call("digestProtected", {}, {
  auth: {
    digest({ request }) {
      request.headers.set("authorization", buildDigestHeader(request));
    },
  },
});
```

## Results and errors

HTTP outcomes are protocol-native in the standalone client:

```ts
const result = await client.call("createPet", { body: pet });

if (result.ok) {
  result.data;                    // decoded application value
  result.response.status;         // concrete HTTP status
  result.response.headers;        // concrete HTTP headers
  result.openapi.responseKey;     // governing Response Object, if any
} else {
  result.error;                   // decoded response body, if any
  result.response;                // exact HTTP response evidence
  result.openapi.declared;        // whether the artifact declared it
}
```

A non-2xx HTTP response is a returned `ok: false` value. Local selection, source, configuration, transport, protocol, response-decoding, cancellation, and implementation failures throw `OpenAPIClientError`. This distinction is native-client ergonomics; the OpenBindings adapter is responsible for translating it into protocol-independent invocation outcomes.

## Streaming

Use `stream` when an operation can return Server-Sent Events or when cardinality is not known in advance:

```ts
const result = await client.stream<string>("watchPets");
if (!result.ok) throw new Error(`HTTP ${result.response.status}`);

for await (const event of result.events) {
  console.log(event.data, event.sse?.event, event.sse?.id);
}
await result.closed;
```

The async iterable preserves ordering, partial outputs, backpressure, cancellation, and completion. SSE `event`, `id`, and `retry` framing is retained on `event.sse`; it is not mixed into the application `data`. `call` rejects a `text/event-stream` response with `STREAMING_RESPONSE` instead of silently buffering an unbounded stream.

## Middleware and HTTP integration

Middleware is intentionally protocol-aware. That is appropriate here because this is the OpenAPI/HTTP layer, below the OpenBindings abstraction boundary.

```ts
const client = await OpenAPIClient.load(document, {
  headers: { "user-agent": "my-service/1" },
  middleware: [{
    onRequest({ operation, request }) {
      request.headers.set("x-trace-operation", operation.operationId ?? operation.ref);
    },
    onResponse({ response }) {
      metrics.observe(response.status);
    },
  }],
  fetch: instrumentedFetch,
});
```

Per-call options override client defaults for credentials, server selection, headers, cancellation, fetch, redirect handling, and response delivery-unit limits.
The client-level `signal` also cancels document loading and becomes the default
for later calls; a per-call signal overrides it.

Redirect responses are observable native outcomes by default (`manual`) so an
artifact-bound method and body are not silently rewritten or replayed. A
standalone TypeScript caller can select `redirect: "follow"`; a Go caller can
supply an `http.Client` with its preferred `CheckRedirect` policy. Artifact
document retrieval still follows redirects so relative references use the
final retrieval URI.

## Scope

The invocation-complete scope is:

- native Swagger 2.0 plus OpenAPI 3.0.x and 3.1.x document loading and reference resolution;
- operation discovery and exact selection;
- document/path/operation server resolution and variables;
- path, query, header, and cookie parameter serialization;
- JSON, text, raw bytes, URL-encoded, multipart, media ranges, and selection;
- OpenAPI security requirements and credential placement;
- HTTP dispatch, redirect policy, response declaration/media matching, decoding, and exact failure evidence;
- SSE framing, delivery-unit limits, cancellation, and backpressure.

Inbound callbacks and webhooks are reverse interactions, not client calls. Code generation, mocking, validation-as-policy, server implementation, documentation rendering, and link traversal are outside this client's invocation scope.

See [Architecture](docs/architecture.md), [Fidelity contract](docs/fidelity-contract.md), [adapter contract](docs/adapter-contract.md), [extraction ledger](docs/extraction-ledger.md), [release qualification](docs/release-qualification.md), [Go parity plan](docs/go-parity-plan.md), and [Conformance](conformance/README.md).

## Development

```sh
pnpm install
pnpm qualify:release
cd go
GOWORK=off go test -race ./...
```

Neither language implementation has a runtime dependency on an OpenBindings
SDK. TypeScript's default entry point exposes the native client while its
lower-level engine and analysis APIs live on explicit subpaths. Go exposes the
native `Client` and lower-level `Engine` from one idiomatic package.

### Execution-engine entry point

Adapter authors and lower-level consumers can use the same implementation
without adopting the native client's result conventions:

```ts
import { OpenAPIEngine } from "@openbindings/openapi-client/engine";

const prepared = await new OpenAPIEngine().prepare({
  source: { location: "https://example.com/openapi.yaml" },
  ref: "#/paths/~1pets~1{id}/get",
  context: { bearerToken: token },
});

// Resolves only after every artifact/configuration check that can be made
// without application input. No input has been accepted at this point.
const execution = await prepared.start();
await execution.send(routedArtifactInput);
await execution.finishInput();

for await (const event of execution.events) {
  consume(event.value);
}
await execution.completed;
```

The `./engine` API returns its own `OpenAPIExecutionError` and neutral session
interfaces; it never constructs an OpenBindings SDK class. Reusable document
analysis primitives are available from `./analysis`. Most application code
should use the native `OpenAPIClient` entry point above.

For abstraction adapters, `openAPIPortableFailureData(error)` returns only a
JSON-domain value selected and decoded through the governing OpenAPI Response
Object and media declaration. Generic `OpenAPIExecutionError.details` and
native response evidence are intentionally not interchangeable with portable
application failure data.

## License

Apache-2.0.
