# OpenAPI inspection and analysis contract

## Authority and purpose

The maintained Rust client's model interprets the document under its OpenAPI
edition and the RFCs OpenAPI relies on. Inspection and preparation share source
identity and reference interpretation. Their behavior is not derived from a
downstream integration, generator or binding specification. Unspecified client
choices belong to the [policy register](policies.md).

## Authored and effective views

Authored inspection exposes retained declarations and exact values. Effective
operation descriptions resolve applicable declarations and preserve their
source. Inspection can describe an unsupported declaration without promising
that preparation supports it. The [API guide](../rust/client/README.md) covers
`Operation::inspect`, `Operation::describe` and `Operation::parameters`.

The immutable [resource graph](../rust/client/GRAPH.md) owns supplied bytes,
retrieval identities and reference targets. Callers supply missing resources
or fragment layouts through a builder and publish a new snapshot. Inspection
does not retrieve material or mutate an existing snapshot.

## Identity and provenance

Resource and node handles belong to their snapshot. A source coordinate
identifies an owning resource and JSON Pointer; a reference use and its target
remain distinct. Omission is not reconstructed from a typed zero value:
authored null, empty, zero and false values remain distinguishable.

JSON values preserve exact token/source ranges. YAML values have normalized
exact JSON representations and separate original source traces, including
alias use sites. Retained owners keep values and diagnostics usable after
originating handles are dropped. See [loading](../rust/client/LOADING.md).

## Schemas and invocation choices

[Schema inspection](../rust/client/SCHEMAS.md) provides owning handles,
dialect-aware identity, anchors, reference resolution and bounded fact
inspection. It is not instance validation, default insertion or a guarantee of
schema-dependent request planning. Remaining preparation limits are recorded
in the Rust guides and [development checkpoint](../rust/DEVELOPMENT-STATUS.md).

A server, media type, security alternative, credential or host permission
belongs to a call. Selections do not change authored declarations or the
loaded snapshot. Private computation must preserve public ownership and
immutability.

## Consumer boundary

Applications, generators and external integrations consume supported public
handles and descriptions. Their output models, naming strategies and lifecycle
translations are their own responsibilities. They establish no new semantic
authority or migration gate for this client.

Go and TypeScript provider models are legacy, not the maintained analysis API.
Their tests and snapshots impose no cross-language parity or Rust projection
obligation. See [legacy qualification](../LEGACY.md).

The [capability and policy matrix](../rust/client/CAPABILITIES.md) records the
implemented shapes, standards authority, policies and verification scope.
