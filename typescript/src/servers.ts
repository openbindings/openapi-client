import { contextConfiguration, contextMetadata } from "./internal/index.js";
import { denotesTargetBase, hasURIScheme } from "./target-base.js";
import type { OpenAPIDocument, OpenAPIOperation, OpenAPIPathItem } from "./types.js";

/** A missing value needed to resolve an OpenAPI server alternative. */
export class ConfigRequired extends Error {
  constructor(
    readonly point: string,
    readonly path: string,
    message: string,
    readonly schema?: Record<string, unknown>,
    readonly durable?: boolean,
  ) {
    super(message);
    this.name = "ConfigRequired";
  }
}

export interface OpenAPIServerVariable {
  default?: string;
  enum?: string[];
  [key: string]: unknown;
}

export interface OpenAPIServerEntry {
  url: string;
  variables?: Record<string, OpenAPIServerVariable>;
  [key: string]: unknown;
}

export interface OpenAPIServerResolution {
  url: string;
  server?: OpenAPIServerEntry;
}

export type OpenAPIServerLocationResolver = (
  server: OpenAPIServerEntry,
) => string | undefined;

/** Resolves the effective Server alternative without normalizing its spelling. */
export function resolveServer(
  doc: OpenAPIDocument,
  pathItem: OpenAPIPathItem | null,
  operation: OpenAPIOperation | null,
  context: Record<string, unknown> | undefined,
  sourceLocation: string | undefined,
): string {
  const version = typeof doc.openapi === "string" ? doc.openapi : "";
  const servers = eligibleServers(effectiveServers(doc, pathItem, operation), version, sourceLocation);
  const configuration = contextConfiguration(context);
  if (configuration.server != null) {
    const selected = resolveOpenAPIServerSelection(configuration.server, servers, version);
    return joinableServerBase(absolutizeServerURL(selected.url, sourceLocation));
  }

  const metadataBase = contextMetadata(context).baseURL;
  if (typeof metadataBase === "string" && metadataBase !== "") {
    return joinableServerBase(absolutizeServerURL(metadataBase, sourceLocation));
  }

  if (servers.length !== 1) {
    throw new ConfigRequired(
      "server",
      "/url",
      `the effective server list has ${servers.length} alternatives; configuration.server must select one`,
      { enum: servers.map((server) => server.url) },
      true,
    );
  }
  return joinableServerBase(absolutizeServerURL(
    substituteServerVariables(servers[0], undefined, version),
    sourceLocation,
  ));
}

/** Returns the Operation, Path Item, root, or implied Server list. */
export function effectiveServers(
  doc: OpenAPIDocument,
  pathItem: OpenAPIPathItem | null,
  operation: OpenAPIOperation | null,
): [OpenAPIServerEntry, ...OpenAPIServerEntry[]] {
  const operationServers = asServerList(operation?.servers);
  if (operationServers.length > 0) return operationServers as [OpenAPIServerEntry, ...OpenAPIServerEntry[]];
  const pathServers = asServerList(pathItem?.servers);
  if (pathServers.length > 0) return pathServers as [OpenAPIServerEntry, ...OpenAPIServerEntry[]];
  const rootServers = asServerList(doc.servers);
  if (rootServers.length > 0) return rootServers as [OpenAPIServerEntry, ...OpenAPIServerEntry[]];
  return [{ url: "/" }];
}

function asServerList(raw: unknown): OpenAPIServerEntry[] {
  if (!Array.isArray(raw)) return [];
  return raw.filter((entry): entry is OpenAPIServerEntry =>
    entry !== null && typeof entry === "object" && !Array.isArray(entry)
      && typeof (entry as Record<string, unknown>).url === "string");
}

/**
 * Confines declaration defects to their Server alternatives. Missing runtime
 * values and a missing relative base remain configurable; malformed template,
 * enum, query, and fragment alternatives do not.
 */
export function eligibleServers(
  servers: readonly OpenAPIServerEntry[],
  version: string,
  sourceLocation: string | undefined,
  locationOf?: OpenAPIServerLocationResolver,
): [OpenAPIServerEntry, ...OpenAPIServerEntry[]] {
  const eligible: OpenAPIServerEntry[] = [];
  let firstError: unknown;
  for (const server of servers) {
    try {
      const expanded = substituteServerVariables(server, undefined, version);
      absolutizeServerURL(expanded, locationOf?.(server) ?? sourceLocation);
      eligible.push(server);
    } catch (error: unknown) {
      if (error instanceof ConfigRequired) eligible.push(server);
      else firstError ??= error;
    }
  }
  if (eligible.length > 0) return eligible as [OpenAPIServerEntry, ...OpenAPIServerEntry[]];
  throw firstError instanceof Error
    ? firstError
    : new Error("the effective server list has no usable alternative");
}

export function resolveOpenAPIServerSelection(
  raw: unknown,
  servers: [OpenAPIServerEntry, ...OpenAPIServerEntry[]],
  version: string,
): OpenAPIServerResolution {
  if (typeof raw === "string") {
    const selected = serverByURL(servers, raw);
    if (selected) return { url: substituteServerVariables(selected, undefined, version), server: selected };
    if (denotesTargetBase(raw)) return { url: raw };
    throw new Error(
      `configuration.server ${JSON.stringify(raw)} matches no declared server entry and is not an absolute base URL`,
    );
  }
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) {
    throw new Error(`configuration.server must be a string or an object, got ${typeof raw}`);
  }
  const value = raw as Record<string, unknown>;
  if (typeof value.baseUrl === "string" && value.baseUrl !== "") {
    if (!denotesTargetBase(value.baseUrl)) {
      throw new Error(`configuration.server.baseUrl ${JSON.stringify(value.baseUrl)} is not an absolute URL`);
    }
    return { url: value.baseUrl };
  }

  let selected = servers.length === 1 ? servers[0] : undefined;
  if (typeof value.url === "string" && value.url !== "") {
    selected = serverByURL(servers, value.url) ?? undefined;
    if (!selected) {
      throw new Error(`configuration.server.url ${JSON.stringify(value.url)} matches no declared server entry`);
    }
  } else if (Object.hasOwn(value, "index")) {
    const index = configIndex(value.index);
    selected = index === null || index < 0 ? undefined : servers[index];
    if (!selected) {
      throw new Error(
        `configuration.server.index ${JSON.stringify(value.index)} is not a valid index into the effective server list (${servers.length} entries)`,
      );
    }
  }
  if (!selected) {
    throw new Error(
      `the effective server list has ${servers.length} alternatives; configuration.server.url or configuration.server.index must select one`,
    );
  }

  let supplied: Record<string, string> | undefined;
  if (value.variables !== undefined) {
    if (value.variables === null || typeof value.variables !== "object" || Array.isArray(value.variables)) {
      throw new Error("configuration.server.variables must be an object");
    }
    supplied = {};
    for (const [name, member] of Object.entries(value.variables as Record<string, unknown>)) {
      if (typeof member !== "string") {
        throw new Error(`configuration.server.variables[${JSON.stringify(name)}] must be a string, got ${typeof member}`);
      }
      supplied[name] = member;
    }
  }
  return { url: substituteServerVariables(selected, supplied, version), server: selected };
}

function serverByURL(servers: readonly OpenAPIServerEntry[], url: string): OpenAPIServerEntry | null {
  return servers.find((server) => server.url === url) ?? null;
}

function configIndex(raw: unknown): number | null {
  return typeof raw === "number" && Number.isInteger(raw) ? raw : null;
}

/** Expands each declared variable exactly once and validates its declaration. */
export function substituteServerVariables(
  server: OpenAPIServerEntry,
  supplied: Record<string, string> | undefined,
  version = "",
): string {
  let result = server.url;
  if (result.includes("?") || result.includes("#")) {
    throw new Error(`server URL ${JSON.stringify(server.url)} contains a query or fragment`);
  }
  const variables = server.variables ?? {};
  for (const name of Object.keys(variables).sort()) {
    const variable = variables[name];
    if (variable === null || typeof variable !== "object" || Array.isArray(variable)) {
      throw new Error(`server ${JSON.stringify(server.url)} variable ${JSON.stringify(name)} is not a Server Variable Object`);
    }
    const expression = `{${name}}`;
    const occurrences = result.split(expression).length - 1;
    if (occurrences !== 1) {
      throw new Error(
        `server ${JSON.stringify(server.url)} variable ${JSON.stringify(name)} must occur exactly once in its URL template (found ${occurrences})`,
      );
    }

    const defaultPresent = Object.hasOwn(variable, "default") && typeof variable.default === "string";
    const enumPresent = Object.hasOwn(variable, "enum");
    if (enumPresent && (!Array.isArray(variable.enum) || variable.enum.some((member) => typeof member !== "string"))) {
      throw new Error(`server ${JSON.stringify(server.url)} variable ${JSON.stringify(name)} has a malformed enum`);
    }
    const values = Array.isArray(variable.enum) ? variable.enum : [];
    if (enumPresent && values.length === 0) {
      throw new Error(`server ${JSON.stringify(server.url)} variable ${JSON.stringify(name)} declares an empty enum`);
    }
    if (version.startsWith("3.0.") && !defaultPresent) {
      throw new Error(`server ${JSON.stringify(server.url)} variable ${JSON.stringify(name)} omits its required default`);
    }
    if (enumPresent && defaultPresent && !values.includes(variable.default!)) {
      throw new Error(
        `server ${JSON.stringify(server.url)} variable ${JSON.stringify(name)} default ${JSON.stringify(variable.default)} is outside its declared enum`,
      );
    }

    let selected = supplied?.[name];
    if (selected === undefined) {
      if (!defaultPresent) {
        throw new ConfigRequired(
          "server",
          `/variables/${escapeJSONPointerToken(name)}`,
          `server ${JSON.stringify(server.url)}: variable ${JSON.stringify(name)} has no supplied value and no declared default`,
          enumPresent ? { enum: values } : undefined,
        );
      }
      selected = variable.default!;
    }
    if (enumPresent && !values.includes(selected)) {
      throw new Error(
        `server ${JSON.stringify(server.url)} variable ${JSON.stringify(name)} value ${JSON.stringify(selected)} is outside its declared enum`,
      );
    }
    result = result.replace(expression, selected);
  }
  for (const name of Object.keys(supplied ?? {})) {
    if (!Object.hasOwn(variables, name)) {
      throw new Error(`server ${JSON.stringify(server.url)} declares no variable ${JSON.stringify(name)}`);
    }
  }
  if (result.includes("{") || result.includes("}")) {
    throw new Error(`server URL ${JSON.stringify(server.url)} contains an unresolved template variable`);
  }
  return result;
}

/** Resolves a relative Server URL while preserving the resulting trailing slash. */
export function absolutizeServerURL(serverURL: string, sourceLocation: string | undefined): string {
  validateServerBaseSpelling(serverURL);
  if (denotesTargetBase(serverURL)) return serverURL;
  if (!hasURIScheme(serverURL) && sourceLocation && denotesTargetBase(sourceLocation)) {
    try {
      const resolved = new URL(serverURL, sourceLocation).toString();
      validateServerBaseSpelling(resolved);
      if (denotesTargetBase(resolved)) return resolved;
    } catch (error: unknown) {
      if (!(error instanceof TypeError)) throw error;
    }
  }
  throw new ConfigRequired(
    "server",
    "/url",
    `server URL ${JSON.stringify(serverURL)} cannot resolve to an absolute URL: supply a base URL at the server configuration point`,
  );
}

/** Replaces a serializer's temporary base without normalizing the wire suffix. */
export function replaceSerializedServerBase(
  current: string,
  resolvedBase: string,
  serializedBase: string,
): string {
  const parsed = new URL(current);
  const basePath = serializedServerBasePath(serializedBase);
  if (!parsed.pathname.startsWith(basePath)) {
    throw new Error("serialized operation path does not retain the serialization server base");
  }
  const suffix = parsed.pathname.slice(basePath.length);
  return `${resolvedBase}${suffix}${parsed.search}`;
}

/** Validates the completed OpenAPI 3.1 target under its RFC 3986 rules. */
export function validateCompletedOpenAPIURL(raw: string): void {
  let completed: URL;
  try {
    completed = new URL(raw);
  } catch (error: unknown) {
    throw new Error(
      `completed OpenAPI 3.1 URL does not parse under RFC 3986: ${errorMessage(error)}`,
      { cause: error },
    );
  }
  for (const [name, component] of [
    ["path", completed.pathname],
    ["query", completed.search.slice(1)],
    ["fragment", completed.hash.slice(1)],
  ] as const) {
    if (!hasValidPercentEscapes(component)) {
      throw new Error(
        `completed OpenAPI 3.1 URL ${name} does not percent-decode under RFC 3986`,
      );
    }
  }
}

/** Clones a Request onto a completed OpenAPI target URL. */
export function requestWithOpenAPIURL(request: Request, url: string): Request {
  const init: RequestInit & { duplex?: "half" } = {
    method: request.method,
    headers: request.headers,
    redirect: request.redirect,
    signal: request.signal,
  };
  if (request.method !== "GET" && request.method !== "HEAD") {
    init.body = request.body;
    init.duplex = "half";
  }
  return new Request(url, init);
}

function serializedServerBasePath(base: string): string {
  const match = /^[A-Za-z][A-Za-z0-9+.-]*:\/\/[^/]*(\/[^?#]*)?$/u.exec(base);
  return match?.[1] ?? "";
}

function hasValidPercentEscapes(component: string): boolean {
  for (let index = 0; index < component.length; index += 1) {
    if (component[index] !== "%") continue;
    if (!/^[0-9A-Fa-f]{2}$/.test(component.slice(index + 1, index + 3))) return false;
    index += 2;
  }
  return true;
}

function validateServerBaseSpelling(value: string): void {
  if (value.includes("?") || value.includes("#")) {
    throw new Error(`server URL ${JSON.stringify(value)} contains a query or fragment`);
  }
  if (/[^\u0021-\u007e]/u.test(value) || /%(?![0-9A-Fa-f]{2})/u.test(value)) {
    throw new Error(`server URL ${JSON.stringify(value)} does not parse under RFC 3986`);
  }
}

function escapeJSONPointerToken(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

function joinableServerBase(value: string): string {
  return value.replace(/\/+$/u, "");
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
