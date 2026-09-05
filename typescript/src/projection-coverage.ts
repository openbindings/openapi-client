import type {
  OpenAPIDocument,
  OpenAPIOperation,
  OpenAPIPathItem,
} from "./types.js";
import {
  candidateCollides,
  normalizedMediaCollisions,
} from "./media.js";
import {
  planResolvedRequestBodies as planRequestBodies,
  requiredPropertyMediaNames,
} from "./resolved-media.js";
import { effectiveParameters } from "./params.js";
import {
  HTTP_METHODS,
  effectiveRequestBodySynthesisRule,
  requestMediaBindingExclusions,
  synthesisOperationEntries,
  type InboundDependencyDisposition,
  type UnrealizableTarget,
  type ProjectionCoverageEntry,
  type ProjectionDocument,
} from "./projection.js";
import { effectiveParameterDeclarationRows } from "./params.js";
import {
  buildJsonPointerRef as buildJsonPointerSelector,
  codePointCompare,
} from "./util.js";
import {
  ConfigRequired,
  resolveServer,
  type OpenAPIServerEntry as ServerEntry,
} from "./servers.js";
import {
  effectiveSecurityRequirements,
  securityAlternativeDisposition,
  securityCoverageRequirements,
} from "./projection-security.js";
import {
  INVALID_UNIT_REASON_CODE,
  floorDefectDetails,
  floorInvalidAlternativeMessage,
  floorInvalidTargetMessage,
  floorOpVerdict,
  floorProjectionMessage,
  type AcceptanceFloor,
  type FloorOp,
} from "./acceptance-floor.js";
import type { OpenAPI32ResponseMediaExclusion } from "./openapi32-operations.js";
import {
  hasMediaFidelity,
  hasRoutedInputs,
  isImplementedOpenAPIBindingSpec,
  openAPIRule,
  profileForBindingSpec,
} from "./projection-constants.js";

/**
 * Inventories path operations, request-media alternatives, callbacks, and
 * webhooks observed during OpenAPI synthesis. Parameter serialization,
 * response selection, server resolution, and security requirements are
 * incorporated behavior of their represented target rather than separate
 * interaction units.
 */
export function openAPISynthesisCoverage(
  doc: OpenAPIDocument | undefined,
  iface: ProjectionDocument,
  bindingSpec: string,
  sourceLocation: string,
  unrealizable?: ReadonlyMap<string, UnrealizableTarget>,
  floor?: AcceptanceFloor,
  responseMediaExclusions?: ReadonlyMap<string, readonly OpenAPI32ResponseMediaExclusion[]>,
  inboundDependencies?: readonly InboundDependencyDisposition[],
): ProjectionCoverageEntry[] {
  if (!doc) return [];
  const bySelector = new Map<string, { operationKey: string; selector: string }>();
  for (const binding of Object.values(iface.bindings ?? {})) {
    if (binding.selector) bySelector.set(binding.selector, { operationKey: binding.operation, selector: binding.selector });
  }
  if (!isImplementedOpenAPIBindingSpec(bindingSpec)) return [];

  // The walk is driven from the UNION of the loaded document's path×method
  // inventory and the acceptance floor's raw-tree inventory (block 8d design
  // §3): a ladder-invalid operation may be absent from the loaded document
  // (confined) or present (a loadable defect); either way its invalid entry
  // is owed. Deterministic order: sorted paths × the HTTP_METHODS order.
  const pathSet = new Set<string>();
  for (const path of Object.keys(doc.paths ?? {})) pathSet.add(path);
  if (floor) for (const selector of floor.opOrder) pathSet.add(floor.ops.get(selector)!.path);

  const rows = doc.openapi === "3.2.0"
    ? synthesisOperationEntries(doc).map(({ pathStr, pathItem, method, opObj, selector }) => ({
        path: pathStr,
        pathItem,
        method,
        loadedOperation: opObj,
        selector,
      }))
    : [...pathSet].sort(codePointCompare).flatMap((path) => {
        const rawPathItem = doc.paths?.[path];
        const pathItem: OpenAPIPathItem | undefined = rawPathItem && typeof rawPathItem === "object"
          ? rawPathItem
          : undefined;
        return HTTP_METHODS.map((method) => {
      const rawOperation = pathItem?.[method];
      const loadedOperation = rawOperation && typeof rawOperation === "object" ? (rawOperation as OpenAPIOperation) : undefined;
          return { path, pathItem, method, loadedOperation, selector: buildJsonPointerSelector(path, method) };
        });
      });

  const entries: ProjectionCoverageEntry[] = [];
  for (const { pathItem, loadedOperation, selector, method } of rows) {
      const verdict = floorOpVerdict(floor, selector);
      if (!loadedOperation && !verdict) continue;
      if (verdict && verdict.disposition === "invalid") {
        // A ladder-invalid target: one invalid target entry carrying the
        // owning unit and its defects, then the operation's projection
        // entries.
        entries.push({
          sourceIndex: 0,
          sourceRef: selector,
          scope: "target",
          status: "invalid",
          reasonCode: INVALID_UNIT_REASON_CODE,
          message: floorInvalidTargetMessage(verdict.defects.length),
          details: { defects: floorDefectDetails(verdict.defects) },
        });
        entries.push(...floorProjectionEntries(verdict));
        continue;
      }
      if (!loadedOperation) {
        // Raw-inventory-only and not ladder-invalid: nothing the loaded
        // document can account further.
        continue;
      }
      const operation = loadedOperation;
      const identity = bySelector.get(selector);
      if (!identity) {
        // Tolerant synthesis skipped this operation with a recorded,
        // spec-governed reason: a per-operation exclusion, not an
        // implementation defect. Anything else genuinely missing remains
        // an implementation invariant violation.
        const skipped = unrealizable?.get(selector);
        if (skipped) {
          entries.push({
            sourceIndex: 0,
            sourceRef: selector,
            scope: "target",
            status: skipped.status ?? "excluded",
            reasonCode: skipped.reasonCode,
            ...(skipped.rule ? { rule: skipped.rule } : {}),
            message: skipped.message,
          });
          // An excluded target is still ADDRESSED; its ladder-invalid
          // request media alternatives and projection entries are owed
          // regardless.
          entries.push(...floorInvalidAlternativeEntries(verdict));
          entries.push(...floorProjectionEntries(verdict));
        } else {
          entries.push({
            sourceIndex: 0,
            sourceRef: selector,
            scope: "target",
            status: "implementation-unsupported",
            reasonCode: "openapi.missing_emitted_binding",
            message: "the synthesizer returned without emitting this admitted paths operation",
          });
        }
        continue;
      }
      entries.push({
        sourceIndex: 0,
        sourceRef: selector,
        scope: "target",
        status: "represented",
        operationKey: identity.operationKey,
        bindingSelector: identity.selector,
        requirements: [
          ...serverRequirements(doc, pathItem!, operation, sourceLocation),
          ...securityCoverageRequirements(
            doc,
            operation,
            effectiveParameters(pathItem!, operation),
            bindingSpec,
            method,
          ),
          ...requestMediaTargetRequirements(operation, pathItem!, bindingSpec, doc.openapi),
        ],
      });
      entries.push(...serverAlternativeCoverage(
        doc,
        pathItem!,
        operation,
        selector,
        identity.operationKey,
        bindingSpec,
        sourceLocation,
      ));
      entries.push(...securityAlternativeCoverage(
        doc,
        operation,
        effectiveParameters(pathItem!, operation),
        selector,
        identity.operationKey,
        bindingSpec,
        method,
      ));
      entries.push(...requestMediaCoverage(operation, pathItem!, identity, bindingSpec, doc.openapi, verdict));
      entries.push(...parameterConfinementCoverage(pathItem!, operation, selector, bindingSpec));
      const responseEntries = responseConfinementCoverage(operation, identity, bindingSpec);
      entries.push(...responseEntries);
      for (const exclusion of responseMediaExclusions?.get(selector) ?? []) {
        entries.push({
          sourceIndex: 0,
          sourceRef: `${selector}/responses/${escapePointerToken(exclusion.responseKey)}/content/${escapePointerToken(exclusion.mediaType)}`,
          scope: "alternative",
          status: "invalid",
          reasonCode: "openapi.response_media_excluded",
          rule: openAPIRule(bindingSpec, "P-01"),
          message: exclusion.reason,
        });
      }
      entries.push(...floorProjectionEntries(verdict).filter((entry) =>
        !(entry.sourceRef === selector && responseEntries.some((candidate) => candidate.scope === "projection"))));
  }
  for (const disposition of inboundDependencies ?? []) {
    entries.push(disposition.represented
      ? {
          sourceIndex: 0,
          sourceRef: disposition.sourceRef,
          scope: "dependency",
          status: "represented",
          requirements: [],
        }
      : {
          sourceIndex: 0,
          sourceRef: disposition.sourceRef,
          scope: "dependency",
          status: "excluded",
          reasonCode: "openapi.inbound_dependency_excluded",
          rule: openAPIRule(bindingSpec, "P-03"),
          message: disposition.message ?? "inbound dependency cannot be projected faithfully",
        });
  }
  return entries;
}

const HTTP_FIELD_NAME = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/u;

function parameterConfinementCoverage(
  pathItem: OpenAPIPathItem,
  operation: OpenAPIOperation,
  selector: string,
  bindingSpec: string,
): ProjectionCoverageEntry[] {
  const result: ProjectionCoverageEntry[] = [];
  const rows = effectiveParameterDeclarationRows(pathItem, operation);
  const direct = Array.isArray(operation.parameters) ? operation.parameters : [];
  for (const parameter of rows) {
    const name = typeof parameter.name === "string" ? parameter.name : "";
    const invalidHeader = parameter.in === "header" && !HTTP_FIELD_NAME.test(name);
    const invalidCookie = parameter.in === "cookie" && !HTTP_FIELD_NAME.test(name);
    if ((!invalidHeader && !invalidCookie) || parameter.required === true) continue;
    let sourceRef = selector;
    const directIndex = direct.indexOf(parameter);
    if (directIndex >= 0) sourceRef += `/parameters/${directIndex}`;
    else {
      const inherited = Array.isArray(pathItem.parameters) ? pathItem.parameters : [];
      const index = inherited.indexOf(parameter);
      sourceRef = `${selector.slice(0, selector.lastIndexOf("/"))}/parameters/${Math.max(0, index)}`;
    }
    const sameNameSurvivor = rows.some((candidate) => candidate !== parameter
      && candidate.name === parameter.name
      && !(candidate.in === "header" && !HTTP_FIELD_NAME.test(candidate.name ?? "")));
    const rule = invalidCookie
      ? "S-04"
      : sameNameSurvivor
        ? bindingSpec.endsWith("3.0@1") ? "S-18" : "S-17"
        : bindingSpec.endsWith("3.0@1") ? "P-14" : "P-15";
    result.push({
      sourceIndex: 0,
      sourceRef,
      scope: "projection",
      status: "excluded",
      reasonCode: "openapi.parameter_projection_excluded",
      rule: openAPIRule(bindingSpec, rule),
      message: `${parameter.in} parameter name ${JSON.stringify(name)} cannot be emitted safely`,
    });
  }
  return result;
}

function responseConfinementCoverage(
  operation: OpenAPIOperation,
  identity: { operationKey: string; selector: string },
  bindingSpec: string,
): ProjectionCoverageEntry[] {
  const responses = asRecord(operation.responses);
  if (!responses) return [];
  const result: ProjectionCoverageEntry[] = [];
  const caseGroupedKeys = new Set<string>();
  for (const response of Object.values(responses)) {
    const headers = asRecord(asRecord(response)?.headers);
    if (!headers) continue;
    const folds = new Map<string, number>();
    for (const name of Object.keys(headers)) folds.set(name.toLowerCase(), (folds.get(name.toLowerCase()) ?? 0) + 1);
    for (const [name, count] of folds) if (count > 1) caseGroupedKeys.add(name);
  }
  for (const [responseKey, rawResponse] of Object.entries(responses)) {
    if (responseKey.startsWith("x-")) continue;
    const response = asRecord(rawResponse);
    const responseRef = `${identity.selector}/responses/${escapeJSONPointerToken(responseKey)}`;
    if (!response || (!Object.hasOwn(response, "description") && !/^2(?:[0-9]{2}|XX)$/u.test(responseKey))) {
      result.push({
        sourceIndex: 0,
        sourceRef: responseRef,
        scope: "projection",
        status: "invalid",
        reasonCode: "openapi.response_projection_invalid",
        rule: openAPIRule(bindingSpec, bindingSpec.endsWith("3.0@1") ? "P-10" : "P-11"),
        message: "failure response projection is malformed and contributes no failure data",
      });
    }
    if (!response) continue;
    if (["204", "205", "304"].includes(responseKey) && Object.hasOwn(response, "content")) {
      result.push({
        sourceIndex: 0,
        sourceRef: `${responseRef}/content`,
        scope: "projection",
        status: "excluded",
        reasonCode: "openapi.no_content_projection_excluded",
        rule: openAPIRule(bindingSpec, "S-03"),
        message: `${responseKey} never projects response content`,
      });
    }
    const headers = asRecord(response.headers);
    let responseExcluded = false;
    if (headers) {
      for (const [name, rawHeader] of Object.entries(headers)) {
        if (HTTP_FIELD_NAME.test(name)) continue;
        const header = asRecord(rawHeader);
        if (header?.required === true) responseExcluded = true;
        else {
          result.push({
            sourceIndex: 0,
            sourceRef: `${responseRef}/headers/${escapeJSONPointerToken(name)}`,
            scope: "projection",
            status: "excluded",
            reasonCode: "openapi.response_header_projection_excluded",
            rule: openAPIRule(bindingSpec, "S-04"),
            message: `response header name ${JSON.stringify(name)} is not an HTTP field-name`,
          });
        }
      }
      if (requiredContentEncodingGroupUnsatisfiable(headers)) responseExcluded = true;
    }
    if (responseExcluded) {
      result.push({
        sourceIndex: 0,
        sourceRef: responseRef,
        scope: "alternative",
        status: "excluded",
        reasonCode: "openapi.response_alternative_excluded",
        rule: openAPIRule(bindingSpec, requiredContentEncodingGroupUnsatisfiable(headers ?? {})
          ? bindingSpec.endsWith("3.0@1") ? "P-30" : "P-31"
          : "S-04"),
        message: "response alternative has an unavoidable wire-level header conflict",
      });
    } else if (caseGroupedKeys.size > 0) {
      result.push({
        sourceIndex: 0,
        sourceRef: responseRef,
        scope: "alternative",
        status: "represented",
        operationKey: identity.operationKey,
        bindingSelector: identity.selector,
      });
    }
    const content = asRecord(response.content);
    if (content) {
      const qEntries = Object.keys(content).filter((media) => /(?:^|;)\s*q\s*=/iu.test(media));
      if (qEntries.length > 0) {
        for (const media of Object.keys(content)) {
          const bad = /(?:^|;)\s*q\s*=/iu.test(media);
          result.push({
            sourceIndex: 0,
            sourceRef: `${responseRef}/content/${escapeJSONPointerToken(media)}`,
            scope: "alternative",
            status: bad ? "excluded" : "represented",
            ...(bad ? {
              reasonCode: "openapi.response_media_q_excluded",
              rule: openAPIRule(bindingSpec, bindingSpec.endsWith("3.0@1") ? "S-20" : "S-19"),
              message: "response media declaration contains a reserved q parameter",
            } : {
              operationKey: identity.operationKey,
              bindingSelector: identity.selector,
            }),
          });
        }
      }
    }
  }
  if (Object.keys(responses).length === 1 && Object.hasOwn(responses, "default")) {
    const schema = asRecord(asRecord(asRecord(asRecord(responses.default)?.content)?.["application/json"])?.schema);
    if (schema) {
      result.push({
        sourceIndex: 0,
        sourceRef: `${identity.selector}/responses/default/content/application~1json/schema`,
        scope: "projection",
        status: "represented",
        operationKey: identity.operationKey,
        bindingSelector: identity.selector,
      });
    }
  }
  return result;
}

function requiredContentEncodingGroupUnsatisfiable(headers: Record<string, unknown>): boolean {
  const group = Object.entries(headers).filter(([name]) => name.toLowerCase() === "content-encoding");
  if (group.length < 2 || !group.some(([, raw]) => asRecord(raw)?.required === true)) return false;
  let intersection: Set<string> | undefined;
  for (const [, raw] of group) {
    const schema = asRecord(asRecord(raw)?.schema);
    const values = typeof schema?.const === "string"
      ? [schema.const]
      : Array.isArray(schema?.enum) && schema.enum.every((value) => typeof value === "string")
        ? schema.enum
        : undefined;
    if (!values) continue;
    intersection = intersection === undefined
      ? new Set(values)
      : new Set(values.filter((value) => intersection!.has(value)));
  }
  return intersection?.size === 0;
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

/** Renders a ladder-invalid or excluded operation's invalid request media alternatives. */
function floorInvalidAlternativeEntries(verdict: FloorOp | undefined): ProjectionCoverageEntry[] {
  if (!verdict || verdict.altOrder.length === 0) return [];
  return verdict.altOrder.map((altSelector): ProjectionCoverageEntry => {
    const defects = verdict.invalidAlternatives.get(altSelector) ?? [];
    return {
      sourceIndex: 0,
      sourceRef: altSelector,
      scope: "alternative",
      status: "invalid",
      reasonCode: INVALID_UNIT_REASON_CODE,
      message: floorInvalidAlternativeMessage(defects.length),
      details: {
        defects: floorDefectDetails(defects),
        mediaType: unescapeJSONPointerToken(altSelector.slice(altSelector.lastIndexOf("/") + 1)),
      },
    };
  });
}

/**
 * Renders one projection-scope entry per unit whose emitted closure reaches,
 * or whose response rungs record, invalid positions that cost it nothing.
 */
function floorProjectionEntries(verdict: FloorOp | undefined): ProjectionCoverageEntry[] {
  if (!verdict || verdict.projOrder.length === 0) return [];
  return verdict.projOrder.map((unit): ProjectionCoverageEntry => {
    const defects = verdict.projections.get(unit) ?? [];
    return {
      sourceIndex: 0,
      sourceRef: unit,
      scope: "projection",
      status: "invalid",
      reasonCode: INVALID_UNIT_REASON_CODE,
      message: floorProjectionMessage(defects.length),
      details: { defects: floorDefectDetails(defects) },
    };
  });
}

function unescapeJSONPointerToken(value: string): string {
  return value.replaceAll("~1", "/").replaceAll("~0", "~");
}

function escapePointerToken(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

function requestMediaTargetRequirements(
  operation: OpenAPIOperation,
  pathItem: OpenAPIPathItem,
  bindingSpec: string,
  openapiVersion: string | undefined,
): string[] {
  if (!hasMediaFidelity(bindingSpec) || operation.requestBody?.required !== true) return [];
  try {
    const params = effectiveParameters(pathItem, operation);
    const admissible = planRequestBodies(operation, { profile: profileForBindingSpec(bindingSpec), openapiVersion })
      .filter((plan) => hasRoutedInputs(bindingSpec) || !candidateCollides(params, plan));
    if (admissible.length === 1 && requiredPropertyMediaNames(admissible[0]!).length > 0) {
      return ["configuration.propertyMedia"];
    }
    return admissible.some((plan) => !plan.range && !plan.unsupported)
      ? []
      : admissible.some((plan) => plan.range && !plan.unsupported)
        ? ["configuration.requestMedia"]
        : [];
  } catch {
    return [];
  }
}

function serverRequirements(
  doc: OpenAPIDocument,
  pathItem: OpenAPIPathItem,
  operation: OpenAPIOperation,
  sourceLocation: string,
): string[] {
  try {
    resolveServer(doc, pathItem, operation, undefined, sourceLocation);
    return [];
  } catch (error: unknown) {
    return error instanceof ConfigRequired ? ["configuration.server"] : [];
  }
}

function serverAlternativeCoverage(
  document: OpenAPIDocument,
  pathItem: OpenAPIPathItem,
  operation: OpenAPIOperation,
  selector: string,
  operationKey: string,
  bindingSpec: string,
  _sourceLocation: string,
): ProjectionCoverageEntry[] {
  const declaration = effectiveServerDeclaration(document, pathItem, operation, selector);
  if (!declaration) return [];
  const entries: ProjectionCoverageEntry[] = [];
  const classifications = declaration.servers.map((server) =>
    classifyServerAlternative(server, document.openapi ?? ""));
  const hasSurvivingAlternative = classifications.some((classification) => classification === null);
  for (const [index] of declaration.servers.entries()) {
    const classification = classifications[index];
    if (classification) {
      entries.push({
        sourceIndex: 0,
        sourceRef: `${declaration.prefix}/${index}`,
        scope: "alternative",
        status: classification.status,
        reasonCode: `openapi.server_${classification.status}`,
        ...(classification.rule ? { rule: openAPIRule(bindingSpec, classification.rule) } : {}),
        message: classification.message,
        ...(classification.status === "invalid" && hasSurvivingAlternative
          ? { operationKey, bindingSelector: selector }
          : {}),
      });
      continue;
    }
    // Non-HTTP, relative-without-base, and otherwise incomplete alternatives
    // remain represented recovery paths. They create configuration.server on
    // the target but are not themselves coverage exclusions.
  }
  return entries;
}

function classifyServerAlternative(
  server: ServerEntry,
  version: string,
): { status: "invalid" | "excluded"; rule?: string; message: string } | null {
  const raw = server as Record<string, unknown>;
  if (typeof raw.url !== "string") {
    return { status: "invalid", rule: version.startsWith("3.0") ? "P-46" : version.startsWith("3.1") ? "P-47" : "P-51", message: "Server Object has no string url" };
  }
  const names = [...raw.url.matchAll(/\{([^{}]+)\}/gu)].map((match) => match[1]!);
  const variables = raw.variables !== null && typeof raw.variables === "object" && !Array.isArray(raw.variables)
    ? raw.variables as Record<string, unknown>
    : {};
  const variableRule = version.startsWith("3.0") ? "P-46" : version.startsWith("3.1") ? "P-47" : "P-51";
  for (const name of names) {
    const variable = variables[name];
    if (variable === null || typeof variable !== "object" || Array.isArray(variable)) return null;
    const declaration = variable as Record<string, unknown>;
    if (typeof declaration.default !== "string") {
      return { status: "invalid", rule: variableRule, message: `Server variable ${JSON.stringify(name)} has no string default` };
    }
    if (Array.isArray(declaration.enum)) {
      if (declaration.enum.length === 0 || !declaration.enum.every((value) => typeof value === "string")) {
        return {
          status: version.startsWith("3.0") ? "excluded" : "invalid",
          rule: version.startsWith("3.0") ? "P-47" : variableRule,
          message: `Server variable ${JSON.stringify(name)} has no admitted enum domain`,
        };
      }
      if (!declaration.enum.includes(declaration.default)) {
        return {
          status: version.startsWith("3.0") ? "excluded" : "invalid",
          rule: version.startsWith("3.0") ? "P-47" : variableRule,
          message: `Server variable ${JSON.stringify(name)} default is outside its enum`,
        };
      }
    }
  }
  try {
    const expanded = raw.url.replace(/\{([^{}]+)\}/gu, (_whole, name: string) => {
      const value = variables[name];
      return value && typeof value === "object" && !Array.isArray(value)
        && typeof (value as Record<string, unknown>).default === "string"
        ? (value as Record<string, unknown>).default as string
        : `{${name}}`;
    });
    const url = new URL(expanded);
    if (url.username || url.password || !url.hostname) {
      return { status: "excluded", rule: version.startsWith("3.0") ? "P-39" : version.startsWith("3.1") ? "P-38" : "P-38", message: "Server URL has unusable authority" };
    }
    if (url.search || url.hash) {
      return {
        status: version.startsWith("3.0") ? "excluded" : "invalid",
        rule: version.startsWith("3.0") ? "P-39" : version.startsWith("3.1") ? "P-38" : undefined,
        message: "Server URL carries query or fragment bytes",
      };
    }
  } catch {
    if (/^https?:/iu.test(raw.url)) {
      return {
        status: "excluded",
        rule: version.startsWith("3.0") ? "P-39" : "P-38",
        message: "Server URL is not a dispatchable absolute HTTP target",
      };
    }
    // Relative Server URLs are valid; native resolution applies the document base.
  }
  return null;
}

function effectiveServerDeclaration(
  document: OpenAPIDocument,
  pathItem: OpenAPIPathItem,
  operation: OpenAPIOperation,
  selector: string,
): { servers: ServerEntry[]; prefix: string } | null {
  const operationServers = declaredServers(operation.servers);
  if (operationServers.length > 0) return { servers: operationServers, prefix: `${selector}/servers` };
  const pathServers = declaredServers(pathItem.servers);
  if (pathServers.length > 0) {
    return { servers: pathServers, prefix: `${selector.slice(0, selector.lastIndexOf("/"))}/servers` };
  }
  const rootServers = declaredServers(document.servers);
  return rootServers.length > 0 ? { servers: rootServers, prefix: "#/servers" } : null;
}

function declaredServers(raw: unknown): ServerEntry[] {
  return Array.isArray(raw)
    ? raw.filter((value): value is ServerEntry => value !== null && typeof value === "object"
      && !Array.isArray(value))
    : [];
}

function securityAlternativeCoverage(
  document: OpenAPIDocument,
  operation: OpenAPIOperation,
  parameters: ReturnType<typeof effectiveParameters>,
  selector: string,
  operationKey: string,
  bindingSpec: string,
  method: string,
): ProjectionCoverageEntry[] {
  const requirements = effectiveSecurityRequirements(document, operation);
  if (!requirements || requirements.length === 0) return [];
  const prefix = Array.isArray(operation.security) ? `${selector}/security` : "#/security";
  const entries: ProjectionCoverageEntry[] = [];
  for (const [index] of requirements.entries()) {
    const disposition = securityAlternativeDisposition(
      document,
      operation,
      parameters,
      index,
      bindingSpec,
      method,
    );
    if (disposition.status === "represented") continue;
    entries.push({
      sourceIndex: 0,
      sourceRef: `${prefix}/${index}`,
      scope: "alternative",
      status: disposition.status,
      reasonCode: "openapi.security_alternative_unusable",
      ...(disposition.rule ? { rule: openAPIRule(bindingSpec, disposition.rule) } : {}),
      message: disposition.message ?? "security alternative is malformed, unresolved, or collides with an owned request destination",
      ...(disposition.status === "invalid" ? { operationKey, bindingSelector: selector } : {}),
    });
  }
  return entries;
}

function requestMediaCoverage(
  operation: OpenAPIOperation,
  pathItem: OpenAPIPathItem,
  identity: { operationKey: string; selector: string },
  bindingSpec: string,
  openapiVersion: string | undefined,
  verdict: FloorOp | undefined,
): ProjectionCoverageEntry[] {
  const content = operation.requestBody?.content;
  if (!content || Object.keys(content).length === 0) return [];
  const params = effectiveParameters(pathItem, operation);
  const bindingExclusions = requestMediaBindingExclusions(operation, bindingSpec);
  let planError: unknown;
  let plans: ReturnType<typeof planRequestBodies> = [];
  try {
    plans = planRequestBodies(operation, { profile: profileForBindingSpec(bindingSpec), openapiVersion });
  } catch (error: unknown) {
    planError = error;
  }
  // §9.2's normalized collision confines to the colliding parsed identity:
  // the colliding keys are excluded alternatives naming that identity, and the
  // map's non-colliding siblings stay represented beside them.
  const colliding = normalizedMediaCollisions(content, hasMediaFidelity(bindingSpec));
  const planned = new Set(plans.map((plan) => plan.mediaKey));
  const usable = plans
    .filter((plan) => hasRoutedInputs(bindingSpec) || !candidateCollides(params, plan));
  const represented = new Set(usable.map((plan) => plan.mediaKey));
  return Object.keys(content).sort(codePointCompare).map((mediaType): ProjectionCoverageEntry => {
    const sourceRef = `${identity.selector}/requestBody/content/${escapeJSONPointerToken(mediaType)}`;
    const bindingRule = bindingExclusions.get(mediaType);
    const invalidDefects = verdict?.invalidAlternatives.get(sourceRef);
    if (invalidDefects) {
      // The ladder invalidates this alternative: `invalid`, not `excluded`
      // -- the unit is malformed under its upstream authority, not declined
      // by the revision.
      return {
        sourceIndex: 0,
        sourceRef,
        scope: "alternative",
        status: "invalid",
        reasonCode: INVALID_UNIT_REASON_CODE,
        message: floorInvalidAlternativeMessage(invalidDefects.length),
        details: {
          defects: floorDefectDetails(invalidDefects),
          mediaType,
        },
      };
    }
    if (bindingRule) {
      return {
        sourceIndex: 0,
        sourceRef,
        scope: "alternative",
        status: "excluded",
        reasonCode: "openapi.request_media_excluded",
        rule: openAPIRule(bindingSpec, bindingRule),
        message: "request media alternative violates a binding-owned multipart wire constraint",
      };
    }
    if (represented.has(mediaType)) {
      const entry: ProjectionCoverageEntry = {
        sourceIndex: 0,
        sourceRef,
        scope: "alternative",
        status: "represented",
        operationKey: identity.operationKey,
        bindingSelector: identity.selector,
      };
      const requirements: string[] = [];
      if (usable.some((plan) => plan.mediaKey === mediaType && plan.range)) {
        requirements.push("configuration.requestMedia");
      }
      if (usable.some((plan) =>
        plan.mediaKey === mediaType && requiredPropertyMediaNames(plan).length > 0)) {
        requirements.push("configuration.propertyMedia");
      }
      if (requirements.length > 0) entry.requirements = requirements;
      return entry;
    }
    const collision = planned.has(mediaType);
    const collidingIdentity = colliding.get(mediaType);
    let message: string;
    if (collision) {
      message = "request media alternative collides with an independently declared parameter in the candidate's application boundary";
    } else if (collidingIdentity !== undefined) {
      message = `request media alternative denotes the parsed media identity ${collidingIdentity}, which another declaration in this content map also denotes; no selection may land on a normalized-colliding identity`;
    } else {
      message = errorMessage(planError) || "request media alternative has no faithful candidate carriage";
    }
    return {
      sourceIndex: 0,
      sourceRef,
      scope: "alternative",
      status: "excluded",
      reasonCode: collision ? "openapi.flattening_collision" : "openapi.request_media_excluded",
      rule: openAPIRule(bindingSpec, collision ? "P-02" : effectiveRequestBodySynthesisRule(bindingSpec)),
      message,
      details: { mediaType },
    };
  });
}

function escapeJSONPointerToken(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  if (error === undefined) return "";
  try {
    return JSON.stringify(error);
  } catch {
    return "unknown synthesis error";
  }
}
