import { describe, expect, it } from "vitest";
import { OpenAPIEngine, OpenAPIExecutionError } from "./engine.js";
import {
  convertParameterScalars,
  prepareSchemaParameterValue,
} from "./params.js";

const REF = "#/paths/~1items/get";

describe("OpenAPI parameter wire mechanics", () => {
  it("recursively applies the parameter converter and refuses null members", () => {
    const value = { flag: true, list: [2.5, "already-text"] };
    expect(() => convertParameterScalars(value, undefined)).toThrow(/parameterConversion/);
    expect(convertParameterScalars(value, (member) => `configured<${member}>`)).toEqual({
      flag: "configured<true>",
      list: ["configured<2.5>", "already-text"],
    });
    expect(() => convertParameterScalars(["ok", null], String)).toThrow(/null array\/object member/);
  });

  it("rejects CR/LF in serialized header parameters before dispatch", () => {
    expect(() => prepareSchemaParameterValue({
      name: "X-Test",
      in: "header",
      schema: { type: "string" },
    }, "safe\r\nInjected: yes", undefined)).toThrow(/invalid HTTP field byte/);
  });

  it("uses the public converter hook during native execution", async () => {
    const requests: Request[] = [];
    const prepared = await new OpenAPIEngine({ parameterConverter: () => "configured-seven" }).prepare({
      source: { content: document([{ name: "n", in: "query", schema: { type: "integer" } }]) },
      ref: REF,
      fetch: async (input, init) => {
        requests.push(input instanceof Request ? input : new Request(input, init));
        return new Response(null, { status: 204 });
      },
    });
    const execution = await prepared.start();
    await execution.send({ n: 7 });
    await execution.finishInput();
    await execution.completed;
    expect(requests[0]?.url).toBe("https://example.test/items?n=configured-seven");
  });

  it("carries a native raw Cookie header and refuses only an emitted structured collision", async () => {
    const requests: Request[] = [];
    const engine = new OpenAPIEngine();
    const source = {
      content: document([
        { name: "Cookie", in: "header", schema: { type: "string" } },
        { name: "session", in: "cookie", schema: { type: "string" } },
      ]),
    };
    const fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      requests.push(input instanceof Request ? input : new Request(input, init));
      return new Response(null, { status: 204 });
    };

    const rawOnly = await engine.prepare({ source, ref: REF, fetch });
    const first = await rawOnly.start();
    await first.send({ Cookie: "raw=1" });
    await first.finishInput();
    await first.completed;
    expect(requests[0]?.headers.get("Cookie")).toBe("raw=1");

    const colliding = await engine.prepare({ source, ref: REF, fetch });
    const second = await colliding.start();
    await second.send({ Cookie: "raw=1", session: "structured" });
    await second.finishInput();
    await expect(second.completed).rejects.toMatchObject({
      code: "ERR_REFUSED",
    } satisfies Partial<OpenAPIExecutionError>);
    expect(requests).toHaveLength(1);
  });
});

function document(parameters: Array<Record<string, unknown>>): Record<string, unknown> {
  return {
    openapi: "3.1.0",
    info: { title: "parameter mechanics", version: "1" },
    servers: [{ url: "https://example.test" }],
    paths: {
      "/items": {
        get: {
          parameters,
          responses: { "204": { description: "empty" } },
        },
      },
    },
  };
}
