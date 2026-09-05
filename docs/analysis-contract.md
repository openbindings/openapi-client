# OpenAPI analysis contract

## Purpose

The loaded OpenAPI client owns one binding-specification-governed semantic
model. Invocation, preflight, streaming, and generator analysis are views over
that model; a protocol adapter must not parse or reinterpret the source.

The public analysis surface is useful without OpenBindings. It contains no OBI
document, Core expression, SDK lifecycle, or CLI type.

## Two levels

The private compiled graph may retain parser nodes, executable serialization
plans, caches, and reference indexes. It is created by the native loader and is
the state used by `preflight`, `call`, and `stream`.

The public analyzed artifact is a detached read-only view of the same graph. It
contains only values, opaque artifact-scoped identifiers, and immutable schema
images. It performs no retrieval or reparsing. Returning an analysis value must
not allow its consumer to mutate either a later analysis result or invocation.

## Artifact facts

An analyzed artifact provides:

- exact OpenAPI edition and source identity;
- deterministic operation and inbound-interaction inventory;
- artifact-scoped schema/resource identities and source coordinates;
- source and target diagnostics; and
- the smallest-owner support disposition of every declaration that affects
  projection.

Operation analysis provides:

- selector, path, authored method, wire method, operation metadata, and target
  disposition;
- effective parameters, stable application input keys, serialization cells,
  emitted destinations, schema identities, and parameter coverage;
- request representation alternatives, body correspondence, property-media
  requirements, coding requirements, and schemas;
- response lookup precedence, success capability, media alternatives, output
  schemas, response-header obligations, and coding constraints;
- effective server alternatives and their static/runtime dispositions;
- complete security alternatives and credential destinations;
- configuration requirements as disjunctive alternatives of conjunctive
  requirements; and
- exhaustive target, alternative, and projection coverage facts.

## Identity and provenance

Node and schema identifiers are opaque and stable only within one analyzed
artifact. A source coordinate contains a resource identity and JSON Pointer.
Reference-site identity and declaration-target identity remain distinct.
Cycles are represented by identifiers rather than public object pointers.

Authorial presence is preserved wherever omission differs from an authored
empty, null, zero, or false value. Analysis never reconstructs presence from a
typed parser's zero values.

## Schema projection

The provider owns OAS dialect, `$ref` sibling, direction, and authorial-presence
semantics. It exposes detached schema images and a reusable JSON Schema 2020-12
projection with explicit loss diagnostics. The OpenBindings adapter places the
result into an OBI operation but does not reinterpret OAS Schema Objects.

## Static analysis and invocation choices

The analyzed artifact records available alternatives and configuration
requirements. A selected server, media type, security alternative, credential,
or codec belongs to one invocation and never mutates the artifact.

Private lazy computation is allowed when it is deterministic, synchronized,
and observationally immutable. It must not retrieve or reparse source material.

## Language invariants

TypeScript does not expose a raw document or mutable `Map`, `Set`, array, or
record. Public records are deeply frozen or returned as defensive copies.

Go does not expose `kin-openapi` pointers or writable internal maps and slices.
Accessors return values, immutable handles, or defensive copies. Concurrent
analysis, preflight, invocation, and synthesis must pass the race detector.

Go and TypeScript APIs may be idiomatic rather than structurally identical,
but a test-only canonical observer must report the same semantic facts.

## Adapter boundary

The OpenBindings adapter may own:

- OBI operation-key derivation and collision handling;
- OBI source, operation, binding, and dependency construction;
- mechanical Core expression rendering from native correspondence facts;
- mapping native requirements and support dispositions to Core; and
- SDK lifecycle and error translation.

It may not own OpenAPI reference resolution, effective declaration selection,
serialization, media planning, server or security analysis, response lookup,
schema-dialect interpretation, or smallest-owner classification.

## Cutover gate

The provider cutover is complete only while an adapter cannot access a raw
OpenAPI document, load or resolve a source under an independent policy, clone a
parsed graph, import a private client path, or retain a copy of a native
semantic planner. The TypeScript and Go adapters meet that condition: they
consume detached provider values and render only OpenBindings-owned structures
and Core correspondence expressions. Behavioral corpus success supplements
this architecture gate; it does not replace it.
