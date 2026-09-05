import type { InvokeHooks, InvokeSite } from "./hooks.js";
import type { OpenAPIExecutionProfile } from "../profile.js";
import type { OpenAPIParameterConverter } from "../params.js";
import type { OpenAPIHostTransport, OpenAPIPlannedRequest } from "../host-transport.js";

export interface InvocationSource {
  profile: OpenAPIExecutionProfile;
  location?: string;
  content?: unknown;
}

export interface ArtifactSecurityHandlerContext {
  schemeName: string;
  scheme: unknown;
}

export type ArtifactSecurityHandler = (
  request: OpenAPIPlannedRequest,
  context: ArtifactSecurityHandlerContext,
) => OpenAPIPlannedRequest | void | Promise<OpenAPIPlannedRequest | void>;

export interface BindingInvocationArgs {
  source: InvocationSource;
  ref: string;
  context?: Record<string, unknown>;
  maxDeliveryUnitBytes?: number;
  signal?: AbortSignal;
  fetch?: typeof globalThis.fetch;
  /**
   * Transport for the methods the platform `fetch` cannot carry (see
   * `host-transport.ts`). `undefined` resolves the host's own HTTP client
   * lazily, but only when `fetch` is the platform default; `null` declares
   * that none exists and a function supplies one, and either is consulted
   * whether or not a `fetch` was injected.
   */
  hostTransport?: OpenAPIHostTransport | null;
  /** Fetch redirect mode. Artifact engines default to `manual` to preserve the bound exchange. */
  redirect?: RequestRedirect;
  securityHandlers?: Record<string, ArtifactSecurityHandler>;
  parameterConverter?: OpenAPIParameterConverter;
  observeOutput?: (value: unknown, metadata: Record<string, string[]>) => void;
  hooks?: InvokeHooks | null;
  site?: InvokeSite;
}

export const DEFAULT_MAX_DELIVERY_UNIT_BYTES = 10 * 1024 * 1024;

export function resolveDeliveryUnitLimit(
  args: Pick<BindingInvocationArgs, "maxDeliveryUnitBytes">,
): number {
  const value = args.maxDeliveryUnitBytes;
  return value !== undefined && Number.isFinite(value) && value > 0
    ? value
    : DEFAULT_MAX_DELIVERY_UNIT_BYTES;
}
