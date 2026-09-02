// The §9.1 JSON-body trigger-scoping case table, shared byte-identically
// with the Go engine and both adapter engines
// (testdata/json-body-trigger-scoping-cases.json) and exercised through the
// shipped engine invocation path.
//
// Two rules ride the same cells. The trigger keywords are read under the
// GOVERNING EDITION'S dialect: on the 3.0 line patternProperties, if, then,
// else, dependentSchemas and unevaluatedProperties are not in the Schema
// Object dialect at all and decide as if absent, while oneOf, anyOf, not and
// additionalProperties decide alike on both lines. And an explicit
// unevaluatedProperties triggers on ANY value, the `false` spelling
// included.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { OpenAPIRuntime } from "./runtime.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";

interface JSONBodyTriggerCase {
  name: string;
  openapi: string;
  line: string;
  keyword: string;
  presence: string;
  schema: Record<string, unknown>;
  expect: "whole" | "flattened";
}

const FLAT_LANE_WHOLE_REFUSAL = "whole-value carriage";
const ROUTED_LANE_FLAT_REFUSAL =
  "routed whole-body field does not identify any admissible whole-value request-body candidate";
const BODY_VALUE = { value: "v" };

const table = JSON.parse(readFileSync(
  new URL("./testdata/json-body-trigger-scoping-cases.json", import.meta.url),
  "utf8",
)) as { cases: JSONBodyTriggerCase[] };

function document(fixture: JSONBodyTriggerCase): Record<string, unknown> {
  return {
    openapi: fixture.openapi,
    info: { title: "json body trigger scoping", version: "1" },
    servers: [{ url: "https://fixture.invalid" }],
    paths: {
      "/items": {
        post: {
          operationId: "createItem",
          requestBody: {
            required: true,
            content: { "application/json": { schema: fixture.schema } },
          },
          responses: { "204": { description: "stored" } },
        },
      },
    },
  };
}

async function runLane(
  fixture: JSONBodyTriggerCase,
  input: unknown,
  dispatches: boolean,
  refusal: string,
): Promise<void> {
  const requests: { body: string | undefined }[] = [];
  const fetchFn = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ body: typeof init?.body === "string" ? init.body : undefined });
    return new Response(null, { status: 204 });
  }) as typeof fetch;

  const call = new OpenAPIRuntime().invokeBinding({
    source: { profile: OPENAPI_PROFILE_FULL, content: document(fixture) },
    ref: "#/paths/~1items/post",
    fetch: fetchFn,
  });
  await call.write(input);
  await call.close();

  if (dispatches) {
    for await (const _ of call.outputs) void _;
    await call.closed;
    expect(requests).toHaveLength(1);
    expect(JSON.parse(requests[0]!.body ?? "")).toEqual(BODY_VALUE);
    return;
  }
  const failure = await call.closed.then(() => undefined, (err: unknown) => err);
  expect(failure, `expected a pre-dispatch refusal naming ${refusal}`).toBeDefined();
  expect((failure as { message?: string }).message).toContain(refusal);
  expect(requests).toHaveLength(0);
}

describe("language-neutral §9.1 JSON-body trigger scoping", () => {
  const routedInput = [{
    [OPENAPI_PROFILE_FULL.inputRouteKey]: OPENAPI_PROFILE_FULL.inputRouteMarker,
    value: { payload: { ...BODY_VALUE } },
    parameters: [],
    body: { whole: "payload" },
  }];
  const flatInput = { ...BODY_VALUE };

  expect(table.cases.length).toBeGreaterThan(0);
  for (const fixture of table.cases) {
    it(fixture.name, async () => {
      expect(["whole", "flattened"]).toContain(fixture.expect);
      const whole = fixture.expect === "whole";
      // The flat lane carries the artifact's own named property; it
      // dispatches exactly when the cell stays flattened.
      await runLane(fixture, flatInput, !whole, FLAT_LANE_WHOLE_REFUSAL);
      // The routed lane names body.whole; it dispatches exactly when the
      // cell selects whole carriage.
      await runLane(fixture, routedInput, whole, ROUTED_LANE_FLAT_REFUSAL);
    });
  }
});
