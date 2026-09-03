import { describe, expect, it, vi } from "vitest";
import { OpenAPIEngine, OPENAPI_PROFILE_FULL } from "./engine.js";

// An authored `anyOf: [{}, {not: {}}]` at a form property or multipart part
// is a choice with two candidates under Section 5.2 of the 3.x binding
// specifications: a choice skips only a branch whose resolved declaration
// declares only `null`, `not` never participates in resolution -- so
// `{not: {}}` is typeless and a candidate beside `{}` -- and the choice
// supplies a single resolved member declaration only when exactly one
// candidate remains. No single member means no Encoding default row
// (Section 9.3) and no part carriage, so a value supplied for that member
// refuses before dispatch as the plain species, exactly as
// `oneOf: [{type: string}, {type: integer}]` does. Until 2026-09-02 this
// engine read the structure as a literal `true` in `media.ts` (a mirror of the
// Go loader's encoding of a literal) while `resolved-declaration.ts` read it
// as ambiguous; on the 3.2 urlencoded lane the first reading won and the
// member was DISPATCHED as a typeless field.
//
// Every cell runs through the shipped engine on a whole document.

const AMBIGUOUS = { anyOf: [{}, { not: {} }] };

function documentFor(edition: string, media: string, part: unknown): Record<string, unknown> {
  const schema = media === "text/plain"
    ? part
    : { type: "object", properties: { ok: { type: "string" }, choice: part } };
  return {
    openapi: edition,
    info: { title: "t", version: "1" },
    servers: [{ url: "https://api.example.test" }],
    paths: {
      "/up": {
        post: {
          requestBody: { required: true, content: { [media]: { schema } } },
          responses: { "204": { description: "ok" } },
        },
      },
    },
  };
}

async function invoke(
  edition: string,
  media: string,
  part: unknown,
  input: unknown,
): Promise<{ dispatched: boolean; body: string; contentType: string; failed: boolean; message: string }> {
  let dispatched = false;
  let body = "";
  let contentType = "";
  const fetchFn = vi.fn<typeof fetch>(async (url, init) => {
    dispatched = true;
    const request = new Request(url, init);
    contentType = request.headers.get("content-type") ?? "";
    body = await request.text();
    return new Response(null, { status: 204 });
  });
  let failed = false;
  let message = "";
  try {
    const prepared = await new OpenAPIEngine().prepare({
      source: { content: documentFor(edition, media, part) },
      ref: "#/paths/~1up/post",
      profile: OPENAPI_PROFILE_FULL,
      fetch: fetchFn,
    });
    const execution = await prepared.start();
    await execution.send(input);
    await execution.finishInput();
    for await (const _event of execution.events) { /* drain */ }
    await execution.completed;
  } catch (error: unknown) {
    failed = true;
    message = error instanceof Error ? error.message : String(error);
  }
  return { dispatched, body, contentType, failed, message };
}

const TYPED = { oneOf: [{ type: "string" }, { type: "integer" }] };

describe("an ambiguous choice member (Section 5.2)", () => {
  for (const edition of ["3.0.4", "3.1.2", "3.2.0"]) {
    for (const media of ["multipart/form-data", "application/x-www-form-urlencoded", "text/plain"]) {
      it(`refuses a supplied value before dispatch on ${edition} ${media}, by the route every two-candidate choice takes`, async () => {
        // The standalone engine's caller value is flat: form members ride at
        // the top level and a non-object body under `body`.
        const input = media === "text/plain" ? { body: "eA==" } : { ok: "fine", choice: "eA==" };
        const result = await invoke(edition, media, AMBIGUOUS, input);
        expect(result.failed).toBe(true);
        expect(result.dispatched).toBe(false);
        // The route is Section 5.2's, not a typeless-part rule: the engine
        // says exactly what it says of `oneOf: [{type: string}, {type:
        // integer}]`, the two-candidate choice the shared union-type table
        // already pins as refused.
        const typed = await invoke(edition, media, TYPED, input);
        expect(typed.failed).toBe(true);
        expect(typed.dispatched).toBe(false);
        expect(result.message).toBe(typed.message);
      });
    }
  }
});
