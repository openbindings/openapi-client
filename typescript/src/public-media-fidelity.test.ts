import {expect, it} from "vitest";
import {parseJSON, stringifyJSON} from "@openbindings/json";
import {OpenAPIClient} from "./client.js";

const raw = '{"id":9007199254740993,"huge":1e400,"tiny":1e-400,"money":0.10000000000000001,"nested":[null,[],{},"9007199254740993"]}';

for (const edition of ["3.0.4", "3.1.2", "3.2.0"]) {
  for (const media of ["multipart/form-data", "application/x-www-form-urlencoded"]) {
    it(`preserves public ${edition} JSON parts and parameter content in ${media}`, async () => {
      let requests = 0;
      const client = await OpenAPIClient.load({
        openapi: edition, info: {title: "Exact media", version: "1"}, servers: [{url: "https://example.invalid"}],
        paths: {"/x": {post: {
          operationId: "test",
          parameters: [{name: "q", in: "query", required: true, content: {"application/json": {schema: {}}}}],
          requestBody: {required: true, content: {[media]: {
            schema: {type: "object", properties: {payload: {type: "object"}}},
            encoding: {payload: {contentType: "application/json"}},
          }}}, responses: {"200": {description: "ok", content: {"application/json": {schema: {}}}}},
        }}},
      }, {fetch: async (input, init) => {
        requests++;
        const request = new Request(input, init);
        expect(stringifyJSON(parseJSON(new URL(request.url).searchParams.get("q")!))).toBe(raw);
        const fields = await request.formData();
        const payload = fields.get("payload");
        const encoded = typeof payload === "string" ? payload : await payload!.text();
        expect(stringifyJSON(parseJSON(encoded))).toBe(raw);
        return new Response(raw, {headers: {"Content-Type": "application/json"}});
      }});
      const result = await client.call("test", {body: {payload: parseJSON(raw)}, parameters: {query: {q: parseJSON(raw)}}});
      expect(result.ok).toBe(true);
      if (result.ok) expect(stringifyJSON(result.data)).toBe(raw);
      expect(requests).toBe(1);
    });
  }
}

for (const media of ["application/jsonl", "application/x-ndjson", "application/json-seq", "application/problem+json-seq"]) {
  it(`preserves values through the public existing ${media} response lane`, async () => {
    const items = [raw, "9007199254740993", "1e400", "1e-400", "null", "[]"];
    const wire = items.map(value => (media.endsWith("json-seq") ? "\x1e" : "") + value + "\n").join("");
    const client = await OpenAPIClient.load({
      openapi: "3.2.0", info: {title: "Exact sequence", version: "1"}, servers: [{url: "https://example.invalid"}],
      paths: {"/x": {get: {operationId: "test", responses: {"200": {content: {[media]: {itemSchema: {}}}}}}}},
    }, {fetch: async () => new Response(wire, {headers: {"Content-Type": media}})});
    const result = await client.stream("test");
    expect(result.ok).toBe(true);
    if (!result.ok) throw Error("expected successful sequence");
    const actual: string[] = [];
    for await (const event of result.events) actual.push(stringifyJSON(event.data));
    expect(actual).toEqual(items);
  });
}
