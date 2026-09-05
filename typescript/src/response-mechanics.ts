import {
  governingResponse,
  governingResponseMediaMatch,
  isJSONMediaType,
  parseMediaType,
} from "./media.js";
import { resolveDeclaration, type SchemaDeclaration } from "./resolved-declaration.js";
import { classifyOpenAPI32SequentialResponse } from "./openapi32-sequential-response.js";
import type {
  OpenAPIDocument,
  OpenAPIMediaType,
  OpenAPIOperation,
  OpenAPIParameter,
  OpenAPIResponse,
} from "./types.js";

const HTTP_TOKEN = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
export const DEFAULT_OPENAPI_MAX_DELIVERY_UNIT_BYTES = 10 * 1024 * 1024;

export class OpenAPIWireMechanicsError extends Error {
  constructor(
    readonly code: "ERR_REFUSED" | "ERR_PROTOCOL" | "ERR_RESPONSE_ERROR",
    message: string = code,
  ) {
    super(message);
    this.name = "OpenAPIWireMechanicsError";
  }
}

export type OpenAPIContentCodingResult = Uint8Array | ArrayBuffer | ArrayBufferView;
export type OpenAPIContentEncoder = (
  body: Uint8Array,
) => OpenAPIContentCodingResult | Promise<OpenAPIContentCodingResult>;
export type OpenAPIContentDecoder = OpenAPIContentEncoder;
export type OpenAPICharacterEncoder = (value: string) => OpenAPIContentCodingResult;
export type OpenAPICharacterDecoder = (bytes: Uint8Array) => string;

export interface OpenAPIResponseMechanicsModel {
  document: OpenAPIDocument;
  operation: OpenAPIOperation;
  parameters: OpenAPIParameter[];
  method: string;
  emptyResponse: boolean;
  maxDeliveryUnitBytes?: number;
  responseCharacterDecodings?: ReadonlyMap<string, OpenAPICharacterDecoder>;
  unaryEventStream?: {
    actualContentTypeHeader: string;
    mediaType: string;
  };
}

export function normalizeOpenAPIContentCodings<T>(
  input: Record<string, T> | undefined,
  direction: "request" | "response",
): { codecs: ReadonlyMap<string, T>; defect?: Error } {
  const codecs = new Map<string, T>();
  for (const [authored, codec] of Object.entries(input ?? {})) {
    const token = authored.trim().toLowerCase();
    if (typeof codec !== "function") {
      return { codecs, defect: new Error(`invalid ${direction} content-coding capability ${JSON.stringify(authored)}`) };
    }
    if (codecs.has(token)) {
      return { codecs, defect: new Error(`${direction} content-coding capabilities collide at ${JSON.stringify(token)}`) };
    }
    codecs.set(token, codec);
  }
  return { codecs };
}

export async function governOpenAPIRequest(
  input: RequestInfo | URL,
  init: RequestInit | undefined,
  model: OpenAPIResponseMechanicsModel,
  codecs: ReadonlyMap<string, OpenAPIContentEncoder>,
): Promise<{ input: RequestInfo | URL; init: RequestInit | undefined }> {
  const sourceHeaders = init?.headers ?? (input instanceof Request ? input.headers : undefined);
  const headers = new Headers(sourceHeaders);
  const rawCoding = headers.get("Content-Encoding") ?? "";
  let body = init?.body ?? (input instanceof Request ? input.body : null);
  if (rawCoding !== "") {
    if (body === null) requestRefusal("request Content-Encoding cannot be supplied when no request representation is emitted");
    const governing = effectiveContentEncodingParameter(model.parameters);
    if (!governing) requestRefusal("request Content-Encoding has no effective governing Header Parameter");
    if (!schemaAdmitsHeaderValue(governing.schema, rawCoding, model.document.openapi?.startsWith("3.0") ?? true)) {
      requestRefusal("request Content-Encoding is not admitted by its governing Header Parameter");
    }
    let tokens: string[];
    try {
      tokens = parsedContentCodings(rawCoding);
    } catch {
      requestRefusal("invalid Content-Encoding field value");
    }
    let bytes = await bodyBytes(body);
    for (const token of tokens) {
      if (token === "identity") continue;
      const codec = codecs.get(token);
      if (!codec) requestRefusal(`request content-coding ${JSON.stringify(token)} is unsupported`);
      try {
        bytes = codingBytes(await codec(bytes));
      } catch {
        requestRefusal(`request content-coding ${JSON.stringify(token)} failed`);
      }
    }
    body = bytesToArrayBuffer(bytes);
    headers.delete("Content-Length");
  }
  const nextInit: RequestInit = { ...init, headers, ...(body !== null ? { body } : {}) };
  if (input instanceof Request && init === undefined) {
    return { input: new Request(input, nextInit), init: undefined };
  }
  return { input, init: nextInit };
}

export async function governOpenAPIResponse(
  response: Response,
  model: OpenAPIResponseMechanicsModel,
  codecs: ReadonlyMap<string, OpenAPIContentDecoder>,
): Promise<Response> {
  const governing = governingResponse(model.operation, response.status);
  if (governing) requireGovernedResponseHeaders(governing.response, response.headers);

  // A native 3.2 sequential response must remain a stream: the invocation
  // layer owns framing and applies the delivery-unit limit per item. An
  // encoded representation is decoded below before it can be item-framed.
  const sourceContentType = response.headers.get("Content-Type");
  const sourceCoding = response.headers.get("Content-Encoding") ?? "";
  if (
    model.method.toLowerCase() !== "head"
    && response.body
    && sourceContentType !== null
    && sourceCoding === ""
    && governing
  ) {
    // THIS PRE-CHECK MAY NOT REFUSE, and Round R2's F2 finding is why. It runs
    // before a single byte has been read, so it cannot yet know whether the
    // response carries any content at all -- and §9.6 says an EMPTY response
    // "has zero content octets" and "emit[s] no operation output value", so an
    // empty body contradicts no media declaration whatever `Content-Type` the
    // peer stamped on it. (`new Response("")` stamps `text/plain;charset=UTF-8`
    // by itself, so this is the ordinary case rather than a contrived one.)
    // Refusing here made an empty 2xx an ERR_PROTOCOL on this lane while Go's
    // 3.2 lane and both engines on 3.0/3.1 completed it -- the one cell in the
    // eight-shape matrix where the two engines still disagreed after Round R.
    //
    // A media mismatch therefore FALLS THROUGH to the byte-reading path below,
    // which knows the body's length: it reports exactly this error for a
    // NON-EMPTY body and returns absence for an empty one. Nothing is weakened;
    // the same question is asked where it can be answered.
    let match: ReturnType<typeof governingResponseMediaMatch> | undefined;
    try {
      match = governingResponseMediaMatch(governing.response, sourceContentType, true, true);
    } catch {
      match = undefined;
    }
    let sequential;
    if (match) {
      try {
        sequential = classifyOpenAPI32SequentialResponse(sourceContentType, match.media);
      } catch {
        responseError("response itemSchema has no incorporated sequential framing");
      }
    }
    // `sequential` is only ever set inside the `match` guard above, so a
    // sequential classification always has the declaration it was derived from.
    const eventStream = match && parseMediaType(sourceContentType, true).base === "text/event-stream";
    if (eventStream || (model.document.openapi === "3.2.0" && sequential && match)) {
      if (match && sequential === "sse") {
        validateResponseMediaLane(match.media, sourceContentType, false, model.responseCharacterDecodings);
      }
      return response;
    }
  }

  let bytes: Uint8Array;
  const deliveryLimit = resolveOpenAPIDeliveryUnitLimit(model.maxDeliveryUnitBytes);
  try {
    bytes = await readResponseBytes(response, deliveryLimit);
  } catch (error: unknown) {
    if (error instanceof OpenAPIWireMechanicsError) throw error;
    throw new OpenAPIWireMechanicsError("ERR_PROTOCOL");
  }
  if (model.method.toLowerCase() === "head") bytes = new Uint8Array();
  const headers = new Headers(response.headers);
  const rawCoding = headers.get("Content-Encoding") ?? "";
  const successful = response.status >= 200 && response.status < 300;
  const noContent = model.method.toLowerCase() === "head" || responseBodyForbidden(response.status);
  if (rawCoding !== "") {
    let tokens: string[];
    try {
      tokens = parsedContentCodings(rawCoding);
    } catch {
      responseError("invalid Content-Encoding field value");
    }
    if (governing) {
      for (const declared of responseHeaders(governing.response, "Content-Encoding")) {
        if (!headerExactStringDomainAdmits(declared.schema as SchemaDeclaration, rawCoding, model.document.openapi?.startsWith("3.0") ?? true)) {
          responseError("actual response Content-Encoding is outside a binding-understood exact Header domain");
        }
      }
    }
    if (!noContent && (governing || !successful)) {
      for (let index = tokens.length - 1; index >= 0; index -= 1) {
        const token = tokens[index]!;
        if (token === "identity") continue;
        const codec = codecs.get(token);
        if (!codec) responseError(`response content-coding ${JSON.stringify(token)} is unsupported`);
        try {
          bytes = codingBytes(await codec(bytes));
        } catch {
          responseError(`response content-coding ${JSON.stringify(token)} failed`);
        }
        if (bytes.byteLength > deliveryLimit) {
          throw new OpenAPIWireMechanicsError("ERR_RESPONSE_ERROR");
        }
      }
    }
  }

  model.emptyResponse = bytes.length === 0;
  if (bytes.length > 0) {
    if (!governing) {
      if (successful) responseError("non-empty response has no governing Response Object");
      return responseFromBytes(response, headers, bytes);
    }
    let contentType = headers.get("Content-Type") ?? "";
    if (contentType === "") {
      contentType = "application/octet-stream";
      headers.set("Content-Type", contentType);
    }
    let match: ReturnType<typeof governingResponseMediaMatch>;
    try {
      match = governingResponseMediaMatch(governing.response, contentType, true, true);
    } catch {
      if (successful) responseError("actual response media does not match its governing declaration");
      return responseFromBytes(response, headers, bytes);
    }
    if (!match) {
      if (successful) responseError("actual response media does not match its governing declaration");
      return responseFromBytes(response, headers, bytes);
    }
    try {
      validateResponseMediaLane(
        match.media,
        contentType,
        model.document.openapi?.startsWith("3.0") ?? true,
        model.responseCharacterDecodings,
      );
    } catch (error: unknown) {
      if (successful) throw error;
      return responseFromBytes(response, headers, bytes);
    }
    if (parseMediaType(contentType, true).base === "text/event-stream" && model.unaryEventStream) {
      headers.set(model.unaryEventStream.actualContentTypeHeader, contentType);
      headers.set("Content-Type", model.unaryEventStream.mediaType);
    }
  }
  headers.delete("Content-Length");
  const body = bytes.length === 0 && responseBodyForbidden(response.status)
    ? null
    : bytesToArrayBuffer(bytes);
  return new Response(body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

function responseFromBytes(response: Response, headers: Headers, bytes: Uint8Array): Response {
  headers.delete("Content-Length");
  return new Response(bytes.length === 0 && responseBodyForbidden(response.status) ? null : bytesToArrayBuffer(bytes), {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

/** Adds an adapter-private unary alias for every governed SSE declaration. */
export function prepareOpenAPIUnaryResponseView(
  operation: OpenAPIOperation,
  unaryMediaType: string,
): void {
  for (const response of Object.values(operation.responses ?? {})) {
    if (!response || typeof response !== "object") continue;
    try {
      const match = governingResponseMediaMatch(response, "text/event-stream", true, true);
      if (match) {
        response.content ??= {};
        response.content[unaryMediaType] = match.media;
      }
    } catch { /* malformed declarations remain owned by ordinary planning */ }
  }
}

function effectiveContentEncodingParameter(parameters: OpenAPIParameter[]): OpenAPIParameter | null {
  const found = parameters.filter((parameter) =>
    parameter.in === "header" && parameter.name?.toLowerCase() === "content-encoding");
  return found.length === 1 ? found[0]! : null;
}

function responseHeaders(response: OpenAPIResponse, wanted: string): Record<string, unknown>[] {
  const headers = asRecord(response.headers);
  return Object.entries(headers ?? {})
    .filter(([name, value]) => name.toLowerCase() === wanted.toLowerCase() && asRecord(value) !== null)
    .map(([, value]) => asRecord(value)!);
}

function requireGovernedResponseHeaders(response: OpenAPIResponse, actual: Headers): void {
  const headers = asRecord(response.headers);
  for (const [name, raw] of Object.entries(headers ?? {})) {
    const declaration = asRecord(raw);
    if (name.toLowerCase() === "content-type" || declaration?.required !== true) continue;
    if (!actual.has(name)) responseError(`required response header ${JSON.stringify(name)} is absent`);
  }
}

function schemaAdmitsHeaderValue(
  schema: SchemaDeclaration,
  value: string,
  oas30: boolean,
): boolean {
  const declaration = resolveDeclaration(schema, oas30);
  return !declaration.ambiguous
    && (declaration.typeless() || declaration.admitsStringAsSoleNonNullType())
    && declaration.admitsStringEnumValue(value);
}

/**
 * Response Content-Encoding uses only the binding's deliberately small,
 * decidable raw-string subset. Each non-empty string-valued enum/const domain
 * contributes conjunctively; all other Schema constraints are descriptive at
 * this wire boundary and cannot make processors invent inverse serialization.
 */
function headerExactStringDomainAdmits(
  schema: SchemaDeclaration,
  value: string,
  oas30: boolean,
): boolean {
  const declaration = resolveDeclaration(schema, oas30);
  if (declaration.ambiguous || declaration.admitsNoInstance()) return true;
  return declaration.admitsStringEnumValue(value);
}

function parsedContentCodings(raw: string): string[] {
  const members = raw.split(",");
  if (members.length === 0) throw new Error("empty Content-Encoding");
  return members.map((member) => {
    const token = member.trim().toLowerCase();
    if (!HTTP_TOKEN.test(token)) throw new Error("invalid Content-Encoding token");
    return token;
  });
}

function validateResponseMediaLane(
  media: OpenAPIMediaType,
  contentType: string,
  oas30: boolean,
  decoders?: ReadonlyMap<string, OpenAPICharacterDecoder>,
): void {
  const parsed = parseMediaType(contentType, true);
  if (isJSONMediaType(parsed.base)) return;
  const declaration = resolveDeclaration(media.schema, oas30);
  if (isCharacterDataMedia(parsed.base) && declaration.admitsStringAsSoleNonNullType()) {
    requireSupportedCharset(parsed.params.charset ?? "utf-8", decoders);
    return;
  }
  if (declaration.typeless()) return;
  if (declaration.admitsStringAsSoleNonNullType()) {
    const format = declaration.format();
    const encoding = declaration.keywordString("contentEncoding");
    if (format.conflict || encoding.conflict) responseError("response declaration has conflicting carriage annotations");
    if ((oas30 && (format.value === "binary" || format.value === "byte")) || (!oas30 && encoding.value !== "")) {
      return;
    }
  }
  responseError(`response media ${JSON.stringify(contentType)} selects no incorporated carriage lane`);
}

function isCharacterDataMedia(base: string): boolean {
  if (base.startsWith("text/")) return true;
  return base === "application/xml" || base.endsWith("+xml");
}

function requireSupportedCharset(
  charset: string,
  decoders?: ReadonlyMap<string, OpenAPICharacterDecoder>,
): void {
  if (!["utf-8", "utf8"].includes(charset.toLowerCase()) && !decoders?.has(charset.toLowerCase())) {
    responseError(`unsupported response charset ${JSON.stringify(charset)}`);
  }
}

async function bodyBytes(body: BodyInit | ReadableStream<Uint8Array> | null): Promise<Uint8Array> {
  if (body === null) return new Uint8Array();
  return new Uint8Array(await new Response(body as BodyInit).arrayBuffer());
}

async function readResponseBytes(response: Response, limit: number): Promise<Uint8Array> {
  if (!response.body) return new Uint8Array();
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!value) continue;
      length += value.byteLength;
      if (length > limit) {
        await reader.cancel();
        throw new OpenAPIWireMechanicsError("ERR_RESPONSE_ERROR");
      }
      chunks.push(value);
    }
  } catch (error: unknown) {
    try { await reader.cancel(); } catch { /* best effort */ }
    throw error;
  }
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return bytes;
}

function codingBytes(value: OpenAPIContentCodingResult): Uint8Array {
  if (value instanceof Uint8Array) return value;
  if (value instanceof ArrayBuffer) return new Uint8Array(value);
  return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
}

function bytesToArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(bytes.byteLength);
  copy.set(bytes);
  return copy.buffer;
}

function responseBodyForbidden(status: number): boolean {
  return status === 101 || status === 204 || status === 205 || status === 304;
}

function requestRefusal(message: string): never {
  throw new OpenAPIWireMechanicsError("ERR_REFUSED", message);
}

function responseError(_message: string): never {
  // Protocol diagnostics are deliberately structural at the abstract SDK
  // boundary; HTTP evidence and transport prose are not portable error data.
  throw new OpenAPIWireMechanicsError("ERR_PROTOCOL");
}

function resolveOpenAPIDeliveryUnitLimit(value: number | undefined): number {
  return value !== undefined && Number.isFinite(value) && value > 0
    ? value
    : DEFAULT_OPENAPI_MAX_DELIVERY_UNIT_BYTES;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}
