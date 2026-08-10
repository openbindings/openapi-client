import type { InvokeHooks, InvokeSite } from "./hooks.js";
import type { OpenAPIExecutionProfile } from "../profile.js";

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
  request: Request,
  context: ArtifactSecurityHandlerContext,
) => Request | void | Promise<Request | void>;

export interface BindingInvocationArgs {
  source: InvocationSource;
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

export const DEFAULT_MAX_DELIVERY_UNIT_BYTES = 10 * 1024 * 1024;

export function resolveDeliveryUnitLimit(
  args: Pick<BindingInvocationArgs, "maxDeliveryUnitBytes">,
): number {
  const value = args.maxDeliveryUnitBytes;
  return value !== undefined && Number.isFinite(value) && value > 0
    ? value
    : DEFAULT_MAX_DELIVERY_UNIT_BYTES;
}
