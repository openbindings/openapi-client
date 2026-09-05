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
} from "./types.js";
import { effectiveParameters, type OpenAPIParameterConverter } from "./params.js";
import { planAbstractInputRoutes } from "./input-routes-v2.js";
import {
  configureRequestMedia,
  governingResponse,
  governingResponseMedia,
  governingResponseMediaMatch,
  responseUsesRawBoundary,
  type BodyPlan,
} from "./media.js";
import { planResolvedRequestBodies } from "./resolved-media.js";
import { decodeBytesByContentType } from "./invoke.js";
import { openAPIFailureEvidence } from "./failure.js";
import { isSSEContentType } from "./sse.js";
import { errorMessage, parseJSONOrYAML } from "./util.js";
import {
  fetchCarriesMethod,
  hostCarriesMethod,
  hostMethodRefusal,
  hostTransport as defaultHostTransport,
  type OpenAPIHostTransport,
  type OpenAPIPlannedRequest,
  type OpenAPIRedirectPolicy,
} from "./host-transport.js";
import type { OpenAPICharacterDecoder, OpenAPICharacterEncoder } from "./response-mechanics.js";
import {
  loadOpenAPIArtifact,
  type OpenAPIArtifact,
  type OpenAPIEdition as OpenAPI3Edition,
} from "./openapi32-artifact.js";
import {
  parseOpenAPI32OperationReference,
  type OpenAPIResolvedOperation,
} from "./openapi32-operations.js";
import { loadSwagger20, type Swagger20Client } from "./swagger20-loader.js";
import {
  prepareSwagger20,
  Swagger20ExecutionError,
} from "./swagger20-engine.js";
import type {
  Swagger20Input,
  Swagger20ParameterInfo,
} from "./swagger20-parameters.js";
import type { Swagger20SecurityCredentials } from "./swagger20-security.js";

/** Exact artifact edition selected at load time. */
export type OpenAPIEdition = "2.0" | OpenAPI3Edition;

/** Operation declaration key. OAS 3.2 additional operations make this open. */
export type HTTPMethod = string;

/**
 * One entry artifact. A string or URL is retrieved; a plain object is a parsed
 * document; `location` on a content source supplies the reference base.
 */
export type OpenAPISource =
  | string
  | URL
  | Record<string, unknown>
  | { location?: string; content?: unknown };

export type OpenAPIOperationSelector =
  | string
  | { operationId: string }
  | { path: string; method: HTTPMethod; additional?: boolean }
  | { ref: string };

export type OpenAPIAuthValue =
  | string
  | { username: string; password: string }
  | OpenAPISecurityHandler;

export interface OpenAPISecurityHandlerContext {
  operation: Readonly<OpenAPIOperationInfo>;
  schemeName: string;
  scheme: Readonly<Record<string, unknown>>;
  request: OpenAPIPlannedRequest;
}

/** Applies an authored security scheme that the built-in adapters do not own. */
export type OpenAPISecurityHandler = (
  context: OpenAPISecurityHandlerContext,
) => OpenAPIPlannedRequest | void | Promise<OpenAPIPlannedRequest | void>;

export type OpenAPIServerSelection =
  | { index: number; variables?: Record<string, string> }
  | { variables: Record<string, string>; index?: never }
  | { url: string };

export type OpenAPIEmptyValueForm = "name-only" | "empty";

export type OpenAPIContentCodingResult = Uint8Array | ArrayBuffer | ArrayBufferView;
export type OpenAPIContentCodec = (
  body: Uint8Array,
) => OpenAPIContentCodingResult | Promise<OpenAPIContentCodingResult>;

export interface OpenAPIParameterInput {
  path?: Record<string, unknown>;
  query?: Record<string, unknown>;
  /** OAS 3.2 whole-query-component parameter values. */
  querystring?: Record<string, unknown>;
  header?: Record<string, unknown>;
  cookie?: Record<string, unknown>;
}

export interface OpenAPICallInput {
  parameters?: OpenAPIParameterInput;
  /** The exact application body. `false`, `0`, an empty string, and `null` are all present bodies. */
  body?: unknown;
  /** Concrete request media type. Required when a governing declaration is a media range. */
  mediaType?: string;
  /** Concrete media choices for multipart or form properties. */
  propertyMediaTypes?: Record<string, string>;
}

export interface OpenAPICallOptions {
  auth?: Record<string, OpenAPIAuthValue>;
  server?: OpenAPIServerSelection;
  headers?: HeadersInit;
  signal?: AbortSignal;
  maxDeliveryUnitBytes?: number;
  fetch?: typeof globalThis.fetch;
  /** Byte-preserving HTTP transport used when Fetch cannot carry the method token. */
  transport?: OpenAPIHostTransport | null;
  /** Defaults to `manual`, keeping redirect responses observable as the bound operation outcome. */
  redirect?: OpenAPIRedirectPolicy;
  parameterConverter?: OpenAPIParameterConverter;
  securityAlternative?: number;
  /** Chooses the entry or referring document for implicit 3.0 security-scheme names. */
  implicitConnectionScope?: "entry" | "referring";
  emptyValueForm?: OpenAPIEmptyValueForm;
  requestContentCodings?: Record<string, OpenAPIContentCodec>;
  responseContentCodings?: Record<string, OpenAPIContentCodec>;
  requestCharacterEncodings?: Record<string, OpenAPICharacterEncoder>;
  responseCharacterEncodings?: Record<string, OpenAPICharacterDecoder>;
}

export interface OpenAPIClientMiddlewareContext {
  operation: Readonly<OpenAPIOperationInfo>;
  request: OpenAPIPlannedRequest;
}

export interface OpenAPIClientMiddleware {
  onRequest?(context: OpenAPIClientMiddlewareContext): OpenAPIPlannedRequest | Response | void | Promise<OpenAPIPlannedRequest | Response | void>;
  onResponse?(context: OpenAPIClientMiddlewareContext & { response: Response }): Response | void | Promise<Response | void>;
  onError?(context: OpenAPIClientMiddlewareContext & { error: unknown }): Response | Error | void | Promise<Response | Error | void>;
}

export interface OpenAPIClientOptions {
  auth?: Record<string, OpenAPIAuthValue>;
  server?: OpenAPIServerSelection;
  headers?: HeadersInit;
  /** Default cancellation signal for invocations. Document loading is separate. */
  signal?: AbortSignal;
  /** Fetch implementation used only to retrieve the entry document and external references. */
  documentFetch?: typeof globalThis.fetch;
  /** Cancels entry-document and external-reference retrieval only. */
  documentSignal?: AbortSignal;
  /** Default Fetch implementation for operation invocations. */
  fetch?: typeof globalThis.fetch;
  /** Byte-preserving HTTP transport used when Fetch cannot carry the method token. */
  transport?: OpenAPIHostTransport | null;
  /** Defaults to `manual`; set `follow` to opt into ordinary user-agent redirect behavior. */
  redirect?: OpenAPIRedirectPolicy;
  middleware?: OpenAPIClientMiddleware[];
  maxDeliveryUnitBytes?: number;
  parameterConverter?: OpenAPIParameterConverter;
  securityAlternative?: number;
  /** Chooses the entry or referring document for implicit 3.0 security-scheme names. */
  implicitConnectionScope?: "entry" | "referring";
  emptyValueForm?: OpenAPIEmptyValueForm;
  requestContentCodings?: Record<string, OpenAPIContentCodec>;
  responseContentCodings?: Record<string, OpenAPIContentCodec>;
  requestCharacterEncodings?: Record<string, OpenAPICharacterEncoder>;
  responseCharacterEncodings?: Record<string, OpenAPICharacterDecoder>;
}

export interface OpenAPIOperationInfo {
  ref: string;
  path: string;
  method: HTTPMethod;
  /** HTTP method token emitted on the wire. */
  wireMethod: string;
  /** True only for an OAS 3.2 `additionalOperations` member. */
  additional: boolean;
  operationId?: string;
  summary?: string;
  tags: readonly string[];
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

export type OpenAPIConfigurationRequirement =
  | {
    kind: "option";
    /** Native client or call option name, such as `server` or `securityAlternative`. */
    name: string;
    /** JSON Pointer within the option value; the empty pointer names the whole option. */
    path: string;
    allowedValues?: readonly unknown[];
    description?: string;
  }
  | {
    kind: "input";
    /** Native call-input member: `mediaType` or `propertyMediaTypes`. */
    name: "mediaType" | "propertyMediaTypes";
    /** JSON Pointer within the input member; the empty pointer names the whole member. */
    path: string;
    allowedValues?: readonly unknown[];
    description?: string;
  }
  | {
    kind: "credential";
    /** Security-scheme name exactly as authored in the OpenAPI document. */
    name: string;
    /** Required credential family, such as `apiKey`, `basic`, `bearer`, or `oauth2`. */
    credential: string;
    description?: string;
  };

/** Alternatives are disjunctive; every requirement inside one alternative is conjunctive. */
export interface OpenAPIConfigurationRequirements {
  target: string;
  alternatives: readonly (readonly OpenAPIConfigurationRequirement[])[];
}

export class OpenAPIClientError extends Error {
  readonly kind: OpenAPIClientErrorKind;
  readonly code: string;
  readonly details?: unknown;
  /** Actionable alternatives when `code` is `CONFIGURATION_REQUIRED`. */
  readonly requirements?: OpenAPIConfigurationRequirements;

  constructor(kind: OpenAPIClientErrorKind, code: string, message: string, options?: {
    cause?: unknown;
    details?: unknown;
    requirements?: OpenAPIConfigurationRequirements;
  }) {
    super(message, options?.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = "OpenAPIClientError";
    this.kind = kind;
    this.code = code;
    if (options?.details !== undefined) this.details = options.details;
    if (options?.requirements !== undefined) this.requirements = options.requirements;
  }
}

export interface OpenAPIOperationClient {
  readonly info: OpenAPIOperationInfo;
  call<T = unknown, E = unknown>(input?: OpenAPICallInput, options?: OpenAPICallOptions): Promise<OpenAPIResult<T, E>>;
  stream<T = unknown, E = unknown>(input?: OpenAPICallInput, options?: OpenAPICallOptions): Promise<OpenAPIStreamResult<T, E>>;
}

interface ResolvedOperation {
  info: OpenAPIOperationInfo;
  pathItem?: OpenAPIPathItem;
  operation?: OpenAPIOperation;
  target?: OpenAPIResolvedOperation;
}

interface ObservedExchange {
  request?: OpenAPIPlannedRequest;
  response: Response;
}

/**
 * A document-driven Swagger 2.0 and OpenAPI 3.x client. The document remains the runtime
 * authority for operation lookup, request construction, security placement,
 * and response interpretation; no generated client or OBI is required.
 */
export class OpenAPIClient {
  readonly edition: OpenAPIEdition;
  readonly location?: string;

  private readonly engine = new OpenAPIEngine();
  private readonly options: OpenAPIClientOptions;
  private readonly artifact?: OpenAPIArtifact;
  private readonly document?: OpenAPIDocument;
  private readonly swagger20?: Swagger20Client;
  private readonly inventory: readonly ResolvedOperation[];

  private constructor(args: {
    edition: OpenAPIEdition;
    location?: string;
    artifact?: OpenAPIArtifact;
    swagger20?: Swagger20Client;
    inventory: readonly ResolvedOperation[];
    options: OpenAPIClientOptions;
  }) {
    this.artifact = args.artifact;
    this.document = args.artifact?.document;
    this.swagger20 = args.swagger20;
    this.edition = args.edition;
    this.location = args.location;
    this.inventory = args.inventory;
    this.options = snapshotClientOptions(args.options);
  }

  static async load(source: OpenAPISource, options: OpenAPIClientOptions = {}): Promise<OpenAPIClient> {
    const normalized = await materializeSource(normalizeSource(source), options);
    const family = sourceFamily(normalized.content);
    if (family === "2.0") {
      try {
        const swagger20 = await loadSwagger20(normalized, {
          signal: options.documentSignal,
          fetch: options.documentFetch,
        });
        const inventory = (await swagger20.operations()).map((operation): ResolvedOperation => ({
          info: {
            ...operation,
            wireMethod: operation.method.toUpperCase(),
            additional: false,
          },
        }));
        return new OpenAPIClient({
          edition: "2.0",
          location: normalized.location,
          swagger20,
          inventory,
          options,
        });
      } catch (error: unknown) {
        throw new OpenAPIClientError("source", "SOURCE_LOAD_FAILED", errorMessage(error), { cause: error });
      }
    }
    let artifact: OpenAPIArtifact;
    try {
      artifact = await loadOpenAPIArtifact(normalized, {
        signal: options.documentSignal,
        fetch: options.documentFetch,
      });
    } catch (error: unknown) {
      throw new OpenAPIClientError("source", "SOURCE_LOAD_FAILED", errorMessage(error), { cause: error });
    }
    const inventory: ResolvedOperation[] = [];
    for (const disposition of await artifact.operationInventory()) {
      if (!disposition.target) continue;
      const { reference } = disposition;
      const operation = disposition.target.operation;
      inventory.push({
        pathItem: disposition.target.pathItem,
        operation,
        target: disposition.target,
        info: {
          ref: reference.ref,
          path: reference.path,
          method: reference.method,
          wireMethod: reference.wireMethod,
          additional: reference.additional,
          ...(operation.operationId ? { operationId: operation.operationId } : {}),
          ...(operation.summary ? { summary: operation.summary } : {}),
          tags: [...(operation.tags ?? [])],
        },
      });
    }
    inventory.sort((left, right) => left.info.ref < right.info.ref ? -1 : left.info.ref > right.info.ref ? 1 : 0);
    return new OpenAPIClient({
      edition: artifact.edition,
      location: artifact.location,
      artifact,
      inventory,
      options,
    });
  }

  operations(): OpenAPIOperationInfo[] {
    const result: OpenAPIOperationInfo[] = [];
    for (const resolved of this.inventory) result.push(cloneOperationInfo(resolved.info));
    return result;
  }

  operation(selector: OpenAPIOperationSelector): OpenAPIOperationClient {
    const resolved = this.selectOperation(selector);
    return {
      info: cloneOperationInfo(resolved.info),
      call: <T = unknown, E = unknown>(input: OpenAPICallInput = {}, options: OpenAPICallOptions = {}) =>
        this.callResolved<T, E>(resolved, input, options),
      stream: <T = unknown, E = unknown>(input: OpenAPICallInput = {}, options: OpenAPICallOptions = {}) =>
        this.streamResolved<T, E>(resolved, input, options, false),
    };
  }

  async call<T = unknown, E = unknown>(
    selector: OpenAPIOperationSelector,
    input: OpenAPICallInput = {},
    options: OpenAPICallOptions = {},
  ): Promise<OpenAPIResult<T, E>> {
    return this.callResolved<T, E>(this.selectOperation(selector), input, options);
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
    return this.streamResolved<T, E>(this.selectOperation(selector), input, options, false);
  }

  private selectOperation(selector: OpenAPIOperationSelector): ResolvedOperation {
    if (this.artifact?.sourceExclusion) {
      throw new OpenAPIClientError(
        "operation",
        "SOURCE_EXCLUDED",
        this.artifact.sourceExclusion,
      );
    }
    try {
      return resolveOperation(this.inventory, selector);
    } catch (error: unknown) {
      if (typeof selector !== "object" || !("ref" in selector)) throw error;
      if (this.swagger20) {
        const parsed = /^#\/paths\/([^/]+)\/(get|put|post|delete|options|head|patch)$/u.exec(selector.ref);
        if (!parsed) throw error;
        const path = parsed[1]!.replace(/~1/gu, "/").replace(/~0/gu, "~");
        const pathItem = asRecord(asRecord(this.swagger20.document.root.paths)?.[path]);
        if (!pathItem || !Object.hasOwn(pathItem, "$ref")) throw error;
        const method = parsed[2]!;
        return {
          info: {
            ref: selector.ref,
            path,
            method,
            wireMethod: method.toUpperCase(),
            additional: false,
            tags: [],
          },
        };
      }
      if (!this.artifact || this.edition !== "3.2.0") throw error;
      const reference = parseOpenAPI32OperationReference(selector.ref);
      const rootPath = this.document?.paths?.[reference.path];
      if (!rootPath && !this.artifact.refusal) throw error;
      if (rootPath && !Object.hasOwn(rootPath, "$ref")) {
        const declared = reference.additional
          ? asRecord(rootPath.additionalOperations)?.[reference.method]
          : rootPath[reference.method];
        if (declared === undefined) throw error;
      }
      return {
        info: {
          ref: reference.ref,
          path: reference.path,
          method: reference.method,
          wireMethod: reference.wireMethod,
          additional: reference.additional,
          tags: [],
        },
      };
    }
  }

  private async callResolved<T, E>(
    resolved: ResolvedOperation,
    input: OpenAPICallInput,
    callOptions: OpenAPICallOptions,
  ): Promise<OpenAPIResult<T, E>> {
    const stream = await this.streamResolved<T, E>(resolved, input, callOptions, true);
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
    preserveSuccessfulResponseBody: boolean,
  ): Promise<OpenAPIStreamResult<T, E>> {
    if (this.swagger20) return this.streamSwagger20<T, E>(resolved, input, callOptions);
    if (!this.artifact || !this.document) {
      throw new OpenAPIClientError("internal", "INCOMPLETE_OPERATION", "loaded operation has no executable target");
    }
    if (!resolved.pathItem || !resolved.operation || !resolved.target) {
      try {
        resolved = resolvedOperation(await this.artifact.resolveOperation(resolved.info.ref));
      } catch (error: unknown) {
        throw new OpenAPIClientError(
          "operation",
          "OPERATION_UNAVAILABLE",
          errorMessage(error),
          { cause: error },
        );
      }
    }
    if (!resolved.pathItem || !resolved.operation || !resolved.target) {
      throw new OpenAPIClientError("internal", "INCOMPLETE_OPERATION", "resolved operation has no executable target");
    }
    if (Object.hasOwn(input, "body") && requestBodyIsForbiddenForClient(
      resolved.info.wireMethod,
      this.edition,
    )) {
      throw new OpenAPIClientError(
        "input",
        "BODY_FORBIDDEN_FOR_METHOD",
        `method ${JSON.stringify(resolved.info.wireMethod)} cannot carry the supplied request body under the selected OpenAPI binding`,
      );
    }
    const targetDocument = resolved.target?.document ?? this.document;
    const native = await nativeInput(
      targetDocument,
      resolved.pathItem,
      resolved.operation,
      input,
    );
    const headers = mergeHeaders(this.options.headers, callOptions.headers);
    const security = nativeContext(
      targetDocument,
      this.options,
      callOptions,
      native.mediaType,
      input.propertyMediaTypes,
      headers,
    );
    const exchange = deferred<ObservedExchange>();
    // Transport errors are also reported through the execution lifecycle.
    // Mark this observational promise as handled in case execution terminates
    // before the response-race is installed.
    void exchange.promise.catch(() => undefined);
    const injectedFetch = callOptions.fetch ?? this.options.fetch;
    const baseFetch = injectedFetch ?? globalThis.fetch;
    const doFetch = observedFetch(
      baseFetch,
      [...(this.options.middleware ?? [])],
      resolved.info,
      exchange,
      callOptions.redirect ?? this.options.redirect ?? "manual",
      preserveSuccessfulResponseBody,
    );
    const configuredTransport = callOptions.transport !== undefined
      ? callOptions.transport
      : this.options.transport;
    let doTransport: OpenAPIHostTransport | null | undefined;
    if (configuredTransport !== undefined) {
      doTransport = configuredTransport === null
        ? null
        : observedHostTransport(
          configuredTransport,
          [...(this.options.middleware ?? [])],
          resolved.info,
          exchange,
          preserveSuccessfulResponseBody,
        );
    } else if (injectedFetch === undefined && !fetchCarriesMethod(resolved.info.wireMethod)) {
      // The built-in host transport is a fallback for methods Fetch cannot
      // carry. It is not a general override for ordinary Fetch traffic.
      const fallback = await defaultHostTransport();
      if (fallback !== null && !hostCarriesMethod(resolved.info.wireMethod)) {
        throw new OpenAPIClientError(
          "input",
          "ERR_REFUSED",
          hostMethodRefusal(resolved.info.wireMethod, true),
        );
      }
      doTransport = fallback === null
        ? null
        : observedHostTransport(
          fallback,
          [...(this.options.middleware ?? [])],
          resolved.info,
          exchange,
          preserveSuccessfulResponseBody,
        );
    }
    let execution;
    try {
      const prepared = await this.engine.prepare({
        source: {
          location: this.location,
          artifact: this.artifact,
        },
        ref: resolved.info.ref,
        profile: OPENAPI_PROFILE_FULL,
        context: security.context,
        maxDeliveryUnitBytes: callOptions.maxDeliveryUnitBytes ?? this.options.maxDeliveryUnitBytes,
        signal: callOptions.signal ?? this.options.signal,
        fetch: doFetch,
        ...(doTransport === undefined ? {} : { hostTransport: doTransport }),
        redirect: callOptions.redirect ?? this.options.redirect,
        securityHandlers: artifactSecurityHandlers(security.handlers, resolved.info),
        parameterConverter: callOptions.parameterConverter ?? this.options.parameterConverter,
        requestContentCodings: callOptions.requestContentCodings ?? this.options.requestContentCodings,
        responseContentCodings: callOptions.responseContentCodings ?? this.options.responseContentCodings,
        requestCharacterEncodings: callOptions.requestCharacterEncodings ?? this.options.requestCharacterEncodings,
        responseCharacterEncodings: callOptions.responseCharacterEncodings ?? this.options.responseCharacterEncodings,
      });
      execution = await prepared.start<unknown, unknown>();
    } catch (error: unknown) {
      throw clientError(error);
    }

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
        events: mapOutputs<T>(
          execution.events,
          nativeResponseUsesRawBoundary(targetDocument, resolved.operation, observed.response),
          isSSEContentType(observed.response.headers.get("content-type")),
        ),
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

  private async streamSwagger20<T, E>(
    resolved: ResolvedOperation,
    input: OpenAPICallInput,
    callOptions: OpenAPICallOptions,
  ): Promise<OpenAPIStreamResult<T, E>> {
    const swagger20 = this.swagger20;
    if (!swagger20) throw new OpenAPIClientError("internal", "INCOMPLETE_ARTIFACT", "Swagger 2.0 artifact is unavailable");
    const auth = swagger20Credentials(
      swagger20.document.toJSON(),
      { ...(this.options.auth ?? {}), ...(callOptions.auth ?? {}) },
    );
    const selectedServer = callOptions.server ?? this.options.server;
    const server = swagger20ServerSelection(selectedServer);
    const exchange = deferred<ObservedExchange>();
    void exchange.promise.catch(() => undefined);
    const fetchFn = observedFetch(
      callOptions.fetch ?? this.options.fetch ?? globalThis.fetch,
      [...(this.options.middleware ?? [])],
      resolved.info,
      exchange,
      callOptions.redirect ?? this.options.redirect ?? "manual",
      false,
      mergeHeaders(this.options.headers, callOptions.headers),
    );
    let prepared;
    try {
      prepared = await prepareSwagger20({
        source: { location: this.location, document: swagger20.document },
        ref: resolved.info.ref,
        fetch: fetchFn,
        signal: callOptions.signal ?? this.options.signal,
        redirect: callOptions.redirect ?? this.options.redirect,
        ...(server.url ? { server: server.url } : {}),
        ...(server.index !== undefined ? { serverSchemeIndex: server.index } : {}),
        securityAlternative: callOptions.securityAlternative ?? this.options.securityAlternative,
        securityCredentials: auth,
        requestMedia: input.mediaType,
        propertyMedia: input.propertyMediaTypes,
        emptyValueForm: callOptions.emptyValueForm ?? this.options.emptyValueForm,
        requestContentCodings: contentCodingMap(callOptions.requestContentCodings ?? this.options.requestContentCodings),
        responseContentCodings: contentCodingMap(callOptions.responseContentCodings ?? this.options.responseContentCodings),
        requestCharacterEncodings: contentCodingMap(callOptions.requestCharacterEncodings ?? this.options.requestCharacterEncodings),
        responseCharacterEncodings: contentCodingMap(callOptions.responseCharacterEncodings ?? this.options.responseCharacterEncodings),
        parameterConverter: (callOptions.parameterConverter ?? this.options.parameterConverter) as never,
      });
      const parameters = await prepared.parameters();
      const native = swagger20Input(parameters, input);
      const result = await prepared.execute(native);
      const openapi: OpenAPIDeclarationMatch = result.declaration;
      return {
        ok: true,
        events: singleEvent<T>(result.outputPresent, result.output),
        closed: Promise.resolve(),
        cancel: async () => undefined,
        response: result.response,
        openapi,
      };
    } catch (error: unknown) {
      if (error instanceof Swagger20ExecutionError && error.code === "ERR_EXECUTION_FAILED") {
        const evidence = asRecord(error.evidence);
        const declaration = asRecord(evidence?.openapi);
        const response = evidence?.response instanceof Response
          ? evidence.response
          : new Response(null, { status: typeof evidence?.status === "number" ? evidence.status : 500 });
        return {
          ok: false,
          error: error.details as E | undefined,
          response,
          openapi: {
            declared: declaration?.declared === true,
            ...(typeof declaration?.responseKey === "string" && declaration.responseKey !== ""
              ? { responseKey: declaration.responseKey }
              : {}),
            ...(typeof declaration?.mediaType === "string" && declaration.mediaType !== ""
              ? { mediaType: declaration.mediaType }
              : {}),
          },
        };
      }
      throw swagger20ClientError(error);
    }
  }
}

function normalizeSource(source: OpenAPISource): { location?: string; content?: unknown } {
  if (typeof source === "string") return { location: source };
  if (source instanceof URL) return { location: source.toString() };
  if (source !== null && typeof source === "object" && ("location" in source || "content" in source)) {
    return source as { location?: string; content?: unknown };
  }
  return { content: source };
}

async function materializeSource(
  source: { location?: string; content?: unknown },
  options: OpenAPIClientOptions,
): Promise<{ location?: string; content: unknown }> {
  if (source.content !== undefined) {
    const content = source.content instanceof Blob
      ? await source.content.text()
      : source.content instanceof ArrayBuffer
        ? new Uint8Array(source.content)
        : source.content;
    return { ...source, content };
  }
  if (!source.location) throw new OpenAPIClientError("source", "SOURCE_LOAD_FAILED", "source requires location or content");
  try {
    // Parsing the discriminator here makes edition selection one retrieval,
    // not a speculative 3.x fetch followed by a Swagger 2.0 retry.
    const response = await (options.documentFetch ?? globalThis.fetch)(source.location, { signal: options.documentSignal });
    if (!response.ok) throw new Error(`HTTP ${response.status} ${response.statusText}`.trim());
    return { location: response.url || source.location, content: await response.text() };
  } catch (error: unknown) {
    throw new OpenAPIClientError(
      "source",
      "SOURCE_LOAD_FAILED",
      `failed to load ${JSON.stringify(source.location)}: ${errorMessage(error)}`,
      { cause: error },
    );
  }
}

function sourceFamily(content: unknown): "2.0" | "3.x" {
  let root = content;
  if (content instanceof Uint8Array) root = parseJSONOrYAML(new TextDecoder("utf-8", { fatal: true }).decode(content));
  else if (typeof content === "string") root = parseJSONOrYAML(content);
  const object = asRecord(root);
  if (!object) throw new OpenAPIClientError("source", "SOURCE_LOAD_FAILED", "OpenAPI entry resource must be a JSON object");
  const hasSwagger = Object.hasOwn(object, "swagger");
  const hasOpenAPI = Object.hasOwn(object, "openapi");
  if (object.swagger === "2.0") return "2.0";
  if (hasSwagger) throw new OpenAPIClientError("source", "SOURCE_LOAD_FAILED", "unsupported Swagger version: expected exact string \"2.0\"");
  if (hasOpenAPI) return "3.x";
  throw new OpenAPIClientError("source", "SOURCE_LOAD_FAILED", "entry resource has no swagger or openapi discriminator");
}

function resolveOperation(operations: readonly ResolvedOperation[], selector: OpenAPIOperationSelector): ResolvedOperation {
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
    const match = operations.find((candidate) => candidate.info.ref === selector.ref);
    if (match) return match;
    throw new OpenAPIClientError("operation", "OPERATION_NOT_FOUND", `operation ref ${JSON.stringify(selector.ref)} was not found`);
  }
  const matches = operations.filter((candidate) =>
    candidate.info.path === selector.path
    && candidate.info.method === selector.method
    && (selector.additional === undefined || candidate.info.additional === selector.additional));
  if (matches.length === 1) return matches[0]!;
  if (matches.length > 1) {
    throw new OpenAPIClientError(
      "operation",
      "AMBIGUOUS_OPERATION",
      `${selector.method} ${selector.path} identifies both fixed and additional operations; specify additional or use ref`,
    );
  }
  throw new OpenAPIClientError(
    "operation",
    "OPERATION_NOT_FOUND",
    `${selector.method} ${selector.path} was not found`,
  );
}

function cloneOperationInfo(info: OpenAPIOperationInfo): OpenAPIOperationInfo {
  return { ...info, tags: [...info.tags] };
}

function snapshotClientOptions(options: OpenAPIClientOptions): OpenAPIClientOptions {
  return {
    ...options,
    ...(options.auth ? { auth: { ...options.auth } } : {}),
    ...(options.server ? { server: snapshotServer(options.server) } : {}),
    ...(options.headers ? { headers: new Headers(options.headers) } : {}),
    ...(options.middleware ? { middleware: [...options.middleware] } : {}),
    ...(options.requestContentCodings ? { requestContentCodings: { ...options.requestContentCodings } } : {}),
    ...(options.responseContentCodings ? { responseContentCodings: { ...options.responseContentCodings } } : {}),
    ...(options.requestCharacterEncodings ? { requestCharacterEncodings: { ...options.requestCharacterEncodings } } : {}),
    ...(options.responseCharacterEncodings ? { responseCharacterEncodings: { ...options.responseCharacterEncodings } } : {}),
  };
}

function snapshotServer(server: OpenAPIServerSelection): OpenAPIServerSelection {
  if ("url" in server) return { url: server.url };
  if (server.index !== undefined) {
    return {
      index: server.index,
      ...(server.variables ? { variables: { ...server.variables } } : {}),
    };
  }
  return { variables: { ...server.variables } };
}

function resolvedOperation(target: OpenAPIResolvedOperation): ResolvedOperation {
  const { reference } = target;
  const operation = target.operation;
  return {
    target,
    pathItem: target.pathItem,
    operation,
    info: {
      ref: reference.ref,
      path: reference.path,
      method: reference.method,
      wireMethod: reference.wireMethod,
      additional: reference.additional,
      ...(operation.operationId ? { operationId: operation.operationId } : {}),
      ...(operation.summary ? { summary: operation.summary } : {}),
      tags: [...(operation.tags ?? [])],
    },
  };
}

async function nativeInput(
  document: OpenAPIDocument,
  pathItem: OpenAPIPathItem,
  operation: OpenAPIOperation,
  input: OpenAPICallInput,
): Promise<{ supplied: boolean; value: unknown; mediaType?: string }> {
  assertNativeInputShape(input);
  const parameters = effectiveParameters(pathItem, operation);
  const plans = planResolvedRequestBodies(operation, {
    profile: OPENAPI_PROFILE_FULL,
    openapiVersion: document.openapi,
    inventoryUnsupported: true,
  });
  const selectedPlans = selectBodyPlans(plans, input.mediaType, document.openapi);
  const routes = planAbstractInputRoutes(parameters, selectedPlans, OPENAPI_PROFILE_FULL);
  const value: Record<string, unknown> = {};
  let supplied = false;

  const providedParameters = input.parameters ?? {};
  for (const location of ["path", "query", "querystring", "header", "cookie"] as const) {
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
      [OPENAPI_PROFILE_FULL.inputRouteKey]: OPENAPI_PROFILE_FULL.inputRouteMarker,
      value,
      parameters: routes.parameters,
      body: bodyDescriptor,
    }],
    // Only an explicit caller choice becomes configuration.requestMedia.
    // A sole concrete declaration self-selects inside the engine; copying it
    // into configuration here would also (incorrectly) turn the first member
    // of a multi-alternative declaration into an implicit preference.
    ...(input.mediaType ? { mediaType: input.mediaType } : {}),
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

function contentCodingMap<T>(
  codecs: Record<string, T> | undefined,
): ReadonlyMap<string, T> | undefined {
  return codecs ? new Map(Object.entries(codecs)) : undefined;
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
  const supported = plans.filter((plan) => !plan.unsupported);
  const concrete = supported.filter((plan) => !plan.range);
  // This choice is used only to construct the routed caller envelope. The
  // engine independently elects requestMedia and raises the explicit
  // prerequisite before consuming input when the artifact has no sole choice.
  return (concrete.length > 0 ? concrete : supported).slice(0, 1);
}

function nativeContext(
  document: OpenAPIDocument,
  defaults: OpenAPIClientOptions,
  call: OpenAPICallOptions,
  mediaType: string | undefined,
  propertyMediaTypes: Record<string, string> | undefined,
  headers: Headers,
): { context: Record<string, unknown>; handlers: Record<string, NativeSecurityHandler> } {
  const context: Record<string, unknown> = {};
  const handlers: Record<string, NativeSecurityHandler> = {};
  const configuration: Record<string, unknown> = {};
  const server = call.server ?? defaults.server;
  if (server !== undefined) {
    configuration.server = "url" in server
      ? { baseUrl: server.url }
      : {
        ...(server.index !== undefined ? { index: server.index } : {}),
        ...(server.variables ? { variables: server.variables } : {}),
      };
  }
  const securityAlternative = call.securityAlternative ?? defaults.securityAlternative;
  if (securityAlternative !== undefined) configuration.security = { index: securityAlternative };
  const implicitConnectionScope = call.implicitConnectionScope ?? defaults.implicitConnectionScope;
  if (implicitConnectionScope !== undefined) configuration.implicitConnectionScope = implicitConnectionScope;
  if (mediaType) configuration.requestMedia = mediaType;
  if (propertyMediaTypes && Object.keys(propertyMediaTypes).length > 0) {
    configuration.propertyMedia = { ...propertyMediaTypes };
  }
  if (Object.keys(configuration).length > 0) context.configuration = configuration;
  if ([...headers].length > 0) context.headers = Object.fromEntries(headers);

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
      if (basicCredentialIsPortable(credential)) context.basic = credential;
    } else if (scheme.type === "http" && (scheme.scheme ?? "").toLowerCase() === "bearer") {
      if (typeof credential !== "string") throw credentialShape(name, "a token string");
      if (!bearerToken(credential)) throw credentialShape(name, "a nonempty RFC 6750 b64token");
      context.bearerToken = credential;
    } else if (scheme.type === "oauth2" || scheme.type === "openIdConnect") {
      if (typeof credential !== "string") throw credentialShape(name, "an access-token string");
      if (!bearerToken(credential)) throw credentialShape(name, "a nonempty RFC 6750 b64token");
      context.accessToken = credential;
    } else {
      throw new OpenAPIClientError(
        "configuration",
        "UNSUPPORTED_SECURITY_SCHEME",
        `security scheme ${JSON.stringify(name)} uses ${unsupportedSchemeLabel(scheme)}, which the built-in credential adapter cannot apply; supply a scheme-named security handler`,
      );
    }
  }
  if (Object.keys(apiKeys).length > 0) context.apiKeys = apiKeys;
  return { context, handlers };
}

function basicCredentialIsPortable(value: { username: string; password: string }): boolean {
  return !value.username.includes(":") && [value.username, value.password].every((member) =>
    [...member].every((character) => {
      const code = character.codePointAt(0)!;
      return code >= 0x20 && code <= 0x7e;
    }));
}

function requestBodyIsForbiddenForClient(method: string, edition: OpenAPIEdition): boolean {
  const normalized = method.toUpperCase();
  if (normalized === "TRACE") return true;
  return edition.startsWith("3.0.") && ["GET", "HEAD", "DELETE", "OPTIONS"].includes(normalized);
}

interface NativeSecurityScheme extends Record<string, unknown> {
  type: string;
  scheme?: string;
}

interface NativeSecurityHandler {
  scheme: NativeSecurityScheme;
  handler: OpenAPISecurityHandler;
}

function artifactSecurityHandlers(
  handlers: Record<string, NativeSecurityHandler>,
  operation: OpenAPIOperationInfo,
): Record<string, (request: OpenAPIPlannedRequest, context: { schemeName: string; scheme: unknown }) => Promise<OpenAPIPlannedRequest | void>> {
  return Object.fromEntries(Object.entries(handlers).map(([name, security]) => [
    name,
    async (request: OpenAPIPlannedRequest, context: { schemeName: string; scheme: unknown }) => {
      try {
        return await security.handler({
          operation: cloneOperationInfo(operation),
          schemeName: context.schemeName,
          scheme: cloneJSONRecord(context.scheme),
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

function cloneJSONRecord(value: unknown): NativeSecurityScheme {
  const record = asRecord(value) ?? {};
  return JSON.parse(JSON.stringify(record)) as NativeSecurityScheme;
}

function unsupportedSchemeLabel(scheme: NativeSecurityScheme): string {
  if (scheme.type === "http") return `HTTP scheme ${JSON.stringify(scheme.scheme ?? "")}`;
  return `type ${JSON.stringify(scheme.type)}`;
}

function credentialShape(name: string, expected: string): OpenAPIClientError {
  return new OpenAPIClientError("configuration", "INVALID_CREDENTIAL", `security scheme ${JSON.stringify(name)} requires ${expected}`);
}

function bearerToken(value: string): boolean {
  return /^[A-Za-z0-9\-._~+/]+={0,}$/u.test(value);
}

function securitySchemes(document: OpenAPIDocument): Record<string, NativeSecurityScheme> {
  const components = document.components;
  if (!components || typeof components !== "object" || Array.isArray(components)) return {};
  const raw = (components as Record<string, unknown>).securitySchemes;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const result: Record<string, NativeSecurityScheme> = {};
  for (const [name, value] of Object.entries(raw as Record<string, unknown>)) {
    if (value && typeof value === "object" && !Array.isArray(value) && typeof (value as NativeSecurityScheme).type === "string") {
      result[name] = value as NativeSecurityScheme;
    }
  }
  return result;
}

function swagger20Credentials(
  document: Record<string, unknown>,
  auth: Record<string, OpenAPIAuthValue>,
): Swagger20SecurityCredentials {
  const definitions = asRecord(document.securityDefinitions) ?? {};
  const credentials: Swagger20SecurityCredentials = {};
  for (const [name, supplied] of Object.entries(auth)) {
    const definition = asRecord(definitions[name]);
    if (!definition || typeof definition.type !== "string") {
      throw new OpenAPIClientError(
        "configuration",
        "UNKNOWN_SECURITY_SCHEME",
        `security scheme ${JSON.stringify(name)} was not found`,
      );
    }
    if (typeof supplied === "function") {
      throw new OpenAPIClientError(
        "configuration",
        "UNSUPPORTED_SECURITY_HANDLER",
        "Swagger 2.0 defines no custom security-scheme type for this handler",
      );
    }
    if (definition.type === "apiKey") {
      if (typeof supplied !== "string") throw credentialShape(name, "a string API key");
      (credentials.apiKeys ??= {})[name] = supplied;
      continue;
    }
    if (definition.type === "basic") {
      if (typeof supplied === "string") throw credentialShape(name, "{ username, password }");
      (credentials.basic ??= {})[name] = { userId: supplied.username, password: supplied.password };
      continue;
    }
    if (definition.type === "oauth2") {
      if (typeof supplied !== "string") throw credentialShape(name, "an access-token string");
      (credentials.oauth2 ??= {})[name] = { accessToken: supplied, scopes: [] };
      continue;
    }
    throw new OpenAPIClientError(
      "configuration",
      "UNSUPPORTED_SECURITY_SCHEME",
      `security scheme ${JSON.stringify(name)} uses unsupported Swagger 2.0 type ${JSON.stringify(definition.type)}`,
    );
  }
  return credentials;
}

function swagger20ServerSelection(selection: OpenAPIServerSelection | undefined): { url?: string; index?: number } {
  if (selection === undefined) return {};
  return "url" in selection ? { url: selection.url } : { ...(selection.index !== undefined ? { index: selection.index } : {}) };
}

function assertNativeInputShape(input: OpenAPICallInput): void {
  if (input === null || typeof input !== "object" || Array.isArray(input)) {
    throw new OpenAPIClientError("input", "INVALID_INPUT", "call input must be an object");
  }
  const allowed = new Set(["parameters", "body", "mediaType", "propertyMediaTypes"]);
  const extra = Object.keys(input).find((name) => !allowed.has(name));
  if (extra !== undefined) {
    throw new OpenAPIClientError("input", "UNKNOWN_INPUT_MEMBER", `call input contains unknown member ${JSON.stringify(extra)}`);
  }
  if (input.parameters !== undefined && !asRecord(input.parameters)) {
    throw new OpenAPIClientError("input", "INVALID_PARAMETERS", "call input parameters must be an object");
  }
  if (input.parameters !== undefined && !asRecord(input.parameters)) {
    throw new OpenAPIClientError("input", "INVALID_PARAMETERS", "call input parameters must be an object");
  }
  if (input.parameters) {
    const locations = new Set(["path", "query", "querystring", "header", "cookie"]);
    const extraLocation = Object.keys(input.parameters).find((name) => !locations.has(name));
    if (extraLocation !== undefined) {
      throw new OpenAPIClientError(
        "input",
        "UNKNOWN_PARAMETER_LOCATION",
        `call input contains unknown parameter location ${JSON.stringify(extraLocation)}`,
      );
    }
    for (const location of locations) {
      const value = input.parameters[location as keyof typeof input.parameters];
      if (value !== undefined && !asRecord(value)) {
        throw new OpenAPIClientError(
          "input",
          "INVALID_PARAMETER_GROUP",
          `call input parameter group ${JSON.stringify(location)} must be an object`,
      );
    }
    for (const location of locations) {
      const value = input.parameters[location as keyof typeof input.parameters];
      if (value !== undefined && !asRecord(value)) {
        throw new OpenAPIClientError(
          "input",
          "INVALID_PARAMETER_GROUP",
          `call input parameter group ${JSON.stringify(location)} must be an object`,
        );
      }
    }
  }
}
}

function swagger20Input(parameters: Swagger20ParameterInfo[], input: OpenAPICallInput): Swagger20Input {
  assertNativeInputShape(input);
  if (input.parameters?.cookie && Object.keys(input.parameters.cookie).length > 0) {
    throw new OpenAPIClientError("input", "UNKNOWN_PARAMETER_LOCATION", "Swagger 2.0 has no cookie Parameter location");
  }
  if (input.parameters?.querystring && Object.keys(input.parameters.querystring).length > 0) {
    throw new OpenAPIClientError("input", "UNKNOWN_PARAMETER_LOCATION", "Swagger 2.0 has no querystring Parameter location");
  }
  const routed: Swagger20Input = {
    parameters: {
      path: input.parameters?.path,
      query: input.parameters?.query,
      header: input.parameters?.header,
    },
  };
  const bodyPresent = Object.prototype.hasOwnProperty.call(input, "body");
  if (!bodyPresent) return routed;
  if (parameters.some((parameter) => parameter.in === "formData")) {
    if (!asRecord(input.body)) {
      throw new OpenAPIClientError("input", "OBJECT_BODY_REQUIRED", "Swagger 2.0 formData requires an object body");
    }
    routed.parameters!.formData = input.body as Record<string, unknown>;
    return routed;
  }
  routed.body = input.body;
  routed.bodyPresent = true;
  return routed;
}

async function* singleEvent<T>(present: boolean, value: unknown): AsyncIterable<OpenAPIStreamEvent<T>> {
  if (present) yield { data: value as T };
}

function swagger20ClientError(error: unknown): OpenAPIClientError {
  if (error instanceof OpenAPIClientError) return error;
  if (!(error instanceof Swagger20ExecutionError)) return clientError(error);
  const kind: OpenAPIClientErrorKind =
    error.code === "SOURCE_LOAD_FAILED" ? "source"
    : error.code === "INVALID_OPERATION_REF" || error.code === "OPERATION_NOT_FOUND" ? "operation"
    : error.code === "CONTEXT_REQUIRED" ? "configuration"
    : error.code === "ERR_CONNECT_FAILED" ? "transport"
    : error.code === "ERR_RESPONSE_ERROR" ? "response"
    : error.code === "ERR_CANCELLED" ? "cancelled"
    : error.code === "ERR_REFUSED" ? "input"
    : "internal";
  const requirements = error.code === "CONTEXT_REQUIRED"
    ? configurationRequirements(error.details)
    : undefined;
  return new OpenAPIClientError(
    kind,
    requirements ? "CONFIGURATION_REQUIRED" : error.code,
    error.message,
    { cause: error, details: requirements ?? error.details, ...(requirements ? { requirements } : {}) },
  );
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function observedFetch(
  baseFetch: typeof globalThis.fetch,
  middleware: OpenAPIClientMiddleware[],
  operation: OpenAPIOperationInfo,
  exchange: Deferred<ObservedExchange>,
  logicalRedirect: OpenAPIRedirectPolicy,
  preserveSuccessfulResponseBody: boolean,
  additionalHeaders?: Headers,
): typeof globalThis.fetch {
  operation = cloneOperationInfo(operation);
  return async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    let request: OpenAPIPlannedRequest = {
      url: input instanceof Request ? input.url : String(input),
      method: input instanceof Request ? input.method : String(init?.method ?? "GET"),
      headers: new Headers(input instanceof Request ? input.headers : init?.headers),
      body: input instanceof Request ? input.body : init?.body ?? null,
      signal: input instanceof Request ? input.signal : init?.signal,
      redirect: input instanceof Request ? input.redirect : init?.redirect,
    };
    if (additionalHeaders) {
      const headers = new Headers(request.headers);
      additionalHeaders.forEach((value, name) => headers.set(name, value));
      request = { ...request, headers };
    }
    try {
      for (const item of middleware) {
        const changed = await item.onRequest?.({ operation, request });
        if (changed instanceof Response) {
          const response = await applyResponseMiddleware(middleware, operation, request, changed);
          exchange.resolve({ request, response: observedResponse(response, preserveSuccessfulResponseBody) });
          return response;
        }
        if (changed !== undefined) request = changed;
      }
      const init: RequestInit = {
        method: request.method,
        headers: request.headers,
        body: request.body,
        signal: request.signal,
        redirect: request.redirect,
      };
      // URL + init is the standard Fetch adapter contract and preserves the
      // engine's exact planned target for injected transports. Constructing a
      // WHATWG Request here would normalize dot segments before an adapter can
      // observe or carry them, and would reject otherwise valid extension
      // method tokens before the selected transport sees the plan.
      let response = await baseFetch(request.url, init);
      response = await applyResponseMiddleware(middleware, operation, request, response);
      const intermediate = logicalRedirect === "follow"
        && [301, 302, 303, 307, 308].includes(response.status)
        && response.headers.has("location")
        && !clientRedirectRewritesMethod(response.status, request.method);
      if (!intermediate) exchange.resolve({ request, response: observedResponse(response, preserveSuccessfulResponseBody) });
      return response;
    } catch (error: unknown) {
      for (const item of middleware) {
        const handled = await item.onError?.({ operation, request, error });
        if (handled instanceof Response) {
          const response = await applyResponseMiddleware(middleware, operation, request, handled);
          exchange.resolve({ request, response: observedResponse(response, preserveSuccessfulResponseBody) });
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

function clientRedirectRewritesMethod(status: number, method: string): boolean {
  const normalized = method.toUpperCase();
  if (status === 303) return normalized !== "GET" && normalized !== "HEAD";
  return (status === 301 || status === 302) && normalized === "POST";
}

function observedHostTransport(
  base: OpenAPIHostTransport,
  middleware: OpenAPIClientMiddleware[],
  operation: OpenAPIOperationInfo,
  exchange: Deferred<ObservedExchange>,
  preserveSuccessfulResponseBody: boolean,
): OpenAPIHostTransport {
  operation = cloneOperationInfo(operation);
  return async (url, request) => {
    let planned: OpenAPIPlannedRequest = { ...request, url, headers: new Headers(request.headers) };
    try {
      for (const item of middleware) {
        const changed = await item.onRequest?.({ operation, request: planned });
        if (changed instanceof Response) {
          const response = await applyResponseMiddleware(middleware, operation, planned, changed);
          exchange.resolve({ request: planned, response: observedResponse(response, preserveSuccessfulResponseBody) });
          return response;
        }
        if (changed !== undefined) planned = changed;
      }
      let response = await base(planned.url, planned);
      response = await applyResponseMiddleware(middleware, operation, planned, response);
      exchange.resolve({ request: planned, response: observedResponse(response, preserveSuccessfulResponseBody) });
      return response;
    } catch (error: unknown) {
      for (const item of middleware) {
        const handled = await item.onError?.({ operation, request: planned, error });
        if (handled instanceof Response) {
          const response = await applyResponseMiddleware(middleware, operation, planned, handled);
          exchange.resolve({ request: planned, response: observedResponse(response, preserveSuccessfulResponseBody) });
          return response;
        }
        if (handled instanceof Error) {
          exchange.reject(handled);
          throw handled;
        }
      }
      exchange.reject(error instanceof OpenAPIClientError
        ? error
        : new OpenAPIClientError("transport", "TRANSPORT_FAILED", errorMessage(error), { cause: error }));
      throw error;
    }
  };
}

/**
 * Unary and unsuccessful responses are bounded by the engine, so retaining a
 * replay branch is safe and useful. A successful stream can be unbounded: its
 * public Response therefore preserves native status/header/URL metadata while
 * its cloned body branch is cancelled immediately. This avoids WHATWG tee's
 * otherwise-unbounded buffering when the engine is the active stream reader.
 */
function observedResponse(response: Response, preserveSuccessfulBody: boolean): Response {
  const observed = response.clone();
  if (!preserveSuccessfulBody && response.status >= 200 && response.status < 300 && observed.body) {
    void observed.body.cancel().catch(() => undefined);
  }
  return observed;
}

async function applyResponseMiddleware(
  middleware: OpenAPIClientMiddleware[],
  operation: OpenAPIOperationInfo,
  request: OpenAPIPlannedRequest,
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
  sseResponse = false,
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
      const framed = sseResponse ? asRecord(item.value) : undefined;
      const applicationValue = framed && Object.hasOwn(framed, "data")
        ? framed.data
        : item.value;
      const value = rawBoundary && typeof applicationValue === "string"
        ? Uint8Array.from(atob(applicationValue), (character) => character.charCodeAt(0))
        : applicationValue;
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
    if (match !== null && Object.hasOwn(match.media, "itemSchema")) return false;
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
      : error.code === "ERR_MISSING_INPUT" || error.code === "ERR_VALIDATION_FAILED" || error.code === "ERR_REFUSED" ? "input"
      : error.code === "ERR_SOURCE_CONFIG_ERROR" || error.code === "CONTEXT_REQUIRED" ? "configuration"
      : error.code === "ERR_CONNECT_FAILED" ? "transport"
      : error.code === "ERR_PROTOCOL" ? "protocol"
      : error.code === "ERR_RESPONSE_ERROR" || error.code === "ERR_STREAM_ERROR" ? "response"
      : error.code === "ERR_CANCELLED" || error.code === "ERR_TIMEOUT" ? "cancelled"
      : "internal";
    const requirements = error.code === "CONTEXT_REQUIRED"
      ? configurationRequirements(error.details)
      : undefined;
    return new OpenAPIClientError(kind, requirements ? "CONFIGURATION_REQUIRED" : error.code, error.message, {
      cause: error,
      details: requirements ?? error.details,
      ...(requirements ? { requirements } : {}),
    });
  }
  return new OpenAPIClientError("internal", "INTERNAL_ERROR", errorMessage(error), { cause: error });
}

function configurationRequirements(value: unknown): OpenAPIConfigurationRequirements | undefined {
  const details = asRecord(value);
  if (!details || typeof details.target !== "string" || !Array.isArray(details.alternatives)) return undefined;
  const alternatives: OpenAPIConfigurationRequirement[][] = [];
  for (const rawAlternative of details.alternatives) {
    const alternative = asRecord(rawAlternative);
    if (!alternative || !Array.isArray(alternative.requirements)) return undefined;
    const requirements: OpenAPIConfigurationRequirement[] = [];
    for (const rawRequirement of alternative.requirements) {
      const requirement = asRecord(rawRequirement);
      if (!requirement || typeof requirement.type !== "string") return undefined;
      const description = typeof requirement.description === "string"
        ? requirement.description
        : undefined;
      if (requirement.type === "config.value") {
        if (typeof requirement.point !== "string" || typeof requirement.path !== "string") return undefined;
        const schema = asRecord(requirement.schema);
        const allowedValues = Array.isArray(schema?.enum)
          ? JSON.parse(JSON.stringify(schema.enum)) as unknown[]
          : undefined;
        const native = nativeConfigurationPoint(requirement.point);
        if (!native) return undefined;
        const common = {
          path: requirement.path,
          ...(allowedValues ? { allowedValues } : {}),
          ...(description ? { description } : {}),
        };
        requirements.push(native.kind === "input"
          ? { kind: "input", name: native.name, ...common }
          : { kind: "option", name: native.name, ...common });
      } else if (requirement.type.startsWith("auth.") && typeof requirement.name === "string") {
        requirements.push({
          kind: "credential",
          name: requirement.name,
          credential: requirement.type.slice("auth.".length),
          ...(description ? { description } : {}),
        });
      } else {
        return undefined;
      }
    }
    alternatives.push(requirements);
  }
  return { target: details.target, alternatives };
}

function nativeConfigurationPoint(point: string):
  | { kind: "input"; name: "mediaType" | "propertyMediaTypes" }
  | { kind: "option"; name: string }
  | undefined {
  switch (point) {
    case "requestMedia": return { kind: "input", name: "mediaType" };
    case "propertyMedia": return { kind: "input", name: "propertyMediaTypes" };
    case "security": return { kind: "option", name: "securityAlternative" };
    case "parameterConversion": return { kind: "option", name: "parameterConverter" };
    case "server":
    case "emptyValueForm":
    case "requestContentCodings":
    case "responseContentCodings":
    case "requestCharacterEncodings":
    case "responseCharacterEncodings":
      return { kind: "option", name: point };
    default:
      return undefined;
  }
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
