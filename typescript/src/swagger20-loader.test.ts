import { describe, expect, it, vi } from "vitest";
import { loadSwagger20 } from "./swagger20-loader.js";
import { prepareSwagger20, Swagger20ExecutionError, validateSwagger20Selector } from "./swagger20-engine.js";

describe("native Swagger 2.0 load, reference, and selector lane", () => {
  it("owns the exact swagger gate without falling through to openapi", async () => {
    await expect(loadSwagger20({
      content: { swagger: "2.0.1", openapi: "3.1.2", paths: {} },
    })).rejects.toThrow(/exact string/);
    const loaded = await loadSwagger20({
      content: { swagger: "2.0", openapi: "3.1.2", paths: {} },
    });
    expect(loaded.document.swagger).toBe("2.0");
  });

  it("keeps whole-source refusal at preparation rather than load", async () => {
    await expect(loadSwagger20({ content: { swagger: "2.0" } })).resolves.toBeDefined();
    await expect(prepareSwagger20({
      source: { content: { swagger: "2.0" } },
      ref: "#/paths/~1pets/get",
    })).rejects.toMatchObject({ code: "ERR_REFUSED" });
  });

  it("rejects a multi-document YAML stream", async () => {
    await expect(loadSwagger20({ content: 'swagger: "2.0"\npaths: {}\n---\n{}\n' }))
      .rejects.toThrow(/exactly one YAML document/);
  });

  it("never percent-decodes a selector path token", async () => {
    await expect(prepareSwagger20({
      source: { content: { swagger: "2.0", paths: { "/pets": { get: {} } } } },
      ref: "#/paths/%2Fpets/get",
    })).rejects.toMatchObject({ code: "OPERATION_NOT_FOUND" });
    expect(() => validateSwagger20Selector("#/paths/~1pets/GET")).toThrow(/lowercase/);
  });

  it("loads only the selected external closure and terminates its cycle", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url === "https://example.test/a.json") {
        return new Response(JSON.stringify({ path: { $ref: "b.json#/path" } }));
      }
      if (url === "https://example.test/b.json") {
        return new Response(JSON.stringify({ path: { $ref: "a.json#/path" } }));
      }
      throw new Error(`unexpected fetch ${url}`);
    });
    await expect(prepareSwagger20({
      source: {
        location: "https://example.test/root.json",
        content: { swagger: "2.0", paths: { "/pets": { $ref: "a.json#/path" } } },
      },
      ref: "#/paths/~1pets/get",
      fetch: fetchMock,
    })).rejects.toBeInstanceOf(Swagger20ExecutionError);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
