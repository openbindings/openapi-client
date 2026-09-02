import type {
  OpenAPIDocument,
  OpenAPIMediaType,
  OpenAPIOperation,
  OpenAPIResponse,
} from "./types.js";
import {
  asArray,
  asObject,
  primitiveString,
  serializeMultipartValue,
  serializeQueryValue,
} from "./params.js";
import { bodySchemaFlattens, codePointCompare } from "./util.js";
import {
  hasDynamicObjectCarriage,
  hasMediaFidelity,
  hasSchemaOmittedOAS30ByteCarriage,
  hasWholeJSONCarriage,
} from "./constants.js";
import {
  OPENAPI_PROFILE_BASE,
  OPENAPI_PROFILE_ROUTED,
  type OpenAPIExecutionProfile,
} from "./profile.js";
import {
  buildOpenAPI32MultipartBody,
  buildOpenAPI32SequentialBody,
  openAPI32MultipartNeedsNativeBuilder,
  openAPI32RequestMediaAdmission,
  serializeOpenAPI32NonJSONText,
  validateOpenAPI32MultipartFields,
  type OpenAPI32SequentialRequestKind,
} from "./openapi32-media.js";
import { resolveDeclaration } from "./resolved-declaration.js";

// This file implements the family documents' request-media and body rules:
// media selection with its deterministic tiebreaks and pre-dispatch
// refusals, multipart part encoding (including the Base64 boundary encoding
// for binary-signaled parts), urlencoded field serialization, and the
// Accept-header membership rule — plus the §8 declared-media facts (success
// responses, streaming capability) the interaction shape is bounded by.
// Mirrors the Go SDK's formats/openapi/media.go.

/**
 * Lowercases a media type and strips its parameters: matching throughout
 * §9.2 compares type and subtype, ignoring parameters.
 */
export function normalizeMediaType(mt: string): string {
  const i = mt.indexOf(";");
  if (i >= 0) mt = mt.slice(0, i);
  return mt.trim().toLowerCase();
}

/** Reports a media range ("*" anywhere, e.g. application/star): ranges never participate in selection. */
export function isMediaRange(mt: string): boolean {
  return mt.includes("*");
}

/** Revision-3 grammar classification: `*` is otherwise a legal tchar. */
function isStructuralMediaRange(type: string, subtype: string): boolean {
  return subtype === "*" || type === "*";
}

/**
 * Reports application/json or a +json structured-suffix type. The argument
 * must already be normalized.
 */
export function isJSONMediaType(mt: string): boolean {
  return mt === "application/json" || mt.endsWith("+json");
}

export interface ParsedMediaType {
  base: string;
  params: Record<string, string>;
  /** Parsed values preserved for wire rendering, separate from semantic comparison values. */
  renderedParams: Record<string, string>;
  canonical: string;
  identity: string;
}

export interface ParsedMediaRange extends ParsedMediaType {
  specificity: 0 | 1;
}

const HTTP_TOKEN = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;

/** Parses the concrete media-type identity used for declaration collision and matching. */
export function parseMediaType(raw: string, semanticParameters = false): ParsedMediaType {
  const parts = splitMediaType(raw);
  const base = (parts.shift() ?? "").trim().toLowerCase();
  const baseParts = base.split("/");
  const validBase = semanticParameters
    ? baseParts.length === 2
      && HTTP_TOKEN.test(baseParts[0] ?? "")
      && HTTP_TOKEN.test(baseParts[1] ?? "")
      && !isStructuralMediaRange(baseParts[0] ?? "", baseParts[1] ?? "")
    : Boolean(base && base.includes("/") && !isMediaRange(base));
  if (!validBase) {
    throw new Error(`media type ${JSON.stringify(raw)} is not concrete`);
  }
  const params: Record<string, string> = semanticParameters ? Object.create(null) as Record<string, string> : {};
  const rendered: Record<string, string> = Object.create(null) as Record<string, string>;
  for (const part of parts) {
    // RFC 9110's parameter production permits empty list elements. Keep
    // compatibility revisions immutable, but accept trailing/repeated
    // semicolons in revision 3 while retaining strict grammar for every
    // non-empty parameter.
    if (semanticParameters && part.trim() === "") continue;
    const equals = part.indexOf("=");
    if (equals <= 0) throw new Error(`invalid media-type parameter in ${JSON.stringify(raw)}`);
    const namePart = part.slice(0, equals);
    const rawName = semanticParameters ? namePart.replace(/^[ \t]*/, "") : namePart.trimStart();
    const rawValue = part.slice(equals + 1);
    if (semanticParameters && (rawName !== rawName.trimEnd() || /^[ \t]/.test(rawValue))) {
      throw new Error(`invalid whitespace around media-type parameter '=' in ${JSON.stringify(raw)}`);
    }
    const name = rawName.trim().toLowerCase();
    if (semanticParameters && !HTTP_TOKEN.test(name)) {
      throw new Error(`invalid media-type parameter name in ${JSON.stringify(raw)}`);
    }
    if ((semanticParameters ? Object.hasOwn(params, name) : name in params)) {
      throw new Error(`duplicate media-type parameter ${JSON.stringify(name)}`);
    }
    const value = unquoteParameter(
      semanticParameters ? rawValue.replace(/[ \t]+$/, "") : rawValue.trim(),
      semanticParameters,
    );
    rendered[name] = value;
    params[name] = semanticParameters ? normalizeMediaParameterValue(name, value) : value;
  }
  const keys = Object.keys(params).sort();
  const identity = [base, ...keys.map((key) => `${key}=${params[key]}`)].join("\u0000");
  const canonical = base + keys.map((key) => `; ${key}=${formatParameter(rendered[key] ?? "")}`).join("");
  return { base, params, renderedParams: rendered, canonical, identity };
}

/** Parses an OpenAPI request content media range accepted by revision 3. */
export function parseMediaRange(raw: string, semanticParameters = false): ParsedMediaRange {
  const parts = splitMediaType(raw);
  const base = (parts.shift() ?? "").trim().toLowerCase();
  const baseParts = base.split("/");
  const rangeType = baseParts[0] ?? "";
  const rangeSubtype = baseParts[1] ?? "";
  const valid = semanticParameters
    ? baseParts.length === 2
      && rangeSubtype === "*"
      && HTTP_TOKEN.test(rangeType)
      && (rangeType !== "*" || base === "*/*")
    : base === "*/*" || /^[^*/\s]+\/\*$/.test(base);
  if (!valid) throw new Error(`media type ${JSON.stringify(raw)} is not a supported media range`);
  const params: Record<string, string> = semanticParameters ? Object.create(null) as Record<string, string> : {};
  const rendered: Record<string, string> = Object.create(null) as Record<string, string>;
  for (const part of parts) {
    if (semanticParameters && part.trim() === "") continue;
    const equals = part.indexOf("=");
    if (equals <= 0) throw new Error(`invalid media-type parameter in ${JSON.stringify(raw)}`);
    const namePart = part.slice(0, equals);
    const rawName = semanticParameters ? namePart.replace(/^[ \t]*/, "") : namePart.trimStart();
    const rawValue = part.slice(equals + 1);
    if (semanticParameters && (rawName !== rawName.trimEnd() || /^[ \t]/.test(rawValue))) {
      throw new Error(`invalid whitespace around media-type parameter '=' in ${JSON.stringify(raw)}`);
    }
    const name = rawName.trim().toLowerCase();
    if (semanticParameters && !HTTP_TOKEN.test(name)) {
      throw new Error(`invalid media-type parameter name in ${JSON.stringify(raw)}`);
    }
    if ((semanticParameters ? Object.hasOwn(params, name) : name in params)) {
      throw new Error(`duplicate media-type parameter ${JSON.stringify(name)}`);
    }
    const value = unquoteParameter(
      semanticParameters ? rawValue.replace(/[ \t]+$/, "") : rawValue.trim(),
      semanticParameters,
    );
    rendered[name] = value;
    params[name] = semanticParameters ? normalizeMediaParameterValue(name, value) : value;
  }
  const keys = Object.keys(params).sort();
  const identity = [base, ...keys.map((key) => `${key}=${params[key]}`)].join("\u0000");
  const canonical = base + keys.map((key) => `; ${key}=${formatParameter(rendered[key] ?? "")}`).join("");
  return { base, params, renderedParams: rendered, canonical, identity, specificity: base === "*/*" ? 0 : 1 };
}

function splitMediaType(raw: string): string[] {
  const parts: string[] = [];
  let start = 0;
  let quoted = false;
  let escaped = false;
  for (let i = 0; i < raw.length; i++) {
    const char = raw[i];
    if (escaped) escaped = false;
    else if (char === "\\" && quoted) escaped = true;
    else if (char === '"') quoted = !quoted;
    else if (char === ";" && !quoted) {
      parts.push(raw.slice(start, i));
      start = i + 1;
    }
  }
  if (quoted) throw new Error(`unterminated quoted media-type parameter in ${JSON.stringify(raw)}`);
  parts.push(raw.slice(start));
  return parts;
}

function unquoteParameter(value: string, fullQuotedPair = false): string {
  if (!value.startsWith('"')) {
    if (fullQuotedPair && !HTTP_TOKEN.test(value)) throw new Error("invalid unquoted media-type parameter value");
    return value;
  }
  if (!value.endsWith('"') || value.length < 2) throw new Error("invalid quoted parameter");
  if (fullQuotedPair) {
    const inner = value.slice(1, -1);
    for (let i = 0; i < inner.length; i++) {
      const code = inner.charCodeAt(i);
      if (inner[i] === "\\") {
        i++;
        if (i >= inner.length) throw new Error("invalid quoted-pair in media-type parameter");
        const escaped = inner.charCodeAt(i);
        if (!(escaped === 9 || escaped === 32 || (escaped >= 0x21 && escaped <= 0x7e) || (escaped >= 0x80 && escaped <= 0xff))) {
          throw new Error("invalid quoted-pair in media-type parameter");
        }
      } else if (!(code === 9 || code === 32 || code === 0x21 || (code >= 0x23 && code <= 0x5b) || (code >= 0x5d && code <= 0x7e) || (code >= 0x80 && code <= 0xff))) {
        throw new Error("invalid quoted-string in media-type parameter");
      }
    }
  }
  return value.slice(1, -1).replace(fullQuotedPair ? /\\(.)/g : /\\([\\"])/g, "$1");
}

function formatParameter(value: string): string {
  return /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(value)
    ? value
    : `"${value.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}

/**
 * Extensible semantic-normalization seam for registered media parameters.
 * Unknown parameters intentionally retain bytewise comparison. Charset is
 * case-insensitive; a syntactically valid nested media `type` parameter is
 * compared by that media type's own semantic identity.
 */
function normalizeMediaParameterValue(name: string, value: string): string {
  if (name === "charset") return value.toLowerCase();
  if (name === "type") {
    try { return parseMediaType(value, true).identity; } catch { /* unknown/non-media value stays bytewise */ }
  }
  return value;
}

/** Supported request-body families. The artifact gives them no preference order. */
export const FAMILY_JSON = "json";
export const FAMILY_MULTIPART = "multipart";
export const FAMILY_URLENCODED = "urlencoded";
export const FAMILY_TEXT = "text";
export const FAMILY_RAW = "raw";
export const FAMILY_SEQUENTIAL = "sequential";

type MediaSchema = Record<string, unknown> | boolean | null;

export interface RequestPlanningOptions {
  /** Defaults to routed carriage for direct artifact-analysis callers. */
  profile?: OpenAPIExecutionProfile;
  openapiVersion?: string;
  /** Invocation-only: retain valid but unsupported declarations so selection ranks them before refusal. */
  inventoryUnsupported?: boolean;
}

/**
 * The pre-dispatch answer to the request-carriage questions: the selected
 * media type, its family, and the flatten mode its schema implies (§9.1:
 * object schemas flatten by property name; non-object schemas ride the
 * synthetic `body` property, unwrapped at the wire).
 */
export interface BodyPlan {
  declared: boolean;
  required: boolean;
  /** The declared content key, verbatim. */
  mediaKey: string;
  /** Canonical parsed declaration, including identity-affecting parameters. */
  mediaType: string;
  media: OpenAPIMediaType | null;
  family: string;
  synthetic: boolean;
  /** True when a body rides as one complete application value under a public payload field. */
  wholeObject?: boolean;
  /** True while a revision-3 media range awaits a concrete requestMedia choice. */
  range?: boolean;
  /** True when the OBI boundary is a Base64 string representing raw wire octets. */
  rawBoundary?: boolean;
  rangeSpecificity?: 0 | 1;
  /** Valid declaration retained only so revision-3 invocation can rank before admitting carriage. */
  unsupported?: boolean;
  /** Threads the additive multipart semantics without changing direct legacy helper defaults. */
  revision3?: boolean;
  openapiVersion?: string;
  /** OpenAPI 3.2 sequential framing selected by the concrete media type. */
  sequentialKind?: OpenAPI32SequentialRequestKind;
  /** OpenAPI 3.2 positional multipart consumes one complete caller array. */
  positionalMultipart?: boolean;
  /** Declared top-level body property names (object mode). */
  props?: Set<string>;
}

const NO_BODY_PLAN: BodyPlan = {
  declared: false,
  required: false,
  mediaKey: "",
  mediaType: "",
  media: null,
  family: "",
  synthetic: false,
};

function hasRequestBody(op: OpenAPIOperation): boolean {
  return op.requestBody != null && typeof op.requestBody === "object";
}

/**
 * §9.2's degenerate media/schema combination refusal (OAPI-P-04): the
 * selected request media type has no OAS-defined wire form for the
 * declared body schema. A distinct class so synthesis (synthesize.ts) can
 * surface the same fact as the openapi.media_schema_mismatch warning
 * without re-deriving the selection. Mirrors the Go SDK's
 * degenerateMediaError.
 */
export class DegenerateMediaError extends Error {}

/**
 * Inventories ONE OAS content map and returns the keys that denote a parsed
 * media identity another key of the SAME map also denotes, each mapped to a
 * stable rendering of the identity they collide on.
 *
 * §9.2: two keys in one content map denoting the same parsed media type are a
 * normalized collision, and the defect CONFINES to that colliding parsed
 * identity -- the smallest unit that owns it. No first-key rule is invented:
 * every caller skips exactly the keys named here, so no request selection
 * lands on the colliding identity, no response match is governed by it, and it
 * is never advertised as an available response representation -- while the
 * map's non-colliding entries remain usable alternatives. The inventory never
 * spans two content maps, because a content map is the unit the rule names.
 */
export function normalizedMediaCollisions(
  content: Record<string, unknown> | undefined,
  revision3: boolean,
): Map<string, string> {
  const colliding = new Map<string, string>();
  if (!content) return colliding;
  const keys = Object.keys(content);
  if (keys.length < 2) return colliding;
  const members = new Map<string, string[]>();
  const rendering = new Map<string, string>();
  for (const key of keys) {
    let parsed: ParsedMediaType | ParsedMediaRange;
    try {
      parsed = parseMediaType(key, revision3);
    } catch {
      try {
        parsed = parseMediaRange(key, revision3);
      } catch {
        // An unparseable key denotes no identity, so it collides with
        // nothing. Whether it is itself a defect is its owner's question.
        continue;
      }
    }
    const existingMembers = members.get(parsed.identity);
    if (existingMembers) existingMembers.push(key);
    else members.set(parsed.identity, [key]);
    const existingRendering = rendering.get(parsed.identity);
    if (existingRendering === undefined || parsed.canonical < existingRendering) {
      rendering.set(parsed.identity, parsed.canonical);
    }
  }
  for (const [identity, group] of members) {
    if (group.length < 2) continue;
    for (const key of group) colliding.set(key, rendering.get(identity) ?? identity);
  }
  return colliding;
}

/** Whether a concrete media type denotes one of a map's colliding identities. */
function collidesWithNormalizedIdentity(
  colliding: Map<string, string>,
  parsed: ParsedMediaType,
  revision3: boolean,
): boolean {
  if (colliding.size === 0) return false;
  for (const key of colliding.keys()) {
    try {
      if (parseMediaType(key, revision3).identity === parsed.identity) return true;
    } catch {
      continue;
    }
  }
  return false;
}

/**
 * Compatibility convenience for callers that need one SDK-local candidate.
 * Invocation uses planRequestBodies so the binding layer preserves every
 * artifact-permitted alternative until configuration or admissibility chooses.
 */
export function planRequestBody(
  op: OpenAPIOperation,
  options: RequestPlanningOptions = {},
): BodyPlan {
  return planRequestBodies(op, options)[0] ?? { ...NO_BODY_PLAN };
}

/** Preserves all concrete supported request candidates without binding-spec preference. */
export function planRequestBodies(
  op: OpenAPIOperation,
  options: RequestPlanningOptions = {},
): BodyPlan[] {
  if (!hasRequestBody(op)) return [];
  const rb = op.requestBody!;
  const content = rb.content;
  if (!content || Object.keys(content).length === 0) return [];

  const profile = options.profile ?? OPENAPI_PROFILE_ROUTED;
  const revision3 = hasMediaFidelity(profile);
  const schemaOmittedOAS30Bytes = hasSchemaOmittedOAS30ByteCarriage(profile);
  const wholeJSON = hasWholeJSONCarriage(profile);
  const openapiVersion = options.openapiVersion ?? "3.0";
  interface Candidate {
    key: string;
    parsed: ParsedMediaType | ParsedMediaRange;
    family: string;
    range: boolean;
    rawOnlyRange?: boolean;
    unsupported?: boolean;
    sequentialKind?: OpenAPI32SequentialRequestKind;
    positionalMultipart?: boolean;
  }
  const candidates: Candidate[] = [];
  const declared: string[] = [];
  const collided = new Set<string>();
  let nonColliding = 0;
  const colliding = normalizedMediaCollisions(content, revision3);
  for (const key of Object.keys(content)) {
    let parsed: ParsedMediaType | ParsedMediaRange;
    let range = false;
    try {
      parsed = parseMediaType(key, revision3);
    } catch {
      if (!revision3) {
        declared.push(key);
        continue;
      }
      try {
        parsed = parseMediaRange(key, revision3);
        range = true;
      } catch {
        declared.push(key);
        continue;
      }
    }
    declared.push(parsed.canonical);
    const collidingIdentity = colliding.get(key);
    if (collidingIdentity !== undefined) {
      // §9.2 normalized collision, confined: no request selection may land on
      // this parsed identity, so the key contributes no candidate and its
      // alternative is an accounted exclusion. The map's non-colliding
      // entries are unaffected (OAPI-P-04).
      collided.add(collidingIdentity);
      continue;
    }
    nonColliding += 1;
    if (range) {
      const families = supportedRangeCarriageFamilies(
        parsed as ParsedMediaRange,
        content[key] ?? null,
        openapiVersion,
      );
      if (families.size === 0) {
        if (options.inventoryUnsupported) {
          candidates.push({ key, parsed, family: "", range: true, unsupported: true });
        }
        continue;
      }
      candidates.push({
        key,
        parsed,
        family: "",
        range: true,
        rawOnlyRange: families.size === 1 && families.has(FAMILY_RAW),
      });
      continue;
    }
    const media = content[key] ?? null;
    const openapi32 = openapiVersion === "3.2.0"
      ? openAPI32RequestMediaAdmission(parsed.base, media)
      : { handled: false };
    if (openapi32.handled) {
      if (!openapi32.error && openapi32.family) {
        candidates.push({
          key,
          parsed,
          family: openapi32.family,
          range: false,
          ...(openapi32.sequentialKind ? { sequentialKind: openapi32.sequentialKind } : {}),
          ...(openapi32.positionalMultipart ? { positionalMultipart: true } : {}),
        });
      } else if (options.inventoryUnsupported) {
        candidates.push({ key, parsed, family: "", range: false, unsupported: true });
      }
      continue;
    }
    const family = concreteBodyFamily(
      parsed.base,
      mediaSchema(media),
      openapiVersion,
      revision3,
      revision3,
      schemaOmittedOAS30Bytes,
    );
    if (family) candidates.push({ key, parsed, family, range: false });
    else if (options.inventoryUnsupported) candidates.push({ key, parsed, family: "", range: false, unsupported: true });
  }
  if (candidates.length === 0) {
    if (collided.size > 0 && nonColliding === 0) {
      throw new Error(
        `every request content declaration denotes a normalized-colliding parsed media identity, so no selection may land on one (colliding: ${[...collided].sort().join(", ")})`,
      );
    }
    declared.sort();
    throw new Error(
      `request body declares no media type whose declaration selects a request carriage lane the registered OpenAPI binding family defines (declared: ${declared.join(", ")})`,
    );
  }
  candidates.sort((a, b) => a.parsed.identity.localeCompare(b.parsed.identity));
  const plans: BodyPlan[] = [];
  const rejected: string[] = [];
  for (const candidate of candidates) {
    try {
      const plan = buildBodyPlan(rb.required === true, content, candidate, openapiVersion, revision3, wholeJSON);
      if (hasDynamicObjectCarriage(profile)) applyDynamicObjectShape(plan);
      plans.push(plan);
    } catch (error: unknown) {
      if (options.inventoryUnsupported) {
        plans.push(buildBodyPlan(
          rb.required === true,
          content,
          { ...candidate, family: "", unsupported: true },
          openapiVersion,
          revision3,
        ));
      }
      rejected.push(error instanceof Error ? error.message : String(error));
    }
  }
  if (plans.length === 0) throw new DegenerateMediaError(rejected.join("; "));
  return plans;
}

function applyDynamicObjectShape(plan: BodyPlan): void {
  if (plan.synthetic || plan.unsupported) return;
  const raw = mediaSchema(plan.media);
  const schema = raw && typeof raw === "object" && !Array.isArray(raw) ? raw : null;
  const oas30 = (plan.openapiVersion ?? "3.0").startsWith("3.0");
  if (schema === null || !hasExplicitDynamicProperties(schema, new Set(), oas30)) return;
  plan.wholeObject = true;
  plan.props = undefined;
}

/**
 * Reports §9.1's explicitly dynamic object. The trigger keywords are read
 * under the GOVERNING EDITION'S dialect: `patternProperties` is not in the
 * 3.0 line's Schema Object at all, so there it decides as if absent and the
 * 3.0-line trigger is an explicit `additionalProperties` alone.
 */
function hasExplicitDynamicProperties(
  schema: Record<string, unknown>,
  seen: Set<Record<string, unknown>>,
  oas30: boolean,
): boolean {
  if (seen.has(schema)) return false;
  seen.add(schema);
  try {
    const patterns = oas30 ? null : asObject(schema.patternProperties);
    if (patterns && Object.keys(patterns).length > 0) return true;
    if (Object.hasOwn(schema, "additionalProperties") && schema.additionalProperties !== false) {
      return true;
    }
    return Array.isArray(schema.allOf) && schema.allOf.some((member) => {
      const nested = asObject(member);
      return nested !== null && hasExplicitDynamicProperties(nested, seen, oas30);
    });
  } finally {
    seen.delete(schema);
  }
}

/**
 * Proves that a range contains at least one concrete media type for which
 * revision 3 already defines carriage under this declaration's schema.
 * This is an existence check, not selection: invocation still requires an
 * actual configured concrete media type. A representative non-JSON member
 * may prove that the declaration can enter a raw lane, but it never becomes
 * a selected or emitted media type.
 */
export function rangeHasSupportedCarriage(
  range: ParsedMediaRange,
  media: OpenAPIMediaType | null,
  openapiVersion: string,
): boolean {
  return supportedRangeCarriageFamilies(range, media, openapiVersion).size > 0;
}

function supportedRangeCarriageFamilies(
  range: ParsedMediaRange,
  media: OpenAPIMediaType | null,
  openapiVersion: string,
): Set<string> {
  const schema = mediaSchema(media);
  const bases = representativeRangeMembers(range);
  const supported = new Set<string>();
  for (const base of bases) {
    const openapi32 = openapiVersion === "3.2.0"
      ? openAPI32RequestMediaAdmission(base, media)
      : { handled: false };
    if (openapi32.handled && (openapi32.error || !openapi32.family)) continue;
    const family = openapi32.handled
      ? openapi32.family!
      : concreteBodyFamily(base, schema, openapiVersion, true, true);
    if (!family) continue;
    const suffix = range.canonical.slice(range.base.length);
    let parsed: ParsedMediaType;
    try { parsed = parseMediaType(`${base}${suffix}`, true); } catch { continue; }
    try {
      buildBodyPlan(
        false,
        { [range.canonical]: media ?? {} },
        {
          key: range.canonical,
          parsed,
          family,
          range: false,
          ...(openapi32.sequentialKind ? { sequentialKind: openapi32.sequentialKind } : {}),
          ...(openapi32.positionalMultipart ? { positionalMultipart: true } : {}),
        },
        openapiVersion,
        true,
      );
      supported.add(family);
    } catch {
      // Try another concrete member/family before declaring the range inert.
    }
  }
  return supported;
}

function representativeRangeMembers(range: ParsedMediaRange): string[] {
  const type = range.base.slice(0, range.base.indexOf("/"));
  const bases: string[] = [];
  const add = (base: string): void => {
    if ((range.base === "*/*" || base.startsWith(`${type}/`)) && !bases.includes(base)) {
      bases.push(base);
    }
  };
  add("application/json");
  add("application/jsonl");
  add("application/json-seq");
  add("multipart/form-data");
  add("application/x-www-form-urlencoded");
  add("text/plain");
  add(`${type === "*" ? "application" : type}/x+json`);
  add(`${type === "*" ? "application" : type}/x-openbindings-representative`);
  return bases;
}

function buildBodyPlan(
  required: boolean,
  content: Record<string, OpenAPIMediaType>,
  candidate: {
    key: string;
    parsed: ParsedMediaType | ParsedMediaRange;
    family: string;
    range: boolean;
    rawOnlyRange?: boolean;
    unsupported?: boolean;
    sequentialKind?: OpenAPI32SequentialRequestKind;
    positionalMultipart?: boolean;
  },
  openapiVersion: string,
  revision3: boolean,
  wholeJSON = false,
): BodyPlan {
  const plan: BodyPlan = {
    declared: true,
    required,
    mediaKey: candidate.key,
    mediaType: candidate.parsed.canonical,
    media: content[candidate.key] ?? null,
    family: candidate.family,
    synthetic: false,
    range: candidate.range,
    unsupported: candidate.unsupported === true,
    revision3,
    openapiVersion,
    ...(candidate.sequentialKind ? { sequentialKind: candidate.sequentialKind } : {}),
    ...(candidate.positionalMultipart ? { positionalMultipart: true } : {}),
  };
  const schema = mediaSchema(plan.media);
  const objectSchema = schema && typeof schema === "object" ? schema : null;
  const declarationComplexJSON = wholeJSON
    && !candidate.range
    && candidate.family === FAMILY_JSON
    && objectSchema !== null
    && requiresWholeJSONCarriage(objectSchema, new Set(), openapiVersion.startsWith("3.0"));
  const shape = declarationComplexJSON
    ? { object: false, props: new Set<string>() }
    : typeof schema === "boolean"
    ? { object: false, props: new Set<string>() }
    : resolvedBodyShape(objectSchema, new Set(), openapiVersion.startsWith("3.0"));
  if (plan.unsupported) {
    plan.synthetic = schema === null || !shape.object;
    if (candidate.range) plan.rangeSpecificity = (candidate.parsed as ParsedMediaRange).specificity;
    if (!plan.synthetic && shape.props.size > 0) plan.props = shape.props;
    return plan;
  }
  if (candidate.range) {
    plan.synthetic = schema === null || !shape.object;
    plan.rangeSpecificity = (candidate.parsed as ParsedMediaRange).specificity;
    plan.rawBoundary = candidate.rawOnlyRange === true;
    if (!plan.synthetic && shape.props.size > 0) plan.props = shape.props;
    return plan;
  }
  if (candidate.family === FAMILY_SEQUENTIAL) {
    plan.synthetic = true;
    return plan;
  }
  if (candidate.family === FAMILY_MULTIPART && candidate.positionalMultipart) {
    plan.synthetic = true;
    return plan;
  }
  if (revision3 && candidate.family === FAMILY_TEXT) {
    requireSupportedCharset(candidate.parsed, `request media ${plan.mediaType}`);
  }
  if (candidate.family === FAMILY_JSON) {
    if (declarationComplexJSON) {
      plan.wholeObject = true;
    } else {
      plan.synthetic = revision3 ? schema === null || !shape.object : schema !== null && !shape.object;
    }
  } else if (candidate.family === FAMILY_MULTIPART || candidate.family === FAMILY_URLENCODED) {
    // Revision 3 §9.1 defines neither declared property routes nor a whole
    // body expansion for schema-omitted form declarations. Guessing fields
    // from the caller value would add a binding-private rule, so @3 fails
    // closed until that rule is specified by a future binding revision.
    if (revision3 && schema === null) {
      throw new Error(`request media candidate ${plan.mediaType} omits the object schema required for faithful form routing`);
    }
    if (schema !== null && !shape.object) {
      throw new Error(`request media candidate ${plan.mediaType} has a non-object body schema and is inadmissible`);
    }
    if (revision3 && candidate.family === FAMILY_MULTIPART && openapiVersion !== "3.2.0") {
      validateRevision3Multipart(plan.media, objectSchema, openapiVersion);
    } else if (revision3 && candidate.family === FAMILY_URLENCODED && openapiVersion !== "3.2.0") {
      validateRevision3URLEncoded(plan.media, objectSchema, openapiVersion);
    }
  } else if (candidate.family === FAMILY_TEXT) {
    if (schema !== null && shape.object) {
      throw new Error(`request media candidate ${plan.mediaType} has an object body schema and is inadmissible`);
    }
    plan.synthetic = true;
  } else if (candidate.family === FAMILY_RAW) {
    plan.synthetic = true;
    plan.rawBoundary = revision3 && candidate.family === FAMILY_RAW;
  }
  if (!plan.synthetic && shape.props.size > 0) plan.props = shape.props;
  return plan;
}

/**
 * Reports top-level JSON Schema applicators whose complete validation and
 * possible object surface cannot be preserved by projecting a fixed set of
 * named body properties. Only `allOf` is traversed because nested property
 * schemas do not alter the top-level route shape.
 *
 * The keywords are read under the GOVERNING EDITION'S dialect (§9.1). The
 * 3.0 line's Schema Object carries `oneOf`, `anyOf` and `not` only, so
 * `if`/`then`/`else`, `dependentSchemas` and `unevaluatedProperties` are
 * strictly-unsupported keywords there and decide as if absent.
 *
 * `unevaluatedProperties` is a PRESENCE trigger — "an explicit
 * `unevaluatedProperties` (any value)" — so the `false` spelling triggers
 * exactly like `true` or a Schema Object.
 */
function requiresWholeJSONCarriage(
  schema: Record<string, unknown>,
  seen: Set<Record<string, unknown>>,
  oas30: boolean,
): boolean {
  if (seen.has(schema)) return false;
  seen.add(schema);
  try {
    if (Array.isArray(schema.oneOf) || Array.isArray(schema.anyOf) || schema.not !== undefined) {
      return true;
    }
    const dependentSchemas = asObject(schema.dependentSchemas);
    if (
      !oas30
      && (schema.if !== undefined
        || schema.then !== undefined
        || schema.else !== undefined
        || (dependentSchemas !== null && Object.keys(dependentSchemas).length > 0)
        || Object.hasOwn(schema, "unevaluatedProperties"))
    ) return true;
    return Array.isArray(schema.allOf) && schema.allOf.some((member) => {
      const nested = asObject(member);
      return nested !== null && requiresWholeJSONCarriage(nested, seen, oas30);
    });
  } finally {
    seen.delete(schema);
  }
}

function validateRevision3URLEncoded(
  media: OpenAPIMediaType | null,
  schema: Record<string, unknown> | null,
  openapiVersion: string,
): void {
  if (schema === null) return;
  const encoding = asObject(media?.encoding) ?? {};
  for (const [name, rawProperty] of Object.entries(resolvedMultipartPropertySchemas(schema, new Set()))) {
    const { schema: property } = effectiveRevision3PartSchema(rawProperty, openapiVersion.startsWith("3.0"));
    if (property === false || property === null) continue; // an unsatisfiable property has no admissible runtime value
    const enc = asObject(encoding[name]);
    if (Object.hasOwn(encoding, name) && enc === null) {
      throw new Error(`urlencoded property ${JSON.stringify(name)} declares an invalid Encoding Object`);
    }
    if (Object.hasOwn(enc ?? {}, "contentType") && typeof enc?.contentType !== "string") {
      throw new Error(`urlencoded property ${JSON.stringify(name)} declares a non-string encoding.contentType`);
    }
    if (hasExplicitMultipartExpansion(enc)) {
      validateFormStyle(name, property, enc, "urlencoded", openapiVersion.startsWith("3.0"));
      continue;
    }
    if (openapiVersion.startsWith("3.0") && binarySignaled(property, true)) {
      throw new Error(
        `urlencoded binary property ${JSON.stringify(name)} has no Base64 boundary in the selected execution profile`,
      );
    }
    // R4 (ratified 2026-09-01): an array riding this lane whole, whose
    // item-type default defines no serialization for a container, stays an
    // ADMISSIBLE candidate carrying a required `propertyMedia` choice.
    // Rejecting the media candidate here would make the operation unreachable
    // where the specification says one consumer choice makes it dispatchable,
    // and would state a whole-source verdict on a cell that has a stated
    // remedy.
    if (
      typeof enc?.contentType !== "string"
      && urlencodedArrayNeedsPropertyMedia(property, openapiVersion.startsWith("3.0"))
    ) {
      continue;
    }
    validateContentBasedMedia(name, property, enc, openapiVersion.startsWith("3.0"), "urlencoded");
  }
}

const SUPPORTED_CHARSETS = new Set(["utf-8", "us-ascii", "iso-8859-1"]);

function requireSupportedCharset(
  parsed: Pick<ParsedMediaType, "params">,
  subject: string,
): void {
  const charset = parsed.params["charset"];
  if (charset !== undefined && !SUPPORTED_CHARSETS.has(charset.toLowerCase())) {
    throw new Error(`${subject} declares unsupported charset ${JSON.stringify(charset)}`);
  }
}

// hasExplicitMultipartExpansion reports whether an Encoding Object writes an
// EXPLICIT RFC6570-style serialization control. That, and nothing else,
// selects the RFC6570-style lane for a form property; with all three fields
// absent the property takes the CONTENT lane, whose contentType default
// depends on the property's own declared type.
//
// The predicate is EDITION-INDEPENDENT across all eight accepted editions, and
// that uniformity is an incorporated consequence rather than a simplification.
//
//	3.0.4        Section 4.7.15.1.2: "whenever any of style, explode, or
//	             allowReserved are present with an explicit value: The value of
//	             contentType, whether it is explicitly defined or has the
//	             default value, is to be ignored ... However, if all three of
//	             style, explode, and allowReserved fields are absent, it is
//	             RECOMMENDED that: All three keywords are to be entirely
//	             ignored ... Encoding is to be based on contentType alone".
//	3.1.0        Section 4.8.15.1 carries, on EACH of style, explode and
//	             allowReserved: "If a value is explicitly defined, then the
//	             value of contentType (implicit or explicit) SHALL be ignored."
//	3.1.1, 3.1.2 Section 4.8.15.1.2 ("Fixed Fields for RFC6570-style
//	             Serialization") carries that same sentence on each field.
//	3.0.0-3.0.3  State no lane-selection sentence of their own. Their OWN
//	             section 4.1 supplies the rule instead: "Tooling which supports
//	             OAS 3.0 SHOULD be compatible with all OAS 3.0.* versions. The
//	             patch version SHOULD NOT be considered by tooling, making no
//	             distinction between 3.0.0 and 3.0.1 for example." 3.0.4 is a
//	             3.0.* version, so the 3.0 line takes ONE behavior, and only
//	             the content lane is consistent with 3.0.4 read under its own
//	             text -- which section 2 of the binding specification equally
//	             requires.
//
// So this function does NOT read the document's openapi field, and no engine
// here keys any urlencoded behavior on the patch component of a version. Until
// 2026-08-17 a legacyOpenAPIFormEncoding(3.0.0|3.0.1|3.0.2|3.0.3)
// predicate did exactly that. It was the ONLY patch-version-keyed behavioural
// switch in the three engines; it discarded an explicitly written
// encoding.contentType, collided sibling field names, and refused at dispatch
// for values the artifact had fully declared. It is deleted, and
// The family documents' edition surface states the patch-uniformity reading
// once. Package: design/openapi-30-urlencoded-default-lane-ruling.md.
//
// Reproduce the per-edition presence pattern (edition order 3.0.0, 3.0.1,
// 3.0.2, 3.0.3, 3.0.4, 3.1.0, 3.1.1, 3.1.2):
//
//	node corpus-lab/scripts/strip-oas-html.mjs --count \
//	  "If a value is explicitly defined, then the value of contentType \
//	   \(implicit or explicit\) SHALL be ignored"             -> 0/0/0/0/0/3/3/3
//	node corpus-lab/scripts/strip-oas-html.mjs --count \
//	  "Encoding is to be based on contentType alone"          -> 0/0/0/0/1/0/0/0
//	node corpus-lab/scripts/strip-oas-html.mjs --count \
//	  "The patch version SHOULD NOT be considered by tooling" -> 1/1/1/1/1/1/1/1
//
// (each --count is one line). The digests of the renderings those counts run
// over are recorded in corpus-lab/OPENAPI-RUNTIME-INDEX.md.
//
// NOT the discriminator, and the reason an earlier comment here was wrong:
// "the default serialization strategy of such properties is described in the
// Encoding Object's style property as form", which is present in 3.0.0-3.0.3
// AND 3.1.0 and absent from 3.0.4, 3.1.1 and 3.1.2, so it partitions nothing.
//
// Pinned by the 64-cell twin table in urlencoded-lane-partition.test.ts and
// the 80-cell body table in urlencoded-content-path.test.ts, both shared
// byte-for-byte with the other two engines.
function hasExplicitMultipartExpansion(enc: Record<string, unknown> | null): boolean {
  return enc !== null && (
    Object.hasOwn(enc, "style")
    || Object.hasOwn(enc, "explode")
    || Object.hasOwn(enc, "allowReserved")
  );
}


function validateFormStyle(
  name: string,
  schema: Record<string, unknown>,
  enc: Record<string, unknown> | null,
  subject: string,
  is30: boolean,
): void {
  if (Object.hasOwn(enc ?? {}, "style") && (typeof enc?.style !== "string" || enc.style === "")) {
    throw new Error(`${subject} property ${JSON.stringify(name)} declares an invalid style`);
  }
  if (Object.hasOwn(enc ?? {}, "explode") && typeof enc?.explode !== "boolean") {
    throw new Error(`${subject} property ${JSON.stringify(name)} declares a non-boolean explode`);
  }
  if (Object.hasOwn(enc ?? {}, "allowReserved") && typeof enc?.allowReserved !== "boolean") {
    throw new Error(`${subject} property ${JSON.stringify(name)} declares a non-boolean allowReserved`);
  }
  const style = typeof enc?.style === "string" && enc.style !== "" ? enc.style : "form";
  const explode = typeof enc?.explode === "boolean" ? enc.explode : style === "form";
  const types = declaredSchemaTypes(schema);
  if (style === "spaceDelimited" || style === "pipeDelimited") {
    if (explode || types.length === 0 || types.some((type) => type !== "array")) {
      throw new Error(
        `${subject} property ${JSON.stringify(name)} uses ${style}, which is defined only for arrays with explode=false`,
      );
    }
  } else if (style === "deepObject") {
    if (!explode || types.length === 0 || types.some((type) => type !== "object")) {
      throw new Error(
        `${subject} property ${JSON.stringify(name)} uses deepObject, which is defined only for objects with explode=true`,
      );
    }
  } else if (style !== "form") {
    throw new Error(`${subject} property ${JSON.stringify(name)} declares unsupported style ${JSON.stringify(style)}`);
  }
  const member = styleLaneUndefinedExpansionMember(schema, is30);
  if (member !== null) {
    throw new Error(
      `${subject} property ${JSON.stringify(name)} member ${JSON.stringify(name + member)} has no expansion defined for style ${style}`,
    );
  }
}

/**
 * Reports the first DECLARED member of a resolved RFC6570-style-lane schema
 * whose expansion the governing OAS style row leaves undefined. Answers "[]"
 * for an array's items, ".<name>" for an object's property, and null when no
 * declared member offends. Members are visited in code-point order, so both
 * engines name the same one.
 *
 * A member offends when its resolved schema is `object` or `array`. Every
 * style this lane serves expands a composite value exactly one level: each
 * member becomes a member STRING. A member that is itself composite has no
 * representation, and the refusal is decided by the DECLARATION, not by a
 * supplied value — every value conforming to such a declaration is composite,
 * so an operation admitted here would be one this candidate is statically
 * guaranteed to refuse. That is why the exclusion is accounted at synthesis
 * rather than raised at invocation.
 *
 * WHERE THE AUTHORITY SPEAKS, AND WHERE IT DOES NOT. Read per edition, because
 * section 2 of the binding specification reads every accepted edition under its
 * own immutable text and does not aggregate them. The style table is section
 * 4.7.12.3 "Style Values" on the 3.0 line and section 4.8.12.3 on the 3.1 line.
 *
 *	form              Every accepted edition's row cites the incorporated
 *	                  authority by section: "Form style parameters defined by
 *	                  [RFC6570] Section 3.2.8." RFC 6570 section 2.4.2 states
 *	                  that an explode modifier applies "the expansion process
 *	                  ... to each member of the composite as if it were listed
 *	                  as a separate variable", and the expansions it defines
 *	                  append member strings. A member that is itself a list or
 *	                  an associative array has no expansion there.
 *	spaceDelimited,   No accepted edition's row cites any RFC section. The
 *	pipeDelimited     rows read "Space separated array values" / "Pipe
 *	                  separated array values" on 3.0.0 through 3.0.3, and
 *	                  "... array values or object properties and values" on
 *	                  3.0.4 and the 3.1 line.
 *	deepObject        No accepted edition's row cites any RFC section either,
 *	                  and the row's own text differs by edition:
 *
 *	                    3.0.0, 3.0.1, 3.0.2, 3.0.3, 3.1.0
 *	                      "deepObject | object | query | Provides a simple way
 *	                      of rendering nested objects using form parameters."
 *	                      The row GOVERNS this case and does not DEFINE it.
 *	                      The worked example beside it has scalar members only
 *	                      (color[R]=100&color[G]=200&color[B]=150).
 *
 *	                    3.0.4, 3.1.1, 3.1.2
 *	                      "Allows objects with scalar properties to be
 *	                      represented using form parameters. The
 *	                      representation of array or object properties is not
 *	                      defined."
 *
 * The later editions say in words what the earlier ones leave undefined; on no
 * accepted edition does an authority this specification incorporates supply a
 * representation for a composite member. So this function refuses and the
 * synthesizer accounts the exclusion. Nothing here authors bytes for the
 * member: an interpretation choice for these cells remains available to a
 * future revision and is deliberately not taken.
 *
 * Reproduce the per-edition presence pattern (edition order 3.0.0, 3.0.1,
 * 3.0.2, 3.0.3, 3.0.4, 3.1.0, 3.1.1, 3.1.2):
 *
 *	node corpus-lab/scripts/strip-oas-html.mjs --count \
 *	  "Provides a simple way of rendering nested objects using form \
 *	   parameters"                                           -> 1/1/1/1/0/1/0/0
 *	node corpus-lab/scripts/strip-oas-html.mjs --count \
 *	  "The representation of array or object properties is not defined"
 *	                                                         -> 0/0/0/0/1/0/1/1
 *	node corpus-lab/scripts/strip-oas-html.mjs --count \
 *	  "Form style parameters defined by \[RFC6570\] Section 3\.2\.8"
 *	                                                         -> 1/1/1/1/1/1/1/1
 *
 * (each --count is one line). The digests of the renderings those counts run
 * over are recorded in corpus-lab/OPENAPI-RUNTIME-INDEX.md.
 *
 * Pinned by the shared style-lane-composite-member-cases.json case table,
 * carried byte-for-byte by the other two engines. Package:
 * design/openapi-style-lane-composite-member-ruling.md, RULED 2026-08-18.
 */
export function styleLaneUndefinedExpansionMember(
  schema: Record<string, unknown> | boolean | null,
  is30: boolean,
): string | null {
  const resolved = collapsedStyleLaneSchema(schema, is30);
  if (resolved === null) return null;
  if (schemaTypeIs(resolved, "array")) {
    const items = collapsedStyleLaneSchema(resolvedMultipartItems(resolved), is30);
    return styleLaneCompositeMember(items) ? "[]" : null;
  }
  if (!schemaTypeIs(resolved, "object")) return null;
  const properties = resolvedMultipartPropertySchemas(resolved, new Set());
  for (const name of Object.keys(properties).sort(codePointCompare)) {
    const member = collapsedStyleLaneSchema(properties[name] ?? null, is30);
    if (styleLaneCompositeMember(member)) return `.${name}`;
  }
  return null;
}

/**
 * A resolved member schema declaring a composite JSON value. A member
 * declaring no type is NOT composite by declaration: its runtime value may be
 * a scalar, and a declaration-keyed refusal must not reach a declaration that
 * admits one.
 */
function styleLaneCompositeMember(schema: Record<string, unknown> | null): boolean {
  if (schema === null) return false;
  const types = declaredSchemaTypes(schema);
  return types.length > 0 && types.every((type) => type === "object" || type === "array");
}

/**
 * Applies §9.2's single-non-null-branch and union-type collapses before the
 * member kinds are read, so the nullable spelling of a declaration is
 * classified as the declaration it restates. It is the same collapse
 * effectiveRevision3PartSchema applies, reached through that function so the
 * family holds one implementation of it.
 */
function collapsedStyleLaneSchema(
  schema: Record<string, unknown> | boolean | null,
  is30: boolean,
): Record<string, unknown> | null {
  let current = schema;
  for (let i = 0; current !== null && i < 8; i++) {
    const literal = booleanSchemaLiteral(current);
    if (literal === true) return {}; // the same unconstrained declaration as {}
    if (literal === false) return null; // an unsatisfiable member has no admissible runtime value
    const { schema: collapsed } = effectiveRevision3PartSchema(current, is30);
    if (collapsed === false || collapsed === null) return null;
    if (collapsed === current) return collapsed;
    current = collapsed;
  }
  return typeof current === "object" && current !== null ? current : null;
}

function declaredSchemaTypes(schema: Record<string, unknown>): string[] {
  const result = new Set<string>();
  if (typeof schema.type === "string") result.add(schema.type);
  else if (Array.isArray(schema.type)) {
    for (const type of schema.type) if (typeof type === "string") result.add(type);
  }
  if (Array.isArray(schema.allOf)) {
    for (const member of schema.allOf) {
      const nested = asObject(member);
      if (nested !== null) for (const type of declaredSchemaTypes(nested)) result.add(type);
    }
  }
  return [...result];
}

function validateRevision3Multipart(
  media: OpenAPIMediaType | null,
  schema: Record<string, unknown> | null,
  openapiVersion: string,
): void {
  const encoding = asObject(media?.encoding) ?? {};
  for (const [name, rawEncoding] of Object.entries(encoding)) {
    const enc = asObject(rawEncoding);
    if (enc === null) {
      throw new Error(`multipart property ${JSON.stringify(name)} declares an invalid Encoding Object`);
    }
    const headers = asObject(enc.headers);
    if (Object.hasOwn(enc, "headers") && headers === null) {
      throw new Error(`multipart property ${JSON.stringify(name)} declares invalid encoding.headers`);
    }
    if (headers !== null && Object.keys(headers).length > 0) {
      throw new Error(
        `multipart property ${JSON.stringify(name)} declares encoding.headers, for which the selected execution profile defines no caller source mapping`,
      );
    }
    if (Object.hasOwn(enc, "contentType") && typeof enc.contentType !== "string") {
      throw new Error(`multipart property ${JSON.stringify(name)} declares a non-string encoding.contentType`);
    }
    if (!hasExplicitMultipartExpansion(enc) && typeof enc.contentType === "string") {
      parseSingleMultipartContentType(enc.contentType, name);
    }
  }
  if (schema === null) return;
  for (const [name, rawProperty] of Object.entries(resolvedMultipartPropertySchemas(schema, new Set()))) {
    const { schema: property } = effectiveRevision3PartSchema(rawProperty, openapiVersion.startsWith("3.0"));
    if (property === false || property === null) continue; // an unsatisfiable property has no admissible runtime value
    validateContentTransferEncoding(name, property);
    const enc = asObject(encoding[name]);
    if (!openapiVersion.startsWith("3.0") && hasExplicitMultipartExpansion(enc)) {
      validateFormStyle(name, property, enc, "multipart", openapiVersion.startsWith("3.0"));
    }
    let contentSchema = property;
    if (schemaTypeIs(property, "array") && !hasExplicitMultipartExpansion(enc)) {
      const rawItems = resolvedMultipartItemsSchema(property);
      if (rawItems === false) continue;
      const items = rawItems === true || booleanSchemaLiteral(rawItems) === true
        ? {}
        : asObject(rawItems);
      if (items === null) {
        throw new Error(
          `multipart array property ${JSON.stringify(name)} has no items declaration for its default octet-stream parts in the selected execution profile`,
        );
      }
      if (schemaTypeIs(items, "array")) {
        throw new Error(
          `multipart array property ${JSON.stringify(name)} has nested array items with no declaration-defined repeated-part mapping`,
        );
      }
      validateContentTransferEncoding(name, items);
      contentSchema = items;
    }
    if (!hasExplicitMultipartExpansion(enc)) {
      validateContentBasedMedia(name, contentSchema, enc, openapiVersion.startsWith("3.0"), "multipart");
    }
  }
}

function validateContentBasedMedia(
  name: string,
  schema: Record<string, unknown>,
  enc: Record<string, unknown> | null,
  is30: boolean,
  subject: string,
): void {
  const declared = typeof enc?.contentType === "string"
    ? parseSingleMultipartContentType(enc.contentType, name)
    : null;
  if (declared === null && !hasDeclaredSchemaType(schema) && partSchemaDeclaresNoType(schema, is30)) {
    refuseTypelessPart(subject, name, is30, schema);
  }
  const selected = declared ?? parseMediaType(defaultMultipartContentType(schema, is30), true);
  requireSupportedCharset(selected, `${subject} property ${JSON.stringify(name)}`);
  if (isJSONMediaType(selected.base)) return;
  if (selected.base === "text/plain") {
    // The Go twin's rule: the resolved declaration must BE one of the
    // primitive types text/plain can carry. A union that survived the
    // nullable collapse declares value-dependent alternatives and has no
    // single faithful carriage here either.
    if (
      !schemaTypeIs(schema, "string")
      && !schemaTypeIs(schema, "number")
      && !schemaTypeIs(schema, "integer")
      && !schemaTypeIs(schema, "boolean")
    ) {
      throw new Error(`${subject} property ${JSON.stringify(name)} cannot serialize its schema as text/plain`);
    }
    return;
  }
  const encodedString = artifactEncodedString(schema, is30);
  // OAS 3.0 format: binary declares raw part bytes; encoding.contentType
  // describes those bytes and is not restricted to application/octet-stream.
  // A ZIP/image/vendor part therefore uses the same canonical Base64 caller
  // boundary as the default octet-stream case.
  if (is30 && binarySignaled(schema, true)) return;
  // An artifact-encoded string part carries its characters as-is under
  // whatever content type the declaration or the default row named.
  if (encodedString) return;
  throw new Error(`${subject} property ${JSON.stringify(name)} has no serializer for ${selected.canonical}`);
}

function validateContentTransferEncoding(name: string, schema: Record<string, unknown>): void {
  const encoding = resolvedSchemaStringKeyword(schema, "contentEncoding");
  if (encoding !== "" && !HTTP_TOKEN.test(encoding)) {
    throw new Error(
      `multipart property ${JSON.stringify(name)} has contentEncoding ${JSON.stringify(encoding)} that cannot be emitted as Content-Transfer-Encoding`,
    );
  }
}

function resolvedMultipartItemsSchema(
  schema: Record<string, unknown>,
): Record<string, unknown> | boolean | null {
  if (typeof schema.items === "boolean") return schema.items;
  const direct = asObject(schema.items);
  if (direct !== null) return direct;
  if (Array.isArray(schema.allOf)) {
    const members = schema.allOf
      .map((member) => asObject(member))
      .filter((member): member is Record<string, unknown> => member !== null)
      .map((member) => resolvedMultipartItemsSchema(member))
      .filter((member): member is Record<string, unknown> | boolean => member !== null);
    if (members.includes(false)) return false;
    const objects = members.filter((member): member is Record<string, unknown> => typeof member === "object");
    if (objects.length > 0) return objects.length === 1 ? objects[0]! : { allOf: objects };
    if (members.includes(true)) return true;
  }
  return null;
}

function resolvedMultipartPropertySchemas(
  schema: Record<string, unknown>,
  seen: Set<Record<string, unknown>>,
): Record<string, Record<string, unknown> | boolean> {
  if (seen.has(schema)) return {};
  seen.add(schema);
  try {
    const result: Record<string, Record<string, unknown> | boolean> = {};
    const own = asObject(schema.properties);
    for (const [name, property] of Object.entries(own ?? {})) {
      if (typeof property === "boolean") result[name] = property;
      else {
        const object = asObject(property);
        if (object !== null) result[name] = object;
      }
    }
    if (Array.isArray(schema.allOf)) {
      for (const member of schema.allOf) {
        const nested = asObject(member);
        if (nested === null) continue;
        for (const [name, property] of Object.entries(resolvedMultipartPropertySchemas(nested, seen))) {
          const previous = result[name];
          if (previous === undefined) result[name] = property;
          else if (previous === false || property === false) result[name] = false;
          else if (previous === true) result[name] = property;
          else if (property !== true) result[name] = { allOf: [previous, property] };
        }
      }
    }
    return result;
  } finally {
    seen.delete(schema);
  }
}

function hasDeclaredSchemaType(schema: Record<string, unknown>): boolean {
  if (typeof schema.type === "string" && schema.type !== "") return true;
  if (Array.isArray(schema.type) && schema.type.some((type) => typeof type === "string" && type !== "")) return true;
  return Array.isArray(schema.allOf) && schema.allOf.some((member) => {
    const nested = asObject(member);
    return nested !== null && hasDeclaredSchemaType(nested);
  });
}

/**
 * The boolean schema literals in both spellings: the literal itself and the
 * structural encodings some toolchains emit for them — `{"anyOf":[{},
 * {"not":{}}]}` for true and `{"allOf":[{},{"not":{}}]}` for false. Returns
 * null for every ordinary schema.
 */
function booleanSchemaLiteral(schema: Record<string, unknown> | boolean | null): boolean | null {
  if (schema === true || schema === false) return schema;
  if (schema === null || Object.keys(schema).length !== 1) return null;
  for (const [keyword, literal] of [["anyOf", true], ["allOf", false]] as const) {
    const members = schema[keyword];
    if (!Array.isArray(members) || members.length !== 2) continue;
    const first = asObject(members[0]);
    const second = asObject(members[1]);
    if (first === null || Object.keys(first).length !== 0 || second === null || Object.keys(second).length !== 1) continue;
    const not = asObject(second.not);
    if (not !== null && Object.keys(not).length === 0) return literal;
  }
  return null;
}

/**
 * Applies the binding specification's part-schema interpretations before
 * carriage selection (§9.2). The boolean literal true is the same
 * unconstrained declaration as {}. A resolved top level declaring exactly
 * one anyOf/oneOf whose branches comprise one non-null branch beside
 * {type: "null"}-only branches collapses to that non-null branch — the
 * artifact declares exactly one carriage, so this is schema interpretation,
 * not branch selection — and `nullable` lets a JSON null value elide the
 * optional part. The union-type spelling of the same declaration takes the
 * same collapse; see collapsedNullableTypeMember.
 */
function effectiveRevision3PartSchema(
  schema: Record<string, unknown> | boolean | null,
  is30: boolean,
): { schema: Record<string, unknown> | false | null; nullable: boolean } {
  const literal = booleanSchemaLiteral(schema);
  if (literal === true) return { schema: {}, nullable: false };
  if (literal === false) return { schema: false, nullable: false };
  if (schema === null || typeof schema === "boolean") return { schema: null, nullable: false };
  const collapsed = collapsedNullableChoiceBranch(schema);
  if (collapsed !== null) return { schema: collapsed, nullable: true };
  const member = collapsedNullableTypeMember(schema, is30);
  if (member !== null) {
    // The sibling keywords are the same schema's own assertions and keep
    // applying: only the union spelling of the type is resolved.
    return { schema: { ...schema, type: member }, nullable: true };
  }
  return { schema, nullable: false };
}

/**
 * The union-type spelling of the same nullable declaration
 * collapsedNullableChoiceBranch handles. JSON Schema 2020-12 §6.1.1 defines
 * an array-valued `type` as a union — "If the value of `type` is an array,
 * then an instance validates successfully if its type matches any of the
 * types indicated by the strings in the array" — so
 * {"type": ["string", "null"]} asserts exactly what
 * {"anyOf": [{"type": "string"}, {"type": "null"}]} asserts, and takes the
 * same collapse to its single non-null member. Two or more non-null members
 * declare value-dependent alternatives, leave no single faithful part
 * carriage, and do not collapse.
 *
 * The union spelling belongs to the OAS 3.1 line, which adopts JSON Schema
 * 2020-12 wholesale. Every 3.0 edition states of the Schema Object: "type -
 * Value MUST be a string. Multiple types via an array are not supported"
 * (3.0.0 §Schema Object through 3.0.4, verbatim in all five). An
 * array-valued `type` under that line is therefore not a union declaration
 * this specification can read, and gains no part carriage from one.
 *
 * The collapse reads the resolved top level only, exactly as the choice
 * collapse does; a union reached through allOf is not rewritten.
 */
function collapsedNullableTypeMember(schema: Record<string, unknown>, is30: boolean): string | null {
  if (is30 || !Array.isArray(schema.type)) return null;
  const types = schema.type;
  if (types.length < 2) return null; // a single-member `type` already denotes that member
  let member: string | null = null;
  for (const candidate of types) {
    if (typeof candidate !== "string") return null;
    if (candidate === "null") continue;
    if (member !== null) return null;
    member = candidate;
  }
  return member; // null when "null" alone: no carriable value is declared
}

/**
 * A resolved top-level array-valued `type` that collapsedNullableTypeMember
 * declined, so a caller can say which of the two reasons applies instead of
 * reporting the schema as having no declared default.
 */
function nonCollapsingUnionType(schema: Record<string, unknown>): string[] | null {
  if (!Array.isArray(schema.type) || schema.type.length < 2) return null;
  return schema.type.map(entry => String(entry));
}

/**
 * The single-non-null-branch collapse: the schema's top level declares
 * exactly one of anyOf/oneOf, no type and no other applicator, and the
 * branches are exactly one non-null branch with every remaining branch
 * declaring {type: "null"} alone. A choice with more than one non-null
 * branch declares value-dependent alternatives and does not collapse.
 */
function collapsedNullableChoiceBranch(schema: Record<string, unknown>): Record<string, unknown> | null {
  const anyOf = Array.isArray(schema.anyOf) ? schema.anyOf : null;
  const oneOf = Array.isArray(schema.oneOf) ? schema.oneOf : null;
  if (anyOf !== null && oneOf !== null) return null;
  const branches = anyOf ?? oneOf;
  if (branches === null || branches.length < 2) return null;
  const dependentSchemas = asObject(schema.dependentSchemas);
  if (
    hasDeclaredSchemaType(schema)
    || Array.isArray(schema.allOf)
    || schema.not !== undefined
    || schema.if !== undefined
    || schema.then !== undefined
    || schema.else !== undefined
    || (dependentSchemas !== null && Object.keys(dependentSchemas).length > 0)
    || Object.keys(asObject(schema.properties) ?? {}).length > 0
    || schema.items !== undefined
  ) {
    return null;
  }
  let nonNull: Record<string, unknown> | null = null;
  for (const raw of branches) {
    const branch = asObject(raw);
    if (branch === null) return null;
    if (nullOnlyBranch(branch)) continue;
    if (nonNull !== null) return null;
    nonNull = branch;
  }
  return nonNull;
}

/** A branch declaring {type: "null"} alone (annotations aside): the null arm of the nullable-choice idiom. */
function nullOnlyBranch(schema: Record<string, unknown>): boolean {
  const types = declaredSchemaTypes(schema);
  if (types.length !== 1 || types[0] !== "null") return false;
  return !Array.isArray(schema.anyOf) && !Array.isArray(schema.oneOf) && !Array.isArray(schema.allOf)
    && schema.not === undefined && Object.keys(asObject(schema.properties) ?? {}).length === 0
    && schema.items === undefined
    && (!Array.isArray(schema.enum) || schema.enum.length === 0);
}

/**
 * §9.2: a resolved part schema that declares no `type` refuses before
 * dispatch on EVERY accepted edition, and no supplied value's JSON type ever
 * selects a part's carriage. The two lines reach that one outcome on
 * different grounds — the 3.1 editions state a default this revision defines
 * no boundary to cross, the 3.0 editions state no row at all and this
 * revision fills nothing — so the diagnostic names the ground the artifact's
 * own edition supplies.
 */
function refuseTypelessPart(
  subject: string,
  name: string,
  is30: boolean,
  schema: Record<string, unknown> | boolean | null,
): never {
  if (schema !== null && typeof schema === "object" && schema.type !== undefined) {
    // `type` is present but names no type at all.
    if (is30) {
      throw new Error(
        `${subject} property ${JSON.stringify(name)}: the part schema declares an array-valued \`type\`, but an OAS 3.0 \`type\` MUST be a string and multiple types via an array are not supported; no part carriage is defined`,
      );
    }
    // No accepted 3.1 default row reaches it — 3.1.1 and 3.1.2 key their
    // first row on `type` being ABSENT, and 3.1.0's catch-all keys on the
    // property type it does not have — and JSON Schema 2020-12's own
    // meta-schema requires an array-valued `type` to carry at least one
    // member, so the declaration admits no instance.
    throw new Error(
      `${subject} property ${JSON.stringify(name)}: the part schema declares an empty \`type\`, which no accepted OAS 3.1 default part Content-Type row reaches and which admits no instance; no part carriage is defined`,
    );
  }
  if (!is30) {
    // Every accepted 3.1 edition states a default for such a part, and all
    // three state application/octet-stream: 3.1.1 and 3.1.2 tabulate it as
    // the Encoding Object default table's `type`-absent row, and 3.1.0
    // reaches it through the total catch-all closing its prose enumeration.
    // This revision defines no JSON-to-octet part boundary, so it refuses.
    throw new Error(
      `${subject} property ${JSON.stringify(name)}: a part schema declaring no \`type\` defaults to application/octet-stream on every accepted OAS 3.1 edition, but this binding revision defines no JSON-to-octet part boundary`,
    );
  }
  // The 3.0 line states no row for it at all: 3.0.0 through 3.0.3 enumerate a
  // `string` with `format: binary`, "other primitive types", `object` and
  // `array` and close without a catch-all, and 3.0.4 tabulates the same cases
  // keyed on `type`. Every stated row is keyed on a declared `type` and none
  // reaches a declaration carrying none. This specification authors no row
  // for that residue, so the part refuses here too and its alternative is an
  // accounted exclusion.
  throw new Error(
    `${subject} property ${JSON.stringify(name)}: a part schema declaring no \`type\` has no default part Content-Type row on any accepted OAS 3.0 edition, and this binding revision authors none; no part carriage is defined`,
  );
}

/**
 * The §9.2 type-absent part cell: a resolved part declaration carrying no type
 * and no choice or conditional applicator, through allOf. Such a part refuses
 * before dispatch on every accepted edition. An absent schema is not a
 * declaration and is answered by the caller's own absent-schema branch.
 */
function partSchemaDeclaresNoType(schema: Record<string, unknown> | boolean | null, is30: boolean): boolean {
  const literal = booleanSchemaLiteral(schema);
  if (literal !== null) return literal;
  if (schema === null || typeof schema === "boolean") return false;
  if (hasDeclaredSchemaType(schema)) return false;
  const dependentSchemas = asObject(schema.dependentSchemas);
  if (
    Array.isArray(schema.oneOf)
    || Array.isArray(schema.anyOf)
    || schema.not !== undefined
    || schema.if !== undefined
    || schema.then !== undefined
    || schema.else !== undefined
    || (dependentSchemas !== null && Object.keys(dependentSchemas).length > 0)
    || schema.format === "binary"
  ) {
    return false;
  }
  // `contentEncoding` and `contentMediaType` belong to the 3.1 line's
  // dialect. A 3.1 schema carrying one and declaring no `type` reaches the
  // same refusal through the caller's own default-row path rather than this
  // predicate, so it is excluded here. (On a schema that DECLARES a
  // non-string type the keywords are inert annotations, [JSON Schema
  // 2020-12] Section 8.1/8.3/8.4, and the part keeps its own per-type row;
  // that case never reaches here because it has a declared type.) Under the
  // 3.0 line the Schema Object is Wright-Draft-00-based and neither keyword
  // is in its vocabulary, so an artifact carrying one has written an
  // annotation the dialect ignores and the part decides as if it were
  // absent — which is the type-absent cell, as the Go twin also reads it.
  if (
    !is30
    && (resolvedSchemaStringKeyword(schema, "contentEncoding") !== ""
      || resolvedSchemaStringKeyword(schema, "contentMediaType") !== "")
  ) {
    return false;
  }
  if (Array.isArray(schema.allOf)) {
    return schema.allOf.every((member) => {
      const nested = asObject(member);
      return nested === null || partSchemaDeclaresNoType(nested, is30);
    });
  }
  return true;
}

function parseSingleMultipartContentType(raw: string, name: string): ParsedMediaType {
  const members = splitCommaList(raw);
  if (members.length !== 1) {
    throw new Error(
      `multipart property ${JSON.stringify(name)} declares multiple encoding.contentType members, for which the selected execution profile defines no part-selection rule`,
    );
  }
  try {
    return parseMediaType(members[0]!, true);
  } catch (error: unknown) {
    throw new Error(
      `multipart property ${JSON.stringify(name)} encoding.contentType must be one concrete media type: ${error instanceof Error ? error.message : String(error)}`,
      { cause: error },
    );
  }
}

function splitCommaList(raw: string): string[] {
  const members: string[] = [];
  let start = 0;
  let quoted = false;
  let escaped = false;
  for (let index = 0; index < raw.length; index++) {
    const char = raw[index];
    if (escaped) escaped = false;
    else if (char === "\\" && quoted) escaped = true;
    else if (char === '"') quoted = !quoted;
    else if (char === "," && !quoted) {
      const member = raw.slice(start, index).trim();
      if (member !== "") members.push(member);
      start = index + 1;
    }
  }
  const member = raw.slice(start).trim();
  if (member !== "") members.push(member);
  return members;
}

function concreteBodyFamily(
  base: string,
  schema: MediaSchema,
  openapiVersion: string,
  revision3: boolean,
  allowRaw: boolean,
  allowSchemaOmittedOAS30Bytes = false,
): string {
  if (isJSONMediaType(base)) return FAMILY_JSON;
  if (base === "multipart/form-data") return FAMILY_MULTIPART;
  if (base === "application/x-www-form-urlencoded") return FAMILY_URLENCODED;
  if (!revision3) return base === "text/plain" ? FAMILY_TEXT : "";
  // The artifact-authorized byte lanes are evaluated first: they are the
  // cases where the ARTIFACT determines the octets. §9.2 carries no
  // media-type carve-out here — a schema-omitted text declaration is a
  // schema-omitted declaration like any other, which is what keeps it from
  // being orphaned between two lanes.
  if (allowRaw && rawBoundarySchema(schema, openapiVersion, allowSchemaOmittedOAS30Bytes)) return FAMILY_RAW;
  // The artifact declares the string to be already-encoded text, so the
  // caller supplies that encoded string verbatim and it is not decoded at the
  // OpenBindings boundary merely because the concrete media is binary. In OAS
  // 3.1 that declaration is `contentEncoding` on a string; on the 3.0 line it
  // is `format: byte`, which every accepted 3.0 edition's own format registry
  // defines as "base64 encoded characters" — §9.2 states the two as parallels
  // and this candidate carries them as one lane. Neither is gated on
  // character-data media: the artifact, not the media-type registration, is
  // what makes the value characters here.
  if (
    schema !== null
    && typeof schema === "object"
    && artifactEncodedString(schema, openapiVersion.startsWith("3.0"))
  ) {
    return FAMILY_TEXT;
  }
  // §9.2 string carriage. Its scope is DERIVED from two authorities, not
  // chosen: the OAS decides that the value is a string (and under 3.1
  // `format` has no content-encoding force, so `format: binary` there is
  // still a string declaration), while the media-type registration decides
  // whether a string has an octet image at all. A string becomes wire bytes
  // only where a character encoding applies, so the lane admits exactly the
  // character-data media below. The CALLER determines those octets, so the
  // lane authors no correspondence. Scoped by the declaration: the supplied
  // value's type never selects a lane.
  // The gate is the RESOLVED declaration (§5.2): `allOf` branches intersect,
  // so `allOf: [{type: string}, {type: object}]` admits no instance and
  // selects no lane. It is not "some branch is a string" — that union read
  // put such a declaration on the text lane and then failed it inside the
  // plan, which the OBI SDK reported as a degenerate media/schema
  // combination while the Go twin, gating on the intersection, reported no
  // lane selected. OAPI31-SS-48 pins the Go reading.
  if (
    isCharacterDataMedia(base)
    && schema !== null
    && typeof schema === "object"
    && resolveDeclaration(schema, openapiVersion.startsWith("3.0")).admitsStringAsSoleNonNullType()
  ) {
    return FAMILY_TEXT;
  }
  return "";
}

/**
 * §9.2's closed, structural character-data test: the media types whose
 * registration establishes their content as characters under a charset, and
 * therefore the only ones for which a caller-supplied string has a defined
 * octet image. Members, with the authority for each:
 *
 *   text/*                     RFC 6838 §4.2.1, RFC 2046 §4.1 — the text tree
 *                              is "material that is principally textual in
 *                              form", every registration must state how its
 *                              charset is determined, UTF-8 recommended
 *   application/xml, text/xml  RFC 7303 §3, §9.1, §9.2
 *   *+xml                      RFC 7303 §9.6.1
 *   *+json                     RFC 8259 §8.1 (the JSON lanes claim these first)
 *
 * It is deliberately NOT RFC 6838 §4.8's "Encoding considerations" field,
 * which does not decide the question — application/json registers `binary`
 * (RFC 8259 §11) while requiring UTF-8 text, and text/csv (RFC 4180 §3)
 * records no §4.8 value at all — and deliberately not a registry lookup,
 * which would make the accepted domain depend on a mutable source against
 * §2's pinning discipline. The argument must already be normalized.
 */
function isCharacterDataMedia(base: string): boolean {
  const slash = base.indexOf("/");
  if (slash <= 0 || slash === base.length - 1) return false;
  const primary = base.slice(0, slash);
  const subtype = base.slice(slash + 1);
  if (primary === "text") return true;
  const plus = subtype.lastIndexOf("+");
  if (plus >= 0) {
    const suffix = subtype.slice(plus);
    if (suffix === "+xml" || suffix === "+json") return true;
  }
  return base === "application/xml";
}

/**
 * A Media Type Object schema that makes no assertion at all: the JSON Schema
 * boolean `true`, or a Schema Object with no members. §9.2 treats it as the
 * same declaration as an omitted `schema` — the artifact made no claim that
 * the body is a string — so it takes the artifact-authorized byte lanes
 * rather than falling between two lanes.
 */
function schemaAssertsNothing(schema: MediaSchema): boolean {
  if (schema === true) return true;
  return schema !== null && typeof schema === "object" && Object.keys(schema).length === 0;
}

function rawBoundarySchema(
  schema: MediaSchema,
  openapiVersion: string,
  allowSchemaOmittedOAS30Bytes = false,
): boolean {
  // §9.2: a schema that asserts nothing — the JSON Schema boolean `true`, or
  // a memberless Schema Object — is the same declaration as an omitted
  // `schema` for these lanes under either edition.
  if (schemaAssertsNothing(schema)) schema = null;
  if (openapiVersion.startsWith("3.0")) {
    return (allowSchemaOmittedOAS30Bytes && schema === null)
      || (schema !== null && typeof schema === "object" && schemaTypeIs(schema, "string") && schemaFormatIs(schema, "binary"));
  }
  return schema === null;
}

function schemaFormatIs(schema: Record<string, unknown>, want: string): boolean {
  if (schema.format === want) return true;
  return Array.isArray(schema.allOf) && schema.allOf.some((member) => {
    const nested = member && typeof member === "object" && !Array.isArray(member)
      ? member as Record<string, unknown>
      : null;
    return nested !== null && schemaFormatIs(nested, want);
  });
}

/**
 * Resolves revision-3 range declarations against one configured concrete
 * request media. Exact declarations win over subtype-wildcard declarations,
 * which win over the all-media wildcard.
 * The chosen declaration is then admitted only through an already-defined
 * concrete carriage family; schema shape never invents a JSON lane.
 */
export function configureRequestMedia(
  plans: BodyPlan[],
  configured: string,
  options: RequestPlanningOptions,
): BodyPlan[] {
  let concrete: ParsedMediaType;
  try {
    concrete = parseMediaType(configured, true);
  } catch {
    return [];
  }
  const matches: Array<{ plan: BodyPlan; specificity: number; parameters: number }> = [];
  for (const plan of plans) {
    if (!plan.range) {
      try {
        const declared = parseMediaType(plan.mediaKey, true);
        if (declared.base === concrete.base && mediaParametersMatch(declared, concrete)) {
          matches.push({ plan, specificity: 2, parameters: Object.keys(declared.params).length });
        }
      } catch { /* planning already rejected invalid declarations */ }
      continue;
    }
    let range: ParsedMediaRange;
    try { range = parseMediaRange(plan.mediaKey, true); } catch { continue; }
    if (!mediaRangeMatches(range, concrete)) continue;
    matches.push({ plan, specificity: range.specificity, parameters: Object.keys(range.params).length });
  }
  if (matches.length === 0) return [];
  const specificity = Math.max(...matches.map((match) => match.specificity));
  const atSpecificity = matches.filter((match) => match.specificity === specificity);
  const parameterCount = Math.max(...atSpecificity.map((match) => match.parameters));
  const selected = atSpecificity.filter((match) => match.parameters === parameterCount);
  if (selected.length !== 1) return [];
  const plan = selected[0]!.plan;
  if (plan.unsupported) return [];
  if (!plan.range) return [{ ...plan, mediaType: concrete.canonical }];

  const schema = mediaSchema(plan.media);
  const openapiVersion = options.openapiVersion ?? "3.0";
  const openapi32 = openapiVersion === "3.2.0"
    ? openAPI32RequestMediaAdmission(concrete.base, plan.media)
    : { handled: false };
  if (openapi32.handled && (openapi32.error || !openapi32.family)) return [];
  const family = openapi32.handled
    ? openapi32.family!
    : concreteBodyFamily(concrete.base, schema, openapiVersion, true, true);
  if (!family) return [];
  try {
    return [buildBodyPlan(
      plan.required,
      { [plan.mediaKey]: plan.media ?? {} },
      {
        key: plan.mediaKey,
        parsed: concrete,
        family,
        range: false,
        ...(openapi32.sequentialKind ? { sequentialKind: openapi32.sequentialKind } : {}),
        ...(openapi32.positionalMultipart ? { positionalMultipart: true } : {}),
      },
      openapiVersion,
      true,
    )].map((resolved) => ({
      ...resolved,
      mediaKey: plan.mediaKey,
      mediaType: concrete.canonical,
      range: true,
      rangeSpecificity: plan.rangeSpecificity,
      // Keep invocation routing identical to the static range contract. In
      // particular, a schema-omitted range always uses the synthetic whole
      // body even when the configured concrete member is JSON.
      synthetic: plan.synthetic,
      wholeObject: plan.wholeObject,
      props: plan.props,
    }));
  } catch {
    return [];
  }
}

function mediaRangeMatches(range: ParsedMediaRange, concrete: ParsedMediaType): boolean {
  if (range.base !== "*/*" && range.base.slice(0, range.base.indexOf("/")) !== concrete.base.slice(0, concrete.base.indexOf("/"))) {
    return false;
  }
  return mediaParametersMatch(range, concrete);
}

function mediaParametersMatch(
  declared: Pick<ParsedMediaType, "params">,
  concrete: ParsedMediaType,
): boolean {
  return Object.entries(declared.params).every(([name, value]) => concrete.params[name] === value);
}

function mediaSchema(media: OpenAPIMediaType | null): MediaSchema {
  if (!media || !Object.hasOwn(media, "schema")) return null;
  const schema = media?.schema;
  if (typeof schema === "boolean") return schema;
  return schema && typeof schema === "object" ? schema : null;
}

/**
 * Whether revision 4's protocol-independent output boundary carries the
 * exact response octets as canonical Base64. JSON, text, and SSE retain
 * their application-value lanes; only artifact-authorized binary forms use
 * the byte boundary.
 */
export function responseUsesRawBoundary(
  media: OpenAPIMediaType,
  actualContentType: string,
  openapiVersion: string,
  profile: OpenAPIExecutionProfile = OPENAPI_PROFILE_BASE,
  exactDeclaration = true,
): boolean {
  const actual = parseMediaType(actualContentType, true).base;
  if (isJSONMediaType(actual) || actual.startsWith("text/")) return false;
  const schema = mediaSchema(media);
  if (openapiVersion.startsWith("3.0")) {
    return (hasSchemaOmittedOAS30ByteCarriage(profile) && exactDeclaration && !Object.hasOwn(media, "schema"))
      || (schema !== null
      && typeof schema === "object"
      && binarySignaled(schema, true));
  }
  return !Object.hasOwn(media, "schema");
}

/**
 * The selected media schema's declared top-level property names — the body
 * half of the flattened model's collision rule.
 */
function resolvedBodyShape(
  schema: Record<string, unknown> | null,
  seen: Set<Record<string, unknown>>,
  oas30: boolean,
): { object: boolean; props: Set<string> } {
  if (schema === null) return { object: true, props: new Set() };
  if (seen.has(schema)) return { object: false, props: new Set() };
  seen.add(schema);
  try {
    // Read under the governing edition's dialect (§9.1): `if`/`then`/`else`
    // are not in the 3.0 line's Schema Object at all, so a 3.0 declaration
    // carrying one is an ordinary named-property object and flattens.
    if (
      Array.isArray(schema.oneOf) || Array.isArray(schema.anyOf) || schema.not !== undefined
      || (!oas30
        && (schema.if !== undefined || schema.then !== undefined || schema.else !== undefined))
    ) {
      throw new Error(
        "conditional/combinatorial request schema has no single declaration-defined flattened surface in the selected execution profile",
      );
    }
    const props = new Set<string>();
    const ownProps = schema.properties;
    if (ownProps && typeof ownProps === "object" && !Array.isArray(ownProps)) {
      for (const name of Object.keys(ownProps)) props.add(name);
    }
    let object = bodySchemaFlattens(schema);
    if (Array.isArray(schema.allOf)) {
      for (const member of schema.allOf) {
        if (!member || typeof member !== "object" || Array.isArray(member)) continue;
        const nested = resolvedBodyShape(member as Record<string, unknown>, seen, oas30);
        object ||= nested.object;
        for (const name of nested.props) props.add(name);
      }
    }
    return { object, props };
  } finally {
    seen.delete(schema);
  }
}

export function candidateCollides(params: Array<{ name?: string }>, plan: BodyPlan): boolean {
  return params.some((parameter) => {
    const name = parameter.name ?? "";
    return ((plan.synthetic || plan.wholeObject) && name === "body")
      || (!plan.synthetic && !plan.wholeObject && plan.props?.has(name) === true);
  });
}

/** A routed input's body halves, as buildRequestBody consumes them. */
export interface RoutedBody {
  bodyFields: Record<string, unknown>;
  bodyValue: unknown;
  bodySet: boolean;
}

/** The wire body for one dispatch: undefined body + empty content type means no body is sent. */
export interface WireBody {
  body: BodyInit | undefined;
  /** The request Content-Type; empty when the runtime sets it (multipart boundary) or no body rides. */
  contentType: string;
  /** Selected multipart declaration awaiting a runtime-generated or author-declared boundary. */
  multipartMediaType?: string;
}

/**
 * Produces the wire body for the selected media type. An undefined body
 * with an empty content type means no body is sent (§9.1's remaining-body
 * rule: with a JSON-family selection and every input field consumed by
 * parameters, the body is {} if the request body is declared required and
 * omitted otherwise).
 */
export function buildRequestBody(
  doc: OpenAPIDocument,
  plan: BodyPlan | null,
  routed: RoutedBody,
): WireBody {
  if (!plan?.declared) return { body: undefined, contentType: "" };
  switch (plan.family) {
    case FAMILY_JSON: {
      if (plan.synthetic || plan.wholeObject) {
        if (!routed.bodySet) {
          // A supplied input missing the synthetic body member is sent
          // as-is (the server's declared validation is the authority): no
          // body rides the wire.
          return { body: undefined, contentType: "" };
        }
        return { body: JSON.stringify(routed.bodyValue ?? null), contentType: plan.mediaType };
      }
      if (Object.keys(routed.bodyFields).length === 0) {
        if (plan.required || routed.bodySet) return { body: "{}", contentType: plan.mediaType };
        return { body: undefined, contentType: "" };
      }
      return { body: JSON.stringify(routed.bodyFields), contentType: plan.mediaType };
    }
    case FAMILY_MULTIPART: {
      if (plan.openapiVersion === "3.2.0") {
        if (plan.positionalMultipart) {
          if (!routed.bodySet && !plan.required) return { body: undefined, contentType: "" };
          return buildOpenAPI32MultipartBody(plan.mediaType, plan.media, routed);
        }
        const openAPI32Fields = objectBodyFields(plan, routed);
        validateOpenAPI32MultipartFields(plan.media, openAPI32Fields);
        if (openAPI32MultipartNeedsNativeBuilder(plan.media)) {
          return buildOpenAPI32MultipartBody(
            plan.mediaType,
            plan.media,
            { ...routed, bodyFields: openAPI32Fields },
          );
        }
      }
      const fields = objectBodyFields(plan, routed);
      if (Object.keys(fields).length === 0 && !plan.required && !routed.bodySet) {
        return { body: undefined, contentType: "" };
      }
      // The runtime stamps the multipart boundary onto Content-Type itself.
      return {
        body: buildMultipartBody(
          doc,
          plan.media,
          fields,
          plan.revision3 === true,
          plan.wholeObject === true,
        ),
        contentType: "",
        ...(plan.revision3 === true ? { multipartMediaType: plan.mediaType } : {}),
      };
    }
    case FAMILY_URLENCODED: {
      const fields = objectBodyFields(plan, routed);
      if (Object.keys(fields).length === 0 && !plan.required && !routed.bodySet) {
        return { body: undefined, contentType: "" };
      }
      return {
        body: buildURLEncodedBody(
          plan.media,
          fields,
          plan.revision3 === true,
          plan.openapiVersion ?? "3.0",
          plan.wholeObject === true,
        ),
        contentType: plan.mediaType,
      };
    }
    case FAMILY_TEXT: {
      if (!routed.bodySet) return { body: undefined, contentType: "" };
      if (plan.openapiVersion !== "3.2.0" && typeof routed.bodyValue !== "string") {
        // §9.2 selects this lane from the declaration alone, so a non-string
        // value is a pre-dispatch refusal against the artifact's own type,
        // not a change of candidate set (OAPI-P-04).
        throw new Error(
          `request media ${plan.mediaType} declares a string body but the supplied value is ${typeof routed.bodyValue}, not a string`,
        );
      }
      const text = plan.openapiVersion === "3.2.0"
        ? serializeOpenAPI32NonJSONText(mediaSchema(plan.media), routed.bodyValue)
        : routed.bodyValue as string;
      return {
        body: plan.revision3 === true && Object.hasOwn(parseMediaType(plan.mediaType, true).params, "charset")
          ? encodeTextForMedia(text, plan.mediaType, "request body")
          : text,
        contentType: plan.mediaType,
      };
    }
    case FAMILY_RAW: {
      if (!routed.bodySet) return { body: undefined, contentType: "" };
      return {
        body: decodeBoundaryBase64(routed.bodyValue, plan.mediaType),
        contentType: plan.mediaType,
      };
    }
    case FAMILY_SEQUENTIAL: {
      if (!routed.bodySet) return { body: undefined, contentType: "" };
      if (!plan.sequentialKind) throw new Error("sequential request plan has no framing kind");
      return {
        body: buildOpenAPI32SequentialBody(plan.sequentialKind, routed.bodyValue),
        contentType: plan.mediaType,
      };
    }
  }
  throw new Error(`unknown body family "${plan.family}"`);
}

/**
 * Materializes a revision-3 multipart FormData body before dispatch so the
 * selected declaration's non-boundary parameters survive and an explicit
 * boundary, when present, is the boundary used by both header and bytes.
 */
export async function finalizeRequestBody(wire: WireBody): Promise<WireBody> {
  if (!wire.multipartMediaType || !(wire.body instanceof FormData)) return wire;
  const selected = parseMediaType(wire.multipartMediaType, true);
  const generated = new Response(wire.body);
  const runtimeType = generated.headers.get("content-type") ?? "";
  const runtime = parseMediaType(runtimeType, true);
  const runtimeBoundary = runtime.params["boundary"];
  if (!runtimeBoundary) throw new Error("multipart runtime did not generate a boundary");

  const declaredBoundary = selected.params["boundary"];
  const boundary = declaredBoundary ?? runtimeBoundary;
  if (declaredBoundary !== undefined) validateMultipartBoundary(declaredBoundary);
  let bytes = new Uint8Array(await generated.arrayBuffer());
  if (boundary !== runtimeBoundary) {
    bytes = replaceMultipartBoundary(bytes, runtimeBoundary, boundary);
  }
  // R5 (2026-09-01): a `contentEncoding` declaration never produces a
  // Content-Transfer-Encoding field. The edition's equivalence describes what
  // the declaration MEANS, not a field a serializer adds, and RFC 7578 §4.7
  // says senders SHOULD NOT generate it. The declared equivalence still
  // governs the conflict check in openapi32-media.ts and response parsing.

  const params: Record<string, string> = { ...selected.renderedParams, boundary };
  const contentType = selected.base + Object.keys(params)
    .sort()
    .map((name) => `; ${name}=${formatParameter(params[name]!)}`)
    .join("");
  return { body: bytes, contentType };
}

function validateMultipartBoundary(boundary: string): void {
  if (
    boundary.length < 1
    || boundary.length > 70
    || boundary.endsWith(" ")
    || !/^[0-9A-Za-z'()+_,./:=? -]+$/.test(boundary)
  ) {
    throw new Error(`multipart boundary ${JSON.stringify(boundary)} is not a valid RFC 2046 boundary`);
  }
}

function objectBodyFields(plan: BodyPlan, routed: RoutedBody): Record<string, unknown> {
  if (!plan.wholeObject) return routed.bodyFields;
  if (!routed.bodySet) return {};
  const value = asObject(routed.bodyValue);
  if (value === null) {
    throw new Error(`request media ${plan.mediaType} requires the whole body value to be an object`);
  }
  return value;
}

function replaceMultipartBoundary(
  bytes: Uint8Array,
  from: string,
  to: string,
): Uint8Array<ArrayBuffer> {
  const source = new TextEncoder().encode(`--${from}`);
  const replacement = new TextEncoder().encode(`--${to}`);
  const chunks: Uint8Array[] = [];
  let start = 0;
  for (let index = 0; index <= bytes.length - source.length; index++) {
    let equal = true;
    for (let offset = 0; offset < source.length; offset++) {
      if (bytes[index + offset] !== source[offset]) { equal = false; break; }
    }
    if (!equal) continue;
    const lineStart = index === 0 || (index >= 2 && bytes[index - 2] === 13 && bytes[index - 1] === 10);
    const after = index + source.length;
    const lineEnd = (
      bytes[after] === 13 && bytes[after + 1] === 10
    ) || (
      bytes[after] === 45 && bytes[after + 1] === 45
    );
    if (!lineStart || !lineEnd) continue;
    chunks.push(bytes.slice(start, index), replacement);
    start = after;
    index = after - 1;
  }
  if (chunks.length === 0) throw new Error("multipart runtime boundary was absent from the serialized body");
  chunks.push(bytes.slice(start));
  const length = chunks.reduce((total, chunk) => total + chunk.length, 0);
  const result = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    result.set(chunk, offset);
    offset += chunk.length;
  }
  return result;
}

function encodeTextForMedia(value: unknown, mediaType: string, subject: string): Uint8Array<ArrayBuffer> {
  if (typeof value !== "string") {
    throw new Error(`${subject} requires a string value`);
  }
  const parsed = parseMediaType(mediaType, true);
  requireSupportedCharset(parsed, subject);
  switch ((parsed.params["charset"] ?? "utf-8").toLowerCase()) {
    case "utf-8":
      return new TextEncoder().encode(value);
    case "us-ascii": {
      const result = new Uint8Array(value.length);
      for (let index = 0; index < value.length; index++) {
        const code = value.charCodeAt(index);
        if (code > 0x7f) throw new Error(`${subject} cannot represent U+${code.toString(16).toUpperCase().padStart(4, "0")} as US-ASCII`);
        result[index] = code;
      }
      return result;
    }
    case "iso-8859-1": {
      const result = new Uint8Array(value.length);
      for (let index = 0; index < value.length; index++) {
        const code = value.charCodeAt(index);
        if (code > 0xff) throw new Error(`${subject} cannot represent U+${code.toString(16).toUpperCase().padStart(4, "0")} as ISO-8859-1`);
        result[index] = code;
      }
      return result;
    }
  }
  throw new Error(`${subject} declares an unsupported charset`);
}

function decodeBoundaryBase64(value: unknown, mediaType: string): Uint8Array<ArrayBuffer> {
  if (typeof value !== "string") {
    throw new Error(`request media ${mediaType} requires a Base64 string at the OpenBindings body boundary`);
  }
  if (
    value.length % 4 !== 0
    || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value)
  ) {
    throw new Error(`request media ${mediaType} received an invalid Base64 body`);
  }
  try {
    const binary = atob(value);
    if (btoa(binary) !== value) {
      throw new Error("non-canonical Base64");
    }
    return Uint8Array.from(binary, (char) => char.charCodeAt(0));
  } catch {
    throw new Error(`request media ${mediaType} received an invalid Base64 body`);
  }
}

// ---------------------------------------------------------------------------
// Multipart (OAPI-P-04's part-encoding rules)
// ---------------------------------------------------------------------------

function isOpenAPI30(doc: OpenAPIDocument): boolean {
  return typeof doc.openapi === "string" && doc.openapi.startsWith("3.0");
}

/**
 * Encodes body fields as multipart/form-data. OAS 3.0 `format: binary`
 * parts use the Base64 boundary. Revision 3 keeps OAS 3.1 contentEncoding
 * strings (and identity-encoded contentMediaType strings) unchanged; the
 * compatibility revisions retain their historical decode behavior. Other
 * parts serialize per the artifact's encoding object or OAS defaults.
 * Nothing is decided by payload sniffing, and fields are sorted.
 */
export function buildMultipartBody(
  doc: OpenAPIDocument,
  media: OpenAPIMediaType | null,
  fields: Record<string, unknown>,
  revision3 = false,
  dynamicProperties = false,
): FormData {
  const fd = new FormData();
  const is30 = isOpenAPI30(doc);
  const rawSchema = mediaSchema(media);
  const schema = rawSchema && typeof rawSchema === "object" ? rawSchema : null;
  const encoding = (media?.encoding ?? {}) as Record<string, Record<string, unknown>>;

  for (const name of Object.keys(fields).sort()) {
    const value = fields[name];
    let propSchema = dynamicProperties
      ? resolvedMultipartProperty(schema, name, new Set(), is30)
      : asObject(resolvedMultipartProperties(schema, new Set())[name]);
    const enc = asObject(encoding[name]);
    if (revision3) {
      if (propSchema === null && !dynamicProperties && staticPartBooleanLiteral(schema, name) === true) {
        propSchema = {}; // true is the same unconstrained declaration as {}
      }
      const effective = effectiveRevision3PartSchema(propSchema, is30);
      if (effective.nullable && value === null) {
        continue; // §9.2: a JSON null value elides the nullable optional part
      }
      if (effective.schema !== null && effective.schema !== false) propSchema = effective.schema;
    }

    // A declared array expands into repeated parts of the same name, each
    // element encoded per the items schema (the multipart way to carry
    // arrays — including arrays of files).
    if (
      propSchema
      && schemaTypeIs(propSchema, "array")
      && !(revision3 && hasExplicitMultipartExpansion(enc))
    ) {
      const arr = asArray(value);
      if (arr) {
        const items = resolvedMultipartItems(propSchema);
        // The write lane refuses exactly what the admission lane refuses.
        // validateRevision3Multipart excludes a nested array declaration from
        // the candidate set, so a plan carrying one cannot be selected;
        // without this the two lanes disagree about the same declaration, and
        // the disagreement is only invisible because something else happens
        // to exclude it first. Same decision, each engine's own wording.
        if (revision3 && items !== null && schemaTypeIs(items, "array")) {
          throw new Error(
            `multipart array property ${JSON.stringify(name)} has nested array items with no declaration-defined repeated-part mapping`,
          );
        }
        for (const elem of arr) {
          writeMultipartPart(fd, name, elem, items, enc, is30, revision3);
        }
        continue;
      }
      // §9.2: "An invalid value or a member for which the resolved schema
      // leaves no faithful form carriage refuses before dispatch." The
      // declaration decides this part's carriage — an array property rides as
      // one part per element — so a supplied value with no elements to carry
      // has no faithful form carriage at all. Falling through would serialize
      // it against the whole-property schema and send one application/json
      // part the declaration never described, which is the silent-carriage
      // outcome the rule exists to prevent.
      if (revision3) {
        throw new Error(
          `multipart part ${JSON.stringify(name)} is declared as an array but the supplied value carries no elements`,
        );
      }
    }
    writeMultipartPart(fd, name, value, propSchema, enc, is30, revision3);
  }
  return fd;
}

/**
 * The boolean literal directly declared for a statically named property,
 * through allOf; null when the property's declaration is not a boolean
 * literal. Object property resolution drops boolean literals, so the
 * revision-3 part pipeline recovers them here.
 */
function staticPartBooleanLiteral(
  schema: Record<string, unknown> | null,
  name: string,
  seen: Set<Record<string, unknown>> = new Set(),
): boolean | null {
  if (schema === null || seen.has(schema)) return null;
  seen.add(schema);
  try {
    const properties = asObject(schema.properties);
    const direct = properties?.[name];
    if (direct === true || direct === false) return direct;
    if (Array.isArray(schema.allOf)) {
      for (const member of schema.allOf) {
        const literal = staticPartBooleanLiteral(asObject(member), name, seen);
        if (literal !== null) return literal;
      }
    }
    return null;
  } finally {
    seen.delete(schema);
  }
}

/**
 * Walks §9.2's dynamic-member resolution chain: an exact `properties`
 * schema and every matching `patternProperties` schema, or the
 * `additionalProperties` schema when neither matches. The resolution
 * keywords are read under the governing edition's dialect (§9.1/§9.2): on
 * the 3.0 line `patternProperties` has no meaning, so the chain there is
 * `properties`, then `additionalProperties`.
 */
function resolvedMultipartProperty(
  schema: Record<string, unknown> | null,
  name: string,
  seen: Set<Record<string, unknown>>,
  oas30: boolean,
): Record<string, unknown> | null {
  if (schema === null || seen.has(schema)) return null;
  seen.add(schema);
  try {
    const candidates: Record<string, unknown>[] = [];
    const properties = asObject(schema.properties);
    const exact = properties ? asObject(properties[name]) : null;
    if (exact !== null) candidates.push(exact);

    let patternMatched = false;
    const patterns = oas30 ? null : asObject(schema.patternProperties);
    for (const [pattern, raw] of Object.entries(patterns ?? {})) {
      let matches = false;
      try { matches = new RegExp(pattern, "u").test(name); } catch { /* validation owns invalid patterns */ }
      if (!matches) continue;
      patternMatched = true;
      const candidate = asObject(raw);
      if (candidate !== null) candidates.push(candidate);
    }

    if (exact === null && !patternMatched) {
      const additional = schema.additionalProperties;
      const candidate = asObject(additional);
      if (candidate !== null) candidates.push(candidate);
      else if (additional === true || !Object.hasOwn(schema, "additionalProperties")) candidates.push({});
    }

    if (Array.isArray(schema.allOf)) {
      for (const member of schema.allOf) {
        const nested = asObject(member);
        if (nested === null) continue;
        const candidate = resolvedMultipartProperty(nested, name, seen, oas30);
        if (candidate !== null) candidates.push(candidate);
      }
    }
    if (candidates.length === 0) return null;
    return candidates.length === 1 ? candidates[0]! : { allOf: candidates };
  } finally {
    seen.delete(schema);
  }
}

/**
 * Reports a resolved declaration whose type IS `want` — the single type it
 * denotes, not one alternative among several. An array-valued `type` is a
 * union (JSON Schema 2020-12 §6.1.1), so it denotes `want` only when `want`
 * is its one member; a genuine union is resolved by
 * collapsedNullableTypeMember before carriage, or refuses. This is the Go
 * twin's `Types.Is` rule, and reading a multi-member union as every one of
 * its members would let the two engines pick different carriage lanes for
 * one declaration.
 */
function schemaTypeIs(schema: Record<string, unknown>, want: string): boolean {
  const ty = schema.type;
  if (typeof ty === "string") return ty === want;
  if (Array.isArray(ty)) return ty.length === 1 && ty[0] === want;
  return Array.isArray(schema.allOf) && schema.allOf.some(member => {
    const nested = asObject(member);
    return nested !== null && schemaTypeIs(nested, want);
  });
}

function resolvedMultipartProperties(
  schema: Record<string, unknown> | null,
  seen: Set<Record<string, unknown>>,
): Record<string, Record<string, unknown>> {
  if (schema === null || seen.has(schema)) return {};
  seen.add(schema);
  try {
    const result: Record<string, Record<string, unknown>> = {};
    const own = asObject(schema.properties);
    for (const [name, property] of Object.entries(own ?? {})) {
      const value = asObject(property);
      if (value !== null) result[name] = value;
    }
    if (Array.isArray(schema.allOf)) {
      for (const member of schema.allOf) {
        const nested = asObject(member);
        if (nested === null) continue;
        for (const [name, property] of Object.entries(resolvedMultipartProperties(nested, seen))) {
          result[name] = result[name] === undefined
            ? property
            : { allOf: [result[name], property] };
        }
      }
    }
    return result;
  } finally {
    seen.delete(schema);
  }
}

function writeMultipartPart(
  fd: FormData,
  name: string,
  value: unknown,
  schema: Record<string, unknown> | null,
  enc: Record<string, unknown> | null,
  is30: boolean,
  revision3: boolean,
): void {
  if (revision3) {
    writeRevision3MultipartPart(fd, name, value, schema, enc, is30);
    return;
  }
  const encContentType = typeof enc?.contentType === "string" ? enc.contentType : "";

  if (revision3 && !is30 && schema !== null && schemaTypeIs(schema, "string")) {
    const contentEncoding = resolvedSchemaStringKeyword(schema, "contentEncoding");
    const contentMediaType = resolvedSchemaStringKeyword(schema, "contentMediaType");
    if (contentEncoding !== "" || contentMediaType !== "") {
      if (typeof value !== "string") {
        throw new Error(`multipart part ${JSON.stringify(name)} requires an artifact-encoded string value`);
      }
      const contentType = encContentType || contentMediaType;
      if (contentType !== "") {
        fd.append(name, new Blob([value], { type: contentType }), name);
      } else {
        fd.append(name, value);
      }
      return;
    }
  }

  if (binarySignaled(schema, is30)) {
    // An in-process Blob passes through raw (it cannot have arrived as
    // JSON) — the convenience counterpart of Go's []byte passthrough.
    const ct =
      encContentType ||
      declaredContentMediaType(schema) ||
      "application/octet-stream";
    if (revision3 && is30) {
      if (typeof value !== "string") {
        throw new Error(`binary part ${JSON.stringify(name)} requires a canonical Base64 string`);
      }
      const data = binaryPartBytes(name, value, "", true);
      fd.append(name, new Blob([data as BlobPart], { type: ct }), name);
      return;
    }
    if (value instanceof Blob) {
      fd.append(name, value.type ? value : new Blob([value], { type: ct }), name);
      return;
    }
    const data = binaryPartBytes(
      name,
      value,
      declaredContentEncoding(schema, is30),
      revision3 && is30,
    );
    fd.append(name, new Blob([data as BlobPart], { type: ct }), name);
    return;
  }

  // The encoding object's contentType, where declared, decides the part's
  // serialization; else the OAS per-type part defaults apply.
  if (encContentType) {
    const ct = normalizeMediaType(encContentType);
    let body: string;
    if (isJSONMediaType(ct)) {
      body = JSON.stringify(value ?? null);
    } else if (typeof value === "string") {
      body = value;
    } else {
      try {
        body = primitiveString(value);
      } catch {
        body = JSON.stringify(value ?? null);
      }
    }
    fd.append(name, new Blob([body], { type: encContentType }), name);
    return;
  }

  // Per-type defaults: objects (and undeclared complex values) ride as
  // application/json parts; primitives as plain form fields.
  if (isComplexPartValue(value, schema)) {
    fd.append(name, new Blob([JSON.stringify(value ?? null)], { type: "application/json" }), name);
    return;
  }
  fd.append(name, primitiveString(value));
}

function writeRevision3MultipartPart(
  fd: FormData,
  name: string,
  value: unknown,
  schema: Record<string, unknown> | null,
  enc: Record<string, unknown> | null,
  is30: boolean,
): void {
  if (hasExplicitMultipartExpansion(enc)) {
    const style = typeof enc?.style === "string" && enc.style !== "" ? enc.style : "form";
    const explode = typeof enc?.explode === "boolean" ? enc.explode : style === "form";
    let parts: Array<[string, string]>;
    try {
      parts = serializeMultipartValue(name, value, style, explode);
    } catch (error: unknown) {
      throw new Error(
        `multipart property ${JSON.stringify(name)}: ${error instanceof Error ? error.message : String(error)}`,
        { cause: error },
      );
    }
    for (const [partName, partValue] of parts) fd.append(partName, partValue);
    return;
  }

  if (schema === null) {
    throw new Error(
      `multipart property ${JSON.stringify(name)} has no declaration-defined mapping from its default octet-stream part to a JSON caller value`,
    );
  }

  const declaredEncodingType = typeof enc?.contentType === "string"
    ? parseSingleMultipartContentType(enc.contentType, name)
    : null;
  if (declaredEncodingType === null && !hasDeclaredSchemaType(schema) && !binarySignaled(schema, is30)) {
    if (!partSchemaDeclaresNoType(schema, is30)) {
      throw new Error(Array.isArray(schema.anyOf) || Array.isArray(schema.oneOf)
        ? `multipart property ${JSON.stringify(name)} declares a choice applicator that does not collapse to one non-null branch; no single part carriage is defined`
        : `multipart property ${JSON.stringify(name)} has no declaration-defined mapping from its default octet-stream part to a JSON caller value`);
    }
    refuseTypelessPart("multipart", name, is30, schema);
  }
  const selected = declaredEncodingType
    ?? parseMediaType(defaultMultipartContentType(schema, is30), true);
  requireSupportedCharset(selected, `multipart property ${JSON.stringify(name)}`);
  const contentType = selected.canonical;

  const contentEncoding = !is30 && schemaTypeIs(schema, "string")
    ? resolvedSchemaStringKeyword(schema, "contentEncoding")
    : "";
  // The artifact-encoded string lane, both editions: the declared characters
  // ride the wire unchanged, with no OpenBindings boundary decode. No
  // Content-Transfer-Encoding is emitted on the 3.0 line -- 3.0.4 equates a
  // `format: byte` part with an Encoding Object declaring that header, and
  // Section 9.2 reads the equivalence as declaration semantics.
  if (artifactEncodedString(schema, is30)) {
    const data = encodeTextForMedia(value, contentType, `multipart property ${JSON.stringify(name)}`);
    fd.append(name, new Blob([data], { type: contentType }), name);
    return;
  }

  if (is30 && binarySignaled(schema, true)) {
    if (typeof value !== "string") {
      throw new Error(`binary part ${JSON.stringify(name)} requires a canonical Base64 string`);
    }
    const data = binaryPartBytes(name, value, "", true);
    fd.append(name, new Blob([data as BlobPart], { type: contentType }), name);
    return;
  }

  if (isJSONMediaType(selected.base)) {
    fd.append(name, new Blob([JSON.stringify(value ?? null)], { type: contentType }), name);
    return;
  }

  if (selected.base === "application/octet-stream" && contentEncoding !== "") {
    if (typeof value !== "string") {
      throw new Error(is30
        ? `binary part ${JSON.stringify(name)} requires a canonical Base64 string`
        : `octet-stream part ${JSON.stringify(name)} requires an artifact-encoded string`);
    }
    const data = is30
      ? binaryPartBytes(name, value, "", true)
      : encodeTextForMedia(value, contentType, `multipart property ${JSON.stringify(name)}`);
    fd.append(name, new Blob([data as BlobPart], { type: contentType }), name);
    return;
  }

  if (selected.base !== "text/plain") {
    throw new Error(
      `multipart property ${JSON.stringify(name)} has no serializer for ${contentType}`,
    );
  }

  const text = primitiveString(value);
  if (
    declaredEncodingType === null
    && selected.base === "text/plain"
    && Object.keys(selected.params).length === 0
  ) {
    fd.append(name, text);
    return;
  }
  const data = encodeTextForMedia(text, contentType, `multipart property ${JSON.stringify(name)}`);
  fd.append(name, new Blob([data], { type: contentType }), name);
}

/**
 * The accepted editions' own default part-Content-Type rows. Every row is
 * keyed on the DECLARED type:
 *
 * - 3.0.0-3.0.3 and 3.0.4 key application/octet-stream on "a `type: string`
 *   with `format: binary` or `format: base64`"; `contentEncoding` is not in the
 *   3.0 Schema Object's dialect at all.
 * - 3.1.0 Section 4.8.14.5 keys it on "a `type: string` with a
 *   `contentEncoding`", and gives primitives text/plain and complex values
 *   application/json without reference to the keyword.
 * - 3.1.1 and 3.1.2 Section 4.8.15.1.1 tabulate `string` x contentEncoding
 *   present -> application/octet-stream, and hold `n/a` in the contentEncoding
 *   column of the `number, integer, or boolean`, `object` and `array` rows —
 *   which the table's own note defines as "the presence or value of
 *   contentEncoding is irrelevant".
 *
 * The `schemaTypeIs(schema, "string")` guard on the encoded row is that
 * condition. Without it a declared non-string part carrying `contentEncoding`
 * took application/octet-stream, which no accepted edition assigns it.
 */
/**
 * R4 (ratified 2026-09-01). Reports the content-based urlencoded lane's
 * ungoverned cell: an `array` property, riding one field as a whole compound,
 * whose item-derived default selects a media type that defines no
 * serialization for a container. `application/json` does define one and is
 * therefore governed; `text/plain` and `application/octet-stream` do not, and
 * this specification authors no bytes there — the property needs an explicit
 * Encoding `contentType` or the `propertyMedia` choice. Exported so the
 * preflight requirement and the emission refusal read the same predicate.
 */
export function urlencodedArrayNeedsPropertyMedia(
  schema: Record<string, unknown> | null,
  is30: boolean,
): boolean {
  if (schema === null) return false;
  // `type: ["array", "null"]` is an array property that also admits null; the
  // nullable collapse is what every other rule on this lane reads, so the
  // predicate reads it too rather than answering differently for the two
  // spellings of the same declaration.
  const { schema: collapsed } = effectiveRevision3PartSchema(schema, is30);
  const container = collapsed ?? schema;
  if (typeof container !== "object" || container === null || !schemaTypeIs(container, "array")) return false;
  const items = resolvedMultipartItems(container);
  if (items === null) return false;
  // A typeless item declaration has no default concrete type on either edition
  // line, which is the same "selection yields no single concrete media type"
  // cell Section 9.3's configuration point already names.
  if (!hasDeclaredSchemaType(items) && partSchemaDeclaresNoType(items, is30)) return true;
  let selected: string;
  try {
    selected = defaultMultipartContentType(container, is30);
  } catch {
    // No item-type default resolves at all -- a choice applicator that
    // supplies no single resolved member, or a union that does not collapse.
    // Section 5.2 leaves that property with no resolved member declaration, so
    // no media choice repairs it and the ordinary refusal stands.
    return false;
  }
  return !isJSONMediaType(parseMediaType(selected, true).base);
}

function defaultMultipartContentType(
  schema: Record<string, unknown>,
  is30: boolean,
  depth = 0,
): string {
  // 3.0.4's default-`contentType` table gives `string` with `format`
  // "`binary` or `byte`" the default application/octet-stream, while 3.0.0
  // through 3.0.3's prose names only `binary` before "other primitive types".
  // Under the editions' own Section 4.1 patch-uniformity instruction the line
  // answers uniformly, and 3.0.4 read under its own text fixes what the
  // uniform default is (Section 9.2, OAPI-P-04).
  if (is30 && (binarySignaled(schema, true) || artifactEncodedString(schema, true))) {
    return "application/octet-stream";
  }
  if (
    !is30
    && schemaTypeIs(schema, "string")
    && resolvedSchemaStringKeyword(schema, "contentEncoding") !== ""
  ) {
    return "application/octet-stream";
  }
  if (schemaTypeIs(schema, "object")) return "application/json";
  // The edition's default table gives an `array` "the item-type default", not
  // its container's `application/json`: the type that decides is the resolved
  // `items` declaration. On multipart this is invisible because an array
  // property expands into one part per item, each already carrying the item's
  // own schema; on the content-based urlencoded lane the whole array rides one
  // field, and reading `application/json` off the container there would author
  // bytes the edition never selected. R4 (ratified 2026-09-01) reads the table
  // literally, and the urlencoded lane refuses-or-configures where the
  // item-derived selection defines no serialization for the container.
  if (schemaTypeIs(schema, "array")) {
    if (depth > 8) {
      throw new Error("multipart property nests arrays past the depth an item-type default can resolve");
    }
    const items = resolvedMultipartItems(schema);
    if (items === null) {
      throw new Error("multipart array property has no resolved items declaration and therefore no item-type default");
    }
    return defaultMultipartContentType(items, is30, depth + 1);
  }
  if (
    schemaTypeIs(schema, "string")
    || schemaTypeIs(schema, "number")
    || schemaTypeIs(schema, "integer")
    || schemaTypeIs(schema, "boolean")
  ) {
    return "text/plain";
  }
  if (Array.isArray(schema.anyOf) || Array.isArray(schema.oneOf)) {
    throw new Error(
      "multipart property declares a choice applicator that does not collapse to one non-null branch; no single part carriage is defined",
    );
  }
  const union = nonCollapsingUnionType(schema);
  if (union !== null) {
    throw new Error(
      is30
        ? `multipart property declares type [${union.join(" ")}], but an OAS 3.0 \`type\` MUST be a string and multiple types via an array are not supported; no part carriage is defined`
        : `multipart property declares union type [${union.join(" ")}], which does not collapse to one non-null type; no single part carriage is defined`,
    );
  }
  throw new Error("multipart property has no declaration-defined default Content-Type");
}

/**
 * Decides object-vs-primitive part encoding: by the declared schema type
 * where one exists, by the JSON value's own shape for undeclared
 * passthrough fields (a declaration-free field has no artifact answer; a
 * JSON value's TYPE is structure, not byte-sniffing).
 */
function isComplexPartValue(value: unknown, schema: Record<string, unknown> | null): boolean {
  const ty = schema?.type;
  if (typeof ty === "string" && ty !== "") {
    return ty === "object" || ty === "array";
  }
  if (Array.isArray(ty) && ty.length > 0) {
    return ty.includes("object") || ty.includes("array");
  }
  return asObject(value) !== null || asArray(value) !== null;
}

/**
 * The compatibility-edition binary signal. Revision 3 intercepts OAS 3.1
 * encoded strings before this legacy path.
 */
export function binarySignaled(schema: Record<string, unknown> | null, is30: boolean): boolean {
  if (!schema) return false;
  const direct = is30
    ? schema.format === "binary"
    : schemaTypeIs(schema, "string") && (
    declaredContentMediaType(schema) !== "" || declaredContentEncoding(schema, false) !== ""
  );
  if (direct) return true;
  return Array.isArray(schema.allOf) && schema.allOf.some(member => binarySignaled(asObject(member), is30));
}

function resolvedMultipartItems(schema: Record<string, unknown>): Record<string, unknown> | null {
  const candidates: Record<string, unknown>[] = [];
  const direct = asObject(schema.items);
  if (direct !== null) candidates.push(direct);
  if (Array.isArray(schema.allOf)) {
    for (const member of schema.allOf) {
      const nested = asObject(member);
      if (nested === null) continue;
      const items = resolvedMultipartItems(nested);
      if (items !== null) candidates.push(items);
    }
  }
  if (candidates.length === 0) return null;
  return candidates.length === 1 ? candidates[0]! : { allOf: candidates };
}

/**
 * §9.2's artifact-encoded string cell, under the governing edition's own
 * vocabulary: a string the ARTIFACT declares to be already-encoded text, so
 * the characters ride the wire unchanged and the OpenAPI raw-boundary decode
 * never runs. The 3.1 line spells it with `contentEncoding` on a declared
 * string; the 3.0 line spells it with `format: byte`, which every accepted
 * 3.0 edition's own format registry defines as "base64 encoded characters"
 * (3.0.4's row citing [RFC 4648] Section 4). The edition gate is inside the
 * predicate because the two spellings are inert on each other's line:
 * `contentEncoding` is not in the 3.0 Schema Object's dialect at all, and on
 * the 3.1 line `format` has no content-encoding force and `byte` is absent
 * from the format tables.
 */
export function artifactEncodedString(schema: Record<string, unknown> | null, is30: boolean): boolean {
  if (schema === null || !schemaTypeIs(schema, "string")) return false;
  return is30
    ? schemaFormatIs(schema, "byte")
    : resolvedSchemaStringKeyword(schema, "contentEncoding") !== "";
}

/**
 * The 3.1 schema's declared contentEncoding (3.0 has no equivalent
 * keyword; its binary signal carries no encoding).
 */
function declaredContentEncoding(schema: Record<string, unknown> | null, is30: boolean): string {
  if (is30) return "";
  if (typeof schema?.contentEncoding === "string") return schema.contentEncoding;
  if (Array.isArray(schema?.allOf)) {
    for (const member of schema.allOf) {
      const encoding = declaredContentEncoding(asObject(member), false);
      if (encoding) return encoding;
    }
  }
  return "";
}

function declaredContentMediaType(schema: Record<string, unknown> | null): string {
  if (typeof schema?.contentMediaType === "string") return schema.contentMediaType;
  if (Array.isArray(schema?.allOf)) {
    for (const member of schema.allOf) {
      const mediaType = declaredContentMediaType(asObject(member));
      if (mediaType) return mediaType;
    }
  }
  return "";
}

/** Resolves one string-valued schema keyword through allOf and refuses conflicts. */
function resolvedSchemaStringKeyword(
  schema: Record<string, unknown> | null,
  keyword: "contentEncoding" | "contentMediaType",
): string {
  const values = new Set<string>();
  collectSchemaStringKeyword(schema, keyword, values, new Set());
  if (values.size > 1) {
    throw new Error(
      `request schema has conflicting ${keyword} declarations: ${[...values].sort().map((value) => JSON.stringify(value)).join(", ")}`,
    );
  }
  return values.values().next().value ?? "";
}

function collectSchemaStringKeyword(
  schema: Record<string, unknown> | null,
  keyword: "contentEncoding" | "contentMediaType",
  values: Set<string>,
  seen: Set<Record<string, unknown>>,
): void {
  if (schema === null || seen.has(schema)) return;
  seen.add(schema);
  const direct = schema[keyword];
  if (typeof direct === "string" && direct !== "") values.add(direct);
  if (Array.isArray(schema.allOf)) {
    for (const member of schema.allOf) {
      collectSchemaStringKeyword(asObject(member), keyword, values, seen);
    }
  }
}

/**
 * Decodes a binary-signaled part's bytes from the caller's string value:
 * per the declared contentEncoding when one is declared, Base64 otherwise
 * (the boundary encoding for bytes — the operation value domain is JSON).
 * An in-process Uint8Array passes through raw (it cannot have arrived as
 * JSON).
 */
export function binaryPartBytes(
  name: string,
  value: unknown,
  contentEncoding: string,
  canonicalBase64 = false,
): Uint8Array {
  if (value instanceof Uint8Array) return value;
  if (typeof value !== "string") {
    throw new Error(
      `binary part "${name}": the value must be a string carrying the encoded bytes, got ${typeof value}`,
    );
  }
  switch (contentEncoding.toLowerCase()) {
    case "":
    case "base64":
      if (canonicalBase64) {
        try {
          return decodeBoundaryBase64(value, `multipart part ${JSON.stringify(name)}`);
        } catch {
          throw new Error(`binary part ${JSON.stringify(name)}: invalid canonical base64`);
        }
      }
      return decodeBase64(name, value, "base64");
    case "base64url":
      return decodeBase64(name, value.replace(/-/g, "+").replace(/_/g, "/"), "base64url");
    case "base16":
    case "hex": {
      if (!/^([0-9a-fA-F]{2})*$/.test(value)) {
        throw new Error(`binary part "${name}": invalid base16`);
      }
      const out = new Uint8Array(value.length / 2);
      for (let i = 0; i < out.length; i++) {
        out[i] = parseInt(value.slice(i * 2, i * 2 + 2), 16);
      }
      return out;
    }
    case "base32":
      return decodeBase32(name, value);
    case "quoted-printable":
      return decodeQuotedPrintable(name, value);
    case "binary":
    case "7bit":
    case "8bit":
      return new TextEncoder().encode(value);
    default:
      throw new Error(`binary part "${name}": unsupported contentEncoding "${contentEncoding}"`);
  }
}

function decodeBase64(name: string, s: string, label: string): Uint8Array {
  let bin: string;
  try {
    bin = atob(s);
  } catch {
    throw new Error(`binary part "${name}": invalid ${label}`);
  }
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

const BASE32_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

function decodeBase32(name: string, s: string): Uint8Array {
  const stripped = s.replace(/=+$/, "");
  let bits = 0;
  let acc = 0;
  const out: number[] = [];
  for (const ch of stripped) {
    const idx = BASE32_ALPHABET.indexOf(ch.toUpperCase());
    if (idx < 0) throw new Error(`binary part "${name}": invalid base32`);
    acc = (acc << 5) | idx;
    bits += 5;
    if (bits >= 8) {
      bits -= 8;
      out.push((acc >> bits) & 0xff);
    }
  }
  return new Uint8Array(out);
}

function decodeQuotedPrintable(name: string, s: string): Uint8Array {
  const withoutSoftBreaks = s.replace(/=\r?\n/g, "");
  const out: number[] = [];
  for (let i = 0; i < withoutSoftBreaks.length; i++) {
    const c = withoutSoftBreaks.charAt(i);
    if (c === "=") {
      const hex = withoutSoftBreaks.slice(i + 1, i + 3);
      if (!/^[0-9a-fA-F]{2}$/.test(hex)) {
        throw new Error(`binary part "${name}": invalid quoted-printable`);
      }
      out.push(parseInt(hex, 16));
      i += 2;
    } else {
      const code = c.charCodeAt(0);
      if (code > 0xff) {
        throw new Error(`binary part "${name}": invalid quoted-printable`);
      }
      out.push(code);
    }
  }
  return new Uint8Array(out);
}

// ---------------------------------------------------------------------------
// application/x-www-form-urlencoded
// ---------------------------------------------------------------------------

/**
 * Serializes body fields per the OAS `encoding` rules: each field's
 * style/explode/allowReserved come from the media type's encoding object
 * where present, defaulting to form/explode=true. Fields are serialized
 * with the same expansions as query parameters and joined in sorted-name
 * order for a deterministic body.
 */
export function buildURLEncodedBody(
  media: OpenAPIMediaType | null,
  fields: Record<string, unknown>,
  revision3 = false,
  openapiVersion = "3.0",
  dynamicProperties = false,
): string {
  const encoding = (media?.encoding ?? {}) as Record<string, Record<string, unknown>>;
  if (revision3) {
    return buildRevision3URLEncodedBody(media, fields, openapiVersion, dynamicProperties);
  }
  const units: string[] = [];
  for (const name of Object.keys(fields).sort()) {
    let style = "form";
    let explode: boolean | undefined;
    let allowReserved = false;
    const enc = asObject(encoding[name]);
    if (enc) {
      if (typeof enc.style === "string" && enc.style) style = enc.style;
      if (typeof enc.explode === "boolean") explode = enc.explode;
      allowReserved = enc.allowReserved === true;
    }
    try {
      units.push(...serializeQueryValue(name, fields[name], style, explode ?? style === "form", allowReserved));
    } catch (e) {
      throw new Error(`form field "${name}": ${(e as Error).message}`, { cause: e });
    }
  }
  return units.join("&");
}

function buildRevision3URLEncodedBody(
  media: OpenAPIMediaType | null,
  fields: Record<string, unknown>,
  openapiVersion: string,
  dynamicProperties: boolean,
): string {
  const rawSchema = mediaSchema(media);
  const schema = rawSchema && typeof rawSchema === "object" ? rawSchema : null;
  const properties = resolvedMultipartProperties(schema, new Set());
  const encoding = asObject(media?.encoding) ?? {};
  const units: string[] = [];
  for (const name of Object.keys(fields).sort()) {
    let property = dynamicProperties
      ? resolvedMultipartProperty(schema, name, new Set(), openapiVersion.startsWith("3.0"))
      : asObject(properties[name]);
    if (property === null && !dynamicProperties && staticPartBooleanLiteral(schema, name) === true) {
      property = {}; // true is the same unconstrained declaration as {}
    }
    const effective = effectiveRevision3PartSchema(property, openapiVersion.startsWith("3.0"));
    if (effective.nullable && fields[name] === null) {
      if (
        openapiVersion === "3.2.0"
        && resolveDeclaration(schema, false).requiresProperty(name)
      ) {
        throw new Error(`urlencoded property ${JSON.stringify(name)} is required and cannot be elided for null content-based encoding`);
      }
      continue; // §9.2: a JSON null value elides the nullable optional field
    }
    if (effective.schema !== null && effective.schema !== false) property = effective.schema;
    if (property === null) {
      throw new Error(`urlencoded property ${JSON.stringify(name)} has no declaration-defined carriage`);
    }
    const enc = asObject(encoding[name]);
    if (hasExplicitMultipartExpansion(enc)) {
      const style = typeof enc?.style === "string" && enc.style !== "" ? enc.style : "form";
      const explode = typeof enc?.explode === "boolean" ? enc.explode : style === "form";
      try {
        units.push(...serializeQueryValue(
          name,
          fields[name],
          style,
          explode,
          enc?.allowReserved === true,
          true,
          true,
        ));
      } catch (error: unknown) {
        throw new Error(
          `urlencoded property ${JSON.stringify(name)}: ${error instanceof Error ? error.message : String(error)}`,
          { cause: error },
        );
      }
      continue;
    }

    if (
      openapiVersion === "3.2.0"
      && !hasDeclaredSchemaType(property)
      && partSchemaDeclaresNoType(property, false)
    ) {
      const bytes = decodeBoundaryBase64(fields[name], `urlencoded property ${JSON.stringify(name)}`);
      units.push(`${formEncodeBytes(new TextEncoder().encode(name))}=${formEncodeBytes(bytes)}`);
      continue;
    }

    const declaredCT = typeof enc?.contentType === "string"
      ? parseSingleMultipartContentType(enc.contentType, name)
      : null;
    if (declaredCT === null && !hasDeclaredSchemaType(property) && partSchemaDeclaresNoType(property, openapiVersion.startsWith("3.0"))) {
      refuseTypelessPart("urlencoded", name, openapiVersion.startsWith("3.0"), property);
    }
    const selected = declaredCT
      ?? parseMediaType(defaultMultipartContentType(property, openapiVersion.startsWith("3.0")), true);
    requireSupportedCharset(selected, `urlencoded property ${JSON.stringify(name)}`);
    // R4 (ratified 2026-09-01). On this lane the whole compound rides one
    // field, so the media type the default table selects has to define the
    // bytes of the CONTAINER. The item-type default can select one that does
    // not — `text/plain` for primitive items, `application/octet-stream` for
    // encoded-string items — and no accepted edition says what an array looks
    // like there. This specification authors no bytes for that cell: the
    // property needs an explicit single concrete Encoding `contentType` in the
    // artifact or the `propertyMedia` choice, and without one the invocation
    // refuses before dispatch as the context-required species.
    if (declaredCT === null && urlencodedArrayNeedsPropertyMedia(property, openapiVersion.startsWith("3.0"))) {
      throw new Error(
        `urlencoded property ${JSON.stringify(name)} is an array whose item-type default `
        + `${selected.canonical} defines no serialization for the whole value; `
        + `configuration.propertyMedia.${name} is required`,
      );
    }
    let text: string;
    const encodedString = artifactEncodedString(property, openapiVersion.startsWith("3.0"));
    if (encodedString) {
      if (typeof fields[name] !== "string") {
        throw new Error(`urlencoded property ${JSON.stringify(name)} requires an artifact-encoded string`);
      }
      text = fields[name];
    } else if (isJSONMediaType(selected.base)) {
      text = JSON.stringify(fields[name] ?? null);
    } else if (selected.base === "text/plain") {
      text = openapiVersion === "3.2.0"
        ? serializeOpenAPI32NonJSONText(property, fields[name])
        : primitiveString(fields[name]);
    } else {
      throw new Error(
        `urlencoded property ${JSON.stringify(name)} has no serializer for ${selected.canonical}`,
      );
    }
    const bytes = encodeTextForMedia(text, selected.canonical, `urlencoded property ${JSON.stringify(name)}`);
    units.push(`${formEncodeBytes(new TextEncoder().encode(name))}=${formEncodeBytes(bytes)}`);
  }
  return units.join("&");
}

// formEncodeBytes percent-encodes one piece of an
// application/x-www-form-urlencoded body for the CONTENT lane — the lane the
// OAS reaches when an Encoding Object declares none of style, explode or
// allowReserved, and which it assigns to RFC 1866 Section 8.2.1 rather than to
// RFC 6570. The twin Go implementations'
// formURLEncodedEscape; the two lanes are SUPPOSED to spell a space
// differently, so nothing here should be converged onto the style lane.
//
// RFC 1866 Section 8.2.1 names the space ("space characters are replaced by
// `+', and then reserved characters are escaped as per [URL]") and delegates
// the rest to [URL] = RFC 1738; its following "that is, non-alphanumeric
// characters are replaced by `%HH'" gloss is stricter than the rule it presents
// itself as restating, and no accepted OAS edition asks for the stricter form.
// RFC 1738 Section 2.2 permits "only alphanumerics, the special characters
// `$-_.+!*'(),`, and reserved characters used for their reserved purposes"
// unencoded, permits encoding anything not required to be encoded, and requires
// `~` to be encoded (it is named unsafe). The set below is the WHATWG
// form-urlencoded serializer set, which lies inside that permission on every
// accepted edition and which OAS 3.0.4 / 3.1.1 Appendix E.3.2 (E.4.2 in 3.1.2)
// gives a SHOULD for.
//
// Which member of the permitted set to emit is this implementation's
// convention, not an OpenAPI family rule, and it is pinned by the shared twin
// case table (testdata/urlencoded-escaper-cases.json).
function formEncodeBytes(bytes: Uint8Array): string {
  let result = "";
  for (const byte of bytes) {
    if (
      (byte >= 0x41 && byte <= 0x5a)
      || (byte >= 0x61 && byte <= 0x7a)
      || (byte >= 0x30 && byte <= 0x39)
      || byte === 0x2a
      || byte === 0x2d
      || byte === 0x2e
      || byte === 0x5f
    ) {
      result += String.fromCharCode(byte);
    } else if (byte === 0x20) {
      result += "+";
    } else {
      result += `%${byte.toString(16).toUpperCase().padStart(2, "0")}`;
    }
  }
  return result;
}

// ---------------------------------------------------------------------------
// Success responses, Accept, streaming capability (§8, §9.2)
// ---------------------------------------------------------------------------

/**
 * A literal 2xx entry or `2XX` is intrinsically successful. `default` also
 * participates in the possible success surface because it can govern a 2xx
 * status that has no more-specific declaration.
 */
export function isSuccessResponseKey(key: string): boolean {
  if (key === "2XX") return true;
  return /^2[0-9][0-9]$/.test(key);
}

/**
 * The declared CONCRETE media types of the operation's success responses,
 * normalized, deduplicated, and sorted (membership is normative, ordering
 * is not). Media ranges are excluded: they are not concrete.
 */
/**
 * Two SUCCESS RESPONSES may declare one identity in different spellings; that
 * is not a collision (§9.2's unit is one content map), so the advertised set
 * carries the identity once and takes the smallest spelling rather than
 * whichever key was enumerated first.
 */
function advertise(seen: Map<string, string>, parsed: ParsedMediaType): void {
  const existing = seen.get(parsed.identity);
  if (existing === undefined || parsed.canonical < existing) {
    seen.set(parsed.identity, parsed.canonical);
  }
}

export function successMediaTypes(
  op: OpenAPIOperation | null | undefined,
  revision3 = false,
  responseFidelity = false,
): string[] {
  if (!op?.responses) return [];
  const seen = new Map<string, string>();
  const hasRange = Object.hasOwn(op.responses, "2XX");
  const exactSuccesses = new Set(
    Object.keys(op.responses).filter((key) => /^2[0-9][0-9]$/.test(key)),
  );
  for (const [key, resp] of Object.entries(op.responses)) {
    const defaultCanGovernSuccess = key === "default" && !hasRange && exactSuccesses.size < 100;
    if (!isSuccessResponseKey(key) && !defaultCanGovernSuccess) continue;
    const content = (resp as OpenAPIResponse | undefined)?.content;
    if (!content) continue;
    // §9.2 normalized collision, confined: a colliding parsed identity can
    // govern no response match, so it is not an available representation and
    // is not advertised -- advertising it would invite exactly the response
    // the decode lane must refuse. Non-colliding siblings still advertise.
    const colliding = normalizedMediaCollisions(content, revision3);
    for (const mt of Object.keys(content)) {
      if (colliding.has(mt)) continue;
      try {
        const parsed = parseMediaType(mt, revision3);
        advertise(seen, parsed);
      } catch {
        if (revision3 && responseFidelity) {
          try {
            advertise(seen, parseMediaRange(mt, true));
          } catch { /* malformed keys are handled when they govern */ }
        }
      }
    }
  }
  return [...seen.values()].sort();
}

/**
 * Advertises the declared concrete media types of the operation's success
 * responses. No declaration means no invented Accept preference.
 */
export function acceptHeader(
  op: OpenAPIOperation,
  revision3 = false,
  responseFidelity = false,
): string {
  const types = successMediaTypes(op, revision3, responseFidelity);
  return types.join(", ");
}

/** The response declaration governing one actual status: exact, range, then default. */
export function governingResponse(
  op: OpenAPIOperation,
  status: number,
): { key: string; response: OpenAPIResponse } | null {
  const responses = op.responses ?? {};
  const exact = String(status);
  const range = `${Math.floor(status / 100)}XX`;
  for (const key of [exact, range, "default"]) {
    const response = responses[key];
    if (response && typeof response === "object") return { key, response };
  }
  return null;
}

/**
 * Selects the most-specific declared media identity compatible with the
 * actual Content-Type. A declaration's parameters must be a subset of the
 * actual parameters; equally specific alternatives are ambiguous.
 */
export function governingResponseMedia(
  response: OpenAPIResponse,
  actualContentType: string | null,
  revision3 = false,
  responseFidelity = false,
): string | null {
  return governingResponseMediaMatch(
    response,
    actualContentType,
    revision3,
    responseFidelity,
  )?.declared.canonical ?? null;
}

export interface GoverningResponseMediaMatch {
  mediaKey: string;
  declared: ParsedMediaType | ParsedMediaRange;
  media: OpenAPIMediaType;
}

/**
 * Selects the governing Media Type Object as well as its parsed declaration.
 * Revision 4 lets an actual concrete Content-Type instantiate an artifact-
 * declared range; earlier revisions inventory ranges only for collision
 * detection and never select them.
 */
export function governingResponseMediaMatch(
  response: OpenAPIResponse,
  actualContentType: string | null,
  revision3 = false,
  responseFidelity = false,
): GoverningResponseMediaMatch | null {
  const content = response.content ?? {};
  if (Object.keys(content).length === 0) return null;
  if (!actualContentType) {
    throw new Error("response declaration has content alternatives but the response omits Content-Type");
  }
  const actual = parseMediaType(actualContentType, revision3);
  const matches: Array<{
    mediaKey: string;
    declared: ParsedMediaType | ParsedMediaRange;
    media: OpenAPIMediaType;
    rangeSpecificity: number;
    parameterSpecificity: number;
  }> = [];
  const colliding = normalizedMediaCollisions(content, revision3);
  for (const [key, media] of Object.entries(content)) {
    let declared: ParsedMediaType | ParsedMediaRange;
    let rangeSpecificity = 2;
    try {
      declared = parseMediaType(key, revision3);
    } catch (error: unknown) {
      if (revision3) {
        try {
          const parsedRange = parseMediaRange(key, true);
          declared = parsedRange;
          rangeSpecificity = parsedRange.specificity;
        } catch (_rangeError: unknown) {
          // Preserve the concrete parse failure for a malformed declaration.
          throw error;
        }
      } else {
        throw error;
      }
    }
    if (colliding.has(key)) {
      // §9.2 normalized collision, confined: no response match may be
      // governed by this parsed identity, so the declaration competes for
      // nothing. A concrete response that DOES denote the colliding identity
      // then matches nothing and is loud below; a response denoting a
      // non-colliding sibling decodes unaffected.
      continue;
    }
    if (rangeSpecificity < 2 && !responseFidelity) continue;
    if (rangeSpecificity === 2) {
      if (declared.base !== actual.base) continue;
    } else if (!mediaRangeMatches(declared as ParsedMediaRange, actual)) {
      continue;
    }
    if (Object.entries(declared.params).every(([name, value]) => actual.params[name] === value)) {
      matches.push({
        mediaKey: key,
        declared,
        media,
        rangeSpecificity,
        parameterSpecificity: Object.keys(declared.params).length,
      });
    }
  }
  if (matches.length === 0) {
    if (collidesWithNormalizedIdentity(colliding, actual, revision3)) {
      throw new Error(
        `response Content-Type ${JSON.stringify(actualContentType)} denotes a parsed media identity more than one content-map key declares (normalized collision), so no response match may be governed by it`,
      );
    }
    throw new Error(
      `response Content-Type ${JSON.stringify(actualContentType)} matches no media declaration in the governing Response Object`,
    );
  }
  matches.sort((a, b) => b.rangeSpecificity - a.rangeSpecificity
    || b.parameterSpecificity - a.parameterSpecificity
    || a.declared.identity.localeCompare(b.declared.identity));
  if (
    matches.length > 1
    && matches[0]?.rangeSpecificity === matches[1]?.rangeSpecificity
    && matches[0]?.parameterSpecificity === matches[1]?.parameterSpecificity
  ) {
    throw new Error(
      `response Content-Type ${JSON.stringify(actualContentType)} ambiguously matches equally specific media declarations`,
    );
  }
  const selected = matches[0];
  return selected
    ? { mediaKey: selected.mediaKey, declared: selected.declared, media: selected.media }
    : null;
}

/**
 * The §8 static capability: an operation is streaming-capable iff
 * text/event-stream appears among the declared media types of its success
 * responses.
 */
export function isStreamingCapable(
  op: OpenAPIOperation,
  revision3 = false,
  responseFidelity = false,
): boolean {
  const media = successMediaTypes(op, revision3, responseFidelity);
  if (!revision3) return media.includes("text/event-stream");
  return media.some((value) => {
    try { return parseMediaType(value, true).base === "text/event-stream"; } catch {
      if (!responseFidelity) return false;
      try {
        const range = parseMediaRange(value, true);
        // Static capability asks whether some concrete event-stream response
        // could satisfy the declaration. Declaration parameters constrain the
        // eventual response, but do not make that possibility disappear.
        return range.base === "text/*" || range.base === "*/*";
      } catch { return false; }
    }
  });
}
