import { describe, expect, it } from "vitest";

import { OPENAPI_PROFILE_ROUTED, withInputRouteMarker } from "./profile.js";

const ROUTED_PROFILE = withInputRouteMarker(OPENAPI_PROFILE_ROUTED, "openbindings.openapi@2");
import {
  parseRoutedEnvelope,
  routeEnvelope,
  validateEnvelopeRoutes,
  type RoutedEnvelope,
} from "./input-routes-v2.js";
import { FAMILY_JSON, type BodyPlan } from "./media.js";

describe("OpenAPI revision-2 routed input", () => {
  it("refuses one source field supplying two destinations", () => {
    expect(() => parseRoutedEnvelope([{
      $openbindings: ROUTED_PROFILE.inputRouteMarker,
      value: { shared: "x" },
      parameters: [
        { in: "path", name: "id", field: "shared" },
        { in: "query", name: "id", field: "shared" },
      ],
      body: {},
    }], ROUTED_PROFILE)).toThrow(/more than one destination/);
  });

  it("requires the private descriptor's exact top-level shape", () => {
    expect(() => parseRoutedEnvelope([{
      $openbindings: ROUTED_PROFILE.inputRouteMarker,
      value: {},
      parameters: [],
      body: {},
      extra: true,
    }], ROUTED_PROFILE)).toThrow(/exactly/);
  });

  it("leaves a marker-shaped application object in the flat representation", () => {
    expect(parseRoutedEnvelope({
      $openbindings: ROUTED_PROFILE.inputRouteMarker,
      value: { application: true },
    })).toBeNull();
  });

  it("refuses a route that names no effective declaration", () => {
    const envelope: RoutedEnvelope = {
      value: {},
      parameters: [{ in: "query", name: "id", field: "queryID" }],
      bodyFields: {},
      wholeBodyField: "",
    };
    expect(() => validateEnvelopeRoutes(
      [{ in: "path", name: "id", schema: { type: "string" } }],
      [],
      envelope,
      ROUTED_PROFILE,
    )).toThrow(/does not identify/);
  });

  it("passes a field mapped only by another candidate into the selected open JSON body", () => {
    const envelope: RoutedEnvelope = {
      value: { renamed: "x" },
      parameters: [],
      bodyFields: { id: "renamed" },
      wholeBodyField: "",
    };
    const plan: BodyPlan = {
      declared: true,
      required: false,
      mediaKey: "application/json",
      media: { schema: { type: "object", properties: { name: { type: "string" } } } },
      family: FAMILY_JSON,
      mediaType: "application/json",
      props: new Set(["name"]),
      synthetic: false,
    };
    expect(routeEnvelope([], envelope, "/items", plan, ROUTED_PROFILE).bodyFields).toEqual({ renamed: "x" });
  });
});
