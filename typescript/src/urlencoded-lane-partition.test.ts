import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildURLEncodedBody, planRequestBodies } from "./media.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import type { OpenAPIMediaType, OpenAPIOperation } from "./types.js";

// The identical file is executed by openbindings-go/formats/openapi and by
// openapi-client/go; changing it in one engine without the others fails here.
const CASES_DIGEST = "26ec2ca0143b630fe045e3ec8841057b2afb26e68ef4c86922bca36ef73a1d5b";

const EDITIONS = ["3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2"];
const VARIANTS = [
  "no-encoding",
  "empty-encoding",
  "content-type",
  "style",
  "explode-true",
  "explode-false",
  "allow-reserved-true",
  "allow-reserved-false",
];

interface LaneCase {
  name: string;
  openapi: string;
  variant: string;
  encoding: Record<string, unknown> | null;
  explicitControl: boolean;
  lane: string;
  expect: string;
  basis: string;
}

interface LaneTable {
  partition: Record<string, string>;
  cases: LaneCase[];
}

function loadTable(): LaneTable {
  const raw = readFileSync(new URL("./testdata/urlencoded-lane-partition-cases.json", import.meta.url));
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== CASES_DIGEST) {
    throw new Error(
      `case table digest = ${digest}, want ${CASES_DIGEST} (the table is shared byte-for-byte with the two Go engines)`,
    );
  }
  const table = JSON.parse(raw.toString("utf8")) as LaneTable;
  if (table.cases.length === 0) throw new Error("case table is empty");
  return table;
}

function bodyMedia(c: LaneCase): OpenAPIMediaType {
  return {
    schema: {
      type: "object",
      properties: { address: { type: "object", properties: { street: { type: "string" } } } },
    },
    ...(c.encoding === null ? {} : { encoding: { address: c.encoding } }),
  } as unknown as OpenAPIMediaType;
}

function operation(c: LaneCase): OpenAPIOperation {
  return {
    requestBody: { required: true, content: { "application/x-www-form-urlencoded": bodyMedia(c) } },
  } as OpenAPIOperation;
}

// Refusal messages are each implementation's own surface, so only the decision
// itself crosses the twin boundary.
function decision(c: LaneCase): string {
  try {
    planRequestBodies(operation(c), { profile: OPENAPI_PROFILE_FULL, openapiVersion: c.openapi });
  } catch {
    return "refused";
  }
  let encoded: string;
  try {
    encoded = buildURLEncodedBody(bodyMedia(c), { address: { street: "main st" } }, true, c.openapi, false);
  } catch {
    return "error";
  }
  return `admitted;emit=${encoded === "" ? "elided" : encoded}`;
}

// The partition this table pins is edition-keyed and comes from upstream
// authority text: 3.0.4 and the 3.1 line each state that an explicitly defined
// style, explode or allowReserved makes the Encoding Object's contentType
// ignored, which is meaningful only because contentType otherwise governs;
// 3.0.4 adds that with all three absent "Encoding is to be based on contentType
// alone". No 3.0.0-3.0.3 text says either thing. See each case's "basis".
describe("urlencoded edition-to-lane partition — the twin case table", () => {
  const table = loadTable();

  for (const c of table.cases) {
    it(c.name, () => {
      const got = decision(c);
      if (got !== c.expect) {
        throw new Error(`${c.name}: decision = ${got}, want ${c.expect}\nbasis: ${c.basis}`);
      }
    });
  }

  // The partition stated as a claim in its own right, against the table's own
  // literal edition->lane map, so a silent drift in legacyOpenAPIFormEncoding
  // cannot pass by moving the expectations with it.
  it("matches the literal edition-to-lane table", () => {
    const want: Record<string, string> = {
      "3.0.0": "style",
      "3.0.1": "style",
      "3.0.2": "style",
      "3.0.3": "style",
      "3.0.4": "content",
      "3.1.0": "content",
      "3.1.1": "content",
      "3.1.2": "content",
    };
    expect(Object.keys(table.partition).sort()).toEqual(Object.keys(want).sort());
    for (const [edition, lane] of Object.entries(want)) {
      expect(`${edition}=${table.partition[edition]}`).toBe(`${edition}=${lane}`);
    }
    for (const c of table.cases) {
      const expected = c.explicitControl ? "style" : want[c.openapi];
      if (c.lane !== expected) {
        throw new Error(
          `${c.name}: lane = ${c.lane}, want ${expected} (${c.explicitControl ? "an explicit RFC6570-style control is written" : "no RFC6570-style control is written, so the edition decides"})`,
        );
      }
    }
  });

  // A cell silently dropping out of the table is the failure mode this asserts
  // against.
  it("covers every edition and every Encoding Object shape", () => {
    const want = new Set<string>();
    for (const edition of EDITIONS) {
      for (const variant of VARIANTS) want.add(`${edition}|${variant}`);
    }
    for (const c of table.cases) {
      if (!want.has(c.name)) throw new Error(`case ${c.name} is not one of the covered cells`);
      want.delete(c.name);
    }
    expect([...want]).toEqual([]);
  });
});
