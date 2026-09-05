// Executes the shared acceptance-floor case table (block 8d-1): the 8 policy
// mechanism fixtures (block 8b) and the 66-cell OAS shape table, with
// expectations updated for the published smallest-owner response confinement
// and addressability algebra. The same table file, at the same digest, embeds
// in the standalone Go and TypeScript engines. OpenBindings integration moves
// to this answer after the standalone engine contract is frozen.

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { computeAcceptanceFloor } from "./acceptance-floor.js";

// The embedded table's own digest, and the digest of the shape table it was
// generated from. A change to either is a change to the shared answer and
// must land in every engine simultaneously.
const CASE_TABLE_SHA256 = "b7cd7bf071efe73d6675f9b2fee9bd1afe9be3658403397bf73e56ceb34277fa";
const SHAPE_TABLE_SHA256 = "4e8f5393e48868e2a9468d7232921e1c2f3b33efd941f605b9e328b23191d456";

interface CaseExpect {
  refuses: boolean;
  operationsRepresented: number;
  operationsInvalid: number;
  operationsExcludedByRequestMedia: number;
  invalidRequestAlternatives: number;
  projectionEntriesOnReachingUnits: number;
  dispositions: Record<string, string>;
}

interface CaseTable {
  generatedFrom: { shapeTableSha256: string };
  mechanisms: Array<{ name: string; doc: unknown; expect: CaseExpect }>;
  shapeCells: Array<{ id: string; doc: unknown; expect: CaseExpect }>;
}

const tablePath = fileURLToPath(new URL("../testdata/acceptance-floor-case-table.json", import.meta.url));
const tableBytes = readFileSync(tablePath);
const table: CaseTable = JSON.parse(tableBytes.toString("utf8"));

function assertCase(name: string, doc: unknown, expected: CaseExpect): void {
  const floor = computeAcceptanceFloor(doc);
  expect(floor, `${name}: floor applicable`).toBeDefined();
  expect(floor!.refusal !== "", `${name}: refuses`).toBe(expected.refuses);
  let represented = 0;
  let invalid = 0;
  let excluded = 0;
  let invalidAlts = 0;
  let onReaching = 0;
  for (const ref of floor!.opOrder) {
    const op = floor!.ops.get(ref)!;
    if (op.disposition === "represented") represented++;
    else if (op.disposition === "invalid") invalid++;
    else if (op.disposition === "excluded-request-media") excluded++;
    invalidAlts += op.invalidAlternatives.size;
    for (const ds of op.projections.values()) {
      for (const d of ds) if (d.class === "D6" || d.class === "D11") onReaching++;
    }
    const want = expected.dispositions[ref];
    if (want !== undefined) expect(op.disposition, `${name}: ${ref}`).toBe(want);
  }
  expect(represented, `${name}: represented`).toBe(expected.operationsRepresented);
  expect(invalid, `${name}: invalid`).toBe(expected.operationsInvalid);
  expect(excluded, `${name}: excluded-request-media`).toBe(expected.operationsExcludedByRequestMedia);
  expect(invalidAlts, `${name}: invalid request alternatives`).toBe(expected.invalidRequestAlternatives);
  expect(onReaching, `${name}: reaching-unit projections`).toBe(expected.projectionEntriesOnReachingUnits);
  expect(floor!.opOrder.length, `${name}: raw inventory`).toBe(Object.keys(expected.dispositions).length);
}

describe("acceptance-floor shared case table", () => {
  it("carries the pinned digests", () => {
    const digest = createHash("sha256").update(tableBytes).digest("hex");
    expect(digest).toBe(CASE_TABLE_SHA256);
    expect(table.generatedFrom.shapeTableSha256).toBe(SHAPE_TABLE_SHA256);
  });

  it("reproduces the 8 policy mechanism fixtures", () => {
    expect(table.mechanisms).toHaveLength(8);
    for (const m of table.mechanisms) assertCase(m.name, m.doc, m.expect);
  });

  it("reproduces the 66-cell shape table", () => {
    expect(table.shapeCells).toHaveLength(68);
    for (const c of table.shapeCells) assertCase(c.id, c.doc, c.expect);
  });
});
