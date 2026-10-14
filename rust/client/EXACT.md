# Exact values and Serde

`exact::Value` is one owning JSON node handle. `Value::parse(bytes, limits)` is
JSON-only; `Document::parse` also accepts the [loading profile](LOADING.md).
A child handle keeps its source after its parent is dropped. `raw()` preserves
its token/subtree spelling, `get()` distinguishes absence from null, and traversal
preserves authored member order. JSON syntax follows [RFC8259 §§4–9](https://www.rfc-editor.org/rfc/rfc8259#section-4).
Exact retention and checked conversion are client policies, not a promise that
all JSON consumers preserve precision. See matrix rows V01–V03 in
[CAPABILITIES.md](CAPABILITIES.md).

## Choose input once

For ordinary calls, borrow a Serde value into `RequestBuilder::parameter`, `json`
or `form`. Setters check and serialize immediately; caller data can then be
dropped. Preparation never reruns the serializer. For precision-sensitive JSON
spelling, parse a `Value` and use `parameter_exact` or `json_exact`.
`Value` deliberately has no implicit `Serialize` implementation. For reusable
constructed values, use `Value::from_serializable(&model, limits)` and later
exact setters. [The form example](examples/forms.rs) combines ordinary values,
exact tokens and finite binary parts.

Supported checked Serde input includes signed/unsigned integers through 128 bits,
finite f32/f64, strings/chars, sequences/tuples, structs, string-key maps, standard
enums and bytes as numeric arrays. None/unit emits null; a skipped field is absent.
NaN/infinity and implicit numeric/bool/container map keys refuse. Char, unit-enum,
string-emitting newtype and explicit `collect_str` keys are supported. Float
spelling describes the supplied binary float, including negative zero; it cannot
recover earlier lost precision. Duplicate emitted names refuse with generated
source retained. These are the checked constructor's policies, not schema validation.

The pinned `serde_json` arbitrary-precision Number protocol is validated when its
named callbacks reach the constructor. At that boundary RawValue and unknown
private protocols refuse; use `Value::parse` for raw JSON. Adapters such as Serde
`flatten` can erase a wrapper's identity and emit ordinary marker-keyed maps.
That wrapper composition is unsupported. Literal marker keys stay literal data;
the constructor never interprets them as number or raw-JSON instructions.
Ordinary `serde_json::Value` order follows its active map representation, including
feature unification with `preserve_order`.

## Bounds, cancellation and ownership

All setters share aggregate byte/node admission across generated values, exact
backing sources and caller metadata. Shared allocations are charged once per
builder; equal text in separate allocations is charged separately. Exact child
handles admit their backing owner, not just the selected subtree. Final wire body
and target bounds are separate. Explicit constructor limits do not replace the
receiving builder's limits.

Each assignment counts, including `no_body()`. Repeated parameter identity,
body, election, server variable or credential scheme refuses even if equal.
Header parameter names compare without ASCII case; extra `header` calls append
ordered lines. The first refusal drops drafts and stops later serializer/iterator
callbacks. Duplicate assignments precede cancellation; count/name/budget admission
precedes Serialize. Select cancellation before input setters. A later token cannot
undo caller callbacks. Start a fresh builder to correct a refusal.

Checked construction caps emitted JSON depth at `min(limits.depth, 128)` and
checks cancellation at owned-work checkpoints. Arbitrary Serialize/Display,
iterator code, allocations and transparent wrapper recursion remain caller work;
container depth does not bound recursion through Some/newtypes. Before a complete
representation exists, failures have Construction context without invented source
positions. Complete generated parse failures retain Constructed bytes and locations.
Custom serializer/formatter error text is neither formatted nor retained.

## Read exact or project explicitly

`Number::to_i64()` and `to_u64()` check lexical form and range. `to_f64_exact()`
currently requires an integer lexeme fitting i64 and exact binary64 representation;
decimal/exponent forms and 2^63 are outside this convenience. Numeric conversion
work has its own character bound.

`Value::deserialize::<T>()` uses standard Serde directly on the selected original
JSON bytes, without an intermediate `serde_json::Value`. Integer destinations
reject overflow; floating-point destinations can round. Serde's `-0` integer
projection refuses, while checked `to_i64()` returns zero. Borrowed output can
borrow unescaped strings. Custom Deserialize is caller work and does no schema
evaluation. Response owners may defer their dynamic index; validation finishes
before acceptance and typed projection does not force that index.

Projection errors are `Diagnostic`, with coordinates and explicit
`detail() -> Option<&serde_json::Error>`. Default Display/Debug/error chaining omit
untrusted Serde text. Root coordinates include leading whitespace; child projection
uses its subtree. A failed projection leaves the value/outcome available.
[HOSTS.md](HOSTS.md) defines independent body admission and received-2xx acceptance.
