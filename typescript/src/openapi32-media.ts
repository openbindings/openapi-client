import type { OpenAPIMediaType } from "./types.js";
import { resolveDeclaration, type SchemaDeclaration } from "./resolved-declaration.js";

export type OpenAPI32SequentialRequestKind = "json-lines" | "json-seq";

export interface OpenAPI32RequestMediaAdmission {
  handled: boolean;
  family?: "text" | "multipart" | "sequential";
  sequentialKind?: OpenAPI32SequentialRequestKind;
  positionalMultipart?: boolean;
  error?: string;
}

export interface OpenAPI32RoutedBody {
  bodyFields: Record<string, unknown>;
  bodyValue: unknown;
  bodySet: boolean;
}

export interface OpenAPI32MultipartWire {
  body: Uint8Array<ArrayBuffer>;
  contentType: string;
}

/** Classifies the 3.2-only request-media lanes and their confined defects. */
export function openAPI32RequestMediaAdmission(
  base: string,
  media: OpenAPIMediaType | null,
): OpenAPI32RequestMediaAdmission {
  const itemSchema = media !== null && Object.hasOwn(media, "itemSchema");
  if (base === "text/event-stream") {
    return { handled: true, error: "text/event-stream has no incorporated request write algorithm" };
  }
  if (base === "application/jsonl" || base === "application/x-ndjson") {
    return { handled: true, family: "sequential", sequentialKind: "json-lines" };
  }
  if (base === "application/json-seq" || base.endsWith("+json-seq")) {
    return { handled: true, family: "sequential", sequentialKind: "json-seq" };
  }
  if (base.startsWith("multipart/")) {
    const positional = openAPI32PositionalMultipart(media);
    const error = validateMultipartDeclaration(media, base, positional);
    return {
      handled: true,
      family: "multipart",
      positionalMultipart: positional,
      ...(error ? { error } : {}),
    };
  }
  if (itemSchema) {
    return {
      handled: true,
      error: `media type ${JSON.stringify(base)} declares itemSchema but has no incorporated sequential request framing`,
    };
  }
  const schema = mediaSchema(media);
  if (isCharacterDataMedia(base) && openAPI32NonJSONTextSchema(schema)) {
    return { handled: true, family: "text" };
  }
  return { handled: false };
}

/** The 3.2 non-JSON text lane admits only a closed scalar type union. */
export function openAPI32NonJSONTextSchema(schema: SchemaDeclaration): boolean {
  const resolved = resolveDeclaration(schema, false);
  if (resolved.ambiguous || resolved.types === null) return false;
  let nonNull = false;
  for (const member of resolved.types) {
    if (member === "null") continue;
    if (!["string", "boolean", "number", "integer"].includes(member)) return false;
    nonNull = true;
  }
  return nonNull;
}

/** Serializes a runtime-selected scalar without JSON string quoting. */
export function serializeOpenAPI32NonJSONText(
  schema: SchemaDeclaration,
  value: unknown,
): string {
  if (value === null) throw new Error("non-JSON text serialization has no null lexical form");
  const resolved = resolveDeclaration(schema, false);
  const kind = openAPI32JSONValueType(value);
  if (!kind || !resolvedTypeAdmits(resolved.types, kind)) {
    throw new Error(`supplied ${kind || typeof value} does not determine one permitted non-JSON serialization type`);
  }
  if (typeof value === "string") return value;
  if (typeof value === "boolean") return value ? "true" : "false";
  if (typeof value === "number" && Number.isFinite(value)) {
    return normalizeOpenAPI32JSONNumber(JSON.stringify(value));
  }
  throw new Error(`JSON ${kind} has no non-JSON text serialization`);
}

/** Chooses the shortest exact RFC 8259 spelling, preferring plain form on ties. */
export function normalizeOpenAPI32JSONNumber(input: string): string {
  if (!/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$/u.test(input)) {
    throw new Error(`${JSON.stringify(input)} is not an RFC 8259 number`);
  }
  let raw = input;
  const negative = raw.startsWith("-");
  if (negative) raw = raw.slice(1);
  const exponentAt = raw.search(/[eE]/u);
  const mantissa = exponentAt < 0 ? raw : raw.slice(0, exponentAt);
  const exponent = exponentAt < 0 ? 0n : BigInt(raw.slice(exponentAt + 1));
  const dotAt = mantissa.indexOf(".");
  const integer = dotAt < 0 ? mantissa : mantissa.slice(0, dotAt);
  const fraction = dotAt < 0 ? "" : mantissa.slice(dotAt + 1);
  let digits = (integer + fraction).replace(/^0+/u, "");
  if (digits === "") return "0";
  let scale = BigInt(fraction.length) - exponent;
  while (digits.endsWith("0")) {
    digits = digits.slice(0, -1);
    scale -= 1n;
  }
  const prefix = negative ? "-" : "";
  const exponentialExponent = BigInt(digits.length - 1) - scale;
  const exponentialMantissa = digits.length === 1 ? digits : `${digits[0]}.${digits.slice(1)}`;
  const exponential = `${prefix}${exponentialMantissa}e${exponentialExponent}`;

  let plainLength: bigint;
  if (scale <= 0n) plainLength = BigInt(prefix.length + digits.length) - scale;
  else if (scale >= BigInt(digits.length)) plainLength = BigInt(prefix.length + 2) + scale;
  else plainLength = BigInt(prefix.length + digits.length + 1);
  if (plainLength > BigInt(exponential.length)) return exponential;
  const materializedScale = Number(scale);
  if (materializedScale <= 0) return `${prefix}${digits}${"0".repeat(-materializedScale)}`;
  if (materializedScale >= digits.length) {
    return `${prefix}0.${"0".repeat(materializedScale - digits.length)}${digits}`;
  }
  return `${prefix}${digits.slice(0, digits.length - materializedScale)}.${digits.slice(digits.length - materializedScale)}`;
}

/** Frames one caller array as JSON Lines/NDJSON or RFC 7464 JSON text sequences. */
export function buildOpenAPI32SequentialBody(
  kind: OpenAPI32SequentialRequestKind,
  value: unknown,
): string {
  if (!Array.isArray(value)) {
    throw new Error(`sequential request body must be an array, got ${value === null ? "null" : typeof value}`);
  }
  return value.map((item) => {
    const json = stringifyOpenAPI32JSON(item);
    return kind === "json-seq" ? `\u001e${json}\n` : `${json}\n`;
  }).join("");
}

export function openAPI32PositionalMultipart(media: OpenAPIMediaType | null): boolean {
  if (!media) return false;
  if (Object.hasOwn(media, "prefixEncoding") || Object.hasOwn(media, "itemEncoding") || Object.hasOwn(media, "itemSchema")) {
    return true;
  }
  return resolveDeclaration(media.schema as SchemaDeclaration, false).declaresOnly("array");
}

export function openAPI32MultipartNeedsNativeBuilder(media: OpenAPIMediaType | null): boolean {
  if (openAPI32PositionalMultipart(media)) return true;
  const encoding = asRecord(media?.encoding);
  return Object.values(encoding ?? {}).some((value) => {
    const entry = asRecord(value);
    return activeNestedEncoding(entry) || Object.hasOwn(entry ?? {}, "headers");
  });
}

/** Validates field-local Content-Transfer-Encoding contradictions on use. */
export function validateOpenAPI32MultipartFields(
  media: OpenAPIMediaType | null,
  fields: Record<string, unknown>,
): void {
  const root = resolveDeclaration(media?.schema as SchemaDeclaration, false);
  const encoding = asRecord(media?.encoding);
  for (const name of Object.keys(fields)) {
    const contentEncoding = root.property(name).keywordString("contentEncoding");
    if (contentEncoding.conflict || contentEncoding.value === "") continue;
    const enc = asRecord(encoding?.[name]);
    const header = headerDeclaration(enc, "Content-Transfer-Encoding");
    if (!header || header.admits(contentEncoding.value)) continue;
    throw new Error(
      `multipart part ${JSON.stringify(name)} Content-Transfer-Encoding disallows contentEncoding ${JSON.stringify(contentEncoding.value)}`,
    );
  }
}

/** Builds positional and one-level nested multipart requests without flattening. */
export function buildOpenAPI32MultipartBody(
  mediaType: string,
  media: OpenAPIMediaType | null,
  routed: OpenAPI32RoutedBody,
): OpenAPI32MultipartWire {
  if (!media) throw new Error("OpenAPI 3.2 multipart plan has no Media Type Object");
  const base = mediaBase(mediaType);
  const formData = base === "multipart/form-data";
  const boundary = mediaParameter(mediaType, "boundary") ?? generatedBoundary();
  const parts: Uint8Array[] = [];
  if (openAPI32PositionalMultipart(media)) {
    if (!routed.bodySet || !Array.isArray(routed.bodyValue)) {
      throw new Error("positional multipart request requires one array body");
    }
    const prefix = Array.isArray(media.prefixEncoding) ? media.prefixEncoding : [];
    const itemEncoding = asRecord(media.itemEncoding);
    const itemSchema = media.itemSchema as SchemaDeclaration
      ?? resolveDeclaration(media.schema as SchemaDeclaration, false).items();
    routed.bodyValue.forEach((value, index) => {
      const encoding = asRecord(prefix[index]) ?? itemEncoding;
      const disposition = formData ? positionalDisposition(encoding) : undefined;
      parts.push(multipartPart(
        boundary,
        disposition,
        value,
        itemSchema,
        encoding,
        formData,
        0,
      ));
    });
  } else {
    const root = resolveDeclaration(media.schema as SchemaDeclaration, false);
    const encoding = asRecord(media.encoding);
    for (const name of Object.keys(routed.bodyFields).sort()) {
      parts.push(multipartPart(
        boundary,
        `form-data; name=${JSON.stringify(name)}`,
        routed.bodyFields[name],
        root.property(name),
        asRecord(encoding?.[name]),
        formData,
        0,
      ));
    }
  }
  parts.push(ascii(`--${boundary}--\r\n`));
  return {
    body: concatBytes(parts),
    contentType: `${base}; boundary=${boundary}`,
  };
}

function multipartPart(
  boundary: string,
  disposition: string | undefined,
  value: unknown,
  schema: SchemaDeclaration | ReturnType<typeof resolveDeclaration>,
  encoding: Record<string, unknown> | null,
  formData: boolean,
  depth: number,
): Uint8Array<ArrayBuffer> {
  const declaration = isResolvedDeclaration(schema) ? schema : resolveDeclaration(schema, false);
  let contentType = typeof encoding?.contentType === "string" && encoding.contentType !== ""
    ? firstContentType(encoding.contentType)
    : defaultPartContentType(declaration, value);
  let body: Uint8Array<ArrayBuffer>;
  if (activeNestedEncoding(encoding) && mediaBase(contentType).startsWith("multipart/")) {
    if (depth >= 1) throw new Error("more than one nested Encoding level is not supported");
    const nestedBoundary = mediaParameter(contentType, "boundary") ?? generatedBoundary();
    const nestedParts: Uint8Array[] = [];
    const prefix = Array.isArray(encoding?.prefixEncoding) ? encoding.prefixEncoding : [];
    const itemEncoding = asRecord(encoding?.itemEncoding);
    if (Object.hasOwn(encoding ?? {}, "encoding")) {
      const object = asRecord(value);
      if (!object) throw new Error("nested name-based multipart value must be an object");
      const children = asRecord(encoding?.encoding);
      for (const name of Object.keys(object).sort()) {
        nestedParts.push(multipartPart(
          nestedBoundary,
          `form-data; name=${JSON.stringify(name)}`,
          object[name],
          declaration.property(name),
          asRecord(children?.[name]),
          mediaBase(contentType) === "multipart/form-data",
          depth + 1,
        ));
      }
    } else {
      if (!Array.isArray(value)) throw new Error("nested positional multipart value must be an array");
      value.forEach((item, index) => {
        const child = asRecord(prefix[index]) ?? itemEncoding;
        const childDisposition = mediaBase(contentType) === "multipart/form-data"
          ? positionalDisposition(child)
          : undefined;
        nestedParts.push(multipartPart(
          nestedBoundary,
          childDisposition,
          item,
          declaration.items(),
          child,
          mediaBase(contentType) === "multipart/form-data",
          depth + 1,
        ));
      });
    }
    nestedParts.push(ascii(`--${nestedBoundary}--\r\n`));
    body = concatBytes(nestedParts);
    contentType = `${mediaBase(contentType)}; boundary=${nestedBoundary}`;
  } else {
    body = partBody(declaration, value, contentType);
  }

  const headers: string[] = [];
  if (disposition !== undefined) headers.push(`Content-Disposition: ${disposition}`);
  else if (formData) headers.push("Content-Disposition: form-data; name=\"\"");
  headers.push(`Content-Type: ${contentType}`);
  const contentEncoding = declaration.keywordString("contentEncoding");
  if (!contentEncoding.conflict && contentEncoding.value !== "" && declaration.admitsStringAsSoleNonNullType()) {
    const transfer = headerDeclaration(encoding, "Content-Transfer-Encoding");
    if (transfer && !transfer.admits(contentEncoding.value)) {
      throw new Error(`explicit Content-Transfer-Encoding Header disallows contentEncoding ${JSON.stringify(contentEncoding.value)}`);
    }
    // R5 (2026-09-01): the edition's equivalence describes what the
    // declaration MEANS, not a field a serializer adds, and RFC 7578 §4.7 says
    // senders SHOULD NOT generate the field. No emission; the declared
    // equivalence still governs the conflict check above and parsing. Matches
    // the 3.0/3.1 lanes.
  }
  for (const [name, value] of fixedHeaders(encoding)) {
    if (name.toLowerCase() === "content-type" || name.toLowerCase() === "content-transfer-encoding") continue;
    if (name.toLowerCase() === "content-disposition" && disposition !== undefined) continue;
    headers.push(`${name}: ${value}`);
  }
  return concatBytes([
    ascii(`--${boundary}\r\n${headers.join("\r\n")}\r\n\r\n`),
    body,
    ascii("\r\n"),
  ]);
}

function partBody(
  declaration: ReturnType<typeof resolveDeclaration>,
  value: unknown,
  contentType: string,
): Uint8Array<ArrayBuffer> {
  const encoding = declaration.keywordString("contentEncoding");
  if (!encoding.conflict && encoding.value !== "") {
    if (typeof value !== "string") throw new Error("artifact-encoded multipart part requires a string");
    return new TextEncoder().encode(value);
  }
  if (declaration.typeless()) return canonicalBase64Bytes(value, "multipart part");
  if (mediaBase(contentType) === "application/json" || mediaBase(contentType).endsWith("+json")) {
    return new TextEncoder().encode(stringifyOpenAPI32JSON(value));
  }
  return new TextEncoder().encode(serializeOpenAPI32NonJSONText(declarationAsSchema(declaration), value));
}

function validateMultipartDeclaration(
  media: OpenAPIMediaType | null,
  base: string,
  positional: boolean,
): string | undefined {
  if (!media) return "multipart Media Type Object is absent";
  const nameBased = Object.hasOwn(media, "encoding");
  const positionalFields = Object.hasOwn(media, "prefixEncoding") || Object.hasOwn(media, "itemEncoding");
  if (nameBased && positionalFields) return "name-based encoding is mutually exclusive with prefixEncoding and itemEncoding";
  if (positionalFields && !positional) return "positional multipart encoding requires itemSchema or an array schema";
  const top = [
    ...Object.values(asRecord(media.encoding) ?? {}),
    ...(Array.isArray(media.prefixEncoding) ? media.prefixEncoding : []),
    ...(Object.hasOwn(media, "itemEncoding") ? [media.itemEncoding] : []),
  ];
  for (const raw of top) {
    const error = validateEncodingDepth(asRecord(raw), 0);
    if (error) return error;
  }
  if (positional && base === "multipart/form-data") {
    const prefix = Array.isArray(media.prefixEncoding) ? media.prefixEncoding : [];
    for (const raw of prefix) {
      try { positionalDisposition(asRecord(raw)); } catch (error: unknown) {
        return error instanceof Error ? error.message : String(error);
      }
    }
    if (Object.hasOwn(media, "itemEncoding")) {
      try { positionalDisposition(asRecord(media.itemEncoding)); } catch (error: unknown) {
        return error instanceof Error ? error.message : String(error);
      }
    } else if (prefix.length === 0) {
      return "positional multipart/form-data requires an artifact-fixed Content-Disposition header";
    }
  }
  return undefined;
}

function validateEncodingDepth(encoding: Record<string, unknown> | null, depth: number): string | undefined {
  if (!activeNestedEncoding(encoding)) return undefined;
  if (depth >= 1) return "more than one nested Encoding level is not supported";
  if (Object.hasOwn(encoding ?? {}, "encoding")
    && (Object.hasOwn(encoding ?? {}, "prefixEncoding") || Object.hasOwn(encoding ?? {}, "itemEncoding"))) {
    return "name-based encoding is mutually exclusive with prefixEncoding and itemEncoding";
  }
  const children = [
    ...Object.values(asRecord(encoding?.encoding) ?? {}),
    ...(Array.isArray(encoding?.prefixEncoding) ? encoding.prefixEncoding : []),
    ...(Object.hasOwn(encoding ?? {}, "itemEncoding") ? [encoding?.itemEncoding] : []),
  ];
  for (const raw of children) {
    const error = validateEncodingDepth(asRecord(raw), depth + 1);
    if (error) return error;
  }
  return undefined;
}

function activeNestedEncoding(encoding: Record<string, unknown> | null): boolean {
  if (!encoding || typeof encoding.contentType !== "string") return false;
  const multipart = encoding.contentType.split(",").some((member) => {
    const base = mediaBase(member.trim());
    return base === "*/*" || base.startsWith("multipart/");
  });
  return multipart && ["encoding", "prefixEncoding", "itemEncoding"].some((field) => Object.hasOwn(encoding, field));
}

function positionalDisposition(encoding: Record<string, unknown> | null): string {
  const header = headerDeclaration(encoding, "Content-Disposition");
  const fixed = header?.fixed;
  if (!fixed || !/^form-data\s*;/iu.test(fixed) || !/(?:^|;)\s*name=(?:"[^"]+"|[^;\s]+)/iu.test(fixed)) {
    throw new Error("positional multipart/form-data requires an artifact-fixed Content-Disposition header");
  }
  return fixed;
}

function headerDeclaration(
  encoding: Record<string, unknown> | null,
  wanted: string,
): { fixed?: string; admits(value: string): boolean } | null {
  const headers = asRecord(encoding?.headers);
  const key = Object.keys(headers ?? {}).find((name) => name.toLowerCase() === wanted.toLowerCase());
  if (!key) return null;
  const header = asRecord(headers?.[key]);
  const schema = asRecord(header?.schema)
    ?? asRecord(Object.values(asRecord(header?.content) ?? {})[0])?.schema as Record<string, unknown> | null;
  if (!schema) return { admits: () => true };
  if (typeof schema.const === "string") {
    const fixed = schema.const;
    return { fixed, admits: (value) => fixed.toLowerCase() === value.toLowerCase() };
  }
  if (Array.isArray(schema.enum)) {
    const values = schema.enum.filter((value): value is string => typeof value === "string");
    return {
      ...(values.length === 1 ? { fixed: values[0] } : {}),
      admits: (value) => values.some((candidate) => candidate.toLowerCase() === value.toLowerCase()),
    };
  }
  return { admits: () => true };
}

function fixedHeaders(encoding: Record<string, unknown> | null): Array<[string, string]> {
  const headers = asRecord(encoding?.headers);
  const result: Array<[string, string]> = [];
  for (const name of Object.keys(headers ?? {}).sort()) {
    const declaration = headerDeclaration(encoding, name);
    if (declaration?.fixed) result.push([name, declaration.fixed]);
  }
  return result;
}

function defaultPartContentType(
  declaration: ReturnType<typeof resolveDeclaration>,
  value: unknown,
): string {
  const declared = declaration.keywordString("contentMediaType");
  if (!declared.conflict && declared.value !== "") return declared.value;
  const encoding = declaration.keywordString("contentEncoding");
  if (!encoding.conflict && encoding.value !== "") return "application/octet-stream";
  if (declaration.typeless()) return "application/octet-stream";
  // Section 9.3 of openbindings.openapi-3.2@1: the default determination is
  // declaration-keyed, not value-keyed, and a multi-type resolved set
  // determines no default. The planner reports such a part as a propertyMedia
  // requirement before any value is consumed, so this is reached only by a
  // path that bypassed that requirement, and it refuses rather than reading a
  // Content-Type off the supplied value's JSON type.
  if (declaration.determinesNoDefault()) {
    throw new Error("multipart part declares a multi-type resolved set, which determines no default Content-Type; configuration.propertyMedia is required");
  }
  const kind = openAPI32JSONValueType(value);
  return kind === "array" || kind === "object" ? "application/json" : "text/plain";
}

function canonicalBase64Bytes(value: unknown, subject: string): Uint8Array<ArrayBuffer> {
  if (typeof value !== "string" || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(value)) {
    throw new Error(`${subject} must be a canonical Base64 string`);
  }
  let binary: string;
  try { binary = atob(value); } catch { throw new Error(`${subject} must be a canonical Base64 string`); }
  if (btoa(binary) !== value) throw new Error(`${subject} must be a canonical Base64 string`);
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

function stringifyOpenAPI32JSON(value: unknown): string {
  const serialized = JSON.stringify(value);
  if (serialized === undefined) throw new Error("value has no JSON representation");
  return serialized.replace(/[<>&\u2028\u2029]/gu, (character) =>
    `\\u${character.codePointAt(0)!.toString(16).padStart(4, "0")}`);
}

function openAPI32JSONValueType(value: unknown): string {
  if (typeof value === "string") return "string";
  if (typeof value === "boolean") return "boolean";
  if (typeof value === "number" && Number.isFinite(value)) return Number.isInteger(value) ? "integer" : "number";
  if (Array.isArray(value)) return "array";
  if (asRecord(value)) return "object";
  return "";
}

function resolvedTypeAdmits(types: ReadonlySet<string> | null, kind: string): boolean {
  if (types === null) return false;
  if (types.has(kind)) return true;
  return (kind === "integer" || kind === "number") && (types.has("integer") || types.has("number"));
}

function isCharacterDataMedia(base: string): boolean {
  return base.startsWith("text/") || base === "application/xml" || base.endsWith("+xml");
}

function mediaSchema(media: OpenAPIMediaType | null): SchemaDeclaration {
  return media && Object.hasOwn(media, "schema") ? media.schema as SchemaDeclaration : null;
}

function mediaBase(mediaType: string): string {
  return mediaType.split(";", 1)[0]!.trim().toLowerCase();
}

function mediaParameter(mediaType: string, name: string): string | undefined {
  for (const part of mediaType.split(";").slice(1)) {
    const equals = part.indexOf("=");
    if (equals < 0 || part.slice(0, equals).trim().toLowerCase() !== name) continue;
    return part.slice(equals + 1).trim().replace(/^"|"$/gu, "");
  }
  return undefined;
}

function firstContentType(value: string): string {
  return value.split(",", 1)[0]!.trim();
}

function generatedBoundary(): string {
  return `----openbindings-${Math.random().toString(16).slice(2)}`;
}

function ascii(value: string): Uint8Array<ArrayBuffer> {
  return new TextEncoder().encode(value);
}

function concatBytes(chunks: readonly Uint8Array[]): Uint8Array<ArrayBuffer> {
  const result = new Uint8Array(chunks.reduce((total, chunk) => total + chunk.byteLength, 0));
  let offset = 0;
  for (const chunk of chunks) {
    result.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return result;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function isResolvedDeclaration(value: unknown): value is ReturnType<typeof resolveDeclaration> {
  return value !== null && typeof value === "object" && "declaresOnly" in value;
}

// ResolvedDeclaration is intentionally opaque; its methods retain the schema
// semantics needed by the scalar serializer, so this adapter exposes them as
// the minimal SchemaDeclaration-shaped contract that serializer accepts.
function declarationAsSchema(declaration: ReturnType<typeof resolveDeclaration>): SchemaDeclaration {
  return { type: [...(declaration.types ?? [])] };
}
