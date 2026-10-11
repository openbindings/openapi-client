# Optional finite Fetch transport

This unpublished companion connects prepared core requests to Browser or Worker
Fetch on wasm32-unknown-unknown. It is under qualification; raw host observations
and author checks do not establish complete host/resource/performance support.
Native construction returns the controlled Unavailable refusal and performs no I/O.

```no_run
use dynamic_openapi_client::{Cancellation, PreparedRequest, ReplayPolicy, policies};
use dynamic_openapi_client_fetch::{Client, Profile};
# async fn call(prepared: &PreparedRequest) -> Result<(), Box<dyn std::error::Error>> {
let client = Client::new(Profile::Browser)?;
let outcome = client.execute_with_replay(
    prepared, Cancellation::default(), ReplayPolicy::AllowHostManaged,
).await;
match policies::received_2xx_json(outcome) {
    Ok(accepted) => println!("{}", accepted.json.root().raw()),
    Err(refused) => eprintln!("{refused}"),
}
# Ok(()) }
```

`execute` uses strict RequireNoReplay and ordinary Fetch refuses before its callback.
Explicit AllowHostManaged authorizes the host's repetition behavior. The library
calls Fetch once; this is not a bound on network attempts. Upload remains Unknown.
The separate received-response policy accepts a complete response without inventing
complete upload. There is no automatic library retry or redirect-follow loop.

Profiles use a real Request with manual redirects, omitted ambient credentials and
an empty referrer. Browser uses CORS. Request URL, method and every unique supplied
header must preserve their prepared representations or preflight refuses. Duplicate
supplied header names refuse; browser forbidden names/prefixes/conditional methods
and User-Agent also refuse. Worker applies conservative framing/decoding restrictions
and permits explicit Cookie. GET/HEAD refuse every present body, including empty.
Origin-scoped credentials and finite serialization stay in core preparation.

Browser field bytes use isomorphic ByteString code units; Worker outbound values
must be valid UTF-8 and incoming strings encode their exposed Unicode scalars as
UTF-8. Worker replacement already performed by the host cannot be undone. Neither
profile promises wire spelling/order or original uncombined fields. No lossy Rust
conversion, arbitrary comma splitting or whole Headers array copy occurs.

The adapter inspects host string lengths and exact encoded byte sizes before Rust
field copying, under the immutable response admission view. It retains whole header
entries up to count/byte bounds, observes EOF versus an extra entry, and stops at the
first cutoff/conversion failure with Incomplete. The host-exposed enumeration fact
is separate from HostProcessed fidelity, CORS filtering, body state and actual core
omission count. Metadata-only failure creates no TransportResult error detail.

One reader and one pending read retain at most body_bytes. Only the admitted chunk
prefix is copied; geometric capacity growth stays within that bound. Equal length
requires an additional EOF/positive-byte observation. Empty chunks do not establish
EOF. A task yield after 32 completed read iterations permits event-loop cancellation;
its timer is cleared on drop. Backend buffers, host-owned chunks and Request copies
remain separate from Rust retained-byte bounds.

Every response body is declared HostDelivered before parsing, consistently for both
profiles: raw() and exact JSON refer to host-exposed bytes, without wire-equality or
decompression promises. Content-Encoding remains visible metadata. Valid delivered
JSON with stale gzip or unknown labels intentionally parses; encoded binary refuses
exact parsing. There is no decoder, sniffing, codec allowlist, JS JSON roundtrip or
post-parse provenance upgrade. All existing media, completeness, metadata, limits,
status, replay and cancellation guards still apply. Native companion remains Wire.

Each invocation owns its controller, reader and cancellation registration. Dropping
one future aborts/cancels/releases its own resources without cancelling sibling
users of the same token. Cleanup attempts continue after synchronous exceptions;
rejected cleanup promises are observed by static JS glue with no Rust captures.
Unresolved original host promises retain only JS observation state. A separately
settled wrapper releases Rust callback/waker state after a microtask, even if the
original stalls. Local drop is not immediate host settlement or remote cancellation.
The embedding must continue running its event loop; host-only pending residue and
Rust settled ownership require separate measurement. No eval, Function constructor,
forgotten Rust closure or detached Rust cleanup future is used.

All failures use controlled labels; arbitrary JS rejection messages are never copied
or formatted. Source preparation diagnostics stay with core; host refusals do not
invent authored ranges. Worker execution belongs inside an active request handler.
Actual packaged Browser/Worker jobs, feature graphs and resource gates remain
qualification obligations. No TypeScript facade, stream API or publication is implied.

The build pins wasm-bindgen 0.2.129, js-sys 0.3.106 and
wasm-bindgen-futures 0.4.79. Generated glue must use the matching wasm-bindgen CLI.
The Worker profile is being checked with workerd 1.20261006.1, compatibility date
2026-10-08 and flags `url_standard`, `fetch_standard_url`, `enable_request_signal`,
`brotli_content_encoding`, `http_headers_getsetcookie`, `no_nodejs_compat`. These
explicit assumptions do not establish behavior for other Worker configurations.
