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
    responses = swagger20ResponsesFor(prepared.operation);
  } catch (error: unknown) {
    throw refused(error);
  }
  let routed;
  try {
    routed = routeSwagger20Input(
      parameters,
      prepared.operation.path,
      input,
      prepared.options.parameterConverter,
      prepared.options.emptyValueForm,
    );
  } catch (error: unknown) {
    throw refused(error);
  }
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
      throw refused(error);
    }
  }
  const server = configuredServer(prepared.options.server);
  const query = swagger20RawQuery(routed.query);
  const url = `${server}${routed.resolvedPath}${query === "" ? "" : `?${query}`}`;
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") throw new Error("target scheme is not HTTP");
    decodeURIComponent(parsed.pathname + parsed.search);
  } catch (error: unknown) {
    throw refused(error);
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
      throw refused(error);
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

function configuredServer(value: string | undefined): string {
  if (!value) throw new Swagger20ExecutionError("ERR_REFUSED", "Swagger 2.0 target requires a complete server URL");
  let parsed: URL;
  try { parsed = new URL(value); }
  catch (error: unknown) { throw refused(error); }
  if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.host === "" || parsed.search !== "" || parsed.hash !== "") {
    throw new Swagger20ExecutionError("ERR_REFUSED", "Swagger 2.0 consumer server is not a complete HTTP target URL");
  }
  return value;
}

function refused(error: unknown): Swagger20ExecutionError {
  return error instanceof Swagger20ExecutionError
    ? error
    : new Swagger20ExecutionError("ERR_REFUSED", errorMessage(error), { cause: error });
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
