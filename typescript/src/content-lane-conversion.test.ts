import { describe, expect, it } from "vitest";
import { numberToken } from "@openbindings/json";
import type { OpenAPIParameterConverter } from "./params.js";
import { OpenAPIEngine } from "./engine.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import {
  planResolvedRequestBodies,
  prepareEncodingStylePropertyValue,
  prepareResolvedPropertyMediaView,
  requiredPropertyMediaNames,
} from "./resolved-media.js";
import type { OpenAPIOperation } from "./types.js";

// Content-based form and multipart properties are serialized by their media
// lanes. parameterConversion belongs only to schema-form parameters and an
// explicitly RFC 6570-style Encoding path. The converter here is deliberately
// visible -- `n` + the scalar's own spelling -- so accidental consultation is
// observable.
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
              styled: { type: "integer" },
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
  styled: { explode: true },
};
const visibleConverter: OpenAPIParameterConverter = (value) => "n" + (numberToken(value) ?? String(value));

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
    it(`never converts content-based properties of a ${mediaType} body`, () => {
      const p = plan(mediaType, JSON_ENCODING);
      // Explicit Encoding contentType application/json: the JSON lane, untouched.
      expect(prepareEncodingStylePropertyValue(p, "ids", [1, 2], true, visibleConverter)).toEqual([1, 2]);
      expect(prepareEncodingStylePropertyValue(p, "flag", true, true, visibleConverter)).toBe(true);
      // No Encoding Object: the default table gives text/plain and the media
      // serializer supplies the binding-fixed lexical form without conversion.
      expect(prepareEncodingStylePropertyValue(p, "count", 7, true, visibleConverter)).toBe(7);
      expect(prepareEncodingStylePropertyValue(p, "count", 7, true, undefined)).toBe(7);
      // The JSON lane needs no converter at all.
      expect(prepareEncodingStylePropertyValue(p, "flag", true, true, undefined)).toBe(true);
      if (mediaType === "application/x-www-form-urlencoded") {
        // Explicit explode selects the style path, which retains the converter.
        expect(prepareEncodingStylePropertyValue(p, "styled", 7, true, visibleConverter)).toBe("n7");
        expect(() => prepareEncodingStylePropertyValue(p, "styled", 7, true, undefined)).toThrow(/parameterConversion/u);
      } else {
        // OAS 3.0 ignores style controls on multipart Encoding Objects.
        expect(prepareEncodingStylePropertyValue(p, "styled", 7, true, visibleConverter)).toBe(7);
      }
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

  it("does not consult the converter on the 3.1 line's content path either", () => {
    const plans = planResolvedRequestBodies(operation("multipart/form-data"), {
      profile: OPENAPI_PROFILE_FULL,
      openapiVersion: "3.1.2",
    });
    expect(prepareEncodingStylePropertyValue(plans[0]!, "count", 7, false, visibleConverter)).toBe(7);
  });

  it("carries JSON and text properties through the standalone engine without conversion", async () => {
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
    await execution.send({ ids: [1, 2], flag: true, count: 1000 });
    await execution.finishInput();
    await execution.completed;
    expect(captured?.body).toBe("count=1e3&flag=true&ids=%5B1%2C2%5D");
  });
});
