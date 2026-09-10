# Candidate value-fidelity surface alignment

The September 2026 value-fidelity candidate adds the already-qualified Go provider
`SourceJSONImage` helper and allows the neutral `JSONNumber` carrier in the
TypeScript parameter-conversion callback. TypeScript declaration chunk identities
also change as a consequence of that carrier. The public root Go surface is
unchanged. These changes were present in the qualified source checkpoint
`e5e61ce4e818951a7a1dec80057c97f1d053d605`; the landing pass adds no client API.

The public API digest file has been aligned with that exact source. Source runtime
and provider declarations remain separate. Boundary qualification permits only
the neutral JSON/schema leaves; it continues to reject Core, invocation, synthesis
and binding-specific public vocabulary. Go additionally checks the complete
compiled dependency closure rather than treating module ownership as coupling.

Before registry publication, clean consumer tests install real client and neutral
leaf archives together. That proves package portability, not registry availability.
