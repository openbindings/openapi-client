// Round R2 F2: an EMPTY successful response on the 3.2 lane.
//
// `openbindings.openapi-3.2@1` §9.6 states the answer twice -- "An **empty
// response** has zero content octets after transfer decoding and content-coding
// decoding" and "Empty responses emit no operation output value" -- so an empty
// 2xx body loses nothing and completes, whatever `Content-Type` the peer
// happened to stamp on it.
//
// The 3.2-only sequential pre-check in `governOpenAPIResponse` ran BEFORE any
// byte was read, so it judged the media declaration of a response that turned
// out to carry no content at all and refused it as ERR_PROTOCOL. Go's 3.2 lane
// and both engines on 3.0/3.1 completed the same interaction: a
// one-engine-one-edition divergence, and the only cell in the eight-shape
// matrix where the two engines disagreed after Round R.

import { describe, expect, it } from "vitest";
import {
  governOpenAPIResponse,
  type OpenAPIResponseMechanicsModel,
} from "./response-mechanics.js";

type ResponseContent = NonNullable<
  NonNullable<NonNullable<OpenAPIResponseMechanicsModel["operation"]["responses"]>[string]>["content"]
>;

function model(edition: string, content: ResponseContent | undefined): OpenAPIResponseMechanicsModel {
  return {
    document: { openapi: edition },
    operation: {
      responses: { "200": { description: "ok", ...(content ? { content } : {}) } },
    },
    parameters: [],
    method: "get",
    emptyResponse: false,
  };
}

describe("an empty successful response carries absence", () => {
  // The peer stamps a `Content-Type` that matches no governing declaration --
  // which is what `new Response("")` does by default in any fetch stack -- and
  // the body is empty, so nothing about the declaration was contradicted.
  it.each([
    ["3.2.0", "text/plain;charset=UTF-8"],
    ["3.2.0", "application/json"],
    ["3.1.2", "text/plain;charset=UTF-8"],
    ["3.0.4", "text/plain;charset=UTF-8"],
  ])("completes on %s when the peer stamps %s on an empty body", async (edition, contentType) => {
    const governed = await governOpenAPIResponse(
      new Response("", { status: 200, headers: { "Content-Type": contentType } }),
      model(edition, undefined),
      new Map(),
    );
    expect(await governed.text()).toBe("");
  });

  // The same document and the same declaration with a NON-EMPTY body must still
  // be a loud protocol error: the fall-through has to keep reporting it, or the
  // fix would trade one divergence for a hole.
  it.each(["3.2.0", "3.1.2", "3.0.4"])(
    "still refuses a non-empty body whose media matches no declaration on %s",
    async (edition) => {
      await expect(governOpenAPIResponse(
        new Response("payload", { status: 200, headers: { "Content-Type": "text/plain" } }),
        model(edition, { "application/json": { schema: { type: "object" } } }),
        new Map(),
      )).rejects.toMatchObject({ code: "ERR_PROTOCOL" });
    },
  );

  // A genuine 3.2 sequential response still streams: the pre-check must keep
  // its job for the case it exists for.
  it("still admits a declared 3.2 sequential response", async () => {
    const governed = await governOpenAPIResponse(
      new Response("data: one\n\n", { status: 200, headers: { "Content-Type": "text/event-stream" } }),
      model("3.2.0", { "text/event-stream": { itemSchema: { type: "string" } } }),
      new Map(),
    );
    expect(governed.body).not.toBeNull();
  });
});
