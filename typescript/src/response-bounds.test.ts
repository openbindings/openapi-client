import { describe, expect, it } from "vitest";
import { OpenAPIClient } from "./index.js";

for (const edition of ["2.0", "3.0.4", "3.1.2", "3.2.0"]) {
  describe(`${edition} response ownership`, () => {
    for (const status of [200, 400]) for (const surface of ["call", "stream"] as const) {
      it(`${surface} terminates and cancels an oversized ${status} response`, async () => {
        let cancelled = false;
        const response = edition === "2.0" ? { description: "value", schema: {} }
          : { description: "value", content: { "application/json": { schema: {} } } };
        const document = {
          ...(edition === "2.0" ? { swagger: edition, host: "api.example.test", schemes: ["https"], produces: ["application/json"] }
            : { openapi: edition, servers: [{ url: "https://api.example.test" }] }),
          info: { title: "Bounds", version: "1" },
          paths: { "/value": { get: { operationId: "value", responses: { [status]: response } } } },
        };
        const client = await OpenAPIClient.load(document, {
          maxDeliveryUnitBytes: 4,
          fetch: async () => new Response(new ReadableStream<Uint8Array>({
            start(controller) { controller.enqueue(new TextEncoder().encode('"oversized"')); },
            cancel() { cancelled = true; },
          }), { status, headers: { "content-type": "application/json" } }),
        });
        const run = async () => {
          if (surface === "call") return client.call("value");
          const result = await client.stream("value");
          if (result.ok) {
            for await (const event of result.events) void event;
            await result.closed;
          }
          return result;
        };
        await expect(run()).rejects.toMatchObject({ kind: "response" });
        await expect.poll(() => cancelled, { timeout: 1000 }).toBe(true);
      }, 2000);
    }
  });
}
