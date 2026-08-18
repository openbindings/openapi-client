import { describe, expect, it } from "vitest";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { planRequestBodies, styleLaneUndefinedExpansionMember } from "./media.js";
import { effectiveParameters, styleLaneUndefinedExpansionParam } from "./params.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import type { OpenAPIOperation, OpenAPIPathItem } from "./types.js";

/**
 * The style-lane composite-member case table is SHARED, byte-for-byte, with
 * three other engines: `openbindings-go/formats/openapi`, `openapi-client/go`
 * and `openbindings-ts/packages/openapi`. Each cell pins the ADMISSION
 * decision all four must reach for one style-lane declaration, so a divergence
 * in any one of them fails the others' suites.
 *
 * This engine ships no synthesizer, so it executes the cells through the
 * shipped admission predicates themselves — `styleLaneUndefinedExpansionParam`
 * for a parameter cell and `planRequestBodies` for a body cell — and it is one
 * of the two engines that additionally assert the MEMBER the predicate names,
 * which the coverage-level assertion in the two synthesizing engines cannot
 * see.
 *
 * Authority: `styleLaneUndefinedExpansionMember` in `media.ts` reads the style
 * table per edition. Package:
 * `design/openapi-style-lane-composite-member-ruling.md`, RULED 2026-08-18.
 */
const CASES_DIGEST = "1ea1045c75039b00c1035a2e2c3d09e440644e32a5fa1c3689be6add1eac7673";

interface StyleLaneCase {
  readonly name: string;
  readonly openapi: string;
  readonly position: "parameter" | "body";
  readonly in?: string;
  readonly style?: string;
  readonly explode?: boolean;
  readonly media?: string;
  readonly encoding: Record<string, unknown> | null;
  readonly schema: Record<string, unknown> | null;
  readonly expect: "admitted" | "refused";
  readonly member: string | null;
  readonly basis: string;
}

const raw = readFileSync(new URL("./testdata/style-lane-composite-member-cases.json", import.meta.url));
const digest = createHash("sha256").update(raw).digest("hex");
const table = JSON.parse(raw.toString("utf8")) as { cases: readonly StyleLaneCase[] };

/**
 * Renders one cell as a WHOLE OpenAPI document, byte-corresponding with the
 * twin engines' renderer.
 */
function document(c: StyleLaneCase): Record<string, unknown> {
  let paths: Record<string, unknown>;
  if (c.position === "parameter") {
    const parameter: Record<string, unknown> = { name: "filter", in: c.in };
    if (c.style !== undefined) parameter["style"] = c.style;
    if (c.explode !== undefined) parameter["explode"] = c.explode;
    if (c.schema !== null) parameter["schema"] = c.schema;
    else {
      parameter["content"] = {
        "application/json": { schema: { type: "object", properties: { where: { type: "object" } } } },
      };
    }
    let template = "/q";
    if (c.in === "path") {
      template = "/q/{filter}";
      parameter["required"] = true;
    }
    paths = {
      [template]: {
        get: { operationId: "query", parameters: [parameter], responses: { "200": { description: "ok" } } },
      },
    };
  } else {
    const media: Record<string, unknown> = {
      schema: { type: "object", properties: { field: c.schema } },
    };
    if (c.encoding !== null) media["encoding"] = { field: c.encoding };
    paths = {
      "/form": {
        post: {
          operationId: "postForm",
          requestBody: { content: { [c.media!]: media } },
          responses: { "200": { description: "ok" } },
        },
      },
    };
  }
  return {
    openapi: c.openapi,
    info: { title: "style lane composite member case table", version: "1.0.0" },
    servers: [{ url: "https://api.example.test" }],
    paths,
  };
}

describe("style-lane composite-member case table", () => {
  it("is byte-identical to the copies the twin engines execute", () => {
    expect(digest).toBe(CASES_DIGEST);
    expect(table.cases.length).toBeGreaterThan(0);
  });

  for (const testCase of table.cases) {
    it(testCase.name, () => {
      const doc = document(testCase);
      const is30 = testCase.openapi.startsWith("3.0");
      const paths = doc["paths"] as Record<string, OpenAPIPathItem>;

      if (testCase.position === "parameter") {
        const template = testCase.in === "path" ? "/q/{filter}" : "/q";
        const pathItem = paths[template]!;
        const op = pathItem.get as OpenAPIOperation;
        const member = styleLaneUndefinedExpansionParam(
          effectiveParameters(pathItem, op),
          OPENAPI_PROFILE_FULL,
          is30,
        );
        expect(member === null ? "admitted" : "refused").toBe(testCase.expect);
        expect(member).toBe(testCase.member);
        return;
      }

      const op = (paths["/form"] as OpenAPIPathItem).post as OpenAPIOperation;
      let decision = "admitted";
      try {
        const plans = planRequestBodies(op, { profile: OPENAPI_PROFILE_FULL, openapiVersion: testCase.openapi });
        if (plans.length === 0) decision = "refused";
      } catch {
        decision = "refused";
      }
      expect(decision).toBe(testCase.expect);

      // A cell with no Encoding Object is on the CONTENT path, where this
      // predicate is never consulted, so only the style-lane cells assert it.
      if (testCase.encoding === null) return;
      expect(styleLaneUndefinedExpansionMember(testCase.schema, is30)).toBe(testCase.member);
    });
  }
});
