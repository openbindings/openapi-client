# Native connection reuse qualification

The native companion reuses eligible HTTP/1.1 connections between a client and
its clones, retaining up to eight idle connections per scheme/authority with a
90-second idle timeout. Active concurrency and the number of origins remain
caller-controlled. Idle timeout is not a promise that physical socket closure
occurs at exactly ninety seconds.

Public Rust signatures remain unchanged. The explicit contract revision permits
recovery only when the backend proves the original request was never serialized.
It forbids automatic replay after serialization began or may have begun. The
core invokes its callback once; the companion has no retry loop and retains
`reqwest::retry::never()`. Upload completion remains unknown after backend
execution starts, even if a complete response arrives. Multiple proven-unsent
connection attempts share the existing total request deadline.

## Source basis for no replay

The qualified backend is reqwest 0.13.5, hyper-util 0.1.21 and hyper 1.12.0.
The companion constrains those already-used packages exactly, with default
features disabled on its direct hyper/hyper-util constraints. The public
`Certificate` and builder-error types remain reqwest's types. There is no backend
fork, local registry patch required by consumers, runtime inspection hook or
alternative public execution path.

In the pinned hyper-util HTTP/1 client, internal recovery requires both a reused
connection and recovery of the original request through `take_message()`
(`src/client/legacy/client.rs`, retry loop and `send_request`). The criterion is
ownership of a wholly unsent request, not method idempotence or a cloneable body.
Hyper can return that ownership while the message remains queued or dispatch is
not ready. Once HTTP/1 dispatch extracts the request head/body for serialization
(`src/proto/h1/dispatch.rs`, `poll_msg`), it retains only the completion callback;
subsequent errors and callback destruction have no original message to return.
Those errors cannot enter the recovered-request retry branch. This conservative
boundary precedes possible wire visibility. The alternate checkout retry path
is HTTP/2-only; the companion explicitly selects HTTP/1 even under feature
unification. Reqwest's separate outer retry policy declines every retry.

Exact constraints deliberately make backend upgrades require review and
qualification of these ownership paths. They also trade dependency-resolution
flexibility for the qualified contract. A library Cargo.lock alone would not
constrain a consuming workspace's transitive versions. The external archive
driver compares the selected backend versions and checksums with the qualified
workspace lock, retaining every matching version, and checks reqwest's actual
hyper/hyper-util dependency edges. Caller source patches/overrides are outside
the registry-source qualification.

## Complementary test layers

The deterministic backend test controls its executor and in-memory I/O. It
warms a connection, enqueues the next request without polling serialization,
then injects stale closure. Recovery enabled yields a fresh connection and one
complete request on each connection; disabled recovery returns failure. A
separate injection after seven written bytes fails without a replacement attempt.
The original standalone probe remains preserved in the development evidence.

This lower-layer test uses GET with an empty body and an equivalent outer Never
policy. It does not by itself test reqwest's complete private composition or
arbitrary request bodies. The source audit and actual marked-POST socket tests
provide complementary evidence. TCP accepts alone cannot distinguish an empty
connection-establishment race from an application replay.

The socket recorder observes connection IDs, TLS handshakes, request starts,
complete requests and partial bytes. Tests use unique call markers, explicit
server channels and joined shutdown. Positive idle readiness is observed through
a pinned, test-only hyper-util tracing event on a current-thread runtime. The
event occurs during synchronous pool insertion; it is not a production API and
does not alter the companion's pooling behavior. No sleep is used to infer
readiness or request receipt.

The matrix covers HTTP/TLS reuse and clones, separate origins and trust settings,
complete/chunked/close-delimited framing, malformed/incomplete bodies, body and
metadata limits, application refusals, cancellation and deadlines, concurrent
calls, idle capacity/expiry, stale closure and partial/full started POSTs. A
complete framed response may remain reusable after an application refusal;
deliberately withheld framing must not contaminate the next explicit call.

The stale-socket cases distinguish server-observed closure from a racy closure.
They do not deterministically control the backend's private checkout/serialization
interleaving or claim proof of the internal unsent-recovery branch. Unstable
hyper tracing is not enabled merely to expose that state. The controlled
in-memory test supplies the private-branch evidence.

## Replaying qualification

Run `python3 rust/qualification/verify.py <output-directory>` from the repository
root. In addition to workspace tests and existing core/native public consumers,
the native archive driver copies the actual packaged integration tests and
fixtures into a separate consuming crate. It runs them under default,
arbitrary_precision, backend-unified and combined features; backend-unified
enables reqwest HTTP/2 and gzip dependencies while the companion must retain its
HTTP/1 and no-decoding restrictions. Ordinary example dependencies remain
unchanged; archived test-only dependencies are development dependencies in that
consumer. No workspace implementation is substituted for either extracted crate.

`backend-graph.json` records selected versions/checksums and dependency edges.
Command logs and consumer manifests/locks are preserved, including failures.
The development packet `design/openapi-connection-reuse-2026-10-10` in the parent
workspace binds final source/packages, independent reviews, probe observations
and any fixture corrections. Claims about passing runs belong to those exact
receipts, rather than this description of the maintained checks.

This qualification establishes connection and handshake reuse and the stated
failure boundaries. It makes no latency, throughput or universal performance
claim. JSON-indexing measurements remain separately scoped in
`response-indexing.md`; enabling pooling does not remeasure those results.
