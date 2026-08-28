import { describe, expect, it, vi } from "vitest";
import { OpenAPIEngine } from "./engine.js";
import { loadOpenAPIArtifact } from "./openapi32-artifact.js";

const operationsDocument = {
  openapi: "3.2.0",
  servers: [{ url: "https://api.example" }],
  paths: {
    "/operations": {
      query: {},
      trace: {
        requestBody: {
          required: true,
          content: { "application/json": { schema: { type: "object" } } },
        },
      },
      additionalOperations: {
        COPY: {},
        "F~O": {},
        GeT: {},
      },
    },
  },
};

describe("OpenAPI 3.2 operation correspondence", () => {
  it.each([
    ["#/paths/~1operations/query", "QUERY"],
    ["#/paths/~1operations/additionalOperations/COPY", "COPY"],
    ["#/paths/~1operations/additionalOperations/F~0O", "F~O"],
  ])("dispatches %s with its exact wire method", async (ref, method) => {
    const fetchFn = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
    const prepared = await new OpenAPIEngine().prepare({
      source: { content: operationsDocument },
      ref,
      fetch: fetchFn,
    });
    const execution = await prepared.start();
    await execution.finishInput();
    for await (const _event of execution.events) { /* drain */ }
    await execution.completed;
    expect(fetchFn.mock.calls[0]?.[1]?.method).toBe(method);
  });

  it("prohibits fixed TRACE request content without changing additional methods", async () => {
    const artifact = await loadOpenAPIArtifact({ content: operationsDocument });
    const trace = await artifact.resolveOperation("#/paths/~1operations/trace");
    expect(trace.operation.requestBody).toBeUndefined();
    expect(trace.reference.wireMethod).toBe("TRACE");
    const copy = await artifact.resolveOperation("#/paths/~1operations/additionalOperations/COPY");
    expect(copy.reference).toMatchObject({ additional: true, wireMethod: "COPY" });
  });

  it("retains excluded additional-operation positions in deterministic inventory", async () => {
    const artifact = await loadOpenAPIArtifact({ content: operationsDocument });
    const inventory = await artifact.operationInventory();
    expect(inventory.map(({ reference }) => reference.ref)).toEqual([
      "#/paths/~1operations/trace",
      "#/paths/~1operations/query",
      "#/paths/~1operations/additionalOperations/COPY",
      "#/paths/~1operations/additionalOperations/F~0O",
      "#/paths/~1operations/additionalOperations/GeT",
    ]);
    expect(inventory.at(-1)?.error).toMatchObject({ kind: "excluded" });
  });
});
