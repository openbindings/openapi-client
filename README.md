# OpenAPI Client

Document-driven OpenAPI clients for TypeScript/JavaScript and Go.

**The Go invocation client is implemented.** It is a general-purpose,
highly configurable dynamic client for Swagger 2.0 and OpenAPI 3.0, 3.1 and
3.2 whose only authorities are the OpenAPI specifications and the RFCs they
rely on. It depends on no OpenBindings code or concepts; binding-specification
conformance belongs to adapters built on it. It loads JSON and YAML documents,
calls operations, exposes editable prepared requests and streaming responses,
and provides descriptors and a lazy authored-schema graph. The optional
`schema2020.Project` helper remains unimplemented. See
[go/README.md](go/README.md) and [go/DESIGN.md](go/DESIGN.md). This is the
direction for the repository: the TypeScript client will follow the Go design
once it settles.

Apart from the Go section, this README, and the documents under `docs/`,
`api/`, `conformance/` and `authority/`, describe the TypeScript package and
the earlier design that the Go restart supersedes.

> TypeScript status: candidate under qualification. The current authority includes 964
> hash-locked portable processor scenarios. Native, race, package, real-host,
> API, and clean-consumer checks are separate gates; a source candidate is not
> a published release or a claim that every ecosystem gate has passed.

The TypeScript package preserves JSON numbers across source, request, and
response boundaries. This is the official clients' implementation-quality policy, not a
requirement that every implementation choose the same host representation.
Candidate source and packed-consumer qualification do not publish packages.

## TypeScript

```ts
import { OpenAPIClient } from "@openbindings/openapi-client";

const client = await OpenAPIClient.load(new URL("https://example.com/openapi.yaml"), {
  auth: { session: process.env.EXAMPLE_TOKEN! },
});

const result = await client.call("getPet", {
  parameters: {
    path: { petId: "p-123" },
    query: { include: ["owner", "vaccinations"] },
  },
});

if (result.ok) console.log(result.data);
else console.error(result.response.status, result.error);
```

See the [TypeScript package guide](typescript/README.md) for sources,
selection, inputs, authentication, results, streaming, middleware, transports,
redirects, and [exact JSON number handling](typescript/README.md#json-number-values).

## Go

```go
import "github.com/openbindings/openapi-client/go/openapi"

c, err := openapi.Load(ctx, "https://example.com/openapi.yaml", nil)
if err != nil {
    log.Fatal(err)
}

var pet Pet
_, err = c.Call(ctx, "getPet", &openapi.Input{
    Params: map[string]any{"petId": "p-123"},
}, &pet)
```

Load reads a document once into a Client that is safe for concurrent use.
Call names an operation by operationId or `"METHOD /path"`, sends it, and
decodes a 2xx into your value; Stream, Prepare and Request.Send cover streams,
inspection before sending, and caller-owned response policy. Where a document
offers several servers, security alternatives or media types, the client
never picks one: the call names the setting it needs. The package
documentation holds every rule; the examples in
`go/openapi/example_test.go` walk through each caller scenario.

## Product boundary

The repository deliberately publishes one small application client and one
advanced OpenAPI-native provider surface per language. Routed OpenBindings
envelopes, OBI synthesis structures, and existing OB CLI integration APIs are
not public compatibility constraints.

```text
direct application ───────────────────────► native OpenAPI client
generated typed facade (optional) ────────► native client + provider
OpenBindings SDK ─► OpenAPI adapter ──────► native client + provider
                                                        |
                                                   private engine
                                                        |
                                          editions / transport / codecs
```

The adapter may select bindings and translate OpenBindings lifecycle and
values. It consumes supported native package surface and may not import private
engine files or reimplement OpenAPI request serialization, security, redirect,
response, or streaming behavior. The OpenBindings SDKs and OB CLI consume this
adapter through their generic prepared-provider boundary; no OpenAPI-specific
selection or invocation policy exists above it.

## Deterministic scope

Both clients own:

- exact-edition loading and reference closure;
- operation inventory, canonical references, QUERY, and OpenAPI 3.2
  additional method tokens;
- server resolution and complete URL construction;
- effective parameters and style/explode/content serialization;
- JSON, character, raw, URL-encoded, multipart, and sequential media lanes;
- request-media and property-media choice;
- security alternatives and credential placement;
- request and response content codings;
- response-key and media selection, decoding, and native failure evidence;
- redirect safety, cancellation, delivery limits, backpressure, and exactly
  one terminal outcome.

Callbacks and webhooks are reverse interactions rather than ordinary client
calls. Generated schema types, validation-as-policy, link traversal, mocking,
server implementation, and documentation rendering are separate products.

## Authority and conformance

The exact OpenBindings 0.2 source revision and processor/synthesis corpora are
hash-locked under `authority/` and `conformance/upstream/`. They are
development and release evidence, never runtime dependencies.

The release loop requires:

- all pinned portable processor scenarios in TypeScript;
- native and race-enabled test suites;
- exact public API snapshots;
- exactly two intentional TypeScript exports and the reviewed Go package
  surface;
- clean installed ESM, CommonJS, browser-bundle, and external Go consumers;
- cancellation, redirect, security, streaming, and size-bound tests; and
- no unresolved high- or medium-severity review finding.

Full OBI synthesis and OpenBindings lifecycle conformance are qualified in the
separate adapters. Invocation already crosses a thin mechanical bridge to the
client engines. Synthesis now derives from the detached native provider
projection in both languages and passes the portable corpus; the displaced
adapter-owned OpenAPI planners and executors have been removed. SDK
registration and OB CLI migration are complete on the integration cohort. OB
retains bounded prepared provider revisions, renders process-local
operation-validation locations without changing portable error records, and
keeps raw binding invocation as an explicit below-operation diagnostic lane.

See [architecture](docs/architecture.md), [public API contract](docs/public-api-v1.md),
[qualification ledger](docs/extraction-ledger.md), [release qualification](docs/release-qualification.md),
[OpenBindings migration](docs/openbindings-migration.md), and
[conformance](conformance/README.md).

## Development

```sh
pnpm install
pnpm qualify:release
```

## License

Apache-2.0
