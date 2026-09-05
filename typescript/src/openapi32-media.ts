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
  openapiVersion = "3.2.0",
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
      ?? resolveDeclaration(media.schema as SchemaDeclaration, openapiVersion.startsWith("3.0.")).items();
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
        openapiVersion,
      ));
    });
  } else {
    const root = resolveDeclaration(media.schema as SchemaDeclaration, openapiVersion.startsWith("3.0."));
    const encoding = asRecord(media.encoding);
    for (const name of Object.keys(routed.bodyFields).sort()) {
      const property = root.property(name);
      const value = routed.bodyFields[name];
      if (value === null && property.admitsNull() && !root.requiresProperty(name)) {
        continue;
      }
      const propertyEncoding = asRecord(encoding?.[name]);
      if (formData) validateFixedFormDisposition(name, propertyEncoding);
      if (property.declaresOnly("array") && !Array.isArray(value)) {
        throw new Error(`multipart property ${JSON.stringify(name)} requires an array value`);
      }
      const nested = activeNestedEncoding(propertyEncoding);
      const values = property.declaresOnly("array") && Array.isArray(value) && !nested ? value : [value];
      const declaration = property.declaresOnly("array") && !nested ? property.items() : property;
      for (const member of values) {
        parts.push(multipartPart(
          boundary,
          generatedFormDisposition(name),
          member,
          declaration,
          propertyEncoding,
          formData,
          0,
          openapiVersion,
        ));
      }
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
  openapiVersion: string,
): Uint8Array<ArrayBuffer> {
  const headerGroups = encodingHeaderGroups(encoding, openapiVersion);
  const declaration = isResolvedDeclaration(schema)
    ? schema
    : resolveDeclaration(schema, openapiVersion.startsWith("3.0."));
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
          generatedFormDisposition(name),
          object[name],
          declaration.property(name),
          asRecord(children?.[name]),
          mediaBase(contentType) === "multipart/form-data",
          depth + 1,
          openapiVersion,
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
          openapiVersion,
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
  const declaredDisposition = headerGroups.get("content-disposition")?.fixed;
  if (declaredDisposition !== undefined) disposition = declaredDisposition;
  if (disposition !== undefined) headers.push(`Content-Disposition: ${disposition}`);
  else if (formData) headers.push("Content-Disposition: form-data; name=\"\"");
  headers.push(`Content-Type: ${contentType}`);
  const contentEncoding = declaration.keywordString("contentEncoding");
  if (!contentEncoding.conflict && contentEncoding.value !== "" && declaration.admitsStringAsSoleNonNullType()) {
    const transfer = headerGroups.get("content-transfer-encoding");
    if (transfer && !transfer.admits(contentEncoding.value)) {
      throw new Error(`explicit Content-Transfer-Encoding Header disallows contentEncoding ${JSON.stringify(contentEncoding.value)}`);
    }
    // R5 (2026-09-01): the edition's equivalence describes what the
    // declaration MEANS, not a field a serializer adds, and RFC 7578 §4.7 says
    // senders SHOULD NOT generate the field. No emission; the declared
    // equivalence still governs the conflict check above and parsing. Matches
    // the 3.0/3.1 lanes.
  }
  for (const { name, fixed: value } of headerGroups.values()) {
    if (value === undefined) continue;
    if (name.toLowerCase() === "content-type") continue;
    if (name.toLowerCase() === "content-disposition" && disposition !== undefined) continue;
    headers.push(`${name}: ${value}`);
  }
  return concatBytes([
    ascii(`--${boundary}\r\n${headers.join("\r\n")}\r\n\r\n`),
    body,
    ascii("\r\n"),
  ]);
}

function generatedFormDisposition(name: string): string {
  for (const character of name) {
    const code = character.codePointAt(0)!;
    if (code === 0 || code === 0x7f || code < 0x20) {
      throw new Error(`multipart part name ${JSON.stringify(name)} contains a forbidden control character`);
    }
  }
  return `form-data; name="${name.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}

function validateFixedFormDisposition(name: string, encoding: Record<string, unknown> | null): void {
  const fixed = headerDeclaration(encoding, "Content-Disposition")?.fixed;
  if (fixed === undefined) return;
  if (/(?:^|;)\s*filename\*\s*=/iu.test(fixed)) {
    throw new Error(`multipart property ${JSON.stringify(name)} declares forbidden filename* in Content-Disposition`);
  }
  const match = /(?:^|;)\s*name\s*=\s*(?:"((?:\\.|[^"])*)"|([^;\s]+))/iu.exec(fixed);
  const declared = match?.[1] !== undefined
    ? match[1].replace(/\\(["\\])/gu, "$1")
    : match?.[2];
  if (!/^form-data\s*;/iu.test(fixed) || declared !== name) {
    throw new Error(`multipart Content-Disposition does not name property ${JSON.stringify(name)}`);
  }
}

function partBody(
  declaration: ReturnType<typeof resolveDeclaration>,
  value: unknown,
  contentType: string,
): Uint8Array<ArrayBuffer> {
  const encoding = declaration.keywordString("contentEncoding");
  if (!encoding.conflict && encoding.value !== "" && declaration.admitsStringAsSoleNonNullType()) {
    if (typeof value !== "string") throw new Error("artifact-encoded multipart part requires a string");
    return new TextEncoder().encode(value);
  }
  const format = declaration.format();
  if (!format.conflict && format.value === "binary") {
    return canonicalBase64Bytes(value, "multipart binary part");
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

interface EncodingHeaderGroup {
  name: string;
  fixed?: string;
  required: boolean;
  admits(value: string): boolean;
}

/**
 * Resolves one HTTP field per ASCII-case-insensitive Encoding-header group.
 * Exact raw-string domains are intentionally narrow: only schema-form
 * string const/enum constraints, conjoined through allOf, participate.
 */
function encodingHeaderGroups(
  encoding: Record<string, unknown> | null,
  openapiVersion: string,
): Map<string, EncodingHeaderGroup> {
  const grouped = new Map<string, Array<{
    name: string;
    required: boolean;
    domain: ReadonlySet<string> | null;
  }>>();
  const headers = asRecord(encoding?.headers);
  for (const name of Object.keys(headers ?? {}).sort()) {
    if (name.toLowerCase() === "content-type") continue;
    if (!HTTP_FIELD_NAME.test(name)) {
      throw new Error(`Encoding header ${JSON.stringify(name)} is not an HTTP field-name token`);
    }
    const header = asRecord(headers?.[name]);
    if (!header) continue; // invalid projection is confined and treated absent
    const key = name.toLowerCase();
    const members = grouped.get(key) ?? [];
    members.push({
      name,
      required: header.required === true,
      domain: Object.hasOwn(header, "content")
        ? null
        : exactHeaderStringDomain(header.schema, !openapiVersion.startsWith("3.0.")),
    });
    grouped.set(key, members);
  }

  const result = new Map<string, EncodingHeaderGroup>();
  for (const [key, members] of grouped) {
    const fixed = new Set<string>();
    for (const member of members) {
      if (member.domain?.size === 1) fixed.add(member.domain.values().next().value!);
    }
    if (fixed.size > 1) {
      throw new Error(`case-equivalent Encoding headers for ${JSON.stringify(members[0]!.name)} fix conflicting values`);
    }
    const value = fixed.values().next().value as string | undefined;
    if (value !== undefined && members.some((member) => member.domain !== null && !member.domain.has(value))) {
      throw new Error(`case-equivalent Encoding headers for ${JSON.stringify(members[0]!.name)} have no common fixed value`);
    }
    const required = members.some((member) => member.required);
    if (value === undefined && required) {
      throw new Error(`required Encoding header ${JSON.stringify(members[0]!.name)} has no exact artifact-fixed value`);
    }
    if (value !== undefined && !validHTTPFieldValue(value)) {
      throw new Error(`Encoding header ${JSON.stringify(members[0]!.name)} fixes an invalid HTTP field value`);
    }
    result.set(key, {
      name: members[0]!.name,
      ...(value === undefined ? {} : { fixed: value }),
      required,
      admits: (candidate) => members.every((member) => member.domain === null || member.domain.has(candidate)),
    });
  }
  return result;
}

const HTTP_FIELD_NAME = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u;

function validHTTPFieldValue(value: string): boolean {
  if (/^[ \t]|[ \t]$/u.test(value)) return false;
  for (const character of value) {
    const code = character.codePointAt(0)!;
    if (code === 0x7f || (code < 0x20 && code !== 0x09) || (code >= 0xd800 && code <= 0xdfff)) return false;
  }
  return true;
}

function exactHeaderStringDomain(schema: unknown, allowConst: boolean): ReadonlySet<string> | null {
  if (schema === false) return new Set();
  const object = asRecord(schema);
  if (!object) return null;
  const domains: Set<string>[] = [];
  if (allowConst && typeof object.const === "string") domains.push(new Set([object.const]));
  if (Array.isArray(object.enum)) {
    const strings = object.enum.filter((candidate): candidate is string => typeof candidate === "string");
    if (strings.length > 0) domains.push(new Set(strings));
  }
  for (const member of Array.isArray(object.allOf) ? object.allOf : []) {
    const domain = exactHeaderStringDomain(member, allowConst);
    if (domain !== null) domains.push(new Set(domain));
  }
  if (domains.length === 0) return null;
  const [first, ...rest] = domains;
  for (const candidate of [...first!]) {
    if (rest.some((domain) => !domain.has(candidate))) first!.delete(candidate);
  }
  return first!;
}

function defaultPartContentType(
  declaration: ReturnType<typeof resolveDeclaration>,
  value: unknown,
): string {
  const declared = declaration.keywordString("contentMediaType");
  if (!declared.conflict && declared.value !== "") return declared.value;
  const encoding = declaration.keywordString("contentEncoding");
  if (!encoding.conflict && encoding.value !== "" && declaration.admitsStringAsSoleNonNullType()) {
    return "application/octet-stream";
  }
  if (declaration.typeless()) return "application/octet-stream";
  const format = declaration.format();
  if (!format.conflict && (format.value === "binary" || format.value === "byte")) {
    return "application/octet-stream";
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
