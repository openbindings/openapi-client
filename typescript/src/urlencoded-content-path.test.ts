import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildURLEncodedBody, planRequestBodies } from "./media.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import type { OpenAPIMediaType, OpenAPIOperation } from "./types.js";

// The identical file is executed by openbindings-go/formats/openapi and by
// openapi-client/go, and carried by openbindings-ts's openapi package.
const CASES_DIGEST = "cdd4c439ff453c3b5a8c2df13edb4ed9065766ac8e1210fcfbf3c6a337c2c732";

const EDITIONS = ["3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2"];
const SHAPES = [
  "object",
  "array-of-objects",
  "array-of-strings",
  "primitive-with-space",
  "explicit-content-type",
  "explicit-style-only",
  "explicit-allow-reserved-false",
  "typeless-with-members",
  "unconstrained",
  "non-collapsing-anyOf",
];

interface ContentPathCase {
  name: string;
  openapi: string;
  shape: string;
  path: string;
  propertySchema: Record<string, unknown>;
  encoding: Record<string, unknown> | null;
  value: unknown;
  expect: string;
  basis: string;
}

function loadCases(): ContentPathCase[] {
  const raw = readFileSync(new URL("./testdata/urlencoded-content-path-cases.json", import.meta.url));
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== CASES_DIGEST) {
    throw new Error(
      `case table digest = ${digest}, want ${CASES_DIGEST} (the table is shared byte-for-byte with the twin engines)`,
    );
  }
  const table = JSON.parse(raw.toString("utf8")) as { cases: ContentPathCase[] };
  if (table.cases.length !== 80) throw new Error(`case table has ${table.cases.length} cases, want 80`);
  return table.cases;
}

function bodyMedia(c: ContentPathCase): OpenAPIMediaType {
  return {
    schema: { type: "object", properties: { p: c.propertySchema } },
    ...(c.encoding === null ? {} : { encoding: { p: c.encoding } }),
  } as unknown as OpenAPIMediaType;
}

function operation(c: ContentPathCase): OpenAPIOperation {
  return {
    requestBody: { required: true, content: { "application/x-www-form-urlencoded": bodyMedia(c) } },
  } as OpenAPIOperation;
}

// Refusal messages are each implementation's own surface, so only the decision
// itself crosses the twin boundary.
function decision(c: ContentPathCase): string {
  try {
    planRequestBodies(operation(c), { profile: OPENAPI_PROFILE_FULL, openapiVersion: c.openapi });
  } catch {
    return "refused";
  }
  let encoded: string;
  try {
    encoded = buildURLEncodedBody(bodyMedia(c), { p: c.value }, true, c.openapi, false);
  } catch {
    return "error";
  }
  return `admitted;emit=${encoded === "" ? "elided" : encoded}`;
}

// The shared 80-cell table: ten declaration shapes on each of the eight
// accepted OAS editions, each pinned to the whole emitted request body. It is
// the layer neither sibling table covers — the lane table pins WHICH path a
// shape takes, the escaper table pins WHICH CHARACTERS a path leaves literal —
// and the layer no corpus aggregate can see, because no evaluation report
// records request bytes.
describe("urlencoded content path — the twin case table", () => {
  const cases = loadCases();

  for (const c of cases) {
    it(c.name, () => {
      const got = decision(c);
      if (got !== c.expect) {
        throw new Error(`${c.name}: decision = ${got}, want ${c.expect}\nbasis: ${c.basis}`);
      }
    });
  }

  // The deleted legacyOpenAPIFormEncoding predicate stated as an executable
  // claim rather than as an absence in prose: two documents differing ONLY in
  // the patch component of their openapi field emit the same bytes, for every
  // declaration shape on both accepted lines.
  it("emits identical bytes across the patch component of a line", () => {
    const byLineAndShape = new Map<string, Set<string>>();
    for (const c of cases) {
      const line = c.openapi.slice(0, c.openapi.lastIndexOf("."));
      const key = `${line}|${c.shape}`;
      if (!byLineAndShape.has(key)) byLineAndShape.set(key, new Set());
      byLineAndShape.get(key)!.add(decision(c));
    }
    expect(byLineAndShape.size).toBe(20);
    for (const [key, decisions] of byLineAndShape) {
      expect(`${key}=${[...decisions].join(" AND ")}`).toBe(`${key}=${[...decisions][0]}`);
    }
  });

  // Which shapes may differ between the two accepted LINES, stated positively.
  // The type-absent part default is the only one: every 3.1 edition states
  // application/octet-stream for a part declaring no type and this
  // specification defines no JSON-to-octet part boundary, while no 3.0 edition
  // states a row for it and §9.2's own five-edition convention answers instead.
  it("confines every edition difference to a line split", () => {
    const typeAbsent = new Set(["typeless-with-members", "unconstrained"]);
    const byShape = new Map<string, Record<string, string>>();
    for (const c of cases) {
      const line = c.openapi.slice(0, c.openapi.lastIndexOf("."));
      const byLine = byShape.get(c.shape) ?? {};
      byLine[line] = c.expect;
      byShape.set(c.shape, byLine);
    }
    expect(byShape.size).toBe(10);
    for (const [shape, byLine] of byShape) {
      const differs = byLine["3.0"] !== byLine["3.1"];
      expect(`${shape} differs=${differs}`).toBe(`${shape} differs=${typeAbsent.has(shape)}`);
    }
    for (const shape of typeAbsent) {
      expect(byShape.get(shape)!["3.1"]).toBe("refused");
      expect(byShape.get(shape)!["3.0"]).toBe("admitted;emit=p=main+st");
    }
  });

  // A cell silently dropping out of the table is the failure mode this asserts
  // against.
  it("covers every edition and every declaration shape", () => {
    const want = new Set<string>();
    for (const edition of EDITIONS) {
      for (const shape of SHAPES) want.add(`${edition}|${shape}`);
    }
    for (const c of cases) {
      if (!want.has(c.name)) throw new Error(`case ${c.name} is not one of the covered cells`);
      want.delete(c.name);
    }
    if (want.size !== 0) throw new Error(`case table is missing ${want.size} cells: ${[...want].join(", ")}`);
  });
});
