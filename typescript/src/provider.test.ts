import { describe, expect, it } from "vitest";
import { analyzeOpenAPIProjection } from "./provider.js";

const DOCUMENT = {
  openapi: "3.1.2",
  info: { title: "Projection", version: "1" },
  servers: [{ url: "https://api.example.test" }],
  paths: {
    "/things/{id}": {
      post: {
        operationId: "createThing",
        parameters: [
          { in: "path", name: "id", required: true, schema: { type: "string" } },
          { in: "query", name: "id", schema: { type: "string" } },
        ],
        requestBody: {
          required: true,
          content: {
            "application/json": {
              schema: {
                type: "object",
                properties: { label: { type: "string" } },
              },
            },
          },
        },
        responses: {
          "200": {
            description: "ok",
            content: { "application/json": { schema: { type: "object" } } },
          },
        },
      },
    },
  },
};

describe("detached provider projection", () => {
  it("returns deeply immutable JSON facts without an OBI or parser graph", async () => {
    const analysis = await analyzeOpenAPIProjection(DOCUMENT);
    const binding = analysis.openapi3?.bindings?.["createThing.openapi"];
    expect(analysis.edition).toBe("3.1.2");
    expect(binding?.input?.parameters).toEqual([
      { in: "path", name: "id", field: "id", callerKey: "path/id" },
      { in: "query", name: "id", field: "id_2", callerKey: "query/id" },
    ]);
    expect(binding?.input).toMatchObject({
      bodyProperties: { label: "label" },
      openBody: true,
      bodyRequired: true,
    });
    expect(Object.isFrozen(analysis)).toBe(true);
    expect(Object.isFrozen(binding?.input?.parameters)).toBe(true);
    const serialized = JSON.stringify(analysis);
    expect(serialized).not.toContain("inputTransform");
    expect(serialized).not.toContain("openbindings\"");
    expect(serialized).not.toContain("document\"");
    expect(() => {
      (analysis.openapi3!.operations as Record<string, unknown>).replacement = {};
    }).toThrow(TypeError);
  });

  it("retrieves the entry source exactly once before projection", async () => {
    let calls = 0;
    const analysis = await analyzeOpenAPIProjection("https://projection.example.test/openapi.json", {
      documentFetch: async () => {
        calls += 1;
        return new Response(JSON.stringify(DOCUMENT), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      },
    });
    expect(calls).toBe(1);
    expect(analysis.openapi3?.operations.createThing).toBeDefined();
  });
});
