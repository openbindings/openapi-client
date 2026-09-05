import {
  SWAGGER20_METHODS,
  arrayMember,
  booleanMember,
  isSwagger20Object,
  member,
  objectMember,
  stringMember,
  type Swagger20Document,
  type Swagger20Object,
  type Swagger20ResolvedOperation,
  type Swagger20Resource,
} from "./swagger20-model.js";
import { escapePointerToken } from "./swagger20-reference.js";
import { resolveSwagger20Operation } from "./swagger20-engine.js";
import {
  effectiveSwagger20Parameters,
  type Swagger20Parameter,
  type Swagger20ParameterLocation,
  type Swagger20ParameterSet,
} from "./swagger20-parameters.js";
import {
  effectiveSwagger20MediaSet,
  parseSwagger20ConcreteMedia,
  resolveSwagger20ResponseValue,
  swagger20PayloadFor,
  swagger20RangeHasUsableLane,
  swagger20RequestLane,
  swagger20ResponseLane,
  swagger20ResponsesFor,
  type Swagger20PayloadModel,
} from "./swagger20-media.js";
import { resolveSwagger20SchemaDeclaration } from "./swagger20-schema.js";
import { resolveSwagger20Server } from "./swagger20-server.js";
import { selectSwagger20Security, type Swagger20SecurityCredentials } from "./swagger20-security.js";

export interface Swagger20SynthesisDocument {
  name?: string;
  version?: string;
  description?: string;
  operations: Swagger20SynthesisOperation[];
}

export interface Swagger20SynthesisOperation {
  ref: string;
  path: string;
  method: string;
  operationId?: string;
  description?: string;
  deprecated: boolean;
  tags: string[];
  parameters: Swagger20SynthesisParameter[];
  body?: Swagger20SynthesisBody;
  responses: Swagger20SynthesisResponse[];
  alternatives: Swagger20SynthesisAlternative[];
  security: Swagger20SynthesisSecurityAlternative[];
  requirements: string[];
  excluded: boolean;
  /** Smallest-owner coverage disposition when the operation is not represented. */
  disposition?: "excluded" | "invalid";
  /** Governing portable binding rule, when the family fixes one. */
  rule?: string;
  /** Subordinate declaration facts already classified by native analysis. */
  coverage: Swagger20SynthesisCoverageFact[];
  reason?: string;
}

export interface Swagger20SynthesisCoverageFact {
  sourceRef: string;
  scope: "alternative" | "projection";
  status: "represented" | "excluded" | "invalid" | "lossy";
  rule?: string;
  reason?: string;
  requirements: string[];
}

export interface Swagger20SynthesisParameter {
  name: string;
  in: Swagger20ParameterLocation;
  required: boolean;
  allowEmptyValue: boolean;
  schema: Swagger20Object;
}

export interface Swagger20SynthesisBody { required: boolean; schema: Swagger20Object }
export interface Swagger20SynthesisResponse {
  key: string;
  sourceRef: string;
  schemaPresent: boolean;
  schema?: Swagger20Object;
  canSucceed: boolean;
  usable: boolean;
  reason?: string;
  headers: Swagger20SynthesisResponseHeader[];
}
export interface Swagger20SynthesisResponseHeader {
  name?: string;
  sourceRef: string;
  usable: boolean;
  reason?: string;
  needsContentCodec?: boolean;
}
export interface Swagger20SynthesisAlternative {
  sourceRef: string;
  kind: "requestMedia" | "response" | "security" | "server";
  index?: number;
  usable: boolean;
  disposition?: "excluded" | "invalid";
  rule?: string;
  reason?: string;
  requirements: string[];
}
export interface Swagger20SynthesisSecurityAlternative {
  sourceRef: string;
  index: number;
  anonymous: boolean;
  usable: boolean;
  rule?: string;
  reason?: string;
  schemes: Swagger20SynthesisSecurityScheme[];
}
export interface Swagger20SynthesisSecurityScheme { name: string; type?: string; scopes: string[] }

/** Analyzes every authored fixed operation slot with per-target confinement. */
export async function swagger20SynthesisModel(document: Swagger20Document): Promise<Swagger20SynthesisDocument> {
  const model: Swagger20SynthesisDocument = { operations: [] };
  const info = objectMember(document.root, "info");
  if (info.valid) {
    model.name = stringMember(info.value!, "title").value;
    model.version = stringMember(info.value!, "version").value;
    model.description = stringMember(info.value!, "description").value;
  }
  const paths = objectMember(document.root, "paths");
  if (!paths.valid) return model;
  for (const path of Object.keys(paths.value!).sort()) {
    const rawItem = paths.value![path];
    if (!isSwagger20Object(rawItem)) continue;
    const referenced = member(rawItem, "$ref").present;
    for (const method of SWAGGER20_METHODS) {
      if (!Object.hasOwn(rawItem, method) && !referenced) continue;
      const ref = `#/paths/${escapePointerToken(path)}/${method}`;
      try {
        const operation = await resolveSwagger20Operation(document, ref);
        model.operations.push(await analyzeSwagger20Operation(document, operation, ref));
      } catch (error: unknown) {
        if (!referenced) model.operations.push(excludedOperation(ref, path, method, errorMessage(error)));
      }
    }
  }
  return model;
}

export function analyzePreparedSwagger20Operation(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  ref: string,
): Promise<Swagger20SynthesisOperation> {
  return analyzeSwagger20Operation(document, operation, ref);
}

async function analyzeSwagger20Operation(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  ref: string,
): Promise<Swagger20SynthesisOperation> {
  const result = baseOperation(operation, ref);
  let parameters: Swagger20ParameterSet;
  let responses: Swagger20Object;
  try {
    parameters = await effectiveSwagger20Parameters(operation);
    responses = await swagger20ResponsesFor(operation);
  } catch (error: unknown) { return exclude(result, error); }
    try { resolveSwagger20Server(document, operation); }
  catch (error: unknown) {
    const host = stringMember(document.root, "host");
    if (host.present && (!host.valid || host.value === "")) return exclude(result, error);
    try { resolveSwagger20Server(document, operation, "https://configured.invalid"); }
    catch { return exclude(result, error); }
    const effectiveSchemes = arrayMember(operation.raw, "schemes").present
      ? arrayMember(operation.raw, "schemes")
      : arrayMember(document.root, "schemes");
    if (effectiveSchemes.present) addRequirement(result.requirements, "configuration.server");
  }

  for (const parameter of parameters.nonBody) {
    result.parameters.push({
      name: parameter.name,
      in: parameter.in,
      required: parameter.required,
      allowEmptyValue: parameter.allowEmptyValue,
      schema: parameterSchemaImage(parameter),
    });
    if (parameter.allowEmptyValue) addRequirement(result.requirements, "configuration.emptyValueForm");
    if (parameterNeedsConversion(parameter)) addRequirement(result.requirements, "configuration.parameterConversion");
    if (parameter.in === "header" && parameter.name.toLowerCase() === "content-encoding"
      && codingDeclarationNeedsCodec(parameter.raw)) addRequirement(result.requirements, "configuration.requestContentCodings");
  }
  result.coverage.push(...omittedOptionalHeaderFacts(operation, ref));
  if (parameters.body) {
    try {
      result.body = {
        required: parameters.body.required,
        schema: await materializeSwagger20Schema(operation.graph, parameters.body.raw.schema, parameters.body.resource),
      };
    } catch (error: unknown) { return exclude(result, error); }
  }

  let payload: Swagger20PayloadModel;
  try { payload = await swagger20PayloadFor(parameters, operation); }
  catch (error: unknown) { return exclude(result, error); }
  let consumes;
  try { consumes = effectiveSwagger20MediaSet(document, operation, "consumes"); }
  catch (error: unknown) { return exclude(result, error); }
  const consumesPrefix = arrayMember(operation.raw, "consumes").present ? `${ref}/consumes` : "#/consumes";
  let usableConsumes = 0;
  let soleConcrete = false;
  for (const [index, entry] of consumes.entries.entries()) {
    if (!payload.kind) break;
    const alternative: Swagger20SynthesisAlternative = {
      sourceRef: `${consumesPrefix}/${index}`, kind: "requestMedia", index, usable: false, requirements: [],
    };
    if (entry.error) alternative.reason = entry.error.message;
    else if (entry.colliding) alternative.reason = "media declaration collides after normalized identity comparison";
    else if (entry.parsed!.specificity < 2) {
      alternative.usable = swagger20RangeHasUsableLane(entry.parsed!, payload);
      if (alternative.usable) addRequirement(alternative.requirements, "configuration.requestMedia");
      else alternative.reason = "media range selects no usable request carriage lane";
    } else {
      try { swagger20RequestLane(entry.parsed!, payload); alternative.usable = true; }
      catch (error: unknown) { alternative.reason = errorMessage(error); }
    }
    if (alternative.usable) {
      usableConsumes++;
      soleConcrete = usableConsumes === 1 && entry.parsed!.specificity === 2;
      if (entry.parsed!.base === "multipart/form-data" && payload.form.some((parameter) => parameter.typeName === "file")) {
        addRequirement(alternative.requirements, "configuration.propertyMedia");
      }
    }
    result.alternatives.push(alternative);
  }
  if (payload.kind) {
    const required = payload.body?.required === true || payload.form.some((parameter) => parameter.required);
    if (usableConsumes === 0) {
      if (required) return exclude(result, "required request payload has no usable effective consumes alternative");
      result.body = undefined;
      result.parameters = result.parameters.filter((parameter) => parameter.in !== "formData");
    } else {
      if (usableConsumes !== 1 || !soleConcrete) {
        for (const alternative of result.alternatives) if (alternative.kind === "requestMedia" && alternative.usable) {
          addRequirement(alternative.requirements, "configuration.requestMedia");
        }
        if (required) addRequirement(result.requirements, "configuration.requestMedia");
      }
      if (required && payload.form.some((parameter) => parameter.required && parameter.typeName === "file")) {
        addRequirement(result.requirements, "configuration.propertyMedia");
      }
    }
  }
  if (!payload.kind && result.parameters.some((parameter) =>
    parameter.in === "header" && parameter.required && parameter.name.toLowerCase() === "content-encoding")) {
    result.excluded = true;
    result.disposition = "excluded";
    result.rule = "OAPI20-P-27";
    result.reason = "required Content-Encoding parameter has no surviving request payload lane";
    result.requirements.length = 0;
    return result;
  }

  result.security = analyzeSecurity(document, operation, parameters, ref);
  if (result.security.length > 0 && !result.security.some((alternative) => alternative.usable)) {
    return exclude(result, "effective security declaration has no usable complete alternative");
  }
  if (result.security.filter((alternative) => alternative.usable).length > 1
    || (result.security.length > 1 && result.security.every((alternative) => alternative.sourceRef.startsWith("#/security/")))) {
    addRequirement(result.requirements, "configuration.security");
  }
  for (const alternative of result.security) if (!alternative.usable) result.alternatives.push({
    sourceRef: alternative.sourceRef, kind: "security", index: alternative.index,
    usable: false, reason: alternative.reason, requirements: [],
  });
  result.alternatives.push(...serverAlternatives(document, operation, ref));

  result.responses = await analyzeResponses(document, operation, responses, ref);
  for (const response of result.responses) if (response.schemaPresent && !response.usable) result.alternatives.push({
    sourceRef: response.sourceRef, kind: "response", usable: false, reason: response.reason, requirements: [],
  });
  if (responsesUseContentCoding(result.responses)) addRequirement(result.requirements, "configuration.responseContentCodings");
  result.coverage.push(...responseProjectionFacts(result));
  result.coverage.push(...responseMediaFacts(document, operation, parameters, ref));
  result.requirements.sort();
  return result;
}

function baseOperation(operation: Swagger20ResolvedOperation, ref: string): Swagger20SynthesisOperation {
  const description = stringMember(operation.raw, "description");
  const summary = stringMember(operation.raw, "summary");
  const tags = arrayMember(operation.raw, "tags");
  return {
    ref, path: operation.path, method: operation.method,
    operationId: stringMember(operation.raw, "operationId").value,
    description: description.valid && description.value !== "" ? description.value : summary.value,
    deprecated: booleanMember(operation.raw, "deprecated").value === true,
    tags: tags.valid ? tags.value!.filter((tag): tag is string => typeof tag === "string") : [],
    parameters: [], responses: [], alternatives: [], security: [], requirements: [], coverage: [], excluded: false,
  };
}

function excludedOperation(ref: string, path: string, method: string, reason: string): Swagger20SynthesisOperation {
  const classification = classifyTargetFailure(reason);
  return {
    ref, path, method, deprecated: false, tags: [], parameters: [], responses: [], alternatives: [], security: [],
    requirements: [], coverage: [], excluded: true, disposition: classification.disposition,
    ...(classification.rule ? { rule: classification.rule } : {}), reason,
  };
}

function exclude(result: Swagger20SynthesisOperation, error: unknown): Swagger20SynthesisOperation {
  result.excluded = true;
  result.reason = errorMessage(error);
  const classification = classifyTargetFailure(result.reason);
  result.disposition = classification.disposition;
  result.rule = classification.rule;
  return result;
}

const PARAMETER_SCHEMA_KEYS = [
  "type", "format", "default", "multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum",
  "maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems", "enum", "items",
] as const;

function parameterSchemaImage(parameter: Swagger20Parameter): Swagger20Object {
  const schema: Swagger20Object = {};
  for (const key of PARAMETER_SCHEMA_KEYS) if (Object.hasOwn(parameter.raw, key)) schema[key] = structuredClone(parameter.raw[key]);
  return schema;
}

function parameterNeedsConversion(parameter: Swagger20Parameter): boolean {
  if (parameter.typeName !== "array") return parameter.typeName !== "string" && parameter.typeName !== "file";
  let type = parameter.items?.typeName;
  let items = parameter.items?.items;
  while (type === "array") { type = items?.typeName; items = items?.items; }
  return type !== undefined && type !== "string";
}

function analyzeSecurity(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  parameters: Swagger20ParameterSet,
  ref: string,
): Swagger20SynthesisSecurityAlternative[] {
  let member = arrayMember(operation.raw, "security");
  let prefix = `${ref}/security`;
  if (!member.present) { member = arrayMember(document.root, "security"); prefix = "#/security"; }
  if (!member.present) return [];
  if (!member.valid) return [{ sourceRef: `${prefix}/0`, index: 0, anonymous: false, usable: false, reason: "effective security field is not an array", schemes: [] }];
  const definitions = objectMember(document.root, "securityDefinitions");
  return member.value!.map((raw, index) => {
    const alternative: Swagger20SynthesisSecurityAlternative = {
      sourceRef: `${prefix}/${index}`, index, anonymous: false, usable: false, schemes: [],
    };
    if (!isSwagger20Object(raw)) { alternative.reason = "Security Requirement is not an object"; return alternative; }
    alternative.anonymous = Object.keys(raw).length === 0;
    const credentials: Swagger20SecurityCredentials = { basic: {}, apiKeys: {}, oauth2: {} };
    for (const name of Object.keys(raw).sort()) {
      const scopes = Array.isArray(raw[name]) ? (raw[name] as unknown[]).filter((scope): scope is string => typeof scope === "string") : [];
      const definition = definitions.valid && isSwagger20Object(definitions.value![name]) ? definitions.value![name] as Swagger20Object : undefined;
      const type = definition ? stringMember(definition, "type").value : undefined;
      alternative.schemes.push({ name, type, scopes });
      if (type === "basic") credentials.basic![name] = { userId: "", password: "" };
      else if (type === "apiKey") credentials.apiKeys![name] = "";
      else if (type === "oauth2") credentials.oauth2![name] = { accessToken: "token", scopes };
    }
    try { selectSwagger20Security(document, operation, parameters, index, credentials); alternative.usable = true; }
    catch (error: unknown) {
      alternative.reason = errorMessage(error);
      if (alternative.reason.includes("collides")
        && securityHasRequiredFixedCollision(alternative, document, parameters)) {
        alternative.rule = "OAPI20-P-19";
      }
    }
    return alternative;
  });
}

function serverAlternatives(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  ref: string,
): Swagger20SynthesisAlternative[] {
  let member = arrayMember(operation.raw, "schemes");
  let prefix = `${ref}/schemes`;
  if (!member.present) { member = arrayMember(document.root, "schemes"); prefix = "#/schemes"; }
  if (!member.valid) return [];
  return member.value!.map((raw, index) => {
    const usable = raw === "http" || raw === "https";
    const invalid = raw !== "http" && raw !== "https" && raw !== "ws" && raw !== "wss";
    return {
      sourceRef: `${prefix}/${index}`, kind: "server" as const, index, usable, requirements: [],
      ...(usable ? {} : {
        reason: `effective scheme ${JSON.stringify(raw)} is unusable`,
        disposition: invalid ? "invalid" as const : "excluded" as const,
        rule: invalid ? "OAPI20-S-11" : "OAPI20-P-04",
      }),
    };
  });
}

async function analyzeResponses(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  responses: Swagger20Object,
  ref: string,
): Promise<Swagger20SynthesisResponse[]> {
  let produces;
  let producesError: string | undefined;
  try { produces = effectiveSwagger20MediaSet(document, operation, "produces"); }
  catch (error: unknown) { producesError = errorMessage(error); }
  const result: Swagger20SynthesisResponse[] = [];
  for (const key of Object.keys(responses).filter((key) => key === "default" || /^[1-5][0-9][0-9]$/u.test(key)).sort()) {
    const entry: Swagger20SynthesisResponse = {
      key, sourceRef: `${ref}/responses/${escapePointerToken(key)}`, schemaPresent: false,
      canSucceed: key === "default" || key.startsWith("2"), usable: false, headers: [],
    };
    let resolved;
    try { resolved = await resolveSwagger20ResponseValue(operation, responses[key], operation.resource, key); }
    catch (error: unknown) { entry.reason = errorMessage(error); result.push(entry); continue; }
    entry.headers = analyzeResponseHeaders(resolved.raw, entry.sourceRef);
    const description = stringMember(resolved.raw, "description");
    if (!description.valid) {
      entry.reason = "Response Object requires a string description";
      result.push(entry);
      continue;
    }
    entry.schemaPresent = Object.hasOwn(resolved.raw, "schema");
    if (!entry.schemaPresent) { entry.usable = true; result.push(entry); continue; }
    try { entry.schema = await materializeSwagger20Schema(operation.graph, resolved.raw.schema, resolved.resource); }
    catch (error: unknown) { entry.reason = errorMessage(error); result.push(entry); continue; }
    if (producesError) { entry.reason = producesError; result.push(entry); continue; }
    try {
      const declaration = await resolveSwagger20SchemaDeclaration(operation.graph, resolved.raw.schema, resolved.resource, true);
      for (const media of produces!.entries) {
        if (!media.parsed || media.colliding) continue;
        const candidates = media.parsed.specificity === 2 ? [media.parsed] : [
          "application/json", "text/plain", "application/octet-stream", "image/png",
        ].map(parseSwagger20ConcreteMedia).filter((candidate) => declarationMatches(media.parsed!, candidate));
        if (candidates.some((candidate) => { try { swagger20ResponseLane(candidate, declaration); return true; } catch { return false; } })) {
          entry.usable = true;
          break;
        }
      }
    } catch (error: unknown) { entry.reason = errorMessage(error); }
    if (!entry.usable && !entry.reason) entry.reason = "response schema and effective produces define no usable response carriage lane";
    result.push(entry);
  }
  return result;
}

function analyzeResponseHeaders(response: Swagger20Object, responseRef: string): Swagger20SynthesisResponseHeader[] {
  const headers = objectMember(response, "headers");
  if (!headers.present) return [];
  if (!headers.valid) return [{ sourceRef: `${responseRef}/headers`, usable: false, reason: "Response headers is not an object" }];
  const identities = new Map<string, number>();
  for (const name of Object.keys(headers.value!)) identities.set(name.toLowerCase(), (identities.get(name.toLowerCase()) ?? 0) + 1);
  return Object.keys(headers.value!).sort().map((name) => {
    const entry: Swagger20SynthesisResponseHeader = { name, sourceRef: `${responseRef}/headers/${escapePointerToken(name)}`, usable: false };
    const raw = headers.value![name];
    const object = isSwagger20Object(raw) ? raw : undefined;
    if (!object) entry.reason = "response Header Object is not an object";
    else if (identities.get(name.toLowerCase())! > 1) entry.reason = "response Header Object name collides under ASCII case-insensitive identity";
    else if (!httpFieldName(name)) entry.reason = "response header field name is not an HTTP field-name";
    else entry.reason = responseHeaderDefect(object);
    entry.usable = entry.reason === undefined;
    if (entry.usable && name.toLowerCase() === "content-encoding") entry.needsContentCodec = codingDeclarationNeedsCodec(object!);
    return entry;
  });
}

function responseHeaderDefect(raw: Swagger20Object): string | undefined {
  const type = stringMember(raw, "type");
  if (!type.valid) return "response Header Object requires string type";
  if (type.value === "array") {
    if (!objectMember(raw, "items").valid) return "array response Header Object requires an Items Object";
    const collection = stringMember(raw, "collectionFormat");
    if (collection.present && (!collection.valid || !["csv", "ssv", "tsv", "pipes"].includes(collection.value!))) {
      return `response Header Object collectionFormat ${JSON.stringify(collection.value)} is not admitted`;
    }
  } else if (!["string", "number", "integer", "boolean"].includes(type.value!)) {
    return `response Header Object type ${JSON.stringify(type.value)} is not admitted`;
  } else if (stringMember(raw, "collectionFormat").present) return "response Header Object collectionFormat applies only to arrays";
  return undefined;
}

function responsesUseContentCoding(responses: Swagger20SynthesisResponse[]): boolean {
  return responses.some((response) => response.headers.some((header) =>
    header.name?.toLowerCase() === "content-encoding" && header.usable && header.needsContentCodec === true));
}

function codingDeclarationNeedsCodec(raw: Swagger20Object): boolean {
  if (!Array.isArray(raw.enum) || raw.enum.length === 0) return true;
  return raw.enum.some((value) => typeof value !== "string" || value.toLowerCase() !== "identity");
}

function classifyTargetFailure(reason: string): { disposition: "excluded" | "invalid"; rule?: string } {
  const lower = reason.toLowerCase();
  if (lower.includes("inadmissible key")) return { disposition: "invalid" };
  if (/effective path parameter .* no matching template expression/u.test(lower)) return { disposition: "invalid" };
  if (lower.includes("responses object has no exact status or default response")) {
    return { disposition: "invalid", rule: "OAPI20-P-05" };
  }
  if (lower.includes("header name is not an http field-name")) return { disposition: "excluded", rule: "OAPI20-P-12" };
  if (lower.includes("processor-owned field")) return { disposition: "excluded", rule: "OAPI20-P-13" };
  if (lower.includes("no effective path parameter")) return { disposition: "excluded", rule: "OAPI20-P-02" };
  if (lower.includes("content-encoding") && lower.includes("payload")) return { disposition: "excluded", rule: "OAPI20-P-27" };
  if (/response|consumes|produces|payload/u.test(lower)) return { disposition: "excluded", rule: "OAPI20-P-03" };
  if (/security|scheme|host|server/u.test(lower)) return { disposition: "excluded", rule: "OAPI20-P-04" };
  if (/parameter|path template/u.test(lower)) return { disposition: "excluded", rule: "OAPI20-P-02" };
  return { disposition: "excluded", rule: "OAPI20-P-01" };
}

function omittedOptionalHeaderFacts(operation: Swagger20ResolvedOperation, ref: string): Swagger20SynthesisCoverageFact[] {
  const facts: Swagger20SynthesisCoverageFact[] = [];
  const scopes: Array<{ member: ReturnType<typeof arrayMember>; prefix: string }> = [
    { member: arrayMember(operation.pathItem.raw, "parameters"), prefix: `${ref.slice(0, ref.lastIndexOf("/"))}/parameters` },
    { member: arrayMember(operation.raw, "parameters"), prefix: `${ref}/parameters` },
  ];
  const rawNames = new Map<string, Set<string>>();
  for (const { member: declaration } of scopes) {
    if (!declaration.valid) continue;
    for (const raw of declaration.value!) {
      if (!isSwagger20Object(raw)) continue;
      const name = stringMember(raw, "name").value;
      const location = stringMember(raw, "in").value;
      if (!name || !location) continue;
      const locations = rawNames.get(name) ?? new Set<string>();
      locations.add(location);
      rawNames.set(name, locations);
    }
  }
  for (const { member: declaration, prefix } of scopes) {
    if (!declaration.valid) continue;
    declaration.value!.forEach((raw, index) => {
      if (!isSwagger20Object(raw) || stringMember(raw, "in").value !== "header"
        || booleanMember(raw, "required").value === true) return;
      const name = stringMember(raw, "name").value;
      if (name && !httpFieldName(name)) facts.push({
        sourceRef: `${prefix}/${index}`, scope: "projection", status: "excluded",
        rule: (rawNames.get(name)?.size ?? 0) > 1 ? "OAPI20-S-10" : "OAPI20-P-10",
        reason: "optional non-token header parameter is confined from the effective input surface", requirements: [],
      });
    });
  }
  return facts;
}

function securityHasRequiredFixedCollision(
  alternative: Swagger20SynthesisSecurityAlternative,
  document: Swagger20Document,
  parameters: Swagger20ParameterSet,
): boolean {
  const definitions = objectMember(document.root, "securityDefinitions");
  if (!definitions.valid) return false;
  for (const scheme of alternative.schemes) {
    const definition = definitions.value![scheme.name];
    if (!isSwagger20Object(definition) || stringMember(definition, "type").value !== "apiKey") continue;
    const location = stringMember(definition, "in").value;
    const name = stringMember(definition, "name").value;
    if (!name || (location !== "header" && location !== "query")) continue;
    if (parameters.nonBody.some((parameter) => parameter.required && parameter.in === location
      && (location === "header" ? parameter.name.toLowerCase() === name.toLowerCase() : parameter.name === name))) {
      return true;
    }
  }
  return false;
}

function responseProjectionFacts(operation: Swagger20SynthesisOperation): Swagger20SynthesisCoverageFact[] {
  const facts: Swagger20SynthesisCoverageFact[] = [];
  for (const response of operation.responses) {
    if (!response.canSucceed && response.reason) facts.push({
      sourceRef: response.sourceRef, scope: "projection", status: "invalid", rule: "OAPI20-P-07",
      reason: response.reason, requirements: [],
    });
    if (response.schemaPresent && ["204", "205", "304"].includes(response.key)) facts.push({
      sourceRef: `${response.sourceRef}/schema`, scope: "projection", status: "excluded", rule: "OAPI20-S-03",
      reason: `response ${response.key} cannot carry response content`, requirements: [],
    });
    if (response.key === "default" && operation.responses.length === 1 && response.schemaPresent && response.usable) facts.push({
      sourceRef: `${response.sourceRef}/schema`, scope: "projection", status: "represented", requirements: [],
    });
  }
  return facts;
}

function responseMediaFacts(
  document: Swagger20Document,
  operation: Swagger20ResolvedOperation,
  parameters: Swagger20ParameterSet,
  ref: string,
): Swagger20SynthesisCoverageFact[] {
  let produces;
  try { produces = effectiveSwagger20MediaSet(document, operation, "produces"); }
  catch { return []; }
  const hasQ = produces.entries.some((entry) => entry.parsed && Object.hasOwn(entry.parsed.params, "q"));
  const callerOwnsAccept = parameters.nonBody.some((parameter) =>
    parameter.in === "header" && parameter.name.toLowerCase() === "accept");
  if (!hasQ) return [];
  const prefix = arrayMember(operation.raw, "produces").present ? `${ref}/produces` : "#/produces";
  return produces.entries.map((entry, index) => {
    const qBearing = entry.parsed && Object.hasOwn(entry.parsed.params, "q");
    if (qBearing && !callerOwnsAccept) return {
      sourceRef: `${prefix}/${index}`, scope: "alternative" as const, status: "excluded" as const,
      rule: "OAPI20-S-12", reason: "generated Accept does not admit q media parameters", requirements: [],
    };
    return { sourceRef: `${prefix}/${index}`, scope: "alternative" as const, status: "represented" as const, requirements: [] };
  });
}

function httpFieldName(value: string): boolean {
  return /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/u.test(value);
}

function declarationMatches(declaration: { base: string; params: Record<string, string> }, concrete: { base: string; params: Record<string, string> }): boolean {
  const [declaredType, declaredSubtype] = declaration.base.split("/");
  const [actualType, actualSubtype] = concrete.base.split("/");
  return (declaredType === "*" || declaredType === actualType) && (declaredSubtype === "*" || declaredSubtype === actualSubtype)
    && Object.entries(declaration.params).every(([name, value]) => concrete.params[name] === value);
}

async function materializeSwagger20Schema(
  graph: Swagger20ResolvedOperation["graph"],
  value: unknown,
  resource: Swagger20Resource,
): Promise<Swagger20Object> {
  const defs: Swagger20Object = {};
  const names = new Map<string, string>();
  let next = 0;
  const schema = async (rawValue: unknown, rawResource: Swagger20Resource): Promise<Swagger20Object> => {
    if (!isSwagger20Object(rawValue)) throw new Error("Schema Object is not an object");
    const reference = stringMember(rawValue, "$ref");
    if (reference.present) {
      if (!reference.valid || reference.value === "") throw new Error("Schema Object has an invalid $ref");
      const key = `${rawResource.retrieval ?? rawResource.requested ?? ""}\u0000${reference.value}`;
      const known = names.get(key);
      if (known) return { $ref: `#/$defs/${escapePointerToken(known)}` };
      const name = `schema${next++}`;
      names.set(key, name);
      defs[name] = {};
      const resolved = await graph.resolveReference(reference.value!, rawResource);
      defs[name] = await schema(resolved.node, resolved.resource);
      return { $ref: `#/$defs/${escapePointerToken(name)}` };
    }
    const result: Swagger20Object = {};
    for (const key of Object.keys(rawValue).sort()) {
      const child = rawValue[key];
      if ((key === "items" || key === "additionalProperties") && isSwagger20Object(child)) result[key] = await schema(child, rawResource);
      else if ((key === "properties" || key === "definitions") && isSwagger20Object(child)) {
        const projected: Swagger20Object = {};
        for (const name of Object.keys(child).sort()) projected[name] = await schema(child[name], rawResource);
        result[key] = projected;
      } else if (key === "allOf" && Array.isArray(child)) result[key] = await Promise.all(child.map((branch) => schema(branch, rawResource)));
      else result[key] = structuredClone(child);
    }
    return result;
  };
  const root = await schema(value, resource);
  if (Object.keys(defs).length > 0) root.$defs = defs;
  return root;
}

function addRequirement(values: string[], value: string): void { if (!values.includes(value)) values.push(value); }
function errorMessage(error: unknown): string { return error instanceof Error ? error.message : String(error); }
