import { describe, expect, it, vi } from "vitest";
import { OpenAPIClient, OpenAPIClientError } from "./client.js";
import type { OpenAPIDocument } from "./types.js";

function document(operation: Record<string, unknown>, path = "/widgets/{id}", method = "post"): OpenAPIDocument {
  return {
    openapi: "3.1.0",
    info: { title: "Native client contract", version: "1" },
    servers: [{ url: "https://api.example.test/v1" }],
    paths: {
      [path]: {
        [method]: operation,
      },
    },
  };
}

describe("OpenAPIClient native API", () => {
  it("loads a document and selects operations by id, path/method, and ref", async () => {
    const doc = document({
      operationId: "createWidget",
      summary: "Create one",
      tags: ["widgets"],
      responses: { "204": { description: "created" } },
    });
    const client = await OpenAPIClient.load(doc, {
      fetch: vi.fn<typeof fetch>(async () => new Response(null, { status: 204 })),
    });

    expect(client.operations()).toEqual([{
      ref: "#/paths/~1widgets~1{id}/post",
      path: "/widgets/{id}",
      method: "post",
      operationId: "createWidget",
      summary: "Create one",
      tags: ["widgets"],
    }]);
    expect(client.operation("createWidget").info.method).toBe("post");
    expect(client.operation({ path: "/widgets/{id}", method: "post" }).info.operationId).toBe("createWidget");
    expect(client.operation({ ref: "#/paths/~1widgets~1{id}/post" }).info.path).toBe("/widgets/{id}");
  });

  it("preserves same-named values in path, query, and body", async () => {
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input as Request;
      expect(request.url).toBe("https://api.example.test/v1/widgets/path-id?id=query-id");
      expect(await request.json()).toEqual({ id: "body-id", enabled: true });
      return new Response('{"created":true}', {
        status: 201,
        headers: { "content-type": "application/json", "x-request-id": "req-1" },
      });
    });
    const client = await OpenAPIClient.load(document({
      operationId: "createWidget",
      parameters: [
        { name: "id", in: "path", required: true, schema: { type: "string" } },
        { name: "id", in: "query", schema: { type: "string" } },
      ],
      requestBody: {
        required: true,
        content: {
          "application/json": {
            schema: {
              type: "object",
              properties: { id: { type: "string" }, enabled: { type: "boolean" } },
            },
          },
        },
      },
      responses: {
        "201": {
          description: "created",
          content: { "application/json": { schema: { type: "object" } } },
        },
      },
    }), { fetch: fetchFn });

    const result = await client.call<{ created: boolean }>("createWidget", {
      parameters: { path: { id: "path-id" }, query: { id: "query-id" } },
      body: { id: "body-id", enabled: true },
    });

    expect(result.ok).toBe(true);
    if (!result.ok) throw new Error("expected success");
    expect(result.data).toEqual({ created: true });
    expect(result.response.status).toBe(201);
    expect(result.response.headers.get("x-request-id")).toBe("req-1");
    expect(result.openapi).toEqual({ declared: true, responseKey: "201", mediaType: "application/json" });
  });

  it("places credentials by their authored security-scheme names", async () => {
    const doc = document({
      operationId: "secured",
      security: [{ TenantKey: [], Session: [] }],
      parameters: [{ name: "id", in: "path", required: true, schema: { type: "string" } }],
      responses: { "200": { description: "ok", content: { "application/json": {} } } },
    }, "/secured/{id}", "get");
    doc.components = {
      securitySchemes: {
        TenantKey: { type: "apiKey", in: "header", name: "X-Tenant-Key" },
        Session: { type: "http", scheme: "bearer" },
      },
    };
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input as Request;
      expect(request.headers.get("x-tenant-key")).toBe("tenant-secret");
      expect(request.headers.get("authorization")).toBe("Bearer session-token");
      return Response.json({ ok: true });
    });
    const client = await OpenAPIClient.load(doc, { fetch: fetchFn });

    await expect(client.call("secured", {
      parameters: { path: { id: "42" } },
    }, {
      auth: { TenantKey: "tenant-secret", Session: "session-token" },
    })).resolves.toMatchObject({ ok: true, data: { ok: true } });
  });

  it("returns declared HTTP failures as native values with response evidence", async () => {
    const client = await OpenAPIClient.load(document({
      operationId: "failing",
      parameters: [{ name: "id", in: "path", required: true, schema: { type: "string" } }],
      responses: {
        "422": {
          description: "invalid",
          content: { "application/problem+json": { schema: { type: "object" } } },
        },
      },
    }, "/fail/{id}", "get"), {
      fetch: vi.fn<typeof fetch>(async () => new Response('{"code":"invalid"}', {
        status: 422,
        headers: { "content-type": "application/problem+json", "retry-after": "3" },
      })),
    });

    const result = await client.call("failing", { parameters: { path: { id: "bad" } } });
    expect(result).toMatchObject({
      ok: false,
      error: { code: "invalid" },
      openapi: { declared: true, responseKey: "422", mediaType: "application/problem+json" },
    });
    expect(result.response.status).toBe(422);
    expect(result.response.headers.get("retry-after")).toBe("3");
  });

  it("preserves an explicit falsy whole body", async () => {
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input as Request;
      expect(await request.text()).toBe("false");
      return Response.json({ stored: true });
    });
    const client = await OpenAPIClient.load(document({
      operationId: "storeFlag",
      parameters: [{ name: "id", in: "path", required: true, schema: { type: "string" } }],
      requestBody: {
        content: { "application/json": { schema: { type: "boolean" } } },
      },
      responses: { "200": { description: "ok", content: { "application/json": {} } } },
    }, "/flags/{id}", "put"), { fetch: fetchFn });

    await expect(client.call("storeFlag", {
      parameters: { path: { id: "one" } },
      body: false,
    })).resolves.toMatchObject({ ok: true });
  });

  it("distinguishes an explicit empty optional object body from no body", async () => {
    const observed: string[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input as Request;
      observed.push(await request.text());
      return new Response(null, { status: 204 });
    });
    const client = await OpenAPIClient.load(document({
      operationId: "optionalBody",
      requestBody: {
        required: false,
        content: { "application/json": { schema: { type: "object", properties: {} } } },
      },
      responses: { "204": { description: "ok" } },
    }, "/optional", "post"), { fetch: fetchFn });

    await client.call("optionalBody", { body: {} });
    await client.call("optionalBody");
    expect(observed).toEqual(["{}", ""]);
  });

  it("runs middleware around the protocol-native request and response", async () => {
    const events: string[] = [];
    const client = await OpenAPIClient.load(document({
      operationId: "middleware",
      responses: { "200": { description: "ok", content: { "application/json": {} } } },
    }, "/middleware", "get"), {
      fetch: vi.fn<typeof fetch>(async (input) => {
        expect((input as Request).headers.get("x-client")).toBe("yes");
        events.push("fetch");
        return Response.json({ ok: true });
      }),
      middleware: [{
        onRequest({ request }) {
          events.push("request");
          request.headers.set("x-client", "yes");
        },
        onResponse({ response }) {
          events.push("response");
          const headers = new Headers(response.headers);
          headers.set("x-middleware", "yes");
          return new Response(response.body, { status: response.status, headers });
        },
      }],
    });

    const result = await client.call("middleware");
    expect(events).toEqual(["request", "fetch", "response"]);
    expect(result.response.headers.get("x-middleware")).toBe("yes");
  });

  it("exposes SSE as a backpressured native async stream", async () => {
    const client = await OpenAPIClient.load(document({
      operationId: "events",
      responses: {
        "200": {
          description: "events",
          content: { "text/event-stream": { schema: { type: "string" } } },
        },
      },
    }, "/events", "get"), {
      fetch: vi.fn<typeof fetch>(async () => new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode(
              "event: update\nid: cursor-7\nretry: 250\ndata: first\n\ndata: second\n\n",
            ));
            controller.close();
          },
        }),
        { status: 200, headers: { "content-type": "text/event-stream" } },
      )),
    });

    const stream = await client.stream<string>("events");
    expect(stream.ok).toBe(true);
    if (!stream.ok) throw new Error("expected a stream");
    const events: unknown[] = [];
    for await (const event of stream.events) events.push(event);
    await expect(stream.closed).resolves.toBeUndefined();
    expect(events).toEqual([
      { data: "first", sse: { event: "update", id: "cursor-7", retry: 250 } },
      { data: "second", sse: { id: "cursor-7" } },
    ]);
    await expect(client.call("events")).rejects.toMatchObject({ code: "STREAMING_RESPONSE" });
  });

  it("propagates native stream cancellation to terminal lifecycle", async () => {
    const client = await OpenAPIClient.load(document({
      operationId: "endlessEvents",
      responses: {
        "200": {
          description: "events",
          content: { "text/event-stream": { schema: { type: "string" } } },
        },
      },
    }, "/endless-events", "get"), {
      fetch: vi.fn<typeof fetch>(async () => new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode("data: ready\n\n"));
          },
        }),
        { headers: { "content-type": "text/event-stream" } },
      )),
    });

    const stream = await client.stream<string>("endlessEvents");
    if (!stream.ok) throw new Error("expected a stream");
    const iterator = stream.events[Symbol.asyncIterator]();
    await expect(iterator.next()).resolves.toEqual({ done: false, value: { data: "ready" } });
    await stream.cancel();
    await expect(stream.closed).rejects.toMatchObject({ kind: "cancelled", code: "ERR_CANCELLED" });
  });

  it("throws typed client errors for local selection and configuration failures", async () => {
    const client = await OpenAPIClient.load(document({
      operationId: "known",
      responses: { "204": { description: "ok" } },
    }, "/known", "get"));

    expect(() => client.operation("missing")).toThrowError(OpenAPIClientError);
    await expect(client.call("known", {}, { auth: { missing: "secret" } })).rejects.toMatchObject({
      kind: "configuration",
      code: "UNKNOWN_SECURITY_SCHEME",
    });
  });

  it("refuses unsupported security schemes instead of misapplying credentials", async () => {
    const doc = document({
      operationId: "digest",
      security: [{ digest: [] }],
      responses: { "204": { description: "ok" } },
    }, "/digest", "get");
    doc.components = { securitySchemes: { digest: { type: "http", scheme: "digest" } } };
    const client = await OpenAPIClient.load(doc);

    await expect(client.call("digest", {}, { auth: { digest: "not-a-bearer-token" } })).rejects.toMatchObject({
      kind: "configuration",
      code: "UNSUPPORTED_SECURITY_SCHEME",
    });
  });

  it("allows an explicit native handler to own an otherwise unsupported security scheme", async () => {
    const doc = document({
      operationId: "digestHandled",
      security: [{ digest: [] }],
      responses: { "204": { description: "ok" } },
    }, "/digest-handled", "get");
    doc.components = { securitySchemes: { digest: { type: "http", scheme: "digest" } } };
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      expect((input as Request).headers.get("authorization")).toBe("Digest native-proof");
      return new Response(null, { status: 204 });
    });
    const client = await OpenAPIClient.load(doc, { fetch: fetchFn });

    await expect(client.call("digestHandled", {}, {
      auth: {
        digest({ request, schemeName, scheme }) {
          expect(schemeName).toBe("digest");
          expect(scheme.scheme).toBe("digest");
          request.headers.set("authorization", "Digest native-proof");
        },
      },
    })).resolves.toMatchObject({ ok: true });
  });

  it("does not combine native security handlers from separate OR alternatives", async () => {
    const doc = document({
      operationId: "securityOr",
      security: [{ first: [] }, { second: [] }],
      responses: { "204": { description: "ok" } },
    }, "/security-or", "get");
    doc.components = {
      securitySchemes: {
        first: { type: "http", scheme: "first-auth" },
        second: { type: "http", scheme: "second-auth" },
      },
    };
    const calls: string[] = [];
    const client = await OpenAPIClient.load(doc, {
      fetch: vi.fn<typeof fetch>(async (input) => {
        expect((input as Request).headers.get("authorization")).toBe("First proof");
        return new Response(null, { status: 204 });
      }),
    });

    await client.call("securityOr", {}, {
      auth: {
        first({ request }) {
          calls.push("first");
          request.headers.set("authorization", "First proof");
        },
        second({ request }) {
          calls.push("second");
          request.headers.set("authorization", "Second proof");
        },
      },
    });
    expect(calls).toEqual(["first"]);
  });
});
