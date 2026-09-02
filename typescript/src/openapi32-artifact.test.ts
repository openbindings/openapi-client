import { describe, expect, it } from "vitest";
import {
  classifyOpenAPIEdition,
  loadOpenAPIArtifact,
} from "./openapi32-artifact.js";
import { OpenAPIOperationResolutionError } from "./openapi32-operations.js";

function resourceFetch(resources: Record<string, unknown>, requests: string[] = []): typeof fetch {
  return async (input) => {
    const url = input instanceof Request ? input.url : String(input);
    requests.push(url);
    const resource = resources[url];
    return resource === undefined
      ? new Response("missing", { status: 404 })
      : new Response(typeof resource === "string" ? resource : JSON.stringify(resource), { status: 200 });
  };
}

describe("OpenAPI 3.2 artifact lane", () => {
  it("classifies the exact edition before resolving any reference", async () => {
    expect(classifyOpenAPIEdition({ openapi: "3.2.0", paths: {} })).toBe("3.2.0");
    const requests: string[] = [];
    await expect(loadOpenAPIArtifact({ content: `
openapi: 3.2.1
paths:
  /x: {$ref: https://resources.example/path-item.yaml}
` }, { fetch: resourceFetch({}, requests) })).rejects.toThrow(/unsupported OpenAPI version "3\.2\.1"/u);
    expect(requests).toEqual([]);
  });

  it.each([
    "openapi: 3.2.0\nopenapi: 3.2.0\npaths: {}\n",
    "openapi: 3.2.0\npaths: {}\n---\nopenapi: 3.2.0\npaths: {}\n",
    "openapi: 3.2.0\npaths: {}\nx-bad:\n  ? [a, b]\n  : value\n",
    "- openapi\n- 3.2.0\n",
    "paths: {}\n",
    "openapi: 32\npaths: {}\n",
  ])("refuses a source outside the closed representation/root/edition gates", async (content) => {
    await expect(loadOpenAPIArtifact({ content })).rejects.toThrow();
  });

  it("carries $self identity and uses it as the selected-reference base", async () => {
    const requests: string[] = [];
    const artifact = await loadOpenAPIArtifact({
      location: "https://retrieval.example/openapi.yaml",
      content: `
openapi: 3.2.0
$self: https://identity.example/descriptions/root.yaml
paths:
  /x: {$ref: path-item.yaml}
`,
    }, {
      fetch: resourceFetch({
        "https://identity.example/descriptions/path-item.yaml": "get: {}",
      }, requests),
    });
    expect(artifact.openAPI32).toEqual({
      retrievalURI: "https://retrieval.example/openapi.yaml",
      identityURI: "https://identity.example/descriptions/root.yaml",
      self: "https://identity.example/descriptions/root.yaml",
    });
    await expect(artifact.resolveOperation("#/paths/~1x/get")).resolves.toMatchObject({
      reference: { path: "/x", method: "get", wireMethod: "GET" },
    });
    expect(requests).toEqual(["https://identity.example/descriptions/path-item.yaml"]);
  });

  it("confines Path Item collisions and unreachable broken references to selected closures", async () => {
    const artifact = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      components: {
        pathItems: { Shared: { get: {}, post: {} } },
      },
      paths: {
        "/selected": { $ref: "#/components/pathItems/Shared", get: {} },
        "/unused": { $ref: "#/components/pathItems/Shared", post: {} },
        "/broken": { post: { requestBody: { $ref: "#/components/requestBodies/Missing" } } },
      },
    } });
    await expect(artifact.resolveOperation("#/paths/~1selected/get")).rejects.toThrow(/collision/u);
    await expect(artifact.resolveOperation("#/paths/~1unused/get")).resolves.toBeDefined();
    await expect(artifact.resolveOperation("#/paths/~1broken/post")).rejects.toMatchObject({ kind: "excluded" });
  });

  it("enforces $self and $id identity only through the selected request closure", async () => {
    const alias = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      paths: {
        "/x": { post: { requestBody: { $ref: "https://retrieval.example/library.yaml#/components/requestBodies/Payload" } } },
      },
    } }, { fetch: resourceFetch({
      "https://retrieval.example/library.yaml": {
        openapi: "3.2.0",
        $self: "https://identity.example/library.yaml",
        components: { requestBodies: { Payload: { content: { "application/json": { schema: {} } } } } },
      },
    }) });
    await expect(alias.resolveOperation("#/paths/~1x/post")).rejects.toThrow(/retrieval alias/u);

    const schema = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      components: {
        schemas: {
          Resource: {
            $id: "https://schemas.example/resource",
            properties: { name: { type: "string" } },
          },
        },
      },
      paths: {
        "/x": { post: { requestBody: { content: { "application/json": {
          schema: { $ref: "#/components/schemas/Resource/properties/name" },
        } } } } },
      },
    } });
    await expect(schema.resolveOperation("#/paths/~1x/post")).rejects.toThrow(/\$id/u);
  });

  it("keeps post-load refusal, dialect exclusion, and target exclusion distinct", async () => {
    const noSurface = await loadOpenAPIArtifact({ content: { openapi: "3.2.0", info: {} } });
    expect(noSurface.refusal).toMatch(/addressable-target position/u);
    expect(noSurface.sourceExclusion).toBeUndefined();

    const dialect = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      jsonSchemaDialect: "https://json-schema.org/draft/2020-12/schema",
      paths: {},
    } });
    expect(dialect.refusal).toBeUndefined();
    expect(dialect.sourceExclusion).toMatch(/default dialect/u);

    const responses = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      paths: { "/omitted": { get: {} }, "/empty": { get: { responses: {} } } },
    } });
    await expect(responses.resolveOperation("#/paths/~1omitted/get")).resolves.toBeDefined();
    await expect(responses.resolveOperation("#/paths/~1empty/get")).rejects.toMatchObject({
      kind: "excluded",
    });
  });

  it("resolves cyclic selected schemas and supports immutable prepared target views", async () => {
    const artifact = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      components: {
        schemas: {
          Node: { type: "object", properties: { next: { $ref: "#/components/schemas/Node" } } },
        },
      },
      paths: { "/nodes": { post: { requestBody: { content: {
        "application/json": { schema: { $ref: "#/components/schemas/Node" } },
      } } } } },
    } });
    const target = await artifact.resolveOperation("#/paths/~1nodes/post");
    const schema = target.operation.requestBody?.content?.["application/json"]?.schema as Record<string, unknown>;
    expect((schema.properties as Record<string, unknown>).next).toBe(schema);

    const operation = { ...target.operation, operationId: "prepared" };
    const prepared = artifact.withOperationTarget({ ...target, operation });
    expect((await prepared.resolveOperation(target.reference.ref)).operation.operationId).toBe("prepared");
    expect((await artifact.resolveOperation(target.reference.ref)).operation.operationId).toBeUndefined();
  });

  it("classifies selector errors independently of target lookup", async () => {
    const artifact = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      paths: { "/x": { query: {}, additionalOperations: { COPY: {}, get: {}, GET: {} } } },
    } });
    await expect(artifact.resolveOperation("#/paths/~1x/query")).resolves.toMatchObject({
      reference: { wireMethod: "QUERY" },
    });
    await expect(artifact.resolveOperation("#/paths/~1x/additionalOperations/COPY")).resolves.toMatchObject({
      reference: { wireMethod: "COPY", additional: true },
    });
    await expect(artifact.resolveOperation("#/paths/~1x/QUERY")).rejects.toBeInstanceOf(OpenAPIOperationResolutionError);
    // §6.1: `get` spells a method token the fixed `get` field does not send, so
    // it is admitted; only the byte-exact `GET` is the declaration defect.
    await expect(artifact.resolveOperation("#/paths/~1x/additionalOperations/get")).resolves.toMatchObject({
      reference: { wireMethod: "get", additional: true },
    });
    await expect(artifact.resolveOperation("#/paths/~1x/additionalOperations/GET")).rejects.toMatchObject({
      kind: "excluded",
    });
  });
});
