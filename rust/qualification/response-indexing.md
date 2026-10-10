# Response indexing qualification

The response path retains compact, fully validated JSON admission data and builds
the full dynamic index on first dynamic access. Typed projection reads the
original bytes without building that index. One shared breadth-first admission
algorithm owns all validation and diagnostic decisions. Materialization moves
already decoded payloads into the existing index; it does not parse or validate
the document again. Scalar roots and empty containers use the eager representation.

Public Rust signatures, source ownership, exact tokens and eager validation are
preserved. Public parsing, document construction and ordinary input construction
remain eager. Native HTTP transport configuration is unchanged. In particular,
this change does not enable connection reuse or alter the no-retry promise.

## Measured scope

The reference baseline is `60538f41cd0e79fedc5b278c8258dbf59f0ab203`, whose tree
equals PR 70 at `d8fbf55d167d8bf676c60b53e11ac821f9ce7826`. Measurements use candidate
`9d5ddbf6d6ff15b68a912e47b87f324ef7d9dc9d`, tree
`30f261eb4a630e0923ffa4cfd42fc90f1d19031a`. Later additions are a regression test
and documentation only; measured runtime source and dependency inputs are unchanged.

Actual core archive SHA-256:
`94fda97a1256e23455b31fda809ffa00cc53bca74f02431fa1c9115ef157edd1`.
Actual companion archive SHA-256:
`a48e126cf007b36de5d7c1bab75817e71574550c2aef8eae3f2496057509d455`.

The independent harness freezes six JSON shapes (small, medium, large,
escape-heavy, deeply nested and wide), default and arbitrary-precision Serde
graphs, and complete response admission/projection/inspection/destruction jobs.
Allocation instrumentation is separate from timing. Direct Serde measurements
are context, not an equivalent-work comparison. These local results are not a
production throughput or platform-wide guarantee.

| Route | Requested allocation traffic | Peak live requested bytes |
|---|---:|---:|
| Typed projection | 13.38–33.48% less | 17.58–40.78% less |
| First dynamic lookup | 3.26–11.29% more | 5.21–15.91% more |
| Full dynamic inspection | 2.13–6.81% more | 0.62–11.37% more |
| Typed projection then full dynamic inspection | 2.10–6.26% more | 18.93% less to 8.16% more |
| Ordinary document parsing | unchanged | unchanged |

Actual held requested bytes before dynamic access fall 22.47–45.52%. Medium and
large shapes retain approximately 45.5% fewer requested bytes. After
materialization, a surviving child holds 0.003–3.97% more requested bytes. Every
tracked owner and live allocation returns to its starting value after final
destruction. These are allocator-requested sizes, not RSS.

The frozen 50% benefit gate passes only through its **accounted index storage**
alternative: medium falls from 442,512 to 218,381 bytes and large from 7,071,960
to 3,488,573. That public estimate counts shared key text in multiple roles for
the materialized index and once in the compact representation. It excludes
allocator overhead and opaque synchronization storage. Therefore this is **not
a claim of 50% physical heap savings**. No cell achieves 50% less allocation
traffic. Future physical-storage gates should use allocator measurements rather
than comparison of these estimates.

## Timing verdict: inconclusive

Each complete timing round includes 64 cells with nine alternating paired
samples per cell and the original baseline-calibrated batches. Both rounds keep
all 40 mandatory control medians within the 1.05 candidate/baseline ceiling.
However, the single permitted complete repeat still contains one distribution
above the predeclared 20% range/median noise threshold. Both rounds are retained;
there is no third round and no qualified latency claim.

This candidate is retained for its deterministic typed-response memory benefit,
with the dynamic allocation cost explicitly accepted as a tradeoff. It is not
an unconditional performance improvement. The earlier implementation at
`3a3433053facfc86679b0c6a9eef6a352e24b54e` was rejected for conclusive first-lookup
latency regressions; its measurements are not attributed to this implementation.

## Correctness and review

Qualification includes the full repository verifier (workspace tests, corpus,
packaged public consumers across four Serde graphs, and the actual native
companion package), strict Clippy, a Wasm build, Chromium and workerd hosts.
Independent original-baseline differential checks cover 77,700 matrix entries
and 155,400 baseline-versus-candidate route comparisons across five Serde graphs.
Those checks cover admission diagnostics and priority, exact values, source
locations, ordering and conversions. Separate ownership checks cover concurrent
readers, borrowed projection and surviving child handles.

Permanent tests cover unchanged eager admission, concurrency, source-owner
identity, nonmaterializing typed projection/accounting and movement of payload
allocations. A subsequent test compares every materialized node against eager
parsing across twelve representative literals, including spans, parents,
segments, ordered object members, lookup maps and all scalar/array payloads.
It passes with default, arbitrary_precision, preserve_order and combined Serde
features. The node comparison module is compiled only under `cfg(test)`.

Astra and Sol found no concrete correctness or public API blocker. Claude Opus
5.5 Extra independently reviewed the source and actual archive correspondence;
it ran no tests or benchmarks. Its accounting caveat is reflected above, and its
requested permanent node comparison is included. A low-priority suggestion to
improve the panic message after an internal materialization failure is deferred;
no user-input path causing that panic was identified. Claude's original review
predates the final measurement receipts and does not endorse a latency result.

Run `python3 rust/qualification/verify.py <output-directory>` for the maintained
functional and packaged-consumer qualification. The complete frozen performance
harness, raw samples, allocation receipts, source/package hashes, failed first
design and independent reviews are preserved in the development evidence packet
`design/openapi-native-performance-2026-10-09` in the parent workspace. They are
not part of either published crate; both crates remain unpublished.
