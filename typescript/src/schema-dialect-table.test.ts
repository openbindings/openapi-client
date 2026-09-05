// Executes the shared Schema Object dialect case table
// (testdata/schema-object-dialect-cases.json) through the shipped acceptance
// floor. The same file, at the same digest, embeds in the standalone Go and
// TypeScript engines. OpenBindings integration migrates after their public
// contract is frozen.
//
// Each cell places one Schema Object at a success response's only media
// alternative beside a clean sibling operation, so every cell asserts three
// things at once: which positions the governing dialect finds defective, which
// class owns each position, and that the confinement stays inside the unit that
// earned it.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { computeAcceptanceFloor } from "./acceptance-floor.js";

// The embedded table's own digest. A change here is a change to the shared
// answer and must land in every engine simultaneously.
const SCHEMA_OBJECT_DIALECT_TABLE_SHA256 = "3f455cbd34904fa90a002b0276816c5ed0a9d527c8bbd05bb5e7e1d4e5479803";

export interface SchemaDialectPosition {
  position: string;
  class: string;
}

export interface SchemaDialectCell {
  id: string;
  line: "3.0" | "3.1";
  openapi: string;
  schema: Record<string, unknown>;
  positions: SchemaDialectPosition[];
  disposition: "represented" | "invalid";
  downstream: "coverage" | "obi-invalid" | "go-loader-refusal";
  downstreamNote?: string;
  why: string;
}

export const SCHEMA_DIALECT_SUBJECT_REF = "#/paths/~1a/get";
export const SCHEMA_DIALECT_SCHEMA_PTR = "#/paths/~1a/get/responses/200/content/application~1json/schema";
export const SCHEMA_DIALECT_CLEAN_REF = "#/paths/~1b/get";

/** Loads the shared table from `dir`, refusing a copy that has drifted. */
export function loadSchemaDialectTable(path: string): SchemaDialectCell[] {
  const raw = readFileSync(path, "utf8");
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== SCHEMA_OBJECT_DIALECT_TABLE_SHA256) {
    throw new Error(
      `shared dialect case table digest ${digest}, pinned ${SCHEMA_OBJECT_DIALECT_TABLE_SHA256}: the table changed without a simultaneous four-engine landing`,
    );
  }
  const table = JSON.parse(raw) as { cells: SchemaDialectCell[] };
  if (!table.cells?.length) throw new Error("shared dialect case table carries no cells");
  return table.cells;
}

/**
 * The one document shape every engine builds for a cell: the cell's Schema
 * Object as the sole media alternative of the subject operation's success
 * response, beside a clean sibling.
 */
export function schemaDialectDocument(cell: SchemaDialectCell): Record<string, unknown> {
  const response = (schema: unknown) => ({
    "200": { description: "ok", content: { "application/json": { schema } } },
  });
  return {
    openapi: cell.openapi,
    info: { title: "schema object dialect", version: "1" },
    paths: {
      "/a": { get: { responses: response(cell.schema) } },
      "/b": { get: { responses: response({ type: "object" }) } },
    },
  };
}

const cells = loadSchemaDialectTable(new URL("./testdata/schema-object-dialect-cases.json", import.meta.url).pathname);

describe("the shared Schema Object dialect case table", () => {
  for (const cell of cells) {
    it(cell.id, () => {
      const floor = computeAcceptanceFloor(schemaDialectDocument(cell));
      expect(floor, `no floor for edition ${cell.openapi}`).toBeTruthy();
      expect(floor!.line).toBe(cell.line);
      expect(floor!.refusal, "a confined defect refused the whole source").toBe("");

      const subject = floor!.ops.get(SCHEMA_DIALECT_SUBJECT_REF);
      expect(subject, `no verdict for ${SCHEMA_DIALECT_SUBJECT_REF}`).toBeTruthy();
      expect(subject!.disposition, cell.why).toBe(cell.disposition);

      // Defective response positions stay on the represented operation's
      // smallest-owner projections.
      const want = cell.positions.map((p) => `${p.class} ${SCHEMA_DIALECT_SCHEMA_PTR}${p.position}`).sort();
      const got = [...subject!.projections.values()].flat().map((d) => `${d.class} ${d.position}`).sort();
      expect(got, cell.why).toEqual(want);

      // Confinement: the clean sibling never pays for the cell's defect.
      const clean = floor!.ops.get(SCHEMA_DIALECT_CLEAN_REF);
      expect(clean?.disposition, "the clean sibling operation lost its target").toBe("represented");
      expect(clean?.defects ?? [], "the clean sibling picked up a defect").toEqual([]);
    });
  }
});
