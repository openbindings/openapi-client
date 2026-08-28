import { describe, expect, it } from "vitest";
import { loadSwagger20 } from "./swagger20-loader.js";

describe("native Swagger 2.0 synthesis analysis", () => {
  it("confines response defects and retains sibling targets", async () => {
    const client = await loadSwagger20({ content: {
      swagger: "2.0", schemes: ["https"], host: "api.example",
      paths: {
        "/bad": { get: { operationId: "badResponse", responses: { "2XX": { description: "bad" } } } },
        "/good": { get: { operationId: "goodResponse", responses: { 204: { description: "ok" } } } },
      },
    } });
    const model = await client.synthesisModel();
    expect(model.operations.map((operation) => [operation.operationId, operation.excluded])).toEqual([
      ["badResponse", true],
      ["goodResponse", false],
    ]);
  });

  it("accounts unusable server and security alternatives at their smallest owners", async () => {
    const client = await loadSwagger20({ content: {
      swagger: "2.0", schemes: ["https", "wss"], host: "api.example",
      securityDefinitions: {
        bad: { type: "apiKey", in: "query", name: "id" },
        good: { type: "apiKey", in: "header", name: "X-Key" },
      },
      security: [{ bad: [] }, { good: [] }],
      paths: { "/x": { get: {
        parameters: [{ name: "id", in: "query", type: "string" }],
        responses: { 204: { description: "ok" } },
      } } },
    } });
    const [operation] = (await client.synthesisModel()).operations;
    expect(operation?.excluded).toBe(false);
    expect(operation?.requirements).toContain("configuration.security");
    expect(operation?.alternatives.filter((alternative) => !alternative.usable).map((alternative) => alternative.kind))
      .toEqual(["security", "server"]);
  });
});
