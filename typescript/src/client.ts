import { VALID_METHODS } from "./constants.js";
import { OPENAPI_PROFILE_FULL } from "./profile.js";
import {
  OpenAPIEngine,
  OpenAPIExecutionError,
  type OpenAPIExecutionEvent,
} from "./engine.js";
import type {
  OpenAPIDocument,
  OpenAPIOperation,
  OpenAPIPathItem,
  OpenAPISecurityScheme,
} from "./types.js";
import { effectiveParameters } from "./params.js";
import { planAbstractInputRoutes } from "./input-routes-v2.js";
import {
  configureRequestMedia,
  governingResponse,
  governingResponseMedia,
  governingResponseMediaMatch,
  planRequestBodies,
  responseUsesRawBoundary,
  type BodyPlan,
} from "./media.js";
import { decodeBytesByContentType } from "./invoke.js";
import { openAPIFailureEvidence } from "./failure.js";
import { isSSEContentType } from "./sse.js";
import { errorMessage, loadOpenAPIDocument, parseRef } from "./util.js";

export type HTTPMethod = "get" | "put" | "post" | "delete" | "options" | "head" | "patch" | "trace";

export type OpenAPISource =
  | string
  | URL
  | OpenAPIDocument
  | { location?: string; content?: unknown };

export type OpenAPIOperationSelector =
  | string
  | { operationId: string }
  | { path: string; method: HTTPMethod }
  | { ref: string };

export type OpenAPIAuthValue =
  | string
  | { username: string; password: string }
  | OpenAPISecurityHandler;

export interface OpenAPISecurityHandlerContext {
  operation: OpenAPIOperationInfo;
  schemeName: string;
  scheme: OpenAPISecurityScheme;
  request: Request;
}

/** Applies an authored security scheme that the built-in adapters do not own. */
export type OpenAPISecurityHandler = (
  context: OpenAPISecurityHandlerContext,
) => Request | void | Promise<Request | void>;

export type OpenAPIServerSelection =
  | string
  | number
  | {
      index?: number;
      url?: string;
      baseUrl?: string;
      variables?: Record<string, string>;
    };

export interface OpenAPIParameterInput {
  path?: Record<string, unknown>;
  query?: Record<string, unknown>;
  header?: Record<string, unknown>;
  cookie?: Record<string, unknown>;
}

export interface OpenAPICallInput {
  parameters?: OpenAPIParameterInput;
  /** The exact application body. `false`, `0`, an empty string, and `null` are all present bodies. */
  body?: unknown;
  /** Concrete request media type. Required when a governing declaration is a media range. */
  mediaType?: string;
}

export interface OpenAPICallOptions {
  auth?: Record<string, OpenAPIAuthValue>;
  server?: OpenAPIServerSelection;
  headers?: HeadersInit;
  signal?: AbortSignal;
  maxResponseBytes?: number;
  fetch?: typeof globalThis.fetch;
  /** Defaults to `manual`, keeping redirect responses observable as the bound operation outcome. */
  redirect?: RequestRedirect;
}

export interface OpenAPIClientMiddlewareContext {
  operation: OpenAPIOperationInfo;
  request: Request;
}

export interface OpenAPIClientMiddleware {
  onRequest?(context: OpenAPIClientMiddlewareContext): Request | Response | void | Promise<Request | Response | void>;
  onResponse?(context: OpenAPIClientMiddlewareContext & { response: Response }): Response | void | Promise<Response | void>;
  onError?(context: OpenAPIClientMiddlewareContext & { error: unknown }): Response | Error | void | Promise<Response | Error | void>;
}

export interface OpenAPIClientOptions {
  auth?: Record<string, OpenAPIAuthValue>;
  server?: OpenAPIServerSelection;
  headers?: HeadersInit;
  /** Cancels document loading and, unless overridden per call, later invocations. */
  signal?: AbortSignal;
  fetch?: typeof globalThis.fetch;
  /** Defaults to `manual`; set `follow` to opt into ordinary user-agent redirect behavior. */
  redirect?: RequestRedirect;
  middleware?: OpenAPIClientMiddleware[];
  maxResponseBytes?: number;
}

export interface OpenAPIOperationInfo {
  ref: string;
  path: string;
  method: HTTPMethod;
  operationId?: string;
  summary?: string;
  tags: string[];
}

export interface OpenAPIDeclarationMatch {
  declared: boolean;
  responseKey?: string;
  mediaType?: string;
}

interface OpenAPIResultBase {
  response: Response;
  openapi: OpenAPIDeclarationMatch;
}

export interface OpenAPISuccessResult<T = unknown> extends OpenAPIResultBase {
  ok: true;
  data: T | undefined;
}

export interface OpenAPIFailureResult<E = unknown> extends OpenAPIResultBase {
  ok: false;
  error: E | undefined;
}

export type OpenAPIResult<T = unknown, E = unknown> =
  | OpenAPISuccessResult<T>
  | OpenAPIFailureResult<E>;

export interface OpenAPIStreamSuccessResult<T = unknown> extends OpenAPIResultBase {
  ok: true;
  /** Values decoded from the response in arrival order, with backpressure. */
  events: AsyncIterable<OpenAPIStreamEvent<T>>;
  /** Resolves on clean completion and rejects with a typed client error otherwise. */
  closed: Promise<void>;
  /** Cancels response consumption and the underlying request. */
  cancel(): Promise<void>;
}

export interface OpenAPIStreamEvent<T = unknown> {
  data: T;
  /** Server-Sent Events framing retained when the response uses SSE. */
  sse?: {
    event?: string;
    id?: string;
    retry?: number;
  };
}

export type OpenAPIStreamResult<T = unknown, E = unknown> =
  | OpenAPIStreamSuccessResult<T>
  | OpenAPIFailureResult<E>;

export type OpenAPIClientErrorKind =
  | "source"
  | "operation"
  | "input"
  | "configuration"
  | "transport"
  | "protocol"
  | "response"
  | "cancelled"
  | "internal";

export class OpenAPIClientError extends Error {
  readonly kind: OpenAPIClientErrorKind;
  readonly code: string;
  readonly details?: unknown;

  constructor(kind: OpenAPIClientErrorKind, code: string, message: string, options?: { cause?: unknown; details?: unknown }) {
    super(message, options?.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = "OpenAPIClientError";
    this.kind = kind;
    this.code = code;
    if (options?.details !== undefined) this.details = options.details;
  }
}

export interface OpenAPIOperationClient {
  readonly info: OpenAPIOperationInfo;
  call<T = unknown, E = unknown>(input?: OpenAPICallInput, options?: OpenAPICallOptions): Promise<OpenAPIResult<T, E>>;
  stream<T = unknown, E = unknown>(input?: OpenAPICallInput, options?: OpenAPICallOptions): Promise<OpenAPIStreamResult<T, E>>;
}

interface ResolvedOperation {
  info: OpenAPIOperationInfo;
  pathItem: OpenAPIPathItem;
  operation: OpenAPIOperation;
}

interface ObservedExchange {
  request: Request;
  response: Response;
}

/**
 * A document-driven OpenAPI 3.0/3.1 client. The document remains the runtime
 * authority for operation lookup, request construction, security placement,
 * and response interpretation; no generated client or OBI is required.
 */
export class OpenAPIClient {
  readonly document: OpenAPIDocument;
  readonly location?: string;

  private readonly engine = new OpenAPIEngine();
  private readonly options: OpenAPIClientOptions;

  private constructor(document: OpenAPIDocument, location: string | undefined, options: OpenAPIClientOptions) {
    this.document = document;
    this.location = location;
    this.options = options;
  }

  static async load(source: OpenAPISource, options: OpenAPIClientOptions = {}): Promise<OpenAPIClient> {
    const normalized = normalizeSource(source);
    let document: OpenAPIDocument;
    try {
      document = await loadOpenAPIDocument(
        normalized.location,
        normalized.content,
        { signal: options.signal },
        options.fetch,
      );
    } catch (error: unknown) {
      throw new OpenAPIClientError("source", "SOURCE_LOAD_FAILED", errorMessage(error), { cause: error });
    }
    return new OpenAPIClient(document, normalized.location, options);
  }

  operations(): OpenAPIOperationInfo[] {
    const result: OpenAPIOperationInfo[] = [];
    for (const resolved of enumerateOperations(this.document)) result.push(resolved.info);
    return result;
  }

  operation(selector: OpenAPIOperationSelector): OpenAPIOperationClient {
    const resolved = resolveOperation(this.document, selector);
    return {
      info: resolved.info,
      call: <T = unknown, E = unknown>(input: OpenAPICallInput = {}, options: OpenAPICallOptions = {}) =>
        this.callResolved<T, E>(resolved, input, options),
      stream: <T = unknown, E = unknown>(input: OpenAPICallInput = {}, options: OpenAPICallOptions = {}) =>
        this.streamResolved<T, E>(resolved, input, options),
    };
  }

  async call<T = unknown, E = unknown>(
    selector: OpenAPIOperationSelector,
    input: OpenAPICallInput = {},
    options: OpenAPICallOptions = {},
  ): Promise<OpenAPIResult<T, E>> {
    return this.callResolved<T, E>(resolveOperation(this.document, selector), input, options);
  }

  /**
   * Opens an operation as an async value stream. It is also valid for unary
   * operations, which yield zero or one event. A non-2xx HTTP response is
   * returned as the same rich failure value used by {@link call}.
   */
  async stream<T = unknown, E = unknown>(
    selector: OpenAPIOperationSelector,
    input: OpenAPICallInput = {},
    options: OpenAPICallOptions = {},
  ): Promise<OpenAPIStreamResult<T, E>> {
    return this.streamResolved<T, E>(resolveOperation(this.document, selector), input, options);
  }

  private async callResolved<T, E>(
    resolved: ResolvedOperation,
    input: OpenAPICallInput,
    callOptions: OpenAPICallOptions,
  ): Promise<OpenAPIResult<T, E>> {
    const stream = await this.streamResolved<T, E>(resolved, input, callOptions);
    if (!stream.ok) return stream;
    if (isSSEContentType(stream.response.headers.get("content-type"))) {
      await stream.cancel();
      throw new OpenAPIClientError(
        "response",
        "STREAMING_RESPONSE",
        "operation returned text/event-stream; use client.stream()",
      );
    }
    const outputs: T[] = [];
    try {
      for await (const output of stream.events) outputs.push(output.data);
      await stream.closed;
    } catch (error: unknown) {
      throw clientError(error);
    }
    if (outputs.length > 1) {
      throw new OpenAPIClientError(
        "response",
        "STREAMING_RESPONSE",
        "operation produced multiple outputs; use client.stream()",
      );
    }
    return {
      ok: true,
      data: outputs[0],
      response: stream.response,
      openapi: stream.openapi,
    };
  }

  private async streamResolved<T, E>(
    resolved: ResolvedOperation,
    input: OpenAPICallInput,
    callOptions: OpenAPICallOptions,
  ): Promise<OpenAPIStreamResult<T, E>> {
    const native = await nativeInput(
      this.document,
      resolved.pathItem,
      resolved.operation,
      input,
    );
    const headers = mergeHeaders(this.options.headers, callOptions.headers);
    const security = nativeContext(
      this.document,
      this.options,
      callOptions,
      native.mediaType,
      headers,
    );
    const exchange = deferred<ObservedExchange>();
    const baseFetch = callOptions.fetch ?? this.options.fetch ?? globalThis.fetch;
    const doFetch = observedFetch(
      baseFetch,
      [...(this.options.middleware ?? [])],
      resolved.info,
      exchange,
    );
    const prepared = await this.engine.prepare({
      source: {
        location: this.location,
        content: this.document,
      },
      ref: resolved.info.ref,
      profile: OPENAPI_PROFILE_FULL,
      context: security.context,
      maxDeliveryUnitBytes: callOptions.maxResponseBytes ?? this.options.maxResponseBytes,
      signal: callOptions.signal ?? this.options.signal,
      fetch: doFetch,
      redirect: callOptions.redirect ?? this.options.redirect,
      securityHandlers: artifactSecurityHandlers(security.handlers, resolved.info),
    });
    const execution = await prepared.start<unknown, unknown>();

    try {
      if (native.supplied) await execution.send(native.value);
      await execution.finishInput();
      const observed = await Promise.race([
        exchange.promise,
        execution.completed.then<ObservedExchange>(() => {
          throw new OpenAPIClientError("response", "NO_HTTP_RESPONSE", "invocation completed without an HTTP response");
        }),
      ]);
      const declaration = declarationMatch(resolved.operation, observed.response);
      if (observed.response.status < 200 || observed.response.status >= 300) {
        let terminal: unknown;
        try {
          await execution.completed;
        } catch (error: unknown) {
          terminal = error;
        }
        const evidence = openAPIFailureEvidence(terminal);
        const bytes = evidence?.httpResponse.body
          ?? new Uint8Array(await observed.response.clone().arrayBuffer());
        return {
          ok: false,
          error: decodeFailure<E>(bytes, observed.response.headers.get("content-type")),
          response: observed.response,
          openapi: evidence ? {
            declared: evidence.openapi.declared,
            ...(evidence.openapi.responseKey ? { responseKey: evidence.openapi.responseKey } : {}),
            ...(evidence.openapi.governingMedia ? { mediaType: evidence.openapi.governingMedia } : {}),
          } : declaration,
        };
      }
      const closed = execution.completed.catch((error: unknown) => {
        throw clientError(error);
      });
      // Consumers normally observe termination by iterating events. This
      // guard prevents an ignored `closed` view from becoming an unhandled
      // rejection while preserving its rejection for consumers that await it.
      void closed.catch(() => undefined);
      return {
        ok: true,
        events: mapOutputs<T>(execution.events, nativeResponseUsesRawBoundary(this.document, resolved.operation, observed.response)),
        closed,
        cancel: () => execution.cancel(),
        response: observed.response,
        openapi: declaration,
      };
    } catch (error: unknown) {
      const evidence = openAPIFailureEvidence(error);
      if (evidence) {
        const observed = await exchange.promise.catch(() => null);
        const response = observed?.response ?? responseFromEvidence(evidence.httpResponse);
        return {
          ok: false,
          error: decodeFailure<E>(evidence.httpResponse.body, response.headers.get("content-type")),
          response,
          openapi: {
            declared: evidence.openapi.declared,
            ...(evidence.openapi.responseKey ? { responseKey: evidence.openapi.responseKey } : {}),
            ...(evidence.openapi.governingMedia ? { mediaType: evidence.openapi.governingMedia } : {}),
          },
        };
      }
      throw clientError(error);
    }
  }
}

function normalizeSource(source: OpenAPISource): { location?: string; content?: unknown } {
  if (typeof source === "string") return { location: source };
  if (source instanceof URL) return { location: source.toString() };
  if ("location" in source || "content" in source) {
    return source as { location?: string; content?: unknown };
  }
  return { content: source };
}

function enumerateOperations(document: OpenAPIDocument): ResolvedOperation[] {
  const result: ResolvedOperation[] = [];
  for (const path of Object.keys(document.paths ?? {}).sort()) {
    const pathItem = document.paths?.[path];
    if (!pathItem) continue;
    for (const method of VALID_METHODS) {
      const operation = pathItem[method] as OpenAPIOperation | undefined;
      if (!operation) continue;
      result.push({
        pathItem,
        operation,
        info: {
          ref: `#/paths/${escapePointer(path)}/${method}`,
          path,
          method: method as HTTPMethod,
          ...(operation.operationId ? { operationId: operation.operationId } : {}),
          ...(operation.summary ? { summary: operation.summary } : {}),
          tags: [...(operation.tags ?? [])],
        },
      });
    }
  }
  return result;
}

function resolveOperation(document: OpenAPIDocument, selector: OpenAPIOperationSelector): ResolvedOperation {
  const operations = enumerateOperations(document);
  if (typeof selector === "string" || "operationId" in selector) {
    const operationId = typeof selector === "string" ? selector : selector.operationId;
    const matches = operations.filter((candidate) => candidate.info.operationId === operationId);
    if (matches.length === 1) return matches[0]!;
    if (matches.length > 1) {
      throw new OpenAPIClientError("operation", "DUPLICATE_OPERATION_ID", `operationId ${JSON.stringify(operationId)} is not unique`);
    }
    throw new OpenAPIClientError("operation", "OPERATION_NOT_FOUND", `operationId ${JSON.stringify(operationId)} was not found`);
  }
  if ("ref" in selector) {
    let path: string;
    let method: string;
    try {
      ({ path, method } = parseRef(selector.ref));
    } catch (error: unknown) {
      throw new OpenAPIClientError("operation", "INVALID_OPERATION_REF", errorMessage(error), { cause: error });
    }
    const match = operations.find((candidate) => candidate.info.path === path && candidate.info.method === method);
    if (match) return match;
    throw new OpenAPIClientError("operation", "OPERATION_NOT_FOUND", `operation ref ${JSON.stringify(selector.ref)} was not found`);
  }
  const method = selector.method.toLowerCase() as HTTPMethod;
  const match = operations.find((candidate) => candidate.info.path === selector.path && candidate.info.method === method);
  if (match) return match;
  throw new OpenAPIClientError(
    "operation",
    "OPERATION_NOT_FOUND",
    `${method.toUpperCase()} ${selector.path} was not found`,
  );
}

async function nativeInput(
  document: OpenAPIDocument,
  pathItem: OpenAPIPathItem,
  operation: OpenAPIOperation,
  input: OpenAPICallInput,
): Promise<{ supplied: boolean; value: unknown; mediaType?: string }> {
  const parameters = effectiveParameters(pathItem, operation);
  const plans = planRequestBodies(operation, {
    profile: OPENAPI_PROFILE_FULL,
    openapiVersion: document.openapi,
    inventoryUnsupported: true,
  });
  const selectedPlans = selectBodyPlans(plans, input.mediaType, document.openapi);
  const routes = planAbstractInputRoutes(parameters, selectedPlans, OPENAPI_PROFILE_FULL);
  const value: Record<string, unknown> = {};
  let supplied = false;

  const providedParameters = input.parameters ?? {};
  for (const location of ["path", "query", "header", "cookie"] as const) {
    const provided = providedParameters[location] ?? {};
    for (const name of Object.keys(provided)) {
      if (!parameters.some((parameter) => parameter.in === location && parameter.name === name)) {
        throw new OpenAPIClientError(
          "input",
          "UNKNOWN_PARAMETER",
          `operation does not declare ${location} parameter ${JSON.stringify(name)}`,
        );
      }
      const field = routes.parameterField(location, name);
      value[field] = provided[name];
      supplied = true;
    }
  }

  if (Object.prototype.hasOwnProperty.call(input, "body") && input.body !== undefined) {
    if (selectedPlans.length === 0) {
      throw new OpenAPIClientError("input", "BODY_NOT_DECLARED", "operation does not declare a supported request body");
    }
    const plan = selectedPlans[0]!;
    const body = await nativeRequestBody(plan, input.body);
    if (plan.synthetic || plan.wholeObject) {
      value[routes.wholeBodyField] = body;
    } else {
      if (body === null || typeof body !== "object" || Array.isArray(body)) {
        throw new OpenAPIClientError("input", "OBJECT_BODY_REQUIRED", `${plan.mediaType} requires an object body`);
      }
      for (const [name, member] of Object.entries(body as Record<string, unknown>)) {
        value[routes.bodyField(name)] = member;
      }
    }
    supplied = true;
  }

  const bodyDescriptor: Record<string, unknown> = {};
  if (Object.keys(routes.bodyFields).length > 0) bodyDescriptor.properties = routes.bodyFields;
  if (routes.wholeBodyField) bodyDescriptor.whole = routes.wholeBodyField;
  if (Object.prototype.hasOwnProperty.call(input, "body") && input.body !== undefined) {
    bodyDescriptor.present = true;
  }
  return {
    supplied,
    value: [{
      $openbindings: OPENAPI_PROFILE_FULL.inputRouteMarker,
      value,
      parameters: routes.parameters,
      body: bodyDescriptor,
    }],
    ...(selectedPlans[0]?.mediaType ? { mediaType: input.mediaType ?? selectedPlans[0].mediaType } : {}),
  };
}

async function nativeRequestBody(plan: BodyPlan, body: unknown): Promise<unknown> {
  if (!plan.rawBoundary) return body;
  let bytes: Uint8Array;
  if (typeof body === "string") {
    bytes = new TextEncoder().encode(body);
  } else if (body instanceof Uint8Array) {
    bytes = body;
  } else if (body instanceof ArrayBuffer) {
    bytes = new Uint8Array(body);
  } else if (ArrayBuffer.isView(body)) {
    bytes = new Uint8Array(body.buffer, body.byteOffset, body.byteLength);
  } else if (body instanceof Blob) {
    bytes = new Uint8Array(await body.arrayBuffer());
  } else {
    throw new OpenAPIClientError(
      "input",
      "RAW_BODY_BYTES_REQUIRED",
      `${plan.mediaType} requires a string, Blob, ArrayBuffer, or typed-array body`,
    );
  }
  return bytesToBase64(bytes);
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunkSize = 0x8000;
  for (let offset = 0; offset < bytes.byteLength; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunkSize));
  }
  return btoa(binary);
}

function selectBodyPlans(plans: BodyPlan[], configured: string | undefined, openapiVersion: string | undefined): BodyPlan[] {
  if (configured) {
    const selected = configureRequestMedia(plans, configured, {
      profile: OPENAPI_PROFILE_FULL,
      openapiVersion,
      inventoryUnsupported: true,
    });
    if (selected.length === 0) {
      throw new OpenAPIClientError("input", "REQUEST_MEDIA_NOT_DECLARED", `request media ${JSON.stringify(configured)} is not a supported declaration`);
    }
    return selected;
  }
  return plans.filter((plan) => !plan.range && !plan.unsupported).slice(0, 1);
}

function nativeContext(
  document: OpenAPIDocument,
  defaults: OpenAPIClientOptions,
  call: OpenAPICallOptions,
  mediaType: string | undefined,
  headers: Headers,
): { context: Record<string, unknown>; handlers: Record<string, NativeSecurityHandler> } {
  const context: Record<string, unknown> = {};
  const handlers: Record<string, NativeSecurityHandler> = {};
  const configuration: Record<string, unknown> = {};
  const server = call.server ?? defaults.server;
  if (server !== undefined) configuration.server = typeof server === "number" ? { index: server } : server;
  if (mediaType) configuration.requestMedia = mediaType;
  if (Object.keys(configuration).length > 0) context.configuration = configuration;
  if ([...headers].length > 0) context.metadata = { headers: Object.fromEntries(headers) };

  const auth = { ...(defaults.auth ?? {}), ...(call.auth ?? {}) };
  const schemes = securitySchemes(document);
  const apiKeys: Record<string, string> = {};
  for (const [name, credential] of Object.entries(auth)) {
    const scheme = schemes[name];
    if (!scheme) {
      throw new OpenAPIClientError("configuration", "UNKNOWN_SECURITY_SCHEME", `security scheme ${JSON.stringify(name)} was not found`);
    }
    if (typeof credential === "function") {
      handlers[name] = { scheme, handler: credential };
      continue;
    }
    if (scheme.type === "apiKey") {
      if (typeof credential !== "string") throw credentialShape(name, "a string API key");
      apiKeys[name] = credential;
    } else if (scheme.type === "http" && (scheme.scheme ?? "").toLowerCase() === "basic") {
      if (typeof credential === "string") throw credentialShape(name, "{ username, password }");
      context.basic = credential;
    } else if (scheme.type === "http" && (scheme.scheme ?? "").toLowerCase() === "bearer") {
      if (typeof credential !== "string") throw credentialShape(name, "a token string");
      context.bearerToken = credential;
    } else if (scheme.type === "oauth2" || scheme.type === "openIdConnect") {
      if (typeof credential !== "string") throw credentialShape(name, "an access-token string");
      context.accessToken = credential;
    } else {
      throw new OpenAPIClientError(
        "configuration",
        "UNSUPPORTED_SECURITY_SCHEME",
        `security scheme ${JSON.stringify(name)} uses ${unsupportedSchemeLabel(scheme)}, which the built-in credential adapter cannot apply; configure the HTTP client and middleware explicitly`,
      );
    }
  }
  if (Object.keys(apiKeys).length > 0) context.apiKeys = apiKeys;
  return { context, handlers };
}

interface NativeSecurityHandler {
  scheme: OpenAPISecurityScheme;
  handler: OpenAPISecurityHandler;
}

function artifactSecurityHandlers(
  handlers: Record<string, NativeSecurityHandler>,
  operation: OpenAPIOperationInfo,
): Record<string, (request: Request, context: { schemeName: string; scheme: unknown }) => Promise<Request | void>> {
  return Object.fromEntries(Object.entries(handlers).map(([name, security]) => [
    name,
    async (request: Request, context: { schemeName: string; scheme: unknown }) => {
      try {
        return await security.handler({
          operation,
          schemeName: context.schemeName,
          scheme: context.scheme as OpenAPISecurityScheme,
          request,
        });
      } catch (error: unknown) {
        throw new OpenAPIClientError(
          "configuration",
          "SECURITY_HANDLER_FAILED",
          `security handler ${JSON.stringify(name)} failed: ${errorMessage(error)}`,
          { cause: error },
        );
      }
    },
  ]));
}

function unsupportedSchemeLabel(scheme: OpenAPISecurityScheme): string {
  if (scheme.type === "http") return `HTTP scheme ${JSON.stringify(scheme.scheme ?? "")}`;
  return `type ${JSON.stringify(scheme.type)}`;
}

function credentialShape(name: string, expected: string): OpenAPIClientError {
  return new OpenAPIClientError("configuration", "INVALID_CREDENTIAL", `security scheme ${JSON.stringify(name)} requires ${expected}`);
}

function securitySchemes(document: OpenAPIDocument): Record<string, OpenAPISecurityScheme> {
  const components = document.components;
  if (!components || typeof components !== "object" || Array.isArray(components)) return {};
  const raw = (components as Record<string, unknown>).securitySchemes;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const result: Record<string, OpenAPISecurityScheme> = {};
  for (const [name, value] of Object.entries(raw as Record<string, unknown>)) {
    if (value && typeof value === "object" && !Array.isArray(value) && typeof (value as OpenAPISecurityScheme).type === "string") {
      result[name] = value as OpenAPISecurityScheme;
    }
  }
  return result;
}

function observedFetch(
  baseFetch: typeof globalThis.fetch,
  middleware: OpenAPIClientMiddleware[],
  operation: OpenAPIOperationInfo,
  exchange: Deferred<ObservedExchange>,
): typeof globalThis.fetch {
  return async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    let request = new Request(input, init);
    try {
      for (const item of middleware) {
        const changed = await item.onRequest?.({ operation, request });
        if (changed instanceof Response) {
          const response = await applyResponseMiddleware(middleware, operation, request, changed);
          exchange.resolve({ request, response: response.clone() });
          return response;
        }
        if (changed instanceof Request) request = changed;
      }
      let response = await baseFetch(request);
      response = await applyResponseMiddleware(middleware, operation, request, response);
      exchange.resolve({ request, response: response.clone() });
      return response;
    } catch (error: unknown) {
      for (const item of middleware) {
        const handled = await item.onError?.({ operation, request, error });
        if (handled instanceof Response) {
          const response = await applyResponseMiddleware(middleware, operation, request, handled);
          exchange.resolve({ request, response: response.clone() });
          return response;
        }
        if (handled instanceof Error) throw handled;
      }
      exchange.reject(error instanceof OpenAPIClientError
        ? error
        : new OpenAPIClientError("transport", "FETCH_FAILED", errorMessage(error), { cause: error }));
      throw error;
    }
  };
}

async function applyResponseMiddleware(
  middleware: OpenAPIClientMiddleware[],
  operation: OpenAPIOperationInfo,
  request: Request,
  initial: Response,
): Promise<Response> {
  let response = initial;
  for (const item of [...middleware].reverse()) {
    response = await item.onResponse?.({ operation, request, response }) ?? response;
  }
  return response;
}

async function* mapOutputs<T>(
  events: AsyncIterable<OpenAPIExecutionEvent<unknown>>,
  rawBoundary = false,
): AsyncIterable<OpenAPIStreamEvent<T>> {
  try {
    for await (const item of events) {
      const native = item.metadata;
      const event = native["x-sse-event"]?.[0];
      const id = native["x-sse-id"]?.[0];
      const retryValue = native["x-sse-retry"]?.[0];
      const retry = retryValue !== undefined ? Number.parseInt(retryValue, 10) : undefined;
      const sse = event !== undefined || id !== undefined || retry !== undefined
        ? {
            ...(event !== undefined ? { event } : {}),
            ...(id !== undefined ? { id } : {}),
            ...(retry !== undefined && Number.isFinite(retry) ? { retry } : {}),
          }
        : undefined;
      const value = rawBoundary && typeof item.value === "string"
        ? Uint8Array.from(atob(item.value), (character) => character.charCodeAt(0))
        : item.value;
      yield { data: value as T, ...(sse ? { sse } : {}) };
    }
  } catch (error: unknown) {
    throw clientError(error);
  }
}

function nativeResponseUsesRawBoundary(
  document: OpenAPIDocument,
  operation: OpenAPIOperation,
  response: Response,
): boolean {
  const contentType = response.headers.get("content-type");
  if (!contentType) return false;
  const declaration = governingResponse(operation, response.status);
  if (!declaration) return false;
  try {
    const match = governingResponseMediaMatch(declaration.response, contentType, true, true);
    return match !== null && responseUsesRawBoundary(
      match.media,
      contentType,
      document.openapi ?? "3.0",
      OPENAPI_PROFILE_FULL,
      !("specificity" in match.declared),
    );
  } catch {
    return false;
  }
}

function declarationMatch(operation: OpenAPIOperation, response: Response): OpenAPIDeclarationMatch {
  const declaration = governingResponse(operation, response.status);
  if (!declaration) return { declared: false };
  let mediaType: string | undefined;
  try {
    mediaType = governingResponseMedia(declaration.response, response.headers.get("content-type"), true, true) ?? undefined;
  } catch {
    // The engine is the authority for rejecting an incompatible response.
  }
  return {
    declared: true,
    responseKey: declaration.key,
    ...(mediaType ? { mediaType } : {}),
  };
}

function decodeFailure<E>(body: Uint8Array<ArrayBuffer> | undefined, contentType: string | null): E | undefined {
  if (!body || body.byteLength === 0) return undefined;
  try {
    const decoder = decodeBytesByContentType(contentType, body, true);
    return decoder({ operation: "", invokedAs: "", bindingKey: "", bindingSpec: "openapi", ref: "", target: "" }, { status: null, body: "", meta: {} }) as E;
  } catch {
    return body as E;
  }
}

function responseFromEvidence(evidence: {
  status: number;
  statusText?: string;
  url?: string;
  headers: Record<string, string[]>;
  body?: Uint8Array<ArrayBuffer>;
}): Response {
  const headers = new Headers();
  for (const [name, values] of Object.entries(evidence.headers)) {
    for (const value of values) headers.append(name, value);
  }
  return new Response(evidence.body, { status: evidence.status, statusText: evidence.statusText, headers });
}

function clientError(error: unknown): OpenAPIClientError {
  if (error instanceof OpenAPIClientError) return error;
  if (error instanceof OpenAPIExecutionError) {
    const kind: OpenAPIClientErrorKind =
      error.code === "ERR_SOURCE_LOAD_FAILED" ? "source"
      : error.code === "ERR_INVALID_REF" || error.code === "ERR_REF_NOT_FOUND" ? "operation"
      : error.code === "ERR_MISSING_INPUT" || error.code === "ERR_VALIDATION_FAILED" ? "input"
      : error.code === "ERR_SOURCE_CONFIG_ERROR" || error.code === "CONTEXT_REQUIRED" ? "configuration"
      : error.code === "ERR_CONNECT_FAILED" ? "transport"
      : error.code === "ERR_PROTOCOL" ? "protocol"
      : error.code === "ERR_RESPONSE_ERROR" || error.code === "ERR_STREAM_ERROR" ? "response"
      : error.code === "ERR_CANCELLED" || error.code === "ERR_TIMEOUT" ? "cancelled"
      : "internal";
    return new OpenAPIClientError(kind, error.code, error.message, {
      cause: error,
      details: error.details,
    });
  }
  return new OpenAPIClientError("internal", "INTERNAL_ERROR", errorMessage(error), { cause: error });
}

function mergeHeaders(left: HeadersInit | undefined, right: HeadersInit | undefined): Headers {
  const headers = new Headers(left);
  new Headers(right).forEach((value, name) => headers.set(name, value));
  return headers;
}

function escapePointer(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

interface Deferred<T> {
  promise: Promise<T>;
  resolve(value: T): void;
  reject(error: unknown): void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}
