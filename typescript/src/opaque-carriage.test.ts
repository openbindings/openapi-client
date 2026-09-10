import {expect, it} from "vitest";
import {parseJSON, stringifyJSON} from "@openbindings/json";
import {OpenAPIClient} from "./client.js";
import {dereference} from "./internal/deref.js";
import type {OpenAPIDocument} from "./types.js";

it("preserves exact values in the default external-resource parser", async () => {
  const document = await dereference({item: {$ref: "https://example.invalid/values#/item"}}, {
    fetch: async () => new Response('{"item":{"const":9007199254740993,"default":1e400}}'),
  });
  expect(stringifyJSON(document)).toBe('{"item":{"const":9007199254740993,"default":1e400}}');
});

it("detaches native security-handler metadata without changing values", async () => {
  const doc = parseJSON('{"openapi":"3.1.2","info":{"title":"Exact","version":"1"},"servers":[{"url":"https://example.invalid"}],"components":{"securitySchemes":{"custom":{"type":"http","scheme":"custom","x-id":9007199254740993}}},"paths":{"/x":{"get":{"operationId":"test","security":[{"custom":[]}],"responses":{"204":{"description":"ok"}}}}}}');
  const client = await OpenAPIClient.load(doc as OpenAPIDocument, {fetch: async () => new Response(null,{status:204})});
  let calls = 0;
  for (let repeat = 0; repeat < 2; repeat++) {
    const result = await client.call("test", {}, {auth: {custom({scheme}) {
      calls++; expect(stringifyJSON(scheme["x-id"])).toBe("9007199254740993");
      Reflect.set(scheme, "x-id", "caller changed the detached metadata");
    }}});
    expect(result.ok).toBe(true);
  }
  expect(calls).toBe(2);
});
