import { swagger20ConfigRequired } from "./swagger20-context.js";
import { escapePointerToken } from "./swagger20-reference.js";
import { parseMediaRange, parseMediaType, type ParsedMediaType } from "./media.js";
import {
  arrayMember,
  isSwagger20Object,
  member,
  objectMember,
  stringMember,
  type Swagger20Document,
  type Swagger20Object,
  type Swagger20ResolvedOperation,
  type Swagger20Resource,
} from "./swagger20-model.js";
import {
  canonicalBase64Bytes,
  type Swagger20Parameter,
  type Swagger20ParameterSet,
  type Swagger20RoutedInput,
  type Swagger20WireContribution,
} from "./swagger20-parameters.js";
import {
  resolveSwagger20SchemaDeclaration,
  swagger20ByteString,
  swagger20RawOctets,
  swagger20SoleString,
  type Swagger20SchemaDeclaration,
} from "./swagger20-schema.js";
import { newSwagger20ResolutionMemo } from "./swagger20-reference.js";

export interface Swagger20ParsedMedia extends ParsedMediaType {
  specificity: 0 | 1 | 2;
}

export interface Swagger20MediaEntry {
  raw: string;
  parsed?: Swagger20ParsedMedia;
  error?: Error;
  colliding: boolean;
}

export interface Swagger20MediaSet {
  entries: Swagger20MediaEntry[];
}

export type Swagger20MediaLane = "json" | "text" | "byte" | "octets" | "urlencoded" | "multipart";

export interface Swagger20PayloadModel {
  kind?: "body" | "formData";
  body?: Swagger20Parameter;
  form: Swagger20Parameter[];
  declaration?: Swagger20SchemaDeclaration;
}

export interface Swagger20MediaSelection {
  media: Swagger20ParsedMedia;
  declaration: Swagger20ParsedMedia;
  lane: Swagger20MediaLane;
}

export interface Swagger20ResolvedResponse {
  raw: Swagger20Object;
  resource: Swagger20Resource;
  key: string;
}

export function effectiveSwagger20MediaSet(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  field: "consumes" | "produces",
): Swagger20MediaSet {
  let declaration = arrayMember(operation.raw, field);
  if (!declaration.present) declaration = arrayMember(document.root, field);
  if (!declaration.present) return { entries: [] };
  if (!declaration.valid) throw new Error(`effective ${field} is not an array`);
  const entries = declaration.value!.map((value, index): Swagger20MediaEntry => {
    if (typeof value !== "string" || value === "") throw new Error(`effective ${field} member ${index} is not a nonempty string`);
    try { return { raw: value, parsed: parseDeclaration(value), colliding: false }; }
    catch (error: unknown) { return { raw: value, error: asError(error), colliding: false }; }
  });
  const identities = new Map<string, number>();
  for (const entry of entries) if (entry.parsed) identities.set(entry.parsed.identity, (identities.get(entry.parsed.identity) ?? 0) + 1);
  for (const entry of entries) if (entry.parsed && identities.get(entry.parsed.identity)! > 1) entry.colliding = true;
  return { entries };
}

export async function swagger20PayloadFor(
  parameters: Swagger20ParameterSet,
  operation: Swagger20ResolvedOperation,
): Promise<Swagger20PayloadModel> {
  if (parameters.body) {
    const schema = member(parameters.body.raw, "schema").value;
    const declaration = await resolveSwagger20SchemaDeclaration(operation.graph, schema, parameters.body.resource);
    return { kind: "body", body: parameters.body, form: [], declaration };
  }
  const form = parameters.nonBody.filter((parameter) => parameter.in === "formData")
    .sort((left, right) => left.name.localeCompare(right.name));
  return form.length === 0 ? { form } : { kind: "formData", form };
}

export function swagger20PayloadRequired(model: Swagger20PayloadModel): boolean {
  return model.body?.required === true || model.form.some((parameter) => parameter.required);
}

export function selectSwagger20RequestMedia(
  set: Swagger20MediaSet,
  model: Swagger20PayloadModel,
  configured?: string,
): Swagger20MediaSelection {
  if (!model.kind) throw new Error("payload was supplied but the operation declares no request payload model");
  if (configured !== undefined) {
    const wanted = parseConcrete(configured);
    const matches = bestMatches(set, wanted);
    if (matches.length === 0) throw new Error(`configuration.requestMedia ${JSON.stringify(wanted.canonical)} matches no non-colliding effective consumes declaration`);
    if (matches.length !== 1) throw new Error(`configuration.requestMedia ${JSON.stringify(wanted.canonical)} ambiguously matches effective consumes`);
    return { media: wanted, declaration: matches[0]!, lane: swagger20RequestLane(wanted, model) };
  }
  const candidates: Array<{ declaration: Swagger20ParsedMedia; lane?: Swagger20MediaLane }> = [];
  for (const entry of set.entries) {
    if (!entry.parsed || entry.colliding) continue;
    if (entry.parsed.specificity < 2) {
      if (swagger20RangeHasUsableLane(entry.parsed, model)) candidates.push({ declaration: entry.parsed });
      continue;
    }
    try { candidates.push({ declaration: entry.parsed, lane: swagger20RequestLane(entry.parsed, model) }); }
    catch { /* this smallest media lane is unusable */ }
  }
  if (candidates.length === 1 && candidates[0]!.lane) {
    return { media: candidates[0]!.declaration, declaration: candidates[0]!.declaration, lane: candidates[0]!.lane! };
  }
  if (candidates.length === 0) throw new Error("effective consumes has no usable request-media candidate");
  throw swagger20ConfigRequired("requestMedia", "");
}

export function encodeSwagger20RequestPayload(
  selection: Swagger20MediaSelection,
  model: Swagger20PayloadModel,
  routed: Swagger20RoutedInput,
  propertyMedia: Record<string, string> = {},
): { body: Uint8Array; contentType: string } {
  if (selection.lane === "json") {
    const encoded = JSON.stringify(routed.body);
    if (encoded === undefined) throw new Error("request value has no strict JSON image");
    return { body: new TextEncoder().encode(encoded), contentType: selection.media.canonical };
  }
  if (selection.lane === "text") {
    if (typeof routed.body !== "string") throw new Error("character-data request lane requires a string");
    requireUTF8(selection.media);
    return { body: new TextEncoder().encode(routed.body), contentType: selection.media.canonical };
  }
  if (selection.lane === "byte") {
    if (typeof routed.body !== "string" || !standardBase64(routed.body)) throw new Error("format byte request value is not standard Base64");
    return { body: new TextEncoder().encode(routed.body), contentType: selection.media.canonical };
  }
  if (selection.lane === "octets") {
    if (typeof routed.body !== "string") throw new Error("raw-octet request lane requires a canonical Base64 string");
    return { body: canonicalBase64Bytes(routed.body), contentType: selection.media.canonical };
  }
  if (selection.lane === "urlencoded") {
    return { body: new TextEncoder().encode(urlEncoded(routed.formData)), contentType: selection.media.canonical };
  }
  if (model.kind !== "formData") throw new Error("multipart lane has no formData model");
  return multipart(routed.formData, selection.media, propertyMedia);
}

export function swagger20RequestLane(media: Swagger20ParsedMedia, model: Swagger20PayloadModel): Swagger20MediaLane {
  if (media.specificity !== 2) throw new Error("media type is not concrete");
  if (model.kind === "formData") {
    if (media.base === "application/x-www-form-urlencoded") {
      const file = model.form.find((parameter) => parameter.typeName === "file");
      if (file) throw new Error(`urlencoded form cannot carry file parameter ${JSON.stringify(file.name)}`);
      return "urlencoded";
    }
    if (media.base === "multipart/form-data") {
      if (model.form.some((parameter) => !safeMultipartName(parameter.name))) throw new Error("multipart form parameter name is unsafe");
      return "multipart";
    }
    throw new Error("formData requires application/x-www-form-urlencoded or multipart/form-data");
  }
  if (isJSONMedia(media.base)) return "json";
  if (swagger20ByteString(model.declaration!)) return "byte";
  if (swagger20RawOctets(model.declaration!)) return "octets";
  if (isCharacterMedia(media.base) && swagger20SoleString(model.declaration!)) {
    requireUTF8(media);
    return "text";
  }
  throw new Error("selected media and resolved declaration define no request byte carriage");
}

export function swagger20RangeHasUsableLane(range: Swagger20ParsedMedia, model: Swagger20PayloadModel): boolean {
  const candidates = model.kind === "formData"
    ? ["application/x-www-form-urlencoded", "multipart/form-data"]
    : ["application/json", "text/plain", "application/octet-stream", "image/png"];
  return candidates.some((candidate) => {
    const media = parseConcrete(candidate);
    if (!declarationMatches(range, media)) return false;
    try { swagger20RequestLane(media, model); return true; } catch { return false; }
  });
}

/**
 * Reports whether a Responses key names a declaration that can govern a
 * SUCCESSFUL (2xx final status) response.
 *
 * Round R2's F1 ruling scopes the upstream-invalid Response Object exclusion to
 * the governing SUCCESS declaration, family-wide: a failure body is opaque
 * application-authored data, so a defect in the declaration that governs one
 * loses no representation and must not destroy a target whose success path is
 * intact. It is the same "loses no representation" reasoning that justifies the
 * `{}` carve-out, and it is what the 3.0/3.1 acceptance floor already performs
 * by never climbing at a non-success response.
 *
 * `default` ALWAYS qualifies on this edition, and that is the edition
 * difference rather than an oversight. OAS 2.0's Responses Object has no range
 * keys, so nothing can exhaustively declare the 2xx class; `default` can
 * therefore always govern some 2xx status. The 3.x siblings qualify `default`
 * on the absence of a `2XX` range key, which is the same question asked where
 * ranges exist.
 */
export function swagger20SuccessResponseKey(key: string): boolean {
  return key === "default" || /^2[0-9][0-9]$/u.test(key);
}

/**
 * Reports the upstream-invalid governing Response Object defects
 * `openbindings.openapi-2.0@1` §9.4 names, in this edition's own spelling: the
 * member is not a Response Object at all, or it violates the Response Object's
 * fixed-field constraints -- a `description` that is not a string, a `schema`
 * that is not a Schema Object, a `headers` or `examples` value that is not a
 * map, or a `headers` member that is not a Header Object.
 *
 * It is a DECIDABLE test and deliberately not a validator, exactly as the
 * 3.0/3.1 floor's D16 is. Nothing here inspects a Header Object's or a Schema
 * Object's own fields: those are declaration questions the response and schema
 * rungs already own, and a wrong answer would cost a target its representation.
 *
 * A `$ref`ed Response Object governs its referencing target exactly as an
 * inline one does, so the constraints are read at the position they actually
 * occupy -- inside the referenced object -- which is why this resolves first.
 */
export async function swagger20ResponseObjectDefect(
  operation: Swagger20ResolvedOperation,
  value: unknown,
  key: string,
): Promise<void> {
  const resolved = await resolveSwagger20ResponseValue(operation, value, operation.resource, key);
  const raw = resolved.raw;
  const descriptionDeclared = Object.hasOwn(raw, "description");
  const schemaDeclared = Object.hasOwn(raw, "schema");
  if (!descriptionDeclared && schemaDeclared) {
    // D9's declared-content gate in this edition's spelling. Without a `schema`
    // this is the carve-out and never reaches here.
    throw new Error("Response Object omits its REQUIRED description while declaring a schema");
  }
  if (schemaDeclared && !isSwagger20Object(raw.schema)) {
    throw new Error("Response Object `schema` is not a Schema Object");
  }
  for (const field of ["headers", "examples"] as const) {
    if (!Object.hasOwn(raw, field)) continue;
    const members = raw[field];
    if (!isSwagger20Object(members)) throw new Error(`Response Object ${JSON.stringify(field)} is not a map`);
    if (field !== "headers") continue;
    for (const name of Object.keys(members).sort()) {
      // The specification-extension prefix is lower-case `x-`, and the test is
      // case-SENSITIVE for the same reason the 3.0/3.1 floor's is: these keys
      // are HEADER NAMES, and `X-Request-Id` is an ordinary header rather than
      // an extension. Lower-casing here would exempt most real custom headers.
      if (name.startsWith("x-")) continue;
      if (!isSwagger20Object(members[name])) {
        throw new Error(`Response Object header ${JSON.stringify(name)} is not a Header Object`);
      }
    }
  }
}

export async function swagger20ResponsesFor(operation: Swagger20ResolvedOperation): Promise<Swagger20Object> {
  const responses = objectMember(operation.raw, "responses");
  if (!responses.valid) throw new Error("selected Swagger 2.0 Operation requires a Responses Object");
  let count = 0;
  for (const [key, value] of Object.entries(responses.value!)) {
    if (key.toLowerCase().startsWith("x-")) continue;
    if (key !== "default" && !/^[1-5][0-9][0-9]$/u.test(key)) throw new Error(`selected Swagger 2.0 Responses Object contains inadmissible key ${JSON.stringify(key)}`);
    count++;
    // The upstream-invalid governing Response Object exclusion, scoped to the
    // declaration that can govern a SUCCESS response (F1; see
    // `swagger20SuccessResponseKey`). A defective NON-SUCCESS declaration is
    // left standing here: it destroys no representation, and if it actually
    // governs an actual failure response the response rung reports it there.
    if (!swagger20SuccessResponseKey(key)) continue;
    try {
      await swagger20ResponseObjectDefect(operation, value, key);
    } catch (error: unknown) {
      throw new Error(
        `selected Swagger 2.0 response ${JSON.stringify(key)} is upstream-invalid: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  }
  if (count === 0) throw new Error("selected Swagger 2.0 Responses Object has no exact status or default Response");
  return responses.value!;
}

export async function governingSwagger20Response(
  operation: Swagger20ResolvedOperation,
  responses: Swagger20Object,
  status: number,
): Promise<Swagger20ResolvedResponse | undefined> {
  const exact = String(status);
  const key = Object.hasOwn(responses, exact) ? exact : Object.hasOwn(responses, "default") ? "default" : undefined;
  if (!key) return undefined;
  return resolveResponse(operation, responses[key], operation.resource, key, new Set());
}

/** @internal - resolves one authored Response/Reference position for native analysis. */
export function resolveSwagger20ResponseValue(
  operation: Swagger20ResolvedOperation,
  value: unknown,
  resource: Swagger20Resource,
  key: string,
): Promise<Swagger20ResolvedResponse> {
  return resolveResponse(operation, value, resource, key, new Set());
}

async function resolveResponse(
  operation: Swagger20ResolvedOperation,
  value: unknown,
  resource: Swagger20Resource,
  key: string,
  active: Set<string>,
): Promise<Swagger20ResolvedResponse> {
  if (!isSwagger20Object(value)) throw new Error("Response or Reference Object is not an object");
  const reference = stringMember(value, "$ref");
  if (reference.present) {
    if (!reference.valid || reference.value === "") throw new Error("Response Reference Object has an invalid $ref");
    const identity = `${resource.retrieval ?? resource.requested ?? ""}|response|${reference.value}`;
    if (active.has(identity)) throw new Error("selected Response reference cycle is not resolvable");
    active.add(identity);
    try {
      const resolved = await operation.graph.resolveReference(reference.value!, resource, newSwagger20ResolutionMemo());
      if (resolved.cycle) throw new Error("selected Response reference cycle is not resolvable");
      return resolveResponse(operation, resolved.node, resolved.resource, key, active);
    } finally { active.delete(identity); }
  }
  // `description` is REQUIRED and its value MUST be a string. ABSENCE is the
  // carve-out `openbindings.openapi-2.0@1` §9.4 states in the same breath as
  // the exclusion: a governing Response Object that omits it while declaring no
  // `schema` loses no representation -- nothing it says about a response body
  // is misdeclared -- so it still GOVERNS, and the omission-with-`schema` case
  // is excluded before dispatch by `swagger20ResponseObjectDefect` rather than
  // refused here. A PRESENT `description` of another kind is not the carve-out:
  // it is a fixed-field violation like any other.
  const description = stringMember(value, "description");
  if (description.present && !description.valid) {
    throw new Error("Response Object `description` is not a string");
  }
  return { raw: value, resource, key };
}

export async function decodeSwagger20Response(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  governing: Swagger20ResolvedResponse,
  body: Uint8Array,
  actualContentType: string,
): Promise<unknown> {
  if (!member(governing.raw, "schema").present) throw new Error("non-empty response is governed by a Response Object without schema");
  const declaration = await resolveSwagger20SchemaDeclaration(
    operation.graph, governing.raw.schema, governing.resource, true,
  );
  const produces = effectiveSwagger20MediaSet(document, operation, "produces");
  const media = parseConcrete(actualContentType || "application/octet-stream");
  const matches = bestMatches(produces, media);
  if (matches.length === 0) throw new Error(`response media ${JSON.stringify(media.canonical)} matches no non-colliding effective produces declaration`);
  if (matches.length !== 1) throw new Error(`response media ${JSON.stringify(media.canonical)} ambiguously matches effective produces`);
  const lane = swagger20ResponseLane(media, declaration);
  if (lane === "json") {
    try { return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(body)) as unknown; }
    catch (error: unknown) { throw new Error("response body is not strict JSON", { cause: error }); }
  }
  if (lane === "text") return new TextDecoder("utf-8", { fatal: true }).decode(body);
  if (lane === "byte") {
    const text = new TextDecoder("utf-8", { fatal: true }).decode(body);
    if (!standardBase64(text)) throw new Error("format byte response is not standard Base64");
    return text;
  }
  return bytesToBase64(body);
}

export function swagger20ResponseLane(
  media: Swagger20ParsedMedia,
  declaration: Swagger20SchemaDeclaration,
): Swagger20MediaLane {
  if (isJSONMedia(media.base)) return "json";
  if (swagger20ByteString(declaration)) return "byte";
  if (swagger20RawOctets(declaration)) return "octets";
  if (isCharacterMedia(media.base) && swagger20SoleString(declaration)) {
    requireUTF8(media);
    return "text";
  }
  throw new Error(`response media ${JSON.stringify(media.canonical)} and declaration define no byte carriage`);
}

export function contentCodingTokens(value: string): string[] {
  const tokens = value.split(",").map((token) => token.trim().toLowerCase());
  if (tokens.some((token) => !/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u.test(token))) throw new Error("invalid Content-Encoding token list");
  return tokens;
}

function bestMatches(set: Swagger20MediaSet, wanted: Swagger20ParsedMedia): Swagger20ParsedMedia[] {
  let specificity = -1;
  let parameterCount = -1;
  let best: Swagger20ParsedMedia[] = [];
  for (const entry of set.entries) {
    if (!entry.parsed || entry.colliding || !declarationMatches(entry.parsed, wanted)) continue;
    const nextParameters = Object.keys(entry.parsed.params).length;
    if (entry.parsed.specificity > specificity || entry.parsed.specificity === specificity && nextParameters > parameterCount) {
      specificity = entry.parsed.specificity;
      parameterCount = nextParameters;
      best = [entry.parsed];
    } else if (entry.parsed.specificity === specificity && nextParameters === parameterCount) best.push(entry.parsed);
  }
  return best;
}

function declarationMatches(declaration: Swagger20ParsedMedia, concrete: Swagger20ParsedMedia): boolean {
  const [declaredType, declaredSubtype] = declaration.base.split("/");
  const [actualType, actualSubtype] = concrete.base.split("/");
  if (declaredType !== "*" && declaredType !== actualType) return false;
  if (declaredSubtype !== "*" && declaredSubtype !== actualSubtype) return false;
  return Object.entries(declaration.params).every(([name, value]) => concrete.params[name] === value);
}

function parseDeclaration(raw: string): Swagger20ParsedMedia {
  if (raw.includes("*")) {
    const range = parseMediaRange(raw, true);
    return { ...range, specificity: range.specificity };
  }
  return parseConcrete(raw);
}

function parseConcrete(raw: string): Swagger20ParsedMedia {
  return { ...parseMediaType(raw, true), specificity: 2 };
}

export function parseSwagger20ConcreteMedia(raw: string): Swagger20ParsedMedia {
  return parseConcrete(raw);
}

function isJSONMedia(base: string): boolean {
  return base === "application/json" || base.split("/")[1]?.endsWith("+json") === true;
}

function isCharacterMedia(base: string): boolean {
  const [type, subtype] = base.split("/");
  return type === "text" || base === "application/xml" || subtype?.endsWith("+xml") === true;
}

function requireUTF8(media: Swagger20ParsedMedia): void {
  const charset = media.params.charset?.toLowerCase();
  if (charset !== undefined && charset !== "utf-8" && charset !== "utf8") throw new Error(`unsupported character-data charset ${JSON.stringify(charset)}`);
}

function urlEncoded(contributions: Swagger20WireContribution[]): string {
  return contributions.map((contribution) =>
    formEncode(contribution.name) + (contribution.valuePresent ? `=${formEncode(contribution.value)}` : "")).join("&");
}

function formEncode(value: string): string {
  return [...new TextEncoder().encode(value)].map((byte) => {
    if (byte === 0x20) return "+";
    if (byte >= 0x41 && byte <= 0x5a || byte >= 0x61 && byte <= 0x7a || byte >= 0x30 && byte <= 0x39
      || byte === 0x2d || byte === 0x2e || byte === 0x5f || byte === 0x7e) return String.fromCharCode(byte);
    return `%${byte.toString(16).toUpperCase().padStart(2, "0")}`;
  }).join("");
}

function multipart(
  contributions: Swagger20WireContribution[],
  media: Swagger20ParsedMedia,
  propertyMedia: Record<string, string>,
): { body: Uint8Array; contentType: string } {
  let boundary = media.params.boundary ?? `swagger20-boundary-${Math.random().toString(16).slice(2)}`;
  for (let attempt = 0; contributions.some((item) => multipartContent(item).includes(boundary)); attempt++) {
    if (attempt >= 31 || media.params.boundary) throw new Error("multipart boundary occurs in representation content");
    boundary = `swagger20-boundary-${Math.random().toString(16).slice(2)}`;
  }
  const chunks: Uint8Array[] = [];
  const text = (value: string) => new TextEncoder().encode(value);
  for (const contribution of contributions) {
    const file = contribution.parameter?.typeName === "file";
    let contentType = "text/plain; charset=utf-8";
    if (file) {
      const configured = propertyMedia[contribution.name];
      if (!configured) throw swagger20ConfigRequired("propertyMedia", `/${escapePointerToken(contribution.name)}`);
      contentType = parseConcrete(configured).canonical;
    }
    const name = contribution.name.replaceAll("\\", "\\\\").replaceAll('"', '\\"');
    chunks.push(text(`--${boundary}\r\nContent-Disposition: form-data; name="${name}"\r\nContent-Type: ${contentType}\r\n\r\n`));
    chunks.push(file ? contribution.octets! : text(contribution.value));
    chunks.push(text("\r\n"));
  }
  chunks.push(text(`--${boundary}--\r\n`));
  const body = new Uint8Array(chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0));
  let offset = 0;
  for (const chunk of chunks) { body.set(chunk, offset); offset += chunk.byteLength; }
  return { body, contentType: `${media.base}; boundary=${boundary}` };
}

function multipartContent(contribution: Swagger20WireContribution): string {
  return `${contribution.name}\u0000${contribution.value}\u0000${contribution.octets ? bytesToBase64(contribution.octets) : ""}`;
}

function safeMultipartName(value: string): boolean {
  return value !== "" && !/[\u0000-\u0008\u000a-\u001f\u007f]/u.test(value);
}

function standardBase64(value: string): boolean {
  try { atob(value); return /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u.test(value); }
  catch { return false; }
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}
