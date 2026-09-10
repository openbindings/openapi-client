import { describe, expect, it } from "vitest";
import { OpenAPIClient } from "./index.js";

describe("3.2 public request Encoding header closure", () => {
  it.each(["control", "ignored-key", "fixed-header", "chained-header", "ignored-content-type", "positional-content-type", "unavailable-header"])("%s", async which => {
    const positional = which === "positional-content-type";
    const fixed = which === "fixed-header" || which === "chained-header";
    const ignored = which.includes("content-type");
    const encoding = { headers: { [ignored ? "cOnTeNt-TyPe" : "X-Fixed"]: { $ref: "https://other.example/header" } } };
    const media = positional
      ? { itemSchema: { type: "string" }, itemEncoding: encoding }
      : { schema: { type: "object", properties: { x: { type: "string" } } }, encoding: which === "control" ? {} : { [which === "ignored-key" ? "ghost" : "x"]: encoding } };
    const source = { openapi: "3.2.0", info: { title: which, version: "1" }, servers: [{ url: "https://api.example" }], paths: { "/x": { post: { requestBody: { required: true, content: { [positional ? "multipart/mixed" : "multipart/form-data"]: media } }, responses: { "204": { description: "ok" } } } } } };
    const reads: string[] = [], bodies: string[] = [];
    const client = await OpenAPIClient.load(source, {
      documentFetch: async input => {
        reads.push(String(input));
        if (which === "chained-header" && String(input).endsWith("/header")) return new Response('{"$ref":"https://other.example/final-header"}');
        // required:boolean makes this a non-Schema secondary root. Ignored
        // fields must never acquire it; an active header must not disappear.
        return new Response(JSON.stringify({ ...(fixed ? {} : { required: true }), schema: { type: "string", const: "fixed" } }));
      },
      fetch: async (input, init) => {
        bodies.push(await new Request(input, init).text());
        return new Response(null, { status: 204 });
      },
    });
    if (which === "unavailable-header") {
      await expect(client.call({ ref: "#/paths/~1x/post" }, { body: { x: "value" } })).rejects.toThrow();
      expect(bodies).toEqual([]);
      expect(reads).toEqual(["https://other.example/header"]);
    } else {
      const result = await client.call({ ref: "#/paths/~1x/post" }, { body: positional ? ["value"] : { x: "value" } });
      expect(result.ok).toBe(true);
      expect(bodies).toHaveLength(1);
      expect(bodies[0]!.includes("X-Fixed: fixed\r\n")).toBe(fixed);
      expect(reads).toEqual(which === "chained-header" ? ["https://other.example/header", "https://other.example/final-header"] : fixed ? ["https://other.example/header"] : []);
    }
  });
});
