import { createServer } from "node:http";
import { describe, expect, it, vi } from "vitest";
import { OpenAPIClient, OpenAPIClientError } from "./client.js";
import type { OpenAPIDocument } from "./types.js";

function document(
  operation: Record<string, unknown>,
  path = "/widgets/{id}",
  method = "post",
  openapi = "3.1.0",
): OpenAPIDocument {
  return {
    openapi,
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
      wireMethod: "POST",
      additional: false,
      operationId: "createWidget",
      summary: "Create one",
      tags: ["widgets"],
    }]);
    expect(client.operation("createWidget").info.method).toBe("post");
    expect(client.operation({ path: "/widgets/{id}", method: "post" }).info.operationId).toBe("createWidget");
    expect(client.operation({ ref: "#/paths/~1widgets~1{id}/post" }).info.path).toBe("/widgets/{id}");
  });

  it("loads and invokes Swagger 2.0 through the same client surface", async () => {
    const fetchFn = vi.fn<typeof fetch>(async (input, init) => {
      const request = new Request(input, init);
      expect(request.method).toBe("GET");
      expect(request.url).toBe("https://api.example.test/v1/pets/a%2Fb");
      return new Response('{"name":"Ada"}', {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    });
    const client = await OpenAPIClient.load({
      swagger: "2.0",
      info: { title: "Swagger client", version: "1" },
      schemes: ["https"],
      host: "api.example.test",
      basePath: "/v1",
      produces: ["application/json"],
      paths: {
        "/pets/{id}": {
          get: {
            operationId: "getPet",
            parameters: [{ name: "id", in: "path", required: true, type: "string" }],
            responses: { "200": { description: "pet", schema: { type: "object" } } },
          },
        },
      },
    } as never, { fetch: fetchFn });

    expect(client.edition).toBe("2.0");
    expect(client.operations()).toEqual([{
      ref: "#/paths/~1pets~1{id}/get",
      path: "/pets/{id}",
      method: "get",
      wireMethod: "GET",
      additional: false,
      operationId: "getPet",
      tags: [],
    }]);
    await expect(client.call("getPet", { parameters: { path: { id: "a/b" } } })).resolves.toMatchObject({
      ok: true,
      data: { name: "Ada" },
      openapi: { declared: true, responseKey: "200" },
    });
  });

  it("inventories and invokes OAS 3.2 QUERY and additional operations", async () => {
    const methods: string[] = [];
    const client = await OpenAPIClient.load({
      openapi: "3.2.0",
      info: { title: "3.2 operations", version: "1" },
      servers: [{ url: "https://api.example.test" }],
      paths: {
        "/cache": {
          query: { operationId: "findCached", responses: { "204": { description: "done" } } },
          additionalOperations: {
            PURGE: { operationId: "purgeCache", responses: { "204": { description: "done" } } },
          },
        },
      },
    }, {
      fetch: vi.fn<typeof fetch>(async (input, init) => {
        methods.push(input instanceof Request ? input.method : init?.method ?? "GET");
        return new Response(null, { status: 204 });
      }),
    });

    expect(client.edition).toBe("3.2.0");
    expect(client.operations().map(({ method, wireMethod, additional }) => ({ method, wireMethod, additional }))).toEqual([
      { method: "PURGE", wireMethod: "PURGE", additional: true },
      { method: "query", wireMethod: "QUERY", additional: false },
    ]);
    await client.call("findCached");
    await client.call({ path: "/cache", method: "PURGE", additional: true });
    expect(methods).toEqual(["QUERY", "PURGE"]);
  });

  it("snapshots caller-owned parsed artifacts at load", async () => {
    const source = document({ operationId: "stable", responses: { "204": { description: "done" } } });
    const client = await OpenAPIClient.load(source);
    source.paths = {};
    expect(client.operations().map(({ operationId }) => operationId)).toEqual(["stable"]);
  });

  it("snapshots client defaults and bound operation metadata", async () => {
    const artifact = document({
      operationId: "stableOptions",
      tags: ["original"],
      security: [{ session: [] }],
      responses: { "204": { description: "done" } },
    }, "/stable", "get");
    artifact.components = {
      securitySchemes: { session: { type: "apiKey", in: "header", name: "X-Session" } },
    };
    const headers: Record<string, string> = { "X-Default": "first" };
    const auth: Record<string, string> = { session: "first-secret" };
    const fetchFn = vi.fn<typeof globalThis.fetch>(async (input, init) => {
      const request = new Request(input, init);
      expect(request.headers.get("x-default")).toBe("first");
      expect(request.headers.get("x-session")).toBe("first-secret");
      return new Response(null, { status: 204 });
    });
    const client = await OpenAPIClient.load(artifact, { headers, auth, fetch: fetchFn });
    headers["X-Default"] = "second";
    auth.session = "second-secret";
    const operation = client.operation("stableOptions");
    (operation.info.tags as string[]).push("mutated");

    expect(client.operations()[0]?.tags).toEqual(["original"]);
    await expect(operation.call()).resolves.toMatchObject({ ok: true });
  });

  it("supports concurrent calls through one immutable loaded client", async () => {
    const fetchFn = vi.fn<typeof globalThis.fetch>(async () =>
      new Response(null, { status: 204 }));
    const client = await OpenAPIClient.load(document({
      operationId: "shared",
      parameters: [{ name: "request", in: "query", schema: { type: "string" } }],
      responses: { "204": { description: "done" } },
    }, "/shared", "get"), { fetch: fetchFn });

    const results = await Promise.all(Array.from({ length: 32 }, (_, request) =>
      client.call("shared", { parameters: { query: { request: String(request) } } })));

    expect(results.every(({ ok }) => ok)).toBe(true);
    expect(fetchFn).toHaveBeenCalledTimes(32);
  });

  it("propagates cancellation through document loading", async () => {
    const controller = new AbortController();
    const fetchFn = vi.fn<typeof globalThis.fetch>(async (_input, init) => {
      return await new Promise<Response>((_resolve, reject) => {
        const signal = init?.signal;
        if (signal?.aborted) {
          reject(signal.reason);
          return;
        }
        signal?.addEventListener("abort", () => reject(signal.reason), { once: true });
      });
    });
    const loading = OpenAPIClient.load("https://example.test/openapi.yaml", {
      documentFetch: fetchFn,
      documentSignal: controller.signal,
    });
    controller.abort(new DOMException("release qualification cancellation", "AbortError"));
    await expect(loading).rejects.toMatchObject({
      name: "OpenAPIClientError",
      kind: "source",
      code: "SOURCE_LOAD_FAILED",
    });
    expect(fetchFn).toHaveBeenCalledOnce();
  });

  it("keeps document retrieval separate from invocation transport", async () => {
    const entry = JSON.stringify(document({
      operationId: "separateTransport",
      responses: { "204": { description: "done" } },
    }, "/ping", "get"));
    const documentFetch = vi.fn<typeof globalThis.fetch>(async () => new Response(entry, {
      status: 200,
      headers: { "content-type": "application/json" },
    }));
    const invocationFetch = vi.fn<typeof globalThis.fetch>(async () => new Response(null, { status: 204 }));
    const client = await OpenAPIClient.load("https://documents.example.test/openapi.json", {
      documentFetch,
      fetch: invocationFetch,
    });

    await expect(client.call("separateTransport")).resolves.toMatchObject({ ok: true });
    expect(documentFetch).toHaveBeenCalledOnce();
    expect(invocationFetch).toHaveBeenCalledOnce();
  });

  it("keeps redirects observable and follows only method-preserving hops when requested", async () => {
    const observed = new Map<string, { method: string; body: string }>();
    const server = createServer(async (request, response) => {
      const chunks: Uint8Array[] = [];
      for await (const chunk of request) chunks.push(typeof chunk === "string" ? Buffer.from(chunk) : chunk);
      observed.set(request.url ?? "", {
        method: request.method ?? "",
        body: Buffer.concat(chunks).toString("utf8"),
      });
      if (request.url === "/rewrite") {
        response.writeHead(303, { location: "/rewrite-final" });
        response.end();
        return;
      }
      if (request.url === "/preserve") {
        response.writeHead(307, { location: "/preserve-final" });
        response.end();
        return;
      }
      response.writeHead(204);
      response.end();
    });
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", resolve);
    });
    const address = server.address();
    if (address === null || typeof address === "string") throw new Error("expected an internet server address");
    const baseURL = `http://127.0.0.1:${address.port}`;
    const redirectDocument: OpenAPIDocument = {
      openapi: "3.1.2",
      info: { title: "Redirect fidelity", version: "1" },
      servers: [{ url: baseURL }],
      paths: {
        "/rewrite": { post: jsonRedirectOperation("rewrite") },
        "/preserve": { post: jsonRedirectOperation("preserve") },
      },
    };
    try {
      const client = await OpenAPIClient.load(redirectDocument);
      await expect(client.call("rewrite", { body: { value: "rewrite" } })).resolves.toMatchObject({
        ok: false,
        response: { status: 303 },
      });
      await expect(client.call("preserve", { body: { value: "preserve" } })).resolves.toMatchObject({
        ok: false,
        response: { status: 307 },
      });
      expect(observed.has("/rewrite-final")).toBe(false);
      expect(observed.has("/preserve-final")).toBe(false);

      await expect(client.call("rewrite", { body: { value: "rewrite" } }, { redirect: "follow" }))
        .resolves.toMatchObject({ ok: false, response: { status: 303 } });
      await expect(client.call("preserve", { body: { value: "preserve" } }, { redirect: "follow" }))
        .resolves.toMatchObject({ ok: true });
    } finally {
      await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
    }
    expect(observed.has("/rewrite-final")).toBe(false);
    expect(observed.get("/preserve-final")).toEqual({ method: "POST", body: '{"value":"preserve"}' });
  });

  it.each([
    ["Swagger 2.0", {
      swagger: "2.0",
      info: { title: "Redirect credentials", version: "1" },
      schemes: ["https"],
      host: "first.example.test",
      securityDefinitions: {
        headerKey: { type: "apiKey", in: "header", name: "X-Secret" },
        queryKey: { type: "apiKey", in: "query", name: "querySecret" },
        basic: { type: "basic" },
      },
      paths: { "/start": { get: {
        operationId: "redirectCredentials",
        security: [{ headerKey: [], queryKey: [], basic: [] }],
        responses: { "204": { description: "done" } },
      } } },
    }, { headerKey: "header-secret", queryKey: "query-secret", basic: { username: "me", password: "secret" } }],
    ["OpenAPI 3.1", {
      openapi: "3.1.2",
      info: { title: "Redirect credentials", version: "1" },
      servers: [{ url: "https://first.example.test" }],
      components: { securitySchemes: {
        headerKey: { type: "apiKey", in: "header", name: "X-Secret" },
        queryKey: { type: "apiKey", in: "query", name: "querySecret" },
        cookieKey: { type: "apiKey", in: "cookie", name: "session" },
        bearer: { type: "http", scheme: "bearer" },
      } },
      paths: { "/start": { get: {
        operationId: "redirectCredentials",
        security: [{ headerKey: [], queryKey: [], cookieKey: [], bearer: [] }],
        responses: { "204": { description: "done" } },
      } } },
    }, { headerKey: "header-secret", queryKey: "query-secret", cookieKey: "cookie-secret", bearer: "bearer-secret" }],
  ] as const)("strips selected credentials on a cross-origin preserving redirect for %s", async (_name, artifact, auth) => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof globalThis.fetch>(async (input, init) => {
      const request = new Request(input, init);
      requests.push(request);
      return requests.length === 1
        ? new Response(null, { status: 307, headers: { location: "https://second.example.test/final" } })
        : new Response(null, { status: 204 });
    });
    const client = await OpenAPIClient.load(artifact, {
      fetch: fetchFn,
      auth,
      headers: { "X-Trace": "ordinary" },
      redirect: "follow",
    });

    await expect(client.call("redirectCredentials")).resolves.toMatchObject({ ok: true });
    expect(requests).toHaveLength(2);
    expect(requests[0]!.url).toContain("querySecret=query-secret");
    expect(requests[0]!.headers.get("x-secret")).toBe("header-secret");
    expect(requests[0]!.headers.get("authorization")).toBeTruthy();
    expect(requests[1]!.url).toBe("https://second.example.test/final");
    expect(requests[1]!.headers.get("x-secret")).toBeNull();
    expect(requests[1]!.headers.get("authorization")).toBeNull();
    expect(requests[1]!.headers.get("cookie")).toBeNull();
    expect(requests[1]!.headers.get("x-trace")).toBe("ordinary");
  });

  it("preserves same-named values in path, query, and body", async () => {
    const fetchFn = vi.fn<typeof fetch>(async (input, init) => {
      const request = new Request(input, init);
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
    expect(await result.response.json()).toEqual({ created: true });
    expect(result.openapi).toEqual({ declared: true, responseKey: "201", mediaType: "application/json" });
  });

  it("accepts native bytes for raw request media without exposing private Base64 carriage", async () => {
    const bytes = Uint8Array.of(0, 1, 254, 255);
    const fetchFn = vi.fn<typeof fetch>(async (input, init) => {
      const request = new Request(input, init);
      expect(request.headers.get("content-type")).toBe("image/png");
      expect(new Uint8Array(await request.arrayBuffer())).toEqual(bytes);
      return new Response(null, { status: 204 });
    });
    const client = await OpenAPIClient.load(document({
      operationId: "uploadImage",
      requestBody: { required: true, content: { "image/png": {} } },
      responses: { "204": { description: "stored" } },
    }, "/image", "put"), { fetch: fetchFn });

    await expect(client.call("uploadImage", {
      body: bytes,
      mediaType: "image/png",
    })).resolves.toMatchObject({ ok: true });
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
    const fetchFn = vi.fn<typeof fetch>(async (input, init) => {
      const request = new Request(input, init);
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
    expect(await result.response.json()).toEqual({ code: "invalid" });
  });

  it("preserves an explicit falsy whole body", async () => {
    const fetchFn = vi.fn<typeof fetch>(async (input, init) => {
      const request = new Request(input, init);
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
    const fetchFn = vi.fn<typeof fetch>(async (input, init) => {
      const request = new Request(input, init);
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
      fetch: vi.fn<typeof fetch>(async (input, init) => {
        expect(new Request(input, init).headers.get("x-client")).toBe("yes");
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
    }, "/events", "get", "3.2.0"), {
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
    expect(stream.response.bodyUsed).toBe(true);
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
    }, "/endless-events", "get", "3.2.0"), {
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

  it("translates missing execution context into native configuration requirements", async () => {
    const doc = document({
      operationId: "secured",
      security: [{ session: [] }],
      responses: { "204": { description: "ok" } },
    }, "/secured", "get");
    doc.components = {
      securitySchemes: {
        session: { type: "apiKey", in: "header", name: "X-Session" },
      },
    };
    const client = await OpenAPIClient.load(doc);

    await expect(client.call("secured")).rejects.toMatchObject({
      kind: "configuration",
      code: "CONFIGURATION_REQUIRED",
      requirements: {
        alternatives: [[{
          kind: "credential",
          name: "session",
          credential: "apiKey",
        }]],
      },
    });
  });

  it("names missing request-media choices in the native call input", async () => {
    const client = await OpenAPIClient.load(document({
      operationId: "createWithMedia",
      requestBody: {
        required: true,
        content: {
          "application/json": { schema: { type: "object" } },
          "application/problem+json": { schema: { type: "object" } },
        },
      },
      responses: { "204": { description: "done" } },
    }, "/media", "post"));

    await expect(client.call("createWithMedia", { body: { ok: true } })).rejects.toMatchObject({
      kind: "configuration",
      code: "CONFIGURATION_REQUIRED",
      requirements: {
        alternatives: [[{
          kind: "input",
          name: "mediaType",
          path: "",
        }]],
      },
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
    const fetchFn = vi.fn<typeof fetch>(async (input, init) => {
      expect(new Request(input, init).headers.get("authorization")).toBe("Digest native-proof");
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
      fetch: vi.fn<typeof fetch>(async (input, init) => {
        expect(new Request(input, init).headers.get("authorization")).toBe("First proof");
        return new Response(null, { status: 204 });
      }),
    });

    await client.call("securityOr", {}, {
      securityAlternative: 0,
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

function jsonRedirectOperation(operationId: string): Record<string, unknown> {
  return {
    operationId,
    requestBody: {
      required: true,
      content: { "application/json": { schema: { type: "object" } } },
    },
    responses: { "204": { description: "done" } },
  };
}
