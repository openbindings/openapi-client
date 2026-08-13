import { describe, expect, it, vi } from "vitest";
import { CONTEXT_REQUIRED, single, type InvocationError } from "./internal/index.js";
import { OpenAPIRuntime } from "./runtime.js";
import type { OpenAPIDocument } from "./types.js";

const document = {
  openapi: "3.1.0",
  info: { title: "Standalone runtime", version: "1" },
  servers: [{ url: "https://api.example.test/v1" }],
  paths: {
    "/users/{id}": {
      get: {
        parameters: [
          {
            name: "id",
            in: "path",
            required: true,
            schema: { type: "string" },
          },
        ],
        responses: {
          "200": {
            description: "user",
            content: {
              "application/json": {
                schema: {
                  type: "object",
                  properties: { id: { type: "string" } },
                  required: ["id"],
                },
              },
            },
          },
        },
      },
    },
  },
};

describe("OpenAPIRuntime", () => {
  it("invokes a directly selected artifact operation without an OBI", async () => {
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      expect(String(input)).toBe("https://api.example.test/v1/users/42");
      return new Response('{"id":"42"}', {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    });
    const runtime = new OpenAPIRuntime();
    const call = runtime.invoke({
      source: { content: document },
      ref: "#/paths/~1users~1{id}/get",
      fetch: fetchFn,
    });

    await call.write({ id: "42" });
    await call.close();

    await expect(single(call.outputs)).resolves.toEqual({ id: "42" });
    await expect(call.closed).resolves.toBeUndefined();
    expect(fetchFn).toHaveBeenCalledOnce();
  });

  it("derives prerequisites directly from the artifact", async () => {
    const secured = structuredClone(document) as OpenAPIDocument;
    secured.components = {
      securitySchemes: {
        token: { type: "http", scheme: "bearer" },
      },
    };
    const operation = secured.paths?.["/users/{id}"]?.get;
    if (!operation) throw new Error("test document operation is missing");
    operation.security = [{ token: [] }];

    const runtime = new OpenAPIRuntime();
    const requirement = await runtime.prepare({
      source: { content: secured },
      ref: "#/paths/~1users~1{id}/get",
    });

    expect(requirement?.target).toBe("https://api.example.test/v1");
    expect(requirement?.alternatives[0]?.requirements[0]?.type).toBe("auth.bearer");

    const call = runtime.invoke({
      source: { content: secured },
      ref: "#/paths/~1users~1{id}/get",
      fetch: vi.fn<typeof fetch>(),
    });
    await expect(call.closed).rejects.toMatchObject({ code: CONTEXT_REQUIRED } satisfies Partial<InvocationError>);
  });

  it("preserves declared failure values opaquely, including JSON null", async () => {
    const failureDocument = structuredClone(document) as OpenAPIDocument;
    const operation = failureDocument.paths?.["/users/{id}"]?.get;
    if (!operation) throw new Error("test document operation is missing");
    operation.responses ??= {};
    operation.responses["404"] = {
      description: "missing",
      content: { "application/problem+json": {} },
    };

    for (const value of [{ reason: "missing" }, ["missing"], "missing", 0, false, null]) {
      const runtime = new OpenAPIRuntime();
      const call = runtime.invoke({
        source: { content: failureDocument },
        ref: "#/paths/~1users~1{id}/get",
        fetch: vi.fn<typeof fetch>(async () => new Response(JSON.stringify(value), {
          status: 404,
          headers: { "content-type": "application/problem+json" },
        })),
      });
      await call.write({ id: "42" });
      await call.close();
      const error = await call.closed.catch((caught: unknown) => caught) as InvocationError;
      expect(error.code).toBe("ERR_EXECUTION_FAILED");
      expect(Object.hasOwn(error, "details")).toBe(true);
      expect(error.details).toEqual(value);
    }
  });

  it("does not promote undeclared, mismatched, malformed, or raw failure bodies", async () => {
    const failureDocument = structuredClone(document) as OpenAPIDocument;
    const operation = failureDocument.paths?.["/users/{id}"]?.get;
    if (!operation) throw new Error("test document operation is missing");
    operation.responses ??= {};
    operation.responses["404"] = {
      description: "missing",
      content: { "application/json": {} },
    };

    const cases: Array<[string, Response]> = [
      ["malformed JSON", new Response("not-json", { status: 404, headers: { "content-type": "application/json" } })],
      ["mismatched media", new Response('{"reason":"missing"}', { status: 404, headers: { "content-type": "text/plain" } })],
      ["undeclared raw body", new Response(new Uint8Array([0, 255]), { status: 500, headers: { "content-type": "application/octet-stream" } })],
    ];
    for (const [caseName, response] of cases) {
      const call = new OpenAPIRuntime().invoke({
        source: { content: failureDocument },
        ref: "#/paths/~1users~1{id}/get",
        fetch: vi.fn<typeof fetch>(async () => response.clone()),
      });
      await call.write({ id: "42" });
      await call.close();
      const error = await call.closed.catch((caught: unknown) => caught) as InvocationError;
      expect(error.code).toBe("ERR_EXECUTION_FAILED");
      expect(error.details, caseName).toBeUndefined();
    }
  });
});
