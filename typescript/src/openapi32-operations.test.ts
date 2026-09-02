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
        // openbindings.openapi-3.2@1 §6.1: an additionalOperations key denotes
        // the method token it spells, and RFC 9110 §9.1 makes that token
        // case-sensitive. `GeT` is a method no fixed field defines; `GET` is
        // the byte-exact token the `get` fixed field sends, so it is the
        // declaration defect OAS forbids.
        GeT: {},
        GET: {},
      },
    },
  },
};

describe("OpenAPI 3.2 operation correspondence", () => {
  it.each([
    ["#/paths/~1operations/query", "QUERY"],
    ["#/paths/~1operations/additionalOperations/COPY", "COPY"],
    ["#/paths/~1operations/additionalOperations/F~0O", "F~O"],
    ["#/paths/~1operations/additionalOperations/GeT", "GeT"],
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

  it("admits a case-distinct method token and excludes only the byte-exact collision", async () => {
    const artifact = await loadOpenAPIArtifact({ content: operationsDocument });
    const mixed = await artifact.resolveOperation("#/paths/~1operations/additionalOperations/GeT");
    expect(mixed.reference).toMatchObject({ additional: true, method: "GeT", wireMethod: "GeT" });
    await expect(artifact.resolveOperation("#/paths/~1operations/additionalOperations/GET"))
      .rejects.toMatchObject({ kind: "excluded" });
  });

  it("retains excluded additional-operation positions in deterministic inventory", async () => {
    const artifact = await loadOpenAPIArtifact({ content: operationsDocument });
    const inventory = await artifact.operationInventory();
    expect(inventory.map(({ reference }) => reference.ref)).toEqual([
      "#/paths/~1operations/trace",
      "#/paths/~1operations/query",
      "#/paths/~1operations/additionalOperations/COPY",
      "#/paths/~1operations/additionalOperations/F~0O",
      "#/paths/~1operations/additionalOperations/GET",
      "#/paths/~1operations/additionalOperations/GeT",
    ]);
    // A key that resolves also enumerates as a target; the excluded one
    // enumerates as a position with no target.
    const byRef = new Map(inventory.map((disposition) => [disposition.reference.ref, disposition]));
    expect(byRef.get("#/paths/~1operations/additionalOperations/GeT")?.target).toBeDefined();
    expect(byRef.get("#/paths/~1operations/additionalOperations/GET")?.target).toBeUndefined();
    expect(byRef.get("#/paths/~1operations/additionalOperations/GET")?.error)
      .toMatchObject({ kind: "excluded" });
  });
});
