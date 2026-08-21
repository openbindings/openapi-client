// The two guards the split in schema-dialect.ts owes — the byte twin of
// `schema_dialect_test.go`.
//
// 1. The 3.1 line DELEGATES, so what must be guarded is the ARTIFACT: the
//    vendored dialect bytes are the published ones, verbatim.
// 2. The 3.0 line TRANSCRIBES, so what must be guarded is the CELL SET: the
//    transcription answers for exactly the four keywords whose sentences the
//    accepted 3.0 editions state, and for nothing else. The sentences
//    themselves are checked against the pinned edition bytes by
//    `spec/scripts/verify-openapi-30-schema-object-transcription.mjs`, which is
//    where those bytes live.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { schemaObjectDefects } from "./schema-dialect.js";

// The published digests of the artifacts the 3.1 verdict is delegated to, and
// of the 2020-12 meta-schemas the dialect composes. Re-fetch and re-digest to
// check: these are byte copies, never paraphrases.
const VENDORED_ARTIFACTS: Record<string, string> = {
  "oas-3.1-dialect-base.json": "8a0e89e365dadbebce2921ce6244340c1090e9d544c60d977e9ad6b97a61227b",
  "oas-3.1-meta-base.json": "267a88226e64e96dfc8c89dbd7e863160c84715e0fb893ca1d9fbf9f830f1f54",
  "json-schema-2020-12.json": "41da76f5afb7ce062d248f762463a92f7ca47e4e0f905b224ba6afeef91ded0f",
  "json-schema-2020-12-meta-core.json": "21f79d143fab1f180245c331e5657057045b36794d41fe151e6e4fed65035299",
  "json-schema-2020-12-meta-applicator.json": "bf273b26f9f735b93ece78f2b61b36676e1d122ce78ab37ad5a2e45dfa1ca2b1",
  "json-schema-2020-12-meta-validation.json": "e921c5b79264d3689af01c1af1ffdf692e09f1c45df90a0f08eb7288c9acdeab",
  "json-schema-2020-12-meta-meta-data.json": "c664d438a84d58889c8edecd248ce2f945a4bc0e3b087323b11303dc136abfbe",
  "json-schema-2020-12-meta-format-annotation.json": "5c79404f831dd905c0f40fefac7c6f3e51bf3729b4a876a5c2020178d97f3bcc",
  "json-schema-2020-12-meta-content.json": "a10456605b2b5bb12a1b4dcfc0300f02f54d3e8bb3646bed7724583866627682",
  "json-schema-2020-12-meta-unevaluated.json": "fc99f32188da41689a9382af174dd42e8b255e4374965c157b8286556b4ab2bc",
};

describe("the vendored authority artifacts", () => {
  for (const [name, want] of Object.entries(VENDORED_ARTIFACTS)) {
    it(name, () => {
      const bytes = readFileSync(new URL(`./authority/${name}`, import.meta.url));
      const got = createHash("sha256").update(bytes).digest("hex");
      expect(got, `the delegated artifact is not the authority's bytes`).toBe(want);
    });
  }
});

/**
 * The transcription's whole surface, exercised at once: a node carrying every
 * cell's trigger plus a spread of keyword values the 3.0 line does NOT judge. A
 * cell added to the 3.0 branch without a grounded sentence shows up here as an
 * extra position.
 */
const PROBE: Record<string, unknown> = {
  // The four grounded cells, each triggered.
  required: ["a", "a"],
  enum: "not-a-list",
  items: [{ type: "string" }],
  properties: { f: true },
  // Positions this line's own dialect states nothing about, or states the
  // opposite of the 3.1 line about. None may be judged here.
  exclusiveMinimum: true,
  exclusiveMaximum: true,
  minItems: -1,
  pattern: 42,
  multipleOf: 0,
  $recursiveAnchor: true,
  contains: 3,
  nullable: true,
  min_items: 3,
  unknownKeyword: {},
};

describe("the 3.0 transcription", () => {
  it("answers exactly its four grounded cells", () => {
    expect(schemaObjectDefects(PROBE, "3.0")).toEqual(["/enum", "/items", "/properties/f", "/required"]);
  });

  it("differs from the 3.1 line because the DIALECT differs, not because we chose", () => {
    const on31 = schemaObjectDefects(PROBE, "3.1");
    for (const position of ["/$recursiveAnchor", "/exclusiveMinimum", "/minItems", "/pattern", "/multipleOf", "/contains"]) {
      expect(on31, `the delegated 3.1 verdict does not reach ${position}`).toContain(position);
    }
    for (const legal of ["/nullable", "/min_items", "/unknownKeyword"]) {
      expect(on31, `the delegated 3.1 verdict refuses ${legal}, which the dialect admits as an annotation`)
        .not.toContain(legal);
    }
  });
});
