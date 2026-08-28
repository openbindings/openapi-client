import { describe, expect, it } from "vitest";
import { loadOpenAPIArtifact } from "./openapi32-artifact.js";
import { admittedOpenAPI32ResponseKey } from "./openapi32-response.js";
import { classifyOpenAPI32SequentialResponse } from "./openapi32-sequential-response.js";

function document(responses: Record<string, unknown>): Record<string, unknown> {
  return {
    openapi: "3.2.0",
    info: { title: "responses", version: "1" },
    paths: { "/x": { get: { responses } } },
  };
}

describe("OpenAPI 3.2 response governance", () => {
  it("admits only exact, uppercase range, default, and extension keys", () => {
    expect([
      "100", "204", "599", "1XX", "2XX", "5XX", "default", "x-note",
    ].every(admittedOpenAPI32ResponseKey)).toBe(true);
    expect([
      "099", "600", "20X", "2xx", "DEFAULT", "2000", "",
    ].some(admittedOpenAPI32ResponseKey)).toBe(false);
  });

  it("selects exact, then range, then default without changing native status", async () => {
    const artifact = await loadOpenAPIArtifact({ content: document({
      "206": { content: { "application/json": {} } },
      "2XX": { content: { "text/plain": {} } },
      default: { content: { "application/octet-stream": {} } },
    }) });
    const target = await artifact.resolveOperation("#/paths/~1x/get");

    expect(artifact.selectOpenAPI32Response(target, 206)).toMatchObject({
      statusCode: 206,
      success: true,
      responseKey: "206",
    });
    expect(artifact.selectOpenAPI32Response(target, 207)).toMatchObject({
      success: true,
      responseKey: "2XX",
    });
    expect(artifact.selectOpenAPI32Response(target, 404)).toMatchObject({
      success: false,
      responseKey: "default",
    });
  });

  it("allows description-less responses and excludes invalid response keys", async () => {
    const valid = await loadOpenAPIArtifact({ content: document({
      "200": { summary: "description is optional", content: { "application/json": {} } },
    }) });
    await expect(valid.resolveOperation("#/paths/~1x/get")).resolves.toMatchObject({
      operation: { responses: { "200": { summary: "description is optional" } } },
    });

    for (const key of ["2xx", "600"]) {
      const invalid = await loadOpenAPIArtifact({ content: document({ [key]: {} }) });
      await expect(invalid.resolveOperation("#/paths/~1x/get")).rejects.toMatchObject({
        kind: "excluded",
      });
    }
  });

  it("classifies incorporated sequential response framing and rejects unframed itemSchema", () => {
    expect(classifyOpenAPI32SequentialResponse("application/jsonl", { itemSchema: {} }))
      .toBe("json-lines");
    expect(classifyOpenAPI32SequentialResponse("application/problem+json-seq", { itemSchema: {} }))
      .toBe("json-seq");
    expect(classifyOpenAPI32SequentialResponse("text/event-stream; charset=utf-8", { itemSchema: {} }))
      .toBe("sse");
    expect(classifyOpenAPI32SequentialResponse("multipart/mixed; boundary=b", {
      schema: { type: "array" },
    })).toBe("multipart");
    expect(classifyOpenAPI32SequentialResponse("application/json", { schema: {} }))
      .toBeUndefined();
    expect(() => classifyOpenAPI32SequentialResponse("application/x-private", { itemSchema: {} }))
      .toThrow(/no incorporated sequential framing/u);
  });
});
