import { describe, expect, it } from "vitest";
import { analyzeOpenAPIProjection } from "./provider.js";

describe("declared scalar response projection", () => {
  for (const type of ["integer", "number", "boolean", "string"]) {
    for (const referenced of [false, true]) {
      it(`${type}, reference=${referenced}`, async () => {
        const schema = { type, description: "declared scalar" };
        const result = await analyzeOpenAPIProjection({ content: {
          openapi: "3.2.0", info: { title: "Scalar", version: "1" },
          servers: [{ url: "https://api.example" }],
          components: { schemas: { Value: schema } },
          paths: { "/scalar": { get: { operationId: "read", responses: {
            "200": { description: "ok", content: { "text/plain": {
              schema: referenced ? { $ref: "#/components/schemas/Value" } : schema,
            } } },
          } } } },
        } });
        expect(result.openapi3?.operations.read?.output).toEqual(schema);
      });
    }
  }
});
