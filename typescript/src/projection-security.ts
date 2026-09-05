import type {
  OpenAPIDocument,
  OpenAPIOperation,
  OpenAPIParameter,
  OpenAPISecurityScheme,
} from "./types.js";
import {
  openAPICredentialCollision,
  openAPICredentialDestinations,
  type OpenAPINamedSecurityScheme,
} from "./security-wire.js";
import { REFERRING_SECURITY_SCHEMES_MARKER } from "./projection-binding-origins.js";

export interface SecurityAlternativeDisposition {
  status: "represented" | "excluded" | "invalid";
  rule?: string;
  message?: string;
}

export function effectiveSecurityRequirements(
  document: OpenAPIDocument,
  operation: OpenAPIOperation,
): Array<Record<string, unknown>> | null {
  if (Array.isArray(operation.security)) return operation.security as Array<Record<string, unknown>>;
  return Array.isArray(document.security)
    ? document.security as Array<Record<string, unknown>>
    : null;
}

/** Static declaration disposition shared by native projection and coverage. */
export function securityAlternativeDisposition(
  document: OpenAPIDocument,
  operation: OpenAPIOperation,
  parameters: OpenAPIParameter[],
  authoredIndex: number,
  bindingSpec: string,
  method: string,
): SecurityAlternativeDisposition {
  const requirements = effectiveSecurityRequirements(document, operation) ?? [];
  const requirement = asRecord(requirements[authoredIndex]);
  if (!requirement) return malformedDisposition(document, "security alternative is not an object");
  const names = Object.keys(requirement);
  if (names.length === 0) return { status: "represented" };

  const schemes: OpenAPINamedSecurityScheme[] = [];
  for (const name of names) {
    const scopes = requirement[name];
    const scheme = securitySchemeForScope(document, operation, name, "entry")
      ?? securitySchemeForScope(document, operation, name, "referring");
    if (!scheme || malformedSecurityScheme(scheme) || !Array.isArray(scopes)
      || scopes.some((value) => typeof value !== "string")) {
      return malformedDisposition(document, `security alternative names malformed or unresolved scheme ${JSON.stringify(name)}`);
    }
    if (document.openapi?.startsWith("3.0.") && scheme.type === "mutualTLS") {
      return { status: "excluded", rule: "P-04", message: "mutualTLS is outside the OpenAPI 3.0 Security Scheme type set" };
    }
    if (document.openapi?.startsWith("3.0.") && !securitySchemeUsesScopes(scheme) && scopes.length > 0) {
      return { status: "excluded", rule: "P-04", message: "OpenAPI 3.0 non-OAuth security scheme carries scopes" };
    }
    schemes.push({ name, scheme });
  }

  if (method.toLowerCase() === "trace" && schemes.some(({ scheme }) => schemeEmitsSensitiveField(scheme))) {
    return {
      status: "excluded",
      rule: bindingSpec.endsWith("3.0@1") ? "P-38" : bindingSpec.endsWith("3.1@1") ? "P-39" : "P-41",
      message: "TRACE cannot emit selected credentials or cookies",
    };
  }

  const requiredParameters = parameters.filter((parameter) => parameter.required === true);
  const rawCookieCredential = schemes.some(({ scheme }) => scheme.type === "apiKey"
    && scheme.in === "header" && scheme.name?.toLowerCase() === "cookie");
  const structuredCookieCredential = schemes.some(({ scheme }) => scheme.type === "apiKey"
    && scheme.in === "cookie");
  const requiredStructuredCookie = requiredParameters.some((parameter) => parameter.in === "cookie");
  const requiredRawCookie = requiredParameters.some((parameter) => parameter.in === "header"
    && parameter.name?.toLowerCase() === "cookie");
  if ((rawCookieCredential && (structuredCookieCredential || requiredStructuredCookie))
    || (structuredCookieCredential && requiredRawCookie)) {
    return {
      status: "excluded",
      rule: bindingSpec.endsWith("3.0@1") ? "P-28" : "P-29",
      message: "security alternative would mix raw and structured Cookie sources",
    };
  }

  const collision = openAPICredentialCollision(
    openAPICredentialDestinations(schemes),
    parameters,
    { header: new Set(), query: new Set(), cookie: new Set() },
  );
  if (collision) {
    return {
      status: "excluded",
      rule: bindingSpec.endsWith("3.0@1") ? "P-32" : "P-33",
      message: collision,
    };
  }
  return alternativeUsableAtScope(document, operation, parameters, authoredIndex, "entry")
    || alternativeUsableAtScope(document, operation, parameters, authoredIndex, "referring")
    ? { status: "represented" }
    : { status: "excluded", rule: "P-04", message: "security alternative is not executable" };
}

export function securityCoverageRequirements(
  document: OpenAPIDocument,
  operation: OpenAPIOperation,
  parameters: OpenAPIParameter[],
  bindingSpec = "openbindings.openapi-3.1@1",
  method = "",
): string[] {
  const requirements = effectiveSecurityRequirements(document, operation);
  if (!requirements || requirements.length === 0) return [];
  const selectable = requirements.filter((_, index) =>
    securityAlternativeDisposition(document, operation, parameters, index, bindingSpec, method).status !== "excluded");
  const result: string[] = [];
  if (selectable.length > 1) result.push("configuration.security");
  const entry = requirements.some((_, index) =>
    alternativeUsableAtScope(document, operation, parameters, index, "entry"));
  const referring = requirements.some((_, index) =>
    alternativeUsableAtScope(document, operation, parameters, index, "referring"));
  if (!entry && referring) result.push("configuration.implicitConnectionScope");
  return result;
}

function alternativeUsableAtScope(
  document: OpenAPIDocument,
  operation: OpenAPIOperation,
  parameters: OpenAPIParameter[],
  index: number,
  scope: "entry" | "referring",
): boolean {
  const requirement = asRecord((effectiveSecurityRequirements(document, operation) ?? [])[index]);
  if (!requirement) return false;
  const schemes: OpenAPINamedSecurityScheme[] = [];
  for (const [name, rawScopes] of Object.entries(requirement)) {
    const scheme = securitySchemeForScope(document, operation, name, scope);
    if (!scheme || malformedSecurityScheme(scheme) || !Array.isArray(rawScopes)
      || rawScopes.some((value) => typeof value !== "string")) return false;
    if (document.openapi?.startsWith("3.0.") && scheme.type === "mutualTLS") return false;
    if (document.openapi?.startsWith("3.0.") && !securitySchemeUsesScopes(scheme) && rawScopes.length > 0) return false;
    schemes.push({ name, scheme });
  }
  return openAPICredentialCollision(
    openAPICredentialDestinations(schemes),
    parameters,
    { header: new Set(), query: new Set(), cookie: new Set() },
  ) === "";
}

function securitySchemeForScope(
  document: OpenAPIDocument,
  operation: OpenAPIOperation,
  name: string,
  scope: "entry" | "referring",
): OpenAPISecurityScheme | null {
  if (scope === "referring") {
    const referring = asRecord(operation[REFERRING_SECURITY_SCHEMES_MARKER]);
    const candidate = asRecord(referring?.[name]);
    if (candidate) return candidate as OpenAPISecurityScheme;
  }
  const components = asRecord(document.components);
  const schemes = asRecord(components?.securitySchemes);
  const candidate = asRecord(schemes?.[name]);
  return candidate ? candidate as OpenAPISecurityScheme : null;
}

function malformedSecurityScheme(scheme: OpenAPISecurityScheme): string {
  switch (scheme.type) {
    case "apiKey":
      return typeof scheme.name === "string" && scheme.name !== ""
        && ["header", "query", "cookie"].includes(scheme.in ?? "") ? ""
        : "apiKey Security Scheme Object requires a name and destination";
    case "http":
      return typeof scheme.scheme === "string" && scheme.scheme !== "" ? ""
        : "HTTP Security Scheme Object requires a scheme";
    case "oauth2":
      return asRecord(scheme.flows) ? "" : "OAuth 2.0 Security Scheme Object requires flows";
    case "openIdConnect":
      return typeof scheme.openIdConnectUrl === "string" && scheme.openIdConnectUrl !== "" ? ""
        : "OpenID Connect Security Scheme Object requires openIdConnectUrl";
    case "mutualTLS":
      return "";
    default:
      return `Security Scheme Object type ${JSON.stringify(scheme.type)} is not admitted`;
  }
}

function malformedDisposition(document: OpenAPIDocument, message: string): SecurityAlternativeDisposition {
  return document.openapi === "3.2.0"
    ? { status: "invalid", message }
    : { status: "excluded", rule: "P-04", message };
}

function securitySchemeUsesScopes(scheme: OpenAPISecurityScheme): boolean {
  return scheme.type === "oauth2" || scheme.type === "openIdConnect";
}

function schemeEmitsSensitiveField(scheme: OpenAPISecurityScheme): boolean {
  return scheme.type !== "mutualTLS"
    && ["apiKey", "http", "oauth2", "openIdConnect"].includes(scheme.type ?? "");
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}
