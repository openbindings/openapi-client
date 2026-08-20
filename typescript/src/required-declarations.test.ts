// The §9.1 required-declarations case table, shared byte-identically with
// the Go engine (conformance/cases/required-declarations.json) and exercised
// through the shipped engine invocation path. Exactly two conditions refuse
// before dispatch — a required request body with no value to carry and an
// unsupplied path parameter — each applied to absent and
// supplied-but-incomplete input alike; every other missing declaration is
// sent as supplied, the server's validation authoritative.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { OpenAPIRuntime } from "./runtime.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";

interface RequiredDeclarationsCase {
  name: string;
  document: Record<string, unknown>;
  ref: string;
  input: { present: boolean; value?: Record<string, unknown> };
  expect: {
    dispatched: boolean;
    code?: string;
    messageContains?: string;
    method?: string;
    path?: string;
    query?: string;
    bodyJSON?: unknown;
  };
}

const cases = JSON.parse(readFileSync(
  new URL("../../conformance/cases/required-declarations.json", import.meta.url),
  "utf8",
)) as RequiredDeclarationsCase[];

describe("language-neutral §9.1 required-declarations conformance", () => {
  for (const fixture of cases) {
    it(fixture.name, async () => {
      const requests: { method: string; url: URL; body: string | undefined }[] = [];
      const fetchFn = (async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({
          method: init?.method ?? "GET",
          url: new URL(input instanceof Request ? input.url : String(input)),
          body: typeof init?.body === "string" ? init.body : undefined,
        });
        return new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }) as typeof fetch;

      const call = new OpenAPIRuntime().invokeBinding({
        source: { profile: OPENAPI_PROFILE_FULL, content: structuredClone(fixture.document) },
        ref: fixture.ref,
        fetch: fetchFn,
      });
      if (fixture.input.present) {
        await call.write(fixture.input.value ?? {});
      }
      await call.close();

      if (fixture.expect.dispatched) {
        for await (const _ of call.outputs) void _;
        await call.closed;
        expect(requests).toHaveLength(1);
        const request = requests[0]!;
        if (fixture.expect.method !== undefined) expect(request.method).toBe(fixture.expect.method);
        if (fixture.expect.path !== undefined) expect(request.url.pathname).toBe(fixture.expect.path);
        if (fixture.expect.query !== undefined) expect(request.url.search.slice(1)).toBe(fixture.expect.query);
        if (fixture.expect.bodyJSON !== undefined) {
          expect(JSON.parse(request.body ?? "")).toEqual(fixture.expect.bodyJSON);
        }
        return;
      }
      const failure = await call.closed.then(
        () => undefined,
        (err: unknown) => err,
      );
      expect(failure).toBeDefined();
      const executionErr = failure as { code?: string; message?: string };
      expect(executionErr.code).toBe(fixture.expect.code);
      expect(executionErr.message).toContain(fixture.expect.messageContains);
      expect(requests).toHaveLength(0);
    });
  }
});
