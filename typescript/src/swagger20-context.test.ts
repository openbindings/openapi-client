// Twin of the Go engine's swagger20_context_test.go. openbindings.openapi-2.0@1
// §3.2 gives a pre-dispatch refusal two species: context-required, where a named
// §12.1 configuration point or a declared credential is awaited and the refusal
// carries its own resolution path, and plain refusal, where no supplied context
// could change the answer. This file pins which condition carries which species,
// and pins that the species never moves the boundary between refusing and
// dispatching.
import { describe, expect, it, vi } from "vitest";
import { prepareSwagger20, Swagger20ExecutionError } from "./swagger20-engine.js";

const HOST = {
  swagger: "2.0",
  info: { title: "species", version: "1" },
  host: "api.example",
  schemes: ["https"],
};
const LOCATION = "https://api.example/swagger.json";
// The target is the concrete destination the invoker is about to use: the
// resolved §10 server base once it resolves, and only where the server itself
// is awaited the source location the caller supplied — the same two scopes the
// 3.x lane asserts.
const SERVER_BASE = "https://api.example";

interface Requirement { type: string; name?: string; point?: string; path?: string; schema?: unknown; durable?: boolean }

async function run(
  content: Record<string, unknown>,
  ref: string,
  input: unknown,
  options: Record<string, unknown> = {},
): Promise<{ error?: Swagger20ExecutionError; dispatches: number }> {
  const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
  try {
    const prepared = await prepareSwagger20({
      source: { location: LOCATION, content },
      ref,
      fetch: fetchMock,
      ...options,
    });
    await prepared.execute((input ?? {}) as never);
  } catch (error: unknown) {
    return { error: error as Swagger20ExecutionError, dispatches: fetchMock.mock.calls.length };
  }
  return { dispatches: fetchMock.mock.calls.length };
}

function requirements(error: Swagger20ExecutionError | undefined, target = SERVER_BASE): Requirement[] {
  const details = error?.details as { target?: string; alternatives?: { requirements: Requirement[] }[] };
  expect(details?.target).toBe(target);
  expect(details?.alternatives).toHaveLength(1);
  return details!.alternatives![0]!.requirements;
}

const EMPTY_VALUE = { ...HOST, paths: { "/p": { get: {
  parameters: [{ name: "q", in: "query", type: "string", allowEmptyValue: true }],
  responses: { 204: { description: "ok" } } } } } };

describe("Swagger 2.0 refusal species", () => {
  it("names emptyValueForm where the two admitted empty spellings differ", async () => {
    const { error, dispatches } = await run(EMPTY_VALUE, "#/paths/~1p/get", { parameters: { query: { q: "" } } });
    expect(error?.code).toBe("CONTEXT_REQUIRED");
    expect(dispatches).toBe(0);
    expect(requirements(error)).toEqual([{
      type: "config.value", point: "emptyValueForm", path: "",
      description: "supply the Swagger 2.0 emptyValueForm configuration value",
      schema: { enum: ["name-only", "empty"] }, durable: true,
    }]);
  });

  it("dispatches the same invocation once the choice is supplied", async () => {
    const { error, dispatches } = await run(EMPTY_VALUE, "#/paths/~1p/get",
      { parameters: { query: { q: "" } } }, { emptyValueForm: "name-only" });
    expect(error).toBeUndefined();
    expect(dispatches).toBe(1);
  });

  it("names requestMedia before input consumption", async () => {
    const { error, dispatches } = await run({
      ...HOST, consumes: ["application/json", "text/plain"],
      paths: { "/p": { post: {
        parameters: [{ name: "b", in: "body", required: true, schema: { type: "string" } }],
        responses: { 204: { description: "ok" } } } } },
    }, "#/paths/~1p/post", { body: "x" });
    expect(error?.code).toBe("CONTEXT_REQUIRED");
    expect(dispatches).toBe(0);
    expect(requirements(error)[0]!.point).toBe("requestMedia");
  });

  it("names propertyMedia keyed by the file parameter", async () => {
    const { error } = await run({
      ...HOST, consumes: ["multipart/form-data"],
      paths: { "/p": { post: {
        parameters: [{ name: "f", in: "formData", required: true, type: "file" }],
        responses: { 204: { description: "ok" } } } } },
    }, "#/paths/~1p/post", { parameters: { formData: { f: "QUFB" } } });
    expect(error?.code).toBe("CONTEXT_REQUIRED");
    expect(requirements(error)[0]).toMatchObject({ point: "propertyMedia", path: "/f" });
  });

  it("names server on two usable schemes", async () => {
    const { error } = await run({ ...HOST, schemes: ["http", "https"],
      paths: { "/p": { get: { responses: { 204: { description: "ok" } } } } } }, "#/paths/~1p/get", {});
    expect(error?.code).toBe("CONTEXT_REQUIRED");
    expect(requirements(error, LOCATION)[0]!.point).toBe("server");
  });

  it("names server where §10 states a configured URL is the recovery", async () => {
    const { error } = await run({ ...HOST, schemes: ["ws", "wss"],
      paths: { "/p": { get: { responses: { 204: { description: "ok" } } } } } }, "#/paths/~1p/get", {});
    expect(error?.code).toBe("CONTEXT_REQUIRED");
    expect(requirements(error, LOCATION)[0]!.point).toBe("server");
  });

  it("names security when two alternatives are declared", async () => {
    const { error } = await run({ ...HOST,
      securityDefinitions: { k: { type: "apiKey", name: "X-Key", in: "header" }, b: { type: "basic" } },
      security: [{ k: [] }, { b: [] }],
      paths: { "/p": { get: { responses: { 204: { description: "ok" } } } } } }, "#/paths/~1p/get", {});
    expect(error?.code).toBe("CONTEXT_REQUIRED");
    expect(requirements(error)[0]!.point).toBe("security");
  });

  it("names the declared credential as an auth requirement, not a config point", async () => {
    const { error } = await run({ ...HOST,
      securityDefinitions: { k: { type: "apiKey", name: "X-Key", in: "header" } },
      security: [{ k: [] }],
      paths: { "/p": { get: { responses: { 204: { description: "ok" } } } } } }, "#/paths/~1p/get", {});
    expect(error?.code).toBe("CONTEXT_REQUIRED");
    // A declared credential amortizes across invocations, so every credential
    // requirement carries the contract's persistence permission, as the 3.x
    // lane's security requirements do.
    expect(requirements(error)).toEqual([{ type: "auth.apiKey", name: "k", durable: true }]);
  });

  it("carries the whole ANDed alternative, not the first missing member", async () => {
    const { error } = await run({ ...HOST,
      securityDefinitions: { k: { type: "apiKey", name: "X-Key", in: "header" }, b: { type: "basic" } },
      security: [{ k: [], b: [] }],
      paths: { "/p": { get: { responses: { 204: { description: "ok" } } } } } }, "#/paths/~1p/get", {});
    expect(error?.code).toBe("CONTEXT_REQUIRED");
    expect(requirements(error)).toEqual([
      { type: "auth.basic", name: "b", durable: true },
      { type: "auth.apiKey", name: "k", durable: true },
    ]);
  });

  it("keeps an empty string the declaration never admits a plain refusal", async () => {
    const { error, dispatches } = await run({ ...HOST, paths: { "/p": { get: {
      parameters: [{ name: "q", in: "query", type: "string" }],
      responses: { 204: { description: "ok" } } } } } }, "#/paths/~1p/get", { parameters: { query: { q: "" } } });
    expect(error?.code).toBe("ERR_REFUSED");
    expect(error?.details).toBeUndefined();
    expect(dispatches).toBe(0);
  });

  it("keeps a supplied value outside the point's admissible set a plain refusal", async () => {
    const { error } = await run(EMPTY_VALUE, "#/paths/~1p/get",
      { parameters: { query: { q: "" } } }, { emptyValueForm: "sometimes" });
    expect(error?.code).toBe("ERR_REFUSED");
    expect(error?.details).toBeUndefined();
  });

  it("keeps a supplied OAuth2 token this lane cannot use a plain refusal", async () => {
    const { error } = await run({ ...HOST,
      securityDefinitions: { o: { type: "oauth2", flow: "implicit", authorizationUrl: "https://auth.example/a", scopes: {} } },
      security: [{ o: [] }],
      paths: { "/p": { get: { responses: { 204: { description: "ok" } } } } } },
      "#/paths/~1p/get", {}, { securityCredentials: { oauth2: { o: { accessToken: "not a token", scopes: [] } } } });
    expect(error?.code).toBe("ERR_REFUSED");
    expect(error?.details).toBeUndefined();
  });
});
