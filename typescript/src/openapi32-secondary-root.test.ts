import { expect, it } from "vitest";
import { loadOpenAPIArtifact } from "./openapi32-artifact.js";

const root = { name: "q", in: "query", required: true, schema: { type: "string" } };
const source = (operation: unknown) => ({ content: {
  openapi: "3.2.0", info: { title: "N2", version: "1" },
  paths: { "/x": { post: operation }, "/safe": { get: { responses: { "204": { description: "ok" } } } } },
} });
const fetchRoot = (value: unknown): typeof fetch => async () => new Response(JSON.stringify(value));

it("excludes only a request-media reference into another root, preserving its sibling", async () => {
  const artifact = await loadOpenAPIArtifact(source({ requestBody: { required: true, content: {
    "text/plain": { schema: { $ref: "https://example.test/other#/schema" } },
    "application/json": { schema: {} },
  } }, responses: { "204": { description: "ok" } } }), { fetch: fetchRoot(root) });
  const target = await artifact.resolveOperation("#/paths/~1x/post");
  expect(Object.keys(target.operation.requestBody!.content!)).toEqual(["application/json"]);
  await expect(artifact.resolveOperation("#/paths/~1safe/get")).resolves.toBeDefined();
});

it.each([
  [root, "excluded"],
  [{ ...root, $self: "https://example.test/canonical" }, "invalid"],
  [[1], "invalid"],
] as const)("preserves independent reference-defect classification", async (value, kind) => {
  const artifact = await loadOpenAPIArtifact(source({ parameters: [{
    $ref: `https://example.test/other${Array.isArray(value) ? "#/0" : ""}`,
  }], responses: { "204": { description: "ok" } } }), { fetch: fetchRoot(value) });
  await expect(artifact.resolveOperation("#/paths/~1x/post")).rejects.toMatchObject({ kind });
  await expect(artifact.resolveOperation("#/paths/~1safe/get")).resolves.toBeDefined();
});
