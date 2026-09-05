/**
 * Byte-preserving transport support for methods the host Fetch implementation
 * refuses or rewrites. The client still owns the completed URL, method,
 * headers, body, redirects, and response interpretation.
 */

import type * as NodeHttp from "node:http";
import type * as NodeHttps from "node:https";
import type * as NodeStream from "node:stream";

/** Redirect handling admitted by the deterministic invocation contract. */
export type OpenAPIRedirectPolicy = "manual" | "follow";

/** The planned request handed to a host transport. */
export interface OpenAPIHostRequest {
  /** The wire method token, exactly as the artifact denotes it. */
  method: string;
  /** Planned header fields; the transport sends them as given. */
  headers: Headers;
  /** Planned body, or absent. */
  body?: BodyInit | null;
  signal?: AbortSignal | null;
  /** Fetch redirect mode; the engine defaults it to `manual`. */
  redirect?: RequestRedirect;
}

/** Complete request plan exposed to engine hooks before transport dispatch. */
export interface OpenAPIPlannedRequest extends OpenAPIHostRequest {
  /** Completed HTTP(S) target, including the serialized path and query. */
  url: string;
}

/**
 * Sends one planned request through a host-owned HTTP client and returns a
 * standard `Response`. Used only for methods the platform `fetch` cannot
 * carry; `null` declares that the host has no such client.
 */
export type OpenAPIHostTransport = (
  url: string,
  request: OpenAPIHostRequest,
) => Promise<Response>;

const FETCH_FORBIDDEN_METHODS = new Set(["CONNECT", "TRACE", "TRACK"]);
const FETCH_NORMALIZED_METHODS = new Set(["DELETE", "GET", "HEAD", "OPTIONS", "POST", "PUT"]);

/** ASCII byte-uppercase, the Fetch Standard's own case operation on a token. */
function byteUppercase(method: string): string {
  return method.replace(/[a-z]/gu, (c) => c.toUpperCase());
}

/**
 * Whether the platform `fetch` sends this method token byte-exactly: neither
 * a forbidden method (the Request constructor throws) nor a non-uppercase
 * spelling of one of the six methods it normalizes (sent rewritten).
 */
export function fetchCarriesMethod(method: string): boolean {
  const upper = byteUppercase(method);
  if (FETCH_FORBIDDEN_METHODS.has(upper)) return false;
  if (FETCH_NORMALIZED_METHODS.has(upper) && method !== upper) return false;
  return true;
}

/**
 * Whether the host transport sends this method token byte-exactly. Node's
 * `http.request` uppercases every method token before it is written, so only
 * an already-uppercase token survives it unchanged.
 */
export function hostCarriesMethod(method: string): boolean {
  return method === byteUppercase(method);
}

/**
 * The plain, owned refusal text for a method no available transport can send
 * byte-exactly. It names the platform limit, never a transport failure.
 */
export function hostMethodRefusal(method: string, hostAvailable: boolean): string {
  const fetchLimit = FETCH_FORBIDDEN_METHODS.has(byteUppercase(method))
    ? "the WHATWG fetch API forbids it"
    : `the WHATWG fetch API would send it as ${JSON.stringify(byteUppercase(method))}`;
  const hostLimit = hostAvailable
    ? "the host HTTP client uppercases method tokens"
    : "no host HTTP client is available in this runtime";
  return `method ${JSON.stringify(method)} cannot be sent byte-exactly from this host: ${fetchLimit} and ${hostLimit}`;
}

let resolved: Promise<OpenAPIHostTransport | null> | undefined;

/**
 * The host's own HTTP client as a transport, resolved once per process:
 * `node:http`/`node:https` under Node, `null` in a browser, Worker, or any
 * runtime where the import fails.
 */
export function hostTransport(): Promise<OpenAPIHostTransport | null> {
  resolved ??= loadNodeTransport();
  return resolved;
}

interface NodeModules {
  http: typeof NodeHttp;
  https: typeof NodeHttps;
  stream: typeof NodeStream;
}

async function loadNodeTransport(): Promise<OpenAPIHostTransport | null> {
  const versions = (globalThis as { process?: { versions?: { node?: unknown } } }).process?.versions;
  if (typeof versions?.node !== "string") return null;
  try {
    const [http, https, stream] = await Promise.all([
      importHostModule("node:http"),
      importHostModule("node:https"),
      importHostModule("node:stream"),
    ]);
    return nodeTransport({ http, https, stream } as NodeModules);
  } catch {
    return null;
  }
}

/**
 * The specifier is deliberately not a literal so browser-targeting bundlers
 * neither resolve nor polyfill Node builtins; the guard above keeps the
 * import from running anywhere but Node.
 */
function importHostModule(specifier: string): Promise<unknown> {
  return import(/* @vite-ignore */ /* webpackIgnore: true */ specifier);
}

/** Fetch's redirect statuses (Fetch Standard §2.2.6 "redirect status"). */
const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);
const MAX_REDIRECTS = 20;
const NULL_BODY_STATUSES = new Set([101, 204, 205, 304]);

function nodeTransport(modules: NodeModules): OpenAPIHostTransport {
  return async (url, request) => {
    if (!hostCarriesMethod(request.method)) {
      throw new TypeError(hostMethodRefusal(request.method, true));
    }
    const mode = request.redirect ?? "manual";
    let current = new URL(url);
    let method = request.method;
    const headers = new Headers(request.headers);
    let body = await materializeBody(request.body, headers);

    for (let redirects = 0; ; redirects += 1) {
      const response = await dispatchOnce(modules, current, method, headers, body, request.signal ?? undefined);
      const location = response.headers.get("location");
      if (!REDIRECT_STATUSES.has(response.status) || location === null || mode === "manual") {
        return response;
      }
      if (mode === "error") {
        await response.body?.cancel();
        throw new TypeError(`redirect mode is "error" and the response is a redirect`);
      }
      // Fetch Standard §4.4 "HTTP-redirect fetch": at most twenty redirects;
      // the Location is parsed against the response's URL; a 301/302 answered
      // POST and a 303 answered non-GET/HEAD continue as GET without a body
      // and without the body-describing headers; `Authorization` is dropped
      // across origins.
      if (redirects >= MAX_REDIRECTS) {
        await response.body?.cancel();
        throw new TypeError("redirect count exceeded twenty");
      }
      let next: URL;
      try {
        next = new URL(location, current);
      } catch (error: unknown) {
        await response.body?.cancel();
        throw new TypeError(`redirect Location ${JSON.stringify(location)} does not parse`, { cause: error });
      }
      if (next.protocol !== "http:" && next.protocol !== "https:") {
        await response.body?.cancel();
        throw new TypeError(`redirect Location scheme ${JSON.stringify(next.protocol)} is not HTTP(S)`);
      }
      await response.body?.cancel();
      const status = response.status;
      if (((status === 301 || status === 302) && method === "POST") || (status === 303 && method !== "GET" && method !== "HEAD")) {
        method = "GET";
        body = null;
        for (const name of ["content-encoding", "content-language", "content-location", "content-type", "content-length"]) {
          headers.delete(name);
        }
      }
      if (next.origin !== current.origin) headers.delete("authorization");
      current = next;
    }
  };
}

/**
 * Fetch's "extract a body": the body bytes, and the Content-Type the body
 * source implies when the planned headers carry none (a multipart `FormData`
 * boundary, a `Blob` type, `text/plain;charset=UTF-8` for a string).
 */
async function materializeBody(body: BodyInit | null | undefined, headers: Headers): Promise<Uint8Array | null> {
  if (body === null || body === undefined) return null;
  const extracted = new Response(body);
  const implied = extracted.headers.get("content-type");
  if (implied !== null && !headers.has("content-type")) headers.set("content-type", implied);
  return new Uint8Array(await extracted.arrayBuffer());
}

function dispatchOnce(
  modules: NodeModules,
  target: URL,
  method: string,
  headers: Headers,
  body: Uint8Array | null,
  signal: AbortSignal | undefined,
): Promise<Response> {
  return new Promise<Response>((resolve, reject) => {
    if (signal?.aborted) {
      reject(abortReason(signal));
      return;
    }
    const client = target.protocol === "https:" ? modules.https : modules.http;
    const requestHeaders: Record<string, string> = {};
    headers.forEach((value, name) => {
      requestHeaders[name] = value;
    });
    if (body !== null) requestHeaders["content-length"] = String(body.byteLength);

    let settled = false;
    const finish = (settle: () => void): void => {
      if (settled) return;
      settled = true;
      signal?.removeEventListener("abort", onAbort);
      settle();
    };
    const req = client.request(target, { method, headers: requestHeaders });
    const onAbort = (): void => {
      const reason = abortReason(signal!);
      req.destroy(reason instanceof Error ? reason : undefined);
      finish(() => reject(reason));
    };
    signal?.addEventListener("abort", onAbort, { once: true });

    req.on("response", (res: NodeHttp.IncomingMessage) => {
      finish(() => {
        try {
          resolve(toResponse(modules, res, method));
        } catch (error: unknown) {
          res.destroy();
          reject(error);
        }
      });
    });
    // A 2xx answer to CONNECT arrives as a tunnel rather than a response
    // (RFC 9110 §9.3.6); the engine observes its status and header section
    // and the tunnel is not kept.
    req.on("connect", (res: NodeHttp.IncomingMessage, socket: { destroy(): void }) => {
      finish(() => {
        try {
          resolve(toResponse(modules, res, method, true));
        } catch (error: unknown) {
          reject(error);
        } finally {
          socket.destroy();
        }
      });
    });
    req.on("upgrade", (res: NodeHttp.IncomingMessage, socket: { destroy(): void }) => {
      socket.destroy();
      finish(() => reject(new TypeError(`host transport cannot represent a ${res.statusCode} upgrade response`)));
    });
    req.on("error", (error: Error) => finish(() => reject(error)));
    if (body !== null) req.end(body);
    else req.end();
  });
}

function toResponse(
  modules: NodeModules,
  res: NodeHttp.IncomingMessage,
  method: string,
  headerSectionOnly = false,
): Response {
  const status = res.statusCode ?? 0;
  const headers = new Headers();
  const raw = res.rawHeaders;
  for (let index = 0; index + 1 < raw.length; index += 2) {
    headers.append(raw[index]!, raw[index + 1]!);
  }
  const bodyless = headerSectionOnly || method === "HEAD" || NULL_BODY_STATUSES.has(status);
  if (bodyless) res.resume();
  const body = bodyless ? null : (modules.stream.Readable.toWeb(res) as ReadableStream<Uint8Array>);
  return new Response(body, { status, statusText: res.statusMessage ?? "", headers });
}

function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException("This operation was aborted", "AbortError");
}
