# OpenAPI Client

Deterministic, document-driven OpenAPI clients for TypeScript/JavaScript and
Go. Both implementations load Swagger 2.0 and OpenAPI 3.0, 3.1, or 3.2
descriptions and invoke authored operations directly.

No generated code or OpenBindings Interface is required. The four
OpenBindings OpenAPI binding specifications supply the deterministic behavior,
but the published clients are OpenAPI-native and have no OpenBindings runtime
dependency.

> Status: candidate under qualification. The current authority includes 964
> hash-locked portable processor scenarios. Native, race, package, real-host,
> API, and clean-consumer checks are separate gates; a source candidate is not
> a published release or a claim that every ecosystem gate has passed.

Both implementations preserve JSON numbers across source, request, and response
boundaries. This is the official clients' implementation-quality policy, not a
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
import openapi "github.com/openbindings/openapi-client/go"

client, err := openapi.Load(ctx, openapi.FromURL("https://example.com/openapi.yaml"), openapi.Options{
    Auth: openapi.Credentials{
        "session": openapi.Token(os.Getenv("EXAMPLE_TOKEN")),
    },
})
if err != nil {
    log.Fatal(err)
}

getPet, err := client.Operation(openapi.OperationID("getPet"))
if err != nil {
    log.Fatal(err)
}
result, err := getPet.Call(ctx, openapi.Input{
    Parameters: openapi.Parameters{
        Path:  map[string]any{"petId": "p-123"},
        Query: map[string]any{"include": []any{"owner", "vaccinations"}},
    },
})
if err != nil {
    log.Fatal(err)
}
if result.OK {
    fmt.Println(result.Data)
} else if result.ErrorPresent {
    // Error may be nil: a JSON null body is still a present failure value.
    fmt.Println(result.Response.StatusCode, result.Error)
} else {
    fmt.Println(result.Response.StatusCode, "no failure body")
}
```

The Go module exposes the same concepts idiomatically:

- `FromURL`, `FromBytes`, and `FromText` make source intent explicit;
- `OperationID`, `OperationRef`, `PathOperation`, and
  `AdditionalOperation` select operations;
- `Parameters` keeps path, query, whole-querystring, header, and cookie
  identities separate;
- `Token`, `Basic`, and `CustomSecurity` provide typed, scheme-named
  credentials;
- `Server`, `ServerVariables`, and `ServerURL` create mutually exclusive server
  selections;
- `Call` returns unary outcomes and `Stream` preserves ordered delivery,
  cancellation, backpressure, and SSE metadata;
- non-2xx HTTP outcomes are native results, while local and protocol failures
  are typed `ClientError` values.

Decoded JSON response numbers (including failure bodies and sequential JSON
items) are `encoding/json.Number`, not `float64`. This implementation selects
the binding's permitted exact-carriage alternative. `json.Marshal` preserves
these as JSON numbers. For computation, explicitly choose `Int64`, `Float64`,
or an appropriate decimal library and handle conversion errors/rounding.
Generic typed-input adaptation and metadata clones also retain number tokens.
Caller-supplied native floats retain only the value already supplied; they
cannot recover precision lost before the call. Lexically constrained non-JSON
parameter conversion and artifact-loader limitations are separate concerns.

`ClientError.Kind` is the closed coarse classification for the public major
version. `ClientError.Code` supplies a more specific reason; documented or
exported values are stable, while callers should allow future codes.

Missing artifact choices use `CodeConfigurationRequired` and carry typed
`ConfigurationRequirements`. Each alternative is a complete remedy made of
native input fields, option fields, or authored security-scheme credentials;
callers never need an OpenBindings context object.

Security preflight is deliberately staged. When multiple complete authored
security alternatives survive, the first requirement names the scalar
`SecurityAlternative` option and its allowed indices. After that explicit
choice, preflight names only the credentials required by the selected
alternative; credentials never create an implicit preference.

Unary and unsuccessful results retain a bounded replay of `Response.Body`.
For successful streams, `Response.Body` is nil because the stream is its sole
consumer; status, headers, request, and other response metadata remain native.

`DocumentHTTPClient` retrieves artifacts and external references;
`HTTPClient` dispatches operations. Their redirect and security policies are
independent. Invocation redirects default to `RedirectManual`.
`RedirectFollow` follows only method-and-body-preserving hops and strips
selected credentials and Cookie across origins.

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

- all pinned portable processor scenarios in both languages;
- native and race-enabled test suites;
- exact public API snapshots;
- exactly two intentional TypeScript exports and matching clean Go root and
  provider package surfaces;
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
