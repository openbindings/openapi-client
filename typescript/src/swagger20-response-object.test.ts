// Smallest-owner response-defect confinement on the Swagger 2.0 lane.

import { describe, expect, it } from "vitest";
import { loadSwagger20 } from "./swagger20-loader.js";

const defectiveShapes: Array<[string, unknown]> = [
  ["the member is not a Response Object at all", "ok"],
  ["`description` is present and is not a string", { description: 123 }],
  ["`headers` is present and is not a map", { description: "ok", headers: "nope" }],
  ["`schema` is present and is not a Schema Object", { description: "ok", schema: "nope" }],
  ["`examples` is present and is not a map", { description: "ok", examples: "nope" }],
  ["a `headers` member is not a Header Object", { description: "ok", headers: { "X-Thing": "nope" } }],
  ["`description` is omitted while `schema` is declared", { schema: { type: "object" } }],
];

function responseDocument(code: string, response: unknown, alsoSuccess: boolean): Record<string, unknown> {
  const responses: Record<string, unknown> = { [code]: response };
  if (alsoSuccess) responses["200"] = { description: "ok" };
  return {
    swagger: "2.0",
    info: { title: "R2", version: "1" },
    host: "api.example",
    schemes: ["https"],
    paths: {
      "/broken": { get: { operationId: "broken", responses } },
      "/intact": { get: { operationId: "intact", responses: { 204: { description: "ok" } } } },
    },
  };
}

async function analyze(document: Record<string, unknown>) {
  const client = await loadSwagger20({ content: document });
  const model = await client.synthesisModel();
  return new Map(model.operations.map((operation) => [operation.operationId, operation]));
}

describe("Swagger 2.0 upstream-invalid governing Response Object", () => {
  it.each(defectiveShapes)("keeps the operation represented when %s", async (_name, response) => {
    const operations = await analyze(responseDocument("200", response, false));
    expect(operations.get("broken")?.excluded).toBe(false);
    expect(operations.get("intact")?.excluded).toBe(false);
  });

  // An admitted key retains lookup precedence even when its smallest
  // Response projection is invalid; it never promotes that defect to the
  // operation owner.
  it.each(defectiveShapes)("keeps `default` precedence without removing the operation when %s", async (_name, response) => {
    const operations = await analyze(responseDocument("default", response, false));
    expect(operations.get("broken")?.excluded).toBe(false);
  });

  // F1: a defective NON-SUCCESS declaration loses no representation and must
  // not destroy a target whose success path is intact.
  it.each(defectiveShapes)("keeps its target when a non-success declaration has it: %s", async (_name, response) => {
    const operations = await analyze(responseDocument("404", response, true));
    expect(operations.get("broken")?.excluded).toBe(false);
  });

  // The carve-out `openbindings.openapi-2.0@1` §9.4 states in the same breath.
  it("keeps a `description`-less Response Object that declares no `schema`", async () => {
    const operations = await analyze(responseDocument("200", {}, false));
    expect(operations.get("broken")?.excluded).toBe(false);
  });

  it("confines a defect inside a `$ref`ed Response Object", async () => {
    const operations = await analyze({
      swagger: "2.0",
      info: { title: "R2", version: "1" },
      host: "api.example",
      schemes: ["https"],
      responses: { Broken: { description: "ok", headers: "nope" } },
      paths: {
        "/broken": { get: { operationId: "broken", responses: { 200: { $ref: "#/responses/Broken" } } } },
        "/intact": { get: { operationId: "intact", responses: { 204: { description: "ok" } } } },
      },
    });
    expect(operations.get("broken")?.excluded).toBe(false);
    expect(operations.get("intact")?.excluded).toBe(false);
  });
});
