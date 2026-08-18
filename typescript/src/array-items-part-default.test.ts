import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildMultipartBody, buildURLEncodedBody, planRequestBodies } from "./media.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import type { OpenAPIDocument, OpenAPIMediaType, OpenAPIOperation } from "./types.js";

// The identical file is executed by openbindings-go/formats/openapi and by
// openapi-client/go; changing it in one engine without the others fails here.
const CASES_DIGEST = "ab97e415bb05951f406037c1850878d65beb6f9c8457ead87c51cfe0ff88be12";

interface PartDefaultCase {
  name: string;
  openapi: string;
  media: string;
  items: string;
  itemsSchema: Record<string, unknown>;
  propertyName: string;
  value: unknown[];
  derivedFrom: string;
  expect: string;
  writeLane?: string;
  nonArrayValue?: unknown;
  nonArrayValueExpect?: string;
  basis: string;
}

function loadCases(): PartDefaultCase[] {
  const raw = readFileSync(new URL("./testdata/array-items-part-default-cases.json", import.meta.url));
  const digest = createHash("sha256").update(raw).digest("hex");
  if (digest !== CASES_DIGEST) {
    throw new Error(
      `case table digest = ${digest}, want ${CASES_DIGEST} (the table is shared byte-for-byte with the two Go engines)`,
    );
  }
  const table = JSON.parse(raw.toString("utf8")) as { cases: PartDefaultCase[] };
  if (table.cases.length === 0) throw new Error("case table is empty");
  return table.cases;
}

function bodyMedia(c: PartDefaultCase): OpenAPIMediaType {
  return {
    schema: {
      type: "object",
      properties: { [c.propertyName]: { type: "array", items: c.itemsSchema } },
    },
  } as OpenAPIMediaType;
}

function operation(c: PartDefaultCase): OpenAPIOperation {
  return {
    requestBody: { required: true, content: { [c.media]: bodyMedia(c) } },
  } as OpenAPIOperation;
}

async function emission(c: PartDefaultCase): Promise<string> {
  return rendering(c, { [c.propertyName]: c.value });
}

// The emission renderer with the supplied fields as a parameter, so one
// rendering serves both the table's declared array value and its
// nonArrayValue row.
async function rendering(c: PartDefaultCase, fields: Record<string, unknown>): Promise<string> {
  const doc = { openapi: c.openapi } as OpenAPIDocument;
  try {
    if (c.media === "application/x-www-form-urlencoded") {
      const encoded = buildURLEncodedBody(bodyMedia(c), fields, true, c.openapi, false);
      return encoded === "" ? "elided" : encoded;
    }
    const form = buildMultipartBody(doc, bodyMedia(c), fields, true, false);
    const rendered: string[] = [];
    for (const entry of form.getAll(c.propertyName)) {
      if (typeof entry === "string") {
        // A bare FormData string field emits a part with NO Content-Type
        // header, which [RFC7578] Section 4.4 makes the same wire fact as an
        // explicit text/plain: "Each part MAY have an (optional)
        // \"Content-Type\" header field, which defaults to \"text/plain\"".
        // The Go twins emit the header; both spellings are inside the
        // permitted set, so the rendering normalizes them together.
        rendered.push(`text/plain:${entry}`);
      } else {
        rendered.push(`${entry.type}:${await entry.text()}`);
      }
    }
    return rendered.length === 0 ? "elided" : rendered.join("&");
  } catch {
    return "error";
  }
}

// Refusal messages are each implementation's own surface, so only the decision
// itself crosses the twin boundary.
async function decision(c: PartDefaultCase): Promise<string> {
  try {
    planRequestBodies(operation(c), { profile: OPENAPI_PROFILE_FULL, openapiVersion: c.openapi });
  } catch {
    return "refused";
  }
  return `admitted;emit=${await emission(c)}`;
}

// The multipart expectations come from upstream authority text: every accepted
// OAS edition derives an array property's default part Content-Type from the
// ITEMS schema, never from the array. The urlencoded content-lane cells are
// pinned AS OBSERVED and carry basis "OPEN" — see the table's own $comment and
// corpus-lab/OPENAPI-RUNTIME.md.
describe("array-items part default — the twin case table", () => {
  const cases = loadCases();

  for (const c of cases) {
    it(c.name, async () => {
      const got = await decision(c);
      if (got !== c.expect) {
        throw new Error(`${c.name}: decision = ${got}, want ${c.expect}\nbasis: ${c.basis}`);
      }
    });
  }

  // Runs every multipart cell a second time with a value that is not an array.
  //
  // openbindings.openapi@1 §9.2 says of the form lanes: "An invalid value or a
  // member for which the resolved schema leaves no faithful form carriage
  // refuses before dispatch." An array property's multipart carriage is one
  // part per element, so a value with no elements has no faithful carriage at
  // all. The engines used to fall through to the WHOLE-property schema and send
  // one application/json part carrying the invalid value — a part the
  // declaration never described, emitted silently.
  let nonArrayCells = 0;
  for (const c of cases) {
    if (c.nonArrayValueExpect === undefined) continue;
    nonArrayCells += 1;
    it(`${c.name} — non-array value`, async () => {
      expect(c.media).toBe("multipart/form-data");
      let got = "refused";
      try {
        planRequestBodies(operation(c), { profile: OPENAPI_PROFILE_FULL, openapiVersion: c.openapi });
        got = `admitted;emit=${await rendering(c, { [c.propertyName]: c.nonArrayValue })}`;
      } catch {
        got = "refused";
      }
      if (got !== c.nonArrayValueExpect) {
        throw new Error(
          `${c.name}: decision with a non-array value = ${got}, want ${c.nonArrayValueExpect}\nbasis: ${c.basis}`,
        );
      }
    });
  }

  it("carries a non-array-value decision for every multipart cell", () => {
    expect(nonArrayCells).toBe(24);
  });

  // Executes the body-encoding lane DIRECTLY, bypassing media admission, for
  // every cell whose table row carries a `writeLane` decision.
  //
  // Admission and encoding are two lanes reading one declaration. Where
  // admission refuses, nothing selects the plan, so the encoder's own answer is
  // invisible on the wire — which is precisely why it went unasserted and why
  // the two lanes were free to disagree. This measures the encoder without
  // admission in front of it, so "unreachable" is a property of the code rather
  // than of what happens to run first.
  let writeLaneCells = 0;
  for (const c of cases) {
    if (c.writeLane === undefined) continue;
    writeLaneCells += 1;
    it(`${c.name} — write lane`, () => {
      expect(c.media).toBe("multipart/form-data");
      const doc = { openapi: c.openapi } as OpenAPIDocument;
      let got = "admitted";
      try {
        buildMultipartBody(doc, bodyMedia(c), { [c.propertyName]: c.value }, true, false);
      } catch {
        got = "refused";
      }
      if (got !== c.writeLane) {
        throw new Error(
          `${c.name}: write-lane decision = ${got}, want ${c.writeLane} (admission decides ${c.expect})\nbasis: ${c.basis}`,
        );
      }
    });
  }

  // The guard cannot be removed by deleting a table field instead of a test.
  it("pins both lanes for every multipart nested-array cell", () => {
    let seen = 0;
    for (const c of cases) {
      if (c.media !== "multipart/form-data" || c.items !== "nested-array") continue;
      seen += 1;
      if (c.expect !== "refused" || c.writeLane !== "refused") {
        throw new Error(
          `${c.name}: expect = ${c.expect}, writeLane = ${c.writeLane}; both lanes must refuse a nested array declaration`,
        );
      }
    }
    expect(seen).toBe(3);
    expect(writeLaneCells).toBe(3);
  });

  // The invariant the table exists for, stated as a claim in its own right: on
  // the multipart lane an array property carries its ITEMS schema's default,
  // which for every items kind but `object` is exactly what an array-keyed
  // default (application/json) would not produce.
  it("keys the multipart part default on the items schema, not on the array", () => {
    let seen = 0;
    for (const c of cases) {
      if (c.media !== "multipart/form-data" || c.derivedFrom !== "items") continue;
      seen += 1;
      if (c.expect.includes("application/json") && c.items !== "object") {
        throw new Error(
          `${c.name} expects an application/json part, but its items schema is ${c.items}; only an object items schema reaches that OAS row`,
        );
      }
    }
    expect(seen).toBe(24);
  });

  // Before this table the invariant was guarded only incidentally, by cells of
  // tables about other questions, so a cell silently dropping out of this one is
  // the failure mode this asserts against.
  it("covers every edition branch and items kind", () => {
    const want = new Set<string>();
    for (const edition of ["3.0.3", "3.0.4", "3.1.1"]) {
      for (const media of ["multipart", "urlencoded"]) {
        for (
          const items of [
            "string",
            "integer",
            "object",
            "nested-array",
            "unconstrained",
            "string-base64",
            "union-type",
            "union-choice",
          ]
        ) {
          want.add(`${edition}|${media}|${items}`);
        }
      }
    }
    for (const c of cases) {
      if (!want.has(c.name)) throw new Error(`case ${c.name} is not one of the covered cells`);
      want.delete(c.name);
    }
    expect([...want]).toEqual([]);
  });
});
