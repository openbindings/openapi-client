import {
  InvocationError,
  ERR_RESPONSE_ERROR,
  resolveDeliveryUnitLimit,
  type BindingHandle,
  type BindingInvocationArgs,
  type InvokeSite,
  type Metadata,
} from "./internal/index.js";
import { isJSONMediaType, parseMediaType } from "./media.js";
import { openAPI32PositionalMultipart } from "./openapi32-media.js";
import { resolveDeclaration, type SchemaDeclaration } from "./resolved-declaration.js";
import { streamSSE } from "./sse.js";
import type { OpenAPIMediaType } from "./types.js";
import { errorMessage, jsonCarriesLoneSurrogate } from "./util.js";

/** One item-framing form incorporated by an OpenAPI 3.2 response media type. */
export type OpenAPI32SequentialResponseKind =
  | "json-lines"
  | "json-seq"
  | "sse"
  | "multipart";

/**
 * Reports the response item framing selected by a concrete media type and
 * its governing Media Type Object. The absent result denotes a unary lane.
 */
export function classifyOpenAPI32SequentialResponse(
  mediaType: string,
  media: OpenAPIMediaType,
): OpenAPI32SequentialResponseKind | undefined {
  const parsed = parseMediaType(mediaType, true);
  if (parsed.base === "application/jsonl" || parsed.base === "application/x-ndjson") {
    return "json-lines";
  }
  if (parsed.base === "application/json-seq" || parsed.base.endsWith("+json-seq")) {
    return "json-seq";
  }
  if (parsed.base === "text/event-stream") return "sse";
  if (parsed.base.startsWith("multipart/") && openAPI32PositionalMultipart(media)) {
    return "multipart";
  }
  if (Object.hasOwn(media, "itemSchema")) {
    throw new Error(
      `response media ${JSON.stringify(parsed.base)} declares itemSchema but has no incorporated sequential framing`,
    );
  }
  return undefined;
}

/** @internal Streams one operation output for each OpenAPI 3.2 response item. */
export async function streamOpenAPI32SequentialResponse(
  response: Response,
  args: BindingInvocationArgs,
  site: InvokeSite,
  inv: BindingHandle<unknown, unknown>,
  invocationMeta: Metadata,
  kind: OpenAPI32SequentialResponseKind,
  media: OpenAPIMediaType,
): Promise<void> {
  switch (kind) {
    case "sse":
      await streamSSE(response, args, site, inv, invocationMeta, () => "", true);
      return;
    case "json-lines":
      await streamJSONLines(response, args, inv, invocationMeta);
      return;
    case "json-seq":
      await streamJSONSequence(response, args, inv, invocationMeta);
      return;
    case "multipart":
      await streamMultipart(response, args, inv, invocationMeta, media);
  }
}

async function streamJSONLines(
  response: Response,
  args: BindingInvocationArgs,
  inv: BindingHandle<unknown, unknown>,
  metadata: Metadata,
): Promise<void> {
  const reader = response.body?.getReader();
  if (!reader) {
    inv.closeOutput();
    return;
  }
  const limit = resolveDeliveryUnitLimit(args);
  let pending: Uint8Array<ArrayBufferLike> = new Uint8Array();
  let index = 0;
  try {
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      pending = appendBytes(pending, chunk.value);
      for (;;) {
        const newline = pending.indexOf(0x0a);
        if (newline < 0) break;
        let item = pending.slice(0, newline);
        pending = pending.slice(newline + 1);
        if (item.at(-1) === 0x0d) item = item.slice(0, -1);
        if (!await emitJSONItem(item, index, "JSON Lines", limit, args, inv, metadata)) return;
        index += 1;
      }
      if (pending.byteLength > limit + 1) {
        failSequential(inv, `sequential response item exceeds ${limit} byte limit`);
        await reader.cancel().catch(() => {});
        return;
      }
    }
    if (pending.byteLength > 0) {
      if (pending.at(-1) === 0x0d) pending = pending.slice(0, -1);
      if (!await emitJSONItem(pending, index, "JSON Lines", limit, args, inv, metadata)) return;
    }
    inv.closeOutput();
  } catch (error: unknown) {
    if (!inv.signal.aborted) failSequential(inv, errorMessage(error));
  } finally {
    reader.releaseLock();
  }
}

async function streamJSONSequence(
  response: Response,
  args: BindingInvocationArgs,
  inv: BindingHandle<unknown, unknown>,
  metadata: Metadata,
): Promise<void> {
  const reader = response.body?.getReader();
  if (!reader) {
    inv.closeOutput();
    return;
  }
  const limit = resolveDeliveryUnitLimit(args);
  let pending: Uint8Array<ArrayBufferLike> = new Uint8Array();
  let index = 0;
  try {
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      pending = appendBytes(pending, chunk.value);
      if (pending.byteLength > 0 && pending[0] !== 0x1e) {
        failSequential(inv, "JSON text sequence does not begin with RS");
        await reader.cancel().catch(() => {});
        return;
      }
      for (;;) {
        const next = pending.indexOf(0x1e, 1);
        if (next < 0) break;
        let item = trimSequenceTerminator(pending.slice(1, next));
        pending = pending.slice(next);
        if (!await emitJSONItem(item, index, "JSON text sequence", limit, args, inv, metadata, true)) return;
        index += 1;
      }
      if (pending.byteLength > limit + 3) {
        failSequential(inv, `sequential response item exceeds ${limit} byte limit`);
        await reader.cancel().catch(() => {});
        return;
      }
    }
    if (pending.byteLength > 0) {
      if (pending[0] !== 0x1e) {
        failSequential(inv, "JSON text sequence does not begin with RS");
        return;
      }
      const item = trimSequenceTerminator(pending.slice(1));
      if (!await emitJSONItem(item, index, "JSON text sequence", limit, args, inv, metadata, true)) return;
    }
    inv.closeOutput();
  } catch (error: unknown) {
    if (!inv.signal.aborted) failSequential(inv, errorMessage(error));
  } finally {
    reader.releaseLock();
  }
}

async function emitJSONItem(
  item: Uint8Array,
  index: number,
  framing: string,
  limit: number,
  args: BindingInvocationArgs,
  inv: BindingHandle<unknown, unknown>,
  metadata: Metadata,
  whitespaceEmpty = false,
): Promise<boolean> {
  if (item.byteLength > limit) {
    failSequential(inv, `sequential response item exceeds ${limit} byte limit`);
    return false;
  }
  let text: string;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(item);
  } catch (error: unknown) {
    failSequential(inv, `${framing} item ${index} is malformed JSON: ${errorMessage(error)}`);
    return false;
  }
  if (item.byteLength === 0 || (whitespaceEmpty && text.trim() === "")) {
    failSequential(inv, `${framing} item ${index} is empty`);
    return false;
  }
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (error: unknown) {
    failSequential(inv, `${framing} item ${index} is malformed JSON: ${errorMessage(error)}`);
    return false;
  }
  // The response-JSON strictness pin reaches every item: an unpaired
  // surrogate yields no value, so the item is malformed rather than emitted.
  if (jsonCarriesLoneSurrogate(text, value)) {
    failSequential(inv, `${framing} item ${index} carries an unpaired surrogate`);
    return false;
  }
  return emitSequentialValue(value, args, inv, metadata);
}

async function streamMultipart(
  response: Response,
  args: BindingInvocationArgs,
  inv: BindingHandle<unknown, unknown>,
  metadata: Metadata,
  media: OpenAPIMediaType,
): Promise<void> {
  let boundary: string;
  try {
    boundary = parseMediaType(response.headers.get("Content-Type") ?? "", true).params.boundary ?? "";
  } catch {
    boundary = "";
  }
  if (boundary === "") {
    failSequential(inv, "positional multipart response has no valid boundary");
    return;
  }
  const reader = response.body?.getReader();
  if (!reader) {
    inv.closeOutput();
    return;
  }
  const stream = new MultipartByteReader(reader);
  const marker = asciiBytes(`--${boundary}`);
  const between = asciiBytes(`\r\n--${boundary}`);
  const limit = resolveDeliveryUnitLimit(args);
  try {
    await stream.readUntil(marker, 64 * 1024);
    if (await readBoundaryEnding(stream) === "close") {
      inv.closeOutput();
      return;
    }
    for (let index = 0; ; index += 1) {
      const headerBytes = await stream.readUntil(asciiBytes("\r\n\r\n"), 64 * 1024);
      const headers = parsePartHeaders(asciiText(headerBytes));
      let body = await stream.readUntil(between, limit);
      body = decodeTransferEncoding(body, headers.get("content-transfer-encoding") ?? "");
      if (body.byteLength > limit) {
        throw new Error(`sequential response item exceeds ${limit} byte limit`);
      }
      const schema = positionalItemSchema(media, index);
      const contentType = headers.get("content-type") ?? defaultPartContentType(schema);
      const value = decodeSequentialPart(contentType, body, schema);
      if (!await emitSequentialValue(value, args, inv, metadata)) return;

      if (await readBoundaryEnding(stream, index) === "close") {
        inv.closeOutput();
        return;
      }
    }
  } catch (error: unknown) {
    if (!inv.signal.aborted) failSequential(inv, `positional multipart response: ${errorMessage(error)}`);
    await reader.cancel().catch(() => {});
  } finally {
    reader.releaseLock();
  }
}


// readBoundaryEnding consumes what follows a boundary delimiter: "--" closes
// the stream, and otherwise optional linear whitespace precedes the CRLF.
// RFC 2046 SS5.1.1 permits transport padding after the delimiter line, so the
// comparison takes the boundary at the beginning of the line and does not
// require an exact match of the entire candidate line
// (openbindings.openapi-3.2@1 SS9.5). The Go twin inherits this from
// mime/multipart.
async function readBoundaryEnding(stream: MultipartByteReader, index?: number): Promise<"close" | "part"> {
  const where = index === undefined ? "initial multipart boundary" : `multipart boundary after item ${index}`;
  let byte = asciiText(await stream.readExact(1));
  if (byte === "-") {
    if (asciiText(await stream.readExact(1)) !== "-") throw new Error(`invalid ${where}`);
    return "close";
  }
  while (byte === " " || byte === "\t") byte = asciiText(await stream.readExact(1));
  if (byte === "\r" && asciiText(await stream.readExact(1)) === "\n") return "part";
  throw new Error(`invalid ${where}`);
}

class MultipartByteReader {
  private pending: Uint8Array<ArrayBufferLike> = new Uint8Array();

  constructor(private readonly reader: ReadableStreamDefaultReader<Uint8Array>) {}

  async readExact(length: number): Promise<Uint8Array> {
    while (this.pending.byteLength < length) await this.readMore();
    const result = this.pending.slice(0, length);
    this.pending = this.pending.slice(length);
    return result;
  }

  async readUntil(delimiter: Uint8Array, limit: number): Promise<Uint8Array> {
    const chunks: Uint8Array[] = [];
    let length = 0;
    for (;;) {
      const found = indexOfBytes(this.pending, delimiter);
      if (found >= 0) {
        length += found;
        if (length > limit) throw new Error(`sequential response item exceeds ${limit} byte limit`);
        chunks.push(this.pending.slice(0, found));
        this.pending = this.pending.slice(found + delimiter.byteLength);
        return concatBytes(chunks, length);
      }
      const retain = Math.min(this.pending.byteLength, delimiter.byteLength - 1);
      const release = this.pending.byteLength - retain;
      if (release > 0) {
        length += release;
        if (length > limit) throw new Error(`sequential response item exceeds ${limit} byte limit`);
        chunks.push(this.pending.slice(0, release));
        this.pending = this.pending.slice(release);
      }
      await this.readMore();
    }
  }

  private async readMore(): Promise<void> {
    const chunk = await this.reader.read();
    if (chunk.done) throw new Error("unexpected end of multipart response");
    this.pending = appendBytes(this.pending, chunk.value);
  }
}

function parsePartHeaders(text: string): Headers {
  const headers = new Headers();
  for (const line of text.split("\r\n")) {
    const colon = line.indexOf(":");
    if (colon <= 0) throw new Error("invalid multipart part header");
    headers.append(line.slice(0, colon).trim(), line.slice(colon + 1).trim());
  }
  return headers;
}

function positionalItemSchema(media: OpenAPIMediaType, index: number): SchemaDeclaration {
  if (Object.hasOwn(media, "itemSchema")) return media.itemSchema as SchemaDeclaration;
  const root = media.schema;
  if (!root || typeof root !== "object" || Array.isArray(root)) return undefined;
  if (Array.isArray(root.prefixItems) && index < root.prefixItems.length) {
    return root.prefixItems[index] as SchemaDeclaration;
  }
  return root.items as SchemaDeclaration;
}

function defaultPartContentType(schema: SchemaDeclaration): string {
  const declaration = resolveDeclaration(schema, false);
  const explicit = declaration.keywordString("contentMediaType");
  if (!explicit.conflict && explicit.value !== "") return explicit.value;
  if (declaration.admitsStringAsSoleNonNullType()) return "text/plain; charset=utf-8";
  if (declaration.declaresOnly("object", "array", "boolean", "number", "integer", "null")) {
    return "application/json";
  }
  return "application/octet-stream";
}

function decodeSequentialPart(
  contentType: string,
  body: Uint8Array,
  schema: SchemaDeclaration,
): unknown {
  const parsed = parseMediaType(contentType, true);
  if (isJSONMediaType(parsed.base)) {
    let text: string;
    let value: unknown;
    try {
      text = new TextDecoder("utf-8", { fatal: true }).decode(body);
      value = JSON.parse(text);
    } catch (error: unknown) {
      throw new Error(`part declares ${JSON.stringify(contentType)} but is not valid JSON: ${errorMessage(error)}`);
    }
    if (jsonCarriesLoneSurrogate(text, value)) {
      throw new Error(`part declares ${JSON.stringify(contentType)} but carries an unpaired surrogate`);
    }
    return value;
  }
  if (parsed.base.startsWith("text/") || parsed.base === "application/xml" || parsed.base.endsWith("+xml")) {
    return decodePartText(body, parsed.params.charset ?? "utf-8");
  }
  if (resolveDeclaration(schema, false).typeless()) return bytesToBase64(body);
  throw new Error(`part media ${JSON.stringify(contentType)} and its declaration select no incorporated carriage lane`);
}

function decodePartText(body: Uint8Array, charset: string): string {
  switch (charset.toLowerCase()) {
    case "utf-8":
    case "utf8":
      return new TextDecoder("utf-8", { fatal: true }).decode(body);
    case "us-ascii":
    case "ascii":
      if (body.some((byte) => byte >= 0x80)) throw new Error("part is not valid US-ASCII");
      return String.fromCodePoint(...body);
    case "iso-8859-1":
    case "iso8859-1":
    case "latin-1":
    case "latin1":
      return String.fromCodePoint(...body);
    default:
      throw new Error(`unsupported part charset ${JSON.stringify(charset)}`);
  }
}

function decodeTransferEncoding(body: Uint8Array, raw: string): Uint8Array {
  switch (raw.trim().toLowerCase()) {
    case "":
    case "binary":
    case "7bit":
    case "8bit":
      return body;
    case "base64": {
      const text = asciiText(body).replace(/[\t\n\r ]/gu, "");
      if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(text)) {
        throw new Error("invalid base64 Content-Transfer-Encoding");
      }
      return Uint8Array.from(atob(text), (value) => value.charCodeAt(0));
    }
    case "quoted-printable":
      return decodeQuotedPrintable(body);
    default:
      throw new Error(`unsupported Content-Transfer-Encoding ${JSON.stringify(raw)}`);
  }
}

function decodeQuotedPrintable(body: Uint8Array): Uint8Array {
  const text = asciiText(body).replace(/=\r?\n/gu, "");
  const output: number[] = [];
  for (let index = 0; index < text.length; index += 1) {
    if (text[index] !== "=") {
      output.push(text.charCodeAt(index));
      continue;
    }
    const hex = text.slice(index + 1, index + 3);
    if (!/^[0-9A-Fa-f]{2}$/u.test(hex)) throw new Error("invalid quoted-printable Content-Transfer-Encoding");
    output.push(Number.parseInt(hex, 16));
    index += 2;
  }
  return Uint8Array.from(output);
}

async function emitSequentialValue(
  value: unknown,
  args: BindingInvocationArgs,
  inv: BindingHandle<unknown, unknown>,
  metadata: Metadata,
): Promise<boolean> {
  try {
    args.observeOutput?.(value, metadata);
    await inv.emitOutput(value);
    return true;
  } catch {
    return false;
  }
}

function failSequential(inv: BindingHandle<unknown, unknown>, message: string): void {
  inv.fireError(new InvocationError(ERR_RESPONSE_ERROR, message));
}

function trimSequenceTerminator(item: Uint8Array): Uint8Array {
  if (item.at(-1) === 0x0a) item = item.slice(0, -1);
  if (item.at(-1) === 0x0d) item = item.slice(0, -1);
  return item;
}

function appendBytes(left: Uint8Array, right: Uint8Array): Uint8Array {
  if (left.byteLength === 0) return right.slice();
  const result = new Uint8Array(left.byteLength + right.byteLength);
  result.set(left);
  result.set(right, left.byteLength);
  return result;
}

function concatBytes(chunks: Uint8Array[], length: number): Uint8Array {
  const result = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    result.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return result;
}

function indexOfBytes(haystack: Uint8Array, needle: Uint8Array): number {
  outer: for (let index = 0; index <= haystack.byteLength - needle.byteLength; index += 1) {
    for (let child = 0; child < needle.byteLength; child += 1) {
      if (haystack[index + child] !== needle[child]) continue outer;
    }
    return index;
  }
  return -1;
}

function asciiBytes(text: string): Uint8Array {
  return new TextEncoder().encode(text);
}

function asciiText(bytes: Uint8Array): string {
  return new TextDecoder("latin1").decode(bytes);
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}
