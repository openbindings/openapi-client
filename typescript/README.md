# OpenBindings OpenAPI Client

A document-driven OpenAPI 3.0 and 3.1 client for invoking brownfield APIs directly from their OpenAPI documents.

The client does not generate source code and does not require an OpenBindings Interface (OBI). Load a document, select an authored operation, and call it. The document remains authoritative for server resolution, parameter serialization, request-body carriage, security placement, response selection, decoding, and streaming.

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

Operation redirects are observable by default (`redirect: "manual"`). Set
`redirect: "follow"` on the client or call only when ordinary user-agent
following is intended. Artifact retrieval follows redirects independently so
external-reference bases use the final retrieval URI.

## Scope

The invocation-complete scope is:

- OpenAPI 3.0.x and 3.1.x document loading and reference resolution;
- operation discovery and exact selection;
- document/path/operation server resolution and variables;
- path, query, header, and cookie parameter serialization;
- JSON, text, raw bytes, URL-encoded, multipart, media ranges, and selection;
- OpenAPI security requirements and credential placement;
- HTTP dispatch, redirect policy, response declaration/media matching, decoding, and exact failure evidence;
- SSE framing, delivery-unit limits, cancellation, and backpressure.

Inbound callbacks and webhooks are reverse interactions, not client calls. Code generation, mocking, validation-as-policy, server implementation, documentation rendering, and link traversal are outside this client's invocation scope.

See [Architecture](docs/architecture.md), [Fidelity contract](docs/fidelity-contract.md), [adapter contract](docs/adapter-contract.md), [extraction ledger](docs/extraction-ledger.md), [Go parity plan](docs/go-parity-plan.md), and [Conformance](conformance/README.md).

## Development

```sh
pnpm install
pnpm qualify:release
```

The package has no runtime dependency on an OpenBindings SDK. Its distributable public entry point exposes only the native client surface.

## License

Apache-2.0.
