import { describe, expect, it } from "vitest";
import { followsPointerBelowReference } from "./util.js";

/**
 * The edition partition for reference traversal, as a literal table over all
 * eight accepted editions, so a change to it is a change to something visible
 * rather than to a switch statement's default arm. The Go twin is
 * `TestPointerBelowReferenceEditionPartition` in both Go engines.
 *
 * The authorities per edition are quoted on `followsPointerBelowReference` in
 * `util.ts` and re-derived at the pinned bytes by
 * `corpus-lab/scripts/verify-pointer-below-reference-authorities.mjs`. The
 * end-to-end verdicts are executed from the shared twin case table in
 * `openbindings-ts/packages/openapi/src/external-composition.test.ts`.
 */
describe("reference traversal is decided per edition line", () => {
  const partition: ReadonlyArray<readonly [string, boolean]> = [
    ["3.0.0", true], // JSON Reference incorporated for $ref processing
    ["3.0.1", true],
    ["3.0.2", true],
    ["3.0.3", true],
    ["3.0.4", true],
    ["3.1.0", false], // fragment is a JSON-Pointer over the referenced document
    ["3.1.1", false],
    ["3.1.2", false],
  ];

  for (const [edition, follows] of partition) {
    it(`${follows ? "follows" : "refuses"} under OAS ${edition}`, () => {
      expect(followsPointerBelowReference(edition)).toBe(follows);
    });
  }

  it("leaves an unaccepted edition to OAPI-P-01's own diagnostic", () => {
    for (const edition of ["", "2.0", "3.0.5", "3.1.3", "3.2.0"]) {
      expect(followsPointerBelowReference(edition), edition).toBe(true);
    }
  });
});
