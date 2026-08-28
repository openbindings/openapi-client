import {
  FAMILY_MULTIPART,
  FAMILY_URLENCODED,
  buildMultipartBody as buildMultipartBodyFromClient,
  parseMediaRange,
  parseMediaType,
  planRequestBodies as planRequestBodiesFromClient,
  type BodyPlan,
  type ParsedMediaRange,
  type ParsedMediaType,
} from "./media.js";
import type {
  OpenAPIDocument,
  OpenAPIMediaType,
  OpenAPIOperation,
} from "./types.js";
import {
  resolveDeclaration,
  resolvedPropertySlots,
  type SchemaDeclaration,
} from "./resolved-declaration.js";
import {
  convertParameterScalars,
  type OpenAPIParameterConverter,
} from "./params.js";

interface PropertyMediaFacts {
  required: string[];
  declarations: Record<string, string>;
  raw: string[];
  transferEncodings: Record<string, string>;
  unsafeMultipartName: boolean;
  unusable: boolean;
  oas30: boolean;
}

export interface OpenAPIResolvedBodyPlan extends BodyPlan {
  /** Properties whose selected form/part representation needs one consumer choice. */
  propertyMedia?: string[];
  /** Authored Encoding contentType declaration for each required choice. */
  propertyMediaDeclarations?: Record<string, string>;
  /** Properties represented as canonical Base64 at the client boundary. */
  rawProperties?: string[];
  /** Artifact-declared OAS 3.0 Content-Transfer-Encoding fields. */
  transferEncodings?: Record<string, string>;
  oas30?: boolean;
}

/**
 * Routes request planning through the resolved-declaration view. The returned
 * plan retains the authored media object and records every property-media
 * decision separately.
 */
export function planResolvedRequestBodies(
  ...args: Parameters<typeof planRequestBodiesFromClient>
): ReturnType<typeof planRequestBodiesFromClient> {
  const [operation, options] = args;
  const oas30 = options?.openapiVersion?.startsWith("3.0") ?? true;
  const facts = requestPropertyMediaFacts(operation, oas30);
  return withEngineEncodingAdmissionView(operation, options?.openapiVersion, () =>
    withEngineMediaAdmissionView(operation, oas30, facts, () => {
      const plans = planRequestBodiesFromClient(...args) as OpenAPIResolvedBodyPlan[];
      return plans.flatMap((plan) => {
        const mediaFacts = facts.get(plan.mediaKey);
        if (mediaFacts && (mediaFacts.unusable
          || (mediaFacts.unsafeMultipartName && plan.family === FAMILY_MULTIPART))) return [];
        if (mediaFacts) {
          plan.propertyMedia = [...mediaFacts.required];
          plan.propertyMediaDeclarations = { ...mediaFacts.declarations };
          plan.rawProperties = [...mediaFacts.raw];
          plan.transferEncodings = { ...mediaFacts.transferEncodings };
          plan.oas30 = mediaFacts.oas30;
        }
        return [plan];
      });
    }));
}

/** Required propertyMedia names carried by one represented request plan. */
export function requiredPropertyMediaNames(plan: BodyPlan): string[] {
  return [...((plan as OpenAPIResolvedBodyPlan).propertyMedia ?? [])];
}

export function plansRequirePropertyMedia(plans: readonly BodyPlan[]): boolean {
  return plans.some((plan) => requiredPropertyMediaNames(plan).length > 0);
}

/**
 * The direct helper's corrected 3.1 typeless-part lane. Other parts remain on
 * the standalone carrier. A typeless application value is a canonical Base64
 * string and the part receives the decoded octets.
 */
export function buildResolvedMultipartBody(
  doc: OpenAPIDocument,
  media: OpenAPIMediaType | null,
  fields: Record<string, unknown>,
  revision3 = false,
  dynamicProperties = false,
): FormData {
  if (!revision3) {
    return buildMultipartBodyFromClient(doc, media, fields, revision3, dynamicProperties);
  }
  const oas30 = doc.openapi?.startsWith("3.0") ?? true;
  const root = media?.schema as SchemaDeclaration;
  const resolved = resolveDeclaration(root, oas30);
  const rawNames = new Set<string>();
  if (!oas30) {
    for (const name of Object.keys(fields)) {
      let member = resolved.property(name);
      if (member.declaresOnly("array")) member = member.items();
      if (member.typeless()) rawNames.add(name);
    }
  }

  const ordinary = Object.fromEntries(
    Object.entries(fields).filter(([name]) => !rawNames.has(name)),
  );
  const form = withRuntimeEncodingView(media, oas30, () =>
    buildMultipartBodyFromClient(doc, media, ordinary, revision3, dynamicProperties));
  const encoding = asRecord(media?.encoding) ?? {};
  for (const name of [...rawNames].sort(codePointCompare)) {
    const enc = asRecord(encoding[name]);
    const declared = typeof enc?.contentType === "string" ? enc.contentType : "";
    const contentType = declared === ""
      ? "application/octet-stream"
      : singleConcreteMediaType(declared).canonical;
    const property = resolved.property(name);
    const values = property.declaresOnly("array")
      ? requireArray(fields[name], name)
      : [fields[name]];
    for (const value of values) {
      const bytes = canonicalBase64Bytes(value, `multipart property ${JSON.stringify(name)}`);
      form.append(name, new Blob([bytes as BlobPart], { type: contentType }), name);
    }
  }
  return form;
}

/** Replaces canonical Base64 multipart payloads with their represented octets. */
export function decodeBase64MultipartParts(body: BodyInit, names: readonly string[]): BodyInit {
  let bytes: Uint8Array;
  if (typeof body === "string") {
    bytes = new TextEncoder().encode(body);
  } else if (body instanceof ArrayBuffer) {
    bytes = new Uint8Array(body);
  } else if (ArrayBuffer.isView(body)) {
    bytes = new Uint8Array(body.buffer, body.byteOffset, body.byteLength);
  } else {
    return body;
  }
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  let changed = false;
  for (const name of names) {
    const marker = `name="${name}"`;
    const markerAt = binary.indexOf(marker);
    if (markerAt < 0) continue;
    const headerStart = binary.lastIndexOf("\r\n", markerAt);
    const bodyStart = binary.indexOf("\r\n\r\n", markerAt);
    const bodyEnd = bodyStart < 0 ? -1 : binary.indexOf("\r\n--", bodyStart + 4);
    if (headerStart < 0 || bodyStart < 0 || bodyEnd < 0) continue;
    const encoded = binary.slice(bodyStart + 4, bodyEnd);
    let decoded: string;
    try {
      decoded = atob(encoded);
      if (btoa(decoded) !== encoded) continue;
    } catch {
      continue;
    }
    const headers = binary.slice(headerStart, bodyStart)
      .replace("\r\nContent-Transfer-Encoding: base64", "");
    binary = binary.slice(0, headerStart) + headers + "\r\n\r\n" + decoded + binary.slice(bodyEnd);
    changed = true;
  }
  if (!changed) return body;
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

/** Applies only explicitly declared multipart Content-Transfer-Encoding fields. */
export function applyMultipartTransferEncodings(
  body: BodyInit,
  declared: Readonly<Record<string, string>>,
): BodyInit {
  let bytes: Uint8Array;
  if (typeof body === "string") bytes = new TextEncoder().encode(body);
  else if (body instanceof ArrayBuffer) bytes = new Uint8Array(body);
  else if (ArrayBuffer.isView(body)) bytes = new Uint8Array(body.buffer, body.byteOffset, body.byteLength);
  else return body;

  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  const withoutImplicit = binary.replace(/\r\nContent-Transfer-Encoding:[^\r\n]*/giu, "");
  let changed = withoutImplicit !== binary;
  binary = withoutImplicit;
  for (const [name, encoding] of Object.entries(declared)) {
    const markerAt = binary.indexOf(`name="${name}"`);
    if (markerAt < 0) continue;
    const headersEnd = binary.indexOf("\r\n\r\n", markerAt);
    if (headersEnd < 0) continue;
    binary = `${binary.slice(0, headersEnd)}\r\nContent-Transfer-Encoding: ${encoding}${binary.slice(headersEnd)}`;
    changed = true;
  }
  return changed ? Uint8Array.from(binary, (character) => character.charCodeAt(0)) : body;
}

/**
 * Validates and materializes property-media choices on a prepared operation.
 */
export function prepareResolvedPropertyMediaView(
  plans: readonly BodyPlan[],
  configured: Record<string, unknown> | undefined,
): void {
  for (const basePlan of plans) {
    const plan = basePlan as OpenAPIResolvedBodyPlan;
    if (!plan.media || (plan.family !== FAMILY_MULTIPART && plan.family !== FAMILY_URLENCODED)) {
      continue;
    }
    const required = plan.propertyMedia ?? [];
    const selected: Record<string, string> = {};
    for (const name of required) {
      const raw = configured?.[name];
      if (typeof raw === "string") {
        selected[name] = selectPropertyMedia(plan, name, raw);
        continue;
      }
      // Unselected and optional plans still need a predecessor-carrier
      // spelling at prepare time. Invocation validates the selected plan and
      // never treats this placeholder as a consumer decision.
      const declaration = plan.propertyMediaDeclarations?.[name] ?? "";
      selected[name] = firstConcreteMediaMember(declaration) ?? "application/octet-stream";
    }

    const media = plan.media as OpenAPIMediaType & { encoding?: Record<string, Record<string, unknown>> };
    media.encoding ??= {};
    for (const [name, contentType] of Object.entries(selected)) {
      media.encoding[name] = { ...(media.encoding[name] ?? {}), contentType };
    }
    for (const name of plan.rawProperties ?? []) {
      const contentType = selected[name] ?? media.encoding[name]?.contentType;
      media.encoding[name] = {
        ...(media.encoding[name] ?? {}),
        ...(typeof contentType === "string" && contentType !== "" ? { contentType } : {}),
      };
      materializeRawProperty(plan.media.schema, name, plan.oas30 === true);
    }
    stripDescriptiveEncodingHeaders(media);
    if (plan.oas30 && plan.family === FAMILY_MULTIPART) stripMultipartStyleControls(media);
  }
}

export function configuredResolvedPropertyMedia(
  plan: BodyPlan,
  configured: Record<string, unknown> | undefined,
): Record<string, string> {
  const result: Record<string, string> = {};
  for (const name of requiredPropertyMediaNames(plan)) {
    const raw = configured?.[name];
    if (typeof raw !== "string") throw new Error(`configuration.propertyMedia.${name} is required`);
    result[name] = selectPropertyMedia(plan, name, raw);
  }
  return result;
}

export function selectPropertyMedia(
  plan: OpenAPIResolvedBodyPlan,
  name: string,
  choice: string,
): string {
  const wanted = parseMediaType(choice, true);
  const declaration = plan.propertyMediaDeclarations?.[name] ?? "";
  if (declaration === "") return wanted.canonical;
  const members = splitHTTPList(declaration);
  const parsed = members.map((member) => parseMediaDeclaration(member));
  const identities = new Map<string, number>();
  for (const member of parsed) identities.set(member.identity, (identities.get(member.identity) ?? 0) + 1);
  const matches = parsed.filter((member) =>
    identities.get(member.identity) === 1 && mediaDeclarationMatches(member, wanted));
  if (matches.length === 0) {
    throw new Error(`configuration.propertyMedia.${name} matches no declared Encoding contentType member`);
  }
  const bestRange = Math.max(...matches.map(mediaSpecificity));
  const atRange = matches.filter((member) => mediaSpecificity(member) === bestRange);
  const bestParams = Math.max(...atRange.map((member) => Object.keys(member.params).length));
  if (atRange.filter((member) => Object.keys(member.params).length === bestParams).length !== 1) {
    throw new Error(`configuration.propertyMedia.${name} is ambiguous`);
  }
  return wanted.canonical;
}

function requestPropertyMediaFacts(
  operation: OpenAPIOperation,
  oas30: boolean,
): Map<string, PropertyMediaFacts> {
  const result = new Map<string, PropertyMediaFacts>();
  for (const [mediaKey, media] of Object.entries(operation.requestBody?.content ?? {})) {
    const multipart = concreteBase(mediaKey) === "multipart/form-data";
    const urlencoded = concreteBase(mediaKey) === "application/x-www-form-urlencoded";
    if (!multipart && !urlencoded) continue;
    const root = media.schema as SchemaDeclaration;
    const resolved = resolveDeclaration(root, oas30);
    const encoding = asRecord(media.encoding) ?? {};
    const required: string[] = [];
    const raw: string[] = [];
    const transferEncodings: Record<string, string> = {};
    const declarations: Record<string, string> = {};
    let unsafeMultipartName = false;
    let unusable = false;
    for (const name of resolved.propertyNames()) {
      if (multipart && /[\r\n]/.test(name)) unsafeMultipartName = true;
      if (
        oas30
        && resolvedPropertySlots(root, name, true)
          .some((slot) => typeof slot.value === "boolean")
      ) {
        // Boolean-literal schemas are outside the closed 3.0 Schema Object
        // dialect. The acceptance floor accounts the invalid alternative;
        // no propertyMedia decision repairs that malformed spelling.
        continue;
      }
      let property = resolved.property(name);
      if (property.declaresOnly("array")) property = property.items();
      const typeless = property.typeless();
      const enc = asRecord(encoding[name]);
      const contentType = typeof enc?.contentType === "string" ? enc.contentType : "";
      const contentPath = !encodingUsesStyleControls(enc);
      const mediaChoice = contentPath && contentType !== "" && !isSingleConcreteMediaType(contentType);
      if ((oas30 && multipart && typeless) || mediaChoice) {
        required.push(name);
        declarations[name] = contentType;
      }
      if (multipart && typeless) raw.push(name);
      if (oas30 && multipart && property.format().value === "byte") {
        const transferEncoding = declaredBase64TransferEncoding(enc);
        if (transferEncoding === false) unusable = true;
        else if (transferEncoding !== null) transferEncodings[name] = transferEncoding;
      }
    }
    result.set(mediaKey, {
      required: uniqueSorted(required),
      declarations,
      raw: uniqueSorted(raw),
      transferEncodings,
      unsafeMultipartName,
      unusable,
      oas30,
    });
  }
  return result;
}

/** Materializes strict Encoding style declarations for the base planner. */
export function prepareResolvedEncodingView(plans: BodyPlan[]): void {
  for (const plan of plans) {
    const root = plan.media?.schema as SchemaDeclaration;
    const encoding = asRecord(plan.media?.encoding);
    if (!encoding || !asRecord(root)) continue;
    const oas30 = plan.openapiVersion?.startsWith("3.0") === true;
    for (const [name, rawEncoding] of Object.entries(encoding)) {
      const entry = asRecord(rawEncoding);
      if (!encodingUsesSerializationForPlan(plan, entry)) continue;
      const style = typeof entry!.style === "string" && entry!.style !== "" ? entry!.style : "form";
      for (const slot of resolvedPropertySlots(root, name, oas30)) {
        slot.owner[slot.name] = engineSchemaForStyle(style);
      }
    }
  }
}

/** Prepares one form or multipart property for its OpenAPI Encoding lane. */
export function prepareEncodingStylePropertyValue(
  plan: BodyPlan | undefined,
  name: string,
  value: unknown,
  oas30: boolean,
  converter: OpenAPIParameterConverter | undefined,
): unknown {
  const encoding = asRecord(asRecord(plan?.media?.encoding)?.[name]);
  if (!encoding || !encodingUsesSerializationForPlan(plan, encoding)) {
    return prepareContentFormPropertyValue(plan, name, value, oas30, converter);
  }
  const style = typeof encoding.style === "string" && encoding.style !== "" ? encoding.style : "form";
  const prepared = prepareBodyStyleValue(name, value, style, converter);
  return delimitedObjectAsSequence(prepared, style);
}

/** Whether an optional null is omitted on the OpenAPI 3.0 content-form lane. */
export function contentFormNullIsElided(
  plan: BodyPlan | undefined,
  name: string,
  value: unknown,
  oas30: boolean,
): boolean {
  if (value !== null || !plan?.media || !oas30) return false;
  const encoding = asRecord(asRecord(plan.media.encoding)?.[name]);
  if (encodingUsesSerializationForPlan(plan, encoding)) return false;
  const root = resolveDeclaration(plan.media.schema, true);
  return !root.requiresProperty(name) && root.property(name).admitsNull();
}

function prepareBodyStyleValue(
  name: string,
  value: unknown,
  style: string,
  converter: OpenAPIParameterConverter | undefined,
): unknown {
  if (value === null) {
    if (["matrix", "label", "simple", "form"].includes(style)) return null;
    throw new Error(`JSON null has n/a in style ${JSON.stringify(style)}'s undefined cell`);
  }
  const prepared = convertParameterScalars(value, converter);
  const delimiters = nonRFCStyleDelimiters(style);
  if (delimiters !== "" && (
    containsAnyDelimiter(name, delimiters)
    || styleValueContainsDelimiter(prepared, delimiters)
  )) {
    throw new Error(`value or member name contains style ${JSON.stringify(style)}'s structural delimiter`);
  }
  if (style === "spaceDelimited" || style === "pipeDelimited") {
    if (!Array.isArray(prepared) && !asRecord(prepared)) {
      throw new Error(`style ${JSON.stringify(style)} is defined only for arrays or objects`);
    }
  } else if (style === "deepObject" && !asRecord(prepared)) {
    throw new Error("style deepObject is defined only for objects");
  }
  return prepared;
}

function delimitedObjectAsSequence(value: unknown, style: string): unknown {
  const object = asRecord(value);
  if (!object || (style !== "spaceDelimited" && style !== "pipeDelimited")) return value;
  return Object.keys(object).sort(codePointCompare).flatMap((name) => [name, object[name]]);
}

function nonRFCStyleDelimiters(style: string): string {
  if (style === "spaceDelimited") return " ";
  if (style === "pipeDelimited") return "|";
  if (style === "deepObject") return "[]=&";
  return "";
}

function containsAnyDelimiter(value: string, delimiters: string): boolean {
  for (const delimiter of delimiters) if (value.includes(delimiter)) return true;
  return false;
}

function styleValueContainsDelimiter(value: unknown, delimiters: string): boolean {
  if (Array.isArray(value)) {
    return value.some((member) => typeof member === "string" && containsAnyDelimiter(member, delimiters));
  }
  const object = asRecord(value);
  if (!object) return false;
  return Object.entries(object).some(([name, member]) =>
    containsAnyDelimiter(name, delimiters)
    || (typeof member === "string" && containsAnyDelimiter(member, delimiters)));
}

function prepareContentFormPropertyValue(
  plan: BodyPlan | undefined,
  name: string,
  value: unknown,
  oas30: boolean,
  converter: OpenAPIParameterConverter | undefined,
): unknown {
  if (!plan?.media || !oas30) return value;
  const declaration = resolveDeclaration(plan.media.schema, true).property(name);
  try {
    return convertContentFormScalars(declaration, value, converter);
  } catch (error: unknown) {
    throw new Error(`body property ${JSON.stringify(name)}: ${errorMessage(error)}`, { cause: error });
  }
}

function convertContentFormScalars(
  declaration: ReturnType<typeof resolveDeclaration>,
  value: unknown,
  converter: OpenAPIParameterConverter | undefined,
): unknown {
  if (value === null || declaration.ambiguous || declaration.typeless()) return value;
  if (declaration.declaresOnly("array", "null")) {
    if (!Array.isArray(value)) return value;
    return value.map((member, index) => {
      try {
        return convertContentFormScalars(declaration.items(), member, converter);
      } catch (error: unknown) {
        throw new Error(`array member ${index}: ${errorMessage(error)}`, { cause: error });
      }
    });
  }
  if (
    declaration.declaresOnly("boolean", "number", "integer", "null")
    && (typeof value === "boolean" || typeof value === "number")
  ) {
    return convertParameterScalars(value, converter);
  }
  return value;
}

/** Temporarily adapts Encoding declarations while the base planner admits them. */
function withEngineEncodingAdmissionView<T>(
  operation: OpenAPIOperation,
  openapiVersion: string | undefined,
  run: () => T,
): T {
  const restores: Array<() => void> = [];
  const oas30 = openapiVersion?.startsWith("3.0") ?? true;
  try {
    for (const [mediaKey, media] of Object.entries(operation.requestBody?.content ?? {})) {
      const root = media.schema as SchemaDeclaration;
      const encoding = asRecord(media.encoding);
      if (!encoding || !asRecord(root)) continue;
      if (oas30 && mediaKey.split(";", 1)[0]!.trim().toLowerCase() === "multipart/form-data") {
        continue;
      }
      for (const [name, rawEncoding] of Object.entries(encoding)) {
        const entry = asRecord(rawEncoding);
        if (!encodingUsesSerialization(entry)) continue;
        const style = typeof entry!.style === "string" && entry!.style !== "" ? entry!.style : "form";
        const explode = typeof entry!.explode === "boolean" ? entry!.explode : style === "form";
        validateEncodingStyle(name, resolveDeclaration(root, oas30).property(name), style, explode);
        const member = resolvedStyleLaneUndefinedExpansionMember(propertySchema(root, name, oas30), oas30);
        if (member !== null) {
          throw new Error(`body property ${JSON.stringify(name)} member ${JSON.stringify(name + member)} has no expansion defined`);
        }
        for (const slot of resolvedPropertySlots(root, name, oas30)) {
          const previous = slot.value;
          slot.owner[slot.name] = engineSchemaForStyle(style);
          restores.push(() => { slot.owner[slot.name] = previous; });
        }
      }
    }
    return run();
  } finally {
    for (let index = restores.length - 1; index >= 0; index -= 1) restores[index]!();
  }
}

function validateEncodingStyle(
  name: string,
  resolved: ReturnType<typeof resolveDeclaration>,
  style: string,
  explode: boolean,
): void {
  if (style === "form") return;
  if (style === "spaceDelimited" || style === "pipeDelimited") {
    if (explode) throw new Error(`body property ${JSON.stringify(name)} style ${JSON.stringify(style)} has no explode=true cell`);
    if (resolved.declaresOnly("null", "boolean", "number", "integer", "string")) {
      throw new Error(`body property ${JSON.stringify(name)} style ${JSON.stringify(style)} is defined only for arrays or objects`);
    }
    return;
  }
  if (style === "deepObject") {
    if (!explode) throw new Error(`body property ${JSON.stringify(name)} style deepObject has no explode=false cell`);
    if (resolved.declaresOnly("null", "boolean", "number", "integer", "string", "array")) {
      throw new Error(`body property ${JSON.stringify(name)} style deepObject is defined only for objects`);
    }
    return;
  }
  throw new Error(`body property ${JSON.stringify(name)} declares unsupported encoding style ${JSON.stringify(style)}`);
}

function propertySchema(root: SchemaDeclaration, name: string, oas30: boolean): SchemaDeclaration {
  const slots = resolvedPropertySlots(root, name, oas30);
  if (slots.length === 0) return undefined;
  if (slots.length === 1) return slots[0]!.value;
  return { allOf: slots.map((slot) => slot.value) };
}

function resolvedStyleLaneUndefinedExpansionMember(
  schema: SchemaDeclaration,
  oas30: boolean,
): string | null {
  const resolved = resolveDeclaration(schema, oas30);
  if (resolved.declaresOnly("array")) {
    return resolved.items().declaresOnly("object", "array") ? "[]" : null;
  }
  if (!resolved.declaresOnly("object")) return null;
  for (const name of resolved.propertyNames()) {
    if (resolved.property(name).declaresOnly("object", "array")) return `.${name}`;
  }
  return null;
}

function engineSchemaForStyle(style: string): Record<string, unknown> {
  if (style === "spaceDelimited" || style === "pipeDelimited") return { type: "array" };
  if (style === "deepObject") return { type: "object" };
  return {};
}

function encodingUsesSerialization(encoding: Record<string, unknown> | null): boolean {
  return encoding !== null && (
    Object.hasOwn(encoding, "style")
    || Object.hasOwn(encoding, "explode")
    || Object.hasOwn(encoding, "allowReserved")
  );
}

function encodingUsesSerializationForPlan(
  plan: BodyPlan | undefined,
  encoding: Record<string, unknown> | null,
): boolean {
  if (!encodingUsesSerialization(encoding)) return false;
  const oas30 = plan?.openapiVersion?.startsWith("3.0") === true
    || (plan !== undefined && "oas30" in plan && plan.oas30 === true);
  return !(oas30 && plan?.family === FAMILY_MULTIPART);
}

function withEngineMediaAdmissionView<T>(
  operation: OpenAPIOperation,
  oas30: boolean,
  facts: ReadonlyMap<string, PropertyMediaFacts>,
  run: () => T,
): T {
  const restores: Array<() => void> = [];
  try {
    for (const [mediaKey, media] of Object.entries(operation.requestBody?.content ?? {})) {
      const mediaFacts = facts.get(mediaKey);
      if (!mediaFacts) continue;
      const root = media.schema as SchemaDeclaration;
      for (const name of mediaFacts.raw) {
        for (const slot of resolvedPropertySlots(root, name, oas30)) {
          const previous = slot.value;
          slot.owner[slot.name] = privateRawProperty(previous, oas30);
          restores.push(() => { slot.owner[slot.name] = previous; });
        }
      }
      const encoding = asRecord(media.encoding);
      for (const name of mediaFacts.required) {
        const entry = asRecord(encoding?.[name]);
        if (!entry || typeof entry.contentType !== "string" || isSingleConcreteMediaType(entry.contentType)) {
          continue;
        }
        const previous = entry.contentType;
        entry.contentType = propertyMediaAdmissionPlaceholder(
          previous,
          resolveDeclaration(root, oas30).property(name),
        );
        restores.push(() => { entry.contentType = previous; });
      }
      if (encoding) {
        for (const entry of Object.values(encoding)) {
          const enc = asRecord(entry);
          if (!enc) continue;
          if (Object.hasOwn(enc, "headers")) {
            const previous = enc.headers;
            delete enc.headers;
            restores.push(() => { enc.headers = previous; });
          }
          if (oas30 && concreteBase(mediaKey) === "multipart/form-data") {
            for (const key of ["style", "explode", "allowReserved"] as const) {
              if (!Object.hasOwn(enc, key)) continue;
              const previous = enc[key];
              delete enc[key];
              restores.push(() => { enc[key] = previous; });
            }
          }
        }
      }
    }
    return run();
  } finally {
    for (let index = restores.length - 1; index >= 0; index -= 1) restores[index]!();
  }
}

function withRuntimeEncodingView<T>(
  media: OpenAPIMediaType | null,
  oas30: boolean,
  run: () => T,
): T {
  if (!media) return run();
  const restores: Array<() => void> = [];
  const encoding = asRecord(media.encoding);
  try {
    for (const entry of Object.values(encoding ?? {})) {
      const enc = asRecord(entry);
      if (!enc) continue;
      if (Object.hasOwn(enc, "headers")) {
        const previous = enc.headers;
        delete enc.headers;
        restores.push(() => { enc.headers = previous; });
      }
      if (oas30) {
        for (const key of ["style", "explode", "allowReserved"] as const) {
          if (!Object.hasOwn(enc, key)) continue;
          const previous = enc[key];
          delete enc[key];
          restores.push(() => { enc[key] = previous; });
        }
      }
    }
    return run();
  } finally {
    for (let index = restores.length - 1; index >= 0; index -= 1) restores[index]!();
  }
}

function privateRawProperty(schema: SchemaDeclaration, oas30: boolean): Record<string, unknown> {
  const raw = asRecord(schema) ?? {};
  if (resolveDeclaration(raw, oas30).declaresOnly("array")) {
    const items = asRecord(raw.items) ?? {};
    return { ...raw, items: privateRawProperty(items, oas30) };
  }
  return oas30
    ? { ...raw, type: "string", format: "binary" }
    : { ...raw, type: "string", contentEncoding: "base64" };
}

function materializeRawProperty(root: SchemaDeclaration, name: string, oas30: boolean): void {
  for (const slot of resolvedPropertySlots(root, name, oas30)) {
    slot.owner[slot.name] = privateRawProperty(slot.value, oas30);
  }
}

function stripDescriptiveEncodingHeaders(media: OpenAPIMediaType): void {
  for (const value of Object.values(asRecord(media.encoding) ?? {})) {
    const encoding = asRecord(value);
    if (encoding) delete encoding.headers;
  }
}

function stripMultipartStyleControls(media: OpenAPIMediaType): void {
  for (const value of Object.values(asRecord(media.encoding) ?? {})) {
    const encoding = asRecord(value);
    if (!encoding) continue;
    delete encoding.style;
    delete encoding.explode;
    delete encoding.allowReserved;
  }
}

function encodingUsesStyleControls(encoding: Record<string, unknown> | null): boolean {
  return encoding !== null && ["style", "explode", "allowReserved"]
    .some((key) => Object.hasOwn(encoding, key));
}

function declaredBase64TransferEncoding(
  encoding: Record<string, unknown> | null,
): "base64" | false | null {
  const headers = asRecord(encoding?.headers);
  const declared = Object.entries(headers ?? {})
    .filter(([name]) => name.toLowerCase() === "content-transfer-encoding")
    .map(([, value]) => asRecord(value));
  if (declared.length === 0) return null;
  const header = declared[0];
  if (declared.length !== 1 || header === null || header === undefined) return false;
  const schema = header.schema as SchemaDeclaration;
  const resolved = resolveDeclaration(schema, true);
  return !resolved.ambiguous
    && (resolved.typeless() || resolved.admitsStringAsSoleNonNullType())
    && resolved.admitsStringEnumValue("base64")
    ? "base64"
    : false;
}

function splitHTTPList(raw: string): string[] {
  const result: string[] = [];
  let start = 0;
  let quoted = false;
  let escaped = false;
  for (let index = 0; index < raw.length; index += 1) {
    const character = raw[index]!;
    if (escaped) escaped = false;
    else if (character === "\\" && quoted) escaped = true;
    else if (character === '"') quoted = !quoted;
    else if (character === "," && !quoted) {
      const member = raw.slice(start, index).trim();
      if (member === "") throw new Error("empty media declaration member");
      result.push(member);
      start = index + 1;
    }
  }
  if (quoted) throw new Error("unterminated quoted media declaration");
  const last = raw.slice(start).trim();
  if (last === "") throw new Error("empty media declaration member");
  result.push(last);
  return result;
}

function parseMediaDeclaration(raw: string): ParsedMediaType | ParsedMediaRange {
  try { return parseMediaType(raw, true); } catch { return parseMediaRange(raw, true); }
}

function mediaDeclarationMatches(
  declared: ParsedMediaType | ParsedMediaRange,
  concrete: ParsedMediaType,
): boolean {
  const specificity = mediaSpecificity(declared);
  if (specificity === 2 && declared.base !== concrete.base) return false;
  if (specificity === 1 && declared.base.split("/", 1)[0] !== concrete.base.split("/", 1)[0]) return false;
  return Object.entries(declared.params).every(([name, value]) => concrete.params[name] === value);
}

function mediaSpecificity(value: ParsedMediaType | ParsedMediaRange): number {
  return "specificity" in value ? value.specificity : 2;
}

function isSingleConcreteMediaType(raw: string): boolean {
  try {
    return splitHTTPList(raw).length === 1 && Boolean(parseMediaType(raw, true));
  } catch {
    return false;
  }
}

function singleConcreteMediaType(raw: string): ParsedMediaType {
  const members = splitHTTPList(raw);
  if (members.length !== 1) throw new Error("media declaration requires one concrete member");
  return parseMediaType(members[0]!, true);
}

function firstConcreteMediaMember(raw: string): string | null {
  if (raw === "") return null;
  try {
    for (const member of splitHTTPList(raw)) {
      try { return parseMediaType(member, true).canonical; } catch { /* range member */ }
    }
  } catch { /* malformed declarations remain owned by planning */ }
  return null;
}

function propertyMediaAdmissionPlaceholder(
  raw: string,
  property: ReturnType<typeof resolveDeclaration>,
): string {
  try {
    for (const member of splitHTTPList(raw)) {
      try { return parseMediaType(member, true).canonical; } catch { /* range member */ }
      try {
        const range = parseMediaRange(member, true);
        if (range.specificity === 1 && range.base.startsWith("text/")) return "text/plain";
        if (range.specificity === 1 && range.base.startsWith("application/")) return "application/json";
      } catch { /* malformed declarations remain owned by planning */ }
    }
  } catch { /* malformed declarations remain owned by planning */ }
  return property.admitsStringAsSoleNonNullType() ? "text/plain" : "application/json";
}

function concreteBase(raw: string): string {
  try { return parseMediaType(raw, true).base; } catch { return ""; }
}

function canonicalBase64Bytes(value: unknown, subject: string): Uint8Array {
  if (typeof value !== "string") throw new Error(`${subject} requires a canonical Base64 string`);
  try {
    const binary = atob(value);
    if (btoa(binary) !== value) throw new Error("non-canonical Base64");
    return Uint8Array.from(binary, (character) => character.charCodeAt(0));
  } catch (error: unknown) {
    throw new Error(`${subject} requires a canonical Base64 string`, { cause: error });
  }
}

function requireArray(value: unknown, name: string): unknown[] {
  if (!Array.isArray(value)) throw new Error(`multipart property ${JSON.stringify(name)} requires an array`);
  return value;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function uniqueSorted(values: string[]): string[] {
  return [...new Set(values)].sort(codePointCompare);
}

function codePointCompare(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
