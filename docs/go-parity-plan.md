# Go parity port

Status: native-client parity complete; downstream OpenBindings migration is
deferred until the standalone surface is accepted.

The Go client implements the same native contract as the TypeScript client. It
is not a façade that exposes native-looking methods while importing the
OpenBindings binding package as its execution implementation.

## Public shape

The idiomatic Go API will center on:

```go
client, err := openapiclient.Load(ctx, source, options)
result, err := client.Call(ctx, openapiclient.OperationID("getPet"), openapiclient.Input{
    Parameters: openapiclient.Parameters{
        Path:  map[string]any{"petId": "p-123"},
        Query: map[string]any{"include": []any{"owner"}},
    },
})
```

`Result` distinguishes successful and non-2xx HTTP outcomes while retaining
`*http.Response`, decoded data/error values, and governing OpenAPI declaration
information. Local source, selection, configuration, transport, protocol,
decode, cancellation, and implementation failures are typed Go errors.

Streaming uses an explicit session with an ordered iterator method,
`Cancel`, response metadata, and a terminal error. It preserves partial values
before a terminal error.

## Extraction sequence

1. Move document loading/resolution, parameter/media/server/security planning, HTTP execution, response matching, and SSE framing behind an SDK-neutral engine package. **Complete.**
2. Define small native request/result/error/session types in this repository. **Complete.**
3. Run language-neutral conformance cases in both clients. **Complete.**
4. Implement the native `Client` over the engine. **Complete.**
5. Freeze the clean standalone surface without preserving current adapter, SDK,
   or OB CLI APIs. **In qualification.**
6. Add the smallest public, immutable OpenAPI-native analysis capability needed
   by generators and thin binding adapters. **Deferred to the analysis phase.**
7. Rewrite the Go binding package as an adapter over the accepted native
   surface, then migrate the SDK and OB CLI. **Deferred.**
8. Delete the displaced adapter execution mirror only after differential parity
   and all portable synthesis scenarios pass. **Deferred.**

## Why this is not a wrapper-first port

The current Go `formats/openapi` runtime depends on Core invocation handles, context requirements, hooks, errors, metadata, and binding argument types throughout the execution path. A thin new repository wrapper could hide those types from signatures, but it would preserve the architectural coupling and make the binding package the standalone product's upstream dependency.

That would invert the intended dependency direction. The correct port first extracts the engine's neutral carrier, then makes both the native client and the OpenBindings adapter consumers of it.

## Parity gate

- identical operation selection and pre-dispatch refusal;
- exact wire parity for every shared fixture;
- identical concrete media and response-declaration selection;
- identical application values and raw-byte boundaries;
- identical auth-alternative behavior, including custom native schemes;
- identical SSE ordering, delivery-unit bounds, cancellation, and completion;
- no OpenBindings types in the public Go API or engine package;
- no Core document-model change.
