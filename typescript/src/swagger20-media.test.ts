import { describe, expect, it, vi } from "vitest";
import { prepareSwagger20 } from "./swagger20-engine.js";

describe("native Swagger 2.0 media execution", () => {
  it("encodes a JSON body and decodes a JSON response", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (_input, init) => {
      expect(new Headers(init?.headers).get("Content-Type")).toBe("application/json");
      expect(new TextDecoder().decode(init?.body as ArrayBuffer)).toBe('{"name":"Ada"}');
      return new Response('{"stored":true}', { status: 200, headers: { "Content-Type": "application/json" } });
    });
    const prepared = await prepareSwagger20({
      source: { content: {
        swagger: "2.0",
        consumes: ["application/json"],
        produces: ["application/json"],
        paths: { "/pets": { post: {
          parameters: [{ name: "payload", in: "body", required: true, schema: { type: "object" } }],
          responses: { 200: { description: "ok", schema: { type: "object" } } },
        } } },
      } },
      ref: "#/paths/~1pets/post",
      server: "https://peer.example",
      fetch: fetchMock,
    });
    await expect(prepared.execute({ body: { name: "Ada" }, bodyPresent: true })).resolves.toMatchObject({
      outputPresent: true,
      output: { stored: true },
    });
  });

  it("keeps raw-octet and format-byte carriage distinct", async () => {
    const bodies: string[] = [];
    const fetchMock = vi.fn<typeof fetch>(async (_input, init) => {
      bodies.push(bytesToBase64(new Uint8Array(init?.body as ArrayBuffer)));
      return new Response(null, { status: 204 });
    });
    for (const [format, body] of [["binary", "AAH+/w=="], ["byte", "YWJj"]] as const) {
      const prepared = await prepareSwagger20({
        source: { content: {
          swagger: "2.0", consumes: ["application/octet-stream"],
          paths: { "/x": { put: {
            parameters: [{ name: "payload", in: "body", required: true, schema: { type: "string", format } }],
            responses: { 204: { description: "ok" } },
          } } },
        } },
        ref: "#/paths/~1x/put", server: "https://peer.example", fetch: fetchMock,
      });
      await prepared.execute({ body, bodyPresent: true });
    }
    expect(bodies).toEqual(["AAH+/w==", "WVdKag=="]);
  });
});

function bytesToBase64(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes));
}
