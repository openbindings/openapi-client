import { describe, expect, it } from "vitest";
import { stringifyJSON } from "@openbindings/json";
import { parseJSONOrYAML } from "./util.js";
import { parseSwagger20Resource } from "./swagger20-loader.js";
import { analyzeOpenAPIProjection } from "./provider.js";

describe("exact artifact source values", () => {
  for (const parse of [parseJSONOrYAML, parseSwagger20Resource]) {
    for (const token of ["9007199254740993", "0.10000000000000001", "1e400", "1e-400"]) {
      it(`${parse.name} preserves ${token} in JSON and YAML`, () => {
        expect(stringifyJSON(parse(`{"value":${token}}`))).toBe(`{"value":${token}}`);
        expect(stringifyJSON(parse(`value: ${token}`))).toBe(`{"value":${token}}`);
      });
    }
    it(`${parse.name} retains scalar mapping names and rejects duplicates`, () => {
      expect(stringifyJSON(parse("9007199254740993: 0x20000000000001")))
        .toBe('{"9007199254740993":9007199254740993}');
      expect(() => parse("a: 1\na: 2")).toThrow();
      expect(() => parse("value: .inf")).toThrow();
      expect(stringifyJSON(parse("value: '1e400'"))).toBe('{"value":"1e400"}');
    });
  }
});

for (const edition of ["2.0", "3.0.4", "3.1.2", "3.2.0"]) {
  it(`${edition} carries exact schema values into detached projection`, async () => {
    const raw = edition === "2.0"
      ? '{"swagger":"2.0","info":{"title":"Exact","version":"1"},"paths":{"/x":{"get":{"operationId":"read","responses":{"200":{"description":"ok","schema":{"type":"number","minimum":9007199254740993,"example":1e400}}}}}}}'
      : `{"openapi":"${edition}","info":{"title":"Exact","version":"1"},"paths":{"/x":{"get":{"operationId":"read","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"number","minimum":9007199254740993,"example":1e400}}}}}}}}}`;
    const projected = await analyzeOpenAPIProjection({ content: raw });
    const text = stringifyJSON(projected);
    expect(text).toContain('"minimum":9007199254740993');
    expect(text).toContain('"example":1e400');
    expect(text).not.toContain('"rawJSON"');
  });
}
