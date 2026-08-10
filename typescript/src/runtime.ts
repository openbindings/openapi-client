import {
  InvocationError,
  InvocationImpl,
  ERR_RUNTIME,
  ERR_SOURCE_LOAD_FAILED,
  type BindingInvocationArgs,
  type ArtifactSecurityHandler,
  type ContextRequiredDetails,
  type Invocation,
  type InvokeHooks,
  type InvokeSite,
} from "./internal/index.js";
import { OPENAPI_PROFILE_FULL, type OpenAPIExecutionProfile } from "./profile.js";
import type { OpenAPIDocument } from "./types.js";
import {
  preflightTarget,
  requiredContext,
  requiredRequestMediaContext,
  runBinding,
} from "./invoke.js";
import { errorMessage, loadOpenAPIDocument } from "./util.js";

/** OpenAPI source accepted by the standalone runtime. */
export interface OpenAPIRuntimeSource {
  /** Artifact-execution capabilities. Defaults to the fullest native profile. */
  profile?: OpenAPIExecutionProfile;
  /** Retrieval base and, when content is absent, the artifact location. */
  location?: string;
  /** Parsed object, JSON text, YAML text, or UTF-8 bytes. */
  content?: unknown;
}

/**
 * Artifact-centric invocation arguments. No OBI document, operation model, or
 * binding-selection machinery is required: the caller identifies the OpenAPI
 * operation directly through its binding reference.
 */
export interface OpenAPIRuntimeInvocationArgs {
  source: OpenAPIRuntimeSource;
  ref: string;
  context?: Record<string, unknown>;
  maxDeliveryUnitBytes?: number;
  signal?: AbortSignal;
  fetch?: typeof globalThis.fetch;
  securityHandlers?: Record<string, ArtifactSecurityHandler>;
  observeOutput?: (value: unknown, metadata: Record<string, string[]>) => void;
  hooks?: InvokeHooks | null;
  site?: InvokeSite;
}

/**
 * Standalone OpenAPI 3.0/3.1 invocation runtime.
 *
 * It owns artifact loading, operation resolution, request planning, HTTP/SSE
 * interaction, response decoding, completion, and diagnostic evidence. The
 * OpenBindings binding package is an adapter over this runtime.
 */
export class OpenAPIRuntime {
  private readonly docCache = new Map<string, OpenAPIDocument>();

  /** Invokes an OpenAPI operation without requiring an OBI document. */
  invoke<I = unknown, O = unknown>(args: OpenAPIRuntimeInvocationArgs): Invocation<I, O> {
    return this.invokeBinding(toBindingArgs(args));
  }

  /**
   * Adapter seam for OpenBindings binding invokers. Kept separate from
   * {@link invoke} so artifact-runtime consumers do not need to construct OBI
   * binding entries or interfaces.
   */
  invokeBinding<I = unknown, O = unknown>(args: BindingInvocationArgs): Invocation<I, O> {
    const inv = new InvocationImpl<unknown, unknown>({ signal: args.signal });
    queueMicrotask(() => {
      this.run(args, inv).catch((err: unknown) => {
        inv.fireError(
          err instanceof InvocationError
            ? err
            : new InvocationError(ERR_RUNTIME, errorMessage(err)),
        );
      });
    });
    return inv as Invocation<I, O>;
  }

  /** Side-effect-free prerequisite inspection for a directly selected operation. */
  async prepare(args: OpenAPIRuntimeInvocationArgs): Promise<ContextRequiredDetails | null> {
    return this.prepareBinding(toBindingArgs(args));
  }

  /** Adapter seam corresponding to the binding-invoker preflight operation. */
  async prepareBinding(args: BindingInvocationArgs): Promise<ContextRequiredDetails | null> {
    let doc: OpenAPIDocument | undefined;
    if (args.source.content !== undefined) {
      try {
        doc = await loadOpenAPIDocument(args.source.location, args.source.content, {
          allowExternalRefs: false,
          signal: args.signal,
        });
      } catch {
        return null;
      }
    } else if (args.source.location) {
      doc = this.docCache.get(args.source.location);
    }
    if (!doc) return null;

    const target = preflightTarget(doc, args.ref, args.context, args.source.location);
    if (!target) return null;
    const auth = requiredContext(doc, target.op, args.context, target.baseURL, target.params);
    const requestMedia = requiredRequestMediaContext(
      doc,
      target.op,
      args.source.profile,
      args.context,
      target.baseURL,
    );
    return composeContextRequirements(auth, requestMedia);
  }

  private async run(
    args: BindingInvocationArgs,
    inv: InvocationImpl<unknown, unknown>,
  ): Promise<void> {
    let doc: OpenAPIDocument;
    try {
      doc = await this.loadDocument(args);
    } catch (err: unknown) {
      inv.fireError(new InvocationError(ERR_SOURCE_LOAD_FAILED, errorMessage(err)));
      return;
    }
    await runBinding(args, inv, doc);
  }

  private async loadDocument(args: BindingInvocationArgs): Promise<OpenAPIDocument> {
    const { location, content } = args.source;
    if (content !== undefined) {
      const doc = await loadOpenAPIDocument(
        location,
        content,
        { signal: args.signal },
        args.fetch,
      );
      if (location) this.docCache.set(location, doc);
      return doc;
    }
    if (!location) {
      return loadOpenAPIDocument(location, content, { signal: args.signal }, args.fetch);
    }
    const cached = this.docCache.get(location);
    if (cached) return cached;
    const doc = await loadOpenAPIDocument(location, undefined, { signal: args.signal }, args.fetch);
    this.docCache.set(location, doc);
    return doc;
  }
}

function toBindingArgs(args: OpenAPIRuntimeInvocationArgs): BindingInvocationArgs {
  return {
    source: {
      profile: args.source.profile ?? OPENAPI_PROFILE_FULL,
      location: args.source.location,
      content: args.source.content,
    },
    ref: args.ref,
    context: args.context,
    maxDeliveryUnitBytes: args.maxDeliveryUnitBytes,
    signal: args.signal,
    fetch: args.fetch,
    securityHandlers: args.securityHandlers,
    observeOutput: args.observeOutput,
    hooks: args.hooks,
    site: args.site,
  };
}

function composeContextRequirements(
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
