# Go parity port

Status: completed for the current pre-release support boundary. This document
remains the maintenance contract for future cross-language changes.

The Go client will implement the same native contract as the TypeScript client. It must not be a façade that exposes native-looking methods while importing the OpenBindings binding package as its execution implementation.

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

`Result` will distinguish successful and non-2xx HTTP outcomes while retaining `*http.Response`, decoded data/error values, and governing OpenAPI declaration information. Local source, selection, configuration, transport, protocol, decode, cancellation, and implementation failures will be typed Go errors.

Streaming will use an explicit session with an ordered receive channel or iterator method, `Close/Cancel`, response metadata, and a terminal error. It will preserve partial values before a terminal error.

## Extraction sequence

1. Move document loading/resolution, parameter/media/server/security planning, HTTP execution, response matching, and SSE framing behind an SDK-neutral engine package. **Complete.**
2. Define small native request/result/error/session types in this repository. **Complete.**
3. Run language-neutral conformance cases in both clients. **Complete.**
4. Implement the native `Client` over the engine. **Complete.**
5. Replace the Go binding package's direct runtime implementation with an adapter over the engine. **Complete.**
6. Keep OBI synthesis in `openbindings-go/formats/openapi`; share document-analysis helpers only where their ownership is genuinely artifact-native. **Preserved.**
7. Delete the old execution mirror after differential parity passes. **Complete.**

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
