/**
 * Advanced, detached OpenAPI-native analysis for generators and protocol
 * adapters. Application invocation remains on the package root.
 */
export {
  OpenAPIClient,
  type OpenAPIAnalysis,
  type OpenAPIOperationAnalysis,
  type OpenAPIParameterInfo,
  type OpenAPIRequestBodyAnalysis,
  type OpenAPIResponseAlternativeAnalysis,
  type OpenAPISecurityAlternativeAnalysis,
  type OpenAPIServerAlternativeAnalysis,
  type OpenAPISource,
  type OpenAPISupportDisposition,
} from "./client.js";
export type * from "./types.js";
export type { OpenAPIParameterConverter } from "./params.js";
export {
  PROPERTY_MEDIA_REQUIREMENT_DESCRIPTION,
  REQUEST_MEDIA_REQUIREMENT_DESCRIPTION,
} from "./invoke.js";
export { Swagger20Number } from "./swagger20-model.js";
export type {
  Swagger20SynthesisAlternative,
  Swagger20SynthesisDocument,
  Swagger20SynthesisOperation,
} from "./swagger20-synthesis.js";
export {
  codePointCompare,
  errorMessage,
  sanitizeKey,
  uniqueKey,
  validateDocumentAddress,
} from "./util.js";

import {
  OpenAPIClient,
  type OpenAPIAnalysis,
  type OpenAPIClientOptions,
  type OpenAPISource,
  materializeOpenAPISource,
  normalizeOpenAPISource,
  openAPISourceFamily,
} from "./client.js";
import { loadSwagger20 } from "./swagger20-loader.js";
import { parseJSONOrYAML } from "./util.js";
import {
  OpenAPISourceExcludedError,
  projectOpenAPIDocument,
  type ProjectionAnalysis,
  type ProjectionFailure,
  type ProjectionWarning,
  type UnrealizableTarget,
} from "./projection.js";
import { openAPISynthesisCoverage } from "./projection-coverage.js";
import type { AcceptanceFloor } from "./acceptance-floor.js";
import type { OpenAPIDocument } from "./types.js";
import type { OpenAPI32ResponseMediaExclusion } from "./openapi32-operations.js";

/** Loads one artifact and returns its detached declaration analysis. */
export async function analyzeOpenAPI(
  source: OpenAPISource,
  options: OpenAPIClientOptions = {},
): Promise<OpenAPIAnalysis> {
  return (await OpenAPIClient.load(source, options)).analysis();
}

export type {
  ProjectionAnalysis,
  ProjectionBinding,
  ProjectionCoverageEntry,
  ProjectionDependency,
  ProjectionDocument,
  ProjectionFailure,
  ProjectionInputCorrespondence,
  ProjectionOperation,
  ProjectionParameterRoute,
  ProjectionSchema,
  ProjectionWarning,
} from "./projection.js";

/**
 * Builds a detached, deeply immutable generator projection from one retrieved
 * source. The value contains no parser graph, execution engine, OpenBindings
 * SDK type, OBI node, or transform-language expression.
 */
export async function analyzeOpenAPIProjection(
  source: OpenAPISource,
  options: OpenAPIClientOptions = {},
): Promise<Readonly<ProjectionAnalysis>> {
  const materialized = await materializeOpenAPISource(normalizeOpenAPISource(source), options);
  const family = openAPISourceFamily(materialized.content);
  if (family === "2.0") {
    const loaded = await loadSwagger20(
      { location: materialized.location, content: materialized.content },
      { signal: options.documentSignal, fetch: options.documentFetch },
    );
    return immutableProjection({
      edition: "2.0",
      ...(materialized.location ? { location: materialized.location } : {}),
      sourceRef: "#",
      sourceKind: "swagger",
      swagger20: await loaded.synthesisModel(),
      coverage: [],
      failures: [],
      warnings: [],
    });
  }

  const bindingSpec = projectionBindingSpec(materialized.content);
  const unrealizable = new Map<string, UnrealizableTarget>();
  const warnings: ProjectionWarning[] = [];
  let document: OpenAPIDocument | undefined;
  let floor: AcceptanceFloor | undefined;
  let responseMediaExclusions: ReadonlyMap<string, readonly OpenAPI32ResponseMediaExclusion[]> | undefined;
  let inboundDependencies: readonly import("./projection.js").InboundDependencyDisposition[] | undefined;
  let projected: import("./projection.js").ProjectionDocument | undefined;
  try {
    projected = await projectOpenAPIDocument(
      materialized.location,
      materialized.content,
      { signal: options.documentSignal, fetch: options.documentFetch },
      (warning) => warnings.push(warning),
      (value) => { document = value; },
      (target) => unrealizable.set(target.selector, target),
      bindingSpec,
      (value) => { floor = value; },
      (value) => { responseMediaExclusions = value; },
      (value) => { inboundDependencies = value; },
    );
  } catch (error: unknown) {
    if (!(error instanceof OpenAPISourceExcludedError)) throw error;
    return immutableProjection({
      edition: projectionEditionForBindingSpec(bindingSpec),
      ...(materialized.location ? { location: materialized.location } : {}),
      sourceRef: "#",
      sourceKind: "openapi",
      coverage: [],
      failures: [{
        sourceRef: "#",
        reasonCode: "openapi.source_excluded",
        status: "excluded",
        rule: error.rule,
        message: error.message,
      }],
      warnings,
    });
  }

  const failures: ProjectionFailure[] = [...unrealizable.values()]
    .sort((left, right) => left.selector.localeCompare(right.selector))
    .map((target) => ({
      sourceRef: target.selector,
      operationKey: target.operationKey,
      reasonCode: target.reasonCode,
      status: target.status ?? "excluded",
      ...(target.rule ? { rule: target.rule } : {}),
      message: target.message,
    }));
  const coverage = openAPISynthesisCoverage(
    document,
    projected,
    bindingSpec,
    materialized.location ?? "",
    unrealizable,
    floor,
    responseMediaExclusions,
    inboundDependencies,
  );
  return immutableProjection({
    edition: document?.openapi ?? projectionEditionForBindingSpec(bindingSpec),
    ...(materialized.location ? { location: materialized.location } : {}),
    sourceRef: "#",
    sourceKind: "openapi",
    openapi3: projected,
    coverage,
    failures,
    warnings,
  });
}

function projectionBindingSpec(content: unknown): string {
  let value = content;
  if (content instanceof Uint8Array) value = parseJSONOrYAML(new TextDecoder("utf-8", { fatal: true }).decode(content));
  else if (typeof content === "string") value = parseJSONOrYAML(content);
  const root = value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
  const edition = typeof root.openapi === "string" ? root.openapi : "";
  if (/^3\.0\.[0-4]$/u.test(edition)) return "openbindings.openapi-3.0@1";
  if (/^3\.1\.[0-2]$/u.test(edition)) return "openbindings.openapi-3.1@1";
  if (edition === "3.2.0") return "openbindings.openapi-3.2@1";
  throw new Error(`unsupported OpenAPI edition ${JSON.stringify(edition)}`);
}

function projectionEditionForBindingSpec(bindingSpec: string): string {
  if (bindingSpec.includes("3.0")) return "3.0.0";
  if (bindingSpec.includes("3.1")) return "3.1.0";
  return "3.2.0";
}

function immutableProjection<T>(value: T): Readonly<T> {
  return deepFreezeProjection(structuredClone(value));
}

function deepFreezeProjection<T>(value: T): T {
  if (value !== null && typeof value === "object" && !Object.isFrozen(value)) {
    Object.freeze(value);
    for (const member of Object.values(value)) deepFreezeProjection(member);
  }
  return value;
}
