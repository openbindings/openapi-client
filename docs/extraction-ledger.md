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
| Portable processor corpus | pinned and complete | all 888 pinned scenarios pass unchanged through both public clients |
| Engine-owned analysis | deferred | next native phase adds immutable dispositions and prerequisites in both languages before adapters |
| Portable OBI synthesis | adapter phase | the later adapter derives all 154 OpenAPI synthesis scenarios from engine analysis |
| Cross-language parity | candidate-complete | normalized observations match on all 888 corrected portable invocation scenarios |
| OpenBindings adapters | deferred | thin bridges pass differential tests with no wire logic |
| SDK and OB CLI | deferred | consumers migrated to the accepted native substrate |
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
