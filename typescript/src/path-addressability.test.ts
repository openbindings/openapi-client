import { describe, expect, it, vi } from "vitest";
import { OpenAPIClient } from "./client.js";
import { pathTemplateAddressabilityConflict } from "./invoke.js";
import type { OpenAPIDocument } from "./types.js";

// §9.3 (OAPI-P-05): the target URL is the resolved server joined with the
// operation's path template, so a template variable no declared path parameter
// can supply leaves no target to address. The artifact is invalid under every
// accepted OAS edition — a path template variable MUST have a corresponding
// `in: path` parameter — and the refusal must precede dispatch rather than
// putting a percent-encoded `%7Bname%7D` segment on a live service.
//
// The corpus original is spree/spree's
// `/api/v2/storefront/policies/{policy_slug}` `show-policy`, which declares no
// `parameters` at all. Twinned with openapi-client/go's
// path_addressability_test.go.

function document(paths: Record<string, unknown>, components?: Record<string, unknown>): OpenAPIDocument {
  return {
    openapi: "3.1.0",
    info: { title: "addressability", version: "1" },
    servers: [{ url: "https://api.example.test" }],
    ...(components ? { components } : {}),
    paths,
  } as OpenAPIDocument;
}

describe("path-template addressability", () => {
  it("refuses an undeclared path template variable before dispatch", async () => {
    const fetchFn = vi.fn<typeof fetch>(async () =>
      new Response(`{"ok":true}`, { status: 200, headers: { "Content-Type": "application/json" } }));
    const client = await OpenAPIClient.load(
      document({
        "/api/v2/storefront/policies/{policy_slug}": {
          get: {
            operationId: "show-policy",
            responses: { "200": { description: "ok", content: { "application/json": {} } } },
          },
        },
      }),
      { fetch: fetchFn },
    );
    await expect(client.call("show-policy", {})).rejects.toThrow(
      "path template variable(s) policy_slug have no declared path parameter",
    );
    // Supplying the variable as an ordinary field does not manufacture the
    // missing declaration either: the artifact, not the input, owns the defect.
    // The native client screens an undeclared parameter name at its own input
    // surface first, so this spelling refuses one layer above the binding.
    await expect(client.call("show-policy", { parameters: { path: { policy_slug: "returns" } } }))
      .rejects.toThrow(/policy_slug/);
    expect(fetchFn).not.toHaveBeenCalled();
  });

  it("names every unaddressable variable, in code-point order", async () => {
    const client = await OpenAPIClient.load(
      document({
        "/{tenant}/reports/{report_id}/{section}": {
          get: {
            operationId: "showSection",
            parameters: [{ name: "report_id", in: "path", required: true, schema: { type: "string" } }],
            responses: { "200": { description: "ok", content: { "application/json": {} } } },
          },
        },
      }),
      { fetch: vi.fn<typeof fetch>(async () => new Response(null, { status: 204 })) },
    );
    await expect(client.call("showSection", { parameters: { path: { report_id: "r1" } } }))
      .rejects.toThrow("path template variable(s) section, tenant have no declared path parameter");
  });

  // The refusal reaches exactly the unaddressable case. Every declaration that
  // CAN supply its variable still dispatches, and a brace that delimits no
  // expression is an ordinary path literal.
  it.each([
    {
      name: "operation-level declaration",
      paths: {
        "/items/{id}": {
          get: {
            operationId: "op",
            parameters: [{ name: "id", in: "path", required: true, schema: { type: "string" } }],
            responses: { "200": { description: "ok", content: { "application/json": {} } } },
          },
        },
      },
      input: { parameters: { path: { id: "42" } } },
      want: "/items/42",
    },
    {
      name: "path-item-level declaration",
      paths: {
        "/items/{id}": {
          parameters: [{ name: "id", in: "path", required: true, schema: { type: "string" } }],
          get: {
            operationId: "op",
            responses: { "200": { description: "ok", content: { "application/json": {} } } },
          },
        },
      },
      input: { parameters: { path: { id: "42" } } },
      want: "/items/42",
    },
    {
      name: "referenced declaration",
      paths: {
        "/items/{id}": {
          get: {
            operationId: "op",
            parameters: [{ $ref: "#/components/parameters/ItemID" }],
            responses: { "200": { description: "ok", content: { "application/json": {} } } },
          },
        },
      },
      input: { parameters: { path: { id: "42" } } },
      want: "/items/42",
    },
    {
      name: "unpaired opening brace is a literal",
      paths: {
        "/items/a{b": {
          get: {
            operationId: "op",
            responses: { "200": { description: "ok", content: { "application/json": {} } } },
          },
        },
      },
      input: {},
      want: "/items/a%7Bb",
    },
    {
      name: "unpaired closing brace is a literal",
      paths: {
        "/items/a}b": {
          get: {
            operationId: "op",
            responses: { "200": { description: "ok", content: { "application/json": {} } } },
          },
        },
      },
      input: {},
      want: "/items/a%7Db",
    },
  ])("keeps $name dispatchable", async ({ paths, input, want }) => {
    let observed = "";
    const client = await OpenAPIClient.load(
      document(paths, {
        parameters: { ItemID: { name: "id", in: "path", required: true, schema: { type: "string" } } },
      }),
      {
        fetch: vi.fn<typeof fetch>(async (request) => {
          observed = new URL(request instanceof Request ? request.url : String(request)).pathname;
          return new Response(`{"ok":true}`, { status: 200, headers: { "Content-Type": "application/json" } });
        }),
      },
    );
    const result = await client.call("op", input);
    expect(result.ok).toBe(true);
    expect(observed).toBe(want);
  });

  // The neighbouring §9.1 case is untouched: a DECLARED path parameter the
  // caller omitted keeps its own refusal, which states the missing INPUT
  // rather than an artifact defect. Absent input and a supplied-but-
  // incomplete input are ONE rule, so both spellings refuse while routing
  // with the same message — the URL cannot be built either way.
  it.each([
    { name: "required", required: true, want: "missing path parameter(s) id" },
    { name: "optional", required: false, want: "is upstream-invalid" },
  ])("refuses an omitted $name declared path parameter at the correct owner", async ({ required, want }) => {
    const client = await OpenAPIClient.load(
      document({
        "/items/{id}": {
          get: {
            operationId: "op",
            parameters: [{ name: "id", in: "path", required, schema: { type: "string" } }],
            responses: { "200": { description: "ok", content: { "application/json": {} } } },
          },
        },
      }),
      { fetch: vi.fn<typeof fetch>(async () => new Response(null, { status: 204 })) },
    );
    await expect(client.call("op", {})).rejects.toThrow(want);
  });

  it("reads brace-delimited expressions and nothing else", () => {
    const declared = [{ name: "id", in: "path" as const }];
    expect(pathTemplateAddressabilityConflict("/items", [])).toBe("");
    expect(pathTemplateAddressabilityConflict("/items/{id}", declared)).toBe("");
    expect(pathTemplateAddressabilityConflict("/items/a{b", [])).toBe("");
    expect(pathTemplateAddressabilityConflict("/items/a}b", [])).toBe("");
    expect(pathTemplateAddressabilityConflict("/items/{{id}", declared)).toBe("");
    expect(pathTemplateAddressabilityConflict("/items/{id}.{format}", declared))
      .toContain("variable(s) format ");
    expect(pathTemplateAddressabilityConflict("/{b}/{a}/{b}", []))
      .toContain("variable(s) a, b ");
  });
});
