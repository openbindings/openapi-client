# Immutable resource graphs

`Document::parse(bytes, retrieval_uri, limits)` remains the ordinary single-resource
entry point. When callers already hold several resources, `DocumentBuilder::new`
accepts each `ResourceInput` and publishes the same `Document` with `build(entry)`.
The core never reads a file or makes an HTTP request. Retrieval URI metadata grants
no access authority and does not carry API credentials.

```rust
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, exact::*, form::*, graph::*, inspect::*, schema::*, source::*, transport::*};
# fn complete(document: &Document, error: &Diagnostic, bytes: &[u8]) -> Result<Document, Box<dyn std::error::Error>> {
let need = error.resource_need().expect("a missing-resource diagnostic");
let mut builder = DocumentBuilder::from_document(document);
let uri = need.requested_uri().ok_or_else(|| Diagnostic::new(Code::MissingResourceBase))?;
builder.supply(need, ResourceInput::at(uri, bytes)?)?;
let completed = builder.build(document.entry_resource().id())?;
// Reacquire operations/sites in the new snapshot before preparing again.
# Ok(completed)
# }
```

Builder preparation, effective inspection and parameter inspection expose the
same `Diagnostic::resource_need()` when
their selected protocol dependencies are unavailable. A need identifies bytes or
layout, the requested URI, source edge, expected reference kind and edition, and
target pointer. These fields are explicit access to untrusted document data.
Default diagnostic formatting omits them.

An OpenAPI resource indexes its own standard protocol positions. A reference to
the root of an otherwise untyped fragment can establish that root's protocol kind.
A pointer into an untyped bundle requires an explicit `ResourceLayout`; arbitrary
payload fields are never guessed to be declarations. This is an explicit current
compatibility limitation. Repair a layout need with
`builder.apply_layout(need, ResourceLayout::new().protocol(pointer, kind, edition))`.
`enrich_layout(resource_id, layout)` also supports deliberate compatible enrichment
before publication. It shares the original bytes, source arena and ResourceId.
Conflicting interpretation or byte/identity replacement refuses.

A published snapshot never changes. `from_document` shares admitted arenas but
rebuilds semantic indices and caches. Existing operations, resolved values and
errors retain their original meaning. `ProtocolSite` and `ResourceNeed` are bound
to their exact snapshot: reusing an old site with a new Document, or an old need
with a builder seeded from another snapshot, returns `SnapshotMismatch`, even
when both snapshots share the same ResourceId. Raw Values own source arenas
independently; cached graph results and errors do not own the snapshot itself.

For direct protocol inspection, use `document.protocol_at(pointer, kind)` for the
entry resource or `document.resource(id).unwrap().protocol_at(pointer, kind)` for
another source, then `document.resolve_protocol(&site)`. The result preserves the
authored wrapper, chain, target and supported metadata overlays. No second resolver
or caller-selected version mode is required. Resources expose original bytes,
retrieval identity, recorded aliases and applied description base.

Protocol kinds include Path Item, Parameter, Response and each later edition's
Request Body, Header, Example, Link, Callback, Security Scheme and Media Type
positions. OpenAPI 3.2 nested/positional Encoding Header references are indexed;
that inspection support does not enable structured multipart serialization.
Referenced optional headers in currently supported finite forms use the graph;
required/custom header refusal remains explicit. Schema positions use the typed
[schema graph](SCHEMAS.md); examples' instance values and extension payloads remain inert. A direct Operation Object `$ref`
still reports `UnsupportedReferencePosition`.

External Path Items contribute mounted operations. `operations()` is the known
inventory; `discovery_complete()` reports whether all mounted Path Items resolved.
An unresolved mount makes operation-ID uniqueness unavailable, so `operation_id`
returns `IncompleteDiscovery` with the corrective need when applicable. Explicit
path/method selection can still use unaffected paths. Visible local declaration
errors appear in `graph_issues()` in authored path order and do not alone imply
hidden operation IDs. Unsupported editions remain available for raw inspection;
semantic preparation still refuses their edition.

Path Item resolution ignores unknown and extension members when interpreting
operations; it preserves their authored values. Only fields defined by the
originating edition participate in reference conflicts. This tolerance is not a
claim that the entire input validates against an OpenAPI schema.

The client applies an explicit policy to Path Item `$ref` conflicts, whose behavior
OpenAPI leaves undefined. Repeated standard method declarations make that method
ambiguous, without disabling unrelated methods. A successful `resolve_protocol`
retains the complete chain and reports these conflicts in
`ResolvedReference::path_item_conflicts` (method plus located diagnostic).
`graph_issues()` also exposes them. Raw path/method selection retains the first
visible declaration for inspection, but its preparation and effective description
refuse with `AmbiguousReference`. ID lookup never selects a conflicting declaration
as a winner; IDs from every layer still participate in global uniqueness checks.
Unaffected operations remain discoverable, selectable and preparable.

Repeated recognized shared fields (parameters, or the edition's metadata/server
fields) still refuse the whole mount. The conservative whole-mount policy for two
3.2 `additionalOperations` maps is unchanged. Missing resources, wrong-kind targets,
cycles, incompatible editions and reference-depth limits still fail resolution.
`discovery_complete()` describes declaration visibility, not operation validity;
method-local conflicts can coexist with complete discovery.

Implicit security names resolve from the entry OpenAPI document. In 3.2, an exact
component name takes precedence over URI interpretation, including URI-looking
names. Otherwise a security requirement key is resolved as a URI reference from
its source. Earlier editions retain undefined-name refusal. Reference target
interpretation must match the originating feature family; cross-family protocol
references refuse rather than translate.

For 3.2, `$self` establishes description identity and relative reference base.
The exact identifier, including a nonempty fragment, aliases the resource root;
the fragment-free URI supplies the relative base. Retrieval aliases remain usable
and are observable as noncanonical when they differ. Empty `$self` resolves against
a real retrieval URI; without a base it reports located `MissingResourceBase`.
API Server URLs use the declaring source's retrieval context independently.
Anonymous sources resolve local fragments without an invented base URI.

`GraphLimits` caps aggregate resource count, original/source-arena bytes, nodes,
reference edges, identities, layout roots and URI/pointer metadata bytes. Admission
checks counts before constructing metadata indices, including idempotent additions
and enrichment. Per-source `Limits` still apply. `reference_steps` bounds each
protocol chain; `descriptor_work` independently bounds aggregate declaration work.
There is no product retained-byte telemetry API. Unsupported qualification hooks
account for source arenas separately from graph metadata, compiled descriptors,
temporary overlap and allocator overhead. Bounds describe library work;
constructing a caller-owned ResourceInput/layout is caller work.

Explicit Link operationRef/operationId connection resolution and discriminator
URI inspection are not implemented by this Reference Object resolver; their raw
fields remain inspectable. Schema inspection and private bounded facts are described
in [SCHEMAS.md](SCHEMAS.md). Form planning and selected-field descriptions expose
schema needs through this same recovery path. File/HTTP/custom acquisition
remains caller-owned. No evaluator,
implicit fetching, synthetic base, document rewrite or complete-feature claim is
introduced by this graph layer.

Protocol interpretation follows the edition's Reference and Path Item Objects,
[RFC3986 §5.2](https://www.rfc-editor.org/rfc/rfc3986#section-5.2) and
[RFC6901 §§4–6](https://www.rfc-editor.org/rfc/rfc6901#section-4).
Snapshot identity, fragment layouts, conflict handling and bounded discovery are
client policies. Matrix rows I01 and G01 in [CAPABILITIES.md](CAPABILITIES.md)
link their authority and implementation evidence.
