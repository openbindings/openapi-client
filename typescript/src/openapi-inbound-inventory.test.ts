import { describe, expect, it } from "vitest";
import { documentInboundOperationInventory } from "./openapi-inbound-inventory.js";
import { loadOpenAPIArtifact } from "./openapi32-artifact.js";

describe("OpenAPI inbound operation inventory", () => {
  it("resolves declaration-local callback references and inventories all 3.2 operation forms", async () => {
    const document = {
      openapi: "3.2.0",
      paths: {
        "/jobs": {
          post: {
            callbacks: {
              done: { $ref: "#/components/callbacks/Done" },
            },
          },
        },
      },
      webhooks: {
        changed: {
          query: { operationId: "queryChange" },
        },
      },
      components: {
        callbacks: {
          Done: {
            "{$request.body#/url}": {
              post: { operationId: "receiveDone" },
              additionalOperations: {
                REPORT: { operationId: "reportDone" },
              },
            },
          },
        },
      },
    };

    const inventory = documentInboundOperationInventory(document);
    expect(inventory.map(({ reference }) => reference.ref)).toEqual([
      "#/paths/~1jobs/post/callbacks/done/{$request.body#~1url}/post",
      "#/paths/~1jobs/post/callbacks/done/{$request.body#~1url}/additionalOperations/REPORT",
      "#/webhooks/changed/query",
    ]);
    expect(inventory.map(({ reference }) => reference.wireMethod)).toEqual([
      "POST",
      "REPORT",
      "QUERY",
    ]);
    expect(inventory.map(({ target }) => target?.operation.operationId)).toEqual([
      "receiveDone",
      "reportDone",
      "queryChange",
    ]);
    expect(inventory.map(({ reference }) => reference.kind)).toEqual([
      "callback",
      "callback",
      "webhook",
    ]);

    const artifact = await loadOpenAPIArtifact({ content: document });
    expect(artifact.inboundOperationInventory().map(({ reference }) => reference.ref))
      .toEqual(inventory.map(({ reference }) => reference.ref));
  });

  it("keeps a 3.0 root webhooks spelling silent while retaining callbacks", () => {
    const inventory = documentInboundOperationInventory({
      openapi: "3.0.4",
      paths: {
        "/jobs": {
          post: {
            callbacks: {
              done: {
                "{$request.body#/url}": {
                  post: { operationId: "receiveDone" },
                  query: { operationId: "notA30FixedMethod" },
                },
              },
            },
          },
        },
      },
      webhooks: {
        ignored: { post: { operationId: "ignored" } },
      },
    });

    expect(inventory).toHaveLength(1);
    expect(inventory[0]?.target?.operation.operationId).toBe("receiveDone");
  });

  it("reports an unresolvable declaration without losing its source slot", () => {
    const inventory = documentInboundOperationInventory({
      openapi: "3.1.2",
      paths: {},
      webhooks: { missing: { $ref: "#/components/pathItems/Missing" } },
    });

    expect(inventory).toHaveLength(1);
    expect(inventory[0]?.reference).toMatchObject({
      ref: "#/webhooks/missing",
      kind: "webhook",
      name: "missing",
    });
    expect(inventory[0]?.error).toBeInstanceOf(Error);
  });
});
