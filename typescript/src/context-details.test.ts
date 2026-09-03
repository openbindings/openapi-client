// The exported CONTEXT_REQUIRED builders: one payload per configuration
// point, carrying the scope, the durability and the prompt text the Go
// engine carries, so an adapter that raises the same point raises the same
// bytes instead of re-minting a copy.
import { describe, expect, it } from "vitest";
import {
  PROPERTY_MEDIA_REQUIREMENT_DESCRIPTION,
  REQUEST_MEDIA_REQUIREMENT_DESCRIPTION,
  configRequiredDetails,
  propertyMediaContextDetails,
  requestMediaContextDetails,
} from "./invoke.js";
import { ConfigRequired } from "./servers.js";

describe("context-required payload builders", () => {
  it("requestMedia: one durable config.value at the whole point, scoped to the target", () => {
    expect(requestMediaContextDetails("https://api.example")).toEqual({
      target: "https://api.example",
      alternatives: [{
        requirements: [{
          type: "config.value",
          point: "requestMedia",
          path: "",
          description: REQUEST_MEDIA_REQUIREMENT_DESCRIPTION,
          durable: true,
        }],
      }],
    });
  });

  it("propertyMedia: one durable config.value per property in one alternative, pointer-escaped", () => {
    expect(propertyMediaContextDetails("https://api.example", ["profile", "a/b", "c~d"])).toEqual({
      target: "https://api.example",
      alternatives: [{
        requirements: [
          { type: "config.value", point: "propertyMedia", path: "/profile", description: PROPERTY_MEDIA_REQUIREMENT_DESCRIPTION, durable: true },
          { type: "config.value", point: "propertyMedia", path: "/a~1b", description: PROPERTY_MEDIA_REQUIREMENT_DESCRIPTION, durable: true },
          { type: "config.value", point: "propertyMedia", path: "/c~0d", description: PROPERTY_MEDIA_REQUIREMENT_DESCRIPTION, durable: true },
        ],
      }],
    });
  });

  it("configRequiredDetails carries the signal's fields exactly, durable only where the signal says so", () => {
    expect(configRequiredDetails(
      new ConfigRequired("server", "/url", "pick one", { enum: ["https://a.example"] }, true),
      "https://doc.example/openapi.json",
    )).toEqual({
      target: "https://doc.example/openapi.json",
      alternatives: [{
        requirements: [{
          type: "config.value",
          point: "server",
          path: "/url",
          description: "pick one",
          schema: { enum: ["https://a.example"] },
          durable: true,
        }],
      }],
    });
    expect(configRequiredDetails(new ConfigRequired("server", "/variables/region", "no default"), "")).toEqual({
      target: "",
      alternatives: [{
        requirements: [{ type: "config.value", point: "server", path: "/variables/region", description: "no default" }],
      }],
    });
  });
});
