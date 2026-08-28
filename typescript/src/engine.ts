import {
  InvocationError,
  InvocationImpl,
  USE_DEFAULT,
  newInvokeHooks,
  type ArtifactSecurityHandler,
  type ContextRequiredDetails,
  type Invocation,
  type InvokeHooks,
  type InvokeSite,
  type Metadata,
  type RawResult,
} from "./internal/index.js";
import {
  OPENAPI_PROFILE_FULL,
  type OpenAPIExecutionProfile,
} from "./profile.js";
import type { OpenAPIDocument } from "./types.js";
import {
  preflightTarget,
  requiredContext,
  requiredRequestMediaContext,
  runBinding,
} from "./invoke.js";
import { errorMessage, loadOpenAPIDocument, parseRef } from "./util.js";
import { computeAcceptanceFloor, floorInvalidTargetMessage, floorOpVerdict, type AcceptanceFloor } from "./acceptance-floor.js";
import type { OpenAPIParameterConverter } from "./params.js";

/** Artifact source accepted by the SDK-neutral execution engine. */
export interface OpenAPIEngineSource {
  /** Retrieval base and, when content is absent, the artifact location. */
  location?: string;
  /** Parsed object, JSON text, YAML text, or UTF-8 bytes. */
  content?: unknown;
}

export interface OpenAPISecurityHandlerContext {
  schemeName: string;
  scheme: unknown;
}

/** Applies an artifact-authored security scheme not covered by a builtin. */
export type OpenAPIEngineSecurityHandler = (
  request: Request,
  context: OpenAPISecurityHandlerContext,
) => Request | void | Promise<Request | void>;

/** Protocol-native facts supplied to an optional execution hook. */
export interface OpenAPIHookResult {
  status: number | null;
  body: string;
  metadata: Record<string, string[]>;
}

/** Location at which an execution hook is consulted. */
export interface OpenAPIHookSite {
  ref: string;
  target: string;
  profile: string;
}

/** Declines a hook decision and continues to the next tier or builtin. */
export const OPENAPI_USE_DEFAULT: unique symbol = Symbol("openapi: use default");

export interface OpenAPIExecutionHooks {
  decode?(
    site: OpenAPIHookSite,
    result: OpenAPIHookResult,
  ): unknown | typeof OPENAPI_USE_DEFAULT | Promise<unknown | typeof OPENAPI_USE_DEFAULT>;
  classify?(
    site: OpenAPIHookSite,
    result: OpenAPIHookResult,
  ): boolean | typeof OPENAPI_USE_DEFAULT | Promise<boolean | typeof OPENAPI_USE_DEFAULT>;
}

export interface OpenAPIEngineOptions {
  fetch?: typeof globalThis.fetch;
  redirect?: RequestRedirect;
  hooks?: OpenAPIExecutionHooks;
  maxDeliveryUnitBytes?: number;
  parameterConverter?: OpenAPIParameterConverter;
}

/** Inputs fixed before an operation is prepared. */
export interface OpenAPIPrepareOptions {
  source: OpenAPIEngineSource;
  ref: string;
  profile?: OpenAPIExecutionProfile;
  context?: Record<string, unknown>;
  signal?: AbortSignal;
  fetch?: typeof globalThis.fetch;
  redirect?: RequestRedirect;
  hooks?: OpenAPIExecutionHooks;
  maxDeliveryUnitBytes?: number;
  securityHandlers?: Record<string, OpenAPIEngineSecurityHandler>;
  parameterConverter?: OpenAPIParameterConverter;
  /** Disable external `$ref` retrieval for strictly side-effect-free inspection. */
  allowExternalRefs?: boolean;
}

export interface OpenAPIRequirement {
  type: string;
  name?: string;
  durable?: boolean;
  description?: string;
  [key: string]: unknown;
}

export interface OpenAPIRequirementAlternative {
  requirements: OpenAPIRequirement[];
}

/** Artifact-derived prerequisites known during preparation. */
export interface OpenAPIPrerequisites {
  target: string;
  alternatives: OpenAPIRequirementAlternative[];
}

export interface OpenAPIExecutionEvent<T = unknown> {
  value: T;
  /** Per-delivery protocol evidence. Ordinary application logic may ignore it. */
  metadata: Record<string, string[]>;
}

export interface OpenAPIExecutionDiagnostics {
  readonly leading: Promise<Record<string, string[]>>;
  trailing(): Record<string, string[]>;
}

/** SDK-neutral operation execution session. */
export interface OpenAPIExecution<I = unknown, O = unknown> {
  send(input: I): Promise<void>;
  finishInput(): Promise<void>;
  cancel(): Promise<void>;
  readonly events: AsyncIterable<OpenAPIExecutionEvent<O>>;
  readonly completed: Promise<void>;
  readonly inputFinished: Promise<void>;
  readonly diagnostics: OpenAPIExecutionDiagnostics;
}

/** A failure produced by artifact loading, planning, transport, or response handling. */
export class OpenAPIExecutionError extends Error {
  readonly code: string;
  readonly details?: unknown;
  /** Optional protocol-native or implementation evidence. */
  readonly evidence?: unknown;

  constructor(
    code: string,
    message: string,
    options: { cause?: unknown; details?: unknown; evidence?: unknown } = {},
  ) {
    super(message, options.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = "OpenAPIExecutionError";
    this.code = code;
    if (options.details !== undefined) this.details = options.details;
    if (options.evidence !== undefined) this.evidence = options.evidence;
  }
}

export type OpenAPIPortableFailureData =
  | { present: false }
  | { present: true; value: unknown };

/**
 * Returns the application value that the OpenAPI document itself declared for
 * an unsuccessful response. Native response evidence remains available to
 * standalone runtime consumers, but adapters must not promote arbitrary error
 * details across a protocol-agnostic boundary.
 */
export function openAPIPortableFailureData(error: unknown): OpenAPIPortableFailureData {
  // Admitted failure data is always a JSON-domain value, so undefined is an
  // unambiguous absence sentinel. (An own-property check is not: the class
  // field declaration defines `details` as undefined on every instance.)
  if (!(error instanceof OpenAPIExecutionError) || error.details === undefined) {
    return { present: false };
  }
  const evidence = record(error.evidence);
  const declaration = record(evidence?.openapi);
  return declaration?.declared === true && typeof declaration.governingMedia === "string"
    ? { present: true, value: error.details }
    : { present: false };
}

/**
 * A loaded, directly resolved OpenAPI operation. Calling start() first runs
 * every pre-input check, then returns a session waiting for application input.
 */
export class PreparedOpenAPIOperation {
  readonly ref: string;
  readonly profile: OpenAPIExecutionProfile;
  readonly prerequisites: OpenAPIPrerequisites | null;

  private readonly document: OpenAPIDocument;
  private readonly args: PreparedArguments;

  /** @internal */
  constructor(document: OpenAPIDocument, args: PreparedArguments, prerequisites: OpenAPIPrerequisites | null) {
    this.document = document;
    this.args = args;
    this.ref = args.ref;
    this.profile = args.profile;
    this.prerequisites = prerequisites;
  }

  /**
   * Starts execution and resolves only after artifact/configuration preflight
   * succeeds. No application input has been requested or consumed yet.
   */
  async start<I = unknown, O = unknown>(): Promise<OpenAPIExecution<I, O>> {
    const metadata: Metadata[] = [];
    const invocation = new InvocationImpl<unknown, unknown>({ signal: this.args.signal });
    const ready = deferred<void>();
    const hooks = engineHooks(this.args.hooks, this.args.defaultHooks, this.profile);
    const site: InvokeSite = {
      operation: "",
      invokedAs: "",
      bindingKey: "",
      bindingSpec: `openapi/${this.profile.name}`,
      ref: this.ref,
      target: "",
    };

    queueMicrotask(() => {
      runBinding(
        {
          source: {
            profile: this.profile,
            location: this.args.source.location,
            content: this.document,
          },
          ref: this.ref,
          context: this.args.context,
          maxDeliveryUnitBytes: this.args.maxDeliveryUnitBytes,
          signal: this.args.signal,
          fetch: this.args.fetch,
          redirect: this.args.redirect,
          securityHandlers: this.args.securityHandlers as Record<string, ArtifactSecurityHandler> | undefined,
          parameterConverter: this.args.parameterConverter,
          observeOutput: (_value, valueMetadata) => metadata.push(cloneMetadata(valueMetadata)),
          hooks,
          site,
        },
        invocation,
        this.document,
        ready.resolve,
      ).catch((error: unknown) => invocation.fireError(toInternalError(error)));
    });

    try {
      await Promise.race([
        ready.promise,
        invocation.closed.then(() => {
          throw new OpenAPIExecutionError(
            "EXECUTION_COMPLETED_BEFORE_READY",
            "OpenAPI execution completed before its pre-input boundary",
          );
        }),
      ]);
    } catch (error: unknown) {
      throw toExecutionError(error);
    }
    return executionView<I, O>(invocation, metadata);
  }
}

interface PreparedArguments extends Omit<RequiredByKey<OpenAPIPrepareOptions, "profile">, "source"> {
  source: OpenAPIEngineSource;
  defaultHooks?: OpenAPIExecutionHooks;
}

type RequiredByKey<T, K extends keyof T> = T & Required<Pick<T, K>>;

/** SDK-neutral OpenAPI document loading and operation execution engine. */
interface LoadedOpenAPIDocument {
  document: OpenAPIDocument;
  floor: AcceptanceFloor | undefined;
}

export class OpenAPIEngine {
  private readonly cache = new Map<string, LoadedOpenAPIDocument>();
  private readonly options: OpenAPIEngineOptions;

  constructor(options: OpenAPIEngineOptions = {}) {
    this.options = options;
  }

  /** Loads the source and resolves one operation without consuming application input. */
  async prepare(options: OpenAPIPrepareOptions): Promise<PreparedOpenAPIOperation> {
    const args: PreparedArguments = {
      ...options,
      profile: options.profile ?? OPENAPI_PROFILE_FULL,
      context: contextWithSecurityHandlers(options.context, options.securityHandlers),
      fetch: options.fetch ?? this.options.fetch,
      redirect: options.redirect ?? this.options.redirect,
      hooks: options.hooks,
      defaultHooks: this.options.hooks,
      maxDeliveryUnitBytes: options.maxDeliveryUnitBytes ?? this.options.maxDeliveryUnitBytes,
      parameterConverter: options.parameterConverter ?? this.options.parameterConverter,
    };
    let loaded: LoadedOpenAPIDocument;
    try {
      loaded = await this.load(args.source, args.signal, args.fetch, args.allowExternalRefs);
    } catch (error: unknown) {
      throw new OpenAPIExecutionError("SOURCE_LOAD_FAILED", errorMessage(error), { cause: error });
    }
    return this.prepared(loaded, args);
  }

  /**
   * Prepares only from embedded content or a previously loaded location.
   * Returns null instead of retrieving a location, for side-effect-free
   * inspection surfaces.
   */
  async prepareCached(options: OpenAPIPrepareOptions): Promise<PreparedOpenAPIOperation | null> {
    if (options.source.content !== undefined) {
      return this.prepare({ ...options, allowExternalRefs: false });
    }
    const location = options.source.location;
    if (!location) return null;
    const document = this.cache.get(location);
    if (!document) return null;
    const args: PreparedArguments = {
      ...options,
      profile: options.profile ?? OPENAPI_PROFILE_FULL,
      context: contextWithSecurityHandlers(options.context, options.securityHandlers),
      fetch: options.fetch ?? this.options.fetch,
      redirect: options.redirect ?? this.options.redirect,
      hooks: options.hooks,
      defaultHooks: this.options.hooks,
      maxDeliveryUnitBytes: options.maxDeliveryUnitBytes ?? this.options.maxDeliveryUnitBytes,
      parameterConverter: options.parameterConverter ?? this.options.parameterConverter,
    };
    return this.prepared(document, args);
  }

  private prepared(
    loaded: LoadedOpenAPIDocument,
    args: PreparedArguments,
  ): PreparedOpenAPIOperation {
    const document = loaded.document;
    // The family-document acceptance-floor inventory filter: a
    // ladder-invalid target is not addressed, and its invocation is refused
    // before dispatch -- provably no interaction side effect.
    const verdict = floorOpVerdict(loaded.floor, args.ref);
    if (verdict && verdict.disposition === "invalid") {
      throw new OpenAPIExecutionError("ERR_REFUSED", `${floorInvalidTargetMessage(verdict.defects.length)} (${args.ref})`);
    }
    assertOperation(document, args.ref);
    let prerequisites: ContextRequiredDetails | null = null;
    const target = preflightTarget(document, args.ref, args.context, args.source.location);
    if (target) {
      prerequisites = composeRequirements(
        requiredContext(document, target.op, args.context, target.baseURL, target.params),
        requiredRequestMediaContext(document, target.op, args.profile, args.context, target.baseURL),
      );
    }
    return new PreparedOpenAPIOperation(document, args, prerequisites);
  }

  private async load(
    source: OpenAPIEngineSource,
    signal: AbortSignal | undefined,
    fetchFn: typeof globalThis.fetch | undefined,
    allowExternalRefs: boolean | undefined,
  ): Promise<LoadedOpenAPIDocument> {
    let floor: AcceptanceFloor | undefined;
    const floorOptions = {
      onRawDocument: (raw: unknown) => {
        floor = computeAcceptanceFloor(raw);
      },
      tolerateUnresolvableInternalRefs: true,
    };
    const refuseWholeSource = (): void => {
      // §3 part 2's derived whole-source refusal, at load.
      if (floor && floor.refusal) throw new Error(floor.refusal);
    };
    if (source.content !== undefined) {
      const document = await loadOpenAPIDocument(
        source.location,
        source.content,
        { signal, allowExternalRefs, ...floorOptions },
        fetchFn,
      );
      refuseWholeSource();
      const loaded = { document, floor };
      if (source.location) this.cache.set(source.location, loaded);
      return loaded;
    }
    if (!source.location) {
      const document = await loadOpenAPIDocument(undefined, undefined, { signal, allowExternalRefs, ...floorOptions }, fetchFn);
      refuseWholeSource();
      return { document, floor };
    }
    const cached = this.cache.get(source.location);
    if (cached) return cached;
    const document = await loadOpenAPIDocument(source.location, undefined, { signal, allowExternalRefs, ...floorOptions }, fetchFn);
    refuseWholeSource();
    const loaded = { document, floor };
    this.cache.set(source.location, loaded);
    return loaded;
  }
}

/**
 * Custom security satisfaction is engine-owned configuration, not caller
 * invocation context. Strip any caller-authored marker and derive the private
 * compatibility view exclusively from handlers installed for this prepare.
 */
function contextWithSecurityHandlers(
  context: Record<string, unknown> | undefined,
  handlers: Record<string, OpenAPIEngineSecurityHandler> | undefined,
): Record<string, unknown> | undefined {
  const result: Record<string, unknown> = { ...(context ?? {}) };
  delete result["$openapiSecurity"];
  const configured = Object.entries(handlers ?? {})
    .filter(([, handler]) => typeof handler === "function")
    .map(([name]) => name);
  if (configured.length > 0) {
    result["$openapiSecurity"] = Object.fromEntries(configured.map((name) => [name, true]));
  }
  return Object.keys(result).length > 0 ? result : undefined;
}

function assertOperation(document: OpenAPIDocument, ref: string): void {
  let parsed: { path: string; method: string };
  try {
    parsed = parseRef(ref);
  } catch (error: unknown) {
    throw new OpenAPIExecutionError("INVALID_OPERATION_REF", errorMessage(error), { cause: error });
  }
  if (!document.paths) {
    throw new OpenAPIExecutionError("INVALID_DOCUMENT", "OpenAPI document has no paths defined");
  }
  const pathItem = document.paths[parsed.path];
  if (!pathItem || !pathItem[parsed.method]) {
    throw new OpenAPIExecutionError("OPERATION_NOT_FOUND", `operation ${JSON.stringify(ref)} was not found`);
  }
}

function executionView<I, O>(
  invocation: Invocation<unknown, unknown>,
  metadata: Metadata[],
): OpenAPIExecution<I, O> {
  const completed = invocation.closed.catch((error: unknown) => {
    throw toExecutionError(error);
  });
  void completed.catch(() => undefined);
  return {
    send: async (input: I) => {
      try {
        await invocation.write(input);
      } catch (error: unknown) {
        throw toExecutionError(error);
      }
    },
    finishInput: () => invocation.close(),
    cancel: () => invocation.cancel(),
    events: mapEvents<O>(invocation.outputs, metadata),
    completed,
    inputFinished: invocation.inputClosed,
    diagnostics: invocation.diagnostics,
  };
}

async function* mapEvents<O>(
  outputs: AsyncIterable<unknown>,
  metadata: Metadata[],
): AsyncIterable<OpenAPIExecutionEvent<O>> {
  try {
    for await (const value of outputs) {
      yield { value: value as O, metadata: cloneMetadata(metadata.shift() ?? {}) };
    }
  } catch (error: unknown) {
    throw toExecutionError(error);
  }
}

function engineHooks(
  perCall: OpenAPIExecutionHooks | undefined,
  defaults: OpenAPIExecutionHooks | undefined,
  profile: OpenAPIExecutionProfile,
): InvokeHooks | null {
  const slots = (hooks: OpenAPIExecutionHooks | undefined) => ({
    decode: hooks?.decode
      ? async (site: InvokeSite, raw: RawResult) => {
          const value = await callHook(
            () => hooks.decode!(hookSite(site, profile), hookResult(raw)),
          );
          return value === OPENAPI_USE_DEFAULT ? USE_DEFAULT : value;
        }
      : undefined,
    classify: hooks?.classify
      ? async (site: InvokeSite, raw: RawResult) => {
          const value = await callHook(
            () => hooks.classify!(hookSite(site, profile), hookResult(raw)),
          );
          return value === OPENAPI_USE_DEFAULT ? USE_DEFAULT : value;
        }
      : undefined,
  });
  return newInvokeHooks(slots(perCall), slots(defaults));
}

async function callHook<T>(fn: () => T | Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (error: unknown) {
    throw toInternalError(error);
  }
}

function hookSite(site: InvokeSite, profile: OpenAPIExecutionProfile): OpenAPIHookSite {
  return { ref: site.ref, target: site.target, profile: profile.name };
}

function hookResult(raw: RawResult): OpenAPIHookResult {
  return { status: raw.status, body: raw.body, metadata: cloneMetadata(raw.meta) };
}

function toExecutionError(error: unknown): OpenAPIExecutionError {
  if (error instanceof OpenAPIExecutionError) return error;
  if (error instanceof InvocationError) {
    return new OpenAPIExecutionError(error.code, error.message, {
      cause: error,
      ...(Object.hasOwn(error, "details") ? { details: error.details } : {}),
      ...(Object.hasOwn(error, "diagnostics") ? { evidence: error.diagnostics } : {}),
    });
  }
  return new OpenAPIExecutionError("RUNTIME_ERROR", errorMessage(error), { cause: error });
}

function toInternalError(error: unknown): InvocationError {
  if (error instanceof InvocationError) return error;
  if (error instanceof OpenAPIExecutionError) {
    return new InvocationError(
      error.code,
      error.message,
      error.details,
      error.evidence,
      error,
    );
  }
  return new InvocationError("ERR_RUNTIME", errorMessage(error));
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function cloneMetadata(metadata: Record<string, string[]>): Record<string, string[]> {
  return Object.fromEntries(Object.entries(metadata).map(([name, values]) => [name, [...values]]));
}

function composeRequirements(
  left: ContextRequiredDetails | null,
  right: ContextRequiredDetails | null,
): ContextRequiredDetails | null {
  if (left === null) return right;
  if (right === null) return left;
  return {
    target: left.target || right.target,
    alternatives: left.alternatives.flatMap((leftAlternative) =>
      right.alternatives.map((rightAlternative) => ({
        requirements: [...leftAlternative.requirements, ...rightAlternative.requirements],
      })),
    ),
  };
}

function deferred<T>(): {
  promise: Promise<T>;
  resolve(value: T | PromiseLike<T>): void;
} {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

export {
  OPENAPI_PROFILE_BASE,
  OPENAPI_PROFILE_DYNAMIC_OBJECT,
  OPENAPI_PROFILE_FULL,
  OPENAPI_PROFILE_MEDIA,
  OPENAPI_PROFILE_RESPONSE,
  OPENAPI_PROFILE_ROUTED,
  OPENAPI_PROFILE_WHOLE_JSON,
  withInputRouteMarker,
  type OpenAPIExecutionProfile,
} from "./profile.js";
export {
  PreparedSwagger20Operation,
  Swagger20ExecutionError,
  listSwagger20Operations,
  prepareSwagger20,
  validateSwagger20Selector,
  type Swagger20PrepareOptions,
  type Swagger20ContentCodec,
  type Swagger20ContentCodingResult,
} from "./swagger20-engine.js";
export {
  Swagger20Client,
  loadSwagger20,
} from "./swagger20-loader.js";
export {
  Swagger20Document,
  Swagger20Number,
  SWAGGER20_METHODS,
  type Swagger20LoadOptions,
  type Swagger20Method,
  type Swagger20OperationInfo,
  type Swagger20Source,
} from "./swagger20-model.js";
export type {
  Swagger20EmptyValueForm,
  Swagger20Input,
  Swagger20ParameterConverter,
  Swagger20ParameterInfo,
  Swagger20ParameterLocation,
  Swagger20Parameters,
} from "./swagger20-parameters.js";
export type {
  Swagger20BasicCredential,
  Swagger20OAuth2Credential,
  Swagger20SecurityCredentials,
} from "./swagger20-security.js";
