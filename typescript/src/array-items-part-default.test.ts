import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildMultipartBody, buildURLEncodedBody, planRequestBodies } from "./media.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import type { OpenAPIDocument, OpenAPIMediaType, OpenAPIOperation } from "./types.js";

// The identical file is executed by openbindings-go/formats/openapi and by
// openapi-client/go; changing it in one engine without the others fails here.
const CASES_DIGEST = "16ac8ae3c08e081b82c1f6d9a7ffebefe1a215292eded8dea677a6b63a561be0";

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
  const doc = { openapi: c.openapi } as OpenAPIDocument;
  const fields = { [c.propertyName]: c.value };
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
