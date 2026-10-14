# OpenAPI fidelity contract

## Goal and authority

Within its documented support profile, the client should produce a request as
faithful as protocol code written from the same OpenAPI document. The OpenAPI
specifications and their relied-on RFCs are the only behavioral authority. The
client preserves distinctions relevant to invocation and records its own
unspecified choices in [policies.md](policies.md).

## Required properties

- **Declaration fidelity:** edition-specific operation, server, parameter,
  media, security and response interpretation follows the artifact.
- **Wire fidelity:** supported serialization distinguishes logical names,
  data and separators; unsupported conventions receive refusals.
- **Value fidelity:** exact numeric tokens and authored presence survive;
  typed conversion is an explicit caller choice with its own limits.
- **Evidence fidelity:** HTTP status, admitted metadata, delivered bytes,
  provenance, dispatch, upload and cancellation facts remain independent.
- **Ownership fidelity:** retained values and diagnostics own their source;
  immutable snapshots cannot change through a later caller mutation.
- **Explicit limits:** unsupported or ambiguous choices refuse before dispatch
  when knowable. Host transformations and incomplete evidence are disclosed.

## Current support boundary

The maintained Rust line accepts JSON/YAML Swagger 2.0 and OpenAPI 3.0.x, 3.1.x
and 3.2.x descriptions. Its profile includes supplied-resource graphs, schema
inspection, supported parameter styles/content codecs, finite JSON/raw/named
form requests, scoped supplied credentials and native response evidence. The
[Rust scope](../rust/README.md) and [API guide](../rust/client/README.md) describe
exact capabilities and exclusions.

Edition acceptance is not full feature coverage. Acquisition, schema
evaluation, streaming and remaining serialization/coding profiles remain open.
Callbacks and webhooks describe reverse interactions, not ordinary outbound
path-operation calls. Unknown extensions remain inspection data rather than
an implicit instruction to change standard OpenAPI semantics.

The core host contract and each companion's restrictions determine which
prepared requests can be dispatched. Native evidence does not promise
original wire header spelling, host-unfiltered metadata, remote rollback or
known complete upload. Borrowed JSON acceptance reads retain these facts;
they do not define every application's success criteria.

## Evaluation and qualification

1. Reduce behavior to a document and an independent OpenAPI/RFC expectation
   or a cited existing client policy.
2. Exercise the public API and observe the exact prepared request, response,
   diagnostic or lifecycle facts relevant to that behavior.
3. Classify divergence as a standards violation, documented policy, unsupported
   capability, artifact ambiguity or host limitation.
4. Fix the owning layer and retain a discriminating case.
5. Recheck relevant native, packaged-consumer and host evidence.

A corpus is evidence, not authority. Finite fixtures do not establish full
edition coverage, a real-host pass or the accuracy of an incomplete API
description. Known qualification failures remain visible in the
[development checkpoint](../rust/DEVELOPMENT-STATUS.md).

Go and TypeScript are legacy implementations, not maintained-product fidelity
claims. Their corpora and parity checks remain scoped to
[legacy gates](../LEGACY.md). Downstream synthesis, SDK behavior and migration
are not this client's qualification obligations.

The [capability and policy matrix](../rust/client/CAPABILITIES.md) records the
implemented shapes, standards authority, policies and verification scope.
