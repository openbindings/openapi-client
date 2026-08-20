// The §9.2 normalized-collision CONFINEMENT case table, shared
// byte-identically with the Go engine and both adapter engines
// (testdata/media-collision-confinement-cases.json) and exercised here
// through the shipped engine invocation path.
//
// Two keys in ONE content map that denote the same parsed media type are a
// normalized collision, and the defect confines to that colliding parsed
// identity -- the smallest unit that owns it. No first-key rule: no request
// selection may land on the colliding identity and no response match may be
// governed by it, while the map's non-colliding entries remain usable.
//
// Request cells drive the requestMedia configuration point and observe the
// dispatch; response cells drive the peer's Content-Type and observe both the
// decoded output and the Accept set the request advertised.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { OpenAPIRuntime } from "./runtime.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";

export interface MediaCollisionCase {
  name: string;
  openapi: string;
  side: "request" | "response";
  description: string;
  content: Record<string, unknown>;
  select: string;
  responseBody?: string;
  outcome: "usable" | "refused";
  output?: unknown;
  advertised?: string[];
  target?: "represented" | "excluded";
  targetReasonCode?: string;
  targetRule?: string;
  represented?: string[];
  excluded?: string[];
  collidingIdentity?: string;
}

const REQUEST_VALUE = { name: "A" };

export const mediaCollisionTable = JSON.parse(readFileSync(
  new URL("../testdata/media-collision-confinement-cases.json", import.meta.url),
  "utf8",
)) as { cases: MediaCollisionCase[] };

export function mediaCollisionDocument(fixture: MediaCollisionCase): Record<string, unknown> {
  const operation = fixture.side === "request"
    ? {
      operationId: "createItem",
      requestBody: { required: true, content: fixture.content },
      responses: { "204": { description: "stored" } },
    }
    : {
      operationId: "readItem",
      responses: { "200": { description: "item", content: fixture.content } },
    };
  return {
    openapi: fixture.openapi,
    info: { title: "media collision confinement", version: "1" },
    servers: [{ url: "https://fixture.invalid" }],
    paths: { "/items": { [fixture.side === "request" ? "post" : "get"]: operation } },
  };
}

interface Observed {
  requests: { contentType: string | null; accept: string | null; body: string | undefined }[];
  outputs: unknown[];
  failure: unknown;
}

async function run(
  fixture: MediaCollisionCase,
  context: Record<string, unknown> | undefined,
  input: unknown,
  sendInput: boolean,
): Promise<Observed> {
  const requests: Observed["requests"] = [];
  const fetchFn = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    requests.push({
      contentType: headers.get("Content-Type"),
      accept: headers.get("Accept"),
      body: typeof init?.body === "string" ? init.body : undefined,
    });
    if (fixture.side === "request") return new Response(null, { status: 204 });
    return new Response(fixture.responseBody ?? "", {
      status: 200,
      headers: { "Content-Type": fixture.select },
    });
  }) as typeof fetch;

  const call = new OpenAPIRuntime().invokeBinding({
    source: { profile: OPENAPI_PROFILE_FULL, content: mediaCollisionDocument(fixture) },
    ref: `#/paths/~1items/${fixture.side === "request" ? "post" : "get"}`,
    context,
    fetch: fetchFn,
  });
  if (sendInput) await call.write(input);
  await call.close();

  const outputs: unknown[] = [];
  let failure: unknown;
  try {
    for await (const value of call.outputs) outputs.push(value);
    await call.closed;
  } catch (error: unknown) {
    failure = error ?? new Error("unknown failure");
  }
  return { requests, outputs, failure };
}

describe("language-neutral §9.2 normalized-collision confinement", () => {
  expect(mediaCollisionTable.cases.length).toBeGreaterThan(0);
  for (const fixture of mediaCollisionTable.cases) {
    it(fixture.name, async () => {
      expect(["usable", "refused"]).toContain(fixture.outcome);
      if (fixture.side === "request") {
        const observed = await run(
          fixture,
          { configuration: { requestMedia: fixture.select } },
          REQUEST_VALUE,
          true,
        );
        if (fixture.outcome === "usable") {
          expect(observed.failure, "expected a completed dispatch").toBeUndefined();
          expect(observed.requests).toHaveLength(1);
          expect(observed.requests[0]!.contentType).toBe(fixture.select);
          expect(JSON.parse(observed.requests[0]!.body ?? "")).toEqual(REQUEST_VALUE);
          return;
        }
        expect(observed.failure, "expected a pre-dispatch refusal").toBeDefined();
        expect(observed.requests, "a refusal must precede dispatch").toHaveLength(0);
        return;
      }

      const observed = await run(fixture, undefined, undefined, false);
      expect(observed.requests).toHaveLength(1);
      // The Accept set is the advertised success-media membership: a
      // colliding parsed identity can govern no match, so it is never
      // advertised.
      const advertised = fixture.advertised ?? [];
      expect(observed.requests[0]!.accept).toBe(
        advertised.length === 0 ? null : advertised.join(", "),
      );
      if (fixture.outcome === "usable") {
        expect(observed.failure, "expected a decoded response").toBeUndefined();
        expect(observed.outputs).toEqual([fixture.output]);
        return;
      }
      expect(observed.failure, "expected a loud response-media refusal").toBeDefined();
      expect(observed.outputs).toEqual([]);
    });
  }
});
