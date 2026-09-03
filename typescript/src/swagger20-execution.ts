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
  swagger20PayloadFor,
  swagger20ResponsesFor,
  type Swagger20ResolvedResponse,
} from "./swagger20-media.js";
import { booleanMember, objectMember, stringMember, type Swagger20Object } from "./swagger20-model.js";
import { resolveSwagger20Server } from "./swagger20-server.js";
import { applySwagger20Security, selectSwagger20Security } from "./swagger20-security.js";

export interface Swagger20ExecutionResult {
  outputPresent: boolean;
  output?: unknown;
  status: number;
  headers: Headers;
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
  } catch (error: unknown) {
    throw refused(error, prepared);
  }
  applySwagger20Security(routed, security);
  const payloadPresent = routed.bodyPresent || routed.formPresent;
  if (payloadPresent && ["get", "head", "delete", "options"].includes(prepared.operation.method)) {
    throw new Swagger20ExecutionError(
      "ERR_REFUSED",
      `Swagger 2.0 ${prepared.operation.method} operations exclude the payload lane`,
    );
  }
  let requestBody: Uint8Array | undefined;
  let contentType: string | undefined;
  if (payloadPresent) {
    try {
      const model = await swagger20PayloadFor(parameters, prepared.operation);
      const consumes = effectiveSwagger20MediaSet(prepared.document, prepared.operation, "consumes");
      const selection = selectSwagger20RequestMedia(consumes, model, prepared.options.requestMedia);
      const encoded = encodeSwagger20RequestPayload(selection, model, routed, prepared.options.propertyMedia);
      requestBody = encoded.body;
      contentType = encoded.contentType;
    } catch (error: unknown) {
      throw refused(error, prepared);
    }
  }
  const query = swagger20RawQuery(routed.query);
  const url = `${server}${routed.resolvedPath}${query === "" ? "" : `?${query}`}`;
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") throw new Error("target scheme is not HTTP");
    decodeURIComponent(parsed.pathname + parsed.search);
  } catch (error: unknown) {
    throw refused(error, prepared);
  }
  const headers = new Headers();
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
    response = await fetchFn(url, {
      method: prepared.operation.method.toUpperCase(),
      headers,
      ...(requestBody === undefined ? {} : { body: Uint8Array.from(requestBody).buffer }),
      signal: prepared.options.signal,
    });
  } catch (error: unknown) {
    throw new Swagger20ExecutionError("ERR_CONNECT_FAILED", errorMessage(error), { cause: error });
  }
  let governing: Swagger20ResolvedResponse | undefined;
  try {
    governing = await governingSwagger20Response(prepared.operation, responses, response.status);
  } catch (error: unknown) {
    throw responseError(error);
  }
  let bytes: Uint8Array<ArrayBufferLike> = new Uint8Array(await response.arrayBuffer());
  if (prepared.operation.method === "head") bytes = new Uint8Array();
  try {
    bytes = await decodeResponseContentCodings(
      response.headers,
      governing,
      bytes,
      prepared.options.responseContentCodings,
    );
  } catch (error: unknown) {
    throw responseError(error);
  }
  const success = response.status >= 200 && response.status < 300;
  if (bytes.byteLength === 0) {
    if (!success) throw httpFailure(response, governing, undefined);
    return { outputPresent: false, status: response.status, headers: response.headers };
  }
  if (!governing) {
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
    );
  } catch (error: unknown) {
    throw responseError(error);
  }
  if (!success) throw httpFailure(response, governing, output);
  return { outputPresent: true, output, status: response.status, headers: response.headers };
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
  headers: Headers,
  governing: Swagger20ResolvedResponse | undefined,
  body: Uint8Array,
  codecs: ReadonlyMap<string, Swagger20ContentCodec> | undefined,
): Promise<Uint8Array> {
  const raw = headers.get("Content-Encoding");
  if (!raw) return body;
  if (!governing) throw new Error("coded response has no governing Response Object");
  const header = responseHeader(governing.raw, "Content-Encoding");
  if (!header) throw new Error("actual response Content-Encoding has no governing Header Object");
  headerObjectAdmits(header, raw);
  const tokens = contentCodingTokens(raw);
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

function responseHeader(response: Swagger20Object, name: string): Swagger20Object | undefined {
  const headers = objectMember(response, "headers");
  if (!headers.present) return undefined;
  if (!headers.valid) throw new Error("governing Response headers is not an object");
  const matches = Object.entries(headers.value!).filter(([declared]) => declared.toLowerCase() === name.toLowerCase());
  if (matches.length > 1) throw new Error(`governing response has ambiguous case-insensitive Header Objects named ${JSON.stringify(name)}`);
  if (matches.length === 0) return undefined;
  const value = matches[0]![1];
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`governing response Header Object ${JSON.stringify(matches[0]![0])} is not an object`);
  }
  return value as Swagger20Object;
}

function headerObjectAdmits(header: Swagger20Object, value: string): void {
  const type = stringMember(header, "type");
  if (!type.valid || type.value !== "string") throw new Error("Header Object does not declare type string");
  const required = booleanMember(header, "required");
  if (required.present && !required.valid) throw new Error("Header Object required is not a boolean");
  if (Object.hasOwn(header, "enum") && (!Array.isArray(header.enum) || !header.enum.includes(value))) {
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
    evidence: { status: response.status, openapi: { declared: governing !== undefined, responseKey: governing?.key ?? "" } },
  });
}

function responseError(error: unknown): Swagger20ExecutionError {
  return new Swagger20ExecutionError("ERR_RESPONSE_ERROR", errorMessage(error), { cause: error });
}

/**
 * Classifies one pre-dispatch refusal into its §3.2 species. The target is the
 * asserted context scope, {@link PreparedSwagger20Operation.contextTarget}:
 * the resolved server base once it resolves, matching the side-effect-free
 * preflight's assertion.
 */
function refused(error: unknown, prepared?: PreparedSwagger20Operation): Swagger20ExecutionError {
  return swagger20RefusalError(error, prepared?.contextTarget() ?? "");
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
