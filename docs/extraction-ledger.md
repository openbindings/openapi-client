# Implementation qualification ledger

This is the human-readable companion to
`conformance/openapi-authority-ledger.json`. The generated ledger inventories
every normative processor and synthesis rule at the pinned OpenBindings 0.2
source revision. Implementation evidence is recorded separately so refreshing
the authority never overwrites the implementation's claims.

## Current phase

| Area | State | Stopping condition |
| --- | --- | --- |
| Authority | pinned | source, corpus, schemas, hashes, and rule citations verify |
| Product contract | candidate-frozen | reviewed TypeScript declarations and Go documentation snapshots pass |
| TypeScript native client | candidate-complete | pinned corpus, lifecycle, browser, package, and consumer gates pass |
| Go native client | candidate-complete | pinned corpus, race, package, and external-consumer gates pass |
| Portable processor corpus | pinned and complete | all 896 pinned scenarios pass unchanged through both public clients |
| Engine-owned analysis | complete for adapter cutover | detached immutable provider projections own parameter, media, security, response, schema, and coverage decisions in both languages |
| Portable OBI synthesis | behavior- and architecture-complete | TypeScript and Go adapters pass all 154 portable OAS-family synthesis scenarios as mechanical projections of native provider facts |
| Cross-language parity | candidate-complete | normalized observations match on all 896 portable invocation scenarios |
| OpenBindings adapters | provider cutover complete | thin invocation and synthesis bridges pass; no duplicate OpenAPI executor or declaration planner remains |
| SDK and OB CLI | cut over | generic prepared-provider routing, bounded revision reuse, safe local diagnostics, and exact raw-binding exploration pass together |
| Standalone release quality | candidate-qualified | the complete repository-local release gate passes against the pinned authority |

## Evidence rules

An implementation rule is complete only when the implementation-evidence file
names executable TypeScript and Go evidence. Passing an upstream structural
verifier or citing an upstream scenario is not implementation conformance.

The release gate requires:

1. every applicable P-rule to have passing TypeScript and Go processor evidence;
2. every engine-owned analysis fact required by an S-rule to have passing
   TypeScript and Go evidence, and every full OBI synthesis rule to pass in the
   later adapter phase;
3. every scenario result to match across languages;
4. adapter differentials to prove the adapters add translation only;
5. all package, race, lifecycle, cancellation, size, security, and clean-consumer
   gates to pass at one exact clean commit;
6. no unresolved P0, P1, or P2 review finding.

Compatibility with the repository's previous pre-release API is expressly not
part of the gate.
