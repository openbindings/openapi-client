/** Returns the distinct authored identities named by one effective security list. */
export function openAPI32SecurityRequirementNames(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  const names = new Set<string>();
  for (const member of raw) {
    const requirement = asRecord(member);
    for (const name of Object.keys(requirement ?? {})) names.add(name);
  }
  return [...names].sort(codePointCompare);
}

/** A component-name match wins; ./ explicitly selects the URI interpretation. */
export function openAPI32SecurityNameKind(
  name: string,
  entryComponentNames: ReadonlySet<string>,
  referringComponentNames: ReadonlySet<string>,
  referringResource: boolean,
): "entry" | "referring" | "uri" {
  if (entryComponentNames.has(name)) return "entry";
  if (referringResource && !name.startsWith("./") && referringComponentNames.has(name)) {
    return "referring";
  }
  return "uri";
}

export function openAPI32SecuritySchemeReference(raw: unknown): string | undefined {
  const ref = asRecord(raw)?.$ref;
  return typeof ref === "string" && ref !== "" ? ref : undefined;
}

/** Admits the closed Security Scheme Object shapes used by request security. */
export function openAPI32SecurityScheme(raw: unknown): Record<string, unknown> | null {
  const scheme = asRecord(raw);
  if (!scheme) return null;
  switch (scheme.type) {
    case "apiKey":
      return typeof scheme.name === "string" && scheme.name !== ""
        && typeof scheme.in === "string" && ["query", "header", "cookie"].includes(scheme.in)
        ? scheme
        : null;
    case "http":
      return typeof scheme.scheme === "string" && scheme.scheme !== "" ? scheme : null;
    case "oauth2":
      return asRecord(scheme.flows) ? scheme : null;
    case "openIdConnect":
      return typeof scheme.openIdConnectUrl === "string" && scheme.openIdConnectUrl !== ""
        ? scheme
        : null;
    case "mutualTLS":
      return scheme;
    default:
      return null;
  }
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function codePointCompare(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}
