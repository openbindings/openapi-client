# October 2026 independent-review repairs

Baseline: `4058c2cb160c859342426ffbeee71dc748d01f29` (PR #55 on main).
The independent Opus review's original probes and logs are preserved outside
the checkout, with a SHA-256 manifest. The review found defects in both earlier
stages and the continuation; this pass owns the fixes regardless of origin.

## Scope and verification

The exported API, its snapshot, the YAML dependency choice and the URI-storage
optimization are unchanged. Regression tests preserve the reported triggers
and add adjacent valid cases. This report is provisional until the checks below
and the approved real-document corpus have completed.

| Finding | Repair or disposition |
| --- | --- |
| Quadratic octal parsing | Pack octal digits into machine words before decimal conversion; test exact values across word boundaries and allocation scaling. Decimal formatting still costs more than a linear scan. |
| Fragment `$id` corrupts document identity | Do not register a nonempty-fragment identifier as a schema resource or change its base. |
| Swagger host/basePath changes authority | Reject malformed host and basePath declarations before request preparation can use them. |
| Versionless document `$self` | Apply `$self` only to an OpenAPI 3.2 document, not a referenced document inheriting its edition. |
| JSON-sequence latency | Deliver a complete JSON text at its terminating LF; retain RS recovery, scalar truncation detection, and record size limits. |
| Windows drive paths | Form `file:///C:/...` and convert file URI paths back to native drive paths; add an actual Windows CI job. |
| Reserved path triplets lost | Escape query/fragment delimiters in path values while preserving valid percent triplets. |
| Truncated multipart header block | Distinguish a closing delimiter from EOF after a regular delimiter, using bounded storage. |
| Edited ContentLength truncation | Do not synthesize EOF for caller-supplied bodies at the declared length; detect excess bytes and retain completion when a transport closes at exactly that length. |
| Edited Content-Type lost on redirect | Rebuild the client's generated Content-Type after crossing origin; do not forward the caller's edited value. |
| Contradictory YAML locations | Retain the parser's exact position and remove its redundant legacy line prefix. |
| One-letter URI scheme | Distinguish URI schemes from native Windows drive paths before reference admission. |
| Retrieval-URI anchor alias | Resolve anchors through the document's retrieval and redirect aliases as well as its canonical identity. |
| Custom Fetch and real filesystem admission | Keep the documented symlink-aware default boundary. A virtual filesystem supplies an explicit AllowReference policy; test that path. Changing the default would change the security contract. |

## Decisions reserved for the owner

- **YAML maintenance:** this pass does not approve or replace the parser copy.
  The frozen Loader contract promises YAML 1.2 Core behavior and exact positions;
  the copied parser's patches implement parts of that promise. Keeping the copy
  requires explicit acceptance of upstream monitoring and backport work.
  Using the unmodified module requires a reviewed compatibility change, including
  source positions and the affected YAML syntax. Neither is a free substitution.
- **Performance target:** historical misses of the 15% handwritten-client target
  remain misses. A recorded engineering explanation is not owner acceptance.
  Keep the original measurements alongside fresh before/after repair results;
  do not quietly redefine the target or claim an optimization ceiling.
- **Source fields:** eager public Source strings still have a worst-case memory
  cost. Replacing them with methods changes the frozen public API. The URI-storage
  repair does not resolve that decision. Real-document measurements should inform
  whether to retain the fields for the first release.
- **Corpus:** document bodies are downloaded only after approval of the pinned
  source/file/size manifest. Loading, inspection and preparation use local copies;
  no live API requests or arbitrary reference downloads are part of the test.
