# @openbindings/openapi-client

A deterministic, document-driven client for Swagger 2.0 and OpenAPI 3.0,
3.1, and 3.2. Load an API description, select an authored operation, and call
it directly—no generated code and no OpenBindings document required.

The package implements the OpenBindings Project's four OpenAPI binding
specifications as an OpenAPI-native client contract. OpenBindings Core, the
OpenBindings SDK, and OB CLI are not runtime dependencies and do not appear in
the public API.

> Status: release candidate. The invocation behavior passes all 888
> hash-locked processor scenarios at the pinned OpenBindings 0.2 authority
> revision, and the public API is candidate-frozen.

## Install

```sh
npm install @openbindings/openapi-client
```

Node.js 18 or newer is supported. Browser and Worker hosts need the standard
Fetch, URL, Headers, Request, Response, Blob, FormData, and AbortSignal APIs.

## Quick start

```ts
import { OpenAPIClient } from "@openbindings/openapi-client";

const client = await OpenAPIClient.load(new URL("https://example.com/openapi.yaml"), {
  auth: { session: process.env.EXAMPLE_TOKEN! },
});

const getPet = client.operation("getPet");
const result = await getPet.call<{ name: string }, { message: string }>({
  parameters: {
    path: { petId: "p-123" },
    query: { include: ["owner", "vaccinations"] },
  },
});

if (result.ok) {
  console.log(result.data?.name);
} else {
  console.error(result.response.status, result.error);
}
```

The generic result types are caller assertions, not runtime schema
validation. A generated typed facade can sit over this client without
reimplementing OpenAPI wire behavior.

## Supported artifacts

The exact accepted editions are:

- Swagger 2.0;
- OpenAPI 3.0.0 through 3.0.4;
- OpenAPI 3.1.0 through 3.1.2; and
- OpenAPI 3.2.0.

`load` accepts a URL string, a `URL`, a parsed document object, or a content
source. A content source can provide a location as its external-reference
base:

```ts
const fromObject = await OpenAPIClient.load(document);

const fromText = await OpenAPIClient.load({
  location: "https://documents.example/openapi.yaml",
  content: yamlText,
});
```

A plain string is a location to retrieve, not document text. Parsed input and
client defaults are snapshotted during loading; later caller mutation cannot
change the loaded client.

Document retrieval is deliberately separate from API invocation:

```ts
const client = await OpenAPIClient.load(source, {
  documentFetch: restrictedArtifactFetch,
  documentSignal: loadController.signal,
  fetch: instrumentedInvocationFetch,
});
```

Use `documentFetch` to enforce artifact allowlists, size limits, proxy policy,
and TLS policy. Those are host security decisions, not OpenAPI semantics.

## Selecting operations

Select by unique `operationId`, path plus method, or canonical OpenAPI
reference:

```ts
await client.call("getPet", input);
await client.call({ path: "/pets/{petId}", method: "get" }, input);
await client.call({ ref: "#/paths/~1pets~1{petId}/get" }, input);

for (const operation of client.operations()) {
  console.log(operation.operationId, operation.wireMethod, operation.path);
}
```

Duplicate operation IDs fail instead of selecting by map order. OpenAPI 3.2
QUERY and case-sensitive `additionalOperations` method tokens are preserved.

## Inputs and request media

Parameter locations remain separate even when the same name appears in more
than one location:

```ts
await client.call("replacePet", {
  parameters: {
    path: { id: "path-id" },
    query: { id: "query-id" },
    header: { "X-Mode": "safe" },
    cookie: { tenant: "acme" },
  },
  body: { id: "body-id", active: false },
});
```

`body` is present whenever the property exists, including for `null`,
`false`, `0`, `""`, and `{}`. Omission means no body. Swagger 2.0 `formData`
uses the same body object rather than a second public input model.

Use `mediaType` when the artifact does not determine one concrete request
representation. Use `propertyMediaTypes` for multipart or form properties
whose Encoding declaration requires a concrete choice.

OpenAPI 3.2 whole-query-component parameters use
`parameters.querystring`; they are distinct from ordinary query parameters.

## Authentication and servers

Credentials are keyed by the scheme names authored in the API description.
The document determines how each value is carried:

```ts
const client = await OpenAPIClient.load(document, {
  auth: {
    tenantKey: process.env.TENANT_KEY!,
    bearer: process.env.ACCESS_TOKEN!,
    admin: { username: "admin", password: process.env.ADMIN_PASSWORD! },
  },
  server: { index: 1, variables: { region: "eu" } },
});
```

Unknown names, incompatible credential shapes, invalid credential bytes, and
unsatisfied security alternatives fail before dispatch. A scheme the built-in
API-key, Basic, Bearer, OAuth 2, or OpenID Connect handling does not own can be
implemented with a scheme-named function:

```ts
await client.call("signedOperation", {}, {
  auth: {
    signature({ request, scheme }) {
      request.headers.set("authorization", sign(request, scheme));
    },
  },
});
```

A complete server replacement is `{ url }`. Authored servers are selected by
`{ index, variables }`; `{ variables }` applies variables to the sole/default
selection when no index is needed.

## Results and errors

A non-2xx HTTP response is a native result, not an exception:

```ts
const result = await client.call("createPet", { body: pet });

result.response.status;
result.response.headers;
result.openapi.declared;
result.openapi.responseKey;
result.openapi.mediaType;
```

`result.ok === true` exposes decoded `data`. `result.ok === false` exposes the
declared failure `error`, when one can be decoded. The concrete `Response` and
OpenAPI declaration match remain available in either branch. Unary and
unsuccessful responses retain a replayable, delivery-bounded response body.
For a successful `stream()` result, the engine exclusively owns body
consumption: the `Response` retains native status, headers, URL, and redirect
metadata, but its body branch is cancelled so observing it cannot defeat
backpressure or buffer an unbounded stream.

Source, operation-selection, input, configuration, transport, protocol,
response-decoding, cancellation, and internal failures throw
`OpenAPIClientError`. Its `kind` is the stable coarse category; `code` is the
more specific machine-readable reason. The category set is closed for this
major version; code values are stable when documented or exported, but the
code space is extensible so callers should retain a default branch.

When an artifact leaves a required choice open, the error uses
`code === "CONFIGURATION_REQUIRED"` and supplies actionable alternatives:

```ts
try {
  await client.call("upload", { body: bytes });
} catch (error) {
  if (error instanceof OpenAPIClientError && error.requirements) {
    for (const alternative of error.requirements.alternatives) {
      console.log(alternative); // all entries in one alternative are required
    }
  }
}
```

Requirements point directly to the public surface: `kind: "input"` names
`mediaType` or `propertyMediaTypes`, `kind: "option"` names an actual client or
call option such as `server` or `securityAlternative`, and
`kind: "credential"` names an authored security scheme. No OpenBindings
context object or internal configuration-point spelling is exposed.

## Streaming

`stream` is valid for unary operations and required for sequential media or
Server-Sent Events:

```ts
const result = await client.stream<string>("watchPets");
if (!result.ok) throw new Error(`HTTP ${result.response.status}`);

for await (const event of result.events) {
  console.log(event.data, event.sse?.event, event.sse?.id);
}
await result.closed;
```

The async iterable preserves ordering, partial delivery, backpressure, and
cancellation. `cancel()` stops response consumption. `maxDeliveryUnitBytes`
bounds each delivered value or event rather than the lifetime of the stream.
Unary `call` rejects a sequential response with `STREAMING_RESPONSE` instead
of buffering it without a bound.

## Redirects, middleware, and transports

Redirects default to `manual`, so the response to the authored operation stays
observable. `redirect: "follow"` follows only method-and-body-preserving hops.
Selected credentials and Cookie are not forwarded or reconstructed across an
origin boundary.

Middleware is HTTP-native and ordered:

```ts
const client = await OpenAPIClient.load(document, {
  middleware: [{
    onRequest({ operation, request }) {
      request.headers.set("x-operation", operation.operationId ?? operation.ref);
    },
    onResponse({ response }) {
      metrics.observe(response.status);
    },
  }],
});
```

The platform Fetch API cannot carry every valid HTTP method token. On Node,
the client uses a byte-preserving host transport for methods such as TRACE.
Browser-like hosts can provide `transport`; `null` explicitly declares that
no such transport exists. The client refuses before dispatch rather than
silently changing an authored method.

Request and response content-coding codecs, character codecs, parameter
conversion, server selection, security-alternative selection, headers,
transport, redirect policy, cancellation, and delivery limits can be set as
client defaults and overridden per call.

## Package boundary

The package intentionally exports one OpenAPI-native entry point. Internal
parser models, development profiles, routed-input envelopes, OpenBindings
context shapes, and synthesis helpers are not public compatibility surface.
The future OpenBindings adapter and OB CLI integration will consume this
client rather than constrain its design.

## License

Apache-2.0
