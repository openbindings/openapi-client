import { describe, expect, it, vi } from "vitest";

import {
  OpenAPIEngine,
  OpenAPIExecutionError,
  openAPIPortableFailureData,
  OPENAPI_PROFILE_FULL,
} from "./engine.js";

const operationRef = "#/paths/~1widgets~1{id}/get";

function document(secured = false) {
  return {
    openapi: "3.1.0",
    info: { title: "engine", version: "1" },
    servers: [{ url: "https://api.example.test" }],
    ...(secured ? {
      components: {
        securitySchemes: { token: { type: "http", scheme: "bearer" } },
      },
    } : {}),
    paths: {
      "/widgets/{id}": {
        get: {
          parameters: [{
            name: "id",
            in: "path",
            required: true,
            schema: { type: "string" },
          }],
          ...(secured ? { security: [{ token: [] }] } : {}),
          responses: {
            "200": {
              description: "widget",
              content: { "application/json": {} },
            },
          },
        },
      },
    },
  };
}

describe("OpenAPIEngine", () => {
  it("prepares and executes an operation without SDK classes", async () => {
    const fetchFn = vi.fn<typeof fetch>(async () => new Response('{"id":"42"}', {
      status: 200,
      headers: { "content-type": "application/json" },
    }));
    const prepared = await new OpenAPIEngine().prepare({
      source: { content: document() },
      ref: operationRef,
      profile: OPENAPI_PROFILE_FULL,
      fetch: fetchFn,
    });
    const execution = await prepared.start<{ id: string }, { id: string }>();
    await execution.send({ id: "42" });
    await execution.finishInput();
    const events = [];
    for await (const event of execution.events) events.push(event);
    await execution.completed;

    expect(events).toEqual([{ value: { id: "42" }, metadata: expect.any(Object) }]);
    expect(fetchFn).toHaveBeenCalledOnce();
  });

  it("surfaces prerequisites and refuses before accepting application input", async () => {
    const fetchFn = vi.fn<typeof fetch>();
    const prepared = await new OpenAPIEngine().prepare({
      source: { content: document(true) },
      ref: operationRef,
      fetch: fetchFn,
    });
    expect(prepared.prerequisites?.alternatives[0]?.requirements[0]?.type).toBe("auth.bearer");

    await expect(prepared.start()).rejects.toMatchObject({
      name: "OpenAPIExecutionError",
      code: "CONTEXT_REQUIRED",
    } satisfies Partial<OpenAPIExecutionError>);
    expect(fetchFn).not.toHaveBeenCalled();
  });

  it("derives custom-security satisfaction only from an installed handler", async () => {
    const secured: any = document();
    secured.components = {
      securitySchemes: { digest: { type: "http", scheme: "digest" } },
    };
    secured.paths["/widgets/{id}"]!.get!.security = [{ digest: [] }];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input as Request;
      expect(request.headers.get("cookie")).toBe("session=ready");
      expect(request.headers.get("authorization")).toBe("Digest engine-proof");
      return new Response('{"id":"42"}', {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    });
    const engine = new OpenAPIEngine();

    const spoofed = await engine.prepare({
      source: { content: secured },
      ref: operationRef,
      context: { $openapiSecurity: { digest: true } },
      fetch: fetchFn,
    });
    expect(spoofed.prerequisites?.alternatives[0]?.requirements[0]?.name).toBe("digest");
    await expect(spoofed.start()).rejects.toMatchObject({ code: "CONTEXT_REQUIRED" });

    const prepared = await engine.prepare({
      source: { content: secured },
      ref: operationRef,
      context: { cookies: { session: "ready" } },
      securityHandlers: {
        digest(request, context) {
          expect(context.schemeName).toBe("digest");
          request.headers.set("authorization", "Digest engine-proof");
        },
      },
      fetch: fetchFn,
    });
    expect(prepared.prerequisites).toBeNull();
    const execution = await prepared.start<{ id: string }, { id: string }>();
    await execution.send({ id: "42" });
    await execution.finishInput();
    for await (const _event of execution.events) {
      // Drain the unary output.
    }
    await execution.completed;
    expect(fetchFn).toHaveBeenCalledOnce();
  });

  it("rejects an unresolved operation during preparation", async () => {
    await expect(new OpenAPIEngine().prepare({
      source: { content: document() },
      ref: "#/paths/~1missing/get",
    })).rejects.toMatchObject({ code: "OPERATION_NOT_FOUND" });
  });
});

// A declared failure body decodes through the same response lanes as a
// successful body (openbindings.openapi §9.5, ruled 2026-08-13; Go twin:
// TestEngineFailureBodiesDecodeThroughSuccessLanes).
describe("failure bodies decode through the success lanes", () => {
  const failureDocument = {
    openapi: "3.1.0",
    info: { title: "engine", version: "1" },
    servers: [{ url: "https://api.example.test" }],
    paths: {
      "/widgets": {
        get: {
          responses: {
            "404": {
              description: "missing",
              content: {
                "application/json": {},
                "text/plain": {},
                "application/octet-stream": {},
              },
            },
          },
        },
      },
    },
  };

  const bodyText = "not-json";
  const cases: Array<[string, { present: boolean; value?: unknown }]> = [
    ["application/json", { present: false }],
    ["text/plain", { present: true, value: "not-json" }],
    ["application/octet-stream", { present: true, value: btoa("not-json") }],
  ];

  for (const [contentType, want] of cases) {
    it(`${contentType}`, async () => {
      const fetchFn = vi.fn<typeof fetch>(async () => new Response(bodyText, {
        status: 404,
        headers: { "content-type": contentType },
      }));
      const prepared = await new OpenAPIEngine().prepare({
        source: { content: failureDocument },
        ref: "#/paths/~1widgets/get",
        profile: OPENAPI_PROFILE_FULL,
        fetch: fetchFn,
      });
      const execution = await prepared.start();
      await execution.finishInput();
      let terminal: unknown;
      try {
        for await (const _ of execution.events) { /* drain */ }
        await execution.completed;
      } catch (error: unknown) {
        terminal = error;
      }
      expect(terminal).toBeInstanceOf(OpenAPIExecutionError);
      const portable = openAPIPortableFailureData(terminal);
      expect(portable.present).toBe(want.present);
      if (want.present && portable.present) expect(portable.value).toEqual(want.value);
    });
  }
});

// A multi-alternative effective server list without a selection challenges
// CONTEXT_REQUIRED (config.value, point server) instead of refusing
// terminally (openbindings.openapi §9.3, ruled 2026-08-13; Go twin:
// TestEngineMultiServerWithoutSelectionChallengesContextRequired).
describe("multi-server selection negotiation", () => {
  const multiServerDocument = {
    openapi: "3.1.0",
    info: { title: "engine", version: "1" },
    servers: [{ url: "https://a.example.test" }, { url: "https://b.example.test" }],
    paths: { "/things": { get: { responses: { "204": { description: "ok" } } } } },
  };

  it("challenges config.value/server when unselected", async () => {
    const prepared = await new OpenAPIEngine().prepare({
      source: { content: multiServerDocument },
      ref: "#/paths/~1things/get",
      profile: OPENAPI_PROFILE_FULL,
      fetch: async () => new Response(null, { status: 204 }),
    });
    let challenge: unknown;
    try {
      await prepared.start();
    } catch (error: unknown) {
      challenge = error;
    }
    expect(challenge).toBeInstanceOf(OpenAPIExecutionError);
    const failure = challenge as OpenAPIExecutionError & { details?: { alternatives?: Array<{ requirements: Array<Record<string, unknown>> }> } };
    expect(failure.code).toBe("CONTEXT_REQUIRED");
    const requirement = failure.details?.alternatives?.[0]?.requirements?.[0] as Record<string, unknown>;
    expect(requirement.type).toBe("config.value");
    expect(requirement.point).toBe("server");
  });

  it("dispatches once a member is selected", async () => {
    const fetchFn = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
    const prepared = await new OpenAPIEngine().prepare({
      source: { content: multiServerDocument },
      ref: "#/paths/~1things/get",
      profile: OPENAPI_PROFILE_FULL,
      context: { configuration: { server: { index: 1 } } },
      fetch: fetchFn,
    });
    const execution = await prepared.start();
    await execution.finishInput();
    for await (const _ of execution.events) { /* drain */ }
    await execution.completed;
    expect(fetchFn.mock.calls[0]?.[0]?.toString()).toContain("b.example.test");
  });
});
