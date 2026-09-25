# Caller scenarios

Every candidate design writes each scenario below as a compilable Go
`Example` against its own exported declarations, with stub bodies. The
examples are the design's evidence: reviewers grade the caller code, not the
declarations alone. A scenario the design cannot express cleanly is a design
defect.

The client is a plain OpenAPI client. No scenario involves any concept beyond
OpenAPI, HTTP, and Go.

## Integrating a known API (application developer)

1. **First call.** Load a document from a URL, call an operation with one
   path parameter and one query parameter (an integer), and decode the JSON
   result into the caller's struct. No configuration beyond the document.
2. **Create.** Send a JSON body built from a struct; read a response header
   (for example `Location`) from a 201.
3. **Failure.** The server answers 404 or 422 with a declared error body.
   Decode it into the caller's error struct, and tell it apart from a
   transport failure, a cancelled context, and a response the document does
   not declare.
4. **Credentials.** In turn: an API key, a static bearer token, a token that
   refreshes (an `oauth2.TokenSource` or a callback), HTTP Basic, and mutual
   TLS supplied through the caller's own `http.Client`. Also a document that
   offers several security alternatives when the caller holds credentials
   for more than one.
5. **Environments.** Pick staging or production: a server by URL, a server
   the document lists, and server variables.
6. **Files.** Upload a file as a multipart part with other fields; download
   a binary response to a writer without holding it all in memory where the
   design allows.
7. **Pagination.** Loop over pages the caller drives (cursor in the result,
   passed back as a parameter).

## Running it in production (platform engineer)

8. **Shared transport and middleware.** Many clients share one
   `http.Client`; a RoundTripper adds tracing and metrics labelled with the
   operation, and retries idempotent calls.
9. **Timeouts and cancellation.** Per-call deadlines; cancelling mid-call and
   mid-stream; knowing which of the two happened.
10. **Concurrency.** One loaded client used from many goroutines.
11. **Redirects and credential safety.** Opting into following redirects;
    credentials never crossing to another origin.
12. **Bounds.** Limits on document size and on response size, and what the
    caller sees when one is hit.

## Building tools over unknown documents (tooling author)

13. **Enumerate and describe.** List operations; for one, describe its
    parameters, request body, and responses well enough to build a JSON
    Schema for an AI tool's input and to render a form.
14. **Dynamic call.** Call with inputs from JSON (`map[string]any`, numbers
    kept exact) and get a dynamic JSON result, numbers kept exact.
15. **What is missing.** Before calling, find out what the caller still has
    to choose or supply (a server, credentials, a security alternative, a
    media type), present it to a user, supply it, and call.
16. **Streaming.** Consume server-sent events and JSON Lines as they arrive;
    stop early; read the raw stream when the caller wants its own framing.
17. **Sloppy documents.** Load a document with a defect in one operation;
    the rest work, and the defective one reports why.

## Building on top of the client (a generic library author)

18. **Exact behavior, no defaults.** A library wrapping the client for
    arbitrary documents wants every open choice made explicitly (no
    convenience defaults), gets a refusal it can present when a choice is
    missing, and can classify responses and decode custom media itself.
    It asks only for OpenAPI and HTTP facts, never for anything shaped for
    its own purposes.

## Reviewing the package (Go API reviewer)

19. **The package itself.** `go doc` reads well top to bottom; zero values
    are safe or impossible; errors work with `errors.Is` and `errors.As`;
    options compose; the exported surface is as small as the scenarios
    allow; nothing would need to break to add the obvious next features.
