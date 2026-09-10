import { describe, expect, it } from "vitest";
import { OpenAPIClient } from "./client.js";
import { stringifyRequestJSON } from "./request-json.js";

describe("JSON request native nonfinite admission", () => {
  for (const edition of ["2.0", "3.0.4", "3.1.2", "3.2.0"]) {
    for (const value of [Infinity, -Infinity, NaN]) {
      it(`${edition} rejects nested ${String(value)} before dispatch`, async () => {
        let dispatches = 0;
        const schema = { type: "object", additionalProperties: true };
        const op = { operationId: "createItem", responses: { "204": { description: "ok" } },
          ...(edition === "2.0" ? { parameters: [{ name: "body", in: "body", required: true, schema }] }
            : { requestBody: { required: true, content: { "application/json": { schema } } } }) };
        const document = { info: { title: "Nonfinite", version: "1" }, paths: { "/items": { post: op } },
          ...(edition === "2.0" ? { swagger: edition, host: "fixture.invalid", schemes: ["https"], consumes: ["application/json"] }
            : { openapi: edition, servers: [{ url: "https://fixture.invalid" }] }) };
        const client = await OpenAPIClient.load(document, { fetch: async () => { dispatches++; return new Response(null, { status: 204 }); } });
        await expect(client.operation("createItem").call({ body: { nested: [value] } })).rejects.toBeDefined();
        expect(dispatches).toBe(0);
      });
    }
  }
});

describe("request JSON serializer controls", () => {
  it.each([null, false, "", 0, -0, 0.1, Number.MAX_VALUE, Number.MIN_VALUE, { nested: [null, 0.1] }])(
    "retains the existing JSON image of %j", value => {
      expect(stringifyRequestJSON(value)).toBe(JSON.stringify(value));
    },
  );
  it("rejects foreign serialization hooks without invoking them", () => {
    let calls = 0;
    expect(() => stringifyRequestJSON({ toJSON: () => { calls++; return { value: Infinity }; } })).toThrow();
    expect(calls).toBe(0);
  });
});
