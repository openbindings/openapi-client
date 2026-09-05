import { describe, expect, it } from "vitest";
import { loadOpenAPIArtifact } from "./openapi32-artifact.js";
import {
  openAPI32ParameterSerializationMethod,
  serializeOpenAPI32CookieValue,
  serializeOpenAPI32QueryStringParameter,
  serializeOpenAPI32QueryValue,
  validateOpenAPI32ParameterSerialization,
} from "./openapi32-parameters.js";
import type { OpenAPIParameter } from "./types.js";

const schemas = {
  primitive: { type: "string" },
  array: { type: "array", items: { type: "string" } },
  object: { type: "object", properties: { member: { type: "string" } } },
} as const;

describe("OpenAPI 3.2 parameter surface", () => {
  it("serializes querystring, protected query delimiters, and cookie pairs exactly", () => {
    expect(serializeOpenAPI32QueryStringParameter({
      name: "whole",
      in: "querystring",
      content: {
        "application/x-www-form-urlencoded": {
          schema: { type: "object", properties: { page: { type: "string" }, tag: { type: "string" } } },
        },
      },
    }, { page: "a b", tag: "x/y" })).toBe("page=a+b&tag=x%2Fy");
    expect(serializeOpenAPI32QueryStringParameter({
      name: "whole",
      in: "querystring",
      content: { "application/json": { schema: { type: "object" } } },
    }, { a: "x&y" })).toBe("%7B%22a%22%3A%22x%5Cu0026y%22%7D");
    expect(serializeOpenAPI32QueryValue(
      "q",
      "a/b?c#d&e=f+g[h]",
      "form",
      true,
      true,
    )).toEqual(["q=a/b?c%23d%26e%3Df%2Bg%5Bh%5D"]);
    expect(serializeOpenAPI32QueryValue("q", ["a", "b"], "pipeDelimited", false, false))
      .toEqual(["q=a%7Cb"]);
    expect(serializeOpenAPI32QueryValue("filter", { kind: "value" }, "deepObject", false, false))
      .toEqual(["filter%5Bkind%5D=value"]);
    expect(serializeOpenAPI32CookieValue("parts", ["a", "b"], "cookie", true))
      .toEqual(["parts=a", "parts=b"]);
  });

  it("defaults explicit cookie style to explode=true per the document adjudication", () => {
    const parameter: OpenAPIParameter = {
      name: "parts",
      in: "cookie",
      style: "cookie",
      schema: schemas.array,
    };
    expect(openAPI32ParameterSerializationMethod(parameter)).toEqual({ style: "cookie", explode: true });
    const method = openAPI32ParameterSerializationMethod(parameter);
    expect(serializeOpenAPI32CookieValue("parts", ["a", "b"], method.style, method.explode))
      .toEqual(["parts=a", "parts=b"]);
  });

  it("copies the Go twin's closed style table with the adjudicated default layered above it", () => {
    const rows = [
      ["path", "matrix", ["primitive", "array", "object"], [false, true]],
      ["path", "label", ["primitive", "array", "object"], [false, true]],
      ["path", "simple", ["primitive", "array", "object"], [false, true]],
      ["query", "form", ["primitive", "array", "object"], [false, true]],
      ["query", "spaceDelimited", ["array", "object"], [false]],
      ["query", "pipeDelimited", ["array", "object"], [false]],
      ["query", "deepObject", ["object"], [false, true]],
      ["header", "simple", ["primitive", "array", "object"], [false, true]],
      ["cookie", "form", ["primitive", "array", "object"], [false, true]],
      ["cookie", "cookie", ["primitive", "array", "object"], [false, true]],
    ] as const;
    for (const [location, style, shapes, explodes] of rows) {
      for (const shape of shapes) {
        for (const explode of explodes) {
          expect(() => validateOpenAPI32ParameterSerialization({
            name: "value",
            in: location,
            style,
            explode,
            schema: schemas[shape],
          })).not.toThrow();
        }
      }
    }

    const disallowed = [
      ["path", "form", "primitive", false],
      ["header", "label", "primitive", false],
      ["cookie", "simple", "primitive", false],
      ["query", "matrix", "primitive", false],
      ["query", "spaceDelimited", "primitive", false],
      ["query", "spaceDelimited", "array", true],
      ["query", "pipeDelimited", "object", true],
      ["query", "deepObject", "array", true],
    ] as const;
    for (const [location, style, shape, explode] of disallowed) {
      expect(() => validateOpenAPI32ParameterSerialization({
        name: "value",
        in: location,
        style,
        explode,
        schema: schemas[shape],
      })).toThrow();
    }
  });

  it.each([
    ["query collision", [
      { name: "whole", in: "querystring", content: { "application/json": { schema: { type: "object" } } } },
      { name: "q", in: "query", schema: { type: "string" } },
    ], "/x"],
    ["two querystrings", [
      { name: "one", in: "querystring", content: { "application/json": { schema: { type: "object" } } } },
      { name: "two", in: "querystring", content: { "application/json": { schema: { type: "object" } } } },
    ], "/x"],
    ["querystring schema-form field", [
      { name: "whole", in: "querystring", allowReserved: false, content: { "application/json": { schema: { type: "object" } } } },
    ], "/x"],
    ["querystring sequential media", [
      { name: "whole", in: "querystring", content: { "application/json": { itemSchema: { type: "object" } } } },
    ], "/x"],
    ["undefined style cell", [
      { name: "q", in: "query", style: "spaceDelimited", explode: true, schema: schemas.array },
    ], "/x"],
    ["compound member", [
      { name: "q", in: "query", style: "form", explode: false, schema: {
        type: "object", properties: { nested: { type: "object" } },
      } },
    ], "/x"],
    ["unmatched path parameter", [
      { name: "id", in: "path", required: true, schema: { type: "string" } },
    ], "/x"],
    ["duplicate expression", [
      { name: "id", in: "path", required: true, schema: { type: "string" } },
    ], "/{id}/{id}"],
  ])("confines %s exclusion to the selected target", async (_name, parameters, path) => {
    const artifact = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      paths: {
        [path]: { get: { parameters } },
        "/survivor": { get: {} },
      },
    } });
    const ref = `#/paths/${path.replaceAll("~", "~0").replaceAll("/", "~1")}/get`;
    await expect(artifact.resolveOperation(ref)).rejects.toMatchObject({ kind: "excluded" });
    await expect(artifact.resolveOperation("#/paths/~1survivor/get")).resolves.toBeDefined();
  });

  it("keeps a form/exploded cookie declaration represented because zero or one emitted pair is usable", async () => {
    const artifact = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      paths: {
        "/x": { get: { parameters: [
          { name: "c", in: "cookie", style: "form", explode: true, schema: schemas.array },
        ] } },
      },
    } });
    await expect(artifact.resolveOperation("#/paths/~1x/get")).resolves.toBeDefined();
  });

  it("excludes both members of an equivalent templated path hierarchy", async () => {
    const artifact = await loadOpenAPIArtifact({ content: {
      openapi: "3.2.0",
      paths: {
        "/items/{id}": { get: { parameters: [
          { name: "id", in: "path", required: true, schema: { type: "string" } },
        ] } },
        "/items/{name}": { get: { parameters: [
          { name: "name", in: "path", required: true, schema: { type: "string" } },
        ] } },
      },
    } });
    await expect(artifact.resolveOperation("#/paths/~1items~1{id}/get")).rejects.toThrow(/same templated hierarchy/u);
    await expect(artifact.resolveOperation("#/paths/~1items~1{name}/get")).rejects.toThrow(/same templated hierarchy/u);
  });
});
