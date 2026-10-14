# Schema inspection in supplied resource graphs

`document.entry_resource().schema_at(pointer)` returns an owning schema handle at
an indexed schema position. The ordinary OpenAPI document selects the applied
schema profile. Callers do not switch editions to inspect a request schema.
`ResourceLayout::schema` supplies interpretation only for otherwise untyped bundles.
The core acquires no files, network resources or metaschemas.

A handle exposes the authored value, physical resource, applied profile, lexical
schema resource root, optional canonical URI/base and resource-relative pointer.
`references()` lists direct `$ref` and supported `$dynamicRef` keywords in source
order. `resolve_static()` resolves one edge; a legal self edge can return the same
schema. `resolve_static_with_cancellation` provides a cancellation checkpoint and
uses the configured reference allowance. Dynamic-anchor resolution reports the
initial target and `requires_dynamic_scope`, without claiming an evaluation target.

Supported schema profiles are Swagger 2.0, OpenAPI 3.0, JSON Schema 2020-12, the
OpenAPI 3.1 base/2024-11-10 dialect and OpenAPI 3.2's 2026-02-26 dialect. The latest
applied 3.2 profile governs both authored 3.2.0 and 3.2.1 documents. Explicit root
`$schema` and recognized OpenAPI `jsonSchemaDialect` take precedence. Unknown
dialects remain opaque; a familiar raw field never grants a fallback dialect.
Legacy schemas do not activate modern `$id`, `$defs` or `$schema` semantics.

Known schema roots with opaque dialects remain inspectable. A static reference
may identify an opaque root or an independently laid-out schema location. Its
`references()` and facts report unsupported semantics. Arbitrary descendants
under unfamiliar keywords are not inferred to be schemas; named-anchor lookup
in an opaque dialect is unsupported. Examples, defaults, enum values, extensions
and unknown custom vocabulary content remain instance data.

Indexing respects lexical `$id` resources, supported subschema positions and
`$anchor`/`$dynamicAnchor`. URI fragments are percent-decoded once, then JSON Pointer
escapes are decoded once. Malformed references differ from absent targets. Duplicate
anchor declarations refuse, including two anchor keywords at one node. Physical
pointers crossing an embedded `$id` can identify the indexed schema and record
`CrossingPointer`; subsequent references use the target's lexical base. Retrieval
aliases record their own noncanonical access reason. No incoming reference rebases
the target schema.

Supplied bare whole-schema resources receive their default interpretation through
a bounded closure before publication, regardless of insertion order. Explicit
contexts win; conflicting implicit defaults refuse with both source contexts.
Later resolution never changes a snapshot. Missing resources and layouts use the
same `Diagnostic::resource_need()` as protocol references. See the executable
[`schema_resources` example](examples/schema_resources.rs) for handled Bytes and
anonymous Layout recovery. Reacquire handles after publishing the completed graph.
Old handles continue to describe the old graph.

This unpublished slice changes several ResourceNeed getters:

| Getter | Result |
| --- | --- |
| `requested_uri()` | `Option<&str>`; Bytes has an absolute fragment-free URI, anonymous Layout can have none. |
| `reference_kind()` / `edition()` | `Option`; protocol needs return Some, schema needs return None. |
| `target_pointer()` | `Option<&str>`; root is Some empty string, pointer is Some pointer, anchor is None. |
| `interpretation()` | `Protocol { kind, edition }` or `Schema { default_profile }`. |
| `fragment()` | `Root`, `Pointer`, or `Anchor`. |
| `kind()` / `target_resource()` / `source()` | Retain their prior meanings. |

Identity-collision diagnostics keep the later declaration as their primary site.
`DiagnosticReason::IdentityCollision { previous }` identifies the earlier claimant
with its owning `SourcePosition`, using authored JSON/YAML when available.
The position retains its source independently of the document. Its `source()` and
`raw()` accessors explicitly expose original content; routine formatting does not.
`Diagnostic::related()` continues to contain only locations in the primary source.

Default diagnostics and handle/profile Debug output omit document-controlled URI
and name text. Explicit getters retain it as untrusted authored data.

The private declaration-fact layer distinguishes Known, Absent, Need, Indeterminate
and Invalid. It follows supported static references, respects legacy siblings and
modern conjunctions, and retains lexical property/additionalProperties membership.
It does not validate instances, infer types from enum, select branches, insert
defaults or coerce values. Composition, recursive required facts, dynamic scope and
conflicting declarations remain explicit indeterminacy. Unknown-property queries
on open schemas remain absent facts, not an input rejection rule.

`GraphLimits` also bounds schema identity and reference indexing. Each private fact
session shares `Limits::schema_fact_work` (500,000 by default); fact traversal depth
(128 by default) is independent of reference-chain depth and descriptor work.
Each query memoizes completed source nodes and accumulates unique contributions
and provenance in deterministic order. Memo hits still consume work and enforce
the full selected path's depth and hop limits; active recursion remains indeterminate.
Known provenance identifies local Known contributions to the returned payload;
Absent provenance identifies absence sites only when the whole result is Absent.
Every relevant conjunct is still inspected and charged. Provenance is not a log of
all visited reference, scope or absence evidence. Conflicts retain their incompatible
Known contributors.
Name membership grows incrementally, with incoming strings and joins charged before
growth. Numeric type conjunction treats integer as a subset of number, without
inspecting instances. Query scratch is released after the result is returned; no
caller-name cache persists in the snapshot.
Facts and retained caches contain owner-free keys, never handles owning the same
snapshot. Raw authored values retain only their source arenas after graph owners drop.

Request/form preparation does not consume these schema facts yet. Selected coding,
media, richer representation behavior, acquisition and evaluation remain later work.
A correct Twilio enum reference, Flyn declared type union or Transmute content metadata
fact is not evidence that its full request workflow is supported.

`SchemaHandle::keywords_supported()` reports whether direct keyword semantics are
supported. Unknown dialects remain inspectable without a schema-validation claim.
`references()` returns an exact-size iterator owning its ordered edges; it may be
consumed after dropping the originating handle and Document.
