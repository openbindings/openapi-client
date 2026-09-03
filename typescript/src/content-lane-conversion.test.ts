import { describe, expect, it } from "vitest";
import { OpenAPIEngine } from "./engine.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import {
  planResolvedRequestBodies,
  prepareEncodingStylePropertyValue,
  prepareResolvedPropertyMediaView,
  requiredPropertyMediaNames,
} from "./resolved-media.js";
import type { OpenAPIOperation } from "./types.js";

// openbindings.openapi-3.0@1 Section 8.1 names `parameterConversion` for a
// Section 9.3 form or part property only where that property "must convert a
// JSON scalar to a string", and Section 9.3 routes a content-based property
// through Section 9.2's lane for its selected media type. The text/plain lane
// is therefore the converter's only content-lane site; the JSON lane
// serializes the supplied value as strict JSON and never consults it. Until
// 2026-09-03 `prepareEncodingStylePropertyValue` -- the preparation the
// @openbindings/openapi adapter runs on every caller-envelope body member --
// converted by declaration, so an integer bound for application/json reached
// the wire as its converted STRING (`["1","2"]`, `"true"`). The converter here
// is deliberately visible -- `n` + the scalar's own spelling -- so a converted
// scalar cannot be mistaken for its JSON image.
const REF = "#/paths/~1form/post";

function operation(mediaType: string, encoding?: Record<string, unknown>): OpenAPIOperation {
  return {
    requestBody: {
      required: true,
      content: {
        [mediaType]: {
          schema: {
            type: "object",
            properties: {
              ids: { type: "array", items: { type: "integer" } },
              flag: { type: "boolean" },
              count: { type: "integer" },
            },
          },
          ...(encoding ? { encoding } : {}),
        },
      },
    },
    responses: { "204": { description: "ok" } },
  } as OpenAPIOperation;
}

function document(mediaType: string, encoding?: Record<string, unknown>): Record<string, unknown> {
  return {
    openapi: "3.0.4",
    info: { title: "content lane", version: "1" },
    servers: [{ url: "https://example.test" }],
    paths: { "/form": { post: operation(mediaType, encoding) } },
  };
}

const JSON_ENCODING = {
  ids: { contentType: "application/json" },
  flag: { contentType: "application/json" },
};
const visibleConverter = (value: boolean | number): string => "n" + String(value);

function plan(mediaType: string, encoding?: Record<string, unknown>, propertyMedia?: Record<string, string>) {
  const plans = planResolvedRequestBodies(operation(mediaType, encoding), {
    profile: OPENAPI_PROFILE_FULL,
    openapiVersion: "3.0.4",
  });
  prepareResolvedPropertyMediaView(plans, propertyMedia);
  return plans[0]!;
}

describe("OAS 3.0 content-lane conversion scope", () => {
  for (const mediaType of ["application/x-www-form-urlencoded", "multipart/form-data"]) {
    it(`converts only the text/plain property of a ${mediaType} body`, () => {
      const p = plan(mediaType, JSON_ENCODING);
      // Explicit Encoding contentType application/json: the JSON lane, untouched.
      expect(prepareEncodingStylePropertyValue(p, "ids", [1, 2], true, visibleConverter)).toEqual([1, 2]);
      expect(prepareEncodingStylePropertyValue(p, "flag", true, true, visibleConverter)).toBe(true);
      // No Encoding Object: the default table gives text/plain, Section 8.1's one site.
      expect(prepareEncodingStylePropertyValue(p, "count", 7, true, visibleConverter)).toBe("n7");
      expect(() => prepareEncodingStylePropertyValue(p, "count", 7, true, undefined)).toThrow(/parameterConversion/u);
      // The JSON lane needs no converter at all.
      expect(prepareEncodingStylePropertyValue(p, "flag", true, true, undefined)).toBe(true);
    });
  }

  it("leaves an array untouched once the propertyMedia choice selects application/json", () => {
    // R4: on the urlencoded lane an integer array's item-type default (text/plain)
    // defines no container bytes, so the property requires the choice; the chosen
    // lane, not the declaration, decides whether the converter runs.
    const p = plan("application/x-www-form-urlencoded", undefined, { ids: "application/json" });
    expect(requiredPropertyMediaNames(p)).toEqual(["ids"]);
    expect(prepareEncodingStylePropertyValue(p, "ids", [1, 2], true, visibleConverter)).toEqual([1, 2]);
  });

  it("does not consult the converter on the 3.1 line's content path at all", () => {
    const plans = planResolvedRequestBodies(operation("multipart/form-data"), {
      profile: OPENAPI_PROFILE_FULL,
      openapiVersion: "3.1.2",
    });
    expect(prepareEncodingStylePropertyValue(plans[0]!, "count", 7, false, visibleConverter)).toBe(7);
  });

  it("carries JSON-lane properties as their JSON images through the standalone engine", async () => {
    let captured: RequestInit | undefined;
    const prepared = await new OpenAPIEngine({ parameterConverter: visibleConverter }).prepare({
      source: { content: document("application/x-www-form-urlencoded", JSON_ENCODING) },
      ref: REF,
      fetch: async (_input, init) => {
        captured = init;
        return new Response(null, { status: 204 });
      },
    });
    const execution = await prepared.start();
    await execution.send({ ids: [1, 2], flag: true });
    await execution.finishInput();
    await execution.completed;
    expect(captured?.body).toBe("flag=true&ids=%5B1%2C2%5D");
  });
});
