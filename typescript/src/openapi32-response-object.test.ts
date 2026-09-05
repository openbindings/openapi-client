// Smallest-owner response-defect confinement on the OpenAPI 3.2 lane.

import { describe, expect, it } from "vitest";
import { loadOpenAPIArtifact } from "./openapi32-artifact.js";

const defectiveShapes: Array<[string, unknown]> = [
  ["the member is not a Response Object at all", "ok"],
  ["`description` is present and is not a string", { description: 123 }],
  ["`headers` is present and is not a map", { description: "ok", headers: "nope" }],
  ["`content` is present and is not a map", { description: "ok", content: "application/json" }],
  ["`links` is present and is not a map", { description: "ok", links: "nope" }],
  ["a `headers` member is not a Header Object", { description: "ok", headers: { "X-Thing": "nope" } }],
];

function responseDocument(code: string, response: unknown, alsoSuccess: boolean): Record<string, unknown> {
  const responses: Record<string, unknown> = { [code]: response };
  if (alsoSuccess) responses["200"] = { description: "ok" };
  return {
    openapi: "3.2.0",
    info: { title: "R2", version: "1" },
    servers: [{ url: "https://api.example" }],
    paths: {
      "/broken": { get: { responses } },
      "/intact": { get: { responses: { 204: { description: "ok" } } } },
    },
  };
}

async function resolve(document: Record<string, unknown>, selector: string) {
  const artifact = await loadOpenAPIArtifact({ content: document });
  return artifact.resolveOperation(selector);
}

describe("OpenAPI 3.2 upstream-invalid governing Response Object", () => {
  it.each(defectiveShapes)("keeps the operation represented when %s", async (_name, response) => {
    const document = responseDocument("200", response, false);
    await expect(resolve(document, "#/paths/~1broken/get")).resolves.toBeDefined();
    await expect(resolve(document, "#/paths/~1intact/get")).resolves.toBeDefined();
  });

  // F1: a defective NON-SUCCESS declaration loses no representation -- its
  // failure body is opaque application-authored data -- and must not destroy a
  // target whose success path is intact. 3.0/3.1 already keep it.
  it.each(defectiveShapes)("keeps its target when a non-success declaration has it: %s", async (_name, response) => {
    const document = responseDocument("404", response, true);
    await expect(resolve(document, "#/paths/~1broken/get")).resolves.toBeDefined();
    await expect(resolve(document, "#/paths/~1intact/get")).resolves.toBeDefined();
  });

  // A defective admitted default remains subordinate whether or not a 2XX
  // range shadows it for successful responses.
  it("keeps a defective `default` subordinate with and without a `2XX` range key", async () => {
    const defect = { description: "ok", headers: "nope" };
    await expect(resolve(responseDocument("default", defect, false), "#/paths/~1broken/get"))
      .resolves.toBeDefined();
    await expect(resolve({
      openapi: "3.2.0",
      info: { title: "R2", version: "1" },
      servers: [{ url: "https://api.example" }],
      paths: { "/broken": { get: { responses: { "2XX": { description: "ok" }, default: defect } } } },
    }, "#/paths/~1broken/get")).resolves.toBeDefined();
  });

  // OAS 3.2.0 dropped the REQUIRED marker from `description`, so an omission is
  // conformant here with or without declared content.
  it.each([
    ["with no declared content", {}],
    ["with declared content", { content: { "application/json": { schema: { type: "object" } } } }],
    ["with only a summary", { summary: "ok" }],
  ])("treats an omitted `description` as conformant %s", async (_name, response) => {
    await expect(resolve(responseDocument("200", response, false), "#/paths/~1broken/get")).resolves.toBeDefined();
  });
});
