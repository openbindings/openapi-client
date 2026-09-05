import { describe, expect, it, vi } from "vitest";
import { prepareSwagger20 } from "./swagger20-engine.js";

describe("native Swagger 2.0 server and security execution", () => {
  it("selects an authored scheme and one complete non-colliding security alternative", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
    const prepared = await prepareSwagger20({
      source: { content: {
        swagger: "2.0",
        schemes: ["http", "https"], host: "api.example", basePath: "/v1",
        securityDefinitions: {
          bad: { type: "apiKey", in: "query", name: "id" },
          good: { type: "apiKey", in: "header", name: "X-Good" },
        },
        security: [{ bad: [] }, { good: [] }],
        paths: { "/x": { get: {
          parameters: [{ name: "id", in: "query", type: "string" }],
          responses: { 204: { description: "ok" } },
        } } },
      } },
      ref: "#/paths/~1x/get",
      serverSchemeIndex: 1,
      securityAlternative: 1,
      securityCredentials: { apiKeys: { bad: "bad", good: "good" } },
      fetch: fetchMock,
    });
    await prepared.execute({ parameters: { query: { id: "42" } } });
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe("https://api.example/v1/x?id=42");
    expect(new Headers(init?.headers).get("X-Good")).toBe("good");
  });

  it("inherits retrieval authority and port, and omits an absent basePath", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
    const prepared = await prepareSwagger20({
      source: {
        location: "https://docs.example:8443/swagger.json",
        content: { swagger: "2.0", paths: { "/x": { get: { responses: { 204: { description: "ok" } } } } } },
      },
      ref: "#/paths/~1x/get",
      fetch: fetchMock,
    });
    await prepared.execute();
    expect(fetchMock.mock.calls[0]?.[0]).toBe("https://docs.example:8443/x");
  });
  it("joins an authored root basePath and operation path at one slash boundary", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
    const prepared = await prepareSwagger20({
      source: {
        content: {
          swagger: "2.0",
          schemes: ["https"],
          host: "api.example",
          basePath: "/",
          paths: { "/x": { get: { responses: { 204: { description: "ok" } } } } },
        },
      },
      ref: "#/paths/~1x/get",
      fetch: fetchMock,
    });
    await prepared.execute();
    expect(fetchMock.mock.calls[0]?.[0]).toBe("https://api.example/x");
  });
});
