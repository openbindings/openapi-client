import { describe, expect, it, vi } from "vitest";
import { prepareSwagger20 } from "./swagger20-engine.js";
import { Swagger20Number } from "./swagger20-model.js";

const response204 = () => new Response(null, { status: 204 });

describe("native Swagger 2.0 parameter execution", () => {
  it("keeps cross-location identities distinct and percent-encodes data", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => response204());
    const prepared = await prepareSwagger20({
      source: { content: {
        swagger: "2.0",
        paths: { "/pets/{id}": { get: {
          parameters: [
            { name: "id", in: "path", required: true, type: "string" },
            { name: "id", in: "query", type: "string" },
            { name: "X-Ready", in: "header", type: "boolean" },
          ],
          responses: { 204: { description: "empty" } },
        } } },
      } },
      ref: "#/paths/~1pets~1{id}/get",
      server: "https://peer.example/root",
      fetch: fetchMock,
      parameterConverter: (value) => value === true ? "ready" : String(value),
    });
    await prepared.execute({ parameters: {
      path: { id: "a/b" },
      query: { id: "two words" },
      header: { "X-Ready": true },
    } });
    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe("https://peer.example/root/pets/a%2Fb?id=two%20words");
    expect(new Headers(init?.headers).get("X-Ready")).toBe("ready");
  });

  // A supplied array with ZERO members is an empty value under every
  // collectionFormat, `multi` included -- not an omission -- so it reaches the
  // same emptyValueForm choice a supplied empty string does.
  it("treats a zero-member array as an empty value under every collectionFormat", async () => {
    for (const [collectionFormat, form, want] of [
      ["csv", "name-only", "https://peer.example/pets?tags"],
      ["csv", "empty", "https://peer.example/pets?tags="],
      ["multi", "name-only", "https://peer.example/pets?tags"],
      ["multi", "empty", "https://peer.example/pets?tags="],
    ] as const) {
      const fetchMock = vi.fn<typeof fetch>(async () => response204());
      const prepared = await prepareSwagger20({
        source: { content: {
          swagger: "2.0",
          paths: { "/pets": { get: {
            parameters: [{ name: "tags", in: "query", type: "array", collectionFormat, allowEmptyValue: true, items: { type: "string" } }],
            responses: { 204: { description: "empty" } },
          } } },
        } },
        ref: "#/paths/~1pets/get",
        server: "https://peer.example",
        fetch: fetchMock,
        emptyValueForm: form,
      });
      await prepared.execute({ parameters: { query: { tags: [] } } });
      expect(fetchMock.mock.calls[0]?.[0]).toBe(want);
    }
  });

  it("refuses a zero-member array without allowEmptyValue rather than dropping it", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => response204());
    const prepared = await prepareSwagger20({
      source: { content: {
        swagger: "2.0",
        paths: { "/pets": { get: {
          parameters: [{ name: "tags", in: "query", type: "array", collectionFormat: "csv", items: { type: "string" } }],
          responses: { 204: { description: "empty" } },
        } } },
      } },
      ref: "#/paths/~1pets/get",
      server: "https://peer.example",
      fetch: fetchMock,
    });
    await expect(prepared.execute({ parameters: { query: { tags: [] } } })).rejects.toThrow(/empty value/u);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("preserves multi member order and structural delimiters", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => response204());
    const prepared = await prepareSwagger20({
      source: { content: {
        swagger: "2.0",
        paths: { "/pets": { get: {
          parameters: [{ name: "tag", in: "query", type: "array", collectionFormat: "multi", items: { type: "string" } }],
          responses: { 204: { description: "empty" } },
        } } },
      } },
      ref: "#/paths/~1pets/get",
      server: "https://peer.example",
      fetch: fetchMock,
    });
    await prepared.execute({ parameters: { query: { tag: ["a b", "c/d"] } } });
    expect(fetchMock.mock.calls[0]?.[0]).toBe("https://peer.example/pets?tag=a%20b&tag=c%2Fd");
  });

  it("retains the literal integer token when the host supplies it", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => response204());
    const prepared = await prepareSwagger20({
      source: { content: {
        swagger: "2.0",
        paths: { "/pets": { get: {
          parameters: [{ name: "count", in: "query", type: "integer" }],
          responses: { 204: { description: "empty" } },
        } } },
      } },
      ref: "#/paths/~1pets/get",
      server: "https://peer.example",
      fetch: fetchMock,
      parameterConverter: String,
    });
    await expect(prepared.execute({ parameters: { query: { count: new Swagger20Number("1.0") } } }))
      .rejects.toMatchObject({ code: "ERR_REFUSED" });
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
