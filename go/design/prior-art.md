# Prior art for a dynamic Go OpenAPI client

Research date: 2026-09-24. Scope: public API shape of a Go client that loads any
OpenAPI document (Swagger 2.0, OpenAPI 3.0, 3.1, 3.2) and invokes operations at
runtime with runtime values, without generated code.

Sources and conventions:

- Go standard library citations are to the local Go 1.25.12 toolchain source,
  written as `$GOROOT/src/...:line` where
  `GOROOT=/Users/matt/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.12.darwin-arm64`.
  Go 1.26 and 1.27 facts come from the release notes (links at the end).
- Module cache citations are to `/Users/matt/go/pkg/mod/...` (kin-openapi
  v0.149.0, grpc v1.83.2, jhump/protoreflect v1.18.0, modelcontextprotocol/go-sdk
  v1.6.1).
- Code snippets below are short illustrations of each library's API shape,
  written from its docs or signatures, not copied examples.
- Everything is paraphrased; there is no long quotation.

---

## 0. Executive summary

1. **No mainstream dynamic OpenAPI invoker exists in Go.** Go tooling is either
   code generation (oapi-codegen, ogen, go-swagger) or document
   parsing/validation (kin-openapi, libopenapi). The closest runtime primitive,
   go-openapi/runtime `Submit(*ClientOperation)`, still expects generated param
   writers and response readers. The Go precedents for dynamic invocation live
   outside OpenAPI: grpc-go `ClientConn.Invoke(ctx, method, args, reply)`,
   jhump `grpcdynamic.Stub`, the MCP Go SDK `CallTool`, and openai-go
   `client.Post(ctx, path, params, &result)`. The design space is open, and the
   dynamic clients in other languages (restish, swagger-client,
   openapi-client-axios, openapi-fetch, ReadMe `api`) are the richest guide.
2. **Across the modern Go SDKs, the conventions have converged.** The context
   is the first argument. The client is a long-lived value with no global
   state (stripe removed global state explicitly). Non-2xx responses come back
   as typed errors inspected with `errors.As` (AWS, stripe, go-github, openai-go).
   Response metadata is reachable from both the success path and the error path.
   Lists and streams come back as a handle whose `All()` method returns an
   `iter.Seq2[T, error]`, with metadata, `Err`, and `Close` alongside (stripe
   v85, ogen SSE, gax-go, MCP go-sdk).
3. **The loudest complaints are the same across ecosystems:**
   - Non-2xx treated as success, so `err == nil` hides a 500 (resty #1062,
     oapi-codegen `JSON200 == nil`).
   - Success decided by body presence rather than status (openapi-fetch
     #2291, #2530).
   - Pointer-helper zoos (AWS #205, since softened by Go 1.26 `new(expr)`).
   - Bespoke middleware stacks (AWS).
   - Global registries and global loggers (kin-openapi, stripe, retryablehttp).
   - "Magic" argument inference (ReadMe `api` guesses body vs params with a 25%
     overlap heuristic; `auth()` guesses the scheme).
   - Mutating the caller's `*http.Client` (go-github #3028).
   - Options that silently override other options (google `WithHTTPClient`).
4. **Security is where dynamic clients differ most from typed SDKs.** An
   OpenAPI document is an input that may be untrusted. Restish, go-github
   (PR #4564), and openai-go all bind credentials to caller-approved origins,
   never to servers chosen by the document or reached by a cross-origin
   redirect. Restish evaluates OpenAPI security per operation with OR/AND
   semantics and treats `security: []` as a hard no-auth. kin-openapi documents
   `$ref` SSRF and local-file-read risks.
5. **A small, read-only operation descriptor beats exposing the parser model.**
   kin-openapi exposes a mutable document model, and its README keeps a
   changelog of dozens of sub-v1 breaking changes. Restish explicitly builds a
   neutral operation model that holds no parser-library objects.

---

## 1. Go standard library: the bar

### 1.1 net/http

**Shapes**

```go
c := &http.Client{Transport: rt, CheckRedirect: f, Timeout: d} // zero value usable
req, err := http.NewRequestWithContext(ctx, "GET", url, body)
resp, err := c.Do(req) // err only for transport/protocol failure
defer resp.Body.Close()

type RoundTripper interface{ RoundTrip(*http.Request) (*http.Response, error) }
```

**Good for callers**

- `Client` is a plain struct whose zero value works. Configuration is fields,
  not constructors (`$GOROOT/src/net/http/client.go:58-106`).
- `RoundTripper` is the universal, composable extension point. It must be
  concurrency-safe, must not interpret the response, and must not handle
  "redirects, authentication, or cookies"
  (`$GOROOT/src/net/http/client.go:114-137`). The whole ecosystem composes
  here: retryablehttp, oauth2, httpcache, go-github's rate-limit transports.
- Redirect policy is one hook, and a sentinel lets the caller stop and keep
  the 3xx response (`CheckRedirect`, `ErrUseLastResponse`,
  `$GOROOT/src/net/http/client.go:71-77,489-493`).
- On cross-domain redirects, net/http strips `Authorization`,
  `WWW-Authenticate`, and `Cookie` (`$GOROOT/src/net/http/client.go:44-49`).
- `Request.GetBody` makes bodies replayable for redirects and retries
  (`$GOROOT/src/net/http/client.go:~580`).

**Bad or footguns**

- **A non-2xx status is not an error** (`client.go:438,467,555`). That is
  correct for a protocol-level client. One layer up it becomes resty's and
  oapi-codegen's most-reported confusion (sections 2.5 and 3.1).
- **`DefaultClient` has no timeout** (`client.go:109`). "Don't use Go's
  default HTTP client (in production)" (Nathan Smith) is a canonical warning.
- **`Client.Timeout` covers reading the body** (`client.go:97-105`), so it
  kills healthy long-lived streams. Restish splits header-wait timeout from
  stream lifetime for exactly this reason (section 4.6).
- **Response bodies must be read and closed.** Forgetting leaks connections,
  and golangci-lint ships a `bodyclose` linter for it. Go 1.27 auto-drains
  unread HTTP/1 bodies on close, but `Close` is still required.
- **Auth added in a RoundTripper is re-applied on every redirect hop,**
  defeating net/http's cross-domain header stripping. go-github had to fix
  exactly this (section 2.4).
- **`url.Error` strips only the userinfo password** (`stripPassword`,
  `client.go:1034`; `url.URL.Redacted`, `$GOROOT/src/net/url/url.go:927`).
  Credentials carried in query strings (OpenAPI `apiKey in: query`) leak into
  error strings. retryablehttp #41 is this leak reported in practice.

**Verdict.** Adopt:

- Accept a caller `*http.Client` and let RoundTripper be the only middleware.
- Use context for deadlines.
- Keep request bodies replayable (`GetBody`).

Avoid:

- Inheriting the "non-2xx is fine" rule at the OpenAPI layer.
- Relying on `Client.Timeout` for streams.
- Attaching credentials at the RoundTripper layer.
- Letting `url.Error` text carry query secrets.

### 1.2 database/sql

**Shapes**

```go
db, err := sql.Open(driver, dsn)                 // long-lived, pooled, concurrency-safe
err = db.QueryRowContext(ctx, q, args...).Scan(&a, &b) // errors deferred to Scan
rows, err := db.QueryContext(ctx, q, args...)
defer rows.Close()
for rows.Next() { rows.Scan(&a, &b) }
err = rows.Err()
```

**Good**

- **One handle.** `Open` is called once, the `*DB` is safe for concurrent use
  and rarely closed, and connection failure is not forced at open time
  (`$GOROOT/src/database/sql/sql.go:845-863`).
- **`QueryRow` always returns a non-nil `*Row`, with errors deferred to
  `Scan`** (`sql.go:1846-1851`). The chain reads cleanly with no intermediate
  `if err`. `ErrNoRows` is a sentinel (`sql.go:493`).
- **`Scan(dest ...any)` lets the caller choose destination types.** The
  library converts. This is the model for "decode into what I want".

**Bad**

- The `Next`/`Scan`/`Err`/`Close` protocol has three things to forget
  (`sql.go:3024-3029,3136,3433`). golangci-lint ships `rowserrcheck` and
  `sqlclosecheck` purely for this.
- Proposal golang/go#61637 (whole-row `ScanRow`) was motivated partly by
  iterators.

**Verdict.** Adopt:

- One long-lived client value.
- Caller-chosen decode targets.
- Deferred-error chaining where it reads well.

Avoid:

- Multi-step iteration protocols that need an `Err()` call to be correct.

### 1.3 log/slog

**Shapes**

```go
h := slog.NewJSONHandler(w, nil)                // nil options = defaults
h = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})
logger := slog.New(h)                          // tiny Handler interface
type LogValuer interface{ LogValue() slog.Value } // self-redaction hook
```

**Good**

- **A modern stdlib API chose an options struct, not functional options.**
  A zero `HandlerOptions` is entirely defaults, and `nil` is accepted
  (`$GOROOT/src/log/slog/handler.go:133-135`, `json_handler.go:30`).
- The extension point is one small interface (`Handler`, `handler.go:33`).
- `LogValuer` (`value.go:483-489`) lets a type control how it is logged,
  which is the idiomatic way to make a secret-bearing value self-redacting.
- Go 1.26 added `slog.NewMultiHandler`: composition by value, not by
  framework.

**Verdict.** Adopt an options struct with zero value meaning defaults. Adopt
`slog` (or nothing) instead of a private logger interface. Give credential
types a redacting `String`/`LogValue`.

### 1.4 iter

**Shapes**

```go
type Seq2[K, V any] func(yield func(K, V) bool)
for v, err := range it.All() { ... }
```

**Facts**

- The package doc describes `Seq2` as "conventionally key-value or
  index-value pairs" (`$GOROOT/src/iter/iter.go:25`). It asks APIs to use the
  standard types (`iter.go:118-121`). It gives no error-handling guidance.
- The community has converged on `Seq2[T, error]` anyway. Sinclair Target and
  Boldly Go both survey the alternatives: a `(Seq[T], func() error)` pair, or
  an `Err()` method. The known weakness of `Seq2[T, error]` is setup and
  teardown errors that do not belong to any element.
- Libraries shipping it: stripe-go, go-github `ListIter`, gax-go
  `iterator.RangeAdapter` / `All()`, the MCP Go SDK
  (`ClientSession.Tools(ctx, ...) iter.Seq2[*Tool, error]`,
  `/Users/matt/go/pkg/mod/github.com/modelcontextprotocol/go-sdk@v1.6.1/mcp/client.go:1114`),
  and ogen SSE.

**Verdict.**

- Use `iter.Seq2[T, error]` for streams and sequences.
- Return it from a method (`All()`) on a handle that also carries metadata,
  `Err`, and `Close`, not as the bare return value (see stripe, section 2.3).

### 1.5 encoding/json (v1 and v2)

**Shapes**

```go
dec := json.NewDecoder(r); dec.UseNumber(); dec.DisallowUnknownFields()
var raw json.RawMessage // defer decoding
// v2 (GA in Go 1.27):
json.Unmarshal(b, &v, json.RejectUnknownMembers(true)) // variadic Options
```

**Good**

- `UseNumber` and `json.Number` avoid float64 precision loss for int64 IDs in
  `any` values (`$GOROOT/src/encoding/json/stream.go:37-39`,
  `decode.go:190-191`). This matters for any client that hands back
  `map[string]any`.
- `RawMessage` defers decoding (`stream.go:258-261`). This is the natural
  "raw body" type.
- v2 (`$GOROOT/src/encoding/json/v2/options.go`, `arshal.go:173,408`) uses
  variadic `Options` values for rarely used knobs. That is stdlib precedent
  for variadic options when most callers pass none.
- Go 1.27 graduates `encoding/json/v2` and `jsontext` and backs v1 with the
  v2 engine.

**Verdict.**

- Return raw bytes plus a `Decode(any) error` so callers choose `struct`,
  `map[string]any`, or `json.RawMessage`.
- Decode numbers into `any` without float64 loss.
- Do not invent a private JSON value type.

### 1.6 errors and context

- `errors.Is`/`errors.As` over `%w` chains is the idiom. Go 1.26 adds generic
  `errors.AsType[E]`.
- The Google Go style guide asks for structured error types so callers do not
  match on strings. It also says to keep `%w` chains inspectable but use `%v`
  at system boundaries.
- Context is always an explicit first parameter and never lives in a struct
  or options value (Google style guide).
  - go-openapi/runtime is deprecating its `ClientOperation.Context` field in
    favor of `SubmitContext(ctx, op)`. The field carries a
    `//nolint:containedctx` comment
    (https://github.com/go-openapi/runtime/blob/master/client_operation.go).
  - kin-openapi's `Loader` still carries a `Context` field
    (`/Users/matt/go/pkg/mod/github.com/getkin/kin-openapi@v0.149.0/openapi3/loader.go:58`).

**Verdict.**

- Put `ctx` first in every call that does I/O.
- Return one exported structured error type for HTTP responses, reachable via
  `errors.As`/`AsType`.
- Let transport and context errors pass through wrapped, so
  `errors.Is(err, context.DeadlineExceeded)` works.

---

## 2. Go HTTP API clients and SDKs

### 2.1 aws-sdk-go-v2

**Shapes**

```go
cfg, _ := config.LoadDefaultConfig(ctx)
c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.Region = "us-west-2" })
out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("b"), Key: aws.String("k")},
    func(o *s3.Options) { o.Region = "us-east-1" }) // per-call override, concurrency-safe
p := s3.NewListObjectsV2Paginator(c, in); for p.HasMorePages() { page, err := p.NextPage(ctx) }
var ae smithy.APIError; errors.As(err, &ae) // ErrorCode(), ErrorMessage(), ErrorFault()
var re *awshttp.ResponseError; errors.As(err, &re) // HTTPStatusCode(), ServiceRequestID()
```

**Praised**

- **Options-struct-mutator functional options (`func(*Options)`).** They need
  no per-option constructor, work at client and call level, and per-call
  overrides do not affect concurrent calls (AWS guide, "Using the SDK").
- **Layered, inspectable errors.** `smithy.OperationError` (service and
  operation) wraps `ResponseError` (status, request ID), which wraps
  `APIError` (code, message, fault) or a modeled type (AWS guide, "Handling
  errors").
- `ResultMetadata` on every output carries unmodeled response data.

**Criticized**

- **Pointers everywhere** (`aws.String`). Issue aws/aws-sdk-go-v2#205 argues
  it goes against how Go is supposed to be written. Go 1.26 `new(expr)`
  softens the problem. kin-openapi v0.136.0 deleted its `Ptr` helpers in
  favor of `new(..)`, then v0.137.0 reinstated them for Go 1.25 users.
- **The middleware stack**: five steps, middleware IDs, relative insertion,
  stack-scoped context values. AWS's own guide warns that it can produce
  malformed requests. It is powerful and heavy.
- Streaming bodies are `io.ReadCloser` that callers must always close.

**Verdict.** Adopt:

- Layered errors via `errors.As`.
- Metadata reachable on success.

Consider:

- `func(*Options)` if per-call overrides are needed at all.

Avoid:

- A private middleware pipeline.
- Pointer helpers for optional inputs.

### 2.2 google-cloud-go

**Shapes**

```go
c, err := storage.NewClient(ctx, option.WithCredentialsFile(f), option.WithEndpoint(u))
it := c.Bucket(b).Objects(ctx, q)
for { o, err := it.Next(); if err == iterator.Done { break } }
for o, err := range iterator.RangeAdapter(it.Next) { ... } // gax-go, Go 1.23+
```

**Facts**

- **Iterator Guidelines** (googleapis/google-cloud-go wiki):
  - `Next() (T, error)`, ending with the `iterator.Done` sentinel.
  - `PageInfo()` and a `Pager` for page-at-a-time resumption.
  - `Close`/`Stop` for cleanup.
  - Context fixed at iterator creation.
  - Non-concurrent iterators.
  - Short names.
- gax-go now adapts these iterators to `iter.Seq2`.
- **Option precedence footgun.** The `option.WithHTTPClient` doc says it
  takes precedence over all other supplied options (credentials, endpoint)
  (pkg.go.dev/google.golang.org/api/option). Passing both silently drops the
  credentials. The package also warns that accepting credential
  configuration from sources you do not control is a security risk.
- `iterator.Done` as an error value leaks into tracing as a false error
  (googleapis/google-cloud-go#7711).

**Verdict.** Avoid:

- Sentinel-error iteration.
- Options that silently override other options: conflicting configuration
  should be a construction error.

Adopt:

- Treating externally supplied configuration (for us, the document) as
  untrusted.

### 2.3 stripe-go

**Shapes (v82.1+ `stripe.Client`, README at v86)**

```go
sc := stripe.NewClient(key, stripe.WithBackends(b))
c, err := sc.V1Customers.Create(ctx, &stripe.CustomerCreateParams{Email: stripe.String("a@b")})
c.LastResponse.RequestID; c.LastResponse.RawJSON
for c, err := range sc.V1Customers.List(ctx, p).All(ctx) { ... } // v85+: handle, then All
params.AddExtra("secret_feature", "true")                   // undocumented params
rawrequest.Client{B: b, Key: k}.RawRequest("POST", "/v2/x", body, nil)
```

**Praised**

- **The move to `stripe.Client`.** The migration guide lists its reasons:
  - No dependence on global state (`stripe.Key`), so each client carries its
    own key, HTTP client, and backend, and is easy to mock.
  - `ctx` as the first argument.
  - Method-specific params.
  - Lists as `iter.Seq2`, dropping `Next`/`Current`/`Err`.
  - Names that match the API docs.
- Automatic retries (default 2) are safe because every request gets an
  idempotency key.
- Escape hatches for undocumented features: `AddExtra`, `RawJSON`,
  `rawrequest`.

**Criticized or revised**

- **The bare `Seq2` return (v82) lost response metadata.** In
  stripe/stripe-go#2087, `LastResponse` is nil during iteration and there is
  no list object to reach it through.
- **v85 changed List/Search to return a struct.** You now call `.All(ctx)`
  to iterate, and `Err()`, `Data()`, and `Meta()` are available for manual
  paging. That was a breaking change made purely for ergonomics and metadata
  access (v85.0.0 release notes).
- **The v85 shape passes `ctx` twice** (`List(ctx, p).All(ctx)`), which is
  a wart.
- Global `DefaultLeveledLogger`; logging goes to stderr by default.

**Verdict.** Adopt:

- An explicit client value with no globals.
- A handle-plus-`All()` shape for sequences.
- Raw JSON and extra-parameter escape hatches.

Avoid:

- Bare `Seq2` returns.
- Passing `ctx` in two places for one logical call.
- Global loggers.

### 2.4 google/go-github

**Shapes (v92)**

```go
c, err := github.NewClient(github.WithAuthToken(tok), github.WithHTTPClient(hc)) // options, returns error
repos, resp, err := c.Repositories.ListByOrg(ctx, "org", opt) // (value, *Response, error)
resp.NextPage; resp.Rate
for r, err := range c.Repositories.ListIter(ctx, "org", nil) { ... }
var rl *github.RateLimitError; errors.As(err, &rl)
errors.As(err, new(*github.AcceptedError)) // 202 modeled as an error
repo := &github.Repository{Name: new("foo"), Private: new(true)} // Go 1.26
```

**Praised**

- **Composes with the ecosystem through `http.Client` and transports.**
  Caching (RFC 9111 httpcache), OAuth, and rate limiting are all transports,
  and the README defers conditional requests to a caching transport.
- **Specific error types** (`ErrorResponse`, `RateLimitError`,
  `AbuseRateLimitError`, `AcceptedError`).

**Revised or criticized**

- **Construction became functional options returning `error`** in recent
  majors (v89/v91 per downstream migration notes), replacing
  `NewClient(*http.Client).WithAuthToken()`.
- **PR google/go-github#4564 fixed a credential leak.** Auth transports sent
  credentials after redirects to other hosts, and to absolute URLs on foreign
  origins. The fix:
  - Credentials go only to the configured API and upload origins.
  - Other destinations, including redirect hops, go out unauthenticated.
  - Uploads to untrusted destinations are refused (`ErrUntrustedDestination`).
  - `Authorization` is set in exactly one place.
- **Issue google/go-github#3028: the library mutated the caller's
  `http.Client`.** Passing `http.DefaultClient` corrupted it for the rest of
  the program.
- **The triple return `(v, *Response, error)`** means `_` at nearly every
  call site.
- **Pointer fields everywhere** for resources, now eased by `new(expr)`.

**Verdict.** Adopt:

- Origin-scoped credentials, with one place that sets auth.
- Never mutating the caller's client.
- Typed error variants reachable with `errors.As`.

Avoid:

- The triple return. Put metadata on the result value instead.

### 2.5 go-resty/resty

**Shapes**

```go
resp, err := client.R().SetContext(ctx).SetPathParam("id", "1").
    SetResult(&out).SetError(&apiErr).Get("/pets/{id}")
if err != nil { /* transport */ } else if resp.IsError() { /* 4xx/5xx */ }
```

**Praised.** A fluent, compact builder. v3 adds circuit breaker, retries with
`Retry-After`, SSE, load balancing, and composable middleware (resty.dev,
"New features").

**Criticized**

- **Two error channels** (`err` versus `resp.IsError()`). Issue
  go-resty/resty#1062 describes a new user seeing `err == nil` while holding
  a 500.
- **Silent decode failure into `SetResult`** (#204).
- **v2 read whole bodies into memory**, causing large-payload memory problems
  (#753).
- **Request bodies written twice under retries** (#743).
- **Response bodies not released on some paths** (PRs #1201 and #1208).
- **v3 reversed several defaults** in response: clients must be closed, and
  bodies are no longer auto-read.
- The fluent builder hides wire behavior in mutable request state.

**Verdict.** Avoid:

- Split success/error channels.
- Auto-buffering unbounded bodies.
- Silent decode failures.
- Mutable builders.

Adopt:

- One `error` that fully describes failure, including HTTP failure.

### 2.6 hashicorp/go-retryablehttp

**Shapes**

```go
rc := retryablehttp.NewClient() // fields: RetryMax, CheckRetry, Backoff, Logger, ...
hc := rc.StandardClient()       // a *http.Client with a retrying transport
req, _ := retryablehttp.NewRequest("POST", u, retryablehttp.ReaderFunc(open)) // rewindable body
```

**Praised**

- **`StandardClient()` makes retries a drop-in `*http.Client`.** Any library
  that accepts an `*http.Client` gets retries for free.
- Retries on connection errors, 5xx (except 501), and 429 with `Retry-After`.
- Rewindable request bodies.

**Criticized**

- **Logs to stderr by default** (issues #31 and #134).
- **URLs, including query-string API keys, end up in logs and errors** (#41).

**Verdict.** Stay out of retries in the core. Accept an `*http.Client` so
retryablehttp composes, and set `Request.GetBody` so replay works. Stay silent
by default.

### 2.7 Extra: openai-go (Stainless-generated, typical of 2025-26 Go SDKs)

**Shapes**

```go
c := openai.NewClient(option.WithHeader("X", "y"), option.WithMaxRetries(0))
r, err := c.Responses.New(ctx, params,
    option.WithJSONSet("a.b", v), option.WithResponseInto(&httpResp))
var e *openai.Error; errors.As(err, &e) // StatusCode, Request, Response, raw JSON
err = c.Post(ctx, "/unspecified", params, &result) // params: io.Reader|[]byte|any; result: []byte|*http.Response|any
```

**Notable**

- **v1 dropped `openai.F()` and `param.Field[T]` wrappers** in favor of
  `omitzero` and `param.Opt[T]`, to reduce verbosity (MIGRATION.md).
- **The generic `client.Post(ctx, path, params, &result)` lets the caller
  pick the result type.** That is the dynamic call shape in a popular Go SDK.
- **Client middleware is plain** `func(req, next) (*http.Response, error)`.
- **With a native `*http.Client`**, the SDK keeps credential-origin checks on
  the redirect path.
- **Its README warns that `Error.DumpRequest` and `Error.Error` may expose
  auth headers and URL credentials.**
- Retries are on by default (2), including 409 and 429.
- No timeout by default; context governs.

**Verdict.** Adopt:

- Caller-chosen result targets (`[]byte`, `*http.Response`, or any
  decodable).
- Origin-checked redirects.

Treat as cautionary:

- Error strings and dumps that can leak secrets.

### 2.8 Extra: dynamic invocation precedents in Go

- **grpc-go:**
  `func (cc *ClientConn) Invoke(ctx, method string, args, reply any, opts ...CallOption) error`.
  The method is addressed by string, and the caller supplies the reply
  target, as with `json.Unmarshal` (`google.golang.org/grpc@v1.83.2/call.go:29`).
  Streaming is a separate entry point, `NewStream(ctx, desc, method, ...)`
  (`stream.go:167`).
- **jhump/protoreflect `grpcdynamic.Stub`:** you resolve a
  `*desc.MethodDescriptor` once, then call it. There are separate methods per
  cardinality: `InvokeRpc`, `InvokeRpcServerStream`, `InvokeRpcClientStream`,
  `InvokeRpcBidiStream`
  (`github.com/jhump/protoreflect@v1.18.0/dynamic/grpcdynamic/stub.go:48-126`).
- **MCP Go SDK:**
  `CallTool(ctx, &CallToolParams{Name: "x", Arguments: any})` (one params
  struct, name as a field, arguments as any JSON-marshalable value). Tools are
  listed as `iter.Seq2[*Tool, error]` (`go-sdk@v1.6.1/mcp/client.go:990,1114`,
  `protocol.go:40`).

**Verdict.** Go already has an idiom for dynamic calls: address by name,
arguments as any JSON-marshalable value, reply decoded into a caller target,
and a distinct shape for streams. Each of the three packages above uses this
idiom; none invents a value system.

---

## 3. Go OpenAPI tooling

### 3.1 oapi-codegen (generated)

**Shapes**

```go
c, err := NewClientWithResponses(server, WithHTTPClient(hc), WithRequestEditorFn(auth.Intercept))
resp, err := c.GetPetWithResponse(ctx, id) // err only for transport
if resp.StatusCode() == 200 && resp.JSON200 != nil { ... } // fields per status: JSON200, JSONDefault, Body, HTTPResponse
type HttpRequestDoer interface{ Do(*http.Request) (*http.Response, error) }
type RequestEditorFn func(ctx context.Context, req *http.Request) error
```

**Praised**

- `HttpRequestDoer` accepts any doer (retryablehttp, fakes).
- `RequestEditorFn` is the simplest possible request hook.
- OpenAPI 3.1 support landed.

**Criticized**

- **Non-2xx is not an error.** Callers check `StatusCode()` and a
  per-status field.
- **`JSON200` is nil when Content-Type is missing or mismatched,** even on a
  200. Unsupported content-type branches made supported ones unreachable
  (oapi-codegen#127). Related: #1329 (unmarshal order), #1923 (`default`
  case).
- **Optional and nullable modeling has exploded into configuration.** There
  are per-field `x-go-type-skip-optional-pointer`, global
  `prefer-skip-optional-pointer`, `-with-omitzero`, and an opt-in
  `nullable.Nullable[T]` library to tell absent from null (README
  lines ~1300-1340 and ~2014-2090).
- **`securityprovider` is not spec-aware.** It is a request editor that adds
  a header to every call, whatever each operation's `security` says.
- 3.1 multi-type unions degrade to `any`.
- No Swagger 2.0.

**Dynamic support:** none.

**Verdict.** Adopt a minimal doer interface or `*http.Client`.

Avoid:

- Status-and-content-type branches that silently yield nil.
- Spec-blind credential injection.

The optional-versus-null explosion argues for untyped JSON values in and out,
where absent and `null` are naturally distinct (`map` key missing versus
`nil` value).

### 3.2 ogen (generated)

**Shapes**

```go
c, err := api.NewClient(serverURL, securitySource, opts...)
res, err := c.GetPetById(ctx, api.GetPetByIdParams{PetId: 1})
switch r := res.(type) { case *api.Pet: ...; case *api.GetPetByIdNotFound: ... } // sum type per status
type SecuritySource interface{ BearerAuth(ctx context.Context, op api.OperationName) (api.BearerAuth, error) }
// return ogenerrors.ErrSkipClientSecurity to skip a scheme for an operation
type Client[E any] interface { Next(ctx) (E, error); All(ctx) iter.Seq2[E, error]; State() (sse.State, error); Close() error } // SSE
```

**Praised**

- No pointers: generic `OptT`, `NilT`, and `OptNilT` wrappers keep absent
  and null distinct.
- Response sum types per status.
- **The client asks a `SecuritySource` for credentials per scheme and per
  operation, lazily.** This is spec-aware, and the ogen examples cover AND/OR
  scope combinations.
- **"Convenient errors"** turns a uniform `default` response into a Go
  error type.
- **An SSE client with reconnection,** `Last-Event-ID`, retry delay, and
  max-event-size bounds, exposing `Next`/`All`/`State`/`Close`.

**Criticized.** Generated wrapper types are verbose. There is no Swagger
2.0.

**Dynamic support:** none.

**Verdict.** Adopt:

- Credential lookup by scheme name with context.
- A bounded stream handle (`All` returning `Seq2`, plus `Close` and a state
  or error accessor).
- A clear split between declared and undeclared responses.

### 3.3 go-swagger client runtime (go-openapi/runtime)

**Shapes**

```go
type ClientOperation struct {
    ID, Method, PathPattern string
    ProducesMediaTypes, ConsumesMediaTypes, Schemes []string
    AuthInfo ClientAuthInfoWriter; Params ClientRequestWriter; Reader ClientResponseReader
    Context context.Context // deprecated in favor of SubmitContext
    Client *http.Client
}
rt.Submit(op) (any, error)
client.APIKeyAuth(name, in, value); client.BearerToken(t); client.Compose(a, b)
```

**Notable**

- **The closest thing to a dynamic runtime in Go.** The operation is data,
  submitted to a transport. But `Params` and `Reader` are interfaces that
  generated code implements per operation, so hand-use is laborious.
- Auth helpers mirror OpenAPI scheme kinds (`APIKeyAuth(name, in, value)`).
- `APIError{OperationName, Response, Code}`.
- **Context in the struct is being deprecated.**

**Criticized**

- Generated per-status response types hide the numeric status, so callers
  cannot easily classify 4xx versus 5xx (go-swagger#2706).
- Heavy `strfmt.Registry` threading.
- Swagger 2.0 only.

**Verdict.** Adopt operation-as-data. Avoid:

- Per-status types that hide status.
- Registries threaded through every call.
- Context in structs.

### 3.4 kin-openapi (parse, route, validate)

**Shapes (v0.149.0)**

```go
loader := &openapi3.Loader{Context: ctx, IsExternalRefsAllowed: false}
doc, err := loader.LoadFromFile("api.yaml"); err = doc.Validate(ctx)
router, _ := gorillamux.NewRouter(doc); route, pathParams, _ := router.FindRoute(req)
err = openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{Request: req, PathParams: pathParams, Route: route})
openapi3filter.RegisterBodyDecoder("application/xml", dec) // package-global
```

**Useful facts**

- Supports 3.0 and 3.1, 3.2 partially (README lines 10-11), and 2.0 via
  `openapi2`/`openapi2conv`.
- `openapi3filter` has a parameter decoder and request/response body
  codecs, and validates by style/explode. It is server-oriented: it
  validates an `*http.Request` you already built. It does not build requests
  from values.
- **The Loader documents the danger of untrusted documents.** A custom
  `ReadFromURIFunc` bypasses `IsExternalRefsAllowed`, and on untrusted input
  that enables local file reads and SSRF through `$ref`
  (`openapi3/loader.go:39-49`).

**Criticized, and evidence from its own README**

- **"CHANGELOG: Sub-v1 breaking API changes" is a long list** (README
  lines 329-440+). Map types became structs (`Paths`, `Responses`,
  `Callback`), `Schema.Type` became `*Type`, `ExclusiveMin` became a union,
  and `Components` became a pointer. This is the cost of publishing a
  mutable document model as the public API.
- **Package-global mutable state:**
  - `RegisterBodyDecoder` and `RegisterBodyEncoder`
    (`openapi3filter/req_resp_encoder.go:22-37`).
  - `DefineStringFormat`.
  - A package-level `IncludeOrigin` global that the Loader doc says is not
    safe for concurrent use (`loader.go:34-37`).
  - Validation options passed through context (`WithValidationOptions(ctx)`).
- **A default that went the other way:** `AuthenticationFunc` no longer
  defaults to a no-op (v0.144.0), so users must opt into auth handling.

**Dynamic support:** none for invocation. Its route matching and
parameter-encoding logic are the most reusable pieces.

**Verdict.**

- Use a parser internally if helpful, but do not expose its types.
- Avoid global registries.
- Default to no external `$ref` fetching.

### 3.5 libopenapi (pb33f), for completeness

- `libopenapi.NewDocument(bytes)`, then `BuildV3Model()` / `BuildV2Model()`,
  returning the model and errors.
- Separate high-level and low-level models. Covers Swagger 2.0 and OpenAPI
  3.0, 3.1, and 3.2. Parse, diff, and validate only; no invocation.
- **The high/low split is how a model library limits churn in its public
  surface.** kin-openapi lacks it.

**Dynamic invocation across Go tooling**

| Tool | Build a request from values at runtime | Swagger 2.0 | OAS 3.1 / 3.2 |
|---|---|---|---|
| oapi-codegen | no (generated methods) | no | 3.1 yes |
| ogen | no (generated methods) | no | OAS 3 (3.1/3.2 coverage not documented in README) |
| go-openapi/runtime | partly (`Submit`, but needs generated writers/readers) | yes (only) | no |
| kin-openapi | no (validates requests you already built) | via conversion | 3.1 yes, 3.2 partial |
| libopenapi | no | yes | yes |

---

## 4. Dynamic OpenAPI clients in other languages

### 4.1 openapi-fetch (TypeScript)

**Shapes**

```ts
const client = createClient<paths>({ baseUrl, headers, fetch });
const { data, error, response } = await client.GET("/pets/{id}", {
  params: { path: { id: 1 }, query: { expand: ["owner"] }, header: {}, cookie: {} },
  body: {...}, parseAs: "json" | "text" | "blob" | "arrayBuffer" | "stream",
});
client.use({ onRequest, onResponse, onError });
```

**How it handles each concern**

- **Selecting operations:** by method plus literal path template. There is
  no operationId access.
- **Parameters:** grouped by location, so there are no name collisions. The
  body is separate.
- **Servers:** a single `baseUrl`.
- **Credentials:** via headers or middleware; not spec-aware.
- **Errors:** it does not throw on HTTP errors. `data` is set for 2xx and
  `error` for 4xx/5xx. Network failures still throw.
- **Streaming:** `parseAs: "stream"`.
- **Describing operations for tooling:** none at runtime (types only).

**Praised**

- About 6 KB.
- Types come from the document, so there is no runtime codegen.
- Serializers follow OpenAPI style/explode defaults (form/explode for
  arrays, deepObject for objects).

**Criticized**

- **Empty bodies leave both `data` and `error` undefined,** so `if (error)`
  misses a real failure (openapi-typescript #2291, #2530). An earlier `{}`
  workaround broke the types (#1868, #2234).
- A path-literal-only API offers no stable handle when operationIds exist.

**Verdict.** Adopt:

- Location-grouped parameters.
- Separate body.
- Status-based classification.

Avoid:

- Inferring outcome from body presence.
- Offering no operationId addressing.

### 4.2 openapi-client-axios (TypeScript/JavaScript)

**Shapes**

```js
const api = new OpenAPIClientAxios({ definition, withServer: 0 | url | serverObj });
const client = await api.init();                  // or initSync()
await client.getPetById(1);                       // implicit first required param
await client.getPetById({ petId: 1 });            // by name
await client.searchPets([{ name: "q", value: "x", in: "query" }]); // explicit location
await client.updatePet(1, body, axiosConfig);     // (params?, data?, config?)
await client.paths["/pets/{petId}"].put(1, body); // method + path
api.getOperation("getPetById"); api.getOperations();
```

**How it handles each concern**

- **Selecting operations:** operationId methods, plus a `paths[...]`
  dictionary.
- **Parameters:** flat object by name. An array form disambiguates location,
  and a positional scalar is accepted for the first required parameter.
- **Servers:** `withServer` by index, URL, or object.
- **Credentials:** axios config (headers); not spec-aware.
- **Errors:** axios throws on non-2xx (`validateStatus`).
- **Describing operations:** `getOperation(id)` / `getOperations()` expose
  the operation objects.

**Verdict.** Adopt:

- operationId plus method/path addressing.
- Operation introspection.
- Server selection by index or URL.

Avoid:

- Positional "first required param" magic.
- Location ambiguity in the default flat form.

### 4.3 swagger-client (JavaScript, SmartBear)

**Shapes**

```js
const client = await SwaggerClient({ url, authorizations: { petstore_auth: { token: {...} }, api_key: "k" } });
await client.apis.pet.getPetById({ petId: 1 }, { requestBody, server, serverVariables });
await SwaggerClient.execute({ spec, operationId | (pathName + method), parameters: { "query.id": 1 },
  requestBody, securities: { authorized: {...} }, server, serverVariables, requestContentType,
  responseContentType, requestInterceptor, responseInterceptor, signal, userFetch });
const req = SwaggerClient.buildRequest({...}); // build without sending
```

**How it handles each concern**

- **Selecting operations:** by tag plus operationId, or `execute` with an
  operationId or pathName+method.
- **Parameters:** flat by name, with `in.name` qualification (for example
  `query.id`) on collision.
- **Body:** a separate `requestBody`, with content-type choice.
- **Servers:** per-call `server` and `serverVariables`.
- **Credentials:** keyed by security scheme name.
- **Errors:** rejects on non-2xx with `statusCode` and `response`. The
  response has `ok`, `status`, `headers`, `text`, and `body` parsed by
  content type.
- **Build versus send:** `buildRequest` separates building from sending,
  which is good for tooling and tests.

**Criticized.** Tag-namespaced operations inherit tag instability (restish
makes flat layout the default for this reason). Interceptors are the only
hook.

**Verdict.** Adopt:

- Credentials keyed by scheme name.
- Per-call server and variables.
- Explicit request and response content-type choice.
- A way to build a request without sending it.

Avoid:

- Tag-based namespaces.

### 4.4 ReadMe `api` (JavaScript), a cautionary tale

**Shapes**

```js
const sdk = require('api')('https://.../openapi.json');
sdk.auth('token');                 // or sdk.auth('user', 'pass'): scheme inferred
sdk.server('https://{region}.api.example.com/{v}', { region: 'eu', v: 'v14' });
const { data, status, headers, res } = await sdk.updatePet(body, { id: 1234 }); // (body?, metadata?)
// throws FetchError for 400-599 (destructurable: status, data)
```

**Notable**

- **Body versus metadata is guessed.** A single object is treated as the
  body if it overlaps the documented body schema by more than about 25%
  (docs, "Parameters and Payloads").
- **`auth()` infers which scheme positional credentials belong to.**
- **v5 dropped HTTP-method accessors** (`sdk.get('/pets')`) in favor of
  operationIds, and generates IDs when a document lacks them.
- The response object and `FetchError` share one destructurable shape
  (`data`, `status`, `headers`, `res`).
- A custom timeout surfaces as `AbortError`, not `FetchError`: two error
  families.

**Verdict.** Adopt:

- One shape for response data on the success and error paths.
- Server URL templates with variables.

Avoid:

- Heuristic argument placement.
- Heuristic credential mapping.

### 4.5 Python: openapi-core, bravado, openapi3

- **openapi-core** (3.0, 3.1, 3.2) is a validator and unmarshaller only; it
  never sends requests.
  - Shape: `OpenAPI.from_file_path(p)`, then `unmarshal_request(req)` /
    `unmarshal_response(req, resp)`, returning `result.parameters.{path,query,headers,cookies}`,
    `result.body`, `result.security`, and `result.errors`.
  - Protocol adapters (Requests, Werkzeug, and so on) decouple it from any
    HTTP library.
  - Lesson: validation is a separable concern with its own result type.
- **bravado** (Yelp, Swagger 2.0 only).
  - Shape: `SwaggerClient.from_url(u, config)`, then
    `client.pet.getPetById(petId=42)` returns an `HttpFuture`; `.response()`
    returns `BravadoResponse(result, metadata)`.
  - Metadata includes status, headers, and elapsed time. `.result()` was
    deprecated in favor of `.response()` so that metadata travels with the
    result.
  - Raises an `HTTPError` subclass per status for any code 300 and above,
    carrying `swagger_result`.
  - Config has `use_models` (dynamic model classes versus plain dicts) and
    `validate_responses`.
  - Lessons: keep result and metadata together; do not class 3xx as errors
    when redirects are followed; plain dict results are a first-class mode.
- **openapi3** (Dorthu).
  - Shape: `api = OpenAPI(spec)`, then `api.authenticate('schemeName', token)`
    and `api.call_getRegions()` or
    `api.call_createLinode(parameters={...}, data={...})`.
  - Credentials are keyed by scheme name. Parameters and body are separate
    arguments.

### 4.6 restish (Go CLI, v2 design docs)

Restish is the most complete public treatment of dynamic OpenAPI invocation,
and it is written in Go. Its design records under `docs/design/` are directly
applicable.

**Operation selection and naming** (007)

- The command name comes from `x-cli-name`, then `operationId`, then a
  deterministic method+path fallback (`GET /users/{id}` becomes
  `get-users-id`). The fallback is collision-aware, and collisions are
  diagnosed, not overwritten.
- The layout is flat by default, because tag taxonomies are unstable.
- An operation that cannot be generated safely yields a diagnostic, never a
  silent drop.

**Parameters** (007, 034)

- Path-item and operation parameters merge on the key `(in, name)`.
  Same-named parameters in different locations stay distinct.
- A missing path parameter fails. The literal `{petId}` must never reach the
  URL.
- Header parameters named `Accept`, `Content-Type`, and `Authorization` are
  ignored, per the OpenAPI spec.
- Full style/explode serialization (form, spaceDelimited, pipeDelimited,
  deepObject, allowReserved, simple, label, matrix) plus JSON `content`
  parameters. The rules are shared by the CLI and the MCP plugin.

**Bodies** (007)

- Bodies are schema-agnostic. The schema drives help and example generation
  but never rewrites values.
- Validation is opt-in, never coerces, and treats the server as the source of
  truth.
- JSON is preferred. Form, multipart (per-part content types), and raw binary
  are first-class.

**Servers** (007, 034)

- Precedence is operation, then path, then document, then configured base
  URL.
- Variables resolve with local config first, then profile, then the
  document default.
- Enum values are advisory. An unknown variable is an error (usually a typo).
- There is never a Cartesian expansion.
- An absolute server URL on another origin is not used unless the operator
  allowlists it. This prevents spec-driven credential exfiltration, and the
  same origin check covers redirects, `$ref` loading, and pagination.

**Security** (033, 034)

- OpenAPI security is normalized into credential alternatives: an OR-list of
  AND-sets keyed by scheme name, with scopes as "needs".
- `security: []` means no credentials are sent at all.
- An empty `{}` requirement means anonymous access is allowed as one
  alternative.
- The first satisfied alternative wins. If none is satisfied and anonymous
  access is not allowed, the request fails before sending, with a diagnostic
  that names what is missing without printing secrets.

**Streaming** (012)

- SSE (`text/event-stream`) and NDJSON/JSONL are classified after headers
  arrive.
- The timeout bounds only the wait for headers; stream lifetime is bounded
  by cancellation and item and size caps.
- The error model distinguishes EOF, cancellation, malformed event, and
  framing error.

**Loading** (006, 034)

- Remote specs may not read local or `file://` refs.
- Cross-origin refs require an explicit allowance.
- Size limits and private-host safeguards apply.

**Operation model** (034)

- A neutral operation model holds no parser-library objects. It carries:
  - name and aliases, summary, description, tags, and deprecation
  - method and resolved path
  - merged parameters
  - request body media alternatives and schemas
  - response media types, schemas, and header names
  - examples
  - security metadata
  - source location

**Pagination** (011) is a separate layer over hypermedia (Link headers and
similar) and configuration, because OpenAPI has no pagination model.

**Verdict.** Restish is the best single source of rules. Adopt its
`(in, name)` parameter model, its servers-and-origin rules, its security
evaluation, its streaming timeout split, its neutral operation descriptor,
and its "diagnose, never silently drop" stance. The CLI-specific parts
(profiles, config files, prompts) do not transfer.

### 4.7 Cross-language comparison

| Concern | openapi-fetch | openapi-client-axios | swagger-client | ReadMe api | bravado | restish |
|---|---|---|---|---|---|---|
| Select op | method + path | operationId, `paths[...]` | tag.opId, opId, path+method | opId (generated if absent) | tag.opId | x-cli-name, opId, method+path fallback |
| Params | grouped by location | flat, array for location | flat, `in.name` on clash | guessed (body vs metadata) | kwargs | `(in, name)` merge |
| Body | `body` | 2nd arg | `requestBody` | guessed | named param (2.0) | separate, schema-agnostic |
| Servers | `baseUrl` | `withServer` | `server` + vars per call | `server(url, vars)` | spec | precedence + origin allowlist |
| Credentials | manual | manual | by scheme name | inferred | per client | by scheme name, OR/AND per op |
| HTTP errors | value (`error`) | throws | rejects | throws `FetchError` | raises per status | nonzero exit + diagnostics |
| Streaming | `parseAs: stream` | axios stream | none built in | none | none | SSE/NDJSON path |
| Describe ops | types only | `getOperation(s)` | resolved spec | generated types | spec models | neutral model, rich help |

---

## 5. Synthesized design principles

Each principle cites the evidence above by library and section.

1. **Build one long-lived, concurrency-safe client from a loaded document,
   with no global state.** Parse and resolve the document once. After that,
   every call is cheap and safe from many goroutines.
   - Evidence: `sql.Open` and `*DB` (1.2); `http.Client` reuse (1.1);
     stripe's move off `stripe.Key` (2.3); grpcdynamic resolving descriptors
     once (2.8).
   - Anti-evidence: kin-openapi and stripe globals (3.4, 2.3).

2. **`ctx` is the first parameter of every call that does I/O, exactly once,
   and never stored in a struct.**
   - Evidence: Google style guide (1.6); go-openapi deprecating
     `ClientOperation.Context` (3.3); kin-openapi `Loader.Context` (3.4); the
     stripe v85 double-`ctx` wart (2.3).
   - For streams, the `ctx` passed at call time bounds the whole stream (the
     google iterator guideline, 2.2).

3. **Transport is the caller's `*http.Client` (or a one-method doer), used
   as given and never mutated.** RoundTripper is the only middleware. There
   is no private pipeline.
   - Evidence: go-github #3028 (2.4); retryablehttp `StandardClient` (2.6);
     oapi-codegen `HttpRequestDoer` (3.1); the AWS middleware stack as the
     heavy counterexample (2.1); openai-go plain middleware (2.7); RoundTripper
     contract (1.1).
   - Timeouts come from `ctx`. Do not rely on `Client.Timeout`, which breaks
     streams (1.1, 4.6).

4. **Construction options are a struct whose zero value means defaults, and
   conflicting options are an error, never a silent precedence rule.** Keep
   per-call knobs to the minimum. If you have them, prefer the AWS-style
   `func(*Options)`, or nothing.
   - Evidence: slog `HandlerOptions` (1.3); Google style guide (1.6); google
     `WithHTTPClient` overriding credentials (2.2); json/v2 variadic options
     for rare knobs (1.5).

5. **Address operations the way the document does.** Use operationId when
   present, and always accept method plus path template. Use no tag
   namespaces and no heuristic or synthesized names without a deterministic,
   documented rule. An unknown operation is an error at lookup, not at send.
   - Evidence: openapi-client-axios (4.2); swagger-client `execute` (4.3);
     openapi-fetch path-only limits (4.1); ReadMe dropping method accessors
     and generating IDs (4.4); restish fallback naming and flat layout (4.6).
   - operationId is optional in OpenAPI, which is why the method+path path
     is mandatory.

6. **Parameters are identified by `(location, name)`. The request body is
   its own argument. Nothing is inferred.**
   - Evidence: openapi-fetch grouped params (4.1); swagger-client `in.name`
     (4.3); openapi-client-axios array form (4.2); restish merge key (4.6);
     the ReadMe 25% heuristic as the anti-pattern (4.4); Python openapi3
     `parameters=` / `data=` (4.5).
   - Follow the spec: ignore `Accept`, `Content-Type`, and `Authorization`
     header parameters (restish, 4.6).

7. **Values in are plain Go values. Values out are decoded into a target
   the caller chooses.**
   - Inputs: accept anything JSON-marshalable (`map[string]any`, structs,
     `json.RawMessage`), plus `io.Reader` and `[]byte` for raw bodies.
   - Outputs: expose status, headers, raw bytes, and `Decode(any) error`.
     Decode into `any` without float64 loss (`json.Number`).
   - Keep absent and `null` distinct (missing key versus `nil`), with no
     pointer or wrapper types.
   - Evidence: `json.Unmarshal`, `Scan`, and grpc `Invoke` reply targets (1.2,
     1.5, 2.8); openai-go `client.Post(..., &result)` (2.7); AWS #205 and
     openai-go v1 wrapper removal (2.1, 2.7); oapi-codegen's optional/nullable
     configuration explosion (3.1); MCP `Arguments any` (2.8).

8. **Classify outcomes by HTTP status and the document, never by body
   presence.**
   - A 2xx returns a result. A non-2xx returns one exported error type,
     reachable with `errors.As`/`errors.AsType`. It carries the operation,
     status, headers, raw body, `Decode`, and whether the status was declared
     by the document.
   - Transport and context errors pass through (wrapped) so `errors.Is`
     works.
   - Evidence: stripe, AWS, go-github, and openai-go (2.1-2.7); ReadMe
     `FetchError` and swagger-client rejection (4.3, 4.4).
   - Anti-evidence: resty #1062 (2.5); oapi-codegen `JSON200 == nil` (3.1);
     openapi-fetch empty-body ambiguity (4.1); go-swagger status hiding (3.3);
     bravado treating 3xx as errors (4.5).
   - net/http's "non-2xx is not an error" is right only at the protocol
     layer (1.1).

9. **Response metadata is always reachable, on both the success and error
   paths, without a triple return.** Put `Status`, `Header`, and raw access
   on the result value and on the error value.
   - Evidence: stripe `LastResponse` and #2087 (2.3); openai-go
     `WithResponseInto` (2.7); AWS `ResultMetadata` (2.1); bravado
     `.response()` superseding `.result()` (4.5); go-github's `_`-heavy
     triple return (2.4).

10. **Streams are explicit, bounded, and closeable.**
    - Binary responses are an `io.ReadCloser`.
    - Sequential media types (SSE, JSONL/NDJSON, json-seq; OpenAPI 3.2
      `itemSchema`) come back as a handle with `All() iter.Seq2[T, error]`,
      `Close`, and an error or state accessor.
    - Never buffer an unbounded body by default. Closing drains or aborts.
    - Evidence: ogen SSE `Client[E]` (3.2); restish streaming (4.6); resty
      v2 memory issues and v3 no-auto-read (2.5); AWS must-close bodies (2.1);
      the `bodyclose` linter (1.1).

11. **Sequences use a handle with an `All()` method returning
    `iter.Seq2[T, error]`, not a bare `Seq2` and not a
    `Next`/`Err`/`Done` protocol.** Pagination stays out of the core,
    because OpenAPI does not model it.
    - Evidence: stripe v82 to v85 (2.3); gax-go `All` and `RangeAdapter`
      (2.2); MCP go-sdk `Tools` (2.8); go-github `ListIter` (2.4); `sql.Rows`
      linters (1.2); `iterator.Done` pain (2.2); restish's separate pagination
      layer (4.6).

12. **Credentials are supplied per security scheme name from the document
    and evaluated per operation with OpenAPI OR/AND semantics.**
    - `security: []` sends nothing. An empty `{}` alternative allows
      anonymous access.
    - An unsatisfied requirement fails before sending, with a diagnostic
      that names schemes, never secrets.
    - Accept static values and a context-aware provider function for dynamic
      tokens.
    - Evidence: restish 033/034 (4.6); swagger-client `authorizations`
      (4.3); Python openapi3 `authenticate(scheme, ...)` (4.5); ogen
      `SecuritySource` and `ErrSkipClientSecurity` (3.2).
    - Anti-evidence: ReadMe's inferred `auth()` (4.4); oapi-codegen's
      spec-blind request editor (3.1).

13. **Credentials go only to origins the caller trusts.**
    - A server chosen by the document, a cross-origin redirect, or a
      spec-supplied absolute URL never receives credentials unless the
      caller allowed that origin.
    - Attach credentials in exactly one place, above the RoundTripper, after
      the origin decision.
    - Redact credential-bearing headers and query parameters from error
      strings and logs.
    - Evidence: go-github PR #4564 (2.4); openai-go origin checks and dump
      warning (2.7); restish origin rules (4.6); net/http header stripping
      versus RoundTripper re-application and `url.Error` behavior (1.1);
      retryablehttp #41 (2.6); google's untrusted-credential warning (2.2).

14. **Servers default from the document and are overridable by the caller.**
    - Precedence is operation, then path, then document. Variables take
      caller values, then document defaults.
    - Unknown variables are errors, and enums are advisory.
    - Relative server URLs resolve against the document's location. Swagger
      2.0 `host`/`basePath`/`schemes` are normalized to the same model.
    - Never expand variables combinatorially.
    - Evidence: restish (4.6); swagger-client `server` + `serverVariables`
      (4.3); openapi-client-axios `withServer` (4.2); ReadMe
      `server(url, vars)` (4.4).
    - Contrast: oapi-codegen requires a server string and ignores the
      document (3.1).

15. **Structural request correctness is enforced before sending. Schema
    validation of values is opt-in and never coerces.**
    - Always check: missing required or path parameters, unknown parameter
      names (typo protection), unresolvable server variables, unsupported
      style.
    - Evidence: restish missing-path-param rule and opt-in validation (4.6);
      openapi-core and kin-openapi `openapi3filter` treating validation as a
      separate result-bearing concern (4.5, 3.4).

16. **Describe operations with a small, read-only, stable descriptor, not
    the parser model.**
    - Fields: id, method, path, summary and description, deprecated,
      parameters (in, name, required, style, schema as raw JSON), request
      body media types and schemas, responses (status to media types and
      schemas), security alternatives, and servers.
    - Provide iteration over all operations.
    - Evidence: openapi-client-axios `getOperation(s)` (4.2); restish neutral
      model (4.6); kin-openapi breaking-change churn (3.4); libopenapi's
      high/low split (3.5); MCP `Tool` listing (2.8).

17. **One API for all four OpenAPI versions.** Version differences stay
    internal: Swagger 2.0 body and formData become the request body,
    `securityDefinitions` become schemes, and `host`/`basePath` become
    servers.
    - Evidence: bravado is 2.0-only, while openapi-core and oapi-codegen are
      3.x-only (4.5, 3.1).
    - kin-openapi's `openapi2conv` and libopenapi show that normalization is
      tractable (3.4, 3.5).

18. **Document loading is safe by default.** Do not fetch external `$ref`s
    unless the caller allows it. Remote documents never read local files.
    Apply size limits.
    - Evidence: kin-openapi Loader security note (3.4); restish loading
      rules (4.6).

19. **Leave retries, caching, and rate limiting to composition, but make
    them possible.** Set `Request.GetBody` so any retrying or caching
    RoundTripper can replay bodies. Do not retry non-idempotent operations
    implicitly, because a generic OpenAPI client has no idempotency-key
    convention.
    - Evidence: retryablehttp `StandardClient` (2.6); go-github caching and
      rate-limit transports (2.4); resty #743 double-writes (2.5); stripe's
      idempotency-key precondition for safe default retries (2.3).

20. **Provide escape hatches for what the document does not say, and keep
    them few.** Extra headers and query parameters, a raw request body, and
    the raw `*http.Response` or bytes.
    - Evidence: stripe `AddExtra`, `RawJSON`, and `rawrequest` (2.3);
      openai-go `WithJSONSet`, `WithQuerySet`, and `client.Post` (2.7);
      swagger-client `buildRequest` (4.3).

21. **Be silent by default.** No logging unless the caller supplies a
    `*slog.Logger`. Credential types redact themselves in `String` and
    `LogValue`.
    - Evidence: retryablehttp #31 and #134 (2.6); stripe stderr logging (2.3);
      slog `LogValuer` (1.3).

### Open questions the evidence does not settle

- **Operation handle versus string key per call.** grpcdynamic resolves a
  descriptor once, while grpc-go `Invoke`, MCP `CallTool`, and openai-go
  `Post` take a string each call. A handle moves "unknown operation" errors
  to lookup time and gives the descriptor a natural home. A string key is
  one fewer concept.
- **How parameters are passed.** One located-args struct (`Path`, `Query`,
  `Header`, `Cookie` maps plus `Body`) is unambiguous but verbose. A flat
  name map is shorter but collides on duplicate names. swagger-client's
  answer is flat with `in.name` qualification only on collision.
- **Returning undeclared-but-successful responses** (for example a 2xx
  status the document does not list): as a result, or as an error? The
  evidence leans toward returning them as a result with a "declared" flag.
- **One-step versus two-step construction.** `Load(doc)` then
  `NewClient(doc, opts)`, versus a single `New(docBytes, opts)`. Separating
  them lets one parsed document back several clients (different
  credentials); sql and stripe show single-step ergonomics.
- **Streaming entry point.** A separate method (grpc `NewStream`,
  grpcdynamic per-cardinality methods) or classification from the
  response's media type after headers arrive (restish)?

### Strawman shape, for pressure-testing the principles only

```go
doc, err := openapi.Parse(data, &openapi.ParseOptions{BaseURL: docURL}) // no external refs by default
c, err := openapi.NewClient(doc, &openapi.Options{                       // zero value = defaults
    HTTPClient:  hc,                                                     // used as given
    Server:      "https://eu.api.example.com/v2",                        // optional override
    Credentials: map[string]openapi.Credential{"api_key": openapi.APIKey(k), "oauth": openapi.BearerFunc(tok)},
})
op, err := c.Operation("getPetById")        // or c.Operation("GET /pets/{petId}")
res, err := op.Call(ctx, openapi.Args{Path: map[string]any{"petId": 42}, Body: nil})
var pet Pet
err = res.Decode(&pet)                      // res.Status, res.Header, res.Bytes()
var he *openapi.ResponseError
if errors.As(err, &he) { he.Status; he.Declared; he.Decode(&problem) }
s, err := op.Stream(ctx, args); defer s.Close()
for ev, err := range s.All() { ... }
for op := range c.Operations() { op.Method, op.Path, op.Parameters }
```

---

## Sources

**Go standard library and release notes**

- Local source: `$GOROOT/src/net/http/client.go`, `net/url/url.go`,
  `database/sql/sql.go`, `log/slog/{handler,json_handler,logger,value}.go`,
  `iter/iter.go`, `encoding/json/{stream,decode}.go`,
  `encoding/json/v2/{options,arshal}.go`
- [Go 1.26 release notes](https://go.dev/doc/go1.26) (`new(expr)`,
  `errors.AsType`, `slog.NewMultiHandler`)
- [Go 1.27 release notes](https://go.dev/doc/go1.27) (json/v2 GA, HTTP/1
  body auto-drain)
- [Google Go style guide: best practices](https://google.github.io/styleguide/go/best-practices#function-argument-lists)
- [golangci-lint linters](https://golangci-lint.run/docs/linters/)
  (bodyclose, rowserrcheck, sqlclosecheck)
- [Don't use Go's default HTTP client](https://medium.com/@nate510/don-t-use-go-s-default-http-client-4804cb19f779)
- [golang/go#61637](https://github.com/golang/go/issues/61637)
- Iterator errors:
  [Sinclair Target](https://sinclairtarget.com/blog/2025/07/error-handling-with-iterators-in-go/),
  [Boldly Go](https://boldlygo.tech/archive/2025-11-13-handling-errors-during-iteration-with-range-over-func/)

**Go SDKs**

- AWS SDK for Go v2 developer guide:
  [using](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/using.html),
  [middleware](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/middleware.html),
  [errors](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/handle-errors.html);
  [aws/aws-sdk-go-v2#205](https://github.com/aws/aws-sdk-go-v2/issues/205)
- google-cloud-go:
  [Iterator Guidelines](https://github.com/googleapis/google-cloud-go/wiki/Iterator-Guidelines),
  [gax-go iterator](https://pkg.go.dev/github.com/googleapis/gax-go/v2/iterator),
  [option package](https://pkg.go.dev/google.golang.org/api/option),
  [#7711](https://github.com/googleapis/google-cloud-go/issues/7711)
- stripe-go:
  [README](https://github.com/stripe/stripe-go/blob/master/README.md),
  [migration guide](https://github.com/stripe/stripe-go/wiki/Migration-guide-for-Stripe-Client),
  [v85.0.0 notes](https://newreleases.io/project/github/stripe/stripe-go/release/v85.0.0),
  [#2087](https://github.com/stripe/stripe-go/issues/2087)
- go-github:
  [README](https://github.com/google/go-github),
  [PR #4564](https://github.com/google/go-github/pull/4564),
  [#3028](https://github.com/google/go-github/issues/3028)
- resty:
  [v3 features](https://resty.dev/docs/new-features-and-enhancements/),
  [#1062](https://github.com/go-resty/resty/issues/1062),
  [#753](https://github.com/go-resty/resty/issues/753),
  [#743](https://github.com/go-resty/resty/issues/743),
  [#204](https://github.com/go-resty/resty/issues/204),
  [PR #1201](https://github.com/go-resty/resty/pull/1201),
  [PR #1208](https://github.com/go-resty/resty/pull/1208)
- go-retryablehttp:
  [pkg.go.dev](https://pkg.go.dev/github.com/hashicorp/go-retryablehttp),
  [#31](https://github.com/hashicorp/go-retryablehttp/issues/31),
  [#41](https://github.com/hashicorp/go-retryablehttp/issues/41),
  [#134](https://github.com/hashicorp/go-retryablehttp/issues/134)
- openai-go:
  [README](https://github.com/openai/openai-go),
  [MIGRATION.md](https://github.com/openai/openai-go/blob/main/MIGRATION.md)
- Dynamic call precedents (local module cache):
  `google.golang.org/grpc@v1.83.2/{call,stream}.go`,
  `github.com/jhump/protoreflect@v1.18.0/dynamic/grpcdynamic/stub.go`,
  `github.com/modelcontextprotocol/go-sdk@v1.6.1/mcp/{client,protocol}.go`

**Go OpenAPI tooling**

- oapi-codegen:
  [README](https://github.com/oapi-codegen/oapi-codegen),
  [#127](https://github.com/deepmap/oapi-codegen/issues/127),
  [#1329](https://github.com/oapi-codegen/oapi-codegen/issues/1329),
  [#1923](https://github.com/oapi-codegen/oapi-codegen/issues/1923)
- ogen:
  [README](https://github.com/ogen-go/ogen),
  [intro](https://ogen.dev/docs/intro),
  [convenient errors](https://ogen.dev/docs/concepts/convenient_errors),
  [ogenerrors](https://pkg.go.dev/github.com/ogen-go/ogen/ogenerrors),
  [AND/OR scopes example](https://pkg.go.dev/github.com/ogen-go/ogen/examples/ex_oauth2_scopes_and_or)
- go-openapi/runtime:
  [client_operation.go](https://github.com/go-openapi/runtime/blob/master/client_operation.go),
  [client package](https://pkg.go.dev/github.com/go-openapi/runtime/client);
  [go-swagger#2706](https://github.com/go-swagger/go-swagger/issues/2706)
- kin-openapi v0.149.0 (local): `README.md`, `openapi3/loader.go`,
  `openapi3filter/{options,validate_request_input,req_resp_encoder}.go`
- [libopenapi](https://pb33f.io/libopenapi/)

**Other languages**

- openapi-fetch:
  [overview](https://openapi-ts.dev/openapi-fetch/),
  [API](https://openapi-ts.dev/openapi-fetch/api);
  openapi-typescript
  [#2291](https://github.com/openapi-ts/openapi-typescript/issues/2291),
  [#2530](https://github.com/openapi-ts/openapi-typescript/issues/2530),
  [#1868](https://github.com/openapi-ts/openapi-typescript/issues/1868),
  [#2234](https://github.com/openapi-ts/openapi-typescript/issues/2234)
- openapi-client-axios:
  [intro](https://openapistack.co/docs/openapi-client-axios/intro/),
  [usage](https://openapistack.co/docs/openapi-client-axios/usage/)
- swagger-client:
  [tags interface](https://github.com/swagger-api/swagger-js/blob/master/docs/usage/tags-interface.md),
  [execute/buildRequest](https://github.com/swagger-api/swagger-js/blob/master/docs/usage/http-client-for-oas-operations.md),
  [http client](https://github.com/swagger-api/swagger-js/blob/master/docs/usage/http-client.md)
- ReadMe `api`:
  [authentication](https://api.readme.dev/docs/authentication),
  [parameters and payloads](https://api.readme.dev/docs/parameters-and-payloads),
  [server configurations](https://api.readme.dev/docs/server-configurations),
  [making requests](https://api.readme.dev/docs/making-requests),
  [upgrading from v4](https://api.readme.dev/docs/upgrading-from-v4)
- Python:
  [openapi-core](https://openapi-core.readthedocs.io/en/latest/),
  [bravado requests and responses](https://bravado.readthedocs.io/en/stable/requests_and_responses.html),
  [Dorthu/openapi3](https://github.com/Dorthu/openapi3)
- restish design records:
  [docs/design](https://github.com/danielgtaylor/restish/tree/main/docs/design)
  (006, 007, 011, 012, 033, 034)

**Specification**

- [OpenAPI 3.2 sequential media types](https://learn.openapis.org/specification/media-types.html)
- [OpenAPI Specification releases](https://github.com/OAI/OpenAPI-Specification/releases)
