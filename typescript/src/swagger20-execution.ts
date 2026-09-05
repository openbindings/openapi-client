import { swagger20ConfigRequired } from "./swagger20-context.js";
import { ConfigRequired } from "./servers.js";
import { swagger20RefusalError } from "./swagger20-context.js";
import {
  Swagger20ExecutionError,
  type PreparedSwagger20Operation,
  type Swagger20ContentCodec,
  type Swagger20ContentCodingResult,
} from "./swagger20-engine.js";
import {
  routeSwagger20Input,
  swagger20RawQuery,
  type Swagger20Input,
  type Swagger20ParameterSet,
} from "./swagger20-parameters.js";
import {
  contentCodingTokens,
  decodeSwagger20Response,
  effectiveSwagger20MediaSet,
  encodeSwagger20RequestPayload,
  governingSwagger20Response,
  selectSwagger20RequestMedia,
  swagger20AcceptHeader,
  swagger20PayloadFor,
  swagger20ResponsesFor,
  type Swagger20ResolvedResponse,
} from "./swagger20-media.js";
import { booleanMember, objectMember, stringMember, type Swagger20Object } from "./swagger20-model.js";
import { joinSwagger20Target, resolveSwagger20Server } from "./swagger20-server.js";
import {
  applySwagger20Security,
  selectSwagger20Security,
  type Swagger20CredentialPlacement,
} from "./swagger20-security.js";

export interface Swagger20ExecutionResult {
  outputPresent: boolean;
  output?: unknown;
  status: number;
  headers: Headers;
  response: Response;
  declaration: {
    declared: boolean;
    responseKey?: string;
    mediaType?: string;
  };
}

/** @internal - exact Swagger 2.0 unary execution lane. */
export async function executeSwagger20(
  prepared: PreparedSwagger20Operation,
  parameters: Swagger20ParameterSet,
  input: Swagger20Input = {},
): Promise<Swagger20ExecutionResult> {
  let responses: Swagger20Object;
  try {
    responses = await swagger20ResponsesFor(prepared.operation);
  } catch (error: unknown) {
    throw refused(error, prepared);
  }
  let routed;
  let server: string;
  let security;
  try {
    server = resolveSwagger20ServerForInvocation(prepared);
    security = selectSwagger20Security(
      prepared.document,
      prepared.operation,
      parameters,
      prepared.options.securityAlternative,
      prepared.options.securityCredentials,
    );
    // A supplied value the point does not admit is the caller's own choice, so
    // no further context changes the answer: it stays §3.2's plain species. The
    // Go engine's Start() has always checked this; without the same check here,
    // an out-of-set value falls through to the "no choice supplied" branch and
    // would be reported as an awaited one.
    if (prepared.options.emptyValueForm !== undefined
      && prepared.options.emptyValueForm !== "name-only"
      && prepared.options.emptyValueForm !== "empty") {
      throw new Swagger20ExecutionError("ERR_REFUSED", "emptyValueForm must be name-only or empty");
    }
    // openbindings.openapi-2.0@1 §9.1: a payload-emitting invocation over a
    // non-self-electing effective `consumes` set "requires one concrete
    // `requestMedia` choice BEFORE input consumption". A required payload
    // always emits, so the choice is owed before any supplied value is read
    // and before any value-conditional point can be reached. The Go engine's
    // Start() has always done this; doing it here is what makes the twins name
    // the same point on the same document.
    const declaredPayload = await swagger20PayloadFor(parameters, prepared.operation);
    if (swagger20PayloadIsRequired(declaredPayload)) {
      selectSwagger20RequestMedia(
        effectiveSwagger20MediaSet(prepared.document, prepared.operation, "consumes"),
        declaredPayload,
        prepared.options.requestMedia,
      );
    }
    routed = routeSwagger20Input(
      parameters,
      prepared.operation.path,
      input,
      prepared.options.parameterConverter,
      prepared.options.emptyValueForm,
    );
    applySwagger20Security(routed, security);
  } catch (error: unknown) {
    throw refused(error, prepared);
  }
  const payloadPresent = routed.bodyPresent || routed.formPresent;
  if (payloadPresent && ["get", "head", "delete", "options"].includes(prepared.operation.method)) {
    throw new Swagger20ExecutionError(
      "ERR_REFUSED",
      `Swagger 2.0 ${prepared.operation.method} operations exclude the payload lane`,
    );
  }
  if (!payloadPresent && routed.headers.some((header) => header.name.toLowerCase() === "content-encoding")) {
    throw new Swagger20ExecutionError(
      "ERR_REFUSED",
      "request Content-Encoding cannot be supplied when the invocation emits no request representation",
    );
  }
  let requestBody: Uint8Array | undefined;
  let contentType: string | undefined;
  if (payloadPresent) {
    try {
      const model = await swagger20PayloadFor(parameters, prepared.operation);
      const consumes = effectiveSwagger20MediaSet(prepared.document, prepared.operation, "consumes");
      const selection = selectSwagger20RequestMedia(consumes, model, prepared.options.requestMedia);
      const encoded = encodeSwagger20RequestPayload(
        selection,
        model,
        routed,
        prepared.options.propertyMedia,
        prepared.options.requestCharacterEncodings,
      );
      requestBody = encoded.body;
      contentType = encoded.contentType;
    } catch (error: unknown) {
      throw refused(error, prepared);
    }
  }
  const query = swagger20RawQuery(routed.query);
  const url = `${joinSwagger20Target(server, routed.resolvedPath)}${query === "" ? "" : `?${query}`}`;
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") throw new Error("target scheme is not HTTP");
    decodeURIComponent(parsed.pathname + parsed.search);
  } catch (error: unknown) {
    throw refused(error, prepared);
  }
  const headers = new Headers();
  try {
    const accept = await swagger20AcceptHeader(prepared.document, prepared.operation, parameters, responses);
    if (accept !== "") headers.set("Accept", accept);
  } catch (error: unknown) {
    throw refused(error, prepared);
  }
  for (const header of routed.headers) headers.append(header.name, header.value);
  if (payloadPresent) {
    headers.set("Content-Type", contentType!);
    try {
      requestBody = await applyRequestContentCodings(
        headers,
        parameters,
        requestBody!,
        prepared.options.requestContentCodings,
      );
    } catch (error: unknown) {
      throw refused(error, prepared);
    }
  }
  let response: Response;
  try {
    const fetchFn = prepared.options.fetch ?? globalThis.fetch;
    if (!fetchFn) throw new Error("no fetch implementation is available");
    response = await swagger20FetchWithSafeRedirects(fetchFn, {
      url,
      method: prepared.operation.method.toUpperCase(),
      headers,
      body: requestBody === undefined ? null : Uint8Array.from(requestBody).buffer,
      signal: prepared.options.signal,
      redirect: prepared.options.redirect ?? "manual",
    }, security);
  } catch (error: unknown) {
    throw new Swagger20ExecutionError("ERR_CONNECT_FAILED", errorMessage(error), { cause: error });
  }
  let governing: Swagger20ResolvedResponse | undefined;
  const resultResponse = response.clone();
  try {
    governing = await governingSwagger20Response(prepared.operation, responses, response.status);
  } catch (error: unknown) {
    throw responseError(error);
  }
  if (response.status === 101) throw responseError(new Error("101 Switching Protocols cannot complete this unary binding"));
  let codingTokens: string[];
  try {
    codingTokens = responseContentCodingTokens(response.headers, governing);
  } catch (error: unknown) {
    throw responseError(error);
  }
  let bytes: Uint8Array<ArrayBufferLike> = new Uint8Array(await response.arrayBuffer());
  const noContent = prepared.operation.method === "head"
    || response.status >= 100 && response.status < 200
    || [204, 205, 304].includes(response.status);
  if (noContent) {
    if (prepared.operation.method !== "head" && bytes.byteLength > 0) {
      throw responseError(new Error("HTTP response carries content where none is permitted"));
    }
    bytes = new Uint8Array();
  } else {
    try {
      bytes = await decodeResponseContentCodings(
        codingTokens,
        bytes,
        prepared.options.responseContentCodings,
      );
    } catch (error: unknown) {
      throw responseError(error);
    }
  }
  const success = response.status >= 200 && response.status < 300;
  const declaration = {
    declared: governing !== undefined,
    ...(governing ? { responseKey: governing.key } : {}),
  };
  if (bytes.byteLength === 0) {
    if (!success) throw httpFailure(response, governing, undefined);
    return { outputPresent: false, status: response.status, headers: response.headers, response: resultResponse, declaration };
  }
  if (!governing) {
    if (!success) throw httpFailure(response, governing, undefined);
    throw new Swagger20ExecutionError(
      "ERR_RESPONSE_ERROR",
      `non-empty response status ${response.status} has no governing exact or default Response Object`,
    );
  }
  let output: unknown;
  try {
    output = await decodeSwagger20Response(
      prepared.document,
      prepared.operation,
      governing,
      bytes,
      response.headers.get("Content-Type") ?? "",
      prepared.options.responseCharacterEncodings,
    );
  } catch (error: unknown) {
    if (!success && governing.invalid) throw httpFailure(response, governing, undefined);
    throw responseError(error);
  }
  if (!success) throw httpFailure(response, governing, output);
  return { outputPresent: true, output, status: response.status, headers: response.headers, response: resultResponse, declaration };
}

interface Swagger20PlannedRequest {
  url: string;
  method: string;
  headers: Headers;
  body: BodyInit | null;
  signal?: AbortSignal;
  redirect: RequestRedirect;
}

async function swagger20FetchWithSafeRedirects(
  fetchFn: typeof globalThis.fetch,
  request: Swagger20PlannedRequest,
  credentials: readonly Swagger20CredentialPlacement[],
): Promise<Response> {
  if (request.redirect !== "follow") return swagger20Fetch(fetchFn, request);
  let current = { ...request, redirect: "manual" as const };
  for (let followed = 0; ; followed += 1) {
    const response = await swagger20Fetch(fetchFn, current);
    if (![301, 302, 303, 307, 308].includes(response.status)) return response;
    const location = response.headers.get("location");
    if (!location || swagger20RedirectRewritesMethod(response.status, current.method)) return response;
    if (followed >= 9) throw new Error("stopped after 10 redirects");
    let nextURL: URL;
    try {
      nextURL = new URL(location, current.url);
    } catch {
      return response;
    }
    const headers = new Headers(current.headers);
    if (new URL(current.url).origin !== nextURL.origin) {
      headers.delete("Cookie");
      for (const credential of credentials) {
        if (credential.query) nextURL.searchParams.delete(credential.name);
        else headers.delete(credential.name);
      }
    }
    current = { ...current, url: nextURL.toString(), headers };
  }
}

function swagger20Fetch(
  fetchFn: typeof globalThis.fetch,
  request: Swagger20PlannedRequest,
): Promise<Response> {
  return fetchFn(request.url, {
    method: request.method,
    headers: request.headers,
    ...(request.body === null ? {} : { body: request.body }),
    signal: request.signal,
    redirect: request.redirect,
  });
}

function swagger20RedirectRewritesMethod(status: number, method: string): boolean {
  const normalized = method.toUpperCase();
  if (status === 303) return normalized !== "GET" && normalized !== "HEAD";
  return (status === 301 || status === 302) && normalized === "POST";
}

async function applyRequestContentCodings(
  headers: Headers,
  parameters: Swagger20ParameterSet,
  body: Uint8Array,
  codecs: ReadonlyMap<string, Swagger20ContentCodec> | undefined,
): Promise<Uint8Array> {
  const raw = headers.get("Content-Encoding");
  if (!raw) return body;
  const governing = parameters.nonBody.filter((parameter) =>
    parameter.in === "header" && parameter.name.toLowerCase() === "content-encoding");
  if (governing.length !== 1) throw new Error("request Content-Encoding has no effective governing Header Parameter");
  if (governing[0]!.typeName !== "string") {
    throw new Error("request Content-Encoding governing Header Parameter is not a string declaration");
  }
  for (const token of contentCodingTokens(raw)) {
    if (token === "identity") continue;
    const codec = codecs?.get(token);
    if (!codec) throw new Error(`request content-coding ${JSON.stringify(token)} is unsupported`);
    try { body = codingBytes(await codec(body)); }
    catch (error: unknown) { throw new Error(`request content-coding ${JSON.stringify(token)} failed`, { cause: error }); }
  }
  return body;
}

async function decodeResponseContentCodings(
  tokens: string[],
  body: Uint8Array,
  codecs: ReadonlyMap<string, Swagger20ContentCodec> | undefined,
): Promise<Uint8Array> {
  for (let index = tokens.length - 1; index >= 0; index--) {
    const token = tokens[index]!;
    if (token === "identity") continue;
    const codec = codecs?.get(token);
    if (!codec) throw new Error(`response content-coding ${JSON.stringify(token)} is unsupported`);
    try { body = codingBytes(await codec(body)); }
    catch (error: unknown) { throw new Error(`response content-coding ${JSON.stringify(token)} failed`, { cause: error }); }
  }
  return body;
}

function responseContentCodingTokens(
  headers: Headers,
  governing: Swagger20ResolvedResponse | undefined,
): string[] {
  const raw = headers.get("Content-Encoding");
  if (!raw) return [];
  const tokens = contentCodingTokens(raw);
  if (governing) {
    for (const header of responseHeaders(governing.raw, "Content-Encoding")) headerObjectAdmits(header, raw);
  }
  return tokens;
}

function responseHeaders(response: Swagger20Object, name: string): Swagger20Object[] {
  const headers = objectMember(response, "headers");
  if (!headers.valid) return [];
  const matches = Object.entries(headers.value!).filter(([declared]) => declared.toLowerCase() === name.toLowerCase());
  return matches.flatMap(([, value]) =>
    value !== null && typeof value === "object" && !Array.isArray(value) ? [value as Swagger20Object] : []);
}

function headerObjectAdmits(header: Swagger20Object, value: string): void {
  const type = stringMember(header, "type");
  if (type.value !== "string" || !Array.isArray(header.enum)) return;
  const domain = header.enum.filter((member): member is string => typeof member === "string");
  if (domain.length > 0 && !domain.includes(value)) {
    throw new Error("value is outside enum");
  }
}

function codingBytes(value: Swagger20ContentCodingResult): Uint8Array {
  if (value instanceof Uint8Array) return value;
  if (value instanceof ArrayBuffer) return new Uint8Array(value);
  if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  throw new Error("content-coding result is not a byte sequence");
}

function httpFailure(response: Response, governing: Swagger20ResolvedResponse | undefined, details: unknown): Swagger20ExecutionError {
  return new Swagger20ExecutionError("ERR_EXECUTION_FAILED", `HTTP ${response.status}`, {
    ...(details === undefined ? {} : { details }),
    evidence: {
      response: response.clone(),
      status: response.status,
      openapi: { declared: governing !== undefined, responseKey: governing?.key ?? "" },
    },
  });
}

function responseError(error: unknown): Swagger20ExecutionError {
  return new Swagger20ExecutionError("ERR_RESPONSE_ERROR", errorMessage(error), { cause: error });
}

/**
 * Classifies one pre-dispatch refusal into its §3.2 species. The target is the
 * asserted context scope: the source location the caller supplied, matching the
 * side-effect-free preflight's assertion.
 */
function refused(error: unknown, prepared?: PreparedSwagger20Operation): Swagger20ExecutionError {
  return swagger20RefusalError(error, prepared?.options.source.location ?? "");
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Resolves the effective server, deciding the species of a failure. §12.1's
 * `server` row admits either one effective scheme with the artifact's own host
 * and basePath, or "one complete URL ... replacing the resolved server base",
 * so the question "would a supplied server repair this?" is answered by
 * resolving again with one — the same probe the synthesis surface uses, so the
 * two surfaces cannot drift apart. A caller who already configured the point is
 * looking at their own value, not at an awaited one.
 */
function resolveSwagger20ServerForInvocation(prepared: PreparedSwagger20Operation): string {
  try {
    return resolveSwagger20Server(
      prepared.document,
      prepared.operation,
      prepared.options.server,
      prepared.options.serverSchemeIndex,
    );
  } catch (error: unknown) {
    if (error instanceof ConfigRequired) throw error;
    if (prepared.options.server !== undefined || prepared.options.serverSchemeIndex !== undefined) throw error;
    if (errorMessage(error).includes("host must contain only an authority")) throw error;
    try {
      resolveSwagger20Server(prepared.document, prepared.operation, "https://configured.invalid", undefined);
    } catch {
      throw error;
    }
    throw swagger20ConfigRequired("server", "");
  }
}

/**
 * Whether the declared payload lane must emit on every invocation: an
 * `in: body` parameter marked required, or any required `formData` parameter.
 * The Go engine's `swagger20PayloadIsRequired` is the same predicate.
 */
function swagger20PayloadIsRequired(model: { body?: { required?: boolean }; form?: { required?: boolean }[] }): boolean {
  if (model.body) return model.body.required === true;
  return (model.form ?? []).some((parameter) => parameter.required === true);
}
