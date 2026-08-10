import { describe, expect, it, vi } from "vitest";

import {
  OpenAPIEngine,
  OpenAPIExecutionError,
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
