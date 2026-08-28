import type { OpenAPIParameter, OpenAPISecurityScheme } from "./types.js";

export interface OpenAPINamedSecurityScheme {
  name: string;
  scheme: OpenAPISecurityScheme;
}

export interface OpenAPICredentialPlacement {
  channel: "header" | "query" | "cookie";
  name: string;
  value: string;
}

export interface OpenAPIBasicCredential {
  username: string;
  password: string;
}

export interface OpenAPICredentialSource {
  apiKey(name: string): string | undefined;
  basic(name: string): OpenAPIBasicCredential | undefined;
  bearer(name: string): string | undefined;
  accessToken(name: string): string | undefined;
}

/** Constructs the wire placements for one selected Security Requirement. */
export function buildOpenAPICredentialPlacements(
  schemes: readonly OpenAPINamedSecurityScheme[],
  source: OpenAPICredentialSource,
): OpenAPICredentialPlacement[] {
  const placements: OpenAPICredentialPlacement[] = [];
  const add = (
    channel: OpenAPICredentialPlacement["channel"],
    name: string,
    value: string,
  ): void => {
    placements.push({ channel, name, value });
  };

  for (const { scheme, name } of schemes) {
    switch (scheme.type) {
      case "apiKey": {
        const value = source.apiKey(name);
        if (!value || !scheme.name) break;
        if (scheme.in === "header" || scheme.in === "query" || scheme.in === "cookie") {
          if (scheme.in === "cookie") validateOpenAPICookieCredential(scheme.name, value);
          add(scheme.in, scheme.name, value);
        }
        break;
      }
      case "http": {
        const token = (scheme.scheme ?? "").toLowerCase();
        if (token === "bearer") {
          const value = source.bearer(name);
          if (value) {
            validateOpenAPIBearerToken(`bearer credential ${JSON.stringify(name)}`, value);
            add("header", "Authorization", `Bearer ${value}`);
          }
        } else if (token === "basic") {
          const value = source.basic(name);
          if (value) {
            validateOpenAPIBasicCredential(name, value);
            add("header", "Authorization", `Basic ${btoa(`${value.username}:${value.password}`)}`);
          }
        }
        break;
      }
      case "oauth2":
      case "openIdConnect": {
        const value = source.accessToken(name) || source.bearer(name);
        if (value) {
          validateOpenAPIBearerToken(`access token for ${JSON.stringify(name)}`, value);
          add("header", "Authorization", `Bearer ${value}`);
        }
        break;
      }
    }
  }
  return placements;
}

/** Returns the destinations owned by one selected requirement before values exist. */
export function openAPICredentialDestinations(
  schemes: readonly OpenAPINamedSecurityScheme[],
): OpenAPICredentialPlacement[] {
  const placements: OpenAPICredentialPlacement[] = [];
  for (const { scheme } of schemes) {
    if (
      scheme.type === "apiKey"
      && scheme.name
      && (scheme.in === "header" || scheme.in === "query" || scheme.in === "cookie")
    ) {
      placements.push({ channel: scheme.in, name: scheme.name, value: "" });
    } else if (scheme.type === "oauth2" || scheme.type === "openIdConnect"
      || (scheme.type === "http" && ["basic", "bearer"].includes((scheme.scheme ?? "").toLowerCase()))) {
      placements.push({ channel: "header", name: "Authorization", value: "" });
    }
  }
  return placements;
}

/** Validates the incorporated HTTP grammar for a basic credential. */
export function validateOpenAPIBasicCredential(name: string, credential: OpenAPIBasicCredential): void {
  if (credential.username.includes(":")
    || !validBasicCredentialText(credential.username)
    || !validBasicCredentialText(credential.password)) {
    throw new Error(`basic credential ${JSON.stringify(name)} violates RFC 7617 constraints`);
  }
}

/** Validates an RFC 6750 bearer or access token. */
export function validateOpenAPIBearerToken(subject: string, token: string): void {
  if (!/^[A-Za-z0-9\-._~+/]+={0,}$/u.test(token)) {
    throw new Error(`${subject} is not an RFC 6750 b64token`);
  }
}

/** Validates an API-key value destined for RFC 6265 cookie carriage. */
export function validateOpenAPICookieCredential(name: string, value: string): void {
  const bytes = new TextEncoder().encode(value);
  const valid = [...bytes].every((byte) => byte === 0x21
    || (byte >= 0x23 && byte <= 0x2b)
    || (byte >= 0x2d && byte <= 0x3a)
    || (byte >= 0x3c && byte <= 0x5b)
    || (byte >= 0x5d && byte <= 0x7e));
  if (!valid) {
    throw new Error(`cookie credential ${JSON.stringify(name)} cannot be carried as an RFC 6265 cookie-value`);
  }
}

/** Percent-encodes an API-key query name or value under RFC 3986. */
export function encodeOpenAPICredentialQuery(value: string): string {
  return encodeURIComponent(value).replace(/[!'()*]/gu, (character) =>
    `%${character.charCodeAt(0).toString(16).toUpperCase()}`);
}

/** Enforces static and populated wire ownership for credential destinations. */
export function openAPICredentialCollision(
  placements: readonly OpenAPICredentialPlacement[],
  params: readonly OpenAPIParameter[],
  populated: { header: ReadonlySet<string>; query: ReadonlySet<string>; cookie: ReadonlySet<string> },
): string {
  const declared = {
    header: new Set<string>(),
    query: new Set<string>(),
    cookie: new Set<string>(),
  };
  for (const parameter of params) {
    if (!parameter.name) continue;
    if (parameter.in === "header") declared.header.add(parameter.name.toLowerCase());
    else if (parameter.in === "query") declared.query.add(parameter.name);
    else if (parameter.in === "cookie") declared.cookie.add(parameter.name);
  }
  const processorOwned = new Set(["host", "content-length", "content-type", "accept"]);
  const hasRawCookieOwner = populated.header.has("cookie") || placements.some(
    (placement) => placement.channel === "header" && placement.name.toLowerCase() === "cookie",
  );
  const hasStructuredCookieOwner = populated.cookie.size > 0
    || placements.some((placement) => placement.channel === "cookie");
  if (hasRawCookieOwner && hasStructuredCookieOwner) {
    return "raw Cookie header source collides with structured cookie assembly (OAPI-P-10)";
  }
  if (placements.some((placement) =>
    placement.channel === "header" && placement.name.toLowerCase() === "cookie")
    && declared.cookie.size > 0) {
    return "raw Cookie credential collides with a declared structured cookie parameter (OAPI-P-10)";
  }
  const seen = new Set<string>();
  for (const placement of placements) {
    const name = placement.channel === "header" ? placement.name.toLowerCase() : placement.name;
    if (placement.channel === "header" && processorOwned.has(name)) {
      return `credential "${placement.name}" collides with processor-owned request field ${placement.name} (OAPI-P-10)`;
    }
    if (placement.channel === "cookie" && declared.header.has("cookie")) {
      return `cookie credential "${placement.name}" collides with an effective raw Cookie header parameter (OAPI-P-10)`;
    }
    if (declared[placement.channel].has(name) || populated[placement.channel].has(name)) {
      return `credential "${placement.name}" collides with an effective ${placement.channel} parameter of the same name (OAPI-P-10: refused before dispatch, never a silent overwrite in either direction)`;
    }
    const key = `${placement.channel}\0${name}`;
    if (seen.has(key)) {
      return `two credentials collide at ${placement.channel} "${placement.name}" (OAPI-P-10)`;
    }
    seen.add(key);
  }
  return "";
}

function validBasicCredentialText(value: string): boolean {
  return [...value].every((character) => {
    const code = character.codePointAt(0)!;
    return code >= 0x20 && code <= 0x7e;
  });
}
