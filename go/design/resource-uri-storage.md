# Sharing retained resource URI storage

The loader keeps canonical resource identifiers in `document.ids`, and parsed
resource bases in `tree.resourceBases`. A long hierarchical identifier can keep
its path twice: in the identifier key and in `url.URL.Path` (or `RawPath`).

Once discovery, retrieval workers and scope settlement have finished, the loader
borrows matching path text from its retained identifier keys. It changes a string's
backing storage only when its bytes match exactly. All URL field values, pointer
identities, resolution rules and diagnostics stay the same. The client is published
after this step; subsequent readers continue to see immutable URLs.

Using the final identifier keys matters: temporary or rejected identity strings
must not acquire a new owner. This pass creates no index or string cache. It also
handles the second claim of an ambiguous identifier, preserving the ambiguity.
Cancellation is checked while walking the identifier map.

Keys shorter than 256 bytes skip resource lookup and compaction. An unrestricted
prototype saved about 48 kB on a wide, short-URI fixture but added 3.3% to loading
time. The cutoff targets substantial duplication while avoiding that tradeoff.
It governs storage only and changes no interpretation of an identifier.

## Measured result

Baseline: `5daf1fd5307ffa866c094b4bac1d13face7082da`.
Go 1.25.12, Apple M1 Max, darwin/arm64, `GOMAXPROCS=4`; synthetic local fixtures.
Numbers below are medians of three fresh-process measurements with multiple
retained clients and two GCs before/after. MB is decimal.

| Workload after Parse | Baseline live heap | Shared storage | Reduction |
|---|---:|---:|---:|
| 512 nested relative IDs | 5.533 MB | 2.776 MB | 49.8% |
| Nested IDs with escaped path segments | 9.057 MB | 5.982 MB | 34.0% |
| Nested IDs with queries | 5.873 MB | 3.113 MB | 47.0% |
| 2,048 short resource URIs | 2.590 MB | 2.591 MB | Within measurement noise |

The deep fixture also falls from 5.642 MB to 2.886 MB after schema inspection.
Ordinary small/large clients, operation compilation, and duplicate-ID controls
show no material retained-memory increase. Exact heap profiles confirm that the
separate resolved-path buffers are released while identifier strings remain.

This improves post-GC retention. It does not remove the transient allocations
used during resolution, the full canonical identifier keys, or public descriptor
`Source` strings. Deep identifiers can still have quadratic total text length.

## Validation

- Both Go public documentation hashes match `api/public-api-v1.json` exactly.
- `go test -race ./...`: 894 top-level test/fuzz targets and 6,147 subtests pass.
- `go vet ./...` and formatting checks pass.
- `FuzzResourceURIStorage` and `FuzzRelativeReferences` each pass a 60-second run.
  The new fuzzer checks complete URL field equality and public schema Base/Raw
  behavior. Storage tests check both plain and escaped paths in loaded clients.
- Six alternating baseline/candidate rounds cover 33 focused and 50 inherited
  benchmark cases: loading, first use, repeated calls, body encodings, streaming,
  shared references, and first/warm schema inspection. Effective final comparisons
  stay below 2% median time increase and satisfy the allocation guards. The gates
  permit at most 5% median time increase, baseline bytes/op times 1.05 plus 128,
  and one extra allocation/op.

Two initial broad-screen flags are retained in the evidence: a first anchor lookup
at 128 nodes was +5.18% using 100 iterations, and one identifier-free versionless
workload reported +2 allocations. One predefined six-round confirmation used
1,000 first-lookups and longer versionless samples; these became +0.91% and zero
extra allocations, respectively. Versionless GraphOnly cases were also collected
with their documented fixed-count protocol. No production code was changed to
obtain those confirmations.

`BenchmarkResourceURIParse` exposes the load-time cost alongside the existing
deep-resource first-lookup gate. The frozen preexisting fixtures and tests are
unchanged. Test-only pointer inspection verifies storage sharing; production code
uses ordinary string slicing and the existing `net/url` implementation.

Qualification for this pass covers Go. The full TypeScript/repository release
qualification was not rerun; TypeScript sources are unchanged.
