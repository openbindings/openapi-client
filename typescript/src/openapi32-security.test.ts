import { describe, expect, it, vi } from "vitest";

import { loadOpenAPIArtifact } from "./openapi32-artifact.js";
import {
  openAPI32SecurityNameKind,
  openAPI32SecurityRequirementNames,
  openAPI32SecurityScheme,
} from "./openapi32-security.js";

describe("OpenAPI 3.2 security requirement identity", () => {
  it("classifies component names before URI names and honors ./ disambiguation", () => {
    expect(openAPI32SecurityRequirementNames([
      { local: [], "./security.yaml#/Key": [] },
      { local: [], "https://identity.example/root.yaml#/Key": [] },
    ])).toEqual([
      "./security.yaml#/Key",
      "https://identity.example/root.yaml#/Key",
      "local",
    ]);
    expect(openAPI32SecurityNameKind("local", new Set(["local"]), new Set(), false)).toBe("entry");
    expect(openAPI32SecurityNameKind("local", new Set(), new Set(["local"]), true)).toBe("referring");
    expect(openAPI32SecurityNameKind("./local", new Set(), new Set(["./local"]), true)).toBe("uri");
  });

  it("admits the closed request-security scheme shapes", () => {
    expect(openAPI32SecurityScheme({ type: "apiKey", in: "header", name: "X-Key" }))
      .toMatchObject({ type: "apiKey" });
    expect(openAPI32SecurityScheme({ type: "mutualTLS" })).toMatchObject({ type: "mutualTLS" });
    expect(openAPI32SecurityScheme({ type: "apiKey", in: "nowhere" })).toBeNull();
    expect(openAPI32SecurityScheme({ type: "unknown" })).toBeNull();
  });

  it("resolves absolute, relative, and nested security references under the authored key", async () => {
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url === "https://identity.example/security.yaml") {
        return new Response(JSON.stringify({ Key: { $ref: "./actual.yaml#/Actual" } }));
      }
      if (url === "https://identity.example/actual.yaml") {
        return new Response(JSON.stringify({
          Actual: { type: "apiKey", in: "query", name: "access_key" },
        }));
      }
      throw new Error(`unexpected resource ${url}`);
    });
    const absolute = "https://identity.example/root.yaml#/components/securitySchemes/EntryKey";
    const relative = "./security.yaml#/Key";
    const artifact = await loadOpenAPIArtifact({
      location: "https://identity.example/root.yaml",
      content: {
        openapi: "3.2.0",
        components: {
          securitySchemes: {
            EntryKey: { type: "apiKey", in: "header", name: "X-URI-Key" },
          },
        },
        paths: {
          "/absolute": { get: { security: [{ [absolute]: [] }] } },
          "/relative": { get: { security: [{ [relative]: [] }] } },
        },
      },
    }, { fetch: fetchFn, allowExternalRefs: true });

    const absoluteTarget = await artifact.resolveOperation("#/paths/~1absolute/get");
    expect(absoluteTarget.operation.security).toEqual([{ [absolute]: [] }]);
    const absoluteComponents = absoluteTarget.document.components as {
      securitySchemes?: Record<string, unknown>;
    };
    expect(absoluteComponents.securitySchemes?.[absolute])
      .toMatchObject({ type: "apiKey", in: "header", name: "X-URI-Key" });

    const relativeTarget = await artifact.resolveOperation("#/paths/~1relative/get");
    expect(relativeTarget.operation.security).toEqual([{ [relative]: [] }]);
    const relativeComponents = relativeTarget.document.components as {
      securitySchemes?: Record<string, unknown>;
    };
    expect(relativeComponents.securitySchemes?.[relative])
      .toMatchObject({ type: "apiKey", in: "query", name: "access_key" });
    expect(fetchFn).toHaveBeenCalledTimes(2);
  });
});
