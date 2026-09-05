// The acceptance floor at the client engine's seams (block 8d-1): §3 part-2
// whole-source refusal at load, plus addressable-but-unusable target behavior.

import { describe, expect, it } from "vitest";
import { OpenAPIEngine, OpenAPIExecutionError } from "./engine.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";

const INVALID_TARGET_DOCUMENT = JSON.stringify({
  openapi: "3.0.3",
  info: { title: "T", version: "1" },
  paths: {
    "/good": {
      get: { operationId: "getGood", responses: { "200": { description: "ok" } } },
    },
    "/bad": {
      get: {
        operationId: "getBad",
        parameters: [{ in: "query", schema: { type: "string" } }],
        responses: { "200": { description: "ok" } },
      },
    },
  },
});

describe("acceptance floor at the engine", () => {
  it("refuses a 3.0 document with no paths at load (§3 part 2)", async () => {
    const engine = new OpenAPIEngine();
    const source = { content: JSON.stringify({ openapi: "3.0.1", info: { title: "T", version: "1" } }) };
    await expect(engine.prepare({ source, ref: "#/paths/~1a/get", profile: OPENAPI_PROFILE_FULL }))
      .rejects.toMatchObject({ code: "SOURCE_LOAD_FAILED", message: expect.stringContaining("whole-source refusal") });
  });

  it("refuses a ladder-invalid target before dispatch and prepares the sibling", async () => {
    const engine = new OpenAPIEngine();
    let refusal: unknown;
    try {
      await engine.prepare({ source: { content: INVALID_TARGET_DOCUMENT }, ref: "#/paths/~1bad/get", profile: OPENAPI_PROFILE_FULL });
    } catch (error: unknown) {
      refusal = error;
    }
    expect(refusal).toBeInstanceOf(OpenAPIExecutionError);
    expect((refusal as OpenAPIExecutionError).code).toBe("ERR_REFUSED");
    await expect(engine.prepare({ source: { content: INVALID_TARGET_DOCUMENT }, ref: "#/paths/~1good/get", profile: OPENAPI_PROFILE_FULL }))
      .resolves.toBeDefined();
  });

  it("tolerates a dangling internal $ref no surviving unit reads", async () => {
    // The dangling reference sits in an unreached component (the algorand
    // shape): the load no longer throws, the floor yields nothing, and the
    // declared target prepares.
    const document = JSON.stringify({
      openapi: "3.0.3",
      info: { title: "T", version: "1" },
      components: { schemas: { Orphan: { allOf: [{ $ref: "#/components/schemas/Missing" }] } } },
      paths: { "/a": { get: { operationId: "getA", responses: { "200": { description: "ok" } } } } },
    });
    await expect(engine_prepare(document)).resolves.toBeDefined();
  });
});

async function engine_prepare(content: string) {
  const engine = new OpenAPIEngine();
  return engine.prepare({ source: { content }, ref: "#/paths/~1a/get", profile: OPENAPI_PROFILE_FULL });
}
