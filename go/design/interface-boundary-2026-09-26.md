# Public API boundary pass

This pass changes the proposed public API and examples only. It assumes
each public contract will be implemented and evaluates whether a caller can
express the intended operation.

## Product boundary

The product is a general-purpose dynamic OpenAPI client. A generated typed
client or another library with conventions of its own can use it as an
engine. Their type generation, tool schemas, user interaction and result
conventions are caller-owned.

`Client.Operations` covers outbound operations under `paths`. OpenAPI
callbacks and webhooks describe requests initiated by an API provider
toward its consumer. Their authored declarations remain available through
`Client.Document`; interpreting a callback runtime expression or serving
an incoming webhook is outside this outbound caller API. Thus the
invocation claim below concerns valid, resolvable `paths` operations,
not every Operation Object in an OpenAPI document.

## Decisions

1. **Keep explicit selections.** One usable server, security alternative
   or concrete request media type can be used directly. Several alternatives
   require an ordinary setting; credentials, list order and input Go types
   do not elect one. `Operation` describes the available values and
   `RequestError.Settings` names the missing field. No choice log or UI
   abstraction is part of the client.
2. **Make pre-preparation escapes composable.** `Input.ParamWriters`
   substitutes serialization for any identified declared parameter. A
   `[]byte` or `io.Reader` body supplies the entire representation under a
   concrete `Input.MediaType`, bypassing structured body, form and schema
   interpretation. `Options.BaseURL` replaces unusable authored servers.
   `FromTransport` delegates credential placement. These escapes can be
   combined, so no general "turn off OpenAPI" switch is added.
3. **Keep the point of refusal narrow.** A raw body cannot be rejected
   because the schema or structured encoder is unsupported. A parameter
   writer bypasses that parameter's serialization error. Refusal remains
   appropriate when a method, path, parameter identity, governing declared
   media key or selected security alternative cannot be determined, or
   when the caller has not supplied a consequential selection.
4. **Handle empty request-body content explicitly.** OpenAPI 3.x permits
   an empty `requestBody.content` map with implementation-defined behavior.
   This client accepts a caller-encoded body with an explicit concrete
   media type. There is no governing `Media` descriptor and no structured
   encoder. The alternative is an actionable refusal for a missing type.
5. **Keep custom HTTP policy above the client.** Once `Prepare` succeeds,
   callers may edit the unsigned `Request.HTTP`, including adding a body
   absent from the description; `Request.Send` leaves every response
   status and body open. The caller owns the wire meaning of those edits.
   A failed preparation does not expose a half-built request.
6. **Separate schema conversion.** The core exposes authored documents,
   schema dialects, source identity and standard reference edges.
   Directional JSON Schema 2020-12 conversion belongs in the optional
   `openapi/schema2020` package. Invocation never calls that package and
   custom dialect users can process the raw graph themselves.
7. **Honor Swagger 2.0 server schemes.** Swagger 2.0 permits `ws` and
   `wss` as well as `http` and `https`. The interface does not discard
   these servers or reject them as BaseURL values. A caller's
   HTTPClient.Transport owns WebSocket handshake and framing semantics;
   Request.Send exposes a 101 or other response without 2xx policy.
   Built-in Basic and bearer placement treats `wss` as secure and `ws`
   as plaintext. Custom schemes are delegated to the transport.

## Configurability inventory

| Decision the description may leave open | Public control |
| --- | --- |
| Deployment, relative server, or missing server variable | `Options.Server`, `ServerID`, `BaseURL`, `Variables`, and `With` |
| Security alternative, credentials, token lifetime, custom signing | `Security`, `SecurityKey`, `Input.Security`, `Credential`, `HTTPClient.Transport` |
| Request or part representation | `Input.MediaType`, `Part.MediaType`, raw body bytes or reader |
| Parameter spelling outside built-in serialization | `Input.ParamWriters` |
| Reference retrieval and name resolution | `Loader.Fetch`, `AllowReference`, `Origins`, `SchemeLookup` |
| Accept, headers, redirects and HTTP middleware | `Header`, `Redirects`, `HTTPClient`, editable `Request.HTTP` |
| Success policy, unusual response media, streaming framing | `Request.Send`, `Response.Body`, `Response.Decode`, `Stream` |

## Edition pass through the interface

| Edition-specific valid use | Public route |
| --- | --- |
| Swagger 2.0 host, basePath and `http`/`https`/`ws`/`wss` schemes | Effective `Operation.Servers`; select with `Server`, `ServerID` or `BaseURL`; custom transport for `ws`/`wss` |
| Swagger 2.0 body, formData, consumes and produces | `Operation.Body.Media`, `Input.Body` and `MediaType`; raw bytes for caller-owned form encoding; `Response.Media` and raw `Body` |
| OpenAPI 3.0 parameter styles, cookie inputs, media ranges and ignored request bodies on GET/HEAD/DELETE | `Params` or `ParamWriters`, explicit `MediaType` for a range, and editable `Request.HTTP` after a bodyless `Prepare` |
| OpenAPI 3.1 JSON Schema dialects, reference siblings and permitted bodies on GET/HEAD/DELETE | `Schema.Raw`, `Dialect`, `References`; `Input.Body` for a declared body on those methods |
| OpenAPI 3.2 QUERY, additional methods, whole-query parameters, positional multipart and sequential media | `Operation.Method`, `ParamWriters` for a caller-owned query format, `Part` or raw body, `Stream` and `Request.Send` |
| Any edition's opaque or unusual media, custom auth and response convention | Raw `Input.Body`, `FromTransport`, `Request.Send`, open `Response.Body` |

## Remaining interface gate

Before calling the interface S-tier, walk a complete set of *valid*
Swagger 2.0 and OpenAPI 3.0, 3.1 and 3.2 `paths` operations through these entry
points on paper. For each refusal, identify the public setting or raw path
that resolves it. A refusal caused solely by an unsupported schema dialect,
media codec, form representation or known parameter serializer is an
interface defect. An inaccessible reference that hides mandatory wire
facts is a document-loading problem; the caller can authorize or supply
the reference through `Loader`. This is a design audit, distinct from
runtime conformance testing.
