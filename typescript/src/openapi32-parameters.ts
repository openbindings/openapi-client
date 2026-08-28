import type {
  OpenAPIDocument,
  OpenAPIMediaType,
  OpenAPIOperation,
  OpenAPIParameter,
  OpenAPIPathItem,
} from "./types.js";
import { isJSONMediaType, buildURLEncodedBody, parseMediaType } from "./media.js";
import {
  checkPathTemplateDeclaration,
  effectiveParameterDeclarationRows,
  formStyleCookieMultiValueProof,
  queryEscape,
  resolvedParameterStyleLaneUndefinedExpansionMember,
  serializationMethod,
  serializeCookieValue,
  serializeQueryValue,
} from "./params.js";
import { resolveDeclaration } from "./resolved-declaration.js";

const OPENAPI32_PARAMETER_LOCATIONS = new Set([
  "path",
  "query",
  "querystring",
  "header",
  "cookie",
]);

export interface OpenAPI32ParameterSerializationMethod {
  style: string;
  explode: boolean;
}

/**
 * Applies the complete 3.2 Parameter Object admission gate to one resolved
 * operation closure. This is deliberately artifact-local: legacy documents
 * continue through their existing whole-document validation lane.
 */
export function validateOpenAPI32OperationParameters(
  document: OpenAPIDocument,
  path: string,
  pathItem: OpenAPIPathItem,
  operation: OpenAPIOperation,
): void {
  const rows = effectiveParameterDeclarationRows(pathItem, operation);
  const seen = new Set<string>();
  let queryCount = 0;
  let queryStringCount = 0;

  for (const parameter of rows) {
    validateOpenAPI32ParameterObject(parameter);
    const identity = `${parameter.in}\u0000${parameter.name}`;
    if (seen.has(identity)) {
      throw new Error(`operation has duplicate effective parameter identity ${parameter.in}/${parameter.name}`);
    }
    seen.add(identity);
    if (parameter.in === "query") queryCount += 1;
    if (parameter.in === "querystring") queryStringCount += 1;
    validateOpenAPI32ParameterSerialization(parameter);
    const member = resolvedParameterStyleLaneUndefinedExpansionMember(parameter, false);
    if (member !== null) {
      throw new Error(`resolved compound member ${JSON.stringify(member)} has no defined style expansion`);
    }
    if (formStyleCookieMultiValueProof(parameter, false)) {
      throw new Error("form-style exploded cookie declaration always produces multiple cookie pairs");
    }
  }

  if (queryStringCount > 1) {
    throw new Error("operation has more than one effective querystring parameter");
  }
  if (queryStringCount > 0 && queryCount > 0) {
    throw new Error("querystring and ordinary query parameters are mutually exclusive");
  }

  const pathFailure = checkPathTemplateDeclaration(path, rows, true);
  if (pathFailure) throw new Error(pathFailure);
  const collision = equivalentOpenAPI32Path(document.paths, path);
  if (collision) {
    throw new Error(`path ${JSON.stringify(path)} has the same templated hierarchy as ${JSON.stringify(collision)}`);
  }
}

/** Applies the 3.2 closed style/location/shape table to one declaration. */
export function validateOpenAPI32ParameterSerialization(parameter: OpenAPIParameter): void {
  if (parameter.in === "querystring") {
    validateOpenAPI32QueryStringMedia(parameter);
    return;
  }
  if (Object.hasOwn(parameter, "content")) return;
  if (Object.hasOwn(parameter, "style") && (typeof parameter.style !== "string" || parameter.style === "")) {
    throw new Error(`parameter ${JSON.stringify(parameter.name)} declares an invalid style`);
  }
  if (Object.hasOwn(parameter, "explode") && typeof parameter.explode !== "boolean") {
    throw new Error(`parameter ${JSON.stringify(parameter.name)} declares a non-boolean explode`);
  }

  const method = openAPI32ParameterSerializationMethod(parameter);
  const resolved = resolveDeclaration(
    parameter.schema as Record<string, unknown> | boolean | undefined,
    false,
  );
  switch (parameter.in) {
    case "path":
      if (!["simple", "label", "matrix"].includes(method.style)) {
        throw new Error(`style ${JSON.stringify(method.style)} is not defined for path parameters`);
      }
      break;
    case "header":
      if (method.style !== "simple") {
        throw new Error(`style ${JSON.stringify(method.style)} is not defined for header parameters`);
      }
      break;
    case "cookie":
      if (method.style !== "form" && method.style !== "cookie") {
        throw new Error(`style ${JSON.stringify(method.style)} is not defined for cookie parameters`);
      }
      break;
    case "query":
      if (method.style === "form") break;
      if (method.style === "spaceDelimited" || method.style === "pipeDelimited") {
        if (method.explode) throw new Error(`query style ${JSON.stringify(method.style)} has no explode=true cell`);
        if (resolved.declaresOnly("null", "boolean", "number", "integer", "string")) {
          throw new Error(`query style ${JSON.stringify(method.style)} is defined only for arrays or objects`);
        }
        break;
      }
      if (method.style === "deepObject") {
        if (resolved.declaresOnly("null", "boolean", "number", "integer", "string", "array")) {
          throw new Error("query style deepObject is defined only for objects");
        }
        break;
      }
      throw new Error(`style ${JSON.stringify(method.style)} is not defined for query parameters`);
    default:
      throw new Error(`parameter ${JSON.stringify(parameter.name)} declares unsupported location ${JSON.stringify(parameter.in)}`);
  }

}

/**
 * Returns 3.2 serialization defaults. The document explicitly defaults the
 * `cookie` style to explode=true; this intentionally differs from the
 * pre-fix Go M5 implementation (Fable adjudication, 2026-08-28).
 */
export function openAPI32ParameterSerializationMethod(
  parameter: OpenAPIParameter,
): OpenAPI32ParameterSerializationMethod {
  const compatible = serializationMethod(parameter);
  const style = compatible.style;
  if (style === "deepObject") return { style, explode: true };
  if (!Object.hasOwn(parameter, "explode") && style === "cookie") {
    return { style, explode: true };
  }
  return compatible;
}

/** Serializes one 3.2 query style with the Appendix C protected delimiters. */
export function serializeOpenAPI32QueryValue(
  name: string,
  value: unknown,
  style: string,
  explode: boolean,
  allowReserved: boolean,
): string[] {
  const effectiveExplode = style === "deepObject" ? true : explode;
  const units = serializeQueryValue(
    name,
    value,
    style,
    effectiveExplode,
    allowReserved,
    true,
    true,
  );
  if (style === "pipeDelimited") return units.map((unit) => unit.replaceAll("|", "%7C"));
  if (style === "deepObject") {
    return units.map((unit) => unit.replaceAll("[", "%5B").replaceAll("]", "%5D"));
  }
  return units;
}

/** Serializes the 3.2 RFC 6265 `cookie` style as one or more cookie pairs. */
export function serializeOpenAPI32CookieValue(
  name: string,
  value: unknown,
  style: string,
  explode: boolean,
): string[] {
  if (style !== "form" && style !== "cookie") {
    throw new Error(`style ${JSON.stringify(style)} is not defined for cookie parameters`);
  }
  const units = serializeCookieValue(name, value, "form", explode);
  validateOpenAPI32CookieUnits(units);
  return units;
}

/** Serializes the sole querystring value as the complete query component. */
export function serializeOpenAPI32QueryStringParameter(
  parameter: OpenAPIParameter,
  value: unknown,
): string {
  const content = asRecord(parameter.content);
  if (!content || Object.keys(content).length !== 1) {
    throw new Error("querystring parameter must declare exactly one content media type");
  }
  const mediaType = Object.keys(content)[0]!;
  const parsed = parseMediaType(mediaType, true);
  const media = asRecord(content[mediaType]) as OpenAPIMediaType | null;
  if (parsed.base === "application/x-www-form-urlencoded") {
    const fields = asRecord(value);
    if (!fields) {
      throw new Error(`querystring form content requires an object value, got ${value === null ? "null" : typeof value}`);
    }
    return buildURLEncodedBody(media, fields, true, "3.2.0", false);
  }
  let serialized: string;
  if (isJSONMediaType(parsed.base)) {
    serialized = stringifyOpenAPI32JSON(value);
  } else if (parsed.base === "text/plain") {
    if (typeof value !== "string") {
      throw new Error(`querystring text content requires a string value, got ${value === null ? "null" : typeof value}`);
    }
    serialized = value;
  } else {
    throw new Error(`querystring content ${JSON.stringify(mediaType)} has no incorporated serialization`);
  }
  return queryEscape(serialized, false, true);
}

/** Validates each serialized contribution under RFC 6265's cookie grammar. */
export function validateOpenAPI32CookieUnits(units: readonly string[]): void {
  for (const unit of units) {
    const separator = unit.indexOf("=");
    if (separator <= 0) throw new Error(`serialized cookie contribution ${JSON.stringify(unit)} is not name=value`);
    const name = unit.slice(0, separator);
    const value = unit.slice(separator + 1);
    if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u.test(name)) {
      throw new Error(`serialized cookie name ${JSON.stringify(name)} is not an RFC 6265 cookie-name`);
    }
    const bytes = new TextEncoder().encode(value);
    if (![...bytes].every((byte) => byte === 0x21
      || (byte >= 0x23 && byte <= 0x2b)
      || (byte >= 0x2d && byte <= 0x3a)
      || (byte >= 0x3c && byte <= 0x5b)
      || (byte >= 0x5d && byte <= 0x7e))) {
      throw new Error(`serialized cookie value for ${JSON.stringify(name)} is not an RFC 6265 cookie-value`);
    }
  }
}

function validateOpenAPI32ParameterObject(parameter: OpenAPIParameter): void {
  if (!parameter || typeof parameter.name !== "string" || parameter.name === "" || typeof parameter.in !== "string") {
    throw new Error("effective Parameter Object must declare non-empty string name and in fields");
  }
  if (!OPENAPI32_PARAMETER_LOCATIONS.has(parameter.in)) {
    throw new Error(`parameter ${JSON.stringify(parameter.name)} declares unsupported location ${JSON.stringify(parameter.in)}`);
  }
  const hasSchema = Object.hasOwn(parameter, "schema");
  const hasContent = Object.hasOwn(parameter, "content");
  if (hasSchema === hasContent) {
    throw new Error(`parameter ${JSON.stringify(parameter.name)} must use exactly one of schema or content`);
  }
  if (parameter.in === "path" && parameter.required !== true) {
    throw new Error(`path parameter ${JSON.stringify(parameter.name)} must declare required: true`);
  }
  if (hasContent) {
    const content = asRecord(parameter.content);
    if (!content || Object.keys(content).length !== 1) {
      throw new Error(`parameter ${JSON.stringify(parameter.name)} content must contain exactly one media type`);
    }
  }
  if (parameter.in === "querystring") {
    if (!hasContent) throw new Error(`querystring parameter ${JSON.stringify(parameter.name)} must use content`);
    for (const field of ["schema", "style", "explode", "allowReserved", "allowEmptyValue"]) {
      if (Object.hasOwn(parameter, field)) {
        throw new Error(`querystring parameter ${JSON.stringify(parameter.name)} declares forbidden schema-form field ${JSON.stringify(field)}`);
      }
    }
  }
}

function validateOpenAPI32QueryStringMedia(parameter: OpenAPIParameter): void {
  const content = asRecord(parameter.content);
  if (!content || Object.keys(content).length !== 1) {
    throw new Error("querystring content is absent");
  }
  const mediaType = Object.keys(content)[0]!;
  const parsed = parseMediaType(mediaType, true);
  const media = asRecord(content[mediaType]);
  if (media && Object.hasOwn(media, "itemSchema")) {
    throw new Error(`querystring content ${JSON.stringify(mediaType)} selects a sequential representation`);
  }
  if (isJSONMediaType(parsed.base) || parsed.base === "text/plain") return;
  if (parsed.base !== "application/x-www-form-urlencoded") {
    throw new Error(`querystring content ${JSON.stringify(mediaType)} has no incorporated serialization`);
  }
  if (!media || !Object.hasOwn(media, "schema")) {
    throw new Error("querystring form content has no application-value schema");
  }
  const resolved = resolveDeclaration(
    media.schema as Record<string, unknown> | boolean | undefined,
    false,
  );
  if (resolved.declaresOnly("null", "boolean", "number", "integer", "string", "array")) {
    throw new Error("querystring form content requires an object application value");
  }
}

function equivalentOpenAPI32Path(
  paths: Record<string, OpenAPIPathItem> | undefined,
  selected: string,
): string | undefined {
  const selectedHierarchy = normalizedPathHierarchy(selected);
  if (!selectedHierarchy.templated) return undefined;
  for (const candidate of Object.keys(paths ?? {})) {
    if (candidate === selected) continue;
    const hierarchy = normalizedPathHierarchy(candidate);
    if (hierarchy.templated && hierarchy.value === selectedHierarchy.value) return candidate;
  }
  return undefined;
}

function normalizedPathHierarchy(path: string): { value: string; templated: boolean } {
  let templated = false;
  return {
    value: path.replace(/\{[^}]*\}/gu, () => {
      templated = true;
      return "{}";
    }),
    get templated() { return templated; },
  };
}

function stringifyOpenAPI32JSON(value: unknown): string {
  const serialized = JSON.stringify(value);
  if (serialized === undefined) throw new Error("value has no JSON representation");
  return serialized.replace(/[<>&\u2028\u2029]/gu, (character) => {
    const code = character.codePointAt(0)!;
    return `\\u${code.toString(16).padStart(4, "0")}`;
  });
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}
