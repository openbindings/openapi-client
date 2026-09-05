import { describe, expect, it } from "vitest";
import {
  governOpenAPIResponse,
  normalizeOpenAPIContentCodings,
  type OpenAPIContentDecoder,
  type OpenAPIResponseMechanicsModel,
} from "./response-mechanics.js";

function model(header: Record<string, unknown>): OpenAPIResponseMechanicsModel {
  return {
    document: { openapi: "3.2.0" },
    operation: {
      responses: {
        "200": {
          headers: { "Content-Encoding": header },
          content: { "text/plain": { schema: { type: "string" } } },
        },
      },
    },
    parameters: [],
    method: "get",
    emptyResponse: false,
  };
}

function unwrap(name: string, order: string[]): OpenAPIContentDecoder {
  return (body) => {
    order.push(name);
    const text = new TextDecoder().decode(body);
    const prefix = `${name}(`;
    if (!text.startsWith(prefix) || !text.endsWith(")")) throw new Error("bad coding");
    return new TextEncoder().encode(text.slice(prefix.length, -1));
  };
}

describe("OpenAPI 3.2 response content codings", () => {
  it("normalizes capability tokens and decodes the declared stack in reverse order", async () => {
    const order: string[] = [];
    const normalized = normalizeOpenAPIContentCodings({
      FIRST: unwrap("first", order),
      second: unwrap("second", order),
    }, "response");
    expect(normalized.defect).toBeUndefined();
    const response = await governOpenAPIResponse(new Response("second(first(payload))", {
      status: 200,
      headers: {
        "Content-Type": "text/plain",
        "Content-Encoding": "first, second",
      },
    }), model({
      required: true,
      schema: { type: "string", enum: ["first, second"] },
    }), normalized.codecs);

    expect(await response.text()).toBe("payload");
    expect(order).toEqual(["second", "first"]);
  });

  it("treats identity as built in and case-folds unconstrained Header declarations conjunctively", async () => {
    const identity = await governOpenAPIResponse(new Response("payload", {
      headers: { "Content-Type": "text/plain", "Content-Encoding": "identity" },
    }), model({ schema: { type: "string", enum: ["identity"] } }), new Map());
    expect(await identity.text()).toBe("payload");

    const ambiguous = model({ schema: { type: "string" } });
    ambiguous.operation.responses!["200"]!.headers!["content-encoding"] = {
      schema: { type: "string" },
    };
    const grouped = await governOpenAPIResponse(new Response("payload", {
      headers: { "Content-Type": "text/plain", "Content-Encoding": "identity" },
    }), ambiguous, new Map());
    expect(await grouped.text()).toBe("payload");
  });
});
