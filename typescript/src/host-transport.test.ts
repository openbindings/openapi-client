import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import {
  fetchCarriesMethod,
  hostCarriesMethod,
  hostMethodRefusal,
  hostTransport,
} from "./host-transport.js";
import { OpenAPIRuntime } from "./runtime.js";
import { OPENAPI_PROFILE_FULL, OpenAPIEngine, OpenAPIExecutionError } from "./engine.js";
import { OpenAPIClient, OpenAPIClientError } from "./client.js";
import { ERR_REFUSED, single, type InvocationError } from "./internal/index.js";

// A real HTTP peer: the methods under test are exactly the ones a fetch
// double cannot exercise, because the platform refuses or rewrites them
// before any transport is called. The peer records the request line and the
// raw header list as they arrived on the socket.

interface Seen {
  method: string;
  url: string;
  headers: string[];
  body: string;
}

let server: Server;
let base = "";
const seen: Seen[] = [];

beforeAll(async () => {
  server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (chunk: Buffer) => chunks.push(chunk));
    req.on("end", () => {
      seen.push({ method: req.method!, url: req.url!, headers: req.rawHeaders, body: Buffer.concat(chunks).toString() });
      if (req.url === "/redirect") {
        res.writeHead(303, { location: "/landing" });
        res.end();
        return;
      }
      if (req.url === "/landing") {
        res.writeHead(200, { "content-type": "application/json" });
        res.end('{"landed":true}');
        return;
      }
      res.writeHead(204, { "x-peer": "1" });
      res.end();
    });
  });
  // A 2xx to CONNECT is a tunnel, not a response (RFC 9110 §9.3.6); Node
  // surfaces it on the server as a `connect` event with the raw socket.
  server.on("connect", (req, socket) => {
    seen.push({ method: req.method!, url: req.url!, headers: req.rawHeaders, body: "" });
    socket.end("HTTP/1.1 204 No Content\r\nx-tunnel: 1\r\n\r\n");
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())));
afterEach(() => {
  seen.length = 0;
});

function headerNames(entry: Seen): string[] {
  return entry.headers.filter((_, index) => index % 2 === 0).map((name) => name.toLowerCase());
}

function traceDocument(version: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    openapi: version,
    info: { title: "fetch-forbidden methods", version: "1" },
    servers: [{ url: base }],
    paths: {
      "/t": {
        trace: {
          requestBody: { content: { "application/json": { schema: { type: "object" } } } },
          responses: { "204": { description: "ok" } },
        },
        ...extra,
      },
    },
  };
}

async function settle(call: { close(): Promise<void>; outputs: AsyncIterable<unknown> }): Promise<InvocationError | null> {
  await call.close();
  try {
    for await (const _ of call.outputs) {
      // drain
    }
    return null;
  } catch (error: unknown) {
    return error as InvocationError;
  }
}

describe("fetch method carriage (Fetch Standard §2.2.1)", () => {
  it("names the forbidden methods and the normalized spellings fetch cannot send byte-exactly", () => {
    for (const method of ["GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS", "PATCH", "QUERY", "patch", "Query", "COPY", "copy", "F~O"]) {
      expect(fetchCarriesMethod(method), method).toBe(true);
    }
    for (const method of ["TRACE", "trace", "Trace", "CONNECT", "connect", "TRACK", "post", "Get", "put", "delete", "head", "options"]) {
      expect(fetchCarriesMethod(method), method).toBe(false);
    }
  });

  it("the host HTTP client carries exactly the uppercase tokens", () => {
    for (const method of ["TRACE", "CONNECT", "TRACK", "GET", "QUERY", "COPY"]) expect(hostCarriesMethod(method), method).toBe(true);
    for (const method of ["post", "Trace", "Query", "copy"]) expect(hostCarriesMethod(method), method).toBe(false);
  });

  it("states the platform limit, never a transport failure", () => {
    expect(hostMethodRefusal("TRACE", false)).toBe(
      'method "TRACE" cannot be sent byte-exactly from this host: the WHATWG fetch API forbids it and no host HTTP client is available in this runtime',
    );
    expect(hostMethodRefusal("post", true)).toBe(
      'method "post" cannot be sent byte-exactly from this host: the WHATWG fetch API would send it as "POST" and the host HTTP client uppercases method tokens',
    );
  });
});

describe("host transport (node:http)", () => {
  it("sends TRACE with the planned headers and nothing else the engine did not plan", async () => {
    const transport = await hostTransport();
    expect(transport).not.toBeNull();
    const headers = new Headers({ "x-planned": "1", accept: "application/json" });
    const response = await transport!(`${base}/t?q=1`, { method: "TRACE", headers, redirect: "manual" });
    expect(response.status).toBe(204);
    expect(response.headers.get("x-peer")).toBe("1");
    expect(response.body).toBeNull();
    expect(seen).toHaveLength(1);
    expect(seen[0]!.method).toBe("TRACE");
    expect(seen[0]!.url).toBe("/t?q=1");
    expect(seen[0]!.body).toBe("");
    expect(headerNames(seen[0]!).sort()).toEqual(["accept", "connection", "host", "x-planned"]);
  });

  it("observes the status and header section of a tunnel answer to CONNECT", async () => {
    const transport = (await hostTransport())!;
    const response = await transport(`${base}/t`, { method: "CONNECT", headers: new Headers(), redirect: "manual" });
    expect(response.status).toBe(204);
    expect(response.headers.get("x-tunnel")).toBe("1");
    expect(response.body).toBeNull();
    expect(seen.map((entry) => entry.method)).toEqual(["CONNECT"]);
  });

  // The transport is generic over uppercase tokens; PURGE is used below
  // because Node's HTTP *server* parser does not know TRACK and answers it
  // with 400 before any handler runs, which would test the peer, not the
  // transport.
  it("carries a planned body byte-exactly with its planned Content-Type and a Content-Length", async () => {
    const transport = (await hostTransport())!;
    const headers = new Headers({ "content-type": "application/json" });
    await transport(`${base}/t`, { method: "PURGE", headers, body: '{"a":1}', redirect: "manual" });
    expect(seen[0]!.method).toBe("PURGE");
    expect(seen[0]!.body).toBe('{"a":1}');
    expect(headerNames(seen[0]!).sort()).toEqual(["connection", "content-length", "content-type", "host"]);
    expect(seen[0]!.headers[seen[0]!.headers.indexOf("content-length") + 1]).toBe("7");
  });

  it("derives fetch's implied Content-Type only when none was planned", async () => {
    const transport = (await hostTransport())!;
    await transport(`${base}/t`, { method: "PURGE", headers: new Headers(), body: "plain", redirect: "manual" });
    expect(seen[0]!.headers[seen[0]!.headers.indexOf("content-type") + 1]).toBe("text/plain;charset=UTF-8");
  });

  it("returns a redirect under `manual`, rejects it under `error`, and follows it as fetch would", async () => {
    const transport = (await hostTransport())!;
    const manual = await transport(`${base}/redirect`, { method: "PURGE", headers: new Headers(), body: "x", redirect: "manual" });
    expect(manual.status).toBe(303);
    await manual.body?.cancel();

    await expect(transport(`${base}/redirect`, { method: "PURGE", headers: new Headers(), redirect: "error" })).rejects.toThrow(TypeError);

    seen.length = 0;
    const followed = await transport(`${base}/redirect`, {
      method: "PURGE",
      headers: new Headers({ "content-type": "text/plain" }),
      body: "x",
      redirect: "follow",
    });
    expect(followed.status).toBe(200);
    await expect(followed.json()).resolves.toEqual({ landed: true });
    // 303: continue as GET, without the body and its describing headers.
    expect(seen.map((entry) => `${entry.method} ${entry.url}`)).toEqual(["PURGE /redirect", "GET /landing"]);
    expect(seen[1]!.body).toBe("");
    expect(headerNames(seen[1]!)).not.toContain("content-type");
  });

  it("rejects an aborted request before dispatch", async () => {
    const transport = (await hostTransport())!;
    const controller = new AbortController();
    controller.abort();
    await expect(transport(`${base}/t`, { method: "TRACE", headers: new Headers(), signal: controller.signal })).rejects.toMatchObject({ name: "AbortError" });
    expect(seen).toHaveLength(0);
  });
});

describe("OpenAPIRuntime default transport", () => {
  for (const version of ["3.0.3", "3.1.0"]) {
    it(`dispatches a body-free trace target on ${version} as TRACE with no Content-Type`, async () => {
      const call = new OpenAPIRuntime().invoke({ source: { content: traceDocument(version) }, ref: "#/paths/~1t/trace" });
      expect(await settle(call)).toBeNull();
      expect(seen).toHaveLength(1);
      expect(seen[0]!.method).toBe("TRACE");
      expect(seen[0]!.url).toBe("/t");
      expect(seen[0]!.body).toBe("");
      expect(headerNames(seen[0]!)).not.toContain("content-type");
    });
  }

  it("refuses before dispatch, naming the platform limit, where the host has no HTTP client", async () => {
    const call = new OpenAPIRuntime().invoke({
      source: { content: traceDocument("3.1.0") },
      ref: "#/paths/~1t/trace",
      hostTransport: null,
    });
    const error = await settle(call);
    expect(error?.code).toBe(ERR_REFUSED);
    expect(error?.message).toBe(hostMethodRefusal("TRACE", false));
    expect(seen).toHaveLength(0);
  });

  it("hands an injected fetch the planned method untouched", async () => {
    const calls: string[] = [];
    const call = new OpenAPIRuntime().invoke({
      source: { content: traceDocument("3.0.3") },
      ref: "#/paths/~1t/trace",
      fetch: async (_input, init) => {
        calls.push(init?.method ?? "");
        return new Response(null, { status: 204 });
      },
    });
    expect(await settle(call)).toBeNull();
    expect(calls).toEqual(["TRACE"]);
    expect(seen).toHaveLength(0);
  });

  it("refuses a secured trace target before dispatch because a security handler needs a WHATWG Request", async () => {
    const document = traceDocument("3.1.0");
    (document.paths as Record<string, Record<string, Record<string, unknown>>>)["/t"]!.trace!.security = [{ digest: [] }];
    document.components = { securitySchemes: { digest: { type: "http", scheme: "digest" } } };
    const prepared = await new OpenAPIEngine().prepare({
      source: { content: document },
      ref: "#/paths/~1t/trace",
      securityHandlers: { digest: () => undefined },
    });
    expect(prepared.prerequisites).toBeNull();
    const execution = await prepared.start();
    await execution.finishInput();
    await expect(execution.completed).rejects.toMatchObject({ code: ERR_REFUSED });
    await expect(execution.completed).rejects.toThrow("security handler");
    expect(seen).toHaveLength(0);
  });
});

describe("OpenAPIEngine default transport on OpenAPI 3.2", () => {
  const additional = (method: string): Record<string, unknown> => ({
    additionalOperations: { [method]: { responses: { "204": { description: "ok" } } } },
  });

  async function invoke(ref: string, document: Record<string, unknown>): Promise<OpenAPIExecutionError | null> {
    const prepared = await new OpenAPIEngine().prepare({ source: { content: document }, ref, profile: OPENAPI_PROFILE_FULL });
    const execution = await prepared.start();
    await execution.finishInput();
    try {
      await execution.completed;
      return null;
    } catch (error: unknown) {
      return error as OpenAPIExecutionError;
    }
  }

  it("dispatches a body-free trace target as TRACE with no Content-Type", async () => {
    expect(await invoke("#/paths/~1t/trace", traceDocument("3.2.0"))).toBeNull();
    expect(seen[0]!.method).toBe("TRACE");
    expect(headerNames(seen[0]!)).not.toContain("content-type");
    expect(seen[0]!.body).toBe("");
  });

  it("dispatches an additional CONNECT operation through the host HTTP client", async () => {
    expect(await invoke("#/paths/~1t/additionalOperations/CONNECT", traceDocument("3.2.0", additional("CONNECT")))).toBeNull();
    expect(seen.map((entry) => `${entry.method} ${entry.url}`)).toEqual(["CONNECT /t"]);
  });

  it("refuses an additional `post` operation before dispatch: no host transport sends that token byte-exactly", async () => {
    const error = await invoke("#/paths/~1t/additionalOperations/post", traceDocument("3.2.0", additional("post")));
    expect(error?.code).toBe(ERR_REFUSED);
    expect(error?.message).toBe(hostMethodRefusal("post", true));
    expect(seen).toHaveLength(0);
  });
});

describe("OpenAPIClient", () => {
  it("refuses a trace call as a configuration limit of its Request-shaped observation surface", async () => {
    const client = await OpenAPIClient.load(traceDocument("3.1.0"));
    await expect(client.call({ path: "/t", method: "trace" })).rejects.toMatchObject({
      kind: "configuration",
      code: "METHOD_UNSUPPORTED_BY_FETCH",
    });
    expect(seen).toHaveLength(0);
  });

  it("wraps that refusal as an OpenAPIClientError", async () => {
    const client = await OpenAPIClient.load(traceDocument("3.1.0"));
    const error = await client.call({ path: "/t", method: "trace" }).catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(OpenAPIClientError);
  });
});

// `single` is imported so an accidental multi-output completion surfaces
// loudly if a future change makes TRACE emit a value.
void single;
